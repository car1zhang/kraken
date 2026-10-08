package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"kraken/internal/util"
)

// cmd_board.go — `k board get [slug]`: an agent-side read of the job board,
// proxying to kraken serve's GET /api/board.
//
// It is the verb an agent answers "what is the state of everything?" with,
// and with a slug "what is the state of THIS?". Both print the server's own
// JSON, so the caller reads exactly the object the web board and the phone render —
// there is no second, thinner board shape maintained here that could drift
// from the one everything else agrees on.
//
// Same behavior contract as the rest of the proxy family (cmd_slack.go's
// header): a read prints the server's JSON to stdout verbatim, a 4xx prints
// the server's own sentence to stderr and exits non-zero, an unreachable
// serve names the address it tried. It NEVER opens kraken.db.
func cmdBoard(args []string) {
	if len(args) == 0 {
		util.Die("usage: k board get [slug]")
	}
	op, rest := args[0], args[1:]
	switch op {
	case "get":
		out, err := runBoardGet(rest)
		if err != nil {
			util.Die("%v", err)
		}
		writeStdout(out)
	default:
		util.Die("usage: k board get [slug] (unknown op %q)", op)
	}
}

const boardGetUsage = "usage: k board get [slug]"

// runBoardGet is `k board get [slug]`: GET /api/board, printed straight
// through with no slug, or narrowed to one job's object with one.
//
// The filter is client-side and jq-free on purpose. There is no
// per-slug board route, and adding one would mean a second assembly path that
// can disagree with /api/board about the same job; narrowing the one payload
// cannot. The cost is that a slug read pulls the whole board across
// localhost, which is a few tens of KB on one machine.
//
// The selected element is re-emitted as the server's OWN bytes
// (json.RawMessage), never re-marshaled: a round trip through a map would
// reorder keys and re-render numbers, and this verb's whole promise is that
// what it prints is what the server said.
func runBoardGet(args []string) ([]byte, error) {
	if len(args) > 1 {
		return nil, fmt.Errorf("%s (unexpected argument %q)", boardGetUsage, args[1])
	}
	body, err := qfGet("/api/board")
	if err != nil {
		return nil, err
	}
	if len(args) == 0 {
		return body, nil
	}
	slug := strings.TrimSpace(args[0])
	if slug == "" {
		return nil, fmt.Errorf("%s (the slug is empty)", boardGetUsage)
	}
	return selectBoardJob(body, slug)
}

// selectBoardJob picks one job object out of a /api/board payload by slug.
//
// It decodes only as far as it must — the "jobs" array as raw elements, then
// each element's "slug" — so every key of the job it returns is untouched,
// including ones this binary has never heard of. An unknown slug is an ERROR
// naming it rather than an empty print: a caller asks about a job because it
// intends to act on it, and silence would read as "the job exists and has
// nothing to say".
func selectBoardJob(body []byte, slug string) ([]byte, error) {
	var envelope struct {
		Jobs []json.RawMessage `json:"jobs"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("kraken serve returned an unreadable board: %w", err)
	}
	for _, raw := range envelope.Jobs {
		var ident struct {
			Slug string `json:"slug"`
		}
		if err := json.Unmarshal(raw, &ident); err != nil {
			// One malformed element must not hide the job actually asked
			// for: skip it and keep looking, and let the not-found error
			// below be the answer if nothing matches.
			continue
		}
		if ident.Slug == slug {
			// A trailing newline so consecutive reads never run together and
			// a terminal shows a prompt on its own line — the same courtesy
			// kraken serve's own encoder extends.
			return append(append([]byte{}, raw...), '\n'), nil
		}
	}
	return nil, fmt.Errorf("no job %q on the board (%d active jobs — run `k board get` with no slug to see them)", slug, len(envelope.Jobs))
}
