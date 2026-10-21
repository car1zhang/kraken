package jobs

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"kraken/internal/store"
	"kraken/internal/util"
)

// jobstack.go — the jobs-level side of a cell's STACK LINKS: the manual-test
// URLs a cell registers through `kraken stack add` (cmd/kraken/cmd_stack.go
// → POST /api/job/stack → the writers below → store.job_stacks), rendered by
// the board as chips beside the PR chips, and kept honest by the daemon's
// probe pass (stackprobe.go).
//
// Like SetJobLoad, none of this goes through WriteJobFields: a stack link is
// bookkeeping about the CELL, rewritten at its manual-test gate, and routing
// it through the job-fields path would bump `rev` (409ing an open editor) and
// `updated_at` (resetting the card's age). With no store wired the writers
// refuse rather than degrade, for SetJobLoad's reason: there is no `stack:`
// frontmatter field, so a silent no-op would be a confirmation line over a
// write that never happened.

// StackLinkMax caps the links one job may carry. A stack is a handful of
// ports; the ninth distinct url is refused by the API with the cap named,
// so a runaway loop fails loudly instead of growing a chip row forever.
const StackLinkMax = 8

// StackLabelMax caps a link's label in RUNES. Over-long labels are TRUNCATED
// by the API (core.TruncateOneLine), never refused: the url is the payload,
// and the chip shows the port beside whatever label survives.
const StackLabelMax = 24

// StackTarget is a parsed stack URL as the probe sees it: the host and port
// it would dial, and whether that host is this machine's loopback — the
// only case kraken ever dials, since a remote host could hang the tick and
// says nothing about a process on THIS Mac.
type StackTarget struct {
	Host     string
	Port     string
	Loopback bool
}

// ParseStackTarget parses raw as a stack URL. It is the ONE validation rule
// for the route and the ONE address derivation for the probe, so the two
// cannot disagree about what a link is. Refusals, each as its own sentence:
// not parseable, a scheme other than http/https (a `javascript:` or `file:`
// chip would render as a link the card would happily follow), no host, or
// userinfo (a credential in a chip's href is a credential in every board
// payload). The port defaults from the scheme when the url names none.
func ParseStackTarget(raw string) (StackTarget, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return StackTarget{}, fmt.Errorf("not a url: %v", err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return StackTarget{}, fmt.Errorf("scheme must be http or https (got %q)", u.Scheme)
	}
	if u.User != nil {
		return StackTarget{}, fmt.Errorf("url must not carry credentials")
	}
	host := u.Hostname()
	if host == "" {
		return StackTarget{}, fmt.Errorf("url has no host")
	}
	port := u.Port()
	if port == "" {
		if scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return StackTarget{Host: host, Port: port, Loopback: isLoopbackHost(host)}, nil
}

// isLoopbackHost is the probe's dial gate. `localhost` and the two loopback
// literals are the obvious three; `0.0.0.0` is included because a dev server
// bound to all interfaces is reachable on loopback and agents paste the bind
// address as the url. Any other name — a LAN ip, an ngrok host, a tailnet
// name — is never dialled.
func isLoopbackHost(host string) bool {
	h := strings.ToLower(strings.Trim(host, "[]"))
	if h == "localhost" || h == "0.0.0.0" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && (ip.IsLoopback() || ip.IsUnspecified())
}

// AddStackLink registers (or re-labels) url on j. The caller (the API) has
// already validated the url through ParseStackTarget and truncated the
// label; this layer enforces the per-job cap, which needs the store.
func AddStackLink(j *Job, rawURL, label string) error {
	st := ActiveStore()
	if st == nil {
		return fmt.Errorf("add stack link for %s: no store is wired — stack links are store-backed only (there is no `stack:` job-file field)", j.Slug)
	}
	links, err := st.ListStacks(j.ID)
	if err != nil {
		return fmt.Errorf("add stack link for %s: %w", j.Slug, err)
	}
	exists := false
	for _, l := range links {
		if l.URL == rawURL {
			exists = true
			break
		}
	}
	if !exists && len(links) >= StackLinkMax {
		return fmt.Errorf("%s already has %d stack links (the cap): remove one with `kraken stack rm` or `kraken stack clear` first", j.Slug, StackLinkMax)
	}
	if err := st.UpsertStack(j.ID, rawURL, label, time.Now().UnixMilli()); err != nil {
		return fmt.Errorf("add stack link for %s: %w", j.Slug, err)
	}
	util.LogEvent("jobs", "info", "stack-add", fmt.Sprintf("%s: %s (%s)", j.Slug, rawURL, label))
	return nil
}

// RemoveStackLink drops the link whose url or label is token. store.ErrNotFound
// comes back unwrapped-in-meaning (errors.Is works) when nothing matched, so
// the API can answer 404 rather than 500.
func RemoveStackLink(j *Job, token string) error {
	st := ActiveStore()
	if st == nil {
		return fmt.Errorf("remove stack link for %s: no store is wired", j.Slug)
	}
	if err := st.RemoveStack(j.ID, token); err != nil {
		return fmt.Errorf("remove stack link %q for %s: %w", token, j.Slug, err)
	}
	util.LogEvent("jobs", "info", "stack-rm", fmt.Sprintf("%s: %s", j.Slug, token))
	return nil
}

// ClearStackLinks drops every link on j — the cell's move after the
// manual-test gate passes and the servers are down.
func ClearStackLinks(j *Job) error {
	st := ActiveStore()
	if st == nil {
		return fmt.Errorf("clear stack links for %s: no store is wired", j.Slug)
	}
	if err := st.ClearStacks(j.ID); err != nil {
		return fmt.Errorf("clear stack links for %s: %w", j.Slug, err)
	}
	util.LogEvent("jobs", "info", "stack-clear", fmt.Sprintf("%s: cleared", j.Slug))
	return nil
}

// ListStackLinks is the read every renderer goes through. No store or no id
// means no links, never an error: the board calls this for every card.
func ListStackLinks(j *Job) []store.StackLink {
	st := ActiveStore()
	if st == nil || j == nil || j.ID == 0 {
		return nil
	}
	links, err := st.ListStacks(j.ID)
	if err != nil {
		util.LogEvent("jobs", "error", "stack-list", fmt.Sprintf("%s: %v", j.Slug, err))
		return nil
	}
	return links
}

// DeleteStacksForTeardown is teardownCell's call: the stack died with the tmux
// session, so its rows go too. No store or no id is a nil, the same posture as
// CloseThreadsForTeardown beside it — the cell is already gone, and a stale
// row is a smaller problem than a teardown that reports failure after
// succeeding.
func DeleteStacksForTeardown(j *Job) error {
	st := ActiveStore()
	if st == nil || j.ID == 0 {
		return nil
	}
	return st.DeleteStacksForJob(j.ID)
}
