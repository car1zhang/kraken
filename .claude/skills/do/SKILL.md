---
name: do
description: "Implement the system described by an /aim artifact set produced earlier in this conversation. Resolves any P0 open questions in questions.yaml with the user first, then writes the code. Use when the user asks for /do, \"send it\", \"ship the doc\", \"implement the spec\", \"build what we specced\", or similar phrasing after an /aim artifact set has been created."
---

# Do — Implement An Artifact Set

Take the artifact set produced by [[aim]] earlier in this conversation — `00-overview.md`, its diagram cards, and its section docs — and implement it. The set is the source of truth for scope, components, constraints, and testing. Before writing code, resolve any `P0` entries in the set's `questions.yaml` with the user.

## Preconditions

- An `/aim` artifact set must exist for this conversation: a set root directory (standalone: `~/designs/<slug>/`; in a kraken cell: `<cellDir>/artifacts/`) containing `00-overview.md`. A sibling `questions.yaml` (standalone: `~/designs/<slug>.questions.yaml`; in a cell: `<cellDir>/questions.yaml`) may or may not exist — it's optional, and its absence just means no open questions were raised. Older conversations may instead have a single flattened `~/designs/<slug>.spec.html` / `.explain.html` from the legacy template flow (or `.spec.md` / `.explain.md`) — treat that file itself as the doc in that case.
- If no such artifact exists in this conversation:
  - Do **not** start implementing.
  - Tell the user: "No /aim artifact found in this conversation — run /aim first, then /do."
  - Stop. Do not infer a spec from prior chat messages alone; the artifact set is the contract.

## Workflow

### 1. Locate And Read The Set

- Identify the most recent `/aim` set root mentioned in this conversation (or the explicit path the user passes as an argument).
- Read `00-overview.md` in full, then every `.md` file in the set root other than `00-overview.md` in full (covers `5N-*.md` and any `6N+` section docs), then `questions.yaml` in full if it exists. The set is the source of truth — do not rely on chat memory of what it said. (Diagram cards are supporting detail; skim captions/titles rather than reading every SVG body.)
- If working from a legacy single-file doc instead, read it in full the same way.
- `questions.yaml` is OPTIONAL: its absence means zero open questions for this set — skip straight to implementation (do not refuse or treat a missing file as an error).
- If any section doc this workflow names (`50-components.md`, `51-code-map.md`, `52-testing-plan.md`) — or one referenced elsewhere in the set (e.g. by `51-code-map.md`'s review guide) — is absent, say so explicitly and fall back to `00-overview.md` plus whatever section docs do exist — never invent content to fill the gap. A solo set legitimately has none of the three named docs; that is not an error.

### 2. Resolve P0 Open Questions

- Collect every `questions.yaml` entry with `importance: P0` and no `answer:` key yet.
- For each unresolved P0, ask the user with `AskUserQuestion`. Group related questions into one call (up to 4 questions per call).
- An entry that already has an `answer:` (and `answered-at:`) is a **decision, not a question** — never re-ask it; treat the answer as settled and cite it if relevant. In a kraken cell those two keys arrive as silent file edits from carl answering on the board, not as pane messages — re-read `questions.yaml` before this step to pick up anything answered since the set was last read.
- If there are no unresolved P0 entries, skip straight to implementation.
- Do **not** ask P1/P2 questions unless the user requests it — state assumptions for those if they block progress, and surface them after implementation.

### 3. Record Newly Resolved Decisions

- After P0 answers come in outside a cell (where there's no board to append `answer:`/`answered-at:` for you), edit `questions.yaml` yourself: remove the resolved entry and note the decision in the relevant section doc (an `Assumptions`/`Decisions` note) instead. Leave P1/P2 entries untouched.
- In a kraken cell, never write `answer:`/`answered-at:` yourself — that's kraken's job once carl answers from the board.

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

### 7. Report Back

- One short summary: what shipped, which P0 answers shaped it, what was deferred to remaining `questions.yaml` entries, and what verification was run.
- Do not re-summarize the set itself — link to `00-overview.md`.

### 8. Clean Up Agents

Once verification passes and the report is written, tear down everything spawned during implementation — do not leave agents idling after the task is done:

- **Teammates (TeamCreate)**: send each still-running teammate a shutdown request via `SendMessage` (`type: "shutdown_request"`), wait for them to exit, then clean up the team (e.g. `TeamDelete`) once all members have shut down.
- **Background subagents / tasks (Agent, Workflow)**: check for still-running background tasks (`TaskList` / task notifications) and stop any that are no longer needed with `TaskStop`.
- Do this even when implementation failed or was cut short — a partial run must not leave orphaned agents behind.
- Exception: if a teammate is mid-way through work the user explicitly asked to keep running (e.g. a long verification job), say so in the report instead of killing it.

## Boundaries

- /do is for implementing an existing doc, not for re-spec'ing. If the user wants scope changes mid-implementation, update the doc first (or hand back to [[aim]]) before continuing to code.
- /do does not create branches, commits, or PRs unless the user asks. Follow repo git conventions (or a personal `git` skill, if one exists in this session) for branches and commits, and use [[pr]] for PRs.
- If the doc is in explain mode (describes an existing system), /do should generally do nothing — explain docs document current state, not future work. Confirm with the user before treating an explain doc as an implementation target.
