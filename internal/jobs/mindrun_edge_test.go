package jobs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"kraken/internal/store"
)

// mindrun_edge_test.go — the EDGE dimension of 50-contract.md slice C (the
// mind run runtime: parse, apply, marks, the runner's failure paths, replay,
// the retired thread role). Written blind from the frozen contract:
// mindrun.go, capturesource.go and the cell.go/daemon.go/thread.go diffs were
// never read. Refusals, error paths and "must not change"; the happy paths
// are athena-logic's. The launcher is always the `SetMindLauncherForTest`
// seam — no claude ever runs.
//
// Every package-level identifier is prefixed `edgeMR`.

// edgeMREnv wires a fresh store under an isolated HOME (useTestStore) with an
// empty ~/.claude/projects, and pins the knobs a run could read.
func edgeMREnv(t *testing.T) *store.Store {
	t.Helper()
	st := useTestStore(t)
	home := os.Getenv("HOME")
	if err := os.MkdirAll(filepath.Join(home, ".claude", "projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KRAKEN_OPERATOR", "Owner")
	t.Setenv("KRAKEN_NODES", "off")
	t.Setenv("KRAKEN_REPOS_DIR", t.TempDir())
	t.Setenv("CLAUDE_SECURESTORAGE_CONFIG_DIR", "")
	os.Unsetenv("CLAUDE_SECURESTORAGE_CONFIG_DIR")
	// MindPrompt reads mind/SKILL.md from SkillsFS; a run cannot reach the
	// launcher without it.
	prevFS := SkillsFS
	SkillsFS = fstest.MapFS{"mind/SKILL.md": {Data: []byte("edge mind skill for {{operator}}")}}
	t.Cleanup(func() { SkillsFS = prevFS })
	return st
}

type edgeMRLauncher struct {
	calls atomic.Int32
	fn    func(ctx context.Context, stdin string) (string, string, error)
}

// edgeMRLaunch installs a fake launcher for the test's lifetime.
func edgeMRLaunch(t *testing.T, fn func(ctx context.Context, stdin string) (string, string, error)) *edgeMRLauncher {
	t.Helper()
	l := &edgeMRLauncher{fn: fn}
	restore := SetMindLauncherForTest(func(ctx context.Context, argv []string, dir string, env []string, stdin string) (string, string, error) {
		l.calls.Add(1)
		return l.fn(ctx, stdin)
	})
	t.Cleanup(restore)
	return l
}

const edgeMREmptyAnswer = `{"ops":[],"excluded":[],"coverage":{}}`

// edgeMRDaily enqueues and claims one daily run over the last hour.
func edgeMRDaily(t *testing.T, st *store.Store) store.MindRun {
	t.Helper()
	now := time.Now().UnixMilli()
	id, err := st.EnqueueMindRun(store.MindRun{Trigger: "daily", Since: now - int64(time.Hour/time.Millisecond), Until: now, CreatedAt: now})
	if err != nil {
		t.Fatalf("EnqueueMindRun: %v", err)
	}
	r, err := st.ClaimNextMindRun(now + 1)
	if err != nil || r == nil || r.ID != id {
		t.Fatalf("ClaimNextMindRun = (%+v, %v), want run %d", r, err, id)
	}
	return *r
}

func edgeMRGetRun(t *testing.T, st *store.Store, id int64) store.MindRun {
	t.Helper()
	r, err := st.GetMindRun(id)
	if err != nil {
		t.Fatalf("GetMindRun(%d): %v", id, err)
	}
	return r
}

func edgeMRItems(t *testing.T, st *store.Store) []store.MMItem {
	t.Helper()
	all, err := st.ListMMItems(true)
	if err != nil {
		t.Fatal(err)
	}
	return all
}

func edgeMRFlags(t *testing.T, st *store.Store) int {
	t.Helper()
	all, err := st.ListMMFlags("")
	if err != nil {
		t.Fatal(err)
	}
	return len(all)
}

func edgeMRAddItem(t *testing.T, st *store.Store, it store.MMItem) int64 {
	t.Helper()
	if it.Kind == "" {
		it.Kind = "note"
	}
	if it.Source == "" {
		it.Source = "manual"
	}
	if it.Title == "" {
		it.Title = "title of " + it.Quote
	}
	if it.Content == "" {
		it.Content = "body of " + it.Quote
	}
	if it.CreatedAt == 0 {
		it.CreatedAt = time.Now().UnixMilli()
	}
	id, err := st.AddMMItem(it)
	if err != nil {
		t.Fatalf("AddMMItem(%+v): %v", it, err)
	}
	return id
}

func edgeMRProposal(quote string) *MMProposal {
	return &MMProposal{
		Title: "claim: " + quote, Scope: "universal", Kind: "note",
		Body: "Owner stated " + quote, Quote: quote, Rung: "rung 2: plain assertion",
		Source: CaptureSource{Kind: "transcript", Session: "sess-1", UUID: "uuid-1"},
	}
}

func edgeMRAdd(quote string) MindOp {
	return MindOp{Op: "add", Item: edgeMRProposal(quote), Quote: quote}
}

func edgeMRPendingMark(t *testing.T, st *store.Store, text string) int64 {
	t.Helper()
	id, _, err := st.AddMMMark(store.MMMark{Text: text, SourceJSON: `{"kind":"manual"}`, CreatedAt: time.Now().UnixMilli()})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func edgeMRMark(t *testing.T, st *store.Store, id int64) store.MMMark {
	t.Helper()
	all, err := st.ListMMMarks(0, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range all {
		if m.ID == id {
			return m
		}
	}
	t.Fatalf("mark %d not found", id)
	return store.MMMark{}
}

// ---------------------------------------------------------------------------
// ParseMindAnswer
// ---------------------------------------------------------------------------

// "an unparseable answer returns an error whose message carries the first
// 500 bytes" — bare junk, a fenced non-JSON block, and an envelope whose
// `result` is prose.
func TestEdgeMRParseMindAnswerRefusesNonJSON(t *testing.T) {
	long := "NOT-JSON-HEAD " + strings.Repeat("0123456789", 70)
	if _, _, _, err := ParseMindAnswer(long); err == nil {
		t.Error("ParseMindAnswer(700 bytes of prose) = nil error")
	} else if !strings.Contains(err.Error(), long[:500]) {
		t.Errorf("error %q does not carry the first 500 bytes of the output", edgeMRHead(err.Error(), 80))
	}

	for name, out := range map[string]string{
		"empty":        "",
		"whitespace":   "  \n\t ",
		"short prose":  "I could not find anything to propose.",
		"fenced prose": "```json\nnot an object\n```",
		"array":        `[{"op":"add"}]`,
		"truncated":    `{"ops":[{"op":"add"`,
	} {
		if _, _, _, err := ParseMindAnswer(out); err == nil {
			t.Errorf("%s: ParseMindAnswer(%q) = nil error, want a refusal", name, out)
		}
	}

	env := `{"type":"result","result":"I cannot answer in JSON tonight","usage":{"input_tokens":12,"output_tokens":3}}`
	_, _, _, err := ParseMindAnswer(env)
	if err == nil {
		t.Fatal("ParseMindAnswer(envelope with a prose result) = nil error")
	}
	if !strings.Contains(err.Error(), "I cannot answer in JSON tonight") {
		t.Errorf("envelope refusal %q does not carry the result's text", err)
	}
}

func edgeMRHead(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// ---------------------------------------------------------------------------
// ApplyMindOps — validate all, then apply
// ---------------------------------------------------------------------------

// Every invalid op shape, each after one VALID add: the call errors and
// writes nothing — no item, no flag, no consumed mark.
func TestEdgeMRApplyOneInvalidOpWritesNothing(t *testing.T) {
	st := edgeMREnv(t)
	run := edgeMRDaily(t, st)
	live := edgeMRAddItem(t, st, store.MMItem{Quote: "a live claim"})
	dead := edgeMRAddItem(t, st, store.MMItem{Quote: "a dead claim"})
	if err := st.InvalidateMMItem(dead, time.Now().UnixMilli(), "removed", ""); err != nil {
		t.Fatal(err)
	}
	code := edgeMRAddItem(t, st, store.MMItem{Kind: "reading", Source: "code", Quote: "func main() {}"})
	mark := edgeMRPendingMark(t, st, "marked words")

	reading := edgeMRProposal("a reading with no pointer")
	reading.Kind = "reading"
	reading.Source = CaptureSource{Kind: "artifact", Slug: "some-job", Name: "00-overview.md"}

	bad := map[string]MindOp{
		"unknown op":                   {Op: "merge", Target: live, Quote: "q"},
		"empty op":                     {Op: "", Target: live, Quote: "q"},
		"add with no item":             {Op: "add", Quote: "q"},
		"supersede with no item":       {Op: "supersede", Target: live, Quote: "q"},
		"reading without mark or save": {Op: "add", Item: reading, Quote: reading.Quote},
		"supersede a code reading":     {Op: "supersede", Target: code, Item: edgeMRProposal("replaces code"), Quote: "replaces code"},
		"retire a code reading":        {Op: "retire", Target: code, Quote: "func main() {}"},
		"supersede an invalidated row": {Op: "supersede", Target: dead, Item: edgeMRProposal("replaces dead"), Quote: "replaces dead"},
		"supersede an unknown target":  {Op: "supersede", Target: 987654, Item: edgeMRProposal("replaces nothing"), Quote: "replaces nothing"},
		"supersede with no target":     {Op: "supersede", Item: edgeMRProposal("replaces zero"), Quote: "replaces zero"},
		"retire an unknown target":     {Op: "retire", Target: 987654, Quote: "q"},
		"retire with no target":        {Op: "retire", Quote: "q"},
		"flag an unknown target":       {Op: "flag", Target: 987654, Quote: "q", Note: "contradicts"},
		"flag with no target":          {Op: "flag", Quote: "q", Note: "contradicts"},
	}
	for name, op := range bad {
		before := edgeMRItems(t, st)
		flags := edgeMRFlags(t, st)
		ans := MindAnswer{Ops: []MindOp{edgeMRAdd("valid add before " + name), op}}
		if _, err := ApplyMindOps(st, run, ans, []int64{mark}, time.Now().UnixMilli()); err == nil {
			t.Errorf("%s: ApplyMindOps = nil error, want a refusal", name)
		}
		if after := edgeMRItems(t, st); len(after) != len(before) {
			t.Errorf("%s: refused apply wrote %d item rows", name, len(after)-len(before))
		}
		if n := edgeMRFlags(t, st); n != flags {
			t.Errorf("%s: refused apply wrote %d flags", name, n-flags)
		}
		if m := edgeMRMark(t, st, mark); m.ConsumedRun != 0 {
			t.Errorf("%s: refused apply consumed mark %d (run %d)", name, mark, m.ConsumedRun)
		}
	}
	// the targets themselves are untouched
	for _, id := range []int64{live, code} {
		it, err := st.GetMMItem(id)
		if err != nil || it.InvalidatedAt != 0 {
			t.Errorf("item %d after refused applies = (%+v, %v), want still live", id, it, err)
		}
	}
}

// A7: "A `retire` whose target is already invalidated … applies as outcome
// `noop` with no write and no count".
func TestEdgeMRApplyRetireOfDeadTargetIsNoop(t *testing.T) {
	st := edgeMREnv(t)
	run := edgeMRDaily(t, st)
	dead := edgeMRAddItem(t, st, store.MMItem{Quote: "already gone"})
	if err := st.InvalidateMMItem(dead, time.Now().UnixMilli(), "removed", "by hand"); err != nil {
		t.Fatal(err)
	}
	before, _ := st.GetMMItem(dead)
	n := len(edgeMRItems(t, st))
	res, err := ApplyMindOps(st, run, MindAnswer{Ops: []MindOp{{Op: "retire", Target: dead, Quote: "already gone"}}}, nil, time.Now().UnixMilli())
	if err != nil {
		t.Fatalf("ApplyMindOps(retire of a dead target) = %v, want applied as noop", err)
	}
	if len(res.Results) != 1 || res.Results[0].Outcome != "noop" {
		t.Fatalf("results = %+v, want one noop", res.Results)
	}
	if res.Counts != (store.MindRunCounts{}) {
		t.Fatalf("counts = %+v, want all zero", res.Counts)
	}
	after, _ := st.GetMMItem(dead)
	if after.InvalidReason != before.InvalidReason || after.InvalidDetail != before.InvalidDetail || after.InvalidatedAt != before.InvalidatedAt {
		t.Fatalf("noop retire rewrote the dead row: %+v → %+v", before, after)
	}
	if len(edgeMRItems(t, st)) != n {
		t.Fatal("noop retire wrote an item row")
	}
}

// "refuse past `mmProposeCap` adds (`refused`, `cap`)" — not an error.
func TestEdgeMRApplyCapRefusesTheOverflow(t *testing.T) {
	st := edgeMREnv(t)
	run := edgeMRDaily(t, st)
	var ops []MindOp
	for i := 0; i <= mmProposeCap; i++ {
		ops = append(ops, edgeMRAdd(fmt.Sprintf("capped claim number %d", i)))
	}
	res, err := ApplyMindOps(st, run, MindAnswer{Ops: ops}, nil, time.Now().UnixMilli())
	if err != nil {
		t.Fatalf("ApplyMindOps(%d adds) = %v, want the overflow refused per op", len(ops), err)
	}
	if len(res.Results) != len(ops) {
		t.Fatalf("results = %d, want one per op (%d)", len(res.Results), len(ops))
	}
	last := res.Results[len(res.Results)-1]
	if last.Outcome != "refused" || last.Reason != "cap" {
		t.Fatalf("op %d past the cap = %q/%q, want refused/cap", mmProposeCap, last.Outcome, last.Reason)
	}
	if res.Counts.Added != mmProposeCap || res.Counts.Refused != 1 {
		t.Fatalf("counts = %+v, want added %d refused 1", res.Counts, mmProposeCap)
	}
	if n := len(edgeMRItems(t, st)); n != mmProposeCap {
		t.Fatalf("rows = %d, want %d", n, mmProposeCap)
	}
}

// "suppress on a quote whose rows are all invalidated `removed`/`contradicted`".
func TestEdgeMRApplySuppressesDroppedQuotes(t *testing.T) {
	st := edgeMREnv(t)
	run := edgeMRDaily(t, st)
	now := time.Now().UnixMilli()
	for quote, reason := range map[string]string{"dropped by hand": "removed", "contradicted later": "contradicted"} {
		id := edgeMRAddItem(t, st, store.MMItem{Quote: quote})
		if err := st.InvalidateMMItem(id, now, reason, ""); err != nil {
			t.Fatal(err)
		}
	}
	before := len(edgeMRItems(t, st))
	res, err := ApplyMindOps(st, run, MindAnswer{Ops: []MindOp{edgeMRAdd("dropped by hand"), edgeMRAdd("contradicted later")}}, nil, now+1)
	if err != nil {
		t.Fatalf("ApplyMindOps = %v", err)
	}
	for i, r := range res.Results {
		if r.Outcome != "suppressed" {
			t.Errorf("op %d outcome = %q, want suppressed", i, r.Outcome)
		}
	}
	if res.Counts.Suppressed != 2 || res.Counts.Added != 0 {
		t.Errorf("counts = %+v, want suppressed 2 added 0", res.Counts)
	}
	if after := len(edgeMRItems(t, st)); after != before {
		t.Errorf("suppressed adds wrote %d rows", after-before)
	}
}

// ---------------------------------------------------------------------------
// MarkRead
// ---------------------------------------------------------------------------

func TestEdgeMRMarkReadRefusals(t *testing.T) {
	st := edgeMREnv(t)
	mustCreateJob(t, st, store.Job{Slug: "edge-mr-draft", Repo: "kraken", Body: "b", State: ""})
	ok := CaptureSource{Kind: "manual"}
	now := time.Now()

	cases := []struct {
		name string
		req  MarkRequest
		want error
	}{
		{"empty text", MarkRequest{Slug: "edge-mr-draft", Text: "", Source: ok}, ErrCaptureBadSource},
		{"too long", MarkRequest{Slug: "edge-mr-draft", Text: strings.Repeat("é", captureMaxRunes+1), Source: ok}, ErrCaptureTooLong},
		{"bad source", MarkRequest{Slug: "edge-mr-draft", Text: "words", Source: CaptureSource{Kind: "bogus"}}, ErrCaptureBadSource},
		{"unknown slug", MarkRequest{Slug: "no-such-job", Text: "words", Source: ok}, ErrMarkNoJob},
	}
	for _, c := range cases {
		if _, err := MarkRead(st, c.req, now); !errors.Is(err, c.want) {
			t.Errorf("%s: MarkRead = %v, want %v", c.name, err, c.want)
		}
	}
	if all, _ := st.ListMMMarks(0, false); len(all) != 0 {
		t.Fatalf("refused marks stored %d rows", len(all))
	}

	// exactly at the cap is accepted (multibyte: the cap counts runes)
	res, err := MarkRead(st, MarkRequest{Slug: "edge-mr-draft", Text: strings.Repeat("é", captureMaxRunes), Source: ok}, now)
	if err != nil || res.ID == 0 || res.Pending != 1 {
		t.Fatalf("MarkRead at exactly captureMaxRunes = (%+v, %v), want accepted with pending 1", res, err)
	}
}

// "no live cell is required"; a draft job and an archived job are accepted;
// an empty slug stores `JobID` 0.
func TestEdgeMRMarkReadNeedsNoCell(t *testing.T) {
	st := edgeMREnv(t)
	t.Setenv("PATH", t.TempDir()) // no tmux anywhere: a liveness probe would fail
	draft := mustCreateJob(t, st, store.Job{Slug: "edge-mr-nocell", Repo: "kraken", Body: "b", State: ""})
	gone := mustCreateJob(t, st, store.Job{Slug: "edge-mr-archived", Repo: "kraken", Body: "b", State: "done"})
	if _, err := st.ArchiveJob(gone.ID); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	r1, err := MarkRead(st, MarkRequest{Slug: draft.Slug, Text: "first words", Source: CaptureSource{Kind: "manual"}}, now)
	if err != nil || r1.Pending != 1 {
		t.Fatalf("MarkRead(draft, no cell) = (%+v, %v), want pending 1", r1, err)
	}
	if _, err := MarkRead(st, MarkRequest{Slug: gone.Slug, Text: "archived words", Source: CaptureSource{Kind: "manual"}}, now); err != nil {
		t.Fatalf("MarkRead(archived job) = %v, want accepted", err)
	}
	r0, err := MarkRead(st, MarkRequest{Slug: "", Text: "library words", Source: CaptureSource{Kind: "manual"}}, now)
	if err != nil {
		t.Fatalf("MarkRead(empty slug) = %v, want accepted", err)
	}
	if m := edgeMRMark(t, st, r0.ID); m.JobID != 0 {
		t.Fatalf("empty-slug mark JobID = %d, want 0", m.JobID)
	}
	if m := edgeMRMark(t, st, r1.ID); m.JobID != draft.ID {
		t.Fatalf("draft mark JobID = %d, want %d", m.JobID, draft.ID)
	}
}

// ---------------------------------------------------------------------------
// RunMindRun — failure paths
// ---------------------------------------------------------------------------

// A launcher that errors: the run is `failed`, bundle + manifest are stored,
// there is no answer (no output), the pending mark is not consumed.
func TestEdgeMRRunLauncherErrorFailsKeepsBundle(t *testing.T) {
	st := edgeMREnv(t)
	mark := edgeMRPendingMark(t, st, "job-less marked words")
	l := edgeMRLaunch(t, func(ctx context.Context, stdin string) (string, string, error) {
		return "", "claude: exploded", errors.New("exit status 1")
	})
	run := edgeMRDaily(t, st)
	if err := RunMindRun(st, run); err == nil {
		t.Fatal("RunMindRun with a failing launcher = nil error")
	}
	if l.calls.Load() != 1 {
		t.Fatalf("launcher called %d times, want 1", l.calls.Load())
	}
	r := edgeMRGetRun(t, st, run.ID)
	if r.State != "failed" || r.FinishedAt == 0 || strings.TrimSpace(r.Note) == "" || strings.ContainsAny(r.Note, "\n\r") {
		t.Fatalf("run = state %s finished %d note %q, want failed with a one-line note", r.State, r.FinishedAt, r.Note)
	}
	b, err := st.GetMindRunBlobs(run.ID)
	if err != nil {
		t.Fatalf("GetMindRunBlobs after a launcher failure = %v, want bundle+manifest kept", err)
	}
	if len(b.BundleGz) == 0 || strings.TrimSpace(b.Manifest) == "" {
		t.Fatalf("blobs = bundle %d bytes manifest %q, want both", len(b.BundleGz), b.Manifest)
	}
	if b.Answer != "" {
		t.Fatalf("answer = %q after a launcher with no output, want none", b.Answer)
	}
	if m := edgeMRMark(t, st, mark); m.ConsumedRun != 0 {
		t.Fatalf("a failed run consumed the pending mark (run %d)", m.ConsumedRun)
	}
}

// Non-JSON stdout: `failed`, the answer blob holds the WHOLE stdout, the note
// carries its head, nothing is applied.
func TestEdgeMRRunNonJSONAnswerKeepsRawStdout(t *testing.T) {
	st := edgeMREnv(t)
	out := "I refuse to answer in JSON.\nSecond line of prose."
	edgeMRLaunch(t, func(ctx context.Context, stdin string) (string, string, error) { return out, "", nil })
	run := edgeMRDaily(t, st)
	if err := RunMindRun(st, run); err == nil {
		t.Fatal("RunMindRun with a non-JSON answer = nil error")
	}
	r := edgeMRGetRun(t, st, run.ID)
	if r.State != "failed" {
		t.Fatalf("run state = %s, want failed", r.State)
	}
	// Contract amendment A23 (lead's edit after the review round): the note
	// names the failure and points at the blob; the model's output never
	// reaches the note or the event log, only the answer blob below.
	if strings.Contains(r.Note, "I refuse to answer in JSON.") || !strings.Contains(r.Note, "answer kept on the run row") || strings.ContainsAny(r.Note, "\n\r") {
		t.Fatalf("note = %q, want one line naming the failure and the kept answer, without the output", r.Note)
	}
	b, err := st.GetMindRunBlobs(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if b.Answer != out {
		t.Fatalf("answer blob = %q, want the whole stdout %q", b.Answer, out)
	}
	if n := len(edgeMRItems(t, st)); n != 0 {
		t.Fatalf("a failed run wrote %d items", n)
	}
}

// "a document with an unknown op kind fails the whole run".
func TestEdgeMRRunUnknownOpFailsWholeRun(t *testing.T) {
	st := edgeMREnv(t)
	out := `{"ops":[{"op":"add","item":{"title":"t","scope":"universal","kind":"note","body":"b","quote":"fine claim","rung":"rung 2","source":{"kind":"transcript","session":"s","uuid":"u"}},"quote":"fine claim"},{"op":"merge","target":1,"quote":"q"}],"excluded":[],"coverage":{}}`
	edgeMRLaunch(t, func(ctx context.Context, stdin string) (string, string, error) { return out, "", nil })
	run := edgeMRDaily(t, st)
	if err := RunMindRun(st, run); err == nil {
		t.Fatal("RunMindRun with an unknown op = nil error")
	}
	if r := edgeMRGetRun(t, st, run.ID); r.State != "failed" {
		t.Fatalf("run state = %s, want failed", r.State)
	}
	if n := len(edgeMRItems(t, st)); n != 0 {
		t.Fatalf("a run with an unknown op applied %d items", n)
	}
}

// "A run whose child exceeds it is killed and `FailMindRun`'d with note
// starting `timed out`".
func TestEdgeMRRunTimeoutFailsWithTimedOut(t *testing.T) {
	st := edgeMREnv(t)
	prev := MindRunTimeout
	MindRunTimeout = 100 * time.Millisecond
	t.Cleanup(func() { MindRunTimeout = prev })
	edgeMRLaunch(t, func(ctx context.Context, stdin string) (string, string, error) {
		select {
		case <-ctx.Done():
			return "", "", ctx.Err()
		case <-time.After(10 * time.Second):
			return edgeMREmptyAnswer, "", nil
		}
	})
	run := edgeMRDaily(t, st)
	start := time.Now()
	if err := RunMindRun(st, run); err == nil {
		t.Fatal("RunMindRun past MindRunTimeout = nil error")
	}
	if el := time.Since(start); el > 5*time.Second {
		t.Fatalf("RunMindRun took %v, want it cut at the 100ms timeout", el)
	}
	r := edgeMRGetRun(t, st, run.ID)
	if r.State != "failed" || !strings.HasPrefix(r.Note, "timed out") {
		t.Fatalf("run = state %s note %q, want failed with a note starting \"timed out\"", r.State, r.Note)
	}
}

// "a bundle error leaves no blob row" — an unreadable projects dir makes the
// daily bundle's transcript read fail; the launcher never runs.
func TestEdgeMRRunBundleErrorLeavesNoBlob(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: mode bits do not deny access")
	}
	st := edgeMREnv(t)
	l := edgeMRLaunch(t, func(ctx context.Context, stdin string) (string, string, error) { return edgeMREmptyAnswer, "", nil })
	projects := filepath.Join(os.Getenv("HOME"), ".claude", "projects")
	if err := os.Chmod(projects, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(projects, 0o755) })
	run := edgeMRDaily(t, st)

	if _, _, err := BuildMindBundle(st, run); err == nil {
		t.Fatal("BuildMindBundle over an unreadable projects dir = nil error")
	}
	if err := RunMindRun(st, run); err == nil {
		t.Fatal("RunMindRun with a bundle read error = nil error")
	}
	if l.calls.Load() != 0 {
		t.Fatalf("launcher ran %d times after a bundle error", l.calls.Load())
	}
	if r := edgeMRGetRun(t, st, run.ID); r.State != "failed" || strings.TrimSpace(r.Note) == "" {
		t.Fatalf("run = state %s note %q, want failed with a note", r.State, r.Note)
	}
	if _, err := st.GetMindRunBlobs(run.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetMindRunBlobs after a bundle error = %v, want ErrNotFound (no blob row)", err)
	}
}

// ---------------------------------------------------------------------------
// ReplayMindRun
// ---------------------------------------------------------------------------

func edgeMRCompletedRun(t *testing.T, st *store.Store) store.MindRun {
	t.Helper()
	l := edgeMRLaunch(t, func(ctx context.Context, stdin string) (string, string, error) { return edgeMREmptyAnswer, "", nil })
	run := edgeMRDaily(t, st)
	if err := RunMindRun(st, run); err != nil {
		t.Fatalf("premise: RunMindRun with an empty answer = %v", err)
	}
	if r := edgeMRGetRun(t, st, run.ID); r.State != "done" {
		t.Fatalf("premise: run state = %s, want done", r.State)
	}
	_ = l
	return run
}

func TestEdgeMRReplayUnknownRunIsErrNotFound(t *testing.T) {
	st := edgeMREnv(t)
	l := edgeMRLaunch(t, func(ctx context.Context, stdin string) (string, string, error) { return edgeMREmptyAnswer, "", nil })
	if _, err := ReplayMindRun(st, 424242, false, time.Now()); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ReplayMindRun(unknown) = %v, want ErrNotFound", err)
	}
	if l.calls.Load() != 0 {
		t.Fatalf("launcher ran %d times for an unknown run", l.calls.Load())
	}
}

// "refuses a pruned bundle with `ErrMindBundlePruned` … whose message names
// the prune date" — for both write modes, with no launcher call and no row.
func TestEdgeMRReplayPrunedBundleIsRefused(t *testing.T) {
	st := edgeMREnv(t)
	run := edgeMRCompletedRun(t, st)
	pruneAt := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	if n, err := st.PruneMindRunBundles(time.Now().Add(time.Hour).UnixMilli(), pruneAt.UnixMilli()); err != nil || n != 1 {
		t.Fatalf("premise: prune = (%d, %v), want (1, nil)", n, err)
	}
	l := edgeMRLaunch(t, func(ctx context.Context, stdin string) (string, string, error) { return edgeMREmptyAnswer, "", nil })
	before, _ := st.ListMindRuns(0, 0)
	for _, write := range []bool{false, true} {
		_, err := ReplayMindRun(st, run.ID, write, time.Now())
		if !errors.Is(err, ErrMindBundlePruned) {
			t.Errorf("ReplayMindRun(pruned, write=%v) = %v, want ErrMindBundlePruned", write, err)
			continue
		}
		if !strings.Contains(err.Error(), "2026-09-01") {
			t.Errorf("pruned refusal %q does not name the prune date 2026-09-01", err)
		}
	}
	if l.calls.Load() != 0 {
		t.Fatalf("launcher ran %d times on a pruned bundle", l.calls.Load())
	}
	if after, _ := st.ListMindRuns(0, 0); len(after) != len(before) {
		t.Fatalf("refused replays created %d run rows", len(after)-len(before))
	}
}

// "`write == false` applies nothing and creates no row" — even when the new
// answer carries ops that would write.
func TestEdgeMRReplayDryRunWritesNothing(t *testing.T) {
	st := edgeMREnv(t)
	victim := edgeMRAddItem(t, st, store.MMItem{Quote: "a claim the replay would retire"})
	run := edgeMRCompletedRun(t, st)
	ans := fmt.Sprintf(`{"ops":[{"op":"add","item":{"title":"new claim","scope":"universal","kind":"note","body":"b","quote":"brand new claim","rung":"rung 2","source":{"kind":"transcript","session":"s","uuid":"u"}},"quote":"brand new claim"},{"op":"retire","target":%d,"quote":"a claim the replay would retire"}],"excluded":[],"coverage":{}}`, victim)
	edgeMRLaunch(t, func(ctx context.Context, stdin string) (string, string, error) { return ans, "", nil })
	runsBefore, _ := st.ListMindRuns(0, 0)
	itemsBefore := edgeMRItems(t, st)

	rep, err := ReplayMindRun(st, run.ID, false, time.Now())
	if err != nil {
		t.Fatalf("ReplayMindRun(dry) = %v", err)
	}
	if rep.NewRun != 0 {
		t.Errorf("dry replay NewRun = %d, want 0", rep.NewRun)
	}
	if runsAfter, _ := st.ListMindRuns(0, 0); len(runsAfter) != len(runsBefore) {
		t.Errorf("dry replay created %d run rows", len(runsAfter)-len(runsBefore))
	}
	itemsAfter := edgeMRItems(t, st)
	if len(itemsAfter) != len(itemsBefore) {
		t.Errorf("dry replay wrote %d items", len(itemsAfter)-len(itemsBefore))
	}
	if it, err := st.GetMMItem(victim); err != nil || it.InvalidatedAt != 0 {
		t.Errorf("dry replay retired item %d: (%+v, %v)", victim, it, err)
	}
}

// ---------------------------------------------------------------------------
// Small refusals
// ---------------------------------------------------------------------------

func TestEdgeMREnqueueForJobNilStoreRefused(t *testing.T) {
	if _, err := EnqueueMindRunForJob(nil, &Job{ID: 1, Slug: "x"}, "done", time.Now()); err == nil {
		t.Fatal("EnqueueMindRunForJob(nil store) = nil error")
	}
}

// "`BranchThreadWithRole(..., "mind")` returns `ErrThreadRoleRetired` (new
// sentinel) before any fork" — no row, no tmux.
func TestEdgeMRBranchMindRoleRetired(t *testing.T) {
	st := edgeMREnv(t)
	t.Setenv("PATH", t.TempDir())
	sj := mustCreateJob(t, st, store.Job{Slug: "edge-mr-thread", Repo: "kraken", Body: "b", State: "working", Session: "sess-lead"})
	j := &Job{ID: sj.ID, Slug: sj.Slug, Repo: "kraken"}
	if _, err := BranchThreadWithRole(j, "t", "seed", "", "mind"); !errors.Is(err, ErrThreadRoleRetired) {
		t.Fatalf("BranchThreadWithRole(mind) = %v, want ErrThreadRoleRetired", err)
	}
	threads, err := st.ListThreads(sj.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) != 0 {
		t.Fatalf("retired role left %d thread rows", len(threads))
	}
}

// "error when `SkillsFS` is nil or the file is absent".
func TestEdgeMRMindPromptNeedsTheSkill(t *testing.T) {
	prev := SkillsFS
	t.Cleanup(func() { SkillsFS = prev })

	SkillsFS = nil
	if text, sha, err := MindPrompt(); err == nil {
		t.Errorf("MindPrompt with nil SkillsFS = (%d bytes, %q, nil), want an error", len(text), sha)
	}
	SkillsFS = fstest.MapFS{"dream/SKILL.md": {Data: []byte("not the mind skill")}}
	if text, sha, err := MindPrompt(); err == nil {
		t.Errorf("MindPrompt with no mind/SKILL.md = (%d bytes, %q, nil), want an error", len(text), sha)
	}
}
