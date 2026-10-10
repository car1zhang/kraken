package jobs

import "testing"

// blockedhold_test.go — the one-tick confirmation of a working job's blocked
// reading (holdBlocked). The roll call reads a mid-turn zeus as idle for
// exactly one tick now and then; without the hold each misread was a state
// write, an artifact sync and a "blocked" notification for a cell that never
// stopped.

func TestHoldBlockedConfirmsOverTwoAdjacentTicks(t *testing.T) {
	// Tick 7: a working job first reads blocked — held, remembered.
	st, first := holdBlocked("working", "blocked", true, 0, 7)
	if st != "working" || first != 7 {
		t.Fatalf("first blocked reading = (%q, %d), want held as working with tick 7 remembered", st, first)
	}
	// Tick 8: still blocked — confirmed, the hold's origin kept.
	st, first = holdBlocked("working", "blocked", true, first, 8)
	if st != "blocked" || first != 7 {
		t.Fatalf("second consecutive blocked reading = (%q, %d), want blocked with tick 7 kept", st, first)
	}
}

func TestHoldBlockedResetsOnAWorkingReading(t *testing.T) {
	_, first := holdBlocked("working", "blocked", true, 0, 7)
	// Tick 8 reads working again: the misread is over and forgotten.
	st, first := holdBlocked("working", "working", true, first, 8)
	if st != "working" || first != 0 {
		t.Fatalf("working reading after a hold = (%q, %d), want working with the hold cleared", st, first)
	}
	// Tick 240 (an hour later): a fresh blocked reading starts a NEW hold
	// rather than confirming the one from tick 7.
	st, first = holdBlocked("working", "blocked", true, first, 240)
	if st != "working" || first != 240 {
		t.Fatalf("blocked reading an hour later = (%q, %d), want a new hold at tick 240", st, first)
	}
}

func TestHoldBlockedConfirmsOnlyAnAdjacentHold(t *testing.T) {
	// A hold begun at tick 3 that was never followed up — the job skipped
	// the roll call for a while (a repo that stopped resolving, a kill to
	// draft and a resubmit, a freed slug reused) — must not confirm a
	// blocked reading at tick 9: that reading is a NEW first reading.
	st, first := holdBlocked("working", "blocked", true, 3, 9)
	if st != "working" || first != 9 {
		t.Fatalf("stale hold from tick 3 at tick 9 = (%q, %d), want a new hold at tick 9", st, first)
	}
	// And the tick after THAT confirms it.
	st, first = holdBlocked("working", "blocked", true, first, 10)
	if st != "blocked" || first != 9 {
		t.Fatalf("adjacent follow-up = (%q, %d), want blocked with tick 9 kept", st, first)
	}
}

func TestHoldBlockedNeverHoldsADeadCell(t *testing.T) {
	// A working job whose tmux session is gone derives blocked for a
	// reason that is not a roll-call misread; it is not held.
	st, first := holdBlocked("working", "blocked", false, 0, 7)
	if st != "blocked" || first != 0 {
		t.Fatalf("dead cell's blocked reading = (%q, %d), want blocked at once with no hold", st, first)
	}
}

func TestHoldBlockedOnlyGuardsTheWorkingToBlockedEdge(t *testing.T) {
	cases := []struct {
		name      string
		persisted string
		derived   string
	}{
		{"already blocked stays blocked", "blocked", "blocked"},
		{"draft reads as derived", "draft", "draft"},
		{"done reads as derived", "done", "done"},
		{"a blocked job reading working flips at once", "blocked", "working"},
		{"a working job reading working", "working", "working"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A stale hold from an earlier generation must not change
			// the answer either: the second argument set carries one.
			for _, stale := range []int{0, 3} {
				st, first := holdBlocked(tc.persisted, tc.derived, true, stale, 9)
				if st != tc.derived {
					t.Errorf("holdBlocked(%q, %q, %d) = %q, want the derived state untouched", tc.persisted, tc.derived, stale, st)
				}
				if first != 0 {
					t.Errorf("holdBlocked(%q, %q, %d) kept hold tick %d, want 0", tc.persisted, tc.derived, stale, first)
				}
			}
		})
	}
}

func TestHoldBlockedNeverFlipsABlockedJobToWorking(t *testing.T) {
	// The hold answers "working" only for a job whose persisted state IS
	// working; a blocked job's blocked reading is never softened.
	st, _ := holdBlocked("blocked", "blocked", true, 0, 1)
	if st != "blocked" {
		t.Fatalf("a blocked job's blocked reading = %q, want blocked", st)
	}
}

func TestPruneArtifactSyncStateDropsBlockedSinceToo(t *testing.T) {
	d := newDaemon()
	d.BlockedSince["gone"] = 4
	d.BlockedSince["live"] = 5
	d.pruneArtifactSyncState([]*Job{{Slug: "live", State: "working", Session: "s"}})
	if _, ok := d.BlockedSince["gone"]; ok {
		t.Errorf("BlockedSince kept an entry for a slug that left the live set")
	}
	if d.BlockedSince["live"] != 5 {
		t.Errorf("BlockedSince[live] = %d, want 5 kept", d.BlockedSince["live"])
	}
}
