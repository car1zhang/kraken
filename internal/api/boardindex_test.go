package api

import (
	"reflect"
	"testing"

	"kraken/internal/jobs"
	"kraken/internal/store"
)

// boardindex_test.go — the two whole-board indexes the board build reads its
// cards' feedback and stored artifact names from (feedbackIndex,
// artifactPathsIndex): each must answer exactly what the per-job read it
// replaced answers, for every job at once.

// Three jobs with feedback and artifacts: one active, one done, one archived,
// plus a job with neither.
func seedIndexedBoard(t *testing.T) (st *store.Store, active, done, archived, bare *jobs.Job) {
	t.Helper()
	st = useTestStore(t)
	active = mustCreateStoreJob(t, st, store.Job{Slug: "ix-active", Repo: "x", State: "working"})
	done = mustCreateStoreJob(t, st, store.Job{Slug: "ix-done", Repo: "x", State: "done"})
	old := mustCreateStoreJob(t, st, store.Job{Slug: "ix-old", Repo: "x", State: "done"})
	bare = mustCreateStoreJob(t, st, store.Job{Slug: "ix-bare", Repo: "x"})
	for _, f := range []store.Feedback{
		{JobID: active.ID, JobSlug: "ix-active", Artifact: "00-overview.md", Note: "active second", TS: 2000},
		{JobID: active.ID, JobSlug: "ix-active", Artifact: "50-x.md", Note: "active first", TS: 1000},
		{JobID: old.ID, JobSlug: "ix-old", Artifact: "90-build-report.md", Note: "archived note", TS: 1500},
		{JobSlug: "gone", Artifact: "a.md", Note: "no owning job", TS: 500},
	} {
		if _, err := st.AddFeedback(f); err != nil {
			t.Fatalf("add feedback %q: %v", f.Note, err)
		}
	}
	files := func(names ...string) []store.ArtifactFile {
		out := make([]store.ArtifactFile, 0, len(names))
		for _, n := range names {
			out = append(out, store.ArtifactFile{Path: n, Bytes: []byte("bytes of " + n), Mtime: 1})
		}
		return out
	}
	if err := st.SyncArtifacts(done.ID, files("90-build-report.md", "00-overview.md", "sub dir/a#b.png"), nil); err != nil {
		t.Fatal(err)
	}
	if err := st.SyncArtifacts(old.ID, files("50-contract.md", "00-overview.md"), nil); err != nil {
		t.Fatal(err)
	}
	name, err := st.ArchiveJob(old.ID)
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	archived = jobs.ArchivedJobByName(jobs.LoadArchivedJobs(), name)
	if archived == nil {
		t.Fatal("archived job not found")
	}
	return st, active, done, archived, bare
}

func TestFeedbackIndexAnswersWhatJobFeedbackAnswers(t *testing.T) {
	st, active, done, archived, bare := seedIndexedBoard(t)
	index := feedbackIndex(st)
	for _, j := range []*jobs.Job{active, done, archived, bare} {
		want := jobFeedback(st, j.ID)
		got := webFeedbackFor(index, j.ID)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("job %s: indexed feedback = %+v, want jobFeedback's %+v", j.Slug, got, want)
		}
	}
	// oldest first within a job, as the per-job read orders them
	if got := webFeedbackFor(index, active.ID); len(got) != 2 || got[0].Note != "active first" || got[1].Note != "active second" {
		t.Errorf("active job's indexed feedback = %+v, want oldest first", got)
	}
	// a row with no owning job belongs to no card
	if _, ok := index[0]; ok {
		t.Errorf("the index carries a bucket for job id 0: %+v", index[0])
	}
	total := 0
	for _, rows := range index {
		total += len(rows)
	}
	if total != 3 {
		t.Errorf("the index holds %d rows, want the 3 that have an owning job", total)
	}
}

func TestFeedbackIndexWithNoStoreOrNoRows(t *testing.T) {
	if got := feedbackIndex(nil); got != nil {
		t.Errorf("feedbackIndex(nil) = %v, want nil", got)
	}
	if got := webFeedbackFor(nil, 7); got != nil {
		t.Errorf("a nil index answered %+v, want no feedback", got)
	}
	st := useTestStore(t)
	if got := webFeedbackFor(feedbackIndex(st), 7); got != nil {
		t.Errorf("an empty store answered %+v, want no feedback", got)
	}
}

func TestBoardArtifactsAnswersWhatJobArtifactsAnswers(t *testing.T) {
	st, active, done, archived, bare := seedIndexedBoard(t)
	index := artifactPathsIndex(st)
	for _, j := range []*jobs.Job{active, done, archived, bare} {
		want := jobArtifacts(j, false)
		got := boardArtifacts(index, j, false)
		if len(got) != len(want) {
			t.Fatalf("job %s: indexed artifacts = %+v, want jobArtifacts' %+v", j.Slug, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("job %s: artifact %d = %+v, want %+v", j.Slug, i, got[i], want[i])
			}
		}
	}
	// the homes: an archived job's URLs are keyed on its archive name under
	// "archive", any other stored list on its slug under "pluto"
	if got := boardArtifacts(index, archived, false); len(got) != 2 || got[0].URL != "/api/artifact/"+archived.ArchivedName+"/archive/00-overview.md" {
		t.Errorf("archived artifacts = %+v", got)
	}
	got := boardArtifacts(index, done, false)
	if len(got) != 3 || got[0].Name != "00-overview.md" || got[1].Name != "90-build-report.md" || got[2].Name != "sub dir/a#b.png" {
		t.Fatalf("done job's artifacts = %+v, want the three in name order", got)
	}
	if got[2].URL != "/api/artifact/ix-done/pluto/sub%20dir/a%23b.png" {
		t.Errorf("escaped URL = %q", got[2].URL)
	}
	// archivedWebArtifacts is the same read for an archived job
	if a, b := archivedWebArtifacts(index, archived), boardArtifacts(index, archived, false); !reflect.DeepEqual(a, b) {
		t.Errorf("archivedWebArtifacts = %+v, boardArtifacts = %+v", a, b)
	}
}

func TestArtifactPathsIndexWithNoStoreOrNoRows(t *testing.T) {
	if got := artifactPathsIndex(nil); got != nil {
		t.Errorf("artifactPathsIndex(nil) = %v, want nil", got)
	}
	j := &jobs.Job{ID: 7, Slug: "none"}
	if got := boardArtifacts(nil, j, false); len(got) != 0 {
		t.Errorf("a nil index listed %+v, want none", got)
	}
	st := useTestStore(t)
	if got := boardArtifacts(artifactPathsIndex(st), j, false); len(got) != 0 {
		t.Errorf("an empty store listed %+v, want none", got)
	}
}

// The archived card is built from the indexes alone: it takes no store, so it
// cannot run a query per job, and what it carries matches the per-job reads.
func TestArchivedWebJobReadsItsFeedbackAndArtifactsFromTheIndexes(t *testing.T) {
	st, _, _, archived, _ := seedIndexedBoard(t)
	wj := archivedWebJob(archived, feedbackIndex(st), artifactPathsIndex(st), nil, nil)
	if !reflect.DeepEqual(wj.Feedback, jobFeedback(st, archived.ID)) {
		t.Errorf("archived card feedback = %+v, want %+v", wj.Feedback, jobFeedback(st, archived.ID))
	}
	if !reflect.DeepEqual(wj.Artifacts, jobArtifacts(archived, false)) {
		t.Errorf("archived card artifacts = %+v, want %+v", wj.Artifacts, jobArtifacts(archived, false))
	}
	// with no indexes it still builds a card, carrying neither
	bare := archivedWebJob(archived, nil, nil, nil, nil)
	if bare.Feedback != nil || len(bare.Artifacts) != 0 || bare.Slug != archived.ArchivedName || !bare.Archived {
		t.Errorf("card built without indexes = %+v", bare)
	}
}
