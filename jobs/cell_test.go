package jobs

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
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
