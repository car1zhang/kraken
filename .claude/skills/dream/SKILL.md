---
name: dream
description: "KRAKEN-LEVEL copy (repo-owned, materialized into cell worktrees). Nightly corroboration pass: read the day's artifact feedback and Claude session transcripts, turn recurring signals into durable improvements to kraken's skills and code, park the rest on the watchlist, and report back. Reads carl's verdicts on watchlist rows and obeys them. Use when a job spec invokes /dream, or when a kraken dream job brief sends you here."
---

# Dream — The Nightly Corroboration Pass

> **Kraken-level copy** — loaded from `.claude/skills/dream/` in this worktree. If the harness
> header above says `Base directory ... ~/.claude/skills/dream`, a personal copy shadowed this one
> (personal beats project on a name collision).

Yesterday produced two kinds of evidence: what carl said about the artifacts (feedback rows) and
what the agents actually did (session transcripts). This pass turns the recurring parts of that
into durable change, and parks the rest where a later night can corroborate it.

The discipline is **corroboration**: one ambiguous signal is evidence, not an instruction.

## The one rule about where things go

Three outputs, three destinations. Getting this wrong is the only mistake here that is hard to undo.

| Output | Destination | May contain |
|---|---|---|
| A hardened change | a PR carl reviews | **the diff only** — never findings, evidence, quotes, or transcript excerpts |
| An observation not yet worth acting on | the `watchlist` table | anything useful; it is local and never committed |
| The night's report | `00-dream-report.md` in the artifacts dir | anything, including carl's own words quoted back to him |

**Findings never enter git.** Not in a commit, not in a PR body, not in a file. The repo gets the
change; the machine keeps the reasoning. This is structural rather than a redaction rule, which is
why it is reliable: a commit contains a diff, and that is all it ever contains.

The corollary is freeing — because the local side never travels, the report and the watchlist can
hold the real thing: actual quotes, real session ids, the prompt text as written. That is what makes
the report worth reading.

## Order of operations

Read in this order. It is not a preference — proposing something carl already settled wastes the
night and teaches him the loop does not listen.

1. **Carl's verdicts and comments** on watchlist rows, and **open dream PRs**. Both are suppression:
   a rule already in flight must not be re-proposed, and a row he has ruled on is already decided.
2. **The rest of the watchlist** — what previous nights are still watching.
3. **The evidence**: the feedback sweep, then the transcripts.

## Reading carl's verdicts

Each watchlist row can carry a verdict from carl and, optionally, a comment.

| Verdict | What it means | What you do |
|---|---|---|
| `fine` | Stop even watching this | Never propose it again. Softer than `declined`, which means "you proposed it and I said no". |
| `fix`, no comment | He wants it done | His asking **is** the explicit instruction — act on it now, no further corroboration needed. |
| `fix` + comment | He wants it done a particular way | **The comment is the specification.** Follow it rather than re-deriving an approach, and say so in the PR body. |

You may also put a **suggested fix** on a row you are not acting on — a concrete proposal for carl to
approve or push back on. Write it into the row's `suggested_fix`; do not act on your own suggestion
until he has turned it into a `fix`.

Never write `verdict`, `comment`, or `commented_at` yourself. Those are carl's, exactly as
`answer`/`answered-at` are on questions.

## Evidence 1 — artifact feedback

`kraken feedback sweep --since <ts>` returns every feedback note across all jobs in the window, each
with the artifact it was left on and a URL for the **snapshot** — the artifact frozen as carl saw it.
Read the snapshot when the note is about something visual or structural: "edges run through boxes" is
unresolvable against an artifact that has since been fixed, or whose cell is long gone.

**Mark a note addressed only when you shipped a change for it**, and name the PR when you do. A note
you merely READ stays open — it belongs to its own job's zeus, and closing it would tell carl it was
handled when nobody handled it.

But the reverse is worse and is the case this rule exists for. A note you acted on will otherwise sit
open **forever**: the job that owns it is usually done, blocked, or torn down by the time you read it,
so its zeus may never run again, and carl is left looking at a note whose fix has already merged. He
has no way to tell that from a note nobody touched.

So the test is authorship of the FIX, not of the note: if this pass shipped something because of it,
close it and say which PR. Otherwise leave it alone.

## Evidence 2 — transcripts

Two passes, and the first is what makes the second affordable.

**The mechanical pass** reads every transcript in the window and counts what is countable: tool
calls, `is_error` clusters by tool, interrupts (carl pressing escape), `<synthetic>` API failures,
turn durations, and literally repeated calls. It also emits a **reduced view** of each session —
user prose, tool calls and assistant text, with thinking, tool results and attachments dropped,
which is about an eighth of the bytes.

**The scouts** are one subagent per transcript, each reading that session's reduced view plus its
digest. They are read-only exploration — they change nothing in the repo and judge nothing — so they are
unnamed subagents, not teammates. Give each one a scratch path and require its findings there: a
scout's report is long, and the reply channel truncates. Spawn them in waves rather than all at once; a few at a time, with the
remainder running as slots free. Every transcript gets read; the pass simply takes as long as it
takes.

Scouts exist to find what counting cannot:

| | |
|---|---|
| **Semantic repetition** | The same mistake made three different ways, so no counter catches it. |
| **A misleading instruction** | A skill sentence several agents read the same wrong way. That several of them did is the evidence. |
| **Confident wrongness** | An agent proceeding smoothly in the wrong direction. Mechanically it looks like a clean session. |
| **Prompt-induced detours** | A run that went sideways because of how it was asked, not what was asked. |

That last one is its own output — see Prompting feedback below.

## Deciding: fix now, or watch

The default bar is **two independent sightings, or one explicitly general instruction from carl**.
You may go below it on judgment, but only in two cases, and you must name it when you do:

- the finding is **mechanically unambiguous** (a counter, an error, a measured duration), or
- the change has **no behavioural blast radius** — skill wording, a comment, a doc line.

Anything touching Go, orchestration, or a brief needs the full bar regardless of how obvious it
feels. Every sub-default ship goes in the report's **judgment calls** section, saying why.

Everything else becomes a watchlist row. A repeat of an existing observation **folds into that row**
— bump its sighting count and update it — rather than adding a second row for the same thing.

## What you may change

Anything in the kraken repo. But reach for **skill text first**: most findings are about how an
agent was instructed, and a sentence is cheaper to get wrong than a function. Larger changes are
allowed and sometimes right — a subagent definition, a brief, orchestration, real Go — and they
carry the same obligations any other change would: tests, and the review the cell's brief specifies.

Two hard fences: never `.claude/settings.json` (per-cell, gitignored, kraken-written — never a
durable improvement), and never anything outside the kraken repo.

**One concern per PR.** A PR mixing a skill tweak with a refactor cannot be partially rejected, and
carl's review is the only thing standing between a judgment call and a merged mistake. The cell
branch recycles after each merge, so several narrow PRs from one night cost nothing.

## Prompting feedback

The scouts also answer a question pointed the other way: **where did carl's own input make the work
harder than it needed to be?** An instruction two agents read two ways. A detail supplied late that
invalidated finished work. A prompt that never said what "done" meant. A correction he has now had
to issue twice.

This goes in the report, where it may quote him verbatim — advice like "one of your prompts was
unclear" is worthless without the prompt. It also accumulates: each distinct observation is a
watchlist row with class `prompting`, folded on repeat like any other, because one ambiguous prompt
is noise and the same one three weeks running is a habit worth naming.

It is what makes a quiet night productive. A night with no corroborated rule and three honest notes
about prompting still produced something worth reading.

## The report

Write `00-dream-report.md` into the artifacts dir. Keep it to a page. Sections, in order, with
"None" spelled out rather than a section dropped:

| Section | Content |
|---|---|
| Shipped | Each change, the evidence behind it, and its PR. |
| Judgment calls | Everything shipped below the two-sighting default, and why. An empty section on a night that shipped several changes means you are not naming your own judgment. |
| Watching | New rows, and what changed on existing ones — the watchlist is not committed, so this is the only place its movement is visible. |
| Your prompting | The prompting feedback, quoted. |
| Seen, not actionable | One line each, capped around ten. It is what distinguishes a quiet night from a blind one. |
| Coverage | Transcripts read, feedback notes swept, and anything skipped. |

## Inside A Kraken Cell

When a kraken job brief sends you here, these deltas apply — the brief wins wherever it is more
specific, and it is the only place the cell's real paths appear:

- **The report goes in the brief's SET ROOT** (`<cellDir>/artifacts/`) as `00-dream-report.md`. That
  directory is the only board-visible one, and the report is the deliverable; it is also what the
  board's `DREAM` view renders, so a pass that ships changes but writes no report is invisible.
- **Read and write watchlist rows with `kraken watchlist get|add|update|remove`**; sweep feedback with
  `kraken feedback sweep --since <ts>`. Run `kraken feedback addressed <slug> <id> <text>` **only for
  a note you shipped a change for**, naming the PR in the text — see Evidence 1. Note the slug is the
  OTHER job's, not this dream job's.
- **Scouts are unnamed subagents**, never teammates: they read and report, and change nothing in the
  repo — they still write their own findings to the scratch path you give them. The crew rules in
  the brief govern any agent that changes the repo.
- **Never `open`** (there is no browser in a cell).
- **The phase ends with one line, then you stop.** Say exactly:

  `dream swept: <set root>/00-dream-report.md (N shipped, M watching, K transcripts)`

  followed by the PR urls if any, and STOP (end the turn). Use `0` where a count is zero — never omit
  the clause. Kraken derives blocked from your silence and shows your last words on the board.
- Never declare the job done — accepting is carl's move, never yours.
- `solo: true` on the job means no subagents at all: run the mechanical pass and read what you can
  yourself, and say in the report that the semantic pass was skipped.
