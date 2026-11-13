package jobs

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"kraken/internal/store"
)

// periodicfields_test.go — regression pin for review round 1 of periodic
// jobs: createPeriodicJob dropped a template's `visibility:` and `color:`
// lines, so a template written `visibility: private` fired readonly jobs
// every other node could see. Both a scheduled firing and a run-now go
// through the same create, and both are checked.

const periodicFieldsTemplate = "---\nrepo: kraken\nvisibility: private\ncolor: blue\n---\nRun {{slug}}.\n"

func periodicFieldsEnv(t *testing.T) *store.Store {
	t.Helper()
	st := useTestStore(t)
	repos := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repos, "kraken", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KRAKEN_REPOS_DIR", repos)
	prevNotify, prevSubmit := periodicNotify, periodicSubmit
	periodicNotify = func(title, msg string) {}
	periodicSubmit = func(j *Job) error { return nil }
	t.Cleanup(func() { periodicNotify, periodicSubmit = prevNotify, prevSubmit })
	return st
}

func periodicFieldsCheck(t *testing.T, st *store.Store, slug string) {
	t.Helper()
	row, err := st.GetJobBySlug(slug)
	if err != nil {
		t.Fatalf("GetJobBySlug(%s): %v", slug, err)
	}
	if row.Visibility != "private" {
		t.Errorf("%s: visibility = %q, want private (from the template)", slug, row.Visibility)
	}
	if row.Color != "blue" {
		t.Errorf("%s: color = %q, want blue (from the template)", slug, row.Color)
	}
}

func TestPeriodicFieldsCarryVisibilityAndColor(t *testing.T) {
	st := periodicFieldsEnv(t)
	loc := time.FixedZone("fields", 0)
	added := time.Date(2026, 11, 12, 8, 0, 0, 0, loc)
	if _, err := AddPeriodic("nightly notes", "notes", "09:00", "", periodicFieldsTemplate, added); err != nil {
		t.Fatalf("AddPeriodic: %v", err)
	}

	now := time.Date(2026, 11, 12, 9, 30, 0, 0, loc)
	if n := periodicPass(now, func(*Job) bool { return true }); n != 1 {
		t.Fatalf("periodicPass started %d, want 1", n)
	}
	periodicFieldsCheck(t, st, "notes-2026-11-12")

	run, err := RunPeriodicNow("nightly notes", now)
	if err != nil {
		t.Fatalf("RunPeriodicNow: %v", err)
	}
	if run.Slug != "notes-2026-11-12-run" {
		t.Fatalf("run slug = %q, want notes-2026-11-12-run", run.Slug)
	}
	periodicFieldsCheck(t, st, run.Slug)
}
