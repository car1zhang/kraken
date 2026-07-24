package jobs

import (
	"path/filepath"
	"strings"
	"testing"
)

// crew_test.go — zeus' brief content matrix. There is no hermes pane agent
// and no escalation-file relay any more: hermes is only a per-diagram
// subagent name inside zeus' own brief. Q14 removed the job-level build/info
// type entirely: the spec-vs-explain (/aim) mode is zeus' own call from what
// the job spec asks for, and whether phase 2 (implement + PR) ever happens
// is carl's call via /do. There is likewise no more job-level skip-doc
// field (the "job-shape cleanup" companion to Q14): whether Phase 1 (the
// doc pipeline) runs at all is also zeus' own call, made from the job spec
// text per the doc-gating rule, so every doc-pipeline paragraph rides in
// every brief variant now — gated only by Solo, never by a doc-skip flag.
// These tests assert that unified contract across the solo flag, in the
// same string-contains style as the rest of the job tests.

func TestZeusBrief(t *testing.T) {
	j := &Job{Slug: "x"}
	brief := zeusBrief(j)
	for _, want := range []string{
		"zeus", "hephaestus", "athena", "/aim", "'/do'", "/pr", "PR", "manual test ready",
		"AskUserQuestion", CellArtifacts(j), "TaskStop",
		// a merged PR is never job completion; follow-ups recycle the branch
		"never ends the job", "checkout -B " + CellBranch(j) + " origin/main",
		// hermes briefs must carry the style contract verbatim and be lint-gated
		// (full-path pins for HermesContractPath/LintSvgPath live in the
		// worktree-relative loop below — no need to duplicate the bare
		// filename here too)
		"--marker-id",
		// the lint command must be anchored to the worktree root, since the
		// fragment path is absolute but the lint script path is worktree-relative
		"from the worktree root",
		// three named single-dimension athena review variants, dispatched in
		// parallel, with a targeted (not full) re-review on a blocking finding
		"athena-logic", "athena-edge", "athena-style",
		"dispatched in parallel", "one message, three Agent calls",
		"re-review with only the flagging variant(s)",
		// /pr is a same-named-shadow risk, so zeus must read the worktree
		// copy by path rather than invoking it by name
		"do NOT invoke /pr by name",
		// the doc phase is a flat artifact SET, not a single combined doc:
		// the overview is referenced by its real path
		docOverviewPath(j),
		// the mode-scoped floor: spec mode requires the testing-plan section
		// doc; explain mode does not
		filepath.Join(CellArtifacts(j), "52-testing-plan.md"), "REQUIRED",
		"spec mode", "explain mode", "encouraged, not required",
		// a purely explanatory job never gets a /do or a PR — carl accepts
		// straight from the board (the unified ending's other half)
		"never gets a '/do'", "carl accepts the artifact set straight from the board",
		// the wrap step must quote the template's REAL placeholder literally,
		// and pin the str.replace (never re.sub) mechanism
		"<!-- DIAGRAM: {{DIAGRAM_ID}} -->", "{{CARD_TITLE}}", "{{CAPTION}}",
		"str.replace", "re.sub", "10-49",
		// TITLE/CAPTION land in markup positions (title, h1, figcaption), so
		// the wrap one-liner must escape them, never the fragment
		"html.escape", "NEVER escape the fragment",
		// diagram ids must be pinned unique across the whole set, distinct
		// from the marker-id/arrow-id relaxation
		"unique across the WHOLE set",
		// no self-polling: kraken's daemon watches the PR and bumps the pane
		"bumps your pane",
		// PR-bump handling: fix CI AND address review comments, plus the
		// awaiting-checks safety net so a silent job isn't mistaken for done
		"address new review comments", "awaiting checks", "NOT done silently",
		// the doc-gating rule (job-shape cleanup, Q14's companion): zeus'
		// own call from the spec text, never a job field, with skip as the
		// stated default
		"Doc gating", "/info", "DEFAULT",
		// the two-core diagram doctrine (53): architecture + end-to-end
		// flows, spec-mode combined current/target card drawn by one hermes,
		// tunnel cards as extras
		"ONE COMBINED card", "current-state panel", "target-state panel",
		"SAME hermes", "never split current/target across two hermes agents",
		"end-to-end flows", "ONE concrete worked example", "tunnel cards",
		"DEFAULT, not a cap",
		// 50-components.md must require a Kind row ahead of responsibility,
		// plus a Boundary row
		"Kind entry", "Boundary row", "OS process", "sqlite table",
		// the store-rows questions verb contract (54), replacing the old
		// questions.yaml format block
		"kraken questions get x", "kraken questions set x",
		"kraken questions add|update|remove x", "Q<digits>", "P0/P1/P2",
		"kraken owns `answer`/`answered-at`",
		// add/update read one question object on stdin, same as set; remove
		// takes a bare qid arg with no stdin — so zeus doesn't try inline
		// JSON args first and burn a turn
		"add/update read one question object on stdin", "remove takes a bare qid arg",
		// the new feedback verb contract (54)
		"kraken feedback get x", "kraken feedback addressed x",
		"kraken owns feedback entry creation and edits",
	} {
		if !strings.Contains(brief, want) {
			t.Errorf("brief missing %q", want)
		}
	}
	// the contract/lint/card-template paths must be worktree-RELATIVE (zeus'
	// cwd is the worktree) — never home-relative, never a bare ~-prefixed
	// path (which filepath.IsAbs alone would miss), and always rooted at
	// .claude/skills/
	for _, path := range []string{HermesContractPath(), LintSvgPath(), DiagramCardPath()} {
		if filepath.IsAbs(path) || strings.HasPrefix(path, "~") || !strings.HasPrefix(path, ".claude/skills/") || !strings.Contains(brief, path) {
			t.Errorf("brief does not reference a worktree-relative .claude/skills/ path (got %q)", path)
		}
	}
	for _, unwanted := range []string{
		// "plan.md" dropped from this list: 52-testing-plan.md is now a
		// legitimate required artifact name and legitimately contains that
		// substring — "done.md" alone still guards the old per-job
		// plan/done file convention.
		"done.md", "hermes pane", "relay", "Success criteria",
		"/send", "/doc skill", "you inherit carl's user skills",
		// there is no separate /info skill invocation any more — /aim covers
		// both modes now
		"do NOT invoke /info", "type:", "(type:",
		// self-polling is gone (the daemon watches PRs now) — bare "poll"
		// never appears in any brief variant. "flatten"/"Flatten"/"flattened"
		// likewise appear zero times in any brief variant now (the clause is
		// "no single combined doc file"), so the bare token is safe and
		// strictly stronger than the old 3-word exact-phrase pin.
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
	// escalation-disclaimer coverage lives in TestZeusBriefEscalationDisclaimer
	// (it loops over both brief variants; "escalation" is an ungated line so
	// a per-test check here would just duplicate that one)
}

// TestZeusBriefAllOpus pins the one-tier rule: every agent in a cell — zeus
// itself (spawned with core.DefaultModel) and every subagent it names — runs
// on Claude Opus 5. The Agent tool takes tier aliases rather than model ids,
// so the brief must say `model: opus` and must never name a cheaper tier for
// a crew role; hephaestus in particular used to be pinned to sonnet.
func TestZeusBriefAllOpus(t *testing.T) {
	for _, j := range []*Job{
		{Slug: "opus-plain"},
		{Slug: "opus-solo", Solo: true},
	} {
		brief := zeusBrief(j)
		if !strings.Contains(brief, "EVERY agent in this cell runs on Claude Opus 5") ||
			!strings.Contains(brief, "`model: opus`") {
			t.Errorf("%s brief missing the all-Opus rule", j.Slug)
		}
		for _, tier := range []string{"sonnet model", "haiku model", "fable model", "model: sonnet", "model: haiku", "model: fable"} {
			if strings.Contains(brief, tier) {
				t.Errorf("%s brief pins a crew role to %q — every agent is Opus 5", j.Slug, tier)
			}
		}
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

func TestZeusBriefSolo(t *testing.T) {
	j := &Job{Slug: "z", Solo: true}
	brief := zeusBrief(j)
	if !strings.Contains(brief, "no subagents at all") {
		t.Error("solo brief missing the no-subagents sentence")
	}
	if strings.Contains(brief, "Delegate implementation slices to subagents you name hephaestus") {
		t.Error("solo brief still describes the hephaestus/athena delegation crew")
	}
	// the ending's build-shaped branches use a second, independent phrasing
	// for the same delegation ("subagents named hephaestus", not "subagents
	// you name hephaestus") — pin it too, since that was the actual
	// self-contradiction: a solo brief could say "no subagents at all" AND
	// still delegate here
	if strings.Contains(brief, "subagents named hephaestus") {
		t.Error("solo brief still describes the ending's hephaestus/athena delegation crew")
	}
	if strings.Contains(brief, "hermes") {
		t.Error("solo brief should not mention hermes (no diagram subagents)")
	}
	// solo still writes the overview by hand (no subagent needed for
	// prose), but never a diagram card (there's no hermes to render one),
	// so the contract/lint/card-template paths should not appear
	if !strings.Contains(brief, docOverviewPath(j)) {
		t.Errorf("solo brief missing %q", docOverviewPath(j))
	}
	// solo jobs get the FULL questions verb contract too (gated only on
	// Solo for the SUBAGENT parts, never for the questions/feedback verb
	// paragraphs, which are unconditional) — the ids, importance, kraken's
	// answer/answered-at ownership, and the re-read-on-resume rule all apply
	for _, want := range []string{
		"kraken owns `answer`/`answered-at`", "re-read it on EVERY resume",
		"kraken questions get z", "Q<digits>", "P0/P1/P2",
	} {
		if !strings.Contains(brief, want) {
			t.Errorf("solo brief missing questions verb contract text %q", want)
		}
	}
	// bare "diagram-card.html" is the strictly stronger absence check
	// (DiagramCardPath() is a substring of it, and worktree-relative
	// path pins for it already live in TestZeusBrief's loop) —
	// don't duplicate the full path here
	for _, unwanted := range []string{"hermes-contract.md", "lint_svg.py", "diagram-card.html"} {
		if strings.Contains(brief, unwanted) {
			t.Errorf("solo brief should not mention %q (no doc pipeline subagents)", unwanted)
		}
	}
}

// TestZeusBriefDocGatingUnconditional asserts the doc-gating rule and every
// doc-pipeline paragraph ride in the brief regardless of any job field —
// there is no more skip-doc flag to gate them on, so a plain job and a solo
// job both see the identical doc-gating text (only the subagent-specific
// hermes paragraphs differ, covered by TestZeusBriefSolo).
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
			"Phase 1 — only when the doc-gating rule above says to take it",
			"If instead the doc-gating rule says to skip (the default)",
			"there is no spec doc and no '/do' gate",
		} {
			if !strings.Contains(brief, want) {
				t.Errorf("%s brief missing doc-gating text %q", j.Slug, want)
			}
		}
	}
}

// TestZeusBriefEscalationDisclaimer asserts every brief variant that
// mentions "escalation" only does so to disclaim it (there is no escalation
// file) — never describes one as an active mechanism. The line is ungated,
// so it rides in both brief shapes; loop over them here rather than
// duplicating the same check inside each per-workflow test.
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
	if !strings.Contains(brief, "the moment a subagent or teammate finishes its task, stop it (TaskStop)") {
		t.Error("brief missing the subagent-hygiene clause")
	}
	// solo jobs run no subagents at all, so the hygiene clause is moot
	solo := zeusBrief(&Job{Slug: "hygiene-solo", Solo: true})
	if strings.Contains(solo, "TaskStop") {
		t.Error("solo brief should not mention TaskStop hygiene — it has no subagents to stop")
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

// TestZeusBriefSkillsPrecedence asserts the worktree .claude/skills/
// precedence sentence rides in every brief variant — it's not gated by
// solo, since kraken materializes its own skills into every cell worktree
// regardless of workflow.
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

func TestZeusBriefUniquePerJob(t *testing.T) {
	a := zeusBrief(&Job{Slug: "a"})
	b := zeusBrief(&Job{Slug: "b"})
	if a == b {
		t.Error("briefs must be unique per job")
	}
}
