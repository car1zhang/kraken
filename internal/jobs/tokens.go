package jobs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
// a job's transcripts — zeus', every pane thread's and self-origin ask
// thread's, and every teammate's, since they all run in the cell's worktree
// and Claude Code buckets transcripts by cwd — plus, last, the cell dir's own
// project dirs, where a remote-origin ask thread's transcripts land.
//
// The path is derived from CellWorktree's STRING, never from
// ExistingWorktree: teardownCell removes the cell dir (cell.go), so
// ExistingWorktree returns "" for every done or archived job, while the
// transcripts under ~/.claude/projects survive indefinitely — Claude Code
// owns that dir and kraken only ever reads it. A number that vanished at
// teardown would defeat the entire point, which is retrospective cost.
//
// Four base dirs per name it has held, not one, along two independent legacy
// axes — and every one of them is load-bearing, because a base that is missing
// here reports as a clean zero rather than as an error:
//
//   - wt-<repo> vs a bare "wt": cells created before worktrees were named
//     wt-<repo> used a bare "wt" (see ExistingWorktree's own comment), which
//     munges to a different project dir.
//   - <slug>-<id> vs a bare <slug>: a cell dir carries the job's id (cell.go's
//     cellName) so that two jobs sharing a slug stop sharing a directory. Every
//     transcript written BEFORE that cutover is filed by Claude Code under the
//     bare-slug path — Claude Code buckets by the agent's startup cwd and has
//     no idea kraken renamed anything — so a scan that knew only the new name
//     would report ZERO for every job's entire history. That is not a
//     hypothetical, and it is LIVE rather than historical: the v11 migration
//     dropped every token row, and v14 drops them all again this release (the
//     streamed-output fix — core.tokenGroup), so the daemon re-sums from
//     scratch and the legacy base is the only thing standing between the board
//     and a wall of zeroes the morning after.
//
// The legacy bare-slug base is the one place where two same-slug jobs still
// collide, and it is not fixable here: their pre-cutover transcripts are
// physically in one directory and nothing recorded which agent wrote which
// file. Summing it for both is the honest answer — under-reporting a job's
// real history would be worse, and the alternative (dropping the base) reports
// zero for both.
//
// A repo of "" yields nothing — with no resolved repo there is no worktree
// path to munge.
//
// Plus every project dir whose name extends one of those bases, because
// Claude Code buckets a transcript by the agent's STARTUP CWD, not by the
// worktree root. Agents in a cell routinely start deeper — a teammate
// spawned in a subpackage, an iOS build agent in ios/ — and each such cwd
// becomes its own sibling dir: `…-cells-asr-latency-wt-monolith` gains
// `…-wt-monolith-internal-dashboard`, `…-cells-kraken-ios-wt-kraken` gains
// `…-wt-kraken-ios`. Both exist on this machine. Measured across every cell
// dir, the deeper ones hold 5.0% of all spend and up to 26.2% for a single
// job — a silent under-count that varies per job, which is worse than a flat
// one because it makes jobs incomparable, and comparing them is the entire
// purpose of a per-job cost.
//
// The extension must be at a SEPARATOR (base + "-"). That is what keeps the
// match inside this job: the text between `-kraken-cells-` and `-wt-` is the
// cell name, so a dir extending `…-cells-demo-25-wt-…` can only belong to job
// 25. Two cell names that merely share a prefix do NOT collide, because
// `…-cells-demo-2-5-wt-monolith` does not extend `…-cells-demo-25-wt`.
// Prefixing on the CELL DIR instead of the worktree would collide on exactly
// that pair, which is why the bases are worktree-shaped. Note that appending
// the id made this MORE important rather than less: `demo` id 25 and `demo-2`
// id 5 are a pair the id itself cannot separate — only the `-wt` anchor can.
//
// One consequence is deliberate rather than accidental: the legacy bare-`wt`
// base is itself a prefix of every `wt-<repo>` name, so it also sweeps in
// OTHER-repo worktrees of the SAME slug. That is correct — a job whose
// `repo:` changed has transcripts under both and they are all its own spend.
// Reaching a DIFFERENT job would require that job's slug to literally contain
// `wt-`, which nobody writes. Checked against every project dir on this
// machine, driving all 95 known slugs: 114 dirs claimed, zero claimed by more
// than one slug.
//
// The error is NOT cosmetic and a caller must not drop it. It means the
// returned list may be SHORT — the deeper dirs could not be enumerated — and a
// short list is indistinguishable from "those dirs went away". Since
// refreshJobTokens stores a watermark per file and SetJobTokenOffsets is
// replace-all, a silently short list DELETES the watermarks of every deeper
// dir and re-sums those files from byte zero into counters that only
// accumulate. Measured: one unlistable ReadDir turned a true 600 into a
// permanent 1100. Returning the bases with an error lets the caller keep
// those watermarks instead.
func JobProjectDirs(j *Job, repo string) ([]string, error) {
	if j == nil || repo == "" {
		return nil, nil
	}
	root := filepath.Join(util.Home(), ".claude", "projects")
	seen := map[string]bool{}
	var bases []string
	// Current naming first, legacy second, so dirs[0] is always the base a
	// live cell is actually writing into. The bare-"wt" variant of each is
	// listed beside it; ProjectDirName-level dedup below collapses any pair
	// that happens to munge identically.
	worktrees := append(
		worktreeNamesIn(cellDir(j), repo),
		worktreeNamesIn(legacyCellDir(j), repo)...,
	)
	// The same four bases per FORMER slug, appended after the current ones.
	// Claude Code buckets a transcript by the agent's startup cwd and a rename
	// moves no worktree, so every transcript a renamed job has ever written —
	// including the ones its live cell is appending to RIGHT NOW — is filed
	// under an older name. Dropping them would not merely lose history: the
	// watermark write below is REPLACE-ALL, so a shrunk dir list deletes those
	// files' offsets and re-sums them from byte zero into counters that only
	// accumulate, permanently inflating the job's number.
	for _, f := range j.FormerSlugs {
		name := formerCellName(j, f)
		if name == "" {
			continue
		}
		dir := filepath.Join(cellsRoot(), name)
		legacy := filepath.Join(cellsRoot(), util.Slugify(f))
		worktrees = append(worktrees, worktreeNamesIn(dir, repo)...)
		worktrees = append(worktrees, worktreeNamesIn(legacy, repo)...)
	}
	for _, wt := range worktrees {
		name := core.ProjectDirName(wt)
		if seen[name] {
			continue
		}
		seen[name] = true
		bases = append(bases, name)
	}
	out := make([]string, 0, len(bases))
	for _, name := range bases {
		out = append(out, filepath.Join(root, name))
	}
	// The cell dir itself, under every name it has held: a remote-origin ask
	// thread runs with the cell dir as its cwd (askthread.go), so its
	// transcripts are filed there. These are exact names only and never join
	// `bases`: a cell dir's munged name can be a prefix of OTHER jobs' cell
	// dirs (job `demo` id 25's `…-cells-demo-25` prefixes job `demo-25-x` id
	// 7's `…-cells-demo-25-x-7`; the doc comment above explains why only the
	// `-wt` anchor separates cell names), so sweeping its extensions would
	// count a stranger's spend. Appended last, after every worktree-derived
	// dir, on every return path below. The legacy bare-slug dirs of former
	// slugs are listed too, so a rename never drops a dir this list returned
	// before it (the watermark reason above).
	cellDirNames := append(cellDirCandidates(j), legacyCellDir(j))
	for _, f := range j.FormerSlugs {
		if s := util.Slugify(f); s != "" {
			cellDirNames = append(cellDirNames, filepath.Join(cellsRoot(), s))
		}
	}
	var cellDirs []string
	for _, d := range cellDirNames {
		name := core.ProjectDirName(d)
		if seen[name] {
			continue
		}
		seen[name] = true
		cellDirs = append(cellDirs, filepath.Join(root, name))
	}
	// One ReadDir of ~/.claude/projects (~145 entries) per job per refresh.
	// It is comparable to the WalkDir each returned dir already costs, and
	// only jobs the pass actually decided to sum get here. A projects dir that
	// can't be listed still yields the bases — they are named by pure string
	// derivation and need no listing — but it is an ERROR, because the deeper
	// dirs are missing from an answer the caller turns into a watermark set.
	entries, err := os.ReadDir(root)
	if err != nil {
		// No projects dir AT ALL is not a degradation — it is a machine with
		// no transcripts, so there are no deeper dirs to miss and no
		// watermarks to protect. Anything else (a permission blip, EMFILE in
		// a long-lived daemon) means the deeper dirs exist and we simply
		// could not see them, which is the case the caller must not mistake
		// for "they went away". Exactly the root-absent/subtree-failed split
		// core.SumTokensDelta's walk makes, for the same reason.
		if os.IsNotExist(err) {
			return append(out, cellDirs...), nil
		}
		return append(out, cellDirs...), fmt.Errorf("list %s: %v", root, err)
	}
	for _, e := range entries {
		if !e.IsDir() || seen[e.Name()] {
			continue
		}
		for _, base := range bases {
			if strings.HasPrefix(e.Name(), base+"-") {
				seen[e.Name()] = true
				out = append(out, filepath.Join(root, e.Name()))
				break
			}
		}
	}
	return append(out, cellDirs...), nil
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
// A degraded scan (core.SumTokensDelta's error contract — a line over the
// 16 MB ceiling, a mid-file read error, an unlistable subtree, an unstattable
// file) still WRITES: the counters it did find are real and the watermarks
// stop at the last complete line, so folding them in is strictly better than
// dropping them, and the error is returned so the caller can say out loud
// that the number is not exact. That is the distinction this repo has got
// wrong three times: a failed read is not a zero, and it must not be
// presented as a total.
//
// EVERY way of seeing less than the whole truth has to arrive as an error,
// not just the ones inside the scan. A short dir list from JobProjectDirs
// counts too, and is folded into the same scanErr, because the carry-forward
// below is the only thing standing between a transient read failure and a
// permanently inflated row — and it can only act on what it is told.
func refreshJobTokens(st *store.Store, j *Job, repo string, prior map[string]int64) (bool, error) {
	var delta store.JobTokens
	offsets := map[string]int64{}
	dirs, scanErr := JobProjectDirs(j, repo)
	for _, dir := range dirs {
		tk, next, err := core.SumTokensDelta(dir, prior)
		if err != nil && scanErr == nil {
			scanErr = err
		}
		delta.In += tk.In
		delta.Out += tk.Out
		delta.CacheWrite += tk.CacheWrite
		delta.CacheRead += tk.CacheRead
		// The zeus half of the same delta — what the cell's lead agent spent,
		// as opposed to what it delegated. core.SumTokensDelta has already
		// partitioned it per transcript file, so there is no decision to make
		// here, only the same fold.
		delta.ZeusIn += tk.ZeusIn
		delta.ZeusOut += tk.ZeusOut
		delta.ZeusCacheWrite += tk.ZeusCacheWrite
		delta.ZeusCacheRead += tk.ZeusCacheRead
		delta.Sessions += int64(tk.Sessions)
		for path, off := range next {
			offsets[path] = off
		}
	}

	// An incomplete scan — a dir list that came up short, a subtree that could
	// not be listed, a file that could not be stat'd, a file that stopped part
	// way — means `offsets` is missing entries for files that still exist.
	// Writing it as-is would DELETE those watermarks (SetJobTokenOffsets is
	// replace-all) and the next pass would re-sum those files from byte zero
	// into counters that only ever accumulate, permanently doubling them.
	// Carrying the unseen ones forward costs at worst a stale row for a
	// genuinely deleted transcript, which is inert; the alternative silently
	// corrupts the number for good.
	//
	// This guard is only as good as the reporting that feeds it, which is why
	// every degradation above is an error rather than a quietly shorter
	// answer. Two of them were silent and this guard could not see either.
	if scanErr != nil {
		for path, off := range prior {
			if _, seen := offsets[path]; !seen {
				offsets[path] = off
			}
		}
	}

	// The four ZEUS counters are deliberately absent from this condition. Each
	// is a SUBSET of the total above it, so a non-zero zeus delta with all four
	// totals at zero is unreachable — testing for it would be dead code, not a
	// missing guard.
	if sameOffsets(prior, offsets) && delta.In == 0 && delta.Out == 0 &&
		delta.CacheWrite == 0 && delta.CacheRead == 0 {
		return false, scanErr
	}
	// Offsets first: a crash between the two writes must not leave the
	// counters advanced with the watermarks behind, which would re-sum the
	// same bytes on the next pass and double-count them into a column that
	// only ever accumulates. Written in the other order, the worst case is a
	// delta silently lost once — an undercount, which is the cheaper way to
	// be wrong here, because nothing can ever take an over-count back out.
	//
	// That asymmetry is the standing hazard of accumulate-only counters, and
	// it is not fully closed. Two states still add a file's bytes twice: a
	// transcript REPLACED by a shorter one (core.SumTokensDelta re-reads it
	// whole while the row already holds the old file's contribution), and the
	// derived project dir CHANGING for a started job (ResolveRepo's answer
	// moving, which re-keys every path). Both need something outside
	// append-only to happen and neither is reachable today; the principled
	// fix is to store per-file totals in job_token_files and make job_tokens
	// a SUM over them rather than a running total, which is a storage-model
	// change and not this pass's to make. Documented rather than claimed
	// away — the counters are exact under append-only and monotonic
	// otherwise.
	if err := st.SetJobTokenOffsets(j.ID, offsets); err != nil {
		return false, err
	}
	if err := st.BumpJobTokens(j.ID, delta); err != nil {
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
		return // no store wired (a storeless test or CLI process): nowhere to put the number
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
		// A job with no store id cannot own a row: job_tokens is keyed on the
		// id since v11, and BumpJobTokens refuses a zero one rather than
		// letting every unpersisted job accumulate into a shared row 0. Only a
		// Job built by ParseJob alone (which never sets ID) reaches this, and
		// loaded jobs always come from the store. Skipped silently: it is the
		// same "this job has no transcripts to measure" state as a job that
		// never started.
		if j.ID == 0 {
			continue
		}
		// d.TokenScan stays keyed on the SLUG while everything around it moved
		// to the id, and that is a deliberate exception rather than an
		// oversight. It is not an identity: it is a per-tick throttle clock over
		// the ACTIVE board only, whose slugs jobs_active_slug already makes
		// unique, it lives in daemon memory and never persists, and the one
		// hazard slug-keying carries here — a freed slug's stale stamp making an
		// unrelated later job wait out an interval it never earned — is closed
		// by the prune at the bottom of this function. Re-keying it would mean
		// retyping daemonState.TokenScan in daemon.go, which this change has no
		// other reason to touch.
		if !live[CellSession(j)] {
			if last := d.TokenScan[j.Slug]; last != 0 && now.UnixMilli()-last < tokensIdleInterval.Milliseconds() {
				continue
			}
			if idle >= tokensIdleBatch {
				continue
			}
			idle++
		}
		// Stamped HERE — before the repo resolve can bail — because this job
		// has now had its turn whatever comes of it. Stamping only on success
		// meant a job whose repo no longer resolves consumed an idle slot and
		// was immediately due again, every tick, forever: with
		// tokensIdleBatch such jobs on the board, no other idle job was ever
		// refreshed again, silently, since a repo bail logs nothing.
		d.TokenScan[j.Slug] = now.UnixMilli()
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
		wrote, err := refreshJobTokens(st, j, repo, all[j.ID])
		if err != nil {
			key := "tokensfail:" + j.Slug
			if d.Notified[key] == 0 {
				dlogErr("%s: token sum is not exact: %v", j.Slug, err)
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
