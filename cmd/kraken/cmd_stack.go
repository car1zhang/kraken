package main

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"kraken/internal/util"
)

// cmd_stack.go — `k stack add|rm|get|clear <slug>`: the cell-side verb for a
// job's STACK LINKS — the URLs of whatever the cell spun up for manual
// testing — proxying to kraken serve's GET/POST /api/job/stack
// (internal/api/jobstack.go). The board renders each as a linked chip beside
// the PR chips, dims it when the daemon's probe finds the port closed, and
// drops them all at teardown.
//
// Behavior contract, the same one cmd_load.go's header spells out:
//   - success: `get` prints the server's JSON to stdout verbatim, exit 0; a
//     write prints one short confirmation line to STDERR, exit 0.
//   - a 4xx prints the server's own error message to stderr, exit non-zero.
//   - kraken serve unreachable: a stderr message naming the address this
//     process tried, exit non-zero — never silently succeeds.
//   - bad/missing args: a usage message to stderr, exit non-zero.
//
// It NEVER opens kraken.db: only kraken serve writes it (single-writer rule).
func cmdStack(args []string) {
	if len(args) == 0 {
		util.Die("usage: k stack add|rm|get|clear <slug> ...")
	}
	op, rest := args[0], args[1:]
	switch op {
	case "get":
		out, err := runStackGet(rest)
		if err != nil {
			util.Die("%v", err)
		}
		if _, err := os.Stdout.Write(out); err != nil {
			util.Die("write stdout: %v", err)
		}
	case "add":
		slug, link, err := parseStackAdd(rest)
		if err != nil {
			util.Die("%v", err)
		}
		if err := runStackOp(slug, "add", link.url, link.label); err != nil {
			util.Die("%v", err)
		}
		fmt.Fprintf(os.Stderr, "stack link added to %s: %s\n", slug, link.url)
	case "rm":
		slug, token, err := parseStackRm(rest)
		if err != nil {
			util.Die("%v", err)
		}
		if err := runStackOp(slug, "remove", token, ""); err != nil {
			util.Die("%v", err)
		}
		fmt.Fprintf(os.Stderr, "stack link removed from %s: %s\n", slug, token)
	case "clear":
		slug, err := parseStackSlugOnly(rest, "clear")
		if err != nil {
			util.Die("%v", err)
		}
		if err := runStackOp(slug, "clear", "", ""); err != nil {
			util.Die("%v", err)
		}
		fmt.Fprintf(os.Stderr, "stack links cleared for %s\n", slug)
	default:
		util.Die("usage: k stack add|rm|get|clear <slug> ... (unknown op %q)", op)
	}
}

const stackAddUsage = "usage: k stack add <slug> <http(s)://host:port[/path]> [--label \"<short name>\"]"

// stackLink is one parsed `add`: the url and its optional label. A named
// struct for the same reason loadFields is one — two strings travelling
// together are exactly what a caller swaps.
type stackLink struct {
	url   string
	label string
}

// runStackGet is `k stack get <slug>`: GET /api/job/stack?slug= — prints
// {"stack": [...]} straight through (an empty array when the job has none).
func runStackGet(args []string) ([]byte, error) {
	slug, err := parseStackSlugOnly(args, "get")
	if err != nil {
		return nil, err
	}
	return qfGet("/api/job/stack?slug=" + url.QueryEscape(slug))
}

// parseStackAdd reads `<slug> <url> [--label <text>]`. Both `--label value`
// and `--label=value` are accepted; anything else is a usage error. The url
// gets one LOCAL check — it must start with http:// or https:// — so the
// common mistake (`localhost:5173` with no scheme) is named here without a
// round trip; the server's ParseStackTarget is the authoritative gate.
func parseStackAdd(args []string) (string, stackLink, error) {
	var l stackLink
	if len(args) < 2 || strings.TrimSpace(args[0]) == "" || strings.HasPrefix(args[0], "-") {
		return "", l, fmt.Errorf("%s", stackAddUsage)
	}
	slug := strings.TrimSpace(args[0])
	l.url = strings.TrimSpace(args[1])
	lower := strings.ToLower(l.url)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return "", l, fmt.Errorf("bad url %q: must start with http:// or https:// (%s)", l.url, stackAddUsage)
	}
	for i := 2; i < len(args); i++ {
		a := args[i]
		name, raw, inline := strings.Cut(a, "=")
		if !inline {
			name = a
			if name != "--label" {
				return "", l, fmt.Errorf("%s (unexpected argument %q)", stackAddUsage, a)
			}
			if i+1 >= len(args) {
				return "", l, fmt.Errorf("%s (--label needs a value)", stackAddUsage)
			}
			raw = args[i+1]
			i++
		}
		if name != "--label" {
			return "", l, fmt.Errorf("%s (unexpected argument %q)", stackAddUsage, a)
		}
		l.label = raw
	}
	return slug, l, nil
}

// parseStackRm reads `<slug> <url-or-label>`. The token goes to the server
// as-is; the server matches url first, then label.
func parseStackRm(args []string) (string, string, error) {
	if len(args) != 2 || strings.TrimSpace(args[0]) == "" || strings.TrimSpace(args[1]) == "" {
		return "", "", fmt.Errorf("usage: k stack rm <slug> <url-or-label>")
	}
	return strings.TrimSpace(args[0]), strings.TrimSpace(args[1]), nil
}

func parseStackSlugOnly(args []string, op string) (string, error) {
	if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
		return "", fmt.Errorf("usage: k stack %s <slug>", op)
	}
	return strings.TrimSpace(args[0]), nil
}

// runStackOp posts one per-op body. The same four keys for every op — the
// server decides per op which are required — so there is one route and one
// body shape to keep in step.
func runStackOp(slug, op, u, label string) error {
	_, err := qfPost("/api/job/stack", map[string]any{"slug": slug, "op": op, "url": u, "label": label})
	return err
}
