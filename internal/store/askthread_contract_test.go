package store

// askthread_contract_test.go — athena-logic's BLIND contract tests for the
// ask-threads port (kraken-core PR #5), Slice A (store): schema v36, the six
// new thread fields, ClaimThreadAnswering / FinishThreadAnswering /
// ClearStaleThreadAnswering / CountLiveThreads, and the CloseThread /
// ClaimClosedThread side effects on the new columns.
//
// Authored from artifacts/50-contract.md alone; only pre-existing TEST
// helpers (openTest, seedThreadJob, downgradeFreshDB, schemaDump) are used.
//
// LOGIC dimension: stated obligations and happy paths. Every test is
// TestAskContract…; every package-level identifier is prefixed askContract.

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

const (
	askContractZeusSession   = "b4a2e1f0-0000-4000-8000-000000000001"
	askContractThreadSession = "7c1e9d44-0000-4000-8000-000000000003"
	askContractQuestion      = "why is CI red on the rollout PR?"
)

// askContractOpen opens a temp store under an isolated HOME (the contract's
// environment isolation obligation).
func askContractOpen(t *testing.T) *Store {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	return openTest(t)
}

// askContractRunningExample is the contract's running example row: the ask
// thread nSTUDIO opened on the owner's tech-comms-rollout job.
func askContractRunningExample(jobID int64) Thread {
	return Thread{
		JobID:         jobID,
		Title:         "why is CI red",
		SessionID:     askContractThreadSession,
		ParentSession: askContractZeusSession,
		State:         "live",
		Seed:          askContractQuestion,
		Model:         "opus",
		Kind:          "headless",
		Role:          "ask",
		OriginNode:    "nSTUDIO",
		OriginLabel:   "studio-mini",
		OriginLogin:   "jess@probook.ai",
		Answering:     true,
	}
}

// askContractAddAsk reserves a headless row through AddThreadNext and
// returns it as stored.
func askContractAddAsk(t *testing.T, s *Store, jobID int64) Thread {
	t.Helper()
	row, err := s.AddThreadNext(askContractRunningExample(jobID))
	if err != nil {
		t.Fatalf("AddThreadNext(headless row): %v", err)
	}
	return row
}

func askContractGet(t *testing.T, s *Store, jobID int64, tid string) *Thread {
	t.Helper()
	got, err := s.GetThread(jobID, tid)
	if err != nil {
		t.Fatalf("GetThread(%d, %s): %v", jobID, tid, err)
	}
	return got
}

// askContractV35DB builds a database aged to v35 with downgradeFreshDB,
// holding one job and two pane threads (one a mind thread with a turn uuid,
// one closed) written in the v35 column set, and returns its path closed.
func askContractV35DB(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "kraken.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	downgradeFreshDB(t, s, 35)
	if _, err := s.db.Exec(`INSERT INTO jobs (id, slug) VALUES (1, 'tech-comms-rollout')`); err != nil {
		t.Fatalf("seed v35 job: %v", err)
	}
	if _, err := s.db.Exec(`INSERT INTO threads (job_id, tid, title, session_id, parent_session, pane, "window", state, seed, role, turn_uuid, created_at)
		VALUES (1, 't1', 'legacy mind thread', 'sess-legacy', ?, '%7', '@3', 'live', 'old seed', 'mind', 'u-turn-1', 1000),
		       (1, 't2', 'closed legacy', 'sess-closed', ?, '', '', 'closed', '', 'ask', '', 2000)`,
		askContractZeusSession, askContractZeusSession); err != nil {
		t.Fatalf("seed v35 threads: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close v35 db: %v", err)
	}
	return path
}

// ---------------------------------------------------------------------------
// schema v36
// ---------------------------------------------------------------------------

// Contract A `schemaVersion`: "a fresh database reports PRAGMA user_version = 36".
func TestAskContractFreshDatabaseIsV36(t *testing.T) {
	if schemaVersion != 36 {
		t.Errorf("schemaVersion = %d, want 36", schemaVersion)
	}
	s := askContractOpen(t)
	var v int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != 36 {
		t.Errorf("fresh user_version = %d, want 36", v)
	}
}

// Contract A `schemaVersion`: "a database at v35 migrates to 36 keeping every
// thread row, which then reads Kind == "pane", empty origin fields,
// Answering == false, Note == "", and its existing Role/TurnUUID untouched".
func TestAskContractV35MigratesKeepingRowsAsPane(t *testing.T) {
	s, err := Open(askContractV35DB(t))
	if err != nil {
		t.Fatalf("Open a v35 database: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	var v int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != 36 {
		t.Errorf("migrated user_version = %d, want 36", v)
	}
	list, err := s.ListThreads(1)
	if err != nil {
		t.Fatalf("ListThreads after migration: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("ListThreads after migration = %+v, want both v35 rows kept", list)
	}
	for _, row := range list {
		if row.Kind != "pane" || row.OriginNode != "" || row.OriginLabel != "" || row.OriginLogin != "" ||
			row.Answering || row.Note != "" {
			t.Errorf("migrated %s = %+v, want kind pane, empty origin, not answering, no note", row.TID, row)
		}
	}
	t1 := askContractGet(t, s, 1, "t1")
	if t1.Title != "legacy mind thread" || t1.SessionID != "sess-legacy" || t1.Pane != "%7" || t1.Window != "@3" ||
		t1.State != "live" || t1.Seed != "old seed" || t1.CreatedAt != 1000 {
		t.Errorf("migrated t1 lost a v35 column: %+v", t1)
	}
	if t1.Role != "mind" || t1.TurnUUID != "u-turn-1" {
		t.Errorf("migrated t1 role/turn uuid = %q/%q, want mind/u-turn-1 untouched", t1.Role, t1.TurnUUID)
	}
	t2 := askContractGet(t, s, 1, "t2")
	if t2.State != "closed" || t2.SessionID != "sess-closed" || t2.Role != "ask" {
		t.Errorf("migrated t2 = %+v, want the closed v35 ask-role row", t2)
	}
}

// Contract A `migrations[36]`: the migrated v35 database ends at the same
// shape as a fresh one (the ALTERs carry exactly the stated declarations).
func TestAskContractFreshAndMigratedSchemasMatch(t *testing.T) {
	fresh := askContractOpen(t)
	migrated, err := Open(askContractV35DB(t))
	if err != nil {
		t.Fatalf("Open a v35 database: %v", err)
	}
	t.Cleanup(func() { migrated.Close() })
	if f, m := schemaDump(t, fresh), schemaDump(t, migrated); f != m {
		t.Errorf("fresh and migrated schemas differ:\n--- fresh ---\n%s\n--- migrated ---\n%s", f, m)
	}
}

// Contract A `migrations[36]`: "each ALTER guarded by a pragma_table_info
// probe so the step replays as a no-op" — a fresh v36 database whose
// user_version alone is reset to 35 reopens to 36 with no error.
func TestAskContractV36StepReplaysAsNoOp(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "kraken.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := s.db.Exec(`PRAGMA user_version = 35`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("re-Open with the six columns already present at user_version 35: %v", err)
	}
	t.Cleanup(func() { s2.Close() })
	var v int
	if err := s2.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != 36 {
		t.Errorf("replayed user_version = %d, want 36", v)
	}
}

// Contract A `migrations[36]`: "Opening a v36 database twice is idempotent
// (user_version stays 36, no error)".
func TestAskContractReopenIsIdempotent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "kraken.db")
	for i := 0; i < 2; i++ {
		s, err := Open(path)
		if err != nil {
			t.Fatalf("Open #%d: %v", i+1, err)
		}
		var v int
		if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
			t.Fatal(err)
		}
		s.Close()
		if v != 36 {
			t.Errorf("Open #%d user_version = %d, want 36", i+1, v)
		}
	}
}

// Contract A `migrations[36]`: the six columns with their stated defaults —
// a row inserted with none of them reads back kind pane, empty origins,
// answering 0, an empty note.
func TestAskContractNewColumnsDefault(t *testing.T) {
	s := askContractOpen(t)
	jobID := seedThreadJob(t, s, "col-defaults")
	if _, err := s.db.Exec(`INSERT INTO threads (job_id, tid) VALUES (?, 't1')`, jobID); err != nil {
		t.Fatalf("raw insert relying on the defaults: %v", err)
	}
	var kind, node, label, login, note string
	var answering int
	if err := s.db.QueryRow(
		`SELECT kind, origin_node, origin_label, origin_login, answering, note FROM threads WHERE job_id = ? AND tid = 't1'`, jobID,
	).Scan(&kind, &node, &label, &login, &answering, &note); err != nil {
		t.Fatalf("read the six v36 columns: %v", err)
	}
	if kind != "pane" || node != "" || label != "" || login != "" || answering != 0 || note != "" {
		t.Errorf("defaults = kind %q node %q label %q login %q answering %d note %q", kind, node, label, login, answering, note)
	}
}

// ---------------------------------------------------------------------------
// the six fields round trip
// ---------------------------------------------------------------------------

// Contract A `AddThread, AddThreadNext` / `ListThreads, GetThread,
// GetThreadBySession`: "store the six new fields" and "return the six new
// fields"; AddThreadNext "hands back the row as stored".
func TestAskContractHeadlessRowRoundTripsTheSixFields(t *testing.T) {
	s := askContractOpen(t)
	jobID := seedThreadJob(t, s, "tech-comms-rollout")
	want := askContractRunningExample(jobID)
	row := askContractAddAsk(t, s, jobID)
	if row.TID == "" {
		t.Fatal("AddThreadNext returned no tid")
	}

	check := func(label string, got Thread) {
		t.Helper()
		if got.Kind != "headless" || got.Role != "ask" || got.OriginNode != want.OriginNode || got.OriginLabel != want.OriginLabel ||
			got.OriginLogin != want.OriginLogin || !got.Answering || got.Note != "" {
			t.Errorf("%s = %+v, want kind headless, role ask, origin nSTUDIO/studio-mini/jess@probook.ai, answering, no note", label, got)
		}
		if got.Pane != "" || got.Window != "" {
			t.Errorf("%s carries a pane/window: %+v", label, got)
		}
		if got.SessionID != want.SessionID || got.ParentSession != want.ParentSession || got.Seed != want.Seed ||
			got.Model != want.Model || got.Title != want.Title || got.State != "live" {
			t.Errorf("%s lost a pre-existing field: %+v", label, got)
		}
	}
	check("AddThreadNext's return", row)
	check("GetThread", *askContractGet(t, s, jobID, row.TID))
	list, err := s.ListThreads(jobID)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListThreads = %+v, %v; want one row", list, err)
	}
	check("ListThreads[0]", list[0])
	bySess, err := s.GetThreadBySession(want.SessionID)
	if err != nil || bySess == nil {
		t.Fatalf("GetThreadBySession: %+v, %v", bySess, err)
	}
	check("GetThreadBySession", *bySess)
}

// Contract A `AddThread`: stores the six new fields (the explicit-tid writer,
// including a non-empty note).
func TestAskContractAddThreadStoresTheSixFields(t *testing.T) {
	s := askContractOpen(t)
	jobID := seedThreadJob(t, s, "tech-comms-rollout")
	want := askContractRunningExample(jobID)
	want.TID = "t3"
	want.Answering = false
	want.Note = "ask cancelled"
	if err := s.AddThread(want); err != nil {
		t.Fatalf("AddThread(headless): %v", err)
	}
	got := askContractGet(t, s, jobID, "t3")
	if got.Kind != "headless" || got.OriginNode != "nSTUDIO" || got.OriginLabel != "studio-mini" ||
		got.OriginLogin != "jess@probook.ai" || got.Answering || got.Note != "ask cancelled" || got.Role != "ask" {
		t.Errorf("AddThread round trip = %+v", got)
	}
}

// Contract A `AddThreadNext`: "Kind "" is stored as "pane""; "Role "" still
// stored as "ask""; `Thread`: "Kind is "pane" or "headless" on every row read
// back"; AddThreadNext hands back the row with Kind filled.
func TestAskContractEmptyKindIsStoredAsPane(t *testing.T) {
	s := askContractOpen(t)
	jobID := seedThreadJob(t, s, "empty-kind")
	row, err := s.AddThreadNext(Thread{JobID: jobID, Title: "pane thread", SessionID: "s1", State: "live", Pane: "%4", Window: "@2"})
	if err != nil {
		t.Fatalf("AddThreadNext with no kind: %v", err)
	}
	if row.Kind != "pane" {
		t.Errorf("AddThreadNext returned Kind %q, want pane (the row as stored)", row.Kind)
	}
	got := askContractGet(t, s, jobID, row.TID)
	if got.Kind != "pane" || got.Role != "ask" {
		t.Errorf("Kind/Role = %q/%q, want pane/ask", got.Kind, got.Role)
	}
	var raw string
	if err := s.db.QueryRow(`SELECT kind FROM threads WHERE job_id = ? AND tid = ?`, jobID, row.TID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw != "pane" {
		t.Errorf("stored kind = %q, want pane", raw)
	}
	if got.Pane != "%4" || got.Window != "@2" {
		t.Errorf("a pane row lost its pane: %+v", got)
	}

	if err := s.AddThread(Thread{JobID: jobID, TID: "t9", SessionID: "s9", State: "live"}); err != nil {
		t.Fatalf("AddThread with no kind: %v", err)
	}
	if got := askContractGet(t, s, jobID, "t9"); got.Kind != "pane" {
		t.Errorf("AddThread with no kind stored Kind %q, want pane", got.Kind)
	}
}

// Contract A `Thread`: a pane MIND thread is a legal row — kind and role are
// orthogonal; the explicit "pane" kind round-trips with role mind.
func TestAskContractPaneMindRowIsAccepted(t *testing.T) {
	s := askContractOpen(t)
	jobID := seedThreadJob(t, s, "pane-mind")
	row, err := s.AddThreadNext(Thread{JobID: jobID, Title: "mind", SessionID: "sm", State: "live", Kind: "pane", Role: "mind",
		TurnUUID: "u-turn-9", Pane: "%5", Window: "@5"})
	if err != nil {
		t.Fatalf("AddThreadNext(pane, mind): %v", err)
	}
	got := askContractGet(t, s, jobID, row.TID)
	if got.Kind != "pane" || got.Role != "mind" || got.TurnUUID != "u-turn-9" {
		t.Errorf("pane mind row = %+v, want kind pane, role mind, turn uuid kept", got)
	}
}

// Contract A `ErrThreadKind` / `ErrAskThreadPane` / `ErrThreadAnswering` /
// `ErrThreadClosed`: the four sentinels exist with their stated texts.
func TestAskContractSentinelTexts(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{ErrThreadKind, "store: thread kind must be pane or headless"},
		{ErrAskThreadPane, "store: an ask thread holds no pane"},
		{ErrThreadAnswering, "store: thread is already answering"},
		{ErrThreadClosed, "store: thread is closed"},
	} {
		if c.err == nil || c.err.Error() != c.want {
			t.Errorf("sentinel = %v, want %q", c.err, c.want)
		}
	}
}

// Contract A `ErrThreadKind`: "ALSO returned (wrapped) by a write whose Kind
// is "headless" and Role is "mind" — the mind thread must be a pane. Nothing
// is inserted".
func TestAskContractHeadlessMindIsRefused(t *testing.T) {
	s := askContractOpen(t)
	jobID := seedThreadJob(t, s, "headless-mind")
	r := askContractRunningExample(jobID)
	r.Role = "mind"
	if _, err := s.AddThreadNext(r); !errors.Is(err, ErrThreadKind) {
		t.Errorf("AddThreadNext(headless, mind) = %v, want ErrThreadKind", err)
	}
	r.TID = "t1"
	if err := s.AddThread(r); !errors.Is(err, ErrThreadKind) {
		t.Errorf("AddThread(headless, mind) = %v, want ErrThreadKind", err)
	}
	if list, err := s.ListThreads(jobID); err != nil || len(list) != 0 {
		t.Errorf("rows after the refusals = %+v, %v; want none", list, err)
	}
}

// ---------------------------------------------------------------------------
// ClaimThreadAnswering / FinishThreadAnswering / ClearStaleThreadAnswering
// ---------------------------------------------------------------------------

// Contract A `ClaimThreadAnswering`: "a live row with answering = 0 becomes
// answering = 1, note = <empty> and nil is returned; a live row with answering = 1
// returns ErrThreadAnswering and changes nothing"; the sentinel comes back BARE.
func TestAskContractClaimFlipsAnsweringAndClearsNote(t *testing.T) {
	s := askContractOpen(t)
	jobID := seedThreadJob(t, s, "claim")
	row := askContractAddAsk(t, s, jobID)
	if err := s.FinishThreadAnswering(jobID, row.TID, "ask failed: exit status 1"); err != nil {
		t.Fatalf("FinishThreadAnswering: %v", err)
	}
	got := askContractGet(t, s, jobID, row.TID)
	if got.Answering || got.Note != "ask failed: exit status 1" {
		t.Fatalf("after finish = %+v, want not answering with the note", got)
	}

	if err := s.ClaimThreadAnswering(jobID, row.TID); err != nil {
		t.Fatalf("ClaimThreadAnswering on an idle live row: %v", err)
	}
	got = askContractGet(t, s, jobID, row.TID)
	if !got.Answering || got.Note != "" || got.State != "live" {
		t.Errorf("after claim = %+v, want answering, note cleared, still live", got)
	}

	err := s.ClaimThreadAnswering(jobID, row.TID)
	if err != ErrThreadAnswering {
		t.Fatalf("second claim = %v, want ErrThreadAnswering bare", err)
	}
	again := askContractGet(t, s, jobID, row.TID)
	if *again != *got {
		t.Errorf("a refused claim changed the row:\nbefore %+v\nafter  %+v", got, again)
	}
}

// Contract A `ClaimThreadAnswering`: "a closed row returns ErrThreadClosed; an
// unknown tid returns ErrNotFound; the three sentinels come back BARE".
func TestAskContractClaimOnClosedAndUnknown(t *testing.T) {
	s := askContractOpen(t)
	jobID := seedThreadJob(t, s, "claim-closed")
	row := askContractAddAsk(t, s, jobID)
	if err := s.CloseThread(jobID, row.TID, 5000); err != nil {
		t.Fatal(err)
	}
	if err := s.ClaimThreadAnswering(jobID, row.TID); err != ErrThreadClosed {
		t.Errorf("claim on a closed row = %v, want ErrThreadClosed bare", err)
	}
	if got := askContractGet(t, s, jobID, row.TID); got.Answering || got.State != "closed" {
		t.Errorf("a refused claim on a closed row changed it: %+v", got)
	}
	if err := s.ClaimThreadAnswering(jobID, "t99"); err != ErrNotFound {
		t.Errorf("claim on an unknown tid = %v, want ErrNotFound bare", err)
	}
}

// Contract A `ClaimThreadAnswering`: "two concurrent claims on one row:
// exactly one succeeds".
func TestAskContractConcurrentClaimsExactlyOneWins(t *testing.T) {
	s := askContractOpen(t)
	jobID := seedThreadJob(t, s, "claim-race")
	row := askContractAddAsk(t, s, jobID)
	if err := s.FinishThreadAnswering(jobID, row.TID, ""); err != nil {
		t.Fatal(err)
	}
	const n = 8
	errs := make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = s.ClaimThreadAnswering(jobID, row.TID)
		}(i)
	}
	close(start)
	wg.Wait()
	wins := 0
	for i, err := range errs {
		switch {
		case err == nil:
			wins++
		case err == ErrThreadAnswering:
		default:
			t.Errorf("claim %d = %v, want nil or ErrThreadAnswering", i, err)
		}
	}
	if wins != 1 {
		t.Errorf("%d of %d concurrent claims won, want exactly 1", wins, n)
	}
}

// Contract A `FinishThreadAnswering`: "sets answering = 0 and note = note (an
// empty note clears it) on the row whatever its state (live or closed);
// unknown tid returns ErrNotFound (bare)".
func TestAskContractFinishSetsNoteWhateverTheState(t *testing.T) {
	s := askContractOpen(t)
	jobID := seedThreadJob(t, s, "finish")
	row := askContractAddAsk(t, s, jobID) // answering = 1

	if err := s.FinishThreadAnswering(jobID, row.TID, "ask timed out after 5m0s"); err != nil {
		t.Fatalf("FinishThreadAnswering: %v", err)
	}
	if got := askContractGet(t, s, jobID, row.TID); got.Answering || got.Note != "ask timed out after 5m0s" || got.State != "live" {
		t.Errorf("after finish = %+v", got)
	}
	if err := s.FinishThreadAnswering(jobID, row.TID, ""); err != nil {
		t.Fatal(err)
	}
	if got := askContractGet(t, s, jobID, row.TID); got.Note != "" {
		t.Errorf("an empty note did not clear: %+v", got)
	}

	if err := s.CloseThread(jobID, row.TID, 9000); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishThreadAnswering(jobID, row.TID, "ask cancelled"); err != nil {
		t.Fatalf("FinishThreadAnswering on a closed row: %v", err)
	}
	if got := askContractGet(t, s, jobID, row.TID); got.Answering || got.Note != "ask cancelled" || got.State != "closed" {
		t.Errorf("finish on a closed row = %+v, want note set and still closed", got)
	}

	if err := s.FinishThreadAnswering(jobID, "t99", ""); err != ErrNotFound {
		t.Errorf("finish on an unknown tid = %v, want ErrNotFound bare", err)
	}
}

// Contract A `ClearStaleThreadAnswering`: "writes answering = 0, note = note
// ONLY where answering = 1 holds at write time and reports whether it wrote;
// a row not answering, or an unknown tid, writes nothing and returns
// (false, nil)".
func TestAskContractClearStaleWritesOnlyWhileAnswering(t *testing.T) {
	s := askContractOpen(t)
	jobID := seedThreadJob(t, s, "clear-stale")
	row := askContractAddAsk(t, s, jobID) // answering = 1

	wrote, err := s.ClearStaleThreadAnswering(jobID, row.TID, "answer interrupted — kraken serve restarted")
	if err != nil || !wrote {
		t.Fatalf("ClearStale on an answering row = (%v, %v), want (true, nil)", wrote, err)
	}
	got := askContractGet(t, s, jobID, row.TID)
	if got.Answering || got.Note != "answer interrupted — kraken serve restarted" {
		t.Errorf("after ClearStale = %+v, want not answering with the note", got)
	}

	// a run that finished first keeps its own note
	if err := s.FinishThreadAnswering(jobID, row.TID, "ask failed: boom"); err != nil {
		t.Fatal(err)
	}
	wrote, err = s.ClearStaleThreadAnswering(jobID, row.TID, "answer interrupted — kraken serve restarted")
	if err != nil || wrote {
		t.Errorf("ClearStale on an idle row = (%v, %v), want (false, nil)", wrote, err)
	}
	if got := askContractGet(t, s, jobID, row.TID); got.Note != "ask failed: boom" || got.Answering {
		t.Errorf("ClearStale on an idle row changed it: %+v", got)
	}

	wrote, err = s.ClearStaleThreadAnswering(jobID, "t99", "x")
	if err != nil || wrote {
		t.Errorf("ClearStale on an unknown tid = (%v, %v), want (false, nil)", wrote, err)
	}
}

// ---------------------------------------------------------------------------
// CloseThread / ClaimClosedThread on the new columns
// ---------------------------------------------------------------------------

// Contract A `CloseThread`: "additionally sets answering = 0; note is left as
// is"; `ClaimClosedThread`: "note, kind and the origin fields are left as
// they were".
func TestAskContractCloseAndReopenKeepNoteKindAndOrigin(t *testing.T) {
	s := askContractOpen(t)
	jobID := seedThreadJob(t, s, "close-reopen")
	row := askContractAddAsk(t, s, jobID)
	// The accessors never leave a row answering AND noted (a claim clears the
	// note), so the note is written through the raw column on an answering
	// row: close must clear answering and leave that note alone.
	if _, err := s.db.Exec(`UPDATE threads SET note = 'kept note' WHERE job_id = ? AND tid = ?`, jobID, row.TID); err != nil {
		t.Fatal(err)
	}

	if err := s.CloseThread(jobID, row.TID, 7000); err != nil {
		t.Fatalf("CloseThread: %v", err)
	}
	closed := askContractGet(t, s, jobID, row.TID)
	if closed.State != "closed" || closed.Answering || closed.Note != "kept note" {
		t.Errorf("after close = %+v, want closed, answering cleared, note kept", closed)
	}

	if err := s.ClaimClosedThread(jobID, row.TID); err != nil {
		t.Fatalf("ClaimClosedThread: %v", err)
	}
	reopened := askContractGet(t, s, jobID, row.TID)
	if reopened.State != "live" || reopened.ClosedAt != 0 || reopened.Pane != "" || reopened.Window != "" {
		t.Errorf("reopened = %+v, want live with no pane and closed_at cleared", reopened)
	}
	if reopened.Note != "kept note" || reopened.Kind != "headless" || reopened.OriginNode != "nSTUDIO" ||
		reopened.OriginLabel != "studio-mini" || reopened.OriginLogin != "jess@probook.ai" || reopened.Role != "ask" {
		t.Errorf("reopen changed note/kind/origin/role: %+v", reopened)
	}
}

// Contract A `CloseThread`: "pane/window still cleared" on a pane row, whose
// kind stays pane.
func TestAskContractClosePaneRowStillClearsPane(t *testing.T) {
	s := askContractOpen(t)
	jobID := seedThreadJob(t, s, "close-pane")
	row, err := s.AddThreadNext(Thread{JobID: jobID, Title: "pane", SessionID: "sp", State: "live", Pane: "%2", Window: "@2"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CloseThread(jobID, row.TID, 100); err != nil {
		t.Fatal(err)
	}
	got := askContractGet(t, s, jobID, row.TID)
	if got.State != "closed" || got.Pane != "" || got.Window != "" || got.Kind != "pane" || got.Answering {
		t.Errorf("closed pane row = %+v, want closed, pane/window cleared, kind pane", got)
	}
}

// ---------------------------------------------------------------------------
// CountLiveThreads
// ---------------------------------------------------------------------------

// Contract A `CountLiveThreads`: "the number of rows of job jobID with
// state = 'live' and kind = kind; 0 for a job with none; a pane row never
// counts under "headless" and vice versa".
func TestAskContractCountLiveThreadsByKind(t *testing.T) {
	s := askContractOpen(t)
	jobID := seedThreadJob(t, s, "count")
	other := seedThreadJob(t, s, "count-other")

	if n, err := s.CountLiveThreads(jobID, "headless"); err != nil || n != 0 {
		t.Errorf("CountLiveThreads on a job with none = %d, %v; want 0", n, err)
	}

	a1 := askContractAddAsk(t, s, jobID)
	askContractAddAsk(t, s, jobID)
	a3 := askContractAddAsk(t, s, jobID)
	if err := s.CloseThread(jobID, a3.TID, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddThreadNext(Thread{JobID: jobID, Title: "pane", SessionID: "sp", State: "live", Pane: "%1", Window: "@1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddThreadNext(Thread{JobID: jobID, Title: "mind", SessionID: "sm", State: "live", Role: "mind", Pane: "%3", Window: "@3"}); err != nil {
		t.Fatal(err)
	}
	askContractAddAsk(t, s, other)

	if n, err := s.CountLiveThreads(jobID, "headless"); err != nil || n != 2 {
		t.Errorf("CountLiveThreads(headless) = %d, %v; want 2 (one headless row closed, the other job's not counted)", n, err)
	}
	if n, err := s.CountLiveThreads(jobID, "pane"); err != nil || n != 2 {
		t.Errorf("CountLiveThreads(pane) = %d, %v; want 2 (both roles)", n, err)
	}
	if err := s.CloseThread(jobID, a1.TID, 2); err != nil {
		t.Fatal(err)
	}
	if n, err := s.CountLiveThreads(jobID, "headless"); err != nil || n != 1 {
		t.Errorf("CountLiveThreads(headless) after a close = %d, %v; want 1", n, err)
	}
	if n, err := s.CountLiveThreads(other, "headless"); err != nil || n != 1 {
		t.Errorf("CountLiveThreads(other job, headless) = %d, %v; want 1", n, err)
	}
}
