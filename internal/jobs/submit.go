package jobs

import "fmt"

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
// a git worktree, a tmux server, or the rotation gate's keychain — swap it,
// call SubmitJob, restore it. Nothing else in the binary may reassign it: the
// production value is StartCell and there is no second real start path.
var submitStartCell = StartCell

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
// Only then does it resolve the repo, and only then does it start. ResolveRepo
// is deliberately inside the refusal sequence rather than folded into
// StartCell: a bad `repo:` is a job-file mistake with a fixable message, and
// discovering it before any worktree/tmux/rotation work happens is what makes
// it a clean refusal rather than a half-built cell.
func SubmitJob(j *Job) error {
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
	return submitStartCell(j, repo)
}
