package jobs

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"kraken/internal/store"
)

// sync.go — block-event artifact sync (spec Q11: a live cell's artifacts/
// tree is mirrored into the store's artifacts table as a TRUE mirror, not
// an append-only log). Three triggers call into this, all running only
// inside `kraken serve` (the only runtime with a live cell to sync, and the
// only one that wires a store):
//
//  1. daemon.go's tick(), once on serve start-up (its first tick), for every
//     started, not-done job — a blocked job's transition may have happened
//     under a previous process (syncArtifactsNow, reason "serve-startup").
//  2. daemon.go's tick(), on a job's working→blocked roll-call transition
//     (syncArtifactsNow, called from tick()'s per-job loop).
//  3. cell.go's teardownCell (syncArtifactsAtTeardown), unconditionally,
//     after the session kill and before the worktree and cell dir are
//     removed — the mandatory final pass: whatever it sees at that instant
//     is the last chance to capture it before the files are gone for good
//     (done, kill, and a resumed interrupted teardown all funnel through
//     teardownCell, so one call site covers all three).
//
// The mirrored set is the cell's artifacts/ tree plus the worktree's
// uncommitted root-level *.md (rootNotes), stored under root/<name>. The
// table is the ONLY record of a torn-down job's deliverables: nothing copies
// them anywhere else, which is why the teardown pass fails the teardown
// rather than letting it delete what it could not save.
//
// Between those triggers a store-backed job's artifacts table can lag
// the live cell dir by up to one working→blocked cycle — acceptable: Q12
// already has live cells serving artifacts straight from the cell dir, so
// the table is a history/inspection copy until the cell is actually gone,
// at which point the final pass has already run.
//
// The file also carries the mirror's one INVERSE, restoreCellArtifacts
// (bottom of file): ReactivateJob writes the stored rows back into a
// recreated cell dir, so the mirror teardown captured is what the revived
// cell wakes up to.

// syncArtifactsNow mirrors j's live cell artifacts into the store. Its only
// callers are the daemon's tick (serve startup, and a working→blocked
// transition), inside kraken serve, where a store is always wired and every
// job it schedules is store-backed (real ID); the nil-store return only
// keeps a storeless caller from panicking. reason is a short label for the
// soft-fail log line only. Errors here must never block scheduling, so
// they're logged and swallowed. Teardown's pass is syncArtifactsAtTeardown,
// which fails loudly instead.
func syncArtifactsNow(j *Job, reason string) {
	st := ActiveStore()
	if st == nil {
		return
	}
	if err := mirrorCellArtifacts(st, j.ID, j, false); err != nil {
		dlog("%s: artifact sync (%s) failed: %v", j.Slug, reason, err)
	}
}

// syncArtifactsAtTeardown is teardownCell's final pass, and unlike
// syncArtifactsNow it RETURNS its error: the table is the only record of a
// torn-down job's deliverables, so teardownCell refuses to remove the
// worktree or the cell dir when this fails. It is also strict about reads
// (mirrorCellArtifacts' atTeardown): an artifact or root note that exists
// but cannot be read fails the pass instead of being skipped, since the
// skip would be followed by the delete of its only copy. No store wired is
// nil — there is nothing to sync into, and nothing has ever been synced.
func syncArtifactsAtTeardown(j *Job) error {
	st := ActiveStore()
	if st == nil {
		return nil
	}
	return mirrorCellArtifacts(st, j.ID, j, true)
}

// syncCellArtifacts walks j's live cell artifacts dir (ListArtifacts —
// absolute paths, hidden-file rules already applied, subdirs preserved),
// adds the worktree's uncommitted root notes (rootNotes) under root/<name>,
// and mirrors that set into jobID's artifacts table with one SyncArtifacts
// call:
// every changed/new file is upserted with fresh bytes, every file whose
// (path, mtime-millis, size) already matches the stored row is passed as
// "unchanged" — ListArtifactMeta means this comparison never reads a blob
// column for a file about to be skipped — and any stored path missing from
// the walk is deleted by SyncArtifacts itself: this function never decides
// what to drop, only what's still there and whether it changed.
//
// L3: the artifacts root is os.Stat-guarded before any of that runs — if it
// doesn't exist, the artifacts/ half of the sync is skipped and every stored
// non-root row is kept, rather than proceeding with an empty file list.
// ListArtifacts silently returns [] when its WalkDir root is missing
// (unreadable-entry errors are swallowed by design), so without this guard
// a missing dir would read as "every artifact just vanished" and
// SyncArtifacts would delete every previously-synced row — mirror-to-empty.
// A missing root only means "nothing to look at right now" (e.g. a resumed
// teardown re-running after cellDir was already removed by an earlier,
// interrupted pass — see teardownCell's ordering: sync runs before the
// RemoveAll that takes the dir away); it must never be read as "the
// artifacts were deleted." The root notes are still captured. True-mirror
// semantics (spec Q11) still apply whenever the dir DOES exist: a file
// genuinely removed from a live root is still reflected as a row deletion.
//
// An EMPTIED-but-present root with rows still stored gets the same skip,
// for the same reason from a different direction: a done-then-reactivate
// cycle recreates the cell dir with an empty artifacts/ while the store
// still mirrors the set, and a wipe by anything outside the session leaves
// the dir present and bare — two cells hit exactly this in one day, and on
// both the next roll-call sync would have deleted every row, converting a
// recoverable disk loss into a permanent one. Dropping from N rows to zero
// files is never a legitimate true-mirror event on a live cell (deleting
// the LAST artifact on purpose is not a shape any cell produces — the set
// always keeps its 00-overview), so zero-files-with-rows reads as
// "catastrophe upstream", not as a deletion to mirror. Partial deletions
// still mirror: the guard fires only when nothing at all survives the walk.
func syncCellArtifacts(st *store.Store, jobID int64, j *Job) error {
	return mirrorCellArtifacts(st, jobID, j, false)
}

// rootNotePrefix is the stored path prefix of a root note: the worktree's
// NOTES.md is the row root/NOTES.md. A path of its own, rather than the
// bare name, so a root note can never collide with — or be restored as — an
// artifacts/ file of the same name.
const rootNotePrefix = "root/"

// mirrorCellArtifacts is syncCellArtifacts with one teardown-only switch,
// atTeardown: an artifact or root note that exists but cannot be read (or
// stat'd), an artifacts subdirectory that cannot be listed, and an
// artifacts dir that exists but cannot be stat'd all fail the pass rather
// than being skipped, because at teardown the skip is followed by the
// delete of the only copy. A file that vanished between the listing and the
// read (ENOENT) is still a skip either way.
//
// Root notes (rootNotes) are stored under rootNotePrefix. An artifacts/
// file at the same path (artifacts/root/<name>) wins: one row, its bytes.
// When rootNotes cannot answer (no worktree, not a checkout, git failed)
// every stored root/ row is kept as UNSEEN rather than mirrored away — a
// teardown resumed after the worktree was removed already captured them,
// and that row is then their only copy. A root/ row is also kept while the
// worktree holds a different file of its name (see the keep loop at the
// end).
func mirrorCellArtifacts(st *store.Store, jobID int64, j *Job, atTeardown bool) error {
	root := CellArtifacts(j)
	artifactsMissing := false
	if _, err := os.Stat(root); err != nil {
		if !os.IsNotExist(err) {
			if atTeardown {
				return fmt.Errorf("stat artifacts dir: %w", err)
			}
			return nil // unreadable root: skip the sync rather than mirror-to-empty
		}
		// missing: no artifacts/ to walk, but root notes are still captured,
		// and every stored non-root row is kept as unseen below
		artifactsMissing = true
	}
	metas, err := st.ListArtifactMeta(jobID)
	if err != nil {
		return err
	}
	byRelPath := make(map[string]store.ArtifactMeta, len(metas))
	for _, m := range metas {
		byRelPath[m.Path] = m
	}

	// The walk set: every file under artifacts/ keyed by its slash-separated
	// relative path, then the worktree's uncommitted root notes under
	// root/<bare name>. An artifacts/ path wins a collision — it is the
	// deliberate deliverable, the root note the incidental one.
	type candidate struct{ rel, abs string }
	var cands []candidate
	seen := map[string]bool{}
	var listed []string
	switch {
	case artifactsMissing:
	case atTeardown:
		// strict: a subdirectory that cannot be listed would otherwise drop
		// its files from the walk, and the mirror would delete their rows
		// right before teardown deletes the files
		if listed, err = listArtifactsStrict(root); err != nil {
			return err
		}
	default:
		listed = ListArtifacts(j)
	}
	for _, p := range listed {
		rel, err := filepath.Rel(root, p)
		if err != nil {
			continue // outside root: shouldn't happen, skip rather than error the whole sync
		}
		// DB paths are slash-separated regardless of host OS:
		// filepath.Rel returns the OS separator, which is already "/" on
		// macOS/Linux, but ToSlash keeps the stored/compared key correct if
		// this ever runs on a host where it isn't.
		rel = filepath.ToSlash(rel)
		cands = append(cands, candidate{rel, p})
		seen[rel] = true
	}
	wt := jobWorktree(j)
	notes, notesOK := rootNotes(wt)
	for _, name := range notes {
		rel := rootNotePrefix + name
		if seen[rel] {
			continue
		}
		cands = append(cands, candidate{rel, filepath.Join(wt, name)})
		seen[rel] = true
	}

	files := make([]store.ArtifactFile, 0, len(cands))
	unchanged := make(map[string]bool, len(cands))
	for _, c := range cands {
		info, err := os.Lstat(c.abs)
		if err != nil {
			if atTeardown && !os.IsNotExist(err) {
				return fmt.Errorf("stat %s: %w", c.rel, err)
			}
			continue // vanished between list and stat — absent from files/unchanged, so SyncArtifacts drops it if it stays gone
		}
		// The walk lists a symlink, a FIFO or a device as a file. None of
		// them is a document — ReadFile fails on a directory and can block
		// forever on a FIFO or /dev/zero — so it is skipped (logged once per
		// job and path), never read and never fatal, at teardown too. Lstat
		// rather than Stat, so a symlink is skipped as itself even when it
		// points at a regular file: following it would store the TARGET's
		// bytes under the link's name, and the artifacts table is what every
		// non-live job serves, to other nodes included, so a link to a file
		// outside artifacts/ would publish that file. A symlinked artifact
		// is therefore absent from the stored set.
		if !info.Mode().IsRegular() {
			logNonRegularOnce(j, c.rel, info.Mode())
			continue
		}
		mtimeMillis := info.ModTime().UnixMilli()
		// (mtime-millis, size) is a heuristic, not a content hash (finding
		// 19): a file rewritten with identical size within the same
		// millisecond would be (incorrectly) treated as unchanged and its
		// new bytes never re-synced until it next changes size or mtime.
		// Accepted — artifacts are agent-authored docs/diagrams on a
		// multi-second edit cadence, not adversarial input racing the
		// clock.
		if prior, ok := byRelPath[c.rel]; ok && prior.Mtime == mtimeMillis && prior.Size == info.Size() {
			unchanged[c.rel] = true
			continue
		}
		data, err := os.ReadFile(c.abs)
		if err != nil {
			if atTeardown && !os.IsNotExist(err) {
				return fmt.Errorf("read %s: %w", c.rel, err)
			}
			continue // unreadable: skip it this pass rather than fail the whole sync
		}
		files = append(files, store.ArtifactFile{Path: c.rel, Bytes: data, Mtime: mtimeMillis})
	}
	// The guard tests the EFFECTIVE set (files ∪ unchanged), after the loop,
	// rather than the walk's path list before it: a wipe landing during the
	// walk enumerates N stale entries whose every Stat then fails, which is
	// zero effective files off a non-empty path list — same catastrophe, one
	// millisecond narrower. It is taken BEFORE the unseen rows below are
	// added, so kept rows can never carry an emptied artifacts dir past it.
	// A MISSING dir is not an emptied one: its rows are all kept below.
	if !artifactsMissing && len(files)+len(unchanged) == 0 && len(metas) > 0 {
		return nil // emptied root with rows stored: skip rather than mirror the loss
	}
	for p := range byRelPath {
		if seen[p] {
			continue
		}
		if !strings.HasPrefix(p, rootNotePrefix) {
			// no artifacts/ dir to look at: unseen, not deleted
			if artifactsMissing {
				unchanged[p] = true
			}
			continue
		}
		// A root/ row rootNotes did not list is kept when rootNotes could
		// not answer, and when the worktree holds a CLEAN file of that name
		// whose bytes differ from the row: that is a note reactivation left
		// as a row because the fresh checkout already had a different file
		// (writeRootNotes never overwrites). A note zeus committed as
		// captured matches its row and is mirrored away; so is one whose
		// file is gone.
		if !notesOK {
			unchanged[p] = true
			continue
		}
		if rootRowDiffersFromFile(st, jobID, p, byRelPath[p], filepath.Join(wt, strings.TrimPrefix(p, rootNotePrefix))) {
			unchanged[p] = true
		}
	}
	return st.SyncArtifacts(jobID, files, unchanged)
}

// nonRegularLogged dedups logNonRegularOnce: "<job id>\x00<rel>" → logged.
var nonRegularLogged sync.Map

// logNonRegularOnce logs, once per process for each job and path, that the
// sync skipped a candidate that is not a regular file.
func logNonRegularOnce(j *Job, rel string, mode fs.FileMode) {
	key := strconv.FormatInt(j.ID, 10) + "\x00" + rel
	if _, dup := nonRegularLogged.LoadOrStore(key, true); dup {
		return
	}
	dlog("%s: artifact sync skipped %s — not a regular file (%s)", j.Slug, rel, mode.Type())
}

// rootRowDiffersFromFile reports whether the worktree file at f exists and
// holds bytes other than the stored row p's. Sizes are compared first, so
// the blob is read only when they agree. A file that cannot be stat'd counts
// as ABSENT, so its row is dropped; a row or file that stats but cannot be
// read counts as differing — the row is kept rather than dropped on a guess.
func rootRowDiffersFromFile(st *store.Store, jobID int64, p string, meta store.ArtifactMeta, f string) bool {
	info, err := os.Stat(f)
	if err != nil {
		return false // no file: nothing holds a different version, the note is gone
	}
	if !info.Mode().IsRegular() || info.Size() != meta.Size {
		// a different size, or a directory/FIFO/device (never read — a FIFO
		// would block), is not the row's bytes, so the row is kept
		return true
	}
	a, err := st.GetArtifact(jobID, p)
	if err != nil {
		return true
	}
	data, err := os.ReadFile(f)
	if err != nil {
		return true
	}
	return string(data) != string(a.Bytes)
}

// listArtifactsStrict is ListArtifacts' walk (same hidden-file rules, same
// absolute paths, sorted) with one difference: an entry it cannot read — an
// unlistable subdirectory above all — is an ERROR rather than a skip. The
// teardown pass uses it because a skipped directory there means rows deleted
// and then files deleted, with nothing left anywhere.
func listArtifactsStrict(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("list %s: %w", p, err)
		}
		if d.IsDir() {
			if p != root && strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		out = append(out, p)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

// jobWorktree is j's cell worktree, or "" when it is gone. The worktree's
// directory name needs only the repo's base name, so a repo that no longer
// resolves under the repos dir (renamed, moved) still finds a worktree named
// for the job's own repo: value.
func jobWorktree(j *Job) string {
	if repo, err := ResolveRepo(j.Repo); err == nil {
		if wt := ExistingWorktree(j, repo); wt != "" {
			return wt
		}
	}
	if j.Repo == "" {
		return ""
	}
	return ExistingWorktree(j, j.Repo)
}

// rootNotes lists the bare names of the UNCOMMITTED (untracked or modified)
// root-level *.md files in the worktree wt, sorted: the docs a zeus leaves
// beside the code rather than in artifacts/ (dream cells' CLAUDE.md edits
// are the measured case). They have no other home once the worktree is
// removed, so syncCellArtifacts mirrors them into the artifacts table beside
// the artifacts/ tree. A rename is reported under its new name; a deletion
// is not reported (there is no file to capture); anything in a subdirectory
// is not a root note; a file kraken itself wrote (the codex AGENTS.md, which
// starts with codexAgentsHeader) is never one.
//
// ok is false when the question could not be answered — wt is "", is not
// the root of a git checkout, or `git status` failed (a linked worktree
// whose primary clone moved, say) — so a caller can tell "no root notes"
// from "could not look".
func rootNotes(wt string) (names []string, ok bool) {
	if wt == "" {
		return nil, false
	}
	// a checkout root carries .git (a dir in a primary clone, a file in a
	// linked worktree); without this check `git -C` would answer for
	// whatever repository happens to enclose wt
	if _, err := os.Lstat(filepath.Join(wt, ".git")); err != nil {
		return nil, false
	}
	// -z: NUL-separated and never C-quoted, so a name with a space or a
	// non-ASCII byte arrives verbatim; a rename's or copy's ORIGINAL path
	// follows its entry as one extra field
	out, err := exec.Command("git", "-C", wt, "status", "--porcelain", "-z").Output()
	if err != nil {
		return nil, false
	}
	fields := strings.Split(string(out), "\x00")
	for i := 0; i < len(fields); i++ {
		e := fields[i]
		if len(e) < 4 {
			continue
		}
		x, y, name := e[0], e[1], e[3:]
		if x == 'R' || x == 'C' || y == 'R' || y == 'C' {
			i++ // skip the original path
		}
		if x == 'D' || y == 'D' {
			continue
		}
		if strings.Contains(name, "/") || !strings.HasSuffix(name, ".md") {
			continue
		}
		if krakenWrote(filepath.Join(wt, name)) {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, true
}

// krakenWrote reports whether the file at p starts with the header kraken
// stamps on a root file it generates itself (codexAgentsHeader). A file that
// cannot be read is not claimed: the sync then reports it as unreadable
// rather than silently dropping it.
func krakenWrote(p string) bool {
	// only a regular file is opened: opening a FIFO for reading blocks
	// until a writer appears (the sync then skips it as non-regular)
	if info, err := os.Stat(p); err != nil || !info.Mode().IsRegular() {
		return false
	}
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, len(codexAgentsHeader))
	n, _ := io.ReadFull(f, head)
	return string(head[:n]) == codexAgentsHeader
}

// restoreCellArtifacts is syncCellArtifacts' inverse, run at reactivation:
// it writes jobID's stored artifact rows back into j's cell artifacts dir —
// except root/ rows, which go to the worktree root (see the end of the
// function) — so a done job's deliverables come back with its cell instead
// of the cell resuming over an empty dir. It restores ONLY into a root that does not
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
	var roots []store.Artifact
	for _, a := range arts {
		if strings.HasPrefix(a.Path, rootNotePrefix) {
			roots = append(roots, a) // a root note: goes back to the worktree, never into artifacts/
			continue
		}
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
	// Root notes go back to the worktree when one exists now. Without
	// overwriting: a worktree that already exists here is live content, and
	// a file it already holds is the current truth. ReactivateJob's
	// recreate path, whose worktree does not exist yet at this point, calls
	// restoreRootNotes itself once it has made a fresh one.
	if wt := jobWorktree(j); wt != "" {
		if _, _, err := writeRootNotes(roots, wt); err != nil {
			return abort(err)
		}
	}
	return nil
}

// restoreRootNotes writes jobID's stored root/ rows into the worktree wt
// under their bare names — the recreate path of ReactivateJob, right after
// it made a fresh worktree. It never overwrites (writeRootNotes): a file the
// fresh checkout already has is the CURRENT committed version, and a
// weeks-old snapshot written over it would silently revert every change
// merged since. Such a note stays a row (the sync keeps it while the
// checkout's file differs from it), and is also STAGED at
// <cell dir>/root-notes/<name> (stageDir; the cell dir is the worktree's
// parent) so zeus can Read the old copy during the live run — the artifacts
// verb cannot, since a live job's listing is its cell dir, not the store.
// The staged copy is kraken-owned scratch, overwritten on every
// reactivation, outside both the worktree and artifacts/, and removed with
// the cell dir at teardown like everything else in it.
// written and skipped name the notes each way, and stageDir is where the
// skipped ones were staged ("" when none were), all for the reorient prompt.
func restoreRootNotes(st *store.Store, jobID int64, wt string) (written, skipped []string, stageDir string, err error) {
	arts, err := st.ListArtifacts(jobID)
	if err != nil {
		return nil, nil, "", err
	}
	var roots []store.Artifact
	byName := map[string]store.Artifact{}
	for _, a := range arts {
		if strings.HasPrefix(a.Path, rootNotePrefix) {
			roots = append(roots, a)
			byName[strings.TrimPrefix(a.Path, rootNotePrefix)] = a
		}
	}
	written, skipped, err = writeRootNotes(roots, wt)
	if err != nil || len(skipped) == 0 {
		return written, skipped, "", err
	}
	stageDir = filepath.Join(filepath.Dir(wt), "root-notes")
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		return written, skipped, "", fmt.Errorf("stage root notes: %v", err)
	}
	for _, name := range skipped {
		if err := os.WriteFile(filepath.Join(stageDir, name), byName[name].Bytes, 0o644); err != nil {
			return written, skipped, "", fmt.Errorf("stage root note %s: %v", name, err)
		}
	}
	return written, skipped, stageDir, nil
}

// writeRootNotes writes each root/<name> row to wt/<name>, re-stamping its
// stored mtime so the next sync reads it as unchanged, and NEVER overwrites
// a file that already exists there (that note goes into skipped instead). A
// row whose name is not one plain file name (a nested or non-local path —
// never produced by rootNotes) is ignored rather than written anywhere else.
func writeRootNotes(roots []store.Artifact, wt string) (written, skipped []string, err error) {
	for _, a := range roots {
		name := strings.TrimPrefix(a.Path, rootNotePrefix)
		if name == "" || strings.Contains(name, "/") || !filepath.IsLocal(name) {
			continue
		}
		dst := filepath.Join(wt, name)
		if _, err := os.Lstat(dst); err == nil {
			skipped = append(skipped, name)
			continue
		}
		if err := os.WriteFile(dst, a.Bytes, 0o644); err != nil {
			return written, skipped, fmt.Errorf("restore root note %s: %v", name, err)
		}
		if a.Mtime > 0 {
			mt := time.UnixMilli(a.Mtime)
			_ = os.Chtimes(dst, mt, mt)
		}
		written = append(written, name)
	}
	return written, skipped, nil
}

// rootNotesReorient is the sentence the recreate path's reorient prompt
// carries about root notes: which were put back at the worktree root, and
// which were not because the fresh checkout already had the file, with the
// path their old copies were staged at (restoreRootNotes' stageDir). ""
// when there were none either way.
func rootNotesReorient(stageDir string, written, skipped []string) string {
	var parts []string
	if len(written) > 0 {
		parts = append(parts, "Uncommitted root-level .md files from the previous run (root notes) were restored to the worktree root (still uncommitted): "+strings.Join(written, ", ")+".")
	}
	if len(skipped) > 0 {
		parts = append(parts, "Not restored, because the fresh checkout already has a file of that name — the previous run's copy of each is in "+stageDir+"/ (Read it there; it is outside the worktree and never committed; compare each with the checkout's version and carry over what still applies): "+strings.Join(skipped, ", ")+".")
	}
	return strings.Join(parts, " ")
}
