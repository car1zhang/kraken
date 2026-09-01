package api

import (
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"kraken/internal/core"
	"kraken/internal/jobs"
	"kraken/internal/store"
)

// orch.go — kronos' HTTP surface: the orchestrator inbox it reads, the ack it
// writes back, the two hook reports its pane's harness makes (context
// percentage, post-compact recall re-arm), and the one status read the
// board's KRONOS view polls.
//
// kronos is a long-running claude outside any cell, iris' sibling: same
// "never opens kraken.db" rule (single-writer), same localhost proxy shape
// through `kraken orch …` / `kraken cell …`
// (cmd/kraken/cmd_orch.go and friends), same wire-field-withholding rule as
// slack.go. Nothing here reads or writes a job file directly — the cell verbs
// kronos runs go through the board's own /api/steer, /api/queue,
// /api/question and /api/doc-question routes, so every guard those already
// carry applies unchanged and this file adds no second, weaker door onto a
// live cell.
//
// Every route here is a NEW PATH rather than a key added to, retyped in, or
// removed from an existing shape, so none of them bumps `apiContract` — the
// bump policy stated in ios/API.md's header and pinned by
// TestAPIContractUnchanged.
//
// Reads vs writes under --mirror: the two GETs stay open (a read is a read,
// the same call /api/slack/inbox and /api/graph make), and the POSTs are
// mirror-gated at registration (register.go) with the standard plain-text
// 403. Gating at registration rather than inline is available for all of them
// for the reason it is on /api/slack/ack: each is a POST-ONLY path, so there
// is no GET on it for a route wrapper to take down.
//
// The refusal matrix, stated once here so every handler below can be read
// against it (jobdraft.go's header style):
//
//	GET  /api/orch/inbox        405 non-GET · 500 no store / read failed
//	POST /api/orch/ack          405 · 400 bad json / unknown field / empty
//	                            ids / empty disposition · 409 unknown or
//	                            already-acked id (nothing written) · 413 ·
//	                            500 no store
//	POST /api/orch/context      405 · 400 bad json / unknown field / blank
//	                            session / absent or out-of-range percentage ·
//	                            413 · 500 no store or write failed
//	POST /api/graph/recall-rearm 405 · 400 bad json / unknown field / blank
//	                            session · 403 session mismatch or no kronos
//	                            recorded · 413 · 500 no store
//	GET  /api/kronos            405 non-GET · 500 no store
//
// The DTOs below are this package's own, per the house rule that the api
// layer owns its wire shapes and store types never serialize directly. As on
// /api/slack/inbox that is load-bearing rather than ceremony: a
// store.OrchItem carries delivered_at, acked_at and disposition — kraken's
// own bookkeeping, which kronos neither needs nor should be able to read back
// as if it were its own record of what it decided.

// orchDispositionMax caps the ack's disposition line, the same 200 runes
// /api/slack/ack and /api/job/load's note use. It is a one-liner recorded
// beside the row for later reading, so an over-long one is TRUNCATED rather
// than refused — the ack itself is the payload. An EMPTY disposition is a
// different thing and IS refused: "I handled this" with no statement of how
// is the one answer the inbox cannot use.
const orchDispositionMax = 200

// orchAckResp is POST /api/orch/ack's success body. It carries the
// disposition AS STORED beside the bare ok, plus whether storing it cut
// anything, because the truncation above is silent by design and the caller
// is the one party that can act on it: kronos composes its ack from the tool
// result it reads back, and measured over one day it composed 18,887 runes
// into 25 acks of which the column kept 26.5% — 21 of 25 over the cap, the
// longest 6.9x — while the CLI's success line echoed kronos' own input back,
// so nothing in its loop could see that the tail of every record it wrote
// was gone. The stored text is what the echo now prints, and `truncated` is
// what turns a quiet overrun into a line the agent reads.
type orchAckResp struct {
	OK          bool   `json:"ok"`
	Disposition string `json:"disposition"`
	Truncated   bool   `json:"truncated"`
}

// orchInboxMax caps how many rows GET /api/orch/inbox SERVES in one call —
// the oldest 100, since the read is oldest-first and the oldest item is the
// one that has waited longest.
//
// The cap exists because this route's only client is kronos, and its answer
// lands whole in kronos' context as one Bash tool result. A summary caps at
// 2000 runes, so a backlog that built up while kronos was down, wedged, or
// off is measured in MEGABYTES — blowing the context of the one agent whose
// context this same feature exists to meter. A truncated read is recoverable
// (the rows are durable and the next call returns the next hundred as acks
// retire these); a blown context is not.
//
// The count is what keeps the truncation from being silent: `total` below is
// the FULL unacked count, so a kronos reading 100 rows can see it is 100 of
// 900 and say so, rather than believing it has seen the inbox.
const orchInboxMax = 100

// No kv key is spelled out anywhere in this file, deliberately. Both rows
// GET /api/kronos serves — the minted session (`kronos_agent`) and the last
// context report (`kronos_ctx`) — are read through jobs' own exported
// readers, jobs.ReadKronosAgent and jobs.ReadKronosContext. That is the
// slackLastPoll rule: read the key from the package that WRITES it, since a
// reader and a writer naming two different keys would both report success
// while disagreeing forever, and a status route that quietly reports zero
// looks exactly like a feature nobody is using.

// webOrchItem is one unacked inbox row on the wire. No omitempty anywhere:
// kronos reads every key unconditionally, and an absent one would be
// indistinguishable from an empty/zero value it is entitled to see (a row
// may belong to no job, so an empty slug is a real answer).
//
// delivered_at, acked_at and disposition are deliberately NOT here, exactly
// as on webSlackMsg: they are kraken's delivery bookkeeping, this route
// serves only UNACKED rows so acked_at is 0 on every one of them by
// construction, and delivered_at says nothing kronos can act on.
type webOrchItem struct {
	ID      int64  `json:"id"`
	Kind    string `json:"kind"`
	Slug    string `json:"slug"`
	Summary string `json:"summary"`
	SeenAt  int64  `json:"seen_at"`
}

// orchAckReq is POST /api/orch/ack's body: the rows kronos has finished with
// and one line saying what it did with them. Disposition is a plain string
// rather than an enum for slackAckReq's reason — it carries what happened
// ("steered:foo", "answered:bar", "ignored: merge chatter"), and an enum
// could not.
type orchAckReq struct {
	IDs         []int64 `json:"ids"`
	Disposition string  `json:"disposition"`
}

// orchContextReq is POST /api/orch/context's body — the statusline hook's
// report of how full kronos' own context window is.
//
// UsedPercentage is a POINTER for jobload.go's reason, read one level up: an
// absent key and an explicit 0 are the same value on a plain float64, and 0
// is IN RANGE, so a body that merely failed to carry the number would record
// "kronos is at 0%" — a fresh, roomy window — at exactly the moment the
// report went missing, and the ctx pass' upward-crossing thresholds would
// re-arm off that. An absent number is therefore the same 400 an
// out-of-range one gets. ContextWindowSize needs no such treatment: it is a
// capacity, nothing keys a decision off it, and 0 honestly means "the
// statusline did not say".
type orchContextReq struct {
	SessionID      string   `json:"session_id"`
	UsedPercentage *float64 `json:"used_percentage"`
	ContextWindow  int64    `json:"context_window_size"`
}

// recallRearmReq is POST /api/graph/recall-rearm's body: the session whose
// kg_recall marks are to be dropped, so a compacted kronos can be re-told the
// facts its compacted-away context used to hold.
type recallRearmReq struct {
	SessionID string `json:"session_id"`
}

// webKronosContext is the last context report on the wire. No omitempty: a
// caller that has the object reads all three keys, and a genuine 0 pct is a
// real answer (a freshly compacted window). Absence is expressed by the
// object itself being gone — see webKronos.Context.
type webKronosContext struct {
	Pct    float64 `json:"pct"`
	Window int64   `json:"window"`
	At     int64   `json:"at"`
}

// webKronos is GET /api/kronos' whole body. Every key except `context` is
// required — a zero is a real answer (`live` false with an empty pane is
// exactly what a machine with the feature off should report) and an absent
// key would be indistinguishable from it.
//
// Context is a POINTER with omitempty: the absence of the KEY is the wire's
// only spelling of "kronos has never reported its context", and a zeroed
// object would instead claim a report OF zero — a window with nothing in it,
// the opposite reading. Same rule webJob.load and webJob.tokens follow.
type webKronos struct {
	Enabled   bool              `json:"enabled"`
	Live      bool              `json:"live"`
	SessionID string            `json:"session_id"`
	Pane      string            `json:"pane"`
	SpawnedAt int64             `json:"spawned_at"`
	Context   *webKronosContext `json:"context,omitempty"`
}

// webOrchItemFrom shapes a stored row for the wire, dropping the three
// bookkeeping columns webOrchItem's comment explains.
func webOrchItemFrom(it store.OrchItem) webOrchItem {
	return webOrchItem{
		ID: it.ID, Kind: it.Kind, Slug: it.Slug,
		Summary: it.Summary, SeenAt: it.SeenAt,
	}
}

// orchInboxHandler serves GET /api/orch/inbox — the OLDEST orchInboxMax
// unacked rows in id order, which is arrival order, plus `total`, the full
// unacked count. This is kronos' working set: an item stays here until kronos
// acks it, so a crash, a compaction, or a respawn loses nothing (the row is
// the durable state, kronos' context is not).
//
// UnackedOrch rather than UndeliveredOrch, deliberately: an item whose bump
// send failed is undelivered but still very much kronos' to handle, and a
// read keyed on delivery would make it unreachable from the one surface that
// exists to reach it.
//
// The cap is applied HERE rather than pushed into the store as a LIMIT, and
// that is a deliberate call rather than a shortcut: `total` needs the full
// unacked count anyway, so a store-side LIMIT would buy one truncated scan
// plus a COUNT where this is one scan — and at the row counts this table
// holds today the scan is the cost, not the slice. If orch_inbox ever grows
// to where that stops being true, the honest shape is a store call returning
// (page, count) together; a LIMIT alone would silently drop `total`.
//
// The list is always an ARRAY, never null — `make(..., 0, n)` rather than a
// nil slice — so a client can iterate it unconditionally.
func orchInboxHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
		return
	}
	st, ok := graphStore(w)
	if !ok {
		return
	}
	items, err := st.UnackedOrch()
	if err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	total := len(items)
	// The oldest end, because UnackedOrch is oldest-first and the oldest item
	// is the one that has waited longest — the opposite slice would strand a
	// backlog's head forever, since nothing else ever surfaces those rows.
	page := items
	if len(page) > orchInboxMax {
		page = page[:orchInboxMax]
	}
	out := make([]webOrchItem, 0, len(page))
	for _, it := range page {
		out = append(out, webOrchItemFrom(it))
	}
	webJSON(w, http.StatusOK, map[string]any{"items": out, "total": total})
}

// orchAckHandler serves POST /api/orch/ack — kronos saying "I am done with
// these rows, and here is what I did". It is the only thing that removes an
// item from the inbox read above, explicit rather than implied by the read
// for the same reason the Slack ack is: an item kronos looked at and never
// decided about must come back on the next inbox call.
//
// The store call is all-or-nothing by contract (an unknown or already-acked
// id fails the whole batch, applying nothing), so a refusal is reported with
// the store's OWN sentence — it names the offending id, which no message
// written here could.
func orchAckHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	var req orchAckReq
	// decodeGraphBody is this package's shared strict decoder (MaxBytesReader
	// + DisallowUnknownFields + the 413/400 split). Strictness matters more
	// here than on most routes: this route's whole job is to RETIRE rows, and
	// a body whose ids landed under a misspelled key would decode to an empty
	// list. The empty-ids check below catches that either way, but the
	// refusal naming the key is the one that says what to fix.
	if !decodeGraphBody(w, r, &req, "this route takes only ids and disposition") {
		return
	}
	if len(req.IDs) == 0 {
		webJSON(w, http.StatusBadRequest, map[string]string{
			"error": "missing ids — send the inbox ids being acked, e.g. {\"ids\":[42,43],\"disposition\":\"steered:csv-export-timeout\"}",
		})
		return
	}
	// The disposition is the only record of WHY an item left the inbox, so an
	// empty one is refused rather than defaulted: "acked, reason unknown" is
	// indistinguishable from a bug that acks everything.
	if strings.TrimSpace(req.Disposition) == "" {
		webJSON(w, http.StatusBadRequest, map[string]string{
			"error": "missing disposition — say what happened to these items (steered:<slug>, answered:<slug>, or ignored:<reason>)",
		})
		return
	}
	st, ok := graphStore(w)
	if !ok {
		return
	}
	// core.TruncateOneLine both one-lines (control characters, newlines
	// included, become spaces; runs of whitespace collapse) and caps at 200
	// RUNES, which is what keeps a multi-byte disposition from blowing the cap
	// where a byte count would not.
	disposition := core.TruncateOneLine(req.Disposition, orchDispositionMax)
	// Truncation is judged on the one-lined form at an uncapped width, so a
	// disposition whose only excess was whitespace runs or newlines — which
	// one-lining collapses without losing a word — is not reported as cut.
	truncated := utf8.RuneCountInString(core.TruncateOneLine(req.Disposition, 1<<30)) > orchDispositionMax
	if err := st.AckOrchItems(req.IDs, disposition, time.Now().UnixMilli()); err != nil {
		// 409 rather than 400/500 for slackAckHandler's reason: every refusal
		// this call makes is about the CURRENT STATE of the named rows (an id
		// that does not exist, or one already acked), which is a conflict with
		// what the caller believed, not a malformed request. There is no
		// sentinel separating a genuine I/O failure from that, so the whole
		// error surface maps here and the store's own sentence says which it
		// was.
		webJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	webJSON(w, http.StatusOK, orchAckResp{OK: true, Disposition: disposition, Truncated: truncated})
}

// orchContextHandler serves POST /api/orch/context — the statusline hook
// (`kraken ctx-hook`) reporting how full kronos' context window is, once per
// statusline render.
//
// It is a WRITE despite reading like telemetry: it persists the report, and
// the daemon's ctx pass fires carl a warning off the upward crossings of it.
// That is why it is mirror-gated with the other POSTs rather than left open
// with the reads.
func orchContextHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	var req orchContextReq
	if !decodeGraphBody(w, r, &req,
		"this route takes only session_id, used_percentage and context_window_size") {
		return
	}
	if strings.TrimSpace(req.SessionID) == "" {
		webJSON(w, http.StatusBadRequest, map[string]string{
			"error": "missing session_id — the context report is keyed to a session, and an unkeyed one cannot be told from another session's",
		})
		return
	}
	// Absent is refused with the same sentence out-of-range gets, because it
	// is the same mistake read one level up: a body that never mentioned the
	// percentage says strictly less than one that named an impossible value,
	// and recording it as 0 would report a full window as an empty one.
	if req.UsedPercentage == nil {
		webJSON(w, http.StatusBadRequest, map[string]string{
			"error": "missing used_percentage — send the statusline's own number in 0..100; a report with no percentage would record as 0%, which reads as an empty window rather than as no report",
		})
		return
	}
	pct := *req.UsedPercentage
	if pct < 0 || pct > 100 {
		webJSON(w, http.StatusBadRequest, map[string]string{
			"error": "used_percentage must be within 0..100",
		})
		return
	}
	st, ok := graphStore(w)
	if !ok {
		return
	}
	if err := jobs.RecordKronosContext(st, strings.TrimSpace(req.SessionID), pct, req.ContextWindow); err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	webJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// graphRecallRearmHandler serves POST /api/graph/recall-rearm — the
// PostCompact hook (`kraken rearm-hook`) dropping kronos' kg_recall marks so
// the facts its compacted-away context used to hold can be delivered again.
//
// A mismatch is a 403 rather than a 404 or a 400, and that is the whole
// safety property of the route: kg_recall is per-session state for EVERY
// session in the binary — every cell's zeus, every teammate — and a session
// id this route accepted freely would let any caller wipe another
// conversation's delivery marks and burn its once-per-fact budget over again.
// jobs.RearmKronosRecall answers only for the session kv records as kronos',
// so "no kronos recorded" and "not that session" are one refusal.
func graphRecallRearmHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	var req recallRearmReq
	if !decodeGraphBody(w, r, &req, "this route takes only session_id") {
		return
	}
	if strings.TrimSpace(req.SessionID) == "" {
		webJSON(w, http.StatusBadRequest, map[string]string{
			"error": "missing session_id — the re-arm is per session, and a blank one names none",
		})
		return
	}
	st, ok := graphStore(w)
	if !ok {
		return
	}
	cleared, err := jobs.RearmKronosRecall(st, strings.TrimSpace(req.SessionID))
	if err != nil {
		webJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	webJSON(w, http.StatusOK, map[string]any{"cleared": cleared})
}

// kronosHandler serves GET /api/kronos — whether the feature is on, whether
// there is a kronos right now and in which pane, which session it is, and
// what it last said about its own context. It is the board's KRONOS view's
// whole status read, and the one place to look when "kronos said nothing all
// day" needs to be told apart from "nothing arrived" — the same job
// /api/slack/status does for iris.
//
// It takes a peerSource for peersHandler's reason: discovery shells out to
// tmux and ps, and the seam is what lets a test drive this handler without
// either.
func kronosHandler(source peerSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
			return
		}
		st, ok := graphStore(w)
		if !ok {
			return
		}
		// jobs.ReadKronosAgent rather than a decode of the kv row here: it is
		// exported precisely so this route and the spawn path cannot disagree
		// about what the recorded session is. Its `false` — never spawned, or
		// a row too damaged to read — leaves both fields zero, which on the
		// wire is an empty session id and a 0 spawned_at, the honest answer.
		agent, _ := jobs.ReadKronosAgent(st)
		out := webKronos{
			Enabled:   jobs.KronosEnabled(),
			SessionID: agent.SessionID,
			SpawnedAt: agent.SpawnedAt,
			Context:   webKronosContextFrom(st),
		}
		// Liveness is the PANE, not the kv row: a recorded session id says
		// only that kronos was spawned once, and a resumable id outlives the
		// process every time. jobs.FindKronos answering nil is exactly "there
		// is no kronos right now", which is what the view needs in order to
		// render an explanatory empty state instead of a composer that cannot
		// send. It is the delivery pass' OWN finder rather than a second copy
		// of its rule here, so the pane this route names is by construction
		// the pane a bump would reach.
		if p := jobs.FindKronos(source()); p != nil {
			out.Live, out.Pane = true, p.Pane
		}
		webJSON(w, http.StatusOK, out)
	}
}

// webKronosContextFrom shapes the last recorded report for the wire, or nil
// when there has never been one.
//
// The presence test is jobs.ReadKronosContext's OWN second return, not a
// field inspection here: it is the reader that knows what an absent, empty or
// damaged row looks like, and a second opinion about that in this package is
// exactly how a status route quietly reports zero forever. Nil is what makes
// `context` an absent key rather than a zeroed object — the distinction the
// pointer exists for, since 0% is a real report (a freshly compacted window)
// and "never measured" is not.
//
// SessionID rides in the stored row but NOT on the wire: the top level
// already names the session, and two ids in one payload invite a client to
// render a mismatch this route has no opinion about — a report from a
// since-replaced session is still the last thing kronos said about its
// context.
func webKronosContextFrom(st *store.Store) *webKronosContext {
	c, ok := jobs.ReadKronosContext(st)
	if !ok {
		return nil
	}
	return &webKronosContext{Pct: c.Pct, Window: c.Window, At: c.At}
}
