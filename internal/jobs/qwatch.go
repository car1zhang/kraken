package jobs

import (
	"errors"
	"fmt"
	"hash/fnv"
	"sort"
	"strconv"
	"strings"
	"time"

	"kraken/internal/core"
	"kraken/internal/store"
)

// The question watcher: bump a live cell's zeus when carl answers one of its
// doc questions on the board. Answers land as SILENT row updates — the brief
// tells zeus to re-read `kraken questions get` on every resume, but a cell
// parked at a gate has no resume coming, so before this pass existed its
// only choices were a heartbeat poll (measured at ~1.2M cache-write tokens
// over one five-hour park, re-caching a ~390K-token conversation to learn
// "still quiet") or sitting blind past an answer that had already landed (a
// renderer was once held four hours for a revision whose gating answer had
// arrived hours earlier, unread). The daemon already acts on live panes
// unprompted — the PR watcher's bumps, the colour reconcile's /color, the
// limit rescue's kill-and-respawn; this is the same move for
// questions.answer. The cell-side half ships in the same change: zeus'
// brief (crew.go) and /do step 2 now say answers arrive as this bump as
// well as silent row updates, so a cell at a gate can end the turn and
// wait instead of heartbeat-polling.

// questionWatchPrefix opens every message this watcher types into a pane.
// Same provenance hazard as prWatchPrefix: the message arrives on the same
// user channel as carl's own words, on a timer, with no knowledge of what
// the pane last said — the tag is what keeps it from being read as carl's
// reply to whatever zeus asked last. (Unlike prWatchPrefix's, this wire
// text is not yet pinned per-variant by a test; there is one message.)
const questionWatchPrefix = "[kraken question-watch — automated notice, not a reply from carl]"

// questionBumpKey is the per-job watermark: the highest answered_at this
// watcher has successfully delivered a bump for (or a seed — see
// questionBumpDecision). Keyed on the immutable job id for the same reason
// limit_hit is — a slug freed by archiving must never let a new job read a
// dead generation's watermark. Never deleted: ids are AUTOINCREMENT and
// never reused, and a surviving watermark is what lets a reactivated job
// receive the answers that landed while it was done.
func questionBumpKey(jobID int64) string { return fmt.Sprintf("question_bump:%d", jobID) }

// questionBumpSeen is the watermark a job is seeded with when its question
// rows are first sighted before any of them is answered. It must be
// non-empty (empty means "never seen", the seed trigger) and must order
// below every real answered_at ("0" < "2…" lexicographically), so the first
// answer that later lands reads as news rather than as history.
const questionBumpSeen = "0"

// questionBumpDecision is the watcher's pure core: given the job's current
// question rows and the stored watermark, decide whether to bump, for which
// qids, and what the new watermark is. Mirrors watchDecision's seed
// discipline with one correction the PR watcher never needed: first sight
// is the first sight of the ROWS, not of the first answer. watchDecision
// consumes its firstSight flag off a field every open PR always populates
// (the head SHA); questions have no such field — a lazy seed that waited
// for the first answered_at would sit unconsumed while zeus parks, then
// swallow the job's first answer batch as "history" (measured against the
// live store: every job that ever had an answer would lose its first, and
// for the ~quarter of jobs whose answers all land in one board sitting
// that is every bump the job would ever get). So an empty watermark with
// NO answers seeds the questionBumpSeen sentinel — everything answered
// later is news — and an empty watermark WITH answers seeds to the newest
// answered_at silently, which is the true deploy/kv-wipe case where
// replaying history into every active cell would be a flood wearing a
// helpful shape. After either seed, any answered_at strictly newer than
// the watermark bumps. answered_at is an RFC3339-shaped string everywhere
// it is written, so lexicographic comparison orders it exactly as the PR
// watcher's LastCommentAt comparison does.
func questionBumpDecision(qs []store.DocQuestion, watermark string) (bump bool, qids []string, newWatermark string) {
	newWatermark = watermark
	maxAnswered := ""
	for _, q := range qs {
		if q.AnsweredAt == "" {
			continue
		}
		if q.AnsweredAt > maxAnswered {
			maxAnswered = q.AnsweredAt
		}
		if watermark != "" && q.AnsweredAt > watermark {
			qids = append(qids, q.QID)
		}
	}
	if watermark == "" {
		if maxAnswered == "" {
			return false, nil, questionBumpSeen // first sight of the rows: arm, silently
		}
		return false, nil, maxAnswered // first sight with history: seed silently
	}
	if len(qids) == 0 {
		return false, nil, newWatermark
	}
	sortQIDs(qids)
	return true, qids, maxAnswered
}

// sortQIDs orders Q<digits> ids numerically — Q2 before Q10 — falling back
// to lexicographic for anything that does not parse (the store validates
// the shape on every write, so the fallback is belt only).
func sortQIDs(qids []string) {
	sort.Slice(qids, func(i, j int) bool {
		a, errA := strconv.Atoi(strings.TrimPrefix(qids[i], "Q"))
		b, errB := strconv.Atoi(strings.TrimPrefix(qids[j], "Q"))
		if errA != nil || errB != nil {
			return qids[i] < qids[j]
		}
		return a < b
	})
}

// watchQuestions is the per-job step, modelled on watchPRs: the same
// working/blocked + live-cell gates, the same send-then-persist discipline
// (the watermark only advances after a confirmed SendToPeer, so a failed
// send retries the same bump next pass), and the same one-line dedup on
// failures so a parked cell doesn't write four identical error lines a
// minute overnight. The j.Err gate has its own reason (prTrackable's is a
// job-file rewrite hazard this pass, which writes only kv, does not
// share): deriveState pins an unparseable job to draft, so the
// working/blocked check below would be judging a state that is not the
// job's own.
func (d *daemonState) watchQuestions(j *Job, all []*core.Peer, live map[string]bool) {
	if j == nil || j.Err != "" || j.ID == 0 {
		return
	}
	if j.State != "working" && j.State != "blocked" {
		return
	}
	if !live[CellSession(j)] {
		return
	}
	// A cell mid-rescue is skipped: its zeus may be latched (turns refused
	// locally) or about to be killed and respawned, and tmux send-keys
	// reports success against a live pane regardless of whether the claude
	// behind it will ever process the text — a bump delivered into that
	// window would advance the watermark over an answer nobody received.
	// Skipping keeps the bump pending until the respawned zeus is back.
	// The skip is bounded twice, because "unresolved" alone is a predicate
	// that can be TRUE FOREVER: the rescue only ever acts on rate_limit
	// hits (any other error type is recorded and never resolved), and a
	// hit on a cell torn down mid-rescue stays unresolved permanently —
	// the live store already holds such rows on done jobs, and a
	// reactivation of one would otherwise re-enter this skip on every
	// tick, silently disabling bumps for that job for the rest of its
	// life. So only a hit the rescue could still be working — rate_limit,
	// younger than the rescue's longest park (limitResetMaxWait) — holds bumps back; anything
	// older or oddly-typed is not an in-flight rescue, and the bump
	// proceeds. (hit.At is unix SECONDS — limithook.go stamps it with now.Unix().)
	if hit := ReadLimitHit(j); hit.At != 0 && hit.HandledAt == 0 &&
		hit.Error == "rate_limit" &&
		time.Since(time.Unix(hit.At, 0)) < limitResetMaxWait {
		return
	}
	st := ActiveStore()
	if st == nil {
		return
	}
	qs, err := st.ListQuestions(j.ID)
	if err != nil {
		if key := "qlistfail:" + j.Slug; d.Notified[key] == 0 {
			dlogErr("%s: question-watch list failed: %v", j.Slug, err)
			d.Notified[key] = time.Now().Unix()
		}
		return // a failed read is not "no questions" — retry next pass
	}
	delete(d.Notified, "qlistfail:"+j.Slug)
	if len(qs) == 0 {
		return
	}
	watermark, err := st.GetKV(questionBumpKey(j.ID))
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		// A read failure is NOT first sight: "" would send the seed branch
		// a fake empty watermark and the seed WRITES, silently skipping
		// every answer since the last good pass. Only a genuinely absent
		// row may seed; anything else leaves the whole pass for the retry.
		if key := "qwmfail:" + j.Slug; d.Notified[key] == 0 {
			dlogErr("%s: question-watch watermark read failed: %v", j.Slug, err)
			d.Notified[key] = time.Now().Unix()
		}
		return
	}
	delete(d.Notified, "qwmfail:"+j.Slug)
	bump, qids, newWatermark := questionBumpDecision(qs, watermark)
	if newWatermark == watermark {
		return // nothing changed, not even a first-sight seed
	}
	if !bump {
		// seed-only: persist immediately, no cell message involved
		if err := st.SetKV(questionBumpKey(j.ID), newWatermark); err != nil {
			dlogErr("%s: question-watch seed write failed: %v", j.Slug, err)
		}
		return
	}
	// Send dedup across the send/persist seam: if the send landed but the
	// kv write failed, the next pass recomputes the identical bump — the
	// qsent marker (keyed on the content, cleared on a successful persist)
	// is what stops it re-typing the same notice into the pane every 15s
	// while only the persist retries. In-memory only, so a daemon restart
	// re-sends once; that is the Notified map's standing contract.
	sentKey := fmt.Sprintf("qsent:%d", j.ID) // id-keyed like the watermark, for the same slug-reuse reason
	mark := contentMark(newWatermark + "|" + strings.Join(qids, ","))
	if d.Notified[sentKey] != mark {
		zeus := CellZeus(j, all)
		if zeus == nil {
			return // no live zeus peer to bump — watermark stays put, retry next pass
		}
		// The message names the qids and the read-back verb, and closes
		// with the gate clause: an answer arriving at a parked cell is
		// exactly what the cell was waiting on, but it is NOT carl's gate
		// reply — the /do gate and the manual-test gate still pass only on
		// carl's own words.
		msg := fmt.Sprintf(
			"%s carl answered %s on the board — run `kraken questions get %s` to read the answer(s) and fold them into the work (an answer supersedes your default; /do step 2's staleness gates apply). This notice is not a gate reply and not a pane steer: if you are parked at a gate, fold the answer and park again",
			questionWatchPrefix, strings.Join(qids, ", "), j.Slug)
		if _, err := core.SendToPeer(zeus, msg); err != nil {
			if key := "qbumpfail:" + j.Slug; d.Notified[key] == 0 {
				dlogErr("%s: question-bump send failed: %v", j.Slug, err)
				d.Notified[key] = time.Now().Unix()
			}
			return // watermark stays put — retried next pass
		}
		delete(d.Notified, "qbumpfail:"+j.Slug)
		d.Notified[sentKey] = mark
		dlogEvent("info", "question-bump", "%s: bumped zeus for %s", j.Slug, strings.Join(qids, ", "))
	}
	if err := st.SetKV(questionBumpKey(j.ID), newWatermark); err != nil {
		if key := "qpersistfail:" + j.Slug; d.Notified[key] == 0 {
			dlogErr("%s: question-watch watermark write failed: %v", j.Slug, err)
			d.Notified[key] = time.Now().Unix()
		}
		return // qsent marker holds the re-send back; only this write retries
	}
	delete(d.Notified, "qpersistfail:"+j.Slug)
	delete(d.Notified, sentKey)
}

// contentMark hashes a bump's content into the Notified map's int64 slot,
// so "same bump already sent" is one map probe. The |1 keeps it off 0,
// which is the map's absent-key value everywhere else in the daemon.
func contentMark(s string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return int64(h.Sum64()) | 1
}
