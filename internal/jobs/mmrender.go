package jobs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"kraken/internal/store"
	"kraken/internal/util"
)

// mmrender.go — the mental model, rendered into Claude Code rules files.
//
// Every live item's TITLE becomes one line in a rules file Claude Code loads
// on its own: a repo-scoped item in `<checkout>/.claude/rules/kraken-mm/`,
// path-scoped to its dir through a `paths:` frontmatter glob so the line is in
// context exactly when an agent touches that part of the repo, and a
// universal item in `~/.claude/rules/kraken-mm.md`. The line carries the item
// id and the verb that opens it, so the title is the hook and `kraken mm show`
// is the drill-down — agents no longer have to remember to pull.
//
// The files land in carl's REAL checkouts (the primary under ~/Developer and
// every live cell worktree of it), so the one rule that shapes this file is
// devenv.go's: git status must stay clean. The dir is excluded through
// `<primary>/.git/info/exclude` (shared by every worktree), written before any
// file, and `git status` over the dir is the arbiter afterwards — anything git
// reports means the exclude did not take, and the dir comes back out.

// mmRulesDir is the repo-relative directory every repo-scoped rules file
// lives in; the exclude line is this plus a trailing slash.
const mmRulesDir = ".claude/rules/kraken-mm"

// mmExcludeLine is the line MMRenderPass keeps in <primary>/.git/info/exclude.
const mmExcludeLine = mmRulesDir + "/"

// mmRenderInterval throttles MMRenderPass.
const mmRenderInterval = 60 * time.Second

// mmRenderTitleRunes caps a fallback title (an item with no title of its own
// renders its first content line, cut here).
const mmRenderTitleRunes = 120

// mmRenderLast is the pass' throttle stamp (unix millis), and mmRenderMu
// serializes every write into a checkout: the pass and a spawn-time
// renderMMIntoWorktree can otherwise race on one directory. mmUnignoredLast
// (guarded by mmRenderMu) is the last mm-render-unignored message logged per
// checkout dir, so a checkout stuck in one refusal logs it once rather than on
// every 60s pass; a clean pass over the dir clears it.
var (
	mmRenderLast    atomic.Int64
	mmRenderMu      sync.Mutex
	mmUnignoredLast = map[string]string{}
)

// mmLogUnignored logs one mm-render-unignored event for dir unless it is the
// same message last logged for that dir. Caller holds mmRenderMu.
func mmLogUnignored(dir, format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	if mmUnignoredLast[dir] == msg {
		return
	}
	mmUnignoredLast[dir] = msg
	dlogEvent("error", "mm-render-unignored", "%s", msg)
}

// mmDirFileName is the per-dir rules-file name: the dir with "/" → "-". A
// dir whose slug is exactly "index" or "root" takes a "-dir" suffix, because
// both names are reserved by the files written beside it — index.md is the
// repo's unscoped index and root.md the repo-root dir's file — and a
// collision would merge an unscoped file into a path-scoped one.
func mmDirFileName(dir string) string {
	slug := strings.ReplaceAll(dir, "/", "-")
	if slug == "index" || slug == "root" {
		slug += "-dir"
	}
	return slug + ".md"
}

// MMRender is the whole render: per repo name, the rules files by
// repo-relative path; and the universal file's content ("" when there are no
// universal items).
type MMRender struct {
	Repos     map[string]map[string]string
	Universal string
}

// mmIndexHeader opens index.md and the universal file: what the lines are, and
// the two verbs that drill down from one. Whose model it is comes from
// Operator().
func mmIndexHeader() string {
	return operatorPossessive() + " mental model — one line per item they have committed, each title a claim they hold.\n" +
		"Open an item with `kraken mm show <id>`; see where it came from with `kraken mm source <id>`.\n"
}

// RenderMMFiles renders items into rules files. Deterministic: items are
// ordered by id, maps are emitted in sorted order, so identical input (and
// the same Operator(), which the index header names) is byte-identical output.
//
// Only live items render, and only items that say where they apply: a
// universal item, or a repo item naming its repo. The pre-capture items
// (scope defaulted to repo, no repo) render nowhere.
func RenderMMFiles(items []store.MMItem) MMRender {
	live := make([]store.MMItem, 0, len(items))
	for _, it := range items {
		if it.InvalidatedAt == 0 {
			live = append(live, it)
		}
	}
	sort.SliceStable(live, func(a, b int) bool { return live[a].ID < live[b].ID })

	out := MMRender{Repos: map[string]map[string]string{}}
	var universal strings.Builder
	type dirFile struct {
		dirs  map[string]bool
		lines strings.Builder
	}
	perRepo := map[string]map[string]*dirFile{} // repo → file name → file
	index := map[string]*strings.Builder{}
	for _, it := range live {
		line := mmRenderLine(it)
		if it.Scope == "universal" {
			universal.WriteString(line)
			continue
		}
		repo := strings.TrimSpace(it.Repo)
		if repo == "" {
			continue
		}
		dir := strings.Trim(strings.TrimSpace(it.Dir), "/")
		name := "root.md"
		if dir != "" {
			name = mmDirFileName(dir)
		}
		files := perRepo[repo]
		if files == nil {
			files = map[string]*dirFile{}
			perRepo[repo] = files
			index[repo] = &strings.Builder{}
		}
		f := files[name]
		if f == nil {
			f = &dirFile{dirs: map[string]bool{}}
			files[name] = f
		}
		f.dirs[dir] = true
		f.lines.WriteString(line)
		index[repo].WriteString(line)
	}
	for repo, files := range perRepo {
		rendered := map[string]string{}
		for name, f := range files {
			rendered[mmRulesDir+"/"+name] = mmFrontmatter(f.dirs) + f.lines.String()
		}
		rendered[mmRulesDir+"/index.md"] = mmIndexHeader() + index[repo].String()
		out.Repos[repo] = rendered
	}
	if universal.Len() > 0 {
		out.Universal = mmIndexHeader() + universal.String()
	}
	return out
}

// mmFrontmatter is a rules file's path scope: none for the repo root (an
// unscoped rule is always loaded), else one `paths:` glob per dir the file
// covers — normally one; two dirs only meet in one file when their slugs
// collide (`a/b` and `a-b`), and both then keep their scope.
func mmFrontmatter(dirs map[string]bool) string {
	var globs []string
	for d := range dirs {
		if d != "" {
			globs = append(globs, d)
		}
	}
	if len(globs) == 0 {
		return ""
	}
	sort.Strings(globs)
	var b strings.Builder
	b.WriteString("---\npaths:\n")
	for _, d := range globs {
		b.WriteString("  - \"" + d + "/**\"\n")
	}
	b.WriteString("---\n")
	return b.String()
}

// mmRenderLine is one item's line: its title (escaped), its id, and the verb
// that opens it.
func mmRenderLine(it store.MMItem) string {
	id := strconv.FormatInt(it.ID, 10)
	return "- " + mmRenderTitle(it) + " — mm #" + id + " (`kraken mm show " + id + "`)\n"
}

// mmRenderTitle is the item's title made safe for one markdown list line: its
// own title, or its first content line cut at mmRenderTitleRunes; line breaks
// folded to spaces; both HTML comment delimiters (`<!--`, `-->`) escaped so a
// title can neither open nor close a comment Claude Code strips; and a
// leading `-` backslashed so it cannot read as a nested
// list marker or a frontmatter fence.
func mmRenderTitle(it store.MMItem) string {
	title := strings.TrimSpace(it.Title)
	if title == "" {
		first := strings.TrimSpace(it.Content)
		if i := strings.IndexAny(first, "\r\n"); i >= 0 {
			first = strings.TrimSpace(first[:i])
		}
		if utf8.RuneCountInString(first) > mmRenderTitleRunes {
			first = string([]rune(first)[:mmRenderTitleRunes])
		}
		title = first
	}
	title = strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ").Replace(title)
	// Both comment delimiters are escaped: a bare `<!--` would open an HTML
	// comment that nothing later closes (every `-->` is escaped too), hiding
	// every line after it from the agent reading the rules file.
	title = strings.NewReplacer("<!--", "&lt;!--", "-->", "--&gt;").Replace(title)
	if strings.HasPrefix(title, "-") {
		title = `\` + title
	}
	return title
}

// MMRenderPass keeps every checkout's rules files in step with the live mental
// model: the primary checkout of every repo under ~/Developer, every live cell
// worktree, and the universal file. Throttled to once per mmRenderInterval,
// stamped before any bail (MMVerifyPass' reason); never under --mirror (the
// daemon gates it) since it writes into real checkouts.
//
// Every candidate checkout is visited, not only repos that have items now:
// a repo whose last item died still carries the files from the last render,
// and only a visit removes them. A visit to a checkout with nothing to render
// and no rules dir is one stat.
func MMRenderPass(st *store.Store, now time.Time) {
	if st == nil {
		return
	}
	if last := mmRenderLast.Load(); last != 0 && now.UnixMilli()-last < mmRenderInterval.Milliseconds() {
		return
	}
	mmRenderLast.Store(now.UnixMilli())

	items, err := st.ListMMItems(false)
	if err != nil {
		dlogErr("mm render: list items: %v", err)
		return
	}
	render := RenderMMFiles(items)

	var checkouts []string
	for _, name := range RepoCandidates() {
		checkouts = append(checkouts, filepath.Join(repoRoot(), name))
	}
	if all, err := loadJobsFromStore(st); err == nil {
		live := LiveSessions()
		resolved := map[string]string{}
		for _, j := range all {
			if _, ok := FindCellSession(j, live); !ok {
				continue
			}
			repo, seen := resolved[j.Repo]
			if !seen {
				repo, _ = ResolveRepo(j.Repo)
				resolved[j.Repo] = repo
			}
			if repo == "" {
				continue
			}
			if wt := ExistingWorktree(j, repo); wt != "" {
				checkouts = append(checkouts, wt)
			}
		}
	} else {
		dlogErr("mm render: load jobs: %v", err)
	}

	changed := map[string]bool{}
	for _, dir := range checkouts {
		name := mmCheckoutRepo(dir)
		c, err := mmSyncCheckout(dir, render.Repos[name])
		if err != nil {
			dlogErr("mm render: %s: %v", dir, err)
		}
		if c {
			changed[name] = true
		}
	}
	if c, err := mmSyncUniversal(render.Universal); err != nil {
		dlogErr("mm render: universal: %v", err)
	} else if c {
		changed["(universal)"] = true
	}
	if len(changed) > 0 {
		names := make([]string, 0, len(changed))
		for n := range changed {
			names = append(names, n)
		}
		sort.Strings(names)
		dlogEvent("info", "mm-render", "mm render updated rules files for %s", strings.Join(names, ", "))
	}
}

// renderMMIntoWorktree is the per-checkout half of the pass, run by StartCell
// and ReactivateJob once the worktree exists and before zeus spawns, so zeus'
// first turn already has the rules files. repo is the repo's checkout path
// (or its name — only the basename keys the render); when it is an existing
// directory other than wt, the primary is brought in step too, since the two
// share one exclude. Errors are returned for the caller to LOG: a render
// failure never fails a spawn.
func renderMMIntoWorktree(st *store.Store, repo, wt string) error {
	if st == nil {
		return nil
	}
	items, err := st.ListMMItems(false)
	if err != nil {
		return fmt.Errorf("mm render: list items: %w", err)
	}
	files := RenderMMFiles(items).Repos[filepath.Base(filepath.Clean(repo))]
	var firstErr error
	if fi, serr := os.Stat(repo); serr == nil && fi.IsDir() && filepath.Clean(repo) != filepath.Clean(wt) {
		if _, err := mmSyncCheckout(repo, files); err != nil {
			firstErr = err
		}
	}
	if _, err := mmSyncCheckout(wt, files); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// mmCheckoutRepo names the repo a checkout belongs to: the basename of the
// directory holding its git common dir — `~/Developer/<repo>` for the primary
// and for every worktree of it alike. Falls back to the checkout's own
// basename when git cannot say.
func mmCheckoutRepo(dir string) string {
	if common, err := mmGitCommonDir(dir); err == nil {
		return filepath.Base(filepath.Dir(common))
	}
	return filepath.Base(dir)
}

// mmRenderGit runs one `git -C dir …` under its own mmGitTimeout (60s) — the
// render runs on the daemon tick, so a wedged git (a stuck index.lock, a hung
// fsmonitor) must not hold the tick. A timeout is just a failed call: the
// callers' existing error branches (no exclude → no files, status failure →
// dir removed) take it. Deliberately NOT mmRunGit: that var is the verify
// pass' test seam, and swapping it must not reroute the render's real git.
func mmRenderGit(dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), mmGitTimeout)
	defer cancel()
	return exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...).Output()
}

// mmGitCommonDir resolves `git rev-parse --git-common-dir` to an absolute path.
func mmGitCommonDir(dir string) (string, error) {
	out, err := mmRenderGit(dir, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", fmt.Errorf("git rev-parse --git-common-dir: %v", err)
	}
	common := strings.TrimSpace(string(out))
	if common == "" {
		return "", errors.New("git rev-parse --git-common-dir: empty")
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(dir, common)
	}
	return filepath.Clean(common), nil
}

// mmSyncCheckout brings one checkout's rules dir in step with files (the
// repo's render; nil for none), reporting whether anything on disk changed.
//
// The order is the safety argument. A path the repo TRACKS is never touched
// (removing it would delete carl's files). The exclude line is written before
// any file, and a checkout whose exclude cannot be written gets no files.
// Files are written only when their bytes differ, files the render no longer
// produces are deleted, and the dir goes when empty. Last, `git status` over
// the dir must report nothing — anything it reports (or a status that fails)
// takes the whole dir back out, so the worst case is a missing memory file,
// never a dirty checkout.
func mmSyncCheckout(dir string, files map[string]string) (bool, error) {
	mmRenderMu.Lock()
	defer mmRenderMu.Unlock()
	rules := filepath.Join(dir, mmRulesDir)
	_, statErr := os.Stat(rules)
	exists := statErr == nil
	if len(files) == 0 && !exists {
		delete(mmUnignoredLast, dir)
		return false, nil
	}
	if out, err := mmRenderGit(dir, "ls-files", "--", mmRulesDir); err != nil {
		return false, fmt.Errorf("git ls-files: %v", err)
	} else if strings.TrimSpace(string(out)) != "" {
		mmLogUnignored(dir, "mm render: %s tracks %s — left untouched", dir, mmRulesDir)
		return false, fmt.Errorf("%s tracks %s", dir, mmRulesDir)
	}
	changed := false
	if len(files) > 0 {
		if err := mmEnsureExclude(dir); err != nil {
			mmLogUnignored(dir, "mm render: %s: cannot write the exclude (%v) — no rules files", dir, err)
			if exists {
				_ = os.RemoveAll(rules)
				mmRemoveEmptyParents(dir)
				changed = true
			}
			return changed, fmt.Errorf("exclude: %w", err)
		}
		if err := os.MkdirAll(rules, 0o755); err != nil {
			return false, err
		}
		for rel, content := range files {
			p := filepath.Join(dir, rel)
			if cur, err := os.ReadFile(p); err == nil && bytes.Equal(cur, []byte(content)) {
				continue
			}
			if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
				return changed, err
			}
			changed = true
		}
	}
	// delete what the render no longer produces
	if entries, err := os.ReadDir(rules); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			if _, keep := files[mmRulesDir+"/"+e.Name()]; keep {
				continue
			}
			if err := os.Remove(filepath.Join(rules, e.Name())); err == nil {
				changed = true
			}
		}
	}
	if err := os.Remove(rules); err == nil { // succeeds only when empty
		mmRemoveEmptyParents(dir)
		delete(mmUnignoredLast, dir)
		return true, nil
	}
	out, err := mmRenderGit(dir, "status", "--porcelain", "--untracked-files=normal", "--", mmRulesDir)
	if err != nil || strings.TrimSpace(string(out)) != "" {
		_ = os.RemoveAll(rules)
		mmRemoveEmptyParents(dir)
		reason := strings.TrimSpace(string(out))
		if err != nil {
			reason = err.Error()
		}
		mmLogUnignored(dir, "mm render: %s: git status reported %s — rules dir removed", dir, reason)
		return true, fmt.Errorf("git status reported the rules dir (%s)", reason)
	}
	delete(mmUnignoredLast, dir)
	return changed, nil
}

// mmRemoveEmptyParents removes .claude/rules and .claude under dir when they
// are empty — left behind by a rules dir this file created and then removed.
// os.Remove refuses a non-empty dir, so nothing anyone else put there goes.
func mmRemoveEmptyParents(dir string) {
	_ = os.Remove(filepath.Join(dir, ".claude", "rules"))
	_ = os.Remove(filepath.Join(dir, ".claude"))
}

// mmEnsureExclude appends mmExcludeLine to the checkout's shared
// info/exclude unless a line already says it. The common dir, not the
// worktree's own git dir, because git reads info/exclude from there for every
// worktree of the repo.
func mmEnsureExclude(dir string) error {
	common, err := mmGitCommonDir(dir)
	if err != nil {
		return err
	}
	p := filepath.Join(common, "info", "exclude")
	cur, err := os.ReadFile(p)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, line := range strings.Split(string(cur), "\n") {
		if strings.TrimSpace(line) == mmExcludeLine {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	add := mmExcludeLine + "\n"
	if len(cur) > 0 && !bytes.HasSuffix(cur, []byte("\n")) {
		add = "\n" + add
	}
	if _, err := f.WriteString(add); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// mmUniversalPath is the user-level rules file universal items render into.
func mmUniversalPath() string {
	return filepath.Join(util.Home(), ".claude", "rules", "kraken-mm.md")
}

// mmSyncUniversal writes (or, for "", deletes) the universal rules file, by
// the same byte-compare rule as a checkout's files.
func mmSyncUniversal(content string) (bool, error) {
	mmRenderMu.Lock()
	defer mmRenderMu.Unlock()
	p := mmUniversalPath()
	cur, err := os.ReadFile(p)
	if content == "" {
		if err != nil {
			return false, nil
		}
		if err := os.Remove(p); err != nil {
			return false, err
		}
		return true, nil
	}
	if err == nil && string(cur) == content {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return false, err
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		return false, err
	}
	return true, nil
}
