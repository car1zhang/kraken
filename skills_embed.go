package main

import (
	"embed"
	"io/fs"

	"kraken/jobs"
)

// skillsFS embeds kraken's kraken-owned skill bundle (info, aim, do, pr) so
// every cell gets them without a runtime dependency on carl's personal
// ~/.claude/skills bank. This lives at repo root rather than in jobs/
// because go:embed can't reach above its own package directory, .claude/ is
// itself at repo root, and jobs cannot import main (the util ← core ← jobs
// ← api ← web dependency chain runs the other way) — so root embeds the
// tree and injects it into jobs.SkillsFS via init() below.
//
//go:embed .claude/skills
var skillsFS embed.FS

func init() {
	sub, err := fs.Sub(skillsFS, ".claude/skills")
	if err != nil {
		// Embedding is compile-time and fixed at ".claude/skills" above; a
		// failure here means the embed itself is broken, which build would
		// already have caught. Leave jobs.SkillsFS nil rather than panic —
		// installSkills treats nil as a no-op.
		return
	}
	jobs.SkillsFS = sub
}
