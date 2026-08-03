package jobs

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"kraken/internal/core"
	"kraken/internal/store"
	"kraken/internal/util"
)

// graph.go — the knowledge graph's daemon-side halves: graphPass (the
// throttled ingest/embed/layout sweep) and CreateGraphJob (the programmatic
// creator for the two graph job shapes).
//
// graphPass does NOT extract. Extraction costs a model and happens inside a
// `/graph` cell; everything here is mechanical — advance cursors, enqueue
// pending episodes, fill in vectors, refresh the cached layout. That split is
// the whole reason the pass can run on a 15-second daemon tick at all.
//
// It is deliberately shaped after tokensPass rather than after the dream pass:
// same self-pacing, same "stamp the throttle before any early bail", same
// "read every watermark in one query". The one structural difference is that
// graphPass is a FREE FUNCTION, not a daemonState method, so its throttle and
// round-robin state live in the package-level atomics below rather than in
// daemonState. That keeps the daemon.go edit to a single line — the pass owns
// its own pacing, which is also what lets a test drive it without building a
// daemon.

const (
	// graphPassInterval paces the whole pass. The daemon ticks every 15s
	// (DaemonInterval); a minute is slow enough that the directory walk over
	// ~/.claude/projects is not a per-tick cost and fast enough that a live
	// cell's transcript is ingested while the work it describes is still
	// recent.
	graphPassInterval = time.Minute

	// graphRowBatch caps how many NEW rows one source contributes per pass.
	// The row tables are small (hundreds of rows), so this bounds the burst a
	// backfill or a restart produces rather than the steady state.
	graphRowBatch = 16

	// graphFilesPerTick caps how many transcript files one pass reads new
	// bytes from. With graphEpisodeBytes below this is ~128 KB of transcript
	// per pass, ~7.5 MB/hour — a deliberate slow drain of the 1.7 GB corpus
	// Q7 asked for, chosen so backfill never competes with the tick's real
	// work. Files are drained in sorted order, so the drain is monotonic:
	// a file with nothing new costs a map lookup and no I/O at all.
	graphFilesPerTick = 8

	// graphEpisodeBytes is one episode's span: how many bytes of a transcript
	// a single episode carries, and (except in the over-long-line case below)
	// exactly how far its cursor advances. Content and span are the same
	// bytes on purpose — a content cap SMALLER than the span would advance
	// the cursor past evidence nothing ever stored, and the cursor never
	// moves backwards.
	graphEpisodeBytes = 16 << 10

	// graphEpisodeMax bounds the search for a line terminator when a single
	// JSONL record is longer than one episode span. The bound has to sit ABOVE
	// the real corpus maximum, because a terminated record over it is SKIPPED
	// AS A FILE, not stepped over: review measured 32 real lines past the old
	// 1 MB bound (p100 8,304,321 bytes), and with 1 MB the sweep stalled at
	// sorted index 111 of 3,057 — 96% of the corpus permanently unreachable
	// behind one long record. 32 MB clears the observed maximum ~4x; a line
	// past it is genuinely corrupt (or a format change worth hearing about),
	// and erroring THAT file while the sweep continues past it is the shape
	// the caller now implements.
	graphEpisodeMax = 32 << 20

	// graphPendingHighWater gates the transcript sweep: while this many
	// episodes sit unextracted, enqueueing more is pure database growth —
	// the drain side is one nightly cell, and an unbounded queue of spans it
	// will not reach for weeks is storage with no reader (review finding).
	// Row sources stay ungated: they are small, curated, and the evidence
	// carl actually acts on. The high-water mark is generous — a nightly
	// ingest job draining a few hundred episodes keeps the queue well under
	// it in the steady state.
	graphPendingHighWater = 2000

	// graphEmbedBatch is how many missing vectors one embedding sweep asks
	// for. core.Embedder batches internally at 128; 64 keeps one pass's API
	// spend and failure blast radius small.
	graphEmbedBatch = 64

	// graphJobVecBatch caps how many job spec vectors one pass refreshes.
	// Each is one embedding call's worth of text, and the cache only has to
	// be warm by the time a job STARTS, not the moment it is written.
	graphJobVecBatch = 8

	// graphSpecEmbedCap truncates a job spec before embedding it. The
	// embedding model's own input limit is far larger; this bounds what a
	// pathological spec costs, and the head of a spec is what states its
	// subject.
	graphSpecEmbedCap = 8000

	// graphArtifactJobsPerTick caps the artifact sweep. Artifacts are the one
	// row source with no unscoped, id-ordered accessor (see sweepArtifacts),
	// so they are swept per job round-robin rather than in one query.
	graphArtifactJobsPerTick = 4

	// graphArtifactContentCap caps one artifact episode's text. Artifacts are
	// whole documents; the extraction agent has the path and can read the
	// rest.
	graphArtifactContentCap = 32 << 10

	// graphLayoutIterationsCold / graphLayoutIterationsWarm are the force
	// simulation's budgets. A layout seeded from existing positions only has
	// to settle the nodes that moved, which is what makes a refresh cheap
	// enough to run on a pass at all.
	graphLayoutIterationsCold = 300
	graphLayoutIterationsWarm = 60

	// graphLayoutInterval floors how often the layout recomputes. The rev
	// check alone re-ran the whole simulation every minute during any active
	// ingest (MaxEdgeID moves on every edge), which at the spec's projected
	// 50k nodes is ~4s of CPU per minute forever — and it holds
	// graphSlowRunning, starving the embedding sweep behind it (review
	// finding). Ten minutes keeps the picture fresh against a graph that
	// changes nightly.
	graphLayoutInterval = 10 * time.Minute
)

// graphLastLayout is graphLayoutInterval's watermark, in-memory like
// graphLastPass and for the same reason: losing it on restart costs one
// extra layout, not correctness.
var graphLastLayout atomic.Int64

// Ingest cursor source keys. A cursor's MEANING is per-source and stated here
// once: for the row tables it is the highest watermark column already
// ingested (id, or last_seen/updated_at where the row can change in place),
// for a transcript it is a byte offset that only ever lands just past a
// newline.
const (
	graphSrcFeedback = "rows:feedback"
	graphSrcJobs     = "rows:jobs"
)

func graphSrcArtifacts(jobID int64) string  { return fmt.Sprintf("artifacts:%d", jobID) }
func graphSrcTranscript(path string) string { return "transcript:" + path }

// graphJobVecKey / graphJobVecHashKey name the two kv rows that make the
// SPAWN path offline: the cached embedding of a job's spec, and the hash of
// the text that vector embedded. graphContext reads the first and never
// calls a network; this pass writes both. Two rows rather than one JSON
// object because the vector is the hot read and the hash is only ever
// compared by the writer.
func graphJobVecKey(jobID int64) string     { return fmt.Sprintf("kg.jobvec.%d", jobID) }
func graphJobVecHashKey(jobID int64) string { return fmt.Sprintf("kg.jobvechash.%d", jobID) }

// graphLastPass is the pass throttle (unix millis of the last run). In-memory
// like d.TokenScan and lastDreamDate: losing it on a restart costs one extra
// sweep, which is one directory walk and no writes when nothing moved.
var graphLastPass atomic.Int64

// graphArtifactRR is the artifact sweep's round-robin position: an index into
// the sorted job-id list, so consecutive passes cover different jobs instead
// of re-examining the first four forever.
var graphArtifactRR atomic.Int64

// graphSlowRunning single-flights the background half of the pass — the same
// shape usagePassRunning and dreamRunning use, and for the sharper version of
// their reason: the embedding sweep makes a NETWORK call (up to a 60s timeout
// in core.Embedder) and the layout refresh is a force simulation over the
// whole graph. Neither may sit inside tick(), which drives every cell's state
// derivation; and two of them overlapping would double-embed the same rows.
var graphSlowRunning atomic.Bool

// graphPass is the daemon's per-tick knowledge-graph sweep. Skipped under
// --mirror by its caller, on the same rule as every other pass in the tick:
// mirror runs state derivation and the block-event artifact sync and nothing
// else — and this one both writes the database and spends money.
//
// Foreground work is local I/O only and bounded by the constants above: row
// sources past their cursors, then a bounded slice of transcript bytes.
// Everything that can block (embeddings, layout) is handed to a
// single-flighted goroutine — see graphSlowRunning.
func graphPass(st *store.Store, now time.Time) {
	if st == nil {
		return // file-mode entrypoints have no graph to sweep
	}
	if last := graphLastPass.Load(); last != 0 && now.UnixMilli()-last < graphPassInterval.Milliseconds() {
		return
	}
	// Stamped HERE, before any of the work below can bail, for tokensPass'
	// reason: a sweep that fails on its first query has still had its turn,
	// and a throttle stamped only on success turns a persistent failure into
	// a per-tick retry that logs the same line four times a minute.
	graphLastPass.Store(now.UnixMilli())

	cursors, err := st.AllIngestCursors()
	if err != nil {
		dlogErr("graph pass: read cursors: %v", err)
		return
	}

	// One error line per pass rather than one per source: the pass runs at
	// most once a minute and a store that cannot answer one query will not
	// answer the next either. The FIRST failure is the one reported, since
	// later ones are usually its consequence.
	// Both sweeps are ATTEMPTED every pass — never `else if`: the sources are
	// independent, and chaining them let one persistently failing artifact
	// blob stop transcript ingest entirely (review finding). The one-line-per-
	// failure logging policy still holds per sweep.
	if err := sweepRows(st, cursors); err != nil {
		dlogErr("graph pass: row sweep: %v", err)
	}
	if err := sweepTranscripts(st, cursors); err != nil {
		dlogErr("graph pass: transcript sweep: %v", err)
	}

	kickGraphSlow(st)
}

// sweepRows ingests the row-backed evidence sources past their cursors.
//
// Every source is read through an EXISTING store accessor and filtered in Go
// rather than in SQL, which is the deliberate trade: these tables hold
// hundreds of rows (149 jobs, 8 feedback on this machine at the time of
// writing), so a full read costs less than the accessor this package
// would otherwise have to add to internal/store — and internal/store is not
// this slice's to grow.
//
// Two of the three sources track a MUTABLE watermark rather than an id (a job
// spec is edited; an artifact is rewritten), so a row can come back around.
// That is safe and intended: store.AddEpisode dedupes on the content hash, so
// an unchanged row that crosses the cursor again writes nothing, while a
// genuinely changed one enqueues a new episode carrying the new text.
//
// The comparison is `< cursor`, NOT `<= cursor`, and the difference is a real
// bug rather than a stylistic one. Two of the three watermarks are
// millisecond timestamps, and a row written and then edited inside the same
// millisecond (which a test reproduces trivially, and a scripted edit does
// too) would sit at exactly the cursor forever and never be re-read. `<`
// re-examines the boundary row on every pass, which the hash dedupe makes
// free — a no-op INSERT for the newest row of each source, once a minute —
// and closes the gap outright.
func sweepRows(st *store.Store, cursors map[string]int64) error {
	if err := sweepFeedback(st, cursors[graphSrcFeedback]); err != nil {
		return err
	}
	if err := sweepJobSpecs(st, cursors[graphSrcJobs]); err != nil {
		return err
	}
	return sweepArtifacts(st, cursors)
}

func sweepFeedback(st *store.Store, cursor int64) error {
	// jobID <= 0 is ListFeedback's documented unscoped sweep — the same one
	// `kraken feedback sweep` uses. Feedback rows are append-mostly, so the
	// id is a sound watermark; an edited note (UpdateFeedbackNote) is not
	// re-ingested, which is a known and accepted v1 gap: the note's original
	// text is already an episode and the graph is bi-temporal by design.
	rows, err := st.ListFeedback(0)
	if err != nil {
		return err
	}
	sort.Slice(rows, func(a, b int) bool { return rows[a].ID < rows[b].ID })
	high := cursor
	n := 0
	for _, f := range rows {
		if f.ID < cursor || n >= graphRowBatch {
			continue
		}
		content := "carl's feedback on artifact " + f.Artifact + " of job " + f.JobSlug + ":\n" + f.Note
		if f.Addressed != "" {
			content += "\nzeus addressed it: " + f.Addressed
		}
		if err := addEpisode(st, "feedback", fmt.Sprintf("feedback:%d", f.ID), content, f.TS); err != nil {
			return err
		}
		n++
		if f.ID > high {
			high = f.ID
		}
	}
	return advanceCursor(st, graphSrcFeedback, cursor, high)
}

func sweepJobSpecs(st *store.Store, cursor int64) error {
	// updated_at rather than id, so an EDITED spec is re-ingested. A job's
	// spec is the single densest statement of what a piece of work was for,
	// and the first version of one is routinely the wrong version.
	rows, err := st.ListJobs()
	if err != nil {
		return err
	}
	sort.Slice(rows, func(a, b int) bool {
		if rows[a].UpdatedAt != rows[b].UpdatedAt {
			return rows[a].UpdatedAt < rows[b].UpdatedAt
		}
		return rows[a].ID < rows[b].ID
	})
	high := cursor
	n := 0
	for _, j := range rows {
		if j.UpdatedAt < cursor || strings.TrimSpace(j.Body) == "" {
			continue
		}
		// The batch cap must never SPLIT a group of rows sharing one
		// watermark: with 17+ rows at one value, capping mid-group leaves
		// `high == cursor`, the cursor never advances, and every row after
		// the group is permanently unreachable (review finding). Rows AT the
		// boundary watermark process past the cap; the first row beyond it
		// stops the pass cleanly.
		if n >= graphRowBatch && j.UpdatedAt > high {
			break
		}
		content := "job " + j.Slug + " (repo " + j.Repo + ", state " + j.State + "):\n" + j.Body
		if err := addEpisode(st, "job", fmt.Sprintf("job:%d", j.ID), content, j.UpdatedAt); err != nil {
			return err
		}
		n++
		if j.UpdatedAt > high {
			high = j.UpdatedAt
		}
	}
	return advanceCursor(st, graphSrcJobs, cursor, high)
}

// sweepArtifacts ingests artifact documents, per job, round-robin.
//
// Artifacts are the one source with no unscoped accessor: ListArtifactMeta
// and ListArtifacts are both job-scoped, and the meta form (the cheap one,
// which deliberately omits the blob column) carries no row id. So the
// watermark here is the artifact's MTIME within one job, the sweep visits
// at most graphArtifactJobsPerTick jobs per pass, and the position advances
// so every job comes around. Blobs are read only for the paths that actually
// moved past the cursor — which is what keeps the hot blob column out of the
// steady state entirely.
func sweepArtifacts(st *store.Store, cursors map[string]int64) error {
	jobs, err := st.ListJobs()
	if err != nil {
		return err
	}
	ids := make([]int64, 0, len(jobs))
	for _, j := range jobs {
		ids = append(ids, j.ID)
	}
	if len(ids) == 0 {
		return nil
	}
	sort.Slice(ids, func(a, b int) bool { return ids[a] < ids[b] })
	start := int(graphArtifactRR.Load()) % len(ids)
	if start < 0 {
		start = 0
	}
	visited := 0
	for i := 0; i < len(ids) && visited < graphArtifactJobsPerTick; i++ {
		jobID := ids[(start+i)%len(ids)]
		visited++
		src := graphSrcArtifacts(jobID)
		cursor := cursors[src]
		metas, err := st.ListArtifactMeta(jobID)
		if err != nil {
			return err
		}
		// ListArtifactMeta is ORDER BY path, but the watermark is MTIME — so
		// the batch must iterate in WATERMARK order or the cap takes the
		// first 16 by path, stamps the max mtime among those, and every
		// artifact sorted after them by path but below that mtime is
		// permanently skipped. Review finding, proven on this very job: 7 of
		// its 23 artifacts (the research/ tail) sat unreachable behind the
		// cursor. Path is the tiebreak so equal mtimes iterate deterministically.
		sort.Slice(metas, func(a, b int) bool {
			if metas[a].Mtime != metas[b].Mtime {
				return metas[a].Mtime < metas[b].Mtime
			}
			return metas[a].Path < metas[b].Path
		})
		high := cursor
		n := 0
		for _, m := range metas {
			if m.Mtime < cursor {
				continue
			}
			// Never split a shared-watermark group across the cap — see
			// sweepJobSpecs for why (a split group parks the cursor forever).
			// Artifact sets are written in bursts, so equal mtimes are the
			// common case here, not the edge.
			if n >= graphRowBatch && m.Mtime > high {
				break
			}
			a, err := st.GetArtifact(jobID, m.Path)
			if err != nil {
				return err
			}
			if a == nil {
				continue // deleted between the meta read and this one
			}
			content := "artifact " + m.Path + " of job id " + fmt.Sprint(jobID) + ":\n" + capString(string(a.Bytes), graphArtifactContentCap)
			if err := addEpisode(st, "artifact", fmt.Sprintf("artifact:%d:%s", jobID, m.Path), content, m.Mtime); err != nil {
				return err
			}
			n++
			if m.Mtime > high {
				high = m.Mtime
			}
		}
		if err := advanceCursor(st, src, cursor, high); err != nil {
			return err
		}
	}
	graphArtifactRR.Store(int64((start + visited) % len(ids)))
	return nil
}

// advanceCursor writes a source's watermark only when it actually moved. A
// no-op pass over a settled source therefore costs zero writes, which is what
// makes a once-a-minute sweep over every row table free in the steady state.
func advanceCursor(st *store.Store, src string, from, to int64) error {
	if to <= from {
		return nil
	}
	return st.SetIngestCursor(src, to)
}

// addEpisode hashes and enqueues one episode. The hash covers kind, ref AND
// content, so re-crossing a cursor with unchanged text writes nothing while
// changed text enqueues a fresh episode against the same ref — which is
// exactly the behaviour the mutable-watermark sources above rely on.
func addEpisode(st *store.Store, kind, ref, content string, ts int64) error {
	sum := sha256.Sum256([]byte(kind + "\x00" + ref + "\x00" + content))
	_, _, err := st.AddEpisode(store.Episode{
		Kind:    kind,
		Ref:     ref,
		Content: content,
		Hash:    hex.EncodeToString(sum[:]),
		TS:      ts,
	})
	return err
}

// capString truncates to at most n bytes on a rune boundary, marking the cut.
// Silent truncation is what this codebase keeps paying for; the marker is
// what tells the extraction agent to go back to the ref.
func capString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	// Back up to a rune start so the cut never leaves half a multi-byte
	// character: the content column is TEXT, and a torn rune is a corrupt row
	// rather than a shorter one.
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "\n…[truncated]"
}

// ---------------------------------------------------------------------------
// Transcript sweep
// ---------------------------------------------------------------------------

// transcriptFile is one candidate: its path, its current size, and its mtime.
// Size comes from the directory walk's own DirEntry, so deciding a file has
// nothing new costs no extra syscall.
type transcriptFile struct {
	path  string
	size  int64
	mtime int64
}

// sweepTranscripts enqueues new transcript bytes as episodes, bounded to
// graphFilesPerTick files per pass.
//
// Four of core.SumTokensDelta's five rules apply here and are implemented in
// readSpan below: a file SHORTER than its cursor can only be a replacement,
// so its cursor rewinds to zero and it is re-read whole; a cursor only ever
// lands just past a newline, so a transcript caught mid-write is resumed
// rather than skipped past; an incomplete read is an ERROR rather than a
// short answer; and a missing path means "not seen", never "deleted".
//
// That last one is materially cheaper here than it is for tokens, and the
// reason is worth stating: SetIngestCursor is a per-source upsert, where
// SetJobTokenOffsets is replace-all. A short walk therefore cannot DELETE a
// watermark — the worst it can do is leave a file unread for a minute — so
// none of refreshJobTokens' carry-forward machinery is needed. The fifth rule
// (a message split across a resume boundary) has no analogue at all: an
// episode's span always ends on a newline, so no JSONL record is ever split.
func sweepTranscripts(st *store.Store, cursors map[string]int64) error {
	files, walkErr := transcriptFiles()
	// A short walk is reported but not fatal: the files it DID find are real
	// and ingesting them is strictly better than ingesting nothing.
	//
	// Per-FILE errors likewise skip that file and keep draining — `return`
	// here once meant the first unreadable or corrupt file blocked every file
	// sorted after it, forever (review finding). The first such error is
	// still returned at the end so the pass logs it; a store write failure is
	// different (the next write will fail too) and does abort.
	// Backpressure: while the pending queue is deep, stop ENQUEUEING — the
	// cursors hold position, so nothing is lost, and the queue's reader (the
	// nightly ingest cell) sets the real pace. Checked once per pass, not per
	// file; the stats read is one indexed COUNT.
	if stats, err := st.GetGraphStats(); err == nil && stats.PendingEpisodes >= graphPendingHighWater {
		return walkErr
	}
	var firstErr error
	n := 0
	for _, f := range files {
		if n >= graphFilesPerTick {
			break
		}
		src := graphSrcTranscript(f.path)
		cursor := cursors[src]
		if cursor == f.size {
			continue // nothing new — the steady state, and it costs a map lookup
		}
		raw, next, err := readSpan(f.path, cursor)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if raw == "" {
			// An unterminated tail (the file is being written right now).
			// Leave the cursor where it is and come back next pass.
			continue
		}
		n++
		// The episode stores the REDUCED span — user prose, tool calls,
		// assistant text; thinking, tool results and attachments dropped —
		// via the one implementation of that rule (core's reducer.line,
		// shared with k mine reduce; a queued mid-turn steer whose two
		// records straddle a span boundary can enter twice — a duplicate
		// episode, never a loss). Storing raw spans measured as ~177 MB/day of
		// mostly tool-result bytes into kraken.db (review finding). The
		// CURSOR still advances over the raw bytes, and the ref records the
		// raw span, so nothing is lost: the extraction agent can reach the
		// original with k mine reduce when the reduced text is not enough.
		content := string(core.ReduceJSONLBytes([]byte(raw)))
		ref := fmt.Sprintf("%s#%d-%d", f.path, spanStart(cursor, f.size), next)
		if content != "" {
			if err := addEpisode(st, "transcript", ref, content, f.mtime); err != nil {
				return err
			}
		}
		if err := st.SetIngestCursor(src, next); err != nil {
			return err
		}
	}
	if firstErr != nil {
		return firstErr
	}
	return walkErr
}

// spanStart reports where a span actually began, which is 0 rather than the
// cursor when the file was replaced by a shorter one (readSpan's rewind). The
// ref has to say where the bytes came from, not where we hoped they would.
func spanStart(cursor, size int64) int64 {
	if cursor > size {
		return 0
	}
	return cursor
}

// transcriptFiles lists every claude transcript on the machine — root
// sessions AND the `<session>/subagents/agent-*.jsonl` sidecars, which carry
// ~24% of all tool calls and which core/tokens.go is otherwise the only
// reader of.
//
// Sorted, so the drain is deterministic and monotonic: a pass always works
// from the front of the same list, so an early file that is fully drained
// costs nothing and the frontier moves forward instead of oscillating.
//
// An unreadable subtree is REPORTED rather than silently omitted, on this
// repo's standing rule that a failed read is not a zero — but unlike the
// token scan it is not dangerous, since a per-source cursor cannot be deleted
// by an answer that came up short (see sweepTranscripts).
func transcriptFiles() ([]transcriptFile, error) {
	root := filepath.Join(util.Home(), ".claude", "projects")
	projects, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil // claude has never run here: no transcripts, no error
	}
	if err != nil {
		return nil, fmt.Errorf("list %s: %v", root, err)
	}
	var out []transcriptFile
	var firstErr error
	note := func(e error) {
		if firstErr == nil && !os.IsNotExist(e) {
			firstErr = e
		}
	}
	add := func(dir string, e os.DirEntry) {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			return
		}
		info, err := e.Info()
		if err != nil {
			note(err)
			return
		}
		out = append(out, transcriptFile{
			path:  filepath.Join(dir, e.Name()),
			size:  info.Size(),
			mtime: info.ModTime().UnixMilli(),
		})
	}
	for _, p := range projects {
		if !p.IsDir() {
			continue
		}
		pdir := filepath.Join(root, p.Name())
		entries, err := os.ReadDir(pdir)
		if err != nil {
			note(err)
			continue
		}
		for _, e := range entries {
			add(pdir, e)
			if !e.IsDir() {
				continue
			}
			// <session>/subagents/agent-*.jsonl
			sdir := filepath.Join(pdir, e.Name(), "subagents")
			sub, err := os.ReadDir(sdir)
			if err != nil {
				note(err)
				continue
			}
			for _, se := range sub {
				add(sdir, se)
			}
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].path < out[b].path })
	return out, firstErr
}

// readSpan reads one episode's worth of complete lines from path starting at
// from, returning the text and the offset just past the last newline it
// consumed.
//
// Three answers, all of them deliberate:
//
//   - ("", from, nil) — nothing complete to read: either the file has not
//     grown, or its tail is a line still being written. Both are resumed next
//     pass rather than skipped past.
//   - (text, next, nil) — a span of whole lines. next always lands just past
//     a newline, which is the invariant that makes the cursor safe to store.
//   - ("", from, err) — a line longer than graphEpisodeMax (32 MB, ~4x the
//     largest real record on this machine). Genuinely corrupt or a format
//     change; the CALLER skips this file and keeps draining the others —
//     erroring the whole sweep here is what once parked ingest at 96% of the
//     corpus (see graphEpisodeMax).
//
// A file shorter than `from` can only be a replacement (transcripts are
// append-only), so the read restarts at zero and the caller's cursor rewinds
// with it.
func readSpan(path string, from int64) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", from, nil // vanished between walk and read: not seen
		}
		return "", from, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", from, err
	}
	size := info.Size()
	if from > size || from < 0 {
		from = 0 // replacement: re-read whole
	}
	if from == size {
		return "", from, nil
	}
	window := int64(graphEpisodeBytes)
	if size-from < window {
		window = size - from
	}
	buf := make([]byte, window)
	n, err := f.ReadAt(buf, from)
	if err != nil && n == 0 {
		return "", from, fmt.Errorf("read %s at %d: %v", path, from, err)
	}
	buf = buf[:n]
	if idx := bytes.LastIndexByte(buf, '\n'); idx >= 0 {
		return string(buf[:idx+1]), from + int64(idx) + 1, nil
	}
	// No terminator inside one episode span: a single record longer than
	// graphEpisodeBytes. Real ones run to 8+ MB (an attachment-bearing
	// record; measured during review), so look as far as graphEpisodeMax for
	// the newline, store only the capped HEAD, and advance the cursor past
	// the whole record — the row is bounded, the cursor is not stuck, and
	// the ref still records the true span.
	wide := int64(graphEpisodeMax)
	if size-from < wide {
		wide = size - from
	}
	ext := make([]byte, wide)
	n, err = f.ReadAt(ext, from)
	if err != nil && n == 0 {
		return "", from, fmt.Errorf("read %s at %d: %v", path, from, err)
	}
	ext = ext[:n]
	idx := bytes.IndexByte(ext, '\n')
	if idx < 0 {
		if int64(len(ext)) >= graphEpisodeMax {
			return "", from, fmt.Errorf("%s: no line terminator within %d bytes of offset %d", path, graphEpisodeMax, from)
		}
		return "", from, nil // unterminated tail: mid-write, resume next pass
	}
	return capString(string(ext[:idx+1]), graphEpisodeBytes), from + int64(idx) + 1, nil
}

// ---------------------------------------------------------------------------
// The slow half: embeddings, the job-spec vector cache, and layout
// ---------------------------------------------------------------------------

// kickGraphSlow runs the parts of the pass that must not sit inside the
// daemon tick — a network round trip and a force simulation — in one
// single-flighted goroutine, the same shape KickUsagePass uses.
func kickGraphSlow(st *store.Store) {
	if !graphSlowRunning.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer graphSlowRunning.Store(false)
		graphSlowPass(st)
	}()
}

// graphSlowPass is kickGraphSlow's body, factored out so a test can drive it
// synchronously.
func graphSlowPass(st *store.Store) {
	emb, err := core.NewEmbedder()
	switch {
	case errors.Is(err, core.ErrNoEmbedder):
		// The normal state of a machine with no key configured. Retrieval
		// degrades to BM25-only and nothing here is a failure, so this is
		// SILENT — a line per minute saying a feature is not configured is
		// noise that trains the reader to ignore the log.
		emb = nil
	case err != nil:
		dlogErr("graph pass: embedder: %v", err)
		emb = nil
	}
	if emb != nil {
		if err := embedMissing(st, emb); err != nil {
			dlogErr("graph pass: embed: %v", err)
		}
		if err := refreshJobVectors(st, emb); err != nil {
			dlogErr("graph pass: job vectors: %v", err)
		}
	}
	if err := refreshLayout(st); err != nil {
		dlogErr("graph pass: layout: %v", err)
	}
}

// embedMissing fills in one batch of absent node/edge vectors. Rows are
// written without vectors and filled in here, so a down API or a missing key
// never blocks a write — it only leaves rows in this queue.
func embedMissing(st *store.Store, emb core.Embedder) error {
	miss, err := st.MissingEmbeddings(graphEmbedBatch)
	if err != nil || len(miss) == 0 {
		return err
	}
	texts := make([]string, 0, len(miss))
	for _, m := range miss {
		texts = append(texts, m.Text)
	}
	vecs, err := emb.Embed(texts)
	if err != nil {
		return err
	}
	if len(vecs) != len(miss) {
		// core.Embedder documents input order and count; a mismatch would
		// pair a vector with the wrong row, which is silent and permanent.
		return fmt.Errorf("embed returned %d vectors for %d texts", len(vecs), len(miss))
	}
	for i, m := range miss {
		if err := st.SetEmbedding(m.Kind, m.ID, core.EncodeVector(vecs[i])); err != nil {
			return err
		}
	}
	return nil
}

// refreshJobVectors keeps the job-spec vector cache current. This is what
// makes graphContext offline: the spawn path reads a stored vector rather
// than embedding the spec at spawn time, so a cell start never waits on (or
// fails because of) a network call.
//
// The hash is of the exact text embedded, so a spec edited back to a previous
// wording re-uses the cached vector rather than paying for it twice.
func refreshJobVectors(st *store.Store, emb core.Embedder) error {
	jobs, err := st.ListJobs()
	if err != nil {
		return err
	}
	n := 0
	for _, j := range jobs {
		if n >= graphJobVecBatch {
			break
		}
		text := capString(strings.TrimSpace(j.Body), graphSpecEmbedCap)
		if text == "" {
			continue
		}
		sum := sha256.Sum256([]byte(text))
		want := hex.EncodeToString(sum[:])
		if have, err := st.GetKV(graphJobVecHashKey(j.ID)); err == nil && have == want {
			continue
		} else if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		vecs, err := emb.Embed([]string{text})
		if err != nil {
			return err
		}
		if len(vecs) != 1 {
			return fmt.Errorf("embed returned %d vectors for 1 text", len(vecs))
		}
		n++
		// Vector first, hash second. A crash between the two writes leaves
		// the vector uncached-looking and it is simply recomputed; the other
		// order would record "cached" against a vector that was never stored,
		// and nothing would ever repair it.
		if err := st.SetKV(graphJobVecKey(j.ID), base64.StdEncoding.EncodeToString(core.EncodeVector(vecs[0]))); err != nil {
			return err
		}
		if err := st.SetKV(graphJobVecHashKey(j.ID), want); err != nil {
			return err
		}
	}
	return nil
}

// refreshLayout recomputes the cached node positions when the graph's
// revision has moved. The revision marker is the max edge id (GraphStats), and
// it is stored on every layout row — so "the layout is current" is a single
// integer comparison rather than a diff of the topology.
//
// Seeded from the existing positions, which is what keeps the picture stable:
// a new node joins the map where its neighbours already are instead of
// reshuffling everything a viewer had learned to read.
func refreshLayout(st *store.Store) error {
	// Time floor first, before any store read: see graphLayoutInterval.
	nowMS := time.Now().UnixMilli()
	if last := graphLastLayout.Load(); last != 0 && nowMS-last < graphLayoutInterval.Milliseconds() {
		return nil
	}
	stats, err := st.GetGraphStats()
	if err != nil {
		return err
	}
	if stats.Nodes == 0 {
		return nil
	}
	prev, err := st.Layout()
	if err != nil {
		return err
	}
	if len(prev) > 0 {
		current := true
		for _, p := range prev {
			if p.Rev != stats.MaxEdgeID {
				current = false
				break
			}
		}
		// A node added with no edge does not move MaxEdgeID, so the row count
		// is checked too — otherwise an isolated node would never be placed.
		if current && int64(len(prev)) == stats.Nodes {
			return nil
		}
	}
	nodes, err := st.AllNodes()
	if err != nil {
		return err
	}
	edges, err := st.AllEdges()
	if err != nil {
		return err
	}
	iterations := graphLayoutIterationsCold
	if len(prev) > 0 {
		iterations = graphLayoutIterationsWarm
	}
	pos := core.ComputeLayout(nodes, edges, prev, iterations)
	for i := range pos {
		pos[i].Rev = stats.MaxEdgeID
	}
	if err := st.SetLayout(pos); err != nil {
		return err
	}
	// Stamped on SUCCESS, unlike the pass throttle: a failed layout write
	// should retry at the rev check's own pace, not sit out the full floor.
	graphLastLayout.Store(time.Now().UnixMilli())
	return nil
}

// ---------------------------------------------------------------------------
// Graph jobs
// ---------------------------------------------------------------------------

// The two graph job shapes (Q3). Both send the cell into the same skill; the
// spec text is what selects the mode, because a job's `type` was removed from
// this codebase deliberately (Q14) and re-introducing one for this feature
// would be the only one left.
const (
	// GraphModeIngest drains the pending episode queue into nodes and edges.
	// Mechanical, opens no PR.
	GraphModeIngest = "ingest"
	// GraphModeRevision carries carl's instruction to correct the graph:
	// merge, split, re-describe, or invalidate.
	GraphModeRevision = "revision"
)

// CreateGraphJob writes a graph job's row and returns it loaded, ready to be
// queued — the sibling of CreateDreamJob, and built the same way for the same
// reason: render the job's TEXT, ParseJob it, and create the row from the
// parsed fields, so the row can never disagree with what the same text would
// have produced had a human pasted it into the board's editor.
//
// instruction is carl's revision brief and is required in revision mode
// (a revision job with nothing to revise would spawn a cell to do nothing)
// and ignored in ingest mode.
//
// ingestCap is how many episodes the run may drain, written into the ingest
// spec as the `Per-run episode cap:` line the skill reads. It is a PARAMETER
// rather than a constant because the only caller that can size it — kickGraph,
// via graphIngestCap — is the only one holding the queue depth; a hand-made
// job that passes 0 gets the skill's own default of 25. Revision mode ignores
// it entirely: a revision run drains nothing.
func CreateGraphJob(st *store.Store, slug, mode, instruction string, ingestCap int) (*Job, error) {
	if st == nil {
		return nil, fmt.Errorf("graph job %s: no store", slug)
	}
	switch mode {
	case GraphModeIngest:
	case GraphModeRevision:
		if strings.TrimSpace(instruction) == "" {
			return nil, fmt.Errorf("graph job %s: revision mode needs an instruction", slug)
		}
	default:
		return nil, fmt.Errorf("graph job %s: unknown mode %q", slug, mode)
	}
	content := graphJobContent(slug, mode, instruction, ingestCap)
	parsed := ParseJob(slug+JobSuffix, []byte(content))
	if parsed.Err != "" {
		return nil, fmt.Errorf("graph job %s does not parse: %s", slug, parsed.Err)
	}
	if strings.TrimSpace(parsed.Body) == "" {
		// startable() does not check for a body — only the board's queue
		// action does — so an empty one would start a cell with an empty
		// prompt. Belt and braces on top of the template below, exactly as
		// CreateDreamJob carries.
		return nil, fmt.Errorf("graph job %s has an empty body", slug)
	}
	if _, err := st.CreateJob(&store.Job{
		Slug: slug, Repo: parsed.Repo, Base: parsed.Base,
		Ready: parsed.Ready, Solo: parsed.Solo, Model: parsed.Model,
		Body: parsed.Body,
	}); err != nil {
		return nil, err
	}
	sr, err := st.GetJobBySlug(slug)
	if err != nil {
		return nil, fmt.Errorf("graph job %s created but could not be reloaded: %w", slug, err)
	}
	return loadJobFull(st, sr)
}

// graphJobContent renders a graph job's file text. ready: true — both shapes
// are meant to start unattended — and solo: false, so the cell may fan
// extraction out across episodes.
//
// The body says as little as possible: the procedure lives in the skill, and
// the only things the skill cannot know are which mode this job is and what
// carl asked for. It is read BY PATH because a personal ~/.claude/skills/graph
// would otherwise shadow the repo-owned copy materialized into the cell's
// worktree — the same hazard dreamJobContent spells out.
//
// The one thing the body does carry beyond the mode is the ingest cap, and it
// is carried as SPEC TEXT rather than as a flag the skill would have to be
// taught to read from somewhere else: the spec is the only channel between the
// daemon that knows the queue depth and the cell that drains it.
func graphJobContent(slug, mode, instruction string, ingestCap int) string {
	head := "---\n" +
		"job: " + slug + "\n" +
		"repo: kraken\n" +
		"ready: true\n" +
		"solo: false\n" +
		"prereqs: []\n" +
		"---\n" +
		"\n" +
		"Read `.claude/skills/graph/SKILL.md` **by path** from this worktree and follow it\n" +
		"end to end. Read it by path rather than invoking `/graph`: a personal skill of the\n" +
		"same name shadows the repo-owned copy, and the repo-owned copy is the one that\n" +
		"knows about cells.\n" +
		"\n"
	if mode == GraphModeRevision {
		return head +
			"## Mode: revision\n" +
			"\n" +
			"Follow the skill's **Revision mode** section. Carl's instruction, verbatim:\n" +
			"\n" +
			instruction + "\n" +
			"\n" +
			"Change only what the instruction reaches. A revision job opens no PR — the\n" +
			"graph lives in `kraken.db`, not in git.\n"
	}
	// 0 means "no caller sized this" (a hand-made job, or a caller written
	// before the parameter existed), and the floor — which is also the value
	// the skill defaults to when the spec carries no cap line at all — is the
	// honest answer to that, not a cap of zero, which would spawn a cell to
	// drain nothing.
	perRun := ingestCap
	if perRun <= 0 {
		perRun = graphIngestCapMin
	}
	return head +
		"## Mode: ingest\n" +
		"\n" +
		fmt.Sprintf("Per-run episode cap: %d\n", perRun) +
		"\n" +
		"Follow the skill's **Ingest mode** section: drain `k graph pending`, extract, resolve\n" +
		"against what already exists, and write with `k graph add`. Stop when the queue is\n" +
		"empty or the cap above is reached, whichever comes first.\n" +
		"\n" +
		"An ingest job opens no PR — nothing it produces belongs in git.\n"
}

// ---------------------------------------------------------------------------
// kickGraph — the nightly ingest trigger
// ---------------------------------------------------------------------------

// graphIngestKVKey is the kv watermark holding the LOCAL DATE (dream's
// dreamDateLayout) of the last nightly ingest job created. A date rather than
// an elapsed duration for kickDream's stated reason: a Mac asleep at midnight
// fires on the first tick after it wakes instead of skipping the night.
const graphIngestKVKey = "graph_ingest_last"

// The nightly batch's shape. A cap that scales with the BACKLOG rather than a
// constant, because a constant is what the queue outran: measured on this
// machine 2026-08-03, 1,997 episodes sat pending against graphPendingHighWater's
// 2,000, with 86 and 85 new episodes arriving on each of the two days before —
// and on 2026-08-01 not one of them was a transcript, since the sweep was
// already backpressured off. A fixed 25 a night against ~85 a day is a drain
// that loses ground every night, not a slow one.
//
// The floor keeps a quiet week cheap (an empty-ish queue still gets exactly one
// batch, and 25 is what the skill has always defaulted to), and the divisor is
// picked so the high-water mark itself asks for 100 — comfortably past that
// measured arrival rate, so the queue moves backwards instead of forwards.
//
// graphIngestCapMin has a THIRD site that must agree with it and that Go cannot
// see: the skill's "when the spec carries no such line, use 25" fallback in
// .claude/skills/graph/SKILL.md. Raising this floor silently changes the
// hand-made-job default (graphJobContent's <= 0 branch) while leaving that
// fallback behind, so a cell reading a capless spec would drain a different
// number than the same cell reading a spec the daemon wrote. Move both.
//
// The ceiling exists because pending is not bounded by the high-water mark:
// backpressure gates only the transcript sweep, and the row sources stay
// ungated by design (see sweepRows and graphPendingHighWater), so a backfill
// can push pending well past 2,000. Without the ceiling one night's cell would
// be handed a batch it cannot finish in one run, which costs the spend and
// produces a partially-drained queue either way.
const (
	graphIngestCapDivisor = 20
	graphIngestCapMin     = 25
	graphIngestCapMax     = 150
)

// graphIngestCap sizes one nightly run's batch from the pending queue's depth:
// clamp(pending/20, 25, 150). Integer division, so the cap only steps up once
// the backlog has actually grown by a whole batch-twentieth.
func graphIngestCap(pending int64) int {
	n := pending / graphIngestCapDivisor
	if n < graphIngestCapMin {
		return graphIngestCapMin
	}
	if n > graphIngestCapMax {
		return graphIngestCapMax
	}
	return int(n)
}

// kickGraph creates at most one nightly ingest job per local calendar day,
// and only when there is actually something to drain. It is what makes the
// whole extraction side reachable: without a caller, CreateGraphJob was
// exported dead code, graphPass enqueued episodes forever, and nothing ever
// turned one into a fact (review finding — the feature shipped inert).
//
// Deliberately smaller than kickDream: no swept-window bookkeeping (the
// pending queue IS the work list, owned by the store), no in-memory date
// guard (one kv read per tick is nothing), and the cell-storm guard is
// structural — one date row, plus a skip while any active graph job exists,
// so a night whose job is still running does not stack a second one.
func kickGraph(st *store.Store, now time.Time) *Job {
	if st == nil {
		return nil
	}
	today := now.Format(dreamDateLayout)
	if last, err := st.GetKV(graphIngestKVKey); err == nil && last >= today {
		return nil // >= for kickDream's backwards-clock reason
	}
	stats, err := st.GetGraphStats()
	if err != nil || stats.PendingEpisodes == 0 {
		return nil // nothing to drain: no job, and the date stays unstamped
	}
	jobs, err := st.ListJobs()
	if err != nil {
		return nil
	}
	slug := "graph-" + today
	for _, j := range jobs {
		if j.Slug == slug {
			// Already created today (the kv write below failed, or a hand-made
			// job took the name): stamp and stand down rather than colliding.
			_ = st.SetKV(graphIngestKVKey, today)
			return nil
		}
		// Any ACTIVE graph job — last night's still draining — means no new
		// one: two cells writing k graph add concurrently is safe for the
		// store but doubles the extraction spend on the same queue.
		if strings.HasPrefix(j.Slug, "graph-") && j.State != "done" && j.Ready {
			return nil
		}
	}
	// The stats read above is the only place the queue depth is in hand, so it
	// is where the batch gets sized — see graphIngestCap for why a constant is
	// not enough.
	created, err := CreateGraphJob(st, slug, GraphModeIngest, "", graphIngestCap(stats.PendingEpisodes))
	if err != nil {
		dlogErr("kick graph: %v", err)
		return nil
	}
	if err := st.SetKV(graphIngestKVKey, today); err != nil {
		// The slug check above makes a re-fire on the next tick a no-op, so a
		// failed stamp costs one extra ListJobs, not a second job.
		dlogErr("kick graph: stamp date: %v", err)
	}
	return created
}
