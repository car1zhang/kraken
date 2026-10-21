---
name: direct
description: "KRAKEN-LEVEL copy (repo-owned, materialized into cell worktrees). A modifier used IN TANDEM with the three phase skills — /aim, /info and /do — for quick jobs: the phase skill still runs as written, and direct removes the parts that need subagents (researcher fan-outs, hermes diagram rendering, the hephaestus/athena crew and blind test authorship) and the review rounds, so zeus does every step itself and the review runs only when the operator asks. Use when a job spec opens with /direct, or when the operator types /direct into the cell's pane — optionally naming a phase (/direct aim, /direct info, /direct do) and optionally the word `review` to opt the review round back in."
---

# Direct — The Phase Skills, Minus The Crew

`/direct` is not a fourth phase and not a shorter procedure. You still read and follow `.claude/skills/aim/SKILL.md`, `.claude/skills/info/SKILL.md` and `.claude/skills/do/SKILL.md` exactly as the brief says, including their `Inside A Kraken Cell` sections and their closing lines. What direct changes is a fixed list of DELETIONS from those skills: every step that exists to brief, wait on, arbitrate or review a subagent is skipped, and the work it would have delegated is yours. The goal is fewer tokens and a faster job, and the two things that buy that are: spawn nothing, and skip the rounds.

## When It Applies, And For How Long

- A job spec that OPENS with `/direct` is a direct job from its first turn: Phase 1 (if the brief's doc-gating rule says one runs at all) and Phase 2 are both direct. The word after `/direct` may name the phase — `/direct aim`, `/direct info`, `/direct do`, or with the slash kept (`/direct /do`, which is what two composer chips produce) — or not: a bare `/direct` leaves the phase choice to the brief's doc-gating rule, exactly as a spec without it would. Any other word there (`/direct review`, `/direct pr`) names no phase and is read as bare `/direct` plus that word's own meaning (so `/direct review` is bare direct with the review opt-in on; a word with no meaning here is ignored).
- {{operator}} may also type it into your pane at any gate: `/direct do` at the `/do` gate makes Phase 2 direct even when Phase 1 ran with the crew; `/direct aim` or `/direct info` makes the next doc round direct. Typed while a crew round is still in flight, it takes effect at the next gate: the round in flight closes under the rules it was dispatched with (its agents are stopped on acceptance, not abandoned), and the next phase is the first direct one.
- Direct is STICKY for the rest of the job: a plain `/do` after a direct Phase 1 is a direct `/do`. {{operator}} leaves it only by asking for the full process in words ("full /do", "with the crew", "run the pipeline"), never by omission. Because a pane-typed `/direct` leaves no record outside this conversation, say `direct` (and whether review was requested) in every final message, and in the "Where we are" line of any `80-handoff.md` you write, so a compacted or resumed zeus does not fall back to the crew.
- **Review is off by default and on by request.** `/direct do review`, `review` as the word after the phase, or an explicit ask in the spec or a steer ("with review", "run the review round") turns the review round back on — see the `/do` deltas below. A reply at the manual-test gate that says {{operator}} will review it themselves is not that ask. Nothing else in a direct job spawns an agent.
- `solo: true` on a direct job makes the review opt-in UNAVAILABLE: solo means no teammates and no subagents at all, and that flag outranks a `review` word. (A solo job without `/direct` never reads this skill; its own no-crew rules are the brief's and the phase skills'.)

## The Rule, Stated Once

**No crew, no subagents.** No researcher subagents, no `hermes-<id>`, no `hephaestus`, no `athena-*` (the review opt-in below is the ONE exception), no `Explore`, no `Workflow`. You read the code, you write the artifacts, you implement, you write the tests, you verify. This overrides the brief's whole crew contract for the rest of the job — the "delegation is earned" gate, the hephaestus/athena split, and its rule that the athena review runs every time: here it runs only when asked. **Everything else in the brief still binds** — the final-message TLDR, `kraken load set|clear` on every idle turn, the `kraken questions get` / `kraken feedback get` re-read on every resume (a feedback note is acted on and named in your final message, never marked — that column is the nightly pass' alone), `kraken mm get` before anything {{operator}} reads, flags, the never-declare-done rule, the self-destruction guard. Direct replaces the brief's crew and review paragraphs, and nothing else of it. Every md artifact still opens with a one-liner stating its main message and never hard-wraps prose (one paragraph, one bullet, one table row is ONE source line, however long); the set root stays operator-facing only.

The deltas below are the complete list. A step of a phase skill that is not named here runs exactly as that skill says.

## `/aim` Under Direct

| aim says | direct does instead |
|---|---|
| Fan out parallel researcher subagents for broad investigations (step 2), waiting on their report files | You research it yourself, with the same question-resolution pass; nothing is spawned and there is nothing to wait on. |
| Delegate every SVG diagram to a `hermes-<diagram-id>` teammate; `≥1` worked-example diagram card required in spec mode; fragments, `lint_svg.py`, the done-test, the wrap | **No diagram cards at all.** The set is `00-overview.md` plus the section docs; the worked-example and data-in-natural-shape content goes into the section docs as tables and traced examples in prose. Nothing stages in `<cellDir>/doc/fragments/`, and you never author SVG yourself. UI mockups are unchanged — aim already has you build those directly. |
| The hold on expensive artifacts behind an open question (dispatch no content-dependent figure before the answer) | Moot — nothing expensive is rendered. Questions still gate nothing: bake in the default and keep going. |
| A diagram {{operator}} asks for by name in a later round | Decline it in your final message, say that a diagram takes the crew (a `hermes-<id>` teammate under aim's delegation rule) and that this job is direct, and offer a full `/aim` round for that figure. `solo: true` forbids the renderer outright. |

Everything else — the artifact set shape, the required section docs in spec mode (`50-components.md`, `51-code-map.md`, `52-testing-plan.md`), the questions rows and their verbs, investigate-before-asking, the sibling-work check, the closing line `doc ready for review: …` with `+0 diagrams` — is aim's, unchanged.

## `/info` Under Direct

| info says | direct does instead |
|---|---|
| Fan out parallel researcher subagents for broad sweeps (workflow step 2) | You research it yourself. |
| Ship the one `1N-<id>.html` diagram card carrying the mental model, rendered by a `hermes-<id>` teammate | **No diagram card.** The mental-model row of the overview carries the picture as a small table, and the "go deeper" menu says a diagram was skipped under direct so the reader can ask for one — which then takes a full `/info` round with the crew, offered the same way as aim's named-diagram row above. |

Everything else — the first-pass table, one artifact per later round, no open questions ever, the closing line `explanation ready: …` with `+0 diagrams` — is info's, unchanged.

## `/do` Under Direct

| do says | direct does instead |
|---|---|
| Step 4's delegation bullets: freeze `50-contract.md`, dispatch hephaestus and the two blind athena authors in one message, the barrier, the join, arbitration into code bug / test bug / contract defect | **Skipped whole.** do already says these apply only when you DELEGATE, and a direct job never does: there is no contract to freeze against yourself. You take the un-delegated path do's step 5 describes — implement it, and write its tests yourself from `50-components.md`'s test plans or the spec's acceptance claims. When an earlier crew round on this slug left blind tests in the repo, they are still asserting: re-cover what they pin or delete the ones this change deliberately obsoletes, never leave them failing. |
| The review round (step 4's review bullets and the cell section's athena-logic / athena-edge / athena-style binding), with its fix rounds and re-reviews | **Skipped unless requested.** When it was NOT requested, write `review skipped (direct)` in the build report's Verification row and go on to step 6. When it WAS: ONE round, run as do describes it — `athena-logic`, `athena-edge`, `athena-style` as three named teammates on `model: opus`, dispatched in one message as three fresh Agent calls (do's "ONE new Agent call" binding resumes two blind authors, which a direct job never has), each briefed with one dimension, the verdict bar verbatim (*BLOCKING means you would not ship the tree as it stands; CLEAR means every remaining item is optional*), its own scratch report path outside the repo, and the zsh dialect line; the tree is frozen while it runs; each verdict is the report file read IN FULL. Then fix every blocking finding yourself and re-review ONCE with only the flagging variant(s). That re-review closes the round whatever it says: a finding still open after it goes into the build report as open, not into a third dispatch. `TaskStop` each reviewer once its verdict is CLEAR or once that single re-review has reported. This cap outranks do's gate rule that a behavioural revision reopens the review: on a direct job a gate revision is pushed to the PR and recorded in the build report as unreviewed, never dispatched. |
| Step 7's join row in `90-build-report.md` | Reads `nothing delegated (direct)`; say in the same row that test authorship was not split. |
| Step 10, clean up agents | Nothing to stop unless the review opt-in ran; then stop the reviewers as above. |

Everything else — reading the set in full (every `.md` in the set root, whoever wrote it), resolving P0s, the un-delegated implementation rules (Edit tool never Bash, destructive commands alone, comments assert only what you checked, grep a retired meaning once, never `git stash` or a probe file in the worktree), the CI-scope verification gate, the build report's every row, the mid-build question round (step 5: a direct job files its rows and stops on the P0s exactly as written, since no crew is involved in it), the PR step (step 8: branch, commit and PR rules, every revision pushed to that same PR), and the manual-test-gate line `manual test ready: …` that follows it — is do's, unchanged.

## Inside A Kraken Cell

This skill only ever runs in a cell, and it never ends a phase: the closing line is the phase skill's, and so is everything it says to do after it. Three reminders, because they are the three things a one-agent job drops:

- The set root is the brief's SET ROOT (`<cellDir>/artifacts/`), the only board-visible directory. Nothing is staged in `<cellDir>/doc/fragments/` — a direct job renders no fragments, ever.
- `kraken load set|clear` and the final-message TLDR fire on every idle turn exactly as in a crew job; a direct job that goes quiet with no estimate is a blocked card nobody can rank.
- Never declare the job done — accepting or merging is {{operator}}'s move, never yours.
