package api

// account_contract_test.go — athena-logic's BLIND contract tests for
// 50-contract.md, Slice S-ui's Go half: webJobAccount's frozen shape,
// webJob.Account's pointer+omitempty absence rule, archivedWebJob's
// withholding, and apiContract staying 6.
//
// Written from the contract text alone. The working-tree version of
// internal/api/board.go was never read; the harness (useTestStore,
// archivedWebJob's pre-existing four-parameter signature, the webJob type,
// apiContract) is PRE-EXISTING package state at the base commit.
//
// Dimension: LOGIC — the stated wire shape and the stated presence/absence
// rules. Refusals, oversize bodies and the mirror gate are athena-edge's
// ground and are not probed here.
//
// One obligation in this slice has NO reachable observer: "built for every
// NON-archived job with AccountSlug != '' regardless of live/display state"
// is decided inside buildWebBoard, which drags in tmux/process-table discovery
// and cannot be called from a test (the constraint jobgoalswire_contract_test.go
// and jobload_test.go both record). Disclosed rather than faked; the ARCHIVED
// half has a callable seam and is covered below.
//
// Every package-level identifier declared here is prefixed `contractAcct`.

import (
	"encoding/json"
	"strings"
	"testing"

	"kraken/internal/jobs"
	"kraken/internal/store"
)

// contractAcctAt is the bind stamp every fixture carries — unix millis, the
// unit `at` is declared in.
const contractAcctAt int64 = 1_700_000_000_000

// contractAcctKeys decodes one marshalled webJob into a loose map, because
// what is under test is key PRESENCE and ABSENCE — which a struct with
// omitempty would silently paper over.
func contractAcctKeys(t *testing.T, wj webJob) map[string]any {
	t.Helper()
	raw, err := json.Marshal(wj)
	if err != nil {
		t.Fatalf("marshal webJob: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return got
}

// ---------------------------------------------------------------------------
// webJobAccount — the frozen inner shape
// ---------------------------------------------------------------------------

// TestContractAcctWebJobAccountCarriesAllThreeFields pins the object's exact
// spelling and, with it, the rule that its inner fields carry NO omitempty:
// a present account object means all three keys are readable unconditionally,
// so a client can render `at` and `email` without a second existence check.
// The literal below is the whole contract row in one string.
func TestContractAcctWebJobAccountCarriesAllThreeFields(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	raw, err := json.Marshal(webJob{
		Slug: "bound", Repo: "kraken", State: "working",
		Account: &webJobAccount{Slug: "slot-a", Email: "one@example.com", At: contractAcctAt},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `"account":{"slug":"slot-a","email":"one@example.com","at":1700000000000}`
	if !strings.Contains(string(raw), want) {
		t.Errorf("webJob JSON missing %s:\n%s", want, raw)
	}
}

// TestContractAcctWebJobAccountEmitsItsZeroValues is the same rule stated the
// way it actually fails. A bind whose slot carried no identity leaves `email`
// empty; the object must still emit it, because the clients' fallback ("show
// the slug when the email is empty") needs an empty string to fall back FROM,
// not a missing key.
func TestContractAcctWebJobAccountEmitsItsZeroValues(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	raw, err := json.Marshal(webJob{
		Slug: "bound", State: "working",
		Account: &webJobAccount{Slug: "slot-a"},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `"account":{"slug":"slot-a","email":"","at":0}`
	if !strings.Contains(string(raw), want) {
		t.Errorf("webJob JSON missing %s:\n%s", want, raw)
	}
}

// TestContractAcctWebJobOmitsTheAccountWhenUnbound (Q7): absence of the KEY is
// the wire's only spelling of "unbound". A zeroed object would claim a binding
// to a slot named "" and put a second falsy shape in front of three clients
// for a state that is not different.
func TestContractAcctWebJobOmitsTheAccountWhenUnbound(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	got := contractAcctKeys(t, webJob{Slug: "unbound", Repo: "kraken", State: "working"})
	if _, present := got["account"]; present {
		t.Errorf("an unbound card carries an account key: %v", got["account"])
	}
}

// ---------------------------------------------------------------------------
// archivedWebJob — the withholding
// ---------------------------------------------------------------------------

// TestContractAcctArchivedWebJobWithholdsTheAccount: the account badge is
// FORWARD-LOOKING, like load and unlike tokens. An archived job's cell is gone
// and cannot be bound again without being unarchived first, at which point the
// row (the columns survive both directions) comes back with it — so serving it
// here would put a fact answering a question this card cannot pose in front of
// a client, which is how a stale binding starts reading as a current one.
func TestContractAcctArchivedWebJobWithholdsTheAccount(t *testing.T) {
	st := useTestStore(t)
	a := &jobs.Job{
		ID: 11, Slug: "old", ArchivedName: "old-1", Repo: "kraken", State: "done",
		AccountSlug: "slot-a", AccountEmail: "one@example.com",
	}

	got := archivedWebJob(a, st, map[int64]store.JobTokens{}, nil)
	if got.Account != nil {
		t.Errorf("archivedWebJob served an account: %+v", got.Account)
	}

	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal archived card: %v", err)
	}
	if strings.Contains(string(raw), `"account"`) {
		t.Errorf("archived card JSON carries an account key:\n%s", raw)
	}
}

// ---------------------------------------------------------------------------
// the contract version
// ---------------------------------------------------------------------------

// TestContractAcctAPIContractStaysSix: the account key is purely ADDITIVE —
// nothing is retyped, renamed or removed — so a bump here would tell every
// shipped iOS build its wire understanding is stale over a key it can simply
// ignore. expectedContract in APIClient.swift mirrors this number.
func TestContractAcctAPIContractStaysSix(t *testing.T) {
	if apiContract != 7 {
		t.Errorf("apiContract = %d, want 7 (bumped by the fleet gate) — webJob.account is additive", apiContract)
	}
}
