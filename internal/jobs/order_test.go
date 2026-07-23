package jobs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"kraken/internal/util"
)

// order_test.go — the kraken-owned board order store. ApplyColumnOrder is
// pure (no filesystem); LoadBoardOrder/SaveColumnOrder point at a temp HOME
// so they never touch the real ~/.kraken/order.json.

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

func TestLoadBoardOrderMissingFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	got := LoadBoardOrder()
	if len(got) != 0 {
		t.Fatalf("missing file: got %v, want empty map", got)
	}
	if got == nil {
		t.Fatal("missing file: got nil map, want non-nil empty map")
	}
}

func TestLoadBoardOrderCorruptJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".kraken")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "order.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := LoadBoardOrder()
	if len(got) != 0 {
		t.Fatalf("corrupt json: got %v, want empty map", got)
	}
}

func TestLoadBoardOrderWrongShape(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".kraken")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "order.json"), []byte(`["not", "the", "right", "shape"]`), 0o644); err != nil {
		t.Fatal(err)
	}
	got := LoadBoardOrder()
	if len(got) != 0 {
		t.Fatalf("wrong shape: got %v, want empty map", got)
	}
}

func TestSaveColumnOrderRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

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

	data, err := os.ReadFile(filepath.Join(util.Home(), ".kraken", "order.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(data) {
		t.Fatalf("file written is not valid JSON: %s", data)
	}
}

func TestSaveColumnOrderPreservesOtherColumns(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

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
	t.Setenv("HOME", t.TempDir())

	if err := SaveColumnOrder("queued", []string{"a", "", "b", "a", "", "c", "b"}); err != nil {
		t.Fatal(err)
	}

	got := LoadBoardOrder()
	want := map[string][]string{"queued": {"a", "b", "c"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dedupe/drop-empty: got %v, want %v", got, want)
	}
}
