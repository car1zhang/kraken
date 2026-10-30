package jobs

// weeklyreset_contract_test.go — athena-logic's BLIND contract tests for
// slice A2 of dream-2026-10-30: a weekly banner's reset is never read as
// stale (parseLimitReset, weeklyBannerRe). Authored from
// artifacts/50-contract.md alone.
//
// LOGIC dimension. Every package-level identifier is prefixed
// `contractWeekly`.

import (
	"fmt"
	"testing"
	"time"
)

func contractWeeklyLoc(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

// A weekly banner 17 hours out is returned as that target, never as hitAt.
func TestContractWeeklyResetFarTargetKept(t *testing.T) {
	loc := contractWeeklyLoc(t)
	hitAt := time.Date(2026, 5, 12, 12, 0, 0, 0, loc)
	want := time.Date(2026, 5, 13, 5, 0, 0, 0, loc).Unix()
	for _, details := range []string{
		"You've hit your weekly limit · resets 5am (America/New_York)",
		"zeus hit a usage limit: You've hit your Weekly Limit · resets 5am (America/New_York)",
		"YOU'VE HIT YOUR WEEKLY LIMIT · resets 5:00am (America/New_York)",
	} {
		if got := parseLimitReset(details, hitAt); got != want {
			t.Errorf("parseLimitReset(%q) = %d (%s), want %d (%s)", details, got, time.Unix(got, 0).In(loc), want, time.Unix(want, 0).In(loc))
		}
	}
}

// A weekly banner inside the cap is the first occurrence strictly after hitAt.
func TestContractWeeklyResetNearTarget(t *testing.T) {
	loc := contractWeeklyLoc(t)
	hitAt := time.Date(2026, 5, 12, 12, 0, 0, 0, loc)
	got := parseLimitReset("You've hit your weekly limit · resets 3:30pm (America/New_York)", hitAt)
	if want := time.Date(2026, 5, 12, 15, 30, 0, 0, loc).Unix(); got != want {
		t.Errorf("= %d (%s), want %d", got, time.Unix(got, 0).In(loc), want)
	}
}

// The stated zone is honoured for the weekly arm.
func TestContractWeeklyResetStatedZone(t *testing.T) {
	la, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	hitAt := time.Date(2026, 5, 12, 20, 0, 0, 0, la)
	got := parseLimitReset("You've hit your weekly limit · resets 7am (America/Los_Angeles)", hitAt)
	if want := time.Date(2026, 5, 13, 7, 0, 0, 0, la).Unix(); got != want {
		t.Errorf("= %d (%s), want %d", got, time.Unix(got, 0).In(la), want)
	}
}

// A banner that does not name the weekly window keeps the 6h staleness cap.
func TestContractWeeklyNonWeeklyKeepsCap(t *testing.T) {
	loc := contractWeeklyLoc(t)
	hitAt := time.Date(2026, 5, 12, 12, 0, 0, 0, loc)
	for _, details := range []string{
		"You've hit your session limit · resets 5am (America/New_York)",
		"resets 5am (America/New_York)",
	} {
		if got := parseLimitReset(details, hitAt); got != hitAt.Unix() {
			t.Errorf("parseLimitReset(%q) = %d, want hitAt %d (stale cap)", details, got, hitAt.Unix())
		}
	}
	// and inside the cap it is returned as computed
	if got, want := parseLimitReset("You've hit your session limit · resets 2pm (America/New_York)", hitAt), time.Date(2026, 5, 12, 14, 0, 0, 0, loc).Unix(); got != want {
		t.Errorf("session banner inside cap = %d, want %d", got, want)
	}
}

// The resets_at=<unix> form is unchanged: returned as-is.
func TestContractWeeklyResetsAtUnchanged(t *testing.T) {
	hitAt := time.Unix(1_790_000_000, 0)
	far := hitAt.Add(40 * time.Hour).Unix()
	got := parseLimitReset(fmt.Sprintf("codex weekly limit reached resets_at=%d", far), hitAt)
	if got != far {
		t.Errorf("resets_at form = %d, want %d", got, far)
	}
}

func TestContractWeeklyBannerRe(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"weekly limit", true},
		{"You've hit your weekly limit · resets 6am (America/New_York)", true},
		{"WEEKLY LIMIT", true},
		{"zeus: Weekly Limit reached", true},
		{"You've hit your session limit · resets 6am (America/New_York)", false},
		{"weekly", false},
		{"limit", false},
		{"", false},
	}
	for _, c := range cases {
		if got := weeklyBannerRe.MatchString(c.in); got != c.want {
			t.Errorf("weeklyBannerRe.MatchString(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
