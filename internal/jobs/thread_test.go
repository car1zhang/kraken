package jobs

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"kraken/internal/core"
	"kraken/internal/store"
)

// --- names ---

func TestThreadNamesAreJobScopedAndPrefixed(t *testing.T) {
	j := &Job{Slug: "Side Threads"}
	if got, want := ThreadNamePrefix(j), "thread-side-threads-"; got != want {
		t.Errorf("ThreadNamePrefix = %q, want %q", got, want)
	}
	if got, want := ThreadAgentName(j, "t3"), "thread-side-threads-t3"; got != want {
		t.Errorf("ThreadAgentName = %q, want %q", got, want)
	}
	// the prefix must not be a prefix of zeus' own name, or the CellZeus
	// filter would exclude zeus itself and the cell would have no lead at all
	if strings.HasPrefix(ZeusName(j), ThreadNamePrefix(j)) {
		t.Errorf("zeus name %q starts with the thread prefix %q", ZeusName(j), ThreadNamePrefix(j))
	}
	// two jobs' threads must never collide: the skip/filter checks are prefix
	// matches, so a shared prefix would silence another job's agents
	other := &Job{Slug: "other-job"}
	if strings.HasPrefix(ThreadAgentName(other, "t1"), ThreadNamePrefix(j)) {
		t.Errorf("job %q's thread name matches job %q's prefix", other.Slug, j.Slug)
	}
}

// --- session uuid ---

var uuidV4Re = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// TestNewSessionUUIDShape pins the exact shape claude's --session-id accepts:
// lowercase 8-4-4-4-12 hex with the version-4 nibble and the RFC 4122 variant
// bits set. A malformed id is not a soft failure — claude refuses to start and
// the thread's window dies immediately.
func TestNewSessionUUIDShape(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 64; i++ {
		got, err := newSessionUUID()
		if err != nil {
			t.Fatalf("newSessionUUID: %v", err)
		}
		if !uuidV4Re.MatchString(got) {
			t.Fatalf("newSessionUUID = %q, want an RFC 4122 v4 uuid", got)
		}
		if seen[got] {
			t.Fatalf("newSessionUUID repeated %q — a collision would make two threads share a transcript", got)
		}
		seen[got] = true
	}
}

// --- tmux -P output parsing ---

func TestParseWindowTarget(t *testing.T) {
	cases := []struct {
		name, in, window, pane string
	}{
		{"normal", "cell-foo:3 (%17)", "cell-foo:3", "%17"},
		{"trailing newline", "cell-foo:0 (%1)\n", "cell-foo:0", "%1"},
		{"multi-digit pane", "cell-a-b:12 (%1234)", "cell-a-b:12", "%1234"},
		// a pane id that isn't a pane id must come back empty rather than be
		// passed on: every later verify-before-acting check targets it, and
		// garbage there would make those checks compare against nothing.
		{"no pane part", "cell-foo:3", "cell-foo:3", ""},
		{"malformed pane", "cell-foo:3 (17)", "cell-foo:3", ""},
		{"empty", "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			window, pane := parseWindowTarget(c.in)
			if window != c.window || pane != c.pane {
				t.Errorf("parseWindowTarget(%q) = (%q, %q), want (%q, %q)", c.in, window, pane, c.window, c.pane)
			}
		})
	}
}

// --- spawn commands ---

func threadCellDir(t *testing.T, j *Job) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(cellDir(j), 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestThreadSpawnCommandBranch pins the fork half of the contract: a brand-new
// session id kraken itself minted, --fork-session against zeus' session, the
// thread's own --name, and the brief + seed staged as files (never inline —
// see TestSpawnCommandStaysShortRegardlessOfInputSize for why).
func TestThreadSpawnCommandBranch(t *testing.T) {
	j := &Job{Slug: "branch-cmd"}
	threadCellDir(t, j)

	cmd, err := threadSpawnCommand(j, "t1", "why is it blocked", "explain the block", "", "ask", "",
		[]string{"--session-id", "11111111-2222-4333-8444-555555555555", "--fork-session", "-r", "zeus-sess"})
	if err != nil {
		t.Fatalf("threadSpawnCommand: %v", err)
	}
	for _, want := range []string{
		"--name thread-branch-cmd-t1",
		"--session-id 11111111-2222-4333-8444-555555555555",
		"--fork-session",
		"-r zeus-sess",
		"--append-system-prompt",
		threadBriefPath(j, "t1"),
		threadPromptPath(j, "t1"),
		"$(cat ",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("branch command %q missing %q", cmd, want)
		}
	}
	// `--` must stay last: everything after it is claude's initial prompt, so
	// a brief flag appended after it would be swallowed into the seed text
	if i, k := strings.Index(cmd, " -- "), strings.Index(cmd, "--append-system-prompt"); i < k {
		t.Errorf("branch command puts -- before --append-system-prompt: %q", cmd)
	}
	// the staged brief must be the THREAD's, not zeus'
	body, err := os.ReadFile(threadBriefPath(j, "t1"))
	if err != nil {
		t.Fatalf("read staged thread brief: %v", err)
	}
	if !strings.Contains(string(body), "side thread t1") {
		t.Errorf("staged brief is not the thread brief: %q", string(body))
	}
	if _, err := os.Stat(zeusBriefPath(j)); err == nil {
		t.Error("threadSpawnCommand staged into zeus' brief path — a thread must never overwrite zeus' spawn inputs")
	}
	seed, err := os.ReadFile(threadPromptPath(j, "t1"))
	if err != nil {
		t.Fatalf("read staged seed: %v", err)
	}
	// carl's question leads; the standing prompt guard rides under it
	// (TestThreadSeedCarriesTheGuard owns the guard's wording)
	if !strings.HasPrefix(string(seed), "explain the block") {
		t.Errorf("staged seed = %q, want it to open with the seed text", string(seed))
	}
	if string(seed) != threadSeed("explain the block") {
		t.Errorf("staged seed = %q, want the guarded seed", string(seed))
	}
}

// TestThreadSpawnCommandRevive pins the resume half: the SAME session id via
// -r and NO --fork-session (that would branch a third session off the thread),
// with the brief re-passed — a resumed session does not retain an appended
// system prompt, and a revive without it comes back as a plain claude holding
// an implementer's history.
func TestThreadSpawnCommandRevive(t *testing.T) {
	j := &Job{Slug: "revive-cmd"}
	threadCellDir(t, j)

	cmd, err := threadSpawnCommand(j, "t2", "the title", "", "", "ask", "", []string{"-r", "thread-sess"})
	if err != nil {
		t.Fatalf("threadSpawnCommand: %v", err)
	}
	if strings.Contains(cmd, "--fork-session") {
		t.Errorf("revive command must not fork the thread's own session: %q", cmd)
	}
	if strings.Contains(cmd, "--session-id") {
		t.Errorf("revive command must resume, not name a new session: %q", cmd)
	}
	for _, want := range []string{"--name thread-revive-cmd-t2", "-r thread-sess", "--append-system-prompt", threadBriefPath(j, "t2")} {
		if !strings.Contains(cmd, want) {
			t.Errorf("revive command %q missing %q", cmd, want)
		}
	}
	// no seed on revive: an empty input must produce no `--` at all, not an
	// empty file and a `$(cat …)` expanding to nothing
	if strings.Contains(cmd, " -- ") {
		t.Errorf("revive command carries an empty prompt: %q", cmd)
	}
	if _, err := os.Stat(threadPromptPath(j, "t2")); err == nil {
		t.Error("revive staged a seed file for an empty seed")
	}
	body, err := os.ReadFile(threadBriefPath(j, "t2"))
	if err != nil {
		t.Fatalf("read staged thread brief: %v", err)
	}
	if !strings.Contains(string(body), "the title") {
		t.Errorf("revived brief lost the thread's title: %q", string(body))
	}
}

// TestThreadSpawnInputsAreThreadScoped pins the per-tid staging paths. A shared
// path would be a real race: the pane's shell reads the file milliseconds AFTER
// tmux returns, so a second branch could overwrite the first thread's brief
// before its claude ever read it, handing one thread another's identity.
func TestThreadSpawnInputsAreThreadScoped(t *testing.T) {
	j := &Job{Slug: "scoped"}
	threadCellDir(t, j)

	if threadBriefPath(j, "t1") == threadBriefPath(j, "t2") {
		t.Fatal("two threads share one brief path")
	}
	if threadPromptPath(j, "t1") == threadPromptPath(j, "t2") {
		t.Fatal("two threads share one seed path")
	}
	// staged plumbing lives beside zeus' spawn inputs in the cell dir, never
	// in the artifacts dir carl's board reads
	for _, p := range []string{threadBriefPath(j, "t1"), threadPromptPath(j, "t1")} {
		if strings.HasPrefix(p, CellArtifacts(j)) {
			t.Errorf("%q is inside the artifacts dir — spawn inputs are plumbing, not deliverables", p)
		}
		if !strings.HasPrefix(p, cellDir(j)) {
			t.Errorf("%q is not in the cell dir", p)
		}
	}
}

// --- guards ---

func TestZeusForkTargetRefusals(t *testing.T) {
	j := &Job{Slug: "guard"}
	sess := CellSession(j)
	cases := []struct {
		name  string
		peers []*core.Peer
		want  string
	}{
		{
			name:  "no peers at all",
			peers: nil,
			want:  "has no live zeus to branch from",
		},
		{
			name:  "no claude in the cell session",
			peers: []*core.Peer{{TmuxSession: "somewhere-else", Agent: "claude", Depth: 0, Pane: "%1", SessionID: "s"}},
			want:  "has no live zeus to branch from",
		},
		{
			// only threads are live (zeus died): the CellZeus filter means
			// there is no lead to fork, NOT that a thread gets elected
			name:  "only a thread is live",
			peers: []*core.Peer{{TmuxSession: sess, Agent: "claude", Depth: 0, Pane: "%9", Name: ThreadAgentName(j, "t1"), SessionID: "thread-sess"}},
			want:  "has no live zeus to branch from",
		},
		{
			name:  "zeus with no session id",
			peers: []*core.Peer{{TmuxSession: sess, Agent: "claude", Depth: 0, Pane: "%1", Name: ZeusName(j)}},
			want:  "no session id resolved",
		},
		{
			// THE dangerous one: a guessed id could fork some other claude's
			// session entirely
			name:  "zeus with a guessed session id",
			peers: []*core.Peer{{TmuxSession: sess, Agent: "claude", Depth: 0, Pane: "%1", Name: ZeusName(j), SessionID: "s", SessionIDGuessed: true}},
			want:  "is a guess",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := zeusForkTarget(j, c.peers)
			if err == nil {
				t.Fatalf("want a refusal, got peer %+v", got)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("refusal = %q, want it to mention %q", err, c.want)
			}
		})
	}

	ok := []*core.Peer{{TmuxSession: sess, Agent: "claude", Depth: 0, Pane: "%1", Name: ZeusName(j), SessionID: "real-sess"}}
	got, err := zeusForkTarget(j, ok)
	if err != nil {
		t.Fatalf("healthy zeus refused: %v", err)
	}
	if got.SessionID != "real-sess" {
		t.Errorf("forked from %q, want real-sess", got.SessionID)
	}
}

func TestThreadTargetVerifiesTheThreadStillHoldsThePane(t *testing.T) {
	j := &Job{Slug: "verify"}
	row := store.Thread{JobID: 1, TID: "t1", Pane: "%7", State: "live"}
	name := ThreadAgentName(j, "t1")

	cases := []struct {
		name  string
		peers []*core.Peer
		ok    bool
	}{
		{"thread holds its pane", []*core.Peer{{Pane: "%7", Name: name, Agent: "claude"}}, true},
		{"pane empty", nil, false},
		{"pane recycled by another claude", []*core.Peer{{Pane: "%7", Name: "zeus-verify", Agent: "claude"}}, false},
		{"another job's thread on the pane", []*core.Peer{{Pane: "%7", Name: "thread-other-t1", Agent: "claude"}}, false},
		{"right name, wrong pane", []*core.Peer{{Pane: "%8", Name: name, Agent: "claude"}}, false},
		{"pane is a shell", []*core.Peer{{Pane: "%7", Name: name}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, err := threadTarget(j, row, c.peers, "revive it first")
			if c.ok {
				if err != nil {
					t.Fatalf("want the thread's peer, got refusal %v", err)
				}
				if p.Pane != "%7" {
					t.Errorf("got pane %q", p.Pane)
				}
				return
			}
			if err == nil {
				t.Fatalf("want a refusal, got peer %+v", p)
			}
			for _, want := range []string{"no longer holds thread t1", j.Slug, "revive it first"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal = %q, want it to mention %q", err, want)
				}
			}
		})
	}

	// a live row with no pane at all can never be found again
	if _, err := threadTarget(j, store.Thread{TID: "t1"}, nil, "refusing to kill it"); err == nil {
		t.Error("want a refusal for a row with no pane")
	}
}

// TestLiveThreadClaudeRefusesADoubleWriter pins ReviveThread's guard. Keying it
// on the recorded PANE was the bug: the pane is the one field a close clears,
// so a row closed while its claude was still alive (a reconcile that fired too
// early) would pass a pane-keyed guard and get a SECOND `claude -r` on one
// session id — two claudes writing one transcript.
func TestLiveThreadClaudeRefusesADoubleWriter(t *testing.T) {
	j := &Job{Slug: "double-writer"}
	sess := CellSession(j)
	row := store.Thread{TID: "t1", Pane: "%7", SessionID: "thread-sess", State: "live"}
	cleared := store.Thread{TID: "t1", SessionID: "thread-sess", State: "closed"} // pane cleared by a close

	cases := []struct {
		name  string
		row   store.Thread
		peers []*core.Peer
		alive bool
	}{
		{"named thread on its own pane", row,
			[]*core.Peer{{Pane: "%7", Name: ThreadAgentName(j, "t1"), Agent: "claude", TmuxSession: sess}}, true},
		// THE regression: the row lost its pane, but the claude is still up
		// somewhere in the cell session under the thread's session id
		{"row's pane cleared, claude still running", cleared,
			[]*core.Peer{{Pane: "%9", Name: ThreadAgentName(j, "t1"), Agent: "claude", SessionID: "thread-sess", TmuxSession: sess}}, true},
		// still starting: no name AND no session id yet, only a pane
		{"claude still starting in the recorded pane", row,
			[]*core.Peer{{Pane: "%7", Agent: "claude", Depth: 0, TmuxSession: sess}}, true},
		{"nothing running", row, nil, false},
		{"pane recycled by a stranger", row,
			[]*core.Peer{{Pane: "%7", Name: "zeus-double-writer", Agent: "claude", TmuxSession: sess}}, false},
		// zeus' session id is often GUESSED from cwd + newest transcript, which
		// in a cell may be the thread's own — matching it would wedge revive shut
		{"zeus guessing the thread's session id", cleared,
			[]*core.Peer{{Pane: "%1", Name: ZeusName(j), Agent: "claude", SessionID: "thread-sess", SessionIDGuessed: true, TmuxSession: sess}}, false},
		{"same session id in another cell", cleared,
			[]*core.Peer{{Pane: "%3", Agent: "claude", SessionID: "thread-sess", TmuxSession: "cell-other"}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := liveThreadClaude(j, c.peers, c.row)
			if (got != nil) != c.alive {
				t.Errorf("liveThreadClaude = %+v, want alive=%v", got, c.alive)
			}
		})
	}
}

// TestThreadVerbsRefuseWithoutAStore pins that thread verbs never silently
// no-op without a store: threads live only in kraken.db, so a caller without
// a wired store (the viewer, a bare CLI process) must hear about it.
func TestThreadVerbsRefuseWithoutAStore(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	UseStore(nil)
	j := &Job{Slug: "no-store", ID: 0}
	if _, err := threadStore(j); err == nil {
		t.Fatal("want a refusal with no store wired")
	} else if !strings.Contains(err.Error(), "store") {
		t.Errorf("refusal = %q, want it to mention the store", err)
	}
	if err := RenameThread(j, "t1", "x"); err == nil {
		t.Error("RenameThread succeeded with no store")
	}
	if err := KillThread(j, "t1"); err == nil {
		t.Error("KillThread succeeded with no store")
	}
	// the daemon-side passes stay silent instead: a tick and a teardown must
	// not fail just because this deployment has no store
	if err := ReconcileThreads(j, nil, nil); err != nil {
		t.Errorf("ReconcileThreads with no store = %v, want nil", err)
	}
	if err := CloseThreadsForTeardown(j); err != nil {
		t.Errorf("CloseThreadsForTeardown with no store = %v, want nil", err)
	}
}

func TestThreadStoreRefusesAFileBackedJob(t *testing.T) {
	useTestStore(t)
	if _, err := threadStore(&Job{Slug: "file-job", ID: 0}); err == nil {
		t.Fatal("want a refusal for a job with no store row")
	} else if !strings.Contains(err.Error(), "no store row") {
		t.Errorf("refusal = %q", err)
	}
}

// TestBranchThreadRefusesADeadCell covers the first and cheapest refusal:
// nothing to fork from, so nothing is created — the check lands before any
// discovery, tmux window, or DB write.
func TestBranchThreadRefusesADeadCell(t *testing.T) {
	st := useTestStore(t)
	sr := mustCreateJob(t, st, store.Job{Slug: "dead-cell-" + strings.ToLower(t.Name()), Repo: "kraken", State: "working"})
	j := &Job{ID: sr.ID, Slug: sr.Slug}

	_, err := BranchThread(j, "why", "", "")
	if err == nil {
		t.Fatal("want a refusal branching off a dead cell")
	}
	if !strings.Contains(err.Error(), "no live cell to branch") {
		t.Errorf("refusal = %q, want the dead-cell message", err)
	}
	threads, lerr := st.ListThreads(j.ID)
	if lerr != nil {
		t.Fatal(lerr)
	}
	if len(threads) != 0 {
		t.Errorf("a refused branch left %d thread rows behind", len(threads))
	}
}

// TestConcurrentBranchesNeverShareATID walks the F4 failure end to end at this
// layer: two branches of one job, reserved the way BranchThread now reserves
// (AddThreadNext, atomic) BEFORE either stages a file. With a separate
// NextThreadID + AddThread and a tmux call between them, both branches got t3,
// both wrote thread-t3-brief.md, and the surviving thread was briefed with the
// other request's title while its row recorded what carl actually typed.
func TestConcurrentBranchesNeverShareATID(t *testing.T) {
	st := useTestStore(t)
	sr := mustCreateJob(t, st, store.Job{Slug: "concurrent-branch", Repo: "kraken", State: "working"})
	j := &Job{ID: sr.ID, Slug: sr.Slug}
	if err := os.MkdirAll(cellDir(j), 0o755); err != nil {
		t.Fatal(err)
	}

	titles := []string{"why is it blocked", "what did the daemon do"}
	rows := make([]store.Thread, len(titles))
	var wg sync.WaitGroup
	for i, title := range titles {
		wg.Add(1)
		go func(i int, title string) {
			defer wg.Done()
			got, err := st.AddThreadNext(store.Thread{JobID: j.ID, Title: title, SessionID: "s" + title, State: "live"})
			if err != nil {
				t.Errorf("AddThreadNext(%q): %v", title, err)
				return
			}
			rows[i] = got
		}(i, title)
	}
	wg.Wait()

	if rows[0].TID == rows[1].TID {
		t.Fatalf("two concurrent branches both got %q", rows[0].TID)
	}
	// each thread's staged brief must carry ITS OWN title: the staged paths are
	// keyed by tid, so distinct tids are exactly what keeps them apart
	for i, r := range rows {
		if _, err := threadSpawnCommand(j, r.TID, titles[i], "seed "+titles[i], "", "ask", "", []string{"-r", "x"}); err != nil {
			t.Fatalf("threadSpawnCommand: %v", err)
		}
	}
	for i, r := range rows {
		body, err := os.ReadFile(threadBriefPath(j, r.TID))
		if err != nil {
			t.Fatalf("read staged brief: %v", err)
		}
		if !strings.Contains(string(body), titles[i]) {
			t.Errorf("thread %s was briefed with another request's title: %q", r.TID, string(body))
		}
	}
}

// TestReleaseReservedThreadClosesTheRow pins the other half of the reservation:
// a branch that dies after reserving must leave nothing claiming to be live —
// a live row with no pane would sit in the roll call's skip set forever — and
// must not hand the tid back out, since tids name forked sessions.
func TestReleaseReservedThreadClosesTheRow(t *testing.T) {
	st := useTestStore(t)
	sr := mustCreateJob(t, st, store.Job{Slug: "release-reserved", Repo: "kraken", State: "working"})
	j := &Job{ID: sr.ID, Slug: sr.Slug}

	reserved, err := st.AddThreadNext(store.Thread{JobID: j.ID, Title: "doomed", SessionID: "s1", State: "live"})
	if err != nil {
		t.Fatal(err)
	}
	releaseReservedThread(st, j, reserved.TID)

	got, err := st.GetThread(j.ID, reserved.TID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "closed" {
		t.Errorf("released row = %+v, want closed", got)
	}
	next, err := st.NextThreadID(j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if next == reserved.TID {
		t.Errorf("NextThreadID handed out %q again — a tid names a forked session, never two", next)
	}
}

// --- revive: claim before spawn ---

// reviveFixture stands a job + one CLOSED thread row up, with the cell dir in
// place so the staging step can actually write, and returns the row.
func reviveFixture(t *testing.T, st *store.Store, slug string) (*Job, store.Thread) {
	t.Helper()
	sr := mustCreateJob(t, st, store.Job{Slug: slug, Repo: "kraken", State: "working"})
	j := &Job{ID: sr.ID, Slug: sr.Slug}
	if err := os.MkdirAll(cellDir(j), 0o755); err != nil {
		t.Fatal(err)
	}
	row, err := st.AddThreadNext(store.Thread{JobID: j.ID, Title: "why blocked", SessionID: "sess-1", State: "live"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CloseThread(j.ID, row.TID, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { threadSpawns.Delete(threadSpawnKey(j.ID, row.TID)) })
	return j, row
}

// countingOpener is a windowOpener that records how many times it was called,
// so a test can assert the thing that matters most here: a LOST claim spawns
// nothing at all.
func countingOpener(out string, err error) (windowOpener, *int) {
	n := 0
	return func(string, string, string, []string, []string) (string, error) {
		n++
		return out, err
	}, &n
}

// TestReviveClaimsTheRowBeforeSpawning is the TOCTOU fix, from the loser's
// side. The discovery scan in ReviveThread is a snapshot, so two concurrent
// revives of one tid both pass it; the claim is what actually arbitrates. The
// loser must return a refusal having opened NO window and staged NO brief —
// the alternative is a second `claude -r` on one session id, two writers on one
// transcript, which nothing can unpick afterwards.
func TestReviveClaimsTheRowBeforeSpawning(t *testing.T) {
	st := useTestStore(t)
	j, row := reviveFixture(t, st, "revive-claim")

	// the winner: claims the row and spawns
	open, calls := countingOpener("cell-x:3 (%17)", nil)
	got, err := reviveClaimed(st, j, row, CellSession(j), t.TempDir(), open)
	if err != nil {
		t.Fatalf("reviveClaimed: %v", err)
	}
	if *calls != 1 {
		t.Errorf("winner opened %d windows, want exactly 1", *calls)
	}
	if got.State != "live" || got.Pane != "%17" || got.Window != "cell-x:3" || got.ClosedAt != 0 {
		t.Errorf("returned row = %+v, want live at %%17/cell-x:3", got)
	}
	stored, err := st.GetThread(j.ID, row.TID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != "live" || stored.Pane != "%17" || stored.Window != "cell-x:3" {
		t.Errorf("stored row = %+v, want the pane recorded", stored)
	}
	if stored.SessionID != row.SessionID {
		t.Errorf("revive changed the session id: %+v — the row is a pointer to one transcript", stored)
	}

	// the loser: same row, still live. Nothing may be spawned.
	brief := threadBriefPath(j, row.TID)
	if err := os.Remove(brief); err != nil {
		t.Fatal(err)
	}
	loserOpen, loserCalls := countingOpener("cell-x:4 (%18)", nil)
	if _, err := reviveClaimed(st, j, row, CellSession(j), t.TempDir(), loserOpen); err == nil {
		t.Fatal("the second revive succeeded — two claudes would now share one session id")
	} else if !strings.Contains(err.Error(), "already live or being revived") {
		t.Errorf("loser's refusal = %q", err)
	}
	if *loserCalls != 0 {
		t.Errorf("a lost claim opened %d windows, want 0", *loserCalls)
	}
	if _, err := os.Stat(brief); !os.IsNotExist(err) {
		t.Errorf("a lost claim staged %s — it must not touch the winner's spawn inputs", brief)
	}
	// and the winner's row is exactly as it was: the loser wrote nothing
	after, err := st.GetThread(j.ID, row.TID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Pane != "%17" || after.Window != "cell-x:3" {
		t.Errorf("the loser overwrote the winner's coordinates: %+v", after)
	}
}

// TestReviveUndoesItsClaimOnFailure is the other half of the ordering: once the
// claim is taken the row is LIVE, so every failure after it must put the row
// back to closed. A row left live with no pane sits in the roll call's skip set
// with no window to reconcile it away, and no revive can ever reclaim it.
func TestReviveUndoesItsClaimOnFailure(t *testing.T) {
	st := useTestStore(t)

	cases := []struct {
		name string
		out  string
		err  error
		want string
	}{
		{"tmux refused to open the window", "", errors.New("no server running"), "open thread window"},
		// tmux answered, but not with a pane id — nothing to target later, so
		// the spawn counts as failed
		{"no pane id in tmux's reply", "", nil, "no pane id"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			j, row := reviveFixture(t, st, "revive-undo-"+strings.ReplaceAll(c.name, " ", "-"))
			open, calls := countingOpener(c.out, c.err)
			if _, err := reviveClaimed(st, j, row, CellSession(j), t.TempDir(), open); err == nil {
				t.Fatal("want a failure")
			} else if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error = %q, want it to mention %q", err, c.want)
			}
			if *calls != 1 {
				t.Errorf("opener called %d times, want 1", *calls)
			}
			got, err := st.GetThread(j.ID, row.TID)
			if err != nil {
				t.Fatal(err)
			}
			if got.State != "closed" || got.Pane != "" {
				t.Fatalf("row after a failed revive = %+v, want it closed and revivable again", got)
			}
			// revivable again means exactly that: a retry can claim it
			retry, retryCalls := countingOpener("cell-x:9 (%99)", nil)
			if _, err := reviveClaimed(st, j, row, CellSession(j), t.TempDir(), retry); err != nil {
				t.Fatalf("retry after a failed revive: %v", err)
			}
			if *retryCalls != 1 {
				t.Errorf("retry opened %d windows, want 1", *retryCalls)
			}
		})
	}
}

func TestThreadRowVerbsReportAnUnknownTID(t *testing.T) {
	st := useTestStore(t)
	sr := mustCreateJob(t, st, store.Job{Slug: "unknown-tid", Repo: "kraken", State: "working"})
	j := &Job{ID: sr.ID, Slug: sr.Slug}

	if err := KillThread(j, "t9"); err == nil {
		t.Error("KillThread succeeded for an unknown tid")
	}
	if err := SendToThread(j, "t9", "hi"); err == nil {
		t.Error("SendToThread succeeded for an unknown tid")
	}
	if _, err := ReviveThread(j, "t9"); err == nil {
		t.Error("ReviveThread succeeded for an unknown tid")
	}
	if err := RenameThread(j, "t9", "new title"); err == nil {
		t.Error("RenameThread succeeded for an unknown tid")
	}
	if err := RenameThread(j, "t9", "  "); err == nil {
		t.Error("RenameThread accepted a blank title")
	}
}

// --- reconcile ---

func TestStaleThreadTIDs(t *testing.T) {
	j := &Job{Slug: "recon"}
	prefix := ThreadNamePrefix(j)
	sess := CellSession(j)
	now := time.Now()
	// CreatedAt far enough back that the grace window has expired: these cases
	// are all about what discovery says, not about the boot race below
	old := now.Add(-time.Hour).UnixMilli()
	live := func(tid, pane string) store.Thread {
		return store.Thread{TID: tid, Pane: pane, State: "live", CreatedAt: old}
	}
	held := func(tid, pane string) *core.Peer {
		return &core.Peer{Pane: pane, Name: prefix + tid, Agent: "claude"}
	}
	// what a thread's pane looks like in its first seconds: claude is up, but
	// its session file isn't written, so discovery has no name for it
	starting := func(pane string) *core.Peer {
		return &core.Peer{Pane: pane, Agent: "claude", Depth: 0, TmuxSession: sess}
	}

	cases := []struct {
		name    string
		threads []store.Thread
		peers   []*core.Peer
		want    []string
	}{
		{
			name:    "all held -> nothing stale",
			threads: []store.Thread{live("t1", "%1"), live("t2", "%2")},
			peers:   []*core.Peer{held("t1", "%1"), held("t2", "%2")},
		},
		{
			name:    "window died -> stale",
			threads: []store.Thread{live("t1", "%1"), live("t2", "%2")},
			peers:   []*core.Peer{held("t1", "%1")},
			want:    []string{"t2"},
		},
		{
			// tmux recycles pane ids: the row's pane exists again, but
			// somebody else is in it
			name:    "pane recycled by another agent -> stale",
			threads: []store.Thread{live("t1", "%1")},
			peers:   []*core.Peer{{Pane: "%1", Name: ZeusName(j), Agent: "claude"}},
			want:    []string{"t1"},
		},
		{
			name:    "live row with no pane -> stale",
			threads: []store.Thread{live("t1", "")},
			want:    []string{"t1"},
		},
		{
			// already-closed rows are never re-closed: CloseThread would
			// restamp closed_at on every single tick
			name:    "closed rows are left alone",
			threads: []store.Thread{{TID: "t1", State: "closed"}},
		},
		{
			name: "nothing at all",
		},
		{
			// THE bug this guard exists for: claude is alive in the thread's
			// pane and simply hasn't written its session file yet, so it has no
			// Name to match. Closing here would clear the pane, put a live
			// thread back into the roll call, orphan kill/send, and let a
			// revive start a second claude on the one session id.
			name:    "pane held by a still-starting claude -> not stale",
			threads: []store.Thread{live("t1", "%1")},
			peers:   []*core.Peer{starting("%1")},
		},
		{
			// a nameless claude in ANOTHER session is not this thread: the pane
			// was recycled by a window somewhere else entirely
			name:    "nameless claude in another session -> stale",
			threads: []store.Thread{live("t1", "%1")},
			peers:   []*core.Peer{{Pane: "%1", Agent: "claude", Depth: 0, TmuxSession: "somewhere-else"}},
			want:    []string{"t1"},
		},
		{
			// the daemon ticks every 15s; a branch landing seconds before one
			// is invisible to discovery and must survive it
			name:    "branched seconds ago -> not stale",
			threads: []store.Thread{{TID: "t1", Pane: "%1", State: "live", CreatedAt: now.Add(-5 * time.Second).UnixMilli()}},
		},
		{
			// AddThreadNext reserves the row BEFORE the window exists, so a row
			// with no pane is a normal mid-branch state, not a dead thread
			name:    "reserved row with no pane yet -> not stale",
			threads: []store.Thread{{TID: "t1", State: "live", CreatedAt: now.Add(-time.Second).UnixMilli()}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := staleThreadTIDs(j, c.threads, c.peers, now)
			if strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Errorf("staleThreadTIDs = %v, want %v", got, c.want)
			}
		})
	}
}

// TestStaleThreadTIDsHonorsAReviveGrace covers the boot race the row itself
// cannot describe: a revive deliberately leaves CreatedAt alone (it resumes an
// old session, it does not create one), so a thread revived one second ago
// carries a timestamp from days back and would be closed on the very next
// tick — while its claude was still starting.
func TestStaleThreadTIDsHonorsAReviveGrace(t *testing.T) {
	j := &Job{Slug: "revive-grace", ID: 4242}
	row := store.Thread{JobID: j.ID, TID: "t1", Pane: "%1", State: "live",
		CreatedAt: time.Now().Add(-72 * time.Hour).UnixMilli()}
	now := time.Now()

	if got := staleThreadTIDs(j, []store.Thread{row}, nil, now); len(got) != 1 {
		t.Fatalf("staleThreadTIDs = %v, want t1 stale before any spawn is recorded", got)
	}
	noteThreadSpawn(j.ID, row.TID)
	t.Cleanup(func() { threadSpawns.Delete(threadSpawnKey(j.ID, row.TID)) })
	if got := staleThreadTIDs(j, []store.Thread{row}, nil, now); len(got) != 0 {
		t.Errorf("staleThreadTIDs = %v, want a just-revived thread left alone", got)
	}
	// the grace is a window, not an amnesty: once it passes, a thread nothing
	// can find is closed like any other
	if got := staleThreadTIDs(j, []store.Thread{row}, nil, now.Add(threadStartGrace+time.Minute)); len(got) != 1 {
		t.Errorf("staleThreadTIDs = %v, want t1 stale once the grace expires", got)
	}
}

// TestThreadSnapshotTrusted is the fleet-wide-close guard: listTmuxPanes
// swallows exec errors and returns nothing, and ReconcileThreads is the only
// consumer of that snapshot that WRITES off it — so an empty snapshot must
// never be read as "every thread on the machine is gone".
func TestThreadSnapshotTrusted(t *testing.T) {
	sess := "cell-foo"
	inSess := []*core.Peer{{TmuxSession: sess, Pane: "%1"}}
	elsewhere := []*core.Peer{{TmuxSession: "other", Pane: "%2"}}

	cases := []struct {
		name     string
		all      []*core.Peer
		live     map[string]bool
		emptyFor time.Duration
		trust    bool
	}{
		{"healthy snapshot of a live cell", inSess, map[string]bool{sess: true}, 0, true},
		{"cell session really is gone", elsewhere, map[string]bool{"other": true}, 0, true},
		// the incident: one failed `tmux list-panes` while sessions still exist
		{"no panes but sessions exist", nil, map[string]bool{sess: true}, 0, false},
		{"panes but no sessions", inSess, nil, 0, false},
		// scoped version of the same failure: the session is live, yet not one
		// of its panes made it into the snapshot
		{"live session missing from the snapshot", elsewhere, map[string]bool{sess: true, "other": true}, 0, false},
		// the both-empty hole the cross-check cannot see: a dead tmux server and
		// a double exec failure are byte-identical, so only persistence tells
		// them apart. One tick of nothing must close nothing.
		{"first empty tick", nil, nil, 0, false},
		{"still empty, but not for long enough", nil, nil, emptySnapshotConfirm - time.Second, false},
		{"tmux server is gone entirely", nil, nil, emptySnapshotConfirm, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := threadSnapshotTrusted(sess, c.all, c.live, c.emptyFor); got != c.trust {
				t.Errorf("threadSnapshotTrusted = %v, want %v", got, c.trust)
			}
		})
	}
}

// TestNoteEmptySnapshotLatchesTheRun pins the cross-tick memory the both-empty
// case needs: the run starts at the FIRST empty snapshot (every other job in
// that same tick reads the same start back, so a fleet of jobs can't confirm a
// hiccup by itself), and any snapshot with something in it clears it.
func TestNoteEmptySnapshotLatchesTheRun(t *testing.T) {
	resetEmptySnapshotLatch(t)
	now := time.Now()

	if got := noteEmptySnapshot(true, now); got != 0 {
		t.Errorf("first empty tick = %v, want a zero-length run", got)
	}
	// same tick, second job: the run must not restart, or it could never grow
	if got := noteEmptySnapshot(true, now); got != 0 {
		t.Errorf("second job in the same tick = %v, want 0", got)
	}
	if got := noteEmptySnapshot(true, now.Add(45*time.Second)); got != 45*time.Second {
		t.Errorf("run length = %v, want 45s measured from the first empty tick", got)
	}
	// tmux came back: the run is over, and the next empty tick starts a new one
	if got := noteEmptySnapshot(false, now.Add(50*time.Second)); got != 0 {
		t.Errorf("non-empty snapshot = %v, want 0", got)
	}
	if got := noteEmptySnapshot(true, now.Add(60*time.Second)); got != 0 {
		t.Errorf("first empty tick after a recovery = %v, want a fresh run", got)
	}
}

// resetEmptySnapshotLatch clears the package-level empty-snapshot latch before
// and after a test, so a test that leaves it armed can't hand a later test a
// snapshot it would wrongly trust (or wrongly distrust).
func resetEmptySnapshotLatch(t *testing.T) {
	t.Helper()
	clear := func() {
		emptySnapshotSince.Lock()
		defer emptySnapshotSince.Unlock()
		emptySnapshotSince.at = time.Time{}
	}
	clear()
	t.Cleanup(clear)
}

// backdateEmptySnapshotLatch pretends the current empty run started long
// enough ago to be confirmed — what a second tick 15s later would produce,
// without a test sleeping for it.
func backdateEmptySnapshotLatch(t *testing.T) {
	t.Helper()
	emptySnapshotSince.Lock()
	defer emptySnapshotSince.Unlock()
	emptySnapshotSince.at = time.Now().Add(-2 * emptySnapshotConfirm)
}

// TestReconcileSkipsAnUntrustedSnapshot is the same guard at the write layer:
// the rows must survive a tick whose discovery came back empty.
func TestReconcileSkipsAnUntrustedSnapshot(t *testing.T) {
	st := useTestStore(t)
	resetEmptySnapshotLatch(t)
	sr := mustCreateJob(t, st, store.Job{Slug: "untrusted-snap", Repo: "kraken", State: "working"})
	j := &Job{ID: sr.ID, Slug: sr.Slug}
	if err := st.AddThread(store.Thread{JobID: j.ID, TID: "t1", SessionID: "s1", Pane: "%1", State: "live",
		CreatedAt: time.Now().Add(-time.Hour).UnixMilli()}); err != nil {
		t.Fatal(err)
	}

	// tmux hiccup: no peers at all, but the cell session is still there
	if err := ReconcileThreads(j, nil, map[string]bool{CellSession(j): true}); err != nil {
		t.Fatalf("ReconcileThreads: %v", err)
	}
	got, err := st.GetThread(j.ID, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "live" || got.Pane != "%1" {
		t.Fatalf("t1 = %+v, want it untouched — an empty snapshot is a failed tmux call, not a dead fleet", got)
	}

	// worse hiccup: BOTH tmux calls came back empty in one tick, which is
	// indistinguishable from a dead server — so a single such tick closes
	// nothing either, no matter how many jobs it reconciles
	for i := 0; i < 3; i++ {
		if err := ReconcileThreads(j, nil, nil); err != nil {
			t.Fatalf("ReconcileThreads: %v", err)
		}
	}
	got, err = st.GetThread(j.ID, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "live" {
		t.Fatalf("t1 = %+v, want it untouched — one empty tick is a failed tmux call, not a dead fleet", got)
	}

	// a genuinely dead server stays empty, so once the emptiness has persisted
	// the close does happen
	backdateEmptySnapshotLatch(t)
	if err := ReconcileThreads(j, nil, nil); err != nil {
		t.Fatalf("ReconcileThreads: %v", err)
	}
	got, err = st.GetThread(j.ID, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "closed" {
		t.Errorf("t1 = %+v, want closed once tmux and discovery agree it's gone", got)
	}
}

func TestReconcileAndTeardownCloseLiveRows(t *testing.T) {
	st := useTestStore(t)
	sr := mustCreateJob(t, st, store.Job{Slug: "recon-store", Repo: "kraken", State: "working"})
	j := &Job{ID: sr.ID, Slug: sr.Slug}
	for _, tid := range []string{"t1", "t2"} {
		// stamped an hour back: a freshly created row is inside
		// threadStartGrace, which is the point of that window
		if err := st.AddThread(store.Thread{JobID: j.ID, TID: tid, SessionID: "s-" + tid, Pane: "%" + tid[1:], State: "live",
			CreatedAt: time.Now().Add(-time.Hour).UnixMilli()}); err != nil {
			t.Fatal(err)
		}
	}
	// t1 still held, t2's window is gone. The cell session is live and present
	// in the snapshot, so the snapshot is trustworthy enough to write off.
	all := []*core.Peer{{Pane: "%1", Name: ThreadAgentName(j, "t1"), Agent: "claude", TmuxSession: CellSession(j)}}
	if err := ReconcileThreads(j, all, map[string]bool{CellSession(j): true}); err != nil {
		t.Fatalf("ReconcileThreads: %v", err)
	}
	if got, err := st.GetThread(j.ID, "t1"); err != nil || got.State != "live" {
		t.Errorf("t1 = %+v (%v), want it still live", got, err)
	}
	got2, err := st.GetThread(j.ID, "t2")
	if err != nil {
		t.Fatal(err)
	}
	if got2.State != "closed" || got2.Pane != "" || got2.ClosedAt == 0 {
		t.Errorf("t2 = %+v, want closed with its pane cleared", got2)
	}
	// the row is a POINTER, not a transcript: closing must never lose the
	// session id a revive resumes from
	if got2.SessionID != "s-t2" {
		t.Errorf("closing t2 lost its session id: %+v", got2)
	}

	// teardown closes whatever is left, no snapshot
	if err := CloseThreadsForTeardown(j); err != nil {
		t.Fatalf("CloseThreadsForTeardown: %v", err)
	}
	for _, tid := range []string{"t1", "t2"} {
		got, err := st.GetThread(j.ID, tid)
		if err != nil {
			t.Fatal(err)
		}
		if got.State != "closed" {
			t.Errorf("%s survived teardown as %q", tid, got.State)
		}
		if got.SessionID == "" {
			t.Errorf("%s lost its session id at teardown — the transcript pointer is the whole row", tid)
		}
	}
}

// --- roll-call / zeus-election exclusions ---

// TestCellZeusIgnoresThreads is the other half of the roll-call fix: the
// positional fallback picks the lowest-numbered depth-0 claude pane, so a
// thread branched before zeus' session file lands could be elected zeus — and
// stampSession would then write a THREAD's session id into the job.
func TestCellZeusIgnoresThreads(t *testing.T) {
	j := &Job{Slug: "elect"}
	sess := CellSession(j)
	thread := &core.Peer{TmuxSession: sess, Agent: "claude", Depth: 0, Pane: "%1",
		Name: ThreadAgentName(j, "t1"), SessionID: "thread-sess"}
	zeusNamed := &core.Peer{TmuxSession: sess, Agent: "claude", Depth: 0, Pane: "%2",
		Name: ZeusName(j), SessionID: "zeus-sess"}
	zeusUnnamed := &core.Peer{TmuxSession: sess, Agent: "claude", Depth: 0, Pane: "%2", SessionID: "zeus-sess"}

	t.Run("named zeus wins over a lower-numbered thread pane", func(t *testing.T) {
		if got := CellZeus(j, []*core.Peer{thread, zeusNamed}); got != zeusNamed {
			t.Errorf("elected %+v, want zeus", got)
		}
	})
	t.Run("positional fallback skips the thread", func(t *testing.T) {
		// zeus has no --name yet (its session file is still being written),
		// and the thread sits on the LOWER pane number — the exact race
		if got := CellZeus(j, []*core.Peer{thread, zeusUnnamed}); got != zeusUnnamed {
			t.Errorf("elected %+v, want the unnamed zeus, not the thread", got)
		}
	})
	t.Run("only threads live -> no zeus", func(t *testing.T) {
		if got := CellZeus(j, []*core.Peer{thread}); got != nil {
			t.Errorf("elected a thread as zeus: %+v", got)
		}
	})
	t.Run("unnamed thread pane loses to a higher-numbered zeus", func(t *testing.T) {
		// the name filter above is useless in the exact race it exists for: a
		// still-starting thread has NO name, so only its recorded pane can
		// exclude it. Elected here, CellHealth would report a healthy cell
		// while zeus is dead, and /api/steer would type carl's message into
		// the thread's pane.
		st := useTestStore(t)
		sr := mustCreateJob(t, st, store.Job{Slug: "elect-pane", Repo: "kraken", State: "working"})
		job := &Job{ID: sr.ID, Slug: sr.Slug}
		if err := st.AddThread(store.Thread{JobID: job.ID, TID: "t1", SessionID: "thread-sess", Pane: "%1", State: "live"}); err != nil {
			t.Fatal(err)
		}
		jsess := CellSession(job)
		unnamedThread := &core.Peer{TmuxSession: jsess, Agent: "claude", Depth: 0, Pane: "%1"}
		unnamedZeus := &core.Peer{TmuxSession: jsess, Agent: "claude", Depth: 0, Pane: "%2", SessionID: "zeus-sess"}
		if got := CellZeus(job, []*core.Peer{unnamedThread, unnamedZeus}); got != unnamedZeus {
			t.Errorf("elected %+v, want the unnamed zeus — the thread's pane is recorded live", got)
		}
		// and with zeus genuinely gone, the cell has NO zeus rather than a
		// thread wearing zeus' hat: the dead-cell alert must still fire
		if got := CellZeus(job, []*core.Peer{unnamedThread}); got != nil {
			t.Errorf("elected an unnamed thread pane as zeus: %+v", got)
		}
		if reason := CellHealth(job, []*core.Peer{unnamedThread}); !strings.Contains(reason, "zeus process is gone") {
			t.Errorf("CellHealth = %q, want the dead-cell reason", reason)
		}
	})
	t.Run("another job's thread name is not filtered", func(t *testing.T) {
		foreign := &core.Peer{TmuxSession: sess, Agent: "claude", Depth: 0, Pane: "%3",
			Name: "thread-other-job-t1", SessionID: "x"}
		if got := CellZeus(j, []*core.Peer{foreign}); got != foreign {
			t.Errorf("filtered a peer that isn't this job's thread: %+v", got)
		}
	})
}

// --- the thread brief ---

// TestThreadBriefStatesTheContract pins the load-bearing sentences. A fork
// inherits zeus' CONVERSATION but not its system prompt, so this brief is the
// only thing standing between the thread and continuing zeus' work in zeus'
// worktree — every clause below counters a specific prior that history sets.
func TestThreadBriefStatesTheContract(t *testing.T) {
	j := &Job{Slug: "brief-job"}
	brief := threadBrief(j, "t2", "why is the daemon quiet", "/tmp/wt-kraken", "ask")
	lower := strings.ToLower(brief)

	for _, want := range []struct{ token, why string }{
		{"side thread t2", "the thread must know its own tid"},
		{"why is the daemon quiet", "carl's title is the thread's purpose"},
		{"brief-job", "the job slug"},
		{"not zeus", "the identity correction is the whole point"},
		{"forked", "it has to know the history above it is inherited, not its own"},
		{"never open prs", "a thread must never ship"},
		{"artifacts", "a thread must never write the job's deliverables"},
		{"kraken` write verbs", "a thread must never write questions/feedback rows"},
		{"still live", "zeus is working in the same worktree right now"},
		{"/tmp/wt-kraken", "the shared worktree path"},
		{".git", "the repo state is off limits"},
		{"scratch files", "the line is the job's work, not the tooling"},
		{"unrestricted", "it should not ask for permissions it already has"},
		{"no subagents", "one agent, one pane"},
		{"own pane", "there is no other channel to carl"},
		{"lead every answer with its tldr", "carl's steering: the first sentence alone must carry the answer"},
	} {
		if !strings.Contains(lower, strings.ToLower(want.token)) {
			t.Errorf("thread brief is missing %q (%s):\n%s", want.token, want.why, brief)
		}
	}

	// it must NOT hand the thread zeus' job: no phase gating, no crew, no
	// pipeline skills — a thread that reads those is a second zeus
	for _, banned := range []string{"hephaestus", "athena", "hermes", "/do", "phase 2", "skills/aim"} {
		if strings.Contains(lower, strings.ToLower(banned)) {
			t.Errorf("thread brief mentions %q — that is zeus' contract, not a thread's:\n%s", banned, brief)
		}
	}
}

// TestThreadSeedCarriesTheGuard holds the prompt-side half of the thread
// contract. The brief states it in full, but a fork wakes up holding zeus'
// whole conversation with the brief at the far end; measured across one
// night's threads, prompts carrying a guard clause were obeyed (2/2 wrote
// nothing) while bare-question prompts were not (2/4 wrote artifacts, ran
// kraken write verbs, or spawned a teammate — one emitted /aim's closing
// line from a thread pane). The guard rides the LAST text before the
// thread's first token, which is the one placement the recency gradient
// favors.
func TestThreadSeedCarriesTheGuard(t *testing.T) {
	t.Setenv("KRAKEN_OPERATOR", "Ada")
	seeded := threadSeed("why does the export table hold arrays?")
	if !strings.HasPrefix(seeded, "why does the export table hold arrays?") {
		t.Fatalf("carl's question must lead the prompt: %q", seeded)
	}
	lower := strings.ToLower(seeded)
	for _, want := range []struct{ token, why string }{
		{"about the job's work", "the question/request distinction is the guard's core"},
		{"do not edit artifacts", "the measured violation: threads writing the artifact set"},
		{"kraken write verbs", "the measured violation: questions add / load set from a thread"},
		{"do not spawn agents", "the measured violation: a thread spawning a hermes teammate"},
		{"leave the doing to zeus", "the escape hatch — say so instead of complying"},
		{"nothing relays this thread to zeus", "the escape hatch must not promise a handoff that does not exist"},
		{"ada steers zeus", "the one real channel: the operator carrying the ask into zeus' pane"},
	} {
		if !strings.Contains(lower, want.token) {
			t.Errorf("thread seed guard is missing %q (%s):\n%s", want.token, want.why, seeded)
		}
	}
	// the revive path stages no prompt at all; an empty seed must stay empty
	// or stagedCommand would stage a guard-only prompt on every revive
	if threadSeed("") != "" {
		t.Error("empty seed must stay empty — revive resumes an existing conversation")
	}
}

// TestReviveOpensTheWindowWithTheToolchainOnPath closes the third daemon-issued
// spawn site. StartCell and ReactivateJob hand their `tmux new-session` the
// cellSpawnEnv PATH; a thread's window is opened by kraken itself rather than by
// zeus, so it inherits none of that transitively and has to be told directly.
// Without it a thread — reading the same worktree, running the same `go test` —
// gets launchd's PATH and hits the exact papercut cellSpawnEnv exists to remove.
func TestReviveOpensTheWindowWithTheToolchainOnPath(t *testing.T) {
	st := useTestStore(t)
	j, row := reviveFixture(t, st, "revive-path")
	stubLookPath(t, func(name string) (string, error) {
		if name != "go" {
			return "", errors.New("a thread window resolves only go")
		}
		return "/usr/local/go/bin/go", nil
	})
	t.Setenv("PATH", "/usr/bin:/bin")

	var gotEnv []string
	open := func(_, _, _ string, env, argv []string) (string, error) {
		gotEnv = env
		return "cell-x:3 (%17)", nil
	}
	if _, err := reviveClaimed(st, j, row, CellSession(j), t.TempDir(), open); err != nil {
		t.Fatalf("reviveClaimed: %v", err)
	}

	path := ""
	for _, kv := range gotEnv {
		if strings.HasPrefix(kv, "PATH=") {
			path = strings.TrimPrefix(kv, "PATH=")
		}
	}
	if path != "/usr/local/go/bin:/usr/bin:/bin" {
		t.Errorf("thread window PATH = %q, want the go dir prepended to the inherited one", path)
	}
}

// The best-effort contract holds here too: an unresolvable toolchain must open
// the window with a nil env (exec's "inherit"), never fail a revive carl asked
// for. A thread spawn that refused because a PATH probe failed would be strictly
// worse than the papercut.
func TestReviveOpensTheWindowEvenWithNoToolchain(t *testing.T) {
	st := useTestStore(t)
	j, row := reviveFixture(t, st, "revive-nopath")
	stubLookPath(t, func(string) (string, error) { return "", exec.ErrNotFound })
	prev := toolDirs
	toolDirs = []string{filepath.Join(t.TempDir(), "nowhere")}
	t.Cleanup(func() { toolDirs = prev })

	sawEnv := true
	open := func(_, _, _ string, env, argv []string) (string, error) {
		sawEnv = env != nil
		return "cell-x:4 (%18)", nil
	}
	if _, err := reviveClaimed(st, j, row, CellSession(j), t.TempDir(), open); err != nil {
		t.Fatalf("reviveClaimed refused because the toolchain was missing: %v", err)
	}
	if sawEnv {
		t.Error("thread window got a non-nil env with no resolvable go, want nil so the spawn is unchanged")
	}
}
