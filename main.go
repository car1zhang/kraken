package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		args = []string{"ls"}
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "ls":
		cmdLs(rest)
	case "v":
		cmdViewer(rest)
	case "o":
		cmdOpen(rest)
	case "b":
		cmdBranch(rest)
	case "s":
		cmdSend(rest)
	case "r":
		cmdRead(rest)
	case "k":
		cmdKill(rest)
	case "n":
		cmdNewShell(rest)
	case "c":
		cmdNewClaude(rest)
	case "_complete":
		cmdComplete()
	case "-h", "--help", "help":
		usage()
	default:
		die("unknown command: %s (try 'k help')", cmd)
	}
}

func usage() {
	fmt.Println(`kraken - live Claude and Codex sessions across tmux

Commands:
  k ls [--json] [-g <mode>]
			list live Claude and Codex sessions plus agent-free
			shell panes, one numbering across all rows; -g groups the tables:
			type = Claude + Codex + shells (default), session = one table per tmux
                      session with window numbers, none = everything in one table
  k v [-g <mode>]     live viewer TUI: vim keys, enter/click to open, b to branch,
                      dd to kill, t to cycle grouping
  k o <sel>           jump to an agent or shell pane: focus its ghostty window + pane
  k b [sel]           branch (fork) a session into a vertical split (default: current pane)
  k s <sel> <msg...>  send a message into an agent's prompt (stdin if no msg args)
  k r <sel> [n]       read back the last n message blocks (user/thinking/text; default 1; sel '.' = current pane)
  k k <sel>           kill an agent or shell pane (refuses the current pane and nested agents)
  k n [-c dir] [cmd...]
                      new shell in a new window of the current session, optionally
                      running cmd (drops back to the shell when cmd exits)
  k c [-c dir] <name> [prompt...]
                      new named claude in a new window of the current session

<sel>: index from 'k ls', '.' for the current pane, an exact pane id (%3), or
substring match on name/project/agent/tmux-session; also a session-uuid
prefix (>=4 chars).`)
}

func home() string {
	h, err := os.UserHomeDir()
	if err != nil {
		die("home dir: %v", err)
	}
	return h
}

func die(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "kraken: "+format+"\n", a...)
	os.Exit(1)
}

// --- completion ---

func slugify(s string) string {
	var out strings.Builder
	lastDash := true
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			out.WriteRune(r)
			lastDash = false
		} else if !lastDash {
			out.WriteRune('-')
			lastDash = true
		}
	}
	return strings.Trim(out.String(), "-")
}

func cmdComplete() {
	all, _ := orderAll(discoverAll())
	for _, p := range all {
		slug := slugify(displayName(p))
		if slug == "" {
			slug = slugify(projectLabel(p))
		}
		if slug == "" {
			slug = strconv.Itoa(p.Index)
		}
		fmt.Printf("%s\t%s %s %s %s\n", slug, p.Status, projectLabel(p), p.Pane, p.TmuxSession)
	}
}
