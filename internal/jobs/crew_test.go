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
// is carl's call via /do. These tests assert that unified contract across
// the solo and skip-doc flags, in the same string-contains style as the rest
// of the job tests.

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
		// the doc phase is now a flat artifact SET, not a single combined
		// doc: the overview + questions file get referenced by their real
		// paths (DiagramCardPath() is pinned in the worktree-relative loop
		// below, so the bare "diagram-card.html" substring isn't repeated here)
		docOverviewPath(j), QuestionsPath(j),
		// the mode-scoped floor: spec mode requires the testing-plan section
		// doc and a worked-example diagram card; explain mode does not
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
		// strictly stronger than the old 3-word exact-phrase pin — it also
		// catches a regression that rephrases the wording, e.g. "flatten the
		// set" or "flattening the fragments".
		"flatten", "poll",
	} {
		if strings.Contains(brief, unwanted) {
			t.Errorf("brief still mentions %q", unwanted)
		}
	}
	// escalation-disclaimer coverage lives in TestZeusBriefEscalationDisclaimer
	// (it loops over all four brief variants; "escalation" is an ungated line
	// so a per-test check here would just duplicate that one)
}

// TestZeusBriefImageHandling asserts the [image: <path>] hardening line
// rides in every brief variant — it's not gated by solo or skip-doc, since
// carl can attach images to any job's spec or a steering message.
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
	// the ending's build-shaped branches (skip-doc and Phase 2) use a
	// second, independent phrasing for the same delegation ("subagents
	// named hephaestus", not "subagents you name hephaestus") — pin it
	// too, since that was the actual self-contradiction: a solo brief
	// could say "no subagents at all" AND still delegate here
	if strings.Contains(brief, "subagents named hephaestus") {
		t.Error("solo brief still describes the ending's hephaestus/athena delegation crew")
	}
	if strings.Contains(brief, "hermes") {
		t.Error("solo brief should not mention hermes (no diagram subagents)")
	}
	// solo still writes the overview + questions.yaml by hand (no
	// subagent needed for prose/yaml), but never a diagram card (there's
	// no hermes to render one), so neither the contract nor the lint
	// gate nor the card template should appear
	for _, want := range []string{docOverviewPath(j), QuestionsPath(j)} {
		if !strings.Contains(brief, want) {
			t.Errorf("solo brief missing %q", want)
		}
	}
	// solo jobs get the FULL questions.yaml contract too (gated only on
	// !SkipDoc, not on !Solo) — the strict format, kraken's answer/
	// answered-at ownership, and the re-read-on-resume rule all apply
	for _, want := range []string{
		"answered-at", "re-read", "questions:", "- id: Q1", "importance",
	} {
		if !strings.Contains(brief, want) {
			t.Errorf("solo brief missing questions.yaml contract text %q", want)
		}
	}
	// bare "diagram-card.html" is the strictly stronger absence check
	// (DiagramCardPath() is a substring of it, and worktree-relative
	// path pins for it already live in TestZeusBrief's loop) —
	// don't duplicate the full path here
	for _, unwanted := range []string{"hermes-contract.md", "lint_svg.py", "diagram-card.html"} {
		if strings.Contains(brief, unwanted) {
			t.Errorf("solo brief should not mention %q (no doc pipeline)", unwanted)
		}
	}
}

func TestZeusBriefSkipDoc(t *testing.T) {
	j := &Job{Slug: "sd", SkipDoc: true}
	brief := zeusBrief(j)
	// no doc pipeline, no phase gate — but the crew, the manual-test gate,
	// and the PR pipeline all survive the branch
	for _, want := range []string{
		"skips the doc phase", "hephaestus", "athena", "manual test ready",
		"open a PR with gh", "do NOT invoke /pr by name", "never ends the job", CellArtifacts(j),
		// like the escalation disclaimer: /aim and /do may appear only to
		// say they don't apply
		"no spec doc", "no '/do' gate",
		// no self-polling even on the skip-doc branch: the daemon watches
		// the PR and bumps the pane
		"bumps your pane",
	} {
		if !strings.Contains(brief, want) {
			t.Errorf("skip-doc brief missing %q", want)
		}
	}
	for _, unwanted := range []string{
		"Phase 1", "Phase 2", "hermes", "hermes-contract.md", "lint_svg.py", "doc ready for review",
		"/send", "you inherit carl's user skills",
		"diagram-card.html", "questions.yaml", "poll", "flatten",
	} {
		if strings.Contains(brief, unwanted) {
			t.Errorf("skip-doc brief still mentions %q", unwanted)
		}
	}
	// solo composes: skip-doc + solo has neither doc pipeline nor crew, and
	// (fix 13) the solo doc-shape sentence itself is gated on !SkipDoc — a
	// solo+skip-doc job gets ONLY the bare no-subagents line, no mention of
	// questions.yaml or 00-overview.md at all
	solo := zeusBrief(&Job{Slug: "sd2", SkipDoc: true, Solo: true})
	if !strings.Contains(solo, "no subagents at all") || strings.Contains(solo, "hermes") {
		t.Error("skip-doc solo brief should keep the no-subagents delta and stay hermes-free")
	}
	// build-solo variant of the type-switch self-contradiction fix: the
	// skip-doc branch must not delegate to a hephaestus/athena crew either
	if strings.Contains(solo, "subagents named hephaestus") {
		t.Error("skip-doc solo brief still delegates to the hephaestus/athena crew")
	}
	for _, unwanted := range []string{"questions.yaml", "00-overview.md"} {
		if strings.Contains(solo, unwanted) {
			t.Errorf("skip-doc solo brief should not mention %q — the doc phase is entirely absent", unwanted)
		}
	}
}

// TestZeusBriefEscalationDisclaimer asserts every brief variant that
// mentions "escalation" only does so to disclaim it (there is no escalation
// file) — never describes one as an active mechanism. The line is ungated,
// so it rides in all four brief shapes; loop over them here rather than
// duplicating the same check inside each per-workflow test.
func TestZeusBriefEscalationDisclaimer(t *testing.T) {
	for _, j := range []*Job{
		{Slug: "esc-plain"},
		{Slug: "esc-solo", Solo: true},
		{Slug: "esc-skipdoc", SkipDoc: true},
		{Slug: "esc-solo-skipdoc", SkipDoc: true, Solo: true},
	} {
		brief := zeusBrief(j)
		if strings.Contains(brief, "escalation") && !strings.Contains(brief, "no escalation file") {
			t.Errorf("%s (solo=%v skipdoc=%v) brief mentions escalation without disclaiming it", j.Slug, j.Solo, j.SkipDoc)
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
// solo or skip-doc, since kraken materializes its own skills into every
// cell worktree regardless of workflow.
func TestZeusBriefSkillsPrecedence(t *testing.T) {
	for _, j := range []*Job{
		{Slug: "prec-plain"},
		{Slug: "prec-solo", Solo: true},
		{Slug: "prec-skipdoc", SkipDoc: true},
	} {
		brief := zeusBrief(j)
		for _, want := range []string{
			".claude/skills/", "kraken-owned and canonical",
			"shadows one of them", "never commit `.claude/` changes",
		} {
			if !strings.Contains(brief, want) {
				t.Errorf("%s (solo=%v skipdoc=%v) brief missing skills-precedence text %q", j.Slug, j.Solo, j.SkipDoc, want)
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
