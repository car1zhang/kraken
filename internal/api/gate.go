package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"mime"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"kraken/internal/core"
	"kraken/internal/jobs"
	"kraken/internal/store"
	"kraken/internal/util"
)

// gate.go — the request gate: every request either listener serves passes
// through Gate, which decides WHO is asking and refuses a route whose floor
// is above that. Four tiers, ordered:
//
//   - owner: the Mac itself (isLocalOrigin — loopback or one of its own
//     interface addresses). Everything, exactly as before the gate existed,
//     which is why every cell hook, CLI verb, the mac app and the board
//     opened on this Mac are unaffected.
//   - paired: a bearer token (header, or the kraken_pair cookie for the
//     <img>/<iframe>/WKWebView loads that cannot carry a header) whose
//     SHA-256 matches a pair_devices row. Full control of this node.
//   - node: any other device on the tailnet (tailscale status snapshot first,
//     whois as the fallback). The node API, pairing, and the SPA shell.
//   - anon: everything else. /api/health only.
//
// The tailnet is the only network a kraken serve listens on besides loopback,
// so "node" is not a weaker version of "paired": it is every other person's
// machine on the tailnet, which is why it reaches only what a sibling kraken
// needs and the per-job visibility check does the rest.

// Tier is a caller's standing. A request passes a route when tier >= floor.
type Tier int

const (
	TierAnon Tier = iota
	TierNode
	TierPaired
	TierOwner
)

// gateDeps are the gate's three lookups; tests swap defaultGateDeps.
type gateDeps struct {
	localOrigin   func(remoteAddr string) bool
	lookupToken   func(hash string) (*store.PairDevice, bool)
	tailnetDevice func(ip string) (core.TailnetIdentity, bool)
}

var defaultGateDeps = gateDeps{
	localOrigin:   isLocalOrigin,
	lookupToken:   lookupPairToken,
	tailnetDevice: tailnetDeviceFor,
}

// lookupPairToken finds the paired device holding a token hash in the wired
// store; no store, no device.
func lookupPairToken(hash string) (*store.PairDevice, bool) {
	st := jobs.ActiveStore()
	if st == nil || hash == "" {
		return nil, false
	}
	d, err := st.GetPairDeviceByHash(hash)
	if err != nil {
		return nil, false
	}
	return d, true
}

// gateStatus is the status read behind the snapshot; a seam like every other
// shell-out in this package.
var gateStatus = core.TailscaleStatus

// gateSnapshotTTL is how long one `tailscale status` answers the gate. The
// snapshot is the cheap path (one exec answers every device); whois is the
// per-IP fallback for a device that joined since.
const gateSnapshotTTL = 30 * time.Second

var (
	gateSnapMu         sync.Mutex
	gateSnap           core.TailnetStatus
	gateSnapAt         time.Time
	gateSnapErr        error
	gateSnapRefreshing bool
)

// refreshGateSnapshot runs the one status read; the single-flight flag is
// cleared by defer, so a panicking read cannot leave every later caller on a
// snapshot that never refreshes.
func refreshGateSnapshot() {
	defer func() {
		gateSnapMu.Lock()
		gateSnapRefreshing = false
		gateSnapMu.Unlock()
	}()
	s, err := gateStatus()
	gateSnapMu.Lock()
	gateSnap, gateSnapErr, gateSnapAt = s, err, time.Now()
	gateSnapMu.Unlock()
}

// tailscaleULA is tailscale's IPv6 range, fd7a:115c:a1e0::/48.
var tailscaleULA = &net.IPNet{IP: net.ParseIP("fd7a:115c:a1e0::"), Mask: net.CIDRMask(48, 128)}

// tailnetDeviceFor resolves a caller ip to a tailnet identity: the status
// snapshot first, whois second. An address outside both of tailscale's
// ranges is answered no without an exec — it cannot be a tailnet device, and
// the gate sits on every request.
func tailnetDeviceFor(ip string) (core.TailnetIdentity, bool) {
	parsed := net.ParseIP(ip)
	if parsed == nil || !(core.IsTailnetIP(parsed) || tailscaleULA.Contains(parsed)) {
		return core.TailnetIdentity{}, false
	}
	// Refresh single-flight, never holding the lock across the exec: one
	// caller runs `tailscale status`, every concurrent caller reads the
	// previous snapshot (or, before the first one lands, none — and falls to
	// whois) rather than queueing behind a slow tailscaled.
	gateSnapMu.Lock()
	refresh := !gateSnapRefreshing && (gateSnapAt.IsZero() || time.Since(gateSnapAt) > gateSnapshotTTL)
	if refresh {
		gateSnapRefreshing = true
	}
	gateSnapMu.Unlock()
	if refresh {
		refreshGateSnapshot()
	}
	gateSnapMu.Lock()
	snap, snapErr := gateSnap, gateSnapErr
	gateSnapMu.Unlock()
	if snapErr == nil {
		for _, d := range append([]core.TailnetDevice{snap.Self}, snap.Peers...) {
			if d.IP != "" && d.IP == parsed.String() {
				return core.TailnetIdentity{ID: d.ID, Name: d.DNSName, Login: d.Login}, true
			}
		}
	}
	id, err := core.WhoisCached(parsed.String())
	if err != nil {
		return core.TailnetIdentity{}, false
	}
	return id, true
}

// multipartPosts are the only POST routes whose shipped callers send a body
// that is not JSON: both are file uploads, posted as multipart/form-data by
// the SPA (client.ts uploadImage/uploadBackground) and iOS (APIClient's
// image upload). multipart/form-data is itself a CORS simple-request type, so
// the JSON rule cannot cover them (requiring JSON would break every shipped
// uploader); postOriginOK is what refuses a browser's cross-origin POST to
// them.
var multipartPosts = map[string]bool{"/api/upload": true, "/api/background": true}

// postContentTypeOK is the gate's JSON rule for writes: every POST under
// /api/ other than multipartPosts must carry Content-Type application/json,
// requireJSONPost's rule applied to the whole API in one place so no write
// route (and no route added later) can miss it. A JSON POST is never a CORS
// simple request, so a page on another origin — or a sandboxed, opaque-origin
// frame on this one — has to preflight it, and this server answers no
// preflight with CORS headers. That matters most for the owner tier, which
// is classified by address alone: without the rule, a script in any page the
// owner's browser loads could fire a text/plain POST at loopback and have it
// run as owner. Every shipped caller already sends the header (the SPA's
// postJSON, iOS's post and pairing requests, the CLI's qfPost and hookPost,
// and the cell hooks' client.Post calls).
func postContentTypeOK(r *http.Request) bool {
	if r.Method != http.MethodPost || !strings.HasPrefix(r.URL.Path, "/api/") || multipartPosts[r.URL.Path] {
		return true
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && mediaType == "application/json"
}

// gateRefuse writes one of the gate's own refusals: a JSON error body, with
// nosniff like every other reply the node routes write by hand.
func gateRefuse(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	webJSON(w, status, map[string]string{"error": msg})
}

// postOriginOK refuses a POST that a browser marked as coming from another
// origin. A browser sends Origin on every cross-origin POST, and the literal
// "null" from an opaque origin (a sandboxed frame, among others), so a
// present Origin that is not this server's own — "http://" + r.Host, or
// "https://" when the request arrived over TLS — is refused, "null"
// included. An ABSENT Origin passes: iOS (URLSession), the CLI, the cell
// hooks and node-to-node calls send none, while a browser always sends it on
// a cross-origin POST. This is what refuses a browser's cross-origin POST to
// the two multipart upload routes, which the JSON rule cannot cover.
//
// What it does NOT cover: a DNS-rebinding page. Its origin is a hostname
// that the attacker re-points at 127.0.0.1, so its Origin equals the Host it
// sends, the request arrives from loopback, and the gate classifies it
// owner. Closing that needs a Host allowlist, which is a separate change.
//
// The comparison relies on r.Host carrying the port exactly as the page's
// own origin does. Checked for this server: Go's server takes r.Host from
// the Host header, which a browser writes as host:port for a non-default
// port (127.0.0.1:7878 on both sides); the board is served over plain HTTP
// (web/serve.go calls srv.Serve on a plain listener and imports no TLS); and
// the vite dev proxy keeps the browser's Host (changeOrigin: false), so a
// dev page's http://localhost:5173 still matches its own Host.
func postOriginOK(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return true
	}
	origin, present := r.Header["Origin"]
	if !present {
		return true
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return len(origin) == 1 && strings.EqualFold(origin[0], scheme+"://"+r.Host)
}

// pairCookieName is the cookie a paired client's header-less loads carry.
const pairCookieName = "kraken_pair"

// pairTokenFromRequest is the caller's pairing token: the Authorization bearer
// wins, then the kraken_pair cookie, else "".
func pairTokenFromRequest(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		if scheme, tok, ok := strings.Cut(h, " "); ok && strings.EqualFold(scheme, "Bearer") {
			if tok = strings.TrimSpace(tok); tok != "" {
				return tok
			}
		}
	}
	if c, err := r.Cookie(pairCookieName); err == nil && c.Value != "" {
		return c.Value
	}
	return ""
}

// tokenHash is the lowercase hex SHA-256 of a token — the only form a token
// is ever stored or compared in.
func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// gateRequestIP is requestIP with one stricter rule: a RemoteAddr whose
// brackets do not balance ("[::1", "::1]") is unparseable. requestIP trims
// brackets leniently because a synthesized request may carry a bare address,
// which would read "[::1" as loopback; the gate fails closed instead.
func gateRequestIP(remoteAddr string) net.IP {
	if strings.Count(remoteAddr, "[") != strings.Count(remoteAddr, "]") {
		return nil
	}
	return requestIP(remoteAddr)
}

// classify decides a request's tier and the identity that goes with it.
func classify(r *http.Request, deps gateDeps) (Tier, core.TailnetIdentity) {
	tier, id, _ := classifyDevice(r, deps)
	return tier, id
}

// classifyDevice is classify plus the paired device row, which the gate needs
// to touch and the revoke route needs to name "this caller's own row".
func classifyDevice(r *http.Request, deps gateDeps) (Tier, core.TailnetIdentity, *store.PairDevice) {
	ip := gateRequestIP(r.RemoteAddr)
	if ip == nil {
		return TierAnon, core.TailnetIdentity{}, nil // fail closed, token or not
	}
	if deps.localOrigin != nil && deps.localOrigin(r.RemoteAddr) {
		return TierOwner, core.TailnetIdentity{}, nil
	}
	if t := pairTokenFromRequest(r); t != "" && deps.lookupToken != nil {
		if d, ok := deps.lookupToken(tokenHash(t)); ok && d != nil {
			return TierPaired, core.TailnetIdentity{Name: d.TailnetName}, d
		}
	}
	if deps.tailnetDevice != nil {
		if id, ok := deps.tailnetDevice(ip.String()); ok {
			return TierNode, id, nil
		}
	}
	return TierAnon, core.TailnetIdentity{}, nil
}

// routePolicy is every route's floor. A route Register adds without an entry
// here falls to policyFor's fail-closed default (owner), and a test holds
// RoutePatterns against this table so that never happens silently.
var routePolicy = func() map[string]Tier {
	m := map[string]Tier{
		"/api/health": TierAnon,

		"/api/node":             TierNode,
		"/api/node/jobs":        TierNode,
		"/api/node/job":         TierNode,
		"/api/node/job/raw":     TierNode,
		"/api/node/read":        TierNode,
		"/api/node/artifact":    TierNode,
		"/api/node/transcript":  TierNode,
		"/api/node/thread/read": TierNode,
		"/api/node/announce":    TierNode,
		"/api/node/action":      TierNode,
		"/api/pair/challenge":   TierNode,
		"/api/pair":             TierNode,
		// an unpaired phone fans its pairing out over the registry its seed
		// serves, so the registry list itself is node tier; the proxies that
		// act through it stay paired
		"/api/nodes": TierNode,

		"/api/pair/password": TierOwner,
		"/api/pair/devices":  TierOwner,
		"/api/editor/open":   TierOwner,

		"/api/pair/revoke": TierPaired,
		"/api/artifact/":   TierPaired,
		"/api/upload/":     TierPaired,
	}
	for _, p := range RoutePatterns() {
		if _, ok := m[p]; !ok {
			m[p] = TierPaired
		}
	}
	return m
}()

// policyFor is the floor for a request path: its exact entry, else the
// longest subtree entry (a pattern ending in "/") it falls under, else owner
// for anything under /api/ — a route nobody gave a policy is refused rather
// than opened — else node, which is the SPA shell and its assets.
func policyFor(path string) Tier {
	if t, ok := routePolicy[path]; ok {
		return t
	}
	best, bestLen := Tier(0), -1
	for p, t := range routePolicy {
		if strings.HasSuffix(p, "/") && strings.HasPrefix(path, p) && len(p) > bestLen {
			best, bestLen = t, len(p)
		}
	}
	if bestLen >= 0 {
		return best
	}
	if strings.HasPrefix(path, "/api/") || path == "/api" {
		return TierOwner
	}
	return TierNode
}

type gateCtxKey int

const (
	gateTierKey gateCtxKey = iota
	gateIdentityKey
	gateDeviceKey
)

// gateInfo is what Gate attached to a request, or (for a request that never
// went through Gate — a handler test calling a handler directly) what
// classifying it now says.
func gateInfo(r *http.Request) (Tier, core.TailnetIdentity, *store.PairDevice) {
	if t, ok := r.Context().Value(gateTierKey).(Tier); ok {
		id, _ := r.Context().Value(gateIdentityKey).(core.TailnetIdentity)
		d, _ := r.Context().Value(gateDeviceKey).(*store.PairDevice)
		return t, id, d
	}
	return classifyDevice(r, defaultGateDeps)
}

// tierOf is the caller's tier.
func tierOf(r *http.Request) Tier {
	t, _, _ := gateInfo(r)
	return t
}

// identityOf is the caller's tailnet identity (empty for the owner).
func identityOf(r *http.Request) core.TailnetIdentity {
	_, id, _ := gateInfo(r)
	return id
}

// pairDeviceOf is the paired caller's own device row, nil for other tiers.
func pairDeviceOf(r *http.Request) *store.PairDevice {
	_, _, d := gateInfo(r)
	return d
}

// gateLogGap is how often one (ip, path) refusal may reach the event log, so
// a misconfigured client polling every few seconds cannot flood it.
const gateLogGap = 10 * time.Minute

// pairTouchGap bounds how often a paired device's last-seen is written.
const pairTouchGap = time.Minute

var (
	gateLogMu   sync.Mutex
	gateLogged  = map[string]time.Time{}
	gateTouchMu sync.Mutex
	gateTouched = map[int64]time.Time{}
)

// logRefusal records a refusal once per (ip, path) per gateLogGap. The token,
// if any, is never part of the line.
func logRefusal(ip, path string, tier, floor Tier) {
	now := time.Now()
	key := ip + " " + path
	gateLogMu.Lock()
	if at, ok := gateLogged[key]; ok && now.Sub(at) < gateLogGap {
		gateLogMu.Unlock()
		return
	}
	if len(gateLogged) > 4096 {
		for k, at := range gateLogged {
			if now.Sub(at) >= gateLogGap {
				delete(gateLogged, k)
			}
		}
	}
	gateLogged[key] = now
	gateLogMu.Unlock()
	util.LogEvent("gate", "info", "gate-refused", "refused "+ip+" "+core.TruncateOneLine(path, 120)+" ("+tierName(tier)+" below "+tierName(floor)+")")
}

func tierName(t Tier) string {
	switch t {
	case TierOwner:
		return "owner"
	case TierPaired:
		return "paired"
	case TierNode:
		return "node"
	default:
		return "anon"
	}
}

// touchPaired stamps a paired device's last-seen at most once per
// pairTouchGap.
func touchPaired(d *store.PairDevice, ip string) {
	st := jobs.ActiveStore()
	if st == nil || d == nil {
		return
	}
	now := time.Now()
	gateTouchMu.Lock()
	if at, ok := gateTouched[d.ID]; ok && now.Sub(at) < pairTouchGap {
		gateTouchMu.Unlock()
		return
	}
	gateTouched[d.ID] = now
	gateTouchMu.Unlock()
	_ = st.TouchPairDevice(d.ID, now.UnixMilli(), ip)
}

// Gate wraps the mux both listeners serve. It classifies the request, puts
// the tier and identity on its context, and answers 401 when the tier is
// below the route's floor; past the floor, a POST carrying a foreign Origin
// answers 403 (postOriginOK) and a POST under /api/ without a JSON
// Content-Type answers 415 (postContentTypeOK). Every refusal carries
// nosniff (gateRefuse). It runs under --mirror too: who may reach a
// mirror's board is the same question.
func Gate(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tier, id, dev := classifyDevice(r, defaultGateDeps)
		floor := policyFor(r.URL.Path)
		ipText := ""
		if ip := requestIP(r.RemoteAddr); ip != nil {
			ipText = ip.String()
		}
		if tier < floor {
			logRefusal(ipText, r.URL.Path, tier, floor)
			gateRefuse(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if !postOriginOK(r) {
			gateRefuse(w, http.StatusForbidden, "cross-origin post refused")
			return
		}
		if !postContentTypeOK(r) {
			gateRefuse(w, http.StatusUnsupportedMediaType, "content-type must be application/json")
			return
		}
		if tier == TierPaired {
			touchPaired(dev, ipText)
		}
		ctx := context.WithValue(r.Context(), gateTierKey, tier)
		ctx = context.WithValue(ctx, gateIdentityKey, id)
		ctx = context.WithValue(ctx, gateDeviceKey, dev)
		h.ServeHTTP(w, r.WithContext(ctx))
	})
}
