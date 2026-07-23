# kraken

A macOS CLI/TUI for wrangling live [Claude Code](https://claude.com/claude-code) and Codex sessions across tmux — and an orchestrator that runs whole jobs for you. When you have half a dozen agents running in different panes, windows, and sessions, `k` tells you what they're all doing and lets you act on any of them from wherever you are. Write a job spec as a markdown file, flip it ready, and the kraken daemon gives it a **cell** (its own git worktree + tmux session) staffed by a single agent, **zeus**, that researches and writes a spec, waits for your `/do` to build it, delegates implementation to subagents it manages itself, and opens the PR. There's no separate relay agent and no escalation inbox: zeus talks to you directly in its own pane, and kraken derives whether a cell is working or blocked from its transcript activity.

```
$ k
 #  STATUS   NAME          PROJECT   BRANCH       MODEL   PANE  TMUX      AGE
 1  working  yelp-dedupe   monolith  carl/yelp…   opus    %4    monolith  2m
 2  blocked  api-refactor  monolith  carl/api…    opus    %7    monolith  14s
 3  idle     scraper       pluto     main         sonnet  %2    pluto     1h
 ...
```

## Commands

| Command | What it does |
|---|---|
| `k` / `k ls` | list live Claude and Codex sessions plus agent-free shell panes, one numbering across everything |
| `k v` | live viewer TUI (see below) |
| `k o <sel>` | jump to an agent or shell: focuses its ghostty window and tmux pane |
| `k s <sel> <msg…>` | send a message into an agent's prompt (reads stdin if no message args) |
| `k r <sel> [n]` | read back the agent's last *n* transcript blocks (user/thinking/text) |
| `k b [sel]` | branch: fork a session into a vertical split via `claude --fork-session` (default: current pane) |
| `k k <sel>` | kill an agent or shell pane (refuses the current pane and nested agents) |
| `k n [-c dir] [cmd…]` | new shell window in the current session, optionally running a command |
| `k c [-c dir] <name> [prompt…]` | new named claude in a new window of the current session |
| `k j [--json]` | job board: every `*.job.md` in `~/Documents/pluto` with state, repo, prereqs, live cell |
| `k start <slug>` | start a job by hand: worktree on `carl/<slug>` off origin/main + tmux cell + zeus |
| `k done <slug> [outcome…]` | manually complete a job (the acceptance path for every job — a merged PR is a board chip + notification, never an auto-teardown) |
| `k daemon` | the scheduler loop (launchd runs it at login via `make install`) |
| `k migrate` | migrate jobs stuck in the old crew model's states: terminal ones (merged/abandoned/failed) → done; in-flight ones → cell torn down, requeuing left to the job's own `ready` flag |

`<sel>` is an index from `k ls`, `.` for the current pane, an exact pane id (`%3`), a session-uuid prefix (≥4 chars), or a substring match on name/project/tmux session.

`k ls` and `k v` take `-g type|session|none` to group tables by agent type (default), by tmux session, or not at all. `k ls --json` emits machine-readable output.

### Viewer (`k v`)

A live-refreshing dashboard of every agent and shell. Vim keys:

- `j`/`k` move, `g`/`G` top/bottom, `enter` or click to jump to the selection
- `b` fork the selected session into a split
- `dd` kill the selected agent/pane (press `d` twice, vim-style)
- `t` cycle grouping (type → session → none), `r` force refresh, `q` quit
- `J` toggle the **jobs view**: the job board and daemon health — `enter` drills into a cell's zeus pane, `a` sends a free-text steering message to a blocked job's zeus (refused while a question dialog is open in the pane — answer a pending `AskUserQuestion` from the web board or iOS app instead, the TUI has no digit path), `s` start a queued job, `A` open the latest artifact, `xx` abandon a job (cell killed, worktree kept)

A header shows your Claude usage-limit windows (and Codex rate limits when codex sessions are live).

### Jobs

Jobs live as `<slug>.job.md` files in `~/Documents/pluto` (typing `{{name}}` in a pluto note creates one from a template). A job names a repo, a `type` (`build` or `info`), prereqs (which form a DAG — a `gated: true` prereq parks the job for your sign-off after the prereq's outcome lands), and a `ready` flag.

Every job sits in one of five states: **draft** (not ready, or unparseable — the board shows the parse error), **queued** (ready, no cell yet), **working**, **blocked**, or **done**. Kraken derives working/blocked itself, straight from the cell's transcript activity — nobody writes status by hand: if a cell hasn't produced any new transcript output in a few minutes, it's blocked, and the board's detail column shows whatever zeus last said before going idle. There's no status file and no escalation inbox to check separately.

The daemon queues ready jobs (at most 5 cells at once), each into a cell: a fresh worktree off `origin/main` (or `base:`) plus a single tmux pane running **zeus** (fable) — the sole agent for the job. Build jobs follow the `/aim` workflow: zeus researches the repo, writes a spec-mode doc into the cell's artifacts, and stops. Messaging it `/do` (from the board's steer composer) tells it to proceed: it implements the spec, delegating slices to **hephaestus** (sonnet) subagents that it reviews itself with three parallel single-dimension **athena** (opus) variants — athena-logic, athena-edge, athena-style — runs tests, and opens the PR. Info jobs follow the `/info` workflow instead — zeus researches and writes an explain-mode doc, with no PR; you accept it from the board (`k done`). Either doc's diagrams are each rendered by a one-shot **hermes** (opus) subagent zeus spins up per figure — hermes never talks to you, it just writes one SVG and exits. When zeus hits a real decision it can't default, it asks you directly with `AskUserQuestion`, which the web board and iOS app render as buttons (the TUI has no digit path — steer it with `a` instead, or jump into the pane). `solo: true` skips the whole subagent crew (hephaestus/athena/hermes) for a prose-only doc written by zeus alone. Frontmatter is parsed strictly: a job kraken can't fully read never runs, and the error surfaces as `draft` on the board.

## Install

Requires macOS, tmux, and Go ≥ 1.24 to build:

```
make install
```

This builds the binary, installs it to `~/.local/bin` as `kraken` with a `k` symlink, and drops zsh completion into your completions dir. `k o` focuses [ghostty](https://ghostty.org) windows via AppleScript, which needs Accessibility permission the first time.

## How it works

Discovery is stateless: every invocation snapshots `tmux list-panes -a` and one `ps` run, walks each pane's process subtree looking for claude/codex processes, and enriches what it finds from the session files Claude Code already maintains (`~/.claude/sessions/`, transcripts under `~/.claude/projects/`) and codex's session index. Status is refined by peeking at pane content, so "blocked on a question" is detected even mid-turn. Teams of in-process agents are nested under their leads via `~/.claude/teams` configs.

The one long-lived process is the launchd-managed daemon — and even it owns nothing it can't re-derive from job files plus discovery after a restart. Job files are the only durable state; cells (worktree, doc source, artifacts) are plain directories under `~/.kraken/cells/`, disposable and rebuilt by `k start`/the daemon. A cell's status isn't stored anywhere either — it's derived on every read from the job file's state plus the cell's transcript activity under `~/.claude/projects/`.

Because discovery output can go stale between listing and acting, every targeted action (`k s`, `k k`, `k b`) re-verifies the pane still holds the agent you meant before touching it.
