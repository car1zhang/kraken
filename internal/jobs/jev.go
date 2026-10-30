package jobs

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"kraken/internal/core"
	"kraken/internal/util"
)

// jev.go is the dialog answerer. A cell runs unattended, so a Claude Code or
// codex dialog that appears in one of its panes (a folder-trust prompt, a
// release notice, a theme picker) stops the job until someone presses a key.
// Every tick this pass reads the bottom of each live cell pane, and when
// core.ParseDialog finds an option list it asks TypeSafe's Jev model which
// option keeps the job running, types that option's keys, and logs the
// decision. The operator's policy (artifact 50-answer-policy.md of job 666)
// is enforced in code before the model is asked anything:
//
//   - An AskUserQuestion is the agent asking the operator, and looks the same
//     on screen as a harness dialog. The only reliable difference is in the
//     transcript, so a pane whose claude has an unanswered AskUserQuestion
//     tool call, or whose codex has an unanswered request_user_input
//     (FindPendingQuestion reads both), is escalated without an API call. A
//     pending ExitPlanMode plan approval is treated the same way.
//   - Options that exit, quit, reset, delete, remove or spend money are
//     struck from the choice set (jevVetoed) before the request is built, so
//     no wording in a dialog can talk the model into one. A header naming a
//     login, terms, an API key or rm escalates the whole dialog
//     (jevHeaderVetoed).
//   - The choice set always carries an "escalate" key, and an answer below
//     either confidence floor escalates too.
//   - The pane is read again after the call and typed into only if the same
//     dialog is still there with the cursor where it was.
//   - Every decision, typed or escalated, goes to the event log as one line
//     holding the pane tail (passed through core.RedactSecrets first), so a
//     wrong pick can be found and replayed.
//
// The feature is off unless ~/.kraken/typesafe.env (or the environment)
// carries TYPESAFE_API_KEY; the key itself never reaches a log line.

// jevDefaultDirective is the state Jev is given as the operator's standing
// instruction when ~/.kraken/jev-directive.md does not override it.
const jevDefaultDirective = `You answer dialogs that appear in an unattended kraken cell, so that the job keeps running without the operator having to look at it.
The cell is a kraken worktree made from the operator's own repo, and its agent was launched on purpose with the flags it has. Trust the folder, its settings and its MCP servers.
Accept notices, announcements, release notes, surveys and onboarding screens with their default or dismiss option. Any theme is fine.
Never pick an option that exits or quits the program, resets configuration to defaults, deletes or removes anything, upgrades a plan, or spends money, credits or extra usage.
Escalate anything that needs a credential, a login, a signature such as accepting terms, or the operator's own judgement. A question the agent itself is asking the operator (AskUserQuestion) or a plan approval is never yours to answer: escalate it.
When no option is clearly safe, escalate.`

// jevDirectivePath is where the operator can replace the built-in directive.
func jevDirectivePath() string {
	return filepath.Join(util.Home(), ".kraken", "jev-directive.md")
}

// JevDirective returns the directive Jev is given: the operator's file when
// it exists and holds anything but whitespace, else the built-in text.
func JevDirective() string {
	b, err := os.ReadFile(jevDirectivePath())
	if err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			return s
		}
	}
	return jevDefaultDirective
}

// jevVetoWords are the words and phrases that strike an option from Jev's
// choice set, matched on word boundaries so "exited" or "remover" stay.
var jevVetoWords = []string{
	"exit",
	"quit",
	// "reset" alone as well, since "Reset to defaults" has no word
	// boundary after "default" and would slip past the phrase below
	"reset",
	"reset to default",
	"use credits",
	"extra usage",
	"upgrade",
	"purchase",
	"delete",
	"remove",
}

// jevWordMatch reports whether s, lower-cased, contains any of words as a
// whole word or phrase. The words become one alternation with \b on both
// sides; a phrase's inner spaces match any run of whitespace, since a pane
// may pad between words. It is built on each call (a dialog is rare, the
// lists are short) so the lists stay plain vars.
func jevWordMatch(words []string, s string) bool {
	alts := make([]string, 0, len(words))
	for _, w := range words {
		parts := strings.Fields(strings.ToLower(w))
		if len(parts) == 0 {
			continue
		}
		for i, p := range parts {
			parts[i] = regexp.QuoteMeta(p)
		}
		alts = append(alts, strings.Join(parts, `\s+`))
	}
	if len(alts) == 0 {
		return false
	}
	return regexp.MustCompile(`\b(?:` + strings.Join(alts, "|") + `)\b`).MatchString(strings.ToLower(s))
}

// jevVetoed reports whether an option's text contains a veto word or phrase.
func jevVetoed(option string) bool {
	return jevWordMatch(jevVetoWords, option)
}

// jevHeaderVetoWords mark a whole dialog as the operator's to answer, matched
// in its header: a login or API key is a credential, terms are a signature,
// and an rm confirmation is data destruction. The directive says the same,
// and this list is the code backstop that stops a dialog like that before
// any request is sent.
var jevHeaderVetoWords = []string{
	"login",
	"log in",
	"sign in",
	"terms",
	"api key",
	"rm",
}

// jevHeaderVetoed reports whether a dialog header contains a header veto
// word or phrase, matched the way jevVetoed matches options.
func jevHeaderVetoed(header string) bool {
	return jevWordMatch(jevHeaderVetoWords, header)
}

// pendingPlanApproval reports whether the transcript's last ExitPlanMode
// tool call has no tool_result yet: the agent is waiting for the operator to
// approve a plan, which the policy treats like an AskUserQuestion. It scans
// the way scanPendingQuestion does, for a different tool name, and an
// unreadable file or a scan that fails partway reads as false.
func pendingPlanApproval(transcript string) bool {
	f, err := os.Open(transcript)
	if err != nil {
		return false
	}
	defer f.Close()
	lastID := ""
	answered := false
	sc := bufio.NewScanner(f)
	// transcript lines can run to several MB (scanPendingQuestion has the
	// measurement), so give the scanner the same headroom
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var ev transcriptEvent
		if json.Unmarshal(line, &ev) != nil {
			continue
		}
		switch ev.Type {
		case "assistant":
			for _, c := range ev.Message.Content {
				if c.Type == "tool_use" && c.Name == "ExitPlanMode" {
					lastID = c.ID
					answered = false
				}
			}
		case "user":
			for _, c := range ev.Message.Content {
				if c.Type == "tool_result" && lastID != "" && c.ToolUseID == lastID {
					answered = true
				}
			}
		}
	}
	if sc.Err() != nil {
		return false
	}
	return lastID != "" && !answered
}

// jevEscalateKey is the criteria key of the escalate choice every request
// carries.
const jevEscalateKey = "escalate"

// The two floors jevDecide applies: how sure Jev must be that the pane shows
// a dialog at all, and how sure it must be of its pick.
const (
	jevMinConfidence = 0.6
	jevMinDialog     = 0.7
)

// jevOptKey names the criteria key of the option at 0-based index i. Keys
// are 1-based so they read like the dialog's own numbering.
func jevOptKey(i int) string { return "opt" + strconv.Itoa(i+1) }

// jevBuildRequest builds the System One request for one parsed dialog. It
// asks two questions: is_dialog (a probability that the pane is a modal
// dialog an unattended operator may answer) and pick (which option, or
// escalate). Vetoed options are left out of pick's criteria, so the model
// cannot choose them; they stay in the state's option list so the model
// still sees the whole dialog.
func jevBuildRequest(directive, slug, agent, paneTail string, d core.Dialog, model string) core.SystemOneRequest {
	opts := make([]string, len(d.Options))
	criteria := map[string]string{}
	for i, o := range d.Options {
		opts[i] = o.Text
		if !jevVetoed(o.Text) {
			criteria[jevOptKey(i)] = o.Text
		}
	}
	criteria[jevEscalateKey] = "Choose this when no option is safe to pick, or when the operator must decide."
	return core.SystemOneRequest{
		Model: model,
		State: map[string]any{
			"directive": directive,
			"cell":      map[string]any{"slug": slug, "agent": agent},
			"pane_tail": paneTail,
			"dialog":    map[string]any{"header": d.Header, "options": opts},
		},
		Questions: map[string]core.SystemOneQuestion{
			"is_dialog": {
				Type:         "noul",
				Instructions: "Does the pane show a modal dialog waiting for a keypress, of a kind an unattended operator may answer on the directive's terms?",
			},
			"pick": {
				Type:         "choice",
				Instructions: "Which option should be selected to keep the job running under the directive? Choose escalate when none is safe.",
				Criteria:     criteria,
			},
		},
	}
}

// jevOptRe matches a well-formed option key: opt followed by a positive
// number with no leading zero.
var jevOptRe = regexp.MustCompile(`^opt([1-9][0-9]*)$`)

// jevDecide turns Jev's answers into a pick (an index into d.Options) or an
// escalation reason. Pure: the request is not consulted, so a veto is
// re-checked here in case the model answered with a key it was never
// offered.
func jevDecide(d core.Dialog, resp core.SystemOneResponse) (pick int, reason string) {
	isDialog, ok1 := resp.Answers["is_dialog"]
	choice, ok2 := resp.Answers["pick"]
	if !ok1 || !ok2 {
		return -1, "bad-answer"
	}
	if !(isDialog.Noul >= jevMinDialog) {
		return -1, "not-a-dialog"
	}
	if choice.Choice == jevEscalateKey {
		return -1, "model-escalated"
	}
	m := jevOptRe.FindStringSubmatch(choice.Choice)
	if m == nil {
		return -1, "bad-answer"
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n > len(d.Options) {
		return -1, "bad-answer"
	}
	idx := n - 1
	if jevVetoed(d.Options[idx].Text) {
		return -1, "vetoed"
	}
	if !(choice.Confidence >= jevMinConfidence) {
		return -1, "low-confidence"
	}
	return idx, ""
}

// jevDecision is the record of one decision, rendered by jevLogLine.
type jevDecision struct {
	Slug, Pane, Header string
	Options            []string
	Vetoed             []bool
	Choice             string
	Confidence         float64
	Probabilities      map[string]float64
	IsDialog           float64
	Model              string
	Keys               []string
	Action             string
	Reason             string
	PaneTail           string
}

// jevLogMax caps a decision line in runes.
const jevLogMax = 8000

// jevLogLine renders a decision as one event-log line. The pane tail goes
// last because it is the only field long enough to need shortening; when the
// line would pass jevLogMax runes the tail's oldest text is dropped, keeping
// the bottom of the pane where the dialog sits.
func jevLogLine(dec jevDecision) string {
	var b strings.Builder
	fmt.Fprintf(&b, "slug=%s pane=%s action=%s", jevOneLine(dec.Slug), jevOneLine(dec.Pane), jevOneLine(dec.Action))
	if dec.Reason != "" {
		fmt.Fprintf(&b, " reason=%q", jevOneLine(dec.Reason))
	}
	fmt.Fprintf(&b, " choice=%q confidence=%.2f is_dialog=%.2f model=%s keys=%q",
		jevOneLine(dec.Choice), dec.Confidence, dec.IsDialog, jevOneLine(dec.Model), strings.Join(dec.Keys, " "))
	fmt.Fprintf(&b, " header=%q options=", core.TruncateOneLine(dec.Header, 200))
	for i, o := range dec.Options {
		if i > 0 {
			b.WriteByte(' ')
		}
		if i < len(dec.Vetoed) && dec.Vetoed[i] {
			b.WriteString("✗")
		}
		b.WriteString("[" + jevOneLine(o) + "]")
	}
	b.WriteString(" probabilities=")
	keys := make([]string, 0, len(dec.Probabilities))
	for k := range dec.Probabilities {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%s=%.2f", jevOneLine(k), dec.Probabilities[k])
	}
	b.WriteString(" tail=")
	head := b.String()
	tail := jevTailLine(dec.PaneTail)
	room := jevLogMax - utf8.RuneCountInString(head)
	if room <= 0 {
		// only an extremely wide pane gets here (every option text is
		// uncut); hard-cut the line so the cap still holds
		r := []rune(head)
		return string(r[:jevLogMax])
	}
	if r := []rune(tail); len(r) > room {
		tail = "…" + string(r[len(r)-room+1:])
	}
	return head + tail
}

// jevOneLine flattens a field to one line: control characters become spaces.
func jevOneLine(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}

// jevTailLine renders the pane tail on one line, newlines as ⏎.
func jevTailLine(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return jevOneLine(strings.ReplaceAll(s, "\n", "⏎"))
}

// The pass' side effects, each a var so a test can replace it and never
// reach tmux, the network or a real transcript.
var (
	jevSendKeys = func(pane string, keys []string) error {
		args := append([]string{"send-keys", "-t", pane}, keys...)
		return exec.Command("tmux", args...).Run()
	}
	jevPaneTail  = core.PaneTail
	jevSystemOne = func(c *core.TypeSafeClient, req core.SystemOneRequest) (core.SystemOneResponse, error) {
		return c.SystemOne(req)
	}
	jevLoadClient = func() *core.TypeSafeClient {
		return core.NewTypeSafeClient(core.LoadTypeSafeConfig())
	}
	jevPendingQuestion = func(transcript string) bool {
		return FindPendingQuestion(transcript) != nil || pendingPlanApproval(transcript)
	}
)

// A dialog key is typed into at most jevMaxAnswers times and never twice
// within jevRetryAfter. A dialog still on screen after its answers ran out
// is escalated as still-showing rather than typed into a third time. One
// pass makes at most jevMaxCallsPerPass API calls, so an API outage holds
// the tick for at most twice the client's timeout per job; the dialogs left
// over wait for the next tick.
const (
	jevMaxAnswers      = 2
	jevRetryAfter      = 60 * time.Second
	jevMaxCallsPerPass = 2
)

// jevTailLines is how many pane lines are captured, matching the bottom-25
// window core.ParseDialog reads.
const jevTailLines = 25

// jevForget is how long a dialog key is remembered after it was last seen
// on screen.
const jevForget = 10 * time.Minute

// jevKey identifies one dialog on one agent. The pid is in it because tmux
// respawn-pane keeps the pane id, so a respawned agent's identical dialog
// would otherwise inherit a spent budget. The option texts are in it
// because one header can front several screens (multi-step onboarding).
type jevKey struct {
	pane    string
	pid     int
	header  string
	options string
}

type jevMemo struct {
	typed     int
	lastTyped time.Time
	escalated bool
	// lastAsked bounds how often an api-error is retried; every other
	// escalation is final for the key and never re-asks the model.
	// apiErrLogged keeps a failing API to one logged line per key.
	lastAsked    time.Time
	apiErrLogged bool
	seen         time.Time
}

// jevSameDialog reports whether two parses show the same dialog: the same
// header, the same option texts in the same order, and the cursor on the
// same option.
func jevSameDialog(a, b core.Dialog) bool {
	if a.Header != b.Header || a.Cursor != b.Cursor || len(a.Options) != len(b.Options) {
		return false
	}
	for i := range a.Options {
		if a.Options[i].Text != b.Options[i].Text {
			return false
		}
	}
	return true
}

// jevState is the pass' memory for one daemon process. It is not persisted:
// after a restart a dialog still on screen can be typed into or logged once
// more, which costs at most one keypress or one duplicate line.
type jevState struct {
	keys map[jevKey]*jevMemo
}

func newJevState() *jevState {
	return &jevState{keys: map[jevKey]*jevMemo{}}
}

func (s *jevState) memo(k jevKey, now time.Time) *jevMemo {
	if s.keys == nil {
		s.keys = map[jevKey]*jevMemo{}
	}
	m := s.keys[k]
	if m == nil {
		m = &jevMemo{}
		s.keys[k] = m
	}
	m.seen = now
	return m
}

func (s *jevState) prune(now time.Time) {
	for k, m := range s.keys {
		if now.Sub(m.seen) > jevForget {
			delete(s.keys, k)
		}
	}
}

// jevPass looks for dialogs in one job's live cell and answers or escalates
// each. See the top of this file for the policy; the steps, in order, are
// the AskUserQuestion and plan-approval guard with the header veto, the
// per-dialog answer budget, the Jev call, the decision, and a re-read of the
// pane before its keys are sent. logf is the only way it logs.
func (s *jevState) jevPass(j *Job, all []*core.Peer, live map[string]bool, threadPanes map[string]bool, now time.Time, logf func(level, event, msg string)) {
	if s == nil || j == nil || j.State == "done" {
		return
	}
	sess, ok := FindCellSession(j, live)
	if !ok {
		return
	}
	client := jevLoadClient()
	if client == nil {
		return
	}
	s.prune(now)
	zeus := CellZeus(j, all)
	threadPrefixes := threadNamePrefixes(j)
	calls := 0
	var model string
	modelLoaded := false
	for _, p := range all {
		if p == nil || p.TmuxSession != sess || p.Depth != 0 || threadPanes[p.Pane] {
			continue
		}
		// the name filter CellZeus applies too, so a nil threadPanes after
		// a store error still cannot make a side thread a candidate
		if hasAnyPrefix(p.Name, threadPrefixes) {
			continue
		}
		if p.Agent != "claude" && p.Agent != "codex" {
			continue
		}
		raw := jevPaneTail(p.Pane, jevTailLines)
		d, ok := core.ParseDialog(raw)
		if !ok {
			continue
		}
		optTexts := make([]string, len(d.Options))
		for i, o := range d.Options {
			optTexts[i] = o.Text
		}
		k := jevKey{pane: p.Pane, pid: p.PID, header: d.Header, options: strings.Join(optTexts, "\x00")}
		m := s.memo(k, now)
		// A dialog already escalated for the operator is skipped before
		// anything else, so a dialog left waiting does not re-scan the
		// transcript every tick. An api-error does not set escalated.
		if m.escalated {
			continue
		}
		// The tail, the header and every option text go through
		// RedactSecrets before they reach the request state or the log
		// line. The raw parse d stays for the memo key above, the re-read
		// comparison and DialogKeys.
		tail := core.RedactSecrets(raw)
		red := core.Dialog{Header: core.RedactSecrets(d.Header), Cursor: d.Cursor}
		for _, o := range d.Options {
			o.Text = core.RedactSecrets(o.Text)
			red.Options = append(red.Options, o)
		}
		dec := jevDecision{
			Slug:     j.Slug,
			Pane:     p.Pane,
			Header:   red.Header,
			PaneTail: tail,
		}
		for i, o := range red.Options {
			dec.Options = append(dec.Options, o.Text)
			dec.Vetoed = append(dec.Vetoed, jevVetoed(d.Options[i].Text))
		}
		escalate := func(reason string) {
			if m.escalated {
				return
			}
			m.escalated = true
			dec.Action = "escalated"
			dec.Reason = reason
			dec.Choice = ""
			dec.Keys = nil
			logf("error", "jev-escalate", jevLogLine(dec))
		}

		// (1) The AskUserQuestion guard. A codex peer's transcript is its
		// rollout file, where FindPendingQuestion reads an unanswered
		// request_user_input. A claude peer's is found by its session id,
		// a guessed one included: a guessed id can only make the check
		// escalate, which is the safe direction. For zeus the job's stamped
		// session is the fallback, since a resumed zeus whose session file
		// is not written yet can still have a question open in it.
		path := ""
		if p.Agent == "codex" {
			path = p.RolloutPath
		} else if p.SessionID != "" {
			path = core.TranscriptPathForSession(p.SessionID)
		}
		if path == "" && p == zeus && j.Session != "" {
			path = core.TranscriptPathForSession(j.Session)
		}
		if path != "" {
			if jevPendingQuestion(path) {
				escalate("askuserquestion")
				continue
			}
		} else if p != zeus {
			// a teammate or any other pane whose transcript cannot be
			// read: an AskUserQuestion cannot be ruled out
			escalate("no-transcript")
			continue
		}
		// zeus with no resolvable transcript is let through as a cell that
		// is still starting: before its first turn there is no tool call
		// that could be pending. A zeus whose session id was only guessed
		// takes this path too when neither that id nor the job's stamped
		// session finds a file; the contract accepts that rather than
		// escalating every starting cell.

		// Login, terms, API-key and rm dialogs are the operator's whatever
		// their options say.
		if jevHeaderVetoed(d.Header) {
			escalate("header-vetoed")
			continue
		}

		// (2) The answer budget for this dialog.
		if m.typed >= jevMaxAnswers {
			escalate("still-showing")
			continue
		}
		if !m.lastTyped.IsZero() && now.Sub(m.lastTyped) < jevRetryAfter {
			continue
		}

		// Every option struck by a veto: nothing is safe to type.
		anyLeft := false
		for _, v := range dec.Vetoed {
			anyLeft = anyLeft || !v
		}
		if !anyLeft {
			escalate("vetoed")
			continue
		}

		// (3) Ask Jev. An API error is retried, but at most once per
		// jevRetryAfter, so a bad key does not cost a call every tick.
		if !m.lastAsked.IsZero() && now.Sub(m.lastAsked) < jevRetryAfter {
			continue
		}
		if calls >= jevMaxCallsPerPass {
			continue
		}
		calls++
		m.lastAsked = now
		if !modelLoaded {
			model = core.LoadTypeSafeConfig().Model
			modelLoaded = true
		}
		dec.Model = model
		resp, err := jevSystemOne(client, jevBuildRequest(JevDirective(), j.Slug, p.Agent, tail, red, model))
		if err != nil {
			// the one escalation worth retrying, so it does not mark the key
			// as escalated: a later call that succeeds still types
			if !m.apiErrLogged {
				m.apiErrLogged = true
				dec.Action = "escalated"
				dec.Reason = "api-error: " + err.Error()
				logf("error", "jev-escalate", jevLogLine(dec))
			}
			continue
		}
		if resp.Model != "" {
			dec.Model = resp.Model
		}
		dec.IsDialog = resp.Answers["is_dialog"].Noul
		pa := resp.Answers["pick"]
		dec.Confidence = pa.Confidence
		dec.Probabilities = pa.Probabilities

		// (4) Decide, then type.
		pick, reason := jevDecide(d, resp)
		if pick < 0 {
			escalate(reason)
			continue
		}
		keys := core.DialogKeys(d, pick)
		if len(keys) == 0 {
			escalate("no-keys")
			continue
		}
		// Read the pane again before typing. The call can take seconds, and
		// in that window acceptTrustPrompts, the operator or the agent itself
		// can replace the screen; a bare Enter or a relative arrow count
		// would then select whatever sits there now, "No, exit" included. A changed or vanished dialog is not typed into and not
		// escalated, and clearing lastAsked lets the next tick judge
		// whatever is on screen then.
		if again, ok := core.ParseDialog(jevPaneTail(p.Pane, jevTailLines)); !ok || !jevSameDialog(d, again) {
			m.lastAsked = time.Time{}
			continue
		}
		if err := jevSendKeys(p.Pane, keys); err != nil {
			escalate("send-error: " + err.Error())
			continue
		}
		m.typed++
		m.lastTyped = now
		m.lastAsked = time.Time{}
		dec.Action = "typed"
		dec.Choice = red.Options[pick].Text
		dec.Keys = keys
		logf("info", "jev-answer", jevLogLine(dec))
	}
}
