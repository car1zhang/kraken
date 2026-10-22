package core

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"kraken/internal/util"
)

// operator.go — `kraken mine operator|around`: the operator's own words out of
// claude's root transcripts, each with the reduced context it was said in.
//
// The delta from `kraken mine reduce`: no author flag separates the operator's
// keystrokes from kraken's pane bumps — both arrive as
// `origin:{kind:"human"}, promptSource:"typed"`, because both reach claude
// through the TTY — so the reducer's `[user]` label is mostly NOT the
// operator. Attribution is therefore the record's flags (which do rule out
// tool results, skill bodies, compaction summaries, task notifications and
// teammate traffic) plus a CLOSED denylist of kraken's own texts, matched
// anywhere in the text because a bump typed while a draft sat in the input
// box is glued onto the draft as one record. Context rides along because a
// prompt's referent ("ok do that", "no, the other one") lives in what the
// agent had just said.

// OperatorOptions bounds one sweep. Window edges are unix millis, inclusive.
type OperatorOptions struct {
	Since, Until int64
	Login        string
	Before       int
	After        int
}

// OperatorResult is one sweep's answer. Records is never nil.
type OperatorResult struct {
	Since     int64            `json:"since"`
	Until     int64            `json:"until"`
	Scanned   int              `json:"scanned"`
	Skipped   int              `json:"skipped"`
	Truncated int              `json:"truncated"`
	Records   []OperatorRecord `json:"records"`
}

// OperatorRecord is one operator-authored prompt.
type OperatorRecord struct {
	Session        string          `json:"session"`
	UUID           string          `json:"uuid"`
	TS             int64           `json:"ts"`
	TranscriptPath string          `json:"transcript_path"`
	ProjectDir     string          `json:"project_dir"`
	Slug           string          `json:"slug"`
	Repo           string          `json:"repo"`
	Form           string          `json:"form"`
	Text           string          `json:"text"`
	Pasted         bool            `json:"pasted"`
	Context        OperatorContext `json:"context"`
}

// OperatorContext is the reduced sections around a record, oldest first on
// each side. Both slices are never nil.
type OperatorContext struct {
	Before []ContextBlock `json:"before"`
	After  []ContextBlock `json:"after"`
}

// ContextBlock is one reduced section: the label line `kraken mine reduce`
// prints ("[assistant]", "[tool] Bash(…)", "[user]") and its body.
type ContextBlock struct {
	Label string `json:"label"`
	Text  string `json:"text"`
}

// OperatorDenylist is every text kraken (or an agent, through `kraken s`)
// types into a pane that is not the operator speaking. Each entry is matched
// ANYWHERE in a candidate's text, the text is cut at the earliest match, and
// the part before it is what the operator typed. Closed on purpose: a text
// kraken starts typing must be added here, which
// internal/jobs/operator_denylist_test.go pins for the jobs package's own
// constants. `steering from ` is deliberately absent — a reactivation
// prompt wraps the operator's words rather than replacing them, and is
// unwrapped by its own rung.
var OperatorDenylist = []string{
	"[kraken pr-watch", // prWatchPrefix: all four PR-watch bumps and the merge bump
	"/color ",          // the colour pass
	"/handoff",         // the auto-handoff send, and the handoff skill's own `/handoff resume`
	"/compact",         // the handoff skill's queued compaction
	"/model claude-",   // the Fable fallback's switch
	"kraken: ",         // Fable fallback continue, limit-rescue resumes, rename notice
	"you stalled",      // the stall nudges (zeus and the standing agent)
	"slack: ",          // the Slack intake's bump into iris
	"orch: ",           // the retired orchestrator's inbox bump, still in old transcripts
}

// OperatorDenylistRE holds the machine texts a literal cannot: the Linear
// bump opens "Linear <identifier> changed" (linearUpdateText,
// internal/jobs/linear.go) with a per-issue identifier, and a bare
// "Linear " literal would cut any prompt of the operator's that uses the
// word. Matched anywhere, exactly like OperatorDenylist; the cut is at the
// earliest match across both lists.
var OperatorDenylistRE = []*regexp.Regexp{
	regexp.MustCompile(`Linear [A-Z][A-Z0-9]*-[0-9]+ changed`),
}

// operatorAnchored are the OperatorDenylist entries kraken types ALONE on a
// line (slash commands), so they match only at offset 0 or right after a
// newline: `just /handoff to a new cell` is the operator's sentence. Every
// other entry is a bump that glues onto a draft and matches anywhere.
var operatorAnchored = map[string]bool{
	"/color ":        true,
	"/handoff":       true,
	"/compact":       true,
	"/model claude-": true,
}

// Thread-guard tails kraken appends under a question, each cut off with
// everything after it: the pane thread's threadPromptGuard
// (internal/jobs/crew.go), the headless ask thread's askGuard
// (internal/jobs/askthread.go), and the board's two seed guards
// (askThreadSeed/questionThreadSeed, web/_ui/src/components/overlays/askThread.ts).
var operatorGuardTails = []string{
	"\n\n(Answer here in this thread.",
	"\n\n(Here, answer in one message.",
	"\n\nAnswer here in the thread —",
}

// The board's two ask-thread seeds (askThread.ts) wrap the operator's
// question in kraken text. The artifact seed's preamble ends at
// operatorArtifactSeedMark; the question seed's preamble is followed by the
// AGENT-authored question row, then the operator's ask — or
// operatorQuestionSeedDefault when nothing was typed, which is kraken's
// sentence and yields no record. internal/core/operator_seed_test.go reads
// the web file and fails when any of these strings (or the seeds' guard
// opening) is no longer in it verbatim.
const (
	operatorArtifactSeedOpen    = "I'm reading the artifact `"
	operatorArtifactSeedMark    = "` from this job's artifact set. A question about it:"
	operatorQuestionSeedOpen    = "I'm looking at an open question this job raised on the board. The row as filed:"
	operatorQuestionSeedDefault = "Help me understand this question: what is it really asking, what context do I need to answer it, and what hinges on the answer?"
)

// operatorCaptureSeedRE is the first line of the mind-thread capture messages
// (mindSeed / mindCaptureMessage, internal/jobs/mind.go): kraken's sentence
// around a highlighted, usually agent-written passage. The whole record is
// dropped — a denylist cut would keep the operator's name as a "record".
// It keys on kraken's FULL phrases, so the operator's own "I highlighted this
// passage in the doc because …" is kept. The pronoun alternation covers
// transcripts written before the seeds said "their".
var operatorCaptureSeedRE = regexp.MustCompile(`^.{1,64}? highlighted (?:this passage and sent it to you|another passage for (?:their|his|her) mental model:)`)

// Context budgets: one block, and one record's blocks in total.
const (
	operatorBlockCap  = 1500
	operatorRecordCap = 8000
	aroundRawCap      = 4000
)

// operatorLine is the narrow slice of a transcript record the operator rung
// reads — the flags mineRecord has no reason to carry.
type operatorLine struct {
	Type             string `json:"type"`
	UUID             string `json:"uuid"`
	Timestamp        string `json:"timestamp"`
	SessionID        string `json:"session_id"`
	IsMeta           bool   `json:"isMeta"`
	IsCompactSummary bool   `json:"isCompactSummary"`
	IsSidechain      bool   `json:"isSidechain"`
	AgentName        string `json:"agentName"`
	PromptSource     string `json:"promptSource"`
	// StackedExpansion marks the second command record a `/aim /direct …`
	// line writes, carrying the same args as the first.
	StackedExpansion bool `json:"stackedExpansion"`
	Origin           struct {
		Kind string `json:"kind"`
	} `json:"origin"`
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// decodeOperatorLine decodes one line, keeping whatever decoded when a single
// field is wrongly typed (the mine.go tolerance rule).
func decodeOperatorLine(line []byte) (operatorLine, bool) {
	var rec operatorLine
	if len(strings.TrimSpace(string(line))) == 0 {
		return rec, false
	}
	if err := json.Unmarshal(line, &rec); err != nil {
		var typeErr *json.UnmarshalTypeError
		if !errors.As(err, &typeErr) {
			return rec, false
		}
	}
	return rec, true
}

// sectionSink collects the reducer's sections as a list.
type sectionSink struct{ secs []ContextBlock }

func (s *sectionSink) section(label, body string) {
	s.secs = append(s.secs, ContextBlock{Label: label, Text: body})
}

// prompt reports whether a record passes the record rung's flag conditions —
// everything but the time window. false means it is not a prompt at all.
func (rec operatorLine) prompt() bool {
	if rec.Type != "user" || rec.IsMeta || rec.IsCompactSummary || rec.IsSidechain || rec.AgentName != "" || rec.StackedExpansion {
		return false
	}
	switch {
	case rec.Origin.Kind == "human":
		return rec.PromptSource == "" || rec.PromptSource == "typed" || rec.PromptSource == "queued"
	case rec.PromptSource == "sdk":
		return true
	}
	return false
}

// recordText is a user record's prose: string content whole, or the text
// blocks of a list joined by newlines. A list holding only tool results
// yields "".
func recordText(content json.RawMessage) string {
	if s, ok := mineUserString(content); ok {
		return s
	}
	var parts []string
	for _, b := range mineBlocks(content) {
		if b.Type == "text" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// splitStamp parses a leading `[from <label> · <who>]` stamp. ok=false means
// the text carries none — including a bracket with no ` · ` in it, which is
// the operator's own words (`[from memory] the gate is per-cell`).
func splitStamp(t string) (who, rest string, ok bool) {
	if !strings.HasPrefix(t, "[from ") {
		return "", t, false
	}
	end := strings.Index(t, "]")
	if end < 0 {
		return "", t, false
	}
	inner := t[len("[from "):end]
	i := strings.LastIndex(inner, " · ")
	if i < 0 {
		return "", t, false
	}
	return strings.TrimSpace(inner[i+len(" · "):]), strings.TrimSpace(t[end+1:]), true
}

// peelPasted splits a text that opens with a `<pasted_content id="…">`
// wrapper (how a multi-line pane send arrives) into its opening tag, the
// text inside it, and whatever follows the closing tag. ok=false: no wrapper.
func peelPasted(t string) (open, inner, after string, ok bool) {
	if !strings.HasPrefix(t, "<pasted_content") {
		return "", t, "", false
	}
	gt := strings.Index(t, ">")
	if gt < 0 {
		return "", t, "", false
	}
	open, rest := t[:gt+1], t[gt+1:]
	if c := strings.LastIndex(rest, "</pasted_content"); c >= 0 {
		tail := rest[c:]
		if e := strings.Index(tail, ">"); e >= 0 {
			after = tail[e+1:]
		}
		rest = rest[:c]
	}
	return open, strings.TrimSpace(rest), strings.TrimSpace(after), true
}

// unwrapAskSeed returns the operator's paragraph(s) out of one of the board's
// ask-thread seeds (guard tail already cut). seed=false: t is not a seed.
// keep=false: the seed carries only kraken's default ask.
func unwrapAskSeed(t string) (words string, seed, keep bool) {
	if strings.HasPrefix(t, operatorArtifactSeedOpen) {
		if i := strings.Index(t, operatorArtifactSeedMark); i >= 0 {
			w := strings.TrimSpace(t[i+len(operatorArtifactSeedMark):])
			return w, true, w != ""
		}
	}
	if !strings.HasPrefix(t, operatorQuestionSeedOpen) {
		return t, false, true
	}
	paras := strings.Split(strings.TrimSpace(t[len(operatorQuestionSeedOpen):]), "\n\n")
	// The row is `<qid> (<imp>): text`, then optional `context:` and
	// `recommendation:` lines, recommendation last. It ends at the first
	// paragraph carrying a recommendation line, else a context line, else
	// its own first paragraph.
	end := 0
	for _, key := range []string{"recommendation: ", "context: "} {
		found := false
		for i, p := range paras {
			if strings.HasPrefix(p, key) || strings.Contains(p, "\n"+key) {
				end, found = i, true
				break
			}
		}
		if found {
			break
		}
	}
	w := strings.TrimSpace(strings.Join(paras[end+1:], "\n\n"))
	return w, true, w != "" && w != operatorQuestionSeedDefault
}

// operatorDropPrefixes are the harness texts that ride in a human-origin user
// record without being a prompt.
var operatorDropPrefixes = []string{
	"<task-notification>",
	"<system-reminder>",
	"<local-command-",
	"<bash-",
	"[Request interrupted",
	"This session is being continued",
}

// operatorText applies the text rung to one candidate's raw text, returning
// the operator's words and the form they arrived in; ok=false drops the
// record. The record's promptSource decides the default form.
//
// A text wrapped in `<pasted_content>` is judged by what is INSIDE the
// wrapper — kraken's multi-line sends arrive pasted, so offset 0 is the first
// byte after the opening tag — and, when operator words survive, comes back
// with the wrapper intact.
func operatorText(raw string, rec operatorLine, login string) (text, form string, ok bool) {
	full := strings.TrimSpace(raw)
	open, inner, after, wrapped := peelPasted(full)
	view := inner
	if wrapped && after != "" {
		view = inner + "\n\n" + after
	}
	kept, form, ok := operatorWords(view, rec, login)
	if !ok {
		return "", "", false
	}
	if !wrapped {
		return kept, form, true
	}
	if kept == strings.TrimSpace(view) {
		return full, form, true
	}
	// A cut inside the wrapper: keep the block's shape around what survived.
	closing := "</pasted_content" + strings.TrimPrefix(open, "<pasted_content")
	return open + "\n" + kept + "\n" + closing, form, true
}

// operatorWords is the text rung over an unwrapped text.
func operatorWords(t string, rec operatorLine, login string) (text, form string, ok bool) {
	t = strings.TrimSpace(t)
	if t == "" {
		return "", "", false
	}
	form = "typed"
	if rec.PromptSource == "queued" {
		form = "queued"
	}
	// The stamp rung. An sdk record is admitted ONLY on the operator's own
	// stamp. A human-origin text can carry one too — a remote steer from
	// another node arrives typed into zeus' pane — and is held to the same
	// rule, since a `· cell <slug>` stamp is another machine's agent and
	// another login is another person.
	if who, rest, stamped := splitStamp(t); stamped {
		// askStampValue fences every `@` in a stamped login as `＠`, so an
		// email login is compared with the fence undone on both sides.
		if who == "" || strings.HasPrefix(who, "cell ") || unfenceAt(who) != unfenceAt(login) {
			return "", "", false
		}
		t, form = rest, "remote"
	} else if rec.PromptSource == "sdk" {
		return "", "", false
	}
	for _, p := range operatorDropPrefixes {
		if strings.HasPrefix(t, p) {
			return "", "", false
		}
	}
	if _, isTeammate := teammateBlock(t); isTeammate {
		return "", "", false
	}
	if strings.HasPrefix(t, "<command-message>") || strings.HasPrefix(t, "<command-name>") {
		args, has := commandArgs(t)
		if !has {
			return "", "", false
		}
		// The handoff skill queues `/handoff resume` into its own pane
		// through `kraken s .`, and the harness rewrites it into this
		// wrapper — so a command that is itself a denylisted send is
		// kraken's. `/handoff` is the one exception: typed WITH a brief it
		// is the operator describing continuation agents, so only the
		// skill's own `resume` is dropped.
		name, a := commandName(t), strings.TrimSpace(args)
		if name == "/handoff" {
			if a == "resume" {
				return "", "", false
			}
		} else if denylistIndex(name+" "+a) == 0 {
			return "", "", false
		}
		t, form = strings.TrimSpace(args), "command-args"
	}
	if first, _, _ := strings.Cut(t, "\n"); operatorCaptureSeedRE.MatchString(first) {
		return "", "", false
	}
	// Reactivation wraps the operator's message under kraken's reorient
	// text; only what follows the marker is theirs. With no message the
	// prompt is the rebase note alone (reactivatePrompt, internal/jobs/cell.go).
	if marker := "\nsteering from " + login + ": "; strings.Contains(t, marker) {
		t = strings.TrimSpace(t[strings.Index(t, marker)+len(marker):])
		form = "reactivation"
	} else if strings.HasPrefix(t, "your branch ") && strings.Contains(t, " is behind ") {
		return "", "", false
	}
	for _, g := range operatorGuardTails {
		if i := strings.Index(t, g); i >= 0 {
			t = t[:i]
		}
	}
	if w, seed, keep := unwrapAskSeed(t); seed {
		if !keep {
			return "", "", false
		}
		t = w
	}
	if cut := denylistIndex(t); cut >= 0 {
		if cut == 0 {
			return "", "", false
		}
		t = t[:cut]
	}
	t = strings.TrimSpace(t)
	if t == "" {
		return "", "", false
	}
	return t, form, true
}

// unfenceAt undoes askStampValue's `@` → `＠` (U+FF20) rewrite.
func unfenceAt(s string) string { return strings.ReplaceAll(s, "＠", "@") }

// commandName extracts `<command-name>/x</command-name>`'s `/x`, or "".
func commandName(t string) string {
	const open, closing = "<command-name>", "</command-name>"
	i := strings.Index(t, open)
	if i < 0 {
		return ""
	}
	rest := t[i+len(open):]
	if j := strings.Index(rest, closing); j >= 0 {
		return strings.TrimSpace(rest[:j])
	}
	return ""
}

// commandArgs extracts `<command-args>X</command-args>`.
func commandArgs(t string) (string, bool) {
	const open, closing = "<command-args>", "</command-args>"
	i := strings.Index(t, open)
	if i < 0 {
		return "", false
	}
	rest := t[i+len(open):]
	if j := strings.Index(rest, closing); j >= 0 {
		rest = rest[:j]
	}
	return rest, true
}

// denylistIndex is the earliest offset any OperatorDenylist entry or
// OperatorDenylistRE pattern matches at, or -1. An operatorAnchored entry
// counts only at offset 0 or right after a newline.
func denylistIndex(t string) int {
	best := -1
	for _, p := range OperatorDenylist {
		i := strings.Index(t, p)
		for operatorAnchored[p] && i > 0 && t[i-1] != '\n' {
			j := strings.Index(t[i+1:], p)
			if j < 0 {
				i = -1
				break
			}
			i += 1 + j
		}
		if i >= 0 && (best < 0 || i < best) {
			best = i
		}
	}
	for _, re := range OperatorDenylistRE {
		if loc := re.FindStringIndex(t); loc != nil && (best < 0 || loc[0] < best) {
			best = loc[0]
		}
	}
	return best
}

// cellNameParts splits a munged project dir into its cell slug and repo:
// the text between `-kraken-cells-` and the first `-wt-`, and the text after
// that `-wt-`. A dir that is not a cell's yields "", "".
func cellNameParts(projectDir string) (slug, repo string) {
	const cells, wt = "-kraken-cells-", "-wt-"
	i := strings.Index(projectDir, cells)
	if i < 0 {
		return "", ""
	}
	rest := projectDir[i+len(cells):]
	j := strings.Index(rest, wt)
	if j < 0 {
		return "", ""
	}
	return rest[:j], rest[j+len(wt):]
}

// operatorHit is one emitted record plus where its own sections sit in the
// file's section list: [pos, end), plus own, an earlier section that is also
// the record's (a delivered queued prompt's `[user mid-turn]`), or -1.
type operatorHit struct {
	rec           OperatorRecord
	pos, end, own int
}

// queuedOwnSection finds a delivered queued prompt's own section. The reducer
// suppresses the delivered `user` copy (queuedAlready) and emitted the text
// earlier as `[user mid-turn]`, so the nearest earlier such section with the
// same text is the record's. -1 when there is none.
func queuedOwnSection(secs []ContextBlock, pos int, raw string) int {
	want := redactSecrets(strings.TrimSpace(raw))
	for k := pos - 1; k >= 0; k-- {
		if secs[k].Label == "[user mid-turn]" && secs[k].Text == want {
			return k
		}
	}
	return -1
}

// OperatorProse sweeps every root transcript touched since opts.Since for
// the operator's own prompts. See the file comment at the top of
// operator.go for why the rungs are what they are.
func OperatorProse(opts OperatorOptions) (*OperatorResult, error) {
	if opts.Since <= 0 {
		return nil, fmt.Errorf("operator prose needs a since (unix millis), got %d", opts.Since)
	}
	opts.Before = max(opts.Before, 0)
	opts.After = max(opts.After, 0)
	opts.Login = strings.TrimSpace(opts.Login)

	res := &OperatorResult{Since: opts.Since, Until: opts.Until, Records: []OperatorRecord{}}
	root := filepath.Join(util.Home(), ".claude", "projects")
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return res, nil
		}
		return nil, fmt.Errorf("read projects dir: %v", err)
	}
	var paths []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(root, e.Name()))
		if err != nil {
			res.Truncated++ // a project we could not list is evidence lost, said out loud
			continue
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
				continue
			}
			info, err := f.Info()
			if err != nil || info.ModTime().UnixMilli() < opts.Since {
				continue
			}
			paths = append(paths, filepath.Join(root, e.Name(), f.Name()))
		}
	}
	sort.Strings(paths)

	seen := map[string]bool{}
	for _, p := range paths {
		operatorFile(p, opts, seen, res)
	}
	sort.SliceStable(res.Records, func(i, j int) bool {
		a, b := res.Records[i], res.Records[j]
		if a.TS != b.TS {
			return a.TS < b.TS
		}
		if a.Session != b.Session {
			return a.Session < b.Session
		}
		return a.UUID < b.UUID
	})
	return res, nil
}

// operatorFile scans one root transcript: the reducer runs over every line
// (since 0, so the context covers the whole file) while the operator rungs
// pick candidates out of the same lines.
func operatorFile(path string, opts OperatorOptions, seen map[string]bool, res *OperatorResult) {
	f, err := os.Open(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			res.Truncated++
		}
		return
	}
	defer f.Close()
	res.Scanned++
	if isAgentTranscript(f, path) {
		res.Skipped++
		return
	}
	zeus := strings.HasPrefix(headAgentName(f), "zeus-")
	stem := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	projectDir := filepath.Base(filepath.Dir(path))
	slug, repo := cellNameParts(projectDir)

	sink := &sectionSink{}
	var r reducer
	var hits []operatorHit
	firstPrompt := true
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, mineScanBufInit), mineScanBufMax)
	for sc.Scan() {
		line := sc.Bytes()
		pos := len(sink.secs)
		r.line(sink, line, 0)
		rec, ok := decodeOperatorLine(line)
		if !ok || !rec.prompt() {
			continue
		}
		// A zeus transcript's first prompt is the staged job spec, not a
		// keystroke. "First" is counted over prompts regardless of the
		// window, so a spec written before the window cannot pass the
		// distinction on to the operator's first in-window message.
		if zeus && firstPrompt {
			firstPrompt = false
			continue
		}
		firstPrompt = false
		ts := isoMillis(rec.Timestamp)
		if ts < opts.Since || (opts.Until > 0 && ts > opts.Until) {
			continue
		}
		if rec.SessionID != "" && rec.SessionID != stem {
			continue // a fork's replay of its parent's record
		}
		raw := recordText(rec.Message.Content)
		text, form, ok := operatorText(raw, rec, opts.Login)
		if !ok || (rec.UUID != "" && seen[rec.UUID]) {
			continue
		}
		if rec.UUID != "" {
			seen[rec.UUID] = true
		}
		own := -1
		if form == "queued" && len(sink.secs) == pos {
			own = queuedOwnSection(sink.secs, pos, raw)
		}
		hits = append(hits, operatorHit{
			own: own,
			rec: OperatorRecord{
				Session:        stem,
				UUID:           rec.UUID,
				TS:             ts,
				TranscriptPath: path,
				ProjectDir:     projectDir,
				Slug:           slug,
				Repo:           repo,
				Form:           form,
				Text:           redactSecrets(text),
				Pasted:         strings.Contains(text, "<pasted_content"),
			},
			pos: pos,
			end: len(sink.secs),
		})
	}
	if sc.Err() != nil {
		res.Truncated++ // records read so far are kept
	}
	for _, h := range hits {
		h.rec.Context = operatorContext(sink.secs, h.pos, h.end, h.own, opts.Before, opts.After)
		res.Records = append(res.Records, h.rec)
	}
}

// operatorContext slices the sections around a record's own [pos, end),
// skipping own (an earlier section that is also the record's; -1 for none),
// caps each block, and holds the record's total to operatorRecordCap by
// dropping blocks farthest from the record first.
func operatorContext(secs []ContextBlock, pos, end, own, before, after int) OperatorContext {
	prior := secs[:pos]
	if own >= 0 && own < pos {
		prior = append(append([]ContextBlock(nil), secs[:own]...), secs[own+1:pos]...)
	}
	b := capBlocks(prior[max(len(prior)-before, 0):])
	a := capBlocks(secs[end:min(end+after, len(secs))])
	total := 0
	for _, x := range b {
		total += len([]rune(x.Text))
	}
	for _, x := range a {
		total += len([]rune(x.Text))
	}
	droppedB, droppedA := 0, 0
	for total > operatorRecordCap && len(b)+len(a) > 0 {
		// distance from the record: b[0] sits len(b) away, a[len-1] len(a)
		if len(b) >= len(a) {
			total -= len([]rune(b[0].Text))
			b = b[1:]
			droppedB++
		} else {
			total -= len([]rune(a[len(a)-1].Text))
			a = a[:len(a)-1]
			droppedA++
		}
	}
	if droppedB > 0 {
		b = append(b, ContextBlock{Label: "…", Text: fmt.Sprintf("[%d blocks elided]", droppedB)})
	}
	if droppedA > 0 {
		a = append(a, ContextBlock{Label: "…", Text: fmt.Sprintf("[%d blocks elided]", droppedA)})
	}
	return OperatorContext{Before: b, After: a}
}

// capBlocks copies blocks with each Text capped at operatorBlockCap runes and
// redacted. Never returns nil.
func capBlocks(in []ContextBlock) []ContextBlock {
	out := make([]ContextBlock, 0, len(in))
	for _, x := range in {
		out = append(out, ContextBlock{Label: x.Label, Text: capRunes(redactSecrets(x.Text), operatorBlockCap)})
	}
	return out
}

// capRunes truncates s to n runes with the elision suffix.
func capRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + fmt.Sprintf("… [%d more characters elided]", len(r)-n)
}

// AroundRecord returns the blocks around the record carrying uuid in one
// transcript. raw=false: the reduced sections, radius each side of the
// record's own section(s), hit the index of the first of them. raw=true: one
// block per transcript record inside the radius, labelled by record type.
func AroundRecord(path, uuid string, radius int, raw bool) (blocks []ContextBlock, hit int, found bool, err error) {
	radius = max(radius, 0)
	if uuid == "" {
		return nil, 0, false, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, false, fmt.Errorf("open transcript: %v", err)
	}
	defer f.Close()
	if raw {
		return aroundRaw(f, uuid, radius)
	}

	sink := &sectionSink{}
	var r reducer
	pos, end := -1, -1
	var hitLine []byte
	// bufio.Reader rather than a Scanner: one oversized record has no
	// ceiling to trip, so a 20 MB tool result cannot fail the whole read.
	rd := bufio.NewReaderSize(f, 64*1024)
	for {
		line, rerr := rd.ReadBytes('\n')
		if len(line) > 0 {
			p := len(sink.secs)
			r.line(sink, line, 0)
			if pos < 0 {
				if rec, ok := decodeOperatorLine(line); ok && rec.UUID == uuid {
					pos, end = p, len(sink.secs)
					hitLine = line
				}
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return nil, 0, false, fmt.Errorf("read transcript: %v", rerr)
		}
		// past the hit by radius sections: nothing later can be shown
		if pos >= 0 && len(sink.secs) >= end+radius {
			break
		}
	}
	if pos < 0 {
		return nil, 0, false, nil
	}
	own := sink.secs[pos:end]
	if len(own) == 0 {
		// The record reduced to nothing (a tool result, a dropped harness
		// text): show it raw so the hit is still visible.
		own = []ContextBlock{rawBlock(hitLine)}
		sink.secs = append(sink.secs[:pos:pos], append(own, sink.secs[pos:]...)...)
		end = pos + 1
	}
	lo := max(pos-radius, 0)
	hi := min(end+radius, len(sink.secs))
	blocks = append([]ContextBlock(nil), sink.secs[lo:hi]...)
	for i := range blocks {
		blocks[i].Label = redactSecrets(blocks[i].Label)
		blocks[i].Text = redactSecrets(blocks[i].Text)
	}
	return blocks, pos - lo, true, nil
}

// aroundRaw renders every record within radius of the hit, one block each.
func aroundRaw(f *os.File, uuid string, radius int) ([]ContextBlock, int, bool, error) {
	var before []ContextBlock
	var blocks []ContextBlock
	found, after := false, 0
	rd := bufio.NewReaderSize(f, 64*1024)
	for {
		line, rerr := rd.ReadBytes('\n')
		if rec, ok := decodeOperatorLine(line); ok && rec.Type != "" {
			switch {
			case (found || rec.UUID != uuid) && !aroundRawTypes[rec.Type]:
				// bookkeeping (file-history-snapshot, system, mode, …): never
				// shown and never spends the radius — unless it IS the hit
			case !found && rec.UUID == uuid:
				found = true
				blocks = append(before, rawBlock(line))
				before = nil
			case found:
				blocks = append(blocks, rawBlock(line))
				after++
			case radius > 0:
				if len(before) == radius {
					before = before[1:]
				}
				before = append(before, rawBlock(line))
			}
		}
		if found && after >= radius {
			break
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return nil, 0, false, fmt.Errorf("read transcript: %v", rerr)
		}
	}
	if !found {
		return nil, 0, false, nil
	}
	return blocks, len(blocks) - 1 - after, true, nil
}

// aroundRawTypes are the record types raw mode shows and counts.
var aroundRawTypes = map[string]bool{"user": true, "assistant": true, "attachment": true, "queue-operation": true}

// rawRecord is the slice of a record the raw renderer reads.
type rawRecord struct {
	Type       string `json:"type"`
	Content    string `json:"content"`
	Attachment struct {
		Type   string `json:"type"`
		Prompt string `json:"prompt"`
	} `json:"attachment"`
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// rawBlock renders one record as `[<type>]` plus its text: prose and text
// blocks whole, a tool call as its signature, a tool result's body capped at
// aroundRawCap runes.
func rawBlock(line []byte) ContextBlock {
	var rec rawRecord
	if err := json.Unmarshal(line, &rec); err != nil {
		var typeErr *json.UnmarshalTypeError
		if !errors.As(err, &typeErr) {
			return ContextBlock{Label: "[?]"}
		}
	}
	label := "[" + rec.Type + "]"
	var parts []string
	switch rec.Type {
	case "queue-operation":
		parts = append(parts, rec.Content)
	case "attachment":
		if rec.Attachment.Prompt != "" {
			parts = append(parts, rec.Attachment.Prompt)
		} else if rec.Attachment.Type != "" {
			parts = append(parts, rec.Attachment.Type)
		}
	default:
		if s, ok := mineUserString(rec.Message.Content); ok {
			parts = append(parts, s)
			break
		}
		var raws []json.RawMessage
		_ = json.Unmarshal(rec.Message.Content, &raws)
		for _, rm := range raws {
			var b struct {
				Type    string          `json:"type"`
				Text    string          `json:"text"`
				Name    string          `json:"name"`
				Input   json.RawMessage `json:"input"`
				Content json.RawMessage `json:"content"`
			}
			if json.Unmarshal(rm, &b) != nil {
				continue
			}
			switch b.Type {
			case "text":
				parts = append(parts, b.Text)
			case "tool_use":
				parts = append(parts, "[tool_use] "+callSignature(b.Name, b.Input))
			case "tool_result":
				label = "[tool_result]"
				parts = append(parts, capRunes(recordText(b.Content), aroundRawCap))
			}
		}
	}
	return ContextBlock{Label: label, Text: redactSecrets(strings.TrimSpace(strings.Join(parts, "\n")))}
}
