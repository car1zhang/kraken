package core

import (
	"strings"
	"testing"
	"time"
)

// Blind tests for the email arm of redactSecrets, written from the frozen
// contract ("email-shaped strings are masked at every redaction point")
// without the implementation in view. Every address below is invented.

// TestRedactEmailMasksEmailShapes: each email-shaped string the contract
// lists as MUST-mask is replaced whole by the literal `[email]`, nothing of
// the value kept and no prefix, through both the unexported redactSecrets
// and the exported RedactSecrets.
func TestRedactEmailMasksEmailShapes(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"dotted local part", "jane.doe@example.com", "[email]"},
		{"plus tag and multi-label domain", "ops+alerts@mail.example.co.uk", "[email]"},
		{"upper case with underscore and digits", "ME_42@EXAMPLE.ORG", "[email]"},
		{"ssh remote user part", "git@github.com", "[email]"},
		{"percent and hyphen in local part", "first-last%dept@example.org", "[email]"},
		{"hyphenated domain label", "nobody@my-example.example.com", "[email]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := redactSecrets(c.in); got != c.want {
				t.Errorf("redactSecrets(%q) = %q, want %q", c.in, got, c.want)
			}
			if got := RedactSecrets(c.in); got != c.want {
				t.Errorf("RedactSecrets(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestRedactEmailLeavesNonEmailsAlone: the contract's MUST-NOT-mask examples
// come back byte for byte unchanged.
func TestRedactEmailLeavesNonEmailsAlone(t *testing.T) {
	cases := []struct{ name, in string }{
		{"handle with no local part", "@handle"},
		{"single domain label", "foo@bar"},
		{"final label not letters only", "a@b.c1"},
		{"no dot after the at sign", "price@2x"},
		{"the mask literal itself", "[email]"},
		{"spelled-out address", "user at example dot com"},
		{"handle inside prose", "ping @handle when the build is green"},
		{"single label inside prose", "ssh into foo@bar and check"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := redactSecrets(c.in); got != c.in {
				t.Errorf("redactSecrets(%q) = %q, want it unchanged", c.in, got)
			}
			if got := RedactSecrets(c.in); got != c.in {
				t.Errorf("RedactSecrets(%q) = %q, want it unchanged", c.in, got)
			}
		})
	}
}

// TestRedactEmailPreservesSurroundingText: only the match is replaced;
// everything around it, punctuation included, survives byte for byte, and
// every address in a string is masked, not just the first.
func TestRedactEmailPreservesSurroundingText(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"embedded in prose", "mail jane@example.com now", "mail [email] now"},
		{"angle brackets", "<jane@example.com>", "<[email]>"},
		{"sentence-ending period", "jane@example.com.", "[email]."},
		{"several addresses", "cc ada@example.org, bob.smith@example.com and ops+x@mail.example.co.uk today",
			"cc [email], [email] and [email] today"},
		{"two adjacent by a space", "ada@example.org bob@example.org", "[email] [email]"},
		{"quoted in a command", "git config user.email 'pat.doe@example.com' --global",
			"git config user.email '[email]' --global"},
		{"after a colon and before a newline", "From: sam@example.net\nSubject: hi", "From: [email]\nSubject: hi"},
		{"parenthesized", "(owner: lee@example.io)", "(owner: [email])"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := redactSecrets(c.in); got != c.want {
				t.Errorf("redactSecrets(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestRedactEmailComposesWithCredentials: one call masks both a credential
// token (unchanged arm: first five runes plus `…[redacted]`) and an email,
// and the credential arm on its own is untouched by the new behaviour.
func TestRedactEmailComposesWithCredentials(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"credential then email", "key sk-abcdefghijklmnop for jane@example.com",
			"key sk-ab…[redacted] for [email]"},
		{"email then credential", "from ops@example.org: sk-ant-api03-aaaaaaaaaaaaaaaaaaaaaaaa done",
			"from [email]: sk-an…[redacted] done"},
		{"aws key and two emails", "ada@example.org AKIAIOSFODNN7EXAMPLE bob@example.com",
			"[email] AKIAI…[redacted] [email]"},
		{"credential alone still keeps its prefix", "key AKIAIOSFODNN7EXAMPLE end", "key AKIAI…[redacted] end"},
		{"prefix-sharing name alone still passes", "use sk-learn and deriveState()", "use sk-learn and deriveState()"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := redactSecrets(c.in); got != c.want {
				t.Errorf("redactSecrets(%q) = %q, want %q", c.in, got, c.want)
			}
			if got := RedactSecrets(c.in); got != c.want {
				t.Errorf("RedactSecrets(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestRedactEmailIsIdempotent: redacting an already-redacted string changes
// nothing, for emails, credentials, both, and neither.
func TestRedactEmailIsIdempotent(t *testing.T) {
	inputs := []string{
		"jane.doe@example.com",
		"mail jane@example.com now",
		"<jane@example.com>",
		"jane@example.com.",
		"key sk-abcdefghijklmnop for jane@example.com",
		"from ops@example.org: sk-ant-api03-aaaaaaaaaaaaaaaaaaaaaaaa done",
		"git@github.com:example-org/example-repo.git",
		"[email]",
		"plain prose about a session token and a cookie",
		"@handle foo@bar a@b.c1 price@2x",
		"",
	}
	for _, in := range inputs {
		once := redactSecrets(in)
		if twice := redactSecrets(once); twice != once {
			t.Errorf("not idempotent on %q: once %q, twice %q", in, once, twice)
		}
	}
}

// TestRedactEmailPlainTextUnchanged: a string carrying neither an email nor
// a credential comes back exactly as given.
func TestRedactEmailPlainTextUnchanged(t *testing.T) {
	inputs := []string{
		"",
		"plain prose about a session token and a cookie",
		"run go test ./internal/core/ and report",
		"the at sign @ alone, and an @mention",
		"version 1.2.3 at 10:42, see docs.example.com/path",
		"multi\nline\ttext with  double spaces",
	}
	for _, in := range inputs {
		if got := redactSecrets(in); got != in {
			t.Errorf("redactSecrets(%q) = %q, want it unchanged", in, got)
		}
	}
}

// TestRedactEmailReachesReducedView: the mask lives inside redactSecrets, so
// the reduced view ReduceTranscript produces carries `[email]` wherever an
// address sat in a user record's text, an assistant's text, or a tool call's
// summary, and never the address itself.
func TestRedactEmailReachesReducedView(t *testing.T) {
	const userAddr = "quinn.example@example.com"
	const assistantAddr = "support+desk@mail.example.co.uk"
	const commandAddr = "robin.sample@example.org"
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	since := now - int64(time.Hour/time.Millisecond)

	path := mineWrite(t, projects, "-Users-carl-Developer-monolith", "sess.jsonl",
		mineUser(t, now-5000, "please email "+userAddr+" about the outage"),
		mineAssistant(t, now-4000, "claude-opus-5",
			mineText("I will copy "+assistantAddr+" on the follow-up; @handle stays"),
			mineToolUse("t1", "Bash", map[string]any{"command": "git config user.email " + commandAddr}),
		),
	)

	out, err := ReduceTranscript(path, since)
	if err != nil {
		t.Fatalf("ReduceTranscript: %v", err)
	}
	got := string(out)
	for _, leaked := range []string{userAddr, assistantAddr, commandAddr} {
		if strings.Contains(got, leaked) {
			t.Errorf("reduced view leaked the address %q:\n%s", leaked, got)
		}
	}
	for _, want := range []string{"please email [email] about the outage", "I will copy [email] on the follow-up", "@handle stays"} {
		if !strings.Contains(got, want) {
			t.Errorf("reduced view missing %q:\n%s", want, got)
		}
	}
	if n := strings.Count(got, "[email]"); n < 3 {
		t.Errorf("reduced view carries %d [email] markers, want at least 3:\n%s", n, got)
	}
}
