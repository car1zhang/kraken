package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kraken/internal/core"
	"kraken/internal/jobs"
	"kraken/internal/store"
)

// limithit_edge_test.go — the EDGE dimension of POST /api/limit-hit
// (artifacts/50-contract.md, slice A): every refusal the route owns, the
// absent-optional-field case that is the NORMAL main-thread shape rather
// than an error, the details truncation that happens instead of a refusal,
// and the deliveries the route must accept even though the rescue will
// never act on them. Written blind from the contract text; the happy path
// and the stated invariants belong to the sibling author, so every
// package-level identifier here carries the `edge` prefix.
//
// Bodies are raw JSON strings rather than the handler's own request struct:
// the wire shape is what the hook process actually sends, and a test that
// builds it from the same struct the handler decodes with cannot catch a
// renamed field.

const edgeLimitHitPath = "/api/limit-hit"

// edgePostLimitHit drives the handler directly with a raw body.
func edgePostLimitHit(t *testing.T, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, edgeLimitHitPath, nil)
	} else {
		req = httptest.NewRequest(method, edgeLimitHitPath, bytes.NewReader([]byte(body)))
	}
	rec := httptest.NewRecorder()
	limitHitHandler(rec, req)
	return rec
}

// Every refusal on the route. The unknown-slug case is deliberately a 400
// rather than the 404 its /api/pr-attach neighbour answers: the contract
// names 400 for it, and the hook process treats any non-200 the same way
// (one stderr line, normal exit), so the distinction only ever matters to a
// human reading the event log.
func TestEdgeLimitHitHandlerRefusals(t *testing.T) {
	st := useTestStore(t)
	mustCreateStoreJob(t, st, store.Job{Slug: "edge-refuse", Repo: "monolith"})

	cases := []struct {
		name   string
		method string
		body   string
		want   int
	}{
		{"GET refused", http.MethodGet, "", http.StatusMethodNotAllowed},
		{"PUT refused", http.MethodPut, `{"slug":"edge-refuse","error":"rate_limit"}`, http.StatusMethodNotAllowed},
		{"DELETE refused", http.MethodDelete, "", http.StatusMethodNotAllowed},
		{"HEAD refused", http.MethodHead, "", http.StatusMethodNotAllowed},
		{"malformed body", http.MethodPost, `{not json`, http.StatusBadRequest},
		{"truncated body", http.MethodPost, `{"slug":"edge-refuse","error":"rate_li`, http.StatusBadRequest},
		{"empty body", http.MethodPost, "", http.StatusBadRequest},
		{"JSON null body", http.MethodPost, `null`, http.StatusBadRequest},
		{"JSON array body", http.MethodPost, `["edge-refuse"]`, http.StatusBadRequest},
		{"no fields at all", http.MethodPost, `{}`, http.StatusBadRequest},
		{"empty slug", http.MethodPost, `{"slug":"","error":"rate_limit"}`, http.StatusBadRequest},
		{"slug absent", http.MethodPost, `{"error":"rate_limit"}`, http.StatusBadRequest},
		{"empty error", http.MethodPost, `{"slug":"edge-refuse","error":""}`, http.StatusBadRequest},
		{"error absent", http.MethodPost, `{"slug":"edge-refuse","details":"d"}`, http.StatusBadRequest},
		{"slug matching no job", http.MethodPost, `{"slug":"no-such-job-anywhere","error":"rate_limit"}`, http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := edgePostLimitHit(t, c.method, c.body)
			if rec.Code != c.want {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, c.want, rec.Body.String())
			}
		})
	}
}

// A refused delivery must leave NO record behind: a 400 that still wrote
// would give the rescue pass a hit for a job that does not exist, and the
// pass has no way to tell that record from a real one.
func TestEdgeLimitHitHandlerRefusalsRecordNothing(t *testing.T) {
	st := useTestStore(t)
	j := mustCreateStoreJob(t, st, store.Job{Slug: "edge-refuse-clean", Repo: "monolith"})

	for _, body := range []string{
		`{not json`,
		`{"slug":"edge-refuse-clean","error":""}`,
		`{"slug":"no-such-job-anywhere","error":"rate_limit"}`,
	} {
		if rec := edgePostLimitHit(t, http.MethodPost, body); rec.Code == http.StatusOK {
			t.Fatalf("body %q was accepted with 200", body)
		}
	}
	if got := jobs.ReadLimitHit(j); got.At != 0 {
		t.Fatalf("a refused delivery still recorded a hit: %+v", got)
	}
	// The companion assertion — "the unknown slug got a record of its own" —
	// retired with Amendment 5: the record is keyed on the job's id, and a
	// slug matching no job has no id to look under. The route refuses that
	// body before RecordLimitHit is reached, which the refusals table above
	// covers; there is no longer a row it could have written.
}

// The main thread's real payload carries neither `error_details` nor
// `agent_id` (the contract's captured ground truth), so the route's normal
// case is a body with both absent. That is not a degraded delivery: the
// record must land with the optionals simply empty.
func TestEdgeLimitHitHandlerAcceptsAbsentOptionalFields(t *testing.T) {
	st := useTestStore(t)
	j := mustCreateStoreJob(t, st, store.Job{Slug: "edge-absent", Repo: "monolith"})

	rec := edgePostLimitHit(t, http.MethodPost, `{"slug":"edge-absent","error":"rate_limit"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	got := jobs.ReadLimitHit(j)
	if got.At == 0 {
		t.Fatal("no record written for an accepted delivery")
	}
	if got.Error != "rate_limit" {
		t.Errorf("Error = %q, want rate_limit", got.Error)
	}
	if got.Details != "" || got.SessionID != "" || got.AgentID != "" {
		t.Errorf("absent optionals came back non-empty: %+v", got)
	}
	if got.Deliveries != 1 {
		t.Errorf("Deliveries = %d, want 1", got.Deliveries)
	}
	if got.HandledAt != 0 {
		t.Errorf("HandledAt = %d, want 0 — a fresh hit is unresolved", got.HandledAt)
	}
}

// Unknown extra fields are the schema drift the whole hook path is written
// around: Claude Code owns the payload and can add to it at any release, so
// a field the route has never seen must never turn a real limit into a 400.
func TestEdgeLimitHitHandlerIgnoresUnknownFields(t *testing.T) {
	st := useTestStore(t)
	j := mustCreateStoreJob(t, st, store.Job{Slug: "edge-extra", Repo: "monolith"})

	body := `{"slug":"edge-extra","error":"rate_limit","details":"d",` +
		`"hook_event_name":"StopFailure","api_error":{"type":"rate_limit_error"},` +
		`"transcript_path":"/x.jsonl","effort":{"level":"high"},"future_field":[1,2,3]}`
	rec := edgePostLimitHit(t, http.MethodPost, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if got := jobs.ReadLimitHit(j); got.Error != "rate_limit" || got.Details != "d" {
		t.Fatalf("record = %+v", got)
	}
}

// `details` is a message Claude Code wrote, so it can be long and
// multi-line. The route TRUNCATES it rather than refusing the delivery —
// the error type is the payload, and losing a real limit over a verbose
// sentence would be the worse answer.
func TestEdgeLimitHitHandlerTruncatesDetailsRatherThanRefusing(t *testing.T) {
	st := useTestStore(t)
	j := mustCreateStoreJob(t, st, store.Job{Slug: "edge-trunc", Repo: "monolith"})

	long := "You've hit your session limit.\nresets at 4pm.\r\n" + strings.Repeat("wait or upgrade ", 60)
	body, err := edgeJSONBody(map[string]string{"slug": "edge-trunc", "error": "rate_limit", "details": long})
	if err != nil {
		t.Fatal(err)
	}
	rec := edgePostLimitHit(t, http.MethodPost, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — a long message is truncated, never refused (body %q)", rec.Code, rec.Body.String())
	}

	got := jobs.ReadLimitHit(j)
	if want := core.TruncateOneLine(long, 200); got.Details != want {
		t.Fatalf("Details = %q, want core.TruncateOneLine(details, 200) = %q", got.Details, want)
	}
	if strings.ContainsAny(got.Details, "\n\r") {
		t.Errorf("stored details carry a line break: %q", got.Details)
	}
	if n := len([]rune(got.Details)); n > 200 {
		t.Errorf("stored details are %d runes, want at most 200", n)
	}
}

// A short details string must come through unchanged — truncation is a
// ceiling, not a rewrite.
func TestEdgeLimitHitHandlerKeepsShortDetailsVerbatim(t *testing.T) {
	st := useTestStore(t)
	j := mustCreateStoreJob(t, st, store.Job{Slug: "edge-short", Repo: "monolith"})

	rec := edgePostLimitHit(t, http.MethodPost, `{"slug":"edge-short","error":"rate_limit","details":"resets at 4pm"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := jobs.ReadLimitHit(j).Details; got != "resets at 4pm" {
		t.Fatalf("Details = %q, want it verbatim", got)
	}
}

// The matcher is scoped to rate_limit today, but the route must accept
// EVERY error type it is handed and log it verbatim — that log is the only
// way a change in Claude Code's error-type mapping is ever noticed, and a
// route that refused the unmapped types would hide exactly the evidence it
// exists to collect.
func TestEdgeLimitHitHandlerAcceptsAndLogsUnactedErrorTypes(t *testing.T) {
	st := useTestStore(t)

	for _, errType := range []string{"overloaded", "authentication_failed", "max_output_tokens", "brand_new_type"} {
		t.Run(errType, func(t *testing.T) {
			slug := "edge-type-" + strings.ReplaceAll(errType, "_", "-")
			j := mustCreateStoreJob(t, st, store.Job{Slug: slug, Repo: "monolith"})

			rec := edgePostLimitHit(t, http.MethodPost, `{"slug":"`+slug+`","error":"`+errType+`"}`)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 — every delivery is recorded (body %q)", rec.Code, rec.Body.String())
			}
			if got := jobs.ReadLimitHit(j); got.Error != errType {
				t.Fatalf("stored Error = %q, want %q verbatim", got.Error, errType)
			}
			if !edgeEventMentions(t, st, errType) {
				t.Fatalf("no event log entry names the error type %q verbatim", errType)
			}
		})
	}
}

// The teammate herd arrives as N POSTs within seconds of each other; the
// route folds them (jobs.RecordLimitHit's window) and answers 200 to every
// one of them. A duplicate is not a refusal — the hook process would report
// a non-200 as a lost limit on stderr.
func TestEdgeLimitHitHandlerAnswers200ToTheHerd(t *testing.T) {
	st := useTestStore(t)
	j := mustCreateStoreJob(t, st, store.Job{Slug: "edge-herd", Repo: "monolith"})

	for i := 0; i < 5; i++ {
		rec := edgePostLimitHit(t, http.MethodPost, `{"slug":"edge-herd","error":"rate_limit","session_id":"s","agent_id":"a"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("delivery %d: status = %d, want 200 (body %q)", i, rec.Code, rec.Body.String())
		}
	}
	got := jobs.ReadLimitHit(j)
	if got.Deliveries != 5 {
		t.Errorf("Deliveries = %d, want 5 — every delivery is counted", got.Deliveries)
	}
}

// The route is NOT mirror-gated: a --mirror board records hits, it just
// never rescues (the pass, not the route, sits inside the daemon's
// `if !mirror` block). Driven through the real mux so this covers
// register.go's wiring rather than the handler in isolation.
func TestEdgeLimitHitRouteIsNotMirrorGated(t *testing.T) {
	useTestStore(t)
	SetMirrorMode(true)
	t.Cleanup(func() { SetMirrorMode(false) })

	mux := http.NewServeMux()
	Register(mux)

	rec := postJSON(t, mux, edgeLimitHitPath, []byte(`{"slug":"no-such-job-anywhere","error":"rate_limit"}`))
	if rec.Code == http.StatusForbidden {
		t.Fatalf("POST %s = 403 under mirror mode — a mirror must still RECORD hits", edgeLimitHitPath)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (the handler's own unknown-slug refusal, proving the gate was passed)", rec.Code)
	}
}

// A non-POST through the real mux is still a 405 rather than a 404 — the
// route is registered by path, and a hook process that somehow issued a GET
// must get the method refusal the contract names.
func TestEdgeLimitHitRouteRegisteredForNonPostToo(t *testing.T) {
	useTestStore(t)
	SetMirrorMode(false)

	mux := http.NewServeMux()
	Register(mux)

	req := httptest.NewRequest(http.MethodGet, edgeLimitHitPath, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code == http.StatusNotFound {
		t.Fatalf("GET %s = 404 — the route is not registered", edgeLimitHitPath)
	}
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

// --- helpers ---

// edgeJSONBody marshals a map into a request body, so a details string
// carrying real newlines is escaped correctly rather than hand-quoted.
func edgeJSONBody(v map[string]string) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// edgeEventMentions reports whether any recent event names s verbatim, in
// either its id or its message.
func edgeEventMentions(t *testing.T, st *store.Store, s string) bool {
	t.Helper()
	events, err := st.ListEvents(200, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if strings.Contains(e.Event, s) || strings.Contains(e.Msg, s) {
			return true
		}
	}
	return false
}

// --- Amendment 3: the accepted-delivery event ---

// edgeEventsNamed returns every recent event with the given id.
func edgeEventsNamed(t *testing.T, st *store.Store, id string) []store.Event {
	t.Helper()
	all, err := st.ListEvents(200, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []store.Event
	for _, e := range all {
		if e.Event == id {
			out = append(out, e)
		}
	}
	return out
}

// The event is named `limit-hit`, there is exactly ONE per accepted
// delivery, and it names the error type verbatim. Per DELIVERY, not per
// hit: the teammate herd folds into one record but each of its deliveries
// is its own line, which is what makes the log usable as evidence that a
// future error-type mapping change has happened.
func TestEdgeLimitHitEventIsNamedAndOnePerDelivery(t *testing.T) {
	st := useTestStore(t)
	j := mustCreateStoreJob(t, st, store.Job{Slug: "edge-event", Repo: "monolith"})

	body := `{"slug":"edge-event","error":"rate_limit"}`
	if rec := edgePostLimitHit(t, http.MethodPost, body); rec.Code != http.StatusOK {
		t.Fatalf("first delivery: status = %d", rec.Code)
	}
	events := edgeEventsNamed(t, st, "limit-hit")
	if len(events) != 1 {
		t.Fatalf("after one accepted delivery there are %d `limit-hit` events, want exactly 1", len(events))
	}
	if !strings.Contains(events[0].Msg, "rate_limit") {
		t.Errorf("event message %q does not name the error type verbatim", events[0].Msg)
	}

	// two more deliveries of the same limit fold into one RECORD but are
	// three separate lines in the log
	for i := 0; i < 2; i++ {
		if rec := edgePostLimitHit(t, http.MethodPost, body); rec.Code != http.StatusOK {
			t.Fatalf("herd delivery %d: status = %d", i, rec.Code)
		}
	}
	if got := len(edgeEventsNamed(t, st, "limit-hit")); got != 3 {
		t.Fatalf("after three accepted deliveries there are %d `limit-hit` events, want exactly 3", got)
	}
	if d := jobs.ReadLimitHit(j).Deliveries; d != 3 {
		t.Fatalf("Deliveries = %d, want 3 — the three events describe one folded hit", d)
	}
}

// The event names the error type and NEVER `details`. Details is a message
// Claude Code wrote, it is already stored on the record, and the log is a
// mapping-change tripwire rather than a second copy of the payload.
func TestEdgeLimitHitEventNeverCarriesDetails(t *testing.T) {
	st := useTestStore(t)
	j := mustCreateStoreJob(t, st, store.Job{Slug: "edge-event-details", Repo: "monolith"})

	const secretish = "zzz-unmistakable-details-string-zzz"
	rec := edgePostLimitHit(t, http.MethodPost, `{"slug":"edge-event-details","error":"rate_limit","details":"`+secretish+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := jobs.ReadLimitHit(j).Details; got != secretish {
		t.Fatalf("Details = %q — the record is where details belong", got)
	}
	all, err := st.ListEvents(200, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range all {
		if strings.Contains(e.Msg, secretish) || strings.Contains(e.Event, secretish) {
			t.Fatalf("an event carries the details string: %q / %q", e.Event, e.Msg)
		}
	}
}

// A refused delivery writes no `limit-hit` event: the log's whole value is
// that one line means one accepted delivery, and a 400 that still logged
// would make an unknown slug indistinguishable from a real limit.
func TestEdgeLimitHitRefusalWritesNoEvent(t *testing.T) {
	st := useTestStore(t)
	mustCreateStoreJob(t, st, store.Job{Slug: "edge-event-refuse", Repo: "monolith"})

	for _, body := range []string{
		`{not json`,
		`{"slug":"edge-event-refuse","error":""}`,
		`{"slug":"no-such-job-anywhere","error":"rate_limit"}`,
		`{}`,
	} {
		if rec := edgePostLimitHit(t, http.MethodPost, body); rec.Code == http.StatusOK {
			t.Fatalf("body %q was accepted", body)
		}
	}
	if got := edgeEventsNamed(t, st, "limit-hit"); len(got) != 0 {
		t.Fatalf("refused deliveries wrote %d `limit-hit` event(s): %+v", len(got), got)
	}
}
