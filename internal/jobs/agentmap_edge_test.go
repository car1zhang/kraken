package jobs

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"kraken/internal/store"
)

// agentmap_edge_test.go — athena-edge's blind suite for the jobs half of the
// agent map (50-contract.md G3, G4's jobs verbs, G7): empty and missing
// transcripts, crew entries that must be omitted, n clamping, the same-job
// and zero-id refusals, an absent store, and every refusal SetMapPositions
// lists.

const agentMapEdgeBaseMS int64 = 1_793_000_000_000

func agentMapEdgeTS(ms int64) string {
	return time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

func agentMapEdgeCall(ms int64, id, name, input string) string {
	return `{"type":"assistant","timestamp":"` + agentMapEdgeTS(ms) + `","uuid":"a-` + id + `","message":{"role":"assistant","content":[{"type":"tool_use","id":"` + id + `","name":"` + name + `","input":` + input + `}]}}`
}

func agentMapEdgeUserText(ms int64, uuid, text string) string {
	b, _ := json.Marshal(text)
	return `{"type":"user","timestamp":"` + agentMapEdgeTS(ms) + `","uuid":"` + uuid + `","message":{"role":"user","content":` + string(b) + `}}`
}

func agentMapEdgeWriteAt(t *testing.T, p string, lines ...string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	return p
}

func agentMapEdgeWrite(t *testing.T, lines ...string) string {
	t.Helper()
	return agentMapEdgeWriteAt(t, filepath.Join(t.TempDir(), "s.jsonl"), lines...)
}

// agentMapEdgeCalls returns n Grep calls one second apart, ids c0..c(n-1).
func agentMapEdgeCalls(n int) []string {
	out := make([]string, n)
	for i := 0; i < n; i++ {
		out[i] = agentMapEdgeCall(agentMapEdgeBaseMS+int64(i)*1000, fmt.Sprintf("c%d", i), "Grep", `{}`)
	}
	return out
}

// --- BuildJobActivity -------------------------------------------------------

// nil when the lead transcript yields no events: no path, a missing file, an
// empty file, or a file of nothing but noise and garbage.
func TestAgentMapEdgeBuildJobActivityNilWithoutLeadEvents(t *testing.T) {
	noise := agentMapEdgeWrite(t,
		`not json`,
		agentMapEdgeUserText(agentMapEdgeBaseMS, "u1", "<system-reminder>x</system-reminder>"),
		agentMapEdgeUserText(agentMapEdgeBaseMS+1, "u2", "[Request interrupted by user]"),
	)
	empty := filepath.Join(t.TempDir(), "empty.jsonl")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	crew := agentMapEdgeWrite(t, agentMapEdgeCall(agentMapEdgeBaseMS, "k1", "Edit", `{"file_path":"/a/b.go"}`))
	for name, lead := range map[string]string{
		"no path": "",
		"missing": filepath.Join(t.TempDir(), "gone.jsonl"),
		"empty":   empty,
		"noise":   noise,
	} {
		// a crew with events does not rescue a lead with none
		if got := BuildJobActivity(lead, map[string]string{"heph-1": crew}, []string{"heph-1"}); got != nil {
			t.Errorf("%s: BuildJobActivity = %+v, want nil", name, got)
		}
	}
}

// A lead that has only heard messages: Act think, Label empty, At the newest
// event's T, Events still carries the hears.
func TestAgentMapEdgeBuildJobActivityOnlyHears(t *testing.T) {
	lead := agentMapEdgeWrite(t,
		agentMapEdgeUserText(agentMapEdgeBaseMS, "u1", "go build it"),
		agentMapEdgeUserText(agentMapEdgeBaseMS+5000, "u2", "[kraken notice] something"),
	)
	got := BuildJobActivity(lead, nil, nil)
	if got == nil {
		t.Fatal("BuildJobActivity = nil, want an activity of hears")
	}
	if got.Act != "think" || got.Label != "" {
		t.Errorf("Act/Label = %q/%q, want think and empty", got.Act, got.Label)
	}
	if got.At != agentMapEdgeBaseMS+5000 {
		t.Errorf("At = %d, want %d", got.At, agentMapEdgeBaseMS+5000)
	}
	if len(got.Events) != 2 {
		t.Errorf("Events = %+v, want both hears", got.Events)
	}
	if len(got.Crew) != 0 {
		t.Errorf("Crew = %+v, want none", got.Crew)
	}
}

// A hear newer than the newest call: Act and Label come from the call, At from
// the hear.
func TestAgentMapEdgeBuildJobActivityHearNewerThanCall(t *testing.T) {
	lead := agentMapEdgeWrite(t,
		agentMapEdgeCall(agentMapEdgeBaseMS, "t1", "Read", `{"file_path":"/x/CLAUDE.md"}`),
		agentMapEdgeUserText(agentMapEdgeBaseMS+9000, "u1", "a prompt"),
	)
	got := BuildJobActivity(lead, nil, nil)
	if got == nil {
		t.Fatal("nil activity")
	}
	if got.Act != "read" || got.Label != "Read CLAUDE.md" || got.At != agentMapEdgeBaseMS+9000 {
		t.Fatalf("activity = %+v, want act read, label Read CLAUDE.md, At of the hear", got)
	}
}

// Events is capped at ActivityBoardEvents, the NEWEST ones, oldest first.
func TestAgentMapEdgeBuildJobActivityCapsEvents(t *testing.T) {
	if ActivityBoardEvents != 24 {
		t.Fatalf("ActivityBoardEvents = %d, want 24", ActivityBoardEvents)
	}
	lead := agentMapEdgeWrite(t, agentMapEdgeCalls(40)...)
	got := BuildJobActivity(lead, nil, nil)
	if got == nil || len(got.Events) != 24 {
		t.Fatalf("activity = %+v, want 24 events", got)
	}
	if got.Events[0].ID != "c16" || got.Events[23].ID != "c39" {
		t.Fatalf("events span %s..%s, want c16..c39", got.Events[0].ID, got.Events[23].ID)
	}
}

// Crew omits a name with no path, a missing file, a transcript with no call
// (only hears), and every name not in crewOrder; the rest follow crewOrder.
func TestAgentMapEdgeBuildJobActivityCrewOmissions(t *testing.T) {
	lead := agentMapEdgeWrite(t, agentMapEdgeCall(agentMapEdgeBaseMS, "l1", "Grep", `{}`))
	hearsOnly := agentMapEdgeWrite(t, agentMapEdgeUserText(agentMapEdgeBaseMS, "u1", `<teammate-message teammate_id="zeus">do it</teammate-message>`))
	working := agentMapEdgeWrite(t,
		agentMapEdgeCall(agentMapEdgeBaseMS, "w1", "Grep", `{}`),
		agentMapEdgeCall(agentMapEdgeBaseMS+3000, "w2", "Edit", `{"file_path":"/a/board.go"}`),
		agentMapEdgeUserText(agentMapEdgeBaseMS+9000, "w3", "a later message"),
	)
	other := agentMapEdgeWrite(t, agentMapEdgeCall(agentMapEdgeBaseMS+100, "o1", "Read", `{"file_path":"/a/x.go"}`))
	paths := map[string]string{
		"hears-only": hearsOnly,
		"missing":    filepath.Join(t.TempDir(), "gone.jsonl"),
		"working":    working,
		"other":      other,
		"unlisted":   working,
		"blank":      "",
	}
	order := []string{"no-path", "other", "hears-only", "missing", "blank", "working"}
	got := BuildJobActivity(lead, paths, order)
	if got == nil {
		t.Fatal("nil activity")
	}
	want := []CrewActivity{
		{Name: "other", Act: "read", Label: "Read x.go", At: agentMapEdgeBaseMS + 100},
		{Name: "working", Act: "edit", Label: "Edit board.go", At: agentMapEdgeBaseMS + 3000},
	}
	if !reflect.DeepEqual(got.Crew, want) {
		t.Fatalf("Crew = %+v, want %+v", got.Crew, want)
	}
	// with no crewOrder, no crew at all
	if got := BuildJobActivity(lead, paths, nil); got == nil || len(got.Crew) != 0 {
		t.Fatalf("no crewOrder: %+v, want an activity with no crew", got)
	}
}

// --- JobActivityFor / JobEvents --------------------------------------------

// agentMapEdgeLeadJob makes a sandboxed HOME and repos dir and returns a job
// with a stamped session plus the path its lead transcript lives at.
func agentMapEdgeLeadJob(t *testing.T) (*Job, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	repos := filepath.Join(home, "Developer")
	if err := os.MkdirAll(filepath.Join(repos, "kraken", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KRAKEN_REPOS_DIR", repos)
	j := &Job{ID: 4242, Slug: "edge-lead", Repo: "kraken", State: "working", Session: "edge-session-0001"}
	repo, err := ResolveRepo("kraken")
	if err != nil {
		t.Fatalf("ResolveRepo: %v", err)
	}
	dirs, err := JobProjectDirs(j, repo)
	if err != nil || len(dirs) == 0 {
		t.Fatalf("JobProjectDirs = %v, %v", dirs, err)
	}
	return j, filepath.Join(dirs[0], j.Session+".jsonl")
}

// nil for a nil job, a job with no stamped session, and a missing transcript.
func TestAgentMapEdgeJobActivityForNils(t *testing.T) {
	if got := JobActivityFor(nil, []string{"heph-1"}); got != nil {
		t.Errorf("nil job: %+v, want nil", got)
	}
	j, _ := agentMapEdgeLeadJob(t)
	if got := JobActivityFor(j, nil); got != nil {
		t.Errorf("missing transcript: %+v, want nil", got)
	}
	noSession := *j
	noSession.Session = ""
	if got := JobActivityFor(&noSession, nil); got != nil {
		t.Errorf("no session: %+v, want nil", got)
	}
}

// A job with no stamped session still answers nil even when a transcript of
// some other session sits in its project dir.
func TestAgentMapEdgeJobActivityForIgnoresUnstampedSessions(t *testing.T) {
	j, lead := agentMapEdgeLeadJob(t)
	agentMapEdgeWriteAt(t, filepath.Join(filepath.Dir(lead), "some-other-session.jsonl"), agentMapEdgeCalls(3)...)
	if got := JobActivityFor(j, nil); got != nil {
		t.Fatalf("activity = %+v, want nil: the stamped session has no transcript", got)
	}
	j.Session = ""
	if got := JobActivityFor(j, nil); got != nil {
		t.Fatalf("activity = %+v, want nil for a job with no stamped session", got)
	}
}

// A teammate name with no transcript of its own is omitted from Crew.
func TestAgentMapEdgeJobActivityForUnknownTeammate(t *testing.T) {
	j, lead := agentMapEdgeLeadJob(t)
	agentMapEdgeWriteAt(t, lead, agentMapEdgeCalls(2)...)
	got := JobActivityFor(j, []string{"heph-nobody"})
	if got == nil {
		t.Fatal("activity = nil, want the lead's")
	}
	if len(got.Crew) != 0 {
		t.Fatalf("Crew = %+v, want none", got.Crew)
	}
}

// n is clamped to 1..200.
func TestAgentMapEdgeJobEventsClampsN(t *testing.T) {
	j, lead := agentMapEdgeLeadJob(t)
	agentMapEdgeWriteAt(t, lead, agentMapEdgeCalls(250)...)
	for _, tc := range []struct{ n, want int }{{0, 1}, {-7, 1}, {1, 1}, {200, 200}, {201, 200}, {100000, 200}} {
		evs, err := JobEvents(j, tc.n)
		if err != nil {
			t.Fatalf("JobEvents(n=%d): %v", tc.n, err)
		}
		if len(evs) != tc.want {
			t.Errorf("JobEvents(n=%d) = %d events, want %d", tc.n, len(evs), tc.want)
		}
		if len(evs) > 0 && evs[len(evs)-1].ID != "c249" {
			t.Errorf("JobEvents(n=%d) ends at %s, want the newest c249", tc.n, evs[len(evs)-1].ID)
		}
	}
}

// Nothing to read is a non-nil empty slice.
func TestAgentMapEdgeJobEventsNothingToRead(t *testing.T) {
	j, _ := agentMapEdgeLeadJob(t)
	evs, _ := JobEvents(j, 60)
	if evs == nil || len(evs) != 0 {
		t.Errorf("missing transcript: %#v, want a non-nil empty slice", evs)
	}
	noSession := *j
	noSession.Session = ""
	evs, _ = JobEvents(&noSession, 60)
	if evs == nil || len(evs) != 0 {
		t.Errorf("no session: %#v, want a non-nil empty slice", evs)
	}
}

// --- RecordCellMessage / JobForZeusName --------------------------------------

func agentMapEdgeLoad(t *testing.T, slug string) *Job {
	t.Helper()
	j := JobBySlug(LoadJobs(), slug)
	if j == nil {
		t.Fatalf("job %s not loaded", slug)
	}
	return j
}

// No store wired: an error, and nothing panics.
func TestAgentMapEdgeRecordCellMessageNoStore(t *testing.T) {
	UseStore(nil)
	a := &Job{ID: 1, Slug: "a"}
	b := &Job{ID: 2, Slug: "b"}
	if err := RecordCellMessage(a, b); err == nil {
		t.Fatal("RecordCellMessage with no store: nil error, want one")
	}
}

// Either job with id 0, or both the same job: an error and no row.
func TestAgentMapEdgeRecordCellMessageRefusals(t *testing.T) {
	st := useTestStore(t)
	mustCreateJob(t, st, store.Job{Slug: "alpha", Repo: "kraken", State: "working"})
	mustCreateJob(t, st, store.Job{Slug: "beta", Repo: "kraken", State: "working"})
	a, b := agentMapEdgeLoad(t, "alpha"), agentMapEdgeLoad(t, "beta")
	zero := &Job{Slug: "ghost"}
	aCopy := *a
	for name, pair := range map[string][2]*Job{
		"from id 0":     {zero, b},
		"to id 0":       {a, zero},
		"both id 0":     {zero, &Job{Slug: "ghost-2"}},
		"same job":      {a, a},
		"same id, copy": {a, &aCopy},
	} {
		if err := RecordCellMessage(pair[0], pair[1]); err == nil {
			t.Errorf("%s: nil error, want a refusal", name)
		}
	}
	rows, err := st.ListJobMessages()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("rows = %+v, want none after refusals only", rows)
	}
	// and the refusals did not poison a real send
	if err := RecordCellMessage(a, b); err != nil {
		t.Fatalf("a->b: %v", err)
	}
	if rows, _ := st.ListJobMessages(); len(rows) != 1 || rows[0].FromID != a.ID || rows[0].ToID != b.ID || rows[0].N != 1 {
		t.Fatalf("rows = %+v, want one a->b row with N 1", rows)
	}
}

// nil for an empty name, a non-zeus name, a near miss, and an ARCHIVED job's
// zeus name; no store wired answers nil too.
func TestAgentMapEdgeJobForZeusNameMisses(t *testing.T) {
	UseStore(nil)
	if got := JobForZeusName("zeus-anything"); got != nil {
		t.Errorf("no store: %+v, want nil", got)
	}
	st := useTestStore(t)
	mustCreateJob(t, st, store.Job{Slug: "alpha", Repo: "kraken", State: "working"})
	gone := mustCreateJob(t, st, store.Job{Slug: "gone", Repo: "kraken", State: "done"})
	if _, err := st.ArchiveJob(gone.ID); err != nil {
		t.Fatalf("archive: %v", err)
	}
	for _, name := range []string{"", "alpha", "zeus-", "zeus-alph", "zeus-alpha-2", "thread-alpha-1", " zeus-alpha", "zeus-gone"} {
		if got := JobForZeusName(name); got != nil {
			t.Errorf("JobForZeusName(%q) = %s, want nil", name, got.Slug)
		}
	}
	if got := JobForZeusName("zeus-alpha"); got == nil || got.Slug != "alpha" {
		t.Errorf("JobForZeusName(zeus-alpha) = %+v, want alpha", got)
	}
}

// --- map positions -----------------------------------------------------------

// No store: an empty non-nil map, and every write refused.
func TestAgentMapEdgeMapPositionsNoStore(t *testing.T) {
	UseStore(nil)
	got := MapPositions()
	if got == nil || len(got) != 0 {
		t.Errorf("MapPositions with no store = %#v, want a non-nil empty map", got)
	}
	if err := SetMapPositions(map[string][2]float64{"a": {1, 2}}); err == nil {
		t.Error("SetMapPositions with no store: nil error, want one")
	}
}

// Nothing saved yet: an empty non-nil map.
func TestAgentMapEdgeMapPositionsEmptyStore(t *testing.T) {
	useTestStore(t)
	got := MapPositions()
	if got == nil || len(got) != 0 {
		t.Fatalf("MapPositions = %#v, want a non-nil empty map", got)
	}
}

// Every refusal leaves the saved set exactly as it was — including a batch
// that mixes valid keys with one bad one.
func TestAgentMapEdgeSetMapPositionsRefusals(t *testing.T) {
	useTestStore(t)
	base := map[string][2]float64{"kept": {10, 20}}
	if err := SetMapPositions(base); err != nil {
		t.Fatalf("seed: %v", err)
	}
	long := strings.Repeat("k", 201)
	wide := strings.Repeat("é", 101) // 101 runes, 202 bytes
	for name, changes := range map[string]map[string][2]float64{
		"nil":            nil,
		"empty":          {},
		"empty key":      {"": {1, 2}},
		"201-byte key":   {long: {1, 2}},
		"202-byte runes": {wide: {1, 2}},
		"NaN x":          {"n": {math.NaN(), 1}},
		"NaN y":          {"n": {1, math.NaN()}},
		"+Inf":           {"n": {math.Inf(1), 1}},
		"-Inf":           {"n": {1, math.Inf(-1)}},
		"mixed batch":    {"good-1": {1, 1}, "good-2": {2, 2}, "bad": {math.NaN(), 0}},
		"mixed key":      {"good-3": {1, 1}, "": {2, 2}},
		// amendment 6: |coordinate| > 10,000,000 is refused like NaN
		"x past +1e7":    {"n": {10_000_000.5, 0}},
		"y past -1e7":    {"n": {0, -10_000_000.5}},
		"next after 1e7": {"n": {math.Nextafter(10_000_000, math.Inf(1)), 0}},
		"MaxFloat64":     {"n": {math.MaxFloat64, 0}},
		"-MaxFloat64":    {"n": {0, -math.MaxFloat64}},
		"mixed far":      {"good-4": {1, 1}, "far": {1e8, 0}},
	} {
		if err := SetMapPositions(changes); err == nil {
			t.Errorf("%s: nil error, want a refusal", name)
		}
		if got := MapPositions(); !reflect.DeepEqual(got, base) {
			t.Fatalf("%s: saved set = %v, want unchanged %v", name, got, base)
		}
	}
}

// The key bound is 200 BYTES inclusive.
func TestAgentMapEdgeSetMapPositionsKeyBound(t *testing.T) {
	useTestStore(t)
	k200 := strings.Repeat("k", 200)
	r200 := strings.Repeat("é", 100) // 200 bytes
	if err := SetMapPositions(map[string][2]float64{k200: {1, 2}, r200: {3, 4}}); err != nil {
		t.Fatalf("200-byte keys refused: %v", err)
	}
	got := MapPositions()
	if got[k200] != [2]float64{1, 2} || got[r200] != [2]float64{3, 4} {
		t.Fatalf("saved = %v, want both 200-byte keys", got)
	}
}

// Negative, zero and fractional coordinates are positions like any other, and
// amendment 6's bound is inclusive: exactly ±10,000,000 is accepted.
func TestAgentMapEdgeSetMapPositionsFiniteExtremes(t *testing.T) {
	useTestStore(t)
	in := map[string][2]float64{
		"neg":   {-12345.5, -0.25},
		"zero":  {0, 0},
		"edge":  {10_000_000, -10_000_000},
		"edge2": {-10_000_000, 10_000_000},
	}
	if err := SetMapPositions(in); err != nil {
		t.Fatalf("finite extremes refused: %v", err)
	}
	if got := MapPositions(); !reflect.DeepEqual(got, in) {
		t.Fatalf("saved = %v, want %v", got, in)
	}
}

// agentMapEdgeFill writes n keys "<prefix>-%04d" in ONE call. A key's age is
// the order of the call that last wrote it (G7 amendment 6), so each call here
// and below is its own generation, strictly newer than the one before.
func agentMapEdgeFill(t *testing.T, prefix string, n int) {
	t.Helper()
	m := make(map[string][2]float64, n)
	for i := 0; i < n; i++ {
		m[fmt.Sprintf("%s-%04d", prefix, i)] = [2]float64{float64(i), 0}
	}
	if err := SetMapPositions(m); err != nil {
		t.Fatalf("fill %s x%d: %v", prefix, n, err)
	}
}

func agentMapEdgeHas(t *testing.T, got map[string][2]float64, want bool, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if _, ok := got[k]; ok != want {
			t.Errorf("key %q present = %v, want %v", k, ok, want)
		}
	}
}

// Amendment 6: at the cap a new key is ACCEPTED and the key written longest
// ago is dropped, leaving exactly 2,000.
func TestAgentMapEdgeSetMapPositionsEvictsOldest(t *testing.T) {
	useTestStore(t)
	if err := SetMapPositions(map[string][2]float64{"oldest": {1, 1}}); err != nil {
		t.Fatal(err)
	}
	agentMapEdgeFill(t, "mid", 1999)
	if err := SetMapPositions(map[string][2]float64{"new": {7, 7}}); err != nil {
		t.Fatalf("a new key at the cap: %v, want it accepted", err)
	}
	got := MapPositions()
	if len(got) != 2000 {
		t.Fatalf("len = %d, want 2000", len(got))
	}
	agentMapEdgeHas(t, got, false, "oldest")
	agentMapEdgeHas(t, got, true, "new", "mid-0000", "mid-1998")
	if got["new"] != [2]float64{7, 7} {
		t.Fatalf("new = %v, want [7 7]", got["new"])
	}
}

// Amendment 6: a key's age is its LAST write — rewriting the oldest key makes
// the next-oldest the one evicted.
func TestAgentMapEdgeSetMapPositionsRewriteRefreshesAge(t *testing.T) {
	useTestStore(t)
	if err := SetMapPositions(map[string][2]float64{"a": {1, 1}}); err != nil {
		t.Fatal(err)
	}
	if err := SetMapPositions(map[string][2]float64{"b": {2, 2}}); err != nil {
		t.Fatal(err)
	}
	agentMapEdgeFill(t, "f", 1998)
	if err := SetMapPositions(map[string][2]float64{"a": {3, 3}}); err != nil {
		t.Fatal(err)
	}
	if err := SetMapPositions(map[string][2]float64{"new": {4, 4}}); err != nil {
		t.Fatalf("new at the cap: %v", err)
	}
	got := MapPositions()
	if len(got) != 2000 {
		t.Fatalf("len = %d, want 2000", len(got))
	}
	agentMapEdgeHas(t, got, false, "b")
	agentMapEdgeHas(t, got, true, "a", "new")
	if got["a"] != [2]float64{3, 3} {
		t.Fatalf("a = %v, want its rewrite [3 3]", got["a"])
	}
}

// Amendment 6: a key IN changes is never evicted, even when it is among the
// oldest saved; only as many old keys go as the merge overshoots.
func TestAgentMapEdgeSetMapPositionsNeverEvictsChanges(t *testing.T) {
	useTestStore(t)
	if err := SetMapPositions(map[string][2]float64{"o1": {1, 1}, "o2": {2, 2}, "o3": {3, 3}}); err != nil {
		t.Fatal(err)
	}
	agentMapEdgeFill(t, "m", 1997)
	if err := SetMapPositions(map[string][2]float64{"n1": {9, 9}, "n2": {9, 9}, "o1": {5, 5}}); err != nil {
		t.Fatalf("batch at the cap: %v", err)
	}
	got := MapPositions()
	if len(got) != 2000 {
		t.Fatalf("len = %d, want 2000", len(got))
	}
	agentMapEdgeHas(t, got, false, "o2", "o3")
	agentMapEdgeHas(t, got, true, "o1", "n1", "n2", "m-0000", "m-1996")
	if got["o1"] != [2]float64{5, 5} {
		t.Fatalf("o1 = %v, want [5 5]", got["o1"])
	}
}

// Amendment 6: a changes map of exactly 2,000 keys is accepted even over a
// store already holding other keys (they all go); one of 2,001 is refused and
// stores nothing.
func TestAgentMapEdgeSetMapPositionsChangesSizeBound(t *testing.T) {
	useTestStore(t)
	if err := SetMapPositions(map[string][2]float64{"x": {1, 1}, "y": {2, 2}}); err != nil {
		t.Fatal(err)
	}
	agentMapEdgeFill(t, "k", 2000)
	got := MapPositions()
	if len(got) != 2000 {
		t.Fatalf("len = %d, want 2000", len(got))
	}
	agentMapEdgeHas(t, got, false, "x", "y")
	before := MapPositions()
	over := make(map[string][2]float64, 2001)
	for i := 0; i < 2001; i++ {
		over[fmt.Sprintf("z%d", i)] = [2]float64{0, float64(i)}
	}
	if err := SetMapPositions(over); err == nil {
		t.Fatal("2001 keys in one write: nil error, want a refusal")
	}
	if got := MapPositions(); !reflect.DeepEqual(got, before) {
		t.Fatalf("a refused 2001-key write changed the saved set (len %d)", len(got))
	}
}

// Amendment 6: a key saved before write times were kept counts as OLDEST. The
// pre-amendment row is the plain {"<key>": [x, y]} object in kv
// `map_positions` (G7 names the row).
func TestAgentMapEdgeSetMapPositionsLegacyKeyIsOldest(t *testing.T) {
	st := useTestStore(t)
	if err := st.SetKV("map_positions", `{"legacy":[1,2]}`); err != nil {
		t.Fatal(err)
	}
	if got := MapPositions(); got["legacy"] != [2]float64{1, 2} {
		t.Fatalf("legacy row read = %v, want legacy [1 2]", got)
	}
	agentMapEdgeFill(t, "fresh", 1999)
	if err := SetMapPositions(map[string][2]float64{"new": {3, 3}}); err != nil {
		t.Fatalf("new at the cap: %v", err)
	}
	got := MapPositions()
	if len(got) != 2000 {
		t.Fatalf("len = %d, want 2000", len(got))
	}
	agentMapEdgeHas(t, got, false, "legacy")
	agentMapEdgeHas(t, got, true, "new", "fresh-0000", "fresh-1998")
}
