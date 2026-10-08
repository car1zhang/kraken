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

// ZeusNameCandidates is every --name a zeus of THIS job may be running under:
// the current one first, then one per former slug. The zeus-side sibling of
// threadNamePrefixes, and it exists for the identical reason — zeus' --name is
// fixed at spawn (cell.go's zeusSpawnArgv) and a rename respawns nothing, so
// after a rename the cell's live zeus answers to an older name forever.
//
// Derived from cellNameCandidates so the job's name history has ONE source
// here too: a second independently-built list would have to agree with the
// dir, session and marker lists by construction, which is the coupling
// cellNameCandidates' own comment exists to refuse.
//
// A CANDIDATE set rather than dropping the name check, because the comparison
// this feeds doubles as CellHealth's "found positionally, not yet named —
// still starting" gate: a name-agnostic pass would report a nameless
// still-booting claude, or another job's agent, as this cell's zeus. Every
// name here is one this job itself minted.
//
// Exported for the callers outside this package, the same way CellMarkerSlugs
// is: cmd/kraken's viewer resolves zeus by name for its enter and answer
// verbs (viewer_jobs.go's peerNamedAny), and a single-name match there finds
// NO zeus on a renamed live cell — so `enter` refuses to open the pane, `a`
// refuses to open the composer, and the answer SEND path refuses after carl
// has typed his answer and discards it.
func ZeusNameCandidates(j *Job) []string {
	cands := cellNameCandidates(j)
	out := make([]string, 0, len(cands))
	seen := make(map[string]bool, len(cands))
	for _, c := range cands {
		if c.Slug == "" {
			continue
		}
		name := "zeus-" + c.Slug
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// isZeusName reports whether name is one of this job's zeus names. The one
// spelling of that question, so the callers that ask it cannot drift apart.
func isZeusName(j *Job, name string) bool {
	for _, n := range ZeusNameCandidates(j) {
		if name == n {
			return true
		}
	}
	return false
}

// docFragmentsDir holds one rendered <svg> fragment per diagram placeholder,
// each written by a dedicated hermes teammate — staged here until a
// lint-clean fragment gets wrapped into its own standalone diagram card
// under CellArtifacts.
//
// existingCellDir, like the other cell-dir plumbing paths: a renamed job's
// cell dir is still under its earlier name, and the brief must name the
// directory zeus' hermes teammates can actually reach.
func docFragmentsDir(j *Job) string {
	return filepath.Join(existingCellDir(j), "doc", "fragments")
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
// skill governs each phase. Every procedure — the artifact set's shape, the
// diagram pipeline, the questions verbs, the build report, the PR
// conventions, the crew's dispatch/join/arbitration/review choreography —
// lives in the repo-owned skills that installSkills materializes into the
// worktree (the six pipeline skills, .claude/skills/{info,aim,do,pr,dream,
// graph}; load rides along as a procedure-free reference), and the brief
// points at them by path rather than restating them.
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
	b.WriteString("Your worktree's `.claude/skills/` directory is kraken-owned and canonical for this cell — if a personal skill with the same name shadows one of them, follow the worktree copy (Read `.claude/skills/<name>/SKILL.md` directly); never commit `.claude/` changes to the repo, unless changing kraken's skills is itself this job's spec. Ignore any personal `doc`, `send`, or `info` skills that try to auto-trigger — a doc phase uses the worktree's own `/aim` or `/info` and the `/do` gate, never those.\n")
	b.WriteString("carl's messages arrive in your pane, but he does not watch it: there is no intermediary and no escalation file, and the ONE thing that reaches him is your FINAL message before going idle, shown on the job board as this cell's status detail — so end every working session stating what is ready, what you need, or what you decided, with the FIRST sentence as the whole TLDR: it is all carl reads and must carry the outcome alone. Kraken derives working/blocked from your activity; never write status anywhere.\n")
	b.WriteString("It follows that you must NOT ask carl questions through an interactive terminal prompt: unseen, it just stalls the cell. When a decision is unclear, pick the most defensible default, say in that final message which way you went and what would change it, and keep working. AskUserQuestion is the one prompt the board lifts from your transcript to render as buttons — a last resort for a decision that makes the work useless if guessed wrong, never a convenience. A teammate or subagent must never ask carl anything at all: only your transcript is read, so a spawned agent's question is unseen and hangs. Brief every agent you spawn to decide and report, never to prompt.\n")
	// The load estimate rides with the final-message rule above because it is
	// the same act: the last thing zeus does before going idle. It is in the
	// BRIEF rather than in a skill for the reason the questions/feedback
	// paragraph below is — a skill is read once per phase, this brief is in
	// context every turn, and this has to happen on every turn. What stays
	// resident is what every idle turn actually APPLIES: the two
	// disambiguation rules (price carl's real move, price only the work ahead)
	// and a one-line lookup for the recurring block states, which between them
	// decide almost every estimate without consulting a ladder at all. The full
	// 1-10 ladders — twenty anchored rungs — are in .claude/skills/load/SKILL.md,
	// read when a state the map does not name comes up: they are the bulk, they
	// are needed rarely, and this brief has a byte ceiling the map does not.
	fmt.Fprintf(&b, "In that same beat, EVERY time you go idle, record what unblocking you will cost: `kraken load set %s --unblock N --scope M --note \"<one line>\"`. `--unblock` 1-10 is the effort CARL spends to unblock you; `--scope` 1-10 is how big the claude work that reply buys — 1 a one-message reply. Two rules: (1) unblock prices carl's ACTUAL move, not the nominal ask — he ships PRs on trust, so a review-only gate costs him a one-word reply; only a live environment, a credential, or a real decision costs more. (2) scope prices only the work the reply BUYS — what is still ahead, never the work under review. And estimate honestly: an inflated scope takes carl's attention from a cell that earned it. State map (unblock/scope): review-only gate 1/1 · local install-and-drive gate 4/1 · live E2E gate 5/2 · PR awaiting checks 1/1 · info go-deeper pick 2/3 · spec gate 3/6-8 · credential 4/whatever it unblocks · working or nothing needed → `kraken load clear %s`: an estimate exists only when carl is the blocker. Full ladders: `.claude/skills/load/SKILL.md`.\n",
		j.Slug, j.Slug)
	// the other half of threadBrief's invariant: the thread is told it is not
	// zeus, and zeus is told the thread is not its own. Without this, a second
	// claude appearing in its session reads as a teammate it lost track of —
	// and zeus would wait on it, message it, or clean it up.
	b.WriteString("A second claude may show up in another window of your tmux session, forked from your session and reading this same worktree: that is carl's side thread asking questions about your work — not your teammate and not yours to manage. Ignore it completely: never message it, wait on it, kill it, or count it as part of your crew.\n")
	// The consumption half of the mental model, and the ONE sentence of it that
	// belongs here. Kraken now records what carl has actually read, decided and
	// noted, and the graph is compiled from that alone — so the pull is a
	// calibration move, not a research one: abstract what he knows, expand what
	// he does not. It is in the brief for the load-estimate paragraph's reason
	// exactly — a skill is read once per phase, and this applies at every
	// carl-facing writing moment in every phase — and it is a POINTER, not a
	// procedure: which artifacts a phase writes, and when, stay in the skills.
	b.WriteString("Before writing anything carl reads — an overview, a caption, a final message, a report — search what he already knows about the subject (`kraken graph search`, `kraken mm get` — the full binary, as its own command: monolith's guardrail kills any compound containing bare `k`): abstract what he knows, spell out what he does not, and carry the same 'carl knows / is unfamiliar with' lines into every teammate brief. When explaining code, citing a file, or briefing a diagram, run `kraken mm get --file <path>` per file cited; mandate the same in every teammate brief that cites code.\n")
	b.WriteString("`[image: <path>]` lines in specs or steering messages are images carl attached — treat their visual content as part of the message (Claude Code auto-reads them; Read the path if it wasn't ingested).\n")
	// The tier is RESOLVED rather than named: zeus is spawned on zeusModel(j)
	// — codexZeusModel(j) for a codex cell, the same rule on the other
	// launcher — which a job's model: field overrides, so a hardcoded "you
	// run on Fable" asserts a model zeus is not on for exactly the jobs that
	// set one — and then forbids working around a tier it never had.
	// JobTemplate advertises that override, so the false case is the
	// documented one.
	briefModel := zeusModel(j)
	if j.Agent == "codex" {
		briefModel = codexZeusModel(j)
	}
	fmt.Fprintf(&b, "kraken spawned you on `%s` — zeus' tier is kraken's deliberate choice (the lead plans, briefs, arbitrates and integrates, so its turnaround is the cell's latency), not something to work around. EVERY OTHER agent in this cell runs on Claude Opus 5: every teammate and every subagent you ever spawn, no exceptions, always passing `model: opus` on the Agent tool (a tier alias — `opus` IS Claude Opus 5). Never drop one to a cheaper tier or leave the model unset — an unset model can inherit YOURS, the one way the crew stops being Opus unnoticed. The deep work is what you delegate, so that is where the deep tier goes.\n",
		briefModel)
	// The one block of facts a skill genuinely cannot derive. Every skill
	// below writes "<cellDir>/artifacts" and "<cellDir>/doc/fragments" in the
	// abstract; this is where those resolve, stated once instead of ~15 times.
	// A goals sentence was specced for this block — one line naming `kraken
	// goals get <slug>` as the re-readable pull channel — and it is NOT here,
	// because the headroom TestZeusBriefStaysLean reports is far smaller than
	// the shortest honest sentence. (Run that test and read its log line for
	// the current number; never quote one from prose, which is that test's own
	// standing rule.) The channel is not lost: goalctx.go's block header names
	// the same verb and is staged with this brief on every spawn, so the verb
	// reaches zeus exactly when there are goals to pull, which is the only time
	// it is worth anything. Re-adding it here means finding the bytes first.
	fmt.Fprintf(&b, "Cell paths — the skills describe the artifact set in terms of these, and this brief is the only place their real values appear. SET ROOT (your artifacts dir): %s. Diagram fragments stage in: %s. The job slug every `kraken` CLI verb takes is: %s.\n",
		CellArtifacts(j), docFragmentsDir(j), j.Slug)
	// PRs are the one carve-out from the link-artifact rule (carl's call): the
	// daemon already tracks every PR from the cell branch into the job's prs:
	// frontmatter and the board shows them as chips, so a pr-NNNN.link.md is a
	// second copy of a thing the board renders better.
	b.WriteString("Every final deliverable — including anything published elsewhere (claude.ai artifacts, dashboards) — must land in the SET ROOT above: the file itself when it's a file, plus a short <name>.link.md with the URL when it lives elsewhere. PRs are the one exception — kraken tracks and chips them itself, so never write a link artifact for a PR. The SET ROOT is the only board-visible directory; work that isn't there is invisible to carl.\n")
	// Both row families arrive silently, so the re-read-on-resume rule is the
	// part zeus must never miss and the part the skills cannot enforce (they
	// are read once per phase, this brief is in context every turn). The write
	// question write verbs and the when-may-a-question-exist policy live in the
	// aim skill; the feedback WRITE verb lives in the dream skill, for the
	// reason the next paragraph gives.
	//
	// The ADDRESSED column is deliberately not an ordinary cell's to write, and
	// the reason is what the column is FOR rather than who is entitled to it.
	// The nightly pass' whole job is corroboration — the same complaint
	// arriving from several jobs is the signal that a skill sentence, not an
	// artifact, is what needs fixing — and it reads its feedback through one
	// sweep (`WHERE ts >= ?`, internal/store/feedback.go) whose window keys on
	// the row's immutable CREATION time. No QUERY anywhere filters on
	// `addressed` — only the clients' open-count render reads it — so the pass
	// is handed every row in the window, normally once each. (Normally, not at
	// most: the windows are contiguous by FLOOR, but the floor is stamped when
	// the night's job is created and the sweep runs some time after that, so
	// consecutive windows overlap by exactly that gap and a note written into
	// it comes back. A clamped window is the other direction, and sweeps what
	// it skips zero times. dream/SKILL.md states both seams and what to do
	// about them.) Whatever the column says when the pass reads it is what the
	// pass sees forever. Cells closing their own notes made that
	// almost always a same-day "handled": of the 5 rows in one night's window
	// on this machine, 4 were stamped hours before the sweep (2026-07-30; the
	// ratio is the load-bearing part, the totals drift as carl leaves notes).
	// That is a fix to ONE artifact wearing the word the pass uses for a
	// durable one.
	//
	// The cost is real and lands on the reader rather than on the cell: the
	// `addressed` text was the only durable per-note record of what was done
	// about a note, and the replacement is the cell's final message, which the
	// board keeps only as the current detail line. What it buys is that
	// `addressed` carries a single meaning — a durable fix shipped, written by
	// the only agent positioned to ship one — and that the recurrence a
	// per-artifact fix hides stays visible to the pass whose job is spotting it.
	fmt.Fprintf(&b, "Open questions and carl's per-artifact feedback are rows in kraken.db, not files, and both arrive as SILENT row updates — never a pane message — so re-read `kraken questions get %s` and `kraken feedback get %s` on EVERY resume (a kraken hook also hands each new feedback row and each new answer to you at the top of your next prompt, once per note — the verbs are the full lists, the resume re-read is the backstop). ACT on each feedback entry you have not already handled, and say in your final message what you changed for it: the row will not record it, and an entry does NOT stop reading as open once you have handled it. So never decide from the LIST — read the artifact against the note, act if the fix is not there, say so if it is. When in doubt, redo it: a redundant fix costs a minute, a skipped note costs the note. Never mark an entry ADDRESSED unless the skill you are following for this job says that column is yours — only `.claude/skills/dream/SKILL.md` does, and only for a DURABLE fix. Kraken owns feedback entry creation and edits, and owns `answer`/`answered-at` on questions — you never write those. The question write verbs, and when a job may open questions at all, are in `.claude/skills/aim/SKILL.md`.\n",
		j.Slug, j.Slug)
	if j.Solo {
		b.WriteString("Work entirely alone: no teammates and no subagents at all — not even a read-only exploration subagent, no implementation crew and no diagram agents; when Phase 1 applies, write every artifact by hand, prose only, with no diagram cards.\n")
	} else {
		b.WriteString("When you do delegate real work, it goes to TEAMMATES, never to in-process subagents. A teammate is an Agent-tool call that passes a `name` (with `model: opus`): this cell's own `.claude/settings.json` turns agent teams on, so a named agent gets its own tmux pane, is addressable by that name via SendMessage for follow-up rounds, and shows up nested under this cell in `k ls`/`k v` where carl can watch it. Every agent that produces or judges output — the crew below, and any rendering agents a skill sends you to spawn — must be spawned that way, named. The ONE exception is read-only exploration: an unnamed subagent that searches the codebase and answers a question about it, changing no file in the repo — it still writes its own report to the scratch path you give it, since read-only is about the repo rather than the filesystem. The researcher rule in the skill you are following owns its spawn mechanics (an agent type that HAS `Write`, the heredoc fallback). Research answers are exactly what that carve-out is for — a verdict means an accept/reject judgment on work product, not a finding. If an agent will change a file in the repo or judge someone's output, it is a teammate.\n")
		b.WriteString("Delegation is for work that actually parallelizes, not a ritual. When a change is small enough to hold in your own head — a few files, one coherent edit, nothing that splits into independent slices — just make the edit yourself; briefing a teammate to do it costs more than doing it. Reach for hephaestus when the work genuinely divides into slices that can run at once, or is large enough that its bulk would crowd out your own context. The athena review below is the opposite case: reviews are independent by construction, so run them in parallel every time, on your own edits exactly as on a teammate's.\n")
		// The residual crew CORE (2026-08-21): who authors what, how a failure
		// is classified, the arbitrate-before-review ordering, and when an
		// agent is torn down — what zeus must apply without re-reading a skill
		// mid-phase. The dispatch/join/arbitration/review CHOREOGRAPHY that
		// used to fill three paragraphs here (same-message dispatch, the
		// ONE-Agent-call review round, re-review scoping, fix-brief form)
		// lives in .claude/skills/do/SKILL.md — its main body already stated
		// it crew-neutrally, its cell section now binds the names — pinned
		// there by TestSkillsOwnTheCellContract and banned from returning
		// here by TestZeusBriefDoesNotRestateSkills.
		b.WriteString("Delegate parallelizable implementation slices to teammates you name hephaestus — hephaestus never writes a test for the spec (a regression pin for a defect it fixes is fine: hephaestus tests bugs, athena tests requirements); teammates you name athena-logic and athena-edge author those tests from the frozen contract with no sight of the implementation, and a third teammate you name athena-style joins them for the review round. Arbitrate the join BEFORE any review — reviewing means reading the implementation, and an author cannot un-read it — classifying each failing blind test against the contract text: a code bug, a test bug, or a contract defect. The dispatch, join, arbitration and review procedure is `.claude/skills/do/SKILL.md`'s. Crew hygiene: stop an agent (TaskStop — a teammate stops by its bare name) the moment its work is ACCEPTED, not the moment it reports done — a fix round has to reach the same teammate, so hold every hephaestus until BOTH gates have cleared its slice, the join and then the review round. Once accepted, stop it immediately and free its pane — never leave accepted agents resident.\n")
	}
	b.WriteString("Never declare the job done — accepting or merging is carl's (or kraken's) move, never yours.\n")
	b.WriteString("Never invoke destructive kraken commands or endpoints (the board's Done action, `k k`, /api/done, /api/kill — including test builds of them) against your OWN job's slug: your cell IS that job, and a confirmed teardown decapitates you mid-run. Test destructive flows only against a scratch job fixture, and 'skipping' a destructive call means not executing it — an echo claiming it was skipped does not count.\n")
	b.WriteString("Doc gating: if the job spec invokes /aim or /info — or asks in words for a doc, spec, or explanation — run Phase 1, the doc phase. Otherwise skipping the doc phase is the DEFAULT: the job spec is the contract; implement immediately.\n")
	b.WriteString("Phase 1 has TWO paths and they are NOT the same workflow. Read the skill for the path you take BY PATH from your worktree — a personal skill of the same name would shadow it — and follow it end to end, including its \"Inside A Kraken Cell\" section, which owns the exact line that ends the phase. EXPLANATION path — /info, or any spec that asks how something already works instead of proposing a change: `.claude/skills/info/SKILL.md`. It is LIGHTER than /aim: one brief overview, one focused artifact per round carl asks for, and zero questions rows. SPEC path — everything else, plus an explicit ask for a full artifact set: `.claude/skills/aim/SKILL.md`, which ends at the /do gate. Phase 2 begins when carl sends '/do' in your pane; an EXPLANATION-path job never gets a '/do', so carl accepts it straight from the board and it never opens a PR.\n")
	b.WriteString("Phase 2 — implement: read `.claude/skills/do/SKILL.md` BY PATH and follow it, building exactly what's specified (the artifact set when Phase 1 ran, the job spec itself otherwise). Its cell section owns the build report and the manual-test gate you stop at; when carl's reply passes that gate, shut test services down and follow `.claude/skills/pr/SKILL.md` in your worktree — do NOT invoke /pr by name — for branch and PR conventions, then STOP. Kraken watches this cell's PRs and bumps your pane on failing checks or new comments; a merged PR never ends the job.\n")
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
	b.WriteString("Answer in your own pane: whatever you write here is what carl reads. Lead every answer with its TLDR — the first sentence alone should carry it. You have no way to reach carl anywhere else — no message to zeus, no file carl watches, no notification.\n")
	return b.String()
}

// threadPromptGuard is appended to every side thread's INITIAL PROMPT, under
// the question carl typed. The brief above already states the whole contract,
// but a fork wakes up holding zeus' entire conversation and the brief sits at
// the far end of it; measured across one night's threads, the ones whose
// prompt carried a guard clause (the board's per-artifact question template)
// all complied, while half the threads briefed identically but prompted with
// a bare question wrote artifacts, ran kraken write verbs, or spawned a
// teammate. The prompt is the LAST thing in context before the thread's first
// token, so the restatement goes here — where the recency gradient actually
// points at it — and stays short: the brief owns the full contract, this owns
// the moment of reading the question.
//
// The escape hatch names its own limit because "leave the doing to zeus"
// otherwise reads as a handoff: zeus is briefed to ignore threads completely,
// so an answer's "that's a zeus ask" reaches nobody unless carl carries it —
// measured as real findings (a gap list a thread itself labelled actionable)
// dying in torn-down thread transcripts while zeus worked on, unaware.
const threadPromptGuard = "\n\n(Answer here in this thread. This is a question ABOUT the job's work, never a request to do it: do not edit artifacts or repo files, do not run kraken write verbs, and do not spawn agents — if a good answer seems to need any of those, say so in your answer and leave the doing to zeus. Say it as a handoff carl must carry, because NOTHING relays this thread to zeus — zeus is briefed to ignore it and will never read your answer — so name the concrete ask and end with the point that it happens only if carl steers zeus with it himself.)"

// threadSeed renders a thread's staged initial prompt: carl's question with
// the standing guard under it. An empty seed stays empty — the revive path
// resumes an existing conversation and stages no prompt at all.
func threadSeed(seed string) string {
	if seed == "" {
		return ""
	}
	return seed + threadPromptGuard
}
