package jobs

import (
	"encoding/json"
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
		{"zeus process is gone — restart the cell (kill session + k start after cleanup)", false},
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
			want: "zeus process is gone — restart the cell (kill session + k start after cleanup)",
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
