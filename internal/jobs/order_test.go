package jobs

import (
	"reflect"
	"testing"

	"kraken/internal/store"
)

// order_test.go — the kraken-owned board order store. ApplyColumnOrder is
// pure (no store access); LoadBoardOrder/SaveColumnOrder are the SLUG <-> ID
// boundary over store.GetBoardOrder/SetBoardColumn (which own dedup/
// zero-id-drop/cross-column-removal — see internal/store's own board-order
// tests for that coverage), so the tests here confirm the wrapper wiring
// round-trips slugs through a real store that keys on ids.

// seedJobs creates one active job per slug so SaveColumnOrder has something to
// resolve. Since schema v11 a slug naming no active job is simply not a board
// member, so every ordering test needs real rows behind its slugs — which is
// also the fact under test: the boundary translates, it does not invent.
func seedJobs(t *testing.T, st *store.Store, slugs ...string) {
	t.Helper()
	for _, slug := range slugs {
		if _, err := st.CreateJob(&store.Job{Slug: slug}); err != nil {
			t.Fatalf("CreateJob %q: %v", slug, err)
		}
	}
}

func TestApplyColumnOrder(t *testing.T) {
	cases := []struct {
		name    string
		members []string
		stored  []string
		want    []string
	}{
		{
			name:    "all unlisted keeps given order",
			members: []string{"asr-eval", "tui-polish", "web-sse"},
			stored:  nil,
			want:    []string{"asr-eval", "tui-polish", "web-sse"},
		},
		{
			name:    "all listed returns stored order",
			members: []string{"asr-eval", "tui-polish", "web-sse"},
			stored:  []string{"web-sse", "asr-eval", "tui-polish"},
			want:    []string{"web-sse", "asr-eval", "tui-polish"},
		},
		{
			name:    "mixed: unlisted first, then stored order",
			members: []string{"asr-eval", "new-job", "tui-polish", "web-sse"},
			stored:  []string{"web-sse", "tui-polish"},
			want:    []string{"asr-eval", "new-job", "web-sse", "tui-polish"},
		},
		{
			name:    "stale stored entries not a member are ignored",
			members: []string{"asr-eval", "web-sse"},
			stored:  []string{"web-sse", "gone-job", "asr-eval"},
			want:    []string{"web-sse", "asr-eval"},
		},
		{
			name:    "duplicate stored entries don't duplicate output",
			members: []string{"asr-eval", "web-sse"},
			stored:  []string{"web-sse", "web-sse", "asr-eval"},
			want:    []string{"web-sse", "asr-eval"},
		},
		{
			name:    "empty members yields empty output",
			members: nil,
			stored:  []string{"web-sse"},
			want:    []string{},
		},
	}
	for _, c := range cases {
		got := ApplyColumnOrder(c.members, c.stored)
		if len(got) == 0 {
			got = []string{}
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestLoadBoardOrderEmptyStore(t *testing.T) {
	useTestStore(t)
	got := LoadBoardOrder()
	if len(got) != 0 {
		t.Fatalf("no columns saved yet: got %v, want empty map", got)
	}
	if got == nil {
		t.Fatal("no columns saved yet: got nil map, want non-nil empty map")
	}
}

func TestSaveColumnOrderRoundTrip(t *testing.T) {
	st := useTestStore(t)
	seedJobs(t, st, "web-sse", "asr-eval", "tui-polish")

	if err := SaveColumnOrder("queued", []string{"web-sse", "asr-eval"}); err != nil {
		t.Fatal(err)
	}
	if err := SaveColumnOrder("draft", []string{"tui-polish"}); err != nil {
		t.Fatal(err)
	}

	got := LoadBoardOrder()
	want := map[string][]string{
		"queued": {"web-sse", "asr-eval"},
		"draft":  {"tui-polish"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip: got %v, want %v", got, want)
	}
}

func TestSaveColumnOrderPreservesOtherColumns(t *testing.T) {
	st := useTestStore(t)
	seedJobs(t, st, "a", "b", "c", "z")

	if err := SaveColumnOrder("queued", []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	if err := SaveColumnOrder("draft", []string{"c"}); err != nil {
		t.Fatal(err)
	}
	if err := SaveColumnOrder("queued", []string{"b", "a", "z"}); err != nil {
		t.Fatal(err)
	}

	got := LoadBoardOrder()
	want := map[string][]string{
		"queued": {"b", "a", "z"},
		"draft":  {"c"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("preserve other columns: got %v, want %v", got, want)
	}
}

func TestSaveColumnOrderDedupesAndDropsEmpties(t *testing.T) {
	st := useTestStore(t)
	seedJobs(t, st, "a", "b", "c")

	if err := SaveColumnOrder("queued", []string{"a", "", "b", "a", "", "c", "b"}); err != nil {
		t.Fatal(err)
	}

	got := LoadBoardOrder()
	want := map[string][]string{"queued": {"a", "b", "c"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dedupe/drop-empty: got %v, want %v", got, want)
	}
}

// TestSaveColumnOrderRemovesFromOtherColumns covers L9: a slug moved into
// this column must vanish from every OTHER column, not just get added here.
func TestSaveColumnOrderRemovesFromOtherColumns(t *testing.T) {
	st := useTestStore(t)
	seedJobs(t, st, "a", "b", "c")

	if err := SaveColumnOrder("working", []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	// "a" moves to queued, and appears twice in the input — first
	// occurrence wins, and it must vanish from working in the same save.
	if err := SaveColumnOrder("queued", []string{"a", "c", "a"}); err != nil {
		t.Fatal(err)
	}

	got := LoadBoardOrder()
	want := map[string][]string{
		"queued":  {"a", "c"},
		"working": {"b"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cross-column removal: got %v, want %v", got, want)
	}
}

// TestBoardOrderBoundaryDropsUnknownSlugs pins both directions of the
// translation this file owns. A slug naming no active job never reaches the
// store on the way IN (it is not a board member and there is no id to store),
// and an id whose job has since gone never reaches the caller on the way OUT
// (it would otherwise render as an empty-string card).
func TestBoardOrderBoundaryDropsUnknownSlugs(t *testing.T) {
	st := useTestStore(t)
	seedJobs(t, st, "here")

	if err := SaveColumnOrder("queued", []string{"here", "never-existed"}); err != nil {
		t.Fatal(err)
	}
	if got := LoadBoardOrder()["queued"]; !reflect.DeepEqual(got, []string{"here"}) {
		t.Fatalf("queued = %v, want just the slug that names a job", got)
	}
	raw, err := st.GetBoardOrder()
	if err != nil {
		t.Fatalf("GetBoardOrder: %v", err)
	}
	if len(raw["queued"]) != 1 {
		t.Fatalf("stored ids = %v, want the unknown slug never to have reached the store", raw["queued"])
	}

	// the job goes away; its stale row STAYS in the table (no cascade — see
	// store/board_order.go) but stops appearing in the rendered order
	job, err := st.GetJobBySlug("here")
	if err != nil {
		t.Fatalf("GetJobBySlug: %v", err)
	}
	if err := st.DeleteJob(job.ID); err != nil {
		t.Fatalf("DeleteJob: %v", err)
	}
	if got := LoadBoardOrder()["queued"]; len(got) != 0 {
		t.Fatalf("queued = %v, want the vanished job dropped from the render", got)
	}
	raw, _ = st.GetBoardOrder()
	if len(raw["queued"]) != 1 {
		t.Fatalf("stored ids = %v, want the stale row to SURVIVE — it must not vanish mid-drag", raw["queued"])
	}
}
