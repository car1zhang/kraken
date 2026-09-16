# kraken

A macOS CLI/TUI for wrangling live [Claude Code](https://claude.com/claude-code) and Codex sessions across tmux — and an orchestrator that runs whole jobs for you. When you have half a dozen agents running in different panes, windows, and sessions, `k` tells you what they're all doing and lets you act on any of them from wherever you are. Write a job spec as a markdown file, submit it from the board, and `kraken serve` gives it a **cell** (its own git worktree + tmux session) staffed by a single agent, **zeus**, that researches and writes a spec, waits for your `/do` to build it, splits the parallelizable parts out to teammates it manages itself — each in its own pane you can watch — and opens the PR. There's no separate relay agent and no escalation inbox: zeus talks to you directly in its own pane, and kraken derives whether a cell is working or blocked from its transcript activity.

```
$ k
 #  STATUS   NAME          PROJECT   BRANCH       MODEL    PANE  TMUX      AGE
 1  working  yelp-dedupe   monolith  carl/yelp…   fable-5  %4    monolith  2m
 2  blocked  api-refactor  monolith  carl/api…    fable-5  %7    monolith  14s
 3  idle     scraper       pluto     main         sonnet   %2    pluto     1h
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
| `k c [-c dir] <name> [prompt…]` | new named claude (on kraken's default model, `claude-opus-5`) in a new window of the current session |
| `k serve [-p port] [--tailnet] [-addr host:port] [--mirror]` | the one long-lived kraken process: opens `~/.kraken/kraken.db`, runs the scheduler tick loop and nightly backups in the background, and serves the web UI on localhost + the tailnet (dual-bind, automatic once tailscale is up) — launchd runs it at login via `make install`. `--mirror` is a local-testing-only variant against a sandbox DB — see [Local mirror testing](#local-mirror-testing) |
| `kraken import [--dry-run] [--force] [--reimport] [--live-sources]` | **one-shot only**: sweeps any legacy file-mode job, artifact, upload, and log still on disk into `kraken.db`; a human-run migration verb, not a day-to-day one. `--live-sources` is a local-testing-only escape hatch — see [Local mirror testing](#local-mirror-testing) |

`<sel>` is an index from `k ls`, `.` for the current pane, an exact pane id (`%3`), a session-uuid prefix (≥4 chars), or a substring match on name/project/tmux session.

`k ls` and `k v` take `-g type|session|none` to group tables by agent type (default), by tmux session, or not at all. `k ls --json` emits machine-readable output.

### Viewer (`k v`)

A live-refreshing dashboard of every agent and shell. Vim keys:

- `j`/`k` move, `g`/`G` top/bottom, `enter` or click to jump to the selection
- `b` fork the selected session into a split
- `dd` kill the selected agent/pane (press `d` twice, vim-style)
- `t` cycle grouping (type → session → none), `r` force refresh, `q` quit
- `J` toggle the **jobs view**: the job board and daemon health — `enter` drills into a cell's zeus pane, `a` sends a free-text steering message to a blocked job's zeus (refused while a question dialog is open in the pane — answer a pending `AskUserQuestion` from the web board or iOS app instead, the TUI has no digit path), `s` start a draft (cuts its cell, same validation the board's start runs), `A` open the latest artifact, `xx` abandon a job (cell killed, worktree kept)

A header shows your Claude usage-limit windows (and Codex rate limits when codex sessions are live).

### Jobs

Jobs live as `<slug>.job.md` files in `~/Documents/pluto` (typing `{{name}}` in a pluto note creates one from a template). A job names a repo and a spec, and optionally `base:`, `solo:`, `model:` and `color:`.

Every job sits in one of four states: **draft** (never started, or unparseable — the board shows the parse error), **working**, **blocked**, or **done**. Kraken derives working/blocked itself, straight from the cell's transcript activity — nobody writes status by hand: if a cell hasn't produced any new transcript output in a few minutes, it's blocked, and the board's detail column shows whatever zeus last said before going idle. There's no status file and no escalation inbox to check separately.

Starting a job is one move: **submit** it (drag a draft into the working column, or `s` in the TUI). That starts its cell inside the request and answers once the cell is up, or refuses with the reason and leaves the job a draft — there is no queue, no cell cap, and nothing that starts a job in the background. The only exception is the nightly dream and graph jobs, which kraken creates for itself and submits on the same tick.

A cell is a fresh worktree off `origin/main` (or `base:`) plus a single tmux pane running **zeus** — the sole agent for the job. A cell runs on two tiers, split by role: zeus itself is spawned with `--model claude-fable-5-1` — the fast tier, since zeus plans, briefs, arbitrates and integrates, and overridable per job with `model:` — while its crew, hephaestus, athena and hermes, are all pinned to `model: opus` (Claude Opus 5) by zeus' brief, because the deep work is the part zeus delegates. `claude-opus-5` stays kraken's default everywhere else it starts a claude (`k c`, the board's spawn action, a cell's side threads). That crew are **teammates**, not in-process subagents: each is spawned with a `name`, which buys it its own tmux pane you can watch (`k v`) and attach to, a row in the team config so it hangs under the cell in `k ls`, and an address zeus can message to resume it mid-review instead of respawning it. Anonymous subagents are kept for read-only exploration that writes nothing. Which workflow a cell runs is zeus' own call, read from the spec text rather than from any field on the job. A job whose spec asks for something to be BUILT follows the `/aim` workflow: zeus researches the repo, writes a spec-mode doc into the cell's artifacts, and stops. Messaging it `/do` (from the board's steer composer) tells it to proceed: it implements the spec — making small self-contained edits itself and splitting only genuinely parallelizable work out to **hephaestus** teammates — reviews every change with three parallel single-dimension **athena** variants (athena-logic, athena-edge, athena-style), runs tests, writes a brief `90-build-report.md` (what shipped, where reality diverged from the spec, what fought back, what was deferred), and opens the PR. A job whose spec asks how something WORKS follows the `/info` workflow instead — zeus researches just as deeply but answers briefly: one short high-level `00-overview.md` ending in a "go deeper" menu of named areas, no open questions at all, and one focused artifact per follow-up you actually ask for. No PR; you accept it from the board (board Done). Either doc's diagrams are each rendered by a **hermes** teammate zeus spins up per figure (`hermes-<diagram-id>`) — hermes never talks to you, it writes one SVG and, if the linter complains, gets messaged for a fix round rather than replaced. Zeus doesn't stop to ask you things: a prompt in its pane is invisible to you and hangs the cell, so it picks a defensible default and says which way it went in its last message — the line the board shows. `AskUserQuestion` (rendered as buttons by the web board and iOS app; the TUI has no digit path — steer it with `a` instead) is a last resort for a call that can't be defaulted, and spawned agents never ask you anything at all. `solo: true` skips the whole crew (hephaestus/athena/hermes) for a prose-only doc written by zeus alone. Frontmatter is parsed strictly: a job kraken can't fully read never runs, and the error surfaces as `draft` on the board.

## Install

Requires macOS, tmux, and Go ≥ 1.24 to build:

```
make install
```

This builds the binary, installs it to `~/.local/bin` as `kraken` with a `k` symlink, and drops zsh completion into your completions dir. The launchd job runs `kraken serve`, which binds localhost and — automatically, whenever tailscale is up — the tailnet IP in the same process, so the iOS app reaches the board with no extra flags. `k o` focuses [ghostty](https://ghostty.org) windows via AppleScript, which needs Accessibility permission the first time.

## How it works

Discovery is stateless: every invocation snapshots `tmux list-panes -a` and one `ps` run, walks each pane's process subtree looking for claude/codex processes, and enriches what it finds from the session files Claude Code already maintains (`~/.claude/sessions/`, transcripts under `~/.claude/projects/`) and codex's session index. Status is refined by peeking at pane content, so "blocked on a question" is detected even mid-turn. Teams of in-process agents are nested under their leads via `~/.claude/teams` configs.

The one long-lived process is `kraken serve` — and even it owns nothing it can't re-derive from durable state plus discovery after a restart. Durable state is `~/.kraken/kraken.db`; preserved artifact flat-copies (`<slug>.<name>` files) and archived jobs' artifact copies still live as plain files under `~/Documents/pluto`/`~/Documents/pluto/archive` (by design — Q12), but a job's own spec/state now lives in the DB. Cells (worktree, doc source, artifacts) are plain directories under `~/.kraken/cells/`, disposable and rebuilt by the board's start action or the daemon. A cell's status isn't stored anywhere either — it's derived on every read from the job's state plus the cell's transcript activity under `~/.claude/projects/`.

kraken migrated from file-mode storage (job files plus size-rotated JSONL logs) to a single sqlite database; `kraken serve` is the only runtime and the sole DB writer. `kraken import` remains as the one-shot sweep for any machine that still has legacy file data sitting around, and can itself be retired once run.

Because discovery output can go stale between listing and acting, every targeted action (`k s`, `k k`, `k b`) re-verifies the pane still holds the agent you meant before touching it.

## Local mirror testing

`kraken import --live-sources` plus `kraken serve --mirror` let you try the sqlite-backed UI/API against a **snapshot of your real data** — real jobs, real artifacts, real usage history — without writing to `~/.kraken/kraken.db` or touching the live `kraken serve` process's cells at all. Everything below runs in a separate terminal, entirely off to the side of your real deployment.

Sandbox every path kraken would otherwise touch, in one shell — including a real **copy** of your pluto notes, not the live directory: preserved artifact copies (`<slug>.<name>` files) and archived jobs' artifact copies live as plain files under `~/Documents/pluto`/`~/Documents/pluto/archive` even in sqlite-backed mode (by design — see "How it works" above), untouched by `KRAKEN_DB_PATH` and the other overrides below, so skipping this step would leave the mirror's archive/unarchive/delete buttons (see caveats below) one gate slip away from touching your real notes:

```
cp -R ~/Documents/pluto /tmp/kraken-mirror/pluto
export KRAKEN_PLUTO_DIR=/tmp/kraken-mirror/pluto
export KRAKEN_DB_PATH=/tmp/kraken-mirror/kraken.db
export KRAKEN_UPLOADS_DIR=/tmp/kraken-mirror/uploads
export KRAKEN_LOGS_DIR=/tmp/kraken-mirror/logs
export KRAKEN_BACKUPS_DIR=/tmp/kraken-mirror/backups
```

Snapshot your real data into the sandbox DB:

```
kraken import --dry-run --live-sources   # counts only, writes nothing — sanity-check first
kraken import --live-sources             # the real sandbox import
```

`--live-sources` is what lets this run even while your real `kraken serve` is up: it skips the normal daemon-alive guard, but *only* because `KRAKEN_DB_PATH` above resolves somewhere other than the real `~/.kraken/kraken.db` — pointed at the real path (i.e. without the env override), it refuses. With `KRAKEN_PLUTO_DIR` pointed at the copy, `--live-sources` reads that copy instead of your real pluto dir — an equally valid import source, and one that leaves zero real-file exposure even if a future action slipped past a mirror-mode gate.

Then serve the mirror on a different port:

```
kraken serve --mirror -p 7778
```

Open `http://127.0.0.1:7778` — same board, same UI, backed by the sandbox DB. `-p` (or `-addr`) must differ from the real deployment's default port 7777; `--mirror` refuses to start otherwise, so it can never squat where the real `kraken serve` listens.

**Two caveats, always:**

- **Every tmux/process-facing AND real-pluto-file-facing board button is disabled.** Submit, kill, stop, steer, answer-question, retry, done, reactivate all 403 with `disabled in mirror mode`. Archive/unarchive/delete work — their only file side effect (preserved-artifact moves) lands in the sandboxed `KRAKEN_PLUTO_DIR` copy — but 403 if you skip the pluto sandbox step. There is no ungated exception any more: queue/dequeue used to be one, safe only because each merely flipped a `ready` field in the sandbox DB, and submitting a job now starts a real cell. The first group would otherwise reach into your REAL tmux panes and worktrees; archive/unarchive/delete are gated too because even in store mode they move or remove real `~/Documents/pluto` artifact files (`movePrefixed`/`removeArchivedArtifactFiles` — the job row itself is DB-local, but the preserved artifact copies never are). Job create/edit/save, reorder, uploads, drafts, feedback, and artifact/log/usage viewing all stay fully live (sandbox DB only). The mirror's own scheduler tick is read-only to match: it derives working/blocked state and mirrors artifacts into the sandbox DB, but never starts a cell, tears one down, polls/repairs PRs, auto-archives, or pops a real macOS notification.
- **The snapshot goes stale immediately.** With `KRAKEN_PLUTO_DIR` set as above, `--live-sources` reads the `cp -R` copy rather than your live pluto dir, so what goes stale is the *copy's* age relative to your real notes, not a mid-write race against the live daemon — re-run the `cp -R` if you want a fresher snapshot. (Without `KRAKEN_PLUTO_DIR`, it reads your real pluto/uploads/logs/cell files live, and the live daemon may be mid-write to any of them — the import is a point-in-time copy, seconds-stale the moment it finishes either way.) Fine for kicking the tires on the UI/API; never treat it as a substitute for the real one-shot cutover import.
