package jobs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"kraken/internal/core"
	"kraken/internal/store"
)

// mm.go — the mental model's daemon-side half: the verify pass that decides
// when an item carl marked has died, and the invalidation that records it.
//
// Nothing here spends a model: keeping the anchors honest is mechanical
// enough to sit on a daemon tick.
//
// One rule shapes every branch below, and it is the invariant carl's mental
// model rests on: A FAILED READ IS NEVER AN INVALIDATION. A repo that will not
// resolve, a fetch with no network, a git call that times out, an artifact row
// that cannot be read — every one of those records `check_error` and leaves the
// item live. Only positive evidence that the exact text is gone may say
// "changed", because invalidation is one-way: nothing ever resurrects an item,
// so a wrong invalidation is permanent and silent, while a wrong survival costs
// one more pass.

// ---------------------------------------------------------------------------
// The verify pass
// ---------------------------------------------------------------------------

const (
	// mmVerifyInterval paces the whole pass. The daemon ticks every 15s; ten
	// minutes is the floor because the code half FETCHES, and a repo carl is
	// not pushing to has nothing to say more often than that.
	mmVerifyInterval = 10 * time.Minute

	// mmGitTimeout bounds ONE git call — one per call, never one budget shared
	// across a repo's pass, and mmGit below is the only place a context is
	// made so that stays true. The fetch is the call that can hang forever (a
	// socket left dead by a laptop sleeping mid-transfer can take macOS' TCP
	// keepalive hours to notice), and this whole half runs inside a
	// single-flighted goroutine whose deferred CAS release is the ONLY thing
	// that re-arms it. Unbounded, one dead fetch retires the verify pass for
	// the daemon's entire lifetime, with every anchor reading as verified.
	// Same reasoning and same shape as refreshUpdateAvail (update.go).
	//
	// A SHARED budget was the first spelling and it was wrong in a way that
	// cost items: a 55-second fetch on a bad uplink left five seconds for
	// every anchor in the repo, so each subsequent call failed instantly —
	// and a failed `git show` used to read as `file-deleted`, which
	// invalidated the tail of the anchor list permanently while the anchors
	// processed before the deadline survived, keeping the circuit breaker
	// disarmed. Per-call deadlines and the positive-evidence rule below are
	// the two halves of that fix; neither is sufficient alone.
	mmGitTimeout = 60 * time.Second

	// mmBreakerMin is the circuit breaker's floor: a repo with at least this
	// many live anchors, ALL of which would invalidate in one pass, is
	// evidence of a repo-level problem (a truncated clone, a fetch that
	// resolved to the wrong ref, a rewritten history) rather than of N
	// independent content changes. Below the floor "all of them" is too small
	// a sample to mean anything — one anchor in a one-anchor repo is just an
	// anchor that died.
	mmBreakerMin = 3
)

// mmLastPass is the pass throttle (unix millis), held in memory: losing it on a restart costs one extra pass, which is one fetch per marked
// repo and no writes when nothing moved.
var mmLastPass atomic.Int64

// mmCodeRunning single-flights the code half — the shape usagePassRunning
// uses, for the sharper version of its reason: this half
// makes network calls into real checkouts, and two of them overlapping would
// have two goroutines fetching the same repo and racing each other's
// re-anchors.
var mmCodeRunning atomic.Bool

// The dirty-repo set: an ACCELERANT for the head watermark, never the trigger
// of record. MarkMMRepoDirty is called from the PR tracker's newlyMerged block,
// which only ever sees kraken's own cell branches — so a repo carl pushed to
// from anywhere else is still caught by the ordinary head comparison on the
// next pass. What the mark buys is that a merge kraken watched lands in the
// mental model on the very next pass rather than whenever the watermark
// happens to disagree.
var (
	mmDirtyMu    sync.Mutex
	mmDirtyRepos = map[string]bool{}
)

// MarkMMRepoDirty forces the next verify pass to re-check one repo even when
// its head watermark has not moved.
func MarkMMRepoDirty(repo string) {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return
	}
	mmDirtyMu.Lock()
	defer mmDirtyMu.Unlock()
	mmDirtyRepos[repo] = true
}

// mmRepoDirty reads the flag WITHOUT clearing it. Clearing happens only after
// a repo's pass has actually completed (mmClearRepoDirty), so a pass that bails
// on a failed fetch does not consume the mark it was told to act on.
func mmRepoDirty(repo string) bool {
	mmDirtyMu.Lock()
	defer mmDirtyMu.Unlock()
	return mmDirtyRepos[repo]
}

func mmClearRepoDirty(repo string) {
	mmDirtyMu.Lock()
	defer mmDirtyMu.Unlock()
	delete(mmDirtyRepos, repo)
}

// mmHeadKey names the kv row holding the last origin/main sha a repo's anchors
// were verified against. A repo whose head has not moved has nothing to say, so
// this is what keeps a settled machine's pass down to one fetch per repo.
func mmHeadKey(repo string) string { return "mm_head:" + repo }

// mmRunGit runs one git command in dir under ctx — the swappable seam, the
// same pattern update.go's updateRunGit and rotation.go's gate use. Nothing in
// the jobs test suite may shell out, and the code half below is nothing but git
// calls, so every branch in it is reachable only through this var.
var mmRunGit = func(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(bytes.TrimSpace(ee.Stderr)) > 0 {
			return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(ee.Stderr)))
		}
		return "", fmt.Errorf("git %s: %v", strings.Join(args, " "), err)
	}
	return string(out), nil
}

// mmGit is how every git call in this file is made: one fresh mmGitTimeout
// context per CALL, released before the next one starts.
//
// One function so the property is structural. The alternative — a ctx threaded
// down from mmVerifyRepo — is what shipped first, and it silently turned the
// per-call bound into a per-repo one: the deadline is a property of the
// context's lifetime, so passing one context to twenty calls gives twenty
// calls one deadline between them, and nothing about the call sites says so.
// With the context made here there is no ctx parameter anywhere below for a
// later edit to accidentally share.
func mmGit(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), mmGitTimeout)
	defer cancel()
	return mmRunGit(ctx, dir, args...)
}

// MMVerifyPass keeps every live mental-model item honest against what its
// substrate says today. Self-throttled at mmVerifyInterval and skipped under
// --mirror by its caller (it fetches into real checkouts and writes rows).
//
// Two halves, split by what they cost. The ARTIFACT half is local reads out of
// kraken.db and runs inline. The CODE half fetches, so it runs in a
// single-flighted goroutine — the daemon tick derives every cell's state and
// must never sit behind a network call.
func MMVerifyPass(st *store.Store, now time.Time) {
	if st == nil {
		return // file-mode entrypoints have no mental model to verify
	}
	if last := mmLastPass.Load(); last != 0 && now.UnixMilli()-last < mmVerifyInterval.Milliseconds() {
		return
	}
	// Stamped HERE, before any of the work below can bail, for tokensPass'
	// reason: this pass has had its turn whatever comes of it, and a throttle
	// stamped only on success turns a persistent failure into a per-tick retry
	// that fetches every marked repo four times a minute.
	mmLastPass.Store(now.UnixMilli())

	if err := mmVerifyArtifacts(st, now.UnixMilli()); err != nil {
		dlogErr("mm verify: artifacts: %v", err)
	}
	kickMMCode(st, now.UnixMilli())
}

// mmVerifyArtifacts is the no-git half: whole-artifact and artifact-element
// readings, checked against what their document says today.
//
// THE SUBSTRATE IS THE LIVE CELL DIR'S FILE whenever that dir exists, and the
// `artifacts` table row only when it does not. That is not a preference — the
// two disagree, and the table is the one that lags. The mirror runs on exactly
// three triggers (serve startup, a working→blocked roll-call transition,
// teardown — sync.go states it outright), so a live cell's table row can be a
// whole working phase behind the file carl is reading, and reading a doc while
// its cell still works is the NORMAL way carl meets a fresh artifact: the ✓
// sits in the head of that very view. Verified against the table, a mark taken
// on a just-written artifact reads as `artifact-deleted` (no row yet) or
// `content-changed` (row one revision behind) within ten minutes, one-way and
// terminal, with a notification — reporting on a copy that is neither the one
// carl read nor the current one. The capture side already reads the file
// (`resolveArtifact`, home `cell`); this makes the check side agree with it.
//
// Whole artifact: any byte difference invalidates (Q3 — carl's call), decided
// by comparing SHAs rather than blobs. An artifact is a document carl read end
// to end, and a rewritten paragraph he has not seen is exactly the thing the
// mental model must stop claiming he knows.
//
// Element: the comparison is between ALPHANUMERIC PROJECTIONS of both sides,
// and that is the fix for a mismatch that made every table mark die on its
// first pass. An element mark freezes RENDERED text (`innerText` of a table or
// a selection in the rendered markdown body) while the substrate is markdown
// SOURCE — and a rendered table's text can never be a byte substring of
// `| a | b |\n|---|---|`, because the pipes and the separator row are
// characters the renderer DELETES rather than whitespace it moves. The same
// holds for any selection crossing a link, emphasis or inline code. Projecting
// both sides to letters and digits removes exactly the class of character the
// renderer adds or drops, and keeps CASE, which is content.
//
// Errors READING either side are skipped, never invalidated — one unreadable
// row does not end the sweep, and the first failure is what the caller logs.
func mmVerifyArtifacts(st *store.Store, at int64) error {
	items, err := st.ListMMItems(false) // live only
	if err != nil {
		return err
	}
	var firstErr error
	note := func(e error) {
		if firstErr == nil {
			firstErr = e
		}
	}
	// Three memos, all per PASS, all for the same reason: marks concentrate.
	// carl reads a set, and he marks several elements of one document — so a
	// job row, a live-session snapshot and a document's bytes are each read
	// once and reused, rather than once per mark.
	jobs := map[int64]*Job{}
	subs := map[string]mmSubstrate{}
	var live map[string]bool
	liveSessions := func() map[string]bool {
		// Lazy: LiveSessions shells out to tmux, and a pass with no artifact
		// marks at all must not pay for one.
		if live == nil {
			live = LiveSessions()
		}
		return live
	}
	for _, it := range items {
		if it.Source != "artifact" && it.Source != "artifact-element" {
			continue
		}
		if it.JobID <= 0 {
			continue // no job, no substrate to check it against
		}
		j, ok := jobs[it.JobID]
		if !ok {
			loaded, err := mmJobByID(st, it.JobID)
			if err != nil {
				note(err)
				continue
			}
			jobs[it.JobID] = loaded // nil is a real answer: the row is gone
			j = loaded
		}
		key := fmt.Sprintf("%d\x00%s", it.JobID, it.SourceRef)
		sub, ok := subs[key]
		if !ok {
			sub.data, sub.present, sub.err = mmArtifactSubstrate(st, j, liveSessions(), it)
			subs[key] = sub
		}
		data, present, err := sub.data, sub.present, sub.err
		if err != nil {
			// Any read failure is this ITEM's alone: noted for one line at the
			// end of the pass, never an invalidation, and never a reason to
			// stop examining the items after it. A half that aborted on one
			// unreadable row would leave the rest of carl's marks unchecked
			// with nothing saying which ones.
			note(err)
			continue
		}
		if !present {
			// A JOB THAT NO LONGER EXISTS IS NOT A CONTENT CHANGE. `artifacts`
			// cascades on `jobs` (ON DELETE CASCADE), and hard-deleting from
			// the archive is the sanctioned way to get rid of a job — so every
			// artifact and element mark taken from it would otherwise be
			// invalidated, terminally, with a notification each, minutes after
			// carl tidied up. mm rows outlive jobs BY DESIGN (no FK, no
			// cascade, and the memory pane renders an item whose job is long
			// gone); withdrawing the mark because its provenance was deleted
			// is the one reading of "the row is gone" that contradicts the
			// feature. Left untouched, and not an error either.
			if j == nil {
				continue
			}
			mmInvalidate(st, it, at, "artifact-deleted", fmt.Sprintf("%s of job %s is gone", it.SourceRef, it.Slug))
			continue
		}
		if it.Source == "artifact" {
			changed, err := mmArtifactChanged(st, it, data)
			if err != nil {
				note(err)
				continue
			}
			if changed {
				mmInvalidate(st, it, at, "content-changed", fmt.Sprintf("%s of job %s was rewritten", it.SourceRef, it.Slug))
			}
			continue
		}
		if !mmElementPresent(string(data), it.Content) {
			mmInvalidate(st, it, at, "content-changed", fmt.Sprintf("the marked passage is no longer in %s of job %s", it.SourceRef, it.Slug))
		}
	}
	return firstErr
}

// mmJobByID loads one job, answering (nil, nil) for a row that no longer
// exists. The absence is a fact the caller acts on, not an error: it is how
// the artifact half tells a deleted DOCUMENT from a deleted JOB.
func mmJobByID(st *store.Store, id int64) (*Job, error) {
	sr, err := st.GetJobByID(id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return loadJobFull(st, sr)
}

// mmSubstrate is one document's read, memoised per (job, source_ref) for the
// pass — several element marks routinely share one artifact.
type mmSubstrate struct {
	data    []byte
	present bool
	err     error
}

// mmArtifactSubstrate reads the bytes one artifact mark is verified against,
// and reports whether the substrate is PRESENT at all.
//
// THE GATE IS THE CAPTURE SIDE'S OWN PREDICATE — is this job's tmux SESSION
// live — and not a stat of the cell dir, because agreeing with the capture
// side is the entire point. `currentArtifactHome` serves home `cell` (a
// straight file read) exactly while `LiveSessions()[CellSession(j)]`, and
// anything else store-first; so a job whose session died without a teardown
// still HAS a cell dir while the board is already serving it out of the table.
// Gated on the dir, this pass would read the file where carl read the row,
// and a file zeus wrote after the last sync would read as `content-changed`
// on the first pass — a false death, terminal, that re-marking cannot escape
// because the re-mark freezes the table's bytes again.
//
// Within the live gate, a file that is NOT THERE is absence — the cell dir is
// the truth for a live cell, including about deletions, and falling back to
// the mirror would judge an artifact zeus deleted mid-run against a copy from
// the last working→blocked sync and kill it as `content-changed` ("was
// rewritten") instead of `artifact-deleted` ("is gone"). The verdict would be
// right and its whole explanation wrong, and that explanation is the entire
// content of the notification and of the pane's dead-row line.
//
// Outside the live gate the table row is the substrate, and its absence is
// absence. Either way it is the caller that decides what absence MEANS: a job
// whose row is gone leaves the mark untouched.
//
// A read that FAILS for any other reason returns the error and no verdict.
func mmArtifactSubstrate(st *store.Store, j *Job, live map[string]bool, it store.MMItem) (data []byte, present bool, err error) {
	if j != nil && live[CellSession(j)] {
		// A source_ref this pass may not open (a hostile or malformed stored
		// name) is not absence — it is a question that was never asked, so it
		// falls through to the table rather than deciding anything.
		if p, ok := mmCellArtifactPath(j, it.SourceRef); ok {
			b, rerr := os.ReadFile(p)
			switch {
			case rerr == nil:
				return b, true, nil
			case os.IsNotExist(rerr):
				return nil, false, nil
			default:
				return nil, false, rerr
			}
		}
	}
	a, gerr := st.GetArtifact(it.JobID, it.SourceRef)
	switch {
	case errors.Is(gerr, store.ErrNotFound), gerr == nil && a == nil:
		// GetArtifact reports an absent (job_id, source_ref) row as
		// ErrNotFound rather than as (nil, nil); the second test is belt and
		// braces for an accessor that later chooses the other convention.
		return nil, false, nil
	case gerr != nil:
		return nil, false, gerr
	}
	return a.Bytes, true, nil
}

// mmCellArtifactPath resolves one artifact name inside the job's cell dir, and
// reports false only when the NAME is not one this pass may open.
//
// Whether the cell is the right substrate at all is the CALLER's question, and
// it is answered by the live-session gate rather than here — this returns a
// path, not a claim that the file exists, precisely so an absent file can mean
// absence rather than "no cell". `existingCellDir` picks whichever candidate
// dir is on disk and falls back to the canonical name, which is the correct
// path to look for a file in either way.
//
// The segment rules mirror api's ArtifactPath exactly — no traversal, no empty
// or hidden segments — because a source_ref is a stored string and this is a
// filesystem read: the route that wrote it validated it once, and a check that
// lives only at the write side is one a later writer can skip.
func mmCellArtifactPath(j *Job, name string) (string, bool) {
	name = strings.TrimPrefix(name, "/")
	if name == "" {
		return "", false
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == "" || seg == "." || seg == ".." || strings.HasPrefix(seg, ".") {
			return "", false
		}
	}
	return filepath.Join(cellArtifactsIn(existingCellDir(j)), name), true
}

// mmArtifactChanged answers the whole-artifact question by SHA, without
// loading the frozen snapshot in the common case.
//
// `content_sha` for a source=artifact item is the hash of the snapshot bytes,
// and FindLiveMMItem is the one store verb that compares against it without
// hydrating a blob — so a settled machine now pays one hash of the current
// bytes per item per pass instead of two full BLOB reads (a diagram card in
// this repo runs 30 KB, and there is no watermark on this half the way
// `mm_head:<repo>` bounds the code half).
//
// The probe deliberately answers on ANY live row of this quad rather than on
// this row's id: two live rows for one artifact can only exist through the
// duplicate-mark race, and they are frozen from the same bytes, so a hit is
// evidence the current bytes are the frozen ones either way. Reading it the
// strict way would invalidate the older duplicate — the permanent direction of
// the error, against this file's standing rule.
//
// A miss is not enough on its own: an artifact that resolved to ZERO bytes
// never had SetMMSnapshot called, so its content_sha is the hash of Content
// (the name) and could never match. Confirming the snapshot exists costs one
// blob read, and only on the path where something has actually changed.
func mmArtifactChanged(st *store.Store, it store.MMItem, current []byte) (bool, error) {
	if _, found, err := st.FindLiveMMItem(it.Source, it.JobID, it.SourceRef, store.SHA256Hex(current)); err != nil {
		return false, err
	} else if found {
		return false, nil
	}
	if _, _, err := st.GetMMSnapshot(it.ID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return false, nil // nothing was frozen: nothing to compare against
		}
		return false, err
	}
	return true, nil
}

// mmElementPresent reports whether a marked element is still in the document.
//
// THE TWO SIDES MAY BE DIFFERENT RENDERINGS OF ONE THING, and every rule here
// follows from that: the mark may hold RENDERED text (`innerText` of a table
// or a selection in the rendered markdown body) while the substrate is
// markdown SOURCE. Since the gate-R1 amendment that is the MINORITY case —
// `/api/mm/element` locates the capture in the source and stores the source
// LINES (`core.LocateProjectedSpan`), and such a mark is the substrate's own
// bytes, so it clears the contiguity rung below directly and never reaches the
// fallback. What still arrives as rendered text is a row marked before that
// amendment, or one whose locate was refused. So the comparison happens
// between ALPHANUMERIC PROJECTIONS — every unicode letter and digit, in
// order, case preserved — which removes the whole class of PUNCTUATION the
// renderer adds or drops: a pipe table's
// `|`, its `|---|` separator and `:--:` alignment, `**`, backticks, heading
// `#`. Both sides of `| a | b |\n|---|---|\n| c | d |` and `a\tb\nc\td`
// project to `abcd`.
//
// Case is kept because case is content: two headings differing only in case
// are two different headings.
//
// AND THEN A SUBSEQUENCE FALLBACK, because the projection alone closes only
// half the gap. Three markdown constructs put ALPHANUMERIC characters in the
// source that the renderer deletes outright, so they survive projection and
// break contiguity from the inside:
//
//   - ordered-list markers — `1. alpha\n2. beta` renders as `alpha\nbeta`
//     (the digits never reach the DOM, and `innerText` excludes `::marker`
//     content by spec), so the source projects to `1alpha2beta` against a
//     marked `alphabeta`;
//   - link and image URLs — `[contract](https://x.dev/a)` renders as
//     `contract`, and the url's own letters and digits sit INSIDE the marked
//     span in the source;
//   - a fenced block's language tag — ```` ```go ```` is source-only.
//
// Every one of those is a construct the renderer only DELETES from, so the
// rendered projection is always a SUBSEQUENCE of the source projection. A
// contiguity miss therefore proves nothing on its own, and only a subsequence
// miss is evidence the passage is gone. Measured on the real corpus before
// this was written: 19 of the 109 preserved `.md` artifacts contain an ordered
// list, so this is a class of terminal false deaths rather than a corner.
//
// It weakens the check far past "reorders survive": a subsequence needs only
// want's runes somewhere in order, and an ordinary artifact holds 30x-1000x
// the alphanumerics a marked passage needs, so once the fallback is reached an
// element mark survives most WHOLESALE REWRITES too — whether it bites at all
// turns on whether the passage holds a rare rune the rewrite lacks. Element
// verification past the contiguity rung is therefore close to advisory — for
// the minority of marks that reach it, which since the gate-R1 amendment is
// the pre-amendment rows and the refused locates named above; a LOCATED mark
// is source lines, stops at contiguity, and dies honestly on a real rewrite of
// the passage. What still bites for real on the advisory path is
// artifact-deleted, the whole-artifact sha, and the code ladder.
// Accepted in the direction this file chooses everywhere: a wrong
// invalidation is permanent and silent, a wrong survival costs one more pass.
// The structural alternative (render the source and compare rendered to
// rendered) is a markdown renderer in Go beside the TypeScript one, which is
// two renderers to keep in step.
//
// TWO ACCEPTED LIMITS, recorded because neither is visible from a call site:
//
//   - SHORT PROJECTIONS ARE EFFECTIVELY UNFALSIFIABLE. A two-cell table of
//     `1`/`2` projects to `12`, which is a subsequence of almost any document
//     and a substring of most — so small marks (a one-row table, a two-word
//     caption) are never really re-verified and can "survive" against text
//     they have nothing to do with. Accepted deliberately: the failure is a
//     mark that outlives its subject, which is the safe direction, and the
//     alternative — a minimum projected length falling back to the strict
//     rule — would put those marks back on a rule that cannot match rendered
//     text against source at all, i.e. it would trade silence for false death.
//   - THE PROJECTION ASSUMES BOTH SIDES SHARE A UNICODE NORMAL FORM. Combining
//     marks are Mn/Mc/Me, which are neither letters nor digits, so NFD `e` +
//     U+0301 projects to `e` while NFC `é` projects to `é`. Today both sides
//     derive from the same file bytes with nothing in between that could
//     re-normalize, so they agree; the day something rewrites an artifact in a
//     different normal form, marks on the affected passages die for a change
//     nobody can see. Normalizing properly needs golang.org/x/text, which this
//     repo does not carry, so the assumption is stated rather than enforced.
//
// When the projection of the marked text is EMPTY — a selection of nothing but
// punctuation or symbols, a diagram caption of arrows — there is nothing left
// to match on and both tests would trivially succeed against any document.
// That falls back to the strict `\r\n`-normalized byte-substring rule, which
// is what the two sides do share when there is no alphanumeric content at all.
//
// The projection itself is core.ProjectAlnum, shared with the capture side's
// core.LocateProjectedSpan rather than kept here: the locate decides what a
// mark's content IS and this pass decides whether that content is still there,
// which is one comparison split across two packages. A second copy of the
// projection is the way those two halves drift into disagreeing.
func mmElementPresent(current, marked string) bool {
	want := core.ProjectAlnum(marked)
	if want == "" {
		return strings.Contains(mmNormalize(current), mmNormalize(marked))
	}
	have := core.ProjectAlnum(current)
	if strings.Contains(have, want) {
		return true
	}
	return mmSubsequence(have, want)
}

// mmSubsequence reports whether want's runes appear in have, in order, not
// necessarily adjacent. One pass over have, no allocation beyond want's runes.
func mmSubsequence(have, want string) bool {
	w := []rune(want)
	if len(w) == 0 {
		return true
	}
	i := 0
	for _, r := range have {
		if r != w[i] {
			continue
		}
		if i++; i == len(w) {
			return true
		}
	}
	return false
}

// mmNormalize is the one line-ending policy the strict comparisons share. A
// file that only changed its line endings is not a document carl has to
// re-read.
func mmNormalize(s string) string { return strings.ReplaceAll(s, "\r\n", "\n") }

// mmInvalidate is the one death path: stamp the item and say so once.
//
// THE ROW TRANSITION IS THE DEDUP, and it is enforced here rather than assumed
// of the callers. `InvalidateMMItem` is one-way and answers an already-dead
// row with a silent success — deliberately, so it cannot overwrite the first
// reason — which means a second call is indistinguishable from a first unless
// somebody looks. So this re-reads the row and does NOTHING when it is already
// invalidated: no event, and above all no notification. The
// property used to be held by the two worklists both filtering to live items,
// which made it true by luck of the caller; a third caller (carl's own unmark
// routing through here, say) would have got duplicate "memory stale" pings
// with nothing failing anywhere.
//
// The re-read is also what makes the side effects act on the CURRENT row: the
// caller's copy came out of a list read earlier in the pass.
func mmInvalidate(st *store.Store, it store.MMItem, at int64, reason, detail string) {
	cur, err := st.GetMMItem(it.ID)
	if err != nil {
		dlogErr("mm verify: re-read item %d before invalidating: %v", it.ID, err)
		return
	}
	if cur.InvalidatedAt != 0 {
		return // already dead: this transition already happened, and was said
	}
	if err := st.InvalidateMMItem(cur.ID, at, reason, detail); err != nil {
		dlogErr("mm verify: invalidate item %d: %v", cur.ID, err)
		return
	}
	dlogEvent("info", "mm-invalidated", "mm item %d (%s/%s) invalidated: %s — %s",
		cur.ID, cur.Kind, cur.Source, reason, detail)
	notify("kraken — memory stale", mmDeathHeadline(cur, reason))
}

// mmDeathHeadline is what carl actually reads on the notification: what he knew
// and why kraken stopped believing he still does.
func mmDeathHeadline(it store.MMItem, reason string) string {
	what := it.SourceRef
	if strings.TrimSpace(what) == "" {
		what = core.TruncateOneLine(it.Content, 60)
	}
	return what + " — " + reason
}

// ---------------------------------------------------------------------------
// The code half
// ---------------------------------------------------------------------------

// kickMMCode runs the git half in one single-flighted goroutine: it fetches,
// so it must not sit inside tick().
func kickMMCode(st *store.Store, at int64) {
	if !mmCodeRunning.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer mmCodeRunning.Store(false)
		mmCodePass(st, at)
	}()
}

// mmCodePass verifies every live code anchor, repo by repo.
func mmCodePass(st *store.Store, at int64) {
	anchors, err := st.ListLiveMMCodeAnchors()
	if err != nil {
		dlogErr("mm verify: list anchors: %v", err)
		return
	}
	if len(anchors) == 0 {
		return
	}
	// ListLiveMMCodeAnchors orders by repo, so this grouping is one linear
	// walk — but the group list is built explicitly rather than relying on
	// that order, since "grouped because the accessor happens to sort" is the
	// kind of coupling that breaks silently when the accessor's ORDER BY
	// changes for an unrelated reason.
	byRepo := map[string][]store.MMCodeAnchor{}
	var repos []string
	for _, a := range anchors {
		if _, seen := byRepo[a.Repo]; !seen {
			repos = append(repos, a.Repo)
		}
		byRepo[a.Repo] = append(byRepo[a.Repo], a)
	}
	sort.Strings(repos)
	// ResolveRepo does an os.ReadDir of ~/Developer plus an isGitRepo per
	// entry on every call (the tokensPass lesson), so it is memoized for the
	// pass. Grouping already makes it one call per repo; the memo is what
	// keeps that true if a later change stops grouping.
	resolved := map[string]string{}
	for _, repo := range repos {
		mmVerifyRepo(st, repo, byRepo[repo], resolved, at)
	}
}

// mmVerdict is one anchor's outcome, computed BEFORE anything is written. The
// two-phase shape is what the circuit breaker needs: "all of this repo's
// anchors would die" is only knowable once every anchor has been decided, and
// by then a one-phase pass would have already written the first death.
type mmVerdict struct {
	anchor    store.MMCodeAnchor
	survived  bool
	path      string // head-coordinate path (relocated on a rename)
	start     int
	end       int
	ambiguous bool
	reason    string // invalidation reason, when !survived
	detail    string
	failed    string // a READ failure: neither survival nor death
}

func mmVerifyRepo(st *store.Store, repo string, anchors []store.MMCodeAnchor, resolved map[string]string, at int64) {
	dir, ok := resolved[repo]
	if !ok {
		d, err := ResolveRepo(repo)
		if err != nil {
			// Unresolvable is a read failure, not a death: a checkout carl
			// moved or has not cloned on this machine says nothing at all
			// about whether the text he read is still there.
			mmFailAnchors(st, anchors, at, fmt.Sprintf("resolve repo %s: %v", repo, err))
			resolved[repo] = ""
			return
		}
		resolved[repo] = d
		dir = d
	}
	if dir == "" {
		mmFailAnchors(st, anchors, at, "repo "+repo+" does not resolve")
		return
	}
	if _, err := mmGit(dir, "fetch", "origin", "main"); err != nil {
		mmFailAnchors(st, anchors, at, err.Error())
		return
	}
	head, err := mmGit(dir, "rev-parse", "origin/main")
	if err != nil {
		mmFailAnchors(st, anchors, at, err.Error())
		return
	}
	head = strings.TrimSpace(head)
	if head == "" {
		mmFailAnchors(st, anchors, at, "origin/main resolved to nothing")
		return
	}
	dirty := mmRepoDirty(repo)
	if !dirty {
		if prev, err := st.GetKV(mmHeadKey(repo)); err == nil && prev == head {
			return // head has not moved: nothing in this repo can have changed
		}
	}

	// Phase 1: decide every anchor, write nothing.
	renames := map[string]string{} // memoized name-status probes, keyed by anchor sha
	verdicts := make([]mmVerdict, 0, len(anchors))
	for _, a := range anchors {
		verdicts = append(verdicts, mmVerifyAnchor(dir, head, a, renames))
	}

	// Phase 2: the circuit breaker. A repo where EVERY live anchor would die at
	// once is a repo-level problem — a truncated clone, a fetch that landed on
	// the wrong ref — and recording that as N content changes would burn N
	// items permanently on one machine-level fault.
	deaths := 0
	reads := 0
	for _, v := range verdicts {
		if v.failed != "" {
			reads++
			continue
		}
		if !v.survived {
			deaths++
		}
	}
	// The condition is "every anchor that could be READ would die", not "every
	// anchor would die": the faults this exists for do not present uniformly —
	// a truncated clone or a rewritten history makes some anchors' diffs ERROR
	// while others' blobs come back missing — so requiring `reads == 0` turned
	// the breaker off in exactly the scenario it was built for, and every death
	// in that pass was applied. The floor still binds on the deaths alone: two
	// dead anchors beside one read failure is not evidence of anything.
	if deaths == len(verdicts)-reads && deaths >= mmBreakerMin {
		// The count is DEATHS, not len(verdicts). Now that a read failure no
		// longer disarms the breaker, those two differ exactly when the
		// breaker is most likely to fire, and this string is both the event
		// line and the check_error every anchor carries — saying "all 9" over
		// a repo where 3 were unreadable and 6 would die describes a pass that
		// did not happen.
		msg := fmt.Sprintf("all %d of %s's live anchors that this pass could read would invalidate at once — treating it as a repo-level fault, not %d content changes", deaths, repo, deaths)
		dlogEvent("error", "mm-breaker", "%s", msg)
		mmFailAnchors(st, anchors, at, msg)
		// Head is deliberately NOT stamped: the next pass must look again,
		// since the fault is expected to clear and the watermark would hide
		// the repo until head happened to move.
		return
	}

	// Phase 3: apply.
	failed := false
	for _, v := range verdicts {
		switch {
		case v.failed != "":
			failed = true
			if err := st.SetMMCodeAnchorError(v.anchor.ItemID, at, v.failed); err != nil {
				dlogErr("mm verify: record check error for item %d: %v", v.anchor.ItemID, err)
			}
		case v.survived:
			if err := st.ReanchorMMCodeAnchor(v.anchor.ItemID, v.path, v.start, v.end, head, at, v.ambiguous); err != nil {
				dlogErr("mm verify: re-anchor item %d: %v", v.anchor.ItemID, err)
			}
		default:
			it, err := st.GetMMItem(v.anchor.ItemID)
			if err != nil {
				dlogErr("mm verify: load item %d: %v", v.anchor.ItemID, err)
				continue
			}
			if err := st.SetMMCodeAnchorInvalidMeta(v.anchor.ItemID, head, mmBestEffortPR(dir, head)); err != nil {
				dlogErr("mm verify: record invalid meta for item %d: %v", v.anchor.ItemID, err)
			}
			mmInvalidate(st, it, at, v.reason, v.detail)
		}
	}
	// The head watermark is what lets the NEXT pass skip this repo entirely, so
	// it may only be stamped by a pass that actually reached a verdict on every
	// anchor. Stamping over a pass where one anchor's git call failed would
	// park that anchor unchecked until head happened to move again — a failed
	// read quietly becoming a permanent one, which is the shape this whole file
	// is written against. The cost of not stamping is one more fetch in ten
	// minutes; re-verifying an anchor that already survived is idempotent.
	if !failed {
		if err := st.SetKV(mmHeadKey(repo), head); err != nil {
			dlogErr("mm verify: stamp head for %s: %v", repo, err)
		}
		mmClearRepoDirty(repo)
	}
}

// mmFailAnchors records one read failure across a set of anchors. It never
// invalidates and never stamps a head watermark — the whole point is that the
// pass learned nothing.
func mmFailAnchors(st *store.Store, anchors []store.MMCodeAnchor, at int64, msg string) {
	for _, a := range anchors {
		if err := st.SetMMCodeAnchorError(a.ItemID, at, msg); err != nil {
			dlogErr("mm verify: record check error for item %d: %v", a.ItemID, err)
		}
	}
}

// mmVerifyAnchor decides ONE anchor against head. It writes nothing.
//
// Stage (a), the fast path: when the anchor's own commit is still known to the
// repo, a `--unified=0` diff between it and head says which line ranges moved.
// A range no hunk touches is PROVABLY unchanged, and its new coordinates are
// the accumulated shift of the hunks strictly above it.
//
// Stage (b), the only thing allowed to say "changed": locate the exact text at
// head. Hunk overlap alone NEVER invalidates — a pure block move produces hunks
// that can miss the moved range entirely (verified against git directly), so a
// touch is a reason to look, never a verdict.
//
// The two stages share ONE whitespace policy — `--ignore-all-space` in (a),
// core.StripWS in (b) — because a change one stage forgives and the other does
// not makes invalidation depend on which stage happened to run.
func mmVerifyAnchor(dir, head string, a store.MMCodeAnchor, renames map[string]string) mmVerdict {
	v := mmVerdict{anchor: a, path: a.Path, start: a.StartLine, end: a.EndLine}
	// expect is where the anchor's text SHOULD be at head. Stage (a) refines
	// it from the hunk arithmetic; without a usable anchor sha the old
	// coordinates are the best guess there is. It never decides survival —
	// only which of several occurrences is the one carl read.
	expect := a.StartLine
	if a.AnchorSHA != "" && mmCommitKnown(dir, a.AnchorSHA) {
		paths := []string{a.Path}
		if a.OrigPath != "" && a.OrigPath != a.Path {
			paths = append(paths, a.OrigPath)
		}
		args := append([]string{"diff", "--unified=0", "--ignore-all-space", "-M50%", a.AnchorSHA + ".." + head, "--"}, paths...)
		diff, err := mmGit(dir, args...)
		if err != nil {
			v.failed = err.Error()
			return v
		}
		hunks := core.ParseHunks(diff)
		newStart, newEnd, touched := core.MapRange(hunks, a.StartLine, a.EndLine)
		expect = newStart
		if !touched {
			v.survived, v.start, v.end = true, newStart, newEnd
			return v
		}
	}

	// The text ladder. Rung 1: the exact text at the anchor's own path.
	blob, blobErr := mmShowBlob(dir, head, a.Path)
	if blobErr == nil {
		if hit, amb, ok := mmLocate(blob, a.Text, expect); ok {
			v.survived, v.path, v.ambiguous = true, a.Path, amb
			v.start, v.end = hit, hit+mmLineSpan(a.Text)-1
			return v
		}
	}

	// Rung 2: the same text at a path git says was renamed out from under it.
	// One name-status probe per (repo, anchor sha, head), memoized — a repo
	// with fifty anchors at one sha must not run fifty identical diffs.
	newPath := ""
	if a.AnchorSHA != "" {
		key := a.AnchorSHA
		table, done := renames[key]
		if !done {
			table = mmRenameTable(dir, a.AnchorSHA, head)
			renames[key] = table
		}
		newPath = mmRenamedTo(table, a.Path, a.OrigPath)
	}
	var renamedBlob string
	if newPath != "" && newPath != a.Path {
		if b, err := mmShowBlob(dir, head, newPath); err == nil {
			renamedBlob = b
			if hit, amb, ok := mmLocate(b, a.Text, expect); ok {
				v.survived, v.path, v.ambiguous = true, newPath, amb
				v.start, v.end = hit, hit+mmLineSpan(a.Text)-1
				return v
			}
		}
	}

	// Rung 3: whitespace-normalized equality, on whichever blobs rungs 1 and 2
	// found. This is stage (a)'s --ignore-all-space read one level up: a
	// re-indented block is not a block carl has to read again.
	for _, cand := range []struct {
		blob string
		path string
	}{{blob, a.Path}, {renamedBlob, newPath}} {
		if cand.blob == "" || cand.path == "" {
			continue
		}
		if hit, amb, ok := mmLocateWS(cand.blob, a.Text, expect); ok {
			v.survived, v.path, v.ambiguous = true, cand.path, amb
			v.start, v.end = hit, hit+mmLineSpan(a.Text)-1
			return v
		}
	}

	// The ladder found nothing. What that MEANS depends on whether this pass
	// ever managed to read the file, and getting that distinction wrong is how
	// a transient git failure became a permanent invalidation.
	//
	// Read it (at its own path or its renamed one) and the text is not in it:
	// positive evidence of a content change, decided from bytes this function
	// held. That is the only cheap verdict here.
	if blobErr == nil || renamedBlob != "" {
		v.survived = false
		v.reason = "content-changed"
		v.detail = fmt.Sprintf("the marked lines of %s/%s are no longer there at %s", a.Repo, a.Path, mmShortSHA(head))
		return v
	}
	// Could NOT read it. `git show` failing is not evidence the path is gone —
	// it is also what an `index.lock`, a corrupt object, a full disk, a git
	// that is not on PATH, and an expired deadline all look like. So the
	// absence has to be PROVEN by a listing that itself succeeded; anything
	// else is a read failure and the anchor lives.
	absent, ok := mmPathAbsentAtHead(dir, head, a.Path)
	switch {
	case ok && absent:
		v.survived = false
		v.reason = "file-deleted"
		v.detail = fmt.Sprintf("%s/%s is gone at %s", a.Repo, a.Path, mmShortSHA(head))
	default:
		// Either the listing failed too, or it says the path IS there and only
		// the read of it failed. Neither is a death.
		v.failed = fmt.Sprintf("could not read %s at %s and its absence is unproven: %v", a.Path, mmShortSHA(head), blobErr)
	}
	return v
}

// mmPathAbsentAtHead asks git whether one path exists at a commit, and reports
// separately whether the QUESTION could be answered.
//
// `ls-tree` is the probe rather than `show` because its exit status carries no
// verdict: it succeeds with EMPTY output for a path that is not in the tree and
// prints the path for one that is, so a zero exit is a real answer either way,
// and a non-zero exit is unambiguously a failure to look rather than a possible
// absence. `show` conflates the two, which is the whole reason this exists.
func mmPathAbsentAtHead(dir, head, path string) (absent, ok bool) {
	out, err := mmGit(dir, "ls-tree", "--name-only", head, "--", path)
	if err != nil {
		return false, false
	}
	return strings.TrimSpace(out) == "", true
}

// mmCommitKnown reports whether this checkout can still reach a commit. An
// anchor SHA from a branch that was force-pushed or garbage-collected is not an
// error — it just means stage (a) has nothing to diff and the text ladder is
// the whole check.
func mmCommitKnown(dir, sha string) bool {
	_, err := mmGit(dir, "cat-file", "-e", sha+"^{commit}")
	return err == nil
}

// mmShowBlob reads one path's bytes at a commit.
//
// AN ERROR HERE MEANS NOTHING ON ITS OWN. It is what an absent path looks like,
// and equally what an `index.lock`, a corrupt object, a full disk, a missing
// git and an expired deadline look like — git's exit status does not separate
// them, and this function does not check. The caller treats a failure as "I
// have not read this file", which sends it to the rename rung and, if that
// finds nothing either, to mmPathAbsentAtHead for the only evidence of absence
// this file accepts. An earlier comment here asserted the opposite ("an error
// means the path does not exist at head"); it was the single place where the
// header's rule — A FAILED READ IS NEVER AN INVALIDATION — was broken, and a
// slow fetch was enough to kill the tail of a repo's anchor list.
func mmShowBlob(dir, rev, path string) (string, error) {
	return mmGit(dir, "show", rev+":"+path)
}

// mmRenameTable runs one `--name-status -M50%` probe and returns its raw
// output, memoized by the caller. Returns "" on any failure: a probe that could
// not run leaves the ladder to its remaining rungs rather than failing it.
func mmRenameTable(dir, from, to string) string {
	out, err := mmGit(dir, "diff", "--name-status", "-M50%", from+".."+to)
	if err != nil {
		return ""
	}
	return out
}

// mmRenamedTo finds where git says one of the anchor's known paths ended up.
// Rename lines are `R<score>\t<old>\t<new>`; anything else is skipped.
func mmRenamedTo(table, path, origPath string) string {
	if table == "" {
		return ""
	}
	for _, line := range strings.Split(table, "\n") {
		fields := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(fields) != 3 || !strings.HasPrefix(fields[0], "R") {
			continue
		}
		if fields[1] == path || (origPath != "" && fields[1] == origPath) {
			return fields[2]
		}
	}
	return ""
}

// mmLocate finds the anchor's exact text in a blob and picks the occurrence
// NEAREST the position the hunk arithmetic expected.
//
// Multiple hits set `ambiguous` and NEVER invalidate: a helper extracted into
// two call sites, a test table with a repeated row — the text is genuinely
// still there, and the only honest thing to say is which copy is nearest and
// that there was more than one.
//
// The pick is core.NearestLine, not a local copy: the CAPTURE side (api) runs
// the same ladder against the same blobs, and two spellings of "nearest" that
// tie-break differently would relocate an anchor to a different copy than the
// one the mark was taken at, silently.
func mmLocate(blob, text string, expect int) (line int, ambiguous bool, ok bool) {
	hits := core.LocateText(blob, text)
	if len(hits) == 0 {
		return 0, false, false
	}
	return core.NearestLine(hits, expect), len(hits) > 1, true
}

// mmLocateWS is rung 3: the same search with every whitespace rune removed from
// both sides, which is what makes it agree with stage (a)'s --ignore-all-space.
// It compares a window of the SAME line count as the anchor text, so a
// re-indented block matches where a reflowed one does not.
func mmLocateWS(blob, text string, expect int) (line int, ambiguous bool, ok bool) {
	want := core.StripWS(text)
	if want == "" {
		return 0, false, false
	}
	span := mmLineSpan(text)
	lines := strings.Split(mmNormalize(blob), "\n")
	if span > len(lines) {
		return 0, false, false
	}
	// Each line is stripped ONCE, not once per window it appears in. The
	// straightforward spelling — join the window and StripWS the join — walks
	// every line `span` times: a 100k-line generated file against a 50-line
	// anchor is ~5M lines of allocating string work for ONE anchor, inside a
	// pass that has to get through every anchor in the repo. StripWS removes
	// all whitespace including the newlines the join introduced, so the
	// stripped window is exactly the concatenation of the stripped lines, and
	// the cumulative lengths let a window be REJECTED on arithmetic before any
	// of it is built — which is what makes the common case linear.
	stripped := make([]string, len(lines))
	cum := make([]int, len(lines)+1)
	for i, ln := range lines {
		stripped[i] = core.StripWS(ln)
		cum[i+1] = cum[i] + len(stripped[i])
	}
	var hits []int
	var win strings.Builder
	for i := 0; i+span <= len(lines); i++ {
		if cum[i+span]-cum[i] != len(want) {
			continue
		}
		win.Reset()
		win.Grow(len(want))
		for _, s := range stripped[i : i+span] {
			win.WriteString(s)
		}
		if win.String() == want {
			hits = append(hits, i+1)
		}
	}
	if len(hits) == 0 {
		return 0, false, false
	}
	return core.NearestLine(hits, expect), len(hits) > 1, true
}

// mmLineSpan is how many lines a piece of anchor text occupies, which is what
// turns a located START line back into the inclusive range the anchor stores.
// A trailing newline does not open a line (core.EndRowFromSelection draws the
// same boundary for the capture side).
func mmLineSpan(text string) int {
	t := mmNormalize(text)
	if t == "" {
		return 1
	}
	t = strings.TrimSuffix(t, "\n")
	return strings.Count(t, "\n") + 1
}

func mmShortSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// mmBestEffortPR names the PR that most likely carries the change, for the
// board's invalidated-detail view. BEST EFFORT is the whole contract: it runs
// one bounded git call, and every failure — no merge commit, a head that was
// pushed directly, a message in a shape nothing here recognises — returns "",
// because "which PR did this" is decoration on top of a death that is already
// recorded.
func mmBestEffortPR(dir, head string) string {
	out, err := mmGit(dir, "log", "-1", "--format=%s", head)
	if err != nil {
		return ""
	}
	// `Merge pull request #231 from carl/...` — github's own merge subject.
	fields := strings.Fields(out)
	for i, f := range fields {
		if !strings.HasPrefix(f, "#") || i == 0 {
			continue
		}
		if n := strings.TrimLeft(f[1:], "0123456789"); n == "" && len(f) > 1 {
			return f
		}
	}
	return ""
}
