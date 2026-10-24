package jobs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"kraken/internal/core"
)

// slack_log_test.go — how a pass's failed conversation reads are logged. A
// network outage must cost one error line for the conversation reads, not one
// per conversation the sweep's rotation happens to reach while it lasts, and
// a single conversation failing beside successful reads must keep its own
// per-conversation line.

// slackLogTransportErr is a Do failure as core.SlackClient wraps it: the
// *url.Error survives the %w, with a net-shaped cause inside it.
func slackLogTransportErr(method string) error {
	return fmt.Errorf("slack %s call: %w", method, &url.Error{
		Op:  "Post",
		URL: "https://slack.example/api/" + method,
		Err: errors.New("dial tcp: lookup slack.example: no such host"),
	})
}

// slackLogReset empties slackLogOnce's dedup set for one test and restores it
// after, so a test reads only the keys its own calls armed.
func slackLogReset(t *testing.T) {
	t.Helper()
	slackMem.Lock()
	saved := slackMem.logged
	slackMem.logged = nil
	slackMem.Unlock()
	t.Cleanup(func() {
		slackMem.Lock()
		slackMem.logged = saved
		slackMem.Unlock()
	})
}

// slackLoggedKeys returns a copy of the armed slackLogOnce keys.
func slackLoggedKeys() map[string]bool {
	slackMem.Lock()
	defer slackMem.Unlock()
	out := map[string]bool{}
	for k := range slackMem.logged {
		out[k] = true
	}
	return out
}

func TestSlackTransportFailureClassifiesTheNetworkNotSlack(t *testing.T) {
	transport := map[string]error{
		"dns miss":  slackLogTransportErr("conversations.history"),
		"deadline":  fmt.Errorf("read: %w", context.DeadlineExceeded),
		"net error": fmt.Errorf("slack conversations.history read: %w", &slackTimeoutErr{}),
		"bad base":  fmt.Errorf("slack conversations.history request: %w", &url.Error{Op: "parse", URL: "::bad", Err: errors.New("missing protocol scheme")}),
	}
	for name, err := range transport {
		if !slackTransportFailure(err) {
			t.Errorf("%s: read as Slack's answer, want transport", name)
		}
	}
	answers := map[string]error{
		"api error":   errors.New("slack conversations.history: fatal_error"),
		"non-200":     errors.New("slack conversations.history: 502 Bad Gateway: <html>"),
		"rate limit":  &core.SlackRateLimitError{},
		"plain error": errors.New("something else"),
	}
	for name, err := range answers {
		if slackTransportFailure(err) {
			t.Errorf("%s: read as transport, want Slack's answer", name)
		}
	}
}

// slackTimeoutErr is a net.Error with no *url.Error around it — the shape a
// mid-body read failure takes through the client's "read: %w" wrap.
type slackTimeoutErr struct{}

func (*slackTimeoutErr) Error() string   { return "read tcp: i/o timeout" }
func (*slackTimeoutErr) Timeout() bool   { return true }
func (*slackTimeoutErr) Temporary() bool { return true }

func TestSlackLogTransportOutageIsOneLineUntilAPassReads(t *testing.T) {
	slackLogReset(t)
	err := slackLogTransportErr("conversations.history")
	outage := []slackReadFail{{"C1", err}, {"C2", err}, {"C3", err}}
	// Two passes of an outage, a fresh dozen each: one key, armed once.
	slackLogTransport(outage, false)
	slackLogTransport([]slackReadFail{{"C4", err}, {"C5", err}}, false)
	if got := slackLoggedKeys(); len(got) != 1 || !got[slackTransportKey] {
		t.Fatalf("two outage passes armed %v, want exactly {%s}", got, slackTransportKey)
	}
	// A pass that reads nothing and fails nothing (budget spent, empty
	// inventory) leaves the key armed: no evidence the network is back.
	slackLogTransport(nil, false)
	if got := slackLoggedKeys(); !got[slackTransportKey] {
		t.Fatal("an empty pass disarmed the transport key")
	}
	// The first pass that reads anything re-arms it, so a second outage is
	// reported rather than swallowed.
	slackLogTransport(nil, true)
	if got := slackLoggedKeys(); len(got) != 0 {
		t.Fatalf("a successful pass left %v armed", got)
	}
}

func TestSlackLogTransportPartialPassKeepsThePerConversationKey(t *testing.T) {
	slackLogReset(t)
	err := slackLogTransportErr("conversations.history")
	// One conversation timing out while the rest of the pass reads is that
	// conversation's own failure: logged under its own key, once, and the
	// shared key is not armed.
	slackLogTransport([]slackReadFail{{"C1", err}}, true)
	slackLogTransport([]slackReadFail{{"C1", err}}, true)
	got := slackLoggedKeys()
	if !got["history:C1"] || got[slackTransportKey] || len(got) != 1 {
		t.Fatalf("partial pass armed %v, want exactly {history:C1}", got)
	}
	// A partial pass with a lone failure also re-arms a transport key an
	// earlier outage left.
	slackLogTransport([]slackReadFail{{"C2", err}}, false)
	slackLogTransport([]slackReadFail{{"C3", err}}, true)
	got = slackLoggedKeys()
	if got[slackTransportKey] || !got["history:C3"] {
		t.Fatalf("after an outage then a partial pass: %v, want transport cleared and history:C3 armed", got)
	}
}

func TestSlackLogTransportOutageEdgesAreTheOutageNotTheConversations(t *testing.T) {
	slackLogReset(t)
	err := slackLogTransportErr("conversations.history")
	// The pass an outage begins in: early reads succeeded, the rest failed.
	slackLogTransport([]slackReadFail{{"C4", err}, {"C5", err}, {"C6", err}}, true)
	got := slackLoggedKeys()
	if len(got) != 1 || !got[slackTransportKey] {
		t.Fatalf("an outage beginning mid-pass armed %v, want exactly {%s}", got, slackTransportKey)
	}
	// The pass it ends in: early reads failed, a later one succeeded — the
	// key stays armed, since the network was not clean for the whole pass.
	slackLogTransport([]slackReadFail{{"C1", err}, {"C2", err}}, true)
	if got := slackLoggedKeys(); len(got) != 1 || !got[slackTransportKey] {
		t.Fatalf("an outage ending mid-pass left %v, want the transport key still armed alone", got)
	}
	// The first clean pass re-arms it.
	slackLogTransport(nil, true)
	if got := slackLoggedKeys(); len(got) != 0 {
		t.Fatalf("a clean pass left %v armed", got)
	}
}

// slackSweepClient points the client at a server standing in for Slack and
// seeds the conversation inventory so the sweep makes no users.conversations
// call; the sweep's own rotation clock is reset so every pass visits every
// seeded conversation.
func slackSweepClient(t *testing.T, base string, ids ...string) *core.SlackClient {
	t.Helper()
	slackMem.Lock()
	savedInv, savedAt, savedSwept := slackMem.inventory, slackMem.inventoryAt, slackMem.lastSwept
	slackMem.inventory = nil
	for _, id := range ids {
		slackMem.inventory = append(slackMem.inventory, core.SlackConversation{ID: id, IsIM: true})
	}
	slackMem.inventoryAt = time.Now()
	slackMem.lastSwept = nil
	slackMem.Unlock()
	t.Cleanup(func() {
		slackMem.Lock()
		slackMem.inventory, slackMem.inventoryAt, slackMem.lastSwept = savedInv, savedAt, savedSwept
		slackMem.Unlock()
	})
	return &core.SlackClient{Token: "xoxp-test", BaseURL: base, HTTPClient: &http.Client{Timeout: 2 * time.Second}}
}

// TestSlackSweepWiresTheTransportKey drives slackSweepConversations itself,
// so the clear and arm sites inside the sweep are what is under test rather
// than slackLogTransport alone: a server that is down arms the shared key,
// and the first pass against a server that answers disarms it.
func TestSlackSweepWiresTheTransportKey(t *testing.T) {
	slackLogReset(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"ok":true,"messages":[]}`)
	}))
	base := srv.URL
	srv.Close() // pass 1: the network is gone
	client := slackSweepClient(t, base, "C1", "C2")
	now := time.Now()
	if err := slackSweepConversations(client, map[string]string{}, "U1", newSlackBatch(now), now); err != nil {
		t.Fatalf("outage pass returned %v, want nil (an outage skips, it does not halt)", err)
	}
	got := slackLoggedKeys()
	if len(got) != 1 || !got[slackTransportKey] {
		t.Fatalf("outage pass armed %v, want exactly {%s}", got, slackTransportKey)
	}
	// pass 2: the network is back, on a fresh server at a new address.
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"ok":true,"messages":[]}`)
	}))
	defer srv2.Close()
	client.BaseURL = srv2.URL
	now = now.Add(time.Minute)
	if err := slackSweepConversations(client, map[string]string{}, "U1", newSlackBatch(now), now); err != nil {
		t.Fatalf("recovered pass returned %v", err)
	}
	if got := slackLoggedKeys(); len(got) != 0 {
		t.Fatalf("recovered pass left %v armed", got)
	}
}

// TestSlackSweepOneConversationFailingKeepsItsOwnKey: a transport failure on
// one conversation while its siblings read is filed under that conversation,
// with the shared key untouched — the partial-pass arm, through the sweep.
func TestSlackSweepOneConversationFailingKeepsItsOwnKey(t *testing.T) {
	slackLogReset(t)
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"ok":true,"messages":[]}`)
	}))
	defer good.Close()
	client := slackSweepClient(t, good.URL, "C1", "C2")
	client.HTTPClient = &http.Client{Transport: slackFailChannelTransport{channel: "C2", next: http.DefaultTransport}}
	now := time.Now()
	if err := slackSweepConversations(client, map[string]string{}, "U1", newSlackBatch(now), now); err != nil {
		t.Fatalf("partial pass returned %v", err)
	}
	got := slackLoggedKeys()
	if len(got) != 1 || !got["history:C2"] {
		t.Fatalf("partial pass armed %v, want exactly {history:C2}", got)
	}
}

// slackFailChannelTransport fails every request whose form names one channel at
// the transport (the error Do wraps in a *url.Error) and passes the rest on.
type slackFailChannelTransport struct {
	channel string
	next    http.RoundTripper
}

func (f slackFailChannelTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	if strings.Contains(string(body), "channel="+f.channel) {
		return nil, &slackTimeoutErr{}
	}
	return f.next.RoundTrip(r)
}
