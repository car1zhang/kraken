package jobs

import (
	"strconv"
	"strings"
	"time"

	"kraken/internal/core"
	"kraken/internal/store"
)

// standingmodel.go — the standing agents' MODEL-DRIFT pass: the third thing
// the daemon does to kronos and iris unprompted, beside the health pass and
// the usage-limit rescue in standingrescue.go.
//
// Every claude kraken spawns runs on a model kraken names (`--model`, the
// policy CLAUDE.md pins), and a standing agent is the one process that
// outlives that decision by weeks: it is spawned once and resumed, never
// respawned on a schedule. So a model change that happens INSIDE the session
// — measured: a safeguards refusal that re-tiered kronos for the life of its
// session, six consecutive days on a different tier while core.KronosModel
// still named the pinned one, with a degenerate filler loop growing to two
// thirds of its output on the far side of it — is invisible to every spawn
// path, and the only thing that reasserts `--model` is a kill and a resume.
// That primitive already exists (standingKillAndRespawn, the limit rescue's
// resume half, which the seam `standingRespawn` fronts); this pass is the
// second trigger for it.
//
// The evidence is the transcript, on the file's standing rule: the newest
// assistant record of the RECORDED session id, never a discovered one
// (core.TranscriptModelID). Peer.Model is not used even though `k ls` renders
// the same drift from it, because for a flag-spawned process discovery takes
// the model off the ARGV — exactly the value the drift departs from.
//
// Three gates keep a respawn from becoming a loop, each measured against the
// shape that would trip it. An open usage-limit hit defers the pass entirely:
// that pane belongs to the rescue for the duration, and two passes killing
// one session on one tick's snapshot would kill the fresh process with the
// stale peer's evidence. A backoff stamp (kv, per agent) is written BEFORE
// the kill and honoured for standingModelDriftBackoff, so a refusal that
// re-fires on the resumed session costs one respawn per window rather than
// one per tick, and a stamp that cannot be written refuses the kill outright
// — an unwritable watermark is the loop. And the identity gate is the
// rescue's own (standingKillTarget): a nameless or unmatched pane defers the
// round, never kills on the positional fallback.

// standingModelDriftBackoff is the least time between two drift respawns of
// one agent. Six hours: long enough that a fallback which re-fires on every
// resume costs four restarts a day rather than a crash loop, short enough
// that a drift caught at dawn is fixed before the day's work rides on it.
const standingModelDriftBackoff = 6 * time.Hour

// standingModelDriftKey is the kv row holding the unix-seconds stamp of the
// last drift respawn for one agent — the backoff's whole state. Keyed on the
// agent id like the hit rows (standingHitKey), and disjoint from them.
func standingModelDriftKey(agentID string) string { return "model_drift:" + agentID }

// standingModelDrift answers whether the agent's recorded transcript says it
// is running on a model other than the one it is pinned to, returning the id
// it found either way. Every quiet arm is a refusal to judge rather than a
// verdict of "no drift": no pinned model (nothing to hold against), no live
// peer or a NAMELESS one (a claude still starting, whose transcript tail is
// the previous process'), no recorded session, no resolvable transcript, and
// a tail record whose model is not a claude id at all — `<synthetic>` is a
// limit banner's stamp, and acting on it would respawn an agent the rescue
// is about to handle for the real reason.
func standingModelDrift(a standingAgent, all []*core.Peer, recorded string) (got string, drifted bool) {
	if a.Model == "" || a.Find == nil || recorded == "" {
		return "", false
	}
	p := a.Find(all)
	if p == nil || p.Name == "" {
		return "", false
	}
	transcript := core.TranscriptPathForSession(recorded)
	if transcript == "" {
		return "", false
	}
	got = core.TranscriptModelID(transcript)
	if !strings.HasPrefix(got, "claude-") {
		return got, false
	}
	return got, got != a.Model
}

// readStandingModelStamp reads the backoff stamp; a missing or malformed row
// reads as zero, i.e. never respawned for drift.
func readStandingModelStamp(st *store.Store, agentID string) int64 {
	if st == nil {
		return 0
	}
	raw, err := st.GetKV(standingModelDriftKey(agentID))
	if err != nil || raw == "" {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// standingModelPass is the tick's drift arm for one standing agent. It runs
// BEFORE the health pass and the limit rescue on the daemon's loop, and defers
// to an unresolved limit hit, which together are what keep the two kill
// paths from both acting on one snapshot.
//
// A respawn that FAILS keeps its stamp: the failure is logged and notified,
// and the next attempt is a backoff window away rather than a tick away —
// the same reason the rescue's failed rounds are counted, applied to a pass
// that has no attempt cap of its own.
func (d *daemonState) standingModelPass(a standingAgent, st *store.Store, all []*core.Peer, live map[string]bool, now time.Time) {
	if st == nil || a.Enabled == nil || !a.Enabled() || !live[a.Session] {
		return
	}
	recorded := ""
	if a.RecordedSession != nil {
		recorded, _ = a.RecordedSession(st)
	}
	got, drifted := standingModelDrift(a, all, recorded)
	if !drifted {
		return
	}
	if hit := readLimitHitByKey(standingHitKey(a.ID)); hit.At != 0 && hit.HandledAt == 0 {
		// The rescue owns this pane until the hit resolves; the drift is
		// re-read next tick and every tick after, so nothing is lost by
		// waiting for it.
		return
	}
	if last := readStandingModelStamp(st, a.ID); last != 0 && now.Sub(time.Unix(last, 0)) < standingModelDriftBackoff {
		return
	}
	peer, why := standingKillTarget(a, all, recorded)
	if peer == nil {
		dlogEvent("info", "model-drift-skipped", "%s: on %s, pinned to %s — %s", a.ID, got, a.Model, why)
		return
	}
	// Stamped BEFORE the kill, and a stamp that cannot be written refuses the
	// kill: the stamp is the only thing between this pass and a respawn every
	// fifteen seconds for as long as the drift persists.
	if err := st.SetKV(standingModelDriftKey(a.ID), strconv.FormatInt(now.Unix(), 10)); err != nil {
		dlogErr("%s: model-drift backoff stamp failed — not respawning without it: %v", a.ID, err)
		return
	}
	if err := standingRespawn(a, st, live); err != nil {
		dlogEvent("error", "model-drift-failed", "%s: on %s, pinned to %s — pane %s: respawn failed: %v", a.ID, got, a.Model, peer.Pane, err)
		notify("kraken ⚠ "+a.ID, "running on "+got+" instead of "+a.Model+" — respawn failed, restart it by hand")
		return
	}
	dlogEvent("info", "model-drift-respawn", "%s: was on %s, pinned to %s — pane %s killed, %s resumed", a.ID, got, a.Model, peer.Pane, a.ID)
	notify("kraken ⇄ "+a.ID, "was running on "+got+" — resumed on "+a.Model)
}
