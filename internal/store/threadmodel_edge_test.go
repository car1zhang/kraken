package store

import (
	"strings"
	"testing"
)

// threadmodel_edge_test.go — the EDGE dimension of the per-thread model row
// of artifacts/50-contract.md (slice S1): boundaries, error paths, refusals.
//
// Written BLIND, against the frozen contract text alone — no implementation
// was read. Every assertion below traces to a stated obligation:
//
//   - store.validateThreadModel: nil for "" and each of the four aliases; a
//     descriptive error naming the four aliases for anything else.
//   - store.normalizeThread: an invalid Model makes AddThread / AddThreadNext
//     refuse with that error and insert NOTHING.
//   - store.Thread.Model: "" on every legacy row.
//   - schema v22: `ALTER TABLE threads ADD COLUMN model TEXT NOT NULL
//     DEFAULT ''` — additive, so a row written without the column lands on ''.
//   - round trip: Model is stored on insert and returned by GetThread /
//     ListThreads; SetThreadTitle, SetThreadPane, CloseThread and
//     ClaimClosedThread leave it untouched.
//
// Where the contract is silent the probe is OMITTED rather than widened: a
// tolerant multi-shape assertion turns a gap into a false pass. The
// disclosures are in /tmp/better-dialog-258-athena-edge.md.
//
// Package-level identifiers are `edge`-prefixed by this package's own
// convention (watch_edge_test.go), and because a sibling blind author writes
// `contract`-prefixed declarations into the same package.

// edgeThreadAliases is the frozen alias set — the ONLY four non-empty values
// a thread row may carry. Deliberately a literal rather than a read of
// core.ThreadModels: store cannot import core (util ← store ← core ← jobs ←
// api), and a test that derived the set from the thing under test would pass
// no matter what that thing contained.
var edgeThreadAliases = []string{"haiku", "sonnet", "opus", "fable"}

// edgeThreadJob creates a job to hang threads off: every threads row carries
// a real jobs FK (ON DELETE CASCADE), so an insert against a made-up job id
// is refused by _foreign_keys=on before any model validation could run.
func edgeThreadJob(t *testing.T, s *Store, slug string) int64 {
	t.Helper()
	id, err := s.CreateJob(&Job{Slug: slug})
	if err != nil {
		t.Fatalf("CreateJob %s: %v", slug, err)
	}
	return id
}

// edgeThreadRowCount counts a job's rows straight from SQL rather than
// through ListThreads, so "nothing was inserted" is answered by the database
// and not by the same read path the insert might have corrupted.
func edgeThreadRowCount(t *testing.T, s *Store, jobID int64) int64 {
	t.Helper()
	var n int64
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM threads WHERE job_id = ?`, jobID).Scan(&n); err != nil {
		t.Fatalf("count threads: %v", err)
	}
	return n
}

// TestEdgeValidateThreadModelRefusals is validateThreadModel's whole
// boundary. The accepted set is EXACTLY {"", haiku, sonnet, opus, fable};
// everything else — including the exact model ids those aliases resolve to,
// and case variants of the aliases themselves — must be refused.
//
// The exact-id case is the one that matters most in practice: `claude-opus-5`
// is a real, correct model id, and an implementation that accepted ids
// alongside aliases would look right in every happy-path test while quietly
// letting a caller pin a tier the map no longer controls. The contract says
// "aliases only", so a valid id is a REFUSAL here.
func TestEdgeValidateThreadModelRefusals(t *testing.T) {
	for _, ok := range append([]string{""}, edgeThreadAliases...) {
		if err := validateThreadModel(ok); err != nil {
			t.Errorf("validateThreadModel(%q) = %v, want nil", ok, err)
		}
	}

	bad := []struct{ name, model string }{
		{"garbage", "gpt-5"},
		{"empty-ish whitespace", " "},
		{"tab", "\t"},
		{"exact opus id", "claude-opus-5"},
		{"exact haiku id", "claude-haiku-4-5-20251001"},
		{"exact sonnet id", "claude-sonnet-5"},
		{"exact fable id", "claude-fable-5"},
		{"title case", "Haiku"},
		{"upper case", "OPUS"},
		{"mixed case", "SoNnEt"},
		{"trailing space", "haiku "},
		{"leading space", " fable"},
		{"two aliases", "haiku,sonnet"},
		{"alias plus junk", "opusx"},
		{"prefix of alias", "hai"},
		{"numeric", "5"},
		{"newline", "opus\n"},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			err := validateThreadModel(c.model)
			if err == nil {
				t.Fatalf("validateThreadModel(%q) = nil, want a refusal", c.model)
			}
			// "a descriptive error naming the four aliases": a bare "bad
			// model" tells a caller (and the 400 the API derives from it)
			// nothing about what to send instead.
			msg := err.Error()
			for _, alias := range edgeThreadAliases {
				if !strings.Contains(msg, alias) {
					t.Errorf("validateThreadModel(%q) error %q does not name %q", c.model, msg, alias)
				}
			}
		})
	}
}

// TestEdgeAddThreadRefusesInvalidModelAndStoresNothing is the refusal half of
// normalizeThread's obligation. The error alone is not the contract — "insert
// nothing" is, and the two can come apart: an implementation that validated
// AFTER insertThreadTx would return the right error over a row that is
// already in the table, and every later read would serve a thread on a model
// kraken cannot spawn.
func TestEdgeAddThreadRefusesInvalidModelAndStoresNothing(t *testing.T) {
	s := openTest(t)
	jobID := edgeThreadJob(t, s, "edge-badmodel")

	err := s.AddThread(Thread{JobID: jobID, TID: "t1", Model: "gpt-5"})
	if err == nil {
		t.Fatal("AddThread with model \"gpt-5\" = nil, want a refusal")
	}
	for _, alias := range edgeThreadAliases {
		if !strings.Contains(err.Error(), alias) {
			t.Errorf("AddThread refusal %q does not name %q", err.Error(), alias)
		}
	}
	if n := edgeThreadRowCount(t, s, jobID); n != 0 {
		t.Fatalf("%d thread rows after a refused AddThread, want 0", n)
	}
	if _, gerr := s.GetThread(jobID, "t1"); gerr == nil {
		t.Fatal("GetThread found a row a refused AddThread must never have written")
	}
}

// TestEdgeAddThreadNextRefusesInvalidModelAndStoresNothing is the same
// refusal on the CREATION path, where "insert nothing" is strictly harder:
// AddThreadNext allocates the tid inside its own transaction BEFORE the row
// is normalized, so a validation failure must roll that transaction back
// whole. A leaked half-transaction would either leave a row or burn t1 —
// NextThreadID would then hand out t2 for a job that has no threads at all.
func TestEdgeAddThreadNextRefusesInvalidModelAndStoresNothing(t *testing.T) {
	s := openTest(t)
	jobID := edgeThreadJob(t, s, "edge-badmodel-next")

	got, err := s.AddThreadNext(Thread{JobID: jobID, Model: "Haiku"})
	if err == nil {
		t.Fatalf("AddThreadNext with model \"Haiku\" = %+v, nil; want a refusal", got)
	}
	if n := edgeThreadRowCount(t, s, jobID); n != 0 {
		t.Fatalf("%d thread rows after a refused AddThreadNext, want 0", n)
	}
	next, nerr := s.NextThreadID(jobID)
	if nerr != nil {
		t.Fatalf("NextThreadID: %v", nerr)
	}
	if next != "t1" {
		t.Fatalf("NextThreadID = %q after a refused branch, want t1 — the tid was burned", next)
	}
}

// TestEdgeThreadModelEmptyRoundTrips pins the legacy value itself: "" is a
// LEGAL model, not a missing one, and it must survive insert → get → list
// without being defaulted to an alias on the way through. A store that
// helpfully stamped "opus" onto a blank model would make every legacy row
// claim a tier it was never branched on.
func TestEdgeThreadModelEmptyRoundTrips(t *testing.T) {
	s := openTest(t)
	jobID := edgeThreadJob(t, s, "edge-emptymodel")

	if err := s.AddThread(Thread{JobID: jobID, TID: "t1", CreatedAt: 1}); err != nil {
		t.Fatalf("AddThread with no model: %v", err)
	}
	got, err := s.GetThread(jobID, "t1")
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if got.Model != "" {
		t.Fatalf("Model = %q on a row inserted without one, want \"\"", got.Model)
	}
	list, err := s.ListThreads(jobID)
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(list) != 1 || list[0].Model != "" {
		t.Fatalf("ListThreads = %+v, want one row with an empty model", list)
	}
}

// TestEdgeLegacyThreadRowReadsEmptyModel simulates the row the migration
// actually has to survive: one written by a kraken that had no model column
// at all. The INSERT below names every pre-v22 column and OMITS model, which
// only succeeds if the added column is NOT NULL with an empty-string DEFAULT,
// exactly as the contract's ALTER pins it — a column added NOT NULL with no
// default would fail this insert outright, and one added nullable would scan
// back through threadColumns as a NULL rather than "".
//
// This is the read side of "rows inserted before this change read back with
// Model == empty": GetThread and ListThreads must both serve "" rather than
// erroring, and nothing may back-fill it to a tier.
func TestEdgeLegacyThreadRowReadsEmptyModel(t *testing.T) {
	s := openTest(t)
	jobID := edgeThreadJob(t, s, "edge-legacyrow")

	if _, err := s.db.Exec(
		`INSERT INTO threads (job_id, tid, title, session_id, parent_session, fork_uuid,
			cwd, pane, "window", state, seed, created_at, closed_at)
			VALUES (?, 't1', 'legacy thread', 'sess-legacy', 'zeus-legacy', 'uuid-legacy',
			'/tmp/wt', '%3', 'cell-x:1', 'live', 'why', 1000, 0)`,
		jobID,
	); err != nil {
		t.Fatalf("legacy insert (no model column) failed — the added column is not NOT NULL DEFAULT '': %v", err)
	}

	got, err := s.GetThread(jobID, "t1")
	if err != nil {
		t.Fatalf("GetThread on a legacy row: %v", err)
	}
	if got.Model != "" {
		t.Fatalf("legacy row Model = %q, want \"\"", got.Model)
	}
	// the rest of the row must be untouched by the new column's arrival —
	// a mis-ordered scanThread would transpose model into a neighbour
	if got.Title != "legacy thread" || got.SessionID != "sess-legacy" || got.Seed != "why" {
		t.Fatalf("legacy row scanned wrong: %+v", *got)
	}
	list, err := s.ListThreads(jobID)
	if err != nil {
		t.Fatalf("ListThreads on a legacy row: %v", err)
	}
	if len(list) != 1 || list[0].Model != "" || list[0].Title != "legacy thread" {
		t.Fatalf("ListThreads = %+v, want one legacy row with an empty model", list)
	}
}

// TestEdgeThreadModelSurvivesCloseAndClaim is the close/revive cycle. The row
// is the ONLY place a revive can learn which model to respawn on — the tmux
// window is gone by then — so a CloseThread or ClaimClosedThread that touched
// `model` would silently drop a thread branched on haiku back to the default
// tier the next time carl revived it, with nothing visibly wrong.
//
// Every intervening verb the contract names is exercised on the way: title,
// pane, close, claim.
func TestEdgeThreadModelSurvivesCloseAndClaim(t *testing.T) {
	s := openTest(t)
	jobID := edgeThreadJob(t, s, "edge-cycle")

	for _, model := range edgeThreadAliases {
		t.Run(model, func(t *testing.T) {
			tt, err := s.AddThreadNext(Thread{JobID: jobID, Model: model, Title: "ask", CreatedAt: 1})
			if err != nil {
				t.Fatalf("AddThreadNext(%q): %v", model, err)
			}
			if tt.Model != model {
				t.Fatalf("AddThreadNext returned Model %q, want %q", tt.Model, model)
			}
			tid := tt.TID

			if err := s.SetThreadPane(jobID, tid, "%11", "cell-edge:2"); err != nil {
				t.Fatalf("SetThreadPane: %v", err)
			}
			if err := s.SetThreadTitle(jobID, tid, "renamed"); err != nil {
				t.Fatalf("SetThreadTitle: %v", err)
			}
			mid, err := s.GetThread(jobID, tid)
			if err != nil {
				t.Fatalf("GetThread after pane+title: %v", err)
			}
			if mid.Model != model {
				t.Fatalf("Model = %q after SetThreadPane/SetThreadTitle, want %q", mid.Model, model)
			}

			if err := s.CloseThread(jobID, tid, 0); err != nil {
				t.Fatalf("CloseThread: %v", err)
			}
			closed, err := s.GetThread(jobID, tid)
			if err != nil {
				t.Fatalf("GetThread after close: %v", err)
			}
			if closed.State != "closed" {
				t.Fatalf("State = %q after CloseThread, want closed", closed.State)
			}
			if closed.Model != model {
				t.Fatalf("Model = %q on a CLOSED row, want %q — a revive can only learn the tier from here", closed.Model, model)
			}

			if err := s.ClaimClosedThread(jobID, tid); err != nil {
				t.Fatalf("ClaimClosedThread: %v", err)
			}
			revived, err := s.GetThread(jobID, tid)
			if err != nil {
				t.Fatalf("GetThread after claim: %v", err)
			}
			if revived.State != "live" {
				t.Fatalf("State = %q after ClaimClosedThread, want live", revived.State)
			}
			if revived.Model != model {
				t.Fatalf("Model = %q after a close/revive cycle, want %q", revived.Model, model)
			}
		})
	}
}

// TestEdgeLegacyThreadModelSurvivesCloseAndClaim is the same cycle on the
// value the cycle is most likely to mangle in the other direction: "". A
// close/revive that DEFAULTED an empty model would make a legacy thread
// silently acquire a tier it was never branched on — the mirror image of the
// bug above, and equally invisible.
func TestEdgeLegacyThreadModelSurvivesCloseAndClaim(t *testing.T) {
	s := openTest(t)
	jobID := edgeThreadJob(t, s, "edge-cycle-legacy")

	if err := s.AddThread(Thread{JobID: jobID, TID: "t1", CreatedAt: 1}); err != nil {
		t.Fatalf("AddThread: %v", err)
	}
	if err := s.CloseThread(jobID, "t1", 0); err != nil {
		t.Fatalf("CloseThread: %v", err)
	}
	if err := s.ClaimClosedThread(jobID, "t1"); err != nil {
		t.Fatalf("ClaimClosedThread: %v", err)
	}
	got, err := s.GetThread(jobID, "t1")
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if got.Model != "" {
		t.Fatalf("Model = %q after a legacy row's close/revive cycle, want \"\"", got.Model)
	}
}

// TestEdgeThreadModelValidationRunsBeforeTIDAllocation is a narrow ordering
// probe with a real consequence. AddThreadNext ignores any TID the caller
// set, so the tid a refused call would have taken is the job's NEXT one — and
// the contract's "insert nothing" is only actually true if the whole
// transaction rolls back. Interleaving a refused call between two good ones
// must leave the numbering unbroken: t1, t2, with nothing skipped.
func TestEdgeThreadModelValidationLeavesTheCounterIntact(t *testing.T) {
	s := openTest(t)
	jobID := edgeThreadJob(t, s, "edge-counter")

	first, err := s.AddThreadNext(Thread{JobID: jobID, Model: "opus", CreatedAt: 1})
	if err != nil {
		t.Fatalf("first AddThreadNext: %v", err)
	}
	if first.TID != "t1" {
		t.Fatalf("first TID = %q, want t1", first.TID)
	}
	if _, err := s.AddThreadNext(Thread{JobID: jobID, Model: "claude-opus-5", CreatedAt: 2}); err == nil {
		t.Fatal("AddThreadNext accepted the exact id \"claude-opus-5\" — aliases only")
	}
	second, err := s.AddThreadNext(Thread{JobID: jobID, Model: "fable", CreatedAt: 3})
	if err != nil {
		t.Fatalf("second AddThreadNext: %v", err)
	}
	if second.TID != "t2" {
		t.Fatalf("second TID = %q, want t2 — the refused call burned a tid", second.TID)
	}
	if n := edgeThreadRowCount(t, s, jobID); n != 2 {
		t.Fatalf("%d thread rows, want 2", n)
	}
}
