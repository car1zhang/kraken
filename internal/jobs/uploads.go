package jobs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

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

// SaveUpload writes data as the next sequential image for slug, returning
// the assigned name and its absolute path — bytes land in the uploads
// table, keyed by the job's store id (saveUploadToStore). The path-
// returning contract still materializes a copy alongside the DB row, since
// callers (the "[image: <path>]" line Claude Code auto-ingests, per
// crew.go) need a real file to hand the agent, not a blob.
func SaveUpload(slug, ext string, data []byte) (name, path string, err error) {
	ext = strings.TrimPrefix(ext, ".")
	if ext == "" || strings.ContainsAny(ext, "/\\.") {
		return "", "", fmt.Errorf("bad upload extension %q", ext)
	}
	return saveUploadToStore(ActiveStore(), slug, ext, data)
}

// RemoveUploads deletes a job's uploads: the store rows (when an active job
// still exists under that slug) and the materialized uploads dir — a
// missing dir is not an error, and an empty-slugifying slug is a no-op
// rather than a RemoveAll on the shared uploads root.
func RemoveUploads(slug string) error {
	if sr, err := ActiveStore().GetJobBySlug(slug); err == nil {
		if err := ActiveStore().DeleteUploadsForJob(sr.ID); err != nil {
			return err
		}
	} // no active job under that slug: nothing in the DB to remove, but the materialized dir below may still exist
	dir := UploadsDir(slug)
	if dir == "" {
		return nil
	}
	return os.RemoveAll(dir)
}
