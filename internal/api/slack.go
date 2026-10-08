package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"kraken/internal/core"
	"kraken/internal/jobs"
	"kraken/internal/store"
)

// slack.go — the Slack intake bot's HTTP surface: iris' inbox read, its ack
// write, and the configured/backlog status read. Iris is a long-running
// claude outside any cell; like zeus it never opens kraken.db (single-writer
// rule) and reaches these routes through the `kraken slack …` verbs
// (cmd/kraken/cmd_slack.go), the same localhost proxy shape the
// questions/feedback/load family already uses.
//
// Every route here is a NEW PATH rather than a key added to, retyped in, or
// removed from an existing shape, so none of them bumps `apiContract` — the
// bump policy stated in ios/API.md's header and pinned by
// TestAPIContractUnchanged.
//
// Reads vs writes under --mirror: the two GETs stay open (a read is a read,
// the same call /api/watchlist's GET makes), and the one POST
// is mirror-gated at registration (register.go) with the standard plain-text
// 403 — the same "a mirror board is read-only, full stop" policy every other
// agent-side write follows. Gating at registration rather than inline is
// available here because ack is a POST-ONLY path, so there is no GET on it
// for a route wrapper to take down.
//
// The DTOs below are this package's own, per the house rule that the api
// layer owns its wire shapes and store types never serialize directly. That
// is load-bearing here rather than ceremony: a store.SlackInboxMsg carries
// delivered_at, acked_at and disposition — kraken's own bookkeeping, which
// iris neither needs nor should be able to read back as if it were its own
// record of what it decided.

// slackDispositionMax caps the ack's disposition line. It is a one-liner
// recorded beside the row for later reading, so an over-long one is
// TRUNCATED rather than refused — same call jobLoadNoteMax makes: the ack
// itself is the payload, and losing it over a verbose sentence would be the
// worse answer. An EMPTY disposition is a different thing and IS refused
// (see slackAckHandler): "I handled this" with no statement of how is the
// one answer the inbox cannot use.
const slackDispositionMax = 200

// webSlackMsg is one unacked inbox row on the wire. No omitempty anywhere:
// iris reads every key unconditionally, and an absent one would be
// indistinguishable from an empty/zero value it is entitled to see (a DM has
// no channel_name, a top-level message has no thread_ts — both are real
// answers).
//
// delivered_at, acked_at and disposition are deliberately NOT here. They are
// kraken's delivery bookkeeping, not the message; this route serves only
// UNACKED rows, so acked_at is 0 on every one of them by construction and
// disposition is empty, and delivered_at says nothing iris can act on.
type webSlackMsg struct {
	ID          int64  `json:"id"`
	ChannelID   string `json:"channel_id"`
	ChannelName string `json:"channel_name"`
	TS          string `json:"ts"`
	ThreadTS    string `json:"thread_ts"`
	AuthorID    string `json:"author_id"`
	AuthorName  string `json:"author_name"`
	Source      string `json:"source"`
	Text        string `json:"text"`
	SeenAt      int64  `json:"seen_at"`
}

// webSlackStatus is GET /api/slack/status' whole body. Every key except
// config_error is required — a zero count is a real answer (an empty inbox),
// and `configured` false with three zeroes is exactly what an unconfigured
// machine should report rather than an error.
//
// ConfigError is omitempty because it distinguishes the two ways `configured`
// can be false, which otherwise look identical here and are opposites in
// practice: nothing set up (the normal fresh-install state, no key at all) vs.
// set up wrong in a way no amount of waiting fixes. Only a fixed sentinel's
// text is ever put in it — never a raw error string, since a future error
// could carry the credential it failed on.
type webSlackStatus struct {
	Configured  bool   `json:"configured"`
	ConfigError string `json:"config_error,omitempty"`
	LastPollAt  int64  `json:"last_poll_at"`
	Total       int    `json:"total"`
	Undelivered int    `json:"undelivered"`
	Unacked     int    `json:"unacked"`
}

// slackAckReq is POST /api/slack/ack's body: the rows iris has finished with
// and one line saying what it did with them. Disposition is a plain string
// rather than an enum on purpose — it carries the drafted job's slug
// ("drafted:csv-export-timeout", "edited:foo", "ignored: standup noise"), and
// an enum could not.
type slackAckReq struct {
	IDs         []int64 `json:"ids"`
	Disposition string  `json:"disposition"`
}

// webSlackMsgFrom shapes a stored row for the wire, dropping the three
// bookkeeping columns webSlackMsg's comment explains.
func webSlackMsgFrom(m store.SlackInboxMsg) webSlackMsg {
	return webSlackMsg{
		ID: m.ID, ChannelID: m.ChannelID, ChannelName: m.ChannelName,
		TS: m.TS, ThreadTS: m.ThreadTS,
		AuthorID: m.AuthorID, AuthorName: m.AuthorName,
		Source: m.Source, Text: m.Text, SeenAt: m.SeenAt,
	}
}

// slackInboxHandler serves GET /api/slack/inbox — every unacked row in id
// order, which is arrival order. This is iris' whole working set: a message
// stays here until iris acks it, so a crash, a compaction, or a respawn
// loses nothing (the inbox row is the durable state, iris' context is not).
//
// The list is always an ARRAY, never null — `make(..., 0, n)` rather than a
// nil slice — so a client can iterate it unconditionally.
func slackInboxHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
		return
	}
	st, ok := activeStore(w)
	if !ok {
		return
	}
	msgs, err := st.UnackedSlackInbox()
	if err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	out := make([]webSlackMsg, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, webSlackMsgFrom(m))
	}
	webJSON(w, http.StatusOK, map[string]any{"messages": out})
}

// slackAckHandler serves POST /api/slack/ack — iris saying "I am done with
// these rows, and here is what I did". It is the only thing that removes a
// message from the inbox read above, which is why it is explicit rather than
// implied by the read: a message iris looked at and never decided about must
// come back on the next inbox call.
//
// The store call is all-or-nothing by contract (an unknown or already-acked
// id fails the whole batch, applying nothing), so a refusal is reported with
// the store's OWN sentence — it names the offending id, which no message
// written here could.
func slackAckHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	var req slackAckReq
	// decodeStrictBody is this package's shared strict decoder (MaxBytesReader
	// + DisallowUnknownFields + the 413/400 split), and reusing it keeps one
	// refusal wording across every route that rejects unknown fields. That
	// strictness matters more here than elsewhere: this route's whole job is to RETIRE
	// rows, and a body whose ids landed under a misspelled key would decode
	// to an empty list. The empty-ids check below catches that either way,
	// but the refusal naming the key is the one that says what to fix.
	if !decodeStrictBody(w, r, &req, "this route takes only ids and disposition") {
		return
	}
	if len(req.IDs) == 0 {
		webJSON(w, http.StatusBadRequest, map[string]string{
			"error": "missing ids — send the inbox ids being acked, e.g. {\"ids\":[42,43],\"disposition\":\"drafted:csv-export-timeout\"}",
		})
		return
	}
	// The disposition is the only record of WHY a message left the inbox, so
	// an empty one is refused rather than defaulted: "acked, reason unknown"
	// is indistinguishable from a bug that acks everything.
	if strings.TrimSpace(req.Disposition) == "" {
		webJSON(w, http.StatusBadRequest, map[string]string{
			"error": "missing disposition — say what happened to these messages (drafted:<slug>, edited:<slug>, or ignored:<reason>)",
		})
		return
	}
	st, ok := activeStore(w)
	if !ok {
		return
	}
	// core.TruncateOneLine both one-lines (control characters, newlines
	// included, become spaces; runs of whitespace collapse) and caps at 200
	// RUNES, which is what keeps a multi-byte disposition from blowing the
	// cap where a byte count would not.
	disposition := core.TruncateOneLine(req.Disposition, slackDispositionMax)
	if err := st.AckSlackMessages(req.IDs, disposition, time.Now().UnixMilli()); err != nil {
		// 409 rather than 400/500: every refusal this call makes is about the
		// CURRENT STATE of the named rows (an id that does not exist, or one
		// already acked), which is a conflict with what the caller believed,
		// not a malformed request. There is no sentinel to separate a genuine
		// I/O failure from that, so the whole error surface maps here — the
		// store's own sentence rides along and says which it was.
		webJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	webJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// slackStatusHandler serves GET /api/slack/status — whether the feature is
// configured at all, when the poll last ran, and how deep the backlog is.
// It is the one place to look when "iris said nothing all day" needs to be
// told apart from "nothing arrived", and it answers both halves.
//
// `configured` is LoadSlackConfig succeeding. A failure is otherwise dropped
// rather than echoed — the token value must never reach a response body — with
// ONE exception, and it is an exception about a fixed string rather than about
// an error: core.ErrSlackHalfConfigured is a constant sentence naming a missing
// key, carries no credential, and is the one config failure a human can act on
// (core.ErrNoSlack means the feature is simply off, and says nothing).
func slackStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
		return
	}
	st, ok := activeStore(w)
	if !ok {
		return
	}
	_, cfgErr := core.LoadSlackConfig()
	total, undelivered, unacked, err := st.SlackInboxCounts()
	if err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	configErr := ""
	if errors.Is(cfgErr, core.ErrSlackHalfConfigured) {
		configErr = core.ErrSlackHalfConfigured.Error()
	}
	webJSON(w, http.StatusOK, webSlackStatus{
		Configured:  cfgErr == nil,
		ConfigError: configErr,
		LastPollAt:  slackLastPoll(st),
		Total:       total,
		Undelivered: undelivered,
		Unacked:     unacked,
	})
}

// slackLastPoll reads the poll watermark the daemon's pass stamps at the
// START of every attempt (stamp-before-bail). The key comes from
// jobs.SlackLastPollKV — read from the package that WRITES it rather than
// re-spelled here, so a reader and a writer cannot drift into naming two
// different keys while both report success; the layering allows it, since
// api sits above jobs.
//
// The value is a decimal unix-millis string, and it degrades every failure to 0. An
// unset key is the normal state before the first tick, and a kv read that
// fails says nothing about the counts beside it — neither is worth failing a
// status read over, and 0 already means "no poll recorded" on the wire.
func slackLastPoll(st *store.Store) int64 {
	raw, err := st.GetKV(jobs.SlackLastPollKV)
	if err != nil {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0
	}
	return n
}
