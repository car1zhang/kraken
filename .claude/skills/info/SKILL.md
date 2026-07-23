---
name: info
description: "KRAKEN-LEVEL copy (repo-owned, materialized into cell worktrees). Skill for explaining existing systems and answering deep questions about current behavior — the explain workflow. Produces an explain-mode HTML artifact (the /spec explain template: progressive detail, inline-SVG diagrams via Opus subagents, code map, verification guidance) without proposing changes. Absorbs what kraken explore/question jobs do. Use when the user asks for /info, \"explain this system\", \"how does X work end to end\", \"document the current behavior of X\", \"investigate and write up X\", \"answer this question with a doc\", or when a kraken info-job brief invokes it. For proposed/future changes use /spec spec mode instead."
---

# Info — Explain An Existing System

> **Kraken-level copy** — loaded from `.claude/skills/info/` in this worktree. If the harness
> header above says `Base directory ... ~/.claude/skills/info`, the personal copy shadowed
> this one (personal beats project on a name collision).

Research an existing system, behavior, or question and produce an **explain-mode doc**: a
rendered HTML artifact that transfers the current state of the world into the reader's head —
what exists, why, how data flows, where code lives, how to verify or debug it, and what
remains uncertain. This is the read-only sibling of /spec: it never proposes changes. Possible
follow-ups belong in Open Questions or Iteration Notes, and the canonical next step for a
change is a /spec spec.

/info IS /spec's explain mode, packaged as its own workflow. All of /spec's Core Rules apply
verbatim — investigate before asking, progressive disclosure, spam diagrams, animate flow
diagrams, worked examples (at least one exemplary diagram tracing a concrete named instance
with real field values through the system, plus example-data renderings in their natural
shape — a 2D array as a .cell grid, a schedule as a day/time strip — never raw JSON prose),
all-inline-SVG (never Mermaid/ASCII), subagent delegation for every diagram
fragment, placeholder-comment source + mechanical flatten, tables over lists, globally
numbered open questions (`Q1, Q2, …`), and re-flatten + open after every edit round. Read
`.claude/skills/spec/SKILL.md` (paths relative to the worktree root — your cwd in a cell —
not this skill's own base directory) for those rules if not already loaded this session; do not
duplicate or drift from them — with one override: **diagram subagents run on `model: opus`**
(sonnet produces visual artifacts and bad element placement), and every diagram brief must
include the anti-artifact rules: no overlapping elements, text measured against its box
(~7px/char at 13px) and wrapped with tspans before it can overflow, edges attached to box
borders and routed around (never through) other boxes, every coordinate inside the viewBox,
generous whitespace between groups. Every diagram brief must also carry the **full contents
of `.claude/skills/spec/assets/hermes-contract.md` verbatim** (same worktree-relative sourcing; the parts-kit style contract —
hermes composes from that catalog, it never invents markup or color); after each fragment is
written, run `python3 .claude/skills/spec/assets/lint_svg.py <fragment> --marker-id arrow-<id>
--prefix <prefix>` (same worktree-relative sourcing) and feed line-numbered violations back as a fresh fix-round brief to a new
subagent (max 2 fix rounds, then debug the fragment source directly). Only lint-clean fragments
get flattened.

## Workflow

1. **Frame the question** in 2-4 bullets: what the reader wants to understand, which system
   or flow is in scope, what depth is right (overview vs. code-level), what is explicitly out
   of scope.
2. **Research until the doc can be written responsibly.** Read the relevant `AGENTS.md` /
   `CLAUDE.md`, trace the active entrypoints with `rg`, read enough implementation to cite
   real paths, types, and behaviors. Fan out parallel Explore agents for broad sweeps. Every
   codebase-answerable question gets answered here — Open Questions is reserved for genuine
   unknowns (missing history, ambiguous intent, things only production data can show).
3. **Draft in explain mode.** Copy `.claude/skills/spec/assets/explain-template.html` (paths relative to the
   worktree root — your cwd in a cell) to
   `~/designs/<slug>.explain.html` and fill it in: mental model, current architecture,
   data flow, component walkthrough with per-component tables, key behaviors (success path,
   failure modes as they exist today), observability, low-level code map, review/reading
   guide, verification plan, open questions, iteration notes. Keep Section 1 very concise —
   the diagrams carry it.
4. **Diagrams via subagents, then flatten.** Every figure slot keeps a permanent
   `<!-- DIAGRAM: <id> -->` placeholder in the source; each diagram is designed AND rendered
   by an **Opus** subagent into `~/designs/.fragments/<slug>/<id>.svg` (batch them in
   parallel; assign each a doc-unique marker id and path-id prefix; include the
   anti-artifact rules and the full `hermes-contract.md` contents in every brief). After each
   fragment is written, run `python3 .claude/skills/spec/assets/lint_svg.py <fragment>
   --marker-id arrow-<id> --prefix <prefix>` (same worktree-relative sourcing) and feed line-numbered violations back as a fresh
   fix-round brief (max 2 rounds, then debug the fragment source directly). Only lint-clean
   fragments get flattened. Flatten mechanically into `~/designs/.build/<slug>.explain.html`
   and share the build-file path (never run `open` on it).
5. **Iterate.** Summarize the mental model and the highest-value open questions (by number);
   fold answers and corrections in; re-flatten each round.

## Explain-mode discipline

- Describe, never prescribe: no "should", no proposed refactors in the body. A tempting
  improvement goes into Open Questions ("worth changing X?") or Iteration Notes.
- Cite real code: file paths (and functions) for every load-bearing claim, so the reader can
  jump from any paragraph into the source.
- Verification over assertion: for each key behavior, say how to SEE it being true — the
  test that covers it, the log/trace to read, the command to run, the fixture that encodes it.
- Uncertainty is content: things you could not confirm are stated as such in Assumptions or
  Open Questions, not smoothed over.

## Inside a kraken info cell

When invoked by a kraken info-job brief (the brief will say so and give paths), apply these
deltas — the cell's brief wins wherever it is more specific:

- Doc source lives at `<cellDir>/doc/<slug>.explain.html`, fragments at
  `<cellDir>/doc/fragments/<id>.svg`, and the flattened build is written to
  `<cellDir>/artifacts/<slug>.explain.html` — the artifacts copy is the only board-visible
  file and the deliverable.
- Never run `open` (no browser in a cell); the board renders the artifact.
- Bake open questions into the doc's tables rather than blocking on them. When finished
  producing/editing the artifact, state what's ready in your final output and STOP (end the
  turn) — kraken derives the blocked state from your silence and shows your last words on
  the board; the human reads the doc and messages back directly. A decision that genuinely
  cannot be defaulted may use AskUserQuestion — kraken surfaces the options as buttons.
  Never declare the job done; accepting is the human's move.
- `solo: true` on the job means no subagents at all: skip the diagram pipeline and write a
  prose-only doc (keep the figure slots' placeholders out entirely).
