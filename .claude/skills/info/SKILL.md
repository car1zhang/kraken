---
name: info
description: "KRAKEN-LEVEL copy (repo-owned, materialized into cell worktrees). Skill for explaining existing systems and answering deep questions about current behavior — the explain workflow. Produces an explain-mode artifact set (an /aim explain-mode overview, standalone diagram cards via Opus subagents, and section docs) without proposing changes. Absorbs what kraken explore/question jobs do. Use when the user asks for /info, \"explain this system\", \"how does X work end to end\", \"document the current behavior of X\", \"investigate and write up X\", \"answer this question with a doc\", or when a kraken info-job brief invokes it. For proposed/future changes use /aim spec mode instead."
---

# Info — Explain An Existing System

> **Kraken-level copy** — loaded from `.claude/skills/info/` in this worktree. If the harness
> header above says `Base directory ... ~/.claude/skills/info`, the personal copy shadowed
> this one (personal beats project on a name collision).

Research an existing system, behavior, or question and produce an **explain-mode artifact set**:
a brief `00-overview.md`, standalone diagram cards, and section docs that together transfer the
current state of the world into the reader's head — what exists, why, how data flows, where code
lives, how to verify or debug it, and what remains uncertain. This is the read-only sibling of
/aim: it never proposes changes. Possible follow-ups belong in `questions.yaml` or a notes
section doc, and the canonical next step for a change is an /aim spec.

/info IS /aim's explain mode, packaged as its own workflow. All of /aim's Core Rules apply
verbatim — investigate before asking, progressive disclosure, spam diagrams, animate flow
diagrams, worked examples (at least one exemplary diagram card tracing a concrete named instance
with real field values through the system, plus example-data renderings in their natural
shape — a 2D array as a .cell grid, a schedule as a day/time strip — never raw JSON prose),
all-inline-SVG (never Mermaid/ASCII), subagent delegation for every diagram
fragment, the artifact-set shape (`00-overview.md` + `1N`-`4N` diagram cards + `5N`+ section
docs), questions in `questions.yaml` (not in any artifact) with globally numbered ids
(`Q1, Q2, …`), and sharing the overview + set-root path after every edit round. Read
`.claude/skills/aim/SKILL.md` (paths relative to the worktree root — your cwd in a cell —
not this skill's own base directory) for those rules if not already loaded this session; do not
duplicate or drift from them — with one override: **diagram subagents run on `model: opus`**
(Claude Opus 5; sonnet produces visual artifacts and bad element placement), and every diagram brief must
include the anti-artifact rules: no overlapping elements, text measured against its box
(~7px/char at 13px) and wrapped with tspans before it can overflow, edges attached to box
borders and routed around (never through) other boxes, every coordinate inside the viewBox,
generous whitespace between groups. Every diagram brief must also carry the **full contents
of `.claude/skills/aim/assets/hermes-contract.md` verbatim** (same worktree-relative sourcing; the parts-kit style contract —
hermes composes from that catalog, it never invents markup or color); after each fragment is
written, run `python3 .claude/skills/aim/assets/lint_svg.py <fragment> --marker-id arrow-<id>
--prefix <prefix>` (same worktree-relative sourcing) and feed line-numbered violations back as a fresh fix-round brief to a new
subagent (max 2 fix rounds, then debug the fragment source directly). Only lint-clean fragments
get wrapped into cards, per /aim's "Wrap, don't flatten" rule: copy `.claude/skills/aim/assets/diagram-card.html`
to `<set-root>/10-<id>.html` (taking the next free number in `10-49`, assigned by you sequentially — needing
more than the band means over-decomposition, merge; never use 3-digit prefixes) and mechanically substitute all
three placeholders with a python one-liner using `str.replace` (never `re.sub` — a fragment containing `\1` or
`\g` crashes `re.sub`'s replacement parsing), substituting in one pass — never let an earlier replacement's value
introduce a later token. HTML-escape `&`, `<`, `>` in the title and caption — they land in markup; NEVER escape
the fragment (it is markup). Fill the `{{CARD_TITLE}}` token (occurs twice: the `<title>` and
the `<h1>`) and the `{{CAPTION}}` token (occurs once, in the `<figcaption>`) first, then substitute the literal
`<!-- DIAGRAM: {{DIAGRAM_ID}} -->` placeholder comment with the fragment file's raw contents — e.g.
`python3 -c "import pathlib, html; t = pathlib.Path('.claude/skills/aim/assets/diagram-card.html').read_text(); t =
t.replace('{{CARD_TITLE}}', html.escape(TITLE)).replace('{{CAPTION}}', html.escape(CAPTION)).replace('<!-- DIAGRAM: {{DIAGRAM_ID}} -->',
pathlib.Path(FRAG).read_text()); pathlib.Path(OUT).write_text(t)"` (adapt `TITLE`/`CAPTION`/`FRAG`/`OUT` to real
values). All three replacements are global — never hand-edited. Verify the output contains `<svg` and does NOT
contain `{{CARD_TITLE}}`, `{{CAPTION}}`, or `<!-- DIAGRAM:` — if either check fails, the wrap failed; do not ship the card.

## Workflow

1. **Frame the question** in 2-4 bullets: what the reader wants to understand, which system
   or flow is in scope, what depth is right (overview vs. code-level), what is explicitly out
   of scope.
2. **Research until the set can be written responsibly.** Read the relevant `AGENTS.md` /
   `CLAUDE.md`, trace the active entrypoints with `rg`, read enough implementation to cite
   real paths, types, and behaviors. Fan out parallel Explore agents for broad sweeps. Every
   codebase-answerable question gets answered here — `questions.yaml` is reserved for genuine
   unknowns (missing history, ambiguous intent, things only production data can show).
3. **Draft in explain mode.** Create the set root (see below for the exact path in a cell vs.
   standalone) and write `00-overview.md`: mental model, current architecture, data flow —
   very concise, 1-3 sentences per subsection, the diagram cards carry the detail. Then write
   section docs as needed: component walkthrough with per-component tables, key behaviors
   (success path, failure modes as they exist today), observability, low-level code map,
   review/reading guide, verification plan, notes. No questions requirement in explain mode,
   but `questions.yaml` is still the right home for anything genuinely open.
4. **Diagrams via subagents, then wrap.** Each diagram is designed AND rendered by an
   **Opus** (`model: opus` — Claude Opus 5) subagent into a fragment file under the set's fragments dir (batch them in
   parallel; assign each a card-unique marker id and path-id prefix; include the
   anti-artifact rules and the full `hermes-contract.md` contents in every brief). After each
   fragment is written, run `python3 .claude/skills/aim/assets/lint_svg.py <fragment>
   --marker-id arrow-<id> --prefix <prefix>` (same worktree-relative sourcing) and feed line-numbered violations back as a fresh
   fix-round brief (max 2 rounds, then debug the fragment source directly). Only lint-clean
   fragments get wrapped, per /aim's "Wrap, don't flatten" rule: copy `.claude/skills/aim/assets/diagram-card.html`
   to `<set-root>/10-<id>.html` (taking the next free number in `10-49`, assigned by you sequentially) and
   mechanically substitute all three placeholders with a python one-liner using `str.replace` (never `re.sub`
   — a fragment containing `\1` or `\g` crashes `re.sub`'s replacement parsing), substituting in one pass —
   never let an earlier replacement's value introduce a later token. HTML-escape `&`, `<`, `>` in the title
   and caption — they land in markup; NEVER escape the fragment (it is markup). Fill `{{CARD_TITLE}}` (occurs
   twice) and `{{CAPTION}}` (occurs once) first, then substitute the literal `<!-- DIAGRAM: {{DIAGRAM_ID}} -->`
   placeholder comment with the fragment file's raw contents — all replacements global, never hand-edited.
   Verify the output contains `<svg` and does NOT contain `{{CARD_TITLE}}`, `{{CAPTION}}`, or `<!-- DIAGRAM:`
   before shipping the card. Share the `00-overview.md` path plus the set root (never run `open` on anything).
5. **Iterate.** Summarize the mental model and the highest-value open questions (by
   `questions.yaml` id); fold answers and corrections into the relevant artifact each round.

## Explain-mode discipline

- Describe, never prescribe: no "should", no proposed refactors in the body. A tempting
  improvement goes into `questions.yaml` ("worth changing X?") or a notes section doc.
- Cite real code: file paths (and functions) for every load-bearing claim, so the reader can
  jump from any paragraph into the source.
- Verification over assertion: for each key behavior, say how to SEE it being true — the
  test that covers it, the log/trace to read, the command to run, the fixture that encodes it.
- Uncertainty is content: things you could not confirm are stated as such in an assumptions
  section or `questions.yaml`, not smoothed over.

## Inside a kraken info cell

When invoked by a kraken info-job brief (the brief will say so and give paths), apply these
deltas — the cell's brief wins wherever it is more specific:

- The set root is `<cellDir>/artifacts/` — write `00-overview.md`, diagram cards
  (`1N-<id>.html`), and section docs (`5N-<section>.md`) directly there. Fragments stage at
  `<cellDir>/doc/fragments/<id>.svg`. Questions, if any, go in `<cellDir>/questions.yaml` — a
  sibling of `artifacts/`, never inside it. `artifacts/` is the only board-visible directory
  and the deliverable.
- Never run `open` (no browser in a cell); the board renders the artifacts.
- Bake genuinely open items into `questions.yaml` rather than blocking on them. When finished
  producing/editing the set, state what's ready in your final output and STOP (end the
  turn) — kraken derives the blocked state from your silence and shows your last words on
  the board; the human reads the artifacts and messages back directly. A decision that
  genuinely cannot be defaulted may use AskUserQuestion — kraken surfaces the options as
  buttons. Never declare the job done; accepting is the human's move.
- `solo: true` on the job means no subagents at all: skip the diagram pipeline and write
  prose-only section docs plus `00-overview.md` (no diagram cards at all).
