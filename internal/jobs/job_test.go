package jobs

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const sampleJob = `---
job: yelp-dedupe          # slug; this file is yelp-dedupe.job.md
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
	if j.Slug != "yelp-dedupe" || j.Repo != "monolith" {
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
	j := ParseJob("x.job.md", []byte("---\njob: x\nrepo: monolith\nskip-doc: true\n---\n"))
	if j.Err != "" || !j.SkipDoc {
		t.Fatalf("skip-doc: err=%q skipdoc=%v", j.Err, j.SkipDoc)
	}
	if j := ParseJob("x.job.md", []byte("---\njob: x\nskip-doc: sure\n---\n")); j.Err == "" {
		t.Error("bad skip-doc bool: expected Err")
	}
	// Q14: skip-doc is now valid on any job — there is no more job-level
	// type to cross-check it against, so it never errors purely for being
	// set, key order or not.
	for _, src := range []string{
		"---\njob: x\nrepo: monolith\nskip-doc: true\n---\n",
		"---\njob: x\nskip-doc: true\nrepo: monolith\n---\n",
	} {
		if j := ParseJob("x.job.md", []byte(src)); j.Err != "" {
			t.Errorf("skip-doc: err=%q", j.Err)
		}
	}
	// false is fine too — the checkbox merely untoggled
	if j := ParseJob("x.job.md", []byte("---\njob: x\nrepo: monolith\nskip-doc: false\n---\n")); j.Err != "" {
		t.Errorf("skip-doc false: err=%q", j.Err)
	}
}

func TestParseJobStrict(t *testing.T) {
	// every construct the parser can't fully understand must set Err — an
	// erred job never runs, and the error is surfaced on the board
	cases := map[string]string{
		"no frontmatter":     "just some text\n",
		"unterminated":       "---\njob: x\n",
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

// TestParseJobTypeLegacyDiscard covers Q14: the job-level type concept is
// gone, so the type: frontmatter key is now a consumed-and-discarded legacy
// key exactly like auto-approve/success — ANY value (including the old
// explore/question aliases, or outright garbage) must parse clean, never
// erroring, and never surviving anywhere on the parsed Job.
func TestParseJobTypeLegacyDiscard(t *testing.T) {
	for _, src := range []string{
		"---\ntype: build\n---\n",
		"---\ntype: info\n---\n",
		"---\ntype: explore\n---\n",  // legacy alias
		"---\ntype: question\n---\n", // legacy alias
		"---\ntype:\n---\n",          // empty
		"---\n---\n",                 // absent
		"---\ntype: garbage\n---\n",  // previously an error; now just discarded
	} {
		j := ParseJob("x.job.md", []byte(src))
		if j.Err != "" {
			t.Errorf("%q: unexpected err %q", src, j.Err)
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

// TestParseJobPRsWatermarks covers the watchPRs watermark fields riding
// alongside url/state in a hand-written or daemon-written prs: block, and
// confirms a legacy entry with neither key still parses clean with both
// watermarks left at their empty default — the backward-compat guarantee: a
// file written by an old binary (or a human, or the form editor, which never
// emits these keys) must keep parsing exactly as before.
func TestParseJobPRsWatermarks(t *testing.T) {
	src := `---
job: x
type: build
repo: kraken
ready: true
state: working
prs:
  - url: https://github.com/car1zhang/kraken/pull/7
    state: open
    last-comment-at: 2024-01-02T03:04:05Z
    bumped-sha: abc123
  - url: https://github.com/car1zhang/kraken/pull/9
    state: open
---
body`
	j := ParseJob("x.job.md", []byte(src))
	if j.Err != "" {
		t.Fatalf("unexpected err: %s", j.Err)
	}
	if len(j.PRs) != 2 {
		t.Fatalf("prs: %+v", j.PRs)
	}
	want0 := JobPR{URL: "https://github.com/car1zhang/kraken/pull/7", State: "open",
		LastCommentAt: "2024-01-02T03:04:05Z", BumpedSHA: "abc123"}
	if j.PRs[0] != want0 {
		t.Fatalf("watermarked entry: got %+v, want %+v", j.PRs[0], want0)
	}
	want1 := JobPR{URL: "https://github.com/car1zhang/kraken/pull/9", State: "open"}
	if j.PRs[1] != want1 {
		t.Fatalf("legacy entry (no watermark keys): got %+v, want %+v", j.PRs[1], want1)
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

// TestPatchPRsWatermarkRoundTrip covers watchPRs' two extra frontmatter
// fields through patchPRs: a watermarked entry emits last-comment-at/
// bumped-sha lines and round-trips through the strict parser, while an
// entry with no watermarks set (the pre-watchPRs shape) writes byte-
// identical to before — no blank watermark lines — so files nothing has
// ever watched stay untouched by this feature.
func TestPatchPRsWatermarkRoundTrip(t *testing.T) {
	src := "---\njob: x\ntype: build\nrepo: kraken\nready: true\nstate: working\n---\nbody\n"
	prs := []JobPR{
		{URL: "https://x/pull/1", State: "open", LastCommentAt: "2024-01-02T03:04:05Z", BumpedSHA: "abc123"},
		{URL: "https://x/pull/2", State: "open"}, // no watermarks yet
	}
	got, err := patchPRs(src, prs)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "  - url: https://x/pull/1\n    state: open\n    last-comment-at: 2024-01-02T03:04:05Z\n    bumped-sha: abc123\n") {
		t.Fatalf("watermark lines missing: %q", got)
	}
	if !strings.Contains(got, "  - url: https://x/pull/2\n    state: open\n---") {
		t.Fatalf("unwatermarked entry grew blank watermark lines: %q", got)
	}
	j := ParseJob("x.job.md", []byte(got))
	if j.Err != "" || len(j.PRs) != 2 || j.PRs[0] != prs[0] || j.PRs[1] != prs[1] {
		t.Fatalf("round-trip: err=%q prs=%+v\n%s", j.Err, j.PRs, got)
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

// writeJobPRs must never drop a URL already on disk: the daemon's reconciled
// list is built from j.PRs loaded at the start of a tick, but appendJobPR
// (the pr-hook verb's real-time path) may append a fresh record straight to
// the FILE in between. File has A, X recorded; the caller passes an updated
// A alone (as if X had never been loaded) — the write must still land with
// both A(merged) and X(unchanged), X appended after the caller's slice.
func TestWriteJobPRsUnionsFileOnlyRecords(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.job.md")
	src := "---\njob: x\ntype: build\nrepo: kraken\nready: true\nstate: working\nprs:\n" +
		"  - url: https://x/pull/1\n    state: open\n" +
		"  - url: https://x/pull/2\n    state: open\n---\nbody\n"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	j := ParseJob("x.job.md", []byte(src))
	j.File = path

	// caller only knows about pull/1 (as if pull/2 was appended to the file
	// by the hook after this tick's j.PRs snapshot was taken), and reports
	// it merged
	callerPRs := []JobPR{{URL: "https://x/pull/1", State: "merged"}}
	if err := writeJobPRs(j, callerPRs); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	reparsed := ParseJob("x.job.md", data)
	if reparsed.Err != "" {
		t.Fatalf("reparsed job errored: %s", reparsed.Err)
	}
	want := []JobPR{
		{URL: "https://x/pull/1", State: "merged"},
		{URL: "https://x/pull/2", State: "open"},
	}
	if len(reparsed.PRs) != 2 || reparsed.PRs[0] != want[0] || reparsed.PRs[1] != want[1] {
		t.Fatalf("prs after write: %+v, want %+v", reparsed.PRs, want)
	}
	if len(j.PRs) != 2 || j.PRs[0] != want[0] || j.PRs[1] != want[1] {
		t.Fatalf("j.PRs not updated to union: %+v", j.PRs)
	}
}

// removeJobPRs is the repair pass's write path: unlike writeJobPRs (which
// unions in any file-only URL, protecting a concurrent attach but wrongly
// resurrecting exactly what a repair means to delete), it re-reads the file
// fresh and filters ITS current list by URL — so a PR the drop set never
// named survives untouched. This test only exercises that drop-set
// filtering semantics (a URL outside drop passes through unchanged); it
// doesn't simulate a true concurrent appendJobPR race across processes.
func TestRemoveJobPRs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.job.md")
	src := "---\njob: x\ntype: build\nrepo: kraken\nready: true\nstate: working\nprs:\n" +
		"  - url: https://x/pull/1\n    state: open\n" +
		"  - url: https://x/pull/2\n    state: open\n" +
		"  - url: https://x/pull/3\n    state: open\n" +
		"  - url: https://x/pull/4\n    state: open\n" +
		"  - url: https://x/pull/5\n    state: open\n---\nbody\n"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	j := ParseJob("x.job.md", []byte(src))
	j.File = path

	drop := map[string]bool{
		"https://x/pull/1": true,
		"https://x/pull/2": true,
		"https://x/pull/3": true,
		"https://x/pull/4": true,
	}
	removed, err := removeJobPRs(j, drop)
	if err != nil {
		t.Fatal(err)
	}
	wantRemoved := []string{"https://x/pull/1", "https://x/pull/2", "https://x/pull/3", "https://x/pull/4"}
	if !reflect.DeepEqual(removed, wantRemoved) {
		t.Fatalf("removed: got %v, want %v", removed, wantRemoved)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	reparsed := ParseJob("x.job.md", data)
	if reparsed.Err != "" {
		t.Fatalf("reparsed job errored: %s", reparsed.Err)
	}
	// pull/5 was never in the drop set — it survives untouched, which is
	// the filtering behavior that also protects a PR attached concurrently
	// between this call's read and write (not exercised here directly).
	want := []JobPR{{URL: "https://x/pull/5", State: "open"}}
	if !reflect.DeepEqual(reparsed.PRs, want) {
		t.Fatalf("prs after remove: got %+v, want %+v", reparsed.PRs, want)
	}
	if !reflect.DeepEqual(j.PRs, want) {
		t.Fatalf("j.PRs not updated: got %+v, want %+v", j.PRs, want)
	}

	// no-op: nothing in drop matches what's now on disk, so nothing is
	// removed and the file is left untouched.
	removed2, err := removeJobPRs(j, map[string]bool{"https://x/pull/999": true})
	if err != nil {
		t.Fatal(err)
	}
	if removed2 != nil {
		t.Fatalf("no-op drop: got removed %v, want nil", removed2)
	}
}

// updatePRWatermark is watchPRs' write path: it must land the new watermark
// on the right URL, leave every other recorded field (including a sibling
// PR's own watermark) untouched, and no-op cleanly when the URL is no longer
// on disk (dropped by a concurrent repair pass or hand edit between load and
// write) rather than resurrecting it.
func TestUpdatePRWatermark(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.job.md")
	src := "---\njob: x\ntype: build\nrepo: kraken\nready: true\nstate: working\nprs:\n" +
		"  - url: https://x/pull/1\n    state: open\n" +
		"  - url: https://x/pull/2\n    state: open\n    last-comment-at: 2024-01-01T00:00:00Z\n    bumped-sha: sha0\n---\nbody\n"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	j := ParseJob("x.job.md", []byte(src))
	j.File = path

	if err := updatePRWatermark(j, "https://x/pull/1", "2024-02-01T00:00:00Z", "sha1"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	reparsed := ParseJob("x.job.md", data)
	if reparsed.Err != "" {
		t.Fatalf("reparsed job errored: %s", reparsed.Err)
	}
	want := []JobPR{
		{URL: "https://x/pull/1", State: "open", LastCommentAt: "2024-02-01T00:00:00Z", BumpedSHA: "sha1"},
		{URL: "https://x/pull/2", State: "open", LastCommentAt: "2024-01-01T00:00:00Z", BumpedSHA: "sha0"},
	}
	if len(reparsed.PRs) != 2 || reparsed.PRs[0] != want[0] || reparsed.PRs[1] != want[1] {
		t.Fatalf("prs after watermark update: got %+v, want %+v", reparsed.PRs, want)
	}
	if len(j.PRs) != 2 || j.PRs[0] != want[0] || j.PRs[1] != want[1] {
		t.Fatalf("j.PRs not updated: got %+v, want %+v", j.PRs, want)
	}

	// URL no longer on disk (simulating a concurrent drop): no-op, no error,
	// and the file is left byte-for-byte as read.
	before, _ := os.ReadFile(path)
	if err := updatePRWatermark(j, "https://x/pull/999", "2024-03-01T00:00:00Z", "sha2"); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatalf("unknown-URL update touched the file:\nbefore: %s\nafter:  %s", before, after)
	}
}
