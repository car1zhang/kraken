package jobs

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func pruneTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-C", dir, "-c", "user.email=t@t", "-c", "user.name=t", "-c", "commit.gpgsign=false"}, args...)
	out, err := exec.Command("git", full...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// A worktree directory removed with os.RemoveAll (never `git worktree
// remove`) leaves git believing its branch is still checked out there, and
// the next `git worktree add` on that branch fails with "is already used by
// worktree at". pruneStaleWorktrees must clear that registration so the add
// succeeds, and must leave a live worktree alone.
func TestPruneStaleWorktreesFreesTheBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	// git prints worktree paths with symlinks resolved (/private/var on
	// macOS for a /var temp dir), so compare against the resolved root
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	pruneTestGit(t, repo, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pruneTestGit(t, repo, "add", "f.txt")
	pruneTestGit(t, repo, "commit", "-q", "-m", "init")

	gone := filepath.Join(root, "cell", "wt-repo")
	pruneTestGit(t, repo, "worktree", "add", "-q", gone, "-b", "carl/demo-7", "main")
	live := filepath.Join(root, "live", "wt-repo")
	pruneTestGit(t, repo, "worktree", "add", "-q", live, "-b", "carl/other-8", "main")

	// the measured shape: the directory vanishes, the registration stays
	if err := os.RemoveAll(filepath.Join(root, "cell")); err != nil {
		t.Fatal(err)
	}
	again := filepath.Join(root, "cell2", "wt-repo")
	if out, err := exec.Command("git", "-C", repo, "worktree", "add", again, "-B", "carl/demo-7", "main").CombinedOutput(); err == nil {
		t.Fatalf("precondition: expected the stale registration to refuse the add, got success:\n%s", out)
	}

	pruneStaleWorktrees(repo)

	if out, err := exec.Command("git", "-C", repo, "worktree", "add", again, "-B", "carl/demo-7", "main").CombinedOutput(); err != nil {
		t.Fatalf("worktree add after prune: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(live, "f.txt")); err != nil {
		t.Fatalf("live worktree must survive the prune: %v", err)
	}
	list := pruneTestGit(t, repo, "worktree", "list", "--porcelain")
	for _, want := range []string{live, again} {
		if !strings.Contains(list, "worktree "+want) {
			t.Fatalf("worktree list missing %s:\n%s", want, list)
		}
	}
	if strings.Contains(list, "worktree "+gone) {
		t.Fatalf("stale registration survived the prune:\n%s", list)
	}
}
