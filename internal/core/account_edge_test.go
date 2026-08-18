package core

import (
	"fmt"
	"path/filepath"
	"testing"
)

// account_edge_test.go — the EDGE dimension of 50-contract.md's Slice S-core
// CORE obligations for per-cell account binding: every eligibility gate at its
// exact constant, the exclusions that are filters rather than gates, the empty
// inputs, stickiness that must NOT fire, ties in every ranking key, and
// SlotItemExists' fail-closed rule.
//
// Written blind from the frozen contract. internal/core/{rotation,usagepool}.go
// were never read in their working-tree state; AccountUsage, UsageWindow,
// ModelWindow, the candidate* constants, slotKeychainService, PoolDir and the
// stubSecurity test seam are pre-existing package state read at the base commit.
//
// The sibling blind author (athena-logic) owns the stated invariants and the
// happy ranking. What lives here is the other half: the boundary VALUES (a
// percentage exactly AT a gate, a cooldown exactly AT now), the reason strings
// a dry pool has to hand its caller, and the inputs with nothing in them.
//
// Package-level identifiers are prefixed `edgeAcct` — this package's test
// namespace is shared with a second blind author writing in the same round.

// edgeAcctNowMillis is the fixed `now` every case below passes. A constant
// rather than time.Now() so a freshness verdict never depends on when the
// suite ran.
const edgeAcctNowMillis int64 = 1_760_000_000_000

// edgeAcctFifteenMin is the freshness window the contract names, in millis.
const edgeAcctFifteenMin int64 = 15 * 60 * 1000

// edgeAcctHealthy is a candidate that clears every gate: identity, a session
// window well under the bar, no cooldown, and a FetchedAt stamped at `now`
// (fresh). Every case below starts from this and breaks exactly one thing, so
// the reason a candidate is excluded is never ambiguous.
func edgeAcctHealthy(slug string) AccountUsage {
	return AccountUsage{
		Slug:      slug,
		Email:     slug + "@example.com",
		Session:   &UsageWindow{Percent: 10},
		FetchedAt: edgeAcctNowMillis,
	}
}

// edgeAcctWithFable returns c carrying one Fable model window at pct.
func edgeAcctWithFable(c AccountUsage, pct float64) AccountUsage {
	c.Models = []ModelWindow{{Name: "Fable", Percent: pct}}
	return c
}

// edgeAcctBind is the one call shape under test, named so a case row reads as
// its inputs rather than as five positional arguments.
func edgeAcctBind(candidates []AccountUsage, exclude []string, counts map[string]int, prev string) (string, string, string) {
	return SelectBindTarget(candidates, exclude, counts, prev, edgeAcctNowMillis)
}

// ---------------------------------------------------------------------------
// the constants the gates are stated against
// ---------------------------------------------------------------------------

// TestEdgeAccountBindGatesReuseTheRotationConstants pins the three numbers the
// contract states the bind gates are (Fable ≥ 80, session ≥ 80, weekly ≥ 90)
// AND that they are the rotation candidate constants rather than a second copy.
// The gate tests below are written against these names, so without this row a
// silent retune of any one of them would move every boundary case with it and
// the suite would still pass.
func TestEdgeAccountBindGatesReuseTheRotationConstants(t *testing.T) {
	for _, c := range []struct {
		name string
		got  int
		want int
	}{
		{"candidateFablePct", candidateFablePct, 80},
		{"candidateSessionPct", candidateSessionPct, 80},
		{"candidateWeeklyPct", candidateWeeklyPct, 90},
	} {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d — SelectBindTarget's gates are stated as reusing these", c.name, c.got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// every gate, at its boundary
// ---------------------------------------------------------------------------

// TestEdgeSelectBindTargetGateBoundaries drives ONE candidate per row so the
// returned reason is that candidate's alone: the contract says an empty result
// carries the per-candidate exclusion reasons, naming the slug and the failed
// gate "like SelectRotationTarget's", and with a single candidate the join has
// exactly one element to compare.
//
// The pairs are the point. Every threshold in this family is stated as ≥, so
// the value exactly AT the constant must EXCLUDE and the value just under it
// must not — an off-by-one in either direction is invisible on a fixture that
// only uses round numbers far from the bar. The cooldown is the mirror case
// (`CooldownUntil > now`, so a cooldown that expires exactly NOW is over), and
// the two "missing means eligible" rows are the ones a naive nil check gets
// backwards: a plan with no Fable line, and a weekly window that simply could
// not be fetched, must both stay bindable.
func TestEdgeSelectBindTargetGateBoundaries(t *testing.T) {
	for _, c := range []struct {
		name       string
		cand       AccountUsage
		wantPick   bool
		wantReason string
	}{
		{
			name:       "no creds",
			cand:       func() AccountUsage { a := edgeAcctHealthy("a"); a.NoCreds = true; return a }(),
			wantReason: "a: no creds",
		},
		{
			name:       "reauth required",
			cand:       func() AccountUsage { a := edgeAcctHealthy("a"); a.Reauth = true; return a }(),
			wantReason: "a: reauth required",
		},
		{
			name:       "no identity",
			cand:       func() AccountUsage { a := edgeAcctHealthy("a"); a.Email = ""; return a }(),
			wantReason: "a: no identity",
		},
		{
			name:       "no session data",
			cand:       func() AccountUsage { a := edgeAcctHealthy("a"); a.Session = nil; return a }(),
			wantReason: "a: no session data",
		},
		{
			name:       "cooling: one milli PAST now is still cooling",
			cand:       func() AccountUsage { a := edgeAcctHealthy("a"); a.CooldownUntil = edgeAcctNowMillis + 1; return a }(),
			wantReason: "a: cooling",
		},
		{
			name:     "cooling boundary: a cooldown that expires exactly at now is over",
			cand:     func() AccountUsage { a := edgeAcctHealthy("a"); a.CooldownUntil = edgeAcctNowMillis; return a }(),
			wantPick: true,
		},
		{
			name:       "fable exactly at the gate is excluded",
			cand:       edgeAcctWithFable(edgeAcctHealthy("a"), 80),
			wantReason: fmt.Sprintf("a: fable %g%% ≥ %d%%", 80.0, candidateFablePct),
		},
		{
			name:     "fable just under the gate is eligible",
			cand:     edgeAcctWithFable(edgeAcctHealthy("a"), 79.9),
			wantPick: true,
		},
		{
			name:     "no fable window at all is eligible (missing = 0%)",
			cand:     edgeAcctHealthy("a"),
			wantPick: true,
		},
		{
			name:       "session exactly at the gate is excluded",
			cand:       func() AccountUsage { a := edgeAcctHealthy("a"); a.Session = &UsageWindow{Percent: 80}; return a }(),
			wantReason: fmt.Sprintf("a: session %g%% ≥ %d%%", 80.0, candidateSessionPct),
		},
		{
			name:     "session just under the gate is eligible",
			cand:     func() AccountUsage { a := edgeAcctHealthy("a"); a.Session = &UsageWindow{Percent: 79.9}; return a }(),
			wantPick: true,
		},
		{
			name:       "weekly exactly at the gate is excluded",
			cand:       func() AccountUsage { a := edgeAcctHealthy("a"); a.Weekly = &UsageWindow{Percent: 90}; return a }(),
			wantReason: fmt.Sprintf("a: weekly %g%% ≥ %d%%", 90.0, candidateWeeklyPct),
		},
		{
			name:     "weekly just under the gate is eligible",
			cand:     func() AccountUsage { a := edgeAcctHealthy("a"); a.Weekly = &UsageWindow{Percent: 89.9}; return a }(),
			wantPick: true,
		},
		{
			name:     "a nil weekly window is eligible — unfetched is not exhausted",
			cand:     func() AccountUsage { a := edgeAcctHealthy("a"); a.Weekly = nil; return a }(),
			wantPick: true,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			slug, email, reason := edgeAcctBind([]AccountUsage{c.cand}, nil, nil, "")
			if c.wantPick {
				if slug != "a" {
					t.Fatalf("slug = %q (reason %q), want the candidate to survive this boundary", slug, reason)
				}
				if email != c.cand.Email {
					t.Errorf("email = %q, want the picked candidate's own %q", email, c.cand.Email)
				}
				if reason != "" {
					t.Errorf("reason = %q on a successful pick, want empty", reason)
				}
				return
			}
			if slug != "" || email != "" {
				t.Fatalf("(slug, email) = (%q, %q), want the empty pair — this candidate fails a gate", slug, email)
			}
			if reason != c.wantReason {
				t.Errorf("reason = %q, want %q — the reason has to name the slug and the failed gate, or a dry pool is unactionable",
					reason, c.wantReason)
			}
		})
	}
}

// TestEdgeSelectBindTargetJoinsEveryCandidatesReason: with more than one
// candidate excluded, the reasons are semicolon-joined. Joining is what makes
// the notification the binder emits on a dry pool worth reading — one reason
// for a five-slot pool says nothing about the other four.
func TestEdgeSelectBindTargetJoinsEveryCandidatesReason(t *testing.T) {
	a := edgeAcctHealthy("a")
	a.NoCreds = true
	b := edgeAcctHealthy("b")
	b.Reauth = true

	slug, email, reason := edgeAcctBind([]AccountUsage{a, b}, nil, nil, "")
	if slug != "" || email != "" {
		t.Fatalf("(slug, email) = (%q, %q), want the empty pair", slug, email)
	}
	if reason != "a: no creds; b: reauth required" {
		t.Errorf("reason = %q, want %q", reason, "a: no creds; b: reauth required")
	}
}

// ---------------------------------------------------------------------------
// the filters: main, and the excluded emails
// ---------------------------------------------------------------------------

// TestEdgeSelectBindTargetExcludesMainUnconditionally. "main" is the machine's
// own login, not a pool slot: binding a cell to it would set
// CLAUDE_SECURESTORAGE_CONFIG_DIR to a slot dir that is not one and re-create
// exactly the shared-lane contention the whole feature exists to end. The
// exclusion is stated as unconditional, so a perfectly healthy main — and a
// main named as the sticky previous slug — must still be refused.
func TestEdgeSelectBindTargetExcludesMainUnconditionally(t *testing.T) {
	main := edgeAcctHealthy("main")
	main.Current = true

	for _, prev := range []string{"", "main"} {
		slug, email, _ := edgeAcctBind([]AccountUsage{main}, nil, nil, prev)
		if slug != "" || email != "" {
			t.Errorf("prevSlug %q: picked (%q, %q), want the empty pair — the main row is never a bind target", prev, slug, email)
		}
	}

	// with a real slot beside it, the slot is what comes back
	slug, _, _ := edgeAcctBind([]AccountUsage{main, edgeAcctHealthy("slot-a")}, nil, nil, "main")
	if slug != "slot-a" {
		t.Errorf("slug = %q, want slot-a — a sticky prevSlug of main falls through to ranking", slug)
	}
}

// TestEdgeSelectBindTargetExcludesEmailsCaseInsensitively. The caller passes
// the emails a bind must avoid — the main identity (whose save-back mirror
// sits in the pool under its own slug) and, on a rescue, the account the cell
// is already bound to and just exhausted. An email address is case-insensitive
// in its domain and conventionally in its local part, and the pool's
// account.json and ~/.claude.json are two independently-written files: a
// case-sensitive compare silently fails to exclude and the rescue rebinds the
// cell to the account it was rescued FROM.
func TestEdgeSelectBindTargetExcludesEmailsCaseInsensitively(t *testing.T) {
	mixed := edgeAcctHealthy("slot-a")
	mixed.Email = "Carl@Example.COM"

	for _, exclude := range []string{"carl@example.com", "CARL@EXAMPLE.COM", "Carl@Example.COM"} {
		slug, email, _ := edgeAcctBind([]AccountUsage{mixed}, []string{exclude}, nil, "")
		if slug != "" || email != "" {
			t.Errorf("excludeEmails %q: picked (%q, %q), want the empty pair — the compare is case-insensitive", exclude, slug, email)
		}
	}

	// an entry that matches nothing changes nothing, and a nil list is legal
	for _, exclude := range [][]string{nil, {}, {"someone-else@example.com"}} {
		slug, _, _ := edgeAcctBind([]AccountUsage{mixed}, exclude, nil, "")
		if slug != "slot-a" {
			t.Errorf("excludeEmails %v: slug = %q, want slot-a", exclude, slug)
		}
	}

	// every entry excludes, not just the first
	a, b := edgeAcctHealthy("slot-a"), edgeAcctHealthy("slot-b")
	slug, _, _ := edgeAcctBind([]AccountUsage{a, b}, []string{a.Email, b.Email}, nil, "")
	if slug != "" {
		t.Errorf("slug = %q, want the empty pick — both candidates were named in excludeEmails", slug)
	}
}

// ---------------------------------------------------------------------------
// the empty inputs
// ---------------------------------------------------------------------------

// TestEdgeSelectBindTargetEmptyCandidateListIsAnEmptyPick. A dry pool is a
// NORMAL state (Q4: a cell spawns unbound on it), so the empty input must
// answer rather than misbehave: nil and an empty slice are the same answer,
// and neither is a panic.
//
// Only the pair is asserted. The reason string for an input with no candidates
// at all is a contract SILENCE — the stated construction is a join over
// per-candidate reasons, which has no elements here — and is disclosed rather
// than guessed at.
func TestEdgeSelectBindTargetEmptyCandidateListIsAnEmptyPick(t *testing.T) {
	for _, c := range []struct {
		name string
		in   []AccountUsage
	}{
		{"nil", nil},
		{"an empty slice", []AccountUsage{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			slug, email, _ := edgeAcctBind(c.in, nil, nil, "prev")
			if slug != "" || email != "" {
				t.Errorf("picked (%q, %q) out of no candidates", slug, email)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// stickiness, and the four ways it must NOT fire
// ---------------------------------------------------------------------------

// TestEdgeSelectBindTargetStickinessOutranksEveryRankingKey is the control for
// the four negatives below: when prevSlug names an ELIGIBLE candidate it comes
// back BEFORE any ranking, so a previous account that loses on every single
// key — most bound cells, stalest row, highest session %, highest fable %,
// largest slug — still wins. Q6: a reactivated job returning to the account
// its transcripts were billed to is worth more than a marginally better slot.
func TestEdgeSelectBindTargetStickinessOutranksEveryRankingKey(t *testing.T) {
	prev := edgeAcctWithFable(edgeAcctHealthy("zzz-prev"), 79)
	prev.Session = &UsageWindow{Percent: 79}
	prev.FetchedAt = 0 // stale
	better := edgeAcctHealthy("aaa-better")

	slug, email, reason := edgeAcctBind([]AccountUsage{better, prev}, nil, map[string]int{"zzz-prev": 9}, "zzz-prev")
	if slug != "zzz-prev" {
		t.Fatalf("slug = %q, want zzz-prev — stickiness is applied before ranking, not as a tie-break", slug)
	}
	if email != prev.Email {
		t.Errorf("email = %q, want the sticky candidate's own %q", email, prev.Email)
	}
	if reason != "" {
		t.Errorf("reason = %q on a successful sticky pick, want empty", reason)
	}
}

// TestEdgeSelectBindTargetStickinessNeverResurrectsAnIneligibleSlot. Stickiness
// is stated over ELIGIBLE candidates only, and each row here is a way an
// implementation that checked prevSlug first would hand back a slot that must
// never be bound: one that is over a gate, one that no longer exists in the
// snapshot at all (a slot dir carl deleted between two runs of the same job),
// one whose email the caller explicitly excluded (on a rescue, that is the
// exhausted account the cell is being moved OFF), and the empty prevSlug that
// every first bind passes.
func TestEdgeSelectBindTargetStickinessNeverResurrectsAnIneligibleSlot(t *testing.T) {
	fallback := edgeAcctHealthy("fallback")

	exhausted := edgeAcctHealthy("prev")
	exhausted.Session = &UsageWindow{Percent: 95}

	cooling := edgeAcctHealthy("prev")
	cooling.CooldownUntil = edgeAcctNowMillis + 60_000

	present := edgeAcctHealthy("prev")

	for _, c := range []struct {
		name     string
		cands    []AccountUsage
		exclude  []string
		prevSlug string
	}{
		{"the previous slot is over the session gate", []AccountUsage{exhausted, fallback}, nil, "prev"},
		{"the previous slot is cooling", []AccountUsage{cooling, fallback}, nil, "prev"},
		{"the previous slot vanished from the snapshot", []AccountUsage{fallback}, nil, "prev"},
		{"the previous slot's email is excluded", []AccountUsage{present, fallback}, []string{present.Email}, "prev"},
		{"no previous slot at all", []AccountUsage{fallback}, nil, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			slug, email, reason := edgeAcctBind(c.cands, c.exclude, nil, c.prevSlug)
			if slug != "fallback" {
				t.Fatalf("slug = %q (reason %q), want fallback — stickiness must not bypass an exclusion", slug, reason)
			}
			if email != fallback.Email {
				t.Errorf("email = %q, want %q", email, fallback.Email)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// the ranking keys, each decided at a tie in the one above it
// ---------------------------------------------------------------------------

// TestEdgeSelectBindTargetRankingKeysBreakInOrder walks the five keys the
// contract states, each row tied on every key ABOVE it so the row can only be
// decided by the key it is about. Ranking that agrees with the contract only
// on well-separated fixtures is ranking that has not been tested — the whole
// point of the order is what happens when two slots look alike.
func TestEdgeSelectBindTargetRankingKeysBreakInOrder(t *testing.T) {
	t.Run("bound count outranks a better session %", func(t *testing.T) {
		busy := edgeAcctHealthy("busy")
		busy.Session = &UsageWindow{Percent: 1}
		idle := edgeAcctHealthy("idle")
		idle.Session = &UsageWindow{Percent: 70}

		slug, _, _ := edgeAcctBind([]AccountUsage{busy, idle}, nil, map[string]int{"busy": 1}, "")
		if slug != "idle" {
			t.Errorf("slug = %q, want idle — fewest bound cells is the FIRST key, ahead of session %%", slug)
		}
	})

	t.Run("a slug absent from boundCounts counts as zero", func(t *testing.T) {
		known := edgeAcctHealthy("known")
		unknown := edgeAcctHealthy("unknown")
		slug, _, _ := edgeAcctBind([]AccountUsage{known, unknown}, nil, map[string]int{"known": 2}, "")
		if slug != "unknown" {
			t.Errorf("slug = %q, want unknown — a missing key is zero bound cells, not an unknown quantity", slug)
		}
		// a nil map is every candidate at zero, so the later keys decide
		nilSlug, _, _ := edgeAcctBind([]AccountUsage{known, unknown}, nil, nil, "")
		if nilSlug != "known" {
			t.Errorf("slug = %q with a nil boundCounts, want known (the lexicographic tail) — nil must mean all-zero, not all-excluded", nilSlug)
		}
	})

	t.Run("fresh outranks stale at an equal bound count", func(t *testing.T) {
		stale := edgeAcctHealthy("stale")
		stale.Session = &UsageWindow{Percent: 1}
		stale.FetchedAt = 0 // stated explicitly: 0 is stale
		fresh := edgeAcctHealthy("fresh")
		fresh.Session = &UsageWindow{Percent: 70}

		slug, _, _ := edgeAcctBind([]AccountUsage{stale, fresh}, nil, nil, "")
		if slug != "fresh" {
			t.Errorf("slug = %q, want fresh — FetchedAt 0 is stale and freshness outranks session %%", slug)
		}
	})

	t.Run("a row well inside the 15-minute window is fresh and one well outside is stale", func(t *testing.T) {
		// deliberately a millisecond clear of the endpoint on both sides: the
		// verdict exactly AT 15 minutes is a contract silence ("within" reads
		// inclusive, the repo's existing rowFresh is exclusive) and is
		// disclosed rather than pinned here.
		inside := edgeAcctHealthy("inside")
		inside.FetchedAt = edgeAcctNowMillis - (edgeAcctFifteenMin - 1)
		inside.Session = &UsageWindow{Percent: 70}
		outside := edgeAcctHealthy("outside")
		outside.FetchedAt = edgeAcctNowMillis - (edgeAcctFifteenMin + 1)
		outside.Session = &UsageWindow{Percent: 1}

		slug, _, _ := edgeAcctBind([]AccountUsage{outside, inside}, nil, nil, "")
		if slug != "inside" {
			t.Errorf("slug = %q, want inside — a row one milli inside the window is fresh and outranks a better-looking stale one", slug)
		}
	})

	t.Run("a stale row still binds when nothing fresh is eligible", func(t *testing.T) {
		stale := edgeAcctHealthy("stale")
		stale.FetchedAt = 0
		slug, email, _ := edgeAcctBind([]AccountUsage{stale}, nil, nil, "")
		if slug != "stale" || email != stale.Email {
			t.Errorf("(slug, email) = (%q, %q), want the stale row — staleness ranks, it does not exclude", slug, email)
		}
	})

	t.Run("session % breaks a bound-count and freshness tie", func(t *testing.T) {
		low := edgeAcctHealthy("zzz-low")
		low.Session = &UsageWindow{Percent: 10}
		high := edgeAcctHealthy("aaa-high")
		high.Session = &UsageWindow{Percent: 11}

		slug, _, _ := edgeAcctBind([]AccountUsage{high, low}, nil, nil, "")
		if slug != "zzz-low" {
			t.Errorf("slug = %q, want zzz-low — session %% decides before the slug does", slug)
		}
	})

	t.Run("fable % breaks a session tie, and a missing window is zero", func(t *testing.T) {
		withFable := edgeAcctWithFable(edgeAcctHealthy("aaa-fable"), 50)
		none := edgeAcctHealthy("zzz-none") // no Models at all → 0%

		slug, _, _ := edgeAcctBind([]AccountUsage{withFable, none}, nil, nil, "")
		if slug != "zzz-none" {
			t.Errorf("slug = %q, want zzz-none — a missing Fable window ranks as 0%%, which is the LOWEST, not the highest", slug)
		}
	})

	t.Run("the slug is the final, order-independent tie-break", func(t *testing.T) {
		a, b, c := edgeAcctHealthy("aaa"), edgeAcctHealthy("mmm"), edgeAcctHealthy("zzz")
		for _, order := range [][]AccountUsage{{a, b, c}, {c, b, a}, {b, c, a}} {
			slug, _, _ := edgeAcctBind(order, nil, nil, "")
			if slug != "aaa" {
				t.Errorf("input order %v: slug = %q, want aaa — with every other key tied the answer must not depend on input order",
					[]string{order[0].Slug, order[1].Slug, order[2].Slug}, slug)
			}
		}
	})
}

// TestEdgeSelectBindTargetIsPureOverItsInputs. The contract states the function
// is pure over its inputs, and the caller that matters is the binder: it holds
// the snapshot it just read and the live bound counts it just computed, and
// hands the same values to other readers. A sort in place would reorder the
// caller's snapshot; a write into boundCounts would corrupt the very tally the
// next concurrent submit ranks against.
func TestEdgeSelectBindTargetIsPureOverItsInputs(t *testing.T) {
	cands := []AccountUsage{edgeAcctHealthy("zzz"), edgeAcctHealthy("aaa"), edgeAcctHealthy("mmm")}
	counts := map[string]int{"zzz": 1}

	before := fmt.Sprintf("%+v", cands)
	countsBefore := fmt.Sprintf("%+v", counts)

	slug1, email1, reason1 := edgeAcctBind(cands, []string{"nobody@example.com"}, counts, "")
	slug2, email2, reason2 := edgeAcctBind(cands, []string{"nobody@example.com"}, counts, "")

	if slug1 != slug2 || email1 != email2 || reason1 != reason2 {
		t.Errorf("two identical calls disagreed: (%q,%q,%q) then (%q,%q,%q)", slug1, email1, reason1, slug2, email2, reason2)
	}
	if got := fmt.Sprintf("%+v", cands); got != before {
		t.Errorf("the candidate slice was mutated:\n before %s\n after  %s", before, got)
	}
	if got := fmt.Sprintf("%+v", counts); got != countsBefore {
		t.Errorf("boundCounts was mutated: %s, want %s — the caller's live tally must survive the call", got, countsBefore)
	}
}

// ---------------------------------------------------------------------------
// SlotItemExists
// ---------------------------------------------------------------------------

// TestEdgeSlotItemExistsFailsClosed. The probe is Q1's per-bind self-test:
// binding a cell to a slot whose keychain item is missing sends a claude to a
// credential store with nothing in it, which is a dead pane rather than a
// degraded one. So the answer on ANY error is false — an unprobeable slot is
// an ineligible slot — and the question it asks is EXISTENCE, not value: an
// item present but empty still exists, and the contract states the test as
// "exits 0" rather than "returns a password".
//
// The service name is derived from the slot dir, so a sibling slot is a
// different item: a probe that ignored its argument would pass every case
// above and still bind every cell to whatever one slot happened to be seeded.
func TestEdgeSlotItemExistsFailsClosed(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	pool := filepath.Join(t.TempDir(), "pool")
	t.Setenv("CLAUDE_POOL_DIR", pool)

	items := map[string]string{}
	stubSecurity(t, items)

	seeded := filepath.Join(pool, "slot-a")
	empty := filepath.Join(pool, "slot-empty")
	absent := filepath.Join(pool, "slot-b")

	if SlotItemExists(seeded) {
		t.Error("SlotItemExists reported true for a slot with no keychain item at all")
	}

	items[slotKeychainService(seeded)] = `{"claudeAiOauth":{"accessToken":"x"}}`
	if !SlotItemExists(seeded) {
		t.Error("SlotItemExists reported false for a seeded slot")
	}
	if SlotItemExists(absent) {
		t.Errorf("SlotItemExists reported true for %s, whose own item was never seeded — the probe must key on the slot dir", absent)
	}

	items[slotKeychainService(empty)] = ""
	if !SlotItemExists(empty) {
		t.Error("SlotItemExists reported false for an item that EXISTS but holds an empty value — the test is whether the lookup succeeds, not what it returns")
	}
}
