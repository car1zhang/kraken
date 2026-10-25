package jobs

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"kraken/internal/core"
	"kraken/internal/store"
	"kraken/internal/util"
)

// mindrun_contract_test.go — athena-logic's BLIND contract tests for
// artifacts/50-contract.md (job 674, mind runs), slice C: the run runtime —
// `MindPrompt`, `MindArgv`, `mindModel`, the launcher seam, `ParseMindAnswer`,
// `ApplyMindOps`, `BuildMindBundle`, `MindRunReport`, `RunMindRun`,
// `EnqueueMindRunForJob`, `DoneJob`'s enqueue, `kickMindDaily`,
// `mindRunPass`, `MarkRead`, `ReplayMindRun` and the `PruneMindRunBundles`
// wrapper.
//
// Written from the frozen contract alone; mindrun.go, capturesource.go and the
// cell.go/daemon.go/thread.go diffs were never read for their new code. The
// pre-existing shapes this file leans on (MMProposal, CaptureSource,
// MMEvidence* types, store.Thread/Feedback/DocQuestion/SavedArtifact, the
// committed helpers useTestStore, mustCreateJob, dreamNoon) were read through
// `git show HEAD:` or the committed test files.
//
// Dimension: LOGIC — invariants and happy paths. Refusals are athena-edge's.
// Every package-level identifier is prefixed `contractMR`.

const contractMRSkill = "# mind\n\nYou are {{operator}}'s mind pass. Answer with one JSON object.\n"

// contractMREnv isolates HOME, installs a store, gives the run a skill to
// read, a repos dir with a kraken checkout and a clean account env.
func contractMREnv(t *testing.T) *store.Store {
	t.Helper()
	st := useTestStore(t)
	t.Setenv("KRAKEN_OPERATOR", "Test Operator")
	t.Setenv("CLAUDE_POOL_DIR", filepath.Join(t.TempDir(), "claude-pool"))
	t.Setenv("CLAUDE_SECURESTORAGE_CONFIG_DIR", "")
	os.Unsetenv("CLAUDE_SECURESTORAGE_CONFIG_DIR")
	repos := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repos, "kraken", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KRAKEN_REPOS_DIR", repos)
	prev := SkillsFS
	SkillsFS = fstest.MapFS{"mind/SKILL.md": &fstest.MapFile{Data: []byte(contractMRSkill)}}
	t.Cleanup(func() { SkillsFS = prev })
	return st
}

type contractMRCall struct {
	Argv  []string
	Dir   string
	Env   []string
	Stdin string
}

type contractMRFake struct {
	mu     sync.Mutex
	calls  []contractMRCall
	stdout string
	during func(stdin string)
}

func (f *contractMRFake) set(stdout string) {
	f.mu.Lock()
	f.stdout = stdout
	f.mu.Unlock()
}

func (f *contractMRFake) n() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *contractMRFake) last(t *testing.T) contractMRCall {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		t.Fatal("launcher never called")
	}
	return f.calls[len(f.calls)-1]
}

// contractMRLauncher swaps the launcher seam (`SetMindLauncherForTest`) for a
// fake answering a fixed stdout.
func contractMRLauncher(t *testing.T, stdout string) *contractMRFake {
	t.Helper()
	f := &contractMRFake{stdout: stdout}
	restore := SetMindLauncherForTest(func(ctx context.Context, argv []string, dir string, env []string, stdin string) (string, string, error) {
		f.mu.Lock()
		f.calls = append(f.calls, contractMRCall{Argv: append([]string(nil), argv...), Dir: dir, Env: env, Stdin: stdin})
		out, during := f.stdout, f.during
		f.mu.Unlock()
		if during != nil {
			during(stdin)
		}
		return out, "", nil
	})
	t.Cleanup(restore)
	return f
}

// --- transcript fixtures (the typed-prompt record shape core's reader keeps) ---

func contractMRISO(ms int64) string { return time.UnixMilli(ms).UTC().Format(time.RFC3339Nano) }

func contractMRPrompt(t *testing.T, uuid, sess string, ts int64, text string, extra map[string]any) string {
	t.Helper()
	m := map[string]any{
		"type":         "user",
		"message":      map[string]any{"role": "user", "content": text},
		"uuid":         uuid,
		"timestamp":    contractMRISO(ts),
		"origin":       map[string]any{"kind": "human"},
		"promptSource": "typed",
		"sessionId":    sess,
	}
	for k, v := range extra {
		m[k] = v
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func contractMRAsst(t *testing.T, ts int64, text string) string {
	t.Helper()
	b, _ := json.Marshal(map[string]any{
		"type": "assistant", "timestamp": contractMRISO(ts),
		"message": map[string]any{"model": "claude-opus-5", "content": []any{map[string]any{"type": "text", "text": text}}},
	})
	return string(b)
}

func contractMRWrite(t *testing.T, project, sess string, lines ...string) string {
	t.Helper()
	p := filepath.Join(util.Home(), ".claude", "projects", project, sess+".jsonl")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// contractMRWindow is [now-1h, now+1h] with records at now-30m+n s.
func contractMRWindow() (since, until int64, at func(n int64) int64) {
	now := time.Now().UnixMilli()
	base := now - 30*60_000
	return now - 60*60_000, now + 60*60_000, func(n int64) int64 { return base + n*1000 }
}

const (
	contractMRProject      = "-Users-carl--kraken-cells-bundle-job-1-wt-kraken"
	contractMROtherProject = "-Users-carl--kraken-cells-sibling-2-wt-kraken"
	contractMRPlainProject = "-Users-carl-Developer-kraken"
)

func contractMRSet(xs []string) []string {
	m := map[string]bool{}
	for _, x := range xs {
		m[x] = true
	}
	out := []string{}
	for x := range m {
		out = append(out, x)
	}
	sort.Strings(out)
	return out
}

func contractMRIDSet(xs []int64) []int64 {
	m := map[int64]bool{}
	for _, x := range xs {
		m[x] = true
	}
	out := []int64{}
	for x := range m {
		out = append(out, x)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func contractMRPromptTexts(b MindBundle) []string {
	out := []string{}
	for _, p := range b.Prompts {
		out = append(out, p.Text)
	}
	sort.Strings(out)
	return out
}

func contractMRAddItem(t *testing.T, st *store.Store, it store.MMItem) int64 {
	t.Helper()
	if it.Kind == "" {
		it.Kind = "decision"
	}
	if it.Source == "" {
		it.Source = "capture"
	}
	if it.Content == "" {
		it.Content = "body"
	}
	if it.CreatedAt == 0 {
		it.CreatedAt = 1
	}
	id, err := st.AddMMItem(it)
	if err != nil {
		t.Fatalf("AddMMItem: %v", err)
	}
	return id
}

func contractMRGetItem(t *testing.T, st *store.Store, id int64) store.MMItem {
	t.Helper()
	it, err := st.GetMMItem(id)
	if err != nil {
		t.Fatalf("GetMMItem(%d): %v", id, err)
	}
	return it
}

// contractMRProposal is a valid add/supersede item rooted in a transcript.
func contractMRProposal(title, quote, sess, uuid string) map[string]any {
	return map[string]any{
		"title": title, "scope": "universal", "repo": "", "dir": "",
		"kind": "decision", "body": "Carl stated: " + quote, "quote": quote, "rung": "plain assertion",
		"source": map[string]any{"kind": "transcript", "session": sess, "uuid": uuid},
	}
}

func contractMRAnswerJSON(t *testing.T, ops []map[string]any, excluded []map[string]any) string {
	t.Helper()
	if ops == nil {
		ops = []map[string]any{}
	}
	if excluded == nil {
		excluded = []map[string]any{}
	}
	b, err := json.Marshal(map[string]any{"ops": ops, "excluded": excluded, "coverage": map[string]any{"prompts": len(ops)}})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func contractMRParse(t *testing.T, stdout string) MindAnswer {
	t.Helper()
	ans, _, _, err := ParseMindAnswer(stdout)
	if err != nil {
		t.Fatalf("ParseMindAnswer(%q): %v", stdout, err)
	}
	return ans
}

func contractMRRun(t *testing.T, st *store.Store, r store.MindRun) store.MindRun {
	t.Helper()
	if r.CreatedAt == 0 {
		r.CreatedAt = time.Now().UnixMilli()
	}
	id, err := st.EnqueueMindRun(r)
	if err != nil {
		t.Fatalf("EnqueueMindRun: %v", err)
	}
	got, err := st.GetMindRun(id)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func contractMRClaim(t *testing.T, st *store.Store, id int64) store.MindRun {
	t.Helper()
	r, err := st.ClaimNextMindRun(time.Now().UnixMilli())
	if err != nil || r == nil || r.ID != id {
		t.Fatalf("ClaimNextMindRun = %+v, %v; want run %d", r, err, id)
	}
	return *r
}

func contractMRGunzip(t *testing.T, b []byte) []byte {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("gunzip: %v", err)
	}
	var out bytes.Buffer
	if _, err := out.ReadFrom(zr); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// ---------------------------------------------------------------------------
// MindPrompt / mindModel / MindArgv
// ---------------------------------------------------------------------------

// "Reads `mind/SKILL.md` from `SkillsFS`, substitutes `{{operator}}` with
// `Operator()`, returns the text and its sha256 hex".
func TestContractMRMindPromptSubstitutesAndHashes(t *testing.T) {
	contractMREnv(t)
	text, sha, err := MindPrompt()
	if err != nil {
		t.Fatalf("MindPrompt: %v", err)
	}
	want := strings.ReplaceAll(contractMRSkill, "{{operator}}", Operator())
	if text != want {
		t.Errorf("text = %q, want %q", text, want)
	}
	if !strings.Contains(text, "Test Operator") {
		t.Errorf("text %q does not carry the operator", text)
	}
	sum := sha256.Sum256([]byte(text))
	if sha != hex.EncodeToString(sum[:]) {
		t.Errorf("sha = %q, want sha256 hex of the text", sha)
	}
}

// `mindModel` is `core.DefaultModel` (Q7).
func TestContractMRMindModelIsDefault(t *testing.T) {
	if got := mindModel(); got != core.DefaultModel {
		t.Errorf("mindModel() = %q, want %q", got, core.DefaultModel)
	}
}

func contractMRWantArgv(t *testing.T, model string) []string {
	t.Helper()
	text, _, err := MindPrompt()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{core.ClaudeLauncher(), "-p", "--strict-mcp-config", "--restricted", "--disable-slash-commands",
		"--permission-mode", "default", "--permission-prompts", "none", "--system-prompt-snapshot", "off",
		"--output-format", "json", "--tools", ""}
	want = append(want, core.ModelArgs(model)...)
	return append(want, "--append-system-prompt", text)
}

func contractMRSlotEnv(env []string) []string {
	out := []string{}
	for _, e := range env {
		if strings.HasPrefix(e, "CLAUDE_SECURESTORAGE_CONFIG_DIR=") {
			out = append(out, e)
		}
	}
	return out
}

// MindArgv: the exact argv, no fork/resume/session id, cwd falls back to
// `~/.kraken/mind` (created), and the account env follows AskArgv's rule.
func TestContractMRMindArgvShape(t *testing.T) {
	contractMREnv(t)
	j := &Job{ID: 41, Slug: "argv-job", Repo: "kraken", AccountSlug: "jess-max"}
	argv, cwd, env, err := MindArgv(j, mindModel())
	if err != nil {
		t.Fatalf("MindArgv: %v", err)
	}
	if want := contractMRWantArgv(t, mindModel()); !reflect.DeepEqual(argv, want) {
		t.Errorf("argv =\n%q\nwant\n%q", argv, want)
	}
	for _, bad := range []string{"--fork-session", "-r", "--session-id"} {
		for _, a := range argv {
			if a == bad {
				t.Errorf("argv carries %q", bad)
			}
		}
	}
	wantCwd := filepath.Join(util.Home(), ".kraken", "mind")
	if cwd != wantCwd {
		t.Errorf("cwd = %q, want %q (no worktree, no cell dir)", cwd, wantCwd)
	}
	if fi, err := os.Stat(wantCwd); err != nil || !fi.IsDir() {
		t.Errorf("~/.kraken/mind not created: %v", err)
	}
	if got := contractMRSlotEnv(env); len(got) != 1 || got[0] != "CLAUDE_SECURESTORAGE_CONFIG_DIR="+slotDir("jess-max") {
		t.Errorf("account env = %q, want exactly one entry for %s", got, slotDir("jess-max"))
	}
	if len(env) < 2 {
		t.Errorf("env = %d entries, want the process environment plus the slot", len(env))
	}
}

// "plus CLAUDE_SECURESTORAGE_CONFIG_DIR=<slotDir> exactly when j != nil &&
// j.AccountSlug != "" && j.Agent != "codex"".
func TestContractMRMindArgvAccountEnvRule(t *testing.T) {
	contractMREnv(t)
	cases := []struct {
		name string
		j    *Job
	}{
		{"nil job (daily run)", nil},
		{"unbound", &Job{ID: 1, Slug: "unbound", Repo: "kraken"}},
		{"codex", &Job{ID: 2, Slug: "codexy", Repo: "kraken", AccountSlug: "jess-max", Agent: "codex"}},
	}
	for _, c := range cases {
		argv, _, env, err := MindArgv(c.j, mindModel())
		if err != nil {
			t.Errorf("%s: MindArgv: %v", c.name, err)
			continue
		}
		if got := contractMRSlotEnv(env); len(got) != 0 {
			t.Errorf("%s: account env = %q, want none", c.name, got)
		}
		if want := contractMRWantArgv(t, mindModel()); !reflect.DeepEqual(argv, want) {
			t.Errorf("%s: argv = %q, want %q", c.name, argv, want)
		}
	}
}

// ---------------------------------------------------------------------------
// ParseMindAnswer
// ---------------------------------------------------------------------------

func contractMRSampleAnswer(t *testing.T) string {
	t.Helper()
	add := map[string]any{"op": "add", "item": contractMRProposal("dispatch groups own trades", "trades live in dispatch groups", "s1", "u1"), "quote": "trades live in dispatch groups"}
	ret := map[string]any{"op": "retire", "target": 12, "quote": "we dropped areas", "note": "areas are gone"}
	return contractMRAnswerJSON(t, []map[string]any{add, ret},
		[]map[string]any{{"rung": "hedge", "quote": "maybe the cap is ten", "why": "hedged"}})
}

func contractMRCheckSample(t *testing.T, what string, ans MindAnswer) {
	t.Helper()
	if len(ans.Ops) != 2 {
		t.Fatalf("%s: ops = %+v, want 2", what, ans.Ops)
	}
	a := ans.Ops[0]
	if a.Op != "add" || a.Item == nil || a.Item.Title != "dispatch groups own trades" || a.Item.Quote != "trades live in dispatch groups" ||
		a.Item.Source.Kind != "transcript" || a.Item.Source.Session != "s1" || a.Quote != "trades live in dispatch groups" {
		t.Errorf("%s: ops[0] = %+v item %+v", what, a, a.Item)
	}
	r := ans.Ops[1]
	if r.Op != "retire" || r.Target != 12 || r.Quote != "we dropped areas" || r.Note != "areas are gone" || r.Item != nil {
		t.Errorf("%s: ops[1] = %+v", what, r)
	}
	if len(ans.Excluded) != 1 || ans.Excluded[0] != (MindExclusion{Rung: "hedge", Quote: "maybe the cap is ten", Why: "hedged"}) {
		t.Errorf("%s: excluded = %+v", what, ans.Excluded)
	}
	if ans.Coverage == nil {
		t.Errorf("%s: coverage nil", what)
	}
}

// "Accepts … the bare answer JSON"; "raw is the answer text as found".
func TestContractMRParseBareAnswer(t *testing.T) {
	doc := contractMRSampleAnswer(t)
	ans, raw, _, err := ParseMindAnswer(doc)
	if err != nil {
		t.Fatalf("ParseMindAnswer: %v", err)
	}
	contractMRCheckSample(t, "bare", ans)
	if raw != doc {
		t.Errorf("raw = %q, want the stdout as found", raw)
	}
}

// "… or the --output-format json envelope (an object whose result string
// holds the answer, and whose usage.input_tokens/usage.output_tokens fill
// usage)".
func TestContractMRParseEnvelope(t *testing.T) {
	doc := contractMRSampleAnswer(t)
	env, _ := json.Marshal(map[string]any{
		"type": "result", "subtype": "success", "is_error": false, "result": doc,
		"usage": map[string]any{"input_tokens": 1234, "output_tokens": 56, "cache_read_input_tokens": 9},
	})
	ans, raw, usage, err := ParseMindAnswer(string(env))
	if err != nil {
		t.Fatalf("ParseMindAnswer(envelope): %v", err)
	}
	contractMRCheckSample(t, "envelope", ans)
	if usage != (MindUsage{In: 1234, Out: 56}) {
		t.Errorf("usage = %+v, want {1234 56}", usage)
	}
	if raw != doc {
		t.Errorf("raw = %q, want the result string %q", raw, doc)
	}
}

// "strips a leading/trailing ``` fence" — bare and inside the envelope.
func TestContractMRParseFencedAnswer(t *testing.T) {
	doc := contractMRSampleAnswer(t)
	fenced := "```\n" + doc + "\n```"
	contractMRCheckSample(t, "fenced bare", contractMRParse(t, fenced))
	env, _ := json.Marshal(map[string]any{"result": fenced, "usage": map[string]any{"input_tokens": 1, "output_tokens": 2}})
	ans, _, usage, err := ParseMindAnswer(string(env))
	if err != nil {
		t.Fatalf("fenced envelope: %v", err)
	}
	contractMRCheckSample(t, "fenced envelope", ans)
	if usage != (MindUsage{In: 1, Out: 2}) {
		t.Errorf("usage = %+v", usage)
	}
}

// ---------------------------------------------------------------------------
// ApplyMindOps
// ---------------------------------------------------------------------------

type contractMRApplyWorld struct {
	st  *store.Store
	job *store.Job
	run store.MindRun
}

func contractMRApplySetup(t *testing.T) contractMRApplyWorld {
	t.Helper()
	st := contractMREnv(t)
	j := mustCreateJob(t, st, store.Job{Slug: "apply-job", Repo: "kraken", Body: "x", State: "done", CreatedAt: 1000, UpdatedAt: 1000})
	run := contractMRRun(t, st, store.MindRun{JobID: j.ID, Slug: j.Slug, Trigger: "done", Since: 1000, Until: 5000})
	return contractMRApplyWorld{st: st, job: j, run: run}
}

func contractMRResult(t *testing.T, ap MindApply, i int) MindOpResult {
	t.Helper()
	for _, r := range ap.Results {
		if r.Index == i {
			return r
		}
	}
	t.Fatalf("no result for op %d in %+v", i, ap.Results)
	return MindOpResult{}
}

// add → "insert state `live`, source `nightly`, `RunID = run.ID`, `Slug/JobID`
// from the run (`added`)".
func TestContractMRApplyAddInsertsALiveNightlyRow(t *testing.T) {
	w := contractMRApplySetup(t)
	ans := contractMRParse(t, contractMRAnswerJSON(t, []map[string]any{
		{"op": "add", "item": contractMRProposal("the cap is twenty", "the cap is twenty per run", "s1", "u1"), "quote": "the cap is twenty per run"},
	}, nil))
	ap, err := ApplyMindOps(w.st, w.run, ans, nil, 7000)
	if err != nil {
		t.Fatalf("ApplyMindOps: %v", err)
	}
	r := contractMRResult(t, ap, 0)
	if r.Op != "add" || r.Outcome != "added" || r.ID <= 0 {
		t.Fatalf("result = %+v, want added with an id", r)
	}
	it := contractMRGetItem(t, w.st, r.ID)
	if it.State != "live" || it.Source != "nightly" || it.RunID != w.run.ID || it.JobID != w.job.ID || it.Slug != w.job.Slug {
		t.Errorf("added row = state %q source %q run %d job %d slug %q; want live/nightly/%d/%d/%q",
			it.State, it.Source, it.RunID, it.JobID, it.Slug, w.run.ID, w.job.ID, w.job.Slug)
	}
	if it.Title != "the cap is twenty" || it.Quote != "the cap is twenty per run" || it.Kind != "decision" || it.InvalidatedAt != 0 {
		t.Errorf("added row text = %+v", it)
	}
	if ap.Counts != (store.MindRunCounts{Added: 1}) {
		t.Errorf("counts = %+v, want Added 1", ap.Counts)
	}
	// visible to every live consumer with no confirm
	live, _ := w.st.ListMMItems(false)
	found := false
	for _, l := range live {
		found = found || l.ID == r.ID
	}
	if !found {
		t.Errorf("added row %d not in ListMMItems(false)", r.ID)
	}
}

// add folds "on a live/proposed byte-equal quote (`folded`, ID = existing)".
func TestContractMRApplyAddFoldsOntoALiveQuote(t *testing.T) {
	w := contractMRApplySetup(t)
	existing := contractMRAddItem(t, w.st, store.MMItem{Quote: "the cap is twenty per run", Title: "cap"})
	before, _ := w.st.ListMMItems(true)
	ans := contractMRParse(t, contractMRAnswerJSON(t, []map[string]any{
		{"op": "add", "item": contractMRProposal("cap again", "the cap is twenty per run", "s1", "u1"), "quote": "the cap is twenty per run"},
	}, nil))
	ap, err := ApplyMindOps(w.st, w.run, ans, nil, 7000)
	if err != nil {
		t.Fatal(err)
	}
	if r := contractMRResult(t, ap, 0); r.Outcome != "folded" || r.ID != existing {
		t.Errorf("result = %+v, want folded onto %d", r, existing)
	}
	after, _ := w.st.ListMMItems(true)
	if len(after) != len(before) {
		t.Errorf("rows %d → %d, a fold inserts nothing", len(before), len(after))
	}
	if ap.Counts.Folded != 1 || ap.Counts.Added != 0 {
		t.Errorf("counts = %+v, want Folded 1", ap.Counts)
	}
}

// add on "a quote whose rows are all invalidated removed/contradicted
// (`suppressed`)".
func TestContractMRApplyAddSuppressesADroppedQuote(t *testing.T) {
	w := contractMRApplySetup(t)
	dropped := contractMRAddItem(t, w.st, store.MMItem{Quote: "areas own the trades", Title: "areas"})
	if err := w.st.InvalidateMMItem(dropped, 10, "removed", "carl dropped it"); err != nil {
		t.Fatal(err)
	}
	ans := contractMRParse(t, contractMRAnswerJSON(t, []map[string]any{
		{"op": "add", "item": contractMRProposal("areas", "areas own the trades", "s1", "u1"), "quote": "areas own the trades"},
	}, nil))
	ap, err := ApplyMindOps(w.st, w.run, ans, nil, 7000)
	if err != nil {
		t.Fatal(err)
	}
	if r := contractMRResult(t, ap, 0); r.Outcome != "suppressed" {
		t.Errorf("result = %+v, want suppressed", r)
	}
	if ap.Counts.Suppressed != 1 || ap.Counts.Added != 0 {
		t.Errorf("counts = %+v, want Suppressed 1", ap.Counts)
	}
}

// supersede → "insert the new row with `Supersedes = Target`, then
// `store.SupersedeMMItem(Target, newID, at, "run <id>: <quote>")`
// (`superseded`)"; the old row keeps title, body and quote. Accepted on a
// captured item and on a manual note (Q4).
func TestContractMRApplySupersedeReplacesWithoutEditing(t *testing.T) {
	for _, src := range []string{"capture", "manual"} {
		t.Run(src, func(t *testing.T) {
			w := contractMRApplySetup(t)
			kind := "decision"
			if src == "manual" {
				kind = "note"
			}
			old := contractMRAddItem(t, w.st, store.MMItem{Kind: kind, Source: src, Title: "areas own trades", Content: "old body", Quote: "areas own the trades"})
			const q = "dispatch groups own the trades now"
			ans := contractMRParse(t, contractMRAnswerJSON(t, []map[string]any{
				{"op": "supersede", "target": old, "item": contractMRProposal("dispatch groups own trades", q, "s1", "u1"), "quote": q},
			}, nil))
			ap, err := ApplyMindOps(w.st, w.run, ans, nil, 8000)
			if err != nil {
				t.Fatalf("ApplyMindOps: %v", err)
			}
			r := contractMRResult(t, ap, 0)
			if r.Op != "supersede" || r.Outcome != "superseded" || r.ID <= 0 || r.ID == old {
				t.Fatalf("result = %+v, want superseded with the new id", r)
			}
			n := contractMRGetItem(t, w.st, r.ID)
			if n.Supersedes != old || n.RunID != w.run.ID || n.State != "live" || n.Source != "nightly" || n.InvalidatedAt != 0 || n.Quote != q {
				t.Errorf("new row = %+v, want supersedes %d, run %d, live nightly", n, old, w.run.ID)
			}
			o := contractMRGetItem(t, w.st, old)
			wantDetail := "run " + contractMRItoa(w.run.ID) + ": " + q
			if o.InvalidReason != "superseded" || o.SupersededBy != r.ID || o.InvalidatedAt != 8000 || o.InvalidDetail != wantDetail {
				t.Errorf("old row = reason %q by %d at %d detail %q; want superseded/%d/8000/%q",
					o.InvalidReason, o.SupersededBy, o.InvalidatedAt, o.InvalidDetail, r.ID, wantDetail)
			}
			if o.Title != "areas own trades" || o.Content != "old body" || o.Quote != "areas own the trades" {
				t.Errorf("old row text edited: %q/%q/%q", o.Title, o.Content, o.Quote)
			}
			if ap.Counts != (store.MindRunCounts{Superseded: 1}) {
				t.Errorf("counts = %+v, want Superseded 1", ap.Counts)
			}
		})
	}
}

// retire → `InvalidateMMItem(Target, at, "outdated", "run <id>: <quote>")`
// (`retired`).
func TestContractMRApplyRetireInvalidatesOutdated(t *testing.T) {
	w := contractMRApplySetup(t)
	tgt := contractMRAddItem(t, w.st, store.MMItem{Title: "the cap is ten", Quote: "cap ten"})
	const q = "we no longer cap at ten"
	ans := contractMRParse(t, contractMRAnswerJSON(t, []map[string]any{{"op": "retire", "target": tgt, "quote": q}}, nil))
	ap, err := ApplyMindOps(w.st, w.run, ans, nil, 9000)
	if err != nil {
		t.Fatal(err)
	}
	if r := contractMRResult(t, ap, 0); r.Op != "retire" || r.Outcome != "retired" {
		t.Errorf("result = %+v, want retired", r)
	}
	it := contractMRGetItem(t, w.st, tgt)
	if it.InvalidReason != "outdated" || it.InvalidatedAt != 9000 || it.InvalidDetail != "run "+contractMRItoa(w.run.ID)+": "+q {
		t.Errorf("retired row = %q at %d detail %q", it.InvalidReason, it.InvalidatedAt, it.InvalidDetail)
	}
	if it.Title != "the cap is ten" || it.Quote != "cap ten" {
		t.Errorf("retire edited text: %+v", it)
	}
	if ap.Counts != (store.MindRunCounts{Retired: 1}) {
		t.Errorf("counts = %+v", ap.Counts)
	}
}

// flag → "store.AddMMFlag state raised, RaisedBy "run:<id>", RaisedAt at, Note
// (flagged)".
func TestContractMRApplyFlagRaisesAFlag(t *testing.T) {
	w := contractMRApplySetup(t)
	tgt := contractMRAddItem(t, w.st, store.MMItem{Title: "the board polls every 3s", Quote: "poll 3s"})
	ans := contractMRParse(t, contractMRAnswerJSON(t, []map[string]any{
		{"op": "flag", "target": tgt, "quote": "it polls every 10s now", "note": "the poll interval changed"},
	}, nil))
	ap, err := ApplyMindOps(w.st, w.run, ans, nil, 9500)
	if err != nil {
		t.Fatal(err)
	}
	if r := contractMRResult(t, ap, 0); r.Op != "flag" || r.Outcome != "flagged" {
		t.Errorf("result = %+v, want flagged", r)
	}
	flags, err := w.st.ListMMFlags("raised")
	if err != nil {
		t.Fatal(err)
	}
	var got *store.MMFlag
	for i := range flags {
		if flags[i].ItemID == tgt {
			got = &flags[i]
		}
	}
	if got == nil {
		t.Fatalf("no raised flag on item %d among %+v", tgt, flags)
	}
	if got.State != "raised" || got.RaisedBy != "run:"+contractMRItoa(w.run.ID) || got.RaisedAt != 9500 || got.Note != "the poll interval changed" {
		t.Errorf("flag = %+v", *got)
	}
	if it := contractMRGetItem(t, w.st, tgt); it.InvalidatedAt != 0 {
		t.Errorf("a flag invalidated the item")
	}
	if ap.Counts != (store.MindRunCounts{Flagged: 1}) {
		t.Errorf("counts = %+v", ap.Counts)
	}
}

// "After every op, ConsumeMMMarks(markIDs, run.ID, at) whether or not any op
// cited a mark" — with an op that cites none, and with no ops at all.
func TestContractMRApplyConsumesEveryBundledMark(t *testing.T) {
	for _, withOp := range []bool{true, false} {
		w := contractMRApplySetup(t)
		m1, _, err := w.st.AddMMMark(store.MMMark{JobID: w.job.ID, Slug: w.job.Slug, Text: "read one", SourceJSON: `{"kind":"manual"}`, CreatedAt: 100})
		if err != nil {
			t.Fatal(err)
		}
		m2, _, _ := w.st.AddMMMark(store.MMMark{JobID: w.job.ID, Slug: w.job.Slug, Text: "read two", SourceJSON: `{"kind":"manual"}`, CreatedAt: 200})
		other, _, _ := w.st.AddMMMark(store.MMMark{JobID: w.job.ID, Slug: w.job.Slug, Text: "not bundled", SourceJSON: `{"kind":"manual"}`, CreatedAt: 300})
		var ops []map[string]any
		if withOp {
			ops = []map[string]any{{"op": "add", "item": contractMRProposal("x claim", "an unrelated statement", "s1", "u1"), "quote": "an unrelated statement"}}
		}
		if _, err := ApplyMindOps(w.st, w.run, contractMRParse(t, contractMRAnswerJSON(t, ops, nil)), []int64{m1, m2}, 4321); err != nil {
			t.Fatalf("withOp=%v: ApplyMindOps: %v", withOp, err)
		}
		marks, _ := w.st.ListMMMarks(w.job.ID, false)
		for _, m := range marks {
			switch m.ID {
			case m1, m2:
				if m.ConsumedRun != w.run.ID || m.ConsumedAt != 4321 {
					t.Errorf("withOp=%v: mark %d consumed run %d at %d, want %d/4321", withOp, m.ID, m.ConsumedRun, m.ConsumedAt, w.run.ID)
				}
			case other:
				if m.ConsumedRun != 0 {
					t.Errorf("withOp=%v: unbundled mark consumed", withOp)
				}
			}
		}
	}
}

// "Counts tally the outcomes" across a mixed document; a `reading` rooted in a
// mark is accepted.
func TestContractMRApplyMixedDocumentTallies(t *testing.T) {
	w := contractMRApplySetup(t)
	sup := contractMRAddItem(t, w.st, store.MMItem{Title: "old rule", Quote: "the old rule"})
	ret := contractMRAddItem(t, w.st, store.MMItem{Title: "stale rule", Quote: "the stale rule"})
	flg := contractMRAddItem(t, w.st, store.MMItem{Title: "doubtful rule", Quote: "the doubtful rule"})
	fold := contractMRAddItem(t, w.st, store.MMItem{Title: "kept rule", Quote: "the kept rule"})
	mark, _, _ := w.st.AddMMMark(store.MMMark{JobID: w.job.ID, Slug: w.job.Slug, Text: "the marked passage", SourceJSON: `{"kind":"artifact","name":"50-x.md"}`, CreatedAt: 1})
	reading := contractMRProposal("read the contract", "the marked passage", "", "")
	reading["kind"] = "reading"
	reading["source"] = map[string]any{"kind": "artifact", "slug": "apply-job", "name": "50-x.md", "mark_id": mark}
	ops := []map[string]any{
		{"op": "add", "item": contractMRProposal("a new rule", "the new rule", "s1", "u1"), "quote": "the new rule"},
		{"op": "add", "item": contractMRProposal("kept again", "the kept rule", "s1", "u2"), "quote": "the kept rule"},
		{"op": "supersede", "target": sup, "item": contractMRProposal("replacement rule", "the replacement rule", "s1", "u3"), "quote": "the replacement rule"},
		{"op": "retire", "target": ret, "quote": "that rule is stale"},
		{"op": "flag", "target": flg, "quote": "I doubt that", "note": "contradicted in prose"},
		{"op": "add", "item": reading, "quote": "the marked passage"},
	}
	ap, err := ApplyMindOps(w.st, w.run, contractMRParse(t, contractMRAnswerJSON(t, ops, nil)), []int64{mark}, 1234)
	if err != nil {
		t.Fatalf("ApplyMindOps: %v", err)
	}
	want := store.MindRunCounts{Added: 2, Folded: 1, Superseded: 1, Retired: 1, Flagged: 1}
	if ap.Counts != want {
		t.Errorf("counts = %+v, want %+v", ap.Counts, want)
	}
	if len(ap.Results) != len(ops) {
		t.Errorf("results = %d, want one per op (%d)", len(ap.Results), len(ops))
	}
	wantOutcome := []string{"added", "folded", "superseded", "retired", "flagged", "added"}
	for i, o := range wantOutcome {
		if r := contractMRResult(t, ap, i); r.Outcome != o {
			t.Errorf("op %d outcome = %q, want %q", i, r.Outcome, o)
		}
	}
	if r := contractMRResult(t, ap, 1); r.ID != fold {
		t.Errorf("fold id = %d, want %d", r.ID, fold)
	}
	if r := contractMRResult(t, ap, 5); r.ID > 0 {
		if it := contractMRGetItem(t, w.st, r.ID); it.Kind != "reading" || it.RunID != w.run.ID {
			t.Errorf("reading row = kind %q run %d", it.Kind, it.RunID)
		}
	}
}

// "Applying the same ops document twice folds every add" (50-components
// idempotency).
func TestContractMRApplyTwiceFoldsEveryAdd(t *testing.T) {
	w := contractMRApplySetup(t)
	doc := contractMRAnswerJSON(t, []map[string]any{
		{"op": "add", "item": contractMRProposal("one", "statement one", "s1", "u1"), "quote": "statement one"},
		{"op": "add", "item": contractMRProposal("two", "statement two", "s1", "u2"), "quote": "statement two"},
	}, nil)
	first, err := ApplyMindOps(w.st, w.run, contractMRParse(t, doc), nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ApplyMindOps(w.st, w.run, contractMRParse(t, doc), nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	if second.Counts != (store.MindRunCounts{Folded: 2}) {
		t.Errorf("second apply counts = %+v, want Folded 2", second.Counts)
	}
	for i := 0; i < 2; i++ {
		if a, b := contractMRResult(t, first, i), contractMRResult(t, second, i); b.ID != a.ID {
			t.Errorf("op %d folded onto %d, want %d", i, b.ID, a.ID)
		}
	}
}

// ---------------------------------------------------------------------------
// BuildMindBundle
// ---------------------------------------------------------------------------

type contractMRBundleWorld struct {
	st                       *store.Store
	job, sibling             *store.Job
	since, until             int64
	zeusPath, threadPath     string
	savedID                  int64
	pendingMark, consumedMk  int64
	liveIDs                  []int64
	inFeedback, inFeedbackTS int64
}

// contractMRBundleSetup: one job with a zeus session, a pane thread, a
// teammate root, a ★-saved artifact, a synced-only artifact, a pending and a
// consumed mark, feedback in and out of the window, an answered question; a
// sibling job with its own session; one live and one dead item.
func contractMRBundleSetup(t *testing.T, author string) contractMRBundleWorld {
	t.Helper()
	st := contractMREnv(t)
	since, until, at := contractMRWindow()
	w := contractMRBundleWorld{st: st, since: since, until: until}
	w.job = mustCreateJob(t, st, store.Job{Slug: "bundle-job", Repo: "kraken", Body: "THE SPEC BODY build the bundle", Author: author,
		State: "working", Session: "zeus-sess", CreatedAt: since - 1000, UpdatedAt: since - 1000})
	w.sibling = mustCreateJob(t, st, store.Job{Slug: "sibling", Repo: "kraken", Body: "x", State: "working", Session: "sib-sess",
		CreatedAt: since - 1000, UpdatedAt: since - 1000})
	if err := st.AddThread(store.Thread{JobID: w.job.ID, TID: "t1", Title: "q", SessionID: "thread-sess", ParentSession: "zeus-sess",
		State: "live", CreatedAt: since}); err != nil {
		t.Fatalf("AddThread: %v", err)
	}
	w.zeusPath = contractMRWrite(t, contractMRProject, "zeus-sess",
		contractMRPrompt(t, "z1", "zeus-sess", at(1), "ZEUS-PROMPT the cap is twenty", nil),
		contractMRAsst(t, at(2), "ok"),
		contractMRPrompt(t, "z2", "zeus-sess", at(3), "ZEUS-PROMPT trades live in dispatch groups", nil),
	)
	w.threadPath = contractMRWrite(t, contractMRProject, "thread-sess",
		contractMRPrompt(t, "t1u", "thread-sess", at(4), "THREAD-PROMPT why twenty", nil),
	)
	contractMRWrite(t, contractMRProject, "mate-sess",
		contractMRPrompt(t, "m1", "mate-sess", at(5), "TEAMMATE-BRIEF you are athena", map[string]any{"agentName": "athena-logic", "teamName": "x"}),
	)
	contractMRWrite(t, contractMROtherProject, "sib-sess",
		contractMRPrompt(t, "s1", "sib-sess", at(6), "SIBLING-PROMPT not this job", nil),
	)
	contractMRWrite(t, contractMRPlainProject, "plain-sess",
		contractMRPrompt(t, "p1", "plain-sess", at(7), "PLAIN-PROMPT an interactive session", nil),
	)

	var err error
	w.savedID, err = st.SaveArtifact(w.job.ID, w.job.Slug, "50-saved.md", []byte("SAVED TEXT the whole saved artifact"), since+10)
	if err != nil {
		t.Fatalf("SaveArtifact: %v", err)
	}
	if err := st.SyncArtifacts(w.job.ID, []store.ArtifactFile{{Path: "60-synced.md", Bytes: []byte("SYNCED TEXT never read"), Mtime: since + 20}}, nil); err != nil {
		t.Fatalf("SyncArtifacts: %v", err)
	}
	w.pendingMark, _, err = st.AddMMMark(store.MMMark{JobID: w.job.ID, Slug: w.job.Slug, Text: "PENDING MARK text", SourceJSON: `{"kind":"manual"}`, CreatedAt: since + 30})
	if err != nil {
		t.Fatal(err)
	}
	w.consumedMk, _, _ = st.AddMMMark(store.MMMark{JobID: w.job.ID, Slug: w.job.Slug, Text: "CONSUMED MARK text", SourceJSON: `{"kind":"manual"}`, CreatedAt: since + 31})
	if err := st.ConsumeMMMarks([]int64{w.consumedMk}, 999, since+32); err != nil {
		t.Fatal(err)
	}
	w.inFeedbackTS = since + 40
	w.inFeedback, err = st.AddFeedback(store.Feedback{JobID: w.job.ID, JobSlug: w.job.Slug, Artifact: "50-saved.md", Note: "IN-WINDOW note", TS: w.inFeedbackTS})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddFeedback(store.Feedback{JobID: w.job.ID, JobSlug: w.job.Slug, Artifact: "50-saved.md", Note: "BEFORE note", TS: since - 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddFeedback(store.Feedback{JobID: w.sibling.ID, JobSlug: w.sibling.Slug, Artifact: "x.md", Note: "SIBLING note", TS: since + 50}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetQuestions(w.job.ID, []store.DocQuestion{{QID: "Q1", Text: "which cap?", Importance: "P1", Recommendation: "ten"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.AnswerQuestion(w.job.ID, "Q1", "twenty, my own words", time.UnixMilli(since+60_000).UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	live := contractMRAddItem(t, st, store.MMItem{Title: "LIVE item", Quote: "live quote"})
	dead := contractMRAddItem(t, st, store.MMItem{Title: "DEAD item", Quote: "dead quote"})
	if err := st.InvalidateMMItem(dead, 5, "removed", "x"); err != nil {
		t.Fatal(err)
	}
	all, _ := st.ListMMItems(false)
	for _, it := range all {
		w.liveIDs = append(w.liveIDs, it.ID)
	}
	if len(w.liveIDs) != 1 || w.liveIDs[0] != live {
		t.Fatalf("fixture live items = %v", w.liveIDs)
	}
	return w
}

// The job run's bundle: prompts from zeus + thread sessions only; spec for an
// operator author; this job's in-window feedback and answers; the saved
// artifact whole; pending marks; every live item; the manifest lists exactly
// what the bundle holds.
func TestContractMRBuildMindBundleForAJob(t *testing.T) {
	w := contractMRBundleSetup(t, "operator")
	run := contractMRRun(t, w.st, store.MindRun{JobID: w.job.ID, Slug: w.job.Slug, Trigger: "done", Since: w.since, Until: w.until})
	b, man, err := BuildMindBundle(w.st, run)
	if err != nil {
		t.Fatalf("BuildMindBundle: %v", err)
	}
	if b.Run.RunID != run.ID || b.Run.JobID != w.job.ID || b.Run.Slug != w.job.Slug || b.Run.Trigger != "done" || b.Run.Since != w.since || b.Run.Until != w.until {
		t.Errorf("bundle run = %+v", b.Run)
	}
	wantPrompts := []string{"THREAD-PROMPT why twenty", "ZEUS-PROMPT the cap is twenty", "ZEUS-PROMPT trades live in dispatch groups"}
	if got := contractMRPromptTexts(b); !reflect.DeepEqual(got, wantPrompts) {
		t.Errorf("prompts = %q, want %q (zeus + thread only)", got, wantPrompts)
	}
	if b.Spec == nil || b.Spec.Body != JobSpecText(w.job.Body) {
		t.Errorf("spec = %+v, want the operator-authored body", b.Spec)
	}
	if len(b.Feedback) != 1 || b.Feedback[0].ID != w.inFeedback || b.Feedback[0].Note != "IN-WINDOW note" {
		t.Errorf("feedback = %+v, want only this job's in-window note", b.Feedback)
	}
	if len(b.Answers) != 1 || b.Answers[0].QID != "Q1" || b.Answers[0].Answer != "twenty, my own words" {
		t.Errorf("answers = %+v", b.Answers)
	}
	if len(b.Saved) != 1 {
		t.Fatalf("saved = %+v, want only the ★-saved artifact", b.Saved)
	}
	sv := b.Saved[0]
	if sv.ID != w.savedID || sv.Slug != w.job.Slug || sv.Name != "50-saved.md" || sv.Text != "SAVED TEXT the whole saved artifact" ||
		sv.SHA != store.SHA256Hex([]byte("SAVED TEXT the whole saved artifact")) {
		t.Errorf("saved[0] = %+v", sv)
	}
	if len(b.Marks) != 1 || b.Marks[0].ID != w.pendingMark || b.Marks[0].Text != "PENDING MARK text" || b.Marks[0].Source.Kind != "manual" {
		t.Errorf("marks = %+v, want only the pending mark", b.Marks)
	}
	gotItems := []int64{}
	for _, it := range b.Items {
		gotItems = append(gotItems, it.ID)
		if it.ID == w.liveIDs[0] && (it.Title != "LIVE item" || it.Quote != "live quote") {
			t.Errorf("item = %+v", it)
		}
	}
	if !reflect.DeepEqual(contractMRIDSet(gotItems), contractMRIDSet(w.liveIDs)) {
		t.Errorf("items = %v, want every live item %v", gotItems, w.liveIDs)
	}
	if b.Truncated == nil || len(b.Truncated) != 0 {
		t.Errorf("Truncated = %#v, want an empty non-nil slice", b.Truncated)
	}
	enc, _ := json.Marshal(b)
	for _, banned := range []string{"SIBLING-PROMPT", "TEAMMATE-BRIEF", "PLAIN-PROMPT", "SYNCED TEXT", "CONSUMED MARK", "BEFORE note", "SIBLING note", "DEAD item"} {
		if bytes.Contains(enc, []byte(banned)) {
			t.Errorf("bundle carries %q", banned)
		}
	}

	if got := contractMRSet(man.Sessions); !reflect.DeepEqual(got, []string{"thread-sess", "zeus-sess"}) {
		t.Errorf("manifest sessions = %v", man.Sessions)
	}
	if len(man.Sessions) != 2 {
		t.Errorf("manifest sessions not deduped: %v", man.Sessions)
	}
	if got := contractMRSet(man.TranscriptPaths); !reflect.DeepEqual(got, contractMRSet([]string{w.zeusPath, w.threadPath})) {
		t.Errorf("manifest paths = %v, want %v", man.TranscriptPaths, []string{w.zeusPath, w.threadPath})
	}
	if got := contractMRSet(man.UUIDs); !reflect.DeepEqual(got, []string{"t1u", "z1", "z2"}) {
		t.Errorf("manifest uuids = %v", man.UUIDs)
	}
	if !reflect.DeepEqual(man.MarkIDs, []int64{w.pendingMark}) || !reflect.DeepEqual(man.SavedIDs, []int64{w.savedID}) {
		t.Errorf("manifest marks/saved = %v/%v", man.MarkIDs, man.SavedIDs)
	}
	if !reflect.DeepEqual(contractMRIDSet(man.ItemIDs), contractMRIDSet(w.liveIDs)) {
		t.Errorf("manifest items = %v", man.ItemIDs)
	}
	if man.Bytes <= 0 {
		t.Errorf("manifest Bytes = %d, want the bundle size", man.Bytes)
	}
}

// "`Spec` is the job's body … only when `Author` is `operator` or `unknown`".
func TestContractMRBuildMindBundleSpecByAuthor(t *testing.T) {
	for author, want := range map[string]bool{"operator": true, "unknown": true, "agent": false, "linear": false, "kraken": false} {
		t.Run(author, func(t *testing.T) {
			w := contractMRBundleSetup(t, author)
			run := contractMRRun(t, w.st, store.MindRun{JobID: w.job.ID, Slug: w.job.Slug, Trigger: "done", Since: w.since, Until: w.until})
			b, _, err := BuildMindBundle(w.st, run)
			if err != nil {
				t.Fatal(err)
			}
			if got := b.Spec != nil; got != want {
				t.Errorf("author %s: spec present = %v, want %v", author, got, want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// RunMindRun end to end
// ---------------------------------------------------------------------------

var contractMRSections = []string{"Proposed", "Superseded", "Retired", "Folded", "Excluded", "Flagged", "Coverage"}

// contractMRSectionLines returns the line index of each section heading, in
// contract order, failing when one is missing or out of order.
func contractMRSectionLines(t *testing.T, report string) ([]string, []int) {
	t.Helper()
	lines := strings.Split(report, "\n")
	idx := []int{}
	for _, name := range contractMRSections {
		re := regexp.MustCompile(`^#{1,6}\s+` + name + `\b`)
		found := -1
		for i, l := range lines {
			if re.MatchString(l) {
				found = i
				break
			}
		}
		if found < 0 {
			t.Fatalf("report has no %q heading:\n%s", name, report)
		}
		idx = append(idx, found)
	}
	for i := 1; i < len(idx); i++ {
		if idx[i] <= idx[i-1] {
			t.Errorf("section %q (line %d) does not follow %q (line %d)", contractMRSections[i], idx[i], contractMRSections[i-1], idx[i-1])
		}
	}
	return lines, idx
}

func contractMRSectionBody(lines []string, idx []int, i int) string {
	end := len(lines)
	if i+1 < len(idx) {
		end = idx[i+1]
	}
	return strings.Join(lines[idx[i]+1:end], "\n")
}

// The integration scenario: zeus + thread transcripts, one mark, one saved
// artifact, one live item; the fake answers one add, one supersede of that
// item and one exclusion.
func TestContractMRRunMindRunEndToEnd(t *testing.T) {
	w := contractMRBundleSetup(t, "operator")
	old := w.liveIDs[0]
	addQ := "trades live in dispatch groups"
	supQ := "ZEUS-PROMPT the cap is twenty"
	answer := contractMRAnswerJSON(t, []map[string]any{
		{"op": "add", "item": contractMRProposal("dispatch groups own trades", addQ, "zeus-sess", "z2"), "quote": addQ},
		{"op": "supersede", "target": old, "item": contractMRProposal("the cap is twenty", supQ, "zeus-sess", "z1"), "quote": supQ},
	}, []map[string]any{{"rung": "hedge", "quote": "THREAD-PROMPT why twenty", "why": "a question, not an assertion"}})
	fake := contractMRLauncher(t, answer)
	queued := contractMRRun(t, w.st, store.MindRun{JobID: w.job.ID, Slug: w.job.Slug, Trigger: "done", Since: w.since, Until: w.until})
	run := contractMRClaim(t, w.st, queued.ID)

	var duringErr []string
	fake.during = func(stdin string) {
		b, err := w.st.GetMindRunBlobs(run.ID)
		if err != nil {
			duringErr = append(duringErr, "blobs not written before the launcher: "+err.Error())
			return
		}
		if len(b.BundleGz) == 0 || b.Manifest == "" {
			duringErr = append(duringErr, "bundle/manifest empty at launch")
		}
		if !bytes.Equal(contractMRGunzip(t, b.BundleGz), []byte(stdin)) {
			duringErr = append(duringErr, "stored bundle != stdin bundle")
		}
		items, _ := w.st.ListMMItems(true)
		for _, it := range items {
			if it.RunID == run.ID {
				duringErr = append(duringErr, "an op was applied before the launcher answered")
			}
		}
	}

	if err := RunMindRun(w.st, run); err != nil {
		t.Fatalf("RunMindRun: %v", err)
	}
	for _, e := range duringErr {
		t.Error(e)
	}
	if fake.n() != 1 {
		t.Fatalf("launcher called %d times, want 1", fake.n())
	}
	call := fake.last(t)
	var stdinBundle MindBundle
	if err := json.Unmarshal([]byte(call.Stdin), &stdinBundle); err != nil {
		t.Fatalf("stdin is not the bundle JSON: %v", err)
	}
	if stdinBundle.Run.RunID != run.ID || len(stdinBundle.Prompts) != 3 {
		t.Errorf("stdin bundle run %+v prompts %d", stdinBundle.Run, len(stdinBundle.Prompts))
	}
	if j := LoadJobBySlug(w.job.Slug); j != nil {
		if want, _, _, err := MindArgv(j, mindModel()); err == nil && !reflect.DeepEqual(call.Argv, want) {
			t.Errorf("launcher argv = %q, want MindArgv's %q", call.Argv, want)
		}
	}

	// items
	items, _ := w.st.ListMMItems(true)
	var added, replacement *store.MMItem
	for i := range items {
		it := items[i]
		if it.RunID != run.ID {
			continue
		}
		switch it.Quote {
		case addQ:
			added = &items[i]
		case supQ:
			replacement = &items[i]
		}
	}
	if added == nil || replacement == nil {
		t.Fatalf("run rows missing: added %v replacement %v", added, replacement)
	}
	if added.State != "live" || added.Source != "nightly" || replacement.Supersedes != old || replacement.State != "live" {
		t.Errorf("added %+v replacement %+v", *added, *replacement)
	}
	o := contractMRGetItem(t, w.st, old)
	if o.InvalidReason != "superseded" || o.SupersededBy != replacement.ID {
		t.Errorf("old row = %q by %d, want superseded by %d", o.InvalidReason, o.SupersededBy, replacement.ID)
	}
	live, _ := w.st.ListMMItems(false)
	liveIDs := []int64{}
	for _, it := range live {
		liveIDs = append(liveIDs, it.ID)
	}
	if !reflect.DeepEqual(contractMRIDSet(liveIDs), contractMRIDSet([]int64{added.ID, replacement.ID})) {
		t.Errorf("live items = %v, want the two new rows and not the old", liveIDs)
	}

	// marks
	marks, _ := w.st.ListMMMarks(w.job.ID, false)
	for _, m := range marks {
		if m.ID == w.pendingMark && m.ConsumedRun != run.ID {
			t.Errorf("pending mark consumed by run %d, want %d", m.ConsumedRun, run.ID)
		}
	}

	// run row
	r, err := w.st.GetMindRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r.State != "done" || r.FinishedAt == 0 {
		t.Errorf("run state %q finished %d, want done", r.State, r.FinishedAt)
	}
	if r.Added != 1 || r.Superseded != 1 || r.Retired != 0 || r.Flagged != 0 || r.Excluded != 1 {
		t.Errorf("counts added/superseded/retired/flagged/excluded = %d/%d/%d/%d/%d, want 1/1/0/0/1",
			r.Added, r.Superseded, r.Retired, r.Flagged, r.Excluded)
	}
	_, sha, _ := MindPrompt()
	if r.PromptSHA != sha || r.Model != mindModel() {
		t.Errorf("prompt sha %q model %q, want %q %q", r.PromptSHA, r.Model, sha, mindModel())
	}
	if r.BundleBytes <= 0 {
		t.Errorf("BundleBytes = %d", r.BundleBytes)
	}
	lines, idx := contractMRSectionLines(t, r.Report)
	if first := strings.TrimSpace(lines[0]); first == "" || idx[0] == 0 {
		t.Errorf("report does not open with a headline: %q", lines[0])
	}
	if !strings.Contains(contractMRSectionBody(lines, idx, 0), "dispatch groups own trades") {
		t.Errorf("Proposed section lacks the added title:\n%s", r.Report)
	}
	if !strings.Contains(contractMRSectionBody(lines, idx, 4), "THREAD-PROMPT why twenty") {
		t.Errorf("Excluded section lacks the excluded quote:\n%s", r.Report)
	}
	if !strings.Contains(contractMRSectionBody(lines, idx, 2), "None") {
		t.Errorf("empty Retired section does not say None:\n%s", r.Report)
	}

	// blobs
	bl, err := w.st.GetMindRunBlobs(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if bl.Answer != answer {
		t.Errorf("answer blob = %q, want the launcher's exact bytes %q", bl.Answer, answer)
	}
	var man MindManifest
	if err := json.Unmarshal([]byte(bl.Manifest), &man); err != nil {
		t.Fatalf("manifest JSON: %v", err)
	}
	if got := contractMRSet(man.Sessions); !reflect.DeepEqual(got, []string{"thread-sess", "zeus-sess"}) {
		t.Errorf("stored manifest sessions = %v", man.Sessions)
	}
	if !reflect.DeepEqual(man.MarkIDs, []int64{w.pendingMark}) || !reflect.DeepEqual(man.SavedIDs, []int64{w.savedID}) {
		t.Errorf("stored manifest marks/saved = %v/%v", man.MarkIDs, man.SavedIDs)
	}
	found := false
	for _, p := range man.TranscriptPaths {
		found = found || p == w.zeusPath
	}
	if !found {
		t.Errorf("stored manifest paths %v lack %s", man.TranscriptPaths, w.zeusPath)
	}
	if int64(len(contractMRGunzip(t, bl.BundleGz))) != int64(man.Bytes) {
		t.Errorf("manifest Bytes %d != gunzipped bundle %d", man.Bytes, len(contractMRGunzip(t, bl.BundleGz)))
	}
}

// RunMindRun with the --output-format json envelope stamps usage.
func TestContractMRRunMindRunStampsUsage(t *testing.T) {
	st := contractMREnv(t)
	j := mustCreateJob(t, st, store.Job{Slug: "usage-job", Repo: "kraken", Body: "x", State: "done", CreatedAt: 1, UpdatedAt: 1})
	env, _ := json.Marshal(map[string]any{"result": contractMRAnswerJSON(t, nil, nil), "usage": map[string]any{"input_tokens": 500, "output_tokens": 40}})
	contractMRLauncher(t, string(env))
	q := contractMRRun(t, st, store.MindRun{JobID: j.ID, Slug: j.Slug, Trigger: "done", Since: 1, Until: time.Now().UnixMilli()})
	run := contractMRClaim(t, st, q.ID)
	if err := RunMindRun(st, run); err != nil {
		t.Fatalf("RunMindRun: %v", err)
	}
	r, _ := st.GetMindRun(run.ID)
	if r.State != "done" || r.InTokens != 500 || r.OutTokens != 40 {
		t.Errorf("run = state %q in %d out %d, want done/500/40", r.State, r.InTokens, r.OutTokens)
	}
	if r.Added+r.Superseded+r.Retired+r.Flagged != 0 {
		t.Errorf("empty answer changed counts: %+v", r)
	}
}

// ---------------------------------------------------------------------------
// MindRunReport
// ---------------------------------------------------------------------------

// "sections in order Proposed, Superseded, Retired, Folded, Excluded, Flagged,
// Coverage, each present with None when empty; opens with a one-line headline".
func TestContractMRMindRunReportEmptySectionsSayNone(t *testing.T) {
	run := store.MindRun{ID: 3, JobID: 4, Slug: "rep", Trigger: "done", Since: 1, Until: 2, State: "running"}
	rep := MindRunReport(run, MindAnswer{Ops: []MindOp{}, Excluded: []MindExclusion{}, Coverage: map[string]any{}},
		MindApply{Results: []MindOpResult{}}, MindManifest{})
	lines, idx := contractMRSectionLines(t, rep)
	if idx[0] == 0 {
		t.Errorf("report opens with a section, want a headline first:\n%s", rep)
	}
	if strings.TrimSpace(lines[0]) == "" {
		t.Errorf("first line is blank, want the headline")
	}
	for i, name := range contractMRSections {
		if name == "Coverage" {
			continue // coverage may render the empty map or the manifest's own numbers
		}
		if body := contractMRSectionBody(lines, idx, i); !strings.Contains(body, "None") {
			t.Errorf("empty %s section = %q, want None", name, body)
		}
	}
}

// ---------------------------------------------------------------------------
// EnqueueMindRunForJob / DoneJob / kickMindDaily / mindRunPass
// ---------------------------------------------------------------------------

// "Since = LastMindRunUntil(j.ID, "done"), else the job row's CreatedAt; Until =
// now millis".
func TestContractMREnqueueMindRunForJobWindow(t *testing.T) {
	st := contractMREnv(t)
	row := mustCreateJob(t, st, store.Job{Slug: "enq-job", Repo: "kraken", Body: "x", State: "done", CreatedAt: 123_000, UpdatedAt: 123_000})
	j := LoadJobBySlug(row.Slug)
	if j == nil {
		t.Fatal("fixture did not load")
	}
	now1 := time.UnixMilli(500_000)
	id1, err := EnqueueMindRunForJob(st, j, "done", now1)
	if err != nil {
		t.Fatalf("EnqueueMindRunForJob: %v", err)
	}
	r1, _ := st.GetMindRun(id1)
	if r1.JobID != row.ID || r1.Trigger != "done" || r1.State != "queued" || r1.Since != 123_000 || r1.Until != 500_000 {
		t.Errorf("first run = %+v, want job %d done queued since 123000 until 500000", r1, row.ID)
	}
	now2 := time.UnixMilli(900_000)
	id2, err := EnqueueMindRunForJob(st, j, "done", now2)
	if err != nil {
		t.Fatal(err)
	}
	if id2 == id1 {
		t.Fatalf("second enqueue merged into the first")
	}
	r2, _ := st.GetMindRun(id2)
	if r2.Since != 500_000 || r2.Until != 900_000 {
		t.Errorf("second window = [%d, %d], want [500000, 900000]", r2.Since, r2.Until)
	}
}

// DoneJob "After the `state done` write succeeds, enqueues a `done` run";
// a job with no cell still enqueues.
func TestContractMRDoneJobEnqueuesADoneRun(t *testing.T) {
	st := contractMREnv(t)
	row := mustCreateJob(t, st, store.Job{Slug: "done-enq", Repo: "kraken", Body: "x", State: "working", CreatedAt: 77_000, UpdatedAt: 77_000})
	j := LoadJobBySlug(row.Slug)
	if j == nil {
		t.Fatal("fixture did not load")
	}
	if _, err := DoneJob(j, "shipped"); err != nil {
		t.Fatalf("DoneJob: %v", err)
	}
	runs, err := st.ListMindRuns(row.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs after Done = %+v, want exactly one", runs)
	}
	if runs[0].Trigger != "done" || runs[0].Since != 77_000 || runs[0].Until <= 77_000 {
		t.Errorf("run = %+v, want trigger done since created_at", runs[0])
	}
}

// kickMindDaily: "Once per local calendar date …: enqueues a daily run with
// Since = previous until (else now - 24h) and Until = now".
func TestContractMRKickMindDailyOncePerDate(t *testing.T) {
	st := contractMREnv(t)
	now := dreamNoon()
	id, ok := kickMindDaily(st, now)
	if !ok || id <= 0 {
		t.Fatalf("first kick = %d, %v; want an enqueue", id, ok)
	}
	r, _ := st.GetMindRun(id)
	if r.Trigger != "daily" || r.JobID != 0 || r.State != "queued" || r.Since != now.Add(-24*time.Hour).UnixMilli() || r.Until != now.UnixMilli() {
		t.Errorf("first daily = %+v", r)
	}
	if id2, ok := kickMindDaily(st, now.Add(2*time.Hour)); ok {
		t.Errorf("same-date kick enqueued %d", id2)
	}
	next := now.Add(24 * time.Hour)
	id3, ok := kickMindDaily(st, next)
	if !ok {
		t.Fatal("next-date kick did not enqueue")
	}
	r3, _ := st.GetMindRun(id3)
	if r3.Since != now.UnixMilli() || r3.Until != next.UnixMilli() {
		t.Errorf("second daily window = [%d, %d], want [%d, %d]", r3.Since, r3.Until, now.UnixMilli(), next.UnixMilli())
	}
}

// mindRunPass "calls kickMindDaily; when no run is in flight … claims the next
// queued row and runs RunMindRun in a goroutine".
func TestContractMRMindRunPassClaimsAndRuns(t *testing.T) {
	st := contractMREnv(t)
	contractMRLauncher(t, contractMRAnswerJSON(t, nil, nil))
	d := &daemonState{}
	d.mindRunPass(st, dreamNoon())
	deadline := time.Now().Add(5 * time.Second)
	for {
		runs, _ := st.ListMindRuns(0, 0)
		var daily *store.MindRun
		for i := range runs {
			if runs[i].Trigger == "daily" {
				daily = &runs[i]
			}
		}
		if daily != nil && (daily.State == "done" || daily.State == "failed") {
			if daily.State != "done" {
				t.Errorf("daily run ended %q (note %q), want done", daily.State, daily.Note)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("daily run never completed: %+v", runs)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// ---------------------------------------------------------------------------
// the daily catch-all bundle
// ---------------------------------------------------------------------------

// "For the daily run (JobID 0): prompts are every root session touched in the
// window minus sessions any done job run's manifest lists … and minus kraken's
// own dream-/mind- project dirs; Marks are every pending mark whose job is
// done or archived, plus every job-less mark; Saved, Spec, Feedback, Answers
// empty".
func TestContractMRDailyBundleHoldsOnlyUnsweptSessions(t *testing.T) {
	w := contractMRBundleSetup(t, "operator")
	_, _, at := contractMRWindow()
	contractMRWrite(t, "-Users-carl--kraken-cells-dream-2026-10-24-9-wt-kraken", "dream-sess",
		contractMRPrompt(t, "d1", "dream-sess", at(8), "DREAM-CELL prompt", nil))
	contractMRWrite(t, "-Users-carl--kraken-cells-mind-2026-10-24-8-wt-kraken", "mindcell-sess",
		contractMRPrompt(t, "mc1", "mindcell-sess", at(9), "MIND-CELL prompt", nil))

	// sweep the job's sessions with a real done run
	contractMRLauncher(t, contractMRAnswerJSON(t, nil, nil))
	q := contractMRRun(t, w.st, store.MindRun{JobID: w.job.ID, Slug: w.job.Slug, Trigger: "done", Since: w.since, Until: w.until})
	if err := RunMindRun(w.st, contractMRClaim(t, w.st, q.ID)); err != nil {
		t.Fatalf("job run: %v", err)
	}

	doneJob := mustCreateJob(t, w.st, store.Job{Slug: "finished", Repo: "kraken", Body: "x", State: "done", CreatedAt: 1, UpdatedAt: 1})
	working := mustCreateJob(t, w.st, store.Job{Slug: "busy", Repo: "kraken", Body: "x", State: "working", CreatedAt: 1, UpdatedAt: 1})
	archived := mustCreateJob(t, w.st, store.Job{Slug: "shelved", Repo: "kraken", Body: "x", State: "done", CreatedAt: 1, UpdatedAt: 1})
	doneMark, _, _ := w.st.AddMMMark(store.MMMark{JobID: doneJob.ID, Slug: doneJob.Slug, Text: "DONE-JOB mark", SourceJSON: `{"kind":"manual"}`, CreatedAt: 10})
	busyMark, _, _ := w.st.AddMMMark(store.MMMark{JobID: working.ID, Slug: working.Slug, Text: "WORKING-JOB mark", SourceJSON: `{"kind":"manual"}`, CreatedAt: 11})
	archMark, _, _ := w.st.AddMMMark(store.MMMark{JobID: archived.ID, Slug: archived.Slug, Text: "ARCHIVED-JOB mark", SourceJSON: `{"kind":"manual"}`, CreatedAt: 12})
	freeMark, _, _ := w.st.AddMMMark(store.MMMark{JobID: 0, Text: "JOBLESS mark", SourceJSON: `{"kind":"manual"}`, CreatedAt: 13})
	if _, err := w.st.ArchiveJob(archived.ID); err != nil {
		t.Fatalf("ArchiveJob: %v", err)
	}

	daily := contractMRRun(t, w.st, store.MindRun{Trigger: "daily", Since: w.since, Until: w.until})
	b, man, err := BuildMindBundle(w.st, daily)
	if err != nil {
		t.Fatalf("BuildMindBundle(daily): %v", err)
	}
	texts := contractMRPromptTexts(b)
	for _, want := range []string{"PLAIN-PROMPT an interactive session"} {
		found := false
		for _, x := range texts {
			found = found || x == want
		}
		if !found {
			t.Errorf("daily prompts %q lack the unswept %q", texts, want)
		}
	}
	// Contract amendment A14 (lead's edit after the review round): the sibling
	// job is still working, so its session belongs to its own Done run and the
	// daily bundle leaves it out beside the swept and kraken-owned sessions.
	for _, x := range texts {
		if strings.HasPrefix(x, "ZEUS-PROMPT") || strings.HasPrefix(x, "THREAD-PROMPT") || strings.HasPrefix(x, "DREAM-CELL") || strings.HasPrefix(x, "MIND-CELL") || strings.HasPrefix(x, "SIBLING-PROMPT") {
			t.Errorf("daily bundle carries %q", x)
		}
	}
	gotMarks := []int64{}
	for _, m := range b.Marks {
		gotMarks = append(gotMarks, m.ID)
	}
	if want := contractMRIDSet([]int64{doneMark, archMark, freeMark}); !reflect.DeepEqual(contractMRIDSet(gotMarks), want) {
		t.Errorf("daily marks = %v, want %v (not the working job's %d)", gotMarks, want, busyMark)
	}
	if b.Spec != nil || len(b.Saved) != 0 || len(b.Feedback) != 0 || len(b.Answers) != 0 {
		t.Errorf("daily spec/saved/feedback/answers = %v/%d/%d/%d, want empty", b.Spec, len(b.Saved), len(b.Feedback), len(b.Answers))
	}
	if b.Saved == nil || b.Feedback == nil || b.Answers == nil || b.Marks == nil || b.Prompts == nil || b.Items == nil {
		t.Errorf("a daily bundle slice is nil: %+v", b)
	}
	if !reflect.DeepEqual(contractMRIDSet(man.MarkIDs), contractMRIDSet(gotMarks)) {
		t.Errorf("manifest marks %v != bundle marks %v", man.MarkIDs, gotMarks)
	}
}

// ---------------------------------------------------------------------------
// MarkRead
// ---------------------------------------------------------------------------

// "no live cell is required; stores the mark (AddMMMark) …; Pending is the
// job's pending count after the write"; the same text and source folds.
func TestContractMRMarkReadHappyPath(t *testing.T) {
	st := contractMREnv(t)
	row := mustCreateJob(t, st, store.Job{Slug: "mark-job", Repo: "kraken", Body: "x", State: "done", CreatedAt: 1, UpdatedAt: 1})
	src := CaptureSource{Kind: "artifact", Slug: "mark-job", Name: "50-contract.md"}
	now := time.UnixMilli(10_000_000)
	r1, err := MarkRead(st, MarkRequest{Slug: "mark-job", Text: "the contract freezes signatures", Source: src}, now)
	if err != nil {
		t.Fatalf("MarkRead: %v", err)
	}
	if r1.ID <= 0 || r1.Pending != 1 || r1.Folded {
		t.Errorf("first mark = %+v, want id, pending 1, not folded", r1)
	}
	r2, err := MarkRead(st, MarkRequest{Slug: "mark-job", Text: "the contract freezes signatures", Source: src}, now.Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if r2.ID != r1.ID || !r2.Folded || r2.Pending != 1 {
		t.Errorf("repeat within a minute = %+v, want folded onto %d with pending 1", r2, r1.ID)
	}
	r3, err := MarkRead(st, MarkRequest{Slug: "mark-job", Text: "a different passage", Source: src}, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if r3.ID == r1.ID || r3.Folded || r3.Pending != 2 {
		t.Errorf("second passage = %+v, want a new mark, pending 2", r3)
	}
	marks, _ := st.ListMMMarks(row.ID, true)
	if len(marks) != 2 {
		t.Fatalf("stored marks = %+v", marks)
	}
	for _, m := range marks {
		if m.JobID != row.ID || m.Slug != "mark-job" || m.SourceJSON != captureSourceJSON(src) {
			t.Errorf("stored mark = %+v, want job %d slug mark-job source %s", m, row.ID, captureSourceJSON(src))
		}
	}
	// no run of its own
	if runs, _ := st.ListMindRuns(0, 0); len(runs) != 0 {
		t.Errorf("a mark enqueued runs: %+v", runs)
	}
}

// "stores the mark … with JobID 0 for an empty slug".
func TestContractMRMarkReadWithNoSlug(t *testing.T) {
	st := contractMREnv(t)
	r, err := MarkRead(st, MarkRequest{Text: "a library passage", Source: CaptureSource{Kind: "manual"}}, time.UnixMilli(5000))
	if err != nil {
		t.Fatalf("MarkRead(no slug): %v", err)
	}
	marks, _ := st.ListMMMarks(0, true)
	if len(marks) != 1 || marks[0].ID != r.ID || marks[0].JobID != 0 || marks[0].Text != "a library passage" {
		t.Errorf("job-less mark = %+v", marks)
	}
}

// ---------------------------------------------------------------------------
// ReplayMindRun
// ---------------------------------------------------------------------------

func contractMRRecordedRun(t *testing.T) (contractMRBundleWorld, store.MindRun, string, *contractMRFake) {
	t.Helper()
	w := contractMRBundleSetup(t, "operator")
	answer := contractMRAnswerJSON(t, []map[string]any{
		{"op": "add", "item": contractMRProposal("dispatch groups own trades", "trades live in dispatch groups", "zeus-sess", "z2"), "quote": "trades live in dispatch groups"},
	}, []map[string]any{{"rung": "hedge", "quote": "THREAD-PROMPT why twenty", "why": "question"}})
	fake := contractMRLauncher(t, answer)
	q := contractMRRun(t, w.st, store.MindRun{JobID: w.job.ID, Slug: w.job.Slug, Trigger: "done", Since: w.since, Until: w.until})
	run := contractMRClaim(t, w.st, q.ID)
	if err := RunMindRun(w.st, run); err != nil {
		t.Fatalf("RunMindRun: %v", err)
	}
	return w, run, answer, fake
}

func contractMRCounts(t *testing.T, st *store.Store) (items, runs int) {
	t.Helper()
	all, _ := st.ListMMItems(true)
	rs, _ := st.ListMindRuns(0, 0)
	return len(all), len(rs)
}

// "a replay of a run against itself under the same prompt and a fake launcher
// returning the recorded answer prints an empty diff"; write == false writes
// nothing.
func TestContractMRReplayAgainstItselfIsEmpty(t *testing.T) {
	w, run, _, fake := contractMRRecordedRun(t)
	items0, runs0 := contractMRCounts(t, w.st)
	rep, err := ReplayMindRun(w.st, run.ID, false, time.Now())
	if err != nil {
		t.Fatalf("ReplayMindRun: %v", err)
	}
	if fake.n() != 2 {
		t.Errorf("launcher calls = %d, want 2 (the run and the replay)", fake.n())
	}
	if rep.SourceRun != run.ID || rep.NewRun != 0 {
		t.Errorf("replay source %d new %d, want %d/0", rep.SourceRun, rep.NewRun, run.ID)
	}
	_, sha, _ := MindPrompt()
	if rep.OldPromptSHA != sha || rep.NewPromptSHA != sha {
		t.Errorf("prompt shas = %q/%q, want %q", rep.OldPromptSHA, rep.NewPromptSHA, sha)
	}
	if len(rep.Diff.OpsAdded) != 0 || len(rep.Diff.OpsLost) != 0 || len(rep.Diff.ExclusionsChanged) != 0 {
		t.Errorf("self diff = %+v, want empty", rep.Diff)
	}
	if len(rep.Old.Ops) != 1 || len(rep.New.Ops) != 1 {
		t.Errorf("old/new ops = %d/%d, want 1/1", len(rep.Old.Ops), len(rep.New.Ops))
	}
	if items1, runs1 := contractMRCounts(t, w.st); items1 != items0 || runs1 != runs0 {
		t.Errorf("a dry replay wrote: items %d→%d runs %d→%d", items0, items1, runs0, runs1)
	}
	// the launcher read the stored bundle
	bl, _ := w.st.GetMindRunBlobs(run.ID)
	if got := fake.last(t).Stdin; got != string(contractMRGunzip(t, bl.BundleGz)) {
		t.Errorf("replay stdin is not the stored bundle")
	}
}

// A different answer: the diff names the ops that differ and writes nothing.
func TestContractMRReplayDiffsADifferentAnswer(t *testing.T) {
	w, run, _, fake := contractMRRecordedRun(t)
	fake.set(contractMRAnswerJSON(t, []map[string]any{
		{"op": "add", "item": contractMRProposal("the cap is twenty", "ZEUS-PROMPT the cap is twenty", "zeus-sess", "z1"), "quote": "ZEUS-PROMPT the cap is twenty"},
	}, []map[string]any{{"rung": "hedge", "quote": "THREAD-PROMPT why twenty", "why": "question"}}))
	items0, runs0 := contractMRCounts(t, w.st)
	rep, err := ReplayMindRun(w.st, run.ID, false, time.Now())
	if err != nil {
		t.Fatalf("ReplayMindRun: %v", err)
	}
	if len(rep.Diff.OpsAdded) != 1 || rep.Diff.OpsAdded[0].Quote != "ZEUS-PROMPT the cap is twenty" {
		t.Errorf("OpsAdded = %+v", rep.Diff.OpsAdded)
	}
	if len(rep.Diff.OpsLost) != 1 || rep.Diff.OpsLost[0].Quote != "trades live in dispatch groups" {
		t.Errorf("OpsLost = %+v", rep.Diff.OpsLost)
	}
	if len(rep.Diff.ExclusionsChanged) != 0 {
		t.Errorf("ExclusionsChanged = %+v, want none (same exclusion)", rep.Diff.ExclusionsChanged)
	}
	if items1, runs1 := contractMRCounts(t, w.st); items1 != items0 || runs1 != runs0 {
		t.Errorf("a dry replay wrote: items %d→%d runs %d→%d", items0, items1, runs0, runs1)
	}
}

// "write == true enqueues and runs a row with trigger replay, SourceRun = id,
// the same window, and applies its ops".
func TestContractMRReplayWriteCreatesAReplayRun(t *testing.T) {
	w, run, _, fake := contractMRRecordedRun(t)
	const q = "ZEUS-PROMPT the cap is twenty"
	fake.set(contractMRAnswerJSON(t, []map[string]any{
		{"op": "add", "item": contractMRProposal("the cap is twenty", q, "zeus-sess", "z1"), "quote": q},
	}, nil))
	rep, err := ReplayMindRun(w.st, run.ID, true, time.Now())
	if err != nil {
		t.Fatalf("ReplayMindRun(write): %v", err)
	}
	if rep.NewRun == 0 || rep.NewRun == run.ID {
		t.Fatalf("NewRun = %d", rep.NewRun)
	}
	nr, err := w.st.GetMindRun(rep.NewRun)
	if err != nil {
		t.Fatal(err)
	}
	if nr.Trigger != "replay" || nr.SourceRun != run.ID || nr.Since != run.Since || nr.Until != run.Until || nr.JobID != run.JobID {
		t.Errorf("replay row = %+v, want trigger replay, source %d, window [%d,%d]", nr, run.ID, run.Since, run.Until)
	}
	if nr.State != "done" {
		t.Errorf("replay row state = %q, want done", nr.State)
	}
	items, _ := w.st.ListMMItems(false)
	found := false
	for _, it := range items {
		if it.Quote == q {
			found = true
			if it.RunID != rep.NewRun {
				t.Errorf("replayed row run = %d, want %d", it.RunID, rep.NewRun)
			}
		}
	}
	if !found {
		t.Errorf("the replay's add was not applied")
	}
}

// ---------------------------------------------------------------------------
// PruneMindRunBundles (jobs wrapper)
// ---------------------------------------------------------------------------

// "prunes blob rows older than now - MindBundleRetention".
func TestContractMRPruneWrapperUsesTheRetention(t *testing.T) {
	st := contractMREnv(t)
	now := time.Now()
	oldR := contractMRRun(t, st, store.MindRun{Trigger: "daily", Since: 1, Until: 2, CreatedAt: now.Add(-MindBundleRetention - time.Hour).UnixMilli()})
	newR := contractMRRun(t, st, store.MindRun{Trigger: "daily", Since: 1, Until: 2, CreatedAt: now.Add(-MindBundleRetention + time.Hour).UnixMilli()})
	for _, id := range []int64{oldR.ID, newR.ID} {
		if err := st.SetMindRunBlobs(id, []byte{1, 2, 3}, "{}"); err != nil {
			t.Fatal(err)
		}
	}
	n, err := PruneMindRunBundles(st, now)
	if err != nil {
		t.Fatalf("PruneMindRunBundles: %v", err)
	}
	if n != 1 {
		t.Errorf("pruned %d, want 1", n)
	}
	ob, _ := st.GetMindRunBlobs(oldR.ID)
	nb, _ := st.GetMindRunBlobs(newR.ID)
	if ob.BundleGz != nil || ob.PrunedAt == 0 || ob.Manifest != "{}" {
		t.Errorf("old blob = %+v, want pruned with manifest kept", ob)
	}
	if nb.BundleGz == nil || nb.PrunedAt != 0 {
		t.Errorf("new blob pruned: %+v", nb)
	}
	if MindBundleRetention != 90*24*time.Hour {
		t.Errorf("MindBundleRetention = %v, want 90 days", MindBundleRetention)
	}
}

// ---------------------------------------------------------------------------
// retired surfaces (logic half)
// ---------------------------------------------------------------------------

// "`BranchThreadWithRole(..., "mind")` returns ErrThreadRoleRetired" — pinned
// through the sentinel's existence and identity only (the branch path needs a
// live cell; the API test drives the 409).
func TestContractMRThreadRoleRetiredSentinel(t *testing.T) {
	if ErrThreadRoleRetired == nil || !errors.Is(ErrThreadRoleRetired, ErrThreadRoleRetired) {
		t.Fatal("ErrThreadRoleRetired is not a usable sentinel")
	}
	if ErrMarkNoJob == nil || ErrMindBundlePruned == nil {
		t.Fatal("ErrMarkNoJob / ErrMindBundlePruned missing")
	}
}

func contractMRItoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
