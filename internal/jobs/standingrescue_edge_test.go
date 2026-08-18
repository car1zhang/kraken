package jobs

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"kraken/internal/core"
	"kraken/internal/store"
	"kraken/internal/util"
)

// standingrescue_edge_test.go — the EDGE dimension of the standing-agent
// usage-limit rescue (artifacts/50-contract.md, slices P and S):
// boundaries, refusals, degenerate inputs, and the arms that must do
// NOTHING. The happy paths and the stated invariants belong to the sibling
// author's contract suite in this same package, so every package-level
// identifier here carries the `edgeStanding` prefix — one declaration space
// per package, and `edge` alone is already crowded by
// limitrescue_edge_test.go (edgeNow, edgeJob, edgeAgentPeer, …), whose
// identifiers this file deliberately does not redeclare.
//
// Written blind against the contract text, before the implementation
// existed. Two boundaries are never crossed: nothing here performs a real
// machine-wide credential swap (the rotate arm is reached only through its
// no-target refusal, and swapMain/selectTarget stay untouched), and nothing
// kills a real pane — the standingRespawn seam is what the resume arm is
// exercised through.

// edgeStandingNow is a fixed whole-second instant. Whole seconds matter:
// the record stores unix SECONDS (At/SwappedAt/LastAttempt/ResetAt), so a
// now carrying nanoseconds would put every window boundary a sub-second off
// and make the "exactly at the boundary" cases test something other than
// the boundary.
var edgeStandingNow = time.Unix(1_800_000_000, 0)

// --- the registry and its ids ---

// standingAgentIDs is the one list the whole feature keys off: the hit key,
// the trigger key, the API route's validation and the descriptor rows are
// all derived from it, so its contents and its ORDER are contract, not
// convenience.
func TestEdgeStandingAgentIDsAreExactAndStable(t *testing.T) {
	want := []string{"kronos", "iris"}
	for round := 0; round < 2; round++ {
		got := standingAgentIDs()
		if len(got) != len(want) {
			t.Fatalf("round %d: standingAgentIDs() = %v, want %v", round, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("round %d: standingAgentIDs() = %v, want %v (order is contract)", round, got, want)
			}
		}
	}
}

// IsStandingAgentID is case-SENSITIVE and exact. It is the API route's
// whole validation, so anything it accepts becomes a kv key that the rescue
// pass will later act on — an accepted near-miss ("Kronos", " kronos") would
// mint a record no pass ever reads, and an accepted numeric string would
// land in the same namespace a job's key lives in.
func TestEdgeIsStandingAgentIDRejectsEverythingElse(t *testing.T) {
	for _, id := range []string{"kronos", "iris"} {
		if !IsStandingAgentID(id) {
			t.Errorf("IsStandingAgentID(%q) = false, want true", id)
		}
	}
	for _, id := range []string{
		"",
		" ",
		"KRONOS",
		"Kronos",
		"IRIS",
		"Iris",
		" kronos",
		"kronos ",
		"kronos\n",
		"\tiris",
		"kronos-1",
		"kronos.iris",
		"krono",
		"kronoss",
		"iri",
		"iriss",
		"zeus",
		"hephaestus",
		"athena-edge",
		"0",
		"1",
		"42",
		"123456",
		"-1",
		"limit_hit:kronos",
		"limit_hit:iris",
		"kronos,iris",
		"kronos|iris",
	} {
		if IsStandingAgentID(id) {
			t.Errorf("IsStandingAgentID(%q) = true, want false", id)
		}
	}
}

// standingHitKey's whole job is to put a standing agent's record in the
// SAME namespace as a job's without ever landing on the same key. Nothing
// deletes either row, so a collision is permanent: a standing agent would
// read a job's swap watermark (or the other way round) and swap the
// machine's login on another party's evidence.
func TestEdgeStandingHitKeyShape(t *testing.T) {
	for _, id := range standingAgentIDs() {
		if got, want := standingHitKey(id), "limit_hit:"+id; got != want {
			t.Errorf("standingHitKey(%q) = %q, want %q", id, got, want)
		}
	}
}

// The contract's mandated collision fixture, by SHAPE rather than by luck:
// a job whose decimal id renders to a string the same LENGTH as "kronos"
// must still key differently, and so must the extremes of the int64 range.
func TestEdgeStandingHitKeyNeverCollidesWithAJobKey(t *testing.T) {
	agentKeys := map[string]string{}
	for _, id := range standingAgentIDs() {
		agentKeys[standingHitKey(id)] = id
	}

	for _, jobID := range []int64{
		0, 1, -1, 6, 42,
		123456,  // exactly len("kronos") == 6 digits — the mandated same-length shape
		1111,    // len("iris") == 4 digits
		9999999, // longer than either
		math.MaxInt64,
		math.MinInt64,
	} {
		key := limitHitKey(jobID)
		if id, clash := agentKeys[key]; clash {
			t.Fatalf("limitHitKey(%d) = %q collides with standingHitKey(%q)", jobID, key, id)
		}
	}

	// and the shape assertion itself: equal length, different string. A test
	// that only compared unequal ids would pass on a key scheme that
	// happened to differ in length today.
	sameLen := limitHitKey(123456)
	if len(sameLen) != len(standingHitKey("kronos")) {
		t.Fatalf("fixture is not the intended shape: %q and %q differ in length",
			sameLen, standingHitKey("kronos"))
	}
	if sameLen == standingHitKey("kronos") {
		t.Fatalf("limitHitKey(123456) == standingHitKey(\"kronos\") == %q", sameLen)
	}
}

// standingHitTriggerKey is the re-arming dedup's key, and the contract's
// second mandated fixture lives here: a reason that CONTAINS the agent id
// verbatim (which every real reason does — standingStallReason names the
// agent) must not let two agents' keys merge, and the separator must keep
// the id and the reason distinguishable.
func TestEdgeStandingHitTriggerKeyShapeAndCollision(t *testing.T) {
	const reason = "kronos hit a usage limit: You've hit your session limit"

	for _, id := range standingAgentIDs() {
		if got, want := standingHitTriggerKey(id, reason), "limithit:"+id+"|"+reason; got != want {
			t.Errorf("standingHitTriggerKey(%q, reason) = %q, want %q", id, got, want)
		}
	}

	// the collision fixture: one reason naming kronos, keyed under both
	// agents, must produce two different keys — otherwise iris' dedup stamp
	// would mute kronos' re-record (and vice versa) forever.
	if a, b := standingHitTriggerKey("kronos", reason), standingHitTriggerKey("iris", reason); a == b {
		t.Fatalf("both agents key one reason to %q", a)
	}

	// and it can never land on a JOB's trigger key: those render a decimal
	// int64 where this renders a non-numeric id.
	for _, jobID := range []int64{0, 1, 123456, math.MaxInt64} {
		jk := limitHitTriggerKey(jobID, reason)
		for _, id := range standingAgentIDs() {
			if jk == standingHitTriggerKey(id, reason) {
				t.Fatalf("limitHitTriggerKey(%d, reason) collides with agent %q: %q", jobID, id, jk)
			}
		}
	}
}

// --- readLimitHitByKey / recordLimitHitByKey ---

// The keyed read is errorless for ReadLimitHit's reason exactly: every
// failure means the rescue does NOTHING, which is the conservative
// direction. A read that could report "I couldn't tell" would tempt a
// caller into treating it as "there is a hit", and that direction ends in a
// machine-wide account swap performed on no evidence.
func TestEdgeReadLimitHitByKeyDegradesToZero(t *testing.T) {
	t.Run("no store wired", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		if got := readLimitHitByKey(standingHitKey("kronos")); got != (LimitHit{}) {
			t.Fatalf("readLimitHitByKey with no store = %+v, want the zero record", got)
		}
	})

	t.Run("unset key", func(t *testing.T) {
		useTestStore(t)
		if got := readLimitHitByKey(standingHitKey("iris")); got != (LimitHit{}) {
			t.Fatalf("readLimitHitByKey(unset) = %+v, want the zero record", got)
		}
	})

	t.Run("garbage rows", func(t *testing.T) {
		st := useTestStore(t)
		key := standingHitKey("kronos")
		for _, bad := range []string{
			"{not json",
			"",
			"[1,2,3]",
			`{"at":"not-a-number"}`,
			`"a bare string"`,
			"null",
			"17",
		} {
			if err := st.SetKV(key, bad); err != nil {
				t.Fatal(err)
			}
			if got := readLimitHitByKey(key); got != (LimitHit{}) {
				t.Fatalf("readLimitHitByKey over stored %q = %+v, want the zero record", bad, got)
			}
		}
	})
}

// ReadLimitHit(j) delegates to the keyed read via limitHitKey(j.ID) with
// byte-identical behavior — which means the two must be observationally the
// same function on the same row, not merely similar.
func TestEdgeReadLimitHitDelegatesToTheKeyedRead(t *testing.T) {
	st := useTestStore(t)
	j := edgeStandingStoreJob(t, st, "edge-standing-delegate")

	if got, err := RecordLimitHit(j, "rate_limit", "d", "sess", "agent", edgeStandingNow); err != nil {
		t.Fatalf("RecordLimitHit: %v (%+v)", err, got)
	}
	direct := ReadLimitHit(j)
	keyed := readLimitHitByKey(limitHitKey(j.ID))
	if direct != keyed {
		t.Fatalf("ReadLimitHit = %+v but readLimitHitByKey(limitHitKey(id)) = %+v", direct, keyed)
	}
	if direct.At == 0 {
		t.Fatal("fixture wrote no record")
	}
}

// With no store the keyed record has nowhere to go: it errors and writes
// NOTHING, and its error names the LABEL it was handed. The label is the
// only thing distinguishing one caller's failure from another's in a log —
// the key is a namespace, the label is the human word for who hit the wall.
func TestEdgeRecordLimitHitByKeyWithoutStoreErrsAndNamesTheLabel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	got, err := recordLimitHitByKey(standingHitKey("kronos"), "kronos", "", "rate_limit", "d", "s", "a", edgeStandingNow)
	if err == nil {
		t.Fatalf("recordLimitHitByKey with no store = (%+v, nil), want an error", got)
	}
	if !strings.Contains(err.Error(), "kronos") {
		t.Errorf("error %q does not name the label", err)
	}
	if got != (LimitHit{}) {
		t.Errorf("a failed record returned %+v, want the zero record", got)
	}
}

// The fold rules are mergeLimitHitDelivery's and are REUSED UNCHANGED, so
// the keyed path has to exhibit every one of them. Each subtest is a
// boundary the fold either crosses or refuses to.
func TestEdgeRecordLimitHitByKeyFoldEdges(t *testing.T) {
	t.Run("a resolved hit never folds", func(t *testing.T) {
		st := useTestStore(t)
		key := standingHitKey("kronos")
		edgeStandingSeedHit(t, st, key, LimitHit{
			Error: "rate_limit", At: edgeStandingNow.Unix() - 600, Deliveries: 4,
			HandledAt: edgeStandingNow.Unix() - 300, Outcome: "rotated",
		})
		got, err := recordLimitHitByKey(key, "kronos", "", "rate_limit", "again", "s", "", edgeStandingNow)
		if err != nil {
			t.Fatal(err)
		}
		if got.At != edgeStandingNow.Unix() {
			t.Errorf("At = %d, want a FRESH record at %d — a resolved hit is a closed event", got.At, edgeStandingNow.Unix())
		}
		if got.Deliveries != 1 {
			t.Errorf("Deliveries = %d, want 1", got.Deliveries)
		}
		if got.HandledAt != 0 || got.Outcome != "" {
			t.Errorf("the fresh record inherited the resolution: %+v", got)
		}
	})

	t.Run("a different error type never folds", func(t *testing.T) {
		st := useTestStore(t)
		key := standingHitKey("kronos")
		edgeStandingSeedHit(t, st, key, LimitHit{
			Error: "rate_limit", At: edgeStandingNow.Unix() - 60, Deliveries: 2,
		})
		got, err := recordLimitHitByKey(key, "kronos", "", "overloaded", "", "", "", edgeStandingNow)
		if err != nil {
			t.Fatal(err)
		}
		if got.Error != "overloaded" || got.Deliveries != 1 || got.At != edgeStandingNow.Unix() {
			t.Fatalf("a different error type folded: %+v", got)
		}
	})

	t.Run("an unresolved swap watermark survives supersession", func(t *testing.T) {
		st := useTestStore(t)
		key := standingHitKey("kronos")
		edgeStandingSeedHit(t, st, key, LimitHit{
			Error: "rate_limit", At: edgeStandingNow.Unix() - 60, Deliveries: 1,
			SwappedAt: edgeStandingNow.Unix() - 30, SwappedTo: "pool-b@example.com",
		})
		// a DIFFERENT error type supersedes the record; the landed swap must
		// ride across, or the next tick reads a hit that was never swapped
		// and swaps the machine a second time.
		got, err := recordLimitHitByKey(key, "kronos", "", "overloaded", "", "", "", edgeStandingNow)
		if err != nil {
			t.Fatal(err)
		}
		if got.SwappedAt != edgeStandingNow.Unix()-30 || got.SwappedTo != "pool-b@example.com" {
			t.Fatalf("the unresolved swap watermark was dropped: %+v", got)
		}
	})

	t.Run("a RESOLVED swap watermark does not ride across", func(t *testing.T) {
		st := useTestStore(t)
		key := standingHitKey("iris")
		edgeStandingSeedHit(t, st, key, LimitHit{
			Error: "rate_limit", At: edgeStandingNow.Unix() - 600, Deliveries: 1,
			SwappedAt: edgeStandingNow.Unix() - 590, SwappedTo: "pool-b@example.com",
			HandledAt: edgeStandingNow.Unix() - 500, Outcome: "rotated",
		})
		got, err := recordLimitHitByKey(key, "iris", "", "rate_limit", "", "", "", edgeStandingNow)
		if err != nil {
			t.Fatal(err)
		}
		if got.SwappedAt != 0 || got.SwappedTo != "" {
			t.Fatalf("a FINISHED rescue's swap stamp was carried onto a brand-new hit: %+v", got)
		}
	})

	t.Run("a folded delivery with a reset clause replaces clause-less details", func(t *testing.T) {
		st := useTestStore(t)
		key := standingHitKey("kronos")
		edgeStandingSeedHit(t, st, key, LimitHit{
			Error: "rate_limit", At: edgeStandingNow.Unix() - 60, Deliveries: 1,
			Details: "429 Too Many Requests",
		})
		const withClause = "You've hit your session limit · resets 4:50am (America/New_York)"
		got, err := recordLimitHitByKey(key, "kronos", "", "rate_limit", withClause, "", "", edgeStandingNow)
		if err != nil {
			t.Fatal(err)
		}
		if got.Details != withClause {
			t.Fatalf("Details = %q, want the clause-carrying delivery's %q", got.Details, withClause)
		}
		if got.Deliveries != 2 {
			t.Errorf("Deliveries = %d, want 2 — it still folded", got.Deliveries)
		}
	})

	t.Run("a clause-less delivery never replaces clause-carrying details", func(t *testing.T) {
		st := useTestStore(t)
		key := standingHitKey("kronos")
		const withClause = "You've hit your session limit · resets 4:50am (America/New_York)"
		edgeStandingSeedHit(t, st, key, LimitHit{
			Error: "rate_limit", At: edgeStandingNow.Unix() - 60, Deliveries: 1,
			Details: withClause,
		})
		got, err := recordLimitHitByKey(key, "kronos", "", "rate_limit", "429 Too Many Requests", "", "", edgeStandingNow)
		if err != nil {
			t.Fatal(err)
		}
		if got.Details != withClause {
			t.Fatalf("Details = %q — the reset clause was overwritten by a clause-less delivery", got.Details)
		}
	})

	t.Run("the fold keeps the FIRST delivery's identity", func(t *testing.T) {
		useTestStore(t)
		key := standingHitKey("kronos")
		first, err := recordLimitHitByKey(key, "kronos", "", "rate_limit", "one", "sess-a", "", edgeStandingNow)
		if err != nil {
			t.Fatal(err)
		}
		// hours later, from the transcript arm: still folds (there is no
		// time window on the fold), and At/SessionID stay the first one's.
		got, err := recordLimitHitByKey(key, "kronos", "", "rate_limit", "two", "sess-b", "agent-2", edgeStandingNow.Add(9*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if got.At != first.At || got.SessionID != first.SessionID {
			t.Fatalf("the fold rewrote the hit's identity: %+v (first %+v)", got, first)
		}
		if got.Deliveries != 2 {
			t.Errorf("Deliveries = %d, want 2", got.Deliveries)
		}
	})
}

// --- recordMainSwap / readMainSwap ---

// readMainSwap is errorless for readLimitHitByKey's reason: the watermark
// only ever SUPPRESSES a second swap, so an unreadable one costs at most a
// duplicate rotate, while an invented one would make a hit adopt a swap
// that never happened and skip straight to a resume on the exhausted
// account.
func TestEdgeReadMainSwapDegradesToZero(t *testing.T) {
	t.Run("no store wired", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		if at, to := readMainSwap(); at != 0 || to != "" {
			t.Fatalf("readMainSwap with no store = (%d, %q), want (0, \"\")", at, to)
		}
	})

	t.Run("unset row", func(t *testing.T) {
		useTestStore(t)
		if at, to := readMainSwap(); at != 0 || to != "" {
			t.Fatalf("readMainSwap(unset) = (%d, %q), want (0, \"\")", at, to)
		}
	})

	t.Run("garbage rows", func(t *testing.T) {
		st := useTestStore(t)
		for _, bad := range []string{
			"{not json",
			"",
			"[1,2,3]",
			`{"at":"nope","to":"x@y"}`,
			`"a bare string"`,
			"null",
			"17",
		} {
			if err := st.SetKV("main_swap", bad); err != nil {
				t.Fatal(err)
			}
			if at, to := readMainSwap(); at != 0 || to != "" {
				t.Fatalf("readMainSwap over stored %q = (%d, %q), want (0, \"\")", bad, at, to)
			}
		}
	})
}

func TestEdgeRecordMainSwapWithoutStoreErrs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := recordMainSwap("pool-b@example.com", edgeStandingNow); err == nil {
		t.Fatal("recordMainSwap with no store = nil, want an error")
	}
}

// The watermark is one row and the LATEST swap is what it names: a second
// swap overwrites rather than accumulating, since the question it answers
// ("has the machine's login moved since this hit?") only ever has one
// current answer. Also pins the UNIT — unix SECONDS, the same unit every
// timestamp on the hit record uses, so `swapAt >= hit.At` compares like
// with like.
func TestEdgeRecordMainSwapOverwritesAndStoresSeconds(t *testing.T) {
	useTestStore(t)

	if err := recordMainSwap("pool-a@example.com", edgeStandingNow); err != nil {
		t.Fatal(err)
	}
	at, to := readMainSwap()
	if at != edgeStandingNow.Unix() {
		t.Fatalf("at = %d, want %d (unix SECONDS)", at, edgeStandingNow.Unix())
	}
	if to != "pool-a@example.com" {
		t.Fatalf("to = %q, want the swapped-in address verbatim", to)
	}

	later := edgeStandingNow.Add(90 * time.Second)
	if err := recordMainSwap("pool-b@example.com", later); err != nil {
		t.Fatal(err)
	}
	at, to = readMainSwap()
	if at != later.Unix() || to != "pool-b@example.com" {
		t.Fatalf("after a second swap the watermark reads (%d, %q), want (%d, %q)",
			at, to, later.Unix(), "pool-b@example.com")
	}
}

// --- adoptLandedSwap ---

// edgeStandingAdoptable is the one shape adoption accepts: an unresolved
// rate_limit hit with no swap of its own. Every case below breaks exactly
// ONE of its six conditions, so a failure names the condition that stopped
// binding.
func edgeStandingAdoptable() LimitHit {
	return LimitHit{
		Error:       "rate_limit",
		Details:     "You've hit your session limit",
		At:          edgeStandingNow.Unix() - 120,
		SessionID:   "sess-kronos",
		Deliveries:  3,
		Attempts:    2,
		LastAttempt: edgeStandingNow.Unix() - 60,
		ResetAt:     edgeStandingNow.Unix() + 3600,
	}
}

// Every non-adopting arm. Adoption is what stops a herd of hits each
// initiating its own machine-wide swap, but adopting on the WRONG evidence
// is worse than not adopting at all: it tells a hit its account already
// moved, so the rescue skips the rotate and resumes onto the exhausted
// credential.
func TestEdgeAdoptLandedSwapRefusals(t *testing.T) {
	base := edgeStandingAdoptable()
	swapAt := edgeStandingNow.Unix()
	const swapTo = "pool-b@example.com"

	cases := []struct {
		name   string
		hit    LimitHit
		swapAt int64
		swapTo string
	}{
		{"no hit recorded at all", LimitHit{}, swapAt, swapTo},
		{"hit.At == 0 with everything else set", func() LimitHit { h := base; h.At = 0; return h }(), swapAt, swapTo},
		{"already resolved", func() LimitHit { h := base; h.HandledAt = edgeStandingNow.Unix() - 10; h.Outcome = "rotated"; return h }(), swapAt, swapTo},
		{"a non-rate_limit error", func() LimitHit { h := base; h.Error = "overloaded"; return h }(), swapAt, swapTo},
		{"an empty error type", func() LimitHit { h := base; h.Error = ""; return h }(), swapAt, swapTo},
		{"a near-miss error type", func() LimitHit { h := base; h.Error = "rate_limited"; return h }(), swapAt, swapTo},
		{"already carries its own swap", func() LimitHit { h := base; h.SwappedAt = swapAt - 5; h.SwappedTo = "pool-a@example.com"; return h }(), swapAt, swapTo},
		{"the swap predates the hit by one second", base, base.At - 1, swapTo},
		{"the swap predates the hit by an hour", base, base.At - 3600, swapTo},
		{"no watermark at all", base, 0, ""},
		{"a watermark with no timestamp", base, 0, swapTo},
		{"a watermark naming no account", base, swapAt, ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := adoptLandedSwap(c.hit, c.swapAt, c.swapTo)
			if ok {
				t.Fatalf("adoptLandedSwap adopted: %+v", got)
			}
			if got != c.hit {
				t.Fatalf("a refusal changed the record:\n got %+v\nwant %+v", got, c.hit)
			}
		})
	}
}

// The >= boundary, both sides of it. A swap landing in the same SECOND as
// the hit is a swap that landed after it: the record stores seconds, so
// equal timestamps are the ordinary shape of "the rescue swapped the moment
// the hit was recorded", and refusing there would send a second rotate.
func TestEdgeAdoptLandedSwapEqualTimestampsAdopt(t *testing.T) {
	hit := edgeStandingAdoptable()
	got, ok := adoptLandedSwap(hit, hit.At, "pool-b@example.com")
	if !ok {
		t.Fatalf("swapAt == hit.At did not adopt: %+v", got)
	}
	if got.SwappedAt != hit.At || got.SwappedTo != "pool-b@example.com" {
		t.Fatalf("adoption stamped %+v, want SwappedAt=%d SwappedTo=pool-b@example.com", got, hit.At)
	}

	// one second earlier is the other side of the same boundary
	if _, ok := adoptLandedSwap(hit, hit.At-1, "pool-b@example.com"); ok {
		t.Fatal("a swap one second BEFORE the hit was adopted")
	}
}

// Adoption stamps exactly two fields and touches nothing else. Attempts,
// LastAttempt, HandledAt and Outcome belong to the decide chain: bumping
// Attempts here would spend a rotate the pass never made, and stamping
// HandledAt would resolve a hit whose agent is still latched onto the old
// credential.
func TestEdgeAdoptLandedSwapTouchesOnlyTheSwapFields(t *testing.T) {
	hit := edgeStandingAdoptable()
	got, ok := adoptLandedSwap(hit, edgeStandingNow.Unix(), "pool-b@example.com")
	if !ok {
		t.Fatalf("the adoptable fixture was refused: %+v", got)
	}

	want := hit
	want.SwappedAt = edgeStandingNow.Unix()
	want.SwappedTo = "pool-b@example.com"
	if got != want {
		t.Fatalf("adoption changed more than the swap fields:\n got %+v\nwant %+v", got, want)
	}
}

// --- kronos' limit-hook command and its settings entry ---

// The command's shape is exact, and the ORDER within it is load-bearing:
// hookCommandFlags renders the flags a hook process cannot inherit, and
// they have to sit between the verb and its argument the way every other
// kraken hook command in this package builds them.
func TestEdgeKronosLimitHookCommandShape(t *testing.T) {
	const bin = "/opt/kraken-bin/kraken"

	t.Run("default machine", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		t.Setenv("KRAKEN_DB_PATH", "")
		got := kronosLimitHookCommand(bin)
		if want := bin + " limit-hook" + hookCommandFlags() + " --agent kronos"; got != want {
			t.Fatalf("kronosLimitHookCommand = %q, want %q", got, want)
		}
		if !strings.HasSuffix(got, " --agent kronos") {
			t.Fatalf("kronosLimitHookCommand = %q, want it to end in the agent flag", got)
		}
		// never the cell form: a slug where an agent id goes would record a
		// standing agent's limit against a job that does not exist.
		if strings.Contains(got, "--agent kronos ") {
			t.Fatalf("kronosLimitHookCommand = %q, want nothing after the agent id", got)
		}
	})

	t.Run("a non-default db path is baked in ahead of the flag", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		t.Setenv("KRAKEN_DB_PATH", "/custom/kraken.db")
		got := kronosLimitHookCommand(bin)
		if want := bin + " limit-hook" + hookCommandFlags() + " --agent kronos"; got != want {
			t.Fatalf("kronosLimitHookCommand = %q, want %q", got, want)
		}
		if !strings.Contains(got, "/custom/kraken.db") {
			t.Fatalf("kronosLimitHookCommand = %q, want the db path baked in", got)
		}
		if strings.Index(got, "/custom/kraken.db") > strings.Index(got, "--agent") {
			t.Fatalf("the flag run sits AFTER the agent id in %q", got)
		}
	})

	// The replace-merge recognizes kraken's own entry by the "limit-hook"
	// verb, and that predicate has to survive the trailing `--agent <id>`
	// shape — if it does not, every install appends another entry and
	// kronos grows one dead hook per install, forever.
	t.Run("the replace-merge predicate recognizes it", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		t.Setenv("KRAKEN_DB_PATH", "")
		group := map[string]any{
			"matcher": "rate_limit",
			"hooks": []any{
				map[string]any{"type": "command", "command": kronosLimitHookCommand(bin)},
			},
		}
		if !groupRunsVerb(group, "limit-hook") {
			t.Fatalf("groupRunsVerb could not find the limit-hook verb in %q", kronosLimitHookCommand(bin))
		}
	})
}

// edgeStandingHookGroups returns every hook GROUP registered under one
// event of a merged settings blob, as raw maps — the matcher is part of
// what is being measured, so a reader that only returned command strings
// could not see it.
func edgeStandingHookGroups(t *testing.T, settings []byte, event string) []map[string]any {
	t.Helper()
	var parsed map[string]any
	if err := json.Unmarshal(settings, &parsed); err != nil {
		t.Fatalf("merged settings are not valid JSON: %v\n%s", err, settings)
	}
	hooks, _ := parsed["hooks"].(map[string]any)
	groups, _ := hooks[event].([]any)
	var out []map[string]any
	for _, g := range groups {
		if gm, ok := g.(map[string]any); ok {
			out = append(out, gm)
		}
	}
	return out
}

// edgeStandingLimitGroups filters those groups down to the ones running
// kraken's limit-hook verb.
func edgeStandingLimitGroups(t *testing.T, settings []byte) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, g := range edgeStandingHookGroups(t, settings, "StopFailure") {
		if groupRunsVerb(g, "limit-hook") {
			out = append(out, g)
		}
	}
	return out
}

// Installing twice yields ONE entry, and a changed binary path REPLACES
// rather than appending. The documented parallel-server drill runs a
// scratch build from /tmp, so append-if-absent would leave kronos with a
// second StopFailure hook naming a binary that is then deleted — a failing
// hook on every rate-limit death, on the one agent nobody is watching.
func TestEdgeMergeKronosSettingsStopFailureIsIdempotentAndReplaced(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KRAKEN_DB_PATH", "")
	const binA = "/tmp/kraken-test-a"
	const binB = "/usr/local/bin/kraken"

	once, err := mergeKronosSettings(nil, binA)
	if err != nil {
		t.Fatalf("merge(nil): %v", err)
	}
	groups := edgeStandingLimitGroups(t, once)
	if len(groups) != 1 {
		t.Fatalf("a fresh install wrote %d StopFailure limit-hook groups, want exactly 1:\n%s", len(groups), once)
	}
	if m, _ := groups[0]["matcher"].(string); m != "rate_limit" {
		t.Fatalf("matcher = %q, want %q", m, "rate_limit")
	}

	twice, err := mergeKronosSettings(once, binA)
	if err != nil {
		t.Fatalf("merge(once): %v", err)
	}
	if got := len(edgeStandingLimitGroups(t, twice)); got != 1 {
		t.Fatalf("a second install with the same binary left %d limit-hook groups, want 1:\n%s", got, twice)
	}

	thrice, err := mergeKronosSettings(twice, binB)
	if err != nil {
		t.Fatalf("merge(twice, binB): %v", err)
	}
	final := edgeStandingLimitGroups(t, thrice)
	if len(final) != 1 {
		t.Fatalf("installing from a new binary path left %d limit-hook groups, want 1:\n%s", len(final), thrice)
	}
	cmds := edgeHookCommands(t, thrice, "StopFailure")
	for _, c := range cmds {
		if strings.Contains(c, binA) {
			t.Fatalf("the stale binary path survived in %q", c)
		}
	}
	if n := edgeCountString(cmds, kronosLimitHookCommand(binB)); n != 1 {
		t.Fatalf("StopFailure carries %d copies of the current command, want 1: %v", n, cmds)
	}
}

// A hand-edited StopFailure key that is not an array of groups is a shape
// Claude Code cannot run anyway. Replacing it outright is what keeps the
// hook really installed; returning early and reporting success would leave
// kronos permanently without its fast trigger on a settings file that looks
// fine.
func TestEdgeMergeKronosSettingsReplacesANonArrayStopFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KRAKEN_DB_PATH", "")
	const bin = "/usr/local/bin/kraken"

	for _, weird := range []string{
		`{"hooks":{"StopFailure":{"note":"hand edited into an object"}}}`,
		`{"hooks":{"StopFailure":"a bare string"}}`,
		`{"hooks":{"StopFailure":null}}`,
		`{"hooks":{"StopFailure":42}}`,
		`{"hooks":{"StopFailure":[]}}`,
	} {
		t.Run(weird, func(t *testing.T) {
			merged, err := mergeKronosSettings([]byte(weird), bin)
			if err != nil {
				t.Fatalf("merge: %v", err)
			}
			groups := edgeStandingLimitGroups(t, merged)
			if len(groups) != 1 {
				t.Fatalf("StopFailure carries %d limit-hook groups, want 1:\n%s", len(groups), merged)
			}
			if m, _ := groups[0]["matcher"].(string); m != "rate_limit" {
				t.Fatalf("matcher = %q, want %q", m, "rate_limit")
			}
		})
	}
}

// The whole file is merged, never clobbered: somebody else's StopFailure
// group, somebody else's statusLine, and any unrelated top-level key are
// all left exactly as they were, and kraken's three existing hook groups
// keep their single entry each.
func TestEdgeMergeKronosSettingsLeavesEverythingElseAlone(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KRAKEN_DB_PATH", "")
	const bin = "/usr/local/bin/kraken"

	existing := `{
	  "hooks": {
	    "StopFailure": [
	      {"matcher": "rate_limit", "hooks": [{"type": "command", "command": "/opt/somebody/else --report"}]}
	    ]
	  },
	  "statusLine": {"type": "command", "command": "/opt/somebody/else/statusline"},
	  "someUserKey": {"kept": true}
	}`

	merged, err := mergeKronosSettings([]byte(existing), bin)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}

	cmds := edgeHookCommands(t, merged, "StopFailure")
	if edgeCountString(cmds, "/opt/somebody/else --report") != 1 {
		t.Fatalf("a foreign StopFailure entry was disturbed: %v", cmds)
	}
	if n := edgeCountString(cmds, kronosLimitHookCommand(bin)); n != 1 {
		t.Fatalf("StopFailure carries %d copies of kraken's own entry, want 1: %v", n, cmds)
	}

	var parsed map[string]any
	if err := json.Unmarshal(merged, &parsed); err != nil {
		t.Fatalf("merged settings are not valid JSON: %v", err)
	}
	status, _ := parsed["statusLine"].(map[string]any)
	if cmd, _ := status["command"].(string); cmd != "/opt/somebody/else/statusline" {
		t.Fatalf("a foreign statusLine was rewritten: %q", cmd)
	}
	user, _ := parsed["someUserKey"].(map[string]any)
	if kept, _ := user["kept"].(bool); !kept {
		t.Fatalf("an unrelated top-level key was dropped: %v", parsed["someUserKey"])
	}

	// kraken's three pre-existing groups still carry exactly one entry each
	for _, ev := range []string{"PreToolUse", "PostCompact", "UserPromptSubmit", "PostToolUse"} {
		if len(edgeHookCommands(t, merged, ev)) == 0 {
			t.Errorf("event %q lost its kraken entry", ev)
		}
	}
}

// A settings file kraken cannot parse is a file kraken must not overwrite —
// the StopFailure addition does not change that.
func TestEdgeMergeKronosSettingsRefusesInvalidJSON(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := mergeKronosSettings([]byte("{not json"), "/usr/local/bin/kraken"); err == nil {
		t.Fatal("mergeKronosSettings over unparseable JSON returned nil error")
	}
}

// --- standingStallReason ---

// The quiet kinds. A healthy transcript and carl's own esc-interrupt must
// both produce nothing at all: an interrupted turn is a deliberate human
// act, and treating it as a stall would have the daemon typing at a pane
// carl just stopped.
func TestEdgeStandingStallReasonQuietKinds(t *testing.T) {
	for _, id := range standingAgentIDs() {
		for _, st := range []core.Stall{
			{},
			{Kind: ""},
			{Kind: "interrupted"},
			{Kind: "interrupted", Text: "[Request interrupted by user]"},
			{Kind: "something-claude-code-invented-later", Text: "who knows"},
		} {
			reason, isLimit, retryable := standingStallReason(id, st)
			if reason != "" || isLimit || retryable {
				t.Errorf("standingStallReason(%q, %+v) = (%q, %v, %v), want (\"\", false, false)",
					id, st, reason, isLimit, retryable)
			}
		}
	}
}

// An api-error's retryability is the STALL's, verbatim. Deciding it here
// would give the standing arm a second opinion about what a transient
// failure is, and the two would drift.
func TestEdgeStandingStallReasonAPIErrorMirrorsTransient(t *testing.T) {
	for _, id := range standingAgentIDs() {
		for _, transient := range []bool{true, false} {
			st := core.Stall{Kind: "api-error", Text: "API Error: Unable to connect to API (ENOTFOUND)", Transient: transient}
			reason, isLimit, retryable := standingStallReason(id, st)
			if reason == "" {
				t.Errorf("standingStallReason(%q, api-error transient=%v) gave an empty reason", id, transient)
			}
			if isLimit {
				t.Errorf("standingStallReason(%q, api-error) reported isLimit — an API fault is not an exhausted account", id)
			}
			if retryable != transient {
				t.Errorf("standingStallReason(%q, api-error transient=%v) retryable = %v, want %v",
					id, transient, retryable, transient)
			}
		}
	}
}

// A usage-limit reason carries the banner text, so carl can see WHICH wall
// was hit (and so the reset clause survives into the recorded details,
// which is the only thing that can wake a parked hit).
func TestEdgeStandingStallReasonUsageLimitEmbedsTheText(t *testing.T) {
	const text = "You've hit your session limit · resets 4:50am (America/New_York)"
	for _, id := range standingAgentIDs() {
		reason, isLimit, _ := standingStallReason(id, core.Stall{Kind: "usage-limit", Text: text})
		if !isLimit {
			t.Errorf("standingStallReason(%q, usage-limit) reported isLimit=false", id)
		}
		if !strings.Contains(reason, text) {
			t.Errorf("standingStallReason(%q, usage-limit) = %q, want it to embed %q", id, reason, text)
		}
	}
}

// A limit banner Claude Code wrote with no text at all still has to produce
// a reason: the reason is what the pass routes and dedupes on, and an empty
// one reads as "no stall" — the standing agent would sit exhausted forever
// with nothing recorded.
func TestEdgeStandingStallReasonUsageLimitWithNoTextStillReports(t *testing.T) {
	for _, id := range standingAgentIDs() {
		reason, isLimit, _ := standingStallReason(id, core.Stall{Kind: "usage-limit"})
		if reason == "" {
			t.Errorf("standingStallReason(%q, usage-limit with empty text) gave an empty reason", id)
		}
		if !isLimit {
			t.Errorf("standingStallReason(%q, usage-limit with empty text) reported isLimit=false", id)
		}
		if !strings.Contains(reason, id) {
			t.Errorf("standingStallReason(%q, …) = %q, want it to name the agent", id, reason)
		}
	}
}

// Every non-empty reason names its agent and NEVER says "zeus". The reason
// is what reaches carl's notification and the stored record: a standing
// agent's stall reported as zeus' would send him looking for a cell that
// does not exist, and — worse — `IsUsageLimitReason` and the cell health
// vocabulary are prefix-matched, so borrowed wording is a real routing
// hazard rather than a cosmetic one.
func TestEdgeStandingStallReasonNamesTheAgentAndNeverZeus(t *testing.T) {
	stalls := []core.Stall{
		{Kind: "usage-limit", Text: "You've hit your weekly limit"},
		{Kind: "usage-limit", Text: "Usage credits are required for this model."},
		{Kind: "api-error", Text: "API Error: Unable to connect to API (ENOTFOUND)", Transient: true},
		{Kind: "api-error", Text: "API Error: Credit balance is too low", Transient: false},
	}
	for _, id := range standingAgentIDs() {
		for _, st := range stalls {
			reason, _, _ := standingStallReason(id, st)
			if reason == "" {
				t.Fatalf("standingStallReason(%q, %+v) gave an empty reason", id, st)
			}
			if !strings.Contains(reason, id) {
				t.Errorf("standingStallReason(%q, %+v) = %q, want it to name the agent", id, st, reason)
			}
			if strings.Contains(strings.ToLower(reason), "zeus") {
				t.Errorf("standingStallReason(%q, %+v) = %q — a standing agent is not zeus", id, st, reason)
			}
		}
	}
}

// --- standingHealth ---

// edgeStandingStub is what the descriptor's four func fields answer with.
type edgeStandingStub struct {
	enabled     bool
	recorded    string
	hasRecorded bool
	peer        *core.Peer
	respawnErr  error
}

// edgeStandingAgentOf builds a descriptor row from a stub. Session and
// AgentName equal the id for both real rows, so the fixture does the same.
func edgeStandingAgentOf(id string, s edgeStandingStub) standingAgent {
	return standingAgent{
		ID:              id,
		Session:         id,
		AgentName:       id,
		Enabled:         func() bool { return s.enabled },
		RecordedSession: func(*store.Store) (string, bool) { return s.recorded, s.hasRecorded },
		Find:            func([]*core.Peer) *core.Peer { return s.peer },
		Respawn:         func(*store.Store, map[string]bool) error { return s.respawnErr },
	}
}

// edgeStandingPeer builds the shape DiscoverAll produces for a standing
// agent's own pane: a depth-0 foreground claude in the agent's session.
func edgeStandingPeer(session, name, sessionID string) *core.Peer {
	return &core.Peer{
		TmuxSession: session, Pane: "%1", Agent: "claude", Depth: 0, Role: "main",
		Name: name, SessionID: sessionID,
	}
}

// edgeStandingTranscript fabricates a session transcript where
// core.TranscriptPathForSession globs for one — under the isolated HOME, so
// nothing here reads a real conversation.
func edgeStandingTranscript(t *testing.T, sessionID string, lines ...string) string {
	t.Helper()
	dir := filepath.Join(util.Home(), ".claude", "projects", "-Users-x-kraken-kronos")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	p := filepath.Join(dir, sessionID+".jsonl")
	body := ""
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

// edgeStandingLimitBanner is a real synthetic usage-limit record, the shape
// core.LastStall classifies as Kind "usage-limit".
const edgeStandingLimitBanner = `{"type":"assistant","message":{"model":"<synthetic>","content":[{"type":"text","text":"You've hit your session limit · resets 4:50am (America/New_York)"}]}}`

// Every arm of standingHealth that must classify NOTHING. Each one is a
// state the health pass sees routinely — a booting agent, an agent that has
// never been recorded, a transcript that has not been written yet — and
// each would otherwise be classified off evidence that is not the agent's.
func TestEdgeStandingHealthQuietArms(t *testing.T) {
	t.Run("no peer at all", func(t *testing.T) {
		useTestStore(t)
		a := edgeStandingAgentOf("kronos", edgeStandingStub{enabled: true})
		edgeStandingTranscript(t, "sess-kronos", edgeStandingLimitBanner)
		reason, isLimit, retryable := standingHealth(a, nil, "sess-kronos")
		edgeStandingWantQuiet(t, "nil peer", reason, isLimit, retryable)
	})

	t.Run("a nameless peer is still starting", func(t *testing.T) {
		useTestStore(t)
		edgeStandingTranscript(t, "sess-kronos", edgeStandingLimitBanner)
		a := edgeStandingAgentOf("kronos", edgeStandingStub{
			enabled: true, peer: edgeStandingPeer("kronos", "", ""),
		})
		reason, isLimit, retryable := standingHealth(a, []*core.Peer{a.Find(nil)}, "sess-kronos")
		edgeStandingWantQuiet(t, "nameless peer", reason, isLimit, retryable)
	})

	t.Run("no recorded session id", func(t *testing.T) {
		useTestStore(t)
		edgeStandingTranscript(t, "sess-kronos", edgeStandingLimitBanner)
		a := edgeStandingAgentOf("kronos", edgeStandingStub{
			enabled: true, peer: edgeStandingPeer("kronos", "kronos", "sess-kronos"),
		})
		reason, isLimit, retryable := standingHealth(a, []*core.Peer{a.Find(nil)}, "")
		edgeStandingWantQuiet(t, "empty recorded id", reason, isLimit, retryable)
	})

	t.Run("an unresolvable transcript path", func(t *testing.T) {
		useTestStore(t)
		a := edgeStandingAgentOf("kronos", edgeStandingStub{
			enabled: true, peer: edgeStandingPeer("kronos", "kronos", "sess-kronos"),
		})
		// no transcript was ever written for this id
		reason, isLimit, retryable := standingHealth(a, []*core.Peer{a.Find(nil)}, "sess-never-written")
		edgeStandingWantQuiet(t, "unresolvable transcript", reason, isLimit, retryable)
	})

	t.Run("an empty transcript file", func(t *testing.T) {
		useTestStore(t)
		edgeStandingTranscript(t, "sess-empty")
		a := edgeStandingAgentOf("kronos", edgeStandingStub{
			enabled: true, peer: edgeStandingPeer("kronos", "kronos", "sess-empty"),
		})
		reason, isLimit, retryable := standingHealth(a, []*core.Peer{a.Find(nil)}, "sess-empty")
		edgeStandingWantQuiet(t, "empty transcript", reason, isLimit, retryable)
	})
}

func edgeStandingWantQuiet(t *testing.T, what, reason string, isLimit, retryable bool) {
	t.Helper()
	if reason != "" || isLimit || retryable {
		t.Fatalf("%s: standingHealth = (%q, %v, %v), want (\"\", false, false)", what, reason, isLimit, retryable)
	}
}

// Classification runs on the RECORDED session id and nothing else. A peer's
// own SessionID can be guessed from cwd plus the newest transcript
// (Peer.SessionIDGuessed), and a rescue driven off a guess would kill a
// pane and resume a conversation that is not the agent's. The fixture makes
// the two disagree: the peer points at an exhausted session, the recorded
// id points at a healthy one, and the healthy answer is the only correct
// one.
func TestEdgeStandingHealthClassifiesOnlyTheRecordedSession(t *testing.T) {
	useTestStore(t)
	edgeStandingTranscript(t, "sess-guessed", edgeStandingLimitBanner)
	edgeStandingTranscript(t, "sess-recorded",
		`{"type":"assistant","message":{"model":"claude-fable-5","content":[{"type":"text","text":"all good"}]}}`)

	peer := edgeStandingPeer("kronos", "kronos", "sess-guessed")
	peer.SessionIDGuessed = true
	a := edgeStandingAgentOf("kronos", edgeStandingStub{enabled: true, peer: peer})

	reason, isLimit, retryable := standingHealth(a, []*core.Peer{peer}, "sess-recorded")
	if reason != "" || isLimit || retryable {
		t.Fatalf("standingHealth classified the PEER's session, not the recorded one: (%q, %v, %v)",
			reason, isLimit, retryable)
	}
}

// --- standingKillTarget ---

// Every refusal. The kill is a SIGKILL of a live process, so the guard has
// to demand positive identity evidence: a refusal costs one deferred round,
// a false positive destroys a stranger's pane.
func TestEdgeStandingKillTargetRefusals(t *testing.T) {
	a := edgeStandingAgentOf("kronos", edgeStandingStub{enabled: true})

	cases := []struct {
		name     string
		peers    []*core.Peer
		recorded string
	}{
		{"no peers at all", nil, "sess-kronos"},
		{"an empty snapshot", []*core.Peer{}, "sess-kronos"},
		{
			"a nameless claude with no session id and nothing recorded",
			[]*core.Peer{edgeStandingPeer("kronos", "", "")},
			"",
		},
		{
			"a nameless claude with no session id",
			[]*core.Peer{edgeStandingPeer("kronos", "", "")},
			"sess-kronos",
		},
		{
			"a foreign name in the agent's session",
			[]*core.Peer{edgeStandingPeer("kronos", "somebody-else", "sess-other")},
			"sess-kronos",
		},
		{
			"the agent's own name in ANOTHER session",
			[]*core.Peer{edgeStandingPeer("cell-something-3", "kronos", "sess-kronos")},
			"sess-kronos",
		},
		{
			"the recorded session id in ANOTHER session",
			[]*core.Peer{edgeStandingPeer("cell-something-3", "", "sess-kronos")},
			"sess-kronos",
		},
		{
			"a nested (depth > 0) agent",
			[]*core.Peer{func() *core.Peer { p := edgeStandingPeer("kronos", "kronos", "sess-kronos"); p.Depth = 1; return p }()},
			"sess-kronos",
		},
		{
			"a shell pane holding no agent",
			[]*core.Peer{{TmuxSession: "kronos", Pane: "%1", Role: "shell"}},
			"sess-kronos",
		},
		{
			"a codex pane",
			[]*core.Peer{func() *core.Peer {
				p := edgeStandingPeer("kronos", "kronos", "sess-kronos")
				p.Agent = "codex"
				return p
			}()},
			"sess-kronos",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, why := standingKillTarget(a, c.peers, c.recorded)
			if got != nil {
				t.Fatalf("standingKillTarget returned a peer to kill: %+v", got)
			}
			if strings.TrimSpace(why) == "" {
				t.Fatal("a refusal reported no reason — a deferral nobody can read is a silent stall")
			}
		})
	}
}

// The recorded-session-id clause is the second half of the identity test,
// and it exists precisely for the peer the NAME clause cannot vouch for:
// Peer.Name only appears once claude writes its session file, so a kronos
// that is up and working but not yet named is exactly the case a name-only
// guard would defer forever. A refusal is a deferral, and deferring on an
// agent whose own recorded session id is sitting in the pane would leave a
// limit-stalled kronos dead until carl typed into it.
//
// (JOIN NOTE — the contract states both "matches BY IDENTITY: … or
// (recorded != "" and SessionID == recorded)" and "a nameless peer …
// returns (nil, reason)". Read strictly the second sentence makes the
// second identity clause unreachable, since a NAMED peer either matches
// AgentName or is a "foreign name" that the same sentence also refuses. A
// reading that renders a stated clause dead is the wrong reading, so the
// refusal list is taken to mean "a peer with no identity evidence", and
// this is the case that separates the two readings.)
func TestEdgeStandingKillTargetAcceptsTheRecordedSessionIDOfANamelessPeer(t *testing.T) {
	a := edgeStandingAgentOf("kronos", edgeStandingStub{enabled: true})
	peer := edgeStandingPeer("kronos", "", "sess-kronos")

	got, why := standingKillTarget(a, []*core.Peer{peer}, "sess-kronos")
	if got != peer {
		t.Fatalf("standingKillTarget = (%+v, %q), want the peer whose SessionID is the recorded one", got, why)
	}
}

// --- the registry rows ---

// Every descriptor field the passes dereference must be populated on every
// row: a nil Find or a nil Enabled is a panic inside the daemon tick, which
// takes the whole scheduler down rather than one agent's rescue.
func TestEdgeStandingAgentsRowsAreFullyPopulated(t *testing.T) {
	rows := standingAgents()
	ids := standingAgentIDs()
	if len(rows) != len(ids) {
		t.Fatalf("standingAgents() has %d rows, standingAgentIDs() has %d", len(rows), len(ids))
	}
	seen := map[string]bool{}
	for i, a := range rows {
		if a.ID != ids[i] {
			t.Errorf("row %d id = %q, want %q (the two lists are ordered together)", i, a.ID, ids[i])
		}
		if seen[a.ID] {
			t.Errorf("duplicate row id %q — two rows would share one kv key", a.ID)
		}
		seen[a.ID] = true
		if a.Session == "" {
			t.Errorf("row %q has an empty tmux session name", a.ID)
		}
		if a.AgentName == "" {
			t.Errorf("row %q has an empty agent name — the kill guard has nothing to match on", a.ID)
		}
		if a.Enabled == nil {
			t.Errorf("row %q has a nil Enabled", a.ID)
		}
		if a.RecordedSession == nil {
			t.Errorf("row %q has a nil RecordedSession", a.ID)
		}
		if a.Find == nil {
			t.Errorf("row %q has a nil Find", a.ID)
		}
		if a.Respawn == nil {
			t.Errorf("row %q has a nil Respawn", a.ID)
		}
	}
}

// A machine that has opted into NEITHER feature has no standing agents
// enabled, and the passes contribute nothing at all. Both gates are file
// gates under HOME, so an isolated HOME is a machine with neither.
func TestEdgeStandingAgentsAreDisabledInAnEmptyHome(t *testing.T) {
	useTestStore(t) // isolates HOME
	t.Setenv("SLACK_USER_TOKEN", "")
	t.Setenv("SLACK_D_COOKIE", "")

	for _, a := range standingAgents() {
		if a.Enabled == nil {
			t.Fatalf("row %q has a nil Enabled", a.ID)
		}
		if a.Enabled() {
			t.Errorf("row %q reports enabled on a machine with no opt-in file", a.ID)
		}
	}
}

// --- the passes: the arms that must do nothing ---

// edgeStandingSeedHit writes a record straight into the kv row a key names.
func edgeStandingSeedHit(t *testing.T, st *store.Store, key string, h LimitHit) {
	t.Helper()
	data, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetKV(key, string(data)); err != nil {
		t.Fatal(err)
	}
}

// edgeStandingStoreJob creates a real job row so a job-keyed record has an
// id to live under.
func edgeStandingStoreJob(t *testing.T, st *store.Store, slug string) *Job {
	t.Helper()
	row := store.Job{Slug: slug, Repo: "monolith", State: "working"}
	id, err := st.CreateJob(&row)
	if err != nil {
		t.Fatalf("create job %s: %v", slug, err)
	}
	return &Job{ID: id, Slug: slug, Repo: "monolith", State: "working"}
}

// edgeStandingStubRespawn swaps the respawn seam and returns a counter plus
// the ids it was called for. Nothing in this file may kill a real pane.
func edgeStandingStubRespawn(t *testing.T, err error) *[]string {
	t.Helper()
	calls := &[]string{}
	prev := standingRespawn
	standingRespawn = func(a standingAgent, _ *store.Store, _ map[string]bool) error {
		*calls = append(*calls, a.ID)
		return err
	}
	t.Cleanup(func() { standingRespawn = prev })
	return calls
}

// A tick with nothing recorded, and a tick whose record is already
// resolved, must both be free: no respawn, no write, no swap. Nothing ever
// deletes this row, so without the resolved short-circuit every standing
// agent that has EVER hit a limit would pay a usage-snapshot read and a
// full rotation ranking every 15 seconds for the rest of the daemon's life.
func TestEdgeStandingLimitRescueSkipsEmptyAndResolvedHits(t *testing.T) {
	t.Run("no hit recorded", func(t *testing.T) {
		st := useTestStore(t)
		calls := edgeStandingStubRespawn(t, nil)
		d := newDaemon()
		a := edgeStandingAgentOf("kronos", edgeStandingStub{
			enabled: true, recorded: "sess-kronos", hasRecorded: true,
			peer: edgeStandingPeer("kronos", "kronos", "sess-kronos"),
		})

		d.standingLimitRescue(a, st, []*core.Peer{a.Find(nil)}, map[string]bool{"kronos": true}, edgeStandingNow)

		if len(*calls) != 0 {
			t.Fatalf("standingRespawn was called %d times with no hit recorded", len(*calls))
		}
		if got := readLimitHitByKey(standingHitKey("kronos")); got != (LimitHit{}) {
			t.Fatalf("a no-hit tick wrote a record: %+v", got)
		}
	})

	t.Run("an already-resolved hit", func(t *testing.T) {
		st := useTestStore(t)
		calls := edgeStandingStubRespawn(t, nil)
		key := standingHitKey("kronos")
		resolved := LimitHit{
			Error: "rate_limit", At: edgeStandingNow.Unix() - 3600, Deliveries: 2,
			SwappedAt: edgeStandingNow.Unix() - 3500, SwappedTo: "pool-b@example.com",
			HandledAt: edgeStandingNow.Unix() - 3400, Outcome: "rotated", Attempts: 1,
		}
		edgeStandingSeedHit(t, st, key, resolved)

		d := newDaemon()
		a := edgeStandingAgentOf("kronos", edgeStandingStub{
			enabled: true, recorded: "sess-kronos", hasRecorded: true,
			peer: edgeStandingPeer("kronos", "kronos", "sess-kronos"),
		})
		d.standingLimitRescue(a, st, []*core.Peer{a.Find(nil)}, map[string]bool{"kronos": true}, edgeStandingNow)

		if len(*calls) != 0 {
			t.Fatalf("standingRespawn was called %d times on a resolved hit", len(*calls))
		}
		if got := readLimitHitByKey(key); got != resolved {
			t.Fatalf("a resolved hit was rewritten:\n got %+v\nwant %+v", got, resolved)
		}
	})
}

// A resume round that cannot identify the agent's own claude must NOT
// respawn anything and must NOT resolve the hit — but it is counted, so an
// agent that can never be reached parks instead of writing this event every
// tick forever.
func TestEdgeStandingLimitRescueRefusedKillTargetCountsOutWithoutRespawning(t *testing.T) {
	st := useTestStore(t)
	calls := edgeStandingStubRespawn(t, nil)
	key := standingHitKey("kronos")
	edgeStandingSeedHit(t, st, key, LimitHit{
		Error: "rate_limit", At: edgeStandingNow.Unix() - 300, Deliveries: 1,
		SwappedAt: edgeStandingNow.Unix() - 120, SwappedTo: "pool-b@example.com",
	})

	d := newDaemon()
	a := edgeStandingAgentOf("kronos", edgeStandingStub{
		enabled: true, recorded: "sess-kronos", hasRecorded: true,
	})
	// the session is live, but its only pane holds a stranger — no identity
	// evidence, so the round is refused
	strangers := []*core.Peer{edgeStandingPeer("kronos", "somebody-else", "sess-nothing")}
	d.standingLimitRescue(a, st, strangers, map[string]bool{"kronos": true}, edgeStandingNow)

	if len(*calls) != 0 {
		t.Fatalf("standingRespawn ran %d times despite a refused kill target", len(*calls))
	}
	got := readLimitHitByKey(key)
	if got.HandledAt != 0 {
		t.Fatalf("a refused resume RESOLVED the hit: %+v", got)
	}
	if got.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1 — the round is counted so the hit can eventually park", got.Attempts)
	}
	if got.SwappedAt == 0 {
		t.Errorf("the landed swap was erased by a failed resume round: %+v", got)
	}
}

// A respawn that FAILS is the same "no-agents" round: the hit stays open,
// the swap stamp survives, and the counter advances. Resolving here would
// strand a standing agent whose account moved and whose process is still
// latched onto the old one.
func TestEdgeStandingLimitRescueRespawnFailureLeavesTheHitOpen(t *testing.T) {
	st := useTestStore(t)
	calls := edgeStandingStubRespawn(t, os.ErrPermission)
	key := standingHitKey("kronos")
	edgeStandingSeedHit(t, st, key, LimitHit{
		Error: "rate_limit", At: edgeStandingNow.Unix() - 300, Deliveries: 1,
		SwappedAt: edgeStandingNow.Unix() - 120, SwappedTo: "pool-b@example.com",
	})

	d := newDaemon()
	a := edgeStandingAgentOf("kronos", edgeStandingStub{
		enabled: true, recorded: "sess-kronos", hasRecorded: true,
	})
	peer := edgeStandingPeer("kronos", "kronos", "sess-kronos")
	d.standingLimitRescue(a, st, []*core.Peer{peer}, map[string]bool{"kronos": true}, edgeStandingNow)

	if len(*calls) != 1 {
		t.Fatalf("standingRespawn ran %d times, want exactly 1", len(*calls))
	}
	got := readLimitHitByKey(key)
	if got.HandledAt != 0 || got.Outcome != "" {
		t.Fatalf("a failed respawn resolved the hit: %+v", got)
	}
	if got.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1", got.Attempts)
	}
	if got.SwappedAt != edgeStandingNow.Unix()-120 || got.SwappedTo != "pool-b@example.com" {
		t.Errorf("the swap stamp did not survive the failed round: %+v", got)
	}
}

// A watermark that PREDATES the hit is evidence about an earlier event, and
// adopting it would tell this hit its account already moved — the rescue
// would skip its rotate and resume onto the exhausted credential. The
// record must come back untouched, and since nothing changed there is
// nothing to persist.
func TestEdgeStandingLimitRescueIgnoresAWatermarkOlderThanTheHit(t *testing.T) {
	st := useTestStore(t)
	edgeStandingStubRespawn(t, nil)
	key := standingHitKey("kronos")
	hit := LimitHit{
		Error: "rate_limit", At: edgeStandingNow.Unix() - 300, Deliveries: 1,
		Details: "You've hit your session limit",
	}
	edgeStandingSeedHit(t, st, key, hit)
	// a swap that landed a full minute BEFORE the hit was recorded
	if err := recordMainSwap("pool-b@example.com", time.Unix(hit.At-60, 0)); err != nil {
		t.Fatal(err)
	}

	d := newDaemon()
	a := edgeStandingAgentOf("kronos", edgeStandingStub{
		enabled: true, recorded: "sess-kronos", hasRecorded: true,
	})
	// the agent's session is NOT live, so the decide chain stops at its
	// liveness gate and the only thing this tick could have written is an
	// adoption
	d.standingLimitRescue(a, st, nil, map[string]bool{}, edgeStandingNow)

	got := readLimitHitByKey(key)
	if got.SwappedAt != 0 || got.SwappedTo != "" {
		t.Fatalf("a watermark older than the hit was adopted: %+v", got)
	}
	if got != hit {
		t.Fatalf("the record changed on a no-op tick:\n got %+v\nwant %+v", got, hit)
	}
}

// A watermark naming no account is not evidence of anything: adopting it
// would stamp SwappedAt with an empty SwappedTo, which reads downstream as
// "swapped to nowhere" and suppresses the rotate that was owed.
func TestEdgeStandingLimitRescueIgnoresAWatermarkWithNoAccount(t *testing.T) {
	st := useTestStore(t)
	edgeStandingStubRespawn(t, nil)
	key := standingHitKey("iris")
	hit := LimitHit{
		Error: "rate_limit", At: edgeStandingNow.Unix() - 300, Deliveries: 1,
	}
	edgeStandingSeedHit(t, st, key, hit)
	if err := st.SetKV("main_swap", `{"at":`+strconv.FormatInt(edgeStandingNow.Unix(), 10)+`,"to":""}`); err != nil {
		t.Fatal(err)
	}

	d := newDaemon()
	a := edgeStandingAgentOf("iris", edgeStandingStub{
		enabled: true, recorded: "sess-iris", hasRecorded: true,
	})
	d.standingLimitRescue(a, st, nil, map[string]bool{}, edgeStandingNow)

	if got := readLimitHitByKey(key); got != hit {
		t.Fatalf("an account-less watermark changed the record:\n got %+v\nwant %+v", got, hit)
	}
}

// The health pass has two gates before it does anything, and both are
// absence conditions: an agent whose feature is off has no pane to read,
// and an agent whose session is not live has nothing to classify. Either
// one firing must leave the kv row untouched — a hit recorded for a dead
// agent is one nothing can ever resolve.
func TestEdgeStandingHealthPassSkipsWhenDisabledOrNotLive(t *testing.T) {
	t.Run("feature disabled", func(t *testing.T) {
		st := useTestStore(t)
		edgeStandingTranscript(t, "sess-kronos", edgeStandingLimitBanner)
		d := newDaemon()
		a := edgeStandingAgentOf("kronos", edgeStandingStub{
			enabled: false, recorded: "sess-kronos", hasRecorded: true,
			peer: edgeStandingPeer("kronos", "kronos", "sess-kronos"),
		})
		d.standingHealthPass(a, st, []*core.Peer{a.Find(nil)}, map[string]bool{"kronos": true}, edgeStandingNow)
		edgeStandingWantNoHit(t, "kronos", "a disabled agent")
	})

	t.Run("session not live", func(t *testing.T) {
		st := useTestStore(t)
		edgeStandingTranscript(t, "sess-kronos", edgeStandingLimitBanner)
		d := newDaemon()
		a := edgeStandingAgentOf("kronos", edgeStandingStub{
			enabled: true, recorded: "sess-kronos", hasRecorded: true,
			peer: edgeStandingPeer("kronos", "kronos", "sess-kronos"),
		})
		d.standingHealthPass(a, st, []*core.Peer{a.Find(nil)}, map[string]bool{}, edgeStandingNow)
		edgeStandingWantNoHit(t, "kronos", "an agent whose session is not live")
	})
}

// With no recorded session id — and with a peer that has not been named yet
// — the health pass has nothing it is allowed to classify, so it records
// nothing however loud the transcript beside it is.
func TestEdgeStandingHealthPassRecordsNothingWithoutIdentity(t *testing.T) {
	t.Run("no recorded session", func(t *testing.T) {
		st := useTestStore(t)
		edgeStandingTranscript(t, "sess-kronos", edgeStandingLimitBanner)
		d := newDaemon()
		a := edgeStandingAgentOf("kronos", edgeStandingStub{
			enabled: true, recorded: "", hasRecorded: false,
			peer: edgeStandingPeer("kronos", "kronos", "sess-kronos"),
		})
		d.standingHealthPass(a, st, []*core.Peer{a.Find(nil)}, map[string]bool{"kronos": true}, edgeStandingNow)
		edgeStandingWantNoHit(t, "kronos", "an agent with no recorded session id")
	})

	t.Run("a nameless peer", func(t *testing.T) {
		st := useTestStore(t)
		edgeStandingTranscript(t, "sess-kronos", edgeStandingLimitBanner)
		d := newDaemon()
		a := edgeStandingAgentOf("kronos", edgeStandingStub{
			enabled: true, recorded: "sess-kronos", hasRecorded: true,
			peer: edgeStandingPeer("kronos", "", ""),
		})
		d.standingHealthPass(a, st, []*core.Peer{a.Find(nil)}, map[string]bool{"kronos": true}, edgeStandingNow)
		edgeStandingWantNoHit(t, "kronos", "an agent still starting")
	})

	t.Run("no peer at all", func(t *testing.T) {
		st := useTestStore(t)
		edgeStandingTranscript(t, "sess-kronos", edgeStandingLimitBanner)
		d := newDaemon()
		a := edgeStandingAgentOf("kronos", edgeStandingStub{
			enabled: true, recorded: "sess-kronos", hasRecorded: true,
		})
		d.standingHealthPass(a, st, nil, map[string]bool{"kronos": true}, edgeStandingNow)
		edgeStandingWantNoHit(t, "kronos", "an agent with no pane in the snapshot")
	})
}

func edgeStandingWantNoHit(t *testing.T, agentID, what string) {
	t.Helper()
	if got := readLimitHitByKey(standingHitKey(agentID)); got != (LimitHit{}) {
		t.Fatalf("%s recorded a hit: %+v", what, got)
	}
}
