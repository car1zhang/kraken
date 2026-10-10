package jobs

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"kraken/internal/core"
)

// status.go — cell status, derived by kraken rather than declared by any
// agent. There is no status file any agent writes: working/blocked is read
// off a live ROLL-CALL of the cell's pane agents and team members (see
// cellActive), and done is explicit. Pending interactive questions are
// likewise pulled from the zeus session's own transcript.

// --- activity ---

// cellActive reports whether a cell is doing live work right now: PURE, so
// it's table-testable — no exec, no file reads. peers is the full discovery
// snapshot (orderAll's result); members is the cell's team roster (loaded by
// the caller via teamMembersForLead, "" sessions -> nil); paneQuestion checks
// whether a pane is showing an AskUserQuestion dialog right now (the caller
// passes paneShowsQuestion; only consulted for peers whose Status is
// "running", to keep the tmux capture-pane calls at the call site minimal);
// skipPanes is the set of panes that must NOT vote (nil/empty = nobody).
//
// skipPanes exists for SIDE THREADS (thread.go). A thread is a forked claude
// running in its own window of the cell's tmux session — so it matches point 1
// exactly, and a carl sitting in a thread asking questions would hold the job
// in "working" and hide the very block he branched to investigate. The panes of
// live thread rows are passed in here so they abstain from the roll call
// entirely. They are excluded only from point 1: a thread is never a team
// member, so point 2's roster can't name one.
//
// Active iff either:
//  1. a pane agent in the cell (TmuxSession == sess, claude or codex) is
//     "running" or "polling", isn't in skipPanes, AND its pane isn't showing an
//     open question —
//     a question is waiting-for-carl, i.e. blocked, even though the session
//     file stays "busy" while the AskUserQuestion tool call is in flight.
//     "polling" means the agent is mid poll-loop (a pending ScheduleWakeup),
//     idle between ticks by design rather than genuinely waiting at its
//     prompt — it counts as live work, same as "running".
//  2. a live non-lead teammate, cross-checked by backend: a tmux-backed
//     member counts only if its pane is currently occupied by a peer whose
//     Status is "running" — mere pane existence would also match an idle
//     teammate sitting at its prompt, which is the exact false-WORKING bug
//     this function exists to prevent. An in-process member counts only if
//     its config entry has IsActive explicitly true: it has no pane to
//     cross-check (it lives inside zeus' own process), so isActive is the
//     only liveness signal available, and an absent value must not default
//     to active. Either way isActive:false (finished without being removed
//     from the config) is filtered up front.
func cellActive(sess string, peers []*core.Peer, members []core.TeamMember, paneQuestion func(string) bool, skipPanes map[string]bool) bool {
	for _, p := range peers {
		if p.TmuxSession != sess || (p.Status != "running" && p.Status != "polling") {
			continue
		}
		if skipPanes[p.Pane] {
			continue // a side thread: carl asking questions, not the job working
		}
		if p.Agent != "claude" && p.Agent != "codex" {
			// codex peers rarely carry a "running" status today, so a
			// codex-only cell may read blocked here — acceptable since
			// cells run a claude zeus.
			continue
		}
		// refineStatus is the real gate (it sets Status to "question" the
		// moment it sees an open AskUserQuestion, which already fails the
		// running/polling check above); this callback is belt-and-braces for
		// a capture-pane race between that refinement and this call.
		if paneQuestion != nil && paneQuestion(p.Pane) {
			continue
		}
		return true
	}
	runningByPane := map[string]bool{}
	for _, p := range peers {
		if p.Pane != "" && p.Status == "running" {
			runningByPane[p.Pane] = true
		}
	}
	for _, m := range members {
		if strings.HasPrefix(m.AgentID, "team-lead@") || m.TmuxPaneID == "leader" {
			continue // the lead is zeus itself, not a subagent doing work
		}
		if m.IsActive != nil && !*m.IsActive {
			continue // finished without being removed from the config
		}
		switch m.BackendType {
		case "in-process":
			// no pane to cross-check against: isActive is the only signal,
			// so require it explicitly true rather than defaulting an
			// absent flag to active. Getting this wrong true->false just
			// costs a spurious notification (self-correcting); wrong
			// false->true wedges the cell reading "working" forever.
			if m.IsActive != nil && *m.IsActive {
				return true
			}
		case "tmux":
			if m.TmuxPaneID != "" && runningByPane[m.TmuxPaneID] {
				return true
			}
		}
	}
	return false
}

// --- pending questions ---

// A PendingQuestion is an AskUserQuestion tool call awaiting an answer: the
// tool_use appears in the zeus transcript with no later tool_result for its
// id.
type PendingQuestion struct {
	ToolUseID string         `json:"tool_use_id"`
	Questions []QuestionItem `json:"questions"`
}

type QuestionItem struct {
	Question    string           `json:"question"`
	Header      string           `json:"header"`
	MultiSelect bool             `json:"multiSelect"`
	Options     []QuestionOption `json:"options"`
}

type QuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

// transcriptEvent is the tiny slice of a transcript jsonl line pendingQuestion
// cares about: an assistant tool_use (possibly AskUserQuestion) or a user
// tool_result answering one. Everything else in a real event is ignored by
// virtue of not being in this struct.
type transcriptEvent struct {
	Type    string `json:"type"`
	Message struct {
		Content []struct {
			Type      string          `json:"type"`
			ID        string          `json:"id"`
			Name      string          `json:"name"`
			Input     json.RawMessage `json:"input"`
			ToolUseID string          `json:"tool_use_id"`
		} `json:"content"`
	} `json:"message"`
}

// pendingQuestionCache memoizes pendingQuestion results per transcript path,
// keyed on (size, mtime) via os.Stat: the board/TUI poll this against
// multi-MB transcripts on every tick, and re-scanning unchanged files on
// every poll is wasted work. Follows the spirit of discover.go's cwdCache.
var (
	pendingQuestionCacheMu sync.Mutex
	pendingQuestionCache   = map[string]pendingQuestionCacheEntry{}
)

type pendingQuestionCacheEntry struct {
	size  int64
	mtime time.Time
	q     *PendingQuestion
}

// pendingQuestion scans a session transcript for an unanswered
// AskUserQuestion call. Lenient throughout: an unreadable file, malformed
// lines, or no pending call all read as no pending question, never an error.
// Results are memoized on (size, mtime); an unchanged file returns the cached
// verdict without rescanning.
//
// Algorithm: stream the transcript line by line, remembering only the LAST
// AskUserQuestion tool_use seen (id + decoded questions) — each new one
// supersedes the last, since only the most recent question can still be
// open. A tool_result naming that same id marks it answered; a tool_result
// for any other id is irrelevant (it answered a since-superseded question,
// or something else entirely) and is ignored. Whatever the last call's
// answered state is when the file ends is the verdict.
func FindPendingQuestion(transcript string) *PendingQuestion {
	info, statErr := os.Stat(transcript)
	if statErr == nil {
		pendingQuestionCacheMu.Lock()
		if e, ok := pendingQuestionCache[transcript]; ok && e.size == info.Size() && e.mtime.Equal(info.ModTime()) {
			pendingQuestionCacheMu.Unlock()
			return e.q
		}
		pendingQuestionCacheMu.Unlock()
	}

	// A codex zeus writes a rollout under <its codex home>/sessions/, not a
	// claude JSONL transcript, so its pending-question shape
	// (request_user_input) is scanned differently. Dispatch by path exactly as
	// core.LastBlocks does, so the caller (board.go) hands us one path and
	// stays agent-agnostic.
	//
	// core.IsCodexRollout rather than a "/.codex/sessions/" substring: a cell
	// bound to a codex pool slot writes its rollouts under
	// ~/.codex-pool/accounts/<slug>/sessions/, which that substring does not
	// match — the bound cell's pending questions would have been scanned as
	// claude JSONL and never found.
	var q *PendingQuestion
	if core.IsCodexRollout(transcript) {
		q = scanPendingQuestionCodex(transcript)
	} else {
		q = scanPendingQuestion(transcript)
	}

	if statErr == nil {
		pendingQuestionCacheMu.Lock()
		pendingQuestionCache[transcript] = pendingQuestionCacheEntry{size: info.Size(), mtime: info.ModTime(), q: q}
		pendingQuestionCacheMu.Unlock()
	}
	return q
}

// scanPendingQuestion does the actual line-by-line transcript scan behind
// pendingQuestion's cache.
func scanPendingQuestion(transcript string) *PendingQuestion {
	f, err := os.Open(transcript)
	if err != nil {
		return nil
	}
	defer f.Close()

	var lastID string
	var lastQuestions []QuestionItem
	answered := false

	sc := bufio.NewScanner(f)
	// transcript lines can run to hundreds of KB, even several MB (large tool
	// inputs/outputs inline, e.g. an 8.3MB line seen in real transcripts);
	// bufio's 64KB default silently truncates scanning, so give it real
	// headroom.
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var ev transcriptEvent
		if json.Unmarshal(line, &ev) != nil {
			continue // malformed line: skip it, don't lose the rest of the transcript
		}
		switch ev.Type {
		case "assistant":
			for _, c := range ev.Message.Content {
				if c.Type != "tool_use" || c.Name != "AskUserQuestion" {
					continue
				}
				// newest ask always wins: update the tracked id (and reset
				// questions) even if the input fails to decode, so a
				// malformed newer call can never leave a superseded older
				// question looking pending — it just yields nil instead.
				lastID = c.ID
				lastQuestions = nil
				answered = false // a new question call supersedes any prior answered state
				var input struct {
					Questions []QuestionItem `json:"questions"`
				}
				if json.Unmarshal(c.Input, &input) != nil {
					continue // malformed input: not a usable question, leave lastQuestions nil
				}
				lastQuestions = input.Questions
			}
		case "user":
			for _, c := range ev.Message.Content {
				if c.Type == "tool_result" && lastID != "" && c.ToolUseID == lastID {
					answered = true
				}
			}
		}
	}
	if sc.Err() != nil {
		// scan failed partway (e.g. a line over the buffer max): fail closed
		// rather than risk reporting a phantom pending question off a
		// truncated scan.
		return nil
	}
	if lastID == "" || answered || len(lastQuestions) == 0 {
		return nil
	}
	return &PendingQuestion{ToolUseID: lastID, Questions: lastQuestions}
}

// scanPendingQuestionCodex is the codex-rollout analogue of scanPendingQuestion.
// Codex asks with a `request_user_input` tool call recorded as a response_item /
// function_call whose `arguments` is a JSON STRING (the Responses API shape);
// the answer is a function_call_output sharing the same call_id. The newest
// unanswered request wins, mapped onto the same PendingQuestion the board
// already renders — codex's per-question {header, question, options[label,
// description]} lines up 1:1 with claude's AskUserQuestion; codex has no
// multi-select, so MultiSelect stays false.
func scanPendingQuestionCodex(transcript string) *PendingQuestion {
	f, err := os.Open(transcript)
	if err != nil {
		return nil
	}
	defer f.Close()

	var lastID string
	var lastQuestions []QuestionItem
	answered := false

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var rec struct {
			Type    string `json:"type"`
			Payload struct {
				Type      string `json:"type"`
				Name      string `json:"name"`
				CallID    string `json:"call_id"`
				Arguments string `json:"arguments"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &rec) != nil || rec.Type != "response_item" {
			continue // malformed or not a response item: skip, keep scanning
		}
		switch rec.Payload.Type {
		case "function_call":
			if rec.Payload.Name != "request_user_input" {
				continue
			}
			// newest ask wins, even if its arguments fail to decode: reset the
			// tracked id/questions so a malformed newer call yields nil rather
			// than leaving a superseded older question looking pending.
			lastID = rec.Payload.CallID
			lastQuestions = nil
			answered = false
			var args struct {
				Questions []QuestionItem `json:"questions"`
			}
			if json.Unmarshal([]byte(rec.Payload.Arguments), &args) != nil {
				continue // malformed arguments: not a usable question
			}
			lastQuestions = args.Questions
		case "function_call_output":
			if lastID != "" && rec.Payload.CallID == lastID {
				answered = true
			}
		}
	}
	if sc.Err() != nil {
		return nil // fail closed on a truncated scan, as the claude path does
	}
	if lastID == "" || answered || len(lastQuestions) == 0 {
		return nil
	}
	return &PendingQuestion{ToolUseID: lastID, Questions: lastQuestions}
}

// --- state derivation ---

// deriveState maps a job to the four-state board model: draft ·
// working · blocked · done.
//
// Shared precedence (kept in lockstep with DisplayState in job.go): done
// wins first, then an error reads as draft, then any kraken-managed state
// wins, else draft.
//
// Truth table:
//   - j.State == "done"                        → "done"
//   - j.Err != ""                               → "draft" (unparseable job; error is the detail)
//   - j.State == ""                             → "draft" (never started — submit is what starts a
//     cell, in the same request, so there is no state between not-submitted and running)
//   - started (state working/blocked) but !live → "blocked" (the cell is gone — a dead cell
//     always needs carl; echoing a stale "working" is how the status-investigation
//     self-decapitation sat invisible on the board)
//   - live && active                            → "working"
//   - live && !active                           → "blocked"
//
// deriveState returns board-state strings identical to job State strings;
// persisting a working/blocked transition to the job file is the daemon's
// concern, not this function's — it stays pure so it's testable without
// touching disk. active is the caller's ROLL-CALL verdict (cellActive, plus
// any session-just-created grace) — deriveState itself never touches
// transcripts or panes.
func deriveState(j *Job, live bool, active bool) string {
	if j.State == "done" {
		return "done"
	}
	if j.Err != "" {
		return "draft"
	}
	if j.State == "" {
		return "draft"
	}
	if !live {
		return "blocked"
	}
	if active {
		return "working"
	}
	return "blocked"
}

// holdBlocked confirms a WORKING job's blocked reading over two consecutive
// ticks before the daemon acts on it. It returns the state the tick should
// act on and the value to store back as the job's BlockedSince tick.
//
// The roll call (cellActive) reads a cell's claudes from a tmux snapshot and
// a pane capture taken at one instant, and a zeus in the middle of a turn
// measurably reads as idle for exactly one tick now and then — between two
// tool calls, inside a long silent thinking span — then as running again on
// the next. Each such misread was a full working→blocked→working round
// trip: a state write, an artifact sync, and a "blocked — see the board"
// notification to carl for a cell that never stopped. Holding the first
// reading for one tick costs a genuine block one DaemonInterval of latency
// on its notification and nothing else; a cell that really stopped is
// still stopped a tick later.
//
// The hold applies ONLY to the working→blocked edge of a LIVE cell: a job
// already blocked, a draft and a done job pass through unchanged, since
// there is no transition to confirm (and a blocked job must never be
// flipped to working by a hold), and so does a job whose tmux session is
// gone — a missing session is not a roll-call misread, and holding it
// would only delay the dead-cell state by a tick for nothing. firstTick is
// the tick on which this hold began, zero when none is in progress. The
// confirmation demands ADJACENCY — the hold must have begun on the
// immediately preceding tick — rather than any earlier hold: the map entry
// survives every path that skips this call (a repo that stopped resolving,
// a job killed to draft mid-hold and resubmitted, a freed slug reused by a
// new job), and without the adjacency check a stale entry would confirm
// the next job's very first misread with no hold at all. A non-adjacent
// blocked reading therefore starts a NEW hold, restamped to this tick.
func holdBlocked(persisted, derived string, live bool, firstTick, tickN int) (string, int) {
	if derived != "blocked" || !live {
		return derived, 0
	}
	if persisted != "working" {
		return derived, 0
	}
	if firstTick == 0 || tickN-firstTick != 1 {
		return "working", tickN
	}
	return "blocked", firstTick
}

// --- job file writes ---

// ListArtifacts lists a cell's artifact files, sorted by name. Missing dir
// (no cell, or nothing produced yet) reads as no artifacts, not an error.
// ListArtifacts returns absolute paths of every file under the cell's
// artifacts dir, recursing into subdirectories (an artifact can be organized
// into folders, e.g. icon-variations/) so the board and the app can list and
// navigate them. Hidden files and dirs are skipped.
func ListArtifacts(j *Job) []string {
	root := CellArtifacts(j)
	var out []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entry: skip it, keep walking the rest
		}
		if d.IsDir() {
			if p != root && strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		out = append(out, p)
		return nil
	})
	sort.Strings(out)
	return out
}
