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
// no message can fix: the turn failed because the credential behind it is
// exhausted, so the only repair is a different account. The hook (limithook.go)
// records the hit; this file decides what to do about it and does it.
//
// The repair is a REBIND followed by a kill+resume of ZEUS. The rebind half
// is Q2's whole point: the cell is bound to one pool account
// (CLAUDE_SECURESTORAGE_CONFIG_DIR, binding.go), so moving it off an
// exhausted window means stamping a different slot on THIS job's row —
// nothing machine-wide moves, no other cell is displaced, carl's own main
// login is untouched, and there is no save-back slot and no cross-process
// flock to manage. An UNBOUND cell (the pool was dry when it spawned, or it
// predates binding) gets bound by its rescue, which is strictly better than
// the machine swap that used to happen here.
//
// The resume half is unchanged and is forced by measurement (2026-08-07,
// Claude Code 2.1.224). A RUNNING claude does not re-read a changed
// credential: it resolves one at session start and re-resolves it only on an
// auth error or on a refresh scheduled off the OLD token's expiry, so turns
// kept billing the outgoing account for as long as the probe ran. And a
// claude that has already 429'd carries a client-side limit latch — Claude
// Code blocks further requests locally until the reset time the banner
// names. On a latched process, a credential change and a real /usage fetch
// showing a healthy account were both measured to leave the latch exactly
// where it was. A message typed into such a pane cannot produce a successful
// turn no matter what it says.
//
// So the resume arm kills zeus' claude in place and respawns it with the
// full cell-spawn surface plus `-r <session-id>` (resumeCellAgents through
// core.RespawnPeerPane). That command is built by spawnCommand, which is
// where the new binding's env assignment goes on, so the fresh process comes
// up on the rebound account, adopts it on its first turn, and carries no
// latch; the conversation comes back intact.
//
// Zeus alone, because a tmux teammate has no resolvable session id to resume
// (discover.go leaves SessionID empty for every teammate row) — so the crew
// is handed to zeus as its first instruction rather than pretended at here.
// The blast radius stays that one pane: nothing here may kill a tmux
// session, restart a cell, or call ReactivateJob — the cell's windows, its
// worktree and its session are exactly what this rescue exists to preserve.
//
// The rebind and the resume stay two steps a settle window apart. Not for
// propagation — a new process reads the keychain when it starts — but as a
// debounce: the halves land on different ticks, so the new binding is
// durably recorded, and visibly complete, before anything SIGKILLs a pane.

// limitRescueAction is the decision's outcome — what the pass should do with
// a recorded hit on this tick.
//
// The ROTATION vocabulary below is deliberately kept while every comment,
// log line, notification and prompt in this file says rebind, and the reason
// is that these three are PERSISTED where the prose is not. `rescueRotate`'s
// literal "rotate" is a component of limitRescue's `d.Notified` dedup key,
// so renaming it re-notifies every hit the daemon has already announced;
// `LimitHit.SwappedAt`/`SwappedTo` and the resolved outcome word "rotated"
// are JSON fields and values in kv rows that outlive this process, and a
// rename orphans every hit in flight at the moment of the upgrade —
// SwappedAt in particular is the gate that stops a rebound cell being
// rebound a second time. A future rename owes those rows a migration; until
// then the words mean what the file's prose says, not what they say.
type limitRescueAction string

const (
	rescueSkip         limitRescueAction = "skip"
	rescueRotate       limitRescueAction = "rotate"
	rescueResume       limitRescueAction = "resume"
	rescueParkManual   limitRescueAction = "park-manual"
	rescueParkNoTarget limitRescueAction = "park-no-target"
)

const (
	// limitRescueSettle is how long after the rebind the resume waits. It is
	// NOT a propagation delay — a fresh process reads the keychain when it
	// starts, so there is nothing to wait for — it is a debounce: the rebind
	// and the resume are separate arms of the decide chain, and this window
	// is what stops a tick that has just rebound from also killing the pane,
	// so the new binding is recorded and a half-failed one is visible before
	// anything irreversible happens.
	// It is enforced ACROSS ticks by the gate chain rather than by sleeping
	// inside one: the daemon's own DaemonInterval cadence is the timer, and
	// a pass that blocks the tick would stall every other job's pass behind
	// it.
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
	// reset-resume waits. The banner's time carries minute precision and no
	// seconds, and the serverside window is not guaranteed to reopen on the
	// exact minute boundary — a resume landing seconds early would burn the
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

// limitRescueResumePrompt is zeus' first turn back after a rebind-path rescue.
// It is a PROMPT handed to a process that was just SIGKILLed and resumed,
// not a message typed at a running agent, and everything unusual about it
// follows from that: the reader has no memory of the last few seconds, its
// background tasks and in-flight tool calls did not survive, and its
// teammates are still sitting where they were. Saying so is the whole job of
// this text — an agent that assumes its crew came back with it will wait on
// panes that will never answer.
//
// It names no account, and it says THIS CELL's account rather than the
// machine's: since Q2 a rescue rebinds one job and touches nothing else, so
// a prompt claiming the main login moved would tell zeus something false
// about the rest of the machine. Which account it moved to belongs in the
// notification carl reads, not in the cell's context.
const limitRescueResumePrompt = "kraken: your last turns failed on a usage limit. This cell has been rebound to a healthier account — only this cell; nothing else on the machine moved — and kraken killed and resumed you so this process would pick the new credential up. Your conversation is intact, but any background tasks and in-flight tool calls from before the resume are gone. Your teammates were NOT resumed: they are still limit-stalled or dead, and reviving or replacing them (TaskStop plus fresh spawns, or SendMessage to a live one) is your first move. Then re-check the work in flight and continue."

// limitResetResumePrompt is the no-rebind sibling: the account never moved,
// its own usage window reopened. Same shape and the same warnings — the
// resume was just as fatal to this process' background work either way — and
// no account is named here either.
const limitResetResumePrompt = "kraken: your last turns failed on a usage limit. That account's usage window has since reset — the account never changed — and kraken killed and resumed you so this process would drop the limit it had latched onto. Your conversation is intact, but any background tasks and in-flight tool calls from before the resume are gone. Your teammates were NOT resumed: they are still limit-stalled or dead, and reviving or replacing them (TaskStop plus fresh spawns, or SendMessage to a live one) is your first move. Then re-check the work in flight and continue."

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
	// chain's settle margin then releases the resume on the next tick, which
	// is the right action for a window that reopened before kraken ever
	// read the banner. The cap also bounds the fall-back DST roll (once a
	// year, same shape as a stale banner). Spring-forward names a wall time
	// that does not exist; Go documents time.Date's choice there as
	// unspecified, and this toolchain measures it resolving an hour EARLY —
	// the resume then fails on the still-closed window and resolves the hit,
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
//     pinned to manual is never rebound whatever else is true.
//   - gate 5 (a landed rebind) sits ABOVE gate 6, because once the cell has
//     actually been rebound the resume must complete even if the mode changed
//     in between: a rebound cell left un-resumed is worse than either outcome
//     on its own — its credential moved and the crew is still latched onto
//     the old one.
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
		// Nothing to rescue: the resume respawns a live cell's zeus pane,
		// and a dead cell has none to respawn.
		return rescueSkip, ""
	}
	if hit.SwappedAt != 0 {
		if now.Sub(time.Unix(hit.SwappedAt, 0)) >= limitRescueSettle {
			return rescueResume, "cell rebound to " + hit.SwappedTo + " — resuming it"
		}
		return rescueSkip, ""
	}
	if mode == "manual" {
		return rescueParkManual, "account rotation is pinned to manual — not rebinding"
	}
	// Attempts spent. With no reset time on the record this is the defensive
	// floor it always was: limitRescueApply resolves a park at the moment the
	// INCREMENTED attempt count reaches the max, so an exhausted hit already
	// carries a HandledAt and never gets past gate 2, and this gate only
	// catches a record whose counter was advanced by something else. WITH a
	// reset time, an exhausted hit is the ordinary parked-until-reset state:
	// the park deliberately left it unresolved, and this gate is what holds
	// it (skip) until the banner's own stated reopen time has passed, then
	// releases the no-swap resume. Four cells measured 2026-08-06 sat 42m to
	// 10h37m dead on the far side of a reset their banner named, because the
	// park was terminal.
	if hit.Attempts >= limitRescueMaxAttempts {
		if hit.ResetAt != 0 {
			if now.Sub(time.Unix(hit.ResetAt, 0)) >= limitResetSettle {
				return rescueResume, "usage window reset — resuming the cell"
			}
			return rescueSkip, ""
		}
		return rescueParkNoTarget, fmt.Sprintf("no rotation target after %d attempts", hit.Attempts)
	}
	if hit.Attempts > 0 && now.Sub(time.Unix(hit.LastAttempt, 0)) < limitRescueRetryInterval {
		return rescueSkip, ""
	}
	if !targetAvailable {
		return rescueParkNoTarget, "no healthy pool account to rebind to"
	}
	return rescueRotate, "usage limit hit — rebinding the cell to a healthier account"
}

// cellAccountEmail is the account this cell is billing RIGHT NOW: its own
// binding when it has one, else the machine's live main identity, since an
// unbound cell runs on main. It is what the rescue must move OFF, and what
// the notification names as the "from" side.
//
// The live identity (mainIdentityEmail, ~/.claude.json) is preferred over
// the snapshot's main row for an unbound cell because the snapshot is a
// poll: a manual `k account <slug>` switch is invisible to it until the next
// usage pass lands, and feeding a stale name to the selector would exclude
// the wrong row — the exclusion is by email, so pointing it at the wrong
// identity aims it at exactly the wrong account.
//
// It keys on the EMAIL rather than on AccountSlug, and that rests on an
// invariant rather than on carelessness: a bound job always has an email,
// because core.SelectBindTarget's candidate gates exclude a row with
// `Email == ""` outright ("no identity"), and its two writers — bindAccount
// and rescueLimitedCell — both stamp exactly what that selector returned. A
// slug with no email therefore cannot reach these columns today. It matters
// which way this fails if that ever changes: such a job would report MAIN's
// email as the account to move off, so the rescue would exclude main, leave
// the exhausted slot a candidate, and can rebind the cell straight back onto
// it. A third writer, or a relaxed gate, owes this function a slug-keyed
// arm — and `SelectBindTarget`'s exclusion set is email-only, so it owes it
// a slug arm too.
func cellAccountEmail(j *Job) string {
	if j != nil && j.AccountEmail != "" {
		return j.AccountEmail
	}
	return mainIdentityEmail()
}

// rescueProbeDue reports whether this tick's decision can actually USE a
// target-availability answer, so the pass can decline to compute one.
//
// It exists because `targetAvailable` is a value parameter: limitRescueDecide
// cannot ask for it lazily, so the probe runs before the chain does — and
// the probe is now expensive (a usage-snapshot read, a full LoadJobs, a tmux
// round trip and one keychain fork per pool slot) while the gates that
// discard it cover minutes-to-forever. Each line below mirrors, in order,
// one decide-chain gate that answers WITHOUT consulting targetAvailable;
// mirroring rather than reordering keeps limitRescueDecide's own gate order
// — which is its stated contract — the single source of truth, and a gate
// added there that ignores the target simply costs a probe again rather than
// changing an outcome.
//
// Gates 1-4 (no hit, resolved, wrong error, dead cell) are the caller's
// short-circuits already; this covers the four after them.
func rescueProbeDue(hit LimitHit, mode string, now time.Time) bool {
	if hit.SwappedAt != 0 {
		return false // gate 5: a landed rebind owns this tick — resume or wait
	}
	if mode == "manual" {
		return false // gate 6: pinned, so no account decision is taken at all
	}
	if hit.Attempts >= limitRescueMaxAttempts {
		return false // gate 7: spent — the reset re-arm or the terminal park
	}
	if hit.Attempts > 0 && now.Sub(time.Unix(hit.LastAttempt, 0)) < limitRescueRetryInterval {
		return false // gate 8: inside the retry backoff
	}
	return true
}

// selectRescueBind picks the pool slot the rescue would rebind this cell to.
// Shared by the pass' targetAvailable probe and by rescueLimitedCell's own
// selection under the binder mutex, so the two can never disagree about what
// "a target exists" means.
//
// It goes through decideBind, which is what makes it the SAME decision a
// spawn would take — the eligibility gates, the keychain-item guard and the
// emptiest-slot ranking are all one implementation, and a rescue picking by
// different rules than a spawn is how two paths drift into disagreeing about
// which accounts are usable.
//
// Two exclusions are layered on top of decideBind's own. The mainEmail
// argument carries the account the CELL is on rather than the machine's,
// which is what moves the rescue off the exhausted window: rebinding to the
// account that just 429'd would resolve the hit onto the same wall. And
// main's own identity is excluded too, always — not only for an unbound
// cell — because SwapMainTo mirrors main into a pool slot of its own, and
// binding a rescued cell to that mirror would put it on carl's interactive
// account under a different name. For an unbound cell the two collapse into
// one, which is why the first argument alone would not be enough.
//
// Both needles resolve through resolvedMainEmail (binding.go), so an
// unreadable ~/.claude.json falls back to the snapshot's own main row rather
// than silently dropping the mirror exclusion entirely.
//
// Deliberately no row-freshness filter: the limit hit IS the evidence that
// the cell's account is exhausted, and the polled snapshot can be minutes
// stale and still show that account as healthy. The attempt counter is what
// bounds a selection made on stale candidate data.
//
// prevSlug is deliberately empty. Stickiness exists so a reactivated cell
// returns to the account that built its prompt cache; a rescue is the exact
// opposite situation — that account is the one that ran out.
func selectRescueBind(j *Job, now time.Time) (slug, email, why string) {
	snap, ok := ReadUsageSnapshot()
	if !ok {
		return "", "", "no usage snapshot"
	}
	mainEmail := resolvedMainEmail(snap)
	from := cellAccountEmail(j)
	if from == "" {
		// An unbound cell whose ~/.claude.json could not be read: it is on
		// main, and the snapshot is the only thing left that can name main.
		from = mainEmail
	}
	// decideBind takes ONE excluded identity, so main's is applied here by
	// dropping its rows before the snapshot goes in. Doing it as a filter
	// rather than as a check on the result matters: rejecting a pick after
	// the fact would answer "no target" while a perfectly good second-choice
	// slot sat in the pool.
	if mainEmail != "" && !strings.EqualFold(mainEmail, from) {
		var kept []core.AccountUsage
		for _, a := range snap.Accounts {
			if a.Email != "" && strings.EqualFold(a.Email, mainEmail) {
				continue
			}
			kept = append(kept, a)
		}
		snap.Accounts = kept
	}
	// Same predicate a spawn-time bind uses (bindCountPredicate, binding.go):
	// a rescue runs from the daemon tick while carl may be submitting, and
	// counting a sibling's just-stamped binding as zero would rebind this
	// cell onto the slot that submit is about to spawn on.
	counts := boundCounts(LoadJobs(), bindCountPredicate(now))
	return decideBind(snap, counts, RotationMode(), from, "", slotItemExists, now)
}

// rescueLimitedCell performs the REBIND half of a rotate decision: pick a
// healthier pool slot and stamp it on this one job's row. It kills nothing;
// resumeCellAgents does that, a settle window later, once the new binding is
// recorded.
//
// It takes the binder's own mutex (bindMu, binding.go) and NOT the
// cross-process rotation flock, and the difference is the feature. The flock
// existed because a swap rewrote machine-wide credentials that every process
// on the Mac shares, so two swappers had to be kept apart across processes.
// A rebind writes one row in kraken.db and displaces nothing, so the only
// race left is the in-process one the flock never addressed: two select-then-
// stamp sequences reading the same bound counts and both landing on the same
// emptiest slot. bindMu is what closes that, and it is the same mutex a
// spawn-time bind takes, so a rescue and a submit cannot interleave either.
//
// core.SwapMainTo is not called here and cannot be reached from here — that
// is Q2, and it is why a rescue no longer takes any account away from carl's
// interactive sessions or from any other cell.
//
// No usage-pass kick: the rebind changed no account's usage and moved no
// machine-wide identity, so there is nothing for a fresh snapshot to catch
// up on.
func rescueLimitedCell(j *Job, now time.Time) (toEmail string, outcome string, err error) {
	bindMu.Lock()
	defer bindMu.Unlock()

	slug, email, why := selectRescueBind(j, now)
	if slug == "" {
		dlogEvent("info", "limit-rescue-skipped", "%s: no rebind target: %s", j.Slug, why)
		return "", "no-target", nil
	}
	st := ActiveStore()
	if st == nil {
		return "", "bind-failed", fmt.Errorf("%s: no store wired", j.Slug)
	}
	at := now.UnixMilli()
	if bindErr := st.SetJobAccount(j.ID, slug, email, at); bindErr != nil {
		return "", "bind-failed", bindErr
	}
	// The in-memory Job is updated too, and it is load-bearing rather than
	// tidy: the resume a settle window later builds zeus' command through
	// spawnCommand, which reads j.AccountSlug for the env prefix. On this
	// tick nothing spawns, but the same *Job is what the rest of this pass
	// logs and notifies from.
	j.AccountSlug, j.AccountEmail, j.AccountAt = slug, email, at
	return email, "rebound", nil
}

// resumeCellAgents brings the cell back on the account it is bound to NOW:
// zeus' claude is killed in its own pane and its session is respawned there,
// with prompt as its first turn. The binding reaches the new process through
// spawnCommand's env prefix, which is built from the job row this pass has
// already rewritten — so "the account that is current" is a fact about the
// row, not about the machine.
//
// ZEUS AND NOBODY ELSE, which is a measured constraint rather than a policy
// choice. A resume needs a session id, and a tmux teammate has none: it
// writes no ~/.claude/sessions file and its transcript is not locatable, so
// discovery leaves SessionID empty for every teammate row (discover.go's
// teammate arm). Sweeping cellAgentPanes here — Stop's set — would therefore
// respawn zeus, have core.ResumeGuard refuse every teammate for want of an
// id, and still report a resume that "reached at least one pane", resolving
// the hit with the rest of the crew latched onto a dead account. Codex and
// shell rows are out for the same class of reason (nothing to resume with
// `claude -r`), and a side thread is out because it always is: carl branched
// it to ASK about the work, and killing his conversation is never part of
// rescuing the job.
//
// So the crew is zeus' problem, and the prompt says so — reviving or
// replacing its teammates is zeus' first move on the way back. That is the
// one thing a rescue can do about processes it cannot address itself, and it
// is also what zeus is for.
//
// The command is the FULL cell-spawn surface plus `-r`, built through the
// same spawnCommand/zeusSpawnArgv machinery ReactivateJob uses. A bare `-r`
// is not enough and that is measured (thread.go's threadSpawnCommand
// records it, and cell.go's reactivation path was corrected for it): a
// resumed session does NOT retain an earlier run's --append-system-prompt,
// and the model comes from the command line too — a briefless, modelless
// resume comes back as a plain claude holding zeus' history, on whatever
// tier claude defaults to, with a crew that would inherit that tier.
//
// The respawn goes through the respawnPeerPane seam rather than
// core.RespawnPeerPane directly, so who gets resumed and with what command is
// exercisable without a tmux server; the seam owns the guard, the pre-kill
// re-verification and the session-file cleanup.
//
// CellZeus alone is not enough to authorize a kill, and that is the
// difference between this caller and every other one. Its election has a
// POSITIONAL fallback — lowest pane index among the session's depth-0
// claudes — for the startup window where zeus has not written its name yet,
// and a `k b` fork or any unregistered claude carl started in the cell's
// session can win it. Reading a bit of the wrong transcript is what that
// fallback was priced against; SIGKILLing carl's side conversation is not.
// So the candidate must also be zeus BY IDENTITY: its --name, or the session
// id the job file itself records for zeus. Anything else is counted out and
// the round is retried next tick, when the name has usually landed.
//
// An empty CWD is refused: PeerCWD falls back to $HOME, and a zeus respawned
// in $HOME cannot see the worktree it was working in. No other zeus spawn
// site has to check, since they build the worktree path themselves rather
// than reading it off a peer.
//
// The side-thread set is read STRICTLY here (liveThreadPanesForOK). Its
// ordinary fail-open — an unreadable store reads as "no threads" — is
// correct for Stop, where the cost is a stray Escape, and wrong for a verb
// that kills: a thread pane missing from the set is carl's conversation
// destroyed. Unreadable therefore counts the round out; a skipped round is
// retried, a SIGKILL is not.
//
// Only a missing zeus pane is an error — that is the "no-agents" outcome the
// apply arm counts and eventually parks on. Every other refusal (identity,
// cwd, thread set, command build, seam) is counted out with its own log
// line at the point it fires, and leaves resumed at 0, which the caller
// already reads as the same "reached nobody, try again next tick".
func resumeCellAgents(j *Job, all []*core.Peer, prompt string) (resumed int, err error) {
	zeus := CellZeus(j, all)
	if zeus == nil || zeus.Pane == "" {
		return 0, fmt.Errorf("%s: no zeus pane found to resume", j.Slug)
	}
	if zeus.Name != ZeusName(j) && !(j.Session != "" && zeus.SessionID == j.Session) {
		dlogEvent("info", "limit-rescue-skipped", "%s: pane %s is not zeus by identity (name %q, session %q) — not killing it", j.Slug, zeus.Pane, zeus.Name, zeus.SessionID)
		return 0, nil
	}
	if zeus.CWD == "" {
		dlogEvent("error", "limit-rescue-failed", "%s: zeus pane %s has no cwd — a respawn would land in $HOME instead of the worktree", j.Slug, zeus.Pane)
		return 0, nil
	}
	threads, ok := liveThreadPanesForOK(j)
	if !ok {
		dlogEvent("error", "limit-rescue-failed", "%s: cannot read the cell's live threads — not killing pane %s while a side thread may be in it", j.Slug, zeus.Pane)
		return 0, nil
	}
	if threads[zeus.Pane] {
		dlogEvent("info", "limit-rescue-skipped", "%s: pane %s holds a live side thread — not killing it", j.Slug, zeus.Pane)
		return 0, nil
	}
	// Same brief + goals + graph concatenation as the other two zeus spawns
	// (StartCell, ReactivateJob): a rescued zeus is a resumed session, which
	// does not retain an earlier run's --append-system-prompt, so all three
	// injections have to be re-staged here or they are simply absent.
	cmd, cmdErr := spawnCommand(j, zeusSpawnArgv(j, "-r", zeus.SessionID), zeusBrief(j)+goalContext(j)+graphContext(j), prompt)
	if cmdErr != nil {
		dlogEvent("error", "limit-rescue-failed", "%s: building zeus' resume command failed: %v", j.Slug, cmdErr)
		return 0, nil
	}
	if resumeErr := respawnPeerPane(zeus, cmd); resumeErr != nil {
		dlogEvent("error", "limit-rescue-failed", "%s: resume of zeus pane %s failed: %v", j.Slug, zeus.Pane, resumeErr)
		return 0, nil
	}
	return 1, nil
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
// ever recorded. The resume arm's own failure path (outcome "no-agents")
// takes the same treatment for the same reason — see below.
//
// outcome is read on two arms and means something different on each: for
// rescueRotate it is rescueLimitedCell's own outcome word, and for
// rescueResume only the literal "no-agents" is significant (anything else,
// including the empty string, is a round whose respawn landed).
func limitRescueApply(hit LimitHit, action limitRescueAction, outcome, toEmail string, now time.Time) LimitHit {
	switch action {
	case rescueSkip:
		// Nothing happened, so nothing is recorded — a skip must not stamp a
		// LastAttempt, or the retry backoff would restart on every tick and
		// the next rotate would never come due.
	case rescueRotate:
		hit.Attempts++
		hit.LastAttempt = now.Unix()
		if outcome == "rebound" {
			hit.SwappedAt = now.Unix()
			hit.SwappedTo = toEmail
			// HandledAt stays zero deliberately: the rebind is only half the
			// rescue, and the resume a settle window later is what resolves it.
		}
	case rescueResume:
		if outcome == "no-agents" {
			// A round that respawned NOTHING. It must not resolve the hit
			// (that strands a rebound cell), but it cannot be unbounded
			// either — an open hit is re-decided every tick, and a zeus that
			// can never be reached would write this event forever.
			// Increment-then-test, exactly as rescueParkNoTarget does, so a
			// cell whose zeus cannot be reached parks instead. The cap is
			// path-relative: a reset re-arm ARRIVES with Attempts already at
			// the max (the parks spent them), so capping it at the same
			// absolute number would give its resume exactly one shot at a
			// discovery snapshot the code below calls "not exotic" to find
			// empty — the rebind path's resume effectively gets several
			// rounds, and the reset path gets the same allowance on top of
			// the attempts it inherited.
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
		// Zeus is back on the account the cell is bound to now: resolved. The
		// binding already moved, and re-running the rebind because the crew it
		// now has to revive is still down would burn a second pool slot for
		// nothing. The outcome word records WHICH resume this was: a rebind's
		// completion, or the no-rebind resume after the window's own reset.
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
			// (skip) until the reset passes, then releases the no-swap resume
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
// we read a rebind ago would erase a Deliveries increment that landed in
// between — and, far worse, a hook writing back ITS stale copy erases
// SwappedAt, after which gate 5 never fires and the cell is rebound a second
// time, burning a second pool slot.
//
// ok is false when the stored record is a DIFFERENT hit (a fresh At, which
// RecordLimitHit writes when the previous one was resolved, carried another
// error, or fell outside the dedupe window). That is not a merge conflict to
// resolve — our counters describe an event the store has already moved past,
// and stamping them onto the new one would tell a brand-new limit it had
// already been rebound. The caller reports it instead.
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
// the attempt counters, the rebind stamp and the resolution — merged under a
// fresh read (see mergeLimitRescueState) so a delivery that landed while the
// pass was working is neither clobbered nor allowed to clobber.
//
// It RAISES its failures rather than swallowing them, and the rotate arm is
// why: on every other arm a lost write costs one repeated decision on the
// next tick, but a lost SwappedAt means the next tick reads a hit whose cell
// was never rebound and rebinds it AGAIN, onto a third pool account, with
// nothing anywhere saying why — and the cell's own claudes are still running
// against the binding from the first rebind, since only the resume picks a
// new one up. The record is the only thing standing between one rebind and
// two.
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
// rewrite a job's binding or destroy and recreate a live pane's process.
//
// The two halves of a rescue land on different ticks by design: a rotate
// stamps SwappedAt and leaves HandledAt at zero, so the next pass that finds
// the settle window elapsed performs the resume and only then resolves the
// hit.
//
// What this owns is the effects — the store write, the deduped notification,
// the event. The record half is limitRescueApply's, which is pure and is
// therefore the thing a test can hold: every field rule of the write-back
// lives there rather than inline here.
//
// One arm deliberately records NOTHING: a resume that never reached zeus
// leaves the hit open, since resolving it would strand a cell whose binding
// moved and whose every claude is still latched onto the old one. It means
// "try again next tick", and the absence of a write is what says so.
//
// The resume arm's write lands AFTER the respawn rather than before, which
// is the one place this pass holds a record across seconds of work. It has
// to: whether the hit may be resolved at all is not known until the respawn
// has been tried. What makes that safe is saveLimitRescueState merging
// under a fresh read — a delivery landing during the respawn folds into the
// stored row and survives, and a delivery that started a NEW hit makes the
// write abandon rather than stamp this rescue's counters onto someone
// else's event.
func (d *daemonState) limitRescue(j *Job, all []*core.Peer, live map[string]bool) {
	hit := ReadLimitHit(j)
	if hit.At == 0 || hit.HandledAt != 0 {
		// Gates 1 and 2 of the decision say the same thing; short-circuiting
		// both here is what keeps the pass free on the overwhelmingly common
		// tick. The resolved half matters as much as the empty one: nothing
		// ever deletes this row, so without it every job that has EVER hit a
		// limit would pay a usage-snapshot read, a full job load with its
		// bound-count sweep, a keychain probe per pool slot and an
		// os.ReadFile of rotation.json every 15 seconds for the rest of the
		// daemon's life, only to be skipped by gate 2.
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
	// selectRescueBind below costs a usage-snapshot read, a full job load and
	// a keychain probe per slot. Skip the wait ticks before paying it. In auto mode
	// the decide chain would return skip anyway (its exhausted-attempts
	// gate holds a future reset); under a MANUAL pin it would return
	// park-manual instead, and suppressing that is deliberate — the hit has
	// already parked once, its notification already fired, and re-parking
	// it would resolve the record and kill the pending reset resume. The
	// skip also makes the wait's rebind-blindness explicit: a pool account
	// that turns healthy mid-wait is not used — the attempts were spent,
	// the window's own reset is nearer than another rebind cycle, and
	// re-arming rebinds here would re-open the uncapped-selection problem
	// the attempt cap exists for.
	if hit.SwappedAt == 0 && hit.Attempts >= limitRescueMaxAttempts && hit.ResetAt != 0 &&
		now.Sub(time.Unix(hit.ResetAt, 0)) < limitResetSettle {
		return
	}
	// FindCellSession rather than live[CellSession(j)]: !cellLive is an
	// unconditional rescueSkip, so on a renamed cell whose tmux convergence
	// has not landed the rescue does nothing at all — the hit is recorded and
	// never acted on. Every other current-name-only read left in this package
	// costs a card rendering wrong for one tick; this one costs a cell sitting
	// dead until something else notices, which is the failure mode the whole
	// rescue exists to end.
	//
	// It is resolved BEFORE the probe and answered here rather than left to
	// the decide chain, because "the cell is gone" is the one skip with no
	// end: nothing deletes a hit row, so a job whose cell was torn down but
	// which has not been archived stays in the tick's list and would pay the
	// full probe every 15 seconds for the rest of the daemon's life.
	_, cellLive := FindCellSession(j, live)
	if !cellLive {
		return
	}
	// Gate 3's caller-side twin (review finding R1): a non-rate_limit hit is
	// recorded-but-never-acted-on, nothing ever resolves it, and the cell can
	// stay live for the job's whole life — without this line such a job pays
	// the full probe every tick forever and the decide chain discards the
	// answer at its own gate 3 anyway. Same nothing, bought cheaper.
	if hit.Error != "rate_limit" {
		return
	}
	mode := RotationMode()
	// Only probe when this tick can actually USE the answer — see
	// rescueProbeDue. The probe is a snapshot read, a full job load, a tmux
	// round trip and one keychain fork per pool slot, and the two waits it
	// now skips are ten minutes and indefinite, i.e. ~40 discarded rounds
	// per backoff window at DaemonInterval.
	target := ""
	if rescueProbeDue(hit, mode, now) {
		target, _, _ = selectRescueBind(j, now)
	}
	action, reason := limitRescueDecide(hit, mode, cellLive, target != "", now)
	if action == rescueSkip {
		return
	}

	// The dedup key carries the HIT, not just the job and the action: nothing
	// clears d.Notified for this pass and the daemon runs for weeks, so a
	// slug+action key would announce a job's first rescue and then silently
	// rebind the cell for every later one. The park-manual notice is
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
	// The "from" side of what carl is told. The RECORD's own answer wins:
	// hit.SourceAccount is the account that was bound when this hit opened,
	// while the row says whatever it says now, and the two diverge precisely
	// when it matters — a rebind that landed but whose bookkeeping write was
	// lost would otherwise be reported as "rebound B → C" when the window
	// that failed was A. The row is the fallback for a hit recorded before
	// this field existed and for a cell that was unbound (SourceAccount is
	// empty for the main lane by design), and it is read HERE, before the
	// rotate arm below rewrites the binding.
	fromEmail := hit.SourceAccount
	if fromEmail == "" {
		fromEmail = cellAccountEmail(j)
	}
	if fromEmail == "" {
		fromEmail = "main"
	}

	switch action {
	case rescueRotate:
		to, outcome, err := rescueLimitedCell(j, now)
		hit = limitRescueApply(hit, action, outcome, to, now)
		saveErr := saveLimitRescueState(j, hit)
		switch {
		case outcome == "rebound":
			notifyOnce("kraken ⇄ "+j.Slug, fmt.Sprintf("usage limit — rebound %s → %s", fromEmail, to))
			dlogEvent("info", "limit-rescue-done", "%s: %s: rebound %s → %s", j.Slug, reason, fromEmail, to)
			if saveErr != nil {
				// The one lost write in this pass that is not self-correcting:
				// the binding HAS moved, and a record with no SwappedAt sends
				// the next tick around for a second rebind onto a third
				// account.
				dlogEvent("error", "limit-rescue-failed", "%s: rebound to %s but recording it failed — a later tick may rebind again: %v", j.Slug, to, saveErr)
			}
		case err != nil:
			dlogEvent("error", "limit-rescue-failed", "%s: rebind attempt %d failed (%s): %v", j.Slug, hit.Attempts, outcome, err)
		default:
			dlogEvent("info", "limit-rescue-skipped", "%s: rebind attempt %d did not rebind (%s)", j.Slug, hit.Attempts, outcome)
		}
		if saveErr != nil && outcome != "rebound" {
			dlogErr("%s: persist limit-hit record failed: %v", j.Slug, saveErr)
		}

	case rescueResume:
		// Which resume this is decides what zeus comes back to: a landed
		// rebind means the cell is now pointed at a different slot; a zero
		// SwappedAt on this arm can only be the reset re-arm, where the
		// account never moved.
		resumePrompt := limitRescueResumePrompt
		if hit.SwappedAt == 0 {
			resumePrompt = limitResetResumePrompt
		}
		resumed, err := resumeCellAgents(j, all, resumePrompt)
		if err != nil || resumed == 0 {
			// A hit resolved with zeus NOT resumed is a permanently stranded
			// cell: the binding moved, every claude in the cell is
			// still latched onto the old one, and a stalled cell makes no
			// further API calls — so it can never produce another hook, and
			// gate 2 would skip this hit forever.
			// Failing to reach zeus is not exotic (an empty discovery
			// snapshot, a zeus that died inside a surviving session, a
			// session whose only live panes are side threads, a pane that
			// stopped holding a claude between the snapshot and the kill), so
			// the round is counted rather than resolved and a later tick
			// tries again — but only up to limitRescueMaxAttempts of them,
			// after which the hit parks. An unbounded open rebound hit is
			// worse than a parked one: it re-arms the rotate gate every time
			// a late delivery supersedes it, and it writes this event every
			// 15s for as long as the session lives.
			hit = limitRescueApply(hit, action, "no-agents", "", now)
			if saveErr := saveLimitRescueState(j, hit); saveErr != nil {
				dlogErr("%s: persist limit-hit record failed: %v", j.Slug, saveErr)
			}
			if hit.HandledAt != 0 {
				// Which resume failed decides what carl is told: on the reset
				// path nothing ever moved, and a notice claiming a rebind
				// would report a change that never happened.
				if hit.SwappedAt != 0 {
					notifyOnce("kraken ⚠ "+j.Slug, "usage limit — cell rebound but its zeus could not be resumed")
					dlogEvent("info", "limit-rescue-skipped", "%s: parked after %d resume rounds reached no zeus pane — cell was rebound, not resumed", j.Slug, hit.Attempts)
				} else {
					notifyOnce("kraken ⚠ "+j.Slug, "usage limit — window reset but the cell's zeus could not be resumed")
					dlogEvent("info", "limit-rescue-skipped", "%s: parked after %d resume rounds reached no zeus pane — window had reset, cell was not resumed", j.Slug, hit.Attempts)
				}
				return
			}
			// err is nil on every COUNTED-OUT round (identity, cwd, an
			// unreadable thread set, a command that would not build, a
			// refused respawn) — each of those logged its own specific cause
			// where it fired. Rendering the nil here would print a bare
			// "(<nil>)" and read as a missing zeus, which is the one thing it
			// is not.
			if err != nil {
				dlogEvent("error", "limit-rescue-failed", "%s: %s: resume round %d found no zeus pane: %v", j.Slug, reason, hit.Attempts, err)
			} else {
				dlogEvent("error", "limit-rescue-failed", "%s: %s: resume round %d did not resume zeus — see the refusal logged above", j.Slug, reason, hit.Attempts)
			}
			return
		}
		hit = limitRescueApply(hit, action, "", "", now)
		if err := saveLimitRescueState(j, hit); err != nil {
			dlogErr("%s: persist limit-hit record failed: %v", j.Slug, err)
		}
		dlogEvent("info", "limit-rescue-done", "%s: %s (zeus resumed)", j.Slug, reason)

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
		// chance): two more rebind attempts are still ahead, so the reset
		// resume is conditional, not promised. The time carries a date
		// whenever the target is not today — a bare "10:20pm" for a
		// tomorrow target reads as tonight.
		parkNote := "hit a usage limit — " + reason
		if hit.ResetAt != 0 {
			reset := time.Unix(hit.ResetAt, 0).Local()
			form := "3:04pm"
			if reset.Format("2006-01-02") != now.Local().Format("2006-01-02") {
				form = "3:04pm on Jan 2"
			}
			parkNote += " — will keep retrying, and if no account appears, resume the cell after the window resets at " + reset.Format(form)
		}
		notifyOnce("kraken ⚠ "+j.Slug, parkNote)
		dlogEvent("info", "limit-rescue-skipped", "%s: %s (attempt %d)", j.Slug, reason, hit.Attempts)
	}
}
