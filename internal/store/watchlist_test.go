package store

import (
	"errors"
	"strings"
	"testing"
)

// TestAddWatchItemDefaultsAndRoundTrip pins AddWatchItem's defaults (the ones
// a caller normally leaves to this package) and that everything it did set
// comes back unchanged.
func TestAddWatchItemDefaultsAndRoundTrip(t *testing.T) {
	s := openTest(t)
	id, err := s.AddWatchItem(WatchItem{
		Observation:  "zeus keeps re-reading the same skill file",
		Class:        "waste",
		Evidence:     `{"jobs":["a","b"]}`,
		SuggestedFix: "cache the skill text in the brief",
	})
	if err != nil {
		t.Fatalf("AddWatchItem: %v", err)
	}
	w, err := s.GetWatchItem(id)
	if err != nil {
		t.Fatalf("GetWatchItem: %v", err)
	}
	if w.Observation != "zeus keeps re-reading the same skill file" ||
		w.Class != "waste" || w.Evidence != `{"jobs":["a","b"]}` ||
		w.SuggestedFix != "cache the skill text in the brief" {
		t.Fatalf("round-trip mangled the row: %+v", w)
	}
	if w.State != "watching" {
		t.Fatalf("State = %q, want the watching default", w.State)
	}
	if w.Sightings != 1 {
		t.Fatalf("Sightings = %d, want the 1 default (inserting a row is the first sighting)", w.Sightings)
	}
	if w.FirstSeen == 0 || w.LastSeen == 0 || w.UpdatedAt == 0 {
		t.Fatalf("zero timestamps should be stamped to now: %+v", w)
	}
	// a fresh row starts unjudged — the carl-owned trio is not caller-settable
	if w.Verdict != "" || w.Comment != "" || w.CommentedAt != "" {
		t.Fatalf("a fresh row must start unjudged: %+v", w)
	}
}

// TestAddWatchItemKeepsExplicitValues is the other half of the defaults
// contract: a caller that DOES supply timestamps/sightings/state (an importer,
// a fixture) must get them stored verbatim rather than overwritten by "now".
func TestAddWatchItemKeepsExplicitValues(t *testing.T) {
	s := openTest(t)
	id, err := s.AddWatchItem(WatchItem{
		Observation: "explicit", Class: "feedback", State: "shipped",
		Sightings: 7, FirstSeen: 100, LastSeen: 200, UpdatedAt: 300,
		PRURL: "https://github.com/example/kraken/pull/9",
	})
	if err != nil {
		t.Fatalf("AddWatchItem: %v", err)
	}
	w, err := s.GetWatchItem(id)
	if err != nil {
		t.Fatalf("GetWatchItem: %v", err)
	}
	if w.State != "shipped" || w.Sightings != 7 || w.FirstSeen != 100 ||
		w.LastSeen != 200 || w.UpdatedAt != 300 ||
		w.PRURL != "https://github.com/example/kraken/pull/9" {
		t.Fatalf("explicit values overwritten: %+v", w)
	}
}

// TestAddWatchItemFirstSeenNeverAfterLastSeen pins the ordering invariant a
// replayed row would otherwise violate: a caller that supplies LastSeen and
// leaves FirstSeen zero (an importer, a sweep backfilling a night it missed)
// must NOT get FirstSeen stamped to now, which is after the supplied LastSeen.
// Nothing errors on that shape — it just stores a row whose lifetime is
// negative, which every age calculation downstream reads as nonsense.
func TestAddWatchItemFirstSeenNeverAfterLastSeen(t *testing.T) {
	s := openTest(t)
	id, err := s.AddWatchItem(WatchItem{Observation: "replayed from an old night", LastSeen: 1000})
	if err != nil {
		t.Fatalf("AddWatchItem: %v", err)
	}
	w, err := s.GetWatchItem(id)
	if err != nil {
		t.Fatalf("GetWatchItem: %v", err)
	}
	if w.FirstSeen > w.LastSeen {
		t.Fatalf("FirstSeen (%d) is after LastSeen (%d) — a negative lifetime", w.FirstSeen, w.LastSeen)
	}
	if w.FirstSeen != 1000 {
		t.Fatalf("FirstSeen = %d, want it to default to the supplied LastSeen (1000), not now", w.FirstSeen)
	}

	// and with both left zero, both land on the same "now" — still ordered.
	id2, err := s.AddWatchItem(WatchItem{Observation: "brand new tonight"})
	if err != nil {
		t.Fatalf("AddWatchItem: %v", err)
	}
	w2, err := s.GetWatchItem(id2)
	if err != nil {
		t.Fatalf("GetWatchItem: %v", err)
	}
	if w2.FirstSeen == 0 || w2.FirstSeen > w2.LastSeen {
		t.Fatalf("both-zero case should stamp an ordered pair: %+v", w2)
	}
}

// TestListWatchItemsFilterAndOrder covers both listing modes: "" is every row,
// a state filters to it, and both come back last_seen DESC.
func TestListWatchItemsFilterAndOrder(t *testing.T) {
	s := openTest(t)
	for _, seed := range []WatchItem{
		{Observation: "oldest", State: "watching", FirstSeen: 100, LastSeen: 100},
		{Observation: "newest", State: "watching", FirstSeen: 300, LastSeen: 300},
		{Observation: "middle", State: "shipped", FirstSeen: 200, LastSeen: 200},
	} {
		if _, err := s.AddWatchItem(seed); err != nil {
			t.Fatalf("AddWatchItem %q: %v", seed.Observation, err)
		}
	}

	all, err := s.ListWatchItems("")
	if err != nil {
		t.Fatalf(`ListWatchItems(""): %v`, err)
	}
	if len(all) != 3 {
		t.Fatalf(`ListWatchItems("") returned %d rows, want all 3: %+v`, len(all), all)
	}
	wantOrder := []string{"newest", "middle", "oldest"}
	for i, want := range wantOrder {
		if all[i].Observation != want {
			t.Fatalf("row %d = %q, want %q (last_seen DESC): %+v", i, all[i].Observation, want, all)
		}
	}

	watching, err := s.ListWatchItems("watching")
	if err != nil {
		t.Fatalf(`ListWatchItems("watching"): %v`, err)
	}
	if len(watching) != 2 {
		t.Fatalf("watching rows = %+v, want exactly the two watching ones", watching)
	}
	if watching[0].Observation != "newest" || watching[1].Observation != "oldest" {
		t.Fatalf("filtered list lost its last_seen DESC order: %+v", watching)
	}

	none, err := s.ListWatchItems("declined")
	if err != nil {
		t.Fatalf(`ListWatchItems("declined"): %v`, err)
	}
	if len(none) != 0 {
		t.Fatalf("no row is declined yet, got %+v", none)
	}
}

// TestBumpWatchSightingFoldsIntoExistingRow pins the fold: a repeat sighting
// increments the row that already tracks the observation rather than adding a
// second one, refreshing last_seen/updated_at/evidence while leaving
// first_seen (the whole point of that column) alone.
func TestBumpWatchSightingFoldsIntoExistingRow(t *testing.T) {
	s := openTest(t)
	id, err := s.AddWatchItem(WatchItem{
		Observation: "repeat", Class: "repetition",
		FirstSeen: 100, LastSeen: 100, UpdatedAt: 100,
		Evidence: `{"night":1}`,
	})
	if err != nil {
		t.Fatalf("AddWatchItem: %v", err)
	}
	if err := s.BumpWatchSighting(id, 500, `{"night":2}`); err != nil {
		t.Fatalf("BumpWatchSighting: %v", err)
	}

	all, err := s.ListWatchItems("")
	if err != nil {
		t.Fatalf("ListWatchItems: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("a bump must fold into the existing row, not insert: %+v", all)
	}
	w := all[0]
	if w.Sightings != 2 {
		t.Fatalf("Sightings = %d, want 2", w.Sightings)
	}
	if w.LastSeen != 500 || w.UpdatedAt != 500 {
		t.Fatalf("bump should refresh last_seen/updated_at: %+v", w)
	}
	if w.FirstSeen != 100 {
		t.Fatalf("bump must not touch first_seen: %+v", w)
	}
	if w.Evidence != `{"night":2}` {
		t.Fatalf("evidence should be replaced with the fresh sample, got %q", w.Evidence)
	}

	// a second bump keeps counting from the stored value, not from 1
	if err := s.BumpWatchSighting(id, 600, `{"night":3}`); err != nil {
		t.Fatalf("second BumpWatchSighting: %v", err)
	}
	w2, err := s.GetWatchItem(id)
	if err != nil {
		t.Fatalf("GetWatchItem: %v", err)
	}
	if w2.Sightings != 3 {
		t.Fatalf("Sightings after two bumps = %d, want 3", w2.Sightings)
	}
}

// TestWatchItemColumnOwnership is THE test this file exists for: the agent's
// write (UpdateWatchItem) and carl's write (SetWatchVerdict) touch disjoint
// column sets, so neither can silently erase the other's work. An agent write
// that clobbered carl's comment would leave a row that still looks fine —
// nothing would ever surface the loss — which is why it is pinned here rather
// than left to callers remembering to re-read first.
func TestWatchItemColumnOwnership(t *testing.T) {
	s := openTest(t)
	id, err := s.AddWatchItem(WatchItem{
		Observation: "original observation", Class: "correction", State: "watching",
		Sightings: 2, FirstSeen: 100, LastSeen: 200, UpdatedAt: 200,
		Evidence: `{"v":1}`, SuggestedFix: "original fix",
	})
	if err != nil {
		t.Fatalf("AddWatchItem: %v", err)
	}

	// carl judges the row.
	if err := s.SetWatchVerdict(id, "fix", "yes, do this one", "2026-07-26T00:00:00Z"); err != nil {
		t.Fatalf("SetWatchVerdict: %v", err)
	}

	// the agent then restates the row in full, INCLUDING contradictory values
	// in the carl-owned trio — those three must be ignored.
	if err := s.UpdateWatchItem(id, WatchItem{
		Observation: "refined observation", Class: "waste", State: "shipped",
		Sightings: 5, FirstSeen: 111, LastSeen: 555, UpdatedAt: 555,
		Evidence: `{"v":2}`, SuggestedFix: "refined fix",
		PRURL:   "https://github.com/example/kraken/pull/12",
		Verdict: "fine", Comment: "CLOBBERED", CommentedAt: "1999-01-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("UpdateWatchItem: %v", err)
	}

	w, err := s.GetWatchItem(id)
	if err != nil {
		t.Fatalf("GetWatchItem: %v", err)
	}
	if w.Verdict != "fix" || w.Comment != "yes, do this one" || w.CommentedAt != "2026-07-26T00:00:00Z" {
		t.Fatalf("agent write clobbered carl's verdict/comment/commented_at: %+v", w)
	}
	// ...while every agent-owned column did move.
	if w.Observation != "refined observation" || w.Class != "waste" || w.State != "shipped" ||
		w.Sightings != 5 || w.FirstSeen != 111 || w.LastSeen != 555 || w.UpdatedAt != 555 ||
		w.Evidence != `{"v":2}` || w.SuggestedFix != "refined fix" ||
		w.PRURL != "https://github.com/example/kraken/pull/12" {
		t.Fatalf("agent-owned columns not updated: %+v", w)
	}

	// now the mirror: carl re-judges, and none of the agent-owned columns move.
	if err := s.SetWatchVerdict(id, "fine", "changed my mind", "2026-07-27T00:00:00Z"); err != nil {
		t.Fatalf("second SetWatchVerdict: %v", err)
	}
	w2, err := s.GetWatchItem(id)
	if err != nil {
		t.Fatalf("GetWatchItem: %v", err)
	}
	if w2.Verdict != "fine" || w2.Comment != "changed my mind" || w2.CommentedAt != "2026-07-27T00:00:00Z" {
		t.Fatalf("re-judge didn't take: %+v", w2)
	}
	if w2.Observation != "refined observation" || w2.Class != "waste" || w2.State != "shipped" ||
		w2.Sightings != 5 || w2.FirstSeen != 111 || w2.LastSeen != 555 || w2.UpdatedAt != 555 ||
		w2.Evidence != `{"v":2}` || w2.SuggestedFix != "refined fix" ||
		w2.PRURL != "https://github.com/example/kraken/pull/12" {
		t.Fatalf("carl's write touched agent-owned columns: %+v", w2)
	}

	// a bump is an agent write too — it must preserve the trio as well.
	if err := s.BumpWatchSighting(id, 900, `{"v":3}`); err != nil {
		t.Fatalf("BumpWatchSighting: %v", err)
	}
	w3, err := s.GetWatchItem(id)
	if err != nil {
		t.Fatalf("GetWatchItem: %v", err)
	}
	if w3.Verdict != "fine" || w3.Comment != "changed my mind" || w3.CommentedAt != "2026-07-27T00:00:00Z" {
		t.Fatalf("bump clobbered carl's verdict/comment/commented_at: %+v", w3)
	}
}

// TestSetWatchVerdictEmptyClears pins the documented way to undo a judgement:
// the three columns are written unconditionally, so an empty verdict reopens
// the row rather than being refused.
func TestSetWatchVerdictEmptyClears(t *testing.T) {
	s := openTest(t)
	id, err := s.AddWatchItem(WatchItem{Observation: "clearable"})
	if err != nil {
		t.Fatalf("AddWatchItem: %v", err)
	}
	if err := s.SetWatchVerdict(id, "fix", "do it", "2026-07-26T00:00:00Z"); err != nil {
		t.Fatalf("SetWatchVerdict: %v", err)
	}
	if err := s.SetWatchVerdict(id, "", "", ""); err != nil {
		t.Fatalf("SetWatchVerdict clear: %v", err)
	}
	w, err := s.GetWatchItem(id)
	if err != nil {
		t.Fatalf("GetWatchItem: %v", err)
	}
	if w.Verdict != "" || w.Comment != "" || w.CommentedAt != "" {
		t.Fatalf("empty verdict must clear the trio: %+v", w)
	}
	if w.Observation != "clearable" {
		t.Fatalf("clearing a verdict touched an agent-owned column: %+v", w)
	}
}

func TestDeleteWatchItem(t *testing.T) {
	s := openTest(t)
	id, err := s.AddWatchItem(WatchItem{Observation: "doomed"})
	if err != nil {
		t.Fatalf("AddWatchItem: %v", err)
	}
	if err := s.DeleteWatchItem(id); err != nil {
		t.Fatalf("DeleteWatchItem: %v", err)
	}
	if _, err := s.GetWatchItem(id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetWatchItem after delete: want ErrNotFound, got %v", err)
	}
	if err := s.DeleteWatchItem(id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteWatchItem on an already-deleted id: want ErrNotFound, got %v", err)
	}
}

// TestWatchItemAccessorsNotFound sweeps every accessor that takes an id: a
// missing row is ErrNotFound from all of them, never a silent no-op, so the
// API layer can answer 404 without substring-matching an error string.
func TestWatchItemAccessorsNotFound(t *testing.T) {
	s := openTest(t)
	const missing int64 = 999

	if _, err := s.GetWatchItem(missing); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetWatchItem: want ErrNotFound, got %v", err)
	}
	if err := s.UpdateWatchItem(missing, WatchItem{Observation: "x"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("UpdateWatchItem: want ErrNotFound, got %v", err)
	}
	if err := s.BumpWatchSighting(missing, 1, "{}"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("BumpWatchSighting: want ErrNotFound, got %v", err)
	}
	if err := s.SetWatchVerdict(missing, "fine", "c", "t"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetWatchVerdict: want ErrNotFound, got %v", err)
	}
	if err := s.DeleteWatchItem(missing); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteWatchItem: want ErrNotFound, got %v", err)
	}
}

// TestFindWatchItemMatching pins the lookup's matching rule: exact after
// trimming surrounding whitespace and ASCII-case-folding, and nothing fuzzier.
// This is the accessor that makes folding mechanically possible, so its rule
// being exactly what the doc comment claims is the whole contract.
func TestFindWatchItemMatching(t *testing.T) {
	s := openTest(t)
	const obs = "Hermes agents re-read the contract"
	id, err := s.AddWatchItem(WatchItem{Observation: obs, Class: "repetition"})
	if err != nil {
		t.Fatalf("AddWatchItem: %v", err)
	}

	for _, probe := range []struct{ name, query string }{
		{"exact", obs},
		{"case-folded", "hermes agents re-read the contract"},
		{"upper", "HERMES AGENTS RE-READ THE CONTRACT"},
		{"leading/trailing whitespace", "   " + obs + "\t\n"},
		{"both at once", "  hermes AGENTS re-read the contract  "},
	} {
		got, err := s.FindWatchItem(probe.query)
		if err != nil {
			t.Fatalf("FindWatchItem(%s): %v", probe.name, err)
		}
		if got.ID != id {
			t.Fatalf("FindWatchItem(%s) = id %d, want %d", probe.name, got.ID, id)
		}
	}

	// ...and nothing fuzzier. Internal whitespace is NOT collapsed and a
	// differently-worded observation is a different row — that judgment
	// belongs to the sweeping agent, not to a SQL predicate.
	for _, probe := range []struct{ name, query string }{
		{"collapsed internal whitespace", "Hermes  agents re-read the contract"},
		{"reworded", "hermes re-reads the contract"},
		{"substring", "re-read the contract"},
		{"empty", ""},
	} {
		if _, err := s.FindWatchItem(probe.query); !errors.Is(err, ErrNotFound) {
			t.Fatalf("FindWatchItem(%s) should not match: want ErrNotFound, got %v", probe.name, err)
		}
	}
}

// TestFindWatchItemIgnoresState covers the scope decision: a judged row —
// including a declined one — is still findable, because finding it is exactly
// how the caller learns not to re-propose it. Hiding those would make a
// declined observation come back as a fresh one every night.
func TestFindWatchItemIgnoresState(t *testing.T) {
	s := openTest(t)
	for _, seed := range []struct{ obs, state, verdict string }{
		{"a declined thing", "declined", "fine"},
		{"a shipped thing", "shipped", "fix"},
	} {
		id, err := s.AddWatchItem(WatchItem{Observation: seed.obs, State: seed.state})
		if err != nil {
			t.Fatalf("AddWatchItem %q: %v", seed.obs, err)
		}
		if err := s.SetWatchVerdict(id, seed.verdict, "judged", "2026-07-26T00:00:00Z"); err != nil {
			t.Fatalf("SetWatchVerdict %q: %v", seed.obs, err)
		}
		got, err := s.FindWatchItem(seed.obs)
		if err != nil {
			t.Fatalf("FindWatchItem(%q) on a %s row: %v", seed.obs, seed.state, err)
		}
		if got.ID != id || got.State != seed.state || got.Verdict != seed.verdict {
			t.Fatalf("FindWatchItem(%q) returned the wrong row: %+v", seed.obs, got)
		}
	}
}

// TestFindWatchItemPrefersMostRecent pins the tie-break when duplicates
// already exist (rows predating the lookup, or a caller that skipped it): the
// most recently seen row wins, so a bump lands on the live copy rather than
// resurrecting the stalest one.
func TestFindWatchItemPrefersMostRecent(t *testing.T) {
	s := openTest(t)
	stale, err := s.AddWatchItem(WatchItem{Observation: "dupe", FirstSeen: 100, LastSeen: 100})
	if err != nil {
		t.Fatalf("AddWatchItem stale: %v", err)
	}
	fresh, err := s.AddWatchItem(WatchItem{Observation: "DUPE", FirstSeen: 300, LastSeen: 300})
	if err != nil {
		t.Fatalf("AddWatchItem fresh: %v", err)
	}
	got, err := s.FindWatchItem("dupe")
	if err != nil {
		t.Fatalf("FindWatchItem: %v", err)
	}
	if got.ID != fresh {
		t.Fatalf("FindWatchItem = id %d, want the most recently seen row %d (not the stale %d)", got.ID, fresh, stale)
	}
}

// TestFindThenBumpFoldsInsteadOfDuplicating walks the call order the lookup
// exists to enable — Find → hit → BumpWatchSighting — and pins the outcome
// that actually matters: one row, sightings counting up. A caller that could
// not find the row would insert a second one here and leave both at 1.
func TestFindThenBumpFoldsInsteadOfDuplicating(t *testing.T) {
	s := openTest(t)
	const obs = "zeus re-reads the same skill file every phase"

	// three nights, the observation worded with drifting case/whitespace
	for _, seen := range []string{obs, "  Zeus re-reads the same skill file every phase", "ZEUS RE-READS THE SAME SKILL FILE EVERY PHASE"} {
		existing, err := s.FindWatchItem(seen)
		switch {
		case errors.Is(err, ErrNotFound):
			if _, err := s.AddWatchItem(WatchItem{Observation: seen, Class: "waste"}); err != nil {
				t.Fatalf("AddWatchItem: %v", err)
			}
		case err != nil:
			t.Fatalf("FindWatchItem: %v", err)
		default:
			if err := s.BumpWatchSighting(existing.ID, 0, `{}`); err != nil {
				t.Fatalf("BumpWatchSighting: %v", err)
			}
		}
	}

	all, err := s.ListWatchItems("")
	if err != nil {
		t.Fatalf("ListWatchItems: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("three sightings of one observation must fold into ONE row, got %d: %+v", len(all), all)
	}
	if all[0].Sightings != 3 {
		t.Fatalf("Sightings = %d, want 3 — the signal that separates a one-off from a pattern", all[0].Sightings)
	}
}

// TestFindWatchItemUsesTheExpressionIndex pins the coupling schemaV8's
// watchlist_observation_key exists for: the index is written as the exact
// expression FindWatchItem puts in its WHERE clause, so the normalisation rule
// lives in one place and the lookup is a seek. Editing one side without the
// other still returns correct answers — SQLite just scans — so nothing else in
// this file would notice the drift. EXPLAIN QUERY PLAN is what notices.
func TestFindWatchItemUsesTheExpressionIndex(t *testing.T) {
	s := openTest(t)
	rows, err := s.db.Query(
		`EXPLAIN QUERY PLAN SELECT id FROM watchlist
			WHERE LOWER(TRIM(observation, char(32,9,10,13))) = LOWER(TRIM(?, char(32,9,10,13)))
			ORDER BY last_seen DESC, id DESC LIMIT 1`, "x")
	if err != nil {
		t.Fatalf("EXPLAIN QUERY PLAN: %v", err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatalf("scan plan row: %v", err)
		}
		plan.WriteString(detail + "\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("plan rows: %v", err)
	}
	if !strings.Contains(plan.String(), "watchlist_observation_key") {
		t.Fatalf("FindWatchItem's predicate does not use watchlist_observation_key —\n"+
			"the index expression and the WHERE clause have drifted apart. Plan:\n%s", plan.String())
	}
}

// TestFindWatchItemNotFoundOnEmptyTable is the degenerate miss: the very first
// night, before anything is tracked, must be a clean ErrNotFound rather than
// an error the caller has to special-case.
func TestFindWatchItemNotFoundOnEmptyTable(t *testing.T) {
	s := openTest(t)
	if _, err := s.FindWatchItem("anything at all"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

// TestListWatchItemsEmpty pins that an empty table is a normal, error-free
// read (nil slice), not a special case callers have to guard.
func TestListWatchItemsEmpty(t *testing.T) {
	s := openTest(t)
	all, err := s.ListWatchItems("")
	if err != nil {
		t.Fatalf("ListWatchItems on an empty table: %v", err)
	}
	if len(all) != 0 {
		t.Fatalf("want no rows, got %+v", all)
	}
}
