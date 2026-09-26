package jobs

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"kraken/internal/core"
	"kraken/internal/store"
)

// The brief-drift pass: a standing agent (kronos, iris) is a claude that
// kraken resumes rather than restarts, and its brief is handed to it ONCE, at
// spawn, staged as a file beside its prompt. Ensure* respawns only a DEAD
// session, so a running agent keeps the brief it was spawned with for as long
// as its tmux session lives — on 2026-09-25 two brief fixes merged, were
// installed by `kraken update`, and reached neither agent for the rest of the
// day (both staged briefs dated five days earlier).
// A brief fix for a standing agent is otherwise inert on merge and on install
// until carl kills the session by hand.
//
// The signal is the staged file itself: it holds what the running agent got,
// the brief function holds what a fresh spawn would get from the installed
// binary, and the two disagreeing means the code moved under a live agent. The
// remedy is the standing rescue's own resume half — kill the session and let
// Ensure* respawn-resume it, which re-stages the brief and keeps the
// conversation (`-r` on the recorded session id) — so the only new machinery
// here is the comparison and the gates around when it may act.
//
// Two things are shared with the limit rescue — the identity gate
// (standingKillTarget: the agent's own --name or the recorded session id, so a
// pane that cannot be positively identified is never killed) and the
// kill-and-respawn seam (standingRespawn, whose own pane re-check refuses a
// session carl has opened a second pane in). Three gates are this pass' own:
//
//   - a peer that is mid-turn defers the round, with no stamp, rather than
//     losing the turn;
//   - an OPEN limit hit hands the pane to the rescue, whose two-phase
//     swap-then-resume must not be interleaved with a second kill;
//   - a kv backoff is stamped BEFORE the kill, so a respawn whose fresh brief
//     somehow still differs (a spawn that failed to stage, a Brief() that is
//     not deterministic) cannot loop the agent to death at the tick rate.
//
// The mirror daemon never reaches this pass: it sits in the standing block the
// tick runs only outside --mirror.

// standingBriefBackoff is the minimum interval between two brief-drift
// respawn ATTEMPTS of one agent. Six hours is long enough that a respawn which
// did not clear the drift costs four attempts a day rather than one per tick,
// and short enough that a brief fix landing in the morning is live by the
// afternoon.
const standingBriefBackoff = 6 * time.Hour

// standingBriefKey is the kv row holding the unix-millis of the agent's last
// brief-drift respawn attempt, as a decimal string. Its prefix is its own,
// disjoint from standingHitKey's `limit_hit:`, so the two passes' records can
// never read each other.
func standingBriefKey(agentID string) string { return "standing_brief_respawn:" + agentID }

// standingBriefDrift reports whether the agent's staged brief differs from the
// brief a spawn would stage now. A pure read with no side effects.
//
// Every failure to answer reads as NO drift, with the reason returned for the
// caller's log: an agent with no brief wired has nothing to compare; a
// missing staged file means the next spawn stages one anyway (the file is
// written by the spawn command, so absence means "never spawned by this
// binary", not "stale"); and an unreadable file must never be treated as
// drift, because the only action drift buys is a kill, and a kill on a failed
// read is the exact shape every other pass in this package refuses.
//
// The comparison is byte-exact. A brief is rendered by a pure function of the
// binary's constants, so any difference at all — a trailing newline included —
// is a binary that renders something other than what the agent holds.
func standingBriefDrift(a standingAgent) (drifted bool, reason string) {
	if a.BriefPath == nil || a.Brief == nil {
		return false, a.ID + ": no brief wired — nothing to compare"
	}
	staged, err := os.ReadFile(a.BriefPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, fmt.Sprintf("%s: no staged brief at %s — the next spawn stages one", a.ID, a.BriefPath())
		}
		return false, fmt.Sprintf("%s: staged brief unreadable — never read as drift: %v", a.ID, err)
	}
	current := []byte(a.Brief())
	if bytes.Equal(staged, current) {
		return false, ""
	}
	return true, fmt.Sprintf("%s: staged brief differs from the current render (%d vs %d bytes)", a.ID, len(staged), len(current))
}

// standingBriefPass is the brief-drift pass for one standing agent, run each
// tick after the health pass and the limit rescue. Seven gates, each a return
// with nothing written and nothing killed; the stamp is the first side effect
// and the kill the second, in that order, so a crash between the two costs a
// backoff window rather than a loop.
func (d *daemonState) standingBriefPass(a standingAgent, st *store.Store, all []*core.Peer, live map[string]bool, now time.Time) {
	if st == nil {
		return
	}
	if !live[a.Session] {
		// Nothing running to hold a stale brief: Ensure* stages a fresh one
		// on the next spawn, and killing nothing is the whole of this pass.
		return
	}
	if hit := readLimitHitByKey(standingHitKey(a.ID)); hit.At != 0 && hit.HandledAt == 0 {
		// The rescue owns the pane while a hit is open: its rebind and its
		// resume land on different ticks, and a second kill in between would
		// respawn onto a swap the rescue has not finished recording.
		return
	}
	key := standingBriefKey(a.ID)
	if raw, err := st.GetKV(key); err == nil && raw != "" {
		if last, perr := strconv.ParseInt(raw, 10, 64); perr == nil && now.Sub(time.UnixMilli(last)) < standingBriefBackoff {
			return
		}
		// An unparseable stamp reads as no prior attempt: the row is this
		// pass' own, and a value it did not write is not evidence of one.
	}
	drifted, reason := standingBriefDrift(a)
	if !drifted {
		return
	}
	recorded, _ := a.RecordedSession(st)
	peer, why := standingKillTarget(a, all, recorded)
	if peer == nil {
		// A deferral, not a resolution: no stamp, so the next tick re-checks
		// once a name has landed. Same reasoning as the rescue's identity gate.
		dlogEvent("info", "standing-brief", "%s: brief drifted (%s) but the pane is not identifiable this round — %s", a.ID, reason, why)
		return
	}
	if peer.Status == "running" {
		// Mid-turn. The kill would take the turn with it; wait for the pane
		// to go idle, which on a bump-driven agent is seconds away.
		return
	}
	// Stamped BEFORE the kill. A stamp that cannot be written means the
	// backoff cannot hold, and a kill with no backoff behind it is the one
	// shape that can loop; so the failure refuses the round.
	if err := st.SetKV(key, strconv.FormatInt(now.UnixMilli(), 10)); err != nil {
		dlogEvent("error", "standing-brief", "%s: brief drifted (%s) but the backoff stamp failed — not killing: %v", a.ID, reason, err)
		return
	}
	if err := standingRespawn(a, st, live); err != nil {
		dlogEvent("error", "standing-brief-respawn", "%s: brief drifted (%s); respawn on pane %s failed: %v", a.ID, reason, peer.Pane, err)
		return
	}
	dlogEvent("info", "standing-brief-respawn", "%s: brief drifted (%s); killed pane %s and respawned on the fresh brief (session %s)", a.ID, reason, peer.Pane, recorded)
}
