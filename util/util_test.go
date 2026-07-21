package util

import (
	"os"
	"path/filepath"
	"testing"
)

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
