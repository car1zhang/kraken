package jobs

import (
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/url"
	"path"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"kraken/internal/core"
	"kraken/internal/store"
	"kraken/internal/util"
)

// linear.go — the Linear intake pass: poll Linear for issues assigned to
// carl, turn each newly-assigned one into a DRAFT job carrying the whole
// ticket (description verbatim, comments, media downloaded locally), and keep
// an existing one in step afterwards — later edits and comments either bump
// the cell's zeus or land in the job body, depending on whether the job has
// started.
//
// Shaped as a self-paced pass rather than after kickDream: a single-flighted
// goroutine, and "a free function owns its own throttle so the daemon.go edit
// is one line". What it borrows from dream.go instead is
// the WATERMARK DISCIPLINE, and there are two watermarks doing two different
// jobs:
//
//   - the kv cursor (linear_sync) is the fetch window's floor — how far
//     Linear's own updatedAt has been swept;
//   - each linear_issues row's last_event_at is what has actually been
//     DELIVERED for that issue.
//
// The second is what makes the first's replayed overlap margin free: a poll
// re-fetching the last minute of activity re-derives events it has already
// stamped and does nothing with them. And it is why nothing here ever stamps
// on anything short of a confirmed delivery (the watchPRs rule) — an event
// counted as delivered because a send errored is an event nobody ever sees.

// linearSyncKVKey is the kv row holding the cursor, following dream_last_run
// and update_avail exactly: a small JSON singleton that doesn't warrant a
// table of its own, written by the daemon, which is alive exactly when it
// writes and already holds the store's single writer handle.
const linearSyncKVKey = "linear_sync"

// LinearSync is the pass' watermark: how far Linear's updatedAt has been
// swept, and when the last poll was attempted.
//
// The two are deliberately separate and advance under DIFFERENT rules.
// LastPollMS advances on every poll, success or failure, so a failing poll
// retries at the next INTERVAL rather than on the next 15-second tick.
// CursorMS advances only when the whole poll succeeded — a window nobody
// could read must not be marked swept, since the cursor never moves
// backwards (the dream-watermark rule).
type LinearSync struct {
	CursorMS   int64 `json:"cursor_ms"`
	LastPollMS int64 `json:"last_poll_ms"`
}

// linearPollInterval paces the poll. Linear's rate budget for a personal key
// is 2,500 requests/hour and one poll costs two GraphQL calls plus
// pagination, so two minutes is ~1% of it — chosen for latency (a ticket
// assigned in Linear should be on the board within a few minutes), not to
// stay inside a budget that was never close.
const linearPollInterval = 2 * time.Minute

// linearOverlap is how far BEFORE the cursor each fetch actually queries.
// Linear's clock and this machine's are not the same clock, and an issue
// written in the same millisecond the cursor lands on would otherwise fall
// through the gap between two polls. The replayed margin is free because it
// is the per-issue last_event_at stamp, not the cursor, that decides whether
// an event has been delivered — so a re-fetched event is re-derived and
// discarded rather than re-delivered.
const linearOverlap = 60 * time.Second

// linearCommentCap truncates a comment body inside an update bump. The full
// text is one click away in Linear (every message carries the issue URL), and
// a bump is typed into a tmux pane.
const linearCommentCap = 400

// linearPollRunning single-flights the poll across ticks, the same shape
// usagePassRunning and mmCodeRunning use: the poll does network I/O,
// creates job rows and writes files, and two of those overlapping would race
// to create the same slug.
var linearPollRunning atomic.Bool

// lastLinearPoll is the in-memory backstop for the stored throttle: the
// millis at which THIS process last started a poll, regardless of whether
// the write of LastPollMS ever landed. Deliberately the SECOND gate (the
// lastUpdateAvailCheck ordering): it is lost on restart while the stored
// stamp survives one, so the store stays the source of truth and this only
// catches what the store cannot tell us — a kv write that silently never
// lands would otherwise turn a two-minute poll into a Linear round trip on
// every 15-second tick.
var lastLinearPoll atomic.Int64

// readLinearSync loads the cursor, with the distinction the throttle needs:
// ok=false means the watermark could not be READ at all (no store, a failed
// query), as opposed to a genuinely absent key, which is ok=true with a zero
// value — the honest "never polled" that must fire a first poll.
//
// That inversion is readDreamRun's, and for readDreamRun's reason: an
// unreadable row degrading to the zero value would read as "never polled",
// which is always due, so a broken store would poll Linear every 15 seconds
// forever. A missed poll costs two minutes; a poll storm costs the rate
// budget and carl's notification tray.
//
// A present-but-unparseable row is neither: it is garbage, and treating it as
// unreadable would wedge the pass permanently and in silence, since the only
// writer of this key sits downstream of the gate. It resets loudly instead
// (R2) and behaves as a first poll — which, per R1, seeds the cursor at now
// and fetches nothing, so the reset costs at most the events of the window it
// forgot.
func readLinearSync(st *store.Store) (LinearSync, bool) {
	if st == nil {
		return LinearSync{}, false
	}
	raw, err := st.GetKV(linearSyncKVKey)
	if errors.Is(err, store.ErrNotFound) {
		return LinearSync{}, true // never polled — genuinely due
	}
	if err != nil {
		return LinearSync{}, false
	}
	var s LinearSync
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		dlogErr("linear sync watermark unparseable (%q) — resetting it; this poll seeds the cursor at now and fetches nothing", raw)
		return LinearSync{}, true
	}
	return s, true
}

// writeLinearSync persists the cursor. No store wired is a no-op rather than
// an error, matching writeDreamRun — but note that losing this write is not
// free: lastLinearPoll above is what keeps a store that never records from
// re-polling forever.
func writeLinearSync(st *store.Store, s LinearSync) error {
	if st == nil {
		return nil
	}
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return st.SetKV(linearSyncKVKey, string(data))
}

// linearSource is the seam runLinearPoll is tested through: exactly the three
// client calls the poll makes, satisfied by *core.LinearClient. The poll
// itself does no network I/O of its own, so a test drives a whole intake —
// creation, media, updates — with a fake that never opens a socket.
type linearSource interface {
	AssignedIssuesSince(time.Time) ([]core.LinearIssue, error)
	AssignedCommentsSince(time.Time) ([]core.LinearComment, error)
	DownloadFile(string) ([]byte, string, error)
}

// LinearPass is the tick's entry point: decide that a poll is due, then run
// one in a fire-and-forget goroutine.
//
// The gates are ordered most-decisive first, which is not the same as
// cheapest first and is deliberately not it: the config read that answers
// "is this feature even on" opens a file, and it still precedes the two
// atomic loads, because a machine with no linear.env must reach a decision
// rather than a throttle. No store or no configured key returns SILENTLY —
// an absent ~/.kraken/linear.env means the feature is simply not configured,
// which is core.ErrNoSlack's posture and must not log an error every
// fifteen seconds on a machine that never wanted the feature.
func LinearPass(now time.Time) {
	st := ActiveStore()
	if st == nil {
		return
	}
	cfg := core.LoadLinearConfig()
	if cfg.APIKey == "" {
		return // feature off — not an error, and never logged as one
	}
	ms := now.UnixMilli()
	sync, ok := readLinearSync(st)
	if !ok {
		return // unreadable watermark degrades to "not due" — see readLinearSync
	}
	if sync.LastPollMS != 0 && ms-sync.LastPollMS < linearPollInterval.Milliseconds() {
		return
	}
	if last := lastLinearPoll.Load(); last != 0 && ms-last < linearPollInterval.Milliseconds() {
		return
	}
	// The client is built before the CAS so the goroutine below closes over a
	// finished value rather than doing work inside the single-flight window.
	src := core.NewLinearClient(cfg)
	// The seam is captured into a local BEFORE the CAS, the same reason
	// kickDream captures dreamPass and kickUpdateAvail captures
	// checkUpdateAvail: a test that swaps it back the moment this returns must
	// not race the goroutine's own read of it.
	poll := linearPoll
	if !linearPollRunning.CompareAndSwap(false, true) {
		return
	}
	lastLinearPoll.Store(ms)
	go func() {
		defer linearPollRunning.Store(false)
		// The error is already dlogged inside runLinearPoll (R11's
		// linear-sync-error event), which is where the detail is; logging it
		// again here would double every failure in the event log.
		_ = poll(st, src, cfg, now)
	}()
}

// linearPoll is the seam LinearPass runs the work through, so a test can drive
// the CADENCE — the two-gate throttle, the degrade-to-not-due inversion, the
// single-flight — without a network, a store cursor, or a fake client. The
// dreamPass shape exactly.
var linearPoll = runLinearPoll

// linearNotify is the seam every notification in this pass fires through:
// exactly one per creation (R4) and one per skip (R6), and nothing else. A
// package var so a test can count them without an osascript popping on carl's
// screen for a fixture issue.
var linearNotify = notify

// runLinearPoll is one poll: read the cursor, fetch what changed, and give
// every issue exactly one of four outcomes — created, skipped, updated, or
// left alone.
//
// The cursor advance at the bottom is the whole of R11 and it is stricter
// than "did the fetches work". It advances only when every issue this poll
// touched was fully settled, where settled means its events were either
// delivered AND stamped or provably need no delivery. An event that was
// derived but not stamped — a bump that could not reach a live zeus, a body
// write that lost a rev race — MUST hold the cursor, because the cursor is
// the only thing that would ever bring that event back: the next poll's
// window starts at the cursor, so advancing past an undelivered event loses
// it permanently, and every one of R8/R9/R10 promises a retry instead.
func runLinearPoll(st *store.Store, src linearSource, cfg core.LinearConfig, now time.Time) error {
	nowMS := now.UnixMilli()
	sync, ok := readLinearSync(st)
	if !ok {
		// Seeding a cursor we could not read would either lose a real window
		// (the row exists and holds a floor we just can't see) or invent one.
		// Write nothing at all and let the next interval retry.
		return linearFail(errors.New("sync watermark unreadable — cursor left alone"))
	}
	if sync.CursorMS <= 0 {
		// R1: first poll ever. Seed at now and fetch nothing — Q6's no-backfill
		// call. Every issue already assigned to carl predates the cursor, so
		// enabling the feature must not flood the board with the whole backlog.
		//
		// <= rather than == 0, runDreamPass' guard for runDreamPass' reason: a
		// corrupt or hand-edited NEGATIVE cursor would otherwise be treated as
		// a real floor and fetch every issue touched since 1969 — the exact
		// backlog flood the seed exists to prevent.
		if err := writeLinearSync(st, LinearSync{CursorMS: nowMS, LastPollMS: nowMS}); err != nil {
			return linearFail(fmt.Errorf("seed cursor: %w", err))
		}
		dlogEvent("info", "linear-seed", "linear: cursor seeded at %s — no backfill", now.Format(time.RFC3339))
		return nil
	}

	since := time.UnixMilli(sync.CursorMS).Add(-linearOverlap)
	// Both fetches are attempted every poll, never chained: they answer
	// different questions (a comment does not reliably bump its parent
	// issue's updatedAt), so one failing must not silently retire the other.
	issues, issuesErr := src.AssignedIssuesSince(since)
	comments, commentsErr := src.AssignedCommentsSince(since)

	var errs []error
	if issuesErr != nil {
		errs = append(errs, fmt.Errorf("fetch issues: %w", issuesErr))
	}
	if commentsErr != nil {
		errs = append(errs, fmt.Errorf("fetch comments: %w", commentsErr))
	}

	// One pass over each issue id, in fetch order: an issue that came back
	// from BOTH queries is processed once, so its comments and its own edit
	// fold into a single bump rather than two.
	byID := map[string]core.LinearIssue{}
	commentsByIssue := map[string][]core.LinearComment{}
	seen := map[string]bool{}
	var order []string
	add := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			order = append(order, id)
		}
	}
	maxSeen := int64(0)
	bump := func(t time.Time) {
		if ms := t.UnixMilli(); ms > maxSeen {
			maxSeen = ms
		}
	}
	for _, iss := range issues {
		byID[iss.ID] = iss
		add(iss.ID)
		bump(iss.UpdatedAt)
	}
	for _, c := range comments {
		commentsByIssue[c.IssueID] = append(commentsByIssue[c.IssueID], c)
		add(c.IssueID)
		bump(c.UpdatedAt)
	}

	var holds []string
	for _, id := range order {
		iss, haveIssue := byID[id]
		row, err := st.GetLinearIssue(id)
		switch {
		case errors.Is(err, store.ErrNotFound):
			if !haveIssue {
				// A comment on an issue kraken has no row for. There is nothing
				// to create from a comment (no title, no state, no description),
				// and R5 already says the issue itself will be caught when it
				// enters a matching state. Nothing to do, and nothing held.
				continue
			}
			if !linearStateMatches(iss.StateName, cfg.States) {
				// R5: no row and no job. Deliberately no row — a row here would
				// permanently eat the issue, and entering a matching state later
				// bumps its updatedAt, so the cursor catches it on its own.
				continue
			}
			if err := linearCreateJob(st, src, cfg, iss, now); err != nil {
				errs = append(errs, fmt.Errorf("issue %s: %w", iss.Identifier, err))
			}
		case err != nil:
			errs = append(errs, fmt.Errorf("issue %s: read row: %w", id, err))
		default:
			hold, err := linearApplyUpdate(st, src, row, iss, haveIssue, commentsByIssue[id], now)
			if err != nil {
				errs = append(errs, fmt.Errorf("issue %s: %w", row.Identifier, err))
			}
			if hold != "" {
				holds = append(holds, hold)
			}
		}
	}
	// A15: a hold is silent by design — it is not a failure — but an OLD one
	// is a different thing. A working or blocked job whose cell is gone is
	// never reached by auto-archive, so its hold lasts until carl acts, and
	// nothing anywhere said so. One line an hour, naming the issue and the
	// job, is what makes that state findable without turning the ordinary
	// case (a hold that resolves on the next poll) into log noise.
	if len(holds) > 0 {
		linearWarnHold(now, time.UnixMilli(sync.CursorMS), holds)
	}

	next := sync.CursorMS
	if len(errs) == 0 && len(holds) == 0 && maxSeen > next {
		// Never now(): Linear's clock is not this machine's, and stamping a
		// local now as a Linear updatedAt floor skips everything written in
		// the skew. Never backwards either — the overlap deliberately re-reads
		// events older than the cursor.
		next = maxSeen
	}
	if err := writeLinearSync(st, LinearSync{CursorMS: next, LastPollMS: nowMS}); err != nil {
		errs = append(errs, fmt.Errorf("write cursor: %w", err))
	}
	if len(errs) > 0 {
		return linearFail(errors.Join(errs...))
	}
	return nil
}

// linearHoldWarnAfter is both halves of A15's gate: how old the held window
// must be before it is worth a line, and how often that line may repeat. One
// hour on both counts because the poll runs every two minutes — a hold that
// clears on the next poll (a cell coming up, a zeus finishing its restart) is
// the ORDINARY case and must stay silent, while one that outlives the hour is
// something only carl can clear.
const linearHoldWarnAfter = time.Hour

// linearLastHoldWarn is the in-memory throttle for that line. In-memory
// rather than a kv row for the reason every dedup map in daemon.go is: losing
// it on a restart costs one extra line, and a warning is not state.
var linearLastHoldWarn atomic.Int64

// linearWarnHold emits at most one held-cursor line per linearHoldWarnAfter,
// and only once the held window itself has aged past the same bound.
func linearWarnHold(now, cursor time.Time, holds []string) {
	if now.Sub(cursor) <= linearHoldWarnAfter {
		return
	}
	ms := now.UnixMilli()
	last := linearLastHoldWarn.Load()
	if last != 0 && ms-last < linearHoldWarnAfter.Milliseconds() {
		return
	}
	// CAS rather than a plain Store so two polls that somehow overlap cannot
	// both pass the check above and both log.
	if !linearLastHoldWarn.CompareAndSwap(last, ms) {
		return
	}
	dlog("linear cursor held since %s by %d issue(s): %s",
		cursor.UTC().Format(time.RFC3339), len(holds),
		core.TruncateOneLine(strings.Join(holds, "; "), 300))
}

// linearFail is R11's one error surface: every failed poll says so once, with
// the same event id, and returns the error for the caller. A held issue is
// NOT an error and never comes through here — holding is the design working,
// not failing.
func linearFail(err error) error {
	dlogEvent("error", "linear-sync-error", "linear poll: %v", err)
	return err
}

// linearStateMatches reports whether an issue's workflow state name is one
// the config asks for, case-insensitively (Q3: the filter is on the NAME, a
// per-team renamable string, which is exactly why it is a config knob and why
// the comparison cannot be exact).
func linearStateMatches(state string, want []string) bool {
	state = strings.TrimSpace(state)
	for _, w := range want {
		if strings.EqualFold(strings.TrimSpace(w), state) {
			return true
		}
	}
	return false
}

// linearCreateJob is R4: one new issue becomes one draft job.
//
// The order (A14) is load-bearing and it is deliberately NOT "everything that
// can fail, then the row". The job row is created first because the uploads
// dir is named <slug>-<id> and that id does not exist until the row does. But
// the linear_issues row is written IMMEDIATELY after it, before a single byte
// of media is fetched, which is the narrowest that window can be made:
// everything the dedup row needs — the issue and its creation-time comments —
// is already in hand and needs no network.
//
// The two things a crash in that window can lose are not comparable. A
// missing media line is one HTTP GET from being recovered, and the ticket
// still says where the image lives. A missing dedup row SEVERS the ticket
// from its job permanently: the next poll finds the slug taken, and without
// the adopt path below would record a skip against a job that is really this
// issue's, which no later poll ever reconsiders. Writing the row last put a
// whole media download inside that window.
func linearCreateJob(st *store.Store, src linearSource, cfg core.LinearConfig, iss core.LinearIssue, now time.Time) error {
	slug := linearSlug(iss.Identifier, iss.Title)
	if slug == "" {
		return linearSkip(st, iss, now, "identifier and title produce an empty slug")
	}
	// R6/A14: the slug is already held. Checked before the insert rather than
	// only catching its error, so the common case reads as a decision rather
	// than as a recovered failure.
	//
	// Two very different situations reach here, and telling them apart is what
	// A14 added. If the colliding job's body cites THIS issue's Source URL, it
	// is not a collision at all — it is this ticket's own job, created by an
	// earlier poll that died before its dedup row landed. Adopt it: write the
	// row pointing at that job and stop. Anything else is a genuine collision
	// with unrelated work and records the skip, never retried and never
	// silently suffixed into a second job for the same ticket.
	if existing, err := st.GetJobBySlug(slug); err == nil && existing != nil {
		if linearBodyCitesIssue(existing.Body, iss.URL) {
			if err := st.PutLinearIssue(&store.LinearIssueRow{
				IssueID: iss.ID, Identifier: iss.Identifier, JobID: existing.ID,
				CreatedAt: now.UnixMilli(), LastEventAt: linearCreationEvent(iss),
			}); err != nil {
				return fmt.Errorf("adopt job %s: %w", slug, err)
			}
			dlogEvent("info", "linear-job-adopted", "linear %s: adopted job %s — it cites this issue, so an earlier poll created it and died before its row landed", iss.Identifier, slug)
			return nil
		}
		return linearSkip(st, iss, now, "slug "+slug+" is taken by an active job")
	}

	text := composeLinearBody(iss, cfg, nil)
	parsed := ParseJob(slug+JobSuffix, []byte(text))
	if parsed.Err != "" {
		// A parse failure is a bug in composeLinearBody, not in the ticket:
		// every byte of user content lands BELOW the closing fence, so no
		// title or description can reach the frontmatter. Loud, and no row —
		// so a fix makes the issue eligible again.
		return fmt.Errorf("composed job does not parse: %s", parsed.Err)
	}
	if _, err := st.CreateJob(&store.Job{
		Slug: slug, Repo: parsed.Repo, Base: parsed.Base,
		Solo: parsed.Solo, Model: parsed.Model,
		Body: parsed.Body,
	}); err != nil {
		if linearSlugTaken(err) {
			return linearSkip(st, iss, now, "slug "+slug+" is taken by an active job")
		}
		return err
	}
	sr, err := st.GetJobBySlug(slug)
	if err != nil {
		return fmt.Errorf("job %s created but could not be reloaded: %w", slug, err)
	}
	// The dedup row, before anything that touches the network. Its stamp
	// covers the issue AND its creation-time comments, both of which the body
	// already carries — without that, the very comments just rendered into the
	// job would come back on the next poll as an update.
	if err := st.PutLinearIssue(&store.LinearIssueRow{
		IssueID: iss.ID, Identifier: iss.Identifier, JobID: sr.ID,
		CreatedAt: now.UnixMilli(), LastEventAt: linearCreationEvent(iss),
	}); err != nil {
		return fmt.Errorf("record issue row: %w", err)
	}

	// Media is best-effort by contract: the ticket TEXT is the payload, and a
	// download that fails degrades to the original URL in the body (see
	// linearMediaSection) rather than failing a creation that already
	// succeeded.
	if media := linearFetchMedia(st, src, slug, linearMediaURLs(iss)); len(media) > 0 {
		full := ParseJob(slug+JobSuffix, []byte(composeLinearBody(iss, cfg, media)))
		switch {
		case full.Err != "":
			dlogErr("linear %s: media patch does not parse (%s) — job kept without media lines", iss.Identifier, full.Err)
		default:
			// The rev is re-read HERE rather than reused from sr above, and the
			// difference is a whole download's worth of wall-clock: sr's rev was
			// read before the network, so any write in between — the board's own
			// editor, a second pass — made the patch a guaranteed ErrStaleRev
			// and silently cost the job its media lines. One extra read closes
			// that; a stale rev at this range is logged and dropped, since the
			// ticket still names every attachment.
			fresh, err := st.GetJobBySlug(slug)
			if err != nil {
				dlogErr("linear %s: media patch skipped — job %s could not be re-read: %v", iss.Identifier, slug, err)
			} else if _, err := st.UpdateJobFields(fresh.ID, fresh.Rev, map[string]any{"body": full.Body}); err != nil {
				dlogErr("linear %s: media patch failed: %v", iss.Identifier, err)
			}
		}
	}

	linearNotify("kraken ⧉ "+iss.Identifier, iss.Title)
	dlogEvent("info", "linear-job-created", "linear %s: created draft job %s (%s)", iss.Identifier, slug, iss.URL)
	return nil
}

// linearCreationEvent is the last_event_at a freshly created job's row
// carries: the newest of the issue's own updatedAt and the comments the body
// just rendered. Its own function because the adopt path and the create path
// must agree on it exactly — a lower stamp in either would re-deliver the
// creation snapshot as an update.
func linearCreationEvent(iss core.LinearIssue) int64 {
	last := iss.UpdatedAt.UnixMilli()
	for _, c := range iss.Comments {
		if ms := c.UpdatedAt.UnixMilli(); ms > last {
			last = ms
		}
	}
	return last
}

// linearBodyCitesIssue reports whether a job body is one composeLinearBody
// wrote for THIS issue, by looking for the Source line it always emits. It is
// the whole evidence the adopt path acts on, so it is deliberately narrow: an
// issue with no URL cites nothing and can never be adopted, and the match is
// on the full "Source: <url>" line rather than the bare URL, which a human
// could plausibly have pasted into an unrelated job's spec.
func linearBodyCitesIssue(body, issueURL string) bool {
	if issueURL == "" {
		return false
	}
	return strings.Contains(body, "Source: "+issueURL)
}

// linearSkip records an issue that will never become a job, with the reason,
// and says so once. The row is what makes it never retry — the whole point of
// R6 is that a collision is a decision taken once, not a failure re-attempted
// every two minutes forever.
func linearSkip(st *store.Store, iss core.LinearIssue, now time.Time, reason string) error {
	if err := st.PutLinearIssue(&store.LinearIssueRow{
		IssueID: iss.ID, Identifier: iss.Identifier, JobID: 0,
		SkipReason: reason, CreatedAt: now.UnixMilli(),
	}); err != nil {
		return fmt.Errorf("record skipped issue: %w", err)
	}
	linearNotify("kraken ⚠ "+iss.Identifier, "no job created — "+reason)
	dlogEvent("info", "linear-job-skipped", "linear %s: %s", iss.Identifier, reason)
	return nil
}

// linearSlugTaken reports whether a CreateJob error is the active-slug unique
// index refusing a duplicate. store.CreateJob does not map that constraint
// onto ErrSlugTaken the way UpdateJobFields/UnarchiveJob do, so the sentinel
// check alone would miss it — hence the text fallback, which only ever runs
// for the race the pre-check in linearCreateJob cannot close (an unrelated
// job claiming the slug between the lookup and the insert).
func linearSlugTaken(err error) bool {
	return errors.Is(err, store.ErrSlugTaken) || strings.Contains(err.Error(), "UNIQUE constraint")
}

// linearApplyUpdate is R7-R10: everything that happens to an issue kraken has
// already seen.
//
// A non-empty hold means this issue's events were derived but NOT stamped,
// which is what holds the poll's cursor (see runLinearPoll); the string
// itself is the one-line reason A15's warning renders, so the return carries
// both the fact and its explanation rather than a bare bool the caller would
// have to re-derive a message from. It is not an error — a cell that is not
// up yet, or a zeus mid-restart, is an ordinary state, and the only correct
// reaction is to leave both watermarks where they are and try again in two
// minutes.
func linearApplyUpdate(st *store.Store, src linearSource, row *store.LinearIssueRow, iss core.LinearIssue, haveIssue bool, comments []core.LinearComment, now time.Time) (hold string, err error) {
	if row.JobID <= 0 {
		return "", nil // a skipped issue (R6) never becomes a job later
	}
	// Strictly newer than the stamp: an event AT the stamp is one already
	// delivered, and the overlap margin re-fetches those by design.
	issueChanged := haveIssue && iss.UpdatedAt.UnixMilli() > row.LastEventAt
	var fresh []core.LinearComment
	newest := row.LastEventAt
	if issueChanged {
		newest = iss.UpdatedAt.UnixMilli()
	}
	for _, c := range comments {
		ms := c.UpdatedAt.UnixMilli()
		if ms <= row.LastEventAt {
			continue
		}
		fresh = append(fresh, c)
		if ms > newest {
			newest = ms
		}
	}
	if !issueChanged && len(fresh) == 0 {
		return "", nil // R7: nothing qualifying, nothing happens
	}

	sr, err := st.GetJobByID(row.JobID)
	switch {
	case errors.Is(err, store.ErrNotFound), err == nil && sr.Archived():
		// R7: the job is gone or archived. Stamp and do nothing else — an
		// archived job is one carl has finished with, and resurrecting it
		// from a Linear comment is a decision that belongs to him, not to a
		// poll. Stamping is what stops the same dead event being re-derived
		// every two minutes forever.
		if err := st.SetLinearIssueEvent(row.IssueID, newest); err != nil {
			return "", fmt.Errorf("stamp event for a gone job: %w", err)
		}
		return "", nil
	case err != nil:
		return "", fmt.Errorf("read job %d: %w", row.JobID, err)
	}
	j, err := loadJobFull(st, sr)
	if err != nil {
		return "", fmt.Errorf("load job %d: %w", row.JobID, err)
	}

	identifier := row.Identifier
	if identifier == "" && haveIssue {
		identifier = iss.Identifier
	}
	issueURL := ""
	if haveIssue {
		issueURL = iss.URL
	}
	if j.Started() && !linearCellLive(j) {
		// R10: hold. No bump, no body write, no stamp — and the deliver seam
		// is not reached at all. It is checked HERE, above the download, so a
		// held issue costs nothing per poll: fetching media first would mint a
		// fresh uploads row and a fresh img-NNN file for the same screenshot
		// every two minutes for as long as the cell stays down.
		return identifier + " → job " + j.Slug + " (" + j.State + ", cell not deliverable)", nil
	}
	// Media in an update is mirrored exactly as it is at creation: the local
	// path is what an agent can actually open, and the description is only
	// scanned when the description is what changed.
	var urls []string
	if issueChanged {
		urls = append(urls, linearUploadURLs(iss.Description)...)
	}
	for _, c := range fresh {
		urls = append(urls, linearUploadURLs(c.Body)...)
	}
	media := linearFetchMedia(st, src, j.Slug, linearDedupe(urls))
	text := linearUpdateText(identifier, issueURL, issueChanged, fresh, media)

	if j.Started() {
		// R8: the cell is live (checked above), so the events go into zeus'
		// pane as ONE message. A false return — a send that errored, a zeus
		// discovery could not find — leaves the stamp exactly where it was,
		// so the next poll carries the same events again.
		if !linearDeliverBump(j, text) {
			return identifier + " → job " + j.Slug + " (bump not confirmed)", nil
		}
	} else {
		// R9: a job that has never started has no pane to bump, so the update goes
		// where the job's own spec is — APPENDED, never rewritten: the body
		// below is carl's to edit, and a poll that regenerated it would throw
		// away whatever he had typed since.
		// The stamp is the POLL's now in UTC, not the event's own time: one
		// section per delivery, and a reader comparing two sections is
		// comparing two deliveries.
		body := j.Body + "\n\n## Linear update (" + now.UTC().Format(time.RFC3339) + ")\n\n" + text + "\n"
		if _, err := st.UpdateJobFields(j.ID, j.Rev, map[string]any{"body": body}); err != nil {
			if errors.Is(err, store.ErrStaleRev) {
				// An editor saved between this poll's read and its write.
				// Leaving the stamp is the whole remedy: the next poll re-reads
				// the fresh rev and appends onto whatever carl just wrote.
				return identifier + " → job " + j.Slug + " (body write lost a rev race)", nil
			}
			return "", fmt.Errorf("append update to job %s: %w", j.Slug, err)
		}
	}
	if err := st.SetLinearIssueEvent(row.IssueID, newest); err != nil {
		// The delivery already happened, so this is the one failure that can
		// duplicate a message. Reported rather than swallowed, and the row
		// keeps its old stamp — a repeated bump is recoverable, a silently
		// lost stamp is not.
		return "", fmt.Errorf("stamp delivered event: %w", err)
	}
	delivered := len(fresh)
	if issueChanged {
		delivered++
	}
	dlogEvent("info", "linear-update", "linear %s: delivered %d event(s) to %s", identifier, delivered, j.Slug)
	return "", nil
}

// linearCellLive reports whether this job's cell can actually be DELIVERED to
// — the R8/R10 fork, and deliberately its OWN seam rather than an answer
// folded into linearDeliverBump. The two rows differ in what they are allowed
// to do, not only in their effect: R10 must not reach the deliverer at all,
// which is what makes "held" observable as something other than "the send
// failed", and what lets the caller skip the media download a hold would
// otherwise repeat every two minutes.
//
// A live SESSION is not the test (A13). A session with no discoverable zeus —
// a dead pane, or a side thread holding the session open on its own — passes
// a session check and then fails every send forever, and because the download
// sits above the send, each of those attempts re-fetched and re-materialized
// the same attachments. Requiring a zeus routes that cell to the hold, where
// the answer is free and the retry costs nothing. What is left is bounded: a
// transient send failure with a real zeus present still re-downloads once per
// retry.
//
// A package var for linearDeliverBump's reason: the default shells out to
// tmux, and nothing in this package's tests may.
var linearCellLive = func(j *Job) bool {
	if !LiveSessions()[CellSession(j)] {
		return false
	}
	all, _ := core.OrderAll(core.DiscoverAll())
	return CellZeus(j, all) != nil
}

// linearDeliverBump types one combined update into the cell's zeus, returning
// true only when the send was actually confirmed — which is what licenses the
// caller to stamp the event as delivered (the watchPRs discipline).
//
// A package var so tests swap it: the default reaches tmux through discovery,
// and nothing in this package's tests may.
//
// It no longer decides liveness (linearCellLive above does, before this is
// called at all). A nil zeus here is a live session whose zeus discovery
// could not find — mid-restart, or a pane that hasn't written its session
// file yet — and the answer is the same as a failed send: nothing typed,
// nothing stamped, retried on the next poll.
var linearDeliverBump = func(j *Job, msg string) bool {
	all, _ := core.OrderAll(core.DiscoverAll())
	zeus := CellZeus(j, all)
	if zeus == nil {
		return false
	}
	// SendToPeer's own queued flag distinguishes "typed while zeus was
	// mid-turn" from "typed while it was idle" — both are DELIVERED, so only
	// the error decides. Treating a busy zeus as undelivered would re-send
	// the same update every two minutes for as long as its turn lasts.
	if _, err := core.SendToPeer(zeus, msg); err != nil {
		return false
	}
	return true
}

// linearUpdateText renders one issue's new events — the same content whether
// it is typed into a pane (R8) or appended to a body (R9), so the two can
// never drift into saying different things about the same event.
func linearUpdateText(identifier, issueURL string, issueChanged bool, comments []core.LinearComment, media []linearMediaRef) string {
	var b strings.Builder
	b.WriteString("Linear " + identifier + " changed")
	if issueURL != "" {
		b.WriteString(" (" + issueURL + ")")
	}
	b.WriteString(":\n")
	if issueChanged {
		b.WriteString("- the issue itself was edited (title, description, state, labels or priority) — re-read it in Linear\n")
	}
	for _, c := range comments {
		author := c.Author
		if author == "" {
			author = "someone"
		}
		b.WriteString("- new comment from " + author + ": " + core.TruncateOneLine(c.Body, linearCommentCap) + "\n")
	}
	b.WriteString(linearMediaLines(media))
	return strings.TrimRight(b.String(), "\n")
}

// composeLinearBody renders the whole job file text for one issue: everything
// a cell needs to work the ticket without opening Linear.
//
// Every byte of user content — title, description, comment bodies — lands
// BELOW the closing fence, and the frontmatter carries only a slugified slug
// and a config-supplied repo. That is the entire reason an adversarial
// description (a bare "---" line, YAML metacharacters) round-trips ParseJob:
// nothing hostile can reach the part that is parsed.
//
// The description is written VERBATIM, with its own line structure intact —
// re-wrapping a ticket is how a code block or a table stops being one.
//
// cfg is passed in rather than re-read: runLinearPoll's own parameter is the
// single configuration source everywhere in the pass, so the body a poll
// composes can never disagree with the config that poll ran under — which a
// second LoadLinearConfig, reading a file that may have changed mid-poll,
// could.
func composeLinearBody(iss core.LinearIssue, cfg core.LinearConfig, media []linearMediaRef) string {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("job: " + linearSlug(iss.Identifier, iss.Title) + "\n")
	b.WriteString("repo: " + linearOneLine(cfg.DefaultRepo) + "\n")
	// No start-flag key of any kind: a ticket lands as a DRAFT for carl to
	// read and submit, never as work that starts itself the moment someone
	// assigns it — and since submitting IS starting now, a draft is that
	// property structurally rather than by a flag left switched off.
	b.WriteString("---\n\n")

	title := strings.TrimSpace(iss.Title)
	if title == "" {
		title = iss.Identifier
	}
	b.WriteString("# " + linearOneLine(title) + " (" + iss.Identifier + ")\n\n")
	if iss.URL != "" {
		b.WriteString("Source: " + iss.URL + "\n\n")
	}
	if meta := linearMetaLine(iss); meta != "" {
		b.WriteString(meta + "\n\n")
	}
	if iss.Description != "" {
		b.WriteString(iss.Description)
		if !strings.HasSuffix(iss.Description, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	if len(iss.Comments) > 0 {
		b.WriteString("## Comments\n\n")
		for _, c := range iss.Comments {
			author := c.Author
			if author == "" {
				author = "someone"
			}
			b.WriteString("### " + linearOneLine(author) + " — " + c.CreatedAt.Format(time.RFC3339) + "\n\n")
			b.WriteString(c.Body)
			if !strings.HasSuffix(c.Body, "\n") {
				b.WriteString("\n")
			}
			b.WriteString("\n")
		}
	}
	b.WriteString(linearMediaSection(media))
	return b.String()
}

// linearMetaLine is the one-line summary under the heading: team, state,
// priority, labels, project. Empty parts are omitted rather than rendered as
// gaps — most tickets carry two or three of the five.
func linearMetaLine(iss core.LinearIssue) string {
	var parts []string
	for _, p := range []string{
		iss.TeamKey,
		iss.StateName,
		linearPriorityLabel(iss.Priority),
		strings.Join(iss.Labels, ", "),
		iss.ProjectName,
	} {
		if s := strings.TrimSpace(p); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, " · ")
}

// linearPriorityLabel names Linear's priority scale. 0 is "no priority set"
// — a real and common value, and rendering it as a label would put a
// meaningless word on most tickets — so it and anything off the scale return
// empty and drop out of the metadata line entirely.
//
// The match is on the EXACT value, never on a truncation of it: the field
// arrives as a JSON number, so 1.5 and 2.0001 are reachable inputs, and
// int(p) would confidently render them as Urgent and High. Anything that is
// not one of the four scale points is "anything else" and has no label —
// inventing one for a value Linear never defined is worse than saying
// nothing.
func linearPriorityLabel(p float64) string {
	switch p {
	case 1:
		return "Urgent"
	case 2:
		return "High"
	case 3:
		return "Normal"
	case 4:
		return "Low"
	default:
		return ""
	}
}

// linearOneLine flattens a value that must not break the line it sits on.
// Only the repo genuinely reaches frontmatter (the rest is heading text), but
// a newline smuggled in through the process environment would split the
// fence, and a job whose frontmatter does not parse never runs.
func linearOneLine(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r", " "), "\n", " ")
	return strings.TrimSpace(s)
}

// linearSlug is the job slug for an issue: the identifier keeps it unique per
// ticket (ENG-142 is Linear's own unique key), the title makes it readable on
// the board. Truncation trims a dangling separator so a cut in the middle of
// a word does not leave "eng-142-fix-the-".
//
// BOTH halves go through util.Slugify, which for a real identifier is a no-op
// (ENG-142 lowercases to eng-142 either way) and is load-bearing for anything
// else: the slug has to be a FIXED POINT of Slugify, because UploadsDir
// re-slugifies it and returns "" on a slug that changes — so an identifier
// carrying a character Slugify drops would produce a job whose media could
// never be materialized, silently and only for that ticket.
func linearSlug(identifier, title string) string {
	s := util.Slugify(identifier) + "-" + util.Slugify(title)
	if r := []rune(s); len(r) > 48 {
		s = string(r[:48])
	}
	return strings.Trim(s, "-")
}

// linearMediaRef is one attachment's fate: where it came from, where it
// landed, and why it did not. Path empty means the bytes are not on disk —
// the body then keeps the original URL so the attachment is still reachable
// by hand.
type linearMediaRef struct {
	URL         string
	Path        string
	ContentType string
	Err         error
}

// linearUploadRe matches a Linear-hosted attachment URL inside markdown. The
// character class stops at whatever closes a markdown image or link, so
// "![shot](https://uploads.linear.app/a/b.png)" yields the URL without its
// bracket. The URLs arrive PRE-SIGNED (the client sends
// public-file-urls-expire-in on every request), which is what makes each one
// a plain authenticated-by-nothing GET.
var linearUploadRe = regexp.MustCompile(`https://uploads\.linear\.app/[^\s)\]}"'<>]+`)

// linearUploadURLs pulls every Linear attachment URL out of one markdown
// blob, in order.
func linearUploadURLs(md string) []string {
	var out []string
	for _, m := range linearUploadRe.FindAllString(md, -1) {
		// Trailing sentence punctuation is never part of a URL and routinely
		// follows a bare link in prose. The markdown wrappers are in the set
		// for a sharper reason than tidiness: *…*, _…_ and `…` around a bare
		// link leave their closing character glued to the URL, and a signed
		// Linear URL with one extra byte is a 404 rather than a near miss.
		if s := strings.TrimRight(m, ".,;:!*`_"); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// linearMediaURLs is every attachment an issue carries at creation: the
// description plus the comments the body will render, deduped so a screenshot
// quoted twice downloads once.
func linearMediaURLs(iss core.LinearIssue) []string {
	urls := linearUploadURLs(iss.Description)
	for _, c := range iss.Comments {
		urls = append(urls, linearUploadURLs(c.Body)...)
	}
	return linearDedupe(urls)
}

func linearDedupe(urls []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, u := range urls {
		if !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	return out
}

// linearFetchMedia downloads each URL and materializes it under the job's
// uploads dir, through saveUploadToStore so the bytes on disk and the uploads
// row are written by the same call and cannot disagree.
//
// Every failure is RECORDED rather than returned: media is best-effort by
// contract, and the caller's job (creating a job, delivering an update) is
// worth doing with a missing screenshot and a URL in its place.
func linearFetchMedia(st *store.Store, src linearSource, slug string, urls []string) []linearMediaRef {
	var out []linearMediaRef
	for _, u := range urls {
		ref := linearMediaRef{URL: u}
		data, ct, err := src.DownloadFile(u)
		switch {
		case err != nil:
			ref.Err = err
		default:
			ref.ContentType = ct
			if _, path, err := saveUploadToStore(st, slug, linearMediaExt(u, ct), data); err != nil {
				ref.Err = err
			} else {
				ref.Path = path
			}
		}
		if ref.Err != nil {
			dlogErr("linear: attachment %s not saved: %v", linearScrubURL(u), ref.Err)
		}
		out = append(out, ref)
	}
	return out
}

// linearMediaExt picks the on-disk extension: the content type first (the
// server knows what it sent), then the URL's own suffix, then "bin". It must
// satisfy store.SaveUpload's shape check (lowercase alphanumerics, ≤8), so
// anything that does not is discarded rather than passed through to fail the
// save.
func linearMediaExt(rawURL, contentType string) string {
	if ct, _, err := mime.ParseMediaType(contentType); err == nil {
		if ext, ok := linearExtByType[ct]; ok {
			return ext
		}
	}
	if u, err := url.Parse(rawURL); err == nil {
		if ext := strings.TrimPrefix(strings.ToLower(path.Ext(u.Path)), "."); linearExtRe.MatchString(ext) {
			return ext
		}
	}
	return "bin"
}

// linearExtByType is a small explicit table rather than mime.ExtensionsByType,
// which returns every registered spelling in an order Go does not promise —
// so a jpeg would land as ".jpe" or ".jpg" depending on the platform's mime
// database, and the same attachment would get different names on two runs.
var linearExtByType = map[string]string{
	"image/png":       "png",
	"image/jpeg":      "jpg",
	"image/gif":       "gif",
	"image/webp":      "webp",
	"image/svg+xml":   "svg",
	"application/pdf": "pdf",
	"video/mp4":       "mp4",
	"video/quicktime": "mov",
	"application/zip": "zip",
	"text/plain":      "txt",
}

// linearExtRe mirrors store.uploadExtRe — the same shape check, applied here
// so a URL-derived extension is rejected before it can fail the save.
var linearExtRe = regexp.MustCompile(`^[a-z0-9]{1,8}$`)

// linearIsImage decides which media get an "[image: ...]" line — the form
// Claude Code auto-ingests. The content type is authoritative; the extension
// is the fallback for a server that sent no useful type.
func linearIsImage(m linearMediaRef) bool {
	if ct, _, err := mime.ParseMediaType(m.ContentType); err == nil && strings.HasPrefix(ct, "image/") {
		return true
	}
	switch strings.TrimPrefix(strings.ToLower(path.Ext(m.Path)), ".") {
	case "png", "jpg", "jpeg", "gif", "webp":
		return true
	}
	return false
}

// linearMediaLines renders the media itself: images as the "[image: <path>]"
// line Claude Code ingests, other files as a bare local path an agent can
// open, and a failed download as its ORIGINAL URL — which is the whole point
// of recording the failure. A silently absent screenshot is the one outcome
// nobody can act on.
func linearMediaLines(media []linearMediaRef) string {
	var b strings.Builder
	for _, m := range media {
		switch {
		case m.Path == "":
			b.WriteString(linearScrubURL(m.URL) + " — download failed; fetch it from Linear by hand\n")
		case linearIsImage(m):
			b.WriteString("[image: " + m.Path + "]\n")
		default:
			b.WriteString(m.Path + "\n")
		}
	}
	return b.String()
}

// linearScrubURL drops the query string from an attachment URL (A16). Those
// URLs come back PRE-SIGNED — the query string is a one-hour bearer token for
// the object, minted because the client asks for it on every request — so a
// raw one written into a job body, a log line, or the events table is a
// credential sitting in a durable store that outlives it by months. The path
// still names the object, which is all a human needs alongside the "fetch it
// from Linear by hand" the body line already carries.
//
// It is applied at the SINKS rather than at extraction, because the download
// itself needs the signature intact.
func linearScrubURL(raw string) string {
	if i := strings.IndexByte(raw, '?'); i >= 0 {
		return raw[:i]
	}
	return raw
}

// linearMediaSection is linearMediaLines under a heading, for the job body.
func linearMediaSection(media []linearMediaRef) string {
	if len(media) == 0 {
		return ""
	}
	return "## Attachments\n\n" + linearMediaLines(media)
}
