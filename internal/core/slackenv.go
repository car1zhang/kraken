package core

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"kraken/internal/util"
)

// slackenv.go — the Slack intake bot's credential channel. This is a copy of
// the asc.env loader rather than a shared helper on purpose: each
// one owns its own allowlist, and an allowlist that is shared is an allowlist
// that grows by accident. What is shared is the SHAPE, and every piece of it
// is load-bearing.
//
// A file rather than launchd environment, for the reason ASCEnvPath spells
// out at length: `launchctl setenv` provably does not reach a job whose plist
// declares its own EnvironmentVariables, and is lost on reboot besides. The
// poller runs inside `kraken serve`, which is exactly such a job.
//
// Parsed, never sourced, and only the two allowlisted keys are honored: this
// file is a credential channel, not a general environment, and letting it set
// PATH or DYLD_* would turn "someone can write one file in ~/.kraken" into
// arbitrary code execution inside the daemon.
//
// Process env wins over the file, so an explicit `export` in a terminal can
// override it without editing the file first.
//
// Failure posture is "off", not ship's refusal: no token means the feature
// is OFF, silently. Every caller treats ErrNoSlack as "not configured" and
// never logs it as an error, so a machine that has never set Slack up sees
// nothing at all rather than a failing pass every tick.

// SlackEnvPath is the credential file: ~/.kraken/slack.env, 0600, holding
// SLACK_USER_TOKEN, optionally SLACK_D_COOKIE, and optionally SLACK_USER_ID.
func SlackEnvPath() string { return filepath.Join(util.Home(), ".kraken", "slack.env") }

// slackEnvKeys is the whole allowlist. SLACK_USER_TOKEN is the user token the
// poller authenticates as; SLACK_D_COOKIE carries the browser session cookie
// that a client-session token additionally requires; SLACK_USER_ID is optional
// and only saves one auth.test call, since the id is otherwise discovered from
// that call.
var slackEnvKeys = []string{"SLACK_USER_TOKEN", "SLACK_D_COOKIE", "SLACK_USER_ID"}

// Two token kinds reach this file, and they differ in what else they need.
//
// An OAuth user token (xoxp-) is self-sufficient: it authenticates with the
// Authorization header alone, does not expire unless token rotation was opted
// into, and is what an installed Slack app mints.
//
// A CLIENT SESSION token (xoxc-) is what the Slack desktop and web clients
// themselves carry, and it is only half a credential: Slack binds it to the
// browser session cookie named `d`, so a request without that cookie is
// rejected as unauthenticated no matter how valid the token is. It is also
// session-scoped rather than durable — signing out, a forced reauth, or a
// workspace session policy invalidates it, and nothing refreshes it. That is a
// real operational difference rather than a stylistic one, so the two halves
// are validated together below: a session token with no cookie is a
// MISCONFIGURATION and says so, rather than degrading to the silent
// "not configured" state a genuinely absent token gets.
const slackSessionTokenPrefix = "xoxc-"

// ErrNoSlack marks "no Slack token configured" — the normal state of a fresh
// install, not an error condition. Callers switch on it with errors.Is. The
// message names the file in its tilde form rather than interpolating
// SlackEnvPath: this is a package-level var initialised before anything can
// override util.Home in tests, and the tilde form is what a human fixes it
// with.
var ErrNoSlack = errors.New("no slack token configured (no SLACK_USER_TOKEN in env or ~/.kraken/slack.env)")

// ErrSlackHalfConfigured is the OTHER terminal state, and it is deliberately
// not ErrNoSlack: an xoxc- session token with no SLACK_D_COOKIE beside it is
// someone half way through setting this up, and every Slack call such a config
// makes fails as unauthenticated. Reporting that as "feature off" would leave
// a poller that is configured, silent, and doing nothing, with the one fixable
// cause unstated. Callers log this one; they never log ErrNoSlack.
var ErrSlackHalfConfigured = errors.New("SLACK_USER_TOKEN is a session (xoxc-) token but SLACK_D_COOKIE is not set — a session token is rejected without its `d` cookie")

// SlackConfig is what the poller needs to talk to Slack as carl. UserID may be
// empty, in which case the caller resolves it once via AuthTest. Cookie is
// empty for an OAuth (xoxp-) token and carries the `d` session cookie's value
// for a client-session (xoxc-) one.
type SlackConfig struct {
	Token  string
	Cookie string
	UserID string
}

// loadSlackEnv parses SlackEnvPath, same defensive shape as loadASCEnv: a
// missing, unreadable, or malformed file is an empty map,
// never an error. A parse error nobody can act on would be strictly worse
// than the feature reading as unconfigured, which is the state a human
// already knows how to fix.
func loadSlackEnv() map[string]string {
	out := map[string]string{}
	data, err := os.ReadFile(SlackEnvPath())
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(strings.Trim(strings.TrimSpace(v), `"'`))
		for _, allowed := range slackEnvKeys {
			if k == allowed && v != "" {
				out[k] = v
			}
		}
	}
	return out
}

// slackSetting reads one key from the pre-loaded file map, process env
// winning over the file.
func slackSetting(file map[string]string, key string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return file[key]
}

// LoadSlackConfig returns the configured Slack credentials. It has exactly two
// error returns, and they mean opposite things: ErrNoSlack is "nothing is set
// up", the normal state of a fresh install, which callers treat as feature-off
// and never log; ErrSlackHalfConfigured is "something is set up wrong", which
// callers DO log, because every call the config would make is doomed and the
// cause is one line in a file. Every other failure mode — an absent file, an
// unreadable one, a line that does not parse — still degrades to ErrNoSlack.
//
// Neither the token's nor the cookie's VALUE ever appears in an error, which is
// why both sentinels are fixed strings rather than anything built from what was
// read.
func LoadSlackConfig() (SlackConfig, error) {
	file := loadSlackEnv()
	token := slackSetting(file, "SLACK_USER_TOKEN")
	if token == "" {
		return SlackConfig{}, ErrNoSlack
	}
	cookie := slackSetting(file, "SLACK_D_COOKIE")
	if strings.HasPrefix(token, slackSessionTokenPrefix) && cookie == "" {
		return SlackConfig{}, ErrSlackHalfConfigured
	}
	return SlackConfig{
		Token:  token,
		Cookie: cookie,
		UserID: slackSetting(file, "SLACK_USER_ID"),
	}, nil
}
