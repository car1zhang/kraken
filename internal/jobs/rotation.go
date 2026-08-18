package jobs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
// and the last account-related notification fired, so a standing condition
// isn't announced again on every single cell spawn.
//
// The MODE is now read by two things and written by neither of them: the
// per-cell binder (binding.go), where "manual" means kraken takes no account
// decision at all, and the limit rescue's park-manual gate. Nothing in this
// package swaps the machine's main login any more — that retired with
// RotationGate (Q2) and is reachable only from `k account <slug>` and POST
// /api/account/switch, both of which call core.SwapMainTo directly and take
// AcquireRotationLock below.

// NoticeState is the last account-related notification fired, keyed so a
// standing condition (a dry pool, a failing swap) can be distinguished from
// a fresh one worth re-notifying about.
type NoticeState struct {
	Key string `json:"key,omitempty"`
	At  int64  `json:"at,omitempty"` // unix millis
}

// RotationState is read and written whole. That alone does NOT prevent a
// mode change and a dedup-notice update from clobbering each other — a naive
// read-mutate-write still loses a concurrent write that lands in the gap
// between the read and the write, and notifyDeduped used to have a gap
// exactly as wide as a blocking osascript call. The actual mitigation (see
// notifyDeduped) is that every writer re-reads immediately before its own
// write and mutates only the field it owns — that narrows the race window
// down to the read-then-write itself, it doesn't eliminate it, but it's the
// practical option given kraken has no cross-process lock on rotation.json
// itself (rotation.lock guards a manual swap, not this file's writers in
// general).
type RotationState struct {
	Mode       string      `json:"mode"` // "auto" or "manual"
	SetBy      string      `json:"set_by,omitempty"`
	SetAt      int64       `json:"set_at,omitempty"` // unix millis
	LastNotice NoticeState `json:"last_notice"`
	// LastGateAt was the retired RotationGate's per-tick cooldown stamp
	// (unix millis). It stays in the struct as accept-and-ignore: nothing
	// writes it any more, but rotation.json is read and written WHOLE, so
	// removing the field would have the next mode change or notify silently
	// strip a key the file on disk already carries.
	LastGateAt int64 `json:"last_gate_at,omitempty"`
}

func rotationStatePath() string {
	return filepath.Join(util.Home(), ".kraken", "rotation.json")
}

// ReadRotationState is defensive like ReadUsageSnapshot: a missing or
// corrupt file reads as mode "auto" with zero everything else, never an
// error — a binder that could not resolve a mode would have nothing to act
// on. Mode normalizes case- and whitespace-insensitively (strings.EqualFold
// on the trimmed value) to "manual" or "auto": anything else (unknown,
// malformed) reads as "auto". This is deliberately fail-CLOSED on the manual
// side — a hand-edited "MANUAL" or " manual\n" must still keep the pin,
// since an exact-match comparison would silently normalize it to "auto" and
// resume taking account decisions despite carl (or a hand edit) having asked
// kraken not to. A bad write can still never wedge anything permanently,
// since whatever ISN'T recognizably "manual" falls through to "auto".
func ReadRotationState() RotationState {
	var st RotationState
	data, err := os.ReadFile(rotationStatePath())
	if err != nil {
		return RotationState{Mode: "auto"}
	}
	if json.Unmarshal(data, &st) != nil {
		return RotationState{Mode: "auto"}
	}
	if strings.EqualFold(strings.TrimSpace(st.Mode), "manual") {
		st.Mode = "manual"
	} else {
		st.Mode = "auto"
	}
	return st
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

// --- notification dedup, freshness, and the manual-switch lock ---

// rotationNoticeSuppress is how long notifyDeduped suppresses a repeat
// notification carrying the same key — a standing condition (a dry pool, a
// failing swap) shouldn't re-notify on every single cell spawn.
const rotationNoticeSuppress = time.Hour

// rotationFreshSnapshotWindow: how old a usage snapshot row is allowed to be
// — by its OWN FetchedAt, not the snapshot's — before it stops counting as
// fresh. Keying on the row is the load-bearing half: mergeUsage re-stamps
// the whole snapshot's FetchedAt on EVERY pass, including a pass that
// carried a failed row's windows forward unchanged (Stale: true, original
// FetchedAt preserved), so the snapshot's own timestamp says nothing about
// any particular row.
const rotationFreshSnapshotWindow = 15 * time.Minute

func rotationLockPath() string {
	return filepath.Join(util.Home(), ".kraken", "rotation.lock")
}

// --- swappable seams, mirroring core's securityFind pattern, so this
// package's account decisions are testable without network, keychain, tmux
// or osascript.
//
// Their live consumer is the mid-run limit rescue in limitrescue.go —
// notifyFn for what carl is told, respawnPeerPane for the zeus respawn,
// mainIdentityEmail for who the machine's main login belongs to right now.
// runUsagePass is KickUsagePass's (usage.go), which the account API calls
// after a manual switch.
//
// fetchMainUsage went with Q2's rotation gate — nothing here fetches main's
// usage any more, since no spawn-time decision needs it. selectTarget and
// swapMain SURVIVE, but with exactly one consumer left: rescueSwap, the
// STANDING-agent rescue's swap half (standingrescue.go). Kronos and iris are
// main's own tenants — they have no job row to bind, their limit evidence IS
// main's exhaustion, and moving main for them displaces only the lane they
// live on — so theirs is the one automated main swap that outlives Q2. The
// CELL rescue rebinds one job row instead (rescueLimitedCell) and can reach
// neither seam; TestRetirementNoAutomatedMainSwap pins where core.SwapMainTo
// may be named. ---
var (
	// selectTarget ranks a machine-level rotation target for the standing
	// rescue's swap half — core.SelectRotationTarget unchanged. Not used by
	// any cell path: cells select through core.SelectBindTarget.
	selectTarget = core.SelectRotationTarget
	// swapMain is the machine-wide login swap the standing rescue performs —
	// core.SwapMainTo unchanged (journal, save-back, rollback all live
	// there). A seam so rescueSwap is exercisable without a real keychain.
	swapMain = core.SwapMainTo
	notifyFn = notify
	// respawnPeerPane is the limit rescue's resume half — kill the claude in
	// ZEUS' pane (the rescue's only target) and bring its session back on
	// the account the cell is bound to NOW. Its second argument is the
	// CALLER's full launch command: core builds none, since what a resumed
	// zeus must be launched with is this package's knowledge. A seam because
	// the rescue's resume path is decision logic worth exercising without a
	// tmux server under it.
	respawnPeerPane = core.RespawnPeerPane
	// mainIdentityEmail reports who main is RIGHT NOW (~/.claude.json). It is
	// what the binder and the rescue exclude from the pool: main is carl's
	// interactive lane, and SwapMainTo routinely mirrors it into a pool slot
	// of its own, so a cell binding to that mirror would land back on the one
	// account this feature exists to keep it off.
	mainIdentityEmail = core.MainIdentityEmail
	// runUsagePass defaults to the real UsagePass; swapped out in tests so
	// nothing here kicks off a real network usage pass in the background.
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
// distinct so a caller can report "already in flight" separately from "lock
// unavailable: <err>" — one blended message sends users hunting a phantom
// rotation instead of a filesystem problem.
//
// What it now guards is exactly THREE callers, and nothing else in the
// binary takes it: `k account <slug>`, POST /api/account/switch, and
// rescueSwap — the standing-agent rescue's swap half, the one automated
// main swapper that outlived Q2 (per-cell binding retired the spawn-time
// gate and the CELL rescue's swap). All three read the outgoing account's
// creds and write the incoming one's, so the lock keeps them off each other
// across processes.
//
// The file's content (pid + timestamp) is human-facing only — nothing reads
// it back to decide staleness. A process that dies while holding the fd has
// its flock released automatically by the kernel on exit, so the old
// "reclaim a lock file older than N minutes" logic is gone entirely along
// with the double-holder risk it existed to paper over. The flip side: a
// HUNG (not dead) holder blocks every later switch until it exits or is
// killed — accepted, and cheaper than it was, since both remaining callers
// are commands a human just issued and can reissue.
// AcquireRotationLock exports acquireRotationLock for those two call sites
// outside this package — `k account <slug>`'s CLI switch (cmd_account.go)
// and the web API's POST /api/account/switch handler — so neither can race
// the other mid-swap. See acquireRotationLock's doc comment for the flock
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

// rowFresh reports whether a usage-snapshot row's OWN FetchedAt (not the
// snapshot's) is recent enough to count as fresh — see
// rotationFreshSnapshotWindow for why the row's own timestamp is the only
// trustworthy signal.
//
// It is this package's one definition of that judgement. The binder does not
// call it: freshness is one of core.SelectBindTarget's ranking keys (fresh
// outranks stale rather than excluding it, since a stale row still describes
// a real account), and duplicating the bound here would be a second, quietly
// diverging answer to the same question.
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

// --- Q2: there is no rotation gate any more ---
//
// RotationGate used to sit at the top of every cell spawn and, in auto mode,
// swap the machine's main login off a nearly-exhausted account before the
// spawn's usage landed on it. It is gone, and what replaced it is
// binding.go's bindAccount: the same moment in the same two spawn paths,
// deciding a per-JOB account instead of a per-MACHINE one.
//
// The reason it had to go rather than coexist is that its unit was wrong in
// both directions. A gate that swaps main displaces every OTHER live cell
// and carl's own interactive sessions to fix one spawn's problem — and every
// displaced session then floods the fresh account with uncached context. And
// it could not help the case that actually hurt: a window that runs out
// mid-job, after the gate has already run. Binding answers both at once, so
// keeping a machine-wide swap on the spawn path as well would only reopen
// the displacement it exists to end.
//
// What survives above: the mode (a manual pin now means kraken takes no
// account decision at all), notifyDeduped, AcquireRotationLock for the two
// manual swap callers plus rescueSwap, and the selectTarget/swapMain seams —
// whose one consumer is the STANDING-agent rescue (standingrescue.go): a
// swap for main's own tenants displaces exactly the lane whose exhaustion is
// the evidence, which is the case the displacement argument above does not
// reach.
