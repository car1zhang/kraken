package jobs

import (
	"testing"
	"time"
)

// Regression pin for 2026-10-08: on four Fable-exhausted cells (job 554 among
// them) the StopFailure hook's raw 429 body recorded first, the transcript
// arm's Fable banner folded onto it a tick later without replacing it (the
// banner has no reset clause), the account's usage-snapshot row was itself an
// errored 429, and the fallback — with neither banner nor snapshot — handed
// every one to rotation, which had nowhere to go.
func TestFableBannerFoldsOverRawHookBody(t *testing.T) {
	now := time.Unix(1791500146, 0)
	hookFirst := LimitHit{
		Error:      "rate_limit",
		Details:    `429 {"type":"error","error":{"type":"rate_limit_error","message":"This request would exceed your account's rate limit. Please try again later."},"request_id":"req_011CfqbTNTKdCkV7rWR12h4M"}`,
		At:         now.Unix(),
		SessionID:  "sess-zeus",
		Deliveries: 1,
	}
	banner := "zeus hit a usage limit: You've reached your Fable limit. Run /usage-credits to continue or switch models with /model."

	got := mergeLimitHitDelivery(hookFirst, "rate_limit", banner, "sess-zeus", "", now.Add(6*time.Second))
	if got.Deliveries != 2 || got.At != hookFirst.At {
		t.Fatalf("= %+v, want a fold onto the stored hit", got)
	}
	if got.Details != banner {
		t.Fatalf("Details = %q, want the Fable banner adopted over the raw 429", got.Details)
	}
	// No usable snapshot row (errored rows are skipped → nil): the banner
	// alone must now produce the switch.
	if v := fableFallbackDecide(got, "claude-fable-5-1", nil, fallbackRecord{}, 4242, "sess-zeus", now.Add(10*time.Second)); v != fallbackSwitch {
		t.Fatalf("verdict = %q, want %q", v, fallbackSwitch)
	}

	// A later raw 429 never displaces the banner.
	kept := mergeLimitHitDelivery(got, "rate_limit", hookFirst.Details, "sess-zeus", "", now.Add(20*time.Second))
	if kept.Details != banner {
		t.Errorf("Details = %q, want the banner kept over a raw 429", kept.Details)
	}
	// A banner already naming a window is not replaced by another clause-less one.
	session := "zeus hit a usage limit: You've hit your session limit"
	if k := mergeLimitHitDelivery(got, "rate_limit", session, "sess-zeus", "", now.Add(30*time.Second)); k.Details != banner {
		t.Errorf("Details = %q, want the first window-naming banner kept", k.Details)
	}
}

// A teammate's hook can record first; the lead's banner then has to bring the
// lead's session along, or the fallback's session gate refuses the switch.
func TestFableBannerFoldAdoptsLeadSession(t *testing.T) {
	now := time.Unix(1791500146, 0)
	hookFirst := LimitHit{Error: "rate_limit", Details: "429 raw body", At: now.Unix(), SessionID: "sess-teammate", AgentID: "agent-x", Deliveries: 1}
	banner := "zeus hit a usage limit: You've reached your Fable limit. Run /usage-credits to continue or switch models with /model."

	got := mergeLimitHitDelivery(hookFirst, "rate_limit", banner, "sess-zeus", "", now.Add(6*time.Second))
	if got.SessionID != "sess-zeus" || got.AgentID != "" {
		t.Fatalf("session = %q agent = %q, want the banner delivery's", got.SessionID, got.AgentID)
	}
	if v := fableFallbackDecide(got, "claude-fable-5-1", nil, fallbackRecord{}, 4242, "sess-zeus", now.Add(10*time.Second)); v != fallbackSwitch {
		t.Fatalf("verdict = %q, want %q", v, fallbackSwitch)
	}
	// A raw 429 from another session never moves the session.
	if k := mergeLimitHitDelivery(got, "rate_limit", "429 raw body", "sess-other", "", now.Add(20*time.Second)); k.SessionID != "sess-zeus" {
		t.Errorf("session = %q, want sess-zeus kept", k.SessionID)
	}
}

// Window adoption never trades away a reset clause the stored details carry,
// since that clause is what re-arms a parked hit.
func TestWindowFoldKeepsStoredResetClause(t *testing.T) {
	now := resetHitAnchor(t)
	stored := LimitHit{Error: "rate_limit", Details: "usage limit reached · resets 4:50am (America/New_York)", At: now.Unix(), SessionID: "sess-zeus", Deliveries: 1}
	if parseLimitReset(stored.Details, now) == 0 {
		t.Fatalf("fixture details carry no parseable reset clause")
	}
	banner := "You've reached your Fable limit. Run /usage-credits to continue or switch models with /model."
	if got := mergeLimitHitDelivery(stored, "rate_limit", banner, "sess-zeus", "", now.Add(time.Minute)); got.Details != stored.Details {
		t.Errorf("Details = %q, want the reset-bearing details kept", got.Details)
	}
}

func TestNamesLimitWindow(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"You've reached your Fable limit. Run /usage-credits to continue", true},
		{"You've reached your Fable 5 limit.", true},
		{"You've reached your Fable 5.1 limit.", true},
		{"You've hit your session limit · resets 4:30pm (America/New_York)", true},
		{"You've hit your weekly limit", true},
		{"You've reached your Fable", false},
		{`429 {"type":"error","error":{"type":"rate_limit_error"}}`, false},
		{"", false},
	}
	for _, c := range cases {
		if got := namesLimitWindow(c.in); got != c.want {
			t.Errorf("namesLimitWindow(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
