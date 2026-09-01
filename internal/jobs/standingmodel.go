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
// (core.TranscriptModelStamp). Peer.Model is not used even though `k ls`
// renders the same drift from it, because it is the shortened display form
// (`opus-4-8`, which cannot be compared against the full-id constants) and it
// is read off the peer's DISCOVERED session id, where this pass's whole rule
// is the recorded one.
//
// Two clocks and four gates keep a respawn honest, each against a measured
// or probed shape — the rate-limit deferral, the backoff stamp, the identity
// gate, and the idle-peer deferral at the kill site (a working agent is not
// killed mid-turn). The record's own TIMESTAMP is held against the agent's
// spawn stamp: a `-r` resume appends to the SAME transcript, so between any
// restart (the limit rescue's, or carl's own `k k`) and the new process'
// first assistant record, the tail still says the previous process' model —
// judged without the clock, a freshly resumed, correctly-pinned agent reads
// as drifted and eats one spurious kill plus a false notification. An open
// RATE-LIMIT hit defers the pass entirely: that pane belongs to the rescue
// for the duration, and two passes killing one session on one tick's
// snapshot would kill the fresh process on the stale peer's evidence — but
// only a hit the rescue will actually act on defers, because a hit of any
// other error type is never resolved by anything (the rescue skips it
// unresolved by design), and deferring to one would disable this pass
// silently forever. And a backoff stamp (kv, per agent) is written BEFORE
// the kill and honoured for standingModelDriftBackoff, so a refusal that
// re-fires on the resumed session costs one respawn per window rather than
// one per tick; a stamp that cannot be written refuses the kill outright —
// an unwritable watermark is the loop.
//
// Identity is standingKillTarget's, and that is the only LOOKUP into the
// discovery snapshot the pass makes (the idle check below reads a field off
// the peer that lookup returned): the drift judgment itself is peer-free (a
// transcript comparison),
// so the kill gate is also the presence gate — a session holding no pane
// that is the agent by name or by the recorded session id defers the round,
// exactly as the rescue's resume arm does, and a still-booting claude (no
// session file yet, so neither signature) is refused by the same check.

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
// verdict of "no drift": no pinned model (nothing to hold against), no
// recorded session, no resolvable transcript, a tail record whose model is
// not a claude id at all (`<synthetic>` is a limit banner's stamp, and acting
// on it would respawn an agent the rescue is about to handle for the real
// reason), a record with no parseable clock, and a record OLDER than the
// agent's spawn stamp — the previous process' tail, not this one's evidence.
func standingModelDrift(a standingAgent, recorded string, spawnedAt int64) (got string, drifted bool) {
	if a.Model == "" || recorded == "" {
		return "", false
	}
	transcript := core.TranscriptPathForSession(recorded)
	if transcript == "" {
		return "", false
	}
	got, at := core.TranscriptModelStamp(transcript)
	if !strings.HasPrefix(got, "claude-") {
		return got, false
	}
	if at == 0 || (spawnedAt > 0 && at < spawnedAt) {
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
// BEFORE the health pass and the limit rescue on the daemon's loop and defers
// to an unresolved rate-limit hit — those two are what keep the kill paths to
// at most one per tick. The `<synthetic>` refusal above usually separates the
// two on the tail as well, but not always: LastStall skips sidechain and meta
// records where TranscriptModelStamp does not, so one sidechain assistant
// record after a limit banner splits which record each reads (probed) — the
// ordering and the deferral are the guards, not the refusal.
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
	spawnedAt := int64(0)
	if a.SpawnStamp != nil {
		spawnedAt = a.SpawnStamp(st)
	}
	got, drifted := standingModelDrift(a, recorded, spawnedAt)
	if !drifted {
		return
	}
	if hit := readLimitHitByKey(standingHitKey(a.ID)); hit.At != 0 && hit.HandledAt == 0 && hit.Error == "rate_limit" {
		// The rescue owns this pane until the hit resolves; the drift is
		// re-read next tick and every tick after, so nothing is lost by
		// waiting for it. Scoped to rate_limit — the one error the rescue
		// acts on — because any other hit stays unresolved forever by
		// design, and an unscoped deferral would let one stray delivery
		// disable this pass silently for the daemon's life.
		return
	}
	if last := readStandingModelStamp(st, a.ID); last != 0 && now.Sub(time.Unix(last, 0)) < standingModelDriftBackoff {
		return
	}
	peer, why := standingKillTarget(a, all, recorded)
	if peer == nil {
		// Deduped on the reason: a sustained refusal (a live session whose
		// only pane is not the agent — in practice something carl started in
		// a session named kronos/iris) would otherwise log every tick for as
		// long as it lasts, the exact unbounded-event hazard the rescue's
		// no-agents arm names. One line per distinct reason per daemon run.
		nkey := "model-drift-skip:" + a.ID + "|" + why
		if d.Notified[nkey] == 0 {
			dlogEvent("info", "model-drift-skipped", "%s: on %s, pinned to %s — %s", a.ID, got, a.Model, why)
			d.Notified[nkey] = now.Unix()
		}
		return
	}
	if peer.Status == "running" {
		// Makes a mid-turn kill unlikely rather than impossible — the status
		// is the tick's snapshot, so a turn starting after it is not seen,
		// and a pane at a question prompt reads "question" (kronos' brief
		// forbids asking, so that state is not a working turn).
		//
		// This is the binary's one unprompted kill of a HEALTHY agent (the
		// rescue only ever kills a latched one), so it does not land
		// mid-turn: the pass runs every tick and the drift is not going
		// anywhere, so deferring to an idle tick costs seconds and saves
		// whatever the turn was doing. Silent — a log line per tick of a
		// long turn would be spam.
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
