package main

import (
	"strings"
	"testing"
)

// crew_test.go — zeus' brief content matrix. There is no hermes pane agent
// and no escalation-file relay any more: hermes is only a per-diagram
// subagent name inside zeus' own brief. These tests assert that contract
// across the build/info workflows and the solo/auto-approve flags, in the
// same string-contains style as the rest of the job tests.

func TestZeusBriefBuild(t *testing.T) {
	j := &Job{Slug: "x", Type: "build", Success: "tests pass"}
	brief := zeusBrief(j)
	for _, want := range []string{
		"zeus", "hephaestus", "athena", "/send", "PR", "manual test ready",
		"tests pass", "AskUserQuestion", cellArtifacts(j), "TaskStop",
	} {
		if !strings.Contains(brief, want) {
			t.Errorf("build brief missing %q", want)
		}
	}
	for _, unwanted := range []string{"plan.md", "done.md", "hermes pane", "relay"} {
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
	j := &Job{Slug: "y", Type: "info", Success: "explains the system"}
	brief := zeusBrief(j)
	for _, want := range []string{"zeus", "/info", "No PR", "AskUserQuestion", cellArtifacts(j), "TaskStop"} {
		if !strings.Contains(brief, want) {
			t.Errorf("info brief missing %q", want)
		}
	}
	for _, unwanted := range []string{"plan.md", "done.md", "open a PR with gh", "hermes pane", "relay"} {
		if strings.Contains(brief, unwanted) {
			t.Errorf("info brief still mentions %q", unwanted)
		}
	}
	if strings.Contains(brief, "escalation") && !strings.Contains(brief, "no escalation file") {
		t.Error("info brief mentions escalation without disclaiming it")
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
	}
}

func TestZeusBriefAutoApprove(t *testing.T) {
	gated := zeusBrief(&Job{Slug: "w", Type: "build"})
	auto := zeusBrief(&Job{Slug: "w", Type: "build", AutoApprove: true})
	if !strings.Contains(auto, "without stopping") {
		t.Error("auto-approve brief missing the proceed-without-stopping sentence")
	}
	if strings.Contains(gated, "without stopping") {
		t.Error("non-auto-approve brief should not proceed without stopping")
	}
	if auto == gated {
		t.Error("auto-approve and gated briefs must differ")
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

func TestZeusBriefUniquePerJob(t *testing.T) {
	a := zeusBrief(&Job{Slug: "a", Type: "build"})
	b := zeusBrief(&Job{Slug: "b", Type: "build"})
	if a == b {
		t.Error("briefs must be unique per job")
	}
}
