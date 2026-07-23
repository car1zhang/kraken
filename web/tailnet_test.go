package web

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

// TestServeTailnetWhenUpBindsAfterRetry covers the launchd-before-tailscale
// ordering: detection fails at first, the goroutine keeps retrying without
// touching anything else, and once an IP appears the same handler answers
// on the new listener.
func TestServeTailnetWhenUpBindsAfterRetry(t *testing.T) {
	origDetect, origInterval := detectTailscaleIP, tailnetRetryInterval
	t.Cleanup(func() { detectTailscaleIP, tailnetRetryInterval = origDetect, origInterval })
	tailnetRetryInterval = 5 * time.Millisecond

	// pick a free port on loopback to stand in for the "tailnet" bind —
	// the goroutine only cares that detection hands it a bindable IP.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := fmt.Sprintf("%d", probe.Addr().(*net.TCPAddr).Port)
	probe.Close()

	var calls atomic.Int32
	detectTailscaleIP = func() (string, error) {
		if calls.Add(1) < 3 {
			return "", fmt.Errorf("no tailscale interface yet")
		}
		return "127.0.0.1", nil
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "pong")
	})
	go serveTailnetWhenUp("127.0.0.1:1", port, mux) // primary addr differs, so no self-skip

	deadline := time.Now().Add(3 * time.Second)
	for {
		resp, err := http.Get("http://127.0.0.1:" + port + "/ping")
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if string(body) != "pong" {
				t.Fatalf("body = %q, want pong", body)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("tailnet listener never came up: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if calls.Load() < 3 {
		t.Fatalf("detection calls = %d, want ≥3 (retry path exercised)", calls.Load())
	}
}

// TestServeTailnetWhenUpSkipsWhenPrimaryIsTailnet: when the primary bind
// already IS the tailnet address (--tailnet primary), the goroutine must
// return without double-binding the port.
func TestServeTailnetWhenUpSkipsWhenPrimaryIsTailnet(t *testing.T) {
	origDetect, origInterval := detectTailscaleIP, tailnetRetryInterval
	t.Cleanup(func() { detectTailscaleIP, tailnetRetryInterval = origDetect, origInterval })
	tailnetRetryInterval = time.Millisecond
	detectTailscaleIP = func() (string, error) { return "100.100.1.1", nil }

	done := make(chan struct{})
	go func() {
		serveTailnetWhenUp("100.100.1.1:7777", "7777", http.NewServeMux())
		close(done)
	}()
	select {
	case <-done: // returned without binding — correct
	case <-time.After(2 * time.Second):
		t.Fatal("goroutine kept running; want immediate return when primary covers the tailnet")
	}
}
