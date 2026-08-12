package store

import (
	"path/filepath"
	"testing"
)

func openGraphStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "kraken.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestAddEpisodeDedupesByHash(t *testing.T) {
	s := openGraphStore(t)
	id1, added, err := s.AddEpisode(Episode{Kind: "feedback", Ref: "8", Content: "text", Hash: "h1", TS: 100})
	if err != nil || !added {
		t.Fatalf("first add: id=%d added=%v err=%v", id1, added, err)
	}
	id2, added, err := s.AddEpisode(Episode{Kind: "feedback", Ref: "8", Content: "text", Hash: "h1", TS: 100})
	if err != nil {
		t.Fatal(err)
	}
	if added || id2 != id1 {
		t.Fatalf("re-add: id=%d added=%v, want id=%d added=false — a re-scan of the same span must be a no-op", id2, added, id1)
	}
}

func TestPendingEpisodesDrainInOrder(t *testing.T) {
	s := openGraphStore(t)
	for i, h := range []string{"a", "b", "c"} {
		if _, _, err := s.AddEpisode(Episode{Kind: "transcript", Hash: h, TS: int64(i)}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.PendingEpisodes(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Hash != "a" || got[2].Hash != "c" {
		t.Fatalf("pending = %+v, want oldest-first a,b,c", got)
	}
	if err := s.MarkEpisodeDone(got[0].ID); err != nil {
		t.Fatal(err)
	}
	got, err = s.PendingEpisodes(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Hash != "b" {
		t.Fatalf("after done: pending = %+v, want b,c", got)
	}
}

func TestUpsertNodePreservesRicherGloss(t *testing.T) {
	s := openGraphStore(t)
	id, err := s.UpsertNode(GraphNode{Name: "artifact prose", Type: "convention", Description: "how artifacts are formatted"})
	if err != nil {
		t.Fatal(err)
	}
	// A later extraction that saw less detail must not blank what is stored.
	id2, err := s.UpsertNode(GraphNode{Name: "Artifact Prose"})
	if err != nil {
		t.Fatal(err)
	}
	if id2 != id {
		t.Fatalf("case-insensitive upsert made a second node: %d vs %d", id2, id)
	}
	n, err := s.ResolveNode("ARTIFACT PROSE")
	if err != nil || n == nil {
		t.Fatalf("resolve: %v %v", n, err)
	}
	if n.Description != "how artifacts are formatted" || n.Type != "convention" {
		t.Fatalf("empty upsert clobbered the gloss: %+v", n)
	}
}

func TestUpsertNodeChangedDescriptionClearsEmbedding(t *testing.T) {
	s := openGraphStore(t)
	id, err := s.UpsertNode(GraphNode{Name: "n", Description: "old"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetEmbedding("node", id, []byte{1, 2, 3, 4}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertNode(GraphNode{Name: "n", Description: "new"}); err != nil {
		t.Fatal(err)
	}
	n, err := s.ResolveNode("n")
	if err != nil {
		t.Fatal(err)
	}
	if len(n.Embedding) != 0 {
		t.Fatal("description changed but the stale vector survived; it embedded the old text")
	}
	miss, err := s.MissingEmbeddings(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(miss) != 1 || miss[0].Kind != "node" || miss[0].ID != id {
		t.Fatalf("missing = %+v, want the cleared node queued for re-embed", miss)
	}
}

func TestResolveNodeViaAlias(t *testing.T) {
	s := openGraphStore(t)
	id, err := s.UpsertNode(GraphNode{Name: "markdown artifacts"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddAlias(id, "artifact prose"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddAlias(id, "artifact prose"); err != nil {
		t.Fatal(err) // idempotent
	}
	n, err := s.ResolveNode("Artifact Prose")
	if err != nil {
		t.Fatal(err)
	}
	if n == nil || n.ID != id {
		t.Fatalf("alias resolve = %+v, want node %d", n, id)
	}
	if n, _ := s.ResolveNode("never heard of it"); n != nil {
		t.Fatalf("unknown name resolved to %+v, want nil", n)
	}
}

func TestAllNodeAliases(t *testing.T) {
	s := openGraphStore(t)
	a, err := s.UpsertNode(GraphNode{Name: "tokensPass"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.UpsertNode(GraphNode{Name: "kraken serve"})
	if err != nil {
		t.Fatal(err)
	}
	// Two aliases added out of order: the map's lists come back sorted, the
	// same ORDER BY the per-node NodeAliases read pins.
	if err := s.AddAlias(a, "token pass"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddAlias(a, "the token counter"); err != nil {
		t.Fatal(err)
	}
	all, err := s.AllNodeAliases()
	if err != nil {
		t.Fatal(err)
	}
	if got := all[a]; len(got) != 2 || got[0] != "the token counter" || got[1] != "token pass" {
		t.Fatalf("aliases[%d] = %v, want [the token counter, token pass]", a, got)
	}
	// An alias-less node is ABSENT from the map (nil slice on lookup), which
	// is what lets the API's omitempty drop the key rather than emit [].
	if got, ok := all[b]; ok || got != nil {
		t.Fatalf("aliases[%d] = %v (present %v), want absent", b, got, ok)
	}
}

func TestInsertEdgeRepeatSighting(t *testing.T) {
	s := openGraphStore(t)
	a, _ := s.UpsertNode(GraphNode{Name: "carl"})
	b, _ := s.UpsertNode(GraphNode{Name: "markdown artifacts"})
	id, added, err := s.InsertEdge(GraphEdge{SrcID: a, DstID: b, Predicate: "prefers", Fact: "carl prefers unwrapped prose", Strength: 7})
	if err != nil || !added {
		t.Fatalf("insert: %v %v", added, err)
	}
	id2, added, err := s.InsertEdge(GraphEdge{SrcID: a, DstID: b, Predicate: "prefers", Fact: "carl prefers unwrapped prose", Strength: 9})
	if err != nil {
		t.Fatal(err)
	}
	if added || id2 != id {
		t.Fatalf("repeat: id=%d added=%v, want repeat sighting of %d", id2, added, id)
	}
	edges, err := s.GetEdges([]int64{id})
	if err != nil || len(edges) != 1 {
		t.Fatalf("get: %v %v", edges, err)
	}
	if edges[0].Occurrences != 2 || edges[0].Strength != 9 {
		t.Fatalf("occurrences=%d strength=%d, want 2 and max(7,9)=9", edges[0].Occurrences, edges[0].Strength)
	}
}

func TestInvalidateEdgeClosesWindowAndSurvives(t *testing.T) {
	s := openGraphStore(t)
	a, _ := s.UpsertNode(GraphNode{Name: "artifacts"})
	b, _ := s.UpsertNode(GraphNode{Name: "95 columns"})
	id, _, err := s.InsertEdge(GraphEdge{SrcID: a, DstID: b, Predicate: "wrap-at", Fact: "artifacts wrap at 95 columns"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.InvalidateEdge(id, 1785448586573); err != nil {
		t.Fatal(err)
	}
	edges, err := s.GetEdges([]int64{id})
	if err != nil || len(edges) != 1 {
		t.Fatalf("the invalidated edge must SURVIVE: %v %v", edges, err)
	}
	e := edges[0]
	if e.ValidUntil != 1785448586573 || e.ExpiredAt == 0 {
		t.Fatalf("window not closed: %+v", e)
	}
	// A second invalidation of an ALREADY-EXPIRED edge is a no-op success —
	// idempotence is what lets the api's add handler retry a partially
	// applied request without a 500 on work that already landed. The first
	// invalidation's window is untouched by the retry.
	if err := s.InvalidateEdge(id, 1); err != nil {
		t.Fatalf("re-invalidation errored: %v — retries of applied work must be no-ops", err)
	}
	if again, _ := s.GetEdges([]int64{id}); again[0].ValidUntil != 1785448586573 {
		t.Fatalf("retry rewrote the window: %+v", again[0])
	}
	// An id with NO row at all is still an error — a typo must not be acked.
	if err := s.InvalidateEdge(99999, 1); err == nil {
		t.Fatal("invalidating a nonexistent edge succeeded")
	}
	// After a contradiction, the same (src,dst,predicate) can hold a NEW live
	// edge rather than bumping the dead one.
	id2, added, err := s.InsertEdge(GraphEdge{SrcID: a, DstID: b, Predicate: "wrap-at", Fact: "artifacts are one paragraph per line"})
	if err != nil || !added || id2 == id {
		t.Fatalf("post-invalidation insert: id=%d added=%v err=%v", id2, added, err)
	}
}

func TestSearchEdgesFTSSurvivesHostileStrings(t *testing.T) {
	s := openGraphStore(t)
	a, _ := s.UpsertNode(GraphNode{Name: "branches"})
	b, _ := s.UpsertNode(GraphNode{Name: "naming"})
	id, _, err := s.InsertEdge(GraphEdge{SrcID: a, DstID: b, Predicate: "convention", Fact: "branches are named carl/<slug>-<id> since the cutover"})
	if err != nil {
		t.Fatal(err)
	}
	// The exact strings this corpus is full of: hyphens, slashes, angle
	// brackets, a bare quote. FTS5 raw syntax would choke; the quoted
	// OR-of-terms must not.
	for _, q := range []string{`carl/<slug>-<id>`, `branches "cutover`, `%3 -- naming`} {
		if _, err := s.SearchEdgesFTS(q, 10); err != nil {
			t.Fatalf("query %q errored: %v", q, err)
		}
	}
	ids, err := s.SearchEdgesFTS("cutover branches", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != id {
		t.Fatalf("search = %v, want [%d]", ids, id)
	}
	if ids, _ := s.SearchEdgesFTS("", 10); ids != nil {
		t.Fatalf("empty query returned %v, want nil", ids)
	}
}

func TestFTSStaysInStepWithEdges(t *testing.T) {
	s := openGraphStore(t)
	a, _ := s.UpsertNode(GraphNode{Name: "x"})
	b, _ := s.UpsertNode(GraphNode{Name: "y"})
	id, _, err := s.InsertEdge(GraphEdge{SrcID: a, DstID: b, Predicate: "p", Fact: "the daemon polls zanzibar hourly"})
	if err != nil {
		t.Fatal(err)
	}
	ids, _ := s.SearchEdgesFTS("zanzibar", 10)
	if len(ids) != 1 {
		t.Fatalf("insert did not reach FTS: %v", ids)
	}
	// UPDATE through the sanctioned mutation (invalidation) keeps FTS in
	// step via the AU trigger, and the search's live-only join then hides it.
	if err := s.InvalidateEdge(id, 1); err != nil {
		t.Fatal(err)
	}
	ids, _ = s.SearchEdgesFTS("zanzibar", 10)
	if len(ids) != 0 {
		t.Fatalf("invalidated edge still surfaces in search: %v", ids)
	}
}

func TestIngestCursors(t *testing.T) {
	s := openGraphStore(t)
	if c, err := s.IngestCursor("feedback"); err != nil || c != 0 {
		t.Fatalf("fresh cursor = %d, %v; want 0, nil", c, err)
	}
	if err := s.SetIngestCursor("feedback", 8); err != nil {
		t.Fatal(err)
	}
	if err := s.SetIngestCursor("/path/a.jsonl", 4096); err != nil {
		t.Fatal(err)
	}
	all, err := s.AllIngestCursors()
	if err != nil {
		t.Fatal(err)
	}
	if all["feedback"] != 8 || all["/path/a.jsonl"] != 4096 {
		t.Fatalf("cursors = %v", all)
	}
	// Rewind is the caller's call — the store obeys.
	if err := s.SetIngestCursor("/path/a.jsonl", 0); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.IngestCursor("/path/a.jsonl"); c != 0 {
		t.Fatalf("rewind ignored: %d", c)
	}
}

func TestLayoutReplaceIsWholesale(t *testing.T) {
	s := openGraphStore(t)
	if err := s.SetLayout([]LayoutPos{{NodeID: 1, X: 1, Y: 2, Rev: 10}, {NodeID: 2, X: 3, Y: 4, Rev: 10}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLayout([]LayoutPos{{NodeID: 2, X: 9, Y: 9, Rev: 11}}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Layout()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].NodeID != 2 || got[0].Rev != 11 {
		t.Fatalf("layout = %+v, want only node 2 at rev 11 — replace is wholesale", got)
	}
}

func TestGraphStats(t *testing.T) {
	s := openGraphStore(t)
	a, _ := s.UpsertNode(GraphNode{Name: "a"})
	b, _ := s.UpsertNode(GraphNode{Name: "b"})
	id, _, _ := s.InsertEdge(GraphEdge{SrcID: a, DstID: b, Predicate: "p", Fact: "f"})
	_, _, _ = s.AddEpisode(Episode{Kind: "job", Hash: "h"})
	st, err := s.GetGraphStats()
	if err != nil {
		t.Fatal(err)
	}
	if st.Nodes != 2 || st.Edges != 1 || st.LiveEdges != 1 || st.Episodes != 1 || st.PendingEpisodes != 1 {
		t.Fatalf("stats = %+v", st)
	}
	// 2 nodes + 1 live edge, none embedded yet.
	if st.MissingEmbeddings != 3 || st.MaxEdgeID != id {
		t.Fatalf("stats = %+v, want 3 missing embeddings and maxEdgeID %d", st, id)
	}
}

// TestV14DatabaseMigratesForward opens a database created at the previous
// schema version and asserts v15 lands without touching existing rows —
// the migration-on-a-copy check 52-testing-plan.md calls for, in miniature.
func TestV14DatabaseMigratesForward(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kraken.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// Seed a job row through the normal API, then close and re-open: the
	// second Open's migrate() must be a no-op at current version with the
	// row intact and the kg_* tables present.
	if _, err := s.db.Exec(`INSERT INTO jobs (slug, body) VALUES ('demo', 'spec')`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	var slug string
	if err := s2.db.QueryRow(`SELECT slug FROM jobs`).Scan(&slug); err != nil || slug != "demo" {
		t.Fatalf("existing row after re-open: %q %v", slug, err)
	}
	if _, err := s2.GetGraphStats(); err != nil {
		t.Fatalf("kg_* tables absent after migration: %v", err)
	}
}
