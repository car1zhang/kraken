---
name: load
description: "KRAKEN-LEVEL copy (repo-owned, materialized into cell worktrees). The anchor scale behind a job's load estimate: the two 1-10 ladders, the state map for recurring block states, and the two pricing rules. Use when setting or clearing a job's load estimate with `kraken load set|clear` and the state map in zeus' brief does not already name the state you are in."
---

# Load — The Anchor Scale

> **Kraken-level copy** — loaded from `.claude/skills/load/` in this worktree. If the harness header says `Base directory ... ~/.claude/skills/load`, a personal copy shadowed this one (personal beats project on a name collision).

A load estimate is two integers, both 1-10, written at every idle: `--unblock` is what unblocking costs CARL, `--scope` is how big the claude work that reply buys. Carl triages the blocked column by `roi = scope / unblock` — a cheap unblock buying a long session sorts above an expensive one buying a message — so the numbers are only comparable across jobs if every cell reads the same ruler. This file is that ruler.

Your brief already carries the two rules and a one-line state map, and between them they settle almost every estimate. Read the ladders below when you are in a state the map does not name.

## The two rules

1. **`unblock` prices carl's ACTUAL move, never the nominal ask.** Carl ships PRs on trust: a "review the diff and reply" gate costs him a one-word reply (1), not a review (6). Only three things genuinely cost him: driving a live environment, creating credentials/secrets, and decisions he must actually think about. Never price the ask you'd feel comfortable receiving; price what he will actually do.
2. **`scope` prices only the claude work the reply BUYS — strictly the work still ahead.** Never the size of the work under review: at a manual-test gate the reply buys opening a PR (1-2), however large the feature behind the gate. The one state where scope is legitimately big is a spec gate, where the answers unlock the whole build.

And the clear rule, which has teeth: **an estimate exists only when carl is the blocker.** Teammates rendering, reviews running, "nothing needed from you yet" — that is `kraken load clear <slug>`, not a low-unblock estimate. An estimate on a working cell is a false blocked signal. In the working state you write NOTHING beyond that clear — never a `set` with a "working" note followed by a `clear` (one lead did exactly this twice in one session, once as both halves of a single command): the set is a pure no-op that briefly publishes a false 1/1 estimate (the drilldowns render it on any live job), and there is no note channel on a cleared estimate — a status sentence belongs in your pane, not in the load row.

## Unblock — what unblocking costs CARL

| N | Anchor | Recognize it by |
|---|---|---|
| 1 | Mechanical | One-word reply, click a button, merge, run one pasted command. Zero thought. Review-only gates land here. |
| 2 | Read one page, pick | A one-page artifact or short note, then a menu pick or yes/no — recommendations already given. |
| 3 | Answer a few concrete questions | Each with a recommendation attached; a minute or two of thought total. Most spec-gate P0 batches land here. |
| 4 | Local smoke, or a credential | `make install` + click around briefly; or create a secret / account / auth grant (gcloud login, repo secret). |
| 5 | Live E2E drive | A test server, a phone build via TestFlight, a real booking/dispatch watched end to end. |
| 6 | Genuine review | A diff or artifact carl must actually absorb before replying. Rare by policy — claim it only when his eyes are truly required. |
| 7 | Decision batch needing new context | He must read several artifacts or build understanding he doesn't have before he can answer. |
| 8 | Deep decision, real consequences | Architecture pick, irreversible cutover, spend commitment. |
| 9 | Many deep decisions in one sitting | A full set review where most questions are hard and coupled. |
| 10 | A day's thought | The whole design is on him. |

## Scope — the claude work that reply buys (strictly future work)

| N | Anchor | Recognize it by |
|---|---|---|
| 1 | One-message close-out | Open the PR, final report, stop. |
| 2 | One small round | Apply one fix and push; rewrap one artifact; a small edit + re-review. |
| 3 | One focused artifact or round | An /info section doc + diagram; one full review/fix round. |
| 4 | A single-slice implementation | One coherent feature + tests, no teammate fan-out. |
| 5 | A moderate slice | Implementation plus athena review rounds; a day-scale build. |
| 6 | A full /aim set draft | Overview + diagrams + section docs + questions; or a multi-round solo build. |
| 7 | A delegated build | Hephaestus fan-out + blind tests + join + review rounds. |
| 8 | Full phase-2 of a substantial spec | Several slices, several PRs likely. |
| 9 | A multi-PR program | Repeated build/review/ship cycles on one job. |
| 10 | The largest coordinated effort | Many teammates, massive migration, sustained revision over days. |

## State map — mandatory values for the recurring states

These are looked up, not judged: re-deriving them per cell is exactly what made two cells' numbers incomparable. A state not listed here falls back to the ladders above.

| Block state | Set | Why |
|---|---|---|
| Working; teammates running; "nothing needed yet" | `kraken load clear` | Carl is not the blocker. An estimate here is a false signal. |
| PR awaiting checks, nothing else needed | u1 s1 | Merge is the only ask; the daemon watches CI. |
| Manual-test gate, review-only | u1 s1 | Carl ships on trust; the reply buys opening the PR. |
| Manual-test gate, local install + drive | u4 s1 | The install/drive is his real cost; the reply still just buys the PR. |
| Manual-test gate, live E2E environment | u5 s2 | Test server / phone / real transaction — the one gate that genuinely costs him. |
| /info round, go-deeper pick | u2 s3 | Read a page, pick; the pick buys one artifact round. |
| /aim spec gate, P0s block /do | u3 s6-8 | Recommendations attached → cheap to answer; scope is the build the answers unlock, sized by the spec. |
| Credential / secret / auth need | u4, s = the blocked work | The credential is a 4 regardless of what it unblocks; scope carries the real stakes. |
| Mid-build decision | u3 (u7 if he must build context), s = remaining build | Recommendation attached is the difference between 3 and 7. |
| Two asks at once (e.g. PR open + secret needed) | Price the EXPENSIVE ask; name both in the note | Agents used to price whichever ask they focused on, which spread one state across u2-5. |

## Writing it

`kraken load set <slug> --unblock N --scope M --note "<one line>"`, and `kraken load clear <slug>` the moment carl stops being the blocker. Both numbers or neither: a half estimate has no ratio and is refused. The note is one line, and it is where a two-ask state names the ask that is not being priced.

Estimate honestly rather than flatteringly — an inflated scope takes carl's attention from a cell that earned it, and an inflated unblock buries a cheap gate under work he cannot start.
