package jobs

import (
	"os"
	"path/filepath"

	"kraken/store"
)

// sync.go — block-event artifact sync (spec Q11: a live cell's artifacts/
// tree is mirrored into the store's artifacts table as a TRUE mirror, not
// an append-only log). Two triggers call into this, both wired as no-ops
// when no store is wired (k daemon / file-mode never touches the artifacts
// table):
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

// syncArtifactsNow mirrors j's live cell artifacts into the store, when a
// store is wired and j carries a store id (a file-backed Job, ID == 0,
// makes this a no-op — there is nothing to sync into). reason is a
// short label for the soft-fail log line only. Errors here must never
// block scheduling or teardown, so they're logged and swallowed.
func syncArtifactsNow(j *Job, reason string) {
	st := ActiveStore()
	if st == nil || j.ID == 0 {
		return
	}
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
