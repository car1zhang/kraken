package core

import (
	"errors"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// sleep_test.go — the sleep hold (sleep.go). Every command is behind
// sleepRun and the caffeinate start behind sleepSpawn, so nothing here
// touches pmset, sudo or a real process.

// sleepPatternRE is caffeinatePattern as Go compiles it. pgrep reads the
// same text as an extended regular expression; the pattern uses only
// grouping, alternation and anchors, which the two dialects share.
var sleepPatternRE = regexp.MustCompile(caffeinatePattern)

// sleepReply is one scripted answer for a fake command.
type sleepReply struct {
	out  string
	code int
	err  error
}

// sleepFake swaps both seams for the test. Commands are answered by argv[0]
// (and, for sudo, by the value being written) from replies; every argv is
// recorded in order, and spawns counts caffeinate starts.
type sleepFake struct {
	calls   [][]string
	spawns  int
	replies map[string]sleepReply
}

func newSleepFake(t *testing.T, replies map[string]sleepReply) *sleepFake {
	t.Helper()
	f := &sleepFake{replies: replies}
	oldRun, oldSpawn := sleepRun, sleepSpawn
	t.Cleanup(func() { sleepRun, sleepSpawn = oldRun, oldSpawn })
	sleepRun = func(name string, args ...string) (string, int, error) {
		f.calls = append(f.calls, append([]string{name}, args...))
		r := f.replies[name]
		return r.out, r.code, r.err
	}
	sleepSpawn = func() error {
		f.spawns++
		return f.replies["spawn"].err
	}
	return f
}

func TestParseSleepDisabled(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want bool
	}{
		{"disabled", "System-wide power settings:\n SleepDisabled\t\t1\nCurrently in use:\n sleep                1\n", true},
		{"enabled", "System-wide power settings:\n SleepDisabled\t\t0\n", false},
		{"no row", "Currently in use:\n sleep                1\n displaysleep         2\n", false},
		{"empty", "", false},
		// the row is matched by its own name, not by any row mentioning sleep
		{"other sleep rows only", " disksleep            10\n sleep                1 (sleep prevented by caffeinate)\n", false},
		{"row with no value", " SleepDisabled\n", false},
	}
	for _, c := range cases {
		if got := parseSleepDisabled(c.out); got != c.want {
			t.Errorf("%s: parseSleepDisabled = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestSleepDisabledReadsPmset(t *testing.T) {
	f := newSleepFake(t, map[string]sleepReply{pmsetCLI: {out: " SleepDisabled\t\t1\n"}})
	got, err := SleepDisabled()
	if err != nil || !got {
		t.Fatalf("SleepDisabled = %v, %v; want true, nil", got, err)
	}
	if want := [][]string{{pmsetCLI, "-g"}}; !reflect.DeepEqual(f.calls, want) {
		t.Errorf("ran %v, want %v", f.calls, want)
	}
}

func TestSleepDisabledFailures(t *testing.T) {
	newSleepFake(t, map[string]sleepReply{pmsetCLI: {err: errors.New("no such file")}})
	if _, err := SleepDisabled(); err == nil {
		t.Error("an unrunnable pmset returned no error")
	}
	newSleepFake(t, map[string]sleepReply{pmsetCLI: {out: "boom", code: 2}})
	if _, err := SleepDisabled(); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("a failed pmset returned %v, want an error quoting its output", err)
	}
}

func TestSetSleepDisabledOnStartsCaffeinate(t *testing.T) {
	f := newSleepFake(t, map[string]sleepReply{pgrepCLI: {code: 1}})
	if err := SetSleepDisabled(true); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{sudoCLI, "-n", pmsetCLI, "-a", "disablesleep", "1"},
		{pgrepCLI, "-f", caffeinatePattern},
	}
	if !reflect.DeepEqual(f.calls, want) {
		t.Errorf("ran %v, want %v", f.calls, want)
	}
	if f.spawns != 1 {
		t.Errorf("caffeinate started %d times, want 1", f.spawns)
	}
}

func TestSetSleepDisabledOnKeepsARunningCaffeinate(t *testing.T) {
	f := newSleepFake(t, map[string]sleepReply{pgrepCLI: {out: "4242\n", code: 0}})
	if err := SetSleepDisabled(true); err != nil {
		t.Fatal(err)
	}
	if f.spawns != 0 {
		t.Errorf("caffeinate started %d times beside a running one, want 0", f.spawns)
	}
}

func TestSetSleepDisabledOffEndsCaffeinate(t *testing.T) {
	// pkill's 1 is "nothing matched", which is not a failure
	for _, code := range []int{0, 1} {
		f := newSleepFake(t, map[string]sleepReply{pkillCLI: {code: code}})
		if err := SetSleepDisabled(false); err != nil {
			t.Fatalf("pkill exit %d: %v", code, err)
		}
		want := [][]string{
			{sudoCLI, "-n", pmsetCLI, "-a", "disablesleep", "0"},
			{pkillCLI, "-f", caffeinatePattern},
		}
		if !reflect.DeepEqual(f.calls, want) {
			t.Errorf("pkill exit %d: ran %v, want %v", code, f.calls, want)
		}
		if f.spawns != 0 {
			t.Errorf("pkill exit %d: caffeinate started on the way off", code)
		}
	}
}

func TestSetSleepDisabledRefusedSudoChangesNothingElse(t *testing.T) {
	for _, disabled := range []bool{true, false} {
		f := newSleepFake(t, map[string]sleepReply{
			sudoCLI: {out: "sudo: a password is required\n", code: 1},
		})
		err := SetSleepDisabled(disabled)
		if err == nil {
			t.Fatalf("disabled=%v: a refused sudo returned no error", disabled)
		}
		// the refusal names the rule that would allow it
		if !strings.Contains(err.Error(), "NOPASSWD: "+pmsetCLI) {
			t.Errorf("disabled=%v: error %q does not name the sudoers rule", disabled, err)
		}
		if len(f.calls) != 1 || f.spawns != 0 {
			t.Errorf("disabled=%v: after a refused sudo ran %v, spawns %d; want the sudo call alone", disabled, f.calls, f.spawns)
		}
	}
}

func TestSetSleepDisabledSurfacesOtherFailures(t *testing.T) {
	// landed is the clause a failure carries when pmset had already been
	// written: the switch happened and only the caffeinate half did not, so
	// the error must not read as "nothing changed". A failure of the pmset
	// write itself carries neither clause.
	const (
		nowOff = "sleep is now disabled, but"
		nowOn  = "sleep is now enabled, but"
	)
	cases := []struct {
		name     string
		disabled bool
		replies  map[string]sleepReply
		wantIn   string
		landed   string
	}{
		{"pmset fails for another reason", true, map[string]sleepReply{sudoCLI: {out: "pmset: bad arg", code: 64}}, "bad arg", ""},
		{"sudo cannot run", true, map[string]sleepReply{sudoCLI: {err: errors.New("exec: not found")}}, "not found", ""},
		{"pgrep errors", true, map[string]sleepReply{pgrepCLI: {out: "bad pattern", code: 2}}, "bad pattern", nowOff},
		{"pgrep cannot run", true, map[string]sleepReply{pgrepCLI: {err: errors.New("pgrep timed out")}}, "timed out", nowOff},
		{"caffeinate will not start", true, map[string]sleepReply{pgrepCLI: {code: 1}, "spawn": {err: errors.New("fork failed")}}, "fork failed", nowOff},
		{"pkill errors", false, map[string]sleepReply{pkillCLI: {out: "internal", code: 3}}, "internal", nowOn},
		{"pkill cannot run", false, map[string]sleepReply{pkillCLI: {err: errors.New("pkill timed out")}}, "timed out", nowOn},
	}
	for _, c := range cases {
		newSleepFake(t, c.replies)
		err := SetSleepDisabled(c.disabled)
		if err == nil || !strings.Contains(err.Error(), c.wantIn) {
			t.Errorf("%s: err = %v, want one containing %q", c.name, err, c.wantIn)
			continue
		}
		said := strings.Contains(err.Error(), nowOff) || strings.Contains(err.Error(), nowOn)
		if c.landed == "" && said {
			t.Errorf("%s: err %q claims the switch landed when pmset was never written", c.name, err)
		}
		if c.landed != "" && !strings.Contains(err.Error(), c.landed) {
			t.Errorf("%s: err %q does not say %q", c.name, err, c.landed)
		}
	}
}

// The pattern is what keeps the off switch from killing a caffeinate this
// feature did not start, so its reach is pinned from both sides.
func TestCaffeinatePatternReach(t *testing.T) {
	match := func(cmdline string) bool {
		return sleepPatternRE.MatchString(cmdline)
	}
	for _, yes := range []string{"caffeinate -d -i -s -u", "/usr/bin/caffeinate -d -i -s -u"} {
		if !match(yes) {
			t.Errorf("pattern misses %q", yes)
		}
	}
	for _, no := range []string{
		"caffeinate -i ./run.sh",
		"caffeinate -i -t 300",
		"caffeinate -d -i -s -u -t 30",
		"decaffeinate -d -i -s -u",
		// a shell or a pkill carrying the same words as an argument
		"pkill -f caffeinate -d -i -s -u",
		"zsh -c pkill -f caffeinate -d -i -s -u; echo done",
	} {
		if match(no) {
			t.Errorf("pattern matches %q", no)
		}
	}
	// and it is the argv the spawn uses
	if got := "caffeinate " + strings.Join(caffeinateArgs, " "); !match(got) {
		t.Errorf("pattern does not match the spawned argv %q", got)
	}
}
