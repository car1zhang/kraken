package jobs

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"kraken/internal/core"
	"kraken/internal/store"
	"kraken/internal/util"
)

// linear_edge_test.go — the EDGE dimension of the Linear intake pass:
// boundaries, refusals, failure arms, and adversarial ticket text. Written
// blind against 50-contract.md (R1-R12 plus the round-2 amendments A1-A10);
// every identifier declared here is prefixed `edge` because the logic author
// shares this package.
//
// One rule runs through nearly all of it. The pass has two watermarks, and
// each is a CLAIM: the cursor claims "every issue updated before this has
// been seen", and a row's last_event_at claims "every event up to this has
// been applied". Advancing either on an action that did not happen is
// unrecoverable and silent — the window never comes back, because neither
// watermark ever moves backwards. So every failure arm below asserts the same
// shape: the claim is NOT made, and the next poll can still make it. A5 made
// the two watermarks move together on a hold, which is why the held-event
// tests assert on the cursor as well as the stamp.
//
// Everything here drives runLinearPoll through the frozen linearSource seam
// with in-test fakes, and swaps the three side-effect seams (A2's
// linearCellLive, linearDeliverBump, A4's linearNotify) on every poll test,
// so no test in this file touches the network, tmux, or osascript.

// --- fakes ---

// edgeFakeSource is the scripted linearSource. Call counts are the assertion
// in several tests (a fetch that should never have happened, a URL downloaded
// twice), so every method counts itself.
type edgeFakeSource struct {
	issues      []core.LinearIssue
	comments    []core.LinearComment
	issuesErr   error
	commentsErr error
	downloadErr error

	issueCalls   int
	commentCalls int
	sinceSeen    []time.Time
	downloads    map[string]int

	// onComments runs inside AssignedCommentsSince, onDownload inside
	// DownloadFile — the two places a test can inject a concurrent writer.
	onComments func()
	onDownload func(url string)
}

func (f *edgeFakeSource) AssignedIssuesSince(since time.Time) ([]core.LinearIssue, error) {
	f.issueCalls++
	f.sinceSeen = append(f.sinceSeen, since)
	if f.issuesErr != nil {
		return nil, f.issuesErr
	}
	return f.issues, nil
}

func (f *edgeFakeSource) AssignedCommentsSince(since time.Time) ([]core.LinearComment, error) {
	f.commentCalls++
	if f.onComments != nil {
		f.onComments()
	}
	if f.commentsErr != nil {
		return nil, f.commentsErr
	}
	return f.comments, nil
}

func (f *edgeFakeSource) DownloadFile(url string) ([]byte, string, error) {
	if f.downloads == nil {
		f.downloads = map[string]int{}
	}
	f.downloads[url]++
	if f.onDownload != nil {
		f.onDownload(url)
	}
	if f.downloadErr != nil {
		return nil, "", f.downloadErr
	}
	return []byte("\x89PNG edge bytes"), "image/png", nil
}

func edgeCfg() core.LinearConfig {
	return core.LinearConfig{APIKey: "edge-key", DefaultRepo: "kraken", States: []string{"Today"}}
}

func edgeMkIssue(id, ident, title, desc, state string, updated time.Time) core.LinearIssue {
	return core.LinearIssue{
		ID: id, Identifier: ident, Title: title, Description: desc,
		URL:       "https://linear.app/edge/issue/" + ident,
		StateName: state, StateType: "started", TeamKey: "ENG", ProjectName: "Board",
		Priority: 2, Labels: []string{"bug"},
		CreatedAt: updated.Add(-time.Hour), UpdatedAt: updated,
	}
}

// edgeSeams swaps the pass' three side-effect seams at once. EVERY poll test
// installs it: the defaults drive tmux (A2's liveness probe, the deliver
// seam) and osascript (A4's notify), and a test suite must do neither.
type edgeSeams struct {
	bumpRet bool
	liveRet bool

	bumpCalls   int
	bumpMsgs    []string
	liveCalls   int
	notifyCalls int
	notifyMsgs  []string
}

func edgeStubSeams(t *testing.T, bumpRet, cellLive bool) *edgeSeams {
	t.Helper()
	s := &edgeSeams{bumpRet: bumpRet, liveRet: cellLive}
	prevBump, prevLive, prevNotify := linearDeliverBump, linearCellLive, linearNotify
	linearDeliverBump = func(j *Job, msg string) bool {
		s.bumpCalls++
		s.bumpMsgs = append(s.bumpMsgs, msg)
		return s.bumpRet
	}
	linearCellLive = func(j *Job) bool {
		s.liveCalls++
		return s.liveRet
	}
	linearNotify = func(title, msg string) {
		s.notifyCalls++
		s.notifyMsgs = append(s.notifyMsgs, title+" "+msg)
	}
	t.Cleanup(func() {
		linearDeliverBump, linearCellLive, linearNotify = prevBump, prevLive, prevNotify
	})
	return s
}

func edgeWriteSync(t *testing.T, st *store.Store, s LinearSync) {
	t.Helper()
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetKV(linearSyncKVKey, string(data)); err != nil {
		t.Fatal(err)
	}
}

func edgeReadSync(t *testing.T, st *store.Store) LinearSync {
	t.Helper()
	raw, err := st.GetKV(linearSyncKVKey)
	if err != nil {
		t.Fatalf("read %s: %v", linearSyncKVKey, err)
	}
	var s LinearSync
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatalf("kv row %q does not parse back into LinearSync: %v", raw, err)
	}
	return s
}

func edgeSeedRow(t *testing.T, st *store.Store, issueID, ident string, jobID, lastEvent int64) {
	t.Helper()
	if err := st.PutLinearIssue(&store.LinearIssueRow{
		IssueID: issueID, Identifier: ident, JobID: jobID,
		CreatedAt: lastEvent, LastEventAt: lastEvent,
	}); err != nil {
		t.Fatalf("seed linear_issues row: %v", err)
	}
}

func edgeRowOf(t *testing.T, st *store.Store, issueID string) *store.LinearIssueRow {
	t.Helper()
	r, err := st.GetLinearIssue(issueID)
	if err != nil {
		t.Fatalf("GetLinearIssue(%s): %v", issueID, err)
	}
	return r
}

func edgeBodyOf(t *testing.T, st *store.Store, slug string) string {
	t.Helper()
	j, err := st.GetJobBySlug(slug)
	if err != nil {
		t.Fatalf("GetJobBySlug(%s): %v", slug, err)
	}
	return j.Body
}

func edgeJobCount(t *testing.T, st *store.Store) int {
	t.Helper()
	all, err := st.ListJobs()
	if err != nil {
		t.Fatal(err)
	}
	return len(all)
}

// --- linearSlug ---

// The slug is a job's identity and a directory name (UploadsDir slugifies it
// again), so its edges are structural rather than cosmetic: a trailing dash
// from the truncation, a title that slugifies to nothing, a rune count
// measured in bytes.
func TestEdgeLinearSlugBounds(t *testing.T) {
	for _, tc := range []struct {
		name, ident, title string
	}{
		{"empty title", "ENG-142", ""},
		{"title is pure metacharacters", "ENG-142", `--- : {} [] & * | > # "" ''`},
		{"title is only punctuation", "ENG-142", "!!!???..."},
		{"title is non-latin", "ENG-142", "日本語のタイトル 🐙"},
		{"very long title", "ENG-142", strings.Repeat("fix the intermittent flake ", 20)},
		{"title truncating onto a dash", "ENG-1", strings.Repeat("ab ", 30)},
		{"mixed case identifier", "eNg-142", "Fix It"},
		{"leading punctuation title", "ENG-142", "  --- leading junk then Words"},
		// NOTE: an identifier that is not itself slug-safe ("ENG 142", "!!!")
		// is deliberately absent. The frozen formula LOWERCASES the identifier
		// and slugifies only the title, so such an identifier yields a slug
		// that is not a fixed point of Slugify — named as a contract silence
		// in the round-2 report rather than asserted against here. Linear's
		// own identifiers are `<TEAMKEY>-<n>` over alphanumerics, so nothing
		// upstream can produce one.
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := linearSlug(tc.ident, tc.title)

			if n := len([]rune(got)); n > 48 {
				t.Errorf("linearSlug(%q, %q) = %q (%d runes), want ≤ 48", tc.ident, tc.title, got, n)
			}
			if strings.HasSuffix(got, "-") {
				t.Errorf("linearSlug(%q, %q) = %q — a trailing dash survived the truncation", tc.ident, tc.title, got)
			}
			if strings.HasPrefix(got, "-") {
				t.Errorf("linearSlug(%q, %q) = %q — leading dash", tc.ident, tc.title, got)
			}
			if got == "" {
				t.Errorf("linearSlug(%q, %q) = \"\" — an empty slug names no job and no uploads dir", tc.ident, tc.title)
			}
			if got != strings.ToLower(got) {
				t.Errorf("linearSlug(%q, %q) = %q — not lowercased", tc.ident, tc.title, got)
			}
			// the slug has to be a FIXED POINT of Slugify: UploadsDir runs it
			// again, and a slug that changes under it puts the job's files in a
			// directory named after something else.
			if round := util.Slugify(got); round != got {
				t.Errorf("linearSlug(%q, %q) = %q, but Slugify of it is %q — not a stable job slug", tc.ident, tc.title, got, round)
			}
		})
	}
}

// The identifier prefix is what makes slugs unique per issue, so it must
// survive whatever the title does — including a title long enough to want the
// whole budget.
func TestEdgeLinearSlugKeepsTheIdentifierPrefix(t *testing.T) {
	got := linearSlug("ENG-142", strings.Repeat("z", 400))
	if !strings.HasPrefix(got, "eng-142") {
		t.Fatalf("linearSlug = %q, want the lowercased identifier as its prefix", got)
	}
	// two issues sharing a title must not share a slug
	a := linearSlug("ENG-1", "Fix the thing")
	b := linearSlug("ENG-2", "Fix the thing")
	if a == b {
		t.Fatalf("ENG-1 and ENG-2 both slugify to %q — the identifier prefix is the uniqueness", a)
	}
}

// --- linearPriorityLabel ---

// Linear's priority is a float on the wire and 0 means UNSET, not "urgent".
// Anything outside 0-4 has no label; none of it may panic or render a
// number into the job's metadata line.
func TestEdgeLinearPriorityLabelOutOfRange(t *testing.T) {
	for _, tc := range []struct {
		in   float64
		want string
	}{
		{0, ""}, {1, "Urgent"}, {2, "High"}, {3, "Normal"}, {4, "Low"},
		{5, ""}, {-1, ""}, {99, ""}, {1.5, ""}, {2.0001, ""}, {-0.0, ""},
	} {
		if got := linearPriorityLabel(tc.in); got != tc.want {
			t.Errorf("linearPriorityLabel(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// --- composeLinearBody ---

// Ticket text is attacker-adjacent input: carl's colleagues write it, and it
// lands verbatim in a file kraken then parses as frontmatter + body. Every
// shape below has to survive ParseJob with NO Err — an erred job pins to
// draft and never starts — and none may steal the frontmatter.
func TestEdgeComposeLinearBodyParsesForAdversarialIssues(t *testing.T) {
	hostileComment := core.LinearComment{
		ID: "c1", Author: "---\njob: evil", Body: "---\nrepo: not-kraken\nready: true\n---\n\n# heading\n",
		CreatedAt: time.Now().Add(-time.Hour), UpdatedAt: time.Now(),
	}
	for _, tc := range []struct {
		name, title, desc string
		comments          []core.LinearComment
	}{
		{name: "empty description", title: "Fix the thing"},
		{name: "whitespace-only description", title: "Fix the thing", desc: "   \n\n\t\n"},
		{name: "empty title", desc: "a description"},
		{name: "both empty"},
		{name: "title is a yaml fence", title: "---", desc: "body"},
		{name: "title is yaml metacharacters", title: `job: pwned # {} [] & * | > "quoted" 'single'`, desc: "body"},
		{name: "title with a colon", title: "Bug: the thing: broken", desc: "body"},
		{name: "description opens with a fence", title: "T", desc: "---\njob: evil\nready: true\nstate: done\n---\n\nreal text\n"},
		{name: "description holds bare fences", title: "T", desc: "before\n---\nmiddle\n---\nafter\n"},
		{name: "description holds frontmatter keys", title: "T", desc: "job: evil\nrepo: not-kraken\nready: true\nprereqs:\n  - job: other\n"},
		{name: "description holds a code fence", title: "T", desc: "```yaml\njob: evil\n---\n```\n"},
		{name: "description holds control characters", title: "T", desc: "a\x00b\x1b[31mred\x1b[0m\n"},
		{name: "description holds CRLF", title: "T", desc: "line one\r\nline two\r\n"},
		{name: "description is enormous", title: "T", desc: strings.Repeat("long line of ticket prose. ", 2000)},
		{name: "title holds a newline", title: "one\ntwo", desc: "body"},
		{name: "title holds a tab", title: "one\ttwo", desc: "body"},
		{name: "hostile comment", title: "T", desc: "body", comments: []core.LinearComment{hostileComment}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			iss := edgeMkIssue("iss-1", "ENG-1", tc.title, tc.desc, "Today", time.Now())
			iss.Comments = tc.comments
			slug := linearSlug(iss.Identifier, iss.Title)
			cfg := edgeCfg()

			body := composeLinearBody(iss, cfg, nil)

			parsed := ParseJob(slug+JobSuffix, []byte(body))
			if parsed.Err != "" {
				t.Fatalf("composed body does not parse (%s):\n%s", parsed.Err, body)
			}
			if parsed.Slug != slug {
				t.Errorf("parsed slug = %q, want %q — the ticket text moved the job's identity", parsed.Slug, slug)
			}
			if parsed.Repo != cfg.DefaultRepo {
				t.Errorf("parsed repo = %q, want %q (A1: the repo line renders cfg.DefaultRepo)", parsed.Repo, cfg.DefaultRepo)
			}
			if parsed.State != "" {
				t.Errorf("parsed state = %q, want empty — a Linear job lands as a draft (Q1) and ticket text must not pre-start it", parsed.State)
			}
			// FRONTMATTER only: these fixtures are adversarial ticket text and
			// several carry the words in the description on purpose.
			if key := contractFrontmatterRetiredKey(body); key != "" {
				t.Errorf("composed frontmatter carries a retired %q key:\n%s", key, body)
			}
			if strings.TrimSpace(parsed.Body) == "" {
				t.Errorf("parsed body is empty — SubmitJob would refuse it as an empty spec:\n%s", body)
			}
		})
	}
}

// The description is the ticket, and the ticket is the point: its own line
// structure is preserved byte-for-byte, including the shapes a re-wrapper
// would eat.
func TestEdgeComposeLinearBodyKeepsDescriptionVerbatim(t *testing.T) {
	for _, desc := range []string{
		"    indented four spaces\n        and eight\n",
		"trailing spaces   \nand a tab\t\n",
		"blank lines\n\n\n\nbetween paragraphs\n",
		"```go\nfunc main() {\n\tfmt.Println(\"---\")\n}\n```\n",
		"a single very long unwrapped line " + strings.Repeat("x", 500),
		"| a | b |\n|---|---|\n| 1 | 2 |\n",
		"![shot](https://uploads.linear.app/abc/def/shot.png)\n",
	} {
		iss := edgeMkIssue("iss-1", "ENG-1", "Title", desc, "Today", time.Now())

		body := composeLinearBody(iss, edgeCfg(), nil)

		if !strings.Contains(body, desc) {
			t.Errorf("description was not preserved byte-for-byte.\nwant substring:\n%q\ngot body:\n%s", desc, body)
		}
	}
}

// Media is best-effort; the ticket text is not. A failed download keeps the
// ORIGINAL Linear URL in the body with a note, so the image is one click away
// rather than silently absent — and it never emits a media line pointing at
// the empty path it does not have.
func TestEdgeComposeLinearBodyFailedMediaKeepsTheURL(t *testing.T) {
	const failedURL = "https://uploads.linear.app/aaa/bbb/broken.png"
	iss := edgeMkIssue("iss-1", "ENG-1", "Title", "see ![shot]("+failedURL+")", "Today", time.Now())
	media := []linearMediaRef{
		{URL: failedURL, Err: errors.New("403 signature expired")},
		{URL: "https://uploads.linear.app/aaa/ccc/ok.png", Path: "/tmp/edge/ok.png", ContentType: "image/png"},
		{URL: "https://uploads.linear.app/aaa/ddd/spec.pdf", Path: "/tmp/edge/spec.pdf", ContentType: "application/pdf"},
	}

	body := composeLinearBody(iss, edgeCfg(), media)

	if !strings.Contains(body, failedURL) {
		t.Errorf("a failed download dropped its URL from the body — the image is now unreachable:\n%s", body)
	}
	if strings.Contains(body, "[image: ]") || strings.Contains(body, "[image: \n") {
		t.Errorf("emitted an empty media line for a download that produced no path:\n%s", body)
	}
	if !strings.Contains(body, "[image: /tmp/edge/ok.png]") {
		t.Errorf("the downloaded image has no [image: <abs path>] line:\n%s", body)
	}
	if !strings.Contains(body, "/tmp/edge/spec.pdf") {
		t.Errorf("the downloaded non-image file has no path line:\n%s", body)
	}
	if strings.Contains(body, "[image: /tmp/edge/spec.pdf]") {
		t.Errorf("a pdf was rendered as an image line — Claude Code would try to ingest it:\n%s", body)
	}
	// and the whole thing still parses
	if p := ParseJob(linearSlug("ENG-1", "Title")+JobSuffix, []byte(body)); p.Err != "" {
		t.Fatalf("body with media does not parse (%s):\n%s", p.Err, body)
	}
}

// --- runLinearPoll: the cursor ---

// R2. A garbage kv row must reset and behave as R1 — the dream-watermark
// rule. The only writer of this key sits downstream of the gate, so treating
// garbage as fatal would kill the pass permanently and in silence.
func TestEdgeRunLinearPollResetsUnparseableCursor(t *testing.T) {
	for _, raw := range []string{"", "not json", "{", `{"cursor_ms":`, `["array"]`, `{"cursor_ms":"a string"}`} {
		t.Run(fmt.Sprintf("%q", raw), func(t *testing.T) {
			st := useTestStore(t)
			edgeStubSeams(t, true, false)
			if err := st.SetKV(linearSyncKVKey, raw); err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			src := &edgeFakeSource{issues: []core.LinearIssue{
				edgeMkIssue("iss-1", "ENG-1", "Should not be created", "d", "Today", now),
			}}

			if err := runLinearPoll(st, src, edgeCfg(), now); err != nil {
				t.Fatalf("runLinearPoll over a corrupt cursor errored: %v — it must reset, not die", err)
			}

			if src.issueCalls != 0 {
				t.Errorf("fetched issues %d times on the reset poll — R2 behaves as R1, which fetches nothing", src.issueCalls)
			}
			if n := edgeJobCount(t, st); n != 0 {
				t.Errorf("created %d jobs on the reset poll, want 0 (no backfill — Q6)", n)
			}
			got := edgeReadSync(t, st)
			if got.CursorMS != now.UnixMilli() {
				t.Errorf("cursor = %d after the reset, want now (%d) — a seed of 0 would backfill the whole history next poll", got.CursorMS, now.UnixMilli())
			}
			if got.LastPollMS != now.UnixMilli() {
				t.Errorf("last_poll = %d, want now (%d)", got.LastPollMS, now.UnixMilli())
			}

			// and the very next poll actually works: "self-repairing" has to be
			// a behaviour, not a comment.
			later := now.Add(10 * time.Minute)
			if err := runLinearPoll(st, src, edgeCfg(), later); err != nil {
				t.Fatalf("the poll after a reset errored: %v", err)
			}
			if src.issueCalls != 1 {
				t.Errorf("the poll after a reset fetched %d times, want 1 — the pass stayed dead", src.issueCalls)
			}
		})
	}
}

// R11's failure arm, and the whole watermark discipline in one test: an error
// pins the CURSOR (the window is re-read next poll, never marked swept
// unread) while LastPollMS still advances (retry on the next interval, not on
// every 15-second tick).
func TestEdgeRunLinearPollFetchErrorPinsCursorAndAdvancesLastPoll(t *testing.T) {
	boom := errors.New("linear: 503 service unavailable")
	for _, tc := range []struct {
		name string
		src  *edgeFakeSource
	}{
		{"issues fetch fails", &edgeFakeSource{issuesErr: boom}},
		{"comments fetch fails", &edgeFakeSource{commentsErr: boom}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := useTestStore(t)
			edgeStubSeams(t, true, false)
			base := time.Now().Add(-time.Hour)
			edgeWriteSync(t, st, LinearSync{CursorMS: base.UnixMilli(), LastPollMS: base.UnixMilli()})
			now := time.Now()

			err := runLinearPoll(st, tc.src, edgeCfg(), now)

			if err == nil {
				t.Fatal("a failed fetch returned a nil error — a failed read is not an empty result")
			}
			got := edgeReadSync(t, st)
			if got.CursorMS != base.UnixMilli() {
				t.Errorf("cursor moved to %d on a failed fetch, want it pinned at %d — that window would never be read again",
					got.CursorMS, base.UnixMilli())
			}
			if got.LastPollMS != now.UnixMilli() {
				t.Errorf("last_poll = %d, want now (%d) — otherwise the failure retries on every tick", got.LastPollMS, now.UnixMilli())
			}
			if !hasEvent(t, st, "linear-sync-error") {
				t.Error("no linear-sync-error event — a fetch failure that logs nothing is a silent blind spot")
			}
			if n := edgeJobCount(t, st); n != 0 {
				t.Errorf("created %d jobs off a failed fetch", n)
			}
		})
	}
}

// The cursor never moves BACKWARDS. Every poll re-reads the overlap margin,
// so a window whose newest issue is older than the stored cursor is the
// normal case, not a strange one — and rewinding to it would re-read the same
// margin from further and further back, forever.
func TestEdgeRunLinearPollCursorNeverGoesBackwards(t *testing.T) {
	st := useTestStore(t)
	edgeStubSeams(t, true, false)
	cursor := time.Now().Add(-5 * time.Minute)
	edgeWriteSync(t, st, LinearSync{CursorMS: cursor.UnixMilli(), LastPollMS: cursor.UnixMilli()})
	// an issue replayed out of the overlap margin: known already (a recorded
	// skip, so nothing is held), and older than the cursor
	old := cursor.Add(-30 * time.Second)
	edgeSeedRow(t, st, "iss-1", "ENG-1", 0, old.UnixMilli())
	src := &edgeFakeSource{issues: []core.LinearIssue{
		edgeMkIssue("iss-1", "ENG-1", "Old news", "d", "Today", old),
	}}

	if err := runLinearPoll(st, src, edgeCfg(), time.Now()); err != nil {
		t.Fatalf("runLinearPoll: %v", err)
	}

	if got := edgeReadSync(t, st).CursorMS; got < cursor.UnixMilli() {
		t.Fatalf("cursor rewound from %d to %d", cursor.UnixMilli(), got)
	}
}

// R3, from the edge side: the fetch floor is the cursor MINUS the overlap, so
// the margin is re-read on purpose. A floor at the bare cursor would drop
// anything Linear stamped in the second the last poll was reading.
func TestEdgeRunLinearPollFetchesFromCursorMinusOverlap(t *testing.T) {
	st := useTestStore(t)
	edgeStubSeams(t, true, false)
	cursor := time.Now().Add(-time.Hour)
	edgeWriteSync(t, st, LinearSync{CursorMS: cursor.UnixMilli(), LastPollMS: cursor.UnixMilli()})
	src := &edgeFakeSource{}

	if err := runLinearPoll(st, src, edgeCfg(), time.Now()); err != nil {
		t.Fatalf("runLinearPoll: %v", err)
	}

	if len(src.sinceSeen) == 0 {
		t.Fatal("no fetch happened at all")
	}
	want := cursor.Add(-linearOverlap).UnixMilli()
	if got := src.sinceSeen[0].UnixMilli(); got != want {
		t.Fatalf("fetched since %d, want cursor − overlap = %d", got, want)
	}
}

// --- runLinearPoll: creation refusals ---

// R6 (as corrected by A7). A slug already taken by an unrelated job is
// recorded as a SKIP — a row with JobID 0 and a reason naming the slug, plus
// exactly one notification — and that is final. The two alternatives are both
// worse and both silent: retrying forever re-runs the collision every two
// minutes, and minting a suffixed slug produces a second job nobody asked for
// whose relationship to the first is invisible.
func TestEdgeRunLinearPollSlugCollisionRecordsSkipAndNeverRetries(t *testing.T) {
	st := useTestStore(t)
	seams := edgeStubSeams(t, true, false)
	slug := linearSlug("ENG-7", "Fix the thing")
	mustCreateJob(t, st, store.Job{Slug: slug, Repo: "kraken", Body: "an unrelated hand-made job"})
	now := time.Now()
	edgeWriteSync(t, st, LinearSync{CursorMS: now.Add(-time.Hour).UnixMilli(), LastPollMS: now.Add(-time.Hour).UnixMilli()})
	src := &edgeFakeSource{issues: []core.LinearIssue{
		edgeMkIssue("iss-7", "ENG-7", "Fix the thing", "ticket text", "Today", now),
	}}

	if err := runLinearPoll(st, src, edgeCfg(), now); err != nil {
		t.Fatalf("a slug collision failed the whole poll: %v — it is a recorded skip, not an error", err)
	}

	if n := edgeJobCount(t, st); n != 1 {
		all, _ := st.ListJobs()
		t.Fatalf("board holds %d jobs after a collision, want 1 (no suffixed second job): %+v", n, all)
	}
	if got := edgeBodyOf(t, st, slug); got != "an unrelated hand-made job" {
		t.Errorf("the colliding job's body was overwritten: %q", got)
	}
	row := edgeRowOf(t, st, "iss-7")
	if row.JobID != 0 {
		t.Errorf("skip row JobID = %d, want 0", row.JobID)
	}
	if !strings.Contains(row.SkipReason, slug) {
		t.Errorf("SkipReason = %q, want it to name the taken slug %q", row.SkipReason, slug)
	}
	if seams.notifyCalls != 1 {
		t.Errorf("fired %d notifications for one skip, want exactly 1 (A4/R6)", seams.notifyCalls)
	}

	// the next poll re-reads the same issue out of the overlap margin and must
	// do nothing at all — no error, no second row, no second job, no second
	// notification.
	if err := runLinearPoll(st, src, edgeCfg(), now.Add(2*time.Minute)); err != nil {
		t.Fatalf("the poll after a recorded skip errored: %v — the issue is retrying", err)
	}
	if n := edgeJobCount(t, st); n != 1 {
		t.Fatalf("board holds %d jobs after the replay, want 1", n)
	}
	if seams.notifyCalls != 1 {
		t.Errorf("the replay fired another notification (%d total) — the skip is being re-processed", seams.notifyCalls)
	}
}

// R5's negative half. The state filter is an EQUALITY on the name, ignoring
// case — never a substring match, or "Today" would swallow "Not Today" and
// every state a team invents containing the word. A non-matching issue gets
// no row at all, so a later transition into a matching state still creates
// its job.
func TestEdgeRunLinearPollNonMatchingStateWritesNothing(t *testing.T) {
	st := useTestStore(t)
	edgeStubSeams(t, true, false)
	now := time.Now()
	edgeWriteSync(t, st, LinearSync{CursorMS: now.Add(-time.Hour).UnixMilli(), LastPollMS: now.Add(-time.Hour).UnixMilli()})
	src := &edgeFakeSource{issues: []core.LinearIssue{
		edgeMkIssue("iss-1", "ENG-1", "a", "d", "Todays", now),
		edgeMkIssue("iss-2", "ENG-2", "b", "d", "Not Today", now),
		edgeMkIssue("iss-3", "ENG-3", "c", "d", "Backlog", now),
		edgeMkIssue("iss-4", "ENG-4", "d", "d", "", now),
	}}

	if err := runLinearPoll(st, src, edgeCfg(), now); err != nil {
		t.Fatalf("runLinearPoll: %v", err)
	}

	if n := edgeJobCount(t, st); n != 0 {
		all, _ := st.ListJobs()
		t.Fatalf("created %d jobs from non-matching states, want 0: %+v", n, all)
	}
	for _, id := range []string{"iss-1", "iss-2", "iss-3", "iss-4"} {
		if _, err := st.GetLinearIssue(id); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s has a linear_issues row (%v) — R5 leaves no row, so a later transition into a matching state still creates its job", id, err)
		}
	}
}

// A10. An explicitly empty state configuration is HONOURED: nothing matches,
// nothing is created. The alternative — silently falling back to ["Today"] —
// would ignore what the operator wrote and start filing jobs they had just
// switched off.
func TestEdgeRunLinearPollEmptyStatesCreatesNothing(t *testing.T) {
	st := useTestStore(t)
	edgeStubSeams(t, true, false)
	now := time.Now()
	edgeWriteSync(t, st, LinearSync{CursorMS: now.Add(-time.Hour).UnixMilli(), LastPollMS: now.Add(-time.Hour).UnixMilli()})
	cfg := edgeCfg()
	cfg.States = nil
	src := &edgeFakeSource{issues: []core.LinearIssue{
		edgeMkIssue("iss-1", "ENG-1", "Fix the thing", "d", "Today", now),
	}}

	if err := runLinearPoll(st, src, cfg, now); err != nil {
		t.Fatalf("runLinearPoll: %v", err)
	}

	if n := edgeJobCount(t, st); n != 0 {
		t.Errorf("created %d jobs with nil States, want 0 (A10)", n)
	}
	if _, err := st.GetLinearIssue("iss-1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("wrote a row with nil States (%v) — R5's no-row rule applies", err)
	}
}

// The overlap margin replays issues by design, so the SECOND sight of an
// issue is the common case rather than the exception: one job, one row, one
// download, one notification, whatever happens.
func TestEdgeRunLinearPollReplayIsIdempotent(t *testing.T) {
	st := useTestStore(t)
	seams := edgeStubSeams(t, true, false)
	now := time.Now()
	edgeWriteSync(t, st, LinearSync{CursorMS: now.Add(-time.Hour).UnixMilli(), LastPollMS: now.Add(-time.Hour).UnixMilli()})
	const url = "https://uploads.linear.app/aaa/bbb/shot.png"
	src := &edgeFakeSource{issues: []core.LinearIssue{
		edgeMkIssue("iss-1", "ENG-1", "Fix the thing", "see ![shot]("+url+")", "Today", now),
	}}
	slug := linearSlug("ENG-1", "Fix the thing")

	for i := 0; i < 3; i++ {
		if err := runLinearPoll(st, src, edgeCfg(), now.Add(time.Duration(i)*2*time.Minute)); err != nil {
			t.Fatalf("poll %d: %v", i+1, err)
		}
	}

	if n := edgeJobCount(t, st); n != 1 {
		all, _ := st.ListJobs()
		t.Fatalf("three polls of one issue produced %d jobs, want 1: %+v", n, all)
	}
	if _, err := st.GetJobBySlug(slug); err != nil {
		t.Fatalf("the created job is not under the expected slug %q: %v", slug, err)
	}
	rows, err := st.ListLinearIssues()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("three polls produced %d dedup rows, want 1", len(rows))
	}
	if n := src.downloads[url]; n != 1 {
		t.Errorf("downloaded %s %d times across three polls, want 1", url, n)
	}
	if seams.notifyCalls != 1 {
		t.Errorf("fired %d notifications across three polls of one issue, want exactly 1 (A4/R4)", seams.notifyCalls)
	}
}

// A URL that appears twice in one ticket (a markdown image and a bare link to
// the same file, which is what pasting a screenshot then referencing it
// produces) downloads ONCE.
func TestEdgeRunLinearPollDownloadsADuplicateURLOnce(t *testing.T) {
	st := useTestStore(t)
	edgeStubSeams(t, true, false)
	now := time.Now()
	edgeWriteSync(t, st, LinearSync{CursorMS: now.Add(-time.Hour).UnixMilli(), LastPollMS: now.Add(-time.Hour).UnixMilli()})
	const url = "https://uploads.linear.app/aaa/bbb/shot.png"
	desc := "![shot](" + url + ")\n\nand again, as a link: " + url + "\n\nand once more ![shot](" + url + ")\n"
	src := &edgeFakeSource{issues: []core.LinearIssue{
		edgeMkIssue("iss-1", "ENG-1", "Dupe media", desc, "Today", now),
	}}

	if err := runLinearPoll(st, src, edgeCfg(), now); err != nil {
		t.Fatalf("runLinearPoll: %v", err)
	}

	if n := src.downloads[url]; n != 1 {
		t.Fatalf("downloaded %s %d times, want 1 — linearMediaRef is one per UNIQUE url", url, n)
	}
}

// R4's degrade arm. Media is best-effort and the ticket text is not: a
// download that fails end to end still yields a job, still parses, and still
// carries the URL so the image is one click away.
func TestEdgeRunLinearPollMediaFailureStillCreatesTheJob(t *testing.T) {
	st := useTestStore(t)
	edgeStubSeams(t, true, false)
	now := time.Now()
	edgeWriteSync(t, st, LinearSync{CursorMS: now.Add(-time.Hour).UnixMilli(), LastPollMS: now.Add(-time.Hour).UnixMilli()})
	const url = "https://uploads.linear.app/aaa/bbb/shot.png"
	src := &edgeFakeSource{
		downloadErr: errors.New("403 signature expired"),
		issues: []core.LinearIssue{
			edgeMkIssue("iss-1", "ENG-1", "Fix the thing", "see ![shot]("+url+")", "Today", now),
		},
	}
	slug := linearSlug("ENG-1", "Fix the thing")

	if err := runLinearPoll(st, src, edgeCfg(), now); err != nil {
		t.Fatalf("a failed media download failed the whole poll: %v", err)
	}

	body := edgeBodyOf(t, st, slug)
	if !strings.Contains(body, url) {
		t.Errorf("the failed download's URL is not in the body — the image is unreachable now:\n%s", body)
	}
	row := edgeRowOf(t, st, "iss-1")
	if row.JobID == 0 {
		t.Errorf("row = %+v, want a real job id — media failure must not turn a creation into a skip", row)
	}
}

// A ticket with nothing in it is a real shape (a bot-filed issue, a template
// nobody completed). Whatever the pass decides to do with it, it must not
// leave behind a job with an empty slug: UploadsDir refuses one, and the
// board renders it as a nameless card that cannot be selected.
//
// The stronger assertion this test carried in round 1 — that every created
// slug is a fixed point of `util.Slugify` — was dropped in round-2
// arbitration for the reason spelled out on TestEdgeLinearSlugBounds: the
// frozen `linearSlug` formula does not slugify the identifier, so the
// punctuation-identifier case below legitimately produces `!!!`. Contract
// silence, reported, not asserted.
func TestEdgeRunLinearPollHostileIssueMakesNoBrokenJob(t *testing.T) {
	st := useTestStore(t)
	edgeStubSeams(t, true, false)
	now := time.Now()
	edgeWriteSync(t, st, LinearSync{CursorMS: now.Add(-time.Hour).UnixMilli(), LastPollMS: now.Add(-time.Hour).UnixMilli()})
	src := &edgeFakeSource{issues: []core.LinearIssue{
		edgeMkIssue("iss-1", "", "", "", "Today", now),
		edgeMkIssue("iss-2", "!!!", "???", "", "Today", now),
		edgeMkIssue("", "ENG-3", "No id at all", "d", "Today", now),
	}}

	if err := runLinearPoll(st, src, edgeCfg(), now); err != nil {
		t.Logf("poll reported: %v (an error here is a legitimate answer)", err)
	}

	all, err := st.ListJobs()
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range all {
		if strings.TrimSpace(j.Slug) == "" {
			t.Fatalf("created a job with an empty slug (id %d) — UploadsDir refuses it and the board cannot select it", j.ID)
		}
	}
}

// --- runLinearPoll: the update path (Q5) ---

// R7's dead-job arm. An archived or hard-deleted job is not resurrected and
// its events are not held forever: the stamp advances so the issue goes quiet,
// and nothing else moves.
func TestEdgeRunLinearPollDeadJobStampsWithoutResurrecting(t *testing.T) {
	t.Run("archived", func(t *testing.T) {
		st := useTestStore(t)
		seams := edgeStubSeams(t, true, true)
		base := time.Now().Add(-2 * time.Hour)
		job := mustCreateJob(t, st, store.Job{Slug: "eng-1-old", Repo: "kraken", Body: "ORIGINAL"})
		if _, err := st.ArchiveJob(job.ID); err != nil {
			t.Fatalf("ArchiveJob: %v", err)
		}
		edgeSeedRow(t, st, "iss-1", "ENG-1", job.ID, base.UnixMilli())
		event := base.Add(time.Hour)
		edgeWriteSync(t, st, LinearSync{CursorMS: base.UnixMilli(), LastPollMS: base.UnixMilli()})
		src := &edgeFakeSource{issues: []core.LinearIssue{
			edgeMkIssue("iss-1", "ENG-1", "Old", "new description", "Today", event),
		}}

		if err := runLinearPoll(st, src, edgeCfg(), time.Now()); err != nil {
			t.Fatalf("runLinearPoll: %v", err)
		}

		if got := edgeRowOf(t, st, "iss-1").LastEventAt; got != event.UnixMilli() {
			t.Errorf("stamp = %d, want %d — an event against a dead job must go quiet, not retry forever", got, event.UnixMilli())
		}
		if seams.bumpCalls != 0 {
			t.Errorf("bumped a pane %d times for an archived job", seams.bumpCalls)
		}
		if n := edgeJobCount(t, st); n != 0 {
			t.Errorf("the archived job came back to the active board (%d active jobs)", n)
		}
		back, err := st.GetJobByID(job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if back.Body != "ORIGINAL" {
			t.Errorf("the archived job's body was rewritten: %q", back.Body)
		}
		if back.ArchivedAt == 0 {
			t.Error("the job was un-archived by an incoming Linear event")
		}
	})

	t.Run("hard-deleted", func(t *testing.T) {
		st := useTestStore(t)
		seams := edgeStubSeams(t, true, true)
		base := time.Now().Add(-2 * time.Hour)
		job := mustCreateJob(t, st, store.Job{Slug: "eng-2-gone", Repo: "kraken", Body: "ORIGINAL"})
		edgeSeedRow(t, st, "iss-2", "ENG-2", job.ID, base.UnixMilli())
		if err := st.DeleteJob(job.ID); err != nil {
			t.Fatal(err)
		}
		event := base.Add(time.Hour)
		edgeWriteSync(t, st, LinearSync{CursorMS: base.UnixMilli(), LastPollMS: base.UnixMilli()})
		src := &edgeFakeSource{issues: []core.LinearIssue{
			edgeMkIssue("iss-2", "ENG-2", "Gone", "new description", "Today", event),
		}}

		if err := runLinearPoll(st, src, edgeCfg(), time.Now()); err != nil {
			t.Fatalf("runLinearPoll over a hard-deleted job errored: %v", err)
		}

		if got := edgeRowOf(t, st, "iss-2").LastEventAt; got != event.UnixMilli() {
			t.Errorf("stamp = %d, want %d", got, event.UnixMilli())
		}
		if seams.bumpCalls != 0 {
			t.Errorf("bumped a pane %d times for a deleted job", seams.bumpCalls)
		}
		if n := edgeJobCount(t, st); n != 0 {
			t.Errorf("a deleted job was re-created by an incoming event (%d active jobs)", n)
		}
	})
}

// R10, on A2's seam. A started job whose cell is not live HOLDS the event: no
// bump (the deliver seam is not even called), no body write, no stamp — and
// per A5, no cursor advance either, since the cursor is the only thing that
// can bring the event back.
//
// The deliver seam is stubbed to return TRUE on purpose: a delivery that
// would have succeeded must still not be attempted, because there is nothing
// live to attempt it against.
func TestEdgeRunLinearPollStartedCellNotLiveHolds(t *testing.T) {
	st := useTestStore(t)
	seams := edgeStubSeams(t, true, false) // cell NOT live
	base := time.Now().Add(-2 * time.Hour)
	job := mustCreateJob(t, st, store.Job{
		Slug: "eng-3-running", Repo: "kraken", Body: "ORIGINAL SPEC", State: "working",
	})
	edgeSeedRow(t, st, "iss-3", "ENG-3", job.ID, base.UnixMilli())
	event := base.Add(time.Hour)
	edgeWriteSync(t, st, LinearSync{CursorMS: base.UnixMilli(), LastPollMS: base.UnixMilli()})
	src := &edgeFakeSource{issues: []core.LinearIssue{
		edgeMkIssue("iss-3", "ENG-3", "Running", "an edited description", "Today", event),
	}}

	if err := runLinearPoll(st, src, edgeCfg(), time.Now()); err != nil {
		t.Fatalf("a hold reported an error: %v — A5: a hold is the design working, not a failure", err)
	}

	if got := edgeRowOf(t, st, "iss-3").LastEventAt; got != base.UnixMilli() {
		t.Errorf("stamp advanced to %d with no live cell to deliver into — that event is now lost", got)
	}
	if got := edgeBodyOf(t, st, "eng-3-running"); got != "ORIGINAL SPEC" {
		t.Errorf("a started job's body was appended to:\n%s", got)
	}
	if seams.bumpCalls != 0 {
		t.Errorf("called the deliver seam %d times for a cell that is not live (A2: R10 does not call it)", seams.bumpCalls)
	}
	if got := edgeReadSync(t, st).CursorMS; got != base.UnixMilli() {
		t.Errorf("cursor advanced to %d past a held event (A5) — the hold can never be revisited", got)
	}
}

// R8, both arms, on A2's seam — the most destructive thing in this feature to
// get wrong, because stamping a delivery that never happened loses the event
// permanently and the board looks identical either way.
//
// Poll 1 delivers and the seam reports FALSE: nothing stamped, cursor held.
// Poll 2 delivers again and reports TRUE: stamped to the newest event, cursor
// released. Poll 3 replays the same event out of the overlap margin: no third
// delivery.
func TestEdgeRunLinearPollDeliversAndStampsOnlyOnTrue(t *testing.T) {
	st := useTestStore(t)
	seams := edgeStubSeams(t, false, true) // cell live, delivery failing
	base := time.Now().Add(-2 * time.Hour)
	job := mustCreateJob(t, st, store.Job{
		Slug: "eng-4-running", Repo: "kraken", Body: "ORIGINAL SPEC", State: "working",
	})
	edgeSeedRow(t, st, "iss-4", "ENG-4", job.ID, base.UnixMilli())
	event := base.Add(time.Hour)
	edgeWriteSync(t, st, LinearSync{CursorMS: base.UnixMilli(), LastPollMS: base.UnixMilli()})
	src := &edgeFakeSource{issues: []core.LinearIssue{
		edgeMkIssue("iss-4", "ENG-4", "Running", "an edited description", "Today", event),
	}}

	// --- the failed delivery ---
	if err := runLinearPoll(st, src, edgeCfg(), time.Now()); err != nil {
		t.Fatalf("poll 1: %v", err)
	}
	if seams.bumpCalls != 1 {
		t.Fatalf("delivered %d times to a live cell, want 1", seams.bumpCalls)
	}
	if got := edgeRowOf(t, st, "iss-4").LastEventAt; got != base.UnixMilli() {
		t.Fatalf("stamped %d off a delivery that returned false — the event is gone", got)
	}
	if got := edgeReadSync(t, st).CursorMS; got != base.UnixMilli() {
		t.Errorf("cursor advanced to %d past an undelivered event (A5)", got)
	}
	if got := edgeBodyOf(t, st, "eng-4-running"); got != "ORIGINAL SPEC" {
		t.Fatalf("wrote into a STARTED job's body:\n%s", got)
	}

	// --- the retry that lands ---
	seams.bumpRet = true
	if err := runLinearPoll(st, src, edgeCfg(), time.Now().Add(2*time.Minute)); err != nil {
		t.Fatalf("poll 2: %v", err)
	}
	if seams.bumpCalls != 2 {
		t.Fatalf("the retry delivered %d times in total, want 2 — a false return must be retried", seams.bumpCalls)
	}
	if got := edgeRowOf(t, st, "iss-4").LastEventAt; got != event.UnixMilli() {
		t.Fatalf("stamp = %d after a true return, want %d", got, event.UnixMilli())
	}
	if got := edgeReadSync(t, st).CursorMS; got < event.UnixMilli() {
		t.Errorf("cursor = %d after everything was delivered, want it released to at least %d (A5)", got, event.UnixMilli())
	}
	// the message is the point of the delivery: it has to say which issue.
	if len(seams.bumpMsgs) > 0 && !strings.Contains(seams.bumpMsgs[len(seams.bumpMsgs)-1], "ENG-4") {
		t.Errorf("bump message does not name the issue (R8): %q", seams.bumpMsgs[len(seams.bumpMsgs)-1])
	}

	// --- the replay ---
	if err := runLinearPoll(st, src, edgeCfg(), time.Now().Add(4*time.Minute)); err != nil {
		t.Fatalf("poll 3: %v", err)
	}
	if seams.bumpCalls != 2 {
		t.Fatalf("a replayed event delivered again (%d total) — the stamp is not guarding it", seams.bumpCalls)
	}
}

// R9. A not-started job takes the update in its BODY, appended — never a
// rewrite, since the body is the human-authored spec and the job has not run
// yet. Then the stamp guards it: the same event replayed out of the overlap
// margin must not append a second copy, and a genuinely newer event must.
func TestEdgeRunLinearPollDraftAppendsNeverRewrites(t *testing.T) {
	st := useTestStore(t)
	edgeStubSeams(t, true, false)
	base := time.Now().Add(-3 * time.Hour)
	// the decoy heading is deliberate: the append must not be implemented by
	// finding and replacing an existing section.
	const original = "ORIGINAL SPEC TEXT\n\n## Linear update (2020-01-01T00:00:00Z)\n\na decoy the ticket author wrote\n"
	job := mustCreateJob(t, st, store.Job{Slug: "eng-5-draft", Repo: "kraken", Body: original})
	edgeSeedRow(t, st, "iss-5", "ENG-5", job.ID, base.UnixMilli())
	edgeWriteSync(t, st, LinearSync{CursorMS: base.UnixMilli(), LastPollMS: base.UnixMilli()})

	event := base.Add(time.Hour)
	src := &edgeFakeSource{issues: []core.LinearIssue{
		edgeMkIssue("iss-5", "ENG-5", "Draft", "the description was edited", "Today", event),
	}}

	pollNow := time.Now()
	if err := runLinearPoll(st, src, edgeCfg(), pollNow); err != nil {
		t.Fatalf("first poll: %v", err)
	}
	body := edgeBodyOf(t, st, "eng-5-draft")
	if !strings.Contains(body, "ORIGINAL SPEC TEXT") {
		t.Fatalf("the original spec was rewritten away:\n%s", body)
	}
	if !strings.Contains(body, original) {
		t.Errorf("the original body is no longer intact — R9 appends, never rewrites:\n%s", body)
	}
	if n := strings.Count(body, "## Linear update ("); n != 2 {
		t.Fatalf("body holds %d `## Linear update (` headings after one event, want 2 (the decoy plus one appended):\n%s", n, body)
	}
	// A9: the heading's timestamp is the POLL's now, UTC RFC3339 — not the
	// event's, and not local time.
	if want := "## Linear update (" + pollNow.UTC().Format(time.RFC3339) + ")"; !strings.Contains(body, want) {
		t.Errorf("no %q heading in the body (A9):\n%s", want, body)
	}
	if got := edgeRowOf(t, st, "iss-5").LastEventAt; got != event.UnixMilli() {
		t.Errorf("stamp = %d after a successful append, want %d", got, event.UnixMilli())
	}

	// the same event again, out of the overlap margin: nothing.
	if err := runLinearPoll(st, src, edgeCfg(), time.Now().Add(2*time.Minute)); err != nil {
		t.Fatalf("replay poll: %v", err)
	}
	if n := strings.Count(edgeBodyOf(t, st, "eng-5-draft"), "## Linear update ("); n != 2 {
		t.Fatalf("a replayed event appended a second copy (%d headings) — the stamp is not guarding it", n)
	}

	// a genuinely newer event does append again.
	newer := base.Add(2 * time.Hour)
	src.issues = []core.LinearIssue{
		edgeMkIssue("iss-5", "ENG-5", "Draft", "edited again", "Today", newer),
	}
	if err := runLinearPoll(st, src, edgeCfg(), time.Now().Add(4*time.Minute)); err != nil {
		t.Fatalf("third poll: %v", err)
	}
	if n := strings.Count(edgeBodyOf(t, st, "eng-5-draft"), "## Linear update ("); n != 3 {
		t.Fatalf("a newer event produced %d headings, want 3:\n%s", n, edgeBodyOf(t, st, "eng-5-draft"))
	}
}

// R9's rev-conflict arm, asserted through the invariant it exists to protect
// rather than through the timing that produces it. A concurrent editor save
// lands between this poll's read of the job and its write; whichever of the
// two wins, the outcome must be the same: the editor's text survives, and the
// Linear section appears EXACTLY once — never lost (stamped without being
// written) and never doubled (written twice because the stamp did not
// advance).
func TestEdgeRunLinearPollDraftAppendSurvivesAConcurrentEdit(t *testing.T) {
	st := useTestStore(t)
	edgeStubSeams(t, true, false)
	base := time.Now().Add(-3 * time.Hour)
	job := mustCreateJob(t, st, store.Job{Slug: "eng-6-draft", Repo: "kraken", Body: "ORIGINAL SPEC TEXT\n"})
	edgeSeedRow(t, st, "iss-6", "ENG-6", job.ID, base.UnixMilli())
	edgeWriteSync(t, st, LinearSync{CursorMS: base.UnixMilli(), LastPollMS: base.UnixMilli()})

	event := base.Add(time.Hour)
	src := &edgeFakeSource{issues: []core.LinearIssue{
		edgeMkIssue("iss-6", "ENG-6", "Draft", "the description was edited", "Today", event),
	}}
	// the editor: a read-modify-write at the row's CURRENT rev, exactly what a
	// board save does, fired from inside the poll.
	src.onComments = func() {
		cur, err := st.GetJobByID(job.ID)
		if err != nil {
			t.Errorf("concurrent editor could not read the job: %v", err)
			return
		}
		if _, err := st.UpdateJobFields(cur.ID, cur.Rev, map[string]any{
			"body": cur.Body + "\nEDITOR ADDED THIS\n",
		}); err != nil {
			t.Errorf("concurrent editor save: %v", err)
		}
	}

	if err := runLinearPoll(st, src, edgeCfg(), time.Now()); err != nil {
		t.Logf("poll under a concurrent edit reported: %v (a rev conflict is a legitimate error to report)", err)
	}
	src.onComments = nil
	if err := runLinearPoll(st, src, edgeCfg(), time.Now().Add(2*time.Minute)); err != nil {
		t.Fatalf("the retry poll errored: %v", err)
	}

	body := edgeBodyOf(t, st, "eng-6-draft")
	if !strings.Contains(body, "ORIGINAL SPEC TEXT") {
		t.Errorf("the original spec is gone:\n%s", body)
	}
	if !strings.Contains(body, "EDITOR ADDED THIS") {
		t.Errorf("the concurrent editor's save was clobbered by the append:\n%s", body)
	}
	if n := strings.Count(body, "## Linear update ("); n != 1 {
		t.Fatalf("body holds %d `## Linear update (` headings, want exactly 1 — a rev conflict must lose neither the section nor duplicate it:\n%s", n, body)
	}
	if got := edgeRowOf(t, st, "iss-6").LastEventAt; got != event.UnixMilli() {
		t.Errorf("stamp = %d after the retry landed, want %d", got, event.UnixMilli())
	}
}

// --- LinearPass: the gate (A3's seam) ---

// edgePollStub swaps A3's linearPoll seam, so the cadence can be driven
// without a network client.
type edgePollStub struct {
	calls atomic.Int32
	fired chan struct{}
	block chan struct{}
}

func edgeStubPoll(t *testing.T) *edgePollStub {
	t.Helper()
	s := &edgePollStub{fired: make(chan struct{}, 8)}
	prev := linearPoll
	linearPoll = func(st *store.Store, src linearSource, cfg core.LinearConfig, now time.Time) error {
		s.calls.Add(1)
		s.fired <- struct{}{}
		if s.block != nil {
			<-s.block
		}
		return nil
	}
	t.Cleanup(func() { linearPoll = prev })
	return s
}

func (s *edgePollStub) waitFired(t *testing.T) {
	t.Helper()
	select {
	case <-s.fired:
	case <-time.After(2 * time.Second):
		t.Fatal("the poll never fired")
	}
}

// edgeConfigureKey makes the feature look ON for a LinearPass test: a key in
// the process env (HOME is already sandboxed by useTestStore, so there is no
// linear.env to read).
func edgeConfigureKey(t *testing.T) {
	t.Helper()
	t.Setenv("LINEAR_API_KEY", "edge-key")
	t.Setenv("LINEAR_DEFAULT_REPO", "kraken")
	t.Setenv("LINEAR_STATES", "Today")
}

// edgeFuture returns a `now` far enough ahead that the pass' in-memory
// backstop — which is process-global and outlives a test, exactly like
// lastDreamDate — cannot be what blocks this call. Each LinearPass test below
// takes a distinct, increasing offset for the same reason.
func edgeFuture(days int) time.Time {
	return time.Now().Add(time.Duration(days) * 24 * time.Hour)
}

// No key configured is the normal state of a fresh install, and it is
// SILENT: no error, no log, no kv row, no poll, no goroutine.
func TestEdgeLinearPassIsSilentWithoutAKey(t *testing.T) {
	st := useTestStore(t) // sandboxes HOME, so there is no ~/.kraken/linear.env
	for _, k := range []string{"LINEAR_API_KEY", "LINEAR_DEFAULT_REPO", "LINEAR_STATES"} {
		t.Setenv(k, "")
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}
	edgeStubSeams(t, true, false)
	poll := edgeStubPoll(t)

	LinearPass(edgeFuture(400))
	LinearPass(edgeFuture(401))

	if n := poll.calls.Load(); n != 0 {
		t.Errorf("polled %d times with no key configured — the feature is off, not idling", n)
	}
	if _, err := st.GetKV(linearSyncKVKey); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the pass wrote %s with no key configured (err %v)", linearSyncKVKey, err)
	}
	if hasEvent(t, st, "linear-sync-error") {
		t.Error("logged an error for an unconfigured feature — a missing key is not a failure")
	}

	// and with no store wired at all it must not panic either: `k v` and the
	// viewer run this package with ActiveStore() nil.
	UseStore(nil)
	LinearPass(edgeFuture(402))
	if n := poll.calls.Load(); n != 0 {
		t.Errorf("polled %d times with no store wired", n)
	}
}

// The stored gate: a LastPollMS inside the interval is NOT due. This is the
// half of the two-gate throttle that survives a restart, so it is the one
// that actually holds the cadence at one poll per interval.
func TestEdgeLinearPassThrottlesOnTheStoredWatermark(t *testing.T) {
	st := useTestStore(t)
	edgeConfigureKey(t)
	edgeStubSeams(t, true, false)
	poll := edgeStubPoll(t)
	now := edgeFuture(410)
	edgeWriteSync(t, st, LinearSync{CursorMS: now.UnixMilli(), LastPollMS: now.UnixMilli()})

	LinearPass(now)
	LinearPass(now.Add(linearPollInterval / 2))

	if n := poll.calls.Load(); n != 0 {
		t.Fatalf("polled %d times inside the interval, want 0 — the stored watermark is the throttle", n)
	}

	LinearPass(now.Add(linearPollInterval + time.Second))
	poll.waitFired(t)
	if n := poll.calls.Load(); n != 1 {
		t.Fatalf("polled %d times after the interval elapsed, want 1", n)
	}
}

// An ABSENT row is genuinely due — a fresh install must poll rather than wait
// for a watermark only a poll can write.
func TestEdgeLinearPassAbsentWatermarkIsDue(t *testing.T) {
	useTestStore(t)
	edgeConfigureKey(t)
	edgeStubSeams(t, true, false)
	poll := edgeStubPoll(t)

	LinearPass(edgeFuture(420))
	poll.waitFired(t)

	if n := poll.calls.Load(); n != 1 {
		t.Fatalf("polled %d times with no watermark at all, want 1", n)
	}
}

// The readDreamRun inversion: a store that cannot ANSWER degrades to "not
// due". Being wrong the other way costs a poll of an external API every
// 15-second tick, forever, on a machine that is already unwell.
func TestEdgeLinearPassUnreadableStoreIsNotDue(t *testing.T) {
	st := useTestStore(t)
	edgeConfigureKey(t)
	edgeStubSeams(t, true, false)
	poll := edgeStubPoll(t)
	// a closed handle is the cheapest real "the store cannot answer" — every
	// query returns an error rather than a row.
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	LinearPass(edgeFuture(430))
	LinearPass(edgeFuture(431))

	if n := poll.calls.Load(); n != 0 {
		t.Fatalf("polled %d times against a store that cannot answer, want 0 — a broken store must not poll every tick", n)
	}
}

// A present-but-UNPARSEABLE row is a different case from an unreadable one,
// and it must be DUE: R2 puts the reset inside the poll, so a throttle that
// treated garbage as "not due" would leave the only writer of that key
// permanently unreachable — the loop dead, in silence, with nothing able to
// repair it (the exact readDreamRun failure the contract cites).
func TestEdgeLinearPassGarbageWatermarkIsDue(t *testing.T) {
	st := useTestStore(t)
	edgeConfigureKey(t)
	edgeStubSeams(t, true, false)
	poll := edgeStubPoll(t)
	if err := st.SetKV(linearSyncKVKey, "not json"); err != nil {
		t.Fatal(err)
	}

	LinearPass(edgeFuture(440))
	poll.waitFired(t)

	if n := poll.calls.Load(); n != 1 {
		t.Fatalf("polled %d times over a corrupt watermark, want 1 — R2's reset lives inside the poll and is unreachable otherwise", n)
	}
}

// Two due calls with the first still in flight produce ONE poll: neither
// watermark has moved yet (the poll is what writes it), so the CAS is the
// only thing left to stop the second.
func TestEdgeLinearPassSingleFlights(t *testing.T) {
	useTestStore(t)
	edgeConfigureKey(t)
	edgeStubSeams(t, true, false)
	poll := edgeStubPoll(t)
	poll.block = make(chan struct{})

	done := make(chan struct{})
	go func() {
		defer close(done)
		LinearPass(edgeFuture(450))
	}()
	poll.waitFired(t) // the first poll is inside the stub, holding the flag
	LinearPass(edgeFuture(451))
	close(poll.block)
	<-done

	if n := poll.calls.Load(); n != 1 {
		t.Fatalf("polled %d times with one already in flight, want exactly 1", n)
	}
}

// The poll interval and the overlap have to stay in a sane relationship: the
// overlap is a margin INSIDE the interval, and an overlap at or above it
// would re-read every window whole, doubling the work and making the dedup
// row the only thing standing between the board and duplicate jobs.
func TestEdgeLinearOverlapIsSmallerThanTheInterval(t *testing.T) {
	if linearOverlap <= 0 {
		t.Fatalf("linearOverlap = %s, want a positive margin — without one a slow write is dropped", linearOverlap)
	}
	if linearOverlap >= linearPollInterval {
		t.Fatalf("linearOverlap (%s) >= linearPollInterval (%s) — every window would be re-read whole", linearOverlap, linearPollInterval)
	}
}
