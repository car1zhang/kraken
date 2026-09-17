package jobs

// codexpool_contract_test.go — athena-logic's BLIND tests for
// artifacts/50-contract.md, slice JOBS. Dimension: LOGIC — the stated
// invariants and the happy paths of every row in the slice. The refusals,
// the malformed inputs and the G4 degradations belong to athena-edge and are
// deliberately absent.
//
// Authored blind. internal/jobs/codexusage.go was never opened by any route,
// nor were the changed regions of binding.go / cell.go / limitrescue.go /
// status.go / thread.go. The harness this file leans on (useTestStore,
// mustCreateJob, cellDir, CellSession, CellArtifacts, w2FakeCodex, TestMain's
// log quarantine) is PRE-EXISTING package test state.
//
// G5: every test isolates HOME (useTestStore does it, or t.Setenv does) and
// owns its CODEX_POOL_DIR. Nothing here reads the real ~/.codex,
// ~/.codex-pool, ~/.claude-pool or ~/.kraken. G2: every fixture identity is a
// hand-built unsigned JWT over an example.com address — no real token, no real
// email.
//
// Every package-level identifier declared here is prefixed contractCxp.

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kraken/internal/core"
	"kraken/internal/store"
)

// contractCxpNow is the fixed clock every pure call below passes.
var contractCxpNow = time.UnixMilli(1_700_000_000_000)

// contractCxpMain is the machine's own codex identity in every fixture — the
// one decideBindCodex excludes.
const contractCxpMain = "machine@example.com"

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

// contractCxpPool points CODEX_POOL_DIR at a scratch root the test owns and
// returns it. HOME must already be isolated by the caller.
func contractCxpPool(t *testing.T) string {
	t.Helper()
	pool := filepath.Join(t.TempDir(), "codex-pool")
	t.Setenv("CODEX_POOL_DIR", pool)
	return pool
}

// contractCxpAuthJSON is a slot's auth.json: three dot-separated segments, of
// which only the middle is read, carrying a long-expired identity payload.
func contractCxpAuthJSON(email, plan string) string {
	payload := fmt.Sprintf(
		`{"email":%q,"exp":1,"https://api.openai.com/auth":{"chatgpt_plan_type":%q}}`,
		email, plan)
	return fmt.Sprintf(`{"tokens":{"id_token":"x.%s.y"}}`,
		base64.RawURLEncoding.EncodeToString([]byte(payload)))
}

// contractCxpSlot creates a USABLE codex slot and returns its directory.
func contractCxpSlot(t *testing.T, slug, email string) string {
	t.Helper()
	dir := core.CodexSlotDir(slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir slot %s: %v", slug, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(contractCxpAuthJSON(email, "pro")), 0o600); err != nil {
		t.Fatalf("write auth.json for %s: %v", slug, err)
	}
	return dir
}

// contractCxpRow is a fresh, fully eligible codex pool row.
func contractCxpRow(slug, email string, sessionPercent float64) core.AccountUsage {
	return core.AccountUsage{
		Slug:      slug,
		Email:     email,
		Session:   &core.UsageWindow{Percent: sessionPercent},
		FetchedAt: contractCxpNow.UnixMilli() - 60_000,
	}
}

func contractCxpSnap(rows ...core.AccountUsage) core.UsageSnapshot {
	return core.UsageSnapshot{FetchedAt: contractCxpNow.UnixMilli(), Accounts: rows}
}

// contractCxpUsableAll is the probe that passes every slot.
func contractCxpUsableAll(string) bool { return true }

// contractCxpTokenCount renders one whole rollout line carrying rate_limits.
func contractCxpTokenCount(ts, primary, secondary, reached string) string {
	return fmt.Sprintf(
		`{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","rate_limits":{"primary":%s,"secondary":%s,"rate_limit_reached_type":%s}}}`,
		ts, primary, secondary, reached)
}

func contractCxpWindow(usedPercent float64, windowMinutes int, resetsAt int64) string {
	return fmt.Sprintf(`{"used_percent":%v,"window_minutes":%d,"resets_at":%d}`, usedPercent, windowMinutes, resetsAt)
}

// contractCxpWriteRollout writes a rollout under <home>/sessions/<rel>/ and
// returns its path.
func contractCxpWriteRollout(t *testing.T, home, rel, name string, lines ...string) string {
	t.Helper()
	dir := filepath.Join(home, "sessions", rel)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir rollout dir: %v", err)
	}
	p := filepath.Join(dir, "rollout-"+name+".jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write rollout: %v", err)
	}
	return p
}

// contractCxpShellQuote is the single-quoted shell form the contract freezes
// for the env prefix: quoted unconditionally, with the four-character shell
// escape standing in for each embedded single quote.
func contractCxpShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ---------------------------------------------------------------------------
// slotDirFor — one derivation per agent (C1/C3)
// ---------------------------------------------------------------------------

// Obligation: "core.CodexSlotDir(slug) when agent == \"codex\", else exactly
// what slotDir(slug) returns today."
//
// The two pools are different directory trees, so a caller that guessed would
// point CODEX_HOME at a claude slot — codex would create an empty logged-out
// home there and the board would read the dead pane as working.
func TestContractCxpSlotDirForRoutesByAgent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	contractCxpPool(t)

	if got, want := slotDirFor("codex", "tmp-1"), core.CodexSlotDir("tmp-1"); got != want {
		t.Errorf("slotDirFor(codex, tmp-1) = %q, want %q", got, want)
	}
	for _, agent := range []string{"claude", ""} {
		if got, want := slotDirFor(agent, "slot-a"), slotDir("slot-a"); got != want {
			t.Errorf("slotDirFor(%q, slot-a) = %q, want the claude pool's %q", agent, got, want)
		}
	}
	if slotDirFor("codex", "same") == slotDirFor("claude", "same") {
		t.Error("the two pools resolve one slug to the same directory — a codex cell could be pointed at a claude slot")
	}
}

// ---------------------------------------------------------------------------
// accountEnvPrefix — the whole delivery mechanism (C4)
// ---------------------------------------------------------------------------

// Obligation: "Codex job with a non-empty AccountSlug for which
// core.CodexSlotUsable(core.CodexSlotDir(slug)) is true: exactly
// CODEX_HOME='<dir>' — single-quoted unconditionally, each embedded single
// quote shell-escaped, one trailing space. […]
// core.CodexHomeFromCommand(accountEnvPrefix(j)
// + \"codex\") returns the slot dir."
//
// The round trip is the contract's own self-test: the prefix is what a pane's
// start command carries, and discovery reads the home back out of that exact
// string to find the cell's thread. A quoting rule the two halves disagreed on
// would put a bound cell's thread lookup on the machine login.
func TestContractCxpAccountEnvPrefixDeliversCodexHomeAndInvertsBackToIt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// a pool root holding a single quote, so the '\'' escape is in play
	root := filepath.Join(t.TempDir(), "carl's pool")
	t.Setenv("CODEX_POOL_DIR", root)
	dir := contractCxpSlot(t, "tmp-1", "slot-one@example.com")
	if !strings.Contains(dir, "'") {
		t.Fatalf("fixture is wrong: %q carries no single quote, so the escape is untested", dir)
	}

	j := &Job{ID: 1, Slug: "cxp-prefix", Agent: "codex", AccountSlug: "tmp-1"}
	got := accountEnvPrefix(j)
	want := "CODEX_HOME=" + contractCxpShellQuote(dir) + " "
	if got != want {
		t.Fatalf("accountEnvPrefix(codex job) =\n  %q\nwant\n  %q", got, want)
	}
	if back := core.CodexHomeFromCommand(got + "codex"); back != dir {
		t.Errorf("core.CodexHomeFromCommand(prefix + \"codex\") = %q, want the slot dir %q — the prefix and the parser must be exact inverses", back, dir)
	}
}

// Obligation: "A codex job never yields the claude variable; a claude job
// never yields CODEX_HOME", and for a claude job the string is "today's
// string, byte-identical".
func TestContractCxpAccountEnvPrefixKeepsTheTwoAgentsApart(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	contractCxpPool(t)
	contractCxpSlot(t, "tmp-1", "slot-one@example.com")

	codexJob := &Job{ID: 2, Slug: "cxp-codex", Agent: "codex", AccountSlug: "tmp-1"}
	codexPrefix := accountEnvPrefix(codexJob)
	if strings.Contains(codexPrefix, "CLAUDE_SECURESTORAGE_CONFIG_DIR") {
		t.Errorf("a codex job's prefix carries the claude variable: %q", codexPrefix)
	}

	for _, agent := range []string{"", "claude"} {
		claudeJob := &Job{ID: 3, Slug: "cxp-claude", Agent: agent, AccountSlug: "slot-a"}
		prefix := accountEnvPrefix(claudeJob)
		if strings.Contains(prefix, "CODEX_HOME") {
			t.Errorf("a job with agent %q carries CODEX_HOME: %q", agent, prefix)
		}
		want := "CLAUDE_SECURESTORAGE_CONFIG_DIR=" + contractCxpShellQuote(slotDir("slot-a")) + " "
		if prefix != want {
			t.Errorf("accountEnvPrefix(agent %q) =\n  %q\nwant the unchanged claude string\n  %q", agent, prefix, want)
		}
	}
}

// Obligation: "usable = codexSlotUsable (a package var seam defaulting to
// core.CodexSlotUsable)." The seam is what lets a binder test run without a
// pool on disk; its DEFAULT is what makes the production binder fail-closed,
// and a seam left pointing at something more permissive would never show up in
// a test that swaps it.
func TestContractCxpCodexSlotUsableSeamDefaultsToCore(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	contractCxpPool(t)
	good := contractCxpSlot(t, "tmp-1", "slot-one@example.com")
	missing := core.CodexSlotDir("not-there")

	for _, dir := range []string{good, missing} {
		if got, want := codexSlotUsable(dir), core.CodexSlotUsable(dir); got != want {
			t.Errorf("codexSlotUsable(%q) = %v, want core.CodexSlotUsable's %v", dir, got, want)
		}
	}
	if !codexSlotUsable(good) {
		t.Errorf("codexSlotUsable(%q) = false for a slot holding a non-empty auth.json", good)
	}
}

// ---------------------------------------------------------------------------
// decideBindCodex — the pure codex decision (C3)
// ---------------------------------------------------------------------------

// Obligation: "Survivors go to core.SelectBindTarget(...) and its answer is
// returned." The base happy path: one eligible row, a usable slot, both the
// slug AND the email come back — the email is what gets stamped on the job row
// and what a later rescue excludes itself from.
func TestContractCxpDecideBindCodexPicksAUsableSlot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	contractCxpPool(t)
	snap := contractCxpSnap(contractCxpRow("tmp-1", "slot-one@example.com", 12))

	slug, email, reason := decideBindCodex(snap, nil, contractCxpMain, "", "", contractCxpUsableAll, contractCxpNow)
	if slug != "tmp-1" || email != "slot-one@example.com" {
		t.Errorf("decideBindCodex = (%q, %q, %q), want (tmp-1, slot-one@example.com)", slug, email, reason)
	}
}

// Obligation (Q4, carl: "we can remove the pinning"): "No rotation mode is
// consulted." A pinned machine is carl saying which CLAUDE account to bill;
// the codex pool is a different pool with a different login, so parking codex
// work on that pin strands every codex cell for a decision that was never
// about them.
func TestContractCxpDecideBindCodexIgnoresAManualPin(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	contractCxpPool(t)
	if err := SetRotationMode("manual", "cli"); err != nil {
		t.Fatalf("SetRotationMode: %v", err)
	}
	if got := RotationMode(); got != "manual" {
		t.Fatalf("fixture is wrong: RotationMode() = %q, want manual", got)
	}

	snap := contractCxpSnap(contractCxpRow("tmp-1", "slot-one@example.com", 12))
	slug, email, reason := decideBindCodex(snap, nil, contractCxpMain, "", "", contractCxpUsableAll, contractCxpNow)
	if slug != "tmp-1" || email != "slot-one@example.com" {
		t.Errorf("decideBindCodex under a manual pin = (%q, %q, %q), want (tmp-1, slot-one@example.com) — the codex arm never reads the mode",
			slug, email, reason)
	}
}

// Obligation: "rows whose Email equals mainEmail case-insensitively (when
// mainEmail non-empty)" are excluded. The machine's own ~/.codex login is what
// every unbound pane already bills; binding a cell to it is a cell that is not
// distributed at all.
func TestContractCxpDecideBindCodexExcludesTheMachineIdentity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	contractCxpPool(t)
	// the excluded row is the HEALTHIEST, so only the exclusion can decide this
	mirror := contractCxpRow("mirror", "MACHINE@Example.COM", 1)
	other := contractCxpRow("tmp-2", "slot-two@example.com", 70)

	slug, email, _ := decideBindCodex(contractCxpSnap(mirror, other), nil, contractCxpMain, "", "", contractCxpUsableAll, contractCxpNow)
	if slug != "tmp-2" || email != "slot-two@example.com" {
		t.Errorf("decideBindCodex = (%q, %q), want (tmp-2, slot-two@example.com) — the main identity is excluded whatever its case", slug, email)
	}
}

// Obligation: "the row whose Slug equals excludeSlug (when non-empty)" is
// excluded. This is the rescue's self-exclusion, and it is keyed on the SLUG
// rather than on the email precisely because the cell's own slot is the one
// whose window just ran out.
func TestContractCxpDecideBindCodexExcludesTheNamedSlug(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	contractCxpPool(t)
	self := contractCxpRow("tmp-1", "slot-one@example.com", 1)
	other := contractCxpRow("tmp-2", "slot-two@example.com", 70)

	slug, _, _ := decideBindCodex(contractCxpSnap(self, other), nil, contractCxpMain, "", "tmp-1", contractCxpUsableAll, contractCxpNow)
	if slug != "tmp-2" {
		t.Errorf("decideBindCodex with excludeSlug=tmp-1 picked %q, want tmp-2 — the exhausted slot is the one being rescued OFF", slug)
	}

	// without the exclusion the same inputs pick the other row, which is what
	// makes the assertion above about the exclusion rather than about ranking
	slug, _, _ = decideBindCodex(contractCxpSnap(self, other), nil, contractCxpMain, "", "", contractCxpUsableAll, contractCxpNow)
	if slug != "tmp-1" {
		t.Errorf("decideBindCodex with no exclusion picked %q, want tmp-1 (the emptiest row)", slug)
	}
}

// Obligation: "Survivors go to core.SelectBindTarget(candidates, excludeEmails,
// counts, prevSlug, now.UnixMilli()) and its answer is returned." The two
// observable consequences of that delegation: the bound-count key outranks the
// session percentage (the spreading preference), and an eligible prevSlug is
// kept (stickiness, which is what returns a reactivated job to the slot whose
// prompt cache it already warmed).
func TestContractCxpDecideBindCodexDelegatesRankingAndStickiness(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	contractCxpPool(t)

	busy := contractCxpRow("busy", "busy@example.com", 1)
	idle := contractCxpRow("idle", "idle@example.com", 70)
	slug, _, _ := decideBindCodex(contractCxpSnap(busy, idle), map[string]int{"busy": 1}, contractCxpMain, "", "", contractCxpUsableAll, contractCxpNow)
	if slug != "idle" {
		t.Errorf("decideBindCodex picked %q, want idle — fewest live bound cells outranks the session %%", slug)
	}

	prev := contractCxpRow("prev", "prev@example.com", 70)
	better := contractCxpRow("better", "better@example.com", 1)
	slug, email, _ := decideBindCodex(contractCxpSnap(prev, better), map[string]int{"prev": 2}, contractCxpMain, "prev", "", contractCxpUsableAll, contractCxpNow)
	if slug != "prev" || email != "prev@example.com" {
		t.Errorf("decideBindCodex = (%q, %q), want the sticky (prev, prev@example.com)", slug, email)
	}
	slug, _, _ = decideBindCodex(contractCxpSnap(prev, better), map[string]int{"prev": 2}, contractCxpMain, "", "", contractCxpUsableAll, contractCxpNow)
	if slug != "better" {
		t.Errorf("decideBindCodex with no prevSlug picked %q, want better — which is what makes the case above stickiness rather than ordering", slug)
	}
}

// Obligation: "rows for which usable(core.CodexSlotDir(row.Slug)) is false,
// each adding the reason <slug>: no auth.json" and "An empty pick returns
// every collected reason joined by '; ', or 'no codex pool accounts' when
// there were no rows at all."
//
// Both strings are frozen because they are what a bind-skipped event reports:
// "the pool is empty" and "the one slot there has no credential" are different
// problems with different fixes, and one text for both would hide which.
func TestContractCxpDecideBindCodexReasonsNameTheGuardAndTheDryPool(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	contractCxpPool(t)

	deny := func(string) bool { return false }
	slug, email, reason := decideBindCodex(
		contractCxpSnap(contractCxpRow("only", "only@example.com", 5)),
		nil, contractCxpMain, "", "", deny, contractCxpNow)
	if slug != "" || email != "" {
		t.Errorf("a pool whose only slot has no auth.json bound (%q, %q), want the empty pair", slug, email)
	}
	if !strings.Contains(reason, "only: no auth.json") {
		t.Errorf("reason = %q, want it to contain %q", reason, "only: no auth.json")
	}

	slug, email, reason = decideBindCodex(contractCxpSnap(), nil, contractCxpMain, "", "", contractCxpUsableAll, contractCxpNow)
	if slug != "" || email != "" {
		t.Errorf("an empty snapshot bound (%q, %q), want the empty pair", slug, email)
	}
	if reason != "no codex pool accounts" {
		t.Errorf("reason = %q, want exactly %q", reason, "no codex pool accounts")
	}
}

// Obligation: the probe is asked about "core.CodexSlotDir(row.Slug)" — the one
// derivation, so the directory a slot is probed at can never differ from the
// one the env prefix names.
func TestContractCxpDecideBindCodexProbesTheCodexSlotDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	contractCxpPool(t)
	seen := []string{}
	probe := func(dir string) bool {
		seen = append(seen, dir)
		return true
	}

	decideBindCodex(contractCxpSnap(contractCxpRow("tmp-1", "slot-one@example.com", 5)), nil, contractCxpMain, "", "", probe, contractCxpNow)

	want := core.CodexSlotDir("tmp-1")
	var found bool
	for _, dir := range seen {
		if dir == want {
			found = true
		}
	}
	if !found {
		t.Errorf("the probe was asked about %v, want it to include the codex slot dir %q", seen, want)
	}
}

// ---------------------------------------------------------------------------
// boundCountsFor — per-agent distribution (C3)
// ---------------------------------------------------------------------------

// Obligation: "Counts AccountSlug only over jobs whose agent matches:
// agent == \"codex\" matches Job.Agent == \"codex\"; any other value matches
// jobs whose Agent is NOT \"codex\". […] boundCounts(jobs, isLive) keeps its
// current behaviour."
//
// The two pools can hold the same slug. Without the partition a live claude
// cell on claude slot `alpha` would push codex work off codex slot `alpha`,
// which is a different account entirely.
func TestContractCxpBoundCountsForPartitionsByAgent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	live := map[string]bool{"cx": true, "cl-empty": true, "cl-named": true, "unbound": true}
	rows := []*Job{
		{Slug: "cx", Agent: "codex", AccountSlug: "alpha"},
		{Slug: "cl-empty", Agent: "", AccountSlug: "alpha"},
		{Slug: "cl-named", Agent: "claude", AccountSlug: "alpha"},
		// bound to the same slug but its cell is gone: it holds no credential
		{Slug: "cx-dead", Agent: "codex", AccountSlug: "alpha"},
		// live but unbound: it runs on the machine login and counts nowhere
		{Slug: "unbound", Agent: "codex"},
	}
	isLive := func(j *Job) bool { return live[j.Slug] }

	if got := boundCountsFor(rows, "codex", isLive)["alpha"]; got != 1 {
		t.Errorf("boundCountsFor(codex)[alpha] = %d, want 1 — only the live codex cell", got)
	}
	for _, agent := range []string{"claude", ""} {
		if got := boundCountsFor(rows, agent, isLive)["alpha"]; got != 2 {
			t.Errorf("boundCountsFor(%q)[alpha] = %d, want 2 — an empty Agent is a claude job", agent, got)
		}
	}
	if got := boundCounts(rows, isLive)["alpha"]; got != 3 {
		t.Errorf("boundCounts[alpha] = %d, want 3 — the agent-blind original is unchanged", got)
	}
	if got := boundCountsFor(rows, "codex", isLive)[""]; got != 0 {
		t.Errorf("boundCountsFor(codex)[\"\"] = %d, want 0 — an unbound job contributes nothing", got)
	}
	if got := boundCountsFor(nil, "codex", isLive); len(got) != 0 {
		t.Errorf("boundCountsFor(nil) = %v, want no counted slugs", got)
	}
}

// ---------------------------------------------------------------------------
// CodexUsagePass / ReadCodexUsageSnapshot — the snapshot (C2)
// ---------------------------------------------------------------------------

// Obligation: "Builds core.UsageSnapshot{FetchedAt: now millis, Accounts:
// core.FetchCodexUsage()} and persists it under its own key […] never touching
// the claude snapshot", and "Reads it back".
//
// The separate key is the whole point: one snapshot holding both pools would
// let the claude selector rank a codex slot, and the claude table on the board
// would grow rows for accounts that cannot serve a claude at all.
func TestContractCxpUsagePassPersistsItsOwnSnapshotBesideTheClaudeOne(t *testing.T) {
	useTestStore(t)
	contractCxpPool(t)
	contractCxpSlot(t, "tmp-2", "slot-two@example.com")
	contractCxpSlot(t, "tmp-1", "slot-one@example.com")

	before := time.Now().UnixMilli()
	if err := CodexUsagePass(); err != nil {
		t.Fatalf("CodexUsagePass() = %v, want nil", err)
	}

	snap, ok := ReadCodexUsageSnapshot()
	if !ok {
		t.Fatalf("ReadCodexUsageSnapshot() ok = false right after a successful pass")
	}
	if len(snap.Accounts) != 2 {
		t.Fatalf("snapshot holds %d rows, want 2: %+v", len(snap.Accounts), snap.Accounts)
	}
	if snap.Accounts[0].Slug != "tmp-1" || snap.Accounts[1].Slug != "tmp-2" {
		t.Errorf("rows = [%q %q], want [tmp-1 tmp-2] — core.FetchCodexUsage's order", snap.Accounts[0].Slug, snap.Accounts[1].Slug)
	}
	if snap.Accounts[0].Email != "slot-one@example.com" {
		t.Errorf("tmp-1's Email = %q, want slot-one@example.com", snap.Accounts[0].Email)
	}
	if snap.FetchedAt < before {
		t.Errorf("FetchedAt = %d, want the pass' own clock (>= %d)", snap.FetchedAt, before)
	}

	// the claude snapshot is a different key and nothing wrote it
	if _, claudeOK := ReadUsageSnapshot(); claudeOK {
		t.Error("the codex pass wrote the claude snapshot — the two keys must be independent")
	}
}

// ---------------------------------------------------------------------------
// codexLimitReason / parseLimitReset — the codex limit trigger (C9)
// ---------------------------------------------------------------------------

// Obligation: "\"\" unless core.CodexLimitReached is ok; then exactly
// usageLimitReasonPrefix + \"codex \" + reachedType + \" resets_at=\" +
// <resetsAt in decimal>. IsUsageLimitReason is true of it." Plus
// parseLimitReset's own row: "When details contains resets_at=<digits>,
// returns that integer as-is — no banner regex, no 6-hour cap."
//
// The three read as one chain because they ARE one: the health pass routes on
// IsUsageLimitReason, and the park/resume clock reads the reset back out of
// the same string. A reason the parser cannot read parks a codex cell forever.
func TestContractCxpCodexLimitReasonIsTheFrozenTextTheRescueReadsBack(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const resets = int64(1_790_208_555)
	p := contractCxpWriteRollout(t, t.TempDir(), "2026/09/17", "abc",
		contractCxpTokenCount("2026-09-17T00:14:40.724Z",
			contractCxpWindow(97, 10080, resets), "null", `"weekly"`))

	got := codexLimitReason(p)
	want := usageLimitReasonPrefix + "codex weekly resets_at=" + fmt.Sprint(resets)
	if got != want {
		t.Fatalf("codexLimitReason =\n  %q\nwant\n  %q", got, want)
	}
	if !IsUsageLimitReason(got) {
		t.Errorf("IsUsageLimitReason(%q) = false — the daemon's health pass routes on exactly this", got)
	}
	if reset := parseLimitReset(got, contractCxpNow); reset != resets {
		t.Errorf("parseLimitReset(%q) = %d, want %d", got, reset, resets)
	}
}

// Obligation: "returns that integer as-is — no banner regex, no 6-hour cap."
// The claude banner names a reset within one ~5h window and is capped for that
// reason; codex hands over an absolute epoch, and capping it would park a cell
// against a clock the account never agreed to.
func TestContractCxpParseLimitResetTakesTheResetsAtClauseUncapped(t *testing.T) {
	const far = int64(1_900_000_000) // years past `at`, well beyond any claude cap
	got := parseLimitReset("codex weekly resets_at="+fmt.Sprint(far), time.Unix(1_700_000_000, 0))
	if got != far {
		t.Errorf("parseLimitReset = %d, want %d as-is", got, far)
	}
}

// ---------------------------------------------------------------------------
// CellHealth — the codex arm (C9)
// ---------------------------------------------------------------------------

// Obligation: "For a job with Agent == \"codex\": zeus is CellZeus(j, all); the
// zeus-<slug> name test is NOT applied (a codex peer's name is its thread
// title); with a non-empty zeus.RolloutPath the result is
// codexLimitReason(zeus.RolloutPath); otherwise \"\"."
//
// The name test is the load-bearing exclusion: a codex pane carries whatever
// codex titled the thread, so applying it would mean no codex cell ever
// reports a limit and the rescue is never reached.
func TestContractCxpCellHealthReadsACodexLimitFromTheZeusRollout(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	j := &Job{ID: 11, Slug: "cxp-health", Agent: "codex"}
	const resets = int64(1_790_208_555)
	rollout := contractCxpWriteRollout(t, t.TempDir(), "2026/09/17", "abc",
		contractCxpTokenCount("2026-09-17T00:14:40.724Z",
			contractCxpWindow(99, 10080, resets), "null", `"weekly"`))

	zeus := &core.Peer{
		Agent:       "codex",
		Depth:       0,
		Role:        "main",
		TmuxSession: CellSession(j),
		Name:        "refactor the widget", // a thread title, never ZeusName(j)
		SessionID:   "codex-sess-1",
		Pane:        "%3",
		RolloutPath: rollout,
	}
	if zeus.Name == ZeusName(j) {
		t.Fatalf("fixture is wrong: the peer's Name accidentally equals ZeusName(j) = %q", ZeusName(j))
	}
	if CellZeus(j, []*core.Peer{zeus}) != zeus {
		t.Fatalf("fixture is wrong: CellZeus did not elect the depth-0 codex pane")
	}

	if got, want := CellHealth(j, []*core.Peer{zeus}), codexLimitReason(rollout); got != want {
		t.Fatalf("CellHealth =\n  %q\nwant codexLimitReason(rollout)\n  %q", got, want)
	}

	// a codex zeus whose rollout is not known yet is not a health problem
	zeus.RolloutPath = ""
	if got := CellHealth(j, []*core.Peer{zeus}); got != "" {
		t.Errorf("CellHealth with an empty RolloutPath = %q, want \"\"", got)
	}
}

// ---------------------------------------------------------------------------
// FindPendingQuestion — the codex dispatch (C6)
// ---------------------------------------------------------------------------

// Obligation: "Takes the codex path exactly when core.IsCodexRollout(transcript)."
// A slot-rooted rollout is not under ~/.codex, so the retired substring test
// routed it to the claude scanner and every question a bound codex cell asked
// went unanswerable on the board.
func TestContractCxpFindPendingQuestionTakesTheCodexPathForASlotRollout(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	contractCxpPool(t)
	slot := contractCxpSlot(t, "tmp-1", "slot-one@example.com")
	p := contractCxpWriteRollout(t, slot, "2026/09/17", "abc",
		`{"type":"session_meta","payload":{"id":"abc","cwd":"/w"}}`,
		`{"type":"response_item","payload":{"type":"function_call","name":"request_user_input","call_id":"call_1","arguments":"{\"questions\":[{\"header\":\"Scope\",\"question\":\"How far?\",\"options\":[{\"label\":\"A\",\"description\":\"da\"},{\"label\":\"B\",\"description\":\"db\"}]}]}"}}`,
	)
	if !core.IsCodexRollout(p) {
		t.Fatalf("fixture is wrong: core.IsCodexRollout(%q) = false", p)
	}

	pq := FindPendingQuestion(p)
	if pq == nil {
		t.Fatal("FindPendingQuestion(slot-rooted rollout) = nil, want the pending request_user_input")
	}
	if pq.ToolUseID != "call_1" {
		t.Errorf("ToolUseID = %q, want call_1", pq.ToolUseID)
	}
	if len(pq.Questions) != 1 || pq.Questions[0].Header != "Scope" || pq.Questions[0].Question != "How far?" {
		t.Fatalf("questions = %+v, want the one Scope question", pq.Questions)
	}
}

// ---------------------------------------------------------------------------
// resumeCellAgents — the codex rescue respawn (C10)
// ---------------------------------------------------------------------------

// Obligation (in order): "to := slotDirFor(\"codex\", j.AccountSlug) […]; when
// zeus.CodexHome differs from to and zeus.RolloutPath is non-empty,
// placeCodexRollout(zeus.RolloutPath, zeus.CodexHome, to) […]; on success the
// command is zeusLaunch(j, zeus.CWD, prompt, zeus.SessionID); the pane is
// respawned through respawnPeerPane. The command is never a claude command and
// always begins with accountEnvPrefix(j)."
//
// Q1 measured why the placement comes FIRST: `codex exec resume <id>` under the
// new slot fails with `no rollout found for thread id` until the rollout sits
// at the same relative path under it. Resuming before placing is a cell that
// comes back as a stranger to its own conversation.
func TestContractCxpResumeCellAgentsPlacesTheRolloutThenResumesUnderTheNewSlot(t *testing.T) {
	st := useTestStore(t)
	contractCxpPool(t)
	from := contractCxpSlot(t, "tmp-1", "slot-one@example.com") // the exhausted slot
	to := contractCxpSlot(t, "tmp-2", "slot-two@example.com")   // the slot the rebind landed on
	launcher := w2FakeCodex(t)

	row := mustCreateJob(t, st, store.Job{Slug: "cxp-resume", Repo: "monolith", Agent: "codex"})
	j := &Job{
		Slug: row.Slug, ID: row.ID, Repo: "monolith", Agent: "codex",
		Session:      "codex-sess-1",
		AccountSlug:  "tmp-2",
		AccountEmail: "slot-two@example.com",
	}
	if err := os.MkdirAll(cellDir(j), 0o755); err != nil {
		t.Fatal(err)
	}
	worktree := t.TempDir()

	zeus := &core.Peer{
		Agent:       "codex",
		Depth:       0,
		Role:        "main",
		TmuxSession: CellSession(j),
		Pane:        "%3",
		PID:         5150,
		Name:        "refactor the widget",
		SessionID:   j.Session,
		CWD:         worktree,
		CodexHome:   from,
		RolloutPath: filepath.Join(from, "sessions", "2026", "09", "17", "rollout-abc.jsonl"),
	}

	type placement struct{ rollout, fromHome, toHome string }
	placed := []placement{}
	origPlace := placeCodexRollout
	placeCodexRollout = func(rolloutPath, fromHome, toHome string) (string, error) {
		placed = append(placed, placement{rolloutPath, fromHome, toHome})
		return filepath.Join(toHome, "sessions", "2026", "09", "17", "rollout-abc.jsonl"), nil
	}
	t.Cleanup(func() { placeCodexRollout = origPlace })

	type respawn struct{ pane, cmd string }
	respawns := []respawn{}
	origRespawn := respawnPeerPane
	respawnPeerPane = func(p *core.Peer, cmd string) error {
		respawns = append(respawns, respawn{p.Pane, cmd})
		return nil
	}
	t.Cleanup(func() { respawnPeerPane = origRespawn })

	const prompt = "CXP-PROMPT-SENTINEL"
	n, err := resumeCellAgents(j, []*core.Peer{zeus}, prompt)
	if err != nil {
		t.Fatalf("resumeCellAgents(codex cell) = (%d, %v), want (1, nil)", n, err)
	}
	if n != 1 {
		t.Fatalf("resumeCellAgents resumed %d panes, want exactly 1 — zeus and zeus alone", n)
	}

	if len(placed) != 1 {
		t.Fatalf("placeCodexRollout was called %d times, want exactly once: %+v", len(placed), placed)
	}
	if placed[0].rollout != zeus.RolloutPath || placed[0].fromHome != from || placed[0].toHome != to {
		t.Errorf("placeCodexRollout(%q, %q, %q), want (%q, %q, %q)",
			placed[0].rollout, placed[0].fromHome, placed[0].toHome, zeus.RolloutPath, from, to)
	}
	if placed[0].toHome != slotDirFor("codex", j.AccountSlug) {
		t.Errorf("the rollout was placed into %q, want slotDirFor(codex, %q) = %q",
			placed[0].toHome, j.AccountSlug, slotDirFor("codex", j.AccountSlug))
	}

	if len(respawns) != 1 {
		t.Fatalf("respawnPeerPane was called %d times, want exactly once: %+v", len(respawns), respawns)
	}
	if respawns[0].pane != zeus.Pane {
		t.Errorf("respawned pane %q, want zeus' own %q", respawns[0].pane, zeus.Pane)
	}

	wantCmd, err := zeusLaunch(j, zeus.CWD, prompt, zeus.SessionID)
	if err != nil {
		t.Fatalf("zeusLaunch (the expected command): %v", err)
	}
	if respawns[0].cmd != wantCmd {
		t.Errorf("respawn command =\n  %q\nwant zeusLaunch(j, zeus.CWD, prompt, zeus.SessionID)\n  %q", respawns[0].cmd, wantCmd)
	}
	if prefix := accountEnvPrefix(j); !strings.HasPrefix(respawns[0].cmd, prefix) {
		t.Errorf("respawn command does not begin with accountEnvPrefix(j) = %q: %q", prefix, respawns[0].cmd)
	}
	if !strings.Contains(respawns[0].cmd, launcher) {
		t.Errorf("respawn command does not invoke the resolved codex launcher %q: %q", launcher, respawns[0].cmd)
	}
	if strings.Contains(respawns[0].cmd, "claude") {
		t.Errorf("a codex cell was respawned with a claude command: %q", respawns[0].cmd)
	}
}

// ---------------------------------------------------------------------------
// codexResumePrompt / codexHandoffPrompt — what a codex zeus comes back to
// ---------------------------------------------------------------------------

// contractCxpBanned are the three tokens neither codex prompt may carry: they
// name Claude Code's own teammate machinery, which a codex zeus does not have.
// Telling it to SendMessage a teammate is an instruction it can only fail.
var contractCxpBanned = []string{"TaskStop", "SendMessage", "teammate"}

func contractCxpAssertNoClaudeTooling(t *testing.T, label, text string) {
	t.Helper()
	lower := strings.ToLower(text)
	for _, token := range contractCxpBanned {
		if strings.Contains(lower, strings.ToLower(token)) {
			t.Errorf("%s names %q — a codex zeus has no Claude Code teammate tooling: %q", label, token, text)
		}
	}
}

// Obligation: "The prompt a codex zeus is resumed with. Contains base's
// account-moved sentence but none of the tokens TaskStop, SendMessage,
// teammate."
//
// The claude rebind prompt's account-moved clause is the word "rebound" (the
// cell moved, not the machine's main login), and carrying it over is what
// stops a resumed codex zeus believing its account is unchanged.
func TestContractCxpCodexResumePromptKeepsTheAccountMovedSentenceOnly(t *testing.T) {
	got := codexResumePrompt(limitRescueResumePrompt)
	if got == "" {
		t.Fatal("codexResumePrompt returned empty — the respawned zeus would come back with no instruction at all")
	}
	if !strings.Contains(strings.ToLower(got), "rebound") {
		t.Errorf("codexResumePrompt does not carry the base prompt's account-moved sentence: %q", got)
	}
	contractCxpAssertNoClaudeTooling(t, "codexResumePrompt", got)
}

// Obligation: "Names oldRollout's absolute path and the cell's artifacts dir,
// and says the previous conversation could not be carried over. Same three
// banned tokens."
//
// This is the prompt a FRESH session wakes up on, so the two paths are the
// only continuity it has: the rollout it can still read, and the artifacts the
// cell already wrote.
func TestContractCxpCodexHandoffPromptNamesTheRolloutAndTheArtifacts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	j := &Job{ID: 12, Slug: "cxp-handoff", Agent: "codex"}
	const oldRollout = "/tmp/codex-pool/accounts/tmp-1/sessions/2026/09/17/rollout-abc.jsonl"

	got := codexHandoffPrompt(j, oldRollout, limitRescueResumePrompt)
	if !strings.Contains(got, oldRollout) {
		t.Errorf("codexHandoffPrompt does not name the old rollout %q: %q", oldRollout, got)
	}
	if artifacts := CellArtifacts(j); !strings.Contains(got, artifacts) {
		t.Errorf("codexHandoffPrompt does not name the cell's artifacts dir %q: %q", artifacts, got)
	}
	if !strings.Contains(strings.ToLower(got), "conversation") {
		t.Errorf("codexHandoffPrompt never says the previous conversation could not be carried over: %q", got)
	}
	contractCxpAssertNoClaudeTooling(t, "codexHandoffPrompt", got)
}

// ===========================================================================
// AMENDMENT 1 — symbols frozen after the first join, LOGIC dimension.
// Still authored blind: no implementation file was opened for any of these.
// ===========================================================================

// contractCxpBoundWorld wires the world every bindAccount case below runs in:
// an isolated HOME with a wired store, a temp codex pool holding ONE slot with
// real usage evidence (so the pick is forced rather than a tie-break), and a
// persisted codex snapshot written by the real CodexUsagePass. It returns the
// store and the slot's identity.
func contractCxpBoundWorld(t *testing.T) (*store.Store, string, string) {
	t.Helper()
	st := useTestStore(t)
	contractCxpPool(t)
	const slug, email = "tmp-1", "slot-one@example.com"
	slot := contractCxpSlot(t, slug, email)
	contractCxpWriteRollout(t, slot, "2026/09/17", "a",
		contractCxpTokenCount("2026-09-17T00:14:40.724Z", contractCxpWindow(12, 10080, 1_790_208_555), "null", "null"))
	if err := CodexUsagePass(); err != nil {
		t.Fatalf("CodexUsagePass: %v", err)
	}
	if snap, ok := ReadCodexUsageSnapshot(); !ok || len(snap.Accounts) != 1 {
		t.Fatalf("fixture is wrong: codex snapshot = (%+v, %v), want one row", snap, ok)
	}
	return st, slug, email
}

// ---------------------------------------------------------------------------
// bindAccount — the impure wrapper (C3)
// ---------------------------------------------------------------------------

// Obligation: "Codex job: candidates come from ReadCodexUsageSnapshot, counts
// from boundCountsFor(…, \"codex\", …), the decision from decideBindCodex […].
// A pick is stamped through SetJobAccount and mirrored on the in-memory job
// exactly as today."
//
// Both halves of the stamp are asserted because they fail independently and
// only one of them is visible from the board: the ROW is what the card and a
// later reactivation read, while the IN-MEMORY job is what the very next line
// of StartCell hands to accountEnvPrefix. A bind that wrote only the row would
// spawn this cell unbound and look bound forever after.
func TestContractCxpBindAccountStampsACodexSlotOnTheRowAndTheJob(t *testing.T) {
	st, slug, email := contractCxpBoundWorld(t)
	row := mustCreateJob(t, st, store.Job{Slug: "cxp-bind", Repo: "monolith", Agent: "codex"})
	j := &Job{Slug: row.Slug, ID: row.ID, Repo: "monolith", Agent: "codex"}

	bindAccount(j, "test")

	if j.AccountSlug != slug || j.AccountEmail != email {
		t.Fatalf("in-memory job = (%q, %q), want the codex slot (%q, %q)", j.AccountSlug, j.AccountEmail, slug, email)
	}
	if j.AccountAt == 0 {
		t.Errorf("AccountAt = 0 on a bound job — the bind stamp is what the grace window counts from")
	}
	stored, err := st.GetJobByID(row.ID)
	if err != nil {
		t.Fatalf("read the job row back: %v", err)
	}
	if stored.AccountSlug != slug || stored.AccountEmail != email {
		t.Errorf("stored row = (%q, %q), want (%q, %q) — the row and the in-memory job must agree",
			stored.AccountSlug, stored.AccountEmail, slug, email)
	}
}

// Obligation: "A codex job is only ever offered codex slots and a claude job
// only claude slots; there is no input for which the wrong pool's slug is
// returned" (C3), and G1: a claude job's observable output is unchanged.
//
// Run in the SAME world as the codex bind above — one populated codex pool and
// no claude pool at all — because that is the machine this feature actually
// ships onto. A binder that read one pool for both agents would hand this
// claude job `tmp-1` and point CLAUDE_SECURESTORAGE_CONFIG_DIR at a codex slot.
func TestContractCxpBindAccountNeverGivesAClaudeJobACodexSlug(t *testing.T) {
	st, codexSlug, _ := contractCxpBoundWorld(t)

	for _, agent := range []string{"", "claude"} {
		row := mustCreateJob(t, st, store.Job{Slug: "cxp-claude-" + agent + "x", Repo: "monolith", Agent: agent})
		j := &Job{Slug: row.Slug, ID: row.ID, Repo: "monolith", Agent: agent}

		bindAccount(j, "test")

		if j.AccountSlug == codexSlug {
			t.Errorf("a job with agent %q was bound to the CODEX slot %q", agent, codexSlug)
		}
		if j.AccountSlug != "" {
			t.Errorf("a job with agent %q was bound to %q, but this world has no claude pool at all", agent, j.AccountSlug)
		}
		stored, err := st.GetJobByID(row.ID)
		if err != nil {
			t.Fatalf("read the job row back: %v", err)
		}
		if stored.AccountSlug != "" {
			t.Errorf("the stored row for agent %q carries %q, want the unbound empty string", agent, stored.AccountSlug)
		}
	}
}

// Obligation (Q4): the codex arm "never consults" the rotation mode, so a
// manual pin — which is carl choosing a CLAUDE account — must not leave every
// codex cell unbound on the machine login.
func TestContractCxpBindAccountBindsACodexJobUnderAManualPin(t *testing.T) {
	st, slug, _ := contractCxpBoundWorld(t)
	if err := SetRotationMode("manual", "cli"); err != nil {
		t.Fatalf("SetRotationMode: %v", err)
	}
	if got := RotationMode(); got != "manual" {
		t.Fatalf("fixture is wrong: RotationMode() = %q, want manual", got)
	}
	row := mustCreateJob(t, st, store.Job{Slug: "cxp-bind-pinned", Repo: "monolith", Agent: "codex"})
	j := &Job{Slug: row.Slug, ID: row.ID, Repo: "monolith", Agent: "codex"}

	bindAccount(j, "test")

	if j.AccountSlug != slug {
		t.Errorf("a codex job on a pinned machine bound to %q, want %q — the pin governs the claude pool only", j.AccountSlug, slug)
	}
	stored, err := st.GetJobByID(row.ID)
	if err != nil {
		t.Fatalf("read the job row back: %v", err)
	}
	if stored.AccountSlug != slug {
		t.Errorf("stored row = %q, want %q", stored.AccountSlug, slug)
	}
}

// ---------------------------------------------------------------------------
// rescueMode — the one value the rescue pass hands to limitRescueDecide
// ---------------------------------------------------------------------------

// Obligation: "\"auto\" for a job whose Agent is \"codex\" whatever
// ~/.kraken/rotation.json says; RotationMode() for every other job."
//
// This is the symbol that makes the previously-unreachable "rescue mode" row
// testable. The claude arm is asserted against RotationMode() rather than
// against a literal, because the obligation is that it DELEGATES — a claude
// job must follow the pin wherever the pin goes.
func TestContractCxpRescueModeIsAutoForCodexAndTheRealModeForClaude(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	codexJob := &Job{ID: 21, Slug: "cxp-rescue-codex", Agent: "codex"}

	for _, mode := range []string{"manual", "auto"} {
		if err := SetRotationMode(mode, "cli"); err != nil {
			t.Fatalf("SetRotationMode(%q): %v", mode, err)
		}
		if got := RotationMode(); got != mode {
			t.Fatalf("fixture is wrong: RotationMode() = %q, want %q", got, mode)
		}
		if got := rescueMode(codexJob); got != "auto" {
			t.Errorf("rescueMode(codex job) = %q with the machine on %q, want auto — a codex hit never parks on the claude pin", got, mode)
		}
		for _, agent := range []string{"", "claude"} {
			claudeJob := &Job{ID: 22, Slug: "cxp-rescue-claude", Agent: agent}
			if got := rescueMode(claudeJob); got != RotationMode() {
				t.Errorf("rescueMode(agent %q) = %q with the machine on %q, want RotationMode()", agent, got, mode)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// selectRescueBind — where a limited codex cell goes next (C10)
// ---------------------------------------------------------------------------

// Obligation: "Codex job: decideBindCodex over the codex snapshot with
// excludeSlug = j.AccountSlug, prevSlug = \"\", mainEmail = core.CodexMainEmail(),
// per-agent counts."
//
// The exhausted slot is deliberately the EMPTIEST row in the pool, so only the
// self-exclusion can produce the other answer: a rescue that rebound the cell
// to the account whose window just ran out is a rescue that changes nothing and
// resumes into the same wall.
//
// NOTE FOR THE LEAD: this row was not on my assigned list for amendment 1. Its
// signature became known in the same amendment and its no-snapshot arm is
// plainly athena-edge's; delete this one test if the whole row went to edge.
func TestContractCxpSelectRescueBindMovesACodexCellOffItsOwnSlot(t *testing.T) {
	st := useTestStore(t)
	contractCxpPool(t)

	// the cell's own slot is the emptiest: no evidence at all, so 0 percent
	contractCxpSlot(t, "tmp-1", "slot-one@example.com")
	// the target carries real (heavier) usage, and is still the only candidate
	target := contractCxpSlot(t, "tmp-2", "slot-two@example.com")
	contractCxpWriteRollout(t, target, "2026/09/17", "a",
		contractCxpTokenCount("2026-09-17T00:14:40.724Z", contractCxpWindow(55, 10080, 1_790_208_555), "null", "null"))
	if err := CodexUsagePass(); err != nil {
		t.Fatalf("CodexUsagePass: %v", err)
	}

	row := mustCreateJob(t, st, store.Job{Slug: "cxp-rescue-bind", Repo: "monolith", Agent: "codex"})
	j := &Job{
		Slug: row.Slug, ID: row.ID, Repo: "monolith", Agent: "codex",
		AccountSlug: "tmp-1", AccountEmail: "slot-one@example.com",
	}

	slug, email, why := selectRescueBind(j, contractCxpNow)
	if slug != "tmp-2" || email != "slot-two@example.com" {
		t.Fatalf("selectRescueBind = (%q, %q, %q), want (tmp-2, slot-two@example.com) — the cell's own slot is excluded by slug", slug, email, why)
	}
}
