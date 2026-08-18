package core

// account_contract_test.go — athena-logic's BLIND contract tests for
// 50-contract.md, Slice S-core's SELECTION half: core.SelectBindTarget, the
// pure ranker the per-cell binder delegates its whole decision to.
//
// Written from the contract text alone. The working-tree versions of
// internal/core/{rotation,usagepool}.go were never read; the wire types
// (AccountUsage, UsageWindow, ModelWindow) and SelectRotationTarget's gate
// SEMANTICS as the contract restates them are PRE-EXISTING package state at
// the base commit.
//
// Dimension: LOGIC — the stated eligibility gates, the stated ranking order,
// stickiness, and the empty answer. Hostile inputs (nil slices with non-nil
// maps, NaN percentages, a prevSlug naming a row that is not there at all,
// duplicate slugs) are athena-edge's ground and are not probed here.
//
// core.SlotItemExists is deliberately NOT covered: the contract names no
// stubbable seam for its shell-out, and the environment obligation forbids a
// test from invoking security(1). Disclosed as a contract silence.
//
// Every package-level identifier declared here is prefixed `contractAcct`.

import (
	"strings"
	"testing"
)

// contractAcctNow is the fixed `now` (unix millis) every call below passes, so
// freshness and cooldown comparisons are deterministic.
const contractAcctNow int64 = 1_700_000_000_000

// contractAcctFreshAt is one minute before now — inside the contract's
// 15-minute freshness window by a wide margin. contractAcctStaleAt is one hour
// before now, outside it by a wide margin. Neither sits near the boundary on
// purpose: the boundary itself is athena-edge's.
const (
	contractAcctFreshAt = contractAcctNow - 60_000
	contractAcctStaleAt = contractAcctNow - 60*60*1000
)

// contractAcctRow is a fresh, fully eligible pool row: creds present, identity
// known, a session window under every gate, no Fable line, no weekly line, no
// cooldown.
func contractAcctRow(slug, email string, session float64) AccountUsage {
	return AccountUsage{
		Slug:      slug,
		Email:     email,
		Session:   &UsageWindow{Percent: session},
		FetchedAt: contractAcctFreshAt,
	}
}

// contractAcctFable returns a copy of a carrying one Fable model window.
func contractAcctFable(a AccountUsage, pct float64) AccountUsage {
	a.Models = []ModelWindow{{Name: "Fable", Percent: pct}}
	return a
}

// contractAcctFetched returns a copy of a with a different FetchedAt.
func contractAcctFetched(a AccountUsage, at int64) AccountUsage {
	a.FetchedAt = at
	return a
}

// contractAcctPick is the one call shape every test below uses.
func contractAcctPick(rows []AccountUsage, exclude []string, counts map[string]int, prev string) (string, string, string) {
	return SelectBindTarget(rows, exclude, counts, prev, contractAcctNow)
}

// contractAcctWantPick asserts the whole (slug, email, reason) triple for a
// successful selection. reason is "" on success — the contract reserves the
// reason string for the EMPTY answer, where it carries the per-candidate
// exclusions.
func contractAcctWantPick(t *testing.T, where string, rows []AccountUsage, exclude []string, counts map[string]int, prev, wantSlug, wantEmail string) {
	t.Helper()
	slug, email, reason := contractAcctPick(rows, exclude, counts, prev)
	if slug != wantSlug {
		t.Errorf("%s: slug = %q, want %q (reason %q)", where, slug, wantSlug, reason)
	}
	if email != wantEmail {
		t.Errorf("%s: email = %q, want %q — the picked row's own identity is what gets stamped on the job", where, email, wantEmail)
	}
	if reason != "" {
		t.Errorf("%s: reason = %q on a successful pick, want \"\"", where, reason)
	}
}

// contractAcctWantEmpty asserts the empty answer and returns the reason for
// the caller to inspect further.
func contractAcctWantEmpty(t *testing.T, where string, rows []AccountUsage, exclude []string, counts map[string]int, prev string) string {
	t.Helper()
	slug, email, reason := contractAcctPick(rows, exclude, counts, prev)
	if slug != "" || email != "" {
		t.Errorf("%s: got (%q, %q), want the empty pair", where, slug, email)
	}
	return reason
}

// ---------------------------------------------------------------------------
// the happy path
// ---------------------------------------------------------------------------

// TestContractAcctSelectBindTargetPicksTheOneEligibleRow is the base case
// every other test varies: one healthy slot, nothing bound anywhere, no
// stickiness. It returns that slot's slug AND its email — the email is what
// the job row is stamped with and what the rescue later excludes itself from,
// so a selector that returned only a slug would leave both without a source.
func TestContractAcctSelectBindTargetPicksTheOneEligibleRow(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	rows := []AccountUsage{contractAcctRow("alpha", "alpha@example.com", 12)}
	contractAcctWantPick(t, "one eligible row", rows, nil, nil, "", "alpha", "alpha@example.com")
}

// TestContractAcctSelectBindTargetIsPureOverItsInputs: the contract's first
// word about this function is "pure". Two identical calls must agree, and
// neither may reorder or rewrite the caller's slice — the binder holds that
// slice as the usage snapshot it also logs from, and a ranker that sorted it
// in place would silently reorder what the caller sees next.
func TestContractAcctSelectBindTargetIsPureOverItsInputs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	rows := []AccountUsage{
		contractAcctRow("zulu", "zulu@example.com", 70),
		contractAcctRow("alpha", "alpha@example.com", 10),
		contractAcctRow("mike", "mike@example.com", 40),
	}
	order := []string{rows[0].Slug, rows[1].Slug, rows[2].Slug}

	slug1, email1, reason1 := contractAcctPick(rows, nil, nil, "")
	slug2, email2, reason2 := contractAcctPick(rows, nil, nil, "")
	if slug1 != slug2 || email1 != email2 || reason1 != reason2 {
		t.Errorf("two identical calls disagreed: (%q,%q,%q) then (%q,%q,%q)", slug1, email1, reason1, slug2, email2, reason2)
	}
	for i, want := range order {
		if rows[i].Slug != want {
			t.Fatalf("the caller's slice was reordered: position %d is %q, want %q", i, rows[i].Slug, want)
		}
	}
}

// ---------------------------------------------------------------------------
// exclusions
// ---------------------------------------------------------------------------

// TestContractAcctSelectBindTargetExcludesTheMainSlugUnconditionally: the
// "main" row is the machine's own lane, not a pool slot, and binding a cell to
// it would put every cell back on the one account this whole feature exists to
// stop sharing. Unconditional means it loses even when it is the healthiest
// row in the snapshot and the only one there.
func TestContractAcctSelectBindTargetExcludesTheMainSlugUnconditionally(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	main := contractAcctRow("main", "main@example.com", 1)

	contractAcctWantEmpty(t, "main alone", []AccountUsage{main}, nil, nil, "")

	// and it does not win against a worse pool row either
	rows := []AccountUsage{main, contractAcctRow("alpha", "alpha@example.com", 70)}
	contractAcctWantPick(t, "main beside a worse pool row", rows, nil, nil, "", "alpha", "alpha@example.com")

	// nor by being named as the previous binding
	contractAcctWantPick(t, "main named as prevSlug", rows, nil, nil, "main", "alpha", "alpha@example.com")
}

// TestContractAcctSelectBindTargetExcludeEmailsIsCaseInsensitive: the caller's
// exclusion list carries identities (the machine's main identity, the cell's
// own currently-bound account on a rescue), and an email is case-insensitive
// on its own terms. A case-sensitive compare would let the save-back mirror
// slot back into the pool under a differently-cased spelling — which is a cell
// rebound onto the very account it just exhausted.
func TestContractAcctSelectBindTargetExcludeEmailsIsCaseInsensitive(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	rows := []AccountUsage{
		contractAcctRow("mirror", "Main@Example.COM", 1),
		contractAcctRow("alpha", "alpha@example.com", 70),
	}
	contractAcctWantPick(t, "an upper-cased exclusion", rows, []string{"main@example.com"}, nil, "", "alpha", "alpha@example.com")
	contractAcctWantPick(t, "a lower-cased row excluded by an upper-cased entry",
		[]AccountUsage{contractAcctRow("alpha", "alpha@example.com", 1), contractAcctRow("beta", "beta@example.com", 70)},
		[]string{"ALPHA@EXAMPLE.COM"}, nil, "", "beta", "beta@example.com")

	// every entry excludes, not just the first
	contractAcctWantEmpty(t, "both rows excluded", rows, []string{"MAIN@EXAMPLE.COM", "Alpha@Example.com"}, nil, "")
}

// TestContractAcctSelectBindTargetHonoursEveryRotationGate: the contract's
// eligibility gates are IDENTICAL to SelectRotationTarget's candidate gates
// and reuse its constants. Each row below trips exactly one gate; alone it
// yields the empty answer with a reason naming it, and beside a healthy row it
// never wins even though its numbers are better.
func TestContractAcctSelectBindTargetHonoursEveryRotationGate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cases := []struct {
		name string
		row  AccountUsage
	}{
		{"no creds", AccountUsage{Slug: "nocreds", Email: "n@example.com", Session: &UsageWindow{Percent: 1}, FetchedAt: contractAcctFreshAt, NoCreds: true}},
		{"reauth required", AccountUsage{Slug: "reauth", Email: "r@example.com", Session: &UsageWindow{Percent: 1}, FetchedAt: contractAcctFreshAt, Reauth: true}},
		{"no identity", AccountUsage{Slug: "noidentity", Session: &UsageWindow{Percent: 1}, FetchedAt: contractAcctFreshAt}},
		{"no session data", AccountUsage{Slug: "nosession", Email: "s@example.com", FetchedAt: contractAcctFreshAt}},
		{"cooling", AccountUsage{Slug: "cooling", Email: "c@example.com", Session: &UsageWindow{Percent: 1}, FetchedAt: contractAcctFreshAt, CooldownUntil: contractAcctNow + 1}},
		{"fable at the gate", contractAcctFable(contractAcctRow("fabled", "f@example.com", 1), 80)},
		{"session at the gate", contractAcctRow("sessioned", "x@example.com", 80)},
		{"weekly at the gate", func() AccountUsage {
			a := contractAcctRow("weekly", "w@example.com", 1)
			a.Weekly = &UsageWindow{Percent: 90}
			return a
		}()},
	}

	healthy := contractAcctRow("healthy", "healthy@example.com", 60)
	for _, c := range cases {
		reason := contractAcctWantEmpty(t, c.name+" alone", []AccountUsage{c.row}, nil, nil, "")
		if !strings.Contains(reason, c.row.Slug) {
			t.Errorf("%s: reason %q does not name the excluded slug %q", c.name, reason, c.row.Slug)
		}
		// the gated row's numbers are better than healthy's on every
		// ranking key, so a gate that did not fire would show up here
		contractAcctWantPick(t, c.name+" beside a healthy row",
			[]AccountUsage{c.row, healthy}, nil, nil, "", "healthy", "healthy@example.com")
	}
}

// TestContractAcctSelectBindTargetAcceptsTheEligibleSideOfEveryGate is the
// other half of the same obligation: the contract spells the gates with ≥ and
// >, and missing data is eligible rather than disqualifying. A gate written
// one notch too tight rejects healthy slots silently — the pool reads dry and
// every cell spawns unbound, which looks exactly like the feature being off.
func TestContractAcctSelectBindTargetAcceptsTheEligibleSideOfEveryGate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cooldownAtNow := contractAcctRow("coolnow", "coolnow@example.com", 1)
	// > now excludes; == now does not
	cooldownAtNow.CooldownUntil = contractAcctNow

	weeklyUnder := contractAcctRow("weeklyunder", "wu@example.com", 1)
	weeklyUnder.Weekly = &UsageWindow{Percent: 89.9}

	cases := []AccountUsage{
		cooldownAtNow,
		contractAcctFable(contractAcctRow("fableunder", "fu@example.com", 1), 79.9),
		contractAcctRow("sessionunder", "su@example.com", 79.9),
		weeklyUnder,
		// a nil Weekly window is eligible, as is a Fable line reading zero
		contractAcctRow("noweekly", "nw@example.com", 1),
		contractAcctFable(contractAcctRow("zerofable", "zf@example.com", 1), 0),
	}
	for _, row := range cases {
		contractAcctWantPick(t, row.Slug+" is eligible", []AccountUsage{row}, nil, nil, "", row.Slug, row.Email)
	}
}

// TestContractAcctSelectBindTargetEmptyAnswerJoinsEveryReason: when nothing
// survives, the caller's only observability is this string. The contract fixes
// its SHAPE — per-candidate reasons, semicolon-joined, each naming the slug and
// the gate it failed — because the binder logs it verbatim as bind-skipped and
// a bare "no target" would leave a dry pool undiagnosable.
func TestContractAcctSelectBindTargetEmptyAnswerJoinsEveryReason(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	rows := []AccountUsage{
		{Slug: "nocreds", Email: "n@example.com", Session: &UsageWindow{Percent: 1}, FetchedAt: contractAcctFreshAt, NoCreds: true},
		contractAcctRow("sessioned", "x@example.com", 95),
	}
	reason := contractAcctWantEmpty(t, "two gated rows", rows, nil, nil, "")
	if reason == "" {
		t.Fatal("empty answer carried no reason at all — a dry pool must say why per candidate")
	}
	for _, slug := range []string{"nocreds", "sessioned"} {
		if !strings.Contains(reason, slug) {
			t.Errorf("reason %q does not name %q — every considered candidate reports", reason, slug)
		}
	}
	if !strings.Contains(reason, ";") {
		t.Errorf("reason %q joins two candidate reasons without a semicolon", reason)
	}
}

// ---------------------------------------------------------------------------
// ranking
// ---------------------------------------------------------------------------

// TestContractAcctSelectBindTargetRanksBoundCountFirst is the whole point of
// the feature (Q3): distribution beats headroom. A slot with zero live bound
// cells wins over one already carrying a cell even when the busy slot is
// healthier on every other key — because two cells sharing an account is the
// exact situation the binder exists to avoid.
func TestContractAcctSelectBindTargetRanksBoundCountFirst(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// busy is the healthiest row by far; idle has the worst session % and no
	// live bound cell.
	busy := contractAcctRow("busy", "busy@example.com", 1)
	idle := contractAcctRow("idle", "idle@example.com", 70)
	counts := map[string]int{"busy": 0, "idle": 1}

	// sanity: with the counts the other way the healthier row wins, so the
	// assertion below is measuring the count and not the session %
	contractAcctWantPick(t, "counts favour busy", []AccountUsage{busy, idle}, nil, map[string]int{"idle": 1}, "", "busy", "busy@example.com")
	contractAcctWantPick(t, "counts favour idle", []AccountUsage{busy, idle}, nil, map[string]int{"busy": 1}, "", "idle", "idle@example.com")

	// and fewest wins rather than merely zero-wins: 1 beats 2
	one := contractAcctRow("one", "one@example.com", 70)
	two := contractAcctRow("two", "two@example.com", 1)
	contractAcctWantPick(t, "1 bound beats 2 bound", []AccountUsage{two, one},
		nil, map[string]int{"one": 1, "two": 2}, "", "one", "one@example.com")

	// a slug absent from the map counts as zero
	contractAcctWantPick(t, "an absent slug counts as zero", []AccountUsage{busy, idle}, nil, counts, "", "busy", "busy@example.com")
}

// TestContractAcctSelectBindTargetPrefersFreshOverStale: the second key. A row
// fetched within 15 minutes outranks one that is not, and FetchedAt == 0 is
// stale — a snapshot row that has never been fetched carries percentages that
// are guesses, and the tiebreak below it ranks on exactly those percentages.
func TestContractAcctSelectBindTargetPrefersFreshOverStale(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// the stale row is healthier, so only the freshness key can decide this
	stale := contractAcctFetched(contractAcctRow("stale", "stale@example.com", 1), contractAcctStaleAt)
	fresh := contractAcctRow("fresh", "fresh@example.com", 70)
	contractAcctWantPick(t, "fresh beats stale", []AccountUsage{stale, fresh}, nil, nil, "", "fresh", "fresh@example.com")

	never := contractAcctFetched(contractAcctRow("never", "never@example.com", 1), 0)
	contractAcctWantPick(t, "FetchedAt 0 is stale", []AccountUsage{never, fresh}, nil, nil, "", "fresh", "fresh@example.com")

	// but a stale row still binds when nothing fresh is eligible: a bad bind
	// costs one rebind, and refusing to bind costs the whole feature
	contractAcctWantPick(t, "stale alone still binds", []AccountUsage{stale}, nil, nil, "", "stale", "stale@example.com")

	// and freshness ranks BELOW the bound count, not above it
	contractAcctWantPick(t, "a bound fresh row loses to an unbound stale one",
		[]AccountUsage{fresh, stale}, nil, map[string]int{"fresh": 1}, "", "stale", "stale@example.com")
}

// TestContractAcctSelectBindTargetThenLowestSessionThenFableThenSlug walks the
// remaining three keys in order, each with the keys above it held equal.
func TestContractAcctSelectBindTargetThenLowestSessionThenFableThenSlug(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// session: equal counts, both fresh
	contractAcctWantPick(t, "lowest session wins",
		[]AccountUsage{contractAcctRow("high", "high@example.com", 60), contractAcctRow("low", "low@example.com", 20)},
		nil, nil, "", "low", "low@example.com")

	// fable: equal counts, both fresh, equal session
	contractAcctWantPick(t, "lowest fable breaks a session tie",
		[]AccountUsage{
			contractAcctFable(contractAcctRow("fhigh", "fhigh@example.com", 30), 60),
			contractAcctFable(contractAcctRow("flow", "flow@example.com", 30), 10),
		}, nil, nil, "", "flow", "flow@example.com")

	// a missing Fable window counts as 0 and therefore WINS the fable key
	contractAcctWantPick(t, "a missing fable window counts as zero",
		[]AccountUsage{
			contractAcctFable(contractAcctRow("hasfable", "hasfable@example.com", 30), 10),
			contractAcctRow("nofable", "nofable@example.com", 30),
		}, nil, nil, "", "nofable", "nofable@example.com")

	// slug: everything else equal, lexicographically smallest, and the input
	// order is deliberately reversed so first-wins would fail here
	contractAcctWantPick(t, "lexicographically smallest slug is last",
		[]AccountUsage{contractAcctRow("zulu", "zulu@example.com", 30), contractAcctRow("alpha", "alpha@example.com", 30)},
		nil, nil, "", "alpha", "alpha@example.com")
}

// ---------------------------------------------------------------------------
// stickiness (Q6)
// ---------------------------------------------------------------------------

// TestContractAcctSelectBindTargetStickyBeatsEveryRankingKey: "when prevSlug
// names an eligible candidate, it is returned BEFORE any ranking". The row
// below loses every single ranking key — most bound cells, stale, worst
// session, worst fable, last alphabetically — and still wins, because a
// reactivated job resuming on a different account than it ran on is a
// gratuitous move of a conversation between billing identities.
func TestContractAcctSelectBindTargetStickyBeatsEveryRankingKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	prev := contractAcctFetched(contractAcctFable(contractAcctRow("zulu", "zulu@example.com", 70), 70), contractAcctStaleAt)
	better := contractAcctRow("alpha", "alpha@example.com", 1)

	contractAcctWantPick(t, "sticky wins", []AccountUsage{prev, better},
		nil, map[string]int{"zulu": 3}, "zulu", "zulu", "zulu@example.com")

	// with no prevSlug the same inputs pick the other row — which is what
	// makes the assertion above about stickiness rather than about ordering
	contractAcctWantPick(t, "no prevSlug", []AccountUsage{prev, better},
		nil, map[string]int{"zulu": 3}, "", "alpha", "alpha@example.com")
}

// TestContractAcctSelectBindTargetStickyYieldsWhenTheRowIsIneligible: sticky
// is "when prevSlug names an ELIGIBLE candidate". A previous account that has
// since gone into reauth, or crossed a gate, must not hold the job — that is
// precisely the reactivation the ranking exists to re-decide.
func TestContractAcctSelectBindTargetStickyYieldsWhenTheRowIsIneligible(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	gated := AccountUsage{Slug: "zulu", Email: "zulu@example.com", Session: &UsageWindow{Percent: 1}, FetchedAt: contractAcctFreshAt, Reauth: true}
	other := contractAcctRow("alpha", "alpha@example.com", 40)
	contractAcctWantPick(t, "a gated prevSlug", []AccountUsage{gated, other}, nil, nil, "zulu", "alpha", "alpha@example.com")

	// an excluded email is not eligible either
	sticky := contractAcctRow("zulu", "zulu@example.com", 1)
	contractAcctWantPick(t, "an excluded prevSlug", []AccountUsage{sticky, other},
		[]string{"ZULU@example.com"}, nil, "zulu", "alpha", "alpha@example.com")

	// and a prevSlug with no eligible rows anywhere still yields the empty
	// answer rather than resurrecting itself
	contractAcctWantEmpty(t, "a gated prevSlug alone", []AccountUsage{gated}, nil, nil, "zulu")
}
