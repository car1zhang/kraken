package main

import (
	"strings"
	"testing"
)

// periodic_edge_test.go — athena-edge's blind suite for slice 4 of the
// periodic-jobs contract (50-contract.md, "Slice 4: the verbs"): the argument
// parsers' refusals and their boundaries. cmdPeriodic itself is a proxy to a
// live serve and exits the process on a refusal, so it is not exercised here.

// Contract: parsePeriodicAdd "Reads `<name> --prefix <p> --at <HH:MM> [--days
// <list>] [--file <path>]`. Each flag is accepted as `--flag value` and as
// `--flag=value`, in any order after the name. ... `days` and `file` are empty
// when not given. It validates nothing else: the server is the gate."
func TestPeriodicEdgeParseAddAccepts(t *testing.T) {
	for _, c := range []struct {
		args []string
		want periodicAddArgs
	}{
		{
			[]string{"nightly", "--prefix", "nightly", "--at", "09:00"},
			periodicAddArgs{name: "nightly", prefix: "nightly", at: "09:00"},
		},
		{
			[]string{"nightly", "--at=9:00", "--prefix=n"},
			periodicAddArgs{name: "nightly", prefix: "n", at: "9:00"},
		},
		{
			[]string{"nightly", "--file", "/tmp/t.md", "--days=mon,fri", "--at", "23:59", "--prefix", "p"},
			periodicAddArgs{name: "nightly", prefix: "p", at: "23:59", days: "mon,fri", file: "/tmp/t.md"},
		},
		// validates nothing else: an invalid time, an unslugged prefix and a
		// bogus day list all pass through to the server
		{
			[]string{"nightly", "--prefix", "Not A Slug", "--at", "99:99", "--days", "funday"},
			periodicAddArgs{name: "nightly", prefix: "Not A Slug", at: "99:99", days: "funday"},
		},
	} {
		got, err := parsePeriodicAdd(c.args)
		if err != nil {
			t.Errorf("parsePeriodicAdd(%q): %v", c.args, err)
			continue
		}
		if got != c.want {
			t.Errorf("parsePeriodicAdd(%q) = %+v, want %+v", c.args, got, c.want)
		}
	}
}

// Contract: "A missing name (or a first argument starting with `-`), a missing
// `--prefix`, a missing `--at`, a flag with no value, an unknown flag and a
// second positional are each an error."
func TestPeriodicEdgeParseAddRefusals(t *testing.T) {
	for _, c := range []struct {
		why  string
		args []string
	}{
		{"no args at all", nil},
		{"empty args", []string{}},
		{"first argument is a flag", []string{"--prefix", "p", "--at", "09:00"}},
		{"first argument is a single-dash word", []string{"-nightly", "--prefix", "p", "--at", "09:00"}},
		{"missing --prefix", []string{"nightly", "--at", "09:00"}},
		{"missing --at", []string{"nightly", "--prefix", "p"}},
		{"name only", []string{"nightly"}},
		{"--prefix with no value at the end", []string{"nightly", "--at", "09:00", "--prefix"}},
		{"--at with no value at the end", []string{"nightly", "--prefix", "p", "--at"}},
		{"--days with no value at the end", []string{"nightly", "--prefix", "p", "--at", "09:00", "--days"}},
		{"--file with no value at the end", []string{"nightly", "--prefix", "p", "--at", "09:00", "--file"}},
		{"unknown flag", []string{"nightly", "--prefix", "p", "--at", "09:00", "--bogus", "x"}},
		{"unknown flag in = form", []string{"nightly", "--prefix", "p", "--at", "09:00", "--repo=kraken"}},
		{"second positional at the end", []string{"nightly", "--prefix", "p", "--at", "09:00", "extra"}},
		{"second positional right after the name", []string{"nightly", "report", "--prefix", "p", "--at", "09:00"}},
	} {
		if got, err := parsePeriodicAdd(c.args); err == nil {
			t.Errorf("%s: parsePeriodicAdd(%q) = %+v, want an error", c.why, c.args, got)
		}
	}
}

// Contract: parsePeriodicName — "Exactly one non-blank argument, returned
// trimmed. Anything else is an error whose text contains `op`."
func TestPeriodicEdgeParseName(t *testing.T) {
	for _, op := range []string{"pause", "resume", "run", "rm"} {
		for _, args := range [][]string{nil, {}, {""}, {"   "}, {"\t"}, {"a", "b"}, {"a", ""}, {"", "a"}} {
			got, err := parsePeriodicName(args, op)
			if err == nil {
				t.Errorf("parsePeriodicName(%q, %q) = %q, want an error", args, op, got)
				continue
			}
			if !strings.Contains(err.Error(), op) {
				t.Errorf("parsePeriodicName(%q, %q) error %q does not contain the op", args, op, err)
			}
		}
		for _, c := range []struct{ arg, want string }{
			{"nightly", "nightly"},
			{"  nightly report \t", "nightly report"},
		} {
			got, err := parsePeriodicName([]string{c.arg}, op)
			if err != nil || got != c.want {
				t.Errorf("parsePeriodicName([%q], %q) = %q, %v; want %q", c.arg, op, got, err, c.want)
			}
		}
	}
}
