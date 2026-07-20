# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

kraken (`k`) is a macOS CLI/TUI for managing live Claude Code and Codex sessions across tmux: list them (`k ls`), watch them (`k v`), jump to one (`k o`), message one (`k s`), read its transcript tail (`k r`), fork it (`k b`), kill it (`k k`), spawn new shells/claudes (`k n`/`k c`). Run `k help` for full usage.

## Commands

- `make build` — build the `kraken` binary
- `make install` — build + install to `~/.local/bin` (with `k` symlink) + zsh completion. **Never `cp` over the installed binary directly**: macOS SIGKILLs running processes whose backing binary changes (code-signature invalidation). The Makefile copies to a temp name and `mv`s (atomic rename) for this reason.
- `go test ./...` — run all tests
- `go test -run TestName .` — run a single test
- `go vet . && gofmt -l .` — lint

## Architecture

Everything is `package main` — one binary, files split by concern. The only direct dependency is bubbletea (viewer TUI); all else is stdlib. External state is shelled out to: `tmux`, `ps`, `lsof`, `git`, `osascript`.

The core type is `Peer` (peer.go): one live agent process in a tmux pane, an in-process teammate known only from a team config (`Messageable=false`), or an agent-free shell pane (`Role "shell"`).

Data flow, shared by `k ls`, the viewer, and completion:

1. **Discovery** (discover.go): `discoverAll()` snapshots tmux panes (`tmux list-panes -a`) and the full process table (one `ps` call), then walks each pane's process subtree for claude/codex processes. Agents in a pane are listed deepest-first: index 0 is the foreground agent — the one `tmux send-keys` actually reaches; deeper ones are nested subagents. Enrichment comes from `~/.claude/sessions/<pid>.json` (session files Claude Code maintains), `~/.codex/session_index.jsonl` + lsof for codex, transcript files under `~/.claude/projects/<munged-cwd>/`, and `git`/`lsof` for branch/cwd. lsof is the slow path — results are memoized in package-level caches (`cwdCache`, `codexSessionCache`) because the viewer polls discovery on a tick.
2. **Refinement + nesting** (peer.go): `refineStatus` peeks pane content via `tmux capture-pane` to detect blocked/question prompts; `nestTeams` attaches teammates to their leads using `~/.claude/teams/*/config.json`; `orderAll` produces the single flat numbering that every selector references.
3. **Selection** (select.go): `selectPeer` resolves a user selector — index, `.` (current pane), exact pane id (`%3`), session-uuid prefix (≥4 chars), or substring match — against that ordering.
4. **Action or render**: actions (actions.go, branch.go) drive tmux (`send-keys`, `kill-pane`, `split-window`, `new-window`) and ghostty focus via AppleScript. Rendering is ls.go (tables, grouping modes, colors) and viewer.go (bubbletea TUI that re-runs discovery on a tick and reuses ls.go's group/row builders).

Jobs and cells are a separate subsystem layered on top of the above. A job (job.go) is one `<slug>.job.md` file in `~/Documents/pluto`, parsed strictly into a five-state board model — draft · queued · working · blocked · done — that kraken derives, never hand-writes: `status.go`'s `deriveState` combines the job file's own `state` with the live-ness and transcript-mtime freshness of the job's cell (`podLastActivity`, off `~/.claude/projects/<worktree>/*.jsonl`) to decide working vs. blocked, and `status.go`'s `pendingQuestion` pulls any still-open `AskUserQuestion` tool call straight out of zeus' transcript for the web board and iOS app to render as answer buttons. A cell (cell.go) is one job's runtime: a dedicated git worktree plus a single tmux pane running **zeus**, the sole per-job agent — there is no separate relay agent and no escalation-bus file; zeus reports status by simply saying it in its own pane, which the board surfaces as the cell's detail line. Zeus' system prompt (crew.go) branches on job `type`: `build` jobs run a `/doc` (spec) → `/send` (from the board, or immediate if `auto-approve: true`) → implement → PR pipeline, delegating slices to hephaestus/athena subagents that zeus itself manages; `info` jobs run the `/info` (explain) pipeline with no PR, accepted by hand (`k done`). Either doc pipeline renders its diagrams via one-shot, per-figure hermes subagents. The daemon (daemon.go) queues ready jobs into cells and reaps merged PRs, owning nothing it can't reconstruct from job files plus discovery after a restart.

Invariants that shape the code:

- **Verify before acting**: discovery output goes stale, so every destructive/targeted action re-checks the pane still holds the expected agent first (`verifySendTarget`, `verifyKillTarget`, `branchGuard`). Preserve this pattern for new actions.
- `SessionIDGuessed` marks session ids inferred from cwd + newest transcript rather than a session file: fine for display, unsafe as a fork/read target.
- `k b` forks via `claude --fork-session -r <session-id>` in a tmux split, inheriting the source's permission mode from its transcript.
- `k k` refuses to kill the current pane and nested (Depth > 0) agents.

Tests (`main_test.go`, `viewer_test.go`) cover the pure functions — parsers, ordering, selection, rendering — by constructing `Peer` values directly; nothing shells out, so they run anywhere.

## Conventions

- Keep it stdlib-only outside the viewer; new dependencies need a strong reason.
- Data sources owned by other programs (session files, transcripts, codex logs) are read defensively — missing/malformed files degrade to blank fields, never errors.
