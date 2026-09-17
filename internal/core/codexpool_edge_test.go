package core

// codexpool_edge_test.go — the EDGE dimension of slice CORE in
// artifacts/50-contract.md (the codex account pool): boundaries, error paths,
// degradations and refusals of every row in that slice, plus the global
// obligations G2 (nothing but email+plan leaves an identity read), G4 (foreign
// state degrades to the zero value, never an error, never a panic) and G5
// (every test owns its HOME and its CODEX_POOL_DIR).
//
// Authored BLIND from the frozen contract. internal/core/codexpool.go was
// never read by any route — Read, cat, grep, sed, git show/diff — and neither
// were the changed regions of discover.go, read.go or resume.go. What WAS read:
// the contract and 50-components.md/52-testing-plan.md, the pre-existing test
// files in this package (for fixture idioms), and usagepool.go's pinned wire
// types, which the contract cites as unchanged.
//
// The sibling blind author (athena-logic) owns the stated invariants and the
// happy paths — which slug a healthy pool yields, that a weekly window maps to
// Weekly. What lives here is the other half: the missing file, the zero-byte
// auth.json, the window exactly on the boundary, the quote inside the quoted
// value, the last record that UNSETS what an earlier one had set.
//
// Every package-level identifier is prefixed `edgeCxp` and every test is
// `TestEdgeCxp…`: this package's declaration space is shared with the other
// blind author and with the implementation.

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// ---------------------------------------------------------------------------
// fixtures
// ---------------------------------------------------------------------------

// edgeCxpSandbox isolates HOME and points CODEX_POOL_DIR at a directory under
// it (G5). It returns the pool root; the `accounts/` dir is NOT created, so a
// test that wants the missing-pool degradation gets it for free.
func edgeCxpSandbox(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	pool := filepath.Join(home, "codex-pool")
	t.Setenv("CODEX_POOL_DIR", pool)
	return pool
}

// edgeCxpAccounts creates <pool>/accounts and returns it.
func edgeCxpAccounts(t *testing.T, pool string) string {
	t.Helper()
	dir := filepath.Join(pool, "accounts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir accounts: %v", err)
	}
	return dir
}

// edgeCxpJWT builds an unsigned three-segment token whose MIDDLE segment is
// `payload` base64url-encoded WITHOUT padding. Header and signature are junk
// on purpose: the contract says only the second segment is read and no
// signature is checked, so a fixture that made them valid would be testing
// something the contract does not promise.
func edgeCxpJWT(payload string) string {
	return "HEADER." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".SIGNATURE"
}

// edgeCxpJWTPadded is the same token with a PADDED middle segment — the other
// half of "base64url-decodes … with or without padding".
func edgeCxpJWTPadded(payload string) string {
	return "HEADER." + base64.URLEncoding.EncodeToString([]byte(payload)) + ".SIGNATURE"
}

// edgeCxpAuthPayload renders a realistic id_token payload. `extra` is spliced
// in verbatim so a case can add or collide claims. Every value here is
// invented — no real token, no real address (G2, and the standing rule that a
// fixture never carries a real credential).
func edgeCxpAuthPayload(email, plan string) string {
	return fmt.Sprintf(
		`{"email":%q,"exp":1000000000,"sub":"user-fixture","https://api.openai.com/auth":{"chatgpt_plan_type":%q,"chatgpt_account_id":"acct-fixture"}}`,
		email, plan)
}

// edgeCxpWriteAuth writes <dir>/auth.json carrying `token` as tokens.id_token,
// creating the slot dir.
func edgeCxpWriteAuth(t *testing.T, dir, token string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	body := fmt.Sprintf(`{"OPENAI_API_KEY":null,"tokens":{"id_token":%q,"access_token":"fixture-access","refresh_token":"fixture-refresh"},"last_refresh":"2026-09-17T00:00:00Z"}`, token)
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(body), 0o600); err != nil {
		t.Fatalf("write auth.json: %v", err)
	}
}

// edgeCxpSlot creates a usable slot under <pool>/accounts/<slug> whose identity
// is (email, plan).
func edgeCxpSlot(t *testing.T, pool, slug, email, plan string) string {
	t.Helper()
	dir := filepath.Join(edgeCxpAccounts(t, pool), slug)
	edgeCxpWriteAuth(t, dir, edgeCxpJWT(edgeCxpAuthPayload(email, plan)))
	return dir
}

// edgeCxpWindow renders one rate-limit window as raw JSON, or the literal
// `null`, matching the measured record shape in the contract's fixture note.
func edgeCxpWindow(usedPercent float64, windowMinutes int, resetsAt int64) string {
	return fmt.Sprintf(`{"used_percent":%v,"window_minutes":%d,"resets_at":%d}`, usedPercent, windowMinutes, resetsAt)
}

// edgeCxpRecord renders one rollout line carrying rate_limits. primary,
// secondary and reached are spliced in as RAW JSON so a case can pass `null`,
// a number, or a string.
func edgeCxpRecord(ts, primary, secondary, reached string) string {
	return fmt.Sprintf(
		`{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","rate_limits":{"primary":%s,"secondary":%s,"rate_limit_reached_type":%s}}}`,
		ts, primary, secondary, reached)
}

// edgeCxpPlainRecord is a rollout line that is NOT a rate-limits record: a
// token_count carrying no rate_limits at all, which is exactly what a short
// session on the current codex build writes.
func edgeCxpPlainRecord(ts string) string {
	return fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":10}}}}`, ts)
}

// edgeCxpWriteRollout writes a rollout at <home>/sessions/<rel> with the given
// lines and stamps its modification time, since the search order is "newest
// modification time first".
func edgeCxpWriteRollout(t *testing.T, home, rel string, mtime time.Time, lines ...string) string {
	t.Helper()
	p := filepath.Join(home, "sessions", rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir for rollout %s: %v", rel, err)
	}
	body := ""
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write rollout %s: %v", rel, err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatalf("chtimes %s: %v", rel, err)
	}
	return p
}

// edgeCxpMillis converts an RFC 3339 stamp to unix millis, which is what the
// contract says FetchedAt carries.
func edgeCxpMillis(t *testing.T, ts string) int64 {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		t.Fatalf("fixture timestamp %q is not RFC 3339: %v", ts, err)
	}
	return parsed.UnixMilli()
}

// ---------------------------------------------------------------------------
// CodexPoolDir / CodexSlotDir
// ---------------------------------------------------------------------------

// The override is read per call, not captured once at init: a pool root frozen
// into a package variable would make the live gate's `CODEX_POOL_DIR=~/.codex-pool`
// under an isolated HOME (52-testing-plan.md) resolve to the isolated home's
// default instead, which is the one configuration the gate exists to exercise.
// Unset and EMPTY both fall back; the contract's condition is "non-empty".
func TestEdgeCxpPoolDirFallsBackOnlyWhenTheOverrideIsEmpty(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	t.Run("unset falls back to <home>/.codex-pool", func(t *testing.T) {
		t.Setenv("CODEX_POOL_DIR", "")
		if err := os.Unsetenv("CODEX_POOL_DIR"); err != nil {
			t.Fatalf("unsetenv: %v", err)
		}
		if got, want := CodexPoolDir(), filepath.Join(home, ".codex-pool"); got != want {
			t.Errorf("CodexPoolDir() = %q, want %q", got, want)
		}
	})

	t.Run("empty falls back too", func(t *testing.T) {
		t.Setenv("CODEX_POOL_DIR", "")
		if got, want := CodexPoolDir(), filepath.Join(home, ".codex-pool"); got != want {
			t.Errorf("CodexPoolDir() = %q, want %q", got, want)
		}
	})

	t.Run("non-empty wins verbatim", func(t *testing.T) {
		t.Setenv("CODEX_POOL_DIR", "/elsewhere/pool")
		if got := CodexPoolDir(); got != "/elsewhere/pool" {
			t.Errorf("CodexPoolDir() = %q, want /elsewhere/pool — the override is used as given", got)
		}
	})

	t.Run("a relative override is still used as given", func(t *testing.T) {
		// Not cleaned, not made absolute, not joined with home: the contract's
		// only condition is non-emptiness, and a helpfully-absolutized value
		// would point the cell at a directory the probe never checked.
		t.Setenv("CODEX_POOL_DIR", "relative/pool")
		if got := CodexPoolDir(); got != "relative/pool" {
			t.Errorf("CodexPoolDir() = %q, want %q verbatim", got, "relative/pool")
		}
	})
}

// CodexSlotDir is "exactly filepath.Join(CodexPoolDir(), \"accounts\", slug)",
// and R3's rule from the claude pool applies verbatim here: the string the
// probe stats and the string CODEX_HOME is set to must be ONE derivation. A
// trailing slash, a second Clean, or a home-relative variant would point codex
// at a path the usable-probe never answered about.
//
// The collision fixture is a slug literally named `accounts`: it must nest
// under the accounts dir rather than collapse onto it.
func TestEdgeCxpSlotDirIsOneJoinWithNoDecoration(t *testing.T) {
	pool := edgeCxpSandbox(t)

	for _, c := range []struct{ slug, want string }{
		{"tmp-1", filepath.Join(pool, "accounts", "tmp-1")},
		{"accounts", filepath.Join(pool, "accounts", "accounts")},
		{"a.b-c_d", filepath.Join(pool, "accounts", "a.b-c_d")},
	} {
		if got := CodexSlotDir(c.slug); got != c.want {
			t.Errorf("CodexSlotDir(%q) = %q, want %q", c.slug, got, c.want)
		}
	}

	if got := CodexSlotDir("tmp-1"); strings.HasSuffix(got, "/") {
		t.Errorf("CodexSlotDir = %q — a trailing slash makes the exported home differ from the probed dir", got)
	}

	// Changing only the slug must change only the slug.
	a, b := CodexSlotDir("slot-a"), CodexSlotDir("slot-b")
	if strings.ReplaceAll(a, "slot-a", "slot-b") != b {
		t.Errorf("changing the slug changed more than the slug:\n  %q\n  %q", a, b)
	}
}

// The empty slug is the shape a job carries before it is ever bound, and it
// must not silently resolve to the accounts directory ITSELF dressed as a slot
// — filepath.Join drops an empty element, so this is what "exactly
// filepath.Join(…)" produces and the callers above it are what must refuse it.
func TestEdgeCxpSlotDirOfTheEmptySlugIsTheAccountsDir(t *testing.T) {
	pool := edgeCxpSandbox(t)
	if got, want := CodexSlotDir(""), filepath.Join(pool, "accounts"); got != want {
		t.Errorf("CodexSlotDir(\"\") = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// CodexPoolSlugs
// ---------------------------------------------------------------------------

// Every degradation of the pool root is an EMPTY pool, never an error and
// never a panic (G4). The three shapes are all reachable: a pool root that has
// never been created, a root with no accounts/ dir (a fresh `cxp` install), and
// an accounts PATH that is a regular file (a half-finished hand edit).
func TestEdgeCxpPoolSlugsDegradesToEmpty(t *testing.T) {
	t.Run("no pool root at all", func(t *testing.T) {
		edgeCxpSandbox(t)
		if got := CodexPoolSlugs(); len(got) != 0 {
			t.Errorf("CodexPoolSlugs() = %v, want no slugs", got)
		}
	})

	t.Run("pool root without accounts/", func(t *testing.T) {
		pool := edgeCxpSandbox(t)
		if err := os.MkdirAll(pool, 0o755); err != nil {
			t.Fatal(err)
		}
		if got := CodexPoolSlugs(); len(got) != 0 {
			t.Errorf("CodexPoolSlugs() = %v, want no slugs", got)
		}
	})

	t.Run("accounts is a regular file", func(t *testing.T) {
		pool := edgeCxpSandbox(t)
		if err := os.MkdirAll(pool, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(pool, "accounts"), []byte("not a dir"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := CodexPoolSlugs(); len(got) != 0 {
			t.Errorf("CodexPoolSlugs() = %v, want no slugs", got)
		}
	})
}

// What is NOT a slot: a plain file, a dot-name, and the name `main`. Each of
// the three has a distinct cost if it leaks through — a file has no auth.json
// and would be skipped later anyway, a dot-name is codex's own bookkeeping, and
// `main` is the machine login, which binding a cell to would put it straight
// back on the shared lane while the board claimed otherwise.
//
// The exclusions are exactly as written: the whole name `main` and not a name
// merely CONTAINING it, a LEADING dot and not a dot anywhere.
//
// Amendment 1 records why `Main` is not in this table: a case variant cannot
// coexist with `main` as a sibling directory on a case-insensitive filesystem
// (the second MkdirAll lands on the first one's directory), so the literal-case
// behaviour of this skip is unpinnable here and is left unpinned.
func TestEdgeCxpPoolSlugsSkipsFilesDotNamesAndMainOnly(t *testing.T) {
	pool := edgeCxpSandbox(t)
	acc := edgeCxpAccounts(t, pool)

	for _, d := range []string{"tmp-1", "main", "main2", "mainly", "remain", ".hidden", "..odd", "a.b"} {
		if err := os.MkdirAll(filepath.Join(acc, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"snapshots.json", "README", "tmp-9"} {
		if err := os.WriteFile(filepath.Join(acc, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got := CodexPoolSlugs()
	want := []string{"a.b", "main2", "mainly", "remain", "tmp-1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CodexPoolSlugs() = %v, want %v\n"+
			"  (skips exactly: plain files, names starting with \".\", and the WHOLE name \"main\")", got, want)
	}
}

// Siblings of accounts/ are never consulted. `removed/` and `snapshots.json`
// live BESIDE accounts/ in carl's pool, and a reader that walked the pool root
// instead of accounts/ would answer with them and bind a cell to a slot that
// was deliberately retired.
func TestEdgeCxpPoolSlugsNeverReadsSiblingsOfAccounts(t *testing.T) {
	pool := edgeCxpSandbox(t)
	acc := edgeCxpAccounts(t, pool)

	if err := os.MkdirAll(filepath.Join(pool, "removed", "old-slot"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pool, "snapshots.json"), []byte(`{"accounts":[{"slug":"ghost"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(acc, "tmp-1"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := CodexPoolSlugs()
	if !reflect.DeepEqual(got, []string{"tmp-1"}) {
		t.Errorf("CodexPoolSlugs() = %v, want [tmp-1] — removed/ and snapshots.json sit BESIDE accounts/ and are never consulted", got)
	}
}

// The skip list is EXHAUSTIVE, which decides the one case the two source docs
// read differently: `50-components.md` says "`removed/` … never [a slot]" while
// the contract's `CodexPoolSlugs` row excludes only plain files, leading dots
// and the literal name `main`, and separately says `removed/` "beside
// `accounts/`" is never consulted. Per the lead's ruling that 50-contract.md is
// the requirement wherever the two disagree, a DIRECTORY named `removed` under
// `accounts/` is an ordinary slot — the pool's retired-slot bin is the sibling
// `<pool>/removed/`, which the test above covers, and a slug can legitimately
// be called anything `cxp` accepted.
//
// Flagged rather than assumed: if the intent was to exclude the name at both
// levels, this is the test to amend and the contract row is what has to move.
func TestEdgeCxpPoolSlugsTreatsRemovedUnderAccountsAsAnOrdinarySlot(t *testing.T) {
	pool := edgeCxpSandbox(t)
	acc := edgeCxpAccounts(t, pool)

	for _, d := range []string{"removed", "snapshots.json", "tmp-1"} {
		if err := os.MkdirAll(filepath.Join(acc, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	got := CodexPoolSlugs()
	want := []string{"removed", "snapshots.json", "tmp-1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CodexPoolSlugs() = %v, want %v\n"+
			"  (the skip list is exhaustive: plain files, leading dots, and the literal name \"main\";\n"+
			"   `removed/` and `snapshots.json` are excluded as SIBLINGS of accounts/, not as names)", got, want)
	}
}

// "sorted ascending" over strings is byte order, which is the only ordering a
// caller can rely on across pools: the slugs are slugified emails, so mixed
// case is reachable, and a case-folding sort would put a slot in a different
// rank on two machines with the same pool.
//
// The fixture is mixed-case but NOT case-colliding (Amendment 1): `Beta` and
// `alpha` are different names, so both exist on a case-insensitive filesystem,
// and they still separate the two orderings — byte order puts `Beta` first
// (0x42 < 0x61) while a case-folding sort puts `alpha` first. `tmp-10` before
// `tmp-2` is the same claim from the digit side.
func TestEdgeCxpPoolSlugsSortAscendingIsByteOrder(t *testing.T) {
	pool := edgeCxpSandbox(t)
	acc := edgeCxpAccounts(t, pool)

	made := []string{"zeta", "Beta", "alpha", "tmp-10", "tmp-2"}
	for _, d := range made {
		if err := os.MkdirAll(filepath.Join(acc, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	want := append([]string(nil), made...)
	sort.Strings(want)

	got := CodexPoolSlugs()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CodexPoolSlugs() = %v, want %v (ascending byte order)", got, want)
	}
}

// ---------------------------------------------------------------------------
// CodexSlotIdentity
// ---------------------------------------------------------------------------

// Every broken input on the path from a directory to two claims yields the
// empty pair, never an error and never a panic (G4). Each row is a state that
// really occurs: a slot dir that codex has not logged into yet, an auth.json
// truncated by a crash, an id_token that is a session cookie rather than a JWT.
func TestEdgeCxpSlotIdentityDegradesToTheEmptyPair(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	type mk func(t *testing.T, dir string)
	writeRaw := func(body string) mk {
		return func(t *testing.T, dir string) {
			t.Helper()
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}

	cases := []struct {
		name string
		make mk
	}{
		{"the home dir does not exist", func(t *testing.T, dir string) {}},
		{"home exists but holds no auth.json", func(t *testing.T, dir string) {
			t.Helper()
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		{"auth.json is a directory", func(t *testing.T, dir string) {
			t.Helper()
			if err := os.MkdirAll(filepath.Join(dir, "auth.json"), 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		{"auth.json is empty", writeRaw("")},
		{"auth.json is not JSON", writeRaw("this is not json")},
		{"auth.json is truncated JSON", writeRaw(`{"tokens":{"id_token":`)},
		{"auth.json is a JSON array", writeRaw(`["tokens"]`)},
		{"no tokens object", writeRaw(`{"OPENAI_API_KEY":null}`)},
		{"tokens is not an object", writeRaw(`{"tokens":"opaque"}`)},
		{"no id_token", writeRaw(`{"tokens":{"access_token":"fixture-access"}}`)},
		{"id_token is empty", writeRaw(`{"tokens":{"id_token":""}}`)},
		{"id_token is not a string", writeRaw(`{"tokens":{"id_token":42}}`)},
		{"id_token has no second segment", writeRaw(`{"tokens":{"id_token":"onesegmentonly"}}`)},
		{"second segment is not base64url", writeRaw(`{"tokens":{"id_token":"H.not base64!!.S"}}`)},
		{"second segment decodes to non-JSON", func(t *testing.T, dir string) {
			t.Helper()
			writeRaw(fmt.Sprintf(`{"tokens":{"id_token":%q}}`, edgeCxpJWT("not json at all")))(t, dir)
		}},
		{"payload is a JSON array", func(t *testing.T, dir string) {
			t.Helper()
			writeRaw(fmt.Sprintf(`{"tokens":{"id_token":%q}}`, edgeCxpJWT(`["email"]`)))(t, dir)
		}},
		{"payload is an empty object", func(t *testing.T, dir string) {
			t.Helper()
			writeRaw(fmt.Sprintf(`{"tokens":{"id_token":%q}}`, edgeCxpJWT(`{}`)))(t, dir)
		}},
	}

	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := filepath.Join(home, fmt.Sprintf("slot-%d", i))
			c.make(t, dir)
			email, plan := CodexSlotIdentity(dir)
			if email != "" || plan != "" {
				t.Errorf("CodexSlotIdentity = (%q, %q), want the empty pair", email, plan)
			}
		})
	}
}

// The empty home string is its own row: nothing on disk answers to it, and a
// reader that joined it into a relative "auth.json" would read whatever sits in
// the process' working directory.
func TestEdgeCxpSlotIdentityOfTheEmptyHomeIsEmpty(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if email, plan := CodexSlotIdentity(""); email != "" || plan != "" {
		t.Errorf("CodexSlotIdentity(\"\") = (%q, %q), want the empty pair", email, plan)
	}
}

// Padding is optional on the middle segment. Real codex tokens arrive
// unpadded, but the decoder must accept both — an encoder that only handled one
// would blank a whole slot's identity on a token whose payload length happens
// to land on a 3-byte boundary, which is data-dependent and so intermittent.
func TestEdgeCxpSlotIdentityDecodesPaddedAndUnpaddedAlike(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// A payload whose byte length is NOT a multiple of 3, so the padded form
	// really does carry '=' characters and the two spellings differ.
	payload := edgeCxpAuthPayload("slot-one@example.test", "plus")
	if base64.URLEncoding.EncodeToString([]byte(payload)) == base64.RawURLEncoding.EncodeToString([]byte(payload)) {
		t.Fatalf("fixture is wrong: the payload length (%d) leaves the padded and unpadded encodings identical", len(payload))
	}

	for name, token := range map[string]string{
		"unpadded": edgeCxpJWT(payload),
		"padded":   edgeCxpJWTPadded(payload),
	} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(home, name)
			edgeCxpWriteAuth(t, dir, token)
			email, plan := CodexSlotIdentity(dir)
			if email != "slot-one@example.test" || plan != "plus" {
				t.Errorf("CodexSlotIdentity = (%q, %q), want (slot-one@example.test, plus)", email, plan)
			}
		})
	}
}

// No expiry check. Both real slots carry an EXPIRED id_token while the slot
// works — it is an identity artifact, not a credential — so a decoder that
// honoured `exp` would report the whole pool as anonymous and the board would
// show two nameless slots.
func TestEdgeCxpSlotIdentityIgnoresAnExpiredToken(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, "expired")

	// exp long in the past, and an explicit nbf in the future for good measure:
	// neither is consulted.
	payload := `{"email":"expired@example.test","exp":1,"nbf":99999999999,` +
		`"https://api.openai.com/auth":{"chatgpt_plan_type":"pro"}}`
	edgeCxpWriteAuth(t, dir, edgeCxpJWT(payload))

	email, plan := CodexSlotIdentity(dir)
	if email != "expired@example.test" || plan != "pro" {
		t.Errorf("CodexSlotIdentity = (%q, %q), want (expired@example.test, pro) — no expiry check", email, plan)
	}
}

// No signature check either: the third segment is junk in every fixture here,
// and a real slot's signature cannot be verified without OpenAI's key anyway.
func TestEdgeCxpSlotIdentityIgnoresAnUnverifiableSignature(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, "unsigned")

	token := "HEADER." + base64.RawURLEncoding.EncodeToString(
		[]byte(edgeCxpAuthPayload("unsigned@example.test", "plus"))) + "."
	edgeCxpWriteAuth(t, dir, token)

	email, plan := CodexSlotIdentity(dir)
	if email != "unsigned@example.test" || plan != "plus" {
		t.Errorf("CodexSlotIdentity = (%q, %q), want (unsigned@example.test, plus) — an empty signature segment is not a refusal", email, plan)
	}
}

// The plan lives INSIDE the object under the `https://api.openai.com/auth`
// claim, and the collision fixture is a payload carrying a decoy
// `chatgpt_plan_type` at the ROOT with a different value. A reader that
// searched the payload flat would return the decoy, and the board's tier chip
// would name a plan the account does not have.
//
// The symmetric decoy is an `email` INSIDE the auth object: the email is the
// root claim, never the nested one.
func TestEdgeCxpSlotIdentityReadsEachClaimFromItsOwnLevel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, "collide")

	payload := `{"email":"root@example.test","chatgpt_plan_type":"DECOY-ROOT-PLAN",` +
		`"https://api.openai.com/auth":{"email":"decoy-nested@example.test","chatgpt_plan_type":"pro"}}`
	edgeCxpWriteAuth(t, dir, edgeCxpJWT(payload))

	email, plan := CodexSlotIdentity(dir)
	if email != "root@example.test" {
		t.Errorf("email = %q, want root@example.test — the email is the PAYLOAD's claim, not the one inside the auth object", email)
	}
	if plan != "pro" {
		t.Errorf("plan = %q, want pro — the plan is chatgpt_plan_type INSIDE the auth claim's object, not the root decoy", plan)
	}
}

// The two claims are INDEPENDENT — Amendment 1, settling the silence the round-1
// version of this test disclosed: a payload carrying `email` but no plan returns
// (email, ""), and one carrying a plan but no `email` returns ("", plan). The
// blanket empty pair applies to a missing or malformed file, token or payload,
// not to a payload that is merely partial.
//
// The independence is what keeps a partly-provisioned slot legible: a token
// predating the plan claim still says WHO the slot belongs to, and collapsing
// to the empty pair would render it on the board as an anonymous slot that
// looks unprovisioned.
func TestEdgeCxpSlotIdentityClaimsAreIndependent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cases := []struct {
		name        string
		payload     string
		email, plan string
	}{
		{"auth claim absent", `{"email":"a@example.test"}`, "a@example.test", ""},
		{"auth claim is a string", `{"email":"a@example.test","https://api.openai.com/auth":"opaque"}`, "a@example.test", ""},
		{"auth object has no plan", `{"email":"a@example.test","https://api.openai.com/auth":{"chatgpt_account_id":"acct"}}`, "a@example.test", ""},
		{"a plan but no email claim", `{"https://api.openai.com/auth":{"chatgpt_plan_type":"pro"}}`, "", "pro"},
		{"an empty email claim beside a plan", `{"email":"","https://api.openai.com/auth":{"chatgpt_plan_type":"plus"}}`, "", "plus"},
	}

	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := filepath.Join(home, fmt.Sprintf("claim-%d", i))
			edgeCxpWriteAuth(t, dir, edgeCxpJWT(c.payload))
			email, plan := CodexSlotIdentity(dir)
			if email != c.email || plan != c.plan {
				t.Errorf("CodexSlotIdentity = (%q, %q), want (%q, %q) — the two claims are read independently",
					email, plan, c.email, c.plan)
			}
		})
	}
}

// G2 in the one place a test can reach it: the two returned strings are the
// claims and nothing else. A return that leaked the raw token, the access or
// refresh token, or the whole payload would put credential material into every
// board payload and every log line that renders a slot.
func TestEdgeCxpSlotIdentityReturnsNoTokenMaterial(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, "g2")

	token := edgeCxpJWT(edgeCxpAuthPayload("g2@example.test", "plus"))
	edgeCxpWriteAuth(t, dir, token)

	email, plan := CodexSlotIdentity(dir)
	for _, leak := range []string{token, "fixture-access", "fixture-refresh", "HEADER", "SIGNATURE", "user-fixture", "acct-fixture"} {
		if strings.Contains(email, leak) || strings.Contains(plan, leak) {
			t.Errorf("CodexSlotIdentity leaked %q into (%q, %q) — G2: only the email and the plan ever leave this call", leak, email, plan)
		}
	}
	if email != "g2@example.test" || plan != "plus" {
		t.Fatalf("CodexSlotIdentity = (%q, %q); the leak check above is only meaningful when the happy answer is right", email, plan)
	}
}

// ---------------------------------------------------------------------------
// CodexMainEmail
// ---------------------------------------------------------------------------

// The machine login is ~/.codex and nothing else. The collision fixture points
// CODEX_POOL_DIR at a pool holding a slot with a DIFFERENT identity: a reader
// that fell back to the pool (or to the first identity it could find) would
// hand the binder the wrong email to exclude, and the exclusion that exists to
// keep a cell off the machine login would silently exclude a healthy slot
// instead.
func TestEdgeCxpMainEmailReadsOnlyTheCodexHome(t *testing.T) {
	pool := edgeCxpSandbox(t)
	home := os.Getenv("HOME")

	edgeCxpSlot(t, pool, "tmp-1", "slot@example.test", "plus")

	t.Run("no ~/.codex at all", func(t *testing.T) {
		if got := CodexMainEmail(); got != "" {
			t.Errorf("CodexMainEmail() = %q, want empty — an unreadable machine login is an unknown identity, not a pool slot's", got)
		}
	})

	t.Run("~/.codex wins over any pool slot", func(t *testing.T) {
		edgeCxpWriteAuth(t, filepath.Join(home, ".codex"),
			edgeCxpJWT(edgeCxpAuthPayload("machine@example.test", "pro")))
		if got := CodexMainEmail(); got != "machine@example.test" {
			t.Errorf("CodexMainEmail() = %q, want machine@example.test", got)
		}
	})
}

// A ~/.codex whose auth.json is unreadable is an unknown main identity, not an
// error: the binder treats the empty string as "apply no email filter", which
// binds a cell somewhere rather than refusing to bind anything on a machine
// whose login file is mid-rewrite.
func TestEdgeCxpMainEmailDegradesToEmpty(t *testing.T) {
	edgeCxpSandbox(t)
	home := os.Getenv("HOME")
	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := CodexMainEmail(); got != "" {
		t.Errorf("CodexMainEmail() = %q, want empty", got)
	}
}

// ---------------------------------------------------------------------------
// CodexSlotUsable
// ---------------------------------------------------------------------------

// The probe is fail-closed and its whole job is to keep CODEX_HOME off a path
// that is not a logged-in codex home: pointing it at a missing directory makes
// codex CREATE an empty logged-out home there, which is a pane that opens,
// fails, and leaves the board reading the cell as working.
//
// The true row is deliberately a file holding one junk byte: the contract says
// the content is never read, so a probe that parsed it would refuse a slot
// whose auth.json is a shape it does not recognise.
func TestEdgeCxpSlotUsableTruthTable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	mk := func(name string, fn func(dir string)) string {
		dir := filepath.Join(home, name)
		fn(dir)
		return dir
	}

	cases := []struct {
		name string
		dir  string
		want bool
	}{
		{"missing directory", filepath.Join(home, "nope"), false},
		{"the path is a regular file", mk("afile", func(dir string) {
			if err := os.WriteFile(dir, []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}), false},
		{"directory with no auth.json", mk("bare", func(dir string) {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
		}), false},
		{"auth.json is zero bytes", mk("empty", func(dir string) {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "auth.json"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}), false},
		{"auth.json is a directory", mk("authdir", func(dir string) {
			if err := os.MkdirAll(filepath.Join(dir, "auth.json"), 0o755); err != nil {
				t.Fatal(err)
			}
		}), false},
		{"auth.json is one junk byte — content is never read", mk("junk", func(dir string) {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}), true},
		{"the empty path", "", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := CodexSlotUsable(c.dir); got != c.want {
				t.Errorf("CodexSlotUsable(%q) = %v, want %v", c.dir, got, c.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// CodexSlotUsage
// ---------------------------------------------------------------------------

// A slot with no usable creds is a row, not an omission — the board shows it as
// a slot that needs a login — but it carries NO windows: a Session at 0 percent
// would rank it as the emptiest account in the pool and every bind would land
// on the one slot that cannot authenticate.
func TestEdgeCxpSlotUsageNoCredsHasNoWindows(t *testing.T) {
	pool := edgeCxpSandbox(t)
	acc := edgeCxpAccounts(t, pool)
	if err := os.MkdirAll(filepath.Join(acc, "bare"), 0o755); err != nil {
		t.Fatal(err)
	}

	row := CodexSlotUsage("bare")
	if row.Slug != "bare" {
		t.Errorf("Slug = %q, want bare", row.Slug)
	}
	if !row.NoCreds {
		t.Error("NoCreds = false, want true — the slot holds no auth.json")
	}
	if row.Session != nil || row.Weekly != nil {
		t.Errorf("Session = %+v, Weekly = %+v; want both nil for a credential-less slot", row.Session, row.Weekly)
	}
	if row.Email != "" || row.Tier != "" {
		t.Errorf("(Email, Tier) = (%q, %q), want the empty pair — there is no identity to read", row.Email, row.Tier)
	}
	if row.Reauth {
		t.Error("Reauth = true, want false — codex refreshes its own token and kraken never does")
	}
	if row.Current {
		t.Error("Current = true, want false — a codex row is never the machine's current claude login")
	}
	if row.Models != nil {
		t.Errorf("Models = %v, want nil — codex has no per-model windows", row.Models)
	}
}

// A slug naming no directory at all is the same row shape: the pass can race a
// `cxp remove`, and the answer must be a NoCreds row rather than a panic.
func TestEdgeCxpSlotUsageOfAVanishedSlotIsANoCredsRow(t *testing.T) {
	edgeCxpSandbox(t)
	row := CodexSlotUsage("gone")
	if row.Slug != "gone" || !row.NoCreds {
		t.Errorf("row = %+v, want slug gone with NoCreds true", row)
	}
	if row.Session != nil || row.Weekly != nil {
		t.Errorf("Session = %+v, Weekly = %+v, want both nil", row.Session, row.Weekly)
	}
}

// A credentialled slot codex has never actually run in — `tmp-2` on this
// machine today — must still be ELIGIBLE (Q3). The selector excludes a row with
// no Session, so the zero-percent Session is what keeps a brand-new slot in the
// ranking; FetchedAt 0 is what ranks it stale, and Weekly stays nil because
// nothing is known about the week.
//
// Three shapes reach this state and all three must answer alike: no sessions
// dir, an empty one, and one holding only files that are not rollouts.
func TestEdgeCxpSlotUsageWithNoEvidenceIsStaleButEligible(t *testing.T) {
	pool := edgeCxpSandbox(t)

	cases := map[string]func(t *testing.T, slotDir string){
		"no sessions dir": func(t *testing.T, slotDir string) {},
		"empty sessions dir": func(t *testing.T, slotDir string) {
			t.Helper()
			if err := os.MkdirAll(filepath.Join(slotDir, "sessions"), 0o755); err != nil {
				t.Fatal(err)
			}
		},
		"sessions holds nothing matching rollout-*.jsonl": func(t *testing.T, slotDir string) {
			t.Helper()
			dir := filepath.Join(slotDir, "sessions", "2026", "09", "17")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"notes.jsonl", "rollout-x.txt", "rollout.jsonl", "other-rollout-x.jsonl"} {
				if err := os.WriteFile(filepath.Join(dir, name),
					[]byte(edgeCxpRecord("2026-09-17T00:00:00Z", edgeCxpWindow(90, 10080, 1790000000), "null", "null")+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
		},
	}

	i := 0
	for name, setup := range cases {
		i++
		slug := fmt.Sprintf("slot-%d", i)
		t.Run(name, func(t *testing.T) {
			dir := edgeCxpSlot(t, pool, slug, slug+"@example.test", "plus")
			setup(t, dir)

			row := CodexSlotUsage(slug)
			if row.NoCreds {
				t.Fatal("NoCreds = true, want false — the slot has an auth.json")
			}
			if row.Session == nil {
				t.Fatal("Session = nil; a credentialled slot with no evidence must still be eligible, which means a non-nil Session")
			}
			if row.Session.Percent != 0 || row.Session.ResetsAt != 0 {
				t.Errorf("Session = %+v, want {Percent:0 ResetsAt:0}", *row.Session)
			}
			if row.Weekly != nil {
				t.Errorf("Weekly = %+v, want nil — nothing is known about the week", *row.Weekly)
			}
			if row.FetchedAt != 0 {
				t.Errorf("FetchedAt = %d, want 0 — never fetched is what ranks it stale", row.FetchedAt)
			}
		})
	}
}

// The window boundary, in both directions, and in BOTH arrival fields. 10080 is
// weekly by "greater than or equal"; 10079 is a session window. The primary /
// secondary names carry no meaning, which is the whole point of routing on
// window_minutes: the codex build changed which field the weekly window arrives
// in, and a router keyed on the name silently swapped every slot's two bars.
func TestEdgeCxpSlotUsageRoutesExactlyOnTheWindowMinutesBoundary(t *testing.T) {
	pool := edgeCxpSandbox(t)

	cases := []struct {
		name               string
		primary, secondary string
		wantSession        float64
		wantWeekly         float64
	}{
		{"10080 in primary, null secondary", edgeCxpWindow(12, 10080, 1790000000), "null", 12, 12}, // lone weekly is copied into session
		{"10080 in secondary, 300 in primary", edgeCxpWindow(7, 300, 1790000001), edgeCxpWindow(31, 10080, 1790000002), 7, 31},
		{"10079 is a session window", edgeCxpWindow(44, 10079, 1790000003), "null", 44, -1},
		{"10081 is a weekly window", edgeCxpWindow(55, 10081, 1790000004), "null", 55, 55},
	}

	for i, c := range cases {
		slug := fmt.Sprintf("win-%d", i)
		t.Run(c.name, func(t *testing.T) {
			dir := edgeCxpSlot(t, pool, slug, slug+"@example.test", "plus")
			edgeCxpWriteRollout(t, dir, "2026/09/17/rollout-a.jsonl", time.Now(),
				edgeCxpRecord("2026-09-17T00:00:00Z", c.primary, c.secondary, "null"))

			row := CodexSlotUsage(slug)
			if row.Session == nil {
				t.Fatalf("Session = nil, want %v percent", c.wantSession)
			}
			if row.Session.Percent != c.wantSession {
				t.Errorf("Session.Percent = %v, want %v", row.Session.Percent, c.wantSession)
			}
			if c.wantWeekly < 0 {
				if row.Weekly != nil {
					t.Errorf("Weekly = %+v, want nil — a sub-10080 window is never weekly, and the copy only ever runs weekly→session", *row.Weekly)
				}
				return
			}
			if row.Weekly == nil {
				t.Fatalf("Weekly = nil, want %v percent", c.wantWeekly)
			}
			if row.Weekly.Percent != c.wantWeekly {
				t.Errorf("Weekly.Percent = %v, want %v", row.Weekly.Percent, c.wantWeekly)
			}
		})
	}
}

// resets_at crosses the wire in unix SECONDS and the row carries unix MILLIS —
// the same units AccountUsage's claude rows use, so the bars' countdown is one
// arithmetic for both pools. A missing resets_at stays 0 rather than becoming
// the epoch in millis.
func TestEdgeCxpSlotUsageConvertsResetsAtSecondsToMillis(t *testing.T) {
	pool := edgeCxpSandbox(t)

	t.Run("a real reset stamp is multiplied", func(t *testing.T) {
		dir := edgeCxpSlot(t, pool, "res-1", "res-1@example.test", "plus")
		edgeCxpWriteRollout(t, dir, "2026/09/17/rollout-a.jsonl", time.Now(),
			edgeCxpRecord("2026-09-17T00:00:00Z", edgeCxpWindow(12, 10080, 1790208555), "null", "null"))

		row := CodexSlotUsage("res-1")
		if row.Weekly == nil {
			t.Fatal("Weekly = nil")
		}
		if row.Weekly.ResetsAt != 1790208555*1000 {
			t.Errorf("Weekly.ResetsAt = %d, want %d (unix seconds × 1000)", row.Weekly.ResetsAt, int64(1790208555)*1000)
		}
	})

	t.Run("an absent resets_at stays zero", func(t *testing.T) {
		dir := edgeCxpSlot(t, pool, "res-2", "res-2@example.test", "plus")
		edgeCxpWriteRollout(t, dir, "2026/09/17/rollout-a.jsonl", time.Now(),
			edgeCxpRecord("2026-09-17T00:00:00Z", `{"used_percent":12.0,"window_minutes":10080}`, "null", "null"))

		row := CodexSlotUsage("res-2")
		if row.Weekly == nil {
			t.Fatal("Weekly = nil")
		}
		if row.Weekly.ResetsAt != 0 {
			t.Errorf("Weekly.ResetsAt = %d, want 0 — an unknown reset is 0, not the epoch", row.Weekly.ResetsAt)
		}
	})
}

// FetchedAt is the RECORD's own timestamp, not the time kraken read the file.
// That is what makes the selector's fresh-over-stale key mean "used recently"
// for codex; stamping `now` would make every slot look equally fresh on every
// pass and collapse the ranking onto the count key alone.
//
// The stale fixture is a record from months back with a file mtime of NOW: if
// the row came from the filesystem, or from the clock, this reads as fresh.
func TestEdgeCxpSlotUsageFetchedAtComesFromTheRecordNotTheClock(t *testing.T) {
	pool := edgeCxpSandbox(t)
	dir := edgeCxpSlot(t, pool, "stamp", "stamp@example.test", "plus")

	const ts = "2026-06-01T12:34:56.789Z"
	edgeCxpWriteRollout(t, dir, "2026/06/01/rollout-a.jsonl", time.Now(),
		edgeCxpRecord(ts, edgeCxpWindow(12, 10080, 1790208555), "null", "null"))

	row := CodexSlotUsage("stamp")
	if want := edgeCxpMillis(t, ts); row.FetchedAt != want {
		t.Errorf("FetchedAt = %d, want %d — the record's own timestamp in unix millis, never the read time", row.FetchedAt, want)
	}
}

// A record whose timestamp cannot be parsed degrades to 0 (G4) rather than
// erroring the row away or stamping the read time: the windows it carries are
// still real usage, and 0 only ranks the row stale.
func TestEdgeCxpSlotUsageUnparseableTimestampDegradesFetchedAtToZero(t *testing.T) {
	pool := edgeCxpSandbox(t)
	dir := edgeCxpSlot(t, pool, "badstamp", "badstamp@example.test", "plus")

	edgeCxpWriteRollout(t, dir, "2026/09/17/rollout-a.jsonl", time.Now(),
		edgeCxpRecord("not-a-timestamp", edgeCxpWindow(12, 10080, 1790208555), "null", "null"))

	row := CodexSlotUsage("badstamp")
	if row.FetchedAt != 0 {
		t.Errorf("FetchedAt = %d, want 0 for an unparseable timestamp (G4)", row.FetchedAt)
	}
	if row.Weekly == nil || row.Weekly.Percent != 12 {
		t.Errorf("Weekly = %+v, want the record's 12 percent — a bad timestamp must not discard the usage it carries", row.Weekly)
	}
}

// The newest file is where the search STARTS, not where it ends. Short sessions
// on the current codex build write no token_count event at all, so the most
// recently used slot is exactly the one whose newest rollout carries nothing —
// a search that stopped there would report the busiest slot as never-used and
// send every bind straight at it.
//
// Three files: newest is empty, middle carries a token_count with NO
// rate_limits, oldest carries the real record.
func TestEdgeCxpSlotUsageKeepsLookingPastRolloutsWithNoLimitsRecord(t *testing.T) {
	pool := edgeCxpSandbox(t)
	dir := edgeCxpSlot(t, pool, "fallthrough", "fallthrough@example.test", "plus")

	base := time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)
	edgeCxpWriteRollout(t, dir, "2026/09/17/rollout-newest.jsonl", base)
	edgeCxpWriteRollout(t, dir, "2026/09/16/rollout-middle.jsonl", base.Add(-time.Hour),
		edgeCxpPlainRecord("2026-09-16T00:00:00Z"),
		`{"timestamp":"2026-09-16T00:00:01Z","type":"event_msg","payload":{"type":"agent_message","message":"hi"}}`)
	edgeCxpWriteRollout(t, dir, "2026/09/15/rollout-oldest.jsonl", base.Add(-2*time.Hour),
		edgeCxpRecord("2026-09-15T00:00:00Z", edgeCxpWindow(63, 10080, 1790208555), "null", "null"))

	row := CodexSlotUsage("fallthrough")
	if row.Weekly == nil {
		t.Fatal("Weekly = nil — the search stopped at a rollout carrying no rate_limits record")
	}
	if row.Weekly.Percent != 63 {
		t.Errorf("Weekly.Percent = %v, want 63 (from the oldest file, the only one with a rate_limits record)", row.Weekly.Percent)
	}
	if want := edgeCxpMillis(t, "2026-09-15T00:00:00Z"); row.FetchedAt != want {
		t.Errorf("FetchedAt = %d, want %d — the stamp belongs to the record that was actually used", row.FetchedAt, want)
	}
}

// Malformed lines inside a rollout are skipped, not fatal: a rollout caught
// mid-write has a torn last line, and a parser that gave up on it would blank
// the row of whichever slot is busiest right now.
func TestEdgeCxpSlotUsageSkipsMalformedRolloutLines(t *testing.T) {
	pool := edgeCxpSandbox(t)
	dir := edgeCxpSlot(t, pool, "torn", "torn@example.test", "plus")

	edgeCxpWriteRollout(t, dir, "2026/09/17/rollout-a.jsonl", time.Now(),
		"not json at all",
		"",
		edgeCxpRecord("2026-09-17T00:00:00Z", edgeCxpWindow(21, 10080, 1790208555), "null", "null"),
		`{"timestamp":"2026-09-17T00:00:01Z","type":"event_msg","payload":`)

	row := CodexSlotUsage("torn")
	if row.Weekly == nil || row.Weekly.Percent != 21 {
		t.Errorf("Weekly = %+v, want 21 percent — malformed lines are skipped, never fatal", row.Weekly)
	}
}

// A session window with no weekly beside it does NOT gain a fabricated weekly.
// The copy runs one way only — weekly into an absent session — because that
// direction has a reason (the selector excludes a row with no Session) and the
// other would put a number on the board's weekly bar that nothing measured.
func TestEdgeCxpSlotUsageNeverCopiesSessionIntoWeekly(t *testing.T) {
	pool := edgeCxpSandbox(t)
	dir := edgeCxpSlot(t, pool, "sessonly", "sessonly@example.test", "plus")

	edgeCxpWriteRollout(t, dir, "2026/09/17/rollout-a.jsonl", time.Now(),
		edgeCxpRecord("2026-09-17T00:00:00Z", edgeCxpWindow(48, 300, 1790208555), "null", "null"))

	row := CodexSlotUsage("sessonly")
	if row.Session == nil || row.Session.Percent != 48 {
		t.Fatalf("Session = %+v, want 48 percent", row.Session)
	}
	if row.Weekly != nil {
		t.Errorf("Weekly = %+v, want nil", *row.Weekly)
	}
}

// The lone-weekly copy carries BOTH fields, not just the percent: the steer
// panel decides whether to draw a session bar at all by comparing the two
// windows' percent AND resets_at, so a copy that dropped the reset stamp would
// make every codex row render a redundant second bar.
func TestEdgeCxpSlotUsageLoneWeeklyCopyCarriesPercentAndReset(t *testing.T) {
	pool := edgeCxpSandbox(t)
	dir := edgeCxpSlot(t, pool, "lone", "lone@example.test", "plus")

	edgeCxpWriteRollout(t, dir, "2026/09/17/rollout-a.jsonl", time.Now(),
		edgeCxpRecord("2026-09-17T00:00:00Z", edgeCxpWindow(37.5, 10080, 1790208555), "null", "null"))

	row := CodexSlotUsage("lone")
	if row.Weekly == nil || row.Session == nil {
		t.Fatalf("Weekly = %+v, Session = %+v; want both non-nil", row.Weekly, row.Session)
	}
	if *row.Session != *row.Weekly {
		t.Errorf("Session = %+v, Weekly = %+v — with no session window of its own, Session is a COPY of Weekly, reset stamp included", *row.Session, *row.Weekly)
	}
}

// Identity reaches the row under its own field names. The collision fixture
// makes the slug, the email's local part and the plan all the literal string
// `plus`, so a row built by assigning the wrong source to a field still looks
// plausible — and the assertion catches it anyway, because Email carries the
// full address while Slug and Tier do not.
func TestEdgeCxpSlotUsageIdentityFieldsDoNotSwap(t *testing.T) {
	pool := edgeCxpSandbox(t)
	edgeCxpSlot(t, pool, "plus", "plus@example.test", "plus")

	row := CodexSlotUsage("plus")
	if row.Slug != "plus" {
		t.Errorf("Slug = %q, want plus", row.Slug)
	}
	if row.Email != "plus@example.test" {
		t.Errorf("Email = %q, want plus@example.test", row.Email)
	}
	if row.Tier != "plus" {
		t.Errorf("Tier = %q, want plus — Tier is the PLAN", row.Tier)
	}
	if row.Error != "" {
		t.Errorf("Error = %q, want empty — a readable slot is not an errored row", row.Error)
	}
}

// ---------------------------------------------------------------------------
// FetchCodexUsage
// ---------------------------------------------------------------------------

// An empty pool is an empty result, not a nil-versus-empty distinction anyone
// has to reason about and not an error: the wire obligation above it says
// codex_accounts is ALWAYS an array, and this is where that starts.
func TestEdgeCxpFetchCodexUsageEmptyPoolIsEmpty(t *testing.T) {
	t.Run("no pool at all", func(t *testing.T) {
		edgeCxpSandbox(t)
		if got := FetchCodexUsage(); len(got) != 0 {
			t.Errorf("FetchCodexUsage() = %+v, want no rows", got)
		}
	})

	t.Run("accounts dir with only excluded entries", func(t *testing.T) {
		pool := edgeCxpSandbox(t)
		acc := edgeCxpAccounts(t, pool)
		if err := os.MkdirAll(filepath.Join(acc, "main"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(acc, ".cache"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(acc, "snapshots.json"), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := FetchCodexUsage(); len(got) != 0 {
			t.Errorf("FetchCodexUsage() = %+v, want no rows — main, dot-names and files are not slots", got)
		}
	})
}

// The row order is CodexPoolSlugs' order, which is ascending. The slot with no
// creds still gets its row, in position: a pass that dropped it would make a
// logged-out slot invisible on the board, which is the one place carl would see
// that it needs `cxp login`.
func TestEdgeCxpFetchCodexUsageKeepsSlugOrderAndUnusableSlots(t *testing.T) {
	pool := edgeCxpSandbox(t)
	acc := edgeCxpAccounts(t, pool)

	edgeCxpSlot(t, pool, "zeta", "zeta@example.test", "plus")
	edgeCxpSlot(t, pool, "alpha", "alpha@example.test", "pro")
	if err := os.MkdirAll(filepath.Join(acc, "beta"), 0o755); err != nil { // no auth.json
		t.Fatal(err)
	}

	rows := FetchCodexUsage()
	var slugs []string
	for _, r := range rows {
		slugs = append(slugs, r.Slug)
	}
	if !reflect.DeepEqual(slugs, []string{"alpha", "beta", "zeta"}) {
		t.Fatalf("slugs = %v, want [alpha beta zeta] — one row per slug, in CodexPoolSlugs' ascending order", slugs)
	}
	if !rows[1].NoCreds {
		t.Error("the credential-less slot's row has NoCreds false")
	}
	if rows[0].NoCreds || rows[2].NoCreds {
		t.Error("a credentialled slot's row has NoCreds true")
	}
}

// ---------------------------------------------------------------------------
// CodexLimitReached
// ---------------------------------------------------------------------------

// Every unreadable or uninformative input is the same triple, because the
// caller turns `ok` into "record a limit hit and rebind this cell onto another
// account". A false positive here kills a healthy cell's pane.
func TestEdgeCxpLimitReachedDegradesToNotOK(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	write := func(name string, lines ...string) string {
		p := filepath.Join(dir, name)
		body := ""
		for _, l := range lines {
			body += l + "\n"
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	cases := []struct {
		name string
		path string
	}{
		{"the empty path", ""},
		{"a missing file", filepath.Join(dir, "nope.jsonl")},
		{"a directory", dir},
		{"an empty file", write("empty.jsonl")},
		{"no token_count records at all", write("none.jsonl",
			`{"timestamp":"2026-09-17T00:00:00Z","type":"event_msg","payload":{"type":"agent_message","message":"hi"}}`)},
		{"a token_count carrying no rate_limits", write("nolimits.jsonl", edgeCxpPlainRecord("2026-09-17T00:00:00Z"))},
		{"reached_type is null", write("null.jsonl",
			edgeCxpRecord("2026-09-17T00:00:00Z", edgeCxpWindow(99, 10080, 1790208555), "null", "null"))},
		{"reached_type is the empty string", write("blank.jsonl",
			edgeCxpRecord("2026-09-17T00:00:00Z", edgeCxpWindow(99, 10080, 1790208555), "null", `""`))},
		{"reached_type is a number", write("number.jsonl",
			edgeCxpRecord("2026-09-17T00:00:00Z", edgeCxpWindow(99, 10080, 1790208555), "null", `1`))},
		{"reached_type is a bool", write("bool.jsonl",
			edgeCxpRecord("2026-09-17T00:00:00Z", edgeCxpWindow(99, 10080, 1790208555), "null", `true`))},
		{"only malformed lines", write("torn.jsonl", "not json", `{"type":`)},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			kind, resets, ok := CodexLimitReached(c.path)
			if ok || kind != "" || resets != 0 {
				t.Errorf("CodexLimitReached = (%q, %d, %v), want (\"\", 0, false)", kind, resets, ok)
			}
		})
	}
}

// The LAST rate-limits record decides, and an earlier record that HAD the field
// set does not survive it. This is the recovery case stated as a test: a cell
// that hit a wall and then came back writes exactly this file, and a reader
// that scanned for "any record with the field set" would re-rescue a cell that
// is already healthy, on every tick, forever.
func TestEdgeCxpLimitReachedTheLastRecordUnsetsAnEarlierOne(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	p := filepath.Join(dir, "rollout-recovered.jsonl")

	body := strings.Join([]string{
		edgeCxpRecord("2026-09-17T00:00:00Z", edgeCxpWindow(100, 10080, 1790208555), "null", `"weekly"`),
		edgeCxpRecord("2026-09-17T01:00:00Z", edgeCxpWindow(3, 10080, 1790900000), "null", "null"),
	}, "\n") + "\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	kind, resets, ok := CodexLimitReached(p)
	if ok || kind != "" || resets != 0 {
		t.Errorf("CodexLimitReached = (%q, %d, %v), want (\"\", 0, false) — the LAST rate_limits record has the field null", kind, resets, ok)
	}
}

// A later record that carries no rate_limits at all does not become "the last
// one": the contract says the last record CARRYING rate_limits decides, so a
// plain token_count written after the wall must not blank a live hit.
func TestEdgeCxpLimitReachedIgnoresLaterRecordsWithoutRateLimits(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	p := filepath.Join(dir, "rollout-trailing.jsonl")

	body := strings.Join([]string{
		edgeCxpRecord("2026-09-17T00:00:00Z", edgeCxpWindow(100, 10080, 1790208555), "null", `"weekly"`),
		edgeCxpPlainRecord("2026-09-17T00:05:00Z"),
		`{"timestamp":"2026-09-17T00:06:00Z","type":"event_msg","payload":{"type":"agent_message","message":"bye"}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	kind, resets, ok := CodexLimitReached(p)
	if !ok {
		t.Fatal("ok = false; a trailing record with no rate_limits is not the last rate-limits record")
	}
	if kind != "weekly" {
		t.Errorf("reachedType = %q, want weekly", kind)
	}
	if resets != 1790208555 {
		t.Errorf("resetsAt = %d, want 1790208555 in unix SECONDS (never millis — the caller renders it into resets_at= verbatim)", resets)
	}
}

// The reset stamp is the one belonging to the window with the greatest
// used_percent, and the windows arrive in whichever field the build chooses —
// so the fixture puts the exhausted window in `secondary`. Taking `primary`'s
// stamp, or the first non-null one, would park the cell until the wrong clock.
func TestEdgeCxpLimitReachedTakesTheResetOfTheFullestWindow(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	cases := []struct {
		name               string
		primary, secondary string
		want               int64
	}{
		{"fullest is secondary", edgeCxpWindow(12, 300, 1790000001), edgeCxpWindow(100, 10080, 1790000002), 1790000002},
		{"fullest is primary", edgeCxpWindow(100, 300, 1790000003), edgeCxpWindow(4, 10080, 1790000004), 1790000003},
		{"the only non-null window wins by default", "null", edgeCxpWindow(87, 10080, 1790000005), 1790000005},
		{"no non-null window at all", "null", "null", 0},
	}

	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := filepath.Join(dir, fmt.Sprintf("rollout-%d.jsonl", i))
			if err := os.WriteFile(p, []byte(edgeCxpRecord("2026-09-17T00:00:00Z", c.primary, c.secondary, `"weekly"`)+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			kind, resets, ok := CodexLimitReached(p)
			if !ok || kind != "weekly" {
				t.Fatalf("CodexLimitReached = (%q, _, %v), want (weekly, _, true)", kind, ok)
			}
			if resets != c.want {
				t.Errorf("resetsAt = %d, want %d", resets, c.want)
			}
		})
	}
}

// The reached type crosses back VERBATIM: the caller splices it into a reason
// string the rescue and the notification both render, so a normalised or
// lower-cased value would misreport which window carl actually hit.
func TestEdgeCxpLimitReachedReturnsTheTypeVerbatim(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	for _, want := range []string{"weekly", "primary", "Weekly", "usage_limit_reached", "5h"} {
		t.Run(want, func(t *testing.T) {
			p := filepath.Join(dir, "rollout-"+strings.ReplaceAll(want, "/", "_")+".jsonl")
			body, err := json.Marshal(want)
			if err != nil {
				t.Fatal(err)
			}
			line := edgeCxpRecord("2026-09-17T00:00:00Z", edgeCxpWindow(100, 10080, 1790208555), "null", string(body))
			if err := os.WriteFile(p, []byte(line+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			kind, _, ok := CodexLimitReached(p)
			if !ok || kind != want {
				t.Errorf("CodexLimitReached = (%q, _, %v), want (%q, _, true)", kind, ok, want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// CodexHomeFromCommand
// ---------------------------------------------------------------------------

// The refusals, one per way a command can LOOK like a bound one without being
// one. Every false here is a pane whose home is then resolved as ~/.codex,
// which is the safe answer; a false POSITIVE is discovery reading a slot's
// thread index for a pane that is not running under it, which is how a cell
// ends up displaying another account's conversation.
func TestEdgeCxpHomeFromCommandRefusals(t *testing.T) {
	for _, c := range []struct{ name, cmd string }{
		{"the empty command", ""},
		{"a bare launcher", "codex"},
		{"an unquoted value", "CODEX_HOME=/x/y codex"},
		{"double-quoted rather than single-quoted", `CODEX_HOME="/x/y" codex`},
		{"the claude variable", "CLAUDE_SECURESTORAGE_CONFIG_DIR='/x/y' claude"},
		{"another variable first", "FOO=1 CODEX_HOME='/x/y' codex"},
		{"leading whitespace before the assignment", " CODEX_HOME='/x/y' codex"},
		{"the assignment appears mid-command", "echo CODEX_HOME='/x/y' && codex"},
		{"no closing quote", "CODEX_HOME='/x/y codex"},
		{"no space after the closing quote", "CODEX_HOME='/x/y'codex"},
		{"two spaces after the closing quote is still one assignment, but the name is wrong", "CODEX_HOMES='/x/y' codex"},
		{"a lower-case variable name", "codex_home='/x/y' codex"},
		{"the value is the whole rest with no terminator", "CODEX_HOME='"},
		{"two enclosing double-quote pairs — at most ONE is removed", `""CODEX_HOME='/x/y' codex""`},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := CodexHomeFromCommand(c.cmd); got != "" {
				t.Errorf("CodexHomeFromCommand(%q) = %q, want empty", c.cmd, got)
			}
		})
	}
}

// The accepted shapes, including the two layers of quoting a real pane carries:
// tmux hands `#{pane_start_command}` back wrapped in double quotes with inner
// double quotes escaped, and the shell escape for a single quote inside a
// single-quoted value is the four characters quote, backslash, quote, quote
// (spelled out rather than shown, because gofmt rewrites a doubled apostrophe
// inside a doc comment).
//
// The collision fixture is a slot dir whose own path contains the literal text
// `CODEX_HOME='` — a parser that searched for the marker rather than anchoring
// at position zero and reading to the closing quote would cut the value in the
// wrong place and hand back a directory that does not exist.
func TestEdgeCxpHomeFromCommandAcceptedShapes(t *testing.T) {
	for _, c := range []struct{ name, cmd, want string }{
		{"a plain bound command", "CODEX_HOME='/pool/accounts/tmp-1' codex -m gpt-6-astra", "/pool/accounts/tmp-1"},
		{"a value containing spaces", "CODEX_HOME='/pool/my accounts/tmp-1' codex", "/pool/my accounts/tmp-1"},
		{"tmux's outer double quotes", `"CODEX_HOME='/pool/accounts/tmp-1' codex"`, "/pool/accounts/tmp-1"},
		{"tmux's outer quotes with an escaped inner quote", `"CODEX_HOME='/pool/accounts/tmp-1' codex -c \"x=1\""`, "/pool/accounts/tmp-1"},
		{"an escaped single quote inside the value", `CODEX_HOME='/pool/carl'\''s accounts/tmp-1' codex`, "/pool/carl's accounts/tmp-1"},
		{"a value that contains the marker itself", `CODEX_HOME='/tmp/CODEX_HOME='\''x/tmp-1' codex`, `/tmp/CODEX_HOME='x/tmp-1`},
		{"the empty value is indistinguishable from a refusal", "CODEX_HOME='' codex", ""},
		{"trailing content after the launcher is ignored", "CODEX_HOME='/pool/accounts/tmp-1' codex resume abc --yolo", "/pool/accounts/tmp-1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := CodexHomeFromCommand(c.cmd); got != c.want {
				t.Errorf("CodexHomeFromCommand(%q) = %q, want %q", c.cmd, got, c.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// IsCodexRollout
// ---------------------------------------------------------------------------

// The predicate replaces three copies of a `~/.codex`-shaped substring test, so
// its whole reason to exist is that it answers TRUE for a rollout living under
// a pool slot wherever the pool root is. Both halves matter: a false negative
// renders a bound cell's transcript with the claude parser (empty board detail),
// and a false positive parses a claude transcript as codex (the same, inverted).
func TestEdgeCxpIsCodexRolloutTruthTable(t *testing.T) {
	for _, c := range []struct {
		name string
		path string
		want bool
	}{
		{"the machine login's rollout", "/Users/x/.codex/sessions/2026/09/16/rollout-2026-09-16T00-00-00-abc.jsonl", true},
		{"a pool slot's rollout", "/Users/x/.codex-pool/accounts/tmp-1/sessions/2026/09/16/rollout-abc.jsonl", true},
		{"a pool at a wholly different root", "/var/tmp/pool/accounts/tmp-2/sessions/2026/rollout-abc.jsonl", true},
		{"the minimal accepted base name", "/x/sessions/rollout-.jsonl", true},

		{"the empty path", "", false},
		{"a claude project transcript", "/Users/x/.claude/projects/-Users-x-Developer-kraken/2b1f-uuid.jsonl", false},
		{"a claude project dir named after sessions", "/Users/x/.claude/projects/-Users-x-my-sessions-dir/2b1f-uuid.jsonl", false},
		{"a claude project dir named after codex", "/Users/x/.claude/projects/-Users-x-codex-sessions/2b1f-uuid.jsonl", false},
		{"a claude subagent sidecar", "/Users/x/.claude/projects/-Users-x-wt-kraken/2b1f/subagents/agent-1.jsonl", false},
		{"no sessions component — only a similar dir name", "/Users/x/.codex/sessionsdata/rollout-abc.jsonl", false},
		{"sessions only as a trailing basename", "/Users/x/.codex/sessions", false},
		{"capitalised Sessions", "/Users/x/.codex/Sessions/rollout-abc.jsonl", false},
		{"wrong extension", "/Users/x/.codex/sessions/2026/rollout-abc.json", false},
		{"rollouts-, not rollout-", "/Users/x/.codex/sessions/2026/rollouts-abc.jsonl", false},
		{"rollout with no hyphen", "/Users/x/.codex/sessions/2026/rollout.jsonl", false},
		{"the rollout name is a directory component, not the base", "/Users/x/.codex/sessions/rollout-abc.jsonl/inner.jsonl", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := IsCodexRollout(c.path); got != c.want {
				t.Errorf("IsCodexRollout(%q) = %v, want %v", c.path, got, c.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// PlaceCodexRollout
// ---------------------------------------------------------------------------

// edgeCxpPlaceHomes builds a from-home carrying one rollout and an empty
// to-home, and returns (fromHome, toHome, rolloutPath). The two homes are
// siblings whose names SHARE a prefix — `home` and `home2` — because the
// relative path is computed against fromHome and a naive prefix trim would
// leave a stray separator or eat a character on exactly this shape.
func edgeCxpPlaceHomes(t *testing.T) (string, string, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	from := filepath.Join(root, "home")
	to := filepath.Join(root, "home2")
	if err := os.MkdirAll(to, 0o755); err != nil {
		t.Fatal(err)
	}
	roll := edgeCxpWriteRollout(t, from, "2026/09/17/rollout-abc.jsonl", time.Now(),
		edgeCxpRecord("2026-09-17T00:00:00Z", edgeCxpWindow(3, 10080, 1790208555), "null", "null"))
	return from, to, roll
}

// Every refusal writes NOTHING. The destination is another account's home, and
// a half-written or misplaced file there is state kraken has no business
// creating (G3 bounds kraken's writes inside a slot to this one function, which
// is exactly why this function's refusals have to be clean).
func TestEdgeCxpPlaceRolloutRefusalsWriteNothing(t *testing.T) {
	t.Run("the source is not under <fromHome>/sessions/", func(t *testing.T) {
		from, to, _ := edgeCxpPlaceHomes(t)
		outside := filepath.Join(from, "other", "rollout-abc.jsonl")
		if err := os.MkdirAll(filepath.Dir(outside), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(outside, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := PlaceCodexRollout(outside, from, to); err == nil {
			t.Fatal("PlaceCodexRollout accepted a source outside <fromHome>/sessions/")
		}
		edgeCxpAssertEmptyTree(t, to)
	})

	t.Run("the source belongs to a third home", func(t *testing.T) {
		from, to, _ := edgeCxpPlaceHomes(t)
		other := filepath.Join(filepath.Dir(from), "home3")
		alien := edgeCxpWriteRollout(t, other, "2026/09/17/rollout-zzz.jsonl", time.Now(), "{}")
		if _, err := PlaceCodexRollout(alien, from, to); err == nil {
			t.Fatal("PlaceCodexRollout accepted a source under a different home's sessions dir")
		}
		edgeCxpAssertEmptyTree(t, to)
	})

	t.Run("the source does not exist", func(t *testing.T) {
		from, to, _ := edgeCxpPlaceHomes(t)
		missing := filepath.Join(from, "sessions", "2026", "09", "17", "rollout-gone.jsonl")
		if _, err := PlaceCodexRollout(missing, from, to); err == nil {
			t.Fatal("PlaceCodexRollout accepted a source that does not exist")
		}
		edgeCxpAssertEmptyTree(t, to)
	})

	t.Run("the source is a directory", func(t *testing.T) {
		from, to, _ := edgeCxpPlaceHomes(t)
		dir := filepath.Join(from, "sessions", "2026")
		if _, err := PlaceCodexRollout(dir, from, to); err == nil {
			t.Fatal("PlaceCodexRollout accepted a directory as the source")
		}
		edgeCxpAssertEmptyTree(t, to)
	})

	t.Run("fromHome and toHome are the same directory", func(t *testing.T) {
		from, _, roll := edgeCxpPlaceHomes(t)
		before, err := os.ReadFile(roll)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := PlaceCodexRollout(roll, from, from); err == nil {
			t.Fatal("PlaceCodexRollout accepted identical homes — the copy would be onto the live conversation itself")
		}
		after, err := os.ReadFile(roll)
		if err != nil {
			t.Fatalf("the source is gone after a refused placement: %v", err)
		}
		if string(before) != string(after) {
			t.Error("the source was modified by a refused placement")
		}
	})
}

// edgeCxpAssertEmptyTree fails when anything was created under root.
func edgeCxpAssertEmptyTree(t *testing.T, root string) {
	t.Helper()
	var found []string
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if p != root {
			found = append(found, strings.TrimPrefix(p, root))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	if len(found) != 0 {
		t.Errorf("a refused placement created %v under the destination home — a refusal writes NOTHING", found)
	}
}

// The destination mirrors the source's path RELATIVE to its home, which is the
// measured requirement from Q1: `codex exec resume <id>` under the new slot
// fails with "no rollout found for thread id" until the file sits at the same
// relative path. Parent dirs are created; the source is untouched.
func TestEdgeCxpPlaceRolloutMirrorsTheRelativePathAndLeavesTheSource(t *testing.T) {
	from, to, roll := edgeCxpPlaceHomes(t)
	src, err := os.ReadFile(roll)
	if err != nil {
		t.Fatal(err)
	}

	dst, err := PlaceCodexRollout(roll, from, to)
	if err != nil {
		t.Fatalf("PlaceCodexRollout: %v", err)
	}
	want := filepath.Join(to, "sessions", "2026", "09", "17", "rollout-abc.jsonl")
	if dst != want {
		t.Errorf("destination = %q, want %q (the source's path relative to fromHome, under toHome)", dst, want)
	}
	got, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	if string(got) != string(src) {
		t.Error("the destination's bytes differ from the source's")
	}
	again, err := os.ReadFile(roll)
	if err != nil || string(again) != string(src) {
		t.Errorf("the source was modified or removed (err %v) — the placement is a COPY", err)
	}
}

// A destination that already exists is REPLACED, because the source is the live
// conversation and the stale copy is a previous rescue's snapshot — resuming
// against it would rewind the cell. And the write is atomic through a temp file
// in the destination directory, so after the call that directory holds exactly
// the destination and no leftover temp: a codex that globbed `rollout-*.jsonl`
// over a half-written sibling is the failure the temp+rename exists to prevent.
func TestEdgeCxpPlaceRolloutReplacesAndLeavesNoTempBehind(t *testing.T) {
	from, to, roll := edgeCxpPlaceHomes(t)

	stale := filepath.Join(to, "sessions", "2026", "09", "17", "rollout-abc.jsonl")
	if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("STALE PREVIOUS COPY\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	dst, err := PlaceCodexRollout(roll, from, to)
	if err != nil {
		t.Fatalf("PlaceCodexRollout: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "STALE") {
		t.Error("an existing destination was not replaced — the source is the live conversation")
	}

	entries, err := os.ReadDir(filepath.Dir(dst))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if !reflect.DeepEqual(names, []string{"rollout-abc.jsonl"}) {
		t.Errorf("the destination dir holds %v, want exactly [rollout-abc.jsonl] — the temp file is renamed, never left behind", names)
	}
}

// ---------------------------------------------------------------------------
// Peer / tmuxPane structure
// ---------------------------------------------------------------------------

// Both new Peer fields are `json:"-"`, and this is a G2-adjacent obligation
// rather than a style one: a peer is marshalled into API payloads, and
// CodexHome names a pool slot's directory on disk while RolloutPath names a
// file holding a whole conversation. Neither belongs in anything a client sees.
func TestEdgeCxpPeerCodexFieldsNeverReachJSON(t *testing.T) {
	ty := reflect.TypeOf(Peer{})
	for _, name := range []string{"CodexHome", "RolloutPath"} {
		f, ok := ty.FieldByName(name)
		if !ok {
			t.Fatalf("Peer has no %s field", name)
		}
		if f.Type.Kind() != reflect.String {
			t.Errorf("Peer.%s is a %s, want string", name, f.Type.Kind())
		}
		if got := f.Tag.Get("json"); got != "-" {
			t.Errorf("Peer.%s json tag = %q, want %q", name, got, "-")
		}
	}

	blob, err := json.Marshal(&Peer{
		Name:        "cx",
		CodexHome:   "/pool/accounts/tmp-1",
		RolloutPath: "/pool/accounts/tmp-1/sessions/2026/rollout-abc.jsonl",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"/pool/accounts/tmp-1", "rollout-abc.jsonl", "codex_home", "CodexHome", "rollout_path", "RolloutPath"} {
		if strings.Contains(string(blob), leak) {
			t.Errorf("a marshalled Peer carries %q: %s", leak, blob)
		}
	}
}

// Both new fields are EMPTY on a peer discovery did not identify as codex — the
// zero value is the whole statement, and every consumer branches on
// non-emptiness (the rescue places a rollout only when RolloutPath is set).
func TestEdgeCxpPeerCodexFieldsDefaultEmpty(t *testing.T) {
	var p Peer
	if p.CodexHome != "" || p.RolloutPath != "" {
		t.Errorf("zero Peer = (%q, %q), want both empty", p.CodexHome, p.RolloutPath)
	}
}

// The pane record gains the start command as a plain string field. Its VALUE
// comes from tmux, which no hermetic test can produce; what is reachable here is
// that the field exists with the type the parser downstream of it expects.
func TestEdgeCxpTmuxPaneCarriesTheStartCommand(t *testing.T) {
	f, ok := reflect.TypeOf(tmuxPane{}).FieldByName("StartCmd")
	if !ok {
		t.Fatal("tmuxPane has no StartCmd field")
	}
	if f.Type.Kind() != reflect.String {
		t.Errorf("tmuxPane.StartCmd is a %s, want string", f.Type.Kind())
	}
}

// ---------------------------------------------------------------------------
// paneHoldsAgent
// ---------------------------------------------------------------------------

// The guard is a package VAR with the agent kind in its signature, and both
// halves are load-bearing. A var is what lets a test swap it (the pre-existing
// resume suite swaps paneHoldsClaude the same way); the agent parameter is what
// stops a codex cell's rescue respawning a pane whose foreground process is the
// right pid but the wrong program — the pid alone is recycled by the OS.
//
// The claude seam survives beside it: the existing resume tests swap
// paneHoldsClaude by name, and removing it would rewrite them.
func TestEdgeCxpPaneHoldsAgentIsASwappableThreeArgSeam(t *testing.T) {
	var _ func(pane string, pid int, agent string) bool = paneHoldsAgent
	var _ func(pane string, pid int) bool = paneHoldsClaude

	orig := paneHoldsAgent
	t.Cleanup(func() { paneHoldsAgent = orig })

	var seen []string
	paneHoldsAgent = func(pane string, pid int, agent string) bool {
		seen = append(seen, fmt.Sprintf("%s/%d/%s", pane, pid, agent))
		return false
	}
	if paneHoldsAgent("%3", 42, "codex") {
		t.Fatal("the swapped seam was not called")
	}
	if len(seen) != 1 || seen[0] != "%3/42/codex" {
		t.Errorf("seam observed %v, want [%%3/42/codex]", seen)
	}
}

// ---------------------------------------------------------------------------
// parsePaneStartCommands — Amendment 1
// ---------------------------------------------------------------------------

// The cut is at the FIRST tab and the command is taken verbatim. Both halves
// are load-bearing and both fail silently: a start command legitimately carries
// further tabs and double quotes (tmux hands the field back quoted, and a
// staged brief is read with `"$(cat …)"`), and a parser that split on ALL tabs,
// or that unquoted the remainder, would hand CodexHomeFromCommand a string that
// no longer begins with the assignment — resolving a bound pane to ~/.codex and
// showing another account's conversation on the cell's card.
func TestEdgeCxpParsePaneStartCommandsCutsAtTheFirstTabOnly(t *testing.T) {
	var _ func(string) map[string]string = parsePaneStartCommands

	const cmd = "CODEX_HOME='/pool/accounts/tmp-1' codex -c \"a\tb\" --arg\tafter"
	got := parsePaneStartCommands("%1\t" + cmd + "\n")

	if len(got) != 1 {
		t.Fatalf("parsePaneStartCommands = %v, want exactly one pane", got)
	}
	if got["%1"] != cmd {
		t.Errorf("command =\n  %q\nwant\n  %q\n(everything after the first tab, verbatim: further tabs and double quotes included)", got["%1"], cmd)
	}
	if home := CodexHomeFromCommand(got["%1"]); home != "/pool/accounts/tmp-1" {
		t.Errorf("the parsed command reads back as home %q — the two halves of discovery have to compose", home)
	}
}

// Every malformed line is DROPPED and affects no other pane. The input is one
// blob from one `tmux list-panes -a` call, so a parser that gave up on the
// first odd line would blank every pane on the machine — and tmux emits a
// trailing newline and, for a pane with no start command, an empty remainder.
func TestEdgeCxpParsePaneStartCommandsDropsOnlyTheBadLines(t *testing.T) {
	out := strings.Join([]string{
		"",                        // empty line
		"%1\tcodex",               // good
		"no-tab-anywhere",         // no tab → dropped
		"   ",                     // non-empty, no tab → dropped
		"pane2\tclaude",           // id does not begin with % → dropped
		" %3\tclaude",             // leading space, so it does not BEGIN with % → dropped
		"%4\t",                    // empty command, kept as ""
		"%5\tclaude --model opus", // good
		"%\tedge",                 // the id is exactly "%", which begins with % → kept
	}, "\n") + "\n"

	got := parsePaneStartCommands(out)

	want := map[string]string{
		"%1": "codex",
		"%4": "",
		"%5": "claude --model opus",
		"%":  "edge",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parsePaneStartCommands =\n  %v\nwant\n  %v", got, want)
	}
}

// Nothing in, nothing out — and an answer that is empty rather than nil-shaped
// trouble for the caller, which indexes it per pane.
func TestEdgeCxpParsePaneStartCommandsOnEmptyAndAllBadInput(t *testing.T) {
	for name, in := range map[string]string{
		"the empty string":    "",
		"newlines only":       "\n\n\n",
		"no line has a tab":   "alpha\nbeta\n",
		"no id begins with %": "p1\tcodex\np2\tclaude\n",
	} {
		t.Run(name, func(t *testing.T) {
			if got := parsePaneStartCommands(in); len(got) != 0 {
				t.Errorf("parsePaneStartCommands = %v, want no entries", got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// codexThreadForPane — Amendment 1
// ---------------------------------------------------------------------------

// edgeCxpPaneHomes stands up a machine login and two pool slots as known homes,
// and returns their directories. The slots need only to BE directories under
// accounts/ to be known homes; a thread database is what each case adds.
func edgeCxpPaneHomes(t *testing.T) (main, slotA, slotZ string) {
	t.Helper()
	pool := edgeCxpSandbox(t)
	acc := edgeCxpAccounts(t, pool)
	main = filepath.Join(os.Getenv("HOME"), ".codex")
	slotA, slotZ = filepath.Join(acc, "aaa"), filepath.Join(acc, "zzz")
	for _, d := range []string{main, slotA, slotZ} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return main, slotA, slotZ
}

// No row anywhere is `(zero, "", false)`. The empty HOME string in the second
// return is the whole statement — a caller that stamped a home on a miss would
// give the peer a CodexHome the rescue then treats as the source of a rollout
// placement.
func TestEdgeCxpCodexThreadForPaneWithNoRowAnywhere(t *testing.T) {
	var _ func(string, string) (codexThread, string, bool) = codexThreadForPane

	main, slotA, slotZ := edgeCxpPaneHomes(t)
	edgeCxpThreadsDB(t, main, []any{"m", "M", "gpt-6-astra", 1, "/roll/m.jsonl", "/wt/other", 100})
	edgeCxpThreadsDB(t, slotA, []any{"a", "A", "gpt-6-astra", 1, "/roll/a.jsonl", "/wt/other", 200})
	edgeCxpThreadsDB(t, slotZ, []any{"z", "Z", "gpt-6-astra", 1, "/roll/z.jsonl", "/wt/other", 300})

	th, home, ok := codexThreadForPane("CODEX_HOME='"+slotA+"' codex", "/wt/unknown")
	if ok || home != "" {
		t.Errorf("codexThreadForPane = (%+v, %q, %v), want (zero, \"\", false)", th, home, ok)
	}
}

// "A hit there is returned without consulting any other home." The fixture
// makes the short-circuit observable by inverting the ranking: the pane's own
// home holds the OLDEST row for the cwd and another home holds a much newer
// one. A scan that always took the maximum would answer with the other home's
// row — which is the stale row a REBOUND cell leaves behind, pointed at a
// rollout under an account this pane is no longer running on.
func TestEdgeCxpCodexThreadForPaneStopsAtTheHomeTheCommandNames(t *testing.T) {
	main, slotA, slotZ := edgeCxpPaneHomes(t)
	const cwd = "/wt/cell"

	edgeCxpThreadsDB(t, slotA, []any{"own", "Own", "gpt-6-astra", 1, "/roll/own.jsonl", cwd, 100})
	edgeCxpThreadsDB(t, slotZ, []any{"newer", "Newer", "gpt-6-astra", 1, "/roll/newer.jsonl", cwd, 9000})
	edgeCxpThreadsDB(t, main, []any{"newest", "Newest", "gpt-6-astra", 1, "/roll/newest.jsonl", cwd, 99000})

	th, home, ok := codexThreadForPane("CODEX_HOME='"+slotA+"' codex", cwd)
	if !ok {
		t.Fatal("codexThreadForPane found nothing although the named home holds a row")
	}
	if th.ID != "own" {
		t.Errorf("ID = %q, want own — a hit in the home the command names is returned without consulting any other", th.ID)
	}
	if home != slotA {
		t.Errorf("home = %q, want %q", home, slotA)
	}
}

// On a MISS the scan takes the greatest `updated_at` across the other homes,
// never the first match. The fixture puts the loser first in every ordering a
// scan is likely to walk — `aaa` sorts before `zzz`, and the machine login is
// the conventional first fallback — so "first match" and "max" give different
// answers.
func TestEdgeCxpCodexThreadForPaneTakesTheNewestAcrossHomesOnAMiss(t *testing.T) {
	main, slotA, slotZ := edgeCxpPaneHomes(t)
	const cwd = "/wt/cell"

	// the pane's own home has a thread database but no row for this cwd
	bound := filepath.Join(filepath.Dir(slotA), "bound")
	if err := os.MkdirAll(bound, 0o755); err != nil {
		t.Fatal(err)
	}
	edgeCxpThreadsDB(t, bound, []any{"elsewhere", "E", "gpt-6-astra", 1, "/roll/e.jsonl", "/wt/other", 1})

	edgeCxpThreadsDB(t, main, []any{"m", "M", "gpt-6-astra", 1, "/roll/m.jsonl", cwd, 100})
	edgeCxpThreadsDB(t, slotA, []any{"a", "A", "gpt-6-astra", 1, "/roll/a.jsonl", cwd, 200})
	edgeCxpThreadsDB(t, slotZ, []any{"z", "Z", "gpt-6-astra", 1, "/roll/z.jsonl", cwd, 300})

	th, home, ok := codexThreadForPane("CODEX_HOME='"+bound+"' codex", cwd)
	if !ok {
		t.Fatal("codexThreadForPane found nothing although three other homes hold a row for this cwd")
	}
	if th.ID != "z" {
		t.Errorf("ID = %q, want z — the greatest updated_at across the other homes wins, never the first match", th.ID)
	}
	if home != slotZ {
		t.Errorf("home = %q, want %q — the second return is the home the row came from", home, slotZ)
	}
}

// An unreadable home is skipped SILENTLY and costs the scan nothing: `tmp-2`
// really has no state_5.sqlite until codex first runs there, and a database
// caught mid-write is a real state too. One bad home must not hide a good one.
func TestEdgeCxpCodexThreadForPaneSkipsUnreadableHomesSilently(t *testing.T) {
	main, slotA, slotZ := edgeCxpPaneHomes(t)
	const cwd = "/wt/cell"

	// slotA: no database at all. main: corrupt. slotZ: the only good one.
	if err := os.WriteFile(filepath.Join(main, "state_5.sqlite"), []byte("not a sqlite database"), 0o644); err != nil {
		t.Fatal(err)
	}
	edgeCxpThreadsDB(t, slotZ, []any{"z", "Z", "gpt-6-astra", 1, "/roll/z.jsonl", cwd, 300})

	th, home, ok := codexThreadForPane("CODEX_HOME='"+slotA+"' codex", cwd)
	if !ok {
		t.Fatal("codexThreadForPane found nothing — an unreadable home must be skipped, not fatal")
	}
	if th.ID != "z" || home != slotZ {
		t.Errorf("(ID, home) = (%q, %q), want (z, %q)", th.ID, home, slotZ)
	}
}

// A start command that names no home resolves to ~/.codex — every pane that is
// not a bound cell, including a codex carl started by hand. The second return
// names that home explicitly rather than leaving it empty, because the rescue
// compares it against the slot dir to decide whether to place a rollout.
func TestEdgeCxpCodexThreadForPaneFallsBackToTheMachineLogin(t *testing.T) {
	main, _, _ := edgeCxpPaneHomes(t)
	const cwd = "/wt/hand"
	edgeCxpThreadsDB(t, main, []any{"m", "M", "gpt-6-astra", 1, "/roll/m.jsonl", cwd, 100})

	for _, startCmd := range []string{"codex", "", "CODEX_HOME=/unquoted codex", "CLAUDE_SECURESTORAGE_CONFIG_DIR='/x' claude"} {
		th, home, ok := codexThreadForPane(startCmd, cwd)
		if !ok {
			t.Errorf("codexThreadForPane(%q) found nothing; want the ~/.codex row", startCmd)
			continue
		}
		if th.ID != "m" {
			t.Errorf("codexThreadForPane(%q) ID = %q, want m", startCmd, th.ID)
		}
		if home != main {
			t.Errorf("codexThreadForPane(%q) home = %q, want %q", startCmd, home, main)
		}
	}
}

// ---------------------------------------------------------------------------
// readCodexThreadByCWDIn
// ---------------------------------------------------------------------------

// edgeCxpThreadsDB writes <home>/state_5.sqlite with the contract's minimal
// threads schema and closes the writer before returning, so the immutable
// read-only open sees a fully written file.
func edgeCxpThreadsDB(t *testing.T, home string, rows ...[]any) {
	t.Helper()
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", home, err)
	}
	db, err := sql.Open("sqlite3", filepath.Join(home, "state_5.sqlite"))
	if err != nil {
		t.Fatalf("open fixture db: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE threads(
		id TEXT PRIMARY KEY,
		title TEXT,
		model TEXT,
		tokens_used INTEGER,
		rollout_path TEXT,
		cwd TEXT,
		updated_at INTEGER
	)`); err != nil {
		db.Close()
		t.Fatalf("create threads: %v", err)
	}
	for _, r := range rows {
		if _, err := db.Exec(
			`INSERT INTO threads(id,title,model,tokens_used,rollout_path,cwd,updated_at) VALUES(?,?,?,?,?,?,?)`, r...,
		); err != nil {
			db.Close()
			t.Fatalf("insert %v: %v", r, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close fixture db: %v", err)
	}
}

// A home kraken cannot read is skipped SILENTLY, not raised — this lookup runs
// once per known home on every discovery miss, and one slot that codex has
// never started in (`tmp-2` on this machine today has no state_5.sqlite at all)
// must not cost the scan its answer from the homes that do have one.
func TestEdgeCxpReadThreadInDegradesToNotFound(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)

	noDB := filepath.Join(root, "nodb")
	if err := os.MkdirAll(noDB, 0o755); err != nil {
		t.Fatal(err)
	}

	corrupt := filepath.Join(root, "corrupt")
	if err := os.MkdirAll(corrupt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(corrupt, "state_5.sqlite"), []byte("not a sqlite database"), 0o644); err != nil {
		t.Fatal(err)
	}

	noTable := filepath.Join(root, "notable")
	if err := os.MkdirAll(noTable, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", filepath.Join(noTable, "state_5.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE other(id TEXT)`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	populated := filepath.Join(root, "populated")
	edgeCxpThreadsDB(t, populated, []any{"id-a", "A", "gpt-6-astra", 10, "/roll/a.jsonl", "/wt/a", 100})

	for name, home := range map[string]string{
		"a home that does not exist":       filepath.Join(root, "absent"),
		"a home with no database":          noDB,
		"a corrupt database":               corrupt,
		"a database with no threads table": noTable,
		"the empty home":                   "",
	} {
		t.Run(name, func(t *testing.T) {
			_, updated, ok := readCodexThreadByCWDIn(home, "/wt/a")
			if ok || updated != 0 {
				t.Errorf("readCodexThreadByCWDIn = (_, %d, %v), want (zero, 0, false)", updated, ok)
			}
		})
	}

	t.Run("a cwd present in no row", func(t *testing.T) {
		_, updated, ok := readCodexThreadByCWDIn(populated, "/wt/elsewhere")
		if ok || updated != 0 {
			t.Errorf("readCodexThreadByCWDIn = (_, %d, %v), want (zero, 0, false)", updated, ok)
		}
	})
}

// The second return value is the whole reason this variant exists: the caller
// compares it ACROSS homes to take the newest row rather than the first match,
// so within one home it has to be the winning row's own updated_at. Returning a
// constant, or the losing row's, would make a rebound cell keep resolving to
// the stale thread its old slot still lists for the same cwd.
func TestEdgeCxpReadThreadInReturnsTheWinningRowsUpdatedAt(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	home := filepath.Join(root, "slot")

	const shared = "/wt/shared"
	edgeCxpThreadsDB(t, home,
		[]any{"id-old", "Old", "gpt-6-astra", 1, "/roll/old.jsonl", shared, 100},
		[]any{"id-new", "New", "gpt-6-astra", 2, "/roll/new.jsonl", shared, 200},
		[]any{"id-other", "Other", "gpt-6-astra", 3, "/roll/other.jsonl", "/wt/other", 999},
	)

	th, updated, ok := readCodexThreadByCWDIn(home, shared)
	if !ok {
		t.Fatal("readCodexThreadByCWDIn = (_, _, false) for a cwd that has two rows")
	}
	if th.ID != "id-new" {
		t.Errorf("ID = %q, want id-new — the newest row for the cwd wins", th.ID)
	}
	if updated != 200 {
		t.Errorf("updated_at = %d, want 200 — the WINNER's stamp, which is what the cross-home comparison ranks on", updated)
	}
}

// The un-suffixed verb keeps its signature and its meaning, which is what stops
// the pre-existing ~/.codex fixtures from needing an edit: it is exactly the
// new one aimed at the machine login.
func TestEdgeCxpReadThreadByCWDStillMeansTheCodexHome(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	codexHome := filepath.Join(root, ".codex")
	edgeCxpThreadsDB(t, codexHome, []any{"id-main", "Main thread", "gpt-6-astra", 5, "/roll/main.jsonl", "/wt/main", 42})

	direct, ok := readCodexThreadByCWD("/wt/main")
	if !ok {
		t.Fatal("readCodexThreadByCWD = (_, false) for a row in ~/.codex")
	}
	viaIn, updated, okIn := readCodexThreadByCWDIn(codexHome, "/wt/main")
	if !okIn || updated != 42 {
		t.Fatalf("readCodexThreadByCWDIn(~/.codex) = (_, %d, %v), want (_, 42, true)", updated, okIn)
	}
	if direct.ID != viaIn.ID {
		t.Errorf("readCodexThreadByCWD returned %q but the ~/.codex-scoped call returned %q — they must be the same lookup", direct.ID, viaIn.ID)
	}
}

// ---------------------------------------------------------------------------
// ReadTranscriptPath / LastBlocks, for a slot-rooted rollout
// ---------------------------------------------------------------------------

// A discovered RolloutPath is returned with NO database lookup, and the fixture
// proves the negative rather than asserting it: there is no state_5.sqlite
// anywhere under this HOME, so any lookup at all would fail and the call would
// have to answer with an error instead of the path.
func TestEdgeCxpReadTranscriptPathPrefersTheDiscoveredRollout(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const roll = "/pool/accounts/tmp-1/sessions/2026/09/17/rollout-abc.jsonl"

	got, err := ReadTranscriptPath(&Peer{Agent: "codex", Name: "cx", SessionID: "unresolvable-id", RolloutPath: roll})
	if err != nil {
		t.Fatalf("ReadTranscriptPath = %v, want the discovered path with no lookup", err)
	}
	if got != roll {
		t.Errorf("ReadTranscriptPath = %q, want %q", got, roll)
	}
}

// And when the two disagree, the discovered path wins: the session id resolves
// in ~/.codex to a DIFFERENT rollout here, which is exactly the stale row a
// rebound cell leaves behind in the home it came from.
func TestEdgeCxpReadTranscriptPathIgnoresAStaleRowWhenTheRolloutIsKnown(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	edgeCxpThreadsDB(t, filepath.Join(root, ".codex"),
		[]any{"sess-1", "stale", "gpt-6-astra", 1, "/stale/sessions/rollout-stale.jsonl", "/wt/a", 1})

	const fresh = "/pool/accounts/tmp-2/sessions/2026/09/17/rollout-fresh.jsonl"
	got, err := ReadTranscriptPath(&Peer{Agent: "codex", Name: "cx", SessionID: "sess-1", RolloutPath: fresh})
	if err != nil {
		t.Fatalf("ReadTranscriptPath: %v", err)
	}
	if got != fresh {
		t.Errorf("ReadTranscriptPath = %q, want the discovered %q — the database row is the stale one", got, fresh)
	}
}

// The block reader dispatches on the predicate, so a POOL-rooted rollout parses
// as codex exactly as a ~/.codex one does. Before the predicate existed this
// was a `~/.codex`-shaped substring test, which every bound cell's transcript
// fails — and the symptom is an empty detail line on the board, never an error.
func TestEdgeCxpLastBlocksParsesASlotRootedRollout(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)

	p := filepath.Join(root, "codex-pool", "accounts", "tmp-1", "sessions", "2026", "09", "17", "rollout-abc.jsonl")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"timestamp":"t1","type":"event_msg","payload":{"type":"user_message","message":"hello from the pool"}}` + "\n" +
		`{"timestamp":"t2","type":"event_msg","payload":{"type":"agent_message","message":"answered"}}` + "\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if !IsCodexRollout(p) {
		t.Fatalf("fixture is wrong: IsCodexRollout(%q) = false", p)
	}

	blocks, counted, err := LastBlocks(p, 10)
	if err != nil {
		t.Fatalf("LastBlocks on a slot-rooted rollout: %v", err)
	}
	if counted != 2 || len(blocks) != 2 {
		t.Fatalf("counted=%d len=%d, want 2/2 from the codex parser: %+v", counted, len(blocks), blocks)
	}
	if blocks[0].Kind != "user" || blocks[1].Kind != "text" {
		t.Errorf("blocks = %+v, want a user block then an assistant text block", blocks)
	}
}

// ---------------------------------------------------------------------------
// ResumeGuard
// ---------------------------------------------------------------------------

// A codex peer clears the guard on the same four conditions a claude peer does,
// and every OTHER agent value is still refused. The refusal is the load-bearing
// half: the guard stands between discovery and a respawn that SIGKILLs a pane,
// and "codex is allowed now" must not widen into "anything is".
func TestEdgeCxpResumeGuardAdmitsCodexAndStillRefusesEverythingElse(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	ok := &Peer{Agent: "codex", Role: "main", Pane: "%9", SessionID: "cx-session", Name: "Wire the pool"}
	if err := ResumeGuard(ok); err != nil {
		t.Fatalf("ResumeGuard refused a healthy codex peer: %v", err)
	}

	for _, agent := range []string{"", "gemini", "Codex", "CODEX", "aider"} {
		p := *ok
		p.Agent = agent
		if err := ResumeGuard(&p); err == nil {
			t.Errorf("ResumeGuard admitted a peer whose agent is %q; want a refusal", agent)
		}
	}

	t.Run("the four conditions still bind a codex peer", func(t *testing.T) {
		for name, mutate := range map[string]func(p *Peer){
			"a shell pane":         func(p *Peer) { p.Role = "shell" },
			"no pane":              func(p *Peer) { p.Pane = "" },
			"no session id":        func(p *Peer) { p.SessionID = "" },
			"a guessed session id": func(p *Peer) { p.SessionIDGuessed = true },
		} {
			t.Run(name, func(t *testing.T) {
				p := *ok
				mutate(&p)
				if err := ResumeGuard(&p); err == nil {
					t.Errorf("ResumeGuard admitted a codex peer with %s; want a refusal", name)
				}
			})
		}
	})
}
