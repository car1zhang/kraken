package main

import (
	"strings"
	"testing"
)

// cmd_orch_ack_test.go — `k orch ack`'s success line reports what the server
// STORED, never an echo of the input: the route caps a disposition silently,
// and an echo made the overrun invisible from inside kronos' loop.

func TestRenderOrchAckPrintsTheStoredDisposition(t *testing.T) {
	sent := "steered:csv-export -- a long explanation the server may cut"
	got := renderOrchAck([]byte(`{"ok":true,"disposition":"steered:csv-export -- a long","truncated":false}`), 2, sent)
	if got != "acked 2 items: steered:csv-export -- a long" {
		t.Fatalf("stored disposition not printed: %q", got)
	}
	if strings.Contains(got, "warning") {
		t.Fatalf("untruncated ack carries a warning: %q", got)
	}
}

func TestRenderOrchAckWarnsOnTruncation(t *testing.T) {
	sent := strings.Repeat("x", 300)
	stored := strings.Repeat("x", 199) + "…"
	got := renderOrchAck([]byte(`{"ok":true,"disposition":"`+stored+`","truncated":true}`), 1, sent)
	lines := strings.Split(got, "\n")
	if len(lines) != 2 {
		t.Fatalf("want two lines (ack + warning), got %q", got)
	}
	if lines[0] != "acked 1 item: "+stored {
		t.Fatalf("first line: %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "warning: disposition truncated") || !strings.Contains(lines[1], "200 runes") || !strings.Contains(lines[1], "of the 300 you sent") {
		t.Fatalf("warning line: %q", lines[1])
	}
}

// An older serve answering the bare {"ok":true} shape: the line says it is
// echoing the input rather than passing the echo off as what was stored.
func TestRenderOrchAckFallsBackToAMarkedEcho(t *testing.T) {
	got := renderOrchAck([]byte(`{"ok":true}`), 1, "ignored:noise")
	if got != "acked 1 item (stored text not reported; sent): ignored:noise" {
		t.Fatalf("fallback: %q", got)
	}
	if got := renderOrchAck([]byte(`not json`), 1, "ignored:noise"); !strings.Contains(got, "stored text not reported") {
		t.Fatalf("malformed body fallback: %q", got)
	}
}
