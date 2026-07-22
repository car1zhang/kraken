package jobs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFeedbackArtifact writes a fake artifact file with the given content
// and extension into a temp dir, returning its path.
func writeFeedbackArtifact(t *testing.T, content, ext string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "artifact"+ext)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSaveArtifactFeedbackWritesFiles(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	artifact := writeFeedbackArtifact(t, "# a report\n\nbody text\n", ".md")

	dir, err := SaveArtifactFeedback("my-job", "cell", "report.md", artifact, "the diagram is confusing\n")
	if err != nil {
		t.Fatalf("SaveArtifactFeedback: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("entry dir missing: %v", err)
	}

	// extension preserved
	snapPath := filepath.Join(dir, "snapshot.md")
	snap, err := os.ReadFile(snapPath)
	if err != nil {
		t.Fatalf("snapshot missing at %s: %v", snapPath, err)
	}
	if got, want := string(snap), "# a report\n\nbody text\n"; got != want {
		t.Errorf("snapshot content = %q, want %q", got, want)
	}

	fb, err := os.ReadFile(filepath.Join(dir, "feedback.txt"))
	if err != nil {
		t.Fatalf("feedback.txt missing: %v", err)
	}
	text := string(fb)
	for _, want := range []string{
		"job: my-job\n", "home: cell\n", "artifact: report.md\n", "source: " + artifact + "\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("feedback.txt missing header line %q, got:\n%s", want, text)
		}
	}
	if !strings.Contains(text, "time: ") {
		t.Errorf("feedback.txt missing time header, got:\n%s", text)
	}
	// header block, blank line, then the feedback verbatim (trailing
	// newline included, unmodified)
	if !strings.HasSuffix(text, "\n\nthe diagram is confusing\n") {
		t.Errorf("feedback.txt does not end with verbatim feedback after blank line, got:\n%q", text)
	}
}

func TestSaveArtifactFeedbackEmpty(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	artifact := writeFeedbackArtifact(t, "x", ".html")
	for _, bad := range []string{"", "   ", "\n\t\n"} {
		if _, err := SaveArtifactFeedback("job", "cell", "x.html", artifact, bad); err == nil {
			t.Errorf("feedback %q: want error, got none", bad)
		}
	}
}

// TestClaimFeedbackDirCollision exercises the suffix logic directly (rather
// than racing real wall-clock seconds through SaveArtifactFeedback): each
// claim must both pick the next free name — base, -2, -3 — AND create it, so
// two same-second writers can never share an entry.
func TestClaimFeedbackDirCollision(t *testing.T) {
	dayDir := t.TempDir()
	base := "153000-my-job"

	for i, want := range []string{base, base + "-2", base + "-3"} {
		got, err := claimFeedbackDir(dayDir, base)
		if err != nil {
			t.Fatal(err)
		}
		if got != filepath.Join(dayDir, want) {
			t.Fatalf("claim %d = %q, want %q", i+1, got, want)
		}
		if fi, err := os.Stat(got); err != nil || !fi.IsDir() {
			t.Fatalf("claim %d: dir not created (err %v)", i+1, err)
		}
	}
}

// TestSaveArtifactFeedbackSameSlugTwice is a lighter integration check that
// two real back-to-back saves for the same slug never collide on disk,
// whichever side of a second boundary they land on.
func TestSaveArtifactFeedbackSameSlugTwice(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	artifact := writeFeedbackArtifact(t, "v1", ".txt")

	dir1, err := SaveArtifactFeedback("twice", "cell", "x.txt", artifact, "first note")
	if err != nil {
		t.Fatal(err)
	}
	dir2, err := SaveArtifactFeedback("twice", "cell", "x.txt", artifact, "second note")
	if err != nil {
		t.Fatal(err)
	}
	if dir1 == dir2 {
		t.Fatalf("two saves landed in the same dir: %s", dir1)
	}
	if _, err := os.Stat(filepath.Join(dir1, "feedback.txt")); err != nil {
		t.Errorf("first entry lost: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir2, "feedback.txt")); err != nil {
		t.Errorf("second entry lost: %v", err)
	}
}
