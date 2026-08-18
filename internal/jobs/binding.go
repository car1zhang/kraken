package jobs

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"kraken/internal/core"
)

// binding.go — per-cell account binding: which pool account a job's cell
// bills, decided once at spawn and re-decided only by the limit rescue.
//
// It replaces the retired RotationGate, and the difference is the whole
// point of the feature. The gate moved the MACHINE's login — one keychain
// item every claude on this Mac resolves — off a nearly-exhausted account
// before a spawn landed on it, which displaced carl's own interactive
// sessions and every other live cell along with the one being started. A
// bind moves nothing: it stamps a pool slug on the job row, and
// spawnCommand puts `CLAUDE_SECURESTORAGE_CONFIG_DIR=<slot dir>` in front
// of the cell's own launcher, so zeus, every teammate tmux spawns under it
// and every side thread resolve that slot's own keychain item and nobody
// else's.
//
// Two consequences shape the code below. Binding is per-JOB state, so it is
// stored on the job row rather than in rotation.json, and the ranking is
// over how many LIVE cells each slot already carries (boundCounts) rather
// than over usage alone — spreading cells across the pool is the goal, and
// two cells on one slot is what the count exists to avoid. And a bind is
// never load-bearing for a spawn: every failure here (dry pool, unreadable
// snapshot, no store, a panic) leaves the job unbound, which is exactly
// today's behaviour — no env var, the machine's main login, one lane.

// bindMu serializes select-then-stamp. The decision reads every job's
// current binding (boundCounts) and then writes one, so two submits landing
// together would otherwise both read the same counts and both pick the same
// emptiest slot — the one outcome the count exists to prevent. It is also
// what the limit rescue's rebind takes instead of the rotation flock: a
// rebind changes no machine-wide state, so the cross-process lock it used to
// need is gone, but it is still a select-then-stamp against the same counts.
var bindMu sync.Mutex

// slotItemExists is the per-bind usability probe, seamed here rather than
// called through core directly so no test in this package ever shells out to
// security(1). Fail-closed on every axis: an unprobeable slot is an
// ineligible slot.
var slotItemExists = slotUsable

// slotUsable answers whether a pool slot can actually carry a cell, which
// takes TWO questions rather than one.
//
// core.SlotItemExists asks only whether a keychain item exists under the
// hash of the slot dir — and a keychain item OUTLIVES its directory, since
// nothing removes the item when a slot dir is deleted. So a slot removed
// between two usage passes still has a snapshot row (the row is one poll
// behind) and still probes TRUE, and the cell it binds comes up with
// CLAUDE_SECURESTORAGE_CONFIG_DIR pointing at a directory that is not there.
// The phase-0 spike measured that exact input — the var set to an empty or
// absent dir — as `Not logged in`: a pane that is dead on arrival while the
// board reads `working`, which is the worst outcome this feature can
// produce and precisely the one this guard exists to prevent.
//
// So the directory is stat'd first, and it must be a directory: a plain file
// at that path is no more usable than nothing. The two failures share one
// exclusion reason upstream ("no keychain item") because the answer callers
// act on is the same — this slot cannot be bound — and splitting the wording
// would put two spellings of one refusal in the notification.
func slotUsable(dir string) bool {
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		return false
	}
	return core.SlotItemExists(dir)
}

// bindNoticeKey is notifyDeduped's key for "the pool had nothing to give
// this cell". It carries no slug on purpose: a dry pool is a standing
// machine-level condition, and one notice an hour about it is the signal —
// one per cell spawn is noise.
const bindNoticeKey = "bind:no-target"

// slotDir is the ONE derivation of a pool slot's directory from its slug.
// Both readers — the keychain probe's argument and the env value the spawn
// command carries — go through it, so the directory a slot is PROBED at can
// never differ from the one a cell is pointed at.
func slotDir(slug string) string { return filepath.Join(core.PoolDir(), slug) }

// resolvedMainEmail is who main belongs to, for the ONE purpose every caller
// here has: excluding main's own save-back mirror slot from the pool.
//
// The live read (~/.claude.json, through the mainIdentityEmail seam) is
// preferred because it is current — a manual `k account <slug>` switch is
// invisible to the polled snapshot until the next usage pass lands. But an
// unreadable, unparseable or oauthAccount-less ~/.claude.json returns "", and
// an empty needle is not a weaker exclusion, it is NO exclusion: decideBind
// guards its mirror filter on a non-empty string and core.SelectBindTarget
// has no fail-closed arm of its own, so every cell would become eligible to
// bind onto carl's interactive account under a different slug — the exact
// outcome this feature exists to prevent, with a board chip claiming a
// private lane.
//
// The snapshot's own `main` row is the fallback, and it is a restoration
// rather than an invention: the retired selectRescueTarget carried both
// needles for this reason. Refusing to bind at all when neither is readable
// would be worse than binding (an unbound cell is today's behaviour, a
// refused spawn is not), so "" is still a possible answer — but it is now
// the answer only when BOTH sources are silent.
func resolvedMainEmail(snap core.UsageSnapshot) string {
	if email := mainIdentityEmail(); email != "" {
		return email
	}
	for _, a := range snap.Accounts {
		if a.Slug == "main" {
			return a.Email
		}
	}
	return ""
}

// decideBind picks the pool slot a cell should bind to, pure over the five
// facts it is handed. Selection proper — the eligibility gates, the
// stickiness and the ranking — is core.SelectBindTarget's; what lives here
// is the two exclusions that need kraken's own knowledge of the pool, plus
// the manual pin.
//
// The manual pin returns before anything else is read, and that is Q5's
// answer rather than an optimization: pinning the account manually means
// kraken takes no account decisions at all, so a pinned machine spawns
// unbound cells on whatever login carl chose, exactly as it did before this
// feature existed. Reading the snapshot or probing the keychain first would
// be work whose result can never be used.
//
// The main-email exclusion is the save-back mirror: SwapMainTo writes the
// outgoing main account into a pool slot of its own, so the pool routinely
// carries a slot that IS main. Binding a cell to it would put the cell back
// on carl's interactive account under a different name — the one thing this
// feature exists to stop. It is applied here AND passed to SelectBindTarget
// as excludeEmails, which is belt-and-braces on purpose: the selector is
// where the rule is specified, and doing it here as well is what keeps the
// keychain probe below off a slot that was never a candidate.
//
// The item-exists guard is Q1's per-bind safety net. The env var only
// redirects Claude Code at a config DIRECTORY; the credential itself lives
// in the keychain under a name derived from that directory, so a slot dir
// with no matching keychain item produces a cell that comes up "Not logged
// in" — measured in the phase-0 spike. The guard is two-part — the injected
// func is slotUsable, which also stats the DIRECTORY, since a keychain item
// outlives a deleted slot dir (see slotUsable for both halves). An
// unprobeable slot is therefore excluded rather than risked, and it is
// excluded with a reason so a pool that has quietly lost its items says so
// instead of reading as empty.
//
// An empty result is never an error: "" means bind nothing, which is the
// main lane and today's behaviour.
func decideBind(snap core.UsageSnapshot, counts map[string]int, mode, mainEmail, prevSlug string, itemExists func(slotDir string) bool, now time.Time) (slug, email, reason string) {
	if mode == "manual" {
		return "", "", "rotation pinned manual"
	}
	mainLower := strings.ToLower(strings.TrimSpace(mainEmail))
	var candidates []core.AccountUsage
	var excluded []string
	for _, a := range snap.Accounts {
		if a.Slug == "main" {
			continue
		}
		if mainLower != "" && strings.ToLower(a.Email) == mainLower {
			continue // main's own save-back mirror, not a candidate
		}
		if itemExists != nil && !itemExists(slotDir(a.Slug)) {
			excluded = append(excluded, a.Slug+": no keychain item")
			continue
		}
		candidates = append(candidates, a)
	}
	if len(candidates) == 0 && len(excluded) > 0 {
		// Every row that existed was skipped HERE, so the selector has
		// nothing to add and would only say "no pool accounts" — which is
		// false and actively misleading: there IS pool data, its keychain
		// items are missing. Skip the call and let the per-slug reasons be
		// the whole answer.
		return "", "", strings.Join(excluded, "; ")
	}
	var exclude []string
	if mainEmail != "" {
		exclude = append(exclude, mainEmail)
	}
	slug, email, reason = core.SelectBindTarget(candidates, exclude, counts, prevSlug, now.UnixMilli())
	if slug != "" {
		return slug, email, reason
	}
	// No pick: report every exclusion, this function's and the selector's
	// alike, in one sentence. A caller that only ever saw the selector's half
	// would describe a pool with some keychain items missing as a pool that
	// simply had nothing eligible.
	if reason != "" {
		excluded = append(excluded, reason)
	}
	if len(excluded) == 0 {
		return "", "", "no pool accounts"
	}
	return "", "", strings.Join(excluded, "; ")
}

// bindGraceWindow is how long a freshly stamped binding keeps counting
// toward its slot's load even though no cell is live under it yet.
//
// It exists because the bind and the spawn are not one step: bindAccount
// stamps the row at the top of StartCell, and the tmux session it will be
// counted by is only created forty-odd lines later, after `git worktree add`
// and the hook/skill install. Without a grace window two submits landing
// together serialize on bindMu and STILL pick the same slot — the second one
// reads the first's freshly written row, asks whether its cell is live,
// gets "no", and counts it zero. The mutex is satisfied and the spread it
// exists to produce is defeated.
//
// The window only has to cover that gap plus the aftermath of a spawn that
// fails inside it: `SetJobAccount` is never rolled back, so a StartCell that
// dies at `git worktree add` leaves the job draft with a binding no cell will
// ever claim, and nothing clears it. Ten minutes is far longer than any
// worktree-add-plus-tmux sequence and short enough that a slot stranded by
// such a failure is available again within one look at the board.
const bindGraceWindow = 10 * time.Minute

// boundCounts is the ranking's first key: how many cells each pool slot is
// already carrying. Unbound jobs contribute nothing — they are on the main
// lane, which this feature never counts against a slot.
//
// counts is a predicate rather than a call to LiveSessions here because
// liveness costs a tmux round trip: one caller resolves it once and hands
// the same answer to every job, and a test can decide it outright. What the
// predicate answers is "does this job's binding still load its slot", which
// is liveOrFreshlyBound in production — live cell OR a stamp inside
// bindGraceWindow — and not liveness alone.
func boundCounts(jobs []*Job, isLive func(*Job) bool) map[string]int {
	counts := map[string]int{}
	for _, j := range jobs {
		if j == nil || j.AccountSlug == "" {
			continue
		}
		if isLive != nil && !isLive(j) {
			continue
		}
		counts[j.AccountSlug]++
	}
	return counts
}

// liveOrFreshlyBound is the predicate every production caller of boundCounts
// hands it: a job loads its slot while its cell is live, and ALSO for
// bindGraceWindow after the stamp itself, which is what makes a bind taken
// seconds ago visible to the next one before its tmux session exists.
//
// It is built here, from a live-session map the caller already resolved, so
// boundCounts stays pure over its two arguments and a test can supply any
// predicate it likes without a tmux server.
//
// A stamp in the FUTURE — a clock stepped backwards between the bind and
// this read — reads as fresh and so counts. That is deliberate: the two
// failure directions are not symmetric. Under-counting puts two cells on one
// account, which is the whole defect this window exists to close;
// over-counting only makes the binder prefer a different slot, and it
// resolves itself the moment the cell comes up (live wins outright) or the
// clock catches back up.
func liveOrFreshlyBound(live map[string]bool, now time.Time) func(*Job) bool {
	cutoff := bindGraceWindow.Milliseconds()
	return func(j *Job) bool {
		if j == nil {
			return false
		}
		if _, ok := FindCellSession(j, live); ok {
			return true
		}
		return j.AccountAt != 0 && now.UnixMilli()-j.AccountAt < cutoff
	}
}

// bindCountPredicate resolves the live-session snapshot liveOrFreshlyBound
// needs and wraps it, and it exists because of what happens when tmux cannot
// be asked at all.
//
// LiveSessions folds a tmux failure into an EMPTY map, which is the same
// value as "no sessions exist" — listSessionTimes is the sibling that keeps
// the two apart precisely because one is evidence and the other is the
// absence of it. Read through the fold, an unreachable tmux makes every
// long-running binding invisible, boundCounts answers zero for every slot,
// and the binder stacks new cells onto whichever slot looks healthiest: the
// guarantee this feature exists to provide, silently gone for that spawn,
// with nothing logged.
//
// So the error is surfaced and answered by counting EVERY bound job as
// loading its slot. That is the same asymmetry liveOrFreshlyBound's own doc
// argues for one level down, extended to a coarser input: over-counting only
// pushes the pick toward a different slot, while under-counting puts two
// cells on one account. Failing toward "skip the bind" is explicitly NOT the
// answer — an unbound cell runs on main, which is worse than either.
func bindCountPredicate(now time.Time) func(*Job) bool {
	times, err := listSessionTimes()
	if err != nil {
		// Reuses the bind-skipped event kind rather than minting a third the
		// contract does not name; the bind itself still proceeds, on the
		// coarser count. A no-server exit lands here too — deliberately: in
		// that state nothing is live, so over-counting only changes which
		// equally-idle slot wins, the safe direction by the doc above.
		dlogEvent("info", "bind-skipped", "bind: tmux could not be asked which cells are live (%v) — counting every bound job against its slot", err)
		return func(j *Job) bool { return j != nil }
	}
	live := make(map[string]bool, len(times))
	for name := range times {
		live[name] = true
	}
	return liveOrFreshlyBound(live, now)
}

// bindGate is the package-level indirection to bindAccount, and it is what
// TestMain (testmain_test.go) swaps to a no-op for the whole test binary —
// the seam the retired rotationGate used to be. StartCell/ReactivateJob must
// never read the real usage snapshot or probe the real keychain from a test
// that happens to spawn a cell; a test that wants to assert on binding
// re-swaps this locally.
var bindGate = bindAccount

// bindAccount is decideBind's impure wrapper: it gathers the four facts the
// decision needs, takes the pick, and stamps it on the job row AND on the
// in-memory *Job the caller is about to spawn from. That second write is not
// a convenience — spawnCommand reads j.AccountSlug a few lines later, so a
// stamp that only reached the database would produce a bound row and an
// unbound cell.
//
// It never blocks or fails a spawn, which is the contract RotationGate had
// and the reason a panic anywhere in here is recovered rather than
// propagated: this runs inline inside StartCell, which runs inside carl's
// own submit request. Every degradation lands on the same outcome — the job
// stays unbound and the cell comes up on the machine's main login, which is
// precisely how every cell ran before this feature.
//
// trigger names the call site ("start <slug>", "reactivate <slug>") for the
// event log, the same shape the retired gate used.
func bindAccount(j *Job, trigger string) {
	defer func() {
		if r := recover(); r != nil {
			dlogEvent("error", "bind-skipped", "bind (%s): panic: %v\n%s", trigger, r, debug.Stack())
		}
	}()
	if j == nil {
		return
	}
	bindMu.Lock()
	defer bindMu.Unlock()

	now := time.Now()
	mode := RotationMode()
	// A missing snapshot is not distinguished from an empty one here: both
	// mean "no candidate rows", decideBind says so in its reason, and the
	// outcome — an unbound cell — is identical.
	snap, _ := ReadUsageSnapshot()
	// LoadJobs happens under bindMu and AFTER any earlier submit's own
	// SetJobAccount committed, so this read sees a sibling bind taken
	// moments ago — and liveOrFreshlyBound is what stops that sibling from
	// reading as zero just because its tmux session is still forty lines
	// away.
	counts := boundCounts(LoadJobs(), bindCountPredicate(now))
	// prevSlug is this job's OWN previous binding (Q6): a reactivated cell
	// goes back to the account that built its prompt cache when that account
	// is still eligible, which is the whole reason stickiness outranks the
	// count.
	slug, email, reason := decideBind(snap, counts, mode, resolvedMainEmail(snap), j.AccountSlug, slotItemExists, now)
	if slug == "" {
		if mode != "manual" {
			// The manual pin is carl's own standing choice and needs no
			// notice; a pool that has nothing to give is a condition he can
			// act on, so it gets one — deduped on a single key for an hour,
			// the same machinery and window rotation's own no-target notice
			// used.
			notifyDeduped(bindNoticeKey, "kraken ⚠ account binding", fmt.Sprintf("%s: no account to bind (%s)", trigger, reason))
		}
		dlogEvent("info", "bind-skipped", "bind (%s): %s", trigger, reason)
		return
	}
	st := ActiveStore()
	if st == nil {
		dlogEvent("error", "bind-skipped", "bind (%s): picked %s but no store is wired — the cell runs unbound", trigger, slug)
		return
	}
	at := now.UnixMilli()
	if err := st.SetJobAccount(j.ID, slug, email, at); err != nil {
		dlogEvent("error", "bind-skipped", "bind (%s): stamping %s failed — the cell runs unbound: %v", trigger, slug, err)
		return
	}
	j.AccountSlug, j.AccountEmail, j.AccountAt = slug, email, at
	dlogEvent("info", "bind-done", "bind (%s): bound to %s (%s)", trigger, slug, email)
}

// accountEnvPrefix is the WHOLE delivery mechanism for a binding: the
// environment assignment that rides in front of a cell claude's launcher in
// the command string tmux runs. Empty for an unbound job, which is what
// keeps an unbound cell's command byte-identical to what it was before this
// feature.
//
// It has to be part of the COMMAND STRING rather than the exec.Cmd
// environment, and that is measured rather than assumed: `tmux
// respawn-pane` does not build the new pane's environment from the issuing
// client's (0 of 20 env lines survived the trip in the phase-0 spike), so an
// env set on the process issuing the tmux command reaches a new-session pane
// and not a respawned one — and the limit rescue's respawn is exactly the
// spawn that must carry it.
//
// The value is single-quoted unconditionally rather than through
// core.ShellQuote, which leaves a shell-safe path bare: a quoted value is
// what the contract fixes, and it costs nothing. Embedded single quotes are
// escaped the same way ShellQuote escapes them, so a pool slug carrying one
// cannot end the quote early.
func accountEnvPrefix(j *Job) string {
	if j == nil || j.AccountSlug == "" {
		return ""
	}
	dir := slotDir(j.AccountSlug)
	return "CLAUDE_SECURESTORAGE_CONFIG_DIR='" + strings.ReplaceAll(dir, "'", `'\''`) + "' "
}
