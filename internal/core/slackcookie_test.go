package core

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// slackcookie_test.go — the session-token half of the credential, added after
// the blind round when the deployment switched from an installed OAuth app to
// carl's own client session. Authorship was NOT split for these: the change
// was one coherent edit made by the lead, so the lead wrote its tests too.
//
// Identifiers here are cookie-prefixed to stay out of the way of the two blind
// suites that share this package.

func cookieSandbox(t *testing.T) string {
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

func cookieWriteEnv(t *testing.T, body string) {
	t.Helper()
	if err := os.WriteFile(SlackEnvPath(), []byte(body), 0o600); err != nil {
		t.Fatalf("write slack.env: %v", err)
	}
}

// A session token needs its cookie, and the pair round-trips out of the file.
func TestCookieSessionTokenLoadsWithItsCookie(t *testing.T) {
	cookieSandbox(t)
	cookieWriteEnv(t, "SLACK_USER_TOKEN=xoxc-session-value\nSLACK_D_COOKIE=xoxd-cookie-value\n")

	cfg, err := LoadSlackConfig()
	if err != nil {
		t.Fatalf("err = %v, want nil for a session token with its cookie", err)
	}
	if cfg.Token != "xoxc-session-value" || cfg.Cookie != "xoxd-cookie-value" {
		t.Fatalf("cfg = %+v, want both halves carried through", cfg)
	}
}

// A session token WITHOUT its cookie is the misconfiguration that must not be
// reported as "feature off": every call it makes would fail as unauthenticated,
// and the cause is one missing line.
func TestCookieSessionTokenWithoutCookieIsHalfConfigured(t *testing.T) {
	cookieSandbox(t)
	cookieWriteEnv(t, "SLACK_USER_TOKEN=xoxc-session-value\n")

	_, err := LoadSlackConfig()
	if !errors.Is(err, ErrSlackHalfConfigured) {
		t.Fatalf("err = %v, want ErrSlackHalfConfigured", err)
	}
	if errors.Is(err, ErrNoSlack) {
		t.Fatal("half-configured must not read as ErrNoSlack — callers stay silent about that one")
	}
	if strings.Contains(err.Error(), "xoxc-session-value") {
		t.Fatalf("err = %q, must never carry the token value", err)
	}
}

// An OAuth token still needs no cookie: the app-install path is unchanged.
func TestCookieOAuthTokenNeedsNoCookie(t *testing.T) {
	cookieSandbox(t)
	cookieWriteEnv(t, "SLACK_USER_TOKEN=xoxp-oauth-value\n")

	cfg, err := LoadSlackConfig()
	if err != nil {
		t.Fatalf("err = %v, want nil for an OAuth token with no cookie", err)
	}
	if cfg.Cookie != "" {
		t.Fatalf("cfg.Cookie = %q, want empty for an OAuth token", cfg.Cookie)
	}
}

// The cookie's value never reaches an error, on the same rule as the token's.
func TestCookieValueNeverReachesAnError(t *testing.T) {
	cookieSandbox(t)
	const secret = "xoxd-super-secret-cookie"
	cookieWriteEnv(t, "SLACK_USER_TOKEN=xoxc-tok\nSLACK_D_COOKIE="+secret+"\n")

	cfg, err := LoadSlackConfig()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("upstream exploded"))
	}))
	defer srv.Close()

	c := &SlackClient{Token: cfg.Token, Cookie: cfg.Cookie, BaseURL: srv.URL}
	_, err = c.AuthTest()
	if err == nil {
		t.Fatal("want an error from a 500")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "xoxc-tok") {
		t.Fatalf("err = %q, must carry neither credential", err)
	}
}

// The cookie rides on every request, verbatim, and only when it is set.
func TestCookieIsSentVerbatimAndOnlyWhenSet(t *testing.T) {
	const cookieValue = "xoxd-percent%2Fslash-and+plus"

	for _, tc := range []struct {
		name   string
		cookie string
		want   string // "" means: no d cookie at all
	}{
		{"session token sends the cookie", cookieValue, cookieValue},
		{"oauth token sends none", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			var present bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if ck, err := r.Cookie("d"); err == nil {
					present, got = true, ck.Value
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"ok":true,"user_id":"U1","team_id":"T1"}`))
			}))
			defer srv.Close()

			c := NewSlackClient("xoxc-tok", tc.cookie)
			c.BaseURL = srv.URL
			if _, err := c.AuthTest(); err != nil {
				t.Fatalf("AuthTest: %v", err)
			}
			if tc.want == "" {
				if present {
					t.Fatalf("d cookie = %q, want none sent", got)
				}
				return
			}
			if !present {
				t.Fatal("no d cookie sent, want one")
			}
			// Verbatim: the value in the browser's jar is already in wire
			// form, so any re-encoding here is a cookie Slack rejects.
			if got != tc.want {
				t.Fatalf("d cookie = %q, want %q verbatim", got, tc.want)
			}
		})
	}
}
