package jobs

import (
	"bytes"
	"encoding/json"
	"reflect"
	"sync"
	"testing"
	"time"

	"kraken/internal/store"
)

// handoff_contract_test.go — athena-logic's BLIND contract tests for the
// handoff build's slice A (package jobs half: the hook command builders, the
// SessionStart group in mergeHookSettings, the cell statusline) and slice B
// (the cell context report and the auto-handoff pass), per 50-contract.md.
//
// Dimension: LOGIC — the stated obligations and happy paths, one answer per
// assertion. Refusals and malformed input are athena-edge's.
//
// Every package-level identifier is prefixed `logic`: a sibling author shares
// this package's declaration space.

// logicDefaultFlags pins hookCommandFlags() to its empty form for one test.
func logicDefaultFlags(t *testing.T) {
	t.Helper()
	SetHookServeAddr("")
	t.Cleanup(func() { SetHookServeAddr("") })
	t.Setenv("KRAKEN_DB_PATH", "")
}

// logicStubArtifacts stubs the cellArtifactsForSlug seam.
func logicStubArtifacts(t *testing.T, dir string) {
	t.Helper()
	prev := cellArtifactsForSlug
	cellArtifactsForSlug = func(string) string { return dir }
	t.Cleanup(func() { cellArtifactsForSlug = prev })
}

const logicArtifacts = "/Users/x/.kraken/cells/foo-7/artifacts"

// ---------------------------------------------------------------------------
// command builders
// ---------------------------------------------------------------------------

func TestLogicHandoffHookCommandDefault(t *testing.T) {
	logicDefaultFlags(t)
	got := handoffHookCommand("foo", "kraken", logicArtifacts)
	want := "kraken handoff-hook foo --artifacts " + logicArtifacts
	if got != want {
		t.Errorf("handoffHookCommand = %q, want %q", got, want)
	}
}

// The serve flags follow the artifacts dir (`... --artifacts <dir>` followed
// by hookCommandFlags()).
func TestLogicHandoffHookCommandCarriesServeFlags(t *testing.T) {
	SetHookServeAddr("127.0.0.1:9999")
	t.Cleanup(func() { SetHookServeAddr("") })
	t.Setenv("KRAKEN_DB_PATH", "")
	got := handoffHookCommand("foo", "/bin/kraken", logicArtifacts)
	want := "/bin/kraken handoff-hook foo --artifacts " + logicArtifacts + hookCommandFlags()
	if got != want {
		t.Errorf("handoffHookCommand = %q, want %q", got, want)
	}
	if hookCommandFlags() == "" {
		t.Fatal("fixture proves nothing: hookCommandFlags() is empty with a non-default serve addr")
	}
}

func TestLogicHandoffHookEntry(t *testing.T) {
	logicDefaultFlags(t)
	got := handoffHookEntry("foo", "kraken", logicArtifacts)
	want := map[string]any{
		"matcher": "compact",
		"hooks": []any{
			map[string]any{"type": "command", "command": handoffHookCommand("foo", "kraken", logicArtifacts)},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("handoffHookEntry = %#v, want %#v", got, want)
	}
}

func TestLogicCellCtxHookCommand(t *testing.T) {
	logicDefaultFlags(t)
	if got, want := cellCtxHookCommand("foo", "kraken"), "kraken ctx-hook --slug foo"; got != want {
		t.Errorf("cellCtxHookCommand = %q, want %q", got, want)
	}
	SetHookServeAddr("127.0.0.1:9999")
	if got, want := cellCtxHookCommand("foo", "kraken"), "kraken ctx-hook --slug foo"+hookCommandFlags(); got != want {
		t.Errorf("cellCtxHookCommand with a serve addr = %q, want %q", got, want)
	}
}

func TestLogicCellCtxKVKey(t *testing.T) {
	if got, want := cellCtxKVKey(7, "sess-a"), "cell_ctx:7:sess-a"; got != want {
		t.Errorf("cellCtxKVKey = %q, want %q", got, want)
	}
	if got, want := autoHandoffKVKey(7), "auto_handoff:7"; got != want {
		t.Errorf("autoHandoffKVKey = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// mergeHookSettings: the SessionStart group and the statusline
// ---------------------------------------------------------------------------

func logicGroup(matcher, cmd string) map[string]any {
	g := map[string]any{
		"hooks": []any{map[string]any{"type": "command", "command": cmd}},
	}
	if matcher != "" {
		g["matcher"] = matcher
	}
	return g
}

func logicMerge(t *testing.T, existing string) (map[string]any, []byte) {
	t.Helper()
	out, err := mergeHookSettings([]byte(existing), "foo", "kraken")
	if err != nil {
		t.Fatalf("mergeHookSettings: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	return parsed, out
}

// U10 + U13: a fresh render carries the SessionStart compact group, and the
// four existing groups render exactly as TestMergeHookSettings expects them.
func TestLogicMergeHookSettingsAddsSessionStart(t *testing.T) {
	logicDefaultFlags(t)
	logicStubArtifacts(t, logicArtifacts)

	parsed, _ := logicMerge(t, "")
	hooks, ok := parsed["hooks"].(map[string]any)
	if !ok {
		t.Fatalf("hooks = %#v, want an object", parsed["hooks"])
	}
	want := map[string]any{
		"PostToolUse":  []any{logicGroup("Bash", "kraken pr-hook foo")},
		"PreToolUse":   []any{logicGroup("Bash", "kraken bash-lint")},
		"StopFailure":  []any{logicGroup("rate_limit", "kraken limit-hook foo")},
		"SessionStart": []any{logicGroup("compact", "kraken handoff-hook foo --artifacts "+logicArtifacts)},
	}
	for _, event := range recallHookEvents {
		existing, _ := want[event].([]any)
		want[event] = append(existing, logicGroup("", "kraken recall-hook foo"))
	}
	// round-trip the expectation through JSON so its types match the parsed side
	raw, _ := json.Marshal(want)
	var wantParsed map[string]any
	_ = json.Unmarshal(raw, &wantParsed)
	if !reflect.DeepEqual(hooks, wantParsed) {
		t.Errorf("hooks = %v\nwant    %v", hooks, wantParsed)
	}
}

// The statusline is installed by the same merge.
func TestLogicMergeHookSettingsInstallsStatusLine(t *testing.T) {
	logicDefaultFlags(t)
	logicStubArtifacts(t, logicArtifacts)

	parsed, _ := logicMerge(t, "")
	want := map[string]any{"type": "command", "command": "kraken ctx-hook --slug foo"}
	if !reflect.DeepEqual(parsed["statusLine"], want) {
		t.Errorf("statusLine = %#v, want %#v", parsed["statusLine"], want)
	}
}

// U11: a second merge over its own output is byte-identical.
func TestLogicMergeHookSettingsIdempotent(t *testing.T) {
	logicDefaultFlags(t)
	logicStubArtifacts(t, logicArtifacts)

	_, first := logicMerge(t, "")
	second, err := mergeHookSettings(first, "foo", "kraken")
	if err != nil {
		t.Fatalf("second merge: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Errorf("second merge differs:\nfirst  %s\nsecond %s", first, second)
	}
}

// U12: a repo's own SessionStart group survives in its original position,
// kraken's appended after it.
func TestLogicMergeHookSettingsKeepsRepoSessionStart(t *testing.T) {
	logicDefaultFlags(t)
	logicStubArtifacts(t, logicArtifacts)

	parsed, _ := logicMerge(t, `{"hooks":{"SessionStart":[{"matcher":"startup","hooks":[{"type":"command","command":"echo repo"}]}]}}`)
	hooks := parsed["hooks"].(map[string]any)
	got, _ := hooks["SessionStart"].([]any)
	want := []any{
		map[string]any{"matcher": "startup", "hooks": []any{map[string]any{"type": "command", "command": "echo repo"}}},
		map[string]any{"matcher": "compact", "hooks": []any{map[string]any{"type": "command", "command": "kraken handoff-hook foo --artifacts " + logicArtifacts}}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SessionStart = %#v\nwant %#v", got, want)
	}
}

// When the seam resolves no artifacts dir, NO SessionStart group is added and
// nothing else changes.
func TestLogicMergeHookSettingsNoArtifactsNoGroup(t *testing.T) {
	logicDefaultFlags(t)

	logicStubArtifacts(t, logicArtifacts)
	with, _ := logicMerge(t, "")

	cellArtifactsForSlug = func(string) string { return "" }
	without, _ := logicMerge(t, "")

	hooks := without["hooks"].(map[string]any)
	if _, has := hooks["SessionStart"]; has {
		t.Fatalf("SessionStart = %#v, want no group when the slug resolves no artifacts dir", hooks["SessionStart"])
	}
	delete(with["hooks"].(map[string]any), "SessionStart")
	if !reflect.DeepEqual(with, without) {
		t.Errorf("render without the group differs elsewhere:\nwith (group removed) %v\nwithout              %v", with, without)
	}
}

// U13: the recall strip never removes the handoff group — including for a
// job slugged like the recall verb.
func TestLogicRecallStripLeavesHandoffGroup(t *testing.T) {
	logicDefaultFlags(t)
	for _, slug := range []string{"foo", "recall-hook"} {
		groups := []any{handoffHookEntry(slug, "kraken", logicArtifacts)}
		if got := withoutRecallHooks(groups); len(got) != 1 {
			t.Errorf("slug %q: withoutRecallHooks removed the handoff group (%v)", slug, got)
		}
	}
}

// ---------------------------------------------------------------------------
// mergeCellStatusLine
// ---------------------------------------------------------------------------

func TestLogicMergeCellStatusLineArms(t *testing.T) {
	logicDefaultFlags(t)
	want := map[string]any{"type": "command", "command": "kraken ctx-hook --slug foo"}

	// absent → set
	p := map[string]any{}
	mergeCellStatusLine(p, "foo", "kraken")
	if !reflect.DeepEqual(p["statusLine"], want) {
		t.Errorf("absent: statusLine = %#v, want %#v", p["statusLine"], want)
	}

	// ours (contains ctx-hook) → rewritten to the current command
	p = map[string]any{"statusLine": map[string]any{"type": "command", "command": "/old/kraken ctx-hook --slug foo --serve-addr 127.0.0.1:1"}}
	mergeCellStatusLine(p, "foo", "kraken")
	if !reflect.DeepEqual(p["statusLine"], want) {
		t.Errorf("ctx-hook present: statusLine = %#v, want rewritten to %#v", p["statusLine"], want)
	}

	// a repo's own object → untouched
	repo := map[string]any{"type": "command", "command": "./scripts/line.sh", "padding": float64(0)}
	p = map[string]any{"statusLine": repo}
	mergeCellStatusLine(p, "foo", "kraken")
	if !reflect.DeepEqual(p["statusLine"], map[string]any{"type": "command", "command": "./scripts/line.sh", "padding": float64(0)}) {
		t.Errorf("repo statusLine = %#v, want untouched", p["statusLine"])
	}

	// not an object → untouched
	p = map[string]any{"statusLine": "weird"}
	mergeCellStatusLine(p, "foo", "kraken")
	if p["statusLine"] != "weird" {
		t.Errorf("non-object statusLine = %#v, want untouched", p["statusLine"])
	}
}

// ---------------------------------------------------------------------------
// RecordCellContext / ReadCellContext
// ---------------------------------------------------------------------------

func TestLogicCellContextRoundTrip(t *testing.T) {
	st := useTestStore(t)

	before := time.Now().UnixMilli()
	if err := RecordCellContext(st, 7, "sess-a", 42.5, 1_000_000, 0); err != nil {
		t.Fatalf("RecordCellContext: %v", err)
	}
	after := time.Now().UnixMilli()

	c, ok := ReadCellContext(st, 7, "sess-a")
	if !ok {
		t.Fatal("ReadCellContext = false after a recorded report")
	}
	if c.SessionID != "sess-a" || c.Pct != 42.5 || c.Window != 1_000_000 {
		t.Errorf("ReadCellContext = %+v, want session sess-a pct 42.5 window 1000000", c)
	}
	if c.At < before || c.At > after {
		t.Errorf("At = %d, want unix millis within [%d, %d]", c.At, before, after)
	}

	// stored under cellCtxKVKey with the frozen json tags
	raw, err := st.GetKV("cell_ctx:7:sess-a")
	if err != nil {
		t.Fatalf("GetKV cell_ctx:7:sess-a: %v", err)
	}
	var tagged struct {
		SessionID string  `json:"session_id"`
		Pct       float64 `json:"pct"`
		Window    int64   `json:"window"`
		At        int64   `json:"at"`
	}
	if err := json.Unmarshal([]byte(raw), &tagged); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	if tagged.SessionID != "sess-a" || tagged.Pct != 42.5 || tagged.Window != 1_000_000 || tagged.At != c.At {
		t.Errorf("stored %q, want the CellContext under json tags session_id/pct/window/at", raw)
	}

	// keyed per job AND per session
	if _, ok := ReadCellContext(st, 7, "sess-b"); ok {
		t.Error("ReadCellContext(7, sess-b) = true — a teammate's session must not read zeus' report")
	}
	if _, ok := ReadCellContext(st, 8, "sess-a"); ok {
		t.Error("ReadCellContext(8, sess-a) = true — another job must not read this job's report")
	}

	// a later report replaces
	if err := RecordCellContext(st, 7, "sess-a", 80, 1_000_000, 0); err != nil {
		t.Fatalf("second RecordCellContext: %v", err)
	}
	if c, _ := ReadCellContext(st, 7, "sess-a"); c.Pct != 80 {
		t.Errorf("Pct after a second report = %v, want 80", c.Pct)
	}
}

// ---------------------------------------------------------------------------
// autoHandoffDue
// ---------------------------------------------------------------------------

func TestLogicAutoHandoffConstants(t *testing.T) {
	if autoHandoffThreshold != 75.0 {
		t.Errorf("autoHandoffThreshold = %v, want 75", autoHandoffThreshold)
	}
	if autoHandoffRetry != 15*time.Minute {
		t.Errorf("autoHandoffRetry = %v, want 15m", autoHandoffRetry)
	}
	if autoHandoffMaxFires != 3 {
		t.Errorf("autoHandoffMaxFires = %v, want 3", autoHandoffMaxFires)
	}
}

func TestLogicAutoHandoffDueRules(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	ms := func(d time.Duration) int64 { return now.Add(-d).UnixMilli() }
	ctx := func(sid string, pct float64) CellContext {
		return CellContext{SessionID: sid, Pct: pct, Window: 1_000_000}
	}

	cases := []struct {
		name       string
		c          CellContext
		s          autoHandoffState
		wantFire   bool
		wantNotify bool
		wantNext   autoHandoffState
	}{
		{
			name:     "rule 1+4: fresh state, exactly 75.0 fires",
			c:        ctx("s", 75.0),
			s:        autoHandoffState{},
			wantFire: true,
			wantNext: autoHandoffState{SessionID: "s", Fires: 1, LastFireAt: now.UnixMilli(), Armed: false},
		},
		{
			// Amended at the review (50-contract.md, autoHandoffDue rule 2):
			// a drop re-arms but does NOT clear Exhausted — only a session
			// change (rule 1) or a two-hour-old streak (rule 1b) does, so an
			// exhausted streak notifies once however often the fill bounces.
			name:     "rule 2: below threshold re-arms and keeps exhausted",
			c:        ctx("s", 74.9),
			s:        autoHandoffState{SessionID: "s", Fires: 3, LastFireAt: ms(time.Minute), Armed: false, Exhausted: true},
			wantNext: autoHandoffState{SessionID: "s", Fires: 3, LastFireAt: ms(time.Minute), Armed: true, Exhausted: true},
		},
		{
			name:     "rule 4: armed fires again",
			c:        ctx("s", 80),
			s:        autoHandoffState{SessionID: "s", Fires: 1, LastFireAt: ms(time.Minute), Armed: true},
			wantFire: true,
			wantNext: autoHandoffState{SessionID: "s", Fires: 2, LastFireAt: now.UnixMilli(), Armed: false},
		},
		{
			name:     "rule 5: not armed, inside the retry window → nothing",
			c:        ctx("s", 80),
			s:        autoHandoffState{SessionID: "s", Fires: 1, LastFireAt: ms(15*time.Minute - time.Second), Armed: false},
			wantNext: autoHandoffState{SessionID: "s", Fires: 1, LastFireAt: ms(15*time.Minute - time.Second), Armed: false},
		},
		{
			name:     "rule 4: not armed, retry window elapsed exactly → fires",
			c:        ctx("s", 80),
			s:        autoHandoffState{SessionID: "s", Fires: 1, LastFireAt: ms(15 * time.Minute), Armed: false},
			wantFire: true,
			wantNext: autoHandoffState{SessionID: "s", Fires: 2, LastFireAt: now.UnixMilli(), Armed: false},
		},
		{
			name:       "rule 3: at the cap → no fire, notify once, exhausted",
			c:          ctx("s", 90),
			s:          autoHandoffState{SessionID: "s", Fires: 3, LastFireAt: ms(20 * time.Minute), Armed: true},
			wantNotify: true,
			wantNext:   autoHandoffState{SessionID: "s", Fires: 3, LastFireAt: ms(20 * time.Minute), Armed: true, Exhausted: true},
		},
		{
			name:     "rule 3: already exhausted → no fire, no second notify",
			c:        ctx("s", 90),
			s:        autoHandoffState{SessionID: "s", Fires: 3, LastFireAt: ms(20 * time.Minute), Exhausted: true},
			wantNext: autoHandoffState{SessionID: "s", Fires: 3, LastFireAt: ms(20 * time.Minute), Exhausted: true},
		},
		{
			name:     "rule 1: a new session resets before anything else",
			c:        ctx("s-new", 80),
			s:        autoHandoffState{SessionID: "s-old", Fires: 3, LastFireAt: ms(time.Minute), Exhausted: true},
			wantFire: true,
			wantNext: autoHandoffState{SessionID: "s-new", Fires: 1, LastFireAt: now.UnixMilli(), Armed: false},
		},
		{
			name:     "rule 1+2: a new session below threshold is armed with nothing fired",
			c:        ctx("s-new", 10),
			s:        autoHandoffState{SessionID: "s-old", Fires: 2, LastFireAt: ms(time.Minute)},
			wantNext: autoHandoffState{SessionID: "s-new", Armed: true},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fire, notify, next := autoHandoffDue(c.c, c.s, now)
			if fire != c.wantFire {
				t.Errorf("fire = %v, want %v", fire, c.wantFire)
			}
			if notify != c.wantNotify {
				t.Errorf("notifyExhausted = %v, want %v", notify, c.wantNotify)
			}
			if next != c.wantNext {
				t.Errorf("next = %+v, want %+v", next, c.wantNext)
			}
		})
	}
}

// A session pinned above 75% fires three times (once on crossing, twice on
// the 15-minute retry), then notifies exactly once and goes quiet.
func TestLogicAutoHandoffDueCapsAtThree(t *testing.T) {
	start := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	c := CellContext{SessionID: "s", Pct: 88, Window: 1_000_000}
	var s autoHandoffState
	fires, notifies := 0, 0
	for i := 0; i < 8; i++ {
		fire, notify, next := autoHandoffDue(c, s, start.Add(time.Duration(i)*autoHandoffRetry))
		if fire {
			fires++
		}
		if notify {
			notifies++
		}
		s = next
	}
	if fires != 3 {
		t.Errorf("fires = %d, want 3", fires)
	}
	if notifies != 1 {
		t.Errorf("exhausted notifications = %d, want exactly 1", notifies)
	}
}

// ---------------------------------------------------------------------------
// autoHandoffPass
// ---------------------------------------------------------------------------

type logicNotes struct {
	mu   sync.Mutex
	seen []string
}

func (n *logicNotes) list() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.seen...)
}

// logicSwapNotify records every notification through the notifyFn seam the
// existing rotation tests use.
func logicSwapNotify(t *testing.T) *logicNotes {
	t.Helper()
	n := &logicNotes{}
	prev := notifyFn
	notifyFn = func(title, msg string) {
		n.mu.Lock()
		n.seen = append(n.seen, title+"|"+msg)
		n.mu.Unlock()
	}
	t.Cleanup(func() { notifyFn = prev })
	return n
}

func logicPassJob() *Job {
	return &Job{Slug: "logic-ah", ID: 41, Session: "sess-z"}
}

func logicLive(j *Job) map[string]bool { return map[string]bool{CellSession(j): true} }

func logicReadState(t *testing.T, st *store.Store, id int64) (autoHandoffState, bool) {
	t.Helper()
	raw, err := st.GetKV(autoHandoffKVKey(id))
	if err != nil || raw == "" {
		return autoHandoffState{}, false
	}
	var s struct {
		SessionID  string `json:"session_id"`
		Fires      int    `json:"fires"`
		LastFireAt int64  `json:"last_fire_at"`
		Armed      bool   `json:"armed"`
		Exhausted  bool   `json:"exhausted"`
	}
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatalf("decode %s = %q: %v", autoHandoffKVKey(id), raw, err)
	}
	return autoHandoffState(s), true
}

// Positive control for the gates below: every gate open, a below-threshold
// report → the state is persisted (it differs from the absent zero state),
// keyed on the job and read from zeus' stamped session.
func TestLogicAutoHandoffPassPersistsWhenGatesOpen(t *testing.T) {
	st := useTestStore(t)
	logicSwapNotify(t)
	j := logicPassJob()
	if err := RecordCellContext(st, j.ID, j.Session, 40, 1_000_000, 0); err != nil {
		t.Fatalf("RecordCellContext: %v", err)
	}
	autoHandoffPass(st, j, nil, logicLive(j), time.Now())
	s, ok := logicReadState(t, st, j.ID)
	if !ok {
		t.Fatal("no auto_handoff state persisted with every gate open")
	}
	if s != (autoHandoffState{SessionID: "sess-z", Armed: true}) {
		t.Errorf("state = %+v, want {SessionID: sess-z, Armed: true}", s)
	}
}

// Each early return leaves no state behind.
func TestLogicAutoHandoffPassNoSideEffectReturns(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(j *Job, live map[string]bool) bool // returns whether to record a report
	}{
		{"no stamped session", func(j *Job, live map[string]bool) bool { j.Session = ""; return true }},
		{"cell not live", func(j *Job, live map[string]bool) bool { delete(live, CellSession(j)); return true }},
		{"no report", func(j *Job, live map[string]bool) bool { return false }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := useTestStore(t)
			notes := logicSwapNotify(t)
			j := logicPassJob()
			live := logicLive(j)
			if c.mutate(j, live) {
				if err := RecordCellContext(st, j.ID, "sess-z", 40, 1_000_000, 0); err != nil {
					t.Fatalf("RecordCellContext: %v", err)
				}
			}
			autoHandoffPass(st, j, nil, live, time.Now())
			if s, ok := logicReadState(t, st, j.ID); ok {
				t.Errorf("state persisted = %+v, want none", s)
			}
			if got := notes.list(); len(got) != 0 {
				t.Errorf("notifications = %v, want none", got)
			}
		})
	}
}

// A nil store returns without touching anything (and without panicking).
func TestLogicAutoHandoffPassNilStore(t *testing.T) {
	st := useTestStore(t)
	notes := logicSwapNotify(t)
	j := logicPassJob()
	if err := RecordCellContext(st, j.ID, j.Session, 40, 1_000_000, 0); err != nil {
		t.Fatalf("RecordCellContext: %v", err)
	}
	autoHandoffPass(nil, j, nil, logicLive(j), time.Now())
	if s, ok := logicReadState(t, st, j.ID); ok {
		t.Errorf("state persisted = %+v, want none with a nil store", s)
	}
	if got := notes.list(); len(got) != 0 {
		t.Errorf("notifications = %v, want none", got)
	}
}

// Amendment: a nil job returns without side effects (and without panicking).
func TestLogicAutoHandoffPassNilJob(t *testing.T) {
	st := useTestStore(t)
	notes := logicSwapNotify(t)
	j := logicPassJob()
	if err := RecordCellContext(st, j.ID, j.Session, 40, 1_000_000, 0); err != nil {
		t.Fatalf("RecordCellContext: %v", err)
	}
	autoHandoffPass(st, nil, nil, logicLive(j), time.Now())
	if s, ok := logicReadState(t, st, j.ID); ok {
		t.Errorf("state persisted = %+v, want none with a nil job", s)
	}
	if got := notes.list(); len(got) != 0 {
		t.Errorf("notifications = %v, want none", got)
	}
}

// A due fire with no zeus in discovery writes NOTHING and notifies nothing.
func TestLogicAutoHandoffPassNoZeusWritesNothing(t *testing.T) {
	st := useTestStore(t)
	notes := logicSwapNotify(t)
	j := logicPassJob()
	if err := RecordCellContext(st, j.ID, j.Session, 80, 1_000_000, 0); err != nil {
		t.Fatalf("RecordCellContext: %v", err)
	}
	autoHandoffPass(st, j, nil, logicLive(j), time.Now())
	if s, ok := logicReadState(t, st, j.ID); ok {
		t.Errorf("state persisted = %+v, want none when zeus cannot be resolved", s)
	}
	if got := notes.list(); len(got) != 0 {
		t.Errorf("notifications = %v, want none", got)
	}
}

// At the cap: one "gave up" notification with the frozen title and body, the
// state persisted as exhausted, and a second pass says nothing more.
func TestLogicAutoHandoffPassExhaustedNotifiesOnce(t *testing.T) {
	st := useTestStore(t)
	notes := logicSwapNotify(t)
	j := logicPassJob()
	seed, _ := json.Marshal(map[string]any{
		"session_id": "sess-z", "fires": 3, "last_fire_at": time.Now().Add(-time.Hour).UnixMilli(),
		"armed": false, "exhausted": false,
	})
	if err := st.SetKV(autoHandoffKVKey(j.ID), string(seed)); err != nil {
		t.Fatalf("seed state: %v", err)
	}
	if err := RecordCellContext(st, j.ID, j.Session, 90, 1_000_000, 0); err != nil {
		t.Fatalf("RecordCellContext: %v", err)
	}

	autoHandoffPass(st, j, nil, logicLive(j), time.Now())
	// Body amended at the review (50-contract.md, autoHandoffPass row): the
	// cap is a streak inside one window, and the sentence says so.
	want := "kraken: auto-handoff gave up|logic-ah: context back at ≥75% after 3 handoffs each within 2h of the last — look at the pane"
	got := notes.list()
	if len(got) != 1 || got[0] != want {
		t.Fatalf("notifications = %q, want exactly [%q]", got, want)
	}
	s, ok := logicReadState(t, st, j.ID)
	if !ok || !s.Exhausted || s.Fires != 3 {
		t.Errorf("state = %+v (present=%v), want persisted with Exhausted true and Fires 3", s, ok)
	}

	autoHandoffPass(st, j, nil, logicLive(j), time.Now())
	if got := notes.list(); len(got) != 1 {
		t.Errorf("notifications after a second pass = %q, want still exactly one", got)
	}
}
