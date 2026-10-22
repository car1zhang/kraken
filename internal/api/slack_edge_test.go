package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kraken/internal/jobs"
	"kraken/internal/store"
)

// slack_edge_test.go — the EDGE dimension of slice 4 of
// artifacts/50-contract.md. Its centre is `/api/job/draft`'s refusal matrix:
// one case per row, each asserting BOTH the status code and that NOTHING was
// written, because this is the only route in the binary an LLM drives
// unattended and a refusal that half-applied would be discovered days later
// as a job nobody wrote. Happy paths and the stated wire invariants belong to
// the sibling blind file.
//
// Every route is driven through the real mux (api.Register), the same wiring
// web.Serve uses — the contract freezes ROUTES rather than handler symbols,
// so the mux is the only surface it actually names.
//
// Every package-level identifier declared here is prefixed `edge` — this
// package's test namespace is shared with the other blind author.

// --- harness ---------------------------------------------------------------

// edgeSlackAPI wires a temp store, clears the two Slack env keys (useTestStore
// already sandboxes HOME, so ~/.kraken/slack.env is absent) and returns the
// store beside a mux carrying every registered route.
func edgeSlackAPI(t *testing.T) (*store.Store, http.Handler) {
	t.Helper()
	st := useTestStore(t)
	t.Setenv("SLACK_USER_TOKEN", "")
	t.Setenv("SLACK_USER_ID", "")
	mux := http.NewServeMux()
	Register(mux)
	return st, mux
}

func edgePostAPI(t *testing.T, mux http.Handler, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	return edgePostAPIRaw(t, mux, path, string(mustJSON(t, body)))
}

func edgePostAPIRaw(t *testing.T, mux http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func edgeGetAPI(t *testing.T, mux http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// edgeDraftText assembles a job file body the way iris would: frontmatter
// lines, then the spec plus its provenance section.
func edgeDraftText(front []string, body string) string {
	return "---\n" + strings.Join(front, "\n") + "\n---\n\n" + body
}

// edgeDraftBody is the ordinary, well-formed content every refusal case
// varies exactly one thing away from.
func edgeDraftBody() string {
	return "# Spec\n\nThe CSV export times out on large orgs.\n\n## Source\n\nSlack DM from someone (U0001) in D1:\n\n> the csv export times out\n"
}

// edgeNoJob asserts the slug resolves to no active job at all — the "nothing
// is written on any refusal" half of every CREATE row.
func edgeNoJob(t *testing.T, st *store.Store, slug string) {
	t.Helper()
	j, err := st.GetJobBySlug(slug)
	if err == nil {
		t.Fatalf("a refused create wrote job %+v", j)
	}
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetJobBySlug(%q): %v, want ErrNotFound", slug, err)
	}
}

// edgeErrField decodes the `{"error": "..."}` sentence a refusal on this
// surface serves, so an assertion pins the REASON rather than the JSON
// encoding of it. Falls back to the raw body when the response is not that
// shape, so a malformed refusal still shows up in the failure message.
func edgeErrField(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var e struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil || e.Error == "" {
		return rec.Body.String()
	}
	return e.Error
}

// edgeJob reads an active job back, failing the test if it is gone.
func edgeJob(t *testing.T, st *store.Store, slug string) *store.Job {
	t.Helper()
	j, err := st.GetJobBySlug(slug)
	if err != nil {
		t.Fatalf("GetJobBySlug(%q): %v", slug, err)
	}
	return j
}

// --- /api/job/draft — CREATE refusals -------------------------------------

// TestEdgeJobDraftCreateRefusalMatrix walks the CREATE half of the contract's
// matrix. Each row states the condition and the status; every row also
// asserts that no job row appeared, which is the clause the matrix's preamble
// applies to all of them at once.
func TestEdgeJobDraftCreateRefusalMatrix(t *testing.T) {
	cases := []struct {
		name    string
		slug    string
		content string
		want    int
	}{
		{
			name:    "slug empty after Slugify",
			slug:    "   ",
			content: edgeDraftText([]string{"job: whatever", "repo: kraken"}, edgeDraftBody()),
			want:    http.StatusBadRequest,
		},
		{
			name:    "slug is punctuation only",
			slug:    "!!! ???",
			content: edgeDraftText([]string{"job: whatever", "repo: kraken"}, edgeDraftBody()),
			want:    http.StatusBadRequest,
		},
		{
			name:    "content empty",
			slug:    "csv-export-timeout",
			content: "",
			want:    http.StatusBadRequest,
		},
		{
			name:    "content whitespace only",
			slug:    "csv-export-timeout",
			content: "   \n\t\n",
			want:    http.StatusBadRequest,
		},
		{
			name:    "ready: true — iris drafts stay unqueued",
			slug:    "csv-export-timeout",
			content: edgeDraftText([]string{"job: csv-export-timeout", "repo: kraken", "ready: true"}, edgeDraftBody()),
			want:    http.StatusBadRequest,
		},
		{
			name:    "state: working",
			slug:    "csv-export-timeout",
			content: edgeDraftText([]string{"job: csv-export-timeout", "repo: kraken", "state: working"}, edgeDraftBody()),
			want:    http.StatusBadRequest,
		},
		{
			name:    "state: done",
			slug:    "csv-export-timeout",
			content: edgeDraftText([]string{"job: csv-export-timeout", "repo: kraken", "state: done"}, edgeDraftBody()),
			want:    http.StatusBadRequest,
		},
		{
			name:    "empty body under valid frontmatter",
			slug:    "csv-export-timeout",
			content: edgeDraftText([]string{"job: csv-export-timeout", "repo: kraken"}, ""),
			want:    http.StatusBadRequest,
		},
		{
			name:    "whitespace-only body",
			slug:    "csv-export-timeout",
			content: edgeDraftText([]string{"job: csv-export-timeout", "repo: kraken"}, "   \n\n\t\n"),
			want:    http.StatusBadRequest,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st, mux := edgeSlackAPI(t)
			rec := edgePostAPI(t, mux, "/api/job/draft", map[string]any{"slug": c.slug, "content": c.content})
			if rec.Code != c.want {
				t.Fatalf("status %d, want %d (body %s)", rec.Code, c.want, rec.Body.String())
			}
			edgeNoJob(t, st, "csv-export-timeout")
			jobsList, err := st.ListJobs()
			if err != nil {
				t.Fatalf("ListJobs: %v", err)
			}
			if len(jobsList) != 0 {
				t.Fatalf("a refused create left %d job(s) behind: %+v", len(jobsList), jobsList)
			}
		})
	}
}

// TestEdgeJobDraftCreateParseErrorIsNeverSalvaged is the row with the extra
// clause: the raw editor SAVES an unparseable body alongside its error so a
// human can fix it in place, and this route deliberately does NOT. Iris is
// not a human at an editor — a salvaged half-job would sit on the board as a
// draft nobody wrote, and the 400 has to carry the parse text or iris has
// nothing to correct.
func TestEdgeJobDraftCreateParseErrorIsNeverSalvaged(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"no frontmatter at all", "# Spec\n\njust a body\n"},
		{"unterminated frontmatter", "---\njob: csv-export-timeout\nrepo: kraken\n\n# Spec\n"},
		{"frontmatter line with no colon", edgeDraftText([]string{"job: csv-export-timeout", "repo kraken"}, edgeDraftBody())},
		{"duplicate key", edgeDraftText([]string{"job: csv-export-timeout", "repo: kraken", "repo: monolith"}, edgeDraftBody())},
		// `ready: yes` used to be the non-boolean case here; the key is
		// accept-and-ignore in the parser now (the corpus needs it), so it no
		// longer errs and could not exercise this row. `solo` is the same
		// shape and is still validated.
		{"non-boolean solo", edgeDraftText([]string{"job: csv-export-timeout", "repo: kraken", "solo: yes"}, edgeDraftBody())},
		{"hand-mangled state", edgeDraftText([]string{"job: csv-export-timeout", "repo: kraken", "state: queued"}, edgeDraftBody())},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st, mux := edgeSlackAPI(t)
			// The fixture is only meaningful if ParseJob really does refuse it.
			parsed := jobs.ParseJob("csv-export-timeout.job.md", []byte(c.content))
			if parsed.Err == "" {
				t.Fatalf("fixture parses clean — it cannot exercise the parse-error row:\n%s", c.content)
			}

			rec := edgePostAPI(t, mux, "/api/job/draft",
				map[string]any{"slug": "csv-export-timeout", "content": c.content})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400 (body %s)", rec.Code, rec.Body.String())
			}
			// Measured against the DECODED error field, not the raw bytes:
			// the obligation is about the payload, and JSON escapes the
			// double quotes ParseJob puts around the offending token — a raw
			// substring test would fail on `bad line "repo kraken"` while the
			// text it is looking for is right there in the response.
			if got := edgeErrField(t, rec); !strings.Contains(got, parsed.Err) {
				t.Errorf("400 error = %q, want it to carry the parse error %q", got, parsed.Err)
			}
			edgeNoJob(t, st, "csv-export-timeout")
		})
	}
}

// TestEdgeJobDraftCreateSucceedsAsADraft is the control the refusals above
// need: with every guard cleared the route DOES create, the job reads as a
// draft, and it carries no state — so the matrix is measuring the guards
// rather than a route that refuses everything.
func TestEdgeJobDraftCreateSucceedsAsADraft(t *testing.T) {
	st, mux := edgeSlackAPI(t)
	content := edgeDraftText([]string{"job: csv-export-timeout", "repo: kraken"}, edgeDraftBody())

	rec := edgePostAPI(t, mux, "/api/job/draft", map[string]any{"slug": "csv-export-timeout", "content": content})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	j := edgeJob(t, st, "csv-export-timeout")
	if j.State != "" {
		t.Errorf("created job carries state %q, want none", j.State)
	}
	if j.Err != "" {
		t.Errorf("created job carries a parse error %q", j.Err)
	}
	if !strings.Contains(j.Body, "## Source") {
		t.Errorf("body did not survive the create:\n%s", j.Body)
	}
	live := jobs.JobBySlug(jobs.LoadJobs(), "csv-export-timeout")
	if live == nil {
		t.Fatal("the created job is not on the board")
	}
	if got := jobs.DisplayState(live); got != "draft" {
		t.Fatalf("display state = %q, want draft — only carl's submit starts it", got)
	}
}

// --- /api/job/draft — EDIT refusals ---------------------------------------

// edgeSeedEditable creates a job iris IS allowed to edit (stored state empty)
// carrying a full set of kraken-owned values, so a successful edit can be
// checked for collateral damage and a refused one for having changed nothing.
func edgeSeedEditable(t *testing.T, st *store.Store, slug string) *store.Job {
	t.Helper()
	j := &store.Job{
		Slug: slug, Repo: "kraken", Base: "main", Solo: true,
		Model: "sonnet", Color: "Cyan", Session: "aaaa-bbbb-cccc", Run: 3,
		Teardown: "kill", Outcome: "",
		Body: "# Spec\n\noriginal body\n\n## Source\n\noriginal source\n",
	}
	if _, err := st.CreateJob(j); err != nil {
		t.Fatalf("CreateJob %s: %v", slug, err)
	}
	if err := st.UpsertPR(j.ID, "https://github.com/example/kraken/pull/7", "open"); err != nil {
		t.Fatalf("UpsertPR: %v", err)
	}
	return edgeJob(t, st, slug)
}

// edgeSeedStated creates a job whose stored state is one iris may NOT edit.
func edgeSeedStated(t *testing.T, st *store.Store, slug, state string) *store.Job {
	t.Helper()
	j := &store.Job{
		Slug: slug, Repo: "kraken", State: state,
		Body: "# Spec\n\noriginal body\n",
	}
	if _, err := st.CreateJob(j); err != nil {
		t.Fatalf("CreateJob %s: %v", slug, err)
	}
	return edgeJob(t, st, slug)
}

// TestEdgeJobDraftEditRefusesLiveStates is the gate the contract calls
// "deliberately stricter than editableJob": a working cell has zeus reading
// the spec it was briefed on, so rewriting the body underneath it is the one
// edit that changes what an already-running agent is building. `done` is
// refused for the opposite reason — the record of what shipped is not iris'
// to amend.
func TestEdgeJobDraftEditRefusesLiveStates(t *testing.T) {
	for _, state := range []string{"working", "blocked", "done"} {
		t.Run(state, func(t *testing.T) {
			st, mux := edgeSlackAPI(t)
			before := edgeSeedStated(t, st, "live-job", state)

			content := edgeDraftText([]string{"job: live-job", "repo: kraken"},
				"# Spec\n\nREWRITTEN\n\n## Source\n\nslack\n")
			rec := edgePostAPI(t, mux, "/api/job/draft", map[string]any{"slug": "live-job", "content": content})
			if rec.Code != http.StatusConflict {
				t.Fatalf("status %d, want 409 (body %s)", rec.Code, rec.Body.String())
			}
			after := edgeJob(t, st, "live-job")
			if after.Body != before.Body || after.Rev != before.Rev {
				t.Fatalf("a refused edit still wrote: body %q rev %d -> body %q rev %d",
					before.Body, before.Rev, after.Body, after.Rev)
			}
		})
	}
}

// TestEdgeJobDraftRefusesRetiredKeys replaces the ready-flip matrix. There is
// no flag left to flip in either direction — the queued status is gone and
// starting a job is submitting it on the board — but the "loudly, never
// silently ignored" rule that matrix existed for still binds, and now applies
// to the KEY rather than to a change in its value. ParseJob accepts both keys
// on purpose (the corpus needs it), so an author sending one would otherwise
// get a 200 for a line the server threw away.
func TestEdgeJobDraftRefusesRetiredKeys(t *testing.T) {
	for _, tc := range []struct {
		name  string
		front string
	}{
		{"ready true", "ready: true"},
		{"ready false", "ready: false"},
		{"prereqs empty", "prereqs: []"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, mux := edgeSlackAPI(t)
			before := edgeSeedEditable(t, st, "flip-job")

			content := edgeDraftText([]string{"job: flip-job", "repo: kraken", tc.front},
				"# Spec\n\nREWRITTEN\n\n## Source\n\nslack\n")
			rec := edgePostAPI(t, mux, "/api/job/draft", map[string]any{"slug": "flip-job", "content": content})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400 (body %s)", rec.Code, rec.Body.String())
			}
			after := edgeJob(t, st, "flip-job")
			if after.Body != before.Body || after.Rev != before.Rev {
				t.Fatalf("a refused edit still wrote: %+v -> %+v", before, after)
			}
		})
	}
}

// TestEdgeJobDraftEditRefusesStateAndEmptyBody covers the two remaining EDIT
// 400s. A `state:` from iris is an attempt to move a job through the
// lifecycle, which only kraken writes; an empty body would erase the spec a
// human wrote while reporting success.
func TestEdgeJobDraftEditRefusesStateAndEmptyBody(t *testing.T) {
	cases := []struct {
		name    string
		front   []string
		body    string
		wantErr bool
	}{
		{"state: working on an editable job", []string{"job: edit-job", "repo: kraken", "state: working"}, "# Spec\n\nkeep\n", true},
		{"state: done on an editable job", []string{"job: edit-job", "repo: kraken", "state: done"}, "# Spec\n\nkeep\n", true},
		{"empty body", []string{"job: edit-job", "repo: kraken"}, "", true},
		{"whitespace body", []string{"job: edit-job", "repo: kraken"}, "  \n\t\n", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st, mux := edgeSlackAPI(t)
			before := edgeSeedEditable(t, st, "edit-job")

			rec := edgePostAPI(t, mux, "/api/job/draft", map[string]any{
				"slug": "edit-job", "content": edgeDraftText(c.front, c.body),
			})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400 (body %s)", rec.Code, rec.Body.String())
			}
			after := edgeJob(t, st, "edit-job")
			if after.Body != before.Body || after.State != before.State || after.Rev != before.Rev {
				t.Fatalf("a refused edit still wrote: %+v -> %+v", before, after)
			}
		})
	}
}

// TestEdgeJobDraftEditMTimeLock pins the optimistic lock's three states: a
// stale rev is a 409, the matching rev saves, and 0/absent skips the check.
// Iris reads a job with `kraken job raw` and writes it back some turns later,
// which is exactly the window carl's own board edit lands in.
func TestEdgeJobDraftEditMTimeLock(t *testing.T) {
	st, mux := edgeSlackAPI(t)
	before := edgeSeedEditable(t, st, "lock-job")
	content := edgeDraftText([]string{"job: lock-job", "repo: kraken"},
		"# Spec\n\nEXTENDED\n\n## Source\n\nslack\n")

	rec := edgePostAPI(t, mux, "/api/job/draft",
		map[string]any{"slug": "lock-job", "content": content, "mtime": before.Rev - 1})
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale mtime: status %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	if after := edgeJob(t, st, "lock-job"); after.Body != before.Body || after.Rev != before.Rev {
		t.Fatalf("a 409'd edit still wrote: %+v", after)
	}

	rec = edgePostAPI(t, mux, "/api/job/draft",
		map[string]any{"slug": "lock-job", "content": content, "mtime": before.Rev})
	if rec.Code != http.StatusOK {
		t.Fatalf("matching mtime: status %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		OK    bool   `json:"ok"`
		Slug  string `json:"slug"`
		MTime int64  `json:"mtime"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	after := edgeJob(t, st, "lock-job")
	if !resp.OK || resp.Slug != "lock-job" || resp.MTime != after.Rev {
		t.Fatalf("response = %+v, want ok/slug/mtime=%d (the NEW rev)", resp, after.Rev)
	}

	// mtime 0 and an absent key both skip the check — a caller that never
	// read the job is not thereby locked out.
	for _, body := range []map[string]any{
		{"slug": "lock-job", "content": content, "mtime": 0},
		{"slug": "lock-job", "content": content},
	} {
		if rec := edgePostAPI(t, mux, "/api/job/draft", body); rec.Code != http.StatusOK {
			t.Fatalf("%v: status %d, want 200 — mtime 0/absent skips the lock (body %s)",
				body, rec.Code, rec.Body.String())
		}
	}
}

// TestEdgeJobDraftEditPreservesEverythingItDoesNotOwn is the last matrix row,
// and the one whose failure is silent: a full-replace write built from the
// parsed text alone would blank `session`, `run`, `prs`, `color`, `outcome`
// and `teardown` — losing shipping history, orphaning a resumable session and
// making the PR tracker re-notify — while returning 200 and looking right on
// the card.
func TestEdgeJobDraftEditPreservesEverythingItDoesNotOwn(t *testing.T) {
	st, mux := edgeSlackAPI(t)
	before := edgeSeedEditable(t, st, "preserve-job")
	beforePRs, err := st.ListPRs(before.ID)
	if err != nil {
		t.Fatalf("ListPRs: %v", err)
	}
	if len(beforePRs) != 1 {
		t.Fatalf("fixture has %d PRs, want 1", len(beforePRs))
	}

	content := edgeDraftText(
		[]string{"job: preserve-job", "repo: monolith", "base: develop", "solo: false", "model: opus"},
		"# Spec\n\nEXTENDED by iris\n\n## Source\n\nSlack DM\n")
	rec := edgePostAPI(t, mux, "/api/job/draft", map[string]any{"slug": "preserve-job", "content": content})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}

	after := edgeJob(t, st, "preserve-job")
	// the five fields this route DOES own
	if after.Repo != "monolith" || after.Base != "develop" || after.Solo || after.Model != "opus" {
		t.Errorf("editable fields did not land: %+v", after)
	}
	if !strings.Contains(after.Body, "EXTENDED by iris") {
		t.Errorf("body did not land:\n%s", after.Body)
	}
	// everything else
	if after.Slug != "preserve-job" {
		t.Errorf("slug changed to %q — this route never renames", after.Slug)
	}
	if after.State != before.State {
		t.Errorf("state = %q, want the stored %q", after.State, before.State)
	}
	if after.Session != before.Session {
		t.Errorf("session = %q, want the stored %q", after.Session, before.Session)
	}
	if after.Run != before.Run {
		t.Errorf("run = %d, want the stored %d", after.Run, before.Run)
	}
	if after.Color != before.Color {
		t.Errorf("color = %q, want the stored %q", after.Color, before.Color)
	}
	if after.Outcome != before.Outcome {
		t.Errorf("outcome = %q, want the stored %q", after.Outcome, before.Outcome)
	}
	if after.Teardown != before.Teardown {
		t.Errorf("teardown = %q, want the stored %q", after.Teardown, before.Teardown)
	}
	afterPRs, err := st.ListPRs(after.ID)
	if err != nil {
		t.Fatalf("ListPRs: %v", err)
	}
	if len(afterPRs) != 1 || afterPRs[0].URL != beforePRs[0].URL {
		t.Errorf("prs after an iris edit = %+v, want the stored %+v", afterPRs, beforePRs)
	}
}

// TestEdgeJobDraftRejectsMalformedBodies: a request body that is not JSON at
// all must be a 400 rather than a decode panic or a partial write.
func TestEdgeJobDraftRejectsMalformedBodies(t *testing.T) {
	for _, body := range []string{"", "not json", `{"slug":`, `[]`} {
		st, mux := edgeSlackAPI(t)
		rec := edgePostAPIRaw(t, mux, "/api/job/draft", body)
		if rec.Code < 400 || rec.Code >= 500 {
			t.Errorf("body %q: status %d, want a 4xx", body, rec.Code)
		}
		list, err := st.ListJobs()
		if err != nil {
			t.Fatalf("ListJobs: %v", err)
		}
		if len(list) != 0 {
			t.Fatalf("body %q wrote %d job(s)", body, len(list))
		}
	}
}

// --- /api/slack/ack -------------------------------------------------------

// edgeSeedInboxAPI commits two undelivered, unacked rows and returns their ids.
func edgeSeedInboxAPI(t *testing.T, st *store.Store) []int64 {
	t.Helper()
	msgs := []store.SlackInboxMsg{
		{ChannelID: "D1", ChannelName: "carl", TS: "1769040000.000100", AuthorID: "U0001",
			AuthorName: "someone", Source: "dm", Text: "the csv export times out", SeenAt: 1769040000000},
		{ChannelID: "D1", ChannelName: "carl", TS: "1769040000.000200", AuthorID: "U0001",
			AuthorName: "someone", Source: "dm", Text: "any idea?", SeenAt: 1769040000000},
	}
	n, err := st.AddSlackBatch(msgs, nil)
	if err != nil || n != 2 {
		t.Fatalf("AddSlackBatch: n=%d err=%v", n, err)
	}
	rows, err := st.UnackedSlackInbox()
	if err != nil {
		t.Fatalf("UnackedSlackInbox: %v", err)
	}
	ids := make([]int64, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	return ids
}

func edgeUnackedCount(t *testing.T, st *store.Store) int {
	t.Helper()
	_, _, unacked, err := st.SlackInboxCounts()
	if err != nil {
		t.Fatalf("SlackInboxCounts: %v", err)
	}
	return unacked
}

// TestEdgeSlackAckRefusals sweeps the route's own 400s. Each also asserts the
// inbox is untouched: an ack that half-applied would take a message out of
// the queue with no record of what was done about it.
func TestEdgeSlackAckRefusals(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"absent ids", `{"disposition":"ignored"}`},
		{"empty ids", `{"ids":[],"disposition":"ignored"}`},
		{"null ids", `{"ids":null,"disposition":"ignored"}`},
		{"absent disposition", `{"ids":[1]}`},
		{"empty disposition", `{"ids":[1],"disposition":""}`},
		{"whitespace disposition", `{"ids":[1],"disposition":"   \t  "}`},
		{"empty body object", `{}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st, mux := edgeSlackAPI(t)
			edgeSeedInboxAPI(t, st)
			rec := edgePostAPIRaw(t, mux, "/api/slack/ack", c.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400 (body %s)", rec.Code, rec.Body.String())
			}
			if got := edgeUnackedCount(t, st); got != 2 {
				t.Fatalf("unacked = %d, want 2 — a refused ack applies nothing", got)
			}
		})
	}
}

// TestEdgeSlackAckRejectsUnknownFields pins DisallowUnknownFields on the one
// route an LLM composes bodies for by hand. A misspelled key that decoded
// silently would ack the ids under a disposition nobody wrote — or, worse,
// under the zero value the misspelled field left behind.
func TestEdgeSlackAckRejectsUnknownFields(t *testing.T) {
	st, mux := edgeSlackAPI(t)
	ids := edgeSeedInboxAPI(t, st)

	for _, body := range []string{
		`{"ids":[` + edgeItoa(ids[0]) + `],"disposition":"ignored","at":999}`,
		`{"ids":[` + edgeItoa(ids[0]) + `],"disposition":"ignored","dispositon":"typo"}`,
		`{"ids":[` + edgeItoa(ids[0]) + `],"disposition":"ignored","message_ids":[1]}`,
		`{"ack":{"ids":[` + edgeItoa(ids[0]) + `],"disposition":"ignored"}}`,
	} {
		rec := edgePostAPIRaw(t, mux, "/api/slack/ack", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", body, rec.Code)
		}
		if got := edgeUnackedCount(t, st); got != 2 {
			t.Fatalf("%s: unacked = %d, want 2", body, got)
		}
	}
}

// TestEdgeSlackAckSurfacesTheStoreRefusalAs409 covers the two store-level
// refusals reaching the wire with their own sentence: an unknown id and a
// re-ack. 409 rather than 400 because the request was well-formed and the
// STATE is what refuses it — and iris' remedy differs (re-read the inbox
// rather than fix the body).
func TestEdgeSlackAckSurfacesTheStoreRefusalAs409(t *testing.T) {
	st, mux := edgeSlackAPI(t)
	ids := edgeSeedInboxAPI(t, st)
	unknown := ids[1] + 4242

	rec := edgePostAPI(t, mux, "/api/slack/ack",
		map[string]any{"ids": []int64{ids[0], unknown}, "disposition": "drafted:csv-export-timeout"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("unknown id: status %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), edgeItoa(unknown)) {
		t.Errorf("409 body = %s, want the store's own sentence naming id %d", rec.Body.String(), unknown)
	}
	if got := edgeUnackedCount(t, st); got != 2 {
		t.Fatalf("unacked = %d, want 2 — nothing is applied on a refusal", got)
	}

	// A clean ack, then the same ack again.
	if rec := edgePostAPI(t, mux, "/api/slack/ack",
		map[string]any{"ids": []int64{ids[0]}, "disposition": "ignored"}); rec.Code != http.StatusOK {
		t.Fatalf("first ack: status %d, body %s", rec.Code, rec.Body.String())
	}
	if got := edgeUnackedCount(t, st); got != 1 {
		t.Fatalf("unacked = %d after one ack, want 1", got)
	}
	rec = edgePostAPI(t, mux, "/api/slack/ack",
		map[string]any{"ids": []int64{ids[0]}, "disposition": "ignored"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("re-ack: status %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	if got := edgeUnackedCount(t, st); got != 1 {
		t.Fatalf("unacked = %d after a refused re-ack, want 1", got)
	}
}

// TestEdgeSlackAckClipsRatherThanRefusesALongDisposition: the ids are the
// payload, so an over-long or multi-line disposition is truncated and
// one-lined (core.TruncateOneLine at 200) rather than costing iris the whole
// ack — the same call jobLoadNote makes, and for the same reason.
func TestEdgeSlackAckClipsRatherThanRefusesALongDisposition(t *testing.T) {
	st, mux := edgeSlackAPI(t)
	ids := edgeSeedInboxAPI(t, st)

	rec := edgePostAPI(t, mux, "/api/slack/ack", map[string]any{
		"ids": []int64{ids[0]}, "disposition": "drafted:" + strings.Repeat("z", 500),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("long disposition: status %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if got := edgeUnackedCount(t, st); got != 1 {
		t.Fatalf("unacked = %d, want 1 — the ack landed", got)
	}

	rec = edgePostAPI(t, mux, "/api/slack/ack", map[string]any{
		"ids": []int64{ids[1]}, "disposition": "drafted:x\nsecond line\tand a tab",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("multi-line disposition: status %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if got := edgeUnackedCount(t, st); got != 0 {
		t.Fatalf("unacked = %d, want 0", got)
	}
}

// edgeItoa keeps the raw-body fixtures above free of a strconv import used
// nowhere else in this file.
func edgeItoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf []byte
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	if neg {
		return "-" + string(buf)
	}
	return string(buf)
}

// --- the three read routes' empty and boundary shapes ---------------------

// TestEdgeSlackInboxServesAnArrayNeverNull: `null` and `[]` decode
// differently in Swift and in TypeScript, and an inbox that reads as null is
// an error state in both clients rather than "nothing to do".
func TestEdgeSlackInboxServesAnArrayNeverNull(t *testing.T) {
	st, mux := edgeSlackAPI(t)

	rec := edgeGetAPI(t, mux, "/api/slack/inbox")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(strings.ReplaceAll(rec.Body.String(), " ", ""), `"messages":[]`) {
		t.Fatalf("empty inbox = %s, want an empty ARRAY", rec.Body.String())
	}

	ids := edgeSeedInboxAPI(t, st)
	if err := st.AckSlackMessages(ids[:1], "ignored", 1769040001000); err != nil {
		t.Fatalf("AckSlackMessages: %v", err)
	}
	var resp struct {
		Messages []map[string]any `json:"messages"`
	}
	rec = edgeGetAPI(t, mux, "/api/slack/inbox")
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	if len(resp.Messages) != 1 {
		t.Fatalf("inbox = %+v, want only the unacked row", resp.Messages)
	}
	// The wire shape is frozen: an acked row's stamps are not on it, and a
	// key that is not in the contract must not appear either.
	allowed := map[string]bool{
		"id": true, "channel_id": true, "channel_name": true, "ts": true,
		"thread_ts": true, "author_id": true, "author_name": true,
		"source": true, "text": true, "seen_at": true,
	}
	for k := range resp.Messages[0] {
		if !allowed[k] {
			t.Errorf("inbox message grew key %q — the shape is frozen", k)
		}
	}
}

// TestEdgeSlackStatusOnAnUnconfiguredMachine: the observability surface has to
// answer honestly on a machine that has never had a token — a 500 or a
// missing key here is what carl would be staring at while trying to find out
// why nothing arrived.
func TestEdgeSlackStatusOnAnUnconfiguredMachine(t *testing.T) {
	st, mux := edgeSlackAPI(t)

	rec := edgeGetAPI(t, mux, "/api/slack/status")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	for _, k := range []string{"configured", "last_poll_at", "total", "undelivered", "unacked"} {
		if _, ok := resp[k]; !ok {
			t.Errorf("status is missing key %q", k)
		}
	}
	if resp["configured"] != false {
		t.Errorf("configured = %v with no token anywhere, want false", resp["configured"])
	}
	if n, _ := resp["last_poll_at"].(float64); n != 0 {
		t.Errorf("last_poll_at = %v on a machine that never polled, want 0", resp["last_poll_at"])
	}

	// A failed poll must read as STALENESS rather than as an empty inbox, so
	// the counts come from the rows and the stamp comes from kv.
	edgeSeedInboxAPI(t, st)
	if err := st.SetKV("slack_last_poll", "1769040000000"); err != nil {
		t.Fatalf("SetKV: %v", err)
	}
	rec = edgeGetAPI(t, mux, "/api/slack/status")
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	if n, _ := resp["last_poll_at"].(float64); n != 1769040000000 {
		t.Errorf("last_poll_at = %v, want the kv stamp", resp["last_poll_at"])
	}
	if n, _ := resp["total"].(float64); n != 2 {
		t.Errorf("total = %v, want 2", resp["total"])
	}
	if n, _ := resp["unacked"].(float64); n != 2 {
		t.Errorf("unacked = %v, want 2", resp["unacked"])
	}
}

// TestEdgeJobListBoundaries: an empty board serves an array, an archived job
// is not on it, and a spec whose first line is enormous is CLIPPED rather
// than shipped whole — this list is read into iris' context on every batch.
func TestEdgeJobListBoundaries(t *testing.T) {
	st, mux := edgeSlackAPI(t)

	rec := edgeGetAPI(t, mux, "/api/job/list")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(strings.ReplaceAll(rec.Body.String(), " ", ""), `"jobs":[]`) {
		t.Fatalf("empty list = %s, want an empty ARRAY", rec.Body.String())
	}

	long := strings.Repeat("é", 400) // multi-byte on purpose: the bound is RUNES
	live := &store.Job{Slug: "listed", Repo: "kraken",
		Body: "\n\n" + long + "\nsecond line\n"}
	if _, err := st.CreateJob(live); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	gone := &store.Job{Slug: "shelved", Repo: "kraken", Body: "# Spec\n\nold\n"}
	if _, err := st.CreateJob(gone); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if _, err := st.ArchiveJob(gone.ID); err != nil {
		t.Fatalf("ArchiveJob: %v", err)
	}

	var resp struct {
		Jobs []struct {
			Slug    string `json:"slug"`
			State   string `json:"state"`
			Summary string `json:"summary"`
		} `json:"jobs"`
	}
	rec = edgeGetAPI(t, mux, "/api/job/list")
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	if len(resp.Jobs) != 1 || resp.Jobs[0].Slug != "listed" {
		t.Fatalf("jobs = %+v, want only the active one — an archived slug is free to be reused", resp.Jobs)
	}
	if n := len([]rune(resp.Jobs[0].Summary)); n > 120 {
		t.Errorf("summary is %d runes, want <= 120", n)
	}
	if strings.ContainsAny(resp.Jobs[0].Summary, "\n\r") {
		t.Errorf("summary = %q, want the FIRST non-empty line only", resp.Jobs[0].Summary)
	}
	if resp.Jobs[0].State != "draft" {
		t.Errorf("state = %q, want jobs.DisplayState's answer (draft)", resp.Jobs[0].State)
	}
}

// --- mirror gating --------------------------------------------------------

// edgeSlackWriteRoutes and edgeSlackReadRoutes split the new surface the way
// the mirror policy does: a mirror board is read-only, full stop, but a read
// is a read.
var edgeSlackWriteRoutes = []string{"/api/slack/ack", "/api/job/draft"}

var edgeSlackReadRoutes = []string{"/api/slack/inbox", "/api/slack/status", "/api/job/list"}

// TestEdgeSlackWritesAreMirrorGated pins both the status and the exact
// sentence every other gated route in this package serves — a mirror board
// that answered 200 here would write into the PRIMARY machine's kraken.db.
func TestEdgeSlackWritesAreMirrorGated(t *testing.T) {
	st, mux := edgeSlackAPI(t)
	SetMirrorMode(true)
	t.Cleanup(func() { SetMirrorMode(false) })

	for _, path := range edgeSlackWriteRoutes {
		t.Run(path, func(t *testing.T) {
			rec := edgePostAPI(t, mux, path, map[string]any{"slug": "nonexistent"})
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status %d, want 403 (body %s)", rec.Code, rec.Body.String())
			}
			if got := strings.TrimSpace(rec.Body.String()); got != "disabled in mirror mode" {
				t.Fatalf("body = %q, want the standard refusal string", got)
			}
		})
	}
	if list, err := st.ListJobs(); err != nil || len(list) != 0 {
		t.Fatalf("a mirror-refused write left %v (err %v)", list, err)
	}
}

// TestEdgeSlackReadsSurviveMirrorMode is the converse: the gate must be on the
// two writes alone, or a mirror board's slack view would be permanently
// empty and its job list unreachable.
func TestEdgeSlackReadsSurviveMirrorMode(t *testing.T) {
	_, mux := edgeSlackAPI(t)
	SetMirrorMode(true)
	t.Cleanup(func() { SetMirrorMode(false) })

	for _, path := range edgeSlackReadRoutes {
		t.Run(path, func(t *testing.T) {
			rec := edgeGetAPI(t, mux, path)
			if rec.Code == http.StatusForbidden {
				t.Fatalf("%s is gated under mirror mode — a read is a read", path)
			}
			if rec.Code != http.StatusOK {
				t.Fatalf("%s: status %d, want 200 (body %s)", path, rec.Code, rec.Body.String())
			}
		})
	}
}

// TestEdgeSlackWritesAreLiveWithMirrorOff proves the gate is conditional
// rather than a permanent 403 — the mistake the mirror table's own
// mirror-off sweep exists to catch.
func TestEdgeSlackWritesAreLiveWithMirrorOff(t *testing.T) {
	_, mux := edgeSlackAPI(t)
	SetMirrorMode(false)

	for _, path := range edgeSlackWriteRoutes {
		t.Run(path, func(t *testing.T) {
			rec := edgePostAPI(t, mux, path, map[string]any{"slug": "nonexistent"})
			if rec.Code == http.StatusForbidden {
				t.Fatalf("%s: 403 with mirror mode off", path)
			}
		})
	}
}

// --- the wire contract's version ------------------------------------------

// TestEdgeSlackRoutesAreAdditive: every shape in this slice is new, so
// apiContract must not move ON THEIR ACCOUNT. A bump for them would make the
// shipped iOS app refuse to talk to this build over a change that could not
// affect it. The literal tracks whatever the contract currently is — 7 since
// the fleet gate, a real break these routes had no part in.
func TestEdgeSlackRoutesAreAdditive(t *testing.T) {
	if apiContract != 7 {
		t.Fatalf("apiContract = %d, want 7 (bumped by the fleet gate) — five additive routes bump nothing", apiContract)
	}
}
