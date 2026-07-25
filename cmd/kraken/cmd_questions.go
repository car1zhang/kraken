package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"kraken/internal/util"
)

// cmd_questions.go — `k questions get|set|add|update|remove <slug>
// ...`: the cell-side verb family for zeus' own open doc questions,
// proxying to kraken serve's GET/POST /api/questions
// (internal/api/questions.go). zeus learns the read verb from crew.go's
// zeusBrief (its "Open questions and carl's per-artifact feedback are rows"
// paragraph) and the four WRITE verbs from .claude/skills/aim/SKILL.md's
// Core Rules, which the brief points it at; the spellings here match both.
//
// Behavior contract (all five ops):
//   - success: GET prints the server's JSON to stdout verbatim, exit 0; a
//     write prints one short confirmation line to STDERR (stdout stays
//     parseable — a caller piping `kraken questions get` output never sees
//     write-confirmation noise mixed in), exit 0.
//   - a 400 (bad qid, bad importance, malformed body, ...) prints the
//     server's own error message to stderr, exit non-zero — never a raw
//     HTTP dump.
//   - kraken serve unreachable: a stderr message naming the address this
//     process tried, exit non-zero — never silently succeeds.
//   - bad/missing args: a usage message to stderr, exit non-zero.
//
// parse/run are split from cmdQuestions so the exit-code paths are testable
// without going through util.Die's os.Exit — same shape as cmd_import.go's
// parseImportArgs/runImport split.
func cmdQuestions(args []string) {
	if len(args) == 0 {
		util.Die("usage: k questions get|set|add|update|remove <slug> ...")
	}
	op, rest := args[0], args[1:]
	switch op {
	case "get":
		out, err := runQuestionsGet(rest)
		if err != nil {
			util.Die("%v", err)
		}
		if _, err := os.Stdout.Write(out); err != nil {
			util.Die("write stdout: %v", err)
		}
	case "set":
		if err := runQuestionsSet(rest, os.Stdin); err != nil {
			util.Die("%v", err)
		}
		fmt.Fprintln(os.Stderr, "questions set")
	case "add", "update":
		if err := runQuestionsUpsert(op, rest, os.Stdin); err != nil {
			util.Die("%v", err)
		}
		fmt.Fprintf(os.Stderr, "%s request accepted for slug %s (server upserts by qid — `get` to confirm)\n", op, rest[0])
	case "remove":
		if err := runQuestionsRemove(rest); err != nil {
			util.Die("%v", err)
		}
		fmt.Fprintf(os.Stderr, "remove request sent for slug %s, qid %s (remove is a no-op if the qid was already gone)\n", rest[0], rest[1])
	default:
		util.Die("usage: k questions get|set|add|update|remove <slug> ... (unknown op %q)", op)
	}
}

// runQuestionsGet is `kraken questions get <slug>`: GET /api/questions?slug=
// — prints {"questions": [...]} straight through.
func runQuestionsGet(args []string) ([]byte, error) {
	if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
		return nil, fmt.Errorf("usage: k questions get <slug>")
	}
	return qfGet("/api/questions?slug=" + url.QueryEscape(strings.TrimSpace(args[0])))
}

// runQuestionsSet is `kraken questions set <slug>`: reads a JSON array of
// question objects from stdin and bulk-replaces the job's whole set
// (POST /api/questions {slug, questions}). The array is forwarded as raw
// JSON rather than decoded into a local struct — this verb is a thin proxy,
// not a second copy of the wire schema; questions.go's questionWire (and
// its validation) is the single source of truth for what a question object
// looks like, and stays that way even if a field is added there later. The
// only local check is "stdin is valid JSON" (a friendlier error than a
// server-side "bad json" 400 for a plainly malformed stdin).
func runQuestionsSet(args []string, stdin io.Reader) error {
	if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
		return fmt.Errorf("usage: k questions set <slug> (JSON question array on stdin)")
	}
	if stdinIsInteractiveTTY(stdin) {
		return fmt.Errorf("usage: k questions set <slug> (JSON question array on stdin) — refusing to wait on an interactive terminal; pipe the JSON in instead")
	}
	slug := strings.TrimSpace(args[0])
	raw, err := io.ReadAll(stdin)
	if err != nil {
		return fmt.Errorf("read stdin: %w", err)
	}
	var probe []json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return fmt.Errorf("stdin is not a JSON array of questions: %w", err)
	}
	if probe == nil {
		return fmt.Errorf("stdin is not a JSON array of questions: got null")
	}
	body := struct {
		Slug      string          `json:"slug"`
		Questions json.RawMessage `json:"questions"`
	}{slug, json.RawMessage(raw)}
	_, err = qfPost("/api/questions", body)
	return err
}

// runQuestionsUpsert is `kraken questions add|update <slug>`: reads one
// question object from stdin (stdin-based, not a shell arg, since question
// text is full of quotes/unicode that shell-quoting mangles) and posts
// {slug, op, question}. Same raw-passthrough approach as runQuestionsSet.
func runQuestionsUpsert(op string, args []string, stdin io.Reader) error {
	if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
		return fmt.Errorf("usage: k questions %s <slug> (JSON question object on stdin)", op)
	}
	if stdinIsInteractiveTTY(stdin) {
		return fmt.Errorf("usage: k questions %s <slug> (JSON question object on stdin) — refusing to wait on an interactive terminal; pipe the JSON in instead", op)
	}
	slug := strings.TrimSpace(args[0])
	raw, err := io.ReadAll(stdin)
	if err != nil {
		return fmt.Errorf("read stdin: %w", err)
	}
	var probe map[string]any
	if err := json.Unmarshal(raw, &probe); err != nil {
		return fmt.Errorf("stdin is not a JSON question object: %w", err)
	}
	if probe == nil {
		return fmt.Errorf("stdin is not a JSON question object: got null")
	}
	body := struct {
		Slug     string          `json:"slug"`
		Op       string          `json:"op"`
		Question json.RawMessage `json:"question"`
	}{slug, op, json.RawMessage(raw)}
	_, err = qfPost("/api/questions", body)
	return err
}

// runQuestionsRemove is `kraken questions remove <slug> <qid>`: qid alone
// carries no quoting hazard, so it's a plain arg rather than stdin, unlike
// add/update's full question object.
func runQuestionsRemove(args []string) error {
	if len(args) != 2 || strings.TrimSpace(args[0]) == "" || strings.TrimSpace(args[1]) == "" {
		return fmt.Errorf("usage: k questions remove <slug> <qid>")
	}
	body := map[string]string{"slug": strings.TrimSpace(args[0]), "op": "remove", "qid": strings.TrimSpace(args[1])}
	_, err := qfPost("/api/questions", body)
	return err
}
