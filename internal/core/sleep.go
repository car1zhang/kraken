package core

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// sleep.go — the Mac's sleep hold, the board's form of a `togglesleep` shell
// function: `pmset -a disablesleep 1` plus a `caffeinate -d -i -s -u` beside
// it, and the reverse to let the machine sleep again. The state is whatever
// pmset reports, never something kraken remembers, so a toggle made in a
// terminal and one made on the board read and undo each other.
//
// pmset's write needs root. It goes through `sudo -n`, which never prompts:
// a machine whose sudoers has no passwordless rule for /usr/bin/pmset gets a
// refusal naming the rule, not a daemon waiting on a password nobody can
// type.

const (
	pmsetCLI      = "/usr/bin/pmset"
	sudoCLI       = "/usr/bin/sudo"
	caffeinateCLI = "/usr/bin/caffeinate"
	pgrepCLI      = "/usr/bin/pgrep"
	pkillCLI      = "/usr/bin/pkill"
)

// caffeinateArgs are the flags togglesleep starts its caffeinate with:
// display (-d), idle (-i) and system-on-AC (-s) assertions held for the
// process' life, and -u, a user-active assertion caffeinate takes for only
// 5 seconds when no -t is given. Kept identical to the shell function so
// either side's caffeinate matches caffeinatePattern.
var caffeinateArgs = []string{"-d", "-i", "-s", "-u"}

// caffeinatePattern matches a caffeinate started with exactly caffeinateArgs,
// by a bare name or a path, and nothing else: pgrep/pkill -f match the whole
// argument list, and other caffeinates run on this machine with other flags
// (`caffeinate -i <script>`) that a looser pattern would kill.
const caffeinatePattern = `(^|/)caffeinate -d -i -s -u$`

// sleepCmdTimeout bounds each command. None of them waits on input, so a
// run that long is a hung tool, not a slow one.
const sleepCmdTimeout = 10 * time.Second

// sleepRun runs one command to completion and returns its combined output
// and exit code. err is set only when the command could not be run or timed
// out; a non-zero exit is an answer (pgrep's 1 means "no such process").
// Swapped in tests, which never exec.
var sleepRun = func(name string, args ...string) (string, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), sleepCmdTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if ctx.Err() != nil {
		return string(out), -1, fmt.Errorf("%s timed out after %s", name, sleepCmdTimeout)
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return string(out), exit.ExitCode(), nil
	}
	if err != nil {
		return string(out), -1, err
	}
	return string(out), 0, nil
}

// sleepSpawn starts the caffeinate and leaves it running. Setsid puts it in
// a session of its own, the detachment spawnUpdater (internal/api/update.go)
// uses for a child that has to outlive a restart of this process. Swapped in
// tests.
var sleepSpawn = func() error {
	cmd := exec.Command(caffeinateCLI, caffeinateArgs...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	// still this process' child, so reap it whenever it ends
	go func() { _ = cmd.Wait() }()
	return nil
}

// parseSleepDisabled reads the SleepDisabled row out of `pmset -g`. A report
// with no such row reads as not disabled.
func parseSleepDisabled(out string) bool {
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "SleepDisabled" {
			return f[1] == "1"
		}
	}
	return false
}

// SleepDisabled reports whether this Mac's sleep is disabled.
func SleepDisabled() (bool, error) {
	out, code, err := sleepRun(pmsetCLI, "-g")
	if err != nil {
		return false, err
	}
	if code != 0 {
		return false, fmt.Errorf("pmset -g exited %d: %s", code, strings.TrimSpace(out))
	}
	return parseSleepDisabled(out), nil
}

// SetSleepDisabled disables or re-enables sleep. pmset goes first and the
// caffeinate follows only when it succeeded, so a refused sudo changes
// nothing. Disabling starts a caffeinate unless a matching one already runs;
// enabling ends every matching one. An error that follows a pmset write
// which landed says which state the machine is now in.
func SetSleepDisabled(disabled bool) error {
	val := "0"
	if disabled {
		val = "1"
	}
	out, code, err := sleepRun(sudoCLI, "-n", pmsetCLI, "-a", "disablesleep", val)
	if err != nil {
		return err
	}
	if code != 0 {
		msg := strings.TrimSpace(out)
		if strings.Contains(msg, "password is required") {
			return fmt.Errorf("pmset needs passwordless sudo — add `<your user> ALL=(ALL) NOPASSWD: %s` with `sudo visudo` (sudo: %s)", pmsetCLI, msg)
		}
		return fmt.Errorf("pmset disablesleep %s exited %d: %s", val, code, msg)
	}
	// From here the pmset write has landed, so a failure below is a half
	// switch and has to say so: on its own, "pgrep exited 3" reads as
	// nothing having changed.
	if err := syncCaffeinate(disabled); err != nil {
		if disabled {
			return fmt.Errorf("sleep is now disabled, but its caffeinate did not start: %w", err)
		}
		return fmt.Errorf("sleep is now enabled, but its caffeinate was not ended: %w", err)
	}
	return nil
}

// syncCaffeinate brings the caffeinate in line with the pmset state just
// written: one running while sleep is disabled, none while it is enabled.
func syncCaffeinate(disabled bool) error {
	if !disabled {
		// pkill exits 1 when nothing matched: the hold may have been placed
		// with no caffeinate beside it, and letting the Mac sleep is done
		// either way
		out, code, err := sleepRun(pkillCLI, "-f", caffeinatePattern)
		if err != nil {
			return err
		}
		if code > 1 {
			return fmt.Errorf("pkill caffeinate exited %d: %s", code, strings.TrimSpace(out))
		}
		return nil
	}
	out, code, err := sleepRun(pgrepCLI, "-f", caffeinatePattern)
	if err != nil {
		return err
	}
	switch code {
	case 0:
		return nil // one is already running
	case 1:
		return sleepSpawn()
	default:
		return fmt.Errorf("pgrep caffeinate exited %d: %s", code, strings.TrimSpace(out))
	}
}
