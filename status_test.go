package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- newestMtime ---

func TestNewestMtimeMissingDir(t *testing.T) {
	if got := newestMtime(filepath.Join(t.TempDir(), "does-not-exist")); !got.IsZero() {
		t.Errorf("missing dir should read as zero time, got %v", got)
	}
}

func TestNewestMtimeEmptyDir(t *testing.T) {
	if got := newestMtime(t.TempDir()); !got.IsZero() {
		t.Errorf("empty dir should read as zero time, got %v", got)
	}
}

func TestNewestMtimePicksNewestJSONL(t *testing.T) {
	dir := t.TempDir()
	older := time.Now().Add(-time.Hour)
	newer := time.Now()
	write := func(name string, mtime time.Time) {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	write("a-session.jsonl", older)
	write("b-sidechain.jsonl", newer)
	write("ignore-me.txt", newer.Add(time.Hour)) // non-jsonl: must not count

	got := newestMtime(dir)
	if !got.Equal(newer) {
		t.Errorf("got %v, want %v", got, newer)
	}
}

// --- podLastActivity ---

func TestPodLastActivity(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	worktree := "/Users/carl/.kraken/cells/some-job/wt-monolith"
	projDir := filepath.Join(dir, ".claude", "projects", projectDirName(worktree))
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mtime := time.Now().Add(-5 * time.Minute)
	path := filepath.Join(projDir, "sess.jsonl")
	if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	got := podLastActivity(worktree)
	if !got.Equal(mtime) {
		t.Errorf("got %v, want %v", got, mtime)
	}
}

func TestPodLastActivityNoTranscripts(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	if got := podLastActivity("/nowhere/wt-x"); !got.IsZero() {
		t.Errorf("no transcripts should read as zero time, got %v", got)
	}
}

// --- pendingQuestion ---

func writeTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sess.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const askUserQuestionAsk = `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"AskUserQuestion","input":{"questions":[{"question":"Which approach?","header":"Approach","multiSelect":false,"options":[{"label":"A","description":"do A"},{"label":"B","description":"do B"}]}]}}]}}`

func toolResultFor(id string) string {
	return `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + id + `"}]}}`
}

func TestPendingQuestionPending(t *testing.T) {
	path := writeTranscript(t, askUserQuestionAsk)
	pq := pendingQuestion(path)
	if pq == nil {
		t.Fatal("want a pending question, got nil")
	}
	if pq.ToolUseID != "toolu_1" {
		t.Errorf("tool use id: %q", pq.ToolUseID)
	}
	if len(pq.Questions) != 1 || pq.Questions[0].Question != "Which approach?" {
		t.Errorf("questions: %+v", pq.Questions)
	}
	if len(pq.Questions[0].Options) != 2 || pq.Questions[0].Options[0].Label != "A" {
		t.Errorf("options: %+v", pq.Questions[0].Options)
	}
}

func TestPendingQuestionAnswered(t *testing.T) {
	path := writeTranscript(t, askUserQuestionAsk, toolResultFor("toolu_1"))
	if pq := pendingQuestion(path); pq != nil {
		t.Errorf("want no pending question once answered, got %+v", pq)
	}
}

func TestPendingQuestionOnlyLastCallMatters(t *testing.T) {
	first := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"AskUserQuestion","input":{"questions":[{"question":"first?","header":"H","multiSelect":false,"options":[{"label":"A","description":"a"}]}]}}]}}`
	second := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_2","name":"AskUserQuestion","input":{"questions":[{"question":"second?","header":"H","multiSelect":false,"options":[{"label":"A","description":"a"}]}]}}]}}`

	// first answered, second (the last one) left open: still pending, on the second
	path := writeTranscript(t, first, toolResultFor("toolu_1"), second)
	pq := pendingQuestion(path)
	if pq == nil || pq.ToolUseID != "toolu_2" || pq.Questions[0].Question != "second?" {
		t.Fatalf("got %+v", pq)
	}

	// both answered: nothing pending
	path2 := writeTranscript(t, first, toolResultFor("toolu_1"), second, toolResultFor("toolu_2"))
	if pq2 := pendingQuestion(path2); pq2 != nil {
		t.Errorf("want no pending question, got %+v", pq2)
	}

	// a stray result for the superseded first call, arriving AFTER the second
	// ask, must not be mistaken for an answer to the second
	path3 := writeTranscript(t, first, second, toolResultFor("toolu_1"))
	pq3 := pendingQuestion(path3)
	if pq3 == nil || pq3.ToolUseID != "toolu_2" {
		t.Fatalf("stray result for a superseded call should not clear the current one: %+v", pq3)
	}
}

func TestPendingQuestionMalformedLinesInterleaved(t *testing.T) {
	path := writeTranscript(t,
		`not json at all`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_bad","name":"AskUserQuestion","input":"not-an-object"}]}}`,
		``,
		`   `,
		askUserQuestionAsk,
		`{"type":"garbage`,
	)
	pq := pendingQuestion(path)
	if pq == nil || pq.ToolUseID != "toolu_1" {
		t.Fatalf("garbage lines should be skipped, not fatal: %+v", pq)
	}
}

func TestPendingQuestionHugeLine(t *testing.T) {
	// bufio.Scanner's default 64KB buffer would choke on this without an
	// explicit larger buffer; pad well past it.
	pad := strings.Repeat("x", 200*1024)
	huge := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_other","text":"` + pad + `"}]}}`
	path := writeTranscript(t, askUserQuestionAsk, huge)
	pq := pendingQuestion(path)
	// the huge line is an unrelated tool_result (wrong id) — must still be
	// scanned successfully (not choke/truncate) and correctly ignored
	if pq == nil || pq.ToolUseID != "toolu_1" {
		t.Fatalf("got %+v", pq)
	}

	answeringHuge := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1"}]},"pad":"` + pad + `"}`
	path2 := writeTranscript(t, askUserQuestionAsk, answeringHuge)
	if pq2 := pendingQuestion(path2); pq2 != nil {
		t.Errorf("huge answering line should still be parsed and clear pending, got %+v", pq2)
	}
}

func TestPendingQuestionMissingFile(t *testing.T) {
	if pq := pendingQuestion(filepath.Join(t.TempDir(), "nope.jsonl")); pq != nil {
		t.Errorf("missing transcript should read as no pending question, got %+v", pq)
	}
}

func TestPendingQuestionOverFourMBLineWithinNewBuffer(t *testing.T) {
	// real transcripts contain lines up to ~8.3MB (large tool inputs/outputs
	// inline). The old 4MB scanner buffer would choke on a line this size,
	// truncating the scan before it ever reached the answering tool_result
	// and reporting a phantom pending question. The new 16MB buffer must
	// scan straight through it.
	pad := strings.Repeat("x", 5*1024*1024) // >4MB, comfortably under 16MB
	filler := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_other","text":"` + pad + `"}]}}`
	path := writeTranscript(t, askUserQuestionAsk, filler, toolResultFor("toolu_1"))
	if pq := pendingQuestion(path); pq != nil {
		t.Fatalf("answered question after a >4MB line should not report pending, got %+v", pq)
	}
}

func TestPendingQuestionOverSixteenMBLineFailsClosed(t *testing.T) {
	// a line bigger than the scanner's max buffer overflows bufio.Scanner
	// (bufio.ErrTooLong), truncating the scan partway through the file. Before
	// the fix, the code ignored sc.Err() and returned whatever partial state
	// it had accumulated — here, the still-open ask from before the oversized
	// line — reporting a phantom pending question. The fix must fail closed:
	// an unreadable/oversized transcript never renders as blocked-on-a-question.
	pad := strings.Repeat("x", 17*1024*1024) // >16MB: overflows the scanner buffer
	oversized := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_other","text":"` + pad + `"}]}}`
	path := writeTranscript(t, askUserQuestionAsk, oversized)
	if pq := pendingQuestion(path); pq != nil {
		t.Fatalf("oversized line should fail closed to nil, got phantom pending %+v", pq)
	}
}

func TestPendingQuestionNewerAskWithBadInputWins(t *testing.T) {
	// a newer AskUserQuestion tool_use whose input fails to decode must still
	// supersede the older (valid) tracked ask — the newest ask always wins,
	// even when it can't be rendered, rather than leaving the older ask
	// looking pending forever.
	newerBadAsk := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_2","name":"AskUserQuestion","input":"not-an-object"}]}}`
	path := writeTranscript(t, askUserQuestionAsk, newerBadAsk)
	if pq := pendingQuestion(path); pq != nil {
		t.Fatalf("newer ask with undecodable input should supersede the older ask, not leave it pending: %+v", pq)
	}
}

func TestPendingQuestionMemoizedByStat(t *testing.T) {
	// pendingQuestion memoizes per path on (size, mtime): an unchanged file
	// must return the cached verdict without rescanning, and any size-or-mtime
	// change must invalidate it.
	dir := t.TempDir()
	path := filepath.Join(dir, "sess.jsonl")

	pending := askUserQuestionAsk + "\n"
	answered := askUserQuestionAsk + "\n" + toolResultFor("toolu_1") + "\n"
	// pad the pending content to the same byte length as the answered
	// content so a cache hit is distinguishable from a genuine rescan: only
	// the (size, mtime) key changes between the two writes below, not this one.
	if len(answered) < len(pending) {
		t.Fatal("test fixture assumption violated: answered content should be longer")
	}
	pending += strings.Repeat(" ", len(answered)-len(pending))
	if len(pending) != len(answered) {
		t.Fatalf("padding failed: %d != %d", len(pending), len(answered))
	}

	mtime := time.Now()
	if err := os.WriteFile(path, []byte(pending), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	pq1 := pendingQuestion(path)
	if pq1 == nil || pq1.ToolUseID != "toolu_1" {
		t.Fatalf("want pending toolu_1, got %+v", pq1)
	}

	// overwrite with answered content but keep size and mtime identical: the
	// cache must return the stale (still-pending) cached verdict, proving it
	// didn't rescan.
	if err := os.WriteFile(path, []byte(answered), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	pq2 := pendingQuestion(path)
	if pq2 == nil || pq2.ToolUseID != "toolu_1" {
		t.Fatalf("unchanged (size, mtime) should return the cached stale verdict, got %+v", pq2)
	}

	// bump the mtime: the cache must invalidate and rescan, now seeing the
	// answered content correctly.
	newMtime := mtime.Add(time.Second)
	if err := os.Chtimes(path, newMtime, newMtime); err != nil {
		t.Fatal(err)
	}
	if pq3 := pendingQuestion(path); pq3 != nil {
		t.Fatalf("changed mtime should invalidate the cache and rescan, got %+v", pq3)
	}
}

// --- deriveState ---

func TestDeriveState(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-activityStaleAfter) // exactly at the boundary: still fresh (<=)
	stale := now.Add(-activityStaleAfter - time.Second)

	cases := []struct {
		name         string
		j            *Job
		live         bool
		lastActivity time.Time
		want         string
	}{
		{
			name: "done wins even over an error",
			j:    &Job{State: "done", Err: "whatever"},
			live: true,
			want: "done",
		},
		{
			name: "errored job reads as draft",
			j:    &Job{Err: "bad frontmatter", State: "working", Ready: true},
			live: true,
			want: "draft",
		},
		{
			name: "not ready, never started -> draft",
			j:    &Job{Ready: false, State: ""},
			want: "draft",
		},
		{
			name: "ready, never started -> queued",
			j:    &Job{Ready: true, State: ""},
			want: "queued",
		},
		{
			name: "started but cell not live: trust the job file's state",
			j:    &Job{Ready: true, State: "blocked"},
			live: false,
			want: "blocked",
		},
		{
			name:         "started but cell not live, job file says working: trust it too",
			j:            &Job{Ready: true, State: "working"},
			live:         false,
			lastActivity: stale, // irrelevant while !live
			want:         "working",
		},
		{
			name:         "live, activity exactly at the staleness boundary is still fresh -> working",
			j:            &Job{Ready: true, State: "blocked"},
			live:         true,
			lastActivity: fresh,
			want:         "working",
		},
		{
			name:         "live, activity one second past the boundary -> blocked",
			j:            &Job{Ready: true, State: "working"},
			live:         true,
			lastActivity: stale,
			want:         "blocked",
		},
		{
			name:         "live, zero lastActivity (no transcripts yet) -> blocked",
			j:            &Job{Ready: true, State: "working"},
			live:         true,
			lastActivity: time.Time{},
			want:         "blocked",
		},
		{
			name:         "live, activity right now -> working",
			j:            &Job{Ready: true, State: "blocked"},
			live:         true,
			lastActivity: now,
			want:         "working",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := deriveState(c.j, c.live, c.lastActivity, now); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// --- atomicWrite ---

func TestAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := atomicWrite(path, "hello"); err != nil {
		t.Fatalf("err: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Errorf("got %q", got)
	}
	if err := atomicWrite(path, "world"); err != nil {
		t.Fatalf("err: %v", err)
	}
	got, _ = os.ReadFile(path)
	if string(got) != "world" {
		t.Errorf("overwrite: got %q", got)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("no leftover temp file expected, got %v", entries)
	}
}

// --- appendMidRunLog ---

func TestAppendMidRunLogHeadingPresent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "j.job.md")
	content := "---\njob: j\nstate: working\n---\n\n# Spec\ndo the thing\n\n## Mid-run log\n<!-- appended by kraken; do not hand-edit -->\n- [2026-07-01 10:00 me→zeus] earlier note\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	j := &Job{File: path}
	if err := appendMidRunLog(j, "me→zeus", "answer to x.md: yes"); err != nil {
		t.Fatalf("err: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	if !strings.HasPrefix(s, "---\njob: j\nstate: working\n---\n\n# Spec\ndo the thing\n\n## Mid-run log\n") {
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
	content := "---\njob: j\nstate: working\n---\n\n# Spec\ndo the thing\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	j := &Job{File: path}
	if err := appendMidRunLog(j, "me→zeus", "first entry"); err != nil {
		t.Fatalf("err: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	if !strings.HasPrefix(s, "---\njob: j\nstate: working\n---\n\n# Spec\ndo the thing\n") {
		t.Fatalf("spec body above the heading was altered:\n%s", s)
	}
	if !strings.Contains(s, "## Mid-run log\n") {
		t.Errorf("heading was not added:\n%s", s)
	}
	if !strings.Contains(s, "first entry") {
		t.Errorf("entry missing:\n%s", s)
	}
	if strings.Index(s, "## Mid-run log") > strings.Index(s, "first entry") {
		t.Errorf("heading should precede the entry:\n%s", s)
	}
}

func TestAppendMidRunLogFrontmatterByteForByte(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "j.job.md")
	frontmatter := "---\njob: j\nstate: working\nsuccess: |\n  a thing\n  another thing\n---\n"
	content := frontmatter + "\n# Spec\nbody text\n\n## Mid-run log\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	j := &Job{File: path}
	if err := appendMidRunLog(j, "me→zeus", "note"); err != nil {
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

func TestAppendMidRunLogHeadingInFrontmatter(t *testing.T) {
	// a success block scalar mentioning the heading must not suppress the
	// real body heading (regression: false-positive fence-less scan)
	dir := t.TempDir()
	path := dir + "/x.job.md"
	src := "---\njob: x\nstate: working\nsuccess: |\n  ship it\n  ## Mid-run log should show progress\n---\n\n# Spec\nBuild the feature.\n"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	j := &Job{Slug: "x", File: path}
	if err := appendMidRunLog(j, "me→zeus", "first entry"); err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(path)
	body := string(out)
	if !strings.Contains(body, "\n## Mid-run log\n") {
		t.Fatalf("real heading not added:\n%s", body)
	}
	// second append must not duplicate the heading
	if err := appendMidRunLog(j, "me→zeus", "second entry"); err != nil {
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

// --- listArtifacts ---

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
