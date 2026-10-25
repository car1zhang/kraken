package store

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// mindpass_contract_test.go — athena-logic's BLIND contract tests for
// artifacts/50-contract.md (job 639, the mind pass), slice A: schema v37
// (`jobs.author`, the `mm_items` rebuild admitting `nightly` and adding
// `state`), `MMItem.State`, `ListMMItems`/`ListMMItemsFiltered` over the
// state, `FindMMItemsByQuote`, `ConfirmMMItem`, `Job.Author`,
// `ListJobsTouchedSince`/`CountJobsTouchedSince` and `ListQuestionsAll`.
//
// Written from the frozen contract alone; internal/store's working tree was
// never read. The v36 shape used to age a database is the committed v33
// `mm_items` DDL (git show HEAD:internal/store/schema.go — v34-v36 left that
// table alone) without the `jobs.author` column.
//
// Dimension: LOGIC — invariants and happy paths. The refusals (`AddMMItem`
// with an unknown state, `ConfirmMMItem`'s ErrNotFound / ErrMMItemInvalidated,
// `UpdateJobFields` with "author") are athena-edge's. Every package-level
// identifier here is prefixed `mpc`.

func mpcOpenStore(t *testing.T) (*Store, string) {
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

func mpcAddItem(t *testing.T, s *Store, it MMItem) int64 {
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

func mpcGetItem(t *testing.T, s *Store, id int64) MMItem {
	t.Helper()
	it, err := s.GetMMItem(id)
	if err != nil {
		t.Fatalf("GetMMItem(%d): %v", id, err)
	}
	return it
}

func mpcItemIDs(items []MMItem) []int64 {
	out := []int64{}
	for _, it := range items {
		out = append(out, it.ID)
	}
	return out
}

func mpcSameIDs(t *testing.T, what string, got []MMItem, want ...int64) {
	t.Helper()
	g := mpcItemIDs(got)
	if want == nil {
		want = []int64{}
	}
	if !reflect.DeepEqual(g, want) {
		t.Errorf("%s ids = %v, want %v", what, g, want)
	}
}

func mpcInvalidate(t *testing.T, s *Store, id int64, reason string) {
	t.Helper()
	if err := s.InvalidateMMItem(id, 1_700_000_000_500, reason, "test"); err != nil {
		t.Fatalf("InvalidateMMItem(%d): %v", id, err)
	}
}

func mpcContentSHA(t *testing.T, s *Store, id int64) string {
	t.Helper()
	var sha string
	if err := s.db.QueryRow(`SELECT content_sha FROM mm_items WHERE id = ?`, id).Scan(&sha); err != nil {
		t.Fatalf("read content_sha %d: %v", id, err)
	}
	return sha
}

func mpcExec(t *testing.T, s *Store, stmts ...string) {
	t.Helper()
	for _, q := range stmts {
		if _, err := s.db.Exec(q); err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
	}
}

// mpcAgeToV36 rewinds a freshly-opened (v37) database to the v36 shape:
// mm_items in its committed v33 DDL (the seven-value source CHECK, no
// `state`), rows carried across by name, and no `jobs.author`.
func mpcAgeToV36(t *testing.T, s *Store) {
	t.Helper()
	const cols = `id, kind, source, job_id, slug, source_ref, content, context, content_sha, snapshot, snapshot_ext,
		episode_id, created_at, invalidated_at, invalid_reason, invalid_detail, title, scope, repo, dir, quote,
		source_json, thread_session, revised_at`
	mpcExec(t, s,
		`CREATE TABLE mm_items_v36 (
	id             INTEGER PRIMARY KEY AUTOINCREMENT,
	kind           TEXT    NOT NULL CHECK(kind IN ('decision','reading','note')),
	source         TEXT    NOT NULL CHECK(source IN ('question','artifact','artifact-element','code','agent-output','manual','capture')),
	job_id         INTEGER NOT NULL DEFAULT 0,
	slug           TEXT    NOT NULL DEFAULT '',
	source_ref     TEXT    NOT NULL DEFAULT '',
	content        TEXT    NOT NULL,
	context        TEXT    NOT NULL DEFAULT '',
	content_sha    TEXT    NOT NULL DEFAULT '',
	snapshot       BLOB    NULL,
	snapshot_ext   TEXT    NOT NULL DEFAULT '',
	episode_id     INTEGER NOT NULL DEFAULT 0,
	created_at     INTEGER NOT NULL DEFAULT 0,
	invalidated_at INTEGER NULL,
	invalid_reason TEXT    NOT NULL DEFAULT '',
	invalid_detail TEXT    NOT NULL DEFAULT '',
	title          TEXT    NOT NULL DEFAULT '',
	scope          TEXT    NOT NULL DEFAULT 'repo' CHECK(scope IN ('universal','repo')),
	repo           TEXT    NOT NULL DEFAULT '',
	dir            TEXT    NOT NULL DEFAULT '',
	quote          TEXT    NOT NULL DEFAULT '',
	source_json    TEXT    NOT NULL DEFAULT '',
	thread_session TEXT    NOT NULL DEFAULT '',
	revised_at     INTEGER NOT NULL DEFAULT 0
)`,
		`INSERT INTO mm_items_v36 (`+cols+`) SELECT `+cols+` FROM mm_items`,
		`DROP TABLE mm_items`,
		`ALTER TABLE mm_items_v36 RENAME TO mm_items`,
		`CREATE INDEX mm_items_ref ON mm_items(source, job_id, source_ref)`,
		`ALTER TABLE jobs DROP COLUMN author`,
		`PRAGMA user_version = 36`,
	)
}

func mpcUserVersion(t *testing.T, s *Store) int {
	t.Helper()
	var v int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

type mpcItemSnap struct {
	ID                   int64
	SHA, Quote, Title    string
	InvalidatedAt        int64
	Source, Content, Dir string
}

func mpcSnap(t *testing.T, s *Store, id int64) mpcItemSnap {
	t.Helper()
	it := mpcGetItem(t, s, id)
	return mpcItemSnap{ID: it.ID, SHA: mpcContentSHA(t, s, id), Quote: it.Quote, Title: it.Title,
		InvalidatedAt: it.InvalidatedAt, Source: it.Source, Content: it.Content, Dir: it.Dir}
}

// ---------------------------------------------------------------------------
// schema v37
// ---------------------------------------------------------------------------

// `schemaVersion = 37`; `migrateV37MindPass`: "a v36 database with live and
// invalidated items migrates with identical ids, content_sha, quote and title
// on every row, and `state = 'live'` on all of them"; `jobs.author` added with
// DEFAULT 'unknown'; and after it `nightly` is an accepted source.
func TestMpcMigrateV37OverAnAgedV36Database(t *testing.T) {
	s, dbPath := mpcOpenStore(t)
	if v := mpcUserVersion(t, s); v != 39 {
		t.Fatalf("fresh user_version = %d, want 39", v)
	}
	jobID, err := s.CreateJob(&Job{Slug: "pre-v37", Repo: "kraken", Body: "spec"})
	if err != nil {
		t.Fatal(err)
	}
	live := mpcAddItem(t, s, MMItem{Kind: "decision", Source: "capture", Content: "live body", CreatedAt: 10,
		Title: "a live claim", Scope: "repo", Repo: "kraken", Dir: "internal/jobs", Quote: "the live quote", JobID: jobID})
	legacy := mpcAddItem(t, s, MMItem{Kind: "note", Source: "manual", Content: "legacy note", CreatedAt: 20})
	dead := mpcAddItem(t, s, MMItem{Kind: "note", Source: "capture", Content: "dead body", CreatedAt: 30,
		Title: "a dead claim", Scope: "universal", Quote: "the dead quote"})
	mpcInvalidate(t, s, dead, "removed")
	ids := []int64{live, legacy, dead}
	before := map[int64]mpcItemSnap{}
	for _, id := range ids {
		before[id] = mpcSnap(t, s, id)
	}

	mpcAgeToV36(t, s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("re-Open a v36-shaped database: %v", err)
	}
	defer s2.Close()

	if v := mpcUserVersion(t, s2); v != 39 {
		t.Errorf("migrated user_version = %d, want 39", v)
	}
	for _, id := range ids {
		if got := mpcSnap(t, s2, id); got != before[id] {
			t.Errorf("item %d after v37 = %+v, want %+v", id, got, before[id])
		}
		if st := mpcGetItem(t, s2, id).State; st != "live" {
			t.Errorf("item %d state = %q, want live", id, st)
		}
	}
	j, err := s2.GetJobBySlug("pre-v37")
	if err != nil {
		t.Fatal(err)
	}
	if j.Author != "unknown" {
		t.Errorf("pre-v37 job author = %q, want unknown (the column default)", j.Author)
	}
	// the widened CHECK and validMMSource
	n := mpcAddItem(t, s2, MMItem{Kind: "note", Source: "nightly", Content: "from last night", CreatedAt: 40})
	if it := mpcGetItem(t, s2, n); it.Source != "nightly" {
		t.Errorf("nightly item source = %q", it.Source)
	}
	// ids keep climbing past the carried rows (the sqlite_sequence carry)
	if n <= dead {
		t.Errorf("new id %d after the rebuild, want > %d", n, dead)
	}
}

// "Replayable: running the step twice over one database changes nothing the
// second time" — a fresh v37 database whose user_version alone is set back
// re-runs migrations[37] over a table that already has both changes.
func TestMpcMigrateV37IsReplayable(t *testing.T) {
	s, dbPath := mpcOpenStore(t)
	jobID, err := s.CreateJob(&Job{Slug: "replay", Repo: "kraken", Author: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	prop := mpcAddItem(t, s, MMItem{Source: "nightly", State: "proposed", Content: "p", Quote: "q", CreatedAt: 5, JobID: jobID})
	want := mpcSnap(t, s, prop)
	// The v37 hook is replayed on its own, inside a transaction, rather than
	// by resetting user_version and reopening: a reopen now also runs v39,
	// which flips every proposed row live on purpose and would hide what
	// this test is about (v37's rebuild must not re-default state).
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateV37MindPass(tx); err != nil {
		tx.Rollback()
		t.Fatalf("replaying v37 over a v37 shape: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	_ = dbPath
	s2 := s
	if got := mpcSnap(t, s2, prop); got != want {
		t.Errorf("item after replay = %+v, want %+v", got, want)
	}
	if st := mpcGetItem(t, s2, prop).State; st != "proposed" {
		t.Errorf("state after replay = %q, want proposed untouched", st)
	}
	j, err := s2.GetJobBySlug("replay")
	if err != nil {
		t.Fatal(err)
	}
	if j.Author != "operator" {
		t.Errorf("author after replay = %q, want operator untouched", j.Author)
	}
}

// ---------------------------------------------------------------------------
// MMItem.State and the lists
// ---------------------------------------------------------------------------

// `MMItem.State`: "`AddMMItem` stores `""` as `"live"`"; `validMMSource` gains
// `"nightly"`; a proposed nightly row round-trips.
func TestMpcAddMMItemStoresStateAndAcceptsNightly(t *testing.T) {
	s, _ := mpcOpenStore(t)
	def := mpcAddItem(t, s, MMItem{Source: "capture", Content: "c", CreatedAt: 1})
	prop := mpcAddItem(t, s, MMItem{Kind: "decision", Source: "nightly", State: "proposed", Content: "n", Quote: "verbatim", CreatedAt: 2})
	explicit := mpcAddItem(t, s, MMItem{Source: "nightly", State: "live", Content: "l", CreatedAt: 3})
	if st := mpcGetItem(t, s, def).State; st != "live" {
		t.Errorf("empty State stored as %q, want live", st)
	}
	p := mpcGetItem(t, s, prop)
	if p.State != "proposed" || p.Source != "nightly" || p.Kind != "decision" || p.Quote != "verbatim" {
		t.Errorf("proposed nightly row = %+v", p)
	}
	if st := mpcGetItem(t, s, explicit).State; st != "live" {
		t.Errorf("explicit live = %q", st)
	}
}

// `ListMMItems`: "`false` returns rows with `invalidated_at IS NULL AND state =
// 'live'` ONLY — a proposed row is never in that list …; `true` returns every
// row including proposed and invalidated; order `created_at DESC, id DESC`".
func TestMpcListMMItemsHoldsProposedOutOfTheLiveList(t *testing.T) {
	s, _ := mpcOpenStore(t)
	a := mpcAddItem(t, s, MMItem{Content: "a", CreatedAt: 10})
	p := mpcAddItem(t, s, MMItem{Source: "nightly", State: "proposed", Content: "p", CreatedAt: 30})
	d := mpcAddItem(t, s, MMItem{Content: "d", CreatedAt: 20})
	mpcInvalidate(t, s, d, "removed")
	b := mpcAddItem(t, s, MMItem{Source: "nightly", Content: "b", CreatedAt: 30})

	live, err := s.ListMMItems(false)
	if err != nil {
		t.Fatal(err)
	}
	mpcSameIDs(t, "ListMMItems(false)", live, b, a)

	all, err := s.ListMMItems(true)
	if err != nil {
		t.Fatal(err)
	}
	// p and b tie on created_at 30: id DESC puts b (added later) first
	mpcSameIDs(t, "ListMMItems(true)", all, b, p, d, a)
}

// `ListMMItemsFiltered`: "`Live: true` now means `invalidated_at IS NULL AND
// state = 'live'`; `State` adds `state = ?` when non-empty; both compose with
// the existing Repo/Scope/Dir/Q filters".
func TestMpcListMMItemsFilteredByLiveAndState(t *testing.T) {
	s, _ := mpcOpenStore(t)
	liveU := mpcAddItem(t, s, MMItem{Content: "live universal", Scope: "universal", Title: "alpha", CreatedAt: 10})
	propU := mpcAddItem(t, s, MMItem{Source: "nightly", State: "proposed", Content: "prop universal", Scope: "universal", Title: "alpha too", CreatedAt: 20})
	propR := mpcAddItem(t, s, MMItem{Source: "nightly", State: "proposed", Content: "prop repo", Scope: "repo", Repo: "kraken", Title: "beta", CreatedAt: 30})
	deadU := mpcAddItem(t, s, MMItem{Content: "dead universal", Scope: "universal", Title: "alpha dead", CreatedAt: 40})
	mpcInvalidate(t, s, deadU, "removed")

	get := func(f MMFilter) []MMItem {
		t.Helper()
		got, err := s.ListMMItemsFiltered(f)
		if err != nil {
			t.Fatalf("ListMMItemsFiltered(%+v): %v", f, err)
		}
		return got
	}
	mpcSameIDs(t, "Live", get(MMFilter{Live: true}), liveU)
	mpcSameIDs(t, "State proposed", get(MMFilter{State: "proposed"}), propR, propU)
	mpcSameIDs(t, "State live", get(MMFilter{State: "live"}), deadU, liveU)
	mpcSameIDs(t, "State proposed + Scope universal", get(MMFilter{State: "proposed", Scope: "universal"}), propU)
	mpcSameIDs(t, "State proposed + Q beta", get(MMFilter{State: "proposed", Q: "beta"}), propR)
	mpcSameIDs(t, "no filter", get(MMFilter{}), deadU, propR, propU, liveU)
}

// `FindMMItemsByQuote`: "every row whose `quote` column equals `quote`
// byte-for-byte (an empty quote returns an empty slice, no query), ordered
// `created_at DESC, id DESC`; both live and invalidated rows, every source and
// state".
func TestMpcFindMMItemsByQuoteMatchesExactlyNewestFirst(t *testing.T) {
	s, _ := mpcOpenStore(t)
	const q = "the trade mapping belongs in dispatch groups"
	capLive := mpcAddItem(t, s, MMItem{Source: "capture", Content: "c", Quote: q, CreatedAt: 10})
	nightDead := mpcAddItem(t, s, MMItem{Source: "nightly", Content: "n", Quote: q, CreatedAt: 20})
	mpcInvalidate(t, s, nightDead, "contradicted")
	nightProp := mpcAddItem(t, s, MMItem{Source: "nightly", State: "proposed", Content: "p", Quote: q, CreatedAt: 30})
	mpcAddItem(t, s, MMItem{Content: "x", Quote: q + "!", CreatedAt: 40})
	mpcAddItem(t, s, MMItem{Content: "y", Quote: "The trade mapping belongs in dispatch groups", CreatedAt: 50})
	mpcAddItem(t, s, MMItem{Source: "manual", Content: "no quote", CreatedAt: 60})

	got, err := s.FindMMItemsByQuote(q)
	if err != nil {
		t.Fatal(err)
	}
	mpcSameIDs(t, "FindMMItemsByQuote", got, nightProp, nightDead, capLive)
	if got[1].InvalidatedAt == 0 || got[1].InvalidReason != "contradicted" {
		t.Errorf("the invalidated match = %+v, want its invalidation carried", got[1])
	}
	if got[0].State != "proposed" {
		t.Errorf("the proposed match state = %q", got[0].State)
	}

	empty, err := s.FindMMItemsByQuote("")
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != 0 {
		t.Errorf("FindMMItemsByQuote(\"\") = %v, want empty (rows with an empty quote exist)", mpcItemIDs(empty))
	}
}

// `ConfirmMMItem`: "`state = 'live'` where `id = ? AND state = 'proposed' AND
// invalidated_at IS NULL` … an already-live row is a silent success".
func TestMpcConfirmMMItemMakesAProposedRowLive(t *testing.T) {
	s, _ := mpcOpenStore(t)
	p := mpcAddItem(t, s, MMItem{Source: "nightly", State: "proposed", Content: "p", Title: "t", Quote: "q", CreatedAt: 10})
	other := mpcAddItem(t, s, MMItem{Source: "nightly", State: "proposed", Content: "o", CreatedAt: 11})
	before := mpcSnap(t, s, p)

	if err := s.ConfirmMMItem(p); err != nil {
		t.Fatalf("ConfirmMMItem: %v", err)
	}
	if st := mpcGetItem(t, s, p).State; st != "live" {
		t.Errorf("state after confirm = %q, want live", st)
	}
	if got := mpcSnap(t, s, p); got != before {
		t.Errorf("confirm changed more than the state: %+v -> %+v", before, got)
	}
	if st := mpcGetItem(t, s, other).State; st != "proposed" {
		t.Errorf("an unconfirmed sibling became %q", st)
	}
	live, err := s.ListMMItems(false)
	if err != nil {
		t.Fatal(err)
	}
	mpcSameIDs(t, "ListMMItems(false) after confirm", live, p)

	if err := s.ConfirmMMItem(p); err != nil {
		t.Errorf("confirming an already-live row = %v, want a silent success", err)
	}
	legacy := mpcAddItem(t, s, MMItem{Content: "always live", CreatedAt: 12})
	if err := s.ConfirmMMItem(legacy); err != nil {
		t.Errorf("confirming a live capture row = %v, want a silent success", err)
	}
}

// ---------------------------------------------------------------------------
// Job.Author
// ---------------------------------------------------------------------------

// `Job.Author`: "scanned … as `COALESCE(author,'unknown')`; `CreateJob` inserts
// it with COALESCE(NULLIF(?, <empty>), 'unknown')"; "NOT in `jobFieldColumns` (a
// form/raw save cannot touch it)".
func TestMpcJobAuthorRoundTrips(t *testing.T) {
	s, _ := mpcOpenStore(t)
	for _, c := range []struct{ slug, author, want string }{
		{"by-operator", "operator", "operator"},
		{"by-agent", "agent", "agent"},
		{"by-linear", "linear", "linear"},
		{"by-kraken", "kraken", "kraken"},
		{"by-nobody", "", "unknown"},
	} {
		id, err := s.CreateJob(&Job{Slug: c.slug, Repo: "kraken", Body: "b", Author: c.author})
		if err != nil {
			t.Fatalf("CreateJob(%s): %v", c.slug, err)
		}
		bySlug, err := s.GetJobBySlug(c.slug)
		if err != nil {
			t.Fatal(err)
		}
		byID, err := s.GetJobByID(id)
		if err != nil {
			t.Fatal(err)
		}
		if bySlug.Author != c.want || byID.Author != c.want {
			t.Errorf("%s: author = %q / %q, want %q", c.slug, bySlug.Author, byID.Author, c.want)
		}
	}
	all, err := s.ListJobs()
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range all {
		if j.Slug == "by-agent" && j.Author != "agent" {
			t.Errorf("ListJobs author = %q, want agent", j.Author)
		}
	}

	// a field save of an existing job leaves the stored author alone
	j, _ := s.GetJobBySlug("by-agent")
	if _, err := s.UpdateJobFields(j.ID, j.Rev, map[string]any{"body": "edited spec"}); err != nil {
		t.Fatalf("UpdateJobFields: %v", err)
	}
	after, _ := s.GetJobBySlug("by-agent")
	if after.Body != "edited spec" || after.Author != "agent" {
		t.Errorf("after a body save: body %q author %q, want the edit and author agent", after.Body, after.Author)
	}
}

// ---------------------------------------------------------------------------
// ListJobsTouchedSince / CountJobsTouchedSince
// ---------------------------------------------------------------------------

func mpcPinTimes(t *testing.T, s *Store, id, created, updated int64) {
	t.Helper()
	if _, err := s.db.Exec(`UPDATE jobs SET created_at = ?, updated_at = ? WHERE id = ?`, created, updated, id); err != nil {
		t.Fatal(err)
	}
}

func mpcJobSlugs(js []*Job) []string {
	out := []string{}
	for _, j := range js {
		out = append(out, j.Slug)
	}
	return out
}

// `ListJobsTouchedSince`: "active AND archived rows where `created_at` or
// `updated_at` falls in the inclusive window (`untilMS` 0 = no ceiling), with
// `FormerSlugs` and `GoalIDs` attached exactly as `ListJobs` attaches them;
// order `created_at ASC, id ASC`"; `CountJobsTouchedSince` is "the count
// `ListJobsTouchedSince(sinceMS, 0)` would return".
func TestMpcListJobsTouchedSinceWindowAndOrder(t *testing.T) {
	s, _ := mpcOpenStore(t)
	mk := func(slug string, created, updated int64) int64 {
		t.Helper()
		id, err := s.CreateJob(&Job{Slug: slug, Repo: "kraken", Body: "b", CreatedAt: created, UpdatedAt: updated})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	mk("untouched", 100, 100)
	edited := mk("edited-in-window", 100, 500)
	floor := mk("on-the-floor", 400, 400)
	ceiling := mk("on-the-ceiling", 1000, 1000)
	mk("past-the-ceiling", 1001, 1001)
	sameTime := mk("same-created-as-floor", 400, 400)
	if _, err := s.CreateArchivedJob(&Job{Slug: "gone", ArchivedName: "gone", Repo: "kraken", Body: "b", CreatedAt: 600, ArchivedAt: 700}); err != nil {
		t.Fatal(err)
	}

	// FormerSlugs and GoalIDs on one row: a rename and a goal link, then the
	// timestamps pinned back into the window.
	if _, err := s.RenameJob(edited, 1, "edited-renamed"); err != nil {
		t.Fatalf("RenameJob: %v", err)
	}
	week, _ := GoalPeriods(time.Now())
	g, err := s.AddGoal(GoalAdd{Scope: "week", Period: week, Text: "ship the mind pass"}, time.Now().UnixMilli())
	if err != nil {
		t.Fatalf("AddGoal: %v", err)
	}
	if err := s.AddJobGoal(edited, g.ID); err != nil {
		t.Fatalf("AddJobGoal: %v", err)
	}
	mpcPinTimes(t, s, edited, 100, 500)
	_ = floor
	_ = ceiling
	_ = sameTime

	got, err := s.ListJobsTouchedSince(400, 1000)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"edited-renamed", "on-the-floor", "same-created-as-floor", "gone", "on-the-ceiling"}
	if g := mpcJobSlugs(got); !reflect.DeepEqual(g, want) {
		t.Fatalf("ListJobsTouchedSince(400, 1000) = %v, want %v", g, want)
	}
	for _, j := range got {
		if j.Slug == "gone" && j.ArchivedAt == 0 {
			t.Errorf("the archived row reads as active: %+v", j)
		}
	}

	active, err := s.ListJobs()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range active {
		if a.ID != edited {
			continue
		}
		for _, j := range got {
			if j.ID == edited {
				if !reflect.DeepEqual(j.FormerSlugs, a.FormerSlugs) || !reflect.DeepEqual(j.GoalIDs, a.GoalIDs) {
					t.Errorf("attached = former %v goals %v, ListJobs attaches former %v goals %v",
						j.FormerSlugs, j.GoalIDs, a.FormerSlugs, a.GoalIDs)
				}
				if len(j.FormerSlugs) != 1 || len(j.GoalIDs) != 1 || j.GoalIDs[0] != g.ID {
					t.Errorf("attached former %v goals %v, want [edited-in-window] [%d]", j.FormerSlugs, j.GoalIDs, g.ID)
				}
			}
		}
	}

	open, err := s.ListJobsTouchedSince(400, 0)
	if err != nil {
		t.Fatal(err)
	}
	wantOpen := []string{"edited-renamed", "on-the-floor", "same-created-as-floor", "gone", "on-the-ceiling", "past-the-ceiling"}
	if g := mpcJobSlugs(open); !reflect.DeepEqual(g, wantOpen) {
		t.Errorf("ListJobsTouchedSince(400, 0) = %v, want %v", g, wantOpen)
	}
	n, err := s.CountJobsTouchedSince(400)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(wantOpen) {
		t.Errorf("CountJobsTouchedSince(400) = %d, want %d", n, len(wantOpen))
	}
}

// ---------------------------------------------------------------------------
// ListQuestionsAll
// ---------------------------------------------------------------------------

// `ListQuestionsAll`: "every `questions` row of every job, ordered `job_id,
// position`; the same scan `ListQuestions` uses".
func TestMpcListQuestionsAllSpansEveryJob(t *testing.T) {
	s, _ := mpcOpenStore(t)
	a, err := s.CreateJob(&Job{Slug: "qa", Repo: "kraken"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateJob(&Job{Slug: "qb", Repo: "kraken"})
	if err != nil {
		t.Fatal(err)
	}
	// set b's first so insertion order differs from the stated order
	if err := s.SetQuestions(b, []DocQuestion{{QID: "Q1", Text: "b one", Importance: "P1", Recommendation: "yes"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetQuestions(a, []DocQuestion{
		{QID: "Q1", Text: "a one", Importance: "P0", Recommendation: "do it"},
		{QID: "Q2", Text: "a two", Importance: "P2"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.AnswerQuestion(a, "Q2", "no, keep it", "2026-10-22T03:00:00Z"); err != nil {
		t.Fatal(err)
	}

	got, err := s.ListQuestionsAll()
	if err != nil {
		t.Fatal(err)
	}
	qa, _ := s.ListQuestions(a)
	qb, _ := s.ListQuestions(b)
	want := append(append([]DocQuestion{}, qa...), qb...)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListQuestionsAll = %+v, want %+v", got, want)
	}
	if len(got) != 3 || got[1].Answer != "no, keep it" || got[2].JobID != b {
		t.Errorf("ListQuestionsAll = %+v", got)
	}
}
