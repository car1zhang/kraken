// Package kraken is the module-root package and exists for exactly one
// reason: go:embed can't reach above its own package directory, and
// .claude/skills lives at repo root — so this is the only place the skill
// bundle can be embedded from. cmd/kraken blank-imports this package so the
// init() injection below runs in the binary.
package kraken

import (
	"embed"
	"io/fs"

	"kraken/jobs"
)

// skillsFS embeds kraken's kraken-owned skill bundle (info, aim, do, pr) so
// every cell gets them without a runtime dependency on carl's personal
// ~/.claude/skills bank. jobs cannot import this package (the util ← core ←
// jobs ← api ← web dependency chain runs the other way) — so root embeds
// the tree and injects it into jobs.SkillsFS via init() below.
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
