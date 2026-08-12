package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"kraken/internal/util"
)

// cmd_graph.go — `k graph pending|add|done|search|get|stats`: the cell-side
// verbs for the knowledge graph, proxying to kraken serve's /api/graph*
// routes (internal/api/graph.go). `pending`, `add` and `done` are the
// extraction agent's loop — take a batch, write back what it found, close
// each episode it processed; `search` is the retrieval read any agent can
// use; `get` dereferences one injected `[F# E#]` handle back to the full
// fact (or the episode) it was cut down from; `stats` is the growth curve.
//
// The six op names and their flags are the spellings
// .claude/skills/graph/SKILL.md hands the extraction agent, so they are a
// contract with that file, not a local naming choice.
//
// Behavior contract, identical to cmd_load.go's and cmd_questions.go's:
//   - success: a read prints the server's JSON to stdout verbatim, exit 0; a
//     write prints one short confirmation line to STDERR, exit 0.
//   - a 4xx prints the server's own error message to stderr, exit non-zero —
//     never a raw HTTP dump.
//   - kraken serve unreachable: a stderr message naming the address this
//     process tried, exit non-zero — never silently succeeds.
//   - bad/missing args: a usage message to stderr, exit non-zero.
//
// `add` is the one op that breaks the "writes say nothing on stdout" half of
// that contract, and deliberately: its response body IS the payload the
// caller needs — the node and edge ids it just created, which nothing else
// can tell it — so the JSON goes to stdout and the human-readable
// confirmation still goes to stderr. stdout stays parseable either way.
//
// It NEVER opens kraken.db: only kraken serve writes it (single-writer
// rule), so every op here is exactly one HTTP round trip through
// qfGet/qfPost.
func cmdGraph(args []string) {
	if len(args) == 0 {
		util.Die("usage: k graph pending|add|done|search|get|stats ...")
	}
	op, rest := args[0], args[1:]
	switch op {
	case "pending":
		out, err := runGraphPending(rest)
		if err != nil {
			util.Die("%v", err)
		}
		writeStdout(out)
	case "add":
		out, err := runGraphAdd(rest, os.Stdin)
		if err != nil {
			util.Die("%v", err)
		}
		writeStdout(out)
		fmt.Fprintln(os.Stderr, "graph add accepted — ids are in the JSON above")
	case "done":
		id, err := runGraphDone(rest)
		if err != nil {
			util.Die("%v", err)
		}
		fmt.Fprintf(os.Stderr, "episode %d closed\n", id)
	case "search":
		text, err := runGraphSearch(rest)
		if err != nil {
			util.Die("%v", err)
		}
		fmt.Print(text)
	case "get":
		if err := runGraphGet(rest, os.Stdout); err != nil {
			util.Die("%v", err)
		}
	case "stats":
		out, err := runGraphStats(rest)
		if err != nil {
			util.Die("%v", err)
		}
		writeStdout(out)
	default:
		util.Die("usage: k graph pending|add|done|search|get|stats ... (unknown op %q)", op)
	}
}

// writeStdout prints a server response byte-for-byte. Kept as one helper so
// no op silently re-encodes the server's JSON on the way through — a
// re-marshal would reorder keys and drop the server's own formatting for no
// gain.
func writeStdout(out []byte) {
	if _, err := os.Stdout.Write(out); err != nil {
		util.Die("write stdout: %v", err)
	}
}

// parseGraphLimit reads an optional trailing `--limit N` / `--limit=N`. A
// mistyped flag is a usage error rather than a silently ignored argument, so
// `--limt 5` can't quietly fetch the default 20 and look like it worked.
func parseGraphLimit(args []string, usage string) (int, error) {
	limit := 0
	for i := 0; i < len(args); i++ {
		name, raw, inline := strings.Cut(args[i], "=")
		if !inline {
			name = args[i]
			if i+1 >= len(args) && name == "--limit" {
				return 0, fmt.Errorf("%s (--limit needs a value)", usage)
			}
		}
		if name != "--limit" {
			// Checked BEFORE consuming a value, so the message always names
			// the offending flag — a post-increment re-test here once
			// reported the flag's VALUE as the unexpected argument.
			return 0, fmt.Errorf("%s (unexpected argument %q)", usage, name)
		}
		if !inline {
			raw = args[i+1]
			i++
		}
		n, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("bad --limit %q: want a positive whole number (%s)", raw, usage)
		}
		limit = n
	}
	return limit, nil
}

const graphPendingUsage = "usage: k graph pending [--limit N]"

// runGraphPending is `k graph pending [--limit N]`: GET /api/graph/pending —
// prints {"episodes": [...]} straight through, oldest first, so the
// extraction agent drains the backlog in ingest order.
func runGraphPending(args []string) ([]byte, error) {
	limit, err := parseGraphLimit(args, graphPendingUsage)
	if err != nil {
		return nil, err
	}
	path := "/api/graph/pending"
	if limit > 0 {
		path += "?limit=" + strconv.Itoa(limit)
	}
	return qfGet(path)
}

const graphAddUsage = "usage: k graph add [--episode <id>] [--json <path>]  (payload on stdin when --json is omitted: {episode_id?, nodes[], edges[], invalidate[]})"

// runGraphAdd is `k graph add [--episode <id>] [--json <path>]`: reads one
// extraction turn's whole payload and posts it to /api/graph/add.
//
// The payload comes from a FILE or from stdin, never from flags: it is
// nested and full of prose — fact sentences with quotes and unicode that
// shell-quoting mangles — the same reason `k questions add` reads its object
// from stdin. `--json <path>` is the spelling .claude/skills/graph/SKILL.md
// tells the extraction agent to use, and it is the better one for that
// caller: an agent that has just written a payload file names it, rather
// than shelling out a heredoc of it.
//
// The body is forwarded as the caller's own JSON with at most ONE key
// touched — `episode_id`, and only when `--episode` was given. Everything
// else passes through undecoded, so internal/api/graph.go's graphAddReq
// stays the single source of truth for what an add looks like and this verb
// does not become a second copy of the wire schema that drifts from it.
//
// `--episode` is a flag rather than an in-payload-only field because the
// episode id is bookkeeping the agent already has in hand from `pending`,
// while the payload is the thing it just generated — keeping them separable
// means a payload can be written once and posted without being rewritten to
// carry its own provenance. Given BOTH, the flag wins: it is the more
// specific instruction, typed at the call site.
func runGraphAdd(args []string, stdin io.Reader) ([]byte, error) {
	var episode int64
	var haveEpisode bool
	var jsonPath string

	for i := 0; i < len(args); i++ {
		name, raw, inline := strings.Cut(args[i], "=")
		if !inline {
			name = args[i]
			if name != "--episode" && name != "--json" {
				return nil, fmt.Errorf("%s (unexpected argument %q)", graphAddUsage, args[i])
			}
			if i+1 >= len(args) {
				return nil, fmt.Errorf("%s (%s needs a value)", graphAddUsage, name)
			}
			raw = args[i+1]
			i++
		}
		switch name {
		case "--episode":
			n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
			if err != nil || n <= 0 {
				return nil, fmt.Errorf("bad --episode %q: want a positive whole number (%s)", raw, graphAddUsage)
			}
			episode, haveEpisode = n, true
		case "--json":
			jsonPath = strings.TrimSpace(raw)
			if jsonPath == "" {
				return nil, fmt.Errorf("%s (--json needs a path)", graphAddUsage)
			}
		default:
			return nil, fmt.Errorf("%s (unexpected argument %q)", graphAddUsage, args[i])
		}
	}

	var payload []byte
	if jsonPath != "" {
		b, err := os.ReadFile(jsonPath)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", jsonPath, err)
		}
		payload = b
	} else {
		if stdinIsInteractiveTTY(stdin) {
			return nil, fmt.Errorf("%s — refusing to wait on an interactive terminal; pass --json <path> or pipe the JSON in", graphAddUsage)
		}
		b, err := io.ReadAll(stdin)
		if err != nil {
			return nil, fmt.Errorf("read stdin: %w", err)
		}
		payload = b
	}

	// Decoding to map[string]json.RawMessage rather than to a typed struct is
	// what keeps this a proxy: every value stays the caller's own bytes, and
	// a field added server-side needs no change here. The decode is also the
	// local "is this even an object?" check — a friendlier error than the
	// server's "bad json" 400 for a plainly malformed payload.
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(payload, &obj); err != nil {
		return nil, fmt.Errorf("%s is not a JSON add payload: %w", payloadSource(jsonPath), err)
	}
	if obj == nil {
		return nil, fmt.Errorf("%s is not a JSON add payload: got null", payloadSource(jsonPath))
	}
	if haveEpisode {
		obj["episode_id"] = json.RawMessage(strconv.FormatInt(episode, 10))
	}
	return qfPost("/api/graph/add", obj)
}

// payloadSource names where a rejected payload came from, so the error
// points at the file the caller passed rather than at "stdin" it never used.
func payloadSource(jsonPath string) string {
	if jsonPath != "" {
		return jsonPath
	}
	return "stdin"
}

const graphDoneUsage = "usage: k graph done <episode id>"

// runGraphDone is `k graph done <episode id>`: POST /api/graph/episode-done.
//
// Its own verb rather than a field of `add` because an episode can
// legitimately yield NOTHING — a span with no durable facts in it has still
// been read, and folding "done" into the write would leave it pending
// forever, re-extracted (and re-paid for) on every run. That is exactly what
// .claude/skills/graph/SKILL.md tells the extraction agent, so the verb has
// to exist separately for the instruction to be followable.
func runGraphDone(args []string) (int64, error) {
	if len(args) != 1 {
		return 0, fmt.Errorf("%s", graphDoneUsage)
	}
	id, err := strconv.ParseInt(strings.TrimSpace(args[0]), 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("bad episode id %q: want a positive whole number (%s)", args[0], graphDoneUsage)
	}
	if _, err := qfPost("/api/graph/episode-done", map[string]any{"id": id}); err != nil {
		return 0, err
	}
	return id, nil
}

const graphSearchUsage = "usage: k graph search \"<query>\" [--limit N]"

// graphSearchResp is the subset of POST /api/graph/search's response this
// verb renders. It decodes rather than passing through because `search`'s
// output is read by a HUMAN or pasted into an agent's context, and printing
// the JSON envelope would make every caller strip it.
//
// Two of the three response keys are rendered. The `facts` array is
// deliberately NOT modeled: the same facts are already in `triples`, in the
// form meant for reading, and a struct for them would be a second copy of the
// wire schema kept in step by hand.
//
// `nodes` IS modeled, and only its three identifying fields are: the skill's
// canonicalization step searches by NAME to decide whether an incoming entity
// is one already known, and that judgement needs the name, its type and its
// gloss — which is exactly this struct and nothing more.
//
// Unknown keys are ignored on the way in (encoding/json's default), so a
// server that grows a field never breaks this verb.
//
// `strong` is a *int rather than an int, and the pointer is the whole point:
// this binary and kraken serve are separate processes that are not upgraded
// together, so a nil here means "an older server that never sent the key" and
// must render exactly as it always did — whereas a decoded 0 is a live claim
// that nothing in the answer is actually about the query. An int would collapse
// those two into the same zero and put a warning on every response from a
// server that predates it.
type graphSearchResp struct {
	Nodes []struct {
		Name        string `json:"name"`
		Type        string `json:"type"`
		Description string `json:"description"`
	} `json:"nodes"`
	Triples string `json:"triples"`
	Strong  *int   `json:"strong"`
}

// runGraphSearch is `k graph search "<query>" [--limit N]`: POSTs the query
// and returns the triple text ready to print. A query that matched nothing
// returns a one-line note rather than empty output — silence is
// indistinguishable from a broken pipe.
func runGraphSearch(args []string) (string, error) {
	// `--` ends flag parsing, so a query that itself starts with a hyphen —
	// `k graph search -- "-tags sqlite_fts5"` — is reachable. Without the
	// escape a leading-hyphen arg is treated as a mistyped flag, because
	// silently searching for "--limit" when the user fumbled a flag would be
	// the worse behaviour.
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
		if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
			return "", fmt.Errorf("%s", graphSearchUsage)
		}
	} else if len(args) == 0 || strings.TrimSpace(args[0]) == "" || strings.HasPrefix(args[0], "-") {
		return "", fmt.Errorf("%s", graphSearchUsage)
	}
	query := args[0]
	limit, err := parseGraphLimit(args[1:], graphSearchUsage)
	if err != nil {
		return "", err
	}
	body := map[string]any{"query": query}
	if limit > 0 {
		body["limit"] = limit
	}
	raw, err := qfPost("/api/graph/search", body)
	if err != nil {
		return "", err
	}
	var resp graphSearchResp
	if err := json.Unmarshal(raw, &resp); err != nil {
		return "", fmt.Errorf("kraken serve returned an unreadable search response: %w", err)
	}
	// Entities first, then facts. That order is the one the skill's
	// canonicalization step reads in: it is asking "does this name already
	// exist, and as what?" before it looks at anything the entity is involved
	// in.
	var b strings.Builder
	if len(resp.Nodes) > 0 {
		b.WriteString("entities:\n")
		for _, n := range resp.Nodes {
			b.WriteString("  " + n.Name)
			if n.Type != "" {
				b.WriteString(" (" + n.Type + ")")
			}
			if n.Description != "" {
				b.WriteString(" — " + n.Description)
			}
			b.WriteString("\n")
		}
	}
	triples := strings.TrimRight(resp.Triples, "\n")
	if triples != "" {
		if b.Len() > 0 {
			b.WriteString("\nfacts:\n")
		}
		b.WriteString(triples + "\n")
	}
	// Nothing at all: say so rather than print nothing. Silence here is
	// indistinguishable from a broken pipe, and the caller is usually an
	// agent about to decide this name is new.
	if b.Len() == 0 {
		return fmt.Sprintf("nothing matched %q\n", query), nil
	}
	// A non-empty answer with zero strong facts is the case this line exists
	// for, and it is the DANGEROUS one: the server did return entities and
	// facts, so the output looks exactly like a hit, and a caller deciding
	// whether an entity is already known would merge on it. Saying so FIRST —
	// above the rendering rather than below it — is deliberate: the reader is
	// usually a model that acts on what it reads, and a caveat under twenty
	// facts is a caveat read after the decision was made. The facts still print
	// in full underneath, because the near-misses are exactly what a human
	// canonicalizing a name needs to look at.
	if resp.Strong != nil && *resp.Strong == 0 {
		return fmt.Sprintf("nothing strongly matched %q\n", query) + b.String(), nil
	}
	return b.String(), nil
}

const graphGetUsage = "usage: k graph get <handle>... (F<edge-id>, bare <edge-id>, or E<episode-id>)"

// parseGraphHandle reads one handle as it appears in an injected fact line —
// `[F1091 E148111, seen 1x]` — and says which route dereferences it.
//
// `F<id>` and a bare `<id>` are both the EDGE, because the two spellings
// arrive from two different readers: an agent copying the handle out of the
// block it was given types the `F`, while a human who already has an edge id
// in hand types the number. `E<id>` is the episode — a different route and a
// different object, which is why the prefix is part of the parse rather than
// a flag.
//
// The prefixes are case-insensitive: the handle is transcribed by hand out of
// prose, and rejecting `f1091` would fail a caller who named the right fact.
// Everything else — empty, a lone `F`, a float, zero, negative, a non-number
// — is a usage error naming the offending arg, never a silent fallback to
// some other id: dereferencing the WRONG fact is worse than refusing.
func parseGraphHandle(arg string) (episode bool, id int64, err error) {
	s := strings.TrimSpace(arg)
	digits := s
	if s != "" {
		switch s[0] {
		case 'F', 'f':
			digits = s[1:]
		case 'E', 'e':
			episode, digits = true, s[1:]
		}
	}
	n, convErr := strconv.ParseInt(digits, 10, 64)
	if convErr != nil || n <= 0 {
		return false, 0, fmt.Errorf("bad handle %q: want F<edge-id>, a bare edge id, or E<episode-id> (%s)", arg, graphGetUsage)
	}
	return episode, n, nil
}

// runGraphGet is `k graph get <handle>...`: GET /api/graph/fact?id=N per edge
// handle, GET /api/graph/episode?id=N per episode handle, each response body
// printed verbatim in argument order.
//
// It exists because the facts injected into a cell's context are CUT — a
// clause-cut summary plus a handle — so the handle is the only way back to
// the sentence the graph actually stored, and to the episode that sourced it.
// The body is never decoded here: this verb has no opinion about the bundle's
// shape, so a field added server-side needs no change on this side, exactly
// as `pending` and `stats` pass their JSON straight through.
//
// Multiple handles are one call rather than a loop the caller writes, since
// reading a line's `[F# E#]` pair is the common case. The FIRST failure stops
// the run and returns: bodies already written stay written, and printing the
// remaining handles after an error would bury it mid-stream.
func runGraphGet(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", graphGetUsage)
	}
	for _, arg := range args {
		episode, id, err := parseGraphHandle(arg)
		if err != nil {
			return err
		}
		route := "/api/graph/fact?id="
		if episode {
			route = "/api/graph/episode?id="
		}
		body, err := qfGet(route + strconv.FormatInt(id, 10))
		if err != nil {
			return err
		}
		if _, err := out.Write(body); err != nil {
			return fmt.Errorf("write output: %w", err)
		}
		// One newline BETWEEN bundles, and only when the server didn't
		// already end with one — kraken serve's encoder does, so this is
		// the guard for a server that ever stops, not a reformat of what it
		// sent. Two handles must never print as one run-together line.
		if !bytes.HasSuffix(body, []byte("\n")) {
			if _, err := io.WriteString(out, "\n"); err != nil {
				return fmt.Errorf("write output: %w", err)
			}
		}
	}
	return nil
}

const graphStatsUsage = "usage: k graph stats"

// runGraphStats is `k graph stats`: GET /api/graph/stats — its own route
// rather than /api/graph's stats field, so printing six integers doesn't
// pull every node and every edge across the wire first.
func runGraphStats(args []string) ([]byte, error) {
	if len(args) != 0 {
		return nil, fmt.Errorf("%s (unexpected argument %q)", graphStatsUsage, args[0])
	}
	return qfGet("/api/graph/stats")
}
