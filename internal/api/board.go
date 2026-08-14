package api

import (
	"encoding/json"
	"fmt"
	"net/http"
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
const apiContract = 6

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
	Model     string `json:"model,omitempty"`
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
		out[i] = webThread{
			TID: t.TID, Title: t.Title, State: t.State, Pane: t.Pane,
			Model: t.Model, CreatedAt: t.CreatedAt, ClosedAt: t.ClosedAt,
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
	// Detail is the cell's last words: for a live started cell, the full
	// text of the last assistant text block of zeus' transcript, capped at
	// 4000 chars.
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
	// Goal is the goal this job serves (goals.go), present exactly when the
	// job links one. A pointer with omitempty for the reason Load is one: a
	// zeroed object would claim a link to goal 0, and there is no such goal.
	//
	// It carries the goal's TEXT rather than only its id, so a card can render
	// the chip from the board payload alone — the alternative (an id the
	// client resolves against the board's top-level "goals") would be blank
	// for every job whose goal is not in the current period, which is exactly
	// the job most worth labelling.
	//
	// ADDITIVE — a new optional key on an existing shape — so apiContract does
	// NOT bump. A client that does not know the key renders the card exactly
	// as it did before goals existed.
	Goal *webJobGoal `json:"goal,omitempty"`
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
}

// jobArtifacts collects a job's viewable artifacts. A live cell always
// serves straight from the cell dir (Q12), unchanged regardless of storage
// mode. Once the cell is gone: file mode keeps its original pluto-copy
// listing; store mode (L4+E6) UNIONS three sources — the (normally empty,
// post-teardown) cell-dir walk, the artifacts table (the block-event
// sync's mirror, which — unlike the pluto flat-copy preservation — keeps
// nested subdirs), and the pluto flat <slug>.<name> copies — deduped by
// name with precedence cell > store > pluto. The pluto source can't be
// dropped in store mode: a job's root-level *.md notes are preserved
// straight from the worktree root by cell.go's preserveArtifacts, never
// through CellArtifacts()/artifacts/, so jobs/sync.go's syncCellArtifacts
// (which only ever mirrors the artifacts/ subdir) never captures them into
// the store's artifacts table at all — the pluto flat copy is the SOLE
// record of that deliverable, store mode or not. Dropping it (the
// pre-L4/E6 behavior: store mode returned cell ∪ store only) made a job's
// root-level notes vanish from the board entirely the moment its cell was
// torn down.
//
// Both branches end in sortedArtifacts — see its comment for why sources that
// are each sorted do not append into a sorted list, and why the live branch
// goes through it too even though its single source already is.
func jobArtifacts(j *jobs.Job, live bool) []webArtifact {
	seen := map[string]bool{}
	var out []webArtifact
	add := func(entries []webArtifact) {
		for _, e := range entries {
			if seen[e.Name] {
				continue // lower-precedence source already lost this name
			}
			seen[e.Name] = true
			out = append(out, e)
		}
	}

	root := jobs.CellArtifacts(j)
	var cellEntries []webArtifact
	for _, p := range jobs.ListArtifacts(j) {
		// name is the path relative to the artifacts dir, so nested files
		// read as "icon-variations/icon-v1.png" and clients can group by dir
		name := filepath.Base(p)
		if rel, err := filepath.Rel(root, p); err == nil {
			name = rel
		}
		cellEntries = append(cellEntries, webArtifact{Name: name, URL: "/api/artifact/" + j.Slug + "/cell/" + name})
	}
	add(cellEntries)

	// Q12: a live cell always serves straight from the cell dir — nothing
	// else to add once cellEntries is in. It still goes through the sort:
	// its one source is already ordered, so this is a no-op today, but the
	// wire guarantee must not rest on that staying true.
	if live {
		return sortedArtifacts(out)
	}
	add(storeArtifactList(j.Slug, j.ID, "pluto"))
	add(plutoFlatArtifacts(j))
	return sortedArtifacts(out)
}

// sortedArtifacts puts one job's artifact list into the single reading order
// ios/API.md promises for the `artifacts` array, and is the ONE place that
// promise is kept — every producer of a wire artifact list ends in a call to
// it, so a future second source added to either producer inherits the
// guarantee instead of silently breaking it.
//
// It exists because each source arrives sorted WITHIN itself (ListArtifacts'
// sort.Strings, the store's ORDER BY path, ReadDir's lexical order) and
// appending sorted lists does not produce a sorted one: a job whose
// artifacts/ tree is mirrored in the store while a root-level note survives
// only as a pluto flat copy served 00-overview.md AFTER 90-build-report.md.
// That was survivable while the list was a row of chips you scan, and stopped
// being so once the clients gained next/prev — the wire order IS the reading
// order now, and the overview was four steps from the front of its own set.
//
// Sorting the deduped union rather than the sources keeps jobArtifacts' cell
// > store > pluto precedence intact: `add` has already dropped the losers by
// the time this runs, so no entry here can displace a higher-precedence one.
// Pinned by TestJobArtifactsStoreModeUnionIsGloballySorted (torn-down, the
// multi-source case) and TestJobArtifactsLiveCellIsSorted (live, the
// single-source case that reaches the wire without passing through the union).
func sortedArtifacts(out []webArtifact) []webArtifact {
	sort.SliceStable(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out
}

// plutoFlatArtifacts scans PlutoDir for j's <slug>-<id>.<name> flat copies —
// preserveArtifacts' preservation format (cell.go). Pulled out of
// jobArtifacts so both the file-mode branch and the store-mode union
// (L4+E6) share one implementation.
//
// The prefix comes from jobs.PlutoArtifactPrefix rather than being spelled
// out here: this reader and preserveArtifacts' writer disagreeing about the
// string is a job whose preserved artifacts silently vanish from its card.
func plutoFlatArtifacts(j *jobs.Job) []webArtifact {
	prefix := jobs.PlutoArtifactPrefix(j)
	entries, err := os.ReadDir(jobs.PlutoDir())
	if err != nil {
		return nil
	}
	var out []webArtifact
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, prefix) || strings.HasSuffix(name, jobs.JobSuffix) {
			continue
		}
		out = append(out, webArtifact{Name: strings.TrimPrefix(name, prefix), URL: "/api/artifact/" + j.Slug + "/pluto/" + strings.TrimPrefix(name, prefix)})
	}
	return out
}

// storeArtifactList lists jobID's DB-mirrored artifacts (store/artifacts.go,
// populated by the block-event sync — jobs/sync.go) as webArtifacts under
// the given URL "home" segment. The URL scheme is unchanged from file mode
// on purpose (Q12 only moves where the BYTES are served from, not the wire
// shape): ArtifactBytes/artifactHandler resolve any non-"cell" home through
// the store first whenever one is wired, falling back to ArtifactPath's
// filesystem lookup otherwise. home is "pluto" for an active (done) job's
// artifacts, "archive" for an archived job's.
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
		out = append(out, webArtifact{Name: a.Path, URL: "/api/artifact/" + slug + "/" + home + "/" + a.Path})
	}
	return out
}

// ArtifactPath resolves an artifact request to a real file, refusing anything
// that escapes the two artifact homes (the URL is attacker-adjacent input:
// any local process can hit the port). A cell name may be a subpath
// (icon-variations/icon-v1.png) so folders are navigable, but every segment is
// validated against traversal and hidden files.
func ArtifactPath(j *jobs.Job, home, name string) (string, bool) {
	name = strings.TrimPrefix(name, "/")
	if name == "" {
		return "", false
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == "" || seg == "." || seg == ".." || strings.HasPrefix(seg, ".") {
			return "", false // no traversal, no empty/hidden segments
		}
	}
	var p, root string
	switch home {
	case "cell":
		root = jobs.CellArtifacts(j)
		p = filepath.Join(root, name)
	case "pluto":
		// pluto copies are flat (<slug>-<id>.<name>), never nested
		if strings.Contains(name, "/") {
			return "", false
		}
		root = jobs.PlutoDir()
		p = filepath.Join(root, jobs.PlutoArtifactPrefix(j)+name)
	case "archive":
		// Archived copies carry the SAME <slug>-<id>. prefix as live ones:
		// ArchiveJob moves them across without renaming, because the id
		// already disambiguates two generations of a name — which is what
		// the archive's numbered -2/-3 prefix used to be for. ArchivedName
		// remains the archived job's identity in the jobs table; it just no
		// longer names files.
		if j.ArchivedName == "" || strings.Contains(name, "/") {
			return "", false
		}
		root = jobs.ArchiveDir()
		p = filepath.Join(root, jobs.PlutoArtifactPrefix(j)+name)
	default:
		return "", false
	}
	// the flat homes join <prefix>.<name>, so a name like "job.md" would
	// reassemble the job FILE's own path — job files are never artifacts
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
// job+name straight from the artifacts table instead of a filesystem home,
// for any non-"cell" home once a store is wired — a done/archived job's
// "pluto"/"archive" homes no longer need a file on disk to serve from. ok
// is false whenever no store is wired, j has no store row, or the path
// isn't found in the table — callers fall back to ArtifactPath, which is
// always safe (preserveArtifacts/movePrefixed still write the file copies
// too, per jobs/cell.go and jobs/archive.go's own store-mode branches).
//
// L11: home is now a required parameter, refused outright when "cell" —
// Q12's rule is that a live cell's artifacts are NEVER DB-backed, only
// ever served straight off disk. Without this, a caller that fell back to
// ArtifactBytes after an ArtifactPath("cell", ...) miss (feedback.go's
// resolveArtifact does exactly this for non-cell homes: ArtifactBytes
// first, ArtifactPath as the fallback — the same precedence artifactHandler
// itself serves bytes from, B6) could have the DB serve bytes for a
// same-named artifact that actually lives under a DIFFERENT home (e.g. a
// "pluto" flat copy sharing a filename with a would-be cell file) —
// silently mislabeling which home the returned bytes actually came from.
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
	board := webBoard{Daemon: st, DaemonOK: ok && jobs.ProcessAlive(st.PID), CellCap: legacyCellCap, Jobs: []webJob{}, Contract: apiContract, Stale: util.IsSelfStale(), Update: currentUpdate(), ShipIOS: currentShipIOS(), Goals: currentGoals(activeStore)}
	order := jobs.LoadBoardOrder()
	// The card chips, built from the two lists the payload ALREADY carries plus
	// a by-id read for each linked goal those lists do not cover (a job still
	// serving last week's goal). Scoped to what the loaded jobs actually link
	// rather than to the whole table, which only grows — see linkedJobGoals.
	goals := linkedJobGoals(activeStore, board.Goals, js)
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
		wj := webJob{
			Slug: j.Slug, Repo: j.Repo,
			State: displayState, Err: j.Err, Outcome: j.Outcome,
			Color:     jobs.EffectiveColor(j),
			Artifacts: jobArtifacts(j, live[jobs.CellSession(j)]),
			ModTime:   j.ModTime.Unix(),
			PRs:       j.PRs,
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
			// Goal: resolved out of the index above, keyed on the job row's own
			// goal_id — a nil map entry (no link, or a link that resolved to
			// nothing) is exactly the omitempty absence. Emitted for every
			// active job regardless of display state (Load's reasoning) — a card
			// moves between blocked and working tick to tick, and the chip must
			// not flicker with it.
			Goal: goals[j.GoalID],
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
					wj.Roster = append(wj.Roster, webAgent{Name: a.Name, Model: a.Model, Role: a.Role, Status: a.Status, Working: a.Working})
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
			if transcript := core.TranscriptPathForSession(j.Session); transcript != "" {
				wj.Detail = lastAssistantText(transcript)
				wj.LastSent = lastUserSent(transcript)
			}
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
		board.Jobs = append(board.Jobs, archivedWebJob(a, activeStore, tokens))
	}
	return board
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
func archivedWebJob(a *jobs.Job, activeStore *store.Store, tokens map[int64]store.JobTokens) webJob {
	return webJob{
		Slug: a.ArchivedName, Repo: a.Repo,
		State: "archived", Archived: true, DisplaySlug: a.Slug,
		Err: a.Err, Outcome: a.Outcome, Color: jobs.EffectiveColor(a),
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

// jobMTime is a job file's mtime as unix milliseconds — the wire format for
// every editor endpoint. UnixMilli (~1.75e12) fits safely in both a Swift
// Int64 and a JS number, unlike UnixNano, which overflows JS's 2^53
// safe-integer range and forced the old string encoding. fi is nil when the
// preceding os.Stat failed; callers get 0 rather than a panic.
func jobMTime(fi os.FileInfo) int64 {
	if fi == nil {
		return 0
	}
	return fi.ModTime().UnixMilli()
}

// mtimeToken decodes an mtime from the wire as either a JSON number (current
// format) or a legacy JSON string (older clients that still send the
// pre-UnixMilli encoding), normalizing to unix-millis. An empty string or
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
