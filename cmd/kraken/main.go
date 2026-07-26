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
	case "account":
		cmdAccount(rest)
	case "logevent":
		cmdLogEvent(rest)
	case "serve":
		cmdServe(rest)
	case "import":
		cmdImport(rest)
	case "update":
		cmdUpdate(rest)
	case "ship-ios":
		cmdShipIOS(rest)
	case "pr-hook":
		cmdPrHook(rest)
	case "questions":
		cmdQuestions(rest)
	case "feedback":
		cmdFeedback(rest)
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
  k account           show rotation mode (auto/manual) + a table of every
                      Claude account (main + ~/.claude-pool slots): usage,
                      identity, flags
  k account auto      re-enable auto-rotation (no swap)
  k account <slug>    manually switch the main login to pool slot <slug>,
                      always — regardless of thresholds — and pin rotation to
                      manual (k account auto to release it). note: a pool
                      slot literally named "auto" can never be targeted this
                      way — the auto keyword always wins
  k serve [-p port] [--tailnet] [-addr host:port]
                      the one long-lived kraken process (launchd runs this at
                      login via make install): opens ~/.kraken/kraken.db, runs
                      the scheduler tick loop (queues ready jobs (≤5 cells),
                      derives working/blocked from cell activity, tracks each
                      job's PRs — merge = notification + board chip, never
                      teardown) + nightly backup rotation as background
                      goroutines, and serves the web UI/API (default
                      127.0.0.1:7777) in the foreground: jobs as a kanban
                      board, artifacts rendered in the browser, AskUserQuestion
                      prompts answered inline; GET/POST /api/peers* let a
                      device on the tailnet list/read/message live sessions.
                      --tailnet binds the Mac's tailscale IP (100.64.0.0/10)
                      instead of localhost; -addr sets an explicit bind and
                      overrides both -p and --tailnet
  k import [--dry-run] [--force] [--reimport]
                      one-shot sqlite-migration cutover: sweeps every file
                      store (job files, board order, uploads, artifacts,
                      event logs, doc feedback) into ~/.kraken/kraken.db.
                      Refuses while a daemon/serve is running, or against an
                      already-populated/already-imported database without
                      --force/--reimport; --dry-run reports counts without
                      writing anything. Human-run once per machine at cutover.
  k update [--force] [--dry-run] [--test]
                      self-update: fast-forward the kraken checkout to
                      origin/main, rebuild, reinstall, and restart the launchd
                      job — the same sequence the board's UPDATE button fires
                      (it just spawns this verb detached). Refuses on a dirty
                      tree or a non-main branch, and never merges anything but
                      a fast-forward: it will not pull, stash, or reset.
                      --force reinstalls even when already up to date,
                      --dry-run reports the ahead/behind state and changes
                      nothing, --test runs 'go test ./...' after the build and
                      aborts before installing if it is red.
  k ship-ios [--dry-run]
                      build the iOS app and upload it to TestFlight, by running
                      ios/ship-testflight.sh in the kraken checkout (xcodegen,
                      xcodebuild archive, export, xcrun altool) — the same
                      sequence the board's SHIP iOS button fires. Needs
                      ASC_KEY_ID and ASC_ISSUER_ID in the environment plus the
                      matching AuthKey_<id>.p8, and refuses without them.
                      'done' means uploaded, NOT installed: TestFlight still
                      has to process the build before the phone is offered it.
                      --dry-run preflights and prints the plan, building and
                      uploading nothing. See ios/TESTFLIGHT.md — the first run
                      must be done by hand.
  k questions get|set|add|update|remove <slug> ...
                      cell-side verb zeus uses instead of hand-rolled curl to
                      read/write its own open doc questions, proxied over
                      localhost to 'kraken serve': get prints the list as
                      JSON; set bulk-replaces it from a JSON array on stdin;
                      add/update read one question object from stdin; remove
                      takes a bare qid. A 400 (bad qid/importance) or an
                      unreachable serve prints a clean message to stderr and
                      exits non-zero.
  k feedback get|addressed <slug> ...
                      cell-side verb zeus uses to read carl's per-artifact
                      feedback and record how it addressed an entry, proxied
                      the same way as 'k questions'; get prints open+
                      addressed entries as JSON, addressed <slug> <id>
                      <text...> marks one entry addressed (text from args,
                      or stdin if omitted), addressed --clear <slug> <id>
                      clears a prior addressed note and reopens the entry.

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
