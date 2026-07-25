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

- Build a TaskCreate plan grouped by `50-components.md`'s component table. One task per component is a good default.
- Honor the set's scope/non-goals/constraints section doc(s) strictly — do not expand scope.
- Use `51-code-map.md` (where things live, review guide) as the file-targeting guide. Verify those paths still exist before relying on them.
- For complex implementations (4+ files or multi-service), spawn a TeamCreate per the user's global preference rather than serial work in the main thread.
- **Every teammate runs on Opus** (`model: opus` — Claude Opus 5): the mechanical component tasks the set already pins down as much as the judgment-heavy ones (ambiguous integration points, cross-cutting refactors, design gaps the set left open). Never drop a teammate to sonnet or haiku to save tokens, and never leave the model unset. The lead agent is on Opus too — it plans, reviews teammate output, and integrates.

### 5. Implement

- Follow the per-component test plans from `50-components.md` (unit / integration / e2e) as you write each component.
- Match the project's existing conventions; do not introduce new abstractions the set did not call for.
- Mark each TaskCreate task complete as soon as it is done.

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

Once verification passes and the report is written, tear down everything spawned during implementation — do not leave agents idling after the task is done:

- **Teammates (TeamCreate)**: send each still-running teammate a shutdown request via `SendMessage` (`type: "shutdown_request"`), wait for them to exit, then clean up the team (e.g. `TeamDelete`) once all members have shut down.
- **Background subagents / tasks (Agent, Workflow)**: check for still-running background tasks (`TaskList` / task notifications) and stop any that are no longer needed with `TaskStop`.
- Do this even when implementation failed or was cut short — a partial run must not leave orphaned agents behind.
- Exception: if a teammate is mid-way through work the user explicitly asked to keep running (e.g. a long verification job), say so in the report instead of killing it.

## Boundaries

- /do is for implementing an existing doc, not for re-spec'ing. If the user wants scope changes mid-implementation, update the doc first (or hand back to [[aim]]) before continuing to code.
- /do does not create branches, commits, or PRs unless the user asks. Follow repo git conventions (or a personal `git` skill, if one exists in this session) for branches and commits, and use [[pr]] for PRs.
- If the doc is in explain mode (describes an existing system), /do should generally do nothing — explain docs document current state, not future work. Confirm with the user before treating an explain doc as an implementation target.
