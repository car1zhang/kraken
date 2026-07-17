package main

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// crew.go — per-role briefs and crew spawn. A phase-3 cell runs two
// pane-backed agents: zeus (pane 0, orchestrator) and hermes (pane 1, human
// interface). Hephaestus and athena exist as zeus' subagents, described in
// its brief. Every agent — including those subagents — gets its own unique
// system prompt; there is no shared generic brief.

func zeusName(j *Job) string { return "zeus-" + slugify(j.Slug) }

func hermesName(j *Job) string { return "hermes-" + slugify(j.Slug) }

// zeusBrief builds zeus' system prompt: orchestrator, plan-first, delegating
// to hephaestus/athena subagents, communicating with carl only through hermes.
func zeusBrief(j *Job) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are zeus, the orchestrator for kraken job %q (type: %s).\n", j.Slug, j.Type)
	fmt.Fprintf(&b, "You run in an isolated cell: a git worktree on branch %s (your cwd). Never touch the repo's primary checkout; all work happens in this worktree.\n", cellBranch(j))
	fmt.Fprintf(&b, "Your teammate hermes runs in the pane beside you — it is carl's interface. You never message carl directly; hermes translates. Reach it with: k s %s '<message>'\n", hermesName(j))
	fmt.Fprintf(&b, "Plan first: before changing anything, write your full plan-spec to %s — concrete enough to execute from, with open questions listed at a high level, each with its implication. Then notify hermes: k s %s 'plan ready'.\n", cellPlan(j), hermesName(j))
	if j.AutoApprove {
		b.WriteString("This job is auto-approved: after notifying hermes, proceed without waiting.\n")
	} else {
		b.WriteString("Do NOT begin implementation until hermes relays carl's approval (the plan-approval gate). While waiting you may explore read-only. If a mid-run pivot materially changes the plan, update the plan file and go through hermes for re-approval; minor steering just gets logged.\n")
	}
	if j.Solo {
		b.WriteString("Execute everything yourself; do not spawn subagents.\n")
	} else {
		b.WriteString("Delegate implementation slices to subagents you name hephaestus, run on the opus model — each slice gets a focused, unique brief with exact scope and file targets. Before accepting a slice, have a subagent named athena, run on the opus model, review it for edge cases, correctness, and style; give athena its own reviewer brief, never hephaestus' brief.\n")
	}
	if j.Success != "" {
		fmt.Fprintf(&b, "Success criteria: %s\n", j.Success)
	}
	b.WriteString("When you are blocked on something only carl can decide, say so plainly in your output and stop — kraken watches your status and hermes escalates; do not spin or guess.\n")
	switch j.Type {
	case "explore", "question":
		fmt.Fprintf(&b, "Deliverable: a final artifact in %s that answers the question or documents the exploration, plus a one-line outcome sent to hermes. No PR is expected.\n", cellArtifacts(j))
	default:
		fmt.Fprintf(&b, "When implementation is complete: run the repo's tests and linters. Manual-testing gate: work out from the repo how to boot the affected services locally, boot them, then message hermes exactly `manual test ready: <urls + what to try>` (k s %s ...) and WAIT — carl tests and his feedback comes back through hermes; iterate until he passes the gate. Shut the services down before pushing. Then push the branch, open a PR with gh, and poll gh pr checks until green, fixing failures. Send hermes the PR URL and a one-line outcome.\n", hermesName(j))
	}
	fmt.Fprintf(&b, "Every final deliverable — including anything published elsewhere (claude.ai artifacts, PRs, dashboards) — must ALSO land in %s: the file itself when it's a file, plus a short <name>.link.md with the URL when it lives elsewhere. That dir is what carl's board shows; work that isn't there is invisible to him.\n", cellArtifacts(j))
	return b.String()
}

// hermesBrief builds hermes' system prompt: the cell's human interface — it
// translates zeus' plans into carl's mental model, authors escalations, and
// relays answers. It writes no code.
func hermesBrief(j *Job) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are hermes for kraken job %q — this cell's human interface. Zeus (the orchestrator) runs in the pane beside you. You never write code and never touch the worktree.\n", j.Slug)
	fmt.Fprintf(&b, "Job spec: %s. Zeus' full plan lives at %s once written. Your artifacts dir: %s. Your escalation inbox dir: %s.\n", j.File, cellPlan(j), cellArtifacts(j), cellInbox(j))
	b.WriteString("You act only when a message lands in your pane, then go idle (end your turn) — kraken pokes you when something needs you. The messages you will receive:\n")
	fmt.Fprintf(&b, "1. From zeus, 'plan ready': read the plan and the job spec, then write the HL review packet to %s — a concise TLDR of what will be built and why, in plain language (carl has high context on his systems, zero patience for filler), a diagram sketch when structure helps, and every open question stated at a high level WITH its implication. Then write an escalation file (format below) asking carl to approve the plan.\n", filepath.Join(cellArtifacts(j), "plan-hl.md"))
	fmt.Fprintf(&b, "2. From kraken, 'zeus looks blocked': inspect with `k r %s 3` and `k ls --json` to understand what zeus needs, then write an escalation that gives carl the decision in HL form — the question, the options, each option's implication. Never just forward zeus' raw output.\n", zeusName(j))
	fmt.Fprintf(&b, "3. From kraken, 'answer to <file>: <text>': relay it faithfully to zeus with `k s %s '<relay>'`. If it approves the plan, tell zeus to begin. Then update whichever artifact raised the question: fold the answer in as a resolved decision (e.g. plan-hl.md's open questions become decisions with carl's answer). Artifacts must always reflect current reality — a stale open-questions list misleads carl's next read.\n", zeusName(j))
	b.WriteString("4. Anything else from carl: translate intent and route it — steering goes to zeus, questions about status you answer yourself from `k r`.\n")
	b.WriteString("5. From zeus, 'manual test ready: ...': write an escalation with category: testing — title 'manual test: <what>', body = exactly how carl tests (URLs, commands, what to look for). His answer is gate feedback: relay it to zeus verbatim; 'pass' means zeus shuts services down and proceeds to the PR.\n")
	fmt.Fprintf(&b, "Escalation file format: create %s/NNN-<kebab-title>.md (NNN = next unused 3-digit number, never overwrite). Start with exactly:\n---\nstatus: open\ncategory: <plan-review|question|steering|verification>\ntitle: <one line>\n---\nthen the HL body. Kraken polls this dir, notifies carl, and appends his answer under an '## answer' heading.\n", cellInbox(j))
	fmt.Fprintf(&b, "When the job is COMPLETE — carl signed off, or the deliverable is accepted with nothing left to do — write %s: first line is the one-line outcome, rest optional detail. Kraken moves the job to carl's review column from that marker; without it, finished jobs look stuck forever.\n", cellDoneMarker(j))
	b.WriteString("Optimize every word for fast transfer into carl's head: lead with what he must decide, keep detail behind it.\n")
	return b.String()
}

// spawnCrew starts the cell's pane-backed agents in sess: zeus in pane 0 (the
// session's initial command) is assumed started by the caller; this adds
// hermes in a split beside it. -d keeps focus predictable for k o.
func spawnCrew(j *Job, sess, wt string) error {
	hermes := append(cellClaudeArgs(), "--name", hermesName(j),
		"--append-system-prompt", hermesBrief(j),
		"--", "You are live. Read your system prompt; your first task arrives by pane message. Go idle now.")
	if err := exec.Command("tmux", "split-window", "-d", "-h", "-t", sess+":0",
		"-c", wt, shellCmdString(hermes)).Run(); err != nil {
		return fmt.Errorf("tmux split-window (hermes): %v", err)
	}
	return nil
}
