package jobs

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"kraken/internal/store"
	"kraken/internal/util"
)

// stackstop.go — teardown's one process-level cleanup: a parallel kraken
// serve the cell registered as a stack link is stopped when the cell is torn
// down.
//
// The manual-test recipe (CLAUDE.md) runs a test build of kraken as a second
// serve under an isolated HOME on another port, and the do skill has the cell
// register that port with `kraken stack add` before the gate line and shut
// the serve down once the gate passes. The shutdown has no fallback when the
// gate is passed by a merge with no reply: the merge bump and the board's
// Done can land seconds apart, the cell is torn down before its zeus can act,
// teardown deletes the stack ROWS and the serve keeps running. A kraken serve
// is not an inert listener — its daemon tick fires the nightly dream and mind
// passes at the first tick of each local day, each creating a job in the
// serve's own store and spawning a real claude cell into the SHARED tmux
// server under the real HOME — so an orphaned test serve turns into a second
// nightly pass over the machine's real transcripts.
//
// What is stopped is deliberately narrow: a LISTENER on the port of a
// LOOPBACK stack link, whose command line is a kraken binary running `serve`,
// that was already running when the cell LAST registered the link, on a port
// no other job's stack row names. A dev server the cell registered (vite, an
// app's own dev server) is never a target — the cell may not own it and the
// operator may be using it. The board is never a target either: the default
// serve port is excluded whoever is tearing down, and so are the tearing-down
// daemon's own pid and bound port, so neither the real board nor the parallel
// serve running a teardown can be stopped from here. A listener that started
// AFTER the link's registration time is not the one the cell registered (the
// recipe names one port, so another cell's serve can sit on it later) — and
// that time is the LAST `kraken stack add` of the url, which store.UpsertStack
// refreshes on a re-add, because the do skill has the cell re-add before every
// `manual test ready:` line, after the restart a revision round costs the
// serve. A port a sibling job still registers is that sibling's to stop.
// Everything that is skipped is logged, and every step is best-effort and
// never fatal, teardown's posture for its sibling cleanups.

// stackExecTimeout bounds each process-table call. teardownCell runs inside
// the board's Done request, so a hung lsof must not stall it.
const stackExecTimeout = 5 * time.Second

// lsofPath resolves lsof. The launchd daemon's PATH carries no /usr/sbin, so
// a bare "lsof" fails under serve (discovery's two bare execs are the same
// gap); macOS ships it at a fixed path, with PATH as the fallback elsewhere.
func lsofPath() (string, error) {
	const fixed = "/usr/sbin/lsof"
	if _, err := os.Stat(fixed); err == nil {
		return fixed, nil
	}
	return exec.LookPath("lsof")
}

// stackListeners returns the pids listening on TCP port. Seam for tests.
var stackListeners = func(port string) ([]int, error) {
	lsof, err := lsofPath()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), stackExecTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, lsof, "-nP", "-iTCP:"+port, "-sTCP:LISTEN", "-Fp").Output()
	if err != nil {
		// lsof exits 1 with nothing on stderr when nothing matches, which is
		// the common answer. Anything else — a timeout, a real failure — is
		// an error the caller logs and skips.
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) == 0 && ctx.Err() == nil {
			return nil, nil
		}
		return nil, err
	}
	var pids []int
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(line, "p") {
			continue
		}
		if pid, err := strconv.Atoi(strings.TrimPrefix(line, "p")); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

// stackProcInfo returns pid's start time and full command line, from one ps
// call. Seam for tests.
var stackProcInfo = func(pid int) (time.Time, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), stackExecTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ps", "-p", strconv.Itoa(pid), "-o", "lstart=", "-o", "command=")
	// lstart is locale-formatted; parseProcInfoLine reads the C locale's shape.
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		return time.Time{}, "", err
	}
	return parseProcInfoLine(strings.TrimSpace(string(out)))
}

// parseProcInfoLine splits `ps -o lstart= -o command=` output: five fields of
// start time ("Mon Jan 2 15:04:05 2006", in local time) followed by the
// command line.
func parseProcInfoLine(line string) (time.Time, string, error) {
	fields := strings.Fields(line)
	if len(fields) < 6 {
		return time.Time{}, "", fmt.Errorf("ps output too short: %q", line)
	}
	started, err := time.ParseInLocation("Mon Jan 2 15:04:05 2006", strings.Join(fields[:5], " "), time.Local)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("ps lstart %q: %w", strings.Join(fields[:5], " "), err)
	}
	return started, strings.Join(fields[5:], " "), nil
}

// stackStop sends SIGTERM to pid. Seam for tests.
var stackStop = func(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Signal(syscall.SIGTERM)
}

// isKrakenServeCommand says whether a process command line is a kraken binary
// running `serve`: argv[0]'s basename starts with "kraken" (the installed
// binary, the recipe's `kraken-test`, a scratch build named after the repo) or
// is the `k` symlink, and argv[1] is `serve`. Anything else — a dev server, a
// shell, a claude — is not kraken's to stop.
func isKrakenServeCommand(cmd string) bool {
	fields := strings.Fields(cmd)
	if len(fields) < 2 {
		return false
	}
	base := filepath.Base(fields[0])
	return (strings.HasPrefix(base, "kraken") || base == "k") && fields[1] == "serve"
}

// stackStopDecision is one listener's outcome, for the log line.
type stackStopDecision struct {
	URL     string
	PID     int
	Command string
	Stop    bool
	Reason  string // why it is left running; empty when Stop
}

// stackServesToStop decides, for each link, which listeners are the cell's
// parallel kraken serves to stop and which are left running, with the
// reason. otherPorts are the loopback ports every OTHER job's stack rows
// name; ownPort is the port this daemon is bound to; selfPID its own pid.
// Pure over its seams, so the rule is testable without a process table.
func stackServesToStop(links []store.StackLink, otherPorts map[string]bool, ownPort string, selfPID int) []stackStopDecision {
	var out []stackStopDecision
	seen := map[int]bool{}
	defaultPort := portOf(defaultServeAddr)
	for _, l := range links {
		tgt, err := ParseStackTarget(l.URL)
		if err != nil || !tgt.Loopback {
			continue
		}
		if tgt.Port == ownPort || tgt.Port == defaultPort {
			continue
		}
		if otherPorts[tgt.Port] {
			out = append(out, stackStopDecision{URL: l.URL, Reason: "another job still registers this port"})
			continue
		}
		pids, err := stackListeners(tgt.Port)
		if err != nil {
			util.LogEvent("jobs", "info", "stack-stop", fmt.Sprintf("%s: listing listeners on :%s failed: %v", l.URL, tgt.Port, err))
			continue
		}
		registered := time.UnixMilli(l.At)
		for _, pid := range pids {
			if pid == selfPID || pid <= 0 || seen[pid] {
				continue
			}
			seen[pid] = true
			started, cmd, err := stackProcInfo(pid)
			if err != nil {
				out = append(out, stackStopDecision{URL: l.URL, PID: pid, Reason: "process table unreadable: " + err.Error()})
				continue
			}
			d := stackStopDecision{URL: l.URL, PID: pid, Command: cmd}
			switch {
			case !isKrakenServeCommand(cmd):
				d.Reason = "not a kraken serve"
			case l.At > 0 && started.After(registered.Add(time.Second)):
				// ps reports whole seconds; the link's At is millis.
				d.Reason = "started after the link was registered"
			default:
				d.Stop = true
			}
			out = append(out, d)
		}
	}
	return out
}

// portOf is the port of a host:port, or "" when it has none.
func portOf(addr string) string {
	if _, port, err := net.SplitHostPort(addr); err == nil {
		return port
	}
	return ""
}

// ownServePort is the port this process is bound to: the hook serve address
// when serve recorded one, else the default. A process that is not serve
// (tests, the CLI) answers the default.
func ownServePort() string {
	addr := currentHookServeAddr()
	if addr == "" {
		addr = defaultServeAddr
	}
	return portOf(addr)
}

// otherJobStackPorts collects the loopback ports named by every stack row of
// a job other than jobID — a port a sibling still registers is the sibling's
// to stop. A listing failure answers an empty set with the error, and the
// caller then stops nothing: a wrong "nobody else registers this" is the one
// answer here that can take down a live gate.
func otherJobStackPorts(st *store.Store, jobID int64) (map[string]bool, error) {
	all, err := st.ListAllStacks()
	if err != nil {
		return nil, err
	}
	ports := map[string]bool{}
	for _, l := range all {
		if l.JobID == jobID {
			continue
		}
		if tgt, err := ParseStackTarget(l.URL); err == nil && tgt.Loopback {
			ports[tgt.Port] = true
		}
	}
	return ports, nil
}

// StopStackServesForTeardown is teardownCell's call, made BEFORE the stack
// rows are deleted (DeleteStacksForTeardown), since the rows are the only
// record of which ports the cell registered. No store, no id or no links is
// a nil. Each stop and each left-running listener is logged; nothing here
// can fail the teardown.
func StopStackServesForTeardown(j *Job) error {
	st := ActiveStore()
	if st == nil || j == nil || j.ID == 0 {
		return nil
	}
	links, err := st.ListStacks(j.ID)
	if err != nil || len(links) == 0 {
		return err
	}
	otherPorts, err := otherJobStackPorts(st, j.ID)
	if err != nil {
		return fmt.Errorf("listing other jobs' stack links (stopping nothing): %w", err)
	}
	for _, d := range stackServesToStop(links, otherPorts, ownServePort(), os.Getpid()) {
		first := d.Command
		if f := strings.Fields(d.Command); len(f) > 0 {
			first = f[0]
		}
		if !d.Stop {
			util.LogEvent("jobs", "info", "stack-left-running", fmt.Sprintf("%s: %s left running (pid %d, %s): %s", j.Slug, d.URL, d.PID, first, d.Reason))
			continue
		}
		if err := stackStop(d.PID); err != nil {
			util.LogEvent("jobs", "info", "stack-stop", fmt.Sprintf("%s: stopping %s (pid %d, %s) failed: %v", j.Slug, d.URL, d.PID, first, err))
			continue
		}
		util.LogEvent("jobs", "info", "stack-stop", fmt.Sprintf("%s: stopped the kraken serve behind %s (pid %d, %s)", j.Slug, d.URL, d.PID, first))
	}
	return nil
}
