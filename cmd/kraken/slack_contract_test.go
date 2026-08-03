package main

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

// slack_contract_test.go — blind contract tests for slice 4's CLI half
// (50-contract.md § "Slice 4", the verb table): the two frozen argument
// parsers, plus the registration points a new verb has to land in to be
// reachable at all.
//
// Dimension: stated invariants and accepted forms. The refusals (unknown
// flag, missing flag, non-numeric id, an interactive-TTY stdin) belong to
// athena-edge.
//
// Only parseSlackAck and parseJobDraftArgs are frozen by name — the run*
// wrappers are not, so this file deliberately does not reach for them.
// Every package-level identifier is `contract`-prefixed.

// contractSlackInts renders an id slice for comparison in one string.
func contractSlackInts(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return strings.Join(parts, ",")
}

// TestContractParseSlackAckAcceptsBothFlagForms: zeus and iris both write
// these command lines by hand, so both `--flag value` and `--flag=value`
// parse, in any order, and the ids are comma-separated in one flag rather
// than a repeated flag.
func TestContractParseSlackAckAcceptsBothFlagForms(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"space form", []string{"--ids", "42,43", "--disposition", "drafted:csv-export-timeout"}},
		{"equals form", []string{"--ids=42,43", "--disposition=drafted:csv-export-timeout"}},
		{"mixed, reordered", []string{"--disposition", "drafted:csv-export-timeout", "--ids=42,43"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ids, disposition, err := parseSlackAck(c.args)
			if err != nil {
				t.Fatalf("parseSlackAck(%v): %v", c.args, err)
			}
			if got := contractSlackInts(ids); got != "42,43" {
				t.Errorf("ids = %v (%q), want 42,43", ids, got)
			}
			if disposition != "drafted:csv-export-timeout" {
				t.Errorf("disposition = %q", disposition)
			}
		})
	}
}

// TestContractParseSlackAckSingleID: acking one message at a time is the
// common case — a single id needs no comma and must not come back empty.
func TestContractParseSlackAckSingleID(t *testing.T) {
	ids, disposition, err := parseSlackAck([]string{"--ids", "7", "--disposition", "ignored"})
	if err != nil {
		t.Fatalf("parseSlackAck: %v", err)
	}
	if got := contractSlackInts(ids); got != "7" {
		t.Fatalf("ids = %v, want [7]", ids)
	}
	if disposition != "ignored" {
		t.Fatalf("disposition = %q, want %q", disposition, "ignored")
	}
}

// TestContractParseSlackAckKeepsDispositionText: a disposition carries a
// slug and may carry spaces; the parser must hand back the whole value
// rather than the first word.
func TestContractParseSlackAckKeepsDispositionText(t *testing.T) {
	ids, disposition, err := parseSlackAck([]string{"--ids", "1,2,3", "--disposition", "edited:csv-export-timeout (added the xlsx report)"})
	if err != nil {
		t.Fatalf("parseSlackAck: %v", err)
	}
	if got := contractSlackInts(ids); got != "1,2,3" {
		t.Fatalf("ids = %v, want 1,2,3", ids)
	}
	if disposition != "edited:csv-export-timeout (added the xlsx report)" {
		t.Fatalf("disposition = %q, want the whole value", disposition)
	}
}

// TestContractParseJobDraftArgsAcceptedForms: the slug is positional and the
// optional lock is `--mtime N` in either flag form. An absent --mtime is 0,
// which is the documented "skip the check" value — not an error and not a
// sentinel the caller has to invent.
func TestContractParseJobDraftArgsAcceptedForms(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		wantSlug  string
		wantMTime int64
	}{
		{"slug only", []string{"csv-export-timeout"}, "csv-export-timeout", 0},
		{"space form", []string{"csv-export-timeout", "--mtime", "7"}, "csv-export-timeout", 7},
		{"equals form", []string{"csv-export-timeout", "--mtime=7"}, "csv-export-timeout", 7},
		{"explicit zero", []string{"csv-export-timeout", "--mtime=0"}, "csv-export-timeout", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			slug, mtime, err := parseJobDraftArgs(c.args)
			if err != nil {
				t.Fatalf("parseJobDraftArgs(%v): %v", c.args, err)
			}
			if slug != c.wantSlug {
				t.Errorf("slug = %q, want %q", slug, c.wantSlug)
			}
			if mtime != c.wantMTime {
				t.Errorf("mtime = %d, want %d", mtime, c.wantMTime)
			}
		})
	}
}

// TestContractSlackAndJobVerbsAreRegistered covers the registration points a
// verb needs to be reachable: the dispatch switch and the usage heredoc in
// main.go, plus the `_k` zsh completion. A verb implemented in its own file
// and never wired in compiles, tests green, and does not exist — the failure
// mode 50-components §8 calls out.
func TestContractSlackAndJobVerbsAreRegistered(t *testing.T) {
	mainSrc, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	src := string(mainSrc)
	for _, want := range []string{`case "slack":`, `case "job":`} {
		if !strings.Contains(src, want) {
			t.Errorf("main.go's dispatch switch has no %s — the verb is unreachable", want)
		}
	}
	// The usage heredoc is the only discovery surface an agent reading
	// `k help` has, so each new family must name itself there. HOW a family
	// renders is NOT frozen, though, and the heredoc's existing convention
	// is pipe-joined (`k questions get|set|add|update|remove <slug> ...`,
	// `k load get|set|clear <slug> ...`) — so this looks for a usage line
	// that introduces the family and names its verbs, never for the byte
	// sequence "job draft", which would be a rendering requirement nobody
	// set.
	usageNames := func(family string, verbs ...string) bool {
		for _, line := range strings.Split(src, "\n") {
			if !strings.Contains(line, "k "+family) {
				continue
			}
			all := true
			for _, v := range verbs {
				if !strings.Contains(line, v) {
					all = false
					break
				}
			}
			if all {
				return true
			}
		}
		return false
	}
	if !usageNames("slack", "inbox", "status", "ack") {
		t.Error("main.go's usage text has no `k slack` line naming inbox/status/ack — the family is undiscoverable from `k help`")
	}
	if !usageNames("job", "list", "raw", "draft") {
		t.Error("main.go's usage text has no `k job` line naming list/raw/draft — the family is undiscoverable from `k help`")
	}

	completion, err := os.ReadFile("_k")
	if err != nil {
		t.Fatalf("read _k: %v", err)
	}
	comp := string(completion)
	for _, want := range []string{"slack", "job"} {
		if !strings.Contains(comp, want) {
			t.Errorf("_k completion never mentions %q", want)
		}
	}
}
