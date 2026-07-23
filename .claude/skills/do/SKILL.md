---
name: do
description: "Implement the system described by an /aim spec or explain artifact produced earlier in this conversation. Resolves any P0 open questions with the user first, then writes the code. Use when the user asks for /do, \"send it\", \"ship the doc\", \"implement the spec\", \"build what we specced\", or similar phrasing after an /aim artifact has been created."
---

# Do — Implement A Spec Artifact

Take a spec/explain artifact produced by [[aim]] earlier in this conversation and implement it. The doc is the source of truth for scope, components, constraints, and testing. Before writing code, resolve any `P0` items from the doc's Open Questions table with the user.

## Preconditions

- An `/aim` artifact must exist for this conversation. It is usually an HTML file under `~/designs/<slug>.spec.html` or `~/designs/<slug>.explain.html` created by the [[aim]] skill (older artifacts may be `.spec.md` / `.explain.md`).
- If no such artifact exists in this conversation:
  - Do **not** start implementing.
  - Tell the user: "No /aim artifact found in this conversation — run /aim first, then /do."
  - Stop. Do not infer a spec from prior chat messages alone; the doc file is the contract.

## Workflow

### 1. Locate The Doc

- Identify the most recent `/aim` artifact path mentioned in this conversation.
- Read it in full. The doc is the source of truth — do not rely on chat memory of what it said.
- If the user passes an explicit path as an argument, use that instead.

### 2. Resolve P0 Open Questions

- Find the `Open Questions` table in the doc.
- Collect every row with `Importance` = `P0`.
- For each P0 question, ask the user with `AskUserQuestion`. Group related questions into one call (up to 4 questions per call).
- Skip questions the user has already answered earlier in this conversation; cite the prior answer instead of re-asking.
- If there are no P0 questions, skip straight to implementation.
- Do **not** ask P1/P2 questions unless the user requests it — state assumptions for those if they block progress, and surface them after implementation.

### 3. Update The Doc With Resolved Decisions

- After P0 answers come in, edit the doc:
  - Move resolved P0 rows out of Open Questions into an `Assumptions` or new `Decisions` section, noting the answer.
  - Keep P1/P2 rows in Open Questions untouched.
- This keeps the doc the durable record once implementation starts.

### 4. Plan The Implementation

- Build a TaskCreate plan grouped by the doc's `Components` table. One task per component is a good default.
- Honor the doc's `Scope`, `Non-Goals`, and `Constraints And Principles` strictly — do not expand scope.
- Use the doc's `Low-Level Code Map` / `Where Things Live` as the file-targeting guide. Verify those paths still exist before relying on them.
- For complex implementations (4+ files or multi-service), spawn a TeamCreate per the user's global preference rather than serial work in the main thread.
- Use **Sonnet teammates for mechanical implementation** (`model: sonnet`): well-specified component tasks where the doc already pins the design, file targets, and test plan. Reserve Opus/default-model teammates for tasks needing real judgment (ambiguous integration points, cross-cutting refactors, design gaps the doc left open). The lead agent stays on the default model to plan, review teammate output, and integrate.

### 5. Implement

- Follow the per-component test plans from the doc (unit / integration / e2e) as you write each component.
- Match the project's existing conventions; do not introduce new abstractions the doc did not call for.
- Mark each TaskCreate task complete as soon as it is done.

### 6. Verify Against The Doc

- Run the doc's `Testing Plan` checks that are practical locally (unit tests, type checks, targeted integration tests).
- For UI changes, open the feature in a browser before reporting done.
- If a check is not runnable locally, say so explicitly.

### 7. Report Back

- One short summary: what shipped, which P0 answers shaped it, what was deferred to remaining open questions, and what verification was run.
- Do not re-summarize the doc itself — link to it.

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
