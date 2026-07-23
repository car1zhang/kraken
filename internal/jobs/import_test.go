package jobs

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"kraken/internal/store"
)

// --- fixture construction ---

const richJobFixture = `---
job: rich
type: build
repo: monolith
base: origin/develop
ready: true
model: opus
skip-doc: true
solo: true
state: done
outcome: shipped
session: sess-123
teardown: kill
run: 2
prereqs:
  - job: other-job
    gated: true
prs:
  - url: https://github.com/example/repo/pull/1
    state: merged
  - url: https://github.com/example/repo/pull/2
    state: open
---

# Spec
do the rich thing

## Mid-run log
<!-- appended by kraken; do not hand-edit -->
- [2026-07-01 10:00 me->zeus] hello
`

const minimalJobFixture = `---
job: minimal
type: info
ready: false
---

# Spec
minimal job body
`

const brokenJobFixture = `---
job: broken
type: build
bogus-key: oops
---

# Spec
broken
`

const archivedFooFixture = `---
job: foo
type: info
ready: true
state: done
outcome: closed
---

# Spec
archived foo doc
`

const orderJSONFixture = `{"columns": {"queued": ["minimal"], "done": ["rich", "foo"]}}`

const feedbackHeaderFixture = "time: 2026-07-01T10:00:00Z\njob: rich\nhome: /Users/carl\nartifact: report.md\nsource: /tmp/report.md\n\nthis diagram is confusing"

// legacyJobFixture exercises every legacy frontmatter shape that existing
// pluto job files still carry: auto-approve: (discarded scalar), success: |
// (discarded block scalar), and type: explore (parses as info) — all of
// which must still import with Err == "".
const legacyJobFixture = `---
job: legacy
type: explore
auto-approve: true
success: |
  old text line one
  old text line two
ready: true
---

# Spec
legacy job body
`

// dupSlugFixtureA/B share the frontmatter job: slug "dup" across two
// different files — the first (by filename order) must win; the second
// must be skipped and reported, never silently treated as an update.
const dupSlugFixtureA = `---
job: dup
type: info
ready: true
---

# Spec
dup A body
`

const dupSlugFixtureB = `---
job: dup
type: info
ready: false
---

# Spec
dup B body
`

// slugifyCollisionFixtureA/B are two DIFFERENT real slugs that
// util.Slugify reduces to the same string ("foo-bar") — both must still
// import as distinct job rows, but only the first may claim the shared
// uploads/cell-artifacts directory name.
const slugifyCollisionFixtureA = `---
job: Foo Bar
type: info
ready: true
---

# Spec
slugify collision A
`

const slugifyCollisionFixtureB = `---
job: foo-bar
type: info
ready: true
---

# Spec
slugify collision B
`

// archivedHelperCatalogFixture is a standalone archived-job fixture named
// after a real incident (cells/helper-catalog held real deliverables the
// importer never saw).
const archivedHelperCatalogFixture = `---
job: helper-catalog
type: info
ready: true
state: done
outcome: closed
---

# Spec
helper catalog archived job
`

// importFixture is everything one ImportAll test needs: the options
// pointing at a fresh set of fixture dirs under t.TempDir(), plus the list
// of every source file path written (for the never-modified-sources check).
type importFixture struct {
	opts    ImportOptions
	sources []string
}

func mustWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// newImportFixture lays out the full source tree the brief calls for: 2
// active jobs (rich, minimal), 1 parse-error active job (broken), 1
// archived numbered job (foo-2.job.md), a 2-column order.json, uploads for
// "rich" plus an orphan dir, a pluto flat-copy + cell-tree (with a nested
// subdir and a path collision) for "rich", an archive-dir flat-copy PLUS a
// cell-tree for the archived "foo" job, an unclaimed cell artifacts dir
// ("orphan-thing"), 2 JSONL log files (one malformed line), and 1 feedback
// entry.
func newImportFixture(t *testing.T) importFixture {
	t.Helper()
	root := t.TempDir()
	pluto := filepath.Join(root, "pluto")
	archive := filepath.Join(pluto, "archive")
	uploads := filepath.Join(root, "uploads")
	logs := filepath.Join(root, "logs")
	cells := filepath.Join(root, "cells")
	order := filepath.Join(root, "order.json")

	var sources []string
	write := func(path, content string) {
		mustWrite(t, path, []byte(content))
		sources = append(sources, path)
	}

	// active jobs
	write(filepath.Join(pluto, "rich.job.md"), richJobFixture)
	write(filepath.Join(pluto, "minimal.job.md"), minimalJobFixture)
	write(filepath.Join(pluto, "broken.job.md"), brokenJobFixture)

	// archived job: numbered file base "foo-2", frontmatter slug "foo"
	write(filepath.Join(archive, "foo-2.job.md"), archivedFooFixture)

	// board order
	write(order, orderJSONFixture)

	// uploads: one real job + one orphan dir
	write(filepath.Join(uploads, "rich", "img-001.png"), "png-bytes-1")
	write(filepath.Join(uploads, "rich", "img-002.jpg"), "jpg-bytes-2")
	write(filepath.Join(uploads, "ghost", "img-001.png"), "orphan-bytes")

	// artifacts: pluto flat-copy (also the collision target), archive
	// flat-copy, cell tree with a subdir and a colliding top-level file
	write(filepath.Join(pluto, "rich.report.md"), "flat copy version")
	write(filepath.Join(archive, "foo-2.summary.txt"), "archived summary")
	write(filepath.Join(cells, "rich", "artifacts", "report.md"), "cell version wins")
	write(filepath.Join(cells, "rich", "artifacts", "sub", "dir", "notes.txt"), "nested artifact")

	// archived job "foo"'s own cell artifacts dir — keyed by its Slug
	// ("foo"), not its numbered ArchivedName ("foo-2") — plus an unclaimed
	// cell dir belonging to no imported job at all.
	write(filepath.Join(cells, "foo", "artifacts", "foo-notes.txt"), "foo cell artifact")
	write(filepath.Join(cells, "orphan-thing", "artifacts", "junk.txt"), "orphan junk")

	// event logs: 2 files, 1 malformed line
	write(filepath.Join(logs, "20260101-000000.jsonl"), strings.Join([]string{
		`{"ts":1000,"src":"daemon","level":"info","event":"tick","msg":"tick 1"}`,
		`not json at all`,
		`{"ts":1001,"src":"daemon","level":"info","event":"tick","msg":"tick 2"}`,
		``,
	}, "\n"))
	write(filepath.Join(logs, "20260102-000000.jsonl"), strings.Join([]string{
		`{"ts":2000,"src":"web","level":"error","event":"boom","msg":"kaboom"}`,
		``,
	}, "\n"))
	// a non-JSONL file that must never be treated as an event log (Q16)
	write(filepath.Join(logs, "daemon.log"), "2026-01-01 daemon started\n")

	// feedback
	write(filepath.Join(pluto, "doc-feedback", "2026-07-01", "100000-rich", "feedback.txt"), feedbackHeaderFixture)
	write(filepath.Join(pluto, "doc-feedback", "2026-07-01", "100000-rich", "snapshot.png"), "snapshot-bytes")

	return importFixture{
		sources: sources,
		opts: ImportOptions{
			PlutoDir:   pluto,
			ArchiveDir: archive,
			UploadsDir: uploads,
			LogsDir:    logs,
			CellsDir:   cells,
			OrderPath:  order,
		},
	}
}

func hashSources(t *testing.T, paths []string) map[string]string {
	t.Helper()
	out := make(map[string]string, len(paths))
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("hash source %s: %v", p, err)
		}
		sum := sha256.Sum256(data)
		out[p] = hex.EncodeToString(sum[:])
	}
	return out
}

// reportsEqual compares two StoreReports by value: Found/Imported plus the
// Skipped reason count (not exact reason text, which can carry
// map-iteration-order-dependent detail elsewhere) — StoreReport can't use
// == directly since it holds a slice.
func reportsEqual(a, b StoreReport) bool {
	return a.Found == b.Found && a.Imported == b.Imported && len(a.Skipped) == len(b.Skipped)
}

func openImportTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "kraken.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// wantParseErrOnly asserts err is exactly the aggregate parse-error ImportAll
// returns because newImportFixture's broken.job.md never parses — every test
// against that shared fixture hits this: a parse error doesn't
// abort the sweep, it just makes ImportAll's return non-nil once everything
// else has been imported and reported).
func wantParseErrOnly(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("ImportAll: want an aggregate parse error for broken.job.md, got nil")
	}
	if !strings.Contains(err.Error(), "broken.job.md") {
		t.Fatalf("ImportAll err = %v, want it to mention broken.job.md", err)
	}
}

// --- tests ---

func TestImportAllCounts(t *testing.T) {
	fx := newImportFixture(t)
	st := openImportTestStore(t)

	report, err := ImportAll(st, fx.opts)
	wantParseErrOnly(t, err)

	if report.Jobs.Found != 3 || report.Jobs.Imported != 2 || len(report.Jobs.Skipped) != 1 {
		t.Fatalf("Jobs report = %+v", report.Jobs)
	}
	if report.Archived.Found != 1 || report.Archived.Imported != 1 {
		t.Fatalf("Archived report = %+v", report.Archived)
	}
	// board order counts slugs, not columns: queued:[minimal] + done:[rich,foo] = 3
	if report.BoardOrder.Found != 3 || report.BoardOrder.Imported != 3 {
		t.Fatalf("BoardOrder report = %+v", report.BoardOrder)
	}
	// rich: img-001.png + img-002.jpg = 2; ghost orphan: 1 file counted+skipped
	if report.Uploads.Found != 3 || report.Uploads.Imported != 2 || len(report.Uploads.Skipped) != 1 {
		t.Fatalf("Uploads report = %+v", report.Uploads)
	}
	// rich: flat report.md + cell report.md (collision) + cell sub/dir/notes.txt = 3 found, 2 rows (collision merges)
	// foo-2: archive flat summary.txt + cell foo-notes.txt = 2 found, 2 rows
	// plus 1 orphan cell dir (orphan-thing) reported, not counted as found
	if report.Artifacts.Found != 5 || report.Artifacts.Imported != 4 || len(report.Artifacts.Skipped) != 1 {
		t.Fatalf("Artifacts report = %+v", report.Artifacts)
	}
	if !strings.Contains(report.Artifacts.Skipped[0], "orphan-thing") {
		t.Fatalf("Artifacts skip note = %q, want it to name orphan-thing", report.Artifacts.Skipped[0])
	}
	// 3 valid lines + 1 malformed = 4 found, 3 imported, 1 skipped; daemon.log never touched
	if report.Events.Found != 4 || report.Events.Imported != 3 || len(report.Events.Skipped) != 1 {
		t.Fatalf("Events report = %+v", report.Events)
	}
	if report.Feedback.Found != 1 || report.Feedback.Imported != 1 {
		t.Fatalf("Feedback report = %+v", report.Feedback)
	}
}

func TestImportAllFieldRoundTrip(t *testing.T) {
	fx := newImportFixture(t)
	st := openImportTestStore(t)

	_, err := ImportAll(st, fx.opts)
	wantParseErrOnly(t, err)

	rich, err := st.GetJobBySlug("rich")
	if err != nil {
		t.Fatalf("GetJobBySlug rich: %v", err)
	}
	if rich.State != "done" || rich.Outcome != "shipped" || rich.Session != "sess-123" {
		t.Fatalf("rich state/outcome/session: %+v", rich)
	}
	if !rich.SkipDoc || rich.Model != "opus" {
		t.Fatalf("rich skip_doc/model: %+v", rich)
	}
	if rich.Repo != "monolith" || rich.Base != "origin/develop" || rich.Teardown != "kill" {
		t.Fatalf("rich repo/base/teardown: %+v", rich)
	}
	if !rich.Solo || !rich.Ready || rich.Run != 2 {
		t.Fatalf("rich solo/ready/run: %+v", rich)
	}

	// Body is stored verbatim — including the "## Mid-run log" section
	// (losslessness wins, every runtime reader strips it itself).
	wantBody := ParseJob("rich.job.md", []byte(richJobFixture)).Body
	if rich.Body != wantBody {
		t.Fatalf("rich body not stored verbatim:\ngot:  %q\nwant: %q", rich.Body, wantBody)
	}
	if !strings.Contains(rich.Body, "## Mid-run log") {
		t.Fatalf("rich body lost its mid-run log section: %q", rich.Body)
	}
	if !strings.Contains(rich.Body, "do the rich thing") {
		t.Fatalf("rich body lost its spec text: %q", rich.Body)
	}

	prereqs, err := st.ListPrereqs(rich.ID)
	if err != nil {
		t.Fatalf("ListPrereqs: %v", err)
	}
	if len(prereqs) != 1 || prereqs[0].Slug != "other-job" || !prereqs[0].Gated {
		t.Fatalf("rich prereqs: %+v", prereqs)
	}

	prs, err := st.ListPRs(rich.ID)
	if err != nil {
		t.Fatalf("ListPRs: %v", err)
	}
	if len(prs) != 2 {
		t.Fatalf("rich prs count: %+v", prs)
	}
	states := map[string]string{}
	for _, p := range prs {
		states[p.URL] = p.State
	}
	if states["https://github.com/example/repo/pull/1"] != "merged" || states["https://github.com/example/repo/pull/2"] != "open" {
		t.Fatalf("rich pr states: %+v", states)
	}

	archived, err := st.ListArchivedJobs()
	if err != nil {
		t.Fatalf("ListArchivedJobs: %v", err)
	}
	if len(archived) != 1 {
		t.Fatalf("archived count = %d, want 1", len(archived))
	}
	foo := archived[0]
	if foo.ArchivedName != "foo-2" || foo.Slug != "foo" || foo.State != "done" || foo.Outcome != "closed" {
		t.Fatalf("archived foo-2 round trip: %+v", foo)
	}

	minimal, err := st.GetJobBySlug("minimal")
	if err != nil {
		t.Fatalf("GetJobBySlug minimal: %v", err)
	}
	if minimal.Type != "info" || minimal.Ready {
		t.Fatalf("minimal round trip: %+v", minimal)
	}

	order, err := st.GetBoardOrder()
	if err != nil {
		t.Fatalf("GetBoardOrder: %v", err)
	}
	if len(order["queued"]) != 1 || order["queued"][0] != "minimal" {
		t.Fatalf("board order queued: %+v", order["queued"])
	}
	if len(order["done"]) != 2 {
		t.Fatalf("board order done: %+v", order["done"])
	}

	uploads, err := st.ListUploads(rich.ID)
	if err != nil {
		t.Fatalf("ListUploads: %v", err)
	}
	if len(uploads) != 2 {
		t.Fatalf("rich uploads: %+v", uploads)
	}

	artifacts, err := st.ListArtifacts(rich.ID)
	if err != nil {
		t.Fatalf("ListArtifacts: %v", err)
	}
	byPath := map[string]store.Artifact{}
	for _, a := range artifacts {
		byPath[a.Path] = a
	}
	if len(artifacts) != 2 {
		t.Fatalf("rich artifacts count: %+v", artifacts)
	}
	if got := string(byPath["report.md"].Bytes); got != "cell version wins" {
		t.Fatalf("collision: cell copy should win, got %q", got)
	}
	if got := string(byPath[filepath.ToSlash(filepath.Join("sub", "dir", "notes.txt"))].Bytes); got != "nested artifact" {
		t.Fatalf("nested artifact missing/wrong: %q", got)
	}

	// An archived job's cell artifacts dir (keyed by its Slug,
	// not its numbered ArchivedName) must be swept too, alongside its flat
	// archive-dir copy.
	fooArtifacts, err := st.ListArtifacts(foo.ID)
	if err != nil {
		t.Fatalf("ListArtifacts foo: %v", err)
	}
	if len(fooArtifacts) != 2 {
		t.Fatalf("foo-2 artifacts count: %+v", fooArtifacts)
	}
	fooByPath := map[string]store.Artifact{}
	for _, a := range fooArtifacts {
		fooByPath[a.Path] = a
	}
	if string(fooByPath["summary.txt"].Bytes) != "archived summary" {
		t.Fatalf("foo-2 flat artifact: %+v", fooByPath["summary.txt"])
	}
	if string(fooByPath["foo-notes.txt"].Bytes) != "foo cell artifact" {
		t.Fatalf("foo-2 cell artifact: %+v", fooByPath["foo-notes.txt"])
	}

	events, err := st.ListEvents(100, 0)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("events count = %d, want 3", len(events))
	}

	feedback, err := st.ListFeedback("")
	if err != nil {
		t.Fatalf("ListFeedback: %v", err)
	}
	if len(feedback) != 1 {
		t.Fatalf("feedback count = %d, want 1", len(feedback))
	}
	fb := feedback[0]
	if fb.JobSlug != "rich" || fb.Artifact != "report.md" || fb.SnapshotExt != "png" {
		t.Fatalf("feedback fields: %+v", fb)
	}
	if !strings.Contains(fb.Note, "this diagram is confusing") {
		t.Fatalf("feedback note: %q", fb.Note)
	}
	if string(fb.Snapshot) != "snapshot-bytes" {
		t.Fatalf("feedback snapshot bytes: %q", fb.Snapshot)
	}
}

func TestImportAllIdempotent(t *testing.T) {
	fx := newImportFixture(t)
	st := openImportTestStore(t)

	first, err := ImportAll(st, fx.opts)
	wantParseErrOnly(t, err)

	richAfterFirst, err := st.GetJobBySlug("rich")
	if err != nil {
		t.Fatalf("GetJobBySlug rich (after first): %v", err)
	}

	// a re-run against an already-populated jobs table requires Force
	// AND, since the first run already stamped imported_at,
	// Reimport too (E4) — the marker gate runs before the row-count gate.
	fx.opts.Force = true
	fx.opts.Reimport = true
	second, err := ImportAll(st, fx.opts)
	wantParseErrOnly(t, err)

	if !reportsEqual(first.Jobs, second.Jobs) {
		t.Fatalf("jobs report changed: %+v vs %+v", first.Jobs, second.Jobs)
	}
	if !reportsEqual(first.Archived, second.Archived) {
		t.Fatalf("archived report changed: %+v vs %+v", first.Archived, second.Archived)
	}
	if !reportsEqual(first.BoardOrder, second.BoardOrder) {
		t.Fatalf("board order report changed: %+v vs %+v", first.BoardOrder, second.BoardOrder)
	}
	if !reportsEqual(first.Uploads, second.Uploads) {
		t.Fatalf("uploads report changed: %+v vs %+v", first.Uploads, second.Uploads)
	}
	if !reportsEqual(first.Artifacts, second.Artifacts) {
		t.Fatalf("artifacts report changed: %+v vs %+v", first.Artifacts, second.Artifacts)
	}
	if first.Events.Imported != second.Events.Imported || first.Events.Found != second.Events.Found {
		t.Fatalf("events report changed: %+v vs %+v", first.Events, second.Events)
	}
	if first.Feedback.Imported != second.Feedback.Imported || first.Feedback.Found != second.Feedback.Found {
		t.Fatalf("feedback report changed: %+v vs %+v", first.Feedback, second.Feedback)
	}

	// row counts in the store must be identical, not doubled
	jobs, err := st.ListJobs()
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("jobs row count after 2 imports = %d, want 2", len(jobs))
	}
	archived, err := st.ListArchivedJobs()
	if err != nil {
		t.Fatalf("ListArchivedJobs: %v", err)
	}
	if len(archived) != 1 {
		t.Fatalf("archived row count after 2 imports = %d, want 1", len(archived))
	}
	events, err := st.ListEvents(100, 0)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("events row count after 2 imports = %d, want 3", len(events))
	}
	feedback, err := st.ListFeedback("")
	if err != nil {
		t.Fatalf("ListFeedback: %v", err)
	}
	if len(feedback) != 1 {
		t.Fatalf("feedback row count after 2 imports = %d, want 1", len(feedback))
	}
	var rich *store.Job
	for _, j := range jobs {
		if j.Slug == "rich" {
			rich = j
		}
	}
	if rich == nil {
		t.Fatalf("rich job missing after 2 imports")
	}
	// Re-importing an unchanged file must not churn rev/updated_at
	if rich.Rev != richAfterFirst.Rev || rich.UpdatedAt != richAfterFirst.UpdatedAt {
		t.Fatalf("rich rev/updated_at churned on re-import: before=%+v after=%+v", richAfterFirst, rich)
	}
	uploads, err := st.ListUploads(rich.ID)
	if err != nil {
		t.Fatalf("ListUploads: %v", err)
	}
	if len(uploads) != 2 {
		t.Fatalf("uploads row count after 2 imports = %d, want 2", len(uploads))
	}
	artifacts, err := st.ListArtifacts(rich.ID)
	if err != nil {
		t.Fatalf("ListArtifacts: %v", err)
	}
	if len(artifacts) != 2 {
		t.Fatalf("artifacts row count after 2 imports = %d, want 2", len(artifacts))
	}
	var foo *store.Job
	for _, j := range archived {
		if j.ArchivedName == "foo-2" {
			foo = j
		}
	}
	if foo == nil {
		t.Fatalf("foo-2 archived job missing after 2 imports")
	}
	fooArtifacts, err := st.ListArtifacts(foo.ID)
	if err != nil {
		t.Fatalf("ListArtifacts foo: %v", err)
	}
	if len(fooArtifacts) != 2 {
		t.Fatalf("foo-2 artifacts row count after 2 imports = %d, want 2", len(fooArtifacts))
	}
}

func TestImportAllDryRunWritesNothing(t *testing.T) {
	fx := newImportFixture(t)
	st := openImportTestStore(t)
	fx.opts.DryRun = true

	report, err := ImportAll(st, fx.opts)
	wantParseErrOnly(t, err) // dry-run also aggregates+returns the parse error
	if report.Jobs.Imported != 2 || report.Archived.Imported != 1 {
		t.Fatalf("dry-run should still count would-be imports: jobs=%+v archived=%+v", report.Jobs, report.Archived)
	}
	if report.BoardOrder.Found != 3 || report.BoardOrder.Imported != 3 {
		t.Fatalf("dry-run board order: %+v", report.BoardOrder)
	}

	jobs, err := st.ListJobs()
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("dry-run wrote %d job rows, want 0", len(jobs))
	}
	archived, err := st.ListArchivedJobs()
	if err != nil {
		t.Fatalf("ListArchivedJobs: %v", err)
	}
	if len(archived) != 0 {
		t.Fatalf("dry-run wrote %d archived rows, want 0", len(archived))
	}
	order, err := st.GetBoardOrder()
	if err != nil {
		t.Fatalf("GetBoardOrder: %v", err)
	}
	if len(order) != 0 {
		t.Fatalf("dry-run wrote board order: %+v", order)
	}
	events, err := st.ListEvents(100, 0)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("dry-run wrote %d event rows, want 0", len(events))
	}
	feedback, err := st.ListFeedback("")
	if err != nil {
		t.Fatalf("ListFeedback: %v", err)
	}
	if len(feedback) != 0 {
		t.Fatalf("dry-run wrote %d feedback rows, want 0", len(feedback))
	}

	// dry-run must leave uploads/artifacts at zero rows too —
	// there's no job id to call ListUploads/ListArtifacts against (no job
	// rows exist), so count directly.
	var uploadCount, artifactCount int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM uploads`).Scan(&uploadCount); err != nil {
		t.Fatalf("count uploads: %v", err)
	}
	if uploadCount != 0 {
		t.Fatalf("dry-run wrote %d upload rows, want 0", uploadCount)
	}
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM artifacts`).Scan(&artifactCount); err != nil {
		t.Fatalf("count artifacts: %v", err)
	}
	if artifactCount != 0 {
		t.Fatalf("dry-run wrote %d artifact rows, want 0", artifactCount)
	}
}

func TestImportAllSourcesUntouched(t *testing.T) {
	fx := newImportFixture(t)
	before := hashSources(t, fx.sources)

	st := openImportTestStore(t)
	_, err := ImportAll(st, fx.opts)
	wantParseErrOnly(t, err)
	// dry-run too, for good measure — no Force needed since the gate only
	// applies to non-dry runs
	fx.opts.DryRun = true
	_, err = ImportAll(st, fx.opts)
	wantParseErrOnly(t, err)

	after := hashSources(t, fx.sources)
	var changed []string
	for p, sum := range before {
		if after[p] != sum {
			changed = append(changed, p)
		}
	}
	sort.Strings(changed)
	if len(changed) != 0 {
		t.Fatalf("source files modified by ImportAll: %v", changed)
	}
}

func TestImportAllParseErrorJobSkipped(t *testing.T) {
	fx := newImportFixture(t)
	st := openImportTestStore(t)

	report, err := ImportAll(st, fx.opts)
	wantParseErrOnly(t, err)
	if len(report.Jobs.Skipped) != 1 || !strings.Contains(report.Jobs.Skipped[0], "broken.job.md") {
		t.Fatalf("expected broken.job.md listed as skipped, got %+v", report.Jobs.Skipped)
	}
	if _, err := st.GetJobBySlug("broken"); err == nil {
		t.Fatalf("broken job should not have been imported")
	}
}

// --- Force gate + imported_at stamp ---

func TestImportAllRefusesNonEmptyWithoutForce(t *testing.T) {
	fx := newImportFixture(t)
	st := openImportTestStore(t)

	_, err := ImportAll(st, fx.opts)
	wantParseErrOnly(t, err)

	// E4: the first run already stamped imported_at, so a bare re-run now
	// hits the Reimport gate before it ever reaches the Force/row-count
	// gate — isolate the Force gate below by granting Reimport but
	// withholding Force.
	fx.opts.Reimport = true
	report, err := ImportAll(st, fx.opts)
	if err == nil || !strings.Contains(err.Error(), "Force") {
		t.Fatalf("second ImportAll without Force: err = %v, want a Force-gate error", err)
	}
	if report.Jobs.Found != 0 {
		t.Fatalf("guarded run should short-circuit before reading any source: %+v", report)
	}

	fx.opts.Force = true
	report2, err := ImportAll(st, fx.opts)
	wantParseErrOnly(t, err)
	if strings.Contains(err.Error(), "Force") {
		t.Fatalf("Force run should not hit the non-empty gate: %v", err)
	}
	if report2.Jobs.Found != 3 {
		t.Fatalf("Force run should complete the full sweep: %+v", report2.Jobs)
	}
}

// TestImportAllRefusesReimportWithoutFlag covers E4 directly at the
// ImportAll level (cmd_import_test.go's TestRunImportRefusesSecondImport-
// WithoutReimport covers the same contract through the CLI wrapper):
// once a non-dry-run import has stamped imported_at, Force alone is not
// enough for a second run — Reimport must be granted explicitly, and the
// refusal must short-circuit before touching any source.
func TestImportAllRefusesReimportWithoutFlag(t *testing.T) {
	fx := newImportFixture(t)
	st := openImportTestStore(t)

	_, err := ImportAll(st, fx.opts)
	wantParseErrOnly(t, err)

	fx.opts.Force = true // Force alone must not be enough post-marker
	report, err := ImportAll(st, fx.opts)
	if err == nil || !strings.Contains(err.Error(), "Reimport") {
		t.Fatalf("second ImportAll with Force but no Reimport: err = %v, want a Reimport-gate error", err)
	}
	if report.Jobs.Found != 0 {
		t.Fatalf("guarded run should short-circuit before reading any source: %+v", report)
	}

	fx.opts.Reimport = true
	report2, err := ImportAll(st, fx.opts)
	wantParseErrOnly(t, err)
	if report2.Jobs.Found != 3 {
		t.Fatalf("Reimport run should complete the full sweep: %+v", report2.Jobs)
	}
}

// TestImportAllDryRunBypassesReimportGate mirrors
// TestImportAllDryRunBypassesForceGate: a dry run never stamps
// imported_at (it never writes), and must not need Reimport even after a
// prior real import set the marker.
// TestImportAllClearsInProgressMarkerOnSuccess covers L7: the
// import_in_progress kv marker is cleared after a successful non-dry-run
// import, so a `kraken serve` startup right afterward isn't blocked
// forever by a marker that should only live for the duration of the sweep
// itself.
func TestImportAllClearsInProgressMarkerOnSuccess(t *testing.T) {
	fx := newImportFixture(t)
	st := openImportTestStore(t)

	_, err := ImportAll(st, fx.opts)
	wantParseErrOnly(t, err)

	v, err := st.GetKV(importInProgressKVKey)
	if err != nil {
		t.Fatalf("GetKV: %v", err)
	}
	if v != "" {
		t.Fatalf("import_in_progress marker = %q after a completed import, want cleared", v)
	}
}

// TestImportAllClearsInProgressMarkerOnError covers L7's error path: the
// marker must clear via defer even when ImportAll returns early on a
// genuine error (not the parse-error-only case wantParseErrOnly expects),
// so a failed import never leaves serve permanently locked out.
func TestImportAllClearsInProgressMarkerOnError(t *testing.T) {
	st := openImportTestStore(t)

	// PlutoDir pointing at a plain FILE (not a directory) makes
	// readJobFiles's os.ReadDir fail with a real (non-not-exist) error,
	// which ImportAll returns immediately — well after the in-progress
	// marker is set, but before any source is actually imported.
	root := t.TempDir()
	notADir := filepath.Join(root, "pluto-is-a-file")
	if err := os.WriteFile(notADir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := ImportOptions{PlutoDir: notADir, ArchiveDir: filepath.Join(root, "archive")}

	_, err := ImportAll(st, opts)
	if err == nil {
		t.Fatal("want an error reading a non-directory PlutoDir, got nil")
	}

	v, gerr := st.GetKV(importInProgressKVKey)
	if gerr != nil {
		t.Fatalf("GetKV: %v", gerr)
	}
	if v != "" {
		t.Fatalf("import_in_progress marker = %q after a failed import, want cleared", v)
	}
}

// TestGuardServeStartRefusesDuringImport and its ordering-reversed sibling
// below cover L7's mutual-exclusion contract from BOTH directions.

// Ordering 1: import starts first (marker set), serve tries to start next —
// GuardServeStart must refuse.
func TestGuardServeStartRefusesDuringImport(t *testing.T) {
	st := useTestStore(t)
	if err := st.SetKV(importInProgressKVKey, "1784826412000"); err != nil {
		t.Fatal(err)
	}
	err := GuardServeStart()
	if err == nil {
		t.Fatal("want GuardServeStart to refuse while an import is in progress, got nil")
	}
	if !strings.Contains(err.Error(), "import") {
		t.Fatalf("error = %q, want it to mention the in-progress import", err.Error())
	}

	// once cleared (as ImportAll's defer would do), serve may start.
	if err := st.SetKV(importInProgressKVKey, ""); err != nil {
		t.Fatal(err)
	}
	if err := GuardServeStart(); err != nil {
		t.Fatalf("GuardServeStart after the marker clears: want nil, got %v", err)
	}
}

// Ordering 2: serve is already up (daemon.json names a live pid) first,
// import tries to start next — runImport's own GuardServeStart call (the
// pre-existing direction, cmd_import.go) must refuse; covered here at the
// jobs-package level directly against daemonGuardErr's contract, since
// GuardServeStart is exactly daemonGuardErr plus the new in-progress check.
func TestGuardServeStartRefusesWhileDaemonAlive(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeDaemonStatusFile(DaemonStatus{PID: os.Getppid()})
	// E14: also cross-checks the pid is actually named "kraken" — the `go
	// test` runner isn't, so stub the check to simulate a genuine conflict.
	old := ProcessIsKraken
	ProcessIsKraken = func(pid int) bool { return true }
	defer func() { ProcessIsKraken = old }()
	if err := GuardServeStart(); err == nil {
		t.Fatal("want GuardServeStart to refuse while a live daemon.json names another pid, got nil")
	}
}

func TestImportAllDryRunBypassesReimportGate(t *testing.T) {
	fx := newImportFixture(t)
	st := openImportTestStore(t)

	_, err := ImportAll(st, fx.opts)
	wantParseErrOnly(t, err)

	fx.opts.DryRun = true
	report, err := ImportAll(st, fx.opts)
	wantParseErrOnly(t, err)
	if report.Jobs.Found != 3 {
		t.Fatalf("dry-run should complete the full sweep without Reimport: %+v", report.Jobs)
	}
}

func TestImportAllDryRunBypassesForceGate(t *testing.T) {
	fx := newImportFixture(t)
	st := openImportTestStore(t)

	_, err := ImportAll(st, fx.opts)
	wantParseErrOnly(t, err)

	// a dry run against an already-populated store must not need Force —
	// it never writes, so there's nothing to protect
	fx.opts.DryRun = true
	report, err := ImportAll(st, fx.opts)
	wantParseErrOnly(t, err)
	if report.Jobs.Found != 3 {
		t.Fatalf("dry-run should complete the full sweep without Force: %+v", report.Jobs)
	}
}

func TestImportAllStampsImportedAtOnNonDryRun(t *testing.T) {
	fx := newImportFixture(t)
	st := openImportTestStore(t)

	if _, err := st.GetKV("imported_at"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("imported_at should be unset before any run: err=%v", err)
	}

	_, err := ImportAll(st, fx.opts)
	wantParseErrOnly(t, err)

	v, err := st.GetKV("imported_at")
	if err != nil {
		t.Fatalf("GetKV imported_at: %v", err)
	}
	if v == "" || v == "0" {
		t.Fatalf("imported_at = %q, want a stamped millis value", v)
	}
}

func TestImportAllDryRunDoesNotStampImportedAt(t *testing.T) {
	fx := newImportFixture(t)
	st := openImportTestStore(t)
	fx.opts.DryRun = true

	_, err := ImportAll(st, fx.opts)
	wantParseErrOnly(t, err)

	if _, err := st.GetKV("imported_at"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("dry-run must not stamp imported_at: err=%v", err)
	}
}

// --- archived-job cell artifacts + orphan cell dirs ---

func TestImportAllArchivedJobCellArtifactsImported(t *testing.T) {
	root := t.TempDir()
	pluto := filepath.Join(root, "pluto")
	archive := filepath.Join(pluto, "archive")
	cells := filepath.Join(root, "cells")

	mustWrite(t, filepath.Join(archive, "helper-catalog.job.md"), []byte(archivedHelperCatalogFixture))
	mustWrite(t, filepath.Join(cells, "helper-catalog", "artifacts", "notes.md"), []byte("real deliverable"))

	opts := ImportOptions{
		PlutoDir: pluto, ArchiveDir: archive, CellsDir: cells,
		UploadsDir: filepath.Join(root, "uploads-missing"),
		LogsDir:    filepath.Join(root, "logs-missing"),
		OrderPath:  filepath.Join(root, "order-missing.json"),
	}
	st := openImportTestStore(t)
	report, err := ImportAll(st, opts)
	if err != nil {
		t.Fatalf("ImportAll: %v", err)
	}
	if report.Artifacts.Found != 1 || report.Artifacts.Imported != 1 {
		t.Fatalf("Artifacts report = %+v", report.Artifacts)
	}
	if len(report.Artifacts.Skipped) != 0 {
		t.Fatalf("unexpected skip/orphan note: %+v", report.Artifacts.Skipped)
	}

	archived, err := st.ListArchivedJobs()
	if err != nil {
		t.Fatalf("ListArchivedJobs: %v", err)
	}
	if len(archived) != 1 {
		t.Fatalf("archived jobs = %+v, want 1", archived)
	}
	artifacts, err := st.ListArtifacts(archived[0].ID)
	if err != nil {
		t.Fatalf("ListArtifacts: %v", err)
	}
	if len(artifacts) != 1 || string(artifacts[0].Bytes) != "real deliverable" {
		t.Fatalf("archived job's cell artifacts not imported: %+v", artifacts)
	}
}

func TestImportAllOrphanCellArtifactsDirReported(t *testing.T) {
	root := t.TempDir()
	pluto := filepath.Join(root, "pluto")
	cells := filepath.Join(root, "cells")
	mustWrite(t, filepath.Join(cells, "ghost-job", "artifacts", "leftover.txt"), []byte("leftover"))

	opts := ImportOptions{
		PlutoDir: pluto, ArchiveDir: filepath.Join(pluto, "archive"), CellsDir: cells,
		UploadsDir: filepath.Join(root, "uploads-missing"),
		LogsDir:    filepath.Join(root, "logs-missing"),
		OrderPath:  filepath.Join(root, "order-missing.json"),
	}
	st := openImportTestStore(t)
	report, err := ImportAll(st, opts)
	if err != nil {
		t.Fatalf("ImportAll: %v", err)
	}
	var found bool
	for _, s := range report.Artifacts.Skipped {
		if strings.Contains(s, "ghost-job") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an orphan note naming ghost-job, got %+v", report.Artifacts.Skipped)
	}
}

// --- legacy keys, duplicate slugs, slugify collisions ---

func TestImportAllLegacyKeysImportCleanly(t *testing.T) {
	root := t.TempDir()
	pluto := filepath.Join(root, "pluto")
	mustWrite(t, filepath.Join(pluto, "legacy.job.md"), []byte(legacyJobFixture))

	opts := ImportOptions{
		PlutoDir: pluto, ArchiveDir: filepath.Join(pluto, "archive"),
		UploadsDir: filepath.Join(root, "uploads-missing"),
		LogsDir:    filepath.Join(root, "logs-missing"),
		CellsDir:   filepath.Join(root, "cells-missing"),
		OrderPath:  filepath.Join(root, "order-missing.json"),
	}
	st := openImportTestStore(t)
	report, err := ImportAll(st, opts)
	if err != nil {
		t.Fatalf("ImportAll: %v", err)
	}
	if report.Jobs.Found != 1 || report.Jobs.Imported != 1 || len(report.Jobs.Skipped) != 0 {
		t.Fatalf("Jobs report = %+v", report.Jobs)
	}
	legacy, err := st.GetJobBySlug("legacy")
	if err != nil {
		t.Fatalf("GetJobBySlug: %v", err)
	}
	if legacy.Type != "info" || !legacy.Ready {
		t.Fatalf("legacy job round trip (type: explore -> info, auto-approve/success discarded): %+v", legacy)
	}
}

func TestImportAllDuplicateActiveSlugFirstWins(t *testing.T) {
	root := t.TempDir()
	pluto := filepath.Join(root, "pluto")
	mustWrite(t, filepath.Join(pluto, "dup-a.job.md"), []byte(dupSlugFixtureA))
	mustWrite(t, filepath.Join(pluto, "dup-b.job.md"), []byte(dupSlugFixtureB))

	opts := ImportOptions{
		PlutoDir: pluto, ArchiveDir: filepath.Join(pluto, "archive"),
		UploadsDir: filepath.Join(root, "uploads-missing"),
		LogsDir:    filepath.Join(root, "logs-missing"),
		CellsDir:   filepath.Join(root, "cells-missing"),
		OrderPath:  filepath.Join(root, "order-missing.json"),
	}
	st := openImportTestStore(t)
	report, err := ImportAll(st, opts)
	if err != nil {
		t.Fatalf("ImportAll: %v", err)
	}
	if report.Jobs.Found != 2 || report.Jobs.Imported != 1 || len(report.Jobs.Skipped) != 1 {
		t.Fatalf("Jobs report = %+v", report.Jobs)
	}
	if !strings.Contains(report.Jobs.Skipped[0], "dup-b.job.md") || !strings.Contains(report.Jobs.Skipped[0], "duplicate slug") {
		t.Fatalf("skip note = %q, want it to name dup-b.job.md as a duplicate slug", report.Jobs.Skipped[0])
	}
	dup, err := st.GetJobBySlug("dup")
	if err != nil {
		t.Fatalf("GetJobBySlug: %v", err)
	}
	if !dup.Ready {
		t.Fatalf("expected the first file (dup-a, ready: true) to win, got %+v", dup)
	}
}

func TestImportAllSlugifyCollisionReported(t *testing.T) {
	root := t.TempDir()
	pluto := filepath.Join(root, "pluto")
	uploads := filepath.Join(root, "uploads")
	mustWrite(t, filepath.Join(pluto, "slugify-a.job.md"), []byte(slugifyCollisionFixtureA))
	mustWrite(t, filepath.Join(pluto, "slugify-b.job.md"), []byte(slugifyCollisionFixtureB))
	// an upload dir under the shared slugified name: must attribute to
	// whichever slug won the claim (the first file), never both/neither
	mustWrite(t, filepath.Join(uploads, "foo-bar", "img-001.png"), []byte("bytes"))

	opts := ImportOptions{
		PlutoDir: pluto, ArchiveDir: filepath.Join(pluto, "archive"),
		UploadsDir: uploads,
		LogsDir:    filepath.Join(root, "logs-missing"),
		CellsDir:   filepath.Join(root, "cells-missing"),
		OrderPath:  filepath.Join(root, "order-missing.json"),
	}
	st := openImportTestStore(t)
	report, err := ImportAll(st, opts)
	if err != nil {
		t.Fatalf("ImportAll: %v", err)
	}
	// both distinct slugs still import as their own rows
	if report.Jobs.Found != 2 || report.Jobs.Imported != 2 {
		t.Fatalf("Jobs report = %+v", report.Jobs)
	}
	var flagged bool
	for _, s := range report.Jobs.Skipped {
		if strings.Contains(s, "slugifies to") {
			flagged = true
		}
	}
	if !flagged {
		t.Fatalf("expected a slugify-collision note, got %+v", report.Jobs.Skipped)
	}

	fooBar, err := st.GetJobBySlug("Foo Bar")
	if err != nil {
		t.Fatalf("GetJobBySlug(Foo Bar): %v", err)
	}
	if _, err := st.GetJobBySlug("foo-bar"); err != nil {
		t.Fatalf("GetJobBySlug(foo-bar): %v", err)
	}
	uploadsList, err := st.ListUploads(fooBar.ID)
	if err != nil {
		t.Fatalf("ListUploads: %v", err)
	}
	if len(uploadsList) != 1 {
		t.Fatalf("expected the upload dir attributed to the first (winning) slug only, got %+v", uploadsList)
	}
}
