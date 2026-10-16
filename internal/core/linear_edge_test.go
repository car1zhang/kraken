package core

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kraken/internal/util"
)

// linear_edge_test.go — the EDGE dimension of the Linear client and its
// credential file: boundaries, error paths, refusals, and adversarial inputs.
// Written blind against 50-contract.md's frozen symbols; every identifier
// declared here is prefixed `edge` because the logic author shares this
// package.
//
// Two rules under test dominate this file, and both are about what a failure
// is ALLOWED to look like:
//
//   - all-or-error: a non-200, a GraphQL `errors` payload, or undecodable
//     JSON returns (nil, error) — never a partial slice with a nil error.
//     This is SumTokensDelta's reporting rule applied to a network read: a
//     short answer presented as a complete one is the failure with no alarm
//     on it, because the caller then advances its cursor past issues it
//     never saw.
//   - R12: the API key's VALUE appears in no error string, ever. Only the
//     NAME LINEAR_API_KEY, and only to say it is missing.

// edgeLinearKey is deliberately a distinctive literal: every R12 assertion in
// this file is a strings.Contains for it over a forced error message, so a
// value that could occur incidentally (like "key") would make those
// assertions pass for the wrong reason.
const edgeLinearKey = "lin_api_EDGE_SUPERSECRET_0ff1ce"

// edgeMaxDownload is the contract's 25 MB DownloadFile cap.
const edgeMaxDownload = 25 << 20

// edgeClearLinearEnv sandboxes HOME and makes all three LINEAR_* keys
// genuinely ABSENT (not merely empty) for the duration of one test.
// LoadLinearConfig reads the real process env by contract, so a developer's
// own exported key would otherwise decide these tests' outcomes. t.Setenv
// registers the restore of the original value; the Unsetenv after it is what
// makes the key absent rather than present-and-empty.
func edgeClearLinearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"LINEAR_API_KEY", "LINEAR_DEFAULT_REPO", "LINEAR_STATES"} {
		t.Setenv(k, "")
		if err := os.Unsetenv(k); err != nil {
			t.Fatalf("unset %s: %v", k, err)
		}
	}
	t.Setenv("HOME", t.TempDir())
}

func edgeWriteLinearEnvFile(t *testing.T, body string) {
	t.Helper()
	dir := filepath.Join(util.Home(), ".kraken")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "linear.env"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// --- the credential file ---

// LinearEnvPath must derive from util.Home() like every other path in the
// binary — a hardcoded /Users/<name> works on exactly one machine.
func TestEdgeLinearEnvPathIsHomeRelative(t *testing.T) {
	edgeClearLinearEnv(t)
	want := filepath.Join(util.Home(), ".kraken", "linear.env")
	if got := LinearEnvPath(); got != want {
		t.Fatalf("LinearEnvPath() = %q, want %q", got, want)
	}
}

// An absent file is the normal state of a fresh install: the env-only view,
// never an error, and the defaults still filled in. An empty APIKey is how
// "the feature is off" is spelled — there is no error value for it.
func TestEdgeLoadLinearConfigMissingFileIsEnvOnly(t *testing.T) {
	edgeClearLinearEnv(t) // no ~/.kraken at all under the sandboxed HOME

	cfg := LoadLinearConfig()

	if cfg.APIKey != "" {
		t.Errorf("APIKey = %q with nothing configured, want empty (feature off)", cfg.APIKey)
	}
	if cfg.DefaultRepo != "" {
		t.Errorf("DefaultRepo = %q, want empty (no hardcoded default)", cfg.DefaultRepo)
	}
	if len(cfg.States) != 1 || cfg.States[0] != "Today" {
		t.Errorf("States = %#v, want [Today]", cfg.States)
	}

	// and the env alone carries it when the file never appears
	t.Setenv("LINEAR_API_KEY", edgeLinearKey)
	if got := LoadLinearConfig().APIKey; got != edgeLinearKey {
		t.Errorf("APIKey = %q with only the env set, want the env value", got)
	}
}

// A malformed file degrades to the env-only view. Every line below is a shape
// a hand-edited 0600 file can really take; not one of them may error, panic,
// or take a neighbouring good line down with it.
func TestEdgeLoadLinearConfigMalformedFileDegrades(t *testing.T) {
	edgeClearLinearEnv(t)
	edgeWriteLinearEnvFile(t, strings.Join([]string{
		"",
		"# a comment",
		"    ",
		"no-equals-sign-at-all",
		"=",
		"=orphan-value",
		// a key with no value at all
		"LINEAR_API_KEY",
		// padding around both halves
		"  LINEAR_STATES = Today  ",
		// quoted value
		"LINEAR_DEFAULT_REPO=\"kraken\"",
		"export LINEAR_API_KEY='" + edgeLinearKey + "'",
		"LINEAR_UNKNOWN_KEY=whatever",
		"SOME=thing=with=equals",
		// an absurd line
		strings.Repeat("x", 4096) + "=y",
	}, "\n"))

	cfg := LoadLinearConfig()

	if cfg.APIKey != edgeLinearKey {
		t.Errorf("APIKey = %q, want the value from the last good line — a malformed neighbour must not eat it", cfg.APIKey)
	}
	if cfg.DefaultRepo != "kraken" {
		t.Errorf("DefaultRepo = %q, want kraken (quotes stripped)", cfg.DefaultRepo)
	}
	if len(cfg.States) != 1 || cfg.States[0] != "Today" {
		t.Errorf("States = %#v, want [Today]", cfg.States)
	}
}

// A file that is not text at all (binary, truncated mid-write) is still not an
// error — it is the env-only view.
func TestEdgeLoadLinearConfigBinaryFileDegrades(t *testing.T) {
	edgeClearLinearEnv(t)
	edgeWriteLinearEnvFile(t, string([]byte{0x00, 0xff, 0xfe, 0x01, 0x02, '\n', 0x00}))
	t.Setenv("LINEAR_API_KEY", edgeLinearKey)

	cfg := LoadLinearConfig()

	if cfg.APIKey != edgeLinearKey {
		t.Errorf("APIKey = %q, want the process env's value — a binary file must degrade, not win", cfg.APIKey)
	}
	if cfg.DefaultRepo != "" || len(cfg.States) == 0 {
		t.Errorf("defaults lost on a binary file: %+v", cfg)
	}
}

// Process env wins over the file, for all three keys. The launchd reason the
// file exists at all does not make it authoritative — an explicit export in a
// terminal must be able to override it.
func TestEdgeLoadLinearConfigProcessEnvWinsOverFile(t *testing.T) {
	edgeClearLinearEnv(t)
	edgeWriteLinearEnvFile(t, "LINEAR_API_KEY=file-key\nLINEAR_DEFAULT_REPO=file-repo\nLINEAR_STATES=FileState\n")
	t.Setenv("LINEAR_API_KEY", edgeLinearKey)
	t.Setenv("LINEAR_DEFAULT_REPO", "env-repo")
	t.Setenv("LINEAR_STATES", "EnvState")

	cfg := LoadLinearConfig()

	if cfg.APIKey != edgeLinearKey {
		t.Errorf("APIKey = %q, want the process env's", cfg.APIKey)
	}
	if cfg.DefaultRepo != "env-repo" {
		t.Errorf("DefaultRepo = %q, want env-repo", cfg.DefaultRepo)
	}
	if len(cfg.States) != 1 || cfg.States[0] != "EnvState" {
		t.Errorf("States = %#v, want [EnvState]", cfg.States)
	}
}

// LINEAR_STATES is a comma-separated list whose entries are trimmed and whose
// empties are dropped. The empty entry is the load-bearing one: a "" state
// would match an issue whose StateName is empty, which is every issue Linear
// ever fails to describe.
func TestEdgeLoadLinearConfigStatesEdges(t *testing.T) {
	edgeClearLinearEnv(t)

	for _, tc := range []struct {
		name, raw string
		want      []string
	}{
		{"padded entries", "  Today ,  In Progress  ", []string{"Today", "In Progress"}},
		{"trailing comma", "Today,", []string{"Today"}},
		{"leading comma", ",Today", []string{"Today"}},
		{"interior empties", "Today,,,Backlog", []string{"Today", "Backlog"}},
		{"tabs and newlines", "\tToday\t,\nBacklog\n", []string{"Today", "Backlog"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("LINEAR_STATES", tc.raw)
			got := LoadLinearConfig().States
			if len(got) != len(tc.want) {
				t.Fatalf("States(%q) = %#v, want %#v", tc.raw, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("States(%q) = %#v, want %#v", tc.raw, got, tc.want)
				}
			}
		})
	}

	// A10: a value that trims to nothing is an explicit EMPTY configuration —
	// nil States, no state matches, no job is created. It is not an empty
	// entry (which would match an issue with no state at all) and it is not
	// the ["Today"] default (which would ignore what the operator wrote).
	t.Setenv("LINEAR_STATES", " , ,, ")
	if got := LoadLinearConfig().States; len(got) != 0 {
		t.Fatalf("States = %#v for a separators-only value, want nil (A10: an explicit empty configuration is honoured)", got)
	}

	// ...whereas the empty string is ABSENT, and absent takes the default.
	t.Setenv("LINEAR_STATES", "")
	if got := LoadLinearConfig().States; len(got) != 1 || got[0] != "Today" {
		t.Fatalf("States = %#v for an empty value, want [Today] (A10: the default fires when the resolved value is absent or empty)", got)
	}
}

// R12, at the config layer: nothing but APIKey may carry the key's value. A
// parser that assigned the whole line to DefaultRepo (or left the key in a
// States entry) would put it on the metadata line of every job body.
func TestEdgeLoadLinearConfigNeverLeaksKeyIntoOtherFields(t *testing.T) {
	edgeClearLinearEnv(t)
	edgeWriteLinearEnvFile(t, "LINEAR_API_KEY="+edgeLinearKey+"\n")

	cfg := LoadLinearConfig()

	if strings.Contains(cfg.DefaultRepo, edgeLinearKey) {
		t.Errorf("DefaultRepo carries the API key: %q", cfg.DefaultRepo)
	}
	for _, s := range cfg.States {
		if strings.Contains(s, edgeLinearKey) {
			t.Errorf("States carries the API key: %#v", cfg.States)
		}
	}
}

// --- the client's error posture ---

// edgeLinearClient points a client at a scripted server.
func edgeLinearClient(t *testing.T, h http.HandlerFunc) *LinearClient {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := NewLinearClient(LinearConfig{APIKey: edgeLinearKey, DefaultRepo: "kraken", States: []string{"Today"}})
	if c == nil {
		t.Fatal("NewLinearClient returned nil")
	}
	c.BaseURL = srv.URL
	return c
}

// edgeIssueNode is one plausible Linear issue node. The GraphQL response
// SHAPE is not frozen by the contract (see the report's gaps section); every
// assertion in this file that matters is shape-independent — a body that
// cannot be decoded into whatever shape the client expects is still required
// to error rather than come back empty with a nil error.
func edgeIssueNode(id, ident, updated string) string {
	return fmt.Sprintf(`{"id":%q,"identifier":%q,"title":"t","description":"d",`+
		`"url":"https://linear.app/edge/issue/%s","priority":2,`+
		`"createdAt":"2026-08-01T00:00:00Z","updatedAt":%q,`+
		`"state":{"name":"Today","type":"started"},"team":{"key":"ENG"},`+
		`"project":{"name":"P"},"labels":{"nodes":[{"name":"bug"}]},`+
		`"comments":{"nodes":[]}}`, id, ident, ident, updated)
}

func edgeIssuesBody(nodes string, hasNext bool, cursor string) string {
	return fmt.Sprintf(`{"data":{"issues":{"nodes":[%s],"pageInfo":{"hasNextPage":%v,"endCursor":%q}}}}`,
		nodes, hasNext, cursor)
}

// Every non-200 is an error and NOTHING is returned alongside it. 429 is
// listed explicitly: the contract says the client never retries in a loop, so
// a rate limit has to surface as an error the caller's throttle absorbs.
func TestEdgeLinearClientNon200IsErrorNotPartial(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden,
		http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			c := edgeLinearClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(code)
				// a real page of data alongside the failure status: the client
				// must not salvage it
				fmt.Fprint(w, edgeIssuesBody(edgeIssueNode("i1", "ENG-1", "2026-08-02T00:00:00Z"), false, ""))
			})

			issues, err := c.AssignedIssuesSince(time.Unix(0, 0))
			if err == nil {
				t.Fatalf("HTTP %d returned nil error with %d issues — a non-200 is never a result", code, len(issues))
			}
			if issues != nil {
				t.Errorf("HTTP %d returned %d issues alongside its error, want nil", code, len(issues))
			}

			comments, cerr := c.AssignedCommentsSince(time.Unix(0, 0))
			if cerr == nil || comments != nil {
				t.Errorf("AssignedCommentsSince(HTTP %d) = (%v, %v), want (nil, error)", code, comments, cerr)
			}
		})
	}
}

// GraphQL's own failure mode: HTTP 200 carrying BOTH data and an `errors`
// array. This is the partial-as-complete trap in its purest form — the
// response looks successful at the transport layer and holds real nodes, and
// the missing ones are exactly the ones the caller will never look for again.
func TestEdgeLinearClientGraphQLErrorsPayloadIsError(t *testing.T) {
	for _, body := range []string{
		// data + errors together
		`{"data":{"issues":{"nodes":[` + edgeIssueNode("i1", "ENG-1", "2026-08-02T00:00:00Z") +
			`],"pageInfo":{"hasNextPage":false,"endCursor":""}}},"errors":[{"message":"complexity limit"}]}`,
		// errors with a null data field (the total-failure spelling)
		`{"data":null,"errors":[{"message":"authentication failed"}]}`,
		// an errors array whose entries are not the shape anyone expects
		`{"data":{"issues":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}},"errors":["plain string"]}`,
	} {
		c := edgeLinearClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, body)
		})

		issues, err := c.AssignedIssuesSince(time.Unix(0, 0))
		if err == nil {
			t.Errorf("200 with a GraphQL errors payload returned nil error and %d issues:\n%s", len(issues), body)
		}
		if issues != nil {
			t.Errorf("200 with a GraphQL errors payload returned %d issues alongside its error:\n%s", len(issues), body)
		}
	}
}

// Undecodable JSON is an error, not an empty result. The distinction is
// invisible in the return value of a lenient decoder — nil error, zero issues
// — and it is the difference between "nothing changed" and "I could not see".
func TestEdgeLinearClientUndecodableJSONIsError(t *testing.T) {
	for _, body := range []string{
		"",
		"not json at all",
		"{",
		`{"data":`,
		`{"data":{"issues":{"nodes":[{`,
		"<html><body>502 Bad Gateway</body></html>",
		`{"data":{"issues":{"nodes":"a string where an array belongs"}}}`,
	} {
		c := edgeLinearClient(t, func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, body)
		})

		issues, err := c.AssignedIssuesSince(time.Unix(0, 0))
		if err == nil {
			t.Errorf("body %q decoded to %d issues with a nil error — undecodable JSON is an error", body, len(issues))
		}
		if issues != nil {
			t.Errorf("body %q returned %d issues alongside its error", body, len(issues))
		}
	}
}

// A timestamp that is not RFC3339 must fail the whole response rather than
// yield an issue whose UpdatedAt silently reads as the zero time — which the
// pass would then compare against a cursor and treat as ancient, i.e. drop.
func TestEdgeLinearClientRejectsUnparseableTimestamp(t *testing.T) {
	node := `{"id":"i1","identifier":"ENG-1","title":"t","description":"d",` +
		`"url":"https://linear.app/x","priority":2,` +
		`"createdAt":"2026-08-01T00:00:00Z","updatedAt":"yesterday afternoon",` +
		`"state":{"name":"Today","type":"started"},"team":{"key":"ENG"},` +
		`"project":{"name":"P"},"labels":{"nodes":[]},"comments":{"nodes":[]}}`
	c := edgeLinearClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, edgeIssuesBody(node, false, ""))
	})

	issues, err := c.AssignedIssuesSince(time.Unix(0, 0))

	if err == nil {
		t.Fatalf("an unparseable updatedAt decoded cleanly into %d issues — a zero UpdatedAt reads as ancient and is dropped", len(issues))
	}
	if issues != nil {
		t.Errorf("returned %d issues alongside the decode error", len(issues))
	}
}

// A8 froze the author's provenance: `user { displayName }` with a
// `botActor { name }` fallback. Both halves are NULLABLE on the wire — a
// comment written by an integration has no user, and a comment whose author
// was deactivated can have neither — so the decode has to tolerate every
// combination rather than dereference whichever one it expected.
func TestEdgeLinearClientCommentAuthorFallsBackAndToleratesNulls(t *testing.T) {
	comment := func(id, user, bot string) string {
		u, b := "null", "null"
		if user != "" {
			u = fmt.Sprintf(`{"displayName":%q}`, user)
		}
		if bot != "" {
			b = fmt.Sprintf(`{"name":%q}`, bot)
		}
		return fmt.Sprintf(`{"id":%q,"body":"a comment","createdAt":"2026-08-01T00:00:00Z",`+
			`"updatedAt":"2026-08-02T00:00:00Z","user":%s,"botActor":%s}`, id, u, b)
	}
	node := `{"id":"i1","identifier":"ENG-1","title":"t","description":"d",` +
		`"url":"https://linear.app/x","priority":0,` +
		`"createdAt":"2026-08-01T00:00:00Z","updatedAt":"2026-08-02T00:00:00Z",` +
		`"state":{"name":"Today","type":"started"},"team":{"key":"ENG"},` +
		`"project":{"name":"P"},"labels":{"nodes":[]},"comments":{"nodes":[` +
		strings.Join([]string{
			comment("c1", "Carl Zhang", ""),       // a human
			comment("c2", "", "GitHub"),           // a bot: user is null
			comment("c3", "Carl Zhang", "GitHub"), // both: the user wins
			comment("c4", "", ""),                 // neither: no author at all
		}, ",") + `]}}`

	c := edgeLinearClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, edgeIssuesBody(node, false, ""))
	})

	issues, err := c.AssignedIssuesSince(time.Unix(0, 0))

	if err != nil {
		t.Fatalf("a comment with a null user/botActor failed the whole decode: %v", err)
	}
	if len(issues) != 1 || len(issues[0].Comments) != 4 {
		t.Fatalf("got %d issues; comments = %d, want 1 issue with 4 comments", len(issues), len(issues[0].Comments))
	}
	got := issues[0].Comments
	for i, want := range []string{"Carl Zhang", "GitHub", "Carl Zhang", ""} {
		if got[i].Author != want {
			t.Errorf("comment %d author = %q, want %q (A8: user.displayName, falling back to botActor.name)", i+1, got[i].Author, want)
		}
	}
}

// R12 at the client layer. Three routes an error can take out of this
// package, all forced here: a non-200 whose body the client may quote, a
// transport failure (nothing listening), and a HOSTILE server that echoes the
// Authorization header straight back in its body. R12 is written without
// exceptions — "no error message" — so the echo case is included on purpose:
// the key is a bearer credential for a private issue tracker, and a log line
// is forever.
func TestEdgeLinearClientErrorsNeverCarryTheKey(t *testing.T) {
	t.Run("non-200 body", func(t *testing.T) {
		c := edgeLinearClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"errors":[{"message":"invalid credentials"}]}`)
		})
		_, err := c.AssignedIssuesSince(time.Unix(0, 0))
		edgeAssertNoKey(t, err)
	})

	t.Run("transport failure", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		url := srv.URL
		srv.Close() // nothing is listening now
		c := NewLinearClient(LinearConfig{APIKey: edgeLinearKey})
		c.BaseURL = url
		_, err := c.AssignedIssuesSince(time.Unix(0, 0))
		if err == nil {
			t.Fatal("a dead endpoint returned a nil error")
		}
		edgeAssertNoKey(t, err)
	})

	t.Run("server echoes the Authorization header", func(t *testing.T) {
		c := edgeLinearClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, "your token was %s and it is wrong", r.Header.Get("Authorization"))
		})
		_, err := c.AssignedIssuesSince(time.Unix(0, 0))
		edgeAssertNoKey(t, err)
	})
}

func edgeAssertNoKey(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("want an error to inspect, got nil")
	}
	if strings.Contains(err.Error(), edgeLinearKey) {
		t.Fatalf("the API key's VALUE reached an error message (R12): %v", err)
	}
}

// A page that claims another page but hands back no way to REACH it — an
// empty `endCursor` — is a dead end: following it re-requests page 1 forever,
// holding the daemon's goroutine and burning the 2,500 req/h budget. The
// client has to stop.
//
// A sibling case, a server that repeats the SAME non-empty cursor forever,
// was dropped after round 2 arbitration: the contract says only "relay
// pagination followed to exhaustion" and states no bound on page count, so
// asserting one is inventing a requirement. It is named as a contract silence
// in the round-2 report — the remedy, if the lead wants one, is a page cap in
// the contract rather than a test.
func TestEdgeLinearClientStopsOnAnEmptyCursor(t *testing.T) {
	var calls int
	c := edgeLinearClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls > 25 {
			// a backstop so a looping client fails this test instead of
			// hanging the suite
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, edgeIssuesBody(edgeIssueNode("i1", "ENG-1", "2026-08-02T00:00:00Z"), true, ""))
	})

	_, _ = c.AssignedIssuesSince(time.Unix(0, 0))

	if calls > 3 {
		t.Fatalf("client issued %d requests against an empty endCursor — that is an unbounded loop", calls)
	}
}

// --- DownloadFile ---

func edgeDownloadClient(t *testing.T, h http.HandlerFunc) (*LinearClient, string) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := NewLinearClient(LinearConfig{APIKey: edgeLinearKey})
	return c, srv.URL + "/uploads/file.png"
}

// Non-200 is an error, and the bytes that came with it are not a file. The
// specific failure this guards is a 0-byte or HTML-error file landing in the
// uploads dir and rendering as a broken [image: ...] line forever.
func TestEdgeDownloadFileNon200IsError(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusNotFound,
		http.StatusForbidden, http.StatusInternalServerError} {
		c, url := edgeDownloadClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(code)
			fmt.Fprint(w, "<html>expired signature</html>")
		})

		body, ctype, err := c.DownloadFile(url)

		if err == nil {
			t.Errorf("HTTP %d returned nil error with %d bytes (%s)", code, len(body), ctype)
		}
		if len(body) != 0 {
			t.Errorf("HTTP %d returned %d bytes alongside its error — an error page is not a file", code, len(body))
		}
	}
}

// The 25 MB cap REFUSES, it does not truncate. A truncated image written to
// the uploads dir is a corrupt file that reads as a successful download
// everywhere downstream.
func TestEdgeDownloadFileCapBoundary(t *testing.T) {
	serve := func(t *testing.T, n int) (*LinearClient, string) {
		return edgeDownloadClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(bytes.Repeat([]byte("a"), n))
		})
	}

	t.Run("one byte over the cap is refused", func(t *testing.T) {
		c, url := serve(t, edgeMaxDownload+1)
		body, _, err := c.DownloadFile(url)
		if err == nil {
			t.Fatalf("a %d-byte body returned nil error with %d bytes — the cap must refuse, not truncate", edgeMaxDownload+1, len(body))
		}
		if len(body) != 0 {
			t.Errorf("returned %d bytes alongside the oversize error — that is a truncated file", len(body))
		}
	})

	t.Run("exactly the cap is allowed", func(t *testing.T) {
		c, url := serve(t, edgeMaxDownload)
		body, ctype, err := c.DownloadFile(url)
		if err != nil {
			t.Fatalf("a body of exactly the %d-byte cap errored: %v — the contract refuses a LARGER body", edgeMaxDownload, err)
		}
		if len(body) != edgeMaxDownload {
			t.Errorf("got %d bytes, want the whole %d", len(body), edgeMaxDownload)
		}
		if ctype == "" {
			t.Error("Content-Type came back empty")
		}
	})

	t.Run("a small body is untouched", func(t *testing.T) {
		c, url := serve(t, 11)
		body, _, err := c.DownloadFile(url)
		if err != nil || len(body) != 11 {
			t.Fatalf("small download = (%d bytes, %v), want (11, nil)", len(body), err)
		}
	})
}

// R12 on the download path too. DownloadFile attaches no Authorization
// header by contract, so the key has no business being anywhere near these
// errors — which is exactly why a leak here would go unnoticed.
func TestEdgeDownloadFileErrorsNeverCarryTheKey(t *testing.T) {
	c, url := edgeDownloadClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, "signature expired")
	})
	_, _, err := c.DownloadFile(url)
	edgeAssertNoKey(t, err)

	// and a URL that cannot be dialled at all
	_, _, err = c.DownloadFile("http://127.0.0.1:1/never-listening")
	if err == nil {
		t.Fatal("a dead host returned a nil error")
	}
	edgeAssertNoKey(t, err)

	// and a URL that is not a URL
	_, _, err = c.DownloadFile("::not a url::")
	if err == nil {
		t.Fatal("a malformed URL returned a nil error")
	}
	edgeAssertNoKey(t, err)
}
