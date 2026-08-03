package main

import (
	"strings"
	"testing"
)

// slack_edge_test.go — the EDGE dimension of slice 4's CLI half of
// artifacts/50-contract.md: what the two frozen arg parsers REFUSE. The
// contract freezes exactly two symbols here — parseSlackAck and
// parseJobDraftArgs — and both exist for one reason: a mistyped flag must die
// locally, naming what the caller actually typed, rather than reaching the
// server as a well-formed request that means something else.
//
// Nothing in this file makes an HTTP call: a parser that refuses correctly
// never gets that far, and a parser that wrongly ACCEPTS is caught by the
// assertion on its parsed values rather than by a round trip.
//
// Every package-level identifier declared here is prefixed `edge` — this
// package's test namespace is shared with the other blind author.

// --- parseSlackAck --------------------------------------------------------

// edgeIntsEqual compares a parsed id list against its expectation.
func edgeIntsEqual(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestEdgeParseSlackAckRefusesBadArgs sweeps every shape the contract calls
// an error. The consequence of a silent acceptance is specific and bad: `ack`
// is what closes an inbox row forever, so an argument that parsed to the
// WRONG ids retires messages nobody read, and one that parsed to an empty
// disposition retires them with no record of what was done.
func TestEdgeParseSlackAckRefusesBadArgs(t *testing.T) {
	cases := map[string][]string{
		"no args":                   nil,
		"empty args":                {},
		"ids only":                  {"--ids", "42,43"},
		"disposition only":          {"--disposition", "ignored"},
		"ids= only":                 {"--ids=42"},
		"disposition= only":         {"--disposition=ignored"},
		"unknown flag":              {"--ids", "42", "--disposition", "ignored", "--bogus", "x"},
		"unknown flag inline":       {"--ids=42", "--disposition=ignored", "--force=1"},
		"single-dash flag":          {"-ids", "42", "--disposition", "ignored"},
		"stray positional":          {"42", "--disposition", "ignored"},
		"trailing positional":       {"--ids", "42", "--disposition", "ignored", "extra"},
		"ids missing its value":     {"--ids"},
		"ids missing value at end":  {"--disposition", "ignored", "--ids"},
		"disposition missing value": {"--ids", "42", "--disposition"},
		"empty ids value":           {"--ids", "", "--disposition", "ignored"},
		"empty ids inline":          {"--ids=", "--disposition=ignored"},
		"whitespace ids value":      {"--ids", "   ", "--disposition", "ignored"},
		"non-numeric id":            {"--ids", "42,abc", "--disposition", "ignored"},
		"wholly non-numeric ids":    {"--ids", "abc", "--disposition", "ignored"},
		"float id":                  {"--ids", "42.0", "--disposition", "ignored"},
		"empty element":             {"--ids", "42,,43", "--disposition", "ignored"},
		"trailing comma":            {"--ids", "42,", "--disposition", "ignored"},
		"leading comma":             {"--ids", ",42", "--disposition", "ignored"},
		"bare comma":                {"--ids", ",", "--disposition", "ignored"},
		"space-separated ids":       {"--ids", "42 43", "--disposition", "ignored"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			ids, disposition, err := parseSlackAck(args)
			if err == nil {
				t.Fatalf("args %v accepted as ids=%v disposition=%q", args, ids, disposition)
			}
			if len(ids) != 0 || disposition != "" {
				t.Errorf("args %v: a refusal still produced ids=%v disposition=%q", args, ids, disposition)
			}
			if strings.TrimSpace(err.Error()) == "" {
				t.Errorf("args %v: empty error message", args)
			}
		})
	}
}

// TestEdgeParseSlackAckControl is the control the refusal sweep needs: the
// canonical invocation from the contract's own CLI table must parse, in both
// flag forms, or every case above would pass on a parser that refuses
// everything.
func TestEdgeParseSlackAckControl(t *testing.T) {
	for _, args := range [][]string{
		{"--ids", "42,43", "--disposition", "drafted:csv-export-timeout"},
		{"--ids=42,43", "--disposition=drafted:csv-export-timeout"},
		{"--disposition", "drafted:csv-export-timeout", "--ids", "42,43"},
	} {
		ids, disposition, err := parseSlackAck(args)
		if err != nil {
			t.Fatalf("args %v: %v", args, err)
		}
		if !edgeIntsEqual(ids, []int64{42, 43}) {
			t.Errorf("args %v: ids = %v, want [42 43]", args, ids)
		}
		if disposition != "drafted:csv-export-timeout" {
			t.Errorf("args %v: disposition = %q", args, disposition)
		}
	}

	// A single id is the ordinary case and must not need a comma.
	ids, _, err := parseSlackAck([]string{"--ids", "42", "--disposition", "ignored"})
	if err != nil || !edgeIntsEqual(ids, []int64{42}) {
		t.Fatalf("single id: ids = %v, err = %v", ids, err)
	}
}

// TestEdgeParseSlackAckKeepsIDOrderAndDoesNotDedupe: the ids are the store's
// all-or-nothing batch, so silently reordering or collapsing them would make
// the server's refusal message name an id the caller never typed — and a
// duplicate is exactly the request the store's already-acked refusal exists
// to catch, so swallowing it here hides a real iris bookkeeping bug.
func TestEdgeParseSlackAckKeepsIDOrderAndDoesNotDedupe(t *testing.T) {
	ids, _, err := parseSlackAck([]string{"--ids", "43,42,43", "--disposition", "ignored"})
	if err != nil {
		t.Fatalf("parseSlackAck: %v", err)
	}
	if !edgeIntsEqual(ids, []int64{43, 42, 43}) {
		t.Fatalf("ids = %v, want [43 42 43] verbatim", ids)
	}
}

// --- parseJobDraftArgs ----------------------------------------------------

// TestEdgeParseJobDraftArgsRefusesBadArgs: this verb reads a whole job file
// off stdin, so a slug it guessed wrong writes that file over a DIFFERENT
// job. Every shape below must die before the POST.
func TestEdgeParseJobDraftArgsRefusesBadArgs(t *testing.T) {
	cases := map[string][]string{
		"no args":               nil,
		"empty args":            {},
		"empty slug":            {""},
		"whitespace slug":       {"   "},
		"flag in the slug slot": {"--mtime", "7"},
		"unknown flag":          {"csv-export-timeout", "--bogus", "x"},
		"unknown flag inline":   {"csv-export-timeout", "--force=1"},
		"mtime with no value":   {"csv-export-timeout", "--mtime"},
		"non-numeric mtime":     {"csv-export-timeout", "--mtime", "banana"},
		"empty mtime":           {"csv-export-timeout", "--mtime", ""},
		"empty mtime inline":    {"csv-export-timeout", "--mtime="},
		"float mtime":           {"csv-export-timeout", "--mtime", "7.5"},
		"second positional":     {"csv-export-timeout", "extra"},
		"positional after flag": {"csv-export-timeout", "--mtime", "7", "extra"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			slug, mtime, err := parseJobDraftArgs(args)
			if err == nil {
				t.Fatalf("args %v accepted as slug=%q mtime=%d", args, slug, mtime)
			}
			if slug != "" || mtime != 0 {
				t.Errorf("args %v: a refusal still produced slug=%q mtime=%d", args, slug, mtime)
			}
			if strings.TrimSpace(err.Error()) == "" {
				t.Errorf("args %v: empty error message", args)
			}
		})
	}
}

// TestEdgeParseJobDraftArgsControl: the two legitimate invocations from the
// contract's CLI table, so the sweep above is measuring the guards rather
// than a parser that refuses everything. An absent --mtime is mtime 0, which
// is the wire's documented "skip the optimistic lock".
func TestEdgeParseJobDraftArgsControl(t *testing.T) {
	slug, mtime, err := parseJobDraftArgs([]string{"csv-export-timeout"})
	if err != nil {
		t.Fatalf("bare slug: %v", err)
	}
	if slug != "csv-export-timeout" || mtime != 0 {
		t.Fatalf("bare slug: slug=%q mtime=%d, want the slug and 0", slug, mtime)
	}

	for _, args := range [][]string{
		{"csv-export-timeout", "--mtime", "7"},
		{"csv-export-timeout", "--mtime=7"},
	} {
		slug, mtime, err := parseJobDraftArgs(args)
		if err != nil {
			t.Fatalf("args %v: %v", args, err)
		}
		if slug != "csv-export-timeout" || mtime != 7 {
			t.Fatalf("args %v: slug=%q mtime=%d", args, slug, mtime)
		}
	}
}

// NOTE (reported to zeus): `kraken job draft`'s "refuse an interactive TTY"
// obligation is NOT pinned here. The contract freezes only parseJobDraftArgs
// on this verb, and the TTY refusal lives in the runner, which has no frozen
// symbol to compile against; the shared stdinIsInteractiveTTY helper it is
// contracted to use is pre-existing and already covered in
// cmd_questions_test.go, so re-asserting it here would test neither this
// slice's code nor this slice's wiring.
