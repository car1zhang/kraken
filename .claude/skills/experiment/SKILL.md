---
name: experiment
description: "KRAKEN-LEVEL copy (repo-owned, materialized into cell worktrees). The experiment workflow: run a task end to end — an experiment, a hill climb, a benchmark, a data pull, a one-off investigation that needs things RUN rather than explained — then publish the results for the operator to review, and stop. No doc phase, no /do, no review crew, no PR unless the spec asks for one. Use when a kraken job spec invokes /experiment."
---

# Experiment — Run It, Publish The Results, Stop

> **Kraken-level copy** — loaded from `.claude/skills/experiment/` in this worktree. If the harness
> header above says `Base directory ... ~/.claude/skills/experiment`, a personal copy shadowed this
> one (personal beats project on a name collision).

The job spec is the whole contract: what to run, and what {{operator}} wants to learn from it.
The deliverable is the RESULT, not code quality and not a design. Spend the tokens on the work.

## What this workflow skips

- **No doc phase.** No /aim spec, no /info overview, no questions rows. Plan inline, in your head.
- **No /do and no review crew.** Never spawn athena (or any reviewer, judge, or second-opinion
  agent). Never run a manual-test gate. This overrides the crew rules in your brief.
- **No PR** unless the spec explicitly asks to ship code. Code you change to make the experiment
  run is committed on the cell branch and listed in the results; that is enough.

## How to run it

1. **Check for sibling work first.** `kraken job list`, then `kraken artifacts get <slug>` on any
   job naming the same system — a finished sibling may already hold the setup or the numbers.
2. **Read what the operator knows** — `kraken mm get --q "<subject>"`, as your brief says — so the
   results are written for them.
3. **Run the task.** Do it yourself. Spawn helpers only when independent runs genuinely parallelize
   (several hill climbs, several orgs, several configs at once): each is a NAMED teammate on
   `model: opus`, briefed to run its slice, write its numbers to a file, and report — never to
   review anyone else's work. On a `solo: true` job, no helpers at all.
4. **Long steps keep a clock.** Before any step that takes more than a minute, write down its start
   time and expected duration, give it a hard timeout, and check real progress (log tail, file
   modification time) rather than assuming. A step past twice its expected time with no new output
   is hung: kill it, say so, and re-scope.
5. **When something fails,** fix what is in your power and keep going. When it cannot be fixed
   (missing credential, service down), publish what you have, say exactly what is missing, and stop.

## Publish the results

Always write `00-results.md` in the set root (`<cellDir>/artifacts/`, the only board-visible
directory). It is the first thing {{operator}} opens, so put the answer on the first screen:

- **Summary first** — three sentences at most: what was run, what was found, what to do next.
- **Numbers live in tables**, never in prose. Comparisons are tables.
- **How to reproduce** — the exact commands, the commit on the cell branch, the inputs used.
- **Where the raw outputs are** — file paths, run ids, table rows.
- **Code changed** — each file and one line on why, if any.

Then publish wherever else the spec names, and add a `<name>.link.md` in the set root for each:

- **Obsidian** — a note in {{operator}}'s vault following their report rules (their global
  `CLAUDE.md` names the vault and the rules); the link file holds the `obsidian://` URL.
- **A review UI or upload** (board review page, a climb upload, a dashboard) — do the upload with
  the tool or command the spec or repo provides; the link file holds the URL to open.

Never run `open`: a cell has no display. The board renders the artifacts.

## Ending a round

End with one line, then stop (end the turn):

`results ready: <set root>/00-results.md — <the finding in one sentence>`

Do not ask questions along the way: pick the most defensible default and say which way you went in
the results. `AskUserQuestion` is only for a blocker that makes the run worthless if guessed wrong.
{{operator}}'s reply, if any, is the next round — re-run, extend, or publish elsewhere, then end
the same way. Never declare the job done; accepting it from the board is {{operator}}'s move.
