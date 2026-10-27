package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"kraken/internal/store"
)

// mindrun_daily_test.go — a daily run that FAILS rewinds the daily watermark
// to its own since, so the next daily run re-covers the window it never
// judged; the date stays, so nothing re-queues today; a failed done-trigger
// run leaves the watermark alone.

func mindDailyKV(t *testing.T, st *store.Store) mindDaily {
	t.Helper()
	raw, err := st.GetKV(mindDailyKVKey)
	if err != nil {
		t.Fatalf("daily watermark: %v", err)
	}
	var d mindDaily
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		t.Fatalf("daily watermark %q: %v", raw, err)
	}
	return d
}

func TestFailedDailyRunRewindsTheWatermark(t *testing.T) {
	st := edgeMREnv(t)
	edgeMRLaunch(t, func(ctx context.Context, stdin string) (string, string, error) {
		return "", "", errors.New("dns is down")
	})
	now := time.Now().UnixMilli()
	since := now - int64(2*time.Hour/time.Millisecond)
	today := time.Now().Format(dreamDateLayout)
	seed, _ := json.Marshal(mindDaily{Date: today, Until: now})
	if err := st.SetKV(mindDailyKVKey, string(seed)); err != nil {
		t.Fatal(err)
	}
	queued := contractMRRun(t, st, store.MindRun{Trigger: "daily", Since: since, Until: now})
	edgeMRPendingMark(t, st, "a mark, so the run reaches the launcher and fails there")
	claimed, err := st.ClaimNextMindRun(now)
	if err != nil || claimed == nil || claimed.ID != queued.ID {
		t.Fatalf("claim = (%+v, %v)", claimed, err)
	}
	if err := RunMindRun(st, *claimed); err == nil {
		t.Fatal("RunMindRun with a dead launcher = nil error")
	}
	got := mindDailyKV(t, st)
	if got.Until != since {
		t.Fatalf("watermark until = %d after the failure, want the run's since %d", got.Until, since)
	}
	if got.Date != today {
		t.Fatalf("watermark date = %q, want %q unchanged so nothing re-queues today", got.Date, today)
	}
	// and the next day's enqueue starts where the failed run started
	if id, ok := kickMindDaily(st, time.Now().Add(24*time.Hour)); !ok {
		t.Fatal("the next day's daily run must be queued")
	} else if r := edgeMRGetRun(t, st, id); r.Since != since {
		t.Fatalf("next daily run since = %d, want %d", r.Since, since)
	}
}

func TestFailedDoneRunLeavesTheDailyWatermark(t *testing.T) {
	st := edgeMREnv(t)
	edgeMRLaunch(t, func(ctx context.Context, stdin string) (string, string, error) {
		return "", "", errors.New("dns is down")
	})
	now := time.Now().UnixMilli()
	seed, _ := json.Marshal(mindDaily{Date: time.Now().Format(dreamDateLayout), Until: now})
	if err := st.SetKV(mindDailyKVKey, string(seed)); err != nil {
		t.Fatal(err)
	}
	j := mustCreateJob(t, st, store.Job{Slug: "operator-filed", Repo: "kraken", Body: "please build the thing", Author: "operator", State: "done", CreatedAt: now - 5000, UpdatedAt: now - 5000})
	queued := contractMRRun(t, st, store.MindRun{JobID: j.ID, Slug: j.Slug, Trigger: "done", Since: now - 5000, Until: now})
	claimed, err := st.ClaimNextMindRun(now)
	if err != nil || claimed == nil || claimed.ID != queued.ID {
		t.Fatalf("claim = (%+v, %v)", claimed, err)
	}
	if err := RunMindRun(st, *claimed); err == nil {
		t.Fatal("RunMindRun with a dead launcher = nil error")
	}
	if got := mindDailyKV(t, st); got.Until != now {
		t.Fatalf("a failed done run moved the daily watermark to %d", got.Until)
	}
}
