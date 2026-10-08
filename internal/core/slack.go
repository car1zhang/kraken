package core

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// slack.go — typed wrappers over the six Slack Web API methods the intake
// poller needs. Stdlib net/http only, per the house rule.
//
// Every Slack method here is a form-encoded POST carrying the xoxp user token
// as a Bearer header, and every Slack response is JSON with a top-level `ok`
// bool. That envelope is the whole reason for the single `call` seam below:
// `ok: false` is an ERROR carrying Slack's own error string, never an empty
// success — a decode straight into the payload struct would turn "invalid
// auth" into "you have no conversations" and the poller would report a silent
// zero forever.
//
// Two things deliberately never escape this file. CURSORS: pagination is
// internal to each method, so no caller can persist a cursor and resume a
// stale one. And the TOKEN's value: it lives only in the Authorization header,
// which http.Client errors do not echo (they carry the URL), so no error path
// here can leak it.
//
// Message timestamps (`ts`) are STRINGS end to end and are never parsed
// through a float — see SlackTSLess. Slack's `ts` is "seconds.micros" with
// full microsecond precision; float64 has 53 bits of mantissa, and rounding a
// watermark by one microsecond either re-delivers a message or drops one.

const (
	slackAPIBase = "https://slack.com/api"

	// slackTimeout bounds one Slack call. Slack's own gateway gives up well
	// before this; the point is that a hung socket cannot wedge a daemon pass.
	slackTimeout = 30 * time.Second

	// slackMaxPages bounds any one method's cursor walk. Slack terminates a
	// walk with an empty next_cursor, so this only fires if the API (or a test
	// fixture) hands back a cursor that never resolves — a bounded wrong
	// answer beats an unbounded loop inside a daemon tick.
	slackMaxPages = 100

	// slackMaxBody caps one response read. Search pages of 100 matches are the
	// largest thing here by an order of magnitude.
	slackMaxBody = 1 << 24

	// slackDefaultRetryAfter is what a 429 with no usable Retry-After header
	// backs off by. Slack documents a minute as its own default; zero would
	// mean "no backoff", which is the one answer that makes the rate limit
	// worse instead of better.
	slackDefaultRetryAfter = 60 * time.Second
)

// SlackClient talks to one Slack workspace as one user. BaseURL and
// HTTPClient are exported so a test can point the client at an httptest.Server
// and give it its own transport; both have working zero values.
//
// Cookie is the `d` session cookie's value, and is empty for an OAuth (xoxp-)
// token. A client-session (xoxc-) token is bound to that cookie: Slack rejects
// the token alone as unauthenticated, so for that kind the cookie is half the
// credential rather than an optimisation. An empty Cookie sends no Cookie
// header at all, which is what keeps the xoxp path byte-identical to what it
// was.
type SlackClient struct {
	Token      string
	Cookie     string
	BaseURL    string
	HTTPClient *http.Client
}

// NewSlackClient builds a client for the real Slack API with a bounded
// timeout. cookie is "" for an OAuth token and the `d` cookie's value for a
// session token.
func NewSlackClient(token, cookie string) *SlackClient {
	return &SlackClient{
		Token:      token,
		Cookie:     cookie,
		BaseURL:    slackAPIBase,
		HTTPClient: &http.Client{Timeout: slackTimeout},
	}
}

// SlackAuthInfo is auth.test's answer: who this token acts as.
type SlackAuthInfo struct {
	UserID string
	TeamID string
}

// SlackConversation is one entry of the DM/group-DM inventory.
type SlackConversation struct {
	ID     string
	IsIM   bool
	IsMPIM bool
}

// SlackMessage is one message from history, replies, or search. ChannelName is
// filled only where Slack supplies it — search results carry a channel object,
// conversations.history does not — so it is empty on the polling paths.
//
// ReplyCount and LatestReply are the thread-chase signal: thread replies never
// appear in conversations.history, so the parent's LatestReply is the only
// thing that says a thread moved.
type SlackMessage struct {
	ChannelID   string
	ChannelName string
	TS          string
	ThreadTS    string
	User        string
	SubType     string
	Text        string
	ReplyCount  int
	LatestReply string
}

// SlackRateLimitError is HTTP 429. It is returned rather than retried inside
// the client: the caller owns the pass budget and is the only layer that can
// decide to sit the next few passes out, and a retry loop buried in a client
// method would burn the daemon tick it is running on.
type SlackRateLimitError struct {
	RetryAfter time.Duration
}

func (e *SlackRateLimitError) Error() string {
	return fmt.Sprintf("slack rate limited, retry after %s", e.RetryAfter)
}

// slackEnvelope is the part of every Slack response that is always present.
type slackEnvelope struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

// slackCursor is the cursor-pagination block. Slack puts it at the top level
// on every method here.
type slackCursor struct {
	ResponseMetadata struct {
		NextCursor string `json:"next_cursor"`
	} `json:"response_metadata"`
}

// slackAPIMessage is the wire shape of a message in conversations.history and
// conversations.replies. Search matches are decoded separately — they carry a
// channel OBJECT that these payloads do not have at all.
type slackAPIMessage struct {
	SubType     string `json:"subtype"`
	User        string `json:"user"`
	Text        string `json:"text"`
	TS          string `json:"ts"`
	ThreadTS    string `json:"thread_ts"`
	ReplyCount  int    `json:"reply_count"`
	LatestReply string `json:"latest_reply"`
}

func (m slackAPIMessage) toMessage(channelID string) SlackMessage {
	return SlackMessage{
		ChannelID:   channelID,
		TS:          m.TS,
		ThreadTS:    m.ThreadTS,
		User:        m.User,
		SubType:     m.SubType,
		Text:        m.Text,
		ReplyCount:  m.ReplyCount,
		LatestReply: m.LatestReply,
	}
}

// baseURL resolves the API root, so a zero-value SlackClient still talks to
// the real Slack rather than to an empty string.
func (c *SlackClient) baseURL() string {
	if strings.TrimSpace(c.BaseURL) == "" {
		return slackAPIBase
	}
	return strings.TrimSuffix(c.BaseURL, "/")
}

func (c *SlackClient) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{Timeout: slackTimeout}
}

// call POSTs one form-encoded Slack method and decodes the response into out.
//
// Order matters here: 429 is decided from the STATUS, before the body is
// looked at, because a throttled response is not required to carry a usable
// envelope; then `ok: false` becomes an error carrying Slack's own error
// string; only then does out see anything.
func (c *SlackClient) call(method string, form url.Values, out any) error {
	body := strings.NewReader(form.Encode())
	req, err := http.NewRequest(http.MethodPost, c.baseURL()+"/"+method, body)
	if err != nil {
		return fmt.Errorf("slack %s request: %w", method, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if c.Cookie != "" {
		// A session token's other half. The value is sent EXACTLY as read from
		// the browser's cookie jar — it is already in its wire form there, so
		// re-encoding it produces a cookie Slack does not recognise. Set via
		// AddCookie rather than a raw header so the name/value framing is the
		// stdlib's rather than ours.
		req.AddCookie(&http.Cookie{Name: "d", Value: c.Cookie})
	}

	resp, err := c.httpClient().Do(req)
	if err != nil {
		// err carries the URL, never the Authorization header — safe to wrap.
		return fmt.Errorf("slack %s call: %w", method, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return &SlackRateLimitError{RetryAfter: slackRetryAfter(resp.Header.Get("Retry-After"))}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, slackMaxBody))
	if err != nil {
		return fmt.Errorf("slack %s read: %w", method, err)
	}
	if resp.StatusCode != http.StatusOK {
		// A non-200 that is not 429 is a gateway or auth-layer failure whose
		// body is HTML as often as JSON. Truncate hard; it never echoes the
		// request headers.
		msg := string(raw)
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return fmt.Errorf("slack %s: %s: %s", method, resp.Status, msg)
	}
	var env slackEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("slack %s parse: %w", method, err)
	}
	if !env.OK {
		e := env.Error
		if e == "" {
			e = "unknown error"
		}
		return fmt.Errorf("slack %s: %s", method, e)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("slack %s parse: %w", method, err)
	}
	return nil
}

// slackRetryAfter reads the Retry-After header, which Slack sends in whole
// seconds. An absent or unparseable value falls back to Slack's own documented
// default rather than to zero — see slackDefaultRetryAfter.
func slackRetryAfter(h string) time.Duration {
	n, err := strconv.Atoi(strings.TrimSpace(h))
	if err != nil || n <= 0 {
		return slackDefaultRetryAfter
	}
	return time.Duration(n) * time.Second
}

// AuthTest reports who the token acts as. It is the only way to learn the
// self user id when SLACK_USER_ID is not configured, and the self id is what
// both the mention query and the drop-my-own-messages filter are built from.
func (c *SlackClient) AuthTest() (SlackAuthInfo, error) {
	var resp struct {
		UserID string `json:"user_id"`
		TeamID string `json:"team_id"`
	}
	if err := c.call("auth.test", url.Values{}, &resp); err != nil {
		return SlackAuthInfo{}, err
	}
	return SlackAuthInfo{UserID: resp.UserID, TeamID: resp.TeamID}, nil
}

// UserConversations lists the DMs and group DMs this user is in, walking every
// cursor page internally.
func (c *SlackClient) UserConversations() ([]SlackConversation, error) {
	var out []SlackConversation
	cursor := ""
	for page := 0; page < slackMaxPages; page++ {
		form := url.Values{}
		form.Set("types", "im,mpim")
		form.Set("exclude_archived", "true")
		form.Set("limit", "200")
		if cursor != "" {
			form.Set("cursor", cursor)
		}
		var resp struct {
			slackCursor
			Channels []struct {
				ID     string `json:"id"`
				IsIM   bool   `json:"is_im"`
				IsMPIM bool   `json:"is_mpim"`
			} `json:"channels"`
		}
		if err := c.call("users.conversations", form, &resp); err != nil {
			return nil, err
		}
		for _, ch := range resp.Channels {
			out = append(out, SlackConversation{ID: ch.ID, IsIM: ch.IsIM, IsMPIM: ch.IsMPIM})
		}
		next := strings.TrimSpace(resp.ResponseMetadata.NextCursor)
		if next == "" || next == cursor {
			break
		}
		cursor = next
	}
	return out, nil
}

// ConversationHistory reads a channel's messages newer than oldest, walking
// every cursor page.
//
// oldest is passed through VERBATIM as the string it came from the store as.
// Slack's `oldest` is EXCLUSIVE and the client adds no inclusive flag, which
// is what makes a watermark of the last-seen ts correct without an off-by-one
// adjustment anywhere.
func (c *SlackClient) ConversationHistory(channel, oldest string, limit int) ([]SlackMessage, error) {
	return c.messagePages("conversations.history", channel, limit, func(form url.Values) {
		form.Set("channel", channel)
		if oldest != "" {
			form.Set("oldest", oldest)
		}
	})
}

// ConversationReplies reads a thread's replies newer than oldest. Thread
// replies never appear in conversations.history, so this is the only way to
// see one. Same verbatim-oldest and internal-pagination rules as history.
func (c *SlackClient) ConversationReplies(channel, threadTS, oldest string) ([]SlackMessage, error) {
	return c.messagePages("conversations.replies", channel, 0, func(form url.Values) {
		form.Set("channel", channel)
		form.Set("ts", threadTS)
		if oldest != "" {
			form.Set("oldest", oldest)
		}
	})
}

// messagePages is the shared cursor walk behind history and replies: both
// return the same `messages` array in the same envelope and differ only in the
// per-request parameters, which the caller sets.
func (c *SlackClient) messagePages(method, channel string, limit int, setParams func(url.Values)) ([]SlackMessage, error) {
	var out []SlackMessage
	cursor := ""
	for page := 0; page < slackMaxPages; page++ {
		form := url.Values{}
		setParams(form)
		if limit > 0 {
			form.Set("limit", strconv.Itoa(limit))
		}
		if cursor != "" {
			form.Set("cursor", cursor)
		}
		var resp struct {
			slackCursor
			Messages []slackAPIMessage `json:"messages"`
		}
		if err := c.call(method, form, &resp); err != nil {
			return nil, err
		}
		for _, m := range resp.Messages {
			out = append(out, m.toMessage(channel))
		}
		next := strings.TrimSpace(resp.ResponseMetadata.NextCursor)
		if next == "" || next == cursor {
			break
		}
		cursor = next
	}
	return out, nil
}

// SearchMessages runs search.messages, which is the only way to find direct
// @-mentions in channels — Slack has no mentions API.
//
// The first request sends `cursor=*`, which is Slack's documented way of
// asking search for its first cursor page; subsequent ones send the returned
// next_cursor. Results are newest-first (`sort=timestamp`, `sort_dir=desc`),
// and the caller applies the exact ts floor, because the `after:` operator the
// query carries is DAY-grained and returns messages below the real watermark.
func (c *SlackClient) SearchMessages(query string) ([]SlackMessage, error) {
	var out []SlackMessage
	cursor := "*"
	for page := 0; page < slackMaxPages; page++ {
		form := url.Values{}
		form.Set("query", query)
		form.Set("sort", "timestamp")
		form.Set("sort_dir", "desc")
		form.Set("count", "100")
		form.Set("cursor", cursor)
		var resp struct {
			slackCursor
			Messages struct {
				Matches []struct {
					User     string `json:"user"`
					Text     string `json:"text"`
					TS       string `json:"ts"`
					ThreadTS string `json:"thread_ts"`
					Channel  struct {
						ID   string `json:"id"`
						Name string `json:"name"`
					} `json:"channel"`
				} `json:"matches"`
			} `json:"messages"`
		}
		if err := c.call("search.messages", form, &resp); err != nil {
			return nil, err
		}
		for _, m := range resp.Messages.Matches {
			out = append(out, SlackMessage{
				ChannelID:   m.Channel.ID,
				ChannelName: m.Channel.Name,
				TS:          m.TS,
				ThreadTS:    m.ThreadTS,
				User:        m.User,
				Text:        m.Text,
			})
		}
		next := strings.TrimSpace(resp.ResponseMetadata.NextCursor)
		if next == "" || next == cursor {
			break
		}
		cursor = next
	}
	return out, nil
}

// UserInfo resolves a user id to something a human reads. Display name first,
// then real name, then the id itself — the id is always available and an
// inbox row with a blank author is worse than one naming a U-code.
func (c *SlackClient) UserInfo(userID string) (string, error) {
	form := url.Values{}
	form.Set("user", userID)
	var resp struct {
		User struct {
			Name     string `json:"name"`
			RealName string `json:"real_name"`
			Profile  struct {
				DisplayName string `json:"display_name"`
				RealName    string `json:"real_name"`
			} `json:"profile"`
		} `json:"user"`
	}
	if err := c.call("users.info", form, &resp); err != nil {
		return "", err
	}
	for _, cand := range []string{
		resp.User.Profile.DisplayName,
		resp.User.Profile.RealName,
		resp.User.RealName,
		resp.User.Name,
	} {
		if s := strings.TrimSpace(cand); s != "" {
			return s, nil
		}
	}
	return userID, nil
}

// SlackTSLess reports whether Slack timestamp a is strictly older than b.
//
// The comparison splits on `.` and compares the two integer halves. It never
// converts the whole value to a float: `ts` is "seconds.micros" and a float64
// cannot hold that many significant digits exactly, so a parsed-and-formatted
// watermark drifts by microseconds — which either re-delivers a message
// forever or silently skips one. The fractional half is padded to six digits
// before comparing so "1.1" and "1.05" order numerically rather than
// lexically; Slack always sends six, but a truncated value must not invert.
//
// A half that does not parse counts as 0, which keeps a malformed ts ordered
// rather than panicking a daemon pass.
func SlackTSLess(a, b string) bool {
	as, af := splitSlackTS(a)
	bs, bf := splitSlackTS(b)
	if as != bs {
		return as < bs
	}
	return af < bf
}

// splitSlackTS returns a Slack ts as (seconds, microseconds).
func splitSlackTS(ts string) (int64, int64) {
	sec, frac, _ := strings.Cut(strings.TrimSpace(ts), ".")
	s, err := strconv.ParseInt(sec, 10, 64)
	if err != nil {
		s = 0
	}
	// Pad/trim to exactly six digits so the halves are comparable as integers
	// regardless of how many digits the sender wrote.
	if len(frac) > 6 {
		frac = frac[:6]
	}
	for len(frac) < 6 {
		frac += "0"
	}
	f, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		f = 0
	}
	return s, f
}
