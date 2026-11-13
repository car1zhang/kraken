package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"kraken/internal/util"
)

// cmd_periodic.go — `k periodic add|list|run|pause|resume|rm`: periodic jobs,
// the stored templates the daemon turns into jobs on a cadence (a time of
// day on chosen days, or a fixed interval; internal/jobs/periodic.go),
// proxied to kraken serve's GET/POST /api/periodic
// (internal/api/periodic.go).
//
// Behavior contract, cmd_stack.go's:
//   - success: `list` prints the server's JSON to stdout verbatim, exit 0; a
//     write prints one short confirmation line to STDERR, exit 0.
//   - a 4xx prints the server's own error message to stderr, exit non-zero.
//   - kraken serve unreachable: a stderr message naming the address this
//     process tried, exit non-zero.
//   - bad/missing args: a usage message to stderr, exit non-zero.
//
// It NEVER opens kraken.db: only kraken serve writes it.

const periodicUsage = "usage: k periodic add|list|run|pause|resume|rm ..."

const periodicAddUsage = "usage: k periodic add <name> --prefix <slug> (--at <HH:MM> [--days daily|weekdays|mon,wed,...] | --every <30m|2h|3d|1w>) [--file <template.md>] (template on stdin when no --file)"

// periodicRunTimeout is the run op's client timeout. The server submits the
// job inside the request, and starting a cell (worktree, tmux, spawn) can
// take far longer than the 5s every other proxy verb waits.
const periodicRunTimeout = 60 * time.Second

// periodicAddArgs is one parsed `add`.
type periodicAddArgs struct {
	name, prefix, at, days, every, file string
}

func cmdPeriodic(args []string) {
	if len(args) == 0 {
		util.Die("%s", periodicUsage)
	}
	op, rest := args[0], args[1:]
	switch op {
	case "list":
		if len(rest) != 0 {
			util.Die("usage: k periodic list")
		}
		out, err := qfGet("/api/periodic")
		if err != nil {
			util.Die("%v", err)
		}
		if _, err := os.Stdout.Write(out); err != nil {
			util.Die("write stdout: %v", err)
		}
	case "add":
		a, err := parsePeriodicAdd(rest)
		if err != nil {
			util.Die("%v", err)
		}
		template, err := readPeriodicTemplate(a.file)
		if err != nil {
			util.Die("%v", err)
		}
		if _, err := qfPost("/api/periodic", map[string]any{
			"op": "add", "name": a.name, "prefix": a.prefix, "at": a.at, "days": a.days, "every": a.every, "template": template,
		}); err != nil {
			util.Die("%v", err)
		}
		fmt.Fprintf(os.Stderr, "periodic job added: %s\n", a.name)
	case "pause", "resume":
		name, err := parsePeriodicName(rest, op)
		if err != nil {
			util.Die("%v", err)
		}
		if _, err := qfPost("/api/periodic", map[string]any{"op": op, "name": name}); err != nil {
			util.Die("%v", err)
		}
		fmt.Fprintf(os.Stderr, "periodic job %sd: %s\n", op, name)
	case "run":
		name, err := parsePeriodicName(rest, op)
		if err != nil {
			util.Die("%v", err)
		}
		out, err := qfPostWith("/api/periodic", map[string]any{"op": "run", "name": name}, nil, periodicRunTimeout)
		if err != nil {
			util.Die("%v", err)
		}
		var res struct {
			Slug    string `json:"slug"`
			Started bool   `json:"started"`
			Note    string `json:"note"`
		}
		if err := json.Unmarshal(out, &res); err != nil {
			util.Die("decode server answer: %v", err)
		}
		if !res.Started {
			// The job exists as a draft; exit non-zero so a script sees
			// that nothing is running.
			util.Die("created %s but it did not start: %s", res.Slug, res.Note)
		}
		fmt.Fprintf(os.Stderr, "started %s\n", res.Slug)
	case "rm":
		name, err := parsePeriodicName(rest, "rm")
		if err != nil {
			util.Die("%v", err)
		}
		if _, err := qfPost("/api/periodic", map[string]any{"op": "remove", "name": name}); err != nil {
			util.Die("%v", err)
		}
		fmt.Fprintf(os.Stderr, "periodic job removed: %s\n", name)
	default:
		util.Die("%s (unknown op %q)", periodicUsage, op)
	}
}

// parsePeriodicAdd reads `<name> --prefix <p>`, then either `--at <HH:MM>
// [--days <list>]` or `--every <interval>`, and an optional `--file <path>`,
// each flag as `--flag value` or `--flag=value`. It checks shape only;
// kraken serve validates every value.
func parsePeriodicAdd(args []string) (periodicAddArgs, error) {
	var a periodicAddArgs
	if len(args) == 0 || strings.TrimSpace(args[0]) == "" || strings.HasPrefix(args[0], "-") {
		return a, fmt.Errorf("%s", periodicAddUsage)
	}
	a.name = args[0]
	seen := map[string]bool{}
	for i := 1; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "--") {
			return a, fmt.Errorf("%s (unexpected argument %q)", periodicAddUsage, arg)
		}
		flag, val, inline := strings.Cut(arg, "=")
		var dst *string
		switch flag {
		case "--prefix":
			dst = &a.prefix
		case "--at":
			dst = &a.at
		case "--days":
			dst = &a.days
		case "--every":
			dst = &a.every
		case "--file":
			dst = &a.file
		default:
			return a, fmt.Errorf("%s (unknown flag %q)", periodicAddUsage, flag)
		}
		if !inline {
			// a following flag is not a value: `--prefix --at 09:00` is a
			// --prefix with none, not a prefix spelled "--at".
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				return a, fmt.Errorf("%s (%s needs a value)", periodicAddUsage, flag)
			}
			val = args[i+1]
			i++
		}
		if val == "" {
			return a, fmt.Errorf("%s (%s needs a value)", periodicAddUsage, flag)
		}
		*dst = val
		seen[flag] = true
	}
	if !seen["--prefix"] {
		return a, fmt.Errorf("%s (missing --prefix)", periodicAddUsage)
	}
	switch {
	case seen["--every"] && (seen["--at"] || seen["--days"]):
		return a, fmt.Errorf("%s (--every is an interval and takes neither --at nor --days)", periodicAddUsage)
	case !seen["--every"] && !seen["--at"]:
		return a, fmt.Errorf("%s (missing --at, or --every for an interval)", periodicAddUsage)
	}
	return a, nil
}

// parsePeriodicName reads the one name pause, resume, run and rm take.
func parsePeriodicName(args []string, op string) (string, error) {
	if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
		return "", fmt.Errorf("usage: k periodic %s <name>", op)
	}
	return strings.TrimSpace(args[0]), nil
}

// readPeriodicTemplate reads the job template from file, or from stdin when
// no file is named. An interactive stdin is refused rather than waited on.
func readPeriodicTemplate(file string) (string, error) {
	if file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("read template: %w", err)
		}
		return string(b), nil
	}
	if fi, err := os.Stdin.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		return "", fmt.Errorf("no template: pass --file <path> or pipe the job text on stdin")
	}
	b, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", fmt.Errorf("read template from stdin: %w", err)
	}
	return string(b), nil
}
