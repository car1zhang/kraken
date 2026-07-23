package jobs

import (
	"errors"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync/atomic"
	"time"

	"kraken/internal/store"
	"kraken/internal/util"
)

// store.go — the jobs package's storage substrate switch. Every accessor
// below (LoadJobs, WriteJobFields, ArchiveJob, ...) checks ActiveStore()
// first and falls back to file-based behavior when no store is wired; the
// file-based code paths are kept under a "FromFiles" name where a
// store-backed sibling now shadows the original unqualified name (LoadJobs,
// LoadArchivedJobs), and the legacy importer keeps using ParseJob and the
// FromFiles loaders directly. UseStore is called exactly once, at kraken
// serve's startup, so every other entrypoint (the bubbletea viewer,
// existing api/jobs tests) keeps running unmodified against files.
//
// This shim is deliberate, not incidental: the OLD deployment (`kraken
// daemon` + `k web`, no store wired) must keep working on files alone
// until cutover to `kraken serve` — the FromFiles paths and this
// ActiveStore branch are scheduled for deletion in a post-cutover cleanup
// PR.
var activeStore atomic.Pointer[store.Store]

// UseStore wires the package onto a store: every accessor below switches
// from file-backed to store-backed behavior. Also wires util.LogEvent to
// additionally append into the store's events table (on top of, not
// instead of, its existing JSONL file write — see the doc comment on
// util.SetEventSink for why this stays additive) and clears any in-process
// daemon status left over from a previous store (a fresh serve process
// hasn't ticked yet).
//
// UseStore(nil) unwires it — the shape every store-backed test uses on
// cleanup so it never leaks into a later, unrelated file-mode test. Passing
// nil here must NOT install a sink closure that captures a nil *store.Store:
// util.LogEvent firing after that (a background goroutine mid-test-teardown,
// or simply a later test that forgot its own wiring) would call a method on
// a nil receiver and panic — so a nil st clears the sink
// instead of wrapping it.
func UseStore(st *store.Store) {
	activeStore.Store(st)
	if st == nil {
		util.SetEventSink(nil)
		inProcessDaemonStatus.Store(nil)
		return
	}
	util.SetEventSink(func(src, level, event, msg string) {
		_, _ = st.AppendEvent(store.Event{Src: src, Level: level, Event: event, Msg: msg})
	})
	// a fresh serve process hasn't ticked yet: drop any in-process daemon
	// status left over from a previous store on this process (there isn't
	// one in practice — UseStore is called exactly once, at kraken serve's
	// startup — but this keeps ReadDaemonStatus honest rather than serving
	// a stale status from before the wire).
	inProcessDaemonStatus.Store(nil)
}

// ActiveStore returns the wired store, or nil if UseStore was never called
// (the default for every entrypoint except kraken serve).
func ActiveStore() *store.Store {
	return activeStore.Load()
}

// jobFromStoreRow converts one store row (plus its prereqs/PRs) into a
// jobs.Job, mirroring ParseJob's shape so board-building code ports with
// minimal churn. File/ModTime read as empty/zero — a store-backed job has
// no backing file; File-dependent legacy paths (raw file editing in
// api/jobedit.go — S2: OldJobState, mentioned here previously, is gone;
// it was k-migrate-only dead code) are out of this slice's scope (next
// slice: the api handlers move onto the store's own accessors).
func jobFromStoreRow(sr *store.Job, prereqs []store.Prereq, prs []store.PR) *Job {
	j := &Job{
		ID:       sr.ID,
		Slug:     sr.Slug,
		Type:     sr.Type,
		Repo:     sr.Repo,
		Base:     sr.Base,
		Ready:    sr.Ready,
		Solo:     sr.Solo,
		Model:    sr.Model,
		SkipDoc:  sr.SkipDoc,
		Body:     sr.Body,
		State:    sr.State,
		Outcome:  sr.Outcome,
		Session:  sr.Session,
		Teardown: sr.Teardown,
		Run:      int(sr.Run),
		Rev:      sr.Rev,
		ModTime:  time.UnixMilli(sr.UpdatedAt),
		// L6: mirrors file mode's ParseJob-derived Err — a store-backed job
		// whose last save didn't parse pins to "draft" in deriveState the
		// same way an unparseable file does.
		Err: sr.Err,
	}
	if sr.Archived() {
		j.ArchivedName = sr.ArchivedName
	}
	for _, p := range prereqs {
		j.Prereqs = append(j.Prereqs, Prereq{Job: p.Slug, Gated: p.Gated})
	}
	for _, p := range prs {
		j.PRs = append(j.PRs, JobPR{URL: p.URL, State: p.State,
			LastCommentAt: p.LastCommentAt, BumpedSHA: p.BumpedSHA})
	}
	return j
}

// loadJobFull fetches one store job row plus its prereqs/PRs and converts
// it, so every caller building a *Job from the store goes through the same
// three-call shape.
func loadJobFull(st *store.Store, sr *store.Job) (*Job, error) {
	prereqs, err := st.ListPrereqs(sr.ID)
	if err != nil {
		return nil, err
	}
	prs, err := st.ListPRs(sr.ID)
	if err != nil {
		return nil, err
	}
	return jobFromStoreRow(sr, prereqs, prs), nil
}

// loadJobsFromStore fetches every active job row (sorted by slug, matching
// LoadJobsFromFiles' ordering contract) plus its prereqs/PRs — via
// LoadJobsWithChildren (L12), ONE transaction covering the jobs query and
// every row's prereqs/PRs, instead of the 1+2N separate queries this used
// to issue (a write landing between any two of them could otherwise mix
// state from two different moments into the loaded board). Converts each
// row and re-runs the same post-load bookkeeping LoadJobsFromFiles
// performs (slug-collision and prereq-cycle marking) so store-backed and
// file-backed boards agree on Err annotations.
func loadJobsFromStore(st *store.Store) ([]*Job, error) {
	rows, prereqsByID, prsByID, err := st.LoadJobsWithChildren()
	if err != nil {
		return nil, err
	}
	out := make([]*Job, 0, len(rows))
	for _, sr := range rows {
		out = append(out, jobFromStoreRow(sr, prereqsByID[sr.ID], prsByID[sr.ID]))
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].Slug < out[b].Slug })
	markSlugIssues(out)
	markPrereqCycles(out)
	return out, nil
}

// loadArchivedJobsFromStore mirrors loadJobsFromStore for the archive:
// LoadArchivedJobsWithChildren's single-transaction snapshot (L12),
// already ordered by archived_name, same ordering LoadArchivedJobsFromFiles
// uses.
func loadArchivedJobsFromStore(st *store.Store) ([]*Job, error) {
	rows, prereqsByID, prsByID, err := st.LoadArchivedJobsWithChildren()
	if err != nil {
		return nil, err
	}
	out := make([]*Job, 0, len(rows))
	for _, sr := range rows {
		out = append(out, jobFromStoreRow(sr, prereqsByID[sr.ID], prsByID[sr.ID]))
	}
	return out, nil
}

// writeJobFieldsToStore is WriteJobFields' store-backed path: converts the
// keyvals into a fields map via jobFieldValue and applies them through
// store.UpdateJobFields, gated on j.ID/j.Rev. On success it mirrors the
// same field-by-field local update WriteJobFields' file path performs, plus
// refreshes j.Rev to the value UpdateJobFields returns.
//
// On store.ErrStaleRev — another writer touched this job between j's load
// and this call — it retries exactly once: re-fetch the row, take its
// current Rev, and reapply ONLY this call's own named fields on top (never
// j's other in-memory fields, which may themselves be stale). This mirrors
// the file path's re-read-then-patch behavior, where two writers touching
// DIFFERENT fields both land because each patch starts from the bytes on
// disk at write time. A second collision inside that narrow retry window is
// rare enough to surface as a genuine error rather than loop indefinitely.
func writeJobFieldsToStore(st *store.Store, j *Job, keyvals ...string) error {
	if len(keyvals)%2 != 0 {
		return fmt.Errorf("write job fields for %s: odd keyvals", j.Slug)
	}
	fields := make(map[string]any, len(keyvals)/2)
	for i := 0; i+1 < len(keyvals); i += 2 {
		fields[keyvals[i]] = jobFieldValue(keyvals[i], keyvals[i+1])
	}
	rev, err := st.UpdateJobFields(j.ID, j.Rev, fields)
	if errors.Is(err, store.ErrStaleRev) {
		fresh, gerr := st.GetJobByID(j.ID)
		if gerr != nil {
			return fmt.Errorf("write job fields for %s: refresh after stale rev: %w", j.Slug, gerr)
		}
		j.Rev = fresh.Rev
		rev, err = st.UpdateJobFields(j.ID, j.Rev, fields)
	}
	if err != nil {
		return fmt.Errorf("write job fields for %s: %w", j.Slug, err)
	}
	j.Rev = rev
	for i := 0; i+1 < len(keyvals); i += 2 {
		switch keyvals[i] {
		case "state":
			j.State = keyvals[i+1]
		case "outcome":
			j.Outcome = keyvals[i+1]
		case "session":
			j.Session = keyvals[i+1]
		case "teardown":
			j.Teardown = keyvals[i+1]
		case "run":
			if n, err := strconv.Atoi(keyvals[i+1]); err == nil {
				j.Run = n
			}
		}
	}
	return nil
}

// writeJobPRsToStore is writeJobPRs' store-backed path. Store rows are
// per-URL (job_id, url) so there's no file-race union to reconstruct by
// hand the way the file path does: UpsertPR is itself the union — a
// concurrently-attached URL missing from prs simply keeps its own row
// untouched by this call. To preserve the "never drop a recorded URL"
// contract that j.PRs (the in-memory mirror) must also honor, this reads
// the current DB set first and folds in any URL prs omitted before
// upserting, exactly mirroring the file path's semantics.
func writeJobPRsToStore(st *store.Store, j *Job, prs []JobPR) error {
	existing, err := st.ListPRs(j.ID)
	if err != nil {
		return fmt.Errorf("write job prs for %s: %w", j.Slug, err)
	}
	have := make(map[string]bool, len(prs))
	for _, p := range prs {
		have[p.URL] = true
	}
	merged := append([]JobPR{}, prs...)
	for _, p := range existing {
		if !have[p.URL] {
			merged = append(merged, JobPR{URL: p.URL, State: p.State,
				LastCommentAt: p.LastCommentAt, BumpedSHA: p.BumpedSHA})
		}
	}
	for _, p := range merged {
		if err := st.UpsertPR(j.ID, p.URL, p.State); err != nil {
			return fmt.Errorf("write job prs for %s: %w", j.Slug, err)
		}
	}
	j.PRs = merged
	return nil
}

// appendJobPRToStore is appendJobPR's store-backed path: UpsertPR itself
// is the idempotent attach, so this only needs to distinguish "already
// there" (no-op, attached=false) from a genuinely new URL.
func appendJobPRToStore(st *store.Store, j *Job, pr JobPR) (attached bool, err error) {
	existing, err := st.ListPRs(j.ID)
	if err != nil {
		return false, fmt.Errorf("attach pr for %s: %w", j.Slug, err)
	}
	for _, p := range existing {
		if p.URL == pr.URL {
			return false, nil
		}
	}
	if err := st.UpsertPR(j.ID, pr.URL, pr.State); err != nil {
		return false, fmt.Errorf("attach pr for %s: %w", j.Slug, err)
	}
	j.PRs = append(j.PRs, pr)
	return true, nil
}

// updatePRWatermarkToStore is updatePRWatermark's store-backed path:
// store.SetPRWatermark is itself a no-op for an unrecorded URL, mirroring
// the file path's dropped-between-load-and-write tolerance. The in-memory
// copy updates only when the URL is still present.
func updatePRWatermarkToStore(st *store.Store, j *Job, url, lastCommentAt, bumpedSHA string) error {
	if err := st.SetPRWatermark(j.ID, url, lastCommentAt, bumpedSHA); err != nil {
		return fmt.Errorf("pr watermark for %s: %w", j.Slug, err)
	}
	for i := range j.PRs {
		if j.PRs[i].URL == url {
			j.PRs[i].LastCommentAt = lastCommentAt
			j.PRs[i].BumpedSHA = bumpedSHA
			break
		}
	}
	return nil
}

// removeJobPRsFromStore is removeJobPRs' store-backed path: re-reads the
// current DB set (mirroring the file path's read-fresh-then-filter shape,
// so a concurrently attached PR absent from drop survives untouched) and
// deletes only the rows named in drop.
func removeJobPRsFromStore(st *store.Store, j *Job, drop map[string]bool) (removed []string, err error) {
	existing, err := st.ListPRs(j.ID)
	if err != nil {
		return nil, fmt.Errorf("remove prs for %s: %w", j.Slug, err)
	}
	var kept []JobPR
	for _, p := range existing {
		if drop[p.URL] {
			if err := st.RemovePR(j.ID, p.URL); err != nil {
				return nil, fmt.Errorf("remove prs for %s: %w", j.Slug, err)
			}
			removed = append(removed, p.URL)
			continue
		}
		kept = append(kept, JobPR{URL: p.URL, State: p.State,
			LastCommentAt: p.LastCommentAt, BumpedSHA: p.BumpedSHA})
	}
	if len(removed) == 0 {
		return nil, nil
	}
	j.PRs = kept
	return removed, nil
}

// saveUploadToStore is SaveUpload's store-backed path: writes the blob to
// the uploads table (mime sniffed from ext via the stdlib mime package,
// same source api/uploads.go would otherwise guess from), then
// materializes the same bytes to UploadsDir(slug)/<name> so the returned
// path is a real, readable file — SaveUpload's contract even store-backed.
//
// E13: UploadsDir(slug) is validated non-empty BEFORE the store insert,
// not after — a slug that slugifies to nothing (all punctuation, say)
// used to insert the DB row FIRST and only then discover it had nowhere
// to materialize the file, leaving an orphaned upload row in the table
// whose bytes were never written to disk (api.UploadPath's file-based
// lookup — still consulted alongside the DB by some callers — would find
// nothing there). Failing before the insert means a bad slug leaves zero
// trace in the store, matching the file-mode SaveUpload's own
// check-before-write ordering (uploads.go).
func saveUploadToStore(st *store.Store, slug, ext string, data []byte) (name, path string, err error) {
	dir := UploadsDir(slug)
	if dir == "" {
		return "", "", fmt.Errorf("slug %q slugifies to nothing", slug)
	}
	sr, err := st.GetJobBySlug(slug)
	if err != nil {
		return "", "", fmt.Errorf("save upload: no active job %q: %w", slug, err)
	}
	mimeType := mime.TypeByExtension("." + ext)
	u, err := st.SaveUpload(sr.ID, ext, mimeType, data)
	if err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}
	path = filepath.Join(dir, u.Name)
	if err := util.AtomicWrite(path, string(data)); err != nil {
		return "", "", err
	}
	return u.Name, path, nil
}

// jobFieldValue converts one of WriteJobFields' variadic string keyvals
// into the typed value store.UpdateJobFields expects, per jobFieldColumns'
// column types (bool for ready/solo/skip_doc, int64 for run, string
// otherwise). Unknown keys pass through as strings; store.UpdateJobFields
// itself rejects anything outside its allowlist.
func jobFieldValue(key, val string) any {
	switch key {
	case "ready", "solo", "skip_doc":
		return val == "true"
	case "run":
		n, _ := strconv.Atoi(val)
		return int64(n)
	default:
		return val
	}
}
