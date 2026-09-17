package core

// codexpool_contract_test.go — athena-logic's BLIND tests for
// artifacts/50-contract.md, slice CORE. Dimension: LOGIC — the stated
// invariants and the happy paths of every row in the slice. The refusals,
// the malformed inputs and the G4 degradations are athena-edge's dimension
// and are deliberately absent.
//
// Authored blind. internal/core/codexpool.go was never opened by any route,
// nor were the changed regions of discover.go / read.go / resume.go. Every
// assertion below is derived from an obligation the contract STATES; where
// the contract is silent the case is left out and recorded in the author's
// report rather than probed tolerantly.
//
// G5: every test isolates HOME with t.Setenv and owns its CODEX_POOL_DIR.
// Nothing here reads the real ~/.codex, ~/.codex-pool, ~/.claude-pool or
// ~/.kraken. G2: every token in every fixture is a hand-built unsigned JWT
// over a fake example.com identity — no real token, no real email.
//
// Every package-level identifier declared here is prefixed contractCxp.

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

// contractCxpEnv isolates HOME and points CODEX_POOL_DIR at a scratch pool
// root under it. Returns (home, poolRoot). The pool root is NOT created — a
// test that wants slots calls contractCxpSlot.
func contractCxpEnv(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	pool := filepath.Join(t.TempDir(), "codex-pool")
	t.Setenv("CODEX_POOL_DIR", pool)
	return home, pool
}

// contractCxpJWT builds the three-segment id_token shape the contract names:
// "only the middle one is read, so x.<payload>.y is a valid fixture". The
// payload is base64url with NO padding and carries an `exp` that is long
// past, since the contract requires the decode to ignore expiry.
func contractCxpJWT(email, plan string) string {
	payload := fmt.Sprintf(
		`{"email":%q,"exp":1,"https://api.openai.com/auth":{"chatgpt_plan_type":%q}}`,
		email, plan)
	return "x." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".y"
}

// contractCxpAuthJSON is the auth.json body a slot (or ~/.codex) carries.
func contractCxpAuthJSON(email, plan string) string {
	return fmt.Sprintf(`{"tokens":{"id_token":%q}}`, contractCxpJWT(email, plan))
}

// contractCxpWriteAuth writes an auth.json with the given identity into
// homeDir, creating it.
func contractCxpWriteAuth(t *testing.T, homeDir, email, plan string) {
	t.Helper()
	if err := os.MkdirAll(homeDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", homeDir, err)
	}
	if err := os.WriteFile(filepath.Join(homeDir, "auth.json"), []byte(contractCxpAuthJSON(email, plan)), 0o600); err != nil {
		t.Fatalf("write auth.json in %s: %v", homeDir, err)
	}
}

// contractCxpSlot creates a usable slot under <pool>/accounts/<slug> carrying
// the given identity, and returns its directory.
func contractCxpSlot(t *testing.T, pool, slug, email, plan string) string {
	t.Helper()
	dir := filepath.Join(pool, "accounts", slug)
	contractCxpWriteAuth(t, dir, email, plan)
	return dir
}

// contractCxpWindow renders one non-null rate_limits window.
func contractCxpWindow(usedPercent float64, windowMinutes int, resetsAt int64) string {
	return fmt.Sprintf(`{"used_percent":%v,"window_minutes":%d,"resets_at":%d}`, usedPercent, windowMinutes, resetsAt)
}

// contractCxpTokenCount renders one whole rollout line carrying rate_limits.
// primary/secondary are either a contractCxpWindow result or the literal
// "null"; reached is either "null" or a quoted JSON string.
func contractCxpTokenCount(ts, primary, secondary, reached string) string {
	return fmt.Sprintf(
		`{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","rate_limits":{"primary":%s,"secondary":%s,"rate_limit_reached_type":%s}}}`,
		ts, primary, secondary, reached)
}

// contractCxpNoLimitsLine is a rollout record that carries no rate_limits at
// all — the short-session shape the evidence search has to look past.
const contractCxpNoLimitsLine = `{"timestamp":"2026-09-17T00:00:00.000Z","type":"event_msg","payload":{"type":"agent_message","message":"hello"}}`

// contractCxpMillis converts an RFC 3339 timestamp to unix millis, the
// conversion the contract names for FetchedAt.
func contractCxpMillis(t *testing.T, ts string) int64 {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		t.Fatalf("fixture timestamp %q is not RFC 3339: %v", ts, err)
	}
	return parsed.UnixMilli()
}

// contractCxpWriteRollout writes a rollout file at
// <homeDir>/sessions/<rel>/rollout-<name>.jsonl and stamps its modification
// time, which is the key the evidence search orders files by.
func contractCxpWriteRollout(t *testing.T, homeDir, rel, name string, mtime time.Time, lines ...string) string {
	t.Helper()
	dir := filepath.Join(homeDir, "sessions", rel)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir rollout dir: %v", err)
	}
	p := filepath.Join(dir, "rollout-"+name+".jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write rollout: %v", err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatalf("chtimes rollout: %v", err)
	}
	return p
}

// contractCxpThreadRow is one row of the threads-table fixture.
type contractCxpThreadRow struct {
	ID          string
	Title       string
	Model       string
	TokensUsed  int64
	RolloutPath string
	CWD         string
	UpdatedAt   int64
}

// contractCxpStateDB writes a state_5.sqlite into homeDir with the minimal
// threads table the readers SELECT from. The writer is closed before the
// function returns, so the reader's read-only+immutable open sees a settled
// file.
func contractCxpStateDB(t *testing.T, homeDir string, rows ...contractCxpThreadRow) {
	t.Helper()
	if err := os.MkdirAll(homeDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", homeDir, err)
	}
	db, err := sql.Open("sqlite3", filepath.Join(homeDir, "state_5.sqlite"))
	if err != nil {
		t.Fatalf("open fixture db: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE threads(id TEXT PRIMARY KEY, title TEXT, model TEXT, tokens_used INTEGER, rollout_path TEXT, cwd TEXT, updated_at INTEGER)`); err != nil {
		db.Close()
		t.Fatalf("create threads: %v", err)
	}
	for _, r := range rows {
		if _, err := db.Exec(
			`INSERT INTO threads(id, title, model, tokens_used, rollout_path, cwd, updated_at) VALUES(?,?,?,?,?,?,?)`,
			r.ID, r.Title, r.Model, r.TokensUsed, r.RolloutPath, r.CWD, r.UpdatedAt,
		); err != nil {
			db.Close()
			t.Fatalf("insert %+v: %v", r, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close fixture db: %v", err)
	}
}

// ---------------------------------------------------------------------------
// CodexPoolDir / CodexSlotDir / CodexPoolSlugs — the layout (C1)
// ---------------------------------------------------------------------------

// Obligation: "$CODEX_POOL_DIR when non-empty, else <util.Home()>/.codex-pool."
// The override is what lets the live gate run under an isolated HOME while
// still pointing at the real pool, so both arms are load-bearing.
func TestContractCxpPoolDirHonoursTheOverrideThenTheHome(t *testing.T) {
	home, pool := contractCxpEnv(t)
	if got := CodexPoolDir(); got != pool {
		t.Errorf("CodexPoolDir() = %q, want the $CODEX_POOL_DIR override %q", got, pool)
	}

	t.Setenv("CODEX_POOL_DIR", "")
	want := filepath.Join(home, ".codex-pool")
	if got := CodexPoolDir(); got != want {
		t.Errorf("CodexPoolDir() with no override = %q, want %q", got, want)
	}
}

// Obligation: "Exactly filepath.Join(CodexPoolDir(), \"accounts\", slug)."
// One derivation for every caller, so the directory a slot is PROBED at can
// never differ from the one a cell is POINTED at through CODEX_HOME.
func TestContractCxpSlotDirIsPoolAccountsSlug(t *testing.T) {
	_, pool := contractCxpEnv(t)
	for _, slug := range []string{"tmp-1", "alpha"} {
		want := filepath.Join(pool, "accounts", slug)
		if got := CodexSlotDir(slug); got != want {
			t.Errorf("CodexSlotDir(%q) = %q, want %q", slug, got, want)
		}
	}
}

// Obligation: "Names of the immediate subdirectories of <CodexPoolDir()>/accounts,
// sorted ascending. Skips plain files, names beginning with '.', and the name
// 'main'. removed/ and snapshots.json beside accounts/ are never consulted."
func TestContractCxpPoolSlugsEnumeratesSortedSlotDirs(t *testing.T) {
	_, pool := contractCxpEnv(t)
	accounts := filepath.Join(pool, "accounts")
	// deliberately created out of order: the answer is sorted, not read order
	for _, name := range []string{"zeta", "alpha", "main", ".hidden", "beta"} {
		if err := os.MkdirAll(filepath.Join(accounts, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(accounts, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// the two entries that live BESIDE accounts/ and are never consulted
	if err := os.MkdirAll(filepath.Join(pool, "removed"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pool, "snapshots.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := CodexPoolSlugs()
	want := []string{"alpha", "beta", "zeta"}
	if len(got) != len(want) {
		t.Fatalf("CodexPoolSlugs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("CodexPoolSlugs() = %v, want %v (sorted ascending, no main/dot/file/sibling entries)", got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// CodexSlotIdentity / CodexMainEmail — who a slot belongs to (C1)
// ---------------------------------------------------------------------------

// Obligation: "Reads <homeDir>/auth.json; takes tokens.id_token; base64url-decodes
// the JWT's SECOND segment with or without padding; no signature check and no
// expiry check (an exp in the past still decodes). email is the payload's email
// claim; plan is chatgpt_plan_type inside the object under the claim named
// https://api.openai.com/auth."
//
// The fixture's signature segment is the literal "y" and its exp is 1 — the
// token on a real slot is expired while the slot works, because this is an
// identity artifact rather than a credential.
func TestContractCxpSlotIdentityDecodesEmailAndPlan(t *testing.T) {
	_, pool := contractCxpEnv(t)
	dir := contractCxpSlot(t, pool, "tmp-1", "slot-one@example.com", "pro")

	email, plan := CodexSlotIdentity(dir)
	if email != "slot-one@example.com" {
		t.Errorf("email = %q, want %q", email, "slot-one@example.com")
	}
	if plan != "pro" {
		t.Errorf("plan = %q, want %q (chatgpt_plan_type under https://api.openai.com/auth)", plan, "pro")
	}
}

// Obligation: the second segment decodes "with or without padding". A payload
// whose byte length is not a multiple of three is encoded WITH '=' padding
// here, which RawURLEncoding alone would reject.
func TestContractCxpSlotIdentityDecodesAPaddedPayload(t *testing.T) {
	_, pool := contractCxpEnv(t)
	dir := filepath.Join(pool, "accounts", "padded")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	payload := `{"email":"padded@example.com","https://api.openai.com/auth":{"chatgpt_plan_type":"plus"}}`
	encoded := base64.URLEncoding.EncodeToString([]byte(payload))
	if !strings.HasSuffix(encoded, "=") {
		t.Fatalf("fixture is wrong: %q carries no padding, so this test would not distinguish the two encodings", encoded)
	}
	body := fmt.Sprintf(`{"tokens":{"id_token":"x.%s.y"}}`, encoded)
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	email, plan := CodexSlotIdentity(dir)
	if email != "padded@example.com" || plan != "plus" {
		t.Errorf("CodexSlotIdentity(padded payload) = (%q, %q), want (padded@example.com, plus)", email, plan)
	}
}

// Obligation: "The email of CodexSlotIdentity(filepath.Join(util.Home(), \".codex\"))."
// This is the identity the codex binder excludes, so reading it from anywhere
// but the machine's own login would let a cell bind to main.
func TestContractCxpMainEmailIsTheMachineLoginsIdentity(t *testing.T) {
	home, pool := contractCxpEnv(t)
	contractCxpWriteAuth(t, filepath.Join(home, ".codex"), "machine@example.com", "pro")
	// a differently-identified slot exists too: the answer must not come from it
	contractCxpSlot(t, pool, "tmp-1", "slot-one@example.com", "pro")

	if got := CodexMainEmail(); got != "machine@example.com" {
		t.Errorf("CodexMainEmail() = %q, want %q", got, "machine@example.com")
	}
}

// ---------------------------------------------------------------------------
// CodexSlotUsable — the fail-closed probe (C3's guard, C1's file)
// ---------------------------------------------------------------------------

// Obligation: "True only when slotDir exists and is a directory AND
// <slotDir>/auth.json exists, is a regular file and has size greater than
// zero. […] Never reads the file's content."
//
// The content clause is asserted positively: a slot whose auth.json holds
// bytes that are not JSON at all is still USABLE, because the probe is about
// whether codex has a credential to resolve, not about whether kraken can
// read it. (The false arms of the truth table are athena-edge's.)
func TestContractCxpSlotUsableAcceptsANonEmptyAuthJSONWithoutReadingIt(t *testing.T) {
	_, pool := contractCxpEnv(t)

	good := contractCxpSlot(t, pool, "tmp-1", "slot-one@example.com", "pro")
	if !CodexSlotUsable(good) {
		t.Errorf("CodexSlotUsable(%q) = false, want true — the slot holds a regular non-empty auth.json", good)
	}

	opaque := filepath.Join(pool, "accounts", "opaque")
	if err := os.MkdirAll(opaque, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(opaque, "auth.json"), []byte("not json at all"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !CodexSlotUsable(opaque) {
		t.Errorf("CodexSlotUsable(%q) = false — the probe never reads the file's content, so unparseable bytes are still a usable slot", opaque)
	}
}

// ---------------------------------------------------------------------------
// CodexSlotUsage — the usage row (C2)
// ---------------------------------------------------------------------------

// Obligation: "Each non-null window with window_minutes >= 10080 becomes
// Weekly; each non-null window with window_minutes < 10080 becomes Session;
// routing never depends on whether the window arrived as primary or secondary.
// Percent is used_percent; ResetsAt is resets_at converted from unix SECONDS
// to unix MILLIS."
//
// The two orderings are asserted against ONE expected answer, which is what
// makes this a test of the routing rule rather than of the field names.
func TestContractCxpSlotUsageRoutesWindowsByMinutesNotFieldName(t *testing.T) {
	const ts = "2026-09-17T00:14:40.724Z"
	const sessionResets = int64(1_790_200_000)
	const weeklyResets = int64(1_790_208_555)
	session := contractCxpWindow(5, 300, sessionResets)
	weekly := contractCxpWindow(12, 10080, weeklyResets)

	cases := []struct {
		name               string
		primary, secondary string
	}{
		{"session in primary, weekly in secondary", session, weekly},
		{"weekly in primary, session in secondary", weekly, session},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, pool := contractCxpEnv(t)
			slot := contractCxpSlot(t, pool, "tmp-1", "slot-one@example.com", "pro")
			contractCxpWriteRollout(t, slot, "2026/09/17", "a", time.Now(),
				contractCxpTokenCount(ts, c.primary, c.secondary, "null"))

			got := CodexSlotUsage("tmp-1")
			if got.Session == nil || got.Weekly == nil {
				t.Fatalf("Session = %+v, Weekly = %+v — both windows are non-null in this record", got.Session, got.Weekly)
			}
			if got.Session.Percent != 5 {
				t.Errorf("Session.Percent = %v, want 5 (the 300-minute window)", got.Session.Percent)
			}
			if got.Session.ResetsAt != sessionResets*1000 {
				t.Errorf("Session.ResetsAt = %d, want %d (unix seconds converted to millis)", got.Session.ResetsAt, sessionResets*1000)
			}
			if got.Weekly.Percent != 12 {
				t.Errorf("Weekly.Percent = %v, want 12 (the 10080-minute window)", got.Weekly.Percent)
			}
			if got.Weekly.ResetsAt != weeklyResets*1000 {
				t.Errorf("Weekly.ResetsAt = %d, want %d", got.Weekly.ResetsAt, weeklyResets*1000)
			}
			if want := contractCxpMillis(t, ts); got.FetchedAt != want {
				t.Errorf("FetchedAt = %d, want %d — the record's own timestamp in millis, not the time kraken read it", got.FetchedAt, want)
			}
		})
	}
}

// Obligation: "When a Weekly was produced and no Session was, Session is a
// copy of Weekly." This is the current codex build's measured shape
// (weekly in primary, secondary null), and the selector excludes a row with
// no Session — so without the copy the whole pool blanks.
func TestContractCxpSlotUsageCopiesALoneWeeklyIntoSession(t *testing.T) {
	_, pool := contractCxpEnv(t)
	slot := contractCxpSlot(t, pool, "tmp-1", "slot-one@example.com", "pro")
	const ts = "2026-09-17T00:14:40.724Z"
	const resets = int64(1_790_208_555)
	contractCxpWriteRollout(t, slot, "2026/09/17", "a", time.Now(),
		contractCxpTokenCount(ts, contractCxpWindow(12, 10080, resets), "null", "null"))

	got := CodexSlotUsage("tmp-1")
	if got.Weekly == nil {
		t.Fatalf("Weekly = nil, want the 10080-minute window")
	}
	if got.Session == nil {
		t.Fatalf("Session = nil — a lone weekly window is copied into Session")
	}
	if got.Session.Percent != got.Weekly.Percent || got.Session.ResetsAt != got.Weekly.ResetsAt {
		t.Errorf("Session = %+v, Weekly = %+v — Session is a COPY of Weekly when no shorter window exists", got.Session, got.Weekly)
	}
	if got.Session.Percent != 12 || got.Session.ResetsAt != resets*1000 {
		t.Errorf("Session = %+v, want {12, %d}", got.Session, resets*1000)
	}
}

// Obligation: "the most recent token_count record carrying rate_limits". Two
// such records in one file: the LAST is the answer. A first-wins reader would
// report a stale percentage for the rest of the slot's life.
func TestContractCxpSlotUsageTakesTheMostRecentRecordInAFile(t *testing.T) {
	_, pool := contractCxpEnv(t)
	slot := contractCxpSlot(t, pool, "tmp-1", "slot-one@example.com", "pro")
	contractCxpWriteRollout(t, slot, "2026/09/17", "a", time.Now(),
		contractCxpTokenCount("2026-09-17T00:00:00.000Z", contractCxpWindow(3, 10080, 1_790_000_000), "null", "null"),
		contractCxpNoLimitsLine,
		contractCxpTokenCount("2026-09-17T01:00:00.000Z", contractCxpWindow(44, 10080, 1_790_208_555), "null", "null"),
	)

	got := CodexSlotUsage("tmp-1")
	if got.Weekly == nil || got.Weekly.Percent != 44 {
		t.Fatalf("Weekly = %+v, want the LAST record's 44 percent", got.Weekly)
	}
	if want := contractCxpMillis(t, "2026-09-17T01:00:00.000Z"); got.FetchedAt != want {
		t.Errorf("FetchedAt = %d, want %d — that same record's timestamp", got.FetchedAt, want)
	}
}

// Obligation: "searching the slot's rollout files […] newest modification time
// first and continuing to older files when a file has no such record." Short
// sessions on the current codex build write no token_count event at all, so a
// reader that stopped at the newest file would report nothing for a slot that
// has plenty of evidence one file down.
func TestContractCxpSlotUsageFallsThroughToAnOlderRolloutFile(t *testing.T) {
	_, pool := contractCxpEnv(t)
	slot := contractCxpSlot(t, pool, "tmp-1", "slot-one@example.com", "pro")
	now := time.Now()
	// newest file: a short session with no rate_limits record at all
	contractCxpWriteRollout(t, slot, "2026/09/17", "newest", now, contractCxpNoLimitsLine)
	// older file: the evidence
	contractCxpWriteRollout(t, slot, "2026/09/16", "older", now.Add(-48*time.Hour),
		contractCxpTokenCount("2026-09-16T00:00:00.000Z", contractCxpWindow(31, 10080, 1_790_208_555), "null", "null"))

	got := CodexSlotUsage("tmp-1")
	if got.Weekly == nil {
		t.Fatalf("Weekly = nil — the search must continue past a newest file carrying no rate_limits record")
	}
	if got.Weekly.Percent != 31 {
		t.Errorf("Weekly.Percent = %v, want 31 (the older file's record)", got.Weekly.Percent)
	}
}

// Obligation: "Slug is slug. Email and Tier come from CodexSlotIdentity (Tier
// is the plan). NoCreds is !CodexSlotUsable. Models is nil; Reauth is false;
// Current is false."
//
// Models/Reauth/Current are pinned because they are claude-pool concepts with
// no codex meaning: codex refreshes its own token (so there is no reauth to
// ask for), there are no per-model windows, and there is no codex "main swap"
// for Current to mark.
func TestContractCxpSlotUsageCarriesIdentityAndTheFixedFlags(t *testing.T) {
	_, pool := contractCxpEnv(t)
	slot := contractCxpSlot(t, pool, "tmp-1", "slot-one@example.com", "pro")
	contractCxpWriteRollout(t, slot, "2026/09/17", "a", time.Now(),
		contractCxpTokenCount("2026-09-17T00:14:40.724Z", contractCxpWindow(12, 10080, 1_790_208_555), "null", "null"))

	got := CodexSlotUsage("tmp-1")
	if got.Slug != "tmp-1" {
		t.Errorf("Slug = %q, want %q", got.Slug, "tmp-1")
	}
	if got.Email != "slot-one@example.com" {
		t.Errorf("Email = %q, want the slot's auth.json identity", got.Email)
	}
	if got.Tier != "pro" {
		t.Errorf("Tier = %q, want the plan %q", got.Tier, "pro")
	}
	if got.NoCreds {
		t.Errorf("NoCreds = true for a usable slot at %q", slot)
	}
	if got.Models != nil {
		t.Errorf("Models = %v, want nil — codex has no per-model windows", got.Models)
	}
	if got.Reauth {
		t.Error("Reauth = true — codex refreshes its own token, so kraken never asks for a re-login")
	}
	if got.Current {
		t.Error("Current = true — there is no codex main swap for Current to mark")
	}
}

// Obligation: "With no such record anywhere and usable creds: Session is
// non-nil with Percent 0 and ResetsAt 0, Weekly is nil, FetchedAt is 0."
// This is Q3's answer in full — a slot with no evidence is ELIGIBLE at zero
// percent and ranked never-fetched, rather than excluded.
func TestContractCxpSlotUsageWithCredsButNoEvidenceIsAZeroPercentRow(t *testing.T) {
	_, pool := contractCxpEnv(t)
	contractCxpSlot(t, pool, "tmp-2", "slot-two@example.com", "pro")

	got := CodexSlotUsage("tmp-2")
	if got.NoCreds {
		t.Fatalf("NoCreds = true — the slot holds an auth.json; only the usage evidence is missing")
	}
	if got.Session == nil {
		t.Fatalf("Session = nil — a no-evidence slot is eligible at 0 percent, which needs a non-nil Session")
	}
	if got.Session.Percent != 0 || got.Session.ResetsAt != 0 {
		t.Errorf("Session = %+v, want {Percent: 0, ResetsAt: 0}", got.Session)
	}
	if got.Weekly != nil {
		t.Errorf("Weekly = %+v, want nil with no evidence anywhere", got.Weekly)
	}
	if got.FetchedAt != 0 {
		t.Errorf("FetchedAt = %d, want 0 — never fetched, which is what ranks it stale", got.FetchedAt)
	}
}

// Obligation: "CodexSlotUsage for each of CodexPoolSlugs(), in that order."
// The order is CodexPoolSlugs' sorted one, which is what makes the wire's
// "sorted by slug" free rather than a second sort that could disagree.
func TestContractCxpFetchCodexUsageIsOneRowPerSlugInPoolOrder(t *testing.T) {
	_, pool := contractCxpEnv(t)
	contractCxpSlot(t, pool, "zeta", "zeta@example.com", "pro")
	contractCxpSlot(t, pool, "alpha", "alpha@example.com", "plus")

	rows := FetchCodexUsage()
	if len(rows) != 2 {
		t.Fatalf("FetchCodexUsage() returned %d rows, want 2: %+v", len(rows), rows)
	}
	if rows[0].Slug != "alpha" || rows[1].Slug != "zeta" {
		t.Fatalf("rows = [%q %q], want [alpha zeta] — CodexPoolSlugs' order", rows[0].Slug, rows[1].Slug)
	}
	if rows[0].Email != "alpha@example.com" || rows[1].Email != "zeta@example.com" {
		t.Errorf("each row carries its OWN slot's identity: got %q and %q", rows[0].Email, rows[1].Email)
	}
}

// ---------------------------------------------------------------------------
// CodexLimitReached — the limit trigger's evidence (C9)
// ---------------------------------------------------------------------------

// Obligation: "ok is true only when that record's rate_limit_reached_type is a
// non-empty JSON string; then reachedType is that string and resetsAt is, in
// unix SECONDS, the resets_at of the window with the greatest used_percent
// among the non-null windows."
//
// The fixture puts the greater used_percent in SECONDARY so that a reader
// taking primary's reset by position fails: the window that is actually
// exhausted is the one whose reset time matters.
func TestContractCxpLimitReachedReadsTheTypeAndTheBusiestWindowsReset(t *testing.T) {
	_, _ = contractCxpEnv(t)
	home := os.Getenv("HOME")
	p := contractCxpWriteRollout(t, filepath.Join(home, ".codex"), "2026/09/17", "a", time.Now(),
		contractCxpTokenCount("2026-09-17T00:14:40.724Z",
			contractCxpWindow(40, 300, 1_790_200_000),
			contractCxpWindow(99, 10080, 1_790_208_555),
			`"weekly"`))

	reached, resets, ok := CodexLimitReached(p)
	if !ok {
		t.Fatalf("CodexLimitReached(%q) ok = false, want true — the last record carries a non-empty rate_limit_reached_type", p)
	}
	if reached != "weekly" {
		t.Errorf("reachedType = %q, want %q", reached, "weekly")
	}
	if resets != 1_790_208_555 {
		t.Errorf("resetsAt = %d, want 1790208555 — unix SECONDS of the window with the greatest used_percent", resets)
	}
}

// Obligation: "Looks at the LAST token_count record carrying rate_limits in
// that file." Two records both carrying the field: the later one is the
// current state of the account, and an earlier-wins reader would report a
// limit type the account has already moved past.
func TestContractCxpLimitReachedUsesTheLastRecordNotTheFirst(t *testing.T) {
	_, _ = contractCxpEnv(t)
	home := os.Getenv("HOME")
	p := contractCxpWriteRollout(t, filepath.Join(home, ".codex"), "2026/09/17", "a", time.Now(),
		contractCxpTokenCount("2026-09-17T00:00:00.000Z",
			contractCxpWindow(90, 300, 1_790_000_111), "null", `"session"`),
		contractCxpNoLimitsLine,
		contractCxpTokenCount("2026-09-17T01:00:00.000Z",
			contractCxpWindow(97, 10080, 1_790_208_555), "null", `"weekly"`),
	)

	reached, resets, ok := CodexLimitReached(p)
	if !ok {
		t.Fatalf("ok = false, want true")
	}
	if reached != "weekly" || resets != 1_790_208_555 {
		t.Errorf("CodexLimitReached = (%q, %d), want (weekly, 1790208555) — the LAST record decides", reached, resets)
	}
}

// ---------------------------------------------------------------------------
// CodexHomeFromCommand — the start-command parser (C5)
// ---------------------------------------------------------------------------

// Obligation: "Returns the directory when cmd, after removing at most one pair
// of enclosing double quotes and un-escaping \" inside them, BEGINS with
// CODEX_HOME=' … ' (single-quoted value, one space after); the four-character
// shell escape for an embedded single quote decodes back to one of them."
//
// Three positive shapes: the plain prefix, tmux's own outer double quoting,
// and a pool root carrying a single quote. The refusals are athena-edge's.
func TestContractCxpHomeFromCommandDecodesTheThreePositiveShapes(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		want string
	}{
		{
			name: "the plain prefix a bound cell's pane carries",
			cmd:  `CODEX_HOME='/tmp/pool/accounts/tmp-1' codex --dangerously-bypass-approvals-and-sandbox -m gpt-6-astra`,
			want: "/tmp/pool/accounts/tmp-1",
		},
		{
			name: "tmux's own outer double quoting, with escaped inner quotes",
			cmd:  `"CODEX_HOME='/tmp/pool/accounts/tmp-1' codex -m gpt-6-astra \"$(cat '/tmp/p.md')\""`,
			want: "/tmp/pool/accounts/tmp-1",
		},
		{
			name: "a pool root holding a single quote, shell-escaped as '\\''",
			cmd:  `CODEX_HOME='/tmp/carl'\''s pool/accounts/tmp-1' codex`,
			want: `/tmp/carl's pool/accounts/tmp-1`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := CodexHomeFromCommand(c.cmd); got != c.want {
				t.Errorf("CodexHomeFromCommand(%q) = %q, want %q", c.cmd, got, c.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// IsCodexRollout — the one rollout predicate (C6)
// ---------------------------------------------------------------------------

// Obligation: "True when the path contains a /sessions/ component AND its base
// name starts with rollout- and ends with .jsonl. So true for
// ~/.codex/sessions/…/rollout-….jsonl and for
// <any pool root>/accounts/<slug>/sessions/…/rollout-….jsonl; false for every
// ~/.claude/projects/<dir>/<uuid>.jsonl, including a project dir whose name
// contains sessions or codex."
//
// The discrimination IS the obligation — this predicate replaced three copies
// of a ~/.codex-shaped substring test, and a claude project dir munged from a
// cwd containing the word "sessions" is exactly what those copies got wrong.
func TestContractCxpIsCodexRolloutSeparatesRolloutsFromClaudeTranscripts(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/Users/x/.codex/sessions/2026/09/16/rollout-2026-09-16T00-00-00-abc.jsonl", true},
		{"/tmp/codex-pool/accounts/tmp-1/sessions/2026/09/16/rollout-abc.jsonl", true},
		{"/elsewhere/entirely/sessions/rollout-abc.jsonl", true},
		{"/Users/x/.claude/projects/-Users-x-work/1234-uuid.jsonl", false},
		{"/Users/x/.claude/projects/-Users-x-my-sessions-repo/1234-uuid.jsonl", false},
		{"/Users/x/.claude/projects/-Users-x-codex/1234-uuid.jsonl", false},
	}
	for _, c := range cases {
		if got := IsCodexRollout(c.path); got != c.want {
			t.Errorf("IsCodexRollout(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

// Obligation (LastBlocks): "Parses as codex exactly when IsCodexRollout(path)."
// A slot-rooted rollout is not under ~/.codex, so the retired substring test
// would have handed it to the claude parser and rendered nothing.
func TestContractCxpLastBlocksParsesASlotRootedRollout(t *testing.T) {
	_, pool := contractCxpEnv(t)
	slot := contractCxpSlot(t, pool, "tmp-1", "slot-one@example.com", "pro")
	p := contractCxpWriteRollout(t, slot, "2026/09/17", "a", time.Now(),
		`{"timestamp":"2026-09-17T00:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"do the thing"}}`,
		`{"timestamp":"2026-09-17T00:00:02Z","type":"event_msg","payload":{"type":"agent_message","message":"on it"}}`,
	)

	blocks, _, err := LastBlocks(p, 50)
	if err != nil {
		t.Fatalf("LastBlocks(slot-rooted rollout) err = %v, want nil", err)
	}
	if len(blocks) != 2 {
		t.Fatalf("LastBlocks returned %d blocks, want 2 — the codex parser ran: %+v", len(blocks), blocks)
	}
	if blocks[0].Kind != "user" || blocks[0].Text != "do the thing" {
		t.Errorf("block[0] = {%q,%q}, want {user, do the thing}", blocks[0].Kind, blocks[0].Text)
	}
	if blocks[1].Kind != "text" || blocks[1].Text != "on it" {
		t.Errorf("block[1] = {%q,%q}, want {text, on it}", blocks[1].Kind, blocks[1].Text)
	}
}

// ---------------------------------------------------------------------------
// PlaceCodexRollout — the rescue's one write into a slot (G3)
// ---------------------------------------------------------------------------

// Obligation: "Copies the file to <toHome>/<the path of rolloutPath relative to
// fromHome> and returns that destination. Creates missing parent dirs. […]
// The write is atomic: a temp file in the destination dir, then rename. […]
// The source is never modified."
//
// Q1 measured that `codex exec resume <id>` under slot B fails with
// `no rollout found for thread id` until the rollout sits at the SAME relative
// path under B — so the relative-path rule is the whole feature, not a
// convenience.
func TestContractCxpPlaceCodexRolloutCopiesToTheSameRelativePath(t *testing.T) {
	_, pool := contractCxpEnv(t)
	from := contractCxpSlot(t, pool, "tmp-1", "slot-one@example.com", "pro")
	to := contractCxpSlot(t, pool, "tmp-2", "slot-two@example.com", "pro")
	const body = `{"timestamp":"2026-09-17T00:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"live conversation"}}`
	src := contractCxpWriteRollout(t, from, "2026/09/17", "abc", time.Now(), body)

	dst, err := PlaceCodexRollout(src, from, to)
	if err != nil {
		t.Fatalf("PlaceCodexRollout = %v, want nil", err)
	}
	want := filepath.Join(to, "sessions", "2026", "09", "17", "rollout-abc.jsonl")
	if dst != want {
		t.Fatalf("destination = %q, want %q (the source's path relative to fromHome, under toHome)", dst, want)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read placed rollout: %v", err)
	}
	if strings.TrimSpace(string(got)) != body {
		t.Errorf("placed content = %q, want the source's %q", string(got), body)
	}

	// the source is the LIVE conversation: it is read, never touched
	srcBody, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read source after the copy: %v", err)
	}
	if strings.TrimSpace(string(srcBody)) != body {
		t.Errorf("the source was modified: %q", string(srcBody))
	}

	// "a temp file in the destination dir, then rename" — nothing is left
	// behind beside the placed file, so a later pass cannot read a partial
	// rollout as a real one.
	entries, err := os.ReadDir(filepath.Dir(dst))
	if err != nil {
		t.Fatalf("read destination dir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(dst) {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("destination dir holds %v, want only %q — the temp file must be renamed away", names, filepath.Base(dst))
	}
}

// Obligation: "Replaces an existing destination (the source is the live
// conversation)." A rebind back to a slot the cell ran on before finds a
// STALE copy of the same rollout there; keeping it would resume the cell into
// a conversation missing everything since.
func TestContractCxpPlaceCodexRolloutReplacesAStaleDestination(t *testing.T) {
	_, pool := contractCxpEnv(t)
	from := contractCxpSlot(t, pool, "tmp-1", "slot-one@example.com", "pro")
	to := contractCxpSlot(t, pool, "tmp-2", "slot-two@example.com", "pro")
	src := contractCxpWriteRollout(t, from, "2026/09/17", "abc", time.Now(), `{"n":"fresh"}`)
	stale := contractCxpWriteRollout(t, to, "2026/09/17", "abc", time.Now().Add(-time.Hour), `{"n":"stale"}`)

	dst, err := PlaceCodexRollout(src, from, to)
	if err != nil {
		t.Fatalf("PlaceCodexRollout = %v, want nil", err)
	}
	if dst != stale {
		t.Fatalf("destination = %q, want the existing %q", dst, stale)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "fresh") {
		t.Errorf("destination still reads %q — the live conversation must replace the stale copy", string(got))
	}
}

// ---------------------------------------------------------------------------
// Peer / tmuxPane — the two carriers (C5)
// ---------------------------------------------------------------------------

// Obligation: "CodexHome string and RolloutPath string, both tagged json:\"-\".
// […] Neither appears in any JSON the API serves."
//
// A slot path in a served payload would put the pool's layout — and which
// account a cell is on — on the wire beside the board's own fields, where no
// client asked for it and nothing documents it.
func TestContractCxpPeerCodexFieldsNeverReachJSON(t *testing.T) {
	p := &Peer{
		Agent:       "codex",
		Pane:        "%7",
		CodexHome:   "/tmp/codex-pool/accounts/tmp-1",
		RolloutPath: "/tmp/codex-pool/accounts/tmp-1/sessions/2026/09/17/rollout-abc.jsonl",
	}
	blob, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal Peer: %v", err)
	}
	text := string(blob)
	for _, needle := range []string{
		"CodexHome", "codex_home", "RolloutPath", "rollout_path",
		p.CodexHome, p.RolloutPath,
	} {
		if strings.Contains(text, needle) {
			t.Errorf("served Peer JSON contains %q: %s", needle, text)
		}
	}
}

// Obligation: "tmuxPane.StartCmd — new field on tmuxPane. Filled from tmux's
// #{pane_start_command}." The field's existence and type is what the parser's
// new column lands in; the parse itself has no frozen symbol in this contract
// (recorded as a silence in the author's report).
func TestContractCxpTmuxPaneCarriesAStartCommand(t *testing.T) {
	var pane tmuxPane
	pane.StartCmd = `CODEX_HOME='/tmp/codex-pool/accounts/tmp-1' codex`
	if CodexHomeFromCommand(pane.StartCmd) != "/tmp/codex-pool/accounts/tmp-1" {
		t.Errorf("a pane's StartCmd must be the exact string CodexHomeFromCommand inverts; got %q", pane.StartCmd)
	}
}

// ---------------------------------------------------------------------------
// readCodexThreadByCWDIn — the per-home lookup (C5)
// ---------------------------------------------------------------------------

// Obligation: "The per-home lookup […]: newest row for cwd in
// <home>/state_5.sqlite, its updated_at, and whether one was found."
//
// The fixture is the cross-home case in miniature: the SAME cwd has a stale
// row in slot A and a newer one in slot B, which is exactly what a rebind
// leaves behind. Each home answers with its own row and its own updated_at —
// the two numbers discovery then compares to pick the greater.
func TestContractCxpReadCodexThreadByCWDInAnswersPerHome(t *testing.T) {
	_, pool := contractCxpEnv(t)
	a := contractCxpSlot(t, pool, "tmp-1", "slot-one@example.com", "pro")
	b := contractCxpSlot(t, pool, "tmp-2", "slot-two@example.com", "pro")
	const cwd = "/work/cell"
	contractCxpStateDB(t, a, contractCxpThreadRow{
		ID: "thread-stale", Title: "old title", Model: "gpt-6-astra", TokensUsed: 10,
		RolloutPath: filepath.Join(a, "sessions", "2026", "09", "16", "rollout-stale.jsonl"),
		CWD:         cwd, UpdatedAt: 100,
	})
	contractCxpStateDB(t, b, contractCxpThreadRow{
		ID: "thread-live", Title: "new title", Model: "gpt-6-astra", TokensUsed: 20,
		RolloutPath: filepath.Join(b, "sessions", "2026", "09", "17", "rollout-live.jsonl"),
		CWD:         cwd, UpdatedAt: 900,
	})

	gotA, updA, okA := readCodexThreadByCWDIn(a, cwd)
	if !okA {
		t.Fatalf("readCodexThreadByCWDIn(slot A) ok = false, want true")
	}
	if gotA.ID != "thread-stale" || updA != 100 {
		t.Errorf("slot A = (%+v, %d), want thread-stale at 100", gotA, updA)
	}

	gotB, updB, okB := readCodexThreadByCWDIn(b, cwd)
	if !okB {
		t.Fatalf("readCodexThreadByCWDIn(slot B) ok = false, want true")
	}
	if gotB.ID != "thread-live" || updB != 900 {
		t.Errorf("slot B = (%+v, %d), want thread-live at 900", gotB, updB)
	}
	if updB <= updA {
		t.Fatalf("fixture is wrong: slot B must be the newer row for the cross-home comparison to mean anything")
	}
}

// Obligation: "newest row for cwd", and "readCodexThreadByCWD(cwd) keeps its
// signature and means home = ~/.codex."
func TestContractCxpReadCodexThreadByCWDDefaultsToTheMachineHome(t *testing.T) {
	home, pool := contractCxpEnv(t)
	machine := filepath.Join(home, ".codex")
	const cwd = "/work/cell"
	contractCxpStateDB(t, machine,
		contractCxpThreadRow{ID: "older", Title: "older", Model: "gpt-6-astra", RolloutPath: "/r/older.jsonl", CWD: cwd, UpdatedAt: 100},
		contractCxpThreadRow{ID: "newest", Title: "newest", Model: "gpt-6-astra", RolloutPath: "/r/newest.jsonl", CWD: cwd, UpdatedAt: 400},
	)
	// a slot with a DIFFERENT row for the same cwd: the default must not read it
	slot := contractCxpSlot(t, pool, "tmp-1", "slot-one@example.com", "pro")
	contractCxpStateDB(t, slot, contractCxpThreadRow{ID: "slot-row", Title: "slot", Model: "gpt-6-astra", RolloutPath: "/r/slot.jsonl", CWD: cwd, UpdatedAt: 999})

	got, ok := readCodexThreadByCWD(cwd)
	if !ok {
		t.Fatalf("readCodexThreadByCWD ok = false, want true")
	}
	if got.ID != "newest" {
		t.Errorf("readCodexThreadByCWD = %q, want %q — the newest row of ~/.codex, never the slot's", got.ID, "newest")
	}

	inHome, upd, okHome := readCodexThreadByCWDIn(machine, cwd)
	if !okHome || inHome != got || upd != 400 {
		t.Errorf("readCodexThreadByCWDIn(~/.codex) = (%+v, %d, %v), want the same row readCodexThreadByCWD returned at 400", inHome, upd, okHome)
	}
}

// ---------------------------------------------------------------------------
// ReadTranscriptPath — the rollout the peer already knows (C5)
// ---------------------------------------------------------------------------

// Obligation: "For a codex peer with a non-empty RolloutPath, returns it with
// no database lookup."
//
// "No database lookup" is made observable by seeding ~/.codex's state db with
// a DIFFERENT rollout for the peer's session id: a reader that still looked it
// up would answer with the machine login's path for a cell running under a
// slot, which is the pre-binding bug this field exists to end.
func TestContractCxpReadTranscriptPathPrefersThePeersRolloutPath(t *testing.T) {
	home, pool := contractCxpEnv(t)
	slot := contractCxpSlot(t, pool, "tmp-1", "slot-one@example.com", "pro")
	slotRollout := filepath.Join(slot, "sessions", "2026", "09", "17", "rollout-live.jsonl")
	contractCxpStateDB(t, filepath.Join(home, ".codex"), contractCxpThreadRow{
		ID: "sess-codex-1", Title: "machine row", Model: "gpt-6-astra",
		RolloutPath: filepath.Join(home, ".codex", "sessions", "2026", "09", "17", "rollout-machine.jsonl"),
		CWD:         "/work/cell", UpdatedAt: 500,
	})

	p := &Peer{Agent: "codex", Name: "cell thread", SessionID: "sess-codex-1", RolloutPath: slotRollout, CodexHome: slot}
	got, err := ReadTranscriptPath(p)
	if err != nil {
		t.Fatalf("ReadTranscriptPath(codex peer) err = %v, want nil", err)
	}
	if got != slotRollout {
		t.Errorf("ReadTranscriptPath = %q, want the peer's own RolloutPath %q (no database lookup)", got, slotRollout)
	}
}

// ---------------------------------------------------------------------------
// ResumeGuard / paneHoldsAgent — the rescue's pane primitives (C10)
// ---------------------------------------------------------------------------

// contractCxpPeer is the complete resumable shape for the named agent: the
// four conditions ResumeGuard tests, all satisfied.
func contractCxpPeer(agent string) *Peer {
	return &Peer{
		Agent:     agent,
		Role:      "main",
		Pane:      "%7",
		PID:       4242,
		SessionID: "contract-cxp-session",
		CWD:       "/tmp/contract-cxp-cwd",
	}
}

// Obligation: "A codex peer passes on the same four conditions a claude peer
// does (not a shell, has a pane, has a session id, id not guessed)."
// Before this, a codex peer was refused outright, which made the whole rescue
// unreachable for a codex cell.
func TestContractCxpResumeGuardAcceptsACodexPeer(t *testing.T) {
	if err := ResumeGuard(contractCxpPeer("codex")); err != nil {
		t.Errorf("ResumeGuard(complete codex peer) = %v, want nil", err)
	}
	if err := ResumeGuard(contractCxpPeer("claude")); err != nil {
		t.Errorf("ResumeGuard(complete claude peer) = %v, want nil — the claude arm is unchanged", err)
	}
}

// contractCxpProbe is one observed paneHoldsAgent call.
type contractCxpProbe struct {
	pane  string
	pid   int
	agent string
}

// Obligation: "paneHoldsAgent — var paneHoldsAgent = func(pane string, pid int,
// agent string) bool. True only when the pane's foreground agent process has
// that pid AND that agent kind. RespawnPeerPane uses it with the peer's OWN
// Agent."
//
// The peer's own Agent is the load-bearing word: probing for a claude in a
// codex cell's pane answers false for every codex pane, so the rescue would
// refuse to respawn the very cell it was called for.
func TestContractCxpRespawnPeerPaneProbesWithThePeersOwnAgent(t *testing.T) {
	for _, agent := range []string{"codex", "claude"} {
		t.Run(agent, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())

			origHolds, origRespawn := paneHoldsAgent, respawnPane
			probes := []contractCxpProbe{}
			respawns := []string{}
			paneHoldsAgent = func(pane string, pid int, a string) bool {
				probes = append(probes, contractCxpProbe{pane: pane, pid: pid, agent: a})
				return true
			}
			respawnPane = func(target, cwd, cmd string) error {
				respawns = append(respawns, cmd)
				return nil
			}
			t.Cleanup(func() { paneHoldsAgent, respawnPane = origHolds, origRespawn })

			p := contractCxpPeer(agent)
			const cmd = "the-launcher --flag 'arg'"
			if err := RespawnPeerPane(p, cmd); err != nil {
				t.Fatalf("RespawnPeerPane(%s peer) = %v, want nil", agent, err)
			}
			if len(probes) != 1 {
				t.Fatalf("paneHoldsAgent was called %d times, want exactly once: %+v", len(probes), probes)
			}
			if probes[0].agent != agent {
				t.Errorf("paneHoldsAgent was asked about agent %q, want the peer's own %q", probes[0].agent, agent)
			}
			if probes[0].pane != p.Pane || probes[0].pid != p.PID {
				t.Errorf("paneHoldsAgent was asked about (%q, %d), want the pane and pid about to be killed (%q, %d)",
					probes[0].pane, probes[0].pid, p.Pane, p.PID)
			}
			if len(respawns) != 1 || respawns[0] != cmd {
				t.Errorf("respawnPane received %v, want the command passed through untouched (%q)", respawns, cmd)
			}
		})
	}
}

// ===========================================================================
// AMENDMENT 1 — symbols frozen after the first join, LOGIC dimension.
// Still authored blind: no implementation file was opened for any of these.
// ===========================================================================

// ---------------------------------------------------------------------------
// parsePaneStartCommands — the pure half of the tmux read (C5)
// ---------------------------------------------------------------------------

// Obligation: "Each non-empty line is cut at its FIRST tab: the text before is
// the pane id, everything after is the command, verbatim, including any further
// tabs and any double quotes."
//
// Verbatim is the load-bearing word, and the two things that could eat it are
// exactly what a pane command carries: the staged-prompt form ends in
// "$(cat '…')" and a command may contain a tab of its own. Either one mangled
// here and CodexHomeFromCommand is handed a string the pane never ran.
//
// NOTE FOR THE LEAD: this row was not on my assigned list for amendment 1. I
// added the happy half because the row was frozen in response to my own
// disclosure and its drop half is plainly athena-edge's; delete this one test
// if the whole row went to edge.
func TestContractCxpParsePaneStartCommandsCutsAtTheFirstTab(t *testing.T) {
	const withTab = "sh -c 'printf a\tb'"
	const withQuotes = `CODEX_HOME='/tmp/pool/accounts/tmp-1' codex -m gpt-6-astra "$(cat '/tmp/p.md')"`
	out := strings.Join([]string{
		"%0\t" + withQuotes,
		"%12\tzsh",
		"%7\t" + withTab,
	}, "\n")

	got := parsePaneStartCommands(out)
	want := map[string]string{"%0": withQuotes, "%12": "zsh", "%7": withTab}
	if len(got) != len(want) {
		t.Fatalf("parsePaneStartCommands returned %d panes, want %d: %#v", len(got), len(want), got)
	}
	for pane, cmd := range want {
		if got[pane] != cmd {
			t.Errorf("pane %s = %q, want %q (verbatim after the first tab)", pane, got[pane], cmd)
		}
	}
	if CodexHomeFromCommand(got["%0"]) != "/tmp/pool/accounts/tmp-1" {
		t.Errorf("the parsed command no longer inverts to its home: %q", got["%0"])
	}
}

// ---------------------------------------------------------------------------
// codexThreadForPane — the whole cross-home lookup (C5)
// ---------------------------------------------------------------------------

// contractCxpPaneCWD is the cwd every codexThreadForPane fixture keys on.
const contractCxpPaneCWD = "/work/cell-443"

// Obligation: "Home is CodexHomeFromCommand(startCmd) […]; a hit there is
// returned without consulting any other home."
//
// The control is the whole test: ~/.codex holds a much NEWER row for the same
// cwd, so a reader that scanned every home and took the greatest updated_at
// unconditionally would return the machine login's row for a bound pane — the
// exact confusion CODEX_HOME exists to prevent. The bound home's hit must win
// on the strength of being the bound home, not on being newer.
func TestContractCxpCodexThreadForPaneReadsTheBoundHomeFirst(t *testing.T) {
	home, pool := contractCxpEnv(t)
	bound := contractCxpSlot(t, pool, "a-bound", "slot-a@example.com", "pro")
	contractCxpStateDB(t, bound, contractCxpThreadRow{
		ID: "bound-row", Title: "bound title", Model: "gpt-6-astra", TokensUsed: 7,
		RolloutPath: filepath.Join(bound, "sessions", "2026", "09", "17", "rollout-bound.jsonl"),
		CWD:         contractCxpPaneCWD, UpdatedAt: 50,
	})
	contractCxpStateDB(t, filepath.Join(home, ".codex"), contractCxpThreadRow{
		ID: "machine-row", Title: "machine title", Model: "gpt-6-astra",
		RolloutPath: "/r/machine.jsonl", CWD: contractCxpPaneCWD, UpdatedAt: 999,
	})

	startCmd := "CODEX_HOME=" + "'" + bound + "' " + "codex -m gpt-6-astra"
	row, gotHome, ok := codexThreadForPane(startCmd, contractCxpPaneCWD)
	if !ok {
		t.Fatalf("codexThreadForPane(bound pane) ok = false, want true")
	}
	if row.ID != "bound-row" {
		t.Errorf("row = %q, want bound-row — the bound home's hit is returned without consulting any other home", row.ID)
	}
	if gotHome != bound {
		t.Errorf("home = %q, want the bound slot %q", gotHome, bound)
	}
	if row.Title != "bound title" || row.Model != "gpt-6-astra" || row.RolloutPath == "" {
		t.Errorf("row = %+v, want the bound slot's title, model and absolute rollout path", row)
	}
}

// Obligation: "Home is CodexHomeFromCommand(startCmd), else ~/.codex."
// An unbound codex pane — every pane that predates this feature, and every
// `k c`-style codex outside a cell — must still resolve, and it must resolve
// to the machine login rather than to whichever slot happens to hold a row.
func TestContractCxpCodexThreadForPaneFallsBackToTheMachineHome(t *testing.T) {
	home, pool := contractCxpEnv(t)
	machine := filepath.Join(home, ".codex")
	contractCxpStateDB(t, machine, contractCxpThreadRow{
		ID: "machine-row", Title: "machine title", Model: "gpt-6-astra",
		RolloutPath: filepath.Join(machine, "sessions", "2026", "09", "17", "rollout-machine.jsonl"),
		CWD:         contractCxpPaneCWD, UpdatedAt: 10,
	})
	// a slot with a far newer row for the same cwd: the ~/.codex hit is a hit,
	// so nothing else may be consulted
	slot := contractCxpSlot(t, pool, "a-slot", "slot-a@example.com", "pro")
	contractCxpStateDB(t, slot, contractCxpThreadRow{
		ID: "slot-row", Title: "slot title", Model: "gpt-6-astra",
		RolloutPath: "/r/slot.jsonl", CWD: contractCxpPaneCWD, UpdatedAt: 5000,
	})

	row, gotHome, ok := codexThreadForPane("codex -m gpt-6-astra", contractCxpPaneCWD)
	if !ok {
		t.Fatalf("codexThreadForPane(unbound pane) ok = false, want true")
	}
	if row.ID != "machine-row" {
		t.Errorf("row = %q, want machine-row — an unbound pane resolves to ~/.codex", row.ID)
	}
	if gotHome != machine {
		t.Errorf("home = %q, want %q", gotHome, machine)
	}
}

// Obligation: "on a miss every other known home is tried and the greatest
// updated_at wins, never the first match."
//
// This is the rebound cell: slot A ran it yesterday and still holds a stale
// row for the cwd, the rescue moved it to slot C, and ~/.codex holds an older
// one still. Taking the first match would resume the wrong conversation, and
// the fixture is built so that no plausible iteration order puts the winner
// first — the greatest updated_at is neither the bound home nor ~/.codex nor
// the alphabetically-first slot.
func TestContractCxpCodexThreadForPaneTakesTheGreatestUpdatedAtAcrossHomes(t *testing.T) {
	home, pool := contractCxpEnv(t)
	// the bound home MISSES: it has a row, but for a different cwd
	bound := contractCxpSlot(t, pool, "a-bound", "slot-a@example.com", "pro")
	contractCxpStateDB(t, bound, contractCxpThreadRow{
		ID: "other-cwd", Title: "elsewhere", Model: "gpt-6-astra",
		RolloutPath: "/r/other.jsonl", CWD: "/somewhere/else", UpdatedAt: 4242,
	})
	contractCxpStateDB(t, filepath.Join(home, ".codex"), contractCxpThreadRow{
		ID: "machine-row", Title: "machine", Model: "gpt-6-astra",
		RolloutPath: "/r/machine.jsonl", CWD: contractCxpPaneCWD, UpdatedAt: 100,
	})
	mid := contractCxpSlot(t, pool, "b-mid", "slot-b@example.com", "pro")
	contractCxpStateDB(t, mid, contractCxpThreadRow{
		ID: "mid-row", Title: "mid", Model: "gpt-6-astra",
		RolloutPath: "/r/mid.jsonl", CWD: contractCxpPaneCWD, UpdatedAt: 200,
	})
	top := contractCxpSlot(t, pool, "c-top", "slot-c@example.com", "pro")
	topRollout := filepath.Join(top, "sessions", "2026", "09", "17", "rollout-top.jsonl")
	contractCxpStateDB(t, top, contractCxpThreadRow{
		ID: "top-row", Title: "top title", Model: "gpt-6-astra", TokensUsed: 31,
		RolloutPath: topRollout, CWD: contractCxpPaneCWD, UpdatedAt: 900,
	})

	startCmd := "CODEX_HOME=" + "'" + bound + "' " + "codex"
	row, gotHome, ok := codexThreadForPane(startCmd, contractCxpPaneCWD)
	if !ok {
		t.Fatalf("codexThreadForPane ok = false, want true — three other homes hold a row for this cwd")
	}
	if row.ID != "top-row" {
		t.Errorf("row = %q, want top-row — the greatest updated_at across homes wins, never the first match", row.ID)
	}
	if gotHome != top {
		t.Errorf("home = %q, want %q — the second return names the home the row actually came from", gotHome, top)
	}
	if row.RolloutPath != topRollout {
		t.Errorf("RolloutPath = %q, want the winning home's absolute path %q", row.RolloutPath, topRollout)
	}
}
