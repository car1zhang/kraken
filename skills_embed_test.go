package main

import (
	"io/fs"
	"strings"
	"testing"

	"kraken/jobs"
)

// skills_embed_test.go verifies the go:embed directive actually captured
// the four kraken-owned skills and that each SKILL.md's renamed frontmatter
// took: this can only be checked from package main, since the embed.FS
// itself (skillsFS) lives here, not in jobs (jobs only sees the fs.Sub'd
// jobs.SkillsFS assigned by init()).
func TestSkillsFSContainsExpectedSkills(t *testing.T) {
	want := map[string]string{
		"info": "info",
		"spec": "spec",
		"do":   "do",
		"pr":   "pr",
	}
	for dir, wantName := range want {
		data, err := fs.ReadFile(skillsFS, ".claude/skills/"+dir+"/SKILL.md")
		if err != nil {
			t.Fatalf("read embedded .claude/skills/%s/SKILL.md: %v", dir, err)
		}
		nameLine := ""
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "name:") {
				nameLine = strings.TrimSpace(strings.TrimPrefix(line, "name:"))
				break
			}
		}
		if nameLine != wantName {
			t.Errorf(".claude/skills/%s/SKILL.md frontmatter name = %q, want %q", dir, nameLine, wantName)
		}
	}
}

// TestCrewPathsPinnedToEmbeddedTree guards against the exact failure the
// reviewer flagged: a rename of an embedded skill asset passes CI (nothing
// else references the literal path) but silently breaks every doc job,
// since jobs.HermesContractPath/LintSvgPath are plain string builders with
// no compile-time link to the embedded tree. Asserting the paths they
// return actually exist in jobs.SkillsFS pins the brief's paths to reality.
func TestCrewPathsPinnedToEmbeddedTree(t *testing.T) {
	for _, path := range []string{jobs.HermesContractPath(), jobs.LintSvgPath()} {
		rel := strings.TrimPrefix(path, ".claude/skills/")
		if _, err := fs.ReadFile(jobs.SkillsFS, rel); err != nil {
			t.Errorf("jobs.SkillsFS missing %q (from %s): %v", rel, path, err)
		}
	}
}

func TestJobsSkillsFSWiredByInit(t *testing.T) {
	// init() must have already run by the time tests execute; jobs.SkillsFS
	// should be a non-nil sub-FS rooted at .claude/skills (each top-level
	// entry a skill dir directly), not the raw embed.FS (whose top-level
	// entry is ".claude").
	if jobs.SkillsFS == nil {
		t.Fatal("jobs.SkillsFS is nil — init() did not wire it")
	}
	entries, err := fs.ReadDir(jobs.SkillsFS, ".")
	if err != nil {
		t.Fatalf("read jobs.SkillsFS root: %v", err)
	}
	names := map[string]bool{}
	for _, e := range entries {
		names[e.Name()] = true
	}
	for _, want := range []string{"info", "spec", "do", "pr"} {
		if !names[want] {
			t.Errorf("jobs.SkillsFS root missing skill dir %q, got %v", want, names)
		}
	}
}
