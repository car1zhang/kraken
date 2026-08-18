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

// limithook.go — `kraken limit-hook <slug>`, wired as a Claude Code
// StopFailure hook in each cell's worktree (cell.go's mergeHookSettings
// writes the entry, matcher "rate_limit"). It is the detection half of the
// mid-run usage-limit rescue: when a claude in the cell dies on a usage
// limit, Claude Code fires this hook, and this verb reports the failure to
// kraken serve so the daemon can swap the main login to a healthier pool
// account without anyone watching the pane.
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
// calls below do not contradict the sentence above. LimitHook and
// parseLimitPayload run in the cell, in a process spawned by Claude Code,
// and touch nothing but stdin and one HTTP request. ReadLimitHit,
// RecordLimitHit and mergeLimitHitDelivery are the server's half: they take
// a *Job, key on its immutable id, and run only inside kraken serve — the
// API handler on the way in, the rescue pass on the way out.

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
func LimitHook(slug string) {
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "limit-hook %s: read stdin: %v\n", slug, err)
		return
	}
	errorType, details, sessionID, agentID, ok := parseLimitPayload(raw)
	if !ok {
		fmt.Fprintf(os.Stderr, "limit-hook %s: unreadable StopFailure payload — nothing recorded\n", slug)
		return
	}
	body, err := json.Marshal(map[string]string{
		"slug": slug,
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
		fmt.Fprintf(os.Stderr, "limit-hook %s: encode %s: %v\n", slug, errorType, err)
		return
	}
	client := http.Client{Timeout: limitHookHTTPTimeout}
	resp, err := client.Post(ServeBaseURL()+"/api/limit-hit", "application/json", bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(os.Stderr, "limit-hook %s: %s NOT RECORDED (serve unreachable): %v\n", slug, errorType, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "limit-hook %s: %s NOT RECORDED (serve answered %d)\n", slug, errorType, resp.StatusCode)
		return
	}
	fmt.Fprintf(os.Stderr, "limit-hook %s: recorded %s\n", slug, errorType)
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
	st := ActiveStore()
	if st == nil {
		return LimitHit{}
	}
	raw, err := st.GetKV(limitHitKey(j.ID))
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
		if parseLimitReset(cur.Details, time.Unix(cur.At, 0)) == 0 &&
			parseLimitReset(details, time.Unix(cur.At, 0)) != 0 {
			cur.Details = details
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
func RecordLimitHit(j *Job, errorType, details, sessionID, agentID string, now time.Time) (LimitHit, error) {
	if j == nil {
		return LimitHit{}, fmt.Errorf("record limit hit: no job")
	}
	st := ActiveStore()
	if st == nil {
		return LimitHit{}, fmt.Errorf("record limit hit for %s: no store wired", j.Slug)
	}
	prev := ReadLimitHit(j)
	hit := mergeLimitHitDelivery(prev, errorType, details, sessionID, agentID, now)
	// Stamped here rather than inside the merge, because it is the one field
	// that needs the JOB and the merge is pure over the record alone — and on
	// FRESH records only, never on a fold, which is the shape that would
	// record a falsehood: an unbound cell hits a limit (SourceAccount "", the
	// main lane, correct), the rescue's rotate arm BINDS it, the herd is
	// still delivering, and a fold would then stamp the account the rescue
	// moved the cell TO as the one that ran out. A fold carries whatever the
	// record already had, which is what the hit saw when it opened.
	if !limitHitFolds(prev, errorType) {
		hit.SourceAccount = j.AccountEmail
	}
	data, err := json.Marshal(hit)
	if err != nil {
		return LimitHit{}, fmt.Errorf("record limit hit for %s: %w", j.Slug, err)
	}
	if err := st.SetKV(limitHitKey(j.ID), string(data)); err != nil {
		return LimitHit{}, fmt.Errorf("record limit hit for %s: %w", j.Slug, err)
	}
	return hit, nil
}
