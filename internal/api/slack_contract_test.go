package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kraken/internal/jobs"
	"kraken/internal/store"
)

// slack_contract_test.go — blind contract tests for slice 4's wire surface
// (50-contract.md § "Slice 4"): the three /api/slack routes plus /api/job/list
// and /api/job/draft's two SUCCESS rows.
//
// Dimension: stated invariants and happy paths. The refusal matrix (every
// 400/409 row), DisallowUnknownFields, and the mirror gate belong to
// athena-edge.
//
// Everything goes through the real mux (api.Register), like mirror_test.go:
// the contract freezes ROUTES, not handler function names, so a route test
// that called a handler by name would be pinning something the contract
// never said.
//
// Every package-level identifier is `contract`-prefixed and slack-scoped:
// watch_contract_test.go already owns the bare `contract*` names here.

func contractSlackMux(t *testing.T) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux)
	return mux
}

func contractSlackGet(t *testing.T, mux http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func contractSlackPost(t *testing.T, mux http.Handler, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(mustJSON(t, body))))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func contractSlackDecode(t *testing.T, rec *httptest.ResponseRecorder, into any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), into); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
}

// contractSlackNoCreds clears both allowlisted variables so `configured`
// measures the fixture rather than the developer's shell. useTestStore has
// already pointed HOME at a temp dir, so no slack.env can be found either.
func contractSlackNoCreds(t *testing.T) {
	t.Helper()
	t.Setenv("SLACK_USER_TOKEN", "")
	t.Setenv("SLACK_USER_ID", "")
}

func contractSlackSeedRow(t *testing.T, st *store.Store, channel, ts, source, text string) {
	t.Helper()
	n, err := st.AddSlackBatch([]store.SlackInboxMsg{{
		ChannelID:   channel,
		ChannelName: "engineering",
		TS:          ts,
		ThreadTS:    "",
		AuthorID:    "U0BOB",
		AuthorName:  "bobby",
		Source:      source,
		Text:        text,
		SeenAt:      1755000000000,
	}}, nil)
	if err != nil || n != 1 {
		t.Fatalf("AddSlackBatch(%s/%s) = %d, err %v", channel, ts, n, err)
	}
}

// --- GET /api/slack/inbox ---

// TestContractSlackInboxEmptyIsAnEmptyList: iris reads this JSON with a
// tool, so "nothing new" must decode as a list of length zero rather than
// null — a null is a shape change every consumer has to special-case.
func TestContractSlackInboxEmptyIsAnEmptyList(t *testing.T) {
	useTestStore(t)
	contractSlackNoCreds(t)
	mux := contractSlackMux(t)

	rec := contractSlackGet(t, mux, "/api/slack/inbox")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "null") {
		t.Fatalf("body = %s, want an empty list rather than null", rec.Body.String())
	}
	var envelope struct {
		Messages []map[string]json.RawMessage `json:"messages"`
	}
	contractSlackDecode(t, rec, &envelope)
	if envelope.Messages == nil {
		t.Fatal("messages decoded as nil — the empty inbox must serve []")
	}
	if len(envelope.Messages) != 0 {
		t.Fatalf("messages = %v, want empty", envelope.Messages)
	}
}

// TestContractSlackInboxServesUnackedRowsInIDOrder pins the payload's ten
// frozen keys and the two rules that make it usable: only rows still awaiting
// iris' verdict, in arrival order.
func TestContractSlackInboxServesUnackedRowsInIDOrder(t *testing.T) {
	st := useTestStore(t)
	contractSlackNoCreds(t)
	mux := contractSlackMux(t)

	contractSlackSeedRow(t, st, "D111", "1755000001.000100", "dm", "first")
	contractSlackSeedRow(t, st, "D111", "1755000002.000100", "dm", "second")
	contractSlackSeedRow(t, st, "C0ENG", "1755000003.000100", "mention", "<@U0CARL> third")

	rows, err := st.UnackedSlackInbox()
	if err != nil {
		t.Fatalf("UnackedSlackInbox: %v", err)
	}
	if err := st.AckSlackMessages([]int64{rows[1].ID}, "ignored", 1755000009000); err != nil {
		t.Fatalf("AckSlackMessages: %v", err)
	}

	rec := contractSlackGet(t, mux, "/api/slack/inbox")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Messages []struct {
			ID          int64  `json:"id"`
			ChannelID   string `json:"channel_id"`
			ChannelName string `json:"channel_name"`
			TS          string `json:"ts"`
			ThreadTS    string `json:"thread_ts"`
			AuthorID    string `json:"author_id"`
			AuthorName  string `json:"author_name"`
			Source      string `json:"source"`
			Text        string `json:"text"`
			SeenAt      int64  `json:"seen_at"`
		} `json:"messages"`
	}
	contractSlackDecode(t, rec, &envelope)

	if len(envelope.Messages) != 2 {
		t.Fatalf("got %d messages, want the 2 unacked rows: %s", len(envelope.Messages), rec.Body.String())
	}
	if envelope.Messages[0].ID >= envelope.Messages[1].ID {
		t.Fatalf("ids = %d, %d — want ascending (arrival order)", envelope.Messages[0].ID, envelope.Messages[1].ID)
	}
	first := envelope.Messages[0]
	if first.ID != rows[0].ID {
		t.Errorf("first id = %d, want the store row's id %d — the ack route addresses rows by it", first.ID, rows[0].ID)
	}
	if first.ChannelID != "D111" || first.ChannelName != "engineering" || first.TS != "1755000001.000100" {
		t.Errorf("first message channel/ts = %+v", first)
	}
	if first.AuthorID != "U0BOB" || first.AuthorName != "bobby" || first.Source != "dm" || first.Text != "first" {
		t.Errorf("first message author/source/text = %+v", first)
	}
	if first.SeenAt != 1755000000000 {
		t.Errorf("first message seen_at = %d, want the row's stamp", first.SeenAt)
	}
	if envelope.Messages[1].Source != "mention" {
		t.Errorf("second message = %+v, want the mention row (the acked one is gone)", envelope.Messages[1])
	}
	// ts must stay a JSON STRING end to end — a number here would have gone
	// through a float and lost the microseconds the mark comparison needs
	if !strings.Contains(rec.Body.String(), `"ts":"1755000001.000100"`) {
		t.Errorf("body = %s, want ts serialized as a string", rec.Body.String())
	}
}

// --- POST /api/slack/ack ---

// TestContractSlackAckSettlesTheNamedRows is iris' close-the-loop write: one
// call settles a batch, stamps the disposition, and takes those rows out of
// the inbox for good.
func TestContractSlackAckSettlesTheNamedRows(t *testing.T) {
	st := useTestStore(t)
	contractSlackNoCreds(t)
	mux := contractSlackMux(t)

	contractSlackSeedRow(t, st, "D111", "1755000001.000100", "dm", "first")
	contractSlackSeedRow(t, st, "D111", "1755000002.000100", "dm", "second")
	contractSlackSeedRow(t, st, "D111", "1755000003.000100", "dm", "third")
	rows, err := st.UnackedSlackInbox()
	if err != nil {
		t.Fatalf("UnackedSlackInbox: %v", err)
	}

	rec := contractSlackPost(t, mux, "/api/slack/ack", map[string]any{
		"ids":         []int64{rows[0].ID, rows[1].ID},
		"disposition": "drafted:csv-export-timeout",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	var ok struct {
		OK bool `json:"ok"`
	}
	contractSlackDecode(t, rec, &ok)
	if !ok.OK {
		t.Fatalf("body = %s, want {\"ok\":true}", rec.Body.String())
	}

	remaining, err := st.UnackedSlackInbox()
	if err != nil {
		t.Fatalf("UnackedSlackInbox after ack: %v", err)
	}
	if len(remaining) != 1 || remaining[0].ID != rows[2].ID {
		t.Fatalf("remaining = %+v, want only the un-acked row", remaining)
	}

	got := contractSlackGet(t, mux, "/api/slack/inbox")
	if strings.Contains(got.Body.String(), "first") || strings.Contains(got.Body.String(), "second") {
		t.Fatalf("acked rows still served: %s", got.Body.String())
	}
}

// TestContractSlackAckAcceptsAVerboseDisposition: the disposition is
// agent-written free text, so a long or multi-line one is CLIPPED
// (core.TruncateOneLine, 200) rather than refused — the ids are the payload,
// and losing the settle over a verbose sentence would leave those rows
// pending forever, re-bumped on every tick.
//
// Only the acceptance half is asserted here, and that is a CONTRACT GAP
// rather than a choice: the clipping is unobservable from outside
// internal/store. No frozen accessor returns an ACKED row (UnackedSlackInbox
// excludes it by definition, SlackInboxCounts is only numbers), the ack
// response is a bare {"ok":true}, and the store's db handle is unexported —
// so nothing a blind test can reach ever reports what was stored. See the
// report's contract-gap list.
func TestContractSlackAckAcceptsAVerboseDisposition(t *testing.T) {
	st := useTestStore(t)
	contractSlackNoCreds(t)
	mux := contractSlackMux(t)

	contractSlackSeedRow(t, st, "D111", "1755000001.000100", "dm", "first")
	contractSlackSeedRow(t, st, "D111", "1755000002.000100", "dm", "second")
	rows, err := st.UnackedSlackInbox()
	if err != nil {
		t.Fatalf("UnackedSlackInbox: %v", err)
	}

	if rec := contractSlackPost(t, mux, "/api/slack/ack", map[string]any{
		"ids": []int64{rows[0].ID}, "disposition": strings.Repeat("z", 500),
	}); rec.Code != http.StatusOK {
		t.Fatalf("long disposition: status %d, body %s", rec.Code, rec.Body.String())
	}
	if rec := contractSlackPost(t, mux, "/api/slack/ack", map[string]any{
		"ids": []int64{rows[1].ID}, "disposition": "drafted:thing\nand a second line\twith a tab",
	}); rec.Code != http.StatusOK {
		t.Fatalf("multiline disposition: status %d, body %s", rec.Code, rec.Body.String())
	}

	remaining, err := st.UnackedSlackInbox()
	if err != nil {
		t.Fatalf("UnackedSlackInbox after acks: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("remaining = %+v, want both rows settled", remaining)
	}
}

// --- GET /api/slack/status ---

// TestContractSlackStatusShape: the one debugging surface. `configured`
// tracks LoadSlackConfig, `last_poll_at` is the kv stamp (0 when the pass
// has never run), and the three counts come straight off the inbox — a
// failed poll has to read as STALENESS here rather than as an empty inbox.
func TestContractSlackStatusShape(t *testing.T) {
	st := useTestStore(t)
	contractSlackNoCreds(t)
	mux := contractSlackMux(t)

	type statusBody struct {
		Configured  bool  `json:"configured"`
		LastPollAt  int64 `json:"last_poll_at"`
		Total       int   `json:"total"`
		Undelivered int   `json:"undelivered"`
		Unacked     int   `json:"unacked"`
	}

	rec := contractSlackGet(t, mux, "/api/slack/status")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	var got statusBody
	contractSlackDecode(t, rec, &got)
	if got.Configured {
		t.Error("configured = true with no token anywhere")
	}
	if got.LastPollAt != 0 || got.Total != 0 || got.Undelivered != 0 || got.Unacked != 0 {
		t.Errorf("fresh status = %+v, want zeros", got)
	}
	// every key is present, not merely zero-valued by decode default
	for _, key := range []string{"configured", "last_poll_at", "total", "undelivered", "unacked"} {
		if !strings.Contains(rec.Body.String(), `"`+key+`"`) {
			t.Errorf("status body %s is missing the %q key", rec.Body.String(), key)
		}
	}

	t.Setenv("SLACK_USER_TOKEN", "xoxp-configured")
	if err := st.SetKV("slack_last_poll", "1755000000000"); err != nil {
		t.Fatalf("SetKV: %v", err)
	}
	contractSlackSeedRow(t, st, "D111", "1755000001.000100", "dm", "first")
	contractSlackSeedRow(t, st, "D111", "1755000002.000100", "dm", "second")
	rows, err := st.UnackedSlackInbox()
	if err != nil {
		t.Fatalf("UnackedSlackInbox: %v", err)
	}
	if err := st.MarkSlackDelivered([]int64{rows[0].ID}, 1755000005000); err != nil {
		t.Fatalf("MarkSlackDelivered: %v", err)
	}

	rec = contractSlackGet(t, mux, "/api/slack/status")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	got = statusBody{}
	contractSlackDecode(t, rec, &got)
	if !got.Configured {
		t.Error("configured = false with SLACK_USER_TOKEN exported")
	}
	if got.LastPollAt != 1755000000000 {
		t.Errorf("last_poll_at = %d, want the kv stamp", got.LastPollAt)
	}
	if got.Total != 2 || got.Undelivered != 1 || got.Unacked != 2 {
		t.Errorf("counts = %+v, want total 2 / undelivered 1 / unacked 2", got)
	}
}

// --- GET /api/job/list ---

// TestContractJobListShape is iris' board context: enough to decide whether
// a message pertains to something already on the board, and no more.
func TestContractJobListShape(t *testing.T) {
	st := useTestStore(t)
	contractSlackNoCreds(t)
	mux := contractSlackMux(t)

	if _, err := st.CreateJob(&store.Job{Slug: "csv-export-timeout", Repo: "kraken", Body: "\n\nFix the CSV export timeout\n\nMore detail below.\n"}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if _, err := st.CreateJob(&store.Job{Slug: "second-draft", Repo: "kraken", Body: "Ship the thing\n"}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if _, err := st.CreateJob(&store.Job{Slug: "running-thing", Repo: "kraken", State: "working", Body: "Already running\n"}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if _, err := st.CreateJob(&store.Job{Slug: "wordy", Repo: "kraken", Body: strings.Repeat("w", 400) + "\n"}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	archivedID, err := st.CreateJob(&store.Job{Slug: "gone", Repo: "kraken", State: "done", Body: "old\n"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if _, err := st.ArchiveJob(archivedID); err != nil {
		t.Fatalf("ArchiveJob: %v", err)
	}

	rec := contractSlackGet(t, mux, "/api/job/list")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Jobs []struct {
			Slug    string `json:"slug"`
			State   string `json:"state"`
			Summary string `json:"summary"`
		} `json:"jobs"`
	}
	contractSlackDecode(t, rec, &envelope)
	if envelope.Jobs == nil {
		t.Fatal("jobs decoded as nil — want a list")
	}

	bySlug := map[string]struct {
		Slug    string `json:"slug"`
		State   string `json:"state"`
		Summary string `json:"summary"`
	}{}
	for _, j := range envelope.Jobs {
		bySlug[j.Slug] = j
	}
	if _, ok := bySlug["gone"]; ok {
		t.Error("an archived job is on the list — active jobs only")
	}
	if len(bySlug) != 4 {
		t.Fatalf("jobs = %v, want the four active ones", envelope.Jobs)
	}

	// state is jobs.DisplayState, measured against the same function rather
	// than a hand-copied table
	all := jobs.LoadJobs()
	byJob := map[string]*jobs.Job{}
	for _, j := range all {
		byJob[j.Slug] = j
	}
	for slug, row := range bySlug {
		j := byJob[slug]
		if j == nil {
			t.Fatalf("job %s vanished from the store view", slug)
		}
		if want := jobs.DisplayState(j); row.State != want {
			t.Errorf("job %s state = %q, want DisplayState %q", slug, row.State, want)
		}
	}
	if bySlug["csv-export-timeout"].State != "draft" {
		t.Errorf("an unstarted job reads as %q, want draft", bySlug["csv-export-timeout"].State)
	}
	if bySlug["second-draft"].State != "draft" {
		t.Errorf("a second unstarted job reads as %q, want draft — there is no state between draft and working", bySlug["second-draft"].State)
	}
	if bySlug["running-thing"].State != "working" {
		t.Errorf("a working job reads as %q, want working", bySlug["running-thing"].State)
	}

	// summary is the first NON-EMPTY spec line: a body that opens with blank
	// lines must not summarize as ""
	if got := bySlug["csv-export-timeout"].Summary; got != "Fix the CSV export timeout" {
		t.Errorf("summary = %q, want the first non-empty spec line", got)
	}
	if n := len([]rune(bySlug["wordy"].Summary)); n > 120 {
		t.Errorf("summary is %d runes, want <= 120", n)
	}
	if bySlug["wordy"].Summary == "" {
		t.Error("a long first line summarized to nothing")
	}
}

// --- POST /api/job/draft ---

// contractSlackJobText composes a job file body iris would submit. It emits
// neither `ready:` nor `prereqs:`: the route refuses both outright now (they
// name a feature that is gone, and silently ignoring them would let a caller
// believe it had queued something), so a helper that emitted either would
// make every test below a test of that refusal.
func contractSlackJobText(slug, repo, base, model string, solo bool, body string) string {
	soloVal := "false"
	if solo {
		soloVal = "true"
	}
	out := "---\n" +
		"job: " + slug + "\n" +
		"repo: " + repo + "\n"
	if base != "" {
		out += "base: " + base + "\n"
	}
	out += "solo: " + soloVal + "\n"
	if model != "" {
		out += "model: " + model + "\n"
	}
	out += "---\n\n" + body + "\n"
	return out
}

// TestContractJobDraftCreatesADraftNobodyStarts is the create half of the
// route's whole point: iris turns a Slack message into a board card, and the
// card sits in draft until carl submits it. Every field it is allowed to set
// lands, and state stays empty — and with the queue pass gone there is no
// longer anything on the machine that could pick a draft up on its own.
func TestContractJobDraftCreatesADraftNobodyStarts(t *testing.T) {
	st := useTestStore(t)
	contractSlackNoCreds(t)
	mux := contractSlackMux(t)

	if _, err := st.CreateJob(&store.Job{Slug: "dep-thing", Repo: "kraken", Body: "a dependency\n"}); err != nil {
		t.Fatalf("seed dep: %v", err)
	}

	body := "Bob reports the CSV export times out over 50k rows.\n\n## Source\n\n- **From:** bobby (`U0BOB`) in #engineering\n- **Message:** the csv export times out\n"
	content := contractSlackJobText("csv-export-timeout", "kraken", "origin/main", "claude-opus-5", true, body)

	rec := contractSlackPost(t, mux, "/api/job/draft", map[string]any{
		"slug": "csv-export-timeout", "content": content,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		OK    bool   `json:"ok"`
		Slug  string `json:"slug"`
		MTime int64  `json:"mtime"`
	}
	contractSlackDecode(t, rec, &resp)
	if !resp.OK || resp.Slug != "csv-export-timeout" {
		t.Fatalf("response = %s, want ok:true and the slug", rec.Body.String())
	}

	sr, err := st.GetJobBySlug("csv-export-timeout")
	if err != nil {
		t.Fatalf("GetJobBySlug: %v", err)
	}
	if resp.MTime != sr.Rev {
		t.Errorf("mtime = %d, want the new row's rev %d (the optimistic-lock token a later edit sends back)", resp.MTime, sr.Rev)
	}
	if sr.Repo != "kraken" || sr.Base != "origin/main" || sr.Model != "claude-opus-5" || !sr.Solo {
		t.Errorf("parsed fields did not land: %+v", sr)
	}
	if !strings.Contains(sr.Body, "## Source") {
		t.Errorf("body = %q, want the provenance section preserved", sr.Body)
	}
	if sr.State != "" {
		t.Errorf("state = %q, want empty on a fresh draft", sr.State)
	}
	if sr.Err != "" {
		t.Errorf("Err = %q, want a clean parse", sr.Err)
	}

	// board-visible as a draft, through the same view the board renders
	all := jobs.LoadJobs()
	byJob := map[string]*jobs.Job{}
	for _, j := range all {
		byJob[j.Slug] = j
	}
	j := byJob["csv-export-timeout"]
	if j == nil {
		t.Fatal("the created draft is not on the board")
	}
	if got := jobs.DisplayState(j); got != "draft" {
		t.Fatalf("DisplayState = %q, want draft", got)
	}
	if j.State != "" || j.Err != "" {
		t.Fatalf("a job in this shape is not a clean draft: state=%q err=%q", j.State, j.Err)
	}

	// and it shows up on the list route iris reads for board context
	list := contractSlackGet(t, mux, "/api/job/list")
	if !strings.Contains(list.Body.String(), "csv-export-timeout") {
		t.Errorf("the new draft is missing from /api/job/list: %s", list.Body.String())
	}
}

// TestContractJobDraftEditsOnlyTheAuthoredFields is the edit half: iris may
// extend a DRAFT job's body (a follow-up message about work already carded),
// and everything kraken owns about that row must come back out exactly as it
// went in. There is no longer a `ready` flag for iris to promote a job with:
// starting a job is submitting it, and this route has no submit.
func TestContractJobDraftEditsOnlyTheAuthoredFields(t *testing.T) {
	st := useTestStore(t)
	contractSlackNoCreds(t)
	mux := contractSlackMux(t)

	id, err := st.CreateJob(&store.Job{
		Slug: "csv-export-timeout", Repo: "kraken", Base: "origin/main",
		Solo: false, Model: "", Color: "cyan",
		Body: "original spec\n", Session: "sess-abc", Run: 2, Outcome: "",
	})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if err := st.UpsertPR(id, "https://github.com/car1zhang/kraken/pull/9", "open"); err != nil {
		t.Fatalf("UpsertPR: %v", err)
	}
	before, err := st.GetJobByID(id)
	if err != nil {
		t.Fatalf("GetJobByID: %v", err)
	}

	newBody := "original spec\n\n## Source\n\n- **From:** bobby (`U0BOB`) in a DM\n- **Message:** also breaks for xlsx\n"
	content := contractSlackJobText("csv-export-timeout", "monolith", "origin/develop", "claude-opus-5", true, newBody)

	rec := contractSlackPost(t, mux, "/api/job/draft", map[string]any{
		"slug": "csv-export-timeout", "content": content, "mtime": before.Rev,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		OK    bool   `json:"ok"`
		Slug  string `json:"slug"`
		MTime int64  `json:"mtime"`
	}
	contractSlackDecode(t, rec, &resp)
	if !resp.OK || resp.Slug != "csv-export-timeout" {
		t.Fatalf("response = %s", rec.Body.String())
	}

	after, err := st.GetJobByID(id)
	if err != nil {
		t.Fatalf("GetJobByID after edit: %v", err)
	}
	if resp.MTime != after.Rev {
		t.Errorf("mtime = %d, want the row's new rev %d", resp.MTime, after.Rev)
	}

	// the five authored fields moved
	if after.Repo != "monolith" || after.Base != "origin/develop" || !after.Solo || after.Model != "claude-opus-5" {
		t.Errorf("authored fields did not land: %+v", after)
	}
	if !strings.Contains(after.Body, "also breaks for xlsx") {
		t.Errorf("body = %q, want the appended provenance", after.Body)
	}

	// nothing kraken owns moved
	if after.State != before.State {
		t.Errorf("state = %q, want the stored %q", after.State, before.State)
	}
	if after.Session != "sess-abc" || after.Run != 2 || after.Color != "cyan" {
		t.Errorf("kraken-owned fields moved: session=%q run=%d color=%q", after.Session, after.Run, after.Color)
	}
	if after.Outcome != before.Outcome || after.Teardown != before.Teardown {
		t.Errorf("outcome/teardown moved: %q/%q", after.Outcome, after.Teardown)
	}
	if after.Slug != "csv-export-timeout" {
		t.Errorf("slug = %q — this route never renames a job", after.Slug)
	}
	prs, err := st.ListPRs(id)
	if err != nil {
		t.Fatalf("ListPRs: %v", err)
	}
	if len(prs) != 1 || prs[0].URL != "https://github.com/car1zhang/kraken/pull/9" {
		t.Errorf("prs = %+v, want the stored PR list untouched", prs)
	}

	// the edit is board-visible and the card still reads as a draft
	all := jobs.LoadJobs()
	byJob := map[string]*jobs.Job{}
	for _, j := range all {
		byJob[j.Slug] = j
	}
	if got := jobs.DisplayState(byJob["csv-export-timeout"]); got != "draft" {
		t.Errorf("DisplayState after edit = %q, want draft (unchanged)", got)
	}
}

// TestContractJobDraftRefusesRetiredKeys: `ready:` and `prereqs:` are refused
// LOUDLY on this route rather than ignored, which is the same call the old
// ready-change guard made and for the same reason. ParseJob deliberately
// swallows both (the corpus of job files on disk carries them, and erroring
// there would pin the whole board to draft), so without an explicit refusal
// here an author sending `ready: true` would get a 200 and report to carl
// that the job was queued while nothing at all had happened.
func TestContractJobDraftRefusesRetiredKeys(t *testing.T) {
	useTestStore(t)
	contractSlackNoCreds(t)
	mux := contractSlackMux(t)

	for _, c := range []struct{ name, line string }{
		{"ready true", "ready: true\n"},
		{"ready false", "ready: false\n"},
		{"prereqs empty", "prereqs: []\n"},
		{"prereqs block", "prereqs:\n  - job: dep\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			content := "---\njob: retired-key\nrepo: kraken\n" + c.line + "---\n\na real spec\n"
			rec := contractSlackPost(t, mux, "/api/job/draft", map[string]any{
				"slug": "retired-key", "content": content,
			})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400 — body %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "retired key") {
				t.Errorf("refusal = %s, want it to name the key as retired", rec.Body.String())
			}
		})
	}

	// the word appearing in the SPEC BODY is not a refusal: a spec is free to
	// discuss prereqs in prose, and a route that refused that would be
	// unfixable without deleting the sentence.
	content := "---\njob: prose-ok\nrepo: kraken\n---\n\nCheck the prereqs: none, ready: to go.\n"
	rec := contractSlackPost(t, mux, "/api/job/draft", map[string]any{
		"slug": "prose-ok", "content": content,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("body prose refused: status %d, body %s", rec.Code, rec.Body.String())
	}
}

// TestContractJobDraftEditsADraftWithoutAnMTime: the lock is optional — an
// absent or zero mtime skips the check, which is what lets iris write a
// follow-up without having read the row first.
func TestContractJobDraftEditsADraftWithoutAnMTime(t *testing.T) {
	st := useTestStore(t)
	contractSlackNoCreds(t)
	mux := contractSlackMux(t)

	id, err := st.CreateJob(&store.Job{Slug: "quiet-draft", Repo: "kraken", Body: "first note\n"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	content := contractSlackJobText("quiet-draft", "kraken", "", "", false, "first note\n\nsecond note\n")

	rec := contractSlackPost(t, mux, "/api/job/draft", map[string]any{
		"slug": "quiet-draft", "content": content,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("no-mtime edit: status %d, body %s", rec.Code, rec.Body.String())
	}
	after, err := st.GetJobByID(id)
	if err != nil {
		t.Fatalf("GetJobByID: %v", err)
	}
	if !strings.Contains(after.Body, "second note") {
		t.Fatalf("body = %q, want the edit applied", after.Body)
	}
	if after.State != "" {
		t.Errorf("the edit moved the job's state to %q — this route never starts anything", after.State)
	}
}
