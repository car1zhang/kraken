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

// --- NeedsRotation ---

func modelWindow(name string, pct float64) []ModelWindow {
	return []ModelWindow{{Name: name, Percent: pct}}
}

func TestNeedsRotation(t *testing.T) {
	cases := []struct {
		name       string
		main       AccountUsage
		wantTrip   bool
		wantReason string
	}{
		{
			name:     "all nil windows never trip",
			main:     AccountUsage{},
			wantTrip: false,
		},
		{
			name:     "session exactly at threshold does not trip",
			main:     AccountUsage{Session: &UsageWindow{Percent: 80}},
			wantTrip: false,
		},
		{
			name:       "session just over threshold trips",
			main:       AccountUsage{Session: &UsageWindow{Percent: 80.1}},
			wantTrip:   true,
			wantReason: "session 80.1% > 80%",
		},
		{
			name:     "weekly exactly at threshold does not trip",
			main:     AccountUsage{Weekly: &UsageWindow{Percent: 90}},
			wantTrip: false,
		},
		{
			name:       "weekly just over threshold trips",
			main:       AccountUsage{Weekly: &UsageWindow{Percent: 90.1}},
			wantTrip:   true,
			wantReason: "weekly 90.1% > 90%",
		},
		{
			name:     "fable exactly at threshold does not trip",
			main:     AccountUsage{Models: modelWindow("Fable", 90)},
			wantTrip: false,
		},
		{
			name:       "fable just over threshold trips",
			main:       AccountUsage{Models: modelWindow("Fable", 90.1)},
			wantTrip:   true,
			wantReason: "fable 90.1% > 90%",
		},
		{
			name:     "other model names never trip",
			main:     AccountUsage{Models: modelWindow("Opus", 99)},
			wantTrip: false,
		},
		{
			name: "session and fable both trip, joined in order",
			main: AccountUsage{
				Session: &UsageWindow{Percent: 85},
				Models:  modelWindow("Fable", 92),
			},
			wantTrip:   true,
			wantReason: "session 85% > 80%, fable 92% > 90%",
		},
		{
			name: "all three trip",
			main: AccountUsage{
				Session: &UsageWindow{Percent: 85},
				Weekly:  &UsageWindow{Percent: 95},
				Models:  modelWindow("Fable", 92),
			},
			wantTrip:   true,
			wantReason: "session 85% > 80%, weekly 95% > 90%, fable 92% > 90%",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotTrip, gotReason := NeedsRotation(c.main)
			if gotTrip != c.wantTrip {
				t.Errorf("trip = %v, want %v (reason=%q)", gotTrip, c.wantTrip, gotReason)
			}
			if gotReason != c.wantReason {
				t.Errorf("reason = %q, want %q", gotReason, c.wantReason)
			}
		})
	}
}

// --- SelectRotationTarget ---

func acctRow(slug string, sessionPct float64) AccountUsage {
	// Email defaults to non-empty (derived from slug) so acctRow-built rows
	// stay eligible under SelectRotationTarget's "no identity" exclusion
	// (F1/R3) unless a test explicitly wants to exercise that exclusion.
	return AccountUsage{Slug: slug, Email: slug + "@example.com", Session: &UsageWindow{Percent: sessionPct}}
}

func TestSelectRotationTargetEmptyPool(t *testing.T) {
	slug, reason := SelectRotationTarget(nil, "main@example.com", 1000)
	if slug != "" || reason != "no pool accounts" {
		t.Errorf("slug=%q reason=%q, want empty slug and 'no pool accounts'", slug, reason)
	}
}

func TestSelectRotationTargetOnlyMainRow(t *testing.T) {
	rows := []AccountUsage{{Slug: "main", Session: &UsageWindow{Percent: 10}}}
	slug, reason := SelectRotationTarget(rows, "main@example.com", 1000)
	if slug != "" || reason != "no pool accounts" {
		t.Errorf("slug=%q reason=%q, want empty slug and 'no pool accounts'", slug, reason)
	}
}

func TestSelectRotationTargetSkipsMainSlugAndSameEmail(t *testing.T) {
	rows := []AccountUsage{
		{Slug: "main", Session: &UsageWindow{Percent: 5}},
		{Slug: "mirror-slot", Email: "Main@Example.com", Session: &UsageWindow{Percent: 5}}, // case-insensitive match
		acctRow("good-slot", 10),
	}
	slug, reason := SelectRotationTarget(rows, "main@example.com", 1000)
	if slug != "good-slot" || reason != "" {
		t.Fatalf("slug=%q reason=%q, want good-slot", slug, reason)
	}
}

func TestSelectRotationTargetEligibilityMatrix(t *testing.T) {
	now := int64(1_000_000)
	// Every non-{no-creds,reauth} row below MUST carry an Email: those two
	// checks run before the Email == "" guard in SelectRotationTarget's
	// switch, so they hit their intended branch regardless: but
	// nil-session-slot/cooling-slot/fable-high-slot/session-high-slot are
	// emailless would short-circuit at "no identity" instead of their
	// intended exclusion (Session == nil / CooldownUntil / fable / session
	// thresholds) — deleting any of those later checks would keep this
	// suite green if the rows had no Email (X5).
	//
	// excludedRows and winnerRow are named separately (rather than one
	// slice sliced by position) so the "all excluded" assertion below is
	// built from an explicit exclusion set, not an assumption that the
	// winner sits last — appending a new excluded row later can't silently
	// change what gets asserted (Y7).
	excludedRows := []AccountUsage{
		{Slug: "no-creds-slot", NoCreds: true, Session: &UsageWindow{Percent: 5}},
		{Slug: "reauth-slot", Reauth: true, Session: &UsageWindow{Percent: 5}},
		{Slug: "nil-session-slot", Email: "nil-session-slot@example.com"},
		{Slug: "cooling-slot", Email: "cooling-slot@example.com", Session: &UsageWindow{Percent: 5}, CooldownUntil: now + 1},
		{Slug: "fable-high-slot", Email: "fable-high-slot@example.com", Session: &UsageWindow{Percent: 5}, Models: modelWindow("Fable", 80)},
		{Slug: "session-high-slot", Email: "session-high-slot@example.com", Session: &UsageWindow{Percent: 80}},
	}
	winnerRow := AccountUsage{Slug: "winner-slot", Email: "winner@example.com", Session: &UsageWindow{Percent: 10}}

	allRows := append(append([]AccountUsage{}, excludedRows...), winnerRow)
	slug, reason := SelectRotationTarget(allRows, "main@example.com", now)
	if slug != "winner-slot" || reason != "" {
		t.Fatalf("slug=%q reason=%q, want winner-slot with no exclusion reason", slug, reason)
	}

	// SelectRotationTarget only surfaces `reason` when NO candidate
	// survives, so re-run over JUST excludedRows (not allRows minus a
	// positional slice) to pin down that each excluded row hit ITS
	// intended branch (not just that some row won) — this is the
	// assertion X5 asks for beyond "winner-slot wins".
	_, allExcludedReason := SelectRotationTarget(excludedRows, "main@example.com", now)
	wantSubstrings := []string{
		"no-creds-slot: no creds",
		"reauth-slot: reauth required",
		"nil-session-slot: no session data",
		"cooling-slot: cooling",
		"fable-high-slot: fable 80% ≥ 80%",
		"session-high-slot: session 80% ≥ 80%",
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(allExcludedReason, want) {
			t.Errorf("reason = %q, missing expected exclusion %q", allExcludedReason, want)
		}
	}
}

func TestSelectRotationTargetLowestSessionWins(t *testing.T) {
	rows := []AccountUsage{
		acctRow("high", 50),
		acctRow("low", 10),
		acctRow("mid", 30),
	}
	slug, reason := SelectRotationTarget(rows, "main@example.com", 0)
	if slug != "low" || reason != "" {
		t.Fatalf("slug=%q reason=%q, want low", slug, reason)
	}
}

func TestSelectRotationTargetFableTiebreak(t *testing.T) {
	rows := []AccountUsage{
		{Slug: "a", Email: "a@example.com", Session: &UsageWindow{Percent: 20}, Models: modelWindow("Fable", 50)},
		{Slug: "b", Email: "b@example.com", Session: &UsageWindow{Percent: 20}, Models: modelWindow("Fable", 10)},
	}
	slug, reason := SelectRotationTarget(rows, "main@example.com", 0)
	if slug != "b" || reason != "" {
		t.Fatalf("slug=%q reason=%q, want b (lower fable)", slug, reason)
	}
}

func TestSelectRotationTargetMissingFableTreatedAsZero(t *testing.T) {
	rows := []AccountUsage{
		{Slug: "has-fable", Email: "has-fable@example.com", Session: &UsageWindow{Percent: 20}, Models: modelWindow("Fable", 5)},
		{Slug: "no-fable", Email: "no-fable@example.com", Session: &UsageWindow{Percent: 20}}, // missing Fable ranks as 0, beats 5
	}
	slug, reason := SelectRotationTarget(rows, "main@example.com", 0)
	if slug != "no-fable" || reason != "" {
		t.Fatalf("slug=%q reason=%q, want no-fable (missing fable ranks as 0)", slug, reason)
	}
}

func TestSelectRotationTargetSlugTiebreak(t *testing.T) {
	rows := []AccountUsage{
		{Slug: "zeta", Email: "zeta@example.com", Session: &UsageWindow{Percent: 20}},
		{Slug: "alpha", Email: "alpha@example.com", Session: &UsageWindow{Percent: 20}},
	}
	slug, reason := SelectRotationTarget(rows, "main@example.com", 0)
	if slug != "alpha" || reason != "" {
		t.Fatalf("slug=%q reason=%q, want alpha (slug ascending tiebreak)", slug, reason)
	}
}

func TestSelectRotationTargetNoCandidateReasonContent(t *testing.T) {
	rows := []AccountUsage{
		{Slug: "spare-max-acct", Email: "spare-max-acct@example.com", Session: &UsageWindow{Percent: 5}, Models: modelWindow("Fable", 85)},
		{Slug: "carlsjunk", Email: "carlsjunk@example.com", Session: &UsageWindow{Percent: 5}, CooldownUntil: 2000},
	}
	slug, reason := SelectRotationTarget(rows, "main@example.com", 1000)
	if slug != "" {
		t.Fatalf("slug=%q, want empty", slug)
	}
	want := "spare-max-acct: fable 85% ≥ 80%; carlsjunk: cooling"
	if reason != want {
		t.Errorf("reason = %q, want %q", reason, want)
	}
}

// --- SwapMainTo ---

// swapTestEnv wires up a temp pool dir, a temp faked $HOME (so
// mainAccountMeta/slotAccountMeta never touch the real filesystem), a
// swapped claudeJSONPath pointing inside that fake home, and an in-memory
// keychain store — mirroring the stubbing helpers in usagepool_test.go.
type swapTestEnv struct {
	pool      string
	keychain  map[string]string
	claudeDoc string // path to the faked ~/.claude.json
}

func newSwapTestEnv(t *testing.T) *swapTestEnv {
	t.Helper()
	home := t.TempDir()
	pool := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_POOL_DIR", pool)

	store := map[string]string{}
	stubSecurity(t, store)

	claudeDoc := filepath.Join(home, ".claude.json")
	origPath := claudeJSONPath
	claudeJSONPath = func() string { return claudeDoc }
	t.Cleanup(func() { claudeJSONPath = origPath })

	return &swapTestEnv{pool: pool, keychain: store, claudeDoc: claudeDoc}
}

func (e *swapTestEnv) writeMainClaudeJSON(t *testing.T, doc map[string]any) {
	t.Helper()
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.claudeDoc, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// setMainKeychainCreds seeds the in-memory keychain store with main-account
// creds. Named distinctly from the production writeMainCreds func in
// usagepool.go — same package, and a same-named test helper shadows it
// confusingly at call sites.
func (e *swapTestEnv) setMainKeychainCreds(t *testing.T, accessToken, refreshToken string, expiresAt int64) {
	t.Helper()
	e.keychain[mainKeychainService] = credsFixtureJSON(accessToken, refreshToken, expiresAt)
}

func (e *swapTestEnv) writeSlot(t *testing.T, slug, email, displayName, tier, accessToken, refreshToken string, expiresAt int64) string {
	t.Helper()
	dir := filepath.Join(e.pool, slug)
	mustMkdir(t, dir)
	accountJSON := fmt.Sprintf(`{"email":%q,"display_name":%q,"rate_limit_tier":%q}`, email, displayName, tier)
	if err := os.WriteFile(filepath.Join(dir, "account.json"), []byte(accountJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(credsFixtureJSON(accessToken, refreshToken, expiresAt)), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func farFutureMillis() int64 {
	return time.Now().Add(time.Hour).UnixMilli()
}

func TestSwapMainToHappyPath(t *testing.T) {
	env := newSwapTestEnv(t)
	env.writeMainClaudeJSON(t, map[string]any{
		"oauthAccount": map[string]any{
			"emailAddress": "main@example.com",
			"displayName":  "Main Person",
		},
		"someOtherTopLevelKey": []any{"keep", "me"},
	})
	env.setMainKeychainCreds(t, "main-access", "main-refresh", farFutureMillis())
	env.writeSlot(t, "target-slot", "target@example.com", "Target Person", "default_claude_pro",
		"target-access", "target-refresh", farFutureMillis())

	from, to, err := SwapMainTo("target-slot")
	if err != nil {
		t.Fatalf("SwapMainTo: %v", err)
	}
	if to == "" {
		t.Fatal("SwapMainTo: to must never be empty on a successful swap")
	}
	if from != "main@example.com" || to != "target@example.com" {
		t.Errorf("from=%q to=%q, want main@example.com / target@example.com", from, to)
	}

	// Main keychain now holds the target's creds.
	mainRaw := env.keychain[mainKeychainService]
	mainCreds, ok := decodeOauthCreds([]byte(mainRaw))
	if !ok || mainCreds.ClaudeAiOauth.AccessToken != "target-access" {
		t.Errorf("main keychain creds = %+v, ok=%v, want target-access", mainCreds.ClaudeAiOauth, ok)
	}

	// Save-back slot (auto-created, slugified from main email) got the old
	// main creds plus the mirrored oauthAccount.
	saveBackDir := filepath.Join(env.pool, "main-example-com")
	saveBackAccountData, err := os.ReadFile(filepath.Join(saveBackDir, "account.json"))
	if err != nil {
		t.Fatalf("save-back account.json missing: %v", err)
	}
	if got := string(saveBackAccountData); !strings.Contains(got, "main@example.com") {
		t.Errorf("save-back account.json = %s, want main email", got)
	}
	saveBackCredsData, err := os.ReadFile(filepath.Join(saveBackDir, ".credentials.json"))
	if err != nil {
		t.Fatalf("save-back .credentials.json missing: %v", err)
	}
	saveBackCreds, ok := decodeOauthCreds(saveBackCredsData)
	if !ok || saveBackCreds.ClaudeAiOauth.AccessToken != "main-access" {
		t.Errorf("save-back creds = %+v, ok=%v, want main-access", saveBackCreds.ClaudeAiOauth, ok)
	}
	saveBackClaudeJSON, err := os.ReadFile(filepath.Join(saveBackDir, ".claude.json"))
	if err != nil {
		t.Fatalf("save-back .claude.json missing: %v", err)
	}
	var saveBackDoc map[string]json.RawMessage
	if json.Unmarshal(saveBackClaudeJSON, &saveBackDoc) != nil {
		t.Fatal("save-back .claude.json not valid JSON")
	}
	if _, ok := saveBackDoc["oauthAccount"]; !ok {
		t.Error("save-back .claude.json missing mirrored oauthAccount")
	}
	if info, err := os.Stat(filepath.Join(saveBackDir, ".claude.json")); err != nil || info.Mode().Perm() != 0o600 {
		perm := os.FileMode(0)
		if info != nil {
			perm = info.Mode().Perm()
		}
		t.Errorf("save-back .claude.json perms = %v, err=%v, want 0600", perm, err)
	}

	// ~/.claude.json keeps its existing permissions (written 0644 by
	// writeMainClaudeJSON above) — prepareMainInstall must preserve them,
	// not silently reset to some default.
	if info, err := os.Stat(env.claudeDoc); err != nil || info.Mode().Perm() != 0o644 {
		perm := os.FileMode(0)
		if info != nil {
			perm = info.Mode().Perm()
		}
		t.Errorf("~/.claude.json perms = %v, err=%v, want 0644 preserved", perm, err)
	}

	// ~/.claude.json: oauthAccount replaced with target's identity, sibling
	// keys preserved.
	updatedDoc, err := os.ReadFile(env.claudeDoc)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(updatedDoc, &doc) != nil {
		t.Fatal("~/.claude.json not valid JSON after swap")
	}
	if _, ok := doc["someOtherTopLevelKey"]; !ok {
		t.Error("sibling top-level key was dropped from ~/.claude.json")
	}
	var oauthAccount struct {
		EmailAddress string `json:"emailAddress"`
	}
	if json.Unmarshal(doc["oauthAccount"], &oauthAccount) != nil || oauthAccount.EmailAddress != "target@example.com" {
		t.Errorf("~/.claude.json oauthAccount = %s, want target email", doc["oauthAccount"])
	}
}

func TestSwapMainToAutoCreatesSaveBackSlugifiedName(t *testing.T) {
	env := newSwapTestEnv(t)
	env.writeMainClaudeJSON(t, map[string]any{"oauthAccount": map[string]any{"emailAddress": "carl@probook.ai"}})
	env.setMainKeychainCreds(t, "main-access", "main-refresh", farFutureMillis())
	env.writeSlot(t, "target-slot", "target@example.com", "Target", "pro",
		"target-access", "target-refresh", farFutureMillis())

	_, to, err := SwapMainTo("target-slot")
	if err != nil {
		t.Fatalf("SwapMainTo: %v", err)
	}
	if to == "" {
		t.Fatal("SwapMainTo: to must never be empty on a successful swap")
	}

	want := filepath.Join(env.pool, "carl-probook-ai")
	if _, err := os.Stat(want); err != nil {
		t.Errorf("expected save-back slot at %s, stat err: %v", want, err)
	}
}

func TestSwapMainToReusesExistingSaveBackSlot(t *testing.T) {
	env := newSwapTestEnv(t)
	env.writeMainClaudeJSON(t, map[string]any{"oauthAccount": map[string]any{"emailAddress": "main@example.com"}})
	env.setMainKeychainCreds(t, "main-access", "main-refresh", farFutureMillis())
	env.writeSlot(t, "target-slot", "target@example.com", "Target", "pro",
		"target-access", "target-refresh", farFutureMillis())
	// Pre-existing slot mirroring main's email under an arbitrary dir name.
	existing := env.writeSlot(t, "existing-mirror", "Main@Example.com", "Main Mirror", "pro",
		"old-mirror-access", "old-mirror-refresh", farFutureMillis())

	_, to, err := SwapMainTo("target-slot")
	if err != nil {
		t.Fatalf("SwapMainTo: %v", err)
	}
	if to == "" {
		t.Fatal("SwapMainTo: to must never be empty on a successful swap")
	}

	// The existing slot should have been reused (updated), not a new
	// auto-created one.
	data, err := os.ReadFile(filepath.Join(existing, ".credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	creds, ok := decodeOauthCreds(data)
	if !ok || creds.ClaudeAiOauth.AccessToken != "main-access" {
		t.Errorf("existing save-back slot not updated: %+v, ok=%v", creds.ClaudeAiOauth, ok)
	}
	autoCreated := filepath.Join(env.pool, "main-example-com")
	if _, err := os.Stat(autoCreated); err == nil {
		t.Errorf("auto-created a new save-back slot at %s even though one already existed", autoCreated)
	}
}

func TestSwapMainToAbortMissingSlot(t *testing.T) {
	env := newSwapTestEnv(t)
	env.writeMainClaudeJSON(t, map[string]any{"oauthAccount": map[string]any{"emailAddress": "main@example.com"}})
	env.setMainKeychainCreds(t, "main-access", "main-refresh", farFutureMillis())

	before := env.snapshotMainState(t)
	_, _, err := SwapMainTo("does-not-exist")
	if err == nil {
		t.Fatal("SwapMainTo: want error for missing slot")
	}
	env.assertMainStateUnchanged(t, before)
}

func TestSwapMainToAbortUndecodableTargetCreds(t *testing.T) {
	env := newSwapTestEnv(t)
	env.writeMainClaudeJSON(t, map[string]any{"oauthAccount": map[string]any{"emailAddress": "main@example.com"}})
	env.setMainKeychainCreds(t, "main-access", "main-refresh", farFutureMillis())
	dir := filepath.Join(env.pool, "broken-slot")
	mustMkdir(t, dir)
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	before := env.snapshotMainState(t)
	_, _, err := SwapMainTo("broken-slot")
	if err == nil {
		t.Fatal("SwapMainTo: want error for undecodable target creds")
	}
	env.assertMainStateUnchanged(t, before)
}

func TestSwapMainToAbortExpiredTargetFailingRefresh(t *testing.T) {
	env := newSwapTestEnv(t)
	env.writeMainClaudeJSON(t, map[string]any{"oauthAccount": map[string]any{"emailAddress": "main@example.com"}})
	env.setMainKeychainCreds(t, "main-access", "main-refresh", farFutureMillis())
	env.writeSlot(t, "expired-slot", "target@example.com", "Target", "pro",
		"target-access", "target-refresh", 1000) // already expired

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
	}))
	defer server.Close()
	stubEndpoints(t, "", server.URL)

	before := env.snapshotMainState(t)
	_, _, err := SwapMainTo("expired-slot")
	if err == nil {
		t.Fatal("SwapMainTo: want error for expired target creds with a failing refresh")
	}
	env.assertMainStateUnchanged(t, before)
}

func TestSwapMainToAbortUnavailableMainCreds(t *testing.T) {
	env := newSwapTestEnv(t)
	env.writeMainClaudeJSON(t, map[string]any{"oauthAccount": map[string]any{"emailAddress": "main@example.com"}})
	// No main creds written to the keychain at all.
	env.writeSlot(t, "target-slot", "target@example.com", "Target", "pro",
		"target-access", "target-refresh", farFutureMillis())

	before := env.snapshotMainState(t)
	_, _, err := SwapMainTo("target-slot")
	if err == nil {
		t.Fatal("SwapMainTo: want error when main creds are unavailable")
	}
	env.assertMainStateUnchanged(t, before)
}

// mainState is a snapshot of everything an abort path must leave untouched:
// the main keychain entry (present-or-not, and its exact content) and
// ~/.claude.json's bytes.
type mainState struct {
	keychainVal string
	keychainOK  bool
	claudeJSON  []byte
}

func (e *swapTestEnv) snapshotMainState(t *testing.T) mainState {
	t.Helper()
	v, ok := e.keychain[mainKeychainService]
	data, _ := os.ReadFile(e.claudeDoc)
	return mainState{keychainVal: v, keychainOK: ok, claudeJSON: data}
}

// assertMainStateUnchanged checks an abort path performed zero writes to the
// main keychain service and to ~/.claude.json.
func (e *swapTestEnv) assertMainStateUnchanged(t *testing.T, before mainState) {
	t.Helper()
	after := e.snapshotMainState(t)
	if before.keychainOK != after.keychainOK || before.keychainVal != after.keychainVal {
		t.Error("main keychain entry was written to on an abort path")
	}
	if string(before.claudeJSON) != string(after.claudeJSON) {
		t.Error("~/.claude.json was modified on an abort path")
	}
}

// --- SwapMainTo: new abort-path coverage (F1/F2/F5/F6) ---

func TestSwapMainToAbortMainIdentityEmpty(t *testing.T) {
	env := newSwapTestEnv(t)
	// No ~/.claude.json at all -> mainAccountMeta().Email == "".
	env.setMainKeychainCreds(t, "main-access", "main-refresh", farFutureMillis())
	env.writeSlot(t, "target-slot", "target@example.com", "Target", "pro",
		"target-access", "target-refresh", farFutureMillis())

	before := env.snapshotMainState(t)
	_, _, err := SwapMainTo("target-slot")
	if err == nil {
		t.Fatal("SwapMainTo: want error when main identity is unknown")
	}
	env.assertMainStateUnchanged(t, before)
}

func TestSwapMainToAbortSaveBackCollidesWithExistingSlot(t *testing.T) {
	env := newSwapTestEnv(t)
	env.writeMainClaudeJSON(t, map[string]any{"oauthAccount": map[string]any{"emailAddress": "collide@example.com"}})
	env.setMainKeychainCreds(t, "main-access", "main-refresh", farFutureMillis())
	env.writeSlot(t, "target-slot", "target@example.com", "Target", "pro",
		"target-access", "target-refresh", farFutureMillis())
	// A slot dir that happens to be named exactly what main's email
	// slugifies to, but belongs to a different, unrelated account — the
	// email-match loop in ensureSaveBackSlot can't claim it, so auto-create
	// must refuse to write into it rather than silently adopting it.
	env.writeSlot(t, "collide-example-com", "someone-else@example.com", "Someone Else", "pro",
		"someone-access", "someone-refresh", farFutureMillis())

	before := env.snapshotMainState(t)
	_, _, err := SwapMainTo("target-slot")
	if err == nil {
		t.Fatal("SwapMainTo: want error when save-back slot name collides with an unrelated existing slot")
	}
	env.assertMainStateUnchanged(t, before)
}

func TestSwapMainToAbortSaveBackIsTarget(t *testing.T) {
	env := newSwapTestEnv(t)
	env.writeMainClaudeJSON(t, map[string]any{"oauthAccount": map[string]any{"emailAddress": "same@example.com"}})
	env.setMainKeychainCreds(t, "main-access", "main-refresh", farFutureMillis())
	// The rotation target's own identity matches main's — its mirror slot IS
	// the rotation target, so there is nowhere safe to save main's creds.
	env.writeSlot(t, "target-slot", "same@example.com", "Target", "pro",
		"target-access", "target-refresh", farFutureMillis())

	before := env.snapshotMainState(t)
	_, _, err := SwapMainTo("target-slot")
	if err == nil {
		t.Fatal("SwapMainTo: want error when save-back slot would be the rotation target")
	}
	env.assertMainStateUnchanged(t, before)
}

func TestSwapMainToAbortInvalidSlug(t *testing.T) {
	for _, slug := range []string{"", "../x", ".hidden", "a/b"} {
		t.Run(slug, func(t *testing.T) {
			env := newSwapTestEnv(t)
			env.writeMainClaudeJSON(t, map[string]any{"oauthAccount": map[string]any{"emailAddress": "main@example.com"}})
			env.setMainKeychainCreds(t, "main-access", "main-refresh", farFutureMillis())

			before := env.snapshotMainState(t)
			_, _, err := SwapMainTo(slug)
			if err == nil {
				t.Fatalf("SwapMainTo(%q): want error for invalid slug", slug)
			}
			// Pin down that this is validateSlug rejecting the slug itself,
			// not some later stage (e.g. loadSlotCreds failing because the
			// slug also happens not to resolve to any real slot) — those
			// would fail for an unrelated reason and this test would still
			// go green with validateSlug deleted entirely.
			if !strings.Contains(err.Error(), "invalid slot slug") {
				t.Errorf("SwapMainTo(%q) error = %q, want it to mention %q", slug, err.Error(), "invalid slot slug")
			}
			env.assertMainStateUnchanged(t, before)
		})
	}
}

func TestSwapMainToAbortTargetDisabled(t *testing.T) {
	env := newSwapTestEnv(t)
	env.writeMainClaudeJSON(t, map[string]any{"oauthAccount": map[string]any{"emailAddress": "main@example.com"}})
	env.setMainKeychainCreds(t, "main-access", "main-refresh", farFutureMillis())
	dir := env.writeSlot(t, "disabled-slot", "target@example.com", "Target", "pro",
		"target-access", "target-refresh", farFutureMillis())
	if err := os.WriteFile(filepath.Join(dir, ".disabled"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	before := env.snapshotMainState(t)
	_, _, err := SwapMainTo("disabled-slot")
	if err == nil {
		t.Fatal("SwapMainTo: want error for disabled target slot")
	}
	env.assertMainStateUnchanged(t, before)
}

func TestSwapMainToAbortTargetNoIdentityAnywhere(t *testing.T) {
	env := newSwapTestEnv(t)
	env.writeMainClaudeJSON(t, map[string]any{"oauthAccount": map[string]any{"emailAddress": "main@example.com"}})
	env.setMainKeychainCreds(t, "main-access", "main-refresh", farFutureMillis())

	dir := filepath.Join(env.pool, "no-identity-slot")
	mustMkdir(t, dir)
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"),
		[]byte(credsFixtureJSON("target-access", "target-refresh", farFutureMillis())), 0o600); err != nil {
		t.Fatal(err)
	}
	// Deliberately no account.json and no .claude.json — nothing anywhere to
	// derive an identity from.

	before := env.snapshotMainState(t)
	_, _, err := SwapMainTo("no-identity-slot")
	if err == nil {
		t.Fatal("SwapMainTo: want error when target slot has no identity anywhere")
	}
	env.assertMainStateUnchanged(t, before)
}

// --- SwapMainTo: defensive-degrade + malformed-main-doc coverage (F3) ---

func TestSwapMainToDegradesOnMalformedSlotClaudeJSON(t *testing.T) {
	env := newSwapTestEnv(t)
	env.writeMainClaudeJSON(t, map[string]any{"oauthAccount": map[string]any{"emailAddress": "main@example.com"}})
	env.setMainKeychainCreds(t, "main-access", "main-refresh", farFutureMillis())
	dir := env.writeSlot(t, "target-slot", "target@example.com", "Target Person", "default_claude_pro",
		"target-access", "target-refresh", farFutureMillis())
	if err := os.WriteFile(filepath.Join(dir, ".claude.json"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	from, to, err := SwapMainTo("target-slot")
	if err != nil {
		t.Fatalf("SwapMainTo: want success degrading past malformed slot .claude.json, got: %v", err)
	}
	if to == "" {
		t.Fatal("SwapMainTo: to must never be empty on a successful swap")
	}
	if from != "main@example.com" || to != "target@example.com" {
		t.Errorf("from=%q to=%q, want main@example.com / target@example.com", from, to)
	}

	updatedDoc, err := os.ReadFile(env.claudeDoc)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(updatedDoc, &doc) != nil {
		t.Fatal("~/.claude.json not valid JSON after swap")
	}
	var oauthAccount struct {
		EmailAddress              string `json:"emailAddress"`
		DisplayName               string `json:"displayName"`
		OrganizationRateLimitTier string `json:"organizationRateLimitTier"`
	}
	if json.Unmarshal(doc["oauthAccount"], &oauthAccount) != nil {
		t.Fatal("oauthAccount not valid JSON")
	}
	if oauthAccount.EmailAddress != "target@example.com" || oauthAccount.DisplayName != "Target Person" ||
		oauthAccount.OrganizationRateLimitTier != "default_claude_pro" {
		t.Errorf("synthesized oauthAccount = %+v, want target identity incl. tier", oauthAccount)
	}
}

// TestSwapMainToAbortMalformedMainClaudeJSON covers a malformed (present but
// unparseable) main ~/.claude.json. In practice this trips the F1 "main
// identity unknown" guard rather than prepareMainInstall's own parse error —
// mainAccountMeta() and prepareMainInstall both parse the exact same bytes,
// so a malformed doc always yields an empty main email first. Either failure
// is pre-commit and leaves main untouched, which is the property this test
// actually asserts.
func TestSwapMainToAbortMalformedMainClaudeJSON(t *testing.T) {
	env := newSwapTestEnv(t)
	if err := os.WriteFile(env.claudeDoc, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	env.setMainKeychainCreds(t, "main-access", "main-refresh", farFutureMillis())
	env.writeSlot(t, "target-slot", "target@example.com", "Target", "pro",
		"target-access", "target-refresh", farFutureMillis())

	before := env.snapshotMainState(t)
	_, _, err := SwapMainTo("target-slot")
	if err == nil {
		t.Fatal("SwapMainTo: want error for malformed main ~/.claude.json")
	}
	env.assertMainStateUnchanged(t, before)
}

// TestSwapMainToRefreshesExpiredTarget covers a successful refresh of an
// expired target: the refreshed creds must persist to the slot file, the
// main keychain must end up with the refreshed (not stale) token, and the
// whole thing must still respect the commit-point ordering.
func TestSwapMainToRefreshesExpiredTarget(t *testing.T) {
	env := newSwapTestEnv(t)
	env.writeMainClaudeJSON(t, map[string]any{"oauthAccount": map[string]any{"emailAddress": "main@example.com"}})
	env.setMainKeychainCreds(t, "main-access", "main-refresh", farFutureMillis())
	dir := env.writeSlot(t, "expired-slot", "target@example.com", "Target", "pro",
		"target-access", "target-refresh", 1000) // already expired

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "refreshed-access",
			"refresh_token": "refreshed-refresh",
			"expires_in":    3600,
		})
	}))
	defer server.Close()
	stubEndpoints(t, "", server.URL)

	from, to, err := SwapMainTo("expired-slot")
	if err != nil {
		t.Fatalf("SwapMainTo: %v", err)
	}
	if to == "" {
		t.Fatal("SwapMainTo: to must never be empty on a successful swap")
	}
	if from != "main@example.com" || to != "target@example.com" {
		t.Errorf("from=%q to=%q, want main@example.com / target@example.com", from, to)
	}

	slotData, err := os.ReadFile(filepath.Join(dir, ".credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	slotCreds, ok := decodeOauthCreds(slotData)
	if !ok || slotCreds.ClaudeAiOauth.AccessToken != "refreshed-access" {
		t.Errorf("slot creds = %+v, ok=%v, want refreshed-access", slotCreds.ClaudeAiOauth, ok)
	}

	mainRaw := env.keychain[mainKeychainService]
	mainCreds, ok := decodeOauthCreds([]byte(mainRaw))
	if !ok || mainCreds.ClaudeAiOauth.AccessToken != "refreshed-access" {
		t.Errorf("main keychain creds = %+v, ok=%v, want refreshed-access", mainCreds.ClaudeAiOauth, ok)
	}
}

// TestSwapMainToCanonicalizesSlugCaseBeforeKeychainMirror covers X1: calling
// SwapMainTo with a case-differing spelling of an on-disk slug must resolve
// to the SAME slotDir the pool itself uses, so a refresh triggered mid-swap
// mirrors into the canonical keychain service (slotKeychainService hashes
// the literal path string, so a miscased path hashes to a DIFFERENT
// service — leaving the canonical one stale).
func TestSwapMainToCanonicalizesSlugCaseBeforeKeychainMirror(t *testing.T) {
	env := newSwapTestEnv(t)
	env.writeMainClaudeJSON(t, map[string]any{"oauthAccount": map[string]any{"emailAddress": "main@example.com"}})
	env.setMainKeychainCreds(t, "main-access", "main-refresh", farFutureMillis())
	dir := env.writeSlot(t, "target-slot", "target@example.com", "Target", "pro",
		"target-access", "target-refresh", 1000) // already expired -> forces a refresh mid-swap

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "refreshed-access",
			"refresh_token": "refreshed-refresh",
			"expires_in":    3600,
		})
	}))
	defer server.Close()
	stubEndpoints(t, "", server.URL)

	// Miscased caller spelling of the on-disk "target-slot" slug.
	_, to, err := SwapMainTo("Target-Slot")
	if err != nil {
		t.Fatalf("SwapMainTo: %v", err)
	}
	if to != "target@example.com" {
		t.Fatalf("to=%q, want target@example.com", to)
	}

	canonicalService := slotKeychainService(dir)
	miscasedService := slotKeychainService(filepath.Join(env.pool, "Target-Slot"))

	if _, ok := env.keychain[miscasedService]; ok {
		t.Errorf("refreshed slot creds mirrored under the miscased-path keychain service %q, want only the canonical service %q", miscasedService, canonicalService)
	}
	canonCreds, ok := decodeOauthCreds([]byte(env.keychain[canonicalService]))
	if !ok || canonCreds.ClaudeAiOauth.AccessToken != "refreshed-access" {
		t.Errorf("canonical slot keychain creds = %+v, ok=%v, want refreshed-access", canonCreds.ClaudeAiOauth, ok)
	}
}

// --- SelectRotationTarget: additional boundary coverage ---

func TestSelectRotationTargetMainEmailEmpty(t *testing.T) {
	rows := []AccountUsage{acctRow("good-slot", 10)}
	slug, reason := SelectRotationTarget(rows, "", 1000)
	if slug != "" || reason != "main account email unknown" {
		t.Errorf("slug=%q reason=%q, want empty slug and 'main account email unknown'", slug, reason)
	}
}

func TestSelectRotationTargetCooldownExactlyNowIsEligible(t *testing.T) {
	now := int64(1000)
	rows := []AccountUsage{
		{Slug: "at-cooldown", Email: "at-cooldown@example.com", Session: &UsageWindow{Percent: 10}, CooldownUntil: now},
	}
	slug, reason := SelectRotationTarget(rows, "main@example.com", now)
	if slug != "at-cooldown" || reason != "" {
		t.Fatalf("slug=%q reason=%q, want at-cooldown eligible when CooldownUntil == now", slug, reason)
	}
}

func TestSelectRotationTargetBoundaryJustUnderCandidateThresholds(t *testing.T) {
	rows := []AccountUsage{
		{Slug: "just-under", Email: "just-under@example.com", Session: &UsageWindow{Percent: 79.9}, Models: modelWindow("Fable", 79.9)},
	}
	slug, reason := SelectRotationTarget(rows, "main@example.com", 0)
	if slug != "just-under" || reason != "" {
		t.Fatalf("slug=%q reason=%q, want just-under eligible at 79.9%% session/fable", slug, reason)
	}
}

// --- Fable matching: case-insensitivity + duplicate-window max (F7) ---

func TestNeedsRotationFableCaseInsensitive(t *testing.T) {
	main := AccountUsage{Models: modelWindow("fable", 90.1)}
	tripped, reason := NeedsRotation(main)
	if !tripped || reason != "fable 90.1% > 90%" {
		t.Errorf("tripped=%v reason=%q, want lowercase \"fable\" window to trip", tripped, reason)
	}
}

func TestNeedsRotationFableDuplicateWindowsUseMax(t *testing.T) {
	main := AccountUsage{Models: []ModelWindow{
		{Name: "Fable", Percent: 50},
		{Name: "Fable", Percent: 92},
		{Name: "Fable", Percent: 70},
	}}
	tripped, reason := NeedsRotation(main)
	if !tripped || reason != "fable 92% > 90%" {
		t.Errorf("tripped=%v reason=%q, want the max duplicate window (92%%) to trip", tripped, reason)
	}
}

func TestSelectRotationTargetFableDuplicateWindowsUseMax(t *testing.T) {
	rows := []AccountUsage{
		{Slug: "dup-fable", Email: "dup-fable@example.com", Session: &UsageWindow{Percent: 10}, Models: []ModelWindow{
			{Name: "Fable", Percent: 50},
			{Name: "Fable", Percent: 85}, // max (85) >= candidateFablePct(80) -> excluded
		}},
	}
	slug, reason := SelectRotationTarget(rows, "main@example.com", 0)
	if slug != "" || reason != "dup-fable: fable 85% ≥ 80%" {
		t.Fatalf("slug=%q reason=%q, want dup-fable excluded via max duplicate fable window", slug, reason)
	}
}

// --- SelectRotationTarget: no-identity exclusion (R3) ---

func TestSelectRotationTargetExcludesEmaillessRow(t *testing.T) {
	rows := []AccountUsage{
		{Slug: "no-identity-slot", Session: &UsageWindow{Percent: 10}}, // Email == ""
	}
	slug, reason := SelectRotationTarget(rows, "main@example.com", 0)
	if slug != "" || reason != "no-identity-slot: no identity" {
		t.Fatalf("slug=%q reason=%q, want no-identity-slot excluded via 'no identity'", slug, reason)
	}
}

// --- SwapMainTo: commit-point rollback (R1) ---

// TestSwapMainToRollsBackKeychainOnClaudeJSONWriteFailure covers the exact
// F1 divergence shape: writeMainCreds (the keychain half of INSTALL)
// succeeds, then the ~/.claude.json write fails. Without a rollback, the
// keychain would hold the target's creds while ~/.claude.json still names
// the old main account — and the NEXT rotation would then save the
// target's creds over the old main's own slot, destroying them.
func TestSwapMainToRollsBackKeychainOnClaudeJSONWriteFailure(t *testing.T) {
	env := newSwapTestEnv(t)

	// Point ~/.claude.json at a file inside a directory we seed FIRST and
	// make read-only AFTER seeding — so the pre-commit reads (mainAccountMeta,
	// prepareMainInstall) succeed, but the commit-point atomicWriteFile
	// (which needs to create a temp file in that same directory) fails.
	homeDir := filepath.Dir(env.claudeDoc)
	roDir := filepath.Join(homeDir, "readonly-claude-dir")
	if err := os.MkdirAll(roDir, 0o755); err != nil {
		t.Fatal(err)
	}
	claudeDoc := filepath.Join(roDir, ".claude.json")
	mainDocBytes, err := json.Marshal(map[string]any{
		"oauthAccount": map[string]any{"emailAddress": "main@example.com", "displayName": "Main Person"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(claudeDoc, mainDocBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	origPath := claudeJSONPath
	claudeJSONPath = func() string { return claudeDoc }
	t.Cleanup(func() {
		claudeJSONPath = origPath
		_ = os.Chmod(roDir, 0o755) // restore write perm so TempDir cleanup can remove it
	})

	env.setMainKeychainCreds(t, "main-access", "main-refresh", farFutureMillis())
	env.writeSlot(t, "target-slot", "target@example.com", "Target Person", "pro",
		"target-access", "target-refresh", farFutureMillis())

	// Lock the directory down AFTER the doc exists: reads still work
	// (r-x), but atomicWriteFile's CreateTemp needs write access and fails.
	if err := os.Chmod(roDir, 0o555); err != nil {
		t.Fatal(err)
	}

	origMainCreds, origOK := decodeOauthCreds([]byte(env.keychain[mainKeychainService]))
	if !origOK {
		t.Fatal("precondition: seeded main keychain entry didn't decode")
	}

	_, _, swapErr := SwapMainTo("target-slot")
	if swapErr == nil {
		t.Fatal("SwapMainTo: want error when the ~/.claude.json write fails after the keychain commit")
	}
	if !strings.Contains(swapErr.Error(), "main creds rolled back") {
		t.Errorf("SwapMainTo error = %q, want it to mention the rollback", swapErr.Error())
	}

	// The crux of R1: the main keychain must be back to the ORIGINAL main
	// creds, not left holding the target's creds while ~/.claude.json still
	// names the old main account — the exact split-brain this guards against.
	// Compared by decoded fields, not raw bytes: writeMainCreds re-marshals
	// through oauthCreds.marshal(), which re-serializes the JSON (compact,
	// sorted keys) rather than byte-for-byte preserving the original
	// pretty-printed fixture, even though the content round-trips losslessly.
	mainCreds, ok := decodeOauthCreds([]byte(env.keychain[mainKeychainService]))
	if !ok || mainCreds.ClaudeAiOauth.AccessToken != origMainCreds.ClaudeAiOauth.AccessToken ||
		mainCreds.ClaudeAiOauth.RefreshToken != origMainCreds.ClaudeAiOauth.RefreshToken {
		t.Errorf("main keychain creds after rollback = %+v, ok=%v, want rolled back to original %+v",
			mainCreds.ClaudeAiOauth, ok, origMainCreds.ClaudeAiOauth)
	}
	if mainCreds.ClaudeAiOauth.AccessToken != "main-access" {
		t.Errorf("main keychain creds after rollback = %+v, want main-access", mainCreds.ClaudeAiOauth)
	}

	// ~/.claude.json itself must be untouched — atomicWriteFile never got
	// past creating its temp file, so no rename ever happened.
	after, err := os.ReadFile(claudeDoc)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(mainDocBytes) {
		t.Errorf("~/.claude.json was modified despite the write failing: got %s", after)
	}

	// X2(c): a SUCCESSFUL rollback must remove the rotation journal — only
	// a FAILED rollback (see TestSwapMainToRollbackFailedLeavesJournal)
	// leaves it behind for a human.
	journal := filepath.Join(env.pool, ".rotation-inflight")
	if _, statErr := os.Stat(journal); !os.IsNotExist(statErr) {
		t.Errorf("rotation journal should be removed after a successful rollback, stat err: %v", statErr)
	}
}

// --- SwapMainTo: rotation journal across the commit point (X2/X3/X4) ---

// TestSwapMainToRefusesWhenJournalPresent covers X2(a): a pre-seeded journal
// (left behind by some previous, incomplete rotation) must make SwapMainTo
// refuse outright, with main left completely untouched.
func TestSwapMainToRefusesWhenJournalPresent(t *testing.T) {
	env := newSwapTestEnv(t)
	env.writeMainClaudeJSON(t, map[string]any{"oauthAccount": map[string]any{"emailAddress": "main@example.com"}})
	env.setMainKeychainCreds(t, "main-access", "main-refresh", farFutureMillis())
	env.writeSlot(t, "target-slot", "target@example.com", "Target", "pro",
		"target-access", "target-refresh", farFutureMillis())

	journal := filepath.Join(env.pool, ".rotation-inflight")
	if err := os.WriteFile(journal, []byte("old@example.com -> mid@example.com"), 0o600); err != nil {
		t.Fatal(err)
	}

	before := env.snapshotMainState(t)
	_, _, err := SwapMainTo("target-slot")
	if err == nil {
		t.Fatal("SwapMainTo: want error when a previous rotation's journal is present")
	}
	if !strings.Contains(err.Error(), "did not complete") {
		t.Errorf("SwapMainTo error = %q, want it to mention the incomplete rotation", err.Error())
	}
	env.assertMainStateUnchanged(t, before)
	if _, statErr := os.Stat(journal); statErr != nil {
		t.Errorf("pre-seeded journal should remain untouched, stat err: %v", statErr)
	}
}

// TestSwapMainToHappyPathRemovesJournal covers X2(b): a successful swap must
// remove the journal it wrote for itself, so the pool isn't permanently
// wedged after every single successful rotation.
func TestSwapMainToHappyPathRemovesJournal(t *testing.T) {
	env := newSwapTestEnv(t)
	env.writeMainClaudeJSON(t, map[string]any{"oauthAccount": map[string]any{"emailAddress": "main@example.com"}})
	env.setMainKeychainCreds(t, "main-access", "main-refresh", farFutureMillis())
	env.writeSlot(t, "target-slot", "target@example.com", "Target", "pro",
		"target-access", "target-refresh", farFutureMillis())

	if _, _, err := SwapMainTo("target-slot"); err != nil {
		t.Fatalf("SwapMainTo: %v", err)
	}

	journal := filepath.Join(env.pool, ".rotation-inflight")
	if _, statErr := os.Stat(journal); !os.IsNotExist(statErr) {
		t.Errorf("rotation journal should be removed after a successful swap, stat err: %v", statErr)
	}
}

// failNthSecurityAddToService wraps securityAdd so the Nth call whose
// service matches targetService fails (the store is left untouched for that
// call, as a real security(1) failure would leave the keychain item
// unchanged); every other call, including calls to unrelated services,
// behaves like the normal in-memory stub.
func failNthSecurityAddToService(t *testing.T, store map[string]string, targetService string, n int) {
	t.Helper()
	orig := securityAdd
	count := 0
	securityAdd = func(service, account, value string) error {
		if service == targetService {
			count++
			if count == n {
				return fmt.Errorf("simulated keychain failure (call %d to %s)", count, service)
			}
		}
		store[service] = value
		return nil
	}
	t.Cleanup(func() { securityAdd = orig })
}

// replaceNthSecurityFindForService wraps securityFind so the Nth call whose
// service matches targetService both overwrites and returns replacement,
// instead of whatever the store currently holds for that service —
// mirroring a concurrent process (another rotation, a fresh `ccp login`)
// rewriting that keychain entry in between our own reads of it. Every other
// call, including calls to unrelated services, behaves like the normal
// in-memory stub.
func replaceNthSecurityFindForService(t *testing.T, store map[string]string, targetService string, n int, replacement string) {
	t.Helper()
	orig := securityFind
	count := 0
	securityFind = func(service, account string) (string, error) {
		if service == targetService {
			count++
			if count == n {
				store[service] = replacement
				return replacement, nil
			}
		}
		v, ok := store[service]
		if !ok {
			return "", fmt.Errorf("not found: %s", service)
		}
		return v, nil
	}
	t.Cleanup(func() { securityFind = orig })
}

// TestSwapMainToInstallFailureDropsJournal covers Y1: writeMainCreds itself
// failing (keychain locked/denied) is an ordinary pre-commit abort — the
// FIRST securityAdd call to the main service IS the commit-point install
// write, so main keychain is untouched. The journal was written one line
// earlier and must be dropped rather than left behind to wedge every future
// rotation.
func TestSwapMainToInstallFailureDropsJournal(t *testing.T) {
	env := newSwapTestEnv(t)
	env.writeMainClaudeJSON(t, map[string]any{"oauthAccount": map[string]any{"emailAddress": "main@example.com"}})
	env.setMainKeychainCreds(t, "main-access", "main-refresh", farFutureMillis())
	env.writeSlot(t, "target-slot", "target@example.com", "Target Person", "pro",
		"target-access", "target-refresh", farFutureMillis())

	failNthSecurityAddToService(t, env.keychain, mainKeychainService, 1)

	before := env.snapshotMainState(t)
	_, _, err := SwapMainTo("target-slot")
	if err == nil {
		t.Fatal("SwapMainTo: want error when writeMainCreds itself fails")
	}
	if !strings.Contains(err.Error(), "install target creds") {
		t.Errorf("SwapMainTo error = %q, want it to mention the install-target-creds failure", err.Error())
	}
	env.assertMainStateUnchanged(t, before)

	journal := filepath.Join(env.pool, ".rotation-inflight")
	if _, statErr := os.Stat(journal); !os.IsNotExist(statErr) {
		t.Errorf("rotation journal should be dropped after a clean pre-commit abort, stat err: %v", statErr)
	}

	// A follow-up rotation attempt must NOT be refused by a wedged journal
	// (the securityAdd stub only fails its Nth=1 call, so this one lands
	// normally).
	if _, _, err := SwapMainTo("target-slot"); err != nil {
		t.Fatalf("follow-up SwapMainTo: want success (journal must not wedge future rotations), got: %v", err)
	}
}

// TestSwapMainToRollbackSkippedOnForeignConcurrentWrite covers Y2/Y5: the
// ~/.claude.json write fails after the keychain commit, and the rollback's
// CAS reload observes a FOREIGN token — neither ours nor the original
// main's — simulating a concurrent process (another rotation, a fresh `ccp
// login`) rewriting the main keychain in between. The rollback must be
// skipped rather than clobbering that foreign write, the journal must
// remain for a human, and the main keychain must still hold the foreign
// blob afterward.
func TestSwapMainToRollbackSkippedOnForeignConcurrentWrite(t *testing.T) {
	env := newSwapTestEnv(t)

	homeDir := filepath.Dir(env.claudeDoc)
	roDir := filepath.Join(homeDir, "readonly-claude-dir")
	if err := os.MkdirAll(roDir, 0o755); err != nil {
		t.Fatal(err)
	}
	claudeDoc := filepath.Join(roDir, ".claude.json")
	mainDocBytes, err := json.Marshal(map[string]any{
		"oauthAccount": map[string]any{"emailAddress": "main@example.com", "displayName": "Main Person"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(claudeDoc, mainDocBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	origPath := claudeJSONPath
	claudeJSONPath = func() string { return claudeDoc }
	t.Cleanup(func() {
		claudeJSONPath = origPath
		_ = os.Chmod(roDir, 0o755) // restore write perm so TempDir cleanup can remove it
	})

	env.setMainKeychainCreds(t, "main-access", "main-refresh", farFutureMillis())
	env.writeSlot(t, "target-slot", "target@example.com", "Target Person", "pro",
		"target-access", "target-refresh", farFutureMillis())

	if err := os.Chmod(roDir, 0o555); err != nil {
		t.Fatal(err)
	}

	// The reload inside the rollback CAS check is the SECOND securityFind
	// call to the main service — the first is loadMainCreds at step 3.
	// Make it return a foreign blob, as if some other process rewrote
	// main's keychain entry between our commit-point write and this reload.
	foreignBlob := credsFixtureJSON("foreign-access", "foreign-refresh", farFutureMillis())
	replaceNthSecurityFindForService(t, env.keychain, mainKeychainService, 2, foreignBlob)

	_, _, swapErr := SwapMainTo("target-slot")
	if swapErr == nil {
		t.Fatal("SwapMainTo: want error when the ~/.claude.json write fails and a foreign write is observed on reload")
	}
	if !strings.Contains(swapErr.Error(), "rollback skipped") {
		t.Errorf("SwapMainTo error = %q, want it to mention 'rollback skipped'", swapErr.Error())
	}

	journal := filepath.Join(env.pool, ".rotation-inflight")
	if _, statErr := os.Stat(journal); statErr != nil {
		t.Errorf("rotation journal should remain after a skipped rollback, stat err: %v", statErr)
	}

	// Main keychain must still hold the FOREIGN blob — proof we didn't
	// clobber it with our own rollback write.
	if got := env.keychain[mainKeychainService]; got != foreignBlob {
		t.Errorf("main keychain = %q, want it to still hold the foreign blob %q (rollback must not have overwritten it)", got, foreignBlob)
	}
}

// TestSwapMainToRollbackFailedLeavesJournal covers X4: the install write to
// ~/.claude.json fails AND the rollback's re-write of main's keychain entry
// also fails. The error must say ROLLBACK FAILED, name both accounts, and —
// unlike a successful rollback — the journal must remain so a human can see
// a rotation is stuck mid-flight.
func TestSwapMainToRollbackFailedLeavesJournal(t *testing.T) {
	env := newSwapTestEnv(t)

	homeDir := filepath.Dir(env.claudeDoc)
	roDir := filepath.Join(homeDir, "readonly-claude-dir")
	if err := os.MkdirAll(roDir, 0o755); err != nil {
		t.Fatal(err)
	}
	claudeDoc := filepath.Join(roDir, ".claude.json")
	mainDocBytes, err := json.Marshal(map[string]any{
		"oauthAccount": map[string]any{"emailAddress": "main@example.com", "displayName": "Main Person"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(claudeDoc, mainDocBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	origPath := claudeJSONPath
	claudeJSONPath = func() string { return claudeDoc }
	t.Cleanup(func() {
		claudeJSONPath = origPath
		_ = os.Chmod(roDir, 0o755) // restore write perm so TempDir cleanup can remove it
	})

	env.setMainKeychainCreds(t, "main-access", "main-refresh", farFutureMillis())
	env.writeSlot(t, "target-slot", "target@example.com", "Target Person", "pro",
		"target-access", "target-refresh", farFutureMillis())

	if err := os.Chmod(roDir, 0o555); err != nil {
		t.Fatal(err)
	}

	// The FIRST securityAdd call to the main service is writeMainCreds
	// (INSTALL); the SECOND is the rollback attempt after the
	// ~/.claude.json write fails below. Fail that second call.
	failNthSecurityAddToService(t, env.keychain, mainKeychainService, 2)

	_, _, swapErr := SwapMainTo("target-slot")
	if swapErr == nil {
		t.Fatal("SwapMainTo: want error when both the install write and the rollback fail")
	}
	if !strings.Contains(swapErr.Error(), "ROLLBACK FAILED") {
		t.Errorf("SwapMainTo error = %q, want it to mention ROLLBACK FAILED", swapErr.Error())
	}
	if !strings.Contains(swapErr.Error(), "main@example.com") || !strings.Contains(swapErr.Error(), "target@example.com") {
		t.Errorf("SwapMainTo error = %q, want it to name both accounts", swapErr.Error())
	}

	journal := filepath.Join(env.pool, ".rotation-inflight")
	if _, statErr := os.Stat(journal); statErr != nil {
		t.Errorf("rotation journal should remain after a ROLLBACK FAILED, stat err: %v", statErr)
	}
}

// --- SwapMainTo: case-insensitive save-back/target collision (R2) ---

// NOTE (X6c): this pins the collision outcome, but the exact failure mode
// depends on how directory-entry case-folding is resolved. On the default
// case-insensitive macOS APFS — and, after the X1 slug-canonicalization fix
// in SwapMainTo, on ANY filesystem, since that canonicalization is a pure
// string EqualFold against the in-memory poolSlugs() listing, not a
// filesystem call — "Main-Example-Com" and "main-example-com" resolve to
// the exact same slotDir, so the save-back step must refuse with
// "save-back slot would be the rotation target". If some future change
// removed that canonicalization on a case-sensitive volume, the two
// spellings would instead be genuinely distinct slot dirs, and
// SwapMainTo would treat "Main-Example-Com" as a brand-new,
// no-credentials rotation target — so this test accepts either failure
// mode rather than assuming one requires a specific filesystem.
func TestSwapMainToAbortSaveBackCaseInsensitiveCollision(t *testing.T) {
	env := newSwapTestEnv(t)
	env.writeMainClaudeJSON(t, map[string]any{"oauthAccount": map[string]any{"emailAddress": "main@example.com"}})
	env.setMainKeychainCreds(t, "main-access", "main-refresh", farFutureMillis())
	// Existing slot mirrors main's email under a lowercase slug -- this is
	// the slot ensureSaveBackSlot's email-match loop will find.
	env.writeSlot(t, "main-example-com", "main@example.com", "Main Mirror", "pro",
		"mirror-access", "mirror-refresh", farFutureMillis())

	before := env.snapshotMainState(t)
	// Rotating INTO a case-differing spelling of that same slug: on
	// case-insensitive APFS (the default macOS filesystem) this is the exact
	// same physical directory, so it must trip "save-back slot would be the
	// rotation target" instead of being silently treated as distinct.
	_, _, err := SwapMainTo("Main-Example-Com")
	if err == nil {
		t.Fatal("SwapMainTo: want error when the save-back slot is a case-insensitive collision with the rotation target")
	}
	if !strings.Contains(err.Error(), "save-back slot would be the rotation target") &&
		!strings.Contains(err.Error(), "no credentials") {
		t.Errorf("SwapMainTo error = %q, want either the save-back/target collision message or a no-credentials error (case-sensitive-volume fallback)", err.Error())
	}
	env.assertMainStateUnchanged(t, before)
}

// TestSameSlotDirCaseFoldIsFilesystemIndependent covers X6(b): sameSlotDir's
// EqualFold check must fire from the strings alone, without touching the
// filesystem at all — pinned against paths that don't even exist on disk.
func TestSameSlotDirCaseFoldIsFilesystemIndependent(t *testing.T) {
	if !sameSlotDir("/nonexistent/pool/main-example-com", "/nonexistent/pool/Main-Example-Com") {
		t.Error("sameSlotDir should treat case-differing paths as the same slot via EqualFold alone, without touching the filesystem")
	}
}

// --- SwapMainTo: target identity verification (R3) ---

func TestSwapMainToAbortTargetCorruptAccountJSON(t *testing.T) {
	env := newSwapTestEnv(t)
	env.writeMainClaudeJSON(t, map[string]any{"oauthAccount": map[string]any{"emailAddress": "main@example.com"}})
	env.setMainKeychainCreds(t, "main-access", "main-refresh", farFutureMillis())

	dir := filepath.Join(env.pool, "corrupt-account-slot")
	mustMkdir(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "account.json"), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"),
		[]byte(credsFixtureJSON("target-access", "target-refresh", farFutureMillis())), 0o600); err != nil {
		t.Fatal(err)
	}

	before := env.snapshotMainState(t)
	_, _, err := SwapMainTo("corrupt-account-slot")
	if err == nil {
		t.Fatal("SwapMainTo: want error when the target slot's account.json is corrupt (no usable identity)")
	}
	if !strings.Contains(err.Error(), "no identity") {
		t.Errorf("SwapMainTo error = %q, want it to mention missing identity", err.Error())
	}
	env.assertMainStateUnchanged(t, before)
}

// TestSwapMainToTargetClaudeJSONEmailMismatchFallsBackToAccountJSON covers a
// slot dir whose own (ccp-owned) .claude.json names a DIFFERENT account than
// its account.json — e.g. left behind by a previous occupant of this slot
// dir. The mismatched identity must never get installed; the swap must fall
// back to the synthesized identity sourced from account.json instead.
func TestSwapMainToTargetClaudeJSONEmailMismatchFallsBackToAccountJSON(t *testing.T) {
	env := newSwapTestEnv(t)
	env.writeMainClaudeJSON(t, map[string]any{"oauthAccount": map[string]any{"emailAddress": "main@example.com"}})
	env.setMainKeychainCreds(t, "main-access", "main-refresh", farFutureMillis())
	dir := env.writeSlot(t, "target-slot", "target@example.com", "Target Person", "pro",
		"target-access", "target-refresh", farFutureMillis())
	staleDoc, err := json.Marshal(map[string]any{
		"oauthAccount": map[string]any{"emailAddress": "someone-else@example.com", "displayName": "Someone Else"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude.json"), staleDoc, 0o600); err != nil {
		t.Fatal(err)
	}

	from, to, err := SwapMainTo("target-slot")
	if err != nil {
		t.Fatalf("SwapMainTo: %v", err)
	}
	if to == "" {
		t.Fatal("SwapMainTo: to must never be empty on a successful swap")
	}
	if from != "main@example.com" || to != "target@example.com" {
		t.Errorf("from=%q to=%q, want main@example.com / target@example.com (the account.json identity, not the stale .claude.json one)", from, to)
	}

	updatedDoc, err := os.ReadFile(env.claudeDoc)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(updatedDoc, &doc) != nil {
		t.Fatal("~/.claude.json not valid JSON after swap")
	}
	var oauthAccount struct {
		EmailAddress string `json:"emailAddress"`
		DisplayName  string `json:"displayName"`
	}
	if json.Unmarshal(doc["oauthAccount"], &oauthAccount) != nil {
		t.Fatal("oauthAccount not valid JSON")
	}
	if oauthAccount.EmailAddress != "target@example.com" || oauthAccount.DisplayName != "Target Person" {
		t.Errorf("installed oauthAccount = %+v, want the synthesized target@example.com identity, not the stale someone-else@example.com one", oauthAccount)
	}
}

// --- SwapMainTo: save-back round-trip check is load-bearing (R4) ---

// TestSwapMainToAbortSaveBackRoundTripMismatch pins down the round-trip
// check at rotation.go's save-back step: with the stub securityAdd made a
// no-op, deleting that check would keep the suite green, since nothing else
// notices the save-back slot's keychain entry silently failing to update.
func TestSwapMainToAbortSaveBackRoundTripMismatch(t *testing.T) {
	env := newSwapTestEnv(t)
	env.writeMainClaudeJSON(t, map[string]any{"oauthAccount": map[string]any{"emailAddress": "main@example.com"}})
	env.setMainKeychainCreds(t, "main-access", "main-refresh", farFutureMillis())
	env.writeSlot(t, "target-slot", "target@example.com", "Target", "pro",
		"target-access", "target-refresh", farFutureMillis())

	// Pre-existing save-back slot for main's email, whose keychain entry
	// (not the file) will keep shadowing whatever writeSlotCreds writes.
	saveBackDir := env.writeSlot(t, "main-example-com", "main@example.com", "Main Mirror", "pro",
		"foreign-access", "foreign-refresh", farFutureMillis())
	env.keychain[slotKeychainService(saveBackDir)] = credsFixtureJSON("foreign-access", "foreign-refresh", farFutureMillis())

	// Make securityAdd a no-op so the write to the save-back slot's keychain
	// entry never actually lands -- the stale foreign entry keeps shadowing
	// the .credentials.json file writeSlotCreds DOES successfully write,
	// since loadSlotCreds prefers the keychain on read.
	origAdd := securityAdd
	securityAdd = func(service, account, value string) error { return nil }
	t.Cleanup(func() { securityAdd = origAdd })

	before := env.snapshotMainState(t)
	_, _, err := SwapMainTo("target-slot")
	if err == nil {
		t.Fatal("SwapMainTo: want error when the save-back slot's persisted creds do not round-trip")
	}
	if !strings.Contains(err.Error(), "did not round-trip") {
		t.Errorf("SwapMainTo error = %q, want it to mention the round-trip check", err.Error())
	}
	env.assertMainStateUnchanged(t, before)
}

// --- SwapMainTo: .disabled handling (R6) ---

// TestSwapMainToAbortTargetDisabledCheckFailsClosedOnStatError covers a stat
// error on the target's .disabled marker that ISN'T "not there" (here:
// slotDir is a plain file, not a directory, so the lookup fails with "not a
// directory"). The check must fail closed and refuse, not silently treat an
// ambiguous stat error as "no marker, proceed".
func TestSwapMainToAbortTargetDisabledCheckFailsClosedOnStatError(t *testing.T) {
	env := newSwapTestEnv(t)
	env.writeMainClaudeJSON(t, map[string]any{"oauthAccount": map[string]any{"emailAddress": "main@example.com"}})
	env.setMainKeychainCreds(t, "main-access", "main-refresh", farFutureMillis())
	slotPath := filepath.Join(env.pool, "not-a-dir-slot")
	if err := os.WriteFile(slotPath, []byte("oops"), 0o644); err != nil {
		t.Fatal(err)
	}

	before := env.snapshotMainState(t)
	_, _, err := SwapMainTo("not-a-dir-slot")
	if err == nil {
		t.Fatal("SwapMainTo: want error for a slot path that isn't even a directory (fail-closed disabled check)")
	}
	// An ENOTDIR stat error is ambiguous, not a confirmed marker — the
	// message must say so distinctly from the "slot IS disabled" case (X7).
	if !strings.Contains(err.Error(), "cannot check .disabled marker") {
		t.Errorf("SwapMainTo error = %q, want it to mention 'cannot check .disabled marker'", err.Error())
	}
	env.assertMainStateUnchanged(t, before)
}

// TestSwapMainToAbortSaveBackSlotDisabled covers ensureSaveBackSlot's
// email-match branch finding a disabled mirror slot: the outgoing main creds
// must not be written somewhere the pool will never surface (NoCreds
// forever because of the .disabled marker).
func TestSwapMainToAbortSaveBackSlotDisabled(t *testing.T) {
	env := newSwapTestEnv(t)
	env.writeMainClaudeJSON(t, map[string]any{"oauthAccount": map[string]any{"emailAddress": "main@example.com"}})
	env.setMainKeychainCreds(t, "main-access", "main-refresh", farFutureMillis())
	env.writeSlot(t, "target-slot", "target@example.com", "Target", "pro",
		"target-access", "target-refresh", farFutureMillis())
	mirrorDir := env.writeSlot(t, "main-example-com", "main@example.com", "Main Mirror", "pro",
		"mirror-access", "mirror-refresh", farFutureMillis())
	if err := os.WriteFile(filepath.Join(mirrorDir, ".disabled"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	before := env.snapshotMainState(t)
	_, _, err := SwapMainTo("target-slot")
	if err == nil {
		t.Fatal("SwapMainTo: want error when the save-back slot is disabled")
	}
	if !strings.Contains(err.Error(), "disabled") {
		t.Errorf("SwapMainTo error = %q, want it to mention 'disabled'", err.Error())
	}
	env.assertMainStateUnchanged(t, before)
}

// TestSwapMainToSaveBackSkipsDisabledMirrorForHealthyOne covers X8:
// ensureSaveBackSlot must skip a disabled email-matched slot and keep
// scanning rather than failing outright, so a healthy mirror later in sort
// order still wins over an earlier, disabled one.
func TestSwapMainToSaveBackSkipsDisabledMirrorForHealthyOne(t *testing.T) {
	env := newSwapTestEnv(t)
	env.writeMainClaudeJSON(t, map[string]any{"oauthAccount": map[string]any{"emailAddress": "main@example.com"}})
	env.setMainKeychainCreds(t, "main-access", "main-refresh", farFutureMillis())
	env.writeSlot(t, "target-slot", "target@example.com", "Target", "pro",
		"target-access", "target-refresh", farFutureMillis())

	// "main-backup" sorts before "main-example-com", so it's the FIRST
	// email match ensureSaveBackSlot's scan encounters — and it's disabled.
	disabledMirror := env.writeSlot(t, "main-backup", "main@example.com", "Main Backup", "pro",
		"backup-access", "backup-refresh", farFutureMillis())
	if err := os.WriteFile(filepath.Join(disabledMirror, ".disabled"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	healthyMirror := env.writeSlot(t, "main-example-com", "main@example.com", "Main Mirror", "pro",
		"mirror-access", "mirror-refresh", farFutureMillis())

	_, to, err := SwapMainTo("target-slot")
	if err != nil {
		t.Fatalf("SwapMainTo: want success skipping the disabled mirror for the healthy one, got: %v", err)
	}
	if to != "target@example.com" {
		t.Fatalf("to=%q, want target@example.com", to)
	}

	data, err := os.ReadFile(filepath.Join(healthyMirror, ".credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	creds, ok := decodeOauthCreds(data)
	if !ok || creds.ClaudeAiOauth.AccessToken != "main-access" {
		t.Errorf("healthy mirror slot not updated with outgoing main creds: %+v, ok=%v", creds.ClaudeAiOauth, ok)
	}

	disabledData, err := os.ReadFile(filepath.Join(disabledMirror, ".credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	disabledCreds, ok := decodeOauthCreds(disabledData)
	if !ok || disabledCreds.ClaudeAiOauth.AccessToken != "backup-access" {
		t.Errorf("disabled mirror slot should be left untouched, got: %+v, ok=%v", disabledCreds.ClaudeAiOauth, ok)
	}
}
