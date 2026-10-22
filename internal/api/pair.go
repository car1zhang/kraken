// Protocol parity: this file is part of the MULTIPLAYER protocol (CLAUDE.md
// § Protocol parity). The protocol is kraken-core's and identical in every
// fork; a change here lands in kraken-core first, carries "protocol:" in its
// PR title, and is named to the operator as a protocol change before it is
// pushed.

package api

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"kraken/internal/core"
	"kraken/internal/jobs"
	"kraken/internal/store"
	"kraken/internal/util"
)

// pair.go — binding a phone (or a browser on another machine) to this node.
//
// The board stores a salted PBKDF2 verifier of carl's pairing password, never
// the password. A phone does not know which kraken on the tailnet holds the
// password it was given, so it asks every node for a challenge and answers
// each with HMAC(K_node, nonce ‖ node_id ‖ device_label), K_node derived from
// the password and THAT node's salt. Only the node whose verifier matches
// mints a token; every other node sees a proof it can only test by guessing
// the password offline — the exposure the 12-character minimum is for (see
// 53-security-model.md in the job's artifacts).
//
// Credential rule, as for every other secret kraken holds: no password,
// verifier or token ever reaches a log line, an event, a notification or an
// error string. The token is returned once, in the 200 that mints it.

// pairVerifier is the kv row: salt and K base64 RawURL.
type pairVerifier struct {
	Salt  string `json:"salt"`
	Iters int    `json:"iters"`
	K     string `json:"k"`
}

const pairVerifierKey = "pair:verifier"

const (
	pairIters        = 600000
	pairMinPassword  = 12
	pairMaxNonces    = 20
	pairRateMax      = 5
	pairLockFailures = 10
)

var (
	pairNonceTTL = time.Minute
	pairLockout  = 10 * time.Minute
	pairNow      = time.Now
)

// pairLabelMax caps a paired device's stored label.
const pairLabelMax = 120

// deriveVerifier is PBKDF2-HMAC-SHA256(password, salt, iters) → 32 bytes.
func deriveVerifier(password string, salt []byte, iters int) []byte {
	k, err := pbkdf2.Key(sha256.New, password, salt, iters, 32)
	if err != nil {
		// only reachable with a key length or FIPS-mode parameter this code
		// never passes; an empty key matches no proof
		return nil
	}
	return k
}

// pairProof is HMAC-SHA256(k, nonce ‖ nodeID ‖ deviceLabel), raw
// concatenation.
func pairProof(k, nonce []byte, nodeID, deviceLabel string) []byte {
	m := hmac.New(sha256.New, k)
	m.Write(nonce)
	m.Write([]byte(nodeID))
	m.Write([]byte(deviceLabel))
	return m.Sum(nil)
}

// nonceStore holds live challenge nonces. The zero value is ready.
type nonceStore struct {
	mu sync.Mutex
	m  map[string]nonceEntry
}

type nonceEntry struct {
	ip string
	at time.Time
}

// issue mints a 16-byte nonce for ip, refusing when ip already holds
// pairMaxNonces live ones — a client cannot fill memory with challenges.
func (n *nonceStore) issue(ip string) ([]byte, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.m == nil {
		n.m = map[string]nonceEntry{}
	}
	now := pairNow()
	live := 0
	for k, e := range n.m {
		if now.Sub(e.at) > pairNonceTTL {
			delete(n.m, k)
			continue
		}
		if e.ip == ip {
			live++
		}
	}
	if live >= pairMaxNonces {
		return nil, false
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return nil, false
	}
	n.m[string(b)] = nonceEntry{ip: ip, at: now}
	return b, true
}

// consume reports whether nonce is live, and spends it: true once, never
// again, and never for a nonce older than pairNonceTTL.
func (n *nonceStore) consume(nonce []byte) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	e, ok := n.m[string(nonce)]
	if !ok {
		return false
	}
	delete(n.m, string(nonce))
	return pairNow().Sub(e.at) <= pairNonceTTL
}

// pairLimiter rate-limits pairing attempts per source ip. The zero value is
// ready.
type pairLimiter struct {
	mu       sync.Mutex
	attempts map[string][]time.Time
	fails    map[string]int
	locked   map[string]time.Time
}

// allow admits at most pairRateMax attempts per ip per minute, and none while
// the ip is locked out.
func (l *pairLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.attempts == nil {
		l.attempts = map[string][]time.Time{}
	}
	now := pairNow()
	if until, ok := l.locked[ip]; ok {
		if now.Before(until) {
			return false
		}
		delete(l.locked, ip)
	}
	recent := l.attempts[ip][:0]
	for _, at := range l.attempts[ip] {
		if now.Sub(at) < time.Minute {
			recent = append(recent, at)
		}
	}
	if len(recent) >= pairRateMax {
		l.attempts[ip] = recent
		return false
	}
	l.attempts[ip] = append(recent, now)
	return true
}

// fail counts a failed attempt; the pairLockFailures-th locks the ip out for
// pairLockout.
func (l *pairLimiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fails == nil {
		l.fails = map[string]int{}
	}
	if l.locked == nil {
		l.locked = map[string]time.Time{}
	}
	l.fails[ip]++
	if l.fails[ip] >= pairLockFailures {
		l.locked[ip] = pairNow().Add(pairLockout)
		l.fails[ip] = 0
		util.LogEvent("pair", "info", "pair-lockout", "pairing locked out for "+ip+" after repeated failed attempts")
	}
}

// The process' one nonce store and limiter.
var (
	pairNonces   = &nonceStore{}
	pairAttempts = &pairLimiter{}
)

// nodeSelf is this node's envelope as the pairing and node routes state it; a
// seam so tests do not shell out to tailscale or scutil.
var nodeSelf = cachedNodeSelf

// nodeSelfTTL is how long one envelope answers. jobs.NodeSelf shells out
// (scutil, tailscale status), and a challenge and the attempt that follows it
// must state the same node id — a fresh read per request could flap between
// them when tailscale is briefly unreadable.
const nodeSelfTTL = 30 * time.Second

var (
	nodeSelfMu         sync.Mutex
	nodeSelfEnv        jobs.NodeEnvelope
	nodeSelfAt         time.Time
	nodeSelfRefreshing bool
)

// cachedNodeSelf serves the envelope from a ~30 s cache, with one refresher
// at a time once an envelope is cached: while a refresh runs, concurrent
// callers get the previous envelope (the gate's status-snapshot posture).
// Concurrent calls before the first envelope lands each read for themselves.
func cachedNodeSelf() jobs.NodeEnvelope {
	nodeSelfMu.Lock()
	fresh := !nodeSelfAt.IsZero() && time.Since(nodeSelfAt) < nodeSelfTTL
	if fresh || (nodeSelfRefreshing && !nodeSelfAt.IsZero()) {
		env := nodeSelfEnv
		nodeSelfMu.Unlock()
		return env
	}
	nodeSelfRefreshing = true
	nodeSelfMu.Unlock()
	defer func() {
		nodeSelfMu.Lock()
		nodeSelfRefreshing = false
		nodeSelfMu.Unlock()
	}()
	env := jobs.NodeSelf(jobs.ServePort())
	nodeSelfMu.Lock()
	nodeSelfEnv, nodeSelfAt = env, time.Now()
	nodeSelfMu.Unlock()
	return env
}

// loadVerifier reads the kv verifier; ok is false when none is set or it does
// not decode.
func loadVerifier(st *store.Store) (pairVerifier, []byte, []byte, bool) {
	if st == nil {
		return pairVerifier{}, nil, nil, false
	}
	raw, err := st.GetKV(pairVerifierKey)
	if err != nil || raw == "" {
		return pairVerifier{}, nil, nil, false
	}
	var v pairVerifier
	if json.Unmarshal([]byte(raw), &v) != nil {
		return pairVerifier{}, nil, nil, false
	}
	salt, err1 := base64.RawURLEncoding.DecodeString(v.Salt)
	k, err2 := base64.RawURLEncoding.DecodeString(v.K)
	if err1 != nil || err2 != nil || len(k) == 0 || v.Iters <= 0 {
		return pairVerifier{}, nil, nil, false
	}
	return v, salt, k, true
}

// decodeB64URL accepts RawURL, tolerating trailing padding a client added.
func decodeB64URL(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(strings.TrimSpace(s), "="))
}

func callerIP(r *http.Request) string {
	if ip := requestIP(r.RemoteAddr); ip != nil {
		return ip.String()
	}
	return ""
}

// pairPasswordHandler sets, changes or clears the pairing password. Either
// way every paired device is revoked: a changed password means carl no
// longer vouches for whoever learned the old one.
func pairPasswordHandler(w http.ResponseWriter, r *http.Request) {
	if !requireJSONPost(w, r) {
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if !decodeStrictBody(w, r, &req, "this route takes password") {
		return
	}
	if req.Password != "" && len([]rune(req.Password)) < pairMinPassword {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "password must be at least 12 characters"})
		return
	}
	st := jobs.ActiveStore()
	if st == nil {
		webJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "no store wired"})
		return
	}
	if req.Password != "" {
		salt := make([]byte, 16)
		if _, err := rand.Read(salt); err != nil {
			webJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not generate a salt"})
			return
		}
		v := pairVerifier{
			Salt:  base64.RawURLEncoding.EncodeToString(salt),
			Iters: pairIters,
			K:     base64.RawURLEncoding.EncodeToString(deriveVerifier(req.Password, salt, pairIters)),
		}
		b, _ := json.Marshal(v)
		if err := st.SetKV(pairVerifierKey, string(b)); err != nil {
			webJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not store the verifier"})
			return
		}
	} else if err := st.DeleteKV(pairVerifierKey); err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not clear the verifier"})
		return
	}
	n, err := st.DeleteAllPairDevices()
	if err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not revoke paired devices"})
		return
	}
	if req.Password != "" {
		util.LogEvent("pair", "info", "pair-password-set", "pairing password set; paired devices revoked")
	} else {
		util.LogEvent("pair", "info", "pair-password-cleared", "pairing password cleared; paired devices revoked")
	}
	webJSON(w, http.StatusOK, map[string]any{"ok": true, "revoked": n})
}

// pairChallengeHandler hands a caller this node's salt, iteration count and a
// fresh single-use nonce.
func pairChallengeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
		return
	}
	v, _, _, ok := loadVerifier(jobs.ActiveStore())
	if !ok {
		webJSON(w, http.StatusConflict, map[string]string{"error": "no pairing password set"})
		return
	}
	nonce, ok := pairNonces.issue(callerIP(r))
	if !ok {
		webJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many challenges"})
		return
	}
	self := nodeSelf()
	webJSON(w, http.StatusOK, map[string]any{
		"node_id": self.ID, "label": self.Label,
		"salt": v.Salt, "iters": v.Iters,
		"nonce": base64.RawURLEncoding.EncodeToString(nonce),
	})
}

// pairAttemptHandler checks one proof. Match and mismatch share every step up
// to the constant-time compare, so the two take the same path.
func pairAttemptHandler(w http.ResponseWriter, r *http.Request) {
	if !requireJSONPost(w, r) {
		return
	}
	ip := callerIP(r)
	if !pairAttempts.allow(ip) {
		webJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many attempts"})
		return
	}
	var req struct {
		Nonce       string `json:"nonce"`
		Proof       string `json:"proof"`
		DeviceLabel string `json:"device_label"`
	}
	if !decodeStrictBody(w, r, &req, "this route takes nonce, proof and device_label") {
		return
	}
	st := jobs.ActiveStore()
	ver, _, k, ok := loadVerifier(st)
	if !ok {
		webJSON(w, http.StatusConflict, map[string]string{"error": "no pairing password set"})
		return
	}
	noMatch := func() {
		pairAttempts.fail(ip)
		webJSON(w, http.StatusUnauthorized, map[string]string{"error": "no match"})
	}
	nonce, nerr := decodeB64URL(req.Nonce)
	if nerr != nil || !pairNonces.consume(nonce) {
		noMatch()
		return
	}
	self := nodeSelf()
	want := pairProof(k, nonce, self.ID, req.DeviceLabel)
	got, perr := decodeB64URL(req.Proof)
	if perr != nil {
		got = nil
	}
	if subtle.ConstantTimeCompare(want, got) != 1 {
		noMatch()
		return
	}
	// A password change between the verifier read above and this point
	// revokes every device (pairPasswordHandler) — a proof against the OLD
	// verifier must not mint a token that outlives that revocation. Re-read
	// and refuse when the salt moved (a cleared password reads as moved too).
	if now, _, _, still := loadVerifier(st); !still || now.Salt != ver.Salt {
		noMatch()
		return
	}
	tokBytes := make([]byte, 32)
	if _, err := rand.Read(tokBytes); err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not mint a token"})
		return
	}
	token := base64.RawURLEncoding.EncodeToString(tokBytes)
	label := core.TruncateOneLine(req.DeviceLabel, pairLabelMax)
	if _, err := st.AddPairDevice(&store.PairDevice{
		Label: label, TailnetName: identityOf(r).Label(), TokenHash: tokenHash(token),
		CreatedAt: pairNow().UnixMilli(), LastIP: ip,
	}); err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not record the device"})
		return
	}
	util.LogEvent("pair", "info", "pair-ok", "paired device "+label+" from "+ip)
	webJSON(w, http.StatusOK, map[string]any{"token": token, "node_id": self.ID, "label": self.Label})
}

// pairRevokeHandler logs a device out. A paired caller revokes itself (no
// id, or its own id); the owner revokes any device by id.
func pairRevokeHandler(w http.ResponseWriter, r *http.Request) {
	if !requireJSONPost(w, r) {
		return
	}
	var req struct {
		ID *int64 `json:"id"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	st := jobs.ActiveStore()
	if st == nil {
		webJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "no store wired"})
		return
	}
	switch tierOf(r) {
	case TierOwner:
		if req.ID == nil {
			webJSON(w, http.StatusBadRequest, map[string]string{"error": "id required"})
			return
		}
		if err := st.DeletePairDevice(*req.ID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				webJSON(w, http.StatusNotFound, map[string]string{"error": "no such device"})
				return
			}
			webJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not revoke"})
			return
		}
	case TierPaired:
		own := pairDeviceOf(r)
		if own == nil || (req.ID != nil && *req.ID != own.ID) {
			webJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		if err := st.DeletePairDevice(own.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
			webJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not revoke"})
			return
		}
	default:
		webJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	util.LogEvent("pair", "info", "pair-revoked", "paired device revoked")
	webJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// pairDeviceWire is one row of GET /api/pair/devices.
type pairDeviceWire struct {
	ID          int64  `json:"id"`
	Label       string `json:"label"`
	TailnetName string `json:"tailnet_name"`
	CreatedAt   int64  `json:"created_at"`
	LastSeenAt  int64  `json:"last_seen_at"`
	LastIP      string `json:"last_ip"`
}

// pairDevicesHandler lists paired devices for the Devices overlay.
func pairDevicesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
		return
	}
	out := []pairDeviceWire{}
	if st := jobs.ActiveStore(); st != nil {
		ds, err := st.ListPairDevices()
		if err != nil {
			webJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not list devices"})
			return
		}
		for _, d := range ds {
			out = append(out, pairDeviceWire{ID: d.ID, Label: d.Label, TailnetName: d.TailnetName,
				CreatedAt: d.CreatedAt, LastSeenAt: d.LastSeenAt, LastIP: d.LastIP})
		}
	}
	webJSON(w, http.StatusOK, map[string]any{"devices": out})
}
