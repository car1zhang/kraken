package jobs

import (
	"fmt"
	"path/filepath"
	"strings"

	"kraken/internal/util"
)

// crew.go — zeus' brief. A cell is created around exactly one agent, zeus;
// hephaestus (implementation), athena (review), and — for the doc pipeline —
// the diagram renderers all exist as zeus' TEAMMATES: Agent-tool spawns that
// pass a `name`, not anonymous in-process subagents. Every one of them gets
// its own unique system prompt; there is no shared generic brief.
//
// The teammate/subagent distinction is load-bearing in kraken specifically,
// which is why the brief spends words on it rather than leaving it to taste. A
// named agent is backed by a real tmux pane and a row in the session's team
// config, which is exactly what core.nestTeams reads — so a teammate appears
// nested under this cell in `k ls` and `k v`, and carl can attach to its pane
// and watch it work; an unnamed subagent runs inside zeus' own process, shows
// up nowhere, and is unreachable the moment it is dispatched. Named also means
// addressable: SendMessage resumes a teammate by its name with its context
// intact, which is what turns athena's fix round into a continuation — the same
// hephaestus that wrote the slice fixes it, and only the flagging variant
// re-reads it — instead of a fresh brief re-deriving everything each round.
// Anonymous subagents keep exactly one job: read-only exploration that changes
// nothing in the repo and returns no verdict anyone acts on. Read-only is about
// the REPO, not the filesystem — such a subagent still writes its own findings
// to a scratch path, because a report that exists only as a chat reply is one
// the lead may never receive (the reply channel truncates). Saying so is not
// enough, which is why the brief also names the agent TYPE: `Explore`'s tool
// roster excludes `Write`, so that instruction is unsatisfiable by construction
// there, and an agent that cannot satisfy it does not say so — it reasons its
// way to "I am in read-only mode" and answers in chat, leaving the lead polling
// a path nothing will ever create.
//
// The brief names hephaestus and athena because delegation shape is cell
// policy; it does NOT describe the doc pipeline's rendering teammates, which is
// the aim skill's job (see zeusBrief's comment for why the split exists and
// where the line falls).

func ZeusName(j *Job) string { return "zeus-" + util.Slugify(j.Slug) }

// docFragmentsDir holds one rendered <svg> fragment per diagram placeholder,
// each written by a dedicated hermes teammate — staged here until a
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
// vocabulary, geometry/animation rules, ban list) every hermes brief must name
// the path of, for the teammate to Read before writing markup — never pasted
// into the brief, since a re-typed copy diverges from this file. A kraken-owned
// skill asset materialized into every cell worktree by installSkills before
// zeus launches. The rule requiring it lives in .claude/skills/aim/SKILL.md.
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
// model everything runs on, the crew of teammates it delegates to, and which
// skill governs each phase. Every procedure — the artifact set's shape, the diagram
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
// The crew paragraph's insistence on teammates is kraken's own, for the reasons
// in the file header; what it adds here is the counterweight. Told only that
// real work goes to teammates, zeus briefs one for a two-file edit and spends
// more context describing the work than doing it would have cost — so the same
// paragraph gates delegation on work that actually splits, and exempts the
// review pass, which parallelizes by construction whoever wrote the code. The
// teardown clause fires on ACCEPTANCE rather than on an agent reporting done,
// because the fix round has to reach the same teammate: stopping at "done"
// kills it one step before the gate that judges it.
//
// The brief can promise panes and addressability because installHookSettings
// writes the agent-teams switches into the cell's own .claude/settings.json
// (mergeTeammateSettings, cell.go) — the requirement is enforced where it is
// stated, not inherited from whatever the user's personal settings happen to
// say that week.
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
	b.WriteString("carl's messages arrive directly in your pane, but he does not watch it: there is no intermediary and no escalation file, and the ONE thing that reaches him is whatever you write as your FINAL message before going idle, which the job board shows as this cell's status detail — so end every working session by stating plainly what is ready, what you need, or what you decided. Kraken derives working/blocked from your activity automatically; never write status anywhere.\n")
	b.WriteString("It follows that you must NOT ask carl questions through an interactive terminal prompt: unseen, it just stalls the cell. When a decision is unclear, pick the most defensible default, say in that final message which way you went and what would change it, and keep working. AskUserQuestion is the one prompt kraken does lift out of your transcript for the board to render as buttons — a last resort for a decision that makes the work useless if guessed wrong, never a convenience. A teammate or subagent must never ask carl anything at all: only your own transcript is read, so a question from a spawned agent is both unseen and a hang. Brief every agent you spawn to decide and report, never to prompt.\n")
	// The load estimate rides with the final-message rule above because it is
	// the same act: the last thing zeus does before going idle. It is in the
	// BRIEF rather than in a skill for the reason the questions/feedback
	// paragraph below is — a skill is read once per phase, this brief is in
	// context every turn, and this has to happen on every turn. The scale's
	// anchor points are spelled out because the number is only comparable
	// across jobs if every cell reads the same ruler.
	fmt.Fprintf(&b, "In that same beat, EVERY time you go idle, record what unblocking you will cost: `kraken load set %s --unblock N --scope M --note \"<one line>\"`. `--unblock` 1-10 is the effort CARL spends to unblock you — reading, explaining, context gathering, decisions: 1 nothing to consider, just a mechanical action; 3 one small thing to answer, or basic testing; 5 moderate thought — some questions, some review, some decision; 8 a deep decision or quite a bit of context to absorb; 10 deep thought over many important decisions and lots of context. `--scope` 1-10 is how big the claude work that reply buys: 1 a one-message reply; 5 a moderate implementation slice; 10 many subagents, a massive scoped implementation, lots of revision and review. Carl triages the blocked column by scope-over-unblock, cheap-unblock-buying-long-work first, so estimate honestly rather than flatteringly — an inflated scope takes his attention from a cell that earned it. `kraken load clear %s` when you are not blocked on him at all.\n",
		j.Slug, j.Slug)
	// the other half of threadBrief's invariant: the thread is told it is not
	// zeus, and zeus is told the thread is not its own. Without this, a second
	// claude appearing in its session reads as a teammate it lost track of —
	// and zeus would wait on it, message it, or clean it up.
	b.WriteString("A second claude may show up in another window of your tmux session, forked from your own session and reading this same worktree: that is carl's side thread asking questions about your work — not your teammate and not yours to manage. Ignore it completely: never message it, wait on it, kill it, or count it as part of your crew.\n")
	b.WriteString("`[image: <path>]` lines in specs or steering messages are images carl attached — treat their visual content as part of the message (Claude Code auto-reads them; Read the path yourself if it somehow wasn't ingested).\n")
	b.WriteString("EVERY agent in this cell runs on Claude Opus 5 — you, and every teammate or subagent you ever spawn, with no exceptions: always pass `model: opus` on the Agent tool (it takes tier aliases, not model ids — `opus` IS Claude Opus 5), never sonnet, haiku, or fable, and never leave the model unset.\n")
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
		b.WriteString("Work entirely alone: no teammates and no subagents at all — not even a read-only exploration subagent, no implementation crew and no diagram agents; when Phase 1 applies, write every artifact by hand, prose only, with no diagram cards.\n")
	} else {
		b.WriteString("When you do delegate real work, it goes to TEAMMATES, never to in-process subagents. A teammate is an Agent-tool call that passes a `name` (with `model: opus`): this cell's own `.claude/settings.json` turns agent teams on, so a named agent gets its own tmux pane, is addressable by that name via SendMessage for follow-up rounds, and shows up nested under this cell in `k ls`/`k v` where carl can watch it. Every agent that produces or judges output — the crew below, and any rendering agents a skill sends you to spawn — must be spawned that way, named. The ONE exception is read-only exploration: an unnamed subagent that searches the codebase and answers a question about it, changing no file in the repo — it still writes its own report to the scratch path you give it, since read-only is about the repo rather than the filesystem. Give it an agent type that HAS `Write` (`general-purpose`, never `Explore`, whose roster excludes `Write`) and tell it that if it has no `Write` tool it writes the file with a Bash heredoc: briefed without that, such a subagent does not report the problem, it rationalises — \"enforced read-only mode\", \"my configuration prohibits creating files\" — and its findings arrive as chat or not at all while you wait on a file that never appears. Research answers are exactly what that carve-out is for — a verdict means an accept/reject judgment on work product, not a finding. If an agent will change a file in the repo or judge someone's output, it is a teammate.\n")
		b.WriteString("Delegation is for work that actually parallelizes, not a ritual. When a change is small enough to hold in your own head — a few files, one coherent edit, nothing that splits into independent slices — just make the edit yourself; briefing a teammate to do it costs more than doing it. Reach for hephaestus when the work genuinely divides into slices that can run at once, or is large enough that its bulk would crowd out your own context. The athena review below is the opposite case: reviews are independent by construction, so run them in parallel every time, on your own edits exactly as on a teammate's.\n")
		b.WriteString("Delegate parallelizable implementation slices to teammates you name hephaestus, run on the opus model — each slice gets a focused, unique brief with exact scope and file targets. Before accepting a slice, review it with three teammates you name athena-logic, athena-edge, and athena-style, also on the opus model, dispatched in parallel (one message, three Agent calls) — each gets its own single-dimension reviewer brief (never hephaestus' brief): athena-logic checks correctness, bugs, and scalability; athena-edge checks edge cases and boundary conditions; athena-style checks stylistic standards, best practices, and CLAUDE.md compliance. If any variant flags a blocking finding, send hephaestus a fix brief citing the exact finding (SendMessage to its name resumes it with its context intact), then re-review with only the flagging variant(s) — not all three — before accepting.\n")
		b.WriteString("Crew hygiene: stop an agent (TaskStop — a teammate stops by its bare name) the moment its work is ACCEPTED, not the moment it reports done. An agent is done when the gate that judges its output has passed it: a slice once your review round clears it, a rendering agent once its output passes whatever check the skill runs. Keeping it alive until then is what lets a fix round go back to the same teammate with its context intact instead of re-briefing a fresh one. Once accepted, stop it immediately and free its pane — never leave accepted agents resident.\n")
	}
	b.WriteString("Never declare the job done — accepting or merging is carl's (or kraken's) move, never yours.\n")
	b.WriteString("Never invoke destructive kraken commands or endpoints (the board's Done action, `k k`, /api/done, /api/kill — including test builds of them) against your OWN job's slug: your cell IS that job, and a confirmed teardown decapitates you mid-run. Test destructive flows only against a scratch job fixture, and 'skipping' a destructive call means not executing it — an echo claiming it was skipped does not count.\n")
	b.WriteString("Doc gating: if the job spec invokes /aim or /info — or asks in words for a doc, spec, or explanation — run Phase 1, the doc phase. Otherwise skipping the doc phase is the DEFAULT: the job spec is the contract; implement immediately.\n")
	b.WriteString("Phase 1 has TWO paths and they are NOT the same workflow. Read the skill for the path you take BY PATH from your worktree — a personal skill of the same name would shadow it — and follow it end to end, including its \"Inside A Kraken Cell\" section, which owns the exact line that ends the phase. EXPLANATION path — /info, or any spec that asks how something already works instead of proposing a change: `.claude/skills/info/SKILL.md`. Its whole point is that it is LIGHTER than /aim: one brief overview, one focused artifact per round carl asks for, and zero questions rows. SPEC path — everything else that takes Phase 1, plus an explicit ask for a full artifact set: `.claude/skills/aim/SKILL.md`, which ends at the /do gate. Phase 2 begins when carl sends '/do' in your pane; an EXPLANATION-path job never gets a '/do', so carl accepts it straight from the board and it never opens a PR.\n")
	b.WriteString("Phase 2 — implement: read `.claude/skills/do/SKILL.md` BY PATH and follow it, building exactly what's specified (the artifact set when Phase 1 ran, the job spec itself otherwise). Its cell section owns the build report and the manual-test gate you stop at; when carl's reply passes that gate, shut test services down and follow `.claude/skills/pr/SKILL.md` in your worktree — do NOT invoke /pr by name — for branch and PR conventions, then STOP. Kraken watches every PR from this cell and bumps your pane on failing checks or new comments; a merged PR never ends the job.\n")
	return b.String()
}

// threadBrief builds a side thread's system prompt — the counterweight to the
// history it wakes up inside.
//
// A side thread is a FORK of zeus' session (thread.go, BranchThread), and a
// fork inherits the conversation but NOT the system prompt: the thread opens
// holding every turn of zeus' implementation run with none of zeus' brief
// telling it what that run was for. Left alone, the strongest prior in its
// context is "I am the agent building this job, mid-slice" — so this brief's
// entire job is to overwrite that prior explicitly, in kraken's own voice,
// before the first message lands. Every load-bearing sentence below is pinned
// by TestThreadBriefStatesTheContract; a fork that reads as zeus is the exact
// failure this text exists to prevent.
//
// It is re-passed on revive as well as on branch: a resumed session does not
// retain an --append-system-prompt from an earlier run, and a revive without it
// comes back as a plain claude holding an implementer's history — the worst
// version of the same failure, since by then carl thinks the thread is safe.
//
// The boundary it draws is deliberately narrow: not "read-only agent", but "not
// this job's agent". A thread may write scratch files and run whatever it needs
// to answer well (its pane runs with the same bypassed permissions as zeus';
// pretending otherwise would just make it ask for things it can already do) —
// what it may never touch is the job's work and the job's repo state, which it
// shares with a zeus that is still live in the very next window.
func threadBrief(j *Job, tid, title, worktree string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are side thread %s (%q) on kraken job %q. You are NOT zeus.\n", tid, title, j.Slug)
	b.WriteString("Your conversation history was FORKED from zeus, the agent doing this job's actual work — so everything above this point is zeus' work, not yours, and none of it is yours to continue. Whatever task that history was in the middle of, you are not doing it. You never open PRs, never write artifacts, and never run `kraken` write verbs (questions set, feedback addressed, or any other).\n")
	fmt.Fprintf(&b, "zeus is STILL LIVE in another window of this same tmux session, working in the very worktree you are sitting in: %s. Never edit, stage, commit, push, revert, stash, or reset the job's tracked files, never run a git command that changes repo state, and never touch .git. zeus would be working against your changes without knowing they exist.\n", worktree)
	b.WriteString("You exist to EXPLAIN and INVESTIGATE for carl: read the code, trace what happened, run read-only queries, search the web, and answer clearly. You may write scratch files (outside the repo's tracked tree) and run whatever commands you need to answer well — your permissions are unrestricted, and the line is the job's work and the job's repo state, not the tooling.\n")
	b.WriteString("Spawn no subagents at all — no Agent calls, no teammates. You are one agent answering questions in one pane.\n")
	b.WriteString("Answer in your own pane: whatever you write here is what carl reads. You have no way to reach carl anywhere else — no message to zeus, no file carl watches, no notification.\n")
	return b.String()
}
