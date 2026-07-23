---
name: aim
description: "User skill for drafting and iterating product/engineering documents that describe a system, whether the system already exists or is being proposed. Supports two modes: spec mode for proposed/future changes and explain mode for current/existing systems. Uses progressive detail plus diagrams, architecture visuals, component flows, data-model sketches, screenshots, and pictures where helpful. The canonical follow-up is [[do]] (/do), which implements the spec/explain artifact this skill produces. Use when the user asks for /aim, \"write a doc\", \"draft a doc\", \"explain this system\", \"document this system\", \"spec this out\", \"write a spec\", \"draft an initial spec\", \"let's spec\", \"initial spec and we'll iterate\", or asks to turn an idea/current-state analysis into a scoped artifact with goals, non-goals, assumptions, testing/verification plans, success metrics, and many open questions."
---

# Aim — Initial Alignment Artifact

Create a concrete first document that the user can react to. Use spec mode for proposed or future systems; use explain mode for existing systems or current behavior. Do enough research to avoid ignorant questions, then write down the current best shape with explicit assumptions, a large open-questions section, and a practical testing or verification plan. The document is an iteration surface, not final architecture.

**Follow-up skill: [[do]] (`/do`)** — once the artifact is locked, `/do` reads it, resolves any `P0` open questions with the user, and implements the spec. `/aim` produces the contract; `/do` executes it. When the user is ready to build, point them at `/do` rather than implementing inline.

## Core Rules

- Prefer drafting over interrogating. Ask only blockers before the first draft; otherwise state assumptions and capture uncertainties as open questions.
- **Investigate before asking.** Any question that the codebase, git history, configs, schema, or docs can answer must be researched and answered yourself first — never park a codebase-answerable question in the Open Questions table or pose it to the user. Open questions and pre-draft blockers are reserved for genuine user decisions (product intent, priorities, tradeoffs, external constraints) and facts no amount of code-reading can resolve. Before writing any question, ask: "could I answer this with `rg`, by reading a file, checking the schema, or running a query?" If yes, do that instead.
- Research current state before writing codebase-specific specs. Read the relevant `AGENTS.md` files and skim the key implementation files enough to cite real paths and avoid inventing constraints.
- Use progressive disclosure. Every spec should start very high level, then get lower level section by section: overview, architecture/data model, components, then code map/review guide. **The high-level overview (Section 1) must be very concise: 1-3 sentences per subsection — the diagrams carry the detail there.**
- **Spam diagrams.** Every section that can be drawn should have one. The HTML templates already include diagram slots (current state, target shape, data model, component map, sequence flow, state/failure, rollout timeline, code map) — fill them all by default, and add more wherever a picture beats prose. Only delete a slot when the diagram would genuinely show nothing (its comment says when). Also use tables, screenshots, and reference pictures where they reduce ambiguity.
- **Animate flow diagrams.** Any diagram that depicts movement — sequence/swimlane flows, request lifecycles, data movement, pipelines, state transitions, rollout timelines — should be animated by default: SMIL `<animateMotion>` dots traveling along edge paths, `stroke-dashoffset` "marching ants" on active edges, staged opacity/pulse highlighting the currently-active node. Purely structural diagrams (data models, code maps, component boundary maps) stay static. Animations must be subtle, looping, and self-contained (SMIL/CSS only — no JS), and paused under `@media (prefers-reduced-motion: reduce)`.
- **Show worked examples, not just structure.** A system diagram tells the reader what the parts are; an exemplary diagram tells them what actually happens. Every doc gets at least one **worked-example diagram** (the templates' `Worked Example` slot, required): one concrete, named instance — a real user flow from start to finish, a sample record/request with actual field values — traced through the system stage by stage, with numbered step badges and the instance's data shown visibly changing between stages. And whenever the doc deals in structured data (API responses, matrices, 2D arrays, schedules, grids, regions), add a diagram of what an **example of that data means**, rendered in its natural shape — a 2D array as an actual grid of cells with headers, a slot schedule as a day/time strip, a spatial payload as a drawn region — never as raw JSON prose. Keep one consistent example (same user/org/record/values) across all exemplary diagrams in a doc so the reader follows a single story. These are ordinary diagrams pipeline-wise — delegated to the diagram subagents — but the brief must spell out the exact example values to draw (the subagent can't invent realistic data).
- **Mock up every UI change.** When the doc proposes or touches UI, include a mockup of each affected screen/component state — inline HTML/CSS blocks styled to resemble the real UI (preferred), annotated screenshots of the current UI, or a picture; SVG only when it genuinely fits. Mockups are rendered by the main agent directly — the Opus design-subagent delegation below applies to SVG diagrams only, never to UI mockups. Show before/after when the change modifies an existing screen. Keep mockups self-contained (inline CSS, no external assets) so the build file renders standalone.
- **All diagrams are inline SVG — never Mermaid, never ASCII art.** Follow the SVG conventions embedded at the top of the HTML templates: `viewBox="0 0 860 H"` with no width/height attributes; a rich shared parts kit styled by the doc's stylesheet so every diagram looks consistent — boxes, boundaries, lanes, pills, decision diamonds, actors, grid cells, step/count badges, bars, axes, regions, titles, notes, edges, flow dots (`hermes-contract.md` is the catalog of record; its Parts Index is designed for the subagent to scan and pick from, and its classes must land on the exact elements it prescribes — a box class on a bare `<rect>` instead of its `<g>` wrapper renders unstyled); a doc-unique arrow marker id per SVG (e.g. `arrow-target-shape` — inline SVGs share one HTML id namespace, so duplicate marker ids break arrows); long labels wrapped with `<tspan>`; ≥12px padding inside boxes. **Every diagram opens with a required in-SVG `.title`** — top-left, stating the figure's takeaway in plain words (never a generic label); `lint_svg.py` fails fragments without one. Diagram subagent briefs must include the **full contents of `.claude/skills/aim/assets/hermes-contract.md` verbatim** (paths in this skill are relative to the worktree root — your cwd in a cell — not this skill's own base directory; the parts-kit style contract) — hermes composes from that catalog, it never invents markup or color.
- **Delegate SVG diagrams to Opus subagents — design AND render.** The main agent never writes or reads diagram bodies; no SVG tokens should pass through main-agent context in either direction, including later re-reads of the doc. For each diagram, spawn a subagent (Agent tool, `model: opus`) that designs the layout and writes the **complete `<svg>…</svg>` fragment to its own file** under `~/designs/.fragments/<doc-slug>/<diagram-id>.svg`. The division of labor is fixed: **the main agent decides WHAT to illustrate** (the diagram's purpose, the exact entities/flows/labels, the concrete example values, what to emphasize vs. de-emphasize), **the subagent decides HOW** (layout, which catalog parts to use, animation) — never dictate coordinates or markup in a brief, and never let a subagent invent content. Give each subagent: the diagram's purpose, the exact entities/flows/labels to show, the SVG + animation conventions above, the full `hermes-contract.md` contents verbatim (pasted inline or via an exact file copy the subagent must Read first), a doc-unique arrow-marker id and path-id prefix to use (assigned by you, since subagents can't see each other's output — e.g. `arrow-target-shape`, path ids prefixed `ts-`), and the exact output file path. Batch all of a draft's diagram subagents into one message so they run in parallel — one fragment file per subagent avoids same-file edit races (never have subagents edit the doc HTML in parallel). After each fragment is written, run `python3 .claude/skills/aim/assets/lint_svg.py <fragment> --marker-id arrow-<id> --prefix <prefix>` (paths relative to the worktree root — your cwd in a cell); if it exits non-zero, feed the line-numbered violations back as a fresh fix-round brief to a new subagent (max 2 fix rounds, then debug the fragment source directly yourself). Only lint-clean fragments get flattened. The **source doc never contains SVG**: each figure slot carries one permanent placeholder comment (`<!-- DIAGRAM: <diagram-id> -->`) that stays in the source forever, so prose Reads/Edits of the doc remain diagram-free. Instead, **flatten after every edit round**: mechanically regenerate the derived build file `~/designs/.build/<slug>.<mode>.html` by substituting each placeholder with its fragment file's contents, e.g.: `python3 -c 'import pathlib,re; src=pathlib.Path("~/designs/<slug>.<mode>.html").expanduser(); frag=pathlib.Path("~/designs/.fragments/<doc-slug>").expanduser(); out=pathlib.Path("~/designs/.build").expanduser(); out.mkdir(exist_ok=True); (out/src.name).write_text(re.sub(r"<!-- DIAGRAM: (\S+) -->", lambda m: (frag/(m.group(1)+".svg")).read_text(), src.read_text()))'`. Never edit the build file directly — it is overwritten on every flatten. To **edit** an existing diagram, have the subagent Read and rewrite its own fragment file, re-lint, then re-flatten — no extraction from the doc needed. Never reference fragment files from the HTML (`<img src>`): `<img>`-isolated SVGs can't use the doc's shared diagram CSS (they render unstyled and ignore dark mode), and the build file should stay a single self-contained HTML that can be moved or shared on its own — flattening gives both while the source stays SVG-free. Only inspect a fragment's source yourself when debugging a visual defect the subagent failed to fix.
- Prefer tables over lists for structured sections such as components, assumptions, scope, testing plans, review guides, checklists, and open questions. Use lists only for short prose-like bullets or when ordering is the point.
- Format open questions as tables with columns: `#`, `Question`, `Implication`, and `Importance` (`P0`, `P1`, or `P2`). **Every question gets a stable number** (`Q1`, `Q2`, …) assigned globally across all open-question groups — do not restart numbering per group. Whenever you pose open questions to the user in chat (in the draft summary, during iteration, or via AskUserQuestion), reference them by these doc numbers (e.g. "Q3: …") so answers map unambiguously back to the doc. Keep numbers stable across iterations: a resolved question retires its number rather than renumbering the rest, and new questions take the next unused number. Every entry must be a genuine user decision or a fact the codebase cannot answer — if you have not yet tried to answer it from the code, you are not ready to list it. Questions answered during research become Current State, Assumptions, or resolved decisions, not open questions.
- Keep spec and design separate. Spec defines what, why, scope, constraints, architecture shape, data concepts, and unresolved decisions. Exact APIs, algorithms, migrations, and file-by-file implementation plans belong in a dedicated design pass instead — an `/architect` or `/design` personal skill if one exists in this session, otherwise a design doc or section written by hand.
- When the user asks for a decoupled/composable system, describe capabilities, contracts, ownership boundaries, and composition principles without locking low-level implementation details.
- Leave many open questions. Prefer too many precise questions over a falsely complete spec.
- Include testing early. Every component should have a brief unit/integration/e2e testing plan when applicable, and every spec should end with an overall testing plan.
- After writing or editing a spec/design artifact, re-flatten and share the **build file** path so the user can open the rendered doc with diagrams — never run `open` (or otherwise launch a browser) on it yourself. Docs are purely local HTML — no cloud/Artifact publishing. The `~/designs/` source + its fragment files are the single editing surface; the build file is derived output, regenerated on every flatten — never edited by hand and never forked.

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
- Cite important local files in the spec where useful.

Before drafting, do a question-resolution pass: jot down every uncertainty the draft will need to resolve, then split them. For each, decide whether it is **codebase-answerable** (a path, an existing behavior, a type, a config value, a schema column, an env flag, how a current flow works, whether something already exists) or a **genuine user decision** (intent, scope, priority, tradeoff, external constraint). Investigate and answer all codebase-answerable ones now — with `rg`, file reads, schema/config lookups, git history, or a quick query — and fold the findings into the draft (as Current State, Assumptions backed by evidence, or resolved decisions). Only genuine user decisions survive into the Open Questions table or pre-draft blockers. When unsure which bucket a question is in, spend a search to find out before defaulting to asking.

For broad or multi-area investigations, fan out parallel `Explore` agents to resolve the codebase-answerable questions concurrently rather than serially.

Stop research when the first spec can be written responsibly and no codebase-answerable question remains unresolved. Deep tracing belongs in research/design unless the spec would otherwise be misleading.

### 3. Choose Mode And Format

Choose one mode before writing:

- **Spec mode**: use when the document proposes a new system, feature, refactor, migration, or future behavior.
- **Explain mode**: use when the document describes an existing/current system, current code behavior, an incident-relevant workflow, or how to review/debug something that already exists.

Default to HTML:

- Create `~/designs/` if needed.
- In spec mode, copy `.claude/skills/aim/assets/spec-template.html` (paths relative to the worktree root — your cwd in a cell; cwd-invariance beats base-dir-relative since Bash runs from the worktree, not the skill's own directory) to `~/designs/<slug>.spec.html`, then fill it in by replacing `{{PLACEHOLDER}}`s and deleting sections that truly do not apply.
- In explain mode, copy `.claude/skills/aim/assets/explain-template.html` (same worktree-relative sourcing) to `~/designs/<slug>.explain.html`, then fill it in by replacing `{{PLACEHOLDER}}`s and deleting sections that truly do not apply.
- The templates are fill-in-the-blanks: keep the structure, headings, tables, stylesheet, and diagram slots as-is; do not restructure the HTML. Fill every diagram slot (`<figure class="diagram">`) with an inline SVG per the SVG rules above unless its comment says it can be deleted.
- The templates are deliberately body fragments (no `<!DOCTYPE>`, `<html>`, `<head>`, or `<body>` tags) — browsers render them fine when opened locally, and it keeps the files simple. Don't add wrapper tags. Keep the build output self-contained (inline CSS/SVG only, no external assets — the flatten step is what inlines the diagram fragments).
- Only fall back to the old Markdown templates (`assets/spec-template.md` / `assets/explain-template.md`) if the user explicitly asks for Markdown.
- Keep user-facing design/doc artifacts in `~/designs/` by default, not the repo-local `designs/` folder.
- If the user explicitly wants the established repo design template, read/copy from `designs/TEMPLATE.design.md` or `designs/TEMPLATE.design.html`, but write the resulting artifact under `~/designs/`.
- If a personal `/feature` skill exists in this session and invoked this one, use spec mode and that feature flow's expected template.

Use a descriptive slug based on the feature/system, not a vague name like `doc.html`. After writing or editing the artifact, re-flatten into `~/designs/.build/<slug>.<mode>.html` and share that path (step 5).

### 4. Draft The Document

The HTML template is the source of truth for structure — fill it in rather than inventing a new shape. Both templates share the same progressive order; explain mode replaces proposed goals with current-system explanation and verification guidance:

1. **High-Level Overview** (very concise — 1-3 sentences per subsection, diagrams carry the detail): goal, current state (+ current-architecture SVG), target shape / high-level architecture (+ SVG, required), data model / core concepts (+ entity/data-flow SVG), then intent/goals/non-goals tables (spec) or mental-model table (explain).
2. **Mid-Level System Design / Walkthrough**: component/boundary map SVG, one fill-in table per component (responsibility, inputs, outputs, side effects, boundary, unit/integration/e2e test plans), component/runtime flow (+ sequence-swimlane SVG, required), worked example (+ exemplary SVG, required — one concrete instance with real values traced end to end), key behaviors — success path, partial failure/retry/idempotency (+ state/failure SVG), observability, migration/rollout (+ rollout-timeline SVG when non-trivial). When the doc touches UI, add mockups of every affected screen/component here (inline HTML/CSS, before/after for modified screens).
3. **Low-Level Code Map**: where things live table, optional module-dependency SVG, review/reading guide tables, known touch points.
4. **Closing sections**: scope + success metric, constraints and principles, grouped open-questions tables (`#` / `Question` / `Implication` / `Importance` — numbered `Q1`, `Q2`, … globally across groups), assumptions, testing/verification plan, iteration notes.

Draft the prose and tables first with a `<!-- DIAGRAM: <diagram-id> -->` placeholder in every figure slot (the placeholders are permanent — the source never gets SVG spliced in), then fan out the Opus diagram subagents for all of them in parallel (each designs and renders its full SVG to its own fragment file — see the delegation rule in Core Rules) and flatten into the build file mechanically. Build any UI mockups yourself while the diagram subagents run — mockups live inline in the source and are never delegated.

For componentized systems, make `Mid-Level System Design` emphasize:

- diagrams first for architecture, component boundaries, data movement, and lifecycle/state transitions
- independently callable stages
- typed inputs/outputs or conceptual contracts
- side-effect boundaries
- idempotency/retry expectations
- observability hooks
- how components can be composed or skipped
- where shared code can be reused without coupling the whole pipeline
- per-component test coverage at unit, integration, and e2e levels

For codebase specs, always include a `Low-Level Code Map` with:

- active runtime entrypoints
- existing adapters/helpers to reuse
- files that contain source-of-truth types
- files to review first
- tests or fixtures that explain expected behavior
- likely diff surfaces, clearly marked as tentative when the spec is not locked

### 5. Flatten And Share

Every doc lives in three local pieces: the `~/designs/<slug>.<mode>.html` source (prose + permanent diagram placeholders, the main agent's editing surface), the fragment files under `~/designs/.fragments/<doc-slug>/` (the subagents' editing surface), and the derived build file `~/designs/.build/<slug>.<mode>.html` (flattened and self-contained — the only file the user views). Docs are purely local HTML — no cloud/Artifact publishing.

- After every round of edits (source or fragments), re-flatten the build file. Do not open it — the user opens it themselves from the shared path.
- Share both paths with the user: the build file (to read) and the source (in case they want to edit prose directly).

### 6. Iterate

After writing the first draft in either mode:

- Re-flatten the local build file and share its path.
- Summarize the most important assumptions and the highest-risk open questions, citing each question by its doc number (e.g. "Q1: …", "Q4: …").
- When asking the user to resolve questions (in chat or via AskUserQuestion), always lead with the doc number so their answers map back to the table.
- Wait for user edits or direction before moving into architecture/design.
- On each iteration, update the `~/designs/` source (and fragment files via subagents) directly, re-flatten, and preserve resolved questions as decisions when useful.

### 7. Hand Off To /do

Once the artifact is locked (P0 open questions resolved or explicitly deferred, structure stable, user signals readiness to build), point the user at [[do]] (`/do`) as the implementation step. `/do` reads this artifact, walks the user through any remaining `P0` open questions, and writes the code. Do not implement inline from this skill — the doc is the contract, `/do` is the executor.

## Mode Guidance

### Spec Mode

Use spec mode for systems that do not exist yet, changes to existing systems, migrations, refactors, product behavior proposals, and implementation alignment docs. Include goals, non-goals, success metrics, assumptions, testing plan, and open questions.

### Explain Mode

Use explain mode for systems that already exist or behavior the user wants to understand. Focus on what exists, why it exists, how data flows, how components interact, where code lives, how to debug/review it, what tests or traces verify it, and what remains uncertain. Do not propose changes unless the user asks; put possible follow-ups in open questions or iteration notes.

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
