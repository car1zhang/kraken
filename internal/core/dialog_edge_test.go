package core

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// Edge-dimension tests for the Jev dialog parser and the TypeSafe client,
// authored blind from 50-contract.md (slice A). Every identifier is prefixed
// `edge` so these files can share the package with athena-logic's.

const edgeThemePane = ` Let's get started.
 Choose the text style that looks best with your terminal
 To change this later, run /theme

     Auto (match terminal)
 ❯ ✔ Dark mode
     Light mode
     Dark mode (colorblind-friendly)
     Light mode (colorblind-friendly)
     Dark mode (ANSI colors only)
     Light mode (ANSI colors only)

 ╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌
  1  function greet() {`

const edgeLoginPane = ` Claude Code can be used with your Claude subscription or billed based on API usage through your Console account.
 Select login method:
 ❯ 1. Claude account with subscription · Pro, Max, Team, or Enterprise
   2. Anthropic Console account · API usage billing
   3. 3rd-party platform · Amazon Bedrock, Microsoft Foundry, Google Vertex AI`

const edgePastePane = ` Browser didn't open? Use the url below to sign in:

 https://claude.ai/oauth/authorize?code=true&client_id=9d1c250a-e61b-44d9-88ed-5944d1962f5e&response_type=code&redirect_uri=https%3A%2F%2Fconsole.anthropic.com%2Foauth%2Fcode%2Fcallback&scope=org%3Acreate_api_key+user%3Aprofile

 Paste code here if prompted >
`

const edgeCodexTrustPane = "Do you trust the contents of this directory?\n\n› 1. Yes, continue\n  2. No, quit\n"

func edgeFiller(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "log output line %d\n", i)
	}
	return b.String()
}

func edgeTexts(d Dialog) []string {
	out := make([]string, 0, len(d.Options))
	for _, o := range d.Options {
		out = append(out, o.Text)
	}
	return out
}

// edgeRefused asserts ok=false and a zero Dialog (ParseDialog obligation).
func edgeRefused(t *testing.T, name, pane string) {
	t.Helper()
	d, ok := ParseDialog(pane)
	if ok {
		t.Errorf("%s: ParseDialog ok=true, want false; got %+v", name, d)
	}
	if !reflect.DeepEqual(d, Dialog{}) {
		t.Errorf("%s: refused parse must return a zero Dialog, got %+v", name, d)
	}
}

func TestDialogEdgeRefusals(t *testing.T) {
	cases := []struct{ name, pane string }{
		{"empty", ""},
		{"whitespace only", "   \n\n\t\n  "},
		{"no cursor glyph, numbered list", "Pick one:\n  1. alpha\n  2. beta\n  3. gamma"},
		{"cursor with no neighbour", "Pick one:\n ❯ lonely option\n\n some unrelated text"},
		{"cursor alone at bottom", "Pick one:\n❯ only option"},
		{"cursor neighbour only across blank line", "❯ 1. alpha\n\n  2. beta"},
		{"cursor followed by a non-matching footer only", "Pick one:\n❯ alone here\nfooter text at column zero"},
		{"two cursors in the last 25 lines", "❯ 1. alpha\n  2. beta\n\nmore text\n› 1. gamma\n  2. delta"},
		{"two cursors, one is a > with text", "> first choice\n  second choice\n\n❯ 1. third\n  2. fourth"},
		{"bare > with nothing after it", " >\n  1. alpha\n  2. beta"},
		{"> followed only by spaces", " >    \n  1. alpha\n  2. beta"},
		{"login paste screen is not a dialog", edgePastePane},
		{"dialog older than 25 lines", edgeLoginPane + "\n" + edgeFiller(30)},
	}
	for _, c := range cases {
		edgeRefused(t, c.name, c.pane)
	}
}

func TestDialogEdgeBottomAnchorPicksTheRecentDialog(t *testing.T) {
	// an old dialog with its own cursor sits far above the last 25 lines; it
	// must neither count as a second cursor nor be the one parsed.
	old := "Old prompt:\n❯ 1. stale alpha\n  2. stale beta\n"
	pane := old + edgeFiller(40) + edgeCodexTrustPane
	d, ok := ParseDialog(pane)
	if !ok {
		t.Fatalf("ParseDialog refused a dialog at the bottom whose sibling is >25 lines up")
	}
	want := []string{"Yes, continue", "No, quit"}
	if got := edgeTexts(d); !reflect.DeepEqual(got, want) {
		t.Fatalf("options = %q, want %q", got, want)
	}
}

func TestDialogEdgeLineIndexesTheFullText(t *testing.T) {
	// Line is the 0-based index in the text handed to ParseDialog, not in the
	// 25-line tail it reads.
	pane := edgeFiller(40) + edgeLoginPane
	d, ok := ParseDialog(pane)
	if !ok {
		t.Fatalf("ParseDialog refused the login picker under 40 filler lines")
	}
	if len(d.Options) != 3 {
		t.Fatalf("want 3 options, got %d: %q", len(d.Options), edgeTexts(d))
	}
	for i, want := range []int{42, 43, 44} {
		if d.Options[i].Line != want {
			t.Errorf("Options[%d].Line = %d, want %d", i, d.Options[i].Line, want)
		}
	}
}

func TestDialogEdgeCodexShape(t *testing.T) {
	d, ok := ParseDialog(edgeCodexTrustPane)
	if !ok {
		t.Fatalf("codex trust dialog did not parse")
	}
	if got, want := edgeTexts(d), []string{"Yes, continue", "No, quit"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("options = %q, want %q", got, want)
	}
	if d.Cursor != 0 || !d.Options[0].Cursor || d.Options[1].Cursor {
		t.Errorf("cursor: d.Cursor=%d opts=%+v", d.Cursor, d.Options)
	}
	if d.Options[0].Number != 1 || d.Options[1].Number != 2 {
		t.Errorf("numbers = %d,%d want 1,2", d.Options[0].Number, d.Options[1].Number)
	}
	if d.Header != "Do you trust the contents of this directory?" {
		t.Errorf("header = %q", d.Header)
	}
}

func TestDialogEdgeThemePickerStripsCheckAndKeepsCodeOut(t *testing.T) {
	d, ok := ParseDialog(edgeThemePane)
	if !ok {
		t.Fatalf("theme picker did not parse")
	}
	want := []string{
		"Auto (match terminal)",
		"Dark mode",
		"Light mode",
		"Dark mode (colorblind-friendly)",
		"Light mode (colorblind-friendly)",
		"Dark mode (ANSI colors only)",
		"Light mode (ANSI colors only)",
	}
	if got := edgeTexts(d); !reflect.DeepEqual(got, want) {
		t.Fatalf("options = %q, want %q", got, want)
	}
	if d.Cursor != 1 {
		t.Errorf("Cursor = %d, want 1", d.Cursor)
	}
	n := 0
	for i, o := range d.Options {
		if o.Cursor {
			n++
		}
		if o.Number != 0 {
			t.Errorf("Options[%d].Number = %d, want 0 (un-numbered)", i, o.Number)
		}
		if strings.ContainsAny(o.Text, "❯✔") {
			t.Errorf("Options[%d].Text kept a glyph: %q", i, o.Text)
		}
	}
	if n != 1 {
		t.Errorf("%d options carry Cursor, want exactly 1", n)
	}
	for _, h := range []string{"Let's get started.", "Choose the text style that looks best with your terminal", "To change this later, run /theme"} {
		if !strings.Contains(d.Header, h) {
			t.Errorf("header %q missing %q", d.Header, h)
		}
	}
}

func TestDialogEdgeTrustShapeCursorOnFirst(t *testing.T) {
	d, ok := ParseDialog(" Do you trust the files in this folder?\n\n ❯ No, exit\n   Yes, I trust this folder\n")
	if !ok {
		t.Fatalf("trust dialog did not parse")
	}
	if got, want := edgeTexts(d), []string{"No, exit", "Yes, I trust this folder"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("options = %q, want %q", got, want)
	}
	if d.Cursor != 0 {
		t.Errorf("Cursor = %d, want 0", d.Cursor)
	}
}

func TestDialogEdgeCursorOnLastOption(t *testing.T) {
	d, ok := ParseDialog("Choose:\n  1. alpha\n  2. beta\n❯ 3. gamma")
	if !ok {
		t.Fatalf("cursor on the last numbered option did not parse")
	}
	if got, want := edgeTexts(d), []string{"alpha", "beta", "gamma"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("options = %q, want %q", got, want)
	}
	if d.Cursor != 2 || d.Options[2].Number != 3 {
		t.Errorf("Cursor=%d Number=%d, want 2 and 3", d.Cursor, d.Options[2].Number)
	}
}

func TestDialogEdgeGreaterThanGlyphWithText(t *testing.T) {
	d, ok := ParseDialog("Continue?\n> Yes\n  No")
	if !ok {
		t.Fatalf("`>` cursor followed by text must count as a cursor")
	}
	if got, want := edgeTexts(d), []string{"Yes", "No"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("options = %q, want %q", got, want)
	}
	if d.Cursor != 0 {
		t.Errorf("Cursor = %d, want 0", d.Cursor)
	}
}

func TestDialogEdgeBlankLineEndsTheRun(t *testing.T) {
	d, ok := ParseDialog("Pick:\n❯ 1. one\n  2. two\n\n  3. three")
	if !ok {
		t.Fatalf("did not parse")
	}
	if got, want := edgeTexts(d), []string{"one", "two"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("options = %q, want %q (blank line ends the run)", got, want)
	}
}

func TestDialogEdgeTrailingFooterNotAnOption(t *testing.T) {
	d, ok := ParseDialog("Pick:\n❯ 1. one\n  2. two\nEsc to cancel")
	if !ok {
		t.Fatalf("did not parse")
	}
	if got, want := edgeTexts(d), []string{"one", "two"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("options = %q, want %q", got, want)
	}
}

func TestDialogEdgeHeaderWindowAndLetterRule(t *testing.T) {
	pane := strings.Join([]string{
		"edgeOutsideWindow", // 9 lines above the first option: outside the window
		"Top title",
		"▐▛███▜▌",
		"ab",
		"",
		"──────",
		"x y z",
		"Pick one:",
		"*",
		"❯ Yes",
		"  No",
	}, "\n")
	d, ok := ParseDialog(pane)
	if !ok {
		t.Fatalf("did not parse")
	}
	if want := "Top title\nx y z\nPick one:"; d.Header != want {
		t.Fatalf("Header = %q, want %q", d.Header, want)
	}
}

func TestDialogEdgeKeys(t *testing.T) {
	three := Dialog{Cursor: 2, Options: []DialogOption{{Text: "a"}, {Text: "b"}, {Text: "c", Cursor: true}}}
	numbered := Dialog{Cursor: -1, Options: []DialogOption{{Text: "a", Number: 1}, {Text: "b", Number: 12}}}
	plain := Dialog{Cursor: -1, Options: []DialogOption{{Text: "a"}, {Text: "b"}}}
	cases := []struct {
		name string
		d    Dialog
		pick int
		want []string
	}{
		{"up twice", three, 0, []string{"Up", "Up", "Enter"}},
		{"up once", three, 1, []string{"Up", "Enter"}},
		{"on cursor", three, 2, []string{"Enter"}},
		{"pick negative", three, -1, nil},
		{"pick == len", three, 3, nil},
		{"pick far out", three, 99, nil},
		{"no cursor numbered", numbered, 1, []string{"12"}},
		{"no cursor numbered first", numbered, 0, []string{"1"}},
		{"no cursor numbered out of range", numbered, 2, nil},
		{"no cursor un-numbered", plain, 0, nil},
		{"empty dialog", Dialog{Cursor: -1}, 0, nil},
		{"empty dialog with cursor 0", Dialog{}, 0, nil},
	}
	for _, c := range cases {
		got := DialogKeys(c.d, c.pick)
		if c.want == nil {
			if got != nil {
				t.Errorf("%s: DialogKeys = %q, want nil", c.name, got)
			}
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: DialogKeys = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestDialogEdgeKeysPure(t *testing.T) {
	d := Dialog{Cursor: 0, Options: []DialogOption{{Text: "a", Cursor: true}, {Text: "b"}}}
	before := Dialog{Cursor: 0, Options: []DialogOption{{Text: "a", Cursor: true}, {Text: "b"}}}
	_ = DialogKeys(d, 1)
	_ = DialogKeys(d, 1)
	if !reflect.DeepEqual(d, before) {
		t.Fatalf("DialogKeys mutated its input: %+v", d)
	}
}

// ---- LoadTypeSafeConfig ----

func edgeIsolateTypeSafe(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TYPESAFE_API_KEY", "")
	t.Setenv("TYPESAFE_MODEL", "")
	return home
}

func edgeWriteEnv(t *testing.T, home, body string) {
	t.Helper()
	dir := filepath.Join(home, ".kraken")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "typesafe.env"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDialogEdgeTypeSafeEnvPath(t *testing.T) {
	home := edgeIsolateTypeSafe(t)
	if got, want := TypeSafeEnvPath(), filepath.Join(home, ".kraken", "typesafe.env"); got != want {
		t.Fatalf("TypeSafeEnvPath = %q, want %q", got, want)
	}
}

func TestDialogEdgeConfigMissingFile(t *testing.T) {
	edgeIsolateTypeSafe(t)
	got := LoadTypeSafeConfig()
	if got.APIKey != "" || got.Model != "jev-latest" {
		t.Fatalf("missing file: got %+v, want empty key and jev-latest", got)
	}
}

func TestDialogEdgeConfigFileIsADirectory(t *testing.T) {
	home := edgeIsolateTypeSafe(t)
	if err := os.MkdirAll(filepath.Join(home, ".kraken", "typesafe.env"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TYPESAFE_API_KEY", "edge-env-key")
	got := LoadTypeSafeConfig()
	if got.APIKey != "edge-env-key" || got.Model != "jev-latest" {
		t.Fatalf("unreadable file: got %+v, want env-only view", got)
	}
}

func TestDialogEdgeConfigUnreadableFile(t *testing.T) {
	home := edgeIsolateTypeSafe(t)
	edgeWriteEnv(t, home, "TYPESAFE_API_KEY=edge-file-key\n")
	p := filepath.Join(home, ".kraken", "typesafe.env")
	if err := os.Chmod(p, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(p, 0o600) })
	if f, err := os.Open(p); err == nil {
		f.Close()
		t.Skip("running with privileges that read a 0000 file")
	}
	got := LoadTypeSafeConfig()
	if got.APIKey != "" || got.Model != "jev-latest" {
		t.Fatalf("unreadable file: got %+v, want env-only view", got)
	}
}

func TestDialogEdgeConfigMalformedFile(t *testing.T) {
	home := edgeIsolateTypeSafe(t)
	edgeWriteEnv(t, home, "\x00\x01\xff\xfe garbage with no equals sign\n}{][\n")
	got := LoadTypeSafeConfig()
	if got.APIKey != "" || got.Model != "jev-latest" {
		t.Fatalf("malformed file: got %+v, want env-only view", got)
	}
}

func TestDialogEdgeConfigParsing(t *testing.T) {
	home := edgeIsolateTypeSafe(t)
	edgeWriteEnv(t, home, strings.Join([]string{
		"# a comment",
		"# TYPESAFE_API_KEY=commented-out",
		"OTHER_KEY=ignored",
		"TYPESAFE_API_KEYX=not-this-one",
		`export TYPESAFE_API_KEY="edge-quoted-key"`,
		"TYPESAFE_MODEL='edge-model-1'",
		"",
	}, "\n"))
	got := LoadTypeSafeConfig()
	if got.APIKey != "edge-quoted-key" {
		t.Errorf("APIKey = %q, want edge-quoted-key (export prefix and quotes stripped)", got.APIKey)
	}
	if got.Model != "edge-model-1" {
		t.Errorf("Model = %q, want edge-model-1 (single quotes stripped)", got.Model)
	}
}

func TestDialogEdgeConfigCommentedModelDefaults(t *testing.T) {
	home := edgeIsolateTypeSafe(t)
	edgeWriteEnv(t, home, "TYPESAFE_API_KEY=k1\n# TYPESAFE_MODEL=commented\n")
	got := LoadTypeSafeConfig()
	if got.APIKey != "k1" || got.Model != "jev-latest" {
		t.Fatalf("got %+v, want key k1 and default model", got)
	}
}

func TestDialogEdgeConfigEnvWinsPerKey(t *testing.T) {
	home := edgeIsolateTypeSafe(t)
	edgeWriteEnv(t, home, "TYPESAFE_API_KEY=edge-file-key\nTYPESAFE_MODEL=edge-file-model\n")
	t.Setenv("TYPESAFE_API_KEY", "edge-env-key")
	got := LoadTypeSafeConfig()
	if got.APIKey != "edge-env-key" {
		t.Errorf("APIKey = %q, want the environment's", got.APIKey)
	}
	if got.Model != "edge-file-model" {
		t.Errorf("Model = %q, want the file's (env left empty)", got.Model)
	}
}

func TestDialogEdgeConfigNeverSourced(t *testing.T) {
	home := edgeIsolateTypeSafe(t)
	marker := filepath.Join(home, "edge-pwned")
	edgeWriteEnv(t, home, "TYPESAFE_API_KEY=$(touch "+marker+")\ntouch "+marker+"\n`touch "+marker+"`\n")
	_ = LoadTypeSafeConfig()
	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("the env file was executed: %s exists", marker)
	}
}

// ---- client ----

func TestDialogEdgeNewClientOff(t *testing.T) {
	if c := NewTypeSafeClient(TypeSafeConfig{}); c != nil {
		t.Fatalf("empty key must return nil (feature off), got %+v", c)
	}
	if c := NewTypeSafeClient(TypeSafeConfig{Model: "m"}); c != nil {
		t.Fatalf("empty key with a model must return nil, got %+v", c)
	}
	if c := NewTypeSafeClient(TypeSafeConfig{APIKey: "k"}); c == nil {
		t.Fatalf("non-empty key must return a client")
	}
}

func TestDialogEdgeNilReceiver(t *testing.T) {
	var c *TypeSafeClient
	_, err := c.SystemOne(SystemOneRequest{Model: "m"})
	if err == nil {
		t.Fatalf("nil receiver must return an error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "not configured") {
		t.Errorf("nil receiver error = %q, want it to say TypeSafe is not configured", err)
	}
}

const edgeKey = "ts-edge-SECRET-0123456789"

func edgeTSClient(t *testing.T, h http.HandlerFunc) *TypeSafeClient {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := NewTypeSafeClient(TypeSafeConfig{APIKey: edgeKey, Model: "jev-latest"})
	if c == nil {
		t.Fatalf("NewTypeSafeClient returned nil for a non-empty key")
	}
	c.BaseURL = srv.URL
	return c
}

func TestDialogEdgeNon200RedactsKey(t *testing.T) {
	for _, code := range []int{400, 401, 422, 500, 503} {
		c := edgeTSClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
			fmt.Fprintf(w, `{"detail":"bad key %s; again %s"}`, edgeKey, edgeKey)
		})
		_, err := c.SystemOne(SystemOneRequest{Model: "m"})
		if err == nil {
			t.Fatalf("status %d: want an error", code)
		}
		msg := err.Error()
		if !strings.Contains(msg, fmt.Sprint(code)) {
			t.Errorf("status %d: error %q does not name the status code", code, msg)
		}
		if strings.Contains(msg, edgeKey) {
			t.Errorf("status %d: error leaks the key: %q", code, msg)
		}
		if strings.Count(msg, "[redacted]") < 2 {
			t.Errorf("status %d: want every key occurrence redacted, got %q", code, msg)
		}
	}
}

func TestDialogEdgeNon200EmptyBody(t *testing.T) {
	c := edgeTSClient(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(422) })
	_, err := c.SystemOne(SystemOneRequest{Model: "m"})
	if err == nil || !strings.Contains(err.Error(), "422") {
		t.Fatalf("empty 422: err = %v, want one naming 422", err)
	}
}

func TestDialogEdgeNon200BodyCappedAt200Runes(t *testing.T) {
	head := strings.Repeat("é", 200)
	c := edgeTSClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		fmt.Fprint(w, head+"EDGETAILBEYOND200")
	})
	_, err := c.SystemOne(SystemOneRequest{Model: "m"})
	if err == nil {
		t.Fatalf("want an error")
	}
	msg := err.Error()
	if !strings.Contains(msg, head) {
		t.Errorf("error should carry the first 200 runes of the body: %q", msg)
	}
	if strings.Contains(msg, "EDGETAILBEYOND200") {
		t.Errorf("error carries body past 200 runes: %q", msg)
	}
	if !utf8.ValidString(msg) {
		t.Errorf("error is not valid UTF-8 (body cut mid-rune)")
	}
}

func TestDialogEdgeNon200Not2xxSuccess(t *testing.T) {
	c := edgeTSClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(201)
		fmt.Fprint(w, `{"model":"m","answers":{},"usage":{"input_tokens":1,"output_tokens":1}}`)
	})
	if _, err := c.SystemOne(SystemOneRequest{Model: "m"}); err == nil {
		t.Fatalf("201 is a non-200 and must be an error")
	}
}

func TestDialogEdgeTransportErrorNoKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()
	c := NewTypeSafeClient(TypeSafeConfig{APIKey: edgeKey})
	c.BaseURL = url
	_, err := c.SystemOne(SystemOneRequest{Model: "m"})
	if err == nil {
		t.Fatalf("closed server: want an error")
	}
	if strings.Contains(err.Error(), edgeKey) {
		t.Fatalf("transport error leaks the key: %q", err)
	}
}

func TestDialogEdgeMalformedJSON(t *testing.T) {
	for _, body := range []string{"", "not json", `{"model":`, `[1,2,3]`} {
		c := edgeTSClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) })
		_, err := c.SystemOne(SystemOneRequest{Model: "m"})
		if err == nil {
			t.Errorf("200 with body %q: want a decode error", body)
		} else if strings.Contains(err.Error(), edgeKey) {
			t.Errorf("decode error leaks the key: %q", err)
		}
	}
}

const edgeValidBody = `{"model":"edge-m","answers":{"is_dialog":{"type":"noul","noul":0.9}},"usage":{"input_tokens":3,"output_tokens":4}}`

func TestDialogEdgeOversizedBody(t *testing.T) {
	// valid JSON at the front, padded past 1 MiB: a decoder that reads one
	// value would succeed, so the size bound has to be its own check.
	padded := edgeValidBody + strings.Repeat(" ", (1<<20)+1-len(edgeValidBody))
	c := edgeTSClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, padded) })
	if _, err := c.SystemOne(SystemOneRequest{Model: "m"}); err == nil {
		t.Fatalf("a %d-byte body (over 1 MiB) must be an error, not a truncated decode", len(padded))
	}

	big := `{"model":"` + strings.Repeat("m", 1<<20) + `","answers":{}}`
	c2 := edgeTSClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, big) })
	if _, err := c2.SystemOne(SystemOneRequest{Model: "m"}); err == nil {
		t.Fatalf("a JSON value over 1 MiB must be an error")
	}
}

func TestDialogEdgeBodyAtExactly1MiB(t *testing.T) {
	exact := edgeValidBody + strings.Repeat(" ", (1<<20)-len(edgeValidBody))
	c := edgeTSClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, exact) })
	resp, err := c.SystemOne(SystemOneRequest{Model: "m"})
	if err != nil {
		t.Fatalf("a body of exactly 1 MiB is not over the limit: %v", err)
	}
	if resp.Model != "edge-m" || resp.Answers["is_dialog"].Noul != 0.9 || resp.Usage.OutputTokens != 4 {
		t.Fatalf("decoded %+v", resp)
	}
}
