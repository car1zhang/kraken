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
	case "cutover":
		cmdCutover(rest)
	case "update":
		cmdUpdate(rest)
	case "ship-ios":
		cmdShipIOS(rest)
	case "pr-hook":
		cmdPrHook(rest)
	case "bash-lint":
		cmdBashLint()
	case "limit-hook":
		cmdLimitHook(rest)
	case "recall-hook":
		cmdRecallHook(rest)
	case "handoff-hook":
		cmdHandoffHook(rest)
	// A cell's statusline hook (mergeCellStatusLine installs it into every
	// cell's settings.json as `ctx-hook --slug <slug>`), feeding the daemon's
	// auto-handoff pass. Kept out of usage() with the rest of the hook family:
	// not a verb a human types, and it takes its whole input on stdin.
	case "ctx-hook":
		cmdCtxHook(os.Args[2:])
	case "questions":
		cmdQuestions(rest)
	case "goals":
		cmdGoals(rest)
	case "feedback":
		cmdFeedback(rest)
	case "watchlist":
		cmdWatchlist(rest)
	case "load":
		cmdLoad(rest)
	case "graph":
		cmdGraph(rest)
	case "mm":
		cmdMM(rest)
	case "slack":
		cmdSlack(rest)
	case "board":
		cmdBoard(rest)
	case "cell":
		cmdCell(rest)
	case "job":
		cmdJob(rest)
	case "mine":
		cmdMine(rest)
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
  k account auto      release the manual pin: each new cell binds to its own
                      pool account; main moves only for standing-agent rescues
  k account <slug>    manually switch the main login to pool slot <slug>,
                      always — regardless of thresholds — and pin rotation to
                      manual (k account auto to release it). note: a pool
                      slot literally named "auto" can never be targeted this
                      way — the auto keyword always wins
  k serve [-p port] [--tailnet] [-addr host:port]
                      the one long-lived kraken process (launchd runs this at
                      login via make install): opens ~/.kraken/kraken.db, runs
                      the scheduler tick loop (derives working/blocked from
                      cell activity, tracks each job's PRs — merge =
                      notification + board chip, never teardown; it starts
                      nothing on its own but the nightly dream/graph jobs it
                      creates itself, since submitting a job on the board is
                      what starts its cell) + nightly backup rotation as
                      background goroutines, and serves the web UI/API (default
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
  k cutover [--commit]
                      one-shot id-scoped renaming: moves the flat preserved
                      artifact copies in ~/Documents/pluto (and archive/) onto
                      the <slug>-<id>. prefix, so a name reused by a later job
                      can no longer claim an earlier job's files. Dry run by
                      default — prints every intended rename and changes
                      nothing; --commit performs them. Deliberately narrow: it
                      never touches cell directories or the token tables. Run
                      with no active jobs. Human-run once per machine.
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
  k goals get [slug]
                      cell-side verb zeus uses to re-read carl's current
                      goals, proxied the same way as 'k questions': prints
                      this week's and today's goals as JSON, day goals
                      carrying the weekly goal they sit under. With a slug it
                      also names every goal that job serves (a job may serve
                      several, or none), first one first. READ
                      ONLY — the goals are carl's, written from the board, so
                      there is deliberately no set/add/remove op here. The
                      same block is staged into every cell's brief at spawn
                      and refreshed mid-run, so this verb is for re-reading
                      it, not for discovering it.
  k feedback get|sweep|addressed <slug> ...
                      cell-side verb zeus uses to read carl's per-artifact
                      feedback and record how it addressed an entry, proxied
                      the same way as 'k questions'; get prints open+
                      addressed entries as JSON, addressed <slug> <id>
                      <text...> marks one entry addressed (text from args,
                      or stdin if omitted), addressed --clear <slug> <id>
                      clears a prior addressed note and reopens the entry.
                      sweep [--since <unix-millis>] takes NO slug: it is the
                      nightly dream pass' cross-job read, every note in the
                      window (last 24h by default) with its job slug and a
                      snapshot_url for the artifact bytes carl was looking at.
  k load get|set|clear <slug> ...
                      cell-side verb zeus uses to record what a BLOCKED job
                      costs and buys, proxied the same way as 'k questions':
                      set <slug> --unblock <1-10> --scope <1-10> [--note "..."]
                      writes how much effort unblocking it takes carl against
                      how big the claude work that reply unblocks is, and the
                      board ranks blocked cards by the ratio scope/unblock.
                      Both numbers or neither — a half estimate has no ratio,
                      and 'clear' is how an estimate is removed. get prints
                      the pair as JSON ({"load": null} when unestimated).
  k graph pending|add|done|search|get|stats ...
                      cell-side verbs for the knowledge graph, proxied the
                      same way as 'k questions'. pending [--limit N] prints
                      the episodes still awaiting extraction as JSON, oldest
                      first; add [--episode <id>] [--json <path>] posts one
                      extraction turn's whole output ({nodes[], edges[],
                      invalidate[]}, on stdin when --json is omitted) and
                      prints the resulting node/edge ids as JSON; done
                      <episode id> closes one episode, including one nothing
                      was extracted from; search "<query>" [--limit N] prints
                      the matching facts as triples ready to paste into
                      context; get <handle>... dereferences a handle from an
                      injected fact line — F<edge id> or a bare edge id for
                      the full untruncated fact, E<episode id> for the
                      episode it came from — printing each bundle as JSON;
                      stats prints the node/edge/episode counts as JSON.
  k mm mark|drop|note|get|lsp ...
                      CARL's verbs for the mental model — what he has read,
                      decided and noted — proxied the same way as 'k graph'.
                      Agents never write it: these are for carl's own shell
                      and the Zed capture task, and no skill documents them.
                      mark --worktree <dir> --file <path> --start-row <n>
                      [--note "..."] records a range of code as read, taking
                      the selected text from $KRAKEN_MM_SELECTION (an empty
                      selection means the single line at --start-row) and
                      anchoring it to the most durable commit that holds it —
                      origin/main, else the merge base, else HEAD, else
                      nothing; drop <id> unmarks one item; note "<text>"
                      [--context "..."] records a free note; get [--all]
                      prints the model as JSON, live items only unless --all
                      widens it to the invalidated ones; lsp runs the stdio
                      language server that draws live code marks back into
                      Zed as hints and offers a code action to drop one (run
                      by the zed/kraken-mm extension, not by hand).
  k watchlist get|add|update|remove ...
                      cell-side verb the nightly dream pass uses to track
                      recurring observations, proxied the same way. A watch
                      item is a row in its own table, so every id here is a
                      watch-row id, and the watchlist is GLOBAL — no slug on
                      any op. get [state] prints the LIVE items as JSON (state
                      filter optional: watching|shipped|declined); add reads
                      one watch-item object from stdin (observation, class,
                      optional src/dst/predicate) and FOLDS into the existing
                      item when that same observation is already tracked;
                      update reads {id, ...workflow fields} and refuses the
                      observation and its endpoints — the fold matches on the
                      sentence, so reword by removing and re-adding; remove
                      takes a bare id and CLOSES the row, which leaves every
                      list while staying readable by id (nothing is deleted).
                      verdict/comment are carl's, set from the board — sending
                      either here is a 400, not a silent drop.
  k slack inbox|status|ack ...
                      iris-side verbs for the Slack intake inbox, proxied the
                      same way as 'k questions'. inbox prints every unacked
                      message as JSON, arrival order — one row per DM, group
                      DM or direct @-mention the daemon's poll pass has seen;
                      ack --ids 42,43 --disposition "<what happened>" retires
                      a batch (all-or-nothing: an unknown or already-acked id
                      fails the whole call), and the disposition is the only
                      record of WHY a message left, so it is required;
                      status prints whether Slack is configured, when the poll
                      last ran, and how deep the backlog is. Reading Slack
                      itself is the daemon's job — no token is reachable here.
  k board get [slug]  agent-side read of the job board, proxied the same
                      way: with no slug it prints the whole /api/board
                      payload the web board and the phone render, with a slug
                      only that job's object (a client-side filter — there is
                      no per-job board route to drift from this one). An
                      unknown slug is an error naming it, never empty output.
  k cell steer|submit|answer <slug> ...
                      agent-side control of one job's cell, proxied to the
                      BOARD's own routes so every guard carl's clicks go
                      through applies unchanged: steer <slug> sends a message
                      into the cell's zeus (the message on stdin); submit
                      <slug> starts its cell, printing the server's own
                      refusal sentence when the ladder says no; answer <slug>
                      --digit N picks an option in a live AskUserQuestion
                      prompt, answer <slug> --question Qn records a doc
                      question's answer (the text on stdin). Each logs an
                      audit line with src 'cell' on success.
  k job list|raw|draft <slug> ...
                      iris-side verbs for the job board, proxied the same way.
                      list prints every active job's slug, state and one-line
                      summary as JSON; raw <slug> prints one job's whole text
                      plus the mtime lock token; draft <slug> [--mtime N]
                      reads the full job text from stdin and creates or amends
                      a DRAFT. The route is guarded server-side: it refuses
                      any state:, the retired ready:/prereqs: keys, an
                      unparseable body, and any started job — iris drafts, and
                      submitting (which starts the cell) stays carl's move on
                      the board.
  k mine digest|reduce [--since <unix-millis>] ...
                      the nightly dream pass' mechanical read of claude's own
                      transcripts under ~/.claude/projects — a pure local file
                      read, so unlike the three verbs above it needs no
                      'kraken serve'. digest walks every transcript touched in
                      the window (root files AND the subagents/agent-*.jsonl
                      sidecars, which carry ~24% of all tool calls) and prints
                      one JSON digest each: tool mix, is_error clusters by
                      tool, interrupts, <synthetic> API failures, turn
                      durations, repeated calls. reduce <path> prints ONE
                      transcript cut to the ~3% that carries a decision (user
                      prose, tool calls, assistant text; thinking, tool
                      results and attachments dropped) — what a per-transcript
                      scout actually reads. Both default to the last 24h.

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
