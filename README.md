# kraken

A macOS CLI/TUI for wrangling live [Claude Code](https://claude.com/claude-code) and Codex sessions across tmux — and an orchestrator that runs whole jobs for you. When you have half a dozen agents running in different panes, windows, and sessions, `k` tells you what they're all doing and lets you act on any of them from wherever you are. Write a job spec as a markdown file, flip it ready, and the kraken daemon gives it a **cell** (its own git worktree + tmux session) staffed by a claude crew that plans, gets your approval, builds, and opens the PR — escalating to you through an inbox when it needs a human.

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
| `k start <slug>` | start a job by hand: worktree on `carl/<slug>` off origin/main + tmux cell + crew |
| `k reap` | check started jobs' PRs; merged ones get a verified teardown and `state: done` |
| `k done <slug> [outcome…]` | manually complete a job (the acceptance path for explore/question jobs) |
| `k daemon` | the scheduler loop (launchd runs it at login via `make install`) |

`<sel>` is an index from `k ls`, `.` for the current pane, an exact pane id (`%3`), a session-uuid prefix (≥4 chars), or a substring match on name/project/tmux session.

`k ls` and `k v` take `-g type|session|none` to group tables by agent type (default), by tmux session, or not at all. `k ls --json` emits machine-readable output.

### Viewer (`k v`)

A live-refreshing dashboard of every agent and shell. Vim keys:

- `j`/`k` move, `g`/`G` top/bottom, `enter` or click to jump to the selection
- `b` fork the selected session into a split
- `dd` kill the selected agent/pane (press `d` twice, vim-style)
- `t` cycle grouping (type → session → none), `r` force refresh, `q` quit
- `J` toggle the **jobs view**: the job board, daemon health, and the escalation inbox — `enter` drills into a cell or answers an escalation inline, `a` answer, `s` start a ready/gated job, `A` open the latest artifact, `xx` abandon a job (cell killed, worktree kept)

A header shows your Claude usage-limit windows (and Codex rate limits when codex sessions are live).

### Jobs

Jobs live as `<slug>.job.md` files in `~/Documents/pluto` (typing `{{name}}` in a pluto note creates one from a template). A job names a repo, success criteria, prereqs (which form a DAG — a `gated: true` prereq parks the job for your sign-off after the prereq's outcome lands), and a `ready` flag. The daemon queues ready jobs (at most 5 cells at once), each into a cell: a fresh worktree off `origin/main`, a tmux session running **zeus** (fable, orchestrator — plans first, and waits for your plan approval unless `auto-approve: true`) beside **hermes** (fable, your interface — it turns plans and blockages into high-level escalation files kraken notifies you about). Zeus delegates implementation to **hephaestus** (sonnet) subagents reviewed by **athena** (opus). Build jobs end at a merged PR (auto-reaped); explore/question jobs end when you accept their artifact (`k done`). Frontmatter is parsed strictly: a job kraken can't fully read never runs, and the error lands in the inbox.

## Install

Requires macOS, tmux, and Go ≥ 1.24 to build:

```
make install
```

This builds the binary, installs it to `~/.local/bin` as `kraken` with a `k` symlink, and drops zsh completion into your completions dir. `k o` focuses [ghostty](https://ghostty.org) windows via AppleScript, which needs Accessibility permission the first time.

## How it works

Discovery is stateless: every invocation snapshots `tmux list-panes -a` and one `ps` run, walks each pane's process subtree looking for claude/codex processes, and enriches what it finds from the session files Claude Code already maintains (`~/.claude/sessions/`, transcripts under `~/.claude/projects/`) and codex's session index. Status is refined by peeking at pane content, so "blocked on a question" is detected even mid-turn. Teams of in-process agents are nested under their leads via `~/.claude/teams` configs.

The one long-lived process is the launchd-managed daemon — and even it owns nothing it can't re-derive from job files plus discovery after a restart. Job files are the only durable state; cells and their escalation inboxes are plain directories under `~/.kraken/cells/`.

Because discovery output can go stale between listing and acting, every targeted action (`k s`, `k k`, `k b`) re-verifies the pane still holds the agent you meant before touching it.
