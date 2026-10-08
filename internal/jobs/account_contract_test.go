package jobs

// account_contract_test.go — athena-logic's BLIND contract tests for
// 50-contract.md, Slice S-jobs: decideBind (the pure decision), boundCounts
// (the distribution input it ranks on), spawnCommand/threadSpawnCommand (the
// ONE delivery seam), and LimitHit.SourceAccount (the record of which account
// was exhausted).
//
// Written from the contract text alone. The working-tree versions of
// internal/jobs/{binding,cell,thread,rotation,limitrescue,limithook}.go were
// never read; the harness (cellDir, UseStore, RecordLimitHit/ReadLimitHit's
// signatures, TestMain's log quarantine) is PRE-EXISTING package state at the
// base commit.
//
// Dimension: LOGIC — the stated invariants and happy paths. The refusal arms
// (a nil job, an unwritable cell dir, a nil itemExists, a store that is not
// wired) are athena-edge's ground and are not probed here.
//
// Two obligations in this slice have NO reachable observer from a test that
// may not read the implementation and may not shell out, and are disclosed
// rather than faked: bindAccount's own impure wrapper (notification, events,
// mutex) and the retirement guard that no spawn path calls RotationGate — the
// latter would need an AST walk over cell.go, which is a byte route into a
// file this author is blind to.
//
// Every package-level identifier declared here is prefixed `contractAcct`.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kraken/internal/core"
	"kraken/internal/store"
)

// contractAcctNow is the fixed clock every call below passes.
var contractAcctNow = time.UnixMilli(1_700_000_000_000)

// contractAcctMainEmail is the machine's main identity in every fixture — the
// one decideBind excludes as the save-back mirror.
const contractAcctMainEmail = "main@example.com"

// contractAcctEnv isolates HOME and points PoolDir() at a scratch pool under
// it, so nothing here reads the real ~/.claude-pool or the real ~/.kraken.
// It returns the pool root, which is also what the env prefix must name.
func contractAcctEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	pool := filepath.Join(home, ".claude-pool")
	if err := os.MkdirAll(pool, 0o755); err != nil {
		t.Fatalf("make scratch pool: %v", err)
	}
	t.Setenv("CLAUDE_POOL_DIR", pool)
	return pool
}

// contractAcctStore opens a scratch store and wires it as the active one.
func contractAcctStore(t *testing.T) *store.Store {
	t.Helper()
	contractAcctEnv(t)
	st, err := store.Open(filepath.Join(t.TempDir(), "kraken.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	UseStore(st)
	t.Cleanup(func() { UseStore(nil); st.Close() })
	return st
}

// contractAcctPoolRow is a fresh, fully eligible pool row.
func contractAcctPoolRow(slug, email string, session float64) core.AccountUsage {
	return core.AccountUsage{
		Slug:      slug,
		Email:     email,
		Session:   &core.UsageWindow{Percent: session},
		FetchedAt: contractAcctNow.UnixMilli() - 60_000,
	}
}

// contractAcctSnap wraps rows into the snapshot shape decideBind takes.
func contractAcctSnap(rows ...core.AccountUsage) core.UsageSnapshot {
	return core.UsageSnapshot{FetchedAt: contractAcctNow.UnixMilli(), Accounts: rows}
}

// contractAcctProbe is the itemExists seam: it records every slot dir it was
// asked about and denies the ones named in deny.
type contractAcctProbe struct {
	seen []string
	deny map[string]bool
}

func (p *contractAcctProbe) exists(slotDir string) bool {
	p.seen = append(p.seen, slotDir)
	return !p.deny[slotDir]
}

// contractAcctAllExist is the probe that passes everything.
func contractAcctAllExist(string) bool { return true }

// contractAcctSlotDir is the exact string the contract says the probe is asked
// about and the env var carries: PoolDir() + "/" + slug, absolute and with no
// trailing slash. It is spelled here ONCE, as the contract requires, so a
// re-derivation elsewhere in the implementation shows up as a mismatch.
func contractAcctSlotDir(slug string) string { return core.PoolDir() + "/" + slug }

// ---------------------------------------------------------------------------
// decideBind
// ---------------------------------------------------------------------------

// TestContractAcctDecideBindManualPinDecidesNothing (Q5): mode "manual"
// returns the fixed refusal BEFORE reading anything else. The pin means kraken
// makes no account decisions at all, so the probe must not be consulted either
// — a keychain probe on a pinned machine is kraken deciding something.
func TestContractAcctDecideBindManualPinDecidesNothing(t *testing.T) {
	contractAcctEnv(t)
	probe := &contractAcctProbe{}
	snap := contractAcctSnap(contractAcctPoolRow("healthy", "healthy@example.com", 5))

	slug, email, reason := decideBind(snap, nil, "manual", contractAcctMainEmail, "healthy", probe.exists, contractAcctNow)
	if slug != "" || email != "" {
		t.Errorf("a manual pin bound (%q, %q), want the empty pair", slug, email)
	}
	if reason != "rotation pinned manual" {
		t.Errorf("reason = %q, want %q", reason, "rotation pinned manual")
	}
	if len(probe.seen) != 0 {
		t.Errorf("the manual pin probed %v — it must return before reading anything else", probe.seen)
	}
}

// TestContractAcctDecideBindPicksTheHealthySlot is the base happy path: a
// non-manual mode, one eligible slot, an existing keychain item. Both the slug
// and the EMAIL come back, because the email is what gets stamped on the job
// row and what the rescue later excludes itself from.
func TestContractAcctDecideBindPicksTheHealthySlot(t *testing.T) {
	contractAcctEnv(t)
	snap := contractAcctSnap(contractAcctPoolRow("alpha", "alpha@example.com", 12))
	slug, email, reason := decideBind(snap, nil, "auto", contractAcctMainEmail, "", contractAcctAllExist, contractAcctNow)
	if slug != "alpha" || email != "alpha@example.com" {
		t.Errorf("decideBind = (%q, %q, %q), want (alpha, alpha@example.com)", slug, email, reason)
	}
}

// TestContractAcctDecideBindExcludesMainAndItsMirror: "candidates are snap's
// rows minus Slug == main; rows whose Email case-folds equal to mainEmail are
// excluded (the save-back mirror)". Both exclusions exist for one reason — a
// cell bound to the machine's own identity is a cell that is not distributed —
// and the case-fold is what stops a differently-cased mirror slot slipping
// through.
func TestContractAcctDecideBindExcludesMainAndItsMirror(t *testing.T) {
	contractAcctEnv(t)
	main := contractAcctPoolRow("main", contractAcctMainEmail, 1)
	mirror := contractAcctPoolRow("save-back", "MAIN@Example.COM", 1)
	other := contractAcctPoolRow("other", "other@example.com", 65)

	slug, email, _ := decideBind(contractAcctSnap(main, mirror, other), nil, "auto", contractAcctMainEmail, "", contractAcctAllExist, contractAcctNow)
	if slug != "other" || email != "other@example.com" {
		t.Errorf("decideBind = (%q, %q), want (other, other@example.com) — main and its mirror are both healthier and both excluded", slug, email)
	}

	slug, email, _ = decideBind(contractAcctSnap(main, mirror), nil, "auto", contractAcctMainEmail, "", contractAcctAllExist, contractAcctNow)
	if slug != "" || email != "" {
		t.Errorf("a pool of main plus its mirror bound (%q, %q), want the empty pair", slug, email)
	}
}

// TestContractAcctDecideBindExcludesSlotsWithNoKeychainItem is Q1's per-bind
// guard. A slot dir whose keychain item is missing cannot serve a credential,
// so binding a cell to it produces a claude that cannot authenticate — the
// self-test that catches both an unseeded slot and a silent semantics change
// in the env var. The reason string is FROZEN by the contract, because it is
// what a bind-skipped event reports.
func TestContractAcctDecideBindExcludesSlotsWithNoKeychainItem(t *testing.T) {
	contractAcctEnv(t)
	probe := &contractAcctProbe{deny: map[string]bool{contractAcctSlotDir("unseeded"): true}}
	// the denied slot is the healthiest, so only the guard can decide this
	snap := contractAcctSnap(
		contractAcctPoolRow("unseeded", "unseeded@example.com", 1),
		contractAcctPoolRow("seeded", "seeded@example.com", 65),
	)

	slug, email, _ := decideBind(snap, nil, "auto", contractAcctMainEmail, "", probe.exists, contractAcctNow)
	if slug != "seeded" || email != "seeded@example.com" {
		t.Errorf("decideBind = (%q, %q), want (seeded, seeded@example.com)", slug, email)
	}

	// the probe is asked about the literal slot dir, spelled exactly once
	var askedAboutUnseeded bool
	for _, dir := range probe.seen {
		if dir == contractAcctSlotDir("unseeded") {
			askedAboutUnseeded = true
		}
		if strings.HasSuffix(dir, "/") {
			t.Errorf("probed %q — the slot dir carries no trailing slash (the keychain service name hashes the raw string)", dir)
		}
	}
	if !askedAboutUnseeded {
		t.Errorf("the probe was asked about %v, want it to include %q", probe.seen, contractAcctSlotDir("unseeded"))
	}
}

// TestContractAcctDecideBindNamesTheKeychainGuardInItsReason pins the frozen
// reason text on the path where it is the ONLY output: every candidate denied.
func TestContractAcctDecideBindNamesTheKeychainGuardInItsReason(t *testing.T) {
	contractAcctEnv(t)
	deny := func(string) bool { return false }
	slug, email, reason := decideBind(
		contractAcctSnap(contractAcctPoolRow("only", "only@example.com", 5)),
		nil, "auto", contractAcctMainEmail, "", deny, contractAcctNow)
	if slug != "" || email != "" {
		t.Errorf("an unprobeable pool bound (%q, %q), want the empty pair", slug, email)
	}
	if !strings.Contains(reason, "only: no keychain item") {
		t.Errorf("reason = %q, want it to contain %q", reason, "only: no keychain item")
	}
}

// TestContractAcctDecideBindDelegatesGatesAndRanking: decideBind owns the
// three exclusions above and DELEGATES everything else. The two observable
// consequences are asserted here — a candidate that trips a rotation gate is
// never picked, and the bound-count key outranks the session % — so a
// decideBind that re-implemented the ranking instead of calling
// SelectBindTarget would have to reproduce both.
func TestContractAcctDecideBindDelegatesGatesAndRanking(t *testing.T) {
	contractAcctEnv(t)

	gated := core.AccountUsage{
		Slug: "gated", Email: "gated@example.com",
		Session:   &core.UsageWindow{Percent: 1},
		FetchedAt: contractAcctNow.UnixMilli() - 60_000,
		Reauth:    true,
	}
	healthy := contractAcctPoolRow("healthy", "healthy@example.com", 60)
	slug, _, _ := decideBind(contractAcctSnap(gated, healthy), nil, "auto", contractAcctMainEmail, "", contractAcctAllExist, contractAcctNow)
	if slug != "healthy" {
		t.Errorf("a reauth-required slot was picked (%q) — the rotation gates must still apply", slug)
	}

	busy := contractAcctPoolRow("busy", "busy@example.com", 1)
	idle := contractAcctPoolRow("idle", "idle@example.com", 70)
	slug, _, _ = decideBind(contractAcctSnap(busy, idle), map[string]int{"busy": 1}, "auto", contractAcctMainEmail, "", contractAcctAllExist, contractAcctNow)
	if slug != "idle" {
		t.Errorf("decideBind picked %q, want idle — fewest live bound cells outranks the session %%", slug)
	}
}

// TestContractAcctDecideBindKeepsAnEligiblePreviousAccount (Q6): stickiness is
// passed through, which is what makes a reactivated job resume on the account
// it ran on rather than being shuffled onto whatever is emptiest tonight.
func TestContractAcctDecideBindKeepsAnEligiblePreviousAccount(t *testing.T) {
	contractAcctEnv(t)
	prev := contractAcctPoolRow("prev", "prev@example.com", 70)
	better := contractAcctPoolRow("better", "better@example.com", 1)
	snap := contractAcctSnap(prev, better)

	slug, email, _ := decideBind(snap, map[string]int{"prev": 2}, "auto", contractAcctMainEmail, "prev", contractAcctAllExist, contractAcctNow)
	if slug != "prev" || email != "prev@example.com" {
		t.Errorf("decideBind = (%q, %q), want the sticky (prev, prev@example.com)", slug, email)
	}

	// without the previous slug the same inputs pick the other row, which is
	// what makes the assertion above about stickiness rather than ordering
	slug, _, _ = decideBind(snap, map[string]int{"prev": 2}, "auto", contractAcctMainEmail, "", contractAcctAllExist, contractAcctNow)
	if slug != "better" {
		t.Errorf("decideBind with no prevSlug picked %q, want better", slug)
	}
}

// TestContractAcctDecideBindOnADryPoolIsNotAFailure (Q4): an empty pool and an
// all-excluded pool both answer with the empty pair. There is no error return
// at all, by design — the binder must never block or fail a spawn, exactly
// RotationGate's contract, and an unbound cell simply runs on the main lane.
func TestContractAcctDecideBindOnADryPoolIsNotAFailure(t *testing.T) {
	contractAcctEnv(t)
	slug, email, _ := decideBind(contractAcctSnap(), nil, "auto", contractAcctMainEmail, "", contractAcctAllExist, contractAcctNow)
	if slug != "" || email != "" {
		t.Errorf("an empty snapshot bound (%q, %q), want the empty pair", slug, email)
	}

	gated := core.AccountUsage{
		Slug: "gated", Email: "gated@example.com",
		Session:   &core.UsageWindow{Percent: 95},
		FetchedAt: contractAcctNow.UnixMilli() - 60_000,
	}
	slug, email, reason := decideBind(contractAcctSnap(gated), nil, "auto", contractAcctMainEmail, "", contractAcctAllExist, contractAcctNow)
	if slug != "" || email != "" {
		t.Errorf("an all-gated snapshot bound (%q, %q), want the empty pair", slug, email)
	}
	if !strings.Contains(reason, "gated") {
		t.Errorf("reason = %q, want it to name the excluded slug", reason)
	}
}

// ---------------------------------------------------------------------------
// boundCounts
// ---------------------------------------------------------------------------

// TestContractAcctBoundCountsCountsLiveBoundJobsPerSlug is the distribution
// input the whole ranking rests on. Three rules in one table: a bound job with
// a live cell counts, a bound job whose cell is gone does not (a torn-down
// cell holds no credential, so counting it would push new work off a slot that
// is actually free), and an unbound job contributes nothing anywhere.
func TestContractAcctBoundCountsCountsLiveBoundJobsPerSlug(t *testing.T) {
	contractAcctEnv(t)
	live := map[string]bool{"a1": true, "a2": true, "b1": true, "u1": true}
	rows := []*Job{
		{Slug: "a1", AccountSlug: "alpha"},
		{Slug: "a2", AccountSlug: "alpha"},
		// a3 is bound to alpha but its cell is gone
		{Slug: "a3", AccountSlug: "alpha"},
		{Slug: "b1", AccountSlug: "beta"},
		// gamma has a bound job and no live cell at all
		{Slug: "c1", AccountSlug: "gamma"},
		// u1 is live but unbound: it runs on the main lane
		{Slug: "u1"},
	}

	got := boundCounts(rows, func(j *Job) bool { return live[j.Slug] })
	for _, c := range []struct {
		slug string
		want int
	}{
		{"alpha", 2},
		{"beta", 1},
		{"gamma", 0},
		{"", 0},
	} {
		if got[c.slug] != c.want {
			t.Errorf("boundCounts[%q] = %d, want %d (full map %v)", c.slug, got[c.slug], c.want, got)
		}
	}
}

// TestContractAcctBoundCountsOfNothingCountsNothing: the ordinary state of a
// machine with no live cells, and the input the very first bind of the day
// ranks on.
func TestContractAcctBoundCountsOfNothingCountsNothing(t *testing.T) {
	contractAcctEnv(t)
	got := boundCounts(nil, func(*Job) bool { return true })
	if len(got) != 0 {
		t.Errorf("boundCounts(nil) = %v, want no counted slugs", got)
	}

	got = boundCounts([]*Job{{Slug: "x", AccountSlug: "alpha"}}, func(*Job) bool { return false })
	if got["alpha"] != 0 {
		t.Errorf("boundCounts with nothing live = %v, want alpha at 0", got)
	}
}

// ---------------------------------------------------------------------------
// spawnCommand — the ONE delivery seam
// ---------------------------------------------------------------------------

// TestContractAcctSpawnCommandPrefixesTheBoundSlotDir is the whole delivery
// mechanism in one assertion, and it is written as a RELATION between the two
// commands rather than against a literal: the contract's two obligations are
// "begins with CLAUDE_SECURESTORAGE_CONFIG_DIR='<PoolDir()>/<slug>' " and "an
// unbound command is byte-identical to today's", and equality against
// prefix+unbound is exactly both — one space before the launcher, single
// quotes, no trailing slash, and nothing else about the command touched.
//
// The var has to ride the command STRING rather than the process environment:
// tmux respawn-pane does not build a new pane's env from the issuing client's
// (measured, 0 of 20 env lines), so an exec.Cmd.Env would reach the spawn
// paths and silently miss the rescue's respawn.
func TestContractAcctSpawnCommandPrefixesTheBoundSlotDir(t *testing.T) {
	contractAcctEnv(t)
	j := &Job{ID: 41, Slug: "acct-spawn"}
	if err := os.MkdirAll(cellDir(j), 0o755); err != nil {
		t.Fatalf("make cell dir: %v", err)
	}
	argv := []string{"claude", "--model", "claude-fable-5"}

	unbound, err := spawnCommand(j, argv, "brief text", "prompt text")
	if err != nil {
		t.Fatalf("spawnCommand unbound: %v", err)
	}
	if strings.Contains(unbound, "CLAUDE_SECURESTORAGE_CONFIG_DIR") {
		t.Fatalf("an unbound spawn carries the env var: %q", unbound)
	}

	j.AccountSlug = "slot-a"
	bound, err := spawnCommand(j, argv, "brief text", "prompt text")
	if err != nil {
		t.Fatalf("spawnCommand bound: %v", err)
	}
	want := "CLAUDE_SECURESTORAGE_CONFIG_DIR='" + contractAcctSlotDir("slot-a") + "' " + unbound
	if bound != want {
		t.Errorf("bound spawn command =\n  %q\nwant\n  %q", bound, want)
	}
}

// TestContractAcctSpawnCommandRebindCarriesTheNewSlot: the value is derived
// from the job row at the moment of EACH spawn, which is what makes a rebind
// need no tmux set-environment — the rescue stamps the row and the respawn
// that follows reads it.
func TestContractAcctSpawnCommandRebindCarriesTheNewSlot(t *testing.T) {
	contractAcctEnv(t)
	j := &Job{ID: 43, Slug: "acct-rebind"}
	if err := os.MkdirAll(cellDir(j), 0o755); err != nil {
		t.Fatalf("make cell dir: %v", err)
	}
	argv := []string{"claude", "-r", "sess-1"}

	j.AccountSlug = "slot-a"
	first, err := spawnCommand(j, argv, "", "")
	if err != nil {
		t.Fatalf("spawnCommand first bind: %v", err)
	}
	j.AccountSlug = "slot-b"
	second, err := spawnCommand(j, argv, "", "")
	if err != nil {
		t.Fatalf("spawnCommand after the rebind: %v", err)
	}

	if !strings.HasPrefix(first, "CLAUDE_SECURESTORAGE_CONFIG_DIR='"+contractAcctSlotDir("slot-a")+"' ") {
		t.Errorf("first spawn = %q, want the slot-a prefix", first)
	}
	if !strings.HasPrefix(second, "CLAUDE_SECURESTORAGE_CONFIG_DIR='"+contractAcctSlotDir("slot-b")+"' ") {
		t.Errorf("respawn after a rebind = %q, want the slot-b prefix", second)
	}
	if strings.Contains(second, contractAcctSlotDir("slot-a")) {
		t.Errorf("respawn after a rebind still names the old slot: %q", second)
	}
}

// TestContractAcctThreadSpawnCommandAppliesTheIdenticalRule: a side thread
// reads the same worktree and belongs to the same cell, so it must bill the
// same account. Same relational assertion as zeus' — identical inputs, one
// differing binding.
func TestContractAcctThreadSpawnCommandAppliesTheIdenticalRule(t *testing.T) {
	contractAcctEnv(t)
	j := &Job{ID: 42, Slug: "acct-thread"}
	if err := os.MkdirAll(cellDir(j), 0o755); err != nil {
		t.Fatalf("make cell dir: %v", err)
	}

	unbound, err := threadSpawnCommand(j, "1", "why is this blocked", "seed text", "", "ask", "", nil)
	if err != nil {
		t.Fatalf("threadSpawnCommand unbound: %v", err)
	}
	if strings.Contains(unbound, "CLAUDE_SECURESTORAGE_CONFIG_DIR") {
		t.Fatalf("an unbound thread spawn carries the env var: %q", unbound)
	}

	j.AccountSlug = "slot-a"
	bound, err := threadSpawnCommand(j, "1", "why is this blocked", "seed text", "", "ask", "", nil)
	if err != nil {
		t.Fatalf("threadSpawnCommand bound: %v", err)
	}
	want := "CLAUDE_SECURESTORAGE_CONFIG_DIR='" + contractAcctSlotDir("slot-a") + "' " + unbound
	if bound != want {
		t.Errorf("bound thread spawn command =\n  %q\nwant\n  %q", bound, want)
	}
}

// ---------------------------------------------------------------------------
// LimitHit.SourceAccount
// ---------------------------------------------------------------------------

// TestContractAcctRecordLimitHitStampsTheBoundAccount: the record now says
// which account was EXHAUSTED, not just where the rescue went. Stamped from
// the job's AccountEmail at record time.
func TestContractAcctRecordLimitHitStampsTheBoundAccount(t *testing.T) {
	contractAcctStore(t)
	j := &Job{ID: 7, Slug: "bound", AccountSlug: "slot-a", AccountEmail: "one@example.com"}

	hit, err := RecordLimitHit(j, "rate_limit", "session limit reached", "sess-1", "", contractAcctNow)
	if err != nil {
		t.Fatalf("RecordLimitHit: %v", err)
	}
	if hit.SourceAccount != "one@example.com" {
		t.Errorf("SourceAccount = %q, want %q", hit.SourceAccount, "one@example.com")
	}
	if got := ReadLimitHit(j); got.SourceAccount != "one@example.com" {
		t.Errorf("stored SourceAccount = %q, want %q", got.SourceAccount, "one@example.com")
	}
}

// TestContractAcctRecordLimitHitOnAnUnboundCellLeavesTheSourceEmpty: empty IS
// the main lane, the same spelling the binding column and the wire both use.
// A synthesised placeholder here would make an unbound cell's exhausted
// account unreadable from its own record.
func TestContractAcctRecordLimitHitOnAnUnboundCellLeavesTheSourceEmpty(t *testing.T) {
	contractAcctStore(t)
	j := &Job{ID: 8, Slug: "unbound"}

	hit, err := RecordLimitHit(j, "rate_limit", "session limit reached", "sess-1", "", contractAcctNow)
	if err != nil {
		t.Fatalf("RecordLimitHit: %v", err)
	}
	if hit.SourceAccount != "" {
		t.Errorf("SourceAccount = %q on an unbound cell, want \"\" (the main lane)", hit.SourceAccount)
	}
}

// TestContractAcctLimitHitFoldKeepsTheOriginalSourceAccount: one exhausted
// window fails the lead and every teammate within seconds, and the rescue may
// already have rebound the job by the time the last hook lands. A fold that
// re-stamped from the job's CURRENT email would rewrite the record to name the
// account the cell was rescued ONTO — the exact opposite of what the field is
// for, and undetectable afterwards.
func TestContractAcctLimitHitFoldKeepsTheOriginalSourceAccount(t *testing.T) {
	contractAcctStore(t)
	j := &Job{ID: 9, Slug: "folder", AccountSlug: "slot-a", AccountEmail: "one@example.com"}

	first, err := RecordLimitHit(j, "rate_limit", "session limit reached", "sess-1", "", contractAcctNow)
	if err != nil {
		t.Fatalf("RecordLimitHit first delivery: %v", err)
	}

	// the rescue rebinds the cell between the two deliveries
	j.AccountSlug, j.AccountEmail = "slot-b", "two@example.com"

	second, err := RecordLimitHit(j, "rate_limit", "session limit reached", "sess-1", "teammate-1", contractAcctNow.Add(time.Second))
	if err != nil {
		t.Fatalf("RecordLimitHit folded delivery: %v", err)
	}
	if second.Deliveries != first.Deliveries+1 {
		t.Fatalf("deliveries = %d after the second hook, want %d — the two must fold onto one hit for this assertion to mean anything",
			second.Deliveries, first.Deliveries+1)
	}
	if second.SourceAccount != "one@example.com" {
		t.Errorf("SourceAccount = %q after a fold, want the exhausted %q", second.SourceAccount, "one@example.com")
	}
	if got := ReadLimitHit(j); got.SourceAccount != "one@example.com" {
		t.Errorf("stored SourceAccount = %q after a fold, want %q", got.SourceAccount, "one@example.com")
	}
}
