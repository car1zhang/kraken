// Protocol parity: this file is part of the MULTIPLAYER protocol (CLAUDE.md
// § Protocol parity). The protocol is kraken-core's and identical in every
// fork; a change here lands in kraken-core first, carries "protocol:" in its
// PR title, and is named to the operator as a protocol change before it is
// pushed.

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"kraken/internal/util"
)

// tailscale.go — the one place the binary runs the `tailscale` CLI. The fleet
// (jobs' nodes pass) reads `status --json` to learn which devices share this
// Mac's tailnet, and the request gate (api) reads `whois --json` to learn who a
// caller is. Both are shell-outs to another program's view of the world, so
// both follow the repo's rule for other programs' data: a parse that cannot
// read a field leaves it blank rather than failing, and only malformed JSON as
// a whole is an error.

// TailnetDevice is one device in `tailscale status --json` — this Mac (Self)
// or one of its peers.
type TailnetDevice struct {
	ID, HostName, DNSName, IP, OS, Login string
	Online, Self                         bool
}

// TailnetStatus is the parsed `tailscale status --json`: this device, every
// peer, and the tailnet's MagicDNS suffix.
type TailnetStatus struct {
	Self           TailnetDevice
	Peers          []TailnetDevice
	MagicDNSSuffix string
}

// TailnetIdentity is who `tailscale whois` says an address belongs to. Name is
// the node's MagicDNS name without the trailing dot.
type TailnetIdentity struct {
	ID, Name, Login string
}

// Label is the first DNS label of Name ("iphone173" for
// "iphone173.tail072ad8.ts.net") — the short device name a sender prefix and
// a pairing row carry.
func (t TailnetIdentity) Label() string {
	if t.Name == "" {
		return ""
	}
	label, _, _ := strings.Cut(t.Name, ".")
	return label
}

// ErrNoTailscale is what every exec wrapper returns when no tailscale launcher
// resolves (see ResolveTailscale).
var ErrNoTailscale = errors.New("no tailscale launcher found")

// ErrTailnetUnknown is TailscaleWhois' answer for an address the tailnet does
// not know: the CLI exits non-zero with `peer not found` on stderr. It is an
// ANSWER rather than a fault, which is why WhoisCached caches it.
var ErrTailnetUnknown = errors.New("address is not a tailnet peer")

// statusEntry is the subset of one status --json device entry this package
// reads. UserID is a json.Number because the User map is keyed by the same
// id's decimal string, and a float64 round-trip is what would lose digits.
type statusEntry struct {
	ID           string      `json:"ID"`
	HostName     string      `json:"HostName"`
	DNSName      string      `json:"DNSName"`
	OS           string      `json:"OS"`
	UserID       json.Number `json:"UserID"`
	TailscaleIPs []string    `json:"TailscaleIPs"`
	Online       bool        `json:"Online"`
}

type statusUser struct {
	LoginName string `json:"LoginName"`
}

type statusJSON struct {
	Self           *statusEntry            `json:"Self"`
	Peer           map[string]*statusEntry `json:"Peer"`
	User           map[string]statusUser   `json:"User"`
	MagicDNSSuffix string                  `json:"MagicDNSSuffix"`
}

// ParseTailscaleStatus reads `tailscale status --json`. Peers come out sorted
// by DNS name then ID: the source is a map keyed by node key, whose iteration
// order Go randomizes, and a caller that probes or lists peers should see the
// same order on every call.
func ParseTailscaleStatus(b []byte) (TailnetStatus, error) {
	var raw statusJSON
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&raw); err != nil {
		return TailnetStatus{}, fmt.Errorf("parse tailscale status: %w", err)
	}
	conv := func(e *statusEntry, self bool) TailnetDevice {
		d := TailnetDevice{
			ID:       e.ID,
			HostName: e.HostName,
			DNSName:  strings.TrimSuffix(e.DNSName, "."),
			IP:       firstIPv4(e.TailscaleIPs),
			OS:       e.OS,
			Online:   e.Online,
			Self:     self,
		}
		if u, ok := raw.User[e.UserID.String()]; ok {
			d.Login = u.LoginName
		}
		return d
	}
	st := TailnetStatus{MagicDNSSuffix: raw.MagicDNSSuffix}
	if raw.Self != nil {
		st.Self = conv(raw.Self, true)
	}
	for _, e := range raw.Peer {
		if e == nil {
			continue
		}
		st.Peers = append(st.Peers, conv(e, false))
	}
	sort.Slice(st.Peers, func(a, b int) bool {
		if st.Peers[a].DNSName != st.Peers[b].DNSName {
			return st.Peers[a].DNSName < st.Peers[b].DNSName
		}
		return st.Peers[a].ID < st.Peers[b].ID
	})
	return st, nil
}

// firstIPv4 is the first entry of ips that parses as IPv4, "" when none does.
func firstIPv4(ips []string) string {
	for _, s := range ips {
		if ip := net.ParseIP(strings.TrimSpace(s)); ip != nil && ip.To4() != nil {
			return ip.To4().String()
		}
	}
	return ""
}

type whoisJSON struct {
	Node struct {
		StableID string `json:"StableID"`
		Name     string `json:"Name"`
	} `json:"Node"`
	UserProfile struct {
		LoginName string `json:"LoginName"`
	} `json:"UserProfile"`
}

// ParseTailscaleWhois reads `tailscale whois --json <ip>`.
func ParseTailscaleWhois(b []byte) (TailnetIdentity, error) {
	var raw whoisJSON
	if err := json.Unmarshal(b, &raw); err != nil {
		return TailnetIdentity{}, fmt.Errorf("parse tailscale whois: %w", err)
	}
	return TailnetIdentity{
		ID:    raw.Node.StableID,
		Name:  strings.TrimSuffix(raw.Node.Name, "."),
		Login: raw.UserProfile.LoginName,
	}, nil
}

// tailscaleSystemPaths is the ladder after PATH: the CLI the standalone
// installer links, then the binary inside the Mac App Store app bundle, which
// is the only copy a machine that installed tailscale from the App Store has.
// launchd hands `kraken serve` a PATH that may reach neither, which is why the
// ladder exists at all.
var tailscaleSystemPaths = []string{
	"/usr/local/bin/tailscale",
	"/Applications/Tailscale.app/Contents/MacOS/Tailscale",
}

// ResolveTailscale finds the tailscale launcher. KRAKEN_TAILSCALE, when set,
// is the ONLY candidate — a bare name is looked up on PATH, anything else is
// used as given — and one that does not resolve is ErrNoTailscale rather than
// a fallthrough, so an operator who named a launcher is never silently handed
// a different one. Unset: PATH, then tailscaleSystemPaths.
func ResolveTailscale() (string, error) {
	if c := strings.TrimSpace(os.Getenv("KRAKEN_TAILSCALE")); c != "" {
		p := c
		if strings.HasPrefix(c, "~/") {
			p = filepath.Join(util.Home(), c[2:])
		}
		if !strings.ContainsRune(p, os.PathSeparator) {
			lp, err := exec.LookPath(p)
			if err != nil {
				return "", ErrNoTailscale
			}
			p = lp
		}
		if !isExecutableFile(p) {
			return "", ErrNoTailscale
		}
		return absPath(p), nil
	}
	if p, err := exec.LookPath("tailscale"); err == nil {
		return absPath(p), nil
	}
	for _, p := range tailscaleSystemPaths {
		if isExecutableFile(p) {
			return p, nil
		}
	}
	return "", ErrNoTailscale
}

// tailscaleTimeout bounds one CLI call. The gate can sit behind a whois on a
// request path, so a wedged tailscaled must cost a refusal, never a hang.
const tailscaleTimeout = 5 * time.Second

// runTailscale is the exec seam every wrapper calls; tests swap it. A failed
// call's error carries the CLI's stderr, which is where `peer not found`
// appears.
var runTailscale = func(args ...string) ([]byte, error) {
	bin, err := ResolveTailscale()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), tailscaleTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, args...).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return out, fmt.Errorf("tailscale %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(ee.Stderr)))
		}
		return out, fmt.Errorf("tailscale %s: %v", strings.Join(args, " "), err)
	}
	return out, nil
}

// TailscaleStatus runs and parses `tailscale status --json`.
func TailscaleStatus() (TailnetStatus, error) {
	out, err := runTailscale("status", "--json")
	if err != nil {
		return TailnetStatus{}, err
	}
	return ParseTailscaleStatus(out)
}

// TailscaleWhois runs and parses `tailscale whois --json <ip>`; an address the
// tailnet does not know is ErrTailnetUnknown.
func TailscaleWhois(ip string) (TailnetIdentity, error) {
	out, err := runTailscale("whois", "--json", ip)
	if err != nil {
		if errors.Is(err, ErrNoTailscale) {
			return TailnetIdentity{}, err
		}
		if strings.Contains(err.Error(), "peer not found") || strings.Contains(string(out), "peer not found") {
			return TailnetIdentity{}, ErrTailnetUnknown
		}
		return TailnetIdentity{}, err
	}
	return ParseTailscaleWhois(out)
}

// whoisCacheTTL is how long WhoisCached trusts an answer; whoisNow is its
// clock seam.
var (
	whoisCacheTTL = 5 * time.Minute
	whoisNow      = time.Now
)

type whoisEntry struct {
	id  TailnetIdentity
	err error // nil, ErrTailnetUnknown, or another failure (cached for whoisErrTTL only)
	at  time.Time
}

var (
	whoisMu    sync.Mutex
	whoisCache = map[string]whoisEntry{}
)

// whoisErrTTL is how long any OTHER whois failure (no launcher, a timeout, a
// wedged tailscaled) answers for its IP. Short, because it says nothing about
// the address — but nonzero, so a caller polling every few seconds does not
// cost one exec, and up to tailscaleTimeout of waiting, per request.
var whoisErrTTL = 15 * time.Second

// WhoisCached is TailscaleWhois behind a per-IP cache: the gate asks it on
// requests from tailnet callers, and one exec per request would make every
// board poll from a phone cost a process spawn. Successes AND ErrTailnetUnknown
// are cached for whoisCacheTTL — "not on the tailnet" is a stable answer too.
// Any other error is cached for the much shorter whoisErrTTL.
func WhoisCached(ip string) (TailnetIdentity, error) {
	now := whoisNow()
	whoisMu.Lock()
	if e, ok := whoisCache[ip]; ok {
		ttl := whoisCacheTTL
		if e.err != nil && !errors.Is(e.err, ErrTailnetUnknown) {
			ttl = whoisErrTTL
		}
		if now.Sub(e.at) < ttl {
			whoisMu.Unlock()
			return e.id, e.err
		}
	}
	whoisMu.Unlock()
	id, err := TailscaleWhois(ip)
	if err != nil && errors.Is(err, ErrTailnetUnknown) {
		err = ErrTailnetUnknown
	}
	whoisMu.Lock()
	whoisCache[ip] = whoisEntry{id: id, err: err, at: now}
	whoisMu.Unlock()
	return id, err
}

// tailnetRange is tailscale's CGNAT block, 100.64.0.0/10.
var tailnetRange = &net.IPNet{IP: net.IPv4(100, 64, 0, 0).To4(), Mask: net.CIDRMask(10, 32)}

// IsTailnetIP reports whether ip is in 100.64.0.0/10.
func IsTailnetIP(ip net.IP) bool {
	ip4 := ip.To4()
	return ip4 != nil && tailnetRange.Contains(ip4)
}

// TailscaleIP picks the first IPv4 address in addrs that falls in tailscale's
// CGNAT range, 100.64.0.0/10. A pure function over an address list so it's
// testable without touching real interfaces. (Moved beside the tailscale
// adapter; web keeps a thin alias so its tests run unchanged.)
func TailscaleIP(addrs []net.Addr) (string, bool) {
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		default:
			continue
		}
		if IsTailnetIP(ip) {
			return ip.To4().String(), true
		}
	}
	return "", false
}

// DetectTailscaleIP gathers every local interface address and picks the
// tailnet one — the thin caller around TailscaleIP. A var so callers' tests
// can stub tailscale coming up without a real tailnet interface.
var DetectTailscaleIP = func() (string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", fmt.Errorf("enumerate network interfaces: %v", err)
	}
	var addrs []net.Addr
	for _, iface := range ifaces {
		a, err := iface.Addrs()
		if err != nil {
			continue // interface came and went; not fatal to the scan
		}
		addrs = append(addrs, a...)
	}
	ip, ok := TailscaleIP(addrs)
	if !ok {
		return "", fmt.Errorf("no tailscale interface (100.64.0.0/10) found — is tailscale up?")
	}
	return ip, nil
}
