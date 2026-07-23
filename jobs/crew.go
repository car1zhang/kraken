package jobs

import (
	"fmt"
	"path/filepath"
	"strings"

	"kraken/util"
)

// crew.go — zeus' brief. A cell runs exactly one pane-backed agent, zeus;
// hephaestus (implementation), athena (review), and — for the doc pipeline —
// a diagram-rendering subagent named hermes all exist only as zeus'
// subagents, described inline in its own prompt below. Every agent,
// including those subagents, gets its own unique system prompt; there is no
// shared generic brief.

func ZeusName(j *Job) string { return "zeus-" + util.Slugify(j.Slug) }

// docExt is the doc pipeline's file extension for j's workflow: build jobs
// produce a SPEC-MODE doc, info jobs an EXPLAIN-MODE doc.
func docExt(j *Job) string {
	if j.Type == "build" {
		return "spec"
	}
	return "explain"
}

// docSourcePath is the doc pipeline's editable source: placeholders live
// here until flattened into the board-visible artifacts copy.
func docSourcePath(j *Job) string {
	return filepath.Join(cellDir(j), "doc", util.Slugify(j.Slug)+"."+docExt(j)+".html")
}

// docFragmentsDir holds one rendered <svg> fragment per diagram placeholder,
// each written by a dedicated hermes subagent.
func docFragmentsDir(j *Job) string {
	return filepath.Join(cellDir(j), "doc", "fragments")
}

// docArtifactPath is the flattened, board-visible copy of the doc — the ONLY
// one carl's board ever renders.
func docArtifactPath(j *Job) string {
	return filepath.Join(CellArtifacts(j), util.Slugify(j.Slug)+"."+docExt(j)+".html")
}

// HermesContractPath is the parts-kit style contract (markup patterns, class
// vocabulary, geometry/animation rules, ban list) every hermes brief must
// paste in verbatim — a kraken-owned skill asset materialized into every
// cell worktree by installSkills before zeus launches. Worktree-relative
// because zeus' cwd IS the worktree.
func HermesContractPath() string {
	return filepath.Join(".claude", "skills", "spec", "assets", "hermes-contract.md")
}

// LintSvgPath is the stdlib-python fragment linter zeus runs against every
// hermes-rendered fragment before it may be flattened into the artifact.
// Same worktree-relative sourcing as HermesContractPath — see its comment.
func LintSvgPath() string {
	return filepath.Join(".claude", "skills", "spec", "assets", "lint_svg.py")
}

// zeusBrief builds zeus' system prompt: sole agent for the job, direct human
// comms, the doc pipeline, and a workflow-branched (build vs info) execution
// plan.
func zeusBrief(j *Job) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are zeus, the sole agent for kraken job %q (type: %s).\n", j.Slug, j.Type)
	fmt.Fprintf(&b, "You run in an isolated cell: a git worktree on branch %s (your cwd). Never touch the repo's primary checkout; all work happens in this worktree.\n", CellBranch(j))
	b.WriteString("Your worktree's `.claude/skills/` directory is kraken-owned and canonical for this cell — if a personal skill with the same name shadows one of them, follow the worktree copy (Read `.claude/skills/<name>/SKILL.md` directly); never commit `.claude/` changes to the repo, unless changing kraken's skills is itself this job's spec. Ignore any personal `doc` or `send` skills that might try to auto-trigger — this pipeline is `/spec` then a `/do` gate, nothing else.\n")
	b.WriteString("carl's messages arrive directly in your pane. There is no intermediary and no escalation file: whatever you write as your FINAL message before going idle is what carl sees on the job board as this cell's status detail — so end every working session by stating plainly what is ready, what you need, or what you decided. Kraken derives working/blocked from your activity automatically; never write status anywhere.\n")
	b.WriteString("For a decision that genuinely blocks you and cannot be defaulted, you may use AskUserQuestion — kraken surfaces the options to carl's board as buttons. Otherwise prefer baking open questions into the doc artifact and continuing with the recommended default.\n")
	b.WriteString("`[image: <path>]` lines in specs or steering messages are images carl attached — treat their visual content as part of the message (Claude Code auto-reads them; Read the path yourself if it somehow wasn't ingested).\n")
	if !j.Solo && !j.SkipDoc {
		fmt.Fprintf(&b, "Doc pipeline: your doc source is %s, written with permanent `<!-- DIAGRAM: <id> -->` placeholder comments. Each diagram is designed AND rendered by a subagent you name hermes, run on the OPUS model, one per diagram, writing one complete <svg> fragment to %s/<id>.svg, then it terminates — you never read or write SVG bodies yourself.\n",
			docSourcePath(j), docFragmentsDir(j))
		fmt.Fprintf(&b, "Every hermes brief must include, VERBATIM, the full contents of %s — the parts-kit style contract, a path relative to your cwd (the worktree) — plus that diagram's own assignment: its purpose/content, a doc-unique arrow marker id like `arrow-<id>`, an id prefix, and its exact output path.\n", HermesContractPath())
		fmt.Fprintf(&b, "After each render, run `python3 %s <fragment> --marker-id arrow-<id> --prefix <prefix>`, from the worktree root (your starting cwd) — %s is worktree-relative while <fragment> is an absolute path, and your Bash cwd persists across calls. On violations, start a fresh hermes fix round with the line-numbered output — at most 2 rounds, after which you may read and fix the fragment source directly. Only lint-clean fragments get flattened.\n", LintSvgPath(), LintSvgPath())
		fmt.Fprintf(&b, "Mechanically flatten (placeholder → fragment file contents) into %s — the flattened artifacts copy is the ONLY board-visible doc. Never run `open`.\n", docArtifactPath(j))
	}
	fmt.Fprintf(&b, "Every final deliverable — including anything published elsewhere (claude.ai artifacts, PRs, dashboards) — must land in %s: the file itself when it's a file, plus a short <name>.link.md with the URL when it lives elsewhere. That dir is what carl's board shows; work that isn't there is invisible to him.\n", CellArtifacts(j))
	if j.Solo {
		b.WriteString("Work entirely alone: no subagents at all — no implementation crew and no diagram subagents; write a prose-only doc with no figure slots.\n")
	} else {
		b.WriteString("Delegate implementation slices to subagents you name hephaestus, run on the sonnet model — each slice gets a focused, unique brief with exact scope and file targets. Before accepting a slice, review it with three subagents you name athena-logic, athena-edge, and athena-style, all on the opus model, dispatched in parallel (one message, three Agent calls) — each gets its own single-dimension reviewer brief (never hephaestus' brief): athena-logic checks correctness, bugs, and scalability; athena-edge checks edge cases and boundary conditions; athena-style checks stylistic standards, best practices, and CLAUDE.md compliance. If any variant flags a blocking finding, send hephaestus a fix brief citing the exact finding, then re-review with only the flagging variant(s) — not all three — before accepting.\n")
		b.WriteString("Subagent hygiene: the moment a subagent or teammate finishes its task, stop it (TaskStop) and free its slot/pane — always; never leave done agents resident.\n")
	}
	b.WriteString("Never declare the job done — accepting or merging is carl's (or kraken's) move, never yours.\n")
	b.WriteString("Never invoke destructive kraken commands or endpoints (k done, k k, /api/done, /api/kill — including test builds of them) against your OWN job's slug: your cell IS that job, and a confirmed teardown decapitates you mid-run. Test destructive flows only against a scratch job fixture, and 'skipping' a destructive call means not executing it — an echo claiming it was skipped does not count.\n")
	switch j.Type {
	case "build":
		if j.SkipDoc {
			// skip-doc: no spec doc and no /do wait — the job spec IS
			// the contract. The manual-test gate and PR pipeline stay.
			b.WriteString("This job skips the doc phase: there is no spec doc and no '/do' gate — the job spec above is the contract; implement exactly what it says, starting immediately. Delegate slices to subagents named hephaestus (sonnet model), each with a focused unique brief (exact scope + file targets); review each slice with three subagents named athena-logic, athena-edge, and athena-style (all opus model, dispatched in parallel), each with its own single-dimension reviewer brief covering correctness/bugs/scalability, edge cases, and style/CLAUDE.md-compliance respectively. Run the repo's tests and linters. Then say 'manual test ready: <urls + what to try>' and STOP; when carl's reply passes the gate, shut test services down, follow `.claude/skills/pr/SKILL.md` in your worktree (do NOT invoke /pr by name) for branch and PR conventions: push the branch, open a PR with gh, poll checks green (fixing failures), then report the PR URL plus a one-line outcome and STOP.\n")
		} else {
			b.WriteString("Phase 1: research the repo, then produce a SPEC-MODE doc artifact per the /spec skill (the kraken-level skills live in .claude/skills/ in your worktree) with the cell deltas above. Bake ALL open questions into the doc's tables with recommended defaults — never block waiting on answers. When the artifact is flattened into artifacts/, say 'doc ready for review: <artifact path>' plus the open P0s in one line, and STOP.\n")
			fmt.Fprintf(&b, "Phase 2 (begins when a '/do' message arrives in your pane): implement exactly what the doc specifies. Delegate slices to subagents named hephaestus (sonnet model), each with a focused unique brief (exact scope + file targets); review each slice with three subagents named athena-logic, athena-edge, and athena-style (all opus model, dispatched in parallel), each with its own single-dimension reviewer brief covering correctness/bugs/scalability, edge cases, and style/CLAUDE.md-compliance respectively. Run the repo's tests and linters. Then say 'manual test ready: <urls + what to try>' and STOP; when carl's reply passes the gate, shut test services down, follow `.claude/skills/pr/SKILL.md` in your worktree (do NOT invoke /pr by name) for branch and PR conventions: push the branch, open a PR with gh, poll checks green (fixing failures), then report the PR URL plus a one-line outcome and STOP.\n")
		}
		fmt.Fprintf(&b, "PRs are per-slice, not per-job: kraken records every PR you open from %s on the job's board, and a PR merging never ends the job. A hook attaches each PR to the board the instant `gh pr create` returns, so you never need to report the URL yourself. If a PR of yours merges and more work arrives, recycle your branch before starting it — git fetch origin && git checkout -B %s origin/main — then ship the follow-up as a new PR.\n", CellBranch(j), CellBranch(j))
	default: // info
		fmt.Fprintf(&b, "Read `.claude/skills/info/SKILL.md` in your worktree and follow it — do NOT invoke /info by name; a same-named personal skill shadows the worktree copy. It's the explain workflow; apply it with the cell deltas above: research, then produce/edit the explain-mode artifact into %s. When it's up, report a one-line outcome plus the artifact path and STOP. No PR — carl accepts from the board.\n", CellArtifacts(j))
	}
	return b.String()
}
