package jobs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"kraken/internal/core"
)

// account_edge_test.go — the EDGE dimension of 50-contract.md's Slice S-jobs
// obligations: decideBind's refusals and its every-candidate-excluded reasons,
// boundCounts' empty and non-counting inputs, the EXACT shape of the env
// assignment spawnCommand prefixes onto a bound cell's command (and the
// byte-identical command an unbound one still gets), and LimitHit's source
// account across a delivery fold.
//
// Written blind from the frozen contract. internal/jobs/{binding,cell,thread,
// rotation,limitrescue,limithook}.go were never read in their working-tree
// state; cellDir, spawnCommand's staging contract, threadSpawnCommand's
// signature, useTestStore, RecordLimitHit/ReadLimitHit/limitHitKey and the
// LimitHit shape are pre-existing package state read at the base commit.
//
// The sibling blind author (athena-logic) owns the stated invariants and the
// happy paths — which slot a healthy pool yields, that a bind stamps the row.
// What lives here is the other half: the pin that must decide NOTHING, the
// guard stickiness must not walk around, the quoting, and the fold.
//
// Package-level identifiers are prefixed `edgeAcct` — this package's test
// namespace already carries several `edge*` families and a second blind author
// is writing beside this file right now.

// edgeAcctEnvVar is the variable the whole delivery mechanism rides on. Named
// once here so a rename shows up as one failure rather than fifteen.
const edgeAcctEnvVar = "CLAUDE_SECURESTORAGE_CONFIG_DIR"

// edgeAcctNow is the fixed clock every decideBind case passes, so a freshness
// or cooldown verdict never depends on when the suite ran.
var edgeAcctNow = time.UnixMilli(1_760_000_000_000)

// edgeAcctSandbox isolates HOME and points the pool at a directory under it,
// then creates that directory. Both halves are contract obligations: no test
// may read the real ~/.claude-pool, and PoolDir() honours $CLAUDE_POOL_DIR.
func edgeAcctSandbox(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	pool := filepath.Join(home, "pool")
	t.Setenv("CLAUDE_POOL_DIR", pool)
	if err := os.MkdirAll(pool, 0o755); err != nil {
		t.Fatalf("create pool dir: %v", err)
	}
	return pool
}

// edgeAcctSlot is a pool row that clears every eligibility gate: an identity,
// a session window well under the bar, and a FetchedAt stamped at the fixed
// now. Cases below break exactly one thing at a time.
func edgeAcctSlot(slug string, sessionPct float64) core.AccountUsage {
	return core.AccountUsage{
		Slug:      slug,
		Email:     slug + "@example.com",
		Session:   &core.UsageWindow{Percent: sessionPct},
		FetchedAt: edgeAcctNow.UnixMilli(),
	}
}

// edgeAcctSnap wraps rows into the snapshot shape decideBind takes.
func edgeAcctSnap(rows ...core.AccountUsage) core.UsageSnapshot {
	return core.UsageSnapshot{FetchedAt: edgeAcctNow.UnixMilli(), Accounts: rows}
}

// edgeAcctAlways / edgeAcctNever are the two trivial keychain probes. Neither
// shells out — the contract forbids any test reaching security(1).
func edgeAcctAlways(string) bool { return true }
func edgeAcctNever(string) bool  { return false }

// edgeAcctCellJob stands a job up with its cell dir in place, which is all the
// two spawn-command renderers need (they stage their inputs as files there).
func edgeAcctCellJob(t *testing.T, slug, accountSlug string) *Job {
	t.Helper()
	j := &Job{Slug: slug, AccountSlug: accountSlug}
	if err := os.MkdirAll(cellDir(j), 0o755); err != nil {
		t.Fatalf("create cell dir: %v", err)
	}
	return j
}

// ---------------------------------------------------------------------------
// decideBind: the manual pin
// ---------------------------------------------------------------------------

// TestEdgeDecideBindManualPinDecidesNothing. Q5: the pin means kraken makes NO
// account decisions, which is stronger than "makes the same decision it would
// have". The contract states the refusal comes before reading anything else,
// so the keychain probe — the one input with a side effect, a shell-out per
// candidate — must not be reached at all, and the reason is a fixed sentence
// rather than a per-candidate exclusion list (there was no candidate list).
func TestEdgeDecideBindManualPinDecidesNothing(t *testing.T) {
	edgeAcctSandbox(t)
	probes := 0
	probe := func(string) bool { probes++; return true }

	snap := edgeAcctSnap(edgeAcctSlot("slot-a", 5), edgeAcctSlot("slot-b", 6))
	slug, email, reason := decideBind(snap, map[string]int{"slot-a": 3}, "manual", "carl@example.com", "slot-a", probe, edgeAcctNow)

	if slug != "" || email != "" {
		t.Errorf("(slug, email) = (%q, %q) under a manual pin, want the empty pair", slug, email)
	}
	if reason != "rotation pinned manual" {
		t.Errorf("reason = %q, want %q", reason, "rotation pinned manual")
	}
	if probes != 0 {
		t.Errorf("the keychain probe ran %d times under a manual pin — the pin is checked before anything else is read", probes)
	}
}

// TestEdgeDecideBindOnlyTheLiteralManualModePins. The mode is read off
// rotation.json, where "manual" and "auto" are the two values kraken itself
// writes — but a mode that is absent, or that does not match exactly, is the
// AUTO lane, and a comparison that accidentally matched more than "manual"
// would silently disable binding for the whole machine with nothing to see.
// The contract states the test as `mode == "manual"`.
func TestEdgeDecideBindOnlyTheLiteralManualModePins(t *testing.T) {
	edgeAcctSandbox(t)
	snap := edgeAcctSnap(edgeAcctSlot("slot-a", 5))

	for _, mode := range []string{"", "auto", "MANUAL", "Manual", "manual-ish"} {
		t.Run("mode "+mode, func(t *testing.T) {
			slug, _, reason := decideBind(snap, nil, mode, "carl@example.com", "", edgeAcctAlways, edgeAcctNow)
			if slug != "slot-a" {
				t.Errorf("mode %q: slug = %q (reason %q), want slot-a — only the literal \"manual\" is the pin", mode, slug, reason)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// decideBind: the empty and the excluded
// ---------------------------------------------------------------------------

// TestEdgeDecideBindEmptyPoolIsUnboundNeverAnError. Q4: a dry pool spawns the
// cell unbound, which is the pre-feature behaviour and always available. The
// contract is explicit that this is not an error, because the caller is a
// spawn: anything that reads as a failure there is a cell that does not start.
func TestEdgeDecideBindEmptyPoolIsUnboundNeverAnError(t *testing.T) {
	edgeAcctSandbox(t)
	for _, c := range []struct {
		name string
		snap core.UsageSnapshot
	}{
		{"a snapshot with no rows", core.UsageSnapshot{FetchedAt: edgeAcctNow.UnixMilli()}},
		{"an empty accounts slice", edgeAcctSnap()},
		{"the zero snapshot", core.UsageSnapshot{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			slug, email, _ := decideBind(c.snap, nil, "auto", "carl@example.com", "slot-a", edgeAcctAlways, edgeAcctNow)
			if slug != "" || email != "" {
				t.Errorf("(slug, email) = (%q, %q), want the empty pair", slug, email)
			}
		})
	}
}

// TestEdgeDecideBindExcludesTheMainSlugRow. The snapshot's first row is the
// machine's own login, not a pool slot — it has no slot dir, so binding to it
// would point CLAUDE_SECURESTORAGE_CONFIG_DIR at a directory that does not
// exist and every claude in the cell would come up logged out.
func TestEdgeDecideBindExcludesTheMainSlugRow(t *testing.T) {
	edgeAcctSandbox(t)
	main := edgeAcctSlot("main", 1)
	main.Current = true

	slug, email, _ := decideBind(edgeAcctSnap(main), nil, "auto", main.Email, "main", edgeAcctAlways, edgeAcctNow)
	if slug != "" || email != "" {
		t.Fatalf("(slug, email) = (%q, %q), want the empty pair — the main row is never a bind target", slug, email)
	}

	picked, _, _ := decideBind(edgeAcctSnap(main, edgeAcctSlot("slot-a", 50)), nil, "auto", "carl@example.com", "", edgeAcctAlways, edgeAcctNow)
	if picked != "slot-a" {
		t.Errorf("slug = %q, want slot-a — a healthy main row must not outrank a real slot", picked)
	}
}

// TestEdgeDecideBindExcludesTheMainMirrorCaseInsensitively. A rotation's
// save-back leaves the main account mirrored into the pool under its own slug,
// so a slot whose email IS main's is main wearing a slot's name: binding a
// cell to it puts that cell back on the shared lane while the board shows a
// badge claiming otherwise. The two spellings come from two independently
// written files (~/.claude.json and the slot's account.json), so the compare
// has to fold case.
func TestEdgeDecideBindExcludesTheMainMirrorCaseInsensitively(t *testing.T) {
	edgeAcctSandbox(t)
	mirror := edgeAcctSlot("save-back", 1)
	mirror.Email = "Carl@Example.COM"

	slug, email, _ := decideBind(edgeAcctSnap(mirror), nil, "auto", "carl@example.com", "", edgeAcctAlways, edgeAcctNow)
	if slug != "" || email != "" {
		t.Errorf("(slug, email) = (%q, %q), want the empty pair — the mirror slot is main", slug, email)
	}

	// and the other direction of the fold
	slug, _, _ = decideBind(edgeAcctSnap(mirror), nil, "auto", "CARL@example.com", "", edgeAcctAlways, edgeAcctNow)
	if slug != "" {
		t.Errorf("slug = %q, want the empty pick", slug)
	}
}

// TestEdgeDecideBindWithNoKnownMainEmailStillBinds. decideBind's stated
// exclusions are the main SLUG row and rows matching mainEmail; an unknown
// main identity removes the second filter and nothing else. It deliberately
// does NOT inherit SelectRotationTarget's fail-closed refusal, because the
// harm the two are guarding differs: rotating main onto its own mirror is a
// churn loop, while binding a cell to a slot that happens to be main's mirror
// is one cell on the shared lane — and refusing instead would mean a machine
// whose ~/.claude.json cannot be read binds NOTHING, ever, silently.
//
// A contract silence, asserted at the literal reading and disclosed.
func TestEdgeDecideBindWithNoKnownMainEmailStillBinds(t *testing.T) {
	edgeAcctSandbox(t)
	slug, email, reason := decideBind(edgeAcctSnap(edgeAcctSlot("slot-a", 5)), nil, "auto", "", "", edgeAcctAlways, edgeAcctNow)
	if slug != "slot-a" {
		t.Fatalf("slug = %q (reason %q), want slot-a", slug, reason)
	}
	if email != "slot-a@example.com" {
		t.Errorf("email = %q, want the slot's own", email)
	}
}

// ---------------------------------------------------------------------------
// decideBind: the keychain guard
// ---------------------------------------------------------------------------

// TestEdgeDecideBindProbesTheLiteralSlotDir is R3 stated as a test: the value
// the spawn will export and the path the guard probes are the SAME string,
// built once as `PoolDir() + "/" + slug`. The keychain service name is a hash
// of that string, so `/x` and `/x/` are different items — a guard that probed
// a re-derived (cleaned, joined, trailing-slashed) variant would answer about
// an item no claude will ever open, which is the exact failure the guard
// exists to catch, inverted.
func TestEdgeDecideBindProbesTheLiteralSlotDir(t *testing.T) {
	pool := edgeAcctSandbox(t)
	var seen []string
	probe := func(dir string) bool { seen = append(seen, dir); return true }

	decideBind(edgeAcctSnap(edgeAcctSlot("slot-a", 5)), nil, "auto", "carl@example.com", "", probe, edgeAcctNow)

	want := core.PoolDir() + "/" + "slot-a"
	if want != pool+"/slot-a" {
		t.Fatalf("fixture is wrong: PoolDir() = %q, pool = %q", core.PoolDir(), pool)
	}
	found := false
	for _, got := range seen {
		if got == want {
			found = true
		}
	}
	if !found {
		t.Errorf("the guard probed %v, want it to probe exactly %q", seen, want)
	}
}

// TestEdgeDecideBindSkipsSlotsWithNoKeychainItem. Q1's per-bind guard: a slot
// dir with no keychain item behind it is an unseeded slot (or the env var's
// semantics moved under us), and a claude launched at it comes up unable to
// authenticate. The excluded slot must fall out of the ranking rather than
// fail the bind — the next slot is a perfectly good answer.
func TestEdgeDecideBindSkipsSlotsWithNoKeychainItem(t *testing.T) {
	pool := edgeAcctSandbox(t)
	best, second := edgeAcctSlot("slot-a", 5), edgeAcctSlot("slot-b", 50)

	t.Run("the best candidate is skipped and the next one binds", func(t *testing.T) {
		probe := func(dir string) bool { return dir != pool+"/slot-a" }
		slug, email, _ := decideBind(edgeAcctSnap(best, second), nil, "auto", "carl@example.com", "", probe, edgeAcctNow)
		if slug != "slot-b" || email != "slot-b@example.com" {
			t.Errorf("(slug, email) = (%q, %q), want (slot-b, slot-b@example.com)", slug, email)
		}
	})

	t.Run("stickiness does not walk around the guard", func(t *testing.T) {
		probe := func(dir string) bool { return dir != pool+"/slot-a" }
		slug, _, _ := decideBind(edgeAcctSnap(best, second), nil, "auto", "carl@example.com", "slot-a", probe, edgeAcctNow)
		if slug != "slot-b" {
			t.Errorf("slug = %q, want slot-b — a reactivation's previous slot is still subject to the item guard", slug)
		}
	})

	t.Run("every slot skipped is an unbound spawn, with a reason per slot", func(t *testing.T) {
		slug, email, reason := decideBind(edgeAcctSnap(best, second), nil, "auto", "carl@example.com", "", edgeAcctNever, edgeAcctNow)
		if slug != "" || email != "" {
			t.Fatalf("(slug, email) = (%q, %q), want the empty pair", slug, email)
		}
		if reason != "slot-a: no keychain item; slot-b: no keychain item" {
			t.Errorf("reason = %q, want %q — every skipped slot has to say so, or a whole dry pool reports one line",
				reason, "slot-a: no keychain item; slot-b: no keychain item")
		}
	})
}

// ---------------------------------------------------------------------------
// decideBind: stickiness that must not fire
// ---------------------------------------------------------------------------

// TestEdgeDecideBindPrevSlugOfAVanishedSlotFallsBackToRanking. prevSlug is the
// job's stored AccountSlug, which outlives the pool: carl can delete a slot
// dir, or an account can be reauthed out of eligibility, between a job's first
// run and its reactivation. A previous name with no live row behind it must
// simply not apply.
func TestEdgeDecideBindPrevSlugOfAVanishedSlotFallsBackToRanking(t *testing.T) {
	edgeAcctSandbox(t)
	slug, email, reason := decideBind(edgeAcctSnap(edgeAcctSlot("slot-a", 5)), nil, "auto", "carl@example.com", "slot-gone", edgeAcctAlways, edgeAcctNow)
	if slug != "slot-a" {
		t.Fatalf("slug = %q (reason %q), want slot-a", slug, reason)
	}
	if email != "slot-a@example.com" {
		t.Errorf("email = %q, want the ranked pick's own", email)
	}
}

// ---------------------------------------------------------------------------
// boundCounts
// ---------------------------------------------------------------------------

// TestEdgeBoundCountsIgnoresUnboundAndNonLiveJobs. The tally is the binder's
// FIRST ranking key, so what it must not count matters more than what it does:
// counting an unbound job would create a phantom entry under the empty slug,
// and counting a torn-down job would make its slot look permanently busy and
// drive every future bind onto the others — the "unique account per live cell"
// preference inverted, quietly, forever.
func TestEdgeBoundCountsIgnoresUnboundAndNonLiveJobs(t *testing.T) {
	live := func(*Job) bool { return true }
	dead := func(*Job) bool { return false }

	t.Run("no jobs at all", func(t *testing.T) {
		for _, c := range []struct {
			name string
			in   []*Job
		}{{"nil", nil}, {"an empty slice", []*Job{}}} {
			got := boundCounts(c.in, live)
			if len(got) != 0 {
				t.Errorf("%s: boundCounts = %v, want no entries", c.name, got)
			}
		}
	})

	t.Run("an unbound job contributes nothing, not an empty-slug entry", func(t *testing.T) {
		got := boundCounts([]*Job{{Slug: "a"}, {Slug: "b", AccountEmail: "stale@example.com"}}, live)
		if len(got) != 0 {
			t.Errorf("boundCounts = %v, want no entries — a job with no AccountSlug is unbound whatever else it carries", got)
		}
	})

	t.Run("a bound but non-live job contributes nothing", func(t *testing.T) {
		got := boundCounts([]*Job{{Slug: "a", AccountSlug: "slot-a"}}, dead)
		if len(got) != 0 {
			t.Errorf("boundCounts = %v, want no entries — the tally is LIVE cells", got)
		}
	})

	t.Run("a binding with no email still counts", func(t *testing.T) {
		got := boundCounts([]*Job{{Slug: "a", AccountSlug: "slot-a"}}, live)
		if got["slot-a"] != 1 {
			t.Errorf("boundCounts = %v, want slot-a at 1 — the slug is what a bind occupies; the email is the display disambiguator", got)
		}
	})

	t.Run("several live jobs on one slug accumulate, and the mix is filtered per job", func(t *testing.T) {
		rows := []*Job{
			{Slug: "one", AccountSlug: "slot-a"},
			{Slug: "two", AccountSlug: "slot-a"},
			{Slug: "three", AccountSlug: "slot-b"},
			{Slug: "four"},
			{Slug: "five", AccountSlug: "slot-c"},
		}
		isLive := func(j *Job) bool { return j.Slug != "five" }
		got := boundCounts(rows, isLive)
		if got["slot-a"] != 2 || got["slot-b"] != 1 {
			t.Errorf("boundCounts = %v, want slot-a 2 and slot-b 1", got)
		}
		if _, ok := got["slot-c"]; ok {
			t.Errorf("boundCounts = %v, want no slot-c entry — its only job is not live", got)
		}
	})

	t.Run("the predicate is asked about every job", func(t *testing.T) {
		var asked []string
		boundCounts([]*Job{{Slug: "one", AccountSlug: "slot-a"}, {Slug: "two", AccountSlug: "slot-b"}},
			func(j *Job) bool { asked = append(asked, j.Slug); return false })
		if len(asked) != 2 || asked[0] != "one" || asked[1] != "two" {
			t.Errorf("the predicate saw %v, want both jobs by pointer", asked)
		}
	})
}

// ---------------------------------------------------------------------------
// spawnCommand: the env prefix
// ---------------------------------------------------------------------------

// TestEdgeSpawnCommandUnboundIsByteIdenticalToToday. The contract's strongest
// claim about the delivery seam is the negative one: an unbound cell's command
// is byte-identical to what it was before the feature existed. Every cell on
// the machine spawns through this one function, so a stray prefix, an empty
// assignment, or one extra space would change EVERY spawn — and the pre-existing
// test that pins this exact string (TestSpawnCommandOmitsEmptyInputs) is the
// control this row deliberately restates from the other side.
func TestEdgeSpawnCommandUnboundIsByteIdenticalToToday(t *testing.T) {
	edgeAcctSandbox(t)
	j := edgeAcctCellJob(t, "spawn-unbound", "")

	cmd, err := spawnCommand(j, []string{"claude", "-r", "sess"}, "", "")
	if err != nil {
		t.Fatalf("spawnCommand: %v", err)
	}
	if cmd != "claude -r sess" {
		t.Errorf("unbound spawn command = %q, want %q", cmd, "claude -r sess")
	}
	if strings.Contains(cmd, edgeAcctEnvVar) {
		t.Errorf("unbound spawn command mentions %s: %q", edgeAcctEnvVar, cmd)
	}
}

// TestEdgeSpawnCommandBoundPrefixIsExactlyOneQuotedAssignment pins the whole
// shape at once, because each half of it fails silently on its own.
//
// The value is single-quoted UNCONDITIONALLY. The pool path needs no shell
// quoting today, so a renderer that quotes only when it must produces the
// right command on this machine and the wrong one the first time a path
// carries a space — and the failure mode is not a syntax error, it is claude
// launching with the variable set to a truncated directory that resolves to a
// DIFFERENT keychain item, i.e. silently logged out.
//
// Exactly one space separates the assignment from the launcher: two would put
// an empty word in command position under some shells, and none would make the
// launcher part of the value.
//
// And everything after the prefix is byte-identical to the unbound rendering —
// asserted against the unbound command for the same job rather than against a
// literal, so the claim is "the prefix is the ONLY difference" rather than "the
// command happens to look like this".
func TestEdgeSpawnCommandBoundPrefixIsExactlyOneQuotedAssignment(t *testing.T) {
	pool := edgeAcctSandbox(t)
	j := edgeAcctCellJob(t, "spawn-bound", "")

	argv := []string{"claude", "--model", "claude-fable-5", "-r", "sess"}
	unbound, err := spawnCommand(j, argv, "brief text", "prompt text")
	if err != nil {
		t.Fatalf("spawnCommand (unbound): %v", err)
	}

	j.AccountSlug = "slot-a"
	bound, err := spawnCommand(j, argv, "brief text", "prompt text")
	if err != nil {
		t.Fatalf("spawnCommand (bound): %v", err)
	}

	wantPrefix := edgeAcctEnvVar + "='" + pool + "/slot-a' "
	if bound != wantPrefix+unbound {
		t.Errorf("bound spawn command =\n  %q\nwant\n  %q\n(the prefix must be the ONLY difference from the unbound rendering)",
			bound, wantPrefix+unbound)
	}
	if strings.HasPrefix(bound, wantPrefix+" ") {
		t.Errorf("bound spawn command has more than one space before the launcher: %q", bound)
	}
}

// TestEdgeSpawnCommandBoundValueIsThePoolDirJoinedWithTheSlug. The slug is the
// only variable in the assignment: the value is PoolDir() joined with it by a
// single slash and nothing else — no trailing slash, no cleaning, no second
// derivation. Two slugs are rendered and diffed so the claim is structural
// rather than a second copy of the same format string.
func TestEdgeSpawnCommandBoundValueIsThePoolDirJoinedWithTheSlug(t *testing.T) {
	pool := edgeAcctSandbox(t)
	j := edgeAcctCellJob(t, "spawn-value", "slot-a")
	argv := []string{"claude"}

	a, err := spawnCommand(j, argv, "", "")
	if err != nil {
		t.Fatalf("spawnCommand: %v", err)
	}
	if got, want := a, edgeAcctEnvVar+"='"+pool+"/slot-a' claude"; got != want {
		t.Fatalf("bound command = %q, want %q", got, want)
	}
	if strings.Contains(a, "/slot-a/'") {
		t.Errorf("the exported slot dir carries a trailing slash: %q — the keychain service name hashes the raw string, so /x and /x/ are different items", a)
	}

	j.AccountSlug = "slot-b"
	b, err := spawnCommand(j, argv, "", "")
	if err != nil {
		t.Fatalf("spawnCommand: %v", err)
	}
	if strings.ReplaceAll(a, "slot-a", "slot-b") != b {
		t.Errorf("changing only the slug changed more than the slug:\n  %q\n  %q", a, b)
	}
}

// TestEdgeSpawnCommandBoundStillFailsWhenStagingFails. A binding must not turn
// a staging failure into a launched agent: the prefix is prepended to a
// command, and a renderer that built the prefix first and returned it on the
// error path would hand StartCell a "command" that runs the env assignment and
// nothing else — a pane that opens, exits, and leaves the cell reading as
// spawned.
func TestEdgeSpawnCommandBoundStillFailsWhenStagingFails(t *testing.T) {
	edgeAcctSandbox(t)
	j := &Job{Slug: "spawn-unwritable", AccountSlug: "slot-a"} // no cell dir at all
	cmd, err := spawnCommand(j, []string{"claude"}, "brief", "")
	if err == nil {
		t.Fatalf("spawnCommand succeeded with an unwritable cell dir, want an error (cmd %q)", cmd)
	}
	if cmd != "" {
		t.Errorf("spawnCommand returned both an error and a command %q — want no command", cmd)
	}
}

// TestEdgeThreadSpawnCommandAppliesTheIdenticalRule. A side thread runs in the
// same worktree and reads the same conversation, so it has to bill the same
// account — and it is a separate renderer, which is exactly how one of the two
// gets missed. Both directions are pinned: a bound job's thread carries the
// prefix and nothing else changes, and an unbound job's thread is byte-identical
// to today.
func TestEdgeThreadSpawnCommandAppliesTheIdenticalRule(t *testing.T) {
	pool := edgeAcctSandbox(t)
	j := edgeAcctCellJob(t, "thread-bound", "")
	resume := []string{"-r", "zeus-sess"}

	unbound, err := threadSpawnCommand(j, "t1", "why blocked", "explain", "", "ask", "", resume)
	if err != nil {
		t.Fatalf("threadSpawnCommand (unbound): %v", err)
	}
	if strings.Contains(unbound, edgeAcctEnvVar) {
		t.Errorf("an unbound job's thread command mentions %s: %q", edgeAcctEnvVar, unbound)
	}

	j.AccountSlug = "slot-a"
	bound, err := threadSpawnCommand(j, "t1", "why blocked", "explain", "", "ask", "", resume)
	if err != nil {
		t.Fatalf("threadSpawnCommand (bound): %v", err)
	}
	wantPrefix := edgeAcctEnvVar + "='" + pool + "/slot-a' "
	if bound != wantPrefix+unbound {
		t.Errorf("bound thread command =\n  %q\nwant\n  %q", bound, wantPrefix+unbound)
	}
}

// ---------------------------------------------------------------------------
// LimitHit.SourceAccount
// ---------------------------------------------------------------------------

// TestEdgeLimitHitSourceAccountIsOmittedWhenEmpty. Empty means the main lane,
// and the omitempty tag is what makes that state readable as an absence rather
// than as an account named "". The record is a kv JSON blob read back by the
// rescue pass across process restarts, so the tag is the wire.
func TestEdgeLimitHitSourceAccountIsOmittedWhenEmpty(t *testing.T) {
	f, ok := reflect.TypeOf(LimitHit{}).FieldByName("SourceAccount")
	if !ok {
		t.Fatal("LimitHit has no SourceAccount field")
	}
	if got := f.Tag.Get("json"); got != "source_account,omitempty" {
		t.Errorf("LimitHit.SourceAccount json tag = %q, want %q", got, "source_account,omitempty")
	}

	blank, err := json.Marshal(LimitHit{Error: "rate_limit", At: 1})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(blank, &keys); err != nil {
		t.Fatalf("decode %s: %v", blank, err)
	}
	if v, ok := keys["source_account"]; ok {
		t.Errorf("a main-lane hit serialized source_account = %s, want no key at all", v)
	}

	named, err := json.Marshal(LimitHit{Error: "rate_limit", At: 1, SourceAccount: "a@example.com"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(named), `"source_account":"a@example.com"`) {
		t.Errorf("a bound hit serialized %s, want it to carry source_account", named)
	}
}

// TestEdgeLimitHitStampsTheBindingAtRecordTime is the fresh-record boundary in
// both directions: a bound cell's hit names the account that was exhausted (so
// the record says which account died, not only which one it moved to), and an
// unbound cell's hit names nothing — which is a real state, not a missing
// value, and is what the empty string means.
func TestEdgeLimitHitStampsTheBindingAtRecordTime(t *testing.T) {
	useTestStore(t)

	bound := &Job{ID: 11, Slug: "bound", AccountSlug: "slot-a", AccountEmail: "a@example.com"}
	hit, err := RecordLimitHit(bound, "rate_limit", "usage limit reached", "sess-1", "", edgeAcctNow)
	if err != nil {
		t.Fatalf("RecordLimitHit: %v", err)
	}
	if hit.SourceAccount != "a@example.com" {
		t.Errorf("SourceAccount = %q, want the job's bound email", hit.SourceAccount)
	}

	unbound := &Job{ID: 12, Slug: "unbound"}
	hit, err = RecordLimitHit(unbound, "rate_limit", "usage limit reached", "sess-2", "", edgeAcctNow)
	if err != nil {
		t.Fatalf("RecordLimitHit: %v", err)
	}
	if hit.SourceAccount != "" {
		t.Errorf("SourceAccount = %q for an unbound cell, want empty — empty IS the main lane", hit.SourceAccount)
	}
}

// TestEdgeLimitHitFoldNeverOverwritesANonEmptySourceAccount. A single exhausted
// window fails zeus and every teammate within seconds, and the transcript
// trigger re-delivers the same banner for as long as the cell is down — so a
// hit typically folds many times, and the rescue may have REBOUND the job in
// between. Overwriting would relabel the record with the destination account
// and lose the only statement of which account actually ran out.
func TestEdgeLimitHitFoldNeverOverwritesANonEmptySourceAccount(t *testing.T) {
	useTestStore(t)
	j := &Job{ID: 21, Slug: "folded", AccountSlug: "slot-a", AccountEmail: "a@example.com"}

	if _, err := RecordLimitHit(j, "rate_limit", "usage limit reached", "sess-1", "", edgeAcctNow); err != nil {
		t.Fatalf("RecordLimitHit: %v", err)
	}
	// the rescue rebinds mid-herd; the teammates are still firing
	j.AccountSlug, j.AccountEmail = "slot-b", "b@example.com"
	hit, err := RecordLimitHit(j, "rate_limit", "usage limit reached", "sess-2", "agent-1", edgeAcctNow.Add(3*time.Second))
	if err != nil {
		t.Fatalf("RecordLimitHit (fold): %v", err)
	}
	if hit.Deliveries != 2 {
		t.Fatalf("Deliveries = %d, want 2 — this case is only about a FOLD", hit.Deliveries)
	}
	if hit.SourceAccount != "a@example.com" {
		t.Errorf("SourceAccount = %q after a fold, want the stored a@example.com — the record names the EXHAUSTED account", hit.SourceAccount)
	}
}

// TestEdgeLimitHitFoldNeverFillsAnEmptySourceAccount — corrected by the LEAD
// against 50-contract.md's REVISED SourceAccount paragraph (review findings
// E3/F4), exactly the amendment this test's original comment disclosed as the
// likelier-safer reading: a fold writes SourceAccount NEVER, empty or not.
// The field captures the binding at the moment the hit OPENED; this job was
// unbound then (the main lane, spelled ""), and a fold arriving after the
// rescue bound it must not relabel the exhausted lane with the destination
// account.
func TestEdgeLimitHitFoldNeverFillsAnEmptySourceAccount(t *testing.T) {
	useTestStore(t)
	j := &Job{ID: 22, Slug: "late-bind"}

	if _, err := RecordLimitHit(j, "rate_limit", "usage limit reached", "sess-1", "", edgeAcctNow); err != nil {
		t.Fatalf("RecordLimitHit: %v", err)
	}
	j.AccountSlug, j.AccountEmail = "slot-b", "b@example.com"
	hit, err := RecordLimitHit(j, "rate_limit", "usage limit reached", "sess-1", "", edgeAcctNow.Add(3*time.Second))
	if err != nil {
		t.Fatalf("RecordLimitHit (fold): %v", err)
	}
	if hit.Deliveries != 2 {
		t.Fatalf("Deliveries = %d, want 2 — this case is only about a FOLD", hit.Deliveries)
	}
	if hit.SourceAccount != "" {
		t.Errorf("SourceAccount = %q, want \"\" — a fold never writes the field; the hit opened on the main lane", hit.SourceAccount)
	}
}

// TestEdgeLimitHitFreshRecordAfterResolutionRestampsTheCurrentBinding.
// Resolution, not time, is the event boundary: a RESOLVED hit never folds, so
// a later banner opens a NEW record — and that new record must name the account
// exhausted THIS time, which after a rescue's rebind is a different one. A
// carried-forward source account would make the second exhaustion read as the
// first account's, and the rescue would exclude the wrong email from its next
// selection.
func TestEdgeLimitHitFreshRecordAfterResolutionRestampsTheCurrentBinding(t *testing.T) {
	st := useTestStore(t)
	j := &Job{ID: 31, Slug: "second-wall", AccountSlug: "slot-b", AccountEmail: "b@example.com"}

	resolved, err := json.Marshal(LimitHit{
		Error:         "rate_limit",
		Details:       "usage limit reached",
		At:            edgeAcctNow.Unix(),
		Deliveries:    4,
		HandledAt:     edgeAcctNow.Unix(),
		Outcome:       "rebound",
		SourceAccount: "a@example.com",
	})
	if err != nil {
		t.Fatalf("marshal seed: %v", err)
	}
	if err := st.SetKV(limitHitKey(j.ID), string(resolved)); err != nil {
		t.Fatalf("seed limit hit: %v", err)
	}

	hit, err := RecordLimitHit(j, "rate_limit", "usage limit reached", "sess-9", "", edgeAcctNow.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("RecordLimitHit: %v", err)
	}
	if hit.Deliveries != 1 || hit.HandledAt != 0 {
		t.Fatalf("a resolved hit must start a FRESH unresolved record, got %+v", hit)
	}
	if hit.SourceAccount != "b@example.com" {
		t.Errorf("SourceAccount = %q, want b@example.com — the new event names the account exhausted this time", hit.SourceAccount)
	}
}
