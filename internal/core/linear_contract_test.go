package core

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kraken/internal/util"
)

// linear_contract_test.go — athena-logic's BLIND contract tests for
// 50-contract.md, Slice 1's `internal/core/linear.go` half. Written from the
// contract alone, with no sight of internal/core/linear.go.
//
// Dimension: LOGIC — stated invariants and happy paths. Boundaries, refusals
// and error paths (non-200, GraphQL `errors` payloads, undecodable JSON, the
// 25 MB download cap) are athena-edge's and are deliberately NOT duplicated
// here.
//
// Every package-level identifier is prefixed `contract`: athena-edge shares
// this package namespace and an unprefixed collision would fail the package.
//
// Round 2: the wire shape the fixtures below serve is no longer a guess — A8
// freezes it. Responses are `data.issues` / `data.comments`, each
// `{ pageInfo { hasNextPage endCursor }, nodes [...] }`; a comment's Author is
// `user.displayName` falling back to `botActor.name`; the `since` floor rides
// as a GraphQL VARIABLE rendered UTC RFC3339.

// ---------------------------------------------------------------------------
// harness
// ---------------------------------------------------------------------------

// contractLinearEnv sandboxes HOME and blanks every LINEAR_* key so a
// developer's real environment and real ~/.kraken/linear.env can never leak
// into an assertion. Returns the sandboxed home.
func contractLinearEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("LINEAR_API_KEY", "")
	t.Setenv("LINEAR_DEFAULT_REPO", "")
	t.Setenv("LINEAR_STATES", "")
	return home
}

// contractWriteLinearEnvFile writes ~/.kraken/linear.env with the given
// contents, 0600 like the asc.env/openai.env siblings it copies.
func contractWriteLinearEnvFile(t *testing.T, contents string) {
	t.Helper()
	dir := filepath.Join(util.Home(), ".kraken")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "linear.env"), []byte(contents), 0o600); err != nil {
		t.Fatalf("write linear.env: %v", err)
	}
}

// contractLinearServer is a Linear stand-in: it hands back scripted response
// bodies in order and records every request's body and headers so the tests
// can assert on the request SHAPE, which is what the contract freezes.
type contractLinearServer struct {
	srv     *httptest.Server
	pages   []string
	bodies  []string
	headers []http.Header
}

func contractNewLinearServer(t *testing.T, pages ...string) *contractLinearServer {
	t.Helper()
	s := &contractLinearServer{pages: pages}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.bodies = append(s.bodies, string(body))
		s.headers = append(s.headers, r.Header.Clone())
		i := len(s.bodies) - 1
		w.Header().Set("Content-Type", "application/json")
		if i >= len(s.pages) {
			// An unscripted request is a test failure, but answering it with
			// a GraphQL error keeps the client on its documented all-or-error
			// path instead of hanging.
			io.WriteString(w, `{"errors":[{"message":"unscripted request"}]}`)
			return
		}
		io.WriteString(w, s.pages[i])
	}))
	t.Cleanup(s.srv.Close)
	return s
}

// contractClientAt builds a client pointed at the scripted server, the way
// the contract says tests reach BaseURL.
func contractClientAt(s *contractLinearServer, key string) *LinearClient {
	c := NewLinearClient(LinearConfig{APIKey: key})
	c.BaseURL = s.srv.URL
	return c
}

func contractPage(field string, hasNext bool, cursor string, nodes ...string) string {
	return fmt.Sprintf(`{"data":{%q:{"nodes":[%s],"pageInfo":{"hasNextPage":%t,"endCursor":%q}}}}`,
		field, strings.Join(nodes, ","), hasNext, cursor)
}

func contractIssueNode(id, identifier, title string) string {
	return fmt.Sprintf(`{"id":%q,"identifier":%q,"title":%q,"description":"",`+
		`"url":"https://linear.app/acme/issue/%s","priority":0,`+
		`"createdAt":"2026-08-01T00:00:00Z","updatedAt":"2026-08-02T00:00:00Z",`+
		`"state":{"name":"Today","type":"started"},"team":{"key":"ENG"},`+
		`"project":null,"labels":{"nodes":[]},"comments":{"nodes":[]}}`,
		id, identifier, title, identifier)
}

func contractCommentNode(id, body, issueID, issueIdentifier string) string {
	return fmt.Sprintf(`{"id":%q,"body":%q,"user":{"displayName":"carl"},"botActor":null,`+
		`"createdAt":"2026-08-02T09:00:00Z","updatedAt":"2026-08-02T10:00:00Z",`+
		`"issue":{"id":%q,"identifier":%q}}`, id, body, issueID, issueIdentifier)
}

// contractVariables decodes the GraphQL envelope's variables object. A8 puts
// the `since` floor and the pagination cursor there rather than inlined in the
// query text.
func contractVariables(t *testing.T, body string) map[string]any {
	t.Helper()
	var env struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("request body is not a GraphQL envelope (%v): %s", err, body)
	}
	if env.Query == "" {
		t.Fatalf("request body carries no query: %s", body)
	}
	return env.Variables
}

// contractHasVarValue reports whether want appears anywhere in the decoded
// variables — top level or nested inside a filter object, since A8 freezes
// that the value rides as a variable without freezing the key path to it.
func contractHasVarValue(v any, want string) bool {
	switch t := v.(type) {
	case string:
		return t == want
	case map[string]any:
		for _, sub := range t {
			if contractHasVarValue(sub, want) {
				return true
			}
		}
	case []any:
		for _, sub := range t {
			if contractHasVarValue(sub, want) {
				return true
			}
		}
	}
	return false
}

func contractMustTime(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return ts
}

// contractSquash removes all whitespace so an assertion on a GraphQL
// fragment ("assignee:{isMe:{eq:true}}") is insensitive to how the query
// string happens to be indented or line-broken.
func contractSquash(s string) string { return strings.Join(strings.Fields(s), "") }

// ---------------------------------------------------------------------------
// LinearEnvPath / LoadLinearConfig
// ---------------------------------------------------------------------------

// Contract: LinearEnvPath is ~/.kraken/linear.env via util.Home() — the
// repo's home-relative rule, never a hardcoded /Users/<name>.
func TestContractLinearEnvPathIsHomeRelative(t *testing.T) {
	home := contractLinearEnv(t)
	want := filepath.Join(home, ".kraken", "linear.env")
	if got := LinearEnvPath(); got != want {
		t.Fatalf("LinearEnvPath() = %q, want %q", got, want)
	}
	if !strings.HasPrefix(LinearEnvPath(), util.Home()) {
		t.Fatalf("LinearEnvPath() = %q is not under util.Home() %q", LinearEnvPath(), util.Home())
	}
}

// Contract: DefaultRepo defaults to "monolith" and States to ["Today"] when
// their keys are absent; an absent file is not an error and an empty APIKey
// is how "feature off" is spelled — there is no error value for it.
func TestContractLoadLinearConfigDefaults(t *testing.T) {
	contractLinearEnv(t) // no file written at all

	cfg := LoadLinearConfig()
	if cfg.APIKey != "" {
		t.Fatalf("APIKey = %q with nothing configured, want empty", cfg.APIKey)
	}
	if cfg.DefaultRepo != "monolith" {
		t.Fatalf("DefaultRepo = %q, want %q", cfg.DefaultRepo, "monolith")
	}
	if len(cfg.States) != 1 || cfg.States[0] != "Today" {
		t.Fatalf("States = %#v, want [\"Today\"]", cfg.States)
	}
}

// Contract: the file is PARSED (never sourced) and supplies all three keys.
func TestContractLoadLinearConfigReadsFile(t *testing.T) {
	contractLinearEnv(t)
	contractWriteLinearEnvFile(t, strings.Join([]string{
		"# a comment line",
		"",
		"LINEAR_API_KEY=lin_api_fromfile",
		"LINEAR_DEFAULT_REPO=kraken",
		"LINEAR_STATES=Today,In Progress",
	}, "\n"))

	cfg := LoadLinearConfig()
	if cfg.APIKey != "lin_api_fromfile" {
		t.Fatalf("APIKey = %q, want the file's value", cfg.APIKey)
	}
	if cfg.DefaultRepo != "kraken" {
		t.Fatalf("DefaultRepo = %q, want %q", cfg.DefaultRepo, "kraken")
	}
	if len(cfg.States) != 2 || cfg.States[0] != "Today" || cfg.States[1] != "In Progress" {
		t.Fatalf("States = %#v, want [Today, In Progress]", cfg.States)
	}
}

// Contract: process env WINS over the file — the openai.env/asc.env
// precedence, and the reason a launchd job can be reconfigured without
// touching the file.
func TestContractLoadLinearConfigProcessEnvWins(t *testing.T) {
	contractLinearEnv(t)
	contractWriteLinearEnvFile(t, strings.Join([]string{
		"LINEAR_API_KEY=lin_api_fromfile",
		"LINEAR_DEFAULT_REPO=fromfile",
		"LINEAR_STATES=FromFile",
	}, "\n"))
	t.Setenv("LINEAR_API_KEY", "lin_api_fromenv")
	t.Setenv("LINEAR_DEFAULT_REPO", "fromenv")
	t.Setenv("LINEAR_STATES", "FromEnv")

	cfg := LoadLinearConfig()
	if cfg.APIKey != "lin_api_fromenv" {
		t.Fatalf("APIKey = %q, want the process env's value", cfg.APIKey)
	}
	if cfg.DefaultRepo != "fromenv" {
		t.Fatalf("DefaultRepo = %q, want the process env's value", cfg.DefaultRepo)
	}
	if len(cfg.States) != 1 || cfg.States[0] != "FromEnv" {
		t.Fatalf("States = %#v, want the process env's value", cfg.States)
	}
}

// Contract: LINEAR_STATES is comma-separated, entries TRIMMED and empties
// DROPPED.
func TestContractLoadLinearConfigStatesTrimAndDropEmpties(t *testing.T) {
	contractLinearEnv(t)
	t.Setenv("LINEAR_API_KEY", "lin_api_x")
	t.Setenv("LINEAR_STATES", " Today , ,In Progress,, Next ")

	cfg := LoadLinearConfig()
	want := []string{"Today", "In Progress", "Next"}
	if len(cfg.States) != len(want) {
		t.Fatalf("States = %#v, want %#v", cfg.States, want)
	}
	for i := range want {
		if cfg.States[i] != want[i] {
			t.Fatalf("States[%d] = %q, want %q (full: %#v)", i, cfg.States[i], want[i], cfg.States)
		}
	}
}

// A10: LINEAR_STATES set but trimming to zero usable entries yields NIL
// States — an explicit empty configuration is honoured, not overridden by the
// ["Today"] default, which fires only when the value is absent or empty.
func TestContractLoadLinearConfigExplicitEmptyStatesIsHonoured(t *testing.T) {
	contractLinearEnv(t)
	t.Setenv("LINEAR_API_KEY", "lin_api_x")
	t.Setenv("LINEAR_STATES", " , ,, ")

	cfg := LoadLinearConfig()
	if len(cfg.States) != 0 {
		t.Fatalf("States = %#v, want nil — an explicit empty configuration is honoured", cfg.States)
	}
}

// Contract: BaseURL defaults to Linear's real endpoint; tests override it.
func TestContractNewLinearClientDefaultBaseURL(t *testing.T) {
	contractLinearEnv(t)
	c := NewLinearClient(LinearConfig{APIKey: "lin_api_x"})
	if c == nil {
		t.Fatal("NewLinearClient returned nil")
	}
	if c.BaseURL != "https://api.linear.app/graphql" {
		t.Fatalf("BaseURL = %q, want https://api.linear.app/graphql", c.BaseURL)
	}
}

// ---------------------------------------------------------------------------
// AssignedIssuesSince — request shape
// ---------------------------------------------------------------------------

// Contract: every request POSTs GraphQL carrying `Authorization: <raw key>`
// (NO "Bearer " — personal API keys take the raw key) and
// `public-file-urls-expire-in: 3600`, which is what makes the media URLs in
// the response pre-signed. The filter is `assignee: { isMe: { eq: true } }`.
func TestContractAssignedIssuesSinceRequestShape(t *testing.T) {
	contractLinearEnv(t)
	s := contractNewLinearServer(t, contractPage("issues", false, "",
		contractIssueNode("uuid-1", "ENG-1", "first")))
	c := contractClientAt(s, "lin_api_rawkey")

	if _, err := c.AssignedIssuesSince(contractMustTime(t, "2026-08-01T00:00:00Z")); err != nil {
		t.Fatalf("AssignedIssuesSince: %v", err)
	}
	if len(s.headers) != 1 {
		t.Fatalf("made %d requests, want 1", len(s.headers))
	}
	h := s.headers[0]
	if got := h.Get("Authorization"); got != "lin_api_rawkey" {
		t.Fatalf("Authorization = %q, want the raw key with no scheme prefix", got)
	}
	if got := h.Get("public-file-urls-expire-in"); got != "3600" {
		t.Fatalf("public-file-urls-expire-in = %q, want %q", got, "3600")
	}
	if got := contractSquash(s.bodies[0]); !strings.Contains(got, "isMe") {
		t.Fatalf("request body carries no isMe filter: %s", s.bodies[0])
	}
}

// A8: the `since` floor rides as a GraphQL VARIABLE, rendered UTC RFC3339 —
// not inlined into the query text and not in some local zone.
func TestContractAssignedIssuesSinceCarriesSinceAsUTCVariable(t *testing.T) {
	contractLinearEnv(t)
	s := contractNewLinearServer(t, contractPage("issues", false, "",
		contractIssueNode("uuid-1", "ENG-1", "first")))
	c := contractClientAt(s, "lin_api_rawkey")

	// A non-UTC input, so a client that forgot .UTC() renders a different string.
	since := contractMustTime(t, "2026-08-01T12:34:56Z").In(time.FixedZone("plus7", 7*3600))
	if _, err := c.AssignedIssuesSince(since); err != nil {
		t.Fatalf("AssignedIssuesSince: %v", err)
	}
	want := since.UTC().Format(time.RFC3339)
	vars := contractVariables(t, s.bodies[0])
	if !contractHasVarValue(vars, want) {
		t.Fatalf("no variable carries the UTC RFC3339 since %q; variables = %#v (body: %s)",
			want, vars, s.bodies[0])
	}
	if got := contractSquash(s.bodies[0]); !strings.Contains(got, "updatedAt") {
		t.Fatalf("query does not mention the updatedAt filter: %s", s.bodies[0])
	}
}

// A8: pagination's `after` also rides as a variable, and only from page 2 —
// page 1 asks with no cursor at all.
func TestContractAssignedIssuesSinceCarriesCursorAsVariable(t *testing.T) {
	contractLinearEnv(t)
	s := contractNewLinearServer(t,
		contractPage("issues", true, "cursor-1", contractIssueNode("uuid-1", "ENG-1", "one")),
		contractPage("issues", false, "", contractIssueNode("uuid-2", "ENG-2", "two")),
	)
	c := contractClientAt(s, "lin_api_rawkey")

	if _, err := c.AssignedIssuesSince(contractMustTime(t, "2026-08-01T00:00:00Z")); err != nil {
		t.Fatalf("AssignedIssuesSince: %v", err)
	}
	if len(s.bodies) != 2 {
		t.Fatalf("made %d requests, want 2", len(s.bodies))
	}
	if contractHasVarValue(contractVariables(t, s.bodies[0]), "cursor-1") {
		t.Fatalf("page 1 already carried a cursor: %s", s.bodies[0])
	}
	if !contractHasVarValue(contractVariables(t, s.bodies[1]), "cursor-1") {
		t.Fatalf("page 2 does not carry page 1's endCursor as a variable: %s", s.bodies[1])
	}
}

// Contract: EVERY request carries the two headers — the media header on a
// later page is what keeps that page's URLs pre-signed too.
func TestContractAssignedIssuesSinceHeadersOnEveryPage(t *testing.T) {
	contractLinearEnv(t)
	s := contractNewLinearServer(t,
		contractPage("issues", true, "cursor-1", contractIssueNode("uuid-1", "ENG-1", "one")),
		contractPage("issues", false, "", contractIssueNode("uuid-2", "ENG-2", "two")),
	)
	c := contractClientAt(s, "lin_api_rawkey")

	if _, err := c.AssignedIssuesSince(contractMustTime(t, "2026-08-01T00:00:00Z")); err != nil {
		t.Fatalf("AssignedIssuesSince: %v", err)
	}
	if len(s.headers) != 2 {
		t.Fatalf("made %d requests, want 2 (one per page)", len(s.headers))
	}
	for i, h := range s.headers {
		if got := h.Get("Authorization"); got != "lin_api_rawkey" {
			t.Fatalf("request %d Authorization = %q, want the raw key", i, got)
		}
		if got := h.Get("public-file-urls-expire-in"); got != "3600" {
			t.Fatalf("request %d public-file-urls-expire-in = %q, want 3600", i, got)
		}
	}
}

// Contract: Relay pagination is followed to exhaustion and a page boundary
// must not drop issues — three issues split 2/1 across two pages come back as
// three, in order, and the second request carries the first page's endCursor.
func TestContractAssignedIssuesSincePaginatesWithoutDropping(t *testing.T) {
	contractLinearEnv(t)
	s := contractNewLinearServer(t,
		contractPage("issues", true, "cursor-1",
			contractIssueNode("uuid-1", "ENG-1", "one"),
			contractIssueNode("uuid-2", "ENG-2", "two")),
		contractPage("issues", false, "",
			contractIssueNode("uuid-3", "ENG-3", "three")),
	)
	c := contractClientAt(s, "lin_api_rawkey")

	got, err := c.AssignedIssuesSince(contractMustTime(t, "2026-08-01T00:00:00Z"))
	if err != nil {
		t.Fatalf("AssignedIssuesSince: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d issues across two pages, want 3: %+v", len(got), got)
	}
	for i, want := range []string{"uuid-1", "uuid-2", "uuid-3"} {
		if got[i].ID != want {
			t.Fatalf("issue %d ID = %q, want %q (order must survive pagination)", i, got[i].ID, want)
		}
	}
	if len(s.bodies) != 2 {
		t.Fatalf("made %d requests, want exactly 2 (stop once hasNextPage is false)", len(s.bodies))
	}
	if !strings.Contains(s.bodies[1], "cursor-1") {
		t.Fatalf("second request did not carry page 1's endCursor: %s", s.bodies[1])
	}
}

// Contract: a single page with hasNextPage false is one request — the client
// must not keep asking past the end.
func TestContractAssignedIssuesSinceStopsAtLastPage(t *testing.T) {
	contractLinearEnv(t)
	s := contractNewLinearServer(t, contractPage("issues", false, "",
		contractIssueNode("uuid-1", "ENG-1", "only")))
	c := contractClientAt(s, "lin_api_rawkey")

	got, err := c.AssignedIssuesSince(contractMustTime(t, "2026-08-01T00:00:00Z"))
	if err != nil {
		t.Fatalf("AssignedIssuesSince: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d issues, want 1", len(got))
	}
	if len(s.bodies) != 1 {
		t.Fatalf("made %d requests for a single non-paginated page, want 1", len(s.bodies))
	}
}

// Contract: LinearIssue's fields — ID is the UUID, Identifier the human key,
// Description the markdown verbatim, plus state name/type, team key, project,
// priority, labels and the issue's comments.
func TestContractAssignedIssuesSinceDecodesIssueFields(t *testing.T) {
	contractLinearEnv(t)
	node := `{"id":"uuid-142","identifier":"ENG-142","title":"Ship the thing",` +
		`"description":"line one\n\nline two","url":"https://linear.app/acme/issue/ENG-142",` +
		`"priority":2,"createdAt":"2026-08-01T08:00:00Z","updatedAt":"2026-08-02T09:30:00Z",` +
		`"state":{"name":"Today","type":"started"},"team":{"key":"ENG"},` +
		`"project":{"name":"Kraken"},"labels":{"nodes":[{"name":"bug"},{"name":"p1"}]},` +
		`"comments":{"nodes":[` +
		`{"id":"cmt-1","body":"first comment","user":{"displayName":"carl"},"botActor":null,` +
		`"createdAt":"2026-08-02T09:00:00Z","updatedAt":"2026-08-02T09:05:00Z"}]}}`
	s := contractNewLinearServer(t, contractPage("issues", false, "", node))
	c := contractClientAt(s, "lin_api_rawkey")

	got, err := c.AssignedIssuesSince(contractMustTime(t, "2026-08-01T00:00:00Z"))
	if err != nil {
		t.Fatalf("AssignedIssuesSince: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d issues, want 1", len(got))
	}
	iss := got[0]
	if iss.ID != "uuid-142" {
		t.Fatalf("ID = %q, want the issue UUID", iss.ID)
	}
	if iss.Identifier != "ENG-142" {
		t.Fatalf("Identifier = %q, want ENG-142", iss.Identifier)
	}
	if iss.Title != "Ship the thing" {
		t.Fatalf("Title = %q", iss.Title)
	}
	if iss.Description != "line one\n\nline two" {
		t.Fatalf("Description = %q, want the markdown verbatim", iss.Description)
	}
	if iss.URL != "https://linear.app/acme/issue/ENG-142" {
		t.Fatalf("URL = %q", iss.URL)
	}
	if iss.StateName != "Today" || iss.StateType != "started" {
		t.Fatalf("state = %q/%q, want Today/started", iss.StateName, iss.StateType)
	}
	if iss.TeamKey != "ENG" {
		t.Fatalf("TeamKey = %q, want ENG", iss.TeamKey)
	}
	if iss.ProjectName != "Kraken" {
		t.Fatalf("ProjectName = %q, want Kraken", iss.ProjectName)
	}
	if iss.Priority != 2 {
		t.Fatalf("Priority = %v, want 2", iss.Priority)
	}
	if len(iss.Labels) != 2 || iss.Labels[0] != "bug" || iss.Labels[1] != "p1" {
		t.Fatalf("Labels = %#v, want [bug p1]", iss.Labels)
	}
	if !iss.CreatedAt.Equal(contractMustTime(t, "2026-08-01T08:00:00Z")) {
		t.Fatalf("CreatedAt = %v", iss.CreatedAt)
	}
	if !iss.UpdatedAt.Equal(contractMustTime(t, "2026-08-02T09:30:00Z")) {
		t.Fatalf("UpdatedAt = %v", iss.UpdatedAt)
	}
	if len(iss.Comments) != 1 {
		t.Fatalf("got %d comments on the issue, want 1 (each issue carries its comments)", len(iss.Comments))
	}
	if iss.Comments[0].Body != "first comment" {
		t.Fatalf("comment body = %q", iss.Comments[0].Body)
	}
	if iss.Comments[0].Author != "carl" {
		t.Fatalf("comment Author = %q, want the display name carl", iss.Comments[0].Author)
	}
}

// ---------------------------------------------------------------------------
// AssignedCommentsSince
// ---------------------------------------------------------------------------

// Contract: the comment fetch exists because a comment does not reliably bump
// its parent issue's updatedAt — so it carries the same auth headers, is
// paginated to exhaustion, and each comment names its parent issue.
func TestContractAssignedCommentsSincePaginatesAndCarriesParent(t *testing.T) {
	contractLinearEnv(t)
	s := contractNewLinearServer(t,
		contractPage("comments", true, "cursor-1",
			contractCommentNode("cmt-1", "one", "uuid-1", "ENG-1")),
		contractPage("comments", false, "",
			contractCommentNode("cmt-2", "two", "uuid-1", "ENG-1"),
			contractCommentNode("cmt-3", "three", "uuid-2", "ENG-2")),
	)
	c := contractClientAt(s, "lin_api_rawkey")

	got, err := c.AssignedCommentsSince(contractMustTime(t, "2026-08-01T00:00:00Z"))
	if err != nil {
		t.Fatalf("AssignedCommentsSince: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d comments across two pages, want 3", len(got))
	}
	for i, want := range []string{"cmt-1", "cmt-2", "cmt-3"} {
		if got[i].ID != want {
			t.Fatalf("comment %d ID = %q, want %q", i, got[i].ID, want)
		}
	}
	if got[0].IssueID != "uuid-1" || got[0].IssueIdentifier != "ENG-1" {
		t.Fatalf("comment 0 parent = %q/%q, want uuid-1/ENG-1", got[0].IssueID, got[0].IssueIdentifier)
	}
	if got[2].IssueID != "uuid-2" {
		t.Fatalf("comment 2 parent = %q, want uuid-2", got[2].IssueID)
	}
	if got[0].Author != "carl" {
		t.Fatalf("Author = %q, want the display name", got[0].Author)
	}
	if !got[0].UpdatedAt.Equal(contractMustTime(t, "2026-08-02T10:00:00Z")) {
		t.Fatalf("UpdatedAt = %v", got[0].UpdatedAt)
	}
	if len(s.headers) != 2 {
		t.Fatalf("made %d requests, want 2", len(s.headers))
	}
	for i, h := range s.headers {
		if h.Get("Authorization") != "lin_api_rawkey" {
			t.Fatalf("request %d Authorization = %q, want the raw key", i, h.Get("Authorization"))
		}
		if h.Get("public-file-urls-expire-in") != "3600" {
			t.Fatalf("request %d missing the media header", i)
		}
	}
	if got := contractSquash(s.bodies[0]); !strings.Contains(got, "isMe") {
		t.Fatalf("comment request body carries no isMe filter: %s", s.bodies[0])
	}
}

// A8: Author is `user.displayName`, FALLING BACK to `botActor.name` — a
// comment posted by an integration has no user at all.
func TestContractCommentAuthorFallsBackToBotActor(t *testing.T) {
	contractLinearEnv(t)
	bot := `{"id":"cmt-bot","body":"deploy finished","user":null,` +
		`"botActor":{"name":"GitHub"},` +
		`"createdAt":"2026-08-02T09:00:00Z","updatedAt":"2026-08-02T10:00:00Z",` +
		`"issue":{"id":"uuid-1","identifier":"ENG-1"}}`
	s := contractNewLinearServer(t, contractPage("comments", false, "",
		bot, contractCommentNode("cmt-human", "a human comment", "uuid-1", "ENG-1")))
	c := contractClientAt(s, "lin_api_rawkey")

	got, err := c.AssignedCommentsSince(contractMustTime(t, "2026-08-01T00:00:00Z"))
	if err != nil {
		t.Fatalf("AssignedCommentsSince: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d comments, want 2", len(got))
	}
	if got[0].Author != "GitHub" {
		t.Fatalf("bot comment Author = %q, want the botActor name GitHub", got[0].Author)
	}
	if got[1].Author != "carl" {
		t.Fatalf("human comment Author = %q, want the user displayName carl", got[1].Author)
	}
}

// ---------------------------------------------------------------------------
// DownloadFile
// ---------------------------------------------------------------------------

// Contract: a plain GET of an absolute, already-pre-signed URL returning body
// bytes and the response Content-Type — and carrying NO Authorization header,
// which is the whole point of the public-file-urls-expire-in request header
// (it also sidesteps Go's Authorization-stripping across a cross-host
// redirect).
func TestContractDownloadFileReturnsBytesAndTypeWithoutAuth(t *testing.T) {
	contractLinearEnv(t)
	payload := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 1, 2, 3}
	var sawAuth string
	var sawMethod string
	var sawHeaderSet bool
	fs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawMethod = r.Method
		sawAuth = r.Header.Get("Authorization")
		_, sawHeaderSet = r.Header["Authorization"]
		w.Header().Set("Content-Type", "image/png")
		w.Write(payload)
	}))
	t.Cleanup(fs.Close)

	c := NewLinearClient(LinearConfig{APIKey: "lin_api_rawkey"})
	body, ctype, err := c.DownloadFile(fs.URL + "/uploads/shot.png")
	if err != nil {
		t.Fatalf("DownloadFile: %v", err)
	}
	if string(body) != string(payload) {
		t.Fatalf("body = %v, want the served bytes %v", body, payload)
	}
	if !strings.HasPrefix(ctype, "image/png") {
		t.Fatalf("content type = %q, want image/png", ctype)
	}
	if sawMethod != http.MethodGet {
		t.Fatalf("method = %s, want GET", sawMethod)
	}
	if sawHeaderSet || sawAuth != "" {
		t.Fatalf("file fetch carried Authorization %q — it must be a plain GET", sawAuth)
	}
}
