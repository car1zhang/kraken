package api

import (
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"kraken/internal/jobs"
	"kraken/internal/store"
)

// artifactnew_test.go — the additive `artifacts[].new` flag: set on an
// active card's artifact when its stored mtime is newer than the operator's
// last read of it (or it was never read), cleared once a read lands after
// the change, kept off the wire when false, and never set on an archived
// card.

func TestMarkNewArtifactsFlagsTheUnreadAndTheChanged(t *testing.T) {
	st := useTestStore(t)
	j := mustCreateStoreJob(t, st, store.Job{Slug: "nu", Repo: "x", State: "blocked"})
	if err := st.SyncArtifacts(j.ID, []store.ArtifactFile{
		{Path: "00-overview.md", Bytes: []byte("o"), Mtime: 1_000},
		{Path: "50-contract.md", Bytes: []byte("c"), Mtime: 2_000},
		{Path: "legacy.md", Bytes: []byte("l"), Mtime: 0},
	}, nil); err != nil {
		t.Fatal(err)
	}
	// the overview was opened after it last changed; the contract before
	seedSeen(t, st, j.ID, map[string]int64{"00-overview.md": 1_500, "50-contract.md": 1_500})

	index := artifactPathsIndex(st)
	seen := jobs.ArtifactSeenIndex(st)
	got := markNewArtifacts(boardArtifacts(index, j, false), seen[j.ID])
	want := map[string]bool{"00-overview.md": false, "50-contract.md": true, "legacy.md": false}
	if len(got) != len(want) {
		t.Fatalf("artifacts = %+v, want %d", got, len(want))
	}
	for _, a := range got {
		if a.New != want[a.Name] {
			t.Errorf("%s new = %v, want %v", a.Name, a.New, want[a.Name])
		}
	}
	// never read at all: every stamped artifact is new, the unstamped one is not
	for _, a := range markNewArtifacts(boardArtifacts(index, j, false), nil) {
		if a.New != (a.Name != "legacy.md") {
			t.Errorf("unread %s new = %v", a.Name, a.New)
		}
	}
}

func TestArtifactNewRidesTheWireOnlyWhenTrue(t *testing.T) {
	fresh, err := json.Marshal(webArtifact{Name: "a.md", URL: "/api/artifact/x/pluto/a.md", New: true, mtime: 5})
	if err != nil {
		t.Fatal(err)
	}
	if s := string(fresh); !strings.Contains(s, `"new":true`) || strings.Contains(s, "mtime") {
		t.Errorf("a new artifact carries new:true and no mtime: %s", s)
	}
	assertShape(t, "webArtifact(new)", fresh, webArtifactSpec)
	read, err := json.Marshal(webArtifact{Name: "b.md", URL: "/api/artifact/x/pluto/b.md", mtime: 5})
	if err != nil {
		t.Fatal(err)
	}
	if s := string(read); strings.Contains(s, "new") || strings.Contains(s, "mtime") {
		t.Errorf("a read artifact carries neither key: %s", s)
	}
	assertShape(t, "webArtifact(read)", read, webArtifactSpec)
}

func TestArchivedCardArtifactsNeverReadAsNew(t *testing.T) {
	st, _, _, archived, _ := seedIndexedBoard(t)
	// the archive was never opened, so by the active rule everything would be new
	wj := archivedWebJob(archived, feedbackIndex(st), artifactPathsIndex(st), nil, nil)
	if len(wj.Artifacts) == 0 {
		t.Fatal("the archived card lists no artifacts; the fixture should give it two")
	}
	for _, a := range wj.Artifacts {
		if a.New {
			t.Errorf("archived artifact %s reads as new; an archived card flags nothing", a.Name)
		}
	}
}

func TestBoardActiveCardsCarryNewAndArchivedDoNot(t *testing.T) {
	// a full board build reads this node's fleet identity, which is memoized
	// per process: keep the real machine's tailscale out of it, as every other
	// board-building test does, or the fleet tests that run after this one
	// read "Carl's Macbook Bro" where they stubbed their own node
	t.Setenv("KRAKEN_TAILSCALE", filepath.Join(t.TempDir(), "no-such-tailscale"))
	t.Setenv("KRAKEN_NODES", "off")
	st, _, done, archived, _ := seedIndexedBoard(t)
	find := func(board webBoard, slug string, isArchived bool) *webJob {
		for i := range board.Jobs {
			if board.Jobs[i].Slug == slug && board.Jobs[i].Archived == isArchived {
				return &board.Jobs[i]
			}
		}
		return nil
	}
	// the done job's artifacts (mtime 1) were never opened: new on its card
	board := buildWebBoard()
	doneCard, archivedCard := find(board, done.Slug, false), find(board, archived.ArchivedName, true)
	if doneCard == nil || archivedCard == nil {
		t.Fatalf("cards not found: done=%v archived=%v", doneCard != nil, archivedCard != nil)
	}
	if len(doneCard.Artifacts) != 3 {
		t.Fatalf("done card artifacts = %+v, want three", doneCard.Artifacts)
	}
	for _, a := range doneCard.Artifacts {
		if !a.New {
			t.Errorf("never-opened %s on an active card is not new", a.Name)
		}
	}
	for _, a := range archivedCard.Artifacts {
		if a.New {
			t.Errorf("archived %s reads as new", a.Name)
		}
	}
	// opening one artifact clears its flag on the next build; the others stay
	seedSeen(t, st, done.ID, map[string]int64{"00-overview.md": 2})
	doneCard = find(buildWebBoard(), done.Slug, false)
	if doneCard == nil {
		t.Fatal("done card gone after the read")
	}
	for _, a := range doneCard.Artifacts {
		if a.New != (a.Name != "00-overview.md") {
			t.Errorf("after opening the overview, %s new = %v", a.Name, a.New)
		}
	}
}

// seedSeen writes a job's artifacts_seen row directly, standing in for the
// GET /api/artifact reads that stamp it in serve.
func seedSeen(t *testing.T, st *store.Store, jobID int64, reads map[string]int64) {
	t.Helper()
	data, err := json.Marshal(reads)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetKV("artifacts_seen:"+strconv.FormatInt(jobID, 10), string(data)); err != nil {
		t.Fatal(err)
	}
}
