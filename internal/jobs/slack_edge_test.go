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
)

// slack_edge_test.go — the EDGE dimension of slice 3 of
// artifacts/50-contract.md: the boundaries of the pure decision helpers, the
// two spawn paths' refusals, and the delivery pass' do-nothing cases. Happy
// paths and the stated invariants belong to the sibling blind file.
//
// Per the contract's note, nothing here drives slackPass' network loops: the
// frozen observable surface is the pure helpers, the kv stamps and the store
// effects, and that is all this file touches.
//
// Every package-level identifier declared here is prefixed `edge` — this
// package's test namespace is shared with the other blind author.

// --- helpers ---------------------------------------------------------------

// edgeSlackMsg builds one Slack message with the fields filterSlackNew reads.
func edgeSlackMsg(ts, user string) core.SlackMessage {
	return core.SlackMessage{ChannelID: "D1", TS: ts, User: user, Text: "hello"}
}

// edgeTSOf lists the surviving messages' ts values, which is what every
// filterSlackNew case actually asserts on.
func edgeTSOf(msgs []core.SlackMessage) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.TS)
	}
	return out
}

func edgeEqualStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// edgePeer builds a discovery row shaped like a claude in a tmux session.
func edgePeer(session, name string, depth int) *core.Peer {
	return &core.Peer{
		Pane: "%1", TmuxSession: session, Name: name, Depth: depth,
		Agent: "claude", Role: "agent", Status: "running",
	}
}

// edgeSandboxHome points util.Home() at a throwaway dir so nothing here can
// read or create the real ~/.kraken/slack.env or ~/.kraken/iris, and clears
// the two allowlisted process-env keys so "Slack is unconfigured" is the
// starting state regardless of the developer's shell.
func edgeSandboxHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SLACK_USER_TOKEN", "")
	t.Setenv("SLACK_USER_ID", "")
	return home
}

// --- filterSlackNew --------------------------------------------------------

// TestEdgeFilterSlackNewExclusiveFloor pins the boundary the whole
// at-least-once design rests on: the mark is the LAST ts already committed,
// so a message whose ts EQUALS it has been seen. Off by one in the inclusive
// direction re-drafts the same message every pass forever; off by one the
// other way loses a message with no trace.
func TestEdgeFilterSlackNewExclusiveFloor(t *testing.T) {
	const mark = "1769040000.000200"
	msgs := []core.SlackMessage{
		edgeSlackMsg("1769040000.000100", "U9"), // below — dropped
		edgeSlackMsg(mark, "U9"),                // EXACTLY the mark — dropped
		edgeSlackMsg("1769040000.000201", "U9"), // one micro past — kept
		edgeSlackMsg("1769040001.000000", "U9"), // next second — kept
	}
	got := edgeTSOf(filterSlackNew(msgs, mark, "UCARL"))
	want := []string{"1769040000.000201", "1769040001.000000"}
	if !edgeEqualStrings(got, want) {
		t.Fatalf("kept %v, want %v — the floor is EXCLUSIVE", got, want)
	}
}

// TestEdgeFilterSlackNewEmptyMarkKeepsAll: a conversation swept for the first
// time has no watermark, and treating "" as a floor everything falls below
// would make a brand-new DM invisible until its second message.
func TestEdgeFilterSlackNewEmptyMarkKeepsAll(t *testing.T) {
	msgs := []core.SlackMessage{
		edgeSlackMsg("0.000001", "U9"),
		edgeSlackMsg("1769040000.000100", "U9"),
	}
	got := edgeTSOf(filterSlackNew(msgs, "", "UCARL"))
	want := []string{"0.000001", "1769040000.000100"}
	if !edgeEqualStrings(got, want) {
		t.Fatalf("kept %v, want %v — an empty mark keeps everything", got, want)
	}
}

// TestEdgeFilterSlackNewDropsSelfAuthored: carl's own replies come back in
// every history sweep. Without this drop, iris would be bumped about carl
// answering someone and would draft a job out of his own words.
func TestEdgeFilterSlackNewDropsSelfAuthored(t *testing.T) {
	msgs := []core.SlackMessage{
		edgeSlackMsg("1769040000.000100", "UCARL"),
		edgeSlackMsg("1769040000.000200", "U9"),
		edgeSlackMsg("1769040000.000300", "UCARL"),
	}
	got := edgeTSOf(filterSlackNew(msgs, "", "UCARL"))
	if !edgeEqualStrings(got, []string{"1769040000.000200"}) {
		t.Fatalf("kept %v, want only the other author's message", got)
	}
}

// TestEdgeFilterSlackNewDropsEmptyTS: a ts is the row's identity half of
// UNIQUE(channel_id, ts) AND the value the watermark advances to. A message
// with none (a malformed or subtype-only record) would collide with every
// other empty-ts message in the channel and could never be re-found.
func TestEdgeFilterSlackNewDropsEmptyTS(t *testing.T) {
	msgs := []core.SlackMessage{
		edgeSlackMsg("", "U9"),
		edgeSlackMsg("1769040000.000200", "U9"),
		edgeSlackMsg("", "U9"),
	}
	got := edgeTSOf(filterSlackNew(msgs, "", "UCARL"))
	if !edgeEqualStrings(got, []string{"1769040000.000200"}) {
		t.Fatalf("kept %v, want only the message with a ts", got)
	}
	// and with an empty mark AND every message empty, the answer is nothing —
	// not "everything, because the floor is off".
	if got := filterSlackNew([]core.SlackMessage{edgeSlackMsg("", "U9")}, "", "UCARL"); len(got) != 0 {
		t.Fatalf("kept %+v, want nothing", got)
	}
}

// TestEdgeFilterSlackNewEmptyAndNilInputs: the pass calls this for every
// swept conversation, most of which return nothing, so neither shape may
// panic or invent a message.
func TestEdgeFilterSlackNewEmptyAndNilInputs(t *testing.T) {
	if got := filterSlackNew(nil, "1769040000.000100", "UCARL"); len(got) != 0 {
		t.Fatalf("nil input produced %+v", got)
	}
	if got := filterSlackNew([]core.SlackMessage{}, "", ""); len(got) != 0 {
		t.Fatalf("empty input produced %+v", got)
	}
}

// TestEdgeFilterSlackNewLeavesTheInputAlone: the caller commits the ORIGINAL
// slice's messages in the same pass (the mention loop re-floors a list it
// also dedupes), so a filter that reordered or truncated its input in place
// would silently drop messages from a caller that never asked it to.
func TestEdgeFilterSlackNewLeavesTheInputAlone(t *testing.T) {
	msgs := []core.SlackMessage{
		edgeSlackMsg("1769040000.000100", "UCARL"),
		edgeSlackMsg("1769040000.000200", "U9"),
		edgeSlackMsg("1769040000.000300", "U9"),
	}
	before := edgeTSOf(msgs)
	filterSlackNew(msgs, "1769040000.000150", "UCARL")
	if !edgeEqualStrings(edgeTSOf(msgs), before) {
		t.Fatalf("input became %v, was %v — filterSlackNew is pure", edgeTSOf(msgs), before)
	}
}

// --- mentionAfterDate ------------------------------------------------------

// edgeUTCDay renders the YYYY-MM-DD the contract specifies, in UTC.
func edgeUTCDay(sec int64) string {
	return time.Unix(sec, 0).UTC().Format("2006-01-02")
}

// TestEdgeMentionAfterDateFallsBackOnAnUnusableMark is this helper's whole
// reason for existing as a named function: `search.messages`' `after:` is
// DAY-grained, so a mark it cannot read must still produce a window rather
// than an empty query string — an empty `after:` would search the entire
// workspace history on every pass.
func TestEdgeMentionAfterDateFallsBackOnAnUnusableMark(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	want := edgeUTCDay(now.Unix() - 24*60*60)

	// Every mark here fails to yield a leading integer. A DOUBLE-DOTTED value
	// (`seconds.micros.micros`) deliberately is NOT in this table: the
	// SlackTSLess row's own semantics — "split on `.`, compare integer parts"
	// — read a seconds field out of exactly such a string, no slack_marks
	// writer can produce that shape, and the contract is silent on it, so
	// requiring the fallback there would be a strictness nobody froze
	// (arbitrated at the join, 2026-08-03).
	for _, mark := range []string{
		"",
		"   ",
		"not-a-timestamp",
		"abc.def",
		".000100",
		"-",
	} {
		got := mentionAfterDate(mark, now)
		if got != want {
			t.Errorf("mentionAfterDate(%q, now) = %q, want the now-24h fallback %q", mark, got, want)
		}
	}
}

// TestEdgeMentionAfterDateIsUTCAndDayGrained: the window is 24h before the
// MARK's seconds, rendered in UTC. A local-zone render would shift the day
// boundary and, west of UTC, ask Slack for a day that has not started there.
func TestEdgeMentionAfterDateIsUTCAndDayGrained(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		mark string
		want string
	}{
		// 2026-08-03T00:00:00Z minus 24h = 2026-08-02
		{"1785715200.000000", "2026-08-02"},
		// one second past midnight UTC — still the previous day
		{"1785715201.000000", "2026-08-02"},
		// one second BEFORE midnight UTC — 2026-08-02T23:59:59Z minus 24h
		{"1785715199.000000", "2026-08-01"},
		// a mark with no micros at all still reads its seconds
		{"1785715200", "2026-08-02"},
	}
	for _, c := range cases {
		if got := mentionAfterDate(c.mark, now); got != c.want {
			t.Errorf("mentionAfterDate(%q, now) = %q, want %q", c.mark, got, c.want)
		}
	}
	// The result is always a bare YYYY-MM-DD: anything else lands verbatim in
	// a Slack query string.
	got := mentionAfterDate("1785715200.000000", now)
	if len(got) != len("2006-01-02") || strings.ContainsAny(got, " T:Z") {
		t.Fatalf("mentionAfterDate = %q, want a bare YYYY-MM-DD", got)
	}
}

// --- slackBumpMessage ------------------------------------------------------

// TestEdgeSlackBumpMessageIsOneLine: this string is typed into a live claude
// pane with send-keys, where a newline is ENTER — a two-line bump submits its
// first line as a turn on its own.
func TestEdgeSlackBumpMessageIsOneLine(t *testing.T) {
	for _, n := range []int{-1, 0, 1, 2, 7, 1000} {
		got := slackBumpMessage(n)
		if strings.ContainsAny(got, "\n\r") {
			t.Errorf("slackBumpMessage(%d) = %q, want one line", n, got)
		}
		if strings.TrimSpace(got) == "" {
			t.Errorf("slackBumpMessage(%d) is blank", n)
		}
		if !strings.Contains(got, "kraken slack inbox") {
			t.Errorf("slackBumpMessage(%d) = %q, want it to name the verb to run", n, got)
		}
	}
}

// TestEdgeSlackBumpMessageSingularBoundary: n==1 is the ONLY singular, and
// the contract spells both strings out exactly.
func TestEdgeSlackBumpMessageSingularBoundary(t *testing.T) {
	if got, want := slackBumpMessage(1), "slack: 1 new message — run: kraken slack inbox"; got != want {
		t.Errorf("slackBumpMessage(1) = %q, want %q", got, want)
	}
	for _, n := range []int{0, 2, 3} {
		want := "slack: " + strconv.Itoa(n) + " new messages — run: kraken slack inbox"
		if got := slackBumpMessage(n); got != want {
			t.Errorf("slackBumpMessage(%d) = %q, want %q", n, got, want)
		}
	}
}

// --- findIris --------------------------------------------------------------

// TestEdgeFindIrisReturnsNilWhenAbsent sweeps every shape that is NOT iris.
// The consequence of a false positive is concrete: deliverSlack would type a
// slack bump into whatever pane this returned, and MarkSlackDelivered would
// stamp the rows as delivered to it.
func TestEdgeFindIrisReturnsNilWhenAbsent(t *testing.T) {
	cases := []struct {
		name string
		all  []*core.Peer
	}{
		{"nil discovery", nil},
		{"empty discovery", []*core.Peer{}},
		{"another session entirely", []*core.Peer{edgePeer("kraken-demo-1", "zeus-demo", 0)}},
		{"a session whose name merely starts with iris", []*core.Peer{edgePeer("iris-other", IrisAgentName, 0)}},
		{"a nested subagent in the iris session", []*core.Peer{edgePeer(IrisSessionName, "hephaestus", 2)}},
		{"only depth>0 claudes in the iris session", []*core.Peer{
			edgePeer(IrisSessionName, IrisAgentName, 1),
			edgePeer(IrisSessionName, "", 3),
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := findIris(c.all); got != nil {
				t.Fatalf("findIris = %+v, want nil", got)
			}
		})
	}
}

// TestEdgeFindIrisIgnoresAShellPane: a pane with no claude in it (Role
// "shell") is where carl left a terminal open in the iris session. Electing
// it would send the bump text to a bash prompt, which would then RUN it.
func TestEdgeFindIrisIgnoresAShellPane(t *testing.T) {
	shell := &core.Peer{Pane: "%4", TmuxSession: IrisSessionName, Role: "shell", Depth: 0}
	if got := findIris([]*core.Peer{shell}); got != nil {
		t.Fatalf("findIris elected a shell pane: %+v", got)
	}

	// With a real iris beside it, the shell must lose rather than merely be
	// tolerated.
	real := edgePeer(IrisSessionName, IrisAgentName, 0)
	got := findIris([]*core.Peer{shell, real})
	if got != real {
		t.Fatalf("findIris = %+v, want the claude peer", got)
	}
}

// TestEdgeFindIrisPrefersTheNamedPeer is the positional fallback's boundary:
// the fallback exists for the window before claude has written its session
// file (Peer.Name is empty then), so it must never OUTRANK a peer that
// already carries the name.
func TestEdgeFindIrisPrefersTheNamedPeer(t *testing.T) {
	nameless := edgePeer(IrisSessionName, "", 0)
	nameless.Pane = "%1"
	named := edgePeer(IrisSessionName, IrisAgentName, 0)
	named.Pane = "%9" // deliberately the higher pane number

	if got := findIris([]*core.Peer{nameless, named}); got != named {
		t.Fatalf("findIris = %+v, want the named peer even at a higher pane number", got)
	}
	// Alone, the nameless one IS the fallback.
	if got := findIris([]*core.Peer{nameless}); got != nameless {
		t.Fatalf("findIris = %+v, want the still-nameless depth-0 claude", got)
	}
}

// --- constants and paths ---------------------------------------------------

// TestEdgeIrisNamesAndDir pins the three identity constants and the
// home-relative path rule (CLAUDE.md: every path derives from util.Home()).
func TestEdgeIrisNamesAndDir(t *testing.T) {
	if IrisSessionName != "iris" {
		t.Errorf("IrisSessionName = %q, want \"iris\"", IrisSessionName)
	}
	if IrisAgentName != "iris" {
		t.Errorf("IrisAgentName = %q, want \"iris\"", IrisAgentName)
	}
	// 12, not the original 30: re-pinned by the review-round contract amendment
	// that re-derived the budget against the rate tier (12 calls per 15s pass =
	// 48/min under a ~50/min tier; 30 was 120/min). Applied by zeus post-join —
	// the authors were sighted by then, so contract-obsoleted assertions are the
	// arbiter's to update, not the author's.
	if slackHistoryBudget != 12 {
		t.Errorf("slackHistoryBudget = %d, want 12", slackHistoryBudget)
	}

	home := edgeSandboxHome(t)
	want := filepath.Join(home, ".kraken", "iris")
	if got := IrisDir(); got != want {
		t.Fatalf("IrisDir() = %q, want %q", got, want)
	}
	// And it must not collide with the cells tree — a cell dir named `iris-N`
	// is a different thing entirely.
	if strings.Contains(IrisDir(), filepath.Join(".kraken", "cells")) {
		t.Fatalf("IrisDir() = %q, want it outside ~/.kraken/cells", IrisDir())
	}
}

// --- irisBrief -------------------------------------------------------------

// TestEdgeIrisBriefStatesTheThreeObligations pins the brief-content clauses.
// These are the only place iris' behaviour is stated: there is no skill for
// it, so a brief missing one of them is a silent behaviour change.
func TestEdgeIrisBriefStatesTheThreeObligations(t *testing.T) {
	edgeSandboxHome(t)
	brief := irisBrief()
	for _, want := range []string{"you never start", "kraken slack ack", "## Source"} {
		if !strings.Contains(brief, want) {
			t.Errorf("irisBrief() does not contain %q", want)
		}
	}
	if !strings.Contains(brief, core.IrisModel) {
		t.Errorf("irisBrief() does not state the resolved model tier %q", core.IrisModel)
	}
}

// TestEdgeIrisBriefNeverAuthorizesQueueing is the REFUSAL half of the
// never-queue rule: the present-token checks above still pass on a brief that
// ALSO handed iris a way to start a job, so the absence of any such route is
// asserted separately.
//
// The literal `ready: true` is deliberately NOT on this list (arbitrated at
// the join, 2026-08-03, and still true for the same reason after the flag
// itself was retired). The contract requires the PRESENCE of the never-start
// rule and the prose that starting is carl's move; it does not forbid the
// words appearing — the brief's sentence saying the server REFUSES a draft
// carrying `ready` satisfies the prohibition while failing a bare absence
// check, so that check was a requirement nobody set.
//
// `/api/queue` joins the list because it is now the SUBMIT: the URL survived
// the rename (shipped iOS builds post to it), so a brief that named it would
// hand iris the exact capability the route's absence is supposed to deny.
func TestEdgeIrisBriefNeverAuthorizesQueueing(t *testing.T) {
	edgeSandboxHome(t)
	brief := irisBrief()
	for _, forbidden := range []string{"kraken job start", "/api/start", "/api/queue"} {
		if strings.Contains(brief, forbidden) {
			t.Errorf("irisBrief() contains %q — starting a job is carl's move, never iris'", forbidden)
		}
	}
}

// --- irisSpawnCommand ------------------------------------------------------

// edgeIrisCommand builds one spawn command with IrisDir already present, so a
// staging write cannot be the reason a case fails.
func edgeIrisCommand(t *testing.T, sessionID string, resume bool) string {
	t.Helper()
	if err := os.MkdirAll(IrisDir(), 0o755); err != nil {
		t.Fatalf("mkdir IrisDir: %v", err)
	}
	cmd, err := irisSpawnCommand(sessionID, resume)
	if err != nil {
		t.Fatalf("irisSpawnCommand(%q, %v): %v", sessionID, resume, err)
	}
	return cmd
}

// TestEdgeIrisSpawnCommandStagesTheBriefOnBothPaths is the measured
// regression the contract cites (thread.go:438): a resumed session does NOT
// retain an earlier run's --append-system-prompt, so a resume path that
// skipped the staging would come up as an iris with no instructions at all —
// and it would look completely healthy in `k ls`.
func TestEdgeIrisSpawnCommandStagesTheBriefOnBothPaths(t *testing.T) {
	edgeSandboxHome(t)
	const sid = "11111111-2222-3333-4444-555555555555"

	for _, resume := range []bool{false, true} {
		cmd := edgeIrisCommand(t, sid, resume)
		if !strings.Contains(cmd, "--append-system-prompt") {
			t.Errorf("resume=%v: command has no --append-system-prompt:\n%s", resume, cmd)
		}
		if !strings.Contains(cmd, "$(cat ") {
			t.Errorf("resume=%v: brief is not read by the shell — tmux caps a command at ~16KB:\n%s", resume, cmd)
		}
	}
}

// TestEdgeIrisSpawnCommandSessionFlagsAreExclusive: `--session-id` MINTS a
// session and `-r` RESUMES one. A command carrying both, or the wrong one for
// its path, either starts a second claude on an existing transcript or
// abandons iris' whole history on every respawn.
func TestEdgeIrisSpawnCommandSessionFlagsAreExclusive(t *testing.T) {
	edgeSandboxHome(t)
	const sid = "11111111-2222-3333-4444-555555555555"

	fresh := edgeIrisCommand(t, sid, false)
	if !strings.Contains(fresh, "--session-id "+sid) {
		t.Errorf("first spawn does not mint the session id:\n%s", fresh)
	}
	if strings.Contains(fresh, " -r ") {
		t.Errorf("first spawn carries -r:\n%s", fresh)
	}

	resumed := edgeIrisCommand(t, sid, true)
	if !strings.Contains(resumed, "-r "+sid) {
		t.Errorf("resume does not resume the stored session id:\n%s", resumed)
	}
	if strings.Contains(resumed, "--session-id") {
		t.Errorf("resume carries --session-id — that would mint a second session:\n%s", resumed)
	}
}

// TestEdgeIrisSpawnCommandNamesTheModelAndAgent: the model policy says kraken
// never leaves the model to claude's own config, and the --name is what makes
// findIris' preferred arm work at all.
func TestEdgeIrisSpawnCommandNamesTheModelAndAgent(t *testing.T) {
	edgeSandboxHome(t)
	cmd := edgeIrisCommand(t, "11111111-2222-3333-4444-555555555555", false)

	if !strings.Contains(cmd, core.IrisModel) {
		t.Errorf("command does not name the model %q:\n%s", core.IrisModel, cmd)
	}
	if strings.Contains(cmd, core.DefaultModel) {
		t.Errorf("command names DefaultModel — iris' tier is its own constant:\n%s", cmd)
	}
	if !strings.Contains(cmd, "--name "+IrisAgentName) {
		t.Errorf("command does not pass --name %s:\n%s", IrisAgentName, cmd)
	}
	if !strings.Contains(cmd, "--dangerously-skip-permissions") {
		t.Errorf("command omits --dangerously-skip-permissions — iris has no one to answer a prompt:\n%s", cmd)
	}
}

// --- EnsureIris ------------------------------------------------------------

// TestEdgeEnsureIrisIsANoOpWhenSlackIsUnconfigured pins the degradation the
// manual acceptance script's step 8 describes: with slack.env moved aside,
// nothing is spawned, nothing is stored, and — the part that matters most —
// it is not an ERROR, since a fresh install would otherwise log one every
// tick forever.
func TestEdgeEnsureIrisIsANoOpWhenSlackIsUnconfigured(t *testing.T) {
	st := useTestStore(t)
	// useTestStore points HOME at its own temp dir; re-sandbox afterwards so
	// slack.env is provably absent and the two env keys are provably clear.
	edgeSandboxHome(t)

	for _, live := range []map[string]bool{nil, {}, {"kraken-demo-1": true}} {
		if err := EnsureIris(st, live); err != nil {
			t.Fatalf("EnsureIris with Slack unconfigured: %v — ErrNoSlack is feature-off, not a failure", err)
		}
	}
	if v, err := st.GetKV("iris_agent"); err == nil && v != "" {
		t.Fatalf("iris_agent = %q — nothing may be minted while Slack is unconfigured", v)
	}
	if _, err := os.Stat(IrisDir()); err == nil {
		t.Fatalf("IrisDir %s was created while Slack is unconfigured", IrisDir())
	}
	// The reason it is a no-op, spelled out so a future refactor that swallows
	// every error still fails this.
	if _, err := core.LoadSlackConfig(); !errors.Is(err, core.ErrNoSlack) {
		t.Fatalf("fixture is wrong: LoadSlackConfig err = %v, want ErrNoSlack", err)
	}
}

// --- deliverSlack ----------------------------------------------------------

// edgeInboxFixture is two undelivered, unacked inbox rows — the state the
// delivery pass is meant to act on.
func edgeInboxFixture() []store.SlackInboxMsg {
	return []store.SlackInboxMsg{
		{ChannelID: "D1", ChannelName: "carl", TS: "1769040000.000100",
			AuthorID: "U0001", AuthorName: "someone", Source: "dm",
			Text: "the csv export times out", SeenAt: 1769040000000},
		{ChannelID: "D1", ChannelName: "carl", TS: "1769040000.000200",
			AuthorID: "U0001", AuthorName: "someone", Source: "dm",
			Text: "any idea?", SeenAt: 1769040000000},
	}
}

// TestEdgeDeliverSlackNoIrisNoStamp is the send-then-watermark rule's
// absent-peer half: with no iris to bump, the rows must stay at
// delivered_at 0 so the NEXT tick retries. Stamping them here would swallow
// every message that arrived while iris was down.
func TestEdgeDeliverSlackNoIrisNoStamp(t *testing.T) {
	st := useTestStore(t)
	if _, err := st.AddSlackBatch(edgeInboxFixture(), nil); err != nil {
		t.Fatalf("AddSlackBatch: %v", err)
	}
	before, err := st.UndeliveredSlackInbox()
	if err != nil {
		t.Fatalf("UndeliveredSlackInbox: %v", err)
	}
	if len(before) != 2 {
		t.Fatalf("fixture seeded %d undelivered rows, want 2", len(before))
	}

	// No iris anywhere in discovery — the ONLY safe peer set for this test,
	// since a matching peer would drive a real tmux send.
	for _, all := range [][]*core.Peer{
		nil,
		{},
		{edgePeer("kraken-demo-1", "zeus-demo", 0)},
	} {
		deliverSlack(st, all)
		after, err := st.UndeliveredSlackInbox()
		if err != nil {
			t.Fatalf("UndeliveredSlackInbox: %v", err)
		}
		if len(after) != 2 {
			t.Fatalf("undelivered = %d after a pass with no iris, want 2 — rows stay pending", len(after))
		}
	}
}

// TestEdgeDeliverSlackWithNothingPendingIsSilent: the common case, every
// tick. It must not panic on an empty inbox and must not bump anyone.
func TestEdgeDeliverSlackWithNothingPendingIsSilent(t *testing.T) {
	st := useTestStore(t)
	deliverSlack(st, nil)
	deliverSlack(st, []*core.Peer{edgePeer("kraken-demo-1", "zeus-demo", 0)})

	total, undelivered, unacked, err := st.SlackInboxCounts()
	if err != nil {
		t.Fatalf("SlackInboxCounts: %v", err)
	}
	if total != 0 || undelivered != 0 || unacked != 0 {
		t.Fatalf("counts = %d/%d/%d on an empty inbox, want all zero", total, undelivered, unacked)
	}
}
