package main

// mmflagecho_edge_test.go — athena-edge's BLIND tests for slice F of
// dream-2026-10-30 (`kraken mm flag` echoes the flag id and the item id
// apart): mmFlagEcho at its numeric and string boundaries. Authored from
// 50-contract.md alone. Every package-level identifier is prefixed edgeMF.

import (
	"math"
	"strings"
	"testing"
)

func TestEdgeMMFlagEcho(t *testing.T) {
	cases := []struct {
		name   string
		flagID int64
		state  string
		itemID int64
		want   string
	}{
		{"contract example", 7, "raised", 42, "flag 7 (raised) on mm #42"},
		{"pending", 3, "pending", 3, "flag 3 (pending) on mm #3"},
		{"same ids stay distinct fields", 12, "raised", 12, "flag 12 (raised) on mm #12"},
		{"zero ids, empty state", 0, "", 0, "flag 0 () on mm #0"},
		{"negative ids", -1, "raised", -5, "flag -1 (raised) on mm #-5"},
		{"max int64", math.MaxInt64, "dismissed", math.MaxInt64, "flag 9223372036854775807 (dismissed) on mm #9223372036854775807"},
		{"min int64 item", 1, "raised", math.MinInt64, "flag 1 (raised) on mm #-9223372036854775808"},
		{"state with a space kept verbatim", 4, "pending review", 9, "flag 4 (pending review) on mm #9"},
		{"state with parens kept verbatim", 4, "a(b)", 9, "flag 4 (a(b)) on mm #9"},
		{"unicode state", 8, "réglé", 2, "flag 8 (réglé) on mm #2"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := mmFlagEcho(c.flagID, c.state, c.itemID)
			if got != c.want {
				t.Errorf("mmFlagEcho(%d, %q, %d) = %q, want %q", c.flagID, c.state, c.itemID, got, c.want)
			}
			if strings.Contains(got, "flagged") {
				t.Errorf("mmFlagEcho = %q still carries the retired `flagged` form", got)
			}
			if strings.HasSuffix(got, "\n") {
				t.Errorf("mmFlagEcho = %q, want no trailing newline", got)
			}
		})
	}
}
