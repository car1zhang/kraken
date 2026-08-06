package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"kraken/internal/core"
	"kraken/internal/store"
)

// recall.go — mid-conversation memory: the same graph facts graphctx.go
// injects at spawn time, delivered again when the conversation turns to
// something the graph already knows about.
//
// The shape is two Claude Code hooks — UserPromptSubmit (query: the user's
// new message) and PostToolUse (query: the assistant's last text, refreshed
// per tool call) — in every cell worktree running `kraken recall-hook`, which
// POSTs the hook payload to kraken serve and emits whatever comes back as
// additionalContext. The hook is a separate, short-lived OS process — it
// opens no database, exactly as pr-hook doesn't (single-writer rule: `kraken
// serve` owns kraken.db) — so the retrieval itself runs server-side in Recall
// below.
//
// Nothing here is specific to those events: RunRecallHook echoes whatever
// `hook_event_name` it was handed, and Recall never reads it. Which events are
// wired is entirely cell.go's recallHookEvents, and Stop is deliberately not
// among them — see that var for why.
//
// Two rules shape every line here, and they are the same two graphctx.go
// states for the spawn path, read one level up:
//
//  1. A hook that breaks a conversation is a bug by definition. Every failure
//     anywhere — no store, no session id, a dead daemon, a non-200, an
//     undecodable response — degrades to injecting NOTHING. There is no error
//     path that reaches the agent's context.
//
//  2. A memory is injected at most once per session. The block is unsolicited
//     text in someone else's conversation; the same five facts arriving on
//     every prompt would be noise the agent learns to skip, which costs the
//     one delivery that mattered. That is what kg_recall records, and it is
//     why an undeduplicatable delivery is refused below rather than sent.

// RecallRequest is POST /api/graph/recall's body: one hook firing, as the
// hook itself saw it.
//
// Event carries hook_event_name for the server's benefit only — Recall does
// not branch on it (a UserPromptSubmit and a PostToolUse ask the same question
// of the graph, they just differ in where the query text comes from: the new
// prompt vs the transcript's last assistant text), but a payload that cannot
// say which hook produced it is one nobody can debug from a log.
type RecallRequest struct {
	SessionID      string `json:"session_id"`
	Event          string `json:"event"`
	Prompt         string `json:"prompt"`
	TranscriptPath string `json:"transcript_path"`
}

// Recall returns the block to inject for one hook firing, or "".
//
// "" is the common answer and the important one, exactly as it is for
// graphContext: no error value, no panic, and a conversation the graph has
// nothing new for is a conversation that sees nothing at all.
//
// The query is the user's new message when there is one, and the assistant's
// last text otherwise. The fallback is the POSTTOOLUSE path: that payload
// carries no `prompt`, so every post-tool-call firing queries on what the
// assistant last said — and a payload that omits `prompt` through schema
// drift lands on the same safe branch, which is exactly prhook.go's stance.
//
// ctx is the REQUEST's context, and it is here for two specific consequences:
// a firing that queued behind a slow head bails at the lock instead of
// running retrieval for a caller that gave up (the check just inside the
// lock), and a delivery nobody received is never marked (the check before
// the mark).
func Recall(ctx context.Context, st *store.Store, req RecallRequest) string {
	if st == nil {
		return ""
	}
	// No session id is no dedup key, and without one this would re-inject the
	// same facts on every single firing. Refusing is the whole of rule 2.
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		return ""
	}
	query := strings.TrimSpace(req.Prompt)
	if query == "" {
		query = strings.TrimSpace(core.LastAssistantText(req.TranscriptPath))
	}
	if query == "" {
		return ""
	}
	// One firing per session at a time, from here through the mark. With
	// PostToolUse wired, parallel tool calls in one assistant message fire
	// CONCURRENT hooks for one session — the exact producer the old
	// read-then-mark window had been ruled safe for lacking — and two firings
	// interleaving in that window would both pass the same skip-set and
	// double-inject. kraken serve is the sole server, so an in-process mutex
	// per session closes the race outright; overlapping firings queue, and
	// the ones behind the head see its marks (or short-circuit below).
	state := recallSessionState(st, sessionID)
	state.mu.Lock()
	defer state.mu.Unlock()
	// Checked FIRST thing inside the lock: a firing that queued here behind a
	// slow embed has a caller that gave up at recallHookTimeout long ago, and
	// without this check each queued corpse would run its own full retrieval
	// and embed for nobody — under a degraded embedder that is one wasted paid
	// call per tool call, forever, with the queue draining at the embed rate.
	// (The pre-mark check below still stands: a caller can also vanish DURING
	// this firing's own slow span.)
	if ctx.Err() != nil {
		return ""
	}
	// Within one assistant message, every PostToolUse firing after the first
	// carries the SAME LastAssistantText — so a query byte-identical to the
	// last one this session completed is answered without touching the store,
	// the embedder, or the graph. This is what makes per-tool-call wiring
	// affordable: real retrieval re-arms exactly when the query changes (a
	// new prompt, a new assistant message). Cost optimization only — set on
	// COMPLETED passes alone: a canceled context, a skip-set read error, or a
	// mark error never suppresses a retry. The one deliberate exception is a
	// failure INSIDE retrieval, which arms like an empty graph because the
	// seam collapses the two (see the comment at the graphFacts call below);
	// kg_recall still owns correctness if this process restarts.
	if query == state.lastQuery {
		return ""
	}
	// Fail CLOSED: not knowing what this session has already been told is not
	// a reason to inject anyway. Read before retrieval so the refusal does not
	// depend on whether the graph happened to have a match. (A store error
	// deliberately does not arm the short-circuit: the next firing retries.)
	skip, err := st.RecalledEdgeIDs(sessionID)
	if err != nil {
		return ""
	}

	// Unlike the spawn path there is no cached vector to read — the query is a
	// message that did not exist a second ago — so this is the one place the
	// graph's injection pays for an embedding. A missing key, a down API, a
	// rate limit: all land on the same nil vector, which is
	// core.GraphSearch's documented BM25-only mode, never a failure. Same
	// treatment POST /api/graph/search gives it.
	//
	// REDACTED before it goes: this is the only path in the binary that ships
	// a raw user prompt off the machine, and a message asking why a token is
	// 401ing is both a plausible prompt and one that must not reach
	// api.openai.com verbatim. Only the EMBEDDER's copy is redacted — the
	// BM25 leg below searches the unredacted query, which never leaves
	// kraken.db, so a credential-shaped string costs no matches locally.
	var vec []float32
	if emb, err := core.NewEmbedder(); err == nil {
		if vecs, err := emb.Embed([]string{core.RedactSecrets(query)}); err == nil && len(vecs) == 1 {
			vec = vecs[0]
		}
	}

	// Through the graphFacts seam rather than core.GraphSearch directly, so
	// this path inherits searchFacts' vector-leg bound (graphctx.go's
	// graphVectorLegMaxEdges) — which matters MORE here than at spawn time:
	// a spawn happens once per job, this runs on every prompt AND every
	// distinct assistant message of every live session (the short-circuit
	// above is what holds it to that rate rather than to the raw
	// per-tool-call one).
	// A search failure collapses to nil here by the seam's design, so the
	// empty-arm below cannot tell it from an empty graph — accepted (contract
	// amendment 7's narrowing): the loss is one message's injection, and it
	// self-heals when the query next changes.
	// The anchors come back with the edges rather than being re-resolved here:
	// they are the search's own answer to "which nodes did this query name",
	// and keepRelevantFacts waives core.RelevantEdge for the facts touching
	// them. Nothing else about this function moves — an anchored delivery is
	// marked in kg_recall like any other, so rule 2 (once per session) covers
	// the anchor path exactly as it covers the ordinary one.
	edges, anchors := graphFacts(st, vec, query)
	if len(edges) == 0 {
		state.lastQuery = query
		return ""
	}
	kept := keepRelevantFacts(edges, vec, core.ContentTerms(query), skip, anchors)
	block, rendered := renderGraphBlock(kept)
	if block == "" {
		state.lastQuery = query
		return ""
	}

	// The caller is a hook that gives up at recallHookTimeout (4s) while the
	// embedder above has a budget an order of magnitude longer. When one slow
	// embed outlasts the hook, the HTTP request is cancelled and nothing this
	// function returns can reach the conversation — so marking here would
	// record the session's best facts as delivered when nobody ever saw them,
	// and the skip-set would withhold them forever after. Checked immediately
	// before the write, because that is the last moment the answer can change.
	if ctx.Err() != nil {
		return ""
	}
	// Recorded BEFORE returning, and a write failure withdraws the block: a
	// delivery this process cannot dedupe is one that would repeat on the next
	// prompt and every prompt after it, which is worse than the memory not
	// arriving at all.
	//
	// The ids marked are the ones renderGraphBlock actually EMITTED, not the
	// wider set the gate kept — the byte cap can skip a long fact and take a
	// shorter one ranked below it, and a fact nobody read is not a fact
	// delivered. It converges rather than starving that fact: every fact the
	// block DID carry leaves the candidate pool through the skip-set, so on
	// the next firing WITH A NEW QUERY (the short-circuit swallows repeats of
	// this one) the skipped fact is ranked against a smaller field, and the
	// cap it lost to is being spent on fewer competitors each time.
	if err := st.MarkRecalled(sessionID, rendered, time.Now().UnixMilli()); err != nil {
		return ""
	}
	state.lastQuery = query
	return block
}

// recallSession is one session's serialization point and its short-circuit
// memory. lastQuery is guarded by mu, which Recall holds from the skip-set
// read through the mark.
type recallSession struct {
	mu        sync.Mutex
	lastQuery string
}

// recallSessionKey keys the state map by STORE as well as session, because
// the short-circuit's claim is "this session already processed this query
// against this graph" — and the graph is the store. In `kraken serve` the
// distinction is invisible (one store for the process' life); what it keeps
// honest is every context where stores come and go (tests open one per case),
// where a session-only key would let one store's completed pass suppress a
// retrieval against another store that never delivered anything.
type recallSessionKey struct {
	st *store.Store
	id string
}

// recallStatesCap bounds the state map. Sessions on one machine number in the
// hundreds, so 4096 is a leak backstop rather than a working-set size; past
// it the whole map is dropped, which costs each live session one redundant
// retrieval (kg_recall still dedups the delivery itself) and, in the worst
// interleaving, lets one already-in-flight firing race a fresh one exactly
// once — the same bounded, duplicate-block-at-worst outcome a serve restart
// has, since this map is process state by design.
const recallStatesCap = 4096

var (
	recallStatesMu sync.Mutex
	recallStates   = map[recallSessionKey]*recallSession{}
)

// recallSessionState returns the per-(store, session) state, creating it on
// first sight. The outer lock covers only the map; the per-session lock
// inside is what Recall actually holds across its slow span, so one session's
// embed call never blocks another session's firing.
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

// recallHookPayload is the subset of Claude Code's UserPromptSubmit and
// PostToolUse hook payloads this verb reads — TranscriptPath is the busiest
// field now, since a PostToolUse payload carries no prompt. Parsed
// defensively, prhook.go's stance: the schema is owned by Claude Code and can
// drift out from under this file at any time, and a field that stops arriving
// must degrade to an empty string rather than to a failed turn.
type recallHookPayload struct {
	HookEventName  string `json:"hook_event_name"`
	SessionID      string `json:"session_id"`
	AgentID        string `json:"agent_id"`
	Prompt         string `json:"prompt"`
	TranscriptPath string `json:"transcript_path"`
	StopHookActive bool   `json:"stop_hook_active"`
}

// recallHookOutput is the exact shape Claude Code reads a hook's stdout as.
// A struct rather than a map so the two keys marshal in a fixed order and the
// spelling — camelCase, unlike every other wire shape in this binary — is
// declared once, where it is owned by Claude Code rather than by kraken.
type recallHookOutput struct {
	Specific recallHookSpecific `json:"hookSpecificOutput"`
}

type recallHookSpecific struct {
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
func RunRecallHook(in io.Reader, out io.Writer) {
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
	// Inert while neither wired event sets it (UserPromptSubmit and
	// PostToolUse payloads never carry the flag); kept so that re-enabling
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
	// Injecting there would spend the parent session's once-per-fact budget on
	// a context that is discarded when the subagent finishes, permanently
	// denying zeus facts nobody ever read. Subagents are read-only exploration
	// in this codebase; they get no memories. Teammates are unaffected — a
	// teammate is its own process with its own session and no agent_id.
	if strings.TrimSpace(p.AgentID) != "" {
		return
	}
	body, err := json.Marshal(RecallRequest{
		SessionID:      p.SessionID,
		Event:          p.HookEventName,
		Prompt:         p.Prompt,
		TranscriptPath: p.TranscriptPath,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "recall-hook: encode request: %v\n", err)
		return
	}
	client := http.Client{Timeout: recallHookTimeout}
	resp, err := client.Post(ServeBaseURL()+"/api/graph/recall", "application/json", bytes.NewReader(body))
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
	msg, err := json.Marshal(recallHookOutput{
		Specific: recallHookSpecific{
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
