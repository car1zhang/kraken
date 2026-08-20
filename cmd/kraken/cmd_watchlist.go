package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"

	"kraken/internal/util"
)

// cmd_watchlist.go — `k watchlist get|add|update|remove ...`: the dream
// loop's own read/write surface for the watchlist (the running record of
// observations the nightly pass is tracking), proxying to kraken serve's
// GET /api/watchlist and POST /api/watchlist/item (internal/api/watchlist.go).
// The agent learns these verbs from .claude/skills/dream/SKILL.md; the
// spellings here match it.
//
// A watch item is a row in its own `watch_items` table: the observation is
// the row's own sentence, and the row carries the dream-loop workflow (class,
// state, sightings, evidence, suggested_fix, pr_url) plus carl's verdict (see
// store/watch.go). Three consequences reach this file, all of them the
// server's rules rather than local ones:
//
//   - Every `id` here is a WATCH-ROW id. It briefly was a graph edge id (the
//     watchlist lived in the knowledge graph for one release); Q9 moved it
//     back out, since the graph is now compiled from carl's own mental-model
//     marks rather than from anything an agent observed.
//   - `add` may name endpoints — src, dst and an optional predicate beside
//     observation and class — but none of them is required any more, and
//     there is no `strength`: that was the edge's confidence and the row has
//     no such column. Re-adding an observation already tracked folds into it
//     server-side — a sighting bump, not a second row — so there is no
//     find-then-bump dance to run here.
//   - `remove` CLOSES the row: it leaves every list and stays readable by id,
//     carl's verdict on it included. No watch row is ever deleted.
//
// Two more things make this verb family different from `k questions`/`k
// feedback`, and both are load-bearing:
//
//   - No slug. Watch items are GLOBAL — they belong to no job, carry no
//     job_id, and outlive whichever job first surfaced the pattern. Every op
//     here is unscoped.
//   - The writes go to /api/watchlist/item, NOT /api/watchlist. The plain
//     route is CARL's: verdict and comment, which he sets from the board.
//     `verdict`/`comment`/`commented_at` in a request here are refused with a
//     400 by the server rather than dropped, so an agent that echoes a whole
//     row back from `get` is told what it did wrong instead of believing a
//     write landed.
//
// Behavior contract, identical to cmd_questions.go's (all four ops):
//   - success: `get` prints the server's JSON to stdout verbatim, exit 0; a
//     write prints one short confirmation line to STDERR (stdout stays
//     parseable), exit 0.
//   - a 400/404 (bad class, unknown id, a carl-owned key, malformed body)
//     prints the server's own error message to stderr, exit non-zero.
//   - kraken serve unreachable: a stderr message naming the address this
//     process tried, exit non-zero.
//   - bad/missing args: a usage message to stderr, exit non-zero.
//
// parse/run are split from cmdWatchlist so the exit-code paths are testable
// without going through util.Die's os.Exit — same shape as cmd_questions.go.
func cmdWatchlist(args []string) {
	if len(args) == 0 {
		util.Die("usage: k watchlist get|add|update|remove ...")
	}
	op, rest := args[0], args[1:]
	switch op {
	case "get":
		out, err := runWatchlistGet(rest)
		if err != nil {
			util.Die("%v", err)
		}
		if _, err := os.Stdout.Write(out); err != nil {
			util.Die("write stdout: %v", err)
		}
	case "add", "update":
		body, err := runWatchlistUpsert(op, rest, os.Stdin)
		if err != nil {
			util.Die("%v", err)
		}
		// the row id is echoed on stderr because it is the ONLY way a caller
		// learns the id of the item it just wrote without re-reading the whole
		// list — and every later `update` needs it. On a fold it is the id of
		// the EXISTING item the observation landed on, which is the answer a
		// caller wants either way.
		if id := watchlistRespID(body); id != 0 {
			fmt.Fprintf(os.Stderr, "watchlist %s ok (id %d)\n", op, id)
		} else {
			fmt.Fprintf(os.Stderr, "watchlist %s ok\n", op)
		}
	case "remove":
		if err := runWatchlistRemove(rest); err != nil {
			util.Die("%v", err)
		}
		fmt.Fprintf(os.Stderr, "watchlist remove ok (id %s)\n", strings.TrimSpace(rest[0]))
	default:
		util.Die("usage: k watchlist get|add|update|remove ... (unknown op %q)", op)
	}
}

// watchlistRespID pulls the row id out of a write's {"ok":true,"id":N}
// response. Best-effort by design: a missing/odd body costs the caller the
// nicer confirmation line, never the exit code — the write already succeeded
// by the time this runs.
func watchlistRespID(body []byte) int64 {
	var resp struct {
		ID int64 `json:"id"`
	}
	if json.Unmarshal(body, &resp) != nil {
		return 0
	}
	return resp.ID
}

// runWatchlistGet is `kraken watchlist get [state]`: GET /api/watchlist —
// prints {"watchlist": [...]} straight through. The optional positional is
// the state filter (watching|shipped|declined); omitted means every row,
// whatever its state. An unrecognized state is the SERVER's 400, not a local
// check — one validation, one place, same thin-proxy rule the questions verbs
// follow.
func runWatchlistGet(args []string) ([]byte, error) {
	switch len(args) {
	case 0:
		return qfGet("/api/watchlist")
	case 1:
		state := strings.TrimSpace(args[0])
		if state == "" {
			return nil, fmt.Errorf("usage: k watchlist get [watching|shipped|declined]")
		}
		return qfGet("/api/watchlist?state=" + url.QueryEscape(state))
	default:
		return nil, fmt.Errorf("usage: k watchlist get [watching|shipped|declined]")
	}
}

// runWatchlistUpsert is `kraken watchlist add|update`: reads ONE watch-item
// object from stdin (stdin-based, not shell args, for the same reason
// questions add/update are — an observation is prose full of quotes and
// unicode that shell-quoting mangles) and posts {op, item}.
//
// add takes {observation, class, [src], [dst], [predicate], [state],
// [evidence], [suggested_fix], [pr_url]}; update takes {id, ...} and refuses
// the observation's own identity fields (observation/src/dst/predicate) — the
// fold matches on the sentence, so reword by removing and re-adding.
//
// The object is forwarded as raw JSON rather than decoded into a local
// struct: this verb is a thin proxy, not a second copy of the wire schema —
// internal/api/watchlist.go's watchItemWire is the single source of truth for
// what a watch item looks like, including which keys are refused. The only
// local check is "stdin is a JSON object", which is a friendlier error than a
// server-side "bad json" 400 for plainly malformed stdin.
func runWatchlistUpsert(op string, args []string, stdin io.Reader) ([]byte, error) {
	if len(args) != 0 {
		return nil, fmt.Errorf("usage: k watchlist %s (JSON watch-item object on stdin — add: observation, class, optional src/dst/predicate; update: id plus the workflow fields; no slug — the watchlist is global)", op)
	}
	if stdinIsInteractiveTTY(stdin) {
		return nil, fmt.Errorf("usage: k watchlist %s (JSON watch-item object on stdin) — refusing to wait on an interactive terminal; pipe the JSON in instead", op)
	}
	raw, err := io.ReadAll(stdin)
	if err != nil {
		return nil, fmt.Errorf("read stdin: %w", err)
	}
	var probe map[string]any
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, fmt.Errorf("stdin is not a JSON watch-item object: %w", err)
	}
	if probe == nil {
		return nil, fmt.Errorf("stdin is not a JSON watch-item object: got null")
	}
	body := struct {
		Op   string          `json:"op"`
		Item json.RawMessage `json:"item"`
	}{op, json.RawMessage(raw)}
	return qfPost("/api/watchlist/item", body)
}

// runWatchlistRemove is `kraken watchlist remove <id>`: an id carries no
// quoting hazard, so it's a plain arg rather than stdin — same split
// `k questions remove <slug> <qid>` makes.
//
// It CLOSES the row: the dream loop stops tracking the observation, and the
// row stays readable by id with carl's verdict on it intact. Nothing here can
// delete a watch row.
func runWatchlistRemove(args []string) error {
	if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
		return fmt.Errorf("usage: k watchlist remove <id> (stops tracking the observation; the row stays readable by id)")
	}
	id, err := strconv.ParseInt(strings.TrimSpace(args[0]), 10, 64)
	if err != nil {
		return fmt.Errorf("bad watch item id %q: %w", args[0], err)
	}
	_, err = qfPost("/api/watchlist/item", map[string]any{"op": "remove", "id": id})
	return err
}
