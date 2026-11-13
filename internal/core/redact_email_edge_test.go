package core

import (
	"strings"
	"testing"
	"time"
)

// redact_email_edge_test.go — athena-edge's BLIND tests for the frozen
// contract "email-shaped strings are masked at every redaction point".
// Written from the contract text alone; the implementation was never read.
//
// Dimension: EDGE — the boundaries of the email shape, punctuation around a
// match, composition with the credential arm, idempotence, and the reach
// through ReduceTranscript and OperatorProse. Every fixture value is
// invented (example.com / example.org / example.co.uk style domains, made-up
// names). The harness (mineHome, mineWrite, mineUser, mineAssistant,
// mineToolUse, mineText) is mine_test.go's; mpcWindow, mpcPrompt, mpcAsst,
// mpcRun and mpcByUUID are mindpass_contract_test.go's.
//
// Cases whose expected output the contract leaves open are deliberately NOT
// pinned here (see the ledger): a final label followed by digits, `_` or `-`
// (`jane@example.com2024`), a non-ASCII rune fused to the local part
// (`josé@example.com`), and two addresses chained through one `@`.

// redactEmailEdgeCase is one exact-output expectation.
type redactEmailEdgeCase struct {
	name, in, want string
}

func redactEmailEdgeRun(t *testing.T, cases []redactEmailEdgeCase) {
	t.Helper()
	for _, c := range cases {
		if got := redactSecrets(c.in); got != c.want {
			t.Errorf("%s: redactSecrets(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
		if got := RedactSecrets(c.in); got != c.want {
			t.Errorf("%s: RedactSecrets(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

// Obligation 2: the boundaries of the domain shape — two or more labels, the
// final label letters only and at least two of them.
func TestRedactEmailDomainShapeBoundaries(t *testing.T) {
	redactEmailEdgeRun(t, []redactEmailEdgeCase{
		// masked
		{"final label exactly two letters", "ping jane@example.io now", "ping [email] now"},
		{"two-letter country label after subdomains", "ops+alerts@mail.example.co.uk", "[email]"},
		{"hyphen inside a domain label", "relay@mail-relay.example.org", "[email]"},
		{"many labels", "a.b@x.y.z.example.com", "[email]"},
		{"digits in a non-final label", "jane@host42.example.com", "[email]"},
		{"long final label", "jane@example.museum", "[email]"},
		// not masked
		{"one-label domain", "jane@localhost is local", "jane@localhost is local"},
		{"numeric final label (IPv4 host)", "root@10.0.0.1", "root@10.0.0.1"},
		{"numeric final label", "jane@example.123", "jane@example.123"},
		{"final label a single letter", "jane@example.c", "jane@example.c"},
		{"empty label between dots", "jane@example..com", "jane@example..com"},
		{"contract: final label not letters only", "a@b.c1", "a@b.c1"},
		{"contract: one domain label", "foo@bar", "foo@bar"},
		{"contract: no dot", "price@2x", "price@2x"},
	})
}

// Obligation 2: the boundaries of the local part — one or more characters
// from `A-Z a-z 0-9 . _ % + -`.
func TestRedactEmailLocalPartBoundaries(t *testing.T) {
	redactEmailEdgeRun(t, []redactEmailEdgeCase{
		{"one-character local part", "j@example.com", "[email]"},
		{"plus tag", "jane+newsletter@example.com", "[email]"},
		{"percent in the local part", "jane%relay@example.com", "[email]"},
		{"underscore and digits", "ME_42@EXAMPLE.ORG", "[email]"},
		{"dotted local part", "first.middle.last@example.org", "[email]"},
		{"hyphenated local part", "on-call@example.org", "[email]"},
		{"no local part", "ask @handle about it", "ask @handle about it"},
		{"bare at sign", "@", "@"},
		{"at sign then domain only", "@example.com", "@example.com"},
		{"spelled out", "user at example dot com", "user at example dot com"},
		{"url-encoded at sign is not an at sign", "jane%40example.com", "jane%40example.com"},
	})
}

// Obligation 2: upper and mixed case are masked the same.
func TestRedactEmailCaseInsensitive(t *testing.T) {
	redactEmailEdgeRun(t, []redactEmailEdgeCase{
		{"all upper", "JANE.DOE@EXAMPLE.COM", "[email]"},
		{"mixed", "Jane.Doe@Example.Com", "[email]"},
		{"mixed final label", "jane@example.CoM", "[email]"},
	})
}

// Obligation 3: punctuation around a match is preserved byte for byte, and
// nothing of the value survives.
func TestRedactEmailPunctuationAroundAMatch(t *testing.T) {
	redactEmailEdgeRun(t, []redactEmailEdgeCase{
		{"trailing dot", "jane@example.com.", "[email]."},
		{"trailing dot mid-sentence", "write to jane@example.com. Then wait.", "write to [email]. Then wait."},
		{"followed by a comma", "jane@example.com, bob@example.org", "[email], [email]"},
		{"angle brackets", "<jane@example.com>", "<[email]>"},
		{"display-name form", `Jane Doe <jane@example.com>`, `Jane Doe <[email]>`},
		{"double quotes", `"jane@example.com"`, `"[email]"`},
		{"single quotes", `'jane@example.com'`, `'[email]'`},
		{"parentheses", "(jane@example.com)", "([email])"},
		{"square brackets", "[jane@example.com]", "[[email]]"},
		{"colon after", "jane@example.com: hello", "[email]: hello"},
		{"semicolon list", "jane@example.com;bob@example.org", "[email];[email]"},
		{"possessive", "jane@example.com's inbox", "[email]'s inbox"},
		{"question mark", "is it jane@example.com?", "is it [email]?"},
		{"mailto prefix", "mailto:jane@example.com", "mailto:[email]"},
		{"html mailto link", `<a href="mailto:jane@example.com">mail</a>`, `<a href="mailto:[email]">mail</a>`},
		{"url query string", "https://app.example.com/invite?email=jane@example.com&ref=1", "https://app.example.com/invite?email=[email]&ref=1"},
		{"ssh remote user part (by design)", "git remote add origin git@github.com:acme/widgets.git", "git remote add origin [email]:acme/widgets.git"},
		{"followed by another at sign", "jane@example.com@bob", "[email]@bob"},
		{"json value", `{"email":"jane@example.com"}`, `{"email":"[email]"}`},
		{"key=value", "OPERATOR_EMAIL=jane@example.com", "OPERATOR_EMAIL=[email]"},
	})
}

// Obligation 3: positions in the string — the very start, the very end, the
// whole string, several in one string, one space apart, across lines.
func TestRedactEmailPositions(t *testing.T) {
	redactEmailEdgeRun(t, []redactEmailEdgeCase{
		{"whole string", "jane@example.com", "[email]"},
		{"very start", "jane@example.com wrote this", "[email] wrote this"},
		{"very end", "this came from jane@example.com", "this came from [email]"},
		{"start and end", "jane@example.com cc bob@example.org", "[email] cc [email]"},
		{"two separated by one space", "jane@example.com bob@example.org", "[email] [email]"},
		{"three in a row", "a@example.com b@example.org c@example.net", "[email] [email] [email]"},
		{"across lines", "to: jane@example.com\ncc: bob@example.org\n", "to: [email]\ncc: [email]\n"},
		{"tab separated", "jane@example.com\tbob@example.org", "[email]\t[email]"},
	})
}

// Obligation 2 + 5: the marker itself, the empty string, and email-free text
// pass through unchanged.
func TestRedactEmailEdgeLeavesNonEmailsAlone(t *testing.T) {
	redactEmailEdgeRun(t, []redactEmailEdgeCase{
		{"empty string", "", ""},
		{"marker already present", "[email]", "[email]"},
		{"marker in prose", "the reducer writes [email] for each address", "the reducer writes [email] for each address"},
		{"marker twice", "[email], [email]", "[email], [email]"},
		{"decorator", "use @dataclass and @property", "use @dataclass and @property"},
		{"scoped npm package", "npm i @types/node", "npm i @types/node"},
		{"git ref syntax", "HEAD@{1} and main@{upstream}", "HEAD@{1} and main@{upstream}"},
		{"versioned package", "pip install requests@2.31", "pip install requests@2.31"},
		{"unicode only", "naïve café — 東京 👋", "naïve café — 東京 👋"},
	})
}

// Obligation 3: unicode text around a match is left byte for byte intact.
func TestRedactEmailUnicodeAroundAMatch(t *testing.T) {
	redactEmailEdgeRun(t, []redactEmailEdgeCase{
		{"accented prose", "résumé from jane@example.com — merci", "résumé from [email] — merci"},
		{"guillemets", "«jane@example.com»", "«[email]»"},
		{"cjk around", "連絡先 jane@example.com です", "連絡先 [email] です"},
		{"emoji after", "ping jane@example.com 👋", "ping [email] 👋"},
		{"em dash fused", "jane@example.com—bob@example.org", "[email]—[email]"},
		{"curly quotes", "“jane@example.com”", "“[email]”"},
	})
}

// Obligation 4: a credential and an email in one string are both masked,
// whichever arm runs first. These shapes are order-independent: the
// separator between them belongs to neither shape.
func TestRedactEmailBesideACredential(t *testing.T) {
	redactEmailEdgeRun(t, []redactEmailEdgeCase{
		{"credential then colon then email", "ghp_ABCDEFGHIJKLMNOPQRSTUVWX12:jane@example.com", "ghp_A…[redacted]:[email]"},
		{"email then colon then credential", "jane@example.com:sk-abcdefghijklmnopqrst", "[email]:sk-ab…[redacted]"},
		{"space separated", "jane@example.com sk-ant-api03-aaaaaaaaaaaaaaaaaaaaaaaa", "[email] sk-an…[redacted]"},
		{"credential inside angle brackets beside email", "<jane@example.com> AKIAIOSFODNN7EXAMPLE", "<[email]> AKIAI…[redacted]"},
	})
}

// Obligation 4: a credential FUSED to an email with no separator overlaps both
// shapes, so the exact output depends on which arm runs first — the contract
// allows either order. What must hold either way: neither the credential's
// tail nor the whole address survives, and the result is a fixed point.
func TestRedactEmailFusedToACredential(t *testing.T) {
	cases := []struct {
		in     string
		leaked []string
	}{
		{"ghp_ABCDEFGHIJKLMNOPQRSTUVWX12@example.com", []string{"ABCDEFGHIJKLMNOPQRSTUVWX12", "ghp_ABCDEFGHIJKLMNOPQRSTUVWX12@example.com"}},
		{"sk-abcdefghijklmnopqrst-jane@example.com", []string{"abcdefghijklmnopqrst", "jane@example.com"}},
		{"xoxd-abcdefghijk%jane@example.com", []string{"abcdefghijk", "jane@example.com"}},
	}
	for _, c := range cases {
		got := redactSecrets(c.in)
		for _, l := range c.leaked {
			if strings.Contains(got, l) {
				t.Errorf("redactSecrets(%q) = %q, leaks %q", c.in, got, l)
			}
		}
		if again := redactSecrets(got); again != got {
			t.Errorf("redactSecrets not idempotent on %q: once %q, twice %q", c.in, got, again)
		}
	}
}

// Obligation 4: idempotence over strings holding several emails, credentials,
// punctuation and the marker itself.
func TestRedactEmailIdempotent(t *testing.T) {
	inputs := []string{
		"",
		"jane@example.com",
		"jane@example.com, bob@example.org; <carol@example.co.uk> \"dave+x@example.net\"",
		"mailto:jane@example.com?cc=bob@example.org",
		"https://app.example.com/invite?email=jane@example.com&ref=bob@example.org",
		"jane@example.com.bob@example.org.",
		"jane@example.com@bob@example.org",
		"[email] jane@example.com [email]",
		"ghp_ABCDEFGHIJKLMNOPQRSTUVWX12:jane@example.com sk-ant-api03-aaaaaaaaaaaaaaaaaaaaaaaa bob@example.org",
		"sk-abcdefghijklmnopqrst-jane@example.com",
		"résumé jane@example.com 👋 bob@example.org — fin",
		"git@github.com:acme/widgets.git and ops+alerts@mail.example.co.uk",
		"jane@example.com\nbob@example.org\n",
	}
	for _, in := range inputs {
		once := redactSecrets(in)
		if twice := redactSecrets(once); twice != once {
			t.Errorf("not idempotent on %q: once %q, twice %q", in, once, twice)
		}
		if strings.Contains(once, "jane@example.com") || strings.Contains(once, "bob@example.org") {
			t.Errorf("redactSecrets(%q) = %q, still carries an address", in, once)
		}
	}
}

// Obligation 6: the reach through ReduceTranscript — an email in a user
// record's text, in an assistant's text, and in a tool call's summary all
// come out as `[email]` in the reduced view.
func TestRedactEmailReachesTheReducedView(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	since := now - int64(time.Hour/time.Millisecond)

	path := mineWrite(t, projects, "-Users-carl-Developer-monolith", "email.jsonl",
		mineUser(t, now-5000, "loop in jane.doe@example.com and <bob@example.org> please"),
		mineAssistant(t, now-4000, "claude-opus-5",
			mineText("noted: ops+alerts@mail.example.co.uk gets the page"),
			mineToolUse("t1", "Bash", map[string]any{"command": "mail -s hi carol@example.net"}),
		),
	)

	out, err := ReduceTranscript(path, since)
	if err != nil {
		t.Fatalf("ReduceTranscript: %v", err)
	}
	got := string(out)
	for _, leaked := range []string{"jane.doe@example.com", "bob@example.org", "ops+alerts@mail.example.co.uk", "carol@example.net"} {
		if strings.Contains(got, leaked) {
			t.Errorf("reduced view leaked %q:\n%s", leaked, got)
		}
	}
	for _, want := range []string{"loop in [email] and <[email]> please", "noted: [email] gets the page", "[email]"} {
		if !strings.Contains(got, want) {
			t.Errorf("reduced view missing %q:\n%s", want, got)
		}
	}
	if strings.Count(got, "[email]") < 4 {
		t.Errorf("reduced view has %d [email] markers, want at least 4:\n%s", strings.Count(got, "[email]"), got)
	}
}

// Obligation 6: the reach through OperatorProse — an email in the operator's
// own prompt comes back as `[email]` in Text, and an email in a neighbouring
// section is masked in the context blocks.
func TestRedactEmailReachesOperatorProse(t *testing.T) {
	projects := mineHome(t)
	since, at := mpcWindow()
	mineWrite(t, projects, mpcPlainProject, "e.jsonl",
		mpcPrompt(t, "e0", "e", at(1), "first question", nil),
		mpcAsst(t, at(2), "the last reply went to bob@example.org"),
		mpcPrompt(t, "e1", "e", at(3), "send it to jane.doe@example.com tonight, cc <carol@example.co.uk>", nil),
		mpcAsst(t, at(4), "sending to jane.doe@example.com"),
	)
	res := mpcRun(t, OperatorOptions{Since: since, Login: mpcLogin, Before: 6, After: 2})
	r := mpcByUUID(t, res, "e1")
	if want := "send it to [email] tonight, cc <[email]>"; r.Text != want {
		t.Errorf("Text = %q, want %q", r.Text, want)
	}
	if len(r.Context.Before) == 0 || len(r.Context.After) == 0 {
		t.Fatalf("context = %+v, want blocks on both sides", r.Context)
	}
	var sawMarker bool
	for _, b := range append(append([]ContextBlock{}, r.Context.Before...), r.Context.After...) {
		for _, leaked := range []string{"bob@example.org", "jane.doe@example.com", "carol@example.co.uk"} {
			if strings.Contains(b.Text, leaked) {
				t.Errorf("context block %+v leaks %q", b, leaked)
			}
		}
		if strings.Contains(b.Text, "[email]") {
			sawMarker = true
		}
	}
	if !sawMarker {
		t.Errorf("no context block carries [email]: before %+v, after %+v", r.Context.Before, r.Context.After)
	}
}
