package store

// submit_edge_test.go — BLIND edge tests for "eliminate the queued status"
// (artifacts/50-contract.md, slice S1 store). Authored from the frozen
// contract with no sight of the implementation.
//
// Dimension: boundaries, error paths, refusals. Two subjects: the field
// allowlist, which must now REFUSE the retired keys rather than silently
// accept them into a column that no longer exists; and migration 20, whose
// whole risk is deleting more than it was asked to.
//
// Every package-level identifier below is prefixed `edge`, and every test
// func `TestSubmitEdge`, so this file can sit beside the logic author's twin
// in the same package.

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
)

// edgeColumnExists reports whether table has a column of that name. Used to
// prove both halves of a drop: that the column really was there at the older
// shape (or the test proves nothing) and that it is gone afterwards.
//
// The table name is interpolated rather than bound: SQLite's table-valued
// pragma functions are happiest with a literal argument, and both callers
// pass a constant from this file. The COLUMN name stays a bound parameter.
func edgeColumnExists(t *testing.T, s *Store, table, column string) bool {
	t.Helper()
	var n int
	q := fmt.Sprintf(`SELECT COUNT(*) FROM pragma_table_info('%s') WHERE name = ?`, table)
	if err := s.db.QueryRow(q, column).Scan(&n); err != nil {
		t.Fatalf("pragma_table_info(%s) for %q: %v", table, column, err)
	}
	return n > 0
}

func edgeUserVersion(t *testing.T, s *Store) int {
	t.Helper()
	var v int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	return v
}

// --- the field allowlist --------------------------------------------------

// TestSubmitEdgeUpdateJobFieldsRefusesRetiredKeys: `ready` leaves
// jobFieldColumns with the column, and the allowlist is what turns a stale
// caller into a loud failure instead of a silent no-op. An UpdateJobFields
// that quietly dropped an unknown key would let a not-yet-updated writer
// (the old iOS form, a leftover call site) report success while writing
// nothing.
func TestSubmitEdgeUpdateJobFieldsRefusesRetiredKeys(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := openTest(t)
	id, err := s.CreateJob(&Job{Slug: "edge-guard", Body: "orig"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	for _, key := range []string{"ready", "prereqs"} {
		t.Run(key, func(t *testing.T) {
			_, err := s.UpdateJobFields(id, 1, map[string]any{key: true})
			if err == nil {
				t.Fatalf("UpdateJobFields(%q) = nil, want the unknown-column refusal", key)
			}
			// not a lookup or a uniqueness failure wearing the same coat —
			// the row exists and its slug is untouched
			if errors.Is(err, ErrNotFound) || errors.Is(err, ErrSlugTaken) {
				t.Fatalf("UpdateJobFields(%q) = %v, want the unknown-column refusal", key, err)
			}
			got, err := s.GetJobByID(id)
			if err != nil {
				t.Fatalf("GetJobByID: %v", err)
			}
			if got.Rev != 1 || got.Body != "orig" {
				t.Fatalf("row after a refused update = %+v, want rev 1 and body untouched", got)
			}
		})
	}
}

// TestSubmitEdgeUpdateJobFieldsRefusesAMixedUpdateWhole is the boundary the
// test above does not reach: a map carrying one LEGAL column beside the
// retired one. The refusal has to be all-or-nothing — applying the legal half
// and dropping the rest would bump rev, satisfy an optimistic-lock check, and
// leave the caller believing both writes landed.
func TestSubmitEdgeUpdateJobFieldsRefusesAMixedUpdateWhole(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := openTest(t)
	id, err := s.CreateJob(&Job{Slug: "edge-mixed", Body: "orig"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	if _, err := s.UpdateJobFields(id, 1, map[string]any{"state": "working", "ready": false}); err == nil {
		t.Fatal("UpdateJobFields with a legal key beside `ready` = nil, want the whole update refused")
	}
	got, err := s.GetJobByID(id)
	if err != nil {
		t.Fatalf("GetJobByID: %v", err)
	}
	if got.State != "" || got.Rev != 1 {
		t.Fatalf("row = %+v, want NOTHING applied — a partial update is worse than a refusal", got)
	}
}

// --- migration 20 ---------------------------------------------------------

// TestSubmitEdgeMigration20DeletesOnlyTheQueuedBoardOrder is the migration's
// blast-radius test. `DELETE FROM board_order WHERE "column"='queued'` is one
// missing WHERE clause away from wiping every saved column order on the
// board — a loss that is total, silent (the board just renders in its default
// order) and unrecoverable. So the assertion is not "queued is gone" but
// "queued is gone AND the others are byte-identical".
func TestSubmitEdgeMigration20DeletesOnlyTheQueuedBoardOrder(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := openTest(t)

	ids := map[string]int64{}
	for _, slug := range []string{"edge-draft", "edge-working", "edge-blocked", "edge-done", "edge-q1", "edge-q2"} {
		id, err := s.CreateJob(&Job{Slug: slug, Repo: "kraken", Body: "spec"})
		if err != nil {
			t.Fatalf("CreateJob %s: %v", slug, err)
		}
		ids[slug] = id
	}

	// edge-draft is deliberately given a QUEUED history first and then moved
	// to draft: after the migration its draft row must still be there, which
	// is what separates "delete the queued column's rows" from "delete every
	// row for a job that was ever queued".
	if err := s.SetBoardColumn("queued", []int64{ids["edge-draft"]}); err != nil {
		t.Fatalf("seed queued (pre-move): %v", err)
	}
	seed := []struct {
		col string
		ids []int64
	}{
		{"draft", []int64{ids["edge-draft"]}},
		{"working", []int64{ids["edge-working"]}},
		{"blocked", []int64{ids["edge-blocked"]}},
		{"done", []int64{ids["edge-done"]}},
		{"queued", []int64{ids["edge-q2"], ids["edge-q1"]}},
	}
	for _, sd := range seed {
		if err := s.SetBoardColumn(sd.col, sd.ids); err != nil {
			t.Fatalf("seed %s: %v", sd.col, err)
		}
	}

	before, err := s.GetBoardOrder()
	if err != nil {
		t.Fatalf("GetBoardOrder before: %v", err)
	}
	if len(before["queued"]) != 2 {
		t.Fatalf("fixture is wrong: queued = %v, want two rows to delete", before["queued"])
	}

	downgradeFreshDB(t, s, 19)
	if err := s.migrate(); err != nil {
		t.Fatalf("migrate from v19: %v", err)
	}
	if got := edgeUserVersion(t, s); got != schemaVersion {
		t.Fatalf("user_version = %d, want %d", got, schemaVersion)
	}

	after, err := s.GetBoardOrder()
	if err != nil {
		t.Fatalf("GetBoardOrder after: %v", err)
	}
	if len(after["queued"]) != 0 {
		t.Fatalf("queued order = %v after migration, want every queued row deleted", after["queued"])
	}
	for _, col := range []string{"draft", "working", "blocked", "done"} {
		if !reflect.DeepEqual(after[col], before[col]) {
			t.Fatalf("%s order = %v after migration, want it untouched at %v", col, after[col], before[col])
		}
	}
	// and the jobs themselves are not what the DELETE was aimed at
	for slug, id := range ids {
		j, err := s.GetJobBySlug(slug)
		if err != nil {
			t.Fatalf("GetJobBySlug(%s) after migration: %v", slug, err)
		}
		if j.ID != id {
			t.Fatalf("job %s = id %d, want %d — the migration must not touch the jobs table's ROWS", slug, j.ID, id)
		}
	}
}

// TestSubmitEdgeMigration20ReadyJobsBecomePlainDrafts is the cutover the
// components doc documents as one-time: the machine's currently-queued jobs
// come back as drafts for carl to re-submit. The failure this guards is the
// opposite of a loss — a migration that dropped the column but left the ROWS
// mangled (a lost body, a lost repo, a state invented from the old flag)
// would silently rewrite work carl has not read yet.
func TestSubmitEdgeMigration20ReadyJobsBecomePlainDrafts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := openTest(t)

	wasReady, err := s.CreateJob(&Job{Slug: "edge-was-ready", Repo: "kraken", Base: "main", Body: "\n# Spec\nqueued when the update landed\n"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	wasDraft, err := s.CreateJob(&Job{Slug: "edge-was-draft", Repo: "monolith", Body: "\n# Spec\nnever queued\n"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	wasWorking, err := s.CreateJob(&Job{Slug: "edge-was-working", Repo: "kraken", State: "working", Body: "spec"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	downgradeFreshDB(t, s, 19)

	// the column really is there at the v19 shape, or the drop below proves
	// nothing.
	if !edgeColumnExists(t, s, "jobs", "ready") {
		t.Fatal("jobs.ready absent at the v19 shape — downgradeFreshDB must restore it for this test to mean anything")
	}
	if _, err := s.db.Exec(`UPDATE jobs SET ready = 1 WHERE id IN (?, ?)`, wasReady, wasWorking); err != nil {
		t.Fatalf("seed ready=1 at the v19 shape: %v", err)
	}

	if err := s.migrate(); err != nil {
		t.Fatalf("migrate from v19: %v", err)
	}
	if got := edgeUserVersion(t, s); got != schemaVersion {
		t.Fatalf("user_version = %d, want %d", got, schemaVersion)
	}
	for _, col := range []string{"ready", "prereqs"} {
		if edgeColumnExists(t, s, "jobs", col) {
			t.Fatalf("jobs.%s still present after migration — the column is retired", col)
		}
	}

	cases := []struct {
		slug  string
		id    int64
		repo  string
		state string
	}{
		{"edge-was-ready", wasReady, "kraken", ""},
		{"edge-was-draft", wasDraft, "monolith", ""},
		{"edge-was-working", wasWorking, "kraken", "working"},
	}
	for _, c := range cases {
		j, err := s.GetJobBySlug(c.slug)
		if err != nil {
			t.Fatalf("GetJobBySlug(%s): %v", c.slug, err)
		}
		if j.ID != c.id || j.Repo != c.repo {
			t.Fatalf("%s = %+v, want id %d repo %q", c.slug, j, c.id, c.repo)
		}
		if j.State != c.state {
			t.Fatalf("%s state = %q, want %q — a dropped `ready` must not become a state", c.slug, j.State, c.state)
		}
		if j.Body == "" {
			t.Fatalf("%s lost its body across the migration", c.slug)
		}
	}

	// the migrated rows are still writable through the normal path — a
	// re-created table that lost a default or a NOT NULL would surface here
	// rather than at 3am on the first board save.
	got, err := s.GetJobBySlug("edge-was-ready")
	if err != nil {
		t.Fatalf("GetJobBySlug: %v", err)
	}
	if _, err := s.UpdateJobFields(got.ID, got.Rev, map[string]any{"state": "working"}); err != nil {
		t.Fatalf("UpdateJobFields on a migrated row: %v", err)
	}
}

// TestSubmitEdgeMigration20SurvivesAnEmptyV19Database is the degenerate
// input: no jobs, no board order. A DELETE and two column drops against an
// empty table must be a clean no-op rather than an error that leaves the
// database stranded below schemaVersion — a failed migration is an
// unopenable kraken.db, i.e. the daemon down.
func TestSubmitEdgeMigration20SurvivesAnEmptyV19Database(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := openTest(t)

	downgradeFreshDB(t, s, 19)
	if err := s.migrate(); err != nil {
		t.Fatalf("migrate an empty v19 database: %v", err)
	}
	if got := edgeUserVersion(t, s); got != schemaVersion {
		t.Fatalf("user_version = %d, want %d", got, schemaVersion)
	}
	order, err := s.GetBoardOrder()
	if err != nil {
		t.Fatalf("GetBoardOrder: %v", err)
	}
	if len(order["queued"]) != 0 {
		t.Fatalf("queued order = %v on an empty database", order["queued"])
	}
}
