package core

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- shell-out stubbing helpers ---

// stubSecurity replaces the security(1) shell-outs with an in-memory map
// keyed by service name, so tests never touch the real keychain.
func stubSecurity(t *testing.T, store map[string]string) {
	t.Helper()
	origFind, origAdd := securityFind, securityAdd
	securityFind = func(service, account string) (string, error) {
		v, ok := store[service]
		if !ok {
			return "", fmt.Errorf("not found: %s", service)
		}
		return v, nil
	}
	securityAdd = func(service, account, value string) error {
		store[service] = value
		return nil
	}
	t.Cleanup(func() {
		securityFind, securityAdd = origFind, origAdd
	})
}

func stubEndpoints(t *testing.T, usageURL, tokenURL string) {
	t.Helper()
	origUsage, origToken := usageEndpoint, tokenEndpoint
	if usageURL != "" {
		usageEndpoint = usageURL
	}
	if tokenURL != "" {
		tokenEndpoint = tokenURL
	}
	t.Cleanup(func() {
		usageEndpoint, tokenEndpoint = origUsage, origToken
	})
}

// --- limits[] parsing ---

const fixtureLimitsResponse = `{
  "limits": [
    {"kind": "session", "percent": 53, "resets_at": "2026-07-22T23:40:00.171898+00:00", "scope": null},
    {"kind": "weekly_all", "percent": 11, "resets_at": "2026-07-26T21:00:00.171921+00:00", "scope": null},
    {"kind": "weekly_scoped", "percent": 18, "resets_at": "2026-07-26T21:00:00.172196+00:00",
      "scope": {"model": {"id": null, "display_name": "Fable"}, "surface": null}}
  ]
}`

func TestParseUsageResponseLimits(t *testing.T) {
	session, weekly, models, err := parseUsageResponse([]byte(fixtureLimitsResponse))
	if err != nil {
		t.Fatalf("parseUsageResponse: %v", err)
	}
	if session == nil || session.Percent != 53 {
		t.Fatalf("session = %+v", session)
	}
	wantSessionMillis := isoMillis("2026-07-22T23:40:00.171898+00:00")
	if session.ResetsAt != wantSessionMillis {
		t.Errorf("session.ResetsAt = %d, want %d", session.ResetsAt, wantSessionMillis)
	}
	if weekly == nil || weekly.Percent != 11 {
		t.Fatalf("weekly = %+v", weekly)
	}
	if len(models) != 1 || models[0].Name != "Fable" || models[0].Percent != 18 {
		t.Fatalf("models = %+v", models)
	}
	wantModelMillis := isoMillis("2026-07-26T21:00:00.172196+00:00")
	if models[0].ResetsAt != wantModelMillis {
		t.Errorf("models[0].ResetsAt = %d, want %d", models[0].ResetsAt, wantModelMillis)
	}
}

func TestParseUsageResponseFallback(t *testing.T) {
	const body = `{
  "limits": [],
  "five_hour": {"utilization": 42, "resets_at": "2026-07-22T23:40:00Z"},
  "seven_day": {"utilization": 7, "resets_at": "2026-07-26T21:00:00Z"}
}`
	session, weekly, models, err := parseUsageResponse([]byte(body))
	if err != nil {
		t.Fatalf("parseUsageResponse: %v", err)
	}
	if session == nil || session.Percent != 42 {
		t.Fatalf("session = %+v", session)
	}
	if weekly == nil || weekly.Percent != 7 {
		t.Fatalf("weekly = %+v", weekly)
	}
	if models != nil {
		t.Errorf("models = %+v, want nil", models)
	}
}

func TestParseUsageResponseSkipsEmptyModelName(t *testing.T) {
	const body = `{"limits": [
    {"kind": "weekly_scoped", "percent": 5, "resets_at": "", "scope": {"model": {"display_name": ""}}}
  ]}`
	_, _, models, err := parseUsageResponse([]byte(body))
	if err != nil {
		t.Fatalf("parseUsageResponse: %v", err)
	}
	if len(models) != 0 {
		t.Errorf("models = %+v, want empty", models)
	}
}

// --- expiry decision ---

func TestOauthCredsExpired(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string
		expiresAt int64
		want      bool
	}{
		{"expired", now.UnixMilli() - 1000, true},
		{"expiring within grace window", now.UnixMilli() + 30_000, true},
		{"valid", now.UnixMilli() + 10*time.Minute.Milliseconds(), false},
		{"missing expiresAt", 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			creds := oauthCreds{ClaudeAiOauth: claudeAiOauthFields{ExpiresAt: c.expiresAt}}
			if got := creds.expired(now); got != c.want {
				t.Errorf("expired(%d) = %v, want %v", c.expiresAt, got, c.want)
			}
		})
	}
}

// --- refresh flow ---

func credsFixtureJSON(accessToken, refreshToken string, expiresAt int64) string {
	return fmt.Sprintf(`{
  "mcpOAuth": ["sentry|abc123"],
  "claudeAiOauth": {
    "accessToken": %q,
    "refreshToken": %q,
    "expiresAt": %d,
    "scopes": ["user:inference"],
    "subscriptionType": "pro",
    "rateLimitTier": "default_claude_pro"
  }
}`, accessToken, refreshToken, expiresAt)
}

func TestRefreshFlowSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["refresh_token"] != "old-refresh" || body["client_id"] == "" {
			t.Errorf("unexpected refresh request body: %+v", body)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "new-access",
			"refresh_token": "new-refresh",
			"expires_in":    3600,
		})
	}))
	defer server.Close()
	stubEndpoints(t, "", server.URL)
	stubSecurity(t, map[string]string{})

	slotDir := t.TempDir()
	fixture := credsFixtureJSON("old-access", "old-refresh", 1000)
	if err := os.WriteFile(filepath.Join(slotDir, ".credentials.json"), []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	creds, ok := decodeOauthCreds([]byte(fixture))
	if !ok {
		t.Fatal("decodeOauthCreds failed on fixture")
	}

	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	updated, outcome, _ := refreshToken(creds, now)
	if outcome != refreshOK {
		t.Fatalf("refreshToken outcome = %v, want refreshOK", outcome)
	}
	if updated.ClaudeAiOauth.AccessToken != "new-access" || updated.ClaudeAiOauth.RefreshToken != "new-refresh" {
		t.Fatalf("updated creds = %+v", updated.ClaudeAiOauth)
	}
	wantExpiry := now.UnixMilli() + 3600*1000
	if updated.ClaudeAiOauth.ExpiresAt != wantExpiry {
		t.Errorf("ExpiresAt = %d, want %d", updated.ClaudeAiOauth.ExpiresAt, wantExpiry)
	}
	if updated.ClaudeAiOauth.SubscriptionType != "pro" || updated.ClaudeAiOauth.RateLimitTier != "default_claude_pro" {
		t.Errorf("untouched fields not preserved: %+v", updated.ClaudeAiOauth)
	}

	if err := writeSlotCreds(slotDir, updated); err != nil {
		t.Fatalf("writeSlotCreds: %v", err)
	}
	credsPath := filepath.Join(slotDir, ".credentials.json")
	info, err := os.Stat(credsPath)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("perms = %o, want 0600", perm)
	}
	data, err := os.ReadFile(credsPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(data, &doc) != nil {
		t.Fatal("written file is not valid JSON")
	}
	if _, ok := doc["mcpOAuth"]; !ok {
		t.Error("mcpOAuth sibling key was dropped on write-back")
	}
	written, ok := decodeOauthCreds(data)
	if !ok || written.ClaudeAiOauth.AccessToken != "new-access" {
		t.Errorf("written creds = %+v, ok=%v", written.ClaudeAiOauth, ok)
	}
}

func TestRefreshFlowInvalidGrantConcurrentRotation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
	}))
	defer server.Close()
	stubEndpoints(t, "", server.URL)
	stubSecurity(t, map[string]string{})

	slotDir := t.TempDir()
	staleFixture := credsFixtureJSON("stale-access", "stale-refresh", 1000)
	creds, ok := decodeOauthCreds([]byte(staleFixture))
	if !ok {
		t.Fatal("decodeOauthCreds failed")
	}

	// Simulate a concurrent rotation: by the time we re-read, the file on
	// disk already has a fresh, unexpired token. resolveToken checks expiry
	// against the real clock, so the fixture must be anchored to it too.
	freshExpiry := time.Now().Add(time.Hour).UnixMilli()
	freshFixture := credsFixtureJSON("fresh-access", "fresh-refresh", freshExpiry)
	if err := os.WriteFile(filepath.Join(slotDir, ".credentials.json"), []byte(freshFixture), 0o600); err != nil {
		t.Fatal(err)
	}

	token, reauth, errMsg, writeWarn := resolveToken(creds, true, slotDir)
	if reauth {
		t.Fatalf("reauth = true, errMsg = %q, want re-read to succeed", errMsg)
	}
	if token != "fresh-access" {
		t.Errorf("token = %q, want fresh-access", token)
	}
	if writeWarn != "" {
		t.Errorf("writeWarn = %q, want empty", writeWarn)
	}
}

func TestRefreshFlowInvalidGrantStillExpired(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
	}))
	defer server.Close()
	stubEndpoints(t, "", server.URL)
	stubSecurity(t, map[string]string{})

	slotDir := t.TempDir()
	fixture := credsFixtureJSON("stale-access", "stale-refresh", 1000)
	if err := os.WriteFile(filepath.Join(slotDir, ".credentials.json"), []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(slotDir, ".credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	creds, ok := decodeOauthCreds([]byte(fixture))
	if !ok {
		t.Fatal("decodeOauthCreds failed")
	}

	token, reauth, errMsg, _ := resolveToken(creds, true, slotDir)
	if !reauth || token != "" || errMsg == "" {
		t.Fatalf("token=%q reauth=%v errMsg=%q, want reauth=true", token, reauth, errMsg)
	}
	after, err := os.ReadFile(filepath.Join(slotDir, ".credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("credentials file was modified on a failed refresh with no valid re-read")
	}
}

// --- keychain vs. file precedence ---

func TestLoadSlotCredsKeychainWinsOverFile(t *testing.T) {
	slotDir := t.TempDir()
	staleFixture := credsFixtureJSON("stale-access", "stale-refresh", time.Now().Add(time.Hour).UnixMilli())
	if err := os.WriteFile(filepath.Join(slotDir, ".credentials.json"), []byte(staleFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	freshFixture := credsFixtureJSON("fresh-access", "fresh-refresh", time.Now().Add(time.Hour).UnixMilli())
	stubSecurity(t, map[string]string{
		slotKeychainService(slotDir): freshFixture,
	})

	creds, ok := loadSlotCreds(slotDir)
	if !ok {
		t.Fatal("loadSlotCreds returned ok=false")
	}
	if creds.ClaudeAiOauth.AccessToken != "fresh-access" {
		t.Errorf("AccessToken = %q, want fresh-access (keychain should win over a stale file)", creds.ClaudeAiOauth.AccessToken)
	}
}

// --- transient vs. permanent refresh failure ---

func TestFetchAccountUsageTransientRefreshFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	stubEndpoints(t, "", server.URL)
	stubSecurity(t, map[string]string{})

	slotDir := t.TempDir()
	fixture := credsFixtureJSON("stale-access", "stale-refresh", 1000)
	if err := os.WriteFile(filepath.Join(slotDir, ".credentials.json"), []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(slotDir, ".credentials.json"))
	if err != nil {
		t.Fatal(err)
	}

	row := fetchAccountUsage("transient-slot", true, slotDir, 0)
	if row.Reauth {
		t.Error("Reauth = true, want false for a transient (500) refresh failure")
	}
	if row.Error == "" {
		t.Error("Error = empty, want a message for a transient refresh failure")
	}
	after, err := os.ReadFile(filepath.Join(slotDir, ".credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("credentials file was modified on a transient refresh failure")
	}
}

// --- main-account cache fallback ---

func TestFetchAccountUsageMainCacheFallbackNoCreds(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	stubSecurity(t, map[string]string{}) // main keychain unreachable

	claudeDir := filepath.Join(home, ".claude")
	mustMkdir(t, claudeDir)
	if err := os.WriteFile(filepath.Join(claudeDir, ".usage-cache.json"), []byte(fixtureLimitsResponse), 0o644); err != nil {
		t.Fatal(err)
	}

	row := fetchAccountUsage("main", false, "", 0)
	if row.Error != "" {
		t.Errorf("Error = %q, want empty (cache fallback should succeed)", row.Error)
	}
	if !row.Stale {
		t.Error("Stale = false, want true for a cache fallback")
	}
	if row.Session == nil || row.Session.Percent != 53 {
		t.Errorf("Session = %+v", row.Session)
	}
}

func TestFetchAccountUsageMainCacheFallbackFetchError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	usageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer usageServer.Close()
	stubEndpoints(t, usageServer.URL, "")

	creds := credsFixtureJSON("main-token", "main-refresh", time.Now().Add(time.Hour).UnixMilli())
	stubSecurity(t, map[string]string{
		mainKeychainService: creds,
	})

	claudeDir := filepath.Join(home, ".claude")
	mustMkdir(t, claudeDir)
	if err := os.WriteFile(filepath.Join(claudeDir, ".usage-cache.json"), []byte(fixtureLimitsResponse), 0o644); err != nil {
		t.Fatal(err)
	}

	row := fetchAccountUsage("main", false, "", 0)
	if row.Error != "" {
		t.Errorf("Error = %q, want empty (cache fallback should succeed)", row.Error)
	}
	if !row.Stale {
		t.Error("Stale = false, want true for a cache fallback")
	}
	if row.Session == nil || row.Session.Percent != 53 {
		t.Errorf("Session = %+v", row.Session)
	}
}

// --- slot enumeration + identity ---

func TestPoolSlugsAndIdentity(t *testing.T) {
	pool := t.TempDir()
	t.Setenv("CLAUDE_POOL_DIR", pool)

	mustMkdir(t, filepath.Join(pool, "zeta-slot"))
	mustMkdir(t, filepath.Join(pool, "alpha-slot"))
	mustMkdir(t, filepath.Join(pool, ".hidden-slot"))
	if err := os.WriteFile(filepath.Join(pool, "not-a-dir"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := poolSlugs()
	want := []string{"alpha-slot", "zeta-slot"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("poolSlugs() = %v, want %v", got, want)
	}

	accountJSON := `{"email":"a@example.com","display_name":"Alpha","rate_limit_tier":"default_claude_pro"}`
	if err := os.WriteFile(filepath.Join(pool, "alpha-slot", "account.json"), []byte(accountJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	meta := slotAccountMeta(filepath.Join(pool, "alpha-slot"))
	if meta.Email != "a@example.com" || meta.DisplayName != "Alpha" || meta.Tier != "default_claude_pro" {
		t.Errorf("slotAccountMeta = %+v", meta)
	}
}

func TestFetchAccountUsageDisabledSlot(t *testing.T) {
	stubSecurity(t, map[string]string{})
	slotDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(slotDir, ".disabled"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	row := fetchAccountUsage("disabled-slot", true, slotDir, 0)
	if !row.NoCreds {
		t.Errorf("row = %+v, want NoCreds", row)
	}
}

func TestFetchAccountUsageMissingCreds(t *testing.T) {
	stubSecurity(t, map[string]string{})
	slotDir := t.TempDir()
	row := fetchAccountUsage("empty-slot", true, slotDir, 0)
	if !row.NoCreds {
		t.Errorf("row = %+v, want NoCreds", row)
	}
}

// --- cooldowns ---

func TestCooldowns(t *testing.T) {
	pool := t.TempDir()
	t.Setenv("CLAUDE_POOL_DIR", pool)

	future := time.Now().Add(time.Hour).Unix()
	past := time.Now().Add(-time.Hour).Unix()
	doc := fmt.Sprintf(`{"future-slot": %d, "past-slot": %d}`, future, past)
	if err := os.WriteFile(filepath.Join(pool, "cooldowns.json"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}

	got := cooldowns()
	if _, ok := got["past-slot"]; ok {
		t.Errorf("past-slot present in %v, want dropped", got)
	}
	want := future * 1000
	if got["future-slot"] != want {
		t.Errorf("future-slot = %d, want %d", got["future-slot"], want)
	}
}

// --- FetchAllUsage end to end ---

func TestFetchAllUsage(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	pool := t.TempDir()
	t.Setenv("CLAUDE_POOL_DIR", pool)

	usageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth == "Bearer goodslot-token" {
			w.Write([]byte(fixtureLimitsResponse))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer usageServer.Close()
	stubEndpoints(t, usageServer.URL, "")
	stubSecurity(t, map[string]string{}) // no keychain entries anywhere; forces file/cache fallback

	mustMkdir(t, filepath.Join(pool, "goodslot"))
	goodCreds := credsFixtureJSON("goodslot-token", "goodslot-refresh", time.Now().Add(time.Hour).UnixMilli())
	if err := os.WriteFile(filepath.Join(pool, "goodslot", ".credentials.json"), []byte(goodCreds), 0o600); err != nil {
		t.Fatal(err)
	}

	mustMkdir(t, filepath.Join(pool, "badslot"))
	badCreds := credsFixtureJSON("badslot-token", "badslot-refresh", time.Now().Add(time.Hour).UnixMilli())
	if err := os.WriteFile(filepath.Join(pool, "badslot", ".credentials.json"), []byte(badCreds), 0o600); err != nil {
		t.Fatal(err)
	}

	rows := FetchAllUsage()
	if len(rows) != 3 {
		t.Fatalf("len(rows) = %d, want 3 (main, badslot, goodslot)", len(rows))
	}
	if rows[0].Slug != "main" {
		t.Errorf("rows[0].Slug = %q, want main", rows[0].Slug)
	}
	if rows[0].Error == "" {
		t.Errorf("main row = %+v, want an error (no keychain, no usage cache)", rows[0])
	}
	if rows[1].Slug != "badslot" || rows[2].Slug != "goodslot" {
		t.Fatalf("slot order = [%s, %s], want [badslot, goodslot]", rows[1].Slug, rows[2].Slug)
	}
	if rows[1].Error == "" {
		t.Errorf("badslot row = %+v, want an error from the 500 response", rows[1])
	}
	if rows[2].Error != "" {
		t.Errorf("goodslot row = %+v, want no error", rows[2])
	}
	if rows[2].Session == nil || rows[2].Session.Percent != 53 {
		t.Errorf("goodslot.Session = %+v", rows[2].Session)
	}
	if rows[2].Weekly == nil || rows[2].Weekly.Percent != 11 {
		t.Errorf("goodslot.Weekly = %+v", rows[2].Weekly)
	}
	if len(rows[2].Models) != 1 || rows[2].Models[0].Name != "Fable" {
		t.Errorf("goodslot.Models = %+v", rows[2].Models)
	}
	if rows[2].FetchedAt == 0 {
		t.Error("goodslot.FetchedAt not set")
	}

	// No token value should ever leak into the marshaled AccountUsage rows.
	data, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"goodslot-token", "goodslot-refresh", "badslot-token", "badslot-refresh"} {
		if strings.Contains(string(data), secret) {
			t.Errorf("marshaled AccountUsage rows leak token value %q", secret)
		}
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}
