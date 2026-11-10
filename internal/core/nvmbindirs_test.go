package core

import (
	"os"
	"path/filepath"
	"testing"
)

// NvmBinDirs is the one spelling of the nvm rung both ResolveCodex and
// jobs.lookToolchain walk: absolute bin dirs, newest node first, and an empty
// list rather than an error on a machine with no nvm.
func TestNvmBinDirsNewestFirstAbsolute(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	nvm := filepath.Join(home, ".nvm", "versions", "node")
	for _, v := range []string{"v9.11.2", "v24.6.0", "v20.1.0", "notes"} {
		if err := os.MkdirAll(filepath.Join(nvm, v, "bin"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	got := NvmBinDirs()
	want := []string{
		filepath.Join(nvm, "v24.6.0", "bin"),
		filepath.Join(nvm, "v20.1.0", "bin"),
		filepath.Join(nvm, "v9.11.2", "bin"),
		filepath.Join(nvm, "notes", "bin"),
	}
	if len(got) != len(want) {
		t.Fatalf("NvmBinDirs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("NvmBinDirs()[%d] = %q, want %q", i, got[i], want[i])
		}
		if !filepath.IsAbs(got[i]) {
			t.Errorf("NvmBinDirs()[%d] = %q is not absolute", i, got[i])
		}
	}
}

func TestNvmBinDirsNoNvmIsEmpty(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if got := NvmBinDirs(); len(got) != 0 {
		t.Fatalf("NvmBinDirs() with no ~/.nvm = %v, want none", got)
	}
}
