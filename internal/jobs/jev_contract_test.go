package jobs

// Blind contract tests (athena-logic) for slice B of the Jev dialog answerer:
// written from artifacts/50-contract.md alone, before any implementation
// existed. Dimension: the stated invariants and happy paths. Boundaries,
// refusals and error paths belong to athena-edge's file.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"kraken/internal/core"
)

const contractJevTrustPane = ` Accessing workspace:
 /Users/x/.kraken/cells/hollow-kraken-609/wt-utils
 Quick safety check: Is this a project you created or one you trust? (Like your own code, a well-known open source project, or work from your team). If not, take a moment to review what's in this folder first.
 Claude Code'll be able to read, edit, and execute files here.
 Security guide
 ❯ No, exit
   Yes, I trust this folder
 Enter to confirm · Esc to cancel
`

const contractJevIdlePane = "some output\n\n> \n"

func contractJevIsolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TYPESAFE_API_KEY", "")
	t.Setenv("TYPESAFE_MODEL", "")
	return home
}

// --- directive ---------------------------------------------------------

func TestJevContractDirectivePath(t *testing.T) {
	home := contractJevIsolate(t)
	if got, want := jevDirectivePath(), filepath.Join(home, ".kraken", "jev-directive.md"); got != want {
		t.Errorf("jevDirectivePath() = %q, want %q", got, want)
	}
}

func TestJevContractDirectiveDefault(t *testing.T) {
	contractJevIsolate(t)
	if got := JevDirective(); got != jevDefaultDirective {
		t.Errorf("no file: JevDirective() = %q, want jevDefaultDirective", got)
	}
}

func TestJevContractDirectiveFromFile(t *testing.T) {
	home := contractJevIsolate(t)
	dir := filepath.Join(home, ".kraken")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "jev-directive.md"), []byte("\n  Answer every onboarding prompt.\n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := JevDirective(); got != "Answer every onboarding prompt." {
		t.Errorf("JevDirective() = %q, want the trimmed file contents", got)
	}
}

func TestJevContractDefaultDirectiveProse(t *testing.T) {
	low := strings.ToLower(jevDefaultDirective)
	for _, w := range []string{"trust", "exit", "quit", "credential", "signature", "escalate"} {
		if !strings.Contains(low, w) {
			t.Errorf("jevDefaultDirective does not mention %q", w)
		}
	}
}

// --- vetoes --------------------------------------------------------------

func TestJevContractVetoWords(t *testing.T) {
	have := map[string]bool{}
	for _, w := range jevVetoWords {
		have[w] = true
	}
	for _, w := range []string{"exit", "quit", "reset to default", "use credits", "extra usage", "upgrade", "purchase", "delete", "remove"} {
		if !have[w] {
			t.Errorf("jevVetoWords lacks %q", w)
		}
	}
}

func TestJevContractVetoed(t *testing.T) {
	cases := map[string]bool{
		"No, exit":                 true,
		"Yes, use extra usage":     true,
		"Yes, I trust this folder": false,
		"Dark mode":                false,
		"Continue":                 false,
		"The process exited":       false,
		"Use the remover tool":     false,
		"Quit":                     true,
		"Reset to default":         true,
	}
	for opt, want := range cases {
		if got := jevVetoed(opt); got != want {
			t.Errorf("jevVetoed(%q) = %v, want %v", opt, got, want)
		}
	}
}

func TestJevContractConstants(t *testing.T) {
	if jevEscalateKey != "escalate" {
		t.Errorf("jevEscalateKey = %q", jevEscalateKey)
	}
	if jevMinConfidence != 0.6 || jevMinDialog != 0.7 {
		t.Errorf("floors = %v, %v; want 0.6, 0.7", jevMinConfidence, jevMinDialog)
	}
	if jevMaxAnswers != 2 || jevRetryAfter != 60*time.Second {
		t.Errorf("jevMaxAnswers=%v jevRetryAfter=%v", jevMaxAnswers, jevRetryAfter)
	}
}

// --- request -------------------------------------------------------------

func contractJevDialog() core.Dialog {
	return core.Dialog{
		Header: "Pick something",
		Cursor: 0,
		Options: []core.DialogOption{
			{Text: "Continue", Line: 3, Cursor: true},
			{Text: "Yes, use extra usage", Line: 4},
			{Text: "Dark mode", Line: 5},
		},
	}
}

// contractJevJSON round-trips v through JSON so assertions read the wire form.
func contractJevJSON(t *testing.T, v any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	return m
}

func TestJevContractBuildRequest(t *testing.T) {
	d := contractJevDialog()
	req := jevBuildRequest("the directive", "demo-slug", "claude", "pane\ntail", d, "jev-model-x")

	if req.Model != "jev-model-x" {
		t.Errorf("Model = %q", req.Model)
	}
	st := contractJevJSON(t, req.State)
	if len(st) != 4 {
		t.Errorf("State keys = %v, want exactly directive/cell/pane_tail/dialog", st)
	}
	if st["directive"] != "the directive" {
		t.Errorf("state.directive = %v", st["directive"])
	}
	if st["pane_tail"] != "pane\ntail" {
		t.Errorf("state.pane_tail = %v", st["pane_tail"])
	}
	cell, _ := st["cell"].(map[string]any)
	if cell["slug"] != "demo-slug" || cell["agent"] != "claude" {
		t.Errorf("state.cell = %v", st["cell"])
	}
	dl, _ := st["dialog"].(map[string]any)
	if dl["header"] != "Pick something" {
		t.Errorf("state.dialog.header = %v", dl["header"])
	}
	if !reflect.DeepEqual(dl["options"], []any{"Continue", "Yes, use extra usage", "Dark mode"}) {
		t.Errorf("state.dialog.options = %v", dl["options"])
	}

	if len(req.Questions) != 2 {
		t.Fatalf("Questions = %v, want exactly is_dialog and pick", req.Questions)
	}
	isd, ok := req.Questions["is_dialog"]
	if !ok || isd.Type != "noul" {
		t.Errorf("is_dialog = %+v, want a noul question", isd)
	}
	if s, _ := isd.Instructions.(string); strings.TrimSpace(s) == "" {
		t.Errorf("is_dialog instructions empty: %v", isd.Instructions)
	}
	pick, ok := req.Questions["pick"]
	if !ok || pick.Type != "choice" {
		t.Fatalf("pick = %+v, want a choice question", pick)
	}
	crit := contractJevJSON(t, pick.Criteria)
	wantKeys := []string{"escalate", "opt1", "opt3"}
	var gotKeys []string
	for k := range crit {
		gotKeys = append(gotKeys, k)
	}
	if len(gotKeys) != len(wantKeys) {
		t.Fatalf("criteria keys = %v, want %v (vetoed opt2 omitted)", gotKeys, wantKeys)
	}
	for _, k := range wantKeys {
		if _, ok := crit[k]; !ok {
			t.Errorf("criteria lacks %q: %v", k, crit)
		}
	}
	if _, ok := crit["opt2"]; ok {
		t.Errorf("vetoed option opt2 present in criteria: %v", crit)
	}
	if crit["opt1"] != "Continue" || crit["opt3"] != "Dark mode" {
		t.Errorf("criteria descriptions = %v, want each option's text", crit)
	}
	if s, _ := crit[jevEscalateKey].(string); strings.TrimSpace(s) == "" {
		t.Errorf("escalate criterion has no description: %v", crit[jevEscalateKey])
	}
}

// --- decide --------------------------------------------------------------

func contractJevResp(isDialog float64, choice string, conf float64) core.SystemOneResponse {
	return core.SystemOneResponse{
		Model: "jev-test",
		Answers: map[string]core.SystemOneAnswer{
			"is_dialog": {Type: "noul", Noul: isDialog},
			"pick":      {Type: "choice", Choice: choice, Confidence: conf, Probabilities: map[string]float64{choice: conf}},
		},
	}
}

func TestJevContractDecide(t *testing.T) {
	d := contractJevDialog() // opt2 ("Yes, use extra usage") is vetoed
	cases := []struct {
		name       string
		resp       core.SystemOneResponse
		wantPick   int
		wantReason string
	}{
		{"accept-third", contractJevResp(0.9, "opt3", 0.8), 2, ""},
		{"accept-first", contractJevResp(0.95, "opt1", 0.99), 0, ""},
		{"floors-inclusive", contractJevResp(jevMinDialog, "opt1", jevMinConfidence), 0, ""},
		{"not-a-dialog", contractJevResp(0.5, "opt1", 0.9), -1, "not-a-dialog"},
		{"model-escalated", contractJevResp(0.9, jevEscalateKey, 0.9), -1, "model-escalated"},
		{"low-confidence", contractJevResp(0.9, "opt3", 0.3), -1, "low-confidence"},
		{"vetoed", contractJevResp(0.9, "opt2", 0.9), -1, "vetoed"},
		{"no-such-option", contractJevResp(0.9, "opt9", 0.9), -1, "bad-answer"},
		{"missing-pick", core.SystemOneResponse{Answers: map[string]core.SystemOneAnswer{
			"is_dialog": {Type: "noul", Noul: 0.9}}}, -1, "bad-answer"},
	}
	for _, c := range cases {
		pick, reason := jevDecide(d, c.resp)
		if pick != c.wantPick || reason != c.wantReason {
			t.Errorf("%s: jevDecide = (%d, %q), want (%d, %q)", c.name, pick, reason, c.wantPick, c.wantReason)
		}
	}
}

// --- log line ------------------------------------------------------------

func contractJevDecision() jevDecision {
	return jevDecision{
		Slug:          "slugzz",
		Pane:          "%93",
		Header:        "HeaderQQ",
		Options:       []string{"Continue", "No, exit"},
		Vetoed:        []bool{false, true},
		Choice:        "Continue",
		Confidence:    0.8312,
		Probabilities: map[string]float64{"opt1": 0.8312},
		IsDialog:      0.9171,
		Model:         "jev-modelww",
		Keys:          []string{"Down", "Enter"},
		Action:        "typed",
		Reason:        "reasonyy",
		PaneTail:      "line one\nline two",
	}
}

func TestJevContractLogLineOrder(t *testing.T) {
	line := jevLogLine(contractJevDecision())
	if strings.Contains(line, "\n") {
		t.Fatalf("log line has a raw newline: %q", line)
	}
	if !strings.Contains(line, "line one⏎line two") {
		t.Errorf("pane tail newline not rendered as ⏎: %q", line)
	}
	order := []string{"slugzz", "%93", "typed", "reasonyy", "Continue", "0.83", "0.92", "jev-modelww", "Down Enter", "HeaderQQ", "✗", "opt1=0.83", "tail=", "line one⏎line two"}
	pos := 0
	for _, s := range order {
		i := strings.Index(line[pos:], s)
		if i < 0 {
			t.Fatalf("%q missing (or out of order) after offset %d in %q", s, pos, line)
		}
		pos += i + len(s)
	}
	if !strings.Contains(line, "[Continue]") {
		t.Errorf("non-vetoed option not rendered as [text]: %q", line)
	}
	vi := strings.Index(line, "✗")
	if vi < 0 || !strings.Contains(line[vi:], "No, exit") || strings.Index(line, "No, exit") < vi {
		t.Errorf("vetoed option not marked with ✗ ahead of its text: %q", line)
	}
}

func TestJevContractLogLineTruncatesTailOnly(t *testing.T) {
	dec := contractJevDecision()
	dec.PaneTail = strings.Repeat("x", 20000)
	line := jevLogLine(dec)
	if n := utf8.RuneCountInString(line); n > 8000 {
		t.Fatalf("log line is %d runes, want <= 8000", n)
	}
	ti := strings.Index(line, "tail=")
	if ti < 0 {
		t.Fatalf("no tail= field: %q", line[:200])
	}
	head := line[:ti]
	for _, s := range []string{"slugzz", "%93", "typed", "reasonyy", "jev-modelww", "Down Enter", "HeaderQQ", "opt1=0.83"} {
		if !strings.Contains(head, s) {
			t.Errorf("field %q lost before tail=", s)
		}
	}
	if !strings.Contains(line[ti:], "xxxx") {
		t.Errorf("tail emptied entirely")
	}
}

// --- pass ----------------------------------------------------------------

type contractJevLog struct{ level, event, msg string }

type contractJevSend struct {
	pane string
	keys []string
}

type contractJevRig struct {
	j       *Job
	sess    string
	live    map[string]bool
	tails   map[string]string
	tailNs  []int
	pending bool
	pendArg []string
	resp    core.SystemOneResponse
	apiReqs []core.SystemOneRequest
	sends   []contractJevSend
	logs    []contractJevLog
	client  *core.TypeSafeClient
}

// contractJevSetup isolates HOME, builds a job with a live cell, and replaces
// every seam the contract names.
func contractJevSetup(t *testing.T) *contractJevRig {
	t.Helper()
	contractJevIsolate(t)
	j := &Job{Slug: "jevdemo", State: "working"}
	r := &contractJevRig{
		j:     j,
		sess:  CellSession(j),
		tails: map[string]string{},
		resp:  contractJevResp(0.95, "opt2", 0.9), // "Yes, I trust this folder"
	}
	r.live = map[string]bool{r.sess: true}
	r.client = core.NewTypeSafeClient(core.TypeSafeConfig{APIKey: "k-test", Model: "jev-latest"})

	oldLoad, oldTail, oldOne, oldSend, oldPend := jevLoadClient, jevPaneTail, jevSystemOne, jevSendKeys, jevPendingQuestion
	t.Cleanup(func() {
		jevLoadClient, jevPaneTail, jevSystemOne, jevSendKeys, jevPendingQuestion = oldLoad, oldTail, oldOne, oldSend, oldPend
	})
	jevLoadClient = func() *core.TypeSafeClient { return r.client }
	jevPaneTail = func(pane string, n int) string {
		r.tailNs = append(r.tailNs, n)
		return r.tails[pane]
	}
	jevSystemOne = func(c *core.TypeSafeClient, req core.SystemOneRequest) (core.SystemOneResponse, error) {
		r.apiReqs = append(r.apiReqs, req)
		return r.resp, nil
	}
	jevSendKeys = func(pane string, keys []string) error {
		r.sends = append(r.sends, contractJevSend{pane, append([]string(nil), keys...)})
		return nil
	}
	jevPendingQuestion = func(transcript string) bool {
		r.pendArg = append(r.pendArg, transcript)
		return r.pending
	}
	return r
}

func (r *contractJevRig) logf(level, event, msg string) {
	r.logs = append(r.logs, contractJevLog{level, event, msg})
}

func (r *contractJevRig) zeus(sid string) *core.Peer {
	return &core.Peer{Pane: "%11", TmuxSession: r.sess, Agent: "claude", Depth: 0, Name: ZeusName(r.j), SessionID: sid}
}

// contractJevTranscript creates a transcript core.TranscriptPathForSession
// resolves for sid, under the isolated HOME.
func contractJevTranscript(t *testing.T, sid string) string {
	t.Helper()
	dir := filepath.Join(os.Getenv("HOME"), ".claude", "projects", "-contract-proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, sid+".jsonl")
	if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestJevContractPassAnswersZeus(t *testing.T) {
	r := contractJevSetup(t)
	z := r.zeus("sid-zeus-1")
	tr := contractJevTranscript(t, "sid-zeus-1")
	r.tails[z.Pane] = contractJevTrustPane
	s := newJevState()
	s.jevPass(r.j, []*core.Peer{z}, r.live, map[string]bool{}, time.Unix(1_800_000_000, 0), r.logf)

	// two reads of 25 lines: the parse before the Jev call, and the re-read
	// right before typing that the contract's step (4) requires
	if len(r.tailNs) != 2 || r.tailNs[0] != 25 || r.tailNs[1] != 25 {
		t.Errorf("jevPaneTail n = %v, want two calls with 25", r.tailNs)
	}
	if !reflect.DeepEqual(r.pendArg, []string{tr}) {
		t.Errorf("jevPendingQuestion args = %v, want [%s]", r.pendArg, tr)
	}
	if len(r.apiReqs) != 1 {
		t.Fatalf("jevSystemOne calls = %d, want 1", len(r.apiReqs))
	}
	st := contractJevJSON(t, r.apiReqs[0].State)
	if cell, _ := st["cell"].(map[string]any); cell["slug"] != "jevdemo" || cell["agent"] != "claude" {
		t.Errorf("request state.cell = %v", st["cell"])
	}
	if st["directive"] != jevDefaultDirective {
		t.Errorf("request directive is not JevDirective(): %v", st["directive"])
	}
	want := []contractJevSend{{"%11", []string{"Down", "Enter"}}}
	if !reflect.DeepEqual(r.sends, want) {
		t.Fatalf("sends = %+v, want %+v", r.sends, want)
	}
	if len(r.logs) != 1 || r.logs[0].level != "info" || r.logs[0].event != "jev-answer" {
		t.Fatalf("logs = %+v, want one info jev-answer", r.logs)
	}
	if !strings.Contains(r.logs[0].msg, "Yes, I trust this folder") || !strings.Contains(r.logs[0].msg, "jevdemo") {
		t.Errorf("jev-answer line lacks slug or choice: %q", r.logs[0].msg)
	}
}

func TestJevContractPassStartingZeusAllowed(t *testing.T) {
	r := contractJevSetup(t)
	z := r.zeus("") // session file not written yet
	r.tails[z.Pane] = contractJevTrustPane
	newJevState().jevPass(r.j, []*core.Peer{z}, r.live, map[string]bool{}, time.Unix(1_800_000_000, 0), r.logf)
	if len(r.sends) != 1 || r.sends[0].pane != "%11" {
		t.Fatalf("starting zeus not answered: sends=%+v logs=%+v", r.sends, r.logs)
	}
	if len(r.pendArg) != 0 {
		t.Errorf("jevPendingQuestion called with no session id: %v", r.pendArg)
	}
}

func TestJevContractPassAskUserQuestionEscalates(t *testing.T) {
	r := contractJevSetup(t)
	z := r.zeus("sid-zeus-2")
	contractJevTranscript(t, "sid-zeus-2")
	r.tails[z.Pane] = contractJevTrustPane
	r.pending = true
	s := newJevState()
	now := time.Unix(1_800_000_000, 0)
	s.jevPass(r.j, []*core.Peer{z}, r.live, map[string]bool{}, now, r.logf)
	s.jevPass(r.j, []*core.Peer{z}, r.live, map[string]bool{}, now.Add(2*time.Minute), r.logf)

	if len(r.sends) != 0 {
		t.Errorf("typed into a pane with a pending AskUserQuestion: %+v", r.sends)
	}
	if len(r.apiReqs) != 0 {
		t.Errorf("API called despite the guard: %d calls", len(r.apiReqs))
	}
	if len(r.logs) != 1 {
		t.Fatalf("logs = %+v, want exactly one escalation across two passes", r.logs)
	}
	if l := r.logs[0]; l.level != "error" || l.event != "jev-escalate" || !strings.Contains(l.msg, "askuserquestion") {
		t.Errorf("escalation = %+v", l)
	}
}

func TestJevContractPassNonZeusNoTranscriptEscalates(t *testing.T) {
	r := contractJevSetup(t)
	z := r.zeus("sid-zeus-3")
	contractJevTranscript(t, "sid-zeus-3")
	r.tails[z.Pane] = contractJevIdlePane
	mate := &core.Peer{Pane: "%12", TmuxSession: r.sess, Agent: "claude", Depth: 0, Name: "hephaestus-x"}
	r.tails[mate.Pane] = contractJevTrustPane
	newJevState().jevPass(r.j, []*core.Peer{z, mate}, r.live, map[string]bool{}, time.Unix(1_800_000_000, 0), r.logf)

	if len(r.sends) != 0 {
		t.Errorf("typed into a teammate pane: %+v", r.sends)
	}
	if len(r.logs) != 1 {
		t.Fatalf("logs = %+v, want one escalation", r.logs)
	}
	if l := r.logs[0]; l.level != "error" || l.event != "jev-escalate" || !strings.Contains(l.msg, "no-transcript") {
		t.Errorf("escalation = %+v", l)
	}
}

func TestJevContractPassModelEscalation(t *testing.T) {
	r := contractJevSetup(t)
	z := r.zeus("")
	r.tails[z.Pane] = contractJevTrustPane
	r.resp = contractJevResp(0.95, jevEscalateKey, 0.9)
	newJevState().jevPass(r.j, []*core.Peer{z}, r.live, map[string]bool{}, time.Unix(1_800_000_000, 0), r.logf)
	if len(r.sends) != 0 {
		t.Errorf("typed on a model escalation: %+v", r.sends)
	}
	if len(r.logs) != 1 || r.logs[0].event != "jev-escalate" || r.logs[0].level != "error" || !strings.Contains(r.logs[0].msg, "model-escalated") {
		t.Errorf("logs = %+v, want one error jev-escalate naming model-escalated", r.logs)
	}
}

func TestJevContractPassRetryAndCap(t *testing.T) {
	r := contractJevSetup(t)
	z := r.zeus("")
	r.tails[z.Pane] = contractJevTrustPane
	s := newJevState()
	t0 := time.Unix(1_800_000_000, 0)
	all := []*core.Peer{z}
	tp := map[string]bool{}

	s.jevPass(r.j, all, r.live, tp, t0, r.logf) // typed #1
	if len(r.sends) != 1 {
		t.Fatalf("first sighting: sends=%d", len(r.sends))
	}
	s.jevPass(r.j, all, r.live, tp, t0.Add(10*time.Second), r.logf) // inside jevRetryAfter: silent skip
	if len(r.sends) != 1 || len(r.logs) != 1 || len(r.apiReqs) != 1 {
		t.Fatalf("within jevRetryAfter: sends=%d logs=%d api=%d, want 1/1/1", len(r.sends), len(r.logs), len(r.apiReqs))
	}
	s.jevPass(r.j, all, r.live, tp, t0.Add(jevRetryAfter+time.Second), r.logf) // typed #2
	if len(r.sends) != 2 {
		t.Fatalf("second sighting after jevRetryAfter: sends=%d, want 2", len(r.sends))
	}
	s.jevPass(r.j, all, r.live, tp, t0.Add(2*jevRetryAfter+2*time.Second), r.logf) // third: still-showing
	if len(r.sends) != 2 {
		t.Errorf("typed a third time: sends=%d", len(r.sends))
	}
	last := r.logs[len(r.logs)-1]
	if last.level != "error" || last.event != "jev-escalate" || !strings.Contains(last.msg, "still-showing") {
		t.Errorf("third sighting log = %+v, want error jev-escalate still-showing", last)
	}
	n := len(r.logs)
	s.jevPass(r.j, all, r.live, tp, t0.Add(5*jevRetryAfter), r.logf) // escalation logged once per pair
	if len(r.logs) != n || len(r.sends) != 2 {
		t.Errorf("fourth sighting: logs %d→%d sends=%d, want no new log and no send", n, len(r.logs), len(r.sends))
	}
}

func TestJevContractPassInert(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	t.Run("no-client", func(t *testing.T) {
		r := contractJevSetup(t)
		r.client = nil
		z := r.zeus("")
		r.tails[z.Pane] = contractJevTrustPane
		newJevState().jevPass(r.j, []*core.Peer{z}, r.live, map[string]bool{}, now, r.logf)
		if len(r.tailNs)+len(r.sends)+len(r.logs)+len(r.apiReqs) != 0 {
			t.Errorf("feature off but pass acted: tails=%d sends=%d logs=%d api=%d", len(r.tailNs), len(r.sends), len(r.logs), len(r.apiReqs))
		}
	})
	t.Run("no-live-cell", func(t *testing.T) {
		r := contractJevSetup(t)
		z := r.zeus("")
		r.tails[z.Pane] = contractJevTrustPane
		newJevState().jevPass(r.j, []*core.Peer{z}, map[string]bool{}, map[string]bool{}, now, r.logf)
		if len(r.tailNs)+len(r.sends)+len(r.logs)+len(r.apiReqs) != 0 {
			t.Errorf("no live cell but pass acted")
		}
	})
	t.Run("done-job", func(t *testing.T) {
		r := contractJevSetup(t)
		r.j.State = "done"
		z := r.zeus("")
		r.tails[z.Pane] = contractJevTrustPane
		newJevState().jevPass(r.j, []*core.Peer{z}, r.live, map[string]bool{}, now, r.logf)
		if len(r.tailNs)+len(r.sends)+len(r.logs)+len(r.apiReqs) != 0 {
			t.Errorf("done job but pass acted")
		}
	})
	t.Run("not-a-dialog-pane", func(t *testing.T) {
		r := contractJevSetup(t)
		z := r.zeus("")
		r.tails[z.Pane] = contractJevIdlePane
		newJevState().jevPass(r.j, []*core.Peer{z}, r.live, map[string]bool{}, now, r.logf)
		if len(r.sends)+len(r.logs)+len(r.apiReqs) != 0 {
			t.Errorf("unparseable pane not skipped silently: sends=%d logs=%d api=%d", len(r.sends), len(r.logs), len(r.apiReqs))
		}
	})
}

func TestJevContractPassCandidateFilter(t *testing.T) {
	r := contractJevSetup(t)
	z := r.zeus("")
	r.tails[z.Pane] = contractJevIdlePane
	others := []*core.Peer{
		{Pane: "%20", TmuxSession: "cell-someone-else-9", Agent: "claude", Depth: 0},            // other session
		{Pane: "%21", TmuxSession: r.sess, Agent: "claude", Depth: 1},                           // nested
		{Pane: "%22", TmuxSession: r.sess, Agent: "", Role: "shell"},                            // shell pane
		{Pane: "%23", TmuxSession: r.sess, Agent: "claude", Depth: 0, Name: "thread-jevdemo-1"}, // thread pane
	}
	for _, p := range others {
		r.tails[p.Pane] = contractJevTrustPane
	}
	all := append([]*core.Peer{z}, others...)
	newJevState().jevPass(r.j, all, r.live, map[string]bool{"%23": true}, time.Unix(1_800_000_000, 0), r.logf)
	if len(r.sends)+len(r.logs)+len(r.apiReqs) != 0 {
		t.Errorf("non-candidate panes acted on: sends=%+v logs=%+v api=%d", r.sends, r.logs, len(r.apiReqs))
	}
	if len(r.tailNs) != 1 {
		t.Errorf("jevPaneTail called %d times, want once (zeus only)", len(r.tailNs))
	}
}

func TestJevContractPassCodexCandidate(t *testing.T) {
	r := contractJevSetup(t)
	z := &core.Peer{Pane: "%31", TmuxSession: r.sess, Agent: "codex", Depth: 0, Name: ZeusName(r.j)}
	r.tails[z.Pane] = "Do you trust the contents of this directory?\n\n› 1. Yes, continue\n  2. No, quit\n"
	r.resp = contractJevResp(0.95, "opt1", 0.9)
	newJevState().jevPass(r.j, []*core.Peer{z}, r.live, map[string]bool{}, time.Unix(1_800_000_000, 0), r.logf)
	want := []contractJevSend{{"%31", []string{"Enter"}}}
	if !reflect.DeepEqual(r.sends, want) {
		t.Fatalf("codex sends = %+v, want %+v (logs %+v)", r.sends, want, r.logs)
	}
	st := contractJevJSON(t, r.apiReqs[0].State)
	if cell, _ := st["cell"].(map[string]any); cell["agent"] != "codex" {
		t.Errorf("request state.cell.agent = %v, want codex", st["cell"])
	}
}
