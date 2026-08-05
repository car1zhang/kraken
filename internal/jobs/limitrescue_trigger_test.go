package jobs

import (
	"testing"
	"time"
)

// The health pass's churn bound, extracted from tick so it can be tested at
// all. It guards the transcript-derived trigger, and the rule it encodes was
// itself a blocking review finding: the first version only ever SET its key,
// which suppressed every later limit on the same banner for the daemon's
// whole lifetime. These tests exist so that regression cannot return quietly.

func TestLimitHitTriggerDue(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cases := []struct {
		name  string
		stamp int64
		want  bool
	}{
		{"never recorded", 0, true},
		{"recorded just now", now.Unix(), false},
		{"recorded inside the backoff", now.Add(-limitRescueRetryInterval + time.Second).Unix(), false},
		{"recorded exactly at the backoff", now.Add(-limitRescueRetryInterval).Unix(), false},
		{"recorded past the backoff", now.Add(-limitRescueRetryInterval - time.Second).Unix(), true},
		{"recorded long ago", now.Add(-72 * time.Hour).Unix(), true},
		// a clock stepped backwards leaves a stamp in the future; deferring
		// is the conservative direction, since the alternative is churning a
		// machine-wide account swap off a bad clock
		{"stamp in the future", now.Add(time.Hour).Unix(), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := limitHitTriggerDue(c.stamp, now); got != c.want {
				t.Errorf("limitHitTriggerDue(%d, %v) = %v, want %v", c.stamp, now, got, c.want)
			}
		})
	}
}

// The boundary is what the whole bound is for: at exactly limitRescueRetryInterval
// it is NOT due, one second later it is. Pinned on its own because an
// accidental >= would widen the window by a tick and nothing else would notice.
func TestLimitHitTriggerDueBoundaryIsStrict(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	at := now.Add(-limitRescueRetryInterval).Unix()
	if limitHitTriggerDue(at, now) {
		t.Fatal("exactly limitRescueRetryInterval old: due, want not due")
	}
	if !limitHitTriggerDue(at-1, now) {
		t.Fatal("one second past limitRescueRetryInterval: not due, want due")
	}
}

// Two jobs that reuse one slug must not share a suppression key. This is the
// same reasoning that keys the record itself on the id — archiving frees a
// slug, so a later generation would otherwise inherit an earlier one's
// suppression and never be rescued.
func TestLimitHitTriggerKeyIsScopedByJobID(t *testing.T) {
	const reason = "zeus hit a usage limit: API Error: You've hit your weekly limit"
	first := limitHitTriggerKey(12, reason)
	second := limitHitTriggerKey(40, reason)
	if first == second {
		t.Fatalf("two job ids share a key: %q", first)
	}
	if same := limitHitTriggerKey(12, reason); same != first {
		t.Fatalf("same id and reason produced %q then %q", first, same)
	}
}

// A different banner is a different key by design: a genuinely different
// limit must be able to record while an earlier one is still suppressed.
func TestLimitHitTriggerKeySeparatesReasons(t *testing.T) {
	weekly := limitHitTriggerKey(7, "zeus hit a usage limit: weekly")
	session := limitHitTriggerKey(7, "zeus hit a usage limit: session")
	if weekly == session {
		t.Fatalf("two reasons share a key: %q", weekly)
	}
}
