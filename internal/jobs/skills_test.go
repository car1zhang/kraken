package jobs

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

// skills_test.go — pure tests of installSkills against an in-memory
// fstest.MapFS standing in for the embedded skill bundle, so nothing here
// shells out or touches the real ~/.claude/skills bank. The one exception
// exercised is isTracked, which the package exposes as a func var precisely
// so this file can stub it instead of shelling out to git.

func testSkillsFS() fstest.MapFS {
	return fstest.MapFS{
		"info/SKILL.md":                  {Data: []byte("---\nname: info\n---\ninfo body")},
		"aim/SKILL.md":                   {Data: []byte("---\nname: aim\n---\naim body")},
		"aim/assets/aim.html":            {Data: []byte("<html>aim template</html>")},
		"aim/assets/lint-fixtures/x.svg": {Data: []byte("<svg>fixture</svg>")},
		"do/SKILL.md":                    {Data: []byte("---\nname: do\n---\ndo body")},
		"pr/SKILL.md":                    {Data: []byte("---\nname: pr\n---\npr body")},
		"dream/SKILL.md":                 {Data: []byte("---\nname: dream\n---\ndream body")},
	}
}

// alwaysUntracked is the isTracked stub every pre-existing test installs so
// installSkills exercises its normal replace path without shelling out to
// git (which would fail anyway, since t.TempDir() dirs aren't git repos, but
// this keeps the suite's no-shell-out convention explicit).
func alwaysUntracked(string, string) bool { return false }

// withSkillsFS takes fs.FS (not fstest.MapFS) so callers can pass a true
// untyped nil — a nil fstest.MapFS boxed into fs.FS is a non-nil interface
// with a nil underlying value, which would make installSkills' `SkillsFS ==
// nil` guard false and silently skip the no-op path this test suite means
// to exercise.
func withSkillsFS(t *testing.T, fsys fs.FS) {
	t.Helper()
	prev := SkillsFS
	SkillsFS = fsys
	t.Cleanup(func() { SkillsFS = prev })
}

// withTrackedStub stubs isTracked for the duration of the test so the
// skip-if-tracked guard in installSkills can be exercised without shelling
// out to git.
func withTrackedStub(t *testing.T, tracked func(wt, name string) bool) {
	t.Helper()
	prev := isTracked
	isTracked = tracked
	t.Cleanup(func() { isTracked = prev })
}

func skillsReadFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// --- full-tree materialization ---

func TestInstallSkillsMaterializesFullTree(t *testing.T) {
	withSkillsFS(t, testSkillsFS())
	withTrackedStub(t, alwaysUntracked)
	wt := t.TempDir()
	j := &Job{Slug: "demo-job"}

	installSkills(wt, j)

	for path, want := range map[string]string{
		"info/SKILL.md":       "info body",
		"aim/SKILL.md":        "aim body",
		"aim/assets/aim.html": "aim template",
		"do/SKILL.md":         "do body",
		"pr/SKILL.md":         "pr body",
	} {
		full := filepath.Join(wt, ".claude", "skills", path)
		got := skillsReadFile(t, full)
		if !strings.Contains(got, want) {
			t.Errorf("%s: got %q, want it to contain %q", full, got, want)
		}
	}
}

// --- idempotence ---

func TestInstallSkillsIdempotent(t *testing.T) {
	withSkillsFS(t, testSkillsFS())
	withTrackedStub(t, alwaysUntracked)
	wt := t.TempDir()
	j := &Job{Slug: "demo-job"}

	installSkills(wt, j)
	first := skillsReadFile(t, filepath.Join(wt, ".claude", "skills", "aim", "SKILL.md"))
	installSkills(wt, j)
	second := skillsReadFile(t, filepath.Join(wt, ".claude", "skills", "aim", "SKILL.md"))

	if first != second {
		t.Errorf("re-running installSkills changed aim/SKILL.md: %q vs %q", first, second)
	}
	// still exactly one copy of the asset file, not duplicated/nested
	full := filepath.Join(wt, ".claude", "skills", "aim", "assets", "aim.html")
	if _, err := os.Stat(full); err != nil {
		t.Errorf("expected %s to exist after second install: %v", full, err)
	}
}

// --- foreign skill dir preserved ---

func TestInstallSkillsPreservesForeignSkillDir(t *testing.T) {
	withSkillsFS(t, testSkillsFS())
	withTrackedStub(t, alwaysUntracked)
	wt := t.TempDir()
	j := &Job{Slug: "demo-job"}

	foreignDir := filepath.Join(wt, ".claude", "skills", "project-only")
	if err := os.MkdirAll(foreignDir, 0o755); err != nil {
		t.Fatalf("mkdir foreign skill dir: %v", err)
	}
	foreignFile := filepath.Join(foreignDir, "SKILL.md")
	if err := os.WriteFile(foreignFile, []byte("project-owned skill"), 0o644); err != nil {
		t.Fatalf("write foreign skill file: %v", err)
	}

	installSkills(wt, j)

	got := skillsReadFile(t, foreignFile)
	if got != "project-owned skill" {
		t.Errorf("foreign skill dir was touched: got %q", got)
	}
}

// --- stale kraken-owned dir contents replaced ---

func TestInstallSkillsReplacesStaleContents(t *testing.T) {
	withSkillsFS(t, testSkillsFS())
	withTrackedStub(t, alwaysUntracked)
	wt := t.TempDir()
	j := &Job{Slug: "demo-job"}

	// simulate a previous install that had an extra/renamed file no longer
	// present in the current embedded bundle
	staleDir := filepath.Join(wt, ".claude", "skills", "aim")
	if err := os.MkdirAll(staleDir, 0o755); err != nil {
		t.Fatalf("mkdir stale aim dir: %v", err)
	}
	staleFile := filepath.Join(staleDir, "stale-leftover.md")
	if err := os.WriteFile(staleFile, []byte("leftover from an old skill version"), 0o644); err != nil {
		t.Fatalf("write stale file: %v", err)
	}

	installSkills(wt, j)

	if _, err := os.Stat(staleFile); !os.IsNotExist(err) {
		t.Errorf("expected stale file %s to be gone after install, stat err = %v", staleFile, err)
	}
	// and the current bundle's file is present
	full := filepath.Join(wt, ".claude", "skills", "aim", "SKILL.md")
	if _, err := os.Stat(full); err != nil {
		t.Errorf("expected %s to exist after install: %v", full, err)
	}
}

// --- nil FS no-op ---

func TestInstallSkillsNilFSNoOp(t *testing.T) {
	withSkillsFS(t, nil)
	wt := t.TempDir()
	j := &Job{Slug: "demo-job"}

	installSkills(wt, j)

	skillsDir := filepath.Join(wt, ".claude", "skills")
	if _, err := os.Stat(skillsDir); !os.IsNotExist(err) {
		t.Errorf("expected %s to not exist with nil SkillsFS, stat err = %v", skillsDir, err)
	}
}

// --- stray non-dir entry at the embedded FS root is skipped, not treated as a skill ---

func TestInstallSkillsSkipsNonDirRootEntry(t *testing.T) {
	fsys := testSkillsFS()
	fsys["README.md"] = &fstest.MapFile{Data: []byte("not a skill")}
	withSkillsFS(t, fsys)
	withTrackedStub(t, alwaysUntracked)
	wt := t.TempDir()
	j := &Job{Slug: "demo-job"}

	installSkills(wt, j)

	if _, err := os.Stat(filepath.Join(wt, ".claude", "skills", "README.md")); !os.IsNotExist(err) {
		t.Errorf("expected root-level non-dir entry README.md not to be materialized as a skill, stat err = %v", err)
	}
	// the real skills still installed fine alongside the stray file
	full := filepath.Join(wt, ".claude", "skills", "aim", "SKILL.md")
	if _, err := os.Stat(full); err != nil {
		t.Errorf("expected %s to exist despite stray root entry: %v", full, err)
	}
}

// --- a stray file directly under <wt>/.claude/skills/ (not inside any skill dir) survives ---

func TestInstallSkillsPreservesStrayFileUnderSkillsRoot(t *testing.T) {
	withSkillsFS(t, testSkillsFS())
	withTrackedStub(t, alwaysUntracked)
	wt := t.TempDir()
	j := &Job{Slug: "demo-job"}

	skillsRoot := filepath.Join(wt, ".claude", "skills")
	if err := os.MkdirAll(skillsRoot, 0o755); err != nil {
		t.Fatalf("mkdir skills root: %v", err)
	}
	strayFile := filepath.Join(skillsRoot, "NOTES.md")
	if err := os.WriteFile(strayFile, []byte("carl's scratch notes"), 0o644); err != nil {
		t.Fatalf("write stray file: %v", err)
	}

	installSkills(wt, j)

	got := skillsReadFile(t, strayFile)
	if got != "carl's scratch notes" {
		t.Errorf("stray file directly under .claude/skills/ was touched: got %q", got)
	}
}

// --- skip-if-tracked guard ---

func TestInstallSkillsSkipsTrackedSkillDir(t *testing.T) {
	withSkillsFS(t, testSkillsFS())
	wt := t.TempDir()
	j := &Job{Slug: "demo-job"}

	// simulate the target repo's own checkout having aim/ tracked in git
	// (a kraken-targeted cell working on kraken's own repo, where the
	// checked-out branch's aim/SKILL.md is fresher truth than this
	// possibly-lagging binary's embed) — everything else stays untracked.
	withTrackedStub(t, func(_, name string) bool { return name == "aim" })

	// pre-seed aim/ with content standing in for the branch's own committed
	// version, so we can assert installSkills left it alone
	aimDir := filepath.Join(wt, ".claude", "skills", "aim")
	if err := os.MkdirAll(aimDir, 0o755); err != nil {
		t.Fatalf("mkdir aim dir: %v", err)
	}
	trackedFile := filepath.Join(aimDir, "SKILL.md")
	if err := os.WriteFile(trackedFile, []byte("branch-committed aim content"), 0o644); err != nil {
		t.Fatalf("write tracked aim file: %v", err)
	}

	installSkills(wt, j)

	if got := skillsReadFile(t, trackedFile); got != "branch-committed aim content" {
		t.Errorf("tracked aim/SKILL.md was overwritten: got %q", got)
	}
	// untracked skills still install normally
	full := filepath.Join(wt, ".claude", "skills", "do", "SKILL.md")
	if _, err := os.Stat(full); err != nil {
		t.Errorf("expected untracked skill %s to still install: %v", full, err)
	}
}
