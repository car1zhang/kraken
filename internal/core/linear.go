package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"kraken/internal/util"
)

// linear.go — kraken's read-only client for Linear's GraphQL API: the issues
// assigned to carl, the comments on them, and the media attached to both.
// It is the binary's third outbound API client after embed.go's embedder and
// slack.go's Slack poller, and
// it deliberately copies that file's shape wherever the two have the same
// problem — credentials, error posture, "the key's value never leaves this
// struct".
//
// Stdlib only: net/http + encoding/json, no GraphQL library. A query here is a
// string constant and a variables map, which is all the two queries below
// need; a code generator would buy nothing and cost the repo its dependency
// rule.
//
// Credentials follow embed.go's openai.env pattern exactly, for the same
// launchd reason: `launchctl setenv` does not reach a job whose plist declares
// its own EnvironmentVariables, so a file is the only way these reach
// `kraken serve`. Process env wins over the file (an explicit export in a
// terminal must be able to override for a test), the file is parsed never
// sourced, and the key's VALUE must never reach a log, an error message, an
// event or a job body — only its name, and only to say it is missing.
//
// That last rule is ENFORCED here rather than assumed, because this client has
// a path the embedder does not: it quotes remote text into its errors, and the
// pass dlogs those durably. Every piece of remote-supplied text that enters an
// error goes through safeText, which redacts the configured key before
// truncating. The %w chains are the deliberate exception and they are safe by
// construction: net/http's own errors carry the request URL and never a
// header, and the URL is either c.BaseURL or an attachment link — the key is
// only ever written into a header, so it cannot appear in one.
//
// Failure posture differs from the embedder's in one way worth stating: there
// is no ErrNoLinear sentinel. An empty APIKey is simply "the feature is off",
// which the pass reads as a silent return — no error value exists for it
// because no caller would do anything with one.
//
// Auth shape, verified against Linear's API docs: a PERSONAL API key is sent
// as `Authorization: <raw key>` with no `Bearer` prefix — the prefixed form is
// OAuth-only and a personal key sent that way is rejected. Every request also
// carries `public-file-urls-expire-in: 3600`, which makes Linear rewrite every
// uploads.linear.app URL in the response into a pre-signed one valid for the
// hour. That is what lets DownloadFile be a plain GET with NO Authorization
// header, which in turn sidesteps Go's redirect handling stripping the header
// on a cross-host hop to blob storage.

// LinearEnvPath is the credential file: ~/.kraken/linear.env, 0600, holding
// LINEAR_API_KEY and optionally LINEAR_DEFAULT_REPO / LINEAR_STATES.
func LinearEnvPath() string { return filepath.Join(util.Home(), ".kraken", "linear.env") }

var linearEnvKeys = []string{"LINEAR_API_KEY", "LINEAR_DEFAULT_REPO", "LINEAR_STATES"}

const (
	// linearAPIURL is Linear's single GraphQL endpoint. LinearClient.BaseURL
	// defaults to it and tests point that field at an httptest server.
	linearAPIURL = "https://api.linear.app/graphql"

	// defaultLinearRepo is the repo a Linear-born job targets when
	// LINEAR_DEFAULT_REPO says nothing (Q2).
	defaultLinearRepo = "monolith"

	// defaultLinearState is the workflow STATE NAME that qualifies an issue
	// for a job when LINEAR_STATES says nothing (Q3). A name rather than a
	// state type because carl's ruling was explicit: names are user-renamable,
	// which is exactly why the filter is a config knob.
	defaultLinearState = "Today"

	// linearPageSize is the `first:` argument on both paginated queries, and
	// linearCommentPage the one on an issue's inline comments and labels.
	// Both are STRINGS because they are pasted into a GraphQL document, which
	// is assembled at compile time where strconv is not a constant expression.
	//
	// Linear's practical ceiling is 250 and its default 50; 100 keeps one
	// page's response comfortably small while making the common case (a
	// handful of changed issues) a single round trip. Only the first page of
	// comments is taken — the creation-time snapshot (Q4) is what those are
	// for, and the delta path is AssignedCommentsSince, not this.
	linearPageSize    = "100"
	linearCommentPage = "50"

	// linearMaxPages is a runaway guard, not a limit anyone should reach: at
	// linearPageSize it bounds one fetch at 50k issues. Exceeding it is an
	// ERROR rather than a truncated slice, per the all-or-error posture below.
	linearMaxPages = 500

	// linearMaxResponse caps one GraphQL response body. Generous because a
	// page of 100 issues carries 100 descriptions and up to 5,000 comment
	// bodies; a body over this fails the decode, which surfaces as an error
	// rather than as a short page.
	linearMaxResponse = 32 << 20

	// linearErrTextRunes bounds one piece of remote text folded into an error
	// message. Runes rather than bytes because safeText truncates with
	// TruncateOneLine.
	linearErrTextRunes = 300

	// linearMaxFile caps one downloaded attachment at 25 MB. A larger body is
	// an error, never a truncated file — half a screenshot written to disk and
	// referenced from a job body is worse than a noted failure.
	linearMaxFile = 25 << 20
)

// LinearConfig is the whole of linear.env, resolved. Plain data: nothing here
// reads the environment after LoadLinearConfig returns.
type LinearConfig struct {
	// APIKey empty means the feature is OFF. There is no error value for it.
	APIKey string
	// DefaultRepo is the `repo:` every Linear-born job gets (Q2).
	DefaultRepo string
	// States are the workflow state NAMES that qualify an issue for a job
	// (Q3). Matching is the caller's job and is case-insensitive.
	States []string
}

// loadLinearEnv parses LinearEnvPath, same defensive shape as loadEmbedEnv: a
// missing or malformed file is an empty map, never an error.
func loadLinearEnv() map[string]string {
	out := map[string]string{}
	data, err := os.ReadFile(LinearEnvPath())
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
		for _, allowed := range linearEnvKeys {
			if k == allowed && v != "" {
				out[k] = v
			}
		}
	}
	return out
}

// linearSetting reads one config key from the pre-loaded file map, process env
// winning over the file.
func linearSetting(file map[string]string, key string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return file[key]
}

// LoadLinearConfig resolves the feature's configuration from the process
// environment and LinearEnvPath, in that precedence. The file is read ONCE per
// call for NewEmbedder's reason: three settings each re-opening a 0600 file
// from disk is pure waste.
//
// Every degradation here is silent by design. An absent file, an unreadable
// one and a line-noise one all produce the env-only view, and an absent key
// produces a zero APIKey, which every caller reads as "not configured".
func LoadLinearConfig() LinearConfig {
	file := loadLinearEnv()
	cfg := LinearConfig{
		APIKey:      linearSetting(file, "LINEAR_API_KEY"),
		DefaultRepo: linearSetting(file, "LINEAR_DEFAULT_REPO"),
	}
	if cfg.DefaultRepo == "" {
		cfg.DefaultRepo = defaultLinearRepo
	}
	raw := linearSetting(file, "LINEAR_STATES")
	if raw == "" {
		// The default applies only when the KEY is absent. A key that is
		// present but names no usable state is taken at its word — that is a
		// deliberate configuration and reinstating "Today" over it would
		// silently create jobs the config asked not to have.
		cfg.States = []string{defaultLinearState}
		return cfg
	}
	for _, s := range strings.Split(raw, ",") {
		if s = strings.TrimSpace(s); s != "" {
			cfg.States = append(cfg.States, s)
		}
	}
	return cfg
}

// LinearClient is one configured caller of Linear's API. BaseURL is exported
// so a test can point it at an httptest server; everything else is unexported
// because the key must not be reachable from outside this package.
type LinearClient struct {
	BaseURL string

	key    string
	client *http.Client
}

// NewLinearClient builds a client for cfg. A zero APIKey is not refused here —
// the feature-off decision belongs to the caller, which never gets this far
// with an empty key.
func NewLinearClient(cfg LinearConfig) *LinearClient {
	return &LinearClient{
		BaseURL: linearAPIURL,
		key:     cfg.APIKey,
		client:  &http.Client{Timeout: 60 * time.Second},
	}
}

// url and httpClient let a hand-built LinearClient{BaseURL: srv.URL} work as
// well as a constructed one: the zero value of either field is a usable
// default rather than a nil-pointer panic.
func (c *LinearClient) url() string {
	if c.BaseURL == "" {
		return linearAPIURL
	}
	return c.BaseURL
}

func (c *LinearClient) httpClient() *http.Client {
	if c.client == nil {
		return http.DefaultClient
	}
	return c.client
}

// LinearIssue is one Linear ticket as kraken needs it. ID is Linear's issue
// UUID (the dedupe key that survives a rename); Identifier is the human key
// ("ENG-142"); Description is the ticket's markdown VERBATIM — no rewrapping,
// no link rewriting, since composing the job body preserves its line structure
// byte for byte.
type LinearIssue struct {
	ID          string
	Identifier  string
	Title       string
	Description string
	URL         string
	StateName   string
	StateType   string
	TeamKey     string
	ProjectName string
	Priority    float64
	Labels      []string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	Comments    []LinearComment
}

// LinearComment is one comment on an issue. Author is a DISPLAY NAME — a
// person's or a bot's — because that is what a job body renders; there is no
// caller that needs the actor's id.
type LinearComment struct {
	ID              string
	Body            string
	Author          string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	IssueID         string
	IssueIdentifier string
}

// The two queries. Filters ride as whole-object VARIABLES ($filter typed
// IssueFilter!/CommentFilter!) rather than as inlined literals so the shapes
// below are Linear's own input types rather than strings this file has to keep
// syntactically valid.
//
// Neither query filters on workflow state: routing by state is the caller's
// decision (cfg.States) and a client that dropped non-matching issues would
// also drop the ones whose state CHANGED into a matching one.
const linearIssuesQuery = `query KrakenAssignedIssues($filter: IssueFilter!, $after: String) {
  issues(first: ` + linearPageSize + `, after: $after, filter: $filter) {
    pageInfo { hasNextPage endCursor }
    nodes {
      id
      identifier
      title
      description
      url
      priority
      createdAt
      updatedAt
      state { name type }
      team { key }
      project { name }
      labels(first: ` + linearCommentPage + `) { nodes { name } }
      comments(first: ` + linearCommentPage + `) {
        nodes {
          id
          body
          createdAt
          updatedAt
          user { displayName }
          botActor { name }
        }
      }
    }
  }
}`

const linearCommentsQuery = `query KrakenAssignedComments($filter: CommentFilter!, $after: String) {
  comments(first: ` + linearPageSize + `, after: $after, filter: $filter) {
    pageInfo { hasNextPage endCursor }
    nodes {
      id
      body
      createdAt
      updatedAt
      user { displayName }
      botActor { name }
      issue { id identifier }
    }
  }
}`

// linearPageInfo is Relay's cursor envelope, identical on both connections.
type linearPageInfo struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

// linearCommentNode is the wire shape of a comment. It carries both actor
// shapes because a Linear comment is authored by a user OR by a bot (an
// integration), and exactly one of the two is non-null.
type linearCommentNode struct {
	ID        string    `json:"id"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	User      *struct {
		DisplayName string `json:"displayName"`
	} `json:"user"`
	BotActor *struct {
		Name string `json:"name"`
	} `json:"botActor"`
	Issue *struct {
		ID         string `json:"id"`
		Identifier string `json:"identifier"`
	} `json:"issue"`
}

func (n linearCommentNode) author() string {
	if n.User != nil && n.User.DisplayName != "" {
		return n.User.DisplayName
	}
	if n.BotActor != nil {
		return n.BotActor.Name
	}
	return ""
}

func (n linearCommentNode) toComment() LinearComment {
	c := LinearComment{
		ID:        n.ID,
		Body:      n.Body,
		Author:    n.author(),
		CreatedAt: n.CreatedAt,
		UpdatedAt: n.UpdatedAt,
	}
	if n.Issue != nil {
		c.IssueID = n.Issue.ID
		c.IssueIdentifier = n.Issue.Identifier
	}
	return c
}

// linearIssueNode is the wire shape of an issue. Every nested object is a
// POINTER: Linear returns null for an issue with no project, no team or no
// state, and a value type would decode those as a zero struct that reads
// identically to "present but blank" — harmless here, but the pointer makes
// the nil check the code actually does the honest one.
type linearIssueNode struct {
	ID          string    `json:"id"`
	Identifier  string    `json:"identifier"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	URL         string    `json:"url"`
	Priority    float64   `json:"priority"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
	State       *struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"state"`
	Team *struct {
		Key string `json:"key"`
	} `json:"team"`
	Project *struct {
		Name string `json:"name"`
	} `json:"project"`
	Labels *struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
	Comments *struct {
		Nodes []linearCommentNode `json:"nodes"`
	} `json:"comments"`
}

func (n linearIssueNode) toIssue() LinearIssue {
	iss := LinearIssue{
		ID:          n.ID,
		Identifier:  n.Identifier,
		Title:       n.Title,
		Description: n.Description,
		URL:         n.URL,
		Priority:    n.Priority,
		CreatedAt:   n.CreatedAt,
		UpdatedAt:   n.UpdatedAt,
	}
	if n.State != nil {
		iss.StateName = n.State.Name
		iss.StateType = n.State.Type
	}
	if n.Team != nil {
		iss.TeamKey = n.Team.Key
	}
	if n.Project != nil {
		iss.ProjectName = n.Project.Name
	}
	if n.Labels != nil {
		for _, l := range n.Labels.Nodes {
			if l.Name != "" {
				iss.Labels = append(iss.Labels, l.Name)
			}
		}
	}
	if n.Comments != nil {
		for _, c := range n.Comments.Nodes {
			// A nested comment carries no issue{} selection — it is already
			// under its issue — so the back-reference is stamped here rather
			// than asked of the API.
			lc := c.toComment()
			lc.IssueID = n.ID
			lc.IssueIdentifier = n.Identifier
			iss.Comments = append(iss.Comments, lc)
		}
	}
	return iss
}

// do POSTs one GraphQL operation and returns the raw `data` object.
//
// The all-or-error posture is total and it is the SumTokensDelta reporting
// rule applied to a network read: a non-200, a GraphQL `errors` payload, or a
// body that will not decode returns an error, and every caller returns nil
// alongside it. A partial page presented as a complete answer would advance
// the pass's cursor past issues nobody ever saw, which no later poll can
// recover — the window would be marked swept unread.
//
// A 429 is one of those errors. The client never retries in a loop: the poll
// interval is the rate budget (2,500 req/h for a personal key against a
// 2-minute poll is ~1% of it), and a retry storm inside one tick is how a
// throttle gets defeated by the thing it throttles.
func (c *LinearClient) do(query string, vars map[string]any) (json.RawMessage, error) {
	body, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return nil, fmt.Errorf("linear marshal: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, c.url(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("linear request: %w", err)
	}
	// A personal API key goes over RAW — no "Bearer ", which is OAuth-only.
	req.Header.Set("Authorization", c.key)
	req.Header.Set("Content-Type", "application/json")
	// Pre-sign every uploads.linear.app URL in the response for an hour, so
	// DownloadFile needs no credential of its own.
	req.Header.Set("public-file-urls-expire-in", "3600")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		// err carries the URL, never a header — safe to wrap.
		return nil, fmt.Errorf("linear call: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, linearMaxResponse))
	if err != nil {
		return nil, fmt.Errorf("linear read: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		// The body is remote text on its way into a durable log line, so it
		// goes through safeText: redacted for the key, then truncated. The
		// status line gets the same treatment — it is remote text too, and a
		// proxy is free to put anything in it.
		return nil, fmt.Errorf("linear: %s: %s", c.safeText(resp.Status), c.safeText(string(raw)))
	}
	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("linear parse: %w", err)
	}
	if len(env.Errors) > 0 {
		// A GraphQL 200 carrying `errors` is a FAILURE here even when `data`
		// is partially populated: partial data is exactly the shape this
		// posture refuses.
		msgs := make([]string, 0, len(env.Errors))
		for _, e := range env.Errors {
			msgs = append(msgs, e.Message)
		}
		return nil, fmt.Errorf("linear: %s", c.safeText(strings.Join(msgs, "; ")))
	}
	if len(env.Data) == 0 {
		return nil, fmt.Errorf("linear: response carried no data")
	}
	return env.Data, nil
}

// safeText prepares remote-supplied text for an error message: the configured
// key is redacted out of it, and only then is it truncated.
//
// The redaction is the point and the ORDER is load-bearing. Truncation alone
// is not a defence: a server, a proxy or a captive portal that echoes the
// request's Authorization header lands the raw key well inside the limit, and
// every error this client returns is dlogged durably into ~/.kraken/logs — so
// one echo would write the credential to disk permanently. Redacting first
// means a cut can never land mid-key and leave a recognizable prefix behind,
// which is exactly what truncate-then-redact would do.
//
// An empty key skips the pass: ReplaceAll with an empty needle would otherwise
// splice the marker between every rune of the message.
//
// The truncation is TruncateOneLine rather than a byte slice: this text is
// arbitrary bytes from a remote server heading for a durable log, so a cut has
// to land on a RUNE boundary (a byte slice can split a multi-byte character
// into mojibake) and control characters — an escape sequence in a reason
// phrase reaching a terminal raw — have to become spaces.
func (c *LinearClient) safeText(s string) string {
	if c.key != "" {
		s = strings.ReplaceAll(s, c.key, "[redacted]")
	}
	return TruncateOneLine(s, linearErrTextRunes)
}

// linearSafeURL strips the query string (and any fragment) off an attachment
// URL.
//
// An uploads.linear.app link arrives PRE-SIGNED because of the
// public-file-urls-expire-in header every GraphQL request carries — the query
// string is a bearer token good for an hour. Every error DownloadFile returns
// is bound for a durable sink (a dlog line, the events table, a failure note
// in a job body), so the token is stripped before the URL gets there. The PATH
// still identifies the object, which is all a human needs to fetch it by hand.
//
// IndexAny rather than url.Parse deliberately: this runs on a string that may
// not parse at all (it is the argument of a request that just failed), and a
// parse error must not cost the caller its error message.
func linearSafeURL(raw string) string {
	if i := strings.IndexAny(raw, "?#"); i >= 0 {
		return raw[:i]
	}
	return raw
}

// linearURLQueryRe matches a query string glued to a URL inside prose — the
// scheme up to the first '?', then everything to the next whitespace or quote.
// Group 1 is the tokenless prefix the match is replaced with.
var linearURLQueryRe = regexp.MustCompile(`(https?://[^\s"'?#]+)[?#][^\s"']*`)

// safeErr renders one stdlib error for an error message with rawURL's signed
// form swapped for its stripped one.
//
// It exists because net/http reports failures as a *url.Error, which
// stringifies the WHOLE request URL — so wrapping one with %w would put the
// pre-signed token in the log by the back door, defeating linearSafeURL at the
// call site right next to it. The cost is the error chain: these are network
// failures nothing matches on with errors.Is, and the text is the entire
// value they carry.
//
// The keyed replace alone is not enough: net/http reassigns the request
// through its redirect loop, so a failure after a redirect stringifies the
// FINAL URL — a signed blob-storage link on another host that matches nothing
// we were passed. The regexp pass strips the query off every URL left in the
// text, whatever host it landed on; an error message's URLs carry their value
// in the path, never the token.
func (c *LinearClient) safeErr(rawURL string, err error) string {
	s := err.Error()
	if safe := linearSafeURL(rawURL); safe != rawURL {
		s = strings.ReplaceAll(s, rawURL, safe)
	}
	s = linearURLQueryRe.ReplaceAllString(s, "$1")
	return c.safeText(s)
}

// linearSince renders a cursor for a Linear date comparator, which takes
// RFC 3339. UTC so the string is stable regardless of the daemon's zone.
func linearSince(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// AssignedIssuesSince returns every issue assigned to the authenticated viewer
// whose updatedAt is strictly after since, newest page state included, with
// each issue's first page of comments attached.
//
// `assignee: { isMe: { eq: true } }` is NullableUserFilter.isMe — the viewer
// is whoever the key authenticates as, so kraken never needs to know or store
// carl's Linear user id.
//
// There is deliberately NO state filter: which states qualify is cfg.States,
// applied by the pass, and an issue that just MOVED into a qualifying state is
// one this call has to return for that transition to be noticed at all.
func (c *LinearClient) AssignedIssuesSince(since time.Time) ([]LinearIssue, error) {
	filter := map[string]any{
		"assignee":  map[string]any{"isMe": map[string]any{"eq": true}},
		"updatedAt": map[string]any{"gt": linearSince(since)},
	}
	var out []LinearIssue
	var after string
	for page := 0; ; page++ {
		if page >= linearMaxPages {
			return nil, fmt.Errorf("linear issues: still paginating after %d pages", linearMaxPages)
		}
		vars := map[string]any{"filter": filter}
		if after != "" {
			vars["after"] = after
		}
		data, err := c.do(linearIssuesQuery, vars)
		if err != nil {
			return nil, err
		}
		var parsed struct {
			Issues struct {
				PageInfo linearPageInfo    `json:"pageInfo"`
				Nodes    []linearIssueNode `json:"nodes"`
			} `json:"issues"`
		}
		if err := json.Unmarshal(data, &parsed); err != nil {
			return nil, fmt.Errorf("linear issues parse: %w", err)
		}
		for _, n := range parsed.Issues.Nodes {
			out = append(out, n.toIssue())
		}
		if !parsed.Issues.PageInfo.HasNextPage {
			return out, nil
		}
		if parsed.Issues.PageInfo.EndCursor == "" {
			// hasNextPage with no cursor to follow: there are more issues and
			// no way to ask for them. Returning what we have would be the
			// partial-presented-as-complete failure this file refuses.
			return nil, fmt.Errorf("linear issues: more pages but no endCursor")
		}
		after = parsed.Issues.PageInfo.EndCursor
	}
}

// AssignedCommentsSince returns every comment updated after since whose parent
// issue is assigned to the viewer.
//
// It exists because a comment does not reliably bump its parent issue's
// updatedAt: without this call, a discussion on an already-known ticket would
// never move the issue cursor and would be invisible to the pass forever.
func (c *LinearClient) AssignedCommentsSince(since time.Time) ([]LinearComment, error) {
	filter := map[string]any{
		"issue":     map[string]any{"assignee": map[string]any{"isMe": map[string]any{"eq": true}}},
		"updatedAt": map[string]any{"gt": linearSince(since)},
	}
	var out []LinearComment
	var after string
	for page := 0; ; page++ {
		if page >= linearMaxPages {
			return nil, fmt.Errorf("linear comments: still paginating after %d pages", linearMaxPages)
		}
		vars := map[string]any{"filter": filter}
		if after != "" {
			vars["after"] = after
		}
		data, err := c.do(linearCommentsQuery, vars)
		if err != nil {
			return nil, err
		}
		var parsed struct {
			Comments struct {
				PageInfo linearPageInfo      `json:"pageInfo"`
				Nodes    []linearCommentNode `json:"nodes"`
			} `json:"comments"`
		}
		if err := json.Unmarshal(data, &parsed); err != nil {
			return nil, fmt.Errorf("linear comments parse: %w", err)
		}
		for _, n := range parsed.Comments.Nodes {
			out = append(out, n.toComment())
		}
		if !parsed.Comments.PageInfo.HasNextPage {
			return out, nil
		}
		if parsed.Comments.PageInfo.EndCursor == "" {
			return nil, fmt.Errorf("linear comments: more pages but no endCursor")
		}
		after = parsed.Comments.PageInfo.EndCursor
	}
}

// DownloadFile GETs one attachment and returns its bytes and Content-Type.
//
// NO Authorization header is attached, and that is the point of the
// public-file-urls-expire-in header every GraphQL request above carries: the
// URL arrives pre-signed, so this is an anonymous GET. Sending the key here
// would also be actively harmful — an attachment URL redirects to blob storage
// on another host, and Go's client strips Authorization across hosts anyway,
// so the header would buy nothing while putting the credential on a hop it has
// no business being on.
//
// The size cap returns an ERROR rather than a truncated file: media is
// best-effort at the caller (a failed download degrades to keeping the Linear
// URL in the job body with a note), and a half-written screenshot referenced
// as if whole is the one outcome that failure mode must not produce.
func (c *LinearClient) DownloadFile(url string) ([]byte, string, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, "", fmt.Errorf("linear download request %s: %s", c.safeText(linearSafeURL(url)), c.safeErr(url, err))
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("linear download %s: %s", c.safeText(linearSafeURL(url)), c.safeErr(url, err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Same rule as do()'s non-200, and it applies here even though this
		// request carries no Authorization header: a redirect chain can end
		// anywhere, and the redaction costs one string scan.
		return nil, "", fmt.Errorf("linear download %s: %s", c.safeText(linearSafeURL(url)), c.safeText(resp.Status))
	}
	// One byte past the cap, so "exactly at the cap" passes and "over it" is
	// detectable rather than silently clipped.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, linearMaxFile+1))
	if err != nil {
		return nil, "", fmt.Errorf("linear download %s: %s", c.safeText(linearSafeURL(url)), c.safeErr(url, err))
	}
	if len(raw) > linearMaxFile {
		return nil, "", fmt.Errorf("linear download %s: larger than the %d byte cap", c.safeText(linearSafeURL(url)), linearMaxFile)
	}
	return raw, resp.Header.Get("Content-Type"), nil
}
