package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"

	"kraken/internal/core"
	"kraken/internal/jobs"
	"kraken/internal/util"
)

// --- board moves (the web UI's only writes; every one re-validates the
// transition server-side — the board in the browser is a stale snapshot) ---

type webReq struct {
	Slug string `json:"slug"`
	Text string `json:"text,omitempty"`
}

// mirrorGatedActions names every webAction whose handler reaches into a
// REAL tmux pane, worktree, or agent process — found by reading every
// handler below for tmux/StartCell/teardownCell/core.SendMessage call
// sites, not just the tmux-driving actions a mirror-mode board button most
// obviously suggests:
//   - kill (webKill → KillJob → teardownCell), stop (webStop → InterruptCell),
//     steer (webSteer → core.SendMessage), question (webAnswerQuestion →
//     sendDigit), retry (webRetry → RetryCell → core.SendMessage), start
//     (webStart → StartCell) and reactivate (webReactivate → ReactivateJob)
//     all shell out to a live tmux session or spawn/tear one down.
//   - done (webDone → DoneJob) force-tears-down the cell's tmux session and
//     worktree exactly like kill does — it is NOT in the brief's example
//     list of gated actions, but DoneJob's own force=true teardownCell call
//     makes it just as real a mutation as kill.
//   - archive (webArchive → ArchiveJob), unarchive (webUnarchive →
//     UnarchiveJob), and delete (webDelete → DeleteArchivedJob) are
//     conditionally gated (mirrorPlutoActions below): all three call
//     movePrefixed/removeArchivedArtifactFiles against PlutoDir (and its
//     archive/ subdir) — preserved artifact flat-copies live as plain
//     files on disk regardless of storage mode (see jobs/archive.go) — so
//     they run in mirror mode only when PlutoDir is a sandbox copy.
//
// webQueue and webDequeue stay UNGATED: each only flips the ready DB field
// and never touches any file outside the sandbox DB. Under mirror the tick
// never starts a queued job (jobs.ServeOpts.Mirror skips StartCell), so a
// mirror-queued job simply sits in the queued column — which is exactly
// what draft→queued parity testing needs to show.
var mirrorGatedActions = map[string]bool{
	"start":      true,
	"kill":       true,
	"stop":       true,
	"steer":      true,
	"question":   true,
	"retry":      true,
	"done":       true,
	"reactivate": true,
}

// mirrorPlutoActions are DB-local moves whose only real-world side effect
// is preserved-artifact file moves under PlutoDir (see the archive bullet
// above). They're allowed in mirror mode when — and only when — PlutoDir is
// sandboxed (KRAKEN_PLUTO_DIR set away from the real ~/Documents/pluto,
// mandatory in the mirror runbook): the file moves then land in the sandbox
// copy, which is exactly the parity behavior worth testing.
var mirrorPlutoActions = map[string]bool{
	"archive":   true,
	"unarchive": true,
	"delete":    true,
}

// webAction wraps a move handler with method/JSON plumbing and uniform
// {ok}/{error} responses. name identifies the action in the unified event
// log: every manual board move — success or refusal — leaves one entry.
// The mirror gates short-circuit before any of that — including before the
// request body is even decoded — so a mirror-mode board can never reach f
// at all for those actions.
func webAction(name string, f func(webReq) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if mirrorGatedActions[name] && mirrorModeOn() {
			http.Error(w, "disabled in mirror mode", http.StatusForbidden)
			return
		}
		if mirrorPlutoActions[name] && mirrorModeOn() && !jobs.PlutoSandboxed() {
			http.Error(w, "disabled in mirror mode (KRAKEN_PLUTO_DIR not sandboxed)", http.StatusForbidden)
			return
		}
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
			util.LogEvent("web", "error", name, name+" "+req.Slug+": "+err.Error())
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		util.LogEvent("web", "info", name, name+" "+req.Slug)
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
// mirrors the pre-Q14 CLI's `k done` validation (started, not already done)
// rather than the board's currently-displayed column: the board can be a tick fresher than the job
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
// invokes a real slash command in zeus' Claude session (e.g. /do) — that's
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

// webStart boots a queued job's cell immediately by hand — the board's
// manual start action: same validation (queued, prereqs met), same
// StartCell mechanics the daemon's own auto-start uses. Since this is carl
// acting by hand, it doubles as the manual release for a gated prereq
// (UnmetPrereqs only counts a dependency as unmet while it isn't done —
// being gated doesn't block this path, only the daemon's own startable()
// auto-start check) and bypasses the daemon's CellCap entirely.
func webStart(req webReq) error {
	j, bySlug, err := webJobFor(req.Slug)
	if err != nil {
		return err
	}
	if state := jobs.DisplayState(j, bySlug); state != "queued" {
		return fmt.Errorf("%s is %s — only queued jobs can be started", j.Slug, state)
	}
	if unmet := jobs.UnmetPrereqs(j, bySlug); len(unmet) > 0 {
		var names []string
		for _, p := range unmet {
			names = append(names, p.Job)
		}
		return fmt.Errorf("%s has unmet prereqs: %s", j.Slug, strings.Join(names, ", "))
	}
	repo, err := jobs.ResolveRepo(j.Repo)
	if err != nil {
		return fmt.Errorf("job %s: %v", j.Slug, err)
	}
	return jobs.StartCell(j, repo)
}

// webStop interrupts a working cell: one Escape to every in-flight agent
// pane (jobs.InterruptCell), stopping short of a teardown — the daemon's own
// roll-call reads the idled cell as blocked on its next tick, no job state
// is written here.
func webStop(req webReq) error {
	j, _, err := webJobFor(req.Slug)
	if err != nil {
		return err
	}
	if !j.Started() || j.State == "done" || !jobs.LiveSessions()[jobs.CellSession(j)] {
		return fmt.Errorf("%s has no live cell to stop", j.Slug)
	}
	all, _ := core.OrderAll(core.DiscoverAll())
	return jobs.InterruptCell(j, all)
}

// reorderReq is POST /api/reorder's request shape — distinct from webReq
// since a manual board reorder posts a column + the full slug order within
// it, not a single-job slug/text move.
type reorderReq struct {
	Column string   `json:"column"`
	Slugs  []string `json:"slugs"`
}

var reorderColumns = map[string]bool{"draft": true, "queued": true, "working": true, "blocked": true, "done": true}

// webReorderHandler persists a manual drag-reorder within one board column.
// Posted slugs are filtered to those whose CURRENT DisplayState actually
// matches the column before saving — a concurrent transition (a queued job
// started mid-drag, say) just silently drops that slug from the stored
// order rather than 409ing the whole reorder.
func webReorderHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "POST only"})
		return
	}
	var req reorderReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "bad json"})
		return
	}
	if !reorderColumns[req.Column] {
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("unknown column %q", req.Column)})
		return
	}
	js := jobs.LoadJobs()
	bySlug := map[string]*jobs.Job{}
	for _, j := range js {
		bySlug[j.Slug] = j
	}
	filtered := make([]string, 0, len(req.Slugs))
	for _, s := range req.Slugs {
		if j := bySlug[s]; j != nil && jobs.DisplayState(j, bySlug) == req.Column {
			filtered = append(filtered, s)
		}
	}
	if err := jobs.SaveColumnOrder(req.Column, filtered); err != nil {
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	webBoardCache.invalidate()
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}
