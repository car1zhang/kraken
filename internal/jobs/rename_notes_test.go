package jobs

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"kraken/internal/store"
)

// rename_notes_test.go — the Notes arm of the round-3 contract amendment to
// jobs.RenameJob (50-contract.md, slice A): "a copy whose rename FAILED
// outright is not a conflict and goes to `Notes` with its cause".
//
// Lead-sanctioned and post-join authored (the blind authors were gone by the
// time the amendment landed), so it is in its own file rather than inside
// either blind one — same disclosure convention as
// internal/store/rename_edge_test.go's TestEdgeUnarchiveJobHonoursTheFormerSlugReservation.
// It exists because the amendment created a behaviour neither blind file
// exercises: both of them construct a name CONFLICT (a file already at the
// destination), which is the Err == nil half of moveSkip. The Err != nil half
// — Skipped stays empty, Notes carries the cause — had no test at all, and it
// is the half where the two channels could silently collapse back into one.
//
// Helpers are `notesRen`-prefixed, so this file cannot collide with the
// `contractRename` and `edgeRen` sets sharing the package.
//
// Both tests rest on chmod 0555 stopping os.Rename, which does not constrain
// ROOT: run as root, the rename succeeds, Notes stays empty, and both tests
// fail loudly (the safe direction — a false green is impossible, a puzzled
// root just reads this line).

// notesRenJob creates one active job and returns it as a real caller holds it.
func notesRenJob(t *testing.T, st *store.Store, slug string) *Job {
	t.Helper()
	mustCreateJob(t, st, store.Job{Slug: slug, Repo: "monolith"})
	j := LoadJobBySlug(slug)
	if j == nil {
		t.Fatalf("fixture job %q did not load", slug)
	}
	return j
}

// TestRenameFailedFlatCopyMoveGoesToNotesNotSkipped drives the amendment's
// Notes obligation with a rename that genuinely CANNOT happen.
//
// The failure is induced with chmod 0555 on the pluto dir rather than with a
// fake filesystem: os.Rename needs WRITE permission on the containing
// directory while os.ReadDir and os.Stat need only read+execute, so a
// read-only dir is the one state where movePrefixed still enumerates its
// entries and still resolves its destination — reaching the os.Rename that
// then fails. A missing dir or an unreadable one would make movePrefixed
// return early and test nothing.
//
// Two assertions, and the pair is the point rather than either alone: the
// copy must appear in Notes WITH its cause, and it must be absent from
// Skipped. Skipped is what both clients render under a sentence saying
// something already sits at the new name, which is false of this file —
// nothing sits there, the move simply could not be performed.
func TestRenameFailedFlatCopyMoveGoesToNotesNotSkipped(t *testing.T) {
	st := useTestStore(t)
	pluto := t.TempDir()
	t.Setenv("KRAKEN_PLUTO_DIR", pluto)

	j := notesRenJob(t, st, "notes-ren-fail")
	oldPrefix := cellName(j)
	stranded := filepath.Join(pluto, oldPrefix+".00-overview.md")
	if err := os.WriteFile(stranded, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// read+execute but not write: ReadDir and Stat still work, Rename cannot.
	if err := os.Chmod(pluto, 0o555); err != nil {
		t.Fatal(err)
	}
	// restored before TempDir's own cleanup, which needs write to remove.
	t.Cleanup(func() { _ = os.Chmod(pluto, 0o755) })

	res, err := RenameJob(j, "notes-ren-fail-new")
	if err != nil {
		t.Fatalf("a failed flat-copy move must not fail the rename: %v", err)
	}
	if res == nil {
		t.Fatal("nil result on success")
	}
	if res.Slug != "notes-ren-fail-new" {
		t.Fatalf("result slug = %q, want the rename to have happened anyway", res.Slug)
	}

	if len(res.Skipped) != 0 {
		t.Errorf("Skipped = %v, want empty — a rename FAILURE is not a name conflict, and Skipped is the list the clients describe as one", res.Skipped)
	}

	joined := strings.Join(res.Notes, "\n")
	if !strings.Contains(joined, oldPrefix+".00-overview.md") {
		t.Errorf("Notes = %v, want it to name the copy that could not move (%s)", res.Notes, oldPrefix+".00-overview.md")
	}
	// "with its cause" — the note must carry more than the path, or the
	// channel reports the same nothing the old mislabelled Skipped entry did.
	if !strings.Contains(joined, "failed") {
		t.Errorf("Notes = %v, want the failure's cause carried with it", res.Notes)
	}

	// the file is still where it was: a failed move must not lose it.
	if _, err := os.Stat(stranded); err != nil {
		t.Errorf("the un-moved copy is gone: %v", err)
	}
}

// TestRenameSeparatesConflictsFromFailures pins the two channels apart in ONE
// call, which is the shape a future "simplify these back into one list" edit
// would break while both single-arm tests stayed green: a conflicting copy and
// an unmovable copy in the same rename must land in different lists.
//
// Read-only pluto makes EVERY move fail, so the conflict here is built the
// only way that still separates the two arms — movePrefixed checks the
// destination's existence BEFORE attempting the rename, so a copy whose
// destination exists is classified as a conflict without an os.Rename ever
// being tried, while its sibling reaches the rename and fails.
func TestRenameSeparatesConflictsFromFailures(t *testing.T) {
	st := useTestStore(t)
	pluto := t.TempDir()
	t.Setenv("KRAKEN_PLUTO_DIR", pluto)

	j := notesRenJob(t, st, "notes-ren-both")
	oldPrefix := cellName(j)
	newPrefix := "notes-ren-both-new-" + strconv.FormatInt(j.ID, 10)

	write := func(name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(pluto, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(oldPrefix + ".10-conflict.md") // destination occupied → conflict
	write(newPrefix + ".10-conflict.md")
	write(oldPrefix + ".20-unmovable.md") // destination free → rename attempted

	if err := os.Chmod(pluto, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(pluto, 0o755) })

	res, err := RenameJob(j, "notes-ren-both-new")
	if err != nil {
		t.Fatalf("RenameJob: %v", err)
	}

	skipped, notes := strings.Join(res.Skipped, "\n"), strings.Join(res.Notes, "\n")
	if !strings.Contains(skipped, oldPrefix+".10-conflict.md") {
		t.Errorf("Skipped = %v, want the conflicting copy's stranded SOURCE", res.Skipped)
	}
	if strings.Contains(skipped, ".20-unmovable.md") {
		t.Errorf("Skipped = %v, want the unmovable copy OUT of it — it is not a conflict", res.Skipped)
	}
	if !strings.Contains(notes, oldPrefix+".20-unmovable.md") {
		t.Errorf("Notes = %v, want the unmovable copy in it", res.Notes)
	}
	if strings.Contains(notes, ".10-conflict.md") {
		t.Errorf("Notes = %v, want the conflicting copy OUT of it — Skipped is its channel", res.Notes)
	}
}
