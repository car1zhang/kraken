package jobs

import (
	"testing"
	"time"
)

// TestRevivedSinceHitIsZeusOwnHitOnly pins the session gate the review
// round added to the contract: a hit stamped with a TEAMMATE's session id is
// never resolved by zeus' own later turns, while a hit stamped with zeus'
// session, or with none, is judged on zeus' transcript as before.
func TestRevivedSinceHitIsZeusOwnHitOnly(t *testing.T) {
	at := time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC)
	zeus := edgeRevPeerWithTranscript(t, "waiting", edgeRevAssistant(at.Add(2*time.Minute), "claude-pin-model"))
	cases := []struct {
		name string
		sid  string
		want bool
	}{
		{"a teammate's hit", "9f9f9f9f-2222-4333-8444-555555555555", false},
		{"zeus' own hit", zeus.SessionID, true},
		{"a hit with no session id", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hit := LimitHit{Error: "rate_limit", At: at.Unix(), SessionID: c.sid}
			if got := revivedSinceHit(hit, "claude", zeus); got != c.want {
				t.Errorf("revivedSinceHit(session %q) = %v, want %v", c.sid, got, c.want)
			}
		})
	}
	t.Run("a teammate's hit on a running codex zeus", func(t *testing.T) {
		codex := *zeus
		codex.Agent = "codex"
		codex.Status = "running"
		hit := LimitHit{Error: "rate_limit", At: at.Unix(), SessionID: "9f9f9f9f-2222-4333-8444-555555555555"}
		if revivedSinceHit(hit, "codex", &codex) {
			t.Error("a teammate's hit was resolved on the codex pane's status")
		}
	})
}
