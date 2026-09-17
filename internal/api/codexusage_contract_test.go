package api

// codexusage_contract_test.go — athena-logic's BLIND tests for
// artifacts/50-contract.md, slice WIRE, Go half. Dimension: LOGIC — the stated
// invariants and the happy paths of the `webUsage` row. The refusals (method,
// malformed state) and the degenerate shapes belong to athena-edge.
//
// Authored blind: internal/api/usage.go's changed region and board.go were
// never opened. The seam names (readUsageSnapshot, readCodexUsageSnapshot,
// usageNow) and the field names (CodexAccounts, CodexFetchedAt) come from the
// contract text; the existing usage_test.go supplied the handler's call shape.
//
// Every package-level identifier declared here is prefixed contractCxp.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"kraken/internal/core"
)

// contractCxpClaudeSnap is the claude payload every case below serves, in the
// deliberately unsorted order the handler is responsible for fixing.
func contractCxpClaudeSnap() core.UsageSnapshot {
	return core.UsageSnapshot{
		FetchedAt: 1_700_000_000_000,
		Accounts: []core.AccountUsage{
			{Slug: "zzz", Email: "zzz@example.com"},
			{Slug: "main", Email: "main@example.com", Current: true},
			{Slug: "aaa", Email: "aaa@example.com"},
		},
	}
}

// contractCxpCodexSnap is the codex payload, likewise unsorted.
func contractCxpCodexSnap() core.UsageSnapshot {
	return core.UsageSnapshot{
		FetchedAt: 1_700_000_777_000,
		Accounts: []core.AccountUsage{
			{Slug: "tmp-2", Email: "slot-two@example.com", Tier: "pro", Session: &core.UsageWindow{Percent: 40}},
			{Slug: "tmp-1", Email: "slot-one@example.com", Tier: "plus", Session: &core.UsageWindow{Percent: 12}, Weekly: &core.UsageWindow{Percent: 12}},
		},
	}
}

// contractCxpServe swaps both snapshot seams plus the clock and returns the
// handler's raw response body.
func contractCxpServe(t *testing.T, claude, codex core.UsageSnapshot, claudeOK, codexOK bool) []byte {
	t.Helper()
	origClaude, origCodex, origNow := readUsageSnapshot, readCodexUsageSnapshot, usageNow
	readUsageSnapshot = func() (core.UsageSnapshot, bool) { return claude, claudeOK }
	readCodexUsageSnapshot = func() (core.UsageSnapshot, bool) { return codex, codexOK }
	usageNow = func() time.Time { return time.UnixMilli(claude.FetchedAt + 5000) }
	t.Cleanup(func() { readUsageSnapshot, readCodexUsageSnapshot, usageNow = origClaude, origCodex, origNow })

	w := httptest.NewRecorder()
	usageHandler(w, httptest.NewRequest(http.MethodGet, "/api/usage", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	return w.Body.Bytes()
}

// ---------------------------------------------------------------------------
// webUsage — the additive codex keys (C7)
// ---------------------------------------------------------------------------

// Obligation: "`accounts` is unchanged in content and order for every input.
// codex_accounts is always a JSON array (never null), sorted by slug."
//
// Both halves are asserted from ONE response, because the failure this pins is
// the two pools mixing: a codex row appearing in `accounts` would be ranked by
// the claude selector and offered a `make main` button for an account that has
// no claude login at all.
func TestContractCxpUsageServesCodexRowsSortedBesideAnUnchangedAccounts(t *testing.T) {
	body := contractCxpServe(t, contractCxpClaudeSnap(), contractCxpCodexSnap(), true, true)

	var got webUsage
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal webUsage: %v — body %s", err, body)
	}

	// `accounts` keeps its own ordering rule: current first, then slug ascending
	wantClaude := []string{"main", "aaa", "zzz"}
	if len(got.Accounts) != len(wantClaude) {
		t.Fatalf("accounts = %+v, want %d rows", got.Accounts, len(wantClaude))
	}
	for i, slug := range wantClaude {
		if got.Accounts[i].Slug != slug {
			t.Fatalf("accounts[%d].Slug = %q, want %q (full order %+v)", i, got.Accounts[i].Slug, slug, got.Accounts)
		}
		if got.Accounts[i].Slug == "tmp-1" || got.Accounts[i].Slug == "tmp-2" {
			t.Fatalf("a codex slot reached `accounts`: %+v", got.Accounts)
		}
	}

	wantCodex := []string{"tmp-1", "tmp-2"}
	if len(got.CodexAccounts) != len(wantCodex) {
		t.Fatalf("codex_accounts = %+v, want %d rows", got.CodexAccounts, len(wantCodex))
	}
	for i, slug := range wantCodex {
		if got.CodexAccounts[i].Slug != slug {
			t.Fatalf("codex_accounts[%d].Slug = %q, want %q (sorted by slug; full %+v)", i, got.CodexAccounts[i].Slug, slug, got.CodexAccounts)
		}
	}
	if got.CodexAccounts[0].Email != "slot-one@example.com" || got.CodexAccounts[0].Tier != "plus" {
		t.Errorf("codex_accounts[0] = %+v, want tmp-1's own identity and tier", got.CodexAccounts[0])
	}
	if got.CodexFetchedAt != 1_700_000_777_000 {
		t.Errorf("codex_fetched_at = %d, want the CODEX snapshot's own stamp 1700000777000 — not the claude one", got.CodexFetchedAt)
	}
	if got.FetchedAt != 1_700_000_000_000 {
		t.Errorf("fetched_at = %d, want the claude snapshot's own stamp — unchanged", got.FetchedAt)
	}
}

// Obligation: "codex_accounts is always a JSON array (never null)". The key is
// read by the board on every poll and by a decode-only iOS build; `null` is
// what turns a `.map` into a crash on a machine that simply has no codex pool.
func TestContractCxpUsageAlwaysEmitsACodexArray(t *testing.T) {
	cases := []struct {
		name             string
		claudeOK, cdxOK  bool
		claude, codexSnp core.UsageSnapshot
	}{
		{"both snapshots present", true, true, contractCxpClaudeSnap(), contractCxpCodexSnap()},
		{"claude only — no codex pool on this machine", true, false, contractCxpClaudeSnap(), core.UsageSnapshot{}},
		{"codex only", false, true, core.UsageSnapshot{}, contractCxpCodexSnap()},
		{"neither", false, false, core.UsageSnapshot{}, core.UsageSnapshot{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := contractCxpServe(t, c.claude, c.codexSnp, c.claudeOK, c.cdxOK)

			var raw map[string]json.RawMessage
			if err := json.Unmarshal(body, &raw); err != nil {
				t.Fatalf("unmarshal: %v — body %s", err, body)
			}
			blob, ok := raw["codex_accounts"]
			if !ok {
				t.Fatalf("the response carries no codex_accounts key at all: %s", body)
			}
			var rows []core.AccountUsage
			if err := json.Unmarshal(blob, &rows); err != nil {
				t.Fatalf("codex_accounts is not an array: %s", blob)
			}
			if string(blob) == "null" {
				t.Fatalf("codex_accounts = null, want an array")
			}
			if c.cdxOK && len(rows) != 2 {
				t.Errorf("codex_accounts holds %d rows, want 2", len(rows))
			}
			if !c.cdxOK && len(rows) != 0 {
				t.Errorf("codex_accounts = %+v with no codex snapshot, want an empty array", rows)
			}

			// `accounts` is likewise an array in every one of these states —
			// the codex keys are additive and change nothing about it.
			if acc, ok := raw["accounts"]; !ok || string(acc) == "null" {
				t.Errorf("accounts = %s, want an array (unchanged by the codex keys)", acc)
			}
		})
	}
}

// Obligation: "apiContract stays 6." The change adds two keys and retypes,
// renames and removes nothing, so every shipped iOS build keeps decoding this
// payload. A bump here would make those builds refuse a response they can
// read perfectly well.
func TestContractCxpApiContractStaysSix(t *testing.T) {
	if apiContract != 6 {
		t.Errorf("apiContract = %d, want 6 — codex_accounts and codex_fetched_at are additive", apiContract)
	}
}
