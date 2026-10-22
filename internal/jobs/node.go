// Protocol parity: this file is part of the MULTIPLAYER protocol (CLAUDE.md
// § Protocol parity). The protocol is kraken-core's and identical in every
// fork; a change here lands in kraken-core first, carries "protocol:" in its
// PR title, and is named to the operator as a protocol change before it is
// pushed.

package jobs

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"kraken/internal/core"
	"kraken/internal/util"
)

// node.go — this kraken serve as a NODE of the fleet: whether it takes part at
// all (FleetEnabled), what it calls itself (DeviceLabel), and the envelope it
// answers a sibling's probe with (NodeSelf). Everything here reads the world
// and degrades to blanks: a machine without tailscale is a node with no
// stable id, never an error.

// NodeContract is the API contract a node advertises in its envelope. It is
// kept equal to the api package's apiContract by a test there (api imports
// jobs, so the constant cannot simply be shared the other way round).
const NodeContract = 7

// FleetEnabled reports whether this machine takes part in the fleet. Only
// KRAKEN_NODES=off (case-insensitive, trimmed) turns it off; any other value,
// unset included, leaves it on — a node joins the fleet by existing.
func FleetEnabled() bool {
	return !strings.EqualFold(strings.TrimSpace(os.Getenv("KRAKEN_NODES")), "off")
}

// NodeEnvelope is a node's self-description: GET /api/node's body, the
// announce request's body, and the "node" half of /api/node/jobs.
type NodeEnvelope struct {
	ID       string   `json:"node_id"`
	Label    string   `json:"label"`
	Operator string   `json:"operator"`
	DNSName  string   `json:"dns_name"`
	IPs      []string `json:"ips"`
	Port     int      `json:"port"`
	Build    string   `json:"build"`
	Contract int      `json:"contract"`
}

// MarshalJSON keeps IPs an array on the wire even when empty: a nil slice
// would encode as null, and the wire's rule is that a list is a list.
func (e NodeEnvelope) MarshalJSON() ([]byte, error) {
	type plain NodeEnvelope
	p := plain(e)
	if p.IPs == nil {
		p.IPs = []string{}
	}
	return json.Marshal(p)
}

// NodeJobsReply is GET /api/node/jobs' body as the poller reads it. Jobs is
// kept raw: the poller stores it verbatim and never needs the card shape.
type NodeJobsReply struct {
	Node NodeEnvelope    `json:"node"`
	Jobs json.RawMessage `json:"jobs"`
}

// Seams for DeviceLabel and NodeSelf; tests swap them.
var (
	tailscaleStatus = core.TailscaleStatus
	computerName    = func() (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "scutil", "--get", "ComputerName").Output()
		return strings.TrimSpace(string(out)), err
	}
	hostName = os.Hostname
)

// DeviceLabel is the name this node shows as on every other board:
// KRAKEN_DEVICE_NAME when set, else the Mac's ComputerName (the name carl gave
// it in System Settings), else tailscale's HostName for this device, else
// os.Hostname, else "". Each rung that fails or answers blank falls through;
// nothing here errors.
func DeviceLabel() string {
	if v := strings.TrimSpace(os.Getenv("KRAKEN_DEVICE_NAME")); v != "" {
		return v
	}
	if v, err := computerName(); err == nil && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	if st, err := tailscaleStatus(); err == nil && strings.TrimSpace(st.Self.HostName) != "" {
		return strings.TrimSpace(st.Self.HostName)
	}
	if v, err := hostName(); err == nil && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return ""
}

// NodeSelf builds this node's envelope for port. A tailscale status that
// cannot be read leaves the id, DNS name and IPs blank.
//
// IPs carries the one IPv4 the status parse keeps per device (core's
// TailnetDevice.IP); the parse drops the device's other addresses, so there is
// nothing further to list.
func NodeSelf(port int) NodeEnvelope {
	env := NodeEnvelope{
		Label:    DeviceLabel(),
		Operator: Operator(),
		IPs:      []string{},
		Port:     port,
		Build:    util.Build(),
		Contract: NodeContract,
	}
	if st, err := tailscaleStatus(); err == nil {
		env.ID = st.Self.ID
		env.DNSName = st.Self.DNSName
		if st.Self.IP != "" {
			env.IPs = append(env.IPs, st.Self.IP)
		}
	}
	return env
}

// ServePort is servePort for the api package, whose node and pairing routes
// state this node's envelope.
func ServePort() int { return servePort() }

// servePort is the port this process' kraken serve is bound to, read off the
// address SetHookServeAddr recorded at startup; 7777 when none was recorded or
// it does not parse.
func servePort() int {
	if a := currentHookServeAddr(); a != "" {
		if _, p, err := net.SplitHostPort(a); err == nil {
			if n, err := strconv.Atoi(p); err == nil && n > 0 {
				return n
			}
		}
	}
	return 7777
}

// RemotePrefix is the line a remote steer arrives with in zeus' pane, saying
// who sent it: `[from <label> · cell <slug>]` when another machine's cell sent
// it, `[from <label> · <login>]` when a person did, `[from <label>]` when the
// sender's login is unknown.
func RemotePrefix(label, login, fromCell string) string {
	switch {
	case fromCell != "":
		return "[from " + label + " · cell " + fromCell + "]"
	case login != "":
		return "[from " + label + " · " + login + "]"
	default:
		return "[from " + label + "]"
	}
}
