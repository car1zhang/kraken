package jobs

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"kraken/internal/core"
	"kraken/internal/store"
)

// handoff_edge_test.go — the EDGE dimension of slices A and B of
// artifacts/50-contract.md (the handoff skill build), package jobs half: the
// settings merge's empty-artifacts arm, idempotency, repo SessionStart
// survival, invalid-JSON refusal and the recall strip; mergeCellStatusLine's
// leave-alone arms; RecordCellContext/ReadCellContext refusals; autoHandoffDue's
// boundaries; autoHandoffPass' no-zeus and no-op arms. Happy paths and stated
// invariants belong to the sibling blind file.
//
// Written blind: none of internal/jobs/cellctx.go or the slice-A/B edits to
// cell.go/daemon.go was read while authoring this (cell.go read at HEAD only).
//
// Every package-level identifier here is prefixed `edge`.

// --- helpers ---------------------------------------------------------------

const (
	edgeHoSlug = "edge-handoff"
	edgeHoBin  = "/opt/edge/bin/kraken"
	edgeHoArts = "/Users/edge/.kraken/cells/edge-handoff-9/artifacts"
)

// edgeHoStubArtifacts swaps the cellArtifactsForSlug seam for the test.
func edgeHoStubArtifacts(t *testing.T, dir string) {
	t.Helper()
	prev := cellArtifactsForSlug
	cellArtifactsForSlug = func(string) string { return dir }
	t.Cleanup(func() { cellArtifactsForSlug = prev })
}

// edgeHoSandbox gives the merge a throwaway HOME and no serve-addr/db flags,
// so hookCommandFlags renders "" deterministically for these tests.
func edgeHoSandbox(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KRAKEN_DB_PATH", "")
}

func edgeHoMerge(t *testing.T, existing string) map[string]any {
	t.Helper()
	out, err := mergeHookSettings([]byte(existing), edgeHoSlug, edgeHoBin)
	if err != nil {
		t.Fatalf("mergeHookSettings: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("merge output is not JSON: %v\n%s", err, out)
	}
	return parsed
}

func edgeHoHooks(t *testing.T, parsed map[string]any) map[string]any {
	t.Helper()
	h, ok := parsed["hooks"].(map[string]any)
	if !ok {
		t.Fatalf("merged settings carry no hooks object: %v", parsed)
	}
	return h
}

// edgeHoNotifyLog captures notifyFn calls.
type edgeHoNotifyLog struct {
	mu   sync.Mutex
	msgs []string
}

func (l *edgeHoNotifyLog) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.msgs)
}

func edgeHoSwapNotify(t *testing.T) *edgeHoNotifyLog {
	t.Helper()
	log := &edgeHoNotifyLog{}
	prev := notifyFn
	notifyFn = func(title, msg string) {
		log.mu.Lock()
		defer log.mu.Unlock()
		log.msgs = append(log.msgs, title+" "+msg)
	}
	t.Cleanup(func() { notifyFn = prev })
	return log
}

// --- mergeHookSettings -----------------------------------------------------

// cellArtifactsForSlug returning "" → NO SessionStart group, nothing else
// changed.
func TestEdgeHandoffMergeNoArtifactsAddsNoSessionStart(t *testing.T) {
	edgeHoSandbox(t)
	edgeHoStubArtifacts(t, "")
	parsed := edgeHoMerge(t, "")
	hooks := edgeHoHooks(t, parsed)
	if _, ok := hooks["SessionStart"]; ok {
		t.Fatalf("SessionStart present with no artifacts dir: %v", hooks["SessionStart"])
	}
	for _, ev := range []string{"PostToolUse", "PreToolUse", "StopFailure", "UserPromptSubmit"} {
		if _, ok := hooks[ev]; !ok {
			t.Errorf("existing group %s missing when the handoff group is skipped", ev)
		}
	}
	raw, _ := json.Marshal(parsed)
	if strings.Contains(string(raw), "handoff-hook") {
		t.Fatalf("output mentions handoff-hook with no artifacts dir:\n%s", raw)
	}
}

// With "" the repo's own SessionStart groups are left exactly as they were.
func TestEdgeHandoffMergeNoArtifactsLeavesRepoSessionStart(t *testing.T) {
	edgeHoSandbox(t)
	edgeHoStubArtifacts(t, "")
	existing := `{"hooks":{"SessionStart":[{"matcher":"startup","hooks":[{"type":"command","command":"repo-start.sh"}]}]}}`
	hooks := edgeHoHooks(t, edgeHoMerge(t, existing))
	got, _ := json.Marshal(hooks["SessionStart"])
	want := `[{"hooks":[{"command":"repo-start.sh","type":"command"}],"matcher":"startup"}]`
	if string(got) != want {
		t.Fatalf("repo SessionStart changed with no artifacts dir:\n got %s\nwant %s", got, want)
	}
}

// The DEFAULT seam (not stubbed) with a slug that names no job resolves to ""
// and adds no SessionStart group.
func TestEdgeHandoffMergeDefaultSeamUnknownSlug(t *testing.T) {
	useTestStore(t)
	t.Setenv("KRAKEN_DB_PATH", "")
	if got := cellArtifactsForSlug("no-such-job-edge"); got != "" {
		t.Fatalf("cellArtifactsForSlug(unknown) = %q, want \"\"", got)
	}
	out, err := mergeHookSettings(nil, "no-such-job-edge", edgeHoBin)
	if err != nil {
		t.Fatalf("mergeHookSettings: %v", err)
	}
	if bytes.Contains(out, []byte("SessionStart")) || bytes.Contains(out, []byte("handoff-hook")) {
		t.Fatalf("unknown slug produced a handoff group:\n%s", out)
	}
}

// U11: a second merge over its own output is byte-identical — from empty, and
// from a file already carrying repo groups and a foreign statusLine.
func TestEdgeHandoffMergeIdempotent(t *testing.T) {
	edgeHoSandbox(t)
	edgeHoStubArtifacts(t, edgeHoArts)
	for name, seed := range map[string]string{
		"empty": "",
		"repo groups": `{"statusLine":{"type":"command","command":"carl-line.sh"},` +
			`"hooks":{"SessionStart":[{"matcher":"startup","hooks":[{"type":"command","command":"repo-start.sh"}]}]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			first, err := mergeHookSettings([]byte(seed), edgeHoSlug, edgeHoBin)
			if err != nil {
				t.Fatalf("first merge: %v", err)
			}
			second, err := mergeHookSettings(first, edgeHoSlug, edgeHoBin)
			if err != nil {
				t.Fatalf("second merge: %v", err)
			}
			if !bytes.Equal(first, second) {
				t.Fatalf("second merge not byte-identical:\nfirst:\n%s\nsecond:\n%s", first, second)
			}
			if n := strings.Count(string(second), "handoff-hook"); n != 1 {
				t.Fatalf("handoff-hook appears %d times, want 1:\n%s", n, second)
			}
		})
	}
}

// U12: repo SessionStart groups survive in their original order with
// kraken's appended after them.
func TestEdgeHandoffMergeRepoSessionStartSurvivesInPosition(t *testing.T) {
	edgeHoSandbox(t)
	edgeHoStubArtifacts(t, edgeHoArts)
	existing := `{"hooks":{"SessionStart":[` +
		`{"matcher":"startup","hooks":[{"type":"command","command":"repo-a.sh"}]},` +
		`{"matcher":"compact","hooks":[{"type":"command","command":"repo-b.sh"}]}` +
		`]}}`
	hooks := edgeHoHooks(t, edgeHoMerge(t, existing))
	groups, ok := hooks["SessionStart"].([]any)
	if !ok || len(groups) != 3 {
		t.Fatalf("SessionStart = %v, want the two repo groups plus kraken's", hooks["SessionStart"])
	}
	cmdOf := func(g any) string {
		m, _ := g.(map[string]any)
		hs, _ := m["hooks"].([]any)
		if len(hs) == 0 {
			return ""
		}
		h, _ := hs[0].(map[string]any)
		c, _ := h["command"].(string)
		return c
	}
	if cmdOf(groups[0]) != "repo-a.sh" || cmdOf(groups[1]) != "repo-b.sh" {
		t.Fatalf("repo groups reordered or altered: %v", groups)
	}
	want := handoffHookEntry(edgeHoSlug, edgeHoBin, edgeHoArts)
	gotJSON, _ := json.Marshal(groups[2])
	wantJSON, _ := json.Marshal(want)
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("appended group = %s, want handoffHookEntry %s", gotJSON, wantJSON)
	}
}

// A repo SessionStart that is not an array is left alone (ensureHookGroup's
// rule), not clobbered.
func TestEdgeHandoffMergeNonArraySessionStartLeftAlone(t *testing.T) {
	edgeHoSandbox(t)
	edgeHoStubArtifacts(t, edgeHoArts)
	hooks := edgeHoHooks(t, edgeHoMerge(t, `{"hooks":{"SessionStart":"repo-owned-string"}}`))
	if hooks["SessionStart"] != "repo-owned-string" {
		t.Fatalf("non-array SessionStart overwritten: %v", hooks["SessionStart"])
	}
}

func TestEdgeHandoffMergeInvalidJSONRefused(t *testing.T) {
	edgeHoSandbox(t)
	edgeHoStubArtifacts(t, edgeHoArts)
	for _, bad := range []string{`{not json`, `{"hooks":`, `[1,2`} {
		if out, err := mergeHookSettings([]byte(bad), edgeHoSlug, edgeHoBin); err == nil {
			t.Fatalf("invalid JSON %q merged without error:\n%s", bad, out)
		}
	}
}

// U13: the recall strip never removes the handoff group — fed directly.
func TestEdgeHandoffRecallStripKeepsHandoffGroup(t *testing.T) {
	edgeHoSandbox(t)
	for _, slug := range []string{edgeHoSlug, "recall-hook"} {
		t.Run(slug, func(t *testing.T) {
			g := handoffHookEntry(slug, edgeHoBin, edgeHoArts)
			kept := withoutRecallHooks([]any{g})
			if len(kept) != 1 {
				t.Fatalf("withoutRecallHooks removed the handoff group for slug %q (command %q)",
					slug, handoffHookCommand(slug, edgeHoBin, edgeHoArts))
			}
		})
	}
}

// U13 through the merge: a handoff group present under the recall events'
// neighbours survives a re-merge, and the stale recall group is still replaced.
func TestEdgeHandoffMergeRecallReplaceUnaffected(t *testing.T) {
	edgeHoSandbox(t)
	edgeHoStubArtifacts(t, edgeHoArts)
	existing := `{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"/old/kraken recall-hook --serve-addr 127.0.0.1:1 ` + edgeHoSlug + `"}]}]}}`
	hooks := edgeHoHooks(t, edgeHoMerge(t, existing))
	ups, _ := hooks["UserPromptSubmit"].([]any)
	if len(ups) != 1 {
		t.Fatalf("UserPromptSubmit = %v, want exactly the one replaced recall group", ups)
	}
	ss, _ := hooks["SessionStart"].([]any)
	if len(ss) != 1 {
		t.Fatalf("SessionStart = %v, want the handoff group", hooks["SessionStart"])
	}
}

// --- mergeCellStatusLine ---------------------------------------------------

func TestEdgeCellStatusLineForeignCommandLeftAlone(t *testing.T) {
	edgeHoSandbox(t)
	parsed := map[string]any{"statusLine": map[string]any{"type": "command", "command": "carl-line.sh", "padding": 2.0}}
	want := map[string]any{"statusLine": map[string]any{"type": "command", "command": "carl-line.sh", "padding": 2.0}}
	mergeCellStatusLine(parsed, edgeHoSlug, edgeHoBin)
	if !reflect.DeepEqual(parsed, want) {
		t.Fatalf("foreign statusLine changed: %v", parsed)
	}
}

func TestEdgeCellStatusLineObjectWithoutCommandLeftAlone(t *testing.T) {
	edgeHoSandbox(t)
	parsed := map[string]any{"statusLine": map[string]any{"type": "command"}}
	mergeCellStatusLine(parsed, edgeHoSlug, edgeHoBin)
	if !reflect.DeepEqual(parsed, map[string]any{"statusLine": map[string]any{"type": "command"}}) {
		t.Fatalf("statusLine object with no command changed: %v", parsed)
	}
}

func TestEdgeCellStatusLineNonObjectLeftAlone(t *testing.T) {
	edgeHoSandbox(t)
	for name, v := range map[string]any{
		"string": "printf hi",
		"number": 3.0,
		"array":  []any{"x"},
		"bool":   true,
		"null":   nil,
	} {
		t.Run(name, func(t *testing.T) {
			parsed := map[string]any{"statusLine": v}
			mergeCellStatusLine(parsed, edgeHoSlug, edgeHoBin)
			if !reflect.DeepEqual(parsed["statusLine"], v) {
				t.Fatalf("non-object statusLine %v changed to %v", v, parsed["statusLine"])
			}
		})
	}
}

// Through the merge: a foreign statusLine survives mergeHookSettings.
func TestEdgeHandoffMergeKeepsForeignStatusLine(t *testing.T) {
	edgeHoSandbox(t)
	edgeHoStubArtifacts(t, edgeHoArts)
	parsed := edgeHoMerge(t, `{"statusLine":{"type":"command","command":"carl-line.sh"}}`)
	sl, _ := parsed["statusLine"].(map[string]any)
	if sl == nil || sl["command"] != "carl-line.sh" {
		t.Fatalf("foreign statusLine replaced by the merge: %v", parsed["statusLine"])
	}
}

// --- RecordCellContext / ReadCellContext -----------------------------------

func TestEdgeRecordCellContextNilStore(t *testing.T) {
	if err := RecordCellContext(nil, 1, "s-1", 50, 200000, 0); err == nil {
		t.Fatalf("nil store accepted")
	}
}

func TestEdgeRecordCellContextRefusals(t *testing.T) {
	st := useTestStore(t)
	cases := []struct {
		name    string
		session string
		pct     float64
	}{
		{"blank session", "", 50},
		{"whitespace session", "  \t ", 50},
		{"NaN", "s-1", math.NaN()},
		{"below zero", "s-1", -0.1},
		{"above hundred", "s-1", 100.1},
		{"+Inf", "s-1", math.Inf(1)},
		{"-Inf", "s-1", math.Inf(-1)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := RecordCellContext(st, 7, c.session, c.pct, 200000, 0); err == nil {
				t.Fatalf("RecordCellContext(%q, %v) accepted", c.session, c.pct)
			}
			if raw, _ := st.GetKV(cellCtxKVKey(7, c.session)); raw != "" {
				t.Fatalf("refused report still wrote %q", raw)
			}
			if _, ok := ReadCellContext(st, 7, c.session); ok {
				t.Fatalf("refused report readable")
			}
		})
	}
}

func TestEdgeRecordCellContextAcceptsBounds(t *testing.T) {
	st := useTestStore(t)
	for _, pct := range []float64{0, 100} {
		sess := "s-bound"
		if err := RecordCellContext(st, 8, sess, pct, 1000, 0); err != nil {
			t.Fatalf("RecordCellContext(pct=%v) = %v, want accepted", pct, err)
		}
		got, ok := ReadCellContext(st, 8, sess)
		if !ok || got.Pct != pct {
			t.Fatalf("ReadCellContext after pct=%v = %+v, %v", pct, got, ok)
		}
	}
}

func TestEdgeReadCellContextNilStore(t *testing.T) {
	if c, ok := ReadCellContext(nil, 1, "s-1"); ok || c != (CellContext{}) {
		t.Fatalf("nil store → %+v, %v; want zero, false", c, ok)
	}
}

func TestEdgeReadCellContextBadValues(t *testing.T) {
	st := useTestStore(t)
	if c, ok := ReadCellContext(st, 9, "missing"); ok || c != (CellContext{}) {
		t.Fatalf("missing key → %+v, %v", c, ok)
	}
	cases := map[string]string{
		"empty value":   "",
		"garbage":       "{not json",
		"blank session": `{"session_id":"","pct":80,"window":1000,"at":1}`,
		"json array":    `[1]`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			sess := "s-" + strings.ReplaceAll(name, " ", "-")
			if err := st.SetKV(cellCtxKVKey(9, sess), raw); err != nil {
				t.Fatal(err)
			}
			if c, ok := ReadCellContext(st, 9, sess); ok || c != (CellContext{}) {
				t.Fatalf("%s → %+v, %v; want zero, false", name, c, ok)
			}
		})
	}
}

// A report for one job/session is not read under another (keying edge).
func TestEdgeReadCellContextKeyedPerJobAndSession(t *testing.T) {
	st := useTestStore(t)
	if err := RecordCellContext(st, 10, "zeus-s", 80, 1000, 0); err != nil {
		t.Fatal(err)
	}
	if _, ok := ReadCellContext(st, 11, "zeus-s"); ok {
		t.Fatalf("job 11 read job 10's report")
	}
	if _, ok := ReadCellContext(st, 10, "teammate-s"); ok {
		t.Fatalf("another session read zeus' report")
	}
}

// --- autoHandoffDue --------------------------------------------------------

var edgeHoNow = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

func TestEdgeAutoHandoffDueThresholdBoundary(t *testing.T) {
	fire, notify, next := autoHandoffDue(CellContext{SessionID: "a", Pct: 74.999}, autoHandoffState{}, edgeHoNow)
	if fire || notify {
		t.Fatalf("74.999 → fire=%v notify=%v, want neither", fire, notify)
	}
	if !next.Armed || next.Exhausted || next.SessionID != "a" || next.Fires != 0 {
		t.Fatalf("74.999 next = %+v, want armed, not exhausted, session a, 0 fires", next)
	}

	fire, notify, next = autoHandoffDue(CellContext{SessionID: "a", Pct: 75.0}, autoHandoffState{}, edgeHoNow)
	if !fire || notify {
		t.Fatalf("75.0 → fire=%v notify=%v, want fire only", fire, notify)
	}
	if next.Fires != 1 || next.Armed || next.LastFireAt != edgeHoNow.UnixMilli() || next.SessionID != "a" {
		t.Fatalf("75.0 next = %+v", next)
	}
}

// A new session id resets a fired-and-exhausted state before anything else.
func TestEdgeAutoHandoffDueSessionChangeResets(t *testing.T) {
	old := autoHandoffState{SessionID: "old", Fires: 3, LastFireAt: edgeHoNow.UnixMilli(), Armed: false, Exhausted: true}

	fire, notify, next := autoHandoffDue(CellContext{SessionID: "new", Pct: 90}, old, edgeHoNow)
	if !fire || notify {
		t.Fatalf("new session over threshold → fire=%v notify=%v, want fire only", fire, notify)
	}
	if next.SessionID != "new" || next.Fires != 1 || next.Exhausted || next.Armed {
		t.Fatalf("next = %+v, want reset to session new then one fire", next)
	}

	fire, notify, next = autoHandoffDue(CellContext{SessionID: "new", Pct: 10}, old, edgeHoNow)
	if fire || notify {
		t.Fatalf("new session under threshold → fire=%v notify=%v", fire, notify)
	}
	want := autoHandoffState{SessionID: "new", Armed: true}
	if next != want {
		t.Fatalf("next = %+v, want %+v", next, want)
	}
}

func TestEdgeAutoHandoffDueRetryBoundary(t *testing.T) {
	fired := func(ago time.Duration) autoHandoffState {
		return autoHandoffState{SessionID: "a", Fires: 1, LastFireAt: edgeHoNow.Add(-ago).UnixMilli(), Armed: false}
	}
	c := CellContext{SessionID: "a", Pct: 80}

	s := fired(15 * time.Minute)
	fire, notify, next := autoHandoffDue(c, s, edgeHoNow)
	if !fire || notify {
		t.Fatalf("exactly 15m since fire → fire=%v notify=%v, want fire", fire, notify)
	}
	if next.Fires != 2 || next.LastFireAt != edgeHoNow.UnixMilli() || next.Armed {
		t.Fatalf("retry next = %+v", next)
	}

	s = fired(15*time.Minute - time.Second)
	fire, notify, next = autoHandoffDue(c, s, edgeHoNow)
	if fire || notify {
		t.Fatalf("14m59s since fire → fire=%v notify=%v, want neither", fire, notify)
	}
	if next != s {
		t.Fatalf("14m59s next = %+v, want the state unchanged %+v", next, s)
	}
}

// Exhaustion notifies exactly once and never fires inside the streak's
// window; a drop re-arms but keeps Exhausted (so a refill inside the window
// neither fires nor re-notifies), and a streak two hours old resets so the
// next climb fires again. Amended at the review (50-contract.md, autoHandoffDue
// rules 1b, 2 and 3): the original assertion that a drop clears Exhausted and
// a refill re-notifies was the per-session-lifetime cap the review found
// BLOCKING — it stopped the feature after a long job's third successful
// handoff and misreported every later climb.
func TestEdgeAutoHandoffDueExhaustion(t *testing.T) {
	c := CellContext{SessionID: "a", Pct: 90}
	lastFire := edgeHoNow.Add(-time.Hour)
	s := autoHandoffState{SessionID: "a", Fires: autoHandoffMaxFires, LastFireAt: lastFire.UnixMilli()}

	fire, notify, next := autoHandoffDue(c, s, edgeHoNow)
	if fire || !notify || !next.Exhausted {
		t.Fatalf("first exhausted report → fire=%v notify=%v next=%+v, want notify once, exhausted", fire, notify, next)
	}
	if next.Fires != autoHandoffMaxFires {
		t.Fatalf("exhausted report changed Fires to %d", next.Fires)
	}

	// still inside the window: silence
	fire, notify, next2 := autoHandoffDue(c, next, edgeHoNow.Add(10*time.Minute))
	if fire || notify || !next2.Exhausted {
		t.Fatalf("second exhausted report → fire=%v notify=%v next=%+v, want silence", fire, notify, next2)
	}

	// a drop inside the window re-arms and keeps Exhausted
	fire, notify, dropped := autoHandoffDue(CellContext{SessionID: "a", Pct: 40}, next2, edgeHoNow.Add(20*time.Minute))
	if fire || notify || !dropped.Exhausted || !dropped.Armed {
		t.Fatalf("drop below threshold → fire=%v notify=%v next=%+v, want armed and still exhausted", fire, notify, dropped)
	}

	// a refill inside the window: Fires is still at the cap → no fire, and
	// Exhausted was never cleared → no second notification
	fire, notify, refill := autoHandoffDue(c, dropped, edgeHoNow.Add(30*time.Minute))
	if fire || notify || !refill.Exhausted {
		t.Fatalf("refill inside the window → fire=%v notify=%v next=%+v, want silence", fire, notify, refill)
	}

	// the streak is two hours old: Fires and Exhausted reset, and the armed
	// climb fires again
	fire, notify, reset := autoHandoffDue(c, refill, lastFire.Add(autoHandoffFireWindow))
	if !fire || notify || reset.Exhausted || reset.Fires != 1 {
		t.Fatalf("climb after the window → fire=%v notify=%v next=%+v, want fire with Fires 1, not exhausted", fire, notify, reset)
	}
}

// Over the threshold, not armed and inside the retry window: rule 5 — nothing.
func TestEdgeAutoHandoffDueDisarmedInsideWindow(t *testing.T) {
	s := autoHandoffState{SessionID: "a", Fires: 1, LastFireAt: edgeHoNow.Add(-time.Minute).UnixMilli()}
	fire, notify, next := autoHandoffDue(CellContext{SessionID: "a", Pct: 100}, s, edgeHoNow)
	if fire || notify || next != s {
		t.Fatalf("→ fire=%v notify=%v next=%+v, want nothing and state unchanged", fire, notify, next)
	}
}

// --- autoHandoffPass -------------------------------------------------------

// edgeHoPassJob creates a store job with a stamped session and returns it as
// loaded by slug, plus the context seeded at pct under zeus' stamped Claude
// session id (amended contract: the report is ReadCellContext(st, j.ID,
// j.Session); liveness stays live[CellSession(j)]).
func edgeHoPassJob(t *testing.T, st *store.Store, slug string, pct float64) *Job {
	t.Helper()
	mustCreateJob(t, st, store.Job{Slug: slug, State: "working", Session: "6f1c2e0a-0000-4000-8000-00000000edge"})
	j := LoadJobBySlug(slug)
	if j == nil {
		t.Fatalf("LoadJobBySlug(%s) = nil", slug)
	}
	if err := RecordCellContext(st, j.ID, j.Session, pct, 200000, 0); err != nil {
		t.Fatalf("seed context under %q: %v", j.Session, err)
	}
	return j
}

// Amended contract: no stamped Claude session (j.Session == "") → no side
// effects, even with a live cell and a due report seeded under the tmux
// session name (the old reading's key).
func TestEdgeAutoHandoffPassEmptySessionIsNoop(t *testing.T) {
	st := useTestStore(t)
	log := edgeHoSwapNotify(t)
	mustCreateJob(t, st, store.Job{Slug: "edge-nosession", State: "working"})
	j := LoadJobBySlug("edge-nosession")
	if j == nil {
		t.Fatal("LoadJobBySlug nil")
	}
	if j.Session != "" {
		t.Fatalf("fixture job carries a session %q", j.Session)
	}
	if err := RecordCellContext(st, j.ID, CellSession(j), 95, 200000, 0); err != nil {
		t.Fatal(err)
	}
	autoHandoffPass(st, j, nil, map[string]bool{CellSession(j): true}, edgeHoNow)
	if got := edgeHoStateRaw(t, st, j.ID); got != "" {
		t.Fatalf("empty-session pass wrote state %q", got)
	}
	if n := log.count(); n != 0 {
		t.Fatalf("empty-session pass notified %d times", n)
	}
}

// Amended contract: j == nil → returns with no side effects (no panic).
func TestEdgeAutoHandoffPassNilJobNoPanic(t *testing.T) {
	st := useTestStore(t)
	log := edgeHoSwapNotify(t)
	autoHandoffPass(st, nil, nil, map[string]bool{}, edgeHoNow)
	if n := log.count(); n != 0 {
		t.Fatalf("nil job notified %d times", n)
	}
}

func edgeHoStateRaw(t *testing.T, st *store.Store, id int64) string {
	t.Helper()
	raw, err := st.GetKV(autoHandoffKVKey(id))
	if errors.Is(err, store.ErrNotFound) {
		return ""
	}
	if err != nil {
		t.Fatalf("GetKV: %v", err)
	}
	return raw
}

// A due fire with no zeus in the snapshot writes NOTHING and notifies nothing.
func TestEdgeAutoHandoffPassNoZeusLeavesStateUntouched(t *testing.T) {
	st := useTestStore(t)
	log := edgeHoSwapNotify(t)
	j := edgeHoPassJob(t, st, "edge-nozeus", 90)

	seeded := `{"session_id":"seed","fires":0,"last_fire_at":0,"armed":true,"exhausted":false}`
	if err := st.SetKV(autoHandoffKVKey(j.ID), seeded); err != nil {
		t.Fatal(err)
	}
	// a peer in some OTHER tmux session: not this cell's zeus
	other := &core.Peer{Pane: "%99", TmuxSession: "cell-somebody-else-1", SessionID: "other", Name: "zeus-somebody-else"}
	live := map[string]bool{CellSession(j): true}

	autoHandoffPass(st, j, []*core.Peer{other}, live, edgeHoNow)
	autoHandoffPass(st, j, nil, live, edgeHoNow)

	if got := edgeHoStateRaw(t, st, j.ID); got != seeded {
		t.Fatalf("state changed with no zeus:\n got %s\nwant %s", got, seeded)
	}
	if n := log.count(); n != 0 {
		t.Fatalf("notified %d times with no zeus, want 0", n)
	}
}

// Same, with NO prior state: nothing is created.
func TestEdgeAutoHandoffPassNoZeusCreatesNoState(t *testing.T) {
	st := useTestStore(t)
	log := edgeHoSwapNotify(t)
	j := edgeHoPassJob(t, st, "edge-nozeus-fresh", 90)
	autoHandoffPass(st, j, nil, map[string]bool{CellSession(j): true}, edgeHoNow)
	if got := edgeHoStateRaw(t, st, j.ID); got != "" {
		t.Fatalf("no-zeus pass wrote state %q", got)
	}
	if n := log.count(); n != 0 {
		t.Fatalf("notified %d times", n)
	}
}

func TestEdgeAutoHandoffPassNilStoreNoPanic(t *testing.T) {
	log := edgeHoSwapNotify(t)
	j := &Job{Slug: "edge-nil", ID: 1, Session: "s"}
	autoHandoffPass(nil, j, nil, map[string]bool{CellSession(j): true}, edgeHoNow)
	if n := log.count(); n != 0 {
		t.Fatalf("nil store notified %d times", n)
	}
}

// Cell not live → no side effects even with a due report.
func TestEdgeAutoHandoffPassNotLiveIsNoop(t *testing.T) {
	st := useTestStore(t)
	log := edgeHoSwapNotify(t)
	j := edgeHoPassJob(t, st, "edge-notlive", 95)
	autoHandoffPass(st, j, nil, map[string]bool{}, edgeHoNow)
	autoHandoffPass(st, j, nil, nil, edgeHoNow)
	if got := edgeHoStateRaw(t, st, j.ID); got != "" {
		t.Fatalf("not-live pass wrote state %q", got)
	}
	if n := log.count(); n != 0 {
		t.Fatalf("not-live pass notified %d times", n)
	}
}

// No report → no side effects.
func TestEdgeAutoHandoffPassNoReportIsNoop(t *testing.T) {
	st := useTestStore(t)
	log := edgeHoSwapNotify(t)
	mustCreateJob(t, st, store.Job{Slug: "edge-noreport", State: "working", Session: "zeus-claude-sess"})
	j := LoadJobBySlug("edge-noreport")
	if j == nil {
		t.Fatal("LoadJobBySlug nil")
	}
	autoHandoffPass(st, j, nil, map[string]bool{CellSession(j): true}, edgeHoNow)
	if got := edgeHoStateRaw(t, st, j.ID); got != "" {
		t.Fatalf("no-report pass wrote state %q", got)
	}
	if n := log.count(); n != 0 {
		t.Fatalf("no-report pass notified %d times", n)
	}
}
