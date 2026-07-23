package core

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"kraken/internal/util"
)

// --- pinned wire types (see kraken-usage-tracker brief) ---

type UsageWindow struct {
	Percent  float64 `json:"percent"`
	ResetsAt int64   `json:"resets_at,omitempty"` // unix millis; 0 = unknown
}

type ModelWindow struct {
	Name     string  `json:"name"` // model display name, e.g. "Fable"
	Percent  float64 `json:"percent"`
	ResetsAt int64   `json:"resets_at,omitempty"`
}

// AccountUsage is one account's usage row. Window pointers are nil when the
// account couldn't be fetched (no creds / reauth / error with no prior data).
type AccountUsage struct {
	Slug          string        `json:"slug"` // "main" or the pool slot dir name
	Email         string        `json:"email,omitempty"`
	DisplayName   string        `json:"display_name,omitempty"`
	Tier          string        `json:"tier,omitempty"` // raw rate_limit_tier
	Current       bool          `json:"current,omitempty"`
	Session       *UsageWindow  `json:"session,omitempty"`
	Weekly        *UsageWindow  `json:"weekly,omitempty"`
	Models        []ModelWindow `json:"models,omitempty"`     // weekly_scoped limits
	FetchedAt     int64         `json:"fetched_at,omitempty"` // unix millis of last successful fetch
	Stale         bool          `json:"stale,omitempty"`
	Error         string        `json:"error,omitempty"`
	Reauth        bool          `json:"reauth,omitempty"` // refresh failed: run ccp login
	NoCreds       bool          `json:"no_creds,omitempty"`
	CooldownUntil int64         `json:"cooldown_until,omitempty"` // unix millis, advisory
}

type UsageSnapshot struct {
	FetchedAt int64          `json:"fetched_at"` // unix millis of last completed pass
	Accounts  []AccountUsage `json:"accounts"`
}

// --- endpoints + shell-outs, isolated for tests ---

var (
	usageEndpoint = "https://api.anthropic.com/api/oauth/usage"
	tokenEndpoint = "https://console.anthropic.com/v1/oauth/token"

	// oauthClientID is Claude Code's public OAuth client id.
	oauthClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"

	httpClient = &http.Client{Timeout: 8 * time.Second}

	securityFind = func(service, account string) (string, error) {
		out, err := exec.Command("security", "find-generic-password", "-a", account, "-s", service, "-w").Output()
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(out)), nil
	}
	// securityAdd's value rides argv to `security add-generic-password -w` —
	// briefly visible in `ps` output. security(1) offers no stdin form; ccp
	// accepts the same exposure.
	securityAdd = func(service, account, value string) error {
		return exec.Command("security", "add-generic-password", "-U", "-a", account, "-s", service, "-w", value).Run()
	}
)

// keychainAccount resolves the macOS account name to qualify every
// find/add-generic-password call with, matching ccp's `-a "$USER"`
// convention so kraken and ccp always address the same keychain item. A
// mismatch (e.g. an empty $USER under launchd, or a different resolved
// identity than whatever wrote the entry) creates a DUPLICATE keychain item
// under a different account — since find-generic-password without -a can
// return either one nondeterministically, a stale duplicate can silently
// shadow the fresh .credentials.json on read. Resolving once and reusing it
// for every call avoids that split-brain. If no identity resolves at all,
// keychain ops are skipped entirely (silent degrade) and callers fall back
// to the file path.
func keychainAccount() (string, bool) {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username, true
	}
	if v := os.Getenv("USER"); v != "" {
		return v, true
	}
	return "", false
}

// --- oauth credential shape ---

type claudeAiOauthFields struct {
	AccessToken      string   `json:"accessToken"`
	RefreshToken     string   `json:"refreshToken"`
	ExpiresAt        int64    `json:"expiresAt"` // unix millis
	Scopes           []string `json:"scopes"`
	SubscriptionType string   `json:"subscriptionType"`
	RateLimitTier    string   `json:"rateLimitTier"`
}

// oauthCreds is the parsed claudeAiOauth block plus the full original
// document (raw), keyed by top-level field, so a refresh write-back
// preserves sibling keys (e.g. mcpOAuth) untouched instead of clobbering
// them.
type oauthCreds struct {
	ClaudeAiOauth claudeAiOauthFields
	raw           map[string]json.RawMessage
}

func (c oauthCreds) expired(now time.Time) bool {
	if c.ClaudeAiOauth.ExpiresAt == 0 {
		return false
	}
	return c.ClaudeAiOauth.ExpiresAt <= now.UnixMilli()+60_000
}

// decodeOauthCreds parses a .credentials.json-shaped document, keeping
// unrecognized top-level keys around for round-tripping on write-back.
func decodeOauthCreds(data []byte) (oauthCreds, bool) {
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil {
		return oauthCreds{}, false
	}
	coRaw, ok := raw["claudeAiOauth"]
	if !ok {
		return oauthCreds{}, false
	}
	var co claudeAiOauthFields
	if json.Unmarshal(coRaw, &co) != nil || co.AccessToken == "" {
		return oauthCreds{}, false
	}
	return oauthCreds{ClaudeAiOauth: co, raw: raw}, true
}

// withClaudeAiOauth returns a copy of c with its claudeAiOauth block
// replaced, leaving all other top-level keys untouched.
func (c oauthCreds) withClaudeAiOauth(co claudeAiOauthFields) oauthCreds {
	raw := make(map[string]json.RawMessage, len(c.raw))
	for k, v := range c.raw {
		raw[k] = v
	}
	coData, _ := json.Marshal(co)
	raw["claudeAiOauth"] = coData
	return oauthCreds{ClaudeAiOauth: co, raw: raw}
}

// marshal serializes the full original document with the current
// claudeAiOauth block, preserving any other top-level keys.
func (c oauthCreds) marshal() ([]byte, error) {
	if c.raw == nil {
		return json.Marshal(map[string]claudeAiOauthFields{"claudeAiOauth": c.ClaudeAiOauth})
	}
	return json.Marshal(c.raw)
}

// --- account identity ---

type accountMeta struct {
	Email       string
	DisplayName string
	Tier        string
}

// PoolDir returns the claude code pool root: $CLAUDE_POOL_DIR or ~/.claude-pool.
func PoolDir() string {
	if d := os.Getenv("CLAUDE_POOL_DIR"); d != "" {
		return d
	}
	return filepath.Join(util.Home(), ".claude-pool")
}

// poolSlugs lists pool slot directory names, sorted.
func poolSlugs() []string {
	entries, err := os.ReadDir(PoolDir())
	if err != nil {
		return nil
	}
	var slugs []string
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		slugs = append(slugs, e.Name())
	}
	sort.Strings(slugs)
	return slugs
}

// mainKeychainService is the keychain service name for the main account's
// Claude Code credentials.
const mainKeychainService = "Claude Code-credentials"

// slotKeychainService mirrors ccp's slot_keychain_service: the service name
// is "Claude Code-credentials-" + first 8 hex chars of sha256(slot dir path).
// The path should be NFC-normalized first, but pool paths are ASCII-only in
// practice so raw bytes match what ccp computes.
func slotKeychainService(slotDir string) string {
	sum := sha256.Sum256([]byte(slotDir))
	return "Claude Code-credentials-" + hex.EncodeToString(sum[:])[:8]
}

func mainAccountMeta() accountMeta {
	var meta accountMeta
	data, err := os.ReadFile(filepath.Join(util.Home(), ".claude.json"))
	if err != nil {
		return meta
	}
	var doc struct {
		OauthAccount struct {
			EmailAddress              string `json:"emailAddress"`
			DisplayName               string `json:"displayName"`
			OrganizationRateLimitTier string `json:"organizationRateLimitTier"`
			UserRateLimitTier         string `json:"userRateLimitTier"`
		} `json:"oauthAccount"`
	}
	if json.Unmarshal(data, &doc) != nil {
		return meta
	}
	meta.Email = doc.OauthAccount.EmailAddress
	meta.DisplayName = doc.OauthAccount.DisplayName
	meta.Tier = doc.OauthAccount.OrganizationRateLimitTier
	if meta.Tier == "" {
		meta.Tier = doc.OauthAccount.UserRateLimitTier
	}
	return meta
}

func slotAccountMeta(slotDir string) accountMeta {
	var meta accountMeta
	data, err := os.ReadFile(filepath.Join(slotDir, "account.json"))
	if err != nil {
		return meta
	}
	var doc struct {
		Email         string `json:"email"`
		DisplayName   string `json:"display_name"`
		RateLimitTier string `json:"rate_limit_tier"`
	}
	if json.Unmarshal(data, &doc) != nil {
		return meta
	}
	meta.Email = doc.Email
	meta.DisplayName = doc.DisplayName
	meta.Tier = doc.RateLimitTier
	return meta
}

// --- credential loading ---

// loadMainCreds reads main-account creds from the macOS keychain.
func loadMainCreds() (oauthCreds, bool) {
	account, ok := keychainAccount()
	if !ok {
		return oauthCreds{}, false
	}
	raw, err := securityFind(mainKeychainService, account)
	if err != nil || raw == "" {
		return oauthCreds{}, false
	}
	return decodeOauthCreds([]byte(raw))
}

// loadSlotCreds prefers the slot's keychain entry, falling back to the
// .credentials.json file, matching how ccp treats keychain-as-authoritative
// on read.
func loadSlotCreds(slotDir string) (oauthCreds, bool) {
	if account, ok := keychainAccount(); ok {
		if raw, err := securityFind(slotKeychainService(slotDir), account); err == nil && raw != "" {
			if c, ok := decodeOauthCreds([]byte(raw)); ok {
				return c, true
			}
		}
	}
	data, err := os.ReadFile(filepath.Join(slotDir, ".credentials.json"))
	if err != nil {
		return oauthCreds{}, false
	}
	return decodeOauthCreds(data)
}

// --- token refresh ---

type refreshResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"` // seconds
	Error        string `json:"error"`
}

// refreshOutcome distinguishes why a token refresh didn't produce a usable
// token, since the caller must react very differently: refreshInvalidGrant
// means the refresh token itself is dead (reauth required), while
// refreshTransient means the exchange merely failed this one time (network
// blip, 5xx, unparseable body) and should be treated as retry-next-pass, not
// a reauth prompt.
type refreshOutcome int

const (
	refreshOK refreshOutcome = iota
	refreshInvalidGrant
	refreshTransient
)

// permanentRefreshErrors are OAuth error codes indicating the refresh token
// itself is no longer valid (revoked, rotated out from under this slot).
// Anything else — including any error code on a non-400/401 status — is
// treated as transient.
var permanentRefreshErrors = map[string]bool{
	"invalid_grant":  true,
	"invalid_client": true,
}

// refreshToken exchanges a refresh token for a new access token. On success,
// returns the updated credentials (with claudeAiOauth fields other than the
// token trio preserved from the input). reason is a short, body-free
// description for non-OK outcomes, suitable for surfacing in AccountUsage.Error.
func refreshToken(creds oauthCreds, now time.Time) (updated oauthCreds, outcome refreshOutcome, reason string) {
	body, _ := json.Marshal(map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": creds.ClaudeAiOauth.RefreshToken,
		"client_id":     oauthClientID,
	})
	req, err := http.NewRequest(http.MethodPost, tokenEndpoint, bytes.NewReader(body))
	if err != nil {
		return creds, refreshTransient, "request build failed"
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return creds, refreshTransient, "network error"
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return creds, refreshTransient, "response read failed"
	}
	var rr refreshResponse
	parseErr := json.Unmarshal(data, &rr)
	if parseErr == nil && resp.StatusCode == http.StatusOK && rr.AccessToken != "" {
		co := creds.ClaudeAiOauth
		co.AccessToken = rr.AccessToken
		if rr.RefreshToken != "" {
			co.RefreshToken = rr.RefreshToken
		}
		co.ExpiresAt = now.UnixMilli() + rr.ExpiresIn*1000
		return creds.withClaudeAiOauth(co), refreshOK, ""
	}
	if parseErr == nil && (resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized) && permanentRefreshErrors[rr.Error] {
		return creds, refreshInvalidGrant, rr.Error
	}
	return creds, refreshTransient, fmt.Sprintf("unexpected status %d", resp.StatusCode)
}

// writeSlotCreds atomically writes creds to <slotDir>/.credentials.json with
// 0600 perms, then best-effort mirrors them into the slot's keychain entry.
func writeSlotCreds(slotDir string, creds oauthCreds) error {
	data, err := creds.marshal()
	if err != nil {
		return err
	}
	if err := atomicWriteFile(filepath.Join(slotDir, ".credentials.json"), data, 0600); err != nil {
		return err
	}
	// Keychain mirror is best-effort: the file is the store other tooling
	// (ccp, the orchestrator) validates against.
	if account, ok := keychainAccount(); ok {
		_ = securityAdd(slotKeychainService(slotDir), account, string(data))
	}
	return nil
}

// writeMainCreds mirrors refreshed creds into the main keychain entry.
func writeMainCreds(creds oauthCreds) error {
	data, err := creds.marshal()
	if err != nil {
		return err
	}
	account, ok := keychainAccount()
	if !ok {
		return fmt.Errorf("keychain account unresolved")
	}
	return securityAdd(mainKeychainService, account, string(data))
}

func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once renamed
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, perm); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// resolveToken returns a usable access token for the account, refreshing and
// persisting if the current one is expired. isSlot/slotDir select the
// persistence target; for the main account slotDir is ignored.
//
// writeWarn is a soft error: set only when refresh succeeded but persisting
// it failed. The token is still returned and usable for this pass (the
// persist retries naturally next pass); the caller should surface writeWarn
// via the row's Error only if the usage fetch that follows also fails —
// usable data beats a spurious error when the fetch itself would've worked.
func resolveToken(creds oauthCreds, isSlot bool, slotDir string) (token string, reauth bool, errMsg string, writeWarn string) {
	now := time.Now()
	if !creds.expired(now) {
		return creds.ClaudeAiOauth.AccessToken, false, "", ""
	}
	updated, outcome, reason := refreshToken(creds, now)
	if outcome == refreshOK {
		var writeErr error
		if isSlot {
			writeErr = writeSlotCreds(slotDir, updated)
		} else {
			writeErr = writeMainCreds(updated)
		}
		if writeErr != nil {
			return updated.ClaudeAiOauth.AccessToken, false, "", fmt.Sprintf("refresh write failed: %v", writeErr)
		}
		return updated.ClaudeAiOauth.AccessToken, false, "", ""
	}

	// Refresh failed — someone else may have rotated the token concurrently.
	// Re-read once from the source of truth; if it's now unexpired, use it.
	var reread oauthCreds
	var rereadOK bool
	if isSlot {
		reread, rereadOK = loadSlotCreds(slotDir)
	} else {
		reread, rereadOK = loadMainCreds()
	}
	if rereadOK && !reread.expired(now) {
		return reread.ClaudeAiOauth.AccessToken, false, "", ""
	}

	if outcome == refreshInvalidGrant {
		return "", true, "token refresh failed (reauth required)", ""
	}
	// Transient: leave Reauth false so a token-endpoint blip never tells the
	// UI to run ccp login. No response body contents surface here — reason
	// is a short fixed description, never rr.Error/rr fields verbatim.
	return "", false, fmt.Sprintf("token refresh failed: %s", reason), ""
}

// --- usage response parsing ---
//
// usageLimitEntry/usageWindowFallback/usageResponse intentionally mirror
// (rather than reuse) usage.go's usageCache/usageLimit/usageWindow shapes:
// those parse ~/.claude/.usage-cache.json, a file maintained by the user's
// statusline script, while these parse the live oauth/usage API response
// consumed by the pool. The two documents happen to look alike today but
// are owned by different producers and are free to diverge.

type usageLimitEntry struct {
	Kind     string  `json:"kind"`
	Percent  float64 `json:"percent"`
	ResetsAt string  `json:"resets_at"`
	Scope    *struct {
		Model struct {
			DisplayName string `json:"display_name"`
		} `json:"model"`
	} `json:"scope"`
}

type usageWindowFallback struct {
	Utilization float64 `json:"utilization"`
	ResetsAt    string  `json:"resets_at"`
}

type usageResponse struct {
	Limits   []usageLimitEntry    `json:"limits"`
	FiveHour *usageWindowFallback `json:"five_hour"`
	SevenDay *usageWindowFallback `json:"seven_day"`
}

// isoMillis parses an RFC3339(Nano) timestamp to unix millis, 0 on failure.
func isoMillis(iso string) int64 {
	if iso == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339Nano, iso)
	if err != nil {
		if t, err = time.Parse(time.RFC3339, iso); err != nil {
			return 0
		}
	}
	return t.UnixMilli()
}

func parseUsageResponse(data []byte) (session, weekly *UsageWindow, models []ModelWindow, err error) {
	var resp usageResponse
	if err = json.Unmarshal(data, &resp); err != nil {
		return nil, nil, nil, err
	}
	if len(resp.Limits) > 0 {
		for _, l := range resp.Limits {
			switch l.Kind {
			case "session":
				session = &UsageWindow{Percent: l.Percent, ResetsAt: isoMillis(l.ResetsAt)}
			case "weekly_all":
				weekly = &UsageWindow{Percent: l.Percent, ResetsAt: isoMillis(l.ResetsAt)}
			case "weekly_scoped":
				if l.Scope == nil || l.Scope.Model.DisplayName == "" {
					continue
				}
				models = append(models, ModelWindow{
					Name:     l.Scope.Model.DisplayName,
					Percent:  l.Percent,
					ResetsAt: isoMillis(l.ResetsAt),
				})
			}
		}
		return session, weekly, models, nil
	}
	if resp.FiveHour != nil {
		session = &UsageWindow{Percent: resp.FiveHour.Utilization, ResetsAt: isoMillis(resp.FiveHour.ResetsAt)}
	}
	if resp.SevenDay != nil {
		weekly = &UsageWindow{Percent: resp.SevenDay.Utilization, ResetsAt: isoMillis(resp.SevenDay.ResetsAt)}
	}
	return session, weekly, models, nil
}

func fetchUsage(token string) (session, weekly *UsageWindow, models []ModelWindow, err error) {
	req, err := http.NewRequest(http.MethodGet, usageEndpoint, nil)
	if err != nil {
		return nil, nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, nil, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, nil, nil, fmt.Errorf("usage fetch: status %d", resp.StatusCode)
	}
	return parseUsageResponse(data)
}

// --- main-account fallback via ~/.claude/.usage-cache.json ---

func mainUsageCacheFallback() (session, weekly *UsageWindow, models []ModelWindow, fetchedAt int64, ok bool) {
	path := filepath.Join(util.Home(), ".claude", ".usage-cache.json")
	info, err := os.Stat(path)
	if err != nil || time.Since(info.ModTime()) >= 10*time.Minute {
		return nil, nil, nil, 0, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, nil, 0, false
	}
	session, weekly, models, err = parseUsageResponse(data)
	if err != nil {
		return nil, nil, nil, 0, false
	}
	return session, weekly, models, info.ModTime().UnixMilli(), true
}

// --- cooldowns ---

// cooldowns reads <PoolDir()>/cooldowns.json (slug -> epoch seconds), keeping
// only entries still in the future, converted to unix millis.
func cooldowns() map[string]int64 {
	data, err := os.ReadFile(filepath.Join(PoolDir(), "cooldowns.json"))
	if err != nil {
		return nil
	}
	var raw map[string]float64
	if json.Unmarshal(data, &raw) != nil {
		return nil
	}
	now := time.Now().UnixMilli()
	out := make(map[string]int64, len(raw))
	for slug, secs := range raw {
		millis := int64(secs * 1000)
		if millis > now {
			out[slug] = millis
		}
	}
	return out
}

// --- top-level fetch ---

// FetchAllUsage enumerates the main account and every pool slot, fetches
// usage for each with bounded concurrency, and returns one row per account
// in stable order (main first, then slots sorted by slug).
func FetchAllUsage() []AccountUsage {
	slugs := poolSlugs()
	rows := make([]AccountUsage, 1+len(slugs))

	cd := cooldowns()
	const workers = 4
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup

	fetchOne := func(idx int, slug string, isSlot bool, slotDir string) {
		defer wg.Done()
		sem <- struct{}{}
		defer func() { <-sem }()
		rows[idx] = fetchAccountUsage(slug, isSlot, slotDir, cd[slug])
	}

	wg.Add(1)
	go fetchOne(0, "main", false, "")
	for i, slug := range slugs {
		wg.Add(1)
		go fetchOne(1+i, slug, true, filepath.Join(PoolDir(), slug))
	}
	wg.Wait()
	return rows
}

func fetchAccountUsage(slug string, isSlot bool, slotDir string, cooldownUntil int64) AccountUsage {
	row := AccountUsage{Slug: slug, CooldownUntil: cooldownUntil}

	var meta accountMeta
	if isSlot {
		meta = slotAccountMeta(slotDir)
	} else {
		meta = mainAccountMeta()
	}
	row.Email, row.DisplayName, row.Tier = meta.Email, meta.DisplayName, meta.Tier

	if isSlot {
		if _, err := os.Stat(filepath.Join(slotDir, ".disabled")); err == nil {
			row.NoCreds = true
			return row
		}
	}

	var creds oauthCreds
	var haveCreds bool
	if isSlot {
		creds, haveCreds = loadSlotCreds(slotDir)
	} else {
		creds, haveCreds = loadMainCreds()
	}

	if !haveCreds {
		if !isSlot {
			// Main keychain may be unreachable (e.g. under launchd); fall
			// back to the statusline's own recent usage cache.
			if session, weekly, models, fetchedAt, ok := mainUsageCacheFallback(); ok {
				row.Session, row.Weekly, row.Models = session, weekly, models
				row.FetchedAt = fetchedAt
				row.Stale = true
				return row
			}
			row.Error = "keychain unavailable"
			return row
		}
		row.NoCreds = true
		return row
	}

	token, reauth, errMsg, writeWarn := resolveToken(creds, isSlot, slotDir)
	if reauth {
		row.Reauth = true
		row.Error = errMsg
		return row
	}
	if errMsg != "" {
		row.Error = errMsg
		return row
	}

	session, weekly, models, err := fetchUsage(token)
	if err != nil {
		if !isSlot {
			if fbSession, fbWeekly, fbModels, fetchedAt, ok := mainUsageCacheFallback(); ok {
				row.Session, row.Weekly, row.Models = fbSession, fbWeekly, fbModels
				row.FetchedAt = fetchedAt
				row.Stale = true
				return row
			}
		}
		if writeWarn != "" {
			row.Error = fmt.Sprintf("%s (usage fetch also failed: %v)", writeWarn, err)
		} else {
			row.Error = err.Error()
		}
		return row
	}

	row.Session, row.Weekly, row.Models = session, weekly, models
	row.FetchedAt = time.Now().UnixMilli()
	return row
}
