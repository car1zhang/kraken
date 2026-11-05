package jobs

import (
	"fmt"
	"time"

	"kraken/internal/core"
)

// jobfreeze.go — the jobs-level writer for a job's hold mark (schema v41's
// frozen_at). A frozen job is a BLOCKED job the operator has said does not
// need them right now: it waits on a reviewer, a deploy, a long-running
// watch. Nothing here changes the job's state, and no state reader treats a
// frozen job differently — the daemon keeps rolling its cell call, tracking
// its PRs and bumping its pane exactly as for any blocked job. The mark only
// changes the board: the card moves out of the blocked column's way, and out
// of the header's flag count.
//
// A frozen job thaws when its cell is activated, and there are two ways the
// daemon learns that.
//
// The first needs no code here: when a tick's roll call reads the cell as
// working, the ordinary blocked → working state write clears frozen_at in
// the same UPDATE (store.updateJobFieldsTx), so the job is an ordinary
// blocked job, with the ordinary blocked notification, the next time it
// stops.
//
// The second is frozenThaw below, and it exists because the roll call
// samples the cell once per tick: a turn that starts and ends between two
// ticks (a watch firing into a few seconds of work) is never read as
// working, and the first arm alone would leave that job frozen with new
// output nobody was told about. So each tick also asks the lead's transcript
// whether it ran since the hold.

// frozenRanSince reports whether j's lead produced output after the hold was
// placed, read from its transcript. A seam so the thaw's decision is testable
// without a transcript on disk.
var frozenRanSince = func(j *Job, all []*core.Peer) bool {
	return core.TranscriptRanSince(zeusTranscriptPath(j, all), j.FrozenAt)
}

// frozenThaw is the per-tick second arm of the thaw, for a job the tick has
// just confirmed as blocked. It returns what the tick should do about the
// operator, and the value to store back as the job's pending-thaw tick.
//
// Two steps, one tick apart. On the tick that first sees the lead ran since
// the hold, the mark is cleared at once, so the card is back in the blocked
// column as soon as the board next polls, and the tick is remembered. On the
// IMMEDIATELY following tick, if the job is still blocked, notify is true:
// the cell ran and has stopped, which is the block event the operator was
// never told about. The one-tick wait is holdBlocked's reason run the other
// way: a cell still mid-turn can read as idle for a single tick, and
// notifying on that reading would announce a block for a cell that is
// working. If the turn is in fact still running, the next tick reads working,
// the ordinary state write takes over, and this arm says nothing.
//
// pending is the tick on which the mark was cleared, zero when none is in
// progress; adjacency is required for holdBlocked's reason (a stale entry
// must never confirm a later, unrelated reading). A job the operator froze
// again in the tick between is not announced: they have just looked at it.
//
// The caller invokes this only for a job whose confirmed state this tick is
// blocked, and drops the pending tick on any other reading.
func frozenThaw(j *Job, live bool, all []*core.Peer, pending, tickN int) (notify bool, next int) {
	if pending != 0 {
		// the mark is already gone; this tick only decides the notification
		return live && j.FrozenAt == 0 && tickN-pending == 1, 0
	}
	if !live || j.FrozenAt == 0 || !frozenRanSince(j, all) {
		return false, 0
	}
	st := ActiveStore()
	if st == nil {
		return false, 0
	}
	// Conditional on the stamp this tick judged: j was loaded at the top of
	// the tick, and a hold the operator placed since then is newer than the
	// turn being reacted to, so it is left standing.
	cleared, err := st.ThawJobFrozenAt(j.ID, j.FrozenAt)
	if err != nil {
		dlogErr("thaw %s failed: %v", j.Slug, err)
		return false, 0
	}
	if !cleared {
		return false, 0
	}
	j.FrozenAt = 0
	dlogEvent("info", "job-thaw", "%s: frozen job's cell ran a turn since the hold; hold cleared", j.Slug)
	return false, tickN
}

// SetJobFrozen puts j on hold (frozen true) or takes it off (false), and
// mirrors the result into j.FrozenAt.
//
// Freezing a job whose stored state is not "blocked" is refused with
// store.ErrJobNotBlocked (wrapped): the check is the store's, made inside the
// UPDATE, so a cell that started working after the caller loaded j is not
// frozen mid-turn. Thawing is idempotent.
//
// Like SetJobLoad it bypasses WriteJobFields, so it bumps neither `rev` nor
// `updated_at`: a hold must not reset the card's age, and it is not a field
// the editor's optimistic lock covers. With no store wired it refuses, since
// there is no `frozen:` job-file field to fall back to.
func SetJobFrozen(j *Job, frozen bool) error {
	st := ActiveStore()
	if st == nil {
		return fmt.Errorf("set frozen for %s: no store is wired — the hold mark is store-backed only", j.Slug)
	}
	var at int64
	if frozen {
		at = time.Now().UnixMilli()
	}
	if err := st.SetJobFrozen(j.ID, at); err != nil {
		return fmt.Errorf("set frozen for %s: %w", j.Slug, err)
	}
	j.FrozenAt = at
	return nil
}
