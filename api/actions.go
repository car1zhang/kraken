package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"

	"kraken/core"
	"kraken/jobs"
)

// --- board moves (the web UI's only writes; every one re-validates the
// transition server-side — the board in the browser is a stale snapshot) ---

type webReq struct {
	Slug string `json:"slug"`
	Text string `json:"text,omitempty"`
}

// webAction wraps a move handler with method/JSON plumbing and uniform
// {ok}/{error} responses.
func webAction(f func(webReq) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "POST only"})
			return
		}
		var req webReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "bad json"})
			return
		}
		if err := f(req); err != nil {
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		webBoardCache.invalidate()
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}
}

// webQueue is the draft→queued drag: flip ready in the job file. The daemon
// takes it from there.
func webQueue(req webReq) error {
	j, bySlug, err := webJobFor(req.Slug)
	if err != nil {
		return err
	}
	if j.Err != "" {
		return fmt.Errorf("%s is unparseable (%s) — fix the file first", j.Slug, j.Err)
	}
	if state := jobs.DisplayState(j, bySlug); state != "draft" {
		return fmt.Errorf("%s is %s — only drafts can be queued", j.Slug, state)
	}
	if err := jobs.WriteJobFields(j, "ready", "true"); err != nil {
		return err
	}
	return nil
}

// webDone is the blocked→done drag: complete the job, auto-filling the
// outcome from its success criterion (the exit criterion, per the board
// contract) since there's no crew-authored done marker anymore. Validation
// mirrors cmdDone (started, not already done) rather than the board's
// currently-displayed column: the board can be a tick fresher than the job
// file (see buildWebBoard's derive-ahead), so gating on a stale "blocked"
// read would 409 a legal drag. The board's column gating (drag only fires
// from BLOCKED) is the UX guard; this is just the server-side floor.
func webDone(req webReq) error {
	j, _, err := webJobFor(req.Slug)
	if err != nil {
		return err
	}
	if !j.Started() {
		return fmt.Errorf("%s hasn't started — nothing to complete", j.Slug)
	}
	if j.State == "done" {
		return fmt.Errorf("%s is already done", j.Slug)
	}
	outcome := "accepted"
	if j.Success != "" {
		outcome = "met: " + jobs.OneLine(j.Success)
	}
	_, err = jobs.DoneJob(j, core.TruncateOneLine(outcome, 200))
	return err
}

// webAnswerQuestion is the blocked→working move for an interactive
// AskUserQuestion prompt: the browser posts the 1-based option index as a
// single digit, delivered as a literal keystroke — that's how Claude Code's
// own selection dialog submits (the digit press IS the submit; a trailing
// Enter would fall through to whatever prompt follows).
func webAnswerQuestion(req webReq) error {
	j, _, err := webJobFor(req.Slug)
	if err != nil {
		return err
	}
	digit := strings.TrimSpace(req.Text)
	if len(digit) != 1 || digit[0] < '1' || digit[0] > '9' {
		return fmt.Errorf("answer must be a single digit 1-9")
	}
	if !j.Started() || j.State == "done" || !jobs.LiveSessions()[jobs.CellSession(j)] {
		return fmt.Errorf("%s has no live cell to answer", j.Slug)
	}
	all, _ := core.OrderAll(core.DiscoverAll())
	zeus, transcript := cellZeusTranscript(j, all)
	if zeus == nil {
		return fmt.Errorf("%s: zeus not found — k o %s to investigate", j.Slug, jobs.CellSession(j))
	}
	if transcript == "" {
		return fmt.Errorf("%s has no pending question — refresh the board", j.Slug)
	}
	pq := jobs.FindPendingQuestion(transcript)
	if pq == nil {
		return fmt.Errorf("%s has no pending question — refresh the board", j.Slug)
	}
	if !answerableSingleDigit(pq) {
		return fmt.Errorf("this question needs the pane — answer with k o %s", jobs.CellSession(j))
	}
	if err := sendDigit(zeus, digit); err != nil {
		return fmt.Errorf("send digit: %v", err)
	}
	if err := jobs.AppendMidRunLog(j, "me→zeus", "answered question: option "+digit); err != nil {
		return fmt.Errorf("delivered, but mid-run log append failed: %v", err)
	}
	return jobs.WriteJobState(j, "working")
}

// answerableSingleDigit reports whether a pending question can be answered by
// a single-digit board button: exactly one question, and it isn't
// multi-select. A multi-question or multi-select prompt packs more choice
// into the payload than one digit can carry — those need the actual pane.
func answerableSingleDigit(q *jobs.PendingQuestion) bool {
	return q != nil && len(q.Questions) == 1 && !q.Questions[0].MultiSelect
}

// sendDigit types a single literal keystroke into a pane with no trailing
// Enter — core.SendMessage() always appends Enter, which would double-submit past
// an AskUserQuestion dialog into whatever the agent does next. Verify before
// acting, mirroring sendMessage's own re-check right before typing — inverted,
// since sendDigit needs the question prompt to still be showing (sendMessage
// refuses when one is; here the digit is meaningless without one).
func sendDigit(p *core.Peer, digit string) error {
	if !core.PaneShowsQuestion(p.Pane) {
		return fmt.Errorf("the pane no longer shows the question — refresh the board")
	}
	return exec.Command("tmux", "send-keys", "-l", "-t", p.Pane, digit).Run()
}

// webRetry pokes an unhealthy cell agent; shared core in jobs.RetryCell.
func webRetry(req webReq) error {
	j, _, err := webJobFor(req.Slug)
	if err != nil {
		return err
	}
	return jobs.RetryCell(j)
}

// webSteer delivers a mid-run instruction directly to zeus — there's no
// intermediary anymore, so the message goes straight into its pane. Also
// appended to the job's mid-run log (the durable trail) and, on successful
// delivery, marks the job working (a steer is carl acting, which un-stalls
// a blocked cell same as an answered question would).
func webSteer(req webReq) error {
	j, _, err := webJobFor(req.Slug)
	if err != nil {
		return err
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		return fmt.Errorf("empty message")
	}
	if !j.Started() || j.State == "done" || !jobs.LiveSessions()[jobs.CellSession(j)] {
		return fmt.Errorf("%s has no live cell to steer", j.Slug)
	}
	all, _ := core.OrderAll(core.DiscoverAll())
	zeus := jobs.CellZeus(j, all)
	if zeus == nil {
		return fmt.Errorf("%s: zeus not found — k o %s to investigate", j.Slug, jobs.CellSession(j))
	}
	if err := jobs.AppendMidRunLog(j, "me→zeus", jobs.OneLine(text)); err != nil {
		return fmt.Errorf("mid-run log append failed: %v", err)
	}
	if err := core.SendMessage(zeus, "from carl: "+text); err != nil {
		return fmt.Errorf("logged, but delivery failed: %v", err)
	}
	return jobs.WriteJobState(j, "working")
}

// webReview is the PR-review handoff: open the cell's worktree in an editor
// to flash through the final diff. nvim gets a new tmux window in the cell
// session (LSP picks up the worktree root) raised in ghostty; VS Code opens
// the folder directly. req.Text carries the editor choice.
func webReview(req webReq) error {
	j, _, err := webJobFor(req.Slug)
	if err != nil {
		return err
	}
	repo, err := jobs.ResolveRepo(j.Repo)
	if err != nil {
		return fmt.Errorf("job %s: %v", j.Slug, err)
	}
	wt := jobs.ExistingWorktree(j, repo)
	if wt == "" {
		return fmt.Errorf("%s has no worktree to review (already torn down?)", j.Slug)
	}
	switch req.Text {
	case "code":
		if err := exec.Command("code", wt).Run(); err != nil {
			return fmt.Errorf("open VS Code: %v", err)
		}
		return nil
	default: // nvim
		sess := jobs.CellSession(j)
		if !jobs.LiveSessions()[sess] {
			return fmt.Errorf("cell session %s is gone — open the worktree manually: nvim %s", sess, wt)
		}
		if err := exec.Command("tmux", "new-window", "-t", sess+":", "-n", "review",
			"-c", wt, "nvim", ".").Run(); err != nil {
			return fmt.Errorf("tmux new-window: %v", err)
		}
		if _, err := core.RaiseSession(sess); err != nil {
			return fmt.Errorf("nvim is open in %s, but %v", sess, err)
		}
		return nil
	}
}

// webKill tears down a live job's cell and returns the job to draft. The
// browser confirms before posting; the server still validates the job is
// actually started and not done.
func webKill(req webReq) error {
	j, _, err := webJobFor(req.Slug)
	if err != nil {
		return err
	}
	if !j.Started() || j.State == "done" {
		return fmt.Errorf("%s has no live work to kill", j.Slug)
	}
	_, err = jobs.KillJob(j)
	return err
}

// webArchive moves a job into the archive. Server-side revalidation
// mirrors jobs.ArchiveJob's own checks; the error text spells out the kill→draft
// two-step for live jobs since that's the UX carl approved.
func webArchive(req webReq) error {
	j, _, err := webJobFor(req.Slug)
	if err != nil {
		return err
	}
	_, err = jobs.ArchiveJob(j, jobs.LiveSessions())
	return err
}

// webUnarchive restores an archived job (by archive file base) to the
// active dir under its original slug.
func webUnarchive(req webReq) error {
	a := jobs.ArchivedJobByName(jobs.LoadArchivedJobs(), req.Slug)
	if a == nil {
		return fmt.Errorf("no archived job %q", req.Slug)
	}
	return jobs.UnarchiveJob(a)
}

// webDelete permanently removes an archived job (file + archived artifact
// copies). The browser confirms before posting; only archive names ever
// resolve here, so nothing active can be deleted.
func webDelete(req webReq) error {
	a := jobs.ArchivedJobByName(jobs.LoadArchivedJobs(), req.Slug)
	if a == nil {
		return fmt.Errorf("no archived job %q", req.Slug)
	}
	return jobs.DeleteArchivedJob(a)
}

// webReactivate resumes a done job's zeus session for adjustments; req.Text
// carries carl's optional steering message into the resume prompt.
func webReactivate(req webReq) error {
	j, _, err := webJobFor(req.Slug)
	if err != nil {
		return err
	}
	return jobs.ReactivateJob(j, "", jobs.OneLine(req.Text))
}
