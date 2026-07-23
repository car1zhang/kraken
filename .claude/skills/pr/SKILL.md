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
   - Inside a kraken cell, polling is unconditional: always poll checks until green (fixing failures) — never ask, since there's no board turn to ask on.
   - Outside a cell (this skill used standalone from carl's personal skill bank): if the user's original message clearly indicates polling, such as "create a PR and watch it", "ship this", or "create and poll", use the `poll-pr` skill immediately with the PR URL; if it clearly indicates no polling, such as "just create the PR", "draft only", or "don't watch it", do not poll; if unclear, ask whether to poll the PR until green.

## Style Guidelines

- **Branch name**: `carl/<short-kebab-name>`, for example `carl/yelp-dedupe` or `carl/telecom-null-sid`.
- **PR title**: `<type>(<category>): <description>`. Types: `feat`, `fix`, `chore`, `refactor`, `docs`, `test`. Category is the module or service, such as `telecom`, `csr-v2`, `dispatch-v2`, `s2l`, or `cj-chatbot`. Lowercase, no period.
- **PR body**: brief and no fluff; keep each section to 2 or 3 sentences.
- **Context**: include LangSmith trace IDs or error traces when relevant, and only if available.

## Commit Style

- Short, succinct, casual, lowercase, no punctuation, for example `s2l chatbot get customer data` or `fix yelp double send`.
- No commit body.
- No Claude Code signature or `Co-Authored-By` line.
