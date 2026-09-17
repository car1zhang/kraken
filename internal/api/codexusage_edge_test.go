package api

// codexusage_edge_test.go — the EDGE dimension of the `webUsage` rows of slice
// WIRE in artifacts/50-contract.md: the shapes `/api/usage` must produce when
// one, both or neither snapshot is there, the null-versus-empty-array
// distinction every client decodes on, the sort, the cross-pool slug collision,
// and G1 — `accounts` is unchanged in content and order for every input.
//
// Authored BLIND. internal/api/usage.go's changed region was never read by any
// route (Read, cat, grep, sed, git show/diff); neither were
// internal/jobs/codexusage.go or internal/core/codexpool.go. What WAS read: the
// contract, 50-components.md, 52-testing-plan.md, the pre-existing
// internal/api/usage_test.go (for the handler's seams and idioms) and
// internal/core/usagepool.go's pinned wire types, which the contract cites as
// unchanged.
//
// The sibling blind author (athena-logic) owns the stated invariants and the
// happy paths. What lives here is the other half: the empty array that must not
// be null, the `current` flag that must not reorder a codex pool, the claude
// payload that must not move.
//
// Every package-level identifier is prefixed `edgeCxp` and every test is
// `TestEdgeCxp…`.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"kraken/internal/core"
	"kraken/internal/jobs"
)

// edgeCxpUsage swaps every seam the handler reads and isolates HOME, so no test
// here touches the real ~/.kraken, ~/.claude-pool or ~/.codex-pool (G5). Both
// snapshot readers are swapped on every call, including the "neither" case:
// leaving one at its production default would make the assertion depend on
// whatever this machine happens to have on disk.
func edgeCxpUsage(t *testing.T, claude, codex core.UsageSnapshot, claudeOK, codexOK bool) map[string]json.RawMessage {
	t.Helper()
	t.Setenv("HOME", t.TempDir())

	oldClaude, oldCodex, oldNow, oldRotation := readUsageSnapshot, readCodexUsageSnapshot, usageNow, readRotationState
	readUsageSnapshot = func() (core.UsageSnapshot, bool) { return claude, claudeOK }
	readCodexUsageSnapshot = func() (core.UsageSnapshot, bool) { return codex, codexOK }
	usageNow = func() time.Time { return time.UnixMilli(1_700_000_005_000) }
	readRotationState = func() jobs.RotationState { return jobs.RotationState{Mode: "auto"} }
	t.Cleanup(func() {
		readUsageSnapshot, readCodexUsageSnapshot, usageNow, readRotationState = oldClaude, oldCodex, oldNow, oldRotation
	})

	w := httptest.NewRecorder()
	usageHandler(w, httptest.NewRequest(http.MethodGet, "/api/usage", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &keys); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
	return keys
}

// edgeCxpClaudeFixture is the claude snapshot the pre-existing handler test
// uses, kept byte-identical so the G1 assertion below is measured against the
// ordering that suite already pins (current first, then slug ascending).
func edgeCxpClaudeFixture() core.UsageSnapshot {
	return core.UsageSnapshot{
		FetchedAt: 1_700_000_000_000,
		Accounts: []core.AccountUsage{
			{Slug: "zzz"},
			{Slug: "main", Current: true},
			{Slug: "aaa"},
		},
	}
}

func edgeCxpCodexRow(slug, email string, pct float64) core.AccountUsage {
	return core.AccountUsage{
		Slug:      slug,
		Email:     email,
		Tier:      "plus",
		Session:   &core.UsageWindow{Percent: pct},
		Weekly:    &core.UsageWindow{Percent: pct},
		FetchedAt: 1_700_000_000_000,
	}
}

// ---------------------------------------------------------------------------
// the wire struct
// ---------------------------------------------------------------------------

// The two tags are the contract, and each carries a different promise.
// `codex_accounts` has NO omitempty because the key must always be present and
// always an array — a client that reads its absence as "older server" behaves
// differently from one reading an empty array, and only one of those is true.
// `codex_fetched_at` DOES omit, because 0 and absent mean the same thing there
// ("the pass has never run") and sending 0 would render as an epoch timestamp.
func TestEdgeCxpWebUsageTags(t *testing.T) {
	ty := reflect.TypeOf(webUsage{})

	accounts, ok := ty.FieldByName("CodexAccounts")
	if !ok {
		t.Fatal("webUsage has no CodexAccounts field")
	}
	if got := accounts.Tag.Get("json"); got != "codex_accounts" {
		t.Errorf("CodexAccounts json tag = %q, want %q — no omitempty: the key is always present and always an array", got, "codex_accounts")
	}
	if accounts.Type != reflect.TypeOf([]core.AccountUsage{}) {
		t.Errorf("CodexAccounts is a %s, want []core.AccountUsage — the same row object the claude pool serves", accounts.Type)
	}

	fetched, ok := ty.FieldByName("CodexFetchedAt")
	if !ok {
		t.Fatal("webUsage has no CodexFetchedAt field")
	}
	if got := fetched.Tag.Get("json"); got != "codex_fetched_at,omitempty" {
		t.Errorf("CodexFetchedAt json tag = %q, want %q", got, "codex_fetched_at,omitempty")
	}
	if fetched.Type.Kind() != reflect.Int64 {
		t.Errorf("CodexFetchedAt is a %s, want int64 (unix millis, the wire's convention)", fetched.Type)
	}
}

// The change is additive, so the contract version does not move. Bumping it
// would make every shipped iOS build refuse a payload it can decode perfectly
// well — `codex_accounts` is a key those builds simply ignore.
func TestEdgeCxpAPIContractDidNotBump(t *testing.T) {
	if apiContract != 6 {
		t.Errorf("apiContract = %d, want 6 — two additive keys never bump the contract", apiContract)
	}
}

// ---------------------------------------------------------------------------
// codex_accounts is ALWAYS an array
// ---------------------------------------------------------------------------

// Four ways to have no codex rows, one answer: `[]`. A JSON `null` decodes into
// a Go nil slice and a TypeScript `undefined`, and the SPA's `codex_accounts`
// is typed optional precisely so an OLDER server's absence reads as "no codex
// pool" — so a new server emitting null would make "the feature is off" and
// "the server predates the feature" indistinguishable at exactly the moment
// somebody is debugging which one it is.
func TestEdgeCxpCodexAccountsIsNeverNullOrAbsent(t *testing.T) {
	for _, c := range []struct {
		name     string
		snap     core.UsageSnapshot
		ok       bool
		claudeOK bool
	}{
		{"no codex snapshot at all", core.UsageSnapshot{}, false, true},
		{"a codex snapshot with a nil slice", core.UsageSnapshot{FetchedAt: 1_700_000_000_000}, true, true},
		{"a codex snapshot with an empty slice", core.UsageSnapshot{FetchedAt: 1_700_000_000_000, Accounts: []core.AccountUsage{}}, true, true},
		{"neither snapshot available", core.UsageSnapshot{}, false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			keys := edgeCxpUsage(t, edgeCxpClaudeFixture(), c.snap, c.claudeOK, c.ok)

			raw, present := keys["codex_accounts"]
			if !present {
				t.Fatalf("the payload carries no codex_accounts key at all: %v", keys)
			}
			if string(raw) != "[]" {
				t.Errorf("codex_accounts = %s, want [] — never null, never absent", raw)
			}
			var rows []core.AccountUsage
			if err := json.Unmarshal(raw, &rows); err != nil {
				t.Fatalf("codex_accounts does not decode as an array of rows: %v", err)
			}
		})
	}
}

// With no codex pass ever run there is no stamp to send, and omitempty is what
// says so. A 0 on the wire would render as 1970 in any client that formats it.
func TestEdgeCxpCodexFetchedAtIsOmittedWhenZero(t *testing.T) {
	t.Run("no snapshot", func(t *testing.T) {
		keys := edgeCxpUsage(t, edgeCxpClaudeFixture(), core.UsageSnapshot{}, true, false)
		if v, ok := keys["codex_fetched_at"]; ok {
			t.Errorf("codex_fetched_at = %s, want no key at all", v)
		}
	})

	t.Run("a snapshot with a real stamp carries it", func(t *testing.T) {
		snap := core.UsageSnapshot{FetchedAt: 1_700_000_004_000, Accounts: []core.AccountUsage{edgeCxpCodexRow("tmp-1", "tmp-1@example.test", 5)}}
		keys := edgeCxpUsage(t, edgeCxpClaudeFixture(), snap, true, true)
		var got int64
		if err := json.Unmarshal(keys["codex_fetched_at"], &got); err != nil {
			t.Fatalf("decode codex_fetched_at: %v", err)
		}
		if got != 1_700_000_004_000 {
			t.Errorf("codex_fetched_at = %d, want 1700000004000 — the codex pass' own stamp, not the claude pass'", got)
		}
	})
}

// ---------------------------------------------------------------------------
// ordering
// ---------------------------------------------------------------------------

// Codex rows sort by SLUG and nothing else. The claude table hoists the row
// flagged `current` because that pool has a main login; the codex pool has none
// — `CodexSlotUsage` never sets the flag and `CodexPoolSlugs` never emits the
// slug `main` — so a row arriving with either would be a bug upstream, and
// honouring it here would quietly reorder the accounts overlay around a lie.
func TestEdgeCxpCodexAccountsSortBySlugAndIgnoreCurrent(t *testing.T) {
	zzz := edgeCxpCodexRow("zzz", "zzz@example.test", 10)
	mid := edgeCxpCodexRow("main", "main@example.test", 20)
	mid.Current = true // a flag the codex pool never sets; it must not hoist
	aaa := edgeCxpCodexRow("aaa", "aaa@example.test", 30)

	snap := core.UsageSnapshot{FetchedAt: 1_700_000_000_000, Accounts: []core.AccountUsage{zzz, mid, aaa}}
	keys := edgeCxpUsage(t, edgeCxpClaudeFixture(), snap, true, true)

	var rows []core.AccountUsage
	if err := json.Unmarshal(keys["codex_accounts"], &rows); err != nil {
		t.Fatal(err)
	}
	var slugs []string
	for _, r := range rows {
		slugs = append(slugs, r.Slug)
	}
	if !reflect.DeepEqual(slugs, []string{"aaa", "main", "zzz"}) {
		t.Errorf("codex_accounts order = %v, want [aaa main zzz] — sorted by slug, with `current` carrying no weight", slugs)
	}
}

// A single row is still an array, and its fields survive the trip: the overlay
// renders slug, email and tier straight off it, so a handler that rebuilt the
// row into a narrower shape would blank the chips with no error anywhere.
func TestEdgeCxpCodexRowSurvivesTheWireIntact(t *testing.T) {
	row := edgeCxpCodexRow("tmp-1", "tmp-1@example.test", 42)
	row.NoCreds = true
	snap := core.UsageSnapshot{FetchedAt: 1_700_000_000_000, Accounts: []core.AccountUsage{row}}

	keys := edgeCxpUsage(t, edgeCxpClaudeFixture(), snap, true, true)
	var rows []core.AccountUsage
	if err := json.Unmarshal(keys["codex_accounts"], &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("codex_accounts = %+v, want one row", rows)
	}
	got := rows[0]
	if got.Slug != "tmp-1" || got.Email != "tmp-1@example.test" || got.Tier != "plus" {
		t.Errorf("row = %+v, want slug/email/tier carried through", got)
	}
	if got.Session == nil || got.Session.Percent != 42 || got.Weekly == nil || got.Weekly.Percent != 42 {
		t.Errorf("row windows = (%+v, %+v), want both at 42 percent", got.Session, got.Weekly)
	}
	if !got.NoCreds {
		t.Error("NoCreds was dropped in transit — it is what draws the overlay's `no usage yet` state")
	}
}

// ---------------------------------------------------------------------------
// G1 — the claude half does not move
// ---------------------------------------------------------------------------

// The strongest claim about this change is the negative one, so it is asserted
// on the RAW bytes of the `accounts` key rather than on a decoded slice: adding
// codex rows must leave the claude array byte-identical, content and order.
// Anything less and every shipped client's account list quietly reorders the
// first time a codex pool appears on the machine.
//
// The colliding fixture is deliberate: both pools carry a slot named `aaa`,
// which is reachable because a pool slug is a slugified email and one person
// can hold an account in both.
func TestEdgeCxpAddingCodexRowsLeavesTheClaudeArrayByteIdentical(t *testing.T) {
	claude := edgeCxpClaudeFixture()

	without := edgeCxpUsage(t, claude, core.UsageSnapshot{}, true, false)
	with := edgeCxpUsage(t, claude, core.UsageSnapshot{
		FetchedAt: 1_700_000_004_000,
		Accounts: []core.AccountUsage{
			edgeCxpCodexRow("aaa", "someone-else@example.test", 77), // the collision
			edgeCxpCodexRow("tmp-1", "tmp-1@example.test", 5),
		},
	}, true, true)

	if string(without["accounts"]) != string(with["accounts"]) {
		t.Errorf("the accounts array changed when codex rows appeared:\n  without: %s\n  with:    %s",
			without["accounts"], with["accounts"])
	}
	for _, key := range []string{"available", "fetched_at", "age_seconds", "rotation_mode"} {
		if string(without[key]) != string(with[key]) {
			t.Errorf("%s changed when codex rows appeared: %s vs %s", key, without[key], with[key])
		}
	}
}

// The two arrays are separate namespaces, which is the whole reason the wire
// carries two of them rather than one with an `agent` field: the colliding slug
// must resolve to a DIFFERENT row in each, and a handler that merged, deduped or
// overwrote by slug would hand a codex cell a claude account's bars.
func TestEdgeCxpACollidingSlugKeepsBothRows(t *testing.T) {
	claude := core.UsageSnapshot{
		FetchedAt: 1_700_000_000_000,
		Accounts: []core.AccountUsage{
			{Slug: "shared", Email: "shared-claude@example.test", Session: &core.UsageWindow{Percent: 11}},
		},
	}
	codex := core.UsageSnapshot{
		FetchedAt: 1_700_000_004_000,
		Accounts: []core.AccountUsage{
			edgeCxpCodexRow("shared", "shared-codex@example.test", 88),
		},
	}

	keys := edgeCxpUsage(t, claude, codex, true, true)

	var claudeRows, codexRows []core.AccountUsage
	if err := json.Unmarshal(keys["accounts"], &claudeRows); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(keys["codex_accounts"], &codexRows); err != nil {
		t.Fatal(err)
	}
	if len(claudeRows) != 1 || len(codexRows) != 1 {
		t.Fatalf("accounts = %+v, codex_accounts = %+v; want exactly one row each", claudeRows, codexRows)
	}
	if claudeRows[0].Email != "shared-claude@example.test" {
		t.Errorf("accounts[0].Email = %q, want the CLAUDE pool's identity", claudeRows[0].Email)
	}
	if codexRows[0].Email != "shared-codex@example.test" {
		t.Errorf("codex_accounts[0].Email = %q, want the CODEX pool's identity", codexRows[0].Email)
	}
}

// A missing CLAUDE snapshot must not suppress the codex rows, and the reverse.
// The two passes fail independently, and a payload that dropped one pool
// because the other's pass had not run yet would make a cold-start board show
// no accounts at all for however long the slower pass takes.
func TestEdgeCxpEitherSnapshotMissingDoesNotSuppressTheOther(t *testing.T) {
	codex := core.UsageSnapshot{
		FetchedAt: 1_700_000_004_000,
		Accounts:  []core.AccountUsage{edgeCxpCodexRow("tmp-1", "tmp-1@example.test", 5)},
	}

	t.Run("no claude snapshot, codex rows present", func(t *testing.T) {
		keys := edgeCxpUsage(t, core.UsageSnapshot{}, codex, false, true)

		var rows []core.AccountUsage
		if err := json.Unmarshal(keys["codex_accounts"], &rows); err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0].Slug != "tmp-1" {
			t.Errorf("codex_accounts = %+v, want the single tmp-1 row", rows)
		}
		if string(keys["accounts"]) != "[]" {
			t.Errorf("accounts = %s, want [] — an unavailable claude snapshot is an empty claude array, unchanged from today", keys["accounts"])
		}
	})

	t.Run("claude rows present, no codex snapshot", func(t *testing.T) {
		keys := edgeCxpUsage(t, edgeCxpClaudeFixture(), core.UsageSnapshot{}, true, false)

		var rows []core.AccountUsage
		if err := json.Unmarshal(keys["accounts"], &rows); err != nil {
			t.Fatal(err)
		}
		if len(rows) != 3 {
			t.Errorf("accounts = %+v, want the three claude rows", rows)
		}
		if string(keys["codex_accounts"]) != "[]" {
			t.Errorf("codex_accounts = %s, want []", keys["codex_accounts"])
		}
	})
}

// The reader is a package var, for the reason every other reader on this
// handler is one: the production default opens kraken.db, and a handler test
// that could not swap it would either touch the real store or not exist.
func TestEdgeCxpCodexSnapshotReaderIsASwappableSeam(t *testing.T) {
	var _ func() (core.UsageSnapshot, bool) = readCodexUsageSnapshot

	orig := readCodexUsageSnapshot
	t.Cleanup(func() { readCodexUsageSnapshot = orig })

	called := 0
	readCodexUsageSnapshot = func() (core.UsageSnapshot, bool) { called++; return core.UsageSnapshot{}, false }
	if _, ok := readCodexUsageSnapshot(); ok || called != 1 {
		t.Errorf("the swapped seam was not the one called (called=%d, ok=%v)", called, ok)
	}
}
