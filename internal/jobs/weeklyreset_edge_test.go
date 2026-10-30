package jobs

// weeklyreset_edge_test.go — athena-edge's BLIND tests for slice A2 of
// dream-2026-10-30 (a weekly banner's reset is never read as stale):
// boundaries of weeklyBannerRe and parseLimitReset's weekly arm. Authored
// from 50-contract.md alone. Every package-level identifier is prefixed
// edgeWR.

import (
	"testing"
	"time"
)

func edgeWRLoc(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestEdgeWeeklyBannerRe(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"You've hit your weekly limit · resets 6am (America/New_York)", true},
		{"You've hit your Weekly Limit", true},
		{"WEEKLY LIMIT REACHED", true},
		{"You've hit your Opus weekly limit · resets 2pm (America/New_York)", true},
		{"prefix text weekly limit", true},
		{"biweekly limit", true}, // "anywhere in the details"
		{"", false},
		{"You've hit your session limit · resets 4:50am (America/New_York)", false},
		{"weekly usage · resets 6am (America/New_York)", false},
		{"weekly-limit", false},
		{"weeklylimit", false},
		{"weekly  limit", false},
		{"week limit", false},
		{"limit weekly", false},
		{"rate_limit", false},
		{"You've reached your Fable 5 limit.", false},
	}
	for _, c := range cases {
		if got := weeklyBannerRe.MatchString(c.in); got != c.want {
			t.Errorf("weeklyBannerRe.MatchString(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// The weekly arm has NO staleness cap: targets far beyond limitResetMaxWait
// come back as computed.
func TestEdgeWeeklyResetNoCap(t *testing.T) {
	loc := edgeWRLoc(t)
	hitAt := time.Date(2026, 5, 12, 13, 0, 0, 0, loc)
	cases := []struct {
		name    string
		details string
		want    time.Time
	}{
		{"17h out", "zeus hit a usage limit: You've hit your weekly limit · resets 6am (America/New_York)", time.Date(2026, 5, 13, 6, 0, 0, 0, loc)},
		{"just under 24h out", "You've hit your weekly limit · resets 12:59pm (America/New_York)", time.Date(2026, 5, 13, 12, 59, 0, 0, loc)},
		{"6h01m out", "You've hit your weekly limit · resets 7:01pm (America/New_York)", time.Date(2026, 5, 12, 19, 1, 0, 0, loc)},
		{"uppercase phrase", "YOU'VE HIT YOUR WEEKLY LIMIT · resets 6am (America/New_York)", time.Date(2026, 5, 13, 6, 0, 0, 0, loc)},
		{"Opus weekly", "You've hit your Opus weekly limit · resets 11pm (America/New_York)", time.Date(2026, 5, 12, 23, 0, 0, 0, loc)},
		{"within the old cap still exact", "You've hit your weekly limit · resets 3pm (America/New_York)", time.Date(2026, 5, 12, 15, 0, 0, 0, loc)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseLimitReset(c.details, hitAt)
			if got != c.want.Unix() {
				t.Errorf("parseLimitReset(%q) = %d (%s), want %d (%s)", c.details, got, time.Unix(got, 0).In(loc), c.want.Unix(), c.want)
			}
		})
	}
}

// "strictly after hitAt": a banner naming the hit's own wall time rolls a
// full day forward on the weekly arm (and the non-weekly arm caps that).
func TestEdgeWeeklyResetStrictlyAfter(t *testing.T) {
	loc := edgeWRLoc(t)
	hitAt := time.Date(2026, 5, 12, 6, 0, 0, 0, loc)
	got := parseLimitReset("You've hit your weekly limit · resets 6am (America/New_York)", hitAt)
	want := time.Date(2026, 5, 13, 6, 0, 0, 0, loc)
	if got != want.Unix() {
		t.Errorf("weekly at the named wall time = %d (%s), want %d (%s)", got, time.Unix(got, 0).In(loc), want.Unix(), want)
	}
	got = parseLimitReset("You've hit your session limit · resets 6am (America/New_York)", hitAt)
	if got != hitAt.Unix() {
		t.Errorf("session at the named wall time = %d, want the hit itself %d (rolled 24h, capped)", got, hitAt.Unix())
	}
}

// A banner that does not name the weekly window keeps the 6h cap.
func TestEdgeWeeklyResetNonWeeklyKeepsCap(t *testing.T) {
	loc := edgeWRLoc(t)
	hitAt := time.Date(2026, 5, 12, 13, 0, 0, 0, loc)
	for _, details := range []string{
		"You've hit your session limit · resets 6am (America/New_York)",
		"weekly usage reached · resets 6am (America/New_York)",
		"You've hit your weekly-limit · resets 6am (America/New_York)",
		"resets 6am (America/New_York)",
	} {
		if got := parseLimitReset(details, hitAt); got != hitAt.Unix() {
			t.Errorf("parseLimitReset(%q) = %d (%s), want the hit itself %d — a non-weekly banner 17h out is stale",
				details, got, time.Unix(got, 0).In(loc), hitAt.Unix())
		}
	}
}

// The weekly phrase alone is not a reset: no clause still means 0.
func TestEdgeWeeklyResetNoClause(t *testing.T) {
	hitAt := time.Date(2026, 5, 12, 13, 0, 0, 0, edgeWRLoc(t))
	for _, details := range []string{
		"You've hit your weekly limit",
		"You've hit your weekly limit · resets soon",
		"You've hit your weekly limit · resets 13pm (America/New_York)",
	} {
		if got := parseLimitReset(details, hitAt); got != 0 {
			t.Errorf("parseLimitReset(%q) = %d, want 0", details, got)
		}
	}
}

// The resets_at=<unix> form is returned as-is, weekly phrase or not.
func TestEdgeWeeklyResetResetsAtUnchanged(t *testing.T) {
	hitAt := time.Unix(1_800_000_000, 0)
	far := int64(1_800_000_000 + 5*24*3600)
	if got := parseLimitReset("weekly limit resets_at=1800432000", hitAt); got != far {
		t.Errorf("weekly resets_at = %d, want %d", got, far)
	}
	if got := parseLimitReset("codex secondary resets_at=1800432000", hitAt); got != far {
		t.Errorf("resets_at = %d, want %d", got, far)
	}
}
