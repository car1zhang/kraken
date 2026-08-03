package jobs

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"kraken/internal/core"
	"kraken/internal/store"
	"kraken/internal/util"
)

// linear_contract_test.go — athena-logic's BLIND contract tests for
// 50-contract.md, Slice 2 (`internal/jobs/linear.go`). Written from the
// contract alone, with no sight of internal/jobs/linear.go or the daemon
// wiring.
//
// Dimension: LOGIC — the stated invariants and happy paths of R1, R3, R4, R5,
// R7, R8, R9 and R11, plus composeLinearBody / linearSlug /
// linearPriorityLabel. Boundaries, refusals and error paths (R2's unparseable
// kv, R6's slug collision, R10's held event, R12's key-leak audit, fetch
// failures, oversize media) are athena-edge's and are deliberately NOT
// duplicated here.
//
// Every package-level identifier is prefixed `contract`: athena-edge shares
// this package namespace. (useTestStore and mustCreateJob are pre-existing
// helpers from store_test.go and are used as-is.)
//
// Every runLinearPoll test drives the contract's own fake seams — the
// linearSource interface plus the linearDeliverBump / linearCellLive (A2) /
// linearNotify (A4) package vars — so nothing here touches the network, tmux,
// or osascript.

// ---------------------------------------------------------------------------
// harness
// ---------------------------------------------------------------------------

type contractFile struct {
	body  []byte
	ctype string
}

// contractFakeSource is the in-test linearSource: scripted responses, and a
// record of every `since` floor and every URL it was asked to download.
type contractFakeSource struct {
	issues       []core.LinearIssue
	comments     []core.LinearComment
	files        map[string]contractFile
	issueSince   []time.Time
	commentSince []time.Time
	downloaded   []string
}

func (f *contractFakeSource) AssignedIssuesSince(since time.Time) ([]core.LinearIssue, error) {
	f.issueSince = append(f.issueSince, since)
	return f.issues, nil
}

func (f *contractFakeSource) AssignedCommentsSince(since time.Time) ([]core.LinearComment, error) {
	f.commentSince = append(f.commentSince, since)
	return f.comments, nil
}

func (f *contractFakeSource) DownloadFile(url string) ([]byte, string, error) {
	f.downloaded = append(f.downloaded, url)
	if file, ok := f.files[url]; ok {
		return file.body, file.ctype, nil
	}
	return nil, "", fmt.Errorf("contract fake: nothing scripted for %s", url)
}

// The fake must satisfy the contract's seam exactly as *core.LinearClient does.
var _ linearSource = (*contractFakeSource)(nil)

func contractCfg() core.LinearConfig {
	// DefaultRepo is deliberately NOT "monolith": R4 says the created job's
	// repo comes from cfg.DefaultRepo, and a test using the same value the
	// config layer defaults to would pass even if the value were hardcoded.
	return core.LinearConfig{APIKey: "lin_api_x", DefaultRepo: "kraken", States: []string{"Today"}}
}

func contractSeedSync(t *testing.T, st *store.Store, s LinearSync) {
	t.Helper()
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal LinearSync: %v", err)
	}
	if err := st.SetKV(linearSyncKVKey, string(raw)); err != nil {
		t.Fatalf("seed %s: %v", linearSyncKVKey, err)
	}
}

func contractReadSync(t *testing.T, st *store.Store) LinearSync {
	t.Helper()
	raw, err := st.GetKV(linearSyncKVKey)
	if err != nil {
		t.Fatalf("read %s: %v", linearSyncKVKey, err)
	}
	var s LinearSync
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatalf("unmarshal %s (%q): %v", linearSyncKVKey, raw, err)
	}
	return s
}

type contractBumpCall struct {
	Slug string
	Msg  string
}

type contractBumpRec struct {
	calls []contractBumpCall
	ret   bool
}

// contractSwapBump replaces the delivery seam so a test never reaches tmux.
// ret is what the swapped bump reports: true is a confirmed queued send.
func contractSwapBump(t *testing.T, ret bool) *contractBumpRec {
	t.Helper()
	rec := &contractBumpRec{ret: ret}
	prev := linearDeliverBump
	linearDeliverBump = func(j *Job, msg string) bool {
		slug := ""
		if j != nil {
			slug = j.Slug
		}
		rec.calls = append(rec.calls, contractBumpCall{Slug: slug, Msg: msg})
		return rec.ret
	}
	t.Cleanup(func() { linearDeliverBump = prev })
	return rec
}

// contractSwapCellLive replaces A2's liveness seam. R8 needs a started job
// AND a live cell; R10 is the same job with this false.
func contractSwapCellLive(t *testing.T, live bool) {
	t.Helper()
	prev := linearCellLive
	linearCellLive = func(*Job) bool { return live }
	t.Cleanup(func() { linearCellLive = prev })
}

type contractNotifyCall struct {
	Title string
	Msg   string
}

type contractNotifyRec struct {
	calls []contractNotifyCall
}

// contractSwapNotify replaces A4's notification seam: the suite runs quiet (no
// osascript) and "exactly one notify per creation" becomes assertable.
func contractSwapNotify(t *testing.T) *contractNotifyRec {
	t.Helper()
	rec := &contractNotifyRec{}
	prev := linearNotify
	linearNotify = func(title, msg string) {
		rec.calls = append(rec.calls, contractNotifyCall{Title: title, Msg: msg})
	}
	t.Cleanup(func() { linearNotify = prev })
	return rec
}

func contractIssue(id, identifier, title, state string, updated time.Time) core.LinearIssue {
	return core.LinearIssue{
		ID:         id,
		Identifier: identifier,
		Title:      title,
		URL:        "https://linear.app/acme/issue/" + identifier,
		StateName:  state,
		StateType:  "started",
		TeamKey:    "ENG",
		CreatedAt:  updated.Add(-24 * time.Hour),
		UpdatedAt:  updated,
	}
}

func contractComment(id, body, issueID, issueIdentifier string, updated time.Time) core.LinearComment {
	return core.LinearComment{
		ID:              id,
		Body:            body,
		Author:          "carl",
		CreatedAt:       updated,
		UpdatedAt:       updated,
		IssueID:         issueID,
		IssueIdentifier: issueIdentifier,
	}
}

func contractLineWithPrefix(t *testing.T, text, prefix string) string {
	t.Helper()
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), prefix) {
			return strings.TrimSpace(line)
		}
	}
	t.Fatalf("no line starting with %q in:\n%s", prefix, text)
	return ""
}

func contractHasExactLine(text, want string) bool {
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == want {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// constants
// ---------------------------------------------------------------------------

// Contract: the kv key, the poll interval and the overlap margin are all
// frozen values — the overlap in particular is what R3's `since` subtracts and
// what the per-issue stamp makes idempotent.
func TestContractLinearConstants(t *testing.T) {
	if linearSyncKVKey != "linear_sync" {
		t.Fatalf("linearSyncKVKey = %q, want linear_sync", linearSyncKVKey)
	}
	if linearPollInterval != 2*time.Minute {
		t.Fatalf("linearPollInterval = %v, want 2m", linearPollInterval)
	}
	if linearOverlap != 60*time.Second {
		t.Fatalf("linearOverlap = %v, want 60s", linearOverlap)
	}
}

// ---------------------------------------------------------------------------
// R1 — first poll ever
// ---------------------------------------------------------------------------

// R1: with no linear_sync row, the poll seeds CursorMS = now, stamps
// LastPollMS = now, fetches NOTHING and creates NOTHING (Q6 — no backfill;
// the historical backlog must not flood the board).
func TestContractLinearPollFirstPollSeedsAndCreatesNothing(t *testing.T) {
	st := useTestStore(t)
	notes := contractSwapNotify(t)
	now := time.UnixMilli(1_754_000_000_000)
	// Scripted content that must never be reached on the seeding poll.
	src := &contractFakeSource{
		issues: []core.LinearIssue{contractIssue("uuid-1", "ENG-1", "Ship it", "Today", now)},
	}

	if err := runLinearPoll(st, src, contractCfg(), now); err != nil {
		t.Fatalf("runLinearPoll: %v", err)
	}

	if len(src.issueSince) != 0 || len(src.commentSince) != 0 {
		t.Fatalf("first poll fetched (%d issue calls, %d comment calls), want none",
			len(src.issueSince), len(src.commentSince))
	}
	sync := contractReadSync(t, st)
	if sync.CursorMS != now.UnixMilli() {
		t.Fatalf("CursorMS = %d, want the seed %d", sync.CursorMS, now.UnixMilli())
	}
	if sync.LastPollMS != now.UnixMilli() {
		t.Fatalf("LastPollMS = %d, want %d", sync.LastPollMS, now.UnixMilli())
	}
	jobs, err := st.ListJobs()
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("first poll created %d jobs, want 0 (no backfill)", len(jobs))
	}
	rows, err := st.ListLinearIssues()
	if err != nil {
		t.Fatalf("ListLinearIssues: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("first poll wrote %d dedup rows, want 0", len(rows))
	}
	if len(notes.calls) != 0 {
		t.Fatalf("first poll fired %d notifications, want 0: %+v", len(notes.calls), notes.calls)
	}
}

// ---------------------------------------------------------------------------
// R3 — the fetch floor
// ---------------------------------------------------------------------------

// R3: both fetches use since = CursorMS − linearOverlap. The margin is
// deliberately replayed; the per-issue stamp is what makes it free.
func TestContractLinearPollFetchesFromCursorMinusOverlap(t *testing.T) {
	st := useTestStore(t)
	contractSwapNotify(t)
	cursor := time.UnixMilli(1_754_000_000_000)
	contractSeedSync(t, st, LinearSync{CursorMS: cursor.UnixMilli(), LastPollMS: cursor.UnixMilli()})
	src := &contractFakeSource{}

	now := cursor.Add(10 * time.Minute)
	if err := runLinearPoll(st, src, contractCfg(), now); err != nil {
		t.Fatalf("runLinearPoll: %v", err)
	}

	want := cursor.Add(-linearOverlap)
	if len(src.issueSince) != 1 {
		t.Fatalf("issue fetch called %d times, want 1", len(src.issueSince))
	}
	if !src.issueSince[0].Equal(want) {
		t.Fatalf("issue fetch since = %v, want cursor−overlap %v", src.issueSince[0], want)
	}
	if len(src.commentSince) != 1 {
		t.Fatalf("comment fetch called %d times, want 1", len(src.commentSince))
	}
	if !src.commentSince[0].Equal(want) {
		t.Fatalf("comment fetch since = %v, want cursor−overlap %v", src.commentSince[0], want)
	}
}

// ---------------------------------------------------------------------------
// R4 — new matching issue becomes a draft job
// ---------------------------------------------------------------------------

// R4: a new issue in a configured state becomes a DRAFT job (ready: false)
// with cfg.DefaultRepo, its body carrying the composed sections, plus a dedup
// row whose LastEventAt is the max of the issue's UpdatedAt and its
// creation-time comments' — so creation-time content is never re-delivered as
// an update.
func TestContractLinearPollCreatesDraftJobFromNewIssue(t *testing.T) {
	st := useTestStore(t)
	notes := contractSwapNotify(t)
	cursor := time.UnixMilli(1_754_000_000_000)
	contractSeedSync(t, st, LinearSync{CursorMS: cursor.UnixMilli(), LastPollMS: cursor.UnixMilli()})

	issueUpdated := cursor.Add(time.Hour)
	commentUpdated := cursor.Add(2 * time.Hour) // newer than the issue itself
	iss := contractIssue("uuid-142", "ENG-142", "Ship the thing", "Today", issueUpdated)
	iss.Description = "line one\n\nline two"
	iss.ProjectName = "Kraken"
	iss.Priority = 2
	iss.Labels = []string{"bug", "p1"}
	iss.Comments = []core.LinearComment{
		contractComment("cmt-1", "a creation-time comment", "uuid-142", "ENG-142", commentUpdated),
	}
	src := &contractFakeSource{issues: []core.LinearIssue{iss}}

	if err := runLinearPoll(st, src, contractCfg(), cursor.Add(3*time.Hour)); err != nil {
		t.Fatalf("runLinearPoll: %v", err)
	}

	slug := linearSlug("ENG-142", "Ship the thing")
	j, err := st.GetJobBySlug(slug)
	if err != nil {
		t.Fatalf("GetJobBySlug(%s): %v", slug, err)
	}
	if j.Ready {
		t.Fatal("created job is ready; R4/Q1 says it lands as a DRAFT")
	}
	if j.Repo != contractCfg().DefaultRepo {
		t.Fatalf("job repo = %q, want cfg.DefaultRepo %q", j.Repo, contractCfg().DefaultRepo)
	}
	if j.Err != "" {
		t.Fatalf("created job carries a parse error %q — the composed text must ParseJob clean", j.Err)
	}
	for _, want := range []string{
		"Ship the thing",
		"ENG-142",
		"line one\n\nline two",                  // description verbatim
		"https://linear.app/acme/issue/ENG-142", // the Source line's URL
		"a creation-time comment",               // Q4's comment snapshot
	} {
		if !strings.Contains(j.Body, want) {
			t.Fatalf("job body is missing %q:\n%s", want, j.Body)
		}
	}
	if !strings.Contains(j.Body, "## Comments") {
		t.Fatalf("job body has no ## Comments section despite a creation-time comment:\n%s", j.Body)
	}

	row, err := st.GetLinearIssue("uuid-142")
	if err != nil {
		t.Fatalf("GetLinearIssue: %v", err)
	}
	if row.JobID != j.ID {
		t.Fatalf("dedup row JobID = %d, want the created job's id %d", row.JobID, j.ID)
	}
	if row.Identifier != "ENG-142" {
		t.Fatalf("dedup row Identifier = %q, want ENG-142", row.Identifier)
	}
	if row.SkipReason != "" {
		t.Fatalf("dedup row SkipReason = %q, want empty for a created job", row.SkipReason)
	}
	if row.LastEventAt != commentUpdated.UnixMilli() {
		t.Fatalf("dedup row LastEventAt = %d, want max(issue, creation comments) = %d",
			row.LastEventAt, commentUpdated.UnixMilli())
	}

	// A4: exactly ONE notification per creation.
	if len(notes.calls) != 1 {
		t.Fatalf("fired %d notifications for one created job, want exactly 1: %+v",
			len(notes.calls), notes.calls)
	}
}

// R4: the state match is CASE-INSENSITIVE (Q3 — state names are user-renamable
// strings, so the filter must not be brittle about capitalisation).
func TestContractLinearPollStateMatchIsCaseInsensitive(t *testing.T) {
	st := useTestStore(t)
	contractSwapNotify(t)
	cursor := time.UnixMilli(1_754_000_000_000)
	contractSeedSync(t, st, LinearSync{CursorMS: cursor.UnixMilli(), LastPollMS: cursor.UnixMilli()})

	iss := contractIssue("uuid-1", "ENG-1", "Lowercase state", "today", cursor.Add(time.Hour))
	src := &contractFakeSource{issues: []core.LinearIssue{iss}}

	if err := runLinearPoll(st, src, contractCfg(), cursor.Add(2*time.Hour)); err != nil {
		t.Fatalf("runLinearPoll: %v", err)
	}

	slug := linearSlug("ENG-1", "Lowercase state")
	if _, err := st.GetJobBySlug(slug); err != nil {
		t.Fatalf("no job for state %q against cfg.States %v: %v", iss.StateName, contractCfg().States, err)
	}
}

// A10: a cfg whose States trimmed to nothing matches NO state — an explicit
// empty configuration is honoured, so a "Today" issue creates nothing.
func TestContractLinearPollEmptyStatesMatchesNothing(t *testing.T) {
	st := useTestStore(t)
	notes := contractSwapNotify(t)
	cursor := time.UnixMilli(1_754_000_000_000)
	contractSeedSync(t, st, LinearSync{CursorMS: cursor.UnixMilli(), LastPollMS: cursor.UnixMilli()})

	cfg := contractCfg()
	cfg.States = nil
	src := &contractFakeSource{
		issues: []core.LinearIssue{contractIssue("uuid-1", "ENG-1", "Today issue", "Today", cursor.Add(time.Hour))},
	}

	if err := runLinearPoll(st, src, cfg, cursor.Add(2*time.Hour)); err != nil {
		t.Fatalf("runLinearPoll: %v", err)
	}

	jobs, err := st.ListJobs()
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("created %d jobs with an empty States config, want 0", len(jobs))
	}
	if len(notes.calls) != 0 {
		t.Fatalf("fired %d notifications, want 0", len(notes.calls))
	}
}

// R4: media found in the description and comments is downloaded through the
// source and materialized under the created job's uploads dir, then the body
// is patched with the media lines. A URL appearing twice downloads ONCE.
func TestContractLinearPollDownloadsAndLinksMedia(t *testing.T) {
	st := useTestStore(t)
	contractSwapNotify(t)
	cursor := time.UnixMilli(1_754_000_000_000)
	contractSeedSync(t, st, LinearSync{CursorMS: cursor.UnixMilli(), LastPollMS: cursor.UnixMilli()})

	shot := "https://uploads.linear.app/abc/def/shot.png"
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 7, 7, 7}
	iss := contractIssue("uuid-1", "ENG-1", "With a screenshot", "Today", cursor.Add(time.Hour))
	iss.Description = "before\n\n![shot](" + shot + ")\n\nafter"
	iss.Comments = []core.LinearComment{
		// The SAME url again: unique-per-URL means one download, not two.
		contractComment("cmt-1", "see "+shot+" again", "uuid-1", "ENG-1", cursor.Add(time.Hour)),
	}
	src := &contractFakeSource{
		issues: []core.LinearIssue{iss},
		files:  map[string]contractFile{shot: {body: png, ctype: "image/png"}},
	}

	if err := runLinearPoll(st, src, contractCfg(), cursor.Add(2*time.Hour)); err != nil {
		t.Fatalf("runLinearPoll: %v", err)
	}

	if len(src.downloaded) != 1 {
		t.Fatalf("downloaded %v, want the duplicate URL fetched exactly once", src.downloaded)
	}
	slug := linearSlug("ENG-1", "With a screenshot")
	j, err := st.GetJobBySlug(slug)
	if err != nil {
		t.Fatalf("GetJobBySlug(%s): %v", slug, err)
	}
	line := contractLineWithPrefix(t, j.Body, "[image: ")
	path := strings.TrimSuffix(strings.TrimPrefix(line, "[image: "), "]")
	if !strings.HasPrefix(path, UploadsDir(slug, j.ID)) {
		t.Fatalf("media path %q is not under the job's uploads dir %q", path, UploadsDir(slug, j.ID))
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("media file %q not materialized: %v", path, err)
	}
	if string(got) != string(png) {
		t.Fatalf("media file holds %v, want the downloaded bytes %v", got, png)
	}
}

// ---------------------------------------------------------------------------
// R5 — a non-matching state is ignored outright
// ---------------------------------------------------------------------------

// R5: an issue whose state is not in cfg.States gets NO dedup row and no job —
// deliberately no row, so that when it LATER enters a matching state that
// transition bumps its updatedAt and the cursor catches it. The cursor still
// advances past it, which is what makes that later catch a new event rather
// than an endless re-read.
func TestContractLinearPollIgnoresNonMatchingState(t *testing.T) {
	st := useTestStore(t)
	notes := contractSwapNotify(t)
	cursor := time.UnixMilli(1_754_000_000_000)
	contractSeedSync(t, st, LinearSync{CursorMS: cursor.UnixMilli(), LastPollMS: cursor.UnixMilli()})

	updated := cursor.Add(time.Hour)
	iss := contractIssue("uuid-1", "ENG-1", "Not today", "Backlog", updated)
	src := &contractFakeSource{issues: []core.LinearIssue{iss}}

	if err := runLinearPoll(st, src, contractCfg(), cursor.Add(2*time.Hour)); err != nil {
		t.Fatalf("runLinearPoll: %v", err)
	}

	jobs, err := st.ListJobs()
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("created %d jobs for a non-matching state, want 0: %+v", len(jobs), jobs)
	}
	rows, err := st.ListLinearIssues()
	if err != nil {
		t.Fatalf("ListLinearIssues: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("wrote %d dedup rows for a non-matching state, want 0: %+v", len(rows), rows)
	}
	if sync := contractReadSync(t, st); sync.CursorMS != updated.UnixMilli() {
		t.Fatalf("CursorMS = %d, want it advanced past the ignored issue to %d",
			sync.CursorMS, updated.UnixMilli())
	}
	if len(notes.calls) != 0 {
		t.Fatalf("fired %d notifications for an ignored issue, want 0: %+v", len(notes.calls), notes.calls)
	}
}

// ---------------------------------------------------------------------------
// R7 + R8 — a known issue's new events reach a live started job
// ---------------------------------------------------------------------------

// contractKnownIssue seeds the pair a "known issue" test needs: a job row and
// its linear_issues row stamped at lastEvent.
func contractKnownIssue(t *testing.T, st *store.Store, slug, state string, body string, lastEvent time.Time) *store.Job {
	t.Helper()
	j := mustCreateJob(t, st, store.Job{Slug: slug, Repo: "kraken", Ready: state != "", State: state, Body: body})
	if err := st.PutLinearIssue(&store.LinearIssueRow{
		IssueID:     "uuid-142",
		Identifier:  "ENG-142",
		JobID:       j.ID,
		CreatedAt:   lastEvent.Add(-time.Hour).UnixMilli(),
		LastEventAt: lastEvent.UnixMilli(),
	}); err != nil {
		t.Fatalf("PutLinearIssue: %v", err)
	}
	return j
}

// R7 + R8: a started job whose delivery is confirmed gets ONE combined bump
// for all of that issue's new events, and the stamp advances to the NEWEST
// delivered event.
func TestContractLinearPollBumpsStartedJobOnceForAllNewEvents(t *testing.T) {
	st := useTestStore(t)
	contractSwapNotify(t)
	contractSwapCellLive(t, true) // A2: R8 needs started AND a live cell
	base := time.UnixMilli(1_754_000_000_000)
	contractSeedSync(t, st, LinearSync{CursorMS: base.UnixMilli(), LastPollMS: base.UnixMilli()})
	stamp := base
	j := contractKnownIssue(t, st, "eng-142-ship-the-thing", "working", "ORIGINAL BODY", stamp)

	issueEvent := stamp.Add(time.Hour)
	commentEvent := stamp.Add(2 * time.Hour)
	iss := contractIssue("uuid-142", "ENG-142", "Ship the thing", "Today", issueEvent)
	iss.Description = "edited description"
	src := &contractFakeSource{
		issues:   []core.LinearIssue{iss},
		comments: []core.LinearComment{contractComment("cmt-1", "a brand new comment", "uuid-142", "ENG-142", commentEvent)},
	}
	rec := contractSwapBump(t, true)

	if err := runLinearPoll(st, src, contractCfg(), commentEvent.Add(time.Minute)); err != nil {
		t.Fatalf("runLinearPoll: %v", err)
	}

	if len(rec.calls) != 1 {
		t.Fatalf("delivered %d bumps, want exactly 1 combined message: %+v", len(rec.calls), rec.calls)
	}
	call := rec.calls[0]
	if call.Slug != j.Slug {
		t.Fatalf("bumped job %q, want %q", call.Slug, j.Slug)
	}
	if !strings.Contains(call.Msg, "ENG-142") {
		t.Fatalf("bump message does not name the issue identifier:\n%s", call.Msg)
	}
	if !strings.Contains(call.Msg, "a brand new comment") {
		t.Fatalf("bump message does not carry the new comment body:\n%s", call.Msg)
	}

	row, err := st.GetLinearIssue("uuid-142")
	if err != nil {
		t.Fatalf("GetLinearIssue: %v", err)
	}
	if row.LastEventAt != commentEvent.UnixMilli() {
		t.Fatalf("LastEventAt = %d, want the newest delivered event %d",
			row.LastEventAt, commentEvent.UnixMilli())
	}

	after, err := st.GetJobBySlug(j.Slug)
	if err != nil {
		t.Fatalf("GetJobBySlug: %v", err)
	}
	if after.Body != "ORIGINAL BODY" {
		t.Fatalf("a started job's body was rewritten (%q); R8 delivers to the PANE, R9 writes bodies", after.Body)
	}
}

// R7: a comment alone qualifies — this is the whole reason
// AssignedCommentsSince exists, since a comment does not reliably bump its
// parent issue's updatedAt, so the issue itself is not in this poll's fetch.
func TestContractLinearPollBumpsOnCommentWithoutIssueRefetch(t *testing.T) {
	st := useTestStore(t)
	contractSwapNotify(t)
	contractSwapCellLive(t, true)
	base := time.UnixMilli(1_754_000_000_000)
	contractSeedSync(t, st, LinearSync{CursorMS: base.UnixMilli(), LastPollMS: base.UnixMilli()})
	contractKnownIssue(t, st, "eng-142-ship-the-thing", "working", "ORIGINAL BODY", base)

	commentEvent := base.Add(90 * time.Minute)
	src := &contractFakeSource{
		comments: []core.LinearComment{contractComment("cmt-1", "comment only", "uuid-142", "ENG-142", commentEvent)},
	}
	rec := contractSwapBump(t, true)

	if err := runLinearPoll(st, src, contractCfg(), commentEvent.Add(time.Minute)); err != nil {
		t.Fatalf("runLinearPoll: %v", err)
	}

	if len(rec.calls) != 1 {
		t.Fatalf("delivered %d bumps for a comment-only event, want 1: %+v", len(rec.calls), rec.calls)
	}
	if !strings.Contains(rec.calls[0].Msg, "comment only") {
		t.Fatalf("bump message does not carry the comment body:\n%s", rec.calls[0].Msg)
	}
	row, err := st.GetLinearIssue("uuid-142")
	if err != nil {
		t.Fatalf("GetLinearIssue: %v", err)
	}
	if row.LastEventAt != commentEvent.UnixMilli() {
		t.Fatalf("LastEventAt = %d, want %d", row.LastEventAt, commentEvent.UnixMilli())
	}
}

// R7: events must be STRICTLY newer than the stamp. An event at or below it —
// which is exactly what the overlap margin replays every poll — is not a
// qualifying event, and nothing happens at all.
func TestContractLinearPollIgnoresEventsAtOrBelowTheStamp(t *testing.T) {
	st := useTestStore(t)
	contractSwapNotify(t)
	contractSwapCellLive(t, true)
	base := time.UnixMilli(1_754_000_000_000)
	contractSeedSync(t, st, LinearSync{CursorMS: base.UnixMilli(), LastPollMS: base.UnixMilli()})
	stamp := base
	contractKnownIssue(t, st, "eng-142-ship-the-thing", "working", "ORIGINAL BODY", stamp)

	iss := contractIssue("uuid-142", "ENG-142", "Ship the thing", "Today", stamp) // equal, not newer
	src := &contractFakeSource{
		issues:   []core.LinearIssue{iss},
		comments: []core.LinearComment{contractComment("cmt-old", "replayed", "uuid-142", "ENG-142", stamp.Add(-time.Minute))},
	}
	rec := contractSwapBump(t, true)

	if err := runLinearPoll(st, src, contractCfg(), stamp.Add(time.Hour)); err != nil {
		t.Fatalf("runLinearPoll: %v", err)
	}

	if len(rec.calls) != 0 {
		t.Fatalf("delivered %d bumps for already-stamped events, want 0: %+v", len(rec.calls), rec.calls)
	}
	row, err := st.GetLinearIssue("uuid-142")
	if err != nil {
		t.Fatalf("GetLinearIssue: %v", err)
	}
	if row.LastEventAt != stamp.UnixMilli() {
		t.Fatalf("LastEventAt = %d, want it unmoved at %d", row.LastEventAt, stamp.UnixMilli())
	}
	after, err := st.GetJobBySlug("eng-142-ship-the-thing")
	if err != nil {
		t.Fatalf("GetJobBySlug: %v", err)
	}
	if after.Body != "ORIGINAL BODY" {
		t.Fatalf("body changed (%q) with no qualifying event", after.Body)
	}
}

// ---------------------------------------------------------------------------
// R9 — a not-started job takes the update in its BODY
// ---------------------------------------------------------------------------

// R9: a draft/queued job never gets a pane bump (there is no pane) — the
// update is APPENDED to the body as a `## Linear update (<RFC3339 ts>)`
// section, behind the rev lock, with the original body preserved.
func TestContractLinearPollAppendsUpdateToNotStartedJob(t *testing.T) {
	st := useTestStore(t)
	contractSwapNotify(t)
	// A2 routes on !j.Started() alone for R9 — a live cell must not divert a
	// draft job's update to a pane it does not have.
	contractSwapCellLive(t, true)
	base := time.UnixMilli(1_754_000_000_000)
	contractSeedSync(t, st, LinearSync{CursorMS: base.UnixMilli(), LastPollMS: base.UnixMilli()})
	original := "# Ship the thing (ENG-142)\n\nORIGINAL BODY LINE\n"
	j := contractKnownIssue(t, st, "eng-142-ship-the-thing", "", original, base)
	before, err := st.GetJobBySlug(j.Slug)
	if err != nil {
		t.Fatalf("GetJobBySlug: %v", err)
	}

	commentEvent := base.Add(90 * time.Minute)
	src := &contractFakeSource{
		comments: []core.LinearComment{contractComment("cmt-1", "an update while it waits", "uuid-142", "ENG-142", commentEvent)},
	}
	rec := contractSwapBump(t, true)

	now := commentEvent.Add(time.Minute)
	if err := runLinearPoll(st, src, contractCfg(), now); err != nil {
		t.Fatalf("runLinearPoll: %v", err)
	}

	if len(rec.calls) != 0 {
		t.Fatalf("bumped a not-started job %d times, want 0: %+v", len(rec.calls), rec.calls)
	}
	after, err := st.GetJobBySlug(j.Slug)
	if err != nil {
		t.Fatalf("GetJobBySlug: %v", err)
	}
	if !strings.Contains(after.Body, "ORIGINAL BODY LINE") {
		t.Fatalf("original body lost — R9 APPENDS, never rewrites:\n%s", after.Body)
	}
	header := contractLineWithPrefix(t, after.Body, "## Linear update (")
	if !strings.Contains(after.Body, "an update while it waits") {
		t.Fatalf("appended section does not carry the new comment body:\n%s", after.Body)
	}
	if strings.Index(after.Body, "ORIGINAL BODY LINE") > strings.Index(after.Body, header) {
		t.Fatalf("the update section landed BEFORE the original body:\n%s", after.Body)
	}
	// A9: the ts is the poll's `now`, rendered UTC RFC3339.
	wantHeader := "## Linear update (" + now.UTC().Format(time.RFC3339) + ")"
	if header != wantHeader {
		t.Fatalf("update header = %q, want %q", header, wantHeader)
	}
	if after.Rev != before.Rev+1 {
		t.Fatalf("rev = %d, want %d — the append must go through UpdateJobFields' rev lock",
			after.Rev, before.Rev+1)
	}

	row, err := st.GetLinearIssue("uuid-142")
	if err != nil {
		t.Fatalf("GetLinearIssue: %v", err)
	}
	if row.LastEventAt != commentEvent.UnixMilli() {
		t.Fatalf("LastEventAt = %d, want it stamped after the successful write to %d",
			row.LastEventAt, commentEvent.UnixMilli())
	}
}

// ---------------------------------------------------------------------------
// R11 — cursor advance
// ---------------------------------------------------------------------------

// R11: the cursor advances to the MAX OBSERVED UpdatedAt across fetched issues
// and comments — never to `now`, whose skew against Linear's clock would skip
// issues. LastPollMS is the one that tracks wall-clock.
func TestContractLinearPollCursorAdvancesToMaxObservedNotNow(t *testing.T) {
	st := useTestStore(t)
	contractSwapNotify(t)
	base := time.UnixMilli(1_754_000_000_000)
	contractSeedSync(t, st, LinearSync{CursorMS: base.UnixMilli(), LastPollMS: base.UnixMilli()})

	issueEvent := base.Add(time.Hour)
	commentEvent := base.Add(2 * time.Hour) // the max across both fetches
	iss := contractIssue("uuid-1", "ENG-1", "Ship it", "Today", issueEvent)
	iss.Comments = []core.LinearComment{contractComment("cmt-1", "hello", "uuid-1", "ENG-1", commentEvent)}
	src := &contractFakeSource{
		issues:   []core.LinearIssue{iss},
		comments: []core.LinearComment{contractComment("cmt-1", "hello", "uuid-1", "ENG-1", commentEvent)},
	}

	now := base.Add(72 * time.Hour) // deliberately far from any observed timestamp
	if err := runLinearPoll(st, src, contractCfg(), now); err != nil {
		t.Fatalf("runLinearPoll: %v", err)
	}

	sync := contractReadSync(t, st)
	if sync.CursorMS == now.UnixMilli() {
		t.Fatalf("CursorMS = %d, i.e. local wall-clock now — R11 forbids it", sync.CursorMS)
	}
	if sync.CursorMS != commentEvent.UnixMilli() {
		t.Fatalf("CursorMS = %d, want the max observed UpdatedAt %d",
			sync.CursorMS, commentEvent.UnixMilli())
	}
	if sync.LastPollMS != now.UnixMilli() {
		t.Fatalf("LastPollMS = %d, want now %d", sync.LastPollMS, now.UnixMilli())
	}
}

// R11: with nothing fetched the cursor is UNCHANGED — an empty window must not
// drag the high-water mark forward — while LastPollMS still advances, so the
// next poll waits an interval rather than firing on the next tick.
func TestContractLinearPollCursorUnchangedWhenNothingFetched(t *testing.T) {
	st := useTestStore(t)
	contractSwapNotify(t)
	base := time.UnixMilli(1_754_000_000_000)
	contractSeedSync(t, st, LinearSync{CursorMS: base.UnixMilli(), LastPollMS: base.UnixMilli()})
	src := &contractFakeSource{}

	now := base.Add(10 * time.Minute)
	if err := runLinearPoll(st, src, contractCfg(), now); err != nil {
		t.Fatalf("runLinearPoll: %v", err)
	}

	sync := contractReadSync(t, st)
	if sync.CursorMS != base.UnixMilli() {
		t.Fatalf("CursorMS = %d, want it unchanged at %d", sync.CursorMS, base.UnixMilli())
	}
	if sync.LastPollMS != now.UnixMilli() {
		t.Fatalf("LastPollMS = %d, want now %d", sync.LastPollMS, now.UnixMilli())
	}
}

// ---------------------------------------------------------------------------
// composeLinearBody
// ---------------------------------------------------------------------------

// The composed text must round-trip ParseJob with NO error for adversarial
// titles and descriptions — a `---` line, YAML metacharacters, an empty
// description. A parse error here would pin the job to "draft" forever with
// nothing able to repair it.
func TestContractComposeLinearBodyRoundTripsParseJob(t *testing.T) {
	base := time.UnixMilli(1_754_000_000_000)
	cases := []struct {
		name        string
		identifier  string
		title       string
		description string
	}{
		{"plain", "ENG-1", "Ship the thing", "just a description"},
		{"empty description", "ENG-2", "No description at all", ""},
		{"yaml metacharacters in title", "ENG-3", `ready: true # {a: [b]} "quoted" 'single' | > &anchor *ref`, "body"},
		{"fence in title", "ENG-4", "---", "body"},
		{"colon in title", "ENG-5", "fix: handle null sid", "body"},
		{"fence lines in description", "ENG-6", "Frontmatter smuggling",
			"---\njob: hijacked\nready: true\n---\n\nreal text"},
		{"job key in description", "ENG-7", "Key smuggling", "job: not-the-slug\nrepo: not-the-repo"},
		{"indented and blank lines", "ENG-8", "Whitespace", "  indented\n\n\n\ttabbed\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			iss := contractIssue("uuid-x", tc.identifier, tc.title, "Today", base)
			iss.Description = tc.description
			text := composeLinearBody(iss, contractCfg(), nil)

			parsed := ParseJob("x"+JobSuffix, []byte(text))
			if parsed.Err != "" {
				t.Fatalf("composed text does not ParseJob clean (%s):\n%s", parsed.Err, text)
			}
			if parsed.Ready {
				t.Fatalf("parsed ready = true, want false (drafts only):\n%s", text)
			}
			if want := linearSlug(tc.identifier, tc.title); parsed.Slug != want {
				t.Fatalf("parsed slug = %q, want the frontmatter job: to carry linearSlug %q", parsed.Slug, want)
			}
			// A1: the repo: line renders cfg.DefaultRepo, and the composer
			// reads no env of its own.
			if parsed.Repo != contractCfg().DefaultRepo {
				t.Fatalf("parsed repo = %q, want cfg.DefaultRepo %q", parsed.Repo, contractCfg().DefaultRepo)
			}
			if strings.TrimSpace(parsed.Body) == "" {
				t.Fatalf("parsed body is empty:\n%s", text)
			}
			if len(parsed.Prereqs) != 0 {
				t.Fatalf("parsed prereqs = %#v, want the empty list", parsed.Prereqs)
			}
		})
	}
}

// The description's own line structure is preserved BYTE for byte — links
// intact, no re-wrapping, no re-indenting. The ticket text is the payload.
func TestContractComposeLinearBodyPreservesDescriptionVerbatim(t *testing.T) {
	base := time.UnixMilli(1_754_000_000_000)
	desc := "# A heading\n\n" +
		"- a bullet with [a link](https://example.com/a?b=c&d=e)\n" +
		"-   three spaces after the dash\n\n\n" +
		"    an indented code block\n" +
		"\ta tab-indented line\n" +
		"trailing spaces here   \n" +
		"| a | table |\n|---|---|\n| 1 | 2 |"
	iss := contractIssue("uuid-1", "ENG-1", "Verbatim", "Today", base)
	iss.Description = desc

	text := composeLinearBody(iss, contractCfg(), nil)
	if !strings.Contains(text, desc) {
		t.Fatalf("description was not preserved byte-for-byte.\nwant substring:\n%q\ngot:\n%s", desc, text)
	}
}

// Media lines: an IMAGE gets an `[image: <abs path>]` line (the form Claude
// Code auto-ingests), a non-image gets its plain path, and a FAILED download
// keeps the original URL with a note — never a silently absent image.
func TestContractComposeLinearBodyRendersMediaLines(t *testing.T) {
	base := time.UnixMilli(1_754_000_000_000)
	iss := contractIssue("uuid-1", "ENG-1", "With media", "Today", base)
	iss.Description = "see the attachments"
	imgPath := "/tmp/kraken-contract/img-001.png"
	pdfPath := "/tmp/kraken-contract/img-002.pdf"
	failedURL := "https://uploads.linear.app/ghi/gone.png"
	media := []linearMediaRef{
		{URL: "https://uploads.linear.app/abc/shot.png", Path: imgPath, ContentType: "image/png"},
		{URL: "https://uploads.linear.app/def/spec.pdf", Path: pdfPath, ContentType: "application/pdf"},
		{URL: failedURL, Err: errors.New("download failed: 503")},
	}

	text := composeLinearBody(iss, contractCfg(), media)

	if !contractHasExactLine(text, "[image: "+imgPath+"]") {
		t.Fatalf("no `[image: %s]` line:\n%s", imgPath, text)
	}
	if !contractHasExactLine(text, pdfPath) {
		t.Fatalf("no plain-path line for the non-image %s:\n%s", pdfPath, text)
	}
	if strings.Contains(text, "[image: "+pdfPath+"]") {
		t.Fatalf("a non-image was rendered as an [image: ] line:\n%s", text)
	}
	if !strings.Contains(text, failedURL) {
		t.Fatalf("a failed download dropped its original URL %s:\n%s", failedURL, text)
	}
	if parsed := ParseJob("x"+JobSuffix, []byte(text)); parsed.Err != "" {
		t.Fatalf("media-carrying text does not ParseJob clean (%s):\n%s", parsed.Err, text)
	}
}

// The heading, the Source line and the metadata line: title + identifier in
// the heading, the Linear URL on a Source line, and team/state/priority/
// labels/project on the metadata line with EMPTY PARTS OMITTED — priority 0
// is unset and must contribute no label at all.
func TestContractComposeLinearBodyHeadingSourceAndMetadata(t *testing.T) {
	base := time.UnixMilli(1_754_000_000_000)
	iss := contractIssue("uuid-142", "ENG-142", "Ship the thing", "Today", base)
	iss.Description = "body"
	iss.Priority = 2
	iss.Labels = []string{"bug", "p1"}
	iss.ProjectName = "Kraken"

	text := composeLinearBody(iss, contractCfg(), nil)
	if !contractHasExactLine(text, "# Ship the thing (ENG-142)") {
		t.Fatalf("no `# <title> (<identifier>)` heading:\n%s", text)
	}
	source := contractLineWithPrefix(t, text, "Source:")
	if !strings.Contains(source, iss.URL) {
		t.Fatalf("Source line %q does not carry the Linear URL %q", source, iss.URL)
	}
	for _, want := range []string{"ENG", "Today", "High", "bug", "p1", "Kraken"} {
		if !strings.Contains(text, want) {
			t.Fatalf("metadata is missing %q:\n%s", want, text)
		}
	}
	// A9: the metadata line's separator is " · ".
	meta := contractLineWithPrefix(t, text, "ENG · ")
	if !strings.Contains(meta, " · Today") {
		t.Fatalf("metadata line %q does not join its parts with %q", meta, " · ")
	}

	// Priority 0 is UNSET: no priority word anywhere.
	bare := contractIssue("uuid-2", "ENG-2", "Bare", "Today", base)
	bare.Description = "body"
	bareText := composeLinearBody(bare, contractCfg(), nil)
	for _, word := range []string{"Urgent", "High", "Normal", "Low"} {
		if strings.Contains(bareText, word) {
			t.Fatalf("priority 0 produced the label %q — it is unset and must be omitted:\n%s", word, bareText)
		}
	}
}

// The `## Comments` section exists only WHEN comments exist (Q4's snapshot at
// creation), and carries author and body.
func TestContractComposeLinearBodyCommentsSection(t *testing.T) {
	base := time.UnixMilli(1_754_000_000_000)
	withNone := contractIssue("uuid-1", "ENG-1", "No comments", "Today", base)
	withNone.Description = "body"
	if text := composeLinearBody(withNone, contractCfg(), nil); strings.Contains(text, "## Comments") {
		t.Fatalf("a commentless issue rendered a ## Comments section:\n%s", text)
	}

	withSome := contractIssue("uuid-2", "ENG-2", "Two comments", "Today", base)
	withSome.Description = "body"
	withSome.Comments = []core.LinearComment{
		contractComment("cmt-1", "first thought", "uuid-2", "ENG-2", base),
		contractComment("cmt-2", "second thought", "uuid-2", "ENG-2", base.Add(time.Hour)),
	}
	text := composeLinearBody(withSome, contractCfg(), nil)
	if !strings.Contains(text, "## Comments") {
		t.Fatalf("no ## Comments section despite two comments:\n%s", text)
	}
	for _, want := range []string{"carl", "first thought", "second thought"} {
		if !strings.Contains(text, want) {
			t.Fatalf("comments section is missing %q:\n%s", want, text)
		}
	}
	// A9: a comment renders as `### <author> — <RFC3339 createdAt>` followed by
	// its body. The zone is not frozen for this one, so the assertion is on the
	// format and the INSTANT rather than on a rendered string.
	header := contractLineWithPrefix(t, text, "### carl — ")
	ts, err := time.Parse(time.RFC3339, strings.TrimPrefix(header, "### carl — "))
	if err != nil {
		t.Fatalf("comment header %q does not end in an RFC3339 createdAt: %v", header, err)
	}
	if !ts.Equal(withSome.Comments[0].CreatedAt.Truncate(time.Second)) {
		t.Fatalf("comment header timestamp = %v, want the comment's createdAt %v",
			ts, withSome.Comments[0].CreatedAt)
	}
	if parsed := ParseJob("x"+JobSuffix, []byte(text)); parsed.Err != "" {
		t.Fatalf("comment-carrying text does not ParseJob clean (%s):\n%s", parsed.Err, text)
	}
}

// ---------------------------------------------------------------------------
// linearSlug / linearPriorityLabel
// ---------------------------------------------------------------------------

// Contract: `strings.ToLower(identifier) + "-" + util.Slugify(title)` — the
// identifier prefix is what keeps the slug unique per issue.
func TestContractLinearSlugComposition(t *testing.T) {
	cases := []struct {
		identifier, title, want string
	}{
		{"ENG-142", "Ship the Thing", "eng-142-ship-the-thing"},
		{"ENG-1", "Fix: handle null SID!", "eng-1-fix-handle-null-sid"},
		{"OPS-9", "  leading and trailing  ", "ops-9-leading-and-trailing"},
	}
	for _, tc := range cases {
		if got := linearSlug(tc.identifier, tc.title); got != tc.want {
			t.Fatalf("linearSlug(%q, %q) = %q, want %q", tc.identifier, tc.title, got, tc.want)
		}
	}
	// And it agrees with the shared slugifier rather than a private copy.
	want := strings.ToLower("ENG-7") + "-" + util.Slugify("A Title")
	if got := linearSlug("ENG-7", "A Title"); got != want {
		t.Fatalf("linearSlug = %q, want %q (util.Slugify's own output)", got, want)
	}
}

// Contract: ≤ 48 runes total, with no trailing "-" left by the truncation.
func TestContractLinearSlugBounded(t *testing.T) {
	long := "a title that runs on and on and on and on and on and on and on forever"
	got := linearSlug("ENG-142", long)
	if n := len([]rune(got)); n > 48 {
		t.Fatalf("linearSlug is %d runes (%q), want <= 48", n, got)
	}
	if strings.HasSuffix(got, "-") {
		t.Fatalf("linearSlug %q ends in a dash", got)
	}
}

// Contract: 0 is UNSET (empty, omitted from the metadata line); 1-4 map to
// Linear's own four labels.
func TestContractLinearPriorityLabel(t *testing.T) {
	cases := map[float64]string{0: "", 1: "Urgent", 2: "High", 3: "Normal", 4: "Low"}
	for p, want := range cases {
		if got := linearPriorityLabel(p); got != want {
			t.Fatalf("linearPriorityLabel(%v) = %q, want %q", p, got, want)
		}
	}
}
