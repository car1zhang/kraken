package main

import (
	"os"
	"sync/atomic"
)

// selfcheck detects a stale running binary: make install replaces the
// installed file via atomic rename, but a process that's already running
// keeps its old inode and old code (see CLAUDE.md's install/deploy note) —
// it silently keeps serving pre-fix behavior until restarted. k web and k
// daemon each record their own executable's identity at startup and re-stat
// it on every health/board build or tick; this only surfaces the condition
// (board banner, daemon status field) and never auto-restarts anything.

var (
	selfBinaryPath  string
	selfBinaryStart os.FileInfo
	staleWarned     atomic.Bool
)

// initSelfCheck records the running executable's identity. Best-effort: any
// failure leaves selfBinaryStart nil, so isSelfStale reads false forever —
// a missing baseline is never an error (defensive-read convention).
func initSelfCheck() {
	path, err := os.Executable()
	if err != nil {
		return
	}
	fi, err := os.Stat(path)
	if err != nil {
		return
	}
	selfBinaryPath, selfBinaryStart = path, fi
}

// binaryStale is the pure comparison behind isSelfStale, split out so it's
// testable without shelling out or touching the filesystem: either side nil
// (no recorded baseline, or a failed re-stat) reads as not-stale.
func binaryStale(start, now os.FileInfo) bool {
	if start == nil || now == nil {
		return false
	}
	return !start.ModTime().Equal(now.ModTime()) || start.Size() != now.Size()
}

// isSelfStale re-stats the running binary's own path and compares it against
// the identity recorded at startup. Called concurrently from net/http
// handlers and the daemon tick, so the once-only log uses atomic.Bool's
// CompareAndSwap rather than a plain bool. Logs once on first detection,
// never again — this is a surface-only signal, not an error condition.
func isSelfStale() bool {
	if selfBinaryStart == nil {
		return false
	}
	now, err := os.Stat(selfBinaryPath)
	if err != nil {
		return false
	}
	stale := binaryStale(selfBinaryStart, now)
	if stale && staleWarned.CompareAndSwap(false, true) {
		dlog("binary on disk changed since this process started (reinstalled?) — restart to pick up the new build")
	}
	return stale
}
