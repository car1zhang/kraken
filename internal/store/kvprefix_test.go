package store

import (
	"testing"
)

// kvprefix_test.go — ListKVPrefix, the one-query family read the board build
// uses for `cell_ctx:*` and `auto_handoff:*` rows. Lead-authored (direct job,
// no blind authorship — see 90-build-report.md of job 630).

func kvPrefixOpen(t *testing.T) *Store {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	return openTest(t)
}

func TestListKVPrefixReturnsOnlyThePrefixedRows(t *testing.T) {
	st := kvPrefixOpen(t)
	for k, v := range map[string]string{
		"cell_ctx:1:a":    "one",
		"cell_ctx:2:b":    "two",
		"cell_ctxx:3:c":   "three", // shares the characters, not the prefix
		"auto_handoff:1":  "ah",
		"usage_snapshot":  "us",
		"cell_ctx:4:d":    "", // an empty value is still a row
		"other:cell_ctx:": "nope",
	} {
		if err := st.SetKV(k, v); err != nil {
			t.Fatalf("SetKV %s: %v", k, err)
		}
	}
	got, err := st.ListKVPrefix("cell_ctx:")
	if err != nil {
		t.Fatalf("ListKVPrefix: %v", err)
	}
	want := map[string]string{"cell_ctx:1:a": "one", "cell_ctx:2:b": "two", "cell_ctx:4:d": ""}
	if len(got) != len(want) {
		t.Fatalf("ListKVPrefix = %v, want exactly %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("ListKVPrefix[%s] = %q, want %q", k, got[k], v)
		}
	}
}

func TestListKVPrefixMatchesWildcardCharactersLiterally(t *testing.T) {
	st := kvPrefixOpen(t)
	for _, k := range []string{"a%b:1", "axb:1", "a_b:2", "aXb:2", `a\b:3`, "ab:3"} {
		if err := st.SetKV(k, "v"); err != nil {
			t.Fatalf("SetKV %s: %v", k, err)
		}
	}
	cases := map[string][]string{
		"a%b": {"a%b:1"},
		"a_b": {"a_b:2"},
		`a\b`: {`a\b:3`},
	}
	for prefix, want := range cases {
		got, err := st.ListKVPrefix(prefix)
		if err != nil {
			t.Fatalf("ListKVPrefix(%q): %v", prefix, err)
		}
		if len(got) != len(want) {
			t.Errorf("ListKVPrefix(%q) = %v, want exactly %v — a wildcard character in the prefix must match itself", prefix, got, want)
			continue
		}
		for _, k := range want {
			if _, ok := got[k]; !ok {
				t.Errorf("ListKVPrefix(%q) lacks %s", prefix, k)
			}
		}
	}
}

func TestListKVPrefixEmptyTableIsAnEmptyMap(t *testing.T) {
	st := kvPrefixOpen(t)
	got, err := st.ListKVPrefix("cell_ctx:")
	if err != nil {
		t.Fatalf("ListKVPrefix on an empty table: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("ListKVPrefix on an empty table = %v, want an empty non-nil map", got)
	}
}
