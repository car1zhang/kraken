package jobs

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"

	"kraken/internal/store"
)

// recallhook_test.go — the transport half of mid-conversation recall:
// `kraken recall-hook`'s body (RunRecallHook) against a stand-in for kraken
// serve's POST /api/recall. These tests were carried over from the
// recall_contract/recall_edge/recall_serial suites when the knowledge graph's
// retrieval half of Recall was removed; what Recall itself still delivers
// (goals, notes) is pinned by the goals and notes suites.
//
// The rule every assertion here serves: a hook that breaks a conversation is a
// bug by definition, so every failure is an untouched output stream — never an
// error, never a panic, never a partial emission.

// storeForRecall opens a scratch store and installs it as the package's active
// store for the test's lifetime.
func storeForRecall(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "kraken.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	UseStore(st)
	t.Cleanup(func() { UseStore(nil); st.Close() })
	return st
}

// recallHookServer stands up a counting httptest server answering every
// request with h, and points ServeBaseURL at it. The counter is what makes
// "no request was made" assertable.
func recallHookServer(t *testing.T, h http.HandlerFunc) *int64 {
	t.Helper()
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("KRAKEN_SERVE_ADDR", strings.TrimPrefix(srv.URL, "http://"))
	return &hits
}

// recallHookOut runs the hook over payload and returns everything it wrote.
func recallHookOut(t *testing.T, payload string) string {
	t.Helper()
	var out bytes.Buffer
	RunRecallHook(strings.NewReader(payload), &out)
	return out.String()
}

func TestRecallHookTimeoutIsFourSeconds(t *testing.T) {
	if recallHookTimeout != 4*time.Second {
		t.Errorf("recallHookTimeout = %v, want 4s", recallHookTimeout)
	}
}

// TestRecallHookPostsAndEmitsTheEventNameVerbatim is the happy path end to
// end: the payload's fields reach POST /api/recall as a RecallRequest (all
// five keys, slug included), and the route's context comes back out as
// additionalContext under the payload's own event name, verbatim. "Stop"
// stands in for any unwired event: the verb stays event-agnostic.
func TestRecallHookPostsAndEmitsTheEventNameVerbatim(t *testing.T) {
	for _, event := range []string{"UserPromptSubmit", "Stop"} {
		t.Run(event, func(t *testing.T) {
			var gotMethod, gotPath string
			var gotBody []byte
			hits := recallHookServer(t, func(w http.ResponseWriter, r *http.Request) {
				gotMethod, gotPath = r.Method, r.URL.Path
				gotBody, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"context":"RECALLED BLOCK"}`)
			})

			payload := `{"hook_event_name":"` + event + `","session_id":"sess-42",` +
				`"prompt":"what did we decide about the throttle",` +
				`"transcript_path":"/tmp/recall-hook.jsonl","stop_hook_active":false}`
			var outBuf bytes.Buffer
			RunRecallHook(strings.NewReader(payload), &outBuf, "the-slug")
			out := outBuf.String()

			if n := atomic.LoadInt64(hits); n != 1 {
				t.Fatalf("server saw %d requests, want exactly 1", n)
			}
			if gotMethod != http.MethodPost || gotPath != "/api/recall" {
				t.Errorf("hook called %s %s, want POST /api/recall", gotMethod, gotPath)
			}
			var req RecallRequest
			if err := json.Unmarshal(gotBody, &req); err != nil {
				t.Fatalf("posted body is not JSON (%v): %q", err, gotBody)
			}
			want := RecallRequest{SessionID: "sess-42", Event: event,
				Prompt: "what did we decide about the throttle", TranscriptPath: "/tmp/recall-hook.jsonl", Slug: "the-slug"}
			if req != want {
				t.Errorf("request body = %+v, want %+v", req, want)
			}

			dec := json.NewDecoder(strings.NewReader(out))
			var emitted map[string]map[string]string
			if err := dec.Decode(&emitted); err != nil {
				t.Fatalf("hook output is not one JSON object (%v): %q", err, out)
			}
			if len(emitted) != 1 {
				t.Errorf("hook output carries keys beside hookSpecificOutput: %#v", emitted)
			}
			spec := emitted["hookSpecificOutput"]
			if spec["hookEventName"] != event {
				t.Errorf("hookEventName = %q, want %q verbatim from the payload", spec["hookEventName"], event)
			}
			if spec["additionalContext"] != "RECALLED BLOCK" {
				t.Errorf("additionalContext = %q, want the server's context", spec["additionalContext"])
			}
			rest, _ := io.ReadAll(dec.Buffered())
			if strings.TrimSpace(string(rest)) != "" {
				t.Errorf("hook wrote trailing output after its object: %q", rest)
			}
		})
	}
}

// TestRecallHookStopHookActiveMakesNoRequest is the loop guard: not merely
// "writes nothing" but "never calls the route at all".
func TestRecallHookStopHookActiveMakesNoRequest(t *testing.T) {
	hits := recallHookServer(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"context":"SHOULD NEVER BE FETCHED"}`)
	})
	out := recallHookOut(t,
		`{"hook_event_name":"Stop","session_id":"sess-loop","stop_hook_active":true,"transcript_path":"/tmp/x.jsonl"}`)
	if n := atomic.LoadInt64(hits); n != 0 {
		t.Errorf("stop_hook_active made %d HTTP requests, want 0", n)
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("stop_hook_active wrote %q, want nothing", out)
	}
}

// TestRecallHookSkipsSubagentFirings: a firing carrying agent_id is an
// in-process subagent's, sharing the parent's session; it must write nothing
// AND make no request, or it would spend the parent's delivery records on a
// context that is discarded when the subagent finishes.
func TestRecallHookSkipsSubagentFirings(t *testing.T) {
	hits := recallHookServer(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"context":"should never be requested"}`)
	})
	out := recallHookOut(t,
		`{"hook_event_name":"UserPromptSubmit","session_id":"parent-sess","agent_id":"a87e4143adcb9957e","prompt":"p"}`)
	if out != "" {
		t.Fatalf("subagent firing wrote %q, want nothing", out)
	}
	if n := atomic.LoadInt64(hits); n != 0 {
		t.Fatalf("subagent firing made %d requests, want 0", n)
	}
}

// TestRecallHookIsSilentOnEveryServerFailure walks the response side of "any
// failure writes nothing".
func TestRecallHookIsSilentOnEveryServerFailure(t *testing.T) {
	for name, h := range map[string]http.HandlerFunc{
		"500 with a valid body": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, `{"context":"do not use me"}`)
		},
		"404": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		},
		"200 with an undecodable body": func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, "not json at all")
		},
		"200 with an empty body": func(w http.ResponseWriter, r *http.Request) {},
		"200 with an empty context": func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, `{"context":""}`)
		},
		"200 with no context key": func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, `{"other":"value"}`)
		},
		"200 with a context of the wrong type": func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, `{"context":42}`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			recallHookServer(t, h)
			out := recallHookOut(t,
				`{"hook_event_name":"UserPromptSubmit","session_id":"s","prompt":"p","transcript_path":"/tmp/t.jsonl"}`)
			if strings.TrimSpace(out) != "" {
				t.Errorf("wrote %q, want nothing", out)
			}
		})
	}
}

// TestRecallHookIsSilentOnAMalformedPayload walks the request side: the hook
// payload schema is Claude Code's and can drift at any release.
func TestRecallHookIsSilentOnAMalformedPayload(t *testing.T) {
	recallHookServer(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"context":"SHOULD NOT BE EMITTED"}`)
	})
	for name, payload := range map[string]string{
		"empty stdin":       ``,
		"truncated object":  `{"hook_event_name":"Stop","session_id":`,
		"not an object":     `[1,2,3]`,
		"a bare string":     `"just a string"`,
		"whitespace only":   "   \n\t\n",
		"nul bytes":         "\x00\x00",
		"wrongly typed ids": `{"hook_event_name":123,"session_id":{"nested":true}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if out := recallHookOut(t, payload); strings.TrimSpace(out) != "" {
				t.Errorf("wrote %q, want nothing", out)
			}
		})
	}
}

// TestRecallHookIsSilentWhenStdinErrors covers the read itself failing.
func TestRecallHookIsSilentWhenStdinErrors(t *testing.T) {
	recallHookServer(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"context":"SHOULD NOT BE EMITTED"}`)
	})
	var out bytes.Buffer
	RunRecallHook(iotest.ErrReader(errors.New("stdin exploded")), &out)
	if strings.TrimSpace(out.String()) != "" {
		t.Errorf("wrote %q, want nothing", out.String())
	}
}

// TestRecallHookIsSilentWhenServeIsUnreachable: a cell whose `kraken serve` is
// down or restarting costs the turn a refused connection and nothing else.
func TestRecallHookIsSilentWhenServeIsUnreachable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // nothing listens there anymore: connections refuse immediately
	t.Setenv("KRAKEN_SERVE_ADDR", addr)

	out := recallHookOut(t,
		`{"hook_event_name":"UserPromptSubmit","session_id":"s","prompt":"p","transcript_path":"/tmp/t.jsonl"}`)
	if strings.TrimSpace(out) != "" {
		t.Errorf("wrote %q against an unreachable serve, want nothing", out)
	}
}
