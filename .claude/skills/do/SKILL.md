---
name: do
description: "Implement the system described by an /aim artifact set produced earlier in this conversation. Resolves any P0 open questions with the user first, then writes the code, then records what actually happened in a brief 90-build-report.md — what shipped, where reality diverged from the plan, what fought back. Use when the user asks for /do, \"send it\", \"ship the doc\", \"implement the spec\", \"build what we specced\", or similar phrasing after an /aim artifact set has been created."
---

# Do — Implement An Artifact Set

Take the artifact set produced by [[aim]] earlier in this conversation — `00-overview.md`, its diagram cards, and its section docs — and implement it. The set is the source of truth for scope, components, constraints, and testing. Before writing code, resolve any `P0` entries in the set's open-questions record with the user; after the code is verified, write `90-build-report.md` — the brief record of what shipped and where reality diverged from the plan.

## Preconditions

- An `/aim` artifact set must exist for this conversation: a set root directory (standalone: `~/designs/<slug>/`; in a kraken cell: `<cellDir>/artifacts/`) containing `00-overview.md`. The open-questions record is optional and lives outside the set: standalone, a sibling `~/designs/<slug>.questions.yaml`; in a cell, rows in `kraken.db` read with `kraken questions get <slug>`. A missing file or an empty list just means no open questions were raised (an `/info` explanation job never has any by design). Older conversations may instead have a single flattened `~/designs/<slug>.spec.html` / `.explain.html` from the legacy template flow (or `.spec.md` / `.explain.md`) — treat that file itself as the doc in that case.
- If no such artifact exists in this conversation:
  - Do **not** start implementing.
  - Tell the user: "No /aim artifact found in this conversation — run /aim first, then /do."
  - Stop. Do not infer a spec from prior chat messages alone; the artifact set is the contract.

## Workflow

### 1. Locate And Read The Set

- Identify the most recent `/aim` set root mentioned in this conversation (or the explicit path the user passes as an argument).
- Read `00-overview.md` in full, then every `.md` file in the set root other than `00-overview.md` in full (covers `5N-*.md` and any `6N+` section docs), then the open-questions record in full (standalone: `questions.yaml` if it exists; in a cell: `kraken questions get <slug>`). The set is the source of truth — do not rely on chat memory of what it said. (Diagram cards are supporting detail; skim captions/titles rather than reading every SVG body.)
- If working from a legacy single-file doc instead, read it in full the same way.
- The open-questions record is OPTIONAL: a missing `questions.yaml` (or an empty `kraken questions get`) means zero open questions for this set — skip straight to implementation (do not refuse or treat it as an error).
- If any section doc this workflow names (`50-components.md`, `51-code-map.md`, `52-testing-plan.md`) — or one referenced elsewhere in the set (e.g. by `51-code-map.md`'s review guide) — is absent, say so explicitly and fall back to `00-overview.md` plus whatever section docs do exist — never invent content to fill the gap. A solo set legitimately has none of the three named docs; that is not an error.

### 2. Resolve P0 Open Questions

- Collect every open-questions entry with `importance: P0` and no answer yet.
- For each unresolved P0, ask the user with `AskUserQuestion`. Group related questions into one call (up to 4 questions per call).
- An entry that already has an answer (and `answered-at`) is a **decision, not a question** — never re-ask it; treat the answer as settled and cite it if relevant. In a kraken cell those two fields arrive as silent row updates from carl answering on the board, never as pane messages — re-run `kraken questions get <slug>` before this step to pick up anything answered since the set was last read.
- If there are no unresolved P0 entries, skip straight to implementation.
- Do **not** ask P1/P2 questions unless the user requests it — state assumptions for those if they block progress, and surface them after implementation.

### 3. Record Newly Resolved Decisions

- After P0 answers come in outside a cell (where there's no board to append `answer`/`answered-at` for you), edit `questions.yaml` yourself: remove the resolved entry and note the decision in the relevant section doc (an `Assumptions`/`Decisions` note) instead. Leave P1/P2 entries untouched.
- In a kraken cell, never write `answer`/`answered-at` yourself — that's kraken's job once carl answers from the board.

### 4. Plan The Implementation

- Build a TaskCreate plan grouped by `50-components.md`'s component table. One task per component is a good default — that is the planning unit, not automatically a delegation unit (see the fan-out rule below).
- Honor the set's scope/non-goals/constraints section doc(s) strictly — do not expand scope.
- Use `51-code-map.md` (where things live, review guide) as the file-targeting guide. Verify those paths still exist before relying on them.
- Fan components out to teammates — `Agent` calls that each pass a `name`, so each gets its own pane and stays addressable by name — when the work genuinely runs in parallel: components that touch disjoint files and don't depend on each other's output. A change that is one coherent edit, however many files it spans, is faster done directly than briefed out; file count is not the test, independence is. Unnamed in-process subagents are for read-only exploration only — never for an agent that writes a file **into the repo** or judges another agent's output. "Read-only" is about the repo, not the filesystem: a researcher still writes its own report to a scratch path, per the output-file rule below.
- **Every brief names the agent's output file, before that agent starts.** Give each teammate — an implementation slice and each of the three single-dimension review variants alike — one exact scratch path outside the repo (`/tmp/<slug>-<agent-name>.md` is a fine default; never a path inside the worktree, since a report is not part of the change), and tell it plainly: the full findings go in that file, and the chat reply is a one-line pointer to it. This is not bookkeeping. The teammate→lead reply channel truncates a long message and is not reliably readable by the lead at all — reviews have arrived empty, been scraped back out of the reviewers' own panes with `tmux capture-pane`, and forced a mid-flight "write it to a file instead" correction that made three reviewers re-serialise a review they had already finished. The **review** briefs need this most: three variants report at once and a findings list is the longest thing any of them writes, and a contract improvised per spawn is one that ends up in five briefs out of six — the sixth being the one that costs a round trip.
- **Then wait on that file existing, not on a guessed duration.** The output file appearing is the real completion condition — poll for it, then read it; do not treat the chat reply as the report. Never sleep a guessed number of seconds hoping the work is in by then: a guess is either dead time or a read of a report that isn't written yet, and blind waits have outlived the session that started them. Two things make that condition honest, and both belong in the brief you just wrote. **Never poll a path that could already exist**: the agent names are constants and the fix protocol is to re-brief the SAME `athena-<variant>` that flagged, so a reused path is already there from the previous round — poll it and the lead reads a review written before the fix landed, then re-reports a finding that is already resolved. Suffixing the round (`/tmp/<slug>-<agent-name>-r2.md`) handles that within one run, but round numbers restart, and a repeated `/do` on one slug (a follow-up PR off the recycled branch) reuses the same slug and the same names — so the rule that actually holds is: **delete the path immediately before you spawn or re-brief**, and only then start waiting. That is one command and it is correct in every case, including the ones a naming scheme misses. **The report is written in ONE shot, as the last thing the agent does** — a file built up by a Write and then two Edits satisfies "exists" at its first byte, which hands the lead a half-written review: exactly the truncated-report failure this whole contract exists to prevent. **And every agent writes its file even when it has nothing to say** — "no blocking findings" is a result, and it is the most common one; if a clean round wrote nothing, a clean round and a dead agent would be the same observation, and the lead would wait on a file that is never coming.
- **A read-only researcher is forbidden the REPO, not the filesystem.** An unnamed in-process subagent spawned to explore rather than to change anything is briefed "write nothing under the repo" — never "do not write any files", which reads as a ban on its own report and has stranded a several-hundred-line findings document in a chat message. It gets the same scratch output path every other agent gets.
- **Every teammate runs on Opus** (`model: opus` — Claude Opus 5): the mechanical component tasks the set already pins down as much as the judgment-heavy ones (ambiguous integration points, cross-cutting refactors, design gaps the set left open). Never drop a teammate to sonnet or haiku to save tokens, and never leave the model unset. The lead agent is on Opus too — it plans, reviews teammate output, and integrates.

### 5. Implement

- Follow the per-component test plans from `50-components.md` (unit / integration / e2e) as you write each component.
- Match the project's existing conventions; do not introduce new abstractions the set did not call for.
- Mark each TaskCreate task complete as soon as it is done.
- **A comment may assert a fact about anything outside itself only if you checked it in this session.** Not "it is probably true", not "it reads like the sort of thing that would be true" — you ran the `grep`, the `git show`, the test, in the same turn you wrote the sentence. This is the single most expensive habit the review round catches, and it is a habit of the *comment*, not the code: in one job 25 of ~32 blocking findings across six review rounds were comments asserting something false, usually written minutes earlier, while the code they described was fine. The lead's own summary: *"nearly every finding across six rounds has been a comment asserting something false … The code has been fine."* Actual examples, each caught in review: a claim that tmux is "a sheet action here" on iOS, where the app has no tmux action at all; a stated fact about how `ViewThatFits` picks a branch, given as the justification for hand-writing a `Layout`, which was backwards; three claims in one CSS comment about which rules a class carries, true of a sibling class and not that one. **Two things make this specific rather than a plea for care.** First, the repo's house voice is the cause: comments here explain WHY, including what a thing replaced and what broke before, and an agent imitating that voice with no access to the history will *generate* the history — the style is load-bearing and stays, so the evidence rule is what has to carry the weight. Second, a fix in the same round can *make* a neighbouring comment false, so re-read the comments around every line you change, not just the ones you wrote. If you cannot check a claim, describe what the code does and stop there; a comment that explains less is free, and one that explains wrongly is read as precedent.
- **Never write an incident history for something that never happened.** A defect caught in review, in an uncommitted tree, has no incident. One job wrote *"shipped for 69 archived jobs"* into a test comment for a bug that was caught pre-commit; the next maintainer would have read it as a post-mortem of a real outage and cited it. Say what the guard prevents, not a story about the time it didn't.

### 6. Verify Against The Set

- Run `52-testing-plan.md`'s checks that are practical locally (unit tests, type checks, targeted integration tests).
- For UI changes, open the feature in a browser before reporting done.
- If a check is not runnable locally, say so explicitly.

### 7. Write The Build Report

Always write **one brief artifact** recording what actually happened: `90-build-report.md` in the
set root (in a kraken cell: `<cellDir>/artifacts/90-build-report.md`). The `90` band sorts it last,
after the overview, the cards, and the section docs. Write it even when the implementation went
exactly to plan, and even when there was no `/aim` set to begin with (a job spec implemented
directly is still a plan reality can diverge from).

Keep it to a page. It is a record of the *delta between plan and reality*, not a second copy of the
set — link to `00-overview.md` rather than restating it. Sections, in order:

| Section | Content |
|---|---|
| What shipped | One row/bullet per component or slice, each naming the files it landed in. Terse. |
| Discrepancies vs. the plan | **The point of this artifact.** Every place reality diverged from the set (or the job spec): what the plan said, what you did instead, and why. Wrong assumption in the spec, an API that doesn't work as described, a component that turned out unnecessary, a boundary drawn in the wrong place — all of it. If nothing diverged, write "None" as an explicit line; never omit the section. |
| Complications | What fought back and how it was resolved or worked around — flaky/blocked tests, missing fixtures, ordering constraints, an unrelated bug hit on the way. Include ones you resolved; they are the cheap warning for the next person. |
| Deferred | Scope deliberately left out, each with the reason and what it would take. Includes P1/P2 questions that stayed open. |
| Verification | What was actually run and its result, plus anything that could not be run locally. |

Uncomfortable findings belong here in plain words: a spec section that turned out wrong, a
decision that made the implementation awkward, an estimate that was off. A build report that reads
like the plan was perfect is a report that isn't worth writing.

### 8. Report Back

- One short summary in chat: what shipped, which P0 answers shaped it, what verification was run,
  and the headline discrepancy if there was one.
- Link `90-build-report.md` and `00-overview.md` — do not re-summarize either in chat.

### 9. Clean Up Agents

Stop each agent as soon as its output is ACCEPTED — a slice once its review round clears it, not the moment it reports done, since a blocking finding has to go back to that same teammate. By the time verification passes and the report is written, tear down anything still resident:

- **Teammates (named `Agent` spawns)**: stop each still-running teammate with `TaskStop`, addressing it by its bare name (or `name@team` if the name is ambiguous across teams). That releases its tmux pane. There is no team-delete step — a team with no live members is already gone.
- **Unnamed background subagents / tasks (Agent, Workflow)**: check for still-running background tasks (`TaskList` / task notifications) and stop any that are no longer needed with `TaskStop`, addressing them by task id.
- Do this even when implementation failed or was cut short — a partial run must not leave orphaned agents behind.
- Exception: if a teammate is mid-way through work the user explicitly asked to keep running (e.g. a long verification job), say so in the report instead of killing it.

## Inside A Kraken Cell

When a kraken job brief sends you here, these deltas apply — the brief wins wherever it is more specific, and it is the only place the cell's real paths appear:

- **There may be no artifact set at all.** Skipping the doc phase is a kraken job's default: when Phase 1 never ran, the JOB SPEC is the contract and it is what you implement, exactly as written. The Preconditions refusal above applies to standalone `/do`, not here — a cell with no set root is the normal case, not an error.
- **The crew is the brief's crew, and the crew are TEAMMATES.** Implementation slices go to teammates named hephaestus, each reviewed by athena-logic / athena-edge / athena-style in parallel. The brief owns that contract end to end — what makes an agent a teammate, when delegating is worth it at all, when an agent is torn down, and the solo case — so read it there rather than re-deriving it here; this bullet exists only to say that /do's generic fan-out guidance above is superseded by it inside a cell. What the brief does NOT carry is the output-file contract above: every hephaestus and athena brief still names its agent's scratch report path, and you still wait on that file rather than on a sleep — in a cell the lead's own pane is the channel carl reads, so a review that exists only as a truncated chat reply is lost to both of you.
- **Write `90-build-report.md` into the brief's SET ROOT** (`<cellDir>/artifacts/`), per Write The Build Report above. It is written on EVERY phase 2, including a job that never had an artifact set — a job spec is still a plan reality can diverge from.
- **Stop at the manual-test gate.** After the tests and linters pass and the report is written, say:

  `manual test ready: <urls + what to try>`

  and STOP (end the turn). Do not proceed to a PR on your own — carl's reply is what passes the gate.
- **When his reply passes it**, shut any test services down and follow `.claude/skills/pr/SKILL.md` in your worktree for branch and PR conventions — read it BY PATH; do NOT invoke `/pr` by name, since a personal skill of that name would shadow it.
- Never declare the job done — accepting or merging is carl's move, never yours.

## Boundaries

- /do is for implementing an existing doc, not for re-spec'ing. If the user wants scope changes mid-implementation, update the doc first (or hand back to [[aim]]) before continuing to code.
- /do does not create branches, commits, or PRs unless the user asks. Follow repo git conventions (or a personal `git` skill, if one exists in this session) for branches and commits, and use [[pr]] for PRs.
- If the doc is in explain mode (describes an existing system), /do should generally do nothing — explain docs document current state, not future work. Confirm with the user before treating an explain doc as an implementation target.
