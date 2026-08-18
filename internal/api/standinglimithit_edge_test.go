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

// standinglimithit_edge_test.go — the EDGE dimension of the
// `agent` form POST /api/limit-hit grew for standing agents
// (artifacts/50-contract.md, slice P, "API route"): the slug-XOR-agent
// refusals, the registry validation, the refusals that must record NOTHING,
// and the fold/cap behavior the agent path inherits from the job path.
//
// Every package-level identifier here carries the `edgeAgt` prefix so this
// file coexists with the cell rescue's own limithit_edge_test.go in one
// package: that suite owns edgeLimitHitPath, edgePostLimitHit, edgeJSONBody,
// edgeEventMentions and edgeEventsNamed, and none of them is redeclared here.
//
// Bodies are raw JSON strings rather than the handler's own request struct:
// the wire shape is what the hook process actually sends, and a test that
// built it from the same struct the handler decodes with could not catch a
// renamed field.

const edgeAgtPath = "/api/limit-hit"

// edgeAgtPost drives the handler directly with a raw body.
func edgeAgtPost(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, edgeAgtPath, bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()
	limitHitHandler(rec, req)
	return rec
}

// edgeAgtRecord reads the record stored under a standing agent's key.
// Straight out of kv rather than through a jobs accessor: the KEY is what
// is being asserted, and a reader that took the agent id and rebuilt the
// key would agree with a wrong implementation.
func edgeAgtRecord(t *testing.T, st *store.Store, agentID string) jobs.LimitHit {
	t.Helper()
	raw, err := st.GetKV("limit_hit:" + agentID)
	if err != nil || raw == "" {
		return jobs.LimitHit{}
	}
	var h jobs.LimitHit
	if err := json.Unmarshal([]byte(raw), &h); err != nil {
		t.Fatalf("stored record for %q is not a LimitHit: %v (%s)", agentID, err, raw)
	}
	return h
}

// edgeAgtSeed writes a record straight into a standing agent's kv row.
func edgeAgtSeed(t *testing.T, st *store.Store, agentID string, h jobs.LimitHit) {
	t.Helper()
	data, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetKV("limit_hit:"+agentID, string(data)); err != nil {
		t.Fatal(err)
	}
}

// The XOR is the route's new refusal, and it refuses in BOTH directions.
// Both keys present is a caller that does not know which record it is
// writing; neither is a caller that named no subject at all. Either one
// silently defaulting — to the slug, to the agent, to a blank key — would
// put a real limit in a row nothing reads.
func TestEdgeAgtLimitHitXORRefusals(t *testing.T) {
	st := useTestStore(t)
	mustCreateStoreJob(t, st, store.Job{Slug: "edge-agt-xor", Repo: "monolith"})

	cases := []struct {
		name string
		body string
	}{
		{"both slug and agent", `{"slug":"edge-agt-xor","agent":"kronos","error":"rate_limit"}`},
		{"both, agent listed first", `{"agent":"kronos","slug":"edge-agt-xor","error":"rate_limit"}`},
		{"both, with an unknown agent", `{"slug":"edge-agt-xor","agent":"zeus","error":"rate_limit"}`},
		{"both, with an unknown slug", `{"slug":"no-such-job-anywhere","agent":"kronos","error":"rate_limit"}`},
		{"neither key present", `{"error":"rate_limit"}`},
		{"both keys present but empty", `{"slug":"","agent":"","error":"rate_limit"}`},
		{"slug empty, agent absent", `{"slug":"","error":"rate_limit"}`},
		{"agent empty, slug absent", `{"agent":"","error":"rate_limit"}`},
		{"agent whitespace only", `{"agent":"   ","error":"rate_limit"}`},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := edgeAgtPost(t, c.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %q)", rec.Code, rec.Body.String())
			}
		})
	}
}

// The refusal has to say WHICH rule was broken. The only client is a hook
// process kraken itself installed, so a refusal that reads as a generic
// "bad request" leaves a lost limit with nothing in the log explaining why
// — and the hook's own stderr line names the status and nothing more.
func TestEdgeAgtLimitHitXORRefusalNamesTheRule(t *testing.T) {
	st := useTestStore(t)
	mustCreateStoreJob(t, st, store.Job{Slug: "edge-agt-rule", Repo: "monolith"})

	for _, body := range []string{
		`{"slug":"edge-agt-rule","agent":"kronos","error":"rate_limit"}`,
		`{"error":"rate_limit"}`,
	} {
		rec := edgeAgtPost(t, body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body %q: status = %d, want 400", body, rec.Code)
		}
		msg := rec.Body.String()
		if !strings.Contains(msg, "slug") || !strings.Contains(msg, "agent") {
			t.Errorf("refusal body %q names neither side of the rule it broke", msg)
		}
	}
}

// An agent id outside the registry is refused LOUDLY, naming the id. The id
// becomes a kv key that lives forever and that no pass will ever read, so
// accepting an unregistered one is a hit recorded into a namespace nothing
// watches — indistinguishable, from the hook's side, from a real record.
func TestEdgeAgtLimitHitUnknownAgentRefusedAndNamed(t *testing.T) {
	useTestStore(t)

	for _, id := range []string{
		"zeus",
		"hephaestus",
		"KRONOS",
		"Kronos",
		"IRIS",
		"kronos-1",
		"krono",
		"irisx",
		"0",
		"42",
		"limit_hit:kronos",
	} {
		t.Run(id, func(t *testing.T) {
			body, err := json.Marshal(map[string]string{"agent": id, "error": "rate_limit"})
			if err != nil {
				t.Fatal(err)
			}
			rec := edgeAgtPost(t, string(body))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 for agent %q (body %q)", rec.Code, id, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), id) {
				t.Errorf("refusal %q does not name the offending id %q", rec.Body.String(), id)
			}
		})
	}
}

// `error` is still required on the agent form: it is the field the whole
// rescue routes on, and a typeless hit would be recorded and then skipped
// by the decide chain forever.
func TestEdgeAgtLimitHitAgentStillNeedsAnErrorType(t *testing.T) {
	st := useTestStore(t)

	for _, body := range []string{
		`{"agent":"kronos"}`,
		`{"agent":"kronos","error":""}`,
		`{"agent":"kronos","details":"resets 4pm"}`,
	} {
		rec := edgeAgtPost(t, body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body %q: status = %d, want 400", body, rec.Code)
		}
	}
	if got := edgeAgtRecord(t, st, "kronos"); got.At != 0 {
		t.Fatalf("a refused delivery recorded a hit: %+v", got)
	}
}

// Every refusal leaves BOTH namespaces clean. A 400 that still wrote would
// give the standing rescue a hit for an agent nobody named, and the pass
// has no way to tell that record from a real one.
func TestEdgeAgtLimitHitRefusalsRecordNothing(t *testing.T) {
	st := useTestStore(t)
	j := mustCreateStoreJob(t, st, store.Job{Slug: "edge-agt-clean", Repo: "monolith"})

	for _, body := range []string{
		`{"slug":"edge-agt-clean","agent":"kronos","error":"rate_limit"}`,
		`{"error":"rate_limit"}`,
		`{"agent":"zeus","error":"rate_limit"}`,
		`{"agent":"KRONOS","error":"rate_limit"}`,
		`{"agent":"kronos","error":""}`,
		`{"slug":"","agent":"","error":"rate_limit"}`,
	} {
		if rec := edgeAgtPost(t, body); rec.Code == http.StatusOK {
			t.Fatalf("body %q was accepted with 200", body)
		}
	}

	for _, id := range []string{"kronos", "iris", "zeus", "KRONOS", ""} {
		if got := edgeAgtRecord(t, st, id); got.At != 0 {
			t.Fatalf("a refused delivery recorded under %q: %+v", id, got)
		}
	}
	if got := jobs.ReadLimitHit(j); got.At != 0 {
		t.Fatalf("a refused delivery recorded against the job: %+v", got)
	}
}

// The valid agent form records under the agent's own key and answers 200 —
// and it records under the AGENT's key only, never against a job.
func TestEdgeAgtLimitHitValidAgentRecordsUnderTheAgentKey(t *testing.T) {
	st := useTestStore(t)
	j := mustCreateStoreJob(t, st, store.Job{Slug: "edge-agt-bystander", Repo: "monolith"})

	rec := edgeAgtPost(t, `{"agent":"kronos","error":"rate_limit","details":"resets 4:50am (America/New_York)","session_id":"sess-kronos"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}

	got := edgeAgtRecord(t, st, "kronos")
	if got.At == 0 {
		t.Fatal("no record written under limit_hit:kronos")
	}
	if got.Error != "rate_limit" {
		t.Errorf("Error = %q, want rate_limit", got.Error)
	}
	if got.HandledAt != 0 {
		t.Errorf("HandledAt = %d, want 0 — a fresh hit is unresolved", got.HandledAt)
	}
	if got.Deliveries != 1 {
		t.Errorf("Deliveries = %d, want 1", got.Deliveries)
	}
	if other := edgeAgtRecord(t, st, "iris"); other.At != 0 {
		t.Fatalf("recording for kronos also wrote iris' row: %+v", other)
	}
	if jb := jobs.ReadLimitHit(j); jb.At != 0 {
		t.Fatalf("recording for an agent wrote a job's row: %+v", jb)
	}
}

// The two id namespaces share one kv prefix and must never meet. A standing
// agent's record and a job's record are written in the same test here so a
// key scheme that collapsed them would show up as one row holding the other
// party's error type.
func TestEdgeAgtLimitHitAgentAndJobRowsAreIndependent(t *testing.T) {
	st := useTestStore(t)
	j := mustCreateStoreJob(t, st, store.Job{Slug: "edge-agt-parallel", Repo: "monolith"})

	if rec := edgeAgtPost(t, `{"agent":"kronos","error":"rate_limit"}`); rec.Code != http.StatusOK {
		t.Fatalf("agent delivery: status = %d", rec.Code)
	}
	if rec := edgeAgtPost(t, `{"slug":"edge-agt-parallel","error":"overloaded"}`); rec.Code != http.StatusOK {
		t.Fatalf("slug delivery: status = %d", rec.Code)
	}

	if got := edgeAgtRecord(t, st, "kronos"); got.Error != "rate_limit" || got.Deliveries != 1 {
		t.Fatalf("the agent row = %+v, want its own rate_limit delivery", got)
	}
	if got := jobs.ReadLimitHit(j); got.Error != "overloaded" || got.Deliveries != 1 {
		t.Fatalf("the job row = %+v, want its own overloaded delivery", got)
	}
}

// The agent path's fold semantics are the job path's, unchanged. The herd
// is real here too — kronos' own claude and the transcript arm both report
// one wall — and the counters have to survive it.
func TestEdgeAgtLimitHitAgentFoldSemantics(t *testing.T) {
	t.Run("the herd folds and every delivery answers 200", func(t *testing.T) {
		st := useTestStore(t)
		for i := 0; i < 5; i++ {
			rec := edgeAgtPost(t, `{"agent":"kronos","error":"rate_limit","session_id":"s"}`)
			if rec.Code != http.StatusOK {
				t.Fatalf("delivery %d: status = %d (body %q)", i, rec.Code, rec.Body.String())
			}
		}
		got := edgeAgtRecord(t, st, "kronos")
		if got.Deliveries != 5 {
			t.Fatalf("Deliveries = %d, want 5", got.Deliveries)
		}
		if got.HandledAt != 0 {
			t.Errorf("the fold resolved the hit: %+v", got)
		}
	})

	t.Run("a resolved hit never folds", func(t *testing.T) {
		st := useTestStore(t)
		edgeAgtSeed(t, st, "kronos", jobs.LimitHit{
			Error: "rate_limit", At: 1_700_000_000, Deliveries: 3,
			HandledAt: 1_700_000_100, Outcome: "rotated",
		})
		if rec := edgeAgtPost(t, `{"agent":"kronos","error":"rate_limit"}`); rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		got := edgeAgtRecord(t, st, "kronos")
		if got.At == 1_700_000_000 {
			t.Fatalf("the delivery folded onto a RESOLVED hit: %+v", got)
		}
		if got.Deliveries != 1 || got.HandledAt != 0 || got.Outcome != "" {
			t.Fatalf("the fresh record inherited the resolution: %+v", got)
		}
	})

	t.Run("a different error type never folds", func(t *testing.T) {
		st := useTestStore(t)
		if rec := edgeAgtPost(t, `{"agent":"iris","error":"rate_limit"}`); rec.Code != http.StatusOK {
			t.Fatalf("first: status = %d", rec.Code)
		}
		if rec := edgeAgtPost(t, `{"agent":"iris","error":"overloaded"}`); rec.Code != http.StatusOK {
			t.Fatalf("second: status = %d", rec.Code)
		}
		got := edgeAgtRecord(t, st, "iris")
		if got.Error != "overloaded" || got.Deliveries != 1 {
			t.Fatalf("a different error type folded: %+v", got)
		}
	})

	t.Run("a reset clause replaces clause-less details", func(t *testing.T) {
		st := useTestStore(t)
		if rec := edgeAgtPost(t, `{"agent":"kronos","error":"rate_limit","details":"429 Too Many Requests"}`); rec.Code != http.StatusOK {
			t.Fatalf("first: status = %d", rec.Code)
		}
		const withClause = "You've hit your session limit · resets 4:50am (America/New_York)"
		body, err := json.Marshal(map[string]string{"agent": "kronos", "error": "rate_limit", "details": withClause})
		if err != nil {
			t.Fatal(err)
		}
		if rec := edgeAgtPost(t, string(body)); rec.Code != http.StatusOK {
			t.Fatalf("second: status = %d", rec.Code)
		}
		got := edgeAgtRecord(t, st, "kronos")
		if got.Details != withClause {
			t.Fatalf("Details = %q, want the clause-carrying delivery's %q", got.Details, withClause)
		}
		if got.Deliveries != 2 {
			t.Errorf("Deliveries = %d, want 2 — it still folded", got.Deliveries)
		}
	})

	t.Run("an unresolved swap watermark survives supersession", func(t *testing.T) {
		st := useTestStore(t)
		edgeAgtSeed(t, st, "kronos", jobs.LimitHit{
			Error: "rate_limit", At: 1_700_000_000, Deliveries: 1,
			SwappedAt: 1_700_000_050, SwappedTo: "pool-b@example.com",
		})
		if rec := edgeAgtPost(t, `{"agent":"kronos","error":"overloaded"}`); rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		got := edgeAgtRecord(t, st, "kronos")
		if got.SwappedAt != 1_700_000_050 || got.SwappedTo != "pool-b@example.com" {
			t.Fatalf("the landed swap was dropped by a superseding delivery: %+v", got)
		}
	})
}

// `details` is free text Claude Code wrote, so the agent form truncates it
// exactly as the slug form does rather than refusing the delivery: the
// error type is the payload, and losing a standing agent's limit over a
// verbose sentence would leave kronos dead with nothing recorded.
func TestEdgeAgtLimitHitAgentDetailsTruncatedRatherThanRefused(t *testing.T) {
	st := useTestStore(t)

	long := "You've hit your session limit.\nresets 4:50am.\r\n" + strings.Repeat("wait or upgrade ", 60)
	body, err := json.Marshal(map[string]string{"agent": "kronos", "error": "rate_limit", "details": long})
	if err != nil {
		t.Fatal(err)
	}
	rec := edgeAgtPost(t, string(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}

	got := edgeAgtRecord(t, st, "kronos")
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

// Unknown extra fields are the schema drift the whole hook path is written
// around: Claude Code owns the payload and can add to it at any release, so
// a field the route has never seen must never turn a standing agent's real
// limit into a 400.
func TestEdgeAgtLimitHitAgentIgnoresUnknownFields(t *testing.T) {
	st := useTestStore(t)

	body := `{"agent":"kronos","error":"rate_limit","details":"d",` +
		`"hook_event_name":"StopFailure","api_error":{"type":"rate_limit_error"},` +
		`"slug_hint":"kronos","effort":{"level":"high"},"future_field":[1,2,3]}`
	if rec := edgeAgtPost(t, body); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if got := edgeAgtRecord(t, st, "kronos"); got.Error != "rate_limit" || got.Details != "d" {
		t.Fatalf("record = %+v", got)
	}
}

// The agent form is NOT mirror-gated, for the slug form's reason exactly:
// recording is a read-side fact, the swap is not. A mirror that could not
// record would leave the real daemon blind to a limit its own observation
// saw. Driven through the real mux so this covers the registration rather
// than the handler in isolation.
func TestEdgeAgtLimitHitAgentRouteIsNotMirrorGated(t *testing.T) {
	st := useTestStore(t)
	SetMirrorMode(true)
	t.Cleanup(func() { SetMirrorMode(false) })

	mux := http.NewServeMux()
	Register(mux)

	rec := postJSON(t, mux, edgeAgtPath, []byte(`{"agent":"kronos","error":"rate_limit"}`))
	if rec.Code == http.StatusForbidden {
		t.Fatalf("POST %s with an agent = 403 under mirror mode — a mirror must still RECORD hits", edgeAgtPath)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if got := edgeAgtRecord(t, st, "kronos"); got.At == 0 {
		t.Fatal("a mirror recorded nothing for a valid agent delivery")
	}
}

// The route's existing slug behavior is byte-identical to today, and the
// refusals are the sharpest half of that: adding a second subject key must
// not have turned any of the slug form's own refusals into something else.
func TestEdgeAgtLimitHitSlugPathRefusalsUnchanged(t *testing.T) {
	st := useTestStore(t)
	mustCreateStoreJob(t, st, store.Job{Slug: "edge-agt-slug", Repo: "monolith"})

	cases := []struct {
		name string
		body string
		want int
	}{
		{"malformed body", `{not json`, http.StatusBadRequest},
		{"empty error", `{"slug":"edge-agt-slug","error":""}`, http.StatusBadRequest},
		{"slug matching no job", `{"slug":"no-such-job-anywhere","error":"rate_limit"}`, http.StatusBadRequest},
		{"a valid slug delivery", `{"slug":"edge-agt-slug","error":"rate_limit"}`, http.StatusOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if rec := edgeAgtPost(t, c.body); rec.Code != c.want {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, c.want, rec.Body.String())
			}
		})
	}
}
