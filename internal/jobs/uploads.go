package jobs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
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

// UploadsDir is where a job's uploaded images live: <slug>-<id>, the same
// naming cellName gives the cell dir and for the same reason.
//
// It takes the id explicitly rather than a *Job because its callers hold
// different things — a store row inside saveUploadToStore, a *Job in
// ArchiveJob, only a URL path segment in the api layer (which resolves the
// slug to an active job first). One function, so a writer and a reader cannot
// disagree about where the bytes live.
//
// The id is not cosmetic here. ArchiveJob does os.RemoveAll on this path, and
// while it was keyed on the bare slug that call would delete a same-slug
// ACTIVE job's materialized uploads — leaving that job's uploads rows in the
// database pointing at bytes no longer on disk. The old job silently
// damaging the new one, the mirror image of the collisions this change
// exists to fix.
//
// An empty post-slugify slug or a non-positive id returns "" so destructive
// callers (RemoveAll) can never resolve to the shared uploads root itself.
func UploadsDir(slug string, id int64) string {
	s := util.Slugify(slug)
	if s == "" || id < 1 {
		return ""
	}
	return filepath.Join(uploadsRoot(), s+"-"+strconv.FormatInt(id, 10))
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
//
// It sweeps every FORMER name's dir too. An uploads dir is materialized at
// SAVE time under whatever the job was called then, and a rename moves
// nothing, so a renamed job's images sit in one directory per name it has
// held — each still keyed by this job's id, which is what makes removing them
// safe: no other generation of the name can own a dir carrying this id.
func RemoveUploads(slug string) error {
	sr, err := ActiveStore().GetJobBySlug(slug)
	if err != nil {
		// No active job under that slug. The uploads dir is named
		// <slug>-<id>, so without an id there is no directory this function
		// may safely remove — and guessing one would risk another
		// generation's files. Nothing in the DB to remove either.
		return nil
	}
	if err := ActiveStore().DeleteUploadsForJob(sr.ID); err != nil {
		return err
	}
	// Best-effort across ALL of them, errors joined rather than returned on
	// the first: the dirs are independent, so abandoning the sweep on one
	// failure leaves the remaining former-name dirs behind for a reason that
	// has nothing to do with them — and those are exactly the ones no later
	// call will name, since a future RemoveUploads reaches them only through
	// this same loop. ArchiveJob's sibling sweep ignores its errors outright;
	// this one reports them, but it still finishes.
	var errs []error
	for _, s := range append([]string{slug}, sr.FormerSlugs...) {
		dir := UploadsDir(s, sr.ID)
		if dir == "" {
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
