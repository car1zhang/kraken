package api

import (
	"path/filepath"
	"testing"

	"kraken/internal/jobs"
	"kraken/internal/store"
)

// useTestStore opens a temp-file store, wires the jobs package onto it
// (jobs.UseStore — the same call kraken serve's cmd_serve.go makes at
// startup), and unwires it on test cleanup so a store-backed test never
// leaks into a later, unrelated file-mode test in this package's test
// binary. Mirrors jobs/store_test.go's helper of the same name/shape,
// duplicated here since it's a test-only unexported helper in a different
// package with no exported "unwire" of its own.
//
// S13: jobs.UseStore(nil) already clears the event-log sink itself
// (jobs/store.go: `if st == nil { util.SetEventSink(nil); ... }`), so a
// separate util.SetEventSink(nil) here was always redundant — a stale
// belt-and-suspenders left over from before UseStore did that internally.
func useTestStore(t *testing.T) *store.Store {
	t.Helper()
	t.Setenv("HOME", t.TempDir()) // sandbox: any accidental file-path fallthrough hits a throwaway dir
	st, err := store.Open(filepath.Join(t.TempDir(), "kraken.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	jobs.UseStore(st)
	t.Cleanup(func() {
		jobs.UseStore(nil)
		st.Close()
	})
	return st
}
