package api

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"kraken/core"
	"kraken/jobs"
	"kraken/util"
)

// writeTestJob writes a minimal parseable job file under a fresh
// KRAKEN_PLUTO_DIR and returns its slug — the shared fixture for the
// webXxx move-validation tests below, none of which need a live cell to
// reach their validation error.
func writeTestJob(t *testing.T, slug, extraFrontmatter string) {
	t.Helper()
	src := "---\njob: " + slug + "\ntype: build\nrepo: monolith\nready: true\n" +
		extraFrontmatter + "---\n\n# Spec\n"
	if err := os.WriteFile(filepath.Join(jobs.PlutoDir(), slug+jobs.JobSuffix), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

// mustJSON marshals v for use as an httptest request body.
func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestArtifactPathSubdirsAndTraversal(t *testing.T) {
	// build a temp cell artifacts tree: file at root + nested subdir file
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "artifacts", "icon-variations"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeF := func(rel string) {
		if err := os.WriteFile(filepath.Join(dir, "artifacts", rel), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeF("top.png")
	writeF(filepath.Join("icon-variations", "icon-v1.png"))
	writeF(filepath.Join("icon-variations", ".hidden"))

	// jobs.CellArtifacts derives from cellDir(j); fake it by pointing the job's
	// cell resolution at our temp dir via a stub is overkill — instead test
	// the pure guard against a known root.
	root := filepath.Join(dir, "artifacts")
	check := func(name string) bool {
		for _, seg := range strings.Split(strings.TrimPrefix(name, "/"), "/") {
			if seg == "" || seg == "." || seg == ".." || strings.HasPrefix(seg, ".") {
				return false
			}
		}
		p := filepath.Join(root, name)
		rel, err := filepath.Rel(root, p)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return false
		}
		fi, err := os.Stat(p)
		return err == nil && !fi.IsDir()
	}
	if !check("top.png") {
		t.Error("top-level file should resolve")
	}
	if !check("icon-variations/icon-v1.png") {
		t.Error("nested file should resolve")
	}
	if check("../../etc/passwd") {
		t.Error("traversal must be refused")
	}
	if check("icon-variations/../../../secret") {
		t.Error("mid-path traversal must be refused")
	}
	if check("icon-variations/.hidden") {
		t.Error("hidden file must be refused")
	}
	if check("icon-variations") {
		t.Error("a directory is not a file artifact")
	}
}

// --- webJob JSON shape: escalations are gone, new fields are omitempty ---

func TestWebJobJSONShapeEmpty(t *testing.T) {
	// a bare webJob (the common case: a draft/queued/done job with no live
	// cell) must not emit any of the live-cell-only fields, and must never
	// emit "escalations" — that field no longer exists on the type at all.
	data, err := json.Marshal(webJob{Slug: "x", Type: "build", Repo: "monolith", State: "draft"})
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, absent := range []string{"escalations", "detail", "question", "wakeup", "roster", "zeus_status", "health", "cell", "err", "outcome", "success", "prereqs", "artifacts"} {
		if strings.Contains(s, `"`+absent+`"`) {
			t.Errorf("empty webJob JSON unexpectedly has %q: %s", absent, s)
		}
	}
	for _, want := range []string{`"slug":"x"`, `"type":"build"`, `"state":"draft"`, `"live":false`, `"mtime":0`} {
		if !strings.Contains(s, want) {
			t.Errorf("webJob JSON missing %q: %s", want, s)
		}
	}
}

func TestWebJobJSONShapePopulated(t *testing.T) {
	wj := webJob{
		Slug: "x", Type: "build", Repo: "monolith", State: "blocked", Live: true,
		Detail: "zeus said something",
		Question: &webQuestion{
			ToolUseID: "tu_1",
			Questions: []webQuestionItem{{
				Question: "which approach?", Header: "approach", MultiSelect: false,
				Options: []webQuestionOption{{Label: "A", Description: "do A"}, {Label: "B", Description: "do B"}},
			}},
		},
		Wakeup: &webWakeup{Reason: "poll PR checks", FiresAt: 1234567890},
		Roster: []webAgent{{Name: "hephaestus-x", Model: "sonnet", Role: "hephaestus", Status: "running", Working: true}},
	}
	data, err := json.Marshal(wj)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, want := range []string{
		`"detail":"zeus said something"`,
		`"question":{"tool_use_id":"tu_1"`,
		`"multi_select":false`,
		`"options":[{"label":"A","description":"do A"}`,
		`"wakeup":{"reason":"poll PR checks","fires_at":1234567890}`,
		`"roster":[{"name":"hephaestus-x","model":"sonnet","role":"hephaestus","status":"running","working":true}]`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("populated webJob JSON missing %q: %s", want, s)
		}
	}
	if strings.Contains(s, "escalations") {
		t.Errorf("escalations leaked into JSON: %s", s)
	}
}

func TestWebQuestionFromNil(t *testing.T) {
	if got := webQuestionFrom(nil); got != nil {
		t.Fatalf("webQuestionFrom(nil) = %+v, want nil", got)
	}
}

func TestWebQuestionFromMirrorsFields(t *testing.T) {
	pq := &jobs.PendingQuestion{
		ToolUseID: "tu_2",
		Questions: []jobs.QuestionItem{{
			Question: "q", Header: "h", MultiSelect: true,
			Options: []jobs.QuestionOption{{Label: "l1", Description: "d1"}},
		}},
	}
	got := webQuestionFrom(pq)
	if got == nil || got.ToolUseID != "tu_2" || len(got.Questions) != 1 {
		t.Fatalf("webQuestionFrom mismatch: %+v", got)
	}
	q := got.Questions[0]
	if q.Question != "q" || q.Header != "h" || !q.MultiSelect || len(q.Options) != 1 {
		t.Fatalf("question item mismatch: %+v", q)
	}
	if q.Options[0].Label != "l1" || q.Options[0].Description != "d1" {
		t.Fatalf("option mismatch: %+v", q.Options[0])
	}
}

func TestWebWakeupFromNil(t *testing.T) {
	if got := webWakeupFrom(nil); got != nil {
		t.Fatalf("webWakeupFrom(nil) = %+v, want nil", got)
	}
}

func TestWebWakeupFromMirrorsFields(t *testing.T) {
	w := &core.Wakeup{Reason: "poll PR checks", DelaySeconds: 300, FiresAt: 1234567890}
	got := webWakeupFrom(w)
	if got == nil || got.Reason != "poll PR checks" || got.FiresAt != 1234567890 {
		t.Fatalf("webWakeupFrom mismatch: %+v", got)
	}
}

// --- lastSentMessage: the most recent me→zeus mid-run log entry ---

func TestLastSentMessageEmptyBody(t *testing.T) {
	if got := lastSentMessage(""); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestLastSentMessageNoLogSection(t *testing.T) {
	body := "# Spec\ndo the thing\n"
	if got := lastSentMessage(body); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestLastSentMessageOneEntry(t *testing.T) {
	body := "# Spec\ndo the thing\n\n## Mid-run log\n<!-- appended by kraken; do not hand-edit -->\n" +
		"- [2026-07-01 10:00 me→zeus] pivot to plan B\n"
	if got := lastSentMessage(body); got != "pivot to plan B" {
		t.Errorf("got %q, want %q", got, "pivot to plan B")
	}
}

func TestLastSentMessageMultipleEntriesInterleaved(t *testing.T) {
	body := "# Spec\ndo the thing\n\n## Mid-run log\n" +
		"<!-- appended by kraken; do not hand-edit -->\n" +
		"- [2026-07-01 10:00 me→zeus] first message\n" +
		"- [2026-07-01 10:05 zeus→me] status update, not mine\n" +
		"- [2026-07-01 10:10 me→zeus] second message\n" +
		"- [2026-07-01 10:15 zeus→me] another status update\n"
	if got := lastSentMessage(body); got != "second message" {
		t.Errorf("got %q, want %q", got, "second message")
	}
}

func TestLastSentMessageSkipsAnsweredQuestionEntries(t *testing.T) {
	body := "# Spec\ndo the thing\n\n## Mid-run log\n" +
		"<!-- appended by kraken; do not hand-edit -->\n" +
		"- [2026-07-01 10:00 me→zeus] real message\n" +
		"- [2026-07-01 10:05 zeus→me] status update\n" +
		"- [2026-07-01 10:10 me→zeus] answered question: option 2\n"
	if got := lastSentMessage(body); got != "real message" {
		t.Errorf("got %q, want %q", got, "real message")
	}
}

func TestTruncateTextSanitizesControlCharsPreservesNewlines(t *testing.T) {
	in := "line one\x1b[31m colored\x7f\nline two"
	want := "line one [31m colored \nline two"
	if got := truncateText(in, 100); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// --- mtime wire contract: JSON number out, number-or-legacy-string in ---

func TestMTimeTokenUnmarshalJSON(t *testing.T) {
	cases := []struct {
		name    string
		json    string
		want    mtimeToken
		wantErr bool
	}{
		{"number", `1690000000000`, 1690000000000, false},
		{"numeric string (legacy)", `"1690000000000"`, 1690000000000, false},
		{"empty string skips lock", `""`, 0, false},
		{"zero string skips lock", `"0"`, 0, false},
		{"zero number skips lock", `0`, 0, false},
		{"garbage string", `"banana"`, 0, true},
		{"non-integer number", `1.5`, 0, true},
		{"null skips lock", `null`, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var tok mtimeToken
			err := json.Unmarshal([]byte(c.json), &tok)
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected error, got tok=%d", tok)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tok != c.want {
				t.Errorf("got %d, want %d", tok, c.want)
			}
		})
	}
}

// fakeFileInfo is a minimal os.FileInfo for exercising binaryStale without
// touching the filesystem.
type fakeFileInfo struct {
	modTime time.Time
	size    int64
}

func (f fakeFileInfo) Name() string       { return "kraken" }
func (f fakeFileInfo) Size() int64        { return f.size }
func (f fakeFileInfo) Mode() fs.FileMode  { return 0o755 }
func (f fakeFileInfo) ModTime() time.Time { return f.modTime }
func (f fakeFileInfo) IsDir() bool        { return false }
func (f fakeFileInfo) Sys() any           { return nil }

func TestBinaryStale(t *testing.T) {
	t0 := time.Unix(1700000000, 0)
	t1 := time.Unix(1700000100, 0)
	same := fakeFileInfo{modTime: t0, size: 1024}
	sameCopy := fakeFileInfo{modTime: t0, size: 1024}
	newerMTime := fakeFileInfo{modTime: t1, size: 1024}
	biggerSize := fakeFileInfo{modTime: t0, size: 2048}

	cases := []struct {
		name       string
		start, now os.FileInfo
		want       bool
	}{
		{"both nil", nil, nil, false},
		{"nil start", nil, same, false},
		{"nil now", same, nil, false},
		{"identical", same, sameCopy, false},
		{"mtime differs", same, newerMTime, true},
		{"size differs", same, biggerSize, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := util.BinaryStale(c.start, c.now); got != c.want {
				t.Errorf("util.BinaryStale(%v, %v) = %v, want %v", c.start, c.now, got, c.want)
			}
		})
	}
}

// TestIsSelfStaleDetectsChange exercises isSelfStale end to end (rather than
// just its pure binaryStale helper): TestHealthHandlerContractAndStale can
// only ever observe stale=false since util.SelfBinaryStart is nil in tests, so
// this drives the package state directly to cover the true branch too.
func TestIsSelfStaleDetectsChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kraken")
	if err := os.WriteFile(path, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	origPath, origStart := util.SelfBinaryPath, util.SelfBinaryStart
	util.SelfBinaryPath, util.SelfBinaryStart = path, fi
	t.Cleanup(func() { util.SelfBinaryPath, util.SelfBinaryStart = origPath, origStart })

	if util.IsSelfStale() {
		t.Fatal("util.IsSelfStale() = true before any change, want false")
	}

	if err := os.WriteFile(path, []byte("v1 plus more bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}

	if !util.IsSelfStale() {
		t.Fatal("util.IsSelfStale() = false after binary changed, want true")
	}
}

// TestHealthHandlerContractAndStale asserts /api/health carries the wire
// contract version and a stale flag alongside the existing ok/host fields —
// no fixtures needed, healthHandler doesn't touch the pluto dir.
func TestHealthHandlerContractAndStale(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	rec := httptest.NewRecorder()
	healthHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("healthHandler: got status %d, body %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("healthHandler: bad response json: %v", err)
	}
	if ok, _ := resp["ok"].(bool); !ok {
		t.Errorf("healthHandler: ok = %#v, want true", resp["ok"])
	}
	if c, ok := resp["contract"].(float64); !ok || int(c) != apiContract {
		t.Errorf("healthHandler: contract = %#v, want %d", resp["contract"], apiContract)
	}
	if _, ok := resp["stale"].(bool); !ok {
		t.Errorf("healthHandler: stale = %#v (%T), want a bool", resp["stale"], resp["stale"])
	}
}

// --- boardCache: TTL memoization + single-flight over buildWebBoard ---

// countingCache builds a boardCache whose build func counts its own calls
// and whose clock is a plain closure variable the test can advance — no
// goroutines, no package-level state, so it can't leak into other tests.
func countingCache(ttl time.Duration, clock *time.Time) (*boardCache, *int32) {
	var builds int32
	c := &boardCache{
		ttl: ttl,
		now: func() time.Time { return *clock },
		build: func() webBoard {
			atomic.AddInt32(&builds, 1)
			return webBoard{}
		},
	}
	return c, &builds
}

func TestBoardCacheTTLHit(t *testing.T) {
	clock := time.Now()
	c, builds := countingCache(2*time.Second, &clock)
	c.get()
	c.get()
	if got := atomic.LoadInt32(builds); got != 1 {
		t.Fatalf("builds = %d, want 1 — a second get() inside the ttl must hit the cache", got)
	}
}

func TestBoardCacheTTLExpiry(t *testing.T) {
	clock := time.Now()
	c, builds := countingCache(2*time.Second, &clock)
	c.get()
	clock = clock.Add(3 * time.Second)
	c.get()
	if got := atomic.LoadInt32(builds); got != 2 {
		t.Fatalf("builds = %d, want 2 — a get() past the ttl must rebuild", got)
	}
}

func TestBoardCacheInvalidate(t *testing.T) {
	clock := time.Now()
	c, builds := countingCache(2*time.Second, &clock)
	c.get()
	c.invalidate()
	c.get()
	if got := atomic.LoadInt32(builds); got != 2 {
		t.Fatalf("builds = %d, want 2 — invalidate() must force the next get() to rebuild", got)
	}
}

// TestBoardCacheSingleFlight exercises the reason get() holds its lock across
// build(): a burst of concurrent callers hitting a cold cache (several web
// tabs + the iOS app polling at once) must collapse into exactly one
// discovery, with the late arrivals blocking on the mutex and then reusing
// the fresh entry rather than each triggering their own build.
func TestBoardCacheSingleFlight(t *testing.T) {
	clock := time.Now()
	var builds int32
	c := &boardCache{
		ttl: 2 * time.Second,
		now: func() time.Time { return clock },
		build: func() webBoard {
			atomic.AddInt32(&builds, 1)
			time.Sleep(20 * time.Millisecond) // simulate a slow tmux/ps/lsof discovery
			return webBoard{}
		},
	}
	var wg sync.WaitGroup
	const n = 10
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.get()
		}()
	}
	wg.Wait()
	if got := atomic.LoadInt32(&builds); got != 1 {
		t.Fatalf("builds = %d, want 1 — %d concurrent misses on a cold cache must single-flight", got, n)
	}
}

// writeArchiveFixture writes a minimal parseable job file into dir.
func writeArchiveFixture(t *testing.T, dir, name, frontmatter string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	content := "---\n" + frontmatter + "---\n\n# Spec\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestArchivedArtifactServing(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	if err := os.MkdirAll(jobs.ArchiveDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	writeArchiveFixture(t, jobs.ArchiveDir(), "foo-2.job.md", "job: foo\nrepo: x\n")
	if err := os.WriteFile(filepath.Join(jobs.ArchiveDir(), "foo-2.report.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := jobs.ArchivedJobByName(jobs.LoadArchivedJobs(), "foo-2")
	arts := archivedWebArtifacts(a)
	if len(arts) != 1 || arts[0].Name != "report.md" || arts[0].URL != "/api/artifact/foo-2/archive/report.md" {
		t.Fatalf("unexpected artifacts: %+v", arts)
	}
	if p, ok := ArtifactPath(a, "archive", "report.md"); !ok || p != filepath.Join(jobs.ArchiveDir(), "foo-2.report.md") {
		t.Fatalf("artifactPath archive: %q %v", p, ok)
	}
	// traversal and job files are refused
	if _, ok := ArtifactPath(a, "archive", "../foo-2.report.md"); ok {
		t.Fatal("traversal allowed")
	}
	if _, ok := ArtifactPath(a, "archive", "job.md"); ok {
		// foo-2.job.md must not be reachable as artifact "job.md"
		t.Fatal("job file served as artifact")
	}
	// an active job (no ArchivedName) never resolves in the archive home
	if _, ok := ArtifactPath(&jobs.Job{Slug: "foo"}, "archive", "report.md"); ok {
		t.Fatal("active job resolved in archive home")
	}
}
