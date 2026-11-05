package jobs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"kraken/internal/store"
)

// agentmap_contract_test.go — athena-logic's blind tests for the agent map's
// jobs half (50-contract.md G3, G4's jobs verbs, G7): BuildJobActivity,
// JobActivityFor, JobEvents, RecordCellMessage, JobForZeusName, and the saved
// map positions.

var agentMapContractT0 = time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)

func agentMapContractTS(offsetMS int64) string {
	return agentMapContractT0.Add(time.Duration(offsetMS) * time.Millisecond).Format("2006-01-02T15:04:05.000Z")
}

func agentMapContractMS(offsetMS int64) int64 { return agentMapContractT0.UnixMilli() + offsetMS }

func agentMapContractLine(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func agentMapContractCall(t *testing.T, at int64, id, name string, input any) string {
	return agentMapContractLine(t, map[string]any{
		"type":      "assistant",
		"uuid":      "a-" + id,
		"timestamp": agentMapContractTS(at),
		"message": map[string]any{
			"role":    "assistant",
			"content": []any{map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}},
		},
	})
}

func agentMapContractHear(t *testing.T, at int64, uuid, text string) string {
	return agentMapContractLine(t, map[string]any{
		"type":      "user",
		"uuid":      uuid,
		"timestamp": agentMapContractTS(at),
		"message":   map[string]any{"role": "user", "content": text},
	})
}

func agentMapContractWrite(t *testing.T, path string, lines []string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := ""
	if len(lines) > 0 {
		body = strings.Join(lines, "\n") + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// agentMapContractBashCalls is n Bash `go test` calls one second apart,
// ids call-0 .. call-(n-1).
func agentMapContractBashCalls(t *testing.T, n int) []string {
	var out []string
	for i := 0; i < n; i++ {
		out = append(out, agentMapContractCall(t, int64(i)*1000, "call-"+strconv.Itoa(i), "Bash", map[string]any{"command": "go test ./..."}))
	}
	return out
}

// G3: "const ActivityBoardEvents = 24" (amended from 16).
func TestAgentMapContractActivityBoardEvents(t *testing.T) {
	if ActivityBoardEvents != 24 {
		t.Fatalf("ActivityBoardEvents = %d, want 24", ActivityBoardEvents)
	}
}

// G3: Events is the lead's newest ActivityBoardEvents (24), oldest first; Act/Label from the newest
// non-hear event; At from the newest event, a hear included.
func TestAgentMapContractBuildJobActivityLead(t *testing.T) {
	dir := t.TempDir()
	lead := filepath.Join(dir, "lead.jsonl")
	lines := agentMapContractBashCalls(t, 29)
	lines = append(lines,
		agentMapContractCall(t, 29000, "call-edit", "Edit", map[string]any{"file_path": "/r/board.go"}),
		agentMapContractHear(t, 30000, "u-last", "a prompt from the operator"),
	)
	agentMapContractWrite(t, lead, lines)

	a := BuildJobActivity(lead, nil, nil)
	if a == nil {
		t.Fatal("BuildJobActivity = nil for a lead with events")
	}
	if len(a.Events) != ActivityBoardEvents {
		t.Fatalf("len(Events) = %d, want %d", len(a.Events), ActivityBoardEvents)
	}
	// 31 events in all; the newest 24 are call-7 .. call-28, call-edit, u-last
	if a.Events[0].ID != "call-7" || a.Events[22].ID != "call-edit" || a.Events[23].ID != "u-last" {
		t.Fatalf("Events ids = first %q, [22] %q, last %q; want call-7, call-edit, u-last",
			a.Events[0].ID, a.Events[22].ID, a.Events[23].ID)
	}
	for i := 1; i < len(a.Events); i++ {
		if a.Events[i].T < a.Events[i-1].T {
			t.Fatalf("Events not oldest first at %d: %+v", i, a.Events)
		}
	}
	if a.Act != "edit" || a.Label != "Edit board.go" {
		t.Errorf("Act/Label = %q/%q, want edit/'Edit board.go' (the newest non-hear)", a.Act, a.Label)
	}
	if a.At != agentMapContractMS(30000) {
		t.Errorf("At = %d, want the newest event's T %d", a.At, agentMapContractMS(30000))
	}
	if len(a.Crew) != 0 {
		t.Errorf("Crew = %+v, want none", a.Crew)
	}
}

// G3: "Act is think and Label empty when there is none" (only hears).
func TestAgentMapContractBuildJobActivityOnlyHears(t *testing.T) {
	lead := filepath.Join(t.TempDir(), "lead.jsonl")
	agentMapContractWrite(t, lead, []string{
		agentMapContractHear(t, 0, "u1", "hello"),
		agentMapContractHear(t, 1000, "u2", `<teammate-message teammate_id="heph-1">done</teammate-message>`),
	})
	a := BuildJobActivity(lead, nil, nil)
	if a == nil {
		t.Fatal("BuildJobActivity = nil though the lead has two events")
	}
	if a.Act != "think" || a.Label != "" {
		t.Errorf("Act/Label = %q/%q, want think/''", a.Act, a.Label)
	}
	if a.At != agentMapContractMS(1000) || len(a.Events) != 2 {
		t.Errorf("At=%d events=%d, want %d and 2", a.At, len(a.Events), agentMapContractMS(1000))
	}
}

// G3: "nil when the lead transcript yields no events".
func TestAgentMapContractBuildJobActivityNoEvents(t *testing.T) {
	lead := filepath.Join(t.TempDir(), "empty.jsonl")
	agentMapContractWrite(t, lead, nil)
	if a := BuildJobActivity(lead, nil, nil); a != nil {
		t.Fatalf("BuildJobActivity(empty) = %+v, want nil", a)
	}
	if a := BuildJobActivity(filepath.Join(t.TempDir(), "missing.jsonl"), nil, nil); a != nil {
		t.Fatalf("BuildJobActivity(missing) = %+v, want nil", a)
	}
}

// G3: Crew follows crewOrder, one entry per name with a path whose transcript
// holds a call; that teammate's newest call's Act, Label and T.
func TestAgentMapContractBuildJobActivityCrew(t *testing.T) {
	dir := t.TempDir()
	lead := filepath.Join(dir, "lead.jsonl")
	agentMapContractWrite(t, lead, agentMapContractBashCalls(t, 2))

	a := filepath.Join(dir, "a.jsonl")
	agentMapContractWrite(t, a, []string{
		agentMapContractCall(t, 100, "a1", "Read", map[string]any{"file_path": "/r/x.go"}),
		agentMapContractCall(t, 200, "a2", "Edit", map[string]any{"file_path": "/r/y.go"}),
		// a hear after the newest call does not displace it
		agentMapContractHear(t, 300, "a-u", "from the lead"),
	})
	b := filepath.Join(dir, "b.jsonl")
	agentMapContractWrite(t, b, []string{
		agentMapContractCall(t, 500, "b1", "Grep", map[string]any{"pattern": "x"}),
	})
	c := filepath.Join(dir, "c.jsonl") // hears only: no call
	agentMapContractWrite(t, c, []string{agentMapContractHear(t, 600, "c-u", "hello")})

	crew := map[string]string{"a": a, "b": b, "c": c}
	act := BuildJobActivity(lead, crew, []string{"b", "nopath", "a", "c"})
	if act == nil {
		t.Fatal("BuildJobActivity = nil")
	}
	want := []CrewActivity{
		{Name: "b", Act: "search", Label: "Grep", At: agentMapContractMS(500)},
		{Name: "a", Act: "edit", Label: "Edit y.go", At: agentMapContractMS(200)},
	}
	if len(act.Crew) != len(want) {
		t.Fatalf("Crew = %+v, want %+v", act.Crew, want)
	}
	for i := range want {
		if act.Crew[i] != want[i] {
			t.Errorf("Crew[%d] = %+v, want %+v", i, act.Crew[i], want[i])
		}
	}
}

// G3: the wire tags of JobActivity and CrewActivity.
func TestAgentMapContractJobActivityJSON(t *testing.T) {
	b, err := json.Marshal(JobActivity{Act: "edit", Label: "l", At: 1, Crew: []CrewActivity{{Name: "n", Act: "read", Label: "x", At: 2}}})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"act", "label", "at", "events", "crew"} {
		if _, ok := m[k]; !ok {
			t.Errorf("JobActivity JSON %s missing %q", b, k)
		}
	}
	crew := m["crew"].([]any)[0].(map[string]any)
	for _, k := range []string{"name", "act", "label", "at"} {
		if _, ok := crew[k]; !ok {
			t.Errorf("CrewActivity JSON missing %q", k)
		}
	}
}

// agentMapContractLeadSession puts a transcript for session sid under
// ~/.claude/projects/<proj>/<sid>.jsonl in a sandboxed HOME.
func agentMapContractLeadSession(t *testing.T, sid string, lines []string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".claude", "projects", "-some-proj", sid+".jsonl")
	agentMapContractWrite(t, path, lines)
	return path
}

// G3: JobActivityFor resolves the stamped lead session to its transcript.
func TestAgentMapContractJobActivityForLead(t *testing.T) {
	sid := "11111111-2222-3333-4444-555555555555"
	agentMapContractLeadSession(t, sid, agentMapContractBashCalls(t, 3))
	j := &Job{Slug: "agentmap-demo", Repo: "kraken", State: "working", Session: sid}
	a := JobActivityFor(j, nil)
	if a == nil {
		t.Fatal("JobActivityFor = nil for a job whose lead transcript holds events")
	}
	if len(a.Events) != 3 || a.Act != "test" || a.Label != "go test" || a.At != agentMapContractMS(2000) {
		t.Fatalf("JobActivityFor = %+v, want 3 events, test/go test at %d", a, agentMapContractMS(2000))
	}
}

// G3: nil for a nil job, no stamped session, or a missing transcript.
func TestAgentMapContractJobActivityForNil(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if a := JobActivityFor(nil, nil); a != nil {
		t.Errorf("JobActivityFor(nil) = %+v", a)
	}
	if a := JobActivityFor(&Job{Slug: "x", Repo: "kraken", State: "working"}, nil); a != nil {
		t.Errorf("JobActivityFor(no session) = %+v", a)
	}
	if a := JobActivityFor(&Job{Slug: "x", Repo: "kraken", State: "working", Session: "99999999-0000-0000-0000-000000000000"}, []string{"heph-1"}); a != nil {
		t.Errorf("JobActivityFor(missing transcript) = %+v", a)
	}
}

// G3: JobEvents — the lead's newest n, clamped to 1..200; a non-nil empty
// slice when there is nothing to read.
func TestAgentMapContractJobEvents(t *testing.T) {
	sid := "22222222-2222-3333-4444-555555555555"
	agentMapContractLeadSession(t, sid, agentMapContractBashCalls(t, 5))
	j := &Job{Slug: "agentmap-demo", Repo: "kraken", State: "working", Session: sid}

	evs, err := JobEvents(j, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 3 || evs[0].ID != "call-2" || evs[2].ID != "call-4" {
		t.Fatalf("JobEvents(3) = %+v, want call-2..call-4", evs)
	}
	evs, err = JobEvents(j, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].ID != "call-4" {
		t.Fatalf("JobEvents(0) = %+v, want only call-4", evs)
	}
	evs, err = JobEvents(j, 10_000)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 5 {
		t.Fatalf("JobEvents(10000) = %d events, want all 5", len(evs))
	}

	empty, err := JobEvents(&Job{Slug: "nobody", Repo: "kraken", State: "working"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if empty == nil || len(empty) != 0 {
		t.Fatalf("JobEvents(no session) = %#v, want a non-nil empty slice", empty)
	}
}

// G3: clamped at 200.
func TestAgentMapContractJobEventsClampsAt200(t *testing.T) {
	sid := "33333333-2222-3333-4444-555555555555"
	agentMapContractLeadSession(t, sid, agentMapContractBashCalls(t, 230))
	j := &Job{Slug: "agentmap-demo", Repo: "kraken", State: "working", Session: sid}
	evs, err := JobEvents(j, 500)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 200 || evs[199].ID != "call-229" || evs[0].ID != "call-30" {
		t.Fatalf("JobEvents(500) = %d events (first %q), want the newest 200 ending call-229", len(evs), evs[0].ID)
	}
}

// ---------------------------------------------------------------------------
// G4: RecordCellMessage, JobForZeusName
// ---------------------------------------------------------------------------

func agentMapContractTwoJobs(t *testing.T) (*store.Store, *Job, *Job) {
	t.Helper()
	st := useTestStore(t)
	mustCreateJob(t, st, store.Job{Slug: "map-alpha", Repo: "kraken", State: "working", Body: "spec\n"})
	mustCreateJob(t, st, store.Job{Slug: "map-beta", Repo: "kraken", State: "blocked", Body: "spec\n"})
	all := LoadJobs()
	a, b := JobBySlug(all, "map-alpha"), JobBySlug(all, "map-beta")
	if a == nil || b == nil || a.ID == 0 || b.ID == 0 {
		t.Fatalf("seed jobs not loaded: %+v %+v", a, b)
	}
	return st, a, b
}

// G4: "Bumps the pair at the current time."
func TestAgentMapContractRecordCellMessage(t *testing.T) {
	st, a, b := agentMapContractTwoJobs(t)
	if err := RecordCellMessage(a, b); err != nil {
		t.Fatalf("RecordCellMessage: %v", err)
	}
	if err := RecordCellMessage(a, b); err != nil {
		t.Fatalf("RecordCellMessage (2nd): %v", err)
	}
	rows, err := st.ListJobMessages()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want one (a->b)", rows)
	}
	r := rows[0]
	if r.FromID != a.ID || r.ToID != b.ID || r.N != 2 || r.FirstAt <= 0 || r.LastAt < r.FirstAt {
		t.Fatalf("row = %+v, want a->b N 2 with positive stamps", r)
	}
}

// G4: an error and nothing stored for the same job on both sides.
func TestAgentMapContractRecordCellMessageSameJob(t *testing.T) {
	st, a, _ := agentMapContractTwoJobs(t)
	if err := RecordCellMessage(a, a); err == nil {
		t.Fatal("RecordCellMessage(a, a) = nil, want an error")
	}
	rows, _ := st.ListJobMessages()
	if len(rows) != 0 {
		t.Fatalf("rows = %+v, want none", rows)
	}
}

// G4: "The active job one of whose ZeusNameCandidates equals name".
func TestAgentMapContractJobForZeusName(t *testing.T) {
	_, a, b := agentMapContractTwoJobs(t)
	if got := JobForZeusName(ZeusName(b)); got == nil || got.ID != b.ID {
		t.Fatalf("JobForZeusName(%q) = %+v, want map-beta", ZeusName(b), got)
	}
	if got := JobForZeusName("zeus-map-alpha"); got == nil || got.ID != a.ID {
		t.Fatalf("JobForZeusName(zeus-map-alpha) = %+v, want map-alpha", got)
	}
	if got := JobForZeusName("zeus-no-such-job"); got != nil {
		t.Fatalf("JobForZeusName(unknown) = %+v, want nil", got)
	}
}

// ---------------------------------------------------------------------------
// G7: saved map positions
// ---------------------------------------------------------------------------

// G7: "a non-nil empty map when none are saved or no store is wired".
func TestAgentMapContractMapPositionsEmpty(t *testing.T) {
	UseStore(nil)
	if m := MapPositions(); m == nil || len(m) != 0 {
		t.Fatalf("MapPositions() with no store = %#v, want non-nil empty", m)
	}
	useTestStore(t)
	if m := MapPositions(); m == nil || len(m) != 0 {
		t.Fatalf("MapPositions() on a fresh store = %#v, want non-nil empty", m)
	}
}

// G7: "Merges: each key in changes is set, every other saved key is kept."
func TestAgentMapContractSetMapPositionsMerges(t *testing.T) {
	useTestStore(t)
	if err := SetMapPositions(map[string][2]float64{"alpha": {10, 20}, "archive:old": {-5.5, 7.25}}); err != nil {
		t.Fatalf("SetMapPositions: %v", err)
	}
	if err := SetMapPositions(map[string][2]float64{"nodeX/beta": {300, 400}}); err != nil {
		t.Fatalf("SetMapPositions: %v", err)
	}
	if err := SetMapPositions(map[string][2]float64{"alpha": {11, 21}}); err != nil {
		t.Fatalf("SetMapPositions: %v", err)
	}
	got := MapPositions()
	want := map[string][2]float64{"alpha": {11, 21}, "archive:old": {-5.5, 7.25}, "nodeX/beta": {300, 400}}
	if len(got) != len(want) {
		t.Fatalf("MapPositions() = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("MapPositions()[%q] = %v, want %v", k, got[k], v)
		}
	}
}

// G7 amendment 6: "the cap no longer refuses a merge. When a merge would
// leave more than 2,000 keys, the keys written longest ago that are not in
// changes are dropped until 2,000 remain (a key's age is its last write
// through this function)" — the order of the call, so back-to-back calls
// are strictly ordered.
func TestAgentMapContractSetMapPositionsEvictsOldest(t *testing.T) {
	useTestStore(t)
	if err := SetMapPositions(map[string][2]float64{"oldest": {1, 1}, "refreshed": {2, 2}}); err != nil {
		t.Fatalf("first write: %v", err)
	}
	batch := make(map[string][2]float64, 1998)
	for i := 0; i < 1998; i++ {
		batch["card-"+strconv.Itoa(i)] = [2]float64{float64(i), float64(-i)}
	}
	if err := SetMapPositions(batch); err != nil {
		t.Fatalf("batch write: %v", err)
	}
	// rewriting a key makes it the newest
	if err := SetMapPositions(map[string][2]float64{"refreshed": {3, 3}}); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if got := len(MapPositions()); got != 2000 {
		t.Fatalf("saved keys = %d, want 2000", got)
	}
	// one more key: the merge would leave 2001, so the oldest is dropped
	if err := SetMapPositions(map[string][2]float64{"newcomer": {4, 4}}); err != nil {
		t.Fatalf("SetMapPositions over the cap = %v, want the merge accepted", err)
	}
	got := MapPositions()
	if len(got) != 2000 {
		t.Fatalf("saved keys after eviction = %d, want 2000", len(got))
	}
	if _, ok := got["oldest"]; ok {
		t.Fatal(`"oldest" survived, want it evicted as the key written longest ago`)
	}
	if got["newcomer"] != [2]float64{4, 4} || got["refreshed"] != [2]float64{3, 3} {
		t.Fatalf("newcomer = %v, refreshed = %v; want both kept", got["newcomer"], got["refreshed"])
	}
	if got["card-0"] != [2]float64{0, 0} || got["card-1997"] != [2]float64{1997, -1997} {
		t.Fatalf("batch keys not kept: card-0 %v card-1997 %v", got["card-0"], got["card-1997"])
	}
}

// G7 amendment 6: a changes map of exactly 2,000 keys is not refused (only
// "a changes map that itself holds more than 2,000 keys is refused"), and it
// replaces older keys not in it.
func TestAgentMapContractSetMapPositionsFullBatch(t *testing.T) {
	useTestStore(t)
	if err := SetMapPositions(map[string][2]float64{"before": {1, 1}}); err != nil {
		t.Fatal(err)
	}
	batch := make(map[string][2]float64, 2000)
	for i := 0; i < 2000; i++ {
		batch["k"+strconv.Itoa(i)] = [2]float64{float64(i), 0}
	}
	if err := SetMapPositions(batch); err != nil {
		t.Fatalf("SetMapPositions(2000 keys) = %v, want accepted", err)
	}
	got := MapPositions()
	if len(got) != 2000 {
		t.Fatalf("saved keys = %d, want 2000", len(got))
	}
	if _, ok := got["before"]; ok {
		t.Fatal(`"before" kept, want it evicted for the 2,000 keys of the batch`)
	}
}
