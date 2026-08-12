package jobs

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"kraken/internal/core"
	"kraken/internal/store"
	"kraken/internal/util"
)

// slack_contract_test.go — blind contract tests for slice 3 of the Slack
// intake bot (50-contract.md § "Slice 3 — internal/jobs"): the pure poll
// helpers, the bump text, iris' identity constants, and the two spawn
// paths.
//
// Dimension: stated invariants and happy paths. Per the contract's "Note for
// authors", slackPass' network loops are NOT faked through the pass — its
// frozen observable surface is the pure helpers, the kv stamps, and the
// store effects, and that is all this file touches.
//
// Every package-level identifier is `contract`-prefixed and slack/iris-
// scoped: loadscale_contract_test.go already owns the bare `contract*` names
// in this package, and athena-edge shares the test binary.

// contractSlackMsg builds one core.SlackMessage with the fields the filters
// read.
func contractSlackMsg(channel, ts, user, text string) core.SlackMessage {
	return core.SlackMessage{ChannelID: channel, TS: ts, User: user, Text: text}
}

// contractSlackTexts renders a message slice as its texts, so an ordering
// assertion is one string comparison.
func contractSlackTexts(msgs []core.SlackMessage) string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.Text
	}
	return strings.Join(out, ",")
}

// contractIrisSandbox points util.Home() at a throwaway dir, clears both
// Slack variables from the process environment, and creates iris' own
// directory — the state a spawn-command builder expects to stage into.
func contractIrisSandbox(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SLACK_USER_TOKEN", "")
	t.Setenv("SLACK_USER_ID", "")
	if err := os.MkdirAll(IrisDir(), 0o755); err != nil {
		t.Fatalf("mkdir iris dir: %v", err)
	}
}

// contractIrisPeer builds a discovery peer in a tmux session.
func contractIrisPeer(session, name, agent, pane string, depth int) *core.Peer {
	return &core.Peer{
		TmuxSession: session,
		Name:        name,
		Agent:       agent,
		Pane:        pane,
		Depth:       depth,
		Role:        "agent",
		Messageable: true,
	}
}

// --- identity ---

// TestContractIrisIdentityConstants: the tmux session name and the --name
// are both "iris", and IrisDir is ~/.kraken/iris via util.Home().
func TestContractIrisIdentityConstants(t *testing.T) {
	if IrisSessionName != "iris" {
		t.Errorf("IrisSessionName = %q, want %q", IrisSessionName, "iris")
	}
	if IrisAgentName != "iris" {
		t.Errorf("IrisAgentName = %q, want %q", IrisAgentName, "iris")
	}
	contractIrisSandbox(t)
	want := filepath.Join(util.Home(), ".kraken", "iris")
	if got := IrisDir(); got != want {
		t.Fatalf("IrisDir() = %q, want %q", got, want)
	}
}

// --- filterSlackNew ---

// TestContractFilterSlackNewAppliesTheFloor: the mark is EXCLUSIVE — the
// boundary message itself has already been committed, so re-seeing it (which
// Slack's own `oldest` semantics and a re-poll both produce) must not emit it
// a second time. Order is preserved: iris reads a conversation in sequence.
func TestContractFilterSlackNewAppliesTheFloor(t *testing.T) {
	msgs := []core.SlackMessage{
		contractSlackMsg("D111", "1755000001.000100", "U0BOB", "older"),
		contractSlackMsg("D111", "1755000002.000100", "U0BOB", "at the mark"),
		contractSlackMsg("D111", "1755000003.000100", "U0BOB", "newer"),
		contractSlackMsg("D111", "1755000004.000100", "U0AMY", "newest"),
	}
	got := filterSlackNew(msgs, "1755000002.000100", "U0CARL")
	if want := "newer,newest"; contractSlackTexts(got) != want {
		t.Fatalf("filterSlackNew = %q, want %q — the mark is exclusive and order is preserved",
			contractSlackTexts(got), want)
	}
}

// TestContractFilterSlackNewEmptyMarkKeepsAll: a conversation kraken has
// never swept has no floor, so a first sweep emits everything it fetched.
func TestContractFilterSlackNewEmptyMarkKeepsAll(t *testing.T) {
	msgs := []core.SlackMessage{
		contractSlackMsg("D111", "1755000001.000100", "U0BOB", "one"),
		contractSlackMsg("D111", "1755000002.000100", "U0AMY", "two"),
	}
	got := filterSlackNew(msgs, "", "U0CARL")
	if want := "one,two"; contractSlackTexts(got) != want {
		t.Fatalf("filterSlackNew with an empty mark = %q, want %q", contractSlackTexts(got), want)
	}
}

// TestContractFilterSlackNewDropsSelfAndEmptyTS: carl's own messages are not
// intake (a DM conversation carries both halves, and iris drafting jobs from
// carl's own replies would loop), and a message with no ts has no identity —
// it cannot be deduped, marked, or ordered.
func TestContractFilterSlackNewDropsSelfAndEmptyTS(t *testing.T) {
	msgs := []core.SlackMessage{
		contractSlackMsg("D111", "1755000001.000100", "U0CARL", "carl's own"),
		contractSlackMsg("D111", "1755000002.000100", "U0BOB", "bob's"),
		contractSlackMsg("D111", "", "U0BOB", "no ts"),
		contractSlackMsg("D111", "1755000003.000100", "U0CARL", "carl again"),
		contractSlackMsg("D111", "1755000004.000100", "U0AMY", "amy's"),
	}
	got := filterSlackNew(msgs, "", "U0CARL")
	if want := "bob's,amy's"; contractSlackTexts(got) != want {
		t.Fatalf("filterSlackNew = %q, want %q", contractSlackTexts(got), want)
	}
	for _, m := range got {
		if m.User == "U0CARL" {
			t.Errorf("a self-authored message survived: %+v", m)
		}
		if m.TS == "" {
			t.Errorf("a ts-less message survived: %+v", m)
		}
	}
}

// TestContractFilterSlackNewIsPure: the input slice is not reordered,
// truncated or rewritten in place — the caller still holds the raw sweep
// result (the thread catch-up reads LatestReply off it afterwards).
func TestContractFilterSlackNewIsPure(t *testing.T) {
	msgs := []core.SlackMessage{
		contractSlackMsg("D111", "1755000001.000100", "U0CARL", "carl's own"),
		contractSlackMsg("D111", "1755000002.000100", "U0BOB", "bob's"),
	}
	before := contractSlackTexts(msgs)
	_ = filterSlackNew(msgs, "1755000001.000100", "U0CARL")
	if after := contractSlackTexts(msgs); after != before {
		t.Fatalf("filterSlackNew mutated its input: %q -> %q", before, after)
	}
}

// --- mentionAfterDate ---

// TestContractMentionAfterDateOverlapsByADay: search.messages' `after:` is
// day-grained, so the query floor is deliberately a day BEHIND the mark —
// the pass re-floors the results exactly, client-side. The date is UTC, not
// local: a machine in a negative offset would otherwise ask for a day that
// has not started in Slack's own reckoning and miss the overlap.
func TestContractMentionAfterDateOverlapsByADay(t *testing.T) {
	now := time.Date(2026, 8, 3, 18, 30, 0, 0, time.UTC)
	markSecs := int64(1755000000)
	mark := "1755000000.000200"

	want := time.Unix(markSecs, 0).UTC().Add(-24 * time.Hour).Format("2006-01-02")
	if got := mentionAfterDate(mark, now); got != want {
		t.Fatalf("mentionAfterDate(%q) = %q, want %q (24h before the mark, UTC)", mark, got, want)
	}
}

// TestContractMentionAfterDateFallsBackToNow: with no usable mark (a first
// ever poll, or a mark that does not parse) the window is 24h before now —
// a bounded query rather than a scan of all history.
func TestContractMentionAfterDateFallsBackToNow(t *testing.T) {
	now := time.Date(2026, 8, 3, 18, 30, 0, 0, time.UTC)
	want := now.UTC().Add(-24 * time.Hour).Format("2006-01-02")

	for _, mark := range []string{"", "not-a-timestamp", "abc.def"} {
		if got := mentionAfterDate(mark, now); got != want {
			t.Errorf("mentionAfterDate(%q) = %q, want %q (24h before now)", mark, got, want)
		}
	}
}

// TestContractMentionAfterDateIsDayGrained pins the format itself:
// search.messages accepts YYYY-MM-DD and nothing finer, so a timestamp here
// would silently return no matches at all.
func TestContractMentionAfterDateIsDayGrained(t *testing.T) {
	now := time.Date(2026, 8, 3, 18, 30, 0, 0, time.UTC)
	got := mentionAfterDate("", now)
	if len(got) != len("2006-01-02") {
		t.Fatalf("mentionAfterDate = %q, want a YYYY-MM-DD date", got)
	}
	if _, err := time.Parse("2006-01-02", got); err != nil {
		t.Fatalf("mentionAfterDate = %q, does not parse as YYYY-MM-DD: %v", got, err)
	}
}

// --- slackBumpMessage ---

// TestContractSlackBumpMessageExactStrings: this text is typed into a live
// claude pane, so it is frozen to the byte — singular for one, plural
// otherwise, one line, and carrying the exact verb iris must run.
func TestContractSlackBumpMessageExactStrings(t *testing.T) {
	if got, want := slackBumpMessage(1), "slack: 1 new message — run: kraken slack inbox"; got != want {
		t.Errorf("slackBumpMessage(1) = %q, want %q", got, want)
	}
	for _, n := range []int{0, 2, 5, 17} {
		want := "slack: " + strconv.Itoa(n) + " new messages — run: kraken slack inbox"
		if got := slackBumpMessage(n); got != want {
			t.Errorf("slackBumpMessage(%d) = %q, want %q", n, got, want)
		}
	}
	for _, n := range []int{0, 1, 2, 17} {
		got := slackBumpMessage(n)
		if strings.Contains(got, "\n") {
			t.Errorf("slackBumpMessage(%d) = %q, want a single line", n, got)
		}
		if !strings.Contains(got, "kraken slack inbox") {
			t.Errorf("slackBumpMessage(%d) = %q, want it to name the verb iris runs next", n, got)
		}
	}
}

// --- findIris ---

// TestContractFindIrisPrefersTheNamedAgent: with a session file written,
// iris carries its --name, and that is the unambiguous match.
func TestContractFindIrisPrefersTheNamedAgent(t *testing.T) {
	all := []*core.Peer{
		contractIrisPeer("kraken-demo-1", IrisAgentName, "claude", "%1", 0),
		contractIrisPeer(IrisSessionName, "", "claude", "%2", 0),
		contractIrisPeer(IrisSessionName, IrisAgentName, "claude", "%3", 0),
	}
	got := findIris(all)
	if got == nil {
		t.Fatal("findIris returned nil with a named iris present")
	}
	if got.Pane != "%3" {
		t.Fatalf("findIris = pane %q, want %%3 (the named depth-0 claude in the iris session)", got.Pane)
	}
}

// TestContractFindIrisPositionalFallback: Peer.Name only appears once claude
// writes its session file, so a freshly-spawned iris is nameless for the
// first seconds. A depth-0 claude in the iris session is iris — the same
// fallback CellZeus makes for the same race.
func TestContractFindIrisPositionalFallback(t *testing.T) {
	all := []*core.Peer{
		contractIrisPeer(IrisSessionName, "", "claude", "%7", 0),
		contractIrisPeer(IrisSessionName, "", "claude", "%4", 0),
	}
	got := findIris(all)
	if got == nil {
		t.Fatal("findIris returned nil for a still-nameless claude in the iris session")
	}
	if got.Pane != "%4" {
		t.Fatalf("findIris = pane %q, want %%4 (the lowest pane, CellZeus' positional rule)", got.Pane)
	}
}

// TestContractFindIrisIgnoresEverythingElse: another session's iris-named
// agent, a nested subagent, and a shell pane are all not iris. Returning one
// of them would send carl's inbox bump into an unrelated pane.
func TestContractFindIrisIgnoresEverythingElse(t *testing.T) {
	shell := contractIrisPeer(IrisSessionName, "", "", "%9", 0)
	shell.Role = "shell"
	all := []*core.Peer{
		contractIrisPeer("kraken-other-2", IrisAgentName, "claude", "%1", 0),
		contractIrisPeer(IrisSessionName, "subagent", "claude", "%2", 1),
		contractIrisPeer(IrisSessionName, "", "codex", "%3", 0),
		shell,
	}
	if got := findIris(all); got != nil {
		t.Fatalf("findIris = %+v, want nil — none of these is a depth-0 claude in the iris session", got)
	}
	if got := findIris(nil); got != nil {
		t.Fatalf("findIris(nil) = %+v, want nil", got)
	}
}

// --- irisBrief ---

// TestContractIrisBriefPinsTheThreeRules is the brief-content matrix
// (crew_test.go's shape): the three strings the contract freezes verbatim
// are the three ways iris can do lasting damage if it forgets them — queue a
// job carl never approved, lose a message by never acking it, or write a job
// nobody can trace back to a Slack conversation.
func TestContractIrisBriefPinsTheThreeRules(t *testing.T) {
	contractIrisSandbox(t)
	brief := irisBrief()
	if strings.TrimSpace(brief) == "" {
		t.Fatal("irisBrief() is empty")
	}
	// "you never start" is the never-start rule (it used to be the literal
	// `ready: false`, back when the rule was about a FLAG iris had to leave
	// switched off; there is no flag now, so the rule is stated as what iris
	// is for), "kraken slack ack" the ack obligation, "## Source" the
	// provenance section every job body it writes must carry, and IrisModel
	// the model tier stated as the resolved id.
	for _, want := range []string{
		"you never start",
		"kraken slack ack",
		"## Source",
		core.IrisModel,
	} {
		if !strings.Contains(brief, want) {
			t.Errorf("irisBrief() does not contain %q", want)
		}
	}
	// starting a job is carl's move, stated in the brief that is in context
	// every turn rather than only in a skill read once
	if !strings.Contains(strings.ToLower(brief), "carl") {
		t.Error("irisBrief() never names carl — the brief must say whose move starting is")
	}
}

// --- irisSpawnCommand ---

// TestContractIrisSpawnCommandFirstSpawn: kraken mints the session id and
// names it, so the id is known before the process exists (never read back
// from discovery), and the model is on the command line where discovery
// reads it back from.
func TestContractIrisSpawnCommandFirstSpawn(t *testing.T) {
	contractIrisSandbox(t)
	const sid = "11111111-2222-4333-8444-555555555555"

	cmd, err := irisSpawnCommand(sid, false)
	if err != nil {
		t.Fatalf("irisSpawnCommand: %v", err)
	}
	for _, want := range []string{
		"--dangerously-skip-permissions",
		"--model " + core.IrisModel,
		"--name " + IrisAgentName,
		"--session-id " + sid,
		"--append-system-prompt",
		"$(cat ",
		IrisDir(),
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("first-spawn command %q missing %q", cmd, want)
		}
	}
	if strings.Contains(cmd, "-r "+sid) {
		t.Errorf("first-spawn command resumes instead of naming a new session: %q", cmd)
	}
}

// TestContractIrisSpawnCommandResume: a respawn continues the SAME
// conversation (-r), never a new one — carl kills the pane and expects iris
// to come back holding what it already knew.
func TestContractIrisSpawnCommandResume(t *testing.T) {
	contractIrisSandbox(t)
	const sid = "11111111-2222-4333-8444-555555555555"

	cmd, err := irisSpawnCommand(sid, true)
	if err != nil {
		t.Fatalf("irisSpawnCommand: %v", err)
	}
	for _, want := range []string{
		"--dangerously-skip-permissions",
		"--model " + core.IrisModel,
		"--name " + IrisAgentName,
		"-r " + sid,
		"--append-system-prompt",
		"$(cat ",
		IrisDir(),
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("resume command %q missing %q", cmd, want)
		}
	}
	if strings.Contains(cmd, "--session-id") {
		t.Errorf("resume command mints a new session id: %q", cmd)
	}
}

// TestContractIrisSpawnCommandStagesTheBriefOnBothPaths is the measured
// asymmetry the contract cites (thread.go:438): a resumed session does NOT
// retain an earlier run's --append-system-prompt, so a resume that skips the
// brief comes back as a plain claude holding iris' history — one that will
// happily set `ready: true` because nothing in context says not to.
//
// It also pins that the brief is STAGED rather than inlined: the text lives
// in a file under IrisDir() that the pane's shell reads, so the tmux command
// stays short regardless of how long the brief grows (the ~16KB ceiling that
// once wedged every cell spawn).
func TestContractIrisSpawnCommandStagesTheBriefOnBothPaths(t *testing.T) {
	contractIrisSandbox(t)
	const sid = "11111111-2222-4333-8444-555555555555"

	for _, resume := range []bool{false, true} {
		cmd, err := irisSpawnCommand(sid, resume)
		if err != nil {
			t.Fatalf("irisSpawnCommand(resume=%v): %v", resume, err)
		}
		if !strings.Contains(cmd, "--append-system-prompt") || !strings.Contains(cmd, "$(cat ") {
			t.Errorf("resume=%v: command %q does not stage a brief", resume, cmd)
		}
		if strings.Contains(cmd, "## Source") {
			t.Errorf("resume=%v: the brief text is inlined on the command line (%d bytes) — tmux rejects a long command with a bare `command too long`", resume, len(cmd))
		}
		if len(cmd) > 4096 {
			t.Errorf("resume=%v: command is %d bytes, want the brief staged in a file instead", resume, len(cmd))
		}
		// `--` stays last: everything after it is claude's initial prompt, so
		// a brief flag appended past it would be swallowed into the seed text
		if i, k := strings.Index(cmd, " -- "), strings.Index(cmd, "--append-system-prompt"); i >= 0 && i < k {
			t.Errorf("resume=%v: command puts -- before --append-system-prompt: %q", resume, cmd)
		}
	}

	// the staged file really holds the brief, so `$(cat …)` delivers it
	var staged string
	err := filepath.Walk(IrisDir(), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		b, readErr := os.ReadFile(path)
		if readErr == nil && strings.Contains(string(b), "## Source") {
			staged = path
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", IrisDir(), err)
	}
	if staged == "" {
		t.Fatalf("no file under %s carries the brief text — the spawn would deliver an empty system prompt", IrisDir())
	}
}

// --- EnsureIris' two gated no-ops ---

// TestContractEnsureIrisNoOpWhenUnconfigured: with no Slack credentials
// anywhere the whole feature is off, silently — nil returned, nothing
// spawned, and no kv row minted (a minted session id with no process behind
// it would be resumed forever by every later tick).
func TestContractEnsureIrisNoOpWhenUnconfigured(t *testing.T) {
	st := useTestStore(t)
	t.Setenv("SLACK_USER_TOKEN", "")
	t.Setenv("SLACK_USER_ID", "")

	if err := EnsureIris(st, map[string]bool{}); err != nil {
		t.Fatalf("EnsureIris with Slack unconfigured = %v, want nil (feature off is not an error)", err)
	}
	if _, err := st.GetKV("iris_agent"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("iris_agent kv err = %v, want ErrNotFound — an unconfigured machine must mint nothing", err)
	}
}

// TestContractIrisDraftShapeIsNeverStartable is the never-auto-starts
// guarantee. It used to assert that startable() rejected an iris draft; that
// predicate is gone with the queue pass, and what replaced it is stronger:
// nothing auto-starts at all, so "iris drafts, carl starts" no longer rests
// on a flag the daemon consults. What is left to pin here is the shape — a
// row in exactly the form /api/job/draft's CREATE produces reads as a DRAFT,
// which is the state the board renders with a submit affordance and nothing
// else. If a future draft route ever wrote a state, this fails.
func TestContractIrisDraftShapeIsNeverStartable(t *testing.T) {
	draft := &Job{Slug: "csv-export-timeout", Repo: "kraken", State: "", Body: "from a slack dm"}

	if got := DisplayState(draft); got != "draft" {
		t.Fatalf("DisplayState = %q, want draft", got)
	}
	if draft.Started() {
		t.Fatal("an iris draft reads as started — carl's submit is what starts a job")
	}
	// the control: a started job is distinguishable from this one, so the
	// assertion above is measuring state rather than something else about
	// the fixture
	started := &Job{Slug: "csv-export-timeout", Repo: "kraken", State: "working", Body: "from a slack dm"}
	if !started.Started() || DisplayState(started) != "working" {
		t.Fatal("the control job does not read as started — the fixture proves nothing")
	}
}

// TestContractEnsureIrisNoOpWhenSessionLive: a live `iris` tmux session is
// assumed to hold iris (carl kills the session to force a respawn), so a
// configured machine with the session up does nothing — no second claude on
// one session id.
func TestContractEnsureIrisNoOpWhenSessionLive(t *testing.T) {
	st := useTestStore(t)
	t.Setenv("SLACK_USER_TOKEN", "xoxp-configured")
	t.Setenv("SLACK_USER_ID", "U0CARL")

	if err := EnsureIris(st, map[string]bool{IrisSessionName: true}); err != nil {
		t.Fatalf("EnsureIris with the iris session live = %v, want nil", err)
	}
	if _, err := st.GetKV("iris_agent"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("iris_agent kv err = %v, want ErrNotFound — a live session is never re-minted", err)
	}
}
