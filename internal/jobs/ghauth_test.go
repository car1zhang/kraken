package jobs

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// ghauth_test.go — the account rescue driven entirely through ghauth.go's two
// seams (ghRun, and update.go's updateRunGit for the remote lookup): no gh
// binary, no keychain, and above all nothing that could change the machine's
// real active GitHub account while the suite runs.

const (
	ghStatusVerb  = "auth status"
	ghSwitchVerb  = "auth switch"
	ghRemoteKey   = "ls-remote --get-url origin"
	ghKrakenHTTPS = "https://github.com/car1zhang/kraken.git"
)

// ghStub scripts ghRun, recording every call so a test can assert what did NOT
// run — the interesting half here, since `auth switch` mutates state the whole
// machine shares. It is keyed on the gh VERB rather than the full command line
// so that the flags ghauth.go passes stay free to change (they are asserted
// separately, where that is the point), and it models the one behaviour that
// matters: a successful switch changes who `auth status` then reports, unless
// switchWedged — the "gh said yes and nothing moved" case. A reply prefixed
// with "!" comes back as an error.
type ghStub struct {
	active       string // who `auth status` names; "" falls through to replies
	switchWedged bool
	replies      map[string]string // by verb
	calls        []string          // full command lines, in order
	switchedTo   string
}

func (g *ghStub) run(args ...string) (string, error) {
	cmd := strings.Join(args, " ")
	g.calls = append(g.calls, cmd)
	verb := cmd
	if len(args) >= 2 {
		verb = args[0] + " " + args[1]
	}
	if out, ok := g.replies[verb]; ok && strings.HasPrefix(out, "!") {
		return "", errors.New(out[1:])
	}
	switch verb {
	case ghStatusVerb:
		if g.active != "" {
			return g.active + "\n", nil
		}
		return g.replies[verb], nil
	case ghSwitchVerb:
		g.switchedTo = args[len(args)-1] // `--user <account>` is last
		if !g.switchWedged {
			g.active = g.switchedTo
		}
		return g.replies[verb], nil
	}
	return "", fmt.Errorf("unstubbed gh %s", cmd)
}

func (g *ghStub) count(verb string) int {
	n := 0
	for _, c := range g.calls {
		if strings.HasPrefix(c, verb) {
			n++
		}
	}
	return n
}

func stubGH(t *testing.T, g *ghStub) *ghStub {
	t.Helper()
	prev := ghRun
	ghRun = g.run
	t.Cleanup(func() { ghRun = prev })
	return g
}

// ghNotFound is the git failure every rescue starts from — the real one, as
// updateGit folds git's stderr into its error.
const ghNotFound = "git fetch origin main: exit status 128: remote: Repository not found.\n" +
	"fatal: repository 'https://github.com/car1zhang/kraken.git/' not found"

func TestGHAuthFailure(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		want bool
	}{
		{"repository not found", ghNotFound, true},
		{"authentication failed", "fatal: Authentication failed for 'https://github.com/car1zhang/kraken.git/'", true},
		{"no credential", "fatal: could not read Username for 'https://github.com': terminal prompts disabled", true},
		{"http 403", "fatal: unable to access '...': The requested URL returned error: 403", true},
		{"case insensitive", "REMOTE: REPOSITORY NOT FOUND.", true},
		// The whole point of the list: these are fetch failures a different
		// credential cannot fix, and switching the machine's GitHub account in
		// response to one would be a global side effect on an unrelated
		// problem.
		{"no network", "fatal: unable to access 'https://github.com/car1zhang/kraken.git/': Could not resolve host: github.com", false},
		{"corrupt object", "fatal: loose object 4030fe1 is corrupt", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ghAuthFailure(tc.msg); got != tc.want {
				t.Fatalf("ghAuthFailure(%q) = %v, want %v", tc.msg, got, tc.want)
			}
		})
	}
}

func TestGHRemoteOwner(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		want    string
		wantErr string // substring; "" = expect success
	}{
		{"https with .git", ghKrakenHTTPS, "car1zhang", ""},
		{"https without .git", "https://github.com/car1zhang/kraken", "car1zhang", ""},
		{"org remote", "https://github.com/anthropics/claude-code.git", "anthropics", ""},
		// git treats the scheme and host case-insensitively; the owner is a
		// path segment and is returned as written.
		{"uppercase host", "HTTPS://GitHub.com/Car1zhang/kraken.git", "Car1zhang", ""},
		// gh's credential helper governs https://github.com only, so a switch
		// could not change what these do. The ssh case is what makes
		// ls-remote --get-url the right lookup: an insteadOf rewrite lands
		// here rather than being reported as the https form it was written as.
		{"ssh", "git@github.com:car1zhang/kraken.git", "", "only governs https://github.com remotes"},
		{"other host", "https://gitlab.com/car1zhang/kraken.git", "", "only governs https://github.com remotes"},
		{"owner only", "https://github.com/car1zhang", "", "could not read an owner"},
		{"git failure", "!fatal: not a git repository", "", "not a git repository"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubGit(t, gitStub{ghRemoteKey: tc.url}.run)
			got, err := ghRemoteOwner("/repo")
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("ghRemoteOwner: %v", err)
				}
				if got != tc.want {
					t.Fatalf("ghRemoteOwner(%s) = %q, want %q", tc.url, got, tc.want)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("ghRemoteOwner(%s) = %q, %v; want error containing %q", tc.url, got, err, tc.wantErr)
			}
		})
	}
}

func TestGHActiveAccount(t *testing.T) {
	t.Run("reads the login gh names", func(t *testing.T) {
		gh := stubGH(t, &ghStub{active: "car1zhang"})
		got, err := ghActiveAccount()
		if err != nil || got != "car1zhang" {
			t.Fatalf("ghActiveAccount = %q, %v; want car1zhang", got, err)
		}
		// --json/--jq, not the human-readable form: `gh auth status` exits 1
		// when an account has auth issues — an expired token, which is one of
		// the very failures this rescue is for — and documents that --json
		// always exits zero instead.
		if len(gh.calls) != 1 || !strings.Contains(gh.calls[0], "--json") {
			t.Fatalf("gh calls = %v, want one --json status query", gh.calls)
		}
	})
	t.Run("no active account", func(t *testing.T) {
		stubGH(t, &ghStub{replies: map[string]string{ghStatusVerb: "\n"}})
		if _, err := ghActiveAccount(); err == nil || !strings.Contains(err.Error(), "no active") {
			t.Fatalf("ghActiveAccount err = %v, want it to say gh named nobody", err)
		}
	})
	t.Run("gh failure propagates", func(t *testing.T) {
		stubGH(t, &ghStub{replies: map[string]string{ghStatusVerb: "!gh: command not found"}})
		if _, err := ghActiveAccount(); err == nil || !strings.Contains(err.Error(), "command not found") {
			t.Fatalf("ghActiveAccount err = %v, want gh's own error", err)
		}
	})
}

func TestRecoverGHAccountSwitchesToTheRemotesOwner(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	stubGit(t, gitStub{ghRemoteKey: ghKrakenHTTPS}.run)
	gh := stubGH(t, &ghStub{active: "carl-probook"})

	account, err := RecoverGHAccount("/repo", ghNotFound)
	if err != nil {
		t.Fatalf("RecoverGHAccount: %v", err)
	}
	if account != "car1zhang" {
		t.Fatalf("RecoverGHAccount = %q, want the remote's owner car1zhang", account)
	}
	// --user, never the bare `gh auth switch`: with three accounts logged in
	// the bare form needs a prompt nobody is there to answer, and with two it
	// would happily switch AWAY from a correct account.
	if gh.switchedTo != "car1zhang" || gh.count(ghSwitchVerb) != 1 {
		t.Fatalf("switched to %q in %v, want exactly one --user car1zhang switch", gh.switchedTo, gh.calls)
	}
	// Read back after switching, not only before: the retry that follows costs
	// a network round trip and must not be spent on an assumption.
	if n := gh.count(ghStatusVerb); n < 2 {
		t.Fatalf("active account read %d time(s), want a check and a confirmation; calls: %v", n, gh.calls)
	}
}

func TestRecoverGHAccountRefusals(t *testing.T) {
	cases := []struct {
		name      string
		failure   string
		remote    string
		gh        ghStub
		env       string // an auth env var to set for this case
		wantErr   string
		wantSwtch bool // did a switch legitimately run before the refusal?
	}{
		{
			// The guard that keeps an unrelated outage from flipping the
			// machine's GitHub account: gh is never even consulted.
			name:    "failure is not credential shaped",
			failure: "git fetch origin main: exit status 128: fatal: unable to access: Could not resolve host: github.com",
			remote:  ghKrakenHTTPS,
			wantErr: "does not look like a gh account mismatch",
		},
		{
			name:    "ssh remote",
			failure: ghNotFound,
			remote:  "git@github.com:car1zhang/kraken.git",
			wantErr: "only governs https://github.com remotes",
		},
		{
			// A token in the environment is what git authenticates with, no
			// matter which account is active — and gh itself refuses to switch
			// while one is set.
			name:    "GH_TOKEN in the environment",
			failure: ghNotFound,
			remote:  ghKrakenHTTPS,
			env:     "GH_TOKEN",
			wantErr: "GH_TOKEN is set",
		},
		{
			name:    "GITHUB_TOKEN in the environment",
			failure: ghNotFound,
			remote:  ghKrakenHTTPS,
			env:     "GITHUB_TOKEN",
			wantErr: "GITHUB_TOKEN is set",
		},
		{
			name:    "already the right account",
			failure: ghNotFound,
			remote:  ghKrakenHTTPS,
			gh:      ghStub{active: "car1zhang"},
			wantErr: "already signed in as car1zhang",
		},
		{
			// GitHub logins are case-insensitive, so this account IS the
			// remote's owner — switching to it would ask gh for a user it
			// does not have under that spelling.
			name:    "same account, different spelling",
			failure: ghNotFound,
			remote:  "https://github.com/Car1zhang/kraken.git",
			gh:      ghStub{active: "car1zhang"},
			wantErr: "already signed in as car1zhang",
		},
		{
			name:      "switch refused",
			failure:   ghNotFound,
			remote:    ghKrakenHTTPS,
			gh:        ghStub{active: "carl-probook", replies: map[string]string{ghSwitchVerb: "!specified user car1zhang not found"}},
			wantErr:   "gh auth switch to car1zhang",
			wantSwtch: true,
		},
		{
			// gh said yes and the active account is still wrong — retrying
			// would fail exactly the same way, so this is a refusal too.
			name:      "switch did not take",
			failure:   ghNotFound,
			remote:    ghKrakenHTTPS,
			gh:        ghStub{active: "carl-probook", switchWedged: true},
			wantErr:   "active account is carl-probook, not car1zhang",
			wantSwtch: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Both are cleared for every case, so a token exported in the
			// developer's own shell can't decide the outcome of a test.
			t.Setenv("GH_TOKEN", "")
			t.Setenv("GITHUB_TOKEN", "")
			if tc.env != "" {
				t.Setenv(tc.env, "gho_notarealtoken")
			}
			stubGit(t, gitStub{ghRemoteKey: tc.remote}.run)
			gh := tc.gh // copy: the stub records calls
			stubGH(t, &gh)

			account, err := RecoverGHAccount("/repo", tc.failure)
			if err == nil {
				t.Fatalf("RecoverGHAccount = %q, want refusal %q", account, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("RecoverGHAccount err = %q, want it to contain %q", err, tc.wantErr)
			}
			if account != "" {
				t.Fatalf("RecoverGHAccount returned account %q alongside an error — the caller would retry on it", account)
			}
			if got := gh.count(ghSwitchVerb) > 0; got != tc.wantSwtch {
				t.Fatalf("switch ran = %v, want %v; calls: %v", got, tc.wantSwtch, gh.calls)
			}
		})
	}
}
