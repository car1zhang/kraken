# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

kraken (`k`) is a macOS CLI/TUI for managing live Claude Code and Codex sessions across tmux: list them (`k ls`), watch them (`k v`), jump to one (`k o`), message one (`k s`), read its transcript tail (`k r`), fork it (`k b`), kill it (`k k`), spawn new shells/claudes (`k n`/`k c`). Run `k help` for full usage.

## Commands

- `make build` — build the `kraken` binary
- `make install` — build + install to `~/.local/bin` (with `k` symlink) + zsh completion. **Never `cp` over the installed binary directly**: macOS SIGKILLs running processes whose backing binary changes (code-signature invalidation). The Makefile copies to a temp name and `mv`s (atomic rename) for this reason.
- `go test ./...` — run all tests
- `go test -run TestName ./<pkg>` — run a single test (root tests: `.`)
- `go vet ./... && gofmt -l .` — lint

## Architecture

The binary is split into a strict dependency chain of packages, each owning one layer: root `package main` (CLI dispatch, `k ls` rendering, the bubbletea viewer) + `util/` (stdlib-only helpers) + `core/` (discovery, `Peer`, tmux primitives, claude spawning, transcript reading, usage) + `jobs/` (job files, cells, crew briefs, status derivation, archive, daemon) + `api/` (JSON handlers + wire types, `apiContract`, `api.Register`) + `web/` (embedded SPA in `web/static/`, artifact serving, bind logic, `web.Serve`). The dependency DAG is `util ← core ← jobs ← api ← web`, and root imports all of them; importing upward is forbidden (and impossible without an import cycle). The only direct dependency is bubbletea (viewer TUI); all else is stdlib. External state is shelled out to: `tmux`, `ps`, `lsof`, `git`, `osascript`.

The core type is `Peer` (core/peer.go): one live agent process in a tmux pane, an in-process teammate known only from a team config (`Messageable=false`), or an agent-free shell pane (`Role "shell"`).

Data flow, shared by `k ls`, the viewer, and completion:

1. **Discovery** (core/discover.go): `DiscoverAll()` snapshots tmux panes (`tmux list-panes -a`) and the full process table (one `ps` call), then walks each pane's process subtree for claude/codex processes. Agents in a pane are listed deepest-first: index 0 is the foreground agent — the one `tmux send-keys` actually reaches; deeper ones are nested subagents. Enrichment comes from `~/.claude/sessions/<pid>.json` (session files Claude Code maintains), `~/.codex/session_index.jsonl` + lsof for codex, transcript files under `~/.claude/projects/<munged-cwd>/`, and `git`/`lsof` for branch/cwd. lsof is the slow path — results are memoized in package-level caches (`cwdCache`, `codexSessionCache`) because the viewer polls discovery on a tick.
2. **Refinement + nesting** (core/peer.go): `refineStatus` peeks pane content via `tmux capture-pane` to detect blocked/question prompts; `nestTeams` attaches teammates to their leads using `~/.claude/teams/*/config.json`; `OrderAll` produces the single flat numbering that every selector references.
3. **Selection** (core/select.go): `SelectPeer` resolves a user selector — index, `.` (current pane), exact pane id (`%3`), session-uuid prefix (≥4 chars), or substring match — against that ordering.
4. **Action or render**: actions (core/actions.go, core/branch.go) drive tmux (`send-keys`, `kill-pane`, `new-window`) and ghostty focus via AppleScript, called from root's thin cmd wrappers (cmd_actions.go, cmd_branch.go); the one exception is `k b`'s `tmux split-window`, issued directly from cmd_branch.go. Rendering is ls.go (tables, grouping modes, colors) and viewer.go (bubbletea TUI that re-runs discovery on a tick and reuses ls.go's group/row builders).

Jobs and cells are a separate subsystem layered on top of the above. Two orthogonal extras sit beside the five states: **archived** jobs (jobs/archive.go) are job files moved into `~/Documents/pluto/archive/` — out of the active namespace entirely (slug freed, invisible to the daemon), numbered `foo-2.job.md` on name conflicts, restorable with `k unarchive` (state preserved; refused while the slug is taken) and hard-deletable only from the archive. **Reactivation** (`k reactivate`, jobs/cell.go) resurrects a done job into its original zeus session: kraken stamps zeus' session id into the job file (`session:` frontmatter) from the daemon's first tick after the cell comes up, so a done job can later be resumed with `claude -r` in a recreated worktree at the same path; the `carl/<slug>` branch recycles (reset off the base once fully merged), falling back to `carl/<slug>-rN` (`run:` frontmatter) only when leftover unmerged commits would be orphaned. A job (jobs/job.go) is one `<slug>.job.md` file in `~/Documents/pluto`, parsed strictly into a five-state board model — draft · queued · working · blocked · done — that kraken derives, never hand-writes: `jobs/status.go`'s `deriveState` combines the job file's own `state` with the live-ness and a ROLL-CALL of the cell's claudes (`cellActive`: the zeus pane peer's own status, any nested subagents in its session, and non-lead team members cross-checked against live tmux panes — any one alive-and-running votes the cell into working, else it reads blocked) to decide working vs. blocked, and `jobs/status.go`'s `FindPendingQuestion` pulls any still-open `AskUserQuestion` tool call straight out of zeus' transcript for the web board and iOS app to render as answer buttons. A cell (jobs/cell.go) is one job's runtime: a dedicated git worktree plus a single tmux pane running **zeus**, the sole per-job agent — there is no separate relay agent and no escalation-bus file; zeus reports status by simply saying it in its own pane, which the board surfaces as the cell's detail line. Zeus' system prompt (jobs/crew.go) branches on job `type`: `build` jobs run a `/doc` (spec) → `/send` (from the board) → implement → PR pipeline, delegating slices to hephaestus/athena subagents that zeus itself manages; `info` jobs run the `/info` (explain) pipeline with no PR, accepted by hand (`k done`). Either doc pipeline renders its diagrams via one-shot, per-figure hermes subagents. Cells and PRs are many-to-one: the daemon's PR tracker records every PR shipped from the cell branch into the job's kraken-owned `prs:` frontmatter list (url + state; a merge is a notification + board chip, NEVER a teardown trigger — teardown lives only behind carl's explicit accept via `k done`/board Done, and there is no `k reap`), and zeus recycles `carl/<slug>` off origin/main after each merge for follow-up PRs. The daemon (jobs/daemon.go) queues ready jobs into cells, tracks PRs, and flags dead cells, owning nothing it can't reconstruct from job files plus discovery after a restart.

Invariants that shape the code:

- **Verify before acting**: discovery output goes stale, so every destructive/targeted action re-checks the pane still holds the expected agent first (`checkSendTarget`, `verifyKillTarget`, `BranchGuard` in core/actions.go and core/branch.go). Preserve this pattern for new actions.
- `SessionIDGuessed` marks session ids inferred from cwd + newest transcript rather than a session file: fine for display, unsafe as a fork/read target.
- `k b` forks via `claude --fork-session -r <session-id>` in a tmux split, inheriting the source's permission mode from its transcript.
- `k k` refuses to kill the current pane and nested (Depth > 0) agents.

Tests (root `main_test.go`, `viewer_test.go`, plus a `_test.go` file alongside its source in each of util/core/jobs/api/web) cover the pure functions — parsers, ordering, selection, rendering — by constructing `Peer` values directly; nothing shells out, so they run anywhere.

New code goes in the package matching its concern — CLI/rendering in root, tmux/discovery primitives in core, job/cell lifecycle in jobs, wire handlers in api, HTTP serving in web — and only exports what actually crosses a package boundary. The `util ← core ← jobs ← api ← web` direction is load-bearing: a lower package must never import a higher one, since that would both violate the layering and create an import cycle.

## Filesystem Conventions

Every path in the binary derives from `os.UserHomeDir()` via `util.Home()` — never hardcode `/Users/<name>` (in Go, prompts, or docs), so the same setup works on any Mac whose home directory follows this layout. The directories kraken assumes, and where each is wired in:

| Path | Owner | Code |
|---|---|---|
| `~/Documents/pluto` (job files `*.job.md`, `archive/`) | user's pluto notes convention | `jobs/job.go` (`JobsDir`) |
| `~/Developer` (git checkouts jobs may target) | user convention | `jobs/job.go` (`ResolveRepo`), `jobs/board.go` (`RepoCandidates`) |
| `~/.kraken` (`cells/`, `daemon.json`) | kraken-owned | `jobs/cell.go`, `jobs/daemon.go` |
| `~/.kraken/uploads` | kraken-owned (per-job image uploads) | `jobs/uploads.go` |
| `~/.kraken/logs` (unified event log, size-rotated JSONL) | kraken-owned | `util/eventlog.go` |
| `~/.claude` (`sessions/`, `projects/`, `teams/`, `.usage-cache.json`) | Claude Code | `core/discover.go`, `core/peer.go`, `core/usage.go` |
| `~/.claude/skills/doc/assets` (doc templates, `hermes-contract.md`, `lint_svg.py`) | user's doc skill; zeus briefs reference these by path | `jobs/crew.go` |
| `~/.codex/session_index.jsonl` | Codex | `core/discover.go` |
| `~/.config/nvim/lua/config/pluto.lua` | user nvim config (`{{name}}` expansion coupling) | noted in `jobs/job.go` |
| `~/.local/bin` (`kraken` + `k` symlink) | install target | `Makefile` |

When adding a path, express it home-relative through `util.Home()` and add it to this table.

## Conventions

- Keep it stdlib-only outside the viewer; new dependencies need a strong reason.
- Data sources owned by other programs (session files, transcripts, codex logs) are read defensively — missing/malformed files degrade to blank fields, never errors.
- **API contract**: any change to JSON served to or accepted from the iOS app/web UI (api/*.go, web/serve.go, jobs/daemon.go, jobs/job.go) lands in the same commit as `ios/API.md`, `ios/Kraken/Models.swift`, and `api/contract_test.go`. `apiContract` (api/board.go) bumps only on breaking changes — a key retyped, renamed, or removed — mirrored by `expectedContract` in `APIClient.swift`; additive keys never bump it.
