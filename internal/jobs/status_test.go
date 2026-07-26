package jobs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kraken/internal/core"
)

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
	pq := FindPendingQuestion(path)
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
	if pq := FindPendingQuestion(path); pq != nil {
		t.Errorf("want no pending question once answered, got %+v", pq)
	}
}

func TestPendingQuestionOnlyLastCallMatters(t *testing.T) {
	first := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"AskUserQuestion","input":{"questions":[{"question":"first?","header":"H","multiSelect":false,"options":[{"label":"A","description":"a"}]}]}}]}}`
	second := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_2","name":"AskUserQuestion","input":{"questions":[{"question":"second?","header":"H","multiSelect":false,"options":[{"label":"A","description":"a"}]}]}}]}}`

	// first answered, second (the last one) left open: still pending, on the second
	path := writeTranscript(t, first, toolResultFor("toolu_1"), second)
	pq := FindPendingQuestion(path)
	if pq == nil || pq.ToolUseID != "toolu_2" || pq.Questions[0].Question != "second?" {
		t.Fatalf("got %+v", pq)
	}

	// both answered: nothing pending
	path2 := writeTranscript(t, first, toolResultFor("toolu_1"), second, toolResultFor("toolu_2"))
	if pq2 := FindPendingQuestion(path2); pq2 != nil {
		t.Errorf("want no pending question, got %+v", pq2)
	}

	// a stray result for the superseded first call, arriving AFTER the second
	// ask, must not be mistaken for an answer to the second
	path3 := writeTranscript(t, first, second, toolResultFor("toolu_1"))
	pq3 := FindPendingQuestion(path3)
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
	pq := FindPendingQuestion(path)
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
	pq := FindPendingQuestion(path)
	// the huge line is an unrelated tool_result (wrong id) — must still be
	// scanned successfully (not choke/truncate) and correctly ignored
	if pq == nil || pq.ToolUseID != "toolu_1" {
		t.Fatalf("got %+v", pq)
	}

	answeringHuge := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1"}]},"pad":"` + pad + `"}`
	path2 := writeTranscript(t, askUserQuestionAsk, answeringHuge)
	if pq2 := FindPendingQuestion(path2); pq2 != nil {
		t.Errorf("huge answering line should still be parsed and clear pending, got %+v", pq2)
	}
}

func TestPendingQuestionMissingFile(t *testing.T) {
	if pq := FindPendingQuestion(filepath.Join(t.TempDir(), "nope.jsonl")); pq != nil {
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
	if pq := FindPendingQuestion(path); pq != nil {
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
	if pq := FindPendingQuestion(path); pq != nil {
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
	if pq := FindPendingQuestion(path); pq != nil {
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
	pq1 := FindPendingQuestion(path)
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
	pq2 := FindPendingQuestion(path)
	if pq2 == nil || pq2.ToolUseID != "toolu_1" {
		t.Fatalf("unchanged (size, mtime) should return the cached stale verdict, got %+v", pq2)
	}

	// bump the mtime: the cache must invalidate and rescan, now seeing the
	// answered content correctly.
	newMtime := mtime.Add(time.Second)
	if err := os.Chtimes(path, newMtime, newMtime); err != nil {
		t.Fatal(err)
	}
	if pq3 := FindPendingQuestion(path); pq3 != nil {
		t.Fatalf("changed mtime should invalidate the cache and rescan, got %+v", pq3)
	}
}

// --- deriveState ---

func TestDeriveState(t *testing.T) {
	cases := []struct {
		name   string
		j      *Job
		live   bool
		active bool
		want   string
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
			name: "started but cell not live: dead cell reads blocked",
			j:    &Job{Ready: true, State: "blocked"},
			live: false,
			want: "blocked",
		},
		{
			// a gone cell must never echo a stale "working" — that's how the
			// status-investigation self-decapitation sat invisible on the board
			name:   "started but cell not live, job file says working: still blocked",
			j:      &Job{Ready: true, State: "working"},
			live:   false,
			active: false, // irrelevant while !live
			want:   "blocked",
		},
		{
			name:   "live and active -> working",
			j:      &Job{Ready: true, State: "blocked"},
			live:   true,
			active: true,
			want:   "working",
		},
		{
			name:   "live and not active -> blocked",
			j:      &Job{Ready: true, State: "working"},
			live:   true,
			active: false,
			want:   "blocked",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := deriveState(c.j, c.live, c.active); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// --- cellActive ---

func boolPtr(b bool) *bool { return &b }

func TestCellActive(t *testing.T) {
	noQuestion := func(string) bool { return false }
	yesQuestion := func(string) bool { return true }

	cases := []struct {
		name         string
		sess         string
		peers        []*core.Peer
		members      []core.TeamMember
		paneQuestion func(string) bool
		skipPanes    map[string]bool
		want         bool
	}{
		{
			name:  "zeus running alone -> true",
			sess:  "cell-x",
			peers: []*core.Peer{{TmuxSession: "cell-x", Agent: "claude", Status: "running", Pane: "%1"}},
			want:  true,
		},
		{
			name:         "zeus running but pane shows an open question -> false",
			sess:         "cell-x",
			peers:        []*core.Peer{{TmuxSession: "cell-x", Agent: "claude", Status: "running", Pane: "%1"}},
			paneQuestion: yesQuestion,
			want:         false,
		},
		{
			name:  "codex running -> true",
			sess:  "cell-x",
			peers: []*core.Peer{{TmuxSession: "cell-x", Agent: "codex", Status: "running", Pane: "%1"}},
			want:  true,
		},
		{
			// zeus itself sitting on an open AskUserQuestion reads as
			// blocked-on-carl, not working, even with no teammates at all.
			name:  "zeus with Status question (open AskUserQuestion) -> false",
			sess:  "cell-x",
			peers: []*core.Peer{{TmuxSession: "cell-x", Agent: "claude", Status: "question", Pane: "%1"}},
			want:  false,
		},
		{
			// a nested subagent (Depth>0) in zeus' own pane/session counts
			// the same as any other running claude in the cell session.
			name: "nested subagent (Depth>0) running in zeus' session -> true",
			sess: "cell-x",
			peers: []*core.Peer{
				{TmuxSession: "cell-x", Agent: "claude", Status: "waiting", Pane: "%1", Depth: 0},
				{TmuxSession: "cell-x", Agent: "claude", Status: "running", Pane: "%1", Depth: 1},
			},
			want: true,
		},
		{
			// polling (mid poll-loop, pending ScheduleWakeup) votes the cell
			// active exactly like running: it's idle between ticks by
			// design, not genuinely waiting at its prompt.
			name:  "zeus polling (pending wakeup) -> true",
			sess:  "cell-x",
			peers: []*core.Peer{{TmuxSession: "cell-x", Agent: "claude", Status: "polling", Pane: "%1"}},
			want:  true,
		},
		{
			// same belt-and-braces question race applies to a polling agent
			// as to a running one.
			name:         "zeus polling but pane shows an open question -> false",
			sess:         "cell-x",
			peers:        []*core.Peer{{TmuxSession: "cell-x", Agent: "claude", Status: "polling", Pane: "%1"}},
			paneQuestion: yesQuestion,
			want:         false,
		},
		{
			// the member cross-check (runningByPane) only recognizes
			// "running" panes, not "polling" ones: zeus is the poller here,
			// never a teammate, so a polling occupant must not vote a
			// teammate pane active. Intentional — not a gap to fix.
			name:  "zeus waiting + tmux teammate whose pane is polling (not running) -> false",
			sess:  "cell-x",
			peers: []*core.Peer{{TmuxSession: "cell-x", Agent: "claude", Status: "waiting", Pane: "%1"}, {Pane: "%2", Status: "polling"}},
			members: []core.TeamMember{
				{AgentID: "team-lead@abc", TmuxPaneID: "leader"},
				{AgentID: "mate@1", BackendType: "tmux", TmuxPaneID: "%2"},
			},
			want: false,
		},
		{
			name:  "zeus waiting + tmux teammate with a running pane -> true",
			sess:  "cell-x",
			peers: []*core.Peer{{TmuxSession: "cell-x", Agent: "claude", Status: "waiting", Pane: "%1"}, {Pane: "%2", Status: "running"}},
			members: []core.TeamMember{
				{AgentID: "team-lead@abc", TmuxPaneID: "leader"},
				{AgentID: "mate@1", BackendType: "tmux", TmuxPaneID: "%2"},
			},
			want: true,
		},
		{
			// regression: an alive-but-idle teammate (pane exists, but the
			// occupying peer is sitting at its prompt, not "running") must
			// not vote the cell into working forever.
			name:  "zeus waiting + tmux teammate whose pane is idle (not running) -> false",
			sess:  "cell-x",
			peers: []*core.Peer{{TmuxSession: "cell-x", Agent: "claude", Status: "waiting", Pane: "%1"}, {Pane: "%2", Status: "waiting"}},
			members: []core.TeamMember{
				{AgentID: "team-lead@abc", TmuxPaneID: "leader"},
				{AgentID: "mate@1", BackendType: "tmux", TmuxPaneID: "%2"},
			},
			want: false,
		},
		{
			name:  "zeus waiting + tmux teammate whose pane is gone -> false",
			sess:  "cell-x",
			peers: []*core.Peer{{TmuxSession: "cell-x", Agent: "claude", Status: "waiting", Pane: "%1"}},
			members: []core.TeamMember{
				{AgentID: "team-lead@abc", TmuxPaneID: "leader"},
				{AgentID: "mate@1", BackendType: "tmux", TmuxPaneID: "%2"},
			},
			want: false,
		},
		{
			// in-process members have no pane to cross-check against, so an
			// absent isActive must not default to active — the opposite
			// default from tmux members, which do have an independent
			// liveness signal (their pane's status).
			name:  "zeus waiting + in-process member, isActive nil -> false",
			sess:  "cell-x",
			peers: []*core.Peer{{TmuxSession: "cell-x", Agent: "claude", Status: "waiting", Pane: "%1"}},
			members: []core.TeamMember{
				{AgentID: "team-lead@abc", TmuxPaneID: "leader"},
				{AgentID: "mate@1", BackendType: "in-process"},
			},
			want: false,
		},
		{
			name:  "zeus waiting + in-process member, isActive true -> true",
			sess:  "cell-x",
			peers: []*core.Peer{{TmuxSession: "cell-x", Agent: "claude", Status: "waiting", Pane: "%1"}},
			members: []core.TeamMember{
				{AgentID: "team-lead@abc", TmuxPaneID: "leader"},
				{AgentID: "mate@1", BackendType: "in-process", IsActive: boolPtr(true)},
			},
			want: true,
		},
		{
			name:  "in-process member, isActive false -> false",
			sess:  "cell-x",
			peers: []*core.Peer{{TmuxSession: "cell-x", Agent: "claude", Status: "waiting", Pane: "%1"}},
			members: []core.TeamMember{
				{AgentID: "team-lead@abc", TmuxPaneID: "leader"},
				{AgentID: "mate@1", BackendType: "in-process", IsActive: boolPtr(false)},
			},
			want: false,
		},
		{
			name:  "lead-only members -> false",
			sess:  "cell-x",
			peers: []*core.Peer{{TmuxSession: "cell-x", Agent: "claude", Status: "waiting", Pane: "%1"}},
			members: []core.TeamMember{
				{AgentID: "team-lead@abc", TmuxPaneID: "leader"},
			},
			want: false,
		},
		{
			name: "empty everything -> false",
			sess: "cell-x",
			want: false,
		},
		// --- side threads (skipPanes) ---
		{
			// THE correctness case: carl branches a thread precisely because
			// the job is blocked. A running thread alone must never vote the
			// job into "working" — that would hide the block he branched to
			// investigate, which is the whole reason skipPanes exists.
			name: "running thread pane alone -> false",
			sess: "cell-x",
			peers: []*core.Peer{
				{TmuxSession: "cell-x", Agent: "claude", Status: "waiting", Pane: "%1"},
				{TmuxSession: "cell-x", Agent: "claude", Status: "running", Pane: "%9"},
			},
			skipPanes: map[string]bool{"%9": true},
			want:      false,
		},
		{
			// the exclusion is per-pane, not per-session: zeus working while a
			// thread happens to be running is still a working job.
			name: "thread pane + running zeus -> true",
			sess: "cell-x",
			peers: []*core.Peer{
				{TmuxSession: "cell-x", Agent: "claude", Status: "running", Pane: "%1"},
				{TmuxSession: "cell-x", Agent: "claude", Status: "running", Pane: "%9"},
			},
			skipPanes: map[string]bool{"%9": true},
			want:      true,
		},
		{
			// point 2's roster is untouched by skipPanes (a thread is never a
			// team member): a running teammate still votes the cell active
			// even when the only other live pane is a thread.
			name: "thread pane + running tmux teammate -> true",
			sess: "cell-x",
			peers: []*core.Peer{
				{TmuxSession: "cell-x", Agent: "claude", Status: "waiting", Pane: "%1"},
				{TmuxSession: "cell-x", Agent: "claude", Status: "running", Pane: "%9"},
				{Pane: "%2", Status: "running"},
			},
			members: []core.TeamMember{
				{AgentID: "team-lead@abc", TmuxPaneID: "leader"},
				{AgentID: "mate@1", BackendType: "tmux", TmuxPaneID: "%2"},
			},
			skipPanes: map[string]bool{"%9": true},
			want:      true,
		},
		{
			// an EMPTY skip set must be byte-for-byte today's behavior: the
			// same peers that read false above read true with nothing skipped.
			name: "empty skip set skips nobody -> true",
			sess: "cell-x",
			peers: []*core.Peer{
				{TmuxSession: "cell-x", Agent: "claude", Status: "waiting", Pane: "%1"},
				{TmuxSession: "cell-x", Agent: "claude", Status: "running", Pane: "%9"},
			},
			skipPanes: map[string]bool{},
			want:      true,
		},
		{
			// a skip set naming some OTHER job's thread pane must not silence
			// this cell's own running agent.
			name: "skip set for an unrelated pane -> true",
			sess: "cell-x",
			peers: []*core.Peer{
				{TmuxSession: "cell-x", Agent: "claude", Status: "running", Pane: "%1"},
			},
			skipPanes: map[string]bool{"%42": true},
			want:      true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pq := c.paneQuestion
			if pq == nil {
				pq = noQuestion
			}
			if got := cellActive(c.sess, c.peers, c.members, pq, c.skipPanes); got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
			// every case must also hold with a nil skip set unless it
			// deliberately depends on one: nil is the no-store/file-mode
			// default, and it must never change a verdict on its own.
			if c.skipPanes == nil {
				if got := cellActive(c.sess, c.peers, c.members, pq, nil); got != c.want {
					t.Errorf("nil skip set: got %v, want %v", got, c.want)
				}
			}
		})
	}
}

// --- ListArtifacts ---

func TestListArtifactsMissingDir(t *testing.T) {
	j := &Job{Slug: "ghost-job-" + t.Name()}
	if got := ListArtifacts(j); got != nil {
		t.Errorf("missing artifacts dir should return nil, got %v", got)
	}
}

func TestListArtifactsSorted(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	j := &Job{Slug: "sorted-artifacts"}
	adir := CellArtifacts(j)
	if err := os.MkdirAll(adir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"b.md", "a.md", "c.md"} {
		if err := os.WriteFile(filepath.Join(adir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := ListArtifacts(j)
	want := []string{
		filepath.Join(adir, "a.md"),
		filepath.Join(adir, "b.md"),
		filepath.Join(adir, "c.md"),
	}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("got %v, want %v", got, want)
	}
}
