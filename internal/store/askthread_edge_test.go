package store

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

// askthread_edge_test.go — the EDGE dimension of slice A of
// artifacts/50-contract.md (ask threads ported into kraken): the thread-kind
// validation (including kraken-core's old "ask" value, which is NOT a kind
// here, and the headless+mind refusal), the headless-row/pane exclusion, the
// answering compare-and-set and its bare sentinels, the stale-clear fence,
// what CloseThread / ClaimClosedThread / FinishThreadAnswering leave behind,
// CountLiveThreads' boundaries, and migration 36's replay and defaults.
//
// Written BLIND against the frozen contract text; no implementation file was
// read. Running example: job tech-comms-rollout, asker nSTUDIO / studio-mini
// / jess@probook.ai, question "why is CI red on the rollout PR?".
//
// Every package-level identifier is `askEdge`-prefixed (a sibling blind
// author writes `askContract`-prefixed ones into this package).

const (
	askEdgeQuestion = "why is CI red on the rollout PR?"
	askEdgeZeus     = "b4a2e1f0-0000-4000-8000-000000000001"
	askEdgeSession  = "7c1e9d44-0000-4000-8000-000000000003"
)

// askEdgeOpen opens a temp store under an isolated HOME (the contract's
// environment obligation for every store test).
func askEdgeOpen(t *testing.T) *Store {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	return openTest(t)
}

// askEdgeJob creates a job row to hang threads off (threads carry a real FK).
func askEdgeJob(t *testing.T, s *Store, slug string) int64 {
	t.Helper()
	id, err := s.CreateJob(&Job{Slug: slug})
	if err != nil {
		t.Fatalf("CreateJob %s: %v", slug, err)
	}
	return id
}

// askEdgeRowCount answers "was anything inserted" from SQL, not from the read
// path the write under test might share.
func askEdgeRowCount(t *testing.T, s *Store, jobID int64) int64 {
	t.Helper()
	var n int64
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM threads WHERE job_id = ?`, jobID).Scan(&n); err != nil {
		t.Fatalf("count threads: %v", err)
	}
	return n
}

// askEdgeAsk is a remote headless row in the running example's shape: no
// pane, no window, origin nSTUDIO, role ask.
func askEdgeAsk(jobID int64) Thread {
	return Thread{
		JobID:         jobID,
		Kind:          "headless",
		Role:          "ask",
		OriginNode:    "nSTUDIO",
		OriginLabel:   "studio-mini",
		OriginLogin:   "jess@probook.ai",
		Title:         "ci red",
		SessionID:     askEdgeSession,
		ParentSession: askEdgeZeus,
		State:         "live",
		Seed:          askEdgeQuestion,
		CreatedAt:     1000,
	}
}

// askEdgeAddAsk reserves a headless row and returns it.
func askEdgeAddAsk(t *testing.T, s *Store, jobID int64) Thread {
	t.Helper()
	row, err := s.AddThreadNext(askEdgeAsk(jobID))
	if err != nil {
		t.Fatalf("AddThreadNext(headless): %v", err)
	}
	return row
}

func askEdgeGet(t *testing.T, s *Store, jobID int64, tid string) Thread {
	t.Helper()
	got, err := s.GetThread(jobID, tid)
	if err != nil {
		t.Fatalf("GetThread %s: %v", tid, err)
	}
	return *got
}

// TestAskEdgeErrThreadKindRefusesAndInsertsNothing: any kind other than "",
// "pane", "headless" is ErrThreadKind (errors.Is) on both write paths, and
// the refusal inserts nothing and burns no tid. Case and whitespace variants
// are not the legal words — and kraken-core's "ask" is not a kind here.
func TestAskEdgeErrThreadKindRefusesAndInsertsNothing(t *testing.T) {
	bad := []string{"bogus", "ask", "Headless", "PANE", "headless ", " pane", "asks", "\t"}
	for _, kind := range bad {
		t.Run("next/"+kind, func(t *testing.T) {
			s := askEdgeOpen(t)
			jobID := askEdgeJob(t, s, "tech-comms-rollout")
			row := askEdgeAsk(jobID)
			row.Kind = kind
			got, err := s.AddThreadNext(row)
			if !errors.Is(err, ErrThreadKind) {
				t.Fatalf("AddThreadNext(kind=%q) = %+v, %v; want ErrThreadKind", kind, got, err)
			}
			if n := askEdgeRowCount(t, s, jobID); n != 0 {
				t.Fatalf("%d rows after a refused kind, want 0", n)
			}
			next, nerr := s.NextThreadID(jobID)
			if nerr != nil {
				t.Fatalf("NextThreadID: %v", nerr)
			}
			if next != "t1" {
				t.Fatalf("NextThreadID = %q after a refused insert, want t1", next)
			}
		})
		t.Run("add/"+kind, func(t *testing.T) {
			s := askEdgeOpen(t)
			jobID := askEdgeJob(t, s, "tech-comms-rollout")
			row := askEdgeAsk(jobID)
			row.TID = "t1"
			row.Kind = kind
			if err := s.AddThread(row); !errors.Is(err, ErrThreadKind) {
				t.Fatalf("AddThread(kind=%q) = %v; want ErrThreadKind", kind, err)
			}
			if n := askEdgeRowCount(t, s, jobID); n != 0 {
				t.Fatalf("%d rows after a refused kind, want 0", n)
			}
		})
	}
}

// TestAskEdgeHeadlessMindRefused: a headless row whose role is "mind" is
// ErrThreadKind on both write paths and nothing is inserted; the mind thread
// must be a pane. A pane mind row (kind "" or "pane") is the control.
func TestAskEdgeHeadlessMindRefused(t *testing.T) {
	s := askEdgeOpen(t)
	jobID := askEdgeJob(t, s, "tech-comms-rollout")

	row := askEdgeAsk(jobID)
	row.Role = "mind"
	if got, err := s.AddThreadNext(row); !errors.Is(err, ErrThreadKind) {
		t.Fatalf("AddThreadNext(headless, mind) = %+v, %v; want ErrThreadKind", got, err)
	}
	row.TID = "t1"
	if err := s.AddThread(row); !errors.Is(err, ErrThreadKind) {
		t.Fatalf("AddThread(headless, mind) = %v; want ErrThreadKind", err)
	}
	if n := askEdgeRowCount(t, s, jobID); n != 0 {
		t.Fatalf("%d rows after a refused headless mind row, want 0", n)
	}

	for _, kind := range []string{"", "pane"} {
		got, err := s.AddThreadNext(Thread{JobID: jobID, Kind: kind, Role: "mind", SessionID: "mind-" + kind, State: "live", Pane: "%2", Window: "@1"})
		if err != nil {
			t.Fatalf("AddThreadNext(kind=%q, mind) = %v, want nil (a pane mind thread is legal)", kind, err)
		}
		if got.Kind != "pane" || got.Role != "mind" {
			t.Fatalf("stored kind=%q role=%q, want pane/mind", got.Kind, got.Role)
		}
	}
}

// TestAskEdgeSentinelTexts pins the four new sentinels' exact text; the kind
// sentinel names THIS repo's two words.
func TestAskEdgeSentinelTexts(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{ErrThreadKind, "store: thread kind must be pane or headless"},
		{ErrAskThreadPane, "store: an ask thread holds no pane"},
		{ErrThreadAnswering, "store: thread is already answering"},
		{ErrThreadClosed, "store: thread is closed"},
	}
	for _, c := range cases {
		if c.err.Error() != c.want {
			t.Errorf("sentinel text = %q, want %q", c.err.Error(), c.want)
		}
	}
}

// TestAskEdgeEmptyKindReadsAsPane: "" is normalised to "pane" on write (both
// paths), AddThreadNext hands back the row with Kind filled, and every read
// path carries one of the two words. An empty Role still stores "ask".
func TestAskEdgeEmptyKindReadsAsPane(t *testing.T) {
	s := askEdgeOpen(t)
	jobID := askEdgeJob(t, s, "tech-comms-rollout")
	row, err := s.AddThreadNext(Thread{JobID: jobID, Title: "legacy-ish", SessionID: "s1", State: "live", Pane: "%3", Window: "@2"})
	if err != nil {
		t.Fatalf("AddThreadNext(kind \"\"): %v", err)
	}
	if row.Kind != "pane" {
		t.Fatalf("AddThreadNext handed back Kind = %q, want pane (the row as stored)", row.Kind)
	}
	if err := s.AddThread(Thread{JobID: jobID, TID: "t7", SessionID: "s7", State: "live", Pane: "%9", Window: "@4"}); err != nil {
		t.Fatalf("AddThread(kind \"\"): %v", err)
	}
	for _, tid := range []string{row.TID, "t7"} {
		got := askEdgeGet(t, s, jobID, tid)
		if got.Kind != "pane" {
			t.Fatalf("%s Kind = %q for a row written with \"\", want pane", tid, got.Kind)
		}
		if got.Role != "ask" {
			t.Fatalf("%s Role = %q for a row written with \"\", want ask", tid, got.Role)
		}
	}
	list, err := s.ListThreads(jobID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("ListThreads = %+v, want two rows", list)
	}
	for _, r := range list {
		if r.Kind != "pane" {
			t.Fatalf("ListThreads row %s Kind = %q, want pane", r.TID, r.Kind)
		}
	}
	bySess, err := s.GetThreadBySession("s7")
	if err != nil {
		t.Fatalf("GetThreadBySession: %v", err)
	}
	if bySess.Kind != "pane" {
		t.Fatalf("GetThreadBySession Kind = %q, want pane", bySess.Kind)
	}
}

// TestAskEdgeGetThreadBySessionCarriesNewFields: the by-session read returns
// the six new fields for a headless row.
func TestAskEdgeGetThreadBySessionCarriesNewFields(t *testing.T) {
	s := askEdgeOpen(t)
	jobID := askEdgeJob(t, s, "tech-comms-rollout")
	r := askEdgeAsk(jobID)
	r.Answering = true
	r.Note = "ask failed: boom"
	if _, err := s.AddThreadNext(r); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetThreadBySession(askEdgeSession)
	if err != nil {
		t.Fatalf("GetThreadBySession: %v", err)
	}
	if got.Kind != "headless" || got.OriginNode != "nSTUDIO" || got.OriginLabel != "studio-mini" ||
		got.OriginLogin != "jess@probook.ai" || !got.Answering || got.Note != "ask failed: boom" {
		t.Fatalf("GetThreadBySession = %+v, want every v36 field of the headless row", got)
	}
}

// TestAskEdgeKindCheckConstraint: the column's CHECK refuses a bad kind even
// when a write bypasses the Go validation — including kraken-core's "ask" —
// and the refused UPDATE leaves the row's kind as it was.
func TestAskEdgeKindCheckConstraint(t *testing.T) {
	s := askEdgeOpen(t)
	jobID := askEdgeJob(t, s, "tech-comms-rollout")
	row, err := s.AddThreadNext(Thread{JobID: jobID, SessionID: "s1", State: "live", Pane: "%1", Window: "@1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"bogus", "ask", "Pane", ""} {
		if _, err := s.db.Exec(`UPDATE threads SET kind = ? WHERE job_id = ? AND tid = ?`, bad, jobID, row.TID); err == nil {
			t.Fatalf("raw UPDATE to kind %q succeeded — CHECK(kind IN ('pane','headless')) is missing", bad)
		}
	}
	if got := askEdgeGet(t, s, jobID, row.TID); got.Kind != "pane" {
		t.Fatalf("Kind = %q after refused raw UPDATEs, want pane", got.Kind)
	}
	if _, err := s.db.Exec(`UPDATE threads SET kind = 'headless' WHERE job_id = ? AND tid = ?`, jobID, row.TID); err != nil {
		t.Fatalf("raw UPDATE to kind 'headless' refused: %v", err)
	}
}

// TestAskEdgeAskRowWithPaneRefused: a headless row with a pane, a window, or
// both is ErrAskThreadPane on both insert paths and nothing is inserted.
func TestAskEdgeAskRowWithPaneRefused(t *testing.T) {
	cases := []struct{ name, pane, window string }{
		{"pane only", "%7", ""},
		{"window only", "", "@3"},
		{"both", "%7", "cell-tech-comms-rollout-1:2"},
	}
	for _, c := range cases {
		t.Run("next/"+c.name, func(t *testing.T) {
			s := askEdgeOpen(t)
			jobID := askEdgeJob(t, s, "tech-comms-rollout")
			row := askEdgeAsk(jobID)
			row.Pane, row.Window = c.pane, c.window
			got, err := s.AddThreadNext(row)
			if !errors.Is(err, ErrAskThreadPane) {
				t.Fatalf("AddThreadNext(headless, pane=%q window=%q) = %+v, %v; want ErrAskThreadPane", c.pane, c.window, got, err)
			}
			if n := askEdgeRowCount(t, s, jobID); n != 0 {
				t.Fatalf("%d rows after a refused headless-with-pane, want 0", n)
			}
		})
		t.Run("add/"+c.name, func(t *testing.T) {
			s := askEdgeOpen(t)
			jobID := askEdgeJob(t, s, "tech-comms-rollout")
			row := askEdgeAsk(jobID)
			row.TID = "t1"
			row.Pane, row.Window = c.pane, c.window
			if err := s.AddThread(row); !errors.Is(err, ErrAskThreadPane) {
				t.Fatalf("AddThread(headless, pane=%q window=%q) = %v, want ErrAskThreadPane", c.pane, c.window, err)
			}
			if n := askEdgeRowCount(t, s, jobID); n != 0 {
				t.Fatalf("%d rows after a refused AddThread, want 0", n)
			}
		})
	}
}

// TestAskEdgeAskRowWithEmptyOriginNodeStores: the store does not enforce a
// non-empty origin_node — a self row on a node with no tailscale stores ""
// and reads back "".
func TestAskEdgeAskRowWithEmptyOriginNodeStores(t *testing.T) {
	s := askEdgeOpen(t)
	jobID := askEdgeJob(t, s, "tech-comms-rollout")
	r := askEdgeAsk(jobID)
	r.OriginNode, r.OriginLabel, r.OriginLogin = "", "owner-mac", "Owner"
	row, err := s.AddThreadNext(r)
	if err != nil {
		t.Fatalf("AddThreadNext(headless, empty origin node) = %v, want nil", err)
	}
	got := askEdgeGet(t, s, jobID, row.TID)
	if got.Kind != "headless" || got.OriginNode != "" || got.OriginLabel != "owner-mac" || got.OriginLogin != "Owner" {
		t.Fatalf("stored %+v, want a headless row with origin \"\"/owner-mac/Owner", got)
	}
}

// TestAskEdgePaneRowMayHoldAPane: the exclusion is headless-only; a pane row
// with a pane still inserts (kind "" and "pane" alike).
func TestAskEdgePaneRowMayHoldAPane(t *testing.T) {
	s := askEdgeOpen(t)
	jobID := askEdgeJob(t, s, "tech-comms-rollout")
	for _, kind := range []string{"", "pane"} {
		if _, err := s.AddThreadNext(Thread{JobID: jobID, Kind: kind, SessionID: "s-" + kind, State: "live", Pane: "%4", Window: "@1"}); err != nil {
			t.Fatalf("AddThreadNext(kind=%q, pane set) = %v, want nil", kind, err)
		}
	}
}

// TestAskEdgeSetThreadPaneOnAskRow: SetThreadPane refuses a non-empty pane or
// window on a headless row and leaves the row pane-less; clearing to ("", "")
// stores nothing non-empty and is not refused.
func TestAskEdgeSetThreadPaneOnAskRow(t *testing.T) {
	s := askEdgeOpen(t)
	jobID := askEdgeJob(t, s, "tech-comms-rollout")
	row := askEdgeAddAsk(t, s, jobID)

	for _, c := range []struct{ pane, window string }{{"%9", "@4"}, {"%9", ""}, {"", "@4"}} {
		if err := s.SetThreadPane(jobID, row.TID, c.pane, c.window); !errors.Is(err, ErrAskThreadPane) {
			t.Fatalf("SetThreadPane(headless, %q, %q) = %v, want ErrAskThreadPane", c.pane, c.window, err)
		}
		got := askEdgeGet(t, s, jobID, row.TID)
		if got.Pane != "" || got.Window != "" {
			t.Fatalf("headless row holds pane=%q window=%q after a refused SetThreadPane", got.Pane, got.Window)
		}
		if got.Kind != "headless" {
			t.Fatalf("Kind = %q after a refused SetThreadPane, want headless", got.Kind)
		}
	}
	if err := s.SetThreadPane(jobID, row.TID, "", ""); err != nil {
		t.Fatalf("SetThreadPane(headless, \"\", \"\") = %v, want nil — nothing non-empty is stored", err)
	}
}

// TestAskEdgeSetThreadPaneUnknownStillNotFound: the unknown-tid answer is
// unchanged by the new check.
func TestAskEdgeSetThreadPaneUnknownStillNotFound(t *testing.T) {
	s := askEdgeOpen(t)
	jobID := askEdgeJob(t, s, "tech-comms-rollout")
	if err := s.SetThreadPane(jobID, "t9", "%1", "@1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetThreadPane(unknown) = %v, want ErrNotFound", err)
	}
}

// TestAskEdgeClaimAnsweringRefusals: answering → ErrThreadAnswering with
// nothing changed; closed → ErrThreadClosed; unknown (or another job's tid)
// → ErrNotFound. All three come back BARE (==, not just errors.Is).
func TestAskEdgeClaimAnsweringRefusals(t *testing.T) {
	s := askEdgeOpen(t)
	jobID := askEdgeJob(t, s, "tech-comms-rollout")
	row := askEdgeAddAsk(t, s, jobID)

	if err := s.ClaimThreadAnswering(jobID, row.TID); err != nil {
		t.Fatalf("first claim on an idle live row = %v, want nil", err)
	}
	before := askEdgeGet(t, s, jobID, row.TID)
	if !before.Answering {
		t.Fatal("Answering = false after a successful claim")
	}
	if err := s.ClaimThreadAnswering(jobID, row.TID); err != ErrThreadAnswering {
		t.Fatalf("claim on an answering row = %v, want bare ErrThreadAnswering", err)
	}
	if after := askEdgeGet(t, s, jobID, row.TID); after != before {
		t.Fatalf("a refused claim changed the row:\n before %+v\n after  %+v", before, after)
	}

	if err := s.CloseThread(jobID, row.TID, 0); err != nil {
		t.Fatal(err)
	}
	closedBefore := askEdgeGet(t, s, jobID, row.TID)
	if err := s.ClaimThreadAnswering(jobID, row.TID); err != ErrThreadClosed {
		t.Fatalf("claim on a closed row = %v, want bare ErrThreadClosed", err)
	}
	if got := askEdgeGet(t, s, jobID, row.TID); got != closedBefore {
		t.Fatalf("a refused claim on a closed row changed it:\n before %+v\n after  %+v", closedBefore, got)
	}

	if err := s.ClaimThreadAnswering(jobID, "t99"); err != ErrNotFound {
		t.Fatalf("claim on an unknown tid = %v, want bare ErrNotFound", err)
	}
	other := askEdgeJob(t, s, "other-job")
	if err := s.ClaimThreadAnswering(other, row.TID); err != ErrNotFound {
		t.Fatalf("claim of %s under another job = %v, want bare ErrNotFound", row.TID, err)
	}
}

// TestAskEdgeClaimClearsNote: a successful claim sets note = ”.
func TestAskEdgeClaimClearsNote(t *testing.T) {
	s := askEdgeOpen(t)
	jobID := askEdgeJob(t, s, "tech-comms-rollout")
	row := askEdgeAddAsk(t, s, jobID)
	if err := s.FinishThreadAnswering(jobID, row.TID, "ask failed: exit status 1"); err != nil {
		t.Fatal(err)
	}
	if err := s.ClaimThreadAnswering(jobID, row.TID); err != nil {
		t.Fatalf("claim = %v", err)
	}
	got := askEdgeGet(t, s, jobID, row.TID)
	if !got.Answering || got.Note != "" {
		t.Fatalf("after claim Answering=%v Note=%q, want true and \"\"", got.Answering, got.Note)
	}
}

// TestAskEdgeConcurrentClaimsExactlyOneWins: N concurrent claims on one idle
// row — exactly one nil, every other bare ErrThreadAnswering.
func TestAskEdgeConcurrentClaimsExactlyOneWins(t *testing.T) {
	s := askEdgeOpen(t)
	jobID := askEdgeJob(t, s, "tech-comms-rollout")
	row := askEdgeAddAsk(t, s, jobID)

	const n = 16
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
			t.Errorf("claim %d = %v, want nil or bare ErrThreadAnswering", i, err)
		}
	}
	if wins != 1 {
		t.Fatalf("%d of %d concurrent claims succeeded, want exactly 1", wins, n)
	}
	if got := askEdgeGet(t, s, jobID, row.TID); !got.Answering {
		t.Fatal("row not answering after the race")
	}
}

// TestAskEdgeFinishAnswering: whatever the state, Finish clears answering and
// sets the note; an empty note clears it; unknown is bare ErrNotFound.
func TestAskEdgeFinishAnswering(t *testing.T) {
	s := askEdgeOpen(t)
	jobID := askEdgeJob(t, s, "tech-comms-rollout")
	row := askEdgeAddAsk(t, s, jobID)

	if err := s.FinishThreadAnswering(jobID, "t42", "x"); err != ErrNotFound {
		t.Fatalf("Finish(unknown) = %v, want bare ErrNotFound", err)
	}

	// on an answering row
	if err := s.ClaimThreadAnswering(jobID, row.TID); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishThreadAnswering(jobID, row.TID, "ask failed: exit status 3"); err != nil {
		t.Fatalf("Finish on an answering row = %v", err)
	}
	if got := askEdgeGet(t, s, jobID, row.TID); got.Answering || got.Note != "ask failed: exit status 3" {
		t.Fatalf("Answering=%v Note=%q, want false and the note", got.Answering, got.Note)
	}
	// on an idle row (answering already 0)
	if err := s.FinishThreadAnswering(jobID, row.TID, "ask timed out after 5m0s"); err != nil {
		t.Fatalf("Finish on an idle row = %v", err)
	}
	if got := askEdgeGet(t, s, jobID, row.TID); got.Answering || got.Note != "ask timed out after 5m0s" {
		t.Fatalf("Answering=%v Note=%q, want false and the note", got.Answering, got.Note)
	}
	// empty clears
	if err := s.FinishThreadAnswering(jobID, row.TID, ""); err != nil {
		t.Fatal(err)
	}
	if got := askEdgeGet(t, s, jobID, row.TID); got.Note != "" {
		t.Fatalf("Note = %q after Finish(\"\"), want cleared", got.Note)
	}
	// on a closed row — "whatever its state"
	if err := s.CloseThread(jobID, row.TID, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishThreadAnswering(jobID, row.TID, "ask cancelled"); err != nil {
		t.Fatalf("Finish on a closed row = %v, want nil", err)
	}
	got := askEdgeGet(t, s, jobID, row.TID)
	if got.State != "closed" || got.Answering || got.Note != "ask cancelled" {
		t.Fatalf("closed row after Finish = %+v, want closed, not answering, note \"ask cancelled\"", got)
	}
}

// TestAskEdgeClearStaleFence: ClearStaleThreadAnswering writes ONLY where
// answering = 1 holds at write time. An idle row keeps its own note and
// reports (false, nil); an unknown tid reports (false, nil); an answering
// row is cleared and reports (true, nil); a second clear is (false, nil).
func TestAskEdgeClearStaleFence(t *testing.T) {
	s := askEdgeOpen(t)
	jobID := askEdgeJob(t, s, "tech-comms-rollout")
	const stale = "answer interrupted — kraken serve restarted"

	if wrote, err := s.ClearStaleThreadAnswering(jobID, "t99", stale); err != nil || wrote {
		t.Fatalf("ClearStale(unknown) = %v, %v; want false, nil", wrote, err)
	}

	idle := askEdgeAddAsk(t, s, jobID)
	if err := s.FinishThreadAnswering(jobID, idle.TID, ""); err != nil {
		t.Fatal(err)
	}
	if wrote, err := s.ClearStaleThreadAnswering(jobID, idle.TID, stale); err != nil || wrote {
		t.Fatalf("ClearStale(idle, no note) = %v, %v; want false, nil", wrote, err)
	}
	if got := askEdgeGet(t, s, jobID, idle.TID); got.Note != "" || got.Answering {
		t.Fatalf("idle row after ClearStale = Answering %v Note %q, want untouched", got.Answering, got.Note)
	}
	// a run that already finished wrote its own note: the stale clear must
	// not overwrite it
	if err := s.FinishThreadAnswering(jobID, idle.TID, "ask failed: exit status 1"); err != nil {
		t.Fatal(err)
	}
	if wrote, err := s.ClearStaleThreadAnswering(jobID, idle.TID, stale); err != nil || wrote {
		t.Fatalf("ClearStale(finished row) = %v, %v; want false, nil", wrote, err)
	}
	if got := askEdgeGet(t, s, jobID, idle.TID); got.Note != "ask failed: exit status 1" {
		t.Fatalf("ClearStale overwrote a finished run's note: %q", got.Note)
	}

	busy := askEdgeAddAsk(t, s, jobID)
	if err := s.ClaimThreadAnswering(jobID, busy.TID); err != nil {
		t.Fatal(err)
	}
	if wrote, err := s.ClearStaleThreadAnswering(jobID, busy.TID, stale); err != nil || !wrote {
		t.Fatalf("ClearStale(answering) = %v, %v; want true, nil", wrote, err)
	}
	if got := askEdgeGet(t, s, jobID, busy.TID); got.Answering || got.Note != stale {
		t.Fatalf("answering row after ClearStale = Answering %v Note %q, want false and %q", got.Answering, got.Note, stale)
	}
	if wrote, err := s.ClearStaleThreadAnswering(jobID, busy.TID, "again"); err != nil || wrote {
		t.Fatalf("second ClearStale = %v, %v; want false, nil", wrote, err)
	}
	if got := askEdgeGet(t, s, jobID, busy.TID); got.Note != stale {
		t.Fatalf("a no-op ClearStale changed the note to %q", got.Note)
	}
}

// TestAskEdgeClearStaleOnClosedAnsweringRow: the fence is answering = 1
// alone, not liveness — a closed row still answering (an impossible state
// through CloseThread, forced here through the insert path) is cleared.
func TestAskEdgeClearStaleOnClosedAnsweringRow(t *testing.T) {
	s := askEdgeOpen(t)
	jobID := askEdgeJob(t, s, "tech-comms-rollout")
	r := askEdgeAsk(jobID)
	r.State = "closed"
	r.ClosedAt = 5
	r.Answering = true
	row, err := s.AddThreadNext(r)
	if err != nil {
		t.Fatalf("AddThreadNext(closed, answering) = %v", err)
	}
	if wrote, err := s.ClearStaleThreadAnswering(jobID, row.TID, "x"); err != nil || !wrote {
		t.Fatalf("ClearStale(closed, answering) = %v, %v; want true, nil", wrote, err)
	}
	if got := askEdgeGet(t, s, jobID, row.TID); got.Answering || got.Note != "x" || got.State != "closed" {
		t.Fatalf("after ClearStale = %+v, want closed, not answering, note x", got)
	}
}

// TestAskEdgeCloseClearsAnsweringKeepsNote: CloseThread sets answering = 0
// and leaves note as is.
func TestAskEdgeCloseClearsAnsweringKeepsNote(t *testing.T) {
	s := askEdgeOpen(t)
	jobID := askEdgeJob(t, s, "tech-comms-rollout")
	r := askEdgeAsk(jobID)
	r.Answering = true
	r.Note = "ask failed: exit status 2"
	row, err := s.AddThreadNext(r)
	if err != nil {
		t.Fatalf("AddThreadNext(answering, note) = %v", err)
	}
	if got := askEdgeGet(t, s, jobID, row.TID); !got.Answering || got.Note != r.Note {
		t.Fatalf("stored Answering=%v Note=%q, want true and %q", got.Answering, got.Note, r.Note)
	}
	if err := s.CloseThread(jobID, row.TID, 0); err != nil {
		t.Fatal(err)
	}
	got := askEdgeGet(t, s, jobID, row.TID)
	if got.Answering {
		t.Fatal("Answering still true after CloseThread")
	}
	if got.Note != r.Note {
		t.Fatalf("Note = %q after CloseThread, want %q kept", got.Note, r.Note)
	}
	if got.Kind != "headless" || got.OriginNode != "nSTUDIO" {
		t.Fatalf("CloseThread disturbed kind/origin: %+v", got)
	}
}

// TestAskEdgeClaimClosedKeepsNoteAndOrigin: a revive reopens the row but
// leaves note, kind and the three origin fields as they were.
func TestAskEdgeClaimClosedKeepsNoteAndOrigin(t *testing.T) {
	s := askEdgeOpen(t)
	jobID := askEdgeJob(t, s, "tech-comms-rollout")
	row := askEdgeAddAsk(t, s, jobID)
	if err := s.FinishThreadAnswering(jobID, row.TID, "answer interrupted — kraken serve restarted"); err != nil {
		t.Fatal(err)
	}
	if err := s.CloseThread(jobID, row.TID, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.ClaimClosedThread(jobID, row.TID); err != nil {
		t.Fatalf("ClaimClosedThread = %v", err)
	}
	got := askEdgeGet(t, s, jobID, row.TID)
	if got.State != "live" || got.Kind != "headless" {
		t.Fatalf("revived row State=%q Kind=%q, want live headless", got.State, got.Kind)
	}
	if got.Role != "ask" {
		t.Fatalf("revived row Role=%q, want ask", got.Role)
	}
	if got.Note != "answer interrupted — kraken serve restarted" {
		t.Fatalf("Note = %q after revive, want kept", got.Note)
	}
	if got.OriginNode != "nSTUDIO" || got.OriginLabel != "studio-mini" || got.OriginLogin != "jess@probook.ai" {
		t.Fatalf("origin after revive = %q/%q/%q, want nSTUDIO/studio-mini/jess@probook.ai", got.OriginNode, got.OriginLabel, got.OriginLogin)
	}
	if got.Answering {
		t.Fatal("revived row is answering")
	}
}

// TestAskEdgeCountLiveThreadsBoundaries: per job, per kind, live only; 0 for
// a job with none and for an unknown job; an unknown kind word (including
// kraken-core's "ask") counts 0; answering does not change liveness.
func TestAskEdgeCountLiveThreadsBoundaries(t *testing.T) {
	s := askEdgeOpen(t)
	jobID := askEdgeJob(t, s, "tech-comms-rollout")
	other := askEdgeJob(t, s, "other-job")

	count := func(job int64, kind string) int {
		t.Helper()
		n, err := s.CountLiveThreads(job, kind)
		if err != nil {
			t.Fatalf("CountLiveThreads(%d, %q): %v", job, kind, err)
		}
		return n
	}
	if n := count(jobID, "headless"); n != 0 {
		t.Fatalf("headless count on a job with no threads = %d, want 0", n)
	}
	if n := count(999999, "headless"); n != 0 {
		t.Fatalf("headless count on an unknown job = %d, want 0", n)
	}

	a1 := askEdgeAddAsk(t, s, jobID)
	askEdgeAddAsk(t, s, jobID)
	closed := askEdgeAddAsk(t, s, jobID)
	if err := s.CloseThread(jobID, closed.TID, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddThreadNext(Thread{JobID: jobID, Kind: "pane", SessionID: "p1", State: "live", Pane: "%2", Window: "@1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddThreadNext(Thread{JobID: jobID, Kind: "pane", Role: "mind", SessionID: "m1", State: "live", Pane: "%3", Window: "@2"}); err != nil {
		t.Fatal(err)
	}
	askEdgeAddAsk(t, s, other)

	if n := count(jobID, "headless"); n != 2 {
		t.Fatalf("live headless count = %d, want 2 (closed and pane rows and other jobs excluded)", n)
	}
	if n := count(jobID, "pane"); n != 2 {
		t.Fatalf("live pane count = %d, want 2 (both roles)", n)
	}
	for _, k := range []string{"bogus", "ask", "", "Headless"} {
		if n := count(jobID, k); n != 0 {
			t.Fatalf("count of kind %q = %d, want 0", k, n)
		}
	}
	if err := s.ClaimThreadAnswering(jobID, a1.TID); err != nil {
		t.Fatal(err)
	}
	if n := count(jobID, "headless"); n != 2 {
		t.Fatalf("live headless count with one answering = %d, want 2", n)
	}
}

// TestAskEdgeMigrationFromV35DefaultsEveryRow: a database aged to v35 by
// downgradeFreshDB migrates to 36 keeping every thread row — a mind pane row
// with a turn uuid and a plain ask pane row — each then reading Kind "pane",
// empty origin, Answering false, Note "", Role/TurnUUID untouched. A row that
// was headless before the age-down comes back as a pane (the columns were
// dropped; the defaults are what a real v35 row gets).
func TestAskEdgeMigrationFromV35DefaultsEveryRow(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dbPath := filepath.Join(t.TempDir(), "kraken.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	jobID := askEdgeJob(t, s, "tech-comms-rollout")
	if err := s.AddThread(Thread{JobID: jobID, TID: "t1", Role: "mind", TurnUUID: "turn-1", SessionID: "m1", State: "live", Pane: "%1", Window: "@1", CreatedAt: 10}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddThread(Thread{JobID: jobID, TID: "t2", SessionID: "a1", State: "closed", ClosedAt: 20, CreatedAt: 11}); err != nil {
		t.Fatal(err)
	}
	h := askEdgeAsk(jobID)
	h.TID = "t3"
	h.Answering = true
	h.Note = "ask failed: x"
	if err := s.AddThread(h); err != nil {
		t.Fatal(err)
	}

	downgradeFreshDB(t, s, 35)
	if err := s.migrate(); err != nil {
		t.Fatalf("migrate from v35: %v", err)
	}
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 38 {
		t.Fatalf("user_version = %d after migrating from v35, want 38", version)
	}
	list, err := s.ListThreads(jobID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("ListThreads after migration = %+v, want the 3 rows kept", list)
	}
	for _, r := range list {
		if r.Kind != "pane" || r.OriginNode != "" || r.OriginLabel != "" || r.OriginLogin != "" || r.Answering || r.Note != "" {
			t.Errorf("row %s after migration = %+v, want pane with empty origin, not answering, no note", r.TID, r)
		}
	}
	m := askEdgeGet(t, s, jobID, "t1")
	if m.Role != "mind" || m.TurnUUID != "turn-1" || m.Pane != "%1" {
		t.Errorf("t1 after migration = %+v, want role mind, turn uuid turn-1, pane %%1 untouched", m)
	}
	if a := askEdgeGet(t, s, jobID, "t2"); a.Role != "ask" || a.State != "closed" || a.ClosedAt != 20 {
		t.Errorf("t2 after migration = %+v, want role ask, closed at 20", a)
	}
}

// TestAskEdgeMigrationReplayIsNoOp: a v36 database whose user_version alone
// is reset to 35 (no reshape — the probe-guarded hook must replay as a
// no-op) re-Opens to 36 with no error and every row and value intact; and
// opening a v36 database twice is idempotent.
func TestAskEdgeMigrationReplayIsNoOp(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dbPath := filepath.Join(t.TempDir(), "kraken.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	jobID := askEdgeJob(t, s, "tech-comms-rollout")
	r := askEdgeAsk(jobID)
	r.Answering = true
	r.Note = "ask failed: x"
	row, err := s.AddThreadNext(r)
	if err != nil {
		t.Fatal(err)
	}
	want := askEdgeGet(t, s, jobID, row.TID)
	if _, err := s.db.Exec(`PRAGMA user_version = 35`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 2; i++ {
		s2, err := Open(dbPath)
		if err != nil {
			t.Fatalf("Open #%d of a replayed v36 db: %v", i+1, err)
		}
		var version int
		if err := s2.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
			s2.Close()
			t.Fatal(err)
		}
		if version != 38 {
			s2.Close()
			t.Fatalf("user_version after Open #%d = %d, want 38", i+1, version)
		}
		got, err := s2.GetThread(jobID, row.TID)
		if err != nil {
			s2.Close()
			t.Fatalf("GetThread after Open #%d: %v", i+1, err)
		}
		if *got != want {
			s2.Close()
			t.Fatalf("row after Open #%d = %+v, want %+v", i+1, *got, want)
		}
		if err := s2.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

// TestAskEdgeSchemaVersionConstant: the version line moves to exactly 36 —
// 37 since the nightly mind pass (schemaV37), 38 since the spawn links
// (jobSpawnsTable).
func TestAskEdgeSchemaVersionConstant(t *testing.T) {
	if schemaVersion != 38 {
		t.Fatalf("schemaVersion = %d, want 38", schemaVersion)
	}
	s := askEdgeOpen(t)
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 38 {
		t.Fatalf("fresh user_version = %d, want 38", version)
	}
}
