package core

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"sync"
	"time"
)

// wakeup.go — detects a claude session mid poll-loop (e.g. /loop,
// /poll-pr): such a loop is idle between ticks and would otherwise falsely
// read as "waiting" off its session file, same as a claude genuinely sitting
// at its prompt. Every loop iteration ends its turn with a ScheduleWakeup
// tool_use in the session transcript, and the LAST one always supersedes
// earlier ones — mirrors jobs/status.go's pending-question scan (newest
// tool_use wins; cache split from the time-sensitive verdict).

// A Wakeup is a pending ScheduleWakeup call driving a "polling" status. It
// goes over the wire verbatim to the board/iOS app, hence the exact json tags.
type Wakeup struct {
	Reason       string `json:"reason"`
	DelaySeconds int    `json:"delay_seconds"`
	FiresAt      int64  `json:"fires_at"` // unix millis
}

// wakeupGrace covers the lag between a wakeup firing and kraken's next
// discovery pass re-invoking off it: a wakeup still counts as pending for
// this long past its FiresAt. Past that, the loop was interrupted or killed
// rather than merely running a little behind, and it reads as no wakeup.
const wakeupGrace = 120 * time.Second

// wakeupEvent is the tiny slice of a transcript jsonl line FindPendingWakeup
// cares about: an assistant tool_use, possibly ScheduleWakeup.
type wakeupEvent struct {
	Type        string `json:"type"`
	Timestamp   string `json:"timestamp"`
	IsSidechain bool   `json:"isSidechain"`
	Message     struct {
		Content []struct {
			Type  string          `json:"type"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	} `json:"message"`
}

// wakeupInput is a ScheduleWakeup tool_use's decoded input. A loop ends by
// calling it with Stop:true and every other field absent; delaySeconds is
// clamped to [60,3600] by the harness but may decode as a JSON float.
type wakeupInput struct {
	DelaySeconds float64 `json:"delaySeconds"`
	Reason       string  `json:"reason"`
	Stop         bool    `json:"stop"`
}

// wakeupCacheEntry is the memoized SCAN result: everything derivable from
// the transcript's bytes alone, keyed on (size, mtime) like discover.go's
// cwdCache. Deliberately excludes any now-vs-firesAt verdict — that changes
// as time passes even though the file (and therefore the cache key) does
// not, so it's recomputed on every FindPendingWakeup call instead of baked
// into what's stored.
type wakeupCacheEntry struct {
	size    int64
	mtime   time.Time
	found   bool
	stop    bool
	reason  string
	delay   int
	firesAt int64 // unix millis; meaningful only when found && !stop
}

var (
	wakeupCacheMu sync.Mutex
	wakeupCache   = map[string]wakeupCacheEntry{}
)

// FindPendingWakeup scans a session transcript for a ScheduleWakeup call
// that is still pending as of now: found, not a stop:true call, and within
// wakeupGrace of its computed fire time. Lenient throughout — an unreadable
// file, malformed lines, or no ScheduleWakeup call at all all read as no
// wakeup, never an error; this is another program's file format.
func FindPendingWakeup(transcript string, now time.Time) *Wakeup {
	info, statErr := os.Stat(transcript)
	if statErr != nil {
		return nil
	}

	wakeupCacheMu.Lock()
	e, ok := wakeupCache[transcript]
	wakeupCacheMu.Unlock()
	if !ok || e.size != info.Size() || !e.mtime.Equal(info.ModTime()) {
		e = scanWakeup(transcript)
		e.size = info.Size()
		e.mtime = info.ModTime()
		wakeupCacheMu.Lock()
		wakeupCache[transcript] = e
		wakeupCacheMu.Unlock()
	}

	if !e.found || e.stop {
		return nil
	}
	if !now.Before(time.UnixMilli(e.firesAt).Add(wakeupGrace)) {
		return nil // stale: fired (or was due) long enough ago that the loop must have died
	}
	return &Wakeup{Reason: e.reason, DelaySeconds: e.delay, FiresAt: e.firesAt}
}

// scanWakeup does the actual line-by-line transcript scan behind
// FindPendingWakeup's cache. Remembers only the LAST ScheduleWakeup tool_use
// seen: each new one supersedes the last, since only the most recent call
// still governs the session's status. A malformed newer call (bad input, or
// an event with no usable top-level timestamp) still resets the tracked
// state to not-found, so it can never leave an older good call looking
// pending — same "newest always wins" principle as scanPendingQuestion.
func scanWakeup(transcript string) wakeupCacheEntry {
	f, err := os.Open(transcript)
	if err != nil {
		return wakeupCacheEntry{}
	}
	defer f.Close()

	var e wakeupCacheEntry
	sc := bufio.NewScanner(f)
	// transcript lines can run to several MB (large tool inputs/outputs
	// inline); bufio's 64KB default silently truncates scanning, so give it
	// real headroom.
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var ev wakeupEvent
		if json.Unmarshal(line, &ev) != nil {
			continue // malformed line: skip it, don't lose the rest of the transcript
		}
		if ev.Type != "assistant" {
			continue
		}
		if ev.IsSidechain {
			continue // subagent traffic — count only the peer's own blocks, same filter as read.go's parseBlocks
		}
		for _, c := range ev.Message.Content {
			if c.Type != "tool_use" || c.Name != "ScheduleWakeup" {
				continue
			}
			ts, tErr := time.Parse(time.RFC3339, ev.Timestamp)
			if tErr != nil {
				// no usable event time to anchor a fire time to: fail closed
				// rather than guess one
				e = wakeupCacheEntry{}
				continue
			}
			var in wakeupInput
			if json.Unmarshal(c.Input, &in) != nil {
				e = wakeupCacheEntry{} // malformed input on the newest call: supersede, but nothing usable to report
				continue
			}
			delay := int(in.DelaySeconds)
			e = wakeupCacheEntry{
				found:   true,
				stop:    in.Stop,
				reason:  in.Reason,
				delay:   delay,
				firesAt: ts.UnixMilli() + int64(delay)*1000,
			}
		}
	}
	if sc.Err() != nil {
		// scan failed partway (e.g. a line over the buffer max): fail closed
		// rather than risk reporting a phantom pending wakeup off a
		// truncated scan.
		return wakeupCacheEntry{}
	}
	return e
}
