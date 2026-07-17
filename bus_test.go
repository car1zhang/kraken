package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const sampleEscalation = `---
status: open
category: plan-review
title: approve the dispatch-v2 plan
---
## Summary
Zeus wants to restructure the dispatcher.

1. Keep the old queue or replace it? Implication: replacing drops in-flight jobs.
`

func TestParseEscalationGood(t *testing.T) {
	e := parseEscalation("dispatch-v2", "003-approve-plan.md", []byte(sampleEscalation), time.Time{})
	if e.Status != "open" || e.Category != "plan-review" || e.Title != "approve the dispatch-v2 plan" {
		t.Fatalf("got %+v", e)
	}
	if !strings.Contains(e.Body, "Zeus wants to restructure") {
		t.Errorf("body: %q", e.Body)
	}
	if e.JobSlug != "dispatch-v2" || e.File != "003-approve-plan.md" {
		t.Errorf("identity: %+v", e)
	}
}

func TestParseEscalationNoFrontmatter(t *testing.T) {
	data := []byte("just some notes hermes dropped, no fence at all\n")
	e := parseEscalation("job1", "007-quick-note.md", data, time.Time{})
	if e.Status != "open" {
		t.Errorf("status: got %q, want open", e.Status)
	}
	if e.Title != "quick note" {
		t.Errorf("title: got %q", e.Title)
	}
	if e.Body != strings.TrimSpace(string(data)) {
		t.Errorf("body should be the whole file: %q", e.Body)
	}
}

func TestParseEscalationUnterminatedFrontmatter(t *testing.T) {
	data := []byte("---\nstatus: open\ncategory: question\n(no closing fence)\n")
	e := parseEscalation("job1", "010-half-written.md", data, time.Time{})
	if e.Status != "open" {
		t.Errorf("status: got %q, want open (unterminated frontmatter never hides an escalation)", e.Status)
	}
	if e.Category != "" {
		t.Errorf("category should not have been parsed out of an unterminated fence: %q", e.Category)
	}
	if e.Title != "half written" {
		t.Errorf("title: got %q", e.Title)
	}
	if e.Body != data0(data) {
		t.Errorf("body should be the raw file: %q", e.Body)
	}
}

// data0 mirrors the TrimSpace parseEscalation applies to the whole-file body.
func data0(data []byte) string { return strings.TrimSpace(string(data)) }

func TestParseEscalationBadStatus(t *testing.T) {
	data := []byte("---\nstatus: resolved-ish\ncategory: steering\ntitle: weird status\n---\nbody\n")
	e := parseEscalation("job1", "x.md", data, time.Time{})
	if e.Status != "open" {
		t.Errorf("unrecognized status must read as open, got %q", e.Status)
	}
}

func TestParseEscalationAnswered(t *testing.T) {
	data := []byte("---\nstatus: answered\ncategory: question\ntitle: t\n---\nbody\n")
	e := parseEscalation("job1", "x.md", data, time.Time{})
	if e.Status != "answered" {
		t.Errorf("got %q", e.Status)
	}
}

func TestParseEscalationUnknownKeyIgnored(t *testing.T) {
	// hermes adds a field kraken doesn't know about yet: must not break parsing
	// of the keys that follow it
	data := []byte("---\nstatus: open\npriority: high\ncategory: verification\ntitle: t\n---\nbody\n")
	e := parseEscalation("job1", "x.md", data, time.Time{})
	if e.Category != "verification" || e.Title != "t" {
		t.Errorf("keys after an unknown one should still parse: %+v", e)
	}
}

func TestTitleFromFilename(t *testing.T) {
	cases := map[string]string{
		"005-approve-the-plan.md": "approve the plan",
		"no-number-prefix.md":     "no number prefix",
		"42.md":                   "42", // no dash after the digits: nothing to strip, kept as-is
		"plain.md":                "plain",
	}
	for in, want := range cases {
		if got := titleFromFilename(in); got != want {
			t.Errorf("titleFromFilename(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLoadEscalationsOpenFirstNewestFirst(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	j := &Job{Slug: "sort-job"}
	inbox := cellInbox(j)
	if err := os.MkdirAll(inbox, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, status string, mtime time.Time) {
		path := filepath.Join(inbox, name)
		if err := os.WriteFile(path, []byte("---\nstatus: "+status+"\ntitle: "+name+"\n---\nbody\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	write("a-answered.md", "answered", now)
	write("b-open-old.md", "open", now.Add(-time.Hour))
	write("c-open-new.md", "open", now)

	escs := loadEscalations([]*Job{j})
	if len(escs) != 3 {
		t.Fatalf("want 3 escalations, got %d", len(escs))
	}
	if escs[0].Status != "open" || escs[0].Title != "c-open-new.md" {
		t.Errorf("pos 0 should be the newest open one, got %+v", escs[0])
	}
	if escs[1].Status != "open" || escs[1].Title != "b-open-old.md" {
		t.Errorf("pos 1 should be the older open one, got %+v", escs[1])
	}
	if escs[2].Status != "answered" {
		t.Errorf("answered escalation should sort last, got %+v", escs[2])
	}
}

func TestLoadEscalationsMissingInboxSkipsSilently(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	j := &Job{Slug: "no-cell-yet"}
	if got := loadEscalations([]*Job{j}); got != nil {
		t.Errorf("want nil for a job with no cell/inbox, got %v", got)
	}
}

func TestAnswerFileContentAppendsAndPatchesStatus(t *testing.T) {
	got, err := answerFileContent(sampleEscalation, "keep the old queue")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(got, "status: answered") {
		t.Errorf("status not patched: %q", got)
	}
	if strings.Contains(got, "status: open") {
		t.Errorf("old status line should be gone: %q", got)
	}
	if !strings.Contains(got, "## answer (") || !strings.Contains(got, "keep the old queue") {
		t.Errorf("answer section missing: %q", got)
	}
	// original body content survives untouched
	if !strings.Contains(got, "Zeus wants to restructure the dispatcher") {
		t.Errorf("original body lost: %q", got)
	}
}

func TestAnswerFileContentDoubleAnswer(t *testing.T) {
	first, err := answerFileContent(sampleEscalation, "first answer")
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := answerFileContent(first, "second answer")
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if strings.Count(second, "status:") != 1 {
		t.Errorf("expected exactly one status: line after two answers, got:\n%s", second)
	}
	if !strings.Contains(second, "status: answered") {
		t.Errorf("status should still read answered: %q", second)
	}
	if !strings.Contains(second, "first answer") || !strings.Contains(second, "second answer") {
		t.Errorf("both answers should be present: %q", second)
	}
	// still parseable as a single well-formed frontmatter block
	lines := strings.Split(second, "\n")
	if !isFence(lines[0]) {
		t.Fatalf("opening fence lost: %q", lines[0])
	}
}

func TestAppendMidRunLogHeadingPresent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "j.job.md")
	content := "---\njob: j\nstate: executing\n---\n\n# Spec\ndo the thing\n\n## Mid-run log\n<!-- appended by kraken; do not hand-edit -->\n- [2026-07-01 10:00 me→hermes] earlier note\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	j := &Job{File: path}
	if err := appendMidRunLog(j, "me→hermes", "answer to x.md: yes"); err != nil {
		t.Fatalf("err: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	if !strings.HasPrefix(s, "---\njob: j\nstate: executing\n---\n\n# Spec\ndo the thing\n\n## Mid-run log\n") {
		t.Fatalf("content above/at the heading was altered:\n%s", s)
	}
	if !strings.Contains(s, "earlier note") {
		t.Errorf("earlier log line lost:\n%s", s)
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	last := lines[len(lines)-1]
	if !strings.Contains(last, "answer to x.md: yes") || !strings.HasPrefix(last, "- [") {
		t.Errorf("new entry should be the last line, got %q", last)
	}
}

func TestAppendMidRunLogHeadingMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "j.job.md")
	content := "---\njob: j\nstate: executing\n---\n\n# Spec\ndo the thing\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	j := &Job{File: path}
	if err := appendMidRunLog(j, "me→hermes", "first entry"); err != nil {
		t.Fatalf("err: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	if !strings.HasPrefix(s, "---\njob: j\nstate: executing\n---\n\n# Spec\ndo the thing\n") {
		t.Fatalf("spec body above the heading was altered:\n%s", s)
	}
	if !strings.Contains(s, "## Mid-run log\n") {
		t.Errorf("heading was not added:\n%s", s)
	}
	if !strings.Contains(s, "first entry") {
		t.Errorf("entry missing:\n%s", s)
	}
	// heading must come before the entry
	if strings.Index(s, "## Mid-run log") > strings.Index(s, "first entry") {
		t.Errorf("heading should precede the entry:\n%s", s)
	}
}

func TestAppendMidRunLogFrontmatterByteForByte(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "j.job.md")
	frontmatter := "---\njob: j\nstate: executing\nsuccess: |\n  a thing\n  another thing\n---\n"
	content := frontmatter + "\n# Spec\nbody text\n\n## Mid-run log\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	j := &Job{File: path}
	if err := appendMidRunLog(j, "me→hermes", "note"); err != nil {
		t.Fatalf("err: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(got), frontmatter) {
		t.Fatalf("frontmatter must be untouched byte-for-byte, got:\n%s", string(got))
	}
}

func TestSyncStatesFromInbox(t *testing.T) {
	planReview := &Job{Slug: "a", State: "executing"}
	blocked := &Job{Slug: "b", State: "executing"}
	untouched := &Job{Slug: "c", State: "executing"}
	done := &Job{Slug: "d", State: "done"}
	notStarted := &Job{Slug: "e", State: ""}
	jobs := []*Job{planReview, blocked, untouched, done, notStarted}

	// writeJobState shells out to nothing but a file write; give each job a
	// real temp file so the write path succeeds
	for _, j := range jobs {
		dir := t.TempDir()
		path := filepath.Join(dir, j.Slug+".job.md")
		os.WriteFile(path, []byte("---\njob: "+j.Slug+"\nstate: "+j.State+"\n---\nbody\n"), 0o644)
		j.File = path
	}

	escs := []*Escalation{
		{JobSlug: "a", Status: "open", Category: "plan-review"},
		{JobSlug: "b", Status: "open", Category: "question"},
		{JobSlug: "d", Status: "open", Category: "plan-review"},     // done job: ignored
		{JobSlug: "e", Status: "open", Category: "plan-review"},     // never started: ignored
		{JobSlug: "c", Status: "answered", Category: "plan-review"}, // no OPEN escalation: left alone
	}

	transitions := syncStatesFromInbox(jobs, escs)
	if planReview.State != "plan-review" {
		t.Errorf("job a: want plan-review, got %s", planReview.State)
	}
	if blocked.State != "blocked" {
		t.Errorf("job b: want blocked, got %s", blocked.State)
	}
	if untouched.State != "executing" {
		t.Errorf("job c should be left alone, got %s", untouched.State)
	}
	if done.State != "done" || notStarted.State != "" {
		t.Errorf("done/not-started jobs must never be touched: d=%s e=%s", done.State, notStarted.State)
	}
	if len(transitions) != 2 {
		t.Fatalf("want 2 transition lines, got %d: %v", len(transitions), transitions)
	}
}

func TestListArtifactsMissingDir(t *testing.T) {
	j := &Job{Slug: "ghost-job-" + t.Name()}
	if got := listArtifacts(j); got != nil {
		t.Errorf("missing artifacts dir should return nil, got %v", got)
	}
}

func TestListArtifactsSorted(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	j := &Job{Slug: "sorted-artifacts"}
	adir := cellArtifacts(j)
	if err := os.MkdirAll(adir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"b.md", "a.md", "c.md"} {
		if err := os.WriteFile(filepath.Join(adir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := listArtifacts(j)
	want := []string{
		filepath.Join(adir, "a.md"),
		filepath.Join(adir, "b.md"),
		filepath.Join(adir, "c.md"),
	}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestAppendMidRunLogHeadingInFrontmatter(t *testing.T) {
	// a success block scalar mentioning the heading must not suppress the
	// real body heading (regression: false-positive fence-less scan)
	dir := t.TempDir()
	path := dir + "/x.job.md"
	src := "---\njob: x\nstate: executing\nsuccess: |\n  ship it\n  ## Mid-run log should show progress\n---\n\n# Spec\nBuild the feature.\n"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	j := &Job{Slug: "x", File: path}
	if err := appendMidRunLog(j, "me→hermes", "first entry"); err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(path)
	body := string(out)
	if !strings.Contains(body, "\n## Mid-run log\n") {
		t.Fatalf("real heading not added:\n%s", body)
	}
	// second append must not duplicate the heading
	if err := appendMidRunLog(j, "me→hermes", "second entry"); err != nil {
		t.Fatal(err)
	}
	out, _ = os.ReadFile(path)
	if strings.Count(string(out), "## Mid-run log") != 2 { // 1 in success scalar + 1 real
		t.Fatalf("heading count wrong:\n%s", out)
	}
	if p := jobPrompt(parseJob("x.job.md", out)); strings.Contains(p, "first entry") {
		t.Fatalf("log leaked into zeus prompt: %q", p)
	}
}
