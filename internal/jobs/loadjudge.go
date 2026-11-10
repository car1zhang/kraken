package jobs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"kraken/internal/core"
	"kraken/internal/store"
)

// loadjudge.go — the daemon pass that writes a blocked job's load estimate.
//
// A blocked card carries two numbers, unblock and scope, and the board ranks
// the blocked column on scope/unblock. Zeus used to write them itself at the
// end of every turn, from the same ruler, so a cell priced its own gate and
// the numbers were as comparable across cells as the cells were honest about
// themselves. So the estimate is now a JUDGMENT the daemon makes about the
// lead's final message, through TypeSafe's Jev model, once per turn:
//
//   - The tick that confirms a live cell blocked reads the lead's transcript
//     for its last text block — the message the board shows as the card's
//     detail — and keys the turn on that record's uuid. A turn already judged
//     is not judged again; a cell that goes blocked, working and blocked
//     again on the same message gets the same answer back without a call.
//   - Jev is asked eighteen yes/no questions, one per rung above the first
//     of each ladder: "does the operator's actual move cost at least this
//     much", "does the reply buy at least this much work". A ladder lands
//     on the highest rung whose answer and every answer below it is yes
//     (loadJudgeLand), which is a rubric that lands a value rather than a
//     number the model is asked to produce — the operator's own rule for
//     LLM scoring.
//   - The pair is written through SetJobLoad with a note naming the two
//     rungs it landed on, and remembered in kv (load_judged:<job id>) so a
//     serve restart re-applies the verdict rather than re-asking.
//   - A tick that confirms the cell working clears the estimate: an
//     estimate exists only while the operator is the blocker. A NEW turn
//     found under blocked clears the previous turn's estimate before Jev is
//     asked, too: a turn that starts and ends between two ticks never shows
//     a working tick, and without that clear a failed judgment would leave
//     the old gate's price on the new gate's card.
//
// The feature shares the dialog answerer's switch: it is off unless
// TYPESAFE_API_KEY is set, and with it off a blocked card simply carries no
// badge. Every failure degrades to "no estimate this tick", never to a
// wrong number: an API error is logged once per turn and retried after a
// minute, an unreadable transcript is skipped, and a malformed answer is
// logged and not written.
//
// Two bounds on "once per turn" are accepted rather than fixed. A turn that
// ends on tool calls with no new text keeps the previous text block's key,
// so it is read as the same turn (the brief mandates a final message, so
// this is the exception). And a codex rollout, which carries no record
// uuid, is keyed on a hash of the text, so two gates ending in byte-identical
// messages share a verdict.

// The two bounds on the pass: how many Jev calls one tick may make across
// every job (an API outage then holds the tick for at most three client
// timeouts), and how long a job waits before a failed call is retried.
const (
	loadJudgeMaxCallsPerTick = 3
	loadJudgeRetryAfter      = 60 * time.Second
)

// loadJudgeYes is the noul value at and above which a rubric question reads
// as yes.
const loadJudgeYes = 0.5

// loadJudgeMessageMax caps the final message handed to Jev, in runes. A
// message over it keeps its head (the TLDR sentence) and its tail (the
// gate line), with the middle cut.
const loadJudgeMessageMax = 4000

// loadJudgeKVPrefix keys the persisted verdict per job: load_judged:<id>.
const loadJudgeKVPrefix = "load_judged:"

// loadJudgeBlocks is how many trailing blocks the transcript tail is read
// for. The last text block is normally the last block; the slack covers the
// tool blocks and bookkeeping records Claude Code writes after a turn.
const loadJudgeBlocks = 20

// loadJudgeQuestionsMax caps how many open questions ride in the request; the
// number left out is sent beside them.
const loadJudgeQuestionsMax = 20

// loadVerdict is one judged turn: the turn key it answers and the pair it
// landed on. It is what the kv row holds and what a same-turn re-block gets
// back without a call.
type loadVerdict struct {
	Key     string `json:"key"`
	Unblock int    `json:"unblock"`
	Scope   int    `json:"scope"`
	At      int64  `json:"at"`
}

// hasPair says whether the verdict carries a pair that can be written: both
// rungs on their ladders. A verdict with no pair is a turn whose answer was
// malformed, and one off the ladder (a hand-edited or foreign kv row) is
// treated the same way rather than indexed — the ladders are fixed arrays,
// and the daemon has no recover() around the tick.
func (v loadVerdict) hasPair() bool {
	return v.Unblock >= 1 && v.Unblock <= len(UnblockLadder) && v.Scope >= 1 && v.Scope <= len(ScopeLadder)
}

// loadJudgeMemo is one job's in-process memory for the pass.
type loadJudgeMemo struct {
	// verdict is the last judgment, loaded from kv on first use (loaded
	// says whether that read happened) and replaced on every new one.
	verdict loadVerdict
	loaded  bool
	// lastAsked bounds how often a failing call is retried for one turn;
	// errKey is the turn whose API error was already logged; writeFailed
	// says a store write failed and was logged, until one succeeds.
	lastAsked   time.Time
	errKey      string
	writeFailed bool
}

// loadJudgeState is the pass' memory for one daemon process.
type loadJudgeState struct {
	jobs map[int64]*loadJudgeMemo
	// calls counts the Jev calls made on tick callsTick.
	calls     int
	callsTick int
}

func newLoadJudgeState() *loadJudgeState {
	return &loadJudgeState{jobs: map[int64]*loadJudgeMemo{}}
}

func (s *loadJudgeState) memo(id int64) *loadJudgeMemo {
	if s.jobs == nil {
		s.jobs = map[int64]*loadJudgeMemo{}
	}
	m := s.jobs[id]
	if m == nil {
		m = &loadJudgeMemo{}
		s.jobs[id] = m
	}
	return m
}

// The pass' side effects, each a var so a test can replace it and never
// reach the network or a transcript on disk.
var (
	loadJudgeSystemOne = func(c *core.TypeSafeClient, req core.SystemOneRequest) (core.SystemOneResponse, error) {
		return c.SystemOne(req)
	}
	loadJudgeClient = func() *core.TypeSafeClient {
		return core.NewTypeSafeClient(core.LoadTypeSafeConfig())
	}
	// loadJudgeTurn reads the lead's last turn off its transcript: the key
	// that identifies it and the text the board shows for it. ok is false
	// when there is no text block to judge.
	loadJudgeTurn = func(path string) (key, text string, ok bool) {
		return lastTurn(path)
	}
)

// lastTurn finds the transcript's last text block. The key is the record's
// uuid when the transcript carries one (a claude transcript always does), else
// a hash of the text, which is what a codex rollout gets.
func lastTurn(path string) (key, text string, ok bool) {
	if path == "" {
		return "", "", false
	}
	if _, err := os.Stat(path); err != nil {
		return "", "", false
	}
	blocks, _, err := core.LastBlocks(path, loadJudgeBlocks)
	if err != nil {
		return "", "", false
	}
	for i := len(blocks) - 1; i >= 0; i-- {
		if blocks[i].Kind != "text" || strings.TrimSpace(blocks[i].Text) == "" {
			continue
		}
		key = blocks[i].UUID
		if key == "" {
			sum := sha256.Sum256([]byte(blocks[i].Text))
			key = "sha:" + hex.EncodeToString(sum[:8])
		}
		return key, blocks[i].Text, true
	}
	return "", "", false
}

// loadJudgeContext is the job as the judge sees it beside the message.
type loadJudgeContext struct {
	Slug   string        `json:"slug"`
	Frozen bool          `json:"frozen"`
	Stage  string        `json:"stage,omitempty"`
	PRs    []loadJudgePR `json:"prs"`
	// Questions are the job's OPEN questions (an answered one no longer
	// blocks anything), at most loadJudgeQuestionsMax of them in row order,
	// with the number left out in QuestionsOmitted.
	Questions        []loadJudgeQuestion `json:"open_questions"`
	QuestionsOmitted int                 `json:"open_questions_omitted,omitempty"`
	Stack            []loadJudgeStack    `json:"stack"`
}

type loadJudgePR struct {
	URL   string `json:"url"`
	State string `json:"state"`
}

type loadJudgeQuestion struct {
	QID        string `json:"qid"`
	Importance string `json:"importance"`
	Text       string `json:"text"`
}

type loadJudgeStack struct {
	Label  string `json:"label"`
	URL    string `json:"url"`
	Status string `json:"status"`
}

// loadJudgeQuestionTextMax caps a question's text in the request.
const loadJudgeQuestionTextMax = 200

// loadJudgeContextFor gathers the job's open questions, PRs and stack links.
// Every read failure degrades to an empty list; the message is the evidence,
// the rest is context.
func loadJudgeContextFor(st *store.Store, j *Job, transcript string) loadJudgeContext {
	ctx := loadJudgeContext{
		Slug:      j.Slug,
		Frozen:    j.FrozenAt != 0,
		Stage:     FindStage(transcript),
		PRs:       []loadJudgePR{},
		Questions: []loadJudgeQuestion{},
		Stack:     []loadJudgeStack{},
	}
	for _, pr := range j.PRs {
		ctx.PRs = append(ctx.PRs, loadJudgePR{URL: pr.URL, State: pr.State})
	}
	if st != nil {
		if rows, err := st.ListQuestions(j.ID); err == nil {
			for _, q := range rows {
				if strings.TrimSpace(q.Answer) != "" {
					continue
				}
				if len(ctx.Questions) >= loadJudgeQuestionsMax {
					ctx.QuestionsOmitted++
					continue
				}
				ctx.Questions = append(ctx.Questions, loadJudgeQuestion{
					QID:        q.QID,
					Importance: q.Importance,
					Text:       core.TruncateOneLine(core.RedactSecrets(q.Text), loadJudgeQuestionTextMax),
				})
			}
		}
		if links, err := st.ListStacks(j.ID); err == nil {
			for _, l := range links {
				ctx.Stack = append(ctx.Stack, loadJudgeStack{Label: l.Label, URL: l.URL, Status: l.Status})
			}
		}
	}
	return ctx
}

// loadJudgeMessage bounds the final message: control characters other than
// newlines become spaces, and a message over loadJudgeMessageMax runes keeps
// its head and tail.
func loadJudgeMessage(text string) string {
	text = strings.Map(func(r rune) rune {
		if r == '\n' {
			return r
		}
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, text)
	r := []rune(text)
	if len(r) <= loadJudgeMessageMax {
		return text
	}
	half := loadJudgeMessageMax / 2
	return string(r[:half]) + "\n…\n" + string(r[len(r)-half:])
}

// Question keys: u2..u10 for the unblock ladder, s2..s10 for scope.
func loadUnblockKey(rung int) string { return "u" + strconv.Itoa(rung) }
func loadScopeKey(rung int) string   { return "s" + strconv.Itoa(rung) }

// loadJudgeRequest builds the System One request for one turn: the
// directive, the job context and the message as state, and eighteen noul
// questions as the rubric. Each question asks whether its axis reaches AT
// LEAST its rung, so the answers form a ladder loadJudgeLand can climb.
func loadJudgeRequest(directive string, ctx loadJudgeContext, message, model string) core.SystemOneRequest {
	qs := map[string]core.SystemOneQuestion{}
	for i := 1; i < len(UnblockLadder); i++ {
		r := UnblockLadder[i]
		qs[loadUnblockKey(i+1)] = core.SystemOneQuestion{
			Type: "noul",
			Instructions: fmt.Sprintf("Does the operator's ACTUAL next move cost them at least as much as unblock rung %d, %q: %s Answer yes only if unblocking costs at least that much.",
				i+1, r.Label, r.Recognize),
		}
	}
	for i := 1; i < len(ScopeLadder); i++ {
		r := ScopeLadder[i]
		qs[loadScopeKey(i+1)] = core.SystemOneQuestion{
			Type: "noul",
			Instructions: fmt.Sprintf("Is the claude work the operator's reply would BUY — strictly the work still ahead, never the work under review — at least as big as scope rung %d, %q: %s Answer yes only if the reply buys at least that much.",
				i+1, r.Label, r.Recognize),
		}
	}
	return core.SystemOneRequest{
		Model: model,
		State: map[string]any{
			"directive":    directive,
			"job":          ctx,
			"last_message": message,
		},
		Questions: qs,
	}
}

// loadJudgeLand climbs one ladder: rung 1 always holds, and each higher rung
// holds while its answer and every answer below it reads yes. A ladder
// missing any of its nine answers is a malformed response (ok false) rather
// than a short ladder: a missing rung would otherwise land every job on the
// rung below it, silently.
func loadJudgeLand(resp core.SystemOneResponse, key func(int) string) (rung int, ok bool) {
	rung = 1
	climbing := true
	for r := 2; r <= 10; r++ {
		a, found := resp.Answers[key(r)]
		if !found {
			return 0, false
		}
		if climbing && a.Noul >= loadJudgeYes {
			rung = r
		} else {
			climbing = false
		}
	}
	return rung, true
}

// loadJudgeNote is the estimate's one-line rationale: the two rungs it landed
// on, by label, so the card's badge tooltip says what the numbers mean.
func loadJudgeNote(unblock, scope int) string {
	return "jev: " + UnblockLadder[unblock-1].Label + " · " + ScopeLadder[scope-1].Label
}

// loadJudgeAnswers renders one ladder's nine answers for the log line.
func loadJudgeAnswers(resp core.SystemOneResponse, key func(int) string) string {
	parts := make([]string, 0, 9)
	for r := 2; r <= 10; r++ {
		parts = append(parts, fmt.Sprintf("%d=%.2f", r, resp.Answers[key(r)].Noul))
	}
	return strings.Join(parts, " ")
}

// loadJudgePass judges one job on one tick. st is the tick's CONFIRMED state
// for the job (after holdBlocked), live whether its cell session exists.
//
// working clears a standing estimate. blocked reads the lead's last turn; a
// new turn first clears any standing estimate (it prices the previous gate)
// and is then judged, a turn already judged has its remembered verdict
// re-applied when the row is empty (the cell read working for a tick in
// between), and otherwise nothing happens. Any other state is left alone.
func (s *loadJudgeState) loadJudgePass(j *Job, st string, live bool, all []*core.Peer, now time.Time, tickN int, logf func(level, event, msg string)) {
	if s == nil || j == nil || j.ID == 0 || !j.Started() || j.State == "done" {
		return
	}
	db := ActiveStore()
	if db == nil {
		return
	}
	if s.callsTick != tickN {
		s.callsTick, s.calls = tickN, 0
	}
	switch {
	case st == "working":
		if j.Load.At != 0 {
			if err := s.clear(j, "working", logf); err != nil {
				return
			}
		}
		return
	case st != "blocked" || !live:
		return
	}

	transcript := zeusTranscriptPath(j, all)
	key, text, ok := loadJudgeTurn(transcript)
	if !ok {
		return
	}
	m := s.memo(j.ID)
	if !m.loaded {
		m.loaded = true
		if raw, err := db.GetKV(loadJudgeKVPrefix + strconv.FormatInt(j.ID, 10)); err == nil {
			var v loadVerdict
			if json.Unmarshal([]byte(raw), &v) == nil {
				m.verdict = v
			}
		}
	}
	if m.verdict.Key == key {
		if j.Load.At == 0 && m.verdict.hasPair() {
			// the same turn, back under blocked after a working tick: the
			// verdict stands and costs no call
			if err := s.write(j, m.verdict, now, logf); err == nil {
				logf("info", "load-judge", fmt.Sprintf("slug=%s action=reapplied key=%s unblock=%d scope=%d", j.Slug, key, m.verdict.Unblock, m.verdict.Scope))
			}
		}
		return
	}
	if j.Load.At != 0 {
		// a new turn under a standing estimate: the row prices the PREVIOUS
		// gate, so it goes before anything can fail to replace it
		if err := s.clear(j, "new-turn", logf); err != nil {
			return
		}
	}

	client := loadJudgeClient()
	if client == nil {
		return
	}
	if !m.lastAsked.IsZero() && now.Sub(m.lastAsked) < loadJudgeRetryAfter {
		return
	}
	if s.calls >= loadJudgeMaxCallsPerTick {
		return
	}
	s.calls++
	m.lastAsked = now
	model := core.LoadTypeSafeConfig().Model
	ctx := loadJudgeContextFor(db, j, transcript)
	message := loadJudgeMessage(core.RedactSecrets(text))
	resp, err := loadJudgeSystemOne(client, loadJudgeRequest(loadDirective, ctx, message, model))
	if err != nil {
		// logged once per turn; the retry gate above spaces the re-asks
		if m.errKey != key {
			m.errKey = key
			logf("error", "load-judge", fmt.Sprintf("slug=%s action=api-error key=%s err=%q", j.Slug, key, err.Error()))
		}
		return
	}
	if resp.Model != "" {
		model = resp.Model
	}
	unblock, okU := loadJudgeLand(resp, loadUnblockKey)
	scope, okS := loadJudgeLand(resp, loadScopeKey)
	if !okU || !okS {
		// a malformed answer is remembered as this turn's verdict with no
		// pair, so it is logged once and never written; a later turn is
		// judged afresh
		m.verdict = loadVerdict{Key: key}
		logf("error", "load-judge", fmt.Sprintf("slug=%s action=bad-answer key=%s model=%s answers=%s", j.Slug, key, model, loadJudgeAnswerKeys(resp)))
		return
	}
	v := loadVerdict{Key: key, Unblock: unblock, Scope: scope, At: now.UnixMilli()}
	if err := s.write(j, v, now, logf); err != nil {
		return
	}
	m.verdict = v
	m.lastAsked = time.Time{}
	if raw, err := json.Marshal(v); err == nil {
		if err := db.SetKV(loadJudgeKVPrefix+strconv.FormatInt(j.ID, 10), string(raw)); err != nil {
			logf("error", "load-judge", fmt.Sprintf("slug=%s action=remember-failed err=%q", j.Slug, err.Error()))
		}
	}
	logf("info", "load-judge", fmt.Sprintf("slug=%s action=judged key=%s unblock=%d scope=%d model=%s u=[%s] s=[%s]",
		j.Slug, key, unblock, scope, model, loadJudgeAnswers(resp, loadUnblockKey), loadJudgeAnswers(resp, loadScopeKey)))
}

// write lands a verdict on the job row, stamped now rather than at the
// verdict's own At so a re-applied verdict reads as current on the card. A
// failed write is logged once per job until one succeeds, since the reapply
// path would otherwise retry it every tick.
func (s *loadJudgeState) write(j *Job, v loadVerdict, now time.Time, logf func(level, event, msg string)) error {
	l := store.JobLoad{Unblock: v.Unblock, Scope: v.Scope, Note: loadJudgeNote(v.Unblock, v.Scope), At: now.UnixMilli()}
	m := s.memo(j.ID)
	if err := SetJobLoad(j, l); err != nil {
		if !m.writeFailed {
			m.writeFailed = true
			logf("error", "load-judge", fmt.Sprintf("slug=%s action=write-failed err=%q", j.Slug, err.Error()))
		}
		return err
	}
	m.writeFailed = false
	return nil
}

// clear zeroes the job's estimate; why names the tick that asked for it.
func (s *loadJudgeState) clear(j *Job, why string, logf func(level, event, msg string)) error {
	if err := SetJobLoad(j, loadCleared); err != nil {
		logf("error", "load-judge", fmt.Sprintf("slug=%s action=clear-failed why=%s err=%q", j.Slug, why, err.Error()))
		return err
	}
	logf("info", "load-judge", fmt.Sprintf("slug=%s action=cleared why=%s", j.Slug, why))
	return nil
}

// loadCleared is the clear: the zero row, note included.
var loadCleared = store.JobLoad{}

// loadJudgeAnswerKeys lists the keys an answer carried, sorted, for the
// bad-answer log line.
func loadJudgeAnswerKeys(resp core.SystemOneResponse) string {
	keys := make([]string, 0, len(resp.Answers))
	for k := range resp.Answers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}
