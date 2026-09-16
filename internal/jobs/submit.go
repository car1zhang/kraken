package jobs

import (
	"fmt"
	"sync"

	"kraken/internal/core"
)

// submit.go — SubmitJob, the ONE start surface.
//
// There used to be three: the board's queue action flipped a `ready` flag and
// left the daemon's queue pass to notice it a tick later, the board's start
// action started a queued job by hand, and the viewer's `s` key did the same
// from the TUI. Each carried its own copy of the validation, and they did not
// agree — the queue action refused an empty spec, the other two did not, so a
// job with no spec could start a cell with an empty prompt from two of the
// three doors. Submitting IS starting now, so the validation has exactly one
// home and every caller (POST /api/queue, the viewer's `s`, the dream and
// graph tick creators) goes through it.
//
// SubmitJob writes NO job state of its own. StartCell owns the `working`
// write, and it makes it only once the cell is actually up — so a refusal
// here, and a StartCell that fails partway, both leave the job exactly as
// draft as it was before the request.

// submitStartCell is the start seam. It exists so a package test can assert
// SubmitJob's validation order and its "starts exactly once" contract without
// a git worktree, a tmux server, or the account binder's keychain — swap it,
// call SubmitJob, restore it. Nothing else in the binary may reassign it: the
// production value is StartCell and there is no second real start path.
var submitStartCell = StartCell

// submitLocks serializes SubmitJob per job id. The refusal ladder below is
// check-then-act with a seconds-wide gap: validation reads the job as a
// startable draft, StartCell's own already-live guard only turns true once
// `tmux new-session` has run, and the job's stored state flips later still,
// at StartCell's closing WriteJobState — so two near-simultaneous submits of
// one job (a client double-fire, two clients of one serve, a double tap)
// both passed every check and raced inside StartCell's `git worktree add`,
// where the loser died inside that command (the exact git text varies with
// arrival offset — `cannot lock ref` is the one instance measured) and
// answered first with a 409 that read as the start failing while the
// winner's cell came up. Under the lock the loser instead waits, then hits
// StartCell's already-live refusal — an honest answer. It is StartCell's
// refusal rather than the ladder's own already-started one because each
// request parses its own *Job: the loser's copy still says draft when it
// wakes. The ladder sits inside the lock simply because it is microseconds
// of reads on the caller's own copy — moving it out would buy nothing, and a
// future rung that reads shared state is covered by construction. The lock
// is per-PROCESS: the viewer's `s` calls SubmitJob from the `k v` process,
// outside this map, where only StartCell's live-session check covers it — a
// viewer-vs-board race on one job within the winner's start window remains
// open, accepted as a two-hands rarity. Keyed by the
// job's immutable id, not slug, so one job's multi-second start never delays
// a different job's; ids without a store row share key 0, which is harmless
// because StartCell's checkCellIdentity refuses those before any side
// effect. Entries are never removed: a *sync.Mutex per job ever submitted is
// a few dozen bytes in a process that restarts routinely.
var submitLocks sync.Map // job id (int64) → *sync.Mutex

func submitLock(id int64) *sync.Mutex {
	mu, _ := submitLocks.LoadOrStore(id, &sync.Mutex{})
	return mu.(*sync.Mutex)
}

// SubmitJob validates a job and starts its cell, synchronously. A nil return
// means the cell is up and the job reads `working`; any error means nothing
// was started and the job is untouched.
//
// The refusals run in a fixed order, cheapest-and-most-fundamental first, so
// the reason a caller is shown is the one that would still be true after
// fixing every later one:
//
//  1. unparseable — the file's own text is broken, so nothing else it says
//     (state, repo, spec) can be trusted.
//  2. done — a finished job is the record of what shipped; restarting it is
//     reactivate's job, not submit's.
//  3. already started — working or blocked. The cell exists (or existed);
//     kill returns it to draft if carl really wants a fresh one.
//  4. empty spec — the one refusal the old auto-start path never made, which
//     is exactly how a cell could come up with an empty prompt.
//
// Only then does it resolve the repo, then — for a codex job — the codex
// launcher (codexPreflight), and only then does it start. Both resolutions
// are deliberately inside the refusal sequence rather than folded into
// StartCell: a bad `repo:` is a job-file mistake with a fixable message and a
// missing or stale codex is a machine mistake with one, and discovering
// either before any worktree/tmux/rotation work happens is what makes it a
// clean refusal rather than a half-built cell.
func SubmitJob(j *Job) error {
	mu := submitLock(j.ID)
	mu.Lock()
	defer mu.Unlock()
	if j.Err != "" {
		return fmt.Errorf("%s is unparseable (%s) — fix the file first", j.Slug, j.Err)
	}
	if j.State == "done" {
		return fmt.Errorf("%s is done — reactivate it instead of submitting it again", j.Slug)
	}
	if j.Started() {
		return fmt.Errorf("%s has already started (%s) — kill it first if you want a fresh cell", j.Slug, j.State)
	}
	if jobPrompt(j) == "" {
		return fmt.Errorf("%s has an empty spec — write one before submitting", j.Slug)
	}
	repo, err := ResolveRepo(j.Repo)
	if err != nil {
		return fmt.Errorf("job %s: %v", j.Slug, err)
	}
	if err := codexPreflight(j); err != nil {
		return fmt.Errorf("job %s: %v", j.Slug, err)
	}
	return submitStartCell(j, repo)
}

// codexPreflight is the ladder's last rung, for codex jobs only: the launcher
// the cell would spawn on must resolve (core.ResolveCodex — at or above the
// version floor) BEFORE any cell dir, worktree or tmux work exists, so a
// stale codex is a refusal here rather than a cell that comes up and dies
// (ResolveCodex's doc has the incident). A claude job never probes codex,
// whatever KRAKEN_CODEX says: the probe execs a binary, and a claude submit
// must not depend on one it will never run.
func codexPreflight(j *Job) error {
	if j.Agent != "codex" {
		return nil
	}
	_, err := core.ResolveCodex()
	return err
}
