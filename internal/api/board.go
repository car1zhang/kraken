package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"kraken/internal/core"
	"kraken/internal/jobs"
	"kraken/internal/store"
	"kraken/internal/util"
)

// apiContract is the wire-shape version for /api/health and /api/board,
// mirrored by the iOS app's expected value (see ios/API.md). Bump ONLY on a
// breaking change (a JSON key retyped, removed, or renamed) — additive keys
// never bump it, since old clients already ignore fields they don't know.
// v2: the success criteria field was removed (webJob.success on /api/board,
// success on the job form GET/POST).
// v3: the auto-approve feature was deleted (auto_approve removed from the
// job form GET/POST shapes, auto-approve: frontmatter no longer written).
// v4: the job-level type (build|info) was deleted (Q14) — type removed from
// /api/board's webJob and the job form GET/POST shapes; the pipeline
// distinction now lives in /aim vs /info skill choice plus carl's /do
// steering, never a wire field.
// v5: skip_doc was removed from the job form GET/POST shapes (the doc phase
// now runs purely from zeus' own read of the job spec text, never a stored
// flag) and doc_questions_err was removed from /api/board's job objects
// (questions moved from a strictly-parsed questions.yaml file to relational
// rows in kraken.db — there is nothing left that can fail to parse).
// v6: the queued status was retired. `ready` and `prereqs` left the job form
// GET/POST shapes (the feature they configured is gone: submitting a job
// starts its cell in the same request, so there is no flag to set and nothing
// to gate on), `start_error` and `prereqs` left /api/board's job objects, and
// `state`'s value domain lost "queued" — draft · working · blocked · done.
// v7: the request gate (gate.go). No key changed shape; what broke is who may
// call: a request from another device on the tailnet with no pairing token is
// the node tier now and is refused (401) on every board route, so a shipped
// client that has not paired stops working against this server. Bumped so
// such a client says why instead of failing route by route.
const apiContract = 7

// legacyCellCap is the number webBoard.CellCap is served at. See that field's
// doc comment: nothing enforces a cap any more, and this exists only so
// shipped clients whose `cellCap` is non-optional keep decoding the board.
const legacyCellCap = 15

type webArtifact struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// webQuestionOption/webQuestionItem/webQuestion mirror status.go's
// QuestionOption/QuestionItem/jobs.PendingQuestion for the wire: those types carry
// json tags matched to Claude's AskUserQuestion tool-call input shape
// (multiSelect etc, decoded straight from the transcript), which isn't the
// API's own naming convention — so the board gets its own DTO instead of
// reusing those tags directly.
type webQuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

type webQuestionItem struct {
	Question    string              `json:"question"`
	Header      string              `json:"header"`
	MultiSelect bool                `json:"multi_select"`
	Options     []webQuestionOption `json:"options"`
}

type webQuestion struct {
	ToolUseID string            `json:"tool_use_id"`
	Questions []webQuestionItem `json:"questions"`
}

// webQuestionFrom converts status.go's jobs.PendingQuestion into the board's DTO.
func webQuestionFrom(pq *jobs.PendingQuestion) *webQuestion {
	if pq == nil {
		return nil
	}
	wq := &webQuestion{ToolUseID: pq.ToolUseID}
	for _, q := range pq.Questions {
		item := webQuestionItem{Question: q.Question, Header: q.Header, MultiSelect: q.MultiSelect}
		for _, o := range q.Options {
			item.Options = append(item.Options, webQuestionOption{Label: o.Label, Description: o.Description})
		}
		wq.Questions = append(wq.Questions, item)
	}
	return wq
}

// webAgent is one roster entry: a live subagent in the cell's tmux session.
type webAgent struct {
	Name  string `json:"name"`
	Model string `json:"model"`
	// Role/Status/Working mirror jobs.CrewAgent — always present (no
	// omitempty), since false/"" are meaningful roster states, not absence.
	Role    string `json:"role"`
	Status  string `json:"status"`
	Working bool   `json:"working"`
	// Kind is additive (job 612): "teammate" for the crew proper, else the
	// background-task kind riding the roster — "subagent", "monitor",
	// "shell". Old clients ignore it and render a task as an "other" crew
	// entry; omitempty only so a pre-field payload shape is still a legal
	// one, since every entry a current server emits carries it.
	Kind string `json:"kind,omitempty"`
}

// webWakeup mirrors core.Wakeup for the wire, minus DelaySeconds — the
// client derives its countdown from FiresAt against wall-clock time, it
// doesn't need the original delay.
type webWakeup struct {
	Reason  string `json:"reason"`
	FiresAt int64  `json:"fires_at"`
}

// webWakeupFrom converts a pending core.Wakeup into the board's DTO.
func webWakeupFrom(w *core.Wakeup) *webWakeup {
	if w == nil {
		return nil
	}
	return &webWakeup{Reason: w.Reason, FiresAt: w.FiresAt}
}

// webDocQuestion mirrors one `questions` row (store.DocQuestion) for the
// wire — same field set (qid renamed to id), so it's a straight field copy
// rather than a renaming DTO like webQuestion above. Kept as its own type
// (not a reuse of store.DocQuestion directly) for the same reason webJob
// never embeds a jobs.Job: the wire shape is a deliberate, independently
// evolvable contract, not whatever the internal struct happens to carry.
// Every key here is additive — no apiContract bump. Position never rides
// the wire: ordering is expressed purely by array order.
type webDocQuestion struct {
	ID             string `json:"id"`
	Text           string `json:"text"`
	Context        string `json:"context,omitempty"`
	Importance     string `json:"importance"`
	Recommendation string `json:"recommendation,omitempty"`
	Answer         string `json:"answer,omitempty"`
	AnsweredAt     string `json:"answered_at,omitempty"`
}

// webDocQuestionsFrom converts store.DocQuestion rows into the wire DTO. A
// nil or empty slice converts to nil so buildWebBoard leaves
// webJob.DocQuestions unset (omitempty) rather than an empty-but-present
// array.
func webDocQuestionsFrom(qs []store.DocQuestion) []webDocQuestion {
	if len(qs) == 0 {
		return nil
	}
	out := make([]webDocQuestion, len(qs))
	for i, q := range qs {
		out[i] = webDocQuestion{
			ID: q.QID, Text: q.Text, Context: q.Context, Importance: q.Importance,
			Recommendation: q.Recommendation, Answer: q.Answer, AnsweredAt: q.AnsweredAt,
		}
	}
	return out
}

// jobQuestions resolves j's doc questions straight from kraken.db — the
// SAME path for a live cell or a long-done job (Q13: rows are always
// answerable, live or dead), replacing the old cell-dir/pluto-fallback/
// malformed-parse tangle jobDocQuestions used to walk. nil (never an error)
// when no store is wired or the job predates a store row.
func jobQuestions(st *store.Store, j *jobs.Job) []webDocQuestion {
	if st == nil || j.ID == 0 {
		return nil
	}
	qs, err := st.ListQuestions(j.ID)
	if err != nil {
		util.LogEvent("web", "error", "board", fmt.Sprintf("list questions for %s: %v", j.Slug, err))
		return nil
	}
	return webDocQuestionsFrom(qs)
}

// webFeedback mirrors one `feedback` row (store.Feedback) for the wire —
// additive, no apiContract bump. Never carries the snapshot bytes
// themselves (those are served through the artifact/feedback-view routes,
// not the board poll) — this is ListFeedbackMeta's own contract.
type webFeedback struct {
	ID          int64  `json:"id"`
	Artifact    string `json:"artifact"`
	Note        string `json:"note"`
	At          int64  `json:"at"`
	Addressed   string `json:"addressed,omitempty"`
	AddressedAt string `json:"addressed_at,omitempty"`
}

// webFeedbackFrom converts store.Feedback rows into the wire DTO — same
// nil/empty-to-nil convention as webDocQuestionsFrom.
func webFeedbackFrom(fs []store.Feedback) []webFeedback {
	if len(fs) == 0 {
		return nil
	}
	out := make([]webFeedback, len(fs))
	for i, f := range fs {
		out[i] = webFeedback{
			ID: f.ID, Artifact: f.Artifact, Note: f.Note, At: f.TS,
			Addressed: f.Addressed, AddressedAt: f.AddressedAt,
		}
	}
	return out
}

// jobFeedback resolves jobID's feedback entries straight from kraken.db —
// scoped on the job's own immutable id, not its (mutable, reusable) slug, so
// this works for live, done, AND archived jobs identically (Q2) without ever
// cross-wiring onto a different generation of a reused slug (see
// store.ListFeedback's doc comment). nil when no store is wired.
func jobFeedback(st *store.Store, jobID int64) []webFeedback {
	if st == nil {
		return nil
	}
	fs, err := st.ListFeedbackMeta(jobID)
	if err != nil {
		util.LogEvent("web", "error", "board", fmt.Sprintf("list feedback for job %d: %v", jobID, err))
		return nil
	}
	return webFeedbackFrom(fs)
}

// webThread mirrors one `threads` row (store.Thread) for the wire — the
// board-facing subset only: the chip's identity (tid/title), its state, and
// the two timestamps the clients render. Provenance (session_id,
// parent_session, fork_uuid, seed, cwd) and the window target stay off the
// board poll entirely — nothing on a card renders them, and fork_uuid rides
// GET /api/thread/read instead, where the split it anchors is actually used.
// Additive, no apiContract bump.
type webThread struct {
	TID   string `json:"tid"`
	Title string `json:"title,omitempty"`
	State string `json:"state"`
	// Pane is a live thread's tmux pane; CloseThread clears it on the row, so
	// it is naturally absent for a closed thread rather than a stale id.
	Pane string `json:"pane,omitempty"`
	// Model is the tier this thread was branched on — a core.ThreadModels
	// alias, absent for a thread branched before the picker or with no pick.
	// It rides the board poll where the rest of the spawn detail does not
	// because it is the one piece of it a client RENDERS (the head's pill),
	// and absence is what tells that client to render nothing at all.
	Model string `json:"model,omitempty"`
	// Role is "ask" or "mind" — the mind thread is the one that writes carl's
	// mental model, and clients chip it. Never omitted: a pre-v33 row reads
	// back as "ask" from the column default.
	Role      string `json:"role"`
	CreatedAt int64  `json:"created_at"` // unix millis
	ClosedAt  int64  `json:"closed_at,omitempty"`
}

// webThreadsFrom converts store.Thread rows into the wire DTO — same
// nil/empty-to-nil convention as webDocQuestionsFrom/webFeedbackFrom, so
// buildWebBoard leaves webJob.Threads unset (omitempty) rather than serving
// an empty-but-present array.
func webThreadsFrom(ts []store.Thread) []webThread {
	if len(ts) == 0 {
		return nil
	}
	out := make([]webThread, len(ts))
	for i, t := range ts {
		role := t.Role
		if role == "" {
			role = "ask"
		}
		out[i] = webThread{
			TID: t.TID, Title: t.Title, State: t.State, Pane: t.Pane,
			Model: t.Model, Role: role, CreatedAt: t.CreatedAt, ClosedAt: t.ClosedAt,
		}
	}
	return out
}

// jobThreads resolves j's side threads straight from kraken.db — live or
// dead job alike, since a closed thread's row outlives its cell (the
// transcript it points at is still readable). nil (never an error) when no
// store is wired or the job predates a store row, and log-and-return-nil on
// a query failure, exactly like jobQuestions: one job's threads must never
// fail the whole board poll.
func jobThreads(st *store.Store, j *jobs.Job) []webThread {
	if st == nil || j.ID == 0 {
		return nil
	}
	ts, err := st.ListThreads(j.ID)
	if err != nil {
		util.LogEvent("web", "error", "board", fmt.Sprintf("list threads for %s: %v", j.Slug, err))
		return nil
	}
	return webThreadsFrom(ts)
}

// webTokens mirrors one `job_tokens` row (store.JobTokens) for the wire: the
// job's summed token spend, computed by the daemon's refresh pass
// (internal/jobs/tokens.go's tokensPass, unexported — this package never
// calls it and could not) and only ever READ here. This package computes
// nothing —
// no summing, no scanning, no weighting — for the reason spelled out in
// jobs/tokens.go: a per-read sum of a job's transcripts costs 0.1-0.3s per
// cell project dir, inside a board build that already holds boardCache's
// mutex for its whole duration.
//
// "tokens", never "usage": `usage` in this package already means ACCOUNT
// QUOTA (usage.go, webUsage, /api/usage), and the two numbers are unrelated.
//
// The four counters ride RAW with no weighted total, deliberately: they are
// billed at wildly different rates, and a "cost" number computed here would
// bake a price ratio into the API and go stale the moment pricing moves.
// Consumers weight them themselves.
//
// THE ZEUS SPLIT. Four of the keys — `zeus_in`, `zeus_out`,
// `zeus_cache_write`, `zeus_cache_read` — are the part of the four totals that
// the cell's LEAD agent spent, as opposed to everything it delegated to
// (teammates and in-process subagents alike). Two things a client will
// otherwise get wrong, both of them structural rather than stylistic:
//
//   - Each zeus key is a SUBSET of the total it shadows, never a sibling of
//     it. `in` already INCLUDES `zeus_in`; adding them would double the lead's
//     spend.
//   - Subagent spend has NO key of its own and never will. It is
//     `in - zeus_in` and so on, derived by the client. Two halves on the wire
//     could arrive disagreeing with the total they are supposed to add up to;
//     a derived half cannot.
//
// No omitempty on any field, for webUpdate's reason: a client reads all ten
// unconditionally, and an absent key would be indistinguishable from a
// measured zero. Absence is expressed one level up instead — webJob.Tokens is
// a POINTER with omitempty, so a job with no row emits no `tokens` key at
// all, which is what keeps "never measured" distinct from "measured, and it
// was zero".
//
// A counter may be a FLOOR rather than an exact total: core.SumTokensDelta
// returns what it found alongside an error when a transcript could only be
// scanned part way, and refreshJobTokens folds that in on purpose (a failed
// read is not a zero). Nothing on the wire marks which rows those are, so no
// client should present this as exact.
type webTokens struct {
	In         int64 `json:"in"`          // input_tokens — uncached input
	Out        int64 `json:"out"`         // output_tokens — generated
	CacheWrite int64 `json:"cache_write"` // cache_creation_input_tokens
	CacheRead  int64 `json:"cache_read"`  // cache_read_input_tokens
	// Sessions is the number of transcript FILES summed (root transcripts
	// plus subagent sidecars) — what lets a reader tell "this job was cheap"
	// from a row of zeros that means something else entirely.
	Sessions  int64 `json:"sessions"`
	UpdatedAt int64 `json:"updated_at"` // unix millis, matching webJob.mtime's convention
	// The zeus half of the four above — see this type's doc comment. Additive
	// keys, so apiContract does NOT bump: a client that predates them reads
	// the same six it always did.
	ZeusIn         int64 `json:"zeus_in"`
	ZeusOut        int64 `json:"zeus_out"`
	ZeusCacheWrite int64 `json:"zeus_cache_write"`
	ZeusCacheRead  int64 `json:"zeus_cache_read"`
}

// jobTokensIndex reads EVERY measured job's token row in one query and keys
// it by slug, so a board build costs one query rather than one per job — the
// payload carries ~74 of them and rebuilds roughly every two seconds, which
// is exactly why store.ListJobTokens exists at all. nil (never an error) when
// no store is wired, and log-and-return-nil on a query failure, the same
// never-fail-the-whole-poll-for-one-field contract as jobQuestions/jobThreads.
func jobTokensIndex(st *store.Store) map[int64]store.JobTokens {
	if st == nil {
		return nil
	}
	rows, err := st.ListJobTokens()
	if err != nil {
		util.LogEvent("web", "error", "board", fmt.Sprintf("list job tokens: %v", err))
		return nil
	}
	index := make(map[int64]store.JobTokens, len(rows))
	for _, r := range rows {
		index[r.JobID] = r
	}
	return index
}

// webTokensFor looks one job's row out of jobTokensIndex' map and shapes it
// for the wire. Keyed on the job's immutable id: the rows moved off the slug
// in schema v12 precisely because two generations of one reused name shared a
// single row, and both cards then showed the sum of both jobs' spend. (v11 is
// the zeus/subagent SPLIT of the same counters and left the slug keying alone —
// two migrations one apart that both rewrote job_tokens, which is exactly why
// naming the wrong one here would send a reader to the wrong doc comment.)
//
// A job with no row — the store's ErrNotFound case, and every job whose
// transcripts have not been summed yet — returns nil, so webJob's omitempty
// drops the key rather than synthesising a zero row that would claim the job
// cost nothing. After a migration that WIPES the counters — v11, and v14 for
// the streamed-output fix — that is EVERY job for a few minutes, since they are
// cleared and re-summed from byte zero by the daemon's throttled pass.
func webTokensFor(index map[int64]store.JobTokens, jobID int64) *webTokens {
	t, ok := index[jobID]
	if !ok {
		return nil
	}
	return &webTokens{
		In: t.In, Out: t.Out, CacheWrite: t.CacheWrite, CacheRead: t.CacheRead,
		Sessions: t.Sessions, UpdatedAt: t.UpdatedAt,
		ZeusIn: t.ZeusIn, ZeusOut: t.ZeusOut,
		ZeusCacheWrite: t.ZeusCacheWrite, ZeusCacheRead: t.ZeusCacheRead,
	}
}

// webJobAccount is the pool slot one cell is BOUND to — the account every
// claude in that cell bills, delivered to the pane as
// CLAUDE_SECURESTORAGE_CONFIG_DIR and stamped on the job row at spawn (schema
// v28's three columns, store.SetJobAccount).
//
// "account", never "usage": this names WHICH login a cell spends on, while
// /api/usage answers how much QUOTA that login has left — the same collision
// webTokens' doc comment refuses in the other direction.
//
// None of the three inner fields carries omitempty, and that is the same rule
// webTokens states: presence of the OBJECT is the whole of the answer, so once
// a client sees the key it reads all three unconditionally. Absence is said one
// level up, by webJob.Account being nil — see its comment for what that covers.
// Email may legitimately be "" for a slot ccp has no identity for yet, which is
// a real answer rather than a missing field, and is exactly why the clients
// fall back to the slug for their visible text rather than hiding the badge.
type webJobAccount struct {
	// Slug is the pool slot's directory name under ~/.claude-pool — the id
	// kraken selects and rebinds on, and what the usage payload's own rows are
	// keyed by, so a client can join the two without a second request.
	Slug string `json:"slug"`
	// Email is the account's identity as ccp recorded it, carried so a card can
	// say WHO rather than only which directory. "" when the slot has no
	// identity yet.
	Email string `json:"email"`
	// At is unix MILLIS of the bind stamp — webJob.mtime's and webTokens'
	// convention, never seconds. It is what makes a rebind legible: the same
	// card carries a different account with a fresh stamp, so a reader can tell
	// "bound at spawn" from "rebound ten minutes ago by the limit rescue".
	At int64 `json:"at"`
}

// webAccountFor shapes one job's stored binding for the wire, or nil when the
// job carries none. The nil rule lives here rather than at the call site for
// webJobLoadFrom's reason: "unbound" is said by the ABSENCE of the key, so
// exactly ONE function decides what absence means.
//
// It takes the whole *jobs.Job rather than the three columns, which is
// webTokensFor's shape and is the point of the function existing at all: the
// wire's presence RULE — served for every non-archived job with a binding,
// regardless of live or display state (Load's no-flicker rule) — has to be
// observable somewhere a test can call, and buildWebBoard is not that place
// (calling it drags in tmux and process-table discovery, the same reason
// archivedWebJob was split out below). Handed three loose strings, the rule
// would live at the call site again and go back to being unreachable.
//
// The test is the SLUG alone. Slug is what the env var is built from and what
// a rebind overwrites, so a row with a slug is a real binding whatever else it
// holds — an empty Email (a slot ccp has no identity for) and a zero At (a row
// written by something that did not stamp a time) are degraded bindings, not
// absent ones, and hiding them would hide the account a cell is actually
// spending on.
//
// There is deliberately no display-state or liveness test in here. That is the
// whole no-flicker rule: a card moves between working and blocked tick to tick,
// and a key that came and went with it would flicker the chip in and out. The
// blocked/working gate the clients apply (web's accountChipVisible) is a
// RENDERING rule, not a payload one. The one state that IS withheld is
// archived, and it is withheld by archivedWebJob never calling this — see the
// comment beside that omission for why an archived card is Load's case rather
// than Tokens'.
func webAccountFor(j *jobs.Job) *webJobAccount {
	if j == nil || j.AccountSlug == "" {
		return nil
	}
	return &webJobAccount{Slug: j.AccountSlug, Email: j.AccountEmail, At: j.AccountAt}
}

type webJob struct {
	Slug    string `json:"slug"`
	Repo    string `json:"repo"`
	State   string `json:"state"`
	Err     string `json:"err,omitempty"`
	Outcome string `json:"outcome,omitempty"`
	// Color is the job's chosen colour: one of jobs.Palette's eight names,
	// omitted entirely when unset — a client that sees no key renders the
	// card exactly as it did before colours existed. Additive, so
	// apiContract stays put.
	Color string `json:"color,omitempty"`
	// Agent is which coding agent the cell's zeus runs — "codex" for a codex
	// cell, omitted for the claude default so a client that predates the field
	// renders the card exactly as before. Additive, so apiContract stays put.
	Agent string `json:"agent,omitempty"`
	// start_error and prereqs used to sit here and are gone in v6. start_error
	// was a side channel for a failure the board could not otherwise see: the
	// daemon's queue pass started jobs in the background, so the reason a
	// queued job would not start reached nobody. A start is a foreground HTTP
	// request now — the reason comes back in that request's own 409 — and a
	// key that can only ever be empty is a key a client keeps rendering
	// affordances for. prereqs went with the feature (Q1).
	Cell      string        `json:"cell,omitempty"`
	Live      bool          `json:"live"`
	Artifacts []webArtifact `json:"artifacts,omitempty"`
	// Detail is the cell's last words: the full text of the last assistant
	// text block of zeus' transcript, capped at 4000 chars. Set wherever
	// that transcript resolves — a live started cell, a cell-gone one, and a
	// DONE job (located by its stamped session, since the cell is torn
	// down) — so a done card reads like a blocked one: the job's final
	// report stays on the board until the job is archived.
	Detail string `json:"detail,omitempty"`
	// LastSent is the last real user message from zeus' own transcript,
	// regardless of send channel (board steer, k s, direct tmux typing),
	// one-lined and capped at 4000 chars. Omitted when no transcript is
	// resolvable or it has no user turn yet; question-answer digit
	// keypresses never appear here since they're tool_results, not user text.
	LastSent string `json:"last_sent,omitempty"`
	// Question mirrors an unanswered AskUserQuestion call in zeus'
	// transcript; the board renders it as header + option buttons.
	Question *webQuestion `json:"question,omitempty"`
	// Stage is the pipeline skill zeus most recently used — one of the four
	// phase-governing skill names (info, aim, do, dream; `pr` folded into do
	// in job 624, and a client renders an unknown stage with its generic
	// fallback) — read
	// off zeus' transcript (jobs.FindStage), never declared by any agent.
	// Set only where a transcript already resolves (live started cells, the
	// cell-gone branch and done jobs — exactly the branches that read Detail).
	// Absence covers three states the wire deliberately does not separate —
	// no resolvable transcript, any failed or truncated scan (FindStage
	// fails closed), and no pipeline skill used yet — because a client
	// renders all three the same way (no badge), the same collapse
	// webJob.load documents for its own key. Additive, so apiContract stays
	// put.
	Stage string `json:"stage,omitempty"`
	// ZeusStatus is zeus' own refined status (running/polling/waiting/
	// question), distinct from the cell's aggregate state; omitted when zeus
	// isn't resolvable (fresh fork, no session file yet).
	ZeusStatus string `json:"zeus_status,omitempty"`
	// Wakeup mirrors the zeus peer's own pending ScheduleWakeup call and is
	// present only while the cell is polling (mid /loop or /poll-pr-style
	// tick). The client renders a countdown chip from it; the cell still
	// counts as "working".
	Wakeup *webWakeup `json:"wakeup,omitempty"`
	// Roster lists the cell's live subagents (nested claude peers).
	Roster []webAgent `json:"roster,omitempty"`
	// Health is a computed condition on the live cell ("" = healthy): API
	// errors or dead agent processes. Never written to the job file — the
	// board shows it in BLOCKED with a retry affordance.
	Health  string `json:"health,omitempty"`
	ModTime int64  `json:"mtime"`
	// Archived jobs ride the same list with State "archived". Their Slug is
	// the numbered archive file base — what unarchive/delete target, and
	// unique WITHIN the archive but NOT across this whole array: archiving
	// `foo` numbers against the archive's own contents only, so the first
	// archival of any slug keeps the bare name, and a new active job that
	// takes the freed slug back then rides the SAME slug string as the
	// archived one. Archived entries are appended last (see the loop at the
	// bottom of buildWebBoard), so a client that keys anything by bare slug
	// silently loses the ACTIVE job to the archived one — which is exactly
	// how a live job stopped being steerable from the board (web/static/
	// app.js' jobKey). Pair slug with `archived` to get a unique key.
	// DisplaySlug carries the original slug for rendering.
	Archived    bool   `json:"archived,omitempty"`
	DisplaySlug string `json:"display_slug,omitempty"`
	// CellGone marks a started, non-done job whose tmux session vanished
	// (killed by hand, tmux restart, a teardown that died mid-flight): the
	// worktree — uncommitted work included — is still on disk, and reactivate
	// is the recovery that preserves it. Additive key; clients without it
	// just miss the chip.
	CellGone bool `json:"cell_gone,omitempty"`
	// Reactivatable marks a job whose zeus session id is recorded and whose
	// cell is resumable — done jobs, plus gone-cell started jobs — so the UIs
	// only offer the reactivate action where it can actually work.
	Reactivatable bool `json:"reactivatable,omitempty"`
	// PRs mirrors the job file's kraken-owned prs: list — every PR the
	// cell ever shipped, rendered by the clients as linked state chips.
	PRs []jobs.JobPR `json:"prs,omitempty"`
	// Stack is the cell's manual-test URLs (jobstack.go), one linked chip
	// each beside the PR chips. Served straight from `job_stacks` rows for
	// active jobs; absent (omitempty, and webStackLinksFrom returns nil for
	// no rows) when the cell registered none, and never set for archived
	// jobs — the rows are deleted at teardown, so an archived job has none
	// to show. ADDITIVE, so apiContract does not bump.
	Stack []webStackLink `json:"stack,omitempty"`
	// DocQuestions mirrors zeus' open doc questions (answered or not),
	// served straight from `questions` rows in kraken.db — live or dead, no
	// file, no parse step, so there is no more malformed-file case (see
	// jobQuestions). Never set for archived jobs.
	DocQuestions []webDocQuestion `json:"doc_questions,omitempty"`
	// Feedback mirrors this job's feedback entries, closed and not, served
	// straight from `feedback` rows in kraken.db (see jobFeedback) — works
	// for live, done, and archived jobs alike (Q2).
	Feedback []webFeedback `json:"feedback,omitempty"`
	// Threads mirrors this job's side threads — every fork of zeus' session,
	// live or closed (see jobThreads). The clients render one chip each.
	Threads []webThread `json:"threads,omitempty"`
	// Tokens is this job's summed token spend, straight off its `job_tokens`
	// row (see webTokensFor). A pointer with omitempty precisely so an
	// unmeasured job emits NO key: "never summed" and "summed, and it was
	// zero" are different answers and a zero object would conflate them.
	// Survives teardown and archival — the row is keyed on the slug, with no
	// FK and no cascade, because retrospective cost is the whole point.
	Tokens *webTokens `json:"tokens,omitempty"`
	// Load is zeus' triage estimate for this job — unblock effort against the
	// size of the work it unblocks (see jobload.go). A pointer with omitempty
	// for the reason Tokens is one: a zeroed object would claim an estimate
	// OF zero, and there is no such thing. The all-zero pair is not refused —
	// it is the clear, accepted with a 200 — but the clear zeroes load_at too,
	// so it is recorded as the absence of an estimate rather than as one. The
	// absence of an estimate is therefore said by the absence of the KEY.
	//
	// Three states share that absence and the wire does not separate them:
	// never estimated, cleared (a clear zeroes load_at, and webJobLoadFrom
	// serves no object for a zero at), and archived (archivedWebJob withholds
	// the key even for a row that holds a real estimate). A client has one
	// rendering for all three — no badge — which is why one key is enough.
	//
	// The ratio the clients render is derived from these two numbers and is
	// deliberately not a key here.
	//
	// ADDITIVE — a new optional key on an existing shape, not a retype,
	// rename, or removal — so apiContract does NOT bump for it. A client that
	// does not know the key ignores it and renders the card exactly as it did
	// before estimates existed.
	Load *webJobLoad `json:"load,omitempty"`
	// Goals is every goal this job serves (goals.go), in link position order
	// and present exactly when there is at least one — absence is the wire's
	// only spelling of "serves none", so an empty ARRAY is never served
	// (omitempty, and webJobGoalsFor returns nil rather than an empty slice).
	//
	// Each element carries the goal's TEXT rather than only its id, so a card
	// can render its chips from the board payload alone — the alternative (ids
	// the client resolves against the board's top-level "goals") would be blank
	// for every goal outside the current period, which is exactly the goal a
	// long-running job is most likely to still serve.
	//
	// ADDITIVE — a new optional key on an existing shape — so apiContract does
	// NOT bump. A client that does not know it reads `goal` below and renders
	// one chip, exactly as it did when a job could serve only one goal.
	Goals []webJobGoal `json:"goals,omitempty"`
	// Goal is this job's PRIMARY goal, and it is `goals[0]` by definition —
	// present exactly when Goals is, built from it by the one function that
	// builds both (primaryJobGoal), so the two can never name different goals.
	//
	// It is retained rather than replaced because every shipped iOS build reads
	// it: the array is the addition, this is the compatibility. A pointer with
	// omitempty for the reason Load is one: a zeroed object would claim a link
	// to goal 0, and there is no such goal.
	Goal *webJobGoal `json:"goal,omitempty"`
	// Account is the pool slot this cell is bound to (webJobAccount above): the
	// login every claude in it bills. A pointer with omitempty because ABSENCE
	// IS THE WIRE'S ONLY SPELLING OF "unbound" — a job with no binding runs on
	// the machine's main login exactly as every cell did before this feature,
	// and a zeroed object would claim a binding to a slot named "".
	//
	// Two states share that absence and the wire deliberately does not separate
	// them, because a client renders both the same way (no chip, and the
	// modal's "main (machine default)" line): the pool was dry or fully
	// excluded when the cell spawned, and rotation is pinned MANUAL, where
	// kraken takes no account decision at all.
	//
	// Emitted for every NON-ARCHIVED job carrying a binding, regardless of live
	// or display state — Load's no-flicker rule verbatim: a card moves between
	// working and blocked tick to tick, and a key that came and went with it
	// would flicker the chip in and out. The blocked/working gate the clients
	// apply is a RENDERING rule, not a payload rule.
	//
	// archivedWebJob deliberately withholds it, which is Load's call rather
	// than Tokens': what a job COST is retrospective and worth keeping, while
	// which account it was spending is a fact about a cell that is running, and
	// an archived job has none. The columns survive archival on the job row, so
	// the key comes back on unarchive.
	//
	// ADDITIVE — a new optional key on an existing shape, not a retype, rename
	// or removal — so apiContract does NOT bump. A client that does not know
	// the key renders the card exactly as it did before per-cell binding.
	Account *webJobAccount `json:"account,omitempty"`
	// Visibility is who on the tailnet may see or drive this job — private,
	// readonly or public (jobs.EffectiveVisibility, so readonly for a job that
	// never set one). Always present: a remote board decides which controls
	// to offer from it, and the local modal shows the current level.
	Visibility string `json:"visibility"`
	// Owner names the node a REMOTE card runs on. Absent on every local card
	// — absence is the wire's spelling of "this machine's job".
	Owner *webJobOwner `json:"owner,omitempty"`
}

// webJobOwner is a remote card's owner chip: which node, whose, and whether
// that node answered its last polls.
type webJobOwner struct {
	NodeID   string `json:"node_id"`
	Label    string `json:"label"`
	Operator string `json:"operator"`
	Online   bool   `json:"online"`
}

// webNode is one registry row on the wire. Jobs (the node's card list) is
// filled only by GET /api/nodes; on the board the cards already ride `jobs`.
type webNode struct {
	NodeID   string          `json:"node_id"`
	Label    string          `json:"label"`
	Operator string          `json:"operator"`
	DNSName  string          `json:"dns_name"`
	IP       string          `json:"ip"`
	Port     int             `json:"port"`
	Build    string          `json:"build"`
	Contract int             `json:"contract"`
	Online   bool            `json:"online"`
	LastOK   int64           `json:"last_ok"`
	LastErr  string          `json:"last_err,omitempty"`
	Jobs     json.RawMessage `json:"jobs,omitempty"`
}

func webNodeFrom(n *store.NodeRow, withJobs bool) webNode {
	w := webNode{NodeID: n.ID, Label: n.Label, Operator: n.Operator, DNSName: n.DNSName,
		IP: n.IP, Port: n.Port, Build: n.Build, Contract: n.Contract, Online: n.Online,
		LastOK: n.LastOK, LastErr: n.LastErr}
	// re-encoded through the same per-element decode and strip the board
	// applies, never the stored jobs_json verbatim — a sibling that serves
	// more than it should is not repeated here either
	if withJobs {
		if cards, ok := strippedCards(n.ID, n.JobsJSON); ok {
			if b, err := json.Marshal(cards); err == nil {
				w.Jobs = json.RawMessage(b)
			}
		}
	}
	return w
}

// strippedCards decodes a node's stored card list element by element — a bad
// element drops alone — and passes each card through nodeCard. ok is false
// when nothing is stored or the stored value is not an array (a stored `[]`
// is ok with zero cards).
//
// Every artifact URL is then REPLACED with this node's own proxy URL for
// (nodeID, slug, name): the sibling's URL addresses the sibling's
// /api/artifact/, which resolved here would read a same-slug job of THIS
// node, so a stored card's url values are never read at all. An artifact
// with a blank name has nothing to address and is dropped, as is a repeat of
// a name already listed on the card.
func strippedCards(nodeID, jobsJSON string) ([]webJob, bool) {
	if jobsJSON == "" {
		return nil, false
	}
	var raws []json.RawMessage
	if err := json.Unmarshal([]byte(jobsJSON), &raws); err != nil {
		return nil, false
	}
	cards := make([]webJob, 0, len(raws))
	for _, raw := range raws {
		var card webJob
		if err := json.Unmarshal(raw, &card); err != nil {
			continue
		}
		card = nodeCard(card)
		if card.Artifacts != nil {
			arts := make([]webArtifact, 0, len(card.Artifacts))
			seen := map[string]bool{}
			for _, a := range card.Artifacts {
				// a repeated name would address the same proxied file twice
				// and collide as a client list key; the first one wins
				if strings.TrimSpace(a.Name) == "" || seen[a.Name] {
					continue
				}
				seen[a.Name] = true
				arts = append(arts, webArtifact{Name: a.Name, URL: remoteArtifactURL(nodeID, card.Slug, a.Name)})
			}
			card.Artifacts = arts
		}
		cards = append(cards, card)
	}
	return cards, true
}

// remoteArtifactURL is the URL this node serves a sibling's artifact under:
// GET /api/nodes/artifact, relayed to the owner's /api/node/artifact. name is
// the LAST parameter because the SPA decides whether to render an artifact as
// a page by matching the URL's tail against .html, and url.QueryEscape leaves
// that suffix as written.
func remoteArtifactURL(nodeID, slug, name string) string {
	return "/api/nodes/artifact?node=" + url.QueryEscape(nodeID) + "&slug=" + url.QueryEscape(slug) + "&name=" + url.QueryEscape(name)
}

// remoteCards are the registry's polled cards, each stamped with its owner.
// A stored list that is not an array contributes nothing; within one, each
// element decodes on its own, so a single malformed card drops alone. Every
// card passes through the strip nodeCard applies to outbound cards — a
// sibling that serves more than it should is not repeated on this board —
// and has its artifact URLs rewritten to this node's proxy (strippedCards).
func remoteCards(rows []*store.NodeRow) []webJob {
	var out []webJob
	for _, n := range rows {
		cards, ok := strippedCards(n.ID, n.JobsJSON)
		if !ok {
			continue
		}
		// nodeLabel: a row announced with no label names its node by id, so
		// the owner chip (and any label fallback a client takes) never reads
		// blank
		owner := webJobOwner{NodeID: n.ID, Label: nodeLabel(n), Operator: n.Operator, Online: n.Online}
		for _, card := range cards {
			o := owner
			card.Owner = &o
			out = append(out, card)
		}
	}
	return out
}

type webBoard struct {
	Daemon   jobs.DaemonStatus `json:"daemon"`
	DaemonOK bool              `json:"daemon_ok"`
	// CellCap is VESTIGIAL and is served only for wire compatibility. Nothing
	// enforces a cap any more: the daemon's queue pass was the only reader of
	// jobs.CellCap and it is gone with the queued status, so the number below
	// describes nothing kraken will actually do.
	//
	// The key stays because `cellCap` is a NON-OPTIONAL member of the iOS
	// app's Board model: removing it would fail the decode of the WHOLE board
	// on every installed build, not merely blank one header. It is
	// deliberately held at the value shipped clients have always seen rather
	// than zeroed, since a client renders it as "cells N/M" and a 0 there
	// reads as a cap of none. It goes when the clients stop reading it.
	CellCap   int      `json:"cell_cap"`
	LiveCells int      `json:"live_cells"`
	Jobs      []webJob `json:"jobs"`
	Contract  int      `json:"contract"`
	// Stale is this k web process' own self-check (see selfcheck.go) — the
	// daemon's equivalent rides Daemon.Stale.
	Stale bool `json:"stale"`
	// Update is the self-update button's whole state — the running binary's
	// build sha, how far behind origin/main it is, and any run in flight.
	// Additive (see apiContract's bump policy above): clients that don't
	// know the key ignore it.
	Update webUpdate `json:"update"`
	// ShipIOS is the TestFlight button's whole state — the phase of any ship
	// in flight and the build number it is uploading. Additive, same policy
	// as Update.
	ShipIOS webShipIOS `json:"ship_ios"`
	// Goals is the CURRENT local week's and today's goals (goals.go), the
	// same pair GET /api/goals serves, from the same assembly function so the
	// two cannot drift. Always present with both arrays non-null: the panel
	// renders "no goals yet" from an empty array, and a null would make every
	// consumer write a second nil check for a state that is not different.
	//
	// It rides the board rather than making the panel poll a second endpoint
	// because the panel is on the board and repaints with it — a separate poll
	// would put the goals and the cards that reference them one tick out of
	// step. Additive, same policy as Update and ShipIOS.
	Goals webGoals `json:"goals"`
	// Background is the board's optional background image — the updated_at
	// stamp only, never the bytes (GET /api/background serves those; see
	// background.go). A pointer with omitempty: absence is the wire's only
	// spelling of "no background", webJob.load's rule. Additive, same policy
	// as Update and ShipIOS.
	Background *webBackground `json:"background,omitempty"`
	// Operator is the name of the person this kraken works for (jobs.Operator:
	// KRAKEN_OPERATOR, else $USER, else "the operator"), so a client can label
	// that person's turns without carrying a name of its own. Additive, same
	// policy as Update and ShipIOS.
	Operator string `json:"operator"`
	// Nodes is the fleet registry (sibling krakens on the tailnet), without
	// their card lists — those ride `jobs` with an `owner`. Absent when the
	// registry is empty, which is every single-machine kraken: the payload is
	// then exactly the pre-fleet one plus each card's `visibility`.
	Nodes []webNode `json:"nodes,omitempty"`
}

// jobArtifacts collects a job's viewable artifacts from exactly one source.
// A live cell serves straight from the cell dir (Q12). Any other job serves
// the artifacts table — the block-event sync's mirror, which teardown's
// final pass completes with the worktree's uncommitted root notes
// (jobs/sync.go) — under the URL home "pluto" for an active job and
// "archive" (keyed on ArchivedName) for an archived one. The home names are
// namespace names that shipped iOS builds decode, not directories: nothing
// here reads the filesystem once the cell is not live.
//
// Both branches end in sortedArtifacts — the wire order is the reading
// order, and the guarantee must not rest on either source happening to
// arrive sorted.
func jobArtifacts(j *jobs.Job, live bool) []webArtifact {
	if !live {
		if j.ArchivedName != "" {
			return sortedArtifacts(storeArtifactList(j.ArchivedName, j.ID, "archive"))
		}
		return sortedArtifacts(storeArtifactList(j.Slug, j.ID, "pluto"))
	}
	root := jobs.CellArtifacts(j)
	var out []webArtifact
	for _, p := range jobs.ListArtifacts(j) {
		// name is the path relative to the artifacts dir, so nested files
		// read as "icon-variations/icon-v1.png" and clients can group by dir
		name := filepath.Base(p)
		if rel, err := filepath.Rel(root, p); err == nil {
			name = rel
		}
		out = append(out, webArtifact{Name: name, URL: artifactURL(j.Slug, "cell", filepath.ToSlash(name))})
	}
	return sortedArtifacts(out)
}

// sortedArtifacts puts one job's artifact list into the single reading order
// ios/API.md promises for the `artifacts` array, and is the ONE place that
// promise is kept — every producer of a wire artifact list ends in a call to
// it, so a future second source added to either producer inherits the
// guarantee instead of silently breaking it.
//
// It exists because the list once came from several sources, each sorted
// WITHIN itself, and appending sorted lists does not produce a sorted one —
// a torn-down job served 00-overview.md AFTER 90-build-report.md. The wire
// order IS the reading order (the clients page through it with next/prev),
// so it is enforced here rather than assumed of whichever source a producer
// reads today.
func sortedArtifacts(out []webArtifact) []webArtifact {
	sort.SliceStable(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out
}

// storeArtifactList lists jobID's DB-mirrored artifacts (store/artifacts.go,
// populated by the block-event sync — jobs/sync.go) as webArtifacts under
// the given URL "home" segment, which ArtifactBytes/artifactHandler resolve
// through the store. home is "pluto" for an active job's artifacts,
// "archive" for an archived job's — namespace names kept for the wire, not
// directories.
func storeArtifactList(slug string, jobID int64, home string) []webArtifact {
	st := jobs.ActiveStore()
	if st == nil || jobID == 0 {
		return nil
	}
	rows, err := st.ListArtifacts(jobID)
	if err != nil {
		return nil
	}
	out := make([]webArtifact, 0, len(rows))
	for _, a := range rows {
		out = append(out, webArtifact{Name: a.Path, URL: artifactURL(slug, home, a.Path)})
	}
	return out
}

// artifactURL builds /api/artifact/<slug>/<home>/<name> with every path
// segment url.PathEscape'd on its own — name's "/" separators kept, each
// segment between them escaped — so a name carrying "#", "%", "?" or a
// space round-trips: net/http decodes r.URL.Path back to the raw name
// before artifactHandler splits it.
func artifactURL(slug, home, name string) string {
	segs := strings.Split(name, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return "/api/artifact/" + url.PathEscape(slug) + "/" + url.PathEscape(home) + "/" + strings.Join(segs, "/")
}

// ArtifactPath resolves a "cell" artifact request to a real file under the
// live cell's artifacts dir, refusing anything that escapes it (the URL is
// attacker-adjacent input: any local process can hit the port). A name may
// be a subpath (icon-variations/icon-v1.png) so folders are navigable, but
// every segment is validated against traversal and hidden files. Every other
// home is served from the artifacts table (ArtifactBytes) and has no file
// here: ("", false).
func ArtifactPath(j *jobs.Job, home, name string) (string, bool) {
	if home != "cell" {
		return "", false
	}
	name = strings.TrimPrefix(name, "/")
	if name == "" {
		return "", false
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == "" || seg == "." || seg == ".." || strings.HasPrefix(seg, ".") {
			return "", false // no traversal, no empty/hidden segments
		}
	}
	root := jobs.CellArtifacts(j)
	p := filepath.Join(root, name)
	// job files are never artifacts, in any home
	if strings.HasSuffix(p, jobs.JobSuffix) {
		return "", false
	}
	// belt-and-suspenders: the cleaned path must still sit under its home
	if rel, err := filepath.Rel(root, p); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	if fi, err := os.Stat(p); err != nil || fi.IsDir() {
		return "", false
	}
	return p, true
}

// ArtifactBytes is ArtifactPath's store-backed twin (Q12): resolves
// job+name straight from the artifacts table, for any non-"cell" home — the
// ONLY way a done or archived job's "pluto"/"archive" artifacts are served.
// ok is false whenever no store is wired, j has no store row, or the path
// isn't found in the table, and there is no file fallback behind it.
//
// L11: home is a required parameter, refused outright when "cell" — Q12's
// rule is that a live cell's artifacts are NEVER DB-backed, only ever
// served straight off disk, so the table must not answer for a name that
// was asked of the cell home.
// Also refuses any name ending in jobs.JobSuffix — mirroring ArtifactPath's
// own "job files are never artifacts" refusal, which ArtifactBytes never
// had (a name reconstructing to a job file's own
// suffix must never be servable as an artifact, DB-backed or not).
func ArtifactBytes(j *jobs.Job, home, name string) (data []byte, mimeType string, ok bool) {
	if home == "cell" {
		return nil, "", false
	}
	st := jobs.ActiveStore()
	if st == nil || j.ID == 0 {
		return nil, "", false
	}
	name = strings.TrimPrefix(name, "/")
	if name == "" {
		return nil, "", false
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == "" || seg == "." || seg == ".." || strings.HasPrefix(seg, ".") {
			return nil, "", false // no traversal, no empty/hidden segments — mirrors ArtifactPath
		}
	}
	if strings.HasSuffix(name, jobs.JobSuffix) {
		return nil, "", false
	}
	a, err := st.GetArtifact(j.ID, name)
	if err != nil {
		return nil, "", false
	}
	return a.Bytes, artifactMimeType(name), true
}

// artifactMimeType mirrors web/serve.go's artifactContentType (same
// extension → type mapping), duplicated here since api sits below web in
// the dependency chain and DB-served bytes never pass through
// http.ServeFile's own path-based content-type sniffing.
func artifactMimeType(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".pdf":
		return "application/pdf"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	default:
		return "text/plain; charset=utf-8"
	}
}

// cellZeusTranscript resolves a live job's zeus peer and (best-effort) its
// transcript path in one shot — the shared lookup behind the board's detail
// and question fields, and the question-answer action. An unresolvable
// transcript (fresh fork, no session file yet) reads as "" rather than an
// error — the board field is just omitted.
func cellZeusTranscript(j *jobs.Job, all []*core.Peer) (zeus *core.Peer, transcript string) {
	zeus = jobs.CellZeus(j, all)
	if zeus == nil {
		return nil, ""
	}
	path, err := core.ReadTranscriptPath(zeus)
	if err != nil {
		return zeus, ""
	}
	return zeus, path
}

// lastAssistantText scans a transcript for the last assistant text block
// (parseBlocks' "text" kind — thinking and tool_use blocks don't count),
// tail-reading via lastBlocks the same way k r does so huge transcripts stay
// fast. "" when the file is unreadable or has no assistant text within the
// lookback window.
// fillLastWords stamps the three transcript-derived card lines — the last
// assistant text (Detail), the last real user message (LastSent) and the
// pipeline stage — from a transcript located WITHOUT a live peer: the
// cell-gone and done branches of the board build, which share this because
// they share the situation (a stamped session, no process). An empty path
// is a job with no resolvable transcript and leaves all three unset.
func fillLastWords(wj *webJob, transcript string) {
	if transcript == "" {
		return
	}
	wj.Detail = lastAssistantText(transcript)
	wj.LastSent = lastUserSent(transcript)
	wj.Stage = jobs.FindStage(transcript)
}

func lastAssistantText(path string) string {
	blocks, _, err := core.LastBlocks(path, 20)
	if err != nil {
		return ""
	}
	for i := len(blocks) - 1; i >= 0; i-- {
		if blocks[i].Kind == "text" {
			return truncateText(blocks[i].Text, 4000)
		}
	}
	return ""
}

// truncateText caps s at limit runes, preserving newlines and internal
// whitespace (unlike truncateOneLine, which flattens to a single line) —
// the board wants the cell's actual formatting, just bounded so a runaway
// transcript block doesn't blow up the wire payload. Other control chars
// (esp. \x1b) are mapped to space, same as truncateOneLine, since this
// feeds iOS notification bodies too.
func truncateText(s string, limit int) string {
	s = strings.Map(func(r rune) rune {
		if r == '\n' {
			return r
		}
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit]) + "…"
}

// lastUserSent resolves a transcript path to the board's last_sent field:
// the last real user message, one-lined and capped at 4000 chars. ""
// when the path is unresolved or the transcript has no user turn yet.
func lastUserSent(transcript string) string {
	if transcript == "" {
		return ""
	}
	return core.TruncateOneLine(core.LastUserText(transcript), 4000)
}

// boardCache memoizes buildWebBoard for a short TTL so a burst of board
// polls (several web tabs + the iOS app) costs one discovery, not N. The
// mutex doubles as single-flight: concurrent misses serialize and late
// arrivals get the fresh entry. Mutations invalidate so a post-action
// refresh never sees pre-action state.
type boardCache struct {
	mu    sync.Mutex
	ttl   time.Duration
	now   func() time.Time // injectable for tests
	build func() webBoard  // injectable for tests
	at    time.Time        // zero = invalid
	board webBoard
}

// get returns the cached board if it's still within ttl, otherwise rebuilds
// under the lock — the lock held across build is what makes concurrent
// misses single-flight instead of a thundering herd of discoveries.
func (c *boardCache) get() webBoard {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.at.IsZero() && c.now().Sub(c.at) < c.ttl {
		return c.board
	}
	c.board = c.build()
	c.at = c.now()
	return c.board
}

// invalidate forces the next get() to rebuild — called after any write so a
// client's post-action refresh never reads a pre-action snapshot.
func (c *boardCache) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = time.Time{}
}

// webBoardCache is the /api/board handler's single instance.
var webBoardCache = &boardCache{ttl: 2 * time.Second, now: time.Now, build: buildWebBoard}

func buildWebBoard() webBoard {
	js := jobs.LoadJobs()
	bySlug := map[string]*jobs.Job{}
	for _, j := range js {
		bySlug[j.Slug] = j
	}
	live := jobs.LiveSessions()
	st, ok := jobs.ReadDaemonStatus()
	all, _ := core.OrderAll(core.DiscoverAll())
	activeStore := jobs.ActiveStore()
	board := webBoard{Daemon: st, DaemonOK: ok && jobs.ProcessAlive(st.PID), CellCap: legacyCellCap, Jobs: []webJob{}, Contract: apiContract, Stale: util.IsSelfStale(), Update: currentUpdate(), ShipIOS: currentShipIOS(), Goals: currentGoals(activeStore), Background: currentBackground(activeStore), Operator: jobs.Operator()}
	order := jobs.LoadBoardOrder()
	// The card chips, in two steps with two different scopes, deliberately: ONE
	// unfiltered query for every link in job_goals (jobGoalLinks — small, and
	// every row of it belongs to a job this board renders), then the goals those
	// links name, taken from the two lists the payload ALREADY carries plus one
	// by-id read per distinct id those lists do not cover (a job still serving
	// last week's goal). Only the second is demand-scoped, since goals is the
	// table that only grows — see linkedJobGoals. The archived loop below reads
	// the same two maps, which is why they are built out here.
	links := jobGoalLinks(activeStore)
	goals := linkedJobGoals(activeStore, board.Goals, links)
	// One query for the WHOLE board's token rows, read before the loop and
	// indexed by slug — never a per-job GetJobTokens, which would be ~74
	// queries inside the cached build (store.ListJobTokens' own reason for
	// existing). The archived loop below reads the same index.
	tokens := jobTokensIndex(activeStore)
	byState := map[string][]webJob{}
	for _, j := range js {
		// Computed once: this loop runs for every job on a payload rebuilt
		// roughly every two seconds, and the byState bucketing below needs the
		// same answer.
		displayState := jobs.DisplayState(j)
		// Resolved once per card, and once for BOTH keys: Goal is this slice's
		// head, never a separate lookup, which is what makes "goal is goals[0]"
		// a property of the code rather than a convention two lines share.
		jobGoals := webJobGoalsFor(goals, links[j.ID])
		wj := webJob{
			Slug: j.Slug, Repo: j.Repo,
			State: displayState, Err: j.Err, Outcome: j.Outcome,
			Color:     jobs.EffectiveColor(j),
			Agent:     j.Agent,
			Artifacts: jobArtifacts(j, live[jobs.CellSession(j)]),
			ModTime:   j.ModTime.Unix(),
			PRs:       j.PRs,
			// Stack: the cell's manual-test URLs, store-only like Threads
			// below. Teardown deletes the rows, so a done job simply has
			// none; nothing here gates on liveness.
			Stack: webStackLinksFrom(jobs.ListStackLinks(j)),
			// DocQuestions/Feedback: one uniform path straight off the store,
			// live or dead, any state — no file, no cell-dir liveness check
			// (see jobQuestions/jobFeedback). Never set for archived jobs,
			// which ride a separate loop below.
			DocQuestions: jobQuestions(activeStore, j),
			Feedback:     jobFeedback(activeStore, j.ID),
			// Threads: same store-only path, and deliberately outside the
			// live[] branch below — a closed thread outlives its cell and
			// stays readable, so its chip must survive teardown too.
			Threads: jobThreads(activeStore, j),
			// Tokens: keyed on j.ID, like Feedback above. The rows moved off
			// the slug in schema v11 — the measured cell dir is <slug>-<id>
			// now, so two generations of one reused name no longer share a
			// directory OR a row.
			Tokens: webTokensFor(tokens, j.ID),
			// Load: straight off the job row already loaded above — no index
			// and no extra query, unlike Tokens, because it lives on `jobs`
			// itself. Emitted for every active job regardless of display
			// state: a card moves between blocked and working tick to tick,
			// so one payload shape with the clients gating on state is the
			// only version that does not flicker the key in and out.
			Load: webJobLoadFrom(j.Load),
			// Goals/Goal: resolved out of the two maps above, keyed on the job's
			// own id — an empty slice (no links, or links that resolved to
			// nothing) is exactly the omitempty absence, for both keys at once.
			// Emitted for every active job regardless of display state (Load's
			// reasoning) — a card moves between blocked and working tick to
			// tick, and its chips must not flicker with it.
			Goals: jobGoals,
			Goal:  primaryJobGoal(jobGoals),
			// Account: straight off the job row already loaded above — no
			// index and no extra query, Load's path rather than Tokens', since
			// schema v28 put the three columns on `jobs` itself. Emitted for
			// every active job that carries a binding regardless of display
			// state, for Load's no-flicker reason: the chip must not come and
			// go as a card moves between working and blocked. The rule itself
			// lives in webAccountFor so it is reachable by a test — this loop
			// is not (LiveSessions/DiscoverAll).
			Account:    webAccountFor(j),
			Visibility: j.EffectiveVisibility(),
		}
		if live[jobs.CellSession(j)] {
			wj.Cell, wj.Live = jobs.CellSession(j), true
			if j.Started() && j.State != "done" {
				board.LiveCells++
				wj.Health = jobs.CellHealth(j, all)
				zeus, transcript := cellZeusTranscript(j, all)
				if transcript != "" {
					wj.Detail = lastAssistantText(transcript)
					wj.LastSent = lastUserSent(transcript)
					wj.Question = webQuestionFrom(jobs.FindPendingQuestion(transcript))
					wj.Stage = jobs.FindStage(transcript)
					if zeus != nil && zeus.Status == "polling" && zeus.Wakeup != nil {
						wj.Wakeup = webWakeupFrom(zeus.Wakeup)
					}
				}
				var members []core.TeamMember
				if zeus != nil && !zeus.SessionIDGuessed {
					members = core.TeamMembersForLead(zeus.SessionID)
				}
				crew := jobs.CellCrew(jobs.CellSession(j), zeus, all, members)
				wj.ZeusStatus = crew.ZeusStatus
				for _, a := range crew.Agents {
					wj.Roster = append(wj.Roster, webAgent{Name: a.Name, Model: a.Model, Role: a.Role, Status: a.Status, Working: a.Working, Kind: a.Kind})
				}
			}
		} else if j.Started() && j.State != "done" {
			// dead cell: the session is gone but the worktree (and any
			// uncommitted WIP) is still on disk. Health routes the card into
			// BLOCKED immediately — a tick ahead of the daemon persisting the
			// blocked state — and the transcript's last words still surface,
			// located by the stamped session id since no live peer exists.
			wj.CellGone = true
			wj.Health = "cell session is gone — reactivate to resume; done/kill discard the worktree"
			fillLastWords(&wj, core.TranscriptPathForSession(j.Session))
		} else if j.State == "done" {
			// done: the cell is torn down but zeus' transcript outlives it,
			// and the board shows a done job the way it shows a blocked one —
			// what zeus last said, what was last said to it, which skill it
			// ended in. Read the same way the cell-gone branch reads them,
			// since no live peer exists for either. The live arm above
			// deliberately skips a done job whose session is still up (the
			// window between accept and teardown), so this is the one place
			// a done job's words come from.
			fillLastWords(&wj, core.TranscriptPathForSession(j.Session))
		}
		wj.Reactivatable = (j.State == "done" || wj.CellGone) && j.Session != ""
		byState[wj.State] = append(byState[wj.State], wj)
	}
	// emit each display-state group in lifecycle order, within-group order
	// coming from the manual board reorder (jobs/order.go): members not yet
	// in the stored order land on top (slug-sorted, since js already is),
	// then stored members in their saved order — the flat array's
	// within-state order IS the wire contract, no new key needed.
	for _, state := range []string{"draft", "working", "blocked", "done"} {
		group := byState[state]
		// slug -> queue, not a single value: duplicate-slug job files (both
		// erred into draft) must each keep their own card, so consume one
		// entry per emitted slug — ApplyColumnOrder preserves multiplicity
		wjBySlug := make(map[string][]webJob, len(group))
		slugs := make([]string, len(group))
		for i, wj := range group {
			slugs[i] = wj.Slug
			wjBySlug[wj.Slug] = append(wjBySlug[wj.Slug], wj)
		}
		for _, slug := range jobs.ApplyColumnOrder(slugs, order[state]) {
			board.Jobs = append(board.Jobs, wjBySlug[slug][0])
			wjBySlug[slug] = wjBySlug[slug][1:]
		}
	}
	// archived jobs ride the same list under state "archived": no cell, no
	// derivation — just the file, its artifacts, and the two
	// actions (unarchive, delete) keyed on the archive file base
	for _, a := range jobs.LoadArchivedJobs() {
		board.Jobs = append(board.Jobs, archivedWebJob(a, activeStore, tokens, webJobGoalsFor(goals, links[a.ID])))
	}
	// the fleet: every sibling's polled cards after this node's own, each
	// carrying its owner. Read from the registry the nodes pass keeps, so the
	// merged board costs this request no network.
	cards, nodes := fleetBoard(activeStore)
	board.Jobs = append(board.Jobs, cards...)
	board.Nodes = nodes
	return board
}

// fleetBoard is the board's fleet half: every sibling's stored cards with
// their owner, and the registry rows (without card lists). Nothing at all
// when KRAKEN_NODES=off — the knob opts out in both directions, so a
// registry left from before it was set never renders — or when the registry
// is empty. Split out of buildWebBoard so the rule is reachable by a test.
func fleetBoard(st *store.Store) ([]webJob, []webNode) {
	if st == nil || !jobs.FleetEnabled() {
		return nil, nil
	}
	rows, err := st.ListNodes()
	if err != nil || len(rows) == 0 {
		return nil, nil
	}
	nodes := make([]webNode, 0, len(rows))
	for _, n := range rows {
		nodes = append(nodes, webNodeFrom(n, false))
	}
	return remoteCards(rows), nodes
}

// archivedWebJob builds one archived job's card. Split out of buildWebBoard's
// archived loop — the sibling of archivedWebArtifacts below, and for the same
// kind of reason: the loop's body is a set of per-field DECISIONS (which of
// the active loop's keys an archived card carries and which it deliberately
// does not), and inside buildWebBoard none of them are reachable by a test,
// since calling it drags in tmux/process-table discovery (LiveSessions and
// DiscoverAll — the phrasing update_test.go and shipios_test.go already use
// for avoiding this same function). As a function they are reachable, and the
// Load decision in particular is one a later edit could reverse in one line.
// goals is the archived job's own chips, already resolved by the caller out of
// the board-wide maps — passed in rather than looked up here for the reason
// tokens is: this function must stay callable by a test, and a lookup inside it
// would need a store the archived path does not otherwise read goals from.
func archivedWebJob(a *jobs.Job, activeStore *store.Store, tokens map[int64]store.JobTokens, goals []webJobGoal) webJob {
	return webJob{
		Slug: a.ArchivedName, Repo: a.Repo,
		State: "archived", Archived: true, DisplaySlug: a.Slug,
		Err: a.Err, Outcome: a.Outcome, Color: jobs.EffectiveColor(a), Agent: a.Agent,
		Artifacts: archivedWebArtifacts(a), ModTime: a.ModTime.Unix(),
		// Feedback (Q2: works for archived jobs too) is keyed on the
		// job's own immutable id — same as the active loop above — not
		// the archive name, so a later reuse of the freed slug can never
		// cross-wire onto this card. DocQuestions is deliberately absent
		// here — never set for archived jobs.
		Feedback: jobFeedback(activeStore, a.ID),
		// Tokens IS set for an archived job — keeping the number after
		// the cell is gone is the whole reason the row has no FK and no
		// cascade. Looked up under a.ID, which also CLOSES the cross-wire
		// this comment used to describe: while the rows were slug-keyed,
		// a new active job taking the freed slug back reused the same
		// cell path, so both jobs' transcripts landed in one project dir
		// and both cards showed the one shared row. Distinct ids, distinct
		// cell dirs, distinct rows.
		//
		// The one residue is historical: transcripts written before the
		// cutover are still physically one directory for two same-slug
		// jobs, and no keying can separate them after the fact (see
		// JobProjectDirs' legacy base).
		Tokens: webTokensFor(tokens, a.ID),
		// Load is deliberately NOT set here, which is the opposite call
		// from Tokens directly above, and the two differ in what the
		// number MEANS. Token spend is a fact about work that happened,
		// and keeping it after the cell is gone is the whole reason its
		// row has no cascade. An estimate is a forward-looking triage
		// question — "is this one worth unblocking right now" — and an
		// archived job can never be blocked again without being
		// unarchived first, at which point it rides the active loop above
		// and its stored estimate (the columns live on the job row and
		// survive both directions) comes back with it. Serving it here
		// would put a number answering a question this card cannot pose
		// in front of a client, which is how a stale estimate starts
		// reading as a current fact.
		//
		// Goals IS set, which is Tokens' call rather than Load's, and for
		// Tokens' reason: the goals a job served are a fact about work that
		// happened, and an archived card answering "what was this for" is the
		// retrospective question the archive column exists to pose. Nothing
		// about it goes stale — a goal's text and done-ness are read live from
		// the same index the active cards use — and unlike a triage estimate it
		// poses no question the card cannot answer. Goal rides along as its
		// head, as everywhere: the two keys appear together or not at all.
		//
		// Account is deliberately NOT set — this function never calls
		// webAccountFor, which is where the served-for-every-active-job rule
		// lives — and that is Load's call for Load's
		// reason rather than Tokens'. Token spend is what a job COST, a fact
		// about work that happened and the whole reason its row has no
		// cascade. A binding is which login a RUNNING cell is spending on —
		// there is no cell here, so the badge would answer a question this
		// card cannot pose, which is how a dead fact starts reading as a
		// current one. The three columns live on the job row and survive
		// archival in both directions, so the key comes back the moment the
		// job is unarchived onto the active loop above.
		Goals: goals,
		Goal:  primaryJobGoal(goals),
		// Visibility is always present on the wire; an archived card is never
		// served to a sibling (/api/node/jobs skips the archive), so this is
		// only ever this board's own rendering of the level.
		Visibility: a.EffectiveVisibility(),
	}
}

// archivedWebArtifacts lists an archived job's artifact copies for the
// wire, from the DB (storeArtifactList) — the cell's artifacts tree is
// already store-mirrored by the time a job can be archived (teardown's
// syncArtifactsNow already ran).
// Single-source today (the store's own ORDER BY path), so the sort is a
// no-op — it is here so the ordering guarantee lives with every producer
// rather than only with jobArtifacts, which an archived job never reaches.
func archivedWebArtifacts(a *jobs.Job) []webArtifact {
	return sortedArtifacts(storeArtifactList(a.ArchivedName, a.ID, "archive"))
}

// boardHandler serves the memoized board snapshot.
func boardHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(webBoardCache.get())
}

func webJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// mtimeToken decodes the editor's optimistic-lock token (the row's rev,
// carried in the wire field "mtime") as either a JSON number (current
// format) or a legacy JSON string (older clients). An empty string or
// JSON null decodes to 0, which callers treat as "skip the optimistic-lock
// check" — the same meaning "0" already carried under the string encoding.
type mtimeToken int64

func (t *mtimeToken) UnmarshalJSON(data []byte) error {
	var n int64
	if err := json.Unmarshal(data, &n); err == nil {
		*t = mtimeToken(n)
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("bad mtime")
	}
	if s == "" {
		*t = 0
		return nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return fmt.Errorf("bad mtime")
	}
	*t = mtimeToken(n)
	return nil
}
