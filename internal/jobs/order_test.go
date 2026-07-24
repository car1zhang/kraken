package jobs

import (
	"reflect"
	"testing"
)

// order_test.go — the kraken-owned board order store. ApplyColumnOrder is
// pure (no store access); LoadBoardOrder/SaveColumnOrder are thin wrappers
// over store.GetBoardOrder/SetBoardColumn (which own dedup/empty-drop/
// cross-column-removal — see internal/store's own board-order tests for
// that coverage), so the tests here just confirm the wrapper wiring round-
// trips through a real store.

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
	useTestStore(t)

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
	useTestStore(t)

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
	useTestStore(t)

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
	useTestStore(t)

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
