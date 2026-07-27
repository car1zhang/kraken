package jobs

import (
	"path/filepath"
	"time"

	"kraken/internal/core"
	"kraken/internal/store"
	"kraken/internal/util"
)

// tokens.go — the per-job TOKEN spend: which transcripts belong to a job,
// and the daemon pass that keeps kraken.db's job_tokens row up to date.
//
// "tokens", never "usage": `usage` in this package already means ACCOUNT
// QUOTA (usage.go, ~/.kraken/usage.json, the board's pool widget). The two
// numbers are unrelated and must stay greppably distinct.
//
// The number is computed HERE, by the daemon, rather than by whoever renders
// it. A per-read sum is not viable and that is measured, not assumed: the
// board payload carries ~74 jobs behind a 2-second cache that web polls every
// 3s and iOS every 4s, and a full re-decode of one 20-60 MB cell project dir
// costs 0.1-0.3s warm in Go. Even restricting the sum to the live cells would
// put seconds inside a board build that already holds its mutex for the whole
// build. So: the daemon computes, the store holds, the board reads a row —
// the same division "daemon writes board state, web only displays it" already
// draws everywhere else in this package.

// tokensIdleInterval is how often a job with NO live cell is re-summed. Its
// transcripts can still change — a torn-down cell's files are frozen, but a
// job whose cell died between ticks may have unread tail — while costing a
// stat per file to find out, so they are checked on a slow cadence rather
// than never (which would strand the last few thousand tokens of every job
// that ends between ticks) or every tick (which would spend the whole board's
// worth of stats on jobs that provably cannot have moved).
const tokensIdleInterval = 10 * time.Minute

// tokensIdleBatch caps how many idle jobs one tick will re-sum. With ~24
// active jobs the interval alone already bounds the work, but the bound must
// not be a function of how many jobs happen to fall due in the same tick —
// a daemon restart makes EVERY job due at once, and that burst is exactly
// when the tick can least afford it. Live cells are never subject to this
// cap; they are the jobs actually accruing spend.
const tokensIdleBatch = 8

// JobProjectDirs returns the absolute ~/.claude/projects directories holding
// a job's transcripts — zeus', every side thread's, and every teammate's,
// since they all run in the cell's worktree and Claude Code buckets
// transcripts by cwd.
//
// The path is derived from CellWorktree's STRING, never from
// ExistingWorktree: teardownCell removes the cell dir (cell.go), so
// ExistingWorktree returns "" for every done or archived job, while the
// transcripts under ~/.claude/projects survive indefinitely — Claude Code
// owns that dir and kraken only ever reads it. A number that vanished at
// teardown would defeat the entire point, which is retrospective cost.
//
// Two dirs, not one: cells created before worktrees were named wt-<repo>
// used a bare "wt" (see ExistingWorktree's own comment), which munges to a
// different project dir. Both are returned, deduped, and summed together.
// A repo of "" yields nothing — with no resolved repo there is no worktree
// path to munge.
func JobProjectDirs(j *Job, repo string) []string {
	if j == nil || repo == "" {
		return nil
	}
	root := filepath.Join(util.Home(), ".claude", "projects")
	seen := map[string]bool{}
	var out []string
	for _, wt := range []string{CellWorktree(j, repo), filepath.Join(cellDir(j), "wt")} {
		name := core.ProjectDirName(wt)
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, filepath.Join(root, name))
	}
	return out
}

// refreshJobTokens sums one job's transcripts forward from the watermarks in
// prior and folds the result into the store. Reports whether it wrote
// anything.
//
// Nothing is written when nothing moved, and that is the pass's whole
// steady-state behaviour: a settled job's files are unchanged, so
// core.SumTokensDelta opens none of them, the delta is zero, the offsets are
// identical, and this returns (false, nil) without touching the database.
//
// A partial scan (core.SumTokensDelta's error contract — a line over the
// 16 MB ceiling, a mid-file read error) still WRITES: the counters it did
// find are real and the watermarks stop at the last complete line, so folding
// them in is strictly better than dropping them, and the error is returned so
// the caller can say out loud that the number is a floor. That is the
// distinction this repo has got wrong three times: a failed read is not a
// zero, and it must not be presented as a total.
func refreshJobTokens(st *store.Store, j *Job, repo string, prior map[string]int64) (bool, error) {
	var delta store.JobTokens
	offsets := map[string]int64{}
	var scanErr error
	for _, dir := range JobProjectDirs(j, repo) {
		tk, next, err := core.SumTokensDelta(dir, prior)
		if err != nil && scanErr == nil {
			scanErr = err
		}
		delta.In += tk.In
		delta.Out += tk.Out
		delta.CacheWrite += tk.CacheWrite
		delta.CacheRead += tk.CacheRead
		delta.Sessions += int64(tk.Sessions)
		for path, off := range next {
			offsets[path] = off
		}
	}

	if sameOffsets(prior, offsets) && delta.In == 0 && delta.Out == 0 &&
		delta.CacheWrite == 0 && delta.CacheRead == 0 {
		return false, scanErr
	}
	// Offsets first: a crash between the two writes must not leave the
	// counters advanced with the watermarks behind, which would re-sum the
	// same bytes on the next pass and double-count them into a column that
	// only ever accumulates. Written in the other order, the worst case is a
	// delta silently lost once — an undercount, which is the direction this
	// number is allowed to be wrong in.
	if err := st.SetJobTokenOffsets(j.Slug, offsets); err != nil {
		return false, err
	}
	if err := st.BumpJobTokens(j.Slug, delta); err != nil {
		return false, err
	}
	return true, scanErr
}

// sameOffsets reports whether two watermark sets are identical. It is what
// lets a settled job cost zero database writes: a file that has not grown
// keeps its exact offset, so an unchanged set means nothing anywhere moved.
func sameOffsets(a, b map[string]int64) bool {
	if len(a) != len(b) {
		return false
	}
	for path, off := range a {
		if b[path] != off {
			return false
		}
	}
	return true
}

// tokensPass is the daemon's per-tick token refresh, a sibling of watchPRs
// and syncColor but with none of their reach: it reads transcript files and
// writes two store tables, and touches no pane, no tmux, no network and no
// git.
//
// Pacing, which is the only interesting thing about it:
//
//   - Watermarks for EVERY job are read in one query, not one per job.
//   - A job that never entered a cell is skipped outright — it has no
//     worktree, so no transcripts can exist under its name.
//   - ResolveRepo is memoized for the pass. It does an os.ReadDir of
//     ~/Developer plus an isGitRepo per entry on EVERY call with no
//     memoization of its own (job.go), and jobs overwhelmingly share a
//     handful of distinct repo names, so calling it per job per tick would
//     be the pass's dominant cost by a wide margin. A failed resolve is
//     cached as "" so a job pointing at a nonexistent repo doesn't re-pay it
//     either.
//   - Jobs with a live cell are summed every tick; everything else is
//     throttled to tokensIdleInterval and capped at tokensIdleBatch per tick.
//
// Steady state for a job whose transcripts have not grown is therefore one
// WalkDir plus one stat per transcript file, and zero writes.
//
// Skipped under mirror by its caller, on the same rule as every other pass in
// the tick: mirror runs state derivation and the block-event artifact sync
// and nothing else.
func (d *daemonState) tokensPass(jobs []*Job, live map[string]bool, now time.Time) {
	st := ActiveStore()
	if st == nil {
		return // file-mode entrypoints have nowhere to put the number
	}
	all, err := st.AllJobTokenOffsets()
	if err != nil {
		// One line, deduped like syncColor's colorfail key: a store that
		// cannot answer this will not answer it four times a minute either.
		if d.Notified["tokensfail"] == 0 {
			dlogErr("token pass: read offsets: %v", err)
			d.Notified["tokensfail"] = now.Unix()
		}
		return
	}
	delete(d.Notified, "tokensfail")

	repos := map[string]string{}
	idle := 0
	present := map[string]bool{}
	for _, j := range jobs {
		present[j.Slug] = true
		if !j.Started() {
			continue
		}
		if !live[CellSession(j)] {
			if last := d.TokenScan[j.Slug]; last != 0 && now.UnixMilli()-last < tokensIdleInterval.Milliseconds() {
				continue
			}
			if idle >= tokensIdleBatch {
				continue
			}
			idle++
		}
		repo, ok := repos[j.Repo]
		if !ok {
			// A resolve failure is cached as "" and reported by the queue
			// pass' own repofail path, not here — this pass has nothing to
			// add to it.
			resolved, err := ResolveRepo(j.Repo)
			if err != nil {
				resolved = ""
			}
			repo, repos[j.Repo] = resolved, resolved
		}
		if repo == "" {
			continue
		}
		d.TokenScan[j.Slug] = now.UnixMilli()
		wrote, err := refreshJobTokens(st, j, repo, all[j.Slug])
		if err != nil {
			key := "tokensfail:" + j.Slug
			if d.Notified[key] == 0 {
				dlogErr("%s: token sum is a FLOOR, not a total: %v", j.Slug, err)
				d.Notified[key] = now.Unix()
			}
			continue
		}
		delete(d.Notified, "tokensfail:"+j.Slug)
		if wrote {
			dlog("%s: tokens refreshed", j.Slug)
		}
	}

	// Drop scan stamps for slugs that left the board (archived, hard-deleted),
	// for pruneArtifactSyncState's reason: archiving frees a slug, and a stale
	// stamp keyed on it would make a later unrelated job of the same name wait
	// out an interval it never earned.
	for slug := range d.TokenScan {
		if !present[slug] {
			delete(d.TokenScan, slug)
		}
	}
}
