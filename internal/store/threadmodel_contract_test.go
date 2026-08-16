package store

import (
	"strings"
	"testing"
)

// threadmodel_contract_test.go — athena-logic's BLIND contract tests for
// 50-contract.md, Slice S1's store half: the per-thread model column
// (schema v22), validateThreadModel, and the round-trip obligations on
// store.Thread.Model.
//
// Written from the contract text alone. internal/store/threads.go and
// internal/store/schema.go were read ONLY at HEAD (for the existing helper
// and fixture patterns — openTest, seedThreadJob, the migrations map's
// shape); nothing from the working tree was observed.
//
// Dimension: LOGIC — the stated obligations and their happy paths.
// Boundaries and hostile inputs are athena-edge's ground; where an
// obligation IS an input rule (validateThreadModel's accept set is stated as
// part of its contract line) it is pinned here too, and overlap is fine.
//
// Every package-level identifier declared here is prefixed `contract` — a
// sibling author shares this package's namespace.

// contractThreadModelAliases is the four model aliases the contract names —
// the KEYS of core.ThreadModels, carrying none of its ids. It is spelled out
// here rather than imported because internal/store cannot import
// internal/core (the layering is util ← store ← core ← jobs ← api ← web), and
// the aliases are all this half of the contract is about: the store accepts
// or refuses an alias and stores it verbatim, never resolving one. The
// alias → EXACT id mapping is pinned in the jobs contract file, against
// core.ThreadModels directly.
var contractThreadModelAliases = []string{"haiku", "sonnet", "opus", "fable"}

// contractThreadColumnValue reads one column of one thread row straight out
// of SQLite, under the accessors. The migration obligation (every existing
// thread row lands on an EMPTY model) is a claim about the stored BYTES, and
// a scan through scanThread could satisfy it by COALESCEing a NULL away.
func contractThreadColumnValue(t *testing.T, s *Store, jobID int64, tid, column string) string {
	t.Helper()
	var v string
	if err := s.db.QueryRow(
		`SELECT `+column+` FROM threads WHERE job_id = ? AND tid = ?`, jobID, tid,
	).Scan(&v); err != nil {
		t.Fatalf("select %s for thread %s of job %d: %v", column, tid, jobID, err)
	}
	return v
}

// ---------------------------------------------------------------------------
// schema v22
// ---------------------------------------------------------------------------

// TestContractSchemaVersionIsV22 pins the version bump itself. A migration
// appended to the map without moving schemaVersion never runs: migrate()
// walks from the database's user_version up to schemaVersion and stops, so
// migrations[22] with schemaVersion still 21 is dead code that no fresh
// open and no upgrade ever executes — and the column would simply not exist.
//
// It used to pin schemaVersion == 22 outright, which was true only while v22
// was the newest step — v23 (the goals table) broke this test while breaking
// nothing about threads.model. Converted to the >= form recall's own version
// pin already carries (recall_contract_test.go's
// TestContractRecallSchemaVersionIs19 owns that reasoning in full): what the
// contract asks for is that the model column's migration IS step 22 and that
// the step is REACHABLE, not that nothing may ever be appended after it.
func TestContractSchemaVersionIsV22(t *testing.T) {
	if migrations[22].sql != schemaV22 {
		t.Fatalf("migrations[22] is not schemaV22 (contract: threads.model arrives at v22)")
	}
	if schemaVersion < 22 {
		t.Fatalf("schemaVersion = %d, want at least 22 (threads.model's migration must be reachable)", schemaVersion)
	}
}

// TestContractMigrationV22IsTheAdditiveAlter pins the step's exact shape:
// one additive ALTER, no Go hook. The contract states the statement
// verbatim, and the "additive" claim is what makes the upgrade story true —
// a rebuild-the-table migration (SQLite's usual ADD-a-constraint dance)
// would rewrite every existing thread row, which is precisely what the
// contract promises does not happen.
func TestContractMigrationV22IsTheAdditiveAlter(t *testing.T) {
	step, ok := migrations[22]
	if !ok {
		t.Fatal("migrations[22] is missing — schemaVersion 22 with no step would make migrate() fail on every database")
	}
	if step.hook != nil {
		t.Error("migrations[22] carries a Go hook; the contract says no hook")
	}
	const want = `ALTER TABLE threads ADD COLUMN model TEXT NOT NULL DEFAULT '';`
	if got := strings.Join(strings.Fields(step.sql), " "); got != want {
		t.Errorf("migrations[22].sql = %q, want exactly %q (whitespace-normalized)", got, want)
	}
}

// TestContractFreshStoreAcceptsTheModelColumn is the "a fresh store's schema
// accepts the column" obligation, end to end through the accessors: openTest
// migrates a brand-new database all the way to schemaVersion, so a model
// that survives AddThread → GetThread IS the assertion that v22 ran and the
// column landed on the threads table with the round-trip wired through
// threadColumns/scanThread/insertThreadTx.
func TestContractFreshStoreAcceptsTheModelColumn(t *testing.T) {
	s := openTest(t)
	jobID := seedThreadJob(t, s, "fresh-model")

	if err := s.AddThread(Thread{JobID: jobID, TID: "t1", Model: "haiku"}); err != nil {
		t.Fatalf("AddThread with a model on a fresh store: %v", err)
	}
	got, err := s.GetThread(jobID, "t1")
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if got.Model != "haiku" {
		t.Errorf("GetThread().Model = %q, want %q", got.Model, "haiku")
	}
	if raw := contractThreadColumnValue(t, s, jobID, "t1", "model"); raw != "haiku" {
		t.Errorf("stored model column = %q, want %q", raw, "haiku")
	}
}

// TestContractLegacyRowsMigrateToAnEmptyModel is the upgrade story: a
// kraken.db written before this change has thread rows with no model at all,
// and the contract's promise is that reopening it leaves every one of them
// on an EMPTY model rather than failing the migration or inventing a default.
//
// The pre-v22 shape is produced by reversing v22 on a freshly migrated
// database — dropping the one column it added and resetting user_version —
// which is exact here precisely because v22 is additive and is the only step
// past v21. Deliberately NOT routed through schema_test.go's
// downgradeFreshDB: that helper belongs to the implementer's own file, and a
// blind test asserting the contract should not depend on it having grown a
// v22 arm.
func TestContractLegacyRowsMigrateToAnEmptyModel(t *testing.T) {
	s := openTest(t)
	jobID := seedThreadJob(t, s, "legacy-model")

	// v23 (the goals table plus jobs.goal_id) landed on top of v22 after this
	// test was written, so reaching a v21 shape now means reversing it too —
	// otherwise migrate() replays step 23 over a database that still has both
	// and fails on the bare CREATE. The column goes first: it carries the
	// REFERENCES clause onto the table dropped right after it.
	//
	// v24 (kronos' orch_inbox) and v25 (saved_artifacts, the artifact library)
	// landed later still, and need the same treatment for the same reason: a
	// leftover table fails that step's bare CREATE. Reversed FIRST — this list
	// unwinds newest-first, mirroring the order migrate() applies the steps.
	for _, stmt := range []string{
		`DROP TABLE saved_artifacts`,
		`DROP TABLE orch_inbox`,
		`ALTER TABLE jobs DROP COLUMN goal_id`,
		`DROP TABLE goals`,
	} {
		if _, err := s.db.Exec(stmt); err != nil {
			t.Fatalf("reverse v25/v24/v23 (%q): %v", stmt, err)
		}
	}
	if _, err := s.db.Exec(`ALTER TABLE threads DROP COLUMN model`); err != nil {
		t.Fatalf("reverse v22 (drop threads.model): %v", err)
	}
	if _, err := s.db.Exec(`PRAGMA user_version = 21`); err != nil {
		t.Fatalf("reset user_version to 21: %v", err)
	}
	// a genuine legacy row: inserted against the pre-v22 column list, so it
	// carries no model in any form
	if _, err := s.db.Exec(
		`INSERT INTO threads (job_id, tid, title, session_id, parent_session, fork_uuid,
			cwd, pane, "window", state, seed, created_at, closed_at)
			VALUES (?, 't1', 'why the retry loop', 'sess-legacy', 'zeus-legacy', 'u9',
			'/tmp/wt', '%7', 'cell-x:1', 'live', 'ask about it', 1000, 0)`, jobID,
	); err != nil {
		t.Fatalf("seed a pre-v22 thread row: %v", err)
	}

	if err := s.migrate(); err != nil {
		t.Fatalf("migrate a v21-shaped database forward: %v", err)
	}

	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("user_version after migrate = %d, want %d", version, schemaVersion)
	}
	if raw := contractThreadColumnValue(t, s, jobID, "t1", "model"); raw != "" {
		t.Errorf("migrated legacy row's model column = %q, want %q", raw, "")
	}
	got, err := s.GetThread(jobID, "t1")
	if err != nil {
		t.Fatalf("GetThread after migrate: %v", err)
	}
	if got.Model != "" {
		t.Errorf("legacy row reads back Model = %q, want %q", got.Model, "")
	}
	// the rest of the row is untouched: an additive ALTER rewrites nothing
	if got.Title != "why the retry loop" || got.SessionID != "sess-legacy" || got.Seed != "ask about it" {
		t.Errorf("migration mangled a pre-existing row: %+v", *got)
	}
}

// ---------------------------------------------------------------------------
// validateThreadModel
// ---------------------------------------------------------------------------

// TestContractValidateThreadModelAcceptsEmptyAndTheFourAliases pins the
// accept set exactly as stated: "" (the legacy/no-choice case, which every
// existing row and every model-less branch lands on) plus the four aliases.
func TestContractValidateThreadModelAcceptsEmptyAndTheFourAliases(t *testing.T) {
	if err := validateThreadModel(""); err != nil {
		t.Errorf("validateThreadModel(%q) = %v, want nil — an empty model is the legacy case", "", err)
	}
	for _, alias := range contractThreadModelAliases {
		if err := validateThreadModel(alias); err != nil {
			t.Errorf("validateThreadModel(%q) = %v, want nil", alias, err)
		}
	}
}

// TestContractValidateThreadModelRefusesAnythingElseByName covers the other
// half of that line: the refusal must NAME the four aliases. This error is
// the text a client sees (normalizeThread hands it straight up through
// AddThread/AddThreadNext), so "bad model" alone would leave carl guessing
// which four words are legal.
func TestContractValidateThreadModelRefusesAnythingElseByName(t *testing.T) {
	// an exact model id is the interesting refusal: it is what a caller who
	// read core.ThreadModels' VALUES would try, and the column stores the
	// alias, never the id
	for _, bad := range []string{"gpt-5", "claude-opus-5", "Haiku", " haiku", "haiku "} {
		err := validateThreadModel(bad)
		if err == nil {
			t.Errorf("validateThreadModel(%q) = nil, want a refusal", bad)
			continue
		}
		for _, alias := range contractThreadModelAliases {
			if !strings.Contains(err.Error(), alias) {
				t.Errorf("validateThreadModel(%q) = %q, want it to name %q", bad, err, alias)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// round trip
// ---------------------------------------------------------------------------

// TestContractAddThreadNextStoresAndReturnsTheModel is the creation path's
// obligation: AddThreadNext is what jobs.BranchThread calls, so the model
// carl picked has to be on the row it hands back AND in the database — a
// returned-but-unstored model would look right at the branch and be gone by
// the next board poll.
func TestContractAddThreadNextStoresAndReturnsTheModel(t *testing.T) {
	s := openTest(t)
	jobID := seedThreadJob(t, s, "next-model")

	got, err := s.AddThreadNext(Thread{JobID: jobID, Title: "why blocked", SessionID: "s1", Model: "fable"})
	if err != nil {
		t.Fatalf("AddThreadNext: %v", err)
	}
	if got.Model != "fable" {
		t.Errorf("AddThreadNext returned Model = %q, want %q", got.Model, "fable")
	}
	stored, err := s.GetThread(jobID, got.TID)
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if stored.Model != "fable" {
		t.Errorf("stored Model = %q, want %q", stored.Model, "fable")
	}
	list, err := s.ListThreads(jobID)
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(list) != 1 || list[0].Model != "fable" {
		t.Errorf("ListThreads = %+v, want one row carrying model %q", list, "fable")
	}
}

// TestContractThreadModelRoundTripsForEveryAlias walks all four through the
// full add → list → get path. One alias working proves the column is wired;
// all four proves nothing along the way narrows the set (a stored enum, a
// mapping applied on the way in) — the store's job is to keep the alias
// verbatim, and the id resolution happens at spawn time in jobs.
func TestContractThreadModelRoundTripsForEveryAlias(t *testing.T) {
	s := openTest(t)
	jobID := seedThreadJob(t, s, "all-aliases")

	for _, alias := range contractThreadModelAliases {
		row, err := s.AddThreadNext(Thread{JobID: jobID, Title: alias, SessionID: "s-" + alias, Model: alias})
		if err != nil {
			t.Fatalf("AddThreadNext(model %q): %v", alias, err)
		}
		got, err := s.GetThread(jobID, row.TID)
		if err != nil {
			t.Fatalf("GetThread(%s): %v", row.TID, err)
		}
		if got.Model != alias {
			t.Errorf("thread %s: Model = %q, want %q verbatim", row.TID, got.Model, alias)
		}
	}
}

// TestContractAddThreadDefaultsModelToEmpty: a row inserted with no model —
// every legacy caller, every `k b`-shaped path, and the API's
// empty-model-is-allowed branch case — must read back "" rather than a
// stamped default. The four aliases are a CHOICE; absence is its own answer,
// and jobs' resolution (j.Model, then core.DefaultModel) is what fills it in
// at spawn time.
func TestContractAddThreadDefaultsModelToEmpty(t *testing.T) {
	s := openTest(t)
	jobID := seedThreadJob(t, s, "no-model")

	if err := s.AddThread(Thread{JobID: jobID, TID: "t1"}); err != nil {
		t.Fatalf("AddThread: %v", err)
	}
	got, err := s.GetThread(jobID, "t1")
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if got.Model != "" {
		t.Errorf("Model = %q for a row inserted without one, want %q", got.Model, "")
	}
}

// TestContractInsertRefusesAnInvalidModelAndInsertsNothing pins
// normalizeThread's added validation on BOTH insert paths, and the
// "inserts nothing" half specifically: AddThreadNext allocates its tid
// inside the transaction, so a validation failure that rolled back late
// would still have burned a tid — and a tid names a forked session.
func TestContractInsertRefusesAnInvalidModelAndInsertsNothing(t *testing.T) {
	s := openTest(t)
	jobID := seedThreadJob(t, s, "bad-model")

	err := s.AddThread(Thread{JobID: jobID, TID: "t1", Model: "gpt-5"})
	if err == nil {
		t.Fatal("AddThread accepted an invalid model")
	}
	for _, alias := range contractThreadModelAliases {
		if !strings.Contains(err.Error(), alias) {
			t.Errorf("AddThread refusal = %q, want validateThreadModel's error naming %q", err, alias)
		}
	}

	if _, err := s.AddThreadNext(Thread{JobID: jobID, Title: "x", Model: "gpt-5"}); err == nil {
		t.Fatal("AddThreadNext accepted an invalid model")
	}

	list, err := s.ListThreads(jobID)
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("a refused insert left %d rows behind: %+v", len(list), list)
	}
	// and the tid was never consumed: the next thread is still t1
	next, err := s.NextThreadID(jobID)
	if err != nil {
		t.Fatalf("NextThreadID: %v", err)
	}
	if next != "t1" {
		t.Errorf("NextThreadID = %q after two refused inserts, want t1", next)
	}
}

// TestContractCloseReviveCycleLeavesTheModelUntouched is the obligation
// revive rests on. jobs.ReviveThread resolves the model off the STORED row,
// so any verb between the branch and the revive that clears `model` would
// silently drop a haiku thread back onto the default tier — and nothing on
// the board would show it. SetThreadTitle and SetThreadPane ride along
// because they are the two other writers a live thread sees.
func TestContractCloseReviveCycleLeavesTheModelUntouched(t *testing.T) {
	s := openTest(t)
	jobID := seedThreadJob(t, s, "cycle-model")

	row, err := s.AddThreadNext(Thread{JobID: jobID, Title: "why blocked", SessionID: "s1", Model: "haiku"})
	if err != nil {
		t.Fatalf("AddThreadNext: %v", err)
	}
	steps := []struct {
		name string
		run  func() error
	}{
		{"SetThreadTitle", func() error { return s.SetThreadTitle(jobID, row.TID, "renamed") }},
		{"SetThreadPane", func() error { return s.SetThreadPane(jobID, row.TID, "%7", "cell-x:1") }},
		{"CloseThread", func() error { return s.CloseThread(jobID, row.TID, 0) }},
		{"ClaimClosedThread", func() error { return s.ClaimClosedThread(jobID, row.TID) }},
	}
	for _, step := range steps {
		if err := step.run(); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		got, gerr := s.GetThread(jobID, row.TID)
		if gerr != nil {
			t.Fatalf("GetThread after %s: %v", step.name, gerr)
		}
		if got.Model != "haiku" {
			t.Fatalf("%s changed the model: %q, want %q", step.name, got.Model, "haiku")
		}
	}
	// the whole point, stated plainly: after a full close/revive cycle the
	// thread still knows which tier it was branched on
	if raw := contractThreadColumnValue(t, s, jobID, row.TID, "model"); raw != "haiku" {
		t.Errorf("model column after a close/revive cycle = %q, want %q", raw, "haiku")
	}
}
