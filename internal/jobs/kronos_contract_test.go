package jobs

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"kraken/internal/core"
	"kraken/internal/store"
	"kraken/internal/util"
)

// kronos_contract_test.go — athena-logic's BLIND contract tests for the kronos
// build's Slice C (50-contract.md § "Slice C — jobs"): kronos' identity and
// spawn shape, its brief, the settings install, the orch enqueue, the context
// sentinel and the recall re-arm.
//
// Written from the contract text alone. internal/jobs/kronos.go,
// internal/jobs/orch.go, internal/jobs/kronosctx.go and
// internal/jobs/daemon.go were never opened.
//
// Dimension: LOGIC — the stated obligations, the happy paths, the invariants.
// Hostile inputs are athena-edge's ground.
//
// The iMessage bridge this file also covered was removed whole at the manual
// gate (carl's call), and every test of it went with it — the inbound pass, the
// send, the echo guard and the ROWID watermark. What survives of that half is
// the context warning, whose delivery is now the macOS notification alone.
//
// Nothing here reaches tmux, osascript or the network. The isolation points
// are the contract's own: KronosEnabled's file gate, the notifyFn seam, and the
// deliberate absence of a kronos peer in every discovery slice passed to
// deliverOrch. EnsureKronos is exercised ONLY on its two arms that return
// before tmux is touched.
//
// Every package-level identifier is prefixed `contractKronos`: this package
// already carries bare `contract*` names (loadscale) plus `contractSlack*` and
// `contractIris*`, and a sibling author shares the test binary.

// ---------------------------------------------------------------------------
// harness
// ---------------------------------------------------------------------------

// contractKronosEnable creates ~/.kraken/kronos.env, the whole feature's
// opt-in. Empty file = on, per the contract. Callers run useTestStore first,
// which is what points util.Home() at a throwaway dir.
func contractKronosEnable(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(util.Home(), ".kraken"), 0o755); err != nil {
		t.Fatalf("mkdir ~/.kraken: %v", err)
	}
	if err := os.WriteFile(filepath.Join(util.Home(), ".kraken", "kronos.env"), nil, 0o600); err != nil {
		t.Fatalf("write kronos.env: %v", err)
	}
	if !KronosEnabled() {
		t.Fatal("KronosEnabled() is false with ~/.kraken/kronos.env present — the fixture proves nothing")
	}
}

// contractKronosDir creates kronos' own directory, the state every
// spawn-command builder stages into.
func contractKronosDir(t *testing.T) string {
	t.Helper()
	if err := os.MkdirAll(KronosDir(), 0o755); err != nil {
		t.Fatalf("mkdir kronos dir: %v", err)
	}
	return KronosDir()
}

// contractKronosRecorder records the warnings ctxPass delivers. Round 2 moved
// the send into a goroutine, so the seam is called from a DIFFERENT goroutine
// than the test's, and an unsynchronised slice append there is a data race
// whichever way the assertion then reads.
//
// Authored SIGHTED post-review.
type contractKronosRecorder struct {
	mu   sync.Mutex
	seen []string
}

func (r *contractKronosRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.seen)
}

func (r *contractKronosRecorder) at(i int) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if i < 0 || i >= len(r.seen) {
		return ""
	}
	return r.seen[i]
}

// contractKronosSwapNotify installs the recording seam behind a mutex, through
// the notifyFn package var — the warning's whole delivery now that the iMessage
// channel is gone. The recorded string is title + body, so an assertion can
// read either half.
//
// Authored SIGHTED post-review.
func contractKronosSwapNotify(t *testing.T) *contractKronosRecorder {
	t.Helper()
	rec := &contractKronosRecorder{}
	prev := notifyFn
	notifyFn = func(title, msg string) {
		rec.mu.Lock()
		rec.seen = append(rec.seen, title+" "+msg)
		rec.mu.Unlock()
	}
	t.Cleanup(func() { notifyFn = prev })
	return rec
}

// contractKronosSettleSends waits until no ctxPass warning send is in flight,
// so a count read after it is FINAL rather than a snapshot of a race.
//
// The single-flight flag is claimed synchronously inside ctxPass, before the
// goroutine starts, and released only after the send, the notify fallback and
// the fired-set write have all run — so "not in flight" is exactly the point at
// which every observable this pass produces has settled. That is what makes it
// usable for the NEGATIVE assertions too: proving no warning was sent needs a
// bounded wait for the absence, and an immediate read would pass on a pass that
// was merely slow.
//
// Authored SIGHTED post-review.
func contractKronosSettleSends(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !kronosCtxSending.Load() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("a context-warning send was still in flight 2s after the pass returned")
}

// contractKronosSummaries renders an inbox row set as its summaries.
func contractKronosSummaries(items []store.OrchItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Summary
	}
	return out
}

// ---------------------------------------------------------------------------
// identity + the feature gate
// ---------------------------------------------------------------------------

// TestContractKronosIdentityConstants: the tmux session name and the --name
// are both "kronos" (the liveness gate keys on the session, and findKronos on
// the name, so a divergence between them breaks both), and KronosDir is
// ~/.kraken/kronos via util.Home() rather than a hardcoded /Users/<name>.
func TestContractKronosIdentityConstants(t *testing.T) {
	if KronosSessionName != "kronos" {
		t.Errorf("KronosSessionName = %q, want %q", KronosSessionName, "kronos")
	}
	if KronosAgentName != "kronos" {
		t.Errorf("KronosAgentName = %q, want %q", KronosAgentName, "kronos")
	}
	t.Setenv("HOME", t.TempDir())
	want := filepath.Join(util.Home(), ".kraken", "kronos")
	if got := KronosDir(); got != want {
		t.Fatalf("KronosDir() = %q, want %q", got, want)
	}
}

// TestContractKronosEnabledIsTheFilesPresence is the whole feature's opt-in.
// An EMPTY file is on: carl turns kronos on by touching a file, so a parser
// that required content in it would make the documented gesture do nothing.
func TestContractKronosEnabledIsTheFilesPresence(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if KronosEnabled() {
		t.Fatal("KronosEnabled() is true with no ~/.kraken/kronos.env — the feature must be off by default")
	}
	if err := os.MkdirAll(filepath.Join(util.Home(), ".kraken"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(util.Home(), ".kraken", "kronos.env"), nil, 0o600); err != nil {
		t.Fatalf("write kronos.env: %v", err)
	}
	if !KronosEnabled() {
		t.Fatal("KronosEnabled() is false with an EMPTY kronos.env present — an empty file is on")
	}
}

// TestContractKronosPassesAreNoOpsWhenDisabled pins the gate's stated reach on
// the one pass a test can drive without tmux: with the file absent, an
// undelivered row is neither delivered nor touched.
func TestContractKronosPassesAreNoOpsWhenDisabled(t *testing.T) {
	st := useTestStore(t)
	// deliberately NOT contractKronosEnable(t)

	id, err := st.AddOrchItem("job-blocked", "csv-export", "blocked on a spec question", time.Now().UnixMilli())
	if err != nil {
		t.Fatalf("AddOrchItem: %v", err)
	}

	deliverOrch(st, nil)

	items, err := st.UnackedOrch()
	if err != nil {
		t.Fatalf("UnackedOrch: %v", err)
	}
	if len(items) != 1 || items[0].ID != id {
		t.Fatalf("inbox = %+v, want only the pre-seeded row — a disabled kronos enqueues nothing", items)
	}
	if items[0].DeliveredAt != 0 {
		t.Errorf("the row was marked delivered (%d) with kronos disabled", items[0].DeliveredAt)
	}
}

// ---------------------------------------------------------------------------
// EnsureKronos — only the arms that return before tmux is touched
// ---------------------------------------------------------------------------

// TestContractEnsureKronosNoOpWhenDisabled: with the feature off, nothing is
// spawned and no session id is minted. A minted id with no process behind it
// would be resumed forever by every later tick.
func TestContractEnsureKronosNoOpWhenDisabled(t *testing.T) {
	st := useTestStore(t)
	if err := EnsureKronos(st, map[string]bool{}); err != nil {
		t.Fatalf("EnsureKronos with the feature off = %v, want nil (off is not an error)", err)
	}
	if _, err := st.GetKV("kronos_agent"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("kronos_agent kv err = %v, want ErrNotFound — a disabled machine must mint nothing", err)
	}
}

// TestContractEnsureKronosNilStoreIsNil: there is nowhere to record the
// session id without a store, so the whole lifecycle is skipped rather than
// half-run.
func TestContractEnsureKronosNilStoreIsNil(t *testing.T) {
	useTestStore(t)
	contractKronosEnable(t)
	if err := EnsureKronos(nil, map[string]bool{}); err != nil {
		t.Fatalf("EnsureKronos(nil, …) = %v, want nil", err)
	}
}

// TestContractEnsureKronosNoOpWhenSessionLive: the gate is the tmux SESSION
// name, never the discovered peer — claude writes its session file seconds
// into startup, so a peer-keyed gate would read a booting kronos as absent and
// start a second `claude -r` on one session id, which is two writers on one
// transcript.
func TestContractEnsureKronosNoOpWhenSessionLive(t *testing.T) {
	st := useTestStore(t)
	contractKronosEnable(t)

	if err := EnsureKronos(st, map[string]bool{KronosSessionName: true}); err != nil {
		t.Fatalf("EnsureKronos with the kronos session live = %v, want nil", err)
	}
	if _, err := st.GetKV("kronos_agent"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("kronos_agent kv err = %v, want ErrNotFound — a live session is never re-minted", err)
	}
}

// ---------------------------------------------------------------------------
// kronosSpawnCommand
// ---------------------------------------------------------------------------

// TestContractKronosSpawnCommandFirstSpawn: kraken mints the session id and
// NAMES it, so the id is known before the process exists rather than read back
// off discovery, and the model rides the command line where discovery reads it
// back from.
func TestContractKronosSpawnCommandFirstSpawn(t *testing.T) {
	useTestStore(t)
	dir := contractKronosDir(t)
	const sid = "11111111-2222-4333-8444-555555555555"

	cmd, err := kronosSpawnCommand(sid, false)
	if err != nil {
		t.Fatalf("kronosSpawnCommand: %v", err)
	}
	for _, want := range []string{
		"--dangerously-skip-permissions",
		"--model " + core.KronosModel,
		"--name " + KronosAgentName,
		"--session-id " + sid,
		"--system-prompt-file",
		filepath.Join(dir, "kronos-brief.md"),
		"$(cat ",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("first-spawn command %q missing %q", cmd, want)
		}
	}
	if strings.Contains(cmd, "-r "+sid) {
		t.Errorf("first-spawn command resumes instead of naming a new session: %q", cmd)
	}
}

// TestContractKronosSpawnCommandResume: a respawn continues the SAME
// conversation. Kronos' memory is the rows and the graph, but the conversation
// is still where an in-flight instruction from carl lives.
func TestContractKronosSpawnCommandResume(t *testing.T) {
	useTestStore(t)
	dir := contractKronosDir(t)
	const sid = "11111111-2222-4333-8444-555555555555"

	cmd, err := kronosSpawnCommand(sid, true)
	if err != nil {
		t.Fatalf("kronosSpawnCommand: %v", err)
	}
	for _, want := range []string{
		"--dangerously-skip-permissions",
		"--model " + core.KronosModel,
		"--name " + KronosAgentName,
		"-r " + sid,
		"--system-prompt-file",
		filepath.Join(dir, "kronos-brief.md"),
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("resume command %q missing %q", cmd, want)
		}
	}
	if strings.Contains(cmd, "--session-id") {
		t.Errorf("resume command mints a new session id: %q", cmd)
	}
}

// TestContractKronosSpawnCommandReplacesRatherThanAppends is the one flag that
// separates kronos from every other agent kraken spawns. Its brief REPLACES
// the default system prompt (which is why the brief carries its own
// tool-usage guidance), and an --append-system-prompt here would leave the
// harness' defaults in place while looking identical in a pane.
func TestContractKronosSpawnCommandReplacesRatherThanAppends(t *testing.T) {
	useTestStore(t)
	contractKronosDir(t)
	const sid = "11111111-2222-4333-8444-555555555555"

	for _, resume := range []bool{false, true} {
		cmd, err := kronosSpawnCommand(sid, resume)
		if err != nil {
			t.Fatalf("kronosSpawnCommand(resume=%v): %v", resume, err)
		}
		if !strings.Contains(cmd, "--system-prompt-file") {
			t.Errorf("resume=%v: command does not pass --system-prompt-file: %q", resume, cmd)
		}
		if strings.Contains(cmd, "--append-system-prompt") {
			t.Errorf("resume=%v: command APPENDS the brief instead of replacing the system prompt: %q", resume, cmd)
		}
	}
}

// TestContractKronosSpawnCommandStagesTheBriefOnEverySpawn: the brief is a
// FILE the pane's shell reads, and it is (re)written on every spawn INCLUDING
// a resume — a resumed session does not retain an earlier run's system prompt,
// and a resumed kronos without its brief is a plain claude holding an
// orchestrator's history, which is the one agent on the machine with the
// authority to steer cells.
func TestContractKronosSpawnCommandStagesTheBriefOnEverySpawn(t *testing.T) {
	useTestStore(t)
	dir := contractKronosDir(t)
	const sid = "11111111-2222-4333-8444-555555555555"
	briefPath := filepath.Join(dir, "kronos-brief.md")

	if _, err := kronosSpawnCommand(sid, false); err != nil {
		t.Fatalf("kronosSpawnCommand(first): %v", err)
	}
	staged, err := os.ReadFile(briefPath)
	if err != nil {
		t.Fatalf("read staged brief after the first spawn: %v", err)
	}
	if string(staged) != kronosBrief() {
		t.Fatalf("%s does not hold kronosBrief() — the spawn would deliver a different system prompt", briefPath)
	}

	// A stale file left by an older build must not survive a respawn.
	if err := os.WriteFile(briefPath, []byte("stale brief from an older build"), 0o644); err != nil {
		t.Fatalf("overwrite staged brief: %v", err)
	}
	if _, err := kronosSpawnCommand(sid, true); err != nil {
		t.Fatalf("kronosSpawnCommand(resume): %v", err)
	}
	restaged, err := os.ReadFile(briefPath)
	if err != nil {
		t.Fatalf("read staged brief after the resume: %v", err)
	}
	if string(restaged) != kronosBrief() {
		t.Fatalf("the brief was not re-written on the RESUME path: %q", string(restaged))
	}
}

// TestContractKronosSpawnCommandStagesThePromptLast: the initial prompt is
// staged too and rides as the final `--` argument, so a flag appended past it
// would be swallowed into claude's first message rather than parsed.
func TestContractKronosSpawnCommandStagesThePromptLast(t *testing.T) {
	useTestStore(t)
	dir := contractKronosDir(t)
	const sid = "11111111-2222-4333-8444-555555555555"

	cmd, err := kronosSpawnCommand(sid, false)
	if err != nil {
		t.Fatalf("kronosSpawnCommand: %v", err)
	}
	promptPath := filepath.Join(dir, "kronos-prompt.md")
	if _, err := os.Stat(promptPath); err != nil {
		t.Fatalf("stat %s: %v — the initial prompt is staged beside the brief", promptPath, err)
	}
	if !strings.Contains(cmd, promptPath) {
		t.Errorf("command does not read the staged prompt: %q", cmd)
	}
	if i, k := strings.Index(cmd, " -- "), strings.Index(cmd, "--system-prompt-file"); i >= 0 && i < k {
		t.Errorf("command puts -- before --system-prompt-file: %q", cmd)
	}
	// Staged, not inlined: tmux rejects a command over ~16KB with a bare
	// `command too long`, which is how a brief that grew by inlining once
	// wedged every cell spawn.
	if len(cmd) > 4096 {
		t.Errorf("spawn command is %d bytes — the brief and prompt must be staged in files, not inlined", len(cmd))
	}
}

// ---------------------------------------------------------------------------
// kronosBrief
// ---------------------------------------------------------------------------

// TestContractKronosBriefPinsTheFiveRules is the brief-content matrix. These
// five sentences are frozen VERBATIM by the contract because each one is a way
// kronos can do lasting damage if it forgets: act on a cell nobody asked it
// to, lose an escalation by never acking it, act without telling carl, trust a
// compacted conversation over the rows, or restate a credential into a
// transcript that is swept forward forever.
func TestContractKronosBriefPinsTheFiveRules(t *testing.T) {
	useTestStore(t)
	brief := kronosBrief()
	if strings.TrimSpace(brief) == "" {
		t.Fatal("kronosBrief() is empty")
	}
	for _, want := range []string{
		"You steer, submit, or answer a cell only on carl's explicit instruction — never on your own initiative.",
		"Ack every inbox row you read, exactly once.",
		"Receipt every action back to the channel the instruction came from.",
		"Your memory is the rows and the graph, not this conversation.",
		"Never restate a secret's value.",
	} {
		if !strings.Contains(brief, want) {
			t.Errorf("kronosBrief() does not contain, verbatim: %q", want)
		}
	}
}

// TestContractKronosBriefNamesEveryVerbAndTheModel: the brief REPLACES the
// default system prompt, so a verb it does not name is a capability kronos
// does not know it has — there is no other place in context that lists them.
// The model is stated as the RESOLVED id rather than a constant's name, so an
// agent asked what it is running on answers correctly.
func TestContractKronosBriefNamesEveryVerbAndTheModel(t *testing.T) {
	useTestStore(t)
	brief := kronosBrief()
	for _, want := range []string{
		"kraken orch inbox",
		"kraken orch ack",
		"kraken board get",
		"kraken cell steer",
		"kraken cell submit",
		"kraken cell answer",
		"kraken graph",
		"kraken goals get",
		core.KronosModel,
	} {
		if !strings.Contains(brief, want) {
			t.Errorf("kronosBrief() never names %q", want)
		}
	}
	if !strings.Contains(strings.ToLower(brief), "escalat") {
		t.Error("kronosBrief() never states the escalation default (Q4: escalate-all)")
	}
	if !strings.Contains(strings.ToLower(brief), "carl") {
		t.Error("kronosBrief() never names carl — the brief must say whose instruction authorises an action")
	}
}

// TestContractKronosBriefRestatesNoSkill: the brief is prose in Go for the
// reason zeus' is — a system prompt is re-sent every turn — but kronos runs no
// pipeline phase, so a skill restated here would be procedure with no owner
// and would drift silently from the repo-owned copy.
func TestContractKronosBriefRestatesNoSkill(t *testing.T) {
	useTestStore(t)
	brief := kronosBrief()
	for _, banned := range []string{".claude/skills", "/aim", "/dream", "hephaestus", "athena"} {
		if strings.Contains(brief, banned) {
			t.Errorf("kronosBrief() restates skill/crew material (%q) — kronos runs no pipeline phase", banned)
		}
	}
}

// ---------------------------------------------------------------------------
// installKronosSettings
// ---------------------------------------------------------------------------

// contractKronosHookGroup is Claude Code's own settings shape, not an
// invention of this build: a hook event maps to groups, each with an optional
// matcher and a list of commands.
type contractKronosHookGroup struct {
	Matcher string `json:"matcher"`
	Hooks   []struct {
		Type    string `json:"type"`
		Command string `json:"command"`
	} `json:"hooks"`
}

func contractKronosReadSettings(t *testing.T, dir string) (map[string]json.RawMessage, map[string][]contractKronosHookGroup, string) {
	t.Helper()
	path := filepath.Join(dir, ".claude", "settings.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatalf("decode %s: %v\n%s", path, err, raw)
	}
	hooks := map[string][]contractKronosHookGroup{}
	if h, ok := top["hooks"]; ok {
		if err := json.Unmarshal(h, &hooks); err != nil {
			t.Fatalf("decode hooks in %s: %v\n%s", path, err, h)
		}
	}
	return top, hooks, string(raw)
}

func contractKronosHookCommands(groups []contractKronosHookGroup) []string {
	var out []string
	for _, g := range groups {
		for _, h := range g.Hooks {
			out = append(out, h.Command)
		}
	}
	return out
}

func contractKronosAnyContains(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// TestContractKronosSettingsCarryExactlyTheIntendedHooks: kronos' directory is
// NOT a cell, so the cell's hook set is the wrong set. It needs the graph
// (both recall hooks), the shell guard (bash-lint), the re-arm that keeps
// recall useful across a compaction, and the statusline that feeds the context
// sentinel — and it must NOT carry the pr-hook (there is no PR) or the
// agent-teams keys (kronos delegates to nobody). The limit-hook it DOES carry
// now, in its --agent form: kronos burns the same one Claude login a cell
// does, so the standing rescue needs the same fast trigger.
func TestContractKronosSettingsCarryExactlyTheIntendedHooks(t *testing.T) {
	useTestStore(t)
	dir := t.TempDir()
	const bin = "/opt/kraken/bin/kraken"

	if err := installKronosSettings(dir, bin); err != nil {
		t.Fatalf("installKronosSettings: %v", err)
	}
	top, hooks, raw := contractKronosReadSettings(t, dir)

	for _, event := range []string{"UserPromptSubmit", "PostToolUse", "PreToolUse", "PostCompact"} {
		if len(hooks[event]) == 0 {
			t.Errorf("settings carry no %s hook group", event)
		}
	}
	for _, event := range []string{"UserPromptSubmit", "PostToolUse"} {
		cmds := contractKronosHookCommands(hooks[event])
		if !contractKronosAnyContains(cmds, "recall-hook") {
			t.Errorf("%s does not run the recall hook: %v", event, cmds)
		}
	}
	if cmds := contractKronosHookCommands(hooks["PreToolUse"]); !contractKronosAnyContains(cmds, "bash-lint") {
		t.Errorf("PreToolUse does not run the bash-lint guard: %v", cmds)
	}
	if cmds := contractKronosHookCommands(hooks["PostCompact"]); !contractKronosAnyContains(cmds, "rearm-hook") {
		t.Errorf("PostCompact does not run the recall re-arm: %v", cmds)
	} else if !contractKronosAnyContains(cmds, bin) {
		t.Errorf("PostCompact does not run the kraken binary it was handed (%s): %v", bin, cmds)
	}

	if _, ok := top["statusLine"]; !ok {
		t.Error("settings carry no statusLine entry — the context sentinel is fed by the statusline command")
	} else if !strings.Contains(string(top["statusLine"]), "ctx-hook") || !strings.Contains(string(top["statusLine"]), bin) {
		t.Errorf("statusLine = %s, want it running %s ctx-hook", top["statusLine"], bin)
	}

	for _, banned := range []string{"pr-hook", "CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS", "teammateMode", "autoCompactEnabled"} {
		if strings.Contains(raw, banned) {
			t.Errorf("settings carry %q, which kronos' directory must not have:\n%s", banned, raw)
		}
	}
}

// TestContractKronosSettingsMergeRatherThanClobber: the file may already carry
// entries carl put there by hand, and an install that replaced it would delete
// them silently on every daemon restart.
func TestContractKronosSettingsMergeRatherThanClobber(t *testing.T) {
	useTestStore(t)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
		t.Fatalf("mkdir .claude: %v", err)
	}
	pre := `{"model":"opus","hooks":{"Notification":[{"hooks":[{"type":"command","command":"say something"}]}]}}`
	if err := os.WriteFile(filepath.Join(dir, ".claude", "settings.json"), []byte(pre), 0o644); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	if err := installKronosSettings(dir, "/opt/kraken/bin/kraken"); err != nil {
		t.Fatalf("installKronosSettings: %v", err)
	}
	top, hooks, raw := contractKronosReadSettings(t, dir)

	if string(top["model"]) != `"opus"` {
		t.Errorf("an unrelated top-level key was lost: model = %s\n%s", top["model"], raw)
	}
	if cmds := contractKronosHookCommands(hooks["Notification"]); !contractKronosAnyContains(cmds, "say something") {
		t.Errorf("an unrelated hook group was lost: Notification = %v\n%s", cmds, raw)
	}
	if cmds := contractKronosHookCommands(hooks["PostCompact"]); !contractKronosAnyContains(cmds, "rearm-hook") {
		t.Errorf("the merge did not install the re-arm hook: %v", cmds)
	}
}

// TestContractKronosSettingsAreIdempotent: the install runs on every spawn, so
// a second call must leave one copy of each entry rather than appending a
// duplicate that fires the hook twice per turn.
func TestContractKronosSettingsAreIdempotent(t *testing.T) {
	useTestStore(t)
	dir := t.TempDir()
	const bin = "/opt/kraken/bin/kraken"

	for i := 0; i < 3; i++ {
		if err := installKronosSettings(dir, bin); err != nil {
			t.Fatalf("installKronosSettings (call %d): %v", i+1, err)
		}
	}
	_, hooks, raw := contractKronosReadSettings(t, dir)
	for _, event := range []string{"UserPromptSubmit", "PostToolUse", "PreToolUse", "PostCompact"} {
		cmds := contractKronosHookCommands(hooks[event])
		if len(cmds) != 1 {
			t.Errorf("%s holds %d commands after three installs (%v), want 1\n%s", event, len(cmds), cmds, raw)
		}
	}
}

// ---------------------------------------------------------------------------
// EnqueueOrch
// ---------------------------------------------------------------------------

// TestContractKronosEnqueueOrchStoresTheRow: the thin wrapper's happy path —
// kind and slug verbatim, the summary readable, the row waiting for delivery.
func TestContractKronosEnqueueOrchStoresTheRow(t *testing.T) {
	st := useTestStore(t)
	contractKronosEnable(t)

	if err := EnqueueOrch(st, "job-blocked", "csv-export", "csv-export is blocked on a spec question"); err != nil {
		t.Fatalf("EnqueueOrch: %v", err)
	}
	items, err := st.UndeliveredOrch()
	if err != nil {
		t.Fatalf("UndeliveredOrch: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d rows, want 1", len(items))
	}
	if items[0].Kind != "job-blocked" || items[0].Slug != "csv-export" {
		t.Errorf("row = %+v, want kind/slug carried verbatim", items[0])
	}
	if items[0].Summary != "csv-export is blocked on a spec question" {
		t.Errorf("summary = %q, want it stored unchanged when it is already one short line", items[0].Summary)
	}
	if items[0].SeenAt == 0 {
		t.Error("seen_at = 0 — the wrapper must stamp when the transition was noticed")
	}
}

// TestContractKronosEnqueueOrchOneLinesAndCapsTheSummary: the summary is
// rendered into a one-line inbox and typed nowhere, but it is also the row's
// only description of what happened. A multi-line summary breaks the inbox's
// one-row-per-line reading; an unbounded one lets a stack trace or a whole PR
// body into the queue.
func TestContractKronosEnqueueOrchOneLinesAndCapsTheSummary(t *testing.T) {
	st := useTestStore(t)
	contractKronosEnable(t)

	multi := "first line\nsecond line\r\nthird\tline"
	if err := EnqueueOrch(st, "cell-unhealthy", "csv-export", multi); err != nil {
		t.Fatalf("EnqueueOrch multi-line: %v", err)
	}
	long := strings.Repeat("x", 3000)
	if err := EnqueueOrch(st, "start-failed", "dream-2026-08-14", long); err != nil {
		t.Fatalf("EnqueueOrch long: %v", err)
	}

	items, err := st.UnackedOrch()
	if err != nil {
		t.Fatalf("UnackedOrch: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d rows, want 2", len(items))
	}
	if strings.ContainsAny(items[0].Summary, "\n\r") {
		t.Errorf("summary %q still holds a line break", items[0].Summary)
	}
	if !strings.Contains(items[0].Summary, "first line") {
		t.Errorf("summary = %q, want the leading content kept", items[0].Summary)
	}
	if n := len([]rune(items[1].Summary)); n > 2000 {
		t.Errorf("summary is %d runes, want it capped at 2000", n)
	}
	if n := len([]rune(items[1].Summary)); n == 0 {
		t.Error("the long summary was capped to nothing — the cap truncates, it does not drop")
	}
}

// TestContractKronosEnqueueOrchAcceptsEverySpecifiedKind: the five kinds are
// the transition points the daemon already notifies on. A kind the store
// refuses would drop that whole class of escalation on the floor, and the
// failure would be one log line inside a tick.
func TestContractKronosEnqueueOrchAcceptsEverySpecifiedKind(t *testing.T) {
	st := useTestStore(t)
	contractKronosEnable(t)

	kinds := []string{"job-blocked", "cell-gone", "cell-unhealthy", "pr-merged", "start-failed"}
	for _, k := range kinds {
		slug := "a-job"
		if err := EnqueueOrch(st, k, slug, "summary for "+k); err != nil {
			t.Fatalf("EnqueueOrch(%q): %v", k, err)
		}
	}
	items, err := st.UnackedOrch()
	if err != nil {
		t.Fatalf("UnackedOrch: %v", err)
	}
	if len(items) != len(kinds) {
		t.Fatalf("got %d rows, want one per kind (%d)", len(items), len(kinds))
	}
	for i, k := range kinds {
		if items[i].Kind != k {
			t.Errorf("row %d kind = %q, want %q (rows come back in insertion order)", i, items[i].Kind, k)
		}
	}
}

// ---------------------------------------------------------------------------
// deliverOrch
// ---------------------------------------------------------------------------

// TestContractKronosDeliverOrchWithNoKronosIsASilentNoOp: an absent kronos is
// a plain no-op — the rows stay pending and the next tick retries. Marking
// them delivered anyway would retire a bump nobody received, and the inbox row
// would then wait forever for an ack from an agent that never heard about it.
func TestContractKronosDeliverOrchWithNoKronosIsASilentNoOp(t *testing.T) {
	st := useTestStore(t)
	contractKronosEnable(t)

	if err := EnqueueOrch(st, "job-blocked", "csv-export", "blocked"); err != nil {
		t.Fatalf("EnqueueOrch: %v", err)
	}

	// No kronos in the snapshot: an empty one, then one holding only agents
	// that are NOT kronos (another session's kronos-named agent and a nested
	// subagent), which is what makes this measure the lookup rather than the
	// empty slice.
	deliverOrch(st, nil)
	deliverOrch(st, []*core.Peer{
		{TmuxSession: "kraken-demo-1", Name: KronosAgentName, Agent: "claude", Pane: "%1", Depth: 0, Role: "agent"},
		{TmuxSession: KronosSessionName, Name: "some-subagent", Agent: "claude", Pane: "%2", Depth: 1, Role: "agent"},
	})

	items, err := st.UndeliveredOrch()
	if err != nil {
		t.Fatalf("UndeliveredOrch: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("UndeliveredOrch = %+v, want the row still pending delivery", items)
	}
	if items[0].DeliveredAt != 0 {
		t.Errorf("delivered_at = %d, want 0 — nothing was sent, so nothing may be watermarked", items[0].DeliveredAt)
	}
}

// ---------------------------------------------------------------------------
// RecordKronosContext + ctxPass
// ---------------------------------------------------------------------------

// TestContractKronosRecordContextPersistsTheReport: the statusline posts every
// few seconds and the threshold pass reads what it left, so the four fields
// have to survive the round trip under the stated kv key.
func TestContractKronosRecordContextPersistsTheReport(t *testing.T) {
	st := useTestStore(t)
	contractKronosEnable(t)

	if err := RecordKronosContext(st, "sess-a", 42.5, 1_000_000); err != nil {
		t.Fatalf("RecordKronosContext: %v", err)
	}
	raw, err := st.GetKV("kronos_ctx")
	if err != nil {
		t.Fatalf("GetKV kronos_ctx: %v", err)
	}
	var got struct {
		SessionID string  `json:"session_id"`
		Pct       float64 `json:"pct"`
		Window    int64   `json:"window"`
		At        int64   `json:"at"`
	}
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("decode kronos_ctx (%q): %v", raw, err)
	}
	if got.SessionID != "sess-a" {
		t.Errorf("session_id = %q, want sess-a", got.SessionID)
	}
	if got.Pct != 42.5 {
		t.Errorf("pct = %v, want 42.5 (a fractional percentage is not rounded away)", got.Pct)
	}
	if got.Window != 1_000_000 {
		t.Errorf("window = %d, want the payload's own window size — Fable is 1M on the API, and a hardcoded 200k would warn five times early", got.Window)
	}
	if got.At == 0 {
		t.Error("at = 0 — the report's age is what makes a stale reading readable as stale")
	}

	// a later report replaces the earlier one rather than accumulating
	if err := RecordKronosContext(st, "sess-a", 51, 1_000_000); err != nil {
		t.Fatalf("second RecordKronosContext: %v", err)
	}
	raw, err = st.GetKV("kronos_ctx")
	if err != nil {
		t.Fatalf("GetKV after the second report: %v", err)
	}
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("decode kronos_ctx: %v", err)
	}
	if got.Pct != 51 {
		t.Errorf("pct = %v after a second report, want 51", got.Pct)
	}
}

// TestContractKronosRecordContextRefusals: a blank session cannot be keyed on
// (the fired-set and the re-arm's authorization both hang off it), and a
// percentage outside 0-100 is a malformed payload rather than a reading. Both
// bounds are INCLUSIVE — an empty context and a full one are both real states.
func TestContractKronosRecordContextRefusals(t *testing.T) {
	st := useTestStore(t)
	contractKronosEnable(t)

	if err := RecordKronosContext(st, "", 50, 1_000_000); err == nil {
		t.Error("RecordKronosContext with a blank session = nil error, want a refusal")
	}
	for _, pct := range []float64{-1, 100.5, 1000} {
		if err := RecordKronosContext(st, "sess-a", pct, 1_000_000); err == nil {
			t.Errorf("RecordKronosContext with pct %v = nil error, want a refusal", pct)
		}
	}
	for _, pct := range []float64{0, 100} {
		if err := RecordKronosContext(st, "sess-a", pct, 1_000_000); err != nil {
			t.Errorf("RecordKronosContext with pct %v = %v, want it accepted (the range is inclusive)", pct, err)
		}
	}
}

// TestContractKronosCtxPassFiresEachThresholdOnceOnTheWayUp is the sentinel's
// whole state machine, driven through the two frozen symbols that feed it. The
// dedupe is what makes the warning readable at all: a pass that warned on
// every tick above 50% would raise a desktop notification every fifteen
// seconds for the rest of the session.
func TestContractKronosCtxPassFiresEachThresholdOnceOnTheWayUp(t *testing.T) {
	st := useTestStore(t)
	contractKronosEnable(t)
	// Round 2 moved the send off the tick (a goroutine, single-flighted), so
	// the seam is recorded under a lock and every assertion waits for the
	// in-flight send to finish first. Asserting straight after ctxPass returns
	// would be reading a value another goroutine is still writing.
	rec := contractKronosSwapNotify(t)

	report := func(pct float64) {
		t.Helper()
		if err := RecordKronosContext(st, "sess-a", pct, 1_000_000); err != nil {
			t.Fatalf("RecordKronosContext(%v): %v", pct, err)
		}
		ctxPass(st)
		contractKronosSettleSends(t)
	}
	count := func() int { return rec.count() }

	report(30)
	if count() != 0 {
		t.Fatalf("a 30%% reading sent %d warning(s), want none", count())
	}

	report(51)
	if count() != 1 {
		t.Fatalf("crossing 50%% sent %d warning(s), want exactly 1", count())
	}
	if !strings.Contains(strings.ToLower(rec.at(0)), "kronos") || !strings.Contains(rec.at(0), "51") {
		t.Errorf("the 50%% warning names neither kronos nor the percentage: %q", rec.at(0))
	}

	report(60)
	if count() != 1 {
		t.Fatalf("a further reading inside the same band sent %d warning(s) in total, want still 1", count())
	}
	ctxPass(st)
	contractKronosSettleSends(t)
	if count() != 1 {
		t.Fatalf("a pass with no NEW reading sent another warning (%d in total)", count())
	}

	report(76)
	if count() != 2 {
		t.Fatalf("crossing 75%% brought the total to %d, want 2", count())
	}
	report(92)
	if count() != 3 {
		t.Fatalf("crossing 90%% brought the total to %d, want 3", count())
	}
	// Q3: auto-compact stays ON as the last resort, so the top warning has to
	// say what happens if carl does nothing.
	if !strings.Contains(strings.ToLower(rec.at(2)), "compact") {
		t.Errorf("the 90%% warning never mentions auto-compact taking over: %q", rec.at(2))
	}

	// After a compact the percentage falls, which re-arms every threshold.
	report(20)
	if count() != 3 {
		t.Fatalf("falling back to 20%% sent a warning (%d in total), want none", count())
	}
	report(55)
	if count() != 4 {
		t.Fatalf("re-crossing 50%% after a compact brought the total to %d, want 4 — a threshold re-arms once the percentage falls below it", count())
	}
}

// TestContractKronosCtxPassResetsOnANewSession: the fired-set is keyed to the
// session id, so a kronos respawned on a fresh conversation warns from zero.
// Carrying the old set forward would silence the first warning of every new
// session — the one that matters most, since a new session's context climbs
// fastest.
func TestContractKronosCtxPassResetsOnANewSession(t *testing.T) {
	st := useTestStore(t)
	contractKronosEnable(t)
	rec := contractKronosSwapNotify(t) // round 2: the send is off the tick

	if err := RecordKronosContext(st, "sess-a", 30, 1_000_000); err != nil {
		t.Fatalf("RecordKronosContext: %v", err)
	}
	ctxPass(st)
	contractKronosSettleSends(t)
	if err := RecordKronosContext(st, "sess-a", 55, 1_000_000); err != nil {
		t.Fatalf("RecordKronosContext: %v", err)
	}
	ctxPass(st)
	contractKronosSettleSends(t)
	if rec.count() != 1 {
		t.Fatalf("sess-a sent %d warning(s), want 1", rec.count())
	}

	if err := RecordKronosContext(st, "sess-b", 55, 1_000_000); err != nil {
		t.Fatalf("RecordKronosContext for a new session: %v", err)
	}
	ctxPass(st)
	contractKronosSettleSends(t)
	if rec.count() != 2 {
		t.Fatalf("a NEW session at 55%% sent %d warning(s) in total, want 2 — the fired-set is per session", rec.count())
	}
}

// ---------------------------------------------------------------------------
// RearmKronosRecall
// ---------------------------------------------------------------------------

// contractKronosRecordAgent writes the kv singleton EnsureKronos maintains.
// Its VALUE shape is not spelled out by the contract; it is taken from the
// contract's own "iris' exact lifecycle, re-implemented for kronos", which
// makes iris' {session_id, spawned_at} JSON the one derivable answer. Recorded
// in the report as a disclosed silence.
func contractKronosRecordAgent(t *testing.T, st *store.Store, sessionID string) {
	t.Helper()
	raw, err := json.Marshal(struct {
		SessionID string `json:"session_id"`
		SpawnedAt int64  `json:"spawned_at"`
	}{sessionID, time.Now().UnixMilli()})
	if err != nil {
		t.Fatalf("marshal kronos_agent: %v", err)
	}
	if err := st.SetKV("kronos_agent", string(raw)); err != nil {
		t.Fatalf("SetKV kronos_agent: %v", err)
	}
}

// TestContractKronosRearmClearsOnlyKronosSession: compaction dropped the
// delivered facts out of kronos' context, so re-delivering them is correct —
// and correct ONLY for kronos. Every cell on the machine shares kg_recall.
func TestContractKronosRearmClearsOnlyKronosSession(t *testing.T) {
	st := useTestStore(t)
	contractKronosEnable(t)
	const sid = "11111111-2222-4333-8444-555555555555"
	contractKronosRecordAgent(t, st, sid)

	if err := st.MarkRecalled(sid, []int64{1, 2, 3}, time.Now().UnixMilli()); err != nil {
		t.Fatalf("MarkRecalled kronos: %v", err)
	}
	if err := st.MarkRecalled("a-cell-session", []int64{4}, time.Now().UnixMilli()); err != nil {
		t.Fatalf("MarkRecalled cell: %v", err)
	}

	n, err := RearmKronosRecall(st, sid)
	if err != nil {
		t.Fatalf("RearmKronosRecall: %v", err)
	}
	if n != 3 {
		t.Errorf("RearmKronosRecall = %d, want 3 cleared", n)
	}
	if ids, _ := st.RecalledEdgeIDs(sid); len(ids) != 0 {
		t.Errorf("kronos' recall marks = %v, want cleared", ids)
	}
	if ids, _ := st.RecalledEdgeIDs("a-cell-session"); !ids[4] {
		t.Errorf("a cell's recall marks were cleared too: %v", ids)
	}
}

// TestContractKronosRearmRefusesAnyOtherSession is the route's authorization
// in its store-side form: the PostCompact hook payload names a session id, and
// an unchecked one would let any compacting agent on the machine wipe another
// agent's delivery marks.
func TestContractKronosRearmRefusesAnyOtherSession(t *testing.T) {
	st := useTestStore(t)
	contractKronosEnable(t)
	const sid = "11111111-2222-4333-8444-555555555555"
	contractKronosRecordAgent(t, st, sid)
	if err := st.MarkRecalled(sid, []int64{1, 2}, time.Now().UnixMilli()); err != nil {
		t.Fatalf("MarkRecalled: %v", err)
	}
	if err := st.MarkRecalled("someone-else", []int64{3}, time.Now().UnixMilli()); err != nil {
		t.Fatalf("MarkRecalled other: %v", err)
	}

	for _, bad := range []string{"", "someone-else", "99999999-2222-4333-8444-555555555555"} {
		n, err := RearmKronosRecall(st, bad)
		if err == nil {
			t.Errorf("RearmKronosRecall(%q) = nil error, want a refusal", bad)
		}
		if n != 0 {
			t.Errorf("RearmKronosRecall(%q) reported %d cleared, want 0", bad, n)
		}
	}
	if ids, _ := st.RecalledEdgeIDs(sid); len(ids) != 2 {
		t.Errorf("kronos' marks = %v, want both intact after refusals", ids)
	}
	if ids, _ := st.RecalledEdgeIDs("someone-else"); !ids[3] {
		t.Errorf("another session's marks = %v, want intact after refusals", ids)
	}
}

// TestContractKronosRearmWithNoKronosRecordedIsAnError: with no session id on
// record there is nothing to compare against, so the answer is a refusal
// rather than a wipe. Treating "unknown" as "authorised" is the shape of the
// bug that deletes everything.
func TestContractKronosRearmWithNoKronosRecordedIsAnError(t *testing.T) {
	st := useTestStore(t)
	contractKronosEnable(t)
	if err := st.MarkRecalled("a-cell-session", []int64{1, 2}, time.Now().UnixMilli()); err != nil {
		t.Fatalf("MarkRecalled: %v", err)
	}

	n, err := RearmKronosRecall(st, "11111111-2222-4333-8444-555555555555")
	if err == nil {
		t.Error("RearmKronosRecall with no kronos recorded = nil error, want a refusal")
	}
	if n != 0 {
		t.Errorf("RearmKronosRecall reported %d cleared, want 0", n)
	}
	if ids, _ := st.RecalledEdgeIDs("a-cell-session"); len(ids) != 2 {
		t.Errorf("recall marks = %v, want nothing deleted", ids)
	}
}

// ---------------------------------------------------------------------------
// ROUND 2 — the amended contract's two frozen delivery symbols
//
// 50-contract.md § "Amendments (round 1)" froze `orchBumpMessage(n int)
// string` and `var orchSendToPeer = core.SendToPeer`, which is what makes the
// bump text and send-then-watermark observable at all. Round 1 disclosed both
// as unreachable (report J3); this section is the coverage that disclosure
// bought. Still blind: nothing under internal/jobs/orch.go was read.
// ---------------------------------------------------------------------------

// contractKronosBumpFormat is the amendment's frozen wording, kept in ONE
// place so the two count cases cannot drift apart in the test itself. The
// literal `(s)` is the point: the amendment picks one wording for every count
// rather than slack's singular/plural pair.
const contractKronosBumpFormat = "orch: %d new item(s) — run: kraken orch inbox"

// contractKronosSend is one recorded delivery attempt: which pane it was typed
// at, and exactly what text.
type contractKronosSend struct {
	pane string
	msg  string
}

// contractKronosSwapOrchSend installs a recording delivery seam through the
// contract's orchSendToPeer package var and restores the real one. fn decides
// what the send REPORTS — the three answers that matter are (false, nil)
// delivered, (true, nil) queued, and (_, err) failed.
func contractKronosSwapOrchSend(t *testing.T, fn func(*core.Peer, string) (bool, error)) *[]contractKronosSend {
	t.Helper()
	var seen []contractKronosSend
	prev := orchSendToPeer
	orchSendToPeer = func(p *core.Peer, msg string) (bool, error) {
		pane := ""
		if p != nil {
			pane = p.Pane
		}
		seen = append(seen, contractKronosSend{pane: pane, msg: msg})
		if fn == nil {
			return false, nil
		}
		return fn(p, msg)
	}
	t.Cleanup(func() { orchSendToPeer = prev })
	return &seen
}

// contractKronosLivePeer builds the discovery row findKronos accepts: a named,
// depth-0, messageable claude in the kronos session.
func contractKronosLivePeer(pane string) *core.Peer {
	return &core.Peer{
		TmuxSession: KronosSessionName,
		Name:        KronosAgentName,
		Agent:       "claude",
		Pane:        pane,
		Depth:       0,
		Role:        "agent",
		Messageable: true,
		Status:      "idle",
	}
}

// contractKronosDeliveredAt reads one row's delivered_at back through the
// unacked listing, which is the only read that still returns a delivered row.
func contractKronosDeliveredAt(t *testing.T, st *store.Store, id int64) int64 {
	t.Helper()
	items, err := st.UnackedOrch()
	if err != nil {
		t.Fatalf("UnackedOrch: %v", err)
	}
	for _, it := range items {
		if it.ID == id {
			return it.DeliveredAt
		}
	}
	t.Fatalf("row %d is no longer unacked, so its delivered_at cannot be read", id)
	return 0
}

// --- orchBumpMessage ---

// TestContractKronosOrchBumpMessageExactStrings: this text is TYPED into a
// live claude pane, so it is frozen to the byte. One wording for every count —
// the literal `(s)` rather than a singular/plural pair — because two wordings
// are two strings for kronos to recognise and the count is already in the
// line.
func TestContractKronosOrchBumpMessageExactStrings(t *testing.T) {
	for _, n := range []int{1, 3} {
		want := fmt.Sprintf(contractKronosBumpFormat, n)
		if got := orchBumpMessage(n); got != want {
			t.Errorf("orchBumpMessage(%d) = %q, want %q", n, got, want)
		}
	}
	for _, n := range []int{1, 2, 3, 17} {
		got := orchBumpMessage(n)
		if !strings.Contains(got, "new item(s)") {
			t.Errorf("orchBumpMessage(%d) = %q, want the literal \"(s)\" at every count — the amendment picks ONE wording", n, got)
		}
		if strings.Contains(got, "\n") {
			t.Errorf("orchBumpMessage(%d) = %q, want a single line — this is typed into a pane", n, got)
		}
		if !strings.Contains(got, "kraken orch inbox") {
			t.Errorf("orchBumpMessage(%d) = %q, want it naming the verb kronos runs next", n, got)
		}
		if !strings.Contains(got, fmt.Sprintf("%d", n)) {
			t.Errorf("orchBumpMessage(%d) = %q, want the count substituted", n, got)
		}
	}
}

// --- deliverOrch: send-then-watermark ---

// TestContractKronosDeliverOrchBumpsOnceThenWatermarks is the happy path of
// the whole delivery contract: ONE count-only bump for the batch, then the
// marks. One bump rather than one per row because kronos reads the inbox
// itself — the pane text is a doorbell, not a payload.
func TestContractKronosDeliverOrchBumpsOnceThenWatermarks(t *testing.T) {
	st := useTestStore(t)
	contractKronosEnable(t)
	seen := contractKronosSwapOrchSend(t, func(*core.Peer, string) (bool, error) { return false, nil })

	for _, r := range []struct{ kind, slug, summary string }{
		{"job-blocked", "csv-export", "csv-export is blocked on a spec question"},
		{"cell-unhealthy", "other-job", "what is blocking the other job: its zeus is stalled"},
		{"pr-merged", "csv-export", "PR #12 merged"},
	} {
		if err := EnqueueOrch(st, r.kind, r.slug, r.summary); err != nil {
			t.Fatalf("EnqueueOrch(%q): %v", r.kind, err)
		}
	}

	deliverOrch(st, []*core.Peer{contractKronosLivePeer("%7")})

	if len(*seen) != 1 {
		t.Fatalf("the seam ran %d times for a 3-row batch, want exactly 1 bump", len(*seen))
	}
	if (*seen)[0].pane != "%7" {
		t.Errorf("the bump went to pane %q, want kronos' own pane %%7", (*seen)[0].pane)
	}
	if want := orchBumpMessage(3); (*seen)[0].msg != want {
		t.Fatalf("bump = %q, want orchBumpMessage(3) = %q", (*seen)[0].msg, want)
	}
	// count-only, never content: a summary in the pane text would put an
	// escalation's words somewhere kronos can read them without ever acking
	// the row they came from.
	for _, content := range []string{"spec question", "what is blocking", "PR #12"} {
		if strings.Contains((*seen)[0].msg, content) {
			t.Errorf("bump %q carries row CONTENT (%q) — the bump is count-only", (*seen)[0].msg, content)
		}
	}

	undelivered, err := st.UndeliveredOrch()
	if err != nil {
		t.Fatalf("UndeliveredOrch: %v", err)
	}
	if len(undelivered) != 0 {
		t.Fatalf("UndeliveredOrch = %+v, want empty after a confirmed send", undelivered)
	}
	unacked, err := st.UnackedOrch()
	if err != nil {
		t.Fatalf("UnackedOrch: %v", err)
	}
	if len(unacked) != 3 {
		t.Fatalf("UnackedOrch = %d rows, want all 3 — delivery does not retire a row, the ACK does", len(unacked))
	}
	for _, it := range unacked {
		if it.DeliveredAt == 0 {
			t.Errorf("row %d was bumped but never stamped: %+v", it.ID, it)
		}
		if it.AckedAt != 0 || it.Disposition != "" {
			t.Errorf("delivery wrote the ack fields on row %d: %+v", it.ID, it)
		}
	}

	// Nothing is left to deliver, so a second pass is silent — no repeat bump.
	deliverOrch(st, []*core.Peer{contractKronosLivePeer("%7")})
	if len(*seen) != 1 {
		t.Fatalf("a second pass with nothing undelivered sent another bump (%d in total)", len(*seen))
	}
}

// TestContractKronosDeliverOrchFailedSendLeavesEveryRowUndelivered is the
// ordering's whole purpose. The watermark is a record that a bump LANDED, so a
// send that reports failure must leave the rows exactly where they were — a
// crash costs a duplicate bump, never a silently dropped batch.
func TestContractKronosDeliverOrchFailedSendLeavesEveryRowUndelivered(t *testing.T) {
	st := useTestStore(t)
	contractKronosEnable(t)
	seen := contractKronosSwapOrchSend(t, func(*core.Peer, string) (bool, error) {
		return false, errors.New("pane %7 is no longer kronos")
	})

	for i, summary := range []string{"first", "second"} {
		if err := EnqueueOrch(st, "job-blocked", fmt.Sprintf("job-%d", i), summary); err != nil {
			t.Fatalf("EnqueueOrch: %v", err)
		}
	}

	deliverOrch(st, []*core.Peer{contractKronosLivePeer("%7")})

	if len(*seen) != 1 {
		t.Fatalf("the seam ran %d times, want 1 attempt", len(*seen))
	}
	undelivered, err := st.UndeliveredOrch()
	if err != nil {
		t.Fatalf("UndeliveredOrch: %v", err)
	}
	if len(undelivered) != 2 {
		t.Fatalf("UndeliveredOrch = %+v, want BOTH rows still pending after a failed send", undelivered)
	}
	for _, it := range undelivered {
		if it.DeliveredAt != 0 {
			t.Errorf("row %d was watermarked despite the send failing: %+v", it.ID, it)
		}
	}

	// ...and the next tick retries, which is the behaviour the un-stamped rows
	// exist to produce.
	deliverOrch(st, []*core.Peer{contractKronosLivePeer("%7")})
	if len(*seen) != 2 {
		t.Fatalf("the seam ran %d times in total, want a retry on the next pass", len(*seen))
	}
	if want := orchBumpMessage(2); (*seen)[1].msg != want {
		t.Errorf("retry bump = %q, want %q", (*seen)[1].msg, want)
	}
}

// TestContractKronosDeliverOrchQueuedSendCountsAsDelivered: core.SendToPeer
// reports `queued` when the pane is mid-turn — the keys are in the pane and
// claude will read them when it finishes. That is a landed bump, so it
// watermarks. Treating it as a failure would re-bump the same batch on every
// tick for as long as kronos stayed busy, which is exactly when it is least
// able to absorb the noise.
func TestContractKronosDeliverOrchQueuedSendCountsAsDelivered(t *testing.T) {
	st := useTestStore(t)
	contractKronosEnable(t)
	seen := contractKronosSwapOrchSend(t, func(*core.Peer, string) (bool, error) { return true, nil })

	if err := EnqueueOrch(st, "cell-gone", "csv-export", "the cell is gone"); err != nil {
		t.Fatalf("EnqueueOrch: %v", err)
	}

	deliverOrch(st, []*core.Peer{contractKronosLivePeer("%7")})

	if len(*seen) != 1 {
		t.Fatalf("the seam ran %d times, want 1", len(*seen))
	}
	if want := orchBumpMessage(1); (*seen)[0].msg != want {
		t.Errorf("bump = %q, want %q", (*seen)[0].msg, want)
	}
	undelivered, err := st.UndeliveredOrch()
	if err != nil {
		t.Fatalf("UndeliveredOrch: %v", err)
	}
	if len(undelivered) != 0 {
		t.Fatalf("UndeliveredOrch = %+v, want empty — a QUEUED send is a landed bump", undelivered)
	}
}

// TestContractKronosDeliverOrchStampsExactlyTheBumpedRows: the worklist is
// UndeliveredOrch, so the count in the bump and the set that gets stamped are
// the same set — and nothing outside it moves. A row already delivered keeps
// its ORIGINAL stamp (re-stamping would say the second bump announced it, when
// no bump ever named it again), and an acked row stays settled.
func TestContractKronosDeliverOrchStampsExactlyTheBumpedRows(t *testing.T) {
	st := useTestStore(t)
	contractKronosEnable(t)

	pending, err := st.AddOrchItem("job-blocked", "csv-export", "still pending", 1_755_000_000_001)
	if err != nil {
		t.Fatalf("AddOrchItem pending: %v", err)
	}
	alreadyBumped, err := st.AddOrchItem("cell-gone", "other-job", "already bumped", 1_755_000_000_002)
	if err != nil {
		t.Fatalf("AddOrchItem alreadyBumped: %v", err)
	}
	settled, err := st.AddOrchItem("pr-merged", "third-job", "already handled", 1_755_000_000_003)
	if err != nil {
		t.Fatalf("AddOrchItem settled: %v", err)
	}

	const oldStamp = int64(1_755_000_005_000)
	if err := st.MarkOrchDelivered([]int64{alreadyBumped}, oldStamp); err != nil {
		t.Fatalf("MarkOrchDelivered: %v", err)
	}
	if err := st.AckOrchItems([]int64{settled}, "escalated", 1_755_000_006_000); err != nil {
		t.Fatalf("AckOrchItems: %v", err)
	}

	seen := contractKronosSwapOrchSend(t, func(*core.Peer, string) (bool, error) { return false, nil })
	deliverOrch(st, []*core.Peer{contractKronosLivePeer("%7")})

	if len(*seen) != 1 {
		t.Fatalf("the seam ran %d times, want 1", len(*seen))
	}
	if want := orchBumpMessage(1); (*seen)[0].msg != want {
		t.Fatalf("bump = %q, want %q — only the ONE row on the worklist is announced", (*seen)[0].msg, want)
	}

	if got := contractKronosDeliveredAt(t, st, pending); got == 0 {
		t.Errorf("the bumped row was not stamped")
	}
	if got := contractKronosDeliveredAt(t, st, alreadyBumped); got != oldStamp {
		t.Errorf("an already-delivered row's stamp moved: %d, want the original %d", got, oldStamp)
	}
	items, err := st.UnackedOrch()
	if err != nil {
		t.Fatalf("UnackedOrch: %v", err)
	}
	for _, it := range items {
		if it.ID == settled {
			t.Fatalf("the acked row came back onto the unacked list: %+v", it)
		}
	}
}

// TestContractKronosDeliverOrchSendsNothingWithAnEmptyWorklist: the pass runs
// on every tick and the common case is nothing new. A bump of `0 new item(s)`
// would type into kronos' pane fifteen seconds apart, forever, interrupting
// whatever it was doing to announce nothing.
//
// Derived rather than quoted (the contract states the delivery order, not this
// case) and disclosed as such in the round-2 report.
func TestContractKronosDeliverOrchSendsNothingWithAnEmptyWorklist(t *testing.T) {
	st := useTestStore(t)
	contractKronosEnable(t)
	seen := contractKronosSwapOrchSend(t, func(*core.Peer, string) (bool, error) { return false, nil })

	deliverOrch(st, []*core.Peer{contractKronosLivePeer("%7")})
	if len(*seen) != 0 {
		t.Fatalf("an empty worklist produced %d bump(s): %+v", len(*seen), *seen)
	}
}

// TestContractKronosDeliverOrchNeverSendsWithNoKronos re-states round 1's
// no-op through the SEAM, which is the form that can actually see it: with no
// kronos in the snapshot the delivery pass must not reach the send at all,
// rather than sending at a peer it picked as a fallback.
func TestContractKronosDeliverOrchNeverSendsWithNoKronos(t *testing.T) {
	st := useTestStore(t)
	contractKronosEnable(t)
	seen := contractKronosSwapOrchSend(t, func(*core.Peer, string) (bool, error) { return false, nil })

	if err := EnqueueOrch(st, "job-blocked", "csv-export", "blocked"); err != nil {
		t.Fatalf("EnqueueOrch: %v", err)
	}

	deliverOrch(st, nil)
	deliverOrch(st, []*core.Peer{
		{TmuxSession: "kraken-demo-1", Name: KronosAgentName, Agent: "claude", Pane: "%1", Depth: 0, Role: "agent", Messageable: true},
		{TmuxSession: KronosSessionName, Name: "some-subagent", Agent: "claude", Pane: "%2", Depth: 1, Role: "agent", Messageable: true},
	})

	if len(*seen) != 0 {
		t.Fatalf("the pass sent %d bump(s) with no kronos present: %+v", len(*seen), *seen)
	}
	undelivered, err := st.UndeliveredOrch()
	if err != nil {
		t.Fatalf("UndeliveredOrch: %v", err)
	}
	if len(undelivered) != 1 {
		t.Fatalf("UndeliveredOrch = %+v, want the row still pending", undelivered)
	}
}

// TestContractKronosDeliverOrchIsGatedOnTheFeatureFlagThroughTheSeam: the
// round-1 gate test could only observe the absence of a watermark, which a
// pass that bumped but failed to stamp would also produce. With the seam in
// hand the stronger statement is available: a disabled kronos is never TYPED
// at.
func TestContractKronosDeliverOrchIsGatedOnTheFeatureFlagThroughTheSeam(t *testing.T) {
	st := useTestStore(t)
	// deliberately NOT contractKronosEnable(t)
	seen := contractKronosSwapOrchSend(t, func(*core.Peer, string) (bool, error) { return false, nil })

	if _, err := st.AddOrchItem("job-blocked", "csv-export", "blocked", 1_755_000_000_001); err != nil {
		t.Fatalf("AddOrchItem: %v", err)
	}

	deliverOrch(st, []*core.Peer{contractKronosLivePeer("%7")})

	if len(*seen) != 0 {
		t.Fatalf("a disabled kronos was bumped %d time(s): %+v", len(*seen), *seen)
	}
}

// ---------------------------------------------------------------------------
// ROUND 3 — pr-merged's dedup exemption
//
// Authored SIGHTED. Every assertion derives from 50-contract.md § "Amendments
// (round 3 — re-review fixes)".
// ---------------------------------------------------------------------------

// TestContractKronosPRMergedIsExemptFromThePendingDedup pins both directions of
// the round-3 exemption, because the amendment is a statement about WHICH kinds
// dedupe rather than about deduping at all.
//
// pr-merged is the one exempt kind, because its events are per-URL and
// unrepeatable. A job's branch recycles after each merge, the daemon keeps no
// in-memory key for a merge (a PR is newly-merged exactly once ever), and once
// the new state is committed nothing reports it again — so a suppressed second
// merge loses that URL from this channel permanently.
//
// job-blocked keeps the dedup, and that is the half that makes the exemption
// meaningful: an unacked row of the same kind and slug says the condition has
// already been reported and not yet handled, which is what the in-memory key
// would have said had it survived the restart.
//
// Authored SIGHTED post-review (round 3).
func TestContractKronosPRMergedIsExemptFromThePendingDedup(t *testing.T) {
	st := useTestStore(t)
	contractKronosEnable(t)

	enqueueOrchNotice("pr-merged", "csv-export", "PR merged — https://example.test/pull/12")
	enqueueOrchNotice("pr-merged", "csv-export", "PR merged — https://example.test/pull/13")

	items, err := st.UnackedOrch()
	if err != nil {
		t.Fatalf("UnackedOrch: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("pr-merged rows = %v, want BOTH merges — the second one's URL exists nowhere else", contractKronosSummaries(items))
	}
	joined := strings.Join(contractKronosSummaries(items), "|")
	for _, url := range []string{"/pull/12", "/pull/13"} {
		if !strings.Contains(joined, url) {
			t.Errorf("the inbox does not carry %q: %v", url, contractKronosSummaries(items))
		}
	}

	// The other direction: a second job-blocked for the same slug is skipped
	// while the first is unacked.
	enqueueOrchNotice("job-blocked", "csv-export", "blocked — the first report")
	enqueueOrchNotice("job-blocked", "csv-export", "blocked — the second report")

	items, err = st.UnackedOrch()
	if err != nil {
		t.Fatalf("UnackedOrch: %v", err)
	}
	var blocked []string
	for _, it := range items {
		if it.Kind == "job-blocked" {
			blocked = append(blocked, it.Summary)
		}
	}
	if len(blocked) != 1 {
		t.Fatalf("job-blocked rows = %v, want exactly 1 — a kind+slug already unacked is already reported", blocked)
	}
	if !strings.Contains(blocked[0], "the first report") {
		t.Errorf("the surviving job-blocked row = %q, want the FIRST report (the dedup skips the later write, it does not replace the row)", blocked[0])
	}

	// The dedup is per SLUG, not per kind: another job blocking is its own
	// event and lands.
	enqueueOrchNotice("job-blocked", "another-job", "blocked — a different job")
	items, err = st.UnackedOrch()
	if err != nil {
		t.Fatalf("UnackedOrch: %v", err)
	}
	blocked = nil
	for _, it := range items {
		if it.Kind == "job-blocked" {
			blocked = append(blocked, it.Slug)
		}
	}
	if len(blocked) != 2 {
		t.Fatalf("job-blocked slugs = %v, want one per job — the dedup keys on kind AND slug", blocked)
	}
}
