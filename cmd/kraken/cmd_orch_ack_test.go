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
	// 199, not 200: the stored form's last rune is the server's own ellipsis,
	// so the callers'-runes-kept figure excludes it.
	if !strings.HasPrefix(lines[1], "warning: disposition truncated") || !strings.Contains(lines[1], "the first 199 of the 300 runes you sent") {
		t.Fatalf("warning line: %q", lines[1])
	}
}

// The "you sent" count is measured with the server's own one-liner, so an
// input carrying control runes — which TruncateOneLine maps to spaces and
// collapses, and strings.Fields does not — counts the same on both sides.
func TestRenderOrchAckSentCountMatchesTheServersOneLining(t *testing.T) {
	sent := "a \x1b\x1b b" + strings.Repeat("y", 300)
	stored := strings.Repeat("z", 199) + "…"
	got := renderOrchAck([]byte(`{"ok":true,"disposition":"`+stored+`","truncated":true}`), 1, sent)
	// "a \x1b\x1b byyy…" one-lines to "a byyy…": the controls become spaces
	// and collapse into the single separator, so the count is
	// 1 ("a") + 1 (space) + 301 ("b" + 300 y's) = 303 — where a Fields-join
	// that never mapped the controls would keep them as two extra runes plus
	// their separator and say 306.
	want := "of the 303 runes you sent"
	if !strings.Contains(got, want) {
		t.Fatalf("sent count diverges from the server's one-lining: %q (want %q)", got, want)
	}
}

// An older serve answering the bare {"ok":true} shape: the line says it is
// echoing the input rather than passing the echo off as what was stored. A
// PRESENT-but-empty disposition is a different answer — a current server
// really stored nothing — and must not be reported as an old server.
func TestRenderOrchAckFallsBackOnlyOnAnAbsentKey(t *testing.T) {
	got := renderOrchAck([]byte(`{"ok":true}`), 1, "ignored:noise")
	if got != "acked 1 item (stored text not reported; sent): ignored:noise" {
		t.Fatalf("fallback: %q", got)
	}
	if got := renderOrchAck([]byte(`not json`), 1, "ignored:noise"); !strings.Contains(got, "stored text not reported") {
		t.Fatalf("malformed body fallback: %q", got)
	}
	if got := renderOrchAck([]byte(`{"ok":true,"disposition":"","truncated":false}`), 1, "ignored:noise"); got != "acked 1 item: " {
		t.Fatalf("present-but-empty disposition misread as an old server: %q", got)
	}
}
