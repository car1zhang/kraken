package jobs

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestWakeupGuardStdin pins the guard's whole decision surface: an arm is
// blocked, a well-formed stop is allowed (blocking stop would strand a cell
// inside the loop the hook exists to prevent), a foreign or unparseable
// payload allows — a hook that fails someone else's tool call by accident is
// a bug, not a safety net — and a ScheduleWakeup call whose input cannot
// prove it is a stop counts as an arm, so a mistyped, non-object or absent
// tool_input blocks rather than disarming the guard.
func TestWakeupGuardStdin(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		block bool
	}{
		{"arm blocks", `{"tool_name":"ScheduleWakeup","tool_input":{"delaySeconds":1200,"noop":true,"prompt":"<<autonomous-loop-dynamic>>"}}`, true},
		{"re-arm with reason blocks", `{"tool_name":"ScheduleWakeup","tool_input":{"delaySeconds":3600,"reason":"fallback heartbeat"}}`, true},
		{"stop allows", `{"tool_name":"ScheduleWakeup","tool_input":{"stop":true}}`, false},
		{"stop plus other fields allows", `{"tool_name":"ScheduleWakeup","tool_input":{"stop":true,"delaySeconds":600}}`, false},
		{"mistyped stop blocks", `{"tool_name":"ScheduleWakeup","tool_input":{"stop":"true"}}`, true},
		{"case-variant stop key blocks", `{"tool_name":"ScheduleWakeup","tool_input":{"Stop":true,"delaySeconds":600}}`, true},
		{"non-object input blocks", `{"tool_name":"ScheduleWakeup","tool_input":"weird"}`, true},
		{"null input blocks", `{"tool_name":"ScheduleWakeup","tool_input":null}`, true},
		{"absent input blocks", `{"tool_name":"ScheduleWakeup"}`, true},
		{"other tool allows", `{"tool_name":"Bash","tool_input":{"command":"ls"}}`, false},
		{"malformed allows", `{not json`, false},
		{"empty allows", ``, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			msg, block := WakeupGuardStdin(strings.NewReader(c.in))
			if block != c.block {
				t.Fatalf("block = %v, want %v (msg %q)", block, c.block, msg)
			}
			if block && !strings.Contains(msg, "stop: true") {
				t.Fatalf("block message must name the allowed stop call, got %q", msg)
			}
			if !block && msg != "" {
				t.Fatalf("allow must carry no message, got %q", msg)
			}
		})
	}
}

// TestMergeHookSettingsInstallsWakeupGuard holds that a cell's settings get
// the PreToolUse ScheduleWakeup guard beside bash-lint, idempotently — the
// same contract TestMergeHookSettingsInstallsBashLint pins for its neighbour.
func TestMergeHookSettingsInstallsWakeupGuard(t *testing.T) {
	out, err := mergeHookSettings(nil, "demo", "/usr/local/bin/kraken")
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatal(err)
	}
	hooks := parsed["hooks"].(map[string]any)
	pre, ok := hooks["PreToolUse"].([]any)
	if !ok || !hasHookCommand(pre, "/usr/local/bin/kraken wakeup-guard") {
		t.Fatalf("PreToolUse wakeup-guard entry missing: %s", out)
	}
	// The matcher must be the tool name, or the hook fires on every tool.
	found := false
	for _, g := range pre {
		m, _ := g.(map[string]any)
		if m["matcher"] == "ScheduleWakeup" {
			found = true
		}
	}
	if !found {
		t.Fatalf("wakeup-guard group must match on ScheduleWakeup: %s", out)
	}
	again, err := mergeHookSettings(out, "demo", "/usr/local/bin/kraken")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(again), "wakeup-guard") != strings.Count(string(out), "wakeup-guard") {
		t.Fatalf("second merge duplicated the wakeup-guard entry:\n%s", again)
	}
}
