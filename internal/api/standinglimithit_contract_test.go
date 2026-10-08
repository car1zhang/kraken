package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kraken/internal/jobs"
	"kraken/internal/store"
)

// standinglimithit_contract_test.go — athena-logic's BLIND contract tests for
// the standing-agent extension of POST /api/limit-hit (50-contract.md, slice
// P, row "API route"). Written from the contract text alone; the working-tree
// state of internal/api/limithit.go was never read.
//
// It lives under its own stem beside the cell-side limithit_contract_test.go
// (which stays untouched); identifiers here carry the contractStandingLimitHit
// prefix so the two blind suites share the package without collision.
//
// Dimension: LOGIC — what the agent form accepts, what it records, and that
// the slug form is unchanged beside it. The 405 and the malformed-body
// refusals are the sibling author's; the XOR rule and the unknown-id refusal
// appear here because the contract states them as the route's central
// admission rule rather than as error handling.

// contractStandingLimitHitMux registers the whole API surface, so route
// registration is asserted rather than assumed.
func contractStandingLimitHitMux(t *testing.T) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux)
	return mux
}

// contractStandingLimitHitPost drives one request through the registered mux —
// the path a real hook process's POST takes.
func contractStandingLimitHitPost(t *testing.T, mux http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/limit-hit", bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// contractStandingLimitHitAgentBody renders the agent form of the body: the
// same five values as the slug form with `agent` where `slug` would go.
func contractStandingLimitHitAgentBody(agent, errType, details, sessionID, agentID string) string {
	b, _ := json.Marshal(map[string]string{
		"agent":      agent,
		"error":      errType,
		"details":    details,
		"session_id": sessionID,
		"agent_id":   agentID,
	})
	return string(b)
}

// contractStandingLimitHitStored reads the record the route wrote under a
// standing agent's key. The keyed reader is unexported in jobs, so this goes
// through the store the same way the row itself is stored — which is also
// what pins the KEY, the thing the api half is responsible for choosing.
func contractStandingLimitHitStored(t *testing.T, agentID string) jobs.LimitHit {
	t.Helper()
	st := jobs.ActiveStore()
	if st == nil {
		t.Fatal("no store wired")
	}
	raw, err := st.GetKV("limit_hit:" + agentID)
	if err != nil || raw == "" {
		return jobs.LimitHit{}
	}
	var h jobs.LimitHit
	if err := json.Unmarshal([]byte(raw), &h); err != nil {
		t.Fatalf("limit_hit:%s is not a LimitHit: %v (%q)", agentID, err, raw)
	}
	return h
}

// TestContractStandingLimitHitAgentFormRecords is the happy path: "A valid
// agent body records via the keyed path under limit_hit:<agent> and answers
// 200." Registered nowhere or keyed wrongly, a `kraken limit-hook --agent`
// delivery posts into the void and the standing rescue never fires — with
// nothing anywhere reporting a problem, because the hook is contractually
// silent.
func TestContractStandingLimitHitAgentFormRecords(t *testing.T) {
	useTestStore(t)
	mux := contractStandingLimitHitMux(t)

	rec := contractStandingLimitHitPost(t, mux,
		contractStandingLimitHitAgentBody("iris", "rate_limit", "You've hit your session limit", "iris-sess", "sub-7"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body.String())
	}

	hit := contractStandingLimitHitStored(t, "iris")
	if hit.At == 0 {
		t.Fatal("nothing recorded under limit_hit:iris")
	}
	if hit.Error != "rate_limit" {
		t.Errorf("Error = %q, want rate_limit", hit.Error)
	}
	if hit.Details != "You've hit your session limit" {
		t.Errorf("Details = %q", hit.Details)
	}
	if hit.SessionID != "iris-sess" {
		t.Errorf("SessionID = %q, want iris-sess", hit.SessionID)
	}
	if hit.AgentID != "sub-7" {
		t.Errorf("AgentID = %q, want sub-7", hit.AgentID)
	}
	if hit.Deliveries != 1 {
		t.Errorf("Deliveries = %d, want 1", hit.Deliveries)
	}
	if hit.HandledAt != 0 {
		t.Errorf("the route recorded an already-resolved hit: %+v", hit)
	}
}

// TestContractStandingLimitHitAgentFormAcceptsEveryRegisteredID: the route
// validates against jobs.IsStandingAgentID, so every id the registry carries
// must be accepted. An id the daemon iterates but the route refuses is a hook
// whose every delivery 400s.
func TestContractStandingLimitHitAgentFormAcceptsEveryRegisteredID(t *testing.T) {
	useTestStore(t)
	mux := contractStandingLimitHitMux(t)
	for _, id := range []string{"iris"} {
		if !jobs.IsStandingAgentID(id) {
			t.Fatalf("fixture is wrong: %q is not a registered standing agent id", id)
		}
		rec := contractStandingLimitHitPost(t, mux,
			contractStandingLimitHitAgentBody(id, "rate_limit", "429", "sess-"+id, ""))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200; body %s", id, rec.Code, rec.Body.String())
		}
		if got := contractStandingLimitHitStored(t, id); got.At == 0 {
			t.Fatalf("%s: nothing recorded under limit_hit:%s", id, id)
		}
	}
}

// TestContractStandingLimitHitAgentFoldSemanticsMatchTheSlugPath: "a valid
// agent body records a hit whose fold semantics match the job path's exactly
// (same-error unresolved deliveries fold, a resolved hit never folds, a
// reset-clause detail replaces a clause-less one)". A cell is a herd of
// claudes on one account and so is nothing else on this machine — but the
// hook arm and the transcript arm BOTH deliver for a standing agent, so the
// fold is what keeps one exhausted window one event.
func TestContractStandingLimitHitAgentFoldSemanticsMatchTheSlugPath(t *testing.T) {
	useTestStore(t)
	mux := contractStandingLimitHitMux(t)

	first := contractStandingLimitHitPost(t, mux,
		contractStandingLimitHitAgentBody("iris", "rate_limit", "429", "iris-sess", ""))
	if first.Code != http.StatusOK {
		t.Fatalf("first delivery: status %d, body %s", first.Code, first.Body.String())
	}
	openedAt := contractStandingLimitHitStored(t, "iris").At

	second := contractStandingLimitHitPost(t, mux,
		contractStandingLimitHitAgentBody("iris", "rate_limit", "429", "iris-sess", ""))
	if second.Code != http.StatusOK {
		t.Fatalf("second delivery: status %d, body %s", second.Code, second.Body.String())
	}
	hit := contractStandingLimitHitStored(t, "iris")
	if hit.Deliveries != 2 {
		t.Errorf("Deliveries = %d after a same-error delivery, want 2 (it must fold)", hit.Deliveries)
	}
	if hit.At != openedAt {
		t.Errorf("At moved to %d — a fold keeps the first hit's time", hit.At)
	}

	// The response reports the delivery count, which is the only signal the
	// hook process ever gets back.
	var body map[string]any
	if err := json.Unmarshal(second.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v (%s)", err, second.Body.String())
	}
	if got, _ := body["deliveries"].(float64); int(got) != 2 {
		t.Errorf("response deliveries = %v, want 2", body["deliveries"])
	}

	// A folded delivery whose details carry a parseable reset clause replaces
	// clause-less stored details: the hook arm's details carry no clause and
	// the transcript arm's carry the banner verbatim, so keeping the first
	// arrival would suppress the reset re-arm for every hook-first hit.
	third := contractStandingLimitHitPost(t, mux, contractStandingLimitHitAgentBody(
		"iris", "rate_limit", "You've hit your session limit · resets 4:50am (America/New_York)", "iris-sess", ""))
	if third.Code != http.StatusOK {
		t.Fatalf("third delivery: status %d, body %s", third.Code, third.Body.String())
	}
	hit = contractStandingLimitHitStored(t, "iris")
	if !strings.Contains(hit.Details, "resets 4:50am") {
		t.Errorf("Details = %q, want the clause-carrying banner to have replaced the clause-less one", hit.Details)
	}
	if hit.Deliveries != 3 {
		t.Errorf("Deliveries = %d, want 3", hit.Deliveries)
	}
}

// TestContractStandingLimitHitAgentDetailsAreCappedAndOneLined: the details
// cap and one-lining are the ROUTE's normalisation — the contract keeps them
// byte-identical for the slug path, and the agent path records through the
// same keyed writer, so both arrive normalised. Details are free text kraken
// did not author, rendered as one line.
func TestContractStandingLimitHitAgentDetailsAreCappedAndOneLined(t *testing.T) {
	useTestStore(t)
	mux := contractStandingLimitHitMux(t)

	long := "line one\nline two\r\tline three " + strings.Repeat("é", 500)
	rec := contractStandingLimitHitPost(t, mux,
		contractStandingLimitHitAgentBody("iris", "rate_limit", long, "iris-sess", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body.String())
	}

	hit := contractStandingLimitHitStored(t, "iris")
	if hit.At == 0 {
		t.Fatal("an over-long details string lost the hit — the error TYPE is what this feature routes on")
	}
	if n := len([]rune(hit.Details)); n > 200 {
		t.Errorf("Details is %d runes, want at most the 200-rune cap", n)
	}
	if strings.ContainsAny(hit.Details, "\n\r\t") {
		t.Errorf("Details still carries control characters: %q", hit.Details)
	}
}

// TestContractStandingLimitHitSlugFormIsUnchanged: "The slug path's behavior —
// including its error shapes and the details cap — is byte-identical to
// today." A slug delivery must still land on the JOB's id-keyed row and never
// on an agent key.
func TestContractStandingLimitHitSlugFormIsUnchanged(t *testing.T) {
	useTestStore(t)
	j := mustCreateStoreJob(t, jobs.ActiveStore(), store.Job{Slug: "standing-slugpath", Repo: "kraken", State: "working"})
	mux := contractStandingLimitHitMux(t)

	body, _ := json.Marshal(map[string]string{
		"slug": j.Slug, "error": "rate_limit", "details": "the banner",
		"session_id": "zeus-sess", "agent_id": "",
	})
	rec := contractStandingLimitHitPost(t, mux, string(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body.String())
	}

	hit := jobs.ReadLimitHit(j)
	if hit.At == 0 || hit.Error != "rate_limit" || hit.Details != "the banner" {
		t.Fatalf("the slug path stopped recording onto the job's own row: %+v", hit)
	}
	for _, id := range []string{"iris"} {
		if got := contractStandingLimitHitStored(t, id); got.At != 0 {
			t.Fatalf("a slug delivery also wrote limit_hit:%s: %+v", id, got)
		}
	}
	// The job's own slug is not a standing agent id, and must never be read
	// as one.
	if jobs.IsStandingAgentID(j.Slug) {
		t.Fatalf("a job slug %q reads as a standing agent id", j.Slug)
	}
}

// TestContractStandingLimitHitRequiresExactlyOneOfSlugAndAgent: "Exactly one
// of slug/agent must be non-empty: both or neither is a 400 whose body names
// the rule." Accepting both would leave which row is written to argument
// order; accepting neither would record a hit belonging to nobody.
func TestContractStandingLimitHitRequiresExactlyOneOfSlugAndAgent(t *testing.T) {
	useTestStore(t)
	j := mustCreateStoreJob(t, jobs.ActiveStore(), store.Job{Slug: "standing-xor", Repo: "kraken", State: "working"})
	mux := contractStandingLimitHitMux(t)

	cases := []struct {
		name string
		body string
	}{
		{"both", `{"slug":"` + j.Slug + `","agent":"iris","error":"rate_limit"}`},
		{"neither", `{"error":"rate_limit","details":"429"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := contractStandingLimitHitPost(t, mux, c.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body %s", rec.Code, rec.Body.String())
			}
			lower := strings.ToLower(rec.Body.String())
			if !strings.Contains(lower, "slug") || !strings.Contains(lower, "agent") {
				t.Errorf("body %q does not name the rule (both of slug and agent)", rec.Body.String())
			}
		})
	}
	if got := jobs.ReadLimitHit(j); got.At != 0 {
		t.Fatalf("a refused body still recorded onto the job: %+v", got)
	}
	if got := contractStandingLimitHitStored(t, "iris"); got.At != 0 {
		t.Fatalf("a refused body still recorded onto limit_hit:iris: %+v", got)
	}
}

// TestContractStandingLimitHitUnknownAgentIsRefusedLoudly: "An agent id
// failing jobs.IsStandingAgentID is a 400 naming the id." Naming it is the
// point — the only client is a hook kraken itself installed, so an id that
// does not validate means a stale hook or a hand-typed POST, and a silent
// refusal leaves nothing to grep for.
func TestContractStandingLimitHitUnknownAgentIsRefusedLoudly(t *testing.T) {
	useTestStore(t)
	mux := contractStandingLimitHitMux(t)
	const bogus = "hephaestus"
	if jobs.IsStandingAgentID(bogus) {
		t.Fatalf("fixture is wrong: %q is a registered standing agent", bogus)
	}

	rec := contractStandingLimitHitPost(t, mux,
		contractStandingLimitHitAgentBody(bogus, "rate_limit", "429", "sess", ""))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), bogus) {
		t.Errorf("body %q does not name the refused id %q", rec.Body.String(), bogus)
	}
	if got := contractStandingLimitHitStored(t, bogus); got.At != 0 {
		t.Fatalf("an unregistered id still got a row: %+v", got)
	}
}

// TestContractStandingLimitHitAgentFormStaysMirrorUngated: "The route stays
// mirror-ungated (recording is a read-side fact; the swap is not)." A mirror
// daemon must still RECORD what it saw — gating the record would leave a
// mirror unable to say what it observed, while the rescue itself stays inside
// the daemon's own !mirror block.
func TestContractStandingLimitHitAgentFormStaysMirrorUngated(t *testing.T) {
	useTestStore(t)
	SetMirrorMode(true)
	t.Cleanup(func() { SetMirrorMode(false) })
	mux := contractStandingLimitHitMux(t)

	rec := contractStandingLimitHitPost(t, mux,
		contractStandingLimitHitAgentBody("iris", "rate_limit", "429", "iris-sess", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d under --mirror, want 200; body %s", rec.Code, rec.Body.String())
	}
	if got := contractStandingLimitHitStored(t, "iris"); got.At == 0 {
		t.Fatal("a mirror recorded nothing — the observation is a read, only the swap is gated")
	}
}
