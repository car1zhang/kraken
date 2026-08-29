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
		// kill returns a job to draft by clearing state ALONE. It used to
		// clear a `ready` flag beside it, because leaving that set handed the
		// job straight back to the daemon's queue loop on the next tick;
		// neither the flag nor the loop exists, and a "ready" key here would
		// now be an unknown column.
		{"kill", []string{"state", "", "teardown", ""}, true},
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
	group := func(matcher, cmd string) map[string]any {
		return map[string]any{
			"matcher": matcher,
			"hooks": []any{
				map[string]any{"type": "command", "command": cmd},
			},
		}
	}
	// the PostToolUse (PR attach) entry, matched on the tool name
	entry := func(cmd string) map[string]any { return group("Bash", cmd) }
	// the StopFailure (usage-limit) entry, matched on the payload's error
	// value rather than a tool name. Every render carries it beside the
	// PostToolUse one, so it appears in each non-error expectation below;
	// its absence would mean a cell that hits a usage limit reports the wall
	// to nobody.
	limitEntry := func(cmd string) map[string]any { return group("rate_limit", cmd) }
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
					"StopFailure": []any{limitEntry("kraken limit-hook foo")},
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
					"StopFailure": []any{limitEntry("kraken limit-hook foo")},
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
					"StopFailure": []any{limitEntry("kraken limit-hook foo")},
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
			// a no-op — no duplicate entry. Dedup is per entry: the existing
			// PostToolUse group is left exactly as it was while the missing
			// StopFailure one is still installed alongside it.
			name:     "existing entry with identical command: no-op, no duplicate",
			existing: `{"hooks": {"PostToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "kraken pr-hook foo"}]}]}}`,
			want: map[string]any{
				"hooks": map[string]any{
					"PostToolUse": []any{entry("kraken pr-hook foo")},
					"StopFailure": []any{limitEntry("kraken limit-hook foo")},
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
					"StopFailure": []any{limitEntry("kraken limit-hook foo")},
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
						map[string]any{"type": "command", "command": "kraken recall-hook foo"},
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
// every other test here reads recallHookEvents and so would silently follow a
// revert or a widening. Exactly ONE event: UserPromptSubmit, whose query is
// carl's new message — the only query in this feature that is evidence of what
// he is thinking about rather than a summary of what an agent just did.
//
// POSTTOOLUSE WAS WIRED AND IS NOT ANY MORE, and it came out on measurement.
// Its query was the assistant's own last text after every tool call, which
// makes it an echo machine independent of the corpus: the sibling
// memory-usefulness job classified 2,556 fact deliveries across 15 days and
// found 4 ever used, 68-79% of them echoing work the agent had just finished,
// every measured arrival 20s-6h late. Re-adding it means re-arguing that
// measurement.
//
// Stop was never wired and still must not be: Claude Code's hooks reference is
// explicit that additionalContext at Stop CONTINUES the turn, so a Stop-wired
// recall would resume every agent past zeus' own stop-gates, hold blocked cells
// at "working" in the roll call, and chain paid model calls until retrieval ran
// dry. Re-adding "Stop" must come with a deliberate decision about those three
// consequences, not ride in on a refactor.
func TestRecallHookEventsPinsTheWiring(t *testing.T) {
	want := []string{"UserPromptSubmit"}
	if !reflect.DeepEqual(recallHookEvents, want) {
		t.Fatalf("recallHookEvents = %v, want exactly %v — see this test's comment before changing it", recallHookEvents, want)
	}
	// The removal half. An event dropped from recallHookEvents without being
	// listed here stops being written and never stops FIRING: the stale group
	// sits in every settings.json already installed, running a hook nothing
	// intends, with no code left that mentions it.
	wantRetired := []string{"PostToolUse"}
	if !reflect.DeepEqual(retiredRecallHookEvents, wantRetired) {
		t.Fatalf("retiredRecallHookEvents = %v, want exactly %v — an unwired event must be listed here, not merely absent above", retiredRecallHookEvents, wantRetired)
	}
}

// TestMergeHookSettingsSurvivesARecallHookSlug pins the marker collision: a
// job slugged `recall-hook` yields the pr-hook command `kraken pr-hook
// recall-hook`, which CONTAINS withoutRecallHooks' " recall-hook" marker as
// its slug argument — and a recall pass still runs over PostToolUse, where
// that pr-hook group lives, now as the RETIREMENT strip rather than as the
// replace. Without the verb-position exemption the group was silently dropped
// on every install, and the cell's PRs degraded to the daemon's scan/poll
// attachment with nothing logged. Retiring PostToolUse did not retire that
// hazard — it renamed the pass that carries it.
func TestMergeHookSettingsSurvivesARecallHookSlug(t *testing.T) {
	out, err := mergeHookSettings(nil, "recall-hook", "kraken")
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatal(err)
	}
	count := func(event string) (prHook, recall int) {
		groups, _ := parsed["hooks"].(map[string]any)[event].([]any)
		for _, g := range groups {
			raw, _ := json.Marshal(g)
			// pr-hook FIRST: this slug's pr-hook command mentions both
			// markers, and which one it is depends on verb position.
			if strings.Contains(string(raw), " pr-hook") {
				prHook++
			} else if strings.Contains(string(raw), " recall-hook") {
				recall++
			}
		}
		return prHook, recall
	}
	if prHook, recall := count("PostToolUse"); prHook != 1 || recall != 0 {
		t.Fatalf("PostToolUse for slug recall-hook holds %d pr-hook and %d recall groups, want 1 and 0: %s", prHook, recall, out)
	}
	if _, recall := count("UserPromptSubmit"); recall != 1 {
		t.Fatalf("UserPromptSubmit for slug recall-hook holds %d recall groups, want 1: %s", recall, out)
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

// --- doneReorient ---

// TestDoneReorientPRClauseIsConditional pins that the done-state
// reactivation prompt only claims earlier PRs — and only orders a new PR —
// when the job's kraken-owned prs: record actually holds one, and that
// neither arm asserts more than the record supports: the no-PR arm reports
// an empty record (never a job class, never a PR prohibition — an
// implementation job can reach done with an empty record when the tracker
// degraded), and the with-PR arm no longer claims the PRs merged.
func TestDoneReorientPRClauseIsConditional(t *testing.T) {
	base := &Job{Slug: "x", State: "done"}
	noPRs := doneReorient(base, "carl/x-1", "freshly reset off origin/main")
	if strings.Contains(noPRs, "Your earlier PRs") || strings.Contains(noPRs, "new PR from the current branch") {
		t.Errorf("no-PR reorient claims PR history or orders a PR: %q", noPRs)
	}
	if !strings.Contains(noPRs, "No PRs are recorded on this job") {
		t.Errorf("no-PR reorient missing the record statement: %q", noPRs)
	}
	if strings.Contains(noPRs, "explanation job") || strings.Contains(noPRs, "do not open a PR") {
		t.Errorf("no-PR reorient infers a job class or forbids a PR: %q", noPRs)
	}
	withPRs := &Job{Slug: "x", State: "done", PRs: []JobPR{{URL: "https://github.com/o/r/pull/1", State: "merged"}}}
	got := doneReorient(withPRs, "carl/x-1", "freshly reset off origin/main")
	if !strings.Contains(got, "Your earlier PRs are recorded on the job") {
		t.Errorf("with-PR reorient missing the PR clause: %q", got)
	}
	if strings.Contains(got, "No PRs are recorded") || strings.Contains(got, "already merged") {
		t.Errorf("with-PR reorient carries the no-PR statement or the merged claim: %q", got)
	}
}

// --- recreateFromRemote ---

// TestRecreateFromRemoteMergedBranchFallsBack pins the merged-branch arm
// against a real git fixture: a remote branch whose every commit is already
// in base must NOT be recreated from (ok=false, so the caller recycles off
// base via ensureUsableBranch) and must never earn a rebase note — the
// is-ancestor drift test alone cannot tell merged from behind (post-merge,
// base is an ancestor of neither), and the note it produced told a
// reactivated zeus to rebase a branch whose PR had already merged, as the
// ENTIRE prompt when carl attached no steer. The behind and ahead-of-base
// cases pin that genuinely-drifted branches still take the remote path.
func TestRecreateFromRemoteMergedBranchFallsBack(t *testing.T) {
	gitEnv := append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	origin := t.TempDir()
	runIn := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = gitEnv
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	commitFile := func(dir, name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
		runIn(dir, "add", name)
		runIn(dir, "commit", "-q", "-m", name)
	}
	runIn(origin, "init", "-q", "-b", "main")
	commitFile(origin, "base.txt")
	runIn(origin, "checkout", "-q", "-b", "feature")
	commitFile(origin, "feature.txt")
	runIn(origin, "checkout", "-q", "main")

	repo := t.TempDir()
	runIn(repo, "init", "-q", "-b", "main")
	runIn(repo, "remote", "add", "origin", origin)
	runIn(repo, "fetch", "-q", "origin")
	runIn(repo, "checkout", "-q", "main")
	runIn(repo, "reset", "-q", "--hard", "origin/main")

	// feature holds a commit main lacks: usable remote, no rebase needed
	if needsRebase, ok := recreateFromRemote(repo, "feature", "origin/main"); !ok || needsRebase {
		t.Fatalf("unmerged up-to-date branch: needsRebase=%v ok=%v, want false/true", needsRebase, ok)
	}
	// main moves on without feature: genuinely behind, rebase earned
	commitFile(origin, "drift.txt")
	if needsRebase, ok := recreateFromRemote(repo, "feature", "origin/main"); !ok || !needsRebase {
		t.Fatalf("behind branch: needsRebase=%v ok=%v, want true/true", needsRebase, ok)
	}
	// feature merges into main: merged is not behind — fall back to the
	// recycle path, no rebase note
	runIn(origin, "merge", "-q", "--no-ff", "-m", "merge feature", "feature")
	if needsRebase, ok := recreateFromRemote(repo, "feature", "origin/main"); ok || needsRebase {
		t.Fatalf("merged branch: needsRebase=%v ok=%v, want false/false", needsRebase, ok)
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
// cellClaudeArgs call (fed by threadModel's own resolution) is legal, so a
// package walk would need a by-name skip
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
// cannot have this, since thread.go's cellClaudeArgs call — fed by
// threadModel's resolution rather than zeusModel's — IS legal.
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
		// The sanctioned shapes are zeusBrief(...) alone, or zeusBrief(...)
		// followed by any run of the sanctioned INJECTIONS — goalContext and
		// graphContext. Both are deliberately OUTSIDE zeusBrief (its two
		// pinning tests measure static policy text), so the concatenation is
		// part of the spawn contract this guard protects — the brief half must
		// still lead, and each addition is one whose ""-on-any-failure
		// guarantee is what keeps a briefless spawn impossible.
		injections := map[string]bool{"goalContext": true, "graphContext": true}
		arg := call.Args[2]
		for {
			bin, ok := arg.(*ast.BinaryExpr)
			if !ok || bin.Op != token.ADD {
				break
			}
			y, ok := bin.Y.(*ast.CallExpr)
			if !ok {
				t.Errorf("%s: spawnCommand's brief concatenates something other than goalContext(...)/graphContext(...)", fset.Position(call.Pos()))
				break
			}
			if fn, ok2 := y.Fun.(*ast.Ident); !ok2 || !injections[fn.Name] {
				t.Errorf("%s: spawnCommand's brief concatenates something other than goalContext(...)/graphContext(...)", fset.Position(call.Pos()))
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
	if calls != 3 {
		t.Fatalf("found %d spawnCommand calls in package jobs, want 3 (StartCell, ReactivateJob, the limit rescue's zeus resume) — a new zeus spawn path must stage the brief too, so cover it here rather than re-aiming this guard", calls)
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

// --- reactivation artifact restore ---

// TestReactivateJobRestoresArtifacts pins ReactivateJob's restore wiring at
// the source level (the function itself needs live git/tmux to run).
// ReactivateJob MkdirAll's the artifacts dir either way (restore runs just
// before that, against the dir's pre-reactivation state), and that dir is a
// true-mirror SOURCE at the next teardown's sync: a reactivation that
// stopped restoring would not only bring the cell back without its
// deliverables, it would leave an existing-but-empty dir that the next
// teardown reads as "every artifact was deleted" and mirrors into the
// store — wiping the history too.
func TestReactivateJobRestoresArtifacts(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "cell.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var reactivate *ast.FuncDecl
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "ReactivateJob" {
			reactivate = fn
		}
	}
	if reactivate == nil {
		t.Fatal("cell.go no longer declares ReactivateJob — re-aim this guard at wherever reactivation moved")
	}
	found := false
	ast.Inspect(reactivate.Body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if fun, ok := call.Fun.(*ast.Ident); ok && fun.Name == "restoreCellArtifacts" {
				found = true
			}
		}
		return true
	})
	if !found {
		t.Fatal("ReactivateJob no longer calls restoreCellArtifacts — a revived cell comes up without its artifacts, and the empty dir wipes the stored mirror at the next teardown")
	}
}

// --- sleepCutTeammates ---

// TestSleepCutTeammates pins the teammate half of the sleep rescue as a pure
// decision over a discovery snapshot and a pane capture: only depth-0 claude
// TEAMMATES of the cell — never zeus, never a side thread, never a pane
// mid-turn or holding a question prompt — and only when the pane's TAIL
// shows the sleep banner with no nudge typed in below it.
func TestSleepCutTeammates(t *testing.T) {
	j := &Job{Slug: "widget-fix"}
	sess := CellSession(j)
	banner := "some output\nAPI Error: Your computer went to sleep mid-response. The response above may be incomplete.\n> "
	panes := map[string]string{
		"%1": banner, // zeus — excluded by identity, not by content
		"%2": banner, // teammate at its prompt, cut: nudged
		"%3": banner, // teammate still running: never nudged mid-turn
		"%4": "all quiet\n> ",
		"%5": banner, // side thread by name
		"%6": banner, // another cell's session
		"%7": banner, // nested claude, not the pane owner
		"%8": banner, // teammate with a question prompt open
	}
	capture := func(pane string) string { return panes[pane] }
	// "waiting" is what a real tmux teammate carries at its prompt (discovery
	// leaves a teammate's status "unknown" and refinedStatus maps a spinnerless
	// pane to "waiting"); "idle" is what a session-file peer carries. Both
	// must pass the gate, which is a blacklist for that reason.
	all := []*core.Peer{
		{Name: ZeusName(j), Agent: "claude", Depth: 0, TmuxSession: sess, Pane: "%1", Status: "idle"},
		{Name: "hephaestus-worker", Agent: "claude", Depth: 0, TmuxSession: sess, Pane: "%3", Status: "running"},
		{Name: "athena-logic", Agent: "claude", Depth: 0, TmuxSession: sess, Pane: "%2", Status: "waiting"},
		{Name: "athena-edge", Agent: "claude", Depth: 0, TmuxSession: sess, Pane: "%4", Status: "idle"},
		{Name: ThreadAgentName(j, "1"), Agent: "claude", Depth: 0, TmuxSession: sess, Pane: "%5", Status: "idle"},
		{Name: "athena-style", Agent: "claude", Depth: 0, TmuxSession: "cell-other", Pane: "%6", Status: "idle"},
		{Name: "nested", Agent: "claude", Depth: 1, TmuxSession: sess, Pane: "%7", Status: "idle"},
		{Name: "hephaestus-fe", Agent: "claude", Depth: 0, TmuxSession: sess, Pane: "%8", Status: "question"},
	}
	names := func(ps []*core.Peer) []string {
		var out []string
		for _, p := range ps {
			out = append(out, p.Name+"@"+p.Pane)
		}
		return out
	}
	got := sleepCutTeammates(j, all, capture)
	if len(got) != 1 || got[0].Pane != "%2" {
		t.Fatalf("sleepCutTeammates = %v, want exactly athena-logic@%%2", names(got))
	}

	// Pane order, not discovery order: a stable log line per tick.
	all = append(all, &core.Peer{Name: "hephaestus-backend", Agent: "claude", Depth: 0, TmuxSession: sess, Pane: "%10", Status: "idle"})
	panes["%10"] = banner
	all = append([]*core.Peer{all[len(all)-1]}, all[:len(all)-1]...)
	got = sleepCutTeammates(j, all, capture)
	if len(got) != 2 || got[0].Pane != "%2" || got[1].Pane != "%10" {
		t.Fatalf("pane order: got %v, want [athena-logic@%%2 hephaestus-backend@%%10] — panes sort numerically, not lexically (%%10 < %%2 as strings)", names(got))
	}

	// No zeus at all (still starting): CellZeus' positional fallback elects
	// the lone claude AS zeus, so it is excluded by identity and NOT nudged —
	// the alternative types a teammate resume into zeus' own pane.
	noZeus := []*core.Peer{{Name: "athena-logic", Agent: "claude", Depth: 0, TmuxSession: sess, Pane: "%2", Status: "idle"}}
	if got := sleepCutTeammates(j, noZeus, capture); len(got) != 0 {
		t.Fatalf("with no named zeus the positional fallback elects the lone claude as zeus; it must not be nudged as a teammate, got %v", names(got))
	}
}

// TestPaneShowsUnhandledSleepCut pins the text half: the banner counts only
// in the last sleepCutTailLines, and only when no sentinel sits below its
// last occurrence — which is what makes a nudge self-limiting on a pane
// whose banner never scrolls away, and what bounds a pane that merely quotes
// the string to one spurious nudge.
func TestPaneShowsUnhandledSleepCut(t *testing.T) {
	if strings.Contains(sleepCutSentinel, sleepCutMarker) {
		t.Fatalf("sleepCutSentinel %q contains sleepCutMarker %q — every nudge would re-trigger itself on the next tick", sleepCutSentinel, sleepCutMarker)
	}
	if !strings.HasPrefix(sleepCutResumeMsg, sleepCutSentinel) {
		t.Fatal("sleepCutResumeMsg must open with sleepCutSentinel — the typed message is what marks a banner handled")
	}
	bannerLine := "API Error: Your computer went to sleep mid-response. The response above may be incomplete."
	deep := strings.Repeat("filler line\n", sleepCutTailLines+5)
	cases := []struct {
		name string
		pane string
		want bool
	}{
		{"banner at the tail", "output\n" + bannerLine + "\n> ", true},
		{"no banner", "output\n> ", false},
		{"banner scrolled above the tail window", bannerLine + "\n" + deep + "> ", false},
		{"banner quoted at the tail, then nudged", bannerLine + "\n> " + sleepCutSentinel + " — pick up\n> ", false},
		{"nudged, then cut AGAIN", "> " + sleepCutSentinel + " — pick up\nworking\n" + bannerLine + "\n> ", true},
		{"sentinel ABOVE the banner still counts as unhandled", sleepCutSentinel + "\n" + bannerLine + "\n> ", true},
	}
	for _, c := range cases {
		if got := paneShowsUnhandledSleepCut(c.pane); got != c.want {
			t.Errorf("%s: paneShowsUnhandledSleepCut = %v, want %v", c.name, got, c.want)
		}
	}
}
