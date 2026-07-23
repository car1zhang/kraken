package main

import (
	"fmt"
	"os"
	"strconv"

	// Blank import of the module-root package: its init() injects the
	// embedded .claude/skills bundle into jobs.SkillsFS (go:embed pins that
	// package to repo root, where .claude/ lives).
	_ "kraken"

	"kraken/internal/core"
	"kraken/internal/util"
)

func main() {
	// Under launchd there is no locale, and in the C locale tmux treats TAB
	// as unprintable — every -F field separator comes back as "_" and
	// discovery parses zero panes. Give our tmux children a UTF-8 locale.
	if os.Getenv("LANG") == "" && os.Getenv("LC_ALL") == "" {
		os.Setenv("LANG", "en_US.UTF-8")
	}
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
	case "j":
		cmdJobs(rest)
	case "account":
		cmdAccount(rest)
	case "start":
		cmdStart(rest)
	case "done":
		cmdDone(rest)
	case "archive":
		cmdArchive(rest)
	case "unarchive":
		cmdUnarchive(rest)
	case "reactivate":
		cmdReactivate(rest)
	case "daemon":
		cmdDaemon(rest)
	case "migrate":
		cmdMigrate(rest)
	case "logevent":
		cmdLogEvent(rest)
	case "web":
		cmdWeb(rest)
	case "pr-hook":
		cmdPrHook(rest)
	case "_complete":
		cmdComplete()
	case "-h", "--help", "help":
		usage()
	default:
		util.Die("unknown command: %s (try 'k help')", cmd)
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
  k j [--json]        job board: every *.job.md in ~/Documents/pluto with
                      state, repo, prereqs, live cell
  k account           show rotation mode (auto/manual) + a table of every
                      Claude account (main + ~/.claude-pool slots): usage,
                      identity, flags
  k account auto      re-enable auto-rotation (no swap)
  k account <slug>    manually switch the main login to pool slot <slug>,
                      always — regardless of thresholds — and pin rotation to
                      manual (k account auto to release it). note: a pool
                      slot literally named "auto" can never be targeted this
                      way — the auto keyword always wins
  k start <slug>      start a job manually: git worktree on carl/<slug> +
                      tmux session cell-<slug> + zeus (requires ready: true
                      and prereqs done)
  k done <slug> [outcome...]
                      manually complete a started job (the acceptance path for
                      info jobs): preserves artifacts into pluto,
                      tears the cell down, records the outcome
  k archive <slug>    move a job (file + preserved artifacts) into
                      ~/Documents/pluto/archive, freeing its slug; archive
                      name conflicts get numbered (foo-2, foo-3, ...). Only
                      jobs with no cell in flight — kill a live job first
                      (kill returns it to draft)
  k unarchive <archived-name>
                      restore an archived job (k j lists archived last) under
                      its original slug, state preserved; refused while the
                      slug is taken by an active job
  k reactivate <slug> [--session <uuid>] [message...]
                      resume a done job's original zeus session for
                      adjustments: same worktree path, branch recycled off
                      the base (fresh -rN only if unmerged commits remain),
                      state back to working
  k daemon            run the scheduler loop in the foreground (launchd runs
                      this at login via make install): queues ready jobs
                      (≤5 cells), derives working/blocked from cell activity,
                      tracks each job's PRs (merge = notification + board
                      chip, never teardown), notifies on blocked cells,
                      writes ~/.kraken/daemon.json
  k migrate           kill + requeue cells started under the old crew format
  k web [-p port] [--tailnet] [-addr host:port]
                      web UI + sessions API (default 127.0.0.1:7777): jobs as
                      a kanban board, artifacts rendered in the browser,
                      AskUserQuestion prompts answered inline; GET/POST
                      /api/peers* let a device on the tailnet list/read/message
                      live sessions. --tailnet binds the Mac's tailscale IP
                      (100.64.0.0/10) instead of localhost; -addr sets an
                      explicit bind and overrides both -p and --tailnet

<sel>: index from 'k ls', '.' for the current pane, an exact pane id (%3), or
substring match on name/project/agent/tmux-session; also a session-uuid
prefix (>=4 chars).`)
}

// --- completion ---

func cmdComplete() {
	all, _ := core.OrderAll(core.DiscoverAll())
	for _, p := range all {
		slug := util.Slugify(core.DisplayName(p))
		if slug == "" {
			slug = util.Slugify(core.ProjectLabel(p))
		}
		if slug == "" {
			slug = strconv.Itoa(p.Index)
		}
		fmt.Printf("%s\t%s %s %s %s\n", slug, p.Status, core.ProjectLabel(p), p.Pane, p.TmuxSession)
	}
}
