package jobs

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"kraken/internal/core"
)

// Edge-dimension tests for the Jev dialog answerer (slice B of
// 50-contract.md), authored blind. Every package-level identifier is
// prefixed `edge`.

// ---- JevDirective ----

func edgeJevHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TYPESAFE_API_KEY", "")
	t.Setenv("TYPESAFE_MODEL", "")
	return home
}

func TestJevEdgeDirectivePath(t *testing.T) {
	home := edgeJevHome(t)
	if got, want := jevDirectivePath(), filepath.Join(home, ".kraken", "jev-directive.md"); got != want {
		t.Fatalf("jevDirectivePath = %q, want %q", got, want)
	}
}

func TestJevEdgeDirectiveFallbacks(t *testing.T) {
	home := edgeJevHome(t)
	if got := JevDirective(); got != jevDefaultDirective {
		t.Fatalf("missing file: got %q, want the default directive", got)
	}
	if err := os.MkdirAll(filepath.Join(home, ".kraken"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(home, ".kraken", "jev-directive.md")
	for _, blank := range []string{"", "   ", "\n\n\t \n"} {
		if err := os.WriteFile(p, []byte(blank), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := JevDirective(); got != jevDefaultDirective {
			t.Errorf("blank file %q: got %q, want the default directive", blank, got)
		}
	}
	if err := os.WriteFile(p, []byte("\n\n  edge custom directive\nline two  \n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, want := JevDirective(), "edge custom directive\nline two"; got != want {
		t.Errorf("custom file: got %q, want trimmed %q", got, want)
	}
}

// ---- jevVetoed ----

func TestJevEdgeVetoWordBoundaries(t *testing.T) {
	vetoed := []string{
		"No, exit",
		"Yes, use extra usage",
		"EXIT",
		"Quit",
		"exit.",
		"(exit)",
		"Reset to default",
		"Yes, use credits",
		"Upgrade your plan",
		"Purchase more",
		"Delete this file",
		"Remove the worktree",
	}
	for _, o := range vetoed {
		if !jevVetoed(o) {
			t.Errorf("jevVetoed(%q) = false, want true", o)
		}
	}
	clear := []string{
		"Yes, I trust this folder",
		"Dark mode",
		"Continue",
		"exited",
		"remover",
		"The process exited cleanly",
		"Use the remover tool",
		"deleted",
		"quitting",
		"",
	}
	for _, o := range clear {
		if jevVetoed(o) {
			t.Errorf("jevVetoed(%q) = true, want false (word-boundary match)", o)
		}
	}
}

// ---- jevBuildRequest ----

type edgeWireReq struct {
	Model     string                    `json:"model"`
	State     map[string]any            `json:"state"`
	Questions map[string]map[string]any `json:"questions"`
}

func edgeDecodeReq(t *testing.T, req core.SystemOneRequest) edgeWireReq {
	t.Helper()
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	var w edgeWireReq
	if err := json.Unmarshal(b, &w); err != nil {
		t.Fatalf("unmarshal request %s: %v", b, err)
	}
	return w
}

func edgeCriteriaKeys(t *testing.T, w edgeWireReq) []string {
	t.Helper()
	pick, ok := w.Questions["pick"]
	if !ok {
		t.Fatalf("no pick question: %+v", w.Questions)
	}
	crit, ok := pick["criteria"].(map[string]any)
	if !ok {
		t.Fatalf("pick criteria is not an object: %#v", pick["criteria"])
	}
	keys := make([]string, 0, len(crit))
	for k := range crit {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func edgeDialog(texts ...string) core.Dialog {
	d := core.Dialog{Header: "edge header", Cursor: 0}
	for i, s := range texts {
		d.Options = append(d.Options, core.DialogOption{Text: s, Line: i, Cursor: i == 0})
	}
	return d
}

func TestJevEdgeBuildRequestVetoedKeysOmitted(t *testing.T) {
	d := edgeDialog("No, exit", "Yes, I trust this folder")
	w := edgeDecodeReq(t, jevBuildRequest("dir", "edge-slug", "claude", "tail", d, "edge-model"))
	if got, want := edgeCriteriaKeys(t, w), []string{jevEscalateKey, "opt2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("criteria keys = %q, want %q (vetoed opt1 omitted, pane position kept)", got, want)
	}
	if len(w.Questions) != 2 {
		t.Errorf("want exactly two questions, got %d: %v", len(w.Questions), w.Questions)
	}
	// State's dialog options are the option texts in pane order — all of them.
	dlg, _ := w.State["dialog"].(map[string]any)
	opts, _ := dlg["options"].([]any)
	if len(opts) != 2 || opts[0] != "No, exit" || opts[1] != "Yes, I trust this folder" {
		t.Errorf("state.dialog.options = %#v", dlg["options"])
	}
}

func TestJevEdgeBuildRequestAllVetoed(t *testing.T) {
	d := edgeDialog("No, exit", "Quit", "Yes, use extra usage")
	w := edgeDecodeReq(t, jevBuildRequest("dir", "s", "claude", "", d, "m"))
	if got, want := edgeCriteriaKeys(t, w), []string{jevEscalateKey}; !reflect.DeepEqual(got, want) {
		t.Fatalf("criteria keys = %q, want only %q", got, want)
	}
}

func TestJevEdgeBuildRequestNoOptions(t *testing.T) {
	w := edgeDecodeReq(t, jevBuildRequest("dir", "s", "codex", "", core.Dialog{Cursor: -1}, "m"))
	if got, want := edgeCriteriaKeys(t, w), []string{jevEscalateKey}; !reflect.DeepEqual(got, want) {
		t.Fatalf("criteria keys = %q, want only %q", got, want)
	}
	if _, ok := w.Questions["is_dialog"]; !ok {
		t.Errorf("is_dialog question missing")
	}
}

func TestJevEdgeBuildRequestKeysArePanePositionNotNumber(t *testing.T) {
	d := core.Dialog{Cursor: 0, Options: []core.DialogOption{
		{Text: "first", Number: 5, Cursor: true},
		{Text: "second", Number: 9},
	}}
	w := edgeDecodeReq(t, jevBuildRequest("dir", "s", "claude", "", d, "m"))
	if got, want := edgeCriteriaKeys(t, w), []string{jevEscalateKey, "opt1", "opt2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("criteria keys = %q, want %q", got, want)
	}
}

func TestJevEdgeBuildRequestPure(t *testing.T) {
	d := edgeDialog("Yes", "No, exit")
	before := edgeDialog("Yes", "No, exit")
	a := jevBuildRequest("dir", "s", "claude", "t", d, "m")
	b := jevBuildRequest("dir", "s", "claude", "t", d, "m")
	if !reflect.DeepEqual(d, before) {
		t.Errorf("jevBuildRequest mutated the dialog")
	}
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if string(ja) != string(jb) {
		t.Errorf("two identical calls differ:\n%s\n%s", ja, jb)
	}
}

// ---- jevDecide ----

func edgeResp(isDialog float64, choice string, conf float64) core.SystemOneResponse {
	return core.SystemOneResponse{Answers: map[string]core.SystemOneAnswer{
		"is_dialog": {Type: "noul", Noul: isDialog},
		"pick":      {Type: "choice", Choice: choice, Confidence: conf},
	}}
}

func TestJevEdgeDecideReasons(t *testing.T) {
	three := edgeDialog("Yes, continue", "Dark mode", "Light mode")
	vet := edgeDialog("No, exit", "Yes, I trust this folder")
	missingPick := core.SystemOneResponse{Answers: map[string]core.SystemOneAnswer{"is_dialog": {Type: "noul", Noul: 0.95}}}
	missingDialog := core.SystemOneResponse{Answers: map[string]core.SystemOneAnswer{"pick": {Type: "choice", Choice: "opt1", Confidence: 0.95}}}
	cases := []struct {
		name     string
		d        core.Dialog
		resp     core.SystemOneResponse
		wantPick int
		reason   string
	}{
		{"floors met exactly", three, edgeResp(jevMinDialog, "opt3", jevMinConfidence), 2, ""},
		{"not a dialog just under floor", three, edgeResp(0.69, "opt1", 0.99), -1, "not-a-dialog"},
		{"not a dialog zero", three, edgeResp(0, "opt1", 0.99), -1, "not-a-dialog"},
		{"model escalated", three, edgeResp(0.95, jevEscalateKey, 0.99), -1, "model-escalated"},
		{"low confidence just under", three, edgeResp(0.95, "opt1", 0.59), -1, "low-confidence"},
		{"vetoed", vet, edgeResp(0.95, "opt1", 0.99), -1, "vetoed"},
		{"opt0", three, edgeResp(0.95, "opt0", 0.99), -1, "bad-answer"},
		{"opt past end", three, edgeResp(0.95, "opt4", 0.99), -1, "bad-answer"},
		{"opt negative", three, edgeResp(0.95, "opt-1", 0.99), -1, "bad-answer"},
		{"opt non-numeric", three, edgeResp(0.95, "optx", 0.99), -1, "bad-answer"},
		{"bare number", three, edgeResp(0.95, "1", 0.99), -1, "bad-answer"},
		{"empty choice", three, edgeResp(0.95, "", 0.99), -1, "bad-answer"},
		{"nil answers", three, core.SystemOneResponse{}, -1, "bad-answer"},
		{"missing pick", three, missingPick, -1, "bad-answer"},
		{"missing is_dialog", three, missingDialog, -1, "bad-answer"},
		{"no options", core.Dialog{Cursor: -1}, edgeResp(0.95, "opt1", 0.99), -1, "bad-answer"},
	}
	for _, c := range cases {
		pick, reason := jevDecide(c.d, c.resp)
		if pick != c.wantPick || reason != c.reason {
			t.Errorf("%s: jevDecide = (%d, %q), want (%d, %q)", c.name, pick, reason, c.wantPick, c.reason)
		}
	}
}

// ---- jevLogLine ----

func edgeDecision() jevDecision {
	return jevDecision{
		Slug:          "edgeslugQ",
		Pane:          "%977",
		Header:        "edgeheaderH",
		Options:       []string{"edgeoptA", "edgeoptB"},
		Vetoed:        []bool{false, true},
		Choice:        "edgechoiceZ",
		Confidence:    0.456,
		Probabilities: map[string]float64{"opt1": 0.25},
		IsDialog:      0.789,
		Model:         "edgemodelM",
		Keys:          []string{"Down", "Enter"},
		Action:        "escalated",
		Reason:        "edgereasonR",
		PaneTail:      "edgetailT",
	}
}

func TestJevEdgeLogLineOrder(t *testing.T) {
	line := jevLogLine(edgeDecision())
	order := []string{"edgeslugQ", "%977", "escalated", "edgereasonR", "edgechoiceZ", "0.46", "0.79", "edgemodelM", "Down Enter", "edgeheaderH", "edgeoptA", "edgeoptB", "opt1=0.25", "tail=", "edgetailT"}
	rest := line
	for _, s := range order {
		i := strings.Index(rest, s)
		if i < 0 {
			t.Fatalf("log line missing %q (in order) — line: %q", s, line)
		}
		rest = rest[i+len(s):]
	}
	if strings.Count(line, "✗") != 1 {
		t.Errorf("want exactly one ✗ (one vetoed option), line: %q", line)
	}
	a, x, b := strings.Index(line, "edgeoptA"), strings.Index(line, "✗"), strings.Index(line, "edgeoptB")
	if !(a < x && x < b) {
		t.Errorf("✗ must prefix the vetoed option only: %q", line)
	}
}

func TestJevEdgeLogLineNoVetoMark(t *testing.T) {
	dec := edgeDecision()
	dec.Vetoed = []bool{false, false}
	if line := jevLogLine(dec); strings.Contains(line, "✗") {
		t.Errorf("no option vetoed, yet ✗ present: %q", line)
	}
}

func TestJevEdgeLogLineNoRawNewlines(t *testing.T) {
	dec := edgeDecision()
	dec.PaneTail = "edgel1\nedgel2\n\nedgel3\n"
	dec.Header = "edgeh1\nedgeh2"
	line := jevLogLine(dec)
	if strings.Contains(line, "\n") {
		t.Fatalf("raw newline in log line: %q", line)
	}
	if !strings.Contains(line, "edgel1⏎edgel2") {
		t.Errorf("pane tail newlines must render as ⏎: %q", line)
	}
}

func TestJevEdgeLogLineHeaderTruncated(t *testing.T) {
	dec := edgeDecision()
	dec.Header = strings.Repeat("h", 500)
	line := jevLogLine(dec)
	if !strings.Contains(line, core.TruncateOneLine(dec.Header, 200)) {
		t.Errorf("header not rendered via TruncateOneLine(…, 200)")
	}
	if strings.Contains(line, strings.Repeat("h", 201)) {
		t.Errorf("header longer than 200 runes reached the line")
	}
}

func TestJevEdgeLogLineTruncatesTailOnly(t *testing.T) {
	dec := edgeDecision()
	dec.PaneTail = strings.Repeat("ж", 20000) + "\n" + strings.Repeat("q", 5000)
	line := jevLogLine(dec)
	if n := utf8.RuneCountInString(line); n > 8000 {
		t.Fatalf("log line is %d runes, want at most 8000", n)
	}
	if !utf8.ValidString(line) {
		t.Errorf("truncation cut a rune")
	}
	if strings.Contains(line, "\n") {
		t.Errorf("raw newline in truncated line")
	}
	rest := line
	for _, s := range []string{"edgeslugQ", "%977", "escalated", "edgereasonR", "edgechoiceZ", "0.46", "0.79", "edgemodelM", "Down Enter", "edgeheaderH", "edgeoptA", "edgeoptB", "opt1=0.25", "tail="} {
		i := strings.Index(rest, s)
		if i < 0 {
			t.Fatalf("truncation dropped field %q — fields before the tail must survive", s)
		}
		rest = rest[i+len(s):]
	}
}

func TestJevEdgeLogLineShortTailKeptWhole(t *testing.T) {
	dec := edgeDecision()
	dec.PaneTail = strings.Repeat("t", 3000)
	if line := jevLogLine(dec); !strings.Contains(line, dec.PaneTail) {
		t.Errorf("a tail that fits must not be shortened")
	}
}

// ---- jevPass ----

// edgeLoginTail is the login picker's shape with its header reworded: the
// real header says "Select login method", and since the contract's header
// veto escalates a login dialog on sight, the suite's generic dialog must not
// carry that word or every pass test would stop at header-vetoed.
const edgeLoginTail = ` Claude Code can be used with your Claude subscription or billed based on API usage through your Console account.
 Select account method:
 ❯ 1. Claude account with subscription · Pro, Max, Team, or Enterprise
   2. Anthropic Console account · API usage billing
   3. 3rd-party platform · Amazon Bedrock, Microsoft Foundry, Google Vertex AI`

const edgeTrustTail = " Do you trust the files in this folder?\n\n ❯ No, exit\n   Yes, I trust this folder\n"

const edgeCodexTail = "Do you trust the contents of this directory?\n\n› 1. Yes, continue\n  2. No, quit\n"

type edgeSent struct {
	pane string
	keys []string
}

type edgeLog struct{ level, event, msg string }

type edgeHarness struct {
	home         string
	tails        map[string]string
	tailCalls    []string
	tailN        []int
	sent         []edgeSent
	sysCalls     int
	resp         core.SystemOneResponse
	sysErr       error
	logs         []edgeLog
	pending      bool
	pendingPaths []string
	client       *core.TypeSafeClient
}

func (h *edgeHarness) logf(level, event, msg string) {
	h.logs = append(h.logs, edgeLog{level, event, msg})
}

func (h *edgeHarness) escalations() []edgeLog {
	var out []edgeLog
	for _, l := range h.logs {
		if l.event == "jev-escalate" {
			out = append(out, l)
		}
	}
	return out
}

func edgeSetupPass(t *testing.T) *edgeHarness {
	t.Helper()
	h := &edgeHarness{tails: map[string]string{}}
	h.home = edgeJevHome(t)
	h.client = core.NewTypeSafeClient(core.TypeSafeConfig{APIKey: "edge-key", Model: "edge-model"})
	h.resp = edgeResp(0.95, "opt2", 0.9)

	oldSend, oldTail, oldSys, oldLoad, oldPending := jevSendKeys, jevPaneTail, jevSystemOne, jevLoadClient, jevPendingQuestion
	t.Cleanup(func() {
		jevSendKeys, jevPaneTail, jevSystemOne, jevLoadClient, jevPendingQuestion = oldSend, oldTail, oldSys, oldLoad, oldPending
	})
	jevSendKeys = func(pane string, keys []string) error {
		h.sent = append(h.sent, edgeSent{pane, append([]string(nil), keys...)})
		return nil
	}
	jevPaneTail = func(pane string, n int) string {
		h.tailCalls = append(h.tailCalls, pane)
		h.tailN = append(h.tailN, n)
		return h.tails[pane]
	}
	jevSystemOne = func(c *core.TypeSafeClient, req core.SystemOneRequest) (core.SystemOneResponse, error) {
		h.sysCalls++
		if h.sysErr != nil {
			return core.SystemOneResponse{}, h.sysErr
		}
		return h.resp, nil
	}
	jevLoadClient = func() *core.TypeSafeClient { return h.client }
	jevPendingQuestion = func(transcript string) bool {
		h.pendingPaths = append(h.pendingPaths, transcript)
		return h.pending
	}
	return h
}

func edgeJevJob() *Job {
	return &Job{Slug: "edge-jev-job", ID: 4242, State: "working"}
}

func edgeLive(j *Job) map[string]bool { return map[string]bool{CellSession(j): true} }

func edgeZeus(j *Job, pane string) *core.Peer {
	return &core.Peer{Pane: pane, TmuxSession: CellSession(j), Agent: "claude"}
}

var edgeT0 = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

func (h *edgeHarness) pass(s *jevState, j *Job, all []*core.Peer, live, threads map[string]bool, now time.Time) {
	s.jevPass(j, all, live, threads, now, h.logf)
}

func TestJevEdgePassFeatureOff(t *testing.T) {
	h := edgeSetupPass(t)
	h.client = nil
	j := edgeJevJob()
	h.tails["%11"] = edgeLoginTail
	h.pass(newJevState(), j, []*core.Peer{edgeZeus(j, "%11")}, edgeLive(j), nil, edgeT0)
	if len(h.tailCalls)+len(h.sent)+h.sysCalls+len(h.logs) != 0 {
		t.Fatalf("feature off must do nothing: tails=%v sent=%v sys=%d logs=%v", h.tailCalls, h.sent, h.sysCalls, h.logs)
	}
}

func TestJevEdgePassNoLiveCell(t *testing.T) {
	h := edgeSetupPass(t)
	j := edgeJevJob()
	h.tails["%11"] = edgeLoginTail
	for _, live := range []map[string]bool{nil, {}, {"cell-some-other-1": true}, {CellSession(j): false}} {
		h.pass(newJevState(), j, []*core.Peer{edgeZeus(j, "%11")}, live, nil, edgeT0)
	}
	if len(h.tailCalls)+len(h.sent)+h.sysCalls+len(h.logs) != 0 {
		t.Fatalf("no live cell must do nothing: tails=%v sent=%v sys=%d logs=%v", h.tailCalls, h.sent, h.sysCalls, h.logs)
	}
}

func TestJevEdgePassDoneJob(t *testing.T) {
	h := edgeSetupPass(t)
	j := edgeJevJob()
	j.State = "done"
	h.tails["%11"] = edgeLoginTail
	h.pass(newJevState(), j, []*core.Peer{edgeZeus(j, "%11")}, edgeLive(j), nil, edgeT0)
	if len(h.tailCalls)+len(h.sent)+h.sysCalls+len(h.logs) != 0 {
		t.Fatalf("done job must do nothing: tails=%v sent=%v sys=%d logs=%v", h.tailCalls, h.sent, h.sysCalls, h.logs)
	}
}

func TestJevEdgePassNonCandidatesNeverCaptured(t *testing.T) {
	h := edgeSetupPass(t)
	j := edgeJevJob()
	sess := CellSession(j)
	zeus := edgeZeus(j, "%11")
	h.tails["%11"] = "nothing to see here, just a prompt\n> "
	thread := &core.Peer{Pane: "%12", TmuxSession: sess, Agent: "claude"}
	nested := &core.Peer{Pane: "%13", TmuxSession: sess, Agent: "claude", Depth: 1}
	shell := &core.Peer{Pane: "%14", TmuxSession: sess, Agent: ""}
	other := &core.Peer{Pane: "%15", TmuxSession: sess, Agent: "shell"}
	elsewhere := &core.Peer{Pane: "%16", TmuxSession: "cell-another-job-7", Agent: "claude"}
	for _, p := range []string{"%12", "%13", "%14", "%15", "%16"} {
		h.tails[p] = edgeLoginTail
	}
	all := []*core.Peer{zeus, thread, nested, shell, other, elsewhere}
	h.pass(newJevState(), j, all, map[string]bool{sess: true, "cell-another-job-7": true}, map[string]bool{"%12": true}, edgeT0)
	for _, p := range h.tailCalls {
		if p != "%11" {
			t.Errorf("jevPaneTail called for non-candidate pane %s", p)
		}
	}
	for _, n := range h.tailN {
		if n != 25 {
			t.Errorf("jevPaneTail called with n=%d, want 25", n)
		}
	}
	if len(h.sent)+h.sysCalls+len(h.logs) != 0 {
		t.Errorf("unparseable zeus pane and no candidates: want silence, got sent=%v sys=%d logs=%v", h.sent, h.sysCalls, h.logs)
	}
}

func TestJevEdgePassUnparseableSilent(t *testing.T) {
	h := edgeSetupPass(t)
	j := edgeJevJob()
	for _, tail := range []string{"", "   \n", " Paste code here if prompted >", "❯ lonely"} {
		h.tails["%11"] = tail
		h.pass(newJevState(), j, []*core.Peer{edgeZeus(j, "%11")}, edgeLive(j), nil, edgeT0)
	}
	if len(h.sent)+h.sysCalls+len(h.logs) != 0 {
		t.Fatalf("unparseable panes must be skipped silently: sent=%v sys=%d logs=%v", h.sent, h.sysCalls, h.logs)
	}
}

func (h *edgeHarness) writeTranscript(t *testing.T, sid string) string {
	t.Helper()
	dir := filepath.Join(h.home, ".claude", "projects", "-edge-proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, sid+".jsonl")
	if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestJevEdgePassAskUserQuestionGuard(t *testing.T) {
	h := edgeSetupPass(t)
	j := edgeJevJob()
	zeus := edgeZeus(j, "%11")
	zeus.SessionID = "edge-sid-aaaa"
	path := h.writeTranscript(t, zeus.SessionID)
	h.pending = true
	h.tails["%11"] = edgeLoginTail
	h.pass(newJevState(), j, []*core.Peer{zeus}, edgeLive(j), nil, edgeT0)
	if len(h.sent) != 0 {
		t.Fatalf("AskUserQuestion pending: nothing may be typed, sent %v", h.sent)
	}
	if h.sysCalls != 0 {
		t.Errorf("the guard escalates before the API call; SystemOne called %d times", h.sysCalls)
	}
	if !reflect.DeepEqual(h.pendingPaths, []string{path}) {
		t.Errorf("jevPendingQuestion paths = %v, want [%s]", h.pendingPaths, path)
	}
	esc := h.escalations()
	if len(esc) != 1 || esc[0].level != "error" || !strings.Contains(esc[0].msg, "askuserquestion") {
		t.Fatalf("want one error-level jev-escalate naming askuserquestion, got %v", h.logs)
	}
}

func TestJevEdgePassTeammateNoTranscript(t *testing.T) {
	for _, tc := range []struct {
		name    string
		sid     string
		guessed bool
	}{
		{"no session id", "", false},
		// a guessed id is looked up like any other under the amended
		// contract (it can only make the check escalate), so the guessed
		// case is "no transcript" only when no file exists for that id
		{"guessed session id, no file on disk", "edge-sid-guess", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := edgeSetupPass(t)
			j := edgeJevJob()
			zeus := edgeZeus(j, "%11")
			h.tails["%11"] = "plain zeus output"
			mate := &core.Peer{Pane: "%12", TmuxSession: CellSession(j), Agent: "claude", SessionID: tc.sid, SessionIDGuessed: tc.guessed}
			h.tails["%12"] = edgeLoginTail
			h.pass(newJevState(), j, []*core.Peer{zeus, mate}, edgeLive(j), nil, edgeT0)
			if len(h.sent) != 0 {
				t.Fatalf("teammate without a usable transcript: nothing may be typed, sent %v", h.sent)
			}
			if h.sysCalls != 0 {
				t.Errorf("the guard escalates before the API call; SystemOne called %d times", h.sysCalls)
			}
			esc := h.escalations()
			if len(esc) != 1 || esc[0].level != "error" || !strings.Contains(esc[0].msg, "no-transcript") || !strings.Contains(esc[0].msg, "%12") {
				t.Fatalf("want one error-level jev-escalate naming no-transcript on %%12, got %v", h.logs)
			}
		})
	}
}

func TestJevEdgePassStartingZeusAllowedThrough(t *testing.T) {
	h := edgeSetupPass(t)
	j := edgeJevJob()
	h.tails["%11"] = edgeLoginTail
	h.pass(newJevState(), j, []*core.Peer{edgeZeus(j, "%11")}, edgeLive(j), nil, edgeT0)
	if len(h.escalations()) != 0 {
		t.Fatalf("zeus with no session id yet must not escalate: %v", h.logs)
	}
	if len(h.sent) != 1 || h.sent[0].pane != "%11" || !reflect.DeepEqual(h.sent[0].keys, []string{"Down", "Enter"}) {
		t.Fatalf("sent = %+v, want Down Enter into %%11", h.sent)
	}
}

func TestJevEdgePassRetryWindowSilent(t *testing.T) {
	h := edgeSetupPass(t)
	j := edgeJevJob()
	s := newJevState()
	h.tails["%11"] = edgeLoginTail
	all := []*core.Peer{edgeZeus(j, "%11")}
	h.pass(s, j, all, edgeLive(j), nil, edgeT0)
	h.pass(s, j, all, edgeLive(j), nil, edgeT0.Add(10*time.Second))
	h.pass(s, j, all, edgeLive(j), nil, edgeT0.Add(jevRetryAfter-time.Second))
	if len(h.sent) != 1 {
		t.Fatalf("typed %d times inside jevRetryAfter, want 1", len(h.sent))
	}
	if h.sysCalls != 1 {
		t.Errorf("a pair inside jevRetryAfter is skipped silently; SystemOne called %d times", h.sysCalls)
	}
	if len(h.escalations()) != 0 {
		t.Errorf("retry-window skip must not log an escalation: %v", h.logs)
	}
}

func TestJevEdgePassStillShowing(t *testing.T) {
	h := edgeSetupPass(t)
	j := edgeJevJob()
	s := newJevState()
	h.tails["%11"] = edgeLoginTail
	all := []*core.Peer{edgeZeus(j, "%11")}
	step := jevRetryAfter + time.Second
	for i := 0; i < 5; i++ {
		h.pass(s, j, all, edgeLive(j), nil, edgeT0.Add(time.Duration(i)*step))
	}
	if len(h.sent) != jevMaxAnswers {
		t.Fatalf("typed %d times, want at most jevMaxAnswers=%d", len(h.sent), jevMaxAnswers)
	}
	if h.sysCalls != jevMaxAnswers {
		t.Errorf("past jevMaxAnswers the pair escalates before the API; SystemOne called %d times", h.sysCalls)
	}
	esc := h.escalations()
	if len(esc) != 1 || !strings.Contains(esc[0].msg, "still-showing") || esc[0].level != "error" {
		t.Fatalf("want exactly one error-level still-showing escalation, got %v", h.logs)
	}
}

func TestJevEdgePassAPIError(t *testing.T) {
	h := edgeSetupPass(t)
	j := edgeJevJob()
	s := newJevState()
	h.sysErr = errors.New("edge upstream 503 boom")
	h.tails["%11"] = edgeLoginTail
	all := []*core.Peer{edgeZeus(j, "%11")}
	for i := 0; i < 3; i++ {
		h.pass(s, j, all, edgeLive(j), nil, edgeT0.Add(time.Duration(i)*(jevRetryAfter+time.Second)))
	}
	if len(h.sent) != 0 {
		t.Fatalf("api error: nothing may be typed, sent %v", h.sent)
	}
	esc := h.escalations()
	if len(esc) != 1 {
		t.Fatalf("want one escalate log per (pane, header), got %d: %v", len(esc), h.logs)
	}
	if esc[0].level != "error" || !strings.Contains(esc[0].msg, "api-error") || !strings.Contains(esc[0].msg, "edge upstream 503 boom") {
		t.Errorf("api-error escalation = %+v", esc[0])
	}
}

func TestJevEdgePassModelEscalatedAndVetoed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		tail   string
		resp   core.SystemOneResponse
		reason string
	}{
		{"model escalated", edgeLoginTail, edgeResp(0.95, jevEscalateKey, 0.99), "model-escalated"},
		{"vetoed pick", edgeTrustTail, edgeResp(0.95, "opt1", 0.99), "vetoed"},
		{"low confidence", edgeCodexTail, edgeResp(0.95, "opt1", 0.1), "low-confidence"},
		{"not a dialog", edgeLoginTail, edgeResp(0.1, "opt1", 0.99), "not-a-dialog"},
		{"bad answer", edgeLoginTail, edgeResp(0.95, "opt9", 0.99), "bad-answer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := edgeSetupPass(t)
			j := edgeJevJob()
			s := newJevState()
			h.resp = tc.resp
			h.tails["%11"] = tc.tail
			all := []*core.Peer{edgeZeus(j, "%11")}
			for i := 0; i < 3; i++ {
				h.pass(s, j, all, edgeLive(j), nil, edgeT0.Add(time.Duration(i)*(jevRetryAfter+time.Second)))
			}
			if len(h.sent) != 0 {
				t.Fatalf("escalation: nothing may be typed, sent %v", h.sent)
			}
			for _, l := range h.logs {
				if l.event == "jev-answer" {
					t.Errorf("escalation logged a jev-answer: %+v", l)
				}
			}
			esc := h.escalations()
			if len(esc) != 1 {
				t.Fatalf("want one escalate log per (pane, header), got %d: %v", len(esc), h.logs)
			}
			if esc[0].level != "error" || !strings.Contains(esc[0].msg, tc.reason) {
				t.Errorf("escalation = %+v, want error level naming %q", esc[0], tc.reason)
			}
		})
	}
}

func TestJevEdgePassEscalateLogPerHeader(t *testing.T) {
	h := edgeSetupPass(t)
	j := edgeJevJob()
	s := newJevState()
	h.resp = edgeResp(0.95, jevEscalateKey, 0.99)
	all := []*core.Peer{edgeZeus(j, "%11")}
	h.tails["%11"] = edgeLoginTail
	h.pass(s, j, all, edgeLive(j), nil, edgeT0)
	h.tails["%11"] = edgeCodexTail
	h.pass(s, j, all, edgeLive(j), nil, edgeT0.Add(time.Second))
	h.tails["%11"] = edgeLoginTail
	h.pass(s, j, all, edgeLive(j), nil, edgeT0.Add(2*time.Minute))
	if n := len(h.escalations()); n != 2 {
		t.Fatalf("two distinct headers on one pane: want 2 escalate logs, got %d: %v", n, h.logs)
	}
}

// ---- r3: amended-contract pins ----

func edgeRollout(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "rollout-2026-10-30T12-00-00-edge.jsonl")
	if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func edgeCodexPeer(j *Job, pane, rollout string) *core.Peer {
	return &core.Peer{Pane: pane, TmuxSession: CellSession(j), Agent: "codex", RolloutPath: rollout}
}

func TestJevEdgePassCodexZeusPendingQuestion(t *testing.T) {
	h := edgeSetupPass(t)
	j := edgeJevJob()
	rollout := edgeRollout(t)
	zeus := edgeCodexPeer(j, "%21", rollout)
	h.pending = true
	h.tails["%21"] = edgeCodexTail
	h.pass(newJevState(), j, []*core.Peer{zeus}, edgeLive(j), nil, edgeT0)
	if len(h.sent) != 0 {
		t.Fatalf("codex request_user_input pending: nothing may be typed, sent %v", h.sent)
	}
	if h.sysCalls != 0 {
		t.Errorf("the guard escalates before the API call; SystemOne called %d times", h.sysCalls)
	}
	if !reflect.DeepEqual(h.pendingPaths, []string{rollout}) {
		t.Errorf("jevPendingQuestion paths = %v, want [%s] (the codex RolloutPath)", h.pendingPaths, rollout)
	}
	esc := h.escalations()
	if len(esc) != 1 || esc[0].level != "error" || !strings.Contains(esc[0].msg, "askuserquestion") {
		t.Fatalf("want one error-level jev-escalate naming askuserquestion, got %v", h.logs)
	}
}

func TestJevEdgePassCodexZeusNoRolloutAllowedThrough(t *testing.T) {
	h := edgeSetupPass(t)
	j := edgeJevJob()
	zeus := edgeCodexPeer(j, "%21", "")
	h.pending = true // must not matter: there is no transcript to ask about
	h.resp = edgeResp(0.95, "opt1", 0.9)
	h.tails["%21"] = edgeCodexTail
	h.pass(newJevState(), j, []*core.Peer{zeus}, edgeLive(j), nil, edgeT0)
	if len(h.escalations()) != 0 {
		t.Fatalf("a starting codex zeus (empty RolloutPath) must be allowed through, got %v", h.logs)
	}
	if len(h.pendingPaths) != 0 {
		t.Errorf("jevPendingQuestion called with %v for a peer with no transcript", h.pendingPaths)
	}
	if h.sysCalls != 1 {
		t.Errorf("SystemOne called %d times, want 1", h.sysCalls)
	}
	if len(h.sent) != 1 || h.sent[0].pane != "%21" || !reflect.DeepEqual(h.sent[0].keys, []string{"Enter"}) {
		t.Fatalf("sent = %+v, want Enter into %%21 (opt1 is the cursor line)", h.sent)
	}
}

func TestJevEdgePassCodexTeammateNoRollout(t *testing.T) {
	h := edgeSetupPass(t)
	j := edgeJevJob()
	zeus := edgeCodexPeer(j, "%21", edgeRollout(t))
	h.tails["%21"] = "plain codex output"
	mate := edgeCodexPeer(j, "%22", "")
	h.tails["%22"] = edgeCodexTail
	h.pass(newJevState(), j, []*core.Peer{zeus, mate}, edgeLive(j), nil, edgeT0)
	if len(h.sent) != 0 || h.sysCalls != 0 {
		t.Fatalf("codex non-zeus with no rollout: sent=%v sys=%d, want nothing", h.sent, h.sysCalls)
	}
	esc := h.escalations()
	if len(esc) != 1 || esc[0].level != "error" || !strings.Contains(esc[0].msg, "no-transcript") || !strings.Contains(esc[0].msg, "%22") {
		t.Fatalf("want one error-level jev-escalate naming no-transcript on %%22, got %v", h.logs)
	}
}

func TestJevEdgePassClaudeSessionFileMissing(t *testing.T) {
	t.Run("non-zeus escalates no-transcript", func(t *testing.T) {
		h := edgeSetupPass(t)
		j := edgeJevJob()
		zeus := edgeZeus(j, "%11")
		h.tails["%11"] = "plain zeus output"
		mate := &core.Peer{Pane: "%12", TmuxSession: CellSession(j), Agent: "claude", SessionID: "edge-sid-not-on-disk"}
		h.tails["%12"] = edgeLoginTail
		h.pending = true
		h.pass(newJevState(), j, []*core.Peer{zeus, mate}, edgeLive(j), nil, edgeT0)
		if len(h.sent) != 0 || h.sysCalls != 0 {
			t.Fatalf("sent=%v sys=%d, want nothing", h.sent, h.sysCalls)
		}
		if len(h.pendingPaths) != 0 {
			t.Errorf("jevPendingQuestion called with %v for an id whose file is not on disk", h.pendingPaths)
		}
		esc := h.escalations()
		if len(esc) != 1 || esc[0].level != "error" || !strings.Contains(esc[0].msg, "no-transcript") || !strings.Contains(esc[0].msg, "%12") {
			t.Fatalf("want one error-level no-transcript escalation on %%12, got %v", h.logs)
		}
	})
	t.Run("zeus allowed through", func(t *testing.T) {
		h := edgeSetupPass(t)
		j := edgeJevJob()
		zeus := edgeZeus(j, "%11")
		zeus.SessionID = "edge-sid-not-on-disk"
		h.tails["%11"] = edgeLoginTail
		h.pending = true
		h.pass(newJevState(), j, []*core.Peer{zeus}, edgeLive(j), nil, edgeT0)
		if len(h.escalations()) != 0 {
			t.Fatalf("zeus with no resolvable transcript must be allowed through, got %v", h.logs)
		}
		if len(h.sent) != 1 || !reflect.DeepEqual(h.sent[0].keys, []string{"Down", "Enter"}) {
			t.Fatalf("sent = %+v, want Down Enter", h.sent)
		}
	})
}

func TestJevEdgeVetoReset(t *testing.T) {
	for _, o := range []string{"Reset to defaults", "Reset", "reset"} {
		if !jevVetoed(o) {
			t.Errorf("jevVetoed(%q) = false, want true", o)
		}
	}
	if jevVetoed("preset") {
		t.Errorf("jevVetoed(\"preset\") = true, want false (word boundary)")
	}
}

func TestJevEdgeDecideLadderPrecedence(t *testing.T) {
	three := edgeDialog("Yes, continue", "Dark mode", "Light mode")
	vet := edgeDialog("No, exit", "Yes, I trust this folder")
	cases := []struct {
		name   string
		d      core.Dialog
		resp   core.SystemOneResponse
		reason string
	}{
		{"low is_dialog + escalate", three, edgeResp(0.1, jevEscalateKey, 0.99), "not-a-dialog"},
		{"vetoed + low confidence", vet, edgeResp(0.95, "opt1", 0.1), "vetoed"},
	}
	for _, c := range cases {
		pick, reason := jevDecide(c.d, c.resp)
		if pick != -1 || reason != c.reason {
			t.Errorf("%s: jevDecide = (%d, %q), want (-1, %q)", c.name, pick, reason, c.reason)
		}
	}
}

func TestJevEdgePassModelEscalatedNotResent(t *testing.T) {
	h := edgeSetupPass(t)
	j := edgeJevJob()
	s := newJevState()
	h.resp = edgeResp(0.95, jevEscalateKey, 0.99)
	h.tails["%11"] = edgeLoginTail
	all := []*core.Peer{edgeZeus(j, "%11")}
	h.pass(s, j, all, edgeLive(j), nil, edgeT0)
	h.resp = edgeResp(0.95, "opt2", 0.9) // even a now-confident answer must not be asked for
	h.pass(s, j, all, edgeLive(j), nil, edgeT0.Add(jevRetryAfter+time.Second))
	h.pass(s, j, all, edgeLive(j), nil, edgeT0.Add(3*(jevRetryAfter+time.Second)))
	if h.sysCalls != 1 {
		t.Fatalf("a model-escalated pair was sent to Jev %d times, want 1", h.sysCalls)
	}
	if len(h.sent) != 0 {
		t.Errorf("a model-escalated pair was typed into: %v", h.sent)
	}
	if n := len(h.escalations()); n != 1 {
		t.Errorf("want one escalate log, got %d: %v", n, h.logs)
	}
}

func TestJevEdgePassAPIErrorRetried(t *testing.T) {
	h := edgeSetupPass(t)
	j := edgeJevJob()
	s := newJevState()
	h.sysErr = errors.New("edge transient 502")
	h.tails["%11"] = edgeLoginTail
	all := []*core.Peer{edgeZeus(j, "%11")}
	h.pass(s, j, all, edgeLive(j), nil, edgeT0)
	h.sysErr = nil
	h.pass(s, j, all, edgeLive(j), nil, edgeT0.Add(10*time.Second))
	if h.sysCalls != 1 {
		t.Fatalf("an api-error pair is retried at most once per jevRetryAfter; SystemOne called %d times", h.sysCalls)
	}
	h.pass(s, j, all, edgeLive(j), nil, edgeT0.Add(jevRetryAfter+time.Second))
	if h.sysCalls != 2 {
		t.Fatalf("an api-error pair must be retried after jevRetryAfter; SystemOne called %d times", h.sysCalls)
	}
	if len(h.sent) != 1 || !reflect.DeepEqual(h.sent[0].keys, []string{"Down", "Enter"}) {
		t.Fatalf("a later success still types: sent = %+v", h.sent)
	}
}

func TestJevEdgePassAllVetoedNoAPICall(t *testing.T) {
	h := edgeSetupPass(t)
	j := edgeJevJob()
	h.tails["%11"] = " Leave this session?\n\n ❯ No, exit\n   Quit\n"
	h.pass(newJevState(), j, []*core.Peer{edgeZeus(j, "%11")}, edgeLive(j), nil, edgeT0)
	if h.sysCalls != 0 {
		t.Fatalf("all-vetoed dialog: SystemOne called %d times, want 0", h.sysCalls)
	}
	if len(h.sent) != 0 {
		t.Fatalf("all-vetoed dialog typed into: %v", h.sent)
	}
	esc := h.escalations()
	if len(esc) != 1 || esc[0].level != "error" || !strings.Contains(esc[0].msg, "vetoed") {
		t.Fatalf("want one error-level jev-escalate naming vetoed, got %v", h.logs)
	}
}

// ---- r4: sighted pins for the newest contract rows ----

// edgeDefaultPending is the jevPendingQuestion seam's DEFAULT, captured at
// package init before any test replaces it.
var edgeDefaultPending = jevPendingQuestion

func TestJevEdgeHeaderVetoed(t *testing.T) {
	for _, h := range []string{
		"Select login method:",
		"Sign in to continue",
		"Accept the terms",
		"Dangerous rm operation not ruled out",
	} {
		if !jevHeaderVetoed(h) {
			t.Errorf("jevHeaderVetoed(%q) = false, want true", h)
		}
	}
	for _, h := range []string{"Choose the text style", "preform"} {
		if jevHeaderVetoed(h) {
			t.Errorf("jevHeaderVetoed(%q) = true, want false", h)
		}
	}
}

const edgeRealLoginTail = ` Claude Code can be used with your Claude subscription or billed based on API usage through your Console account.
 Select login method:
 ❯ 1. Claude account with subscription · Pro, Max, Team, or Enterprise
   2. Anthropic Console account · API usage billing
   3. 3rd-party platform · Amazon Bedrock, Microsoft Foundry, Google Vertex AI`

func TestJevEdgePassLoginHeaderVetoed(t *testing.T) {
	h := edgeSetupPass(t)
	j := edgeJevJob()
	h.tails["%11"] = edgeRealLoginTail
	h.pass(newJevState(), j, []*core.Peer{edgeZeus(j, "%11")}, edgeLive(j), nil, edgeT0)
	if h.sysCalls != 0 {
		t.Fatalf("header-vetoed dialog: SystemOne called %d times, want 0", h.sysCalls)
	}
	if len(h.sent) != 0 {
		t.Fatalf("header-vetoed dialog typed into: %v", h.sent)
	}
	esc := h.escalations()
	if len(esc) != 1 || esc[0].level != "error" || !strings.Contains(esc[0].msg, "header-vetoed") {
		t.Fatalf("want one error-level jev-escalate naming header-vetoed, got %v", h.logs)
	}
}

func edgeWriteLines(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "edge-transcript.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func edgePlanUse(id string) string {
	return `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"` + id + `","name":"ExitPlanMode","input":{"plan":"edge plan"}}]}}`
}

func edgePlanResult(id string) string {
	return `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + id + `","content":"approved"}]}}`
}

func TestJevEdgePendingPlanApproval(t *testing.T) {
	pending := edgeWriteLines(t, `{"type":"user","message":{"role":"user","content":"go"}}`, edgePlanUse("toolu_edge_plan1"))
	if !pendingPlanApproval(pending) {
		t.Errorf("ExitPlanMode tool_use with no tool_result: want true")
	}
	answered := edgeWriteLines(t, edgePlanUse("toolu_edge_plan1"), edgePlanResult("toolu_edge_plan1"))
	if pendingPlanApproval(answered) {
		t.Errorf("ExitPlanMode answered by a matching tool_result: want false")
	}
	otherResult := edgeWriteLines(t, edgePlanUse("toolu_edge_plan1"), edgePlanResult("toolu_edge_other"))
	if !pendingPlanApproval(otherResult) {
		t.Errorf("a tool_result for a different id does not answer the plan: want true")
	}
	lastPending := edgeWriteLines(t, edgePlanUse("toolu_edge_plan1"), edgePlanResult("toolu_edge_plan1"), edgePlanUse("toolu_edge_plan2"))
	if !pendingPlanApproval(lastPending) {
		t.Errorf("the LAST ExitPlanMode is unanswered: want true")
	}
	if pendingPlanApproval(filepath.Join(t.TempDir(), "edge-missing.jsonl")) {
		t.Errorf("unreadable path: want false")
	}
	if pendingPlanApproval(t.TempDir()) {
		t.Errorf("a directory is unreadable as a transcript: want false")
	}
	// the default seam covers ExitPlanMode where FindPendingQuestion does not
	if FindPendingQuestion(pending) != nil {
		t.Fatalf("FindPendingQuestion found a question in a plan-only transcript; the fixture no longer isolates pendingPlanApproval")
	}
	if !edgeDefaultPending(pending) {
		t.Errorf("default jevPendingQuestion must be true for a pending ExitPlanMode")
	}
	if edgeDefaultPending(answered) {
		t.Errorf("default jevPendingQuestion must be false for an answered ExitPlanMode")
	}
}

const edgeThemeTail = ` Choose the text style that looks best with your terminal

     Auto (match terminal)
 ❯ ✔ Dark mode
     Light mode
`

func TestJevEdgePassMaxCallsPerPass(t *testing.T) {
	h := edgeSetupPass(t)
	j := edgeJevJob()
	sess := CellSession(j)
	h.resp = edgeResp(0.95, "opt1", 0.9)
	zeus := edgeZeus(j, "%11")
	m2 := &core.Peer{Pane: "%12", TmuxSession: sess, Agent: "claude", SessionID: "edge-sid-m2"}
	m3 := &core.Peer{Pane: "%13", TmuxSession: sess, Agent: "claude", SessionID: "edge-sid-m3"}
	h.writeTranscript(t, m2.SessionID)
	h.writeTranscript(t, m3.SessionID)
	h.tails["%11"] = edgeLoginTail
	h.tails["%12"] = edgeCodexTail
	h.tails["%13"] = edgeThemeTail
	all := []*core.Peer{zeus, m2, m3}
	s := newJevState()

	h.pass(s, j, all, edgeLive(j), nil, edgeT0)
	if jevMaxCallsPerPass != 2 {
		t.Fatalf("jevMaxCallsPerPass = %d, the contract says 2", jevMaxCallsPerPass)
	}
	if h.sysCalls != jevMaxCallsPerPass {
		t.Fatalf("three dialogs in one pass: SystemOne called %d times, want %d", h.sysCalls, jevMaxCallsPerPass)
	}
	if len(h.sent) != 2 {
		t.Fatalf("first pass typed %d times, want 2: %+v", len(h.sent), h.sent)
	}
	if len(h.escalations()) != 0 {
		t.Fatalf("the capped dialog is skipped silently, got %v", h.logs)
	}
	typed := map[string]bool{h.sent[0].pane: true, h.sent[1].pane: true}

	h.pass(s, j, all, edgeLive(j), nil, edgeT0.Add(time.Second))
	if h.sysCalls != 3 {
		t.Fatalf("next pass: SystemOne called %d times in total, want 3", h.sysCalls)
	}
	if len(h.sent) != 3 {
		t.Fatalf("next pass: typed %d times in total, want 3: %+v", len(h.sent), h.sent)
	}
	third := h.sent[2].pane
	if typed[third] {
		t.Fatalf("the next pass typed into %s again instead of the waiting pane", third)
	}
	if third != "%13" {
		t.Errorf("third typed pane = %s, want %%13 (the pane that waited)", third)
	}
	if len(h.escalations()) != 0 {
		t.Errorf("no escalation expected, got %v", h.logs)
	}
}

func TestJevEdgePassPIDGetsFreshBudget(t *testing.T) {
	h := edgeSetupPass(t)
	j := edgeJevJob()
	s := newJevState()
	h.tails["%11"] = edgeLoginTail
	step := jevRetryAfter + time.Second
	p1 := edgeZeus(j, "%11")
	p1.PID = 1
	h.pass(s, j, []*core.Peer{p1}, edgeLive(j), nil, edgeT0)
	h.pass(s, j, []*core.Peer{p1}, edgeLive(j), nil, edgeT0.Add(step))
	if len(h.sent) != 2 {
		t.Fatalf("pid 1: typed %d times, want 2", len(h.sent))
	}
	// respawn-pane keeps the pane id; the new agent has a new pid
	p2 := edgeZeus(j, "%11")
	p2.PID = 2
	h.pass(s, j, []*core.Peer{p2}, edgeLive(j), nil, edgeT0.Add(2*step))
	if len(h.escalations()) != 0 {
		t.Fatalf("a new pid must get a fresh budget, not still-showing: %v", h.logs)
	}
	if len(h.sent) != 3 || h.sent[2].pane != "%11" {
		t.Fatalf("pid 2: want a third typed answer into %%11, sent = %+v", h.sent)
	}
}

func TestJevEdgePassThreadNameNeverCaptured(t *testing.T) {
	h := edgeSetupPass(t)
	j := edgeJevJob()
	zeus := edgeZeus(j, "%11")
	h.tails["%11"] = "plain zeus output"
	thread := &core.Peer{Pane: "%12", TmuxSession: CellSession(j), Agent: "claude", Name: ThreadAgentName(j, "t1"), SessionID: "edge-sid-thread"}
	h.writeTranscript(t, thread.SessionID)
	h.tails["%12"] = edgeLoginTail
	if !hasAnyPrefix(thread.Name, threadNamePrefixes(j)) {
		t.Fatalf("fixture: %q does not carry a thread prefix of %v", thread.Name, threadNamePrefixes(j))
	}
	h.pass(newJevState(), j, []*core.Peer{zeus, thread}, edgeLive(j), nil, edgeT0)
	for _, p := range h.tailCalls {
		if p == "%12" {
			t.Fatalf("side-thread pane %%12 was captured with threadPanes nil")
		}
	}
	if len(h.sent)+h.sysCalls+len(h.logs) != 0 {
		t.Fatalf("want silence, got sent=%v sys=%d logs=%v", h.sent, h.sysCalls, h.logs)
	}
}
