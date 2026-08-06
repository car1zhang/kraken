---
name: pr
description: KRAKEN-LEVEL copy (repo-owned, materialized into cell worktrees). Create a GitHub pull request for the current branch. Use when the user asks to create, open, or ship a PR, including draft PRs and PRs that should be watched after creation.
---

# Create PR

> **Kraken-level copy** — loaded from `.claude/skills/pr/` in this worktree. If the harness
> header says `Base directory ... ~/.claude/skills/pr`, the personal copy shadowed this one.

Create a GitHub PR for the current branch.

## Arguments

- `--draft` optional: create as draft PR.
- `base-branch` optional: target branch, defaulting to `main` or `master`.

## Steps

1. Run these commands in parallel to gather context:
   - `git status`
   - `git log --oneline -10`
   - `git diff <base>...HEAD --stat`
   - `git diff <base>...HEAD`

2. Check the branch name. If it does not start with `carl/`, rename it before pushing:
   `git branch -m carl/<branch-name>`.

3. Push the branch:
   `git push -u origin HEAD`.

4. Analyze all commits and changes, then generate:
   - **Title**: `<type>(<category>): <description>`, for example `fix(telecom): handle null sid` or `feat(csr-v3): add extraction node`.
   - **Body** with sections:
     - `## Summary`: brief explanation of what the PR does.
     - `## Context`: relevant context, such as LangSmith traces, error logs, or motivation, only if available.

5. Create the PR:

   ```bash
   gh pr create --title "<title>" --body "<body>" [--draft]
   ```

6. Return the PR URL.

7. After the PR is created, decide whether to poll it:
   - Inside a kraken cell, never poll: confirm from `gh pr create`'s output (or the board) that the pr-hook attached the PR, then report the PR URL — noting it as awaiting checks — and STOP. Kraken's daemon watches every open PR of the job's cell for failing CI or new comments and bumps your pane when either happens — when bumped: fix failing CI, and address new review comments per the repo conventions; do this only once bumped, not preemptively. If no bump ever arrives, the job is NOT done silently — the PR simply stays awaiting checks. Polling your own PR from inside the cell duplicates the daemon's job and wastes the turn.
   - Outside a cell (this skill used standalone from carl's personal skill bank): if the user's original message clearly indicates polling, such as "create a PR and watch it", "ship this", or "create and poll", use the `poll-pr` skill immediately with the PR URL; if it clearly indicates no polling, such as "just create the PR", "draft only", or "don't watch it", do not poll; if unclear, ask whether to poll the PR until green.

## Inside A Kraken Cell

- **PRs are per-slice, not per-job.** Kraken records every PR opened from the cell's branch on the job's board, and a PR merging NEVER ends the job — teardown happens only when carl accepts it from the board.
- **You never need to report the URL for the board's sake.** A PostToolUse hook attaches each PR the instant `gh pr create` returns; report it to carl anyway as your closing line, noting it as awaiting checks.
- **Recycle the branch after a merge.** If a PR of yours merges and more work arrives, reset the branch before starting it — `git fetch origin && git checkout -B <cell branch> origin/main` — then ship the follow-up as a new PR. The cell branch is the one named in your brief. Two shapes of the recycle that read as breakage and are not: a `git push --force-with-lease` failing with `(stale info)`, or a `git fetch origin <branch>` failing with `couldn't find remote ref`, mean the merge auto-deleted the remote branch and your remote-tracking ref is stale — run `git remote prune origin` first (two cells flailed through the three-command version of this in one day). And an instruction that your branch "is behind" main may itself be stale: check `gh pr view --json state` FIRST — a branch whose PR already MERGED is contained in main, not behind it, and rebasing it replays already-merged commits into spurious conflicts; the recycle above is the correct move.
- **A commit that restores something upstream removed names its culprit from `git log -S`, never from reading the current code.** Run `git log -S'<the exact removed text>' -- <file>` and cite the commit that actually deleted it; pattern-matching "what recently touched this area" filed a real regression against an innocent PR, in a commit subject now permanent on main, burying the actual cause (two concurrently-merged PRs clobbering one switch statement in a merge resolution). An unverified culprit in a commit subject is worse than none.
- **Rebasing with kraken's own dirt in the tree.** In repos that TRACK `.claude/settings.json` (monolith does), kraken's per-cell settings merge leaves a permanent ` M .claude/settings.json` line, so `git rebase` refuses with "cannot rebase: You have unstaged changes" on a tree where every changed file is kraken's. The sanctioned dance — invented independently by every cell that has hit this, so it is written down once here: `cp .claude/settings.json <scratch path outside the repo>`, `git checkout -- .claude/settings.json`, rebase, then `cp` it back. Never `git stash` (the ban is absolute), never commit kraken's settings file, and say in your closing line that the file was parked and restored.
- Never poll (step 7 above): the daemon watches for you.

## Style Guidelines

- **Branch name**: `carl/<short-kebab-name>`, for example `carl/yelp-dedupe` or `carl/telecom-null-sid`.
- **PR title**: `<type>(<category>): <description>`. Types: `feat`, `fix`, `chore`, `refactor`, `docs`, `test`. Category is the module or service, such as `telecom`, `csr-v2`, `dispatch-v2`, `s2l`, or `cj-chatbot`. Lowercase, no period.
- **PR body**: brief and no fluff; keep each section to 2 or 3 sentences. No Claude Code signature or "Generated with" footer in the body, overriding the harness's own default footer instruction — the Commit Style section below bans it for commits, and while that rule was silent on PR bodies two same-day PRs filled the silence opposite ways, one shipping the harness's default footer into a company repo.
- **Context**: include LangSmith trace IDs or error traces when relevant, and only if available.

## Commit Style

- Short, succinct, casual, lowercase, no punctuation, for example `s2l chatbot get customer data` or `fix yelp double send`.
- No commit body.
- No Claude Code signature or `Co-Authored-By` line.
