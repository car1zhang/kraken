package store

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

// TestConcurrentOpenBothSucceed opens the same fresh DB path from two
// goroutines at once. Without _txlock=immediate (see Open), mattn's default
// deferred BEGIN lets both migrate() calls read PRAGMA user_version before
// either has taken a write lock, then race to apply schemaV1 — one of them
// can lose with a "database is locked"/SQLITE_BUSY that _busy_timeout alone
// doesn't fully cover for a lock acquired only at the write statement. With
// the immediate lock, the loser simply waits for the winner's transaction
// (bounded by _busy_timeout), then re-reads user_version inside its own
// transaction and no-ops.
func TestConcurrentOpenBothSucceed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kraken.db")

	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	stores := make([]*Store, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s, err := Open(path)
			stores[i] = s
			errs[i] = err
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("Open %d: %v", i, err)
		}
	}
	for _, s := range stores {
		defer s.Close()
	}

	var version int
	if err := stores[0].db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("user_version = %d, want %d", version, schemaVersion)
	}

	// every handle must agree on the same, single copy of the data — a lost
	// race that silently re-ran schemaV1 twice would fail on the second
	// CREATE TABLE rather than no-op.
	if _, err := stores[0].CreateJob(&Job{Slug: "post-race", Type: "build"}); err != nil {
		t.Fatalf("CreateJob after concurrent open: %v", err)
	}
}

// TestConcurrentMixedOps drives SaveUpload, UpdateJobFields (with
// ErrStaleRev retry), and ListJobs concurrently against one Store backed by
// a temp file, under -race. Every upload name must be unique (SaveUpload's
// next-sequential-name computation must not race with itself), and the
// final rev must equal the initial rev plus the number of updates that
// actually won their optimistic-concurrency check.
func TestConcurrentMixedOps(t *testing.T) {
	s := openTest(t)
	id, err := s.CreateJob(&Job{Slug: "concurrent", Type: "build"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	const n = 20
	var wg sync.WaitGroup
	var successCount int64
	var mu sync.Mutex
	names := map[string]bool{}

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()

			u, err := s.SaveUpload(id, "png", "image/png", []byte("x"))
			if err != nil {
				t.Errorf("SaveUpload %d: %v", i, err)
				return
			}
			mu.Lock()
			if names[u.Name] {
				t.Errorf("duplicate upload name %q", u.Name)
			}
			names[u.Name] = true
			mu.Unlock()

			for {
				j, err := s.GetJobByID(id)
				if err != nil {
					t.Errorf("GetJobByID %d: %v", i, err)
					return
				}
				_, err = s.UpdateJobFields(id, j.Rev, map[string]any{"body": fmt.Sprintf("upd-%d", i)})
				if err == nil {
					atomic.AddInt64(&successCount, 1)
					break
				}
				if errors.Is(err, ErrStaleRev) {
					continue // lost the race — reread rev and retry, as callers must
				}
				t.Errorf("UpdateJobFields %d: %v", i, err)
				return
			}

			if _, err := s.ListJobs(); err != nil {
				t.Errorf("ListJobs %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	if len(names) != n {
		t.Fatalf("expected %d unique upload names, got %d: %+v", n, len(names), names)
	}
	final, err := s.GetJobByID(id)
	if err != nil {
		t.Fatalf("GetJobByID: %v", err)
	}
	if final.Rev != 1+successCount {
		t.Fatalf("final rev = %d, want %d (1 + %d successful updates)", final.Rev, 1+successCount, successCount)
	}
}
