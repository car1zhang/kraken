package store

import "testing"

// watchfold_contract_test.go — the LOGIC dimension of slice B of the
// dream-2026-09-27 contract: AddWatchItem's state handling on create and on
// fold. Written blind from 50-contract.md; the refusal paths (an invalid
// non-empty state on create or fold) belong to watchfold_edge_test.go.
//
// Every package-level identifier is prefixed `wfLogic` — this package's test
// namespace is shared with many sibling files.

// wfLogicStates is the contract's exact set of valid states.
var wfLogicStates = []string{"watching", "shipped", "declined"}

// wfLogicCreate adds a fresh row carrying `state` and requires it to be a
// create, returning its id.
func wfLogicCreate(t *testing.T, s *Store, obs, state string) int64 {
	t.Helper()
	id, folded, err := s.AddWatchItem(WatchItem{
		Observation: obs,
		Src:         "zeus", Dst: "watchlist", Predicate: "folds",
		Class: "correction", State: state,
		Evidence: `{"n":1}`, SuggestedFix: "first fix", PRURL: "https://example.invalid/pr/1",
	})
	if err != nil {
		t.Fatalf("AddWatchItem (create, state %q): %v", state, err)
	}
	if folded {
		t.Fatalf("AddWatchItem (create, state %q) reported folded=true for a new observation", state)
	}
	return id
}

func wfLogicGet(t *testing.T, s *Store, id int64) WatchItem {
	t.Helper()
	w, err := s.GetWatchItem(id)
	if err != nil {
		t.Fatalf("GetWatchItem(%d): %v", id, err)
	}
	return w
}

// TestWatchFoldCreateEmptyStateDefaultsToWatching pins: CREATE with
// w.State == "" → the new row's state is `watching`.
func TestWatchFoldCreateEmptyStateDefaultsToWatching(t *testing.T) {
	s := openTest(t)
	id := wfLogicCreate(t, s, "a create with no state", "")
	if got := wfLogicGet(t, s, id).State; got != "watching" {
		t.Errorf("state = %q, want watching", got)
	}
}

// TestWatchFoldCreateValidStateStoredAsGiven pins: CREATE with a valid
// non-empty state → stored as given.
func TestWatchFoldCreateValidStateStoredAsGiven(t *testing.T) {
	for _, state := range wfLogicStates {
		t.Run(state, func(t *testing.T) {
			s := openTest(t)
			id := wfLogicCreate(t, s, "a create carrying "+state, state)
			if got := wfLogicGet(t, s, id).State; got != state {
				t.Errorf("state = %q, want %q", got, state)
			}
		})
	}
}

// TestWatchFoldEmptyStateKeepsRowState pins the slice's headline obligation:
// FOLD with w.State == "" → the row's state is UNCHANGED, for each of
// `watching`, `shipped` and `declined`.
func TestWatchFoldEmptyStateKeepsRowState(t *testing.T) {
	for _, state := range wfLogicStates {
		t.Run(state, func(t *testing.T) {
			s := openTest(t)
			obs := "a row sitting at " + state
			id := wfLogicCreate(t, s, obs, state)
			if got := wfLogicGet(t, s, id).State; got != state {
				t.Fatalf("precondition: created state = %q, want %q", got, state)
			}

			gotID, folded, err := s.AddWatchItem(WatchItem{Observation: obs, Evidence: `{"n":2}`})
			if err != nil {
				t.Fatalf("AddWatchItem (fold, no state): %v", err)
			}
			if !folded || gotID != id {
				t.Fatalf("fold returned (id=%d, folded=%v), want (%d, true)", gotID, folded, id)
			}
			if got := wfLogicGet(t, s, id).State; got != state {
				t.Errorf("state after a stateless fold = %q, want unchanged %q", got, state)
			}
		})
	}
}

// TestWatchFoldEmptyStateKeepsStateSetByUpdate pins the same obligation for a
// row whose state was moved by UpdateWatchItem rather than set at create —
// "whatever it was" covers a state the row reached after creation.
func TestWatchFoldEmptyStateKeepsStateSetByUpdate(t *testing.T) {
	for _, state := range []string{"shipped", "declined"} {
		t.Run(state, func(t *testing.T) {
			s := openTest(t)
			obs := "a row moved to " + state
			id := wfLogicCreate(t, s, obs, "")
			if err := s.UpdateWatchItem(id, WatchItem{State: state}); err != nil {
				t.Fatalf("UpdateWatchItem: %v", err)
			}
			if got := wfLogicGet(t, s, id).State; got != state {
				t.Fatalf("precondition: state after update = %q, want %q", got, state)
			}
			if _, _, err := s.AddWatchItem(WatchItem{Observation: obs}); err != nil {
				t.Fatalf("AddWatchItem (fold, no state): %v", err)
			}
			if got := wfLogicGet(t, s, id).State; got != state {
				t.Errorf("state after a stateless fold = %q, want unchanged %q", got, state)
			}
		})
	}
}

// TestWatchFoldValidStateReplacesRowState pins: FOLD with a valid non-empty
// state → it replaces the row's state, including a move back to watching.
func TestWatchFoldValidStateReplacesRowState(t *testing.T) {
	cases := []struct{ from, to string }{
		{"watching", "shipped"},
		{"watching", "declined"},
		{"shipped", "declined"},
		{"shipped", "watching"},
		{"declined", "watching"},
		{"declined", "shipped"},
	}
	for _, c := range cases {
		t.Run(c.from+"->"+c.to, func(t *testing.T) {
			s := openTest(t)
			obs := "a fold moving " + c.from + " to " + c.to
			id := wfLogicCreate(t, s, obs, c.from)
			gotID, folded, err := s.AddWatchItem(WatchItem{Observation: obs, State: c.to})
			if err != nil {
				t.Fatalf("AddWatchItem (fold, state %q): %v", c.to, err)
			}
			if !folded || gotID != id {
				t.Fatalf("fold returned (id=%d, folded=%v), want (%d, true)", gotID, folded, id)
			}
			if got := wfLogicGet(t, s, id).State; got != c.to {
				t.Errorf("state = %q, want %q", got, c.to)
			}
		})
	}
}

// TestWatchFoldCreateReturnsNewIDNotFolded pins the create half of the return
// contract: a non-zero id naming the row just written, folded == false.
func TestWatchFoldCreateReturnsNewIDNotFolded(t *testing.T) {
	s := openTest(t)
	id, folded, err := s.AddWatchItem(WatchItem{Observation: "brand new", Class: "waste"})
	if err != nil {
		t.Fatalf("AddWatchItem: %v", err)
	}
	if folded {
		t.Errorf("folded = true on a create")
	}
	if id == 0 {
		t.Fatalf("id = 0 on a create")
	}
	if got := wfLogicGet(t, s, id); got.Observation != "brand new" || got.Sightings != 1 {
		t.Errorf("row %d = {observation %q, sightings %d}, want {\"brand new\", 1}", id, got.Observation, got.Sightings)
	}
}

// TestWatchFoldReturnsExistingIDAndFolded pins: a fold returns the existing
// row's id and folded == true, and writes no second row (a different
// observation still creates a distinct row).
func TestWatchFoldReturnsExistingIDAndFolded(t *testing.T) {
	s := openTest(t)
	id := wfLogicCreate(t, s, "the same words", "shipped")
	other := wfLogicCreate(t, s, "different words", "")
	if other == id {
		t.Fatalf("two distinct observations share id %d", id)
	}
	for i := 0; i < 2; i++ {
		gotID, folded, err := s.AddWatchItem(WatchItem{Observation: "the same words"})
		if err != nil {
			t.Fatalf("fold %d: %v", i+1, err)
		}
		if gotID != id {
			t.Errorf("fold %d returned id %d, want the existing %d", i+1, gotID, id)
		}
		if !folded {
			t.Errorf("fold %d returned folded=false", i+1)
		}
	}
	if got := wfLogicGet(t, s, id).Sightings; got != 3 {
		t.Errorf("sightings after two folds = %d, want 3", got)
	}
	if got := wfLogicGet(t, s, other).Sightings; got != 1 {
		t.Errorf("the other row's sightings = %d, want 1 (untouched)", got)
	}
}

// TestWatchFoldEmptyFieldsLeaveColumnsAlone pins: sightings + 1, and
// Class/Evidence/SuggestedFix/PRURL are NOT overwritten when the fold sends
// them empty (and the state stays, the slice's own obligation).
func TestWatchFoldEmptyFieldsLeaveColumnsAlone(t *testing.T) {
	s := openTest(t)
	obs := "a bare fold"
	id := wfLogicCreate(t, s, obs, "declined")
	before := wfLogicGet(t, s, id)

	if _, _, err := s.AddWatchItem(WatchItem{Observation: obs}); err != nil {
		t.Fatalf("AddWatchItem (bare fold): %v", err)
	}
	after := wfLogicGet(t, s, id)
	if after.Sightings != before.Sightings+1 {
		t.Errorf("sightings = %d, want %d", after.Sightings, before.Sightings+1)
	}
	if after.Class != before.Class {
		t.Errorf("class = %q, want %q", after.Class, before.Class)
	}
	if after.Evidence != before.Evidence {
		t.Errorf("evidence = %q, want %q", after.Evidence, before.Evidence)
	}
	if after.SuggestedFix != before.SuggestedFix {
		t.Errorf("suggested_fix = %q, want %q", after.SuggestedFix, before.SuggestedFix)
	}
	if after.PRURL != before.PRURL {
		t.Errorf("pr_url = %q, want %q", after.PRURL, before.PRURL)
	}
	if after.State != "declined" {
		t.Errorf("state = %q, want declined", after.State)
	}
}

// TestWatchFoldNonEmptyFieldsOverwrite pins: Class/Evidence/SuggestedFix/PRURL
// are overwritten when the fold sends them non-empty, sightings + 1, and a
// stateless fold carrying them still keeps the state.
func TestWatchFoldNonEmptyFieldsOverwrite(t *testing.T) {
	s := openTest(t)
	obs := "a full fold"
	id := wfLogicCreate(t, s, obs, "shipped")
	before := wfLogicGet(t, s, id)

	if _, _, err := s.AddWatchItem(WatchItem{
		Observation: obs, Class: "waste", Evidence: `{"n":2}`,
		SuggestedFix: "second fix", PRURL: "https://example.invalid/pr/2",
	}); err != nil {
		t.Fatalf("AddWatchItem (full fold): %v", err)
	}
	after := wfLogicGet(t, s, id)
	if after.Sightings != before.Sightings+1 {
		t.Errorf("sightings = %d, want %d", after.Sightings, before.Sightings+1)
	}
	if after.Class != "waste" {
		t.Errorf("class = %q, want waste", after.Class)
	}
	if after.Evidence != `{"n":2}` {
		t.Errorf("evidence = %q, want the fold's", after.Evidence)
	}
	if after.SuggestedFix != "second fix" {
		t.Errorf("suggested_fix = %q, want the fold's", after.SuggestedFix)
	}
	if after.PRURL != "https://example.invalid/pr/2" {
		t.Errorf("pr_url = %q, want the fold's", after.PRURL)
	}
	if after.State != "shipped" {
		t.Errorf("state = %q, want shipped (no state sent)", after.State)
	}
}

// TestWatchFoldNeverTouchesCarlTrio pins: Verdict/Comment/CommentedAt are
// never touched by a fold — neither a stateless one nor one that changes the
// state, even when the fold's own WatchItem carries values for them.
func TestWatchFoldNeverTouchesCarlTrio(t *testing.T) {
	for _, foldState := range []string{"", "watching"} {
		t.Run("state="+foldState, func(t *testing.T) {
			s := openTest(t)
			obs := "carl ruled on this"
			id := wfLogicCreate(t, s, obs, "shipped")
			if err := s.SetWatchVerdict(id, "fix", "worth doing"); err != nil {
				t.Fatalf("SetWatchVerdict: %v", err)
			}
			before := wfLogicGet(t, s, id)
			if before.Verdict != "fix" || before.Comment != "worth doing" || before.CommentedAt == "" {
				t.Fatalf("precondition: trio = (%q, %q, %q)", before.Verdict, before.Comment, before.CommentedAt)
			}

			if _, _, err := s.AddWatchItem(WatchItem{
				Observation: obs, State: foldState, Evidence: `{"n":2}`,
				Verdict: "fine", Comment: "agent-written", CommentedAt: "2000-01-01T00:00:00Z",
			}); err != nil {
				t.Fatalf("AddWatchItem (fold): %v", err)
			}
			after := wfLogicGet(t, s, id)
			if after.Verdict != before.Verdict || after.Comment != before.Comment || after.CommentedAt != before.CommentedAt {
				t.Errorf("trio = (%q, %q, %q), want unchanged (%q, %q, %q)",
					after.Verdict, after.Comment, after.CommentedAt,
					before.Verdict, before.Comment, before.CommentedAt)
			}
			wantState := "shipped"
			if foldState != "" {
				wantState = foldState
			}
			if after.State != wantState {
				t.Errorf("state = %q, want %q", after.State, wantState)
			}
		})
	}
}
