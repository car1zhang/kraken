package jobs

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// This file mirrors a repo checkout's gitignored dev environment — Python
// venvs and root-level editor config — into a cell's fresh worktree, so an
// editor opened on the worktree gets a working language server instead of a
// wall of missing-import errors. `git worktree add` copies only tracked
// files: monolith keeps `.venv` dirs beside each service and its
// `pyrightconfig.json`/`.vscode` are gitignored too, so a fresh worktree has
// neither the interpreter nor the config that names it, and pyright flags
// every third-party import. Symlinking the primary checkout's copies in
// fixes the LSP and, as a side effect, gives `poetry run`/pytest in the cell
// a real interpreter without a per-cell `poetry install`.
//
// Two safety rules shape the mechanics, both about `git add -A` sweeps (a
// recorded incident class in this setup — an untracked path left visible in
// `git status` eventually lands in someone's commit):
//
//   - A directory is mirrored as a REAL directory whose top-level entries are
//     symlinks, never as one symlink to the directory. Ignore patterns written
//     with a trailing slash (`.venv/`, monolith's actual pattern) match only
//     directories, and git does not consider a symlink a directory — measured:
//     a symlink named `.venv` shows as `?? .venv` while a real dir holding
//     symlinks is cleanly ignored.
//   - After creating the links, `git status` over exactly the created paths is
//     the arbiter: anything git reports (i.e. anything not ignored in this
//     repo) is removed again. The check is on status output rather than
//     check-ignore because status answers the actual hazard — "would a bulk
//     add sweep this?" — including pattern shapes check-ignore on the bare
//     path would misjudge.

// devEnvRootEntries are the root-level editor-config candidates mirrored
// beside the venvs. Both are gitignored-but-present in monolith (the repo
// this exists for); in a repo that tracks them they already exist in the
// worktree and mirrorEntry skips them.
var devEnvRootEntries = []string{"pyrightconfig.json", ".vscode"}

// maxVenvDepth bounds findVenvDirs' walk, in path components below the repo
// root (`agent-service/ai_chatbot_v2_service/.venv` is depth 3 — the deepest
// real venv observed outside the pruned dot-dirs — so 4 leaves headroom
// without walking dataset trees).
const maxVenvDepth = 4

// gitUntracked lists which of rels git reports as anything-but-clean in wt —
// for freshly created paths, that means untracked, i.e. not ignored. The
// explicit --untracked-files=normal keeps a status.showUntrackedFiles=no in
// anyone's git config from silently blinding the one check whose entire value
// is that it cannot be wrong. Package-level var so tests can stub the git
// call (rotationGate's pattern).
var gitUntracked = func(wt string, rels []string) ([]string, error) {
	args := append([]string{"-C", wt, "status", "--porcelain", "-z", "--untracked-files=normal", "--"}, rels...)
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		// Output() leaves stderr on the ExitError: surface it, because this
		// error's consequence is pruneUnignored removing everything created,
		// and "exit status 128" alone gives carl nothing to act on
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("git status: %v: %s", err, strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("git status: %v", err)
	}
	return parseStatusPaths(string(out)), nil
}

// parseStatusPaths pulls the paths out of `git status --porcelain -z` output:
// `XY path` records, NUL-terminated. Untracked dirs come back with a trailing
// slash; trim so callers compare plain rels. A rename/copy record carries a
// SECOND NUL-separated field (the original path) with no `XY ` prefix — the
// rec[2] guard drops it, and any field it happens to miss (an original path
// with a space third byte) only ever ADDS a mangled entry to the dirty list,
// which errs toward removing a link, never toward keeping an unignored one.
func parseStatusPaths(out string) []string {
	var dirty []string
	for _, rec := range strings.Split(out, "\x00") {
		if len(rec) < 4 || rec[2] != ' ' {
			continue
		}
		dirty = append(dirty, strings.TrimSuffix(rec[3:], "/"))
	}
	return dirty
}

// linkDevEnv is the entrypoint StartCell and ReactivateJob call once a
// worktree exists. Mirrors installSkills' contract: never fails the cell —
// every failure is a stderr warning plus an event-log entry — and it is
// idempotent, so the reactivation resume path (a surviving worktree that may
// predate this feature) calls it too.
func linkDevEnv(repo, wt string, j *Job) {
	created, err := mirrorDevEnv(repo, wt)
	if err != nil {
		msg := fmt.Sprintf("dev-env link for %s: %v", j.Slug, err)
		fmt.Fprintln(os.Stderr, "kraken: "+msg)
		dlogErr("%s", msg)
	}
	if len(created) == 0 {
		return
	}
	removed, err := pruneUnignored(wt, created)
	if err != nil {
		msg := fmt.Sprintf("dev-env link for %s: could not verify links are gitignored, removed all %d: %v", j.Slug, len(removed), err)
		fmt.Fprintln(os.Stderr, "kraken: "+msg)
		dlogErr("%s", msg)
		return
	}
	if len(removed) > 0 {
		msg := fmt.Sprintf("dev-env link for %s: removed %s — not gitignored in this repo, would show as untracked", j.Slug, strings.Join(removed, ", "))
		fmt.Fprintln(os.Stderr, "kraken: "+msg)
		dlogErr("%s", msg)
	}
}

// mirrorDevEnv creates the links for every candidate present in repo and
// absent in wt, returning the worktree-relative paths it created. A failing
// candidate is skipped, its error joined into the return — one unreadable
// venv must not cost the others.
func mirrorDevEnv(repo, wt string) ([]string, error) {
	rels := append(findVenvDirs(repo), devEnvRootEntries...)
	var created []string
	var errs []error
	for _, rel := range rels {
		ok, err := mirrorEntry(repo, wt, rel)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %v", rel, err))
		}
		if ok {
			created = append(created, rel)
		}
	}
	return created, errors.Join(errs...)
}

// findVenvDirs walks the checkout for directories named `.venv`, at most
// maxVenvDepth components deep. Dot-dirs other than `.venv` (which covers
// `.git`) and `node_modules` are pruned — a venv never lives under them, and
// node_modules is where the walk would otherwise spend its time; a found
// `.venv` is never descended into, and a `.venv` that is itself a symlink is
// skipped (WalkDir does not follow symlinks, and mirroring one would re-hit
// the symlink-is-not-a-directory ignore problem the header describes).
// Unreadable subtrees are skipped, not errors — this is a best-effort scan of
// a checkout other processes own.
func findVenvDirs(root string) []string {
	var found []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == root || !d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return fs.SkipDir
		}
		name := d.Name()
		if name == ".venv" {
			found = append(found, rel)
			return fs.SkipDir
		}
		if name == "node_modules" || strings.HasPrefix(name, ".") {
			return fs.SkipDir
		}
		if strings.Count(rel, string(filepath.Separator))+1 >= maxVenvDepth {
			return fs.SkipDir
		}
		return nil
	})
	return found
}

// mirrorEntry links one candidate into the worktree, reporting whether it
// created anything. Skips (false, nil) when the source cannot be stat'd
// (normally: absent), the destination already exists (a tracked copy, or an
// earlier run's link), or
// the destination's parent is missing from the worktree — a parent absent
// from a fresh worktree is itself untracked in the repo, and creating it
// here would be exactly the untracked noise the status check exists to
// prevent.
func mirrorEntry(repo, wt, rel string) (bool, error) {
	src := filepath.Join(repo, rel)
	fi, err := os.Lstat(src)
	if err != nil {
		return false, nil
	}
	dst := filepath.Join(wt, rel)
	if _, err := os.Lstat(dst); err == nil {
		return false, nil
	}
	if pfi, err := os.Lstat(filepath.Dir(dst)); err != nil || !pfi.IsDir() {
		return false, nil
	}
	if !fi.IsDir() {
		if err := os.Symlink(src, dst); err != nil {
			return false, err
		}
		return true, nil
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return false, err
	}
	if len(entries) == 0 {
		// an empty source (a venv caught mid-recreate): git never reports an
		// empty dir, so a mirrored one would survive the status arbiter and
		// then block re-mirroring forever once the source fills in
		return false, nil
	}
	if err := os.Mkdir(dst, 0o755); err != nil {
		return false, err
	}
	for _, e := range entries {
		if err := os.Symlink(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			// a half-mirrored venv (bin without lib) breaks pyright more
			// confusingly than no venv: take the whole entry back out
			_ = os.RemoveAll(dst)
			return false, err
		}
	}
	return true, nil
}

// pruneUnignored removes every created path git does not ignore, returning
// what it removed. An error from git itself removes EVERYTHING created — an
// unverifiable link is treated as a sweep hazard, since leaving it costs a
// possible commit of editor plumbing into a PR while removing it only costs
// the LSP nicety this file exists for.
func pruneUnignored(wt string, created []string) ([]string, error) {
	dirty, err := gitUntracked(wt, created)
	if err != nil {
		for _, rel := range created {
			_ = os.RemoveAll(filepath.Join(wt, rel))
		}
		return created, err
	}
	var removed []string
	for _, rel := range created {
		for _, d := range dirty {
			if d == rel || strings.HasPrefix(d, rel+"/") {
				_ = os.RemoveAll(filepath.Join(wt, rel))
				removed = append(removed, rel)
				break
			}
		}
	}
	return removed, nil
}
