package jobs

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
)

// SkillsFS holds kraken's kraken-owned skill bundle — the five pipeline skills
// (info, aim, do, dream, mind), each governing a phase (do's last step is the PR;
// the pr skill folded into it in job 624), plus three that
// govern none: load, a reference consulted mid-phase; handoff, typed into
// a pane by the operator or by the daemon's auto-handoff pass to compact an agent
// behind a written record; and direct, the one-agent override a quick job's
// spec opens with (or the operator types) that makes zeus run aim/info/do
// itself with no crew — plus humanizer, vendored unchanged from
// github.com/blader/humanizer, the edit pass zeus' brief applies to every
// operator-facing text — embedded into the binary at build time. Root's
// skills_embed.go assigns this during init(), sub-FS'd to the .claude/skills
// root so each top-level entry here is one skill's own directory. Left nil
// when the jobs package is exercised without root's init running (e.g. unit
// tests of other packages) — installSkills treats a nil SkillsFS as a silent
// no-op.
var SkillsFS fs.FS

// isTracked reports whether <wt>/.claude/skills/<name> is tracked by git in
// the target repo's checkout — i.e. the target repo itself ships a skill
// under that name, committed on the branch this cell checked out. A var so
// tests can stub it (the real implementation shells out to git, which the
// package's tests otherwise avoid entirely).
var isTracked = func(wt, name string) bool {
	rel := filepath.Join(".claude", "skills", name)
	cmd := exec.Command("git", "-C", wt, "ls-files", "--error-unmatch", rel)
	return cmd.Run() == nil
}

// installSkills materializes kraken's kraken-owned skills into
// <wt>/.claude/skills, giving them precedence over any same-named skill a
// target repo happens to check in. Mirrors installHookSettings' contract
// exactly: called from StartCell once the worktree is up, and again from the
// reactivation recreate path; never fails the cell — every failure is a
// stderr warning plus an event-log entry, and it returns. For each skill
// name present in SkillsFS, the corresponding <wt>/.claude/skills/<name>/ is
// wholesale-replaced (removed, then written fresh from the embedded FS) —
// idempotent, and safe to call repeatedly — UNLESS that path is already
// tracked by git in the target repo's own checkout (isTracked), in which
// case the name is skipped silently but for one stderr note: a kraken-
// targeted cell (kraken working on its own repo) checks out a branch whose
// committed .claude/skills/<name> is fresher truth than whatever this
// possibly-lagging installed binary has embedded, and overwriting it would
// revert the branch's own in-progress skill edits. Any other name already
// under <wt>/.claude/skills/ (a target repo's own project skills, untracked
// or not the kraken bundle) is left completely untouched regardless.
func installSkills(wt string, j *Job) {
	if SkillsFS == nil {
		return
	}
	entries, err := fs.ReadDir(SkillsFS, ".")
	if err != nil {
		msg := fmt.Sprintf("skill install for %s: read embedded skills: %v", j.Slug, err)
		fmt.Fprintln(os.Stderr, "kraken: "+msg)
		dlogErr("%s", msg)
		return
	}
	root := filepath.Join(wt, ".claude", "skills")
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if isTracked(wt, name) {
			fmt.Fprintf(os.Stderr, "kraken: skill install for %s: %s is tracked in the target repo, leaving it alone\n", j.Slug, name)
			continue
		}
		sub, err := fs.Sub(SkillsFS, name)
		if err != nil {
			msg := fmt.Sprintf("skill install for %s: sub %s: %v", j.Slug, name, err)
			fmt.Fprintln(os.Stderr, "kraken: "+msg)
			dlogErr("%s", msg)
			continue
		}
		dst := filepath.Join(root, name)
		if err := os.RemoveAll(dst); err != nil {
			msg := fmt.Sprintf("skill install for %s: remove %s: %v", j.Slug, dst, err)
			fmt.Fprintln(os.Stderr, "kraken: "+msg)
			dlogErr("%s", msg)
			continue
		}
		if err := os.MkdirAll(dst, 0o755); err != nil {
			msg := fmt.Sprintf("skill install for %s: mkdir %s: %v", j.Slug, dst, err)
			fmt.Fprintln(os.Stderr, "kraken: "+msg)
			dlogErr("%s", msg)
			continue
		}
		if err := copyFSTree(sub, dst, []byte(Operator())); err != nil {
			msg := fmt.Sprintf("skill install for %s: write %s: %v", j.Slug, dst, err)
			fmt.Fprintln(os.Stderr, "kraken: "+msg)
			dlogErr("%s", msg)
		}
	}
}

// copyFSTree materializes every file under src (an fs.FS rooted at one
// skill's directory) into dst on disk, creating intermediate directories as
// needed. Files land at 0644 and dirs at 0755 — no execute bit on files,
// which is fine since skill assets (e.g. lint_svg.py) are invoked via
// `python3 <path>`, never exec'd directly. path comes from fs.WalkDir over
// an fs.FS, so it's always forward-slash-joined single path elements with no
// ".." segments; filepath.Join(dst, path) can't escape dst.
//
// Every file's literal operatorPlaceholder is replaced by operator on the way
// out: the embedded skills name the person kraken works for only through that
// placeholder, so the bundle carries no real name and each machine's cells
// read their own. It is ONE pass over the embedded bytes, so a name that
// itself contains "{{operator}}" (or any other brace or quote) lands
// literally and is never expanded again. The embedded copy is untouched.
func copyFSTree(src fs.FS, dst string, operator []byte) error {
	return fs.WalkDir(src, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(dst, path)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := fs.ReadFile(src, path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, bytes.ReplaceAll(data, operatorPlaceholder, operator), 0o644)
	})
}

// operatorPlaceholder is the token the embedded skills write where they name
// the operator; copyFSTree substitutes Operator() for it at materialization.
var operatorPlaceholder = []byte("{{operator}}")
