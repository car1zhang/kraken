package jobs

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"kraken/internal/core"
	"kraken/internal/store"
)

// recallnotes.go — carl's unread notes, delivered at the top of zeus' next
// turn.
//
// Two things carl writes about a job go to kraken.db and never to the pane:
// per-artifact FEEDBACK rows (the board's feedback form) and the ANSWERS to
// the questions zeus itself opened (`questions.answer`, the board's answer
// buttons). Both arrive as silent row updates, and without this file the
// only route to the agent is zeus remembering to run `kraken feedback get`
// and `kraken questions get` — a rule in the brief, obeyed on resume and
// forgotten mid-run. Carl's next message after leaving a note is almost
// always ABOUT that note, so the note belongs in the context the message
// lands in.
//
// The shape is the goals block's: a block riding the one hook every cell
// already wires — UserPromptSubmit → `kraken recall-hook` → POST /api/recall
// → Recall — prefixed onto the goals block. No new hook, no settings.json
// re-merge: a cell already running gets it on its next prompt once the serve
// binary carries this file.
//
// What makes it a different kind of block from goals is its delivery rule. A
// goals block is state, re-delivered whenever it differs. A note is an
// INSTRUCTION: it is delivered exactly once per
// (job, note-as-written), and an edit to the note or a re-answer of the
// question is a new instruction, so the key below hashes the text. The record
// is per JOB rather than per session, kept in one kv row
// (`notes_delivered:<job id>`), because the one reader is the job's lead —
// see recallNotesTarget — and every lead respawn kraken performs resumes the
// SAME session (ReactivateJob, the limit rescue), so a per-session record
// would buy nothing a per-job one does not while re-dumping a job's whole
// history onto any session that was not the stamped one. The one way a lead
// gets a NEW session is `/clear` in its pane, and that trades the context
// away deliberately: notes already delivered are not re-sent, and the
// brief's resume re-read is the backstop.
//
// The three rules recall.go lives by hold here unchanged: every failure
// degrades to injecting nothing; a delivery that cannot be recorded is
// withdrawn rather than sent (it would repeat on every prompt forever); and
// nothing is recorded for a caller whose context is already gone.

// notesContextBytes caps the whole block, header and overflow tail included.
// It is an order of magnitude above the goals cap because the payload is
// different in kind: a goal is a line carl reads at a glance, a feedback note
// is an instruction zeus has to act on, and cutting it to fit is cutting the
// instruction. The cap is a backstop against a job with a long backlog
// arriving all at once — items past it are left UNDELIVERED and arrive on
// the next prompt, so the cap defers rather than drops.
const notesContextBytes = 24000

// notesTailReserve is the room renderNotesBlock keeps for the "+N more" line
// when it decides whether one more item fits, so the cap above is a cap on
// the whole block and not on the block minus its last line.
const notesTailReserve = 128

// notesItemRunes caps one item's text. A note may run to 8000 runes (the
// board's feedbackNoteMaxLen); a single item longer than this is cut with a
// marker naming the verb that reads it whole, which is the one place this
// block puts words carl did not write into his note — flagged as such, and
// only ever at the tail. With every one-line field capped at notesAskedRunes
// too, the largest possible item (4000 four-byte runes, every one a newline
// that indents to three bytes, plus marker, stamp and asked line) stays
// near 17 KB — well under notesContextBytes less the header, the
// first-contact line and notesTailReserve — which is what makes the first
// item always fit: structurally, so the backlog can never stall on an item
// that fits nowhere.
const notesItemRunes = 4000

// notesAskedRunes caps the restated question text under an answer, and the
// artifact name on a feedback line. The answer is the payload; the question
// is the reminder of what it answers, and zeus can read its own question in
// full with `kraken questions get`.
const notesAskedRunes = 240

// notesDeliveredPrefix keys the per-job delivery record in kv. The id, never
// the slug, for the reason every per-job kv row in this binary uses it
// (`limit_hit:<id>`): archiving frees a slug, and a new job must never read a
// dead generation's record as its own.
const notesDeliveredPrefix = "notes_delivered:"

// notesContextHeader opens the block. It names both pull verbs — the block is
// the delivery, the verbs are the full lists — and restates the one rule about
// feedback rows the brief already carries, because the brief is read once per
// phase and this block arrives at the moment the rule applies: a row never
// stops reading as open, so the only record of a note being handled is zeus
// SAYING so.
const notesContextHeader = "\nCarl's unread notes on this job (artifact feedback, answers to your questions, and pending mental-model flags for you to raise or dismiss; each arrives here once — full lists any time: `kraken feedback get <slug>` / `kraken questions get <slug>`). Act on every one and say in your final message what you changed — a feedback row never stops reading as open:\n"

// notesFirstContactLine follows the header the FIRST time a job's notes are
// delivered at all — the record row does not exist yet. That is the normal
// first delivery for a fresh job, and it is also the one delivery that can
// carry notes zeus already pulled by hand: a cell running when this feature
// shipped, or a done job reactivated after it, has a backlog the brief's
// resume re-read may have handled. The block does not try to tell those apart
// (nothing records a pull through the verbs), so it says so instead, and the
// brief's own rule — read the artifact against the note before redoing — is
// what keeps a handled note from being redone.
const notesFirstContactLine = "(First delivery for this job: it may include notes you already handled through the verbs — check each against the artifact before redoing anything.)\n"

// noteItem is one deliverable note: its dedup key, its rendered line(s), and
// when carl wrote it (millis), for ordering.
type noteItem struct {
	key  string
	line string
	at   int64
}

// recallNotesTarget reports whether this firing's session is the one the
// notes are addressed to: the job's stamped lead session.
//
// A cell's hook fires for every claude in the worktree — zeus, each tmux
// teammate, every side thread — all carrying the same slug. Carl's feedback is
// addressed to the agent doing the job: a teammate is briefed by zeus and would
// be acting on an instruction its lead never saw, and a side thread is carl
// himself asking about the work, who does not need his own note read back.
// `jobs.session` is zeus' session id, stamped from the daemon's first tick
// after the cell comes up and carried through every resume, so it is exactly
// the discriminator wanted. An unstamped job (the first seconds of a cell's
// life, before any note can exist) matches nothing.
func recallNotesTarget(job *store.Job, sessionID string) bool {
	if job == nil {
		return false
	}
	sessionID = strings.TrimSpace(sessionID)
	return sessionID != "" && job.Session == sessionID
}

// recallNotesState renders the unread-notes block for one firing: the block
// to inject (or ""), and the delivery keys it carries, which the caller
// commits with markNotesDelivered once it knows the delivery reached anyone.
//
// Empty is the common answer and never an error: no job, a job with no notes,
// or every note already delivered. A read failure on the record or on either
// list is ALSO empty — a prompt is never broken over a note, and the next
// firing re-reads. The record-read failure in particular must not be read as
// "nothing delivered yet": that would re-render the job's whole history as
// unread under a header saying act on every one.
//
// The record is read here and NOT written: the caller holds the per-session
// lock across both, and since only the lead's session ever reaches the write,
// one lock serializes every writer of one job's record.
func recallNotesState(st *store.Store, job *store.Job) (string, []string) {
	if st == nil || job == nil || job.ID <= 0 {
		// ID <= 0 is not a stored job: ListFeedbackMeta(0) is the UNSCOPED
		// sweep, which would hand this job every job's notes.
		return "", nil
	}
	delivered, present, err := readNotesDelivered(st, job.ID)
	if err != nil {
		return "", nil
	}
	items, ok := unreadNotes(st, job, delivered)
	if !ok || len(items) == 0 {
		return "", nil
	}
	return renderNotesBlock(items, job.Slug, !present)
}

// unreadNotes lists every note on the job whose key is not in delivered,
// oldest first. The second return is false on a store failure, which the
// caller treats as "inject nothing" — distinct from an empty list, which is a
// job that is simply up to date.
func unreadNotes(st *store.Store, job *store.Job, delivered map[string]int64) ([]noteItem, bool) {
	feedback, err := st.ListFeedbackMeta(job.ID)
	if err != nil {
		return nil, false
	}
	questions, err := st.ListQuestions(job.ID)
	if err != nil {
		return nil, false
	}
	var items []noteItem
	for _, f := range feedback {
		note := strings.TrimSpace(f.Note)
		if note == "" {
			continue
		}
		item := noteItem{key: feedbackNoteKey(f), at: f.TS}
		item.line = "- feedback #" + strconv.FormatInt(f.ID, 10)
		if art := strings.TrimSpace(f.Artifact); art != "" {
			item.line += " on " + core.TruncateOneLine(art, notesAskedRunes)
		}
		item.line += " (" + notesWhen(f.TS) + "): " + notesText(note, job.Slug) + "\n"
		items = append(items, item)
	}
	for _, q := range questions {
		answer := strings.TrimSpace(q.Answer)
		if answer == "" {
			continue
		}
		at := notesParseRFC3339(q.AnsweredAt)
		item := noteItem{key: questionNoteKey(q), at: at}
		item.line = "- " + q.QID + " answered" + notesWhenSuffix(q.AnsweredAt, at) + ": " + notesText(answer, job.Slug) + "\n"
		if asked := core.TruncateOneLine(q.Text, notesAskedRunes); asked != "" {
			item.line += "  (asked: " + asked + ")\n"
		}
		items = append(items, item)
	}
	// The third source: pending contradiction flags on this job's mental-model
	// items, raised by a teammate (or anyone but the lead) and waiting on the
	// lead's raise/dismiss. Only PENDING ones — a raised or dismissed flag is
	// already decided, and the lead is the one who decided it.
	flags, ok := pendingJobFlags(st, job.ID)
	if !ok {
		return nil, false
	}
	for _, f := range flags {
		item := noteItem{key: flagNoteKey(f), at: f.CreatedAt}
		item.line = "- mm flag #" + strconv.FormatInt(f.ID, 10) + " on " + mmFlagItems(f) + " (" + notesWhen(f.CreatedAt) + "): " + notesText(f.Note, job.Slug) + "\n" +
			"  (verify it, then `kraken mm flag raise " + strconv.FormatInt(f.ID, 10) + " --note \"…\"` or `kraken mm flag dismiss " + strconv.FormatInt(f.ID, 10) + " --note \"…\"`)\n"
		items = append(items, item)
	}
	kept := items[:0]
	for _, it := range items {
		if _, seen := delivered[it.key]; !seen {
			kept = append(kept, it)
		}
	}
	// Chronological across both kinds, so the block reads as what carl did
	// in the order he did it; stable, so two rows stamped alike keep their
	// list order (feedback by ts then questions by position).
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].at < kept[j].at })
	return kept, true
}

// feedbackNoteKey is a feedback row's delivery key: its id plus a hash of its
// note. UpdateFeedbackNote rewrites the note in place (ts untouched), and an
// edited note is a new instruction — so the key moves with the text and the
// row is delivered again, while the stale key is pruned at the next write.
func feedbackNoteKey(f store.Feedback) string {
	return "f:" + strconv.FormatInt(f.ID, 10) + ":" + notesHash(f.Note)
}

// questionNoteKey is an answered question's delivery key: its qid plus a hash
// of answer and answered_at together. A re-answer changes both; an answer
// edited to the same text at a new time is still a fresh click of carl's and
// is delivered again, which is the conservative reading.
func questionNoteKey(q store.DocQuestion) string {
	return "q:" + q.QID + ":" + notesHash(q.Answer+"\x00"+q.AnsweredAt)
}

// flagNoteKey is a pending mm flag's delivery key: its id alone. A flag's
// note is never edited after creation, so there is no text to hash — and once
// the lead raises or dismisses it, it leaves the pending list and its key is
// pruned like any other note that stopped existing.
func flagNoteKey(f store.MMFlag) string {
	return "m:" + strconv.FormatInt(f.ID, 10)
}

// pendingJobFlags lists jobID's pending flags; false on a store failure.
func pendingJobFlags(st *store.Store, jobID int64) ([]store.MMFlag, bool) {
	all, err := st.ListMMFlags("pending")
	if err != nil {
		return nil, false
	}
	var out []store.MMFlag
	for _, f := range all {
		if f.JobID == jobID {
			out = append(out, f)
		}
	}
	return out, true
}

// notesHash is the text half of a delivery key: 64-bit FNV-1a, hex. It is a
// change detector, not a credential, so a non-cryptographic hash is enough;
// what a collision would cost is one edited note whose new text hashes to
// its old value and is therefore not re-delivered, at odds of 2^-64 per edit.
func notesHash(s string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return strconv.FormatUint(h.Sum64(), 16)
}

// renderNotesBlock lays the items out under the header within
// notesContextBytes, oldest first, and returns the block with the keys of
// exactly the items it carries. An item that does not fit stops the block —
// not skipped-over, since a later shorter item delivered ahead of an earlier
// longer one would put carl's notes out of order — and the tail says how many
// wait, so zeus knows the list it sees is not the whole list. first adds the
// first-contact line (see notesFirstContactLine) and extends its caveat onto
// the tail, since a deferred backlog's later chunks arrive under a record
// that now exists and so carry no first-contact line of their own.
func renderNotesBlock(items []noteItem, slug string, first bool) (string, []string) {
	var b strings.Builder
	b.WriteString(strings.ReplaceAll(notesContextHeader, "<slug>", slug))
	if first {
		b.WriteString(notesFirstContactLine)
	}
	var keys []string
	for i, it := range items {
		if b.Len()+len(it.line)+notesTailReserve > notesContextBytes {
			remaining := len(items) - i
			fmt.Fprintf(&b, "- (+%d more, held for your next prompt — or read them now with the verbs above", remaining)
			if first {
				b.WriteString("; the same caveat applies to them")
			}
			b.WriteString(")\n")
			break
		}
		b.WriteString(it.line)
		keys = append(keys, it.key)
	}
	if len(keys) == 0 {
		// Unreachable while notesItemRunes' arithmetic holds; stated rather
		// than assumed, because an empty-keys block would deliver and
		// re-deliver the same tail line forever.
		return "", nil
	}
	return b.String(), keys
}

// notesText renders a note's body for the block: line endings normalized
// and other control characters dropped (a pasted terminal capture must not
// put an ESC into the model's context), continuation lines indented so a
// multi-paragraph note reads as one item rather than as several, and cut at
// notesItemRunes with a marker naming where the whole text lives.
func notesText(s, slug string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > notesItemRunes {
		s = notesCut(s, notesItemRunes) + " [cut — the whole note: `kraken feedback get " + slug + "` / `kraken questions get " + slug + "`]"
	}
	return strings.ReplaceAll(s, "\n", "\n  ")
}

// notesCut truncates s to at most n runes, appending an ellipsis when it did.
// It keeps newlines, which is why notesText cannot use core.TruncateOneLine.
func notesCut(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	runes := []rune(s)
	return strings.TrimSpace(string(runes[:n])) + "…"
}

// notesWhen renders a millis stamp in local time, to the minute — the
// resolution a "since your last turn" reads at.
func notesWhen(ms int64) string {
	return time.UnixMilli(ms).Local().Format("2006-01-02 15:04")
}

// notesWhenSuffix renders an answered_at for the answer line: " (<local
// stamp>)" when it parsed, " (<raw>)" when it did not (a stamp kraken itself
// wrote in an unexpected shape is still more information than nothing), and
// nothing at all when it is empty — an empty pair of parentheses says less
// than no parentheses.
func notesWhenSuffix(raw string, ms int64) string {
	if ms != 0 {
		return " (" + notesWhen(ms) + ")"
	}
	if raw = strings.TrimSpace(raw); raw != "" {
		// a well-formed RFC3339 stamp is 20-35 runes; this caps only a malformed one
		return " (" + core.TruncateOneLine(raw, 40) + ")"
	}
	return ""
}

// notesParseRFC3339 reads an answered_at (webAnswerQuestion stamps RFC3339
// UTC) to millis, 0 when it does not parse — which sorts an unparseable
// stamp first rather than dropping the answer.
func notesParseRFC3339(s string) int64 {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}

// notesDeliveredKey is the kv key of jobID's delivery record.
func notesDeliveredKey(jobID int64) string {
	return notesDeliveredPrefix + strconv.FormatInt(jobID, 10)
}

// notesRecord is the kv row's JSON shape.
type notesRecord struct {
	Delivered map[string]int64 `json:"delivered"`
}

// readNotesDelivered returns jobID's delivery record (key → millis first
// delivered), whether a usable row existed, and the read error if any.
//
// Three outcomes, and keeping them apart is the point. A missing row is an
// empty record with present=false: the job has never had a delivery, which
// is what notesFirstContactLine keys on. A row that exists but does not
// decode is ALSO an empty record with present=false — the history is unknown
// either way, which is exactly what the first-contact caveat exists to say;
// a corrupt row costs one repeated delivery (under that caveat) and the next
// write overwrites it with valid JSON, which self-heals. A read that FAILED —
// the kv query itself erroring, as a busy or sick sqlite can — is an error
// and nothing else: folding it into "empty" would re-render the job's whole
// history as unread, and at the write site it would wipe the record
// (markNotesDelivered rebuilds the row from what it read).
func readNotesDelivered(st *store.Store, jobID int64) (map[string]int64, bool, error) {
	raw, err := st.GetKV(notesDeliveredKey(jobID))
	if errors.Is(err, store.ErrNotFound) {
		return map[string]int64{}, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var rec notesRecord
	if err := json.Unmarshal([]byte(raw), &rec); err != nil || rec.Delivered == nil {
		return map[string]int64{}, false, nil
	}
	return rec.Delivered, true, nil
}

// markNotesDelivered records keys as delivered for jobID at ts, PRUNING any
// recorded key that no longer names a live note (a deleted row, a note whose
// edit moved its key) so the record stays the size of the job's note list
// rather than growing with its history. One read-modify-write; the caller's
// per-session lock is what keeps two of them from interleaving, per
// recallNotesState's doc comment.
//
// An error is the caller's cue to WITHDRAW the block: a delivery this
// function could not record is one that would repeat on every prompt. A
// failed READ of the existing record is such an error too — rebuilding the
// row from an empty read would erase every earlier delivery.
func markNotesDelivered(st *store.Store, job *store.Job, keys []string, ts int64) error {
	if st == nil || job == nil || job.ID <= 0 {
		return errors.New("mark notes delivered: no store or job")
	}
	if len(keys) == 0 {
		return nil
	}
	delivered, _, err := readNotesDelivered(st, job.ID)
	if err != nil {
		return fmt.Errorf("mark notes delivered: %w", err)
	}
	for _, k := range keys {
		if _, ok := delivered[k]; !ok {
			delivered[k] = ts
		}
	}
	// Prune against the live note set. A list failure here skips the prune
	// rather than the write: an unpruned record is merely larger, an
	// unwritten one re-delivers.
	if live, ok := liveNoteKeys(st, job); ok {
		for k := range delivered {
			if !live[k] {
				delete(delivered, k)
			}
		}
	}
	raw, err := json.Marshal(notesRecord{Delivered: delivered})
	if err != nil {
		return fmt.Errorf("mark notes delivered: %w", err)
	}
	return st.SetKV(notesDeliveredKey(job.ID), string(raw))
}

// liveNoteKeys is the set of delivery keys the job's notes carry RIGHT NOW,
// for the prune; false on a store failure.
func liveNoteKeys(st *store.Store, job *store.Job) (map[string]bool, bool) {
	feedback, err := st.ListFeedbackMeta(job.ID)
	if err != nil {
		return nil, false
	}
	questions, err := st.ListQuestions(job.ID)
	if err != nil {
		return nil, false
	}
	live := map[string]bool{}
	for _, f := range feedback {
		live[feedbackNoteKey(f)] = true
	}
	for _, q := range questions {
		live[questionNoteKey(q)] = true
	}
	flags, ok := pendingJobFlags(st, job.ID)
	if !ok {
		return nil, false
	}
	for _, f := range flags {
		live[flagNoteKey(f)] = true
	}
	return live, true
}
