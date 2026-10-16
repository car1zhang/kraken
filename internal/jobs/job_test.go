package jobs

import (
	"strings"
	"testing"

	"kraken/internal/core"
	"kraken/internal/store"
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
	if j.Solo {
		t.Fatalf("flags: solo=%v", j.Solo)
	}
	// ready: and prereqs: are retired keys the corpus on disk still carries
	// (this sample is a real file's frontmatter). They must be consumed and
	// discarded — including the prereqs block's indented continuation lines,
	// which would otherwise hit the stray-indented-line arm and err the job
	// into draft. The Err == "" check above is what pins that.
	//
	// success is a retired field too: legacy files still carry it, and the
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
	// a pre-cutover pluto template as expanded: empty prereqs with inline
	// comment lines. JobTemplate no longer emits either key, but files
	// written from the old one are still on disk, so this shape must still
	// parse clean.
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
	if j.State != "" {
		t.Fatalf("template must not carry a state, got %q", j.State)
	}
	if got := DisplayState(j); got != "draft" {
		t.Fatalf("an unstarted job reads %q, want draft", got)
	}
}

func TestJobTemplateDropsRetiredKeys(t *testing.T) {
	// the template is what `k n`/nvim expand into a new file: it must not
	// reintroduce either retired key. Both still PARSE (the corpus needs
	// that), so only this check keeps them from being written afresh.
	src := JobTemplate("x")
	for _, bad := range []string{"ready", "prereqs"} {
		if strings.Contains(src, bad) {
			t.Errorf("template still mentions %q:\n%s", bad, src)
		}
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
	// absent means core.DefaultModel (see TestCellClaudeArgs)
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

// TestParseJobToleratesLegacySkipDoc covers carl's clean-cutover ruling:
// skip-doc is no longer a job field (the doc phase is zeus' own call from
// the spec text now), but legacy pluto job files predating that change may
// still carry a skip-doc: line — ParseJob must accept and silently discard
// it, at any value and any key position, never erroring, same tolerance as
// the type/auto-approve/success legacy keys below.
func TestParseJobToleratesLegacySkipDoc(t *testing.T) {
	for _, src := range []string{
		"---\njob: x\nrepo: monolith\nskip-doc: true\n---\n",
		"---\njob: x\nrepo: monolith\nskip-doc: false\n---\n",
		"---\njob: x\nskip-doc: true\nrepo: monolith\n---\n",
		// even a hand-mangled value must not error — it's discarded, not
		// validated, unlike a real boolField key
		"---\njob: x\nrepo: monolith\nskip-doc: sure\n---\n",
	} {
		if j := ParseJob("x.job.md", []byte(src)); j.Err != "" {
			t.Errorf("skip-doc %q: unexpected err=%q", src, j.Err)
		}
	}
}

func TestParseJobStrict(t *testing.T) {
	// every construct the parser can't fully understand must set Err — an
	// erred job never runs, and the error is surfaced on the board
	cases := map[string]string{
		"no frontmatter": "just some text\n",
		"unterminated":   "---\njob: x\n",
		"colonless line": "---\nnonsense\n---\n",
		"unknown key":    "---\nraedy: true\n---\n",
		"duplicate key":  "---\nrepo: a\nrepo: b\n---\n",
		"bad bool":       "---\nsolo: yes\n---\n",
		"bad state":      "---\nstate: bananas\n---\n",
		"bad color":      "---\ncolor: purpel\n---\n",
		"stray indented": "---\nrepo: a\n  orphan: true\n---\n",
		// no prereqs cases: the key and its whole block are accept-and-ignore
		// now (TestParseJobRetiredKeys below), so nothing under it can err.
	}
	for name, src := range cases {
		j := ParseJob("f.job.md", []byte(src))
		if j == nil {
			t.Fatalf("%s: nil job (must never crash)", name)
		}
		if j.Err == "" {
			t.Errorf("%s: expected Err, got clean parse: %+v", name, j)
		}
		if got := DisplayState(j); got != "draft" {
			t.Errorf("%s: erred job must display draft, got %q", name, got)
		}
	}
	// errors accumulate rather than overwriting
	j := ParseJob("f.job.md", []byte("---\nsolo: yes\nraedy: true\n---\n"))
	if !strings.Contains(j.Err, ";") {
		t.Errorf("errors should accumulate: %q", j.Err)
	}
	// empty file: no frontmatter, still a valid (erred) job value
	if j := ParseJob("f.job.md", []byte("")); j.Err == "" || j.Slug != "f" {
		t.Errorf("empty file: err=%q slug=%q", j.Err, j.Slug)
	}
	// empty bool value reads as absent, not an error
	if j := ParseJob("f.job.md", []byte("---\nsolo:\n---\n")); j.Err != "" || j.Solo {
		t.Errorf("empty bool value: err=%q solo=%v", j.Err, j.Solo)
	}
}

// TestParseJobRetiredKeys pins the accept-and-ignore contract for the two
// keys this change retired. The corpus in ~/Documents/pluto carries them on
// almost every file; a parser that errored on either would pin the entire
// board to draft with an "unknown key" that names a feature nobody can find.
func TestParseJobRetiredKeys(t *testing.T) {
	for name, src := range map[string]string{
		"ready true":         "---\njob: x\nready: true\n---\nbody\n",
		"ready false":        "---\njob: x\nready: false\n---\nbody\n",
		"ready mangled":      "---\njob: x\nready: yes\n---\nbody\n",
		"ready empty":        "---\njob: x\nready:\n---\nbody\n",
		"prereqs empty list": "---\njob: x\nprereqs: []\nsolo: true\n---\nbody\n",
		"prereqs block":      "---\njob: x\nprereqs:\n  - job: dep\n    gated: true\n  - job: other\nsolo: true\n---\nbody\n",
		"prereqs comments":   "---\njob: x\nprereqs: []             # - job: other\n                        #   gated: true\nsolo: true\n---\nbody\n",
		"prereqs tab indent": "---\njob: x\nprereqs:\n\t- job: dep\n\t  gated: true\nsolo: true\n---\nbody\n",
		"prereqs junk":       "---\njob: x\nprereqs:\n  not an item\nsolo: true\n---\nbody\n",
		"prereqs scalar":     "---\njob: x\nprereqs: whenever\nsolo: true\n---\nbody\n",
		"both":               "---\njob: x\nready: true\nprereqs:\n  - job: dep\n---\nbody\n",
	} {
		j := ParseJob("x.job.md", []byte(src))
		if j.Err != "" {
			t.Errorf("%s: unexpected err %q", name, j.Err)
		}
		if got := DisplayState(j); got != "draft" {
			t.Errorf("%s: want draft, got %q", name, got)
		}
	}
	// the block skip must stop at the next top-level key rather than eating
	// it: solo: true above is the canary, checked here on the deepest shape.
	j := ParseJob("x.job.md", []byte("---\njob: x\nprereqs:\n  - job: dep\n    gated: true\nsolo: true\nrepo: monolith\n---\nbody\n"))
	if j.Err != "" || !j.Solo || j.Repo != "monolith" {
		t.Errorf("keys after a prereqs block were swallowed: err=%q solo=%v repo=%q", j.Err, j.Solo, j.Repo)
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
	// draft is derived for display, never written: kraken never authors it
	// and a hand-authored one is a dead end, so it is rejected like any other
	// unknown state. "queued" is now in the same bucket as the other retired
	// state names below — it was never written to a file even when the status
	// existed, so nothing on disk regresses by rejecting it.
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
	src := "---\njob: x\nsuccess: |\n  part one\n  ---\n  part two\nrepo: monolith\nsolo: true\n---\n\n# Spec\n"
	j := ParseJob("x.job.md", []byte(src))
	if j.Err != "" {
		t.Fatalf("unexpected err: %s", j.Err)
	}
	if j.Repo != "monolith" || !j.Solo {
		t.Fatalf("fields after indented --- dropped: repo=%q solo=%v", j.Repo, j.Solo)
	}
	if !strings.Contains(j.Body, "# Spec") {
		t.Fatalf("body truncated: %q", j.Body)
	}
}

func TestParseJobCRLF(t *testing.T) {
	src := strings.ReplaceAll(sampleJob, "\n", "\r\n")
	j := ParseJob("yelp-dedupe.job.md", []byte(src))
	if j.Err != "" || j.Slug != "yelp-dedupe" || j.Repo != "monolith" {
		t.Fatalf("CRLF parse degraded: err=%q slug=%q repo=%q", j.Err, j.Slug, j.Repo)
	}
}

func TestOneLine(t *testing.T) {
	if got := OneLine("a\nb\tc\x1b[31m"); got != "a b c[31m" {
		t.Fatalf("OneLine: %q", got)
	}
}

func mkJob(slug, state string) *Job {
	return &Job{Slug: slug, State: state}
}

func TestDisplayState(t *testing.T) {
	cases := []struct {
		j    *Job
		want string
	}{
		{mkJob("a", ""), "draft"},          // never started
		{mkJob("a", "working"), "working"}, // kraken-managed state wins
		{mkJob("a", "blocked"), "blocked"}, //
		{mkJob("a", "done"), "done"},       //
	}
	for _, c := range cases {
		if got := DisplayState(c.j); got != c.want {
			t.Errorf("DisplayState(%+v) = %q, want %q", c.j, got, c.want)
		}
	}
	// erred job displays draft (the draft column surfaces the error detail)
	broken := mkJob("b", "")
	broken.Err = "unknown key raedy"
	if got := DisplayState(broken); got != "draft" {
		t.Errorf("erred job should display draft: %q", got)
	}
	// an erred job that HAS started still reads draft: the error outranks a
	// kraken-managed state, and the board's draft column is where its detail
	// is rendered.
	brokenLive := mkJob("bl", "working")
	brokenLive.Err = "unknown key raedy"
	if got := DisplayState(brokenLive); got != "draft" {
		t.Errorf("erred working job should display draft: %q", got)
	}
	// a done job with a parse error still shows done: terminal beats broken
	doneButErred := mkJob("c", "done")
	doneButErred.Err = "whatever"
	if got := DisplayState(doneButErred); got != "done" {
		t.Errorf("done should win even over an error: %q", got)
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
	jobs := []*Job{mkJob("a", ""), mkJob("b-c", "")}
	if JobBySlug(jobs, "b-c") == nil || JobBySlug(jobs, "b-c.job.md") == nil {
		t.Fatal("slug lookup (with and without suffix) failed")
	}
	if JobBySlug(jobs, "nope") != nil {
		t.Fatal("phantom job")
	}
}

func TestMarkSlugIssues(t *testing.T) {
	a := mkJob("Yelp Dedupe", "") // slugifies to yelp-dedupe
	b := mkJob("yelp-dedupe", "") // collides with a
	c := mkJob("!!!", "")         // slugifies to ""
	d := mkJob("fine", "")
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
	// no resolved model: kraken's own default rides along — never claude's
	// config. This is the SIDE-THREAD path; zeus resolves through zeusModel
	// below and lands on core.ZeusModel instead.
	if !strings.Contains(joined, "--model "+core.DefaultModel) {
		t.Fatalf("empty model must fall back to core.DefaultModel: %v", args)
	}
	// a job's model: field rides along as --model <value>
	withModel := strings.Join(cellClaudeArgs("sonnet"), " ")
	if !strings.Contains(withModel, "--model sonnet") {
		t.Fatalf("model flag missing: %q", withModel)
	}
}

// TestZeusModel pins the one place the zeus/everything-else split is decided,
// and its FALLBACK is the whole of it: a job with no model: field puts zeus on
// core.ZeusModel while cellClaudeArgs' own fallback (the side-thread path
// above) stays on core.DefaultModel. A hand-authored model: still wins for
// zeus, since it is the documented per-cell override — and it reaches a side
// thread too, so the override is shared and only the fallback is split.
func TestZeusModel(t *testing.T) {
	if got := zeusModel(&Job{Slug: "x"}); got != core.ZeusModel {
		t.Fatalf("zeusModel with no model: field = %q, want core.ZeusModel (%q)", got, core.ZeusModel)
	}
	if got := zeusModel(&Job{Slug: "x", Model: "sonnet"}); got != "sonnet" {
		t.Fatalf("job model: override dropped: %q", got)
	}
}

// TestZeusSpawnArgv pins the seam's own resolution — the value half of what
// TestZeusSpawnPathsResolveTheModel (cell_test.go) holds structurally, and the
// two are deliberately separate: that one asserts both spawn sites go through
// zeusModel, this one asserts what comes out when they do. It also pins the
// resume variadic — the `-r <session>` pair has to reach the argv and has to
// stay ahead of the staged `--` prompt spawnCommand appends last.
func TestZeusSpawnArgv(t *testing.T) {
	fresh := strings.Join(zeusSpawnArgv(&Job{Slug: "x"}), " ")
	if !strings.Contains(fresh, "--model "+core.ZeusModel) {
		t.Fatalf("a fresh zeus must spawn on core.ZeusModel: %q", fresh)
	}
	if strings.Contains(fresh, "--model "+core.DefaultModel) {
		t.Fatalf("zeus took core.DefaultModel — the spawn path skipped zeusModel: %q", fresh)
	}
	if !strings.Contains(fresh, "--name "+ZeusName(&Job{Slug: "x"})) {
		t.Fatalf("zeus spawn missing --name: %q", fresh)
	}
	// reactivation resumes the same session and keeps the same resolution
	resumed := strings.Join(zeusSpawnArgv(&Job{Slug: "x"}, "-r", "sess-1"), " ")
	if !strings.Contains(resumed, "--model "+core.ZeusModel) || !strings.HasSuffix(resumed, "-r sess-1") {
		t.Fatalf("reactivation argv wrong: %q", resumed)
	}
	// a hand-authored model: still wins on both paths
	pinned := strings.Join(zeusSpawnArgv(&Job{Slug: "x", Model: "sonnet"}), " ")
	if !strings.Contains(pinned, "--model sonnet") {
		t.Fatalf("job model: override dropped on the spawn path: %q", pinned)
	}
}

func TestJobColorStable(t *testing.T) {
	if jobColor("yelp-dedupe") != jobColor("yelp-dedupe") {
		t.Fatal("jobColor not deterministic")
	}
}

func TestStateRankOrdering(t *testing.T) {
	// four-state model: live cells first, then draft paperwork, then done
	order := []string{"working", "blocked", "draft", "done"}
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
	// Every runtime name is <slugified slug>-<id>: the slug keeps it readable
	// for a human, the id keeps two jobs of the same name apart.
	j := &Job{Slug: "Weird Slug!", ID: 12}
	if got := CellSession(j); got != "cell-weird-slug-12" {
		t.Fatalf("CellSession: %q", got)
	}
	t.Setenv("KRAKEN_BRANCH_PREFIX", "")
	if got := CellBranch(j); got != "kraken/weird-slug-12" {
		t.Fatalf("CellBranch: %q", got)
	}
	if got := CellWorktree(j, "/Users/car1zhang/Developer/monolith"); !strings.HasSuffix(got, "/weird-slug-12/wt-monolith") {
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

// updatePRWatermark is watchPRs' write path: it must land the new watermark
// on the right URL, leave every other recorded field (including a sibling
// PR's own watermark) untouched, and no-op cleanly when the URL is no longer
// recorded (dropped by a concurrent repair pass between load and write)
// rather than resurrecting it.
func TestUpdatePRWatermark(t *testing.T) {
	st := useTestStore(t)
	row := mustCreateJob(t, st, store.Job{Slug: "x", Repo: "kraken", State: "working"})
	for _, u := range []string{"https://x/pull/1", "https://x/pull/2"} {
		if err := st.UpsertPR(row.ID, u, "open"); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetPRWatermark(row.ID, "https://x/pull/2", "2024-01-01T00:00:00Z", "sha0", ""); err != nil {
		t.Fatal(err)
	}
	j := LoadJobBySlug("x")

	if err := updatePRWatermark(j, "https://x/pull/1", "2024-02-01T00:00:00Z", "sha1", "conflicting"); err != nil {
		t.Fatal(err)
	}
	want := map[string]JobPR{
		"https://x/pull/1": {URL: "https://x/pull/1", State: "open", LastCommentAt: "2024-02-01T00:00:00Z", BumpedSHA: "sha1", ConflictState: "conflicting"},
		"https://x/pull/2": {URL: "https://x/pull/2", State: "open", LastCommentAt: "2024-01-01T00:00:00Z", BumpedSHA: "sha0"},
	}
	check := func(what string, prs []JobPR) {
		t.Helper()
		if len(prs) != 2 {
			t.Fatalf("%s: got %+v, want 2 records", what, prs)
		}
		for _, p := range prs {
			if p != want[p.URL] {
				t.Fatalf("%s: got %+v, want %+v", what, p, want[p.URL])
			}
		}
	}
	check("stored prs after watermark update", LoadJobBySlug("x").PRs)
	check("j.PRs after watermark update", j.PRs)

	// URL no longer recorded (simulating a concurrent drop): no-op, no error,
	// and nothing is resurrected.
	if err := updatePRWatermark(j, "https://x/pull/999", "2024-03-01T00:00:00Z", "sha2", "clean"); err != nil {
		t.Fatal(err)
	}
	check("stored prs after unknown-URL update", LoadJobBySlug("x").PRs)
}

func TestParseJobColor(t *testing.T) {
	// every one of /color's eight names parses clean and lands on the field
	for _, name := range []string{"red", "blue", "green", "yellow", "purple", "orange", "pink", "cyan"} {
		src := "---\njob: x\nrepo: monolith\ncolor: " + name + "\n---\n"
		j := ParseJob("x.job.md", []byte(src))
		if j.Err != "" || j.Color != name {
			t.Errorf("color %q: err=%q color=%q", name, j.Err, j.Color)
		}
	}
	// absent and explicitly-empty both mean NO colour, cleanly
	for _, src := range []string{"---\njob: x\nrepo: monolith\n---\n", "---\njob: x\ncolor:\n---\n"} {
		if j := ParseJob("x.job.md", []byte(src)); j.Err != "" || j.Color != "" {
			t.Errorf("%q: err=%q color=%q, want clean and empty", src, j.Err, j.Color)
		}
	}
	// comments strip like any scalar
	if j := ParseJob("x.job.md", []byte("---\njob: x\ncolor: pink  # the good one\n---\n")); j.Err != "" || j.Color != "pink" {
		t.Errorf("commented color: err=%q color=%q", j.Err, j.Color)
	}
	// off-palette values err loudly — the value is typed straight into a
	// live claude's pane as `/color <val>`, so an allowlist is the contract
	for _, val := range []string{"purpel", "default", "PURPLE", "#827DBD", "red blue"} {
		src := "---\njob: x\ncolor: " + val + "\n---\n"
		j := ParseJob("x.job.md", []byte(src))
		if j.Err == "" {
			t.Errorf("color %q: expected Err, got clean parse", val)
		} else if !strings.Contains(j.Err, "bad color value") {
			t.Errorf("color %q: Err=%q, want a bad color value message", val, j.Err)
		}
	}
}
