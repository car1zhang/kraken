// Protocol parity: this file is part of the MULTIPLAYER protocol (CLAUDE.md
// § Protocol parity). The protocol is kraken-core's and identical in every
// fork; a change here lands in kraken-core first, carries "protocol:" in its
// PR title, and is named to the operator as a protocol change before it is
// pushed.

package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"kraken/internal/util"
)

// cmd_nodes.go — the fleet from the command line: `kraken nodes` lists the
// sibling krakens this node's serve knows, and splitNodeSelector is how
// `k s` / `k r` reach a PUBLIC cell on one of them with a `<node>/<slug>`
// selector. Every call is one round trip to this machine's own serve (owner
// tier), which forwards to the sibling; nothing here talks to the tailnet.

// nodeRowWire mirrors api's webNode.
type nodeRowWire struct {
	NodeID   string          `json:"node_id"`
	Label    string          `json:"label"`
	Operator string          `json:"operator"`
	DNSName  string          `json:"dns_name"`
	IP       string          `json:"ip"`
	Port     int             `json:"port"`
	Build    string          `json:"build"`
	Contract int             `json:"contract"`
	Online   bool            `json:"online"`
	LastOK   int64           `json:"last_ok"`
	LastErr  string          `json:"last_err,omitempty"`
	Jobs     json.RawMessage `json:"jobs,omitempty"`
}

// nodesResponse is GET /api/nodes' body.
type nodesResponse struct {
	Nodes []nodeRowWire `json:"nodes"`
}

func cmdNodes(args []string) {
	asJSON := false
	for _, a := range args {
		switch a {
		case "--json":
			asJSON = true
		default:
			util.Die("usage: kraken nodes [--json]")
		}
	}
	body, err := qfGet("/api/nodes")
	if err != nil {
		util.Die("%v", err)
	}
	if asJSON {
		if _, err := os.Stdout.Write(body); err != nil {
			util.Die("write stdout: %v", err)
		}
		return
	}
	var resp nodesResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		util.Die("decode /api/nodes: %v", err)
	}
	fmt.Print(renderNodesTable(resp))
}

// nodesNow is renderNodesTable's clock; a seam for tests.
var nodesNow = time.Now

// renderNodesTable renders the registry: one row per node, online first, with
// its card counts by state and how long ago it last answered a poll.
func renderNodesTable(resp nodesResponse) string {
	if len(resp.Nodes) == 0 {
		return "no nodes on the tailnet\n"
	}
	nodes := append([]nodeRowWire(nil), resp.Nodes...)
	sort.SliceStable(nodes, func(a, b int) bool { return nodes[a].Online && !nodes[b].Online })
	var b strings.Builder
	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NODE\tOPERATOR\tDNS\tONLINE\tWORKING\tBLOCKED\tDRAFT\tDONE\tLAST OK")
	for _, n := range nodes {
		counts := map[string]int{}
		var cards []struct {
			State string `json:"state"`
		}
		if len(n.Jobs) > 0 && json.Unmarshal(n.Jobs, &cards) == nil {
			for _, c := range cards {
				counts[c.State]++
			}
		}
		online := "no"
		if n.Online {
			online = "yes"
		}
		cells := []string{
			orDash(n.Label), orDash(n.Operator), orDash(n.DNSName), online,
			strconv.Itoa(counts["working"]), strconv.Itoa(counts["blocked"]),
			strconv.Itoa(counts["draft"]), strconv.Itoa(counts["done"]),
			lastOKAge(n.LastOK),
		}
		fmt.Fprintln(tw, strings.Join(cells, "\t"))
	}
	_ = tw.Flush()
	return b.String()
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// lastOKAge renders a unix-millis stamp as an age ("12s ago"), "never" for 0.
func lastOKAge(ms int64) string {
	if ms <= 0 {
		return "never"
	}
	d := nodesNow().Sub(time.UnixMilli(ms))
	switch {
	case d < 0:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// splitNodeSelector splits a `<node>/<slug>` selector. ok only when the text
// before the first "/" names a registry node — its label, the first label of
// its DNS name (both case-insensitively), or a prefix of at least 4
// characters of its stable id; anything else leaves the selector to the local
// resolver exactly as it was.
func splitNodeSelector(sel string, nodes []nodeRowWire) (node *nodeRowWire, slug string, ok bool) {
	left, rest, found := strings.Cut(sel, "/")
	if !found || left == "" {
		return nil, "", false
	}
	for i := range nodes {
		n := &nodes[i]
		dnsFirst, _, _ := strings.Cut(n.DNSName, ".")
		if (n.Label != "" && strings.EqualFold(left, n.Label)) ||
			(dnsFirst != "" && strings.EqualFold(left, dnsFirst)) ||
			(len(left) >= 4 && strings.HasPrefix(strings.ToLower(n.NodeID), strings.ToLower(left))) {
			return n, rest, true
		}
	}
	return nil, "", false
}

// remoteSelector resolves sel against the registry when it could be
// node-qualified. A serve that cannot be reached leaves the selector local:
// `k s` must keep working for tmux panes whether or not serve is up.
func remoteSelector(sel string) (*nodeRowWire, string, bool) {
	if !strings.Contains(sel, "/") {
		return nil, "", false
	}
	// say so whenever the registry cannot be read: a `<node>/<slug>` it would
	// have matched now resolves (or fails) locally, which is a different target
	warn := func(err error) {
		fmt.Fprintf(os.Stderr, "kraken: could not read the registry (%v) — resolving %s locally\n", err, sel)
	}
	body, err := qfGet("/api/nodes")
	if err != nil {
		warn(err)
		return nil, "", false
	}
	var resp nodesResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		warn(err)
		return nil, "", false
	}
	return splitNodeSelector(sel, resp.Nodes)
}

// remoteProxyTimeout outlasts the serve-side proxy's own 5s hop.
const remoteProxyTimeout = 15 * time.Second

// sendRemote steers a public cell on another node through this node's serve.
// Inside a cell, KRAKEN_JOB_SLUG (exported into zeus' spawn command) names
// the sending cell, which the receiving zeus sees in its sender prefix.
func sendRemote(n *nodeRowWire, slug, msg string) {
	body, err := qfPostWith("/api/nodes/action", map[string]any{
		"node": n.NodeID, "slug": slug, "action": "steer",
		"payload": map[string]string{"text": msg},
	}, fromCellHeaders(), remoteProxyTimeout)
	if err != nil {
		util.Die("%v", err)
	}
	if s := strings.TrimSpace(string(body)); s != "" {
		fmt.Println(s)
	}
	fmt.Fprintf(os.Stderr, "kraken: sent to %s on %s\n", slug, nodeDisplay(n))
}

// readRemote prints the last n blocks of a public cell on another node, in
// k r's local format.
func readRemote(n *nodeRowWire, slug string, count int) {
	q := url.Values{"node": {n.NodeID}, "slug": {slug}, "n": {strconv.Itoa(count)}}
	body, err := qfGet("/api/nodes/read?" + q.Encode())
	if err != nil {
		util.Die("%v", err)
	}
	var resp struct {
		Blocks []struct {
			Kind string `json:"kind"`
			Text string `json:"text"`
			From string `json:"from"`
		} `json:"blocks"`
		Counted int `json:"counted"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		util.Die("decode read reply: %v", err)
	}
	if resp.Counted == 0 {
		fmt.Println("no output blocks yet")
		return
	}
	if resp.Counted < count {
		fmt.Fprintf(os.Stderr, "kraken: only %d block(s) available\n", resp.Counted)
	}
	color := util.UseColor()
	for i, b := range resp.Blocks {
		if i > 0 {
			fmt.Println()
		}
		printReadBlock(color, b.Kind, b.Text, b.From)
	}
}

func nodeDisplay(n *nodeRowWire) string {
	if n.Label != "" {
		return n.Label
	}
	return n.NodeID
}
