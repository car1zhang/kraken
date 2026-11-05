package core

import (
	"encoding/json"
	"os"
	"strings"
	"sync"
	"time"
)

// ransince.go — "did this agent run a turn after moment T", answered from
// its transcript. The daemon's roll call samples a cell once per tick, so a
// turn that starts and ends between two ticks is never seen as working; the
// transcript is the record that it happened.

// ranSinceWindow is how much of the transcript's tail TranscriptRanSince
// reads first. A turn's assistant records are the last things written, so the
// evidence is normally at the very end; the window only has to clear the
// bookkeeping records Claude Code writes after them.
//
// ranSinceMaxWindow is how far it grows when that tail holds no timestamped
// assistant record at all, which is what one record larger than the window
// looks like: tailWindow drops the leading partial line, so an oversized last
// record is never parsed and the window reads as empty of evidence.
const (
	ranSinceWindow    = 512 << 10
	ranSinceMaxWindow = 8 << 20
)

// ranSinceRecord is the slice of a transcript line TranscriptRanSince reads.
type ranSinceRecord struct {
	Type        string `json:"type"`
	Timestamp   string `json:"timestamp"`
	IsSidechain bool   `json:"isSidechain"`
}

type ranSinceEntry struct {
	size  int64
	mtime time.Time
	since int64
	ran   bool
}

// ranSinceCache remembers the last answer per path, keyed on the file's
// (size, mtime) and the moment asked about: a transcript that has not moved
// costs one stat per call, never a second read of its tail.
var (
	ranSinceMu    sync.Mutex
	ranSinceCache = map[string]ranSinceEntry{}
)

// TranscriptRanSince reports whether the transcript at path records the
// agent producing output after sinceMS (unix millis).
//
// For a claude transcript that is an ASSISTANT record of the main chain
// stamped later than sinceMS. A record merely being appended is not enough,
// which is why the file's mtime is only the fast path that says "nothing at
// all happened": a slash command typed into an idle pane, a hook's
// attachment and Claude Code's own bookkeeping lines all move the mtime
// without the agent having run.
//
// A codex rollout has no such record vocabulary here, so its mtime moving
// past sinceMS is the answer.
//
// Every unreadable input (no path, a missing file, a failed read) is false:
// the caller acts on a true, and a transcript that cannot be read is not
// evidence of anything.
func TranscriptRanSince(path string, sinceMS int64) bool {
	if path == "" || sinceMS <= 0 {
		return false
	}
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	if info.ModTime().UnixMilli() <= sinceMS {
		return false
	}
	if IsCodexRollout(path) {
		return true
	}

	ranSinceMu.Lock()
	e, ok := ranSinceCache[path]
	ranSinceMu.Unlock()
	if ok && e.size == info.Size() && e.mtime.Equal(info.ModTime()) && e.since == sinceMS {
		return e.ran
	}

	// Grow the window until some assistant record decides the question, or
	// the whole file (or the cap) has been read.
	ran := false
	for window := int64(ranSinceWindow); ; window *= 4 {
		var decided bool
		ran, decided = assistantRecordSince(tailLines(path, window), sinceMS)
		if decided || window >= info.Size() || window >= ranSinceMaxWindow {
			break
		}
	}

	ranSinceMu.Lock()
	if len(ranSinceCache) >= 1024 {
		ranSinceCache = map[string]ranSinceEntry{}
	}
	ranSinceCache[path] = ranSinceEntry{size: info.Size(), mtime: info.ModTime(), since: sinceMS, ran: ran}
	ranSinceMu.Unlock()
	return ran
}

// assistantRecordSince scans transcript lines newest first for a main-chain
// assistant record stamped after sinceMS. It stops at the first timestamped
// main-chain assistant record it meets, whichever way that record answers:
// assistant records are written in order, so if the newest one is not after
// sinceMS no older one is. Other record kinds are skipped without stopping,
// since they carry no timestamp or one that is not in step with the assistant
// records around them.
//
// decided is false when the lines held no such record at all, which tells
// the caller a larger window might.
func assistantRecordSince(lines []string, sinceMS int64) (ran, decided bool) {
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		if !strings.Contains(line, `"assistant"`) {
			continue
		}
		var rec ranSinceRecord
		if json.Unmarshal([]byte(line), &rec) != nil {
			continue
		}
		if rec.Type != "assistant" || rec.IsSidechain {
			continue
		}
		ts := parseRecordTime(rec.Timestamp)
		if ts == 0 {
			continue
		}
		return ts > sinceMS, true
	}
	return false, false
}
