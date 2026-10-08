package jobs

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"kraken/internal/core"
	"kraken/internal/store"
	"kraken/internal/util"
)

// thread.go — side threads: a second claude, forked off a job's zeus, living
// in its own window of the same cell tmux session and the same worktree.
//
// carl branches one to ASK something — usually about the very block he is
// looking at — so a thread must never disturb the job it hangs off. Three
// exclusions carry that, all keyed off this file: a thread must not vote in the
// working/blocked ROLL-CALL (cellActive takes a skip set of live thread panes,
// status.go), must not be elected zeus (CellZeus filters the thread name prefix
// AND the live thread panes, cell.go), and must not be interrupted by Stop
// (InterruptCell skips the same panes, interrupt.go).
//
// The row (store.Thread) is a pointer, not a transcript: killing a thread ends
// its window and closes the row, reviving resumes the SAME claude session in a
// fresh window, and neither ever snapshots the conversation — claude's own
// transcript, located by SessionID, is the history.
//
// The hard part is deciding a thread is GONE. A pane's claude is nameless for
// its first seconds (Peer.Name comes from claude's session file), so absence of
// the name is never proof of death: every close path below demands positive
// evidence — a grace window, a still-starting pane, and a trusted snapshot —
// before it clears a row's pane, because a wrongly closed row un-skips a live
// pane in the roll call, orphans kill/send, and lets a revive start a SECOND
// claude on one session id.
//
// That second claude is the failure this file exists to make impossible, so a
// revive is arbitrated by the DB, never by a snapshot: the discovery scan is a
// fast, friendly refusal, but the row itself is flipped closed→live by one
// compare-and-set (store.ClaimClosedThread) BEFORE anything is staged or
// spawned. Two concurrent revives of one tid — a double-tapped button, the
// board and the phone, an iOS retry — both pass the scan; only one can win the
// claim, and the loser spawns nothing at all.

// ThreadNamePrefix is the --name prefix every one of a job's threads carries,
// and the single string three separate exclusions key off: the roll call, the
// zeus election, and Stop. It embeds the slug so two jobs' threads can never
// collide, and ends in "-" so a prefix match can't span into another tid.
func ThreadNamePrefix(j *Job) string { return "thread-" + util.Slugify(j.Slug) + "-" }

// threadNamePrefixes is every prefix a thread of THIS job may be running
// under: the current one first, then one per former slug. A thread's --name is
// fixed at spawn and a rename does not respawn anything, so after a rename the
// job's own live threads still answer to an older prefix.
//
// It exists because all three exclusions ThreadNamePrefix serves fail in the
// same direction when they miss a thread, and each failure is the one its own
// comment calls the worst outcome available: staleThreadTIDs closes a LIVE row
// (un-skipping its pane in the roll call, orphaning kill/send, and letting a
// revive start a second claude on one session id), CellZeus elects the thread
// as zeus (stamping a thread's session id into the job and pointing carl's
// steer messages at it), and Stop interrupts the very conversation carl
// branched to have.
func threadNamePrefixes(j *Job) []string {
	out := []string{ThreadNamePrefix(j)}
	seen := map[string]bool{out[0]: true}
	for _, f := range j.FormerSlugs {
		s := util.Slugify(f)
		if s == "" {
			continue
		}
		p := "thread-" + s + "-"
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// hasAnyPrefix reports whether name starts with any of prefixes. A tiny
// helper, but it is what keeps the three prefix exclusions from each spelling
// the loop out and drifting.
func hasAnyPrefix(name string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// ThreadAgentName is the claude --name a thread runs under: the prefix plus
// its tid. This is the identity every verify-before-acting check compares
// against — a pane whose claude answers to this name is this thread, and any
// other occupant means the thread is gone and the pane was recycled.
func ThreadAgentName(j *Job, tid string) string { return ThreadNamePrefix(j) + tid }

// threadBriefPath and threadPromptPath name a thread's two staged spawn
// inputs, beside zeus' own (zeusBriefPath/spawnPromptPath) in the cell dir —
// kraken-owned plumbing, never in CellArtifacts, which is carl's deliverables
// directory alone.
//
// Both are keyed by tid rather than shared across a job's threads. The brief
// names the thread's own tid and title, and the pane's shell reads the file
// some milliseconds AFTER tmux returns — so a single shared path would let a
// second branch (or a revive) overwrite the file between the first window
// opening and its claude reading it, handing one thread another's identity.
// Both resolve through existingCellDir for zeusBriefPath's reason: stagedCommand
// writes these files without creating their directory, so branching a thread
// off a RENAMED job would fail on a cell dir that is still under the job's
// earlier name.
func threadBriefPath(j *Job, tid string) string {
	return filepath.Join(existingCellDir(j), "thread-"+tid+"-brief.md")
}

func threadPromptPath(j *Job, tid string) string {
	return filepath.Join(existingCellDir(j), "thread-"+tid+"-prompt.md")
}

// refuseCodexThread is the first line of both spawning verbs (branch, revive):
// a side thread is a CLAUDE fork, and a codex cell has nothing to fork.
//
// Everything a thread is made of is claude machinery. The spawn is `claude
// --fork-session -r <parent session>` under a kraken-minted session id, which
// is how a thread comes up holding zeus' conversation while writing to a
// transcript of its own; codex has no equivalent — `codex resume <id>`
// CONTINUES a conversation in place, so pointing one at zeus' thread would put
// a second writer on the conversation carl is trying to ask about rather than
// branching off it. The model alias, the staged brief and the revive path are
// all built on the same assumption.
//
// It is FIRST, before the store handle and before any liveness check, so a
// refusal leaves the world exactly as it found it: no reserved row, no staged
// brief, no tmux window. A refusal that had already opened a window would be
// worse than the feature being absent.
func refuseCodexThread(j *Job, verb string) error {
	if j != nil && j.Agent == "codex" {
		return fmt.Errorf("%s runs on codex — side threads are claude-only, so there is nothing to %s (open a shell in the cell's session instead: k o %s)", j.Slug, verb, CellSession(j))
	}
	return nil
}

// threadStore resolves the store every thread verb needs. Threads live ONLY in
// kraken.db — there is no file fallback the way jobs still have one — so a
// caller running without a wired store (the bubbletea viewer, a file-mode
// daemon) gets a clear refusal rather than a silent no-op. j.ID == 0 means a
// file-backed job with no row to hang threads off, which is the same refusal
// for the same reason.
func threadStore(j *Job) (*store.Store, error) {
	st := ActiveStore()
	if st == nil {
		return nil, fmt.Errorf("side threads need kraken serve's store — none is wired")
	}
	if j.ID == 0 {
		return nil, fmt.Errorf("%s has no store row — side threads need one", j.Slug)
	}
	return st, nil
}

// newSessionUUID mints the --session-id a forked thread runs under: a random
// RFC 4122 version-4 uuid, stdlib-only (crypto/rand + the standard
// 8-4-4-4-12 hex formatting), since claude accepts nothing else in that flag.
//
// kraken names the session rather than letting claude generate one because the
// id has to be recorded in the thread's row BEFORE the pane exists — it is the
// only handle on the fork's transcript, and the only thing ReviveThread can
// resume. Reading it back off discovery afterwards would race the session file
// (and inherit SessionIDGuessed's whole class of problems).
func newSessionUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate session id: %v", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// parseWindowTarget splits `tmux new-window -P -F '#{session_name}:#{window_index} (#{pane_id})'`
// output — e.g. "cell-foo:3 (%17)" — into the window target and the pane id.
// PURE, so the parse is testable without tmux.
//
// A pane that doesn't start with "%" is reported as "" rather than passed
// along: the pane id is what every later verify-before-acting check targets,
// and a malformed one would make those checks compare against garbage. The
// caller treats an empty pane as a failed spawn and kills the window again.
func parseWindowTarget(out string) (window, pane string) {
	out = strings.TrimSpace(out)
	window, rest, ok := strings.Cut(out, " ")
	if !ok {
		return window, ""
	}
	pane = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(rest), "("), ")")
	if !strings.HasPrefix(pane, "%") {
		return window, ""
	}
	return window, pane
}

// threadStartGrace is how long a freshly spawned thread row is immune to
// reconcile. Peer.Name only appears once claude has written
// ~/.claude/sessions/<pid>.json — seconds into startup, and not at all while a
// folder-trust dialog is up (threads get no acceptTrustPrompts pass; only zeus
// does). The daemon ticks every 15s, so without this window a branch landing
// just before a tick would be "stale" while its claude is alive and booting.
// It also covers the gap AddThreadNext opens deliberately: a reserved row has
// no pane at all until its window exists.
const threadStartGrace = 60 * time.Second

// threadSpawns remembers when kraken last opened a window for a thread, keyed
// job/tid — the reopen half of the grace window. A revive restarts the same
// boot race with an OLD CreatedAt (a claim/SetThreadPane pair leaves identity
// and provenance alone by design), so the row itself can't say "this one just
// started".
//
// Process-local and best effort, deliberately: kraken serve is the single
// process that both spawns threads and runs the reconcile tick, so the ledger
// is complete for every spawn this process made. A serve restart loses it, and
// a thread revived seconds before the restart falls back to its older
// CreatedAt — but that is the safe direction, twice over. The still-starting
// pane check still covers a booting claude whose pane was recorded; a row that
// never got its pane recorded is one whose spawn died with the process, which
// is exactly the row reconcile SHOULD close (closing it makes it revivable
// again). And the guarantee that actually matters — never two claudes on one
// session id — does not rest here at all: liveThreadClaude's session-id scan
// finds an orphaned claude regardless of what the ledger remembers. Persisting
// a spawn timestamp would mean a DB write per spawn to shorten a 60s window
// that costs nothing when it is wrong.
var threadSpawns sync.Map // "<jobID>/<tid>" -> time.Time

func threadSpawnKey(jobID int64, tid string) string {
	return strconv.FormatInt(jobID, 10) + "/" + tid
}

func noteThreadSpawn(jobID int64, tid string) {
	threadSpawns.Store(threadSpawnKey(jobID, tid), time.Now())
}

// threadStartedAt is the instant the thread's CURRENT claude was started, as
// well as this process can know it: the later of the row's CreatedAt and the
// last spawn kraken itself recorded.
func threadStartedAt(t store.Thread) time.Time {
	started := time.UnixMilli(t.CreatedAt)
	if v, ok := threadSpawns.Load(threadSpawnKey(t.JobID, t.TID)); ok {
		if at, ok := v.(time.Time); ok && at.After(started) {
			started = at
		}
	}
	return started
}

// startingClaudeInPane returns the depth-0 claude occupying pane in the cell's
// own tmux session that discovery cannot name yet (no session file, so
// Peer.Name is empty), or nil. PURE.
//
// This is the evidence-of-life that stands in for the name check while a thread
// boots: something claude-shaped is sitting in the pane kraken opened for it,
// which is reason enough not to declare the row dead — and reason enough to
// refuse a second `claude -r` on the same session id.
func startingClaudeInPane(all []*core.Peer, pane, sess string) *core.Peer {
	if pane == "" {
		return nil
	}
	for _, p := range all {
		if p.Pane == pane && p.Agent == "claude" && p.Depth == 0 && p.Name == "" && p.TmuxSession == sess {
			return p
		}
	}
	return nil
}

// threadPeer finds the live claude holding pane under agentName — the
// verify-before-acting check (actions.go's checkSendTarget/verifyKillTarget
// pattern) every targeted thread verb runs against a FRESH discovery snapshot
// before it touches tmux. PURE.
//
// Matching on the name, not just the pane, is the whole point: tmux recycles a
// dead thread's pane id, so a bare pane-exists check would eventually kill or
// message a stranger's pane. Nil means "this pane is no longer that thread".
func threadPeer(all []*core.Peer, pane, agentName string) *core.Peer {
	if pane == "" || agentName == "" {
		return nil
	}
	for _, p := range all {
		if p.Pane == pane && p.Name == agentName && p.Agent == "claude" {
			return p
		}
	}
	return nil
}

// liveThreadClaude returns the claude that already IS this thread — anywhere,
// under any name — or nil. PURE.
//
// Keyed on the SESSION ID first, not on the recorded pane, because the pane is
// exactly the field a close clears: a row whose pane was cleared (a reconcile
// that fired too early, a tmux hiccup) sails straight past a pane-keyed guard,
// and the revive it lets through starts a SECOND claude on one session id —
// two writers on one transcript, unrecoverable. zeus is skipped by name so its
// own GUESSED session id (inferred from cwd + newest transcript, which in a
// cell may well BE the thread's) can never wedge a revive shut forever.
//
// Three signals, any of which means alive: the thread's claude holds its
// recorded pane under its own name, some claude in the cell session is running
// its session id, or its recorded pane holds a claude too young to be named.
func liveThreadClaude(j *Job, all []*core.Peer, t store.Thread) *core.Peer {
	// former names on all three signals, for staleThreadTIDs' reason read from
	// the other end: this is the guard a REVIVE consults, and a renamed job's
	// live thread that reads as gone here is the second claude on one session
	// id this file exists to make impossible.
	prefixes, sessions := threadNamePrefixes(j), cellSessionCandidates(j)
	if p := anyThreadPeer(all, t.Pane, prefixes, t.TID); p != nil {
		return p
	}
	if t.SessionID != "" {
		inCell := map[string]bool{}
		for _, s := range sessions {
			inCell[s] = true
		}
		for _, p := range all {
			// The zeus exclusion goes through the CANDIDATE set, not the one
			// current name: a renamed cell's zeus still answers to
			// zeus-<former slug>, and this exclusion is what keeps a zeus
			// whose GUESSED session id happens to be the thread's (they share
			// a worktree) from standing in for a dead thread's claude. The
			// widening is strictly safe — no thread name starts with "zeus-".
			if p.Agent == "claude" && inCell[p.TmuxSession] && p.SessionID == t.SessionID && !isZeusName(j, p.Name) {
				return p
			}
		}
	}
	return anyStartingClaudeInPane(all, t.Pane, sessions)
}

// zeusForkTarget resolves the peer a new thread forks from, or the refusal
// explaining why the fork can't happen. PURE (it takes the snapshot), so every
// refusal is table-testable by message without tmux.
//
// core.BranchGuard is reused rather than reimplemented, for its reasoning as
// much as its checks: a GUESSED session id (inferred from cwd + newest
// transcript, with no session file to confirm it) could fork some OTHER
// claude's session entirely — the thread would come up holding a stranger's
// conversation, in carl's worktree, named as this job's thread.
func zeusForkTarget(j *Job, all []*core.Peer) (*core.Peer, error) {
	zeus := CellZeus(j, all)
	if zeus == nil {
		return nil, fmt.Errorf("%s has no live zeus to branch from", j.Slug)
	}
	if err := core.BranchGuard(zeus); err != nil {
		return nil, fmt.Errorf("%s: %v", j.Slug, err)
	}
	return zeus, nil
}

// threadTarget is the verify-before-acting resolution every targeted thread
// verb (kill, send) runs: the row's recorded pane must still be held by THIS
// thread's claude in a FRESH snapshot. PURE, so the refusal text is testable.
//
// verb shapes the tail of the message because the two recoveries differ: a
// gone kill target needs nothing (the window is already dead), while a gone
// send target needs a revive first.
func threadTarget(j *Job, t store.Thread, all []*core.Peer, verb string) (*core.Peer, error) {
	// every prefix the job has held, exactly as staleThreadTIDs and
	// liveThreadClaude match them. A thread's --name is fixed at spawn and a
	// rename respawns nothing, so a pre-rename thread answers to
	// thread-<old slug>-<tid> forever — and with the current prefix alone
	// staleThreadTIDs correctly keeps its row live while kill and send both
	// lose their target, wedging the thread with no exit for the rest of the
	// cell's life. Orphan kill/send is this file's own worst outcome, and this
	// is the one door it arrives through.
	p := anyThreadPeer(all, t.Pane, threadNamePrefixes(j), t.TID)
	if p == nil {
		where := t.Pane
		if where == "" {
			where = "(no pane)"
		}
		return nil, fmt.Errorf("pane %s no longer holds thread %s of %s — %s", where, t.TID, j.Slug, verb)
	}
	return p, nil
}

// staleThreadTIDs returns the tids of live rows whose thread is provably gone —
// killed by hand, exited on its own, or lost with the tmux server. PURE (bar
// the process-local spawn ledger) and table-testable; ReconcileThreads is the
// thin wrapper that does the discovery and the DB writes.
//
// A missing NAME is not proof of death, which is the whole shape of this
// function. Peer.Name arrives seconds into claude's startup, so three separate
// answers count as alive: the thread's claude holds its pane under its own name
// (the steady state), the row is inside threadStartGrace of its last spawn, or
// its pane holds a still-nameless depth-0 claude in the cell session. Only a row
// that fails all three is closed — because closing a LIVE thread is the worst
// outcome in this file: its pane leaves LiveThreadPanes and starts voting a
// blocked job into "working", kill/send lose their target, and ReviveThread
// would start a second claude on the one session id.
func staleThreadTIDs(j *Job, threads []store.Thread, all []*core.Peer, now time.Time) []string {
	// every name the job has held, on both keys: a rename respawns nothing, so
	// a live thread of a renamed job still answers to an older prefix and sits
	// in a session still called by an older name. Missing either would close a
	// live row, which is the outcome this file's header calls the worst one in
	// it.
	prefixes, sessions := threadNamePrefixes(j), cellSessionCandidates(j)
	var out []string
	for _, t := range threads {
		if t.State != "live" {
			continue
		}
		if anyThreadPeer(all, t.Pane, prefixes, t.TID) != nil {
			continue
		}
		if now.Sub(threadStartedAt(t)) < threadStartGrace {
			continue // still booting: no session file, so no name to match yet
		}
		if anyStartingClaudeInPane(all, t.Pane, sessions) != nil {
			continue // a nameless claude is in the pane kraken opened for it
		}
		out = append(out, t.TID)
	}
	return out
}

// anyThreadPeer is threadPeer over every name tid may be running under. PURE.
func anyThreadPeer(all []*core.Peer, pane string, prefixes []string, tid string) *core.Peer {
	for _, prefix := range prefixes {
		if p := threadPeer(all, pane, prefix+tid); p != nil {
			return p
		}
	}
	return nil
}

// anyStartingClaudeInPane is startingClaudeInPane over every session name the
// cell may be running under. PURE.
func anyStartingClaudeInPane(all []*core.Peer, pane string, sessions []string) *core.Peer {
	for _, sess := range sessions {
		if p := startingClaudeInPane(all, pane, sess); p != nil {
			return p
		}
	}
	return nil
}

// emptySnapshotConfirm is how long a WHOLLY empty snapshot — no panes and no
// sessions — must persist before ReconcileThreads will write off it. The
// daemon ticks every 15s, so this is "two ticks in a row said the same thing".
//
// A dead tmux server stays dead, so the only cost of waiting is closing a torn
// -down cell's rows one tick later. What it buys is the one hole the
// cross-check below cannot see: a transient exec failure (fork/exec under
// load, a tmux server mid-restart) that hits BOTH `tmux list-sessions` and
// `tmux list-panes -a` in the same tick is byte-for-byte identical to a dead
// server, and would close every live thread row on the machine.
const emptySnapshotConfirm = 30 * time.Second

// emptySnapshotSince latches when the current run of empty snapshots began, or
// the zero time when the last snapshot had something in it. Package-level
// rather than per-job because the condition it tracks is machine-wide, and one
// tick reconciles every started job off the SAME pair of snapshots.
var emptySnapshotSince struct {
	sync.Mutex
	at time.Time
}

// noteEmptySnapshot folds this tick's snapshot into the latch and reports how
// long the current empty run has lasted (0 when the snapshot isn't empty, or
// when this is the first empty one). Impure by necessity — the memory of the
// previous tick is exactly what a single tick cannot have — which is why it is
// separate from threadSnapshotTrusted, which stays pure and table-tested.
//
// Idempotent within a tick: the first job to call it starts the run, and every
// other job that tick reads the same start instant back.
func noteEmptySnapshot(empty bool, now time.Time) time.Duration {
	emptySnapshotSince.Lock()
	defer emptySnapshotSince.Unlock()
	if !empty {
		emptySnapshotSince.at = time.Time{}
		return 0
	}
	if emptySnapshotSince.at.IsZero() {
		emptySnapshotSince.at = now
	}
	return now.Sub(emptySnapshotSince.at)
}

// threadSnapshotTrusted reports whether a discovery snapshot is solid enough to
// close j's thread rows off. PURE (emptyFor carries the one cross-tick fact it
// needs, from noteEmptySnapshot above).
//
// ReconcileThreads is the ONE consumer of the tick's snapshot that writes state
// from it; every other one only reads. listTmuxPanes (core/discover.go) swallows
// exec errors and returns nothing, so a single tmux hiccup would otherwise read
// as "every thread on the machine is gone" and close every live row fleet-wide,
// with nothing that ever reopens them. The two snapshots the caller already has
// cross-check each other: panes but no sessions, or sessions but no panes, is a
// contradiction only a failed call can produce, and a live session missing from
// the pane snapshot is the same failure scoped to one cell.
//
// Both empty is the case the cross-check is blind to — a dead tmux server and a
// double exec failure look identical — so time is the tiebreaker instead: it is
// trusted only once the emptiness has held for emptySnapshotConfirm, which a
// hiccup does not survive and a dead server does.
func threadSnapshotTrusted(sess string, all []*core.Peer, live map[string]bool, emptyFor time.Duration) bool {
	if len(all) == 0 && len(live) == 0 {
		return emptyFor >= emptySnapshotConfirm
	}
	if (len(all) == 0) != (len(live) == 0) {
		return false
	}
	if !live[sess] {
		return true // the cell's session is gone: its windows went with it
	}
	for _, p := range all {
		if p.TmuxSession == sess {
			return true
		}
	}
	return false // tmux says the session is live but no pane of it was seen
}

// liveThreadPanesFor returns the panes of j's own live threads, for the two
// exclusions that are per-job rather than global (InterruptCell's Stop targets,
// and any caller that already has one job in hand). Empty — never an error — on
// no store or a failed read: an exclusion set that fails open costs a spurious
// Escape into a thread's pane, which is recoverable, whereas failing the whole
// verb is not.
func liveThreadPanesFor(j *Job) map[string]bool {
	out, _ := liveThreadPanesForOK(j)
	return out
}

// liveThreadPanesForOK is the readability-aware sibling, for the one caller
// whose failure mode is NOT recoverable: the usage-limit rescue respawns a
// pane with `-k`, so a thread pane wrongly absent from this set is carl's
// side conversation SIGKILLed, not a stray Escape. ok is false only when the
// store was asked and refused — no store wired and a job with no id are
// ordinary "there are no threads" answers, not failures to read.
//
// The two exist rather than one strict function because fail-open is right
// for Stop and wrong for a kill, and a single behaviour would have to be
// wrong for one of them.
func liveThreadPanesForOK(j *Job) (panes map[string]bool, ok bool) {
	out := map[string]bool{}
	st := ActiveStore()
	if st == nil || j.ID == 0 {
		return out, true
	}
	threads, err := st.ListThreads(j.ID)
	if err != nil {
		return out, false
	}
	for _, t := range threads {
		if t.State == "live" && t.Pane != "" {
			out[t.Pane] = true
		}
	}
	return out, true
}

// threadSpawnCommand renders the shell command a thread's window runs, for
// both spawn paths — branch (fork) and revive (resume) — since they differ
// only in the resume flags the caller passes.
//
// The brief is staged on EVERY spawn, revive included. A resumed session does
// NOT retain an --append-system-prompt from an earlier run (verified
// empirically): a revive without it comes back as a plain claude holding an
// implementer's history, which is precisely the prior threadBrief exists to
// overwrite. `--` stays last, after the brief flag, so the seed is claude's
// initial prompt and not another flag's value.
//
// model is the thread's OWN pick (a core.ThreadModels alias) or "" for none;
// threadModel below turns it into the single --model value cellClaudeArgs
// puts on the line. It is a parameter rather than read off the row here
// because branch has no row to read yet — the command is rendered from the
// request, and the revive path passes the stored alias back in.
//
// The job's account binding rides in front of the launcher here exactly as it
// does for zeus (accountEnvPrefix, binding.go). A thread reads the same
// worktree and asks about the same work, so billing it to a different account
// than the cell it hangs off would split one conversation's prompt cache
// across two logins — and on a bound cell the main lane is carl's, not the
// job's.
//
// session and role ride in front too (threadEnvPrefix): the thread's own
// session id and role, so `kraken mm commit` run from the thread's Bash tool
// can say who is calling without a process-tree walk. Both spawn paths pass
// the row's values — a revived mind thread is still the mind thread.
func threadSpawnCommand(j *Job, tid, title, seed, model, role, session string, resumeArgs []string) (string, error) {
	resolved, err := threadModel(j, model)
	if err != nil {
		return "", err
	}
	argv := append(cellClaudeArgs(resolved), "--name", ThreadAgentName(j, tid))
	argv = append(argv, resumeArgs...)
	wt := threadWorktree(j)
	cmd, err := stagedCommand(argv,
		spawnInput{"--append-system-prompt", threadBrief(j, tid, title, wt, role), threadBriefPath(j, tid)},
		spawnInput{"--", threadSeedFor(seed, role), threadPromptPath(j, tid)})
	if err != nil {
		return "", err
	}
	return accountEnvPrefix(j) + threadEnvPrefix(session, role) + cmd, nil
}

// threadEnvPrefix is the env-assignment prefix that tells a thread's claude —
// and every `kraken` verb its Bash tool runs — which thread it is:
// KRAKEN_THREAD_SESSION and KRAKEN_THREAD_ROLE, single-quoted, with a trailing
// space so it concatenates in front of the launcher like accountEnvPrefix.
// It rides the command string rather than the window's environment for
// accountEnvPrefix's measured reason (a respawned pane inherits no client
// env). "" when there is no session to name: an anonymous assignment would
// only make the commit gate's refusal harder to read. PURE.
func threadEnvPrefix(session, role string) string {
	if session == "" {
		return ""
	}
	return "KRAKEN_THREAD_SESSION='" + shellSingleQuote(session) + "' KRAKEN_THREAD_ROLE='" + shellSingleQuote(role) + "' "
}

// validThreadRole is the closed set of thread roles, the threads.role CHECK
// restated so a bad role is refused before any spawn work.
func validThreadRole(role string) bool { return role == "ask" || role == "mind" }

// threadModel resolves the model a thread's claude is spawned on, in the one
// place both spawn paths share: carl's own pick for THIS thread when he made
// one, else the job's model: field, else — through core.ModelArgs' own
// fallback, which is why "" is returned rather than DefaultModel spelled again
// — core.DefaultModel. The order is what makes the picker meaningful: a thread
// is carl asking about the work rather than doing it, so a cheap question may
// run on Haiku inside a job whose cell is pinned to something else entirely.
//
// An unknown alias is an ERROR rather than a silent fallback, and that is the
// point of resolving here at all: an unrecognised value passed through to
// --model would leave claude to reject it AFTER a tmux window had been opened
// and a row written, which reads to carl as a thread that came up dead. The
// API refuses the same value earlier with a 400; this is the guard for every
// other caller (and for a row whose stored alias was retired from the menu).
//
// Which is the one cost of that strictness, and it lands on whoever edits the
// map: RETIRING an alias strands every row still carrying it — the thread can
// never be revived again (this error, forever) and nothing can edit the column
// back, since store deliberately has no SetThreadModel to go with
// SetThreadTitle. So a commit that removes an entry from core.ThreadModels
// owes a migration alongside it, blanking threads.model on every row that
// still names the retired alias — an UPDATE back to the empty string, which
// drops those threads onto the job's model and then core.DefaultModel, the
// fallback they would have had all along.
func threadModel(j *Job, model string) (string, error) {
	if model == "" {
		return j.Model, nil
	}
	id, ok := core.ThreadModels[model]
	if !ok {
		return "", fmt.Errorf("bad thread model %q: want haiku, sonnet, opus or fable", model)
	}
	return id, nil
}

// threadWorktree resolves the cell worktree a thread opens in — the same
// directory zeus is working in, since a thread exists to read that work. "" when
// the repo can't be resolved or the worktree is gone; callers refuse the spawn
// on an empty result rather than letting tmux silently fall back to $HOME.
func threadWorktree(j *Job) string {
	repo, err := ResolveRepo(j.Repo)
	if err != nil {
		return ""
	}
	return ExistingWorktree(j, repo)
}

// BranchThread forks zeus' session into a new side thread: a fresh window of
// the cell's own tmux session, in the cell's worktree, running a claude that
// inherits zeus' conversation under a brand-new session id.
//
// Order matters twice over. Every cheap refusal comes before any tmux or DB
// work; then the tid is RESERVED (AddThreadNext, one transaction) before a
// single file is staged or a window opened. Reserving late — the obvious
// order, since a row with no pane looks unfinished — is a real race: two
// branches on one job would both read t3 from a separate NextThreadID call,
// both write thread-t3-brief.md, and the survivor would be briefed with the
// other request's title and asked the other request's question. From the
// reservation on, every failure path closes the row again, so a failed branch
// leaves neither an orphan pane nor a row claiming one.
//
// model is carl's pick for this thread — a core.ThreadModels alias, or "" to
// take the job's model: field and then core.DefaultModel as every thread did
// before the picker. It is stored on the row, which is what makes a revive
// come back on the same tier.
func BranchThread(j *Job, title, seed, model string) (store.Thread, error) {
	return BranchThreadWithRole(j, title, seed, model, "ask")
}

// BranchThreadWithRole is BranchThread with the thread's role named: "ask"
// (carl's question thread, every thread before the mental-model rework) or
// "mind" (the job's mind thread, the only AGENT-side writer of the mental
// model, which CaptureToMind branches). The role is stored on the row and staged into the
// pane's env prefix and brief; anything else is refused before any spawn work.
func BranchThreadWithRole(j *Job, title, seed, model, role string) (store.Thread, error) {
	if !validThreadRole(role) {
		return store.Thread{}, fmt.Errorf("bad thread role %q: want ask or mind", role)
	}
	if err := refuseCodexThread(j, "branch"); err != nil {
		return store.Thread{}, err
	}
	st, err := threadStore(j)
	if err != nil {
		return store.Thread{}, err
	}
	// with the cheap refusals, not left to the insert: store validates the
	// alias too (normalizeThread), but only after this function has already
	// resolved a zeus to fork and minted a session id, and the refusal is the
	// same either way — so it belongs where the reader is told every cheap
	// refusal lives.
	if _, err := threadModel(j, model); err != nil {
		return store.Thread{}, err
	}
	// FindCellSession, the way ReconcileThreads below already resolves it: a
	// renamed cell whose tmux convergence failed is live under an older
	// session name, and the branch has to open its window in the session that
	// actually exists — refusing here (or, worse, opening a window in a name
	// tmux does not know) for a cell carl is looking at is the whole hole.
	sess, live := FindCellSession(j, LiveSessions())
	if !live {
		return store.Thread{}, fmt.Errorf("%s has no live cell to branch — start or reactivate it first", j.Slug)
	}
	wt := threadWorktree(j)
	if wt == "" {
		return store.Thread{}, fmt.Errorf("%s has no worktree to branch into — reactivate the job first", j.Slug)
	}
	all, _ := core.OrderAll(core.DiscoverAll())
	zeus, err := zeusForkTarget(j, all)
	if err != nil {
		return store.Thread{}, err
	}
	sessionID, err := newSessionUUID()
	if err != nil {
		return store.Thread{}, err
	}
	// provenance only, and best effort: no transcript yet (a zeus that hasn't
	// taken a turn), an unreadable file, or a truncated tail all leave the
	// anchor blank rather than failing a spawn carl asked for.
	//
	// A mind thread records a second anchor beside it, the TURN key
	// (core.LastTurnUUID, zeus' last prompt record): CaptureToMind matches
	// captures to a mind thread on it, and it must not move while zeus makes
	// tool calls inside one turn. ForkUUID itself stays the last record for
	// every role — the thread overlay splits inherited history on it, and a
	// prompt-anchored split would render the whole of zeus' turn as the
	// thread's own conversation.
	forkUUID, turnUUID := "", ""
	if path, perr := core.ReadTranscriptPath(zeus); perr == nil {
		forkUUID = core.LastMessageUUID(path)
		if role == "mind" {
			turnUUID = core.LastTurnUUID(path)
		}
	}
	// the tid reservation: allocate and insert in one transaction, so two
	// concurrent branches can never agree on a tid and stomp each other's
	// staged brief. The row is live-with-no-pane until the window exists —
	// threadStartGrace is what keeps this tick's reconcile off it.
	t, err := st.AddThreadNext(store.Thread{
		JobID:         j.ID,
		Title:         title,
		SessionID:     sessionID,
		ParentSession: zeus.SessionID,
		ForkUUID:      forkUUID,
		TurnUUID:      turnUUID,
		CWD:           wt,
		State:         "live",
		Seed:          seed,
		Model:         model,
		Role:          role,
	})
	if err != nil {
		return store.Thread{}, err
	}
	noteThreadSpawn(j.ID, t.TID)
	cmd, err := threadSpawnCommand(j, t.TID, title, seed, t.Model, role, sessionID,
		[]string{"--session-id", sessionID, "--fork-session", "-r", zeus.SessionID})
	if err != nil {
		releaseReservedThread(st, j, t.TID)
		return store.Thread{}, err
	}
	// cellSpawnEnv, not the daemon's own environment: this window is opened by
	// kraken (launchd-run), not by zeus, so nothing has put the Go toolchain on
	// its PATH — and a thread reads the same worktree and runs the same tools.
	at, err := core.NewWindowNamed(sess, wt, t.TID, cellSpawnEnv(), []string{cmd})
	if err != nil {
		releaseReservedThread(st, j, t.TID)
		return store.Thread{}, fmt.Errorf("%s: open thread window: %v", j.Slug, err)
	}
	window, pane := parseWindowTarget(at)
	if pane == "" {
		killThreadWindow(window)
		releaseReservedThread(st, j, t.TID)
		return store.Thread{}, fmt.Errorf("%s: tmux reported no pane id for the new thread window (%q)", j.Slug, at)
	}
	// SetThreadPane, not a claim: AddThreadNext already inserted this row LIVE
	// (that insert is the reservation), so there is no closed→live transition
	// to win here — only the coordinates to fill in.
	if err := st.SetThreadPane(j.ID, t.TID, pane, window); err != nil {
		// never leave an orphan pane with no row pointing at it: the window is
		// the only thing carl could see, and nothing would reconcile it away.
		killThreadWindow(pane)
		releaseReservedThread(st, j, t.TID)
		return store.Thread{}, err
	}
	t.Pane, t.Window, t.State = pane, window, "live"
	util.LogEvent("jobs", "info", "thread-branch", fmt.Sprintf("%s: branched thread %s (%s) in %s", j.Slug, t.TID, title, pane))
	return t, nil
}

// releaseReservedThread closes a row whose window never came up, so a failed
// branch — or a failed revive, undoing its claim — leaves nothing claiming to
// be live. Best effort and never returned:
// the caller already has the error that actually explains the failure, and the
// tid itself is deliberately NOT recycled (NextThreadID takes the max, never a
// count) — a closed row with no session is a visible, harmless tombstone,
// where a reused tid could name two different forked sessions.
func releaseReservedThread(st *store.Store, j *Job, tid string) {
	if err := st.CloseThread(j.ID, tid, 0); err != nil {
		util.LogEvent("jobs", "error", "thread-branch", fmt.Sprintf("%s: releasing reserved thread %s failed: %v", j.Slug, tid, err))
	}
}

// killThreadWindow tears down one thread window by pane (or window target).
// Best effort by design: it is the cleanup path of a spawn that already
// failed, and the error the caller is about to return is the interesting one.
func killThreadWindow(target string) {
	if target == "" {
		return
	}
	_ = exec.Command("tmux", "kill-window", "-t", target).Run()
}

// KillThread ends a thread's window and closes its row. The ROW SURVIVES: its
// session id still locates the transcript, so a killed thread stays readable
// and ReviveThread can resume it later.
//
// Verify before acting (the repo invariant): the recorded pane must still be
// held by THIS thread's claude, re-checked against a fresh discovery snapshot.
// tmux recycles pane ids, so killing a stale one would take out whatever
// window inherited it — zeus' own, in the worst case.
func KillThread(j *Job, tid string) error {
	st, err := threadStore(j)
	if err != nil {
		return err
	}
	t, err := st.GetThread(j.ID, tid)
	if err != nil {
		return err
	}
	all, _ := core.OrderAll(core.DiscoverAll())
	if _, err := threadTarget(j, *t, all, "refusing to kill it"); err != nil {
		return err
	}
	if err := exec.Command("tmux", "kill-window", "-t", t.Pane).Run(); err != nil {
		return fmt.Errorf("tmux kill-window: %v", err)
	}
	if err := st.CloseThread(j.ID, tid, 0); err != nil {
		return err
	}
	util.LogEvent("jobs", "info", "thread-kill", fmt.Sprintf("%s: killed thread %s", j.Slug, tid))
	return nil
}

// windowOpener opens one thread window and returns tmux's `-P` line;
// core.NewWindowNamed in production and the single seam reviveClaimed takes.
// The env parameter is the spawning environment (cellSpawnEnv in production),
// carried through the seam rather than resolved behind it so a test can see
// which environment a revive would have opened the window with.
// The ordering this file has to guarantee — claim the row BEFORE anything is
// staged or spawned, undo the claim if the spawn then fails — is only
// observable by watching whether a window was opened at all, and no test in
// this repo may start a real tmux server.
type windowOpener func(session, dir, name string, env, argv []string) (string, error)

// ReviveThread resumes a killed thread's claude session in a fresh window of
// the cell — same session id, so the conversation continues exactly where it
// stopped, with no --fork-session (that would branch a THIRD session off it).
//
// A thread lives inside its job's cell, so it cannot outlive it: carl's ruling
// is that a gone cell means reactivate the job first, not silently rebuild a
// worktree here.
//
// Everything here is a refusal; the spawn itself is reviveClaimed, which opens
// with the claim that makes concurrent revives safe.
func ReviveThread(j *Job, tid string) (store.Thread, error) {
	if err := refuseCodexThread(j, "revive"); err != nil {
		return store.Thread{}, err
	}
	st, err := threadStore(j)
	if err != nil {
		return store.Thread{}, err
	}
	t, err := st.GetThread(j.ID, tid)
	if err != nil {
		return store.Thread{}, err
	}
	if t.SessionID == "" {
		return store.Thread{}, fmt.Errorf("thread %s of %s has no recorded session — nothing to resume", tid, j.Slug)
	}
	// FindCellSession, for BranchThread's reason: the revived window has to
	// open in the session that is actually running, which on a renamed cell
	// whose convergence failed is still the old name.
	sess, live := FindCellSession(j, LiveSessions())
	wt := threadWorktree(j)
	if !live || wt == "" {
		return store.Thread{}, fmt.Errorf("%s's cell is gone — reactivate the job, then revive", j.Slug)
	}
	// verify before acting, in the other direction: if this thread's claude is
	// still running anywhere, a second `claude -r` on its session id would put
	// two claudes in one transcript. This scan catches the common case with a
	// message that names the pane; the claim below is what actually arbitrates.
	all, _ := core.OrderAll(core.DiscoverAll())
	if p := liveThreadClaude(j, all, *t); p != nil {
		return store.Thread{}, fmt.Errorf("thread %s of %s is already live in pane %s — kill it before reviving", tid, j.Slug, p.Pane)
	}
	return reviveClaimed(st, j, *t, sess, wt, core.NewWindowNamed)
}

// reviveClaimed is the acting half of a revive, once every refusal has passed:
// CLAIM the row, then spawn. The order is the whole point.
//
// The discovery scan above is a snapshot, so on its own it is a pure TOCTOU:
// net/http serves handlers concurrently, and two `{op:"revive"}` requests for
// one tid can both scan (finding nothing), both open a window, and both run
// `claude -r <the same session id>` — two writers on one transcript, which
// this file's header calls unrecoverable. Nothing else in the path locks.
//
// So the DB is the arbiter. ClaimClosedThread flips closed→live and clears the
// stale pane in ONE compare-and-set: the winner proceeds, the loser gets
// ErrThreadNotClosed and returns a refusal having staged no file, opened no
// window, and started no claude. From the claim on, every failure path undoes
// it (the reservation shape BranchThread uses) so a failed revive leaves the
// row CLOSED and revivable, never live-with-no-pane — a live row with no pane
// would sit in the roll call's skip set with nothing to reconcile it away.
func reviveClaimed(st *store.Store, j *Job, t store.Thread, sess, wt string, open windowOpener) (store.Thread, error) {
	if err := st.ClaimClosedThread(j.ID, t.TID); err != nil {
		if errors.Is(err, store.ErrThreadNotClosed) {
			return store.Thread{}, fmt.Errorf("thread %s of %s is already live or being revived — kill it before reviving", t.TID, j.Slug)
		}
		return store.Thread{}, err
	}
	// before the window, not after: the claim already made the row live with no
	// pane, and the grace window is the only thing keeping this tick's
	// reconcile off it while the window is being opened.
	noteThreadSpawn(j.ID, t.TID)
	// t.Model, the alias the row was branched with: a revive that dropped it
	// would silently move the conversation to another tier mid-thread, which
	// is the one thing a resumed session must not do to carl.
	// t.Role and t.SessionID too, for the same reason: a revived mind thread
	// that came back briefed as an ask thread, or with no session in its env,
	// would lose the one write it exists to make.
	role := t.Role
	if role == "" {
		role = "ask" // a row from before roles existed
	}
	cmd, err := threadSpawnCommand(j, t.TID, t.Title, "", t.Model, role, t.SessionID, []string{"-r", t.SessionID})
	if err != nil {
		releaseReservedThread(st, j, t.TID)
		return store.Thread{}, err
	}
	// same reason as BranchThread's: a revived thread's window is opened by the
	// daemon, so its PATH has to be told about the toolchain
	at, err := open(sess, wt, t.TID, cellSpawnEnv(), []string{cmd})
	if err != nil {
		releaseReservedThread(st, j, t.TID)
		return store.Thread{}, fmt.Errorf("%s: open thread window: %v", j.Slug, err)
	}
	window, pane := parseWindowTarget(at)
	if pane == "" {
		killThreadWindow(window)
		releaseReservedThread(st, j, t.TID)
		return store.Thread{}, fmt.Errorf("%s: tmux reported no pane id for the revived thread window (%q)", j.Slug, at)
	}
	if err := st.SetThreadPane(j.ID, t.TID, pane, window); err != nil {
		killThreadWindow(pane)
		releaseReservedThread(st, j, t.TID)
		return store.Thread{}, err
	}
	t.State, t.Pane, t.Window, t.ClosedAt = "live", pane, window, 0
	util.LogEvent("jobs", "info", "thread-revive", fmt.Sprintf("%s: revived thread %s in %s", j.Slug, t.TID, pane))
	return t, nil
}

// RenameThread retitles a thread. Threads are named by carl, and the title is
// the only field he edits after the fork — it never touches the live pane, so
// it works on closed threads too.
func RenameThread(j *Job, tid, title string) error {
	st, err := threadStore(j)
	if err != nil {
		return err
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return fmt.Errorf("thread %s of %s: title cannot be empty", tid, j.Slug)
	}
	return st.SetThreadTitle(j.ID, tid, title)
}

// SendToThread types a message into a thread's pane, through the same
// verify-before-acting check KillThread runs (and then core.SendMessage's own
// checkSendTarget, which re-resolves the pane's process tree immediately before
// typing) — a message sent at a recycled pane would be typed at, and run by,
// whatever shell inherited it.
func SendToThread(j *Job, tid, text string) error {
	st, err := threadStore(j)
	if err != nil {
		return err
	}
	t, err := st.GetThread(j.ID, tid)
	if err != nil {
		return err
	}
	all, _ := core.OrderAll(core.DiscoverAll())
	p, err := threadTarget(j, *t, all, "revive it first")
	if err != nil {
		return err
	}
	return core.SendMessage(p, text)
}

// ReconcileThreads closes every live row of j whose thread is provably gone,
// using the daemon tick's OWN snapshots — the discovery peers and the
// LiveSessions map it already holds — so it costs one query per started job and
// no extra tmux or ps call.
//
// Without it a killed-by-hand thread would keep its pane in LiveThreadPanes
// forever, and that stale pane would go on being skipped by the roll call (and
// pointed at by kill/send) long after tmux recycled it. With it, but without
// threadSnapshotTrusted below, one failed `tmux list-panes` would do the
// opposite and far worse thing — close every live thread on the machine.
func ReconcileThreads(j *Job, all []*core.Peer, live map[string]bool) error {
	st := ActiveStore()
	if st == nil || j.ID == 0 {
		return nil
	}
	emptyFor := noteEmptySnapshot(len(all) == 0 && len(live) == 0, time.Now())
	// the session the cell is ACTUALLY live under, which after a rename whose
	// convergence has not landed yet is an older name. Handing the current
	// name to a check whose whole job is "is this snapshot solid enough to
	// close rows off" would answer "the session is gone, its windows went with
	// it" about a session that is running.
	sess := CellSession(j)
	if found, ok := FindCellSession(j, live); ok {
		sess = found
	}
	if !threadSnapshotTrusted(sess, all, live, emptyFor) {
		return nil // an untrustworthy snapshot closes nothing; the next tick retries
	}
	threads, err := st.ListThreads(j.ID)
	if err != nil {
		return err
	}
	for _, tid := range staleThreadTIDs(j, threads, all, time.Now()) {
		if err := st.CloseThread(j.ID, tid, 0); err != nil {
			return err
		}
		util.LogEvent("jobs", "info", "thread-closed", fmt.Sprintf("%s: thread %s is gone — row closed", j.Slug, tid))
	}
	return nil
}

// CloseThreadsForTeardown marks every live thread of j closed as its cell goes
// away — the whole tmux session is about to be killed, taking every thread
// window with it, so the rows must not go on claiming live panes.
//
// Nothing is snapshotted (carl's ruling: "no need to snapshot, just tear
// everything down"). Each row keeps its session id, and claude's own transcript
// keeps the conversation — a torn-down job's threads stay readable, they just
// have no pane any more.
func CloseThreadsForTeardown(j *Job) error {
	st := ActiveStore()
	if st == nil || j.ID == 0 {
		return nil
	}
	threads, err := st.ListThreads(j.ID)
	if err != nil {
		return err
	}
	for _, t := range threads {
		if t.State != "live" {
			continue
		}
		if err := st.CloseThread(j.ID, t.TID, 0); err != nil {
			return err
		}
	}
	return nil
}
