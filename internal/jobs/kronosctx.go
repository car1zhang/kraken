package jobs

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"kraken/internal/store"
)

// kronosctx.go — kronos' context meter and the two things kraken does with it:
// warn carl before auto-compact does it for him, and re-arm graph recall after
// a compaction happens.
//
// The measurement cannot be taken from outside the pane. Claude Code knows how
// full a session's context window is; nothing else does, and no file it writes
// reports it. What it DOES offer is the statusline command, which it runs with
// the live session's numbers on stdin — so kronos' settings.json points that
// command at `kraken ctx-hook`, which POSTs the numbers here. That is the only
// reason this file exists as a server-side pass at all: it is reacting to a
// measurement that arrives, rather than taking one.
//
// Auto-compact stays ON (Q3), everywhere, always. Nothing in this file
// disables it and nothing should: it is the last-resort net under a kronos
// nobody is watching, and the warnings below exist to give carl the chance to
// compact deliberately BEFORE the net catches it — not to replace the net.

// kronosCtxKVKey holds the last context report. One kv singleton rather than a
// table because there is exactly one kronos and only its LATEST fill matters —
// a history of the meter would be a chart nobody reads, kept forever.
const kronosCtxKVKey = "kronos_ctx"

// kronosCtxWarnedKVKey holds which thresholds have already fired, keyed to the
// session they fired for. Durable rather than process state for the reason the
// warnings exist: `kraken serve` restarts, and a fired-set that reset with the
// process would re-warn carl about a 90%-full context every time the daemon
// came back.
const kronosCtxWarnedKVKey = "kronos_ctx_warned"

// KronosContext is one context report. Exported because GET /api/kronos serves
// it — pct/window/at are the wire's own three fields, and a second
// hand-decoding of this kv key in internal/api is how a status route quietly
// reports zero forever.
//
// SessionID rides along and is NOT on the wire: it is what makes the fired-set
// below resettable (a new session is a fresh context, so a 90% that fired for
// the old one says nothing about this one), and it is what RearmKronosRecall
// checks a caller against.
type KronosContext struct {
	SessionID string  `json:"session_id"`
	Pct       float64 `json:"pct"`
	Window    int64   `json:"window"`
	At        int64   `json:"at"` // unix millis
}

// kronosCtxWarned is what kronosCtxWarnedKVKey holds: the session the set
// belongs to, and the thresholds already fired for it.
type kronosCtxWarned struct {
	SessionID string `json:"session_id"`
	Fired     []int  `json:"fired"`
}

// kronosCtxThresholds are the fills carl is warned at, ascending. Three rather
// than one because they answer different questions: 50% is "this conversation
// has a shape now", 75% is "compact soon if you want to choose what survives",
// and 90% is "auto-compact is about to choose for you".
var kronosCtxThresholds = []int{50, 75, 90}

// RecordKronosContext persists one report from the statusline hook.
//
// Both refusals are about a number that would poison the meter rather than
// merely be wrong. A blank session id makes the report unattributable, and the
// fired-set keys on it — an unattributed report would either reset the set
// (re-warning on every firing) or inherit another session's (never warning
// again). A percentage outside [0,100] is not a fill at all; stored, it would
// pin the meter at a band nothing can clear.
//
// NaN is checked SEPARATELY and it has to be: every comparison against NaN is
// false, so `pct < 0 || pct > 100` lets it straight through — it is the one
// out-of-range value a range test cannot see. Stored, it is worse than a wild
// number rather than better: the board would render a NaN fill, and
// kronosCtxDue's own `c.Pct >= float64(t)` would be false at every threshold,
// so the meter would sit there silently never warning about anything again.
// (±Inf needs no special case — +Inf > 100 and -Inf < 0 both hold.)
func RecordKronosContext(st *store.Store, sessionID string, pct float64, window int64) error {
	if st == nil {
		return fmt.Errorf("kronos context: no store")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return fmt.Errorf("kronos context: blank session id")
	}
	if math.IsNaN(pct) || pct < 0 || pct > 100 {
		return fmt.Errorf("kronos context: percentage out of range")
	}
	data, err := json.Marshal(KronosContext{
		SessionID: sessionID,
		Pct:       pct,
		Window:    window,
		At:        time.Now().UnixMilli(),
	})
	if err != nil {
		return err
	}
	return st.SetKV(kronosCtxKVKey, string(data))
}

// ReadKronosContext returns the last report, and whether one exists. The
// second return is load-bearing on the wire: `context` is a POINTER on
// /api/kronos and omitted when never reported, because a zeroed object would
// claim a fill of zero — and 0% and "never measured" are different facts that
// a board renders differently.
func ReadKronosContext(st *store.Store) (KronosContext, bool) {
	if st == nil {
		return KronosContext{}, false
	}
	raw, err := st.GetKV(kronosCtxKVKey)
	if err != nil || raw == "" {
		return KronosContext{}, false
	}
	var c KronosContext
	if err := json.Unmarshal([]byte(raw), &c); err != nil || c.SessionID == "" {
		return KronosContext{}, false
	}
	return c, true
}

func readKronosCtxWarned(st *store.Store) kronosCtxWarned {
	raw, err := st.GetKV(kronosCtxWarnedKVKey)
	if err != nil || raw == "" {
		return kronosCtxWarned{}
	}
	var w kronosCtxWarned
	if err := json.Unmarshal([]byte(raw), &w); err != nil {
		// An unparseable set reads as EMPTY, not as "everything fired": the
		// cost of the first reading is one duplicate warning, the cost of the
		// second is silence at 90% with nothing able to repair it.
		return kronosCtxWarned{}
	}
	return w
}

func writeKronosCtxWarned(st *store.Store, w kronosCtxWarned) error {
	sort.Ints(w.Fired)
	data, err := json.Marshal(w)
	if err != nil {
		return err
	}
	return st.SetKV(kronosCtxWarnedKVKey, string(data))
}

// kronosCtxDue is ctxPass' pure core: given a fill, the session it belongs to,
// and the fired-set as stored, it returns the threshold to warn about (0 for
// none) and the fired-set as it should be written back.
//
// Three rules, and each of them is a state the meter has actually been in.
//
// A NEW SESSION resets the set outright. A restarted or compacted kronos is a
// fresh context, and a 90% that fired for the old one is not evidence about
// this one — carried over, it would suppress the warning that matters most.
//
// A pct BACK BELOW a threshold re-arms it. That is what makes a compaction
// visible: the fill drops, the thresholds re-arm, and the next climb warns
// again. Without it the session would warn once ever and then go quiet through
// every subsequent fill.
//
// A jump that crosses SEVERAL thresholds at once warns ONCE, about the highest
// one crossed, and marks them all fired. Three messages arriving in the same
// second would say one thing three times, and the highest is the only one that
// changes what carl would do.
func kronosCtxDue(c KronosContext, w kronosCtxWarned) (warn int, next kronosCtxWarned) {
	next = kronosCtxWarned{SessionID: c.SessionID}
	fired := map[int]bool{}
	if w.SessionID == c.SessionID {
		for _, t := range w.Fired {
			// Re-arm anything the fill has fallen back below; keep the rest.
			if c.Pct >= float64(t) {
				fired[t] = true
			}
		}
	}
	for _, t := range kronosCtxThresholds {
		if c.Pct >= float64(t) && !fired[t] {
			fired[t] = true
			if t > warn {
				warn = t
			}
		}
	}
	for t := range fired {
		next.Fired = append(next.Fired, t)
	}
	sort.Ints(next.Fired)
	return warn, next
}

// kronosCtxWarning is the text of one warning. PURE. It names kronos, the
// actual percentage rather than the threshold (the threshold is why it fired,
// the percentage is the fact), and what happens if he ignores it — which is
// the half that makes the message actionable rather than alarming:
// auto-compact takes over near the window, so doing nothing is a real option
// with a known cost, not a failure. It must render in at most 120 RUNES at
// any percentage: notify truncates the body with core.TruncateOneLine(msg,
// 120), and a text sized past that bound loses its tail — which is exactly
// the do-nothing clause — with nothing anywhere reporting the clip.
func kronosCtxWarning(pct float64) string {
	return fmt.Sprintf("kronos: context %.1f%% full — compact now to choose what survives; auto-compact takes over near the window.", pct)
}

// kronosCtxSending single-flights the warning SEND across ticks, the same
// shape slackPassRunning/usagePassRunning use.
//
// The send is notifyFn — an osascript exec with NO deadline of its own — and
// it is the one thing in this pass that can take unbounded wall clock: a
// pending Notification Center consent or a wedged osascript blocks until
// somebody or something clears it. So the send runs in its own goroutine, and
// a tick that finds one still in flight simply skips its attempt rather than
// queue a second exec behind a stuck one. The trade that buys is bounded and
// worth stating: a genuinely hung exec parks THIS pass — and only this pass —
// until the daemon restarts, where the daemon's other synchronous notify
// call sites would wedge the whole tick first.
var kronosCtxSending atomic.Bool

// ctxPass fires the warnings. Called every tick from the daemon's !mirror
// block; every arm is a no-op, so a machine with the feature off or a kronos
// nobody has measured costs one stat and one kv read.
//
// Delivery is FIRE-AND-FORGET, not send-then-watermark: notifyFn returns
// nothing, so the fired-set advances whether or not the notification actually
// rendered — an osascript that exits non-zero (Notification Center consent
// denied for the daemon, no GUI session) is invisible here, and that
// threshold is not re-offered until the fill falls back below it. That is a
// deliberate trade recorded with the bridge's removal (50-contract.md,
// amendments round 4): the push half is best effort, the board's meter and
// banner — rendered live from /api/kronos on every poll — are the durable
// channel, and restoring a confirmed send would mean giving notify an error
// return, a signature change that ripples to every daemon call site for a
// channel the board already carries.
func ctxPass(st *store.Store) {
	if st == nil {
		return
	}
	if !KronosEnabled() {
		return
	}
	c, ok := ReadKronosContext(st)
	if !ok {
		return // nothing has ever reported; nothing to warn about
	}
	stored := readKronosCtxWarned(st)
	warn, next := kronosCtxDue(c, stored)
	if warn == 0 {
		// The set can still have CHANGED without a warning — a fill that fell
		// back below a threshold re-arms it, and a new session empties it — so
		// it is written whenever it differs from what is stored, or a
		// drop-then-climb would never warn again. Compared against the value
		// read above rather than re-read: this runs every tick, and the pass
		// is the only writer.
		if !sameKronosWarned(stored, next) {
			if err := writeKronosCtxWarned(st, next); err != nil {
				dlogErr("kronos: re-arming the context warnings failed: %v", err)
			}
		}
		return
	}
	msg := kronosCtxWarning(c.Pct)
	if !kronosCtxSending.CompareAndSwap(false, true) {
		// A send from an earlier tick is still in flight — a first send waiting
		// on the TCC dialog is the case this exists for. Skip silently: the
		// fired-set is untouched, so this threshold is still armed and the tick
		// after that one finishes will offer it again. Sending a second copy
		// into a queue behind the first is the one thing that would make a
		// stalled channel worse.
		return
	}
	go func() {
		defer kronosCtxSending.Store(false)
		// The desktop notification is the warning's whole push delivery —
		// best effort, per the fire-and-forget note on ctxPass; the board's
		// meter and banner carry the same fact for anyone looking. notifyFn
		// shells to osascript, which is why this stays off the tick behind
		// the single-flight even without a network send in it.
		notifyFn("kraken ⚠ kronos", msg)
		if err := writeKronosCtxWarned(st, next); err != nil {
			// The message is already away. Leaving the set unwritten means a
			// later tick warns again — a duplicate, which is the cheaper of the
			// two mistakes against a warning that fires and is then forgotten.
			dlogErr("kronos: recording the fired context warning failed: %v", err)
			return
		}
		dlogEvent("info", "kronos-ctx", "kronos: context %.1f%% — warned at %d%%", c.Pct, warn)
	}()
}

// sameKronosWarned compares two fired-sets. Both are written sorted, so this
// is an element-wise comparison rather than a set operation.
func sameKronosWarned(a, b kronosCtxWarned) bool {
	if a.SessionID != b.SessionID || len(a.Fired) != len(b.Fired) {
		return false
	}
	for i := range a.Fired {
		if a.Fired[i] != b.Fired[i] {
			return false
		}
	}
	return true
}

// RearmKronosRecall clears graph recall's per-session delivery record for
// kronos' session, and it exists because a COMPACTION destroys the only thing
// that record is about.
//
// kg_recall says "this session has already been told this fact", and the point
// of it is that the fact is still IN the conversation. A compaction rewrites
// the conversation into a summary, so the facts recall spent its once-per-fact
// budget on are gone — but the rows saying they were delivered are not, and
// without this they would suppress those facts for the rest of the session's
// life. `kraken rearm-hook` runs on PostCompact and calls this.
//
// The session check is the whole security of the route. Recall rows belong to
// every session in the system — every cell's zeus, every teammate, every side
// thread — and a route that cleared an arbitrary session's rows would be a way
// to make any agent re-receive every fact it has ever been told. Only kronos'
// OWN recorded session id is accepted, so the hook can only ever re-arm the
// pane it fired from. No kronos recorded is a refusal, not an empty success:
// nothing to compare against is not the same as a match.
func RearmKronosRecall(st *store.Store, sessionID string) (int64, error) {
	if st == nil {
		return 0, fmt.Errorf("kronos rearm: no store")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return 0, fmt.Errorf("kronos rearm: blank session id")
	}
	agent, ok := ReadKronosAgent(st)
	if !ok || agent.SessionID == "" {
		return 0, fmt.Errorf("kronos rearm: no kronos session recorded")
	}
	if agent.SessionID != sessionID {
		return 0, fmt.Errorf("kronos rearm: session is not kronos'")
	}
	return st.ClearRecalled(sessionID)
}
