package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"kraken/internal/core"
	"kraken/internal/jobs"
	"kraken/internal/store"
	"kraken/internal/util"
)

// --- board moves (the web UI's only writes; every one re-validates the
// transition server-side — the board in the browser is a stale snapshot) ---

type webReq struct {
	Slug string `json:"slug"`
	Text string `json:"text,omitempty"`
	// ID is doc-question-only (POST /api/doc-question's question id, e.g.
	// "Q1"): additive, every other webAction handler simply ignores it.
	ID string `json:"id,omitempty"`
	// FromCell is the X-Kraken-From-Cell header, copied in by webAction and
	// never read from the body (`json:"-"`): the slug of the cell whose own
	// `kraken cell submit` made this request, or "" for the board, the phone
	// and a human at a terminal. webSubmit stamps the started job's parent
	// from it (spawn.go); every other action ignores it.
	FromCell string `json:"-"`
}

// docAnswerMaxLen caps a doc-question answer's length (after trim, counted
// in runes so multi-byte text isn't penalized), same cap convention
// ios/API.md already documents for last_sent/Detail: a board answer field
// isn't a place for pasted essays, and an unbounded answer gets re-served
// inside every /api/board payload for as long as the job is
// started+non-done (see athena-edge's C-E3).
const docAnswerMaxLen = 4000

// mirrorGatedActions names every webAction whose handler reaches into a
// REAL tmux pane, worktree, or agent process — found by reading every
// handler below for tmux/StartCell/teardownCell/core.SendMessage call
// sites, not just the tmux-driving actions a mirror-mode board button most
// obviously suggests:
//   - kill (webKill → KillJob → teardownCell), stop (webStop → InterruptCell),
//     steer (webSteer → core.SendMessage), question (webAnswerQuestion →
//     sendDigit), retry (webRetry → RetryCell → core.SendMessage), submit
//     (webSubmit → jobs.SubmitJob → StartCell) and reactivate
//     (webReactivate → ReactivateJob) all shell out to a live tmux session
//     or spawn/tear one down.
//   - done (webDone → DoneJob) force-tears-down the cell's tmux session and
//     worktree exactly like kill does — it is NOT in the brief's example
//     list of gated actions, but DoneJob's own force=true teardownCell call
//     makes it just as real a mutation as kill.
//   - archive (webArchive → ArchiveJob), unarchive (webUnarchive →
//     UnarchiveJob), and delete (webDelete → DeleteArchivedJob) are
//     conditionally gated (mirrorUploadsActions below): their rows live in
//     the mirror's own database, but ArchiveJob also removes the job's
//     uploads dir and releases its git worktree registration — real
//     filesystem effects outside that database.
//   - doc-question (webAnswerDocQuestion → store.AnswerQuestion) is now a
//     pure DB row UPDATE with no tmux/pane/file involved at all — gated
//     anyway, same "mirror board is read-only, full stop" policy the whole
//     table follows.
//
// There is no exception left. queue/dequeue used to be one: each only flipped
// the ready DB field, never touched a file outside the sandbox DB, and the
// mirror tick's own StartCell skip is what kept a mirror-queued job sitting
// harmlessly in the queued column. Submitting a job STARTS its cell in the
// request now — real worktree, real tmux session, a real account binding
// stamped on the job row and delivered into the cell's environment — so the
// property that made the exception safe is gone, and "submit" is gated like
// everything else that reaches the machine.
var mirrorGatedActions = map[string]bool{
	"submit":       true,
	"kill":         true,
	"stop":         true,
	"steer":        true,
	"question":     true,
	"retry":        true,
	"done":         true,
	"reactivate":   true,
	"doc-question": true,
}

// mirrorUploadsActions run in mirror mode only while KRAKEN_UPLOADS_DIR
// points away from the default uploads dir (jobs.UploadsSandboxed): archive
// removes the job's uploads dir, so on an unsandboxed mirror it would delete
// the live deployment's images. The mirror runbook sets the variable, so a
// correctly sandboxed mirror keeps these actions for parity testing. (The
// worktree release ArchiveJob also performs refuses on a dirty tree and
// touches nothing for a job whose worktree is gone; it is not gated
// separately.)
var mirrorUploadsActions = map[string]bool{
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
		if mirrorUploadsActions[name] && mirrorModeOn() && !jobs.UploadsSandboxed() {
			http.Error(w, "disabled in mirror mode (KRAKEN_UPLOADS_DIR not sandboxed)", http.StatusForbidden)
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
		req.FromCell = strings.TrimSpace(r.Header.Get(fromCellHeader))
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

// submitJobFn is the start seam for this layer, mirroring jobs.SubmitJob's
// own submitStartCell: a handler test swaps it so the whole route — routing,
// mirror gate, slug lookup, the webAction wrapper's 409 encoding — can be
// exercised without a git worktree, a tmux server, or the account binder. The
// production value is jobs.SubmitJob and there is no second real start path.
var submitJobFn = jobs.SubmitJob

// webSubmit is the draft→working drag, and it is the whole start surface: it
// starts the job's cell inside this request and only answers once the cell is
// up or the attempt has failed.
//
// It used to be webQueue, which flipped a `ready` flag and left the daemon's
// queue pass to notice up to a tick later — so a 200 meant "the flag is set",
// and whether a cell ever came up was a separate question answered on some
// later poll, through a side channel (webJob.start_error) invented precisely
// because the answer was otherwise invisible. Now a 200 means the cell is
// live and the job reads working, and a 409 carries the exact reason with the
// job still draft. There is nothing left to publish out of band.
//
// The route is still /api/queue (Q4): shipped iOS builds post to that URL for
// their draft→queued drag, and keeping it is what makes those builds start
// jobs correctly against a server that no longer has a queued column.
//
// Every refusal lives in jobs.SubmitJob rather than here, and its error text
// surfaces verbatim — the viewer's `s` key and the daemon's own dream-job
// start go through the same function, and a validation split across the
// callers is exactly how the old queue action came to refuse an empty spec
// while the other two start paths did not.
func webSubmit(req webReq) error {
	j, _, err := webJobFor(req.Slug)
	if err != nil {
		return err
	}
	if err := submitJobFn(j); err != nil {
		return err
	}
	// The cell is up and the job reads working: a submit that came from
	// another cell's `kraken cell submit` records that cell as the parent
	// (spawn.go). After the start, never before — a refused submit stamps
	// nothing — and the stamp cannot fail the submit.
	stampSpawn(j, req.FromCell, jobs.SpawnViaSubmit)
	return nil
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
	clearDraft(j.ID)
	return nil
}

// cellSessionIfLive answers both halves of the question the steer, stop and
// answer-question routes ask before they act: is this job's cell actually
// running, and under WHICH tmux session name.
//
// jobs.FindCellSession rather than LiveSessions()[jobs.CellSession(j)]: a job
// renamed mid-run keeps running in the session it spawned under until the
// rename's tmux convergence lands (jobs/rename.go), and where convergence
// failed outright it stays there. Reading that cell as gone would refuse
// carl's steer, his Stop and his answer on a job the board shows as working —
// a refusal with no way out but a hand-typed tmux rename. The name it returns
// is the one to quote back in a hint, since it is the session that exists.
//
// The started/done test runs first so a job with no cell at all still costs no
// tmux call, exactly as the inline condition it replaced did.
func cellSessionIfLive(j *jobs.Job) (string, bool) {
	if !j.Started() || j.State == "done" {
		return "", false
	}
	return jobs.FindCellSession(j, jobs.LiveSessions())
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
	sess, cellLive := cellSessionIfLive(j)
	if !cellLive {
		return fmt.Errorf("%s has no live cell to answer", j.Slug)
	}
	all, _ := core.OrderAll(core.DiscoverAll())
	zeus, transcript := cellZeusTranscript(j, all)
	// the FOUND session name in every hint, not CellSession(j): on a renamed
	// cell whose tmux convergence has not landed, the current name is a session
	// that does not exist, so `k o <that>` would fail for carl at exactly the
	// moment the message is telling him to go look. jobs.CellZeus already
	// elects across the same candidate set, so the two agree.
	if zeus == nil {
		return fmt.Errorf("%s: zeus not found — k o %s to investigate", j.Slug, sess)
	}
	if transcript == "" {
		return fmt.Errorf("%s has no pending question — refresh the board", j.Slug)
	}
	pq := jobs.FindPendingQuestion(transcript)
	if pq == nil {
		return fmt.Errorf("%s has no pending question — refresh the board", j.Slug)
	}
	if !answerableSingleDigit(pq) {
		return fmt.Errorf("this question needs the pane — answer with k o %s", sess)
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

// webAnswerDocQuestion is the doc-questions panel's answer submit: a plain
// row UPDATE of answer/answered_at on the matching `questions` row in
// kraken.db (store.AnswerQuestion). Unlike webAnswerQuestion (the
// interactive AskUserQuestion digit-press), this needs NO live agent and no
// live CELL either (Q13): the row is the record, live or done, so
// re-answering is always allowed and there is no "cell is gone — read-only"
// refusal.
func webAnswerDocQuestion(req webReq) error {
	j, _, err := webJobFor(req.Slug)
	if err != nil {
		return err
	}
	id := strings.TrimSpace(req.ID)
	if id == "" {
		return fmt.Errorf("missing question id")
	}
	// store.AnswerQuestion performs no trimming or length validation itself
	// — it's a plain UPDATE — so these checks are the only enforcement, and
	// the TRIMMED value (not req.Text) is what must actually get stored
	// below, or the cap above is trivially bypassable with padding.
	text := strings.TrimSpace(req.Text)
	if text == "" {
		return fmt.Errorf("answer must not be empty")
	}
	if n := len([]rune(text)); n > docAnswerMaxLen {
		return fmt.Errorf("answer too long (%d chars, max %d)", n, docAnswerMaxLen)
	}
	st := jobs.ActiveStore()
	if st == nil {
		return fmt.Errorf("no store wired")
	}
	if err := st.AnswerQuestion(j.ID, id, text, time.Now().UTC().Format(time.RFC3339)); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("no such question %q", id)
		}
		return err
	}
	return nil
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
	sess, cellLive := cellSessionIfLive(j)
	if !cellLive {
		return fmt.Errorf("%s has no live cell to steer", j.Slug)
	}
	all, _ := core.OrderAll(core.DiscoverAll())
	zeus := jobs.CellZeus(j, all)
	if zeus == nil {
		// the found name, not CellSession(j) — see webAnswerQuestion
		return fmt.Errorf("%s: zeus not found — k o %s to investigate", j.Slug, sess)
	}
	if err := core.SendMessage(zeus, text); err != nil {
		return fmt.Errorf("delivery failed: %v", err)
	}
	if err := jobs.WriteJobState(j, "working"); err != nil {
		return err
	}
	clearDraft(j.ID)
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
	clearDraft(j.ID)
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
	// a.ID, not req.Slug: req.Slug here is the ARCHIVE NAME, which was never a
	// draft key even under the old slug-keyed store — and if that name happened
	// to equal an ACTIVE job's slug (`foo` archived as `foo`, a new `foo`
	// started since), this line wiped the live job's composer instead.
	clearDraft(a.ID)
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
	clearDraft(j.ID)
	return nil
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
	if _, cellLive := cellSessionIfLive(j); !cellLive {
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

var reorderColumns = map[string]bool{"draft": true, "working": true, "blocked": true, "done": true}

// webReorderHandler persists a manual drag-reorder within one board column.
// Posted slugs are filtered to those whose CURRENT DisplayState actually
// matches the column before saving — a concurrent transition (a draft
// submitted mid-drag, say) just silently drops that slug from the stored
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
		if j := bySlug[s]; j != nil && jobs.DisplayState(j) == req.Column {
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
