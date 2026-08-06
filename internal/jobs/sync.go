package jobs

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"kraken/internal/store"
)

// sync.go — block-event artifact sync (spec Q11: a live cell's artifacts/
// tree is mirrored into the store's artifacts table as a TRUE mirror, not
// an append-only log). Two triggers call into this, both running only
// inside `kraken serve` (the only runtime with a live cell to sync, and the
// only one that wires a store):
//
//  1. daemon.go's tick(), on a job's working→blocked roll-call transition
//     (syncArtifactsNow, called from tick()'s per-job loop).
//  2. cell.go's teardownCell, unconditionally, right before it removes the
//     cell dir — the mandatory final pass: whatever ListArtifacts sees at
//     that instant is the last chance to capture it before the files are
//     gone for good (done, kill, and a resumed interrupted teardown all
//     funnel through teardownCell, so one call site covers all three).
//
// Between those two triggers a store-backed job's artifacts table can lag
// the live cell dir by up to one working→blocked cycle — acceptable: Q12
// already has live cells serving artifacts straight from the cell dir, so
// the table is a history/inspection copy until the cell is actually gone,
// at which point the final pass has already run.
//
// The file also carries the mirror's one INVERSE, restoreCellArtifacts
// (bottom of file): ReactivateJob writes the stored rows back into a
// recreated cell dir, so the mirror teardown captured is what the revived
// cell wakes up to.

// syncArtifactsNow mirrors j's live cell artifacts into the store. Both call
// sites run only inside kraken serve, where a store is always wired and
// every job it schedules is store-backed (real ID) — so no nil-store or
// zero-ID guard is needed here. reason is a short label for the soft-fail
// log line only. Errors here must never block scheduling or teardown, so
// they're logged and swallowed.
func syncArtifactsNow(j *Job, reason string) {
	st := ActiveStore()
	if err := syncCellArtifacts(st, j.ID, j); err != nil {
		dlog("%s: artifact sync (%s) failed: %v", j.Slug, reason, err)
	}
}

// syncCellArtifacts walks j's live cell artifacts dir (ListArtifacts —
// absolute paths, hidden-file rules already applied, subdirs preserved)
// and mirrors it into jobID's artifacts table with one SyncArtifacts call:
// every changed/new file is upserted with fresh bytes, every file whose
// (path, mtime-millis, size) already matches the stored row is passed as
// "unchanged" — ListArtifactMeta means this comparison never reads a blob
// column for a file about to be skipped — and any stored path missing from
// the walk is deleted by SyncArtifacts itself: this function never decides
// what to drop, only what's still there and whether it changed.
//
// L3: the artifacts root is os.Stat-guarded before any of that runs — if it
// doesn't exist, the sync is SKIPPED entirely rather than proceeding with
// an empty file list. ListArtifacts silently returns [] when its WalkDir
// root is missing (unreadable-entry errors are swallowed by design), so
// without this guard a missing dir would read as "every artifact just
// vanished" and SyncArtifacts would delete every previously-synced row —
// mirror-to-empty. A missing root only means "nothing to look at right
// now" (e.g. a resumed teardown re-running after cellDir was already
// removed by an earlier, interrupted pass — see teardownCell's ordering:
// sync runs before the RemoveAll that takes the dir away); it must never be
// read as "the artifacts were deleted." True-mirror semantics (spec Q11)
// still apply whenever the dir DOES exist: a file genuinely removed from a
// live root is still reflected as a row deletion.
func syncCellArtifacts(st *store.Store, jobID int64, j *Job) error {
	root := CellArtifacts(j)
	if _, err := os.Stat(root); err != nil {
		return nil // dir missing: skip the sync rather than mirror-to-empty
	}
	metas, err := st.ListArtifactMeta(jobID)
	if err != nil {
		return err
	}
	byRelPath := make(map[string]store.ArtifactMeta, len(metas))
	for _, m := range metas {
		byRelPath[m.Path] = m
	}

	paths := ListArtifacts(j)
	files := make([]store.ArtifactFile, 0, len(paths))
	unchanged := make(map[string]bool, len(paths))
	for _, p := range paths {
		rel, err := filepath.Rel(root, p)
		if err != nil {
			continue // outside root: shouldn't happen, skip rather than error the whole sync
		}
		// DB paths are slash-separated regardless of host OS:
		// filepath.Rel returns the OS separator, which is already "/" on
		// macOS/Linux, but ToSlash keeps the stored/compared key correct if
		// this ever runs on a host where it isn't.
		rel = filepath.ToSlash(rel)
		info, err := os.Stat(p)
		if err != nil {
			continue // vanished between list and stat — absent from files/unchanged, so SyncArtifacts drops it if it stays gone
		}
		mtimeMillis := info.ModTime().UnixMilli()
		// (mtime-millis, size) is a heuristic, not a content hash (finding
		// 19): a file rewritten with identical size within the same
		// millisecond would be (incorrectly) treated as unchanged and its
		// new bytes never re-synced until it next changes size or mtime.
		// Accepted — artifacts are agent-authored docs/diagrams on a
		// multi-second edit cadence, not adversarial input racing the
		// clock.
		if prior, ok := byRelPath[rel]; ok && prior.Mtime == mtimeMillis && prior.Size == info.Size() {
			unchanged[rel] = true
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			continue // unreadable: skip it this pass rather than fail the whole sync
		}
		files = append(files, store.ArtifactFile{Path: rel, Bytes: data, Mtime: mtimeMillis})
	}
	return st.SyncArtifacts(jobID, files, unchanged)
}

// restoreCellArtifacts is syncCellArtifacts' inverse, run at reactivation:
// it writes jobID's stored artifact rows back into j's cell artifacts dir,
// so a done job's deliverables come back with its cell instead of the cell
// resuming over an empty dir. It restores ONLY into a root that does not
// exist — a surviving artifacts dir (a mid-run "resume" reactivation whose
// cell dir was never torn down) is the CURRENT truth, deletions included:
// any path the mirror holds that such a dir lacks is a file zeus deleted or
// renamed since the last sync, and writing it back would un-delete it, with
// the next sync re-mirroring the resurrected file as canonical. The
// absent-root gate is also what makes the restore's writes safe: a dir that
// doesn't exist holds no symlinks for MkdirAll/WriteFile to follow out of
// the root. Callers therefore run this BEFORE creating the dir; the rows
// are read before any write, so a concurrent sync pass can at worst wipe
// rows the files on disk re-add at the next sync, never the reverse.
// Each stored mtime is re-stamped onto its restored file so the next sync's
// (path, mtime, size) comparison reads it as unchanged rather than
// re-upserting an identical blob; a failed Chtimes only costs that one
// redundant upsert, so it is best-effort.
//
// Errors surface to the caller instead of being swallowed like the sync
// direction's: an existing-but-incomplete dir is exactly the shape the
// true-mirror sync above reads as "these artifacts were deleted" — a
// restore that half-lands and proceeds anyway would have the next teardown
// wipe the stored history for whatever never made it to disk. The abort
// also REMOVES whatever it already wrote: the gate above guarantees the
// root did not exist at entry, so the removal can destroy nothing
// pre-existing — while a partial dir left behind would turn the next
// attempt's gate into a silent no-op over a half-restored cell, the very
// wipe shape this function exists to prevent. That cleanup is what makes
// the abort retryable for the errors that actually occur (a full disk, a
// permission); a row that can never be written — a file/dir path conflict
// from a hand-edited table — stays an honest refusal on every attempt
// rather than degrading into a silent partial success on the second.
func restoreCellArtifacts(st *store.Store, jobID int64, j *Job) error {
	root := CellArtifacts(j)
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		return nil // dir survived (or is unreadable): disk is the truth, restore nothing
	}
	arts, err := st.ListArtifacts(jobID)
	if err != nil {
		return err
	}
	abort := func(err error) error {
		if rmErr := os.RemoveAll(root); rmErr != nil {
			return fmt.Errorf("%v (and removing the partial restore failed: %v — remove %s by hand before retrying)", err, rmErr, root)
		}
		return err
	}
	for _, a := range arts {
		rel := filepath.FromSlash(a.Path)
		// stored paths come from our own walk and are always local, but a
		// row is still external input to THIS process — never let one write
		// outside the artifacts dir
		if !filepath.IsLocal(rel) {
			continue
		}
		dst := filepath.Join(root, rel)
		if _, err := os.Lstat(dst); err == nil {
			continue // an earlier row already claimed this path (case-fold collision)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return abort(fmt.Errorf("restore artifact %s: %v", a.Path, err))
		}
		if err := os.WriteFile(dst, a.Bytes, 0o644); err != nil {
			return abort(fmt.Errorf("restore artifact %s: %v", a.Path, err))
		}
		if a.Mtime > 0 {
			mt := time.UnixMilli(a.Mtime)
			_ = os.Chtimes(dst, mt, mt)
		}
	}
	return nil
}
