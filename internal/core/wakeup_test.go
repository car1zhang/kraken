package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeWakeupTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sess.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// baseTimestamp / baseTime are the same instant in two forms: the RFC3339
// string as it appears in a transcript line, and the parsed time.Time used
// to compute expected fire times / drive `now` in assertions.
const baseTimestamp = "2026-07-21T21:34:57.209Z"

var baseTime = func() time.Time {
	tm, err := time.Parse(time.RFC3339, baseTimestamp)
	if err != nil {
		panic(err)
	}
	return tm
}()

func scheduleWakeupLine(timestamp, input string) string {
	return `{"type":"assistant","timestamp":"` + timestamp + `","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"ScheduleWakeup","input":` + input + `}]}}`
}

func sidechainScheduleWakeupLine(timestamp, input string) string {
	return `{"type":"assistant","timestamp":"` + timestamp + `","isSidechain":true,"message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"ScheduleWakeup","input":` + input + `}]}}`
}

func TestFindPendingWakeupPending(t *testing.T) {
	path := writeWakeupTranscript(t, scheduleWakeupLine(baseTimestamp, `{"delaySeconds":240,"reason":"Waiting on PR #8069 CI checks (4 running)"}`))
	w := FindPendingWakeup(path, baseTime)
	if w == nil {
		t.Fatal("want a pending wakeup, got nil")
	}
	if w.Reason != "Waiting on PR #8069 CI checks (4 running)" {
		t.Errorf("reason: %q", w.Reason)
	}
	if w.DelaySeconds != 240 {
		t.Errorf("delay seconds: %d", w.DelaySeconds)
	}
	wantFiresAt := baseTime.Add(240 * time.Second).UnixMilli()
	if w.FiresAt != wantFiresAt {
		t.Errorf("fires at: got %d, want %d", w.FiresAt, wantFiresAt)
	}
}

func TestFindPendingWakeupStopTrue(t *testing.T) {
	path := writeWakeupTranscript(t, scheduleWakeupLine(baseTimestamp, `{"stop":true}`))
	if w := FindPendingWakeup(path, baseTime); w != nil {
		t.Errorf("want no wakeup for stop:true, got %+v", w)
	}
}

func TestFindPendingWakeupNewerSupersedesOlder(t *testing.T) {
	older := scheduleWakeupLine(baseTimestamp, `{"delaySeconds":60,"reason":"first tick"}`)
	newerTimestamp := "2026-07-21T21:40:00.000Z"
	newer := scheduleWakeupLine(newerTimestamp, `{"delaySeconds":300,"reason":"second tick"}`)
	path := writeWakeupTranscript(t, older, newer)

	newerTime, err := time.Parse(time.RFC3339, newerTimestamp)
	if err != nil {
		t.Fatal(err)
	}
	w := FindPendingWakeup(path, newerTime)
	if w == nil {
		t.Fatal("want a pending wakeup, got nil")
	}
	if w.Reason != "second tick" || w.DelaySeconds != 300 {
		t.Errorf("want the newer call's fields, got %+v", w)
	}
}

func TestFindPendingWakeupMalformedNewestInputYieldsNil(t *testing.T) {
	older := scheduleWakeupLine(baseTimestamp, `{"delaySeconds":60,"reason":"good older call"}`)
	newer := scheduleWakeupLine("2026-07-21T21:40:00.000Z", `"not-an-object"`) // valid JSON, but not a decodable ScheduleWakeup input
	path := writeWakeupTranscript(t, older, newer)
	if w := FindPendingWakeup(path, baseTime); w != nil {
		t.Errorf("malformed newest call must supersede the older good one and yield nil, got %+v", w)
	}
}

func TestFindPendingWakeupExpiredPastGrace(t *testing.T) {
	path := writeWakeupTranscript(t, scheduleWakeupLine(baseTimestamp, `{"delaySeconds":60,"reason":"tick"}`))
	firesAt := baseTime.Add(60 * time.Second)
	now := firesAt.Add(wakeupGrace).Add(time.Second) // just past grace
	if w := FindPendingWakeup(path, now); w != nil {
		t.Errorf("want stale wakeup to read as nil, got %+v", w)
	}
}

func TestFindPendingWakeupWithinGrace(t *testing.T) {
	path := writeWakeupTranscript(t, scheduleWakeupLine(baseTimestamp, `{"delaySeconds":60,"reason":"tick"}`))
	firesAt := baseTime.Add(60 * time.Second)
	now := firesAt.Add(wakeupGrace / 2) // past fire time but still within grace
	if w := FindPendingWakeup(path, now); w == nil {
		t.Error("want wakeup still pending within grace, got nil")
	}
}

func TestFindPendingWakeupMissingTimestamp(t *testing.T) {
	line := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"ScheduleWakeup","input":{"delaySeconds":60,"reason":"tick"}}]}}`
	path := writeWakeupTranscript(t, line)
	if w := FindPendingWakeup(path, baseTime); w != nil {
		t.Errorf("want nil with no top-level timestamp, got %+v", w)
	}
}

func TestFindPendingWakeupMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope.jsonl")
	if w := FindPendingWakeup(path, baseTime); w != nil {
		t.Errorf("want nil for missing file, got %+v", w)
	}
}

func TestFindPendingWakeupMalformedLinesSkipped(t *testing.T) {
	path := writeWakeupTranscript(t,
		`not even json`,
		`{"type":"assistant","message":{`, // truncated
		scheduleWakeupLine(baseTimestamp, `{"delaySeconds":60,"reason":"tick"}`),
		``,
	)
	w := FindPendingWakeup(path, baseTime)
	if w == nil {
		t.Fatal("want malformed lines skipped and the real call found, got nil")
	}
	if w.Reason != "tick" {
		t.Errorf("reason: %q", w.Reason)
	}
}

func TestFindPendingWakeupSidechainDoesNotSupersedeOwn(t *testing.T) {
	own := scheduleWakeupLine(baseTimestamp, `{"delaySeconds":60,"reason":"zeus own tick"}`)
	sidechainTimestamp := "2026-07-21T21:40:00.000Z"
	sidechain := sidechainScheduleWakeupLine(sidechainTimestamp, `{"delaySeconds":300,"reason":"subagent tick"}`)
	path := writeWakeupTranscript(t, own, sidechain)

	w := FindPendingWakeup(path, baseTime)
	if w == nil {
		t.Fatal("want zeus' own pending wakeup, got nil")
	}
	if w.Reason != "zeus own tick" || w.DelaySeconds != 60 {
		t.Errorf("want zeus' own event to survive a later sidechain call, got %+v", w)
	}
}

func TestFindPendingWakeupOnlySidechainYieldsNil(t *testing.T) {
	path := writeWakeupTranscript(t, sidechainScheduleWakeupLine(baseTimestamp, `{"delaySeconds":60,"reason":"subagent tick"}`))
	if w := FindPendingWakeup(path, baseTime); w != nil {
		t.Errorf("want nil when every ScheduleWakeup call is sidechain, got %+v", w)
	}
}

func TestFindPendingWakeupMemoizedByStat(t *testing.T) {
	path := writeWakeupTranscript(t, scheduleWakeupLine(baseTimestamp, `{"delaySeconds":60,"reason":"tick"}`))

	w1 := FindPendingWakeup(path, baseTime)
	w2 := FindPendingWakeup(path, baseTime)
	if w1 == nil || w2 == nil {
		t.Fatal("want both calls to find the wakeup")
	}
	if *w1 != *w2 {
		t.Errorf("want identical results from an unchanged file, got %+v vs %+v", w1, w2)
	}

	// The expiry verdict must still flip with `now` even though the file
	// (and thus the cache key) hasn't changed: the cache only memoizes the
	// scan, not the time-relative pending/stale check.
	firesAt := baseTime.Add(60 * time.Second)
	stale := firesAt.Add(wakeupGrace).Add(time.Second)
	if w3 := FindPendingWakeup(path, stale); w3 != nil {
		t.Errorf("want stale verdict for a later `now` against the same unchanged file, got %+v", w3)
	}
}
