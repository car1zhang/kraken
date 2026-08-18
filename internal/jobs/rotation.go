package jobs

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"kraken/internal/core"
	"kraken/internal/util"
)

// --- rotation.json: kraken's own auto/manual mode + notify-dedup state ---
//
// Separate from usage.json (the daemon-owned usage cache): this file is
// kraken's own decision-making state — what mode carl (or the web UI) chose,
// and the last rotation notification fired, so RotationGate doesn't spam a
// standing condition on every single cell spawn.

// NoticeState is the last rotation-related notification the gate fired,
// keyed so a standing condition (no candidate, swap failing) can be
// distinguished from a fresh one worth re-notifying about.
type NoticeState struct {
	Key string `json:"key,omitempty"`
	At  int64  `json:"at,omitempty"` // unix millis
}

// RotationState is read and written whole. That alone does NOT prevent a
// mode change and a dedup-notice (or gate-cooldown) update from clobbering
// each other — a naive read-mutate-write still loses a concurrent write
// that lands in the gap between the read and the write, and notifyDeduped
// used to have a gap exactly as wide as a blocking osascript call. The
// actual mitigation (see notifyDeduped, recordGateRun) is that every writer
// re-reads immediately before its own write and mutates only the field it
// owns — that narrows the race window down to the read-then-write itself,
// it doesn't eliminate it, but it's the practical option given kraken has
// no cross-process lock on rotation.json itself (rotation.lock guards the
// GATE's decision-making, not this file's writers in general).
type RotationState struct {
	Mode       string      `json:"mode"` // "auto" or "manual"
	SetBy      string      `json:"set_by,omitempty"`
	SetAt      int64       `json:"set_at,omitempty"` // unix millis
	LastNotice NoticeState `json:"last_notice"`
	// LastGateAt is the last time RotationGate actually ran its real check
	// (auto mode, not cooldown- or lock-skipped) — unix millis. See
	// rotationGateCooldown.
	LastGateAt int64 `json:"last_gate_at,omitempty"`
}

func rotationStatePath() string {
	return filepath.Join(util.Home(), ".kraken", "rotation.json")
}

// ReadRotationState is defensive like ReadUsageSnapshot: a missing or
// corrupt file reads as mode "auto" with zero everything else, never an
// error — RotationGate must always have a mode to act on. Mode normalizes
// case- and whitespace-insensitively (strings.EqualFold on the trimmed
// value) to "manual" or "auto": anything else (unknown, malformed) reads as
// "auto". This is deliberately fail-CLOSED on the manual side — a
// hand-edited "MANUAL" or " manual\n" must still keep the pin, since an
// exact-match comparison would silently normalize it to "auto" and let the
// gate resume auto-rotating despite carl (or a hand edit) having asked for
// manual. A bad write can still never wedge the gate permanently, since
// anything that ISN'T recognizably "manual" still falls through to "auto".
func ReadRotationState() RotationState {
	st, _ := readRotationState()
	return st
}

// readRotationState is ReadRotationState's corruption-aware sibling: the
// exact same defensive degrade-to-auto behavior, but it also reports
// whether a PRESENT file failed to parse (corrupt=true), distinct from a
// simply missing one (corrupt=false) — a missing file is the ordinary
// first-run case and not worth a word, but a present-and-corrupt file means
// recordGateRun is about to rewrite over whatever was actually there
// (possibly a hand-pinned manual) with the auto default, silently. Every
// other caller keeps going through the unexported-detail-free
// ReadRotationState above; only RotationGate needs to know the difference,
// to log it loudly instead of swallowing it.
func readRotationState() (st RotationState, corrupt bool) {
	data, err := os.ReadFile(rotationStatePath())
	if err != nil {
		return RotationState{Mode: "auto"}, false
	}
	if json.Unmarshal(data, &st) != nil {
		return RotationState{Mode: "auto"}, true
	}
	if strings.EqualFold(strings.TrimSpace(st.Mode), "manual") {
		st.Mode = "manual"
	} else {
		st.Mode = "auto"
	}
	return st, false
}

// WriteRotationState atomically persists st, 0600 like the usage snapshot —
// AtomicWrite itself writes 0644, so perms are tightened after the rename.
func WriteRotationState(st RotationState) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	path := rotationStatePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := util.AtomicWrite(path, string(data)); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

// RotationMode reports the current mode ("auto" or "manual"). Re-applies
// the same fail-closed strings.EqualFold(strings.TrimSpace(...), "manual")
// normalization ReadRotationState already does — belt and suspenders, so a
// future refactor that has this function stop routing through
// ReadRotationState can't silently reopen the fail-open bug (see
// ReadRotationState's doc comment) by reverting to an exact-match compare.
func RotationMode() string {
	mode := ReadRotationState().Mode
	if strings.EqualFold(strings.TrimSpace(mode), "manual") {
		return "manual"
	}
	return "auto"
}

// SetRotationMode persists a new mode, stamping who set it and when. Mode
// normalizes exactly like ReadRotationState's read side —
// strings.EqualFold(strings.TrimSpace(mode), "manual") decides manual,
// anything else becomes "auto" — so the two can never disagree about what a
// given stored value means, and the file always stores the canonical
// lowercase string rather than whatever casing/whitespace the caller passed
// in (Slice C feeds this from user input, e.g. the web UI or CLI).
func SetRotationMode(mode, setBy string) error {
	if strings.EqualFold(strings.TrimSpace(mode), "manual") {
		mode = "manual"
	} else {
		mode = "auto"
	}
	st := ReadRotationState()
	st.Mode = mode
	st.SetBy = setBy
	st.SetAt = time.Now().UnixMilli()
	return WriteRotationState(st)
}

// --- rotation gate: called at the top of every cell spawn ---

// rotationNoticeSuppress is how long notifyDeduped suppresses a repeat
// notification carrying the same key — a standing condition shouldn't
// re-notify on every single cell spawn.
const rotationNoticeSuppress = time.Hour

// rotationGateCooldown collapses a burst of same-tick RotationGate calls
// (a tick can spawn both of the daemon's own creations, and carl can submit
// several jobs in quick succession, each calling the gate once) into a single
// real usage check: only the first call within
// this window actually fetches usage and decides; every later call — even
// under a different trigger — is skipped via the persisted LastGateAt. The
// rotation.lock flock alone only bounds CONCURRENT calls (mutual
// exclusion), not a burst of sequential ones across a tick; this is what
// actually bounds FetchMainUsage's cost per tick.
const rotationGateCooldown = 60 * time.Second

// rotationFreshSnapshotWindow: how old a usage snapshot row is allowed to be
// (by its OWN FetchedAt, not the snapshot's) before it's refused as a
// stand-in for a failed live fetch — for both the main row
// (freshOrRecentMainUsage) and candidate pool rows (RotationGate). Rotating
// (or not) on genuinely stale data risks the wrong call either way, so this
// is deliberately tight.
const rotationFreshSnapshotWindow = 15 * time.Minute

func rotationLockPath() string {
	return filepath.Join(util.Home(), ".kraken", "rotation.lock")
}

// --- swappable seams, mirroring core's securityFind pattern, so the two
// account-moving paths in this package are testable without network,
// keychain, tmux or osascript.
//
// They serve BOTH: RotationGate below (fetch, select, swap, notify, the
// post-swap usage kick) and the mid-run limit rescue in limitrescue.go,
// which reuses selectTarget/swapMain/runUsagePass and adds the two of its
// own at the end — the zeus respawn and the live main identity. One block
// rather than a per-file one, because the rescue and the gate must agree
// about what "swap main" and "pick a target" mean; two blocks would be two
// places to swap in a test and one of them would get missed. ---
var (
	fetchMainUsage = core.FetchMainUsage
	selectTarget   = core.SelectRotationTarget
	swapMain       = core.SwapMainTo
	notifyFn       = notify
	// respawnPeerPane is the limit rescue's resume half — kill the claude in
	// ZEUS' pane (the rescue's only target) and bring its session back on
	// the current account. Its second argument is the CALLER's full launch
	// command: core builds none, since what a resumed zeus must be launched
	// with is this package's knowledge. A seam for the same reason swapMain
	// is one — the rescue's resume path is decision logic worth exercising
	// without a tmux server under it.
	respawnPeerPane = core.RespawnPeerPane
	// mainIdentityEmail reports who main is RIGHT NOW (~/.claude.json),
	// which the rescue prefers over the polled snapshot's main row — the
	// snapshot lags a swap the rescue itself just made.
	mainIdentityEmail = core.MainIdentityEmail
	// runUsagePass defaults to the real UsagePass; swapped out in tests so a
	// successful-rotation test doesn't kick off a real network usage pass in
	// the background.
	runUsagePass = UsagePass
	// standingRespawn is the standing-agent rescue's resume half —
	// respawnPeerPane's sibling for the agents that belong to no job (kronos,
	// iris): kill the agent's whole tmux session and bring it back through
	// its own Ensure*. The body lives in standingrescue.go with the rest of
	// the standing machinery; the seam lives HERE for this block's stated
	// reason — one place to swap in a test.
	standingRespawn = standingKillAndRespawn
)

// acquireRotationLock takes the cross-process single-flight guard at
// ~/.kraken/rotation.lock via a kernel flock (LOCK_EX|LOCK_NB) — not the
// former stat/remove/O_EXCL "reclaim if stale" dance, which admitted two
// concurrent holders under the right interleaving (worst case: both
// mid-swap over the SAME outgoing account's creds, destroying them). Three
// outcomes: acquired (release non-nil, ok=true, err=nil); held by someone
// else (nil, false, nil — not an error, just contention); or couldn't even
// open/create the lock file at all — e.g. ~/.kraken exists as a plain file,
// or a permissions problem (nil, false, err). The two false cases are kept
// distinct so RotationGate can log "already in flight" separately from
// "lock unavailable: <err>" — one blended message sends users hunting a
// phantom rotation instead of a filesystem problem.
//
// The file's content (pid + timestamp) is human-facing only now — nothing
// reads it back to decide staleness. A process that dies while holding the
// fd has its flock released automatically by the kernel on exit, so the old
// "reclaim a lock file older than N minutes" logic is gone entirely along
// with the double-holder risk it existed to paper over. The flip side: a
// HUNG (not dead) holder now blocks every future gate indefinitely until it
// exits or is killed — accepted, since RotationGate never blocks or fails
// the spawn it guards; a blocked gate only means that spawn's rotation
// check is skipped, not that the spawn itself fails.
// AcquireRotationLock exports acquireRotationLock for manual rotation call
// sites outside this package — `k account <slug>`'s CLI switch
// (cmd_account.go) and the web API's POST /api/account/switch handler — that
// must take the same cross-process guard RotationGate itself takes before
// swapping main, so a manual switch can never race the daemon's own gate (or
// each other) mid-swap. See acquireRotationLock's doc comment for the flock
// semantics and the three possible outcomes.
func AcquireRotationLock() (release func(), ok bool, err error) {
	return acquireRotationLock()
}

func acquireRotationLock() (release func(), ok bool, err error) {
	path := rotationLockPath()
	if mkErr := os.MkdirAll(filepath.Dir(path), 0o755); mkErr != nil {
		return nil, false, mkErr
	}
	f, openErr := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if openErr != nil {
		return nil, false, openErr
	}
	if flockErr := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); flockErr != nil {
		_ = f.Close()
		return nil, false, nil // held elsewhere — not an error
	}
	_ = f.Truncate(0)
	_, _ = f.WriteString(fmt.Sprintf("pid=%d at=%d", os.Getpid(), time.Now().UnixMilli()))
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, true, nil
}

// freshOrRecentMainUsage resolves the main usage row RotationGate decides
// against: a live fetch when it actually has usable windows, else the usage
// snapshot's main row but ONLY when that ROW'S OWN FetchedAt is recent.
// Keying freshness off the snapshot's own FetchedAt (rather than the row's)
// was a bug: mergeUsage re-stamps the whole snapshot's FetchedAt fresh on
// EVERY pass, even a pass that's carrying a failed main row's windows
// forward unchanged (Stale: true, original FetchedAt preserved) — so a
// 48-hour-old session=95% row could pass as "fresh" throughout an outage,
// repeatedly triggering (and re-triggering, ping-ponging) a swap onto data
// that never actually updated. The row's own FetchedAt is the only
// trustworthy freshness signal.
func freshOrRecentMainUsage(now time.Time) (core.AccountUsage, bool) {
	row := fetchMainUsage()
	if row.Session != nil || row.Weekly != nil {
		return row, true
	}
	snap, ok := ReadUsageSnapshot()
	if !ok {
		return core.AccountUsage{}, false
	}
	for _, a := range snap.Accounts {
		if a.Slug != "main" {
			continue
		}
		if !rowFresh(a, now) {
			return core.AccountUsage{}, false
		}
		return a, true
	}
	return core.AccountUsage{}, false
}

// rowFresh reports whether a usage-snapshot row's OWN FetchedAt (not the
// snapshot's) is recent enough to trust for a rotation decision — the same
// freshness bound freshOrRecentMainUsage's main-row fallback and
// RotationGate's candidate-pool filter both need, pulled out once so the
// two can't quietly drift apart. See freshOrRecentMainUsage's doc comment
// for why the row's own timestamp is the only trustworthy signal.
func rowFresh(a core.AccountUsage, now time.Time) bool {
	return a.FetchedAt != 0 && now.UnixMilli()-a.FetchedAt < rotationFreshSnapshotWindow.Milliseconds()
}

// notifyDeduped fires a notification unless the last one recorded in
// rotation.json carries the same key within rotationNoticeSuppress.
func notifyDeduped(key, title, msg string) {
	st := ReadRotationState()
	now := time.Now().UnixMilli()
	// age >= 0 guards against a clock rollback leaving LastNotice.At in the
	// future: without it, `now - At` goes negative and is always < the
	// suppress window, permanently suppressing the key.
	if age := now - st.LastNotice.At; st.LastNotice.Key == key && age >= 0 && age < rotationNoticeSuppress.Milliseconds() {
		return
	}
	notifyFn(title, msg)
	// Re-read AFTER the (slow, osascript-shelling) notify call rather than
	// reusing the pre-notify `st` above: osascript can block long enough for
	// a concurrent SetRotationMode call from a mode-only path (`k account
	// auto`, POST /api/account/mode — neither takes rotation.lock, unlike the
	// manual-switch paths, which have taken it since K2) to land in the gap,
	// and writing back the stale pre-notify struct would silently clobber
	// that mode flip. Only LastNotice is mutated onto the freshly re-read
	// state.
	st = ReadRotationState()
	st.LastNotice = NoticeState{Key: key, At: now}
	if err := WriteRotationState(st); err != nil {
		dlogEvent("error", "rotation-failed", "notifyDeduped: persist last_notice failed: %v", err)
	}
}

// recordGateRun stamps nowMillis as RotationGate's last real (auto-mode,
// non-cooldown-skipped) run — see rotationGateCooldown. Re-reads state
// immediately before writing and mutates only LastGateAt, for the same
// reason notifyDeduped does: a concurrent SetRotationMode call from a
// mode-only path (`k account auto`, POST /api/account/mode) isn't guarded by
// rotation.lock — unlike the manual-switch paths (`k account <slug>`, POST
// /api/account/switch), which have taken it since K2 — so the read
// RotationGate already did to decide to proceed can be stale by the time
// this write actually happens.
func recordGateRun(nowMillis int64) {
	st := ReadRotationState()
	st.LastGateAt = nowMillis
	if err := WriteRotationState(st); err != nil {
		dlogEvent("error", "rotation-failed", "rotation gate: persist last_gate_at failed: %v", err)
	}
}

// RotationGate is called at the top of every cell spawn (StartCell,
// ReactivateJob) to give kraken a chance to move the main login off a
// nearly-exhausted account before the spawn's own usage lands on it. It
// NEVER blocks or fails the spawn — there is no error return, every
// decision point either proceeds or degrades to a logged skip, and a panic
// anywhere in the decision path is recovered rather than propagated (the
// gate's contract is "never blocks or fails a spawn", and it runs inline in
// both the daemon's spawn path and a human-triggered CLI action). A
// gate-level cooldown (rotationGateCooldown) collapses a tick's worth of
// per-spawn calls into a single real usage check, capping FetchMainUsage's
// cost per tick rather than per spawn. trigger names the call site ("start
// <slug>", "reactivate <slug>") for the event log.
func RotationGate(trigger string) {
	defer func() {
		if r := recover(); r != nil {
			dlogEvent("error", "rotation-failed", "rotation gate (%s): panic: %v\n%s", trigger, r, debug.Stack())
		}
	}()

	release, ok, err := acquireRotationLock()
	if !ok {
		if err != nil {
			dlogEvent("error", "rotation-skipped", "rotation gate (%s): lock unavailable: %v", trigger, err)
		} else {
			dlogEvent("info", "rotation-skipped", "rotation gate (%s): already in flight — skipped", trigger)
		}
		return
	}
	defer release()

	now := time.Now()
	st, corrupt := readRotationState()
	if corrupt {
		// Fail-open stays (a corrupt file must never block a spawn), but this
		// is exactly the moment a hand-pinned manual could get silently
		// overwritten by recordGateRun's rewrite below — say so loudly, once
		// per gate run, rather than let it happen quietly.
		dlogEvent("error", "rotation-failed", "rotation gate (%s): rotation.json unreadable — treating as auto", trigger)
	}
	if st.LastGateAt != 0 {
		if age := now.UnixMilli() - st.LastGateAt; age >= 0 && age < rotationGateCooldown.Milliseconds() {
			return // collapsed into an earlier real check this window — silent
		}
	}
	if st.Mode == "manual" {
		// A standing condition (carl pinned manual), not worth a log line
		// on every single spawn — same rationale as the healthy-main no-op
		// path below staying silent.
		return
	}
	recordGateRun(now.UnixMilli())

	main, fresh := freshOrRecentMainUsage(now)
	if !fresh {
		dlogEvent("info", "rotation-skipped", "rotation gate (%s): no fresh usage data — skipped", trigger)
		return
	}

	needs, reason := core.NeedsRotation(main)
	if !needs {
		return // common path on every spawn — nothing worth logging
	}

	snap, _ := ReadUsageSnapshot()
	var pool []core.AccountUsage
	staleSkipped := 0
	for _, a := range snap.Accounts {
		if a.Slug == "main" {
			continue
		}
		// Row-level freshness bound, same rationale as
		// freshOrRecentMainUsage: mergeUsage can carry a candidate's windows
		// forward unchanged (Stale: true) across many passes, so days-old
		// slot data must never be allowed to pick (or rule out) a rotation
		// target.
		if !rowFresh(a, now) {
			staleSkipped++
			continue
		}
		pool = append(pool, a)
	}

	var target, excludeReason string
	if len(pool) == 0 && staleSkipped > 0 {
		// Every non-main row that exists got filtered out as stale — calling
		// selectTarget with an empty slice here would read (and log) exactly
		// like SelectRotationTarget's own "no pool accounts" case, which
		// claims there's no candidate DATA at all. That's misleading: there
		// IS pool data, it's just too old (>15m) to trust for a rotation
		// decision. Skip the selector call and say the real story directly.
		excludeReason = fmt.Sprintf("%d pool row(s) skipped as stale (>15m) — no fresh candidate data", staleSkipped)
	} else {
		target, excludeReason = selectTarget(pool, main.Email, now.UnixMilli())
		if target == "" && staleSkipped > 0 {
			excludeReason = fmt.Sprintf("%s; %d skipped as stale", excludeReason, staleSkipped)
		}
	}
	if target == "" {
		msg := fmt.Sprintf("%s: %s — no eligible target (%s)", trigger, reason, excludeReason)
		notifyDeduped("rotation:no-target", "kraken ⚠ account rotation", msg)
		dlogEvent("info", "rotation-skipped", "rotation gate (%s): needs rotation (%s) but no target: %s", trigger, reason, excludeReason)
		return
	}

	from, to, swapErr := swapMain(target)
	if swapErr != nil {
		// ":journal" vs ":target" so a wedged-journal alert (which needs a
		// human to run `ccp status` / clear the journal — see
		// journalRefusalError in core/rotation.go) is never suppressed by an
		// unrelated, likely-transient swap failure sharing the generic
		// dedup key. The per-occurrence event log below still records
		// everything regardless of class.
		cause := "target"
		if errors.Is(swapErr, core.ErrRotationInFlight) {
			cause = "journal"
		}
		msg := fmt.Sprintf("%s: %v", trigger, swapErr)
		notifyDeduped("rotation:swap-failed:"+cause, "kraken ⚠ account rotation", msg)
		dlogEvent("error", "rotation-failed", "rotation gate (%s): swap to %s failed: %v", trigger, target, swapErr)
		return
	}

	// Stamped inside the lock, immediately after the swap committed and before
	// the deferred release: this gate moves the ONE login every claude on the
	// machine shares, so a cell or a standing agent already parked on a usage
	// limit has just had its repair performed for it (limitrescue.go's
	// adoptLandedSwap is the reader). Without the stamp that owner's next tick
	// still reads SwappedAt == 0 and swaps a SECOND account for a limit this
	// swap already cleared. Logged and carried on when it fails — the account
	// really did move, and there is nothing here worth failing a completed
	// swap over. The stamp's clock is a FRESH read rather than this gate's own
	// `now`, which was captured before the usage fetch and the keychain write:
	// adoptLandedSwap tests `swapAt >= hit.At`, and the gate rotates exactly
	// when running claudes are 429-ing, so a stamp that predates the commit by
	// those seconds would refuse adoption to the very herd of hits the swap
	// just repaired.
	if stampErr := recordMainSwap(to, time.Now()); stampErr != nil {
		dlogEvent("error", "rotation-failed", "rotation gate (%s): swapped to %s but recording the main-swap watermark failed: %v", trigger, to, stampErr)
	}

	notifyFn("kraken ⇄ rotated main", fmt.Sprintf("%s → %s (%s)", from, to, reason))
	dlogEvent("info", "rotation-done", "rotation gate (%s): rotated %s → %s (%s)", trigger, from, to, reason)

	// Kick a usage pass so the board reflects the new main promptly, single-
	// flighted against the daemon's own periodic pass with the same atomic.
	// runUsagePass is captured into a local before the goroutine starts
	// (rather than read from inside it) so a test that swaps the package var
	// back right after this call returns can never race this read.
	if pass := runUsagePass; usagePassRunning.CompareAndSwap(false, true) {
		go func() {
			defer usagePassRunning.Store(false)
			if err := pass(); err != nil {
				dlog("usage pass after rotation failed: %v", err)
			}
		}()
	}
}
