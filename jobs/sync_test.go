package jobs

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"kraken/store"
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

	id, err := st.CreateJob(&store.Job{Slug: j.Slug, Type: "info"})
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

	id, err := st.CreateJob(&store.Job{Slug: j.Slug, Type: "info"})
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

	id, err := st.CreateJob(&store.Job{Slug: j.Slug, Type: "info"})
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

	id, err := st.CreateJob(&store.Job{Slug: j.Slug, Type: "info"})
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

func TestSyncArtifactsNowNoopWithoutStore(t *testing.T) {
	// no UseStore call in this test: ActiveStore() is nil, so this must be
	// a pure no-op — in particular, it must not panic on a job with ID 0
	// and no live cell dir.
	syncArtifactsNow(&Job{Slug: "unwired"}, "test")
}

func TestSyncArtifactsNowNoopWithoutJobID(t *testing.T) {
	// useTestStore (not a hand-rolled UseStore+partial cleanup)
	// so the event sink util.SetEventSink wires is always unwired too — a
	// bare `activeStore.Store(nil)` cleanup left the sink pointed at this
	// test's closed store for any later test in the same process.
	useTestStore(t)
	// ID == 0 (a file-backed Job never wired to a store row): must no-op,
	// not attempt a sync against a bogus job id.
	syncArtifactsNow(&Job{Slug: "no-id"}, "test")
}
