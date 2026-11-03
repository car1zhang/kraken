package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// queuedTranscript writes invented transcript lines to a temp file. Every
// record shape here is made up to pin the parser; none is copied from a
// real session.
func queuedTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const (
	qEnqueueHandoff = `{"type":"queue-operation","operation":"enqueue","timestamp":"2026-01-02T03:04:05.000Z","content":"/handoff"}`
	qEnqueueNote    = `{"type":"queue-operation","operation":"enqueue","timestamp":"2026-01-02T03:04:06.000Z","content":"<task-notification>\n<task-id>t1</task-id>\n</task-notification>"}`
	qRemoveNote     = `{"type":"queue-operation","operation":"remove","timestamp":"2026-01-02T03:04:07.000Z","content":"<task-notification>\n<task-id>t1</task-id>\n</task-notification>","reason":"absorbed_mid_turn"}`
	qRemoveHandoff  = `{"type":"queue-operation","operation":"remove","timestamp":"2026-01-02T03:04:08.000Z","content":"/handoff","reason":"pulled_back"}`
	qPopAll         = `{"type":"queue-operation","operation":"popAll","timestamp":"2026-01-02T03:04:09.000Z","content":"/handoff"}`
	qDequeue        = `{"type":"queue-operation","operation":"dequeue","timestamp":"2026-01-02T03:05:00.000Z","content":""}`
	qAssistant      = `{"type":"assistant","timestamp":"2026-01-02T03:04:30.000Z","message":{"role":"assistant","content":[{"type":"text","text":"still working on the queue-operation of the day"}]}}`
	qLookalike      = `{"type":"system","subtype":"queue-operation","operation":"enqueue","timestamp":"2026-01-02T03:04:31.000Z","content":"/handoff"}`
)

func TestQueuedCommandPending(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
		want  bool
	}{
		{"enqueued, nothing after", []string{qAssistant, qEnqueueHandoff}, true},
		{"enqueued, turn still absorbing notifications", []string{qEnqueueHandoff, qEnqueueNote, qRemoveNote, qAssistant}, true},
		{"enqueued twice, still queued", []string{qEnqueueHandoff, qAssistant, qEnqueueHandoff}, true},
		{"dequeued", []string{qEnqueueHandoff, qAssistant, qDequeue}, false},
		{"dequeued then queued again", []string{qEnqueueHandoff, qDequeue, qAssistant, qEnqueueHandoff}, true},
		{"an item ahead of it dequeued", []string{qEnqueueNote, qEnqueueHandoff, qDequeue}, true},
		{"both dequeued in order", []string{qEnqueueNote, qEnqueueHandoff, qDequeue, qDequeue}, false},
		{"pulled back", []string{qEnqueueHandoff, qRemoveHandoff}, false},
		{"another item removed leaves it queued", []string{qEnqueueHandoff, qRemoveNote}, true},
		{"everything popped back into the input box", []string{qEnqueueNote, qEnqueueHandoff, qPopAll}, false},
		{"no queue records", []string{qAssistant}, false},
		{"a record of another type carrying the marker as a value", []string{qLookalike}, false},
		{"other command queued", []string{`{"type":"queue-operation","operation":"enqueue","timestamp":"2026-01-02T03:04:05.000Z","content":"/compact keep one line"}`}, false},
		{"handoff with arguments is another command", []string{`{"type":"queue-operation","operation":"enqueue","timestamp":"2026-01-02T03:04:05.000Z","content":"/handoff resume"}`}, false},
		{"malformed queue line skipped", []string{qEnqueueHandoff, `{"type":"queue-operation","operation":`, qDequeue}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := queuedTranscript(t, c.lines...)
			if got := QueuedCommandPending(p, "/handoff", 0); got != c.want {
				t.Fatalf("QueuedCommandPending = %v, want %v", got, c.want)
			}
		})
	}
}

// A process started after the enqueue holds an empty queue, so the dead
// process' record is skipped; one started before it reads the record.
func TestQueuedCommandPendingSinceBoundary(t *testing.T) {
	p := queuedTranscript(t, qEnqueueHandoff)
	enqueuedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC).UnixMilli()
	if QueuedCommandPending(p, "/handoff", enqueuedAt+1) {
		t.Fatal("an enqueue before the process started read as pending")
	}
	if !QueuedCommandPending(p, "/handoff", enqueuedAt-1) {
		t.Fatal("an enqueue after the process started did not read as pending")
	}
	if !QueuedCommandPending(p, "/handoff", enqueuedAt) {
		t.Fatal("an enqueue at the process start did not read as pending")
	}
}

// A line over the scanner ceiling stops the scan short; the queue seen so
// far is not evidence of anything still queued, so the answer is false.
func TestQueuedCommandPendingScanStopsShort(t *testing.T) {
	huge := `{"type":"user","timestamp":"2026-01-02T03:04:40.000Z","message":{"role":"user","content":"` + strings.Repeat("x", 17*1024*1024) + `"}}`
	p := queuedTranscript(t, qEnqueueHandoff, huge, qDequeue)
	if QueuedCommandPending(p, "/handoff", 0) {
		t.Fatal("a scan that stopped before the dequeue read as pending")
	}
}

// The replay is memoized on the file's size and mtime and on the boundary:
// an appended record invalidates it, and so does a new boundary.
func TestQueuedCommandPendingCache(t *testing.T) {
	p := queuedTranscript(t, qEnqueueHandoff)
	if !QueuedCommandPending(p, "/handoff", 0) {
		t.Fatal("first read: want pending")
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(qDequeue + "\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if QueuedCommandPending(p, "/handoff", 0) {
		t.Fatal("after the dequeue was appended: still pending")
	}
	later := time.Date(2026, 1, 2, 4, 0, 0, 0, time.UTC).UnixMilli()
	if QueuedCommandPending(p, "/handoff", later) {
		t.Fatal("a boundary after every record: want not pending")
	}
}

func TestQueuedCommandPendingLenient(t *testing.T) {
	if QueuedCommandPending("", "/handoff", 0) {
		t.Fatal("empty path read as pending")
	}
	if QueuedCommandPending(filepath.Join(t.TempDir(), "missing.jsonl"), "/handoff", 0) {
		t.Fatal("missing file read as pending")
	}
	p := queuedTranscript(t, qEnqueueHandoff)
	if QueuedCommandPending(p, "", 0) {
		t.Fatal("empty command read as pending")
	}
}
