package jobs

import (
	"bufio"
	"os"
	"sync"
	"time"

	"kraken/core"
)

// prscan.go — the daemon's transcript-scanning PR backstop. listBranchPRs and
// the pr-hook (prpoll.go, prhook.go) cover the common cases: a branch-list
// query and a real-time PostToolUse hook on `gh pr create`. Neither catches a
// PR zeus opened by hand outside that exact hook path (a re-run, a manual
// `gh pr create` variant the hook's command match misses) whose URL still
// only ever appears in zeus' own transcript. This file closes that gap by
// scanning the transcript directly, own-repo only — unlike the hook, there is
// no fail-open here: an unresolved repo prefix means scan nothing (carl's Q7
// decision).

// scanTranscriptPRURLs streams path line by line (bufio.Scanner, sized like
// scanPendingQuestion's — transcript lines can run to several MB of inlined
// tool output) and returns every distinct PR URL under prefix, in first-seen
// order. Transcript lines are JSON with escaped strings, but a GitHub PR URL
// never contains a character JSON escapes, so matching prURLPattern against
// the raw line text is correct without decoding each line's structure.
// Best-effort: an unreadable file returns nil, and a line that overflows the
// scanner's buffer just ends the scan there — whatever URLs were already
// found stand, since this is a backstop behind the hook and the branch-list
// tracker, not the sole record.
func scanTranscriptPRURLs(path, prefix string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var urls []string
	seen := map[string]bool{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		for _, url := range matchPRURLs(sc.Text(), prefix) {
			if seen[url] {
				continue
			}
			seen[url] = true
			urls = append(urls, url)
		}
	}
	return urls
}

// prScanCache memoizes scanTranscriptPRURLs per transcript path on (size,
// mtime), mirroring pendingQuestionCache in status.go: the daemon's PR pass
// hits the same live transcripts every tick, and re-reading a multi-MB file
// when nothing has been appended is wasted work. Transcripts only append, so
// a change always means a re-scan of the whole file — simple, correct.
var (
	prScanCacheMu sync.Mutex
	prScanCache   = map[string]prScanCacheEntry{}
)

type prScanCacheEntry struct {
	size   int64
	mtime  time.Time
	prefix string
	urls   []string
}

// transcriptPRURLs is scanTranscriptPRURLs with that memoization. Keyed by
// transcript path; a prefix mismatch against the cached entry also forces a
// rescan (defensive — a job's repo prefix never actually changes underneath
// a live transcript, but a stale cross-job cache hit must never happen).
func transcriptPRURLs(path, prefix string) []string {
	info, statErr := os.Stat(path)
	if statErr == nil {
		prScanCacheMu.Lock()
		if e, ok := prScanCache[path]; ok && e.size == info.Size() && e.mtime.Equal(info.ModTime()) && e.prefix == prefix {
			prScanCacheMu.Unlock()
			return e.urls
		}
		prScanCacheMu.Unlock()
	}

	urls := scanTranscriptPRURLs(path, prefix)

	if statErr == nil {
		prScanCacheMu.Lock()
		prScanCache[path] = prScanCacheEntry{size: info.Size(), mtime: info.ModTime(), prefix: prefix, urls: urls}
		prScanCacheMu.Unlock()
	}
	return urls
}

// zeusTranscriptPath resolves a job's zeus transcript the same way the board
// does (api/board.go's cellZeusTranscript) — jobs sits below api in the
// package DAG and can't import it, so this reimplements the same two-step
// lookup against the same core primitives rather than inventing a new one:
// prefer the live zeus peer's transcript (core.ReadTranscriptPath), falling
// back to the job's own stamped session id (core.TranscriptPathForSession)
// for a done/dead cell whose pane is already gone. "" when neither resolves
// (fresh fork with no transcript yet, session gone, no session ever stamped)
// — the caller scans nothing, same as any other unreadable-file case.
func zeusTranscriptPath(j *Job, all []*core.Peer) string {
	if zeus := CellZeus(j, all); zeus != nil {
		if path, err := core.ReadTranscriptPath(zeus); err == nil {
			return path
		}
	}
	return core.TranscriptPathForSession(j.Session)
}
