package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"kraken/internal/store"
)

// recall.go — mid-conversation delivery: carl's goals and, for a job's lead
// session, his unread notes, injected at the top of the next prompt.
//
// The shape is one Claude Code hook — UserPromptSubmit — in every cell
// worktree running `kraken recall-hook`, which POSTs the hook payload to
// kraken serve (POST /api/recall) and emits whatever comes back as
// additionalContext. The hook is a separate, short-lived OS process — it opens
// no database, exactly as pr-hook doesn't (single-writer rule: `kraken serve`
// owns kraken.db) — so the decision itself runs server-side in Recall below.
//
// Which events are wired is entirely cell.go's recallHookEvents — PostToolUse
// was wired and is retired on measurement, Stop was never wired at all, and
// that var is where both arguments live. RunRecallHook echoes whatever
// `hook_event_name` it was handed; Recall answers "" for every event but
// UserPromptSubmit.
//
// One rule shapes every line here: a hook that breaks a conversation is a bug
// by definition. Every failure anywhere — no store, no session id, a dead
// daemon, a non-200, an undecodable response — degrades to injecting
// NOTHING. There is no error path that reaches the agent's context.

// RecallRequest is POST /api/recall's body: one hook firing, as the hook
// itself saw it.
//
// Event carries hook_event_name: the goals and notes blocks ride
// UserPromptSubmit only (recallEmitsGoals), and a payload that cannot say
// which hook produced it is one nobody can debug from a log.
//
// Prompt and TranscriptPath are accepted and read by nothing: the hook still
// forwards both verbatim from Claude Code's payload, and the route decodes
// with DisallowUnknownFields, so dropping either key here alone would turn
// every firing into a 400.
//
// Slug is the CELL's job slug, baked into the hook command at install time
// (cell.go's recallHookCommand). It is the key the GOALS block uses: a cell's
// teammates and side threads fire this hook with sessions the job row has
// never heard of, and the goal a cell was pointed at is a property of the job,
// not of whichever conversation happens to be asking. EMPTY IS LEGAL and stays
// legal: a worktree whose settings.json predates this argument sends nothing
// here, and recall falls back to matching the session against the job row.
type RecallRequest struct {
	SessionID      string `json:"session_id"`
	Event          string `json:"event"`
	Prompt         string `json:"prompt"`
	TranscriptPath string `json:"transcript_path"`
	Slug           string `json:"slug"`
}

// Recall returns the block to inject for one hook firing, or "".
//
// "" is the common answer and the important one: no error value, no panic,
// and a firing with nothing new to say is a conversation that sees nothing at
// all. Only a UserPromptSubmit firing can carry anything: the goals block for
// any session that resolves to a job, and carl's unread notes ahead of it for
// the job's LEAD session alone. Notes come first, then goals.
//
// ctx is the REQUEST's context, and it is here for two specific consequences:
// a firing that queued behind a slow head bails at the lock instead of
// running for a caller that gave up (the check just inside the lock), and a
// delivery nobody received is never recorded (the check in commit).
func Recall(ctx context.Context, st *store.Store, req RecallRequest) string {
	if st == nil {
		return ""
	}
	// No session id is no per-session state key, and both blocks' suppression
	// is keyed on it.
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		return ""
	}
	if !recallEmitsGoals(req) {
		// No block rides any other event, and this refusal is cheap enough to
		// make BEFORE the lock.
		return ""
	}
	// One firing per session at a time, from the goals decision through the
	// notes mark. A cell's agents share a worktree, a session can be resumed
	// while a firing is still in flight, and a retried prompt arrives before
	// the first has recorded; two firings interleaving between the read and
	// the record would both deliver the same block. kraken serve is the sole
	// server, so an in-process mutex per session closes the race outright.
	state := recallSessionState(st, sessionID)
	state.mu.Lock()
	defer state.mu.Unlock()
	// Checked FIRST thing inside the lock: a firing that queued here behind a
	// slow head has a caller that may already have given up at
	// recallHookTimeout.
	if ctx.Err() != nil {
		return ""
	}
	// --- the goals half ---
	//
	// Goals are STATE: a goals block is delivered again every time it differs
	// from the last one this session saw, which is what the per-session
	// fingerprint below tracks.
	//
	// The fingerprint is of the CURRENT RENDERED STATE, including the empty
	// one, and that is the whole subtlety. Recording only non-empty blocks made
	// "carl deleted every goal" invisible to the suppressor: the session's last
	// recorded fingerprint stayed the deleted block's, so restoring those goals
	// rendered something that matched it and was suppressed FOREVER — the one
	// state change the feature exists to deliver. Empty is a state like any
	// other; it just has nothing to inject.
	// The job is resolved ONCE for the two blocks that need it (goals and
	// notes), and only on the event they ride: one ListJobs read per firing
	// serves both.
	job := recallResolveJob(st, req)
	goalsState, decided := recallGoalsState(st, req, job)
	goals := goalsState
	fingerprint := goalsFingerprint(goalsState)
	if fingerprint == state.lastGoals {
		goals = "" // this session has already seen exactly this state
	}
	// --- the notes half: carl's unread feedback and answers (recallnotes.go) ---
	//
	// Decided here beside goals: a note carl leaves changes neither the prompt
	// nor the transcript, and the prompt it precedes is usually the one about
	// it. Addressed to the job's LEAD session only (recallNotesTarget); every
	// other session in the cell gets "" here. Its delivery record is per job,
	// so only the lead's firings ever write it, and this session lock is what
	// serializes those.
	var notes string
	var noteKeys []string
	if recallNotesTarget(job, sessionID) {
		notes, noteKeys = recallNotesState(st, job)
	}
	// The record step, guarded on ctx: the caller is a hook that gives up at
	// recallHookTimeout, so a return value from a late path may reach nobody.
	// Recording a state that was never injected would suppress it until
	// carl's NEXT edit — the delivery lost silently. The notes record follows
	// the same rule one step further: a delivery that cannot be RECORDED is
	// withdrawn, since it would otherwise repeat on every prompt.
	//
	// `decided` is false when the goals decision did not run at all — a job
	// that did not resolve — and then nothing is recorded: the empty string
	// that path returns is "not asked", not "no goals", and conflating them
	// would re-deliver the block on the next prompt that did resolve.
	if ctx.Err() != nil {
		return goals
	}
	if decided {
		state.lastGoals = fingerprint
	}
	if notes != "" {
		if err := markNotesDelivered(st, job, noteKeys, time.Now().UnixMilli()); err != nil {
			notes = ""
		}
	}
	return notes + goals
}

// recallEmitsGoals reports whether this firing is one the goals block may ride
// on at all: UserPromptSubmit and nothing else.
//
// The narrowing is deliberate and it is about frequency, not about capability.
// It was written when PostToolUse fired this hook on EVERY tool call: the
// goals block would have been silent on almost all of them (the fingerprint
// below suppresses an unchanged block), but the ones it did fire on would land
// mid-tool-loop — carl's week arriving between a Read and an Edit.
// UserPromptSubmit lands where an instruction belongs: at the top of a turn, in
// front of the agent's own thinking. The test is kept now that
// recallHookEvents holds only that event, because the ROUTE accepts any
// payload a hook sends it and the narrowing is a property of goals rather than
// of the current wiring.
func recallEmitsGoals(req RecallRequest) bool {
	return req.Event == "UserPromptSubmit"
}

// recallGoalsState renders the goals block for one firing, plus whether the
// goals decision RAN at all.
//
// The second return is what separates "carl has no goals" (block "", decided
// true — a real state the suppressor must record) from "this firing was never
// going to carry goals" (block "", decided false — an event goals do not ride,
// or a job neither the slug nor the session resolved). Both look identical
// from the block alone, and recording the second as the first is what would
// re-deliver an unchanged block on the next firing that did resolve.
//
// job is the caller's resolution (recallResolveJob, run once per firing for
// this block and the notes block together); nil is "did not resolve", and
// the decision is then not run.
//
// The job's whole LINK SET rides into the render, which is what makes a link
// add, remove or clear re-arm the block through the fingerprint below with no
// machinery of its own: those writes change which lines carry the marker (and
// which off-period goals lead), so the rendered block differs and the
// suppressor lets exactly one delivery through.
//
// A store failure inside the render is ("", true): no read is worth a broken
// turn, and a transient failure that records "no goals" self-heals on the next
// firing, which re-renders and re-arms.
func recallGoalsState(st *store.Store, req RecallRequest, job *store.Job) (string, bool) {
	if !recallEmitsGoals(req) || job == nil {
		return "", false
	}
	return renderGoalsBlock(st, job.GoalIDs), true
}

// recallResolveJob finds the job this firing belongs to, or nil.
//
// The job is resolved by SLUG first — current name, then former names, since a
// rename moves neither the cell's installed hook command nor the worktree it
// fires from — and only then by session, which is the pre-slug fallback for a
// settings.json written before recallHookCommand carried one. Slug first
// rather than session first because it is the answer that works for a cell's
// TEAMMATES and side threads: their sessions appear on no job row, and they are
// working on the same job with the same goals.
//
// One ListJobs read serves all three lookups (current slug, former slugs,
// session), which is also why it is one query rather than three round trips.
func recallResolveJob(st *store.Store, req RecallRequest) *store.Job {
	all, err := st.ListJobs()
	if err != nil {
		return nil
	}
	if slug := strings.TrimSpace(req.Slug); slug != "" {
		for _, j := range all {
			if j.Slug == slug {
				return j
			}
		}
		for _, j := range all {
			for _, former := range j.FormerSlugs {
				if former == slug {
					return j
				}
			}
		}
	}
	if sessionID := strings.TrimSpace(req.SessionID); sessionID != "" {
		for _, j := range all {
			if j.Session == sessionID {
				return j
			}
		}
	}
	return nil
}

// goalsFingerprint is the per-session change key: two firings whose rendered
// goals block hashes the same are the same state, and the second one says
// nothing. A hash rather than the block itself because this is process state
// held per live session and the comparison is the only thing it is ever used
// for.
func goalsFingerprint(block string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(block))
	return strconv.FormatUint(h.Sum64(), 16)
}

// recallSession is one session's serialization point and its goals memory.
// lastGoals is guarded by mu, which Recall holds from the goals decision
// through the notes mark.
//
// lastGoals is the fingerprint of the last GOALS STATE this session was shown —
// "" until a goals decision has run for it, and thereafter the fingerprint of
// whatever was rendered, the empty render included. It is a state key, not a
// delivery record: an empty render injects nothing but still moves the state,
// which is what makes deleting every goal and putting one back a change this
// session is told about.
//
// Process state by design: a serve restart may re-emit one unchanged goals
// block per live session, which is a duplicate paragraph rather than a lost
// one, and the alternative (a table) would make goals a delivery record
// instead of the piece of live state they are.
type recallSession struct {
	mu        sync.Mutex
	lastGoals string
}

// recallSessionKey keys the state map by STORE as well as session, because
// the goals fingerprint's claim is "this session was already shown this state
// from this store". In `kraken serve` the distinction is invisible (one store
// for the process' life); what it keeps honest is every context where stores
// come and go (tests open one per case), where a session-only key would let
// one store's delivery suppress another store's.
type recallSessionKey struct {
	st *store.Store
	id string
}

// recallStatesCap bounds the state map. Sessions on one machine number in the
// hundreds, so 4096 is a leak backstop rather than a working-set size; past
// it the whole map is dropped, which costs each live session at most one
// re-delivered goals block and, in the worst interleaving, lets one
// already-in-flight firing race a fresh one exactly once — the same bounded,
// duplicate-block-at-worst outcome a serve restart has, since this map is
// process state by design.
const recallStatesCap = 4096

var (
	recallStatesMu sync.Mutex
	recallStates   = map[recallSessionKey]*recallSession{}
)

// recallSessionState returns the per-(store, session) state, creating it on
// first sight. The outer lock covers only the map; the per-session lock
// inside is what Recall actually holds across its reads and writes, so one
// session's firing never blocks another session's.
func recallSessionState(st *store.Store, id string) *recallSession {
	recallStatesMu.Lock()
	defer recallStatesMu.Unlock()
	key := recallSessionKey{st: st, id: id}
	if s, ok := recallStates[key]; ok {
		return s
	}
	if len(recallStates) >= recallStatesCap {
		recallStates = map[recallSessionKey]*recallSession{}
	}
	s := &recallSession{}
	recallStates[key] = s
	return s
}

// --- the hook client: `kraken recall-hook` ---

// recallHookTimeout bounds the POST to kraken serve. A hook blocks the
// conversation's turn while it runs, so a daemon that is down or wedged must
// cost four seconds, never Claude Code's own (much longer) hook timeout —
// the whole point of the feature is that it is invisible when it has nothing
// to say, and a turn that hangs is the opposite of invisible.
const recallHookTimeout = 4 * time.Second

// recallHookPayload is the subset of Claude Code's hook payloads this verb
// reads. Prompt and TranscriptPath are forwarded into RecallRequest unchanged
// (the server reads neither; the request shape keeps both keys).
// Parsed defensively, prhook.go's stance: the schema is owned by Claude Code
// and can drift out from under this file at any time, and a field that stops
// arriving must degrade to an empty string rather than to a failed turn.
type recallHookPayload struct {
	HookEventName  string `json:"hook_event_name"`
	SessionID      string `json:"session_id"`
	AgentID        string `json:"agent_id"`
	Prompt         string `json:"prompt"`
	TranscriptPath string `json:"transcript_path"`
	StopHookActive bool   `json:"stop_hook_active"`
}

// hookStdout is the exact shape Claude Code reads a hook's stdout as. A
// struct rather than a map so the two keys marshal in a fixed order and the
// spelling — camelCase, unlike every other wire shape in this binary — is
// declared once, where it is owned by Claude Code rather than by kraken. The
// recall hook (UserPromptSubmit) and the pr-hook (PostToolUse) both emit it.
type hookStdout struct {
	Specific hookStdoutSpecific `json:"hookSpecificOutput"`
}

type hookStdoutSpecific struct {
	HookEventName     string `json:"hookEventName"`
	AdditionalContext string `json:"additionalContext"`
}

// RunRecallHook is `kraken recall-hook`'s body: read one hook payload from in,
// ask kraken serve what to inject, and write the injection to out — or write
// NOTHING, which is every other outcome.
//
// Nothing is written on: an unreadable or unparseable payload, a
// stop_hook_active firing, a transport failure, a non-200, an undecodable
// response, or an empty context. Problems go to stderr, which Claude Code
// shows the user without feeding it to the agent. It never returns an error
// and never exits non-zero; the caller (cmdRecallHook) exits 0 unconditionally.
//
// Event-agnostic on purpose: it reads no event name except to echo it back, so
// wiring or unwiring an event is a change to cell.go's recallHookEvents alone.
//
// slug is the cell's job slug, forwarded verbatim into the request body. It is
// VARIADIC rather than a third parameter because it is genuinely optional at
// both ends: `kraken recall-hook` with no bare argument is a valid invocation
// (every settings.json written before cell.go started baking the slug in), and
// the two-argument call is the shape every existing caller already uses. At
// most one is read; anything past the first is ignored rather than refused,
// since a hook that dies on an unexpected argument fails the turn it fired on.
func RunRecallHook(in io.Reader, out io.Writer, slug ...string) {
	cellSlug := ""
	if len(slug) > 0 {
		cellSlug = strings.TrimSpace(slug[0])
	}
	raw, err := io.ReadAll(in)
	if err != nil {
		fmt.Fprintf(os.Stderr, "recall-hook: read stdin: %v\n", err)
		return
	}
	var p recallHookPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		fmt.Fprintf(os.Stderr, "recall-hook: parse payload: %v\n", err)
		return
	}
	// The loop guard. The hooks reference documents stop_hook_active around
	// Stop's decision:"block" continuation; whether Claude Code also sets it
	// for a continuation caused by additionalContext alone is NOT verified —
	// nothing in the docs says so, and no Stop firing exists here to measure.
	// Inert while the one wired event does not set it (a UserPromptSubmit
	// payload never carries the flag); kept so that re-enabling
	// Stop starts from a guard that may work rather than none — but re-testing
	// this flag against a real additionalContext continuation is part of that
	// change, not something this comment can promise.
	if p.StopHookActive {
		return
	}
	// A non-empty agent_id means an IN-PROCESS SUBAGENT's tool call fired this
	// hook: the hooks reference documents that settings hooks run inside
	// subagents and that their payloads carry agent_id, and that a subagent
	// shares the parent's session is measured (sidecar transcripts hold the
	// parent's sessionId verbatim) — the payload field itself is the
	// documented half, not the measured one.
	// Injecting there would spend the parent session's delivery records (the
	// goals fingerprint, the notes mark) on a context that is discarded when
	// the subagent finishes. Subagents are read-only exploration in this
	// codebase; they get nothing. Teammates are unaffected — a
	// teammate is its own process with its own session and no agent_id.
	if strings.TrimSpace(p.AgentID) != "" {
		return
	}
	body, err := json.Marshal(RecallRequest{
		SessionID:      p.SessionID,
		Event:          p.HookEventName,
		Prompt:         p.Prompt,
		TranscriptPath: p.TranscriptPath,
		Slug:           cellSlug,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "recall-hook: encode request: %v\n", err)
		return
	}
	client := http.Client{Timeout: recallHookTimeout}
	resp, err := client.Post(ServeBaseURL()+"/api/recall", "application/json", bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(os.Stderr, "recall-hook: post: %v\n", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "recall-hook: serve answered %d\n", resp.StatusCode)
		return
	}
	var decoded struct {
		Context string `json:"context"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		fmt.Fprintf(os.Stderr, "recall-hook: decode response: %v\n", err)
		return
	}
	if decoded.Context == "" {
		return
	}
	// hookEventName is echoed VERBATIM from the payload rather than derived:
	// Claude Code matches a hook's output against the event it fired, so a
	// name this process guessed would be a name it could get wrong.
	msg, err := json.Marshal(hookStdout{
		Specific: hookStdoutSpecific{
			HookEventName:     p.HookEventName,
			AdditionalContext: decoded.Context,
		},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "recall-hook: encode output: %v\n", err)
		return
	}
	if _, err := out.Write(msg); err != nil {
		fmt.Fprintf(os.Stderr, "recall-hook: write output: %v\n", err)
	}
}
