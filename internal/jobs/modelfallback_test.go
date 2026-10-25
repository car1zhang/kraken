package jobs

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kraken/internal/core"
	"kraken/internal/store"
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
	withRec := func(r fallbackRecord, f func(*fallbackRecord)) fallbackRecord { f(&r); return r }
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
		{"same process, prompt sent but no turn seen yet", open(fableBanner), fable, healthy, withRec(switched(5*time.Minute, false), func(r *fallbackRecord) { r.PromptedAt = now.Add(-time.Minute).Unix() }), fallbackPrompt},
		{"same process, re-delivery soon after a late confirmation", open(fableBanner), fable, healthy, withRec(switched(10*time.Minute, true), func(r *fallbackRecord) { r.ConfirmedAt = now.Add(-90 * time.Second).Unix() }), fallbackAbsorb},
		{"same process, prompt owed too long", open(fableBanner), fable, healthy, switched(time.Hour, false), fallbackExpire},
		{"same process, re-delivery after the expiry", open(fableBanner), fable, healthy, withRec(switched(2*time.Hour, false), func(r *fallbackRecord) { r.ExpiredAt = now.Add(-90 * time.Minute).Unix() }), fallbackAbsorb},
		{"same process, re-delivery hours after the expiry", open(fableBanner), fable, healthy, withRec(switched(26*time.Hour, false), func(r *fallbackRecord) { r.ExpiredAt = now.Add(-25 * time.Hour).Unix() }), fallbackAbsorb},
		{"same process, follow-on opus limit after the expiry", open("You've hit your opus limit"), fable, healthy, withRec(switched(2*time.Hour, false), func(r *fallbackRecord) { r.ExpiredAt = now.Add(-90 * time.Minute).Unix() }), fallbackNo},
		{"respawned process, back on fable after an expiry", open(fableBanner), fable, healthy, fallbackRecord{At: now.Add(-2 * time.Hour).Unix(), PID: pid + 1, ExpiredAt: now.Add(-90 * time.Minute).Unix()}, fallbackSwitch},
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
	if got := fableFallbackKey(standingHitKey("iris")); got != "fable_fallback:iris" {
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

// The confirmation Claude Code 2.1.306 draws for `/model <id>`, as a pane
// renders it (the banner that triggered the switch sits above it).
const switchModelDialog = `  ⎿  You've reached your Fable limit. Run /usage-credits to continue or switch models with /model.
✻ Done in 1s
▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔
   Switch model?
   Your next response will be slower and use more tokens
   This conversation is cached for the current model. Switching to Opus 5.5 means the full history gets re-read on your next message.
   ❯ 1. Yes, switch to Opus 5.5
     2. No, go back
`

func TestModelSwitchKeysPicksYes(t *testing.T) {
	keys, ok := modelSwitchKeys(switchModelDialog)
	if !ok {
		t.Fatal("the switch-model dialog was not recognised")
	}
	if len(keys) != 1 || keys[0] != "Enter" {
		t.Errorf("cursor on yes: keys = %v, want [Enter]", keys)
	}
	// cursor drawn on "No, go back": step up onto yes first
	onNo := strings.Replace(switchModelDialog, "❯ 1. Yes", "  1. Yes", 1)
	onNo = strings.Replace(onNo, "  2. No", "❯ 2. No", 1)
	keys, ok = modelSwitchKeys(onNo)
	if !ok {
		t.Fatal("the dialog with the cursor on no was not recognised")
	}
	if len(keys) != 2 || keys[0] != "Up" || keys[1] != "Enter" {
		t.Errorf("cursor on no: keys = %v, want [Up Enter]", keys)
	}
}

func TestModelSwitchKeysIgnoresOtherPanes(t *testing.T) {
	for name, text := range map[string]string{
		"banner only":          "  ⎿  You've reached your Fable limit. Run /usage-credits to continue or switch models with /model.\n✻ Done in 1s\n",
		"trust dialog":         "Is this a project you created or one you trust?\n ❯ 1. Yes, I trust this folder\n   2. No, exit\n",
		"agent question":       "Switch model? I can keep going on the current one or try another.\n ❯ 1. Keep the current model\n   2. Try another\n",
		"header, no options":   "   Switch model?\n   Your next response will be slower and use more tokens\n",
		"operator's own tier":  strings.Replace(switchModelDialog, "Yes, switch to Opus 5.5", "Yes, switch to Sonnet 5", 1),
		"agent question, yes":  "Switch model? I could move to Haiku for the rest of this.\n ❯ 1. Yes, switch to Haiku\n   2. No, stay\n",
		"header scrolled away": "   This conversation is cached for the current model.\n   ❯ 1. Yes, switch to Opus 5.5\n     2. No, go back\n",
	} {
		if keys, ok := modelSwitchKeys(text); ok {
			t.Errorf("%s: recognised as the switch-model dialog, keys %v", name, keys)
		}
	}
}

// A switch that expired while its dialog was still up is answered and
// re-armed for one more prompt window, with the hit left unresolved so the
// next tick owes the continue — and a second expiry on the same process is
// not re-armed again, however long the pane keeps drawing the dialog.
func TestFableFallbackRearmsAnExpiredSwitchOnce(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "kraken.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	UseStore(st)
	t.Cleanup(func() { UseStore(nil); st.Close() })

	answered := 0
	orig := answerSwitchPrompt
	answerSwitchPrompt = func(*core.Peer) bool { answered++; return true }
	t.Cleanup(func() { answerSwitchPrompt = orig })

	const banner = "zeus hit a usage limit: You've reached your Fable limit. Run /usage-credits to continue or switch models with /model."
	now := time.Unix(1_790_820_000, 0)
	hitKey := limitHitKey(508)
	p := &core.Peer{PID: 4242, Pane: "%9", SessionID: "sess-1", Agent: "claude", Messageable: true}
	find := func() *core.Peer { return p }
	noEmail := func() string { return "" }
	d := newDaemon()
	d.Poked["fable-retry:"+hitKey] = now.Add(-30 * time.Second).Unix() // a refused continue inside the retry gap

	// An expired switch; the transcript arm re-delivers the banner as a fresh hit.
	recordLastFallback(hitKey, fallbackRecord{At: now.Add(-2 * time.Hour).Unix(), PID: 4242, ExpiredAt: now.Add(-90 * time.Minute).Unix()})
	hit := LimitHit{Error: "rate_limit", Details: banner, At: now.Unix(), SessionID: "sess-1"}
	if !d.tryFableFallback(hitKey, "job", core.ZeusModel, noEmail, hit, find, now) {
		t.Fatal("re-delivery on an expired switch with the dialog up was not handled")
	}
	if answered != 1 {
		t.Fatalf("dialog answered %d times, want 1", answered)
	}
	rec := readLastFallback(hitKey)
	if rec.At != now.Unix() || rec.PID != 4242 || !rec.Rearmed || rec.ExpiredAt != 0 || rec.PromptedAt != 0 {
		t.Errorf("record after the re-arm = %+v, want a fresh {At: now, PID: 4242, Rearmed: true}", rec)
	}
	if got := readLimitHitByKey(hitKey).Outcome; got != "" {
		t.Errorf("hit resolved as %q after the re-arm; it must stay open so the next tick owes the continue", got)
	}
	if _, gap := d.Poked["fable-retry:"+hitKey]; gap {
		t.Error("the retry gap set by the refused continue was not cleared")
	}
	// The next tick reads the re-armed record as a switch owing its continue.
	if v := fableFallbackDecide(hit, core.ZeusModel, nil, rec, 4242, "sess-1", now.Add(15*time.Second)); v != fallbackPrompt {
		t.Errorf("verdict on the tick after the re-arm = %q, want %q", v, fallbackPrompt)
	}

	// The re-armed window runs out too, with the dialog still drawn: no second re-arm.
	rec.ExpiredAt = now.Add(31 * time.Minute).Unix()
	recordLastFallback(hitKey, rec)
	later := now.Add(45 * time.Minute)
	hit2 := LimitHit{Error: "rate_limit", Details: banner, At: later.Unix(), SessionID: "sess-1"}
	if !d.tryFableFallback(hitKey, "job", core.ZeusModel, noEmail, hit2, find, later) {
		t.Fatal("re-delivery after the second expiry was not handled")
	}
	if answered != 1 {
		t.Errorf("dialog answered %d times in total, want 1: a second expiry must not re-arm", answered)
	}
	if got := readLimitHitByKey(hitKey).Outcome; got != "model-fallback-expired" {
		t.Errorf("second re-delivery resolved as %q, want model-fallback-expired", got)
	}
}
