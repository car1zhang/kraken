package jobs

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"kraken/internal/store"
)

// mindlayer_edge_test.go — the EDGE dimension of 50-contract.md slice B (PR 2,
// the mental-model rework) for package jobs. Written blind from the contract:
// capture/mind/commit/flag/render/thread/transcript sources were not read.
// What lives here is refusals in order, boundaries, idempotence and the error
// paths; athena-logic owns the happy paths.
//
// Every package-level identifier is prefixed `edgeML`.

// edgeMLEnv is the contract's "Test environment obligations": HOME isolated,
// KRAKEN_DB_PATH / KRAKEN_THREAD_SESSION / KRAKEN_THREAD_ROLE unset.
func edgeMLEnv(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KRAKEN_DB_PATH", "")
	t.Setenv("KRAKEN_THREAD_SESSION", "")
	t.Setenv("KRAKEN_THREAD_ROLE", "")
}

// edgeMLStore opens a temp store and wires it (UseStore) so events land in it;
// unwired on cleanup.
func edgeMLStore(t *testing.T) *store.Store {
	t.Helper()
	edgeMLEnv(t)
	st, err := store.Open(filepath.Join(t.TempDir(), "kraken.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	UseStore(st)
	t.Cleanup(func() {
		UseStore(nil)
		st.Close()
	})
	return st
}

func edgeMLJob(t *testing.T, st *store.Store, slug, repo, session string) *store.Job {
	t.Helper()
	id, err := st.CreateJob(&store.Job{Slug: slug, Repo: repo, State: "working", Session: session})
	if err != nil {
		t.Fatalf("create job %q: %v", slug, err)
	}
	j, err := st.GetJobByID(id)
	if err != nil {
		t.Fatalf("get job %d: %v", id, err)
	}
	return j
}

// edgeMLFakeRepo makes ResolveRepo(name) resolve inside the sandboxed $HOME (a
// .git entry is the repo test, the submit_edge_test.go precedent).
func edgeMLFakeRepo(t *testing.T, name string) string {
	t.Helper()
	repo := filepath.Join(os.Getenv("HOME"), "Developer", name)
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatalf("fake repo %s: %v", name, err)
	}
	return repo
}

func edgeMLThread(t *testing.T, st *store.Store, jobID int64, session, role, state string) store.Thread {
	t.Helper()
	row, err := st.AddThreadNext(store.Thread{JobID: jobID, Title: "edge " + session, SessionID: session, Role: role, State: state})
	if err != nil {
		t.Fatalf("AddThreadNext(%s): %v", session, err)
	}
	return row
}

func edgeMLItemCount(t *testing.T, st *store.Store) int {
	t.Helper()
	items, err := st.ListMMItems(true)
	if err != nil {
		t.Fatalf("ListMMItems: %v", err)
	}
	return len(items)
}

func edgeMLFlagCount(t *testing.T, st *store.Store) int {
	t.Helper()
	flags, err := st.ListMMFlags("")
	if err != nil {
		t.Fatalf("ListMMFlags: %v", err)
	}
	return len(flags)
}

func edgeMLHasEvent(t *testing.T, st *store.Store, name string) bool {
	t.Helper()
	events, err := st.ListEvents(500, 0)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	for _, e := range events {
		if e.Event == name {
			return true
		}
	}
	return false
}

const edgeMLRepo = "edgerepo"

// edgeMLCommitFixture: a store, a fake repo, a job on it and one live mind
// thread whose session is returned.
func edgeMLCommitFixture(t *testing.T) (*store.Store, *store.Job, string) {
	t.Helper()
	st := edgeMLStore(t)
	edgeMLFakeRepo(t, edgeMLRepo)
	j := edgeMLJob(t, st, "edge-ml-commit", edgeMLRepo, "sess-lead")
	edgeMLThread(t, st, j.ID, "sess-mind", "mind", "live")
	return st, j, "sess-mind"
}

func edgeMLValidCommit() MMCommit {
	return MMCommit{
		Title:  "the daemon owns the render",
		Scope:  "repo",
		Repo:   edgeMLRepo,
		Dir:    "internal/jobs",
		Kind:   "note",
		Body:   "what carl now knows",
		Quote:  "a quote",
		Source: CaptureSource{Kind: "manual"},
	}
}

// ---------------------------------------------------------------------------
// ValidCaptureSource
// ---------------------------------------------------------------------------

// "`Kind` ∈ artifact, transcript, dir, item, manual; `ValidCaptureSource`
// refuses any other kind and an `artifact` without `name`, a `transcript`
// without `session`, a `dir` without `repo`."
func TestMindLayerEdgeValidCaptureSourceRefusals(t *testing.T) {
	bad := []CaptureSource{
		{},
		{Kind: "code", Repo: "kraken"},
		{Kind: "Artifact", Name: "00-overview.md"},
		{Kind: "artifacts", Name: "00-overview.md"},
		{Kind: "capture"},
		{Kind: "artifact", Slug: "s", Home: "h", SHA: "abc"},
		{Kind: "transcript", UUID: "u1", TID: "t1"},
		{Kind: "dir", Dir: "internal/jobs"},
	}
	for _, s := range bad {
		if err := ValidCaptureSource(s); err == nil {
			t.Errorf("ValidCaptureSource(%+v) = nil, want a refusal", s)
		}
	}
	good := []CaptureSource{
		{Kind: "artifact", Name: "00-overview.md"},
		{Kind: "transcript", Session: "s1"},
		{Kind: "dir", Repo: "kraken"},
		{Kind: "item"},
		{Kind: "manual"},
	}
	for _, s := range good {
		if err := ValidCaptureSource(s); err != nil {
			t.Errorf("ValidCaptureSource(%+v) = %v, want nil (the minimal valid shape of its kind)", s, err)
		}
	}
}

// ---------------------------------------------------------------------------
// CaptureToMind — refusals reachable without tmux
// ---------------------------------------------------------------------------

// "Refusals in order …: `ErrCaptureNoCell` (job has no live cell …) → 409;
// `ErrCaptureTooLong` …; `ErrCaptureBadSource` …; empty trimmed text →
// `ErrCaptureBadSource`." With no live cell, NoCell is first, so it wins over
// every later refusal the same request would also trip — and nothing is
// written.
func TestMindLayerEdgeCaptureNoCellWinsInOrder(t *testing.T) {
	st := edgeMLStore(t)
	sj := edgeMLJob(t, st, "edge-ml-capture-dead", "kraken", "sess-lead")
	j := &Job{ID: sj.ID, Slug: sj.Slug, Repo: "kraken", Session: "sess-lead"}

	reqs := []CaptureRequest{
		{Slug: j.Slug, Text: "fine text", Source: CaptureSource{Kind: "manual"}},
		{Slug: j.Slug, Text: strings.Repeat("x", 32_001), Source: CaptureSource{Kind: "manual"}},
		{Slug: j.Slug, Text: "fine", Source: CaptureSource{Kind: "bogus"}},
		{Slug: j.Slug, Text: "   \n\t ", Source: CaptureSource{Kind: "manual"}},
	}
	for _, req := range reqs {
		_, err := CaptureToMind(j, req)
		if !errors.Is(err, ErrCaptureNoCell) {
			t.Errorf("CaptureToMind(dead cell, text %d runes, source %q) = %v, want ErrCaptureNoCell", len([]rune(req.Text)), req.Source.Kind, err)
		}
	}
	// codex is "refused likewise".
	cj := &Job{ID: sj.ID, Slug: sj.Slug, Repo: "kraken", Agent: "codex"}
	if _, err := CaptureToMind(cj, reqs[0]); !errors.Is(err, ErrCaptureNoCell) {
		t.Errorf("CaptureToMind(codex job) = %v, want ErrCaptureNoCell", err)
	}
	threads, err := st.ListThreads(sj.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) != 0 {
		t.Fatalf("a refused capture left %d thread rows", len(threads))
	}
}

// The four sentinels are distinct (the API maps each separately).
func TestMindLayerEdgeCaptureSentinelsAreDistinct(t *testing.T) {
	errs := []error{ErrCaptureNoCell, ErrCaptureTooLong, ErrCaptureBadSource, ErrNotMindThread, ErrCommitInvalid, ErrFlagInvalid, ErrFlagNoItem, ErrFlagNoJob, ErrNotLead}
	for i, a := range errs {
		if a == nil {
			t.Fatalf("sentinel #%d is nil", i)
		}
		for k, b := range errs {
			if i != k && errors.Is(a, b) {
				t.Errorf("sentinel %q matches %q under errors.Is; each must be distinct", a, b)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// mindThreadFor (pure)
// ---------------------------------------------------------------------------

// "Returns the live `mind` thread whose `TurnUUID == zeusUUID`, else nil; an
// `ask` thread never matches; a closed thread never matches; an empty
// `zeusUUID` never matches." (Amendment 13 moved the key from ForkUUID to
// TurnUUID; the lead edited the field name here, sighted, nothing else.)
func TestMindLayerEdgeMindThreadForRefusals(t *testing.T) {
	ask := store.Thread{TID: "t1", Role: "ask", State: "live", TurnUUID: "u1"}
	closed := store.Thread{TID: "t2", Role: "mind", State: "closed", TurnUUID: "u1"}
	older := store.Thread{TID: "t3", Role: "mind", State: "live", TurnUUID: "u0"}
	blank := store.Thread{TID: "t4", Role: "mind", State: "live", TurnUUID: ""}
	noRole := store.Thread{TID: "t5", Role: "", State: "live", TurnUUID: "u1"}

	if got := mindThreadFor(nil, "u1"); got != nil {
		t.Errorf("mindThreadFor(nil) = %+v, want nil", *got)
	}
	if got := mindThreadFor([]store.Thread{ask, closed, older, noRole}, "u1"); got != nil {
		t.Errorf("mindThreadFor(ask/closed/older/role-less) = %+v, want nil", *got)
	}
	// an empty zeusUUID never matches — not even a mind thread whose TurnUUID
	// is itself empty.
	if got := mindThreadFor([]store.Thread{blank}, ""); got != nil {
		t.Errorf("mindThreadFor(empty uuid) matched %+v, want nil", *got)
	}
	// control: the one live mind thread on u1 is found among the decoys.
	live := store.Thread{TID: "t9", Role: "mind", State: "live", TurnUUID: "u1"}
	got := mindThreadFor([]store.Thread{ask, closed, older, blank, noRole, live}, "u1")
	if got == nil || got.TID != "t9" {
		t.Fatalf("mindThreadFor(with a live mind thread on u1) = %v, want t9", got)
	}
}

// ---------------------------------------------------------------------------
// threadEnvPrefix / threadBrief / BranchThreadWithRole
// ---------------------------------------------------------------------------

// "`""` when session is empty."
func TestMindLayerEdgeThreadEnvPrefixEmptySession(t *testing.T) {
	for _, role := range []string{"mind", "ask", ""} {
		if got := threadEnvPrefix("", role); got != "" {
			t.Errorf("threadEnvPrefix(\"\", %q) = %q, want \"\"", role, got)
		}
	}
}

// "Returns `KRAKEN_THREAD_SESSION='<session>' KRAKEN_THREAD_ROLE='<role>' `
// (single-quoted values, trailing space)" — exactly, for both roles.
func TestMindLayerEdgeThreadEnvPrefixExactShape(t *testing.T) {
	for _, role := range []string{"mind", "ask"} {
		want := "KRAKEN_THREAD_SESSION='abc-123' KRAKEN_THREAD_ROLE='" + role + "' "
		if got := threadEnvPrefix("abc-123", role); got != want {
			t.Errorf("threadEnvPrefix(abc-123, %s) = %q, want %q", role, got, want)
		}
	}
}

// "for `role == "ask"` it is the pre-existing brief (names none of `mm
// commit`/`mm revise`)".
func TestMindLayerEdgeAskBriefNamesNoMindWrite(t *testing.T) {
	edgeMLEnv(t)
	j := &Job{Slug: "edge-ml-brief", ID: 5, Repo: "kraken"}
	b := threadBrief(j, "t1", "why", "/tmp/wt-kraken", "ask")
	if b == "" {
		t.Fatal("ask brief is empty")
	}
	for _, verb := range []string{"mm commit", "mm revise"} {
		if strings.Contains(b, verb) {
			t.Errorf("ask brief names %q; only the mind brief may", verb)
		}
	}
	m := threadBrief(j, "t1", "why", "/tmp/wt-kraken", "mind")
	if m == b {
		t.Error("mind brief is byte-identical to the ask brief")
	}
}

// "a role outside `ask`/`mind` → error before any spawn" — and no row.
func TestMindLayerEdgeBranchThreadWithRoleRefusesBadRole(t *testing.T) {
	st := edgeMLStore(t)
	sj := edgeMLJob(t, st, "edge-ml-badrole", "kraken", "sess-lead")
	j := &Job{ID: sj.ID, Slug: sj.Slug, Repo: "kraken"}
	for _, role := range []string{"", "Mind", "zeus", "mind "} {
		if _, err := BranchThreadWithRole(j, "t", "seed", "", role); err == nil {
			t.Errorf("BranchThreadWithRole(role %q) = nil, want an error", role)
		}
	}
	threads, err := st.ListThreads(sj.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) != 0 {
		t.Fatalf("refused branches left %d thread rows", len(threads))
	}
}

// ---------------------------------------------------------------------------
// CommitMMItem
// ---------------------------------------------------------------------------

// "Gate first: `threadSession` must name a thread row with `Role == "mind"`
// and `State == "live"` …, else `ErrNotMindThread`" — an ask thread, a closed
// mind thread, zeus' own session, an empty and an unknown session. The body is
// deliberately ALSO invalid, so a validation-first implementation fails here.
func TestMindLayerEdgeCommitGateRefusalsComeFirst(t *testing.T) {
	st, j, _ := edgeMLCommitFixture(t)
	edgeMLThread(t, st, j.ID, "sess-ask", "ask", "live")
	edgeMLThread(t, st, j.ID, "sess-mind-closed", "mind", "closed")

	invalid := edgeMLValidCommit()
	invalid.Title = ""
	invalid.Kind = "bogus"
	for _, sess := range []string{"sess-ask", "sess-mind-closed", "sess-lead", "", "sess-nobody"} {
		for _, c := range []MMCommit{edgeMLValidCommit(), invalid} {
			id, err := CommitMMItem(st, c, sess)
			if !errors.Is(err, ErrNotMindThread) {
				t.Errorf("CommitMMItem(session %q, title %q) = %d, %v; want ErrNotMindThread", sess, c.Title, id, err)
			}
		}
	}
	if n := edgeMLItemCount(t, st); n != 0 {
		t.Fatalf("refused commits stored %d items", n)
	}
}

// The validation rules, each broken alone on an otherwise valid commit from a
// live mind thread → `ErrCommitInvalid`, nothing stored.
func TestMindLayerEdgeCommitValidationRefusals(t *testing.T) {
	st, _, sess := edgeMLCommitFixture(t)
	cases := []struct {
		name string
		mut  func(*MMCommit)
	}{
		{"title empty", func(c *MMCommit) { c.Title = "" }},
		{"title whitespace", func(c *MMCommit) { c.Title = "  \t\n " }},
		{"title 121 runes", func(c *MMCommit) { c.Title = strings.Repeat("é", 121) }},
		{"body empty", func(c *MMCommit) { c.Body = "" }},
		{"body 8001 runes", func(c *MMCommit) { c.Body = strings.Repeat("é", 8001) }},
		{"kind empty", func(c *MMCommit) { c.Kind = "" }},
		{"kind Decision", func(c *MMCommit) { c.Kind = "Decision" }},
		{"kind question", func(c *MMCommit) { c.Kind = "question" }},
		{"scope empty", func(c *MMCommit) { c.Scope = "" }},
		{"scope global", func(c *MMCommit) { c.Scope = "global" }},
		{"scope Repo", func(c *MMCommit) { c.Scope = "Repo" }},
		{"repo scope, empty repo", func(c *MMCommit) { c.Repo = "" }},
		{"repo scope, unresolvable repo", func(c *MMCommit) { c.Repo = "no-such-repo-anywhere" }},
		{"dir leading slash", func(c *MMCommit) { c.Dir = "/internal/jobs" }},
		{"dir is ..", func(c *MMCommit) { c.Dir = ".." }},
		{"dir starts ..", func(c *MMCommit) { c.Dir = "../elsewhere" }},
		{"dir inner ..", func(c *MMCommit) { c.Dir = "internal/../../etc" }},
		{"dir trailing ..", func(c *MMCommit) { c.Dir = "internal/.." }},
		{"source empty kind", func(c *MMCommit) { c.Source = CaptureSource{} }},
		{"source unknown kind", func(c *MMCommit) { c.Source = CaptureSource{Kind: "code"} }},
		{"source artifact without name", func(c *MMCommit) { c.Source = CaptureSource{Kind: "artifact", Slug: "s"} }},
		{"source transcript without session", func(c *MMCommit) { c.Source = CaptureSource{Kind: "transcript", UUID: "u"} }},
		{"source dir without repo", func(c *MMCommit) { c.Source = CaptureSource{Kind: "dir", Dir: "x"} }},
	}
	for _, tc := range cases {
		c := edgeMLValidCommit()
		tc.mut(&c)
		id, err := CommitMMItem(st, c, sess)
		if !errors.Is(err, ErrCommitInvalid) {
			t.Errorf("%s: CommitMMItem = %d, %v; want ErrCommitInvalid", tc.name, id, err)
		}
	}
	if n := edgeMLItemCount(t, st); n != 0 {
		t.Fatalf("refused commits stored %d items", n)
	}
}

// The boundaries that must be ACCEPTED: title of exactly 120 runes (multi-byte,
// so a byte count would refuse it), the same padded with whitespace that the
// trim removes, body of exactly 8,000 runes, the repo root (dir ""), and a dir
// whose segment merely contains ".." without being one.
func TestMindLayerEdgeCommitAcceptsBoundaries(t *testing.T) {
	st, _, sess := edgeMLCommitFixture(t)
	cases := []struct {
		name string
		mut  func(*MMCommit)
	}{
		{"title 120 runes", func(c *MMCommit) { c.Title = strings.Repeat("é", 120) }},
		{"title 120 runes padded", func(c *MMCommit) { c.Title = "  " + strings.Repeat("é", 120) + "\n" }},
		{"title 1 rune", func(c *MMCommit) { c.Title = "x" }},
		{"body 8000 runes", func(c *MMCommit) { c.Body = strings.Repeat("é", 8000) }},
		{"body 1 rune", func(c *MMCommit) { c.Body = "x" }},
		{"repo root", func(c *MMCommit) { c.Dir = "" }},
		{"dir with dots inside a segment", func(c *MMCommit) { c.Dir = "a..b/c" }},
	}
	for _, tc := range cases {
		c := edgeMLValidCommit()
		tc.mut(&c)
		id, err := CommitMMItem(st, c, sess)
		if err != nil || id <= 0 {
			t.Errorf("%s: CommitMMItem = %d, %v; want a stored item", tc.name, id, err)
		}
	}
}

// "`universal` forces `Repo = Dir = ""`" — even when the commit names a repo
// that does not resolve and a dir that would otherwise be refused: universal
// scope discards both rather than validating them.
func TestMindLayerEdgeCommitUniversalForcesRepoDirEmpty(t *testing.T) {
	st, _, sess := edgeMLCommitFixture(t)
	c := edgeMLValidCommit()
	c.Scope = "universal"
	c.Repo = edgeMLRepo
	c.Dir = "internal/jobs"
	id, err := CommitMMItem(st, c, sess)
	if err != nil {
		t.Fatalf("CommitMMItem(universal): %v", err)
	}
	got, err := st.GetMMItem(id)
	if err != nil {
		t.Fatalf("GetMMItem: %v", err)
	}
	if got.Scope != "universal" || got.Repo != "" || got.Dir != "" {
		t.Fatalf("universal item stored scope=%q repo=%q dir=%q, want universal/''/''", got.Scope, got.Repo, got.Dir)
	}
}

// ---------------------------------------------------------------------------
// jobs.ReviseMMItem
// ---------------------------------------------------------------------------

// "Same gate and validation as commit; the item must exist and be live (else
// error → 404/409)" — and a refused revise changes nothing.
func TestMindLayerEdgeReviseRefusals(t *testing.T) {
	st, j, sess := edgeMLCommitFixture(t)
	edgeMLThread(t, st, j.ID, "sess-ask", "ask", "live")
	id, err := CommitMMItem(st, edgeMLValidCommit(), sess)
	if err != nil {
		t.Fatalf("seed commit: %v", err)
	}
	before, _ := st.GetMMItem(id)

	rev := edgeMLValidCommit()
	rev.Title = "a revised claim"
	if err := ReviseMMItem(st, id, rev, "sess-ask"); !errors.Is(err, ErrNotMindThread) {
		t.Errorf("ReviseMMItem(ask thread) = %v, want ErrNotMindThread", err)
	}
	bad := rev
	bad.Title = strings.Repeat("x", 121)
	if err := ReviseMMItem(st, id, bad, sess); !errors.Is(err, ErrCommitInvalid) {
		t.Errorf("ReviseMMItem(121-rune title) = %v, want ErrCommitInvalid", err)
	}
	if err := ReviseMMItem(st, id+1000, rev, sess); err == nil {
		t.Error("ReviseMMItem(unknown id) = nil, want an error")
	}
	if after, _ := st.GetMMItem(id); !reflect.DeepEqual(after, before) {
		t.Fatalf("refused revises changed the item:\n got %+v\nwant %+v", after, before)
	}

	if err := st.InvalidateMMItem(id, 1, "removed", ""); err != nil {
		t.Fatalf("InvalidateMMItem: %v", err)
	}
	dead, _ := st.GetMMItem(id)
	if err := ReviseMMItem(st, id, rev, sess); err == nil {
		t.Error("ReviseMMItem(invalidated) = nil, want an error")
	}
	if after, _ := st.GetMMItem(id); !reflect.DeepEqual(after, dead) {
		t.Fatalf("a revise of an invalidated item changed it:\n got %+v\nwant %+v", after, dead)
	}
}

// ---------------------------------------------------------------------------
// FlagMMItem
// ---------------------------------------------------------------------------

// "Note empty → `ErrFlagInvalid`"; "a named `ItemID`/`AgainstID` must exist
// (`ErrFlagNoItem`)"; "unknown → `ErrFlagNoJob`" — nothing stored.
func TestMindLayerEdgeFlagRefusals(t *testing.T) {
	st := edgeMLStore(t)
	j := edgeMLJob(t, st, "edge-ml-flag", "kraken", "sess-lead")
	item, err := st.AddMMItem(store.MMItem{Kind: "note", Source: "manual", Content: "x", CreatedAt: 1})
	if err != nil {
		t.Fatalf("AddMMItem: %v", err)
	}

	if _, err := FlagMMItem(st, MMFlagRequest{Slug: j.Slug, ItemID: item, Note: ""}, "sess-x"); !errors.Is(err, ErrFlagInvalid) {
		t.Errorf("FlagMMItem(empty note) = %v, want ErrFlagInvalid", err)
	}
	if _, err := FlagMMItem(st, MMFlagRequest{Slug: j.Slug, ItemID: item + 99, Note: "n"}, "sess-x"); !errors.Is(err, ErrFlagNoItem) {
		t.Errorf("FlagMMItem(unknown item) = %v, want ErrFlagNoItem", err)
	}
	if _, err := FlagMMItem(st, MMFlagRequest{Slug: j.Slug, ItemID: item, AgainstID: item + 99, Note: "n"}, "sess-x"); !errors.Is(err, ErrFlagNoItem) {
		t.Errorf("FlagMMItem(unknown against) = %v, want ErrFlagNoItem", err)
	}
	if _, err := FlagMMItem(st, MMFlagRequest{Slug: "no-such-job", ItemID: item, Note: "n"}, "sess-x"); !errors.Is(err, ErrFlagNoJob) {
		t.Errorf("FlagMMItem(unknown slug) = %v, want ErrFlagNoJob", err)
	}
	if n := edgeMLFlagCount(t, st); n != 0 {
		t.Fatalf("refused flags stored %d rows", n)
	}
}

// "a flag with no slug and no items is still accepted with `JobID 0`" — and,
// having no job and so no lead, it is `pending`.
func TestMindLayerEdgeFlagNoSlugNoItemsAccepted(t *testing.T) {
	st := edgeMLStore(t)
	f, err := FlagMMItem(st, MMFlagRequest{Note: "the transcript contradicts itself"}, "sess-anyone")
	if err != nil {
		t.Fatalf("FlagMMItem(note only): %v", err)
	}
	if f.ID <= 0 || f.JobID != 0 || f.ItemID != 0 || f.AgainstID != 0 {
		t.Errorf("flag = %+v, want a stored row with JobID/ItemID/AgainstID 0", f)
	}
	if f.State != "pending" {
		t.Errorf("State = %q, want pending (no job, no lead)", f.State)
	}
	got, err := st.GetMMFlag(f.ID)
	if err != nil || got.JobID != 0 || got.Note != "the transcript contradicts itself" {
		t.Errorf("stored flag = %+v, %v", got, err)
	}
}

// "created `raised` … when `session` equals the job's stamped lead session …,
// else `pending`" — a teammate session, a case-variant and a prefix of the
// lead's are all non-lead.
func TestMindLayerEdgeFlagFromNonLeadIsPending(t *testing.T) {
	st := edgeMLStore(t)
	j := edgeMLJob(t, st, "edge-ml-flag-nonlead", "kraken", "sess-lead")
	for _, sess := range []string{"sess-teammate", "SESS-LEAD", "sess-lea", "", "sess-lead "} {
		f, err := FlagMMItem(st, MMFlagRequest{Slug: j.Slug, Note: "n " + sess}, sess)
		if err != nil {
			t.Fatalf("FlagMMItem(%q): %v", sess, err)
		}
		if f.State != "pending" || f.RaisedBy != "" || f.RaisedAt != 0 {
			t.Errorf("flag from %q = state %q raised_by %q raised_at %d; want pending, '', 0", sess, f.State, f.RaisedBy, f.RaisedAt)
		}
		if f.JobID != j.ID {
			t.Errorf("flag from %q JobID = %d, want %d", sess, f.JobID, j.ID)
		}
	}
	if edgeMLHasEvent(t, st, "mm-flag-raised") {
		t.Error("pending creations emitted mm-flag-raised")
	}
}

// ---------------------------------------------------------------------------
// RaiseMMFlag / DismissMMFlag / ResolveMMFlag
// ---------------------------------------------------------------------------

// "`session` must equal the lead session of the flag's job (`ErrNotLead` →
// 403 …)" — nothing changed.
func TestMindLayerEdgeRaiseDismissFromNonLead(t *testing.T) {
	st := edgeMLStore(t)
	j := edgeMLJob(t, st, "edge-ml-raise", "kraken", "sess-lead")
	f, err := FlagMMItem(st, MMFlagRequest{Slug: j.Slug, Note: "n"}, "sess-teammate")
	if err != nil {
		t.Fatalf("FlagMMItem: %v", err)
	}
	before, _ := st.GetMMFlag(f.ID)
	for _, sess := range []string{"sess-teammate", "", "SESS-LEAD"} {
		if err := RaiseMMFlag(st, f.ID, sess, "x"); !errors.Is(err, ErrNotLead) {
			t.Errorf("RaiseMMFlag(%q) = %v, want ErrNotLead", sess, err)
		}
		if err := DismissMMFlag(st, f.ID, sess, "x"); !errors.Is(err, ErrNotLead) {
			t.Errorf("DismissMMFlag(%q) = %v, want ErrNotLead", sess, err)
		}
	}
	if after, _ := st.GetMMFlag(f.ID); !reflect.DeepEqual(after, before) {
		t.Fatalf("refused raise/dismiss changed the flag:\n got %+v\nwant %+v", after, before)
	}
	if edgeMLHasEvent(t, st, "mm-flag-raised") || edgeMLHasEvent(t, st, "mm-flag-dismissed") {
		t.Error("a refused raise/dismiss emitted its event")
	}
}

// "a flag with `JobID 0` accepts any NON-EMPTY session" — so an empty one is
// refused.
func TestMindLayerEdgeRaiseJobZeroRefusesEmptySession(t *testing.T) {
	st := edgeMLStore(t)
	f, err := FlagMMItem(st, MMFlagRequest{Note: "orphan"}, "sess-x")
	if err != nil {
		t.Fatalf("FlagMMItem: %v", err)
	}
	if err := RaiseMMFlag(st, f.ID, "", "x"); err == nil {
		t.Error("RaiseMMFlag(JobID 0, empty session) = nil, want an error")
	}
	if err := DismissMMFlag(st, f.ID, "", "x"); err == nil {
		t.Error("DismissMMFlag(JobID 0, empty session) = nil, want an error")
	}
	if got, _ := st.GetMMFlag(f.ID); got.State != "pending" {
		t.Fatalf("State = %q after refused transitions, want pending", got.State)
	}
}

// "a non-pending flag → error (409)" — from the LEAD, so the refusal is the
// state one and not `ErrNotLead`.
func TestMindLayerEdgeRaiseDismissNonPendingFromLead(t *testing.T) {
	st := edgeMLStore(t)
	j := edgeMLJob(t, st, "edge-ml-nonpending", "kraken", "sess-lead")
	f, err := FlagMMItem(st, MMFlagRequest{Slug: j.Slug, Note: "n"}, "sess-teammate")
	if err != nil {
		t.Fatalf("FlagMMItem: %v", err)
	}
	if err := RaiseMMFlag(st, f.ID, "sess-lead", "verified"); err != nil {
		t.Fatalf("RaiseMMFlag(lead): %v", err)
	}
	before, _ := st.GetMMFlag(f.ID)
	for name, fn := range map[string]func() error{
		"raise again":     func() error { return RaiseMMFlag(st, f.ID, "sess-lead", "again") },
		"dismiss raised":  func() error { return DismissMMFlag(st, f.ID, "sess-lead", "no") },
		"raise unknownid": func() error { return RaiseMMFlag(st, f.ID+999, "sess-lead", "x") },
	} {
		err := fn()
		if err == nil {
			t.Errorf("%s = nil, want an error", name)
			continue
		}
		if name != "raise unknownid" && errors.Is(err, ErrNotLead) {
			t.Errorf("%s from the lead = ErrNotLead, want the non-pending refusal", name)
		}
	}
	if after, _ := st.GetMMFlag(f.ID); !reflect.DeepEqual(after, before) {
		t.Fatalf("refused transitions changed the flag:\n got %+v\nwant %+v", after, before)
	}
}

// "a flag not `raised` or already resolved → error (409)"; a value outside
// drop/keep is refused; `keep` resolves only; `drop` on an item that is already
// invalidated ("when non-zero and live") leaves that invalidation alone.
func TestMindLayerEdgeResolveRefusalsAndDropOfDeadItem(t *testing.T) {
	st := edgeMLStore(t)
	j := edgeMLJob(t, st, "edge-ml-resolve", "kraken", "sess-lead")
	item, _ := st.AddMMItem(store.MMItem{Kind: "note", Source: "manual", Content: "keep me", CreatedAt: 1})

	pending, err := FlagMMItem(st, MMFlagRequest{Slug: j.Slug, ItemID: item, Note: "p"}, "sess-teammate")
	if err != nil {
		t.Fatalf("FlagMMItem: %v", err)
	}
	for _, r := range []string{"drop", "keep"} {
		if err := ResolveMMFlag(st, pending.ID, r); err == nil {
			t.Errorf("ResolveMMFlag(pending, %q) = nil, want an error", r)
		}
	}
	if it, _ := st.GetMMItem(item); it.InvalidatedAt != 0 {
		t.Fatalf("a refused drop of a pending flag invalidated item %d", item)
	}

	raised, err := FlagMMItem(st, MMFlagRequest{Slug: j.Slug, ItemID: item, Note: "r"}, "sess-lead")
	if err != nil {
		t.Fatalf("FlagMMItem(lead): %v", err)
	}
	if raised.State != "raised" {
		t.Fatalf("lead flag State = %q, want raised", raised.State)
	}
	for _, r := range []string{"", "Drop", "maybe", " keep"} {
		if err := ResolveMMFlag(st, raised.ID, r); err == nil {
			t.Errorf("ResolveMMFlag(raised, %q) = nil, want an error", r)
		}
	}
	if err := ResolveMMFlag(st, raised.ID, "keep"); err != nil {
		t.Fatalf("ResolveMMFlag(keep): %v", err)
	}
	if it, _ := st.GetMMItem(item); it.InvalidatedAt != 0 {
		t.Fatalf("keep invalidated item %d (reason %q)", item, it.InvalidReason)
	}
	for _, r := range []string{"keep", "drop"} {
		if err := ResolveMMFlag(st, raised.ID, r); err == nil {
			t.Errorf("second ResolveMMFlag(%q) = nil, want an error", r)
		}
	}
	if it, _ := st.GetMMItem(item); it.InvalidatedAt != 0 {
		t.Fatalf("a refused second resolution invalidated item %d", item)
	}

	// drop of an already-invalidated item: resolve succeeds, the earlier
	// invalidation stands.
	if err := st.InvalidateMMItem(item, 77, "removed", "by hand"); err != nil {
		t.Fatalf("InvalidateMMItem: %v", err)
	}
	r2, err := FlagMMItem(st, MMFlagRequest{Slug: j.Slug, ItemID: item, Note: "r2"}, "sess-lead")
	if err != nil {
		t.Fatalf("FlagMMItem(r2): %v", err)
	}
	if err := ResolveMMFlag(st, r2.ID, "drop"); err != nil {
		t.Fatalf("ResolveMMFlag(drop of a dead item): %v", err)
	}
	if it, _ := st.GetMMItem(item); it.InvalidatedAt != 77 || it.InvalidReason != "removed" || it.InvalidDetail != "by hand" {
		t.Fatalf("drop re-invalidated a dead item: at=%d reason=%q detail=%q", it.InvalidatedAt, it.InvalidReason, it.InvalidDetail)
	}

	// a drop with ItemID 0 resolves and invalidates nothing.
	orphan, err := FlagMMItem(st, MMFlagRequest{Slug: j.Slug, Note: "transcript only"}, "sess-lead")
	if err != nil {
		t.Fatalf("FlagMMItem(orphan): %v", err)
	}
	if err := ResolveMMFlag(st, orphan.ID, "drop"); err != nil {
		t.Fatalf("ResolveMMFlag(drop, no item): %v", err)
	}
	if err := ResolveMMFlag(st, 987654, "keep"); err == nil {
		t.Error("ResolveMMFlag(unknown id) = nil, want an error")
	}
}

// ---------------------------------------------------------------------------
// the notes block
// ---------------------------------------------------------------------------

// "raised/dismissed flags are never listed; teammates/threads still get no
// notes" — with one pending flag as the control that the block exists at all.
func TestMindLayerEdgeNotesBlockOmitsRaisedAndDismissed(t *testing.T) {
	st := edgeMLStore(t)
	j := edgeMLJob(t, st, "edge-ml-notes", "kraken", "sess-lead-notes")

	mk := func(note string) store.MMFlag {
		f, err := FlagMMItem(st, MMFlagRequest{Slug: j.Slug, Note: note}, "sess-teammate")
		if err != nil {
			t.Fatalf("FlagMMItem(%s): %v", note, err)
		}
		return f
	}
	mk("edge-pending-note-zq1")
	raised := mk("edge-raised-note-zq2")
	dismissed := mk("edge-dismissed-note-zq3")
	if err := RaiseMMFlag(st, raised.ID, "sess-lead-notes", "ok"); err != nil {
		t.Fatalf("RaiseMMFlag: %v", err)
	}
	if err := DismissMMFlag(st, dismissed.ID, "sess-lead-notes", "no"); err != nil {
		t.Fatalf("DismissMMFlag: %v", err)
	}
	// a lead-created flag is born raised — never a note either.
	if _, err := FlagMMItem(st, MMFlagRequest{Slug: j.Slug, Note: "edge-born-raised-zq4"}, "sess-lead-notes"); err != nil {
		t.Fatalf("FlagMMItem(lead): %v", err)
	}

	// teammate / thread sessions: nothing at all (no goals exist either).
	for _, sess := range []string{"sess-teammate", "sess-thread-t1"} {
		if got := Recall(context.Background(), st, RecallRequest{SessionID: sess, Event: "UserPromptSubmit", Prompt: "p", Slug: j.Slug}); got != "" {
			t.Errorf("Recall(non-lead %q) = %q, want \"\"", sess, got)
		}
	}

	got := Recall(context.Background(), st, RecallRequest{SessionID: "sess-lead-notes", Event: "UserPromptSubmit", Prompt: "p", Slug: j.Slug})
	if !strings.Contains(got, "edge-pending-note-zq1") {
		t.Fatalf("lead Recall lacks the pending flag (control): %q", got)
	}
	for _, n := range []string{"edge-raised-note-zq2", "edge-dismissed-note-zq3", "edge-born-raised-zq4"} {
		if strings.Contains(got, n) {
			t.Errorf("lead Recall lists a non-pending flag %q:\n%s", n, got)
		}
	}
	// "lists each `pending` flag of that job once"
	again := Recall(context.Background(), st, RecallRequest{SessionID: "sess-lead-notes", Event: "UserPromptSubmit", Prompt: "p2", Slug: j.Slug})
	if strings.Contains(again, "edge-pending-note-zq1") {
		t.Errorf("a pending flag was delivered twice:\n%s", again)
	}
}

// `flagNoteKey(f) = "m:" + strconv.FormatInt(f.ID, 10)` — exactly, at the
// boundary ids.
func TestMindLayerEdgeFlagNoteKeyShape(t *testing.T) {
	for _, id := range []int64{0, 1, 42, 9_007_199_254_740_993} {
		if got, want := flagNoteKey(store.MMFlag{ID: id, Note: "x"}), "m:"+strconv.FormatInt(id, 10); got != want {
			t.Errorf("flagNoteKey(id %d) = %q, want %q", id, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// RenderMMFiles (pure)
// ---------------------------------------------------------------------------

const edgeMLRulesDir = ".claude/rules/kraken-mm"

func edgeMLLine(title string, id int64) string {
	ids := strconv.FormatInt(id, 10)
	return "- " + title + " — mm #" + ids + " (`kraken mm show " + ids + "`)"
}

func edgeMLHasLine(body, line string) bool {
	for _, l := range strings.Split(body, "\n") {
		if l == line {
			return true
		}
	}
	return false
}

// "a repo with no live items is absent from `Repos`"; "`Universal` … (`""`
// when none)"; "Only live items render."
func TestMindLayerEdgeRenderNothingLive(t *testing.T) {
	for name, items := range map[string][]store.MMItem{
		"nil":   nil,
		"empty": {},
		"only invalidated": {
			{ID: 1, Kind: "note", Source: "capture", Title: "dead repo", Scope: "repo", Repo: "kraken", Dir: "a", Content: "x", InvalidatedAt: 5},
			{ID: 2, Kind: "note", Source: "capture", Title: "dead uni", Scope: "universal", Content: "x", InvalidatedAt: 5},
		},
	} {
		r := RenderMMFiles(items)
		if len(r.Repos) != 0 {
			t.Errorf("%s: Repos = %v, want empty", name, r.Repos)
		}
		if r.Universal != "" {
			t.Errorf("%s: Universal = %q, want \"\"", name, r.Universal)
		}
	}
	// one live item in repo A and only dead ones in repo B: B is absent.
	r := RenderMMFiles([]store.MMItem{
		{ID: 1, Kind: "note", Source: "capture", Title: "live", Scope: "repo", Repo: "a", Content: "x"},
		{ID: 2, Kind: "note", Source: "capture", Title: "dead", Scope: "repo", Repo: "b", Content: "x", InvalidatedAt: 9},
	})
	if _, ok := r.Repos["b"]; ok {
		t.Errorf("repo with only invalidated items is present: %v", r.Repos["b"])
	}
	if _, ok := r.Repos["a"]; !ok {
		t.Errorf("repo with a live item is absent")
	}
}

// "(a title whose `title == ""` renders its first content line, cut at 120
// runes)".
func TestMindLayerEdgeRenderEmptyTitleFallsBackToFirstLine(t *testing.T) {
	long := strings.Repeat("é", 130)
	items := []store.MMItem{
		{ID: 3, Kind: "note", Source: "manual", Scope: "repo", Repo: "kraken", Content: "first line here\nsecond line"},
		{ID: 4, Kind: "note", Source: "manual", Scope: "repo", Repo: "kraken", Content: long + "\nnext"},
		{ID: 5, Kind: "note", Source: "manual", Scope: "repo", Repo: "kraken", Content: strings.Repeat("a", 120)},
	}
	r := RenderMMFiles(items)
	root := r.Repos["kraken"][edgeMLRulesDir+"/root.md"]
	if root == "" {
		t.Fatalf("no root.md rendered: %v", r.Repos["kraken"])
	}
	for _, want := range []string{
		edgeMLLine("first line here", 3),
		edgeMLLine(strings.Repeat("é", 120), 4),
		edgeMLLine(strings.Repeat("a", 120), 5),
	} {
		if !edgeMLHasLine(root, want) {
			t.Errorf("root.md lacks line %q:\n%s", want, root)
		}
	}
	if strings.Contains(root, "second line") || strings.Contains(root, "next") {
		t.Errorf("root.md carries more than the first content line:\n%s", root)
	}
}

// "titles are escaped so a `-->` or a leading `-` cannot break the file
// (`-->` → `--&gt;`, leading `-` prefixed with `\`)".
func TestMindLayerEdgeRenderEscapesTitles(t *testing.T) {
	items := []store.MMItem{
		{ID: 1, Kind: "note", Source: "capture", Title: "a --> b --> c", Scope: "repo", Repo: "kraken", Dir: "x", Content: "c"},
		{ID: 2, Kind: "note", Source: "capture", Title: "-starts with a dash", Scope: "repo", Repo: "kraken", Dir: "x", Content: "c"},
		{ID: 3, Kind: "note", Source: "capture", Title: "--> leading arrow", Scope: "universal", Content: "c"},
	}
	r := RenderMMFiles(items)
	files := r.Repos["kraken"]
	body := files[edgeMLRulesDir+"/x.md"]
	if body == "" {
		t.Fatalf("x.md not rendered: %v", files)
	}
	// The dir file is frontmatter + item lines only, so a raw `-->` anywhere in
	// it is a title escaping failure. (index.md and the universal file carry a
	// header whose exact text the contract does not fix, so only their item
	// lines are checked.)
	if strings.Contains(body, "-->") {
		t.Errorf("a raw --> survived rendering into x.md:\n%s", body)
	}
	if idx := files[edgeMLRulesDir+"/index.md"]; !edgeMLHasLine(idx, edgeMLLine("a --&gt; b --&gt; c", 1)) {
		t.Errorf("index.md lacks the escaped arrow line:\n%s", idx)
	}
	if !edgeMLHasLine(body, edgeMLLine("a --&gt; b --&gt; c", 1)) {
		t.Errorf("x.md lacks the escaped arrow line:\n%s", body)
	}
	if !edgeMLHasLine(body, edgeMLLine(`\-starts with a dash`, 2)) {
		t.Errorf("x.md lacks the escaped leading-dash line:\n%s", body)
	}
	if !strings.Contains(r.Universal, `\--&gt; leading arrow`) {
		t.Errorf("universal file lacks the doubly-escaped title:\n%s", r.Universal)
	}
}

// Two items in the same dir: one file, two lines, in id order — even when the
// input arrives newest first (ListMMItems' order).
func TestMindLayerEdgeRenderSameDirOneFileIDOrder(t *testing.T) {
	items := []store.MMItem{
		{ID: 9, Kind: "note", Source: "capture", Title: "nine", Scope: "repo", Repo: "kraken", Dir: "internal/jobs", Content: "c"},
		{ID: 2, Kind: "note", Source: "capture", Title: "two", Scope: "repo", Repo: "kraken", Dir: "internal/jobs", Content: "c"},
	}
	r := RenderMMFiles(items)
	files := r.Repos["kraken"]
	want := map[string]bool{edgeMLRulesDir + "/internal-jobs.md": true, edgeMLRulesDir + "/index.md": true}
	if len(files) != len(want) {
		t.Errorf("files = %v, want exactly internal-jobs.md + index.md", edgeMLKeys(files))
	}
	body := files[edgeMLRulesDir+"/internal-jobs.md"]
	if !strings.HasPrefix(body, "---\npaths:\n  - \"internal/jobs/**\"\n---\n") {
		t.Errorf("internal-jobs.md frontmatter wrong:\n%s", body)
	}
	i2 := strings.Index(body, edgeMLLine("two", 2))
	i9 := strings.Index(body, edgeMLLine("nine", 9))
	if i2 < 0 || i9 < 0 || i2 > i9 {
		t.Errorf("lines missing or not in id order (2 at %d, 9 at %d):\n%s", i2, i9, body)
	}
	if strings.Count(body, "— mm #") != 2 {
		t.Errorf("internal-jobs.md has %d item lines, want 2:\n%s", strings.Count(body, "— mm #"), body)
	}
	// "output is byte-identical for identical input"
	if again := RenderMMFiles(items); !reflect.DeepEqual(again, r) {
		t.Error("two renders of identical input differ")
	}
}

// "NO frontmatter for the repo root (`dir == ""`, file `root.md`)"; index.md
// is "unscoped" (no frontmatter either).
func TestMindLayerEdgeRenderRepoRootNoFrontmatter(t *testing.T) {
	r := RenderMMFiles([]store.MMItem{
		{ID: 1, Kind: "note", Source: "capture", Title: "root claim", Scope: "repo", Repo: "kraken", Dir: "", Content: "c"},
	})
	files := r.Repos["kraken"]
	root, ok := files[edgeMLRulesDir+"/root.md"]
	if !ok {
		t.Fatalf("root.md absent: %v", edgeMLKeys(files))
	}
	if strings.HasPrefix(root, "---") || strings.Contains(root, "paths:") {
		t.Errorf("root.md carries frontmatter:\n%s", root)
	}
	if !edgeMLHasLine(root, edgeMLLine("root claim", 1)) {
		t.Errorf("root.md lacks the item line:\n%s", root)
	}
	idx := files[edgeMLRulesDir+"/index.md"]
	if strings.HasPrefix(idx, "---") || strings.Contains(idx, "paths:") {
		t.Errorf("index.md carries frontmatter:\n%s", idx)
	}
	if !strings.Contains(idx, "kraken mm show") || !strings.Contains(idx, "kraken mm source") {
		t.Errorf("index.md header does not name show/source:\n%s", idx)
	}
	if _, ok := files[edgeMLRulesDir+"/.md"]; ok {
		t.Error("an empty dir slug rendered `.md` instead of root.md")
	}
}

func edgeMLKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// ---------------------------------------------------------------------------
// renderMMIntoWorktree — against a real temp git repo + one worktree
// ---------------------------------------------------------------------------

func edgeMLGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-C", dir, "-c", "user.email=edge@example.com", "-c", "user.name=edge", "-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main"}, args...)
	out, err := exec.Command("git", full...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// edgeMLGitRepo builds ~/Developer/<repo> with one commit (plus any extra
// tracked files) and one worktree; it returns (primary, worktree).
func edgeMLGitRepo(t *testing.T, repo string, tracked map[string]string) (string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	primary := filepath.Join(os.Getenv("HOME"), "Developer", repo)
	if err := os.MkdirAll(primary, 0o755); err != nil {
		t.Fatal(err)
	}
	edgeMLGit(t, primary, "init", "-q")
	files := map[string]string{"README.md": "hi\n"}
	for k, v := range tracked {
		files[k] = v
	}
	for rel, body := range files {
		p := filepath.Join(primary, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	edgeMLGit(t, primary, "add", "-A")
	edgeMLGit(t, primary, "commit", "-q", "-m", "init")
	wt := filepath.Join(os.Getenv("HOME"), ".kraken", "cells", "edge-ml-1", "wt-"+repo)
	if err := os.MkdirAll(filepath.Dir(wt), 0o755); err != nil {
		t.Fatal(err)
	}
	edgeMLGit(t, primary, "worktree", "add", "-q", "-b", "carl/edge-ml-1", wt)
	return primary, wt
}

// "a checkout whose exclude cannot be written gets no files" — the primary's
// `.git/info/exclude` (shared by the worktree) is read-only.
func TestMindLayerEdgeRenderIntoWorktreeReadOnlyExcludeWritesNothing(t *testing.T) {
	st := edgeMLStore(t)
	primary, wt := edgeMLGitRepo(t, "edgero", nil)
	if _, err := st.AddMMItem(store.MMItem{Kind: "note", Source: "capture", Title: "ro claim", Scope: "repo", Repo: "edgero", Dir: "src", Content: "c", CreatedAt: 1}); err != nil {
		t.Fatalf("AddMMItem: %v", err)
	}
	infoDir := filepath.Join(primary, ".git", "info")
	exclude := filepath.Join(infoDir, "exclude")
	if err := os.MkdirAll(infoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(exclude); os.IsNotExist(err) {
		if err := os.WriteFile(exclude, []byte("# git ls-files --others --exclude-from=.git/info/exclude\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	before, _ := os.ReadFile(exclude)
	// the file 0444 (the contract's recipe) AND its dir 0555, so a
	// write-temp-then-rename cannot sidestep the read-only file either.
	if err := os.Chmod(exclude, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(infoDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		os.Chmod(infoDir, 0o755)
		os.Chmod(exclude, 0o644)
	})

	_ = renderMMIntoWorktree(st, "edgero", wt) // "a store or render error logs and returns"

	if _, err := os.Stat(filepath.Join(wt, edgeMLRulesDir)); !os.IsNotExist(err) {
		entries, _ := os.ReadDir(filepath.Join(wt, edgeMLRulesDir))
		t.Errorf("worktree got rules files with an unwritable exclude: %v", entries)
	}
	if out := strings.TrimSpace(edgeMLGit(t, wt, "status", "--porcelain", "--untracked-files=normal")); out != "" {
		t.Errorf("worktree is dirty after the render:\n%s", out)
	}
	if after, _ := os.ReadFile(exclude); string(after) != string(before) {
		t.Errorf("the read-only exclude changed")
	}
}

// "if it reports ANYTHING, remove the dir and emit event `mm-render-unignored`
// (… or the repo tracks the path)" — the repo TRACKS a kraken-named file under
// `.claude/rules/kraken-mm/`, so the render's write shows in git status.
func TestMindLayerEdgeRenderIntoWorktreeTrackedPathIsRemoved(t *testing.T) {
	st := edgeMLStore(t)
	_, wt := edgeMLGitRepo(t, "edgetracked", map[string]string{
		edgeMLRulesDir + "/index.md": "carl's own tracked index\n",
	})
	if _, err := st.AddMMItem(store.MMItem{Kind: "note", Source: "capture", Title: "tracked claim", Scope: "repo", Repo: "edgetracked", Dir: "src", Content: "c", CreatedAt: 1}); err != nil {
		t.Fatalf("AddMMItem: %v", err)
	}

	_ = renderMMIntoWorktree(st, "edgetracked", wt)

	if _, err := os.Stat(filepath.Join(wt, edgeMLRulesDir, "src.md")); !os.IsNotExist(err) {
		t.Errorf("src.md survived in a worktree whose repo tracks %s (stat err %v)", edgeMLRulesDir, err)
	}
	if !edgeMLHasEvent(t, st, "mm-render-unignored") {
		t.Error("no mm-render-unignored event for a tracked rules dir")
	}
}

// ---------------------------------------------------------------------------
// ZeusTranscriptBlocks
// ---------------------------------------------------------------------------

// "a job with no session or no transcript returns an empty slice and `""`
// with nil error"; n outside 1..200 is clamped, never an error.
func TestMindLayerEdgeZeusTranscriptBlocksNoSession(t *testing.T) {
	edgeMLEnv(t)
	for name, j := range map[string]*Job{
		"no session":    {Slug: "edge-ml-tx", ID: 3, Repo: "kraken"},
		"no transcript": {Slug: "edge-ml-tx2", ID: 4, Repo: "kraken", Session: "1b6e0a7c-0000-4000-8000-00000000abcd"},
	} {
		for _, n := range []int{-5, 0, 1, 50, 200, 201, 100000} {
			blocks, sess, err := ZeusTranscriptBlocks(j, n)
			if err != nil {
				t.Errorf("%s n=%d: err = %v, want nil", name, n, err)
			}
			if len(blocks) != 0 || sess != "" {
				t.Errorf("%s n=%d: = %d blocks, session %q; want 0, \"\"", name, n, len(blocks), sess)
			}
		}
	}
}
