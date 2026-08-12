package jobs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kraken/internal/store"
)

// graph_test.go — the ingest sweep's arithmetic and the graph job creator.
//
// The cursor rules are the part worth testing directly: they are core.SumTokensDelta's
// rules restated for a different payload, and every one of them fails SILENTLY
// when it is wrong — a span read twice, a span never read, a cursor parked in
// the middle of a JSON record.

func graphTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "kraken.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestReadSpanLandsOnNewline is the rule that makes a stored cursor safe: an
// offset that stopped mid-record would make the next pass's episode start with
// a JSON fragment, which nothing downstream can parse and nothing reports.
func TestReadSpanLandsOnNewline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	writeFile(t, path, "{\"a\":1}\n{\"b\":2}\n")
	got, next, err := readSpan(path, 0)
	if err != nil {
		t.Fatalf("readSpan: %v", err)
	}
	if got != "{\"a\":1}\n{\"b\":2}\n" {
		t.Errorf("content = %q", got)
	}
	if next != 16 {
		t.Errorf("next = %d, want 16 (just past the final newline)", next)
	}
	if !strings.HasSuffix(got, "\n") {
		t.Error("span does not end on a line boundary")
	}
}

// TestReadSpanResumesAnUnterminatedTail: a transcript caught mid-write must be
// resumed on a later pass, never skipped past. Advancing over the partial line
// would lose that record permanently, since the cursor only moves forward.
func TestReadSpanResumesAnUnterminatedTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	writeFile(t, path, "{\"a\":1}\n{\"partial\":")
	got, next, err := readSpan(path, 0)
	if err != nil {
		t.Fatalf("readSpan: %v", err)
	}
	if got != "{\"a\":1}\n" || next != 8 {
		t.Fatalf("content = %q, next = %d, want the first complete line only", got, next)
	}
	// Now from the tail: nothing complete, so nothing is consumed.
	got, next, err = readSpan(path, 8)
	if err != nil {
		t.Fatalf("readSpan (tail): %v", err)
	}
	if got != "" || next != 8 {
		t.Errorf("an unterminated tail was consumed: content = %q, next = %d", got, next)
	}
	// Once the writer finishes the line, the same offset picks it up.
	writeFile(t, path, "{\"a\":1}\n{\"partial\":2}\n")
	got, next, err = readSpan(path, 8)
	if err != nil {
		t.Fatalf("readSpan (completed): %v", err)
	}
	if got != "{\"partial\":2}\n" || next != 22 {
		t.Errorf("content = %q, next = %d", got, next)
	}
}

// TestReadSpanRewindsOnAShorterFile: a transcript shorter than its own cursor
// can only be a replacement (transcripts are append-only), so it is re-read
// whole rather than reported as having nothing new.
func TestReadSpanRewindsOnAShorterFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	writeFile(t, path, "{\"new\":1}\n")
	got, next, err := readSpan(path, 5_000)
	if err != nil {
		t.Fatalf("readSpan: %v", err)
	}
	if got != "{\"new\":1}\n" || next != 10 {
		t.Errorf("content = %q, next = %d — a replaced file must be re-read from zero", got, next)
	}
	if spanStart(5_000, 10) != 0 {
		t.Error("spanStart must report 0 for a rewound read, or the ref lies about where the bytes came from")
	}
}

// TestReadSpanNothingNew pins the steady state: a settled file costs one open
// and one stat, and returns nothing rather than re-reading its tail.
func TestReadSpanNothingNew(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	writeFile(t, path, "{\"a\":1}\n")
	got, next, err := readSpan(path, 8)
	if err != nil || got != "" || next != 8 {
		t.Errorf("readSpan at EOF = (%q, %d, %v), want (\"\", 8, nil)", got, next, err)
	}
}

// TestReadSpanErrorsOnAnUnterminatedMonster: a "line" longer than
// graphEpisodeMax is a corrupt file, and it is the one case that must ERROR
// rather than silently stall — a cursor that stops advancing with nothing
// logged is indistinguishable from a machine with no new transcripts.
func TestReadSpanErrorsOnAnUnterminatedMonster(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	writeFile(t, path, strings.Repeat("x", graphEpisodeMax+16))
	if _, _, err := readSpan(path, 0); err == nil {
		t.Error("a line over the ceiling was accepted silently")
	}
}

// TestReadSpanCapsALongRecord: a single record longer than one episode span
// still advances (real ones reach tens of KB), but the stored content is
// capped and SAYS it was truncated — the ref is what the reader follows back.
func TestReadSpanCapsALongRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	line := strings.Repeat("y", graphEpisodeBytes*2)
	writeFile(t, path, line+"\n")
	got, next, err := readSpan(path, 0)
	if err != nil {
		t.Fatalf("readSpan: %v", err)
	}
	if next != int64(len(line))+1 {
		t.Errorf("next = %d, want %d (just past the record's newline)", next, len(line)+1)
	}
	if !strings.Contains(got, "[truncated]") {
		t.Error("a capped record does not say it was truncated")
	}
	if len(got) > graphEpisodeBytes+64 {
		t.Errorf("capped content is %d bytes, well over the %d cap", len(got), graphEpisodeBytes)
	}
}

// TestCapStringKeepsRunesWhole guards the content column: a cut through a
// multi-byte character is a corrupt row, not a shorter one.
func TestCapStringKeepsRunesWhole(t *testing.T) {
	s := strings.Repeat("知", 10) // 30 bytes
	got := capString(s, 10)      // 10 is not a rune boundary (3 | 10 is false)
	head := strings.TrimSuffix(got, "\n…[truncated]")
	if len(head)%3 != 0 {
		t.Errorf("cut mid-rune: head is %d bytes", len(head))
	}
	if !strings.HasSuffix(got, "[truncated]") {
		t.Error("truncation is not marked")
	}
	if capString("short", 100) != "short" {
		t.Error("a string under the cap was altered")
	}
}

// TestAdvanceCursorNeverRegresses: a source whose watermark did not move must
// cost zero writes, which is what makes a once-a-minute sweep over every row
// table free in the steady state. It must also never be written BACKWARDS from
// a batch that happened to see older rows first.
func TestAdvanceCursorNeverRegresses(t *testing.T) {
	st := graphTestStore(t)
	if err := st.SetIngestCursor("rows:test", 50); err != nil {
		t.Fatalf("seed cursor: %v", err)
	}
	if err := advanceCursor(st, "rows:test", 50, 40); err != nil {
		t.Fatalf("advanceCursor: %v", err)
	}
	got, err := st.IngestCursor("rows:test")
	if err != nil {
		t.Fatalf("read cursor: %v", err)
	}
	if got != 50 {
		t.Errorf("cursor regressed to %d", got)
	}
	if err := advanceCursor(st, "rows:test", 50, 90); err != nil {
		t.Fatalf("advanceCursor: %v", err)
	}
	if got, _ := st.IngestCursor("rows:test"); got != 90 {
		t.Errorf("cursor = %d, want 90", got)
	}
}

// TestSweepRowsIngestsOnceThenSettles is the integration the spec asks for:
// two passes over the same rows enqueue each episode exactly once.
func TestSweepRowsIngestsOnceThenSettles(t *testing.T) {
	st := graphTestStore(t)
	if _, err := st.AddFeedback(store.Feedback{Artifact: "50-components.md", Note: "edges run through boxes", TS: 100}); err != nil {
		t.Fatalf("add feedback: %v", err)
	}
	if _, err := st.CreateJob(&store.Job{Slug: "demo", Repo: "kraken", Body: "do the thing"}); err != nil {
		t.Fatalf("create job: %v", err)
	}

	sweep := func() int {
		cursors, err := st.AllIngestCursors()
		if err != nil {
			t.Fatalf("cursors: %v", err)
		}
		if err := sweepRows(st, cursors); err != nil {
			t.Fatalf("sweepRows: %v", err)
		}
		eps, err := st.PendingEpisodes(100)
		if err != nil {
			t.Fatalf("pending: %v", err)
		}
		return len(eps)
	}
	first := sweep()
	if first != 2 {
		t.Fatalf("first sweep enqueued %d episodes, want 2 (feedback, job)", first)
	}
	if second := sweep(); second != first {
		t.Errorf("second sweep enqueued %d more episodes — a settled source must cost nothing", second-first)
	}
}

// TestSweepRowsReIngestsAnEditedSpec pins the reason job specs are swept on
// updated_at rather than on id: the first version of a spec is routinely the
// wrong version, and the hash dedupe is what makes re-crossing the cursor safe.
func TestSweepRowsReIngestsAnEditedSpec(t *testing.T) {
	st := graphTestStore(t)
	id, err := st.CreateJob(&store.Job{Slug: "demo", Repo: "kraken", Body: "first wording"})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	cursors, _ := st.AllIngestCursors()
	if err := sweepRows(st, cursors); err != nil {
		t.Fatalf("sweepRows: %v", err)
	}
	row, err := st.GetJobByID(id)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if _, err := st.UpdateJobFields(id, row.Rev, map[string]any{"body": "second, much clearer wording"}); err != nil {
		t.Fatalf("update job: %v", err)
	}
	cursors, _ = st.AllIngestCursors()
	if err := sweepRows(st, cursors); err != nil {
		t.Fatalf("sweepRows: %v", err)
	}
	eps, err := st.PendingEpisodes(100)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	n := 0
	for _, e := range eps {
		if e.Kind == "job" {
			n++
		}
	}
	if n != 2 {
		t.Errorf("an edited spec produced %d job episodes, want 2 (the old wording and the new)", n)
	}
}

// TestSweepTranscriptsAdvancesAndSettles: a growing transcript is ingested in
// spans, each exactly once, and a settled one costs nothing.
func TestSweepTranscriptsAdvancesAndSettles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".claude", "projects", "-Users-x-wt-kraken")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, "session.jsonl")
	// The fixture lines must SURVIVE reduction (user/assistant records):
	// sweepTranscripts stores core.ReduceJSONLBytes' output, and a line with
	// no type field reduces to nothing and produces no episode at all —
	// which is itself pinned below.
	line1 := "{\"type\":\"user\",\"message\":{\"content\":\"one\"}}\n"
	line2 := "{\"type\":\"user\",\"message\":{\"content\":\"two\"}}\n"
	writeFile(t, path, line1)

	st := graphTestStore(t)
	sweep := func() []store.Episode {
		cursors, err := st.AllIngestCursors()
		if err != nil {
			t.Fatalf("cursors: %v", err)
		}
		if err := sweepTranscripts(st, cursors); err != nil {
			t.Fatalf("sweepTranscripts: %v", err)
		}
		eps, err := st.PendingEpisodes(100)
		if err != nil {
			t.Fatalf("pending: %v", err)
		}
		return eps
	}
	eps := sweep()
	if len(eps) != 1 || eps[0].Kind != "transcript" {
		t.Fatalf("first sweep produced %d episodes, want 1 transcript", len(eps))
	}
	if !strings.Contains(eps[0].Ref, fmt.Sprintf("#0-%d", len(line1))) {
		t.Errorf("ref %q does not record the RAW byte span (cursor advances over raw bytes, not reduced)", eps[0].Ref)
	}
	if !strings.Contains(eps[0].Content, "one") || strings.Contains(eps[0].Content, "type") {
		t.Errorf("content %q is not the reduced form", eps[0].Content)
	}
	if got := sweep(); len(got) != 1 {
		t.Errorf("a settled transcript produced %d more episodes", len(got)-1)
	}
	// Append: only the NEW bytes become an episode.
	writeFile(t, path, line1+line2)
	eps = sweep()
	if len(eps) != 2 {
		t.Fatalf("an appended transcript produced %d episodes, want 2", len(eps))
	}
	if !strings.Contains(eps[1].Content, "two") || strings.Contains(eps[1].Content, "one") {
		t.Errorf("second episode re-read old bytes or lost new ones: %q", eps[1].Content)
	}
	// A span that reduces to NOTHING (tool results, bookkeeping) advances the
	// cursor without writing an episode — backfill must not fill kg_episodes
	// with empty rows just to move a cursor.
	writeFile(t, path, line1+line2+"{\"type\":\"file-history\"}\n")
	eps = sweep()
	if len(eps) != 2 {
		t.Fatalf("a reduced-to-empty span wrote an episode: %d rows", len(eps))
	}
	cursors, err := st.AllIngestCursors()
	if err != nil {
		t.Fatal(err)
	}
	if got := cursors[graphSrcTranscript(path)]; got != int64(len(line1+line2)+len("{\"type\":\"file-history\"}\n")) {
		t.Errorf("cursor did not advance over the empty-reduction span: %d", got)
	}
}

// TestTranscriptFilesFindsSidecars: the subagents/agent-*.jsonl sidecars carry
// ~24% of all tool calls and core/tokens.go is otherwise the only reader that
// opens them — a sweep that missed them would miss a quarter of the evidence.
func TestTranscriptFilesFindsSidecars(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	proj := filepath.Join(home, ".claude", "projects", "-Users-x-wt-kraken")
	side := filepath.Join(proj, "sess-1", "subagents")
	if err := os.MkdirAll(side, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFile(t, filepath.Join(proj, "sess-1.jsonl"), "{}\n")
	writeFile(t, filepath.Join(side, "agent-7.jsonl"), "{}\n")

	files, err := transcriptFiles()
	if err != nil {
		t.Fatalf("transcriptFiles: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("found %d transcripts, want 2 (root + sidecar): %v", len(files), files)
	}
	// Sorted, which is what makes the drain deterministic and monotonic.
	if files[0].path > files[1].path {
		t.Error("transcriptFiles is not sorted")
	}
}

// TestTranscriptFilesAbsentProjectsDirIsNotAnError: a machine where claude has
// never run has provably zero transcripts. That is an answer, not a
// degradation — the same split anyTranscriptSince and JobProjectDirs both make.
func TestTranscriptFilesAbsentProjectsDirIsNotAnError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	files, err := transcriptFiles()
	if err != nil || len(files) != 0 {
		t.Errorf("transcriptFiles on a bare home = (%d files, %v), want (0, nil)", len(files), err)
	}
}

// TestCreateGraphJobBothModes pins the creator's contract: a row the existing
// queue pass can start unmodified, whose spec reads the skill BY PATH.
func TestCreateGraphJobBothModes(t *testing.T) {
	for _, c := range []struct {
		name, mode, instruction string
		wants                   []string
	}{
		{"ingest", GraphModeIngest, "", []string{"Mode: ingest", "k graph pending", "opens no PR"}},
		{"revision", GraphModeRevision, "merge `zeus` and `zeus agent` — they are one thing",
			[]string{"Mode: revision", "merge `zeus` and `zeus agent`"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := graphTestStore(t)
			j, err := CreateGraphJob(st, "graph-"+c.name, c.mode, c.instruction, 0)
			if err != nil {
				t.Fatalf("CreateGraphJob: %v", err)
			}
			if j.ID == 0 || j.Slug != "graph-"+c.name {
				t.Fatalf("job = %+v", j)
			}
			if j.State != "" {
				t.Errorf("a freshly created graph job must be a draft, got state %q — the tick submits it and SubmitJob refuses a started job", j.State)
			}
			if j.Err != "" {
				t.Errorf("job has a parse error: %s", j.Err)
			}
			// By PATH, never by /name: a personal skill of the same name
			// shadows the repo-owned copy materialized into the worktree, and
			// only the repo-owned copy knows about cells.
			if !strings.Contains(j.Body, ".claude/skills/graph/SKILL.md") {
				t.Error("spec does not point at the skill by path")
			}
			if strings.Contains(j.Body, "invoke `/graph`") && !strings.Contains(j.Body, "rather than invoking `/graph`") {
				t.Error("spec invokes the skill by name")
			}
			for _, want := range c.wants {
				if !strings.Contains(j.Body, want) {
					t.Errorf("spec is missing %q:\n%s", want, j.Body)
				}
			}
		})
	}
}

// TestCreateGraphJobRefusesBadInput: a revision job with nothing to revise
// would spawn a real cell to do nothing, and an unknown mode would spawn one
// with no instructions at all. Both are cheaper to refuse than to explain.
func TestCreateGraphJobRefusesBadInput(t *testing.T) {
	st := graphTestStore(t)
	if _, err := CreateGraphJob(st, "graph-x", GraphModeRevision, "   ", 0); err == nil {
		t.Error("a revision job with a blank instruction was created")
	}
	if _, err := CreateGraphJob(st, "graph-y", "somethingelse", "x", 0); err == nil {
		t.Error("a job with an unknown mode was created")
	}
	if _, err := CreateGraphJob(nil, "graph-z", GraphModeIngest, "", 0); err == nil {
		t.Error("a job was created against a nil store")
	}
}

// TestGraphPassThrottles pins the pacing rule tokensPass states: the stamp is
// written BEFORE any work can bail, so a pass that fails on its first query
// has still had its turn rather than retrying every 15-second tick.
func TestGraphPassThrottles(t *testing.T) {
	// A real HOME would put this test's transcript walk over the machine's
	// whole ~/.claude/projects tree — 3,050 files — for no assertion.
	t.Setenv("HOME", t.TempDir())
	st := graphTestStore(t)
	// Registered AFTER graphTestStore's Close, so it runs BEFORE it (cleanups
	// are LIFO): the background half of the pass holds this store handle, and
	// closing it underneath that goroutine is a panic in a goroutine no test
	// can recover.
	t.Cleanup(waitGraphSlow)
	graphLastPass.Store(0)
	t.Cleanup(func() { graphLastPass.Store(0) })

	now := time.UnixMilli(1_700_000_000_000)
	graphPass(st, now)
	stamped := graphLastPass.Load()
	if stamped != now.UnixMilli() {
		t.Fatalf("throttle stamp = %d, want %d", stamped, now.UnixMilli())
	}
	// A tick a second later must be a no-op, which is observable as the stamp
	// not moving.
	graphPass(st, now.Add(time.Second))
	if graphLastPass.Load() != stamped {
		t.Error("the pass ran again inside its own interval")
	}
	later := now.Add(graphPassInterval + time.Second)
	graphPass(st, later)
	if graphLastPass.Load() != later.UnixMilli() {
		t.Error("the pass did not run after its interval elapsed")
	}
}

// waitGraphSlow blocks until the background half of the pass has finished with
// its store handle.
func waitGraphSlow() {
	for graphSlowRunning.Load() {
		time.Sleep(time.Millisecond)
	}
}

// TestGraphPassNilStoreIsANoOp: file-mode entrypoints (the viewer, `k` itself)
// have no graph, and reaching for one would panic on every tick.
func TestGraphPassNilStoreIsANoOp(t *testing.T) {
	graphLastPass.Store(0)
	t.Cleanup(func() { graphLastPass.Store(0) })
	graphPass(nil, time.UnixMilli(1))
	if graphLastPass.Load() != 0 {
		t.Error("a nil store consumed the pass' throttle slot")
	}
}

// kickGraph's guards, each of which prevents a cell storm: no pending → no
// job; one per local day; an active graph job blocks a second.
func TestKickGraphGuards(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	st := graphTestStore(t)
	now := time.Date(2026, 7, 31, 3, 0, 0, 0, time.Local)

	// Empty queue: no job, and the date must stay unstamped so a queue that
	// fills later the same day still gets its job.
	if j := kickGraph(st, now); j != nil {
		t.Fatalf("empty queue created %q", j.Slug)
	}
	if _, err := st.GetKV(graphIngestKVKey); err == nil {
		t.Fatal("empty-queue decline stamped the date; a later fill today would be skipped")
	}

	if _, _, err := st.AddEpisode(store.Episode{Kind: "feedback", Hash: "k1"}); err != nil {
		t.Fatal(err)
	}
	j := kickGraph(st, now)
	if j == nil {
		t.Fatal("pending queue produced no job")
	}
	if j.Slug != "graph-2026-07-31" || j.State != "" {
		t.Fatalf("job = %+v", j)
	}
	if !strings.Contains(j.Body, ".claude/skills/graph/SKILL.md") {
		t.Errorf("spec does not read the skill by path: %q", j.Body)
	}

	// Same day again: the date stamp holds.
	if j2 := kickGraph(st, now.Add(time.Hour)); j2 != nil {
		t.Fatalf("second kick same day created %q", j2.Slug)
	}
	// Next day, queue still pending, and yesterday's job left as a DRAFT: its
	// start failed, so it will never drain anything on its own. Declining for
	// it would stop the ingest queue forever with nothing on screen saying
	// why — so the kick goes ahead.
	j3 := kickGraph(st, now.Add(24*time.Hour))
	if j3 == nil {
		t.Fatal("a stuck graph DRAFT blocked tonight's kick — only a running one may")
	}

	// Now put that job into a state where a cell actually exists. The day
	// after, the guard declines: two cells writing k graph add concurrently
	// doubles the extraction spend on one queue.
	row, err := st.GetJobBySlug(j3.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateJobFields(row.ID, row.Rev, map[string]any{"state": "working"}); err != nil {
		t.Fatal(err)
	}
	if j4 := kickGraph(st, now.Add(48*time.Hour)); j4 != nil {
		t.Fatalf("kick with a RUNNING graph job created %q", j4.Slug)
	}
}
