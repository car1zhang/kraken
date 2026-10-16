package jobs

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"kraken/internal/core"
	"kraken/internal/store"
	"kraken/internal/util"
)

// portability_contract_test.go — athena-logic's BLIND contract tests for the
// portability audit, internal/jobs half (artifacts/50-contract.md slices E, N
// and P). Written from the contract text alone; no non-test file in this
// package was opened by the author. Fixture conventions (useTestStore,
// mustCreateJob, withSkillsFS, withTrackedStub, fakeKrakenRepo, stubShipGit,
// stubLookPath, contractIssue, writeArtifact, writeUsageSnapshot) are the
// existing test files' own. Dimension: LOGIC — stated invariants and happy
// paths; boundaries and refusals are athena-edge's.

// ---------------------------------------------------------------------------
// shared fixtures
// ---------------------------------------------------------------------------

// contractPortCarlRe is the contract's "no carl/Carl/CARL (word-bounded,
// case-insensitive)".
var contractPortCarlRe = regexp.MustCompile(`(?i)\bcarl\b`)

// contractPortHeRe is the contract's "no he/him/his referring to the
// operator", read strictly: none of the three words anywhere in the text.
var contractPortHeRe = regexp.MustCompile(`(?i)\b(he|him|his)\b`)

// contractPortUnset makes key ABSENT for the test (restored on cleanup).
func contractPortUnset(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "")
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unset %s: %v", key, err)
	}
}

// contractPortEnv isolates every variable this file's subjects read.
func contractPortEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"KRAKEN_REPOS_DIR", "KRAKEN_BRANCH_PREFIX", "KRAKEN_OPERATOR", "KRAKEN_PLUTO_DIR", "LINEAR_DEFAULT_REPO", "LINEAR_API_KEY", "LINEAR_STATES"} {
		contractPortUnset(t, k)
	}
}

// contractPortRepos points KRAKEN_REPOS_DIR at a fresh dir holding one git
// checkout (a `.git` dir) per name, and returns the dir.
func contractPortRepos(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		if err := os.MkdirAll(filepath.Join(dir, n, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("KRAKEN_REPOS_DIR", dir)
	return dir
}

// contractPortNotify swaps notifyFn for a counter.
func contractPortNotify(t *testing.T) *atomic.Int32 {
	t.Helper()
	var n atomic.Int32
	prev := notifyFn
	notifyFn = func(title, msg string) { n.Add(1) }
	t.Cleanup(func() { notifyFn = prev })
	return &n
}

// contractPortEventCount counts events named id in the active store.
func contractPortEventCount(t *testing.T, st *store.Store, id string) int {
	t.Helper()
	events, err := st.ListEvents(500, 0)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range events {
		if e.Event == id {
			n++
		}
	}
	return n
}

// contractPortGit runs git in dir with a throwaway identity.
func contractPortGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{"-C", dir, "-c", "user.name=contract", "-c", "user.email=contract@example.com", "-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main"}, args...)
	out, err := exec.Command("git", full...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func contractPortWrite(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ===========================================================================
// SLICE E — repoRoot, DefaultRepo, JobTemplate, Linear, dream, bind, branches
// ===========================================================================

// Contract: `jobs.repoRoot` "KRAKEN_REPOS_DIR set to an absolute path → that
// path, cleaned · unset, empty, or relative → filepath.Join(util.Home(),
// "Developer")".
func TestContractPortRepoRoot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	contractPortEnv(t)
	def := filepath.Join(util.Home(), "Developer")

	if got := repoRoot(); got != def {
		t.Errorf("unset: repoRoot() = %q, want %q", got, def)
	}
	t.Setenv("KRAKEN_REPOS_DIR", "")
	if got := repoRoot(); got != def {
		t.Errorf("empty: repoRoot() = %q, want %q", got, def)
	}
	t.Setenv("KRAKEN_REPOS_DIR", "relative/repos")
	if got := repoRoot(); got != def {
		t.Errorf("relative: repoRoot() = %q, want the default %q", got, def)
	}
	abs := t.TempDir()
	t.Setenv("KRAKEN_REPOS_DIR", abs+"/sub/../repos/")
	if got, want := repoRoot(), filepath.Clean(abs+"/repos"); got != want {
		t.Errorf("absolute: repoRoot() = %q, want the cleaned %q", got, want)
	}
}

// Contract: "every reader (ResolveRepo, RepoCandidates, PickRepo, …) goes
// through it" — with the knob set, the readers list and resolve under it and
// never under ~/Developer.
func TestContractPortReadersUseRepoRoot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	contractPortEnv(t)
	// a decoy under the default root: a reader that bypasses repoRoot finds it
	if err := os.MkdirAll(filepath.Join(util.Home(), "Developer", "decoy", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	dir := contractPortRepos(t, "alpha", "beta")
	if err := os.MkdirAll(filepath.Join(dir, "not-a-repo"), 0o755); err != nil {
		t.Fatal(err)
	}

	cands := append([]string(nil), RepoCandidates()...)
	sort.Strings(cands)
	if want := []string{"alpha", "beta"}; !reflect.DeepEqual(cands, want) {
		t.Errorf("RepoCandidates() = %v, want %v (the knob's dir, not ~/Developer)", cands, want)
	}
	got, err := ResolveRepo("alpha")
	if err != nil || got != filepath.Join(dir, "alpha") {
		t.Errorf("ResolveRepo(alpha) = (%q, %v), want %q", got, err, filepath.Join(dir, "alpha"))
	}
	if got, err := ResolveRepo("decoy"); err == nil {
		t.Errorf("ResolveRepo(decoy) = %q resolved under ~/Developer while KRAKEN_REPOS_DIR is set", got)
	}
}

// Contract: `jobs.DefaultRepo` "RepoCandidates() has exactly one entry → that
// name · zero or two or more → "" · never "monolith"".
func TestContractPortDefaultRepo(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	contractPortEnv(t)

	contractPortRepos(t)
	if got := DefaultRepo(); got != "" {
		t.Errorf("zero candidates: DefaultRepo() = %q, want \"\"", got)
	}
	contractPortRepos(t, "solo")
	if got := DefaultRepo(); got != "solo" {
		t.Errorf("one candidate: DefaultRepo() = %q, want solo", got)
	}
	contractPortRepos(t, "aaa", "monolith", "zzz")
	if got := DefaultRepo(); got != "" {
		t.Errorf("three candidates: DefaultRepo() = %q, want \"\" (never monolith, never the first)", got)
	}
	contractPortRepos(t, "monolith", "other")
	if got := DefaultRepo(); got != "" {
		t.Errorf("two candidates incl. monolith: DefaultRepo() = %q, want \"\"", got)
	}
}

// contractPortRepoLineValue returns the value of the template's `repo:` line
// with any trailing `# comment` removed, and how many such lines exist.
func contractPortRepoLineValue(src string) (string, int) {
	val, n := "", 0
	for _, line := range strings.Split(src, "\n") {
		if !strings.HasPrefix(line, "repo:") {
			continue
		}
		n++
		rest := strings.TrimPrefix(line, "repo:")
		if i := strings.Index(rest, "#"); i >= 0 {
			rest = rest[:i]
		}
		val = strings.TrimSpace(rest)
	}
	return val, n
}

// Contract: `jobs.JobTemplate` "the repo: line's value is DefaultRepo(); with
// "" the line is repo: followed only by the existing comment · ParseJob(…)
// yields Repo == DefaultRepo() and no parse error either way · the word
// monolith does not appear".
func TestContractPortJobTemplateRepoLine(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	contractPortEnv(t)
	for _, tc := range []struct {
		name  string
		repos []string
		want  string
	}{
		{"one checkout", []string{"solo"}, "solo"},
		{"no checkout", nil, ""},
		{"two checkouts", []string{"one", "two"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			contractPortRepos(t, tc.repos...)
			if got := DefaultRepo(); got != tc.want {
				t.Fatalf("fixture: DefaultRepo() = %q, want %q", got, tc.want)
			}
			src := JobTemplate("port-tmpl")
			val, n := contractPortRepoLineValue(src)
			if n != 1 {
				t.Fatalf("template has %d `repo:` lines, want 1:\n%s", n, src)
			}
			if val != tc.want {
				t.Errorf("template repo value = %q, want %q:\n%s", val, tc.want, src)
			}
			if strings.Contains(strings.ToLower(src), "monolith") {
				t.Errorf("template names monolith:\n%s", src)
			}
			j := ParseJob("port-tmpl"+JobSuffix, []byte(src))
			if j.Err != "" {
				t.Fatalf("ParseJob(template) error: %s", j.Err)
			}
			if j.Repo != tc.want {
				t.Errorf("ParseJob(template).Repo = %q, want %q", j.Repo, tc.want)
			}
		})
	}
}

// Contract: `core.LoadLinearConfig` "DefaultRepo is "" when
// LINEAR_DEFAULT_REPO is absent from both the environment and the file …;
// set → that value".
func TestContractPortLinearConfigDefaultRepo(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	contractPortEnv(t)
	if got := core.LoadLinearConfig().DefaultRepo; got != "" {
		t.Errorf("absent: DefaultRepo = %q, want \"\" (no monolith constant)", got)
	}
	t.Setenv("LINEAR_DEFAULT_REPO", "picked")
	if got := core.LoadLinearConfig().DefaultRepo; got != "picked" {
		t.Errorf("set: DefaultRepo = %q, want picked", got)
	}
}

// Contract: `jobs.composeLinearBody` "the repo: line is cfg.DefaultRepo when
// non-empty, else DefaultRepo()".
func TestContractPortComposeLinearBodyRepo(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	contractPortEnv(t)
	contractPortRepos(t, "derived")
	iss := contractIssue("uuid-port", "ENG-9", "Portable", "Today", time.UnixMilli(1_754_000_000_000))

	withCfg := ParseJob("x"+JobSuffix, []byte(composeLinearBody(iss, core.LinearConfig{APIKey: "k", DefaultRepo: "configured", States: []string{"Today"}}, nil)))
	if withCfg.Err != "" || withCfg.Repo != "configured" {
		t.Errorf("cfg.DefaultRepo set: parsed (repo %q, err %q), want repo configured", withCfg.Repo, withCfg.Err)
	}
	fallback := ParseJob("x"+JobSuffix, []byte(composeLinearBody(iss, core.LinearConfig{APIKey: "k", States: []string{"Today"}}, nil)))
	if fallback.Err != "" || fallback.Repo != "derived" {
		t.Errorf("cfg.DefaultRepo empty: parsed (repo %q, err %q), want DefaultRepo() = derived", fallback.Repo, fallback.Err)
	}
}

// Contract: `jobs.kickDream` "when ResolveRepo("kraken") returns an error:
// returns nil, creates no job row, calls no notification, records today in
// lastDreamDate so a second call the same day returns nil without resolving
// again, and logs one event (dream-skipped)".
//
// "without resolving again" is made observable by making `kraken` resolvable
// between the two calls: a second call that re-resolved would now dream.
func TestContractPortKickDreamSkipsWithoutKrakenCheckout(t *testing.T) {
	st := useTestStore(t)
	contractPortEnv(t)
	dir := contractPortRepos(t) // empty repos dir: ResolveRepo("kraken") fails
	if _, err := ResolveRepo("kraken"); err == nil {
		t.Fatal("fixture: kraken resolved in an empty repos dir")
	}
	lastDreamDate.Store("")
	t.Cleanup(func() { lastDreamDate.Store("") })
	var passes atomic.Int32
	prevPass := dreamPass
	dreamPass = func(now time.Time, prev DreamRun) (*Job, DreamRun, bool) {
		passes.Add(1)
		return nil, DreamRun{}, false
	}
	t.Cleanup(func() { dreamPass = prevPass })
	notes := contractPortNotify(t)

	n := time.Now()
	now := time.Date(n.Year(), n.Month(), n.Day(), 12, 0, 0, 0, time.Local)
	if j := kickDream(now); j != nil {
		t.Fatalf("kickDream returned %+v, want nil", j)
	}
	if got, _ := lastDreamDate.Load().(string); got != now.Format(dreamDateLayout) {
		t.Errorf("lastDreamDate = %q, want today %q", got, now.Format(dreamDateLayout))
	}

	if err := os.MkdirAll(filepath.Join(dir, "kraken", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if j := kickDream(now.Add(time.Hour)); j != nil {
		t.Fatalf("second kickDream the same day returned %+v, want nil", j)
	}
	time.Sleep(200 * time.Millisecond) // a pass, if wrongly started, runs async

	if c := passes.Load(); c != 0 {
		t.Errorf("dream pass ran %d times, want 0", c)
	}
	if c := notes.Load(); c != 0 {
		t.Errorf("%d notifications, want 0", c)
	}
	if js := LoadJobs(); len(js) != 0 {
		t.Errorf("job rows created: %d, want 0", len(js))
	}
	if c := contractPortEventCount(t, st, "dream-skipped"); c != 1 {
		t.Errorf("dream-skipped events = %d, want exactly 1", c)
	}
}

// contractPortDrySnapshot persists a claude usage snapshot whose single row
// names a slot that does not exist on disk, so no slot is eligible.
func contractPortDrySnapshot(t *testing.T) {
	t.Helper()
	now := time.Now().UnixMilli()
	snap := core.UsageSnapshot{FetchedAt: now, Accounts: []core.AccountUsage{{
		Slug: "dry-slot", Email: "dry@example.com",
		Session: &core.UsageWindow{Percent: 10}, FetchedAt: now,
	}}}
	if err := writeUsageSnapshot(snap, UsageSnapshotPath()); err != nil {
		t.Fatalf("write usage snapshot: %v", err)
	}
}

// Contract: `jobs.bindAccount` "in the no-target branch, notifyDeduped is NOT
// called when the pool root (core.PoolDir()) does not exist as a directory;
// the bind-skipped-style event is still logged and the job stays unbound ·
// when the root exists but no slot is eligible: the existing notification,
// unchanged (observable through the package's notifyFn variable)".
func TestContractPortBindClaudeNoticeFollowsPoolRoot(t *testing.T) {
	for _, tc := range []struct {
		name      string
		makeRoot  bool
		wantNotes int32
	}{
		{"pool root absent", false, 0},
		{"pool root present, nothing eligible", true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := useTestStore(t)
			contractPortEnv(t)
			pool := filepath.Join(util.Home(), "contract-claude-pool")
			t.Setenv("CLAUDE_POOL_DIR", pool)
			if tc.makeRoot {
				if err := os.MkdirAll(pool, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			contractPortDrySnapshot(t)
			notes := contractPortNotify(t)
			row := mustCreateJob(t, st, store.Job{Slug: "port-bind-claude", Repo: "kraken"})
			j := &Job{Slug: row.Slug, ID: row.ID, Repo: "kraken"}

			bindAccount(j, "test")

			if j.AccountSlug != "" {
				t.Errorf("job bound to %q, want unbound", j.AccountSlug)
			}
			if got := notes.Load(); got != tc.wantNotes {
				t.Errorf("notifications = %d, want %d", got, tc.wantNotes)
			}
			if c := contractPortEventCount(t, st, "bind-skipped"); c < 1 {
				t.Errorf("no bind-skipped event logged")
			}
		})
	}
}

// Contract: `jobs.bindCodexAccount` — the same rule against
// core.CodexPoolDir(). The codex snapshot is written by the real
// CodexUsagePass over an empty, existing pool; the absent case then removes
// the root.
func TestContractPortBindCodexNoticeFollowsPoolRoot(t *testing.T) {
	for _, tc := range []struct {
		name      string
		keepRoot  bool
		wantNotes int32
	}{
		{"codex pool root absent", false, 0},
		{"codex pool root present, nothing eligible", true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := useTestStore(t)
			contractPortEnv(t)
			pool := filepath.Join(util.Home(), "contract-codex-pool")
			t.Setenv("CODEX_POOL_DIR", pool)
			if err := os.MkdirAll(filepath.Join(pool, "accounts"), 0o755); err != nil {
				t.Fatal(err)
			}
			_ = CodexUsagePass()
			if !tc.keepRoot {
				if err := os.RemoveAll(pool); err != nil {
					t.Fatal(err)
				}
			}
			notes := contractPortNotify(t)
			row := mustCreateJob(t, st, store.Job{Slug: "port-bind-codex", Repo: "kraken", Agent: "codex"})
			j := &Job{Slug: row.Slug, ID: row.ID, Repo: "kraken", Agent: "codex"}

			bindAccount(j, "test")

			if j.AccountSlug != "" {
				t.Errorf("codex job bound to %q, want unbound", j.AccountSlug)
			}
			if got := notes.Load(); got != tc.wantNotes {
				t.Errorf("notifications = %d, want %d", got, tc.wantNotes)
			}
			if c := contractPortEventCount(t, st, "bind-skipped"); c < 1 {
				t.Errorf("no bind-skipped event logged")
			}
		})
	}
}

// Contract: `jobs.branchPrefix` "KRAKEN_BRANCH_PREFIX trimmed; empty →
// kraken/; a value without a trailing / gets one appended; carl/ → carl/".
func TestContractPortBranchPrefix(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	contractPortEnv(t)
	if got := branchPrefix(); got != "kraken/" {
		t.Errorf("unset: branchPrefix() = %q, want kraken/", got)
	}
	for in, want := range map[string]string{
		"":          "kraken/",
		"   ":       "kraken/",
		"carl/":     "carl/",
		"  feat  ":  "feat/",
		"team/me/":  "team/me/",
		" team/me ": "team/me/",
	} {
		t.Setenv("KRAKEN_BRANCH_PREFIX", in)
		if got := branchPrefix(); got != want {
			t.Errorf("KRAKEN_BRANCH_PREFIX=%q: branchPrefix() = %q, want %q", in, got, want)
		}
	}
}

// Contract: `branchForRun(j, 1) == branchPrefix()+cellName(j)`;
// `branchForRun(j, n≥2) == branchPrefix()+cellName(j)+"-r"+n`, and CellBranch
// agrees through j.Run.
func TestContractPortBranchForRunUsesPrefix(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	contractPortEnv(t)
	j := &Job{Slug: "demo-job", ID: 42}
	for _, prefix := range []string{"", "feat/", "carl/"} {
		if prefix == "" {
			contractPortUnset(t, "KRAKEN_BRANCH_PREFIX")
		} else {
			t.Setenv("KRAKEN_BRANCH_PREFIX", prefix)
		}
		p := branchPrefix()
		if got, want := branchForRun(j, 1), p+cellName(j); got != want {
			t.Errorf("prefix %q run 1: %q, want %q", prefix, got, want)
		}
		for _, n := range []int{2, 5} {
			want := p + cellName(j) + fmt.Sprintf("-r%d", n)
			if got := branchForRun(j, n); got != want {
				t.Errorf("prefix %q run %d: %q, want %q", prefix, n, got, want)
			}
			if got := CellBranch(&Job{Slug: j.Slug, ID: j.ID, Run: n}); got != want {
				t.Errorf("prefix %q CellBranch run %d: %q, want %q", prefix, n, got, want)
			}
		}
	}
}

// Contract: "with KRAKEN_BRANCH_PREFIX=carl/ the names are byte-identical to
// today's" (today: carl/<slug>-<id>, carl/<slug>-<id>-rN), and the default is
// kraken/.
func TestContractPortBranchNamesByteIdenticalUnderCarlPrefix(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	contractPortEnv(t)
	t.Setenv("KRAKEN_BRANCH_PREFIX", "carl/")
	j := &Job{Slug: "My Job", ID: 3}
	for run, want := range map[int]string{1: "carl/my-job-3", 2: "carl/my-job-3-r2", 7: "carl/my-job-3-r7"} {
		if got := branchForRun(j, run); got != want {
			t.Errorf("run %d: %q, want %q", run, got, want)
		}
	}
	contractPortUnset(t, "KRAKEN_BRANCH_PREFIX")
	if got := CellBranch(&Job{Slug: "My Job", ID: 3, Run: 1}); got != "kraken/my-job-3" {
		t.Errorf("default CellBranch = %q, want kraken/my-job-3", got)
	}
}

// Contract: `jobs.branchFamily` "contains every name under the current prefix
// (bare and -rN as today), the same names under carl/, and the pre-cutover
// bare-slug forms, with no duplicates; a PR recorded on carl/<slug>-<id> is
// therefore still matched after the default moved".
func TestContractPortBranchFamilyCoversOldPrefix(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	contractPortEnv(t)
	j := &Job{Slug: "slug", ID: 7, Run: 3}

	fam := branchFamily(j) // default prefix kraken/
	for _, want := range []string{
		"kraken/slug-7", "kraken/slug-7-r2", "kraken/slug-7-r3",
		"carl/slug-7", "carl/slug-7-r2", "carl/slug-7-r3",
		"carl/slug", "carl/slug-r2", "carl/slug-r3",
	} {
		if !fam[want] {
			t.Errorf("default prefix: branchFamily is missing %q (got %v)", want, fam)
		}
	}
	for _, reject := range []string{"kraken/slug-7-r4", "carl/slug-7-r4", "kraken/slugx", "carl/slug2-7"} {
		if fam[reject] {
			t.Errorf("default prefix: branchFamily wrongly matches %q", reject)
		}
	}

	// under carl/ the current-prefix names ARE the carl/ names: the family is
	// exactly today's six, with no duplicates.
	t.Setenv("KRAKEN_BRANCH_PREFIX", "carl/")
	famCarl := branchFamily(j)
	want := map[string]bool{
		"carl/slug-7": true, "carl/slug-7-r2": true, "carl/slug-7-r3": true,
		"carl/slug": true, "carl/slug-r2": true, "carl/slug-r3": true,
	}
	if len(famCarl) != len(want) {
		t.Errorf("carl/ prefix: branchFamily = %v, want exactly %v", famCarl, want)
	}
	for b := range want {
		if !famCarl[b] {
			t.Errorf("carl/ prefix: branchFamily is missing %q", b)
		}
	}
}

// Contract: `jobs.PreflightShipIOS` "the credentials-missing error text names
// ~/.kraken/asc.env and does not contain launchctl setenv".
func TestContractPortPreflightShipIOSNamesAscEnv(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	contractPortEnv(t)
	t.Setenv("ASC_KEY_ID", "")
	t.Setenv("ASC_ISSUER_ID", "")
	repo := fakeKrakenRepo(t)
	fakeShipScript(t, repo)
	stubShipGit(t, "main")
	stubLookPath(t, func(string) (string, error) { return "/opt/homebrew/bin/xcodegen", nil })

	_, err := PreflightShipIOS()
	if err == nil {
		t.Fatal("preflight with no credentials succeeded")
	}
	if !strings.Contains(err.Error(), "~/.kraken/asc.env") {
		t.Errorf("refusal %q does not name ~/.kraken/asc.env", err)
	}
	if strings.Contains(err.Error(), "launchctl setenv") {
		t.Errorf("refusal %q still recommends launchctl setenv", err)
	}
}

// ===========================================================================
// SLICE N — the operator name
// ===========================================================================

// Contract: `jobs.Operator` "KRAKEN_OPERATOR trimmed, when non-empty; longer
// than 64 runes → the first 64 · else USER trimmed when non-empty · else "the
// operator" · never "carl" by default".
func TestContractPortOperatorFallbacks(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	contractPortEnv(t)

	t.Setenv("KRAKEN_OPERATOR", "  Ada  ")
	t.Setenv("USER", "someone")
	if got := Operator(); got != "Ada" {
		t.Errorf("KRAKEN_OPERATOR set: Operator() = %q, want Ada", got)
	}

	t.Setenv("KRAKEN_OPERATOR", strings.Repeat("é", 70))
	if got, want := Operator(), strings.Repeat("é", 64); got != want {
		t.Errorf("70-rune name: Operator() = %q (%d runes), want the first 64 runes", got, len([]rune(got)))
	}

	t.Setenv("KRAKEN_OPERATOR", "   ")
	t.Setenv("USER", "  bob ")
	if got := Operator(); got != "bob" {
		t.Errorf("blank KRAKEN_OPERATOR: Operator() = %q, want USER trimmed (bob)", got)
	}

	contractPortUnset(t, "KRAKEN_OPERATOR")
	if got := Operator(); got != "bob" {
		t.Errorf("unset KRAKEN_OPERATOR: Operator() = %q, want bob", got)
	}

	contractPortUnset(t, "USER")
	if got := Operator(); got != "the operator" {
		t.Errorf("nothing set: Operator() = %q, want \"the operator\"", got)
	}
	t.Setenv("USER", "  ")
	if got := Operator(); got != "the operator" {
		t.Errorf("blank USER: Operator() = %q, want \"the operator\"", got)
	}
}

// contractPortAda sets the operator to Ada and USER to a decoy carl, so a
// brief that read USER (or still hardcoded the name) shows it.
func contractPortAda(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	contractPortEnv(t)
	t.Setenv("KRAKEN_OPERATOR", "Ada")
	t.Setenv("USER", "carl")
}

func contractPortAssertNamed(t *testing.T, label, text string) {
	t.Helper()
	if !strings.Contains(text, "Ada") {
		t.Errorf("%s does not name the operator Ada", label)
	}
	if m := contractPortCarlRe.FindString(text); m != "" {
		i := contractPortCarlRe.FindStringIndex(text)[0]
		lo, hi := max(0, i-60), min(len(text), i+60)
		t.Errorf("%s still says %q: …%s…", label, m, text[lo:hi])
	}
}

func contractPortAssertTheyThem(t *testing.T, label, text string) {
	t.Helper()
	if loc := contractPortHeRe.FindStringIndex(text); loc != nil {
		lo, hi := max(0, loc[0]-60), min(len(text), loc[1]+60)
		t.Errorf("%s uses a he/him/his pronoun: …%s…", label, text[lo:hi])
	}
}

// Contract: zeusBrief/threadBrief/irisBrief "with KRAKEN_OPERATOR=Ada, the
// output contains Ada wherever it named the person and contains no
// carl/Carl/CARL … · third-person pronouns for the operator are they/them".
// (mindThreadBrief is reached through threadBrief's "mind" role; its own
// signature is not in the contract.)
func TestContractPortBriefsNameTheOperator(t *testing.T) {
	contractPortAda(t)
	contractPortRepos(t, "solo")
	j := &Job{Slug: "port-brief", ID: 11, Repo: "solo"}
	for label, text := range map[string]string{
		"zeusBrief":          zeusBrief(j),
		"threadBrief(ask)":   threadBrief(j, "t1", "a question", "/tmp/wt", "ask"),
		"threadBrief(mind)":  threadBrief(j, "t2", "a capture", "/tmp/wt", "mind"),
		"irisBrief":          irisBrief(),
		"goalContextHeader":  goalContextHeader(),
		"notesContextHeader": notesContextHeader(),
		"mmIndexHeader":      mmIndexHeader(),
		"prWatchPrefix":      prWatchPrefix(),
	} {
		contractPortAssertNamed(t, label, text)
		contractPortAssertTheyThem(t, label, text)
	}
}

// Contract: irisBrief "says `repo:` defaults to `<DefaultRepo()>` when that is
// non-empty and "has no default on this machine" when empty, and its example
// block uses that value".
func TestContractPortIrisBriefDefaultRepo(t *testing.T) {
	contractPortAda(t)

	contractPortRepos(t, "solo")
	b := irisBrief()
	if !strings.Contains(b, "`repo:` defaults to `solo`") {
		t.Errorf("one checkout: irisBrief lacks \"`repo:` defaults to `solo`\"")
	}
	if !strings.Contains(b, "repo: solo") {
		t.Errorf("one checkout: irisBrief's example block does not use `repo: solo`")
	}
	if strings.Contains(strings.ToLower(b), "monolith") {
		t.Errorf("irisBrief still names monolith")
	}

	contractPortRepos(t)
	b = irisBrief()
	if !strings.Contains(b, "has no default on this machine") {
		t.Errorf("no checkout: irisBrief lacks \"has no default on this machine\"")
	}
	if strings.Contains(b, "`repo:` defaults to `") {
		t.Errorf("no checkout: irisBrief still claims a default repo")
	}
}

// Contract: goalContextHeader()/notesContextHeader() "name Operator()
// possessively (Ada's goals right now, Ada's unread notes on this job)".
func TestContractPortContextHeadersPossessive(t *testing.T) {
	contractPortAda(t)
	if h := goalContextHeader(); !strings.Contains(h, "Ada's goals right now") {
		t.Errorf("goalContextHeader() = %q, want it to contain \"Ada's goals right now\"", h)
	}
	if h := notesContextHeader(); !strings.Contains(h, "Ada's unread notes on this job") {
		t.Errorf("notesContextHeader() = %q, want it to contain \"Ada's unread notes on this job\"", h)
	}
}

// Contract: prWatchPrefix() == "[kraken pr-watch — automated notice, not a
// reply from " + Operator() + "]".
func TestContractPortPRWatchPrefix(t *testing.T) {
	contractPortAda(t)
	if got, want := prWatchPrefix(), "[kraken pr-watch — automated notice, not a reply from Ada]"; got != want {
		t.Errorf("prWatchPrefix() = %q, want %q", got, want)
	}
	t.Setenv("KRAKEN_OPERATOR", "Grace")
	if got, want := prWatchPrefix(), "[kraken pr-watch — automated notice, not a reply from Grace]"; got != want {
		t.Errorf("prWatchPrefix() = %q, want %q (read per call)", got, want)
	}
}

// Contract: mmIndexHeader() "starts <Operator()>'s mental model — one line per
// item they have committed, each title a claim they hold."
func TestContractPortMMIndexHeader(t *testing.T) {
	contractPortAda(t)
	want := "Ada's mental model — one line per item they have committed, each title a claim they hold."
	if got := mmIndexHeader(); !strings.HasPrefix(got, want) {
		t.Errorf("mmIndexHeader() = %q, want prefix %q", got, want)
	}
}

// Contract: `jobs.installSkills` "every file materialized under
// <wt>/.claude/skills/ has each literal {{operator}} replaced by Operator();
// the embedded SkillsFS content is unchanged; a name containing {{ or a quote
// is inserted literally, never re-expanded".
func TestContractPortInstallSkillsSubstitutesOperator(t *testing.T) {
	for _, tc := range []struct {
		name, operator string
	}{
		{"plain name", "Ada"},
		{"name with braces and quotes", `O"Neil {{operator}} 'x'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			contractPortEnv(t)
			t.Setenv("KRAKEN_OPERATOR", tc.operator)
			src := map[string]string{
				"aim/SKILL.md":         "---\nname: aim\n---\nask {{operator}} first; it is {{operator}}'s call.\n",
				"aim/assets/card.html": "<p>{{operator}}</p>",
				"do/SKILL.md":          "---\nname: do\n---\nno placeholder here\n",
			}
			fsys := fstest.MapFS{}
			for p, body := range src {
				fsys[p] = &fstest.MapFile{Data: []byte(body)}
			}
			withSkillsFS(t, fsys)
			withTrackedStub(t, alwaysUntracked)
			wt := t.TempDir()

			installSkills(wt, &Job{Slug: "port-skills", ID: 3})

			for p, body := range src {
				got, err := os.ReadFile(filepath.Join(wt, ".claude", "skills", p))
				if err != nil {
					t.Fatalf("materialized %s: %v", p, err)
				}
				if want := strings.ReplaceAll(body, "{{operator}}", tc.operator); string(got) != want {
					t.Errorf("%s = %q, want %q", p, got, want)
				}
				if string(fsys[p].Data) != body {
					t.Errorf("the source FS's %s was modified: %q", p, fsys[p].Data)
				}
			}
		})
	}
}

// ===========================================================================
// SLICE P — retire pluto
// ===========================================================================

// Contract (amended, review round 1): `jobs.rootNotes` `func rootNotes(wt
// string) (names []string, ok bool)` — "ok == false when wt == "", when it is
// not a checkout, or when git status fails; ok == true with the names
// otherwise" · (original row) the bare names of every UNCOMMITTED (untracked
// or modified) root-level *.md, a rename by its new name, sorted; committed
// and clean root files are not returned.
func TestContractPortRootNotes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	contractPortEnv(t)
	if got, ok := rootNotes(""); ok || got != nil {
		t.Errorf("rootNotes(\"\") = (%#v, %v), want (nil, false)", got, ok)
	}
	if got, ok := rootNotes(t.TempDir()); ok || got != nil {
		t.Errorf("rootNotes(non-git dir) = (%#v, %v), want (nil, false)", got, ok)
	}

	wt := t.TempDir()
	contractPortGit(t, wt, "init", "-q")
	contractPortWrite(t, filepath.Join(wt, "README.md"), "committed and clean\n")
	contractPortWrite(t, filepath.Join(wt, "CHANGES.md"), "committed then modified\n")
	contractPortWrite(t, filepath.Join(wt, "old.md"), "to be renamed\n")
	contractPortWrite(t, filepath.Join(wt, "docs", "tracked.md"), "nested\n")
	contractPortGit(t, wt, "add", "-A")
	contractPortGit(t, wt, "commit", "-q", "-m", "seed")

	if got, ok := rootNotes(wt); !ok || len(got) != 0 {
		t.Errorf("clean checkout: rootNotes = (%#v, %v), want (empty, true)", got, ok)
	}

	contractPortWrite(t, filepath.Join(wt, "CHANGES.md"), "modified\n")
	contractPortWrite(t, filepath.Join(wt, "NOTES.md"), "untracked note\n")
	contractPortWrite(t, filepath.Join(wt, "notes.txt"), "not markdown\n")
	contractPortWrite(t, filepath.Join(wt, "sub", "deep.md"), "not root level\n")
	contractPortWrite(t, filepath.Join(wt, "docs", "tracked.md"), "nested modified\n")
	contractPortGit(t, wt, "mv", "old.md", "new.md")

	got, ok := rootNotes(wt)
	want := []string{"CHANGES.md", "NOTES.md", "new.md"}
	if !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("rootNotes = (%#v, %v), want (%#v, true)", got, ok, want)
	}
}

// Contract (amended): "a root file kraken itself wrote (the codex AGENTS.md
// whose first bytes are kraken's generated header) is never returned" — while
// an AGENTS.md the operator or zeus wrote by hand still is.
func TestContractPortRootNotesSkipsKrakensAgentsMD(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	contractPortEnv(t)
	wt := t.TempDir()
	contractPortGit(t, wt, "init", "-q")
	contractPortWrite(t, filepath.Join(wt, "README.md"), "seed\n")
	contractPortGit(t, wt, "add", "-A")
	contractPortGit(t, wt, "commit", "-q", "-m", "seed")

	contractPortWrite(t, filepath.Join(wt, "AGENTS.md"), codexAgentsMD("you are zeus; a brief"))
	contractPortWrite(t, filepath.Join(wt, "NOTES.md"), "a real note\n")
	got, ok := rootNotes(wt)
	if want := []string{"NOTES.md"}; !ok || !reflect.DeepEqual(got, want) {
		t.Errorf("with kraken's codex AGENTS.md: rootNotes = (%#v, %v), want (%#v, true)", got, ok, want)
	}

	contractPortWrite(t, filepath.Join(wt, "AGENTS.md"), "# Agents\nhand-written project instructions\n")
	got, ok = rootNotes(wt)
	if want := []string{"AGENTS.md", "NOTES.md"}; !ok || !reflect.DeepEqual(got, want) {
		t.Errorf("with a hand-written AGENTS.md: rootNotes = (%#v, %v), want (%#v, true)", got, ok, want)
	}
}

// contractPortCellWorld wires a store, a repos dir with the job's repo, a job
// row, and a git-initialised cell worktree at CellWorktree(j, repo) whose
// committed root file is COMMITTED.md. It returns the store, the job and the
// worktree.
func contractPortCellWorld(t *testing.T, slug string) (*store.Store, *Job, string) {
	t.Helper()
	st := useTestStore(t)
	contractPortEnv(t)
	dir := contractPortRepos(t, "port-cell-repo")
	mustCreateJob(t, st, store.Job{Slug: slug, Repo: "port-cell-repo"})
	j := LoadJobBySlug(slug)
	if j == nil {
		t.Fatal("fixture: job row not loadable")
	}
	wt := CellWorktree(j, filepath.Join(dir, "port-cell-repo"))
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	contractPortGit(t, wt, "init", "-q")
	contractPortWrite(t, filepath.Join(wt, "COMMITTED.md"), "already in git\n")
	contractPortGit(t, wt, "add", "-A")
	contractPortGit(t, wt, "commit", "-q", "-m", "seed")
	return st, j, wt
}

func contractPortArtifactRows(t *testing.T, st *store.Store, id int64) map[string]string {
	t.Helper()
	rows, err := st.ListArtifacts(id)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, r := range rows {
		if _, dup := out[r.Path]; dup {
			t.Errorf("duplicate artifact row for %q", r.Path)
		}
		out[r.Path] = string(r.Bytes)
	}
	return out
}

// Contract: `jobs.syncCellArtifacts` (amended) "root notes are stored under
// the path root/<bare name> (so NOTES.md is the row root/NOTES.md), never
// under the bare name"; C13: "a committed root *.md is not captured". A root
// note sharing a bare name with an artifacts/ file is a DIFFERENT path now, so
// both rows exist.
func TestContractPortSyncCapturesRootNotes(t *testing.T) {
	_, j, wt := contractPortCellWorld(t, "port-sync")
	writeArtifact(t, j, "a.md", []byte("artifact a"), time.Hour)
	contractPortWrite(t, filepath.Join(wt, "NOTES.md"), "root notes")
	contractPortWrite(t, filepath.Join(wt, "a.md"), "root a")

	if err := syncCellArtifacts(ActiveStore(), j.ID, j); err != nil {
		t.Fatalf("syncCellArtifacts: %v", err)
	}
	got := contractPortArtifactRows(t, ActiveStore(), j.ID)
	want := map[string]string{"a.md": "artifact a", "root/NOTES.md": "root notes", "root/a.md": "root a"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("artifact rows = %v, want %v", got, want)
	}
}

// Contract (amended): "an artifacts/root/<name> file beats a root note of the
// same name (one row, the artifacts bytes)".
func TestContractPortSyncRootNoteCollisionKeepsArtifactBytes(t *testing.T) {
	_, j, wt := contractPortCellWorld(t, "port-sync-collide")
	writeArtifact(t, j, "root/a.md", []byte("from artifacts"), time.Hour)
	contractPortWrite(t, filepath.Join(wt, "a.md"), "from the root")

	if err := syncCellArtifacts(ActiveStore(), j.ID, j); err != nil {
		t.Fatalf("syncCellArtifacts: %v", err)
	}
	got := contractPortArtifactRows(t, ActiveStore(), j.ID)
	if want := map[string]string{"root/a.md": "from artifacts"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("artifact rows = %v, want %v", got, want)
	}
}

// Contract (amended): `jobs.syncArtifactsAtTeardown` `func(j *Job) error`
// "returns the store sync's error" — on the happy path nil, with the
// artifacts and the root notes in the table.
func TestContractPortTeardownSyncHappyPath(t *testing.T) {
	_, j, wt := contractPortCellWorld(t, "port-teardown-sync")
	writeArtifact(t, j, "00-overview.md", []byte("overview"), time.Hour)
	contractPortWrite(t, filepath.Join(wt, "NOTES.md"), "notes")

	if err := syncArtifactsAtTeardown(j); err != nil {
		t.Fatalf("syncArtifactsAtTeardown = %v, want nil", err)
	}
	got := contractPortArtifactRows(t, ActiveStore(), j.ID)
	want := map[string]string{"00-overview.md": "overview", "root/NOTES.md": "notes"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("artifact rows = %v, want %v", got, want)
	}
}

// Contract (amended): `jobs.restoreCellArtifacts` "rows whose path starts
// with root/ are restored to <worktree>/<bare name>, not into artifacts/;
// every other row restores as today".
func TestContractPortRestorePutsRootNotesAtWorktreeRoot(t *testing.T) {
	_, j, wt := contractPortCellWorld(t, "port-restore")
	if err := ActiveStore().SyncArtifacts(j.ID, []store.ArtifactFile{
		{Path: "00-overview.md", Bytes: []byte("overview"), Mtime: 1},
		{Path: "root/NOTES.md", Bytes: []byte("root notes"), Mtime: 1},
	}, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(CellArtifacts(j)); err != nil {
		t.Fatal(err)
	}

	if err := restoreCellArtifacts(ActiveStore(), j.ID, j); err != nil {
		t.Fatalf("restoreCellArtifacts: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(CellArtifacts(j), "00-overview.md")); err != nil || string(b) != "overview" {
		t.Errorf("artifacts/00-overview.md = (%q, %v), want overview", b, err)
	}
	if b, err := os.ReadFile(filepath.Join(wt, "NOTES.md")); err != nil || string(b) != "root notes" {
		t.Errorf("<worktree>/NOTES.md = (%q, %v), want root notes", b, err)
	}
	if _, err := os.Stat(filepath.Join(CellArtifacts(j), "root", "NOTES.md")); !os.IsNotExist(err) {
		t.Errorf("artifacts/root/NOTES.md exists (stat err %v), want the root note restored to the worktree only", err)
	}
}

// Contract (amended): `jobs.branchPrefixErr` "nil when branchPrefix()+"x"
// passes git check-ref-format --branch; otherwise an error naming
// KRAKEN_BRANCH_PREFIX and the value".
func TestContractPortBranchPrefixErr(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	contractPortEnv(t)
	if err := branchPrefixErr(); err != nil {
		t.Errorf("unset prefix: branchPrefixErr() = %v, want nil", err)
	}
	for _, ok := range []string{"carl/", "feat", "team/me/"} {
		t.Setenv("KRAKEN_BRANCH_PREFIX", ok)
		if err := branchPrefixErr(); err != nil {
			t.Errorf("KRAKEN_BRANCH_PREFIX=%q: branchPrefixErr() = %v, want nil", ok, err)
		}
	}
	t.Setenv("KRAKEN_BRANCH_PREFIX", "bad..name")
	err := branchPrefixErr()
	if err == nil {
		t.Fatal("KRAKEN_BRANCH_PREFIX=bad..name: branchPrefixErr() = nil, want an error")
	}
	if !strings.Contains(err.Error(), "KRAKEN_BRANCH_PREFIX") || !strings.Contains(err.Error(), "bad..name") {
		t.Errorf("branchPrefixErr() = %q, want it to name KRAKEN_BRANCH_PREFIX and the value", err)
	}
}

// Contract (amended): `jobs.zeusBrief` "states, once, that the literal
// {{operator}} in any skill file means Operator()".
func TestContractPortZeusBriefExplainsPlaceholder(t *testing.T) {
	contractPortAda(t)
	b := zeusBrief(&Job{Slug: "port-placeholder", ID: 12})
	if n := strings.Count(b, "{{operator}}"); n != 1 {
		t.Errorf("zeusBrief mentions {{operator}} %d times, want exactly once", n)
	}
	i := strings.Index(b, "{{operator}}")
	if i < 0 {
		return
	}
	lo, hi := max(0, i-200), min(len(b), i+200)
	if !strings.Contains(b[lo:hi], "Ada") {
		t.Errorf("the {{operator}} sentence does not say it means Ada: …%s…", b[lo:hi])
	}
}

// Contract: `jobs.LoadJobs` / `jobs.LoadJobBySlug` "returns nil when
// ActiveStore() == nil; reads no directory/file" — a legacy job file in the
// old pluto location is never picked up.
func TestContractPortLoadersNilWithoutStore(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	contractPortEnv(t)
	UseStore(nil)
	contractPortWrite(t, filepath.Join(util.Home(), "Documents", "pluto", "legacy"+JobSuffix),
		"---\njob: legacy\nrepo: kraken\n---\n\nbody\n")
	if got := LoadJobs(); got != nil {
		t.Errorf("LoadJobs() with no store = %#v, want nil", got)
	}
	if got := LoadJobBySlug("legacy"); got != nil {
		t.Errorf("LoadJobBySlug(legacy) with no store = %+v, want nil", got)
	}
}

// Contract: ArchiveJob/RenameJob "touch no path outside ~/.kraken;
// RenameResult.Skipped is always empty … and Notes carry no path" (fresh
// HOME: no ~/Documents afterwards).
func TestContractPortArchiveAndRenameStayInsideKraken(t *testing.T) {
	st := useTestStore(t)
	contractPortEnv(t)
	home := util.Home()

	mustCreateJob(t, st, store.Job{Slug: "port-ren", Repo: "kraken"})
	j := LoadJobBySlug("port-ren")
	res, err := RenameJob(j, "port-ren-new")
	if err != nil {
		t.Fatalf("RenameJob: %v", err)
	}
	if res != nil {
		if len(res.Skipped) != 0 {
			t.Errorf("RenameResult.Skipped = %v, want empty", res.Skipped)
		}
		notes := fmt.Sprint(res.Notes)
		if strings.Contains(notes, home) || strings.Contains(notes, "Documents") || strings.Contains(notes, "pluto") {
			t.Errorf("RenameResult.Notes carry a path: %s", notes)
		}
	}

	mustCreateJob(t, st, store.Job{Slug: "port-arch", Repo: "kraken", State: "done"})
	a := LoadJobBySlug("port-arch")
	if _, err := ArchiveJob(a, map[string]bool{}); err != nil {
		t.Fatalf("ArchiveJob: %v", err)
	}

	if _, err := os.Stat(filepath.Join(home, "Documents")); !os.IsNotExist(err) {
		t.Errorf("~/Documents exists after rename+archive (stat err %v), want nothing created outside ~/.kraken", err)
	}
}
