package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// jobgoals_contract_test.go — athena-logic's BLIND contract tests for
// 50-contract.md, Slice S1's store half: schema v27 (the job_goals table, the
// goal_id backfill, the downgrade arm), the three link verbs
// (AddJobGoal/RemoveJobGoal/ClearJobGoals), the batch reader
// ListJobGoalLinks, and store.Job.GoalIDs' loader fill.
//
// Written from the contract text alone. The working-tree versions of
// internal/store/{schema,jobs,goals,jobgoals}*.go were never read; every piece
// of context here is the PRE-EXISTING state at the base commit (HEAD =
// 66eb71a) — store_test.go's openTest shape, schema_test.go's downgradeFreshDB
// and schemaDump, jobs.go's jobFieldColumns/UpdateJobFields contract, and the
// prior round's goals_contract_test.go.
//
// Dimension: LOGIC — the stated invariants and their happy paths. The
// refusals (a missing goal's descriptive sentence, a missing job's ErrNotFound,
// hostile ids) belong to athena-edge and are not probed here; the no-op arms
// kept below are SUCCESS contracts ("adding an existing link is a silent
// no-op"), not rejections.
//
// Every package-level identifier declared here is prefixed `contractJG` —
// athena-edge shares this package's test namespace.

// contractJGNow is the fixed write clock every AddGoal below stamps with.
const contractJGNow = int64(1_700_000_000_000)

// contractJGOpen opens an ISOLATED store (t.TempDir, never ~/.kraken) and
// returns it with its path, so a test can close it, re-Open the same file and
// watch migrate() walk forward — which is the only way to observe a migration.
func contractJGOpen(t *testing.T) (*Store, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "kraken.db")
	s, err := Open(p)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s, p
}

// contractJGGoal seeds one week goal in the CURRENT period.
func contractJGGoal(t *testing.T, s *Store, text string) *Goal {
	t.Helper()
	week, _ := GoalPeriods(time.Now())
	g, err := s.AddGoal(GoalAdd{Scope: "week", Period: week, Text: text}, contractJGNow)
	if err != nil {
		t.Fatalf("AddGoal(%q): %v", text, err)
	}
	if g == nil {
		t.Fatalf("AddGoal(%q) returned a nil goal with a nil error", text)
	}
	return g
}

// contractJGJob creates a job whose rev and updated_at are DELIBERATELY not
// "now": the three link verbs' shared obligation is that they move neither,
// and a row stamped at the current millisecond cannot tell a preserved
// updated_at from a freshly rewritten one.
func contractJGJob(t *testing.T, s *Store, slug string) *Job {
	t.Helper()
	row := Job{Slug: slug, Repo: "kraken", State: "working", Rev: 5,
		CreatedAt: 1_600_000_000_000, UpdatedAt: 1_600_000_000_000}
	id, err := s.CreateJob(&row)
	if err != nil {
		t.Fatalf("CreateJob %s: %v", slug, err)
	}
	got, err := s.GetJobByID(id)
	if err != nil {
		t.Fatalf("GetJobByID %d: %v", id, err)
	}
	return got
}

// contractJGLink adds one link and fails the test if the store refuses it.
func contractJGLink(t *testing.T, s *Store, jobID, goalID int64) {
	t.Helper()
	if err := s.AddJobGoal(jobID, goalID); err != nil {
		t.Fatalf("AddJobGoal(job %d, goal %d): %v", jobID, goalID, err)
	}
}

// contractJGIDs re-reads a job's link ids through the by-id loader.
func contractJGIDs(t *testing.T, s *Store, jobID int64) []int64 {
	t.Helper()
	j, err := s.GetJobByID(jobID)
	if err != nil {
		t.Fatalf("GetJobByID %d: %v", jobID, err)
	}
	return j.GoalIDs
}

// contractJGSameIDs compares two id slices ELEMENT BY ELEMENT: the contract's
// word is "in position order", so a set comparison would pass an
// implementation that lost the order entirely.
func contractJGSameIDs(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// contractJGPositions reads the raw (goal_id, position) pairs for one job in
// position order. Raw SQL because `position` is deliberately NOT on the wire
// or on store.Job — the ORDER is the observable, and the column is what
// produces it, so the two obligations that talk about positions directly
// ("appends at MAX(position)+1", "surviving links keep their positions") can
// only be stated here.
func contractJGPositions(t *testing.T, s *Store, jobID int64) (ids []int64, positions []int64) {
	t.Helper()
	rows, err := s.db.Query(
		`SELECT goal_id, position FROM job_goals WHERE job_id = ? ORDER BY position`, jobID)
	if err != nil {
		t.Fatalf("read job_goals for job %d: %v", jobID, err)
	}
	defer rows.Close()
	for rows.Next() {
		var gid, pos int64
		if err := rows.Scan(&gid, &pos); err != nil {
			t.Fatalf("scan job_goals row: %v", err)
		}
		ids = append(ids, gid)
		positions = append(positions, pos)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("job_goals rows: %v", err)
	}
	return ids, positions
}

// contractJGJobIDBySlug reads a job's id with raw SQL — used only where the
// Go loaders are unusable, i.e. against a DOWNGRADED (v26-shaped) database.
func contractJGJobIDBySlug(t *testing.T, s *Store, slug string) int64 {
	t.Helper()
	var id int64
	if err := s.db.QueryRow(`SELECT id FROM jobs WHERE slug = ?`, slug).Scan(&id); err != nil {
		t.Fatalf("read id for job %s: %v", slug, err)
	}
	return id
}

// ---------------------------------------------------------------------------
// schema v27
// ---------------------------------------------------------------------------

// TestContractJGSchemaVersionIsAtLeastV27 is the one-line pin the whole
// migration hangs off: v27 is APPENDED, so schemaVersion moves and no
// released schemaV<N> string is edited. A migration written into schemaV26
// in place would leave every existing kraken.db on this machine at v26
// forever, with the new table missing and nothing anywhere reporting a
// problem.
func TestContractJGSchemaVersionIsAtLeastV27(t *testing.T) {
	// A FLOOR rather than an exact pin (watch_contract_test.go's S1 shape):
	// what this test claims is that v27 was APPENDED, which stays true as
	// later migrations append past it — v28's account-binding columns are the
	// first to do so. An exact pin would fail on that true statement.
	if schemaVersion < 27 {
		t.Fatalf("schemaVersion = %d, want >= 27", schemaVersion)
	}
}

// TestContractJGMigrationBackfillsEveryGoalIDAndNoNull is the migration's data
// half, driven the only way it can be observed: downgrade a fresh store to the
// v26 SHAPE, write real v26 rows into it (two jobs carrying goal_id, one
// carrying NULL), then re-Open the same file and let migrate() walk forward.
//
// Two clauses, and both matter. Every pre-existing link must survive as
// exactly one row at position 0 — a job's whole label is that one id, and a
// backfill that ran AFTER the DROP would find nothing to copy and silently
// unlabel every card carl has ever filed. And a NULL must yield NO row: a
// backfill without the `WHERE goal_id IS NOT NULL` guard writes a link to goal
// 0, which is a foreign key onto a row that has never existed.
func TestContractJGMigrationBackfillsEveryGoalIDAndNoNull(t *testing.T) {
	s, p := contractJGOpen(t)
	downgradeFreshDB(t, s, 26)

	// goals is untouched by v27, so the ordinary writer still works here
	first := contractJGGoal(t, s, "the goal a linked job serves")
	second := contractJGGoal(t, s, "another linked goal")

	seed := contractJGSeedV26Job(t, s)
	seed("linked-a", first.ID)
	seed("linked-b", second.ID)
	seed("unlinked", nil)

	if err := s.Close(); err != nil {
		t.Fatalf("Close before re-Open: %v", err)
	}
	s2, err := Open(p)
	if err != nil {
		t.Fatalf("re-Open a v26-shaped db (the v27 migration): %v", err)
	}
	defer s2.Close()

	var total int
	if err := s2.db.QueryRow(`SELECT COUNT(*) FROM job_goals`).Scan(&total); err != nil {
		t.Fatalf("count job_goals: %v", err)
	}
	if total != 2 {
		t.Fatalf("job_goals holds %d rows, want exactly 2 — one per non-NULL goal_id", total)
	}

	for _, c := range []struct {
		slug string
		want int64
	}{{"linked-a", first.ID}, {"linked-b", second.ID}} {
		id := contractJGJobIDBySlug(t, s2, c.slug)
		ids, positions := contractJGPositions(t, s2, id)
		if !contractJGSameIDs(ids, []int64{c.want}) {
			t.Errorf("job %s backfilled to %v, want [%d]", c.slug, ids, c.want)
			continue
		}
		if positions[0] != 0 {
			t.Errorf("job %s backfilled at position %d, want 0", c.slug, positions[0])
		}
		// and the same answer through the loader every reader actually uses
		row, err := s2.GetJobBySlug(c.slug)
		if err != nil {
			t.Fatalf("GetJobBySlug %s: %v", c.slug, err)
		}
		if !contractJGSameIDs(row.GoalIDs, []int64{c.want}) {
			t.Errorf("GetJobBySlug(%s).GoalIDs = %v, want [%d]", c.slug, row.GoalIDs, c.want)
		}
	}

	unlinkedID := contractJGJobIDBySlug(t, s2, "unlinked")
	if ids, _ := contractJGPositions(t, s2, unlinkedID); len(ids) != 0 {
		t.Errorf("a job whose goal_id was NULL backfilled to %v, want no row at all", ids)
	}
	row, err := s2.GetJobBySlug("unlinked")
	if err != nil {
		t.Fatalf("GetJobBySlug(unlinked): %v", err)
	}
	if len(row.GoalIDs) != 0 {
		t.Errorf("GetJobBySlug(unlinked).GoalIDs = %v, want empty", row.GoalIDs)
	}

	// the column is GONE — the drop is the second half of the migration, and
	// leaving it would give the link two sources of truth that nothing keeps
	// in step
	if contractJGHasColumn(t, s2, "jobs", "goal_id") {
		t.Error("jobs still carries goal_id after v27; the migration drops the column")
	}
}

// contractJGColumns reads a table's declared column names off PRAGMA
// table_info rather than off the DDL text — the DDL SQLite rewrites for a DROP
// COLUMN is not something a substring match can be trusted on.
func contractJGColumns(t *testing.T, s *Store, table string) map[string]bool {
	t.Helper()
	rows, err := s.db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info(%s): %v", table, err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var (
			cid        int
			name, typ  string
			notNull    int
			dflt       sql.NullString
			primaryKey int
		)
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &primaryKey); err != nil {
			t.Fatalf("scan table_info row: %v", err)
		}
		out[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("table_info rows: %v", err)
	}
	return out
}

// contractJGHasColumn reports whether a table declares a column.
func contractJGHasColumn(t *testing.T, s *Store, table, column string) bool {
	t.Helper()
	return contractJGColumns(t, s, table)[column]
}

// contractJGSeedV26Job returns a `seed(slug, goalID)` writer for a store that
// has ALREADY been rolled back to the v26 shape, inserting one row the way a
// real v26 database carried one: a slug and a goal_id (NULL for an unlinked
// job), plus whatever ordinary columns that shape still declares.
//
// The column list is derived from the DOWNGRADED DATABASE ITSELF rather than
// written out by hand, and that is the whole point of the helper. `jobs` has
// lost columns between v1 and v26 — a hand-copied list taken from schemaV1's
// text names columns the v26 table does not have, and SQLite answers that with
// a fixture error ("table jobs has no column named …") that looks exactly like
// a migration bug. Reading the shape back keeps this fixture honest about what
// a v26 row IS, and it stays blind: PRAGMA table_info reports the schema, not
// anything this change's implementation does with it.
func contractJGSeedV26Job(t *testing.T, s *Store) func(slug string, goalID any) {
	t.Helper()
	cols := contractJGColumns(t, s, "jobs")
	for _, need := range []string{"slug", "goal_id"} {
		if !cols[need] {
			t.Fatalf("the downgraded jobs table has no %q column; a v26 row cannot be seeded", need)
		}
	}
	// The two per-row columns first, then every constant column the v26 shape
	// still declares. A column this list names but the table has dropped is
	// simply skipped; one the table has and this list does not takes its own
	// declared default, which is what a v26 writer would have left it at.
	names := []string{"slug", "goal_id"}
	var constants []any
	for _, c := range []struct {
		name string
		val  any
	}{
		{"repo", "kraken"},
		{"state", "working"},
		{"rev", int64(1)},
		{"created_at", contractJGNow},
		{"updated_at", contractJGNow},
	} {
		if cols[c.name] {
			names = append(names, c.name)
			constants = append(constants, c.val)
		}
	}
	stmt := `INSERT INTO jobs (` + strings.Join(names, ", ") + `) VALUES (` +
		strings.TrimSuffix(strings.Repeat("?, ", len(names)), ", ") + `)`

	return func(slug string, goalID any) {
		t.Helper()
		args := make([]any, 0, len(names))
		args = append(args, slug, goalID)
		args = append(args, constants...)
		if _, err := s.db.Exec(stmt, args...); err != nil {
			t.Fatalf("seed v26 job %s (%s): %v", slug, stmt, err)
		}
	}
}

// TestContractJGMigratedV26SchemaMatchesFresh is the shape half of the same
// migration, and it is a case TestMigratedSchemaMatchesFreshSchema does not
// cover: that test walks from v1..v10 and v15, so a v27 step that creates
// job_goals with a subtly different declaration from schemaV1's — a missing
// NOT NULL, a forgotten index, a different PK order — would pass every one of
// its cases while leaving the ONE shape every real database on this machine
// actually takes (v26 → v27) different from a fresh one.
func TestContractJGMigratedV26SchemaMatchesFresh(t *testing.T) {
	fresh, _ := contractJGOpen(t)
	want := schemaDump(t, fresh)

	s, p := contractJGOpen(t)
	downgradeFreshDB(t, s, 26)
	if err := s.Close(); err != nil {
		t.Fatalf("Close before re-Open: %v", err)
	}
	s2, err := Open(p)
	if err != nil {
		t.Fatalf("re-Open a v26-shaped db: %v", err)
	}
	got := schemaDump(t, s2)
	s2.Close()

	if got != want {
		t.Errorf("schema after migrating from v26 differs from a fresh database:\n--- migrated ---\n%s\n--- fresh ---\n%s", got, want)
	}
}

// TestContractJGDowngradeRestoresTheLowestPositionLink drives the new
// `target < 27` arm of downgradeFreshDB directly. It is not decoration: every
// per-version migration test in schema_test.go hands migrate() a database this
// helper produced, so an arm that restored goal_id from the WRONG link (the
// highest position, or the lowest goal id) would silently feed a wrong v26
// shape into every later test that starts below 27.
//
// The fixture is built so those three readings disagree: the first link is to
// the goal with the LARGER id, so "lowest position" and "lowest id" cannot both
// be satisfied, and there are two links, so "lowest" and "highest" cannot
// either.
func TestContractJGDowngradeRestoresTheLowestPositionLink(t *testing.T) {
	s, _ := contractJGOpen(t)
	lower := contractJGGoal(t, s, "added to the table first, linked second")
	higher := contractJGGoal(t, s, "added to the table second, linked first")
	j := contractJGJob(t, s, "downgrade-job")
	unlinked := contractJGJob(t, s, "downgrade-unlinked")

	contractJGLink(t, s, j.ID, higher.ID) // position 0 — the one goal_id must come back as
	contractJGLink(t, s, j.ID, lower.ID)  // appended after it

	downgradeFreshDB(t, s, 26)

	var got sql.NullInt64
	if err := s.db.QueryRow(`SELECT goal_id FROM jobs WHERE id = ?`, j.ID).Scan(&got); err != nil {
		t.Fatalf("read the restored goal_id: %v", err)
	}
	if !got.Valid || got.Int64 != higher.ID {
		t.Errorf("restored goal_id = %v, want %d — the LOWEST-position link, not the lowest id and not the last one added",
			got, higher.ID)
	}

	var none sql.NullInt64
	if err := s.db.QueryRow(`SELECT goal_id FROM jobs WHERE id = ?`, unlinked.ID).Scan(&none); err != nil {
		t.Fatalf("read the unlinked job's goal_id: %v", err)
	}
	if none.Valid {
		t.Errorf("an unlinked job's restored goal_id = %d, want NULL", none.Int64)
	}

	var tables int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'job_goals'`).Scan(&tables); err != nil {
		t.Fatalf("look for job_goals after the downgrade: %v", err)
	}
	if tables != 0 {
		t.Error("job_goals still exists after downgrading past v27; a v26 database has never had it")
	}
}

// ---------------------------------------------------------------------------
// AddJobGoal
// ---------------------------------------------------------------------------

// TestContractAddJobGoalAppendsInLinkOrder is the ordering contract at its
// source. "Appends at MAX(position)+1 for that job" is what makes the card's
// chips, the brief's lead groups and the wire's `goals[0]` all read in the
// order carl picked them — a fixed position, or one derived from the goal id,
// would reorder the chips under him between two picks.
func TestContractAddJobGoalAppendsInLinkOrder(t *testing.T) {
	s, _ := contractJGOpen(t)
	j := contractJGJob(t, s, "ordered-links")
	// deliberately linked in an order that is neither id-ascending nor
	// id-descending, so neither accident can pass
	second := contractJGGoal(t, s, "second in the table")
	first := contractJGGoal(t, s, "first in the table")
	third := contractJGGoal(t, s, "third in the table")

	contractJGLink(t, s, j.ID, second.ID)
	contractJGLink(t, s, j.ID, first.ID)
	contractJGLink(t, s, j.ID, third.ID)

	want := []int64{second.ID, first.ID, third.ID}
	if got := contractJGIDs(t, s, j.ID); !contractJGSameIDs(got, want) {
		t.Errorf("GoalIDs = %v, want the link order %v", got, want)
	}
	ids, positions := contractJGPositions(t, s, j.ID)
	if !contractJGSameIDs(ids, want) {
		t.Fatalf("job_goals in position order = %v, want %v", ids, want)
	}
	for i := 1; i < len(positions); i++ {
		if positions[i] <= positions[i-1] {
			t.Fatalf("positions %v are not strictly increasing with link order", positions)
		}
	}
}

// TestContractAddJobGoalPositionsAreScopedToTheJob: "MAX(position)+1 for that
// job". A single global counter still lists correctly today and makes two
// jobs' positions drift apart forever, which is invisible until something
// starts reading the number rather than the order it produces.
func TestContractAddJobGoalPositionsAreScopedToTheJob(t *testing.T) {
	s, _ := contractJGOpen(t)
	a := contractJGJob(t, s, "job-a")
	b := contractJGJob(t, s, "job-b")
	g1 := contractJGGoal(t, s, "goal one")
	g2 := contractJGGoal(t, s, "goal two")

	contractJGLink(t, s, a.ID, g1.ID)
	contractJGLink(t, s, a.ID, g2.ID)
	contractJGLink(t, s, b.ID, g2.ID)

	_, aPos := contractJGPositions(t, s, a.ID)
	_, bPos := contractJGPositions(t, s, b.ID)
	if len(aPos) != 2 || len(bPos) != 1 {
		t.Fatalf("positions a=%v b=%v, want 2 links on a and 1 on b", aPos, bPos)
	}
	if bPos[0] != aPos[0] {
		t.Errorf("job b's first link landed at position %d, job a's at %d — the counter is not per job",
			bPos[0], aPos[0])
	}
}

// TestContractAddJobGoalExistingLinkIsASilentNoOp is the idempotency the
// picker depends on: two surfaces (the editor and the steer head) can each
// post the same link, and a double-click is not an error carl has to see.
// "Does not change its position" is the load-bearing half — a re-add
// implemented as delete-then-append would move a goal to the END of the chip
// row for no reason the person clicking could explain.
func TestContractAddJobGoalExistingLinkIsASilentNoOp(t *testing.T) {
	s, _ := contractJGOpen(t)
	j := contractJGJob(t, s, "reads-once")
	a := contractJGGoal(t, s, "goal a")
	b := contractJGGoal(t, s, "goal b")
	contractJGLink(t, s, j.ID, a.ID)
	contractJGLink(t, s, j.ID, b.ID)
	_, before := contractJGPositions(t, s, j.ID)

	if err := s.AddJobGoal(j.ID, a.ID); err != nil {
		t.Fatalf("re-adding an existing link = %v, want nil (a silent no-op)", err)
	}

	want := []int64{a.ID, b.ID}
	if got := contractJGIDs(t, s, j.ID); !contractJGSameIDs(got, want) {
		t.Errorf("GoalIDs after the re-add = %v, want %v — no duplicate and no reorder", got, want)
	}
	ids, after := contractJGPositions(t, s, j.ID)
	if !contractJGSameIDs(ids, want) {
		t.Fatalf("job_goals after the re-add = %v, want %v", ids, want)
	}
	if !contractJGSameIDs(after, before) {
		t.Errorf("positions moved %v → %v; re-adding an existing link changes nothing", before, after)
	}
}

// ---------------------------------------------------------------------------
// RemoveJobGoal
// ---------------------------------------------------------------------------

// TestContractRemoveJobGoalDropsOneLinkAndRenumbersNothing covers both of
// remove's stated clauses on one fixture. The survivors keeping their POSITIONS
// (rather than being renumbered 0..N) is what makes the next add still land at
// the end: a renumber plus a MAX+1 append is self-consistent, but the two
// together are the only pair that keeps the chip order stable across an
// unlink, so the append is asserted here too.
func TestContractRemoveJobGoalDropsOneLinkAndRenumbersNothing(t *testing.T) {
	s, _ := contractJGOpen(t)
	j := contractJGJob(t, s, "remove-one")
	a := contractJGGoal(t, s, "goal a")
	b := contractJGGoal(t, s, "goal b")
	c := contractJGGoal(t, s, "goal c")
	d := contractJGGoal(t, s, "goal d")
	contractJGLink(t, s, j.ID, a.ID)
	contractJGLink(t, s, j.ID, b.ID)
	contractJGLink(t, s, j.ID, c.ID)
	_, before := contractJGPositions(t, s, j.ID)

	if err := s.RemoveJobGoal(j.ID, b.ID); err != nil {
		t.Fatalf("RemoveJobGoal: %v", err)
	}
	if got := contractJGIDs(t, s, j.ID); !contractJGSameIDs(got, []int64{a.ID, c.ID}) {
		t.Fatalf("GoalIDs after removing the middle link = %v, want [%d %d]", got, a.ID, c.ID)
	}
	ids, after := contractJGPositions(t, s, j.ID)
	if !contractJGSameIDs(ids, []int64{a.ID, c.ID}) {
		t.Fatalf("job_goals after the remove = %v, want [%d %d]", ids, a.ID, c.ID)
	}
	if after[0] != before[0] || after[1] != before[2] {
		t.Errorf("surviving positions %v, want the untouched %v (no renumber)",
			after, []int64{before[0], before[2]})
	}

	contractJGLink(t, s, j.ID, d.ID)
	if got := contractJGIDs(t, s, j.ID); !contractJGSameIDs(got, []int64{a.ID, c.ID, d.ID}) {
		t.Errorf("GoalIDs after adding again = %v, want the new link appended LAST", got)
	}
}

// TestContractRemoveJobGoalAbsentLinkIsASilentNoOp: a stale picker retrying an
// unlink it already landed, and an unlink of a goal this job never linked, are
// both nil — the same success contract DeleteGoal has.
func TestContractRemoveJobGoalAbsentLinkIsASilentNoOp(t *testing.T) {
	s, _ := contractJGOpen(t)
	j := contractJGJob(t, s, "remove-absent")
	linked := contractJGGoal(t, s, "linked")
	loose := contractJGGoal(t, s, "never linked to this job")
	contractJGLink(t, s, j.ID, linked.ID)

	if err := s.RemoveJobGoal(j.ID, loose.ID); err != nil {
		t.Errorf("removing a link that was never made = %v, want nil", err)
	}
	if err := s.RemoveJobGoal(j.ID, linked.ID); err != nil {
		t.Fatalf("RemoveJobGoal: %v", err)
	}
	if err := s.RemoveJobGoal(j.ID, linked.ID); err != nil {
		t.Errorf("removing the same link twice = %v, want nil (no-op)", err)
	}
	if got := contractJGIDs(t, s, j.ID); len(got) != 0 {
		t.Errorf("GoalIDs = %v, want empty", got)
	}
}

// ---------------------------------------------------------------------------
// ClearJobGoals
// ---------------------------------------------------------------------------

// TestContractClearJobGoalsRemovesEveryLink is the picker's `—` row: one call
// takes a job from N links to none, and calling it on a job that already has
// none is not an error.
func TestContractClearJobGoalsRemovesEveryLink(t *testing.T) {
	s, _ := contractJGOpen(t)
	j := contractJGJob(t, s, "clear-me")
	other := contractJGJob(t, s, "keeps-its-links")
	a := contractJGGoal(t, s, "goal a")
	b := contractJGGoal(t, s, "goal b")
	contractJGLink(t, s, j.ID, a.ID)
	contractJGLink(t, s, j.ID, b.ID)
	contractJGLink(t, s, other.ID, a.ID)

	if err := s.ClearJobGoals(j.ID); err != nil {
		t.Fatalf("ClearJobGoals: %v", err)
	}
	if got := contractJGIDs(t, s, j.ID); len(got) != 0 {
		t.Errorf("GoalIDs after the clear = %v, want empty", got)
	}
	if err := s.ClearJobGoals(j.ID); err != nil {
		t.Errorf("clearing a job with no links = %v, want nil (no-op)", err)
	}
	// scoped to the one job — a DELETE without the job_id predicate would
	// unlabel every card on the board from one click of one `—` row
	if got := contractJGIDs(t, s, other.ID); !contractJGSameIDs(got, []int64{a.ID}) {
		t.Errorf("another job's GoalIDs = %v after clearing THIS one, want [%d]", got, a.ID)
	}
	// and the goals themselves are untouched: a link is not the goal
	week, _ := GoalPeriods(time.Now())
	rows, err := s.ListGoals("week", week)
	if err != nil {
		t.Fatalf("ListGoals: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("ListGoals = %+v after the clear, want both goals to survive", rows)
	}
}

// ---------------------------------------------------------------------------
// rev / updated_at
// ---------------------------------------------------------------------------

// TestContractJobGoalWritesBumpNeitherRevNorUpdatedAt is SetJobGoal's old
// property carried over verbatim to all three verbs, and it is the one a
// well-meaning "…and stamp updated_at so the board refreshes" line silently
// undoes: rev is the editor's optimistic lock (a bump 409s an open form over a
// field the form never showed) and updated_at is what each card renders as its
// AGE (a bump resets how long the job has been waiting).
func TestContractJobGoalWritesBumpNeitherRevNorUpdatedAt(t *testing.T) {
	s, _ := contractJGOpen(t)
	before := contractJGJob(t, s, "rev-guard")
	a := contractJGGoal(t, s, "goal a")
	b := contractJGGoal(t, s, "goal b")

	check := func(what string) {
		t.Helper()
		after, err := s.GetJobByID(before.ID)
		if err != nil {
			t.Fatalf("GetJobByID after %s: %v", what, err)
		}
		if after.Rev != before.Rev {
			t.Errorf("%s moved rev %d → %d; a goal link is outside the rev gate", what, before.Rev, after.Rev)
		}
		if after.UpdatedAt != before.UpdatedAt {
			t.Errorf("%s moved updated_at %d → %d; a goal link must not reset the card's age",
				what, before.UpdatedAt, after.UpdatedAt)
		}
	}

	contractJGLink(t, s, before.ID, a.ID)
	check("AddJobGoal")
	contractJGLink(t, s, before.ID, b.ID)
	check("a second AddJobGoal")
	if err := s.RemoveJobGoal(before.ID, a.ID); err != nil {
		t.Fatalf("RemoveJobGoal: %v", err)
	}
	check("RemoveJobGoal")
	if err := s.ClearJobGoals(before.ID); err != nil {
		t.Fatalf("ClearJobGoals: %v", err)
	}
	check("ClearJobGoals")
}

// ---------------------------------------------------------------------------
// Job.GoalIDs — the loader fill
// ---------------------------------------------------------------------------

// TestContractJobGoalIDsAreFilledByEveryLoader is the FormerSlugs hazard
// restated: the ids come from a second table, so every read path has to attach
// them, and one that forgets serves an UNLINKED job with no error anywhere. A
// GoalIDs that only appeared on GetJobByID would show chips in the editor and
// none on the board.
func TestContractJobGoalIDsAreFilledByEveryLoader(t *testing.T) {
	s, _ := contractJGOpen(t)
	first := contractJGGoal(t, s, "first goal")
	second := contractJGGoal(t, s, "second goal")
	linked := contractJGJob(t, s, "loader-linked")
	contractJGJob(t, s, "loader-unlinked")
	contractJGLink(t, s, linked.ID, second.ID)
	contractJGLink(t, s, linked.ID, first.ID)
	want := []int64{second.ID, first.ID}

	byID, err := s.GetJobByID(linked.ID)
	if err != nil {
		t.Fatalf("GetJobByID: %v", err)
	}
	if !contractJGSameIDs(byID.GoalIDs, want) {
		t.Errorf("GetJobByID GoalIDs = %v, want %v", byID.GoalIDs, want)
	}

	bySlug, err := s.GetJobBySlug("loader-linked")
	if err != nil {
		t.Fatalf("GetJobBySlug: %v", err)
	}
	if !contractJGSameIDs(bySlug.GoalIDs, want) {
		t.Errorf("GetJobBySlug GoalIDs = %v, want %v", bySlug.GoalIDs, want)
	}

	all, err := s.ListJobs()
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	seen := map[string][]int64{}
	for _, j := range all {
		seen[j.Slug] = j.GoalIDs
	}
	if !contractJGSameIDs(seen["loader-linked"], want) {
		t.Errorf("ListJobs GoalIDs = %v, want %v", seen["loader-linked"], want)
	}
	if len(seen["loader-unlinked"]) != 0 {
		t.Errorf("ListJobs GoalIDs for an unlinked job = %v, want empty", seen["loader-unlinked"])
	}

	rows, _, err := s.LoadJobsWithChildren()
	if err != nil {
		t.Fatalf("LoadJobsWithChildren: %v", err)
	}
	found := false
	for _, j := range rows {
		switch j.Slug {
		case "loader-linked":
			found = true
			if !contractJGSameIDs(j.GoalIDs, want) {
				t.Errorf("LoadJobsWithChildren GoalIDs = %v, want %v", j.GoalIDs, want)
			}
		case "loader-unlinked":
			if len(j.GoalIDs) != 0 {
				t.Errorf("LoadJobsWithChildren GoalIDs for an unlinked job = %v, want empty", j.GoalIDs)
			}
		}
	}
	if !found {
		t.Fatal("LoadJobsWithChildren did not return the linked job at all")
	}
}

// ---------------------------------------------------------------------------
// ListJobGoalLinks
// ---------------------------------------------------------------------------

// TestContractListJobGoalLinksReturnsEveryLinkedJobInPositionOrder is the
// board's ONE query. Its second clause is the one a map built by iterating
// jobs (rather than links) gets wrong: an unlinked job must be ABSENT, never
// present with an empty slice, because the board reads presence to decide
// whether to emit the `goals` key at all and a present-but-empty entry would
// serve `"goals":[]` for every unlabelled card.
func TestContractListJobGoalLinksReturnsEveryLinkedJobInPositionOrder(t *testing.T) {
	s, _ := contractJGOpen(t)
	a := contractJGGoal(t, s, "goal a")
	b := contractJGGoal(t, s, "goal b")
	c := contractJGGoal(t, s, "goal c")
	one := contractJGJob(t, s, "links-two")
	two := contractJGJob(t, s, "links-one")
	none := contractJGJob(t, s, "links-none")

	contractJGLink(t, s, one.ID, c.ID)
	contractJGLink(t, s, one.ID, a.ID)
	contractJGLink(t, s, two.ID, b.ID)

	got, err := s.ListJobGoalLinks()
	if err != nil {
		t.Fatalf("ListJobGoalLinks: %v", err)
	}
	if !contractJGSameIDs(got[one.ID], []int64{c.ID, a.ID}) {
		t.Errorf("links for %s = %v, want [%d %d] in position order", one.Slug, got[one.ID], c.ID, a.ID)
	}
	if !contractJGSameIDs(got[two.ID], []int64{b.ID}) {
		t.Errorf("links for %s = %v, want [%d]", two.Slug, got[two.ID], b.ID)
	}
	if _, present := got[none.ID]; present {
		t.Errorf("an unlinked job is present in the map (as %v); it must be ABSENT", got[none.ID])
	}
	if len(got) != 2 {
		t.Errorf("map holds %d entries, want exactly the 2 linked jobs", len(got))
	}
}

// TestContractListJobGoalLinksIsEmptyWithNoLinks — the shape a fresh install
// hands buildWebBoard on every poll before carl has filed anything.
func TestContractListJobGoalLinksIsEmptyWithNoLinks(t *testing.T) {
	s, _ := contractJGOpen(t)
	contractJGJob(t, s, "unfiled")
	got, err := s.ListJobGoalLinks()
	if err != nil {
		t.Fatalf("ListJobGoalLinks on an empty table: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListJobGoalLinks = %v, want an empty map", got)
	}
}

// ---------------------------------------------------------------------------
// cascades and the lifecycle
// ---------------------------------------------------------------------------

// TestContractDeletingAGoalRemovesExactlyItsLinkRows is today's observable
// behavior preserved through the new table: a job linked to a goal carl then
// deletes keeps reading cleanly with one fewer goal. Under v23 this was ON
// DELETE SET NULL on a column; here it is ON DELETE CASCADE on the link row,
// and the guarantee a client sees has to be the same one.
func TestContractDeletingAGoalRemovesExactlyItsLinkRows(t *testing.T) {
	s, _ := contractJGOpen(t)
	doomed := contractJGGoal(t, s, "doomed goal")
	kept := contractJGGoal(t, s, "kept goal")
	a := contractJGJob(t, s, "cascade-a")
	b := contractJGJob(t, s, "cascade-b")
	contractJGLink(t, s, a.ID, doomed.ID)
	contractJGLink(t, s, a.ID, kept.ID)
	contractJGLink(t, s, b.ID, doomed.ID)
	_, beforeA := contractJGPositions(t, s, a.ID)

	if err := s.DeleteGoal(doomed.ID); err != nil {
		t.Fatalf("DeleteGoal: %v", err)
	}

	rowA, err := s.GetJobByID(a.ID)
	if err != nil {
		t.Fatalf("GetJobByID after deleting a linked goal: %v", err)
	}
	if !contractJGSameIDs(rowA.GoalIDs, []int64{kept.ID}) {
		t.Errorf("GoalIDs = %v after the goal was deleted, want just [%d]", rowA.GoalIDs, kept.ID)
	}
	if rowA.Slug != "cascade-a" {
		t.Errorf("job scanned as %+v; a deleted goal must not disturb the rest of the row", *rowA)
	}
	_, afterA := contractJGPositions(t, s, a.ID)
	if len(afterA) != 1 || afterA[0] != beforeA[1] {
		t.Errorf("the surviving link's position = %v, want the untouched %d", afterA, beforeA[1])
	}
	if got := contractJGIDs(t, s, b.ID); len(got) != 0 {
		t.Errorf("the other job's GoalIDs = %v, want empty", got)
	}
}

// TestContractDeletingAJobRemovesItsLinkRows is the cascade's other direction:
// the link rows have no life of their own once the job is gone, and a stranded
// one would keep counting toward the goal's linked-job count forever.
func TestContractDeletingAJobRemovesItsLinkRows(t *testing.T) {
	s, _ := contractJGOpen(t)
	g := contractJGGoal(t, s, "shared goal")
	doomed := contractJGJob(t, s, "doomed-job")
	survivor := contractJGJob(t, s, "surviving-job")
	contractJGLink(t, s, doomed.ID, g.ID)
	contractJGLink(t, s, survivor.ID, g.ID)

	if err := s.DeleteJob(doomed.ID); err != nil {
		t.Fatalf("DeleteJob: %v", err)
	}
	links, err := s.ListJobGoalLinks()
	if err != nil {
		t.Fatalf("ListJobGoalLinks: %v", err)
	}
	if _, present := links[doomed.ID]; present {
		t.Errorf("the deleted job still has links (%v)", links[doomed.ID])
	}
	if !contractJGSameIDs(links[survivor.ID], []int64{g.ID}) {
		t.Errorf("the surviving job's links = %v, want [%d]", links[survivor.ID], g.ID)
	}
	week, _ := GoalPeriods(time.Now())
	rows, err := s.ListGoals("week", week)
	if err != nil || len(rows) != 1 {
		t.Errorf("ListGoals = %+v (err %v), want the goal itself to survive a job delete", rows, err)
	}
}

// TestContractArchivingAndUnarchivingChangeNoLink: archiving is a column on
// the job row, not a namespace change, and the links ride the row's immutable
// id — so a round trip through the archive returns the same ids in the same
// order. The archived loaders are asserted here rather than in the loader test
// above because this is the only place an archived row exists.
func TestContractArchivingAndUnarchivingChangeNoLink(t *testing.T) {
	s, _ := contractJGOpen(t)
	first := contractJGGoal(t, s, "first goal")
	second := contractJGGoal(t, s, "second goal")
	j := contractJGJob(t, s, "shelved")
	contractJGLink(t, s, j.ID, second.ID)
	contractJGLink(t, s, j.ID, first.ID)
	want := []int64{second.ID, first.ID}

	if _, err := s.ArchiveJob(j.ID); err != nil {
		t.Fatalf("ArchiveJob: %v", err)
	}
	if got := contractJGIDs(t, s, j.ID); !contractJGSameIDs(got, want) {
		t.Errorf("GoalIDs after archiving = %v, want %v", got, want)
	}
	archived, err := s.ListArchivedJobs()
	if err != nil {
		t.Fatalf("ListArchivedJobs: %v", err)
	}
	if len(archived) != 1 {
		t.Fatalf("ListArchivedJobs = %d rows, want 1", len(archived))
	}
	if !contractJGSameIDs(archived[0].GoalIDs, want) {
		t.Errorf("ListArchivedJobs GoalIDs = %v, want %v — the archive drawer's cards carry chips too",
			archived[0].GoalIDs, want)
	}
	arows, _, err := s.LoadArchivedJobsWithChildren()
	if err != nil {
		t.Fatalf("LoadArchivedJobsWithChildren: %v", err)
	}
	if len(arows) != 1 || !contractJGSameIDs(arows[0].GoalIDs, want) {
		t.Errorf("LoadArchivedJobsWithChildren GoalIDs = %v, want %v", arows, want)
	}

	if err := s.UnarchiveJob(j.ID); err != nil {
		t.Fatalf("UnarchiveJob: %v", err)
	}
	if got := contractJGIDs(t, s, j.ID); !contractJGSameIDs(got, want) {
		t.Errorf("GoalIDs after unarchiving = %v, want %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// the form save cannot reach the links
// ---------------------------------------------------------------------------

// TestContractAFormSaveCannotTouchTheLinks replaces the prior round's
// `goal_id`-column pin, whose subject no longer exists. The property it was
// guarding is unchanged and still load-bearing: the editor's saveform is a FULL
// REPLACE over jobFieldColumns, so anything reachable through that allowlist is
// silently wiped by any client that predates the field. The links are now
// unreachable BY CONSTRUCTION (a separate table), and the allowlist is pinned
// alongside so a future column of the same meaning cannot creep back in.
func TestContractAFormSaveCannotTouchTheLinks(t *testing.T) {
	s, _ := contractJGOpen(t)
	for _, key := range []string{"goal_id", "goals", "job_goals", "goal_ids"} {
		if jobFieldColumns[key] {
			t.Errorf("%q is in jobFieldColumns — a full form save could then wipe a job's goal links", key)
		}
	}

	g := contractJGGoal(t, s, "the goal a save must not drop")
	j := contractJGJob(t, s, "form-save")
	contractJGLink(t, s, j.ID, g.ID)

	row, err := s.GetJobByID(j.ID)
	if err != nil {
		t.Fatalf("GetJobByID: %v", err)
	}
	if _, err := s.UpdateJobFields(row.ID, row.Rev, map[string]any{"repo": "monolith"}); err != nil {
		t.Fatalf("UpdateJobFields: %v", err)
	}
	after, err := s.GetJobByID(j.ID)
	if err != nil {
		t.Fatalf("GetJobByID after the save: %v", err)
	}
	if !contractJGSameIDs(after.GoalIDs, []int64{g.ID}) {
		t.Errorf("GoalIDs = %v after an ordinary form save, want [%d]", after.GoalIDs, g.ID)
	}
	if after.Repo != "monolith" {
		t.Errorf("the save itself did not land (repo = %q); this test proves nothing", after.Repo)
	}
}
