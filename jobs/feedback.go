package jobs

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// feedback.go — doc-feedback capture. The board's artifact overlay lets carl
// leave plaintext notes on a doc/diagram; this freezes the artifact as it
// stood at that moment (agents keep editing artifacts in place, so the file
// on disk an hour later may say something different) alongside the note, for
// the /dream skill to fold into the doc-writing skills the next day.

// SaveArtifactFeedback writes one day-grouped feedback entry under
// PlutoDir()/doc-feedback/<YYYY-MM-DD>/<HHMMSS>-<slug>/: a byte-for-byte
// snapshot of the artifact file (original extension kept) plus a
// feedback.txt carrying provenance and the verbatim note. Returns the entry
// directory. now is captured once so the date directory and the entry
// prefix can never disagree across a midnight boundary.
func SaveArtifactFeedback(slug, home, name, artifactPath, feedback string) (string, error) {
	if strings.TrimSpace(feedback) == "" {
		return "", fmt.Errorf("feedback is empty")
	}
	now := time.Now()
	if st := ActiveStore(); st != nil {
		snapshot, err := os.ReadFile(artifactPath)
		if err != nil {
			return "", err
		}
		return saveArtifactFeedbackToStore(st, slug, home, name, snapshot, filepath.Ext(artifactPath), feedback, now)
	}

	dayDir := filepath.Join(PlutoDir(), "doc-feedback", now.Format("2006-01-02"))
	if err := os.MkdirAll(dayDir, 0o755); err != nil {
		return "", err
	}
	entryDir, err := claimFeedbackDir(dayDir, now.Format("150405")+"-"+slug)
	if err != nil {
		return "", err
	}
	if err := writeFeedbackEntry(entryDir, slug, home, name, artifactPath, feedback, now); err != nil {
		// never leave a half-written entry (snapshot without feedback.txt)
		// for /dream to trip on
		os.RemoveAll(entryDir)
		return "", err
	}
	return entryDir, nil
}

// SaveArtifactFeedbackBytes is SaveArtifactFeedback's store-only twin for
// nested artifacts that never had a resolvable filesystem path in the first
// place: Q12's pluto/archive homes are flat-file only (ArtifactPath refuses
// any name containing "/"), but the artifacts table itself preserves nested
// paths (the block-event sync mirrors the cell's whole artifacts tree, not
// just its top level) — api/feedback.go's handler falls back to this when
// ArtifactPath can't resolve a name but api.ArtifactBytes can. Requires a
// store to be wired; callers that might run file-mode should prefer
// SaveArtifactFeedback whenever a real path is available.
func SaveArtifactFeedbackBytes(slug, home, name string, data []byte, feedback string) (string, error) {
	if strings.TrimSpace(feedback) == "" {
		return "", fmt.Errorf("feedback is empty")
	}
	st := ActiveStore()
	if st == nil {
		return "", fmt.Errorf("no store wired")
	}
	return saveArtifactFeedbackToStore(st, slug, home, name, data, filepath.Ext(name), feedback, time.Now())
}

// claimFeedbackDir claims an unused <dayDir>/<base>[-N] entry directory.
// os.Mkdir is the atomic claim — a same-second collision (two feedback posts
// in one second) fails with IsExist and moves on to -2, -3, ... rather than
// two writers sharing one entry.
func claimFeedbackDir(dayDir, base string) (string, error) {
	candidate := filepath.Join(dayDir, base)
	for n := 2; ; n++ {
		err := os.Mkdir(candidate, 0o755)
		if err == nil {
			return candidate, nil
		}
		if !os.IsExist(err) {
			return "", err
		}
		candidate = filepath.Join(dayDir, fmt.Sprintf("%s-%d", base, n))
	}
}

// writeFeedbackEntry fills a claimed entry dir: the byte-for-byte artifact
// snapshot (original extension kept) and feedback.txt (provenance header,
// blank line, verbatim note).
func writeFeedbackEntry(entryDir, slug, home, name, artifactPath, feedback string, now time.Time) error {
	snapshot, err := os.Open(artifactPath)
	if err != nil {
		return err
	}
	defer snapshot.Close()
	dst, err := os.Create(filepath.Join(entryDir, "snapshot"+strings.ToLower(filepath.Ext(artifactPath))))
	if err != nil {
		return err
	}
	defer dst.Close()
	if _, err := io.Copy(dst, snapshot); err != nil {
		return err
	}

	header := fmt.Sprintf(
		"time: %s\njob: %s\nhome: %s\nartifact: %s\nsource: %s\n\n%s",
		now.Format(time.RFC3339), slug, home, name, artifactPath, feedback,
	)
	return os.WriteFile(filepath.Join(entryDir, "feedback.txt"), []byte(header), 0o644)
}
