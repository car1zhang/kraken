package core

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"kraken/internal/util"
)

// slack_contract_test.go — blind contract tests for slice 1 of the Slack
// intake bot (50-contract.md § "Slice 1 — internal/core"): the slack.env
// loader, the model constant, the Web API client's decode/pagination
// obligations, and SlackTSLess.
//
// Dimension: stated invariants and happy paths. Boundaries, refusals and
// error paths (ok:false, transport failure, malformed values) belong to
// athena-edge and are deliberately not duplicated here — the one exception
// is the 429 path, which the contract states as a POSITIVE behaviour
// (SlackRateLimitError carries the header's duration) rather than a refusal.
//
// Every package-level identifier here is `contract`-prefixed: athena-edge
// shares this package's test binary.

// contractSlackSandbox points util.Home() at a throwaway dir and clears both
// allowlisted variables from the process environment, so a developer machine
// with a real ~/.kraken/slack.env or a real SLACK_USER_TOKEN exported cannot
// change what these tests measure.
func contractSlackSandbox(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SLACK_USER_TOKEN", "")
	t.Setenv("SLACK_D_COOKIE", "")
	t.Setenv("SLACK_USER_ID", "")
	if err := os.MkdirAll(filepath.Join(home, ".kraken"), 0o755); err != nil {
		t.Fatalf("mkdir ~/.kraken: %v", err)
	}
	return home
}

// contractWriteSlackEnv stages ~/.kraken/slack.env with the given body.
func contractWriteSlackEnv(t *testing.T, body string) {
	t.Helper()
	if err := os.WriteFile(SlackEnvPath(), []byte(body), 0o600); err != nil {
		t.Fatalf("write slack.env: %v", err)
	}
}

// TestContractSlackEnvPath — the credential file is ~/.kraken/slack.env,
// derived through util.Home() like every other path in the binary (never a
// hardcoded /Users/<name>).
func TestContractSlackEnvPath(t *testing.T) {
	home := contractSlackSandbox(t)
	want := filepath.Join(home, ".kraken", "slack.env")
	if got := SlackEnvPath(); got != want {
		t.Fatalf("SlackEnvPath() = %q, want %q", got, want)
	}
	if got := SlackEnvPath(); !strings.HasPrefix(got, util.Home()) {
		t.Fatalf("SlackEnvPath() = %q, want it under util.Home() %q", got, util.Home())
	}
}

// TestContractLoadSlackConfigParsesTheFile pins the asc.env/openai.env
// parse shape the contract points at — `export ` prefixes, quotes, comments,
// blank lines — plus the ALLOWLIST: exactly SLACK_USER_TOKEN and
// SLACK_USER_ID are honored, and any other key in the file is ignored rather
// than silently promoted into either field.
func TestContractLoadSlackConfigParsesTheFile(t *testing.T) {
	contractSlackSandbox(t)
	contractWriteSlackEnv(t, strings.Join([]string{
		"# carl's slack app, user scopes only",
		"",
		`export SLACK_USER_TOKEN="xoxp-file-token"`,
		"  SLACK_USER_ID = 'UFILE123' ",
		"",
		"# a key that is not on the allowlist must be ignored entirely",
		"SLACK_TOKEN=xoxp-decoy",
		"SLACK_BOT_TOKEN=xoxb-decoy",
		"USER_ID=UDECOY",
		"not even a key=value line",
		"",
	}, "\n"))

	cfg, err := LoadSlackConfig()
	if err != nil {
		t.Fatalf("LoadSlackConfig: %v", err)
	}
	if cfg.Token != "xoxp-file-token" {
		t.Errorf("Token = %q, want the quoted, export-prefixed file value unwrapped", cfg.Token)
	}
	if cfg.UserID != "UFILE123" {
		t.Errorf("UserID = %q, want the single-quoted, space-padded file value unwrapped", cfg.UserID)
	}
	if strings.Contains(cfg.Token, "decoy") || strings.Contains(cfg.UserID, "DECOY") {
		t.Errorf("a non-allowlisted key reached the config: %+v", cfg)
	}
}

// TestContractLoadSlackConfigEnvWinsOverFile — process env beats the file,
// per key. The split assertion matters: a loader that reads the file only
// when NEITHER variable is exported would pass a both-set test and still be
// wrong for the one-set case a terminal override actually produces.
func TestContractLoadSlackConfigEnvWinsOverFile(t *testing.T) {
	contractSlackSandbox(t)
	contractWriteSlackEnv(t, "SLACK_USER_TOKEN=xoxp-file-token\nSLACK_USER_ID=UFILE123\n")

	t.Setenv("SLACK_USER_TOKEN", "xoxp-env-token")
	cfg, err := LoadSlackConfig()
	if err != nil {
		t.Fatalf("LoadSlackConfig (token from env): %v", err)
	}
	if cfg.Token != "xoxp-env-token" {
		t.Errorf("Token = %q, want the environment's value to win over the file's", cfg.Token)
	}
	if cfg.UserID != "UFILE123" {
		t.Errorf("UserID = %q, want the file's value for the key the environment does not set", cfg.UserID)
	}

	t.Setenv("SLACK_USER_ID", "UENV999")
	cfg, err = LoadSlackConfig()
	if err != nil {
		t.Fatalf("LoadSlackConfig (both from env): %v", err)
	}
	if cfg.Token != "xoxp-env-token" || cfg.UserID != "UENV999" {
		t.Errorf("config = %+v, want both values from the environment", cfg)
	}
}

// TestContractLoadSlackConfigEnvOnlyNeedsNoFile: an exported token with no
// file at all is a configured feature — the file is one source, not the
// gate.
func TestContractLoadSlackConfigEnvOnlyNeedsNoFile(t *testing.T) {
	contractSlackSandbox(t)
	t.Setenv("SLACK_USER_TOKEN", "xoxp-env-only")

	cfg, err := LoadSlackConfig()
	if err != nil {
		t.Fatalf("LoadSlackConfig: %v", err)
	}
	if cfg.Token != "xoxp-env-only" {
		t.Fatalf("Token = %q, want the exported value with no file present", cfg.Token)
	}
}

// TestContractLoadSlackConfigUnconfiguredIsErrNoSlack pins the sentinel
// every caller switches on: no token anywhere is ErrNoSlack (matched with
// errors.Is, so a wrapped one still counts), never some other error a caller
// would log as a failure.
func TestContractLoadSlackConfigUnconfiguredIsErrNoSlack(t *testing.T) {
	contractSlackSandbox(t)

	_, err := LoadSlackConfig()
	if !errors.Is(err, ErrNoSlack) {
		t.Fatalf("err = %v, want ErrNoSlack for a machine with no slack.env and nothing exported", err)
	}
}

// TestContractIrisModelIsAnExactID — the model kraken names for iris is a
// pinned id, never an alias (an alias silently follows whatever claude ships
// next), and it sits beside the other two spawn constants.
func TestContractIrisModelIsAnExactID(t *testing.T) {
	if IrisModel != "claude-fable-5-1" {
		t.Fatalf("IrisModel = %q, want %q", IrisModel, "claude-fable-5-1")
	}
	if !strings.Contains(IrisModel, "-") || IrisModel == "opus" || IrisModel == "fable" {
		t.Fatalf("IrisModel = %q, want an exact model id rather than a tier alias", IrisModel)
	}
	// ModelArgs is the one way a model reaches a command line; iris' spawn
	// goes through it like every other kraken-started agent.
	if got := ModelArgs(IrisModel); len(got) != 2 || got[0] != "--model" || got[1] != IrisModel {
		t.Fatalf("ModelArgs(IrisModel) = %v, want [--model %s]", got, IrisModel)
	}
}

// --- SlackTSLess ---

// TestContractSlackTSLess pins NUMERIC ordering of "seconds.micros" strings.
// The lexicographic rows are the load-bearing ones: a ts never passes
// through a float, but it must also never be compared as raw text, which
// gets "9999999999" > "10000000000" exactly backwards at the ten-digit
// boundary Slack timestamps live on.
func TestContractSlackTSLess(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want bool
	}{
		{"seconds ascending", "1755000000.000200", "1755000001.000100", true},
		{"seconds descending", "1755000001.000100", "1755000000.000200", false},
		{"micros ascending", "1755000000.000200", "1755000000.001000", true},
		{"micros descending", "1755000000.001000", "1755000000.000200", false},
		{"equal is not less", "1755000000.000200", "1755000000.000200", false},
		// the lexicographic trap: "1..." sorts before "9..." as text, while
		// 9999999999 is genuinely the smaller second
		{"digit-count boundary", "9999999999.000000", "10000000000.000000", true},
		{"digit-count boundary reversed", "10000000000.000000", "9999999999.000000", false},
		// beyond float64's ~15 significant digits: a whole-value float
		// conversion collapses these two into one number and answers false
		// both ways
		{"micros below float precision", "1755000000.000001", "1755000000.000002", true},
		{"micros below float precision reversed", "1755000000.000002", "1755000000.000001", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := SlackTSLess(c.a, c.b); got != c.want {
				t.Fatalf("SlackTSLess(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
			}
		})
	}
}

// TestContractSlackTSLessIsAStrictOrder: irreflexive and antisymmetric on
// real timestamps, which is what makes it usable as a watermark comparison
// (a "ts <= mark" filter is expressed as !SlackTSLess(mark, ts)).
func TestContractSlackTSLessIsAStrictOrder(t *testing.T) {
	ts := []string{"1755000000.000100", "1755000000.000200", "1755000001.000000", "1755000002.999999"}
	for i, a := range ts {
		if SlackTSLess(a, a) {
			t.Errorf("SlackTSLess(%q, %q) = true, want false — a ts is not less than itself", a, a)
		}
		for j, b := range ts {
			if i == j {
				continue
			}
			if SlackTSLess(a, b) && SlackTSLess(b, a) {
				t.Errorf("both SlackTSLess(%q,%q) and SlackTSLess(%q,%q) are true", a, b, b, a)
			}
			if i < j && !SlackTSLess(a, b) {
				t.Errorf("SlackTSLess(%q, %q) = false, want true", a, b)
			}
		}
	}
}

// --- client fixtures ---

// contractSlackServer stands up an httptest.Server whose handler dispatches
// on the LAST path element (auth.test, conversations.history, …), records
// every request's parsed form, and answers from a per-method queue of JSON
// bodies. Method-agnostic on purpose: ParseForm reads a POST's form body and
// a GET's query string alike, and the contract freezes the PARAMS, not the
// verb.
type contractSlackServer struct {
	t        *testing.T
	srv      *httptest.Server
	bodies   map[string][]string
	requests map[string][]map[string]string
	auth     []string
}

func contractNewSlackServer(t *testing.T, bodies map[string][]string) *contractSlackServer {
	t.Helper()
	s := &contractSlackServer{
		t:        t,
		bodies:   bodies,
		requests: map[string][]map[string]string{},
	}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm for %s: %v", r.URL.Path, err)
		}
		method := r.URL.Path
		if i := strings.LastIndex(method, "/"); i >= 0 {
			method = method[i+1:]
		}
		form := map[string]string{}
		for k := range r.Form {
			form[k] = r.Form.Get(k)
		}
		s.requests[method] = append(s.requests[method], form)
		s.auth = append(s.auth, r.Header.Get("Authorization"))

		queue := s.bodies[method]
		n := len(s.requests[method]) - 1
		if n >= len(queue) {
			t.Errorf("%s called %d times, only %d fixture bodies queued", method, n+1, len(queue))
			http.Error(w, `{"ok":false,"error":"fixture_exhausted"}`, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(queue[n]))
	}))
	t.Cleanup(s.srv.Close)
	return s
}

// contractClient builds a SlackClient pointed at the fixture server — the
// BaseURL override the contract promises tests may use.
func (s *contractSlackServer) contractClient(token string) *SlackClient {
	c := NewSlackClient(token, "")
	c.BaseURL = s.srv.URL
	return c
}

// contractCalls returns the recorded forms for one API method.
func (s *contractSlackServer) contractCalls(method string) []map[string]string {
	return s.requests[method]
}

// contractTokenReached reports whether the token got to the server at all,
// accepting either shape Slack allows (a Bearer header or a `token` form
// field) — the contract freezes that the client authenticates, not how.
func (s *contractSlackServer) contractTokenReached(token string) bool {
	for _, a := range s.auth {
		if strings.Contains(a, token) {
			return true
		}
	}
	for _, calls := range s.requests {
		for _, form := range calls {
			if form["token"] == token {
				return true
			}
		}
	}
	return false
}

// TestContractNewSlackClientDefaults — the constructor's two defaults, both
// of which a test then overrides for BaseURL.
func TestContractNewSlackClientDefaults(t *testing.T) {
	c := NewSlackClient("xoxp-abc", "")
	if c == nil {
		t.Fatal("NewSlackClient returned nil")
	}
	if c.Token != "xoxp-abc" {
		t.Errorf("Token = %q", c.Token)
	}
	if c.BaseURL != "https://slack.com/api" {
		t.Errorf("BaseURL = %q, want %q", c.BaseURL, "https://slack.com/api")
	}
	if c.HTTPClient == nil {
		t.Fatal("HTTPClient is nil — a request would use http.DefaultClient's unbounded timeout")
	}
	if c.HTTPClient.Timeout <= 0 {
		t.Errorf("HTTPClient.Timeout = %v, want a bounded timeout", c.HTTPClient.Timeout)
	}
}

// TestContractAuthTestDecodesOK — the identity call the poller uses once to
// learn carl's own user id.
func TestContractAuthTestDecodesOK(t *testing.T) {
	s := contractNewSlackServer(t, map[string][]string{
		"auth.test": {`{"ok":true,"url":"https://probook.slack.com/","team":"probook","user":"carl","team_id":"T0001","user_id":"U0CARL"}`},
	})
	info, err := s.contractClient("xoxp-abc").AuthTest()
	if err != nil {
		t.Fatalf("AuthTest: %v", err)
	}
	if info.UserID != "U0CARL" || info.TeamID != "T0001" {
		t.Fatalf("SlackAuthInfo = %+v, want UserID U0CARL / TeamID T0001", info)
	}
	if !s.contractTokenReached("xoxp-abc") {
		t.Error("the token never reached the server — the call is unauthenticated")
	}
}

// TestContractUserConversationsPaginatesAndFiltersTypes: the DM/MPIM
// inventory follows next_cursor internally, and the cursor never escapes the
// call (the returned value is conversations, and the caller is given no
// cursor to persist).
func TestContractUserConversationsPaginatesAndFiltersTypes(t *testing.T) {
	s := contractNewSlackServer(t, map[string][]string{
		"users.conversations": {
			`{"ok":true,"channels":[{"id":"D111","is_im":true,"is_mpim":false},{"id":"G222","is_im":false,"is_mpim":true}],"response_metadata":{"next_cursor":"CURSOR2"}}`,
			`{"ok":true,"channels":[{"id":"D333","is_im":true,"is_mpim":false}],"response_metadata":{"next_cursor":""}}`,
		},
	})

	convs, err := s.contractClient("xoxp-abc").UserConversations()
	if err != nil {
		t.Fatalf("UserConversations: %v", err)
	}
	if len(convs) != 3 {
		t.Fatalf("got %d conversations (%+v), want all 3 across both pages", len(convs), convs)
	}
	byID := map[string]SlackConversation{}
	for _, c := range convs {
		byID[c.ID] = c
	}
	if !byID["D111"].IsIM || byID["D111"].IsMPIM {
		t.Errorf("D111 = %+v, want IsIM only", byID["D111"])
	}
	if byID["G222"].IsIM || !byID["G222"].IsMPIM {
		t.Errorf("G222 = %+v, want IsMPIM only", byID["G222"])
	}
	if _, ok := byID["D333"]; !ok {
		t.Errorf("the second page's conversation is missing: %+v", convs)
	}

	calls := s.contractCalls("users.conversations")
	if len(calls) != 2 {
		t.Fatalf("users.conversations called %d times, want 2 (one per page)", len(calls))
	}
	types := calls[0]["types"]
	if !strings.Contains(types, "im") || !strings.Contains(types, "mpim") {
		t.Errorf("types = %q, want it to name im and mpim", types)
	}
	if got := calls[0]["exclude_archived"]; got != "true" && got != "1" {
		t.Errorf("exclude_archived = %q, want true", got)
	}
	if calls[0]["cursor"] != "" {
		t.Errorf("first call carried cursor %q, want none", calls[0]["cursor"])
	}
	if calls[1]["cursor"] != "CURSOR2" {
		t.Errorf("second call's cursor = %q, want the first page's next_cursor", calls[1]["cursor"])
	}
}

// TestContractConversationHistoryPassesOldestVerbatim is the watermark
// obligation: `oldest` is a STRING handed to Slack exactly as stored, never
// re-formatted through a float, and the client adds no inclusive flag of its
// own (Slack's oldest is exclusive, which is what the mark semantics rely
// on). It also pins that every returned message carries the channel it was
// fetched for, since Slack's own history payload never repeats it.
func TestContractConversationHistoryPassesOldestVerbatim(t *testing.T) {
	const oldest = "1755000000.000200"
	s := contractNewSlackServer(t, map[string][]string{
		"conversations.history": {
			`{"ok":true,"has_more":true,"messages":[
				{"type":"message","user":"U0BOB","text":"first","ts":"1755000001.000100","reply_count":2,"latest_reply":"1755000009.000900"},
				{"type":"message","user":"U0BOB","text":"joined","subtype":"channel_join","ts":"1755000002.000100"}
			],"response_metadata":{"next_cursor":"CURSOR2"}}`,
			`{"ok":true,"has_more":false,"messages":[
				{"type":"message","user":"U0AMY","text":"second page","ts":"1755000003.000100","thread_ts":"1755000001.000100"}
			],"response_metadata":{"next_cursor":""}}`,
		},
	})

	msgs, err := s.contractClient("xoxp-abc").ConversationHistory("D111", oldest, 200)
	if err != nil {
		t.Fatalf("ConversationHistory: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("got %d messages (%+v), want all 3 across both pages", len(msgs), msgs)
	}
	for _, m := range msgs {
		if m.ChannelID != "D111" {
			t.Errorf("message %q has ChannelID %q, want the channel it was fetched for", m.TS, m.ChannelID)
		}
	}
	first := msgs[0]
	if first.TS != "1755000001.000100" || first.User != "U0BOB" || first.Text != "first" {
		t.Errorf("first message = %+v", first)
	}
	if first.ReplyCount != 2 || first.LatestReply != "1755000009.000900" {
		t.Errorf("first message thread fields = %+v, want ReplyCount 2 / LatestReply 1755000009.000900 (the reply-chasing signal)", first)
	}
	if msgs[1].SubType != "channel_join" {
		t.Errorf("subtype lost: %+v", msgs[1])
	}
	if msgs[2].ThreadTS != "1755000001.000100" {
		t.Errorf("thread_ts lost: %+v", msgs[2])
	}

	calls := s.contractCalls("conversations.history")
	if len(calls) != 2 {
		t.Fatalf("conversations.history called %d times, want 2 (paginated to exhaustion)", len(calls))
	}
	for i, c := range calls {
		if c["oldest"] != oldest {
			t.Errorf("call %d oldest = %q, want the mark verbatim (%q)", i, c["oldest"], oldest)
		}
		if c["channel"] != "D111" {
			t.Errorf("call %d channel = %q", i, c["channel"])
		}
		if c["inclusive"] != "" {
			t.Errorf("call %d sent inclusive=%q — Slack's oldest is exclusive and the mark semantics depend on it", i, c["inclusive"])
		}
	}
	if calls[1]["cursor"] != "CURSOR2" {
		t.Errorf("second call's cursor = %q, want the first page's next_cursor", calls[1]["cursor"])
	}
}

// TestContractConversationRepliesFollowsTheSameRules — the thread catch-up
// call: same verbatim oldest, same pagination, same ChannelID fill.
func TestContractConversationRepliesFollowsTheSameRules(t *testing.T) {
	const oldest = "1755000005.000000"
	s := contractNewSlackServer(t, map[string][]string{
		"conversations.replies": {
			`{"ok":true,"messages":[
				{"type":"message","user":"U0BOB","text":"parent","ts":"1755000001.000100","thread_ts":"1755000001.000100"},
				{"type":"message","user":"U0AMY","text":"reply one","ts":"1755000006.000100","thread_ts":"1755000001.000100"}
			],"response_metadata":{"next_cursor":"CURSOR2"}}`,
			`{"ok":true,"messages":[
				{"type":"message","user":"U0AMY","text":"reply two","ts":"1755000007.000100","thread_ts":"1755000001.000100","subtype":"thread_broadcast"}
			],"response_metadata":{"next_cursor":""}}`,
		},
	})

	msgs, err := s.contractClient("xoxp-abc").ConversationReplies("D111", "1755000001.000100", oldest)
	if err != nil {
		t.Fatalf("ConversationReplies: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("got %d replies (%+v), want all 3 across both pages", len(msgs), msgs)
	}
	for _, m := range msgs {
		if m.ChannelID != "D111" {
			t.Errorf("reply %q has ChannelID %q, want D111", m.TS, m.ChannelID)
		}
	}
	if msgs[2].SubType != "thread_broadcast" {
		t.Errorf("thread_broadcast subtype lost (%+v) — the dedupe against the history sweep keys off it", msgs[2])
	}

	calls := s.contractCalls("conversations.replies")
	if len(calls) != 2 {
		t.Fatalf("conversations.replies called %d times, want 2", len(calls))
	}
	for i, c := range calls {
		if c["oldest"] != oldest {
			t.Errorf("call %d oldest = %q, want %q verbatim", i, c["oldest"], oldest)
		}
		if c["channel"] != "D111" {
			t.Errorf("call %d channel = %q", i, c["channel"])
		}
		if c["ts"] != "1755000001.000100" && c["thread_ts"] != "1755000001.000100" {
			t.Errorf("call %d never named the thread parent: %v", i, c)
		}
	}
	if calls[1]["cursor"] != "CURSOR2" {
		t.Errorf("second call's cursor = %q, want the first page's next_cursor", calls[1]["cursor"])
	}
}

// TestContractSearchMessagesFillsChannelIdentity — mentions have no API, so
// search.messages is the only way they are found. Its results are the one
// place Slack supplies a channel NAME, and the fixed sort/count parameters
// are what make the client-side floor + dedupe sound.
func TestContractSearchMessagesFillsChannelIdentity(t *testing.T) {
	// next_cursor is queued in both documented places (response_metadata and
	// the messages.pagination object) so this fixture pins pagination
	// happening without freezing which of the two the client reads.
	s := contractNewSlackServer(t, map[string][]string{
		"search.messages": {
			`{"ok":true,"messages":{"total":2,"pagination":{"next_cursor":"CURSOR2"},"matches":[
				{"type":"message","ts":"1755000010.000100","user":"U0BOB","username":"bob","text":"<@U0CARL> can you look at the csv export","channel":{"id":"C0ENG","name":"engineering"}}
			]},"response_metadata":{"next_cursor":"CURSOR2"}}`,
			`{"ok":true,"messages":{"total":2,"pagination":{"next_cursor":""},"matches":[
				{"type":"message","ts":"1755000011.000100","user":"U0AMY","username":"amy","text":"<@U0CARL> ping","channel":{"id":"C0OPS","name":"ops"}}
			]},"response_metadata":{"next_cursor":""}}`,
		},
	})

	const query = "<@U0CARL> after:2026-08-02"
	msgs, err := s.contractClient("xoxp-abc").SearchMessages(query)
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("got %d matches (%+v), want both pages' matches", len(msgs), msgs)
	}
	if msgs[0].ChannelID != "C0ENG" || msgs[0].ChannelName != "engineering" {
		t.Errorf("first match = %+v, want ChannelID/ChannelName from the result's channel object", msgs[0])
	}
	if msgs[1].ChannelID != "C0OPS" || msgs[1].ChannelName != "ops" {
		t.Errorf("second match = %+v, want the second page's channel identity", msgs[1])
	}
	if msgs[0].TS != "1755000010.000100" || msgs[0].User != "U0BOB" {
		t.Errorf("first match ts/user = %+v", msgs[0])
	}

	calls := s.contractCalls("search.messages")
	if len(calls) != 2 {
		t.Fatalf("search.messages called %d times, want 2 (paginated to exhaustion)", len(calls))
	}
	if calls[0]["query"] != query {
		t.Errorf("query = %q, want the caller's query verbatim (%q)", calls[0]["query"], query)
	}
	if calls[0]["sort"] != "timestamp" {
		t.Errorf("sort = %q, want timestamp", calls[0]["sort"])
	}
	if calls[0]["sort_dir"] != "desc" {
		t.Errorf("sort_dir = %q, want desc", calls[0]["sort_dir"])
	}
	if calls[0]["count"] != "100" {
		t.Errorf("count = %q, want 100", calls[0]["count"])
	}
	if calls[0]["cursor"] != "*" {
		t.Errorf("first cursor = %q, want the literal * search.messages requires to start a cursored scan", calls[0]["cursor"])
	}
	if calls[1]["cursor"] != "CURSOR2" {
		t.Errorf("second cursor = %q, want the first page's next_cursor", calls[1]["cursor"])
	}
}

// TestContractUserInfoPrefersDisplayNameThenRealNameThenID pins the whole
// three-step fallback: an author with no profile at all still gets a
// non-empty name (its id), because an inbox row with a blank author is
// unusable to iris.
func TestContractUserInfoPrefersDisplayNameThenRealNameThenID(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			"display name wins",
			`{"ok":true,"user":{"id":"U0BOB","name":"bob","real_name":"Robert Tables","profile":{"display_name":"bobby","real_name":"Robert Tables"}}}`,
			"bobby",
		},
		{
			"real name when display name is blank",
			`{"ok":true,"user":{"id":"U0BOB","name":"bob","real_name":"Robert Tables","profile":{"display_name":"","real_name":"Robert Tables"}}}`,
			"Robert Tables",
		},
		{
			"id when neither is present",
			`{"ok":true,"user":{"id":"U0BOB","profile":{}}}`,
			"U0BOB",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := contractNewSlackServer(t, map[string][]string{"users.info": {c.body}})
			got, err := s.contractClient("xoxp-abc").UserInfo("U0BOB")
			if err != nil {
				t.Fatalf("UserInfo: %v", err)
			}
			if got != c.want {
				t.Fatalf("UserInfo = %q, want %q", got, c.want)
			}
			calls := s.contractCalls("users.info")
			if len(calls) != 1 || calls[0]["user"] != "U0BOB" {
				t.Fatalf("users.info calls = %v, want one naming the user", calls)
			}
		})
	}
}

// contractSlackRetryAfter walks err's unwrap chain for a SlackRateLimitError
// and reads its RetryAfter.
//
// Reflection rather than errors.As with a concrete target because the
// contract freezes the TYPE and its field but not whether Error() has a
// value or a pointer receiver — and a type switch naming both forms does not
// compile under either choice. This helper answers the obligation (a 429
// surfaces the header's duration) without pinning a decision the contract
// never made; see the report's contract-gap list.
func contractSlackRetryAfter(err error) (time.Duration, bool) {
	for e := err; e != nil; e = errors.Unwrap(e) {
		v := reflect.ValueOf(e)
		if v.Kind() == reflect.Ptr {
			if v.IsNil() {
				break
			}
			v = v.Elem()
		}
		if v.Kind() != reflect.Struct || v.Type().Name() != "SlackRateLimitError" {
			continue
		}
		f := v.FieldByName("RetryAfter")
		if !f.IsValid() {
			continue
		}
		d, ok := f.Interface().(time.Duration)
		return d, ok
	}
	return 0, false
}

// TestContractRateLimitCarriesRetryAfter — HTTP 429 is a POSITIVE behaviour
// of this client, not merely a failure: the pass' not-before deadline is
// computed from the duration in this error, so losing the header would turn
// a throttle into a hot retry loop.
func TestContractRateLimitCarriesRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"ok":false,"error":"ratelimited"}`))
	}))
	defer srv.Close()

	c := NewSlackClient("xoxp-secret-abc", "")
	c.BaseURL = srv.URL

	_, err := c.ConversationHistory("D111", "1755000000.000200", 200)
	if err == nil {
		t.Fatal("ConversationHistory returned no error on a 429 — a throttled read must never look like an empty conversation")
	}
	got, ok := contractSlackRetryAfter(err)
	if !ok {
		t.Fatalf("err = %v (%T), want a SlackRateLimitError in the chain", err, err)
	}
	if got != 30*time.Second {
		t.Fatalf("RetryAfter = %v, want 30s from the Retry-After header", got)
	}
	if strings.Contains(err.Error(), "xoxp-secret-abc") {
		t.Fatal("the token value appears in the error string")
	}
	// the error's own sentence must still read as a rate limit
	if msg := err.Error(); msg == "" {
		t.Fatal("SlackRateLimitError.Error() is empty")
	}
}
