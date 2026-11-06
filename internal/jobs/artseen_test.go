package jobs

import (
	"fmt"
	"testing"
)

// artseen_test.go — the operator's artifact reads (artseen.go): a read marks
// the artifact seen at that moment, an artifact changed after its last read
// is new again, one job's reads never reach another job's record, and a row
// over the cap drops the reads made longest ago.

func TestArtifactIsNewAgainstTheRecord(t *testing.T) {
	seen := ArtifactSeen{"00-overview.md": 1_000}
	cases := []struct {
		name  string
		mtime int64
		want  bool
	}{
		{"00-overview.md", 999, false},   // changed before the read
		{"00-overview.md", 1_000, false}, // changed at the read: not after it
		{"00-overview.md", 1_001, true},  // changed after the read
		{"90-build-report.md", 5, true},  // never read
		{"90-build-report.md", 0, false}, // no mtime known: never claimed new
		{"00-overview.md", 0, false},
	}
	for _, c := range cases {
		if got := ArtifactIsNew(seen, c.name, c.mtime); got != c.want {
			t.Errorf("ArtifactIsNew(%q, %d) = %v, want %v", c.name, c.mtime, got, c.want)
		}
	}
	if ArtifactIsNew(nil, "x.md", 7) != true {
		t.Error("a nil record must read every stamped artifact as new")
	}
}

func TestMarkArtifactSeenRoundTripsThroughTheIndex(t *testing.T) {
	st := useTestStore(t)
	j := &Job{ID: 41, Slug: "seen"}
	other := &Job{ID: 42, Slug: "other"}
	markArtifactSeenAt(j, "00-overview.md", 500)
	markArtifactSeenAt(j, "sub dir/a#b.png", 700)
	markArtifactSeenAt(other, "00-overview.md", 900)

	idx := ArtifactSeenIndex(st, nil)
	if got := idx[41]["00-overview.md"]; got != 500 {
		t.Errorf("job 41 overview read at %d, want 500", got)
	}
	if got := idx[41]["sub dir/a#b.png"]; got != 700 {
		t.Errorf("job 41 nested read at %d, want 700", got)
	}
	if got := idx[42]["00-overview.md"]; got != 900 {
		t.Errorf("job 42 overview read at %d, want 900 — the records are per job", got)
	}
	if _, ok := idx[42]["sub dir/a#b.png"]; ok {
		t.Error("job 41's read leaked into job 42's record")
	}
	// a second read moves the stamp forward, so an artifact rewritten after
	// the first read and opened again stops being new
	markArtifactSeenAt(j, "00-overview.md", 1_200)
	idx = ArtifactSeenIndex(st, nil)
	if ArtifactIsNew(idx[41], "00-overview.md", 1_100) {
		t.Error("an artifact opened after its change must not read as new")
	}
	if !ArtifactIsNew(idx[41], "00-overview.md", 1_300) {
		t.Error("an artifact changed after its latest read must read as new")
	}
}

func TestMarkArtifactSeenIsANoOpWithoutAJobOrStore(t *testing.T) {
	// no store wired: nothing to write to, and nothing panics
	UseStore(nil)
	markArtifactSeenAt(&Job{ID: 1, Slug: "x"}, "a.md", 1)
	st := useTestStore(t)
	markArtifactSeenAt(&Job{Slug: "fileonly"}, "a.md", 1) // no id: no row to key on
	markArtifactSeenAt(nil, "a.md", 1)
	markArtifactSeenAt(&Job{ID: 7}, "", 1)
	if idx := ArtifactSeenIndex(st, nil); len(idx) != 0 {
		t.Errorf("index = %v, want nothing recorded", idx)
	}
}

func TestArtifactSeenIndexSkipsARowThatDoesNotParse(t *testing.T) {
	st := useTestStore(t)
	if err := st.SetKV(artifactsSeenKey(3), "not json"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetKV(artifactsSeenPrefix+"abc", `{"a.md":1}`); err != nil {
		t.Fatal(err)
	}
	markArtifactSeenAt(&Job{ID: 4}, "b.md", 2)
	idx := ArtifactSeenIndex(st, nil)
	if _, ok := idx[3]; ok {
		t.Error("a row that does not parse must be skipped, not served")
	}
	if got := idx[4]["b.md"]; got != 2 {
		t.Errorf("job 4 read at %d, want 2", got)
	}
	if len(idx) != 1 {
		t.Errorf("index has %d jobs, want the one good row", len(idx))
	}
	// the caller's keep filter skips a row before it is parsed: the corrupt
	// row 3 is neither parsed nor logged, and row 4 is kept only when asked
	if got := ArtifactSeenIndex(st, func(id int64) bool { return id == 4 }); len(got) != 1 || got[4]["b.md"] != 2 {
		t.Errorf("filtered index = %v, want job 4 alone", got)
	}
	if got := ArtifactSeenIndex(st, func(id int64) bool { return false }); len(got) != 0 {
		t.Errorf("index with nothing kept = %v, want empty", got)
	}
	// a corrupt row is replaced by the next mark rather than wedging the job
	markArtifactSeenAt(&Job{ID: 3}, "c.md", 9)
	if got := ArtifactSeenIndex(st, nil)[3]["c.md"]; got != 9 {
		t.Errorf("after replacing the corrupt row, job 3 read at %d, want 9", got)
	}
}

func TestMarkArtifactSeenEvictsTheOldestReadsPastTheCap(t *testing.T) {
	st := useTestStore(t)
	j := &Job{ID: 5}
	for i := 0; i < artifactsSeenMax; i++ {
		markArtifactSeenAt(j, fmt.Sprintf("gen-%04d.png", i), int64(10+i))
	}
	markArtifactSeenAt(j, "00-overview.md", 5) // older than every generated read, but just marked: kept
	seen := ArtifactSeenIndex(st, nil)[5]
	if len(seen) != artifactsSeenMax {
		t.Fatalf("record holds %d names, want the cap %d", len(seen), artifactsSeenMax)
	}
	if _, ok := seen["00-overview.md"]; !ok {
		t.Error("the read just marked was evicted; only older reads may go")
	}
	if _, ok := seen["gen-0000.png"]; ok {
		t.Error("the read made longest ago survived the eviction")
	}
	if _, ok := seen["gen-0001.png"]; !ok {
		t.Error("the second-oldest read was evicted; only one over the cap should go")
	}
}
