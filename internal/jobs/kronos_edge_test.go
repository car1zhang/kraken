package jobs

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	_ "github.com/mattn/go-sqlite3"

	"kraken/internal/core"
	"kraken/internal/store"
)

// kronos_edge_test.go — the EDGE dimension of Slice C of
// artifacts/50-contract.md (the kronos build): the opt-in gate's off state,
// EnsureKronos' three no-op arms, the spawn command's two mutually exclusive
// flag shapes and the replacement-vs-append distinction, the settings merge's
// exclusions and its idempotence, EnqueueOrch's cap and one-lining,
// deliverOrch's absent-kronos silence, RecordKronosContext's refusals,
// ctxPass' re-arm and reset boundaries, and RearmKronosRecall's three
// refusals. Happy paths and the stated invariants belong to the sibling blind
// file.
//
// Written blind: no byte of internal/jobs/kronos.go, orch.go, kronosctx.go or
// daemon.go was read while authoring this.
//
// The iMessage bridge this file also covered was removed whole at the manual
// gate (carl's call), and every test of it went with it — the inbound pass, the
// send, the chunker and the ROWID watermark. The context warning survives, and
// its delivery is now the macOS notification alone.
//
// NOTHING here reaches tmux, osascript or the network. Every warning-path test
// swaps notifyFn (the package's own notification seam); every pass test either
// runs with the feature gated OFF or with no kronos peer in the snapshot, which
// is the contract's stated silent no-op.
//
// Every package-level identifier declared here is prefixed `edgeKronos` — this
// package's test namespace is shared with the other blind author and with the
// pre-existing `edge*` files.

// --- helpers ---------------------------------------------------------------

// edgeKronosEnv sandboxes HOME (via useTestStore) and returns the store. The
// feature is left OFF: a test that wants it on calls edgeKronosEnable.
func edgeKronosEnv(t *testing.T) *store.Store {
	t.Helper()
	return useTestStore(t)
}

// edgeKronosKrakenDir is ~/.kraken under the sandboxed HOME. KronosDir() is
// the contract's own accessor for the directory beside it, so the parent is
// derived from that rather than re-spelled.
func edgeKronosKrakenDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Dir(KronosDir())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	return dir
}

// edgeKronosEnable creates ~/.kraken/kronos.env — the whole feature's opt-in —
// as an EMPTY file, which the contract says counts as on.
func edgeKronosEnable(t *testing.T) {
	t.Helper()
	dir := edgeKronosKrakenDir(t)
	if err := os.WriteFile(filepath.Join(dir, "kronos.env"), nil, 0o600); err != nil {
		t.Fatalf("write kronos.env: %v", err)
	}
}

// edgeKronosStageDir creates KronosDir so a spawn-command builder has
// somewhere to stage its two files.
func edgeKronosStageDir(t *testing.T) string {
	t.Helper()
	if err := os.MkdirAll(KronosDir(), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", KronosDir(), err)
	}
	return KronosDir()
}

// edgeKronosRunLog records every message the notification seam was handed.
//
// It is mutex-guarded rather than a bare slice because of the round-2
// amendment "ctxPass warning sends run off the tick": the send happens in a
// fire-and-forget goroutine, so the seam is called from a goroutine the test
// never joins directly, and an unguarded append would be a data race even when
// the counts happened to come out right.
type edgeKronosRunLog struct {
	mu      sync.Mutex
	scripts []string
}

func (l *edgeKronosRunLog) add(script string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.scripts = append(l.scripts, script)
}

func (l *edgeKronosRunLog) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.scripts)
}

func (l *edgeKronosRunLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.scripts...)
}

// last is the most recent message, or "" when nothing has been sent.
func (l *edgeKronosRunLog) last() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.scripts) == 0 {
		return ""
	}
	return l.scripts[len(l.scripts)-1]
}

// edgeKronosSwapNotify installs a capturing seam over notifyFn — the context
// warning's whole delivery now that the iMessage bridge is gone — for the
// duration of a test. Title and body are recorded joined, so an assertion can
// read either half.
func edgeKronosSwapNotify(t *testing.T) *edgeKronosRunLog {
	t.Helper()
	log := &edgeKronosRunLog{}
	prev := notifyFn
	notifyFn = func(title, msg string) { log.add(title + " " + msg) }
	t.Cleanup(func() { notifyFn = prev })
	return log
}

// edgeKronosCtxPass runs ctxPass and JOINS the asynchronous send it may have
// started, so an assertion made after it is measuring a finished tick rather
// than a race.
//
// Round-2 amendment: "ctxPass warning sends run off the tick (fire-and-forget
// goroutine, single-flighted (an in-flight send skips the tick's attempt; the
// fired-set still only advances after a confirmed send))." Two consequences
// the tests have to respect. A positive assertion (a warning WAS sent, the
// fired-set advanced) has to wait for the goroutine. And a NEGATIVE one has to
// wait too, in both directions: a send that has not started yet would read as
// silence, and — worse — a send still in flight from the previous step would
// make the NEXT ctxPass skip its own attempt, so a test that did not join
// would be measuring the single-flight rather than the threshold ladder.
//
// The join is on the single-flight gate itself, which ctxPass claims
// synchronously before it spawns the goroutine and releases only after the
// send, the fired-set write and the event have all completed. The second
// settle-and-recheck is the cheap insurance against the gate being claimed
// inside the goroutine instead: a claim that has not happened yet cannot be
// waited on.
func edgeKronosCtxPass(t *testing.T, st *store.Store) {
	t.Helper()
	ctxPass(st)
	edgeKronosAwaitCtxIdle(t)
	time.Sleep(25 * time.Millisecond)
	edgeKronosAwaitCtxIdle(t)
}

// edgeKronosAwaitCtxIdle polls for up to 2s in 10ms steps.
func edgeKronosAwaitCtxIdle(t *testing.T) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if !kronosCtxSending.Load() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("ctxPass' warning send was still in flight after 2s — the tick never settled")
}

// edgeKronosOrchSummaries reads the unacked rows' summaries in order.
func edgeKronosOrchSummaries(t *testing.T, st *store.Store) []string {
	t.Helper()
	rows, err := st.UnackedOrch()
	if err != nil {
		t.Fatalf("UnackedOrch: %v", err)
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Summary)
	}
	return out
}

// edgeKronosKVMissing asserts a kv key has never been written.
func edgeKronosKVMissing(t *testing.T, st *store.Store, key, why string) {
	t.Helper()
	raw, err := st.GetKV(key)
	if errors.Is(err, store.ErrNotFound) {
		return
	}
	t.Fatalf("kv %q = %q/%v, want unset — %s", key, raw, err, why)
}

// --- the opt-in gate --------------------------------------------------------

// TestEdgeKronosEnabledIsOffWithoutTheFile: "True iff ~/.kraken/kronos.env
// exists". Off is the default on every machine, and every pass keys off it —
// a gate that read as ON by accident would spawn an agent and start typing
// into panes on a machine nobody opted in.
func TestEdgeKronosEnabledIsOffWithoutTheFile(t *testing.T) {
	edgeKronosEnv(t)
	edgeKronosKrakenDir(t)
	if KronosEnabled() {
		t.Fatal("KronosEnabled() = true with no ~/.kraken/kronos.env")
	}
}

// TestEdgeKronosEnabledEmptyFileCountsAsOn: "(empty file = on)". The file is a
// switch, not a config: requiring content would make `touch kronos.env` — the
// documented way to turn it on — silently do nothing.
func TestEdgeKronosEnabledEmptyFileCountsAsOn(t *testing.T) {
	edgeKronosEnv(t)
	edgeKronosEnable(t)
	if !KronosEnabled() {
		t.Fatal("KronosEnabled() = false with an EMPTY ~/.kraken/kronos.env — an empty file is on")
	}
}

// TestEdgeKronosDirIsHomeRelative: every path in the binary derives from
// util.Home(), so a sandboxed HOME must redirect this one — otherwise every
// test in this file would stage files into the developer's real ~/.kraken.
func TestEdgeKronosDirIsHomeRelative(t *testing.T) {
	edgeKronosEnv(t)
	if !strings.HasSuffix(KronosDir(), filepath.Join(".kraken", "kronos")) {
		t.Fatalf("KronosDir() = %q, want it to end in .kraken/kronos", KronosDir())
	}
	// IrisDir() is the neighbouring kraken-owned dir under the SAME sandboxed
	// HOME, so comparing their parents proves KronosDir followed util.Home()
	// rather than a hardcoded /Users/<name>.
	if filepath.Dir(KronosDir()) != filepath.Dir(IrisDir()) {
		t.Fatalf("KronosDir() = %q does not sit beside IrisDir() = %q — it did not derive from util.Home()",
			KronosDir(), IrisDir())
	}
}

// TestEdgeKronosIdentityConstants: the tmux session and the --name are both
// "kronos". findKronos matches on the name and EnsureKronos gates on the
// session, so a drift between the two would make the liveness gate and the
// delivery lookup disagree about whether kronos exists.
func TestEdgeKronosIdentityConstants(t *testing.T) {
	if KronosSessionName != "kronos" {
		t.Errorf("KronosSessionName = %q, want %q", KronosSessionName, "kronos")
	}
	if KronosAgentName != "kronos" {
		t.Errorf("KronosAgentName = %q, want %q", KronosAgentName, "kronos")
	}
}

// --- EnsureKronos: the three arms that must never touch tmux ---------------

// TestEdgeEnsureKronosNilStore: "nil st → nil". A file-mode entrypoint has
// nowhere to record the session id, and minting one it cannot persist would
// orphan the conversation on the very next tick.
func TestEdgeEnsureKronosNilStore(t *testing.T) {
	edgeKronosEnv(t)
	edgeKronosEnable(t)
	if err := EnsureKronos(nil, map[string]bool{}); err != nil {
		t.Fatalf("EnsureKronos(nil store) = %v, want nil", err)
	}
	if _, err := os.Stat(KronosDir()); err == nil {
		t.Fatalf("EnsureKronos(nil store) created %s — it must do nothing at all", KronosDir())
	}
}

// TestEdgeEnsureKronosDisabledIsSilent: with the opt-in file absent the whole
// feature is off, and off must mint NOTHING — a recorded session id with no
// process behind it would be resumed forever by every later tick, on a machine
// that never asked for kronos.
func TestEdgeEnsureKronosDisabledIsSilent(t *testing.T) {
	st := edgeKronosEnv(t)
	edgeKronosKrakenDir(t)

	if err := EnsureKronos(st, map[string]bool{}); err != nil {
		t.Fatalf("EnsureKronos with the feature off = %v, want nil (off is not an error)", err)
	}
	edgeKronosKVMissing(t, st, "kronos_agent", "an opted-out machine must mint nothing")
	if _, err := os.Stat(KronosDir()); err == nil {
		t.Fatalf("EnsureKronos created %s while the feature was off", KronosDir())
	}
}

// TestEdgeEnsureKronosLiveSessionIsSilent: "live[KronosSessionName] → nil
// (liveness gate is the tmux session name, never the discovered peer)". claude
// writes its session file seconds into startup, so a peer-keyed gate would
// read a booting kronos as absent and spawn a SECOND `claude -r` on one
// session id — two writers on one transcript, which is unrecoverable.
func TestEdgeEnsureKronosLiveSessionIsSilent(t *testing.T) {
	st := edgeKronosEnv(t)
	edgeKronosEnable(t)

	if err := EnsureKronos(st, map[string]bool{KronosSessionName: true}); err != nil {
		t.Fatalf("EnsureKronos with the kronos session live = %v, want nil", err)
	}
	edgeKronosKVMissing(t, st, "kronos_agent", "a live session is never re-minted")
}

// TestEdgeEnsureKronosLiveGateIsExactlyTheSessionName: a map carrying OTHER
// live sessions (a cell, iris) must not be mistaken for kronos being up —
// substring or prefix matching here would leave kronos permanently unspawned
// on a machine that happens to run a job called `kronos-something`.
//
// The feature is left OFF for this case so the assertion can be made without
// the arm underneath it reaching tmux: what is measured is that these session
// names are not the gate, which the no-op-when-off arm proves by the kv row
// staying unwritten in both.
func TestEdgeEnsureKronosLiveGateIsExactlyTheSessionName(t *testing.T) {
	st := edgeKronosEnv(t)
	edgeKronosKrakenDir(t)

	for _, live := range []map[string]bool{
		{"kraken-kronos-1": true},
		{"kronos-scratch": true},
		{"iris": true},
		{"KRONOS": true},
	} {
		if err := EnsureKronos(st, live); err != nil {
			t.Fatalf("EnsureKronos(live=%v) = %v, want nil", live, err)
		}
	}
	edgeKronosKVMissing(t, st, "kronos_agent", "the feature is off in every one of these cases")
}

// --- kronosSpawnCommand -----------------------------------------------------

// TestEdgeKronosSpawnCommandFlagsAreMutuallyExclusive: a first spawn NAMES the
// session and a resume RESUMES it, and carrying both would either mint a
// second conversation or hand claude two contradictory ids.
func TestEdgeKronosSpawnCommandFlagsAreMutuallyExclusive(t *testing.T) {
	edgeKronosEnv(t)
	edgeKronosStageDir(t)
	const sid = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"

	first, err := kronosSpawnCommand(sid, false)
	if err != nil {
		t.Fatalf("kronosSpawnCommand(first): %v", err)
	}
	if !strings.Contains(first, "--session-id "+sid) {
		t.Errorf("first spawn does not carry --session-id %s: %q", sid, first)
	}
	if strings.Contains(first, "-r "+sid) {
		t.Errorf("first spawn resumes instead of naming a new session: %q", first)
	}

	resume, err := kronosSpawnCommand(sid, true)
	if err != nil {
		t.Fatalf("kronosSpawnCommand(resume): %v", err)
	}
	if !strings.Contains(resume, "-r "+sid) {
		t.Errorf("resume does not carry -r %s: %q", sid, resume)
	}
	if strings.Contains(resume, "--session-id") {
		t.Errorf("resume mints a new session id: %q", resume)
	}
}

// TestEdgeKronosSpawnCommandReplacesRatherThanAppends is the distinction the
// whole brief design rests on: `--system-prompt-file` REPLACES the harness'
// default system prompt, and `--append-system-prompt` does not. Silently using
// the append form would leave kronos with default guidance plus its brief —
// which is not what the brief was written against (it carries its own
// tool-usage section precisely because replacement drops the default one).
func TestEdgeKronosSpawnCommandReplacesRatherThanAppends(t *testing.T) {
	edgeKronosEnv(t)
	edgeKronosStageDir(t)
	const sid = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"

	for _, resume := range []bool{false, true} {
		cmd, err := kronosSpawnCommand(sid, resume)
		if err != nil {
			t.Fatalf("kronosSpawnCommand(resume=%v): %v", resume, err)
		}
		if !strings.Contains(cmd, "--system-prompt-file") {
			t.Errorf("resume=%v: command carries no --system-prompt-file: %q", resume, cmd)
		}
		if strings.Contains(cmd, "--append-system-prompt") {
			t.Errorf("resume=%v: command uses --append-system-prompt — the contract requires FULL replacement: %q",
				resume, cmd)
		}
		if !strings.Contains(cmd, filepath.Join(KronosDir(), "kronos-brief.md")) {
			t.Errorf("resume=%v: command does not name %s: %q", resume, filepath.Join(KronosDir(), "kronos-brief.md"), cmd)
		}
	}
}

// TestEdgeKronosSpawnCommandRestagesTheBriefOnEveryResume: "The brief FILE is
// (re)written on EVERY spawn, resume included." A stale file left from a
// previous build would hand a resumed kronos an out-of-date policy while every
// flag still looked right — the failure with no symptom.
func TestEdgeKronosSpawnCommandRestagesTheBriefOnEveryResume(t *testing.T) {
	edgeKronosEnv(t)
	dir := edgeKronosStageDir(t)
	briefPath := filepath.Join(dir, "kronos-brief.md")
	const stale = "EDGE-KRONOS-STALE-BRIEF-FROM-A-PREVIOUS-BUILD"

	for _, resume := range []bool{false, true} {
		if err := os.WriteFile(briefPath, []byte(stale), 0o644); err != nil {
			t.Fatalf("seed a stale brief: %v", err)
		}
		if _, err := kronosSpawnCommand("aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", resume); err != nil {
			t.Fatalf("kronosSpawnCommand(resume=%v): %v", resume, err)
		}
		got, err := os.ReadFile(briefPath)
		if err != nil {
			t.Fatalf("resume=%v: read the staged brief: %v", resume, err)
		}
		if strings.Contains(string(got), stale) {
			t.Fatalf("resume=%v: the stale brief survived — the file must be rewritten on EVERY spawn", resume)
		}
		if strings.TrimSpace(string(got)) == "" {
			t.Fatalf("resume=%v: the staged brief is empty", resume)
		}
	}
}

// TestEdgeKronosSpawnCommandStagesRatherThanInlines: tmux caps a command at
// ~16KB and rejects anything longer with a bare `command too long`, which is
// exactly how an inlined brief once wedged every cell spawn. The brief's own
// text must therefore not appear on the command line, and the prompt must
// arrive through `"$(cat …)"`.
func TestEdgeKronosSpawnCommandStagesRatherThanInlines(t *testing.T) {
	edgeKronosEnv(t)
	edgeKronosStageDir(t)

	for _, resume := range []bool{false, true} {
		cmd, err := kronosSpawnCommand("aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", resume)
		if err != nil {
			t.Fatalf("kronosSpawnCommand(resume=%v): %v", resume, err)
		}
		if strings.Contains(cmd, "Ack every inbox row you read, exactly once.") {
			t.Errorf("resume=%v: the brief text is inlined on the command line (%d bytes)", resume, len(cmd))
		}
		if !strings.Contains(cmd, "$(cat ") {
			t.Errorf("resume=%v: nothing is read from a staged file: %q", resume, cmd)
		}
		if !strings.Contains(cmd, filepath.Join(KronosDir(), "kronos-prompt.md")) {
			t.Errorf("resume=%v: the initial prompt is not staged to kronos-prompt.md: %q", resume, cmd)
		}
		if len(cmd) > 4096 {
			t.Errorf("resume=%v: command is %d bytes — the brief belongs in a file", resume, len(cmd))
		}
		// `--` stays last: everything past it is claude's initial prompt, so a
		// flag appended after it would be swallowed into the seed text.
		if i, k := strings.Index(cmd, " -- "), strings.Index(cmd, "--system-prompt-file"); i >= 0 && i < k {
			t.Errorf("resume=%v: -- comes before --system-prompt-file: %q", resume, cmd)
		}
	}
}

// TestEdgeKronosSpawnCommandCarriesTheFixedFlags: the model, the name and the
// permission flag are all on the command line, where discovery reads them back
// from. A missing --model would leave kronos on whatever tier claude's own
// config defaults to — a tier drop nothing surfaces.
func TestEdgeKronosSpawnCommandCarriesTheFixedFlags(t *testing.T) {
	edgeKronosEnv(t)
	edgeKronosStageDir(t)

	for _, resume := range []bool{false, true} {
		cmd, err := kronosSpawnCommand("aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", resume)
		if err != nil {
			t.Fatalf("kronosSpawnCommand(resume=%v): %v", resume, err)
		}
		for _, want := range []string{
			"--dangerously-skip-permissions",
			"--model " + core.KronosModel,
			"--name " + KronosAgentName,
		} {
			if !strings.Contains(cmd, want) {
				t.Errorf("resume=%v: command %q missing %q", resume, cmd, want)
			}
		}
		if core.KronosModel != "claude-fable-5-1" {
			t.Errorf("core.KronosModel = %q, want the exact id %q (an alias silently follows whatever ships next)",
				core.KronosModel, "claude-fable-5-1")
		}
	}
}

// --- kronosBrief ------------------------------------------------------------

// TestEdgeKronosBriefNamesEveryVerbItGrants: the brief is kronos' whole
// capability list, and a verb it is never told about is a capability it will
// not use — the failure is silent (kronos simply never acks, or never
// escalates) rather than an error anywhere.
func TestEdgeKronosBriefNamesEveryVerbItGrants(t *testing.T) {
	edgeKronosEnv(t)
	brief := kronosBrief()
	if strings.TrimSpace(brief) == "" {
		t.Fatal("kronosBrief() is empty")
	}
	for _, verb := range []string{
		"kraken orch inbox",
		"kraken orch ack",
		"kraken board get",
		"kraken cell steer",
		"kraken cell submit",
		"kraken cell answer",
		"kraken graph",
		"kraken goals get",
	} {
		if !strings.Contains(brief, verb) {
			t.Errorf("kronosBrief() never names %q", verb)
		}
	}
	if !strings.Contains(brief, core.KronosModel) {
		t.Errorf("kronosBrief() does not state the resolved model id %q", core.KronosModel)
	}
}

// TestEdgeKronosBriefRestatesNoSkill: the brief is prose in Go, re-sent every
// turn; a skill is read once. Copying a skill into it is how the two drift out
// of step, which is the failure TestZeusBriefDoesNotRestateSkills exists for
// on the cell side — and kronos has no skills at all, so a reference to one
// would point at a file that is not in its directory.
func TestEdgeKronosBriefRestatesNoSkill(t *testing.T) {
	edgeKronosEnv(t)
	brief := kronosBrief()
	for _, forbidden := range []string{".claude/skills", "/aim", "/do", "/dream", "SKILL.md"} {
		if strings.Contains(brief, forbidden) {
			t.Errorf("kronosBrief() references %q — the brief restates no skill", forbidden)
		}
	}
}

// TestEdgeKronosBriefStaysLean: the path rides the argv rather than the
// content, so tmux's ~16KB cap does not apply — but a brief that grows without
// bound is re-sent on every single turn of an eternal conversation, which is
// the cost this ceiling pins. The number is generous on purpose; it catches a
// brief that swallowed a skill, not one that gained a paragraph.
func TestEdgeKronosBriefStaysLean(t *testing.T) {
	edgeKronosEnv(t)
	if n := len(kronosBrief()); n > 16000 {
		t.Fatalf("kronosBrief() is %d bytes — a system prompt re-sent every turn must stay lean", n)
	}
}

// --- installKronosSettings --------------------------------------------------

// TestEdgeInstallKronosSettingsExcludesCellOnlyEntries: kronos is not a cell.
// It has no repo, no branch and no PR, so pr-hook has nothing to watch, and
// the agent-teams keys would let kronos spawn a crew, which is not a
// capability it is granted anywhere. The limit-hook is NOT on this list any
// more: it is installed in its --agent form, which files the hit under
// kronos' own standing-agent key rather than against a job id that does not
// exist.
func TestEdgeInstallKronosSettingsExcludesCellOnlyEntries(t *testing.T) {
	edgeKronosEnv(t)
	dir := edgeKronosStageDir(t)
	if err := installKronosSettings(dir, "/usr/local/bin/kraken"); err != nil {
		t.Fatalf("installKronosSettings: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	body := string(raw)
	for _, forbidden := range []string{
		"pr-hook",
		"CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS",
		"teammateMode",
		"autoCompactEnabled",
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("settings.json contains %q — it must not:\n%s", forbidden, body)
		}
	}
}

// TestEdgeInstallKronosSettingsWritesTheFourGroups: the two recall hooks are
// what give kronos the graph continuously, rearm-hook is what undoes recall's
// once-per-session marks after a compaction, and ctx-hook is the whole context
// sentinel. Each is invisible when missing — kronos just quietly stops
// receiving something.
func TestEdgeInstallKronosSettingsWritesTheFourGroups(t *testing.T) {
	edgeKronosEnv(t)
	dir := edgeKronosStageDir(t)
	const bin = "/edge/kronos/bin/kraken"
	if err := installKronosSettings(dir, bin); err != nil {
		t.Fatalf("installKronosSettings: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	body := string(raw)
	// PostToolUse is deliberately NOT in this list any more: recall's
	// per-tool-call arm retired on measurement (see
	// TestRecallHookEventsPinsTheWiring) and kronos has no pr-hook, so
	// nothing kraken writes lands under that event at all.
	for _, want := range []string{
		"UserPromptSubmit", "PreToolUse", "PostCompact",
		"recall-hook", "bash-lint", "rearm-hook", "ctx-hook", "statusLine", bin,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("settings.json does not carry %q:\n%s", want, body)
		}
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("settings.json is not valid JSON: %v\n%s", err, body)
	}
}

// TestEdgeInstallKronosSettingsPreservesUnrelatedEntries: "Merge preserves
// unrelated user entries (never clobbers the file)." carl edits this file by
// hand; a clobbering write would silently drop whatever he put there, and the
// only evidence would be the behaviour he configured quietly stopping.
func TestEdgeInstallKronosSettingsPreservesUnrelatedEntries(t *testing.T) {
	edgeKronosEnv(t)
	dir := edgeKronosStageDir(t)
	claudeDir := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("mkdir .claude: %v", err)
	}
	pre := `{
	  "edgeKronosUnrelatedKey": "keep-me",
	  "hooks": {
	    "Notification": [{"hooks": [{"type": "command", "command": "/edge/kronos/notify"}]}]
	  }
	}`
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(pre), 0o644); err != nil {
		t.Fatalf("seed settings.json: %v", err)
	}

	if err := installKronosSettings(dir, "/edge/kronos/bin/kraken"); err != nil {
		t.Fatalf("installKronosSettings: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	body := string(raw)
	if !strings.Contains(body, "keep-me") {
		t.Errorf("the merge dropped an unrelated top-level key:\n%s", body)
	}
	if !strings.Contains(body, "/edge/kronos/notify") {
		t.Errorf("the merge dropped an unrelated hook group:\n%s", body)
	}
	if !strings.Contains(body, "recall-hook") {
		t.Errorf("the merge did not add its own entries:\n%s", body)
	}
}

// TestEdgeInstallKronosSettingsIsIdempotent: the daemon may call this on every
// spawn, and the recall hooks are REPLACE-merged. Two calls must therefore
// leave ONE of each entry — a duplicated UserPromptSubmit hook fires
// `kraken recall-hook` twice per prompt, which double-marks the facts it
// delivered and burns kronos' once-per-fact budget.
func TestEdgeInstallKronosSettingsIsIdempotent(t *testing.T) {
	edgeKronosEnv(t)
	dir := edgeKronosStageDir(t)
	const bin = "/edge/kronos/bin/kraken"

	if err := installKronosSettings(dir, bin); err != nil {
		t.Fatalf("first installKronosSettings: %v", err)
	}
	first, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	if err := installKronosSettings(dir, bin); err != nil {
		t.Fatalf("second installKronosSettings: %v", err)
	}
	second, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
	if err != nil {
		t.Fatalf("re-read settings.json: %v", err)
	}

	for _, cmd := range []string{"recall-hook", "rearm-hook", "ctx-hook", "bash-lint"} {
		before := strings.Count(string(first), cmd)
		after := strings.Count(string(second), cmd)
		if before != after {
			t.Errorf("%q appears %d time(s) after one install and %d after two — the merge duplicates entries",
				cmd, before, after)
		}
	}
}

// --- EnqueueOrch ------------------------------------------------------------

// TestEdgeEnqueueOrchCapsAtTwoThousandRunes: "summary one-lined + capped at
// 2000 runes", flat across every kind. The cap is in RUNES: a byte cap would
// both cut a multi-byte rune in half and let a 2000-emoji summary through at
// four times the size.
func TestEdgeEnqueueOrchCapsAtTwoThousandRunes(t *testing.T) {
	st := edgeKronosEnv(t)
	long := strings.Repeat("é", 2500)
	if err := EnqueueOrch(st, "job-blocked", "some-job", long); err != nil {
		t.Fatalf("EnqueueOrch: %v", err)
	}
	got := edgeKronosOrchSummaries(t, st)
	if len(got) != 1 {
		t.Fatalf("summaries = %+v, want one row", got)
	}
	if n := utf8.RuneCountInString(got[0]); n > 2000 {
		t.Fatalf("summary is %d runes, want at most 2000", n)
	}
	if !utf8.ValidString(got[0]) {
		t.Fatal("the cap cut a multi-byte rune in half")
	}
	// A summary already under the cap is not touched.
	if err := EnqueueOrch(st, "job-blocked", "some-job", "short and sweet"); err != nil {
		t.Fatalf("EnqueueOrch short: %v", err)
	}
	got = edgeKronosOrchSummaries(t, st)
	if got[len(got)-1] != "short and sweet" {
		t.Fatalf("a short summary was altered: %q", got[len(got)-1])
	}
}

// TestEdgeEnqueueOrchOneLinesTheSummary: the summary is rendered as one row of
// an inbox listing and typed nowhere, but a raw newline in it breaks the JSON
// listing's readability and, worse, lets a crafted summary forge what looks
// like a second inbox row.
func TestEdgeEnqueueOrchOneLinesTheSummary(t *testing.T) {
	st := edgeKronosEnv(t)
	if err := EnqueueOrch(st, "note", "", "line one\nline two\r\nline three\ttabbed"); err != nil {
		t.Fatalf("EnqueueOrch: %v", err)
	}
	got := edgeKronosOrchSummaries(t, st)
	if len(got) != 1 {
		t.Fatalf("summaries = %+v, want one row", got)
	}
	for _, bad := range []string{"\n", "\r"} {
		if strings.Contains(got[0], bad) {
			t.Fatalf("summary %q still contains %q", got[0], bad)
		}
	}
}

// TestEdgeEnqueueOrchRefusesAnEmptyKind: the wrapper is thin, so the store's
// refusal has to survive it — a kind swallowed here would be a row nothing can
// describe, and it would still be enqueued.
func TestEdgeEnqueueOrchRefusesAnEmptyKind(t *testing.T) {
	st := edgeKronosEnv(t)
	if err := EnqueueOrch(st, "", "some-job", "a summary"); err == nil {
		t.Fatal("EnqueueOrch with an empty kind = nil, want the store's refusal")
	}
	if got := edgeKronosOrchSummaries(t, st); len(got) != 0 {
		t.Fatalf("a refused EnqueueOrch left %d row(s): %+v", len(got), got)
	}
}

// TestEdgeEnqueueOrchAcceptsAnEmptySlug: a row's slug may be empty — the
// wrapper is thin and stores what it is handed, and refusing a slugless row
// here would make any event that belongs to no single job unrepresentable.
func TestEdgeEnqueueOrchAcceptsAnEmptySlug(t *testing.T) {
	st := edgeKronosEnv(t)
	if err := EnqueueOrch(st, "note", "", "what is blocking the export"); err != nil {
		t.Fatalf("EnqueueOrch with an empty slug: %v", err)
	}
	if got := edgeKronosOrchSummaries(t, st); len(got) != 1 {
		t.Fatalf("summaries = %+v, want the one slugless row", got)
	}
}

// --- deliverOrch ------------------------------------------------------------

// TestEdgeDeliverOrchWithNoKronosIsASilentNoOp: "nil kronos → silent no-op".
// Rows must stay UNDELIVERED so the next tick retries — marking them delivered
// with nobody to deliver to would retire every escalation kronos was ever
// supposed to see, permanently and invisibly.
func TestEdgeDeliverOrchWithNoKronosIsASilentNoOp(t *testing.T) {
	st := edgeKronosEnv(t)
	edgeKronosEnable(t)
	if err := EnqueueOrch(st, "job-blocked", "some-job", "blocked on carl"); err != nil {
		t.Fatalf("EnqueueOrch: %v", err)
	}

	// Nothing that looks like kronos: an empty snapshot, then a snapshot full
	// of peers that are all disqualified for a different reason each.
	other := &core.Peer{TmuxSession: "kraken-other-1", Name: KronosAgentName, Agent: "claude", Pane: "%1", Depth: 0, Role: "agent"}
	nested := &core.Peer{TmuxSession: KronosSessionName, Name: "subagent", Agent: "claude", Pane: "%2", Depth: 1, Role: "agent"}
	codex := &core.Peer{TmuxSession: KronosSessionName, Name: "", Agent: "codex", Pane: "%3", Depth: 0, Role: "agent"}
	shell := &core.Peer{TmuxSession: KronosSessionName, Name: "", Agent: "", Pane: "%4", Depth: 0, Role: "shell"}

	for _, all := range [][]*core.Peer{nil, {}, {other}, {other, nested, codex, shell}} {
		deliverOrch(st, all)
		rows, err := st.UndeliveredOrch()
		if err != nil {
			t.Fatalf("UndeliveredOrch: %v", err)
		}
		if len(rows) != 1 {
			t.Fatalf("undelivered = %+v after deliverOrch with no kronos, want the row still pending", rows)
		}
		if rows[0].DeliveredAt != 0 {
			t.Fatalf("a row was stamped delivered with no kronos to deliver to: %+v", rows[0])
		}
	}
}

// TestEdgeDeliverOrchWithNothingPendingIsANoOp: the common tick. An empty
// worklist must not bump kronos with "orch: 0 new item(s)" — a bump is an
// interrupt into a live pane, and one per tick forever would make kronos
// unusable.
func TestEdgeDeliverOrchWithNothingPendingIsANoOp(t *testing.T) {
	st := edgeKronosEnv(t)
	edgeKronosEnable(t)
	deliverOrch(st, nil)
	rows, err := st.UnackedOrch()
	if err != nil {
		t.Fatalf("UnackedOrch: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("deliverOrch invented %d row(s): %+v", len(rows), rows)
	}
}

// TestEdgeDeliverOrchDisabledIsASilentNoOp: with the opt-in file absent the
// pass does nothing at all, so a machine that never opted in never has a row
// stamped even if one somehow exists.
func TestEdgeDeliverOrchDisabledIsASilentNoOp(t *testing.T) {
	st := edgeKronosEnv(t)
	edgeKronosKrakenDir(t)
	if err := EnqueueOrch(st, "job-blocked", "some-job", "blocked"); err != nil {
		t.Fatalf("EnqueueOrch: %v", err)
	}
	deliverOrch(st, nil)
	rows, err := st.UndeliveredOrch()
	if err != nil {
		t.Fatalf("UndeliveredOrch: %v", err)
	}
	if len(rows) != 1 || rows[0].DeliveredAt != 0 {
		t.Fatalf("undelivered = %+v with the feature off, want the row untouched", rows)
	}
}

// --- RecordKronosContext ----------------------------------------------------

// TestEdgeRecordKronosContextRefusesBlankSession: "Blank session or pct
// outside [0,100] is an error." The session id is what keys the fired-set, so
// a blank one would merge every session's threshold state into one bucket —
// and a compaction would then never re-arm anything.
func TestEdgeRecordKronosContextRefusesBlankSession(t *testing.T) {
	st := edgeKronosEnv(t)
	for _, sid := range []string{"", "   ", "\t\n"} {
		if err := RecordKronosContext(st, sid, 42, 1_000_000); err == nil {
			t.Errorf("RecordKronosContext(%q) = nil, want an error", sid)
		}
	}
	edgeKronosKVMissing(t, st, "kronos_ctx", "a refused record must write nothing")
}

// TestEdgeRecordKronosContextRefusesOutOfRangePct: the percentage drives the
// warning thresholds, so a value outside [0,100] is either a units mistake (a
// raw token count) or a corrupt payload — and a stored 4_000_000 would fire
// all three warnings at once and then never re-arm.
func TestEdgeRecordKronosContextRefusesOutOfRangePct(t *testing.T) {
	st := edgeKronosEnv(t)
	for _, pct := range []float64{-0.1, -1, 100.1, 101, 4_000_000, math.Inf(1), math.Inf(-1)} {
		if err := RecordKronosContext(st, "sess-1", pct, 1_000_000); err == nil {
			t.Errorf("RecordKronosContext(pct=%v) = nil, want an error", pct)
		}
	}
	edgeKronosKVMissing(t, st, "kronos_ctx", "every one of those was refused")
}

// TestEdgeRecordKronosContextAcceptsTheEndpoints: [0,100] is CLOSED, and both
// ends are real readings — 0 on a fresh session, 100 the instant before
// auto-compact takes over. Refusing either would drop exactly the report that
// matters most.
func TestEdgeRecordKronosContextAcceptsTheEndpoints(t *testing.T) {
	st := edgeKronosEnv(t)
	for _, pct := range []float64{0, 0.1, 50, 99.9, 100} {
		if err := RecordKronosContext(st, "sess-1", pct, 1_000_000); err != nil {
			t.Errorf("RecordKronosContext(pct=%v) = %v, want nil — the range is closed", pct, err)
		}
	}
	if _, err := st.GetKV("kronos_ctx"); err != nil {
		t.Fatalf("kronos_ctx was not written by an accepted record: %v", err)
	}
}

// TestEdgeRecordKronosContextRefusesNaN: NaN is not in [0,100] — every
// comparison against it is false, which is exactly why a naive
// `pct < 0 || pct > 100` lets it through. A stored NaN then compares false
// against all three thresholds forever, so the sentinel goes permanently
// silent on the session it was watching, with nothing logged.
func TestEdgeRecordKronosContextRefusesNaN(t *testing.T) {
	st := edgeKronosEnv(t)
	if err := RecordKronosContext(st, "sess-1", math.NaN(), 1_000_000); err == nil {
		t.Fatal("RecordKronosContext(pct=NaN) = nil, want an error — NaN is outside [0,100]")
	}
	edgeKronosKVMissing(t, st, "kronos_ctx", "a refused record must write nothing")
}

// --- ctxPass ----------------------------------------------------------------

// TestEdgeCtxPassWithNoReportIsANoOp: the pass runs every tick from the moment
// the daemon starts, long before kronos' statusline has ever posted anything.
// No report means nothing to compare against — and firing a warning off a
// zeroed record would tell carl his context was at 0%.
func TestEdgeCtxPassWithNoReportIsANoOp(t *testing.T) {
	st := edgeKronosEnv(t)
	edgeKronosEnable(t)
	seen := edgeKronosSwapNotify(t)

	edgeKronosCtxPass(t, st)

	if seen.count() != 0 {
		t.Fatalf("ctxPass sent %d warning(s) with no context ever reported", seen.count())
	}
	edgeKronosKVMissing(t, st, "kronos_ctx_warned", "nothing was ever reported")
}

// TestEdgeCtxPassBelowTheFirstThresholdIsSilent: 49 is under 50, and the
// thresholds are UPWARD crossings. A pass that fired on every tick below the
// first threshold would raise a notification every fifteen seconds.
func TestEdgeCtxPassBelowTheFirstThresholdIsSilent(t *testing.T) {
	st := edgeKronosEnv(t)
	edgeKronosEnable(t)
	seen := edgeKronosSwapNotify(t)

	for _, pct := range []float64{0, 12.5, 49, 49.9} {
		if err := RecordKronosContext(st, "sess-1", pct, 1_000_000); err != nil {
			t.Fatalf("RecordKronosContext(%v): %v", pct, err)
		}
		edgeKronosCtxPass(t, st)
	}
	if seen.count() != 0 {
		t.Fatalf("ctxPass sent %d warning(s) below 50%%: %v", seen.count(), seen.all())
	}
}

// TestEdgeCtxPassFiresOncePerUpwardCrossing walks the ladder one threshold at
// a time — each step crosses exactly one line, which is the only shape the
// contract states unambiguously — and asserts both halves: the crossing fires,
// and standing still does NOT re-fire. A re-firing threshold is a notification
// every tick until the context moves.
func TestEdgeCtxPassFiresOncePerUpwardCrossing(t *testing.T) {
	st := edgeKronosEnv(t)
	edgeKronosEnable(t)
	seen := edgeKronosSwapNotify(t)

	step := func(pct float64) int {
		t.Helper()
		if err := RecordKronosContext(st, "sess-1", pct, 1_000_000); err != nil {
			t.Fatalf("RecordKronosContext(%v): %v", pct, err)
		}
		before := seen.count()
		edgeKronosCtxPass(t, st)
		return seen.count() - before
	}

	if n := step(49); n != 0 {
		t.Fatalf("49%% sent %d warning(s), want 0", n)
	}
	if n := step(51); n != 1 {
		t.Fatalf("49->51 sent %d warning(s), want exactly 1 (the 50 threshold)", n)
	}
	if n := step(51); n != 0 {
		t.Fatalf("51->51 sent %d warning(s), want 0 — a threshold fires once per crossing", n)
	}
	if n := step(74); n != 0 {
		t.Fatalf("51->74 sent %d warning(s), want 0 — still below 75", n)
	}
	if n := step(76); n != 1 {
		t.Fatalf("74->76 sent %d warning(s), want exactly 1 (the 75 threshold)", n)
	}
	if n := step(88); n != 0 {
		t.Fatalf("76->88 sent %d warning(s), want 0 — still below 90", n)
	}
	if n := step(92); n != 1 {
		t.Fatalf("88->92 sent %d warning(s), want exactly 1 (the 90 threshold)", n)
	}
	if n := step(97); n != 0 {
		t.Fatalf("92->97 sent %d warning(s), want 0 — every threshold is already fired", n)
	}
}

// TestEdgeCtxPassFiresExactlyAtTheThreshold: "bands switch at ≥50/≥75/≥90" is
// how the meter reads it, and the pass has to agree — a `>` here means the
// warning at a context sitting exactly on 90.0 never arrives.
func TestEdgeCtxPassFiresExactlyAtTheThreshold(t *testing.T) {
	st := edgeKronosEnv(t)
	edgeKronosEnable(t)
	seen := edgeKronosSwapNotify(t)

	if err := RecordKronosContext(st, "sess-exact", 49.9, 1_000_000); err != nil {
		t.Fatalf("RecordKronosContext: %v", err)
	}
	edgeKronosCtxPass(t, st)
	if seen.count() != 0 {
		t.Fatalf("49.9%% fired %d warning(s)", seen.count())
	}
	if err := RecordKronosContext(st, "sess-exact", 50, 1_000_000); err != nil {
		t.Fatalf("RecordKronosContext: %v", err)
	}
	edgeKronosCtxPass(t, st)
	if seen.count() != 1 {
		t.Fatalf("exactly 50.0%% fired %d warning(s), want 1 — the threshold is >=", seen.count())
	}
}

// TestEdgeCtxPassRearmsAfterFallingBelow: "A pct back below a threshold
// re-arms it." A /compact drops the percentage, and the next climb is a fresh
// crossing carl needs to hear about — a fired-set that never cleared would
// mean he is warned exactly once in the whole life of an eternal session.
func TestEdgeCtxPassRearmsAfterFallingBelow(t *testing.T) {
	st := edgeKronosEnv(t)
	edgeKronosEnable(t)
	seen := edgeKronosSwapNotify(t)

	step := func(pct float64) int {
		t.Helper()
		if err := RecordKronosContext(st, "sess-1", pct, 1_000_000); err != nil {
			t.Fatalf("RecordKronosContext(%v): %v", pct, err)
		}
		before := seen.count()
		edgeKronosCtxPass(t, st)
		return seen.count() - before
	}

	step(49)
	if n := step(55); n != 1 {
		t.Fatalf("the first 50-crossing sent %d warning(s), want 1", n)
	}
	if n := step(30); n != 0 {
		t.Fatalf("falling to 30%% sent %d warning(s), want 0 — falling is not a crossing", n)
	}
	if n := step(55); n != 1 {
		t.Fatalf("climbing back to 55%% sent %d warning(s), want 1 — falling below re-arms", n)
	}
}

// TestEdgeCtxPassNewSessionResetsTheFiredSet: "a NEW session id resets the
// fired-set." A respawned kronos starts at a low percentage and climbs again;
// carrying the old session's fired thresholds forward would silence the whole
// ladder for the new conversation.
func TestEdgeCtxPassNewSessionResetsTheFiredSet(t *testing.T) {
	st := edgeKronosEnv(t)
	edgeKronosEnable(t)
	seen := edgeKronosSwapNotify(t)

	if err := RecordKronosContext(st, "sess-old", 49, 1_000_000); err != nil {
		t.Fatalf("RecordKronosContext: %v", err)
	}
	edgeKronosCtxPass(t, st)
	if err := RecordKronosContext(st, "sess-old", 55, 1_000_000); err != nil {
		t.Fatalf("RecordKronosContext: %v", err)
	}
	edgeKronosCtxPass(t, st)
	if seen.count() != 1 {
		t.Fatalf("the old session fired %d warning(s), want 1", seen.count())
	}

	// A brand-new session, arriving at the same percentage.
	if err := RecordKronosContext(st, "sess-new", 55, 1_000_000); err != nil {
		t.Fatalf("RecordKronosContext: %v", err)
	}
	edgeKronosCtxPass(t, st)
	if seen.count() != 2 {
		t.Fatalf("a new session at 55%% fired %d warning(s) in total, want 2 — the fired-set resets per session",
			seen.count())
	}
}

// TestEdgeCtxPassWarningNamesKronosThePercentAndAutoCompact: the notification
// arrives with no other context, so it has to say WHO is filling up, HOW full,
// and what happens if he does nothing (Q3: auto-compact stays on as the last
// resort). A bare "90%" from an unnamed sender is unactionable.
func TestEdgeCtxPassWarningNamesKronosThePercentAndAutoCompact(t *testing.T) {
	st := edgeKronosEnv(t)
	edgeKronosEnable(t)
	seen := edgeKronosSwapNotify(t)

	if err := RecordKronosContext(st, "sess-1", 10, 1_000_000); err != nil {
		t.Fatalf("RecordKronosContext: %v", err)
	}
	edgeKronosCtxPass(t, st)
	if err := RecordKronosContext(st, "sess-1", 92, 1_000_000); err != nil {
		t.Fatalf("RecordKronosContext: %v", err)
	}
	edgeKronosCtxPass(t, st)

	if seen.count() == 0 {
		t.Fatal("no warning was sent at 92%")
	}
	script := seen.last()
	lower := strings.ToLower(script)
	if !strings.Contains(lower, "kronos") {
		t.Errorf("the warning does not name kronos:\n%s", script)
	}
	if !strings.Contains(script, "92") {
		t.Errorf("the warning does not carry the percentage:\n%s", script)
	}
	if !strings.Contains(lower, "compact") {
		t.Errorf("the warning does not say auto-compact takes over:\n%s", script)
	}
}

// TestEdgeCtxPassDisabledIsSilent: the feature switch gates this pass too, so
// an opted-out machine never notifies carl about a kronos it does not run.
func TestEdgeCtxPassDisabledIsSilent(t *testing.T) {
	st := edgeKronosEnv(t)
	edgeKronosKrakenDir(t)
	seen := edgeKronosSwapNotify(t)

	if err := RecordKronosContext(st, "sess-1", 95, 1_000_000); err != nil {
		t.Fatalf("RecordKronosContext: %v", err)
	}
	edgeKronosCtxPass(t, st)

	if seen.count() != 0 {
		t.Fatalf("a disabled ctxPass sent %d warning(s): %v", seen.count(), seen.all())
	}
}

// --- RearmKronosRecall ------------------------------------------------------

// TestEdgeRearmKronosRecallRefusesBlankSession: "Refuses (error) unless
// sessionID is non-blank AND equals the kv-recorded kronos session id." A
// blank id reaching a delete is the wipe this guard exists to stop.
func TestEdgeRearmKronosRecallRefusesBlankSession(t *testing.T) {
	st := edgeKronosEnv(t)
	if err := st.MarkRecalled("some-cell-session", []int64{1, 2, 3}, 1_769_040_000_000); err != nil {
		t.Fatalf("MarkRecalled: %v", err)
	}

	for _, sid := range []string{"", "   ", "\t\n"} {
		n, err := RearmKronosRecall(st, sid)
		if err == nil {
			t.Errorf("RearmKronosRecall(%q) = %d, nil — want an error", sid, n)
		}
		if n != 0 {
			t.Errorf("RearmKronosRecall(%q) reported %d cleared despite refusing", sid, n)
		}
	}
	ids, err := st.RecalledEdgeIDs("some-cell-session")
	if err != nil {
		t.Fatalf("RecalledEdgeIDs: %v", err)
	}
	if len(ids) != 3 {
		t.Fatalf("a refused re-arm deleted marks: %d of 3 left", len(ids))
	}
}

// TestEdgeRearmKronosRecallRefusesWithNoKronosRecorded: "No kronos recorded →
// error, nothing deleted." The hook is installed in kronos' own directory, but
// nothing stops the same binary being invoked elsewhere — and with no recorded
// id there is nothing to compare against, so the only safe answer is a
// refusal.
func TestEdgeRearmKronosRecallRefusesWithNoKronosRecorded(t *testing.T) {
	st := edgeKronosEnv(t)
	if err := st.MarkRecalled("looks-like-a-session", []int64{9, 10}, 1_769_040_000_000); err != nil {
		t.Fatalf("MarkRecalled: %v", err)
	}
	edgeKronosKVMissing(t, st, "kronos_agent", "the fixture deliberately records no kronos")

	n, err := RearmKronosRecall(st, "looks-like-a-session")
	if err == nil {
		t.Fatalf("RearmKronosRecall with no kronos recorded = %d, nil — want an error", n)
	}
	if n != 0 {
		t.Fatalf("RearmKronosRecall reported %d cleared with no kronos recorded", n)
	}
	ids, rerr := st.RecalledEdgeIDs("looks-like-a-session")
	if rerr != nil {
		t.Fatalf("RecalledEdgeIDs: %v", rerr)
	}
	if len(ids) != 2 {
		t.Fatalf("a refused re-arm deleted marks: %d of 2 left", len(ids))
	}
}

// TestEdgeRearmKronosRecallRefusesAnUnparseableKronosRecord: a corrupt
// kronos_agent value is indistinguishable from "no kronos recorded" as far as
// authorization goes — there is no id to match, so the answer must still be a
// refusal rather than a clear. (EnsureKronos treats the same corruption as
// never-spawned and mints a fresh id; what must NOT happen is a delete on the
// strength of a value nothing could parse.)
func TestEdgeRearmKronosRecallRefusesAnUnparseableKronosRecord(t *testing.T) {
	st := edgeKronosEnv(t)
	if err := st.SetKV("kronos_agent", "}}} not json at all"); err != nil {
		t.Fatalf("SetKV: %v", err)
	}
	if err := st.MarkRecalled("any-session", []int64{4}, 1_769_040_000_000); err != nil {
		t.Fatalf("MarkRecalled: %v", err)
	}

	n, err := RearmKronosRecall(st, "any-session")
	if err == nil {
		t.Fatalf("RearmKronosRecall against an unparseable kronos_agent = %d, nil — want an error", n)
	}
	if n != 0 {
		t.Fatalf("RearmKronosRecall cleared %d rows off an unparseable record", n)
	}
	ids, rerr := st.RecalledEdgeIDs("any-session")
	if rerr != nil {
		t.Fatalf("RecalledEdgeIDs: %v", rerr)
	}
	if len(ids) != 1 {
		t.Fatalf("a refused re-arm deleted marks: %d of 1 left", len(ids))
	}
}

// --- RearmKronosRecall: the match, now that the kv shape is frozen ---------
//
// Round-1 amendment: `kronos_agent` holds JSON
// `{"session_id": "<uuid>", "spawned_at": <unix millis>}` — iris' shape
// exactly. That is what makes the SUCCESS half of this route reachable blind,
// and the mismatch half testable against a RECORDED id rather than only
// against an empty kv.

// edgeKronosRecordAgent writes the frozen kv shape verbatim. Built as a
// literal rather than through a struct so the test is pinned to the JSON the
// contract names, not to whatever type the implementer declared.
func edgeKronosRecordAgent(t *testing.T, st *store.Store, sessionID string, spawnedAt int64) {
	t.Helper()
	value := `{"session_id":"` + sessionID + `","spawned_at":` + strconv.FormatInt(spawnedAt, 10) + `}`
	if err := st.SetKV("kronos_agent", value); err != nil {
		t.Fatalf("SetKV(kronos_agent): %v", err)
	}
}

const (
	edgeKronosSessionID = "11111111-2222-4333-8444-555555555555"
	edgeKronosOtherUUID = "99999999-8888-4777-8666-555555555555"
)

// TestEdgeRearmKronosRecallClearsOnlyKronosOnAMatch is the whole point of the
// PostCompact hook: compaction dropped the delivered facts out of kronos'
// context, so re-delivery is correct FOR KRONOS — and only for kronos. A cell
// mid-run has its facts still in context, and clearing its marks would
// re-inject every one of them on its next tool call, burning the once-per-fact
// budget the whole recall design is built on.
func TestEdgeRearmKronosRecallClearsOnlyKronosOnAMatch(t *testing.T) {
	st := edgeKronosEnv(t)
	edgeKronosRecordAgent(t, st, edgeKronosSessionID, 1_769_040_000_000)

	if err := st.MarkRecalled(edgeKronosSessionID, []int64{1, 2, 3, 4}, 1_769_040_000_000); err != nil {
		t.Fatalf("MarkRecalled kronos: %v", err)
	}
	if err := st.MarkRecalled("a-cell-session", []int64{1, 2}, 1_769_040_000_000); err != nil {
		t.Fatalf("MarkRecalled cell: %v", err)
	}

	n, err := RearmKronosRecall(st, edgeKronosSessionID)
	if err != nil {
		t.Fatalf("RearmKronosRecall on a match = %v, want nil", err)
	}
	if n != 4 {
		t.Fatalf("RearmKronosRecall cleared %d, want 4 — the count is what the hook's route reports back", n)
	}
	gone, err := st.RecalledEdgeIDs(edgeKronosSessionID)
	if err != nil {
		t.Fatalf("RecalledEdgeIDs(kronos): %v", err)
	}
	if len(gone) != 0 {
		t.Fatalf("kronos' marks after a matching re-arm = %+v, want none", gone)
	}
	kept, err := st.RecalledEdgeIDs("a-cell-session")
	if err != nil {
		t.Fatalf("RecalledEdgeIDs(cell): %v", err)
	}
	if len(kept) != 2 {
		t.Fatalf("the cell session's marks = %d of 2 — a re-arm is session-scoped", len(kept))
	}
}

// TestEdgeRearmKronosRecallOnAMatchWithNothingToClear: kronos can compact
// before recall has ever injected anything into it, and the hook fires anyway.
// Zero is a real answer — an error here would be invisible (the hook exits 0
// and prints nothing on any failure) AND would leave the marks in place, which
// is the one outcome the re-arm exists to prevent.
func TestEdgeRearmKronosRecallOnAMatchWithNothingToClear(t *testing.T) {
	st := edgeKronosEnv(t)
	edgeKronosRecordAgent(t, st, edgeKronosSessionID, 1_769_040_000_000)

	n, err := RearmKronosRecall(st, edgeKronosSessionID)
	if err != nil {
		t.Fatalf("RearmKronosRecall with no marks = %v, want nil", err)
	}
	if n != 0 {
		t.Fatalf("RearmKronosRecall cleared %d with no marks recorded", n)
	}
}

// TestEdgeRearmKronosRecallIsRepeatableOnAMatch: a compaction can fire the
// hook more than once (and the daemon can retry a failed post), so the second
// call must be a plain zero rather than an error — nothing left to delete IS
// success, and an error would make the hook's own retry look like a broken
// re-arm.
func TestEdgeRearmKronosRecallIsRepeatableOnAMatch(t *testing.T) {
	st := edgeKronosEnv(t)
	edgeKronosRecordAgent(t, st, edgeKronosSessionID, 1_769_040_000_000)
	if err := st.MarkRecalled(edgeKronosSessionID, []int64{7, 8}, 1_769_040_000_000); err != nil {
		t.Fatalf("MarkRecalled: %v", err)
	}

	if n, err := RearmKronosRecall(st, edgeKronosSessionID); err != nil || n != 2 {
		t.Fatalf("first re-arm = %d/%v, want 2/nil", n, err)
	}
	n, err := RearmKronosRecall(st, edgeKronosSessionID)
	if err != nil {
		t.Fatalf("second re-arm = %v, want nil", err)
	}
	if n != 0 {
		t.Fatalf("second re-arm cleared %d, want 0", n)
	}
}

// TestEdgeRearmKronosRecallRefusesARecordedMismatch is the authorization check
// asserted against the state it actually guards: a kronos IS recorded, and the
// caller names a DIFFERENT well-formed session id. This is the shape a stray
// `kraken rearm-hook` from any other claude on the machine takes — the hook
// posts whatever session id its payload carries — and honouring it would
// delete that session's marks, or kronos', depending on which side the
// implementation compared.
func TestEdgeRearmKronosRecallRefusesARecordedMismatch(t *testing.T) {
	st := edgeKronosEnv(t)
	edgeKronosRecordAgent(t, st, edgeKronosSessionID, 1_769_040_000_000)

	if err := st.MarkRecalled(edgeKronosSessionID, []int64{1, 2, 3}, 1_769_040_000_000); err != nil {
		t.Fatalf("MarkRecalled kronos: %v", err)
	}
	if err := st.MarkRecalled(edgeKronosOtherUUID, []int64{4, 5}, 1_769_040_000_000); err != nil {
		t.Fatalf("MarkRecalled other: %v", err)
	}

	for _, sid := range []string{
		edgeKronosOtherUUID,             // a different, well-formed uuid
		edgeKronosSessionID + "-suffix", // a prefix of kronos' id plus more
		edgeKronosSessionID[:8],         // a prefix of kronos' id
	} {
		n, err := RearmKronosRecall(st, sid)
		if err == nil {
			t.Errorf("RearmKronosRecall(%q) = %d, nil — want a refusal, kronos is %q",
				sid, n, edgeKronosSessionID)
		}
		if n != 0 {
			t.Errorf("RearmKronosRecall(%q) reported %d cleared despite refusing", sid, n)
		}
	}

	// Neither session lost a mark: the match must be on the WHOLE id, and a
	// refusal deletes nothing on either side of the comparison.
	kronosIDs, err := st.RecalledEdgeIDs(edgeKronosSessionID)
	if err != nil {
		t.Fatalf("RecalledEdgeIDs(kronos): %v", err)
	}
	if len(kronosIDs) != 3 {
		t.Fatalf("kronos' marks = %d of 3 after refused re-arms", len(kronosIDs))
	}
	otherIDs, err := st.RecalledEdgeIDs(edgeKronosOtherUUID)
	if err != nil {
		t.Fatalf("RecalledEdgeIDs(other): %v", err)
	}
	if len(otherIDs) != 2 {
		t.Fatalf("the other session's marks = %d of 2 after refused re-arms", len(otherIDs))
	}
}

// TestEdgeRearmKronosRecallReadsTheFrozenKVShape: the value is iris' shape
// verbatim, and the id the route compares against is `session_id` — not
// `spawned_at`, not the raw kv string. A reader that string-compared the whole
// kv value would refuse every legitimate re-arm forever, silently, because the
// hook can only ever send a bare session id.
func TestEdgeRearmKronosRecallReadsTheFrozenKVShape(t *testing.T) {
	st := edgeKronosEnv(t)
	edgeKronosRecordAgent(t, st, edgeKronosSessionID, 1_769_040_000_000)
	if err := st.MarkRecalled(edgeKronosSessionID, []int64{1}, 1_769_040_000_000); err != nil {
		t.Fatalf("MarkRecalled: %v", err)
	}

	// The stored value really is the frozen JSON, so this test measures the
	// route's read of it rather than some other encoding the fixture invented.
	raw, err := st.GetKV("kronos_agent")
	if err != nil {
		t.Fatalf("GetKV: %v", err)
	}
	var probe struct {
		SessionID string `json:"session_id"`
		SpawnedAt int64  `json:"spawned_at"`
	}
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		t.Fatalf("the fixture's kv value is not the frozen shape: %v (%q)", err, raw)
	}
	if probe.SessionID != edgeKronosSessionID || probe.SpawnedAt != 1_769_040_000_000 {
		t.Fatalf("fixture kv = %+v, want the frozen shape's two fields", probe)
	}

	if n, err := RearmKronosRecall(st, edgeKronosSessionID); err != nil || n != 1 {
		t.Fatalf("RearmKronosRecall against the frozen kv shape = %d/%v, want 1/nil", n, err)
	}

	// And the whole raw kv string is NOT a valid session id to re-arm with.
	if n, err := RearmKronosRecall(st, raw); err == nil {
		t.Fatalf("RearmKronosRecall(<the whole kv json>) = %d, nil — only session_id may match", n)
	}
}

// --- round 2 --------------------------------------------------------------
//
// Everything below this line was authored SIGHTED, against the round-2
// amendments (the REPLACE-merged PostCompact entry; the cold-start seeding and
// the imsg summary cap went with the iMessage bridge). The blindness above it
// is unaffected: none of these obligations existed when the file was written,
// and each assertion still derives from the amendment TEXT.

// TestEdgeInstallKronosSettingsReplacesThePostCompactEntry — authored SIGHTED.
// "PostCompact hook entry is REPLACE-merged like the recall entries (its
// command embeds the binary path and serve-addr flags; append-if-absent
// accumulates stale entries)."
//
// The two installs below differ in binary path exactly as a launchd daemon and
// a scratch build on another port do. Append-if-absent leaves the OLD entry in
// place and adds a second beside it — and the stale one POSTs at an address
// nothing is listening on, gets a connection error, and `rearm-hook` drops it
// and exits 0 by contract. So the file grows one inert entry per distinct
// address forever, every one of them silent, and the only symptom is a
// PostCompact that runs a binary that no longer exists.
func TestEdgeInstallKronosSettingsReplacesThePostCompactEntry(t *testing.T) {
	edgeKronosEnv(t)
	dir := edgeKronosStageDir(t)
	const binA = "/edge/kronos/old/kraken"
	const binB = "/edge/kronos/new/kraken"

	if err := installKronosSettings(dir, binA); err != nil {
		t.Fatalf("installKronosSettings(A): %v", err)
	}
	if err := installKronosSettings(dir, binB); err != nil {
		t.Fatalf("installKronosSettings(B): %v", err)
	}

	groups := edgeKronosHookGroups(t, dir, "PostCompact")
	var rearm []string
	for _, cmd := range groups {
		if strings.Contains(cmd, "rearm-hook") {
			rearm = append(rearm, cmd)
		}
	}
	if len(rearm) != 1 {
		t.Fatalf("PostCompact carries %d rearm-hook entries after two installs, want exactly 1: %v", len(rearm), groups)
	}
	if !strings.HasPrefix(rearm[0], binB+" rearm-hook") {
		t.Fatalf("the surviving PostCompact entry is %q, want it to begin with %q — the LATER install wins",
			rearm[0], binB+" rearm-hook")
	}
	if strings.Contains(rearm[0], binA) {
		t.Fatalf("the stale binary path survived in %q", rearm[0])
	}
}

// TestEdgeInstallKronosSettingsReplacesANonArrayPostCompact — authored
// SIGHTED. The replacement is by TYPE ASSERTION rather than by presence, which
// is what makes a hand-edited `"PostCompact": {...}` (or a string, or a null) a
// shape that is replaced outright rather than one that silently defeats the
// install. The alternative — returning early and installing nothing while
// reporting success — leaves the re-arm hook permanently missing on a machine
// whose settings file looks fine, and a missing re-arm is invisible: recall
// simply stops re-delivering after the first compaction.
func TestEdgeInstallKronosSettingsReplacesANonArrayPostCompact(t *testing.T) {
	for _, weird := range []string{
		`{"hooks": {"PostCompact": {"note": "hand edited into an object"}}}`,
		`{"hooks": {"PostCompact": "a bare string"}}`,
		`{"hooks": {"PostCompact": null}}`,
	} {
		t.Run(weird, func(t *testing.T) {
			edgeKronosEnv(t)
			dir := edgeKronosStageDir(t)
			claudeDir := filepath.Join(dir, ".claude")
			if err := os.MkdirAll(claudeDir, 0o755); err != nil {
				t.Fatalf("mkdir .claude: %v", err)
			}
			if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(weird), 0o644); err != nil {
				t.Fatalf("seed settings.json: %v", err)
			}

			const bin = "/edge/kronos/bin/kraken"
			if err := installKronosSettings(dir, bin); err != nil {
				t.Fatalf("installKronosSettings: %v", err)
			}
			groups := edgeKronosHookGroups(t, dir, "PostCompact")
			var rearm []string
			for _, cmd := range groups {
				if strings.Contains(cmd, "rearm-hook") {
					rearm = append(rearm, cmd)
				}
			}
			if len(rearm) != 1 {
				t.Fatalf("PostCompact carries %d rearm-hook entries, want 1: %v", len(rearm), groups)
			}
			if !strings.HasPrefix(rearm[0], bin+" rearm-hook") {
				t.Fatalf("PostCompact entry = %q, want it to begin with %q", rearm[0], bin+" rearm-hook")
			}
		})
	}
}

// edgeKronosHookGroups reads every hook COMMAND registered under one event of
// the settings file. Decoded through `any` rather than a typed struct because
// what is being measured is the shape as written, including shapes a typed
// decode would quietly normalize away.
func edgeKronosHookGroups(t *testing.T, dir, event string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	var parsed struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("settings.json is not the expected hook shape: %v\n%s", err, raw)
	}
	var out []string
	for _, group := range parsed.Hooks[event] {
		for _, h := range group.Hooks {
			out = append(out, h.Command)
		}
	}
	return out
}

// --- round 3 --------------------------------------------------------------
//
// Authored SIGHTED against the round-3 amendments (REPLACE-merged bash-lint,
// the statusLine skip arms' logging; the chat.db seeding amendments went with
// the iMessage bridge).

// edgeKronosEventCount counts store events carrying one event NAME.
//
// The name rather than the message on purpose: the contract's blind-test note
// forbids asserting human-readable strings it does not quote, and an event name
// is a stable identifier the code chooses deliberately — the same thing a log
// line's key is. UseStore (via useTestStore) wires util.LogEvent into the
// store's events table synchronously, so this is a plain read.
func edgeKronosEventCount(t *testing.T, st *store.Store, name string) int {
	t.Helper()
	rows, err := st.ListEvents(500, 0)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	n := 0
	for _, r := range rows {
		if r.Event == name {
			n++
		}
	}
	return n
}

// TestEdgeInstallKronosSettingsReplacesTheBashLintEntry — authored SIGHTED.
// "kronos' bash-lint entry is REPLACE-merged like rearm/recall (its command
// embeds the resolved binary path, which is exactly the varying component)."
//
// The round-2 reasoning for leaving this one append-if-absent was that the
// command carries no FLAGS — true, and beside the point: it carries
// os.Executable()'s answer, which differs between the launchd daemon and a
// scratch build, and that is the only varying component of the other two
// commands as well on a default machine. Appended, the stale entry names a
// binary that is then deleted, and a PreToolUse hook that cannot run fires on
// EVERY Bash call kronos makes — on the one agent that runs unattended.
func TestEdgeInstallKronosSettingsReplacesTheBashLintEntry(t *testing.T) {
	edgeKronosEnv(t)
	dir := edgeKronosStageDir(t)
	const binA = "/edge/kronos/old/kraken"
	const binB = "/edge/kronos/new/kraken"

	if err := installKronosSettings(dir, binA); err != nil {
		t.Fatalf("installKronosSettings(A): %v", err)
	}
	if err := installKronosSettings(dir, binB); err != nil {
		t.Fatalf("installKronosSettings(B): %v", err)
	}

	groups := edgeKronosHookGroups(t, dir, "PreToolUse")
	var lint []string
	for _, cmd := range groups {
		if strings.Contains(cmd, "bash-lint") {
			lint = append(lint, cmd)
		}
	}
	if len(lint) != 1 {
		t.Fatalf("PreToolUse carries %d bash-lint entries after two installs, want exactly 1: %v", len(lint), groups)
	}
	if !strings.HasPrefix(lint[0], binB+" bash-lint") {
		t.Fatalf("the surviving PreToolUse entry is %q, want it to begin with %q — the LATER install wins",
			lint[0], binB+" bash-lint")
	}
	if strings.Contains(lint[0], binA) {
		t.Fatalf("the stale binary path survived in %q", lint[0])
	}
}

// TestEdgeInstallKronosSettingsReplacesANonArrayPreToolUse — authored SIGHTED.
// The same type-assertion boundary the PostCompact fix closed, now on
// PreToolUse: a hand-edited shape that is not an array of groups must be
// replaced outright rather than silently defeating the install. Silently
// skipped, kronos runs with NO bash-lint gate at all — the zsh shapes that
// abort or lie silently are exactly as dangerous in its pane as in a cell's,
// and its settings file would look fine.
func TestEdgeInstallKronosSettingsReplacesANonArrayPreToolUse(t *testing.T) {
	for _, weird := range []string{
		`{"hooks": {"PreToolUse": {"note": "hand edited into an object"}}}`,
		`{"hooks": {"PreToolUse": "a bare string"}}`,
		`{"hooks": {"PreToolUse": null}}`,
	} {
		t.Run(weird, func(t *testing.T) {
			edgeKronosEnv(t)
			dir := edgeKronosStageDir(t)
			claudeDir := filepath.Join(dir, ".claude")
			if err := os.MkdirAll(claudeDir, 0o755); err != nil {
				t.Fatalf("mkdir .claude: %v", err)
			}
			if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(weird), 0o644); err != nil {
				t.Fatalf("seed settings.json: %v", err)
			}

			const bin = "/edge/kronos/bin/kraken"
			if err := installKronosSettings(dir, bin); err != nil {
				t.Fatalf("installKronosSettings: %v", err)
			}
			groups := edgeKronosHookGroups(t, dir, "PreToolUse")
			var lint []string
			for _, cmd := range groups {
				if strings.Contains(cmd, "bash-lint") {
					lint = append(lint, cmd)
				}
			}
			if len(lint) != 1 {
				t.Fatalf("PreToolUse carries %d bash-lint entries, want 1: %v", len(lint), groups)
			}
			if !strings.HasPrefix(lint[0], bin+" bash-lint") {
				t.Fatalf("PreToolUse entry = %q, want it to begin with %q", lint[0], bin+" bash-lint")
			}
		})
	}
}

// TestEdgeInstallKronosSettingsLogsWhenItLeavesTheStatusLineAlone — authored
// SIGHTED. "mergeKronosStatusLine's two skip arms log (event-log, once per
// install call): a foreign statusline left alone, and a non-object shape — a
// silently-off context sentinel must be diagnosable."
//
// Both skips are the right BEHAVIOUR (a statusline is one slot, and clobbering
// carl's own would be worse than not metering) and both were invisible: with
// ctx-hook never installed nothing POSTs a context report, so ctxPass returns
// at its first arm on every tick and GET /api/kronos omits `context` — which
// renders identically to a kronos that simply has not reported yet. The whole
// 50/75/90 ladder is off and the only symptom is a warning that was never
// going to arrive.
//
// Asserted on the event NAME rather than its text, and with a control: an
// install that DOES take the slot must stay quiet, or the signal means nothing.
func TestEdgeInstallKronosSettingsLogsWhenItLeavesTheStatusLineAlone(t *testing.T) {
	const bin = "/edge/kronos/bin/kraken"

	t.Run("installs on a fresh dir and says nothing", func(t *testing.T) {
		st := edgeKronosEnv(t)
		dir := edgeKronosStageDir(t)
		if err := installKronosSettings(dir, bin); err != nil {
			t.Fatalf("installKronosSettings: %v", err)
		}
		raw, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
		if err != nil {
			t.Fatalf("read settings.json: %v", err)
		}
		if !strings.Contains(string(raw), "ctx-hook") {
			t.Fatalf("the statusLine was not installed on a fresh dir:\n%s", raw)
		}
		if n := edgeKronosEventCount(t, st, "kronos-settings"); n != 0 {
			t.Fatalf("an install that TOOK the statusline logged %d skip event(s) — the signal must mean something", n)
		}
	})

	for _, c := range []struct{ name, body string }{
		{"a foreign statusline", `{"statusLine": {"type": "command", "command": "/usr/local/bin/my-own-statusline"}}`},
		{"a non-object statusline", `{"statusLine": "my-own-statusline"}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := edgeKronosEnv(t)
			dir := edgeKronosStageDir(t)
			claudeDir := filepath.Join(dir, ".claude")
			if err := os.MkdirAll(claudeDir, 0o755); err != nil {
				t.Fatalf("mkdir .claude: %v", err)
			}
			if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(c.body), 0o644); err != nil {
				t.Fatalf("seed settings.json: %v", err)
			}

			if err := installKronosSettings(dir, bin); err != nil {
				t.Fatalf("installKronosSettings: %v", err)
			}
			raw, err := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
			if err != nil {
				t.Fatalf("read settings.json: %v", err)
			}
			if strings.Contains(string(raw), "ctx-hook") {
				t.Fatalf("the merge took a statusline that was not kraken's:\n%s", raw)
			}
			if n := edgeKronosEventCount(t, st, "kronos-settings"); n < 1 {
				t.Fatal("the statusline was left alone with no event — a silently-off context sentinel is undiagnosable")
			}
			// The rest of the install still happened: skipping the statusline
			// must not skip the hooks.
			if !strings.Contains(string(raw), "rearm-hook") || !strings.Contains(string(raw), "recall-hook") {
				t.Fatalf("a skipped statusline dropped the hook entries too:\n%s", raw)
			}
		})
	}
}
