package jobs

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// daemon_test.go — the PR tracker's pure reconciliation. The tracker's whole
// safety story is here: recorded history never shrinks, notifications fire
// exactly on the transition to merged, and an unchanged fetch is a no-op.

func TestAutoArchivePass(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	stale := writeArchiveFixture(t, PlutoDir(), "stale.job.md", "job: stale\nrepo: x\nready: true\nstate: done\n")
	old := time.Now().Add(-25 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	writeArchiveFixture(t, PlutoDir(), "fresh.job.md", "job: fresh\nrepo: x\nready: true\nstate: done\n")

	jobs := LoadJobs()
	archived := autoArchivePass(jobs, map[string]bool{}, time.Now())

	if len(archived) != 1 || archived[0] != "stale" {
		t.Fatalf("archived: got %v, want [stale]", archived)
	}
	if _, err := os.Stat(filepath.Join(ArchiveDir(), "stale.job.md")); err != nil {
		t.Fatal("stale job didn't land in archive/")
	}
	if _, err := os.Stat(filepath.Join(PlutoDir(), "fresh.job.md")); err != nil {
		t.Fatal("fresh done job was archived too early")
	}
	remaining := LoadJobs()
	if len(remaining) != 1 || remaining[0].Slug != "fresh" {
		t.Fatalf("active jobs after sweep: got %+v, want just fresh", remaining)
	}
}

func TestReconcilePRs(t *testing.T) {
	pr := func(n, state string) JobPR { return JobPR{URL: "https://x/pull/" + n, State: state} }
	cases := []struct {
		name        string
		recorded    []JobPR
		fetched     []JobPR
		want        []JobPR
		wantMerged  []string
		wantChanged bool
	}{
		{
			name:        "first sight of an open PR appends",
			fetched:     []JobPR{pr("1", "open")},
			want:        []JobPR{pr("1", "open")},
			wantChanged: true,
		},
		{
			name:        "unchanged fetch is a no-op",
			recorded:    []JobPR{pr("1", "open")},
			fetched:     []JobPR{pr("1", "open")},
			want:        []JobPR{pr("1", "open")},
			wantChanged: false,
		},
		{
			name:        "open to merged transitions once",
			recorded:    []JobPR{pr("1", "open")},
			fetched:     []JobPR{pr("1", "merged")},
			want:        []JobPR{pr("1", "merged")},
			wantMerged:  []string{"https://x/pull/1"},
			wantChanged: true,
		},
		{
			name:        "first sight already merged still notifies",
			fetched:     []JobPR{pr("1", "merged")},
			want:        []JobPR{pr("1", "merged")},
			wantMerged:  []string{"https://x/pull/1"},
			wantChanged: true,
		},
		{
			name:        "recorded history survives a fetch that no longer lists it",
			recorded:    []JobPR{pr("1", "merged")},
			fetched:     []JobPR{pr("2", "open")},
			want:        []JobPR{pr("1", "merged"), pr("2", "open")},
			wantChanged: true,
		},
		{
			name:        "closed updates without a merge notification",
			recorded:    []JobPR{pr("1", "open")},
			fetched:     []JobPR{pr("1", "closed")},
			want:        []JobPR{pr("1", "closed")},
			wantChanged: true,
		},
		{
			name:        "already merged never re-notifies",
			recorded:    []JobPR{pr("1", "merged")},
			fetched:     []JobPR{pr("1", "merged")},
			want:        []JobPR{pr("1", "merged")},
			wantChanged: false,
		},
	}
	for _, c := range cases {
		got, merged, changed := reconcilePRs(c.recorded, c.fetched)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: prs %+v, want %+v", c.name, got, c.want)
		}
		if !reflect.DeepEqual(merged, c.wantMerged) {
			t.Errorf("%s: newlyMerged %v, want %v", c.name, merged, c.wantMerged)
		}
		if changed != c.wantChanged {
			t.Errorf("%s: changed %v, want %v", c.name, changed, c.wantChanged)
		}
	}
}
