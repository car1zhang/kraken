package jobs

import (
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"kraken/internal/core"
)

// cell_test.go — pure decision functions on the teardown/recovery paths:
// the self-teardown guard, the interrupted-teardown marker mapping, and the
// reactivate mode matrix. All disk/tmux/git effects stay behind them.

// --- guardSelfTeardown ---

func TestGuardSelfTeardown(t *testing.T) {
	cases := []struct {
		name    string
		target  string
		own     string
		refused bool
	}{
		{"outside tmux entirely", "cell-x", "", false},
		{"inside a different session", "cell-x", "0", false},
		{"inside another cell's session", "cell-x", "cell-y", false},
		// the status-investigation incident: a cell-resident process
		// confirming teardown of its own cell
		{"inside the target session", "cell-x", "cell-x", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := guardSelfTeardown(c.target, c.own)
			if refused := err != nil; refused != c.refused {
				t.Errorf("guardSelfTeardown(%q, %q) = %v, want refused=%v", c.target, c.own, err, c.refused)
			}
		})
	}
}

// --- teardownFinalFields ---

func TestTeardownFinalFields(t *testing.T) {
	cases := []struct {
		marker string
		want   []string
		ok     bool
	}{
		{"kill", []string{"state", "", "ready", "false", "teardown", ""}, true},
		{"done", []string{"state", "done", "teardown", ""}, true},
		{"done:", []string{"state", "done", "teardown", ""}, true},
		{"done:accepted", []string{"state", "done", "outcome", "accepted", "teardown", ""}, true},
		{"done:shipped the fix", []string{"state", "done", "outcome", "shipped the fix", "teardown", ""}, true},
		// markers this binary doesn't understand are left alone, not guessed at
		{"", nil, false},
		{"nonsense", nil, false},
	}
	for _, c := range cases {
		got, ok := teardownFinalFields(c.marker)
		if ok != c.ok {
			t.Errorf("teardownFinalFields(%q) ok = %v, want %v", c.marker, ok, c.ok)
			continue
		}
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("teardownFinalFields(%q) = %v, want %v", c.marker, got, c.want)
		}
	}
}

// --- mergeHookSettings ---

func TestMergeHookSettings(t *testing.T) {
	entry := func(cmd string) map[string]any {
		return map[string]any{
			"matcher": "Bash",
			"hooks": []any{
				map[string]any{"type": "command", "command": cmd},
			},
		}
	}
	cases := []struct {
		name     string
		existing string
		wantErr  bool
		want     map[string]any
	}{
		{
			name: "no existing file: fresh render",
			want: map[string]any{
				"hooks": map[string]any{
					"PostToolUse": []any{entry("kraken pr-hook foo")},
				},
			},
		},
		{
			name:     "existing file with unrelated keys: preserved",
			existing: `{"env": {"FOO": "bar"}}`,
			want: map[string]any{
				"env": map[string]any{"FOO": "bar"},
				"hooks": map[string]any{
					"PostToolUse": []any{entry("kraken pr-hook foo")},
				},
			},
		},
		{
			name:     "existing file with its own PostToolUse groups: kraken's appended, theirs intact",
			existing: `{"hooks": {"PostToolUse": [{"matcher": "Write", "hooks": [{"type": "command", "command": "echo hi"}]}]}}`,
			want: map[string]any{
				"hooks": map[string]any{
					"PostToolUse": []any{
						map[string]any{
							"matcher": "Write",
							"hooks": []any{
								map[string]any{"type": "command", "command": "echo hi"},
							},
						},
						entry("kraken pr-hook foo"),
					},
				},
			},
		},
		{
			// unparseable JSON must not be overwritten — the caller's contract
			// is to skip hook install with a warning, never clobber a repo's
			// own config
			name:     "unparseable existing: error, caller must skip",
			existing: "{not json",
			wantErr:  true,
		},
		{
			// a second installation path (e.g. reactivation recreate) merging
			// into settings that already carry kraken's exact command must be
			// a no-op — no duplicate entry
			name:     "existing entry with identical command: no-op, no duplicate",
			existing: `{"hooks": {"PostToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "kraken pr-hook foo"}]}]}}`,
			want: map[string]any{
				"hooks": map[string]any{
					"PostToolUse": []any{entry("kraken pr-hook foo")},
				},
			},
		},
		{
			// a differently-slugged or differently-pathed command must still
			// be appended alongside — dedup matches on the full command string
			name:     "existing entry with different command: still appended",
			existing: `{"hooks": {"PostToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "kraken pr-hook other-slug"}]}]}}`,
			want: map[string]any{
				"hooks": map[string]any{
					"PostToolUse": []any{
						entry("kraken pr-hook other-slug"),
						entry("kraken pr-hook foo"),
					},
				},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := mergeHookSettings([]byte(c.existing), "foo", "kraken")
			if (err != nil) != c.wantErr {
				t.Fatalf("mergeHookSettings(%q) err = %v, wantErr=%v", c.existing, err, c.wantErr)
			}
			if c.wantErr {
				return
			}
			// every render also carries the agent-teams switches the cell's
			// crew depends on; TestMergeTeammateSettings owns their semantics,
			// so fold them into each expectation here rather than restating
			// them in a table that is about hook merging
			wantEnv, ok := c.want["env"].(map[string]any)
			if !ok {
				wantEnv = map[string]any{}
			}
			wantEnv[teamsEnvVar] = "1"
			c.want["env"] = wantEnv
			c.want["teammateMode"] = "tmux"
			// every render likewise carries recall's matcher-less group under
			// each of recallHookEvents, APPENDED after whatever that event
			// already expects (PostToolUse holds the pr-hook group too, and
			// the recall merge runs last); folded in here for the same reason
			// as the switches above — this table is about pr-hook merging,
			// and restating the recall groups per case would bury what each
			// case actually varies
			wantHooks, ok := c.want["hooks"].(map[string]any)
			if !ok {
				wantHooks = map[string]any{}
			}
			for _, event := range recallHookEvents {
				existing, _ := wantHooks[event].([]any)
				wantHooks[event] = append(existing, map[string]any{
					"hooks": []any{
						map[string]any{"type": "command", "command": "kraken recall-hook"},
					},
				})
			}
			// and the PreToolUse bash-lint group, folded in for the same
			// reason — TestMergeHookSettingsInstallsBashLint owns its
			// semantics; this table is about pr-hook merging
			wantHooks["PreToolUse"] = []any{entry("kraken bash-lint")}
			c.want["hooks"] = wantHooks
			var gotParsed map[string]any
			if err := json.Unmarshal(got, &gotParsed); err != nil {
				t.Fatalf("mergeHookSettings(%q) produced invalid JSON: %v\n%s", c.existing, err, got)
			}
			if !reflect.DeepEqual(gotParsed, c.want) {
				t.Errorf("mergeHookSettings(%q) = %s, want %v", c.existing, got, c.want)
			}
		})
	}
}

// TestMergeTeammateSettings pins the switches that make zeus' brief true.
// The brief tells zeus that naming an Agent call buys a real tmux pane, a
// nestTeams row, and a SendMessage address — all properties of Claude Code's
// agent-teams feature, which is off unless these two settings are present. If
// the cell stops writing them, nothing fails loudly: the crew quietly becomes
// in-process subagents while the brief keeps describing panes that no longer
// exist, and carl loses the ability to watch or attach to any of the work.
// The preserve cases matter just as much — this file is merged into a repo's
// own checked-in settings, so a deliberate local choice must survive.
func TestMergeTeammateSettings(t *testing.T) {
	for _, c := range []struct {
		name    string
		parsed  map[string]any
		wantEnv map[string]any
		wantMod any
	}{
		{
			name:    "fresh render gets both switches",
			parsed:  map[string]any{},
			wantEnv: map[string]any{teamsEnvVar: "1"},
			wantMod: "tmux",
		},
		{
			name:    "existing env keys survive alongside",
			parsed:  map[string]any{"env": map[string]any{"FOO": "bar"}},
			wantEnv: map[string]any{"FOO": "bar", teamsEnvVar: "1"},
			wantMod: "tmux",
		},
		{
			// a repo that deliberately picked its own backend keeps it:
			// kraken states a requirement, it does not overrule a choice
			name:    "explicit teammateMode is not overruled",
			parsed:  map[string]any{"teammateMode": "in-process"},
			wantEnv: map[string]any{teamsEnvVar: "1"},
			wantMod: "in-process",
		},
		{
			name:    "explicit opt-out of the flag is not overruled",
			parsed:  map[string]any{"env": map[string]any{teamsEnvVar: "0"}},
			wantEnv: map[string]any{teamsEnvVar: "0"},
			wantMod: "tmux",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			mergeTeammateSettings(c.parsed)
			if !reflect.DeepEqual(c.parsed["env"], c.wantEnv) {
				t.Errorf("env = %v, want %v", c.parsed["env"], c.wantEnv)
			}
			if c.parsed["teammateMode"] != c.wantMod {
				t.Errorf("teammateMode = %v, want %v", c.parsed["teammateMode"], c.wantMod)
			}
		})
	}
}

// --- prHookCommand / E7 hook addr propagation ---

// TestPrHookCommandDefaultsMatchPreE7Shape confirms that with neither
// SetHookServeAddr nor KRAKEN_DB_PATH set (file-mode `k daemon`/`k web`,
// or a `kraken serve` process that never called SetHookServeAddr), the
// hook command is byte-identical to before E7 — no flags at all.
func TestPrHookCommandDefaultsMatchPreE7Shape(t *testing.T) {
	SetHookServeAddr("")
	t.Cleanup(func() { SetHookServeAddr("") })
	t.Setenv("KRAKEN_DB_PATH", "")

	if got, want := prHookCommand("foo", "kraken"), "kraken pr-hook foo"; got != want {
		t.Fatalf("prHookCommand = %q, want %q", got, want)
	}
}

// TestPrHookCommandCarriesNonDefaultServeAddr covers E7: a serve addr that
// differs from defaultServeAddr must be baked into the hook command as
// --serve-addr, since a pr-hook invocation is a fresh OS process that
// inherits none of kraken serve's own flags.
func TestPrHookCommandCarriesNonDefaultServeAddr(t *testing.T) {
	SetHookServeAddr("127.0.0.1:9999")
	t.Cleanup(func() { SetHookServeAddr("") })
	t.Setenv("KRAKEN_DB_PATH", "")

	got := prHookCommand("foo", "kraken")
	want := "kraken pr-hook --serve-addr 127.0.0.1:9999 foo"
	if got != want {
		t.Fatalf("prHookCommand = %q, want %q", got, want)
	}
}

// TestPrHookCommandOmitsDefaultServeAddr: an addr equal to defaultServeAddr
// must NOT be spelled out — matches the pre-E7 command shape exactly when
// serve happens to be bound at its own default.
func TestPrHookCommandOmitsDefaultServeAddr(t *testing.T) {
	SetHookServeAddr(defaultServeAddr)
	t.Cleanup(func() { SetHookServeAddr("") })
	t.Setenv("KRAKEN_DB_PATH", "")

	if got, want := prHookCommand("foo", "kraken"), "kraken pr-hook foo"; got != want {
		t.Fatalf("prHookCommand = %q, want %q", got, want)
	}
}

// TestPrHookCommandCarriesDBPathOverride covers E7's second propagated
// value: KRAKEN_DB_PATH set in kraken serve's own process environment
// (a non-default DB location) must ride along too.
func TestPrHookCommandCarriesDBPathOverride(t *testing.T) {
	SetHookServeAddr("")
	t.Cleanup(func() { SetHookServeAddr("") })
	t.Setenv("KRAKEN_DB_PATH", "/custom/kraken.db")

	got := prHookCommand("foo", "kraken")
	want := "kraken pr-hook --db-path /custom/kraken.db foo"
	if got != want {
		t.Fatalf("prHookCommand = %q, want %q", got, want)
	}
}

// TestMergeHookSettingsIdempotentWithNonDefaultAddr covers the actual bug
// E7 had to guard against: prHookCommand is shared by krakenHookEntry AND
// mergeHookSettings' hasHookCommand dedup check specifically so a
// non-default addr/db-path never causes the two to disagree and
// re-append a "new" hook entry on every single reinstall.
func TestMergeHookSettingsIdempotentWithNonDefaultAddr(t *testing.T) {
	SetHookServeAddr("127.0.0.1:9999")
	t.Cleanup(func() { SetHookServeAddr("") })
	t.Setenv("KRAKEN_DB_PATH", "/custom/kraken.db")

	first, err := mergeHookSettings(nil, "foo", "kraken")
	if err != nil {
		t.Fatal(err)
	}
	second, err := mergeHookSettings(first, "foo", "kraken")
	if err != nil {
		t.Fatal(err)
	}
	var firstParsed, secondParsed map[string]any
	if err := json.Unmarshal(first, &firstParsed); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(second, &secondParsed); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(firstParsed, secondParsed) {
		t.Fatalf("second merge duplicated the entry: first=%s second=%s", first, second)
	}
	// PostToolUse legitimately holds two groups now — pr-hook's beside
	// recall's — so the E7 invariant is one PR-HOOK command, counted by
	// command rather than asserted as the whole list.
	groups := secondParsed["hooks"].(map[string]any)["PostToolUse"].([]any)
	prHookGroups := 0
	for _, g := range groups {
		raw, _ := json.Marshal(g)
		if strings.Contains(string(raw), " pr-hook") {
			prHookGroups++
		}
	}
	if prHookGroups != 1 {
		t.Fatalf("want exactly 1 pr-hook group after re-merge, got %d: %v", prHookGroups, groups)
	}
	// the recall hook shares prHookCommand's addr/db-path flag treatment, so it
	// is exposed to the exact E7 bug this test exists for: entry-builder and
	// dedup check disagreeing under a non-default addr and re-appending forever.
	// Counted by COMMAND rather than by group, because PostToolUse legitimately
	// holds the pr-hook group beside recall's — the invariant is one
	// recall-hook command per event, not one group.
	for _, event := range recallHookEvents {
		groups := secondParsed["hooks"].(map[string]any)[event].([]any)
		recallGroups := 0
		for _, g := range groups {
			raw, _ := json.Marshal(g)
			if strings.Contains(string(raw), " recall-hook") {
				recallGroups++
			}
		}
		if recallGroups != 1 {
			t.Fatalf("want exactly 1 recall-hook group under %s after re-merge, got %d: %v", event, recallGroups, groups)
		}
	}
}

// TestRecallHookEventsPinsTheWiring pins the wiring literal itself, because
// every other test here reads recallHookEvents and so would silently follow
// a revert or a widening. Exactly two events: UserPromptSubmit (query = the
// new prompt) and PostToolUse (query = the assistant's last text, per tool
// call — safe because its additionalContext lands in a turn that is already
// continuing). Stop is deliberately NOT wired: Claude Code's hooks reference
// is explicit that additionalContext at Stop CONTINUES the turn, so a
// Stop-wired recall would resume every agent past zeus' own stop-gates, hold
// blocked cells at "working" in the roll call, and chain paid model calls
// until retrieval ran dry (review round 1, blocking finding). Re-adding
// "Stop" must come with a deliberate decision about those three
// consequences, not ride in on a refactor.
func TestRecallHookEventsPinsTheWiring(t *testing.T) {
	want := []string{"UserPromptSubmit", "PostToolUse"}
	if !reflect.DeepEqual(recallHookEvents, want) {
		t.Fatalf("recallHookEvents = %v, want exactly %v — see this test's comment before changing it", recallHookEvents, want)
	}
}

// TestMergeHookSettingsSurvivesARecallHookSlug pins the marker collision: a
// job slugged `recall-hook` yields the pr-hook command `kraken pr-hook
// recall-hook`, which CONTAINS withoutRecallHooks' " recall-hook" marker as
// its slug argument — and the recall replace pass runs over PostToolUse,
// where that pr-hook group lives. Without the pr-hook exemption the group was
// silently dropped on every install, and the cell's PRs degraded to the
// daemon's scan/poll attachment with nothing logged.
func TestMergeHookSettingsSurvivesARecallHookSlug(t *testing.T) {
	out, err := mergeHookSettings(nil, "recall-hook", "kraken")
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatal(err)
	}
	groups := parsed["hooks"].(map[string]any)["PostToolUse"].([]any)
	prHook, recall := 0, 0
	for _, g := range groups {
		raw, _ := json.Marshal(g)
		if strings.Contains(string(raw), " pr-hook") {
			prHook++
		} else if strings.Contains(string(raw), " recall-hook") {
			recall++
		}
	}
	if prHook != 1 || recall != 1 {
		t.Fatalf("PostToolUse for slug recall-hook holds %d pr-hook and %d recall groups, want 1 and 1: %s", prHook, recall, out)
	}
}

// --- reactivateMode ---

func TestReactivateMode(t *testing.T) {
	cases := []struct {
		name     string
		state    string
		worktree bool
		mode     string
		wantErr  bool
	}{
		{"done, clean slate", "done", false, "recreate", false},
		// a done job was torn down; a leftover worktree needs a human look
		{"done, leftover worktree", "done", true, "", true},
		// the WIP-preserving path: cell died mid-run, worktree intact
		{"working, cell gone, worktree intact", "working", true, "resume", false},
		{"blocked, cell gone, worktree intact", "blocked", true, "resume", false},
		{"working, cell gone, worktree lost", "working", false, "recreate", false},
		{"never started", "", false, "", true},
		{"never started with stray worktree", "", true, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mode, err := reactivateMode(c.state, c.worktree)
			if (err != nil) != c.wantErr {
				t.Fatalf("reactivateMode(%q, %v) err = %v, wantErr=%v", c.state, c.worktree, err, c.wantErr)
			}
			if mode != c.mode {
				t.Errorf("reactivateMode(%q, %v) = %q, want %q", c.state, c.worktree, mode, c.mode)
			}
		})
	}
}

// --- RetryableHealth ---

func TestRetryableHealth(t *testing.T) {
	cases := []struct {
		reason string
		want   bool
	}{
		{"zeus hit an API error: overloaded_error", true},
		{"zeus needs attention: credit balance is too low — fix and retry by hand", false},
		{"zeus process is gone — restart the cell (kill the session, then start it again from the board after cleanup)", false},
		{"", false},
	}
	for _, c := range cases {
		if got := RetryableHealth(c.reason); got != c.want {
			t.Errorf("RetryableHealth(%q) = %v, want %v", c.reason, got, c.want)
		}
	}
}

// --- stallReason / RetryableHealth round trip ---
//
// stallReason (CellHealth's transcript-classification switch, pulled out
// for testing) and RetryableHealth (a prefix match over stallReason's own
// output) are coupled by exact wording — this pins that coupling so a
// rewording of one without the other silently breaks auto-retry
// classification instead of loudly failing a test.

func TestStallReasonRetryableHealthRoundTrip(t *testing.T) {
	cases := []struct {
		name          string
		st            core.Stall
		wantRetryable bool
		wantEmpty     bool
	}{
		{"transient api error is retryable", core.Stall{Kind: "api-error", Text: "overloaded_error", Transient: true}, true, false},
		{"non-transient api error is not retryable", core.Stall{Kind: "api-error", Text: "credit balance is too low", Transient: false}, false, false},
		{"interrupted turn yields no reason", core.Stall{Kind: "interrupted"}, false, true},
		{"no stall yields no reason", core.Stall{Kind: ""}, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reason := stallReason(c.st)
			if empty := reason == ""; empty != c.wantEmpty {
				t.Fatalf("stallReason(%+v) = %q, want empty=%v", c.st, reason, c.wantEmpty)
			}
			if got := RetryableHealth(reason); got != c.wantRetryable {
				t.Errorf("RetryableHealth(stallReason(%+v)) = %v, want %v", c.st, got, c.wantRetryable)
			}
		})
	}
}

// --- CellHealth ---
//
// Only the branches reachable without touching disk or tmux are exercised
// here: TranscriptPathForSession resolves paths under the real ~/.claude —
// a hermetic test can't fake that without touching the home dir, so the
// LastStall-driven classification (api-error transient/non-transient) isn't
// covered by this table. The zeus==nil, startup-prompt, and guessed/empty
// session id branches are all pure and covered below.

func TestCellHealth(t *testing.T) {
	j := &Job{Slug: "widget-fix"}
	sess := CellSession(j)
	zeusName := ZeusName(j)

	cases := []struct {
		name string
		zeus *core.Peer // nil = no zeus in `all` at all
		want string
	}{
		{
			name: "zeus process gone",
			zeus: nil,
			want: "zeus process is gone — restart the cell (kill the session, then start it again from the board after cleanup)",
		},
		{
			name: "startup prompt needs a human",
			zeus: &core.Peer{Name: "claude", Depth: 0, TmuxSession: sess, Agent: "claude", Status: "question"},
			want: "cell agent is stuck on a startup prompt — k o " + sess + " to answer it",
		},
		{
			name: "still starting, no prompt — patience, not a flag",
			zeus: &core.Peer{Name: "claude", Depth: 0, TmuxSession: sess, Agent: "claude", Status: "working"},
			want: "",
		},
		{
			name: "named zeus with a guessed session id — fail quiet, never read a guessed transcript",
			zeus: &core.Peer{Name: zeusName, Depth: 0, TmuxSession: sess, SessionID: "abc123", SessionIDGuessed: true},
			want: "",
		},
		{
			name: "named zeus with no session id yet",
			zeus: &core.Peer{Name: zeusName, Depth: 0, TmuxSession: sess, SessionID: ""},
			want: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var all []*core.Peer
			if c.zeus != nil {
				all = append(all, c.zeus)
			}
			if got := CellHealth(j, all); got != c.want {
				t.Errorf("CellHealth() = %q, want %q", got, c.want)
			}
		})
	}
}

// --- reactivatePrompt ---

func TestReactivatePrompt(t *testing.T) {
	const reorient = "re-orient text"
	const rebase = "rebase instruction"
	cases := []struct {
		name       string
		message    string
		rebaseNote string
		wantPrompt string
		wantHas    bool
	}{
		{"blank message, no rebase", "", "", "", false},
		{"blank message, rebase needed", "", rebase, rebase, true},
		{"message, no rebase", "steer me", "", reorient + "\nsteering from carl: steer me", true},
		{"message, rebase needed", "steer me", rebase, rebase + "\n" + reorient + "\nsteering from carl: steer me", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			prompt, hasPrompt := reactivatePrompt(reorient, c.message, c.rebaseNote)
			if hasPrompt != c.wantHas {
				t.Fatalf("reactivatePrompt(%q, %q) hasPrompt = %v, want %v", c.message, c.rebaseNote, hasPrompt, c.wantHas)
			}
			if prompt != c.wantPrompt {
				t.Errorf("reactivatePrompt(%q, %q) = %q, want %q", c.message, c.rebaseNote, prompt, c.wantPrompt)
			}
		})
	}
}

// --- AllArtifactPaths ---

// TestAllArtifactPathsNoReservedNames covers the flip side of the retired
// questions.yaml special-casing: questions are rows in kraken.db now, not a
// file, so there is no reserved pluto name for AllArtifactPaths to exclude
// any more — a preserved copy literally named <slug>.questions.yaml is an
// ordinary deliverable like any other.
func TestAllArtifactPathsNoReservedNames(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	plutoDir := t.TempDir()
	t.Setenv("KRAKEN_PLUTO_DIR", plutoDir)

	j := &Job{Slug: "artifact-paths-job", ID: 8}
	realArtifact := filepath.Join(plutoDir, "artifact-paths-job-8.figures-a.svg")
	namedLikeQuestions := filepath.Join(plutoDir, "artifact-paths-job-8.questions.yaml")
	if err := os.WriteFile(realArtifact, []byte("svg"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(namedLikeQuestions, []byte("real deliverable"), 0o644); err != nil {
		t.Fatal(err)
	}

	paths := AllArtifactPaths(j)
	wantPresent := map[string]bool{realArtifact: false, namedLikeQuestions: false}
	for _, p := range paths {
		if _, ok := wantPresent[p]; ok {
			wantPresent[p] = true
		}
	}
	for want, found := range wantPresent {
		if !found {
			t.Fatalf("want %q to appear in AllArtifactPaths, got %+v", want, paths)
		}
	}
}

// --- the zeus model seam ---

// TestZeusSpawnPathsResolveTheModel holds the WIRING of the zeus/everything-else
// model split, which no value-level test can reach. zeusModel and both model
// constants are pinned by their own tests, but the line CONNECTING them was
// not: reverting cell.go's two spawn sites to `cellClaudeArgs(j.Model)` leaves
// zeusSpawnArgv referenced by nothing but its own test — dead code, since an
// unreferenced func is not a compile error — while every cell's zeus silently
// spawns on core.DefaultModel again. Measured BEFORE this test existed: with
// both call sites reverted and nothing else changed, `go test ./internal/jobs/
// ./internal/core/` passed. This is the test that makes that revert fail, so
// the measurement describes the suite as it stood without it.
//
// So the invariant is stated where it can be checked — over cell.go's own
// syntax, from this file. It is FILE-scoped where its sibling below is
// package-scoped, and that asymmetry is deliberate: thread.go's
// cellClaudeArgs(j.Model) is legal, so a package walk would need a by-name skip
// for it, and a by-name skip is the mechanism that would exempt the very file a
// future zeus spawn got added to. cell.go is where zeus is spawned and NOWHERE
// else, so a raw j.Model reaching cellClaudeArgs HERE is by definition a zeus
// spawn that skipped the Fable fallback.
func TestZeusSpawnPathsResolveTheModel(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "cell.go", nil, 0)
	if err != nil {
		t.Fatalf("parse cell.go: %v", err)
	}
	calls := 0
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		fn, ok := call.Fun.(*ast.Ident)
		if !ok || fn.Name != "cellClaudeArgs" || len(call.Args) != 1 {
			return true
		}
		calls++
		// the one legal shape in this file: cellClaudeArgs(zeusModel(j))
		arg, ok := call.Args[0].(*ast.CallExpr)
		if !ok {
			t.Errorf("%s: cellClaudeArgs called with something other than zeusModel(...) — zeus must resolve through zeusModel; if you introduced a local for it, inline it or re-aim this test",
				fset.Position(call.Pos()))
			return true
		}
		if inner, ok := arg.Fun.(*ast.Ident); !ok || inner.Name != "zeusModel" {
			t.Errorf("%s: cellClaudeArgs argument is not zeusModel(...) — zeus would skip core.ZeusModel",
				fset.Position(call.Pos()))
		}
		return true
	})
	// exactly one, inside zeusSpawnArgv: zero means the guard no longer guards
	// anything, and a second means a direct call that bypasses the seam —
	// legal to the per-call assertion above, yet defeating the seam's purpose
	if calls != 1 {
		t.Fatalf("found %d cellClaudeArgs calls in cell.go, want 1 (inside zeusSpawnArgv) — zeus' argv must be built in one place, so route a new caller through the seam rather than re-aiming this guard", calls)
	}
}

// TestZeusSpawnPathsStageTheBrief is the sibling guard, for the same reason —
// measured the same way, and likewise before it existed:
// reverting ReactivateJob's brief argument back to "" changed nothing the suite
// could see. It is the wider of the two, because the brief is not one flag: it
// carries the crew's `model: opus`, the cell's resolved paths, the
// skill-per-phase pointers, the guardrails, and the never-prompt rule. A
// resumed session does not retain an earlier run's --append-system-prompt
// (thread.go measured that), so a "" here hands back a plain claude holding
// zeus' history, whose teammates can then inherit zeus' own tier — and a system
// prompt that was never delivered shows up neither on the board nor in the
// roll-call, nowhere short of reading the pane's own command line for a missing
// --append-system-prompt.
//
// Unlike its sibling this one walks the WHOLE package, not just cell.go, and
// the asymmetry is the point: spawnCommand is zeus-only (a thread reaches
// stagedCommand directly), so there is no legal briefless caller to exempt, and
// a package walk needs no by-name skip — which is the mechanism that would
// otherwise exempt the very file a future spawn got added to. The model guard
// cannot have this, since thread.go's cellClaudeArgs(j.Model) IS legal.
func TestZeusSpawnPathsStageTheBrief(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}
	calls := 0
	inspect := func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		fn, ok := call.Fun.(*ast.Ident)
		if !ok || fn.Name != "spawnCommand" || len(call.Args) != 4 {
			return true
		}
		calls++
		// The sanctioned shapes are zeusBrief(...) alone or exactly
		// zeusBrief(...) + graphContext(...): the graph injection is
		// deliberately OUTSIDE zeusBrief (its two pinning tests measure
		// static policy text), so the concatenation is part of the spawn
		// contract this guard protects — the brief half must still lead,
		// and the only sanctioned addition is graphContext, whose ""-on-
		// any-failure guarantee is what keeps a briefless spawn impossible.
		arg := call.Args[2]
		if bin, ok := arg.(*ast.BinaryExpr); ok && bin.Op == token.ADD {
			y, ok := bin.Y.(*ast.CallExpr)
			if fn, ok2 := y.Fun.(*ast.Ident); !ok || !ok2 || fn.Name != "graphContext" {
				t.Errorf("%s: spawnCommand's brief concatenates something other than graphContext(...)", fset.Position(call.Pos()))
			}
			arg = bin.X
		}
		brief, ok := arg.(*ast.CallExpr)
		if !ok {
			t.Errorf("%s: spawnCommand's brief argument is not zeusBrief(j) — a briefless zeus can leave its whole crew inheriting zeus' own tier instead of Opus",
				fset.Position(call.Pos()))
			return true
		}
		if inner, ok := brief.Fun.(*ast.Ident); !ok || inner.Name != "zeusBrief" {
			t.Errorf("%s: spawnCommand's brief argument is not zeusBrief(...)", fset.Position(call.Pos()))
		}
		return true
	}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			ast.Inspect(file, inspect)
		}
	}
	// package-wide, so this is also the tripwire for a third zeus spawn path
	// appearing anywhere in the package rather than only in cell.go
	if calls != 2 {
		t.Fatalf("found %d spawnCommand calls in package jobs, want 2 (StartCell, ReactivateJob) — a new zeus spawn path must stage the brief too, so cover it here rather than re-aiming this guard", calls)
	}
}

// --- spawnCommand ---

// TestSpawnCommandStaysShortRegardlessOfInputSize is the regression test for
// the outage that motivated staging spawn inputs in files at all.
//
// zeus' brief and the job spec used to ride inline on the `tmux new-session`
// command line. tmux packs a command into a fixed ~16KB buffer and rejects
// anything larger with a bare `command too long`; once the brief crossed that
// line, EVERY cell spawn failed, and because the failure landed after `git
// worktree add`, each attempt left a worktree behind that made the next tick
// fail with a misleading "worktree already exists" instead. The invariant that
// prevents a recurrence is not "keep the brief small" — it is that the command
// length must not depend on the inputs' length at all.
func TestSpawnCommandStaysShortRegardlessOfInputSize(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	j := &Job{Slug: "spawn-size"}
	if err := os.MkdirAll(cellDir(j), 0o755); err != nil {
		t.Fatal(err)
	}
	brief := strings.Repeat("b", 1<<20)
	prompt := strings.Repeat("p", 1<<20)

	cmd, err := spawnCommand(j, []string{"claude", "--rc"}, brief, prompt)
	if err != nil {
		t.Fatalf("spawnCommand: %v", err)
	}
	// tmux's real ceiling is ~16340 bytes for this argv shape; 4096 leaves
	// room for a long cell path without ever approaching it
	if len(cmd) > 4096 {
		t.Errorf("spawn command is %d bytes for 1MB of input — the inputs are being inlined again, "+
			"which is what tmux rejects with `command too long`; stage them with core.ShellReadFileArg", len(cmd))
	}
	for _, want := range []string{"--append-system-prompt", zeusBriefPath(j), spawnPromptPath(j), "$(cat "} {
		if !strings.Contains(cmd, want) {
			t.Errorf("spawn command %q missing %q", cmd, want)
		}
	}
	// `--` must stay last: everything after it is claude's initial prompt
	if i, j2 := strings.Index(cmd, " -- "), strings.Index(cmd, "--append-system-prompt"); i < j2 {
		t.Errorf("spawn command puts -- before --append-system-prompt: %q", cmd)
	}
	for _, c := range []struct{ path, want string }{
		{zeusBriefPath(j), brief},
		{spawnPromptPath(j), prompt},
	} {
		got, err := os.ReadFile(c.path)
		if err != nil {
			t.Fatalf("read staged input %s: %v", c.path, err)
		}
		if string(got) != c.want {
			t.Errorf("staged %s is %d bytes, want %d — the input was truncated or mangled", c.path, len(got), len(c.want))
		}
	}
}

// TestSpawnCommandOmitsEmptyInputs covers the reactivate path, which resumes a
// session that already carries its system prompt (`-r`) and may have no
// steering message either: an empty input must produce no flag at all, not an
// empty file and a `$(cat …)` that expands to nothing — `claude --` with a
// blank prompt is not the same as `claude` with none.
func TestSpawnCommandOmitsEmptyInputs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	j := &Job{Slug: "spawn-empty"}
	if err := os.MkdirAll(cellDir(j), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd, err := spawnCommand(j, []string{"claude", "-r", "sess"}, "", "")
	if err != nil {
		t.Fatalf("spawnCommand: %v", err)
	}
	if cmd != "claude -r sess" {
		t.Errorf("spawnCommand with no brief or prompt = %q, want %q", cmd, "claude -r sess")
	}
	for _, path := range []string{zeusBriefPath(j), spawnPromptPath(j)} {
		if _, err := os.Stat(path); err == nil {
			t.Errorf("spawnCommand wrote %s for an empty input", path)
		}
	}
}

// TestSpawnCommandReportsWriteFailure pins that a failed stage is an error
// rather than a silently-launched agent with no system prompt: StartCell turns
// this into a "worktree left at …" message the same way a tmux failure does.
func TestSpawnCommandReportsWriteFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	j := &Job{Slug: "spawn-unwritable"}
	// no MkdirAll: the cell dir does not exist, so the write must fail
	if _, err := spawnCommand(j, []string{"claude"}, "brief", ""); err == nil {
		t.Fatal("spawnCommand succeeded with an unwritable cell dir, want an error")
	}
}

// --- cellSpawnEnv ---

// TestCellSpawnEnvPutsTheToolchainOnPath is the regression test for the
// papercut the dream loop surfaced: agents inside a cell ran a bare `go test` /
// `gofmt`, got "command not found", and each independently rediscovered the
// same `export PATH=$PATH:/usr/local/go/bin` workaround — one of them closed a
// style review as "tooling-unverified" because it never recovered.
//
// A pane's PATH is the PATH of the process that issued its tmux command, which
// is why this is the environment of `tmux new-session` rather than a
// `new-session -e PATH=…` (set in the session environment, then overridden —
// it silently does nothing) or a prefix on the launch command (which would
// reach zeus and none of its crew).
func TestCellSpawnEnvPutsTheToolchainOnPath(t *testing.T) {
	stubLookPath(t, func(name string) (string, error) {
		if name != "go" {
			return "", errors.New("a cell spawn resolves only go")
		}
		return "/usr/local/go/bin/go", nil
	})
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("KRAKEN_TEST_MARKER", "kept")

	var path string
	marker := false
	for _, kv := range cellSpawnEnv() {
		if strings.HasPrefix(kv, "PATH=") {
			path = strings.TrimPrefix(kv, "PATH=")
		}
		if kv == "KRAKEN_TEST_MARKER=kept" {
			marker = true
		}
	}
	if path != "/usr/local/go/bin:/usr/bin:/bin" {
		t.Errorf("cell spawn PATH = %q, want the go dir prepended to the inherited one", path)
	}
	if !marker {
		// A cell spawn inherits everything else: HOME, LANG, and whatever the
		// daemon was given, all of which claude itself reads.
		t.Error("cellSpawnEnv dropped the rest of the environment")
	}
}

// TestCellSpawnEnvIsBestEffort: a toolchain that cannot be found anywhere must
// not take the cell down with it. nil is exec.Cmd's "inherit", so the spawn is
// exactly what it was before this existed — a cell that refused to start
// because a PATH probe failed would be far worse than the papercut above.
func TestCellSpawnEnvIsBestEffort(t *testing.T) {
	stubLookPath(t, func(string) (string, error) { return "", exec.ErrNotFound })
	t.Setenv("HOME", t.TempDir()) // so the ~/go/bin fallback finds nothing either
	prev := toolDirs
	toolDirs = []string{filepath.Join(t.TempDir(), "nowhere")}
	t.Cleanup(func() { toolDirs = prev })

	if got := cellSpawnEnv(); got != nil {
		t.Fatalf("cellSpawnEnv() = %v with no resolvable go, want nil so the spawn is unchanged", got)
	}
}
