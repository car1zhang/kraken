package core

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"sync"
	"time"
)

// queued.go — reads a claude session's INPUT QUEUE out of its transcript.
//
// Text typed into a claude pane while a turn is running is not acted on: the
// CLI queues it and runs it when the turn ends, writing one
// `queue-operation` record per queue move into the session transcript —
// `enqueue` (with the typed text as `content`), `dequeue` (content-less, at
// the moment the head item runs), `remove` (an item dropped unrun, which is
// how a background task's notification is absorbed mid-turn, and how the
// operator pulls one queued message back), and `popAll` (every queued item
// pulled back into the input box at once). The daemon types `/handoff` into
// a pane through exactly that channel, so whether its command has run yet
// is a fact of the transcript rather than of the pane: a pane whose turn is
// a foreground wait loop reads idle to the pane probe while the typed
// command sits queued behind it.

// queuedEvent is the slice of a transcript line the queue replay reads.
type queuedEvent struct {
	Type      string `json:"type"`
	Operation string `json:"operation"`
	Content   string `json:"content"`
	Timestamp string `json:"timestamp"`
}

// queuedCacheEntry memoizes one transcript's replay, keyed on (size, mtime,
// since) like discover.go's cwdCache: the scan re-runs only when the file
// or the boundary changed.
type queuedCacheEntry struct {
	size    int64
	mtime   time.Time
	since   int64
	pending bool
}

var (
	queuedCacheMu sync.Mutex
	queuedCache   = map[string]queuedCacheEntry{}
)

// QueuedCommandPending reports whether cmd — typed into the session whose
// transcript this is — is still waiting in the session's input queue. The
// queue is replayed as the FIFO it is: an enqueue appends its content, a
// dequeue pops the head, a remove deletes the first entry with that
// content, and a popAll empties it; cmd is pending when it is still in the
// queue at the end of the file. Records timestamped before since (unix
// millis; zero means no boundary) are skipped, because the queue lives in
// the process' memory: a claude killed and resumed on the same session id
// starts with an empty queue, and the dead process' enqueue would otherwise
// read as pending until some unrelated dequeue happened to clear it. Pass
// the process' start (Peer.StartedAt).
//
// Lenient throughout, in the direction of "not pending": an unreadable
// file, a malformed line, a scan that stops short (a line over the buffer
// ceiling), or no queue records at all read as false, which leaves the
// caller exactly where it was before this function existed.
func QueuedCommandPending(transcript, cmd string, since int64) bool {
	if transcript == "" || cmd == "" {
		return false
	}
	info, err := os.Stat(transcript)
	if err != nil {
		return false
	}
	queuedCacheMu.Lock()
	e, ok := queuedCache[transcript]
	queuedCacheMu.Unlock()
	if ok && e.size == info.Size() && e.mtime.Equal(info.ModTime()) && e.since == since {
		return e.pending
	}
	pending := replayQueue(transcript, cmd, since)
	queuedCacheMu.Lock()
	queuedCache[transcript] = queuedCacheEntry{size: info.Size(), mtime: info.ModTime(), since: since, pending: pending}
	queuedCacheMu.Unlock()
	return pending
}

// replayQueue is the line-by-line scan behind QueuedCommandPending's cache.
func replayQueue(transcript, cmd string, since int64) bool {
	f, err := os.Open(transcript)
	if err != nil {
		return false
	}
	defer f.Close()

	var queue []string
	sc := bufio.NewScanner(f)
	// transcript lines can run to several MB (large tool inputs/outputs
	// inline); bufio's 64KB default silently truncates scanning, so give it
	// the same headroom scanWakeup does.
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if !bytes.Contains(line, []byte(`"queue-operation"`)) {
			continue
		}
		var ev queuedEvent
		if json.Unmarshal(line, &ev) != nil || ev.Type != "queue-operation" {
			continue
		}
		if since > 0 {
			ts, err := time.Parse(time.RFC3339Nano, ev.Timestamp)
			if err != nil || ts.UnixMilli() < since {
				continue
			}
		}
		switch ev.Operation {
		case "enqueue":
			queue = append(queue, ev.Content)
		case "dequeue":
			if len(queue) > 0 {
				queue = queue[1:]
			}
		case "remove":
			for i, c := range queue {
				if c == ev.Content {
					queue = append(queue[:i], queue[i+1:]...)
					break
				}
			}
		case "popAll":
			queue = nil
		}
	}
	if sc.Err() != nil {
		// a line over the ceiling stops the scan short of the file's end,
		// and whatever the queue held at that point is not evidence of
		// anything still queued now.
		return false
	}
	for _, c := range queue {
		if c == cmd {
			return true
		}
	}
	return false
}
