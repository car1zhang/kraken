package jobs

import (
	"strings"
	"testing"
)

const sampleJob = `---
job: yelp-dedupe          # slug; this file is yelp-dedupe.job.md
type: build               # build | info
repo: monolith            # substring-matched against existing repos (alphabetic tiebreak);
                          # no match → job errors, surfaced in the viewer
ready: true               # flip to true to queue
prereqs:
  - job: dedupe-strategy-q
    gated: true
  - job: schema-migration
solo: false               # true → zeus executes alone, no subagent crew
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
	j := ParseJob("yelp-dedupe.job.md", []byte(sampleJob))
	if j.Err != "" {
		t.Fatalf("unexpected err: %s", j.Err)
	}
	if j.Slug != "yelp-dedupe" || j.Type != "build" || j.Repo != "monolith" {
		t.Fatalf("identity: %+v", j)
	}
	if !j.Ready || j.Solo {
		t.Fatalf("flags: ready=%v solo=%v", j.Ready, j.Solo)
	}
	want := []Prereq{{Job: "dedupe-strategy-q", Gated: true}, {Job: "schema-migration"}}
	if len(j.Prereqs) != 2 || j.Prereqs[0] != want[0] || j.Prereqs[1] != want[1] {
		t.Fatalf("prereqs: %+v", j.Prereqs)
	}
	// success is a retired field: legacy files still carry it, and the
	// block-scalar lines must still be fully consumed (Err == "", body intact)
	if !strings.Contains(j.Body, "# Spec") || strings.Contains(j.Body, "ready:") {
		t.Fatalf("body: %q", j.Body)
	}
}

func TestParseJobAutoApproveLegacy(t *testing.T) {
	// auto-approve is a retired field: legacy pluto job files still carry it
	// (true or false), and the parser must keep consuming the line rather
	// than erroring — the value itself is simply discarded.
	for _, val := range []string{"true", "false"} {
		src := "---\njob: x\nrepo: monolith\nready: true\nauto-approve: " + val + "\n---\n"
		j := ParseJob("x.job.md", []byte(src))
		if j.Err != "" {
			t.Fatalf("auto-approve: %s: unexpected err: %s", val, j.Err)
		}
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
success: |
  what "done" looks like
---

# Spec
`
	j := ParseJob("name.job.md", []byte(src))
	if j.Err != "" {
		t.Fatalf("unexpected err: %s", j.Err)
	}
	if len(j.Prereqs) != 0 {
		t.Fatalf("template prereqs should be empty, got %+v", j.Prereqs)
	}
	if j.Ready {
		t.Fatal("template must not be ready")
	}
}

func TestJobTemplateClean(t *testing.T) {
	// JobTemplate is the actual skeleton `k n`/nvim expand: it must parse
	// clean and its body must be blank — no leftover "# Spec" heading and
	// no prompt text to send zeus on an unedited job.
	src := JobTemplate("x")
	if strings.Contains(src, "# Spec") {
		t.Fatalf("template body should have no # Spec heading:\n%s", src)
	}
	j := ParseJob("x.job.md", []byte(src))
	if j.Err != "" {
		t.Fatalf("unexpected err: %s", j.Err)
	}
	if got := jobPrompt(j); got != "" {
		t.Fatalf("template prompt should be empty, got %q", got)
	}
}

func TestParseJobModel(t *testing.T) {
	// model: any plain CLI token (shape check, not an allowlist); empty or
	// absent means claude's default
	for _, val := range []string{"sonnet", "fable", "claude-opus-4-8", "us.anthropic.claude_v2"} {
		src := "---\njob: x\nrepo: monolith\nmodel: " + val + "\n---\n"
		j := ParseJob("x.job.md", []byte(src))
		if j.Err != "" || j.Model != val {
			t.Errorf("model %q: err=%q model=%q", val, j.Err, j.Model)
		}
	}
	if j := ParseJob("x.job.md", []byte("---\njob: x\nmodel:\n---\n")); j.Err != "" || j.Model != "" {
		t.Errorf("empty model: err=%q model=%q", j.Err, j.Model)
	}
	// comments strip like any scalar
	j := ParseJob("x.job.md", []byte("---\njob: x\nmodel: haiku  # cheap\n---\n"))
	if j.Err != "" || j.Model != "haiku" {
		t.Errorf("commented model: err=%q model=%q", j.Err, j.Model)
	}
	// non-token values err loudly — the value lands on a claude argv
	for _, val := range []string{"sonnet or opus", "a;b", "läuft"} {
		src := "---\njob: x\nmodel: " + val + "\n---\n"
		if j := ParseJob("x.job.md", []byte(src)); j.Err == "" {
			t.Errorf("model %q: expected Err, got clean parse", val)
		}
	}
}

func TestParseJobSkipDoc(t *testing.T) {
	j := ParseJob("x.job.md", []byte("---\njob: x\ntype: build\nrepo: monolith\nskip-doc: true\n---\n"))
	if j.Err != "" || !j.SkipDoc {
		t.Fatalf("skip-doc build: err=%q skipdoc=%v", j.Err, j.SkipDoc)
	}
	if j := ParseJob("x.job.md", []byte("---\njob: x\ntype: build\nskip-doc: sure\n---\n")); j.Err == "" {
		t.Error("bad skip-doc bool: expected Err")
	}
	// info jobs have no doc→send split to skip: strict-loud, regardless of
	// key order relative to type
	for _, src := range []string{
		"---\njob: x\ntype: info\nskip-doc: true\n---\n",
		"---\njob: x\nskip-doc: true\ntype: info\n---\n",
	} {
		if j := ParseJob("x.job.md", []byte(src)); !strings.Contains(j.Err, "skip-doc") {
			t.Errorf("skip-doc on info job: err=%q", j.Err)
		}
	}
	// false on an info job is fine — the checkbox merely untoggled
	if j := ParseJob("x.job.md", []byte("---\njob: x\ntype: info\nrepo: monolith\nskip-doc: false\n---\n")); j.Err != "" {
		t.Errorf("skip-doc false on info job: err=%q", j.Err)
	}
}

func TestParseJobStrict(t *testing.T) {
	// every construct the parser can't fully understand must set Err — an
	// erred job never runs, and the error is surfaced on the board
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
		j := ParseJob("f.job.md", []byte(src))
		if j == nil {
			t.Fatalf("%s: nil job (must never crash)", name)
		}
		if j.Err == "" {
			t.Errorf("%s: expected Err, got clean parse: %+v", name, j)
		}
		if got := DisplayState(j, nil); got != "draft" {
			t.Errorf("%s: erred job must display draft, got %q", name, got)
		}
	}
	// errors accumulate rather than overwriting
	j := ParseJob("f.job.md", []byte("---\nready: yes\nraedy: true\n---\n"))
	if !strings.Contains(j.Err, ";") {
		t.Errorf("errors should accumulate: %q", j.Err)
	}
	// empty file: no frontmatter, still a valid (erred) job value
	if j := ParseJob("f.job.md", []byte("")); j.Err == "" || j.Slug != "f" {
		t.Errorf("empty file: err=%q slug=%q", j.Err, j.Slug)
	}
	// empty bool value reads as absent, not an error
	if j := ParseJob("f.job.md", []byte("---\nready:\n---\n")); j.Err != "" || j.Ready {
		t.Errorf("empty bool value: err=%q ready=%v", j.Err, j.Ready)
	}
}

func TestParseJobStateAndOutcome(t *testing.T) {
	src := "---\njob: x\nready: true\nstate: working\noutcome: went well\n---\nbody"
	j := ParseJob("x.job.md", []byte(src))
	if j.State != "working" || j.Outcome != "went well" {
		t.Fatalf("state/outcome: %q %q", j.State, j.Outcome)
	}
	if !j.Started() {
		t.Fatal("working should count as started")
	}
}

func TestParseJobTeardownMarker(t *testing.T) {
	src := "---\njob: x\nready: true\nstate: working\nteardown: done:accepted\n---\nbody"
	j := ParseJob("x.job.md", []byte(src))
	if j.Err != "" {
		t.Fatalf("teardown marker must parse cleanly, got err %q", j.Err)
	}
	if j.Teardown != "done:accepted" {
		t.Fatalf("teardown = %q, want %q", j.Teardown, "done:accepted")
	}
	// clearing the marker removes the line entirely (patchField's empty-val
	// removal), so finished teardowns leave no residue in hand-read files
	out, err := patchField(src, "teardown", "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "teardown") {
		t.Fatalf("cleared marker still present:\n%s", out)
	}
}

func TestParseJobType(t *testing.T) {
	cases := map[string]struct {
		wantType string
		wantErr  bool
	}{
		"---\ntype: build\n---\n":    {"build", false},
		"---\ntype: info\n---\n":     {"info", false},
		"---\ntype: explore\n---\n":  {"info", false},  // legacy alias
		"---\ntype: question\n---\n": {"info", false},  // legacy alias
		"---\ntype:\n---\n":          {"build", false}, // empty keeps default
		"---\n---\n":                 {"build", false}, // absent keeps default
		"---\ntype: garbage\n---\n":  {"build", true},
	}
	for src, want := range cases {
		j := ParseJob("x.job.md", []byte(src))
		if j.Type != want.wantType {
			t.Errorf("%q: type = %q, want %q", src, j.Type, want.wantType)
		}
		if (j.Err != "") != want.wantErr {
			t.Errorf("%q: err = %q, wantErr %v", src, j.Err, want.wantErr)
		}
	}
}

func TestValidStates(t *testing.T) {
	for _, s := range []string{"working", "blocked", "done"} {
		j := ParseJob("x.job.md", []byte("---\nstate: "+s+"\n---\n"))
		if j.Err != "" {
			t.Errorf("state %q should be valid, got err %q", s, j.Err)
		}
	}
	// draft and queued are derived for display, never written: kraken never
	// authors them and a hand-authored one is a dead end (started() false but
	// startable() also false), so they must now be rejected like any other
	// unknown state.
	for _, s := range []string{"queued", "draft", "planning", "plan-review", "executing", "testing", "pr-open", "merged", "failed", "abandoned"} {
		j := ParseJob("x.job.md", []byte("---\nstate: "+s+"\n---\n"))
		if j.Err == "" {
			t.Errorf("state %q should now be unknown, got clean parse", s)
		}
	}
}

func TestStarted(t *testing.T) {
	cases := []struct {
		state string
		want  bool
	}{
		{"", false},
		{"queued", false},
		{"working", true},
		{"blocked", true},
		{"done", true},
	}
	for _, c := range cases {
		j := &Job{State: c.state}
		if got := j.Started(); got != c.want {
			t.Errorf("started() with state %q = %v, want %v", c.state, got, c.want)
		}
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
	// an indented --- inside a legacy success block scalar is content, not
	// the closing fence: the fence must not terminate early, and fields
	// after the block must still parse
	src := "---\njob: x\nsuccess: |\n  part one\n  ---\n  part two\nrepo: monolith\nready: true\n---\n\n# Spec\n"
	j := ParseJob("x.job.md", []byte(src))
	if j.Err != "" {
		t.Fatalf("unexpected err: %s", j.Err)
	}
	if j.Repo != "monolith" || !j.Ready {
		t.Fatalf("fields after indented --- dropped: repo=%q ready=%v", j.Repo, j.Ready)
	}
	if !strings.Contains(j.Body, "# Spec") {
		t.Fatalf("body truncated: %q", j.Body)
	}
}

func TestParseJobCRLF(t *testing.T) {
	src := strings.ReplaceAll(sampleJob, "\n", "\r\n")
	j := ParseJob("yelp-dedupe.job.md", []byte(src))
	if j.Err != "" || !j.Ready || j.Repo != "monolith" {
		t.Fatalf("CRLF parse degraded: err=%q ready=%v repo=%q", j.Err, j.Ready, j.Repo)
	}
}

func TestParsePrereqsTabIndent(t *testing.T) {
	src := "---\njob: x\nready: true\nprereqs:\n\t- job: dep\n\t  gated: true\n---\n"
	j := ParseJob("x.job.md", []byte(src))
	if len(j.Prereqs) != 1 || !j.Prereqs[0].Gated {
		t.Fatalf("tab-indented gated flag lost: %+v", j.Prereqs)
	}
}

func TestPatchStateHealsIndented(t *testing.T) {
	src := "---\njob: x\n state: working\n---\nbody"
	out, err := patchState(src, "done")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(out, "state:") != 1 {
		t.Fatalf("indented state line not healed:\n%s", out)
	}
	if j := ParseJob("x.job.md", []byte(out)); j.State != "done" {
		t.Fatalf("state after heal: %q", j.State)
	}
}

func TestPatchState(t *testing.T) {
	// insert when absent — body untouched byte-for-byte
	out, err := patchState(sampleJob, "working")
	if err != nil {
		t.Fatal(err)
	}
	j := ParseJob("yelp-dedupe.job.md", []byte(out))
	if j.State != "working" {
		t.Fatalf("state after insert: %q", j.State)
	}
	orig := ParseJob("yelp-dedupe.job.md", []byte(sampleJob))
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
	if j2 := ParseJob("x.job.md", []byte(out2)); j2.State != "done" {
		t.Fatalf("state after replace: %q", j2.State)
	}
	// malformed input errors
	if _, err := patchState("no frontmatter", "x"); err == nil {
		t.Fatal("expected error for missing frontmatter")
	}
}

func TestPatchFieldOutcome(t *testing.T) {
	src := "---\njob: x\nstate: working\n---\nbody"
	out, err := patchField(src, "outcome", "approach A works")
	if err != nil {
		t.Fatal(err)
	}
	j := ParseJob("x.job.md", []byte(out))
	if j.Err != "" || j.Outcome != "approach A works" || j.State != "working" {
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
	if got := OneLine("a\nb\tc\x1b[31m"); got != "a b c[31m" {
		t.Fatalf("OneLine: %q", got)
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
		{mkJob("a", "", false), "draft"},                                  // !ready → draft
		{mkJob("a", "", true), "queued"},                                  // ready, no prereqs → queued
		{mkJob("a", "working", true), "working"},                          // kraken-managed state wins
		{mkJob("a", "", true, Prereq{Job: "dep"}), "queued"},              // ready + met prereq → queued
		{mkJob("a", "", true, Prereq{Job: "dep", Gated: true}), "queued"}, // met but gated → still queued
		{mkJob("a", "working", true, Prereq{Job: "dep", Gated: true}), "working"},
		{mkJob("a", "", true, Prereq{Job: "pend"}), "queued"},              // unmet prereq → queued
		{mkJob("a", "", true, Prereq{Job: "pend", Gated: true}), "queued"}, // unmet + gated → queued
		{mkJob("a", "", true, Prereq{Job: "missing"}), "queued"},           // missing dep counts unmet → queued
	}
	bySlug := slugMap(done, pending)
	for _, c := range cases {
		if got := DisplayState(c.j, bySlug); got != c.want {
			t.Errorf("DisplayState(%+v) = %q, want %q", c.j, got, c.want)
		}
	}
	// erred job displays draft (the draft column surfaces the error detail),
	// even though it's Ready
	broken := mkJob("b", "", true)
	broken.Err = "prereq cycle"
	if got := DisplayState(broken, bySlug); got != "draft" {
		t.Errorf("erred job should display draft: %q", got)
	}
	// a done job with a parse error still shows done: terminal beats broken
	doneButErred := mkJob("c", "done", true)
	doneButErred.Err = "whatever"
	if got := DisplayState(doneButErred, bySlug); got != "done" {
		t.Errorf("done should win even over an error: %q", got)
	}
}

func TestStartable(t *testing.T) {
	done := mkJob("dep", "done", true)
	pending := mkJob("pend", "", true)
	bySlug := slugMap(done, pending)
	cases := []struct {
		j    *Job
		want bool
	}{
		{mkJob("a", "", false), false},                                 // !ready
		{mkJob("a", "", true), true},                                   // ready, no prereqs
		{mkJob("a", "", true, Prereq{Job: "dep"}), true},               // ready + met prereq
		{mkJob("a", "", true, Prereq{Job: "pend"}), false},             // unmet prereq
		{mkJob("a", "", true, Prereq{Job: "missing"}), false},          // missing dep counts unmet
		{mkJob("a", "", true, Prereq{Job: "dep", Gated: true}), false}, // met but gated: human must release
		{mkJob("a", "working", true), false},                           // already started
	}
	for _, c := range cases {
		if got := startable(c.j, bySlug); got != c.want {
			t.Errorf("startable(%+v) = %v, want %v", c.j, got, c.want)
		}
	}
	errored := mkJob("e", "", true)
	errored.Err = "boom"
	if startable(errored, bySlug) {
		t.Error("erred job must never be startable")
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
		if got := PickRepo(c[0], candidates); got != c[1] {
			t.Errorf("PickRepo(%q) = %q, want %q", c[0], got, c[1])
		}
	}
}

func TestJobBySlug(t *testing.T) {
	jobs := []*Job{mkJob("a", "", true), mkJob("b-c", "", true)}
	if JobBySlug(jobs, "b-c") == nil || JobBySlug(jobs, "b-c.job.md") == nil {
		t.Fatal("slug lookup (with and without suffix) failed")
	}
	if JobBySlug(jobs, "nope") != nil {
		t.Fatal("phantom job")
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
	j := ParseJob("yelp-dedupe.job.md", []byte(sampleJob))
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
	args := cellClaudeArgs("")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--dangerously-skip-permissions") || !strings.Contains(joined, "--rc") {
		t.Fatalf("cc-alias flags missing: %v", args)
	}
	if strings.Contains(joined, "--model") {
		t.Fatalf("empty model must not add a --model flag: %v", args)
	}
	// a job's model: field rides along as --model <value>
	withModel := strings.Join(cellClaudeArgs("sonnet"), " ")
	if !strings.Contains(withModel, "--model sonnet") {
		t.Fatalf("model flag missing: %q", withModel)
	}
}

func TestJobColorStable(t *testing.T) {
	if jobColor("yelp-dedupe") != jobColor("yelp-dedupe") {
		t.Fatal("jobColor not deterministic")
	}
}

func TestStateRankOrdering(t *testing.T) {
	// five-state model: live cells first, then queued, then draft, then done
	order := []string{"working", "blocked", "queued", "draft", "done"}
	for i := 1; i < len(order); i++ {
		if stateRank(order[i-1]) > stateRank(order[i]) {
			t.Errorf("%s should rank before %s", order[i-1], order[i])
		}
	}
	// working and blocked are peers within the live-cell rank
	if stateRank("working") != stateRank("blocked") {
		t.Errorf("working and blocked should share a rank: %d != %d", stateRank("working"), stateRank("blocked"))
	}
}

func TestCellNaming(t *testing.T) {
	j := &Job{Slug: "Weird Slug!"}
	if got := CellSession(j); got != "cell-weird-slug" {
		t.Fatalf("CellSession: %q", got)
	}
	if got := CellBranch(j); got != "carl/weird-slug" {
		t.Fatalf("CellBranch: %q", got)
	}
	if got := CellWorktree(j, "/Users/car1zhang/Developer/monolith"); !strings.HasSuffix(got, "/weird-slug/wt-monolith") {
		t.Fatalf("CellWorktree: %q", got)
	}
}

func TestParseJobBase(t *testing.T) {
	j := ParseJob("x.job.md", []byte("---\njob: x\nbase: origin/develop\n---\n"))
	if j.Err != "" || j.Base != "origin/develop" {
		t.Fatalf("base parse: err=%q base=%q", j.Err, j.Base)
	}
	if j := ParseJob("x.job.md", []byte("---\njob: x\n---\n")); j.Base != "" {
		t.Fatalf("base should default empty, got %q", j.Base)
	}
}

// --- prs: list (kraken-owned PR record) ---

func TestParseJobPRs(t *testing.T) {
	src := `---
job: x
type: build
repo: kraken
ready: true
state: working
prs:
  - url: https://github.com/car1zhang/kraken/pull/7
    state: merged
  - url: https://github.com/car1zhang/kraken/pull/9
    state: open
---
body`
	j := ParseJob("x.job.md", []byte(src))
	if j.Err != "" {
		t.Fatalf("unexpected err: %s", j.Err)
	}
	want := []JobPR{
		{URL: "https://github.com/car1zhang/kraken/pull/7", State: "merged"},
		{URL: "https://github.com/car1zhang/kraken/pull/9", State: "open"},
	}
	if len(j.PRs) != 2 || j.PRs[0] != want[0] || j.PRs[1] != want[1] {
		t.Fatalf("prs: %+v", j.PRs)
	}
}

func TestParseJobPRsStrict(t *testing.T) {
	cases := []struct {
		name, block string
	}{
		{"missing url", "prs:\n  - state: merged"},
		{"missing state", "prs:\n  - url: https://x/pull/1"},
		{"bad state", "prs:\n  - url: https://x/pull/1\n    state: reopened"},
		{"unknown key", "prs:\n  - url: https://x/pull/1\n    state: open\n    title: nope"},
		{"scalar value", "prs: https://x/pull/1"},
	}
	for _, c := range cases {
		src := "---\njob: x\ntype: build\nrepo: kraken\nready: true\n" + c.block + "\n---\nbody"
		if j := ParseJob("x.job.md", []byte(src)); j.Err == "" {
			t.Errorf("%s: expected parse error, got none (prs=%+v)", c.name, j.PRs)
		}
	}
}

func TestPatchPRs(t *testing.T) {
	src := "---\njob: x\ntype: build\nrepo: kraken\nready: true\nstate: working\n---\nbody\n"
	prs := []JobPR{{URL: "https://x/pull/1", State: "open"}}

	// insert before the closing fence when absent
	got, err := patchPRs(src, prs)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "prs:\n  - url: https://x/pull/1\n    state: open\n---") {
		t.Fatalf("insert: %q", got)
	}
	if !strings.Contains(got, "state: working") || !strings.Contains(got, "body") {
		t.Fatalf("insert clobbered neighbors: %q", got)
	}

	// replace in place on a later write, and round-trip through the parser
	prs[0].State = "merged"
	prs = append(prs, JobPR{URL: "https://x/pull/2", State: "open"})
	got2, err := patchPRs(got, prs)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(got2, "prs:") != 1 {
		t.Fatalf("replace duplicated the block: %q", got2)
	}
	// what patchPRs wrote must round-trip through the strict parser
	j := ParseJob("x.job.md", []byte(got2))
	if j.Err != "" || len(j.PRs) != 2 || j.PRs[0].State != "merged" || j.PRs[1].URL != "https://x/pull/2" {
		t.Fatalf("round-trip: err=%q prs=%+v\n%s", j.Err, j.PRs, got2)
	}

	// no frontmatter refuses
	if _, err := patchPRs("no fences here", prs); err == nil {
		t.Error("expected error on missing frontmatter")
	}
}

// A legacy success: | block scalar whose CONTENT line starts with "prs:"
// must never be mistaken for the prs: key — the tracker rewrites the block
// every tick, and a trimmed-prefix match would silently eat that content
// line (athena finding on the first cut of patchPRs).
func TestPatchPRsSkipsBlockScalarContent(t *testing.T) {
	src := "---\njob: x\ntype: build\nrepo: kraken\nready: true\nsuccess: |\n  prs: shown on the board\nstate: working\n---\nbody\n"
	got, err := patchPRs(src, []JobPR{{URL: "https://x/pull/1", State: "open"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "  prs: shown on the board") {
		t.Fatalf("success block scalar content eaten: %q", got)
	}
	j := ParseJob("x.job.md", []byte(got))
	if j.Err != "" || len(j.PRs) != 1 {
		t.Fatalf("round-trip: err=%q prs=%+v", j.Err, j.PRs)
	}
}
