package jobs

import (
	"testing"
	"time"

	"kraken/internal/core"
)

func TestFableFallbackDecide(t *testing.T) {
	now := time.Unix(1_790_820_000, 0)
	const fableBanner = "zeus hit a usage limit: You've reached your Fable limit. Run /usage-credits to continue or switch models with /model."
	const generic429 = `429 {"type":"error","error":{"type":"rate_limit_error","message":"This request would exceed your account's rate limit. Please try again later."}}`
	const pid, sid = 4242, "sess-zeus"
	open := func(details string) LimitHit {
		return LimitHit{Error: "rate_limit", Details: details, At: now.Unix() - 60, SessionID: sid}
	}
	with := func(h LimitHit, f func(*LimitHit)) LimitHit { f(&h); return h }
	acct := func(session, weekly, fable float64) *core.AccountUsage {
		return &core.AccountUsage{
			Session: &core.UsageWindow{Percent: session},
			Weekly:  &core.UsageWindow{Percent: weekly},
			Models:  []core.ModelWindow{{Name: "Fable", Percent: fable}},
		}
	}
	healthy := acct(20, 60, 100)
	fable := core.ZeusModel
	switched := func(ago time.Duration, prompted bool) fallbackRecord {
		return fallbackRecord{At: now.Add(-ago).Unix(), PID: pid, Prompted: prompted}
	}
	cases := []struct {
		name  string
		hit   LimitHit
		model string
		acct  *core.AccountUsage
		last  fallbackRecord
		want  fallbackVerdict
	}{
		{"banner, headroom", open(fableBanner), fable, healthy, fallbackRecord{}, fallbackSwitch},
		{"versioned banner", open("You've reached your Fable 5 limit. Run /usage-credits"), fable, healthy, fallbackRecord{}, fallbackSwitch},
		{"banner, no snapshot row", open(fableBanner), fable, nil, fallbackRecord{}, fallbackSwitch},
		{"generic 429, snapshot says fable spent", open(generic429), fable, acct(0, 61, 100), fallbackRecord{}, fallbackSwitch},
		{"generic 429, fable not spent", open(generic429), fable, acct(0, 61, 82), fallbackRecord{}, fallbackNo},
		{"generic 429, no snapshot row", open(generic429), fable, nil, fallbackRecord{}, fallbackNo},
		{"generic 429, fable row already reset", open(generic429), fable, &core.AccountUsage{Models: []core.ModelWindow{{Name: "Fable", Percent: 100, ResetsAt: now.Add(-time.Hour).UnixMilli()}}}, fallbackRecord{}, fallbackNo},
		{"session spent too", open(fableBanner), fable, acct(100, 60, 100), fallbackRecord{}, fallbackNo},
		{"weekly spent too", open(fableBanner), fable, acct(20, 100, 100), fallbackRecord{}, fallbackNo},
		{"opus window spent too", open(fableBanner), fable, &core.AccountUsage{Models: []core.ModelWindow{{Name: "Fable", Percent: 100}, {Name: "Opus", Percent: 100}}}, fallbackRecord{}, fallbackNo},
		{"session banner", open("You've hit your session limit · resets 4:50am (America/New_York)"), fable, healthy, fallbackRecord{}, fallbackNo},
		{"opus banner", open("You've hit your opus limit"), fable, healthy, fallbackRecord{}, fallbackNo},
		{"agent not on fable", open(fableBanner), core.DefaultModel, healthy, fallbackRecord{}, fallbackNo},
		{"job model override, other fable id", open(fableBanner), "claude-fable-5", healthy, fallbackRecord{}, fallbackSwitch},
		{"job model override, fable alias", open(fableBanner), "fable", healthy, fallbackRecord{}, fallbackSwitch},
		{"missing session/weekly windows", open(fableBanner), fable, &core.AccountUsage{}, fallbackRecord{}, fallbackSwitch},
		{"hit from another session", with(open(fableBanner), func(h *LimitHit) { h.SessionID = "sess-thread" }), fable, healthy, fallbackRecord{}, fallbackNo},
		{"hit with no session id", with(open(fableBanner), func(h *LimitHit) { h.SessionID = "" }), fable, healthy, fallbackRecord{}, fallbackSwitch},
		{"same process, late re-delivery", open(fableBanner), fable, healthy, switched(30*time.Second, true), fallbackAbsorb},
		{"same process, later limit is opus-side", open(generic429), fable, healthy, switched(3*time.Hour, true), fallbackNo},
		{"same process, prompt still owed", open(fableBanner), fable, healthy, switched(5*time.Minute, false), fallbackPrompt},
		{"same process, prompt owed too long", open(fableBanner), fable, healthy, switched(time.Hour, false), fallbackNo},
		{"same process, prompt owed on an hours-old hit", with(open(fableBanner), func(h *LimitHit) { h.At = now.Add(-5 * time.Hour).Unix() }), fable, healthy, switched(3*time.Minute, false), fallbackPrompt},
		{"same process, follow-on opus limit inside absorb window", open("You've hit your opus limit"), fable, healthy, switched(30*time.Second, true), fallbackNo},
		{"respawned process, back on fable", open(fableBanner), fable, healthy, fallbackRecord{At: now.Add(-5 * time.Minute).Unix(), PID: pid + 1, Prompted: true}, fallbackSwitch},
		{"no hit", LimitHit{}, fable, healthy, fallbackRecord{}, fallbackNo},
		{"resolved", with(open(fableBanner), func(h *LimitHit) { h.HandledAt = now.Unix() }), fable, healthy, fallbackRecord{}, fallbackNo},
		{"rebind landed", with(open(fableBanner), func(h *LimitHit) { h.SwappedAt = now.Unix() }), fable, healthy, fallbackRecord{}, fallbackNo},
		{"not a rate limit", with(open(fableBanner), func(h *LimitHit) { h.Error = "authentication_failed" }), fable, healthy, fallbackRecord{}, fallbackNo},
	}
	for _, c := range cases {
		if got := fableFallbackDecide(c.hit, c.model, c.acct, c.last, pid, sid, now); got != c.want {
			t.Errorf("%s: fableFallbackDecide = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestModelPercentTakesHighestLiveRow(t *testing.T) {
	now := time.Unix(1_790_820_000, 0)
	a := &core.AccountUsage{Models: []core.ModelWindow{
		{Name: "fable", Percent: 40},
		{Name: "Opus", Percent: 100},
		{Name: "Fable", Percent: 90},
		{Name: "Fable", Percent: 100, ResetsAt: now.Add(-time.Minute).UnixMilli()},
	}}
	if got := modelPercent(a, "Fable", now); got != 90 {
		t.Fatalf("modelPercent(Fable) = %v, want 90", got)
	}
	if got := modelPercent(&core.AccountUsage{}, "Fable", now); got != 0 {
		t.Fatalf("modelPercent with no rows = %v, want 0", got)
	}
}

func TestPickFallbackAccountPrefersFreshNewestRow(t *testing.T) {
	snap := core.UsageSnapshot{Accounts: []core.AccountUsage{
		{Slug: "main", Email: "me@x.com", Stale: true, FetchedAt: 300},
		{Slug: "errored", Email: "me@x.com", Error: "reauth", FetchedAt: 900},
		{Slug: "mirror-old", Email: "ME@x.com", FetchedAt: 100},
		{Slug: "mirror-new", Email: "me@x.com", FetchedAt: 200},
		{Slug: "other", Email: "you@x.com", FetchedAt: 999},
	}}
	if got := pickFallbackAccount(snap, "me@x.com"); got == nil || got.Slug != "mirror-new" {
		t.Fatalf("pickFallbackAccount = %+v, want mirror-new", got)
	}
	if got := pickFallbackAccount(snap, ""); got != nil {
		t.Fatalf("empty email picked %+v", got)
	}
	if got := pickFallbackAccount(snap, "nobody@x.com"); got != nil {
		t.Fatalf("unknown email picked %+v", got)
	}
}

func TestFableFallbackKeyMirrorsHitKey(t *testing.T) {
	if got := fableFallbackKey(limitHitKey(508)); got != "fable_fallback:508" {
		t.Errorf("cell key = %q", got)
	}
	if got := fableFallbackKey(standingHitKey("kronos")); got != "fable_fallback:kronos" {
		t.Errorf("standing key = %q", got)
	}
}

func TestStandingAgentsCarryTheirModel(t *testing.T) {
	for _, a := range standingAgents() {
		if a.Model == "" {
			t.Errorf("%s: standing agent has no Model — the Fable fallback can never fire for it", a.ID)
		}
	}
}
