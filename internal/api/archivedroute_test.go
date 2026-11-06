package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"kraken/internal/jobs"
	"kraken/internal/store"
)

// archivedroute_test.go — the archive off the board poll: GET /api/archived
// serves the archived cards, /api/board?archived=0 leaves them out, and the
// board's default answer is what it always was.

// One active job and two archived ones; the first archived job links a goal
// and carries feedback and an artifact.
func seedArchive(t *testing.T) (st *store.Store, goalID int64) {
	t.Helper()
	// the fleet off: a real board build would otherwise read this machine's
	// tailnet identity and leave it in the package's node cache for the
	// fleet tests that run after
	t.Setenv("KRAKEN_NODES", "off")
	st = useTestStore(t)
	mustCreateStoreJob(t, st, store.Job{Slug: "arc-live", Repo: "x", State: "working"})
	one := mustCreateStoreJob(t, st, store.Job{Slug: "arc-one", Repo: "x", State: "done"})
	two := mustCreateStoreJob(t, st, store.Job{Slug: "arc-two", Repo: "x", State: "done"})
	week, _ := store.GoalPeriods(time.Now())
	g, err := st.AddGoal(store.GoalAdd{Scope: "week", Period: week, Text: "ship it"}, time.Now().UnixMilli())
	if err != nil {
		t.Fatalf("add goal: %v", err)
	}
	if err := st.AddJobGoal(one.ID, g.ID); err != nil {
		t.Fatalf("link goal: %v", err)
	}
	if _, err := st.AddFeedback(store.Feedback{JobID: one.ID, JobSlug: "arc-one", Artifact: "00-overview.md", Note: "kept", TS: 1}); err != nil {
		t.Fatal(err)
	}
	if err := st.SyncArtifacts(one.ID, []store.ArtifactFile{{Path: "00-overview.md", Bytes: []byte("x"), Mtime: 1}}, nil); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{one.ID, two.ID} {
		if _, err := st.ArchiveJob(id); err != nil {
			t.Fatalf("archive: %v", err)
		}
	}
	return st, g.ID
}

// A board cache that serves this board and never runs the real build.
func useFixedBoard(t *testing.T, board webBoard) {
	t.Helper()
	orig := webBoardCache
	webBoardCache = &boardCache{ttl: time.Hour, now: time.Now, build: func() webBoard { return board }}
	t.Cleanup(func() { webBoardCache = orig })
}

func archiveGet(t *testing.T, h http.HandlerFunc, method, target string, into any) int {
	t.Helper()
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(method, target, nil))
	if into != nil && rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), into); err != nil {
			t.Fatalf("%s %s: decode %q: %v", method, target, rec.Body, err)
		}
	}
	return rec.Code
}

func TestArchivedRouteServesEveryArchivedCard(t *testing.T) {
	seedArchive(t)
	got := buildWebArchived()
	if len(got.Jobs) != 2 {
		t.Fatalf("archived cards = %d, want 2: %+v", len(got.Jobs), got.Jobs)
	}
	var one *webJob
	for i := range got.Jobs {
		j := &got.Jobs[i]
		if !j.Archived || j.State != "archived" {
			t.Errorf("card %s: archived = %v, state = %q", j.Slug, j.Archived, j.State)
		}
		if j.DisplaySlug == "arc-one" {
			one = j
		}
	}
	if one == nil {
		t.Fatal("arc-one's card is missing")
	}
	if len(one.Feedback) != 1 || one.Feedback[0].Note != "kept" {
		t.Errorf("arc-one feedback = %+v", one.Feedback)
	}
	if len(one.Artifacts) != 1 || one.Artifacts[0].Name != "00-overview.md" {
		t.Errorf("arc-one artifacts = %+v", one.Artifacts)
	}
	if len(one.Goals) != 1 || one.Goals[0].Text != "ship it" || one.Goal == nil {
		t.Errorf("arc-one goals = %+v, goal = %+v", one.Goals, one.Goal)
	}
}

// The route and the board's default answer build the same cards.
func TestArchivedRouteMatchesTheBoardsArchivedTail(t *testing.T) {
	seedArchive(t)
	board := buildWebBoard()
	var tail []webJob
	for _, j := range board.Jobs {
		if j.Archived {
			tail = append(tail, j)
		}
	}
	got := buildWebArchived().Jobs
	if !reflect.DeepEqual(got, tail) {
		t.Errorf("GET /api/archived cards differ from the board's archived tail:\n route %+v\n board %+v", got, tail)
	}
	if board.ArchivedCount != 2 {
		t.Errorf("board archived_count = %d, want 2", board.ArchivedCount)
	}
}

func TestBoardCountsTheArchiveAndItsGoalLinks(t *testing.T) {
	_, goalID := seedArchive(t)
	board := buildWebBoard()
	if board.ArchivedCount != 2 {
		t.Errorf("archived_count = %d, want 2", board.ArchivedCount)
	}
	if want := map[int64]int{goalID: 1}; !reflect.DeepEqual(board.ArchivedGoalCounts, want) {
		t.Errorf("archived_goal_counts = %v, want %v", board.ArchivedGoalCounts, want)
	}
}

func TestArchivedGoalCountsCountsOnlyArchivedLinks(t *testing.T) {
	archived := []*jobs.Job{{ID: 1}, {ID: 2}, {ID: 3}}
	links := map[int64][]int64{1: {7, 8}, 2: {7}, 9: {7, 8}}
	if got, want := archivedGoalCounts(archived, links), map[int64]int{7: 2, 8: 1}; !reflect.DeepEqual(got, want) {
		t.Errorf("counts = %v, want %v", got, want)
	}
	if got := archivedGoalCounts(archived, nil); got != nil {
		t.Errorf("no links: counts = %v, want nil so the key is absent", got)
	}
	if got := archivedGoalCounts(nil, links); got != nil {
		t.Errorf("no archived jobs: counts = %v, want nil", got)
	}
}

func TestArchivedRouteEmptyArchiveIsAnEmptyList(t *testing.T) {
	useTestStore(t)
	var body map[string]json.RawMessage
	if code := archiveGet(t, archivedHandler, http.MethodGet, "/api/archived", &body); code != http.StatusOK {
		t.Fatalf("GET = %d, want 200", code)
	}
	if string(body["jobs"]) != "[]" {
		t.Errorf("jobs = %s, want [] and never null", body["jobs"])
	}
}

func TestArchivedRouteIsGetOnly(t *testing.T) {
	useTestStore(t)
	if code := archiveGet(t, archivedHandler, http.MethodPost, "/api/archived", nil); code != http.StatusMethodNotAllowed {
		t.Errorf("POST = %d, want 405", code)
	}
}

func TestArchivedRouteIsRegisteredAtTheBoardsTier(t *testing.T) {
	found := false
	for _, p := range RoutePatterns() {
		if p == "/api/archived" {
			found = true
		}
	}
	if !found {
		t.Fatal("/api/archived is not registered")
	}
	if got, want := policyFor("/api/archived"), policyFor("/api/board"); got != want {
		t.Errorf("/api/archived tier = %v, want the board's %v", got, want)
	}
}

func TestBoardWithoutArchivedParameter(t *testing.T) {
	board := webBoard{
		Jobs: []webJob{
			{Slug: "a", State: "working"},
			{Slug: "old", State: "archived", Archived: true},
			{Slug: "b", State: "done"},
			{Slug: "old-2", State: "archived", Archived: true},
		},
		ArchivedCount:      2,
		ArchivedGoalCounts: map[int64]int{7: 1},
	}
	useFixedBoard(t, board)
	slugs := func(target string) ([]string, webBoard) {
		var got webBoard
		if code := archiveGet(t, boardHandler, http.MethodGet, target, &got); code != http.StatusOK {
			t.Fatalf("GET %s = %d", target, code)
		}
		var out []string
		for _, j := range got.Jobs {
			out = append(out, j.Slug)
		}
		return out, got
	}
	// the default answer, and any value but 0, is the full board
	for _, target := range []string{"/api/board", "/api/board?archived=1", "/api/board?archived=", "/api/board?archived=false"} {
		if got, _ := slugs(target); !reflect.DeepEqual(got, []string{"a", "old", "b", "old-2"}) {
			t.Errorf("%s jobs = %v, want every card", target, got)
		}
	}
	lean, got := slugs("/api/board?archived=0")
	if !reflect.DeepEqual(lean, []string{"a", "b"}) {
		t.Errorf("?archived=0 jobs = %v, want the active cards in order", lean)
	}
	if got.ArchivedCount != 2 || !reflect.DeepEqual(got.ArchivedGoalCounts, map[int64]int{7: 1}) {
		t.Errorf("?archived=0 lost the counts: %d %v", got.ArchivedCount, got.ArchivedGoalCounts)
	}
	// the lean answer never edits the cached board: the next full read is whole
	if again, _ := slugs("/api/board"); len(again) != 4 {
		t.Errorf("after a lean read the full board has %d cards, want 4", len(again))
	}
}

func TestBoardWithoutArchivedKeepsJobsAnArray(t *testing.T) {
	useFixedBoard(t, webBoard{Jobs: []webJob{{Slug: "old", State: "archived", Archived: true}}, ArchivedCount: 1})
	var body map[string]json.RawMessage
	if code := archiveGet(t, boardHandler, http.MethodGet, "/api/board?archived=0", &body); code != http.StatusOK {
		t.Fatalf("GET = %d", code)
	}
	if string(body["jobs"]) != "[]" {
		t.Errorf("jobs = %s, want [] when only archived cards exist", body["jobs"])
	}
}
