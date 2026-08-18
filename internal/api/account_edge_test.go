package api

import (
	"encoding/json"
	"reflect"
	"testing"

	"kraken/internal/jobs"
	"kraken/internal/store"
)

// account_edge_test.go — the EDGE dimension of 50-contract.md's Slice S-ui
// WIRE obligations for the per-cell account binding: the shapes a client must
// never receive. Absence is the only spelling of "unbound" (Q7), an archived
// card withholds the key outright, and the inner object — once present — is
// complete unconditionally.
//
// Written blind from the frozen contract. internal/api/board.go was never read
// in its working-tree state; useTestStore, mustJSON and archivedWebJob's
// four-argument signature are pre-existing package state read at the base
// commit.
//
// The sibling blind author (athena-logic) owns the round trip and the
// present-when-bound rule. What lives here is the other half: the ABSENT keys,
// the withholding, and the contract number that must not move.
//
// Package-level identifiers are prefixed `edgeAcct` — this package's test
// namespace already carries `edgeJGW*`, `edgeGoal*` and several other `edge*`
// families, and a second blind author is writing beside this file right now.

// edgeAcctKeys marshals a card and returns its top-level keys as raw messages,
// because the contract's whole claim here is key PRESENCE: a typed decode with
// omitempty would smooth an absent key and a zeroed object into one answer.
func edgeAcctKeys(t *testing.T, wj webJob) map[string]json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(wj)
	if err != nil {
		t.Fatalf("marshal webJob: %v", err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return keys
}

// ---------------------------------------------------------------------------
// the tags, which are where the two absence rules are actually implemented
// ---------------------------------------------------------------------------

// TestEdgeAccountCardKeyIsAnOmitemptyPointer. Absence is the wire's ONLY
// spelling of "unbound" (Q7), and a pointer with omitempty is the only shape
// that spells it: a value type would serve `"account":{"slug":"",...}` on
// every unbound card, which two clients would then have to learn to read as
// "not bound" — a second state meaning the same thing as the missing key,
// arrived at by a different route. buildWebBoard drags in tmux/process-table
// discovery and cannot be called from a test, so the tag is where this is
// reachable (the same reason jobload_test.go and jobgoalswire_edge_test.go
// both assert their card keys this way).
func TestEdgeAccountCardKeyIsAnOmitemptyPointer(t *testing.T) {
	f, ok := reflect.TypeOf(webJob{}).FieldByName("Account")
	if !ok {
		t.Fatal("webJob has no Account field")
	}
	if got := f.Tag.Get("json"); got != "account,omitempty" {
		t.Errorf("webJob.Account json tag = %q, want %q", got, "account,omitempty")
	}
	if f.Type.Kind() != reflect.Ptr {
		t.Errorf("webJob.Account is a %s, want a pointer — only a nil pointer marshals to no key at all", f.Type.Kind())
	}
}

// TestEdgeAccountInnerFieldsCarryNoOmitempty is the opposite rule one level in,
// and it is opposite for a reason: the OUTER key answers "is this cell bound",
// so its absence is meaningful, while the inner fields are read only once the
// object is already there. An omitempty on `email` or `at` would make a slot
// with no recorded identity, or a bind stamped at the epoch, arrive as a
// partial object — and the chip's fallback ("show the slug when the email is
// empty") is a decision about an empty STRING, which a client can only take if
// the key is always there to be empty.
func TestEdgeAccountInnerFieldsCarryNoOmitempty(t *testing.T) {
	typ := reflect.TypeOf(webJobAccount{})
	for _, c := range []struct {
		field string
		tag   string
		kind  reflect.Kind
	}{
		{"Slug", "slug", reflect.String},
		{"Email", "email", reflect.String},
		{"At", "at", reflect.Int64},
	} {
		f, ok := typ.FieldByName(c.field)
		if !ok {
			t.Errorf("webJobAccount has no %s field", c.field)
			continue
		}
		if got := f.Tag.Get("json"); got != c.tag {
			t.Errorf("webJobAccount.%s json tag = %q, want %q — no omitempty: a present object is readable unconditionally",
				c.field, got, c.tag)
		}
		if f.Type.Kind() != c.kind {
			t.Errorf("webJobAccount.%s is a %s, want a %s", c.field, f.Type.Kind(), c.kind)
		}
	}
	if typ.NumField() != 3 {
		t.Errorf("webJobAccount has %d fields, want exactly 3 (slug, email, at)", typ.NumField())
	}
}

// ---------------------------------------------------------------------------
// the two boundary cards
// ---------------------------------------------------------------------------

// TestEdgeAccountUnboundCardCarriesNoKeyAndABoundOneIsComplete pins both ends
// of the pointer rule against real serialized bytes rather than against the
// tag alone: an unbound card serves no `account` key at all, and a card whose
// pointer is set serves all three inner keys even when every one of them holds
// its zero value — the case that would otherwise silently disappear, since
// `{"slug":"","email":"","at":0}` is exactly what an omitempty run would
// reduce to `{}`.
func TestEdgeAccountUnboundCardCarriesNoKeyAndABoundOneIsComplete(t *testing.T) {
	t.Run("unbound: no key", func(t *testing.T) {
		keys := edgeAcctKeys(t, webJob{Slug: "x", Repo: "kraken", State: "working"})
		if v, ok := keys["account"]; ok {
			t.Errorf("an unbound card serialized account = %s; absence is the only unbound spelling", v)
		}
	})

	t.Run("present but zeroed: every inner key survives", func(t *testing.T) {
		keys := edgeAcctKeys(t, webJob{Slug: "x", Account: &webJobAccount{}})
		raw, ok := keys["account"]
		if !ok {
			t.Fatal("a card with a non-nil Account served no account key")
		}
		var inner map[string]json.RawMessage
		if err := json.Unmarshal(raw, &inner); err != nil {
			t.Fatalf("decode %s: %v", raw, err)
		}
		for _, key := range []string{"slug", "email", "at"} {
			if _, ok := inner[key]; !ok {
				t.Errorf("a present (zeroed) account object dropped %q: %s — the object is complete or it is absent, never partial", key, raw)
			}
		}
	})

	t.Run("a real binding round-trips every field", func(t *testing.T) {
		want := webJobAccount{Slug: "slot-a", Email: "a@example.com", At: 1_760_000_000_000}
		keys := edgeAcctKeys(t, webJob{Slug: "x", Account: &want})
		var got webJobAccount
		if err := json.Unmarshal(keys["account"], &got); err != nil {
			t.Fatalf("decode account: %v", err)
		}
		if got != want {
			t.Errorf("account = %+v, want %+v", got, want)
		}
	})
}

// TestEdgeAccountArchivedCardWithholdsTheKey. The binding is FORWARD-looking —
// it says where this cell's next claude will bill — which is the opposite class
// from `tokens`, whose whole value is retrospective and which an archived card
// therefore keeps. An archived job has no cell and cannot have one again
// without being unarchived first, so a badge on its card asserts a live fact
// about a job that has none; the row keeps the columns (that is what makes
// unarchiving restore it), and the WIRE is where it is withheld.
func TestEdgeAccountArchivedCardWithholdsTheKey(t *testing.T) {
	st := useTestStore(t)
	row := store.Job{Slug: "acct-archived", Repo: "kraken", State: "done"}
	id, err := st.CreateJob(&row)
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if err := st.SetJobAccount(id, "slot-a", "a@example.com", 1_760_000_000_000); err != nil {
		t.Fatalf("SetJobAccount: %v", err)
	}
	if _, err := st.ArchiveJob(id); err != nil {
		t.Fatalf("ArchiveJob: %v", err)
	}

	archived := jobs.LoadArchivedJobs()
	if len(archived) != 1 {
		t.Fatalf("LoadArchivedJobs = %d jobs, want 1", len(archived))
	}
	a := archived[0]
	if a.AccountSlug != "slot-a" {
		t.Fatalf("the archived ROW lost its binding (AccountSlug %q) — the row keeps it and the wire withholds it, so this test cannot say anything yet",
			a.AccountSlug)
	}

	tokens := map[int64]store.JobTokens{a.ID: {JobID: a.ID, In: 1, Out: 2}}
	wj := archivedWebJob(a, st, tokens, nil)

	if wj.Account != nil {
		t.Errorf("archived card carries account = %+v, want no key at all", wj.Account)
	}
	if v, ok := edgeAcctKeys(t, wj)["account"]; ok {
		t.Errorf("archived card serialized account = %s", v)
	}
	if wj.Tokens == nil {
		t.Error("archived card dropped tokens — the control for this test just broke; retrospective cost is deliberately KEPT")
	}
}

// TestEdgeAccountContractStaysAtSix. Every key this slice adds is additive — a
// new optional object on webJob and nothing retyped, renamed or removed — and
// `apiContract` is mirrored by APIClient.swift's `expectedContract`, so a bump
// here without the same commit moving Swift makes every shipped iOS build
// refuse the whole board payload rather than ignore one key it does not know.
func TestEdgeAccountContractStaysAtSix(t *testing.T) {
	if apiContract != 6 {
		t.Errorf("apiContract = %d, want 6 — webJob.account is additive", apiContract)
	}
}
