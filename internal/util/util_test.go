package util

import (
	"os"
	"path/filepath"
	"testing"
)

// slugCases is the ORACLE for the three implementations of this function.
// Slugify is ported by hand into two other languages — web/_ui/src/lib/slug.ts
// and JobSlug.slugify in ios/Kraken/JobFormView.swift — and both ports carry a
// table test whose rows are these rows, value for value, in this order.
//
// Those two pinned each other while the one that DECIDES had no test at all,
// which is the wrong way round: their headers claim to have been "verified
// against the Go implementation itself", and that claim described a one-time
// manual run the tree could not re-check. This table is what makes it
// checkable. Anything added here belongs in the other two, and only cases all
// three agree on may live in it — the ports' own files carry the two genuine
// divergences (U+0130 case mapping, and Swift's grapheme clustering), which
// are theirs to record because Go is the thing they diverge FROM.
var slugCases = []struct{ in, want string }{
	// the shapes that slugify to nothing — what RenameJob's gate 1 refuses and
	// what disarms both clients' rename buttons
	{"", ""},
	{" ", ""},
	{"!!!", ""},
	{"-", ""},
	{"日本語", ""},
	// the canonical demo: what makes a typed "My Job!" unsurprising
	{"My Job!", "my-job"},
	// separators at each end and in runs
	{"  leading", "leading"},
	{"trailing  ", "trailing"},
	{"--dashes--", "dashes"},
	{"a   b", "a-b"},
	{"a - b", "a-b"},
	// ordinary names
	{"Job 123", "job-123"},
	{"UPPER", "upper"},
	{"a.b.c", "a-b-c"},
	{"foo_bar", "foo-bar"},
	// linearSlug's own literal (internal/jobs/linear_contract_test.go)
	{"A Title", "a-title"},
	// already-slugified inputs are FIXED POINTS
	{"already-slug", "already-slug"},
	{"kraken-renaming-cells", "kraken-renaming-cells"},
	{"2026-08-12", "2026-08-12"},
	// a precomposed non-ASCII letter is a SEPARATOR, not a letter: NFC "café"
	// holds é as one rune, which fails the a-z test. The row is a literal
	// rather than the "café" escape its TypeScript twin uses, and can be:
	// a Go source file is UTF-8 by definition, so the bytes on this line are
	// the bytes the compiler reads. The escape exists in slug.test.ts to stop
	// an editor silently normalizing the character to its DECOMPOSED form,
	// which slugifies differently ("caf" either way here, but "e"+U+0301 is
	// two runes and the divergence block turns on that) — the hazard is the
	// same in this file, so if this row ever starts failing mysteriously,
	// check the bytes before the logic.
	{"café", "caf"},
}

func TestSlugify(t *testing.T) {
	for _, c := range slugCases {
		if got := Slugify(c.in); got != c.want {
			t.Errorf("Slugify(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestSlugifyIsIdempotent pins the property every caller of a slug leans on
// without saying so: a cell dir is <slugify(slug)>-<id>, an uploads dir
// re-slugifies, branchFamily slugifies each former slug, and the rename
// control seeds its input with the CURRENT slug. A slug that changed under a
// second pass would move a job's directories on a NO-OP rename — and
// RenameJob's gate 2 (slugified target == current slug is a no-op) would stop
// recognising the case it exists for.
func TestSlugifyIsIdempotent(t *testing.T) {
	for _, c := range slugCases {
		once := Slugify(c.in)
		if twice := Slugify(once); twice != once {
			t.Errorf("Slugify(%q) = %q, but Slugify(%q) = %q — not a fixed point", c.in, once, once, twice)
		}
	}
}

func TestAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := AtomicWrite(path, "hello"); err != nil {
		t.Fatalf("err: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Errorf("got %q", got)
	}
	if err := AtomicWrite(path, "world"); err != nil {
		t.Fatalf("err: %v", err)
	}
	got, _ = os.ReadFile(path)
	if string(got) != "world" {
		t.Errorf("overwrite: got %q", got)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("no leftover temp file expected, got %v", entries)
	}
}
