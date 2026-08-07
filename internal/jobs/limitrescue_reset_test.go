package jobs

import (
	"encoding/json"
	"testing"
	"time"
)

// limitrescue_reset_test.go — the reset re-arm added 2026-08-07:
// parseLimitReset and its staleness cap, the exhausted-attempts gate's reset
// branch, the park that stays open, the unbounded same-error fold that keeps
// the attempt counter honest under the transcript trigger's re-deliveries,
// the fold's reset-clause details adoption, and the reset nudge's own
// failure allowance. Evidence behind the feature: four cells on 2026-08-06
// sat 42m–10h37m dead past a reset their own banner named, because a
// parked-no-target hit was terminal.

// resetHitAnchor returns the fixed hit-time anchor the parse tests share:
// 2026-08-06 01:06:45 in America/New_York, the moment the measured fold
// actually happened.
func resetHitAnchor(t *testing.T) time.Time {
	t.Helper()
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	return time.Date(2026, 8, 6, 1, 6, 45, 0, loc)
}

func TestLimitResetParseMeasuredBanners(t *testing.T) {
	hitAt := resetHitAnchor(t)
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		details string
		want    time.Time
	}{
		{
			// The exact recorded shape from the 2026-08-06 fold: hit at
			// 01:06 EDT, banner names 4:50am — same day, 3h44m later.
			name:    "session limit with zone",
			details: "zeus hit a usage limit: You've hit your session limit · resets 4:50am (America/New_York)",
			want:    time.Date(2026, 8, 6, 4, 50, 0, 0, loc),
		},
		{
			name:    "no minutes",
			details: "resets 4am (America/New_York)",
			want:    time.Date(2026, 8, 6, 4, 0, 0, 0, loc),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseLimitReset(tc.details, hitAt)
			if got != tc.want.Unix() {
				t.Errorf("parseLimitReset(%q) = %d (%s), want %d (%s)",
					tc.details, got, time.Unix(got, 0).In(loc), tc.want.Unix(), tc.want)
			}
		})
	}

	// The noon/midnight halves of the am-pm conversion get their own
	// anchors INSIDE the cap: from the shared 01:06 anchor a 12pm target is
	// 10h53m out and the cap answers already-due for a correct conversion
	// and a broken one alike, so anchored there the case pins nothing. From
	// 10:55, a correct 12pm is 1h5m out while a 12pm mis-read as 00:00
	// would roll to next-day midnight, get capped, and answer already-due —
	// a visible mismatch.
	t.Run("noon is 12pm", func(t *testing.T) {
		anchor := time.Date(2026, 8, 6, 10, 55, 0, 0, loc)
		got := parseLimitReset("resets 12pm (America/New_York)", anchor)
		want := time.Date(2026, 8, 6, 12, 0, 0, 0, loc)
		if got != want.Unix() {
			t.Errorf("= %d (%s), want %d (%s)", got, time.Unix(got, 0).In(loc), want.Unix(), want)
		}
	})
	t.Run("midnight is 12am", func(t *testing.T) {
		anchor := time.Date(2026, 8, 6, 23, 0, 0, 0, loc)
		got := parseLimitReset("resets 12am (America/New_York)", anchor)
		want := time.Date(2026, 8, 7, 0, 0, 0, 0, loc)
		if got != want.Unix() {
			t.Errorf("= %d (%s), want %d (%s)", got, time.Unix(got, 0).In(loc), want.Unix(), want)
		}
	})

	t.Run("midnight crossing inside the cap rolls forward a day", func(t *testing.T) {
		lateHit := time.Date(2026, 8, 6, 23, 0, 0, 0, loc)
		got := parseLimitReset("You've hit your session limit · resets 12:30am (America/New_York)", lateHit)
		want := time.Date(2026, 8, 7, 0, 30, 0, 0, loc)
		if got != want.Unix() {
			t.Errorf("= %d (%s), want %d (%s)", got, time.Unix(got, 0).In(loc), want.Unix(), want)
		}
	})

	// A same-day target beyond the cap is also already-due — the cap binds
	// on distance from the anchor, rolled or not.
	t.Run("same-day target beyond the cap is already due", func(t *testing.T) {
		got := parseLimitReset("resets 12pm (America/New_York)", hitAt)
		if got != hitAt.Unix() {
			t.Errorf("= %d (%s), want the hit time itself", got, time.Unix(got, 0).In(loc))
		}
	})
}

// A fresh banner's reset is at most one session window (~5h) ahead, so a
// target further out than limitResetMaxWait can only be a STALE banner read
// long after it was written — the transcript trigger's hit.At is the tick
// kraken noticed the stall, not the banner's write time, and both real
// reset-clause rows in kraken.db were recorded after the time their clause
// names (84 and 3 minutes late; a daemon restart or a sleeping Mac can
// stretch that lag to hours). A stale banner means the window already
// reopened, so the parse answers with the hit time itself: already due.
func TestLimitResetParseStaleBannerReadsAsAlreadyDue(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	// The measured shape: limit_hit:229 recorded 23:44, clause names
	// 10:20pm — 84 minutes in the past. The naive roll is tomorrow 22:20,
	// 22h36m out; the cap converts it to "due now".
	hitAt := time.Date(2026, 8, 6, 23, 44, 16, 0, loc)
	got := parseLimitReset("zeus hit a usage limit: You've hit your session limit · resets 10:20pm (America/New_York)", hitAt)
	if got != hitAt.Unix() {
		t.Errorf("stale banner = %d (%s), want the hit time itself %d — already due, not tomorrow",
			got, time.Unix(got, 0).In(loc), hitAt.Unix())
	}
	// And the boundary from the other side: a target inside the cap is
	// returned as computed.
	fresh := parseLimitReset("resets 4:50am (America/New_York)", resetHitAnchor(t))
	if want := time.Date(2026, 8, 6, 4, 50, 0, 0, loc).Unix(); fresh != want {
		t.Errorf("fresh banner = %d, want %d unchanged by the cap", fresh, want)
	}
}

func TestLimitResetParseNoClauseIsZero(t *testing.T) {
	hitAt := resetHitAnchor(t)
	for _, details := range []string{
		"",
		"You've reached your Fable 5 limit. Run /usage-credits to continue or switch models with /model.",
		"Usage credits are required",
		"rate_limit",
		"resets soon",
		"resets 99:99am", // hour out of range
		"resets 13pm (America/New_York)",
	} {
		if got := parseLimitReset(details, hitAt); got != 0 {
			t.Errorf("parseLimitReset(%q) = %d, want 0 — no parseable clause must mean no re-arm", details, got)
		}
	}
}

func TestLimitResetParseBadZoneFallsBackToLocal(t *testing.T) {
	hitAt := resetHitAnchor(t)
	got := parseLimitReset("resets 4:50am (Not/AZone)", hitAt)
	if got == 0 {
		t.Fatal("a bad zone must not lose the time — fall back to local, never to zero")
	}
	// The stated time must land in SOME zone; assert only the invariant
	// that holds regardless of the machine's local zone: the result is the
	// hit itself (stale-capped) or within the cap ahead of it.
	delta := got - hitAt.Unix()
	if delta < 0 || delta > int64(limitResetMaxWait/time.Second) {
		t.Errorf("reset %d is %ds from the hit, want within [0, %v]", got, delta, limitResetMaxWait)
	}
}

func TestLimitResetDecideHoldsThenReleases(t *testing.T) {
	hitAt := resetHitAnchor(t)
	reset := hitAt.Add(3 * time.Hour)
	hit := LimitHit{
		Error:    "rate_limit",
		At:       hitAt.Unix(),
		Attempts: limitRescueMaxAttempts,
		Outcome:  "parked-until-reset",
		ResetAt:  reset.Unix(),
	}

	// Before the reset: held, silently.
	if action, _ := limitRescueDecide(hit, "auto", true, false, reset.Add(-time.Hour)); action != rescueSkip {
		t.Errorf("before reset: action = %q, want skip", action)
	}
	// Inside the settle margin: still held.
	if action, _ := limitRescueDecide(hit, "auto", true, false, reset.Add(limitResetSettle-time.Second)); action != rescueSkip {
		t.Errorf("inside settle: action = %q, want skip", action)
	}
	// Past reset + settle: the no-swap nudge.
	action, reason := limitRescueDecide(hit, "auto", true, false, reset.Add(limitResetSettle))
	if action != rescueNudge {
		t.Fatalf("past reset+settle: action = %q (%s), want nudge", action, reason)
	}
	// A dead cell still never gets nudged — gate 4 outranks the re-arm.
	if action, _ := limitRescueDecide(hit, "auto", false, false, reset.Add(time.Hour)); action != rescueSkip {
		t.Errorf("dead cell past reset: action = %q, want skip", action)
	}
	// Exhausted attempts with NO reset time stays the terminal defensive
	// floor it always was.
	hit.ResetAt = 0
	if action, _ := limitRescueDecide(hit, "auto", true, false, reset.Add(time.Hour)); action != rescueParkNoTarget {
		t.Errorf("no reset time: action = %q, want park-no-target", action)
	}
}

func TestLimitResetParkStaysUnresolved(t *testing.T) {
	now := resetHitAnchor(t)
	hit := LimitHit{
		Error:    "rate_limit",
		At:       now.Unix(),
		Attempts: limitRescueMaxAttempts - 1,
		ResetAt:  now.Add(3 * time.Hour).Unix(),
	}
	got := limitRescueApply(hit, rescueParkNoTarget, "", "", now)
	if got.Attempts != limitRescueMaxAttempts {
		t.Fatalf("Attempts = %d, want %d", got.Attempts, limitRescueMaxAttempts)
	}
	if got.HandledAt != 0 {
		t.Errorf("HandledAt = %d, want 0 — a park with a known reset must stay unresolved so the re-arm can fire", got.HandledAt)
	}
	if got.Outcome != "parked-until-reset" {
		t.Errorf("Outcome = %q, want parked-until-reset", got.Outcome)
	}

	// Same park with no reset time resolves exactly as before.
	hit.ResetAt = 0
	got = limitRescueApply(hit, rescueParkNoTarget, "", "", now)
	if got.HandledAt == 0 || got.Outcome != "parked-no-target" {
		t.Errorf("no-reset park = (HandledAt %d, Outcome %q), want resolved parked-no-target", got.HandledAt, got.Outcome)
	}
}

// The reset re-arm arrives at its nudge with Attempts already spent by the
// parks, so the no-agents cap is path-relative: at the old absolute cap a
// single empty discovery snapshot — "not exotic" per the code's own comment
// — would have spent the re-arm's only shot and closed the hit forever.
func TestLimitResetNudgeGetsItsOwnFailureAllowance(t *testing.T) {
	now := resetHitAnchor(t)
	hit := LimitHit{
		Error:    "rate_limit",
		At:       now.Unix(),
		Attempts: limitRescueMaxAttempts,
		Outcome:  "parked-until-reset",
		ResetAt:  now.Add(-time.Hour).Unix(),
	}
	// Two more failed rounds stay open (attempts 4 and 5); the sixth parks.
	for i := 0; i < 2; i++ {
		hit = limitRescueApply(hit, rescueNudge, "no-agents", "", now)
		if hit.HandledAt != 0 {
			t.Fatalf("failed reset nudge %d resolved the hit: %+v", i+1, hit)
		}
	}
	hit = limitRescueApply(hit, rescueNudge, "no-agents", "", now)
	if hit.HandledAt == 0 || hit.Outcome != "parked-no-agents" {
		t.Fatalf("after %d failed rounds = (HandledAt %d, Outcome %q), want parked-no-agents", hit.Attempts, hit.HandledAt, hit.Outcome)
	}

	// The swap path keeps its absolute cap: swapped hits arrive with fewer
	// attempts spent and must not inherit the doubled allowance.
	swapped := LimitHit{Error: "rate_limit", At: now.Unix(), Attempts: limitRescueMaxAttempts - 1, SwappedAt: now.Unix()}
	swapped = limitRescueApply(swapped, rescueNudge, "no-agents", "", now)
	if swapped.HandledAt == 0 || swapped.Outcome != "parked-no-agents" {
		t.Fatalf("swap-path nudge at the cap = (HandledAt %d, Outcome %q), want parked-no-agents", swapped.HandledAt, swapped.Outcome)
	}
}

// The fold adopts a delivery's details when they carry a reset clause the
// stored ones lack: every hook-arm details string measured in kraken.db
// carries no clause (empty, or a raw 429 body), the transcript trigger
// arrives a tick later with the banner verbatim, and keeping the
// first-arrival details would suppress the reset re-arm for every
// hook-first hit.
func TestLimitResetFoldAdoptsResetBearingDetails(t *testing.T) {
	now := resetHitAnchor(t)
	hookFirst := LimitHit{
		Error:      "rate_limit",
		Details:    `429 {"type":"error","error":{"type":"rate_limit_error"}}`,
		At:         now.Unix(),
		SessionID:  "sess-hook",
		Deliveries: 1,
	}
	banner := "zeus hit a usage limit: You've hit your session limit · resets 4:50am (America/New_York)"

	got := mergeLimitHitDelivery(hookFirst, "rate_limit", banner, "sess-zeus", "", now.Add(10*time.Second))
	if got.Deliveries != 2 || got.At != hookFirst.At {
		t.Fatalf("= %+v, want a fold onto the stored hit", got)
	}
	if got.Details != banner {
		t.Errorf("Details = %q, want the banner adopted — the stored details carry no reset clause", got.Details)
	}

	// The reverse never happens: details that already carry a clause are
	// kept over a clause-less delivery.
	kept := mergeLimitHitDelivery(got, "rate_limit", "429 raw body", "sess-late", "", now.Add(20*time.Second))
	if kept.Details != banner {
		t.Errorf("Details = %q, want the banner kept over a clause-less delivery", kept.Details)
	}
}

// The end-to-end shape the re-arm exists for: a standing stall re-delivered
// every ten minutes must accumulate parks on ONE record until the reset
// releases the nudge — under the old windowed fold each re-delivery minted a
// fresh record and the counter oscillated 0↔1 forever.
func TestLimitResetSurvivesTranscriptRedeliveries(t *testing.T) {
	st := useTestStore(t)
	j := edgeStoreJob(t, st, "reset-redeliver")
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 8, 6, 1, 6, 45, 0, loc)
	banner := "zeus hit a usage limit: You've hit your session limit · resets 4:50am (America/New_York)"

	hit, err := RecordLimitHit(j, "rate_limit", banner, "sess-z", "", t0)
	if err != nil {
		t.Fatal(err)
	}
	// Three park rounds at the rescue's own cadence, a transcript
	// re-delivery between each — the exact interleaving the daemon's tick
	// order produces.
	for i := 0; i < 3; i++ {
		now := t0.Add(time.Duration(i) * (limitRescueRetryInterval + time.Second))
		if i > 0 {
			if hit, err = RecordLimitHit(j, "rate_limit", banner, "sess-z", "", now); err != nil {
				t.Fatal(err)
			}
		}
		if hit.ResetAt == 0 {
			hit.ResetAt = parseLimitReset(hit.Details, time.Unix(hit.At, 0))
		}
		action, _ := limitRescueDecide(hit, "auto", true, false, now)
		if action != rescueParkNoTarget {
			t.Fatalf("round %d: action = %q, want park-no-target", i+1, action)
		}
		hit = limitRescueApply(hit, action, "", "", now)
		if err := saveLimitRescueState(j, hit); err != nil {
			t.Fatal(err)
		}
	}
	if hit.Attempts != limitRescueMaxAttempts || hit.HandledAt != 0 || hit.Outcome != "parked-until-reset" {
		t.Fatalf("after three interleaved rounds = %+v, want an unresolved parked-until-reset at the cap", hit)
	}
	// The reset passes; the next decide releases the no-swap nudge.
	after := time.Unix(hit.ResetAt, 0).Add(limitResetSettle)
	if action, reason := limitRescueDecide(hit, "auto", true, false, after); action != rescueNudge {
		t.Fatalf("past reset: action = %q (%s), want nudge", action, reason)
	}
}

func TestLimitResetMergeCarriesResetAt(t *testing.T) {
	now := resetHitAnchor(t)
	cur := LimitHit{Error: "rate_limit", At: now.Unix(), Deliveries: 3}
	hit := cur
	hit.Attempts = 2
	hit.ResetAt = now.Add(2 * time.Hour).Unix()
	merged, ok := mergeLimitRescueState(cur, hit)
	if !ok {
		t.Fatal("same-hit merge refused")
	}
	if merged.ResetAt != hit.ResetAt {
		t.Errorf("merged ResetAt = %d, want %d — the rescue owns it and the merge must carry it", merged.ResetAt, hit.ResetAt)
	}
	if merged.Deliveries != 3 {
		t.Errorf("merged Deliveries = %d, want 3 preserved from the stored row", merged.Deliveries)
	}
}

// A pre-change stored record has no reset_at key and must decode to zero —
// exactly the old terminal behaviour, never a garbage target.
func TestLimitResetOldRecordsDecodeToZero(t *testing.T) {
	var hit LimitHit
	old := `{"error":"rate_limit","details":"x","at":1700000000,"session_id":"s","agent_id":"","deliveries":1,"handled_at":0,"outcome":"","attempts":2,"last_attempt_at":1700000100,"swapped_at":0,"swapped_to":""}`
	if err := json.Unmarshal([]byte(old), &hit); err != nil {
		t.Fatal(err)
	}
	if hit.ResetAt != 0 {
		t.Errorf("ResetAt = %d, want 0 on a pre-change record", hit.ResetAt)
	}
}
