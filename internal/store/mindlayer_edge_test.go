package store

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// mindlayer_edge_test.go — the EDGE dimension of 50-contract.md slice A (PR 2,
// the mental-model rework, schema v33) for package store. Written blind from
// the contract: schema.go, mm.go, mmflags.go and threads.go were not read.
// What lives here is the refusals, the boundaries and the idempotence — a third
// Open, a bad scope/source, a revise of an unknown or invalidated id, every
// refused flag transition, the Dir ancestor walk, Q over all three columns,
// and an unknown session. athena-logic owns the happy paths.
//
// Every package-level identifier is prefixed `edgeML`.

// edgeMLOpen is the contract's "Test environment obligations": HOME isolated,
// KRAKEN_DB_PATH / KRAKEN_THREAD_SESSION / KRAKEN_THREAD_ROLE unset before any
// store opens.
func edgeMLOpen(t *testing.T) (*Store, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KRAKEN_DB_PATH", "")
	t.Setenv("KRAKEN_THREAD_SESSION", "")
	t.Setenv("KRAKEN_THREAD_ROLE", "")
	path := filepath.Join(t.TempDir(), "kraken.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

const edgeMLNow int64 = 1_780_000_000_000

func edgeMLItem(content string) MMItem {
	return MMItem{Kind: "note", Source: "manual", Content: content, CreatedAt: edgeMLNow}
}

func edgeMLAdd(t *testing.T, s *Store, it MMItem) int64 {
	t.Helper()
	id, err := s.AddMMItem(it)
	if err != nil {
		t.Fatalf("AddMMItem(%+v): %v", it, err)
	}
	return id
}

func edgeMLCount(t *testing.T, s *Store) int {
	t.Helper()
	items, err := s.ListMMItems(true)
	if err != nil {
		t.Fatalf("ListMMItems: %v", err)
	}
	return len(items)
}

func edgeMLIDs(items []MMItem) []int64 {
	out := make([]int64, 0, len(items))
	for _, it := range items {
		out = append(out, it.ID)
	}
	return out
}

func edgeMLSameIDSet(got []int64, want ...int64) bool {
	if len(got) != len(want) {
		return false
	}
	seen := map[int64]int{}
	for _, id := range got {
		seen[id]++
	}
	for _, id := range want {
		if seen[id] == 0 {
			return false
		}
		seen[id]--
	}
	return true
}

func edgeMLFlag(t *testing.T, s *Store, f MMFlag) int64 {
	t.Helper()
	if f.CreatedAt == 0 {
		f.CreatedAt = edgeMLNow
	}
	id, err := s.AddMMFlag(f)
	if err != nil {
		t.Fatalf("AddMMFlag(%+v): %v", f, err)
	}
	return id
}

func edgeMLGetFlag(t *testing.T, s *Store, id int64) MMFlag {
	t.Helper()
	f, err := s.GetMMFlag(id)
	if err != nil {
		t.Fatalf("GetMMFlag(%d): %v", id, err)
	}
	return f
}

// ---------------------------------------------------------------------------
// schemaVersion / migration idempotence
// ---------------------------------------------------------------------------

// "A v32 database migrates to 33 in one `Open`; a fresh database lands at 33;
// a second `Open` is a no-op." — taken one step further: a third and fourth
// Open after the migrating one leave the version and a v33-shaped row (title,
// scope, quote, source_json, thread_session) exactly as they were.
func TestMindLayerEdgeThirdOpenAtV33IsANoOp(t *testing.T) {
	s, path := edgeMLOpen(t)
	if schemaVersion != 36 {
		t.Fatalf("schemaVersion = %d, want 36 (the fleet's v34, job_stacks' v35 and the ask-thread columns' v36 follow the mind layer's v33)", schemaVersion)
	}
	id := edgeMLAdd(t, s, MMItem{
		Kind: "decision", Source: "capture", Content: "body", CreatedAt: edgeMLNow,
		Title: "a claim", Scope: "repo", Repo: "kraken", Dir: "internal/jobs",
		Quote: "the quote", SourceJSON: `{"kind":"manual"}`, ThreadSession: "sess-mind",
	})
	before, err := s.GetMMItem(id)
	if err != nil {
		t.Fatalf("GetMMItem: %v", err)
	}
	// No version downgrade here: the stamp alone is not a v32 database (the
	// v33 columns would already exist), so the re-Opens below are of a store
	// already at v33 — exactly the "second Open is a no-op" case, repeated.
	s.Close()

	for i := 2; i <= 4; i++ {
		si, err := Open(path)
		if err != nil {
			t.Fatalf("Open #%d: %v", i, err)
		}
		var v int
		if err := si.DB().QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
			si.Close()
			t.Fatalf("user_version: %v", err)
		}
		if v != schemaVersion {
			si.Close()
			t.Fatalf("Open #%d: user_version = %d, want %d", i, v, schemaVersion)
		}
		got, err := si.GetMMItem(id)
		si.Close()
		if err != nil {
			t.Fatalf("Open #%d: GetMMItem: %v", i, err)
		}
		if !reflect.DeepEqual(got, before) {
			t.Fatalf("Open #%d: item changed across re-Open:\n got %+v\nwant %+v", i, got, before)
		}
	}
}

// ---------------------------------------------------------------------------
// AddMMItem refusals
// ---------------------------------------------------------------------------

// "a `scope` outside the two values or a `source` outside the seven is refused
// by `AddMMItem` with an error, nothing stored."
func TestMindLayerEdgeAddMMItemRefusesBadScope(t *testing.T) {
	s, _ := edgeMLOpen(t)
	for _, scope := range []string{"Universal", "REPO", "global", "dir", " repo", "repo ", "universal,repo"} {
		it := edgeMLItem("bad scope " + scope)
		it.Scope = scope
		if id, err := s.AddMMItem(it); err == nil {
			t.Errorf("AddMMItem(scope %q) = id %d, nil; want an error", scope, id)
		}
	}
	if n := edgeMLCount(t, s); n != 0 {
		t.Fatalf("refused inserts stored %d rows, want 0", n)
	}
}

func TestMindLayerEdgeAddMMItemRefusesBadSource(t *testing.T) {
	s, _ := edgeMLOpen(t)
	for _, src := range []string{"Capture", "captured", "mind", "transcript", "", "capture "} {
		it := edgeMLItem("bad source " + src)
		it.Source = src
		if id, err := s.AddMMItem(it); err == nil {
			t.Errorf("AddMMItem(source %q) = id %d, nil; want an error", src, id)
		}
	}
	if n := edgeMLCount(t, s); n != 0 {
		t.Fatalf("refused inserts stored %d rows, want 0", n)
	}
}

// "`Scope` defaults to `repo` when empty on insert; `Source == "capture"` is
// valid" — the boundary of the seventh value, and the default on an otherwise
// legacy-shaped insert.
func TestMindLayerEdgeAddMMItemEmptyScopeDefaultsToRepo(t *testing.T) {
	s, _ := edgeMLOpen(t)
	id := edgeMLAdd(t, s, edgeMLItem("legacy shape"))
	got, err := s.GetMMItem(id)
	if err != nil {
		t.Fatalf("GetMMItem: %v", err)
	}
	if got.Scope != "repo" {
		t.Errorf("Scope = %q on an empty-scope insert, want repo", got.Scope)
	}
	if got.Title != "" || got.Quote != "" || got.SourceJSON != "" || got.ThreadSession != "" || got.RevisedAt != 0 {
		t.Errorf("new columns not at their defaults: %+v", got)
	}
}

// ---------------------------------------------------------------------------
// ReviseMMItem
// ---------------------------------------------------------------------------

// "refuses (error, nothing changed) an unknown id"
func TestMindLayerEdgeReviseMMItemRefusesUnknownID(t *testing.T) {
	s, _ := edgeMLOpen(t)
	keep := edgeMLAdd(t, s, edgeMLItem("untouched"))
	before, _ := s.GetMMItem(keep)
	for _, id := range []int64{0, -1, keep + 1, keep + 1000} {
		if err := s.ReviseMMItem(id, MMRevision{Title: "t", Content: "c", Scope: "repo", At: edgeMLNow + 1}); err == nil {
			t.Errorf("ReviseMMItem(%d) = nil, want an error for an unknown id", id)
		}
	}
	if n := edgeMLCount(t, s); n != 1 {
		t.Fatalf("row count = %d after refused revises, want 1", n)
	}
	after, _ := s.GetMMItem(keep)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("an unrelated row changed:\n got %+v\nwant %+v", after, before)
	}
}

// "refuses (error, nothing changed) … an invalidated item"
func TestMindLayerEdgeReviseMMItemRefusesInvalidated(t *testing.T) {
	s, _ := edgeMLOpen(t)
	id := edgeMLAdd(t, s, MMItem{Kind: "note", Source: "capture", Content: "old", Title: "old title", CreatedAt: edgeMLNow})
	if err := s.InvalidateMMItem(id, edgeMLNow+1, "contradicted", "flag #1: x"); err != nil {
		t.Fatalf("InvalidateMMItem: %v", err)
	}
	before, _ := s.GetMMItem(id)
	err := s.ReviseMMItem(id, MMRevision{Title: "new", Content: "new body", Quote: "q", Scope: "universal", At: edgeMLNow + 2})
	if err == nil {
		t.Fatal("ReviseMMItem on an invalidated item = nil, want an error")
	}
	after, _ := s.GetMMItem(id)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("a refused revise changed the row:\n got %+v\nwant %+v", after, before)
	}
}

// "`created_at`, `source_json`, `thread_session` are untouched" — even when the
// revision's At is far from CreatedAt and the revision carries values for all
// six rewritable columns.
func TestMindLayerEdgeReviseMMItemLeavesProvenanceAlone(t *testing.T) {
	s, _ := edgeMLOpen(t)
	id := edgeMLAdd(t, s, MMItem{
		Kind: "reading", Source: "capture", Content: "v1", Title: "t1", CreatedAt: edgeMLNow,
		Scope: "repo", Repo: "kraken", Dir: "a", Quote: "q1",
		SourceJSON: `{"kind":"transcript","session":"s1","uuid":"u1"}`, ThreadSession: "mind-sess",
	})
	if err := s.ReviseMMItem(id, MMRevision{Title: "t2", Content: "v2", Quote: "", Scope: "universal", Repo: "", Dir: "", At: edgeMLNow + 99}); err != nil {
		t.Fatalf("ReviseMMItem: %v", err)
	}
	got, _ := s.GetMMItem(id)
	if got.CreatedAt != edgeMLNow {
		t.Errorf("CreatedAt = %d, want %d untouched", got.CreatedAt, edgeMLNow)
	}
	if got.SourceJSON != `{"kind":"transcript","session":"s1","uuid":"u1"}` {
		t.Errorf("SourceJSON = %q, want untouched", got.SourceJSON)
	}
	if got.ThreadSession != "mind-sess" {
		t.Errorf("ThreadSession = %q, want untouched", got.ThreadSession)
	}
	if got.Source != "capture" || got.Kind != "reading" {
		t.Errorf("source/kind = %q/%q, want capture/reading untouched", got.Source, got.Kind)
	}
	// "Rewrites exactly those six columns" — including rewriting a non-empty
	// quote/repo/dir to empty.
	if got.Title != "t2" || got.Content != "v2" || got.Quote != "" || got.Scope != "universal" || got.Repo != "" || got.Dir != "" {
		t.Errorf("revised columns = %+v, want t2/v2/''/universal/''/''", got)
	}
	if got.RevisedAt != edgeMLNow+99 {
		t.Errorf("RevisedAt = %d, want %d", got.RevisedAt, edgeMLNow+99)
	}
}

// ---------------------------------------------------------------------------
// ListMMItemsFiltered
// ---------------------------------------------------------------------------

// "`Dir` matches `dir` exactly OR any ancestor directory of it (`Dir:"a/b"`
// matches items with dir `a/b`, `a`, and `”`)" — and never a descendant, a
// sibling, or a string-prefix that is not a path ancestor.
func TestMindLayerEdgeListFilteredDirAncestorsOnly(t *testing.T) {
	s, _ := edgeMLOpen(t)
	add := func(dir string) int64 {
		return edgeMLAdd(t, s, MMItem{Kind: "note", Source: "capture", Content: "d " + dir, Scope: "repo", Repo: "kraken", Dir: dir, CreatedAt: edgeMLNow})
	}
	root := add("")
	a := add("a")
	ab := add("a/b")
	add("a/b/c") // descendant: never
	add("a/bc")  // string prefix of nothing / sibling of a/b
	add("ab")    // string-prefix of "a/b" minus the slash: never
	add("b")     // unrelated
	add("a/c")   // sibling

	got, err := s.ListMMItemsFiltered(MMFilter{Dir: "a/b"})
	if err != nil {
		t.Fatalf("ListMMItemsFiltered: %v", err)
	}
	if ids := edgeMLIDs(got); !edgeMLSameIDSet(ids, root, a, ab) {
		t.Fatalf("Dir:a/b matched %v, want exactly {%d,%d,%d} (a/b, a, '')", ids, root, a, ab)
	}
}

// `Dir:"a"` (a top-level dir) matches `a` and `”` only.
func TestMindLayerEdgeListFilteredDirTopLevel(t *testing.T) {
	s, _ := edgeMLOpen(t)
	add := func(dir string) int64 {
		return edgeMLAdd(t, s, MMItem{Kind: "note", Source: "capture", Content: "d " + dir, Scope: "repo", Repo: "kraken", Dir: dir, CreatedAt: edgeMLNow})
	}
	root := add("")
	a := add("a")
	add("a/b")
	add("ab")
	got, err := s.ListMMItemsFiltered(MMFilter{Dir: "a"})
	if err != nil {
		t.Fatalf("ListMMItemsFiltered: %v", err)
	}
	if ids := edgeMLIDs(got); !edgeMLSameIDSet(ids, root, a) {
		t.Fatalf("Dir:a matched %v, want exactly {%d,%d}", ids, root, a)
	}
}

// "`Q` is a case-insensitive substring over `title`, `content` and `quote`
// (any one matching qualifies)" — each column alone qualifies, case folded in
// both directions, and a Q matching none returns an empty result, not an error.
func TestMindLayerEdgeListFilteredQOverAllThreeColumns(t *testing.T) {
	s, _ := edgeMLOpen(t)
	inTitle := edgeMLAdd(t, s, MMItem{Kind: "note", Source: "capture", Title: "The CAPACITY limit", Content: "x", CreatedAt: edgeMLNow})
	inContent := edgeMLAdd(t, s, MMItem{Kind: "note", Source: "capture", Title: "y", Content: "about capacity planning", CreatedAt: edgeMLNow + 1})
	inQuote := edgeMLAdd(t, s, MMItem{Kind: "note", Source: "capture", Title: "z", Content: "w", Quote: "CaPaCiTy is 15", CreatedAt: edgeMLNow + 2})
	edgeMLAdd(t, s, MMItem{Kind: "note", Source: "capture", Title: "unrelated", Content: "nothing", Quote: "here", Repo: "capacity", Dir: "capacity", CreatedAt: edgeMLNow + 3})

	for _, q := range []string{"capacity", "CAPACITY", "cApAcItY", "apacit"} {
		got, err := s.ListMMItemsFiltered(MMFilter{Q: q})
		if err != nil {
			t.Fatalf("Q %q: %v", q, err)
		}
		if ids := edgeMLIDs(got); !edgeMLSameIDSet(ids, inTitle, inContent, inQuote) {
			t.Errorf("Q %q matched %v, want exactly {%d,%d,%d} (repo/dir are not searched)", q, ids, inTitle, inContent, inQuote)
		}
	}

	none, err := s.ListMMItemsFiltered(MMFilter{Q: "no-such-needle-anywhere"})
	if err != nil {
		t.Fatalf("Q matching none: err = %v, want nil", err)
	}
	if len(none) != 0 {
		t.Fatalf("Q matching none returned %d items, want 0", len(none))
	}
}

// "`Live` excludes invalidated rows" combined with Q: an invalidated row whose
// title matches is excluded under Live and included without it.
func TestMindLayerEdgeListFilteredQRespectsLive(t *testing.T) {
	s, _ := edgeMLOpen(t)
	dead := edgeMLAdd(t, s, MMItem{Kind: "note", Source: "capture", Title: "needle dead", Content: "x", CreatedAt: edgeMLNow})
	live := edgeMLAdd(t, s, MMItem{Kind: "note", Source: "capture", Title: "needle live", Content: "x", CreatedAt: edgeMLNow + 1})
	if err := s.InvalidateMMItem(dead, edgeMLNow+5, "removed", ""); err != nil {
		t.Fatalf("InvalidateMMItem: %v", err)
	}
	got, _ := s.ListMMItemsFiltered(MMFilter{Live: true, Q: "NEEDLE"})
	if ids := edgeMLIDs(got); !edgeMLSameIDSet(ids, live) {
		t.Errorf("Live+Q = %v, want only %d", ids, live)
	}
	got, _ = s.ListMMItemsFiltered(MMFilter{Q: "needle"})
	if ids := edgeMLIDs(got); !edgeMLSameIDSet(ids, live, dead) {
		t.Errorf("Q without Live = %v, want both %d and %d", ids, live, dead)
	}
}

// "`Repo` is an exact match on `repo`" — no prefix, no case folding.
func TestMindLayerEdgeListFilteredRepoIsExact(t *testing.T) {
	s, _ := edgeMLOpen(t)
	k := edgeMLAdd(t, s, MMItem{Kind: "note", Source: "capture", Content: "a", Repo: "kraken", CreatedAt: edgeMLNow})
	edgeMLAdd(t, s, MMItem{Kind: "note", Source: "capture", Content: "b", Repo: "kraken-ios", CreatedAt: edgeMLNow})
	edgeMLAdd(t, s, MMItem{Kind: "note", Source: "capture", Content: "c", Repo: "Kraken", CreatedAt: edgeMLNow})
	edgeMLAdd(t, s, MMItem{Kind: "note", Source: "capture", Content: "d", Repo: "", Scope: "universal", CreatedAt: edgeMLNow})
	got, _ := s.ListMMItemsFiltered(MMFilter{Repo: "kraken"})
	if ids := edgeMLIDs(got); !edgeMLSameIDSet(ids, k) {
		t.Fatalf("Repo:kraken matched %v, want only %d", ids, k)
	}
}

// ---------------------------------------------------------------------------
// flags
// ---------------------------------------------------------------------------

// "`Note` empty → error; `State` outside the four (amendment 2 adds `resolved`) → error."
func TestMindLayerEdgeAddMMFlagRefusals(t *testing.T) {
	s, _ := edgeMLOpen(t)
	if id, err := s.AddMMFlag(MMFlag{Note: "", CreatedAt: edgeMLNow}); err == nil {
		t.Errorf("AddMMFlag(empty note) = %d, nil; want an error", id)
	}
	for _, st := range []string{"Pending", "Resolved", "open", "closed", "raised ", "drop"} {
		if id, err := s.AddMMFlag(MMFlag{Note: "n", State: st, CreatedAt: edgeMLNow}); err == nil {
			t.Errorf("AddMMFlag(state %q) = %d, nil; want an error", st, id)
		}
	}
	all, err := s.ListMMFlags("")
	if err != nil {
		t.Fatalf("ListMMFlags: %v", err)
	}
	if len(all) != 0 {
		t.Fatalf("refused flags stored %d rows, want 0", len(all))
	}
}

// "Inserts with `State` as given (`pending` when empty)"; "unknown id → error".
func TestMindLayerEdgeFlagDefaultsAndUnknownID(t *testing.T) {
	s, _ := edgeMLOpen(t)
	id := edgeMLFlag(t, s, MMFlag{Note: "n"})
	if f := edgeMLGetFlag(t, s, id); f.State != "pending" {
		t.Errorf("State = %q on an empty-state insert, want pending", f.State)
	}
	for _, bad := range []int64{0, -1, id + 1} {
		if _, err := s.GetMMFlag(bad); err == nil {
			t.Errorf("GetMMFlag(%d) = nil error, want one", bad)
		}
	}
	// "a state filters exactly" — a state nothing carries lists empty.
	got, err := s.ListMMFlags("dismissed")
	if err != nil {
		t.Fatalf("ListMMFlags(dismissed): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListMMFlags(dismissed) = %d rows, want 0", len(got))
	}
}

// "Each moves a `pending` flag to `raised`/`dismissed` …; a flag not in
// `pending` → error, nothing changed." Every non-pending start × both verbs.
func TestMindLayerEdgeRaiseDismissRefuseNonPending(t *testing.T) {
	s, _ := edgeMLOpen(t)
	type verb struct {
		name string
		fn   func(id, at int64, by, note string) error
	}
	verbs := []verb{{"raise", s.RaiseMMFlag}, {"dismiss", s.DismissMMFlag}}
	for _, start := range []string{"raised", "dismissed"} {
		for _, v := range verbs {
			id := edgeMLFlag(t, s, MMFlag{Note: "n", State: start, RaisedBy: "orig", RaiseNote: "orig note", RaisedAt: 7})
			before := edgeMLGetFlag(t, s, id)
			if err := v.fn(id, edgeMLNow+1, "someone", "again"); err == nil {
				t.Errorf("%s on a %s flag = nil, want an error", v.name, start)
			}
			if after := edgeMLGetFlag(t, s, id); !reflect.DeepEqual(after, before) {
				t.Errorf("%s on a %s flag changed it:\n got %+v\nwant %+v", v.name, start, after, before)
			}
		}
	}
	// unknown id → error for both verbs
	for _, v := range verbs {
		if err := v.fn(99999, edgeMLNow, "x", "y"); err == nil {
			t.Errorf("%s(unknown id) = nil, want an error", v.name)
		}
	}
}

// A raised-then-resolved flag cannot be raised or dismissed (it is not pending).
func TestMindLayerEdgeResolvedFlagRefusesRaiseAndDismiss(t *testing.T) {
	s, _ := edgeMLOpen(t)
	id := edgeMLFlag(t, s, MMFlag{Note: "n"})
	if err := s.RaiseMMFlag(id, edgeMLNow+1, "lead", "real"); err != nil {
		t.Fatalf("RaiseMMFlag: %v", err)
	}
	if err := s.ResolveMMFlag(id, edgeMLNow+2, "keep"); err != nil {
		t.Fatalf("ResolveMMFlag: %v", err)
	}
	before := edgeMLGetFlag(t, s, id)
	if err := s.RaiseMMFlag(id, edgeMLNow+3, "lead", "x"); err == nil {
		t.Error("RaiseMMFlag on a resolved flag = nil, want an error")
	}
	if err := s.DismissMMFlag(id, edgeMLNow+3, "lead", "x"); err == nil {
		t.Error("DismissMMFlag on a resolved flag = nil, want an error")
	}
	if after := edgeMLGetFlag(t, s, id); !reflect.DeepEqual(after, before) {
		t.Fatalf("refused transitions changed the flag:\n got %+v\nwant %+v", after, before)
	}
}

// "Only a `raised` flag with `resolution == ”` accepts it; `resolution` must
// be `drop` or `keep`; … anything else → error, nothing changed." — a pending
// flag, a dismissed flag, a second resolution, and every bad value.
func TestMindLayerEdgeResolveMMFlagRefusals(t *testing.T) {
	s, _ := edgeMLOpen(t)

	pending := edgeMLFlag(t, s, MMFlag{Note: "p"})
	dismissed := edgeMLFlag(t, s, MMFlag{Note: "d"})
	if err := s.DismissMMFlag(dismissed, edgeMLNow, "lead", "no"); err != nil {
		t.Fatalf("DismissMMFlag: %v", err)
	}
	for _, id := range []int64{pending, dismissed} {
		before := edgeMLGetFlag(t, s, id)
		for _, r := range []string{"drop", "keep"} {
			if err := s.ResolveMMFlag(id, edgeMLNow+1, r); err == nil {
				t.Errorf("ResolveMMFlag(%s flag, %q) = nil, want an error", before.State, r)
			}
		}
		if after := edgeMLGetFlag(t, s, id); !reflect.DeepEqual(after, before) {
			t.Errorf("refused resolve changed a %s flag:\n got %+v\nwant %+v", before.State, after, before)
		}
	}

	raised := edgeMLFlag(t, s, MMFlag{Note: "r"})
	if err := s.RaiseMMFlag(raised, edgeMLNow, "lead", "yes"); err != nil {
		t.Fatalf("RaiseMMFlag: %v", err)
	}
	before := edgeMLGetFlag(t, s, raised)
	for _, r := range []string{"", "Drop", "KEEP", "delete", "drop ", "maybe"} {
		if err := s.ResolveMMFlag(raised, edgeMLNow+1, r); err == nil {
			t.Errorf("ResolveMMFlag(raised, %q) = nil, want an error", r)
		}
	}
	if after := edgeMLGetFlag(t, s, raised); !reflect.DeepEqual(after, before) {
		t.Fatalf("bad resolution values changed the flag:\n got %+v\nwant %+v", after, before)
	}

	if err := s.ResolveMMFlag(raised, edgeMLNow+2, "drop"); err != nil {
		t.Fatalf("first ResolveMMFlag(drop): %v", err)
	}
	first := edgeMLGetFlag(t, s, raised)
	if first.Resolution != "drop" || first.ResolvedAt != edgeMLNow+2 {
		t.Fatalf("after drop: resolution=%q resolved_at=%d", first.Resolution, first.ResolvedAt)
	}
	for _, r := range []string{"keep", "drop"} {
		if err := s.ResolveMMFlag(raised, edgeMLNow+3, r); err == nil {
			t.Errorf("second ResolveMMFlag(%q) = nil, want an error", r)
		}
	}
	if after := edgeMLGetFlag(t, s, raised); !reflect.DeepEqual(after, first) {
		t.Fatalf("a second resolution changed the flag:\n got %+v\nwant %+v", after, first)
	}
	if err := s.ResolveMMFlag(424242, edgeMLNow, "keep"); err == nil {
		t.Error("ResolveMMFlag(unknown id) = nil, want an error")
	}
}

// ---------------------------------------------------------------------------
// threads
// ---------------------------------------------------------------------------

// "Returns the thread row whose `session_id` matches, `nil, nil` when none
// (any state)."
func TestMindLayerEdgeGetThreadBySessionUnknown(t *testing.T) {
	s, _ := edgeMLOpen(t)
	jobID, err := s.CreateJob(&Job{Slug: "edge-ml-threads"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if err := s.AddThread(Thread{JobID: jobID, TID: "t1", SessionID: "sess-known", State: "live", CreatedAt: 1}); err != nil {
		t.Fatalf("AddThread: %v", err)
	}
	for _, sess := range []string{"sess-unknown", "SESS-KNOWN", "sess-know", "sess-known ", ""} {
		th, err := s.GetThreadBySession(sess)
		if err != nil {
			t.Errorf("GetThreadBySession(%q) err = %v, want nil", sess, err)
		}
		if th != nil {
			t.Errorf("GetThreadBySession(%q) = %+v, want nil", sess, *th)
		}
	}
}

// "(any state)" — a closed row is still returned, with its role.
func TestMindLayerEdgeGetThreadBySessionReturnsClosedRows(t *testing.T) {
	s, _ := edgeMLOpen(t)
	jobID, err := s.CreateJob(&Job{Slug: "edge-ml-closed"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if err := s.AddThread(Thread{JobID: jobID, TID: "t1", SessionID: "sess-closed", State: "closed", Role: "mind", CreatedAt: 1, ClosedAt: 2}); err != nil {
		t.Fatalf("AddThread: %v", err)
	}
	th, err := s.GetThreadBySession("sess-closed")
	if err != nil || th == nil {
		t.Fatalf("GetThreadBySession(closed) = %v, %v; want the row", th, err)
	}
	if th.State != "closed" || th.Role != "mind" || th.TID != "t1" {
		t.Errorf("row = %+v, want state closed, role mind, tid t1", *th)
	}
}

// "`AddThreadNext`/`AddThread` persist it (`ask` when empty)"; the CHECK admits
// only `ask`/`mind`.
func TestMindLayerEdgeThreadRoleDefaultAndRefusal(t *testing.T) {
	s, _ := edgeMLOpen(t)
	jobID, err := s.CreateJob(&Job{Slug: "edge-ml-role"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	row, err := s.AddThreadNext(Thread{JobID: jobID, Title: "x", SessionID: "s-next", State: "live"})
	if err != nil {
		t.Fatalf("AddThreadNext: %v", err)
	}
	got, err := s.GetThread(jobID, row.TID)
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if got.Role != "ask" {
		t.Errorf("AddThreadNext with empty Role stored %q, want ask", got.Role)
	}
	if err := s.AddThread(Thread{JobID: jobID, TID: "t9", SessionID: "s-plain", State: "live", CreatedAt: 1}); err != nil {
		t.Fatalf("AddThread: %v", err)
	}
	if got, _ := s.GetThread(jobID, "t9"); got == nil || got.Role != "ask" {
		t.Errorf("AddThread with empty Role stored %+v, want role ask", got)
	}
	// the CHECK: a role outside the two never lands.
	for _, role := range []string{"Mind", "zeus", "kronos", "ask "} {
		err := s.AddThread(Thread{JobID: jobID, TID: "t-" + strings.TrimSpace(strings.ToLower(role)) + "x", SessionID: "s-" + role, State: "live", Role: role, CreatedAt: 1})
		if err == nil {
			t.Errorf("AddThread(role %q) = nil, want the CHECK to refuse it", role)
		}
	}
}
