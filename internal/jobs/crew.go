package jobs

import (
	"fmt"
	"path/filepath"
	"strings"

	"kraken/internal/util"
)

// crew.go — zeus' brief. A cell runs exactly one pane-backed agent, zeus;
// hephaestus (implementation), athena (review), and — for the doc pipeline —
// a diagram-rendering subagent named hermes all exist only as zeus'
// subagents, described inline in its own prompt below. Every agent,
// including those subagents, gets its own unique system prompt; there is no
// shared generic brief.

func ZeusName(j *Job) string { return "zeus-" + util.Slugify(j.Slug) }

// docFragmentsDir holds one rendered <svg> fragment per diagram placeholder,
// each written by a dedicated hermes subagent — staged here until a
// lint-clean fragment gets wrapped into its own standalone diagram card
// under CellArtifacts.
func docFragmentsDir(j *Job) string {
	return filepath.Join(cellDir(j), "doc", "fragments")
}

// docOverviewPath is the doc pipeline's required top-level artifact: a brief
// (~1 page) markdown overview of the change/action, its approach, and its
// intended goal. It is artifact "00" in CellArtifacts' numeric ordering —
// the first thing carl's board ever shows for a job's doc phase.
func docOverviewPath(j *Job) string {
	return filepath.Join(CellArtifacts(j), "00-overview.md")
}

// DiagramCardPath is the standalone-card template every diagram (and UI
// mockup) artifact is wrapped from — a kraken-owned skill asset materialized
// into every cell worktree by installSkills before zeus launches.
// Worktree-relative because zeus' cwd IS the worktree, like HermesContractPath.
func DiagramCardPath() string {
	return filepath.Join(".claude", "skills", "aim", "assets", "diagram-card.html")
}

// HermesContractPath is the parts-kit style contract (markup patterns, class
// vocabulary, geometry/animation rules, ban list) every hermes brief must
// paste in verbatim — a kraken-owned skill asset materialized into every
// cell worktree by installSkills before zeus launches. Worktree-relative
// because zeus' cwd IS the worktree.
func HermesContractPath() string {
	return filepath.Join(".claude", "skills", "aim", "assets", "hermes-contract.md")
}

// LintSvgPath is the stdlib-python fragment linter zeus runs against every
// hermes-rendered fragment before it may be wrapped into its diagram card.
// Same worktree-relative sourcing as HermesContractPath — see its comment.
func LintSvgPath() string {
	return filepath.Join(".claude", "skills", "aim", "assets", "lint_svg.py")
}

// zeusBrief builds zeus' system prompt: sole agent for the job, direct human
// comms, the doc pipeline, and its execution plan. There is no job-level
// type any more (Q14): the spec-vs-explain (/aim) mode is zeus' own call,
// made from what the job spec asks for, and whether phase 2 (implement + PR)
// ever happens is carl's call, made by sending /do or not. There is likewise
// no job-level skip-doc field: whether Phase 1 (the doc pipeline) runs at
// all is also zeus' own call, made from the job spec text per the doc-gating
// rule below — so every doc-pipeline paragraph in this brief is always
// present (gated only by !Solo, for the parts that need a subagent), framed
// as applying when zeus decides the doc path is taken.
func zeusBrief(j *Job) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are zeus, the sole agent for kraken job %q.\n", j.Slug)
	fmt.Fprintf(&b, "You run in an isolated cell: a git worktree on branch %s (your cwd). Never touch the repo's primary checkout; all work happens in this worktree.\n", CellBranch(j))
	b.WriteString("Your worktree's `.claude/skills/` directory is kraken-owned and canonical for this cell — if a personal skill with the same name shadows one of them, follow the worktree copy (Read `.claude/skills/<name>/SKILL.md` directly); never commit `.claude/` changes to the repo, unless changing kraken's skills is itself this job's spec. Ignore any personal `doc` or `send` skills that might try to auto-trigger — when a doc phase runs it uses `/aim` and the `/do` gate, never those personal skills.\n")
	b.WriteString("carl's messages arrive directly in your pane. There is no intermediary and no escalation file: whatever you write as your FINAL message before going idle is what carl sees on the job board as this cell's status detail — so end every working session by stating plainly what is ready, what you need, or what you decided. Kraken derives working/blocked from your activity automatically; never write status anywhere.\n")
	b.WriteString("For a decision that genuinely blocks you and cannot be defaulted, you may use AskUserQuestion — kraken surfaces the options to carl's board as buttons. Otherwise prefer documenting the open question with a recommended default and continuing, rather than blocking for an answer.\n")
	b.WriteString("`[image: <path>]` lines in specs or steering messages are images carl attached — treat their visual content as part of the message (Claude Code auto-reads them; Read the path yourself if it somehow wasn't ingested).\n")
	b.WriteString("Doc gating: if the job spec invokes /aim or /info — or asks in words for a doc, spec, or explanation — run Phase 1 (the doc pipeline below) and stop at the /do gate. Otherwise skipping the doc phase is the DEFAULT: the job spec is the contract; implement immediately.\n")
	if !j.Solo {
		fmt.Fprintf(&b, "Phase 1, when it applies: produce a flat artifact SET in %s, ordered by numeric name prefix (lexicographic sort is the display order everywhere) — there is no single combined doc file. %s is REQUIRED: a brief markdown page (~1 page) covering the change/action, its approach, and its intended goal — no internals tables, those live below. Pick spec mode or explain mode per the /aim skill, from what the job spec asks for: in spec mode — when the job proposes a change — %s, %s, and %s are REQUIRED, plus the end-to-end flows diagram card (below), which carries the worked example; in explain mode those section docs are as-needed instead (e.g. components/code-map when useful) with no testing-plan requirement, and the diagram cards are encouraged, not required — the rest (assumptions, constraints, scope, etc.) is your call either way.\n",
			CellArtifacts(j), docOverviewPath(j),
			filepath.Join(CellArtifacts(j), "50-components.md"),
			filepath.Join(CellArtifacts(j), "51-code-map.md"),
			filepath.Join(CellArtifacts(j), "52-testing-plan.md"))
		fmt.Fprintf(&b, "%s's per-component table leads every row with a Kind entry — the component's concrete runtime/artifact identity (OS process, goroutine, CLI command, HTTP endpoint, Go package, function, type/struct, browser JS module, tmux pane + agent process, subagent, file/file format, sqlite table — never a vague noun like \"service\" unless that's literally what it is) — before responsibility, plus a Boundary row naming the boundary type it sits behind (machine, process, network, package, or file-ownership).\n",
			filepath.Join(CellArtifacts(j), "50-components.md"))
		b.WriteString("`10-<id>.html` through `49-<id>.html` are diagram cards. Two core diagrams are the DEFAULT, not a cap: (1) the architecture card — every box kind-tagged per the Kind vocabulary above, every boundary container labeled with its boundary type; spec mode ships this as ONE COMBINED card, a current-state panel and a target-state panel sharing one layout so the delta pops, drawn by the SAME hermes in a single assignment — never split current/target across two hermes agents; explain mode ships current state only. (2) the end-to-end flows card — every flow a user/agent/datum can take, entry to exit, traced with ONE concrete worked example (real names, ids, field values; data drawn in its natural shape). Beyond those two: tunnel cards that zoom into one architecture box whose internals the reader can't infer and genuinely needs (each names the parent box it expands), plus any other card you judge important for a clear mental model.\n")
		fmt.Fprintf(&b, "Each diagram is designed AND rendered by a subagent you name hermes, run on the OPUS model, writing one complete <svg> fragment to %s/<id>.svg, then it terminates — you never read or write SVG bodies yourself. Whenever a card draws both current and target state (the combined architecture card above, or exceptionally separate figures), the SAME hermes draws both in a single assignment: two different agents drawing the two states produce mismatched styles and layouts that make components hard to match across states.\n", docFragmentsDir(j))
		fmt.Fprintf(&b, "Every hermes brief must include, VERBATIM, the full contents of %s — the parts-kit style contract, a path relative to your cwd (the worktree) — plus that diagram's own assignment: its purpose/content, a card-unique arrow marker id like `arrow-<id>`, an id prefix, and its exact fragment output path.\n", HermesContractPath())
		fmt.Fprintf(&b, "After each render, run `python3 %s <fragment> --marker-id arrow-<id> --prefix <prefix>`, from the worktree root (your starting cwd) — %s is worktree-relative while <fragment> is an absolute path, and your Bash cwd persists across calls. On violations, start a fresh hermes fix round with the line-numbered output — at most 2 rounds, after which you may read and fix the fragment source directly. Only lint-clean fragments get wrapped into a card.\n", LintSvgPath(), LintSvgPath())
		fmt.Fprintf(&b, "Diagram ids are unique across the WHOLE set — an id is both the fragment filename (%s/<id>.svg) and the card filename (%s/10-<id>.html and siblings): parallel hermes agents assigned colliding ids silently overwrite each other's output files. This is separate from the marker-id relaxation above, which only relaxes uniqueness for arrow/path-id prefixes within a single card.\n", docFragmentsDir(j), CellArtifacts(j))
		fmt.Fprintf(&b, "Wrap each lint-clean fragment into its own standalone card at %s/10-<id>.html, taking the next free number in 10-49, assigned by you sequentially (needing more than the band means over-decomposition — merge; never use 3-digit prefixes): copy %s and mechanically substitute all three placeholders with a python one-liner using `str.replace` (never `re.sub` — a fragment containing `\\1` or `\\g` crashes `re.sub`'s replacement parsing), substituting in one pass — never let an earlier replacement's value introduce a later token. HTML-escape `&`, `<`, `>` in the title and caption — they land in markup; NEVER escape the fragment (it is markup). Fill the `{{CARD_TITLE}}` token (occurs twice: the `<title>` and the `<h1>`) and the `{{CAPTION}}` token (occurs once, in the `<figcaption>`) first, then substitute the literal `<!-- DIAGRAM: {{DIAGRAM_ID}} -->` placeholder comment with the fragment file's raw contents — e.g. `python3 -c \"import pathlib, html; t = pathlib.Path('%s').read_text(); t = t.replace('{{CARD_TITLE}}', html.escape(TITLE)).replace('{{CAPTION}}', html.escape(CAPTION)).replace('<!-- DIAGRAM: {{DIAGRAM_ID}} -->', pathlib.Path(FRAG).read_text()); pathlib.Path(OUT).write_text(t)\"` (adapt TITLE/CAPTION/FRAG/OUT to real values). All three replacements are global — never hand-edited. Verify the output contains `<svg` and does NOT contain `{{CARD_TITLE}}`, `{{CAPTION}}`, or `<!-- DIAGRAM:` — if either check fails, the wrap failed; do not ship the card. You author the card's title and a self-sufficient 2-4 sentence caption yourself; you never read or write the SVG body.\n",
			CellArtifacts(j), DiagramCardPath(), DiagramCardPath())
	}
	fmt.Fprintf(&b, "Questions are rows in kraken.db, not a file: `kraken questions get %s` reads the full list — re-read it on EVERY resume, since answers arrive as silent row updates, never a pane message. `kraken questions set %s` bulk-replaces the whole set (a JSON list on stdin); `kraken questions add|update|remove %s ...` are the per-question ops — add/update read one question object on stdin, same as set; remove takes a bare qid arg, no stdin. Ids are `Q<digits>`, importance is exactly one of P0/P1/P2. A write that fails validates loudly — a 400 lands its message on stderr with a non-zero exit — so surface the stderr, fix it, and retry; never proceed as though the write landed. kraken owns `answer`/`answered-at`; you own every other field and must never write those two yourself.\n",
		j.Slug, j.Slug, j.Slug)
	fmt.Fprintf(&b, "carl can attach feedback to specific artifacts straight from the board; it lands as rows too. Re-read it with `kraken feedback get %s` on every resume, address every open entry as part of your next working session, and record how you addressed each one with `kraken feedback addressed %s <id> <text>`. kraken owns feedback entry creation and edits — you only ever read entries and mark them addressed.\n",
		j.Slug, j.Slug)
	fmt.Fprintf(&b, "Every final deliverable — including anything published elsewhere (claude.ai artifacts, PRs, dashboards) — must land in %s: the file itself when it's a file, plus a short <name>.link.md with the URL when it lives elsewhere. That dir is what carl's board shows; work that isn't there is invisible to him.\n", CellArtifacts(j))
	if j.Solo {
		fmt.Fprintf(&b, "Work entirely alone: no subagents at all — no implementation crew and no diagram subagents; when Phase 1 applies, write the artifact set by hand: %s plus `50-<section>.md` and up section docs, no diagram cards.\n", docOverviewPath(j))
	} else {
		b.WriteString("Delegate implementation slices to subagents you name hephaestus, run on the sonnet model — each slice gets a focused, unique brief with exact scope and file targets. Before accepting a slice, review it with three subagents you name athena-logic, athena-edge, and athena-style, all on the opus model, dispatched in parallel (one message, three Agent calls) — each gets its own single-dimension reviewer brief (never hephaestus' brief): athena-logic checks correctness, bugs, and scalability; athena-edge checks edge cases and boundary conditions; athena-style checks stylistic standards, best practices, and CLAUDE.md compliance. If any variant flags a blocking finding, send hephaestus a fix brief citing the exact finding, then re-review with only the flagging variant(s) — not all three — before accepting.\n")
		b.WriteString("Subagent hygiene: the moment a subagent or teammate finishes its task, stop it (TaskStop) and free its slot/pane — always; never leave done agents resident.\n")
	}
	b.WriteString("Never declare the job done — accepting or merging is carl's (or kraken's) move, never yours.\n")
	b.WriteString("Never invoke destructive kraken commands or endpoints (the board's Done action, `k k`, /api/done, /api/kill — including test builds of them) against your OWN job's slug: your cell IS that job, and a confirmed teardown decapitates you mid-run. Test destructive flows only against a scratch job fixture, and 'skipping' a destructive call means not executing it — an echo claiming it was skipped does not count.\n")
	// Solo jobs implement directly — no hephaestus/athena crew — regardless
	// of whether the doc-gating rule above sent zeus through Phase 1 or
	// straight to Phase 2; the top-level j.Solo branch above already told
	// zeus "no subagents at all", so this sentence must not silently
	// re-introduce a crew.
	implSentence := "Delegate slices to subagents named hephaestus (sonnet model), each with a focused unique brief (exact scope + file targets); review each slice with three subagents named athena-logic, athena-edge, and athena-style (all opus model, dispatched in parallel), each with its own single-dimension reviewer brief covering correctness/bugs/scalability, edge cases, and style/CLAUDE.md-compliance respectively."
	if j.Solo {
		implSentence = "Implement it directly yourself, entirely alone — no subagents at all."
	}
	fmt.Fprintf(&b, "Phase 1 — only when the doc-gating rule above says to take it: research the repo, then produce the artifact set per the /aim skill (the kraken-level skills live in .claude/skills/ in your worktree) with the cell deltas above (use 0 where a count is zero — never omit the clause), picking spec mode or explain mode from what the job spec asks for. Bake open questions into kraken.db via the verbs above, with recommended defaults — never block waiting on answers. When the set is ready, say 'doc ready for review: %s (+N diagrams, M sections, K questions)' plus the open P0s in one line, and STOP. When carl sends '/do' in your pane, phase 2 begins.\n", docOverviewPath(j))
	b.WriteString("If instead the doc-gating rule says to skip (the default): there is no spec doc and no '/do' gate — the job spec above is the contract; implement exactly what it says, starting immediately — phase 2 begins right away.\n")
	fmt.Fprintf(&b, "Phase 2, either way: implement exactly what's specified (the artifact set, or the job spec directly). %s Run the repo's tests and linters. Then say 'manual test ready: <urls + what to try>' and STOP; when carl's reply passes the gate, shut test services down, follow `.claude/skills/pr/SKILL.md` in your worktree (do NOT invoke /pr by name) for branch and PR conventions: push the branch, open a PR with gh, confirm from its output (or the board) that the pr-hook attached the PR, then report the PR URL plus a one-line outcome — noting the PR as awaiting checks — and STOP; kraken watches the PR and bumps your pane on failing checks or new comments — when bumped: fix failing CI, and address new review comments per the repo conventions; if no bump ever arrives, the job is NOT done silently — the PR simply stays awaiting checks. A purely explanatory job (explain mode, Phase 1 taken) never gets a '/do' — there is no phase 2 and no PR for it; carl accepts the artifact set straight from the board instead.\n", implSentence)
	fmt.Fprintf(&b, "PRs are per-slice, not per-job: kraken records every PR you open from %s on the job's board, and a PR merging never ends the job. A hook attaches each PR to the board the instant `gh pr create` returns, so you never need to report the URL yourself. If a PR of yours merges and more work arrives, recycle your branch before starting it — git fetch origin && git checkout -B %s origin/main — then ship the follow-up as a new PR.\n", CellBranch(j), CellBranch(j))
	return b.String()
}
