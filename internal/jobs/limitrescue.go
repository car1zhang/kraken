package jobs

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"kraken/internal/core"
)

// --- mid-run usage-limit rescue ---
//
// A cell whose claudes have hit the account's usage limit is stuck in a way
// no nudge can fix: the turn failed because the credential behind it is
// exhausted, so the only repair is a different account. The hook (limithook.go)
// records the hit; this file decides what to do about it and does it.
//
// The design has no restart in it, and that is the measured fact it rests on
// (2026-08-05, Claude Code 2.1.222): a RUNNING claude re-reads its credential
// live. The credential LANE is latched at process start — a cell starts with
// no CLAUDE_CODE_OAUTH_TOKEN, so its lane is the macOS keychain item
// core.SwapMainTo already writes — while the VALUE inside that lane is
// re-resolved on the next turn, in both directions. So overwriting the item
// under a live session and then telling it to carry on is enough, and every
// claude in the cell reads that same item: zeus and its teammates recover
// together. Nothing here may kill a tmux session, restart a cell, or call
// ReactivateJob — that would throw away the very crew this rescue exists to
// preserve.
//
// Propagation is fast but not instant (a request ~0.4s after the write still
// used the old token), which is why the swap and the nudge are two separate
// steps a settle window apart rather than one.

// limitRescueAction is the decision's outcome — what the pass should do with
// a recorded hit on this tick.
type limitRescueAction string

const (
	rescueSkip         limitRescueAction = "skip"
	rescueRotate       limitRescueAction = "rotate"
	rescueNudge        limitRescueAction = "nudge"
	rescueParkManual   limitRescueAction = "park-manual"
	rescueParkNoTarget limitRescueAction = "park-no-target"
)

const (
	// limitRescueSettle is how long after the swap the nudge waits, so the
	// new credential has propagated to the running sessions. It is enforced
	// ACROSS ticks by the gate chain rather than by sleeping inside one: the
	// daemon's own DaemonInterval cadence is the timer, and a pass that
	// blocks the tick would stall every other job's pass behind it.
	limitRescueSettle = 15 * time.Second
	// limitRescueMaxAttempts bounds how many rotate attempts one hit gets
	// before it parks. Without a bound, a pool with nothing healthy in it
	// would retry the same doomed selection forever.
	limitRescueMaxAttempts = 3
	// limitRescueRetryInterval is the backoff between rotate attempts,
	// matching the health pass's repokeAfter cadence — a pool account whose
	// window has reset becomes eligible on a timescale of minutes, not
	// seconds.
	limitRescueRetryInterval = 10 * time.Minute
	// limitResetSettle is how long past the banner's stated reset moment the
	// reset-nudge waits. The banner's time carries minute precision and no
	// seconds, and the serverside window is not guaranteed to reopen on the
	// exact minute boundary — a nudge landing seconds early would burn the
	// hit's resolution on a turn that fails on the same limit.
	limitResetSettle = 2 * time.Minute
	// limitResetMaxWait is the staleness bound on a parsed reset target. A
	// FRESH banner names a reset at most one session window (~5h) ahead, so
	// a target further out than this can only be a stale banner read long
	// after it was written — the transcript trigger stamps hit.At at the
	// TICK it noticed the stall, which lags the banner's write time (both
	// real reset-clause rows in kraken.db were recorded after the time
	// their clause names, by 84 and 3 minutes; a daemon restart or a
	// sleeping Mac can stretch that lag to hours), and a stated time
	// already behind the lagged anchor would otherwise roll a full day
	// forward. A stale banner means the window already reopened, so the
	// parse answers "due now" rather than "due tomorrow".
	limitResetMaxWait = 6 * time.Hour
)

// limitRescueNudgeMsg is what every agent in the rescued cell is told. It
// names no account: the message is typed into a pane and lands in a
// transcript, and the identity of the account belongs in the notification
// carl reads, not in the crew's context.
const limitRescueNudgeMsg = "kraken: your last turn failed on a usage limit. The main Claude account has been switched to a healthier one and the new credential is live — continue where you left off."

// limitResetNudgeMsg is the no-swap sibling of limitRescueNudgeMsg: the
// account never moved, its own window reopened. Same rule about naming no
// account — this lands in a transcript.
const limitResetNudgeMsg = "kraken: your last turn failed on a usage limit. That account's usage window has since reset — continue where you left off."

// limitResetRe matches the reset clause the SESSION-limit banner carries:
// "resets 4:50am (America/New_York)", "resets 10:20pm" — an hour, optional
// minutes, am/pm, and an optional IANA zone in parens. That family is the
// only one with a bare-time clause: the weekly banner's date form ("resets
// Aug 12 at 3pm") deliberately never matches (measured — it parses to zero),
// and banners with no clause at all (credit entitlement, Fable tier) never
// match either, so both route to the terminal park exactly as before. That
// scoping matters: a bare time can only describe a window at most a day
// out, so a distant weekly reset must never reach the re-arm through it.
var limitResetRe = regexp.MustCompile(`(?i)resets\s+(\d{1,2})(?::(\d{2}))?\s*(am|pm)(?:\s*\(([^)]+)\))?`)

// parseLimitReset extracts the banner's stated reset moment from a hit's
// recorded details, as unix seconds — the first wall-clock occurrence of the
// stated time STRICTLY AFTER the hit itself, in the banner's own zone when
// it names one and the machine's otherwise. Zero when the text carries no
// parseable clause, which callers treat as "no reset re-arm" rather than an
// error: the terminal park is the correct fallback for a banner that never
// said when it ends.
//
// Anchoring on the HIT time rather than on the current tick is what makes
// the answer stable: every tick re-deriving "next 4:50am after now" would
// walk the target forward a day the moment the first computation's day
// passed, and the re-arm would never fire.
func parseLimitReset(details string, hitAt time.Time) int64 {
	m := limitResetRe.FindStringSubmatch(details)
	if m == nil {
		return 0
	}
	hour, err := strconv.Atoi(m[1])
	if err != nil || hour < 1 || hour > 12 {
		return 0
	}
	minute := 0
	if m[2] != "" {
		minute, err = strconv.Atoi(m[2])
		if err != nil || minute > 59 {
			return 0
		}
	}
	if strings.EqualFold(m[3], "pm") {
		if hour != 12 {
			hour += 12
		}
	} else if hour == 12 {
		hour = 0
	}
	loc := time.Local
	if m[4] != "" {
		if l, lerr := time.LoadLocation(strings.TrimSpace(m[4])); lerr == nil {
			loc = l
		}
	}
	at := hitAt.In(loc)
	candidate := time.Date(at.Year(), at.Month(), at.Day(), hour, minute, 0, 0, loc)
	if !candidate.After(at) {
		candidate = time.Date(at.Year(), at.Month(), at.Day()+1, hour, minute, 0, 0, loc)
	}
	// Staleness cap — see limitResetMaxWait. It binds ANY target beyond the
	// cap, rolled past midnight or not: the cap's premise is that a fresh
	// session banner cannot name a reset more than one window (~5h) ahead,
	// and a rolled-only guard made a 21-minute shift in when the daemon
	// noticed the stall swing the outcome by 22 hours — the same stale
	// 10:20pm banner read at 23:44 was already-due, and read at 00:05 was a
	// 22h15m park, because crossing midnight un-rolled it. Answering with
	// the hit time itself makes the stale case already-due: the decide
	// chain's settle margin then releases the nudge on the next tick, which
	// is the right action for a window that reopened before kraken ever
	// read the banner. The cap also bounds the fall-back DST roll (once a
	// year, same shape as a stale banner). Spring-forward names a wall time
	// that does not exist; Go documents time.Date's choice there as
	// unspecified, and this toolchain measures it resolving an hour EARLY —
	// the nudge then fails on the still-closed window and resolves the hit,
	// but the failed turn writes a fresh banner and a fresh, correctly
	// anchored record within a tick or two, so the cost is one wasted turn
	// once a year, not a strand.
	if candidate.Sub(at) > limitResetMaxWait {
		return at.Unix()
	}
	return candidate.Unix()
}

// limitHitTriggerKey and limitHitTriggerDue are the health pass's churn
// bound, split out of the tick so the rule can be tested without discovery,
// tmux or a daemon. The pass they guard is the transcript-derived trigger:
// CellHealth classifies off the LAST transcript entry, so a cell that stays
// stalled re-reports the same reason every tick. RecordLimitHit's fold now
// absorbs a re-delivery onto the still-unresolved hit, so ungated ticks
// would no longer walk the pool — but they would still write a log event
// and re-marshal the record every 15 seconds per stalled cell, and once the
// hit RESOLVES an ungated trigger would open a fresh one on the very next
// tick, restarting the whole rescue against a banner already handled.
//
// The bound has to expire, and that half was the expensive one to learn: a
// key that is only ever SET reads as "one hit per distinct banner for the
// daemon's whole lifetime", and a weekly-limit banner is byte-identical
// across two exhaustions days apart, so the second one would record nothing
// and rescue nothing — silently, because the health pass's own notification
// key suppresses on the same string. Re-arming off the stamp the map already
// holds keeps the churn bounded while making suppression temporary.
//
// Keyed on the job's immutable ID rather than its slug for the reason the
// record itself is: archiving frees a slug, so a new job reusing an archived
// generation's name would otherwise inherit its suppression.
func limitHitTriggerKey(id int64, reason string) string {
	return "limithit:" + strconv.FormatInt(id, 10) + "|" + reason
}

// limitHitTriggerDue reports whether the transcript trigger may record again:
// never recorded (stamp zero), or recorded longer ago than the rotate
// backoff. A stamp in the future — a clock stepped backwards — reads as not
// due, which defers a rescue rather than churning one, the same conservative
// direction every other window comparison in this file takes.
func limitHitTriggerDue(stamp int64, now time.Time) bool {
	return stamp == 0 || now.Sub(time.Unix(stamp, 0)) > limitRescueRetryInterval
}

// limitRescueDecide is the whole decision surface, pure over the five facts
// it is handed so the gate chain is table-testable without a store, tmux or a
// clock. The ORDER of the gates is itself the contract — a later gate must
// never pre-empt an earlier one — and two of the orderings carry meaning:
//
//   - gate 6 (manual mode) sits above every rotate path, so a job carl has
//     pinned to manual is never rotated whatever else is true.
//   - gate 5 (a landed swap) sits ABOVE gate 6, because once the account has
//     actually been swapped the nudge must complete even if the mode changed
//     in between: a swapped cell left un-nudged is worse than either outcome
//     on its own — the credential moved and nobody told the crew.
//
// The second return is a short human reason, non-empty for every outcome
// except skip (a skip is the overwhelmingly common tick and has nothing to
// say).
func limitRescueDecide(hit LimitHit, mode string, cellLive, targetAvailable bool, now time.Time) (limitRescueAction, string) {
	if hit.At == 0 {
		return rescueSkip, ""
	}
	if hit.HandledAt != 0 {
		return rescueSkip, ""
	}
	if hit.Error != "rate_limit" {
		// Every other StopFailure error type is recorded and logged (that log
		// is how a schema change gets noticed) but never acted on: an auth
		// failure or an overload is not fixed by a different account.
		return rescueSkip, ""
	}
	if !cellLive {
		// Nothing to rescue: a swap only helps sessions that are still running
		// to re-read the credential.
		return rescueSkip, ""
	}
	if hit.SwappedAt != 0 {
		if now.Sub(time.Unix(hit.SwappedAt, 0)) >= limitRescueSettle {
			return rescueNudge, "credential swapped to " + hit.SwappedTo + " — resuming the cell"
		}
		return rescueSkip, ""
	}
	if mode == "manual" {
		return rescueParkManual, "account rotation is pinned to manual — not rotating"
	}
	// Attempts spent. With no reset time on the record this is the defensive
	// floor it always was: limitRescueApply resolves a park at the moment the
	// INCREMENTED attempt count reaches the max, so an exhausted hit already
	// carries a HandledAt and never gets past gate 2, and this gate only
	// catches a record whose counter was advanced by something else. WITH a
	// reset time, an exhausted hit is the ordinary parked-until-reset state:
	// the park deliberately left it unresolved, and this gate is what holds
	// it (skip) until the banner's own stated reopen time has passed, then
	// releases the no-swap nudge. Four cells measured 2026-08-06 sat 42m to
	// 10h37m dead on the far side of a reset their banner named, because the
	// park was terminal.
	if hit.Attempts >= limitRescueMaxAttempts {
		if hit.ResetAt != 0 {
			if now.Sub(time.Unix(hit.ResetAt, 0)) >= limitResetSettle {
				return rescueNudge, "usage window reset — resuming the cell"
			}
			return rescueSkip, ""
		}
		return rescueParkNoTarget, fmt.Sprintf("no rotation target after %d attempts", hit.Attempts)
	}
	if hit.Attempts > 0 && now.Sub(time.Unix(hit.LastAttempt, 0)) < limitRescueRetryInterval {
		return rescueSkip, ""
	}
	if !targetAvailable {
		return rescueParkNoTarget, "no healthy pool account to rotate to"
	}
	return rescueRotate, "usage limit hit — rotating the main account"
}

// selectRescueTarget picks the pool slot the rescue would rotate into, and
// reports main's own email alongside it (the snapshot is the only place the
// rescue learns who main currently is, and both the selector and the
// notification need it). Shared by the pass' targetAvailable probe and by
// rescueLimitedCell's own selection under the lock, so the two can never
// disagree about what "a target exists" means.
//
// Deliberately no NeedsRotation check and no row-freshness filter: the limit
// hit IS the evidence that main is exhausted, and the polled snapshot can be
// minutes stale and still show that account as healthy. The attempt counter
// is what bounds a selection made on stale candidate data.
//
// The selector is reached through rotation.go's selectTarget seam rather
// than core.SelectRotationTarget directly, for the reason that seam was
// declared: it is what lets a rescue's decision be exercised without a real
// pool on disk, and RotationGate and the account API already go through it.
func selectRescueTarget(now time.Time) (target, mainEmail, why string) {
	snap, ok := ReadUsageSnapshot()
	if !ok {
		return "", "", "no usage snapshot"
	}
	var pool []core.AccountUsage
	for _, a := range snap.Accounts {
		if a.Slug == "main" {
			mainEmail = a.Email
			continue
		}
		pool = append(pool, a)
	}
	target, why = selectTarget(pool, mainEmail, now.UnixMilli())
	return target, mainEmail, why
}

// rescueLimitedCell performs the swap half of a rotate decision. The step
// order is the obligation: take the same cross-process lock RotationGate and
// every manual switch take BEFORE selecting, so a target can never be chosen
// against a pool another swap is mid-way through rewriting.
//
// The swap itself is core.SwapMainTo unchanged — the journal, the save-back
// slot, the case canonicalization and the rollback are all load-bearing and
// belong to exactly one implementation — reached through rotation.go's
// swapMain seam, the same identifier RotationGate and the account API use,
// which is what makes the swapped path exercisable without a real keychain.
// This function kills nothing and restarts nothing: the running claudes
// re-read the credential themselves.
func rescueLimitedCell(j *Job, now time.Time) (toEmail string, outcome string, err error) {
	release, ok, lockErr := AcquireRotationLock()
	if !ok {
		// Contention is the ordinary case and is not an error — the other
		// holder is doing the same job. A lock that cannot even be OPENED is
		// a filesystem problem worth naming, but it is still "no swap
		// happened", so it reports the same outcome and is logged here rather
		// than raised: the caller's job is to count the attempt either way.
		if lockErr != nil {
			dlogEvent("info", "limit-rescue-skipped", "%s: rotation lock unavailable: %v", j.Slug, lockErr)
		}
		return "", "lock-busy", nil
	}
	defer release()

	target, _, why := selectRescueTarget(now)
	if target == "" {
		dlogEvent("info", "limit-rescue-skipped", "%s: no rotation target: %s", j.Slug, why)
		return "", "no-target", nil
	}
	_, to, swapErr := swapMain(target)
	if swapErr != nil {
		return "", "swap-failed", swapErr
	}
	return to, "swapped", nil
}

// nudgeCellAgents tells every agent in the cell to carry on, now that the
// credential under it has changed. It targets the same pane set Stop does
// (cellAgentPanes) — zeus plus its tmux teammates, never a side thread — for
// the reason the whole design exists: the teammates were never killed, so a
// message is all they need to resume.
//
// core.SendMessage rather than a raw send-keys: it re-verifies the pane still
// holds the expected agent and refuses a pane sitting on a question prompt,
// where a trailing Enter would answer the question instead of resuming.
//
// A per-pane failure is counted out rather than raised — rescuing three of
// four agents beats rescuing none — so the only error is an empty target set.
func nudgeCellAgents(j *Job, all []*core.Peer, msg string) (nudged int, err error) {
	targets := cellAgentPanes(j, all)
	if len(targets) == 0 {
		return 0, fmt.Errorf("%s: no live agent panes found to nudge", j.Slug)
	}
	for _, p := range targets {
		if sendErr := core.SendMessage(p, msg); sendErr != nil {
			dlogEvent("error", "limit-rescue-failed", "%s: nudge of pane %s failed: %v", j.Slug, p.Pane, sendErr)
			continue
		}
		nudged++
	}
	return nudged, nil
}

// limitRescueApply folds what just happened back into the hit record and
// returns the record to store. Pure — no store, no tmux, no clock, no
// notification, no event; limitRescue keeps all of those and calls this for
// the record half alone.
//
// It exists as its own symbol because the write-back is where the feature's
// subtlest rule lives and there was previously nothing to assert it against:
// a park-no-target resolves ONLY once the attempts are spent, which is what
// lets a pool account whose window resets an hour from now still rescue this
// cell instead of the hit being closed on the first empty pool.
//
// Increment-then-test is the obligation in that arm: the counter is bumped
// first and the INCREMENTED value is what decides whether the hit gives up,
// so the third failed park is the one that resolves and no fourth attempt is
// ever recorded. The nudge arm's own failure path (outcome "no-agents")
// takes the same treatment for the same reason — see below.
//
// outcome is read on two arms and means something different on each: for
// rescueRotate it is rescueLimitedCell's own outcome word, and for
// rescueNudge only the literal "no-agents" is significant (anything else,
// including the empty string, is a round that reached at least one pane).
func limitRescueApply(hit LimitHit, action limitRescueAction, outcome, toEmail string, now time.Time) LimitHit {
	switch action {
	case rescueSkip:
		// Nothing happened, so nothing is recorded — a skip must not stamp a
		// LastAttempt, or the retry backoff would restart on every tick and
		// the next rotate would never come due.
	case rescueRotate:
		hit.Attempts++
		hit.LastAttempt = now.Unix()
		if outcome == "swapped" {
			hit.SwappedAt = now.Unix()
			hit.SwappedTo = toEmail
			// HandledAt stays zero deliberately: the swap is only half the
			// rescue, and the nudge a settle window later is what resolves it.
		}
	case rescueNudge:
		if outcome == "no-agents" {
			// A round that reached NOBODY. It must not resolve the hit (that
			// strands a swapped cell), but it cannot be unbounded either —
			// an open hit is re-decided every tick, and a crew that can
			// never be reached would write this event forever.
			// Increment-then-test, exactly as rescueParkNoTarget does, so a
			// cell whose crew cannot be reached parks instead. The cap is
			// path-relative: a reset re-arm ARRIVES with Attempts already at
			// the max (the parks spent them), so capping it at the same
			// absolute number would give its nudge exactly one shot at a
			// discovery snapshot the code below calls "not exotic" to find
			// empty — the swap path's nudge effectively gets several rounds,
			// and the reset path gets the same allowance on top of the
			// attempts it inherited.
			failCap := limitRescueMaxAttempts
			if hit.SwappedAt == 0 && hit.ResetAt != 0 {
				failCap = limitRescueMaxAttempts * 2
			}
			hit.Attempts++
			hit.LastAttempt = now.Unix()
			if hit.Attempts >= failCap {
				hit.HandledAt = now.Unix()
				hit.Outcome = "parked-no-agents"
			}
			break
		}
		// Reached at least one pane: resolved whether or not EVERY pane was
		// reachable — the account is already swapped, and re-running the swap
		// because one pane was busy would rotate a second time for nothing.
		// The outcome word records WHICH nudge this was: a swap's completion,
		// or the no-swap resume after the window's own reset.
		hit.HandledAt = now.Unix()
		if hit.SwappedAt != 0 {
			hit.Outcome = "rotated"
		} else {
			hit.Outcome = "resumed-after-reset"
		}
	case rescueParkManual:
		// No counter touched: carl pinned the account deliberately, so there
		// is nothing here to retry and nothing to count.
		hit.HandledAt = now.Unix()
		hit.Outcome = "parked-manual"
	case rescueParkNoTarget:
		hit.Attempts++
		hit.LastAttempt = now.Unix()
		if hit.Attempts >= limitRescueMaxAttempts {
			// Terminal only when the banner never said when the window
			// reopens. With a parsed ResetAt the hit stays UNRESOLVED on
			// purpose: the decide chain's exhausted-attempts gate holds it
			// (skip) until the reset passes, then releases the no-swap nudge
			// — resolving here would close the one record that can wake the
			// cell.
			if hit.ResetAt != 0 {
				hit.Outcome = "parked-until-reset"
			} else {
				hit.HandledAt = now.Unix()
				hit.Outcome = "parked-no-target"
			}
		}
	}
	return hit
}

// mergeLimitRescueState folds the rescue's six owned fields onto whatever is
// stored NOW, which is this side's half of the read-modify-write race — the
// delivery path needs the mirror-image merge for the race to be closed, and
// that half is RecordLimitHit's. Two writers share this one kv row: the
// hook's delivery path (from the API handler's goroutine) and this pass
// (from the tick's). The rescue owns
// Attempts, LastAttempt, SwappedAt, SwappedTo, HandledAt and Outcome; the
// delivery side owns Deliveries and the identity fields. Writing the record
// we read a swap ago would erase a Deliveries increment that landed in
// between — and, far worse, a hook writing back ITS stale copy erases
// SwappedAt, after which gate 5 never fires and the cell is swapped a second
// time.
//
// ok is false when the stored record is a DIFFERENT hit (a fresh At, which
// RecordLimitHit writes when the previous one was resolved, carried another
// error, or fell outside the dedupe window). That is not a merge conflict to
// resolve — our counters describe an event the store has already moved past,
// and stamping them onto the new one would tell a brand-new limit it had
// already been swapped. The caller reports it instead.
//
// A stored record that reads as zero (row missing, or JSON that no longer
// parses) is the one case where our copy is written wholesale: it is the
// only readable version of the record left.
func mergeLimitRescueState(cur, hit LimitHit) (LimitHit, bool) {
	if cur.At == 0 {
		return hit, true
	}
	if cur.At != hit.At {
		return cur, false
	}
	cur.Attempts = hit.Attempts
	cur.LastAttempt = hit.LastAttempt
	cur.SwappedAt = hit.SwappedAt
	cur.SwappedTo = hit.SwappedTo
	cur.HandledAt = hit.HandledAt
	cur.Outcome = hit.Outcome
	cur.ResetAt = hit.ResetAt
	return cur, true
}

// saveLimitRescueState persists the rescue's own half of the hit record —
// the attempt counters, the swap stamp and the resolution — merged under a
// fresh read (see mergeLimitRescueState) so a delivery that landed while the
// pass was working is neither clobbered nor allowed to clobber.
//
// It RAISES its failures rather than swallowing them, and the rotate arm is
// why: on every other arm a lost write costs one repeated decision on the
// next tick, but a lost SwappedAt means the next tick reads a hit that was
// never swapped and performs a SECOND machine-wide credential change, on
// another pool account, with nothing anywhere saying why. The record is the
// only thing standing between one swap and two.
//
// The write is retried once, immediately and without a sleep: a tick must
// not block, and the failure this is aimed at (a momentarily busy database)
// is the kind a second attempt clears.
func saveLimitRescueState(j *Job, hit LimitHit) error {
	st := ActiveStore()
	if st == nil {
		return fmt.Errorf("%s: no store wired", j.Slug)
	}
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		merged, ok := mergeLimitRescueState(ReadLimitHit(j), hit)
		if !ok {
			return fmt.Errorf("%s: a newer limit hit replaced the record mid-rescue — this rescue's bookkeeping was not stored", j.Slug)
		}
		data, err := json.Marshal(merged)
		if err != nil {
			return fmt.Errorf("%s: marshal limit-hit record: %w", j.Slug, err)
		}
		if lastErr = st.SetKV(limitHitKey(j.ID), string(data)); lastErr == nil {
			return nil
		}
	}
	return lastErr
}

// limitRescue is the per-cell pass, one job per tick, called from the
// daemon's tick beside syncColor and inside the same !mirror block: a mirror
// daemon must RECORD hits (the API handler is not gated) but must never
// perform a machine-wide credential swap or type into a live pane.
//
// The two halves of a rescue land on different ticks by design: a rotate
// stamps SwappedAt and leaves HandledAt at zero, so the next pass that finds
// the settle window elapsed performs the nudge and only then resolves the
// hit.
//
// What this owns is the effects — the store write, the deduped notification,
// the event. The record half is limitRescueApply's, which is pure and is
// therefore the thing a test can hold: every field rule of the write-back
// lives there rather than inline here.
//
// Two arms deliberately record NOTHING. A lock-busy rotate is a no-op, since
// the lock is contended by every cell spawn and counting that as an attempt
// parks a hit that never reached the pool; and a nudge that reached no pane
// leaves the hit open, since resolving it would strand a cell whose account
// moved and whose crew was never told. Both mean "try again next tick", and
// in both the absence of a write is what says so.
//
// The nudge arm's write lands AFTER its tmux sends rather than before,
// which is the one place this pass holds a record across seconds of work.
// It has to: whether the hit may be resolved at all is not known until the
// sends have been tried. What makes that safe is saveLimitRescueState
// merging under a fresh read — a delivery landing during those sends folds
// into the stored row and survives, and a delivery that started a NEW hit
// makes the write abandon rather than stamp this rescue's counters onto
// someone else's event.
func (d *daemonState) limitRescue(j *Job, all []*core.Peer, live map[string]bool) {
	hit := ReadLimitHit(j)
	if hit.At == 0 || hit.HandledAt != 0 {
		// Gates 1 and 2 of the decision say the same thing; short-circuiting
		// both here is what keeps the pass free on the overwhelmingly common
		// tick. The resolved half matters as much as the empty one: nothing
		// ever deletes this row, so without it every job that has EVER hit a
		// limit would pay a usage-snapshot read, a full SelectRotationTarget
		// ranking and an os.ReadFile of rotation.json every 15 seconds for
		// the rest of the daemon's life, only to be skipped by gate 2.
		return
	}
	now := time.Now()
	// Derive the banner's stated reset moment before deciding, so the decide
	// chain and the park write-back both see it. In-memory only until the
	// next apply-save persists it; a details string with no clause re-derives
	// zero every tick, which costs one regexp scan and writes nothing.
	if hit.ResetAt == 0 {
		hit.ResetAt = parseLimitReset(hit.Details, time.Unix(hit.At, 0))
	}
	// A parked-until-reset hit is unresolved for its whole wait — the one
	// long-lived shape gate 2's short-circuit above cannot cover — and
	// selectRescueTarget below costs a usage-snapshot read plus a full
	// rotation ranking. Skip the wait ticks before paying it. In auto mode
	// the decide chain would return skip anyway (its exhausted-attempts
	// gate holds a future reset); under a MANUAL pin it would return
	// park-manual instead, and suppressing that is deliberate — the hit has
	// already parked once, its notification already fired, and re-parking
	// it would resolve the record and kill the pending reset nudge. The
	// skip also makes the wait's rotate-blindness explicit: a pool account
	// that turns healthy mid-wait is not used — the attempts were spent,
	// the window's own reset is nearer than another swap cycle, and
	// re-arming rotates here would re-open the uncapped-selection problem
	// the attempt cap exists for.
	if hit.SwappedAt == 0 && hit.Attempts >= limitRescueMaxAttempts && hit.ResetAt != 0 &&
		now.Sub(time.Unix(hit.ResetAt, 0)) < limitResetSettle {
		return
	}
	target, mainEmail, _ := selectRescueTarget(now)
	action, reason := limitRescueDecide(hit, RotationMode(), live[CellSession(j)], target != "", now)
	if action == rescueSkip {
		return
	}

	// The dedup key carries the HIT, not just the job and the action: nothing
	// clears d.Notified for this pass and the daemon runs for weeks, so a
	// slug+action key would announce a job's first rescue and then silently
	// swap the machine's login for every later one. The park-manual notice is
	// the sharpest case — it says a cell is stuck waiting on carl, which is
	// exactly the message that must repeat.
	notifyOnce := func(title, msg string) {
		key := "limit:" + j.Slug + "|" + string(action) + "|" + strconv.FormatInt(hit.At, 10)
		if d.Notified[key] != 0 {
			return
		}
		notify(title, msg)
		d.Notified[key] = now.Unix()
	}
	if mainEmail == "" {
		mainEmail = "main"
	}

	switch action {
	case rescueRotate:
		to, outcome, err := rescueLimitedCell(j, now)
		if outcome == "lock-busy" {
			// Nothing happened, so nothing is counted. This is the same lock
			// every cell spawn takes, so losing it is an ordinary event —
			// spending one of three attempts and starting a ten-minute
			// backoff on it would park a hit that never once reached the pool.
			dlogEvent("info", "limit-rescue-skipped", "%s: rotation lock busy — retrying next tick", j.Slug)
			return
		}
		hit = limitRescueApply(hit, action, outcome, to, now)
		saveErr := saveLimitRescueState(j, hit)
		switch {
		case outcome == "swapped":
			notifyOnce("kraken ⇄ "+j.Slug, fmt.Sprintf("usage limit — switched %s → %s", mainEmail, to))
			dlogEvent("info", "limit-rescue-done", "%s: %s: swapped %s → %s", j.Slug, reason, mainEmail, to)
			if saveErr != nil {
				// The one lost write in this pass that is not self-correcting:
				// the account HAS moved, and a record with no SwappedAt sends
				// the next tick around for a second swap onto a third account.
				dlogEvent("error", "limit-rescue-failed", "%s: swapped to %s but recording it failed — a later tick may swap again: %v", j.Slug, to, saveErr)
			}
		case err != nil:
			dlogEvent("error", "limit-rescue-failed", "%s: rotate attempt %d failed (%s): %v", j.Slug, hit.Attempts, outcome, err)
		default:
			dlogEvent("info", "limit-rescue-skipped", "%s: rotate attempt %d did not swap (%s)", j.Slug, hit.Attempts, outcome)
		}
		if saveErr != nil && outcome != "swapped" {
			dlogErr("%s: persist limit-hit record failed: %v", j.Slug, saveErr)
		}

	case rescueNudge:
		// Which nudge this is decides what the crew is told: a landed swap
		// means a new credential is live; a zero SwappedAt on this arm can
		// only be the reset re-arm, where the account never moved.
		nudgeMsg := limitRescueNudgeMsg
		if hit.SwappedAt == 0 {
			nudgeMsg = limitResetNudgeMsg
		}
		nudged, err := nudgeCellAgents(j, all, nudgeMsg)
		if err != nil || nudged == 0 {
			// A hit resolved with NOBODY nudged is a permanently stranded
			// cell: the account was swapped, the crew was never told, and a
			// stalled cell makes no further API calls — so it can never
			// produce another hook, and gate 2 would skip this hit forever.
			// Empty is not exotic (an empty discovery snapshot, a zeus that
			// died inside a surviving session, a cell whose only live panes
			// are side threads), so the round is counted rather than
			// resolved and a later tick tries again — but only up to
			// limitRescueMaxAttempts of them, after which the hit parks. An
			// unbounded open swapped hit is worse than a parked one: it
			// re-arms the rotate gate every time a late delivery supersedes
			// it, and it writes this event every 15s for as long as the
			// session lives. A PARTIAL failure still resolves — that is what
			// nudged > 0 buys.
			hit = limitRescueApply(hit, action, "no-agents", "", now)
			if saveErr := saveLimitRescueState(j, hit); saveErr != nil {
				dlogErr("%s: persist limit-hit record failed: %v", j.Slug, saveErr)
			}
			if hit.HandledAt != 0 {
				// Which nudge failed decides what carl is told: on the reset
				// path no credential ever moved, and a notice claiming a
				// switch would report a machine-wide change that never
				// happened.
				if hit.SwappedAt != 0 {
					notifyOnce("kraken ⚠ "+j.Slug, "usage limit — account switched but the cell's agents could not be reached")
					dlogEvent("info", "limit-rescue-skipped", "%s: parked after %d nudge rounds reached no agent pane — account was switched, cell was not resumed", j.Slug, hit.Attempts)
				} else {
					notifyOnce("kraken ⚠ "+j.Slug, "usage limit — window reset but the cell's agents could not be reached")
					dlogEvent("info", "limit-rescue-skipped", "%s: parked after %d nudge rounds reached no agent pane — window had reset, cell was not resumed", j.Slug, hit.Attempts)
				}
				return
			}
			dlogEvent("error", "limit-rescue-failed", "%s: %s: nudge round %d reached no agent pane (%v)", j.Slug, reason, hit.Attempts, err)
			return
		}
		hit = limitRescueApply(hit, action, "", "", now)
		if err := saveLimitRescueState(j, hit); err != nil {
			dlogErr("%s: persist limit-hit record failed: %v", j.Slug, err)
		}
		dlogEvent("info", "limit-rescue-done", "%s: %s (nudged %d agent pane(s))", j.Slug, reason, nudged)

	case rescueParkManual:
		hit = limitRescueApply(hit, action, "", "", now)
		if err := saveLimitRescueState(j, hit); err != nil {
			dlogErr("%s: persist limit-hit record failed: %v", j.Slug, err)
		}
		notifyOnce("kraken ⚠ "+j.Slug, "hit a usage limit — "+reason)
		dlogEvent("info", "limit-rescue-skipped", "%s: %s", j.Slug, reason)

	case rescueParkNoTarget:
		hit = limitRescueApply(hit, action, "", "", now)
		if err := saveLimitRescueState(j, hit); err != nil {
			dlogErr("%s: persist limit-hit record failed: %v", j.Slug, err)
		}
		// Worded for attempt 1, where the notification actually fires
		// (notifyOnce keys on the hit, and the first park is the first
		// chance): two more rotate attempts are still ahead, so the reset
		// nudge is conditional, not promised. The time carries a date
		// whenever the target is not today — a bare "10:20pm" for a
		// tomorrow target reads as tonight.
		parkNote := "hit a usage limit — " + reason
		if hit.ResetAt != 0 {
			reset := time.Unix(hit.ResetAt, 0).Local()
			form := "3:04pm"
			if reset.Format("2006-01-02") != now.Local().Format("2006-01-02") {
				form = "3:04pm on Jan 2"
			}
			parkNote += " — will keep retrying, and if no account appears, nudge the cell after the window resets at " + reset.Format(form)
		}
		notifyOnce("kraken ⚠ "+j.Slug, parkNote)
		dlogEvent("info", "limit-rescue-skipped", "%s: %s (attempt %d)", j.Slug, reason, hit.Attempts)
	}
}
