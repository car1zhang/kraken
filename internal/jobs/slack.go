package jobs

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"kraken/internal/core"
	"kraken/internal/store"
)

// slack.go — the Slack intake POLLER and its delivery half. Two passes hung
// off the daemon tick: slackPass reads Slack and writes rows, deliverSlack
// tells iris there are rows to read.
//
// The whole design is at-least-once, in four steps that each fail toward
// re-delivery rather than toward loss: the history sweep and the mention
// search deliberately read back FURTHER than their watermarks (slackFetchFloor,
// and Loop B's day-grained `after:` window; the thread chase alone reads from
// exactly its mark, since a reply stream has no index lag to absorb), so a
// message can be seen twice but never stepped over;
// `UNIQUE(channel_id, ts)` makes those re-reads a no-op;
// watermarks advance only in the SAME transaction that commits the rows they
// cover — and a mark only ever COVERS rows in that same batch, so a skipped
// conversation contributes neither and a sweep that dies mid-pass re-reads
// rather than skips; and the
// row itself is not finished until iris acks it, which is a separate stamp from
// the delivery bump. Nothing here is allowed to advance a watermark past a
// message it did not durably record — that is the one failure this file has no
// recovery for, since Slack is read-only to kraken and there is no second copy.
//
// The pass is also SILENT when Slack is unconfigured. core.ErrNoSlack is a
// "feature off" posture, not ship's refusal posture: a machine with
// no ~/.kraken/slack.env is a machine that has not opted in, and it must cost
// nothing and log nothing, forever.

// slackHistoryBudget caps how many conversation-reading calls one pass makes —
// conversations.history AND conversations.replies together, since they cost the
// same against the same tier.
//
// The number is arithmetic rather than taste. Slack's Tier 3 allows ~50 calls
// per minute; the daemon ticks every 15s, so a pass runs four times a minute
// and 12 x 4 = 48 fits under the tier with room for the mention search and the
// odd users.info. An earlier 30 was 120/min — over the tier by 2.4x, which
// would have spent the feature's life inside its own 429 hold-off.
//
// The budget is spent least-recently-swept-FIRST (slackSweepOrder), which is
// what keeps it from starving a conversation: a fixed order would poll the
// first 12 forever and never reach the 13th.
const slackHistoryBudget = 12

// slackInventoryTTL is how long the conversation list is reused before
// users.conversations is called again. The list changes when carl opens a new
// DM — minutes of latency on a brand-new conversation, against one paginated
// call per tick saved. A new DM's first message is still caught, just on the
// first pass after the inventory refreshes.
const slackInventoryTTL = 30 * time.Minute

// slackHistoryPageLimit is the per-request page size for history sweeps.
// Slack's default is 100 and its max 1000. core.ConversationHistory walks
// cursors internally, so this is the size of each page of that walk rather than
// a cap on what comes back — which is exactly why slackFetchFloor below never
// lets a sweep start from the beginning of a channel.
const slackHistoryPageLimit = 200

// slackLookback is the minimum window every history sweep re-reads, even when
// the conversation's watermark is newer than it.
//
// It is what stands in for "fetch the latest page". The frozen
// core.ConversationHistory paginates to exhaustion, so a call with no `oldest`
// does not return one page — it returns the whole channel, up to 100 pages of
// 200, which on any DM with history is dozens of API calls per conversation per
// pass and blows the tier open on the first tick. A floor bounds that to one
// day's traffic, which in a real DM is a single page.
//
// Re-reading a day costs nothing that matters: rows below the watermark are
// dropped by filterSlackNew, and any that slip through hit
// UNIQUE(channel_id, ts). What it BUYS is thread detection — a parent carries
// `latest_reply`, so seeing a recently-posted parent again is how a reply to it
// is noticed at all (slackThreadChases' window arm).
const slackLookback = 24 * time.Hour

// slackThreadRetireAfter bounds the persisted-thread arm of the catch-up
// sweep. Every thread kraken has ever read leaves a `thread:` watermark behind
// forever, so without a cutoff the per-pass work would grow monotonically with
// the workspace's whole history — and a thread nobody has touched in a week is
// not the one carl is waiting on. A retired thread is not forgotten (the mark
// stays, and its rows stay); it is only no longer polled.
const slackThreadRetireAfter = 7 * 24 * time.Hour

// The three kv singletons this feature keeps. slack_self caches the identity
// auth.test reports so the pass does not re-authenticate every tick;
// slack_last_poll is what `kraken slack status` renders as "last polled", and
// is stamped at the START of a poll (tokensPass' stamp-before-bail rule: a
// pass that fails on its first call has still had its turn, and a stamp
// written only on success turns a persistent failure into a per-tick retry).
const (
	slackSelfKVKey = "slack_self"
	// SlackLastPollKV is exported because internal/api serves it as
	// `last_poll_at` on /api/slack/status: the key's spelling is shared between
	// the only writer (this pass) and the only reader (that handler), and two
	// hand-typed copies of one string is how a status route quietly reports 0
	// forever.
	SlackLastPollKV = "slack_last_poll"
)

// slackSelf is what slack_self holds.
type slackSelf struct {
	UserID string `json:"user_id"`
	TeamID string `json:"team_id"`
}

// slackPassRunning single-flights the pass across ticks, the same shape
// usagePassRunning/dreamRunning use and for the same reason: the pass makes up
// to slackHistoryBudget+ network calls, which can easily outlast a 15s tick,
// and two overlapping sweeps would both read the same watermarks and both
// write the rows between them.
var slackPassRunning atomic.Bool

// slackMem is the pass' cross-tick memory. Every field in it is an OPTIMISATION
// or a courtesy, never correctness: losing the whole struct on a restart costs
// one extra users.conversations call, one round of unordered sweeping, one
// duplicate error line, and a rate-limit pause that ends early. The durable
// state — what has been seen and how far each conversation has been read — is
// in kraken.db, where a restart cannot touch it.
var slackMem struct {
	sync.Mutex
	inventory   []core.SlackConversation
	inventoryAt time.Time
	// lastSwept drives slackSweepOrder's rotation: conversation id → unix
	// millis of when its history was last read by this process.
	lastSwept map[string]int64
	// notBefore is the deadline a 429's Retry-After set. Honouring it is the
	// difference between backing off and hammering a rate limiter with the
	// same request every 15 seconds until it escalates.
	notBefore time.Time
	// users caches user id → display name so a chatty conversation costs one
	// users.info call per author rather than one per message.
	users map[string]string
	// logged is the dedup set behind slackLogOnce, the same job the daemon's
	// d.Notified map does for its own passes — this one cannot reach that map
	// because the pass runs in its own goroutine.
	logged map[string]bool
}

// slackLogOnce writes one error line per distinct key per outage. A Slack
// outage, an expired token, or a store that cannot answer will produce the
// same failure four times a minute for as long as it lasts, and the log is
// read by a human.
func slackLogOnce(key, format string, a ...any) {
	slackMem.Lock()
	if slackMem.logged == nil {
		slackMem.logged = map[string]bool{}
	}
	already := slackMem.logged[key]
	slackMem.logged[key] = true
	slackMem.Unlock()
	if !already {
		dlogErr(format, a...)
	}
}

// slackLogClear re-arms a key once the thing it complained about works again,
// so a second outage is reported rather than swallowed.
func slackLogClear(key string) {
	slackMem.Lock()
	defer slackMem.Unlock()
	delete(slackMem.logged, key)
}

// slackHoldOff records a not-before deadline; slackReady is the gate that
// honours it.
func slackHoldOff(until time.Time) {
	slackMem.Lock()
	defer slackMem.Unlock()
	if until.After(slackMem.notBefore) {
		slackMem.notBefore = until
	}
}

func slackReady(now time.Time) bool {
	slackMem.Lock()
	defer slackMem.Unlock()
	return !now.Before(slackMem.notBefore)
}

// slackNoteSwept stamps a conversation as swept, feeding the rotation.
func slackNoteSwept(id string, now time.Time) {
	slackMem.Lock()
	defer slackMem.Unlock()
	if slackMem.lastSwept == nil {
		slackMem.lastSwept = map[string]int64{}
	}
	slackMem.lastSwept[id] = now.UnixMilli()
}

// slackSweptSnapshot copies the rotation clock out from under the mutex, so
// slackSweepOrder stays a pure function over a plain map rather than reaching
// into package state itself.
func slackSweptSnapshot() map[string]int64 {
	slackMem.Lock()
	defer slackMem.Unlock()
	out := make(map[string]int64, len(slackMem.lastSwept))
	for k, v := range slackMem.lastSwept {
		out[k] = v
	}
	return out
}

// --- pure helpers ---

// filterSlackNew keeps only the messages a pass should record: strictly newer
// than the watermark, an ordinary human message, not written by carl himself,
// and carrying a timestamp at all. PURE.
//
// The floor is applied CLIENT-side even though Slack's `oldest` already filters
// server-side, and that redundancy is the point rather than an oversight: the
// sweep deliberately fetches back further than the watermark (slackLookback, so
// thread parents stay visible), so the server-side floor is the WIDER of the
// two and this is what enforces the real one. An empty mark keeps everything —
// the caller passes an effective floor rather than an empty one (slackRowFloor).
//
// SUBTYPE is the other filter and it is not cosmetic. Slack writes edits,
// deletions, joins and bot posts into the same `messages` array as ordinary
// text, with payloads that are empty or nested one level down — a
// `message_changed` record's real text lives under `message`, and decodes here
// as a row with no author and no body. Only three subtypes may become rows:
// `""` (an ordinary message), `thread_broadcast` (a threaded reply the author
// also sent to the channel), and `file_share` (an uploaded file plus its
// comment — a real DM with a real author and text, and a common way to hand
// over a spec or a screenshot).
//
// Dropping carl's own messages is what stops the bot reading his side of every
// DM as an incoming request; a message with no ts is unaddressable (it is the
// row's identity, half of UNIQUE(channel_id, ts)) and is dropped rather than
// stored with an empty key.
func filterSlackNew(msgs []core.SlackMessage, mark, selfID string) []core.SlackMessage {
	var out []core.SlackMessage
	for _, m := range msgs {
		if m.TS == "" {
			continue
		}
		if m.SubType != "" && m.SubType != "thread_broadcast" && m.SubType != "file_share" {
			continue
		}
		if selfID != "" && m.User == selfID {
			continue
		}
		if mark != "" && !core.SlackTSLess(mark, m.TS) {
			continue // ts <= mark: already seen
		}
		out = append(out, m)
	}
	return out
}

// slackDayFloorTS renders the Slack ts of slackLookback ago — the floor a
// conversation with no watermark starts from. PURE.
//
// A brand-new install must import YESTERDAY, never the workspace's entire
// history: an unbounded first sweep would spend hundreds of API calls and then
// hand iris a year of messages to triage, every one of them long since dealt
// with. The `.000000` micros half is what makes it a well-formed ts for
// core.SlackTSLess, which compares the two halves as integers.
func slackDayFloorTS(now time.Time) string {
	return strconv.FormatInt(now.Add(-slackLookback).Unix(), 10) + ".000000"
}

// slackRowFloor is the floor that decides what becomes a ROW: the conversation's
// own watermark, or one lookback window when it has none. PURE.
func slackRowFloor(mark string, now time.Time) string {
	if mark == "" {
		return slackDayFloorTS(now)
	}
	return mark
}

// slackFetchFloor is the floor the history CALL uses — the older of the
// watermark and one lookback window. PURE.
//
// The two floors differ on purpose and the gap between them is the sweep's
// thread-detection window. Fetching from the watermark alone would return only
// unseen messages, and a thread parent is by definition already seen — so its
// `latest_reply`, the only signal Slack gives that a thread moved, would never
// come back. Fetching from at least a lookback ago returns recent parents again
// at the cost of a few already-recorded messages, which slackRowFloor then
// drops.
//
// It never goes NEWER than the watermark: a conversation nobody has swept for a
// week must resume from where it stopped, not from yesterday, or the six days
// in between are lost with nothing recording it.
func slackFetchFloor(mark string, now time.Time) string {
	day := slackDayFloorTS(now)
	if mark != "" && core.SlackTSLess(mark, day) {
		return mark
	}
	return day
}

// mentionAfterDate renders the `after:` operand of the mention search: the UTC
// calendar date 24 hours before the watermark. PURE.
//
// Two coarsenings meet here and both are deliberate. search.messages has no
// finer granularity than a date, so the query can never express an exact floor
// at all. And the extra 24 hours is Slack's search INDEX lag: a message becomes
// searchable some time after it is posted, so a query starting exactly at the
// watermark would silently miss anything indexed late.
//
// That lag is also why the mention loop applies NO client-side floor to what
// comes back (slackSweepMentions): a late-indexed mention arrives BELOW the
// newest ts already seen, so an exact floor would drop precisely the messages
// this widened window was opened to catch. UNIQUE(channel_id, ts) absorbs the
// re-reads instead, which is the cheaper mistake by a wide margin — a duplicate
// insert is a no-op, a dropped mention is gone.
//
// An empty or unparseable mark falls back to 24h before now, which is the
// first-ever-poll case: a brand-new install picks up yesterday's mentions
// rather than the workspace's entire history.
func mentionAfterDate(mark string, now time.Time) string {
	if secs, ok := slackTSSeconds(mark); ok {
		return time.Unix(secs, 0).Add(-24 * time.Hour).UTC().Format("2006-01-02")
	}
	return now.Add(-24 * time.Hour).UTC().Format("2006-01-02")
}

// slackTSSeconds pulls the integer SECONDS out of a Slack ts
// ("1712345678.000100"). PURE. Never a float: a Slack ts is an identity as much
// as a time, and float64 cannot round-trip the microsecond half — which is
// exactly why core.SlackTSLess compares the two halves as integers too.
func slackTSSeconds(ts string) (int64, bool) {
	if ts == "" {
		return 0, false
	}
	secs, _, _ := strings.Cut(ts, ".")
	n, err := strconv.ParseInt(secs, 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// slackBumpMessage is the one line the delivery pass types into iris' pane.
// PURE. Deliberately tiny and deliberately a COUNT rather than the content:
// the message text lives in the inbox, iris reads it with the command named
// here, and typing message bodies into a pane would both flood it and make the
// pane — not kraken.db — the thing that has to be reliable.
func slackBumpMessage(n int) string {
	if n == 1 {
		return "slack: 1 new message — run: kraken slack inbox"
	}
	return fmt.Sprintf("slack: %d new messages — run: kraken slack inbox", n)
}

// slackSweepOrder puts the least-recently-swept conversations first and cuts
// the list to the budget. PURE over (convs, lastSwept, budget) — the input
// slice is copied, never reordered in place, because the caller's copy is the
// cached inventory that every later pass reads.
//
// Rotation rather than truncation is what makes the budget safe: with a fixed
// order, every conversation past the budget's cut would never be read again,
// and nothing would ever say so. Sorting by last-swept means every conversation reaches the front
// of the queue within ceil(n/budget) passes — under a minute in practice — and
// a conversation this process has never swept sorts first, since an absent key
// reads as unix-millis zero, which precedes every real stamp.
//
// Ties keep their input order (sort.SliceStable), so the result is a function
// of the arguments alone. A negative budget is clamped to zero rather than
// read as "unlimited": the contract is "at most budget", and there is no
// sensible at-most for a negative one.
func slackSweepOrder(convs []core.SlackConversation, lastSwept map[string]int64, budget int) []core.SlackConversation {
	if budget < 0 {
		budget = 0
	}
	out := append([]core.SlackConversation(nil), convs...)
	sort.SliceStable(out, func(i, j int) bool {
		return lastSwept[out[i].ID] < lastSwept[out[j].ID]
	})
	if len(out) > budget {
		out = out[:budget]
	}
	return out
}

// slackSourceOf maps a conversation to the inbox row's `source` column, whose
// CHECK constraint allows exactly dm/mpim/mention.
func slackSourceOf(c core.SlackConversation) string {
	if c.IsMPIM {
		return "mpim"
	}
	return "dm"
}

// --- the batch ---

// slackBatch accumulates one pass' worth of rows and advanced watermarks, so
// they can be committed together. ONE AddSlackBatch per pass is not a
// performance choice: a watermark written in a different transaction from the
// rows it covers is a watermark that can outlive them, and every message
// between the two is then lost with nothing recording that it existed.
type slackBatch struct {
	rows  []store.SlackInboxMsg
	marks map[string]string
	// seen dedupes within the batch on the row's real identity. A
	// thread_broadcast reply is returned by BOTH conversations.history and
	// conversations.replies, and a mention in a DM is returned by both loops —
	// the UNIQUE constraint would absorb either, but then AddSlackBatch's
	// inserted count (which is what the log line reports) would be a lie.
	seen map[string]bool
	at   int64 // seen_at for every row in this batch
}

func newSlackBatch(now time.Time) *slackBatch {
	return &slackBatch{marks: map[string]string{}, seen: map[string]bool{}, at: now.UnixMilli()}
}

func (b *slackBatch) add(m core.SlackMessage, source string) {
	key := m.ChannelID + "\x00" + m.TS
	if b.seen[key] {
		return
	}
	b.seen[key] = true
	b.rows = append(b.rows, store.SlackInboxMsg{
		ChannelID:   m.ChannelID,
		ChannelName: m.ChannelName,
		TS:          m.TS,
		ThreadTS:    m.ThreadTS,
		AuthorID:    m.User,
		Source:      source,
		Text:        m.Text,
		SeenAt:      b.at,
	})
}

// advance raises a watermark toward ts, never lowering it. mark is the value
// currently in the store, so a Slack response that arrives out of order — or a
// second loop touching the same key — can only ever move the key forwards.
func (b *slackBatch) advance(key, mark, ts string) {
	if key == "" || ts == "" {
		return
	}
	best := mark
	if cur, ok := b.marks[key]; ok && (best == "" || core.SlackTSLess(best, cur)) {
		best = cur
	}
	if best != "" && !core.SlackTSLess(best, ts) {
		return
	}
	b.marks[key] = ts
}

func (b *slackBatch) empty() bool { return len(b.rows) == 0 && len(b.marks) == 0 }

// --- the pass ---

// slackPass polls Slack once, in its own goroutine, at most one at a time.
//
// It is called from every daemon tick inside the !mirror block — the tick IS
// the cadence, with no interval of its own (a second throttle on top of a 15s
// tick would only add a knob nobody could reason about). A pass still running
// when the next tick arrives simply loses that tick, silently: it is polling
// right now, which is what the tick wanted.
//
// all is accepted so this and deliverSlack share one call shape; the poll
// itself depends on no discovery (EnsureIris takes the tick's live-session map
// instead — see the daemon's comment on the three calls).
func slackPass(st *store.Store, all []*core.Peer, now time.Time) {
	if st == nil {
		return // file-mode entrypoints have nowhere to put the rows
	}
	if !slackPassRunning.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer slackPassRunning.Store(false)
		runSlackPass(st, now)
	}()
}

// errSlackHalt tells the pass to stop sweeping and go straight to the commit.
// It is raised only by a 429: everything else is isolated to the conversation
// that produced it. It is deliberately NOT an abandonment — whatever the sweep
// accumulated before the throttle hit is still committed, because those rows
// were really read and their marks really do cover them.
var errSlackHalt = errors.New("slack: pass halted")

// runSlackPass is the pass itself.
//
// Failure is ISOLATED rather than fatal, which is the one structural thing to
// know about this function. A Slack error on one conversation skips that
// conversation — its marks untouched, so the next pass re-reads exactly what it
// missed — and the sweep continues; the batch the healthy conversations
// produced still commits. The at-least-once invariant survives that because it
// was never a per-PASS property: marks only ever cover rows in the same
// committed batch, and a conversation that contributed no rows contributes no
// mark either. The alternative, which this replaced, was one unreachable DM
// stopping every other conversation on the machine from being read at all.
func runSlackPass(st *store.Store, now time.Time) {
	cfg, err := core.LoadSlackConfig()
	if err != nil {
		if errors.Is(err, core.ErrNoSlack) {
			return // feature off: no log, no error, no cost
		}
		slackLogOnce("config", "slack: reading config failed: %v", err)
		return
	}
	slackLogClear("config")
	if !slackReady(now) {
		return // inside a 429's Retry-After window
	}
	// Stamped before anything can bail — see SlackLastPollKV. It records an
	// ATTEMPT, which is what `kraken slack status` means by "last poll".
	if err := st.SetKV(SlackLastPollKV, strconv.FormatInt(now.UnixMilli(), 10)); err != nil {
		slackLogOnce("poll-stamp", "slack: recording the poll time failed: %v", err)
	} else {
		slackLogClear("poll-stamp")
	}

	client := core.NewSlackClient(cfg.Token, cfg.Cookie)
	selfID, err := slackSelfID(st, client, cfg)
	if err != nil {
		// Nothing can be swept without an identity — the self-filter and the
		// mention query both need it — so this one really is fatal to the pass,
		// and there is nothing accumulated to commit.
		slackHoldOffFor(err, now)
		slackLogOnce("auth", "slack: identifying the workspace user failed: %v", err)
		return
	}
	slackLogClear("auth")
	marks, err := st.SlackMarks()
	if err != nil {
		slackLogOnce("marks", "slack: reading watermarks failed: %v", err)
		return
	}
	slackLogClear("marks")

	b := newSlackBatch(now)
	halted := slackSweepConversations(client, marks, selfID, b, now) != nil
	if !halted {
		halted = slackSweepMentions(client, marks, selfID, b, now) != nil
	}
	// A halted pass means Slack just said 429: issuing users.info calls inside
	// the hold-off window we set two lines ago would be more of exactly what it
	// asked us to stop. Rows commit with blank names; the id is the identity.
	if !halted {
		slackResolveNames(client, b, now)
	}

	if !b.empty() {
		n, err := st.AddSlackBatch(b.rows, b.marks)
		if err != nil {
			slackLogOnce("commit", "slack: committing %d row(s) failed — watermarks left alone: %v", len(b.rows), err)
			return
		}
		slackLogClear("commit")
		if n > 0 {
			dlogEvent("info", "slack-inbox", "slack: %d new message(s) recorded", n)
		}
	}
	// Re-armed only by a pass that ran to the end without being throttled, so a
	// SUSTAINED rate limit keeps saying so once per recovery rather than going
	// quiet after its first line. Clearing it on any pass at all would silence
	// exactly the case worth hearing about.
	if !halted {
		slackLogClear("ratelimit")
	}
}

// slackHoldOffFor recognises a 429 and parks the next passes until its
// Retry-After has elapsed, reporting whether it did. Without the pause a rate
// limiter would receive the identical burst every 15 seconds and the workspace
// would stay throttled indefinitely.
func slackHoldOffFor(err error, now time.Time) bool {
	var rl *core.SlackRateLimitError
	if !errors.As(err, &rl) {
		return false
	}
	slackHoldOff(now.Add(rl.RetryAfter))
	slackLogOnce("ratelimit", "slack: rate limited — pausing polls for %s", rl.RetryAfter)
	return true
}

// slackSelfID resolves carl's own user id, which the pass needs to drop his own
// messages and to build the mention query.
//
// A configured SLACK_USER_ID wins outright and skips auth.test entirely; the
// cached kv row is next; only a machine with neither spends the call, and it
// caches the answer so it never spends it twice.
func slackSelfID(st *store.Store, client *core.SlackClient, cfg core.SlackConfig) (string, error) {
	if cfg.UserID != "" {
		return cfg.UserID, nil
	}
	if raw, err := st.GetKV(slackSelfKVKey); err == nil && raw != "" {
		var s slackSelf
		if json.Unmarshal([]byte(raw), &s) == nil && s.UserID != "" {
			return s.UserID, nil
		}
	}
	info, err := client.AuthTest()
	if err != nil {
		return "", err
	}
	if info.UserID == "" {
		return "", fmt.Errorf("auth.test returned no user id")
	}
	if data, err := json.Marshal(slackSelf{UserID: info.UserID, TeamID: info.TeamID}); err == nil {
		if err := st.SetKV(slackSelfKVKey, string(data)); err != nil {
			// Costs one auth.test next pass, nothing more.
			slackLogOnce("self-cache", "slack: caching the workspace identity failed: %v", err)
		}
	}
	return info.UserID, nil
}

// slackInventory returns the DM/MPIM conversation list, refreshed at most every
// slackInventoryTTL.
func slackInventory(client *core.SlackClient, now time.Time) ([]core.SlackConversation, error) {
	slackMem.Lock()
	if len(slackMem.inventory) > 0 && now.Sub(slackMem.inventoryAt) < slackInventoryTTL {
		convs := slackMem.inventory
		slackMem.Unlock()
		return convs, nil
	}
	slackMem.Unlock()

	convs, err := client.UserConversations()
	if err != nil {
		return nil, err
	}
	slackMem.Lock()
	slackMem.inventory, slackMem.inventoryAt = convs, now
	slackMem.Unlock()
	return convs, nil
}

// slackSweepConversations is Loop A: ONE recent-history call per swept
// conversation, serving two purposes at once, then its live threads.
//
// The single call does double duty because a thread parent is a message like
// any other: reading back a lookback window (slackFetchFloor) returns both the
// messages that are new since the watermark — which become rows, filtered at
// the tighter slackRowFloor — and the recent parents whose `latest_reply`
// fields are the only evidence a thread moved. Reading exactly from the
// watermark would have returned the first and never the second.
//
// One budget covers BOTH kinds of call. A thread chase is the same request
// against the same rate-limit tier as a history read, so budgeting only the
// history half would let a workspace with many live threads spend an unbounded
// number of calls per pass while still reporting itself as inside the budget.
// Spending it interleaved — this conversation's history, then this
// conversation's threads, then the next — is what keeps the rotation fair:
// running out mid-list just truncates the tail, and slackSweepOrder puts that
// tail at the front of the next pass.
//
// A Slack error on one conversation SKIPS that conversation and the sweep goes
// on. Its watermarks are left exactly as found, so the next pass re-reads what
// this one missed, and its lastSwept is stamped anyway — otherwise a single
// permanently-failing DM would sort first forever and eat the head of the
// budget on every pass, starving every healthy conversation behind it. Only a
// 429 stops the sweep, and even then what has already accumulated is committed.
func slackSweepConversations(client *core.SlackClient, marks map[string]string, selfID string, b *slackBatch, now time.Time) error {
	convs, err := slackInventory(client, now)
	if err != nil {
		if slackHoldOffFor(err, now) {
			return errSlackHalt
		}
		slackLogOnce("inventory", "slack: listing conversations failed — nothing swept this pass: %v", err)
		return nil
	}
	slackLogClear("inventory")
	budget := slackHistoryBudget
	for _, c := range slackSweepOrder(convs, slackSweptSnapshot(), slackHistoryBudget) {
		if c.ID == "" {
			continue
		}
		if budget <= 0 {
			break
		}
		key := "dm:" + c.ID
		mark := marks[key]
		rowFloor := slackRowFloor(mark, now)
		budget--
		msgs, err := client.ConversationHistory(c.ID, slackFetchFloor(mark, now), slackHistoryPageLimit)
		// Stamped whatever the outcome: this conversation has had its turn at
		// the front of the rotation, and a failure is not a reason to give it
		// the next pass' turn as well.
		slackNoteSwept(c.ID, now)
		if err != nil {
			if slackHoldOffFor(err, now) {
				return errSlackHalt
			}
			slackLogOnce("history:"+c.ID, "slack: reading conversation %s failed — skipped, its watermarks left alone: %v", c.ID, err)
			continue
		}
		slackLogClear("history:" + c.ID)
		source := slackSourceOf(c)
		for _, m := range filterSlackNew(msgs, rowFloor, selfID) {
			b.add(m, source)
		}
		// The watermark advances over EVERY message the response carried,
		// including carl's own and including the ones below rowFloor that were
		// re-read for thread detection: the mark is a read position, not a
		// record of what was stored, and holding it back at his last message
		// would re-read the conversation forever. advance() only ever raises it.
		for _, m := range msgs {
			b.advance(key, mark, m.TS)
		}
		if err := slackSweepThreads(client, c, msgs, marks, selfID, b, source, rowFloor, now, &budget); err != nil {
			return err
		}
	}
	return nil
}

// slackThreadChases decides which threads of one channel this pass should read,
// as a list of parent timestamps, NEWEST FIRST. PURE over
// (channelID, msgs, marks, floor, now).
//
// TWO arms, and the second one is the whole reason this function exists.
//
// The window arm reads the parents the history call just returned. Each carries
// `latest_reply`, the only signal Slack gives that a thread moved, compared
// against the thread's own watermark — or, for a thread with NO watermark yet,
// against floor, the same floor the conversation's new messages were filtered
// by. That second case is what bootstraps a thread: when a parent is first
// swept it has no replies and so no mark, and if the first reply arrives after
// that, comparing against the parent's own ts would call any reply newer than
// the parent (which is all of them) a chase — cheap and correct — while
// comparing against nothing at all would never chase it. floor is the honest
// middle: chase it if the thread has moved inside the window this pass covers.
//
// The persisted arm covers what the window arm structurally cannot see: a
// parent that has scrolled off the recent-history window. Every thread kraken
// has ever read has a `thread:<channel>:<parent>` mark, and those marks are the
// only durable record that the thread exists, so they are the enumeration. They
// carry no `latest_reply`, so a persisted thread is chased unconditionally;
// slackThreadRetireAfter is what bounds that, and a parent the window arm has
// ALREADY judged settled is not re-added, since the window's answer is fresher
// than no answer at all.
//
// Newest-first, and sorted at all, because the caller spends a bounded budget
// walking this list: unordered it would chase a random subset of one channel's
// threads each pass, and oldest-first would spend the budget on the least
// likely thread to matter while the conversation carl is in right now waits.
func slackThreadChases(channelID string, msgs []core.SlackMessage, marks map[string]string, floor string, now time.Time) []string {
	prefix := "thread:" + channelID + ":"
	chase := map[string]bool{}
	settled := map[string]bool{}
	for _, p := range msgs {
		if p.TS == "" || p.ReplyCount <= 0 || p.LatestReply == "" {
			continue
		}
		mark, seen := marks[prefix+p.TS]
		if !seen || mark == "" {
			mark = floor
		}
		if core.SlackTSLess(mark, p.LatestReply) {
			chase[p.TS] = true
		} else {
			settled[p.TS] = true
		}
	}
	for key, ts := range marks {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		// The trailing colon in prefix anchors the match, so channel C123 can
		// never claim C1234's marks.
		parent := strings.TrimPrefix(key, prefix)
		if parent == "" || chase[parent] || settled[parent] {
			continue
		}
		if slackThreadRetired(parent, ts, now) {
			continue
		}
		chase[parent] = true
	}
	out := make([]string, 0, len(chase))
	for ts := range chase {
		out = append(out, ts)
	}
	sort.SliceStable(out, func(i, j int) bool { return core.SlackTSLess(out[j], out[i]) })
	return out
}

// slackThreadRetired reports whether a persisted thread mark is old enough to
// stop polling. PURE.
//
// The mark's VALUE is the age that matters — it is the last reply actually
// read, so it moves whenever the thread does — with the parent's own ts as the
// fallback for a thread that has a mark but nothing readable in it. A mark with
// no datable timestamp anywhere is retired rather than chased: an
// unparseable one would otherwise be re-read every pass forever with no way
// for the sweep to ever satisfy it.
func slackThreadRetired(parentTS, markTS string, now time.Time) bool {
	secs, ok := slackTSSeconds(markTS)
	if !ok {
		secs, ok = slackTSSeconds(parentTS)
	}
	if !ok {
		return true
	}
	return now.Sub(time.Unix(secs, 0)) > slackThreadRetireAfter
}

// slackSweepThreads reads the replies of every thread slackThreadChases named,
// newest first, until the shared per-pass call budget runs out.
//
// A thread that has never been read uses its PARENT's ts as the request floor
// rather than no floor at all: the parent came through the history sweep a
// moment ago and is already in the batch, so an empty floor would just re-fetch
// it. Note this is the REQUEST's floor, not the chase DECISION's — that one is
// slackThreadChases' floor argument, and the two answer different questions
// ("what should come back" versus "is this thread worth a call").
//
// Running out of budget is a silent truncation and it is safe in exactly one
// direction: nothing is committed for a thread that was not read, so its mark
// stays put and the next pass reads it from the same place. A failed chase is
// isolated the same way the conversation loop isolates a failed history read —
// this thread's mark is untouched and the next thread is still tried — with a
// 429 the one error that stops the pass.
func slackSweepThreads(client *core.SlackClient, c core.SlackConversation, msgs []core.SlackMessage, marks map[string]string, selfID string, b *slackBatch, source, floor string, now time.Time, budget *int) error {
	for _, parent := range slackThreadChases(c.ID, msgs, marks, floor, now) {
		if *budget <= 0 {
			return nil
		}
		key := "thread:" + c.ID + ":" + parent
		mark := marks[key]
		if mark == "" {
			mark = parent
		}
		*budget--
		replies, err := client.ConversationReplies(c.ID, parent, mark)
		if err != nil {
			if slackHoldOffFor(err, now) {
				return errSlackHalt
			}
			slackLogOnce("replies:"+c.ID, "slack: reading a thread of %s failed — skipped, its watermark left alone: %v", c.ID, err)
			continue
		}
		slackLogClear("replies:" + c.ID)
		for _, r := range filterSlackNew(replies, mark, selfID) {
			b.add(r, source)
		}
		for _, r := range replies {
			b.advance(key, mark, r.TS)
		}
	}
	return nil
}

// slackSweepMentions is Loop B: the only way to see a direct @-mention in a
// channel, since Slack has no mentions API. The query is the literal `<@U…>`
// token the message text actually contains, plus a day-grained `after:` window.
//
// NO client-side ts floor is applied to the results, and that is the opposite
// of what Loop A does. Slack's search INDEX lags posting by an unbounded amount,
// so a mention indexed late arrives BELOW the newest ts already seen — an exact
// floor would drop precisely the messages the widened `after:` window was opened
// to catch, and drop them permanently, since the mark only ever moves forwards.
// The store's UNIQUE(channel_id, ts) is the dedupe instead: a re-read costs an
// ignored INSERT, a dropped mention costs the mention.
//
// The `mentions` mark therefore means "how far the after: window should start",
// nothing more. Only three things are refused: carl's own posts, a result with
// no ts (half the row's identity), and one with no channel id (the other half —
// search returns the channel as a nested object, so an empty one means a shape
// this client could not read).
func slackSweepMentions(client *core.SlackClient, marks map[string]string, selfID string, b *slackBatch, now time.Time) error {
	if selfID == "" {
		return nil
	}
	const key = "mentions"
	mark := marks[key]
	results, err := client.SearchMessages("<@" + selfID + "> after:" + mentionAfterDate(mark, now))
	if err != nil {
		if slackHoldOffFor(err, now) {
			return errSlackHalt
		}
		slackLogOnce("mentions", "slack: searching mentions failed — the mentions watermark is left alone: %v", err)
		return nil
	}
	slackLogClear("mentions")
	for _, m := range results {
		if m.TS == "" || m.ChannelID == "" || m.User == selfID {
			continue
		}
		// A mention that is also a DM was already added by Loop A under its
		// truer source; b.add keeps the first.
		b.add(m, "mention")
	}
	for _, m := range results {
		b.advance(key, mark, m.TS)
	}
	return nil
}

// slackResolveNames fills each row's author_name from users.info, one call per
// distinct author and cached for the process' life.
//
// The name is DECORATION: the author id is already on the row and is the
// durable identity, so a failure must never cost a message. It aborts the whole
// resolution for this pass rather than continuing per-author, because the
// failures worth reacting to here are workspace-wide (a revoked scope, a 429),
// and grinding through one users.info per row to collect the same error N times
// is how a rate limit turns into a worse rate limit. Rows already resolved keep
// their names; the rest commit with a blank one and KEEP it — nothing ever
// updates author_name on a committed row (AddSlackBatch is INSERT OR IGNORE),
// and that is fine because the id is the identity. What the per-author cache
// buys is future rows by the same author, once a later pass resolves them.
func slackResolveNames(client *core.SlackClient, b *slackBatch, now time.Time) {
	for i := range b.rows {
		id := b.rows[i].AuthorID
		if id == "" {
			continue
		}
		slackMem.Lock()
		name, cached := slackMem.users[id]
		slackMem.Unlock()
		if !cached {
			var err error
			name, err = client.UserInfo(id)
			if err != nil {
				slackHoldOffFor(err, now)
				slackLogOnce("userinfo", "slack: resolving author names failed (ids kept, names left blank): %v", err)
				return
			}
			slackMem.Lock()
			if slackMem.users == nil {
				slackMem.users = map[string]string{}
			}
			slackMem.users[id] = name
			slackMem.Unlock()
		}
		b.rows[i].AuthorName = name
	}
	slackLogClear("userinfo")
}

// --- delivery ---

// slackSendToPeer is the seam deliverSlack types through — the package's
// existing indirection pattern (bindGate, checkUpdateAvail, dreamPass),
// and here it carries the only property of this pass worth pinning: the ORDER
// of the send against the stamp. "delivered_at is stamped only after a
// confirmed send" is invisible from the outside unless the send can be made to
// fail on demand, and no test in this repo may type into a real tmux pane.
var slackSendToPeer = core.SendToPeer

// deliverSlack tells iris that there are rows waiting: ONE bump per batch,
// carrying a count and the command to run, never the messages themselves.
//
// Send-then-watermark, exactly as watchPRs and syncCellColor do it: delivered_at
// is stamped only after slackSendToPeer confirms, so a refused send (iris is
// mid-question, its pane is starting) leaves every row pending and the next
// tick tries again. The reverse order would mark a batch delivered that iris
// never heard about, and nothing anywhere would ever mention those messages
// again — the rows are not lost, but the only agent that reads them would never
// be told they exist.
//
// No iris and no rows are both plain no-ops. iris being absent is the ordinary
// state during its own boot, and treating it as a failure would log an error
// every 15 seconds on a machine whose only problem is that claude is starting.
func deliverSlack(st *store.Store, all []*core.Peer) {
	if st == nil {
		return
	}
	rows, err := st.UndeliveredSlackInbox()
	if err != nil {
		slackLogOnce("undelivered", "slack: reading undelivered rows failed: %v", err)
		return
	}
	slackLogClear("undelivered")
	if len(rows) == 0 {
		return
	}
	iris := findIris(all)
	if iris == nil {
		return // rows stay pending; retried next tick
	}
	if _, err := slackSendToPeer(iris, slackBumpMessage(len(rows))); err != nil {
		// Deduped like watchPRs' bumpfail key: an iris sitting on a question
		// prompt would otherwise write four identical lines a minute all night.
		slackLogOnce("bump", "slack: bumping iris failed — rows stay undelivered: %v", err)
		return
	}
	slackLogClear("bump")
	ids := make([]int64, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	if err := st.MarkSlackDelivered(ids, time.Now().UnixMilli()); err != nil {
		// The keystroke is already gone. Leaving the stamps off means the next
		// tick bumps again — a duplicate nudge, which iris resolves by reading
		// the inbox, and the cheaper of the two mistakes.
		slackLogOnce("delivered-stamp", "slack: stamping %d delivered row(s) failed: %v", len(ids), err)
		return
	}
	slackLogClear("delivered-stamp")
	dlogEvent("info", "slack-bump", "slack: bumped iris with %d new message(s)", len(ids))
}
