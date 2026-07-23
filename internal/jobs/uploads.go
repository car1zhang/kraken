package jobs

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"kraken/internal/util"
)

// uploads.go — per-job image uploads: files carl attaches from the web
// board or the iOS app land at ~/.kraken/uploads/<slug>/img-NNN.<ext>,
// referenced in messages as "[image: <path>]" lines that Claude Code
// auto-ingests (see the crew brief in crew.go). Lifecycle is tied to the
// job: archiving a job removes its uploads (jobs/archive.go).

func uploadsRoot() string {
	if d := os.Getenv("KRAKEN_UPLOADS_DIR"); d != "" {
		return d // tests point at a temp dir
	}
	return filepath.Join(util.Home(), ".kraken", "uploads")
}

// UploadsDir is where a job's uploaded images live. An empty post-slugify
// slug returns "" so destructive callers (RemoveAll) can never resolve to
// the shared uploads root itself.
func UploadsDir(slug string) string {
	s := util.Slugify(slug)
	if s == "" {
		return ""
	}
	return filepath.Join(uploadsRoot(), s)
}

var uploadNamePattern = regexp.MustCompile(`^img-(\d+)\.`)

// uploadMu serializes the scan-then-write in SaveUpload: two concurrent
// HTTP uploads would otherwise both pick the same NNN (and collide on
// AtomicWrite's tmp file). Single-process server, so a mutex suffices.
var uploadMu sync.Mutex

// nextUploadName scans dir for existing img-NNN.* files (any extension) and
// picks the next sequential name; files that don't match the pattern —
// stray or hand-placed — are ignored rather than colliding with or skipping
// the sequence.
func nextUploadName(dir, ext string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	max := 0
	for _, e := range entries {
		m := uploadNamePattern.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		if n, err := strconv.Atoi(m[1]); err == nil && n > max {
			max = n
		}
	}
	return fmt.Sprintf("img-%03d.%s", max+1, ext), nil
}

// SaveUpload writes data as the next sequential image for slug, returning
// the assigned name and its absolute path — store-backed (bytes in the
// uploads table, keyed by the job's store id) when a store is wired, else
// atomically to slug's uploads dir. The path-returning contract is kept
// even store-backed: a materialized copy is written alongside the DB row,
// since callers (the "[image: <path>]" line Claude Code auto-ingests, per
// crew.go) need a real file to hand the agent, not a blob.
func SaveUpload(slug, ext string, data []byte) (name, path string, err error) {
	ext = strings.TrimPrefix(ext, ".")
	if ext == "" || strings.ContainsAny(ext, "/\\.") {
		return "", "", fmt.Errorf("bad upload extension %q", ext)
	}
	if st := ActiveStore(); st != nil {
		return saveUploadToStore(st, slug, ext, data)
	}
	dir := UploadsDir(slug)
	if dir == "" {
		return "", "", fmt.Errorf("slug %q slugifies to nothing", slug)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}
	uploadMu.Lock()
	defer uploadMu.Unlock()
	name, err = nextUploadName(dir, ext)
	if err != nil {
		return "", "", err
	}
	path = filepath.Join(dir, name)
	if err := util.AtomicWrite(path, string(data)); err != nil {
		return "", "", err
	}
	return name, path, nil
}

// RemoveUploads deletes a job's entire uploads dir; a missing dir is not an
// error — a job that never received an upload has nothing to clean up. An
// empty-slugifying slug is a no-op rather than a RemoveAll on the root.
// Store-backed, this also deletes the DB rows first: SaveUpload
// writes both a blob row AND a materialized file, so cleanup must remove
// both — the row-only delete left the on-disk copies behind forever.
func RemoveUploads(slug string) error {
	if st := ActiveStore(); st != nil {
		if sr, err := st.GetJobBySlug(slug); err == nil {
			if err := st.DeleteUploadsForJob(sr.ID); err != nil {
				return err
			}
		} // no active job under that slug: nothing in the DB to remove, but the materialized dir below may still exist
	}
	dir := UploadsDir(slug)
	if dir == "" {
		return nil
	}
	return os.RemoveAll(dir)
}
