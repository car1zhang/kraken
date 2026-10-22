package store

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// mindpass_edge_test.go — the EDGE dimension of 50-contract.md slice A (schema
// v37, `mm_items.state`, `jobs.author`, the new readers). Written blind from
// the frozen contract: schema.go, mm.go, jobs.go and questions.go were read
// only as committed at HEAD (before any implementer edit). Refusals,
// boundaries and the replayable migration; the round trips are athena-logic's.
//
// Every package-level identifier is prefixed `mpe`.

func mpeOpen(t *testing.T) *Store {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	s, err := Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

const mpeNow int64 = 1_770_000_000_000

func mpeItem(quote string) MMItem {
	return MMItem{Kind: "note", Source: "manual", Content: "body of " + quote, Quote: quote, Title: "t", CreatedAt: mpeNow}
}

func mpeAdd(t *testing.T, s *Store, it MMItem) int64 {
	t.Helper()
	id, err := s.AddMMItem(it)
	if err != nil {
		t.Fatalf("AddMMItem(%+v): %v", it, err)
	}
	return id
}

func mpeMasterSQL(t *testing.T, s *Store, name string) (sqlText string, rootpage int64) {
	t.Helper()
	if err := s.db.QueryRow(`SELECT sql, rootpage FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&sqlText, &rootpage); err != nil {
		t.Fatalf("sqlite_master %s: %v", name, err)
	}
	return sqlText, rootpage
}

func mpeColumns(t *testing.T, s *Store, table string) []string {
	t.Helper()
	rows, err := s.db.Query(`SELECT name FROM pragma_table_info(?) ORDER BY cid`, table)
	if err != nil {
		t.Fatalf("pragma_table_info(%s): %v", table, err)
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		cols = append(cols, c)
	}
	return cols
}

func mpeRunV37(t *testing.T, s *Store) {
	t.Helper()
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateV37MindPass(tx); err != nil {
		_ = tx.Rollback()
		t.Fatalf("migrateV37MindPass replay: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// v37 is replayable
// ---------------------------------------------------------------------------

// "Replayable: running the step twice over one database changes nothing the
// second time" — both halves. A fresh store has already run v37, so running
// the hook again (twice) must neither fail on the `author` ALTER (its probe
// sees the column) nor rebuild `mm_items` (its probe sees `'nightly'`): the
// table's root page, DDL, rows and sequence are untouched.
func TestMPEV37ReplayIsANoOp(t *testing.T) {
	s := mpeOpen(t)
	live := mpeAdd(t, s, mpeItem("live quote"))
	dead := mpeAdd(t, s, mpeItem("dead quote"))
	if err := s.InvalidateMMItem(dead, mpeNow+1, "removed", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateJob(&Job{Slug: "replay-job", Body: "b", Author: "operator"}); err != nil {
		t.Fatal(err)
	}
	beforeSQL, beforeRoot := mpeMasterSQL(t, s, "mm_items")
	beforeJobsSQL, _ := mpeMasterSQL(t, s, "jobs")
	beforeCols := mpeColumns(t, s, "jobs")
	beforeItems, err := s.ListMMItems(true)
	if err != nil {
		t.Fatal(err)
	}

	mpeRunV37(t, s)
	mpeRunV37(t, s)

	afterSQL, afterRoot := mpeMasterSQL(t, s, "mm_items")
	if afterRoot != beforeRoot || afterSQL != beforeSQL {
		t.Fatalf("replaying v37 rebuilt mm_items (rootpage %d → %d) — the 'nightly' probe must skip it", beforeRoot, afterRoot)
	}
	if afterJobsSQL, _ := mpeMasterSQL(t, s, "jobs"); afterJobsSQL != beforeJobsSQL {
		t.Fatalf("replaying v37 changed the jobs DDL:\nbefore %s\nafter  %s", beforeJobsSQL, afterJobsSQL)
	}
	if afterCols := mpeColumns(t, s, "jobs"); !reflect.DeepEqual(afterCols, beforeCols) {
		t.Fatalf("jobs columns %v → %v after a replay", beforeCols, afterCols)
	}
	afterItems, err := s.ListMMItems(true)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afterItems, beforeItems) {
		t.Fatalf("items changed across a replay:\nbefore %+v\nafter  %+v", beforeItems, afterItems)
	}
	// the sequence carries on: the next id follows the last one
	next := mpeAdd(t, s, mpeItem("next quote"))
	if next <= live || next <= dead {
		t.Fatalf("next id %d after a replay does not follow %d/%d", next, live, dead)
	}
}

// The two probes are independent: a database whose mm_items already carries
// 'nightly' but whose jobs table lacks `author` gets the column and NO
// mm_items rebuild; existing jobs read `unknown` (the column DEFAULT).
func TestMPEV37AuthorStepRunsAloneWhenOnlyItIsMissing(t *testing.T) {
	s := mpeOpen(t)
	id, err := s.CreateJob(&Job{Slug: "pre-author", Body: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`ALTER TABLE jobs DROP COLUMN author`); err != nil {
		t.Fatalf("fixture: drop jobs.author: %v", err)
	}
	_, beforeRoot := mpeMasterSQL(t, s, "mm_items")

	mpeRunV37(t, s)

	if _, afterRoot := mpeMasterSQL(t, s, "mm_items"); afterRoot != beforeRoot {
		t.Fatalf("mm_items rebuilt (rootpage %d → %d) although it already accepted 'nightly'", beforeRoot, afterRoot)
	}
	j, err := s.GetJobByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if j.Author != "unknown" {
		t.Fatalf("pre-existing job author = %q, want unknown", j.Author)
	}
}

// ---------------------------------------------------------------------------
// AddMMItem refusals
// ---------------------------------------------------------------------------

// "`AddMMItem` still refuses an unknown source with an error naming the
// accepted set" — and the set now includes nightly.
func TestMPEAddMMItemRefusesUnknownSourceNamingTheSet(t *testing.T) {
	s := mpeOpen(t)
	for _, src := range []string{"nightly-ish", "Nightly", "", "mind"} {
		it := mpeItem("q")
		it.Source = src
		_, err := s.AddMMItem(it)
		if err == nil {
			t.Errorf("AddMMItem(source %q) = nil error, want a refusal", src)
			continue
		}
		for _, name := range []string{"question", "artifact", "artifact-element", "code", "agent-output", "manual", "capture", "nightly"} {
			if !strings.Contains(err.Error(), name) {
				t.Errorf("source %q refusal %q does not name %q", src, err, name)
			}
		}
	}
	if all, _ := s.ListMMItems(true); len(all) != 0 {
		t.Fatalf("refused adds stored %d rows", len(all))
	}
}

// "`AddMMItem` stores `""` as `"live"` and refuses a value outside
// `validMMState`" — the refusal names both members of the set.
func TestMPEAddMMItemRefusesUnknownState(t *testing.T) {
	s := mpeOpen(t)
	for _, st := range []string{"dead", "Proposed", "LIVE", "invalidated", " live"} {
		it := mpeItem("q")
		it.State = st
		_, err := s.AddMMItem(it)
		if err == nil {
			t.Errorf("AddMMItem(state %q) = nil error, want a refusal", st)
			continue
		}
		for _, name := range []string{"proposed", "live"} {
			if !strings.Contains(err.Error(), name) {
				t.Errorf("state %q refusal %q does not name %q", st, err, name)
			}
		}
	}
	if all, _ := s.ListMMItems(true); len(all) != 0 {
		t.Fatalf("refused adds stored %d rows", len(all))
	}
}

// ---------------------------------------------------------------------------
// ConfirmMMItem
// ---------------------------------------------------------------------------

// "an unknown id returns `ErrNotFound` (wrapped)".
func TestMPEConfirmMMItemUnknownIDIsErrNotFound(t *testing.T) {
	s := mpeOpen(t)
	for _, id := range []int64{0, -1, 999} {
		if err := s.ConfirmMMItem(id); !errors.Is(err, ErrNotFound) {
			t.Errorf("ConfirmMMItem(%d) = %v, want ErrNotFound", id, err)
		}
	}
}

// "an invalidated row returns … `ErrMMItemInvalidated` (wrapped)" — and the
// row stays proposed: confirming a dead row writes nothing.
func TestMPEConfirmMMItemInvalidatedRowIsRefused(t *testing.T) {
	s := mpeOpen(t)
	it := mpeItem("proposed then dropped")
	it.Source, it.State = "nightly", "proposed"
	id := mpeAdd(t, s, it)
	if err := s.InvalidateMMItem(id, mpeNow+5, "removed", ""); err != nil {
		t.Fatal(err)
	}
	err := s.ConfirmMMItem(id)
	if !errors.Is(err, ErrMMItemInvalidated) {
		t.Fatalf("ConfirmMMItem(invalidated) = %v, want ErrMMItemInvalidated", err)
	}
	if ErrMMItemInvalidated.Error() != "mm item is invalidated" {
		t.Fatalf("ErrMMItemInvalidated text = %q, want %q", ErrMMItemInvalidated.Error(), "mm item is invalidated")
	}
	got, err := s.GetMMItem(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "proposed" {
		t.Fatalf("refused confirm moved state to %q, want it still proposed", got.State)
	}
}

// An invalidated LIVE row (never proposed) is invalidated all the same — the
// sentinel is about invalidated_at, not about which state it died in.
func TestMPEConfirmMMItemInvalidatedLiveRowIsRefused(t *testing.T) {
	s := mpeOpen(t)
	id := mpeAdd(t, s, mpeItem("live then dropped"))
	if err := s.InvalidateMMItem(id, mpeNow+5, "removed", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.ConfirmMMItem(id); !errors.Is(err, ErrMMItemInvalidated) {
		t.Fatalf("ConfirmMMItem(invalidated live row) = %v, want ErrMMItemInvalidated", err)
	}
}

// "an already-live row is a silent success" — repeatedly.
func TestMPEConfirmMMItemAlreadyLiveIsSilent(t *testing.T) {
	s := mpeOpen(t)
	id := mpeAdd(t, s, mpeItem("already live"))
	for i := 0; i < 2; i++ {
		if err := s.ConfirmMMItem(id); err != nil {
			t.Fatalf("ConfirmMMItem(live) #%d = %v, want nil", i+1, err)
		}
	}
}

// ---------------------------------------------------------------------------
// FindMMItemsByQuote
// ---------------------------------------------------------------------------

// "an empty quote returns an empty slice, no query" — even though rows with
// an empty quote column exist (every legacy mark has one).
func TestMPEFindMMItemsByQuoteEmptyIsEmpty(t *testing.T) {
	s := mpeOpen(t)
	mpeAdd(t, s, mpeItem(""))
	mpeAdd(t, s, mpeItem(""))
	got, err := s.FindMMItemsByQuote("")
	if err != nil {
		t.Fatalf("FindMMItemsByQuote(\"\") = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("FindMMItemsByQuote(\"\") returned %d rows, want none", len(got))
	}
}

// "every row whose `quote` column equals `quote` byte-for-byte" — no case
// folding, no trimming, no prefix match.
func TestMPEFindMMItemsByQuoteIsByteExact(t *testing.T) {
	s := mpeOpen(t)
	want := mpeAdd(t, s, mpeItem("the board owns ordering"))
	for _, near := range []string{"The board owns ordering", "the board owns ordering ", " the board owns ordering", "the board owns orderin", "the board owns ordering."} {
		mpeAdd(t, s, mpeItem(near))
	}
	got, err := s.FindMMItemsByQuote("the board owns ordering")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != want {
		ids := []int64{}
		for _, g := range got {
			ids = append(ids, g.ID)
		}
		t.Fatalf("byte-exact lookup returned ids %v, want only %d", ids, want)
	}
}

// ---------------------------------------------------------------------------
// jobs.author
// ---------------------------------------------------------------------------

// "`UpdateJobFields` with `"author"` is refused exactly as `"account_slug"` is
// today" — an error, and nothing written (rev unchanged, author unchanged).
func TestMPEUpdateJobFieldsRefusesAuthor(t *testing.T) {
	s := mpeOpen(t)
	id, err := s.CreateJob(&Job{Slug: "authored", Body: "b", Author: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.GetJobByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateJobFields(id, before.Rev, map[string]any{"account_slug": "x"}); err == nil {
		t.Fatal("premise: UpdateJobFields(account_slug) is accepted — the comparison point has moved")
	}
	for _, fields := range []map[string]any{
		{"author": "agent"},
		{"body": "new body", "author": "agent"},
	} {
		if _, err := s.UpdateJobFields(id, before.Rev, fields); err == nil {
			t.Errorf("UpdateJobFields(%v) = nil error, want a refusal", fields)
		}
	}
	after, err := s.GetJobByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if after.Author != "operator" || after.Rev != before.Rev || after.Body != before.Body {
		t.Fatalf("refused update wrote: author %q rev %d body %q, want operator/%d/%q", after.Author, after.Rev, after.Body, before.Rev, before.Body)
	}
}

// CreateJob inserts it through COALESCE/NULLIF, "so an empty `Author` stores
// `unknown`".
func TestMPECreateJobEmptyAuthorStoresUnknown(t *testing.T) {
	s := mpeOpen(t)
	id, err := s.CreateJob(&Job{Slug: "no-author", Body: "b"})
	if err != nil {
		t.Fatal(err)
	}
	j, err := s.GetJobByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if j.Author != "unknown" {
		t.Fatalf("author = %q, want unknown", j.Author)
	}
}

// The column's `CHECK(author IN ('operator','agent','linear','kraken',
// 'unknown'))`: a value outside the set never lands.
func TestMPECreateJobRefusesAuthorOutsideTheSet(t *testing.T) {
	s := mpeOpen(t)
	for _, a := range []string{"carl", "Operator", "human"} {
		if _, err := s.CreateJob(&Job{Slug: "bad-author-" + strings.ToLower(a), Body: "b", Author: a}); err == nil {
			t.Errorf("CreateJob(author %q) = nil error, want the CHECK to refuse it", a)
		}
	}
	if jobs, _ := s.ListJobs(); len(jobs) != 0 {
		t.Fatalf("refused creates left %d rows", len(jobs))
	}
}

// ---------------------------------------------------------------------------
// ListJobsTouchedSince / CountJobsTouchedSince
// ---------------------------------------------------------------------------

func mpeJobAt(t *testing.T, s *Store, slug string, created, updated int64) int64 {
	t.Helper()
	id, err := s.CreateJob(&Job{Slug: slug, Body: "b", CreatedAt: created, UpdatedAt: updated})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mpeSlugs(js []*Job) []string {
	out := []string{}
	for _, j := range js {
		out = append(out, j.Slug)
	}
	return out
}

// "rows where `created_at` or `updated_at` falls in the inclusive window
// (`untilMS` 0 = no ceiling) …; order `created_at ASC, id ASC`".
func TestMPEListJobsTouchedSinceWindowEdges(t *testing.T) {
	s := mpeOpen(t)
	const since, until = int64(1_000_000), int64(2_000_000)
	mpeJobAt(t, s, "before", since-1, since-1)
	mpeJobAt(t, s, "at-since", since, since)
	mpeJobAt(t, s, "old-but-updated-at-until", since-500, until)
	mpeJobAt(t, s, "at-until", until, until)
	mpeJobAt(t, s, "after", until+1, until+1)

	got, err := s.ListJobsTouchedSince(since, until)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"old-but-updated-at-until", "at-since", "at-until"}; !reflect.DeepEqual(mpeSlugs(got), want) {
		t.Fatalf("window [%d,%d] = %v, want %v", since, until, mpeSlugs(got), want)
	}

	open, err := s.ListJobsTouchedSince(since, 0)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"old-but-updated-at-until", "at-since", "at-until", "after"}; !reflect.DeepEqual(mpeSlugs(open), want) {
		t.Fatalf("window [%d, no ceiling] = %v, want %v", since, mpeSlugs(open), want)
	}

	n, err := s.CountJobsTouchedSince(since)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(open) {
		t.Fatalf("CountJobsTouchedSince = %d, want %d (ListJobsTouchedSince(since, 0)'s length)", n, len(open))
	}
}

// "order `created_at ASC, id ASC`" — equal created_at breaks on id.
func TestMPEListJobsTouchedSinceTieBreaksOnID(t *testing.T) {
	s := mpeOpen(t)
	const at = int64(5_000_000)
	a := mpeJobAt(t, s, "tie-first", at, at)
	b := mpeJobAt(t, s, "tie-second", at, at)
	got, err := s.ListJobsTouchedSince(at, at)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != a || got[1].ID != b {
		t.Fatalf("tie order = %v, want ids [%d %d]", mpeSlugs(got), a, b)
	}
}

// "active AND archived rows".
func TestMPEListJobsTouchedSinceIncludesArchived(t *testing.T) {
	s := mpeOpen(t)
	const since = int64(1_000_000)
	id := mpeJobAt(t, s, "gone-to-archive", since+1, since+1)
	if _, err := s.ArchiveJob(id); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListJobsTouchedSince(since, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != id {
		t.Fatalf("touched since = %v, want the archived job %d", mpeSlugs(got), id)
	}
	if n, err := s.CountJobsTouchedSince(since); err != nil || n != 1 {
		t.Fatalf("CountJobsTouchedSince = (%d, %v), want (1, nil)", n, err)
	}
}

// An empty window is an empty answer, not an error.
func TestMPEListJobsTouchedSinceEmpty(t *testing.T) {
	s := mpeOpen(t)
	got, err := s.ListJobsTouchedSince(1, 0)
	if err != nil || len(got) != 0 {
		t.Fatalf("empty store = (%v, %v), want (none, nil)", mpeSlugs(got), err)
	}
	if n, err := s.CountJobsTouchedSince(1); err != nil || n != 0 {
		t.Fatalf("CountJobsTouchedSince on an empty store = (%d, %v), want (0, nil)", n, err)
	}
}

// ---------------------------------------------------------------------------
// ListQuestionsAll
// ---------------------------------------------------------------------------

// No questions anywhere: an empty answer, no error.
func TestMPEListQuestionsAllEmpty(t *testing.T) {
	s := mpeOpen(t)
	got, err := s.ListQuestionsAll()
	if err != nil || len(got) != 0 {
		t.Fatalf("ListQuestionsAll on an empty store = (%d rows, %v), want (0, nil)", len(got), err)
	}
}
