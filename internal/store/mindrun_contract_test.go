package store

import (
	"bytes"
	"compress/gzip"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

// mindrun_contract_test.go — athena-logic's BLIND contract tests for
// artifacts/50-contract.md (job 674, mind runs), slice A: schema v39
// (`mind_runs`, `mind_run_blobs`, `mm_marks`, the three new `mm_items`
// columns and the proposed→live flip), the run queue verbs, the blob verbs,
// the mark verbs and `SupersedeMMItem`.
//
// Written from the frozen contract alone; the slice's implementation files
// (mindruns.go, mmmarks.go, the mm.go/schema.go diffs) were never read. The
// pre-existing MMItem shape and ErrMMItemInvalidated were read through
// `git show HEAD:internal/store/mm.go`.
//
// Dimension: LOGIC — invariants and happy paths. Refusals (unknown trigger,
// Until <= Since, ErrNotFound arms, ErrMMMarkConsumed, FinishMindRun on a
// non-running row) are athena-edge's except where a happy path needs them to
// be distinguishable. Every package-level identifier is prefixed `contract`.

func contractOpenStore(t *testing.T) (*Store, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	dbPath := filepath.Join(t.TempDir(), "k.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s, dbPath
}

func contractJob(t *testing.T, s *Store, slug string) int64 {
	t.Helper()
	id, err := s.CreateJob(&Job{Slug: slug, Repo: "kraken", Body: "spec"})
	if err != nil {
		t.Fatalf("CreateJob %s: %v", slug, err)
	}
	return id
}

func contractEnqueue(t *testing.T, s *Store, r MindRun) int64 {
	t.Helper()
	if r.Trigger == "" {
		r.Trigger = "done"
	}
	if r.Until == 0 {
		r.Until = r.Since + 1000
	}
	id, err := s.EnqueueMindRun(r)
	if err != nil {
		t.Fatalf("EnqueueMindRun(%+v): %v", r, err)
	}
	return id
}

func contractGetRun(t *testing.T, s *Store, id int64) MindRun {
	t.Helper()
	r, err := s.GetMindRun(id)
	if err != nil {
		t.Fatalf("GetMindRun(%d): %v", id, err)
	}
	return r
}

func contractAddItem(t *testing.T, s *Store, it MMItem) int64 {
	t.Helper()
	if it.Kind == "" {
		it.Kind = "note"
	}
	if it.Source == "" {
		it.Source = "capture"
	}
	if it.Content == "" {
		it.Content = "body"
	}
	id, err := s.AddMMItem(it)
	if err != nil {
		t.Fatalf("AddMMItem(%+v): %v", it, err)
	}
	return id
}

func contractExec(t *testing.T, s *Store, stmts ...string) {
	t.Helper()
	for _, q := range stmts {
		if _, err := s.db.Exec(q); err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
	}
}

func contractUserVersion(t *testing.T, s *Store) int {
	t.Helper()
	var v int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func contractTableExists(t *testing.T, s *Store, name string) bool {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

func contractColumns(t *testing.T, s *Store, table string) map[string]bool {
	t.Helper()
	rows, err := s.db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		out[n] = true
	}
	return out
}

func contractGzip(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// ---------------------------------------------------------------------------
// schema v39
// ---------------------------------------------------------------------------

// `schemaVersion = 39`; v39 "Creates `mind_runs`, `mind_run_blobs`,
// `mm_marks`; ALTER TABLE mm_items ADD COLUMN run_id … supersedes …
// superseded_by".
func TestContractV39FreshStoreShape(t *testing.T) {
	s, _ := contractOpenStore(t)
	if v := contractUserVersion(t, s); v != 39 {
		t.Errorf("fresh user_version = %d, want 39", v)
	}
	for _, tbl := range []string{"mind_runs", "mind_run_blobs", "mm_marks"} {
		if !contractTableExists(t, s, tbl) {
			t.Errorf("table %s missing on a fresh v39 store", tbl)
		}
	}
	cols := contractColumns(t, s, "mm_items")
	for _, c := range []string{"run_id", "supersedes", "superseded_by", "state"} {
		if !cols[c] {
			t.Errorf("mm_items column %s missing", c)
		}
	}
}

// v39 over a v38 store: `UPDATE mm_items SET state='live' WHERE
// state='proposed' AND invalidated_at IS NULL` (Q8). The v38 shape is a fresh
// store with the three v39 tables and three columns dropped and user_version
// set back to 38.
func TestContractV39FlipsProposedLiveOverAV38Store(t *testing.T) {
	s, dbPath := contractOpenStore(t)
	prop := contractAddItem(t, s, MMItem{Source: "nightly", State: "proposed", Content: "p", Quote: "proposed quote", CreatedAt: 10, Title: "a proposed claim"})
	deadProp := contractAddItem(t, s, MMItem{Source: "nightly", State: "proposed", Content: "dp", Quote: "dead proposed quote", CreatedAt: 11})
	if err := s.InvalidateMMItem(deadProp, 20, "removed", "test"); err != nil {
		t.Fatal(err)
	}
	live := contractAddItem(t, s, MMItem{Source: "capture", Content: "l", Quote: "live quote", CreatedAt: 12})

	contractExec(t, s,
		`DROP TABLE mind_run_blobs`,
		`DROP TABLE mind_runs`,
		`DROP TABLE mm_marks`,
		`ALTER TABLE mm_items DROP COLUMN run_id`,
		`ALTER TABLE mm_items DROP COLUMN supersedes`,
		`ALTER TABLE mm_items DROP COLUMN superseded_by`,
		`PRAGMA user_version = 38`,
	)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("re-Open a v38-shaped database: %v", err)
	}
	defer s2.Close()

	if v := contractUserVersion(t, s2); v != 39 {
		t.Errorf("migrated user_version = %d, want 39", v)
	}
	for _, tbl := range []string{"mind_runs", "mind_run_blobs", "mm_marks"} {
		if !contractTableExists(t, s2, tbl) {
			t.Errorf("table %s missing after v39 over v38", tbl)
		}
	}
	get := func(id int64) MMItem {
		it, err := s2.GetMMItem(id)
		if err != nil {
			t.Fatalf("GetMMItem(%d): %v", id, err)
		}
		return it
	}
	if p := get(prop); p.State != "live" || p.Title != "a proposed claim" || p.Quote != "proposed quote" {
		t.Errorf("proposed row after v39 = state %q title %q quote %q, want live with title/quote kept", p.State, p.Title, p.Quote)
	}
	if d := get(deadProp); d.State != "proposed" {
		t.Errorf("invalidated proposed row state = %q, want proposed (the UPDATE skips invalidated rows)", d.State)
	}
	if l := get(live); l.State != "live" {
		t.Errorf("live row state = %q, want live", l.State)
	}
	if p := get(prop); p.RunID != 0 || p.Supersedes != 0 || p.SupersededBy != 0 {
		t.Errorf("migrated row new columns = %d/%d/%d, want 0/0/0 (DEFAULT 0)", p.RunID, p.Supersedes, p.SupersededBy)
	}
	// the source CHECK is untouched: nightly still accepted
	if _, err := s2.AddMMItem(MMItem{Kind: "note", Source: "nightly", Content: "after", CreatedAt: 30}); err != nil {
		t.Errorf("nightly AddMMItem after v39: %v", err)
	}
}

// ---------------------------------------------------------------------------
// MMItem.RunID / Supersedes / SupersededBy
// ---------------------------------------------------------------------------

// "Scanned and written by `AddMMItem` (`RunID`, `Supersedes`) and
// `scanMMItem`; `SupersededBy` is written only by `SupersedeMMItem`".
func TestContractMMItemRunFieldsRoundTrip(t *testing.T) {
	s, _ := contractOpenStore(t)
	old := contractAddItem(t, s, MMItem{Source: "nightly", Content: "old", Quote: "old quote", CreatedAt: 1})
	id := contractAddItem(t, s, MMItem{Source: "nightly", Content: "new", Quote: "new quote", CreatedAt: 2,
		RunID: 7, Supersedes: old, SupersededBy: 999})
	it, err := s.GetMMItem(id)
	if err != nil {
		t.Fatal(err)
	}
	if it.RunID != 7 || it.Supersedes != old {
		t.Errorf("RunID/Supersedes = %d/%d, want 7/%d", it.RunID, it.Supersedes, old)
	}
	if it.SupersededBy != 0 {
		t.Errorf("SupersededBy = %d after AddMMItem, want 0 (written only by SupersedeMMItem)", it.SupersededBy)
	}
	found := false
	items, err := s.ListMMItems(false)
	if err != nil {
		t.Fatal(err)
	}
	for _, li := range items {
		if li.ID == id {
			found = true
			if li.RunID != 7 || li.Supersedes != old {
				t.Errorf("ListMMItems row RunID/Supersedes = %d/%d, want 7/%d", li.RunID, li.Supersedes, old)
			}
		}
	}
	if !found {
		t.Errorf("item %d missing from ListMMItems(false)", id)
	}
}

// ---------------------------------------------------------------------------
// SupersedeMMItem / ListMMItems(false)
// ---------------------------------------------------------------------------

// "Sets invalidated_at = at, invalid_reason = 'superseded', invalid_detail =
// detail, superseded_by = newID on oldID … never edits title, content or quote
// of either row"; `ListMMItems(false)` "Excludes superseded rows".
func TestContractSupersedeMMItemKeepsTheOldRowWhole(t *testing.T) {
	s, _ := contractOpenStore(t)
	old := contractAddItem(t, s, MMItem{Kind: "decision", Source: "capture", Title: "old claim", Content: "old body",
		Quote: "old quote", CreatedAt: 1})
	nw := contractAddItem(t, s, MMItem{Kind: "decision", Source: "nightly", Title: "new claim", Content: "new body",
		Quote: "new quote", CreatedAt: 2, RunID: 3, Supersedes: old})

	if err := s.SupersedeMMItem(old, nw, 5000, "run 3: new quote"); err != nil {
		t.Fatalf("SupersedeMMItem: %v", err)
	}
	o, err := s.GetMMItem(old)
	if err != nil {
		t.Fatal(err)
	}
	if o.InvalidatedAt != 5000 || o.InvalidReason != "superseded" || o.InvalidDetail != "run 3: new quote" || o.SupersededBy != nw {
		t.Errorf("old row = invalidated_at %d reason %q detail %q superseded_by %d, want 5000/superseded/%q/%d",
			o.InvalidatedAt, o.InvalidReason, o.InvalidDetail, o.SupersededBy, "run 3: new quote", nw)
	}
	if o.Title != "old claim" || o.Content != "old body" || o.Quote != "old quote" {
		t.Errorf("old row text = %q/%q/%q, want title/content/quote unchanged", o.Title, o.Content, o.Quote)
	}
	n, err := s.GetMMItem(nw)
	if err != nil {
		t.Fatal(err)
	}
	if n.Title != "new claim" || n.Content != "new body" || n.Quote != "new quote" || n.InvalidatedAt != 0 {
		t.Errorf("new row = %q/%q/%q invalidated %d, want unchanged and live", n.Title, n.Content, n.Quote, n.InvalidatedAt)
	}

	live, err := s.ListMMItems(false)
	if err != nil {
		t.Fatal(err)
	}
	ids := []int64{}
	for _, it := range live {
		ids = append(ids, it.ID)
	}
	if !reflect.DeepEqual(ids, []int64{nw}) && !(len(ids) == 1 && ids[0] == nw) {
		t.Errorf("ListMMItems(false) ids = %v, want only %d (superseded excluded)", ids, nw)
	}
	all, err := s.ListMMItems(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Errorf("ListMMItems(true) = %d rows, want 2 (the superseded row still readable)", len(all))
	}
}

// "`ErrMMItemInvalidated` when already invalidated".
func TestContractSupersedeMMItemTwiceIsInvalidated(t *testing.T) {
	s, _ := contractOpenStore(t)
	old := contractAddItem(t, s, MMItem{Content: "o", Quote: "o", CreatedAt: 1})
	a := contractAddItem(t, s, MMItem{Content: "a", Quote: "a", CreatedAt: 2})
	b := contractAddItem(t, s, MMItem{Content: "b", Quote: "b", CreatedAt: 3})
	if err := s.SupersedeMMItem(old, a, 10, "first"); err != nil {
		t.Fatal(err)
	}
	if err := s.SupersedeMMItem(old, b, 20, "second"); !errors.Is(err, ErrMMItemInvalidated) {
		t.Errorf("second supersede err = %v, want ErrMMItemInvalidated", err)
	}
	o, _ := s.GetMMItem(old)
	if o.SupersededBy != a || o.InvalidDetail != "first" {
		t.Errorf("old row after refused second supersede = by %d detail %q, want %d/first", o.SupersededBy, o.InvalidDetail, a)
	}
}

// ---------------------------------------------------------------------------
// run queue
// ---------------------------------------------------------------------------

// EnqueueMindRun "Inserts state `queued` with `CreatedAt` as given";
// GetMindRun reads it back.
func TestContractEnqueueMindRunInsertsQueued(t *testing.T) {
	s, _ := contractOpenStore(t)
	jid := contractJob(t, s, "demo")
	id := contractEnqueue(t, s, MindRun{JobID: jid, Slug: "demo", Trigger: "done", Since: 100, Until: 900, CreatedAt: 1234})
	if id <= 0 {
		t.Fatalf("id = %d", id)
	}
	r := contractGetRun(t, s, id)
	if r.ID != id || r.JobID != jid || r.Slug != "demo" || r.Trigger != "done" || r.Since != 100 || r.Until != 900 {
		t.Errorf("row = %+v, want the enqueued identity", r)
	}
	if r.State != "queued" || r.CreatedAt != 1234 || r.StartedAt != 0 || r.FinishedAt != 0 {
		t.Errorf("state/created/started/finished = %q/%d/%d/%d, want queued/1234/0/0", r.State, r.CreatedAt, r.StartedAt, r.FinishedAt)
	}
	for _, trig := range []string{"daily", "replay"} {
		contractEnqueue(t, s, MindRun{Trigger: trig, Since: 1, Until: 2, CreatedAt: 1})
	}
}

// ClaimNextMindRun "Compare-and-set the OLDEST `queued` row (by `created_at`,
// then `id`) to `running` with `StartedAt = at` and returns it; `nil, nil`
// when none is queued".
func TestContractClaimNextMindRunOldestFirst(t *testing.T) {
	s, _ := contractOpenStore(t)
	late := contractEnqueue(t, s, MindRun{Trigger: "daily", Since: 1, Until: 2, CreatedAt: 300})
	tieA := contractEnqueue(t, s, MindRun{Trigger: "daily", Since: 1, Until: 2, CreatedAt: 100})
	tieB := contractEnqueue(t, s, MindRun{Trigger: "daily", Since: 1, Until: 2, CreatedAt: 100})

	order := []int64{}
	for i := 0; i < 3; i++ {
		r, err := s.ClaimNextMindRun(int64(5000 + i))
		if err != nil {
			t.Fatal(err)
		}
		if r == nil {
			t.Fatalf("claim %d returned nil with rows queued", i)
		}
		if r.State != "running" || r.StartedAt != int64(5000+i) {
			t.Errorf("claimed row state/started = %q/%d, want running/%d", r.State, r.StartedAt, 5000+i)
		}
		if got := contractGetRun(t, s, r.ID); got.State != "running" || got.StartedAt != int64(5000+i) {
			t.Errorf("stored claimed row = %q/%d", got.State, got.StartedAt)
		}
		order = append(order, r.ID)
	}
	if want := []int64{tieA, tieB, late}; !reflect.DeepEqual(order, want) {
		t.Errorf("claim order = %v, want %v (created_at then id)", order, want)
	}
	r, err := s.ClaimNextMindRun(9000)
	if err != nil || r != nil {
		t.Errorf("claim with none queued = %v, %v; want nil, nil", r, err)
	}
}

// "two callers racing claim two different rows or one claims and one gets
// nil, never both the same row".
func TestContractClaimNextMindRunNeverDoubleClaims(t *testing.T) {
	s, _ := contractOpenStore(t)
	for i := 0; i < 4; i++ {
		contractEnqueue(t, s, MindRun{Trigger: "daily", Since: 1, Until: 2, CreatedAt: int64(i)})
	}
	var mu sync.Mutex
	seen := map[int64]int{}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			r, err := s.ClaimNextMindRun(int64(100 + g))
			if err != nil || r == nil {
				return
			}
			mu.Lock()
			seen[r.ID]++
			mu.Unlock()
		}(g)
	}
	wg.Wait()
	if len(seen) != 4 {
		t.Errorf("claimed %d distinct rows, want all 4: %v", len(seen), seen)
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("row %d claimed %d times", id, n)
		}
	}
}

// FinishMindRun "Sets state `done`, `FinishedAt = At`, every count and usage
// field, the report".
func TestContractFinishMindRunStampsEverything(t *testing.T) {
	s, _ := contractOpenStore(t)
	id := contractEnqueue(t, s, MindRun{Trigger: "daily", Since: 1, Until: 2, CreatedAt: 1})
	if _, err := s.ClaimNextMindRun(10); err != nil {
		t.Fatal(err)
	}
	f := MindRunFinish{At: 77, Counts: MindRunCounts{Added: 1, Superseded: 2, Retired: 3, Flagged: 4, Excluded: 5, Folded: 6, Suppressed: 7, Refused: 8},
		Report: "# report", PromptSHA: "abc123", Model: "claude-opus-5", InTokens: 1111, OutTokens: 222, BundleBytes: 3333}
	if err := s.FinishMindRun(id, f); err != nil {
		t.Fatalf("FinishMindRun: %v", err)
	}
	r := contractGetRun(t, s, id)
	if r.State != "done" || r.FinishedAt != 77 || r.Report != "# report" || r.PromptSHA != "abc123" || r.Model != "claude-opus-5" {
		t.Errorf("finished row = %+v", r)
	}
	if r.InTokens != 1111 || r.OutTokens != 222 || r.BundleBytes != 3333 {
		t.Errorf("usage = %d/%d/%d, want 1111/222/3333", r.InTokens, r.OutTokens, r.BundleBytes)
	}
	got := MindRunCounts{r.Added, r.Superseded, r.Retired, r.Flagged, r.Excluded, r.Folded, r.Suppressed, r.Refused}
	if got != f.Counts {
		t.Errorf("counts = %+v, want %+v", got, f.Counts)
	}
	if r.StartedAt != 10 {
		t.Errorf("StartedAt = %d, want the claim's 10 kept", r.StartedAt)
	}
}

// FailMindRun "Sets state `failed`, `FinishedAt`, `Note` (one-lined …);
// accepted from `queued` or `running`".
func TestContractFailMindRunFromQueuedAndRunning(t *testing.T) {
	s, _ := contractOpenStore(t)
	q := contractEnqueue(t, s, MindRun{Trigger: "daily", Since: 1, Until: 2, CreatedAt: 1})
	run := contractEnqueue(t, s, MindRun{Trigger: "daily", Since: 1, Until: 2, CreatedAt: 2})
	if c, err := s.ClaimNextMindRun(5); err != nil || c == nil || c.ID != q {
		t.Fatalf("claim = %v, %v; want %d", c, err, q)
	}
	// q is running now, run is queued
	if err := s.FailMindRun(q, 50, "timed out after 10m"); err != nil {
		t.Fatalf("FailMindRun(running): %v", err)
	}
	if err := s.FailMindRun(run, 60, "line one\nline two"); err != nil {
		t.Fatalf("FailMindRun(queued): %v", err)
	}
	a := contractGetRun(t, s, q)
	if a.State != "failed" || a.FinishedAt != 50 || a.Note != "timed out after 10m" {
		t.Errorf("failed running row = %q/%d/%q", a.State, a.FinishedAt, a.Note)
	}
	b := contractGetRun(t, s, run)
	if b.State != "failed" || b.FinishedAt != 60 {
		t.Errorf("failed queued row = %q/%d", b.State, b.FinishedAt)
	}
	if bytes.ContainsAny([]byte(b.Note), "\n\r") {
		t.Errorf("note %q is not one-lined", b.Note)
	}
}

// RequeueRunningMindRuns "Every `running` row becomes `queued` with
// `StartedAt` 0 and the note appended; returns the count".
func TestContractRequeueRunningMindRuns(t *testing.T) {
	s, _ := contractOpenStore(t)
	a := contractEnqueue(t, s, MindRun{Trigger: "daily", Since: 1, Until: 2, CreatedAt: 1})
	b := contractEnqueue(t, s, MindRun{Trigger: "daily", Since: 1, Until: 2, CreatedAt: 2})
	c := contractEnqueue(t, s, MindRun{Trigger: "daily", Since: 1, Until: 2, CreatedAt: 3})
	for i := 0; i < 2; i++ {
		if _, err := s.ClaimNextMindRun(100); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.RequeueRunningMindRuns("serve restarted")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("requeued %d, want 2", n)
	}
	for _, id := range []int64{a, b} {
		r := contractGetRun(t, s, id)
		if r.State != "queued" || r.StartedAt != 0 {
			t.Errorf("row %d = %q started %d, want queued/0", id, r.State, r.StartedAt)
		}
		if !bytes.Contains([]byte(r.Note), []byte("serve restarted")) {
			t.Errorf("row %d note = %q, want it to carry %q", id, r.Note, "serve restarted")
		}
	}
	if r := contractGetRun(t, s, c); r.State != "queued" || r.Note != "" {
		t.Errorf("never-claimed row = %q note %q, want queued with no note", r.State, r.Note)
	}
}

// ListMindRuns "Newest first by `created_at` then id; `jobID` 0 lists every
// job; `n <= 0` means 50".
func TestContractListMindRunsOrderAndScope(t *testing.T) {
	s, _ := contractOpenStore(t)
	j1 := contractJob(t, s, "one")
	j2 := contractJob(t, s, "two")
	r1 := contractEnqueue(t, s, MindRun{JobID: j1, Slug: "one", CreatedAt: 10, Since: 1, Until: 2})
	r2 := contractEnqueue(t, s, MindRun{JobID: j2, Slug: "two", CreatedAt: 30, Since: 1, Until: 2})
	r3 := contractEnqueue(t, s, MindRun{JobID: j1, Slug: "one", CreatedAt: 20, Since: 2, Until: 3})
	r4 := contractEnqueue(t, s, MindRun{Trigger: "daily", CreatedAt: 20, Since: 1, Until: 2})

	ids := func(rs []MindRun) []int64 {
		out := []int64{}
		for _, r := range rs {
			out = append(out, r.ID)
		}
		return out
	}
	all, err := s.ListMindRuns(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if want := []int64{r2, r4, r3, r1}; !reflect.DeepEqual(ids(all), want) {
		t.Errorf("ListMindRuns(0,0) = %v, want %v (created_at desc then id desc)", ids(all), want)
	}
	one, err := s.ListMindRuns(j1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if want := []int64{r3, r1}; !reflect.DeepEqual(ids(one), want) {
		t.Errorf("ListMindRuns(j1) = %v, want %v", ids(one), want)
	}
	top, err := s.ListMindRuns(0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if want := []int64{r2, r4}; !reflect.DeepEqual(ids(top), want) {
		t.Errorf("ListMindRuns(0,2) = %v, want %v", ids(top), want)
	}
	for i := 0; i < 55; i++ {
		contractEnqueue(t, s, MindRun{Trigger: "daily", CreatedAt: int64(1000 + i), Since: 1, Until: 2})
	}
	capped, err := s.ListMindRuns(0, -1)
	if err != nil {
		t.Fatal(err)
	}
	if len(capped) != 50 {
		t.Errorf("ListMindRuns(0,-1) = %d rows, want 50", len(capped))
	}
}

// LastMindRunUntil "The greatest `until` among rows of that job and trigger in
// state `done` or `running` or `queued` (a failed run does not advance the
// floor); 0 when none".
func TestContractLastMindRunUntil(t *testing.T) {
	s, _ := contractOpenStore(t)
	j := contractJob(t, s, "floor")
	other := contractJob(t, s, "other")
	if got, err := s.LastMindRunUntil(j, "done"); err != nil || got != 0 {
		t.Fatalf("empty = %d, %v; want 0", got, err)
	}
	doneID := contractEnqueue(t, s, MindRun{JobID: j, Trigger: "done", Since: 0, Until: 100, CreatedAt: 1})
	if _, err := s.ClaimNextMindRun(2); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishMindRun(doneID, MindRunFinish{At: 3}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.LastMindRunUntil(j, "done"); got != 100 {
		t.Errorf("after a done run = %d, want 100", got)
	}
	contractEnqueue(t, s, MindRun{JobID: j, Trigger: "done", Since: 100, Until: 200, CreatedAt: 4}) // queued
	if got, _ := s.LastMindRunUntil(j, "done"); got != 200 {
		t.Errorf("with a queued run = %d, want 200", got)
	}
	failID := contractEnqueue(t, s, MindRun{JobID: j, Trigger: "done", Since: 200, Until: 900, CreatedAt: 5})
	if err := s.FailMindRun(failID, 6, "boom"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.LastMindRunUntil(j, "done"); got != 200 {
		t.Errorf("a failed run moved the floor: %d, want 200", got)
	}
	contractEnqueue(t, s, MindRun{JobID: j, Trigger: "replay", Since: 0, Until: 5000, CreatedAt: 7})
	contractEnqueue(t, s, MindRun{JobID: other, Trigger: "done", Since: 0, Until: 7000, CreatedAt: 8})
	if got, _ := s.LastMindRunUntil(j, "done"); got != 200 {
		t.Errorf("another trigger or job moved the floor: %d, want 200", got)
	}
	if got, _ := s.LastMindRunUntil(j, "replay"); got != 5000 {
		t.Errorf("replay floor = %d, want 5000", got)
	}
}

// ---------------------------------------------------------------------------
// blobs
// ---------------------------------------------------------------------------

// SetMindRunBlobs upsert + SetMindRunAnswer + GetMindRunBlobs round trip;
// "the bundle round-trips through gzip byte-exact".
func TestContractMindRunBlobsRoundTrip(t *testing.T) {
	s, _ := contractOpenStore(t)
	id := contractEnqueue(t, s, MindRun{Trigger: "daily", Since: 1, Until: 2, CreatedAt: 1})
	bundle := []byte(`{"run":{"run_id":1},"prompts":[]}`)
	gz := contractGzip(t, bundle)
	if err := s.SetMindRunBlobs(id, gz, `{"sessions":["s1"]}`); err != nil {
		t.Fatalf("SetMindRunBlobs: %v", err)
	}
	b, err := s.GetMindRunBlobs(id)
	if err != nil {
		t.Fatal(err)
	}
	if b.RunID != id || !bytes.Equal(b.BundleGz, gz) || b.Manifest != `{"sessions":["s1"]}` || b.Answer != "" || b.PrunedAt != 0 {
		t.Errorf("blobs = run %d manifest %q answer %q pruned %d, bundle equal %v", b.RunID, b.Manifest, b.Answer, b.PrunedAt, bytes.Equal(b.BundleGz, gz))
	}
	zr, err := gzip.NewReader(bytes.NewReader(b.BundleGz))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if _, err := out.ReadFrom(zr); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), bundle) {
		t.Errorf("gunzipped bundle = %q, want %q", out.Bytes(), bundle)
	}

	if err := s.SetMindRunAnswer(id, `{"ops":[]}`); err != nil {
		t.Fatalf("SetMindRunAnswer: %v", err)
	}
	b, _ = s.GetMindRunBlobs(id)
	if b.Answer != `{"ops":[]}` || b.Manifest != `{"sessions":["s1"]}` {
		t.Errorf("after answer: manifest %q answer %q", b.Manifest, b.Answer)
	}

	// upsert: a second SetMindRunBlobs replaces bundle and manifest
	gz2 := contractGzip(t, []byte(`{"v":2}`))
	if err := s.SetMindRunBlobs(id, gz2, `{"v":2}`); err != nil {
		t.Fatal(err)
	}
	b, _ = s.GetMindRunBlobs(id)
	if !bytes.Equal(b.BundleGz, gz2) || b.Manifest != `{"v":2}` {
		t.Errorf("upsert did not replace: manifest %q", b.Manifest)
	}
}

// PruneMindRunBundles "For blob rows whose run `created_at < before` and
// `pruned_at = 0`: sets `bundle_gz` NULL and `pruned_at = at`; never touches
// `manifest`, `answer` or any `mind_runs` row; returns the count".
func TestContractPruneMindRunBundlesKeepsManifestsAndAnswers(t *testing.T) {
	s, _ := contractOpenStore(t)
	oldID := contractEnqueue(t, s, MindRun{Trigger: "daily", Since: 1, Until: 2, CreatedAt: 100})
	newID := contractEnqueue(t, s, MindRun{Trigger: "daily", Since: 1, Until: 2, CreatedAt: 900})
	for _, id := range []int64{oldID, newID} {
		if err := s.SetMindRunBlobs(id, contractGzip(t, []byte("bundle")), "manifest"); err != nil {
			t.Fatal(err)
		}
		if err := s.SetMindRunAnswer(id, "answer"); err != nil {
			t.Fatal(err)
		}
	}
	before := contractGetRun(t, s, oldID)

	n, err := s.PruneMindRunBundles(500, 4242)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("pruned %d, want 1", n)
	}
	ob, err := s.GetMindRunBlobs(oldID)
	if err != nil {
		t.Fatal(err)
	}
	if ob.BundleGz != nil || ob.PrunedAt != 4242 || ob.Manifest != "manifest" || ob.Answer != "answer" {
		t.Errorf("pruned blob = bundle nil %v pruned %d manifest %q answer %q", ob.BundleGz == nil, ob.PrunedAt, ob.Manifest, ob.Answer)
	}
	nb, _ := s.GetMindRunBlobs(newID)
	if nb.BundleGz == nil || nb.PrunedAt != 0 {
		t.Errorf("new blob pruned: bundle nil %v pruned %d", nb.BundleGz == nil, nb.PrunedAt)
	}
	if after := contractGetRun(t, s, oldID); !reflect.DeepEqual(after, before) {
		t.Errorf("prune touched the run row: %+v → %+v", before, after)
	}
	// already pruned rows are not counted again
	if n, _ := s.PruneMindRunBundles(500, 5000); n != 0 {
		t.Errorf("second prune = %d, want 0", n)
	}
	if ob, _ := s.GetMindRunBlobs(oldID); ob.PrunedAt != 4242 {
		t.Errorf("second prune restamped pruned_at = %d", ob.PrunedAt)
	}
}

// ---------------------------------------------------------------------------
// marks
// ---------------------------------------------------------------------------

// AddMMMark inserts; "A pending row with the same JobID, byte-equal Text and
// byte-equal SourceJSON created within the last 60 000 ms … is returned with
// folded == true and no insert".
func TestContractAddMMMarkInsertsAndFolds(t *testing.T) {
	s, _ := contractOpenStore(t)
	j := contractJob(t, s, "marks")
	src := `{"kind":"artifact","name":"50-x.md"}`
	id, folded, err := s.AddMMMark(MMMark{JobID: j, Slug: "marks", Text: "read this", SourceJSON: src, CreatedAt: 1_000_000})
	if err != nil || folded || id <= 0 {
		t.Fatalf("first AddMMMark = %d, %v, %v", id, folded, err)
	}
	id2, folded2, err := s.AddMMMark(MMMark{JobID: j, Slug: "marks", Text: "read this", SourceJSON: src, CreatedAt: 1_000_000 + 59_000})
	if err != nil || !folded2 || id2 != id {
		t.Errorf("same mark 59s later = %d, %v, %v; want %d folded", id2, folded2, err, id)
	}
	id3, folded3, err := s.AddMMMark(MMMark{JobID: j, Slug: "marks", Text: "read this too", SourceJSON: src, CreatedAt: 1_000_100})
	if err != nil || folded3 || id3 == id {
		t.Errorf("different text = %d, %v, %v; want a new row", id3, folded3, err)
	}
	id4, folded4, err := s.AddMMMark(MMMark{JobID: j, Slug: "marks", Text: "read this", SourceJSON: `{"kind":"manual"}`, CreatedAt: 1_000_200})
	if err != nil || folded4 || id4 == id {
		t.Errorf("different source = %d, %v, %v; want a new row", id4, folded4, err)
	}
	marks, err := s.ListMMMarks(j, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(marks) != 3 {
		t.Errorf("marks = %d rows, want 3 (the fold inserted nothing)", len(marks))
	}
	for _, m := range marks {
		if m.ID == id && (m.Text != "read this" || m.SourceJSON != src || m.JobID != j || m.Slug != "marks" || m.CreatedAt != 1_000_000 || m.ConsumedRun != 0) {
			t.Errorf("stored mark = %+v", m)
		}
	}
}

// ListMMMarks "Newest first; `jobID` 0 means every job including job-less
// marks"; pendingOnly filters consumed rows; ConsumeMMMarks "Stamps
// consumed_run/consumed_at on every listed PENDING row; an already-consumed
// row is left as it was (first run wins); unknown ids are ignored".
func TestContractMarksListAndConsume(t *testing.T) {
	s, _ := contractOpenStore(t)
	j := contractJob(t, s, "m")
	a, _, _ := s.AddMMMark(MMMark{JobID: j, Slug: "m", Text: "a", SourceJSON: "{}", CreatedAt: 10})
	b, _, _ := s.AddMMMark(MMMark{JobID: j, Slug: "m", Text: "b", SourceJSON: "{}", CreatedAt: 30})
	c, _, _ := s.AddMMMark(MMMark{JobID: 0, Text: "c", SourceJSON: "{}", CreatedAt: 20})

	ids := func(ms []MMMark) []int64 {
		out := []int64{}
		for _, m := range ms {
			out = append(out, m.ID)
		}
		return out
	}
	all, err := s.ListMMMarks(0, false)
	if err != nil {
		t.Fatal(err)
	}
	if want := []int64{b, c, a}; !reflect.DeepEqual(ids(all), want) {
		t.Errorf("ListMMMarks(0) = %v, want %v (newest first, job-less included)", ids(all), want)
	}
	jm, _ := s.ListMMMarks(j, false)
	if want := []int64{b, a}; !reflect.DeepEqual(ids(jm), want) {
		t.Errorf("ListMMMarks(j) = %v, want %v", ids(jm), want)
	}

	if err := s.ConsumeMMMarks([]int64{a, 99999}, 5, 500); err != nil {
		t.Fatalf("ConsumeMMMarks: %v", err)
	}
	if err := s.ConsumeMMMarks([]int64{a, b}, 6, 600); err != nil {
		t.Fatal(err)
	}
	got := map[int64]MMMark{}
	all, _ = s.ListMMMarks(0, false)
	for _, m := range all {
		got[m.ID] = m
	}
	if got[a].ConsumedRun != 5 || got[a].ConsumedAt != 500 {
		t.Errorf("mark a = run %d at %d, want 5/500 (first run wins)", got[a].ConsumedRun, got[a].ConsumedAt)
	}
	if got[b].ConsumedRun != 6 || got[b].ConsumedAt != 600 {
		t.Errorf("mark b = run %d at %d, want 6/600", got[b].ConsumedRun, got[b].ConsumedAt)
	}
	pend, _ := s.ListMMMarks(0, true)
	if want := []int64{c}; !reflect.DeepEqual(ids(pend), want) {
		t.Errorf("pending = %v, want %v", ids(pend), want)
	}
}

// RemoveMMMark "Deletes a pending row; `ErrMMMarkConsumed` … when
// consumed_run != 0".
func TestContractRemoveMMMark(t *testing.T) {
	s, _ := contractOpenStore(t)
	p, _, _ := s.AddMMMark(MMMark{Text: "pending", SourceJSON: "{}", CreatedAt: 1})
	c, _, _ := s.AddMMMark(MMMark{Text: "consumed", SourceJSON: "{}", CreatedAt: 2})
	if err := s.ConsumeMMMarks([]int64{c}, 1, 3); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveMMMark(p); err != nil {
		t.Fatalf("RemoveMMMark(pending): %v", err)
	}
	if err := s.RemoveMMMark(c); !errors.Is(err, ErrMMMarkConsumed) {
		t.Errorf("RemoveMMMark(consumed) = %v, want ErrMMMarkConsumed", err)
	}
	all, _ := s.ListMMMarks(0, false)
	if len(all) != 1 || all[0].ID != c {
		t.Errorf("marks after remove = %+v, want only the consumed one", all)
	}
}
