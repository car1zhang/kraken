---
name: info
description: "KRAKEN-LEVEL copy (repo-owned, materialized into cell worktrees). Skill for explaining existing systems and answering deep questions about current behavior — the explain workflow. Researches deeply, then answers with ONE brief high-level overview and the minimum artifacts that answer the question; goes deeper only where the reader asks. Never opens open questions (that is /aim's job) and never proposes changes. Use when the user asks for /info, \"explain this system\", \"how does X work end to end\", \"document the current behavior of X\", \"investigate and write up X\", \"answer this question with a doc\", or when a kraken info-job brief invokes it. For proposed/future changes use /aim spec mode instead."
---

# Info — Explain An Existing System

> **Kraken-level copy** — loaded from `.claude/skills/info/` in this worktree. If the harness
> header above says `Base directory ... ~/.claude/skills/info`, the personal copy shadowed
> this one (personal beats project on a name collision).

Answer a question about a system that already exists. Research it deeply; explain it briefly.
The deliverable is a conversation, not a book: a short high-level `00-overview.md` first, then
whatever depth the reader actually asks for, one area at a time.

This is the read-only sibling of /aim — it never proposes changes — and it is deliberately
**lighter** than /aim: /info is not "/aim explain mode with a different name", and it must not
produce /aim's artifact set by default.

## The two rules that define this skill

**1. Deep research, brief output.** Depth of investigation and length of output are independent
knobs. Read the code until you actually understand it — entrypoints, call paths, the awkward
branches, the tests that pin behavior — then write the *short* version. Every claim traces to a
real path you read. Never pad the overview to look thorough; the research is what makes it
trustworthy, not the word count.

**2. No open questions. Ever.** /info never writes an open-questions record — not `kraken
questions add/set/update` in a cell, not `questions.yaml` standalone. Questions belong to /aim.
Uncertainty that survives research is one honest line inside the artifact it belongs to ("X is
not confirmed: <what you'd need to check>"), never a row and never a batch of prompts. If one
thing genuinely blocks the whole explanation, ask it directly in chat — a single question, not
a list — and keep going with a stated default while you wait.

## First pass: one brief overview

The default first deliverable is exactly one file: `00-overview.md`, and it stays short — a page
at most. Assume the reader wants orientation, not the complete tour. It covers, tersely:

| Part | Length | Content |
|---|---|---|
| The answer | 2-5 sentences | The direct answer to what was asked, up front. |
| Mental model | 3-6 sentences or a small table | The handful of moving parts and how they relate — the shape a reader needs to reason about the rest. |
| How it actually flows | 3-8 lines | One concrete pass through the system, real names and values, in order. |
| Where it lives | small table | The 3-8 files/functions that matter, one line each, so the reader can jump into source. |
| Not confirmed | 0-3 lines | Anything research could not settle. Omit the section entirely when there is nothing. |
| Go deeper | 3-6 bullets | Named areas you researched but deliberately did not expand, one line each — the menu the reader picks from. |

That last row is the point of the whole skill: it is how the reader steers the next round. Name
areas concretely ("the retry/backoff path in `daemon.go`", "how conflict state is resolved on
import"), never vaguely ("more detail on the backend").

**Diagrams in the first pass: at most one, usually zero.** Add a single diagram card only when the
mental model is genuinely spatial or flow-shaped and a picture *replaces* prose rather than
decorating it. Otherwise offer it as a "go deeper" item and render it when asked. Never ship the
/aim two-core-diagram pair on a first pass.

**What the first pass does NOT include**, unless the request explicitly asks for it: a components
table, a code map beyond the small "where it lives" table, a testing/verification plan, an
assumptions doc, a scope doc, per-component test coverage, or any `5N` section doc at all. These
are /aim's required floor, not /info's. Shipping them unasked is the failure mode this skill
exists to prevent.

## Later passes: one area, one artifact

When the reader picks an area (or asks a follow-up), go as deep as that area needs and add **one**
artifact for it — a `50-<topic>.md` section doc (numbered sequentially from 50 as topics arrive)
or a `1N-<id>.html` diagram card. Keep `00-overview.md` as the index: add a one-line pointer to
the new artifact, and correct anything the deeper read proved wrong.

Do not retro-fit the rest of /aim's set because a second artifact now exists. Each round adds only
what was asked for. A five-round conversation should end with five focused artifacts, not a
generated spec.

## Explain-mode discipline

- **Describe, never prescribe.** No "should", no proposed refactors. A tempting improvement is
  one parenthetical observation at most, or the reader's cue to start an /aim spec.
- **Cite real code.** File paths (and functions) for every load-bearing claim, so any paragraph
  is jumpable into source. `file.go:120` style is ideal.
- **Verification over assertion.** For a key behavior, say how to SEE it: the test that covers it,
  the log line, the command to run, the fixture that encodes it. One pointer beats a paragraph.
- **Uncertainty is content, not a question.** Say what you could not confirm, in place, and move on.
- **Answer the question that was asked.** Scope creep into neighboring subsystems is what makes
  explanations unreadable; mention the neighbor in one line and put it on the "go deeper" menu.

## Diagrams, when a round calls for one

Diagrams follow /aim's mechanics exactly — read `.claude/skills/aim/SKILL.md` (paths relative to
the worktree root, your cwd in a cell, not this skill's base directory) for the full rules. The
short form, with /info's overrides:

- Each diagram is designed AND rendered by a subagent on **`model: opus`** (Claude Opus 5 — sonnet
  misplaces elements), writing one complete `<svg>` fragment to the set's fragments dir
  (`<cellDir>/doc/fragments/<id>.svg` in a cell). You never read or write SVG bodies.
- Every brief carries the **full contents of `.claude/skills/aim/assets/hermes-contract.md`
  verbatim**, the diagram's exact content, a card-unique `arrow-<id>` marker id and path-id prefix,
  the fragment output path, and the anti-artifact rules: no overlapping elements, text measured
  against its box (~7px/char at 13px) and wrapped with tspans before it can overflow, edges attached
  to box borders and routed around (never through) other boxes, every coordinate inside the viewBox,
  generous whitespace between groups.
- Boxes are `box → kind` pairs using the sanctioned kind list in the contract's Shape Kit section;
  the subagent derives the silhouette from the kind — never dictate a shape class.
- Lint before wrapping: `python3 .claude/skills/aim/assets/lint_svg.py <fragment> --marker-id
  arrow-<id> --prefix <prefix>`. Feed line-numbered violations back as a fresh fix-round brief to a
  new subagent (max 2 rounds, then debug the fragment source directly). Only lint-clean fragments
  get wrapped.
- Wrap per /aim's "Wrap, don't flatten" rule: copy `.claude/skills/aim/assets/diagram-card.html`
  to `<set-root>/10-<id>.html` (next free number in `10-49`, assigned sequentially; never 3-digit
  prefixes) and substitute all three placeholders with a python one-liner using `str.replace`
  (never `re.sub` — a fragment containing `\1` or `\g` crashes its replacement parsing), in one
  pass, never letting an earlier replacement introduce a later token. HTML-escape `&`, `<`, `>` in
  the title and caption; NEVER escape the fragment (it is markup). Fill `{{CARD_TITLE}}` (occurs
  twice) and `{{CAPTION}}` (occurs once) first, then replace the literal
  `<!-- DIAGRAM: {{DIAGRAM_ID}} -->` comment with the fragment's raw contents — e.g.
  `python3 -c "import pathlib, html; t = pathlib.Path('.claude/skills/aim/assets/diagram-card.html').read_text(); t =
  t.replace('{{CARD_TITLE}}', html.escape(TITLE)).replace('{{CAPTION}}', html.escape(CAPTION)).replace('<!-- DIAGRAM: {{DIAGRAM_ID}} -->',
  pathlib.Path(FRAG).read_text()); pathlib.Path(OUT).write_text(t)"` (adapt `TITLE`/`CAPTION`/`FRAG`/`OUT`).
  Verify the output contains `<svg` and contains none of `{{CARD_TITLE}}`, `{{CAPTION}}`,
  `<!-- DIAGRAM:` — if either check fails the wrap failed; do not ship the card.
- Diagram ids are unique across the whole set (an id names both the fragment file and the card
  file). Explain-mode architecture diagrams are **current state only** — there is no target panel,
  because nothing is being proposed.
- All diagrams are inline SVG — never Mermaid, never ASCII art.

## Workflow

1. **Frame the question** in 2-4 bullets: what the reader wants to understand, what is in scope,
   what depth is right, what is explicitly out of scope. Keep this in chat, not in an artifact.
2. **Research until you could write the long version** — read the relevant `CLAUDE.md` / `AGENTS.md`,
   trace entrypoints with `rg`, read enough implementation to cite real paths, types, and behaviors,
   and fan out parallel `Explore` agents for broad sweeps. Every codebase-answerable question gets
   answered here; none of them become open questions.
3. **Write the short version.** Create the set root and write `00-overview.md` per the table above.
   Add at most one diagram card, and only if it earns its place.
4. **Share and stop.** Give the `00-overview.md` path plus the set root (never run `open`), then
   state the "go deeper" menu in your final message so the reader can pick. Then stop — the next
   round is theirs to steer.
5. **Iterate on demand.** Each request → deeper research on that area → one new artifact + a
   one-line pointer added to the overview. Correct anything the deeper read disproved.

## Inside a kraken cell

When invoked by a kraken job brief (the brief will say so and give paths), apply these deltas —
the cell's brief wins wherever it is more specific:

- The set root is `<cellDir>/artifacts/`: write `00-overview.md` there, plus any later diagram
  cards (`1N-<id>.html`) and topic docs (`5N-<topic>.md`) as rounds are requested. Fragments stage
  at `<cellDir>/doc/fragments/<id>.svg`. `artifacts/` is the only board-visible directory and is
  the deliverable.
- **Never run `kraken questions` write verbs on an /info job** — no `add`, `set`, `update`, or
  `remove`. An /info job's questions list stays empty. (`kraken feedback get <slug>` still applies:
  carl's per-artifact feedback arrives as rows, and each open entry gets addressed and marked with
  `kraken feedback addressed <slug> <id> <text>`.)
- Never run `open` (no browser in a cell); the board renders the artifacts.
- When finished with a round, state what's ready plus the "go deeper" menu in your final output and
  STOP (end the turn) — kraken derives the blocked state from your silence and shows your last words
  on the board; carl reads the artifacts and messages back directly. A decision that genuinely
  cannot be defaulted may use `AskUserQuestion` — kraken surfaces the options as buttons — but that
  is for blockers, not for the routine uncertainty that belongs inline. Never declare the job done;
  accepting is carl's move.
- An /info job never gets a `/do` and never opens a PR.
- `solo: true` on the job means no subagents at all: prose only — no diagram cards in any round.
