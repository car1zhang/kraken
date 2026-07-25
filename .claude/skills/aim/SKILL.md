---
name: aim
description: "User skill for drafting and iterating product/engineering documents that describe a system, whether the system already exists or is being proposed. Supports two modes: spec mode for proposed/future changes and explain mode for current/existing systems. Produces a set of standalone artifacts — a brief overview, diagram cards, and section docs — plus an open-questions record (kraken.db rows in a cell, questions.yaml standalone). Uses progressive detail plus diagrams, architecture visuals, component flows, data-model sketches, screenshots, and pictures where helpful. The canonical follow-up is [[do]] (/do), which implements the artifact set this skill produces. Use when the user asks for /aim, \"write a doc\", \"draft a doc\", \"explain this system\", \"document this system\", \"spec this out\", \"write a spec\", \"draft an initial spec\", \"let's spec\", \"initial spec and we'll iterate\", or asks to turn an idea/current-state analysis into a scoped artifact with goals, non-goals, assumptions, testing/verification plans, success metrics, and many open questions."
---

# Aim — Initial Alignment Artifact Set

Create a concrete first artifact set the user can react to: no single flattened doc — a brief overview, standalone diagram cards, and section docs, each openable on its own. Use spec mode for proposed or future systems; use explain mode for existing systems or current behavior. Do enough research to avoid ignorant questions, then write down the current best shape with explicit assumptions, a large set of open questions, and a practical testing or verification plan. The artifact set is an iteration surface, not final architecture.

**Follow-up skill: [[do]] (`/do`)** — once the set is locked, `/do` reads it, resolves any `P0` open questions with the user, and implements it. `/aim` produces the contract; `/do` executes it. When the user is ready to build, point them at `/do` rather than implementing inline.

## Artifact Set Shape

Every set lives in one directory (the **set root**) and is a flat list of files, ordered by numeric name prefix — lexicographic sort is the display order everywhere:

- **`00-overview.md`** — REQUIRED. A brief markdown page (~1 page): the change/system, its approach, its intended goal. The high-level alignment surface — no tables of internals, no diagrams; those live in the cards and section docs below.
- **`10-<id>.html` … `49-<id>.html`** — diagram cards, one per diagram, chosen per-need at your discretion (**≥1 worked-example diagram card required** in spec mode / for build jobs). Each card is a standalone HTML file: the shared diagram CSS + one hermes-rendered SVG + a self-sufficient caption (2-4 sentences: what the figure shows and what to take away, understandable with no other artifact open). UI mockups also live in this band, as their own `1N-<id>.html` card or a single `4N-ui-mockups.html` artifact — built directly by you (never delegated to hermes; see Core Rules).
- **`50-<section>.md` and up** — section docs, at your discretion, mostly for the implementing agent's own use. In spec mode / for build jobs, `50-components.md`, `51-code-map.md`, and `52-testing-plan.md` are REQUIRED; assumptions, constraints, scope, etc. are optional additions in this band. Markdown with tables.

There is no single flattened doc file. Diagram fragments still stage separately (see the delegation rule below) and get mechanically wrapped into cards — you never hand-author or read SVG bodies.

**Where the set lives:**

- In a kraken cell: the set root is `<cellDir>/artifacts/` (a kraken job brief will say so and give you exact paths — its deltas win wherever more specific). Fragments stage in `<cellDir>/doc/fragments/`. Questions are **not a file** — they are rows in `kraken.db`, read and written through the cell-side `kraken questions get/set/add/update/remove` verbs (see Core Rules).
- Standalone/personal use: the set root is `~/designs/<slug>/`. Fragments stage in `~/designs/.fragments/<slug>/` (the legacy fragments convention, kept for standalone use). Questions live in `~/designs/<slug>.questions.yaml` — a sibling of the set root, not inside it.

## Core Rules

- Prefer drafting over interrogating. Ask only blockers before the first draft; otherwise state assumptions and capture uncertainties in the open-questions record.
- **Investigate before asking.** Any question that the codebase, git history, configs, schema, or docs can answer must be researched and answered yourself first — never park a codebase-answerable question in the open-questions record or pose it to the user. Open questions are reserved for genuine user decisions (product intent, priorities, tradeoffs, external constraints) and facts no amount of code-reading can resolve. Before writing any question, ask: "could I answer this with `rg`, by reading a file, checking the schema, or running a query?" If yes, do that instead.
- Research current state before writing codebase-specific specs. Read the relevant `AGENTS.md` files and skim the key implementation files enough to cite real paths and avoid inventing constraints.
- Use progressive disclosure across the set. `00-overview.md` stays very high level (1-3 sentences per subsection — the diagram cards carry the detail); diagram cards each carry exactly one figure's worth of detail; section docs go lowest-level (components, code map, review guide).
- **Two core diagrams, then tunnel.** Most sets need exactly two core diagrams. (1) **Architecture, highest level**: what the components are, what talks to what, and where the boundaries sit. Every box carries a **kind** tag: the concrete runtime/artifact identity, taken from the sanctioned kind list in the Shape Kit section of `.claude/skills/aim/assets/hermes-contract.md` — that section's kind → shape table is the source of truth, and a box's kind determines its silhouette. Never a vague noun like "service" or "layer" unless that's literally what it is; a component you cannot honestly name a kind for falls back to the plain `node` rounded rect with its kind text line intact. Every boundary container is labeled with its boundary type (machine, process, network, package, file-ownership). `50-components.md` (below) carries this same Kind/Boundary requirement into its per-component table. Spec mode ships this as **one combined card**: a current-state panel and a target-state panel stacked in a single figure, sharing one layout so the delta pops (greenfield systems with no current state draw the target panel only — never an empty panel); explain mode ships current state only. This card must be complete at its own altitude — a reader should be able to locate any deeper card inside one of its boxes. (2) **End-to-end flows**: every flow a user/agent/datum can take through the feature, entry to exit, traced with **one concrete worked example** (real names, ids, field values; data drawn in its natural shape; step badges; animated per the animation rules below) — the worked-example and data-in-natural-shape requirements described further down live in this card, not a separate mandated one. Beyond these two: **tunnel cards** that zoom into one architecture box whose internals the reader can't infer and genuinely needs (each names the parent box it expands), plus any other card you judge important for a clear mental model — this is a default, not a hard cap. UI mockups keep their own rules and band (below). Only skip a diagram when it would genuinely show nothing. Also use tables, screenshots, and reference pictures where they reduce ambiguity.
- **Containment: shape inside shape, then `group`, then tunnel.** When one component lives inside another, the parent's own silhouette is the boundary — a component may nest inside another component's silhouette whenever the Shape Kit gives that silhouette a **content well** (a database holding its tables); several rect-looking silhouettes are deliberately leaf-only, so check the kit rather than eyeballing the shape. No dashed container needed. A dashed `group` is for boundaries that are **not** components: ownership, a repo, a network, a machine you are not drawing as a box. Anything past the Shape Kit's **component-nesting depth limit** becomes its own tunnel card instead — deeper than that the innermost box stops being labelable — with the kit's two carve-outs: data `cell`s are exempt (`ui > table > cell` is one component holding one component holding data), and dashed `group`/`region`/`lane` wrappers are boundaries rather than components, so they don't consume a level. Which silhouettes have wells, where their children may sit, and the exact depth limit all live in the Shape Kit section of `.claude/skills/aim/assets/hermes-contract.md`; the brief names the containment, hermes draws it.
- **Animate flow diagrams.** Any diagram that depicts movement — sequence/swimlane flows, request lifecycles, data movement, pipelines, state transitions, rollout timelines — should be animated by default: SMIL `<animateMotion>` dots traveling along edge paths, `stroke-dashoffset` "marching ants" on active edges, staged opacity/pulse highlighting the currently-active node. Purely structural diagrams (data models, code maps, component boundary maps) stay static. Animations must be subtle, looping, and self-contained (SMIL/CSS only — no JS), and paused under `@media (prefers-reduced-motion: reduce)`.
- **Show worked examples, not just structure.** A system diagram tells the reader what the parts are; an exemplary diagram tells them what actually happens. This is the content of the **end-to-end flow card** from "Two core diagrams, then tunnel" above — not an additional card: one concrete, named instance — a real user flow from start to finish, a sample record/request with actual field values — traced through the system stage by stage, with numbered step badges and the instance's data shown visibly changing between stages. And whenever the set deals in structured data (API responses, matrices, 2D arrays, schedules, grids, regions), add a card showing what an **example of that data means**, rendered in its natural shape — a 2D array as an actual grid of cells with headers, a slot schedule as a day/time strip, a spatial payload as a drawn region — never as raw JSON prose. Keep one consistent example (same user/org/record/values) across all exemplary diagram cards in a set so the reader follows a single story. These are ordinary diagrams pipeline-wise — delegated to the diagram subagents — but the brief must spell out the exact example values to draw (the subagent can't invent realistic data).
- **Mock up every UI change.** When the set proposes or touches UI, include a mockup of each affected screen/component state — inline HTML/CSS blocks styled to resemble the real UI (preferred), annotated screenshots of the current UI, or a picture; SVG only when it genuinely fits. Mockups live in the diagram-card band (`1N-<id>.html` or `4N-ui-mockups.html`) but are built by **you directly** — the Opus design-subagent delegation below applies to SVG diagrams only, never to UI mockups. Show before/after when the change modifies an existing screen. Keep mockups self-contained (inline CSS, no external assets).
- **All diagrams are inline SVG — never Mermaid, never ASCII art.** Follow the SVG conventions cataloged in `.claude/skills/aim/assets/hermes-contract.md` (mirrored in the legacy templates' header comments): `viewBox="0 0 860 H"` with no width/height attributes; a rich shared parts kit styled by the card's stylesheet so every diagram looks consistent — component boxes in a silhouette per kind, with the sparkle as the model-powered mark, plus boundaries, lanes, pills, decision diamonds, actors, grid cells, step/count badges, bars, axes, regions, titles, notes, edges, flow dots (`hermes-contract.md` is the catalog of record; its Parts Index is designed for the subagent to scan and pick from, and its classes must land on the exact elements it prescribes — a box class on a bare `<rect>` instead of its `<g>` wrapper renders unstyled); a card-unique arrow marker id per SVG (e.g. `arrow-target-shape` — marker ids only need to be unique within their own card now, since each diagram is its own standalone file); long labels wrapped with `<tspan>`; ≥12px padding inside boxes. **Every diagram opens with a required in-SVG `.title`** — top-left, stating the figure's takeaway in plain words (never a generic label); `lint_svg.py` fails fragments without one. Diagram subagent briefs must include the **full contents of `.claude/skills/aim/assets/hermes-contract.md` verbatim** (paths in this skill are relative to the worktree root — your cwd in a cell — not this skill's own base directory; the parts-kit style contract) — hermes composes from that catalog, it never invents markup or color.
- **Diagram ids are unique across the WHOLE set, not just within a card.** A diagram's id names both its fragment file (`<fragments-dir>/<id>.svg`) and its card file (`<set-root>/10-<id>.html` and siblings) — two diagrams sharing an id means two parallel hermes subagents silently overwrite each other's fragment (and later, card). This is a stronger rule than the arrow-marker-id relaxation above, which only relaxes uniqueness for arrow/path-id prefixes *inside* a single SVG's own markup — diagram ids themselves still need to be unique set-wide.
- **Delegate SVG diagrams to Opus subagents — design AND render.** You never write or read diagram bodies; no SVG tokens should pass through your context in either direction. For each diagram, spawn a subagent (Agent tool, `model: opus` — Claude Opus 5, the tier alias the Agent tool takes; never sonnet or haiku) that designs the layout and writes the **complete `<svg>…</svg>` fragment to its own file** under the set's fragments dir (see Artifact Set Shape above), named `<diagram-id>.svg`. The division of labor is fixed: **you decide WHAT to illustrate** (the diagram's purpose, the exact entities/flows/labels, the concrete example values, what to emphasize vs. de-emphasize), **the subagent decides HOW** (layout, which catalog parts to use, animation) — never dictate coordinates or markup in a brief, and never let a subagent invent content. Give each subagent: the diagram's purpose, the exact entities/flows/labels to show, the SVG + animation conventions above, the full `hermes-contract.md` contents verbatim (pasted inline or via an exact file copy the subagent must Read first), a card-unique arrow-marker id and path-id prefix to use (assigned by you, since subagents can't see each other's output — e.g. `arrow-target-shape`, path ids prefixed `ts-`), and the exact fragment output path. **One hermes across states**: whenever current and target state are both drawn — the Rule-2 combined architecture card above, or exceptionally separate before/after figures — assign the SAME subagent both states in a single assignment (one brief, both fragments), so styles and component placement match 1:1; never split current and target across two different subagents. Batch all of a draft's diagram subagents into one message so they run in parallel — one fragment file per subagent avoids same-file edit races. After each fragment is written, run `python3 .claude/skills/aim/assets/lint_svg.py <fragment> --marker-id arrow-<id> --prefix <prefix>` (paths relative to the worktree root — your cwd in a cell); if it exits non-zero, feed the line-numbered violations back as a fresh fix-round brief to a new subagent (max 2 fix rounds, then debug the fragment source directly yourself). Only lint-clean fragments get wrapped into cards.
- **Wrap, don't flatten.** Each lint-clean fragment becomes its own standalone diagram card at `<set-root>/10-<id>.html`, taking the next free number in `10-49`, assigned by you sequentially (needing more than the band means over-decomposition — merge; never use 3-digit prefixes): copy `.claude/skills/aim/assets/diagram-card.html` and mechanically substitute all three placeholders with a python one-liner using `str.replace` (never `re.sub` — a fragment containing `\1` or `\g` crashes `re.sub`'s replacement parsing), substituting in one pass — never let an earlier replacement's value introduce a later token. HTML-escape `&`, `<`, `>` in the title and caption — they land in markup; NEVER escape the fragment (it is markup). Fill the `{{CARD_TITLE}}` token (occurs twice: the `<title>` and the `<h1>`) and the `{{CAPTION}}` token (occurs once, in the `<figcaption>`) first, then substitute the literal `<!-- DIAGRAM: {{DIAGRAM_ID}} -->` placeholder comment with the fragment file's raw contents — e.g. `python3 -c "import pathlib, html; t = pathlib.Path('.claude/skills/aim/assets/diagram-card.html').read_text(); t = t.replace('{{CARD_TITLE}}', html.escape(TITLE)).replace('{{CAPTION}}', html.escape(CAPTION)).replace('<!-- DIAGRAM: {{DIAGRAM_ID}} -->', pathlib.Path(FRAG).read_text()); pathlib.Path(OUT).write_text(t)"` (adapt `TITLE`/`CAPTION`/`FRAG`/`OUT` to real values). All three replacements are global — never hand-edited. Verify the output contains `<svg` and does NOT contain `{{CARD_TITLE}}`, `{{CAPTION}}`, or `<!-- DIAGRAM:` — if either check fails, the wrap failed; do not ship the card. There is no derived build file and no flatten step: each card is complete and shareable the moment it's wrapped. To **edit** an existing diagram, have the subagent Read and rewrite its own fragment file, re-lint, then re-run the wrap substitution on that one card — no extraction needed.
- **`solo: true` on a kraken job means no subagents at all.** Skip the diagram pipeline entirely — no hermes subagents, no `10-<id>.html` diagram cards — and write prose-only section docs plus `00-overview.md` by hand. The worked-example diagram card requirement above does not apply when there are no diagram cards to begin with.
- Prefer tables over lists for structured sections such as components, assumptions, scope, testing plans, review guides, checklists, and questions. Use lists only for short prose-like bullets or when ordering is the point.
- **Questions: rows in `kraken.db` inside a cell, `questions.yaml` standalone — never in any artifact.** In a kraken cell, questions are **not a file** — they are rows, managed through cell-side CLI verbs that proxy HTTP to `kraken serve` (the same channel the pr-hook already uses): `kraken questions get <slug>` (full list — **re-read on every resume**, since answers arrive as silent row updates, the same contract as the old silent file edits), `kraken questions set <slug>` (bulk upsert-by-qid of the zeus-owned fields from a JSON list on stdin; deletes rows not present; preserves kraken-owned `answer`/`answered-at` on surviving rows), and `kraken questions add|update|remove <slug> ...` for per-question ops. All writes validate loudly — a bad qid shape or importance is a 400 on stderr with a non-zero exit, never a silently-dropped line. On any verb failure (validation error, connection refused because serve is down), surface the stderr and retry after fixing the input; never proceed as if the write landed and never silently drop questions. Outside a cell (standalone use), the file convention stands: a sibling `~/designs/<slug>.questions.yaml` (see Artifact Set Shape), format:
  ```yaml
  questions:
    # one item per question; single-line values; importance P0|P1|P2
    - id: Q1
      text: include model name in title?
      context: affects k ls width
      importance: P0
      recommendation: yes, abbreviated
  ```
  track answers in the conversation and update the file yourself in this mode.

  Both modes share the same fields and ids: `id` (`Q<digits>`, e.g. `Q1`), `text`, `context`, `importance` (`P0`|`P1`|`P2`), `recommendation`, plus `answer`/`answered-at` which **kraken owns exclusively** — you must never write or set those yourself. **Every question gets a stable, globally-numbered id** (`Q1`, `Q2`, …) — do not restart numbering per topic. Whenever you pose open questions to the user in chat (in the draft summary, during iteration, or via AskUserQuestion), reference them by these ids (e.g. "Q3: …") so answers map unambiguously back to the record. Keep ids stable across iterations: a resolved question's entry is removed (its id is not reused), new questions take the next unused id. Every entry must be a genuine user decision or a fact the codebase cannot answer — if you have not yet tried to answer it from the code, you are not ready to list it. Questions answered during research become Current State, Assumptions, or resolved decisions in the relevant artifact, not open-question entries.
- Keep spec and design separate. Spec defines what, why, scope, constraints, architecture shape, data concepts, and unresolved decisions. Exact APIs, algorithms, migrations, and file-by-file implementation plans belong in a dedicated design pass instead — an `/architect` or `/design` personal skill if one exists in this session, otherwise a design doc or section written by hand.
- When the user asks for a decoupled/composable system, describe capabilities, contracts, ownership boundaries, and composition principles without locking low-level implementation details.
- Leave many open questions. Prefer too many precise questions over a falsely complete set.
- Include testing early. Every component should have a brief unit/integration/e2e testing plan when applicable, and `52-testing-plan.md` should carry an overall testing plan.
- After writing or editing the set, share the **`00-overview.md`** path plus the set root so the user can open any card or section doc directly — never run `open` (or otherwise launch a browser) yourself. Sets are purely local files — no cloud/Artifact publishing.

## Workflow

### 1. Frame The Request

Restate intent in 2-4 bullets:

- what outcome the user wants
- what system or workflow is affected
- what constraints are already known
- what is explicitly out of scope or deferred, if known

### 2. Research Enough Context

For repo work:

- Read the relevant root/module `AGENTS.md`.
- Use `rg` to find the active entrypoints, types, and existing docs.
- Inspect enough files to understand current behavior, existing abstractions, and naming.
- Cite important local files in the artifacts where useful.

Before drafting, do a question-resolution pass: jot down every uncertainty the set will need to resolve, then split them. For each, decide whether it is **codebase-answerable** (a path, an existing behavior, a type, a config value, a schema column, an env flag, how a current flow works, whether something already exists) or a **genuine user decision** (intent, scope, priority, tradeoff, external constraint). Investigate and answer all codebase-answerable ones now — with `rg`, file reads, schema/config lookups, git history, or a quick query — and fold the findings into the draft (as current-state prose, assumptions backed by evidence, or resolved decisions). Only genuine user decisions survive into the open-questions record. When unsure which bucket a question is in, spend a search to find out before defaulting to asking.

For broad or multi-area investigations, fan out parallel `Explore` agents to resolve the codebase-answerable questions concurrently rather than serially.

Stop research when the first draft can be written responsibly and no codebase-answerable question remains unresolved. Deep tracing belongs in research/design unless the set would otherwise be misleading.

### 3. Choose Mode And Set Up The Set Root

Choose one mode before writing:

- **Spec mode**: use when the artifacts propose a new system, feature, refactor, migration, or future behavior.
- **Explain mode**: use when the artifacts describe an existing/current system, current code behavior, an incident-relevant workflow, or how to review/debug something that already exists.

Create the set root (see Artifact Set Shape for the exact path in a cell vs. standalone) and write artifacts directly into it — there is no HTML template to copy and fill in for `00-overview.md` or the section docs; write them as plain markdown following the structure in Draft The Artifacts below. Diagram cards DO start from a template: copy `.claude/skills/aim/assets/diagram-card.html` (paths relative to the worktree root — your cwd in a cell) per diagram, per the wrap rule in Core Rules.

Use a descriptive slug for the set root based on the feature/system, not a vague name like `doc`.

> **Legacy fallback.** `.claude/skills/aim/assets/spec-template.html` and `explain-template.html` — the old single-file, fill-in-the-blanks templates — still exist on disk for personal, non-cell, single-file use if you explicitly want one flattened doc instead of a set. They are not the primary flow; do not reach for them unless asked.

If the user explicitly wants the established repo design template, read/copy from `designs/TEMPLATE.design.md` or `designs/TEMPLATE.design.html`, but write the resulting artifacts under the set root above.

If a personal `/feature` skill exists in this session and invoked this one, use spec mode and that feature flow's expected structure.

### 4. Draft The Artifacts

Write `00-overview.md` first (very concise — 1-3 sentences per subsection): the change/system, goal, current state, target shape / approach, and a one-line pointer to the diagram cards and section docs that carry the detail. Both modes share the same underlying progression; explain mode replaces proposed goals with current-system explanation and verification guidance:

1. **`00-overview.md`**: goal, current state, target shape / approach, data model / core concepts — each 1-3 sentences, diagrams referenced but not embedded.
2. **Diagram cards (`1N`-`4N`)**: the two core diagrams per "Two core diagrams, then tunnel" (Core Rules) — one combined current/target architecture card (spec mode; current-state only in explain mode), every box kind-tagged, and one end-to-end worked-example flow card — plus tunnel cards into any architecture box whose internals genuinely need expanding, and UI mockups when the change touches UI.
3. **Section docs (`5N` and up)**: `50-components.md` (one fill-in table per component, **Kind** row first — the concrete runtime/artifact identity per the kind vocabulary in "Two core diagrams, then tunnel" (Core Rules) — then responsibility, inputs, outputs, side effects, **boundary** naming its boundary type (machine, process, network, package, file-ownership), unit/integration/e2e test plans — required for spec mode/build jobs), `51-code-map.md` (where things live, review guide, known touch points — required), `52-testing-plan.md` (overall unit/integration/e2e/manual/observability testing plan — required); optionally `53-assumptions.md`, `54-scope.md`, `55-constraints.md`, or similar at your discretion.
4. **Open questions**: every open question, per the format in Core Rules (`kraken.db` rows in a cell, `questions.yaml` standalone).

Draft prose and tables first, noting each diagram's id and intended content as you go (never embedded into `00-overview.md` — the overview has no figure slots), then fan out the Opus diagram subagents for all of them in parallel (each designs and renders its full SVG to its own fragment file — see the delegation rule in Core Rules), then wrap each into its numbered card. Build any UI mockup cards yourself while the diagram subagents run — mockups are never delegated.

For componentized systems, make `50-components.md` emphasize:

- **Kind** stated first for every component — the concrete runtime/artifact identity, named with a kind from the sanctioned kind list in the Shape Kit section of `.claude/skills/aim/assets/hermes-contract.md` (the same vocabulary the diagrams use, and the kind that determines the component's silhouette there)
- independently callable stages
- typed inputs/outputs or conceptual contracts
- side-effect boundaries, with the **boundary type** named (machine, process, network, package, file-ownership)
- idempotency/retry expectations
- observability hooks
- how components can be composed or skipped
- where shared code can be reused without coupling the whole pipeline
- per-component test coverage at unit, integration, and e2e levels

For codebase specs, always include a `51-code-map.md` with:

- active runtime entrypoints
- existing adapters/helpers to reuse
- files that contain source-of-truth types
- files to review first
- tests or fixtures that explain expected behavior
- likely diff surfaces, clearly marked as tentative when the set is not locked

### 5. Assemble And Share

There is no derived build file — each artifact is complete and self-contained the moment it's written or wrapped. After a round of edits:

- Wrap any newly lint-clean fragments into their numbered cards (Core Rules).
- Share the set: `00-overview.md`'s path plus the set root, so the user can open any card or section doc directly. Do not open anything yourself — the user opens files from the shared paths.

### 6. Iterate

After writing the first draft in either mode:

- Share the `00-overview.md` path and the set root again.
- Summarize the most important assumptions and the highest-risk open questions, citing each by its stable id (e.g. "Q1: …", "Q4: …").
- When asking the user to resolve questions (in chat or via AskUserQuestion), always lead with the id so their answers map back to the record.
- Wait for user edits or direction before moving into architecture/design.
- On each iteration, update the relevant artifact directly (overview, a section doc, or a card via its fragment + rewrap), and update the open-questions record to remove resolved entries and add new ones — in a cell, via `kraken questions set/add/update/remove` (never write `answer`/`answered-at` yourself); standalone, edit `questions.yaml` directly.

### 7. Hand Off To /do

Once the set is locked (P0 open questions resolved or explicitly deferred, structure stable, user signals readiness to build), point the user at [[do]] (`/do`) as the implementation step. `/do` reads the set, walks the user through any remaining `P0` questions, and writes the code. Do not implement inline from this skill — the set is the contract, `/do` is the executor.

## Mode Guidance

### Spec Mode

Use spec mode for systems that do not exist yet, changes to existing systems, migrations, refactors, product behavior proposals, and implementation alignment sets. Section docs should cover goals, non-goals, success metrics, assumptions, testing plan, and questions.

### Explain Mode

Use explain mode for systems that already exist or behavior the user wants to understand. Focus on what exists, why it exists, how data flows, how components interact, where code lives, how to debug/review it, what tests or traces verify it, and what remains uncertain. Do not propose changes unless the user asks; put possible follow-ups in the open-questions record or a notes section doc.

## Spec / Design Boundary

Spec territory:

- user-visible behavior
- lifecycle/stage behavior at a conceptual level
- high-level architecture and conceptual data models
- component responsibilities and composition boundaries
- code map and review guide for orientation
- acceptance criteria
- constraints and tradeoffs
- success metrics
- open questions
- assumptions
- test strategy

Design territory:

- database schema
- exact API signatures
- class/function names
- detailed control flow
- migration SQL
- file-by-file task breakdown

If the spec starts needing exact interfaces, mark them as open questions or move to a dedicated design pass (a personal `/architect` skill if one exists in this session, otherwise a hand-written design doc).
