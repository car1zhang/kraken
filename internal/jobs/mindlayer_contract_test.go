package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"kraken/internal/store"
)

// mindlayer_contract_test.go — athena-logic's BLIND contract tests for
// artifacts/50-contract.md (PR 2, the mental-model rework), slice B (jobs):
// mindThreadFor, mindSeed/mindCaptureMessage, threadEnvPrefix,
// threadSpawnCommand's prefix, threadBrief per role, CommitMMItem,
// ReviseMMItem, FlagMMItem / RaiseMMFlag / DismissMMFlag / ResolveMMFlag, the
// notes block's pending-flag source, RenderMMFiles, renderMMIntoWorktree,
// ZeusTranscriptBlocks' empty case and the zeus brief's ladder.
//
// Written from the frozen contract alone; no non-test file under internal/ was
// read by any route. Dimension: LOGIC — stated invariants and happy paths.
// Refusals and boundaries are athena-edge's (`edgeML…`).
//
// Every package-level identifier here is prefixed `contractML`.

// contractMLEnv is the Test environment obligation.
func contractMLEnv(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	for _, k := range []string{"KRAKEN_DB_PATH", "KRAKEN_THREAD_SESSION", "KRAKEN_THREAD_ROLE"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
}

// contractMLStore opens a scratch store as the package's active store.
func contractMLStore(t *testing.T) *store.Store {
	t.Helper()
	contractMLEnv(t)
	st, err := store.Open(filepath.Join(t.TempDir(), "kraken.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	UseStore(st)
	t.Cleanup(func() { UseStore(nil); st.Close() })
	return st
}

// contractMLNotify swaps the notifyFn seam and counts calls.
type contractMLNotify struct {
	mu   sync.Mutex
	msgs []string
}

func (n *contractMLNotify) count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.msgs)
}

func contractMLSwapNotify(t *testing.T) *contractMLNotify {
	t.Helper()
	n := &contractMLNotify{}
	prev := notifyFn
	notifyFn = func(title, msg string) {
		n.mu.Lock()
		n.msgs = append(n.msgs, title+": "+msg)
		n.mu.Unlock()
	}
	t.Cleanup(func() { notifyFn = prev })
	return n
}

// contractMLRepo makes `<HOME>/Developer/<name>` look like a checkout so
// ResolveRepo accepts the name.
func contractMLRepo(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(os.Getenv("HOME"), "Developer", name)
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatalf("fixture repo: %v", err)
	}
	return dir
}

// contractMLJobWithThread creates a working job stamped with lead session
// `lead`, and one thread row of `role`/`state` carrying `threadSession`.
func contractMLJobWithThread(t *testing.T, st *store.Store, slug, lead, threadSession, role, state string) int64 {
	t.Helper()
	id, err := st.CreateJob(&store.Job{Slug: slug, Repo: "kraken", State: "working", Session: lead})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if threadSession != "" {
		if _, err := st.AddThreadNext(store.Thread{
			JobID: id, Title: "mind", SessionID: threadSession, ParentSession: lead,
			ForkUUID: "fork-uuid-1", State: state, Role: role,
		}); err != nil {
			t.Fatalf("AddThreadNext: %v", err)
		}
	}
	return id
}

// ---------------------------------------------------------------------------
// mindThreadFor
// ---------------------------------------------------------------------------

// "Returns the live `mind` thread whose `TurnUUID == zeusUUID`, else nil; an
// `ask` thread never matches; a closed thread never matches; an empty
// `zeusUUID` never matches." (Amendment 13 moved the key from ForkUUID to
// TurnUUID; the lead edited the field name here, sighted, nothing else.)
func TestMindLayerMindThreadForMatches(t *testing.T) {
	threads := []store.Thread{
		{TID: "t1", Role: "mind", State: "closed", TurnUUID: "u-now"},
		{TID: "t2", Role: "ask", State: "live", TurnUUID: "u-now"},
		{TID: "t3", Role: "mind", State: "live", TurnUUID: "u-old"},
		{TID: "t4", Role: "mind", State: "live", TurnUUID: "u-now"},
		{TID: "t5", Role: "mind", State: "live", TurnUUID: ""},
	}
	got := mindThreadFor(threads, "u-now")
	if got == nil || got.TID != "t4" {
		t.Fatalf("mindThreadFor(u-now) = %+v, want t4 (the live mind thread at this turn)", got)
	}
	if g := mindThreadFor(threads, "u-old"); g == nil || g.TID != "t3" {
		t.Errorf("mindThreadFor(u-old) = %+v, want t3", g)
	}
	if g := mindThreadFor(threads, "u-newer"); g != nil {
		t.Errorf("mindThreadFor(u-newer) = %+v, want nil (no thread forked at this turn)", g)
	}
	if g := mindThreadFor(threads, ""); g != nil {
		t.Errorf("mindThreadFor(\"\") = %+v, want nil", g)
	}
	// only an ask thread and a closed mind thread at the uuid → nil
	if g := mindThreadFor(threads[:2], "u-now"); g != nil {
		t.Errorf("mindThreadFor over {closed mind, live ask} = %+v, want nil", g)
	}
	if g := mindThreadFor(nil, "u-now"); g != nil {
		t.Errorf("mindThreadFor(nil) = %+v, want nil", g)
	}
}

// ---------------------------------------------------------------------------
// mindSeed / mindCaptureMessage
// ---------------------------------------------------------------------------

// "Both contain the quoted text verbatim and the source serialized as JSON; the
// seed additionally states the item contract (a `title` ≤ 120 runes ..., `scope`
// `universal` or `repo` + `dir`, `kind` ∈ decision/reading/note, `body` ≤ 8,000
// runes, "finalize" → `kraken mm commit` with the JSON on stdin)".
func TestMindLayerMindSeedCarriesQuoteAndSource(t *testing.T) {
	req := CaptureRequest{
		Slug: "seed-job",
		Text: "The daemon, not zeus, watches CI — and \"bumps\" the pane.",
		Source: CaptureSource{
			Kind: "artifact", Slug: "seed-job", Home: "cell", Name: "50-components.md", SHA: "abc123",
		},
	}
	src, err := json.Marshal(req.Source)
	if err != nil {
		t.Fatalf("marshal source: %v", err)
	}
	seed := mindSeed(req)
	msg := mindCaptureMessage(req)
	for label, s := range map[string]string{"mindSeed": seed, "mindCaptureMessage": msg} {
		if !strings.Contains(s, req.Text) {
			t.Errorf("%s lacks the quoted text verbatim:\n%s", label, s)
		}
		if !strings.Contains(s, string(src)) {
			t.Errorf("%s lacks the source serialized as JSON %s:\n%s", label, src, s)
		}
	}
	for _, want := range []string{"title", "120", "scope", "universal", "repo", "dir", "kind",
		"decision", "reading", "note", "body", "finalize", "kraken mm commit"} {
		if !strings.Contains(seed, want) {
			t.Errorf("mindSeed lacks %q (the item contract):\n%s", want, seed)
		}
	}
}

// ---------------------------------------------------------------------------
// threadEnvPrefix / threadSpawnCommand
// ---------------------------------------------------------------------------

// "Returns `KRAKEN_THREAD_SESSION='<session>' KRAKEN_THREAD_ROLE='<role>' `
// (single-quoted values, trailing space); `""` when session is empty."
func TestMindLayerThreadEnvPrefix(t *testing.T) {
	if got, want := threadEnvPrefix("sess-123", "mind"), "KRAKEN_THREAD_SESSION='sess-123' KRAKEN_THREAD_ROLE='mind' "; got != want {
		t.Errorf("threadEnvPrefix = %q, want %q", got, want)
	}
	if got, want := threadEnvPrefix("sess-9", "ask"), "KRAKEN_THREAD_SESSION='sess-9' KRAKEN_THREAD_ROLE='ask' "; got != want {
		t.Errorf("threadEnvPrefix = %q, want %q", got, want)
	}
	if got := threadEnvPrefix("", "mind"); got != "" {
		t.Errorf("threadEnvPrefix(\"\", mind) = %q, want \"\"", got)
	}
}

// "The returned command string begins with `accountEnvPrefix(j) +
// threadEnvPrefix(session, role)`".
func TestMindLayerThreadSpawnCommandBeginsWithPrefixes(t *testing.T) {
	contractMLEnv(t)
	j := &Job{Slug: "spawn-prefix"}
	if err := os.MkdirAll(cellDir(j), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"mind", "ask"} {
		sess := "11111111-2222-4333-8444-55555555555" + strconv.Itoa(len(role))
		cmd, err := threadSpawnCommand(j, "t1", "a title", "seed text", "", role, sess,
			[]string{"--session-id", sess, "--fork-session", "-r", "zeus-sess"})
		if err != nil {
			t.Fatalf("threadSpawnCommand(%s): %v", role, err)
		}
		want := accountEnvPrefix(j) + threadEnvPrefix(sess, role)
		if !strings.HasPrefix(cmd, want) {
			t.Errorf("threadSpawnCommand(%s) = %q, want prefix %q", role, cmd, want)
		}
	}
	// revive form (no fork): still carries the thread's session and role.
	cmd, err := threadSpawnCommand(j, "t2", "a title", "", "", "mind", "thread-sess", []string{"-r", "thread-sess"})
	if err != nil {
		t.Fatalf("threadSpawnCommand revive: %v", err)
	}
	if want := accountEnvPrefix(j) + threadEnvPrefix("thread-sess", "mind"); !strings.HasPrefix(cmd, want) {
		t.Errorf("revive command = %q, want prefix %q", cmd, want)
	}
}

// ---------------------------------------------------------------------------
// threadBrief
// ---------------------------------------------------------------------------

// "For `role == "mind"` the text names `kraken mm commit`, `kraken mm revise`,
// `kraken mm show`, `kraken mm get` ... for `role == "ask"` it is the
// pre-existing brief (names none of `mm commit`/`mm revise`)."
func TestMindLayerThreadBriefPerRole(t *testing.T) {
	contractMLEnv(t)
	j := &Job{Slug: "brief-role"}
	mind := threadBrief(j, "t1", "a mind title", "/tmp/wt", "mind")
	for _, want := range []string{"kraken mm commit", "kraken mm revise", "kraken mm show", "kraken mm get"} {
		if !strings.Contains(mind, want) {
			t.Errorf("mind brief lacks %q:\n%s", want, mind)
		}
	}
	ask := threadBrief(j, "t1", "an ask title", "/tmp/wt", "ask")
	for _, banned := range []string{"mm commit", "mm revise"} {
		if strings.Contains(ask, banned) {
			t.Errorf("ask brief names %q:\n%s", banned, ask)
		}
	}
	if !strings.Contains(ask, "an ask title") {
		t.Errorf("ask brief lost its title:\n%s", ask)
	}
	if mind == ask {
		t.Errorf("mind and ask briefs are identical")
	}
}

// ---------------------------------------------------------------------------
// CommitMMItem / ReviseMMItem
// ---------------------------------------------------------------------------

func contractMLCommit() MMCommit {
	return MMCommit{
		Title: "the daemon, not zeus, watches CI",
		Scope: "repo", Repo: "kraken", Dir: "internal/jobs",
		Kind:   "decision",
		Body:   "kraken serve's PR tracker bumps zeus on failing checks.",
		Quote:  "the daemon, not zeus, watches CI",
		Source: CaptureSource{Kind: "artifact", Slug: "commit-job", Home: "cell", Name: "00-overview.md", SHA: "deadbeef"},
	}
}

// "`CommitMMItem`: Gate first: `threadSession` must name a thread row with
// `Role == "mind"` and `State == "live"` ... Creates the item with `Source =
// "capture"`, `JobID`/`Slug` of the thread's job, `Content = Body`, `SourceJSON`
// = the source's JSON, `ThreadSession = threadSession`, `CreatedAt = now`".
func TestMindLayerCommitMMItemCreatesFromLiveMindThread(t *testing.T) {
	st := contractMLStore(t)
	contractMLRepo(t, "kraken")
	jobID := contractMLJobWithThread(t, st, "commit-job", "zeus-sess", "mind-sess", "mind", "live")
	c := contractMLCommit()
	id, err := CommitMMItem(st, c, "mind-sess")
	if err != nil {
		t.Fatalf("CommitMMItem: %v", err)
	}
	it, err := st.GetMMItem(id)
	if err != nil {
		t.Fatalf("GetMMItem: %v", err)
	}
	if it.Source != "capture" {
		t.Errorf("Source = %q, want capture", it.Source)
	}
	if it.JobID != jobID || it.Slug != "commit-job" {
		t.Errorf("job/slug = %d/%q, want %d/commit-job (the thread's job)", it.JobID, it.Slug, jobID)
	}
	if it.Content != c.Body || it.Title != c.Title || it.Kind != c.Kind || it.Quote != c.Quote {
		t.Errorf("item = %+v, want content/title/kind/quote from %+v", it, c)
	}
	if it.Scope != "repo" || it.Repo != "kraken" || it.Dir != "internal/jobs" {
		t.Errorf("scope/repo/dir = %q/%q/%q", it.Scope, it.Repo, it.Dir)
	}
	if it.ThreadSession != "mind-sess" {
		t.Errorf("ThreadSession = %q, want mind-sess", it.ThreadSession)
	}
	if it.CreatedAt <= 0 {
		t.Errorf("CreatedAt = %d, want now", it.CreatedAt)
	}
	var src CaptureSource
	if err := json.Unmarshal([]byte(it.SourceJSON), &src); err != nil {
		t.Fatalf("SourceJSON %q is not JSON: %v", it.SourceJSON, err)
	}
	if src != c.Source {
		t.Errorf("SourceJSON decodes to %+v, want %+v", src, c.Source)
	}
	if it.InvalidatedAt != 0 {
		t.Errorf("a fresh commit is invalidated")
	}
}

// "`universal` forces `Repo = Dir = ""`".
func TestMindLayerCommitUniversalClearsRepoAndDir(t *testing.T) {
	st := contractMLStore(t)
	contractMLRepo(t, "kraken")
	contractMLJobWithThread(t, st, "commit-uni", "zeus-sess", "mind-uni", "mind", "live")
	c := contractMLCommit()
	c.Scope = "universal"
	id, err := CommitMMItem(st, c, "mind-uni")
	if err != nil {
		t.Fatalf("CommitMMItem universal: %v", err)
	}
	it, err := st.GetMMItem(id)
	if err != nil {
		t.Fatalf("GetMMItem: %v", err)
	}
	if it.Scope != "universal" || it.Repo != "" || it.Dir != "" {
		t.Errorf("universal item scope/repo/dir = %q/%q/%q, want universal/''/''", it.Scope, it.Repo, it.Dir)
	}
}

// The gate's positive/negative pair on the same body: only the live mind
// thread's session commits — "`threadSession` must name a thread row with
// `Role == "mind"` and `State == "live"` ... else `ErrNotMindThread`".
func TestMindLayerCommitGateIsTheMindThreadSession(t *testing.T) {
	st := contractMLStore(t)
	contractMLRepo(t, "kraken")
	contractMLJobWithThread(t, st, "commit-gate", "zeus-gate", "mind-gate", "mind", "live")
	if _, err := CommitMMItem(st, contractMLCommit(), "zeus-gate"); !errors.Is(err, ErrNotMindThread) {
		t.Errorf("commit from zeus' own session err = %v, want ErrNotMindThread", err)
	}
	if _, err := CommitMMItem(st, contractMLCommit(), "mind-gate"); err != nil {
		t.Errorf("commit from the live mind thread: %v", err)
	}
}

// "`ReviseMMItem`: Same gate and validation as commit; the item must exist and
// be live ...; applies `store.ReviseMMItem` with `At = now`".
func TestMindLayerReviseMMItemFromMindThread(t *testing.T) {
	st := contractMLStore(t)
	contractMLRepo(t, "kraken")
	contractMLJobWithThread(t, st, "revise-job", "zeus-rev", "mind-rev", "mind", "live")
	id, err := CommitMMItem(st, contractMLCommit(), "mind-rev")
	if err != nil {
		t.Fatalf("CommitMMItem: %v", err)
	}
	before, _ := st.GetMMItem(id)
	c := contractMLCommit()
	c.Title = "revised: the tracker bumps zeus"
	c.Body = "revised body"
	c.Quote = "revised quote"
	c.Dir = "internal/api"
	if err := ReviseMMItem(st, id, c, "mind-rev"); err != nil {
		t.Fatalf("ReviseMMItem: %v", err)
	}
	it, err := st.GetMMItem(id)
	if err != nil {
		t.Fatalf("GetMMItem: %v", err)
	}
	if it.Title != c.Title || it.Content != c.Body || it.Quote != c.Quote || it.Dir != "internal/api" {
		t.Errorf("revised item = %+v, want %+v", it, c)
	}
	if it.RevisedAt <= 0 {
		t.Errorf("RevisedAt = %d, want now", it.RevisedAt)
	}
	if it.CreatedAt != before.CreatedAt || it.SourceJSON != before.SourceJSON || it.ThreadSession != before.ThreadSession {
		t.Errorf("revise moved created_at/source_json/thread_session: before %+v after %+v", before, it)
	}
}

// ---------------------------------------------------------------------------
// flags
// ---------------------------------------------------------------------------

func contractMLItem(t *testing.T, st *store.Store, content string) int64 {
	t.Helper()
	id, err := st.AddMMItem(store.MMItem{Kind: "note", Source: "manual", Content: content, CreatedAt: 1})
	if err != nil {
		t.Fatalf("AddMMItem: %v", err)
	}
	return id
}

// "the row is created `raised` with `RaisedBy = session`, `RaisedAt = now` when
// `session` equals the job's stamped lead session ..., else `pending`; a
// `raised` creation fires one `notify`".
func TestMindLayerFlagMMItemRaisedVsPendingByLeadSession(t *testing.T) {
	st := contractMLStore(t)
	n := contractMLSwapNotify(t)
	jobID := contractMLJobWithThread(t, st, "flag-job", "zeus-flag", "", "", "")
	item := contractMLItem(t, st, "the old belief")
	against := contractMLItem(t, st, "the newer belief")

	team, err := FlagMMItem(st, MMFlagRequest{Slug: "flag-job", ItemID: item, AgainstID: against, Note: "contradicts the newer one"}, "athena-sess")
	if err != nil {
		t.Fatalf("FlagMMItem (teammate): %v", err)
	}
	if team.State != "pending" || team.RaisedBy != "" || team.RaisedAt != 0 {
		t.Errorf("teammate flag = %+v, want pending and unstamped", team)
	}
	if team.JobID != jobID || team.Slug != "flag-job" || team.ItemID != item || team.AgainstID != against ||
		team.Note != "contradicts the newer one" || team.ID <= 0 {
		t.Errorf("teammate flag fields = %+v", team)
	}
	if n.count() != 0 {
		t.Errorf("a pending creation notified %d times, want 0", n.count())
	}

	lead, err := FlagMMItem(st, MMFlagRequest{Slug: "flag-job", ItemID: item, Note: "zeus saw it too"}, "zeus-flag")
	if err != nil {
		t.Fatalf("FlagMMItem (lead): %v", err)
	}
	if lead.State != "raised" || lead.RaisedBy != "zeus-flag" || lead.RaisedAt <= 0 {
		t.Errorf("lead flag = %+v, want raised by zeus-flag, stamped", lead)
	}
	if n.count() != 1 {
		t.Errorf("a raised creation notified %d times, want exactly 1", n.count())
	}
	stored, err := st.GetMMFlag(lead.ID)
	if err != nil || stored.State != "raised" {
		t.Errorf("stored lead flag = %+v, %v", stored, err)
	}
}

// "a flag with no slug and no items is still accepted with `JobID 0`".
func TestMindLayerFlagNoteOnlyAccepted(t *testing.T) {
	st := contractMLStore(t)
	contractMLSwapNotify(t)
	f, err := FlagMMItem(st, MMFlagRequest{Note: "the transcript contradicts mm"}, "some-sess")
	if err != nil {
		t.Fatalf("FlagMMItem note-only: %v", err)
	}
	if f.JobID != 0 || f.ItemID != 0 || f.AgainstID != 0 || f.State != "pending" {
		t.Errorf("note-only flag = %+v, want JobID 0, no items, pending", f)
	}
}

// "`RaiseMMFlag`/`DismissMMFlag`: `session` must equal the lead session of the
// flag's job ...; raise fires one `notify`".
func TestMindLayerRaiseAndDismissByLead(t *testing.T) {
	st := contractMLStore(t)
	n := contractMLSwapNotify(t)
	contractMLJobWithThread(t, st, "raise-job", "zeus-raise", "", "", "")
	item := contractMLItem(t, st, "x")
	a, err := FlagMMItem(st, MMFlagRequest{Slug: "raise-job", ItemID: item, Note: "a"}, "team-1")
	if err != nil {
		t.Fatalf("FlagMMItem a: %v", err)
	}
	b, err := FlagMMItem(st, MMFlagRequest{Slug: "raise-job", ItemID: item, Note: "b"}, "team-2")
	if err != nil {
		t.Fatalf("FlagMMItem b: %v", err)
	}
	if err := RaiseMMFlag(st, a.ID, "zeus-raise", "verified against the transcript"); err != nil {
		t.Fatalf("RaiseMMFlag: %v", err)
	}
	fa, _ := st.GetMMFlag(a.ID)
	if fa.State != "raised" || fa.RaisedBy != "zeus-raise" || fa.RaiseNote != "verified against the transcript" || fa.RaisedAt <= 0 {
		t.Errorf("raised flag = %+v", fa)
	}
	if n.count() != 1 {
		t.Errorf("raise notified %d times, want 1", n.count())
	}
	if err := DismissMMFlag(st, b.ID, "zeus-raise", "not a contradiction"); err != nil {
		t.Fatalf("DismissMMFlag: %v", err)
	}
	fb, _ := st.GetMMFlag(b.ID)
	if fb.State != "dismissed" || fb.RaiseNote != "not a contradiction" {
		t.Errorf("dismissed flag = %+v", fb)
	}
	if n.count() != 1 {
		t.Errorf("dismiss notified (count %d), want still 1", n.count())
	}
	if err := RaiseMMFlag(st, b.ID, "team-2", ""); err == nil {
		t.Errorf("a teammate raised a flag")
	}
}

// "`ResolveMMFlag`: `drop` resolves the flag AND invalidates its `ItemID` (when
// non-zero and live) with reason `contradicted` and detail `flag #<id>: <note>`;
// `keep` resolves only".
func TestMindLayerResolveDropInvalidatesContradicted(t *testing.T) {
	st := contractMLStore(t)
	contractMLSwapNotify(t)
	contractMLJobWithThread(t, st, "resolve-job", "zeus-res", "", "", "")
	dropItem := contractMLItem(t, st, "to be dropped")
	keepItem := contractMLItem(t, st, "to be kept")
	bystander := contractMLItem(t, st, "untouched")

	fd, err := FlagMMItem(st, MMFlagRequest{Slug: "resolve-job", ItemID: dropItem, AgainstID: bystander, Note: "wrong since PR 2"}, "zeus-res")
	if err != nil {
		t.Fatalf("FlagMMItem drop: %v", err)
	}
	fk, err := FlagMMItem(st, MMFlagRequest{Slug: "resolve-job", ItemID: keepItem, Note: "maybe wrong"}, "zeus-res")
	if err != nil {
		t.Fatalf("FlagMMItem keep: %v", err)
	}
	if err := ResolveMMFlag(st, fd.ID, "drop"); err != nil {
		t.Fatalf("ResolveMMFlag drop: %v", err)
	}
	if err := ResolveMMFlag(st, fk.ID, "keep"); err != nil {
		t.Fatalf("ResolveMMFlag keep: %v", err)
	}
	gd, _ := st.GetMMFlag(fd.ID)
	if gd.Resolution != "drop" || gd.ResolvedAt <= 0 || gd.State != "resolved" {
		t.Errorf("dropped flag = %+v", gd)
	}
	gk, _ := st.GetMMFlag(fk.ID)
	if gk.Resolution != "keep" || gk.ResolvedAt <= 0 || gk.State != "resolved" {
		t.Errorf("kept flag = %+v", gk)
	}
	d, _ := st.GetMMItem(dropItem)
	if d.InvalidatedAt == 0 || d.InvalidReason != "contradicted" {
		t.Errorf("dropped item = %+v, want invalidated with reason contradicted", d)
	}
	if want := "flag #" + strconv.FormatInt(fd.ID, 10) + ": wrong since PR 2"; d.InvalidDetail != want {
		t.Errorf("InvalidDetail = %q, want %q", d.InvalidDetail, want)
	}
	if k, _ := st.GetMMItem(keepItem); k.InvalidatedAt != 0 {
		t.Errorf("keep invalidated its item: %+v", k)
	}
	if b, _ := st.GetMMItem(bystander); b.InvalidatedAt != 0 {
		t.Errorf("drop invalidated the against item too: %+v", b)
	}
}

// ---------------------------------------------------------------------------
// notes block
// ---------------------------------------------------------------------------

// "`flagNoteKey(f store.MMFlag) string` = `"m:" + strconv.FormatInt(f.ID, 10)`".
func TestMindLayerFlagNoteKey(t *testing.T) {
	if got := flagNoteKey(store.MMFlag{ID: 41}); got != "m:41" {
		t.Errorf("flagNoteKey = %q, want m:41", got)
	}
}

// "For the job's lead session, `Recall` (UserPromptSubmit) lists each `pending`
// flag of that job once, as a line carrying the flag id, the item id(s) and the
// note ...; raised/dismissed flags are never listed; teammates/threads still get
// no notes."
func TestMindLayerNotesBlockListsPendingFlagOnce(t *testing.T) {
	st := contractMLStore(t)
	contractMLSwapNotify(t)
	const slug, lead = "notes-flag", "zeus-notes"
	contractMLJobWithThread(t, st, slug, lead, "", "", "")
	// pad ids so the flag and item ids are distinctive numbers
	for i := 0; i < 11; i++ {
		contractMLItem(t, st, "pad")
	}
	for i := 0; i < 30; i++ {
		if _, err := st.AddMMFlag(store.MMFlag{Note: "pad", State: "dismissed", CreatedAt: 1}); err != nil {
			t.Fatalf("pad flag: %v", err)
		}
	}
	item := contractMLItem(t, st, "the flagged belief")
	pending, err := FlagMMItem(st, MMFlagRequest{Slug: slug, ItemID: item, Note: "pending-note-zq"}, "athena-sess")
	if err != nil {
		t.Fatalf("FlagMMItem pending: %v", err)
	}
	if _, err := FlagMMItem(st, MMFlagRequest{Slug: slug, ItemID: item, Note: "raised-note-zq"}, lead); err != nil {
		t.Fatalf("FlagMMItem raised: %v", err)
	}
	dis, err := FlagMMItem(st, MMFlagRequest{Slug: slug, ItemID: item, Note: "dismissed-note-zq"}, "athena-sess")
	if err != nil {
		t.Fatalf("FlagMMItem dismissed: %v", err)
	}
	if err := DismissMMFlag(st, dis.ID, lead, "no"); err != nil {
		t.Fatalf("DismissMMFlag: %v", err)
	}

	fire := func(session string) string {
		return Recall(context.Background(), st, RecallRequest{
			SessionID: session, Event: "UserPromptSubmit", Slug: slug, Prompt: "go on",
		})
	}
	if got := fire("athena-sess"); strings.Contains(got, "pending-note-zq") {
		t.Errorf("a teammate received the flag note:\n%s", got)
	}
	got := fire(lead)
	if !strings.Contains(got, "pending-note-zq") {
		t.Fatalf("lead's notes block lacks the pending flag:\n%s", got)
	}
	var line string
	for _, l := range strings.Split(got, "\n") {
		if strings.Contains(l, "pending-note-zq") {
			line = l
		}
	}
	if !strings.Contains(line, strconv.FormatInt(pending.ID, 10)) || !strings.Contains(line, strconv.FormatInt(item, 10)) {
		t.Errorf("flag line %q lacks the flag id %d or item id %d", line, pending.ID, item)
	}
	if strings.Contains(got, "raised-note-zq") || strings.Contains(got, "dismissed-note-zq") {
		t.Errorf("a raised or dismissed flag was listed:\n%s", got)
	}
	if again := fire(lead); strings.Contains(again, "pending-note-zq") {
		t.Errorf("the pending flag was delivered twice:\n%s", again)
	}
}

// ---------------------------------------------------------------------------
// RenderMMFiles
// ---------------------------------------------------------------------------

const contractMLRulesDir = ".claude/rules/kraken-mm"

func contractMLLine(title string, id int64) string {
	s := strconv.FormatInt(id, 10)
	return "- " + title + " — mm #" + s + " (`kraken mm show " + s + "`)"
}

func contractMLLines(s string) []string {
	return strings.Split(s, "\n")
}

// contractMLItemLines returns the lines of s that begin with "- " — the item
// lines, in file order.
func contractMLItemLines(s string) []string {
	var out []string
	for _, l := range contractMLLines(s) {
		if strings.HasPrefix(l, "- ") {
			out = append(out, l)
		}
	}
	return out
}

func contractMLRenderFixture() []store.MMItem {
	return []store.MMItem{
		{ID: 5, Kind: "note", Source: "capture", Title: "second jobs rule", Content: "b", Scope: "repo", Repo: "kraken", Dir: "internal/jobs"},
		{ID: 3, Kind: "note", Source: "capture", Title: "first jobs rule", Content: "a", Scope: "repo", Repo: "kraken", Dir: "internal/jobs"},
		{ID: 4, Kind: "note", Source: "capture", Title: "root rule", Content: "r", Scope: "repo", Repo: "kraken", Dir: ""},
		{ID: 6, Kind: "note", Source: "capture", Title: "universal rule", Content: "u", Scope: "universal"},
		{ID: 7, Kind: "note", Source: "capture", Title: "dead rule", Content: "d", Scope: "repo", Repo: "kraken", Dir: "internal/jobs", InvalidatedAt: 99, InvalidReason: "removed"},
		{ID: 8, Kind: "note", Source: "capture", Title: "", Content: "legacy first line\nsecond line", Scope: "repo", Repo: "other", Dir: "a/b"},
		{ID: 9, Kind: "note", Source: "capture", Title: "ghost only", Content: "g", Scope: "repo", Repo: "ghost", Dir: "x", InvalidatedAt: 5},
	}
}

// "For each `repo`-scoped item, `Repos[repo]["<rules dir>/<dirslug>.md"]` holds a
// rules file whose frontmatter is `---\npaths:\n  - "<dir>/**"\n---\n` for a
// non-empty dir and NO frontmatter for the repo root (`dir == ""`, file
// `root.md`), followed by one line per item in id order: `- <title> — mm #<id>
// (\`kraken mm show <id>\`)`" / "Only live items render" / "a repo with no live
// items is absent from `Repos`".
func TestMindLayerRenderMMFilesRepoFiles(t *testing.T) {
	r := RenderMMFiles(contractMLRenderFixture())
	if _, ok := r.Repos["ghost"]; ok {
		t.Errorf("a repo with no live items is present: %v", r.Repos["ghost"])
	}
	k, ok := r.Repos["kraken"]
	if !ok {
		t.Fatalf("Repos lacks kraken: %v", r.Repos)
	}
	jobsPath := contractMLRulesDir + "/internal-jobs.md"
	rootPath := contractMLRulesDir + "/root.md"
	idxPath := contractMLRulesDir + "/index.md"
	wantKeys := map[string]bool{jobsPath: true, rootPath: true, idxPath: true}
	for p := range k {
		if !wantKeys[p] {
			t.Errorf("unexpected kraken file %q", p)
		}
	}
	for p := range wantKeys {
		if _, ok := k[p]; !ok {
			t.Errorf("kraken lacks file %q (have %v)", p, k)
		}
	}

	jobs := k[jobsPath]
	if fm := "---\npaths:\n  - \"internal/jobs/**\"\n---\n"; !strings.HasPrefix(jobs, fm) {
		t.Errorf("%s does not open with the paths frontmatter %q:\n%s", jobsPath, fm, jobs)
	}
	if got, want := contractMLItemLines(strings.TrimPrefix(jobs, "---\npaths:\n  - \"internal/jobs/**\"\n---\n")),
		[]string{contractMLLine("first jobs rule", 3), contractMLLine("second jobs rule", 5)}; strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("%s item lines = %q, want %q (id order, live only)", jobsPath, got, want)
	}

	root := k[rootPath]
	if strings.HasPrefix(root, "---") {
		t.Errorf("root.md carries frontmatter:\n%s", root)
	}
	if got := contractMLItemLines(root); len(got) != 1 || got[0] != contractMLLine("root rule", 4) {
		t.Errorf("root.md item lines = %q, want [%q]", got, contractMLLine("root rule", 4))
	}
	if strings.Contains(jobs+root, "universal rule") || strings.Contains(jobs+root, "dead rule") {
		t.Errorf("a universal or invalidated item rendered into a repo file")
	}

	// the other repo: a/b → a-b.md with the a/b glob, legacy title fallback.
	o, ok := r.Repos["other"]
	if !ok {
		t.Fatalf("Repos lacks other: %v", r.Repos)
	}
	ab := o[contractMLRulesDir+"/a-b.md"]
	if !strings.HasPrefix(ab, "---\npaths:\n  - \"a/b/**\"\n---\n") {
		t.Errorf("a-b.md frontmatter wrong:\n%s", ab)
	}
	if got := contractMLItemLines(ab); len(got) != 1 || got[0] != contractMLLine("legacy first line", 8) {
		t.Errorf("a-b.md item lines = %q, want the first content line as the title", got)
	}
}

// "`Repos[repo]["<rules dir>/index.md"]` is unscoped and lists every item of the
// repo the same way under a two-line header naming `kraken mm show <id>` and
// `kraken mm source <id>`".
func TestMindLayerRenderMMFilesIndex(t *testing.T) {
	r := RenderMMFiles(contractMLRenderFixture())
	idx := r.Repos["kraken"][contractMLRulesDir+"/index.md"]
	if strings.HasPrefix(idx, "---") {
		t.Errorf("index.md is scoped (carries frontmatter):\n%s", idx)
	}
	for _, want := range []string{"kraken mm show", "kraken mm source"} {
		if !strings.Contains(idx, want) {
			t.Errorf("index.md header lacks %q:\n%s", want, idx)
		}
	}
	want := []string{contractMLLine("first jobs rule", 3), contractMLLine("root rule", 4), contractMLLine("second jobs rule", 5)}
	if got := contractMLItemLines(idx); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("index.md item lines = %q, want %q", got, want)
	}
}

// "`Universal` holds the universal items the same way (`""` when none)".
func TestMindLayerRenderMMFilesUniversal(t *testing.T) {
	r := RenderMMFiles(contractMLRenderFixture())
	if got := contractMLItemLines(r.Universal); len(got) != 1 || got[0] != contractMLLine("universal rule", 6) {
		t.Errorf("Universal item lines = %q, want [%q]", got, contractMLLine("universal rule", 6))
	}
	none := RenderMMFiles([]store.MMItem{
		{ID: 1, Kind: "note", Source: "capture", Title: "repo only", Content: "x", Scope: "repo", Repo: "kraken"},
	})
	if none.Universal != "" {
		t.Errorf("Universal with no universal items = %q, want \"\"", none.Universal)
	}
	if empty := RenderMMFiles(nil); len(empty.Repos) != 0 || empty.Universal != "" {
		t.Errorf("RenderMMFiles(nil) = %+v, want empty", empty)
	}
}

// "titles are escaped so a `-->` or a leading `-` cannot break the file (`-->` →
// `--&gt;`, leading `-` prefixed with `\`)" / "a title whose `title == ""`
// renders its first content line, cut at 120 runes".
func TestMindLayerRenderMMFilesEscapesAndFallback(t *testing.T) {
	long := strings.Repeat("é", 130)
	r := RenderMMFiles([]store.MMItem{
		{ID: 1, Kind: "note", Source: "capture", Title: "a --> b", Content: "x", Scope: "repo", Repo: "kraken"},
		{ID: 2, Kind: "note", Source: "capture", Title: "-leading dash", Content: "x", Scope: "repo", Repo: "kraken"},
		{ID: 3, Kind: "note", Source: "capture", Title: "", Content: long + "\nsecond", Scope: "repo", Repo: "kraken"},
	})
	root := r.Repos["kraken"][contractMLRulesDir+"/root.md"]
	want := []string{
		contractMLLine("a --&gt; b", 1),
		contractMLLine(`\-leading dash`, 2),
		contractMLLine(strings.Repeat("é", 120), 3),
	}
	if got := contractMLItemLines(root); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("root.md item lines = %q, want %q", got, want)
	}
	if strings.Contains(root, "-->") {
		t.Errorf("root.md still carries a raw -->:\n%s", root)
	}
}

// "output is byte-identical for identical input".
func TestMindLayerRenderMMFilesDeterministic(t *testing.T) {
	a := RenderMMFiles(contractMLRenderFixture())
	for i := 0; i < 5; i++ {
		b := RenderMMFiles(contractMLRenderFixture())
		if a.Universal != b.Universal || len(a.Repos) != len(b.Repos) {
			t.Fatalf("render %d differs", i)
		}
		for repo, files := range a.Repos {
			if len(files) != len(b.Repos[repo]) {
				t.Fatalf("render %d: repo %s file count differs", i, repo)
			}
			for p, body := range files {
				if b.Repos[repo][p] != body {
					t.Fatalf("render %d: %s/%s differs", i, repo, p)
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// renderMMIntoWorktree
// ---------------------------------------------------------------------------

func contractMLGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-C", dir, "-c", "user.email=t@t", "-c", "user.name=t", "-c", "commit.gpgsign=false"}, args...)
	out, err := exec.Command("git", full...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// Test environment: "`MMRenderPass`'s checkout half is tested through
// `renderMMIntoWorktree` against a temp `git init` repo with one `git worktree
// add`, asserting `git status --porcelain` empty in both and the files present
// in both" / "ensure the line `.claude/rules/kraken-mm/` is present in
// `<primary>/.git/info/exclude`".
func TestMindLayerRenderIntoWorktreeLeavesGitClean(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	st := contractMLStore(t)
	home := os.Getenv("HOME")
	primary := filepath.Join(home, "Developer", "kraken")
	if err := os.MkdirAll(primary, 0o755); err != nil {
		t.Fatal(err)
	}
	contractMLGit(t, primary, "init", "-q")
	if err := os.WriteFile(filepath.Join(primary, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	contractMLGit(t, primary, "add", "README.md")
	contractMLGit(t, primary, "commit", "-q", "-m", "init")
	wt := filepath.Join(home, ".kraken", "cells", "render-1", "wt-kraken")
	if err := os.MkdirAll(filepath.Dir(wt), 0o755); err != nil {
		t.Fatal(err)
	}
	contractMLGit(t, primary, "worktree", "add", "-q", "-b", "carl/render-1", wt)

	for _, it := range []store.MMItem{
		{Kind: "note", Source: "capture", Title: "jobs rule", Content: "j", Scope: "repo", Repo: "kraken", Dir: "internal/jobs", CreatedAt: 1},
		{Kind: "note", Source: "capture", Title: "root rule", Content: "r", Scope: "repo", Repo: "kraken", Dir: "", CreatedAt: 2},
	} {
		if _, err := st.AddMMItem(it); err != nil {
			t.Fatalf("AddMMItem: %v", err)
		}
	}
	for _, co := range []string{primary, wt} {
		if err := renderMMIntoWorktree(st, "kraken", co); err != nil {
			t.Fatalf("renderMMIntoWorktree(%s): %v", co, err)
		}
	}
	for _, co := range []string{primary, wt} {
		for _, f := range []string{"internal-jobs.md", "root.md", "index.md"} {
			p := filepath.Join(co, ".claude", "rules", "kraken-mm", f)
			b, err := os.ReadFile(p)
			if err != nil {
				t.Errorf("%s missing in %s: %v", f, co, err)
				continue
			}
			if !strings.Contains(string(b), "mm #") {
				t.Errorf("%s in %s carries no item line:\n%s", f, co, b)
			}
		}
		if out := contractMLGit(t, co, "status", "--porcelain"); strings.TrimSpace(out) != "" {
			t.Errorf("git status --porcelain in %s is not clean:\n%s", co, out)
		}
	}
	ex, err := os.ReadFile(filepath.Join(primary, ".git", "info", "exclude"))
	if err != nil {
		t.Fatalf("read exclude: %v", err)
	}
	n := 0
	for _, l := range strings.Split(string(ex), "\n") {
		if strings.TrimSpace(l) == ".claude/rules/kraken-mm/" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("exclude carries the kraken-mm line %d times, want exactly once:\n%s", n, ex)
	}
}

// ---------------------------------------------------------------------------
// ZeusTranscriptBlocks / zeusBrief
// ---------------------------------------------------------------------------

// "a job with no session or no transcript returns an empty slice and `""` with
// nil error."
func TestMindLayerZeusTranscriptBlocksNoSession(t *testing.T) {
	contractMLEnv(t)
	blocks, sess, err := ZeusTranscriptBlocks(&Job{Slug: "no-session"}, 50)
	if err != nil || sess != "" || len(blocks) != 0 {
		t.Errorf("ZeusTranscriptBlocks(no session) = %v, %q, %v; want empty, \"\", nil", blocks, sess, err)
	}
}

// "`jobs.zeusBrief`: Contains the ladder and the raise rule: `kraken mm get --q`,
// `kraken mm show <id>`, `kraken mm source <id>`, `kraken mm flag` ... `kraken mm
// flag raise`/`dismiss` ... Contains no `kraken graph` verb."
func TestMindLayerZeusBriefCarriesTheLadder(t *testing.T) {
	contractMLEnv(t)
	brief := zeusBrief(&Job{Slug: "ladder"})
	for _, want := range []string{"kraken mm get --q", "kraken mm show", "kraken mm source", "kraken mm flag", "raise", "dismiss"} {
		if !strings.Contains(brief, want) {
			t.Errorf("zeus brief lacks %q", want)
		}
	}
	if strings.Contains(brief, "kraken graph") {
		t.Errorf("zeus brief still names a kraken graph verb")
	}
}
