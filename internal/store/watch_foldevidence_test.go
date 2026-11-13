package store

import "testing"

// A fold keeps the row's stored evidence behind the new text instead of
// replacing it, so a caller that rebuilds evidence from a shortened copy of
// the old cannot drop a row's history.

func foldEvidence(t *testing.T, s *Store, observation, evidence string) string {
	t.Helper()
	id, _, err := s.AddWatchItem(WatchItem{Observation: observation, Class: "waste", Evidence: evidence})
	if err != nil {
		t.Fatalf("AddWatchItem(%q): %v", evidence, err)
	}
	got, err := s.GetWatchItem(id)
	if err != nil {
		t.Fatalf("GetWatchItem: %v", err)
	}
	return got.Evidence
}

func TestWatchFoldKeepsStoredEvidenceBehindTheNew(t *testing.T) {
	s := openTest(t)
	const obs = "observation alpha"
	if got := foldEvidence(t, s, obs, "night one"); got != "night one" {
		t.Fatalf("create evidence = %q, want it stored as sent", got)
	}
	want := "night two" + watchEvidencePriorSep + "night one"
	if got := foldEvidence(t, s, obs, "night two"); got != want {
		t.Fatalf("first fold evidence = %q, want %q", got, want)
	}
	want = "night three" + watchEvidencePriorSep + want
	if got := foldEvidence(t, s, obs, "night three"); got != want {
		t.Fatalf("second fold evidence = %q, want %q", got, want)
	}
}

func TestWatchFoldDoesNotDoubleEvidenceTheCallerRestated(t *testing.T) {
	s := openTest(t)
	const obs = "observation bravo"
	foldEvidence(t, s, obs, "night one")
	sent := "night two | Earlier: night one"
	if got := foldEvidence(t, s, obs, sent); got != sent {
		t.Fatalf("evidence = %q, want it stored as sent (%q) since it already holds the stored text", got, sent)
	}
}

func TestWatchFoldKeepsTheWholeStoredEvidenceWhenTheCallerSentACutCopy(t *testing.T) {
	s := openTest(t)
	const obs = "observation charlie"
	stored := "night one, with a tail the caller's copy leaves out"
	foldEvidence(t, s, obs, stored)
	sent := "night two | Earlier: night one, with a tail"
	want := sent + watchEvidencePriorSep + stored
	if got := foldEvidence(t, s, obs, sent); got != want {
		t.Fatalf("evidence = %q, want %q", got, want)
	}
}

func TestWatchFoldWithNoEvidenceSentLeavesTheStoredAlone(t *testing.T) {
	s := openTest(t)
	const obs = "observation delta"
	foldEvidence(t, s, obs, "night one")
	if got := foldEvidence(t, s, obs, ""); got != "night one" {
		t.Fatalf("evidence = %q, want the stored text untouched by an evidence-less fold", got)
	}
}

func TestWatchFoldOntoAnEvidenceLessRowStoresTheNewAlone(t *testing.T) {
	s := openTest(t)
	const obs = "observation echo"
	foldEvidence(t, s, obs, "")
	if got := foldEvidence(t, s, obs, "night two"); got != "night two" {
		t.Fatalf("evidence = %q, want the new text alone, with no separator", got)
	}
}

func TestWatchUpdateStillReplacesEvidenceOutright(t *testing.T) {
	s := openTest(t)
	id, _, err := s.AddWatchItem(WatchItem{Observation: "observation foxtrot", Class: "correction", Evidence: "night one"})
	if err != nil {
		t.Fatalf("AddWatchItem: %v", err)
	}
	if err := s.UpdateWatchItem(id, WatchItem{Evidence: "rewritten"}); err != nil {
		t.Fatalf("UpdateWatchItem: %v", err)
	}
	got, err := s.GetWatchItem(id)
	if err != nil {
		t.Fatalf("GetWatchItem: %v", err)
	}
	if got.Evidence != "rewritten" {
		t.Fatalf("evidence = %q, want an update to replace it outright", got.Evidence)
	}
}
