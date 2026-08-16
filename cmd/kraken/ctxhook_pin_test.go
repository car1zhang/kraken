package main

import (
	"os"
	"testing"
)

// Lead-authored SIGHTED regression pin, from the kronos live demo
// (2026-08-15): installKronosSettings writes `--serve-addr <addr>` into the
// ctx-hook and rearm-hook command strings (hookCommandFlags, the E7 delivery
// every hook verb uses), but both verbs shipped ignoring their argv — so on a
// parallel server every statusline report silently went to the DEFAULT port
// and the context meter read as never-reported, while on the real deployment
// nothing was visibly wrong. The pin holds the property the demo caught
// missing: the flag family reaches the env jobs.ServeBaseURL reads.
func TestCtxHookFlagsReachTheEnv(t *testing.T) {
	t.Setenv("KRAKEN_SERVE_ADDR", "")
	t.Setenv("KRAKEN_DB_PATH", "")

	ctxHookFlags([]string{"--serve-addr", "127.0.0.1:9999", "--db-path", "/tmp/pin.db"})
	if got := os.Getenv("KRAKEN_SERVE_ADDR"); got != "127.0.0.1:9999" {
		t.Fatalf("KRAKEN_SERVE_ADDR = %q, want the flag's value — the hook posts to the wrong daemon without it", got)
	}
	if got := os.Getenv("KRAKEN_DB_PATH"); got != "/tmp/pin.db" {
		t.Fatalf("KRAKEN_DB_PATH = %q, want the flag's value", got)
	}
}

// The never-fail posture: junk argv shapes must be ignored, never fatal — a
// hook that dies on an argument fails the turn it fired on. A trailing half
// flag returns early by design and must not disturb what a full flag already
// set.
func TestCtxHookFlagsIgnoreJunkAndHalfFlags(t *testing.T) {
	t.Setenv("KRAKEN_SERVE_ADDR", "")

	ctxHookFlags([]string{"unexpected-bare-word", "--serve-addr", "127.0.0.1:8888", "--db-path"})
	if got := os.Getenv("KRAKEN_SERVE_ADDR"); got != "127.0.0.1:8888" {
		t.Fatalf("KRAKEN_SERVE_ADDR = %q, want the full flag honoured despite junk neighbours", got)
	}
}
