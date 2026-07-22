package jobs

import (
	"strings"
	"testing"

	"kraken/util"
)

// crew_test.go — zeus' brief content matrix. There is no hermes pane agent
// and no escalation-file relay any more: hermes is only a per-diagram
// subagent name inside zeus' own brief. These tests assert that contract
// across the build/info workflows and the solo flag, in the same
// string-contains style as the rest of the job tests.

func TestZeusBriefBuild(t *testing.T) {
	j := &Job{Slug: "x", Type: "build"}
	brief := zeusBrief(j)
	for _, want := range []string{
		"zeus", "hephaestus", "athena", "/send", "PR", "manual test ready",
		"AskUserQuestion", CellArtifacts(j), "TaskStop",
		// a merged PR is never job completion; follow-ups recycle the branch
		"never ends the job", "checkout -B " + CellBranch(j) + " origin/main",
		// hermes briefs must carry the style contract verbatim and be lint-gated
		"hermes-contract.md", "lint_svg.py", "--marker-id",
		// three named single-dimension athena review variants, dispatched in
		// parallel, with a targeted (not full) re-review on a blocking finding
		"athena-logic", "athena-edge", "athena-style",
		"dispatched in parallel", "one message, three Agent calls",
		"re-review with only the flagging variant(s)",
	} {
		if !strings.Contains(brief, want) {
			t.Errorf("build brief missing %q", want)
		}
	}
	// the contract path must be built home-relative via util.Home(), never a
	// bare ~ or a literal /Users/<name>
	if contract := hermesContractPath(); !strings.HasPrefix(contract, util.Home()) || !strings.Contains(brief, contract) {
		t.Errorf("build brief does not reference a home-relative contract path (got %q)", contract)
	}
	for _, unwanted := range []string{"plan.md", "done.md", "hermes pane", "relay", "Success criteria"} {
		if strings.Contains(brief, unwanted) {
			t.Errorf("build brief still mentions %q", unwanted)
		}
	}
	// the brief may explicitly tell zeus there's no escalation file (that's
	// the point), but must never describe one as an active mechanism
	if strings.Contains(brief, "escalation") && !strings.Contains(brief, "no escalation file") {
		t.Error("build brief mentions escalation without disclaiming it")
	}
}

func TestZeusBriefInfo(t *testing.T) {
	j := &Job{Slug: "y", Type: "info"}
	brief := zeusBrief(j)
	for _, want := range []string{"zeus", "/info", "No PR", "AskUserQuestion", CellArtifacts(j), "TaskStop"} {
		if !strings.Contains(brief, want) {
			t.Errorf("info brief missing %q", want)
		}
	}
	for _, unwanted := range []string{"plan.md", "done.md", "open a PR with gh", "hermes pane", "relay", "Success criteria"} {
		if strings.Contains(brief, unwanted) {
			t.Errorf("info brief still mentions %q", unwanted)
		}
	}
	if strings.Contains(brief, "escalation") && !strings.Contains(brief, "no escalation file") {
		t.Error("info brief mentions escalation without disclaiming it")
	}
}

// TestZeusBriefImageHandling asserts the [image: <path>] hardening line
// rides in every brief variant — it's not gated by type or solo, since carl
// can attach images to any job's spec or a steering message.
func TestZeusBriefImageHandling(t *testing.T) {
	for _, j := range []*Job{
		{Slug: "img-build", Type: "build"},
		{Slug: "img-info", Type: "info"},
		{Slug: "img-solo", Type: "build", Solo: true},
	} {
		brief := zeusBrief(j)
		if !strings.Contains(brief, "[image: <path>]") {
			t.Errorf("%s brief missing the image-handling line", j.Slug)
		}
	}
}

func TestZeusBriefSolo(t *testing.T) {
	for _, typ := range []string{"build", "info"} {
		j := &Job{Slug: "z", Type: typ, Solo: true}
		brief := zeusBrief(j)
		if !strings.Contains(brief, "no subagents at all") {
			t.Errorf("solo %s brief missing the no-subagents sentence", typ)
		}
		if strings.Contains(brief, "Delegate implementation slices to subagents you name hephaestus") {
			t.Errorf("solo %s brief still describes the hephaestus/athena delegation crew", typ)
		}
		if strings.Contains(brief, "hermes") {
			t.Errorf("solo %s brief should not mention hermes (no diagram subagents)", typ)
		}
		// solo jobs have no doc pipeline paragraph at all, so neither the
		// contract nor the lint gate should appear
		for _, unwanted := range []string{"hermes-contract.md", "lint_svg.py"} {
			if strings.Contains(brief, unwanted) {
				t.Errorf("solo %s brief should not mention %q (no doc pipeline)", typ, unwanted)
			}
		}
	}
}

func TestZeusBriefSkipDoc(t *testing.T) {
	j := &Job{Slug: "sd", Type: "build", SkipDoc: true}
	brief := zeusBrief(j)
	// no doc pipeline, no phase gate — but the crew, the manual-test gate,
	// and the PR pipeline all survive the branch
	for _, want := range []string{
		"skips the doc phase", "hephaestus", "athena", "manual test ready",
		"open a PR with gh", "never ends the job", CellArtifacts(j),
		// like the escalation disclaimer: /doc and /send may appear only to
		// say they don't apply
		"no /doc artifact", "no '/send' gate",
	} {
		if !strings.Contains(brief, want) {
			t.Errorf("skip-doc brief missing %q", want)
		}
	}
	for _, unwanted := range []string{
		"Phase 1", "Phase 2", "hermes", "hermes-contract.md", "lint_svg.py", "doc ready for review",
	} {
		if strings.Contains(brief, unwanted) {
			t.Errorf("skip-doc brief still mentions %q", unwanted)
		}
	}
	// solo composes: skip-doc + solo has neither doc pipeline nor crew
	solo := zeusBrief(&Job{Slug: "sd2", Type: "build", SkipDoc: true, Solo: true})
	if !strings.Contains(solo, "no subagents at all") || strings.Contains(solo, "hermes") {
		t.Error("skip-doc solo brief should keep the no-subagents delta and stay hermes-free")
	}
}

func TestZeusBriefSubagentHygiene(t *testing.T) {
	for _, typ := range []string{"build", "info"} {
		brief := zeusBrief(&Job{Slug: "hygiene-" + typ, Type: typ})
		if !strings.Contains(brief, "the moment a subagent or teammate finishes its task, stop it (TaskStop)") {
			t.Errorf("%s brief missing the subagent-hygiene clause", typ)
		}
	}
	// solo jobs run no subagents at all, so the hygiene clause is moot
	solo := zeusBrief(&Job{Slug: "hygiene-solo", Type: "build", Solo: true})
	if strings.Contains(solo, "TaskStop") {
		t.Error("solo brief should not mention TaskStop hygiene — it has no subagents to stop")
	}
}

func TestZeusBriefSelfDestructiveGuard(t *testing.T) {
	// every job type — solo included — carries the never-target-your-own-slug
	// rule: the status-investigation incident was a zeus confirming a test
	// build's /api/done against its own live job and decapitating itself
	for _, j := range []*Job{
		{Slug: "g-build", Type: "build"},
		{Slug: "g-info", Type: "info"},
		{Slug: "g-solo", Type: "build", Solo: true},
	} {
		brief := zeusBrief(j)
		for _, want := range []string{"OWN job's slug", "scratch job fixture", "decapitates"} {
			if !strings.Contains(brief, want) {
				t.Errorf("%s (solo=%v) brief missing %q", j.Type, j.Solo, want)
			}
		}
	}
}

func TestZeusBriefUniquePerJob(t *testing.T) {
	a := zeusBrief(&Job{Slug: "a", Type: "build"})
	b := zeusBrief(&Job{Slug: "b", Type: "build"})
	if a == b {
		t.Error("briefs must be unique per job")
	}
}
