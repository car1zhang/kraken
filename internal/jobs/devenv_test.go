package jobs

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func devEnvMkdirs(t *testing.T, root string, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func devEnvWrite(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFindVenvDirs(t *testing.T) {
	repo := t.TempDir()
	devEnvMkdirs(t, repo,
		".venv",                   // root venv, depth 1
		"agent-service/.venv",     // depth 2
		"agent-service/sub/.venv", // depth 3 — the deepest real monolith shape
		"a/b/c/.venv",             // depth 4 — at the cap, still included
		"a/b/c/d/.venv",           // depth 5 — beyond the cap, pruned
		"node_modules/.venv",      // pruned dir
		".git/.venv",              // pruned dir
		".hidden/.venv",           // dot-dirs other than .venv are pruned
		".venv/nested/.venv",      // a found venv is never descended into
		"agent-service/src",       // ordinary dir, no venv
		"linked",                  // holds the symlinked .venv below
	)
	// a FILE named .venv is not a venv dir
	devEnvWrite(t, repo, "other/.venv", "not a dir")
	// nor is a SYMLINK named .venv (WalkDir does not follow symlinks) — a
	// real shape: monolith's own .gitignore documents api-service/.venv can
	// be a symlink to a shared venv
	if err := os.Symlink(filepath.Join(repo, ".venv"), filepath.Join(repo, "linked", ".venv")); err != nil {
		t.Fatal(err)
	}

	got := findVenvDirs(repo)
	sort.Strings(got)
	want := []string{".venv", "a/b/c/.venv", "agent-service/.venv", "agent-service/sub/.venv"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("findVenvDirs = %v, want %v", got, want)
	}
}

func TestMirrorEntryFile(t *testing.T) {
	repo, wt := t.TempDir(), t.TempDir()
	devEnvWrite(t, repo, "pyrightconfig.json", "{}")

	ok, err := mirrorEntry(repo, wt, "pyrightconfig.json")
	if err != nil || !ok {
		t.Fatalf("mirrorEntry = %v, %v; want true, nil", ok, err)
	}
	dst := filepath.Join(wt, "pyrightconfig.json")
	target, err := os.Readlink(dst)
	if err != nil {
		t.Fatalf("expected symlink at %s: %v", dst, err)
	}
	if want := filepath.Join(repo, "pyrightconfig.json"); target != want {
		t.Errorf("symlink target = %s, want %s", target, want)
	}
}

// A directory candidate must become a REAL directory holding symlinks, never
// a symlink to the directory: dir-only ignore patterns (`.venv/`) do not
// match a symlink, which would leave it visible as untracked.
func TestMirrorEntryDirIsRealDirOfSymlinks(t *testing.T) {
	repo, wt := t.TempDir(), t.TempDir()
	devEnvMkdirs(t, repo, "svc/.venv/bin")
	devEnvWrite(t, repo, "svc/.venv/pyvenv.cfg", "home = /usr/bin")
	devEnvMkdirs(t, wt, "svc")

	ok, err := mirrorEntry(repo, wt, "svc/.venv")
	if err != nil || !ok {
		t.Fatalf("mirrorEntry = %v, %v; want true, nil", ok, err)
	}
	dst := filepath.Join(wt, "svc", ".venv")
	fi, err := os.Lstat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !fi.IsDir() {
		t.Fatalf("%s must be a real directory, got mode %v", dst, fi.Mode())
	}
	for _, entry := range []string{"bin", "pyvenv.cfg"} {
		target, err := os.Readlink(filepath.Join(dst, entry))
		if err != nil {
			t.Fatalf("expected symlink at %s/%s: %v", dst, entry, err)
		}
		if want := filepath.Join(repo, "svc", ".venv", entry); target != want {
			t.Errorf("%s target = %s, want %s", entry, target, want)
		}
	}
}

func TestMirrorEntrySkips(t *testing.T) {
	repo, wt := t.TempDir(), t.TempDir()

	// source absent: nothing to mirror, not an error
	if ok, err := mirrorEntry(repo, wt, "pyrightconfig.json"); ok || err != nil {
		t.Errorf("absent source: got %v, %v; want false, nil", ok, err)
	}

	// destination exists (a tracked copy): left alone
	devEnvWrite(t, repo, "pyrightconfig.json", "{}")
	devEnvWrite(t, wt, "pyrightconfig.json", "tracked copy")
	if ok, err := mirrorEntry(repo, wt, "pyrightconfig.json"); ok || err != nil {
		t.Errorf("existing dest: got %v, %v; want false, nil", ok, err)
	}
	b, _ := os.ReadFile(filepath.Join(wt, "pyrightconfig.json"))
	if string(b) != "tracked copy" {
		t.Errorf("existing dest was overwritten: %q", b)
	}

	// destination parent missing from the worktree (an untracked dir in the
	// repo): creating it would itself be untracked noise, so skip
	devEnvMkdirs(t, repo, "tmp/.venv")
	if ok, err := mirrorEntry(repo, wt, "tmp/.venv"); ok || err != nil {
		t.Errorf("missing parent: got %v, %v; want false, nil", ok, err)
	}
	if _, err := os.Lstat(filepath.Join(wt, "tmp")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing parent was created")
	}
}

// The parse the whole safety property rides on: `XY path` NUL-terminated
// records, trailing slash trimmed from untracked dirs, a rename record's
// second field (the original path, no `XY ` prefix) dropped by the rec[2]
// guard, short/empty records dropped.
func TestParseStatusPaths(t *testing.T) {
	// the final rename's second field has a space third byte, so it survives
	// the guard mangled ("cd/.venv") — pinned because the fail-safe direction
	// (a spurious ADDITION to the dirty list) is what makes the guard's miss
	// acceptable, and prose alone could be invalidated silently
	out := "?? .venv/\x00 M svc/x\x00?? pyrightconfig.json\x00R  new.txt\x00original.txt\x00x\x00R  .vscode\x00ab cd/.venv\x00"
	got := parseStatusPaths(out)
	want := []string{".venv", "svc/x", "pyrightconfig.json", "new.txt", ".vscode", "cd/.venv"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseStatusPaths = %v, want %v", got, want)
	}
	if got := parseStatusPaths(""); got != nil {
		t.Errorf("empty output should parse to nil, got %v", got)
	}
}

// A failed symlink must not report created: created is exactly the list
// pruneUnignored is licensed to RemoveAll, so it may only ever hold paths
// this run actually made.
func TestMirrorEntryFailedSymlinkNotCreated(t *testing.T) {
	repo, wt := t.TempDir(), t.TempDir()
	devEnvWrite(t, repo, "pyrightconfig.json", "{}")
	if err := os.Chmod(wt, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(wt, 0o755) })

	ok, err := mirrorEntry(repo, wt, "pyrightconfig.json")
	if ok || err == nil {
		t.Errorf("read-only dest dir: got %v, %v; want false with an error", ok, err)
	}
}

// An empty source dir is skipped: git never reports an empty dir, so the
// status arbiter cannot see one, and a mirrored empty .venv would block
// re-mirroring forever once the real source fills in.
func TestMirrorEntrySkipsEmptySourceDir(t *testing.T) {
	repo, wt := t.TempDir(), t.TempDir()
	devEnvMkdirs(t, repo, ".venv")

	if ok, err := mirrorEntry(repo, wt, ".venv"); ok || err != nil {
		t.Errorf("empty source dir: got %v, %v; want false, nil", ok, err)
	}
	if _, err := os.Lstat(filepath.Join(wt, ".venv")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("empty source dir was mirrored anyway")
	}
}

// swapGitUntracked stubs the git status probe for one test.
func swapGitUntracked(t *testing.T, fn func(string, []string) ([]string, error)) {
	t.Helper()
	old := gitUntracked
	gitUntracked = fn
	t.Cleanup(func() { gitUntracked = old })
}

func devEnvFixture(t *testing.T) (repo, wt string) {
	t.Helper()
	repo, wt = t.TempDir(), t.TempDir()
	devEnvMkdirs(t, repo, ".venv/bin", "svc/.venv/bin", ".vscode")
	devEnvWrite(t, repo, ".vscode/settings.json", "{}")
	devEnvWrite(t, repo, "pyrightconfig.json", "{}")
	devEnvMkdirs(t, wt, "svc")
	return repo, wt
}

func TestLinkDevEnvKeepsIgnoredLinks(t *testing.T) {
	repo, wt := devEnvFixture(t)
	var probed []string
	swapGitUntracked(t, func(_ string, rels []string) ([]string, error) {
		probed = append([]string{}, rels...)
		return nil, nil // git reports nothing: everything is ignored
	})

	linkDevEnv(repo, wt, &Job{Slug: "t"})

	sort.Strings(probed)
	want := []string{".venv", ".vscode", "pyrightconfig.json", "svc/.venv"}
	if !reflect.DeepEqual(probed, want) {
		t.Errorf("verified paths = %v, want %v", probed, want)
	}
	for _, rel := range want {
		if _, err := os.Lstat(filepath.Join(wt, rel)); err != nil {
			t.Errorf("%s missing after link: %v", rel, err)
		}
	}
}

func TestLinkDevEnvRemovesUnignoredLinks(t *testing.T) {
	repo, wt := devEnvFixture(t)
	swapGitUntracked(t, func(_ string, rels []string) ([]string, error) {
		// git shows the root venv's dir (trailing slash already trimmed by
		// the real prober) and a file inside .vscode: both candidates must go
		return []string{".venv", ".vscode/settings.json"}, nil
	})

	linkDevEnv(repo, wt, &Job{Slug: "t"})

	for _, rel := range []string{".venv", ".vscode"} {
		if _, err := os.Lstat(filepath.Join(wt, rel)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s should have been removed as not-ignored", rel)
		}
	}
	for _, rel := range []string{"pyrightconfig.json", "svc/.venv"} {
		if _, err := os.Lstat(filepath.Join(wt, rel)); err != nil {
			t.Errorf("%s should have been kept: %v", rel, err)
		}
	}
}

// An unverifiable link is a sweep hazard: a git failure removes everything.
func TestLinkDevEnvRemovesAllOnGitError(t *testing.T) {
	repo, wt := devEnvFixture(t)
	swapGitUntracked(t, func(string, []string) ([]string, error) {
		return nil, errors.New("git exploded")
	})

	linkDevEnv(repo, wt, &Job{Slug: "t"})

	for _, rel := range []string{".venv", ".vscode", "pyrightconfig.json", "svc/.venv"} {
		if _, err := os.Lstat(filepath.Join(wt, rel)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s should have been removed after git error", rel)
		}
	}
}

// Reactivation's resume path calls linkDevEnv on a worktree that may already
// carry links from an earlier run: the second pass must create nothing and
// touch nothing.
func TestLinkDevEnvIdempotent(t *testing.T) {
	repo, wt := devEnvFixture(t)
	swapGitUntracked(t, func(string, []string) ([]string, error) { return nil, nil })
	linkDevEnv(repo, wt, &Job{Slug: "t"})

	calls := 0
	swapGitUntracked(t, func(string, []string) ([]string, error) {
		calls++
		return nil, nil
	})
	linkDevEnv(repo, wt, &Job{Slug: "t"})
	if calls != 0 {
		t.Errorf("second linkDevEnv created %d-path verify call; want none (nothing new to create)", calls)
	}
}
