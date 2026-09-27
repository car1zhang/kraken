package store

import (
	"reflect"
	"testing"
)

// watchfold_edge_test.go — the EDGE dimension of slice B (dream-2026-09-27):
// AddWatchItem's state handling on create vs fold. Boundaries, refusals and
// the rows a fold must never disturb. Authored blind from 50-contract.md.
//
// Every package-level identifier is prefixed `edgeFold` — the package's test
// namespace is shared with many sibling files.

// edgeFoldInvalidStates differ from the three valid spellings only by case or
// surrounding whitespace. Each is non-empty and not byte-equal to a valid
// state, so the contract makes each one a refusal on both arms.
var edgeFoldInvalidStates = []string{
	"Watching", "WATCHING", "Shipped", "SHIPPED", "Declined", "DECLINED",
	" watching", "watching ", "shipped\n", "\tdeclined", " ",
}

func edgeFoldGet(t *testing.T, s *Store, id int64) WatchItem {
	t.Helper()
	w, err := s.GetWatchItem(id)
	if err != nil {
		t.Fatalf("GetWatchItem(%d): %v", id, err)
	}
	return w
}

// edgeFoldSeed creates one row with the given observation, then moves its
// state with UpdateWatchItem (an existing verb, so the seed never leans on the
// create arm under test) when state is non-empty.
func edgeFoldSeed(t *testing.T, s *Store, observation, state string) int64 {
	t.Helper()
	id, folded, err := s.AddWatchItem(WatchItem{
		Observation: observation, Src: "zeus", Dst: "carl", Predicate: "observed",
		Class: "waste", Evidence: `{"seed":true}`, SuggestedFix: "seed fix", PRURL: "https://example/pr/9",
	})
	if err != nil {
		t.Fatalf("seed AddWatchItem(%q): %v", observation, err)
	}
	if folded {
		t.Fatalf("seed AddWatchItem(%q) folded onto an existing row", observation)
	}
	if state != "" {
		if err := s.UpdateWatchItem(id, WatchItem{State: state}); err != nil {
			t.Fatalf("seed UpdateWatchItem(state=%q): %v", state, err)
		}
		if got := edgeFoldGet(t, s, id).State; got != state {
			t.Fatalf("seed state = %q, want %q", got, state)
		}
	}
	return id
}

// TestEdgeFoldCreateInvalidStateWritesNoRow: an invalid non-empty state on a
// CREATE is refused with an error and no row is written — proven by a later
// valid add of the same observation creating (folded=false, 1 sighting)
// rather than folding onto a leaked row.
func TestEdgeFoldCreateInvalidStateWritesNoRow(t *testing.T) {
	for _, bad := range edgeFoldInvalidStates {
		t.Run(bad, func(t *testing.T) {
			s := openTest(t)
			obs := "an observation refused on create"
			if _, _, err := s.AddWatchItem(WatchItem{Observation: obs, State: bad}); err == nil {
				t.Fatalf("AddWatchItem(create, state=%q) = nil error, want a refusal", bad)
			}
			id, folded, err := s.AddWatchItem(WatchItem{Observation: obs})
			if err != nil {
				t.Fatalf("follow-up AddWatchItem: %v", err)
			}
			if folded {
				t.Fatalf("follow-up add folded: the refused create (state=%q) wrote a row", bad)
			}
			got := edgeFoldGet(t, s, id)
			if got.Sightings != 1 {
				t.Errorf("follow-up row sightings = %d, want 1", got.Sightings)
			}
			if got.State != "watching" {
				t.Errorf("follow-up row state = %q, want watching", got.State)
			}
		})
	}
}

// TestEdgeFoldInvalidStateLeavesRowIdentical: an invalid non-empty state on a
// FOLD is refused with an error and the existing row is untouched — every
// column, compared as the whole WatchItem before and after, including the
// carl-owned verdict trio.
func TestEdgeFoldInvalidStateLeavesRowIdentical(t *testing.T) {
	for _, bad := range edgeFoldInvalidStates {
		t.Run(bad, func(t *testing.T) {
			s := openTest(t)
			obs := "an observation refused on fold"
			id := edgeFoldSeed(t, s, obs, "shipped")
			if err := s.SetWatchVerdict(id, "fix", "worth doing"); err != nil {
				t.Fatalf("SetWatchVerdict: %v", err)
			}
			before := edgeFoldGet(t, s, id)

			if _, _, err := s.AddWatchItem(WatchItem{
				Observation: obs, State: bad, Class: "correction",
				Evidence: `{"fold":true}`, SuggestedFix: "other fix", PRURL: "https://example/pr/10",
			}); err == nil {
				t.Fatalf("AddWatchItem(fold, state=%q) = nil error, want a refusal", bad)
			}

			after := edgeFoldGet(t, s, id)
			if !reflect.DeepEqual(before, after) {
				t.Errorf("refused fold (state=%q) changed the row:\nbefore %+v\nafter  %+v", bad, before, after)
			}
		})
	}
}

// TestEdgeFoldEmptyStateKeepsTerminalState: a fold that sends no state onto a
// `declined` or `shipped` row leaves that state as it was, while still
// counting the sighting and returning the existing id with folded=true.
func TestEdgeFoldEmptyStateKeepsTerminalState(t *testing.T) {
	for _, state := range []string{"declined", "shipped"} {
		t.Run(state, func(t *testing.T) {
			s := openTest(t)
			obs := "an observation already " + state
			id := edgeFoldSeed(t, s, obs, state)
			before := edgeFoldGet(t, s, id)

			gotID, folded, err := s.AddWatchItem(WatchItem{Observation: obs})
			if err != nil {
				t.Fatalf("AddWatchItem(fold, no state): %v", err)
			}
			if !folded {
				t.Fatalf("a byte-identical observation must fold")
			}
			if gotID != id {
				t.Errorf("fold returned id %d, want the existing row's %d", gotID, id)
			}
			after := edgeFoldGet(t, s, id)
			if after.State != state {
				t.Errorf("state after an empty-state fold = %q, want %q unchanged", after.State, state)
			}
			if after.Sightings != before.Sightings+1 {
				t.Errorf("sightings = %d, want %d", after.Sightings, before.Sightings+1)
			}
		})
	}
}

// TestEdgeFoldNeverTouchesCarlVerdict: a fold onto a row carrying a carl
// verdict (set through SetWatchVerdict) leaves Verdict/Comment/CommentedAt
// exactly as they were — on an empty-state fold and on a valid-state fold —
// and the empty-state fold keeps the row's state.
func TestEdgeFoldNeverTouchesCarlVerdict(t *testing.T) {
	s := openTest(t)
	obs := "an observation carl has ruled on"
	id := edgeFoldSeed(t, s, obs, "declined")
	if err := s.SetWatchVerdict(id, "fine", "leave it"); err != nil {
		t.Fatalf("SetWatchVerdict: %v", err)
	}
	before := edgeFoldGet(t, s, id)

	if _, folded, err := s.AddWatchItem(WatchItem{Observation: obs, Evidence: `{"again":1}`}); err != nil || !folded {
		t.Fatalf("empty-state fold: folded=%v err=%v", folded, err)
	}
	mid := edgeFoldGet(t, s, id)
	if mid.State != "declined" {
		t.Errorf("state after an empty-state fold onto a ruled row = %q, want declined", mid.State)
	}
	if mid.Verdict != before.Verdict || mid.Comment != before.Comment || mid.CommentedAt != before.CommentedAt {
		t.Errorf("empty-state fold touched carl's trio: %q/%q/%q, want %q/%q/%q",
			mid.Verdict, mid.Comment, mid.CommentedAt, before.Verdict, before.Comment, before.CommentedAt)
	}

	if _, folded, err := s.AddWatchItem(WatchItem{Observation: obs, State: "watching"}); err != nil || !folded {
		t.Fatalf("valid-state fold: folded=%v err=%v", folded, err)
	}
	after := edgeFoldGet(t, s, id)
	if after.State != "watching" {
		t.Errorf("state after a valid-state fold = %q, want watching", after.State)
	}
	if after.Verdict != before.Verdict || after.Comment != before.Comment || after.CommentedAt != before.CommentedAt {
		t.Errorf("valid-state fold touched carl's trio: %q/%q/%q, want %q/%q/%q",
			after.Verdict, after.Comment, after.CommentedAt, before.Verdict, before.Comment, before.CommentedAt)
	}
}
