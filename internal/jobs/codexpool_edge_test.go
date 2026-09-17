package jobs

// codexpool_edge_test.go — the EDGE dimension of slice JOBS in
// artifacts/50-contract.md (the codex account pool): the refusals, boundaries,
// degradations and cross-pool collisions of slotDirFor, accountEnvPrefix,
// decideBindCodex, boundCountsFor, the codex usage snapshot pair, the codex
// limit reason, parseLimitReset's resets_at arm, CellHealth's codex arm, the
// rescue resume's rollout placement, the two codex prompts and the side-thread
// refusal — plus G1 (a claude job's observable output is unchanged), G3 (kraken
// writes into a slot only through PlaceCodexRollout), G4 and G5.
//
// Authored BLIND. internal/jobs/codexusage.go and internal/core/codexpool.go
// were never read by any route (Read, cat, grep, sed, git show/diff), and
// neither were the changed regions of binding.go, cell.go, limitrescue.go,
// status.go or thread.go. What WAS read: the contract, 50-components.md,
// 52-testing-plan.md, the pre-existing test files in this package (for
// signatures and fixture idioms) and internal/jobs/job.go's Job struct, which
// the contract cites as unchanged.
//
// The sibling blind author (athena-logic) owns the stated invariants and the
// happy paths. What lives here is the other half: the slug that names a slot in
// the WRONG pool, the quote inside a pool path, the pin that must NOT park a
// codex hit, the placement that must not happen.
//
// Every package-level identifier is prefixed `edgeCxp` and every test is
// `TestEdgeCxp…`: this package's declaration space is shared with the other
// blind author, with the implementation, and with several pre-existing `edge*`
// families.

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"kraken/internal/core"
	"kraken/internal/store"
)

// edgeCxpNow is a fixed clock, so a freshness verdict never depends on when the
// suite ran (the pre-existing account_edge_test.go's reason, restated).
var edgeCxpNow = time.UnixMilli(1_760_000_000_000)

// edgeCxpBannedPromptTokens are the three Claude Code teammate verbs a codex
// zeus must never be told to use: codex has no Agent tool, no SendMessage and
// no TaskStop, so an instruction to use one is an instruction it will spend a
// turn failing to follow.
var edgeCxpBannedPromptTokens = []string{"TaskStop", "SendMessage", "teammate"}

// edgeCxpSandbox isolates HOME and gives the test its own codex pool AND its
// own claude pool (G5). Both are needed by almost every case here, because the
// obligations under test are about which of the two a slug is resolved in.
// Returns (codexPool, claudePool); neither `accounts` dir is created.
func edgeCxpSandbox(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	codexPool := filepath.Join(home, "codex-pool")
	claudePool := filepath.Join(home, "claude-pool")
	t.Setenv("CODEX_POOL_DIR", codexPool)
	t.Setenv("CLAUDE_POOL_DIR", claudePool)
	if err := os.MkdirAll(claudePool, 0o755); err != nil {
		t.Fatalf("create claude pool: %v", err)
	}
	return codexPool, claudePool
}

// edgeCxpMakeCodexSlot creates a USABLE codex slot: the directory plus a
// non-empty auth.json. The contract's probe never reads the content, so the
// body is a single byte and carries nothing resembling a credential.
func edgeCxpMakeCodexSlot(t *testing.T, slug string) string {
	t.Helper()
	dir := core.CodexSlotDir(slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir codex slot %s: %v", slug, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write codex auth.json: %v", err)
	}
	return dir
}

// edgeCxpMakeClaudeSlot creates a claude pool slot directory of the same name,
// which is what every cross-pool collision fixture here needs: a pool slug is a
// slugified email, so one person's account really can name a slot in both.
func edgeCxpMakeClaudeSlot(t *testing.T, claudePool, slug string) string {
	t.Helper()
	dir := filepath.Join(claudePool, slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir claude slot %s: %v", slug, err)
	}
	return dir
}

// edgeCxpRow is a codex usage row that clears every eligibility gate the
// unchanged selector applies: an identity, a session window well under the bar,
// and a FetchedAt at the fixed now. Cases break exactly one thing at a time.
func edgeCxpRow(slug string, sessionPct float64) core.AccountUsage {
	return core.AccountUsage{
		Slug:      slug,
		Email:     slug + "@example.test",
		Session:   &core.UsageWindow{Percent: sessionPct},
		FetchedAt: edgeCxpNow.UnixMilli(),
	}
}

func edgeCxpSnap(rows ...core.AccountUsage) core.UsageSnapshot {
	return core.UsageSnapshot{FetchedAt: edgeCxpNow.UnixMilli(), Accounts: rows}
}

func edgeCxpAlways(string) bool { return true }
func edgeCxpNever(string) bool  { return false }

// edgeCxpPinManual pins rotation MANUAL through the package's own writer rather
// than by hand-writing ~/.kraken/rotation.json, and then verifies the pin took.
// The verification is the point: a hand-written file in a shape the reader does
// not accept degrades to "auto", and every test that asserts "the pin changes
// nothing" would then pass without a pin ever existing.
func edgeCxpPinManual(t *testing.T) {
	t.Helper()
	if err := SetRotationMode("manual", "athena-edge"); err != nil {
		t.Fatalf("SetRotationMode(manual): %v", err)
	}
	if got := RotationMode(); got != "manual" {
		t.Fatalf("RotationMode() = %q after pinning manual — the fixture did not bite, so any \"the pin is ignored\" assertion below would pass vacuously", got)
	}
}

// edgeCxpCellJob stands up a store-backed codex job with its cell dir in place
// — the two things every spawn-command renderer needs.
func edgeCxpCellJob(t *testing.T, st *store.Store, slug, agent, accountSlug string) *Job {
	t.Helper()
	row := mustCreateJob(t, st, store.Job{Slug: slug, Repo: "kraken", State: "working"})
	j := &Job{ID: row.ID, Slug: row.Slug, Repo: "kraken", Agent: agent, AccountSlug: accountSlug}
	if err := os.MkdirAll(cellDir(j), 0o755); err != nil {
		t.Fatalf("create cell dir: %v", err)
	}
	return j
}

// edgeCxpStubCodexLauncher points KRAKEN_CODEX at a stub that answers
// `--version` above the resolver's floor. Without it the launcher ladder walks
// an isolated HOME's (absent) nvm dir and then PATH, where this machine's codex
// is below the floor — so a codex command could not be rendered at all and
// every assertion about its SHAPE would fail for a reason that is not the
// contract's. The stub writes nothing and is never executed as codex.
func edgeCxpStubCodexLauncher(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "codex")
	body := "#!/bin/sh\necho \"codex-cli 0.154.0\"\n"
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatalf("write codex stub: %v", err)
	}
	t.Setenv("KRAKEN_CODEX", p)
	return p
}

// edgeCxpRollout writes a rollout under <home>/sessions/ and returns its path.
func edgeCxpRollout(t *testing.T, home, rel string, lines ...string) string {
	t.Helper()
	p := filepath.Join(home, "sessions", rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir rollout dir: %v", err)
	}
	body := ""
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write rollout: %v", err)
	}
	return p
}

// edgeCxpLimitRecord renders one rollout line carrying rate_limits. `reached` is
// raw JSON so a case can pass null or a string.
func edgeCxpLimitRecord(usedPercent float64, windowMinutes int, resetsAt int64, reached string) string {
	return fmt.Sprintf(
		`{"timestamp":"2026-09-17T00:14:40.724Z","type":"event_msg","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":%v,"window_minutes":%d,"resets_at":%d},"secondary":null,"rate_limit_reached_type":%s}}}`,
		usedPercent, windowMinutes, resetsAt, reached)
}

// ---------------------------------------------------------------------------
// slotDirFor
// ---------------------------------------------------------------------------

// The agent test is the literal string `codex` and nothing else. Every other
// value — "", "claude", a case variant, a typo in a hand-edited job file — is
// the claude lane, which is the pre-feature behaviour and therefore the safe
// default; a loose match would send a CLAUDE cell's
// CLAUDE_SECURESTORAGE_CONFIG_DIR at a codex pool directory that holds no
// keychain item, and every claude in that cell would come up logged out.
//
// The collision fixture is one slug present in both pools: the two dirs must
// differ, or the whole feature is one pool wearing two names.
func TestEdgeCxpSlotDirForSplitsTheTwoPoolsOnTheLiteralAgent(t *testing.T) {
	edgeCxpSandbox(t)

	const slug = "alpha"
	codexWant := core.CodexSlotDir(slug)
	claudeWant := slotDir(slug)

	if codexWant == claudeWant {
		t.Fatalf("fixture is wrong: both pools resolve %q to %q", slug, codexWant)
	}

	if got := slotDirFor("codex", slug); got != codexWant {
		t.Errorf("slotDirFor(codex, %q) = %q, want %q", slug, got, codexWant)
	}
	for _, agent := range []string{"", "claude", "Codex", "CODEX", "codex ", "codexx"} {
		if got := slotDirFor(agent, slug); got != claudeWant {
			t.Errorf("slotDirFor(%q, %q) = %q, want the CLAUDE slot dir %q — only the literal \"codex\" switches pools",
				agent, slug, got, claudeWant)
		}
	}
}

// The empty slug goes through untouched in both lanes: slotDirFor is a pure
// path derivation, and the refusal of an unbound job belongs to its callers.
// Asserting it here is what stops a "helpful" guard being added in the wrong
// layer, where it would answer "" and make the codex prefix indistinguishable
// from the claude one.
func TestEdgeCxpSlotDirForPassesTheEmptySlugThrough(t *testing.T) {
	edgeCxpSandbox(t)
	if got, want := slotDirFor("codex", ""), core.CodexSlotDir(""); got != want {
		t.Errorf("slotDirFor(codex, \"\") = %q, want %q", got, want)
	}
	if got, want := slotDirFor("claude", ""), slotDir(""); got != want {
		t.Errorf("slotDirFor(claude, \"\") = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// accountEnvPrefix
// ---------------------------------------------------------------------------

// The codex prefix is refused whenever the stamped slug does not name a USABLE
// codex slot, and the empty prefix is the only safe answer: exporting CODEX_HOME
// at a missing directory makes codex create an empty, logged-out home there —
// a pane that opens, fails to authenticate, and leaves the board reading the
// cell as working.
//
// The rows are every reachable way a codex job carries a slug that is not one:
// never bound, a slot deleted since the bind, a slot dir with no auth.json, an
// empty auth.json, and — the collision fixture — a slug stamped by the CLAUDE
// binder before this change, which names a real slot in the other pool.
func TestEdgeCxpAccountEnvPrefixRefusesEveryNonUsableCodexSlug(t *testing.T) {
	_, claudePool := edgeCxpSandbox(t)

	// A real, healthy CLAUDE slot named `shared` — the cross-pool decoy.
	edgeCxpMakeClaudeSlot(t, claudePool, "shared")

	mk := func(name string, fn func(slug string)) string {
		fn(name)
		return name
	}

	cases := []struct {
		name string
		slug string
	}{
		{"never bound", ""},
		{"a slug naming nothing at all", "vanished"},
		{"a slug naming a claude slot, not a codex one", "shared"},
		{"a codex slot dir with no auth.json", mk("nodircreds", func(slug string) {
			if err := os.MkdirAll(core.CodexSlotDir(slug), 0o755); err != nil {
				t.Fatal(err)
			}
		})},
		{"a codex slot whose auth.json is empty", mk("emptycreds", func(slug string) {
			dir := core.CodexSlotDir(slug)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "auth.json"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		})},
		{"a codex slot whose auth.json is a directory", mk("dircreds", func(slug string) {
			if err := os.MkdirAll(filepath.Join(core.CodexSlotDir(slug), "auth.json"), 0o755); err != nil {
				t.Fatal(err)
			}
		})},
		{"the slot path is a regular file", mk("filecreds", func(slug string) {
			if err := os.WriteFile(core.CodexSlotDir(slug), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		})},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			j := &Job{Slug: "cx", Agent: "codex", AccountSlug: c.slug}
			if got := accountEnvPrefix(j); got != "" {
				t.Errorf("accountEnvPrefix = %q, want the empty prefix", got)
			}
		})
	}
}

// Neither agent may ever be handed the other's variable. A codex cell carrying
// CLAUDE_SECURESTORAGE_CONFIG_DIR bills a claude slot it cannot use, and a
// claude cell carrying CODEX_HOME points every claude at a directory with no
// keychain item behind it.
//
// The fixture makes the SAME slug usable in both pools, so a renderer that
// picked the variable off "which slot dir exists" rather than off the job's
// agent still passes its own eyeball test and fails here.
func TestEdgeCxpAccountEnvPrefixNeverCrossesTheTwoVariables(t *testing.T) {
	_, claudePool := edgeCxpSandbox(t)
	const slug = "both"
	edgeCxpMakeCodexSlot(t, slug)
	edgeCxpMakeClaudeSlot(t, claudePool, slug)

	codexPrefix := accountEnvPrefix(&Job{Slug: "cx", Agent: "codex", AccountSlug: slug})
	if strings.Contains(codexPrefix, "CLAUDE_SECURESTORAGE_CONFIG_DIR") {
		t.Errorf("a codex job's prefix carries the claude variable: %q", codexPrefix)
	}
	if !strings.HasPrefix(codexPrefix, "CODEX_HOME='") {
		t.Errorf("a codex job's prefix = %q, want it to lead with CODEX_HOME='", codexPrefix)
	}
	if !strings.Contains(codexPrefix, core.CodexSlotDir(slug)) {
		t.Errorf("a codex job's prefix = %q, want the CODEX pool's dir %q", codexPrefix, core.CodexSlotDir(slug))
	}

	for _, agent := range []string{"", "claude"} {
		claudePrefix := accountEnvPrefix(&Job{Slug: "cl", Agent: agent, AccountSlug: slug})
		if strings.Contains(claudePrefix, "CODEX_HOME") {
			t.Errorf("a claude job (agent %q) prefix carries CODEX_HOME: %q", agent, claudePrefix)
		}
		if !strings.Contains(claudePrefix, filepath.Join(claudePool, slug)) {
			t.Errorf("a claude job (agent %q) prefix = %q, want the CLAUDE pool's dir", agent, claudePrefix)
		}
	}
}

// G1 stated where it is cheapest to break: a claude job's prefix is unchanged
// by this feature, including the case the claude lane has always allowed and the
// codex lane refuses — a stamped slug with no slot directory behind it. The
// claude prefix has never had a usable-guard at this layer (the guard lives in
// the binder's keychain probe), and adding one here while adding the codex arm
// would silently unbind every claude cell whose slot dir is absent.
func TestEdgeCxpAccountEnvPrefixLeavesTheClaudeLaneAlone(t *testing.T) {
	_, claudePool := edgeCxpSandbox(t)

	j := &Job{Slug: "cl", AccountSlug: "slot-a"} // agent "" — the claude default
	want := "CLAUDE_SECURESTORAGE_CONFIG_DIR='" + claudePool + "/slot-a' "
	if got := accountEnvPrefix(j); got != want {
		t.Errorf("accountEnvPrefix (claude, no slot dir on disk) = %q, want %q", got, want)
	}

	j.AccountSlug = ""
	if got := accountEnvPrefix(j); got != "" {
		t.Errorf("accountEnvPrefix (claude, unbound) = %q, want the empty prefix", got)
	}
}

// The prefix's SHAPE, pinned whole because each half fails silently alone. The
// value is single-quoted unconditionally (a renderer that quoted only when it
// must produces the right string on today's paths and a truncated directory the
// first time one carries a space), each embedded `'` is written as the four
// characters quote, backslash, quote, quote (spelled out rather than shown,
// because gofmt rewrites a doubled apostrophe inside a doc comment), and
// exactly one space separates the assignment from the launcher — two would put
// an empty word in command position and none would make the launcher part of
// the value.
//
// The fixture forces the escape by putting an apostrophe in the POOL ROOT,
// which is reachable: CODEX_POOL_DIR is a knob and a home directory can carry
// one.
func TestEdgeCxpAccountEnvPrefixQuotesAndEscapesUnconditionally(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	pool := filepath.Join(home, "carl's pool")
	t.Setenv("CODEX_POOL_DIR", pool)

	const slug = "tmp-1"
	dir := core.CodexSlotDir(slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dir, "'") {
		t.Fatalf("fixture is wrong: %q carries no apostrophe to escape", dir)
	}

	got := accountEnvPrefix(&Job{Slug: "cx", Agent: "codex", AccountSlug: slug})
	want := "CODEX_HOME='" + strings.ReplaceAll(dir, "'", `'\''`) + "' "
	if got != want {
		t.Errorf("accountEnvPrefix =\n  %q\nwant\n  %q", got, want)
	}
	if !strings.HasSuffix(got, "' ") || strings.HasSuffix(got, "'  ") {
		t.Errorf("prefix = %q, want exactly one trailing space after the closing quote", got)
	}
}

// The round trip stated as a test, because it is the one property that ties the
// two halves of the delivery mechanism together: discovery reads the pane's
// start command back with core.CodexHomeFromCommand, so whatever quoting this
// function emits, that parser must undo EXACTLY. Both spellings are covered —
// an ordinary pool path and one carrying the apostrophe the escape exists for —
// and the unusable case must round-trip to "" rather than to some partial home.
func TestEdgeCxpAccountEnvPrefixIsTheInverseOfCodexHomeFromCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	for name, pool := range map[string]string{
		"an ordinary pool path":    filepath.Join(home, "codex-pool"),
		"a pool path with a quote": filepath.Join(home, "carl's codex-pool"),
		"a pool path with a space": filepath.Join(home, "my codex pool"),
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("CODEX_POOL_DIR", pool)
			const slug = "tmp-1"
			dir := core.CodexSlotDir(slug)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}

			j := &Job{Slug: "cx", Agent: "codex", AccountSlug: slug}
			cmd := accountEnvPrefix(j) + "codex"
			if got := core.CodexHomeFromCommand(cmd); got != dir {
				t.Errorf("CodexHomeFromCommand(%q) = %q, want %q", cmd, got, dir)
			}

			// and the refusal round-trips to the machine-login answer
			j.AccountSlug = "not-a-slot"
			cmd = accountEnvPrefix(j) + "codex"
			if got := core.CodexHomeFromCommand(cmd); got != "" {
				t.Errorf("an unusable slug produced %q, which parses back to %q — want the empty prefix and the empty home", cmd, got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// codexSlotUsable seam
// ---------------------------------------------------------------------------

// The seam exists so the binder can be tested without a pool on disk, and its
// DEFAULT has to be the real probe: a seam that defaulted to "always usable"
// would make every unit test pass while every production bind pointed a cell at
// a directory nothing had checked.
func TestEdgeCxpCodexSlotUsableSeamDefaultsToTheRealProbe(t *testing.T) {
	edgeCxpSandbox(t)
	var _ func(string) bool = codexSlotUsable

	good := edgeCxpMakeCodexSlot(t, "good")
	bare := core.CodexSlotDir("bare")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		dir  string
		want bool
	}{
		{good, true},
		{bare, false},
		{core.CodexSlotDir("missing"), false},
		{"", false},
	} {
		if got := codexSlotUsable(c.dir); got != c.want {
			t.Errorf("codexSlotUsable(%q) = %v, want %v", c.dir, got, c.want)
		}
		if got, want := codexSlotUsable(c.dir), core.CodexSlotUsable(c.dir); got != want {
			t.Errorf("codexSlotUsable(%q) = %v but core.CodexSlotUsable = %v — the seam's default must BE the real probe", c.dir, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// decideBindCodex
// ---------------------------------------------------------------------------

// Three shapes of "there is no codex pool" all answer with the same fixed
// sentence, which is what the board's bind-skipped event renders. It is a
// distinct string from the per-slot reasons below precisely because it is a
// different operator action: install a pool, versus log one slot back in.
func TestEdgeCxpDecideBindCodexWithNoRowsAtAll(t *testing.T) {
	edgeCxpSandbox(t)
	for name, snap := range map[string]core.UsageSnapshot{
		"the zero snapshot":                    {},
		"an empty accounts slice":              edgeCxpSnap(),
		"a snapshot with only a fetched stamp": {FetchedAt: edgeCxpNow.UnixMilli()},
	} {
		t.Run(name, func(t *testing.T) {
			slug, email, reason := decideBindCodex(snap, nil, "", "", "", edgeCxpAlways, edgeCxpNow)
			if slug != "" || email != "" {
				t.Errorf("(slug, email) = (%q, %q), want the empty pair", slug, email)
			}
			if reason != "no codex pool accounts" {
				t.Errorf("reason = %q, want %q", reason, "no codex pool accounts")
			}
		})
	}
}

// Every slot excluded by the usable probe says so BY NAME, joined with "; ".
// One line for a whole dry pool would be indistinguishable from "no pool", and
// the two need different fixes — this is the reason string carl reads on the
// card when a cell comes up unbound.
func TestEdgeCxpDecideBindCodexNamesEverySlotTheProbeRefused(t *testing.T) {
	edgeCxpSandbox(t)
	snap := edgeCxpSnap(edgeCxpRow("tmp-1", 5), edgeCxpRow("tmp-2", 6))

	slug, email, reason := decideBindCodex(snap, nil, "", "", "", edgeCxpNever, edgeCxpNow)
	if slug != "" || email != "" {
		t.Fatalf("(slug, email) = (%q, %q), want the empty pair", slug, email)
	}
	if reason != "tmp-1: no auth.json; tmp-2: no auth.json" {
		t.Errorf("reason = %q, want %q", reason, "tmp-1: no auth.json; tmp-2: no auth.json")
	}
}

// One refused slot must fall OUT of the ranking rather than fail the bind: the
// next slot is a perfectly good answer, and an unbound cell over one logged-out
// slot is the pre-feature behaviour applied where it is not needed.
func TestEdgeCxpDecideBindCodexSkipsOneRefusedSlotAndBindsTheNext(t *testing.T) {
	edgeCxpSandbox(t)
	snap := edgeCxpSnap(edgeCxpRow("tmp-1", 5), edgeCxpRow("tmp-2", 50))
	probe := func(dir string) bool { return dir != core.CodexSlotDir("tmp-1") }

	slug, email, reason := decideBindCodex(snap, nil, "", "", "", probe, edgeCxpNow)
	if slug != "tmp-2" || email != "tmp-2@example.test" {
		t.Errorf("(slug, email) = (%q, %q) (reason %q), want (tmp-2, tmp-2@example.test)", slug, email, reason)
	}
}

// The probe is asked about the LITERAL slot dir core.CodexSlotDir derives, not a
// re-cleaned or re-joined variant: the value the spawn exports and the path the
// guard checked have to be one string, or the guard answers about a directory
// codex will never open.
func TestEdgeCxpDecideBindCodexProbesTheLiteralSlotDir(t *testing.T) {
	edgeCxpSandbox(t)
	var seen []string
	probe := func(dir string) bool { seen = append(seen, dir); return true }

	decideBindCodex(edgeCxpSnap(edgeCxpRow("tmp-1", 5)), nil, "", "", "", probe, edgeCxpNow)

	want := core.CodexSlotDir("tmp-1")
	found := false
	for _, got := range seen {
		if got == want {
			found = true
		}
	}
	if !found {
		t.Errorf("the probe saw %v, want it asked about exactly %q", seen, want)
	}
}

// The main-identity exclusion compares EMAILS, case-folded, and nothing else.
// The collision fixture is a slot whose SLUG is spelled exactly like the main
// email while its own email is different: a comparison that reached for the
// slug would exclude a perfectly good slot and, on a two-slot pool, halve it.
func TestEdgeCxpDecideBindCodexExcludesOnEmailNeverOnSlug(t *testing.T) {
	edgeCxpSandbox(t)
	const mainEmail = "machine@example.test"

	t.Run("a row whose email is main's is excluded, folding case", func(t *testing.T) {
		mirror := edgeCxpRow("mirror", 1)
		mirror.Email = "Machine@Example.TEST"
		slug, email, _ := decideBindCodex(edgeCxpSnap(mirror), nil, mainEmail, "", "", edgeCxpAlways, edgeCxpNow)
		if slug != "" || email != "" {
			t.Errorf("(slug, email) = (%q, %q), want the empty pair — that slot IS the machine login", slug, email)
		}
	})

	t.Run("and the other direction of the fold", func(t *testing.T) {
		mirror := edgeCxpRow("mirror", 1)
		mirror.Email = mainEmail
		slug, _, _ := decideBindCodex(edgeCxpSnap(mirror), nil, "MACHINE@EXAMPLE.TEST", "", "", edgeCxpAlways, edgeCxpNow)
		if slug != "" {
			t.Errorf("slug = %q, want the empty pick", slug)
		}
	})

	t.Run("a row whose SLUG collides with main's email is still a candidate", func(t *testing.T) {
		collide := edgeCxpRow(mainEmail, 5) // slug spelled like the email
		collide.Email = "someone-else@example.test"
		slug, email, reason := decideBindCodex(edgeCxpSnap(collide), nil, mainEmail, "", "", edgeCxpAlways, edgeCxpNow)
		if slug != mainEmail || email != "someone-else@example.test" {
			t.Errorf("(slug, email) = (%q, %q) (reason %q), want (%q, someone-else@example.test) — the exclusion reads Email, never Slug",
				slug, email, reason, mainEmail)
		}
	})

	t.Run("an unknown main identity applies no email filter at all", func(t *testing.T) {
		// A ~/.codex/auth.json that cannot be read yields "", and refusing to
		// bind anything on that machine would be strictly worse than binding a
		// cell to a slot that might be the machine's own login.
		slug, _, reason := decideBindCodex(edgeCxpSnap(edgeCxpRow("tmp-1", 5)), nil, "", "", "", edgeCxpAlways, edgeCxpNow)
		if slug != "tmp-1" {
			t.Errorf("slug = %q (reason %q), want tmp-1", slug, reason)
		}
	})
}

// excludeSlug is the rescue's own exclusion and it keys on the SLUG, because
// the email-keyed one is not enough: the rescue is moving a cell OFF the slot
// it is on, and that slot's row is otherwise the healthiest-looking candidate.
// Empty means exclude nothing, and the collision fixture is an excludeSlug
// spelled like some row's EMAIL, which must exclude no one.
func TestEdgeCxpDecideBindCodexExcludeSlugIsExactAndOptional(t *testing.T) {
	edgeCxpSandbox(t)
	snap := edgeCxpSnap(edgeCxpRow("tmp-1", 5), edgeCxpRow("tmp-2", 50))

	t.Run("the named slug is out even when it ranks first", func(t *testing.T) {
		slug, _, _ := decideBindCodex(snap, nil, "", "", "tmp-1", edgeCxpAlways, edgeCxpNow)
		if slug != "tmp-2" {
			t.Errorf("slug = %q, want tmp-2 — tmp-1 is the excluded slot", slug)
		}
	})

	t.Run("excluding the only row leaves nothing", func(t *testing.T) {
		slug, email, _ := decideBindCodex(edgeCxpSnap(edgeCxpRow("tmp-1", 5)), nil, "", "", "tmp-1", edgeCxpAlways, edgeCxpNow)
		if slug != "" || email != "" {
			t.Errorf("(slug, email) = (%q, %q), want the empty pair", slug, email)
		}
	})

	t.Run("the empty excludeSlug excludes nothing", func(t *testing.T) {
		slug, _, _ := decideBindCodex(snap, nil, "", "", "", edgeCxpAlways, edgeCxpNow)
		if slug != "tmp-1" {
			t.Errorf("slug = %q, want tmp-1", slug)
		}
	})

	t.Run("an excludeSlug spelled like a row's email excludes no one", func(t *testing.T) {
		slug, _, _ := decideBindCodex(snap, nil, "", "", "tmp-1@example.test", edgeCxpAlways, edgeCxpNow)
		if slug != "tmp-1" {
			t.Errorf("slug = %q, want tmp-1 — excludeSlug is matched against Slug, never Email", slug)
		}
	})
}

// A previous slug that names nothing in this pool is simply not sticky. This is
// job 428's state on disk today: a codex job carrying a slug the CLAUDE binder
// stamped. It must fall through to ranking rather than stick, error, or bind
// the claude slot.
func TestEdgeCxpDecideBindCodexCrossPoolPrevSlugFallsThroughToRanking(t *testing.T) {
	_, claudePool := edgeCxpSandbox(t)
	edgeCxpMakeClaudeSlot(t, claudePool, "claude-only") // real, in the OTHER pool

	slug, email, reason := decideBindCodex(
		edgeCxpSnap(edgeCxpRow("tmp-1", 5)), nil, "", "claude-only", "", edgeCxpAlways, edgeCxpNow)
	if slug != "tmp-1" || email != "tmp-1@example.test" {
		t.Errorf("(slug, email) = (%q, %q) (reason %q), want (tmp-1, tmp-1@example.test)", slug, email, reason)
	}
}

// Q4, and the one that is easiest to reintroduce by accident: the codex lane
// takes NO rotation mode. The signature carries no mode parameter, which is
// half the guarantee; the other half is that nothing reads the mode off disk
// behind it, so the fixture writes a MANUAL rotation.json into the isolated
// HOME and the bind must still happen.
func TestEdgeCxpDecideBindCodexIgnoresAManualRotationPin(t *testing.T) {
	edgeCxpSandbox(t)
	var _ func(core.UsageSnapshot, map[string]int, string, string, string, func(string) bool, time.Time) (string, string, string) = decideBindCodex

	edgeCxpPinManual(t)

	slug, _, reason := decideBindCodex(edgeCxpSnap(edgeCxpRow("tmp-1", 5)), nil, "", "", "", edgeCxpAlways, edgeCxpNow)
	if slug != "tmp-1" {
		t.Errorf("slug = %q (reason %q), want tmp-1 — a manual pin governs claude's main login and nothing in the codex lane", slug, reason)
	}
}

// Amendment 1: "on an empty pick the returned reason is this function's own
// collected reasons FOLLOWED BY the reason `core.SelectBindTarget` returned
// (when that is non-empty), all joined by `; `".
//
// The assertion is STRUCTURAL rather than literal, and deliberately so: the
// selector's own wording is not this contract's to state, so pinning its text
// here would make a reword of an unrelated function fail a codex test. What the
// amendment does fix is the ORDER and the JOIN, and three runs over the same
// pool separate those from everything else —
//
//	own    = a pool of one slot the probe refuses (this function's reason only)
//	sel    = a pool of one window-less slot, which this function passes through
//	         as a candidate and the selector then declines (the selector's only)
//	both   = the two together, which must be exactly own + "; " + sel
//
// A concatenation in the other order, a different separator, a dropped
// selector reason, or a borrowed "no codex pool accounts" each fail one of the
// three. The window-less row is the shape the selector declines — and the
// reason C2 copies a lone weekly into Session in the first place.
func TestEdgeCxpDecideBindCodexEmptyPickJoinsItsOwnReasonsThenTheSelectors(t *testing.T) {
	edgeCxpSandbox(t)
	refused := edgeCxpRow("refused", 5)
	windowless := core.AccountUsage{Slug: "windowless", Email: "windowless@example.test", FetchedAt: edgeCxpNow.UnixMilli()}
	onlyRefused := func(dir string) bool { return dir != core.CodexSlotDir("refused") }

	slug, email, own := decideBindCodex(edgeCxpSnap(refused), nil, "", "", "", onlyRefused, edgeCxpNow)
	if slug != "" || email != "" {
		t.Fatalf("(slug, email) = (%q, %q) for a pool of one refused slot, want the empty pair", slug, email)
	}
	if own != "refused: no auth.json" {
		t.Fatalf("this function's own reason = %q, want %q", own, "refused: no auth.json")
	}

	slug, email, sel := decideBindCodex(edgeCxpSnap(windowless), nil, "", "", "", edgeCxpAlways, edgeCxpNow)
	if slug != "" || email != "" {
		t.Fatalf("(slug, email) = (%q, %q) for a pool of one window-less slot, want the empty pair", slug, email)
	}
	if sel == "" {
		t.Fatal("the selector declined the only candidate and reported nothing; this case cannot separate the join without a selector reason")
	}
	if sel == "no codex pool accounts" {
		t.Errorf("reason = %q, but the snapshot HAD a row — that sentence is reserved for a snapshot with no rows at all", sel)
	}

	_, _, both := decideBindCodex(edgeCxpSnap(refused, windowless), nil, "", "", "", onlyRefused, edgeCxpNow)
	if want := own + "; " + sel; both != want {
		t.Errorf("reason =\n  %q\nwant\n  %q\n(this function's own reasons FIRST, then the selector's, joined by \"; \")", both, want)
	}
}

// The reserved sentence is reserved: a snapshot with rows never answers with
// it, however thoroughly everything was excluded. The two say different things
// to carl — install a pool, versus look at why these slots were declined.
func TestEdgeCxpDecideBindCodexNeverBorrowsTheNoAccountsSentence(t *testing.T) {
	edgeCxpSandbox(t)
	for name, call := range map[string]func() string{
		"every slot refused by the probe": func() string {
			_, _, r := decideBindCodex(edgeCxpSnap(edgeCxpRow("tmp-1", 5)), nil, "", "", "", edgeCxpNever, edgeCxpNow)
			return r
		},
		"every row excluded as main's mirror": func() string {
			_, _, r := decideBindCodex(edgeCxpSnap(edgeCxpRow("tmp-1", 5)), nil, "tmp-1@example.test", "", "", edgeCxpAlways, edgeCxpNow)
			return r
		},
		"the only row excluded by slug": func() string {
			_, _, r := decideBindCodex(edgeCxpSnap(edgeCxpRow("tmp-1", 5)), nil, "", "", "tmp-1", edgeCxpAlways, edgeCxpNow)
			return r
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := call(); got == "no codex pool accounts" {
				t.Errorf("reason = %q, but the snapshot carried a row", got)
			}
		})
	}
}

// The counts map is the selector's first ranking key and it is handed through
// untouched; a nil map is the ordinary shape on a machine with no live cells and
// must not be a panic (G4's spirit applied to an in-process input).
func TestEdgeCxpDecideBindCodexAcceptsNilCounts(t *testing.T) {
	edgeCxpSandbox(t)
	if slug, _, _ := decideBindCodex(edgeCxpSnap(edgeCxpRow("tmp-1", 5)), nil, "", "", "", edgeCxpAlways, edgeCxpNow); slug != "tmp-1" {
		t.Errorf("slug = %q, want tmp-1", slug)
	}
}

// ---------------------------------------------------------------------------
// boundCountsFor
// ---------------------------------------------------------------------------

// The per-agent split, with the collision that makes it necessary: slug `alpha`
// carries one live cell in EACH pool. Counting them together would make both
// pools' `alpha` look twice as busy as it is, and on a two-slot pool that is
// enough to push every subsequent bind onto the other slot permanently.
//
// The agent argument is the same literal test slotDirFor uses: only "codex"
// selects codex jobs, and every other value — including a case variant — selects
// the non-codex ones, so a typo can never produce a THIRD, empty tally.
func TestEdgeCxpBoundCountsForSplitsACollidingSlugByAgent(t *testing.T) {
	live := func(*Job) bool { return true }
	rows := []*Job{
		{Slug: "cx", Agent: "codex", AccountSlug: "alpha"},
		{Slug: "cl", AccountSlug: "alpha"},
		{Slug: "cl2", Agent: "claude", AccountSlug: "alpha"},
	}

	codex := boundCountsFor(rows, "codex", live)
	if codex["alpha"] != 1 {
		t.Errorf("codex counts = %v, want alpha at 1 — only the codex job counts", codex)
	}

	for _, agent := range []string{"claude", "", "CODEX", "Codex"} {
		got := boundCountsFor(rows, agent, live)
		if got["alpha"] != 2 {
			t.Errorf("boundCountsFor(agent %q) = %v, want alpha at 2 — every non-codex job counts in the claude lane", agent, got)
		}
	}
}

// What must NOT count is where the damage is: an unbound job would create a
// phantom entry under the empty slug, and a torn-down one would make its slot
// look permanently busy. Both properties are inherited from boundCounts and
// both have to survive the agent filter.
func TestEdgeCxpBoundCountsForIgnoresUnboundAndNonLiveJobs(t *testing.T) {
	live := func(*Job) bool { return true }
	dead := func(*Job) bool { return false }

	t.Run("no jobs at all", func(t *testing.T) {
		for name, in := range map[string][]*Job{"nil": nil, "empty": {}} {
			if got := boundCountsFor(in, "codex", live); len(got) != 0 {
				t.Errorf("%s: boundCountsFor = %v, want no entries", name, got)
			}
		}
	})

	t.Run("an unbound codex job contributes nothing", func(t *testing.T) {
		got := boundCountsFor([]*Job{{Slug: "a", Agent: "codex"}, {Slug: "b", Agent: "codex", AccountEmail: "x@example.test"}}, "codex", live)
		if len(got) != 0 {
			t.Errorf("boundCountsFor = %v, want no entries — no AccountSlug is unbound whatever else the row carries", got)
		}
	})

	t.Run("a bound but non-live codex job contributes nothing", func(t *testing.T) {
		got := boundCountsFor([]*Job{{Slug: "a", Agent: "codex", AccountSlug: "tmp-1"}}, "codex", dead)
		if len(got) != 0 {
			t.Errorf("boundCountsFor = %v, want no entries — the tally is LIVE cells", got)
		}
	})

	t.Run("the predicate is asked only about jobs of the matching agent", func(t *testing.T) {
		var asked []string
		boundCountsFor([]*Job{
			{Slug: "cx", Agent: "codex", AccountSlug: "tmp-1"},
			{Slug: "cl", AccountSlug: "slot-a"},
		}, "codex", func(j *Job) bool { asked = append(asked, j.Slug); return true })
		if !reflect.DeepEqual(asked, []string{"cx"}) {
			t.Errorf("the predicate saw %v, want only the codex job — asking about the other pool's jobs is work with no answer", asked)
		}
	})
}

// G1 for the tally: the old entry point keeps its current, agent-blind
// behaviour, because it is still what every pre-existing caller and test uses.
// The same colliding fixture that splits into 1 and 2 above must sum to 3 here.
func TestEdgeCxpBoundCountsStaysAgentBlind(t *testing.T) {
	live := func(*Job) bool { return true }
	got := boundCounts([]*Job{
		{Slug: "cx", Agent: "codex", AccountSlug: "alpha"},
		{Slug: "cl", AccountSlug: "alpha"},
		{Slug: "cl2", Agent: "claude", AccountSlug: "alpha"},
	}, live)
	if got["alpha"] != 3 {
		t.Errorf("boundCounts = %v, want alpha at 3 — the un-suffixed verb is unchanged", got)
	}
}

// ---------------------------------------------------------------------------
// CodexUsagePass / ReadCodexUsageSnapshot
// ---------------------------------------------------------------------------

// The codex snapshot lives under its own key, and the two passes are strictly
// independent: a codex pass must never write, clear or disturb the claude
// snapshot. Mixing them would put rows with no keychain item behind them in
// front of the claude binder, which excludes on exactly that probe — the
// failure would be a silently halved claude pool.
func TestEdgeCxpUsagePassNeverTouchesTheClaudeSnapshot(t *testing.T) {
	useTestStore(t)
	edgeCxpSandbox(t)

	t.Run("no claude snapshot is created by a codex pass", func(t *testing.T) {
		if _, ok := ReadUsageSnapshot(); ok {
			t.Fatal("fixture is wrong: a claude snapshot exists before the pass")
		}
		if err := CodexUsagePass(); err != nil {
			t.Fatalf("CodexUsagePass on an empty pool: %v", err)
		}
		if _, ok := ReadUsageSnapshot(); ok {
			t.Error("the codex pass created a CLAUDE snapshot")
		}
	})

	t.Run("an existing claude snapshot survives byte-for-byte", func(t *testing.T) {
		want := core.UsageSnapshot{FetchedAt: 12345, Accounts: []core.AccountUsage{{Slug: "main", Current: true}}}
		if err := writeUsageSnapshot(want, UsageSnapshotPath()); err != nil {
			t.Fatalf("seed claude snapshot: %v", err)
		}
		if err := CodexUsagePass(); err != nil {
			t.Fatalf("CodexUsagePass: %v", err)
		}
		got, ok := ReadUsageSnapshot()
		if !ok {
			t.Fatal("the claude snapshot is gone after a codex pass")
		}
		if got.FetchedAt != want.FetchedAt || len(got.Accounts) != 1 || got.Accounts[0].Slug != "main" {
			t.Errorf("claude snapshot after a codex pass = %+v, want %+v", got, want)
		}
	})
}

// An empty pool is still a pass that RAN: the snapshot is written with a fresh
// stamp and no rows, which is what lets the board tell "codex is not set up
// here" from "the pass has never run". Returning an error instead would put a
// red daemon line on every tick of a machine with no codex pool.
func TestEdgeCxpUsagePassOnAnEmptyPoolStillRecordsAPass(t *testing.T) {
	useTestStore(t)
	edgeCxpSandbox(t)

	before := time.Now().UnixMilli()
	if err := CodexUsagePass(); err != nil {
		t.Fatalf("CodexUsagePass on an empty pool = %v, want nil — an absent pool is a feature that is off, not a failure", err)
	}
	snap, ok := ReadCodexUsageSnapshot()
	if !ok {
		t.Fatal("ReadCodexUsageSnapshot = (_, false) after a successful pass")
	}
	if len(snap.Accounts) != 0 {
		t.Errorf("Accounts = %+v, want none", snap.Accounts)
	}
	if snap.FetchedAt < before {
		t.Errorf("FetchedAt = %d, want a stamp at or after the pass began (%d)", snap.FetchedAt, before)
	}
}

// A snapshot that was never written, and one that is corrupt, are the SAME
// answer: (zero, false). The binder's rescue arm turns that into "no codex
// usage snapshot" and leaves the cell where it is, which is the only safe
// reading — a half-decoded snapshot would rank a pool that does not exist.
func TestEdgeCxpReadCodexUsageSnapshotDegradesToNotOK(t *testing.T) {
	t.Run("never written", func(t *testing.T) {
		useTestStore(t)
		edgeCxpSandbox(t)
		snap, ok := ReadCodexUsageSnapshot()
		if ok {
			t.Errorf("ReadCodexUsageSnapshot = (%+v, true), want (zero, false)", snap)
		}
		if len(snap.Accounts) != 0 || snap.FetchedAt != 0 {
			t.Errorf("snapshot = %+v, want the zero value", snap)
		}
	})

	t.Run("corrupt", func(t *testing.T) {
		st := useTestStore(t)
		edgeCxpSandbox(t)
		if err := st.SetKV("usage_snapshot_codex", "{not json at all"); err != nil {
			t.Fatalf("seed corrupt snapshot: %v", err)
		}
		snap, ok := ReadCodexUsageSnapshot()
		if ok {
			t.Errorf("ReadCodexUsageSnapshot = (%+v, true) for a corrupt blob, want (zero, false)", snap)
		}
	})
}

// A slot that vanished from disk is dropped rather than carried forward: the
// pass rebuilds the row set from CodexPoolSlugs every time, so a `cxp remove`
// takes effect on the next tick instead of leaving a ghost the binder can pick.
func TestEdgeCxpUsagePassDropsASlotThatLeftTheDisk(t *testing.T) {
	useTestStore(t)
	edgeCxpSandbox(t)

	edgeCxpMakeCodexSlot(t, "tmp-1")
	dead := edgeCxpMakeCodexSlot(t, "tmp-2")
	if err := CodexUsagePass(); err != nil {
		t.Fatalf("CodexUsagePass: %v", err)
	}
	snap, ok := ReadCodexUsageSnapshot()
	if !ok || len(snap.Accounts) != 2 {
		t.Fatalf("first pass snapshot = %+v (ok %v), want two rows", snap, ok)
	}

	if err := os.RemoveAll(dead); err != nil {
		t.Fatal(err)
	}
	if err := CodexUsagePass(); err != nil {
		t.Fatalf("CodexUsagePass (second): %v", err)
	}
	snap, ok = ReadCodexUsageSnapshot()
	if !ok {
		t.Fatal("no snapshot after the second pass")
	}
	var slugs []string
	for _, a := range snap.Accounts {
		slugs = append(slugs, a.Slug)
	}
	if !reflect.DeepEqual(slugs, []string{"tmp-1"}) {
		t.Errorf("slugs = %v, want [tmp-1] — a removed slot is dropped, never carried forward", slugs)
	}
}

// ---------------------------------------------------------------------------
// codexLimitReason
// ---------------------------------------------------------------------------

// No evidence is no reason, and the empty string must not itself read as a
// usage limit: the health pass routes on IsUsageLimitReason, so an empty reason
// that tested true would record a limit hit for every healthy codex cell on
// every tick.
func TestEdgeCxpCodexLimitReasonIsEmptyWithoutAHit(t *testing.T) {
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

	for name, path := range map[string]string{
		"the empty path":            "",
		"a missing file":            filepath.Join(dir, "gone.jsonl"),
		"an empty rollout":          write("empty.jsonl"),
		"reached_type null":         write("null.jsonl", edgeCxpLimitRecord(99, 10080, 1790208555, "null")),
		"reached_type empty string": write("blank.jsonl", edgeCxpLimitRecord(99, 10080, 1790208555, `""`)),
		"only malformed lines":      write("torn.jsonl", "not json", `{"type":`),
	} {
		t.Run(name, func(t *testing.T) {
			if got := codexLimitReason(path); got != "" {
				t.Errorf("codexLimitReason = %q, want empty", got)
			}
		})
	}

	if IsUsageLimitReason("") {
		t.Error("IsUsageLimitReason(\"\") = true — the empty reason must never route as a usage limit")
	}
}

// The reason's exact shape, because two readers depend on it: IsUsageLimitReason
// routes on the prefix, and parseLimitReset recovers the reset time from the
// `resets_at=` clause. A reset of 0 is still spelled out — a clause-less reason
// would send the hit down the banner-regex path the codex arm exists to avoid.
func TestEdgeCxpCodexLimitReasonShape(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	for _, c := range []struct {
		name    string
		line    string
		want    string
		resetAt int64
	}{
		{
			name:    "a weekly wall with a reset stamp",
			line:    edgeCxpLimitRecord(100, 10080, 1790208555, `"weekly"`),
			want:    usageLimitReasonPrefix + "codex weekly resets_at=1790208555",
			resetAt: 1790208555,
		},
		{
			name:    "a wall whose windows name no reset",
			line:    `{"timestamp":"2026-09-17T00:14:40.724Z","type":"event_msg","payload":{"type":"token_count","rate_limits":{"primary":null,"secondary":null,"rate_limit_reached_type":"primary"}}}`,
			want:    usageLimitReasonPrefix + "codex primary resets_at=0",
			resetAt: 0,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := filepath.Join(dir, strings.ReplaceAll(c.name, " ", "-")+".jsonl")
			if err := os.WriteFile(p, []byte(c.line+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			got := codexLimitReason(p)
			if got != c.want {
				t.Fatalf("codexLimitReason =\n  %q\nwant\n  %q", got, c.want)
			}
			if !IsUsageLimitReason(got) {
				t.Errorf("IsUsageLimitReason(%q) = false — the health pass routes on this", got)
			}
			if got := parseLimitReset(got, edgeCxpNow); got != c.resetAt {
				t.Errorf("parseLimitReset(reason) = %d, want %d — the reason is what carries the reset forward", got, c.resetAt)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// parseLimitReset — the resets_at arm
// ---------------------------------------------------------------------------

// The codex arm takes the integer AS IS: no banner regex and, above all, no
// 6-hour cap. The cap exists because a claude banner cannot legitimately name a
// reset more than one window ahead; a codex weekly reset legitimately can be
// days out, and capping it would park the cell, then un-park it immediately,
// then re-park it — a rescue loop with no alarm on it.
func TestEdgeCxpParseLimitResetTakesTheCodexIntegerUncapped(t *testing.T) {
	anchor := time.Unix(1_790_000_000, 0)

	for _, c := range []struct {
		name    string
		details string
		want    int64
	}{
		{"a stamp inside any window", usageLimitReasonPrefix + "codex weekly resets_at=1790208555", 1790208555},
		{"a stamp days out, far past the 6h cap", usageLimitReasonPrefix + "codex weekly resets_at=1790900000", 1790900000},
		{"a stamp years out", usageLimitReasonPrefix + "codex weekly resets_at=4102444800", 4102444800},
		{"a stamp in the past", usageLimitReasonPrefix + "codex weekly resets_at=1000000000", 1000000000},
		{"the clause wins over a banner phrase beside it", "usage limit reached · resets 4:50am (America/New_York) resets_at=1790208555", 1790208555},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := parseLimitReset(c.details, anchor); got != c.want {
				t.Errorf("parseLimitReset(%q) = %d, want %d", c.details, got, c.want)
			}
		})
	}
}

// A clause with no digits behind it is not a clause: it must fall through to the
// existing behaviour rather than parse as zero-and-therefore-due, which would
// respawn the cell on the very next tick.
func TestEdgeCxpParseLimitResetIgnoresADigitlessClause(t *testing.T) {
	anchor := time.Unix(1_790_000_000, 0)
	for _, details := range []string{
		usageLimitReasonPrefix + "codex weekly resets_at=",
		usageLimitReasonPrefix + "codex weekly resets_at=soon",
		usageLimitReasonPrefix + "codex weekly resets_at",
	} {
		if got := parseLimitReset(details, anchor); got != 0 {
			t.Errorf("parseLimitReset(%q) = %d, want 0 — there is no integer here and no banner either", details, got)
		}
	}
}

// ---------------------------------------------------------------------------
// CellHealth — the codex arm
// ---------------------------------------------------------------------------

// edgeCxpCodexZeus is a depth-0 codex pane in the cell's own tmux session,
// wearing a THREAD TITLE rather than zeus' minted name — which is what a real
// codex peer carries, and exactly why the name test cannot be applied to it.
//
// CWD is the cell dir, which edgeCxpCellJob has created: a codex zeus' brief is
// staged as `<worktree>/AGENTS.md`, so a CWD that does not exist makes the
// resume command unbuildable and the respawn never happens. (Round 1 used a
// fixed /tmp path that did not exist; that was this fixture's bug, not the
// code's.) For the CellHealth cases the field is unread either way.
func edgeCxpCodexZeus(j *Job, rollout string) *core.Peer {
	return &core.Peer{
		TmuxSession: CellSession(j),
		Pane:        "%1",
		PID:         4242,
		Agent:       "codex",
		Depth:       0,
		Role:        "main",
		Name:        "Wire the codex pool into the binder",
		SessionID:   "codex-thread-id",
		CWD:         cellDir(j),
		CodexHome:   "/tmp/edge-cxp-home",
		RolloutPath: rollout,
	}
}

// A codex cell with no zeus pane still reports today's dead-cell sentence: the
// codex arm changes which EVIDENCE is read, never what an absent process means,
// and that string is what the board renders as the cell-gone alert.
func TestEdgeCxpCellHealthCodexWithNoZeusKeepsTheDeadCellText(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	j := &Job{Slug: "cx-dead", Agent: "codex"}
	want := "zeus process is gone — restart the cell (kill the session, then start it again from the board after cleanup)"
	if got := CellHealth(j, nil); got != want {
		t.Errorf("CellHealth = %q, want %q", got, want)
	}
}

// Everything short of a recorded wall is silence. A codex peer whose rollout
// path is unknown, missing, or carries no reached type is a HEALTHY cell, and a
// health string here is a limit hit recorded against an account that is fine.
func TestEdgeCxpCellHealthCodexStaysSilentWithoutAWall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	j := &Job{Slug: "cx-quiet", Agent: "codex"}

	quiet := edgeCxpRollout(t, filepath.Join(home, "h"), "2026/09/17/rollout-quiet.jsonl",
		edgeCxpLimitRecord(42, 10080, 1790208555, "null"))

	for name, rollout := range map[string]string{
		"no rollout path at all":     "",
		"a rollout that is missing":  filepath.Join(home, "h", "sessions", "gone.jsonl"),
		"a rollout with a null type": quiet,
	} {
		t.Run(name, func(t *testing.T) {
			zeus := edgeCxpCodexZeus(j, rollout)
			if got := CellHealth(j, []*core.Peer{zeus}); got != "" {
				t.Errorf("CellHealth = %q, want empty", got)
			}
		})
	}
}

// The wall itself: a codex zeus wearing a thread title — no zeus-<slug> name
// anywhere — must still be found and its rollout read. Applying the claude
// name test here would make every codex cell permanently silent, which looks
// exactly like a healthy machine.
func TestEdgeCxpCellHealthCodexReadsTheRolloutWithoutTheNameTest(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	j := &Job{Slug: "cx-wall", Agent: "codex"}

	rollout := edgeCxpRollout(t, filepath.Join(home, "h"), "2026/09/17/rollout-wall.jsonl",
		edgeCxpLimitRecord(100, 10080, 1790208555, `"weekly"`))

	zeus := edgeCxpCodexZeus(j, rollout)
	if zeus.Name == ZeusName(j) {
		t.Fatalf("fixture is wrong: the peer is wearing zeus' minted name %q", zeus.Name)
	}

	got := CellHealth(j, []*core.Peer{zeus})
	if want := codexLimitReason(rollout); got != want {
		t.Errorf("CellHealth = %q, want %q", got, want)
	}
	if !IsUsageLimitReason(got) {
		t.Errorf("CellHealth = %q, which does not route as a usage limit", got)
	}
}

// G1 for the health pass: a CLAUDE job is never routed through the codex arm,
// even when its zeus peer happens to carry a rollout path. The claude peer here
// has no session id, which today's pass answers with silence; a codex-shaped
// reading of its RolloutPath would invent a limit hit for a claude cell and
// rebind it onto another account.
func TestEdgeCxpCellHealthClaudeJobNeverReadsARollout(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	j := &Job{Slug: "cl-wall"} // agent "" — claude

	rollout := edgeCxpRollout(t, filepath.Join(home, "h"), "2026/09/17/rollout-wall.jsonl",
		edgeCxpLimitRecord(100, 10080, 1790208555, `"weekly"`))

	zeus := &core.Peer{
		TmuxSession: CellSession(j),
		Pane:        "%1",
		Agent:       "claude",
		Depth:       0,
		Role:        "main",
		Name:        ZeusName(j),
		SessionID:   "",
		CWD:         "/tmp/edge-cxp-worktree",
		RolloutPath: rollout,
	}
	if got := CellHealth(j, []*core.Peer{zeus}); got != "" {
		t.Errorf("CellHealth for a CLAUDE job = %q, want empty — the rollout arm is codex-only", got)
	}
}

// ---------------------------------------------------------------------------
// the two codex prompts
// ---------------------------------------------------------------------------

// Neither prompt may carry a Claude Code teammate verb, and the adversarial
// fixture is a base string that carries all three: the prompts are built FROM
// the rescue's base text, so a builder that concatenated it wholesale would
// smuggle the tokens through and a codex zeus would spend its first turn trying
// to call tools it does not have.
func TestEdgeCxpPromptsNeverCarryClaudeTeammateVerbs(t *testing.T) {
	st := useTestStore(t)
	edgeCxpSandbox(t)
	j := edgeCxpCellJob(t, st, "cx-prompts", "codex", "")

	poisoned := "Your account moved to another slot. Use TaskStop to stop each teammate, " +
		"then SendMessage every teammate to resume."

	resume := codexResumePrompt(poisoned)
	if strings.TrimSpace(resume) == "" {
		t.Fatal("codexResumePrompt returned nothing — the rescue would respawn zeus with no instruction at all")
	}
	for _, tok := range edgeCxpBannedPromptTokens {
		if strings.Contains(resume, tok) {
			t.Errorf("codexResumePrompt carries %q:\n%s", tok, resume)
		}
	}

	handoff := codexHandoffPrompt(j, "/pool/accounts/tmp-1/sessions/2026/09/17/rollout-abc.jsonl", poisoned)
	if strings.TrimSpace(handoff) == "" {
		t.Fatal("codexHandoffPrompt returned nothing")
	}
	for _, tok := range edgeCxpBannedPromptTokens {
		if strings.Contains(handoff, tok) {
			t.Errorf("codexHandoffPrompt carries %q:\n%s", tok, handoff)
		}
	}
}

// The handoff prompt is the only thing a fresh codex session has to work from
// after a placement failure, so it must name BOTH the abandoned rollout's
// absolute path (the conversation, still readable on disk) and the cell's
// artifacts dir (the work). Naming neither leaves the new session with no way
// back to what it was doing, which is the whole cost of the handoff path.
func TestEdgeCxpHandoffPromptNamesTheOldRolloutAndTheArtifacts(t *testing.T) {
	st := useTestStore(t)
	edgeCxpSandbox(t)
	j := edgeCxpCellJob(t, st, "cx-handoff", "codex", "")

	const old = "/pool/accounts/tmp-1/sessions/2026/09/17/rollout-abc.jsonl"
	got := codexHandoffPrompt(j, old, "the account moved")

	if !strings.Contains(got, old) {
		t.Errorf("the handoff prompt does not name the old rollout %q:\n%s", old, got)
	}
	if !strings.Contains(got, cellDir(j)) {
		t.Errorf("the handoff prompt does not name anything under the cell dir %q:\n%s", cellDir(j), got)
	}
	if !strings.Contains(got, "artifacts") {
		t.Errorf("the handoff prompt does not name the artifacts dir:\n%s", got)
	}
}

// ---------------------------------------------------------------------------
// resumeCellAgents — the rollout placement decision
// ---------------------------------------------------------------------------

// edgeCxpStubPlace swaps the placement seam for a recorder, so a test can assert
// on WHETHER and WITH WHAT it was called without any file crossing between two
// homes. G3 in the one form a test can reach it: nothing else may write into a
// slot, so nothing else may be observed writing.
type edgeCxpPlaceCall struct{ rollout, from, to string }

func edgeCxpStubPlace(t *testing.T, dst string, err error) *[]edgeCxpPlaceCall {
	t.Helper()
	orig := placeCodexRollout
	calls := []edgeCxpPlaceCall{}
	placeCodexRollout = func(rollout, from, to string) (string, error) {
		calls = append(calls, edgeCxpPlaceCall{rollout, from, to})
		return dst, err
	}
	t.Cleanup(func() { placeCodexRollout = orig })
	return &calls
}

// edgeCxpStubRespawn swaps the pane seam for a recorder. Nothing in this file
// may SIGKILL a pane, and the command string is what several assertions read.
func edgeCxpStubRespawn(t *testing.T) *[]string {
	t.Helper()
	orig := respawnPeerPane
	cmds := []string{}
	respawnPeerPane = func(p *core.Peer, cmd string) error {
		cmds = append(cmds, cmd)
		return nil
	}
	t.Cleanup(func() { respawnPeerPane = orig })
	return &cmds
}

// The placement is CONDITIONAL, and both conditions guard against a needless
// write into an account's home directory (G3 is a bound on what kraken writes
// there, so the cheapest way to honour it is to write nothing when nothing is
// needed):
//
//   - the homes are already the same — an unbound cell being resumed in place,
//     or a rebind that landed on the slot the cell was already on;
//   - there is no rollout to place, which is every codex peer discovery could
//     not resolve a thread for.
func TestEdgeCxpResumeSkipsThePlacementWhenItWouldBeANoOp(t *testing.T) {
	st := useTestStore(t)
	edgeCxpSandbox(t)
	edgeCxpStubCodexLauncher(t)
	home := os.Getenv("HOME")

	t.Run("an unbound cell whose home is already ~/.codex", func(t *testing.T) {
		j := edgeCxpCellJob(t, st, "cx-noop-unbound", "codex", "")
		j.Session = "codex-thread-id"
		calls, _ := edgeCxpStubPlace(t, "", nil), edgeCxpStubRespawn(t)

		zeus := edgeCxpCodexZeus(j, "/somewhere/sessions/rollout-a.jsonl")
		zeus.CodexHome = filepath.Join(home, ".codex")
		resumeCellAgents(j, []*core.Peer{zeus}, "the account moved")

		if len(*calls) != 0 {
			t.Errorf("the placement seam ran %d times for a cell already on its target home: %+v", len(*calls), *calls)
		}
	})

	t.Run("a bound cell already on its slot's home", func(t *testing.T) {
		edgeCxpMakeCodexSlot(t, "tmp-1")
		j := edgeCxpCellJob(t, st, "cx-noop-bound", "codex", "tmp-1")
		j.Session = "codex-thread-id"
		calls, _ := edgeCxpStubPlace(t, "", nil), edgeCxpStubRespawn(t)

		zeus := edgeCxpCodexZeus(j, "/somewhere/sessions/rollout-a.jsonl")
		zeus.CodexHome = core.CodexSlotDir("tmp-1")
		resumeCellAgents(j, []*core.Peer{zeus}, "the account moved")

		if len(*calls) != 0 {
			t.Errorf("the placement seam ran %d times for a cell already on slot tmp-1: %+v", len(*calls), *calls)
		}
	})

	t.Run("a rebound cell with no rollout to carry", func(t *testing.T) {
		edgeCxpMakeCodexSlot(t, "tmp-2")
		j := edgeCxpCellJob(t, st, "cx-noop-norollout", "codex", "tmp-2")
		j.Session = "codex-thread-id"
		calls, _ := edgeCxpStubPlace(t, "", nil), edgeCxpStubRespawn(t)

		zeus := edgeCxpCodexZeus(j, "")
		zeus.CodexHome = filepath.Join(home, ".codex")
		resumeCellAgents(j, []*core.Peer{zeus}, "the account moved")

		if len(*calls) != 0 {
			t.Errorf("the placement seam ran %d times with no rollout path: %+v", len(*calls), *calls)
		}
	})
}

// When it DOES run, it runs with the three strings the contract names, and the
// destination is derived through the same slotDirFor seam the prefix uses —
// otherwise the rollout lands in one directory and CODEX_HOME points at
// another, which is precisely the state Q1 measured as
// "no rollout found for thread id".
//
// The unbound destination is ~/.codex: a rescue that cannot find a slot still
// has to bring zeus back somewhere, and the machine login is where an unbound
// cell has always run.
func TestEdgeCxpResumePlacesTheRolloutAtTheBoundSlot(t *testing.T) {
	st := useTestStore(t)
	edgeCxpSandbox(t)
	edgeCxpStubCodexLauncher(t)
	home := os.Getenv("HOME")

	t.Run("bound: the destination is the slot dir", func(t *testing.T) {
		edgeCxpMakeCodexSlot(t, "tmp-2")
		j := edgeCxpCellJob(t, st, "cx-place-bound", "codex", "tmp-2")
		j.Session = "codex-thread-id"
		calls, _ := edgeCxpStubPlace(t, "/placed/rollout.jsonl", nil), edgeCxpStubRespawn(t)

		zeus := edgeCxpCodexZeus(j, "/old/sessions/2026/rollout-a.jsonl")
		zeus.CodexHome = core.CodexSlotDir("tmp-1")
		resumeCellAgents(j, []*core.Peer{zeus}, "the account moved")

		if len(*calls) != 1 {
			t.Fatalf("the placement seam ran %d times, want exactly once: %+v", len(*calls), *calls)
		}
		want := edgeCxpPlaceCall{
			rollout: "/old/sessions/2026/rollout-a.jsonl",
			from:    core.CodexSlotDir("tmp-1"),
			to:      slotDirFor("codex", "tmp-2"),
		}
		if (*calls)[0] != want {
			t.Errorf("placement call = %+v, want %+v", (*calls)[0], want)
		}
	})

	t.Run("unbound: the destination is ~/.codex", func(t *testing.T) {
		j := edgeCxpCellJob(t, st, "cx-place-unbound", "codex", "")
		j.Session = "codex-thread-id"
		calls, _ := edgeCxpStubPlace(t, "/placed/rollout.jsonl", nil), edgeCxpStubRespawn(t)

		zeus := edgeCxpCodexZeus(j, "/old/sessions/2026/rollout-a.jsonl")
		zeus.CodexHome = core.CodexSlotDir("tmp-1")
		resumeCellAgents(j, []*core.Peer{zeus}, "the account moved")

		if len(*calls) != 1 {
			t.Fatalf("the placement seam ran %d times, want exactly once: %+v", len(*calls), *calls)
		}
		if got, want := (*calls)[0].to, filepath.Join(home, ".codex"); got != want {
			t.Errorf("placement destination = %q, want %q — an unbound codex cell resumes on the machine login", got, want)
		}
	})
}

// A placement FAILURE is not a failed rescue: zeus comes back on a fresh
// session with the handoff prompt instead. The observable difference is the
// session id — a resume carries it, a handoff must not, because resuming a
// thread whose rollout is not under the new home is the exact failure Q1
// measured.
func TestEdgeCxpResumeFallsBackToAFreshSessionWhenPlacementFails(t *testing.T) {
	st := useTestStore(t)
	edgeCxpSandbox(t)
	edgeCxpStubCodexLauncher(t)
	edgeCxpMakeCodexSlot(t, "tmp-2")

	j := edgeCxpCellJob(t, st, "cx-handoff-path", "codex", "tmp-2")
	j.Session = "codex-thread-id"
	edgeCxpStubPlace(t, "", fmt.Errorf("no space left on device"))
	cmds := edgeCxpStubRespawn(t)

	zeus := edgeCxpCodexZeus(j, "/old/sessions/2026/rollout-a.jsonl")
	zeus.CodexHome = core.CodexSlotDir("tmp-1")

	if _, err := resumeCellAgents(j, []*core.Peer{zeus}, "the account moved"); err != nil {
		t.Fatalf("resumeCellAgents after a placement failure = %v, want the handoff path rather than a refusal", err)
	}
	if len(*cmds) != 1 {
		t.Fatalf("the pane seam ran %d times, want exactly once: %v", len(*cmds), *cmds)
	}
	if strings.Contains((*cmds)[0], zeus.SessionID) {
		t.Errorf("the respawn command carries the session id %q after a FAILED placement — the handoff is a fresh session:\n%s",
			zeus.SessionID, (*cmds)[0])
	}
}

// Whatever path it takes, a codex cell's respawn is a CODEX command carrying
// this job's env prefix. A `claude` command here would replace the cell's agent
// mid-rescue, and a missing prefix would put the resumed conversation back on
// the account that just ran out.
func TestEdgeCxpResumeCommandIsCodexAndCarriesThePrefix(t *testing.T) {
	st := useTestStore(t)
	edgeCxpSandbox(t)
	edgeCxpStubCodexLauncher(t)
	edgeCxpMakeCodexSlot(t, "tmp-2")

	j := edgeCxpCellJob(t, st, "cx-cmd-shape", "codex", "tmp-2")
	j.Session = "codex-thread-id"
	edgeCxpStubPlace(t, "/placed/rollout.jsonl", nil)
	cmds := edgeCxpStubRespawn(t)

	zeus := edgeCxpCodexZeus(j, "/old/sessions/2026/rollout-a.jsonl")
	zeus.CodexHome = core.CodexSlotDir("tmp-1")

	if _, err := resumeCellAgents(j, []*core.Peer{zeus}, "the account moved"); err != nil {
		t.Fatalf("resumeCellAgents: %v", err)
	}
	if len(*cmds) != 1 {
		t.Fatalf("the pane seam ran %d times, want exactly once: %v", len(*cmds), *cmds)
	}
	cmd := (*cmds)[0]

	prefix := accountEnvPrefix(j)
	if prefix == "" {
		t.Fatalf("fixture is wrong: the job is bound to a usable slot but its prefix is empty")
	}
	if !strings.HasPrefix(cmd, prefix) {
		t.Errorf("the respawn command does not begin with this job's env prefix %q:\n%s", prefix, cmd)
	}
	if strings.Contains(cmd, "claude") {
		t.Errorf("a codex cell's respawn command mentions claude:\n%s", cmd)
	}
	if !strings.Contains(cmd, "codex") {
		t.Errorf("a codex cell's respawn command does not name the codex launcher:\n%s", cmd)
	}
	if got := core.CodexHomeFromCommand(cmd); got != core.CodexSlotDir("tmp-2") {
		t.Errorf("the respawn command's CODEX_HOME reads back as %q, want the NEW slot %q", got, core.CodexSlotDir("tmp-2"))
	}
}

// ---------------------------------------------------------------------------
// side threads
// ---------------------------------------------------------------------------

// A side thread is `claude --fork-session` against a claude transcript, which a
// codex cell has none of — so both verbs refuse, and the refusal has to SAY
// codex or carl reads it as the ordinary dead-cell message and retries. The
// control is the same call on a claude job, whose refusal must not mention
// codex: without it, a refusal that happens to contain the word for an unrelated
// reason would pass the first half vacuously.
func TestEdgeCxpSideThreadsAreRefusedOnACodexJob(t *testing.T) {
	st := useTestStore(t)
	edgeCxpSandbox(t)

	codexJob := edgeCxpCellJob(t, st, "cx-thread", "codex", "")
	claudeJob := edgeCxpCellJob(t, st, "cl-thread", "", "")

	t.Run("branch", func(t *testing.T) {
		_, err := BranchThread(codexJob, "why is this blocked", "", "")
		if err == nil {
			t.Fatal("BranchThread on a codex job succeeded; want a refusal")
		}
		if !strings.Contains(strings.ToLower(err.Error()), "codex") {
			t.Errorf("refusal = %q, want it to name codex", err)
		}

		_, err = BranchThread(claudeJob, "why is this blocked", "", "")
		if err == nil {
			t.Fatal("the control BranchThread succeeded; the fixture has a live cell and proves nothing")
		}
		if strings.Contains(strings.ToLower(err.Error()), "codex") {
			t.Errorf("a CLAUDE job's refusal mentions codex: %q — the first half of this test would pass vacuously", err)
		}
	})

	t.Run("revive", func(t *testing.T) {
		_, err := ReviveThread(codexJob, "t1")
		if err == nil {
			t.Fatal("ReviveThread on a codex job succeeded; want a refusal")
		}
		if !strings.Contains(strings.ToLower(err.Error()), "codex") {
			t.Errorf("refusal = %q, want it to name codex", err)
		}
	})

	t.Run("no row was created by either refusal", func(t *testing.T) {
		threads, err := st.ListThreads(codexJob.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(threads) != 0 {
			t.Errorf("the codex job carries %d thread rows after two refusals: %+v", len(threads), threads)
		}
	})
}

// ---------------------------------------------------------------------------
// rescueMode — Amendment 1
// ---------------------------------------------------------------------------

// Q4 at the rescue's own seam. A codex cell's account is its own, so the pin —
// which is a statement about the MACHINE login — has nothing to say about it,
// and a codex hit parked under it would leave a cell dead on a wall it could
// have been moved off. The claude lane keeps reading the pin, which is the
// whole of G1 here.
//
// The pin is set through the package's own writer and verified, so a codex
// assertion can never pass because no pin was in effect.
func TestEdgeCxpRescueModeIsAlwaysAutoForCodexAndTheRealModeOtherwise(t *testing.T) {
	edgeCxpSandbox(t)
	var _ func(*Job) string = rescueMode

	t.Run("codex with no pin at all", func(t *testing.T) {
		if got := rescueMode(&Job{Slug: "cx", Agent: "codex"}); got != "auto" {
			t.Errorf("rescueMode = %q, want auto", got)
		}
	})

	edgeCxpPinManual(t)

	t.Run("codex under a manual pin is still auto", func(t *testing.T) {
		if got := rescueMode(&Job{Slug: "cx", Agent: "codex"}); got != "auto" {
			t.Errorf("rescueMode = %q, want auto — a codex job never consults the pin", got)
		}
	})

	t.Run("every other agent reads the real mode", func(t *testing.T) {
		for _, agent := range []string{"", "claude", "CODEX", "Codex", "codexx"} {
			got := rescueMode(&Job{Slug: "cl", Agent: agent})
			if got != RotationMode() {
				t.Errorf("rescueMode(agent %q) = %q, want RotationMode() = %q", agent, got, RotationMode())
			}
			if got != "manual" {
				t.Errorf("rescueMode(agent %q) = %q under a manual pin, want manual", agent, got)
			}
		}
	})

	t.Run("and a released pin reaches the claude lane too", func(t *testing.T) {
		if err := SetRotationMode("auto", "athena-edge"); err != nil {
			t.Fatalf("SetRotationMode(auto): %v", err)
		}
		if got := rescueMode(&Job{Slug: "cl"}); got != "auto" {
			t.Errorf("rescueMode = %q, want auto — the claude lane tracks the mode in BOTH directions", got)
		}
		if got := rescueMode(&Job{Slug: "cx", Agent: "codex"}); got != "auto" {
			t.Errorf("rescueMode (codex) = %q, want auto", got)
		}
	})
}

// ---------------------------------------------------------------------------
// selectRescueBind — Amendment 1
// ---------------------------------------------------------------------------

// edgeCxpJWTAuth writes <dir>/auth.json carrying an id_token whose middle
// segment decodes to the given identity. Header and signature are junk: the
// contract reads neither. No real token or address appears anywhere.
func edgeCxpJWTAuth(t *testing.T, dir, email, plan string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	payload := fmt.Sprintf(`{"email":%q,"exp":1000000000,"https://api.openai.com/auth":{"chatgpt_plan_type":%q}}`, email, plan)
	token := "HEADER." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".SIGNATURE"
	body := fmt.Sprintf(`{"tokens":{"id_token":%q}}`, token)
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(body), 0o600); err != nil {
		t.Fatalf("write auth.json: %v", err)
	}
}

// edgeCxpSlotWithUsage makes a codex slot that is unambiguously eligible: a
// real identity and a rollout carrying a rate-limits record, so a case about an
// EXCLUSION can never be satisfied by the row merely being unrankable.
func edgeCxpSlotWithUsage(t *testing.T, slug, email string, pct float64) {
	t.Helper()
	dir := core.CodexSlotDir(slug)
	edgeCxpJWTAuth(t, dir, email, "plus")
	edgeCxpRollout(t, dir, "2026/09/17/rollout-"+slug+".jsonl", edgeCxpLimitRecord(pct, 10080, 1790208555, "null"))
}

// With no snapshot there is nothing to rank, and the answer is a fixed sentence
// rather than a guess: the rescue reads it, leaves the cell on the account it
// is on, and says why. Binding on no evidence would move a live cell onto a
// slot nothing had measured.
func TestEdgeCxpSelectRescueBindWithNoCodexSnapshot(t *testing.T) {
	useTestStore(t)
	edgeCxpSandbox(t)
	var _ func(*Job, time.Time) (string, string, string) = selectRescueBind

	if _, ok := ReadCodexUsageSnapshot(); ok {
		t.Fatal("fixture is wrong: a codex snapshot exists before any pass")
	}

	slug, email, why := selectRescueBind(&Job{ID: 1, Slug: "cx", Agent: "codex", AccountSlug: "tmp-1"}, edgeCxpNow)
	if slug != "" || email != "" {
		t.Errorf("(slug, email) = (%q, %q), want the empty pair", slug, email)
	}
	if why != "no codex usage snapshot" {
		t.Errorf("why = %q, want %q", why, "no codex usage snapshot")
	}
}

// The rescue's own exclusion: the cell is being moved OFF the slot it is on, so
// that slot's row — typically the healthiest-looking one, since it is the one
// the cell has been billing — is excluded BY SLUG. The email-keyed exclusion
// alone would not reach it.
func TestEdgeCxpSelectRescueBindExcludesTheCellsOwnSlot(t *testing.T) {
	useTestStore(t)
	edgeCxpSandbox(t)

	// Both slots sit well inside every eligibility gate the unchanged selector
	// applies, and differ only in RANK: tmp-2 is emptier, so it is what the
	// selector prefers. The percentages are deliberately low — an earlier
	// version used 99 for "the fullest slot" and the selector declined it,
	// which is the selector's own business and not something this contract
	// states, so the case was measuring an undocumented threshold instead of
	// the exclusion it names.
	edgeCxpSlotWithUsage(t, "tmp-1", "tmp-1@example.test", 9)
	edgeCxpSlotWithUsage(t, "tmp-2", "tmp-2@example.test", 3)
	if err := CodexUsagePass(); err != nil {
		t.Fatalf("CodexUsagePass: %v", err)
	}

	t.Run("the other slot is chosen", func(t *testing.T) {
		slug, email, why := selectRescueBind(&Job{ID: 1, Slug: "cx", Agent: "codex", AccountSlug: "tmp-1"}, edgeCxpNow)
		if slug != "tmp-2" || email != "tmp-2@example.test" {
			t.Errorf("(slug, email) = (%q, %q) (why %q), want (tmp-2, tmp-2@example.test)", slug, email, why)
		}
	})

	// The discriminating direction. tmp-2 is the emptier slot, so it is what
	// the selector would rank FIRST — a rescue that forgot `excludeSlug` would
	// hand the cell straight back the account it just ran out on, and the
	// respawn would walk into the same wall with the record now reading as
	// rescued.
	t.Run("and the cell's own slot is never chosen back, even though it ranks first", func(t *testing.T) {
		slug, email, why := selectRescueBind(&Job{ID: 2, Slug: "cx2", Agent: "codex", AccountSlug: "tmp-2"}, edgeCxpNow)
		if slug == "tmp-2" {
			t.Fatal("selectRescueBind returned the cell's OWN slot — the rescue would respawn zeus onto the account that just ran out")
		}
		if slug != "tmp-1" || email != "tmp-1@example.test" {
			t.Errorf("(slug, email) = (%q, %q) (why %q), want (tmp-1, tmp-1@example.test) — the only slot left once the cell's own is excluded", slug, email, why)
		}
	})
}

// `mainEmail = core.CodexMainEmail()` stated as a test, with the collision that
// makes it observable: the machine's own ~/.codex identity is spelled exactly
// like slot tmp-2's, so tmp-2 IS the machine login wearing a slot's name. With
// tmp-1 excluded as the cell's own, nothing is left — and a rescue that ignored
// the main identity would move the cell onto the shared lane while the board
// showed a slot badge.
func TestEdgeCxpSelectRescueBindExcludesTheMachineLoginsIdentity(t *testing.T) {
	useTestStore(t)
	edgeCxpSandbox(t)
	home := os.Getenv("HOME")

	edgeCxpSlotWithUsage(t, "tmp-1", "tmp-1@example.test", 99)
	edgeCxpSlotWithUsage(t, "tmp-2", "machine@example.test", 3)
	edgeCxpJWTAuth(t, filepath.Join(home, ".codex"), "MACHINE@example.TEST", "pro") // case differs on purpose
	if err := CodexUsagePass(); err != nil {
		t.Fatalf("CodexUsagePass: %v", err)
	}
	if got := core.CodexMainEmail(); got != "MACHINE@example.TEST" {
		t.Fatalf("fixture is wrong: CodexMainEmail() = %q", got)
	}

	slug, email, _ := selectRescueBind(&Job{ID: 1, Slug: "cx", Agent: "codex", AccountSlug: "tmp-1"}, edgeCxpNow)
	if slug != "" || email != "" {
		t.Errorf("(slug, email) = (%q, %q), want the empty pair — tmp-2 is the machine login and tmp-1 is the cell's own slot", slug, email)
	}
}

// Pool by agent, at the rescue. A claude job's rescue must never be handed a
// codex slug: the slot has no keychain item behind it, so every claude in the
// respawned cell would come up logged out — a worse state than the wall it was
// being rescued from.
func TestEdgeCxpSelectRescueBindNeverHandsAClaudeJobACodexSlug(t *testing.T) {
	useTestStore(t)
	edgeCxpSandbox(t)

	edgeCxpSlotWithUsage(t, "tmp-1", "tmp-1@example.test", 3)
	edgeCxpSlotWithUsage(t, "tmp-2", "tmp-2@example.test", 4)
	if err := CodexUsagePass(); err != nil {
		t.Fatalf("CodexUsagePass: %v", err)
	}

	for _, agent := range []string{"", "claude"} {
		slug, _, why := selectRescueBind(&Job{ID: 1, Slug: "cl", Agent: agent, AccountSlug: "slot-a"}, edgeCxpNow)
		if slug == "tmp-1" || slug == "tmp-2" {
			t.Errorf("a claude job (agent %q) was handed the codex slug %q (why %q)", agent, slug, why)
		}
	}
}
