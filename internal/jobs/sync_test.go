package jobs

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"kraken/internal/store"
)

// writeArtifact writes data under j's live cell artifacts dir at rel
// (nested dirs created as needed), backdating mtime by age so tests can
// distinguish "unchanged since last sync" from "just written".
func writeArtifact(t *testing.T, j *Job, rel string, data []byte, age time.Duration) string {
	t.Helper()
	p := filepath.Join(CellArtifacts(j), rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	mt := time.Now().Add(-age)
	if err := os.Chtimes(p, mt, mt); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSyncCellArtifactsNestedAndBinary(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	st, err := store.Open(filepath.Join(t.TempDir(), "kraken.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	j := &Job{Slug: "sync-nested"}
	binary := []byte{0x00, 0xFF, 0x10, 0x00, 0x20}
	writeArtifact(t, j, "top.txt", []byte("hello"), time.Hour)
	writeArtifact(t, j, "icons/sub/icon.png", binary, time.Hour)

	id, err := st.CreateJob(&store.Job{Slug: j.Slug})
	if err != nil {
		t.Fatal(err)
	}

	if err := syncCellArtifacts(st, id, j); err != nil {
		t.Fatalf("sync: %v", err)
	}

	rows, err := st.ListArtifacts(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 artifact rows, got %d: %+v", len(rows), rows)
	}
	byPath := map[string]store.Artifact{}
	for _, r := range rows {
		byPath[r.Path] = r
	}
	if string(byPath["top.txt"].Bytes) != "hello" {
		t.Fatalf("top.txt bytes: %q", byPath["top.txt"].Bytes)
	}
	sub, ok := byPath["icons/sub/icon.png"]
	if !ok {
		t.Fatalf("nested path missing: %+v", byPath)
	}
	if string(sub.Bytes) != string(binary) {
		t.Fatalf("nested binary bytes mismatch: %v vs %v", sub.Bytes, binary)
	}
}

func TestSyncCellArtifactsVanishDeletesRow(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	st, err := store.Open(filepath.Join(t.TempDir(), "kraken.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	j := &Job{Slug: "sync-vanish"}
	p1 := writeArtifact(t, j, "keep.md", []byte("a"), time.Hour)
	writeArtifact(t, j, "gone.md", []byte("b"), time.Hour)

	id, err := st.CreateJob(&store.Job{Slug: j.Slug})
	if err != nil {
		t.Fatal(err)
	}
	if err := syncCellArtifacts(st, id, j); err != nil {
		t.Fatal(err)
	}
	if rows, _ := st.ListArtifacts(id); len(rows) != 2 {
		t.Fatalf("want 2 rows before removal, got %d", len(rows))
	}

	// gone.md vanishes from disk entirely (as it would after teardown's
	// cellDir removal, or a manual edit) — the next sync must mirror that
	// by deleting its row, true-mirror semantics (spec Q11).
	if err := os.Remove(filepath.Join(filepath.Dir(p1), "gone.md")); err != nil {
		t.Fatal(err)
	}
	if err := syncCellArtifacts(st, id, j); err != nil {
		t.Fatal(err)
	}
	rows, err := st.ListArtifacts(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Path != "keep.md" {
		t.Fatalf("want only keep.md left, got %+v", rows)
	}
}

func TestSyncCellArtifactsSkipsUnchangedBytes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	st, err := store.Open(filepath.Join(t.TempDir(), "kraken.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	j := &Job{Slug: "sync-unchanged"}
	writeArtifact(t, j, "stable.md", []byte("v1"), time.Hour)

	id, err := st.CreateJob(&store.Job{Slug: j.Slug})
	if err != nil {
		t.Fatal(err)
	}
	if err := syncCellArtifacts(st, id, j); err != nil {
		t.Fatal(err)
	}
	before, err := st.GetArtifact(id, "stable.md")
	if err != nil {
		t.Fatal(err)
	}

	// second sync with the file untouched (same mtime/size) must be a
	// pure metadata read — no re-read of file bytes required, and the
	// stored row (including its bytes) must come out byte-identical.
	if err := syncCellArtifacts(st, id, j); err != nil {
		t.Fatal(err)
	}
	after, err := st.GetArtifact(id, "stable.md")
	if err != nil {
		t.Fatal(err)
	}
	if string(after.Bytes) != string(before.Bytes) || after.Mtime != before.Mtime {
		t.Fatalf("unchanged artifact drifted: before=%+v after=%+v", before, after)
	}

	// now actually change it: new content, fresh mtime — must be picked up.
	writeArtifact(t, j, "stable.md", []byte("v2-longer-content"), 0)
	if err := syncCellArtifacts(st, id, j); err != nil {
		t.Fatal(err)
	}
	changed, err := st.GetArtifact(id, "stable.md")
	if err != nil {
		t.Fatal(err)
	}
	if string(changed.Bytes) != "v2-longer-content" {
		t.Fatalf("changed artifact not picked up: %+v", changed)
	}
}

// TestSyncCellArtifactsMissingRootSkipsSync covers L3: when the artifacts
// root doesn't exist at all (e.g. a resumed teardown re-running after
// cellDir was already removed by an earlier interrupted pass), the sync
// must be a pure no-op — pre-seeded rows survive untouched rather than
// being wiped as "everything vanished" (ListArtifacts silently returns []
// for a missing WalkDir root, which without a guard reads exactly like a
// mass deletion to SyncArtifacts' true-mirror logic).
func TestSyncCellArtifactsMissingRootSkipsSync(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	st, err := store.Open(filepath.Join(t.TempDir(), "kraken.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	j := &Job{Slug: "sync-missing-root"}
	writeArtifact(t, j, "keep.md", []byte("a"), time.Hour)

	id, err := st.CreateJob(&store.Job{Slug: j.Slug})
	if err != nil {
		t.Fatal(err)
	}
	if err := syncCellArtifacts(st, id, j); err != nil {
		t.Fatal(err)
	}
	if rows, _ := st.ListArtifacts(id); len(rows) != 1 {
		t.Fatalf("want 1 row before removal, got %d", len(rows))
	}

	// the artifacts root itself disappears entirely — not just the file.
	if err := os.RemoveAll(CellArtifacts(j)); err != nil {
		t.Fatal(err)
	}
	if err := syncCellArtifacts(st, id, j); err != nil {
		t.Fatalf("sync against a missing root must not error: %v", err)
	}
	rows, err := st.ListArtifacts(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Path != "keep.md" {
		t.Fatalf("pre-seeded rows must survive a missing artifacts root untouched, got %+v", rows)
	}
}

// TestRestoreCellArtifactsRoundTrip covers the reactivation half: after a
// teardown-shaped delete of the cell dir, restore must bring every stored
// row back — nested paths, binary bytes, and the stored mtime re-stamped so
// the next sync reads the restored files as unchanged instead of
// re-upserting identical blobs.
func TestRestoreCellArtifactsRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	st, err := store.Open(filepath.Join(t.TempDir(), "kraken.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	j := &Job{Slug: "restore-roundtrip"}
	binary := []byte{0x00, 0xFF, 0x10, 0x00, 0x20}
	writeArtifact(t, j, "00-overview.md", []byte("hello"), time.Hour)
	writeArtifact(t, j, "figures/sub/icon.png", binary, time.Hour)

	id, err := st.CreateJob(&store.Job{Slug: j.Slug})
	if err != nil {
		t.Fatal(err)
	}
	if err := syncCellArtifacts(st, id, j); err != nil {
		t.Fatal(err)
	}
	stored, err := st.ListArtifacts(id)
	if err != nil || len(stored) != 2 {
		t.Fatalf("want 2 stored rows, got %d (%v)", len(stored), err)
	}

	// teardown: the whole cell dir goes, the store keeps the mirror.
	// Reactivation restores BEFORE recreating any dirs — restore self-gates
	// on the root not existing, so it must run against the torn-down state.
	if err := os.RemoveAll(cellDir(j)); err != nil {
		t.Fatal(err)
	}
	if err := restoreCellArtifacts(st, id, j); err != nil {
		t.Fatalf("restore: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(CellArtifacts(j), "00-overview.md"))
	if err != nil || string(got) != "hello" {
		t.Fatalf("restored top-level file: %q, %v", got, err)
	}
	got, err = os.ReadFile(filepath.Join(CellArtifacts(j), "figures/sub/icon.png"))
	if err != nil || string(got) != string(binary) {
		t.Fatalf("restored nested binary: %v, %v", got, err)
	}

	// the stored mtime must be stamped back on, so a follow-up sync's
	// (path, mtime, size) check classifies every restored file as unchanged.
	byPath := map[string]store.Artifact{}
	for _, r := range stored {
		byPath[r.Path] = r
	}
	fi, err := os.Stat(filepath.Join(CellArtifacts(j), "00-overview.md"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.ModTime().UnixMilli() != byPath["00-overview.md"].Mtime {
		t.Fatalf("restored mtime %d != stored %d", fi.ModTime().UnixMilli(), byPath["00-overview.md"].Mtime)
	}

	// and the follow-up sync must leave the rows byte-identical — the
	// restore/sync pair is a fixed point, not a churn loop.
	if err := syncCellArtifacts(st, id, j); err != nil {
		t.Fatal(err)
	}
	after, err := st.ListArtifacts(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 2 {
		t.Fatalf("rows changed after restore+sync: %+v", after)
	}
	for _, r := range after {
		if prior := byPath[r.Path]; string(r.Bytes) != string(prior.Bytes) || r.Mtime != prior.Mtime {
			t.Fatalf("row %s drifted across restore+sync: before=%+v after=%+v", r.Path, prior, r)
		}
	}
}

// TestRestoreCellArtifactsSurvivingDirWins covers the mid-run "resume"
// reactivation whose cell dir survived: the dir is the CURRENT truth,
// deletions included — a path the mirror holds that the dir lacks is a file
// zeus deleted or renamed since the last sync, and restoring it would
// un-delete it (with the next sync re-mirroring the resurrected file as
// canonical). So restore into an existing root must be a complete no-op:
// nothing resurrected, nothing overwritten.
func TestRestoreCellArtifactsSurvivingDirWins(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	st, err := store.Open(filepath.Join(t.TempDir(), "kraken.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	j := &Job{Slug: "restore-surviving-dir"}
	id, err := st.CreateJob(&store.Job{Slug: j.Slug})
	if err != nil {
		t.Fatal(err)
	}
	// the mirror knows two files; on disk one was edited and one deleted
	// since that mirror was taken.
	if err := st.SyncArtifacts(id, []store.ArtifactFile{
		{Path: "live.md", Bytes: []byte("stale-mirror"), Mtime: 1},
		{Path: "deleted.md", Bytes: []byte("from-mirror"), Mtime: 2},
	}, nil); err != nil {
		t.Fatal(err)
	}

	writeArtifact(t, j, "live.md", []byte("newer-on-disk"), 0)
	if err := restoreCellArtifacts(st, id, j); err != nil {
		t.Fatalf("restore: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(CellArtifacts(j), "live.md"))
	if err != nil || string(got) != "newer-on-disk" {
		t.Fatalf("restore overwrote a live file: %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(CellArtifacts(j), "deleted.md")); !os.IsNotExist(err) {
		t.Fatalf("restore resurrected a file deleted since the last mirror: %v", err)
	}
}

// TestRestoreCellArtifactsRejectsNonLocalPath: stored paths come from our
// own walk and are always local, but a row is still external input to the
// restoring process — one carrying a traversal must be skipped, never
// written outside the artifacts dir.
func TestRestoreCellArtifactsRejectsNonLocalPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	st, err := store.Open(filepath.Join(t.TempDir(), "kraken.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	j := &Job{Slug: "restore-nonlocal"}
	id, err := st.CreateJob(&store.Job{Slug: j.Slug})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SyncArtifacts(id, []store.ArtifactFile{
		{Path: "../escape.md", Bytes: []byte("evil"), Mtime: 1},
		{Path: "ok.md", Bytes: []byte("fine"), Mtime: 2},
	}, nil); err != nil {
		t.Fatal(err)
	}

	if err := restoreCellArtifacts(st, id, j); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cellDir(j), "escape.md")); !os.IsNotExist(err) {
		t.Fatalf("non-local path escaped the artifacts dir: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(CellArtifacts(j), "ok.md")); err != nil || string(got) != "fine" {
		t.Fatalf("local sibling not restored: %q, %v", got, err)
	}
}

// TestRestoreCellArtifactsAbortRemovesPartialDir: a restore that fails
// mid-loop must remove everything it wrote. The root's existence is the
// gate for the next attempt, so a partial dir left behind would make every
// retry a silent no-op over a half-restored cell — whose next sync then
// deletes the rows that never landed. The rows "figures" (a file) +
// "figures/icon.md" force a deterministic mid-loop failure: ORDER BY path
// writes the file first, then MkdirAll fails on it standing where a
// directory must go — the same abort shape any mid-loop ENOSPC/EACCES has.
func TestRestoreCellArtifactsAbortRemovesPartialDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	st, err := store.Open(filepath.Join(t.TempDir(), "kraken.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	j := &Job{Slug: "restore-abort"}
	id, err := st.CreateJob(&store.Job{Slug: j.Slug})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SyncArtifacts(id, []store.ArtifactFile{
		{Path: "figures", Bytes: []byte("a file where a dir must go"), Mtime: 1},
		{Path: "figures/icon.md", Bytes: []byte("unreachable"), Mtime: 2},
	}, nil); err != nil {
		t.Fatal(err)
	}

	if err := restoreCellArtifacts(st, id, j); err == nil {
		t.Fatal("restore must fail on a file/dir path conflict")
	}
	if _, err := os.Stat(CellArtifacts(j)); !os.IsNotExist(err) {
		t.Fatalf("abort left a partial artifacts dir behind — the next attempt's gate would silently no-op: %v", err)
	}
	if rows, err := st.ListArtifacts(id); err != nil || len(rows) != 2 {
		t.Fatalf("abort must not touch the stored rows: %d, %v", len(rows), err)
	}

	// with the poisoned row gone, the retry starts from an absent root and
	// completes — the abort kept the failure retryable.
	if err := st.SyncArtifacts(id, []store.ArtifactFile{
		{Path: "figures/icon.md", Bytes: []byte("now writable"), Mtime: 2},
	}, nil); err != nil {
		t.Fatal(err)
	}
	if err := restoreCellArtifacts(st, id, j); err != nil {
		t.Fatalf("retry after fixing the row: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(CellArtifacts(j), "figures/icon.md")); err != nil || string(got) != "now writable" {
		t.Fatalf("retry did not complete the restore: %q, %v", got, err)
	}
}

// TestSyncCellArtifactsEmptiedRootSkipsSync covers the second half of L3:
// a root that EXISTS but has dropped to zero files while the store still
// holds rows. That is what a done-then-reactivate cycle (empty artifacts/
// in a fresh cell dir) and an out-of-session wipe both produce, and unlike
// the missing-root case it passes the os.Stat guard — so without its own
// guard the sync would mirror the loss and delete every row, turning a
// recoverable disk loss into a permanent one. Zero files with rows stored
// is never a legitimate mirror event on a live cell; the sync must no-op.
func TestSyncCellArtifactsEmptiedRootSkipsSync(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	st, err := store.Open(filepath.Join(t.TempDir(), "kraken.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	j := &Job{Slug: "sync-emptied-root"}
	p := writeArtifact(t, j, "keep.md", []byte("a"), time.Hour)

	id, err := st.CreateJob(&store.Job{Slug: j.Slug})
	if err != nil {
		t.Fatal(err)
	}
	if err := syncCellArtifacts(st, id, j); err != nil {
		t.Fatal(err)
	}
	if rows, _ := st.ListArtifacts(id); len(rows) != 1 {
		t.Fatalf("want 1 row before wipe, got %d", len(rows))
	}

	// every file goes, the dir itself stays — the emptied-but-present shape.
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := syncCellArtifacts(st, id, j); err != nil {
		t.Fatalf("sync against an emptied root must not error: %v", err)
	}
	rows, err := st.ListArtifacts(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Path != "keep.md" {
		t.Fatalf("stored rows must survive an emptied artifacts root untouched, got %+v", rows)
	}
}

// TestSyncCellArtifactsSkipsSymlinks is a regression pin: a symlink under
// artifacts/ — to a file outside it, or to a directory — is never stored,
// so its target's bytes cannot reach the table every non-live job is served
// from; the regular file beside it is. Both the ordinary and the teardown
// pass.
func TestSyncCellArtifactsSkipsSymlinks(t *testing.T) {
	for _, atTeardown := range []bool{false, true} {
		t.Setenv("HOME", t.TempDir())
		st, err := store.Open(filepath.Join(t.TempDir(), "kraken.db"))
		if err != nil {
			t.Fatal(err)
		}
		j := &Job{Slug: "sync-symlinks"}
		writeArtifact(t, j, "00-overview.md", []byte("real"), time.Hour)
		outside := t.TempDir()
		secret := filepath.Join(outside, "secret.md")
		if err := os.WriteFile(secret, []byte("SECRET"), 0o644); err != nil {
			t.Fatal(err)
		}
		root := CellArtifacts(j)
		if err := os.Symlink(secret, filepath.Join(root, "link.md")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(root, "linkdir")); err != nil {
			t.Fatal(err)
		}
		id, err := st.CreateJob(&store.Job{Slug: j.Slug})
		if err != nil {
			t.Fatal(err)
		}
		if err := mirrorCellArtifacts(st, id, j, atTeardown); err != nil {
			t.Fatalf("teardown=%v: sync: %v", atTeardown, err)
		}
		rows, err := st.ListArtifacts(id)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0].Path != "00-overview.md" || string(rows[0].Bytes) != "real" {
			t.Errorf("teardown=%v: rows = %+v; want only 00-overview.md", atTeardown, rows)
		}
		for _, r := range rows {
			if string(r.Bytes) == "SECRET" {
				t.Errorf("teardown=%v: %s stored a symlink target's bytes", atTeardown, r.Path)
			}
		}
		st.Close()
	}
}
