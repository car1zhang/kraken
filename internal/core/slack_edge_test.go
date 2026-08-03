package core

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// slack_edge_test.go — the EDGE dimension of slice 1 of
// artifacts/50-contract.md: what the env loader and the Slack client REFUSE,
// what they must never leak, and the boundaries `ts` comparison has to hold.
// Happy paths and stated invariants belong to the sibling blind file; nothing
// here duplicates them.
//
// Every package-level identifier declared in this file is prefixed `edge` —
// this package's test namespace is shared with the other blind author.

// --- helpers ---------------------------------------------------------------

// edgeSandboxHome points util.Home() at a throwaway dir so nothing in this
// file can read (or create) the real ~/.kraken/slack.env, and clears both
// allowlisted process-env keys so "not configured" is the starting state
// regardless of the developer's shell.
func edgeSandboxHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SLACK_USER_TOKEN", "")
	t.Setenv("SLACK_USER_ID", "")
	if err := os.MkdirAll(filepath.Join(home, ".kraken"), 0o755); err != nil {
		t.Fatalf("mkdir ~/.kraken: %v", err)
	}
	return home
}

// edgeWriteSlackEnv writes the credential file at whatever SlackEnvPath()
// resolves to under the sandboxed home.
func edgeWriteSlackEnv(t *testing.T, body string) {
	t.Helper()
	path := SlackEnvPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for slack.env: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write slack.env: %v", err)
	}
}

// edgeSecret is a token-shaped value planted where a careless loader would
// echo it back. It is deliberately distinctive so a substring test on an
// error string is meaningful.
const edgeSecret = "xoxp-EDGE-9f3a1c-DO-NOT-LEAK-THIS"

// --- 1. SlackEnvPath -------------------------------------------------------

// TestEdgeSlackEnvPathIsHomeRelative pins the repo's absolute filesystem rule
// (CLAUDE.md: every path derives from util.Home()) on the new credential
// file: a hardcoded /Users/<name> would pass every other test in this file.
func TestEdgeSlackEnvPathIsHomeRelative(t *testing.T) {
	home := edgeSandboxHome(t)
	got := SlackEnvPath()
	want := filepath.Join(home, ".kraken", "slack.env")
	if got != want {
		t.Fatalf("SlackEnvPath() = %q, want %q", got, want)
	}
}

// --- 1. LoadSlackConfig refusals ------------------------------------------

// TestEdgeLoadSlackConfigNotConfiguredShapes sweeps every way the feature can
// be OFF. The contract's wording is absolute — "a missing, unreadable, or
// malformed file with no env token yields ErrNoSlack, never any other error"
// — so each case asserts errors.Is AND that the returned config is empty: a
// half-filled SlackConfig beside an error is the shape a caller would use.
func TestEdgeLoadSlackConfigNotConfiguredShapes(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T)
	}{
		{"no file at all", func(t *testing.T) {}},
		{"empty file", func(t *testing.T) { edgeWriteSlackEnv(t, "") }},
		{"whitespace only", func(t *testing.T) { edgeWriteSlackEnv(t, "\n\n   \n\t\n") }},
		{"malformed: no separators", func(t *testing.T) {
			edgeWriteSlackEnv(t, "this is not an env file at all\n{\"json\": true}\n")
		}},
		{"malformed: binary noise", func(t *testing.T) {
			edgeWriteSlackEnv(t, "\x00\x01\x02\xff\xfe SLACK_USER_TOKEN\n")
		}},
		{"comments only", func(t *testing.T) {
			edgeWriteSlackEnv(t, "# SLACK_USER_TOKEN="+edgeSecret+"\n# nothing else\n")
		}},
		{"token key with an empty value", func(t *testing.T) {
			edgeWriteSlackEnv(t, "SLACK_USER_TOKEN=\n")
		}},
		{"token key with a whitespace value", func(t *testing.T) {
			edgeWriteSlackEnv(t, "SLACK_USER_TOKEN=   \n")
		}},
		{"export form with an empty value", func(t *testing.T) {
			edgeWriteSlackEnv(t, "export SLACK_USER_TOKEN=\"\"\n")
		}},
		{"only the user id, no token", func(t *testing.T) {
			edgeWriteSlackEnv(t, "SLACK_USER_ID=U12345\n")
		}},
		{"unreadable: a directory where the file goes", func(t *testing.T) {
			if err := os.MkdirAll(SlackEnvPath(), 0o755); err != nil {
				t.Fatalf("mkdir over slack.env: %v", err)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			edgeSandboxHome(t)
			c.setup(t)
			cfg, err := LoadSlackConfig()
			if !errors.Is(err, ErrNoSlack) {
				t.Fatalf("err = %v, want errors.Is(err, ErrNoSlack)", err)
			}
			if cfg.Token != "" || cfg.UserID != "" {
				t.Fatalf("config = %+v, want the zero SlackConfig beside ErrNoSlack", cfg)
			}
		})
	}
}

// TestEdgeLoadSlackConfigIgnoresNonAllowlistedKeys pins the allowlist as a
// REFUSAL rather than a preference: a file full of Slack-shaped keys that are
// not the two named ones leaves the feature off, and a file that carries the
// token plus junk honors only the two.
func TestEdgeLoadSlackConfigIgnoresNonAllowlistedKeys(t *testing.T) {
	edgeSandboxHome(t)
	edgeWriteSlackEnv(t, strings.Join([]string{
		"SLACK_BOT_TOKEN=" + edgeSecret,
		"SLACK_TOKEN=" + edgeSecret,
		"SLACK_APP_TOKEN=" + edgeSecret,
		"SLACK_USER_TOKEN_2=" + edgeSecret,
		"slack_user_token=" + edgeSecret, // case matters: the allowlist is exact
		"TOKEN=" + edgeSecret,
	}, "\n")+"\n")
	if cfg, err := LoadSlackConfig(); !errors.Is(err, ErrNoSlack) {
		t.Fatalf("err = %v (config %+v), want ErrNoSlack — none of those keys is allowlisted", err, cfg)
	}

	edgeSandboxHome(t)
	edgeWriteSlackEnv(t, strings.Join([]string{
		"SLACK_USER_TOKEN=xoxp-real",
		"SLACK_USER_ID=U12345",
		"SLACK_TEAM_ID=T99999",
		"OPENAI_API_KEY=" + edgeSecret,
		"HOME=/somewhere/else",
	}, "\n")+"\n")
	cfg, err := LoadSlackConfig()
	if err != nil {
		t.Fatalf("LoadSlackConfig: %v", err)
	}
	if cfg.Token != "xoxp-real" || cfg.UserID != "U12345" {
		t.Fatalf("config = %+v, want only the two allowlisted keys honored", cfg)
	}
}

// TestEdgeLoadSlackConfigEnvBeatsAMalformedFile is the contrapositive of the
// "malformed file yields ErrNoSlack" row: the file is only ONE of the two
// sources, so garbage in it must not take the feature down when the process
// env carries a token.
func TestEdgeLoadSlackConfigEnvBeatsAMalformedFile(t *testing.T) {
	edgeSandboxHome(t)
	edgeWriteSlackEnv(t, "!!! not an env file !!!\nSLACK_USER_TOKEN\n")
	t.Setenv("SLACK_USER_TOKEN", "xoxp-from-env")

	cfg, err := LoadSlackConfig()
	if err != nil {
		t.Fatalf("LoadSlackConfig: %v", err)
	}
	if cfg.Token != "xoxp-from-env" {
		t.Fatalf("token = %q, want the process env's value", cfg.Token)
	}
}

// TestEdgeLoadSlackConfigNeverLeaksTheTokenValue pins the last clause of the
// LoadSlackConfig row. The secret is planted in the file under a key the
// allowlist rejects, so the loader has read the bytes and must still not put
// them in the error it returns — the failure mode this guards is an error
// like `bad line "SLACK_SECRET=xoxp-…"`, which would land the token in the
// event log the first time a caller logged the error.
func TestEdgeLoadSlackConfigNeverLeaksTheTokenValue(t *testing.T) {
	for _, body := range []string{
		"SLACK_SECRET=" + edgeSecret + "\n",
		"garbage line carrying " + edgeSecret + "\n",
		"export SLACK_USER_TOKEN='" + edgeSecret + "'\nbut then a malformed line\n=\n",
	} {
		edgeSandboxHome(t)
		edgeWriteSlackEnv(t, body)
		_, err := LoadSlackConfig()
		if err != nil && strings.Contains(err.Error(), edgeSecret) {
			t.Fatalf("error carries the token value: %v", err)
		}
	}
	// And the sentinel itself, which every caller prints, must be clean by
	// construction — it is a package-level var built before any file is read.
	if strings.Contains(ErrNoSlack.Error(), "xoxp") {
		t.Fatalf("ErrNoSlack = %q, want a message that names no credential", ErrNoSlack)
	}
}

// --- 1. IrisModel ---------------------------------------------------------

// TestEdgeIrisModelIsAnExactIDNeverAnAlias pins the constant's REFUSAL side:
// the repo's model policy is that an alias silently follows whatever claude
// ships next, so this must be a full id — and it must not have been written
// as one of the Agent-tool tier names.
func TestEdgeIrisModelIsAnExactID(t *testing.T) {
	if IrisModel != "claude-fable-5" {
		t.Fatalf("IrisModel = %q, want the exact id claude-fable-5", IrisModel)
	}
	for _, alias := range []string{"fable", "opus", "sonnet", "haiku", "default", ""} {
		if IrisModel == alias {
			t.Fatalf("IrisModel = %q — an alias, not an id", IrisModel)
		}
	}
	// ModelArgs must put the id on a command line rather than fall through to
	// DefaultModel: an empty/aliased constant would silently spawn iris on Opus.
	args := ModelArgs(IrisModel)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, IrisModel) {
		t.Fatalf("ModelArgs(IrisModel) = %v, want it to name %q", args, IrisModel)
	}
	if strings.Contains(joined, DefaultModel) {
		t.Fatalf("ModelArgs(IrisModel) = %v — fell through to DefaultModel", args)
	}
}

// --- 2. Slack client: every method's refusal path -------------------------

// edgeClientOp is one client method, reduced to "did it error, and did it
// hand back anything". The whole point of the table is that the obligations
// on the contract's last slice-1 row ("all client methods") are swept across
// every method rather than spot-checked on one.
type edgeClientOp struct {
	name string
	// call returns the number of items the method produced (0 for the scalar
	// returns) and its error.
	call func(c *SlackClient) (int, error)
}

var edgeClientOps = []edgeClientOp{
	// The two scalar-returning methods report 0 unconditionally: whether a
	// failed call also hands back a partial value beside its error is not
	// something the contract fixes, so only the error is asserted for them.
	{"AuthTest", func(c *SlackClient) (int, error) {
		_, err := c.AuthTest()
		return 0, err
	}},
	{"UserConversations", func(c *SlackClient) (int, error) {
		v, err := c.UserConversations()
		return len(v), err
	}},
	{"ConversationHistory", func(c *SlackClient) (int, error) {
		v, err := c.ConversationHistory("C1", "1769040000.000100", 200)
		return len(v), err
	}},
	{"ConversationReplies", func(c *SlackClient) (int, error) {
		v, err := c.ConversationReplies("C1", "1769040000.000100", "")
		return len(v), err
	}},
	{"SearchMessages", func(c *SlackClient) (int, error) {
		v, err := c.SearchMessages("<@U1> after:2026-08-01")
		return len(v), err
	}},
	{"UserInfo", func(c *SlackClient) (int, error) {
		_, err := c.UserInfo("U1")
		return 0, err
	}},
}

// edgeSlackServer stands up an httptest.Server whose handler is chosen per
// Slack method name, plus a counter of how many requests it saw (the
// "never retried inside the client" assertion needs it).
func edgeSlackServer(t *testing.T, h http.HandlerFunc) (*httptest.Server, *int64) {
	t.Helper()
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// edgeClient builds a client pointed at srv, per the SlackClient row's
// "tests may point BaseURL at an httptest.Server".
func edgeClient(srv *httptest.Server) *SlackClient {
	c := NewSlackClient(edgeSecret, "")
	c.BaseURL = srv.URL
	return c
}

// edgeEmptyOKBody is the per-method shape of a legitimate, EMPTY success —
// the control every refusal test needs, since "returned nothing" must be
// distinguishable from "failed".
func edgeEmptyOKBody(path string) string {
	switch {
	case strings.Contains(path, "auth.test"):
		return `{"ok":true,"user_id":"U0","team_id":"T0"}`
	case strings.Contains(path, "users.conversations"):
		return `{"ok":true,"channels":[],"response_metadata":{"next_cursor":""}}`
	case strings.Contains(path, "search.messages"):
		return `{"ok":true,"messages":{"matches":[],"pagination":{"next_cursor":""}},"response_metadata":{"next_cursor":""}}`
	case strings.Contains(path, "users.info"):
		return `{"ok":true,"user":{"id":"U1","name":"one","real_name":"One","profile":{"display_name":"one","real_name":"One"}}}`
	default: // conversations.history, conversations.replies
		return `{"ok":true,"messages":[],"has_more":false,"response_metadata":{"next_cursor":""}}`
	}
}

// TestEdgeSlackClientOKFalseIsAnError is the contract's headline refusal:
// Slack answers HTTP 200 with `{"ok":false,"error":"…"}`, so a decoder that
// only checks the status code reads a hard failure as an empty success —
// which the poller would then commit as "no new messages" and advance its
// watermarks past.
func TestEdgeSlackClientOKFalseIsAnError(t *testing.T) {
	for _, slackErr := range []string{"invalid_auth", "ratelimited", "not_in_channel", "missing_scope"} {
		for _, op := range edgeClientOps {
			t.Run(slackErr+"/"+op.name, func(t *testing.T) {
				srv, _ := edgeSlackServer(t, func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprintf(w, `{"ok":false,"error":%q}`, slackErr)
				})
				n, err := op.call(edgeClient(srv))
				if err == nil {
					t.Fatalf("ok:false decoded as a success (%d items)", n)
				}
				if !strings.Contains(err.Error(), slackErr) {
					t.Errorf("err = %v, want it to carry Slack's own error string %q", err, slackErr)
				}
				if n != 0 {
					t.Errorf("a refused call still produced %d item(s)", n)
				}
			})
		}
	}
}

// TestEdgeSlackClientTransportFailureIsAnError: nothing is listening, so
// every method must fail rather than return an empty result set. Same
// consequence as above — an empty success advances a watermark.
func TestEdgeSlackClientTransportFailureIsAnError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // now guaranteed nobody is listening there

	for _, op := range edgeClientOps {
		t.Run(op.name, func(t *testing.T) {
			c := NewSlackClient(edgeSecret, "")
			c.BaseURL = "http://" + addr
			n, err := op.call(c)
			if err == nil {
				t.Fatalf("a dead endpoint decoded as a success (%d items)", n)
			}
			if n != 0 {
				t.Errorf("a failed call still produced %d item(s)", n)
			}
		})
	}
}

// TestEdgeSlackClientMalformedBodyIsAnError covers the third way a 200 can be
// a failure: a proxy or captive portal answering with HTML, or a truncated
// body. Same rule — never an empty success.
func TestEdgeSlackClientMalformedBodyIsAnError(t *testing.T) {
	for _, body := range []string{"<html>captive portal</html>", `{"ok":`, ""} {
		for _, op := range edgeClientOps {
			t.Run(op.name, func(t *testing.T) {
				srv, _ := edgeSlackServer(t, func(w http.ResponseWriter, r *http.Request) {
					w.Write([]byte(body))
				})
				n, err := op.call(edgeClient(srv))
				if err == nil {
					t.Fatalf("body %q decoded as a success (%d items)", body, n)
				}
			})
		}
	}
}

// TestEdgeSlackClientErrorsNeverCarryTheToken pins the credential rule on the
// client half of the row: the token is in every request's Authorization
// header, so an error built by dumping the request (or by http.Client's own
// *url.Error on a redirect) is exactly how it reaches a log.
func TestEdgeSlackClientErrorsNeverCarryTheToken(t *testing.T) {
	srv, _ := edgeSlackServer(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"ok":false,"error":"invalid_auth"}`)
	})
	for _, op := range edgeClientOps {
		if _, err := op.call(edgeClient(srv)); err != nil && strings.Contains(err.Error(), edgeSecret) {
			t.Errorf("%s: error carries the token: %v", op.name, err)
		}
	}

	ln, lerr := net.Listen("tcp", "127.0.0.1:0")
	if lerr != nil {
		t.Fatal(lerr)
	}
	addr := ln.Addr().String()
	ln.Close()
	for _, op := range edgeClientOps {
		c := NewSlackClient(edgeSecret, "")
		c.BaseURL = "http://" + addr
		if _, err := op.call(c); err != nil && strings.Contains(err.Error(), edgeSecret) {
			t.Errorf("%s (transport): error carries the token: %v", op.name, err)
		}
	}
}

// TestEdgeSlackClientEmptyOKIsNotAnError is the control for the three tests
// above: a genuinely empty answer must come back with a nil error, or the
// poller would treat a quiet workspace as an outage.
func TestEdgeSlackClientEmptyOKIsNotAnError(t *testing.T) {
	srv, _ := edgeSlackServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(edgeEmptyOKBody(r.URL.Path)))
	})
	for _, op := range edgeClientOps {
		t.Run(op.name, func(t *testing.T) {
			if _, err := op.call(edgeClient(srv)); err != nil {
				t.Fatalf("empty ok:true answered with an error: %v", err)
			}
		})
	}
}

// TestEdgeSlackRateLimitErrorCarriesRetryAfter pins the 429 row: the seconds
// come off the Retry-After header, the error is reachable with errors.As, and
// — the part with no other alarm on it — the client does NOT retry, since a
// retry inside the client would burn the very budget the poller is pacing.
func TestEdgeSlackRateLimitErrorCarriesRetryAfter(t *testing.T) {
	for _, tc := range []struct {
		header string
		want   time.Duration
	}{
		{"30", 30 * time.Second},
		{"1", 1 * time.Second},
	} {
		for _, op := range edgeClientOps {
			t.Run(tc.header+"/"+op.name, func(t *testing.T) {
				srv, hits := edgeSlackServer(t, func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Retry-After", tc.header)
					w.WriteHeader(http.StatusTooManyRequests)
					w.Write([]byte(`{"ok":false,"error":"ratelimited"}`))
				})
				_, err := op.call(edgeClient(srv))
				if err == nil {
					t.Fatal("429 decoded as a success")
				}
				var rl *SlackRateLimitError
				if !errors.As(err, &rl) {
					t.Fatalf("err = %v (%T), want a *SlackRateLimitError reachable with errors.As", err, err)
				}
				if rl.RetryAfter != tc.want {
					t.Errorf("RetryAfter = %v, want %v", rl.RetryAfter, tc.want)
				}
				if rl.Error() == "" {
					t.Error("SlackRateLimitError.Error() is empty")
				}
				if n := atomic.LoadInt64(hits); n != 1 {
					t.Errorf("server saw %d requests, want exactly 1 — a 429 is not retried inside the client", n)
				}
			})
		}
	}
}

// TestEdgeSlackClientPaginationStopsRefusingOnALaterPage: a first page that
// decodes fine followed by an ok:false second page must be an ERROR, not a
// truncated success. A partial result here reads to the poller as "that is
// all there was", and the watermark it then advances skips the rest forever.
func TestEdgeSlackClientPaginationStopsRefusingOnALaterPage(t *testing.T) {
	var page int64
	srv, _ := edgeSlackServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if atomic.AddInt64(&page, 1) == 1 {
			w.Write([]byte(`{"ok":true,"messages":[{"ts":"1769040000.000100","user":"U9","text":"one"}],` +
				`"has_more":true,"response_metadata":{"next_cursor":"CURSOR2"}}`))
			return
		}
		w.Write([]byte(`{"ok":false,"error":"ratelimited"}`))
	})
	msgs, err := edgeClient(srv).ConversationHistory("C1", "", 200)
	if err == nil {
		t.Fatalf("a failing second page decoded as a %d-message success", len(msgs))
	}
	if !strings.Contains(err.Error(), "ratelimited") {
		t.Errorf("err = %v, want Slack's own error string", err)
	}
}

// TestEdgeNewSlackClientHasATimeout: an HTTPClient with the zero Timeout
// never gives up, and this client runs inside the daemon's tick — one wedged
// connection would stall the pass forever with nothing logged.
func TestEdgeNewSlackClientHasATimeout(t *testing.T) {
	c := NewSlackClient("xoxp-whatever", "")
	if c == nil {
		t.Fatal("NewSlackClient returned nil")
	}
	if c.HTTPClient == nil {
		t.Fatal("HTTPClient is nil — the zero http.Client has no timeout either")
	}
	if c.HTTPClient.Timeout <= 0 {
		t.Fatalf("HTTPClient.Timeout = %v, want a positive bound", c.HTTPClient.Timeout)
	}
	if c.BaseURL != "https://slack.com/api" {
		t.Fatalf("BaseURL = %q, want the documented default", c.BaseURL)
	}
	if c.Token != "xoxp-whatever" {
		t.Fatalf("Token = %q, want the constructor's argument", c.Token)
	}
}

// --- 2. SlackTSLess -------------------------------------------------------

// TestEdgeSlackTSLessNeverGoesThroughAFloat is the assertion the "never
// converts the whole value to a float" clause exists for. Both pairs below
// have integer parts that float64 CANNOT tell apart (17 significant digits,
// past the 15-16 a float64 carries), so a strconv.ParseFloat implementation
// reads them as equal and answers false in both directions — silently
// collapsing two distinct messages onto one watermark.
func TestEdgeSlackTSLessNeverGoesThroughAFloat(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"99999999999999998.000001", "99999999999999999.000001", true},
		{"99999999999999999.000001", "99999999999999998.000001", false},
		{"12345678901234567.000000", "12345678901234568.000000", true},
	}
	for _, c := range cases {
		if got := SlackTSLess(c.a, c.b); got != c.want {
			t.Errorf("SlackTSLess(%q, %q) = %v, want %v — the seconds are being parsed as a float",
				c.a, c.b, got, c.want)
		}
	}
}

// TestEdgeSlackTSLessSameSecondMicroOrdering: within one second the micros
// decide, which is the ordering the exclusive `oldest` floor depends on — two
// messages in the same second are routine in a busy DM.
func TestEdgeSlackTSLessSameSecondMicroOrdering(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"1769040000.000001", "1769040000.000002", true},
		{"1769040000.000002", "1769040000.000001", false},
		{"1769040000.000001", "1769040000.999999", true},
		{"1769040000.999999", "1769040001.000000", true},
		{"1769040001.000000", "1769040000.999999", false},
	}
	for _, c := range cases {
		if got := SlackTSLess(c.a, c.b); got != c.want {
			t.Errorf("SlackTSLess(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// TestEdgeSlackTSLessIsIrreflexiveAndStrict: a numeric comparison is a STRICT
// order, so nothing is less than itself and no pair is less in both
// directions. The equal case is the one the exclusive floor rides on — a
// filter built on `!SlackTSLess(mark, ts)` re-emits every message it has
// already seen the moment equality answers true.
func TestEdgeSlackTSLessIsIrreflexiveAndStrict(t *testing.T) {
	values := []string{
		"1769040000.000100",
		"1769040000.000000",
		"0.000000",
		"", // the zero value a never-swept mark carries
	}
	for _, v := range values {
		if SlackTSLess(v, v) {
			t.Errorf("SlackTSLess(%q, %q) = true — a strict order is irreflexive", v, v)
		}
	}
	for _, a := range values {
		for _, b := range values {
			if a == b {
				continue
			}
			if SlackTSLess(a, b) && SlackTSLess(b, a) {
				t.Errorf("SlackTSLess says %q < %q AND %q < %q", a, b, b, a)
			}
		}
	}
}

// TestEdgeSlackTSLessTolerantOfMissingMicros: `split on "."` means a value
// with no separator is all seconds, which is what a hand-written mark (or a
// legacy row) looks like. It must order by its seconds rather than panic or
// read as zero.
func TestEdgeSlackTSLessTolerantOfMissingMicros(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"1769040000", "1769040001", true},
		{"1769040001", "1769040000", false},
		{"1769040000", "1769040000.000001", true},
		{"1769040000.000001", "1769040000", false},
	}
	for _, c := range cases {
		if got := SlackTSLess(c.a, c.b); got != c.want {
			t.Errorf("SlackTSLess(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
