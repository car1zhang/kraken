package kraken

import (
	"io/fs"
	"strings"
	"testing"

	"kraken/internal/jobs"
)

// skills_embed_test.go verifies the go:embed directive actually captured
// the four kraken-owned skills and that each SKILL.md's renamed frontmatter
// took: this can only be checked from package main, since the embed.FS
// itself (skillsFS) lives here, not in jobs (jobs only sees the fs.Sub'd
// jobs.SkillsFS assigned by init()). It also pins the three worktree-relative
// path helpers zeus' brief depends on — jobs.HermesContractPath,
// jobs.LintSvgPath, and jobs.DiagramCardPath — to real files inside that
// embedded tree, and pins the diagram-card.html template's own placeholder
// counts and header-comment shape.
func TestSkillsFSContainsExpectedSkills(t *testing.T) {
	want := map[string]string{
		"info": "info",
		"aim":  "aim",
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
// since jobs.HermesContractPath/LintSvgPath/DiagramCardPath are plain string
// builders with no compile-time link to the embedded tree. Asserting the
// paths they return actually exist in jobs.SkillsFS pins the brief's paths
// to reality.
func TestCrewPathsPinnedToEmbeddedTree(t *testing.T) {
	for _, path := range []string{jobs.HermesContractPath(), jobs.LintSvgPath(), jobs.DiagramCardPath()} {
		rel := strings.TrimPrefix(path, ".claude/skills/")
		if _, err := fs.ReadFile(jobs.SkillsFS, rel); err != nil {
			t.Errorf("jobs.SkillsFS missing %q (from %s): %v", rel, path, err)
		}
	}
}

// TestDiagramCardTemplateHeaderDoesNotSelfTerminate guards against the exact
// bug a prior review round caught: the diagram-card.html header comment used
// to embed the literal "<!-- DIAGRAM: {{DIAGRAM_ID}} -->" placeholder (and
// other stray "-->" sequences) inside its own explanatory comment. The first
// "-->" a browser (or a naive substring scan) sees closes the OUTER comment
// early, so everything between that point and the comment's real close
// renders as visible body text in every single card. Asserting exactly one
// DIAGRAM placeholder in the whole embedded file, and exactly one "-->"
// before <style> (the comment's own, real close), pins the fix.
func TestDiagramCardTemplateHeaderDoesNotSelfTerminate(t *testing.T) {
	data, err := fs.ReadFile(jobs.SkillsFS, "aim/assets/diagram-card.html")
	if err != nil {
		t.Fatalf("read embedded aim/assets/diagram-card.html: %v", err)
	}
	content := string(data)
	if n := strings.Count(content, "<!-- DIAGRAM: {{DIAGRAM_ID}} -->"); n != 1 {
		t.Errorf("diagram-card.html has %d occurrences of the DIAGRAM placeholder comment, want exactly 1", n)
	}
	if n := strings.Count(content, "{{CARD_TITLE}}"); n != 2 {
		t.Errorf("diagram-card.html has %d occurrences of {{CARD_TITLE}}, want exactly 2 (title + h1)", n)
	}
	if n := strings.Count(content, "{{CAPTION}}"); n != 1 {
		t.Errorf("diagram-card.html has %d occurrences of {{CAPTION}}, want exactly 1 (figcaption)", n)
	}
	styleIdx := strings.Index(content, "<style>")
	if styleIdx < 0 {
		t.Fatal("diagram-card.html missing <style> block")
	}
	head := content[:styleIdx]
	if n := strings.Count(head, "-->"); n != 1 {
		t.Errorf("diagram-card.html header (before <style>) contains %d occurrences of \"-->\", want exactly 1 (the header comment's own real close) — a second occurrence means the comment self-terminates early and everything after renders as visible body text", n)
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
	for _, want := range []string{"info", "aim", "do", "pr"} {
		if !names[want] {
			t.Errorf("jobs.SkillsFS root missing skill dir %q, got %v", want, names)
		}
	}
}
