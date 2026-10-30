package core

// Blind contract tests (athena-logic) for slice A of the Jev dialog answerer:
// written from artifacts/50-contract.md alone, before any implementation
// existed. Dimension: the stated invariants and happy paths. Boundaries,
// refusals and error paths belong to athena-edge's file.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// contractDlgThemePane is the Claude Code 2.1.314 theme picker, verbatim.
const contractDlgThemePane = ` Let's get started.
 Choose the text style that looks best with your terminal
 To change this later, run /theme

     Auto (match terminal)
 ❯ ✔ Dark mode
     Light mode
     Dark mode (colorblind-friendly)
     Light mode (colorblind-friendly)
     Dark mode (ANSI colors only)
     Light mode (ANSI colors only)

 ╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌
  1  function greet() {
`

// contractDlgLoginPane is the login-method picker, verbatim.
const contractDlgLoginPane = ` Claude Code can be used with your Claude subscription or billed based on API usage through your Console account.
 Select login method:
 ❯ 1. Claude account with subscription · Pro, Max, Team, or Enterprise
   2. Anthropic Console account · API usage billing
   3. 3rd-party platform · Amazon Bedrock, Microsoft Foundry, Google Vertex AI
`

// contractDlgTrustPane is the Claude Code 2.1.304 trust dialog, verbatim.
const contractDlgTrustPane = ` Accessing workspace:
 /Users/x/.kraken/cells/hollow-kraken-609/wt-utils
 Quick safety check: Is this a project you created or one you trust? (Like your own code, a well-known open source project, or work from your team). If not, take a moment to review what's in this folder first.
 Claude Code'll be able to read, edit, and execute files here.
 Security guide
 ❯ No, exit
   Yes, I trust this folder
 Enter to confirm · Esc to cancel
`

// contractDlgCodexPane is codex's trust prompt, verbatim.
const contractDlgCodexPane = "Do you trust the contents of this directory?\n\n› 1. Yes, continue\n  2. No, quit\n"

func contractDlgTexts(d Dialog) []string {
	out := make([]string, 0, len(d.Options))
	for _, o := range d.Options {
		out = append(out, o.Text)
	}
	return out
}

// contractDlgCheckCursor asserts the Dialog's Cursor index and that exactly
// that option (and no other) carries Cursor=true.
func contractDlgCheckCursor(t *testing.T, d Dialog, want int) {
	t.Helper()
	if d.Cursor != want {
		t.Fatalf("Dialog.Cursor = %d, want %d", d.Cursor, want)
	}
	for i, o := range d.Options {
		if o.Cursor != (i == want) {
			t.Errorf("Options[%d].Cursor = %v, want %v", i, o.Cursor, i == want)
		}
	}
}

// contractDlgHeaderInOrder asserts each want line appears in the header, in
// order, and that the header holds exactly len(want) lines.
func contractDlgHeaderInOrder(t *testing.T, header string, want []string) {
	t.Helper()
	lines := strings.Split(header, "\n")
	if len(lines) != len(want) {
		t.Fatalf("Header has %d lines, want %d: %q", len(lines), len(want), header)
	}
	for i, w := range want {
		if strings.TrimSpace(lines[i]) != w {
			t.Errorf("Header line %d = %q, want %q", i, lines[i], w)
		}
	}
}

func TestDialogContractThemePicker(t *testing.T) {
	d, ok := ParseDialog(contractDlgThemePane)
	if !ok {
		t.Fatal("theme picker did not parse")
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
	if got := contractDlgTexts(d); !reflect.DeepEqual(got, want) {
		t.Fatalf("options = %q, want %q", got, want)
	}
	contractDlgCheckCursor(t, d, 1)
	for i, o := range d.Options {
		if o.Number != 0 {
			t.Errorf("Options[%d].Number = %d, want 0 (un-numbered)", i, o.Number)
		}
		if o.Line != 4+i {
			t.Errorf("Options[%d].Line = %d, want %d", i, o.Line, 4+i)
		}
	}
	contractDlgHeaderInOrder(t, d.Header, []string{
		"Let's get started.",
		"Choose the text style that looks best with your terminal",
		"To change this later, run /theme",
	})
}

func TestDialogContractLoginPicker(t *testing.T) {
	d, ok := ParseDialog(contractDlgLoginPane)
	if !ok {
		t.Fatal("login picker did not parse")
	}
	want := []string{
		"Claude account with subscription · Pro, Max, Team, or Enterprise",
		"Anthropic Console account · API usage billing",
		"3rd-party platform · Amazon Bedrock, Microsoft Foundry, Google Vertex AI",
	}
	if got := contractDlgTexts(d); !reflect.DeepEqual(got, want) {
		t.Fatalf("options = %q, want %q", got, want)
	}
	contractDlgCheckCursor(t, d, 0)
	for i, o := range d.Options {
		if o.Number != i+1 {
			t.Errorf("Options[%d].Number = %d, want %d", i, o.Number, i+1)
		}
		if o.Line != 2+i {
			t.Errorf("Options[%d].Line = %d, want %d", i, o.Line, 2+i)
		}
	}
	contractDlgHeaderInOrder(t, d.Header, []string{
		"Claude Code can be used with your Claude subscription or billed based on API usage through your Console account.",
		"Select login method:",
	})
}

func TestDialogContractTrustDialog(t *testing.T) {
	d, ok := ParseDialog(contractDlgTrustPane)
	if !ok {
		t.Fatal("trust dialog did not parse")
	}
	want := []string{"No, exit", "Yes, I trust this folder"}
	if got := contractDlgTexts(d); !reflect.DeepEqual(got, want) {
		t.Fatalf("options = %q, want %q", got, want)
	}
	contractDlgCheckCursor(t, d, 0)
	for i, o := range d.Options {
		if o.Number != 0 {
			t.Errorf("Options[%d].Number = %d, want 0", i, o.Number)
		}
		if o.Line != 5+i {
			t.Errorf("Options[%d].Line = %d, want %d", i, o.Line, 5+i)
		}
	}
	contractDlgHeaderInOrder(t, d.Header, []string{
		"Accessing workspace:",
		"/Users/x/.kraken/cells/hollow-kraken-609/wt-utils",
		"Quick safety check: Is this a project you created or one you trust? (Like your own code, a well-known open source project, or work from your team). If not, take a moment to review what's in this folder first.",
		"Claude Code'll be able to read, edit, and execute files here.",
		"Security guide",
	})
}

func TestDialogContractCodexTrust(t *testing.T) {
	d, ok := ParseDialog(contractDlgCodexPane)
	if !ok {
		t.Fatal("codex trust prompt did not parse")
	}
	want := []string{"Yes, continue", "No, quit"}
	if got := contractDlgTexts(d); !reflect.DeepEqual(got, want) {
		t.Fatalf("options = %q, want %q", got, want)
	}
	contractDlgCheckCursor(t, d, 0)
	if d.Options[0].Number != 1 || d.Options[1].Number != 2 {
		t.Errorf("Numbers = %d,%d, want 1,2", d.Options[0].Number, d.Options[1].Number)
	}
	if d.Options[0].Line != 2 || d.Options[1].Line != 3 {
		t.Errorf("Lines = %d,%d, want 2,3", d.Options[0].Line, d.Options[1].Line)
	}
	contractDlgHeaderInOrder(t, d.Header, []string{"Do you trust the contents of this directory?"})
}

// A terminal rendering neither glyph shows `>`; the dialog parses the same.
func TestDialogContractPlainGreaterThanCursor(t *testing.T) {
	pane := strings.Replace(contractDlgTrustPane, "❯ No, exit", "> No, exit", 1)
	d, ok := ParseDialog(pane)
	if !ok {
		t.Fatal("trust dialog with a `>` cursor did not parse")
	}
	if got := contractDlgTexts(d); !reflect.DeepEqual(got, []string{"No, exit", "Yes, I trust this folder"}) {
		t.Fatalf("options = %q", got)
	}
	contractDlgCheckCursor(t, d, 0)
}

// Bottom-anchored reading, but Line indexes the WHOLE text handed in.
func TestDialogContractLineIndexesWholeText(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 40; i++ {
		b.WriteString("scrollback line of earlier output\n")
	}
	d, ok := ParseDialog(b.String() + contractDlgLoginPane)
	if !ok {
		t.Fatal("login picker under 40 lines of scrollback did not parse")
	}
	if len(d.Options) != 3 {
		t.Fatalf("got %d options, want 3", len(d.Options))
	}
	for i, o := range d.Options {
		if o.Line != 42+i {
			t.Errorf("Options[%d].Line = %d, want %d", i, o.Line, 42+i)
		}
	}
}

func TestDialogContractKeysWithCursor(t *testing.T) {
	d, ok := ParseDialog(contractDlgThemePane)
	if !ok {
		t.Fatal("theme picker did not parse")
	}
	cases := []struct {
		pick int
		want []string
	}{
		{1, []string{"Enter"}},
		{4, []string{"Down", "Down", "Down", "Enter"}},
		{0, []string{"Up", "Enter"}},
		{6, []string{"Down", "Down", "Down", "Down", "Down", "Enter"}},
	}
	for _, c := range cases {
		if got := DialogKeys(d, c.pick); !reflect.DeepEqual(got, c.want) {
			t.Errorf("DialogKeys(theme, %d) = %q, want %q", c.pick, got, c.want)
		}
	}
}

func TestDialogContractKeysWithoutCursor(t *testing.T) {
	numbered := Dialog{Cursor: -1, Options: []DialogOption{
		{Text: "Yes", Line: 0, Number: 1},
		{Text: "No", Line: 1, Number: 2},
	}}
	if got := DialogKeys(numbered, 1); !reflect.DeepEqual(got, []string{"2"}) {
		t.Errorf("DialogKeys(numbered, no cursor, 1) = %q, want [\"2\"]", got)
	}
	if got := DialogKeys(numbered, 0); !reflect.DeepEqual(got, []string{"1"}) {
		t.Errorf("DialogKeys(numbered, no cursor, 0) = %q, want [\"1\"]", got)
	}
	plain := Dialog{Cursor: -1, Options: []DialogOption{
		{Text: "Yes", Line: 0},
		{Text: "No", Line: 1},
	}}
	if got := DialogKeys(plain, 1); got != nil {
		t.Errorf("DialogKeys(un-numbered, no cursor) = %q, want nil", got)
	}
}

func contractDlgIsolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TYPESAFE_API_KEY", "")
	t.Setenv("TYPESAFE_MODEL", "")
	return home
}

func contractDlgWriteEnv(t *testing.T, home, body string) {
	t.Helper()
	dir := filepath.Join(home, ".kraken")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "typesafe.env"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDialogContractEnvPath(t *testing.T) {
	home := contractDlgIsolate(t)
	if got, want := TypeSafeEnvPath(), filepath.Join(home, ".kraken", "typesafe.env"); got != want {
		t.Errorf("TypeSafeEnvPath() = %q, want %q", got, want)
	}
}

func TestDialogContractConfigDefaultModel(t *testing.T) {
	contractDlgIsolate(t)
	if got := LoadTypeSafeConfig(); got != (TypeSafeConfig{APIKey: "", Model: "jev-latest"}) {
		t.Errorf("no sources: %+v, want empty key and jev-latest", got)
	}
	t.Setenv("TYPESAFE_API_KEY", "env-key-1")
	if got := LoadTypeSafeConfig(); got != (TypeSafeConfig{APIKey: "env-key-1", Model: "jev-latest"}) {
		t.Errorf("env key only: %+v", got)
	}
}

func TestDialogContractConfigFromFile(t *testing.T) {
	home := contractDlgIsolate(t)
	contractDlgWriteEnv(t, home, "# TypeSafe credentials\n"+
		"export TYPESAFE_API_KEY=\"file-key-2\"\n"+
		"TYPESAFE_MODEL='jev-file-model'\n"+
		"SOMETHING_ELSE=ignored\n")
	if got := LoadTypeSafeConfig(); got != (TypeSafeConfig{APIKey: "file-key-2", Model: "jev-file-model"}) {
		t.Errorf("file config: %+v", got)
	}
}

func TestDialogContractConfigEnvBeatsFile(t *testing.T) {
	home := contractDlgIsolate(t)
	contractDlgWriteEnv(t, home, "TYPESAFE_API_KEY=file-key\nTYPESAFE_MODEL=file-model\n")
	t.Setenv("TYPESAFE_API_KEY", "env-key")
	t.Setenv("TYPESAFE_MODEL", "env-model")
	if got := LoadTypeSafeConfig(); got != (TypeSafeConfig{APIKey: "env-key", Model: "env-model"}) {
		t.Errorf("env over file: %+v", got)
	}
}

func TestDialogContractNewClient(t *testing.T) {
	if c := NewTypeSafeClient(TypeSafeConfig{APIKey: "", Model: "jev-latest"}); c != nil {
		t.Errorf("empty key: got a client, want nil")
	}
	if c := NewTypeSafeClient(TypeSafeConfig{APIKey: "k", Model: "jev-latest"}); c == nil {
		t.Errorf("key set: got nil, want a client")
	}
}

func TestDialogContractSystemOneRoundTrip(t *testing.T) {
	const key = "sk-contract-secret"
	var sawMethod, sawPath, sawAuth, sawCT string
	var sawBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawMethod, sawPath = r.Method, r.URL.Path
		sawAuth, sawCT = r.Header.Get("Authorization"), r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &sawBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"jev-7","answers":{`+
			`"pick":{"type":"choice","choice":"opt2","confidence":0.83,"probabilities":{"opt1":0.1,"opt2":0.83,"escalate":0.07}},`+
			`"is_dialog":{"type":"noul","noul":0.91}},`+
			`"usage":{"input_tokens":123,"output_tokens":4}}`)
	}))
	defer srv.Close()

	c := NewTypeSafeClient(TypeSafeConfig{APIKey: key, Model: "jev-latest"})
	if c == nil {
		t.Fatal("NewTypeSafeClient returned nil for a set key")
	}
	c.BaseURL = srv.URL
	req := SystemOneRequest{
		Model: "jev-latest",
		State: map[string]any{"directive": "keep going"},
		Questions: map[string]SystemOneQuestion{
			"is_dialog": {Type: "noul", Instructions: "is this a dialog?"},
			"pick":      {Type: "choice", Instructions: "pick one", Criteria: map[string]string{"opt1": "A", "opt2": "B"}},
		},
	}
	resp, err := c.SystemOne(req)
	if err != nil {
		t.Fatalf("SystemOne: %v", err)
	}
	if sawMethod != http.MethodPost || sawPath != "/v1/systemone" {
		t.Errorf("request = %s %s, want POST /v1/systemone", sawMethod, sawPath)
	}
	if sawAuth != "Bearer "+key {
		t.Errorf("Authorization = %q, want Bearer <key>", sawAuth)
	}
	if !strings.HasPrefix(sawCT, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", sawCT)
	}
	if sawBody["model"] != "jev-latest" {
		t.Errorf("body model = %v", sawBody["model"])
	}
	if st, _ := sawBody["state"].(map[string]any); st["directive"] != "keep going" {
		t.Errorf("body state = %v", sawBody["state"])
	}
	qs, _ := sawBody["questions"].(map[string]any)
	pick, _ := qs["pick"].(map[string]any)
	isd, _ := qs["is_dialog"].(map[string]any)
	if pick["type"] != "choice" || isd["type"] != "noul" {
		t.Errorf("body questions = %v", sawBody["questions"])
	}
	if crit, _ := pick["criteria"].(map[string]any); crit["opt2"] != "B" {
		t.Errorf("body pick criteria = %v", pick["criteria"])
	}

	if resp.Model != "jev-7" {
		t.Errorf("resp.Model = %q", resp.Model)
	}
	p := resp.Answers["pick"]
	if p.Type != "choice" || p.Choice != "opt2" || p.Confidence != 0.83 {
		t.Errorf("pick answer = %+v", p)
	}
	if !reflect.DeepEqual(p.Probabilities, map[string]float64{"opt1": 0.1, "opt2": 0.83, "escalate": 0.07}) {
		t.Errorf("pick probabilities = %v", p.Probabilities)
	}
	if a := resp.Answers["is_dialog"]; a.Type != "noul" || a.Noul != 0.91 {
		t.Errorf("is_dialog answer = %+v", a)
	}
	if resp.Usage != (SystemOneUsage{InputTokens: 123, OutputTokens: 4}) {
		t.Errorf("usage = %+v", resp.Usage)
	}
}
