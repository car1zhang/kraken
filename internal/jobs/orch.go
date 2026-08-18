package jobs

import (
	"fmt"
	"sync"
	"time"

	"kraken/internal/core"
	"kraken/internal/store"
)

// orch.go — the orchestrator's INBOX and its delivery half: EnqueueOrch writes
// a row when something happens on the board, deliverOrch tells kronos there
// are rows to read.
//
// It is slack.go's delivery contract with the poller removed. The events
// kronos is told about are not fetched from anywhere — the daemon already
// detects every one of them (that is what its notify() calls are), so the
// whole producer side is five lines added at five transitions kraken already
// recognises. What this file owns is the part slack.go proved has to be owned
// somewhere: the ORDER of the send against the watermark, and the rule that a
// bump carries a COUNT rather than content.
//
// Send-then-watermark, exactly as deliverSlack and watchPRs do it. delivered_at
// is stamped only after a confirmed send, so a refused send (kronos is
// mid-question, its pane is starting) leaves every row pending and the next
// tick tries again. The reverse order would mark a batch delivered that kronos
// never heard about, and nothing anywhere would ever mention those events
// again — the rows are not lost, but the only agent that reads them would
// never be told they exist.
//
// The row lifecycle is the slack_inbox one: seen_at, delivered_at, acked_at,
// three stamps and no state column. Delivery and ack are separate on purpose —
// a bump that failed must not make a row unreachable, which is why UnackedOrch
// (what `kraken orch inbox` serves) ignores delivered_at entirely.

// The kinds an inbox row can carry — five board transitions the daemon
// already notifies on. They are constants rather than literals at the call
// sites because the wire, the CLI's output and the store's rows all show the
// same string, and a typo at one transition would produce a kind nothing else
// in the system has ever heard of — visible only as an odd line in kronos'
// pane. (An `imsg` kind carrying carl's own iMessages existed briefly and was
// removed with the whole iMessage bridge — carl's call at the manual gate;
// the board panel is the channel for now.)
const (
	orchKindJobBlocked   = "job-blocked"
	orchKindCellGone     = "cell-gone"
	orchKindCellUnhealth = "cell-unhealthy"
	orchKindPRMerged     = "pr-merged"
	orchKindStartFailed  = "start-failed"
)

// orchSummaryCap bounds a DAEMON row's summary. That summary is a SENTENCE for
// a human (well, for kronos) to read in a JSON list, not a payload: the durable
// evidence for any of the five board transitions is the board, the job file and
// the event log, all of which kronos can read by slug. 2000 runes is small
// enough that no single row can make `kraken orch inbox` unreadable.
const orchSummaryCap = 2000

// EnqueueOrch records one inbox row. A thin wrapper over the store, and
// deliberately thin: it does not dedupe, it does not decide whether the event
// is interesting, and it does not know what kronos will do about it. Dedup
// belongs to the CALLER, because the callers are daemon transitions that
// already carry a Notified-map key per streak — a persisting condition (a job
// that stays blocked for an hour) fires this once, at its transition, exactly
// as it fires one notification.
//
// The summary is one-lined and capped here rather than at the call sites for
// the reason every one-lining in this binary is done once: a multi-line
// summary would break the JSON list `kraken orch inbox` prints into something
// kronos has to reassemble, and five call sites each remembering to trim is
// five chances to forget.
//
// A nil store is a silent no-op, the stance every pass in this package takes
// toward a store-less entrypoint: there is nowhere to put the row, and that is
// not an error anybody can act on.
func EnqueueOrch(st *store.Store, kind, slug, summary string) error {
	if st == nil {
		return nil
	}
	_, err := st.AddOrchItem(kind, slug, core.TruncateOneLine(summary, orchSummaryCap), time.Now().UnixMilli())
	return err
}

// enqueueOrchNotice is what the daemon's transition points call: one line at
// each site, beside the notify() that already fires there.
//
// It exists so the five call sites stay one line each and so the three things
// they all need — the feature gate, the restart dedup and the failure handling
// — are written once. KronosEnabled() is checked HERE rather than inside
// EnqueueOrch because the two have different jobs: EnqueueOrch is the dumb
// store wrapper and must write whatever it is handed; this is the daemon's own
// producer, and a machine that never opted in must not accumulate rows nobody
// will ever read. (Verified this session: this is EnqueueOrch's only caller —
// nothing in internal/api reaches it.)
//
// The DEDUP is the second thing, and it is what makes "once per streak" true
// across a restart. The call sites key on d.Notified, which is rebuilt empty
// every time `kraken serve` starts — so five jobs that sat blocked overnight,
// which kronos already read, reported and acked, would each get a fresh row on
// the first tick after an UPDATE or a reboot, and kronos would text carl about
// all five again. An UNACKED row of the same kind and slug already sitting in
// the inbox says the condition has already been reported and not yet handled,
// which is exactly what the in-memory key would have said had it survived.
// Once kronos acks it, the same condition re-notifying is a real event again.
//
// One kind is EXEMPT from that dedup, because its events are unrepeatable —
// suppressing one loses it permanently rather than deferring it.
//
//   - pr-merged (round-3 amendment). Cells and PRs are many-to-one and the
//     branch recycles after each merge, so a job merges PR #12 and later PR
//     #13; the daemon deliberately keeps NO Notified key for it because a PR is
//     newly-merged exactly once ever, and once writeJobPRs has committed #13's
//     merged state nothing reports it again. Deduping on kind+slug would find
//     #12's unacked row and drop #13's URL from this channel for good.
//
// cell-unhealthy is the one kind whose call-site key is FINER than kind+slug
// (it embeds the reason), so a cell that goes from an API error to an idle
// stall keeps the first reason on its unacked row rather than gaining a second
// row. That coarseness is ACCEPTED by the same amendment: the reason is a
// description of a condition that is still true, kronos re-reads the cell's
// real state anyway, and the next distinct event after the ack carries the
// current reason.
//
// A failed write is logged once per streak and never propagated: the
// transition it accompanies has already notified, already logged an event, and
// already moved the board. Failing a tick over an inbox row would trade the
// thing that works for the thing that did not.
func enqueueOrchNotice(kind, slug, summary string) {
	if !KronosEnabled() {
		return
	}
	st := ActiveStore()
	if orchKindDedups(kind) && orchNoticePending(st, kind, slug) {
		return
	}
	if err := EnqueueOrch(st, kind, slug, summary); err != nil {
		orchLogOnce("enqueue:"+kind, "orch: recording a %s item for %s failed: %v", kind, slug, err)
		return
	}
	orchLogClear("enqueue:" + kind)
}

// orchKindDedups reports whether a kind's rows are deduped against the unacked
// inbox at all. It is a predicate rather than a condition at the call site
// because the exemption is a property of the KIND — of whether its events can
// recur — and enqueueOrchNotice is where every daemon kind arrives.
func orchKindDedups(kind string) bool {
	return kind != orchKindPRMerged
}

// orchNoticePending reports whether an unacked row of this kind and slug is
// already waiting for kronos.
//
// It scans UnackedOrch rather than asking the store a targeted question, and
// that is a size judgement rather than an oversight: the same query already
// runs every tick behind deliverOrch's sibling read and every time kronos runs
// `kraken orch inbox`, over a table whose unacked population is the handful of
// rows one agent has not yet worked through. If that population ever stops
// being a handful, the fix is the index the schema comment already names, not a
// second shape of read here.
//
// A read FAILURE reports "not pending", so the row is written. The two
// mistakes are not symmetrical: a duplicate row costs kronos one ack, while
// suppressing a row because the store could not be asked loses the only durable
// record that a cell went unhealthy.
func orchNoticePending(st *store.Store, kind, slug string) bool {
	if st == nil {
		return false
	}
	rows, err := st.UnackedOrch()
	if err != nil {
		orchLogOnce("pending:"+kind, "orch: checking for a pending %s item for %s failed — enqueuing anyway: %v", kind, slug, err)
		return false
	}
	orchLogClear("pending:" + kind)
	for _, r := range rows {
		if r.Kind == kind && r.Slug == slug {
			return true
		}
	}
	return false
}

// orchBumpMessage is the one line the delivery pass types into kronos' pane.
// PURE. Deliberately tiny and deliberately a COUNT rather than the content:
// the events live in the inbox, kronos reads them with the command named here,
// and typing event bodies into a pane would both flood it and make the pane —
// not kraken.db — the thing that has to be reliable.
//
// One wording for every count, `item(s)` included: the singular/plural split
// slackBumpMessage carries buys nothing here and gives the string two shapes
// where a test, a log line and a reader each have to know both.
func orchBumpMessage(n int) string {
	return fmt.Sprintf("orch: %d new item(s) — run: kraken orch inbox", n)
}

// orchSendToPeer is the seam deliverOrch types through — the package's
// existing indirection pattern (slackSendToPeer, bindGate, dreamPass), and
// here it carries the only property of this pass worth pinning: the ORDER of
// the send against the stamp. "delivered_at is stamped only after a confirmed
// send" is invisible from the outside unless the send can be made to fail on
// demand, and no test in this repo may type into a real tmux pane.
var orchSendToPeer = core.SendToPeer

// orchMem is the delivery pass' cross-tick memory: the dedup set behind
// orchLogOnce, the same job slackMem.logged does for the Slack pass and
// d.Notified does for the daemon's own. This pass cannot reach d.Notified —
// deliverOrch is a free function, called with a store and a snapshot — so it
// keeps its own.
var orchMem struct {
	sync.Mutex
	logged map[string]bool
}

// orchLogOnce writes one error line per distinct key per outage. A store that
// cannot answer, or a kronos parked on a question prompt, produces the same
// failure four times a minute for as long as it lasts, and the log is read by
// a human.
func orchLogOnce(key, format string, a ...any) {
	orchMem.Lock()
	if orchMem.logged == nil {
		orchMem.logged = map[string]bool{}
	}
	already := orchMem.logged[key]
	orchMem.logged[key] = true
	orchMem.Unlock()
	if !already {
		dlogErr(format, a...)
	}
}

// orchLogClear re-arms a key once the thing it complained about works again,
// so a second outage is reported rather than swallowed.
func orchLogClear(key string) {
	orchMem.Lock()
	defer orchMem.Unlock()
	delete(orchMem.logged, key)
}

// deliverOrch tells kronos that there are rows waiting: ONE bump per batch,
// carrying a count and the command to run, never the events themselves.
//
// No kronos and no rows are both plain no-ops. kronos being absent is the
// ordinary state during its own boot, and treating it as a failure would log
// an error every 15 seconds on a machine whose only problem is that claude is
// starting.
func deliverOrch(st *store.Store, all []*core.Peer) {
	if st == nil {
		return
	}
	if !KronosEnabled() {
		return
	}
	rows, err := st.UndeliveredOrch()
	if err != nil {
		orchLogOnce("undelivered", "orch: reading undelivered rows failed: %v", err)
		return
	}
	orchLogClear("undelivered")
	if len(rows) == 0 {
		return
	}
	kronos := findKronos(all)
	if kronos == nil {
		return // rows stay pending; retried next tick
	}
	// A QUEUED send counts as delivered. core.SendToPeer queues when the pane
	// cannot take the keystroke right now, and the keystroke still arrives —
	// treating that as a failure would re-bump on every tick until the pane
	// drained, which is the flood the count-only message exists to avoid.
	if _, err := orchSendToPeer(kronos, orchBumpMessage(len(rows))); err != nil {
		// Deduped like watchPRs' bumpfail key: a kronos sitting on a question
		// prompt would otherwise write four identical lines a minute all night.
		orchLogOnce("bump", "orch: bumping kronos failed — rows stay undelivered: %v", err)
		return
	}
	orchLogClear("bump")
	ids := make([]int64, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	if err := st.MarkOrchDelivered(ids, time.Now().UnixMilli()); err != nil {
		// The keystroke is already gone. Leaving the stamps off means the next
		// tick bumps again — a duplicate nudge, which kronos resolves by
		// reading the inbox, and the cheaper of the two mistakes.
		orchLogOnce("delivered-stamp", "orch: stamping %d delivered row(s) failed: %v", len(ids), err)
		return
	}
	orchLogClear("delivered-stamp")
	dlogEvent("info", "orch-bump", "orch: bumped kronos with %d new item(s)", len(ids))
}
