package main

import (
	"strings"
	"testing"
)

const sampleJob = `---
job: yelp-dedupe          # slug; this file is yelp-dedupe.job.md
type: build               # build | explore | question
repo: monolith            # substring-matched against existing repos (alphabetic tiebreak);
                          # no match → job errors, surfaced in the viewer
ready: true               # flip to true to queue
prereqs:
  - job: dedupe-strategy-q
    gated: true
  - job: schema-migration
solo: false               # true → zeus executes alone, no subagent crew
auto-approve: false       # true → skip the plan-approval gate (default: plan is gated)
success: |
  dupes collapse in the dashboard
  unit tests pass
---

# Spec
Dedupe yelp listings.

## Mid-run log
<!-- appended by kraken; do not hand-edit -->
`

func TestParseJobFull(t *testing.T) {
	j := parseJob("yelp-dedupe.job.md", []byte(sampleJob))
	if j.Err != "" {
		t.Fatalf("unexpected err: %s", j.Err)
	}
	if j.Slug != "yelp-dedupe" || j.Type != "build" || j.Repo != "monolith" {
		t.Fatalf("identity: %+v", j)
	}
	if !j.Ready || j.Solo || j.AutoApprove {
		t.Fatalf("flags: ready=%v solo=%v auto=%v", j.Ready, j.Solo, j.AutoApprove)
	}
	want := []Prereq{{Job: "dedupe-strategy-q", Gated: true}, {Job: "schema-migration"}}
	if len(j.Prereqs) != 2 || j.Prereqs[0] != want[0] || j.Prereqs[1] != want[1] {
		t.Fatalf("prereqs: %+v", j.Prereqs)
	}
	if j.Success != "dupes collapse in the dashboard\nunit tests pass" {
		t.Fatalf("success: %q", j.Success)
	}
	if !strings.Contains(j.Body, "# Spec") || strings.Contains(j.Body, "ready:") {
		t.Fatalf("body: %q", j.Body)
	}
}

func TestParseJobTemplateDefaults(t *testing.T) {
	// the pluto template as expanded: empty prereqs with inline comment lines
	src := `---
job: name
type: build
repo: monolith            # substring-matched
ready: false              # flip to true to queue
prereqs: []               # - job: other-slug
                          #   gated: true
solo: false
auto-approve: false
success: |
  what "done" looks like
---

# Spec
`
	j := parseJob("name.job.md", []byte(src))
	if j.Err != "" {
		t.Fatalf("unexpected err: %s", j.Err)
	}
	if len(j.Prereqs) != 0 {
		t.Fatalf("template prereqs should be empty, got %+v", j.Prereqs)
	}
	if j.Ready {
		t.Fatal("template must not be ready")
	}
	if j.Success != `what "done" looks like` {
		t.Fatalf("success: %q", j.Success)
	}
}

func TestParseJobStrict(t *testing.T) {
	// every construct the parser can't fully understand must set Err — an
	// erred job never runs and the error is surfaced (board now, inbox later)
	cases := map[string]string{
		"no frontmatter":     "just some text\n",
		"unterminated":       "---\njob: x\n",
		"bad type":           "---\ntype: banana\n---\n",
		"colonless line":     "---\nnonsense\n---\n",
		"unknown key":        "---\nraedy: true\n---\n",
		"duplicate key":      "---\nrepo: a\nrepo: b\n---\n",
		"bad bool":           "---\nready: yes\n---\n",
		"bad state":          "---\nstate: bananas\n---\n",
		"stray indented":     "---\nrepo: a\n  orphan: true\n---\n",
		"junk under prereqs": "---\nprereqs:\n  not an item\n---\n",
		"prereq missing job": "---\nprereqs:\n  - gated: true\n---\n",
		"bad prereqs scalar": "---\nprereqs: whenever\n---\n",
		"bad gated bool":     "---\nprereqs:\n  - job: dep\n    gated: yep\n---\n",
	}
	for name, src := range cases {
		j := parseJob("f.job.md", []byte(src))
		if j == nil {
			t.Fatalf("%s: nil job (must never crash)", name)
		}
		if j.Err == "" {
			t.Errorf("%s: expected Err, got clean parse: %+v", name, j)
		}
		if got := displayState(j, nil); got != "error" {
			t.Errorf("%s: erred job must display error, got %q", name, got)
		}
	}
	// errors accumulate rather than overwriting
	j := parseJob("f.job.md", []byte("---\nready: yes\nraedy: true\n---\n"))
	if !strings.Contains(j.Err, ";") {
		t.Errorf("errors should accumulate: %q", j.Err)
	}
	// empty file: no frontmatter, still a valid (erred) job value
	if j := parseJob("f.job.md", []byte("")); j.Err == "" || j.Slug != "f" {
		t.Errorf("empty file: err=%q slug=%q", j.Err, j.Slug)
	}
	// empty bool value reads as absent, not an error
	if j := parseJob("f.job.md", []byte("---\nready:\n---\n")); j.Err != "" || j.Ready {
		t.Errorf("empty bool value: err=%q ready=%v", j.Err, j.Ready)
	}
}

func TestParseJobStateAndOutcome(t *testing.T) {
	src := "---\njob: x\nready: true\nstate: executing\noutcome: went well\n---\nbody"
	j := parseJob("x.job.md", []byte(src))
	if j.State != "executing" || j.Outcome != "went well" {
		t.Fatalf("state/outcome: %q %q", j.State, j.Outcome)
	}
	if !j.started() {
		t.Fatal("executing should count as started")
	}
	j.State = "failed"
	if j.started() {
		t.Fatal("failed returns the job to carl — not started")
	}
}

func TestStripComment(t *testing.T) {
	cases := [][2]string{
		{"monolith            # substring-matched", "monolith"},
		{"value#notacomment", "value#notacomment"},
		{"fix bug #123 done", "fix bug #123 done"}, // issue refs are values, not comments
		{"fix bug #", "fix bug"},                   // trailing bare hash is a comment
		{"  spaced  ", "spaced"},
		{"# all comment", ""},
		{"[]", "[]"},
	}
	for _, c := range cases {
		if got := stripComment(c[0]); got != c[1] {
			t.Errorf("stripComment(%q) = %q, want %q", c[0], got, c[1])
		}
	}
}

func TestParseJobIndentedFenceInBlockScalar(t *testing.T) {
	// an indented --- inside a success block scalar is content, not the
	// closing fence: fields after it must still parse
	src := "---\njob: x\nsuccess: |\n  part one\n  ---\n  part two\nrepo: monolith\nready: true\n---\n\n# Spec\n"
	j := parseJob("x.job.md", []byte(src))
	if j.Repo != "monolith" || !j.Ready {
		t.Fatalf("fields after indented --- dropped: repo=%q ready=%v", j.Repo, j.Ready)
	}
	if !strings.Contains(j.Success, "---") || !strings.Contains(j.Success, "part two") {
		t.Fatalf("block scalar truncated: %q", j.Success)
	}
}

func TestParseJobCRLF(t *testing.T) {
	src := strings.ReplaceAll(sampleJob, "\n", "\r\n")
	j := parseJob("yelp-dedupe.job.md", []byte(src))
	if j.Err != "" || !j.Ready || j.Repo != "monolith" {
		t.Fatalf("CRLF parse degraded: err=%q ready=%v repo=%q", j.Err, j.Ready, j.Repo)
	}
	if strings.Contains(j.Success, "\r") {
		t.Fatalf("carriage return baked into success: %q", j.Success)
	}
}

func TestParsePrereqsTabIndent(t *testing.T) {
	src := "---\njob: x\nready: true\nprereqs:\n\t- job: dep\n\t  gated: true\n---\n"
	j := parseJob("x.job.md", []byte(src))
	if len(j.Prereqs) != 1 || !j.Prereqs[0].Gated {
		t.Fatalf("tab-indented gated flag lost: %+v", j.Prereqs)
	}
}

func TestPatchStateHealsIndented(t *testing.T) {
	src := "---\njob: x\n state: executing\n---\nbody"
	out, err := patchState(src, "done")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(out, "state:") != 1 {
		t.Fatalf("indented state line not healed:\n%s", out)
	}
	if j := parseJob("x.job.md", []byte(out)); j.State != "done" {
		t.Fatalf("state after heal: %q", j.State)
	}
}

func TestPatchState(t *testing.T) {
	// insert when absent — body untouched byte-for-byte
	out, err := patchState(sampleJob, "executing")
	if err != nil {
		t.Fatal(err)
	}
	j := parseJob("yelp-dedupe.job.md", []byte(out))
	if j.State != "executing" {
		t.Fatalf("state after insert: %q", j.State)
	}
	orig := parseJob("yelp-dedupe.job.md", []byte(sampleJob))
	if j.Body != orig.Body {
		t.Fatal("patchState changed the body")
	}
	// replace when present
	out2, err := patchState(out, "done")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(out2, "\nstate:") != 1 {
		t.Fatalf("state line duplicated:\n%s", out2)
	}
	if j2 := parseJob("x.job.md", []byte(out2)); j2.State != "done" {
		t.Fatalf("state after replace: %q", j2.State)
	}
	// malformed input errors
	if _, err := patchState("no frontmatter", "x"); err == nil {
		t.Fatal("expected error for missing frontmatter")
	}
}

func TestPatchFieldOutcome(t *testing.T) {
	src := "---\njob: x\nstate: executing\n---\nbody"
	out, err := patchField(src, "outcome", "approach A works")
	if err != nil {
		t.Fatal(err)
	}
	j := parseJob("x.job.md", []byte(out))
	if j.Err != "" || j.Outcome != "approach A works" || j.State != "executing" {
		t.Fatalf("outcome insert: err=%q outcome=%q state=%q", j.Err, j.Outcome, j.State)
	}
	out2, err := patchField(out, "outcome", "revised")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(out2, "outcome:") != 1 {
		t.Fatalf("outcome duplicated:\n%s", out2)
	}
}

func TestOneLine(t *testing.T) {
	if got := oneLine("a\nb\tc\x1b[31m"); got != "a b c[31m" {
		t.Fatalf("oneLine: %q", got)
	}
}

func mkJob(slug, state string, ready bool, prereqs ...Prereq) *Job {
	return &Job{Slug: slug, State: state, Ready: ready, Prereqs: prereqs}
}

func slugMap(jobs ...*Job) map[string]*Job {
	m := map[string]*Job{}
	for _, j := range jobs {
		m[j.Slug] = j
	}
	return m
}

func TestDisplayState(t *testing.T) {
	done := mkJob("dep", "done", true)
	pending := mkJob("pend", "", true)
	cases := []struct {
		j    *Job
		want string
	}{
		{mkJob("a", "", false), "draft"},
		{mkJob("a", "", true), "ready"},
		{mkJob("a", "executing", true), "executing"},
		{mkJob("a", "", true, Prereq{Job: "dep"}), "ready"},
		{mkJob("a", "", true, Prereq{Job: "dep", Gated: true}), "gate-review"},
		{mkJob("a", "executing", true, Prereq{Job: "dep", Gated: true}), "executing"},
		{mkJob("a", "", true, Prereq{Job: "pend"}), "waits:pend"},
		{mkJob("a", "", true, Prereq{Job: "pend", Gated: true}), "gated:pend"},
		{mkJob("a", "", true, Prereq{Job: "missing"}), "waits:missing"},
	}
	bySlug := slugMap(done, pending)
	for _, c := range cases {
		if got := displayState(c.j, bySlug); got != c.want {
			t.Errorf("displayState(%+v) = %q, want %q", c.j, got, c.want)
		}
	}
	broken := mkJob("b", "", true)
	broken.Err = "prereq cycle"
	if got := displayState(broken, bySlug); got != "error" {
		t.Errorf("error state: %q", got)
	}
}

func TestMarkPrereqCycles(t *testing.T) {
	a := mkJob("a", "", true, Prereq{Job: "b"})
	b := mkJob("b", "", true, Prereq{Job: "a"})
	c := mkJob("c", "", true, Prereq{Job: "a"})
	d := mkJob("d", "", true)
	markPrereqCycles([]*Job{a, b, c, d})
	if a.Err != "prereq cycle" || b.Err != "prereq cycle" {
		t.Fatalf("cycle not marked: a=%q b=%q", a.Err, b.Err)
	}
	if c.Err != "" || d.Err != "" {
		t.Fatalf("non-cycle members marked: c=%q d=%q", c.Err, d.Err)
	}
	// self-loop
	s := mkJob("s", "", true, Prereq{Job: "s"})
	markPrereqCycles([]*Job{s})
	if s.Err != "prereq cycle" {
		t.Fatalf("self-loop not marked: %q", s.Err)
	}
}

func TestPickRepo(t *testing.T) {
	candidates := []string{"monolith", "monolith-voicetuning", "kraken", "another-kraken", "pluto", "Landing"}
	cases := [][2]string{
		{"monolith", "monolith"},          // exact match beats substring hits
		{"kraken", "kraken"},              // exact beats alphabetically-earlier "another-kraken"
		{"mono", "monolith"},              // substring, alphabetic first
		{"voice", "monolith-voicetuning"}, // substring anywhere
		{"landing", "Landing"},            // case-insensitive exact
		{"nomatch", ""},
	}
	for _, c := range cases {
		if got := pickRepo(c[0], candidates); got != c[1] {
			t.Errorf("pickRepo(%q) = %q, want %q", c[0], got, c[1])
		}
	}
}

func TestJobBySlug(t *testing.T) {
	jobs := []*Job{mkJob("a", "", true), mkJob("b-c", "", true)}
	if jobBySlug(jobs, "b-c") == nil || jobBySlug(jobs, "b-c.job.md") == nil {
		t.Fatal("slug lookup (with and without suffix) failed")
	}
	if jobBySlug(jobs, "nope") != nil {
		t.Fatal("phantom job")
	}
}

func TestZeusBrief(t *testing.T) {
	j := &Job{Slug: "x", Type: "build", Success: "tests pass"}
	brief := zeusBrief(j)
	for _, want := range []string{"zeus", "plan-approval gate", "hephaestus", "athena", "hermes-x",
		"carl/x", "tests pass", "gh pr checks", "kraken watches your status"} {
		if !strings.Contains(brief, want) {
			t.Errorf("build brief missing %q", want)
		}
	}
	j2 := &Job{Slug: "y", Type: "question", Solo: true, AutoApprove: true}
	brief2 := zeusBrief(j2)
	if strings.Contains(brief2, "plan-approval gate") {
		t.Error("auto-approve brief still gates on approval")
	}
	if !strings.Contains(brief2, "do not spawn subagents") || strings.Contains(brief2, "hephaestus") {
		t.Error("solo brief still describes the subagent crew")
	}
	if !strings.Contains(brief2, "final artifact") || strings.Contains(brief2, "gh pr checks") {
		t.Error("question brief has wrong deliverable")
	}
	if brief == brief2 {
		t.Error("briefs must be unique per job")
	}
}

func TestHermesBrief(t *testing.T) {
	j := &Job{Slug: "x", Type: "build", File: "/tmp/x.job.md"}
	brief := hermesBrief(j)
	for _, want := range []string{"hermes", "never write code", "plan ready", "zeus looks blocked",
		"answer to", "status: open", "implication", "k s zeus-x", "k r zeus-x"} {
		if !strings.Contains(brief, want) {
			t.Errorf("hermes brief missing %q", want)
		}
	}
	if brief == hermesBrief(&Job{Slug: "y", Type: "explore", File: "/tmp/y.job.md"}) {
		t.Error("hermes briefs must be unique per job")
	}
}

func TestMarkSlugIssues(t *testing.T) {
	a := mkJob("Yelp Dedupe", "", true) // slugifies to yelp-dedupe
	b := mkJob("yelp-dedupe", "", true) // collides with a
	c := mkJob("!!!", "", true)         // slugifies to ""
	d := mkJob("fine", "", true)
	markSlugIssues([]*Job{a, b, c, d})
	if a.Err != "duplicate slug" || b.Err != "duplicate slug" {
		t.Fatalf("collision not flagged: a=%q b=%q", a.Err, b.Err)
	}
	if c.Err != "unusable slug" {
		t.Fatalf("empty slugify not flagged: %q", c.Err)
	}
	if d.Err != "" {
		t.Fatalf("healthy job flagged: %q", d.Err)
	}
}

func TestStripCommentControlChars(t *testing.T) {
	if got := stripComment("evil\x1b[31mFAKE\x1b[0m"); got != "evil[31mFAKE[0m" {
		t.Fatalf("ANSI escape not stripped: %q", got)
	}
	if got := stripComment("a\tb"); got != "ab" {
		t.Fatalf("tab not stripped: %q", got)
	}
}

func TestJobPrompt(t *testing.T) {
	j := parseJob("yelp-dedupe.job.md", []byte(sampleJob))
	prompt := jobPrompt(j)
	if !strings.Contains(prompt, "Dedupe yelp listings") {
		t.Fatalf("spec text missing: %q", prompt)
	}
	if strings.Contains(prompt, "Mid-run log") || strings.Contains(prompt, "do not hand-edit") {
		t.Fatalf("mid-run log leaked into the prompt: %q", prompt)
	}
	// body that is only a mid-run log yields an empty prompt
	if got := jobPrompt(&Job{Body: "## Mid-run log\n- stuff"}); got != "" {
		t.Fatalf("log-only body should give empty prompt: %q", got)
	}
	// no log section: whole body, trimmed
	if got := jobPrompt(&Job{Body: "\n# Spec\nhello\n"}); got != "# Spec\nhello" {
		t.Fatalf("plain body mangled: %q", got)
	}
}

func TestCellClaudeArgs(t *testing.T) {
	args := cellClaudeArgs()
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--dangerously-skip-permissions") || !strings.Contains(joined, "--rc") {
		t.Fatalf("cc-alias flags missing: %v", args)
	}
}

func TestJobColorStable(t *testing.T) {
	if jobColor("yelp-dedupe") != jobColor("yelp-dedupe") {
		t.Fatal("jobColor not deterministic")
	}
}

func TestStateRankOrdering(t *testing.T) {
	order := []string{"executing", "ready", "waits:x", "draft", "failed", "done"}
	for i := 1; i < len(order); i++ {
		if stateRank(order[i-1]) > stateRank(order[i]) {
			t.Errorf("%s should rank before %s", order[i-1], order[i])
		}
	}
}

func TestCellNaming(t *testing.T) {
	j := &Job{Slug: "Weird Slug!"}
	if got := cellSession(j); got != "cell-weird-slug" {
		t.Fatalf("cellSession: %q", got)
	}
	if got := cellBranch(j); got != "carl/weird-slug" {
		t.Fatalf("cellBranch: %q", got)
	}
	if got := cellWorktree(j, "/Users/car1zhang/Developer/monolith"); !strings.HasSuffix(got, "/weird-slug/wt-monolith") {
		t.Fatalf("cellWorktree: %q", got)
	}
}

func TestParseJobBase(t *testing.T) {
	j := parseJob("x.job.md", []byte("---\njob: x\nbase: origin/develop\n---\n"))
	if j.Err != "" || j.Base != "origin/develop" {
		t.Fatalf("base parse: err=%q base=%q", j.Err, j.Base)
	}
	if j := parseJob("x.job.md", []byte("---\njob: x\n---\n")); j.Base != "" {
		t.Fatalf("base should default empty, got %q", j.Base)
	}
}
