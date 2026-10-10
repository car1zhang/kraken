package jobs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"kraken/internal/core"
)

// limithook.go — `kraken limit-hook <slug>` and its standing-agent sibling
// `kraken limit-hook --agent <id>`, wired as a Claude Code StopFailure hook
// in each cell's worktree (cell.go's mergeHookSettings writes the entry,
// matcher "rate_limit"); the --agent form is installed nowhere today and
// stays as the standing agent's reporting verb. It is the detection half of the mid-run usage-limit
// rescue: when a claude dies on a usage limit, Claude Code fires this hook,
// and this verb reports the failure to kraken serve so the daemon can swap
// the main login to a healthier pool account without anyone watching the
// pane.
//
// The payload shapes below follow the SHIPPING binary rather than the
// published hook docs, which disagree: Claude Code 2.1.222 builds a
// StopFailure payload as {...common, hook_event_name, error, error_details?,
// last_assistant_message?} — the field is `error`, not `error_type`, and
// there is no api_error object. That was confirmed by capturing a real
// payload from a StopFailure hook fired with a deliberately-invalid key:
// {"session_id":…,"transcript_path":…,"cwd":…,"prompt_id":…,
// "effort":{"level":"high"},"hook_event_name":"StopFailure",
// "error":"authentication_failed","last_assistant_message":"Invalid API key
// · Fix external API key"} — no error_details, no agent_id. Absent optional
// fields are therefore the NORMAL case here, not a malformed payload, and
// the documented names are still accepted as a fallback because the schema
// is Claude Code's to change, not kraken's.
//
// Same rule as prhook.go: a hook that fails the turn it fires on is a bug,
// not a safety net. Every outcome — unreadable stdin, an unparseable
// payload, an unreachable serve, a non-200 — is one stderr line and a
// normal return. The hook process also never opens kraken.db: it POSTs, and
// kraken serve (the sole writer) records.
//
// This file holds BOTH sides of that hop, which is why the ActiveStore()
// calls below do not contradict the sentence above. LimitHook,
// LimitHookAgent and parseLimitPayload run in the reporting process Claude
// Code spawned, and touch nothing but stdin and one HTTP request.
// ReadLimitHit, RecordLimitHit, their keyed siblings and
// mergeLimitHitDelivery are the server's half: they key on a job's immutable
// id (or a standing agent's fixed id) and run only inside kraken serve — the
// API handler on the way in, the rescue passes on the way out.

// LimitHit is the per-job record of a usage-limit failure, stored as one kv
// row (limitHitKey) and folded across the herd of deliveries one limit
// produces — every claude in the cell fires its own hook for the same wall.
//
// At, HandledAt, LastAttempt, SwappedAt and ResetAt are unix SECONDS, not
// millis: the rescue pass reads them back with time.Unix(x, 0) to compare
// against its settle/backoff windows, so one unit has to hold across the
// whole record and this is it.
//
// ResetAt is the moment the exhausted window reopens, parsed off the banner
// text in Details ("resets 4:50am (America/New_York)"), zero when no such
// clause is parseable. Owned by the rescue pass like the attempt counters:
// it is what lets a hit that parks with no rotation target stay unresolved
// and have its zeus resumed on the first tick after the window actually
// reopens, instead of the cell sitting dead for hours past a reset the
// banner itself named.
//
// At == 0 means no hit is recorded at all; HandledAt == 0 means a recorded
// hit is still unresolved. SwappedAt/SwappedTo are ORIGINATED only by the
// rescue pass (limitrescue.go): nothing here ever decides that a swap
// happened. This file does carry them — a fold leaves them alone and a
// supersession copies an unresolved pair onto the new record (see
// mergeLimitHitDelivery) — and that carrying is the point, because a landed
// swap forgotten between two ticks is a cell swapped twice.
type LimitHit struct {
	Error       string `json:"error"`
	Details     string `json:"details"`
	At          int64  `json:"at"`
	SessionID   string `json:"session_id"`
	AgentID     string `json:"agent_id"`
	Deliveries  int    `json:"deliveries"`
	HandledAt   int64  `json:"handled_at"`
	Outcome     string `json:"outcome"`
	Attempts    int    `json:"attempts"`
	LastAttempt int64  `json:"last_attempt_at"`
	SwappedAt   int64  `json:"swapped_at"`
	SwappedTo   string `json:"swapped_to"`
	ResetAt     int64  `json:"reset_at"`
	// SourceAccount is which account was exhausted: the job's bound account
	// email at the moment the hit OPENED, empty for a cell that was running
	// unbound on the machine's main login. Written on a fresh record only —
	// a delivery fold never touches it, since the herd can outlive the
	// rebind and would otherwise relabel the exhausted account with the
	// destination.
	//
	// Its reader is the rescue's rotate arm, which prefers it over the row's
	// CURRENT email for the "from" side of the notification and the event.
	// The two diverge in exactly the case this file documents at length: a
	// rebind that landed but whose saveLimitRescueState write was lost, where
	// the row already says B while the window that actually failed was A.
	SourceAccount string `json:"source_account,omitempty"`
	// Thread is the side thread's tid when the delivery came from a thread's
	// pane rather than zeus' or a teammate's (threadLimitHitTID below), and
	// such a record lives under threadLimitHitKey, never under the job's own
	// key. Empty on every record the cell's rescue chain reads, which is the
	// property the field exists to make visible: the rescue's two tools —
	// rebind the cell, kill and resume zeus — cannot reach a thread's pane,
	// so a thread's hit recorded as the cell's buys a rebind and a cold
	// re-prime of an idle zeus for a conversation that stays exactly as
	// limited as it was.
	Thread string `json:"thread,omitempty"`
}

// threadLimitHitKey is the kv key a SIDE THREAD's LimitHit lives under:
// the job's own key with the thread's tid appended. It cannot collide with
// limitHitKey (a job's key ends in its decimal id) or standingHitKey (no
// standing id contains a colon), and it is keyed on the job id for the
// reason limitHitKey is. Nothing reads it back today except the fold and the
// notification dedup in recordThreadLimitHit; it exists so the observation
// is still RECORDED — a thread dying on a limit is a fact about the account —
// without entering the row the cell's rescue chain acts on.
func threadLimitHitKey(jobID int64, tid string) string { return limitHitKey(jobID) + ":" + tid }

// threadLimitHitNotifyGap is how long after a thread's recorded hit a repeat
// delivery from the same thread stays silent. A thread has no crew, so its
// herd is carl retrying the same dead turn by hand; one notification per
// incident is the useful number, and the record is written on every delivery
// regardless.
const threadLimitHitNotifyGap = 10 * time.Minute

// threadLimitHitTID resolves a delivery's session_id against the job's side
// threads: the tid when the session is one of this job's thread rows (live or
// closed — a thread that carl killed between the hit and the record is still
// the thread that hit), "" otherwise. Errorless on purpose and "" on every
// failure (no store, a lookup error, a session of another job's thread):
// "" routes the delivery to the cell's own record, which is where every
// non-thread delivery went before this existed, so a lookup that cannot
// answer changes nothing.
//
// The session is the thread's rather than zeus' because a thread is a
// `--fork-session` under a kraken-minted id (thread.go): its claude fires
// the cell's own StopFailure hook — same worktree, same settings.json, the
// cell's slug baked into the command — and the payload's session_id is the
// only field that says whose turn died.
func threadLimitHitTID(j *Job, sessionID string) string {
	if j == nil || sessionID == "" {
		return ""
	}
	st := ActiveStore()
	if st == nil {
		return ""
	}
	t, err := st.GetThreadBySession(sessionID)
	if err != nil || t == nil || t.JobID != j.ID {
		return ""
	}
	return t.TID
}

// recordThreadLimitHit is RecordLimitHit's thread arm: the delivery is
// written under the thread's own key, RESOLVED on record (HandledAt stamped,
// Outcome "thread-hit"), and never under the job's key — so the cell's
// rescue chain, which reads only the job's key and short-circuits on a
// resolved record, sees nothing. Resolution on record also means a later
// delivery never folds onto it (limitHitFolds needs HandledAt == 0): every
// delivery overwrites the key with a fresh record whose Deliveries is 1
// and whose At is that delivery's own time. The notification gap below is
// therefore a SLIDING window measured from the previous delivery, not from
// an incident's start — a thread carl keeps retrying every few minutes
// stays silent until it has been quiet for the gap, which is the useful
// boundary, since each retry is the same dead turn.
//
// SourceAccount is the cell's binding at record time. A thread's own env
// prefix was fixed at its spawn (thread.go's threadSpawnCommand), so a
// thread branched before a rescue rebind still runs on the old slot and
// this stamp names the cell's NEW one; nothing reads it off a thread record
// today, so it is informational rather than exact.
//
// What carl is told, once per quiet gap: the thread is dead on a limit and
// the cell is untouched. Zeus' own hit, when its next turn runs into the
// same wall, carries zeus' session and takes the ordinary chain. notifyOK
// is the caller's mirror gate: a mirror serve records exactly as the live
// one does and never notifies, the same "a mirror records, never acts"
// rule the rescue's `if !mirror` block draws — and the record path is
// where this one has to be drawn, because /api/limit-hit is deliberately
// not mirror-gated.
func recordThreadLimitHit(j *Job, tid, errorType, details, sessionID, agentID string, now time.Time, notifyOK bool) (LimitHit, error) {
	st := ActiveStore()
	if st == nil {
		return LimitHit{}, fmt.Errorf("record limit hit for %s thread %s: no store wired", j.Slug, tid)
	}
	key := threadLimitHitKey(j.ID, tid)
	prev := readLimitHitByKey(key)
	hit := mergeLimitHitDelivery(prev, errorType, details, sessionID, agentID, now)
	hit.SourceAccount = j.AccountEmail
	hit.Thread = tid
	hit.HandledAt = now.Unix()
	hit.Outcome = "thread-hit"
	data, err := json.Marshal(hit)
	if err != nil {
		return LimitHit{}, fmt.Errorf("record limit hit for %s thread %s: %w", j.Slug, tid, err)
	}
	if err := st.SetKV(key, string(data)); err != nil {
		return LimitHit{}, fmt.Errorf("record limit hit for %s thread %s: %w", j.Slug, tid, err)
	}
	if notifyOK && (prev.At == 0 || now.Sub(time.Unix(prev.At, 0)) > threadLimitHitNotifyGap) {
		notifyFn("kraken ⚠ "+j.Slug, fmt.Sprintf("side thread %s hit a usage limit — the cell is untouched; switch the thread's model or revive it", tid))
	}
	return hit, nil
}

// limitHitKey is the kv key one job's LimitHit lives under, keyed on the
// job's IMMUTABLE id rather than its slug — the same reasoning that already
// names cell dirs `<slug>-<id>` and keys job_tokens on job_id, and here it
// guards something sharper than a mixed-up number. A slug is unique only
// among ACTIVE jobs, archiving frees it, nothing ever deletes this row, and
// the rescue's gate chain leaves a hit on a torn-down cell unresolved
// forever. Keyed on the slug, a NEW job reusing an archived generation's
// name would read the dead generation's record on the first tick after its
// cell came up and perform a real, machine-wide credential swap on evidence
// from a job that no longer exists.
func limitHitKey(id int64) string { return "limit_hit:" + strconv.FormatInt(id, 10) }

// --- standing agents ---
//
// iris is the long-lived claude kraken owns that is not a cell: one tmux
// session, one conversation, no job behind it. It hits the same wall a cell
// does — one exhausted account stops every process on this Mac — and every
// key, every record and every pass in the cell feature is keyed on a job id
// there is no such thing as for it.
//
// The ids are a fixed list rather than anything derived. They name real
// singletons that exist because kraken spawns them, and a caller that could
// hand this feature an arbitrary id would be a caller that can make the API
// route write an unbounded number of kv rows nothing ever reads.

// standingAgentIDs is that list, in a stable order — the standing arm walks
// it, and its descriptor table (standingAgents) is pinned against it, so the
// order being fixed is what makes those two comparable at all.
func standingAgentIDs() []string { return []string{"iris"} }

// StandingAgentIDs is standingAgentIDs' exported face, for the API route's
// refusal message: a 400 for an id off the list names the accepted ids, read
// from this same list so the message cannot drift from the check.
func StandingAgentIDs() []string { return standingAgentIDs() }

// IsStandingAgentID reports whether id names one of them. Exported because
// the API route is the one place an id arrives from OUTSIDE this binary (a
// hook command string kraken itself installed, or a hand-typed POST), and
// that is exactly where the list has to be closed rather than trusted.
// Case-sensitive on purpose: the ids are also tmux session names and claude
// --name values, and "Iris" is not one of those.
func IsStandingAgentID(id string) bool {
	for _, a := range standingAgentIDs() {
		if a == id {
			return true
		}
	}
	return false
}

// standingHitKey is a standing agent's LimitHit key, the same row shape a
// job's limitHitKey names. The two namespaces cannot collide: a job's key
// carries the decimal rendering of its int64 id and every standing id is
// non-numeric, so no job — however its id is renumbered — can ever address
// "limit_hit:iris". Which matters for the reason limitHitKey is keyed on an
// immutable id at all: nothing deletes these rows, and a record read by the
// wrong owner is a machine-wide credential swap performed on somebody else's
// evidence.
func standingHitKey(agentID string) string { return "limit_hit:" + agentID }

// There is deliberately no dedupe-window constant here any more: repeat
// deliveries of the same error fold for as long as the hit is UNRESOLVED,
// and mergeLimitHitDelivery's doc below owns the whole rule and its history.

// limitHookHTTPTimeout bounds the POST for the same reason
// prAttachHTTPTimeout bounds pr-hook's: this runs inside a hook, so an
// unreachable serve must fail fast rather than hang the process Claude Code
// is waiting on.
const limitHookHTTPTimeout = 3 * time.Second

// limitHookDetailsMax caps the message this verb sends, matching the cap the
// route applies again on the far side (api's limitHitDetailsMax). Capping
// HERE too is not belt-and-braces: the route reads the body under a 64 KiB
// MaxBytesReader and truncates only after decoding, so a payload whose
// error_details ran long would be refused with a 400 and the limit lost
// entirely — a hit dropped for being too wordy about itself.
const limitHookDetailsMax = 200

// parseLimitPayload pulls the four fields kraken records out of one
// StopFailure payload. Defensive on every axis, because the schema belongs
// to Claude Code: unknown extra fields are ignored (an api_error object, if
// one ever appears, included), and both the shipping field names and the
// documented ones are accepted — `error` first with `error_type` as the
// fallback, `error_details` first with `error_message` as the fallback.
//
// ok is false only for input that says nothing usable: bytes that aren't a
// JSON object, or an object carrying no error type at all. An absent
// session_id, agent_id or details is not a failure — the captured payload
// above carries neither agent_id nor error_details on the main thread, so
// treating absence as malformed would reject the ordinary case.
//
// The decode goes through map[string]json.RawMessage, taking each field only
// when it really is a string, so that a field which DRIFTS to an object,
// array or number costs that one field and nothing more. A struct of strings
// would have been shorter and is wrong in a way that hides: encoding/json
// fails the WHOLE unmarshal on the first type mismatch, so the day Claude
// Code gives error_details a structure (a reset timestamp is the obvious
// addition), every payload — including ones carrying a perfectly readable
// rate_limit — would parse as garbage and the entire rescue would switch off
// with nothing logging a reason. Unknown fields and drifted fields degrade
// identically here, which is what the file header above promises.
func parseLimitPayload(raw []byte) (errorType, details, sessionID, agentID string, ok bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return "", "", "", "", false
	}
	str := func(key string) string {
		v, present := fields[key]
		if !present {
			return ""
		}
		var s string
		if json.Unmarshal(v, &s) != nil {
			return "" // present but not a string: that field alone is lost
		}
		return s
	}
	errorType = str("error")
	if errorType == "" {
		errorType = str("error_type")
	}
	if errorType == "" {
		// nothing to route on: a payload with no error type can't be matched
		// against the rescue's rate_limit test, so it is dropped whole rather
		// than posted as a typeless hit
		return "", "", "", "", false
	}
	details = str("error_details")
	if details == "" {
		details = str("error_message")
	}
	return errorType, details, str("session_id"), str("agent_id"), true
}

// LimitHook is `kraken limit-hook <slug>`'s body: read one StopFailure
// payload from stdin and POST it to kraken serve's /api/limit-hit, which
// does the recording. Never exits, never opens kraken.db, and never echoes
// the payload's free text back out — the stderr lines name the slug and the
// error TYPE only, since `details` is a message kraken doesn't author and a
// hook's stderr is read back into the transcript.
func LimitHook(slug string) { postLimitHit("slug", slug) }

// LimitHookAgent is `kraken limit-hook --agent <id>`'s body: the same verb for
// a STANDING agent (iris), which has no job and so no slug to name
// itself with. Everything else is LimitHook's contract unchanged — one
// payload off stdin, one POST, never an exit, never a kraken.db open, and the
// details already truncated on this side — because the two run in exactly the
// same place: a short-lived process Claude Code spawned inside the turn that
// just died.
//
// The id is NOT validated here. This side's job is to report; the route is
// where an id that names nothing is refused, and a hook that decided for
// itself that its own agent id was unknown would drop a real limit rather
// than let the server say so.
func LimitHookAgent(agentID string) { postLimitHit("agent", agentID) }

// postLimitHit is the body both verbs share, parameterised by the ONE thing
// they differ in: which field of the request names the reporter. Shared
// rather than copied because every rule in it is a rule about not failing the
// turn, and a second copy is a second place for one of them to be dropped.
//
// label is what the stderr lines name the reporter as. Those lines carry the
// label and the error TYPE only — never `details`, which is text kraken did
// not author and which a hook's stderr feeds straight back into the
// transcript.
func postLimitHit(idField, label string) {
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "limit-hook %s: read stdin: %v\n", label, err)
		return
	}
	errorType, details, sessionID, agentID, ok := parseLimitPayload(raw)
	if !ok {
		fmt.Fprintf(os.Stderr, "limit-hook %s: unreadable StopFailure payload — nothing recorded\n", label)
		return
	}
	body, err := json.Marshal(map[string]string{
		idField: label,
		// truncated HERE, before the POST, not only on the far side: see
		// limitHookDetailsMax. core.TruncateOneLine also flattens control
		// characters and newlines, so what goes on the wire is already the
		// one line the record stores.
		"details":    core.TruncateOneLine(details, limitHookDetailsMax),
		"error":      errorType,
		"session_id": sessionID,
		"agent_id":   agentID,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "limit-hook %s: encode %s: %v\n", label, errorType, err)
		return
	}
	client := http.Client{Timeout: limitHookHTTPTimeout}
	resp, err := client.Post(ServeBaseURL()+"/api/limit-hit", "application/json", bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(os.Stderr, "limit-hook %s: %s NOT RECORDED (serve unreachable): %v\n", label, errorType, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "limit-hook %s: %s NOT RECORDED (serve answered %d)\n", label, errorType, resp.StatusCode)
		return
	}
	fmt.Fprintf(os.Stderr, "limit-hook %s: recorded %s\n", label, errorType)
}

// ReadLimitHit returns j's stored record, or the zero LimitHit when there
// isn't a usable one. Deliberately errorless: every failure mode here — no
// job, no store wired, an unset key, a row that doesn't parse — means the
// rescue pass does NOTHING, which is the conservative direction. An error
// return would tempt a caller into treating "I couldn't read it" as "there
// is a hit", and that direction ends in a machine-wide account swap
// performed on no evidence.
func ReadLimitHit(j *Job) LimitHit {
	if j == nil {
		return LimitHit{}
	}
	return readLimitHitByKey(limitHitKey(j.ID))
}

// readLimitHitByKey is that read with the owner reduced to its kv key, which
// is what lets a standing agent (standingHitKey) and a job (limitHitKey) share
// one implementation rather than two that drift. Errorless for ReadLimitHit's
// reasons verbatim — no store, an unset key and a row that no longer parses
// all read as "no hit", the conservative direction, because the alternative
// reading of a failed read is a machine-wide account swap performed on no
// evidence at all.
func readLimitHitByKey(key string) LimitHit {
	st := ActiveStore()
	if st == nil {
		return LimitHit{}
	}
	raw, err := st.GetKV(key)
	if err != nil || raw == "" {
		return LimitHit{}
	}
	var h LimitHit
	if json.Unmarshal([]byte(raw), &h) != nil {
		return LimitHit{}
	}
	return h
}

// mergeLimitHitDelivery lays one delivery onto cur — the record as stored at
// the moment of the write — and returns what to store. It is the mirror
// image of limitrescue.go's mergeLimitRescueState, and the pair is what
// divides the twelve fields into two halves with one writer each: the rescue
// owns Attempts, LastAttempt, SwappedAt, SwappedTo, HandledAt and Outcome,
// a delivery owns the other six, and neither helper writes a field from the
// other half that it did not compute from the SAME stored row it is writing
// back onto.
//
// The fold is the delivery half's whole job: a cell is zeus plus its
// teammates, all reading one account, so a single exhausted window fires
// several StopFailure hooks within seconds. A delivery joins the existing
// hit when that hit is still unresolved and carries the same error type —
// in which case it increments the count on the row as found, so At keeps
// meaning "when this limit first hit" and the rescue's six fields survive a
// herd arriving mid-rescue. There is deliberately NO time window on the fold:
// the transcript trigger re-delivers the same stalled banner every
// limitRescueRetryInterval for as long as the cell is down, and the old
// 2-minute window made each re-delivery mint a fresh record with Attempts
// back at zero — the counter oscillated 0↔1 forever, so the
// exhausted-attempts state (and the reset re-arm behind it) was unreachable
// on exactly the cells it was built for. A RESOLVED hit never folds, so a
// late banner re-read opens a new event rather than resurrecting a finished
// one — resolution, not time, is the event boundary.
//
// One field the fold does update: a folded delivery whose details carry a
// parseable reset clause replaces stored Details that carry none. Every
// hook-arm details string recorded in kraken.db carries no clause (measured:
// empty or a raw 429 body — whether the payload omits the banner or the
// 200-rune cap cuts it, the symptom is the same), while the transcript
// trigger arrives a tick later with the banner verbatim — keeping the
// first-arrival details would suppress the reset re-arm for every
// hook-first hit, silently.
//
// The same holds for the banner that NAMES the window, reset clause or not:
// a folded delivery whose details name a limit window (namesLimitWindow)
// replaces stored Details that name none — unless that would trade away a
// reset clause the stored ones carry — and brings its session id along, since
// only the lead's own transcript produces such a banner while the first
// delivery may be a teammate's hook. The Fable banner ("You've reached your
// Fable limit. Run /usage-credits …") carries no reset clause, so the
// reset rule alone kept the hook's raw 429 body, and the Fable fallback —
// whose only other evidence is a usage-snapshot row that is itself errored
// with a 429 while the account is limited — never saw the banner and left
// four Fable-exhausted cells to rotation on 2026-10-08.
//
// Anything else — no record, a resolved one, or a different error — starts
// a fresh unresolved record, with ONE thing carried across: an unresolved
// swap watermark. A cell whose rescue has swapped but not yet resumed its
// zeus is still emitting failures for a minute or two, and a delivery
// landing past the dedupe window would otherwise zero SwappedAt, clear the
// gate that says "already swapped, go and resume", and re-arm the rotate
// path — with Attempts back at zero, so the attempt cap never fires either.
// That is uncapped machine-wide credential swapping, one pool account per
// cycle, for as long as the cell lives.
//
// limitHitFolds is that decision's condition, named once because TWO callers
// need it: the merge itself, and RecordLimitHit, which stamps SourceAccount
// on fresh records only and must ask the same question rather than infer the
// answer from the merged record (an inferred test — "At moved", "Deliveries
// is 1" — is a second definition that can disagree with this one).
func limitHitFolds(cur LimitHit, errorType string) bool {
	return cur.At != 0 && cur.HandledAt == 0 && cur.Error == errorType
}

// Pure, so the whole decision is table-testable with no store and no clock.
func mergeLimitHitDelivery(cur LimitHit, errorType, details, sessionID, agentID string, now time.Time) LimitHit {
	if limitHitFolds(cur, errorType) {
		cur.Deliveries++
		curReset := parseLimitReset(cur.Details, time.Unix(cur.At, 0)) != 0
		newReset := parseLimitReset(details, time.Unix(cur.At, 0)) != 0
		gainsReset := !curReset && newReset
		gainsWindow := !namesLimitWindow(cur.Details) && namesLimitWindow(details) && (newReset || !curReset)
		if gainsReset || gainsWindow {
			cur.Details = details
		}
		// A window-naming banner comes from the lead's own transcript, so its
		// session is the one the banner describes; the first delivery's may be
		// a teammate's hook, which would fail the fallback's session gate.
		if gainsWindow && sessionID != "" {
			cur.SessionID = sessionID
			cur.AgentID = agentID
		}
		return cur
	}
	fresh := LimitHit{
		Error:      errorType,
		Details:    details,
		At:         now.Unix(),
		SessionID:  sessionID,
		AgentID:    agentID,
		Deliveries: 1,
	}
	if cur.SwappedAt != 0 && cur.HandledAt == 0 {
		fresh.SwappedAt = cur.SwappedAt
		fresh.SwappedTo = cur.SwappedTo
	}
	return fresh
}

// RecordLimitHit folds one delivery into the job's stored record — keyed on
// j.ID, see limitHitKey — and returns what is now stored.
//
// The read happens as late as it can, immediately before the marshal, and
// the decision is taken from THAT read rather than from an earlier one. The
// difference is not stylistic: a read at the top of the function leaves a
// window in which the rescue's own write can land, after which this would
// put back a struct that predates it and erase SwappedAt — the exact harm
// mergeLimitRescueState guards the other direction against, and one that
// costs a second machine-wide credential swap.
//
// A missing key is not an error, it is the fresh-record case. No job and no
// store wired are both errors, for the same reason: there is nowhere to put
// the record, and silently dropping a hit would leave the caller reporting
// success for a delivery nothing will ever act on.
//
// A delivery whose session_id is one of the job's SIDE THREADS never reaches
// the job's record: it goes to recordThreadLimitHit, under the thread's own
// key, resolved on record. A thread shares its cell's account and its
// worktree's hooks, so its first turn on an exhausted window fires this same
// hook with the cell's slug — and recorded as the cell's hit it drove the
// cell's rescue (a rebind, then a kill-and-resume of a zeus that had been
// idle for hours) for a pane the rescue cannot reach. The fallback already
// declined a foreign session (modelfallback.go's session gate); this is the
// same gate for the rest of the chain, drawn where the record is written so
// no later reader has to re-draw it.
func RecordLimitHit(j *Job, errorType, details, sessionID, agentID string, now time.Time) (LimitHit, error) {
	return recordLimitHit(j, errorType, details, sessionID, agentID, now, true)
}

// RecordLimitHitMirror is RecordLimitHit for a `kraken serve --mirror`
// process: the same record, byte for byte, and no notification on the
// thread arm. The API handler picks this form from its mirror flag, since
// the flag lives in the api package and the record must still be written
// (the route is not mirror-gated on purpose).
func RecordLimitHitMirror(j *Job, errorType, details, sessionID, agentID string, now time.Time) (LimitHit, error) {
	return recordLimitHit(j, errorType, details, sessionID, agentID, now, false)
}

func recordLimitHit(j *Job, errorType, details, sessionID, agentID string, now time.Time, notifyOK bool) (LimitHit, error) {
	if j == nil {
		return LimitHit{}, fmt.Errorf("record limit hit: no job")
	}
	if tid := threadLimitHitTID(j, sessionID); tid != "" {
		return recordThreadLimitHit(j, tid, errorType, details, sessionID, agentID, now, notifyOK)
	}
	return recordLimitHitByKey(limitHitKey(j.ID), j.Slug, j.AccountEmail, errorType, details, sessionID, agentID, now)
}

// RecordStandingLimitHit is recordLimitHitByKey for the one caller outside
// this package: the API route, which is where a standing agent's hook lands.
// It re-checks the id against the closed list rather than trusting the route
// to have done it — this is the function that decides which kv key gets
// written, and an unchecked id here is an unbounded row namespace addressable
// from a localhost POST.
//
// hookAgentID is the payload's own `agent_id` (an in-process subagent's id,
// usually absent), not the standing agent id: the two are unrelated fields
// that the StopFailure payload and this feature each happened to call the
// same thing.
func RecordStandingLimitHit(agentID, errorType, details, sessionID, hookAgentID string, now time.Time) (LimitHit, error) {
	if !IsStandingAgentID(agentID) {
		return LimitHit{}, fmt.Errorf("record limit hit: %q is not a standing agent", agentID)
	}
	return recordLimitHitByKey(standingHitKey(agentID), agentID, "", errorType, details, sessionID, hookAgentID, now)
}

// recordLimitHitByKey is RecordLimitHit's write with the owner reduced to a
// kv key plus a LABEL, which appears in the error text and nowhere else — a
// standing agent has no slug, and an error naming nothing is an error nobody
// can act on.
//
// The read stays as LATE as it can, immediately before the marshal, and the
// merge decides from THAT read: a read taken earlier leaves a window in which
// the rescue's own write lands, after which this would put back a struct that
// predates it and erase SwappedAt — one lost swap stamp is one extra
// machine-wide credential swap.
//
// A missing key is the fresh-record case, not an error. No store wired IS an
// error, for the reason RecordLimitHit's doc gives: there is nowhere to put
// the record, and answering success for a delivery nothing will ever act on
// is how a limit gets lost silently.
func recordLimitHitByKey(key, label, sourceEmail, errorType, details, sessionID, agentID string, now time.Time) (LimitHit, error) {
	st := ActiveStore()
	if st == nil {
		return LimitHit{}, fmt.Errorf("record limit hit for %s: no store wired", label)
	}
	prev := readLimitHitByKey(key)
	hit := mergeLimitHitDelivery(prev, errorType, details, sessionID, agentID, now)
	// Stamped here rather than inside the merge, because the merge is pure
	// over the record alone — and on FRESH records only, never on a fold,
	// which is the shape that would record a falsehood: an unbound cell hits
	// a limit (SourceAccount "", the main lane, correct), the rescue's
	// rotate arm BINDS it, the herd is still delivering, and a fold would
	// then stamp the account the rescue moved the cell TO as the one that
	// ran out. A fold carries whatever the record already had, which is what
	// the hit saw when it opened. sourceEmail is the owner's lane at record
	// time: a cell's binding email, "" for an unbound cell and for every
	// standing agent — both of those live on main, and "" is how the field
	// spells the main lane.
	if !limitHitFolds(prev, errorType) {
		hit.SourceAccount = sourceEmail
	}
	data, err := json.Marshal(hit)
	if err != nil {
		return LimitHit{}, fmt.Errorf("record limit hit for %s: %w", label, err)
	}
	if err := st.SetKV(key, string(data)); err != nil {
		return LimitHit{}, fmt.Errorf("record limit hit for %s: %w", label, err)
	}
	return hit, nil
}
