package jobs

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"
	"unicode/utf8"

	"kraken/internal/core"
	"kraken/internal/store"
	"kraken/internal/util"
)

// portability_edge_test.go — the EDGE dimension of artifacts/50-contract.md
// (kraken-portability-audit) for the internal/jobs symbols of slices E, N and
// P: boundaries, refusals and error paths. Written blind from the contract
// text; every helper is prefixed `edge` because athena-logic shares this
// package.

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// edgeHome isolates HOME (and git's XDG config) and returns it.
func edgeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	return home
}

// edgeUnsetEnv makes each key absent for the test, restoring it afterwards.
func edgeUnsetEnv(t *testing.T, keys ...string) {
	t.Helper()
	for _, k := range keys {
		t.Setenv(k, "edge-placeholder")
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}
}

// edgeKnobsClear unsets every portability knob this file reads.
func edgeKnobsClear(t *testing.T) {
	t.Helper()
	edgeUnsetEnv(t, "KRAKEN_REPOS_DIR", "KRAKEN_BRANCH_PREFIX", "KRAKEN_OPERATOR",
		"KRAKEN_PLUTO_DIR", "LINEAR_DEFAULT_REPO", "LINEAR_API_KEY", "LINEAR_STATES",
		"CLAUDE_POOL_DIR", "CODEX_POOL_DIR")
}

// edgeReposDir points KRAKEN_REPOS_DIR at a fresh dir holding one fake git
// checkout per name (isGitRepo is a Stat of .git) and returns the dir.
func edgeReposDir(t *testing.T, names ...string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "repos")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if err := os.MkdirAll(filepath.Join(dir, n, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("KRAKEN_REPOS_DIR", dir)
	return dir
}

type edgeNotes struct {
	mu   sync.Mutex
	msgs []string
}

func (n *edgeNotes) count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.msgs)
}

func edgeCaptureNotify(t *testing.T) *edgeNotes {
	t.Helper()
	n := &edgeNotes{}
	prev := notifyFn
	notifyFn = func(title, msg string) {
		n.mu.Lock()
		defer n.mu.Unlock()
		n.msgs = append(n.msgs, title+" | "+msg)
	}
	t.Cleanup(func() { notifyFn = prev })
	return n
}

func edgeEvents(t *testing.T, st *store.Store) []store.Event {
	t.Helper()
	evs, err := st.ListEvents(500, 0)
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

var edgeCarlWord = regexp.MustCompile(`(?i)\bcarl\b`)
var edgeMalePronoun = regexp.MustCompile(`(?i)\b(he|him|his)\b`)

// edgeGit runs git in dir with a deterministic identity and no signing.
func edgeGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{"-c", "user.name=edge", "-c", "user.email=edge@example.com",
		"-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

func edgeWrite(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// edgeOutsideKraken lists every path under home that is NOT under
// home/.kraken, so a test can assert a call created nothing elsewhere.
func edgeOutsideKraken(t *testing.T, home string) []string {
	t.Helper()
	var out []string
	_ = filepath.Walk(home, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(home, p)
		if rel == "." {
			return nil
		}
		if rel == ".kraken" || strings.HasPrefix(rel, ".kraken"+string(filepath.Separator)) {
			if info.IsDir() && rel == ".kraken" {
				return filepath.SkipDir
			}
			return nil
		}
		out = append(out, rel)
		return nil
	})
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// Slice E — repoRoot, ResolveRepo, RepoCandidates
// ---------------------------------------------------------------------------

// Contract: "KRAKEN_REPOS_DIR set to an absolute path → that path, cleaned ·
// unset, empty, or relative → filepath.Join(util.Home(), "Developer")".
func TestEdgeRepoRootBoundaries(t *testing.T) {
	home := edgeHome(t)
	edgeKnobsClear(t)
	def := filepath.Join(util.Home(), "Developer")
	if def != filepath.Join(home, "Developer") {
		t.Fatalf("fixture: util.Home() = %q, want HOME %q", util.Home(), home)
	}
	if got := repoRoot(); got != def {
		t.Fatalf("unset: repoRoot() = %q, want %q", got, def)
	}
	cases := []struct{ val, want string }{
		{"", def},
		{"relative/dir", def},
		{"./x", def},
		{"../up", def},
		{"~/repos", def}, // a tilde is not absolute; only the env FILE expands it
		{"Developer", def},
		{"/edge/abs/", "/edge/abs"},
		{"/edge/./a/../b", "/edge/b"},
		{"/edge//double", "/edge/double"},
		{"/", "/"},
	}
	for _, c := range cases {
		t.Setenv("KRAKEN_REPOS_DIR", c.val)
		if got := repoRoot(); got != c.want {
			t.Errorf("KRAKEN_REPOS_DIR=%q: repoRoot() = %q, want %q", c.val, got, c.want)
		}
	}
}

// An absolute knob is "returned without validation of contents" (C2): a dir
// that does not exist is still the answer.
func TestEdgeRepoRootNonexistentAbsoluteIsReturned(t *testing.T) {
	edgeHome(t)
	edgeKnobsClear(t)
	missing := filepath.Join(t.TempDir(), "never", "made")
	t.Setenv("KRAKEN_REPOS_DIR", missing)
	if got := repoRoot(); got != missing {
		t.Fatalf("repoRoot() = %q, want the unvalidated %q", got, missing)
	}
}

// The knob is read on every call, not cached at first use.
func TestEdgeRepoRootReadsTheKnobEveryCall(t *testing.T) {
	edgeHome(t)
	edgeKnobsClear(t)
	a, b := t.TempDir(), t.TempDir()
	t.Setenv("KRAKEN_REPOS_DIR", a)
	if got := repoRoot(); got != a {
		t.Fatalf("first: %q, want %q", got, a)
	}
	t.Setenv("KRAKEN_REPOS_DIR", b)
	if got := repoRoot(); got != b {
		t.Fatalf("after change: %q, want %q", got, b)
	}
}

// A relative knob falls back to ~/Developer for the READERS too: ResolveRepo
// finds a checkout under ~/Developer and nowhere else.
func TestEdgeResolveRepoRelativeKnobUsesDefault(t *testing.T) {
	home := edgeHome(t)
	edgeKnobsClear(t)
	want := filepath.Join(home, "Developer", "edgeonly")
	if err := os.MkdirAll(filepath.Join(want, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KRAKEN_REPOS_DIR", "edge-relative")
	got, err := ResolveRepo("edgeonly")
	if err != nil {
		t.Fatalf("ResolveRepo under a relative knob: %v", err)
	}
	if got != want {
		t.Fatalf("ResolveRepo = %q, want %q", got, want)
	}
}

// Contract: "every reader (ResolveRepo, RepoCandidates, PickRepo, …) goes
// through it" — with an absolute knob, a checkout under ~/Developer is
// invisible to both readers.
func TestEdgeReadersIgnoreDeveloperUnderAnAbsoluteKnob(t *testing.T) {
	home := edgeHome(t)
	edgeKnobsClear(t)
	if err := os.MkdirAll(filepath.Join(home, "Developer", "hidden", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	dir := edgeReposDir(t, "visible")
	if _, err := ResolveRepo("hidden"); err == nil {
		t.Error("ResolveRepo found a ~/Developer checkout while KRAKEN_REPOS_DIR names another root")
	}
	got, err := ResolveRepo("visible")
	if err != nil || got != filepath.Join(dir, "visible") {
		t.Errorf("ResolveRepo(visible) = (%q, %v), want %q", got, err, filepath.Join(dir, "visible"))
	}
	cands := RepoCandidates()
	if !reflect.DeepEqual(cands, []string{"visible"}) {
		t.Errorf("RepoCandidates() = %#v, want exactly [visible]", cands)
	}
}

// ---------------------------------------------------------------------------
// Slice E — DefaultRepo
// ---------------------------------------------------------------------------

// Contract: "RepoCandidates() has exactly one entry → that name · zero or two
// or more → "" · never "monolith"".
func TestEdgeDefaultRepoCardinality(t *testing.T) {
	edgeHome(t)
	edgeKnobsClear(t)

	// zero: the repos dir does not exist at all
	t.Setenv("KRAKEN_REPOS_DIR", filepath.Join(t.TempDir(), "absent"))
	if got := DefaultRepo(); got != "" {
		t.Errorf("absent repos dir: DefaultRepo() = %q, want empty", got)
	}
	// zero: exists, but holds only non-checkouts
	dir := edgeReposDir(t)
	if err := os.MkdirAll(filepath.Join(dir, "plain-dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	edgeWrite(t, filepath.Join(dir, "file.txt"), "x")
	if got := DefaultRepo(); got != "" {
		t.Errorf("no checkouts: DefaultRepo() = %q, want empty", got)
	}
	// one checkout among non-checkouts
	if err := os.MkdirAll(filepath.Join(dir, "alpha", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := DefaultRepo(); got != "alpha" {
		t.Errorf("one checkout: DefaultRepo() = %q, want alpha", got)
	}
	// two: never the alphabetically first
	if err := os.MkdirAll(filepath.Join(dir, "beta", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := DefaultRepo(); got != "" {
		t.Errorf("two checkouts: DefaultRepo() = %q, want empty", got)
	}
	// three
	if err := os.MkdirAll(filepath.Join(dir, "gamma", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := DefaultRepo(); got != "" {
		t.Errorf("three checkouts: DefaultRepo() = %q, want empty", got)
	}
}

// "never monolith" is about the hardcoded fallback: with zero candidates and
// two candidates neither of which is monolith, the answer is "" — and when
// the ONE candidate is genuinely named monolith, the one-candidate rule still
// names it (the rule derives, it does not blacklist).
func TestEdgeDefaultRepoMonolithOnlyWhenItIsTheSoleCheckout(t *testing.T) {
	edgeHome(t)
	edgeKnobsClear(t)
	edgeReposDir(t)
	if got := DefaultRepo(); got == "monolith" {
		t.Fatal("DefaultRepo() fell back to monolith with no checkouts")
	}
	edgeReposDir(t, "monolith")
	if got := DefaultRepo(); got != "monolith" {
		t.Errorf("sole checkout named monolith: DefaultRepo() = %q, want monolith", got)
	}
	edgeReposDir(t, "monolith", "other")
	if got := DefaultRepo(); got != "" {
		t.Errorf("monolith + other: DefaultRepo() = %q, want empty", got)
	}
}

// ---------------------------------------------------------------------------
// Slice E — JobTemplate
// ---------------------------------------------------------------------------

// edgeRepoLineRest returns what follows "repo:" on the template's repo line.
func edgeRepoLineRest(t *testing.T, tmpl string) string {
	t.Helper()
	var found []string
	for _, line := range strings.Split(tmpl, "\n") {
		if strings.HasPrefix(line, "repo:") {
			found = append(found, strings.TrimPrefix(line, "repo:"))
		}
	}
	if len(found) != 1 {
		t.Fatalf("template has %d `repo:` lines, want exactly 1:\n%s", len(found), tmpl)
	}
	return found[0]
}

// Contract: "with "" the line is `repo:` followed only by the existing
// comment" and ParseJob yields Repo == DefaultRepo() with no parse error; the
// word monolith does not appear.
func TestEdgeJobTemplateEmptyDefault(t *testing.T) {
	edgeHome(t)
	edgeKnobsClear(t)
	for name, repos := range map[string][]string{"zero": nil, "two": {"alpha", "beta"}} {
		t.Run(name, func(t *testing.T) {
			edgeReposDir(t, repos...)
			tmpl := JobTemplate("edge-tmpl")
			rest := strings.TrimSpace(edgeRepoLineRest(t, tmpl))
			if rest != "" && !strings.HasPrefix(rest, "#") {
				t.Errorf("repo: line carries a value %q with no default repo, want only a comment", rest)
			}
			if strings.Contains(strings.ToLower(tmpl), "monolith") {
				t.Errorf("template mentions monolith:\n%s", tmpl)
			}
			j := ParseJob("edge-tmpl"+JobSuffix, []byte(tmpl))
			if j == nil {
				t.Fatal("ParseJob returned nil for the template")
			}
			if j.Err != "" {
				t.Errorf("template does not parse: %s", j.Err)
			}
			if j.Repo != "" {
				t.Errorf("parsed Repo = %q, want empty", j.Repo)
			}
		})
	}
}

// One candidate: the line's value is that name and nothing but an optional
// comment follows it.
func TestEdgeJobTemplateSoleCheckout(t *testing.T) {
	edgeHome(t)
	edgeKnobsClear(t)
	edgeReposDir(t, "solo-repo")
	tmpl := JobTemplate("edge-tmpl-one")
	rest := strings.TrimSpace(edgeRepoLineRest(t, tmpl))
	if !strings.HasPrefix(rest, "solo-repo") {
		t.Fatalf("repo: line = %q, want it to start with solo-repo", rest)
	}
	after := strings.TrimSpace(strings.TrimPrefix(rest, "solo-repo"))
	if after != "" && !strings.HasPrefix(after, "#") {
		t.Errorf("repo: line carries %q after the name, want only a comment", after)
	}
	j := ParseJob("edge-tmpl-one"+JobSuffix, []byte(tmpl))
	if j == nil || j.Err != "" || j.Repo != "solo-repo" {
		t.Fatalf("ParseJob(template) = %+v, want Repo solo-repo and no error", j)
	}
	if strings.Contains(strings.ToLower(tmpl), "monolith") {
		t.Errorf("template mentions monolith")
	}
}

// ---------------------------------------------------------------------------
// Slice E — Linear default repo
// ---------------------------------------------------------------------------

// Contract: "DefaultRepo is "" when LINEAR_DEFAULT_REPO is absent from both
// the environment and the file; set → that value".
func TestEdgeLoadLinearConfigDefaultRepoAbsentEverywhere(t *testing.T) {
	home := edgeHome(t)
	edgeKnobsClear(t)
	// even with exactly one checkout, the CORE config does not derive it
	edgeReposDir(t, "only")
	if got := core.LoadLinearConfig().DefaultRepo; got != "" {
		t.Errorf("no env, no file: DefaultRepo = %q, want empty", got)
	}
	// a file that exists but lacks the key
	edgeWrite(t, filepath.Join(home, ".kraken", "linear.env"), "LINEAR_API_KEY=lin_x\n")
	if got := core.LoadLinearConfig().DefaultRepo; got != "" {
		t.Errorf("file without the key: DefaultRepo = %q, want empty", got)
	}
	// the file sets it
	edgeWrite(t, filepath.Join(home, ".kraken", "linear.env"), "LINEAR_API_KEY=lin_x\nLINEAR_DEFAULT_REPO=from-file\n")
	if got := core.LoadLinearConfig().DefaultRepo; got != "from-file" {
		t.Errorf("file sets it: DefaultRepo = %q, want from-file", got)
	}
	// the environment wins over the file
	t.Setenv("LINEAR_DEFAULT_REPO", "from-env")
	if got := core.LoadLinearConfig().DefaultRepo; got != "from-env" {
		t.Errorf("env sets it: DefaultRepo = %q, want from-env", got)
	}
}

func edgeLinearIssue() core.LinearIssue {
	ts := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	return core.LinearIssue{
		ID: "edge-uuid-1", Identifier: "ENG-901", Title: "Edge ticket",
		Description: "body", URL: "https://linear.app/x/issue/ENG-901",
		StateName: "Today", CreatedAt: ts, UpdatedAt: ts,
	}
}

// Contract: "the repo: line is cfg.DefaultRepo when non-empty, else
// DefaultRepo()" — at every DefaultRepo cardinality.
func TestEdgeComposeLinearBodyRepoFallback(t *testing.T) {
	edgeHome(t)
	edgeKnobsClear(t)
	cases := []struct {
		name    string
		cfgRepo string
		repos   []string
		want    string
	}{
		{"cfg empty, zero checkouts", "", nil, ""},
		{"cfg empty, one checkout", "", []string{"alpha"}, "alpha"},
		{"cfg empty, two checkouts", "", []string{"alpha", "beta"}, ""},
		{"cfg set, one other checkout", "beta", []string{"alpha"}, "beta"},
		{"cfg set, zero checkouts", "beta", nil, "beta"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			edgeReposDir(t, c.repos...)
			cfg := core.LinearConfig{APIKey: "lin_x", DefaultRepo: c.cfgRepo, States: []string{"Today"}}
			text := composeLinearBody(edgeLinearIssue(), cfg, nil)
			j := ParseJob("edge-lin"+JobSuffix, []byte(text))
			if j == nil {
				t.Fatalf("composed text does not parse:\n%s", text)
			}
			if j.Repo != c.want {
				t.Errorf("Repo = %q, want %q\n%s", j.Repo, c.want, text)
			}
			if c.want == "" && strings.Contains(strings.ToLower(text), "repo: monolith") {
				t.Errorf("composed text fell back to monolith:\n%s", text)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Slice E — kickDream gate
// ---------------------------------------------------------------------------

func edgeDreamNoon() time.Time {
	n := time.Now()
	return time.Date(n.Year(), n.Month(), n.Day(), 12, 0, 0, 0, time.Local)
}

// Contract: "when ResolveRepo("kraken") returns an error: returns nil, creates
// no job row, calls no notification, records today in lastDreamDate so a
// second call the same day returns nil without resolving again, and logs one
// event (dream-skipped)". The night carries real evidence (a feedback row),
// so only the gate can be what stops it; the second call comes AFTER kraken
// became resolvable, so only the date record can be what stops it.
func TestEdgeKickDreamSkipsWhenKrakenUnresolvable(t *testing.T) {
	st := useTestStore(t)
	edgeKnobsClear(t)
	reposDir := edgeReposDir(t, "other")
	lastDreamDate.Store("")
	t.Cleanup(func() { lastDreamDate.Store("") })
	notes := edgeCaptureNotify(t)
	now := edgeDreamNoon()
	if _, err := st.AddFeedback(store.Feedback{JobSlug: "x", Note: "edge evidence", TS: now.UnixMilli()}); err != nil {
		t.Fatal(err)
	}

	if got := kickDream(now); got != nil {
		t.Fatalf("kickDream created %q with kraken unresolvable, want nil", got.Slug)
	}
	if jobs := LoadJobs(); len(jobs) != 0 {
		t.Fatalf("board holds %d jobs after a skipped dream, want 0", len(jobs))
	}
	if n := notes.count(); n != 0 {
		t.Fatalf("%d notifications on a skipped dream, want 0: %v", n, notes.msgs)
	}
	if got := fmt.Sprint(lastDreamDate.Load()); got != now.Format(dreamDateLayout) {
		t.Fatalf("lastDreamDate = %q, want today %q", got, now.Format(dreamDateLayout))
	}
	skipped := 0
	for _, e := range edgeEvents(t, st) {
		if e.Event == "dream-skipped" {
			skipped++
		}
	}
	if skipped != 1 {
		t.Fatalf("%d dream-skipped events, want exactly 1", skipped)
	}

	// kraken becomes resolvable mid-day: the date record still holds.
	if err := os.MkdirAll(filepath.Join(reposDir, "kraken", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := kickDream(now.Add(time.Hour)); got != nil {
		t.Fatalf("second same-day call created %q, want nil", got.Slug)
	}
	if jobs := LoadJobs(); len(jobs) != 0 {
		t.Fatalf("board holds %d jobs after the second call, want 0", len(jobs))
	}
	if n := notes.count(); n != 0 {
		t.Fatalf("%d notifications after the second call, want 0", n)
	}
	skipped = 0
	for _, e := range edgeEvents(t, st) {
		if e.Event == "dream-skipped" {
			skipped++
		}
	}
	if skipped != 1 {
		t.Fatalf("%d dream-skipped events after a second same-day call, want still 1", skipped)
	}
}

// ---------------------------------------------------------------------------
// Slice E — bind notice suppression
// ---------------------------------------------------------------------------

// edgeClaudeSnapshot seeds a claude usage snapshot whose one slot is full, so
// the binder has a snapshot but no eligible target.
func edgeClaudeSnapshot(t *testing.T) {
	t.Helper()
	now := time.Now().UnixMilli()
	snap := core.UsageSnapshot{FetchedAt: now, Accounts: []core.AccountUsage{{
		Slug: "edge-slot", Email: "edge-slot@example.com", FetchedAt: now,
		Session: &core.UsageWindow{Percent: 100}, Weekly: &core.UsageWindow{Percent: 100},
	}}}
	if err := writeUsageSnapshot(snap, UsageSnapshotPath()); err != nil {
		t.Fatal(err)
	}
}

// edgeCodexSnapshot seeds a codex snapshot naming a slot that has no
// auth.json anywhere, so the codex binder has no usable target.
func edgeCodexSnapshot(t *testing.T, st *store.Store) {
	t.Helper()
	now := time.Now().UnixMilli()
	snap := core.UsageSnapshot{FetchedAt: now, Accounts: []core.AccountUsage{{
		Slug: "edge-cx", Email: "edge-cx@example.com", FetchedAt: now,
		Session: &core.UsageWindow{Percent: 1},
	}}}
	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetKV("usage_snapshot_codex", string(b)); err != nil {
		t.Fatal(err)
	}
}

func edgeBindEventLogged(t *testing.T, st *store.Store) bool {
	t.Helper()
	for _, e := range edgeEvents(t, st) {
		if strings.Contains(e.Event, "bind") {
			return true
		}
	}
	return false
}

// Contract: "in the no-target branch, notifyDeduped is NOT called when the
// pool root does not exist as a directory; the bind-skipped-style event is
// still logged and the job stays unbound". A pool root that is a FILE is not
// a directory. The paired DIRECTORY case is the control: same snapshot, same
// job shape, and the existing notification fires — so the file case can only
// be silent because of the directory test.
func TestEdgeBindClaudePoolRootIsAFile(t *testing.T) {
	for _, rootIsDir := range []bool{false, true} {
		t.Run(fmt.Sprintf("dir=%v", rootIsDir), func(t *testing.T) {
			st := useTestStore(t)
			edgeKnobsClear(t)
			notes := edgeCaptureNotify(t)
			root := filepath.Join(t.TempDir(), "claude-pool")
			if rootIsDir {
				if err := os.MkdirAll(root, 0o755); err != nil {
					t.Fatal(err)
				}
			} else {
				edgeWrite(t, root, "not a directory")
			}
			t.Setenv("CLAUDE_POOL_DIR", root)
			if core.PoolDir() != root {
				t.Fatalf("fixture: core.PoolDir() = %q, want %q", core.PoolDir(), root)
			}
			edgeClaudeSnapshot(t)
			row := mustCreateJob(t, st, store.Job{Slug: fmt.Sprintf("edge-bind-%v", rootIsDir), Repo: "x"})
			j := &Job{Slug: row.Slug, ID: row.ID, Repo: "x"}

			bindAccount(j, "test")

			if j.AccountSlug != "" {
				t.Fatalf("job bound to %q with no eligible slot", j.AccountSlug)
			}
			stored, err := st.GetJobByID(row.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.AccountSlug != "" {
				t.Fatalf("stored row bound to %q", stored.AccountSlug)
			}
			if !edgeBindEventLogged(t, st) {
				t.Errorf("no bind event logged — the skip must still be visible in the log")
			}
			if rootIsDir {
				if notes.count() == 0 {
					t.Errorf("pool root exists but nothing eligible: want the existing notification, got none")
				}
			} else if notes.count() != 0 {
				t.Errorf("pool root is a file: %d notifications, want 0: %v", notes.count(), notes.msgs)
			}
		})
	}
}

// Same rule for the codex arm against core.CodexPoolDir().
func TestEdgeBindCodexPoolRootIsAFile(t *testing.T) {
	for _, rootIsDir := range []bool{false, true} {
		t.Run(fmt.Sprintf("dir=%v", rootIsDir), func(t *testing.T) {
			st := useTestStore(t)
			edgeKnobsClear(t)
			notes := edgeCaptureNotify(t)
			root := filepath.Join(t.TempDir(), "codex-pool")
			if rootIsDir {
				if err := os.MkdirAll(root, 0o755); err != nil {
					t.Fatal(err)
				}
			} else {
				edgeWrite(t, root, "not a directory")
			}
			t.Setenv("CODEX_POOL_DIR", root)
			edgeCodexSnapshot(t, st)
			row := mustCreateJob(t, st, store.Job{Slug: fmt.Sprintf("edge-cxbind-%v", rootIsDir), Repo: "x", Agent: "codex"})
			j := &Job{Slug: row.Slug, ID: row.ID, Repo: "x", Agent: "codex"}

			bindAccount(j, "test")

			if j.AccountSlug != "" {
				t.Fatalf("codex job bound to %q with no usable slot", j.AccountSlug)
			}
			if !edgeBindEventLogged(t, st) {
				t.Errorf("no bind event logged for the codex skip")
			}
			if rootIsDir {
				if notes.count() == 0 {
					t.Errorf("codex pool root exists but nothing usable: want the existing notification, got none")
				}
			} else if notes.count() != 0 {
				t.Errorf("codex pool root is a file: %d notifications, want 0: %v", notes.count(), notes.msgs)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Slice E — branch prefix
// ---------------------------------------------------------------------------

// Contract: "KRAKEN_BRANCH_PREFIX trimmed; empty → kraken/; a value without a
// trailing / gets one appended; carl/ → carl/".
func TestEdgeBranchPrefixBoundaries(t *testing.T) {
	edgeHome(t)
	edgeKnobsClear(t)
	if got := branchPrefix(); got != "kraken/" {
		t.Fatalf("unset: branchPrefix() = %q, want kraken/", got)
	}
	cases := []struct{ val, want string }{
		{"", "kraken/"},
		{"   ", "kraken/"},
		{"\t\n", "kraken/"},
		{"carl/", "carl/"},
		{"carl", "carl/"},
		{"  carl/  ", "carl/"},
		{"\tcarl\n", "carl/"},
		{"team/sub", "team/sub/"},
		{"team/sub/", "team/sub/"},
		{"my team", "my team/"}, // trimmed only at the ends; inner space is the operator's business
		{"ada-", "ada-/"},
	}
	for _, c := range cases {
		t.Setenv("KRAKEN_BRANCH_PREFIX", c.val)
		if got := branchPrefix(); got != c.want {
			t.Errorf("KRAKEN_BRANCH_PREFIX=%q: branchPrefix() = %q, want %q", c.val, got, c.want)
		}
	}
}

// Contract note: "the prefix is read on every call".
func TestEdgeBranchPrefixReadEveryCall(t *testing.T) {
	edgeHome(t)
	edgeKnobsClear(t)
	j := &Job{Slug: "edge-pfx", ID: 41}
	t.Setenv("KRAKEN_BRANCH_PREFIX", "one/")
	first := CellBranch(j)
	t.Setenv("KRAKEN_BRANCH_PREFIX", "two")
	second := CellBranch(j)
	if first != "one/"+cellName(j) || second != "two/"+cellName(j) {
		t.Fatalf("CellBranch = %q then %q, want one/… then two/…", first, second)
	}
}

// Contract: "branchForRun(j, 1) == branchPrefix()+cellName(j);
// branchForRun(j, n≥2) == branchPrefix()+cellName(j)+"-r"+n" — at the
// boundary run 2, a two-digit run, and through CellBranch's Run 0/1.
func TestEdgeBranchForRunBoundaries(t *testing.T) {
	edgeHome(t)
	edgeKnobsClear(t)
	j := &Job{Slug: "Edge Job", ID: 12}
	for _, pfx := range []string{"", "carl/", "my team", "a/b"} {
		t.Setenv("KRAKEN_BRANCH_PREFIX", pfx)
		p := branchPrefix()
		base := p + cellName(j)
		if got := branchForRun(j, 1); got != base {
			t.Errorf("prefix %q run 1 = %q, want %q", pfx, got, base)
		}
		if got := branchForRun(j, 2); got != base+"-r2" {
			t.Errorf("prefix %q run 2 = %q, want %q", pfx, got, base+"-r2")
		}
		if got := branchForRun(j, 10); got != base+"-r10" {
			t.Errorf("prefix %q run 10 = %q, want %q", pfx, got, base+"-r10")
		}
		for _, run := range []int{0, 1} {
			jj := &Job{Slug: j.Slug, ID: j.ID, Run: run}
			if got := CellBranch(jj); got != base {
				t.Errorf("prefix %q CellBranch(Run %d) = %q, want %q", pfx, run, got, base)
			}
		}
	}
}

// With KRAKEN_BRANCH_PREFIX=carl/ the names are byte-identical to the
// pre-change shape carl/<slug>-<id>[-rN].
func TestEdgeBranchCarlPrefixByteIdentical(t *testing.T) {
	edgeHome(t)
	edgeKnobsClear(t)
	t.Setenv("KRAKEN_BRANCH_PREFIX", "carl/")
	j := &Job{Slug: "helper-catalog", ID: 111}
	if got := branchForRun(j, 1); got != "carl/helper-catalog-111" {
		t.Errorf("run 1 = %q", got)
	}
	if got := branchForRun(j, 4); got != "carl/helper-catalog-111-r4" {
		t.Errorf("run 4 = %q", got)
	}
}

// Contract: branchFamily holds the current prefix's names, the same names
// under carl/, and the bare-slug forms, "with no duplicates". When the prefix
// IS carl/ — in any spelling the prefix rule normalizes to carl/ — the two
// halves collapse to ONE set.
func TestEdgeBranchFamilyDedupesWhenPrefixIsCarl(t *testing.T) {
	edgeHome(t)
	edgeKnobsClear(t)
	for _, pfx := range []string{"carl/", "carl", "  carl/  "} {
		t.Setenv("KRAKEN_BRANCH_PREFIX", pfx)
		fam := branchFamily(&Job{Slug: "slug", ID: 7, Run: 1})
		want := map[string]bool{"carl/slug-7": true, "carl/slug": true}
		if !reflect.DeepEqual(fam, want) {
			t.Errorf("prefix %q run 1: family = %v, want exactly %v", pfx, fam, want)
		}
		fam3 := branchFamily(&Job{Slug: "slug", ID: 7, Run: 3})
		if len(fam3) != 6 {
			t.Errorf("prefix %q run 3: %d names %v, want 6 (no duplicate carl/ half)", pfx, len(fam3), fam3)
		}
	}
}

// Default prefix, run 3: the kraken/ half and the carl/ half, each with the
// id form and the bare-slug form for runs 1..3 — twelve distinct names, and
// every one of them present.
func TestEdgeBranchFamilyDefaultPrefixRunN(t *testing.T) {
	edgeHome(t)
	edgeKnobsClear(t)
	fam := branchFamily(&Job{Slug: "slug", ID: 7, Run: 3})
	var want []string
	for _, p := range []string{"kraken/", "carl/"} {
		for _, base := range []string{"slug-7", "slug"} {
			want = append(want, p+base, p+base+"-r2", p+base+"-r3")
		}
	}
	for _, w := range want {
		if !fam[w] {
			t.Errorf("default prefix run 3: missing %q", w)
		}
	}
	if len(fam) != len(want) {
		t.Errorf("default prefix run 3: %d names %v, want exactly %d", len(fam), fam, len(want))
	}
	// a name that merely shares the slug as a prefix is not in the family
	for _, stray := range []string{"kraken/slug-70", "carl/slug-7-r4", "kraken/slugx", "slug-7"} {
		if fam[stray] {
			t.Errorf("family contains stray %q", stray)
		}
	}
}

// A custom prefix: three halves would be wrong, two are right — the custom
// prefix and carl/; never the default kraken/ the operator did not choose.
func TestEdgeBranchFamilyCustomPrefixIsNotDefault(t *testing.T) {
	edgeHome(t)
	edgeKnobsClear(t)
	t.Setenv("KRAKEN_BRANCH_PREFIX", "ada")
	fam := branchFamily(&Job{Slug: "slug", ID: 7, Run: 1})
	want := map[string]bool{"ada/slug-7": true, "ada/slug": true, "carl/slug-7": true, "carl/slug": true}
	if !reflect.DeepEqual(fam, want) {
		t.Errorf("family = %v, want exactly %v", fam, want)
	}
}

// ---------------------------------------------------------------------------
// Slice E — PreflightShipIOS refusal text
// ---------------------------------------------------------------------------

// Contract: "the credentials-missing error text names ~/.kraken/asc.env and
// does not contain launchctl setenv" — with neither id, and with only one.
func TestEdgePreflightShipIOSCredentialRefusalText(t *testing.T) {
	for name, set := range map[string]map[string]string{
		"neither":     {"ASC_KEY_ID": "", "ASC_ISSUER_ID": ""},
		"key only":    {"ASC_KEY_ID": "EDGEKEY123", "ASC_ISSUER_ID": ""},
		"issuer only": {"ASC_KEY_ID": "", "ASC_ISSUER_ID": "edge-issuer"},
	} {
		t.Run(name, func(t *testing.T) {
			edgeHome(t)
			edgeKnobsClear(t)
			for k, v := range set {
				t.Setenv(k, v)
			}
			repo := fakeKrakenRepo(t)
			fakeShipScript(t, repo)
			stubShipGit(t, "main")
			stubLookPath(t, func(string) (string, error) { return "/opt/homebrew/bin/xcodegen", nil })

			_, err := PreflightShipIOS()
			if err == nil {
				t.Fatal("preflight succeeded with credentials missing")
			}
			msg := err.Error()
			if !strings.Contains(msg, "~/.kraken/asc.env") {
				t.Errorf("refusal %q does not name ~/.kraken/asc.env", msg)
			}
			if strings.Contains(msg, "launchctl setenv") {
				t.Errorf("refusal %q still recommends launchctl setenv", msg)
			}
			if strings.Contains(msg, "EDGEKEY123") || strings.Contains(msg, "edge-issuer") {
				t.Errorf("refusal %q echoes a credential value", msg)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Slice N — Operator
// ---------------------------------------------------------------------------

// Contract: "KRAKEN_OPERATOR trimmed, when non-empty; longer than 64 runes →
// the first 64 · else USER trimmed when non-empty · else "the operator" ·
// never "carl" by default".
func TestEdgeOperatorFallbackLadder(t *testing.T) {
	edgeHome(t)
	edgeKnobsClear(t)
	cases := []struct {
		name     string
		op, user *string
		want     string
	}{
		{"both unset", nil, nil, "the operator"},
		{"op empty, user unset", edgePtr(""), nil, "the operator"},
		{"op whitespace, user empty", edgePtr("  \t "), edgePtr(""), "the operator"},
		{"op whitespace, user whitespace", edgePtr(" "), edgePtr("   "), "the operator"},
		{"op whitespace, user set", edgePtr("   "), edgePtr("  bob  "), "bob"},
		{"op unset, user set", nil, edgePtr("bob"), "bob"},
		{"op set, user set", edgePtr("  Ada Lovelace  "), edgePtr("bob"), "Ada Lovelace"},
		{"op set explicitly to carl", edgePtr("carl"), edgePtr("bob"), "carl"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			edgeUnsetEnv(t, "KRAKEN_OPERATOR", "USER")
			if c.op != nil {
				t.Setenv("KRAKEN_OPERATOR", *c.op)
			}
			if c.user != nil {
				t.Setenv("USER", *c.user)
			}
			if got := Operator(); got != c.want {
				t.Errorf("Operator() = %q, want %q", got, c.want)
			}
		})
	}
}

func edgePtr(s string) *string { return &s }

// Contract: "longer than 64 runes → the first 64" — runes, not bytes, and
// exactly at the boundary.
func TestEdgeOperatorRuneTruncation(t *testing.T) {
	edgeHome(t)
	edgeKnobsClear(t)
	t.Setenv("USER", "bob")
	cases := []struct{ in, want string }{
		{strings.Repeat("a", 64), strings.Repeat("a", 64)},
		{strings.Repeat("a", 65), strings.Repeat("a", 64)},
		{strings.Repeat("é", 64), strings.Repeat("é", 64)},
		{strings.Repeat("é", 65), strings.Repeat("é", 64)},
		{strings.Repeat("名", 100), strings.Repeat("名", 64)},
		{"   " + strings.Repeat("x", 70) + "   ", strings.Repeat("x", 64)},
	}
	for _, c := range cases {
		t.Setenv("KRAKEN_OPERATOR", c.in)
		got := Operator()
		if got != c.want {
			t.Errorf("KRAKEN_OPERATOR of %d runes: Operator() = %q (%d runes), want %d runes",
				utf8.RuneCountInString(c.in), got, utf8.RuneCountInString(got), utf8.RuneCountInString(c.want))
		}
		if !utf8.ValidString(got) {
			t.Errorf("Operator() returned invalid UTF-8 %q — truncation split a rune", got)
		}
	}
}

// ---------------------------------------------------------------------------
// Slice N — headers under the fallback and under awkward names
// ---------------------------------------------------------------------------

// Contract: the headers name Operator() possessively as `<name>'s`, and
// prWatchPrefix is an exact string. Under the "the operator" fallback the
// possessive is "the operator's"; a name carrying a quote, a printf verb or
// a placeholder is rendered literally.
func TestEdgeOperatorStringsRenderNamesLiterally(t *testing.T) {
	edgeHome(t)
	edgeKnobsClear(t)
	for _, c := range []struct {
		op   *string
		name string
	}{
		{nil, "the operator"},
		{edgePtr("O'Brien"), "O'Brien"},
		{edgePtr(`Ada "the" %s %d`), `Ada "the" %s %d`},
		{edgePtr("{{operator}}"), "{{operator}}"},
		{edgePtr("Zoë"), "Zoë"},
	} {
		edgeUnsetEnv(t, "KRAKEN_OPERATOR", "USER")
		if c.op != nil {
			t.Setenv("KRAKEN_OPERATOR", *c.op)
		}
		if got := Operator(); got != c.name {
			t.Fatalf("fixture: Operator() = %q, want %q", got, c.name)
		}
		if got, want := prWatchPrefix(), "[kraken pr-watch — automated notice, not a reply from "+c.name+"]"; got != want {
			t.Errorf("prWatchPrefix() = %q, want %q", got, want)
		}
		if got := goalContextHeader(); !strings.Contains(got, c.name+"'s goals right now") {
			t.Errorf("goalContextHeader() = %q, want it to contain %q", got, c.name+"'s goals right now")
		}
		if got := notesContextHeader(); !strings.Contains(got, c.name+"'s unread notes on this job") {
			t.Errorf("notesContextHeader() = %q, want it to contain %q", got, c.name+"'s unread notes on this job")
		}
		wantMM := c.name + "'s mental model — one line per item they have committed, each title a claim they hold."
		if got := mmIndexHeader(); !strings.HasPrefix(got, wantMM) {
			t.Errorf("mmIndexHeader() = %q, want prefix %q", got, wantMM)
		}
		for name, s := range map[string]string{
			"goalContextHeader": goalContextHeader(), "notesContextHeader": notesContextHeader(),
			"mmIndexHeader": mmIndexHeader(), "prWatchPrefix": prWatchPrefix(),
		} {
			if strings.Contains(s, "%!") {
				t.Errorf("%s() = %q went through a format verb", name, s)
			}
			if c.op == nil && edgeCarlWord.MatchString(s) {
				t.Errorf("%s() names carl under the fallback: %q", name, s)
			}
		}
	}
}

// The headers change with the env between calls (they are functions now, not
// constants frozen at init).
func TestEdgeOperatorHeadersFollowTheEnv(t *testing.T) {
	edgeHome(t)
	edgeKnobsClear(t)
	t.Setenv("KRAKEN_OPERATOR", "Ada")
	a := goalContextHeader()
	t.Setenv("KRAKEN_OPERATOR", "Grace")
	g := goalContextHeader()
	if !strings.Contains(a, "Ada's") || !strings.Contains(g, "Grace's") {
		t.Fatalf("goalContextHeader() did not follow KRAKEN_OPERATOR: %q then %q", a, g)
	}
	// "otherwise byte-identical": the two differ only by the name
	if strings.Replace(a, "Ada", "Grace", 1) != g {
		t.Errorf("headers differ beyond the name: %q vs %q", a, g)
	}
	n1 := notesContextHeader()
	t.Setenv("KRAKEN_OPERATOR", "Ada")
	n2 := notesContextHeader()
	if strings.Replace(n2, "Ada", "Grace", 1) != n1 {
		t.Errorf("notes headers differ beyond the name: %q vs %q", n2, n1)
	}
}

// ---------------------------------------------------------------------------
// Slice N — briefs
// ---------------------------------------------------------------------------

// Contract: with KRAKEN_OPERATOR set, the briefs name it and carry no
// carl/Carl/CARL (word-bounded); pronouns for the operator are they/them. The
// EDGE cases: the fallback name (no KRAKEN_OPERATOR, USER empty) and an
// operator literally named something awkward.
func TestEdgeBriefsUnderTheFallbackName(t *testing.T) {
	edgeHome(t)
	edgeKnobsClear(t)
	edgeUnsetEnv(t, "KRAKEN_OPERATOR")
	t.Setenv("USER", "")
	edgeReposDir(t)
	j := &Job{Slug: "edge-brief", ID: 88, Repo: "x"}
	briefs := map[string]string{
		"zeusBrief":        zeusBrief(j),
		"threadBrief/ask":  threadBrief(j, "t1", "why", "/tmp/wt-x", "ask"),
		"threadBrief/mind": threadBrief(j, "t2", "a mind title", "/tmp/wt-x", "mind"),
		"irisBrief":        irisBrief(),
	}
	for name, b := range briefs {
		if loc := edgeCarlWord.FindStringIndex(b); loc != nil {
			lo, hi := loc[0]-40, loc[1]+40
			if lo < 0 {
				lo = 0
			}
			if hi > len(b) {
				hi = len(b)
			}
			t.Errorf("%s names carl with no operator configured: …%s…", name, b[lo:hi])
		}
		if !strings.Contains(b, "the operator") {
			t.Errorf("%s never says 'the operator' under the fallback", name)
		}
		if loc := edgeMalePronoun.FindStringIndex(b); loc != nil {
			lo, hi := loc[0]-40, loc[1]+40
			if lo < 0 {
				lo = 0
			}
			if hi > len(b) {
				hi = len(b)
			}
			t.Errorf("%s uses he/him/his: …%s…", name, b[lo:hi])
		}
	}
}

// A name with a quote and a format verb rides into the brief literally.
func TestEdgeZeusBriefAwkwardName(t *testing.T) {
	edgeHome(t)
	edgeKnobsClear(t)
	t.Setenv("KRAKEN_OPERATOR", `D'Arcy "%v"`)
	b := zeusBrief(&Job{Slug: "edge-brief-q", ID: 89, Repo: "x"})
	if !strings.Contains(b, `D'Arcy "%v"`) {
		t.Errorf("zeusBrief does not carry the name literally")
	}
	if strings.Contains(b, "%!") {
		t.Errorf("zeusBrief passed the name through a format verb")
	}
}

// Contract: irisBrief says "`repo:` defaults to `<DefaultRepo()>`" when that is
// non-empty, and "has no default on this machine" when empty — at zero, one
// and two checkouts.
func TestEdgeIrisBriefDefaultRepoSentence(t *testing.T) {
	edgeHome(t)
	edgeKnobsClear(t)
	t.Setenv("KRAKEN_OPERATOR", "Ada")
	for _, c := range []struct {
		name  string
		repos []string
		want  string
		not   string
	}{
		{"zero", nil, "has no default on this machine", "`repo:` defaults to `"},
		{"one", []string{"solo-repo"}, "`repo:` defaults to `solo-repo`", "has no default on this machine"},
		{"two", []string{"alpha", "beta"}, "has no default on this machine", "`repo:` defaults to `"},
	} {
		t.Run(c.name, func(t *testing.T) {
			edgeReposDir(t, c.repos...)
			b := irisBrief()
			if !strings.Contains(b, c.want) {
				t.Errorf("irisBrief() lacks %q", c.want)
			}
			if strings.Contains(b, c.not) {
				t.Errorf("irisBrief() contains %q", c.not)
			}
			if strings.Contains(strings.ToLower(b), "monolith") {
				t.Errorf("irisBrief() still names monolith")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Slice N — installSkills
// ---------------------------------------------------------------------------

func edgeSkillsFS() fstest.MapFS {
	return fstest.MapFS{
		"aim/SKILL.md":           {Data: []byte("---\nname: aim\n---\nAsk {{operator}}. {{operator}}'s call.\nKeep {{ operator }} and {{Operator}} and {operator}.\n")},
		"do/SKILL.md":            {Data: []byte("no placeholder here\n")},
		"aim/assets/contract.md": {Data: []byte("nested {{operator}}{{operator}} twice\n")},
		"aim/assets/fixture.bin": {Data: []byte{0x00, 0xFF, '{', '{', 0x01}},
		"handoff/SKILL.md":       {Data: []byte("{{operator}}")},
		"load/SKILL.md":          {Data: []byte("{{operator")},
		"dream/SKILL.md":         {Data: []byte("{{operator}}}}{{")},
	}
}

func edgeSwapSkills(t *testing.T, fsys fstest.MapFS) {
	t.Helper()
	prevFS, prevTracked := SkillsFS, isTracked
	SkillsFS = fsys
	isTracked = func(string, string) bool { return false }
	t.Cleanup(func() { SkillsFS, isTracked = prevFS, prevTracked })
}

// Contract: "every file materialized under <wt>/.claude/skills/ has each
// literal {{operator}} replaced by Operator(); the embedded SkillsFS content
// is unchanged; a name containing {{ or a quote is inserted literally, never
// re-expanded". The strict form: each materialized file equals the embedded
// bytes with exactly strings.ReplaceAll("{{operator}}", name) applied once.
func TestEdgeInstallSkillsLiteralSubstitution(t *testing.T) {
	for _, name := range []string{
		`Ada`,
		`{{operator}}`,
		`x{{operator}}y "q" 'r'`,
		`{{ operator }}`,
		`$HOME %s \n`,
		`the operator`,
	} {
		t.Run(name, func(t *testing.T) {
			edgeHome(t)
			edgeKnobsClear(t)
			t.Setenv("KRAKEN_OPERATOR", name)
			fsys := edgeSkillsFS()
			before := map[string][]byte{}
			for p, f := range fsys {
				before[p] = append([]byte(nil), f.Data...)
			}
			edgeSwapSkills(t, fsys)
			wt := t.TempDir()

			installSkills(wt, &Job{Slug: "edge-skills"})

			for p, orig := range before {
				got, err := os.ReadFile(filepath.Join(wt, ".claude", "skills", filepath.FromSlash(p)))
				if err != nil {
					t.Errorf("%s not materialized: %v", p, err)
					continue
				}
				want := strings.ReplaceAll(string(orig), "{{operator}}", name)
				if string(got) != want {
					t.Errorf("%s = %q, want %q", p, got, want)
				}
				if !reflect.DeepEqual(fsys[p].Data, orig) {
					t.Errorf("embedded %s was mutated to %q", p, fsys[p].Data)
				}
			}
		})
	}
}

// Re-running installSkills over an already-materialized tree does not expand
// the name a second time (a name containing the placeholder would grow).
func TestEdgeInstallSkillsRerunDoesNotReexpand(t *testing.T) {
	edgeHome(t)
	edgeKnobsClear(t)
	name := "A{{operator}}B"
	t.Setenv("KRAKEN_OPERATOR", name)
	edgeSwapSkills(t, edgeSkillsFS())
	wt := t.TempDir()
	installSkills(wt, &Job{Slug: "edge-skills-rerun"})
	installSkills(wt, &Job{Slug: "edge-skills-rerun"})
	got, err := os.ReadFile(filepath.Join(wt, ".claude", "skills", "handoff", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != name {
		t.Fatalf("handoff/SKILL.md after two installs = %q, want exactly %q", got, name)
	}
}

// ---------------------------------------------------------------------------
// Slice P — rootNotes (as amended: (names, ok), kraken's AGENTS.md excluded)
// ---------------------------------------------------------------------------

// Amended: "ok == false when wt == "", when it is not a checkout, or when git
// status fails".
func TestEdgeRootNotesNotACheckout(t *testing.T) {
	edgeHome(t)
	if got, ok := rootNotes(""); got != nil || ok {
		t.Errorf(`rootNotes("") = (%#v, %v), want (nil, false)`, got, ok)
	}
	plain := t.TempDir()
	edgeWrite(t, filepath.Join(plain, "NOTES.md"), "x")
	if got, ok := rootNotes(plain); got != nil || ok {
		t.Errorf("rootNotes(non-repo with a .md) = (%#v, %v), want (nil, false)", got, ok)
	}
	if got, ok := rootNotes(filepath.Join(t.TempDir(), "missing")); got != nil || ok {
		t.Errorf("rootNotes(missing dir) = (%#v, %v), want (nil, false)", got, ok)
	}
}

// The repos-dir-moved case: a linked worktree whose `.git` FILE points at a
// gitdir that no longer exists. `git status` fails there, and that must read
// as "could not tell" (ok false), never as "no notes" (ok true, empty).
func TestEdgeRootNotesBrokenGitdirIsNotOK(t *testing.T) {
	edgeHome(t)
	wt := t.TempDir()
	edgeWrite(t, filepath.Join(wt, ".git"), "gitdir: "+filepath.Join(t.TempDir(), "gone", ".git", "worktrees", "x")+"\n")
	edgeWrite(t, filepath.Join(wt, "NOTES.md"), "n")
	if got, ok := rootNotes(wt); ok || got != nil {
		t.Fatalf("rootNotes(broken gitdir) = (%#v, %v), want (nil, false)", got, ok)
	}
}

// edgeRepo makes a git checkout whose one commit holds a handful of root and
// nested .md files for the porcelain-shape cases to modify, move and delete.
func edgeRepo(t *testing.T) string {
	t.Helper()
	wt := t.TempDir()
	edgeGit(t, wt, "init", "-q")
	for _, f := range []string{"committed.md", "modified.md", "gone.md", "old.md", "tosub.md", "docs/tracked.md", "docs/up.md"} {
		edgeWrite(t, filepath.Join(wt, filepath.FromSlash(f)), "orig "+f+"\n")
	}
	edgeGit(t, wt, "add", "-A")
	edgeGit(t, wt, "commit", "-q", "-m", "init")
	return wt
}

// Contract: the bare names of every uncommitted root-level *.md, a rename by
// its new name, sorted; committed-and-clean files are not returned; ok true.
func TestEdgeRootNotesPorcelainShapes(t *testing.T) {
	edgeHome(t)
	wt := edgeRepo(t)
	edgeWrite(t, filepath.Join(wt, "NOTES.md"), "n")
	edgeWrite(t, filepath.Join(wt, "my notes.md"), "s")
	edgeWrite(t, filepath.Join(wt, "modified.md"), "changed\n")
	edgeWrite(t, filepath.Join(wt, "added.md"), "a")
	edgeGit(t, wt, "add", "added.md")
	edgeGit(t, wt, "mv", "old.md", "new.md")
	if err := os.MkdirAll(filepath.Join(wt, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	edgeGit(t, wt, "mv", "tosub.md", "sub/tosub.md")
	edgeGit(t, wt, "mv", "docs/up.md", "moved-up.md")
	if err := os.Remove(filepath.Join(wt, "gone.md")); err != nil {
		t.Fatal(err)
	}
	edgeWrite(t, filepath.Join(wt, "notes.txt"), "t")
	edgeWrite(t, filepath.Join(wt, "UPPER.MD"), "u")
	edgeWrite(t, filepath.Join(wt, "x.md.bak"), "b")
	edgeWrite(t, filepath.Join(wt, "drafts", "deep.md"), "d")
	edgeWrite(t, filepath.Join(wt, "docs", "tracked.md"), "changed nested\n")

	got, ok := rootNotes(wt)
	want := []string{"NOTES.md", "added.md", "modified.md", "moved-up.md", "my notes.md", "new.md"}
	if !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("rootNotes = (%#v, %v), want (%#v, true)", got, ok, want)
	}
}

// A clean checkout: ok true and no names — distinguishable from failure.
func TestEdgeRootNotesCleanCheckout(t *testing.T) {
	edgeHome(t)
	wt := edgeRepo(t)
	if got, ok := rootNotes(wt); !ok || len(got) != 0 {
		t.Fatalf("clean checkout: rootNotes = (%#v, %v), want (none, true)", got, ok)
	}
}

// Amended: "a root file kraken itself wrote (the codex AGENTS.md whose first
// bytes are kraken's generated header) is never returned" — but an untracked
// AGENTS.md the operator wrote by hand IS a root note, and so is one that
// merely MENTIONS the header later in the file.
func TestEdgeRootNotesSkipsOnlyKrakensOwnAgentsMD(t *testing.T) {
	edgeHome(t)
	wt := edgeRepo(t)
	edgeWrite(t, filepath.Join(wt, "AGENTS.md"), codexAgentsMD("the brief"))
	edgeWrite(t, filepath.Join(wt, "NOTES.md"), "n")
	got, ok := rootNotes(wt)
	if !ok || !reflect.DeepEqual(got, []string{"NOTES.md"}) {
		t.Errorf("with kraken's AGENTS.md: rootNotes = (%#v, %v), want ([NOTES.md], true)", got, ok)
	}

	wt2 := edgeRepo(t)
	edgeWrite(t, filepath.Join(wt2, "AGENTS.md"), "# my own agent instructions\n")
	if got, ok := rootNotes(wt2); !ok || !reflect.DeepEqual(got, []string{"AGENTS.md"}) {
		t.Errorf("hand-written AGENTS.md: rootNotes = (%#v, %v), want ([AGENTS.md], true)", got, ok)
	}

	wt3 := edgeRepo(t)
	edgeWrite(t, filepath.Join(wt3, "AGENTS.md"), "# mine\n\n"+codexAgentsMD("quoted later"))
	if got, ok := rootNotes(wt3); !ok || !reflect.DeepEqual(got, []string{"AGENTS.md"}) {
		t.Errorf("header not at the first bytes: rootNotes = (%#v, %v), want ([AGENTS.md], true)", got, ok)
	}
}

// ---------------------------------------------------------------------------
// Slice P — syncCellArtifacts / restore / teardown (as amended: root/ rows)
// ---------------------------------------------------------------------------

// edgeCell builds a store job with a resolvable repo under KRAKEN_REPOS_DIR,
// a real git worktree at CellWorktree, and its artifacts dir.
func edgeCell(t *testing.T, st *store.Store, slug string) (*Job, string) {
	t.Helper()
	edgeKnobsClear(t)
	edgeReposDir(t, "edgerepo")
	row := mustCreateJob(t, st, store.Job{Slug: slug, Repo: "edgerepo", State: "working"})
	j := &Job{Slug: row.Slug, ID: row.ID, Repo: "edgerepo", State: "working"}
	wt := CellWorktree(j, "edgerepo")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	edgeGit(t, wt, "init", "-q")
	edgeWrite(t, filepath.Join(wt, "README.md"), "committed readme\n")
	edgeGit(t, wt, "add", "-A")
	edgeGit(t, wt, "commit", "-q", "-m", "init")
	if err := os.MkdirAll(CellArtifacts(j), 0o755); err != nil {
		t.Fatal(err)
	}
	return j, wt
}

func edgeRows(t *testing.T, id int64) map[string][]string {
	t.Helper()
	rows, err := ActiveStore().ListArtifacts(id)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for _, r := range rows {
		out[r.Path] = append(out[r.Path], string(r.Bytes))
	}
	return out
}

// Amended: "root notes are stored under the path root/<bare name>, never
// under the bare name; an artifacts/root/<name> file beats a root note of the
// same name (one row, the artifacts bytes)". A bare-name collision with a
// TOP-LEVEL artifact is no longer a collision at all: two rows.
func TestEdgeSyncRootNotesUnderRootPrefix(t *testing.T) {
	st := useTestStore(t)
	j, wt := edgeCell(t, st, "edge-sync-root")
	edgeWrite(t, filepath.Join(wt, "NOTES.md"), "root notes")
	edgeWrite(t, filepath.Join(wt, "SHADOW.md"), "root shadow")
	edgeWrite(t, filepath.Join(wt, "nested", "deep.md"), "nested")
	edgeWrite(t, filepath.Join(CellArtifacts(j), "NOTES.md"), "artifact notes")
	edgeWrite(t, filepath.Join(CellArtifacts(j), "root", "SHADOW.md"), "artifact shadow")

	if err := syncCellArtifacts(st, j.ID, j); err != nil {
		t.Fatalf("sync: %v", err)
	}
	got := edgeRows(t, j.ID)
	want := map[string][]string{
		"NOTES.md":       {"artifact notes"},
		"root/NOTES.md":  {"root notes"},
		"root/SHADOW.md": {"artifact shadow"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
}

// Amended: "when rootNotes reports ok == false, existing root/* rows are KEPT
// (treated as unseen), never deleted" — on a mid-run sync, not only at
// teardown. The worktree's gitdir is broken AFTER the first capture.
func TestEdgeSyncKeepsRootRowsWhenGitFails(t *testing.T) {
	st := useTestStore(t)
	j, wt := edgeCell(t, st, "edge-sync-keep")
	edgeWrite(t, filepath.Join(wt, "NOTES.md"), "captured once")
	edgeWrite(t, filepath.Join(CellArtifacts(j), "a.md"), "a")
	if err := syncCellArtifacts(st, j.ID, j); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if got := edgeRows(t, j.ID)["root/NOTES.md"]; !reflect.DeepEqual(got, []string{"captured once"}) {
		t.Fatalf("fixture: first sync rows = %v", edgeRows(t, j.ID))
	}
	// break git in the worktree: .git becomes a file naming a missing gitdir
	if err := os.RemoveAll(filepath.Join(wt, ".git")); err != nil {
		t.Fatal(err)
	}
	edgeWrite(t, filepath.Join(wt, ".git"), "gitdir: /nonexistent/edge/gitdir\n")
	if err := syncCellArtifacts(st, j.ID, j); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	got := edgeRows(t, j.ID)
	if !reflect.DeepEqual(got["root/NOTES.md"], []string{"captured once"}) {
		t.Errorf("root/NOTES.md after a git failure = %v, want the captured row kept", got["root/NOTES.md"])
	}
	if !reflect.DeepEqual(got["a.md"], []string{"a"}) {
		t.Errorf("a.md row = %v", got["a.md"])
	}
}

// With git healthy, a root note that has since been committed (ok true, not
// listed) IS deleted — the keep rule is only for "could not tell".
func TestEdgeSyncDropsRootRowWhenNoteIsCommitted(t *testing.T) {
	st := useTestStore(t)
	j, wt := edgeCell(t, st, "edge-sync-commit")
	edgeWrite(t, filepath.Join(wt, "NOTES.md"), "draft")
	edgeWrite(t, filepath.Join(CellArtifacts(j), "a.md"), "a")
	if err := syncCellArtifacts(st, j.ID, j); err != nil {
		t.Fatal(err)
	}
	edgeGit(t, wt, "add", "NOTES.md")
	edgeGit(t, wt, "commit", "-q", "-m", "notes")
	if err := syncCellArtifacts(st, j.ID, j); err != nil {
		t.Fatal(err)
	}
	if _, ok := edgeRows(t, j.ID)["root/NOTES.md"]; ok {
		t.Errorf("root/NOTES.md survived its note being committed: %v", edgeRows(t, j.ID))
	}
}

// Amended: "rows whose path starts with root/ are restored to
// <worktree>/<bare name>, not into artifacts/; every other row restores as
// today" — including a nested artifact and a row named `root.md` (not the
// prefix) and one under `rootx/` (not the prefix either).
func TestEdgeRestoreWritesRootRowsToTheWorktree(t *testing.T) {
	st := useTestStore(t)
	j, wt := edgeCell(t, st, "edge-restore")
	if err := os.RemoveAll(CellArtifacts(j)); err != nil {
		t.Fatal(err)
	}
	if err := st.SyncArtifacts(j.ID, []store.ArtifactFile{
		{Path: "root/NOTES.md", Bytes: []byte("root bytes"), Mtime: 1},
		{Path: "a.md", Bytes: []byte("a"), Mtime: 1},
		{Path: "dir/b.md", Bytes: []byte("b"), Mtime: 1},
		{Path: "root.md", Bytes: []byte("not a root note"), Mtime: 1},
		{Path: "rootx/c.md", Bytes: []byte("c"), Mtime: 1},
	}, nil); err != nil {
		t.Fatal(err)
	}
	if err := restoreCellArtifacts(st, j.ID, j); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(wt, "NOTES.md")); err != nil || string(b) != "root bytes" {
		t.Errorf("worktree NOTES.md = (%q, %v), want the root row's bytes", b, err)
	}
	if _, err := os.Stat(filepath.Join(CellArtifacts(j), "root", "NOTES.md")); err == nil {
		t.Errorf("a root/ row was restored into artifacts/root/")
	}
	for rel, want := range map[string]string{"a.md": "a", "dir/b.md": "b", "root.md": "not a root note", "rootx/c.md": "c"} {
		if b, err := os.ReadFile(filepath.Join(CellArtifacts(j), filepath.FromSlash(rel))); err != nil || string(b) != want {
			t.Errorf("artifacts/%s = (%q, %v), want %q", rel, b, err, want)
		}
	}
	// the round trip: the restored note is a root note again, not shadowed
	edgeWrite(t, filepath.Join(wt, "NOTES.md"), "edited after reactivation")
	if err := syncCellArtifacts(st, j.ID, j); err != nil {
		t.Fatal(err)
	}
	if got := edgeRows(t, j.ID)["root/NOTES.md"]; !reflect.DeepEqual(got, []string{"edited after reactivation"}) {
		t.Errorf("after reactivation + edit, root/NOTES.md = %v, want the new edit", got)
	}
}

// A root/ row that tries to escape (`root/../x.md`, `root/` itself) is never
// written outside the worktree.
func TestEdgeRestoreRootRowCannotEscape(t *testing.T) {
	st := useTestStore(t)
	j, wt := edgeCell(t, st, "edge-restore-escape")
	if err := os.RemoveAll(CellArtifacts(j)); err != nil {
		t.Fatal(err)
	}
	if err := st.SyncArtifacts(j.ID, []store.ArtifactFile{
		{Path: "root/../escaped.md", Bytes: []byte("x"), Mtime: 1},
		{Path: "root/../../escaped2.md", Bytes: []byte("x"), Mtime: 1},
	}, nil); err != nil {
		t.Fatal(err)
	}
	_ = restoreCellArtifacts(st, j.ID, j)
	for _, p := range []string{
		filepath.Join(filepath.Dir(wt), "escaped.md"),
		filepath.Join(filepath.Dir(filepath.Dir(wt)), "escaped2.md"),
		filepath.Join(wt, "..", "escaped.md"),
	} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("restore wrote outside the worktree: %s", p)
		}
	}
}

// Amended: syncArtifactsAtTeardown "returns the store sync's error; at
// teardown an artifact file that cannot be read is a FAILURE (never a skip),
// and a root note whose bytes cannot be read likewise".
func TestEdgeTeardownSyncUnreadableIsAnError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads 0000 files")
	}
	t.Run("artifact", func(t *testing.T) {
		st := useTestStore(t)
		j, _ := edgeCell(t, st, "edge-td-art")
		p := filepath.Join(CellArtifacts(j), "locked.md")
		edgeWrite(t, p, "secret work")
		if err := os.Chmod(p, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(p, 0o644) })
		if err := syncArtifactsAtTeardown(j); err == nil {
			t.Fatal("syncArtifactsAtTeardown = nil with an unreadable artifact, want an error")
		}
	})
	t.Run("root note", func(t *testing.T) {
		st := useTestStore(t)
		j, wt := edgeCell(t, st, "edge-td-root")
		p := filepath.Join(wt, "NOTES.md")
		edgeWrite(t, p, "uncommitted")
		if err := os.Chmod(p, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(p, 0o644) })
		if err := syncArtifactsAtTeardown(j); err == nil {
			t.Fatal("syncArtifactsAtTeardown = nil with an unreadable root note, want an error")
		}
	})
	t.Run("healthy", func(t *testing.T) {
		st := useTestStore(t)
		j, wt := edgeCell(t, st, "edge-td-ok")
		edgeWrite(t, filepath.Join(wt, "NOTES.md"), "n")
		edgeWrite(t, filepath.Join(CellArtifacts(j), "a.md"), "a")
		if err := syncArtifactsAtTeardown(j); err != nil {
			t.Fatalf("syncArtifactsAtTeardown on a healthy cell = %v, want nil", err)
		}
	})
}

// Amended: "when the teardown sync fails, teardown stops BEFORE the worktree
// and cell dir are removed and the job stays in its current state; the
// returned error names the artifact sync ("artifact sync failed … not tearing
// down"), never "preservation"" — through both board verbs.
func TestEdgeKillDoneStopWhenTheTeardownSyncFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads 0000 files")
	}
	for _, verb := range []string{"kill", "done"} {
		t.Run(verb, func(t *testing.T) {
			st := useTestStore(t)
			j, wt := edgeCell(t, st, "edge-td-"+verb+"-fail")
			p := filepath.Join(CellArtifacts(j), "locked.md")
			edgeWrite(t, p, "only copy")
			if err := os.Chmod(p, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(p, 0o644) })
			loaded := LoadJobBySlug(j.Slug)
			if loaded == nil {
				t.Fatal("fixture did not load")
			}
			var err error
			if verb == "kill" {
				_, err = KillJob(loaded)
			} else {
				_, err = DoneJob(loaded, "edge")
			}
			if err == nil {
				t.Fatalf("%s succeeded although the final artifact sync could not read a file", verb)
			}
			msg := strings.ToLower(err.Error())
			if !strings.Contains(msg, "artifact sync failed") || !strings.Contains(msg, "not tearing down") {
				t.Errorf("%s error = %q, want it to say \"artifact sync failed … not tearing down\"", verb, err)
			}
			if strings.Contains(msg, "preserv") {
				t.Errorf("%s error mentions preservation: %q", verb, err)
			}
			if _, serr := os.Stat(p); serr != nil {
				t.Errorf("%s removed the cell's artifacts after a failed sync: %v", verb, serr)
			}
			if _, serr := os.Stat(filepath.Join(wt, "README.md")); serr != nil {
				t.Errorf("%s removed the worktree after a failed sync: %v", verb, serr)
			}
			if after := LoadJobBySlug(j.Slug); after == nil || after.State != "working" {
				t.Errorf("%s left the job in state %v, want it still working", verb, after)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Slice P — no store, no files
// ---------------------------------------------------------------------------

// Contract: "LoadJobs returns nil when ActiveStore() == nil; reads no
// directory" / "LoadJobBySlug returns nil when ActiveStore() == nil; reads no
// file" — even with a perfectly valid job file sitting where the retired
// file-mode loader used to look.
func TestEdgeLoadJobsWithoutStoreIgnoresJobFiles(t *testing.T) {
	home := edgeHome(t)
	edgeKnobsClear(t)
	UseStore(nil)
	t.Cleanup(func() { UseStore(nil) })
	legacy := filepath.Join(home, "Documents", "pluto")
	edgeWrite(t, filepath.Join(legacy, "edge-file-job.job.md"), "---\nrepo: x\n---\nbody\n")
	t.Setenv("KRAKEN_PLUTO_DIR", legacy)

	if got := LoadJobs(); got != nil {
		t.Errorf("LoadJobs() with no store = %d jobs, want nil", len(got))
	}
	if got := LoadJobBySlug("edge-file-job"); got != nil {
		t.Errorf("LoadJobBySlug with no store = %+v, want nil", got)
	}
}

// Contract: KillJob/DoneJob "never return an error mentioning preservation;
// create nothing outside ~/.kraken (fresh HOME: no ~/Documents afterwards)".
// A done-shaped teardown of a job that has a cell artifacts dir but no live
// session: whatever else fails, nothing appears outside ~/.kraken.
func TestEdgeTeardownCreatesNothingOutsideKraken(t *testing.T) {
	for _, verb := range []string{"kill", "done"} {
		t.Run(verb, func(t *testing.T) {
			st := useTestStore(t)
			edgeKnobsClear(t)
			home := os.Getenv("HOME")
			row := mustCreateJob(t, st, store.Job{Slug: "edge-td-" + verb, Repo: "edge-no-such-repo-zz", State: "working"})
			j := LoadJobBySlug(row.Slug)
			if j == nil {
				t.Fatal("fixture did not load")
			}
			edgeWrite(t, filepath.Join(CellArtifacts(j), "00-overview.md"), "overview")
			before := edgeOutsideKraken(t, home)

			var err error
			if verb == "kill" {
				_, err = KillJob(j)
			} else {
				_, err = DoneJob(j, "edge outcome")
			}
			if err != nil && strings.Contains(strings.ToLower(err.Error()), "preserv") {
				t.Errorf("%s error mentions preservation: %v", verb, err)
			}
			if _, serr := os.Stat(filepath.Join(home, "Documents")); serr == nil {
				t.Errorf("%s created ~/Documents", verb)
			}
			if after := edgeOutsideKraken(t, home); !reflect.DeepEqual(before, after) {
				t.Errorf("%s touched paths outside ~/.kraken: before %v, after %v", verb, before, after)
			}
		})
	}
}

// Contract: Archive/Unarchive/DeleteArchived/Rename "touch no path outside
// ~/.kraken; RenameResult.Skipped is always empty and Notes carry no path" —
// including when a stale KRAKEN_PLUTO_DIR still names a populated dir.
func TestEdgeArchiveRenameTouchNothingOutsideKraken(t *testing.T) {
	st := useTestStore(t)
	edgeKnobsClear(t)
	home := os.Getenv("HOME")
	legacy := filepath.Join(home, "legacy-pluto")
	t.Setenv("KRAKEN_PLUTO_DIR", legacy)

	row := mustCreateJob(t, st, store.Job{Slug: "edge-arch", Repo: "x", State: "done"})
	stale := filepath.Join(legacy, fmt.Sprintf("edge-arch-%d.report.md", row.ID))
	edgeWrite(t, stale, "legacy flat copy")
	before := edgeOutsideKraken(t, home)

	j := LoadJobBySlug("edge-arch")
	res, err := RenameJob(j, "edge-arch-renamed")
	if err != nil {
		t.Fatalf("RenameJob: %v", err)
	}
	if len(res.Skipped) != 0 {
		t.Errorf("RenameResult.Skipped = %v, want empty", res.Skipped)
	}
	for _, n := range res.Notes {
		if strings.Contains(n, home) || strings.Contains(n, "Documents") {
			t.Errorf("rename note carries a path: %q", n)
		}
	}
	j = LoadJobBySlug("edge-arch-renamed")
	if j == nil {
		t.Fatal("renamed job not found")
	}
	if _, err := ArchiveJob(j, map[string]bool{}); err != nil {
		t.Fatalf("ArchiveJob: %v", err)
	}
	archived := LoadArchivedJobs()
	if len(archived) != 1 {
		t.Fatalf("%d archived jobs, want 1", len(archived))
	}
	if err := UnarchiveJob(archived[0]); err != nil {
		t.Fatalf("UnarchiveJob: %v", err)
	}
	j = LoadJobBySlug("edge-arch-renamed")
	if _, err := ArchiveJob(j, map[string]bool{}); err != nil {
		t.Fatalf("second ArchiveJob: %v", err)
	}
	archived = LoadArchivedJobs()
	if len(archived) != 1 {
		t.Fatalf("%d archived jobs before delete, want 1", len(archived))
	}
	if err := DeleteArchivedJob(archived[0]); err != nil {
		t.Fatalf("DeleteArchivedJob: %v", err)
	}

	if after := edgeOutsideKraken(t, home); !reflect.DeepEqual(before, after) {
		t.Errorf("archive/rename touched paths outside ~/.kraken: before %v, after %v", before, after)
	}
	if b, err := os.ReadFile(stale); err != nil || string(b) != "legacy flat copy" {
		t.Errorf("the legacy flat copy was moved or changed: (%q, %v)", b, err)
	}
	if _, err := os.Stat(filepath.Join(home, "Documents")); err == nil {
		t.Errorf("~/Documents exists after archive/rename")
	}
}

// ---------------------------------------------------------------------------
// Review-round-1 amendments — branchPrefixErr, SubmitJob, zeusBrief
// ---------------------------------------------------------------------------

// Amended: "branchPrefixErr: nil when branchPrefix()+"x" passes git
// check-ref-format --branch; otherwise an error naming KRAKEN_BRANCH_PREFIX
// and the value".
func TestEdgeBranchPrefixErrBoundaries(t *testing.T) {
	edgeHome(t)
	edgeKnobsClear(t)
	if err := branchPrefixErr(); err != nil {
		t.Fatalf("unset (default kraken/): branchPrefixErr() = %v, want nil", err)
	}
	valid := []string{"carl/", "carl", "  carl/  ", "team/sub", "ada-", "feat.x/", "a_b/", "x1/y2/"}
	for _, v := range valid {
		t.Setenv("KRAKEN_BRANCH_PREFIX", v)
		if err := branchPrefixErr(); err != nil {
			t.Errorf("KRAKEN_BRANCH_PREFIX=%q: branchPrefixErr() = %v, want nil", v, err)
		}
	}
	invalid := []string{"my team", "a//", "/", "-x", "a..b", "~x", "a.lock", "x:y", "q?", "s*", "b[", "c\\d", "dot/.hidden"}
	for _, v := range invalid {
		t.Setenv("KRAKEN_BRANCH_PREFIX", v)
		err := branchPrefixErr()
		if err == nil {
			t.Errorf("KRAKEN_BRANCH_PREFIX=%q: branchPrefixErr() = nil, want a refusal (branch %q is not a valid ref)", v, branchPrefix()+"x")
			continue
		}
		if !strings.Contains(err.Error(), "KRAKEN_BRANCH_PREFIX") {
			t.Errorf("KRAKEN_BRANCH_PREFIX=%q: error %q does not name the knob", v, err)
		}
		if !strings.Contains(err.Error(), strings.TrimSpace(v)) {
			t.Errorf("KRAKEN_BRANCH_PREFIX=%q: error %q does not name the value", v, err)
		}
	}
}

// Amended: "SubmitJob refuses with that error before any worktree exists" —
// the start seam is never reached and no cell dir appears.
func TestEdgeSubmitRefusesAnInvalidBranchPrefix(t *testing.T) {
	home := edgeHome(t)
	edgeKnobsClear(t)
	if err := os.MkdirAll(filepath.Join(home, "Developer", "kraken", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KRAKEN_BRANCH_PREFIX", "bad prefix")
	calls := 0
	prev := submitStartCell
	submitStartCell = func(*Job, string) error { calls++; return nil }
	t.Cleanup(func() { submitStartCell = prev })

	j := &Job{Slug: "edge-badprefix", ID: 77, Repo: "kraken", Body: "# Spec\nreal content\n"}
	err := SubmitJob(j)
	if err == nil {
		t.Fatal("SubmitJob = nil with an invalid KRAKEN_BRANCH_PREFIX, want a refusal")
	}
	if !strings.Contains(err.Error(), "KRAKEN_BRANCH_PREFIX") {
		t.Errorf("refusal %q does not name KRAKEN_BRANCH_PREFIX", err)
	}
	if calls != 0 {
		t.Errorf("the start seam ran %d time(s) despite the refusal", calls)
	}
	if _, serr := os.Stat(filepath.Join(home, ".kraken", "cells", cellName(j))); serr == nil {
		t.Errorf("a cell dir exists after the refusal")
	}
	if j.State != "" {
		t.Errorf("job state = %q after a refusal, want draft", j.State)
	}
}

// Amended: zeusBrief "states, once, that the literal {{operator}} in any skill
// file means Operator()". Once: exactly one occurrence of the literal, and the
// resolved name rides near it.
func TestEdgeZeusBriefNamesThePlaceholderOnce(t *testing.T) {
	edgeHome(t)
	edgeKnobsClear(t)
	t.Setenv("KRAKEN_OPERATOR", "Ada")
	b := zeusBrief(&Job{Slug: "edge-ph", ID: 5, Repo: "x"})
	if n := strings.Count(b, "{{operator}}"); n != 1 {
		t.Fatalf("zeusBrief carries {{operator}} %d times, want exactly 1", n)
	}
	i := strings.Index(b, "{{operator}}")
	lo, hi := i-200, i+200
	if lo < 0 {
		lo = 0
	}
	if hi > len(b) {
		hi = len(b)
	}
	if !strings.Contains(b[lo:hi], "Ada") {
		t.Errorf("the {{operator}} statement does not name Ada nearby: …%s…", b[lo:hi])
	}
	// under the fallback the statement still reads, naming "the operator"
	edgeUnsetEnv(t, "KRAKEN_OPERATOR", "USER")
	b = zeusBrief(&Job{Slug: "edge-ph", ID: 5, Repo: "x"})
	if n := strings.Count(b, "{{operator}}"); n != 1 {
		t.Errorf("fallback zeusBrief carries {{operator}} %d times, want exactly 1", n)
	}
}
