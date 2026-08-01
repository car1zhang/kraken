package jobs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kraken/internal/core"
)

// crew_test.go — zeus' brief content matrix. The brief carries only what no
// skill can know: identity, the cell's real paths, how carl is reached, the
// model tier, the delegation crew, the self-destruction guardrails, and which
// skill governs each phase. Every procedure it used to inline — the artifact
// set's shape, the whole diagram pipeline, the questions write verbs, the
// build report, the PR conventions — now lives in .claude/skills/{info,aim,
// do,pr}, pinned by skills_embed_test.go. So this file asserts two things:
// the brief still says everything only it can say, and it no longer restates
// anything a skill owns (TestZeusBriefDoesNotRestateSkills).
//
// There is no job-level type (Q14) and no skip-doc field: whether Phase 1 runs
// at all, and which of its two paths it takes — the /aim SPEC path or the
// lighter /info EXPLANATION path — is zeus' own call from the job spec text,
// and whether phase 2 happens is carl's call via /do. Every gating paragraph
// therefore rides in every brief variant, gated only by Solo.

func TestZeusBrief(t *testing.T) {
	j := &Job{Slug: "x"}
	brief := zeusBrief(j)
	for _, want := range []string{
		"zeus", "hephaestus", "athena", "AskUserQuestion", "TaskStop",
		// the cell's real paths — the one block of facts a skill cannot
		// derive, and the reason the skills can speak in <cellDir> terms
		"Cell paths", CellArtifacts(j), docFragmentsDir(j),
		"only board-visible directory",
		// both row families arrive as silent updates, so the re-read rule
		// lives in the brief (in context every turn) rather than in a skill
		// (read once per phase)
		"SILENT row updates", "EVERY resume",
		"kraken questions get x", "kraken feedback get x",
		// capital K: the clause now opens a sentence, where before it followed
		// a semicolon
		"Kraken owns feedback entry creation and edits",
		// zeus ACTS on feedback but never closes it: `addressed` is the
		// nightly pass' column alone, and it means a durable fix shipped
		// rather than one artifact patched. Four separate tokens because four
		// separate deletions each break it differently — dropping "ACT on"
		// leaves a cell that ignores its feedback; dropping the prohibition
		// leaves every note closed before the sweep can read it; dropping the
		// "unless the skill" carve-out binds the DREAM cell's own zeus, which
		// would leave the column with no writer anywhere; and dropping the
		// re-read warning leaves a cell re-acting on every note it has already
		// handled on every resume, since the row no longer records that.
		"ACT on each feedback entry you have not already handled",
		"Never mark an entry ADDRESSED unless the skill you are following for this job",
		"does NOT stop reading as open once you have handled it",
		".claude/skills/dream/SKILL.md",
		// three named single-dimension athena review variants, dispatched in
		// parallel, with a targeted (not full) re-review on a blocking finding.
		// Two of them are already live from the authoring dispatch, so the
		// round is one message but only ONE Agent call — spawning three would
		// duplicate a bare name and hand the "fix your own assertion" brief to
		// an agent that never wrote a test.
		"athena-logic", "athena-edge", "athena-style",
		"dispatched in parallel in one message", "ONE new Agent call, not three",
		"re-review with only the flagging variant(s)",
		// the crew are TEAMMATES: named Agent-tool spawns with real panes, not
		// anonymous in-process subagents
		"delegate real work, it goes to TEAMMATES",
		"implementation slices to teammates you name hephaestus",
		// the doc-gating rule: zeus' own call from the spec text, never a job
		// field, with skip as the stated default
		"Doc gating", "/aim or /info", "doc, spec, or explanation", "DEFAULT",
		"the job spec is the contract; implement immediately",
		// Phase 1's two paths, each delegating to its skill BY PATH (a
		// personal skill of the same name would shadow it)
		"Phase 1 has TWO paths", "EXPLANATION path", "SPEC path", "BY PATH",
		".claude/skills/info/SKILL.md", ".claude/skills/aim/SKILL.md",
		"LIGHTER than /aim", "zero questions rows",
		// the gates themselves are cell policy: /do opens phase 2, and an
		// explanation job never gets one
		"'/do'", "never gets a '/do'", "never opens a PR",
		// phase 2 delegates to the do skill, then the pr skill — the latter
		// read by path, since /pr is a same-named-shadow risk
		"Phase 2", ".claude/skills/do/SKILL.md", ".claude/skills/pr/SKILL.md",
		"do NOT invoke /pr by name",
		// no self-polling, and a merge is not completion
		"bumps your pane", "never ends the job",
	} {
		if !strings.Contains(brief, want) {
			t.Errorf("brief missing %q", want)
		}
	}
	for _, unwanted := range []string{
		"done.md", "hermes pane", "relay", "Success criteria",
		"/send", "/doc skill", "you inherit carl's user skills",
		"invoke /info by name", "type:", "(type:",
		// self-polling is gone (the daemon watches PRs now)
		"flatten", "poll",
		// questions.yaml is retired outright: questions are rows now
		"questions.yaml", "answered-at:", "- id: Q1",
		// there is no more job-level skip-doc field or flag
		"skip-doc", "skip_doc", "SkipDoc",
	} {
		if strings.Contains(brief, unwanted) {
			t.Errorf("brief still mentions %q", unwanted)
		}
	}
}

// TestZeusBriefDoesNotRestateSkills is the anti-duplication guard, and it is
// the test that would have caught the outage this split fixed.
//
// The brief used to inline the entire /aim pipeline — the diagram-card wrap
// one-liner, the lint loop, the hermes-contract rule, the artifact numbering —
// verbatim from .claude/skills/aim/SKILL.md, which installSkills materializes
// into the very worktree zeus is told to read skills from. It reached 17.5KB,
// which is past tmux's ~16KB command ceiling, and every cell spawn died with
// `command too long`. Staging the brief in a file (spawnCommand) removed that
// ceiling, so size alone can no longer fail loudly — which makes this the only
// thing standing between a small clarifying sentence and a re-duplicated
// pipeline that drifts from the skill zeus actually obeys.
//
// Each token below names a procedure a skill owns. A brief that needs to
// mention one of them is a brief making a decision that belongs in the skill.
func TestZeusBriefDoesNotRestateSkills(t *testing.T) {
	for _, j := range []*Job{{Slug: "dedupe-plain"}, {Slug: "dedupe-solo", Solo: true}} {
		brief := zeusBrief(j)
		for _, owned := range []struct{ token, owner string }{
			// the diagram pipeline, in full — aim/SKILL.md Core Rules
			{"hermes", "aim"},
			{"hermes-contract.md", "aim"},
			{"lint_svg.py", "aim"},
			{"--marker-id", "aim"},
			{"diagram-card.html", "aim"},
			{"{{CARD_TITLE}}", "aim"},
			{"{{CAPTION}}", "aim"},
			{"<!-- DIAGRAM:", "aim"},
			{"str.replace", "aim"},
			{"re.sub", "aim"},
			{"silhouette", "aim"},
			{"sanctioned kind list", "aim"},
			{"box → kind", "aim"},
			{"tunnel card", "aim"},
			{"worked example", "aim"},
			// the artifact set's shape and filenames — aim/SKILL.md
			{"00-overview.md", "aim"},
			{"50-components.md", "aim"},
			{"51-code-map.md", "aim"},
			{"52-testing-plan.md", "aim"},
			{"10-49", "aim"},
			{"spec mode", "aim"},
			{"explain mode", "aim"},
			// the questions WRITE verbs — aim/SKILL.md Core Rules. The read
			// verb and the silent-update rule deliberately stay in the brief.
			{"kraken questions set", "aim"},
			{"kraken questions add", "aim"},
			{"Q<digits>", "aim"},
			{"P0/P1/P2", "aim"},
			// each phase's exact stop line — the skills' cell sections
			{"doc ready for review", "aim"},
			{"explanation ready", "info"},
			{"'go deeper' menu", "info"},
			{"manual test ready", "do"},
			{"90-build-report.md", "do"},
			// the blind-authorship artifact and the test-file naming that goes
			// with it. The brief says "the frozen contract" and stops there;
			// naming the file, its columns, and the per-dimension filenames is
			// procedure, and it lives in do/SKILL.md for the same reason
			// 90-build-report.md's shape does.
			{"50-contract.md", "do"},
			{"symbol · signature · obligations · owner", "do"},
			{"_contract_test.go", "do"},
			{"_edge_test.go", "do"},
			// PR mechanics — pr/SKILL.md
			{"gh pr create", "pr"},
			{"checkout -B", "pr"},
			{"git fetch origin", "pr"},
			// the feedback WRITE verb — dream/SKILL.md. Same split as the
			// questions verbs above: the brief keeps the read verb and the
			// silent-update rule and states the PROHIBITION in prose, while
			// the one skill allowed to run it owns its spelling. A brief that
			// names the verb is a brief teaching every cell the call it is
			// being told not to make. The binary name is left off for the
			// reason TestOnlyDreamNamesTheFeedbackWriteVerb gives: `k` is a
			// first-class spelling and is the one kraken's own usage strings
			// use, so matching only `kraken ...` leaves the likelier reversal
			// through. (threadBrief does contain this substring, correctly and
			// as a prohibition — this test reads zeusBrief only.)
			{"feedback addressed", "dream"},
		} {
			if strings.Contains(brief, owned.token) {
				t.Errorf("%s brief restates %q, which .claude/skills/%s/SKILL.md owns — "+
					"the brief must point at the skill, not copy it (a copy drifts, and whichever "+
					"one zeus reads first is the one it obeys)", j.Slug, owned.token, owned.owner)
			}
		}
	}
}

// TestZeusBriefStaysLean caps the brief well under tmux's ~16KB command limit.
// spawnCommand means that limit no longer applies to the brief at all, so this
// is not a mechanism guard — it is a second, cruder net under
// TestZeusBriefDoesNotRestateSkills, catching bulk that arrives as prose the
// token list does not happen to name.
//
// The ceiling has moved more than once: 10,000 -> 12,000 to admit the
// load-estimate rule (the `kraken load set` paragraph in zeusBrief, which
// measured 1,017 bytes AT THAT TIME, when it spelled its 1-10 scale out inline
// — see the 2026-07-31 restatement below for what it measures now), 12,000 ->
// 13,000 to admit blind test authorship, and 13,000 -> 13,500 for the
// feedback-ownership contract. That
// number is the ONLY thing this test's failure message tells you not to
// touch, so EACH move is reasoned out below rather than left to the diff.
//
// Deliberately no literal ceiling value appears in the prose from here on as
// a statement of the CURRENT limit: `const ceiling` is the one place the live
// number lives. Section headings and the history list below do name specific
// values, and they should — they describe a MOVE, which a later raise cannot
// falsify, rather than a state that goes stale the moment one happens. Three separate figures
// in this block went stale across the reviews of one change, which is what
// a restated constant does — so the sections below say "the ceiling" and
// leave the arithmetic to the measured byte counts, which are dated
// observations rather than duplicates of a value the code already holds.
//
// WHY IT BELONGS IN THE BRIEF. zeusBrief's own doc comment sets the split:
// the brief carries what no skill can know. Every-turn enforcement is a
// sub-case of that — what a skill cannot DO — since a SKILL.md is read once
// per phase and this fires at the end of every turn. That is not a criterion
// invented here: the questions/feedback re-read paragraph in crew.go is
// resident for the same stated reason. Moving it into .claude/skills/ would
// also have meant three copies, since /aim, /info and /do each own a different
// phase's cell section and a job can block inside any of them — and three
// copies of one 1-10 ruler is exactly the drift
// TestZeusBriefDoesNotRestateSkills exists to prevent.
//
// WHY IT MEASURES A LONG SLUG. The brief carries the slug NINE times: six
// direct interpolations plus one each inside CellBranch, CellArtifacts and
// docFragmentsDir. (It was ten until the feedback paragraph stopped naming
// the `kraken feedback addressed <slug>` call — that verb is the nightly
// pass' alone now, so the brief states the prohibition and never the call.)
// Every extra slug character therefore costs nine bytes, and
// for its entire life until this change the input it measured was the 4-character
// "lean" — which is the SHORTEST brief the function can emit, not a
// representative one. Measured this session against ~/Documents/pluto: 25
// live and archived job slugs exceed 23 characters and the longest is
// `monitor-adaptive-capacity-cache-shadowing` at 41, which renders 370 bytes
// more than "lean" does. A ceiling checked at the floor can be green while
// every real cell is over it — which is the same defect the 10,000 -> 12,000
// move rejected a tighter number FOR, so this test was failing its own
// argument for as long as it measured "lean". The input is now a 48-character
// slug — past the longest observed, and the arithmetic above makes any shorter
// slug strictly cheaper, so one measurement bounds them all.
//
// WHY 13,000 RATHER THAN 12,000. Blind test authorship (hephaestus implements
// and writes no spec tests; athena-logic and athena-edge author them from the
// frozen contract; a failing blind test is arbitrated into one of three
// verdicts) is a crew CONTRACT, and the brief is where the crew contract
// lives — the procedure that carries it out is in do/SKILL.md, and
// TestZeusBriefDoesNotRestateSkills pins that split by forbidding this brief
// the artifact filename and the per-dimension test filenames. What is left
// here is irreducible: who authors what, when they run, and how a failure is
// classified, and in what ORDER — the arbitration has to finish before the
// review round, because reviewing means reading the implementation and an
// author cannot un-read it, so that sequencing is a contract clause and not a
// procedure detail. It cost ~1,160 bytes (HEAD's brief renders 11,290 at this
// slug length, this one 12,450), which the 12,000 ceiling could not absorb
// once measured honestly at 48 characters rather than at 4. Raising it while
// simultaneously fixing the input is the only combination that leaves a
// number that actually bounds something: 13,000 against a measured 12,450 at
// 48 characters left 550 bytes at the time — a little under the 710 this ceiling actually
// had at this slug length before the change, and well under the 980 it was
// believed to have. The next crew change is a trim, not another raise.
//
// WHY 13,500 RATHER THAN 13,000, AND WHY THIS RAISE DISOBEYS THE LINE ABOVE.
// The paragraph above ends "the next crew change is a trim, not another
// raise", and this is a raise. That deserves the argument it asked for.
//
// The feedback-ownership change (an ordinary cell reads its feedback, acts on
// it, and never marks a row `addressed` — the column is the nightly pass' and
// means a durable fix shipped) grew one paragraph and added no section. It
// also removed a slug interpolation on the way, which
// is why the count above is NINE and not ten. Measured on one basis — the
// paragraph's changed tail, rendered at a 48-character slug — it went from
// **413** bytes to **828**. Every clause it gained was put there by a DEFECT
// rather than by an author with something to say (not all by a review
// finding: the carve-out below was caught while fixing a different one):
// the "unless the skill you are following" carve-out, without which the
// unified brief binds the DREAM cell's own zeus and the column ends up with no
// writer anywhere; and the read-the-artifact tie-break, without which a cell
// that cannot remember whether it handled a note has a one-directional
// incentive to skip it, silently, which loses carl's input outright. Those are
// contract clauses, and the brief is where the contract lives.
//
// The measurement: **12,865** bytes at a 48-character slug, 12,802 at the
// longest real one (41), 12,469 at "lean". Against the PREVIOUS ceiling that
// left 135 bytes — and 135 is not a bound, it is one sentence away from red,
// which is the exact defect the paragraph above rejected a tighter number FOR.
// I trimmed first and it is in that figure (12,956 before the trim), so this
// is a raise on top of a trim rather than instead of one. The ceiling now in
// `const ceiling` leaves 635, a little over the 550 the previous author sized
// and reasoned about.
//
// RESTATED 2026-07-31 for the anchored load scale, under the standing
// instruction below — every figure in the paragraph above is now history. The
// load-estimate paragraph was rewritten to carry the two pricing rules (price
// carl's ACTUAL move; price only the work the reply buys), a one-line
// `unblock/scope` state map for the recurring block states, the anti-inflation
// line, and a pointer to `.claude/skills/load/SKILL.md`; the twenty anchored
// ladder rungs it would otherwise have had to spell out live in that skill
// file, and three canary strings from them are BANNED here by the contract
// tests, so this paragraph cannot grow back into a ladder. Measured on the same
// basis as above — the paragraph rendered at a 48-character slug — it went from
// **1,105** bytes to **1,157**, and the whole brief from 13,417 to **13,469**,
// with 13,406 at the longest real slug (41) and 13,073 at "lean". That leaves
// **31** bytes under the ceiling. Thirty-one is not a bound in any useful
// sense, and it is deliberate: the ceiling was NOT raised for this change, so
// the paragraph was trimmed twice — once to fit the state map, once again to
// fit the legend and the anti-inflation line a review round added — rather than
// the constant being moved a fourth time.
//
// AND THE CONDITION FOR THE NEXT ONE, because a gate that has never once
// answered no is a ratchet with good prose attached. This number has gone
// 10,000 -> 12,000 -> 13,000 -> 13,500 with three raises and zero refusals,
// and "write a paragraph like this one first" is a bar any competent author
// clears. So: at 14,000 the answer is NOT another raise. It is either a
// second brief-level test that bounds what this one only proxies for
// (procedure creeping back out of the skills — paragraph count, or a budget
// per section), or the accepted conclusion that per-turn contract has
// outgrown a single system prompt and needs somewhere else to live.
//
// One practice note, because it cost real confusion here. THREE figures in
// this block had silently drifted before anyone re-measured — TEN
// interpolations when the answer was nine, a 12,450 that was 12,682 by then,
// and a line calling 13,000 the bound after the constant had moved past it —
// all in the safe direction, all invisible. The third was found by a reviewer
// one round AFTER this note was written, which is the strongest evidence the
// note has for itself. Restate the measurement on every change that touches
// the brief; do not trust the last one written down, including this one.
//
// And at the headroom the restatement above records, "the next brief change is
// a trim" stops being advice and becomes arithmetic: 31 bytes is a short
// sentence. Any clause added anywhere in zeusBrief has to be paid for by a
// deletion somewhere in it, and the paragraph the clause belongs to is where
// to look first — that is how both of this change's own trims were found.
//
// What the net still catches is unchanged, and the scale is worth stating
// because it is what makes the ceiling a bound rather than a formality: the
// duplication that caused the original outage was the whole /aim pipeline,
// ~10.6KB of it (zeusBrief measured 17,005 bytes at bf91ca5^ and 6,407 at
// bf91ca5, the commit that staged the brief in a file and deduped it).
// Anyone raising this number again should be able to write a paragraph like
// this one first.
func TestZeusBriefStaysLean(t *testing.T) {
	const ceiling = 13_500
	// Longer than any slug observed on this machine (41), so the nine
	// interpolations make every real brief cheaper than this one.
	slug := strings.Repeat("s", 48)
	brief := zeusBrief(&Job{Slug: slug})
	// Always log the measurement: this line is the ONLY sanctioned way to read
	// the current size
	// (`go test -tags sqlite_fts5 -run TestZeusBriefStaysLean -v ./internal/jobs`).
	// When it printed only inside t.Errorf, five agents in one job independently
	// injected 23 throwaway probe tests into the shared worktree to learn a
	// number this test already computes, and a stale prose copy of it misled a
	// review — so never quote a written-down figure; run this and read the log.
	t.Logf("zeusBrief: %d bytes for a %d-char slug (ceiling %d, headroom %d)",
		len(brief), len(slug), ceiling, ceiling-len(brief))
	if len(brief) > ceiling {
		t.Errorf("brief is %d bytes for a %d-char slug, over the %d-byte ceiling — procedure has "+
			"crept back in from the skills; move it to .claude/skills/ rather than raising this number",
			len(brief), len(slug), ceiling)
	}
}

// TestCrewAssetPathsAreWorktreeRelative pins the three skill-asset paths to
// the shape zeus can actually resolve: its cwd IS the worktree, so a
// home-relative or absolute path would miss. The paths are no longer named in
// the brief (the aim skill spells them out instead), so the presence half of
// this check lives in skills_embed_test.go, which asserts each one both exists
// in the embedded tree and is still referenced by the SKILL.md that uses it.
func TestCrewAssetPathsAreWorktreeRelative(t *testing.T) {
	for _, path := range []string{HermesContractPath(), LintSvgPath(), DiagramCardPath()} {
		if filepath.IsAbs(path) || strings.HasPrefix(path, "~") || !strings.HasPrefix(path, ".claude/skills/") {
			t.Errorf("asset path %q is not worktree-relative under .claude/skills/", path)
		}
	}
}

// TestZeusBriefModelSplit pins the two-tier rule the brief has to state,
// because zeus cannot see its own --model flag and its crew's tier is a
// separate axis it sets per Agent call: zeus itself is spawned on
// core.ZeusModel (Fable), and every agent it names runs on Claude Opus 5. The
// Agent tool takes tier aliases rather than model ids, so the brief must say
// `model: opus` and must never name a cheaper tier for a crew role —
// hephaestus in particular used to be pinned to sonnet, and zeus' own tier
// being the fast one is exactly the pressure that would put it back.
func TestZeusBriefModelSplit(t *testing.T) {
	for _, j := range []*Job{
		{Slug: "model-plain"},
		{Slug: "model-solo", Solo: true},
	} {
		brief := zeusBrief(j)
		// the tier is stated as the RESOLVED id, so this assertion follows
		// core.ZeusModel instead of pinning a spelling that goes stale with it
		if !strings.Contains(brief, "kraken spawned you on `"+core.ZeusModel+"`") ||
			!strings.Contains(brief, "EVERY OTHER agent in this cell runs on Claude Opus 5") ||
			!strings.Contains(brief, "`model: opus`") {
			t.Errorf("%s brief missing the zeus-ZeusModel/crew-Opus split", j.Slug)
		}
		// Lowercased, because the brief now legitimately contains the word
		// "fable" (zeus' own tier): a case-sensitive haystack would wave
		// through a future "run hephaestus on Fable" or "model: Fable", which
		// is the exact edit this guard exists to catch.
		low := strings.ToLower(brief)
		for _, tier := range []string{"sonnet model", "haiku model", "fable model", "model: sonnet", "model: haiku", "model: fable"} {
			if strings.Contains(low, tier) {
				t.Errorf("%s brief pins a crew role to %q — every agent zeus spawns is Opus 5", j.Slug, tier)
			}
		}
	}
	// a job's model: override is what zeus is actually spawned on, so it is
	// what the brief must state — a hardcoded tier name lies here
	pinned := zeusBrief(&Job{Slug: "model-pinned", Model: "sonnet"})
	if !strings.Contains(pinned, "kraken spawned you on `sonnet`") {
		t.Error("a job with model: set must have its own tier stated in the brief, not core.ZeusModel")
	}
	if !strings.Contains(pinned, "EVERY OTHER agent in this cell runs on Claude Opus 5") {
		t.Error("the crew stays Opus regardless of zeus' own tier")
	}
}

// TestZeusBriefImageHandling asserts the [image: <path>] hardening line
// rides in every brief variant — it's not gated by solo, since carl can
// attach images to any job's spec or a steering message.
func TestZeusBriefImageHandling(t *testing.T) {
	for _, j := range []*Job{
		{Slug: "img-plain"},
		{Slug: "img-solo", Solo: true},
	} {
		brief := zeusBrief(j)
		if !strings.Contains(brief, "[image: <path>]") {
			t.Errorf("%s brief missing the image-handling line", j.Slug)
		}
	}
}

// TestZeusBriefLoadEstimate pins the load-estimate rule into every brief
// variant. Solo is not a reason to skip it: the estimate is about what CARL
// must spend to unblock the cell, which has nothing to do with whether the
// cell delegates.
//
// The slug is checked INSIDE the command because the brief interpolates the
// cell's real slug rather than leaving a `<slug>` placeholder — which is the
// difference between the brief's spelling of a cell verb and the skills'. A
// skill writes `kraken questions get <slug>` (aim/SKILL.md) and relies on the
// brief's "the job slug every kraken CLI verb takes is: %s" line to resolve it;
// the brief, already holding the value, substitutes it. Asserting the bare verb
// would pass on either spelling.
//
// Two phrases from the ruler are asserted alongside the verbs, and that is the
// load-bearing half: a verbs-only assertion stays green while the paragraph
// says nothing about what the numbers MEAN, and numbers with no shared ruler
// behind them are incomparable between cells, which is the whole feature.
//
// What the paragraph carries has changed, so what these two phrases stand for
// has too (2026-07-31). It used to spell a 1-10 scale out inline, ~600 bytes of
// anchor text; it now carries the verbs, the two pricing rules, the compact
// `unblock/scope` state map, the anti-inflation line and a pointer to
// `.claude/skills/load/SKILL.md`, and the twenty anchored rungs live in that
// skill file instead. So these phrases no longer proxy for a ladder that is
// present — they pin the two AXES still being named here, and the ladder's
// absence is enforced from the other side by the contract tests
// (TestEdgeBriefCarriesNoLadderRow, TestContractZeusBriefLoadScale), which fail
// if a rung label reappears in the brief. Adding an anchor back here would be
// caught there, not by this test.
func TestZeusBriefLoadEstimate(t *testing.T) {
	for _, j := range []*Job{
		{Slug: "load-plain"},
		{Slug: "load-solo", Solo: true},
	} {
		brief := zeusBrief(j)
		for _, want := range []string{
			"kraken load set " + j.Slug,
			"kraken load clear " + j.Slug,
			"--unblock",
			"--scope",
			// one anchor per axis, so the 1-10 ruler cannot be dropped silently
			"1-10 is the effort CARL spends",
			"a one-message reply",
		} {
			if !strings.Contains(brief, want) {
				t.Errorf("%s brief missing %q from the load-estimate rule", j.Slug, want)
			}
		}
	}
}

func TestZeusBriefSolo(t *testing.T) {
	j := &Job{Slug: "z", Solo: true}
	brief := zeusBrief(j)
	if !strings.Contains(brief, "no teammates and no subagents at all") {
		t.Error("solo brief missing the no-teammates/no-subagents sentence")
	}
	// solo means alone: not even the one sanctioned unnamed use (read-only
	// exploration) survives, so the carve-out must be disclaimed here
	if !strings.Contains(brief, "not even a read-only exploration subagent") {
		t.Error("solo brief does not close the read-only-exploration carve-out")
	}
	if strings.Contains(brief, "implementation slices to teammates you name hephaestus") ||
		strings.Contains(brief, "delegate real work, it goes to TEAMMATES") {
		t.Error("solo brief still describes the hephaestus/athena delegation crew")
	}
	// the phase-2 paragraph must not silently re-introduce a crew either —
	// that self-contradiction ("no teammates and no subagents at all" AND
	// delegate here) is exactly what the second, independently-worded
	// delegation sentence used to cause
	for _, crew := range []string{"subagents named hephaestus", "teammates named hephaestus"} {
		if strings.Contains(brief, crew) {
			t.Errorf("solo brief still describes a second hephaestus/athena delegation crew (%q)", crew)
		}
	}
	// solo still gets the full cell-paths and row-reading contract: only the
	// CREW paragraphs are gated on Solo
	for _, want := range []string{
		"Cell paths", CellArtifacts(j), "kraken questions get z", "EVERY resume",
	} {
		if !strings.Contains(brief, want) {
			t.Errorf("solo brief missing %q", want)
		}
	}
}

// TestZeusBriefDocGatingUnconditional asserts the gating rule and the phase
// pointers ride in the brief regardless of any job field — there is no
// skip-doc flag to gate them on, so a plain job and a solo job see identical
// gating text (only the subagent paragraphs differ, per TestZeusBriefSolo).
func TestZeusBriefDocGatingUnconditional(t *testing.T) {
	for _, j := range []*Job{
		{Slug: "gate-plain"},
		{Slug: "gate-solo", Solo: true},
	} {
		brief := zeusBrief(j)
		for _, want := range []string{
			"Doc gating:", "/aim or /info", "doc, spec, or explanation",
			"skipping the doc phase is the DEFAULT",
			"the job spec is the contract; implement immediately",
			"Phase 1 has TWO paths", "EXPLANATION path", "SPEC path",
			".claude/skills/info/SKILL.md", ".claude/skills/aim/SKILL.md",
			"Phase 2", ".claude/skills/do/SKILL.md",
			// each skill's cell section, not the brief, owns the phase's
			// closing line — the brief must send zeus there for it
			"\"Inside A Kraken Cell\" section",
		} {
			if !strings.Contains(brief, want) {
				t.Errorf("%s brief missing gating text %q", j.Slug, want)
			}
		}
	}
}

// TestZeusBriefEscalationDisclaimer asserts every brief variant that
// mentions "escalation" only does so to disclaim it (there is no escalation
// file) — never describes one as an active mechanism.
func TestZeusBriefEscalationDisclaimer(t *testing.T) {
	for _, j := range []*Job{
		{Slug: "esc-plain"},
		{Slug: "esc-solo", Solo: true},
	} {
		brief := zeusBrief(j)
		if strings.Contains(brief, "escalation") && !strings.Contains(brief, "no escalation file") {
			t.Errorf("%s (solo=%v) brief mentions escalation without disclaiming it", j.Slug, j.Solo)
		}
	}
}

func TestZeusBriefSubagentHygiene(t *testing.T) {
	brief := zeusBrief(&Job{Slug: "hygiene"})
	if !strings.Contains(brief, "stop an agent (TaskStop") {
		t.Error("brief missing the crew-hygiene clause")
	}
	// hygiene fires on ACCEPTANCE, not on the agent reporting done. Stopping
	// at "done" kills the teammate one step before the gate that judges it —
	// the lint run, the athena round — and the fix round then has to address
	// an agent that no longer exists, which is exactly what the skills forbid
	// (they require resuming the same one). This clause is what sequences the
	// two rules; without it they contradict each other.
	for _, want := range []string{
		"the moment its work is ACCEPTED, not the moment it reports done",
		"the gate that judges its output has passed it",
		"Once accepted, stop it immediately",
	} {
		if !strings.Contains(brief, want) {
			t.Errorf("brief hygiene clause missing %q — teardown must not race the fix round", want)
		}
	}
	// a teammate holds a real tmux pane, so the hygiene clause has to say how
	// TaskStop addresses one — a pane left resident is visible clutter in `k v`
	if !strings.Contains(brief, "a teammate stops by its bare name") {
		t.Error("brief does not say how TaskStop addresses a teammate")
	}
	// solo jobs run no teammates or subagents at all, so the hygiene clause is moot
	solo := zeusBrief(&Job{Slug: "hygiene-solo", Solo: true})
	if strings.Contains(solo, "TaskStop") {
		t.Error("solo brief should not mention TaskStop hygiene — it has no agents to stop")
	}
}

// TestZeusBriefTeammatesNotSubagents pins the distinction the cell depends on:
// every agent that WRITES something or returns a verdict zeus acts on is a
// teammate — an Agent-tool call carrying a `name`, which is what buys it a tmux
// pane, a team-config row core.nestTeams can hang under this cell, and an
// address SendMessage can reach for a second round. Anonymous in-process
// subagents keep exactly one job, read-only exploration, and a brief that blurs
// the two gives carl a cell whose real work happens in panes he cannot see.
func TestZeusBriefTeammatesNotSubagents(t *testing.T) {
	brief := zeusBrief(&Job{Slug: "teammates"})
	for _, want := range []struct{ token, why string }{
		// (a) the crew is named as teammates, not subagents
		{"implementation slices to teammates you name hephaestus", "hephaestus is a teammate"},
		// the authoring pair and the review-only third are introduced in two
		// different sentences now, so the teammate-ness of each is pinned where
		// it is actually claimed rather than in one merged phrase.
		{"dispatch ONE athena-logic and ONE athena-edge", "the test-authoring variants are named agents"},
		{"a third teammate you name athena-style", "the review-only variant is a teammate too"},
		// (b) the mechanism: a `name` on the Agent tool, and what it buys
		{"A teammate is an Agent-tool call that passes a `name`", "the teammate mechanism"},
		{"its own tmux pane", "why naming matters to kraken: a real pane"},
		{"`k ls`/`k v`", "a teammate is visible to carl through kraken"},
		{"SendMessage", "a named teammate is resumable for the fix round"},
		// (c) the single carve-out for unnamed subagents
		{"The ONE exception is read-only exploration", "the only sanctioned unnamed use"},
		// "read-only" is scoped to the REPO, not the filesystem: an exploration
		// subagent still writes its own report to a scratch path. The brief said
		// "writing nothing" until a dream pass found the cost — several
		// hundred-line findings documents stranded as chat replies, because the
		// teammate->lead channel truncates — so the carve-out has to say which
		// kind of writing it forbids, or it reads as a ban on the report itself.
		{"changing no file in the repo", "an exploration subagent leaves the repo alone"},
		{"it still writes its own report to the scratch path you give it", "…but its findings are not a chat reply"},
		{"If an agent will change a file in the repo or judge someone's output, it is a teammate", "the decision rule"},
		{"a verdict means an accept/reject judgment on work product, not a finding", "the carve-out stays decidable at the margin"},
	} {
		if !strings.Contains(brief, want.token) {
			t.Errorf("brief missing %q (%s)", want.token, want.why)
		}
	}
	// the old wording delegated real work to anonymous subagents; if it comes
	// back, hephaestus and athena lose their panes and their addressability
	for _, stale := range []string{
		"Delegate implementation slices to subagents you name hephaestus",
		"three subagents you name athena-logic",
	} {
		if strings.Contains(brief, stale) {
			t.Errorf("brief still delegates real work to anonymous subagents: %q", stale)
		}
	}
}

// TestZeusBriefBlindTestAuthorship pins the separation between who implements a
// slice and who decides what that slice was supposed to do.
//
// The anchors are kept to the shortest fragment that still identifies the rule
// — a verdict name, a short distinctive phrase — rather than the sentences they
// sit in. That is a deliberate limit rather than a preference: this file already
// holds ~112 anchors, 39 of them five words or longer, and every one of those is
// a build break waiting on a harmless reword. TestZeusBriefTeammatesNotSubagents
// in this same change is the demonstration — its anchor had to be rewritten
// because the sentence carrying it was restructured, while the claim it pins
// (all three variants are teammates) never changed at all. Where a rule
// only exists as a clause (the fix-brief citation rule below), the anchor takes
// its distinctive noun phrase and leaves the rest of the sentence free to move.
//
// What each anchor protects is a distinct way the arrangement collapses back
// into the old one —
//
//   - "never writes a test for the spec": the implementer writing the tests is
//     the thing being removed; without this the rest is decoration.
//   - the regression carve-out: hephaestus may still pin a defect it fixes, so
//     the rule needs its boundary stated or it reads as "hephaestus never
//     touches a test file" and a fixed bug ships unpinned.
//   - "SAME message": the authors are blind because they start before there is
//     an implementation to read, not because they promised not to look. A brief
//     that drops this leaves a promise nobody can audit.
//   - "do NOT run": tests written against absent code fail for uninteresting
//     reasons; the barrier is what makes the first real run meaningful.
//   - the three verdict names: a failing blind test is ambiguous by
//     construction, and a brief without the trichotomy leaves zeus to invent one
//     — the tempting invention being "the test must be wrong, weaken it".
//   - "never send athena to read the implementation": the one resolution that
//     silently converts every later round back to sighted authorship.
func TestZeusBriefBlindTestAuthorship(t *testing.T) {
	brief := zeusBrief(&Job{Slug: "blind"})
	for _, want := range []struct{ token, why string }{
		{"NO test list", "the slice brief stops enumerating cases"},
		{"never writes a test for the spec", "the implementer does not author the requirement's tests"},
		{"regression pin", "…but a defect it fixes may still be pinned"},
		{"tests bugs, athena tests requirements", "the line between the two"},
		{"SAME message", "authoring starts before there is code to read"},
		{"ONE athena-logic and ONE athena-edge", "one authoring pair for the fan-out, not one per slice"},
		{"no sight of the implementation", "the blindness is stated, not implied"},
		{"do NOT run", "authoring and running are separate steps"},
		{"EVERY hephaestus slice is done", "one barrier for the whole fan-out"},
		{"ONE new Agent call, not three", "the live authors are resumed, not re-spawned"},
		{"code bug", "verdict 1"},
		{"test bug", "verdict 2"},
		{"contract defect", "verdict 3"},
		{"a requirement nobody set", "silence defaults to a test bug, not an amendment"},
		{"a genuine product decision", "…and to a contract defect only when it is a real decision"},
		{"read the implementation", "the resolution that would destroy the property"},
		{"contract obligation, never the failing assertion", "the test must not become the requirement"},
		{"BOTH gates have cleared its slice", "the implementer outlives both gates, not just the join"},
		{"Arbitrate the join BEFORE any review", "an author must finish while still blind"},
	} {
		if !strings.Contains(brief, want.token) {
			t.Errorf("brief missing %q (%s)", want.token, want.why)
		}
	}
	// Solo has no crew at all, so every clause above is unactionable there and
	// would be pure bulk in a brief already close to its ceiling.
	// Checked here: every anchor above that names crew procedure. A clause moved
	// out of the else arm into the shared prefix would otherwise leave this green
	// while the solo brief carried procedure it cannot act on. Deliberately count-free:
	// this comment has been wrong twice already, each time because an anchor was
	// added and the number was not.
	// The four omitted are the bare verdict names and "read the implementation":
	// generic enough English that unrelated future prose could plausibly contain
	// one, so asserting their ABSENCE would pin a coincidence rather than a rule.
	solo := zeusBrief(&Job{Slug: "blind-solo", Solo: true})
	for _, want := range []struct{ token, why string }{
		{"NO test list", "the slice brief stops enumerating cases"},
		{"never writes a test for the spec", "the implementer does not author the requirement's tests"},
		{"regression pin", "…but a defect it fixes may still be pinned"},
		{"tests bugs, athena tests requirements", "the line between the two"},
		{"SAME message", "authoring starts before there is code to read"},
		{"ONE athena-logic and ONE athena-edge", "one authoring pair for the fan-out, not one per slice"},
		{"no sight of the implementation", "the blindness is stated, not implied"},
		{"do NOT run", "authoring and running are separate steps"},
		{"EVERY hephaestus slice is done", "one barrier for the whole fan-out"},
		{"ONE new Agent call, not three", "the live authors are resumed, not re-spawned"},
		{"a requirement nobody set", "silence defaults to a test bug, not an amendment"},
		{"a genuine product decision", "…and to a contract defect only when it is a real decision"},
		{"contract obligation, never the failing assertion", "the test must not become the requirement"},
		{"BOTH gates have cleared its slice", "the implementer outlives both gates, not just the join"},
		{"Arbitrate the join BEFORE any review", "an author must finish while still blind"},
	} {
		if strings.Contains(solo, want.token) {
			t.Errorf("solo brief carries the blind-authorship contract (%q — %s) despite having no crew",
				want.token, want.why)
		}
	}
}

// TestZeusBriefDelegationIsEarned guards the other half of the teammate rule.
// Insisting on teammates for real work reads, on its own, like an instruction
// to delegate everything — and a zeus that briefs a hephaestus for a two-file
// edit spends more context writing the brief than the edit would have cost.
// The brief must therefore gate delegation on work that genuinely splits, and
// must keep the review pass ungated: reviews are independent by construction,
// so they parallelize even when the edit was zeus' own.
func TestZeusBriefDelegationIsEarned(t *testing.T) {
	brief := zeusBrief(&Job{Slug: "earned"})
	for _, want := range []struct{ token, why string }{
		{"work that actually parallelizes", "delegation is gated on parallelism"},
		{"just make the edit yourself", "small self-contained changes stay with zeus"},
		{"parallelizable implementation slices", "hephaestus takes the splittable work"},
		{"run them in parallel every time", "the review pass itself is never gated"},
		{"on your own edits exactly as on a teammate's", "review is not gated the same way"},
	} {
		if !strings.Contains(brief, want.token) {
			t.Errorf("brief missing %q (%s)", want.token, want.why)
		}
	}
	// solo already forbids the crew outright, so the gate is meaningless there
	// and must not add bulk to a brief that cannot act on it
	if strings.Contains(zeusBrief(&Job{Slug: "earned-solo", Solo: true}), "work that actually parallelizes") {
		t.Error("solo brief carries the delegation gate despite having no crew to gate")
	}
}

// TestZeusBriefNoTerminalQuestions pins carl's steering: he does not watch the
// cell's pane, so an interactive prompt there is invisible AND blocking — the
// cell reads as working while nothing moves. The board's status detail is
// zeus' last message, which makes "default, state it, keep going" the only
// path that actually reaches him. The rule is strictest for spawned agents:
// kraken reads zeus' transcript alone, so a teammate's prompt cannot surface
// anywhere at all.
func TestZeusBriefNoTerminalQuestions(t *testing.T) {
	for _, j := range []*Job{{Slug: "q-plain"}, {Slug: "q-solo", Solo: true}} {
		brief := zeusBrief(j)
		for _, want := range []struct{ token, why string }{
			{"must NOT ask carl questions through an interactive terminal prompt", "the rule itself"},
			{"he does not watch it", "why a prompt is invisible"},
			{"just stalls the cell", "an unseen prompt is not merely useless, it blocks"},
			{"pick the most defensible default", "what to do instead"},
			{"FINAL message", "where an unresolved decision goes"},
			{"last resort", "AskUserQuestion is not a convenience"},
		} {
			if !strings.Contains(brief, want.token) {
				t.Errorf("%s brief missing %q (%s)", j.Slug, want.token, want.why)
			}
		}
	}
	// the spawned-agent ban only makes sense where agents can be spawned
	plain := zeusBrief(&Job{Slug: "q-crew"})
	for _, want := range []string{
		"A teammate or subagent must never ask carl anything at all",
		"decide and report, never to prompt",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("brief missing spawned-agent question ban %q", want)
		}
	}
	// the rule it replaced invited the prompt as a normal move; a brief
	// carrying both says "don't ask" and "you may ask" in adjacent sentences
	for _, j := range []*Job{{Slug: "q-plain"}, {Slug: "q-solo", Solo: true}} {
		if strings.Contains(zeusBrief(j), "you may use AskUserQuestion") {
			t.Errorf("%s brief re-admits the permissive AskUserQuestion wording", j.Slug)
		}
	}
}

func TestZeusBriefSelfDestructiveGuard(t *testing.T) {
	// every job — solo included — carries the never-target-your-own-slug
	// rule: the status-investigation incident was a zeus confirming a test
	// build's /api/done against its own live job and decapitating itself
	for _, j := range []*Job{
		{Slug: "g-plain"},
		{Slug: "g-solo", Solo: true},
	} {
		brief := zeusBrief(j)
		for _, want := range []string{"OWN job's slug", "scratch job fixture", "decapitates"} {
			if !strings.Contains(brief, want) {
				t.Errorf("%s (solo=%v) brief missing %q", j.Slug, j.Solo, want)
			}
		}
	}
}

// TestZeusBriefNeverDeclaresDone pins the completion boundary in every
// variant: accepting is carl's move, and a merged PR is not acceptance.
func TestZeusBriefNeverDeclaresDone(t *testing.T) {
	for _, j := range []*Job{
		{Slug: "done-plain"},
		{Slug: "done-solo", Solo: true},
	} {
		brief := zeusBrief(j)
		for _, want := range []string{"Never declare the job done", "never ends the job"} {
			if !strings.Contains(brief, want) {
				t.Errorf("%s (solo=%v) brief missing %q", j.Slug, j.Solo, want)
			}
		}
	}
}

// TestZeusBriefSkillsPrecedence asserts the worktree .claude/skills/
// precedence sentence rides in every brief variant — it's not gated by
// solo, since kraken materializes its own skills into every cell worktree
// regardless of workflow. With every procedure now living in those skills,
// this sentence is what makes the whole split work.
func TestZeusBriefSkillsPrecedence(t *testing.T) {
	for _, j := range []*Job{
		{Slug: "prec-plain"},
		{Slug: "prec-solo", Solo: true},
	} {
		brief := zeusBrief(j)
		for _, want := range []string{
			".claude/skills/", "kraken-owned and canonical",
			"shadows one of them", "never commit `.claude/` changes",
		} {
			if !strings.Contains(brief, want) {
				t.Errorf("%s (solo=%v) brief missing skills-precedence text %q", j.Slug, j.Solo, want)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// The colour rule, on the skills' side.
//
// The contract (hermes-contract.md) tells HERMES what a treatment looks like and
// what it claims. But hermes never picks one: the brief it is handed assigns the
// treatment and names the glyph, and the brief is written by zeus. So the half of
// the rule that decides WHICH colour a box gets has to live where zeus reads it —
// in the two skills that govern the doc phase — or it lives nowhere, and zeus
// picks by taste.
//
// Both files carry it, and both must: the explanation path reads info/SKILL.md
// INSTEAD of aim/SKILL.md, never both, so a rule present in only one is absent
// from one whole path. Same reason the diagram-teammate naming rule is duplicated
// there (TestSkillsOwnTheCellContract in skills_embed_test.go).
// ---------------------------------------------------------------------------

// treatmentGlyphs is the palette as the skills must state it: six treatments,
// each paired with the glyph that carries its meaning through greyscale, print,
// and red–green CVD. Held as a Go literal because this test asks whether the
// SKILL.md files say the right thing, not whether they agree with each other.
var treatmentGlyphs = []struct{ class, glyph string }{
	{"grey", "∅"},
	{"red", "✗"},
	{"yellow", "⚠"},
	{"green", "✓"},
	{"blue", "+"},
	{"purple", "★"},
}

// readRepoSkill reads a SKILL.md out of the repo's own .claude/skills tree. The
// package's tests run with cwd at internal/jobs, and this file already speaks in
// worktree-relative asset paths (HermesContractPath and friends), so the repo
// root is two levels up from here. Fails loudly rather than skipping: a test that
// silently passes because it could not find the file is the failure mode the
// whole crew-contract suite exists to avoid.
func readRepoSkill(t *testing.T, skill string) string {
	t.Helper()
	path := filepath.Join("..", "..", ".claude", "skills", skill, "SKILL.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v — the skill moved or this test's repo-root-relative path is wrong; fix "+
			"the path rather than leaving the colour-rule check silently dead", path, err)
	}
	return string(data)
}

// TestSkillsCarryColourRule pins the colour rule in both doc-phase skills.
//
// Each treatment must be named WITH its glyph on the same line — a rule that
// lists six class names and a separate pile of glyphs leaves zeus to pair them,
// and a mispaired glyph is worse than none (it asserts the wrong claim in the one
// channel that survives greyscale). Both decisive rules must be stated, because
// they are the two calls zeus actually hesitates over: red vs yellow turns on
// confirmed-AND-critical, blue vs green on provenance-beats-state. And each skill
// must point at the contract's Colour Semantics section, since the bans, the
// exemptions and the decision procedure live there and are deliberately NOT
// copied here.
func TestSkillsCarryColourRule(t *testing.T) {
	for _, skill := range []string{"aim", "info"} {
		src := readRepoSkill(t, skill)
		lines := strings.Split(src, "\n")

		for _, tr := range treatmentGlyphs {
			token := "`" + tr.class + "`"
			named, paired := false, false
			for _, line := range lines {
				if !strings.Contains(line, token) {
					continue
				}
				named = true
				if strings.Contains(line, tr.glyph) {
					paired = true
					break
				}
			}
			if !named {
				t.Errorf(".claude/skills/%s/SKILL.md never names the treatment %s — zeus reads this file to "+
					"decide which colour a box gets, so a treatment it does not list is one zeus will never "+
					"assign (or will assign by taste)", skill, token)
				continue
			}
			if !paired {
				t.Errorf(".claude/skills/%s/SKILL.md names %s but not its glyph %q on the same line — zeus "+
					"assigns the treatment AND names the glyph in the hermes brief, and pairing them by hand "+
					"from two separate lists is how a box ends up asserting the wrong claim in the one channel "+
					"that survives greyscale", skill, token, tr.glyph)
			}
		}

		for _, rule := range []struct{ want, why string }{
			{"red vs yellow", "the first decisive rule, by name"},
			{"confirmed AND critical", "what red actually requires — both halves"},
			{"blue vs green", "the second decisive rule, by name"},
			{"provenance", "blue is about provenance, green about state"},
		} {
			if !strings.Contains(src, rule.want) {
				t.Errorf(".claude/skills/%s/SKILL.md is missing %q (%s) — these are the two calls zeus "+
					"hesitates over, and a skill that lists the six treatments without them leaves both "+
					"undecidable", skill, rule.want, rule.why)
			}
		}

		for _, pointer := range []string{HermesContractPath(), "Colour Semantics"} {
			if !strings.Contains(src, pointer) {
				t.Errorf(".claude/skills/%s/SKILL.md no longer points at %q — the three bans, the two "+
					"exemptions and the decision procedure live in the contract and are deliberately not "+
					"copied here, so dropping the pointer drops them from this path entirely", skill, pointer)
			}
		}
	}
}

func TestZeusBriefUniquePerJob(t *testing.T) {
	a := zeusBrief(&Job{Slug: "a"})
	b := zeusBrief(&Job{Slug: "b"})
	if a == b {
		t.Error("briefs must be unique per job")
	}
}
