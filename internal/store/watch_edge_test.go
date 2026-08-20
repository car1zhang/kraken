package store

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// watch_edge_test.go — the EDGE dimension of the watchlist's v29 shape:
// boundaries, error paths, refusals and degenerate inputs. Happy paths and
// invariants live in watch_contract_test.go; nothing here duplicates them.
//
// It replaces the v16-era file of the same name for the reason that file's
// sibling states: the overlay design it was written against is retired, and
// its promote/episode/endpoint-fallback assertions no longer have an
// observable consequence once v29 empties the graph they read.
//
// Every package-level identifier is prefixed `edgeWatch` — this package's test
// namespace is shared with a dozen sibling files.

// --- helpers ---------------------------------------------------------------

// edgeWatchAdd records one observation and fails the test if it folded, so a
// fixture that accidentally collides with an earlier one is a loud setup
// failure rather than a confusing assertion failure three lines later.
func edgeWatchAdd(t *testing.T, s *Store, observation string) int64 {
	t.Helper()
	id, folded, err := s.AddWatchItem(WatchItem{Observation: observation})
	if err != nil {
		t.Fatalf("AddWatchItem(%q): %v", observation, err)
	}
	if folded {
		t.Fatalf("AddWatchItem(%q) folded onto an existing row", observation)
	}
	return id
}

func edgeWatchGet(t *testing.T, s *Store, id int64) WatchItem {
	t.Helper()
	w, err := s.GetWatchItem(id)
	if err != nil {
		t.Fatalf("GetWatchItem(%d): %v", id, err)
	}
	return w
}

// --- refusals --------------------------------------------------------------

// TestEdgeWatchVerbsErrNotFoundOnUnknownID: every single-row verb answers
// ErrNotFound for an id no row carries, errors.Is-comparable, so the API layer
// can turn it into a 404 rather than a 500 without substring-matching a
// message.
func TestEdgeWatchVerbsErrNotFoundOnUnknownID(t *testing.T) {
	s := openTest(t)
	const missing = 4242

	if _, err := s.GetWatchItem(missing); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetWatchItem on an unknown id = %v, want ErrNotFound", err)
	}
	if err := s.UpdateWatchItem(missing, WatchItem{Class: "waste"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("UpdateWatchItem on an unknown id = %v, want ErrNotFound", err)
	}
	if err := s.SetWatchVerdict(missing, "fine", "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetWatchVerdict on an unknown id = %v, want ErrNotFound", err)
	}
	if err := s.ExpireWatchItem(missing, 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("ExpireWatchItem on an unknown id = %v, want ErrNotFound", err)
	}
}

// TestEdgeWatchStateAndVerdictValidation: both closed enums are policed, and
// each refusal names the accepted set.
//
// state is the one a QUERY branches on, so an unvalidated typo does not
// produce a visibly wrong row — it produces an invisible one, stored and
// absent from the only listing the board renders.
func TestEdgeWatchStateAndVerdictValidation(t *testing.T) {
	s := openTest(t)
	id := edgeWatchAdd(t, s, "an observation")

	if _, _, err := s.AddWatchItem(WatchItem{Observation: "fresh", State: "wat ching"}); err == nil {
		t.Errorf("AddWatchItem accepted a bad state")
	} else if !strings.Contains(err.Error(), "watching") {
		t.Errorf("state refusal %q does not name the accepted set", err)
	}
	if err := s.UpdateWatchItem(id, WatchItem{State: "nope"}); err == nil {
		t.Errorf("UpdateWatchItem accepted a bad state")
	}
	if _, err := s.ListWatchItems("nope"); err == nil {
		t.Errorf("ListWatchItems accepted a bad state filter")
	}
	if err := s.SetWatchVerdict(id, "maybe", "x"); err == nil {
		t.Errorf("SetWatchVerdict accepted a bad verdict")
	} else if !strings.Contains(err.Error(), "fine") {
		t.Errorf("verdict refusal %q does not name the accepted set", err)
	}

	// A refused write leaves the row exactly as it was — the refusals above
	// run before any statement, so none of them half-wrote anything.
	w := edgeWatchGet(t, s, id)
	if w.State != "watching" || w.Verdict != "" {
		t.Errorf("a refused write changed the row: %+v", w)
	}
}

// --- the fold's boundaries -------------------------------------------------

// TestEdgeWatchFoldIsByteIdentical: the fold key is the observation's exact
// bytes, so a case or whitespace variant is a DIFFERENT observation and gets
// its own row.
//
// This is a deliberate narrowing of the v16 era's normalize-then-case-fold
// rule, and the cost is real — an LLM writing "Hermes agents…" one night and
// "hermes agents…" the next lands two rows. It is priced that way on purpose:
// the matching side is now a plain column this package owns end to end, so
// there is exactly one string a caller can pass to hit an existing row, with
// no normalization rule for a caller (or a future migration) to reproduce
// slightly differently.
func TestEdgeWatchFoldIsByteIdentical(t *testing.T) {
	s := openTest(t)
	base := edgeWatchAdd(t, s, "hermes re-reads the contract")

	for _, variant := range []string{
		"Hermes re-reads the contract",  // case
		"hermes  re-reads the contract", // doubled space
		"hermes re-reads the contract ", // trailing space
	} {
		id, folded, err := s.AddWatchItem(WatchItem{Observation: variant})
		if err != nil {
			t.Fatalf("AddWatchItem(%q): %v", variant, err)
		}
		if folded || id == base {
			t.Errorf("variant %q folded onto the base row; the fold key is byte-identical", variant)
		}
	}
	if n := len(mustListWatch(t, s)); n != 4 {
		t.Errorf("want four distinct rows, got %d", n)
	}
}

// TestEdgeWatchFoldNeverLandsOnAnExpiredRow: retiring an observation and then
// seeing it again is a NEW observation.
//
// Folding onto the closed row would bury the fresh sighting inside a row carl
// has taken off the board — the count would go up somewhere he is not looking,
// which is worse than either alternative.
func TestEdgeWatchFoldNeverLandsOnAnExpiredRow(t *testing.T) {
	s := openTest(t)
	old := edgeWatchAdd(t, s, "a recurring pattern")
	if err := s.ExpireWatchItem(old, 100); err != nil {
		t.Fatalf("ExpireWatchItem: %v", err)
	}

	fresh, folded, err := s.AddWatchItem(WatchItem{Observation: "a recurring pattern"})
	if err != nil {
		t.Fatalf("AddWatchItem after expiry: %v", err)
	}
	if folded || fresh == old {
		t.Fatalf("folded onto an expired row (id %d)", old)
	}
	if got := edgeWatchGet(t, s, old); got.Sightings != 1 || got.ExpiredAt != 100 {
		t.Errorf("the expired row was touched: %+v", got)
	}
}

// TestEdgeWatchFoldPreservesFirstSeenAndTheTrio: a fold moves the two columns
// it is for and nothing else it has no business moving.
//
// first_seen is the whole point of a first-seen column, and the carl-owned
// trio survives a fold for exactly the reason it survives an UpdateWatchItem —
// the fold IS an agent write.
func TestEdgeWatchFoldPreservesFirstSeenAndTheTrio(t *testing.T) {
	s := openTest(t)
	id := edgeWatchAdd(t, s, "an observation seen twice")
	if err := s.SetWatchVerdict(id, "fine", "already ruled on"); err != nil {
		t.Fatalf("SetWatchVerdict: %v", err)
	}
	if err := s.UpdateWatchItem(id, WatchItem{FirstSeen: 111, LastSeen: 222}); err != nil {
		t.Fatalf("UpdateWatchItem: %v", err)
	}
	before := edgeWatchGet(t, s, id)

	if _, folded, err := s.AddWatchItem(WatchItem{Observation: "an observation seen twice"}); err != nil {
		t.Fatalf("AddWatchItem (fold): %v", err)
	} else if !folded {
		t.Fatalf("want a fold")
	}

	after := edgeWatchGet(t, s, id)
	if after.FirstSeen != 111 {
		t.Errorf("first_seen moved on a fold: %d", after.FirstSeen)
	}
	if after.LastSeen <= 222 {
		t.Errorf("last_seen did not move forward on a fold: %d", after.LastSeen)
	}
	if after.Verdict != before.Verdict || after.Comment != before.Comment || after.CommentedAt != before.CommentedAt {
		t.Errorf("a fold touched carl's trio: %+v", after)
	}
	// A `declined` row folds like any other — finding it is precisely how the
	// caller learns the observation has been ruled on.
	if after.Sightings != 2 {
		t.Errorf("sightings = %d after one fold, want 2", after.Sightings)
	}
}

// TestEdgeWatchAddIgnoresCallerCounters: on a CREATE the counters belong to
// this package, not to the caller.
//
// A caller-supplied sightings would let one restatement claim a recurrence
// that never happened, and a first_seen after last_seen — trivially producible
// by a caller filling in both — reads downstream as a negative lifetime with
// nothing anywhere erroring.
func TestEdgeWatchAddIgnoresCallerCounters(t *testing.T) {
	s := openTest(t)
	before := time.Now().UnixMilli()
	id, _, err := s.AddWatchItem(WatchItem{
		Observation: "counters are the store's",
		Sightings:   99, FirstSeen: 5000, LastSeen: 1000, UpdatedAt: 7, CreatedAt: 8, ExpiredAt: 9,
	})
	if err != nil {
		t.Fatalf("AddWatchItem: %v", err)
	}
	after := time.Now().UnixMilli()

	w := edgeWatchGet(t, s, id)
	if w.Sightings != 1 {
		t.Errorf("sightings = %d on a create, want 1", w.Sightings)
	}
	if w.FirstSeen != w.LastSeen {
		t.Errorf("a first sighting is its own last: first=%d last=%d", w.FirstSeen, w.LastSeen)
	}
	if w.FirstSeen < before || w.FirstSeen > after {
		t.Errorf("first_seen = %d, want a stamp from this moment (%d..%d)", w.FirstSeen, before, after)
	}
	if w.ExpiredAt != 0 {
		t.Errorf("a fresh row must be live, expired_at = %d", w.ExpiredAt)
	}
	if w.CreatedAt < before || w.CreatedAt > after {
		t.Errorf("created_at = %d, want a stamp from this moment", w.CreatedAt)
	}
	if w.Verdict != "" || w.Comment != "" || w.CommentedAt != "" {
		t.Errorf("a fresh row starts unjudged: %+v", w)
	}
}

// --- update / verdict boundaries -------------------------------------------

// TestEdgeWatchUpdateZeroMeansLeaveAlone walks the partial-update rule field
// by field: an entirely zero-valued WatchItem changes nothing except
// updated_at, which is the one column always written.
//
// The failure this guards is silent by construction — a verbatim write stores
// state=” and sightings=0, neither errors, and the row simply stops appearing
// in the only listing the board renders.
func TestEdgeWatchUpdateZeroMeansLeaveAlone(t *testing.T) {
	s := openTest(t)
	id := edgeWatchAdd(t, s, "a fully populated row")
	if err := s.UpdateWatchItem(id, WatchItem{
		Class: "waste", State: "shipped", Sightings: 6, FirstSeen: 10, LastSeen: 20,
		Evidence: "e", SuggestedFix: "f", PRURL: "p", UpdatedAt: 30,
	}); err != nil {
		t.Fatalf("UpdateWatchItem (populate): %v", err)
	}
	before := edgeWatchGet(t, s, id)

	if err := s.UpdateWatchItem(id, WatchItem{}); err != nil {
		t.Fatalf("UpdateWatchItem (empty): %v", err)
	}
	after := edgeWatchGet(t, s, id)

	if after.Class != before.Class || after.State != before.State || after.Sightings != before.Sightings ||
		after.FirstSeen != before.FirstSeen || after.LastSeen != before.LastSeen ||
		after.Evidence != before.Evidence || after.SuggestedFix != before.SuggestedFix ||
		after.PRURL != before.PRURL {
		t.Errorf("an empty update wrote zeros:\nbefore %+v\nafter  %+v", before, after)
	}
	if after.UpdatedAt == before.UpdatedAt {
		t.Errorf("updated_at is written even by an update that changed nothing else")
	}
}

// TestEdgeWatchUpdateReachesAnExpiredRow: closing a row does not freeze it.
// The nightly pass may still be recording what it learned about an observation
// carl retired, and refusing that write would lose the note with no
// alternative path to store it.
func TestEdgeWatchUpdateReachesAnExpiredRow(t *testing.T) {
	s := openTest(t)
	id := edgeWatchAdd(t, s, "a retired observation")
	if err := s.ExpireWatchItem(id, 100); err != nil {
		t.Fatalf("ExpireWatchItem: %v", err)
	}
	if err := s.UpdateWatchItem(id, WatchItem{PRURL: "https://example/pr/2"}); err != nil {
		t.Errorf("UpdateWatchItem on an expired row: %v", err)
	}
	if w := edgeWatchGet(t, s, id); w.PRURL != "https://example/pr/2" || w.ExpiredAt != 100 {
		t.Errorf("expired row after update: %+v", w)
	}
}

// TestEdgeWatchVerdictClearReopensTheRow: the documented undo. An empty
// verdict AND an empty comment clears all three columns, stamp included — a
// timestamp left standing beside no verdict would read as a judgement whose
// text had gone missing.
func TestEdgeWatchVerdictClearReopensTheRow(t *testing.T) {
	s := openTest(t)
	id := edgeWatchAdd(t, s, "an observation to judge and unjudge")

	if err := s.SetWatchVerdict(id, "fix", "do it"); err != nil {
		t.Fatalf("SetWatchVerdict: %v", err)
	}
	if w := edgeWatchGet(t, s, id); w.CommentedAt == "" {
		t.Fatalf("a real verdict must carry a stamp: %+v", w)
	}
	if err := s.SetWatchVerdict(id, "", ""); err != nil {
		t.Fatalf("SetWatchVerdict (clear): %v", err)
	}
	w := edgeWatchGet(t, s, id)
	if w.Verdict != "" || w.Comment != "" || w.CommentedAt != "" {
		t.Errorf("the clear left something behind: %+v", w)
	}
}

// --- expiry boundaries -----------------------------------------------------

// TestEdgeWatchExpireIsIdempotentAndKeepsTheFirstStamp: a second close is a
// silent success that overwrites nothing.
//
// The first close is when it actually happened; a second call moving that
// moment to whenever a client double-clicked would rewrite history for no
// caller's benefit.
func TestEdgeWatchExpireIsIdempotentAndKeepsTheFirstStamp(t *testing.T) {
	s := openTest(t)
	id := edgeWatchAdd(t, s, "closed twice")

	if err := s.ExpireWatchItem(id, 100); err != nil {
		t.Fatalf("ExpireWatchItem: %v", err)
	}
	if err := s.ExpireWatchItem(id, 900); err != nil {
		t.Errorf("a repeat expiry must succeed: %v", err)
	}
	if w := edgeWatchGet(t, s, id); w.ExpiredAt != 100 {
		t.Errorf("expired_at = %d, want the FIRST close's stamp", w.ExpiredAt)
	}
}

// TestEdgeWatchExpireZeroStampsNow: `at == 0` is this package's timestamp
// convention, and here it is load-bearing rather than a convenience — a zero
// left in the column reads as live, so the close would be a silent no-op.
func TestEdgeWatchExpireZeroStampsNow(t *testing.T) {
	s := openTest(t)
	id := edgeWatchAdd(t, s, "closed with no stamp")
	before := time.Now().UnixMilli()
	if err := s.ExpireWatchItem(id, 0); err != nil {
		t.Fatalf("ExpireWatchItem: %v", err)
	}
	after := time.Now().UnixMilli()

	w := edgeWatchGet(t, s, id)
	if w.ExpiredAt < before || w.ExpiredAt > after {
		t.Errorf("expired_at = %d, want a stamp from this moment (%d..%d)", w.ExpiredAt, before, after)
	}
	if len(mustListWatch(t, s)) != 0 {
		t.Errorf("a row closed with at==0 is still listed as live")
	}
}

// --- migration boundaries --------------------------------------------------

// TestEdgeWatchMigrateV29LeavesOverlaysOnDeadEdgesBehind: an overlay whose
// edge was invalidated is NOT carried across.
//
// Every read in the v16 design INNER JOINed the live edge, so such a row was
// already invisible to the board and to the dream loop. Carrying it would
// RESURRECT an observation kraken had unlearned — the one thing a migration
// moving rows sideways must not do.
func TestEdgeWatchMigrateV29LeavesOverlaysOnDeadEdgesBehind(t *testing.T) {
	s := openTest(t)
	downgradeFreshDB(t, s, 28)

	live := contractWatchOverlay(t, s, "a", "b", "observed", "a live observation", WatchItem{
		State: "watching", Sightings: 1, FirstSeen: 10, LastSeen: 10,
	})
	dead := contractWatchOverlay(t, s, "c", "d", "observed", "an unlearned observation", WatchItem{
		State: "watching", Sightings: 1, FirstSeen: 20, LastSeen: 20,
	})
	if _, err := s.db.Exec(`UPDATE kg_edges SET expired_at = 999 WHERE id = ?`, dead); err != nil {
		t.Fatalf("expire the edge: %v", err)
	}
	_ = live

	if err := s.migrate(); err != nil {
		t.Fatalf("migrate into v29: %v", err)
	}

	got := contractWatchByObservation(t, s, "")
	if len(got) != 1 {
		t.Fatalf("want only the live overlay carried, got %d: %+v", len(got), got)
	}
	if _, ok := got["a live observation"]; !ok {
		t.Errorf("the live overlay was not carried: %+v", got)
	}
	if _, ok := got["an unlearned observation"]; ok {
		t.Errorf("an overlay on an invalidated edge was resurrected")
	}
}

// TestEdgeWatchMigrateV29OnAnEmptyGraph: a database with no watched facts at
// all migrates cleanly and lands on an empty table.
//
// The degenerate case is worth pinning because the hook's read runs
// unconditionally: a nil row set has to reach the wipe rather than short-
// circuit past it.
func TestEdgeWatchMigrateV29OnAnEmptyGraph(t *testing.T) {
	s := openTest(t)
	downgradeFreshDB(t, s, 28)
	if err := s.migrate(); err != nil {
		t.Fatalf("migrate a v28 database with no overlays: %v", err)
	}
	list, err := s.ListWatchItems("")
	if err != nil {
		t.Fatalf("ListWatchItems: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("want an empty table, got %+v", list)
	}
	if contractWatchTableExists(t, s, "kg_watch") {
		t.Errorf("kg_watch survived a migration with nothing to carry")
	}
}

// mustListWatch is the live listing, hoisted so the assertions above read as
// one line rather than four.
func mustListWatch(t *testing.T, s *Store) []WatchItem {
	t.Helper()
	list, err := s.ListWatchItems("")
	if err != nil {
		t.Fatalf("ListWatchItems: %v", err)
	}
	return list
}
