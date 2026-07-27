package jobs

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ghauth.go — the one place kraken cares WHICH GitHub account `gh` is signed
// into, and the only place anything switches it.
//
// The failure it exists for is specific and recurring. This machine keeps two
// gh accounts logged in at once (car1zhang and carl-probook), and ~/.gitconfig
// points `credential."https://github.com".helper` at `gh auth git-credential`
// — so whichever account is ACTIVE is the account every git fetch of a
// github.com https remote authenticates as. GitHub does not answer a private
// repo an authenticated-but-unentitled token asks for with a permission error;
// it answers "Repository not found", which reads exactly like the checkout
// pointing at a repo that moved or was deleted. `kraken update` then dies at
// its very first network call, and the remedy — one `gh auth switch` — is
// invisible unless you already know this symptom. So the updater runs it
// itself and retries, rather than failing and waiting for a human to recognize
// the shape.
//
// Scope is deliberately one failure path: the updater's fetch, on failure
// only. The active gh account is GLOBAL state that everything else on the
// machine reads (other claude sessions, the daemon's own `gh pr list`), so it
// is changed reactively — after something has actually broken and only when
// switching could plausibly fix it — and never as a precaution.

// ghHost is the only host this file reasons about. gh supports enterprise
// hosts and multiple accounts per host, but the credential-helper wiring above
// is github.com's alone, and everything here keys off a github.com remote.
const ghHost = "github.com"

// ghTimeout bounds every gh call. `gh auth status` validates the token against
// the API, so these are network calls, and the updater that makes them is
// detached with nobody to notice it hanging. A gh that never returns would pin
// the run in `fetching` until staleUpdate's 15-minute deadline retires it —
// fifteen minutes to learn nothing, when the honest answer (the original fetch
// error) was already in hand.
const ghTimeout = 30 * time.Second

// ghAuthFailureSigns mark a git failure as one a DIFFERENT credential could
// plausibly fix. The first entry is the real symptom and the reason this list
// is not just "any fetch error": a network outage, a dead remote, or a corrupt
// object store are all fetch failures too, and switching the machine's active
// GitHub account in response to any of them would be a global side effect
// applied to an unrelated problem.
//
// Matched case-insensitively against git's combined output, which is where the
// useful text lives (updateGit folds stderr into its error for this reason).
var ghAuthFailureSigns = []string{
	"repository not found",      // the symptom: GitHub hides private repos from tokens that cannot see them
	"authentication failed",     // a token the remote rejected outright
	"could not read username",   // GIT_TERMINAL_PROMPT=0 and no usable credential
	"terminal prompts disabled", // the same case, in git's other phrasing
	"invalid username or token",
	"error: 403", // git's "The requested URL returned error: 403"
	"403 forbidden",
}

// ghAuthFailure reports whether a git failure looks like the active gh account
// being the wrong one. Substring, not regex: git's phrasing varies by version
// and transport, and the cost of a false positive is one wasted `gh auth
// status` (the switch itself is still gated on the account actually differing
// from the remote's owner).
func ghAuthFailure(msg string) bool {
	low := strings.ToLower(msg)
	for _, sign := range ghAuthFailureSigns {
		if strings.Contains(low, sign) {
			return true
		}
	}
	return false
}

// ghRemoteOwner returns the GitHub user or org that owns repo's origin remote
// — which is the account gh has to be signed in as for a fetch of it to
// succeed.
//
// Deriving it from the remote rather than hardcoding a username is the same
// rule as every path in this binary coming from util.Home(): the answer is
// already written down in the checkout, and a constant would be one more thing
// to be wrong when the remote changes.
func ghRemoteOwner(repo string) (string, error) {
	// ls-remote --get-url rather than `remote get-url`: it is the form that
	// applies url.<base>.insteadOf rewrites, so what comes back is the URL git
	// will actually dial. The difference decides this function's answer — a
	// checkout that rewrites https://github.com/ to git@github.com: fetches
	// over ssh, where no account switch can help, while `remote get-url` would
	// still report the https form and send us switching for nothing. It is
	// local and instant despite the name (--get-url prints and exits).
	url, err := updateRunGit(context.Background(), repo, "ls-remote", "--get-url", "origin")
	if err != nil {
		return "", err
	}
	url = strings.TrimSpace(url)
	// Case-insensitive on the scheme and host, which git itself treats that
	// way, but never on the owner: that is a path segment and GitHub echoes it
	// back as written.
	prefix := "https://" + ghHost + "/"
	if !strings.HasPrefix(strings.ToLower(url), prefix) {
		// ssh remotes (git@github.com:owner/repo) and every non-github remote
		// land here on purpose. gh's credential helper is configured for
		// https://github.com only, so no account switch could change what a
		// fetch over another transport does — and a switch that cannot help is
		// a global state change made for nothing.
		return "", fmt.Errorf("origin is %s — gh's credential helper only governs https://%s remotes", url, ghHost)
	}
	owner, _, ok := strings.Cut(url[len(prefix):], "/")
	if !ok || owner == "" {
		return "", fmt.Errorf("could not read an owner out of origin url %q", url)
	}
	return owner, nil
}

// ghActiveAccount reports which account gh is currently signed in as on
// github.com.
//
// --json, rather than parsing the human-readable status: `gh auth status`
// exits 1 when an account has authentication issues, and documents that --json
// always exits zero instead. Those two behaviours are the difference between a
// working rescue and none at all in the case it most needs to work — an
// EXPIRED token is both a reason git says "Authentication failed" and a reason
// gh exits non-zero, so the plain form would throw away the perfectly good
// answer to "who is active?" exactly when a switch is what's wanted.
func ghActiveAccount() (string, error) {
	out, err := ghRun("auth", "status", "--hostname", ghHost, "--active",
		"--json", "hosts", "--jq", `.hosts["`+ghHost+`"][] | select(.active) | .login`)
	if err != nil {
		return "", err
	}
	// First line only: --active plus the select() should leave exactly one,
	// and picking the first beats gluing two together into a name that is
	// neither.
	account, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
	if account == "" {
		return "", fmt.Errorf("gh names no active %s account", ghHost)
	}
	return account, nil
}

// RecoverGHAccount is the whole feature: given the git failure that just
// happened in repo, decide whether the active gh account is to blame and, if
// so, switch to the one that owns repo's origin remote. It returns the account
// it switched TO, which is the caller's signal to retry.
//
// An error means NOTHING was changed and a retry would be pointless — the
// failure wasn't credential-shaped, the remote isn't one gh governs, gh is
// already signed in as the right account, or the switch itself was refused.
// The caller reports its original git error in that case, never this one: this
// error explains why the rescue didn't apply, and the git error is what
// actually went wrong.
func RecoverGHAccount(repo, failure string) (string, error) {
	if !ghAuthFailure(failure) {
		return "", fmt.Errorf("git failure does not look like a gh account mismatch")
	}
	// A token in the environment outranks everything gh has stored: the
	// credential helper hands that token to the remote whichever account is
	// active, and `gh auth switch` refuses to run at all while it is set. So
	// whatever went wrong, the active account is not it — and switching would
	// be a global change that could not even affect the retry. Only the NAME
	// is ever reported; the value is a credential.
	for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		if os.Getenv(name) != "" {
			return "", fmt.Errorf("%s is set — git authenticates with that token, not with the active gh account", name)
		}
	}
	owner, err := ghRemoteOwner(repo)
	if err != nil {
		return "", err
	}
	active, err := ghActiveAccount()
	if err != nil {
		return "", fmt.Errorf("could not read the active gh account: %w", err)
	}
	// EqualFold, here and on the confirmation below: GitHub logins are
	// case-insensitive, so a remote written .../Car1zhang/kraken.git names the
	// account already signed in — and comparing exactly would send us switching
	// to an account gh would then refuse as "not found", which reads as a
	// broken machine rather than a spelling difference.
	if strings.EqualFold(active, owner) {
		// The one case worth spelling out: the credentials were already the
		// right ones, so "Repository not found" meant what it said.
		return "", fmt.Errorf("gh is already signed in as %s — the fetch failed for another reason", active)
	}
	// --user, rather than the bare `gh auth switch` that flips between two
	// accounts: with exactly two logged in they are the same thing, but with
	// three the bare form needs a prompt nobody is there to answer, and with
	// two it would happily switch AWAY from a correct account. Naming the
	// target makes "switch until it matches" a single deterministic step.
	if _, err := ghRun("auth", "switch", "--hostname", ghHost, "--user", owner); err != nil {
		return "", fmt.Errorf("gh auth switch to %s: %w", owner, err)
	}
	// Read it back rather than trusting the exit code. This is global state
	// being changed on a machine where other things are also using gh, and a
	// retry costs a network round trip against credentials we would otherwise
	// only be assuming are now correct.
	switched, err := ghActiveAccount()
	if err != nil {
		return "", fmt.Errorf("switched to %s but could not confirm it: %w", owner, err)
	}
	if !strings.EqualFold(switched, owner) {
		return "", fmt.Errorf("gh auth switch reported success but the active account is %s, not %s", switched, owner)
	}
	// gh's spelling of the account, not the remote's: it is what `gh auth
	// status` will show the human who goes looking afterwards.
	return switched, nil
}

// --- swappable seam, same pattern as update.go's: nothing in the test suite
// shells out. ---
var (
	// ghRun runs one gh command and returns its trimmed combined output. gh is
	// resolved through lookToolchain for the same reason go and make are —
	// this process is usually a child of a launchd job, whose PATH does not
	// include /opt/homebrew/bin where gh lives — and prompting is disabled for
	// the same reason updateRunGit sets GIT_TERMINAL_PROMPT=0: a gh that stops
	// to ask a question has already failed, and should say so immediately
	// rather than sit on a prompt no one can see.
	ghRun = func(args ...string) (string, error) {
		bin, err := lookToolchain("gh")
		if err != nil {
			return "", err
		}
		ctx, cancel := context.WithTimeout(context.Background(), ghTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1")
		// stdout kept SEPARATE from stderr, unlike updateGit's CombinedOutput.
		// gh writes advice to stderr while still exiting zero — signed into
		// nothing, it prints "You are not logged into any GitHub hosts…" there
		// and `{"hosts":{}}` on stdout — and this output is PARSED, not just
		// reported. Combined, that sentence would come back as the name of the
		// active account and end up in a refusal reading "the active account
		// is You are not logged into any GitHub hosts…". stderr is still what
		// the error carries, since that is where gh explains a failure.
		var errOut strings.Builder
		cmd.Stderr = &errOut
		out, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("gh %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(errOut.String()))
		}
		return strings.TrimSpace(string(out)), nil
	}
)
