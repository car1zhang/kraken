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
	spec, _ := splitBodyLog(j.Body)
	if strings.TrimSpace(spec) == "" {
		return fmt.Errorf("%s has an empty spec — write one before queueing", j.Slug)
	}
	if err := jobs.WriteJobFields(j, "ready", "true"); err != nil {
		return err
	}
	return nil
}

// webDequeue is the queued→draft reverse move: it's legal precisely because
// no cell exists yet for a queued-but-not-started job, so there's nothing to
// tear down — just flip ready back off. The server re-derives the display
// state itself rather than trusting the browser's (possibly stale) column,
// so a board that hasn't refreshed can't cancel a job that has since
// actually started; the 409 in that case names kill as the correct recovery
// instead. Note DisplayState collapses a ready job still waiting on unmet or
// gated prereqs to "queued" too — those are dequeueable by the same logic,
// since they likewise have no cell yet.
func webDequeue(req webReq) error {
	j, bySlug, err := webJobFor(req.Slug)
	if err != nil {
		return err
	}
	if j.Err != "" {
		return fmt.Errorf("%s is unparseable (%s) — fix the file first", j.Slug, j.Err)
	}
	state := jobs.DisplayState(j, bySlug)
	if j.Started() && j.State != "done" {
		return fmt.Errorf("%s already started — kill instead", j.Slug)
	}
	if state != "queued" {
		return fmt.Errorf("%s is %s — only queued jobs can be cancelled", j.Slug, state)
	}
	return jobs.WriteJobFields(j, "ready", "false")
}

// webDone is the blocked→done drag: complete the job with the fixed
// outcome "accepted" — there's no crew-authored done marker and no
// success criterion anymore, accepting IS the exit criterion. Validation
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
	if _, err := jobs.DoneJob(j, "accepted"); err != nil {
		return err
	}
	clearDraft(j.Slug)
	return nil
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
// intermediary anymore, so the message goes straight into its pane exactly
// as typed, with no "from carl:" prefix. That means text starting with "/"
// invokes a real slash command in zeus' Claude session (e.g. /send) — that's
// intentional, not a leak. The transcript is the record now; on successful
// delivery the job is marked working (a steer is carl acting, which
// un-stalls a blocked cell same as an answered question would).
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
	if err := core.SendMessage(zeus, text); err != nil {
		return fmt.Errorf("delivery failed: %v", err)
	}
	if err := jobs.WriteJobState(j, "working"); err != nil {
		return err
	}
	clearDraft(j.Slug)
	return nil
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
	if _, err := jobs.ArchiveJob(j, jobs.LiveSessions()); err != nil {
		return err
	}
	clearDraft(j.Slug)
	return nil
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
	if err := jobs.DeleteArchivedJob(a); err != nil {
		return err
	}
	clearDraft(req.Slug)
	return nil
}

// webReactivate resumes a done job's zeus session for adjustments; req.Text
// carries carl's optional steering message into the resume prompt.
func webReactivate(req webReq) error {
	j, _, err := webJobFor(req.Slug)
	if err != nil {
		return err
	}
	if err := jobs.ReactivateJob(j, "", jobs.OneLine(req.Text)); err != nil {
		return err
	}
	clearDraft(j.Slug)
	return nil
}
