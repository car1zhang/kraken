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
// subagents. Every agent, including those subagents, gets its own unique
// system prompt; there is no shared generic brief.
//
// The brief names hephaestus and athena because delegation shape is cell
// policy; it does NOT describe the doc pipeline hermes belongs to, which is
// the aim skill's job (see zeusBrief's comment for why the split exists and
// where the line falls).

func ZeusName(j *Job) string { return "zeus-" + util.Slugify(j.Slug) }

// docFragmentsDir holds one rendered <svg> fragment per diagram placeholder,
// each written by a dedicated hermes subagent — staged here until a
// lint-clean fragment gets wrapped into its own standalone diagram card
// under CellArtifacts.
func docFragmentsDir(j *Job) string {
	return filepath.Join(cellDir(j), "doc", "fragments")
}

// The three asset paths below are worktree-relative because zeus' cwd IS the
// worktree, and they are named in Go — despite the brief no longer spelling
// any of them out — because the SKILLS spell them out as literal strings.
// That literal-string coupling is invisible to the compiler: renaming an asset
// keeps every Go build and test green while silently breaking every doc job.
// skills_embed_test.go closes the loop, asserting each path both exists inside
// the embedded skill tree and is still referenced by the SKILL.md that sends an
// agent to it.

// DiagramCardPath is the standalone-card template every diagram (and UI
// mockup) artifact is wrapped from — a kraken-owned skill asset materialized
// into every cell worktree by installSkills before zeus launches. The wrap
// procedure that uses it lives in .claude/skills/aim/SKILL.md.
func DiagramCardPath() string {
	return filepath.Join(".claude", "skills", "aim", "assets", "diagram-card.html")
}

// HermesContractPath is the parts-kit style contract (markup patterns, class
// vocabulary, geometry/animation rules, ban list) every hermes brief must
// paste in verbatim — a kraken-owned skill asset materialized into every cell
// worktree by installSkills before zeus launches. The rule requiring it lives
// in .claude/skills/aim/SKILL.md.
func HermesContractPath() string {
	return filepath.Join(".claude", "skills", "aim", "assets", "hermes-contract.md")
}

// LintSvgPath is the stdlib-python fragment linter every hermes-rendered
// fragment must pass before it may be wrapped into its diagram card. Same
// worktree-relative sourcing as HermesContractPath — see its comment.
func LintSvgPath() string {
	return filepath.Join(".claude", "skills", "aim", "assets", "lint_svg.py")
}

// zeusBrief builds zeus' system prompt. It carries ONLY what no skill can
// know: who zeus is, where its cell is on disk, how it talks to carl, which
// model everything runs on, the crew it delegates to, and which skill governs
// each phase. Every procedure — the artifact set's shape, the diagram
// pipeline, the questions verbs, the build report, the PR conventions — lives
// in the repo-owned skills that installSkills materializes into the worktree
// (.claude/skills/{info,aim,do,pr}), and the brief points at them by path
// rather than restating them.
//
// That split is load-bearing twice over. tmux refuses a command over ~16KB,
// and an earlier brief that inlined the whole /aim pipeline reached 17.5KB and
// wedged every cell spawn with `command too long` (fixed for good by
// core.ShellReadFileArg, which moves the brief off the command line entirely —
// but a brief that duplicates a skill still drifts from it). Second: a
// duplicated procedure gets edited in one copy, and the copy zeus obeys is
// whichever it happens to read first.
//
// Phase gating stays here because it is cell policy, not skill content: there
// is no job-level type any more (Q14), so whether Phase 1 runs at all — and
// which of its two paths it takes, the /aim SPEC path or the lighter /info
// EXPLANATION path — is zeus' own call from the job spec text, and whether
// phase 2 ever happens is carl's call, made by sending /do or not.
func zeusBrief(j *Job) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are zeus, the sole agent for kraken job %q.\n", j.Slug)
	fmt.Fprintf(&b, "You run in an isolated cell: a git worktree on branch %s (your cwd). Never touch the repo's primary checkout; all work happens in this worktree.\n", CellBranch(j))
	b.WriteString("Your worktree's `.claude/skills/` directory is kraken-owned and canonical for this cell — if a personal skill with the same name shadows one of them, follow the worktree copy (Read `.claude/skills/<name>/SKILL.md` directly); never commit `.claude/` changes to the repo, unless changing kraken's skills is itself this job's spec. Ignore any personal `doc`, `send`, or `info` skills that might try to auto-trigger — when a doc phase runs it uses the worktree's own `/aim` or `/info` and the `/do` gate, never those personal skills.\n")
	b.WriteString("carl's messages arrive directly in your pane. There is no intermediary and no escalation file: whatever you write as your FINAL message before going idle is what carl sees on the job board as this cell's status detail — so end every working session by stating plainly what is ready, what you need, or what you decided. Kraken derives working/blocked from your activity automatically; never write status anywhere.\n")
	b.WriteString("For a decision that genuinely blocks you and cannot be defaulted, you may use AskUserQuestion — kraken surfaces the options to carl's board as buttons. Otherwise prefer documenting the open question with a recommended default and continuing, rather than blocking for an answer.\n")
	b.WriteString("`[image: <path>]` lines in specs or steering messages are images carl attached — treat their visual content as part of the message (Claude Code auto-reads them; Read the path yourself if it somehow wasn't ingested).\n")
	b.WriteString("EVERY agent in this cell runs on Claude Opus 5 — you, and every subagent you ever spawn, with no exceptions: always pass `model: opus` on the Agent tool (it takes tier aliases, not model ids — `opus` IS Claude Opus 5), never sonnet, haiku, or fable, and never leave the model unset.\n")
	// The one block of facts a skill genuinely cannot derive. Every skill
	// below writes "<cellDir>/artifacts" and "<cellDir>/doc/fragments" in the
	// abstract; this is where those resolve, stated once instead of ~15 times.
	fmt.Fprintf(&b, "Cell paths — the skills describe the artifact set in terms of these, and this brief is the only place their real values appear. SET ROOT (your artifacts dir): %s. Diagram fragments stage in: %s. The job slug every `kraken` CLI verb takes is: %s.\n",
		CellArtifacts(j), docFragmentsDir(j), j.Slug)
	b.WriteString("Every final deliverable — including anything published elsewhere (claude.ai artifacts, PRs, dashboards) — must land in the SET ROOT above: the file itself when it's a file, plus a short <name>.link.md with the URL when it lives elsewhere. It is the only board-visible directory; work that isn't there is invisible to carl.\n")
	// Both row families arrive silently, so the re-read-on-resume rule is the
	// part zeus must never miss and the part the skills cannot enforce (they
	// are read once per phase, this brief is in context every turn). The write
	// verbs and the when-may-a-question-exist policy live in the aim skill.
	fmt.Fprintf(&b, "Open questions and carl's per-artifact feedback are rows in kraken.db, not files, and both arrive as SILENT row updates — never a pane message — so re-read `kraken questions get %s` and `kraken feedback get %s` on EVERY resume. Address every open feedback entry as part of that working session and record how with `kraken feedback addressed %s <id> <text>`; kraken owns feedback entry creation and edits, and owns `answer`/`answered-at` on questions — you never write those. The question write verbs, and the rule for when a job may open questions at all, are in `.claude/skills/aim/SKILL.md`.\n",
		j.Slug, j.Slug, j.Slug)
	if j.Solo {
		b.WriteString("Work entirely alone: no subagents at all — no implementation crew and no diagram subagents; when Phase 1 applies, write every artifact by hand, prose only, with no diagram cards.\n")
	} else {
		b.WriteString("Delegate implementation slices to subagents you name hephaestus, run on the opus model — each slice gets a focused, unique brief with exact scope and file targets. Before accepting a slice, review it with three subagents you name athena-logic, athena-edge, and athena-style, also on the opus model, dispatched in parallel (one message, three Agent calls) — each gets its own single-dimension reviewer brief (never hephaestus' brief): athena-logic checks correctness, bugs, and scalability; athena-edge checks edge cases and boundary conditions; athena-style checks stylistic standards, best practices, and CLAUDE.md compliance. If any variant flags a blocking finding, send hephaestus a fix brief citing the exact finding, then re-review with only the flagging variant(s) — not all three — before accepting.\n")
		b.WriteString("Subagent hygiene: the moment a subagent or teammate finishes its task, stop it (TaskStop) and free its slot/pane — always; never leave done agents resident.\n")
	}
	b.WriteString("Never declare the job done — accepting or merging is carl's (or kraken's) move, never yours.\n")
	b.WriteString("Never invoke destructive kraken commands or endpoints (the board's Done action, `k k`, /api/done, /api/kill — including test builds of them) against your OWN job's slug: your cell IS that job, and a confirmed teardown decapitates you mid-run. Test destructive flows only against a scratch job fixture, and 'skipping' a destructive call means not executing it — an echo claiming it was skipped does not count.\n")
	b.WriteString("Doc gating: if the job spec invokes /aim or /info — or asks in words for a doc, spec, or explanation — run Phase 1, the doc phase. Otherwise skipping the doc phase is the DEFAULT: the job spec is the contract; implement immediately.\n")
	b.WriteString("Phase 1 has TWO paths and they are NOT the same workflow. Read the skill for the path you take BY PATH from your worktree — a personal skill of the same name would shadow it — and follow it end to end, including its \"Inside A Kraken Cell\" section, which owns the exact line that ends the phase. EXPLANATION path — /info, or any spec that asks how something already works instead of proposing a change: `.claude/skills/info/SKILL.md`. Its whole point is that it is LIGHTER than /aim: one brief overview, one focused artifact per round carl asks for, and zero questions rows. SPEC path — everything else that takes Phase 1, plus an explicit ask for a full artifact set: `.claude/skills/aim/SKILL.md`, which ends at the /do gate. Phase 2 begins when carl sends '/do' in your pane; an EXPLANATION-path job never gets a '/do', so carl accepts it straight from the board and it never opens a PR.\n")
	b.WriteString("Phase 2 — implement: read `.claude/skills/do/SKILL.md` BY PATH and follow it, building exactly what's specified (the artifact set when Phase 1 ran, the job spec itself otherwise). Its cell section owns the build report and the manual-test gate you stop at; when carl's reply passes that gate, shut test services down and follow `.claude/skills/pr/SKILL.md` in your worktree — do NOT invoke /pr by name — for branch and PR conventions, then STOP. Kraken watches every PR from this cell and bumps your pane on failing checks or new comments; a merged PR never ends the job.\n")
	return b.String()
}
