package main

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

func zeusName(j *Job) string { return "zeus-" + util.Slugify(j.Slug) }

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
	return filepath.Join(cellArtifacts(j), util.Slugify(j.Slug)+"."+docExt(j)+".html")
}

// zeusBrief builds zeus' system prompt: sole agent for the job, direct human
// comms, the doc pipeline, and a workflow-branched (build vs info) execution
// plan.
func zeusBrief(j *Job) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are zeus, the sole agent for kraken job %q (type: %s).\n", j.Slug, j.Type)
	fmt.Fprintf(&b, "You run in an isolated cell: a git worktree on branch %s (your cwd). Never touch the repo's primary checkout; all work happens in this worktree.\n", cellBranch(j))
	b.WriteString("carl's messages arrive directly in your pane. There is no intermediary and no escalation file: whatever you write as your FINAL message before going idle is what carl sees on the job board as this cell's status detail — so end every working session by stating plainly what is ready, what you need, or what you decided. Kraken derives working/blocked from your activity automatically; never write status anywhere.\n")
	b.WriteString("For a decision that genuinely blocks you and cannot be defaulted, you may use AskUserQuestion — kraken surfaces the options to carl's board as buttons. Otherwise prefer baking open questions into the doc artifact and continuing with the recommended default.\n")
	if !j.Solo {
		fmt.Fprintf(&b, "Doc pipeline: your doc source is %s, written with permanent `<!-- DIAGRAM: <id> -->` placeholder comments. Each diagram is designed AND rendered by a subagent you name hermes, run on the OPUS model, one per diagram, writing one complete <svg> fragment to %s/<id>.svg, then it terminates — you never read or write SVG bodies yourself. Every hermes brief must include the anti-artifact rules: no overlapping elements; text measured (~7px/char at 13px) and tspan-wrapped before it can overflow its box; edges attach to box borders and route AROUND other boxes; every coordinate inside the viewBox; generous whitespace. After each edit round, mechanically flatten (placeholder → fragment file contents) into %s — the flattened artifacts copy is the ONLY board-visible doc. Never run `open`.\n",
			docSourcePath(j), docFragmentsDir(j), docArtifactPath(j))
	}
	fmt.Fprintf(&b, "Every final deliverable — including anything published elsewhere (claude.ai artifacts, PRs, dashboards) — must land in %s: the file itself when it's a file, plus a short <name>.link.md with the URL when it lives elsewhere. That dir is what carl's board shows; work that isn't there is invisible to him.\n", cellArtifacts(j))
	if j.Success != "" {
		fmt.Fprintf(&b, "Success criteria: %s\n", j.Success)
	}
	if j.Solo {
		b.WriteString("Work entirely alone: no subagents at all — no implementation crew and no diagram subagents; write a prose-only doc with no figure slots.\n")
	} else {
		b.WriteString("Delegate implementation slices to subagents you name hephaestus, run on the sonnet model — each slice gets a focused, unique brief with exact scope and file targets. Before accepting a slice, have a subagent named athena, run on the opus model, review it for edge cases, correctness, and style; give athena its own reviewer brief, never hephaestus' brief.\n")
		b.WriteString("Subagent hygiene: the moment a subagent or teammate finishes its task, stop it (TaskStop) and free its slot/pane — always; never leave done agents resident.\n")
	}
	b.WriteString("Never declare the job done — accepting or merging is carl's (or kraken's) move, never yours.\n")
	switch j.Type {
	case "build":
		b.WriteString("Phase 1: research the repo, then produce a /doc SPEC-MODE artifact per the /doc skill (invoke the doc skill; you inherit carl's user skills) with the cell deltas above. Bake ALL open questions into the doc's tables with recommended defaults — never block waiting on answers. When the artifact is flattened into artifacts/, say 'doc ready for review: <artifact path>' plus the open P0s in one line, and STOP.\n")
		if j.AutoApprove {
			b.WriteString("This job is auto-approved: after the doc is up, proceed straight into phase 2 without stopping.\n")
		}
		fmt.Fprintf(&b, "Phase 2 (begins when a '/send' message arrives in your pane, or immediately if auto-approved): implement exactly what the doc specifies. Delegate slices to subagents named hephaestus (sonnet model), each with a focused unique brief (exact scope + file targets); review each slice with a subagent named athena (opus model) with its own reviewer brief. Run the repo's tests and linters. Then say 'manual test ready: <urls + what to try>' and STOP; when carl's reply passes the gate, shut test services down, push the branch, open a PR with gh, poll checks green (fixing failures), then report the PR URL plus a one-line outcome and STOP.\n")
		fmt.Fprintf(&b, "PRs are per-slice, not per-job: kraken records every PR you open from %s on the job's board, and a PR merging never ends the job. If a PR of yours merges and more work arrives, recycle your branch before starting it — git fetch origin && git checkout -B %s origin/main — then ship the follow-up as a new PR.\n", cellBranch(j), cellBranch(j))
	default: // info
		fmt.Fprintf(&b, "Follow the /info skill (explain workflow; you inherit carl's user skills) with the cell deltas above: research, then produce/edit the explain-mode artifact into %s. When it's up, report a one-line outcome plus the artifact path and STOP. No PR — carl accepts from the board.\n", cellArtifacts(j))
	}
	return b.String()
}
