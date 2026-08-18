package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"kraken/internal/util"
)

// --- candidate thresholds ---
//
// candidate* are the bar a pool slot must clear to be considered usable —
// as a rotation's landing spot (the two manual callers, `k account <slug>`
// and the board's switch route) or as a cell's bind target (SelectBindTarget).
// They are ONE set for both, since "is this slot healthy enough to send work
// to" is one question; candidateGates is the one place they are applied.
//
// The trip thresholds that used to sit beside them (rotate*, read by
// NeedsRotation) are gone with automatic main rotation itself: nothing decides
// on its own initiative that the machine's login must move, so there is no
// longer any "main crossed into needs-to-swap territory" to measure. What is
// left is deliberately a landing-spot bar rather than a trigger — 80 for Fable
// rather than the 90 the old trigger used, because a slot handed new work
// needs meaningful headroom left, not merely to be under a threshold it would
// trip again almost immediately.
const (
	candidateSessionPct = 80
	candidateFablePct   = 80
	// candidateWeeklyPct excludes a pool slot whose OWN weekly window is
	// already at (or past) 90%. Without this, a swap or a bind can land on a
	// slot that is itself weekly-exhausted, which buys nothing: the account
	// runs out again immediately and the next limit hit moves the same work
	// straight back off it.
	candidateWeeklyPct = 90
)

// claudeJSONDisplayName is the literal string used in user-facing error
// messages — never the expanded absolute path, which would just be noise on
// someone else's machine.
const claudeJSONDisplayName = "~/.claude.json"

// FetchMainUsage does a fresh main-account usage fetch. Thin wrapper so
// callers outside this package (the jobs layer) don't need to know
// fetchAccountUsage's full argument shape (isSlot/slotDir/cooldown, all
// irrelevant for "main" right now).
func FetchMainUsage() AccountUsage {
	return fetchAccountUsage("main", false, "", 0)
}

// findModel looks up a model window by name, case-insensitively. A model can
// appear more than once in a snapshot; when it does, the window with the
// highest Percent wins — failing safe rather than first-match, since it's
// whichever window is closest to tripping that actually matters for a
// rotation decision.
func findModel(models []ModelWindow, name string) *ModelWindow {
	var best *ModelWindow
	for i := range models {
		if !strings.EqualFold(models[i].Name, name) {
			continue
		}
		if best == nil || models[i].Percent > best.Percent {
			best = &models[i]
		}
	}
	return best
}

// candidateGates applies the candidate* thresholds to ONE pool row and
// reports why it is unusable, "" meaning eligible. fablePct comes back
// alongside because both selectors below rank on it and re-deriving it in the
// caller is exactly how the gate and the ranking drift apart.
//
// Extracted rather than written twice: SelectRotationTarget picks a landing
// spot for a machine-wide swap and SelectBindTarget picks one for a single
// cell, but "is this slot usable at all" is the same question with the same
// answer, and two copies would agree on the day they were written and not
// after. The reason strings are the ones SelectRotationTarget has always
// emitted, so a caller logging them reads the same sentences it used to.
func candidateGates(a AccountUsage, now int64) (fablePct float64, reason string) {
	switch {
	case a.NoCreds:
		return 0, fmt.Sprintf("%s: no creds", a.Slug)
	case a.Reauth:
		return 0, fmt.Sprintf("%s: reauth required", a.Slug)
	case a.Email == "":
		return 0, fmt.Sprintf("%s: no identity", a.Slug)
	case a.Session == nil:
		return 0, fmt.Sprintf("%s: no session data", a.Slug)
	case a.CooldownUntil > now:
		return 0, fmt.Sprintf("%s: cooling", a.Slug)
	}

	// A missing Fable model window counts as eligible (0%), not
	// disqualifying — plenty of tiers/plans never see a Fable line.
	if fable := findModel(a.Models, "Fable"); fable != nil {
		fablePct = fable.Percent
		if fablePct >= candidateFablePct {
			return fablePct, fmt.Sprintf("%s: fable %g%% ≥ %d%%", a.Slug, fablePct, candidateFablePct)
		}
	}
	if a.Session.Percent >= candidateSessionPct {
		return fablePct, fmt.Sprintf("%s: session %g%% ≥ %d%%", a.Slug, a.Session.Percent, candidateSessionPct)
	}
	// A nil Weekly window (couldn't be fetched) stays eligible, same
	// convention as a missing Fable window above — rotating on missing
	// data would be a false exclusion, not a safe default.
	if a.Weekly != nil && a.Weekly.Percent >= candidateWeeklyPct {
		return fablePct, fmt.Sprintf("%s: weekly %g%% ≥ %d%%", a.Slug, a.Weekly.Percent, candidateWeeklyPct)
	}
	return fablePct, ""
}

// SelectRotationTarget ranks pool rows from a usage snapshot and picks the
// healthiest eligible landing spot for a rotation. Pure function over the
// snapshot — now (unix millis) is passed in rather than read from the clock
// so ranking is deterministic and testable.
//
// On success reason is "". When no candidate survives, slug is "" and reason
// lists why each considered slug was excluded, so a caller can log/alert
// something actionable instead of a bare "no target".
func SelectRotationTarget(accounts []AccountUsage, mainEmail string, now int64) (slug string, reason string) {
	if mainEmail == "" {
		// Without a known main identity we can't reliably exclude the main
		// account's own save-back mirror slot from the pool below, which
		// would make that mirror eligible and cause a self-rotation churn
		// loop. Fail closed instead of silently skipping the filter.
		return "", "main account email unknown"
	}
	mainEmailLower := strings.ToLower(mainEmail)

	var pool []AccountUsage
	for _, a := range accounts {
		if a.Slug == "main" {
			continue
		}
		// The main account is often mirrored into the pool as a save-back
		// slot (see SwapMainTo) — never treat it as a rotation target.
		if strings.ToLower(a.Email) == mainEmailLower {
			continue
		}
		pool = append(pool, a)
	}
	if len(pool) == 0 {
		return "", "no pool accounts"
	}

	type candidate struct {
		acct     AccountUsage
		fablePct float64
	}
	var eligible []candidate
	var excluded []string
	for _, a := range pool {
		fablePct, reason := candidateGates(a, now)
		if reason != "" {
			excluded = append(excluded, reason)
			continue
		}
		eligible = append(eligible, candidate{acct: a, fablePct: fablePct})
	}

	if len(eligible) == 0 {
		return "", strings.Join(excluded, "; ")
	}

	sort.SliceStable(eligible, func(i, j int) bool {
		ci, cj := eligible[i], eligible[j]
		if ci.acct.Session.Percent != cj.acct.Session.Percent {
			return ci.acct.Session.Percent < cj.acct.Session.Percent
		}
		if ci.fablePct != cj.fablePct {
			return ci.fablePct < cj.fablePct
		}
		return ci.acct.Slug < cj.acct.Slug
	})
	return eligible[0].acct.Slug, ""
}

// bindFreshWindow is how recently a pool row's OWN FetchedAt must have been
// stamped for SelectBindTarget to rank it ahead of an equally-loaded row whose
// numbers are older. It is a TIE-BREAK rather than a gate: a stale row is
// still bindable, because refusing to bind on stale data means the cell falls
// back to main, which is the outcome this whole feature exists to avoid —
// whereas the rotation path, which displaces a machine-wide login, refuses
// outright on stale data.
//
// It carries the same 15 minutes as the jobs layer's own freshness bound
// (jobs.rotationFreshSnapshotWindow, read this session) and is spelled out
// again here because core sits BELOW jobs in the dependency chain and cannot
// import it. The two are not required to move together: this one only orders
// equals.
const bindFreshWindow = 15 * time.Minute

// SelectBindTarget picks the pool account a single CELL should bind to — the
// per-cell sibling of SelectRotationTarget, and the reason that function's
// gates were pulled out into candidateGates. Pure over its inputs; now is unix
// millis, passed in rather than read from the clock so ranking is
// deterministic and testable.
//
// The two selectors differ only in what they optimise for, and the difference
// is the whole feature. A rotation is looking for the single HEALTHIEST place
// to move the machine's one login. A bind is looking for the EMPTIEST place to
// add one more cell, because the point is to spread cells across accounts
// rather than to stack them on whichever account happens to be healthiest
// right now — so boundCounts (live cells already bound per slug) leads the
// ranking and the health numbers only break its ties.
//
// The gates are candidateGates', unchanged: a slot too loaded to rotate onto
// is equally too loaded to bind a new cell to.
//
// excludeEmails is how a caller keeps a bind off an identity it already knows
// is wrong — the main account's save-back mirror at spawn, and additionally
// the cell's own exhausted account when the limit rescue rebinds. Matching is
// case-insensitive because these are email addresses, and an empty entry is
// IGNORED rather than treated as a match: it would otherwise silently swallow
// every row with no identity, which candidateGates already excludes with a
// reason naming the slug.
//
// prevSlug is the stickiness input (Q6): a job that was bound before — a
// reactivated cell, most of all — goes back to the SAME account when that
// account is still eligible, ahead of any ranking, because the account that
// ran the session holds its prompt cache and moving the cell would burn the
// new window on re-reading context it already paid for once. Compared
// case-insensitively for sameSlotDir's reason: pool slugs are directory names
// on a case-insensitive filesystem, so two spellings name one account.
//
// On success reason is "". When nothing survives, slug and email are "" and
// reason carries the per-candidate exclusions, so a caller can say what the
// pool actually looked like instead of a bare "no target".
func SelectBindTarget(candidates []AccountUsage, excludeEmails []string, boundCounts map[string]int, prevSlug string, now int64) (slug, email, reason string) {
	skip := make(map[string]bool, len(excludeEmails))
	for _, e := range excludeEmails {
		if e == "" {
			continue
		}
		skip[strings.ToLower(e)] = true
	}

	var pool []AccountUsage
	for _, a := range candidates {
		// main is never a bind target, whatever the caller passed: the whole
		// point of a per-cell binding is to leave the machine's login to
		// carl's own interactive sessions.
		if a.Slug == "main" {
			continue
		}
		if skip[strings.ToLower(a.Email)] {
			continue
		}
		pool = append(pool, a)
	}
	if len(pool) == 0 {
		return "", "", "no pool accounts"
	}

	type bindCandidate struct {
		acct     AccountUsage
		fablePct float64
		bound    int
		fresh    bool
	}
	var eligible []bindCandidate
	var excluded []string
	for _, a := range pool {
		fablePct, r := candidateGates(a, now)
		if r != "" {
			excluded = append(excluded, r)
			continue
		}
		eligible = append(eligible, bindCandidate{
			acct:     a,
			fablePct: fablePct,
			bound:    boundCounts[a.Slug],
			// FetchedAt 0 is "never fetched", which is stale by definition
			// rather than infinitely old.
			fresh: a.FetchedAt != 0 && now-a.FetchedAt < bindFreshWindow.Milliseconds(),
		})
	}
	if len(eligible) == 0 {
		return "", "", strings.Join(excluded, "; ")
	}

	// Stickiness is checked against the ELIGIBLE set, not the pool: a previous
	// account that has since exhausted must lose the cell rather than hold it,
	// which is the same judgement the limit rescue makes when it rebinds.
	if prevSlug != "" {
		for _, c := range eligible {
			if strings.EqualFold(c.acct.Slug, prevSlug) {
				return c.acct.Slug, c.acct.Email, ""
			}
		}
	}

	sort.SliceStable(eligible, func(i, j int) bool {
		ci, cj := eligible[i], eligible[j]
		if ci.bound != cj.bound {
			return ci.bound < cj.bound
		}
		// Fresh before stale, and only here: two rows with the same number of
		// cells on them are ranked on health numbers next, so preferring the
		// row whose numbers were actually measured recently is what keeps a
		// slot that went quiet hours ago from winning on a percentage nobody
		// has re-read since.
		if ci.fresh != cj.fresh {
			return ci.fresh
		}
		if ci.acct.Session.Percent != cj.acct.Session.Percent {
			return ci.acct.Session.Percent < cj.acct.Session.Percent
		}
		if ci.fablePct != cj.fablePct {
			return ci.fablePct < cj.fablePct
		}
		return ci.acct.Slug < cj.acct.Slug
	})
	return eligible[0].acct.Slug, eligible[0].acct.Email, ""
}

// --- ~/.claude.json access, isolated for tests ---
//
// claudeJSONPath itself lives in usagepool.go, next to mainAccountMeta —
// both read the same file and should share one swappable path.

// readClaudeJSON parses a .claude.json-shaped document as a raw top-level
// map so callers can round-trip every key they don't touch. A missing file
// is not an error (a slot may not have its own .claude.json yet) — it
// returns a nil map. A malformed (present but unparseable) file IS an error;
// callers that want defensive degrade-on-malformed behavior (slot files,
// owned by another program) must handle that themselves rather than relying
// on this function to swallow it. The returned error is the bare parse
// error, with no path embedded — the path is often an absolute filesystem
// path that's noise (or a privacy leak) in a user-facing message, so callers
// that want to name the file add their own context (e.g. claudeJSONDisplayName)
// when they wrap this error.
func readClaudeJSON(path string) (map[string]json.RawMessage, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	return doc, nil
}

// slotClaudeJSONPath returns the path to a pool slot's own .claude.json —
// the identity-mirroring file ccp owns, distinct from ~/.claude.json itself.
func slotClaudeJSONPath(slotDir string) string {
	return filepath.Join(slotDir, ".claude.json")
}

// sameSlotDir reports whether two slot directory paths refer to the same
// physical slot. A plain byte-compare isn't enough: macOS APFS (the default
// filesystem save-back slots live on) is case-insensitive, so a
// case-differing spelling of an existing slug (e.g. "Main-Example-Com" vs
// "main-example-com") is the SAME directory on disk even though the two Go
// strings differ. strings.EqualFold catches that without touching the
// filesystem; os.SameFile is a belt-and-suspenders check for when both paths
// happen to stat successfully (e.g. any other path-equivalence a bare
// case-fold wouldn't catch).
//
// This deliberately assumes a case-insensitive filesystem. On a
// case-sensitive volume, two differently-cased directory names really are
// distinct, and EqualFold would then incorrectly treat them as the same
// slot — but the only consequence is an extra, unnecessary refusal (e.g.
// "save-back slot would be the rotation target" for two dirs that were
// actually fine to use independently). That's fail-closed, not fail-open,
// so it's accepted rather than worth detecting the filesystem's actual case
// sensitivity at runtime.
func sameSlotDir(a, b string) bool {
	if strings.EqualFold(a, b) {
		return true
	}
	fiA, errA := os.Stat(a)
	fiB, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(fiA, fiB)
}

// --- rotation swap ---

// validateSlug rejects slot slugs that aren't a plain directory name — this
// is user/API-facing input, and it gets joined straight into a filesystem
// path via PoolDir(), so anything that could escape the pool dir (path
// separators, "..", a leading dot masquerading as a hidden/marker file) must
// be refused before it's ever joined.
func validateSlug(slug string) error {
	if slug == "" || slug != filepath.Base(slug) || strings.HasPrefix(slug, ".") {
		return fmt.Errorf("invalid slot slug %q: %w", slug, ErrInvalidSlug)
	}
	return nil
}

// ErrInvalidSlug sentinels a validateSlug rejection — bad, path-unsafe, or
// empty user/API-facing input, as opposed to a slug that's well-formed but
// doesn't resolve to a real slot. Callers classify on this via errors.Is
// (see api/account.go's 400 mapping) rather than matching the human-facing
// message.
var ErrInvalidSlug = errors.New("invalid slot slug")

// ErrRotationInFlight sentinels a refusal caused by an unresolved rotation
// journal (see journalRefusalError below) — a previous rotation that crashed
// or otherwise never reached its commit point. Callers classify on this via
// errors.Is rather than matching substrings against the human-facing
// message, which is free to reword without silently breaking that
// classification elsewhere (see jobs/rotation.go's swap-failure dedup key).
var ErrRotationInFlight = errors.New("rotation in flight")

// journalRefusalError formats the "previous rotation did not complete"
// refusal shared by the top-of-function warm read and the os.O_EXCL race at
// the commit point below. data is the journal's content — empty if it
// couldn't be read (e.g. we lost the O_EXCL race against a write that's
// still in flight). The journal can also be an orphan left behind by a
// rotation that failed before ever reaching the commit point, so the
// wording doesn't assert main IS diverged, only that it might be. Wraps
// ErrRotationInFlight so callers outside this package can classify the
// refusal with errors.Is instead of parsing the message.
func journalRefusalError(inflight, data string) error {
	return fmt.Errorf("previous rotation (%s) did not complete; main login MAY be diverged (or the rotation failed before committing) — verify with ccp status; if main looks right, rm %s, else run ccp login first: %w",
		strings.TrimSpace(data), inflight, ErrRotationInFlight)
}

// SwapMainTo installs the given pool slot's credentials as the main Claude
// account login. Step order is load-bearing: everything through the
// save-back write is either read-only or writes only to the target/save-back
// slot, so any error there leaves the main login completely untouched; the
// INSTALL step (writeMainCreds + ~/.claude.json) is the sole point of no
// return, and every fallible read that step's write depends on is resolved
// before writeMainCreds runs.
func SwapMainTo(slug string) (from, to string, err error) {
	if err := validateSlug(slug); err != nil {
		return "", "", err
	}

	// Canonicalize a case-differing caller spelling to the on-disk slug
	// spelling before any path is built from it. slotKeychainService hashes
	// the literal path string, and macOS APFS (the default filesystem the
	// pool lives on) is case-insensitive, so "pool/Target-Slot" and
	// "pool/target-slot" are the SAME physical directory but would hash to
	// DIFFERENT keychain services. Without this, a refresh triggered by a
	// miscased caller spelling mirrors the refreshed token under the wrong
	// service, leaving the canonical slot's keychain entry stale — so the
	// slot bricks (Reauth) on the next read despite a good
	// .credentials.json file.
	//
	// An exact spelling match always wins over a fold-equal one: on a
	// case-SENSITIVE volume (not the macOS default, but not impossible —
	// e.g. an exported filesystem), two differently-cased directories are
	// genuinely distinct slots, and silently redirecting an exact spelling
	// to a fold-equal sibling would rotate into the wrong slot entirely.
	// Only fall back to the fold-equal match when nothing spells `slug`
	// exactly, which is the APFS-miscased-caller case this loop exists for.
	slugs := poolSlugs()
	exactMatch := false
	for _, s := range slugs {
		if s == slug {
			exactMatch = true
			break
		}
	}
	if !exactMatch {
		for _, s := range slugs {
			if strings.EqualFold(s, slug) {
				slug = s
				break
			}
		}
	}
	slotDir := filepath.Join(PoolDir(), slug)

	// A previous rotation that crashed (or failed to roll back) between the
	// keychain write and the ~/.claude.json write leaves this journal
	// behind — proceeding would risk compounding that split-brain (see the
	// INSTALL section below), so refuse until a human clears it. Fail
	// closed on a read error other than "no journal file" too: an ambiguous
	// stat/read failure here is not a safe "proceed" default either.
	//
	// The journal is not a lock: the os.O_EXCL create at the commit point
	// below is what actually closes the write race against a concurrent
	// rotation. This warm read exists only so a pre-existing journal gets
	// the friendlier "previous rotation (...) did not complete" message
	// instead of a bare EEXIST surfacing from the O_EXCL create later.
	inflight := filepath.Join(PoolDir(), ".rotation-inflight")
	if data, readErr := os.ReadFile(inflight); readErr == nil {
		return "", "", journalRefusalError(inflight, string(data))
	} else if !os.IsNotExist(readErr) {
		return "", "", fmt.Errorf("check rotation journal %q: %w", inflight, readErr)
	}

	// Fail closed: a stat error other than "not there" (permission denied,
	// slotDir not even a directory, ...) is ambiguous, and rotating into a
	// slot we can't positively confirm is enabled is not a safe default —
	// only a clean IsNotExist means "no marker, proceed".
	if _, statErr := os.Stat(filepath.Join(slotDir, ".disabled")); statErr == nil {
		return "", "", fmt.Errorf("slot %q is disabled", slug)
	} else if !os.IsNotExist(statErr) {
		return "", "", fmt.Errorf("slot %q: cannot check .disabled marker: %w", slug, statErr)
	}

	// 1. Load target creds + identity.
	targetCreds, ok := loadSlotCreds(slotDir)
	if !ok {
		return "", "", fmt.Errorf("rotation target slot %q: no credentials", slug)
	}
	targetMeta := slotAccountMeta(slotDir)

	// 2. Refresh target creds if expired, persisting the result so the
	// slot itself stays current regardless of whether the swap proceeds.
	now := time.Now()
	if targetCreds.expired(now) {
		updated, outcome, reason := refreshToken(targetCreds, now)
		if outcome != refreshOK {
			return "", "", fmt.Errorf("rotation target slot %q needs ccp login: %s", slug, reason)
		}
		if err := writeSlotCreds(slotDir, updated); err != nil {
			return "", "", fmt.Errorf("rotation target slot %q: persist refreshed creds: %w", slug, err)
		}
		targetCreds = updated
	}

	// 3. Load main creds + identity. If we can't read main, we also can't
	// save it back, so this is a hard abort before anything is touched.
	mainCreds, ok := loadMainCreds()
	if !ok {
		return "", "", fmt.Errorf("main account credentials unavailable")
	}
	mainMeta := mainAccountMeta()

	// 4. Find (or create) the slot that mirrors the main account, so its
	// current creds aren't lost when the main login is overwritten below.
	saveBackDir, err := ensureSaveBackSlot(mainMeta, slotDir)
	if err != nil {
		return "", "", err
	}

	// 5. Save main's creds + identity into that slot.
	if err := writeSlotCreds(saveBackDir, mainCreds); err != nil {
		return "", "", fmt.Errorf("save-back main creds to %q: %w", saveBackDir, err)
	}
	// Verify the write actually took: loadSlotCreds prefers the keychain, so
	// a stale slot-keychain entry from a previous account occupying this
	// same slot dir would otherwise silently shadow the file we just wrote,
	// and the save-back would look successful while quietly discarding the
	// outgoing creds.
	if reloaded, ok := loadSlotCreds(saveBackDir); !ok || reloaded.ClaudeAiOauth.AccessToken != mainCreds.ClaudeAiOauth.AccessToken {
		return "", "", fmt.Errorf("save-back slot %q: persisted creds did not round-trip (stale keychain entry?)", saveBackDir)
	}
	if err := mirrorMainIdentityToSlot(saveBackDir); err != nil {
		return "", "", fmt.Errorf("save-back main identity to %q: %w", saveBackDir, err)
	}

	// 6. Resolve every fallible read the install needs, entirely before the
	// commit point below — nothing after this point may fail due to a read.
	installPath, installData, installPerm, err := prepareMainInstall(slotDir, targetMeta)
	if err != nil {
		return "", "", err
	}

	// 6.5. Re-check the target slot's own creds right before the commit
	// point. Everything above (including the refresh in step 2) can take
	// long enough — keychain prompts, disk I/O, a slow refresh HTTP round
	// trip — for a concurrent UsagePass refresh of this SAME slot to land in
	// between step 1's read and here, rotating its refresh token out from
	// under us. Installing step 1's now-superseded access token would still
	// look fine today, but its paired refresh token is already invalidated
	// on the provider side by the concurrent refresh — the NEXT refresh
	// attempt against it would be rejected outright, bricking main into
	// reauth. Prefer whatever's freshest on disk now; a failed re-read
	// (transient, keychain locked) degrades to the step-1 creds rather than
	// aborting the swap over a read hiccup this late.
	if fresher, ok := loadSlotCreds(slotDir); ok &&
		fresher.ClaudeAiOauth.AccessToken != targetCreds.ClaudeAiOauth.AccessToken &&
		!fresher.expired(now) {
		targetCreds = fresher
	}

	// 7. INSTALL. Everything above is reversible / hasn't touched the main
	// login — from here on we're committed: exactly two writes, both
	// pre-computed, neither of which can fail on a read.
	//
	// The rotation journal brackets those two writes. A crash (or a
	// subsequent process) between writeMainCreds and the ~/.claude.json
	// write — or a failed rollback below — would otherwise leave the
	// keychain naming the target while ~/.claude.json (and thus the
	// outgoing account's identity) still names the old main; the NEXT
	// rotation would then silently save the CURRENT (target) creds over
	// the OLD main's own slot under the old main's name, destroying them.
	// Created with O_EXCL (not atomicWriteFile) so this create is also the
	// thing that closes the write race against a concurrent rotation that
	// slipped past the warm read above — a plain EEXIST here gets the same
	// friendly refusal as that warm read. Any other create/write/close
	// error is still a pre-commit abort, before either of the two real
	// writes below.
	jf, jerr := os.OpenFile(inflight, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if jerr != nil {
		if os.IsExist(jerr) {
			data, _ := os.ReadFile(inflight)
			return "", "", journalRefusalError(inflight, string(data))
		}
		return "", "", fmt.Errorf("write rotation journal: %w", jerr)
	}
	if _, werr := jf.Write([]byte(mainMeta.Email + " -> " + targetMeta.Email)); werr != nil {
		jf.Close()
		// Pre-commit abort: nothing has been written to the keychain or
		// ~/.claude.json yet, so an orphaned journal here would wedge every
		// future rotation behind a "previous rotation did not complete"
		// refusal for no reason.
		_ = os.Remove(inflight)
		return "", "", fmt.Errorf("write rotation journal: %w", werr)
	}
	if cerr := jf.Close(); cerr != nil {
		// Same pre-commit abort as above — the write succeeded but Close
		// (flush) didn't, still before either real write.
		_ = os.Remove(inflight)
		return "", "", fmt.Errorf("write rotation journal: %w", cerr)
	}

	if err := writeMainCreds(targetCreds); err != nil {
		// If main still holds the OUTGOING creds, nothing was committed —
		// this is an ordinary pre-commit abort (keychain locked/denied),
		// so drop the journal rather than wedging every future rotation
		// behind it. Ambiguous keychain state here (unreadable, or already
		// holding something other than what we started with) is left for
		// a human instead — it's not a case we can positively rule safe.
		if cur, ok := loadMainCreds(); ok && cur.ClaudeAiOauth.AccessToken == mainCreds.ClaudeAiOauth.AccessToken {
			_ = os.Remove(inflight)
		}
		return "", "", fmt.Errorf("install target creds: %w", err)
	}
	if err := atomicWriteFile(installPath, installData, installPerm); err != nil {
		// The keychain now names the target but ~/.claude.json still names
		// main — that split-brain is worse than the original error, so put
		// the keychain back before surfacing anything. securityAdd -U is
		// idempotent, so re-writing mainCreds here is safe even though we
		// already wrote it as part of the save-back step above. BUT: only
		// skip the rollback when we can POSITIVELY confirm some other
		// process (a concurrent rotation, a fresh `ccp login`) rewrote the
		// main keychain to a DIFFERENT token in the meantime — blindly
		// overwriting that would clobber THEIR write instead of ours,
		// worse than leaving ours in place. A keychain read that merely
		// FAILS (locked, transient) is ambiguous, not a confirmed foreign
		// write — the hazard this guards against only exists once a
		// foreign token is actually observed, so attempt the rollback
		// anyway rather than leaving the journal for a human over a plain
		// read hiccup.
		cur, curOK := loadMainCreds()
		// A concurrent actor (another rotation's own rollback, a fresh `ccp
		// login`) may have already put the ORIGINAL outgoing creds back in
		// place while we were between the keychain write and this check —
		// in that case main is already correct and our own rollback would be
		// a redundant (harmless but noisy) re-write; just clear the journal
		// and surface the original error.
		if curOK && cur.ClaudeAiOauth.AccessToken == mainCreds.ClaudeAiOauth.AccessToken {
			_ = os.Remove(inflight)
			return "", "", fmt.Errorf("update %s: %w (main creds already back in place)", claudeJSONDisplayName, err)
		}
		foreignWrite := curOK && cur.ClaudeAiOauth.AccessToken != targetCreds.ClaudeAiOauth.AccessToken
		if foreignWrite {
			return "", "", fmt.Errorf("update %s: %w (rollback skipped (main rewritten concurrently): main may hold neither %s's nor %s's creds while %s still names %s; run ccp login)",
				claudeJSONDisplayName, err, targetMeta.Email, mainMeta.Email, claudeJSONDisplayName, mainMeta.Email)
		}
		if rbErr := writeMainCreds(mainCreds); rbErr != nil {
			return "", "", fmt.Errorf("update %s: %w (ROLLBACK FAILED: %v — main keychain holds %s's creds while %s still names %s; run ccp login)",
				claudeJSONDisplayName, err, rbErr, targetMeta.Email, claudeJSONDisplayName, mainMeta.Email)
		}
		_ = os.Remove(inflight)
		return "", "", fmt.Errorf("update %s: %w (main creds rolled back)", claudeJSONDisplayName, err)
	}

	_ = os.Remove(inflight)
	return mainMeta.Email, targetMeta.Email, nil
}

// ensureSaveBackSlot finds the pool slot whose identity matches the main
// account (case-insensitive email), auto-creating one if none exists so a
// rotation never has nowhere to put the outgoing main creds. targetSlotDir
// is the slot being rotated INTO — the save-back slot must never resolve to
// it, since that would clobber the very creds the rotation is trying to
// install with the outgoing main creds.
// ErrSaveBackIsTarget sentinels the refusal ensureSaveBackSlot raises when
// the pool slot that would receive the outgoing main account's creds turns
// out to be the very slot the rotation is trying to install — installing
// there would clobber the incoming target creds with the outgoing ones.
// Callers classify on this via errors.Is (see api/account.go's 409 mapping)
// rather than matching the human-facing message, which is free to reword
// without silently breaking that classification elsewhere.
var ErrSaveBackIsTarget = errors.New("save-back slot would be the rotation target")

func ensureSaveBackSlot(mainMeta accountMeta, targetSlotDir string) (string, error) {
	if mainMeta.Email == "" {
		// Pre-install: aborting here is always safe. Proceeding would slugify
		// "" to "" and join it against PoolDir() to get the pool ROOT itself
		// — an invisible-to-poolSlugs() write location that would then get
		// overwritten by the very next INSTALL step below.
		return "", fmt.Errorf("main account identity unknown; refusing to rotate")
	}
	mainEmailLower := strings.ToLower(mainMeta.Email)
	var disabledSlug string
	for _, s := range poolSlugs() {
		dir := filepath.Join(PoolDir(), s)
		if strings.ToLower(slotAccountMeta(dir).Email) != mainEmailLower {
			continue
		}
		if sameSlotDir(dir, targetSlotDir) {
			return "", fmt.Errorf("slot %q: %w", s, ErrSaveBackIsTarget)
		}
		// The outgoing main creds are about to land here — if this slot is
		// disabled, they'd land somewhere the pool never surfaces (NoCreds
		// forever), silently losing the account. Skip a disabled match
		// rather than failing outright and keep scanning: a healthy mirror
		// later in sort order must still win over an earlier, stale/
		// disabled one. Only if every email-matched slot turns out disabled
		// do we actually fail, below.
		_, statErr := os.Stat(filepath.Join(dir, ".disabled"))
		switch {
		case statErr == nil:
			disabledSlug = s
			continue
		case !os.IsNotExist(statErr):
			return "", fmt.Errorf("save-back slot %q: cannot check .disabled marker: %w", s, statErr)
		}
		return dir, nil
	}
	if disabledSlug != "" {
		return "", fmt.Errorf("save-back slot %q is disabled", disabledSlug)
	}

	dirName := util.Slugify(mainMeta.Email)
	if dirName == "" {
		return "", fmt.Errorf("main account email %q slugifies to an empty slot name; refusing to rotate", mainMeta.Email)
	}
	dir := filepath.Join(PoolDir(), dirName)
	if sameSlotDir(dir, targetSlotDir) {
		return "", fmt.Errorf("slot %q: %w", dirName, ErrSaveBackIsTarget)
	}

	if err := os.MkdirAll(PoolDir(), 0o755); err != nil {
		return "", fmt.Errorf("create pool dir: %w", err)
	}
	// os.Mkdir (not MkdirAll) on the leaf: MkdirAll treats an existing dir as
	// success, which would let us silently write into a directory some other
	// account already owns — one whose account.json is missing, corrupt, or
	// simply a different email than what the loop above matched on. Any of
	// those means the dir belongs to someone else's slot, not ours.
	if err := os.Mkdir(dir, 0o755); err != nil {
		if os.IsExist(err) {
			return "", fmt.Errorf("save-back slot name %q collides with an existing slot", dirName)
		}
		return "", fmt.Errorf("create save-back slot %q: %w", dirName, err)
	}
	accountJSON, err := json.Marshal(struct {
		Email         string `json:"email"`
		DisplayName   string `json:"display_name"`
		RateLimitTier string `json:"rate_limit_tier"`
	}{mainMeta.Email, mainMeta.DisplayName, mainMeta.Tier})
	if err != nil {
		return "", err
	}
	if err := atomicWriteFile(filepath.Join(dir, "account.json"), accountJSON, 0o644); err != nil {
		return "", fmt.Errorf("write save-back account.json: %w", err)
	}
	return dir, nil
}

// mirrorMainIdentityToSlot copies ~/.claude.json's top-level oauthAccount
// block (main's identity) into <slotDir>/.claude.json, merging into that
// file's existing top-level keys rather than clobbering them.
func mirrorMainIdentityToSlot(slotDir string) error {
	mainDoc, err := readClaudeJSON(claudeJSONPath())
	if err != nil {
		// readClaudeJSON's non-nil error here is usually a parse failure
		// (a missing file returns a nil map, not an error) — name both
		// possibilities rather than implying this is only ever a read error.
		return fmt.Errorf("read or parse %s: %w", claudeJSONDisplayName, err)
	}
	oauthAccount, ok := mainDoc["oauthAccount"]
	if !ok {
		return nil // nothing to mirror
	}

	slotJSONPath := slotClaudeJSONPath(slotDir)
	// Defensive-degrade on a malformed save-back-slot .claude.json: it's a
	// ccp-owned file, and an unparseable one has nothing worth preserving —
	// error here would abort a rotation over a file we don't even own.
	slotDoc, err := readClaudeJSON(slotJSONPath)
	if err != nil {
		slotDoc = nil
	}
	if slotDoc == nil {
		slotDoc = map[string]json.RawMessage{}
	}
	slotDoc["oauthAccount"] = oauthAccount

	data, err := json.Marshal(slotDoc)
	if err != nil {
		return err
	}
	return atomicWriteFile(slotJSONPath, data, 0o600)
}

// resolveTargetOauthAccount picks the oauthAccount block to install into
// ~/.claude.json for the target slot. targetMeta (from the slot's
// account.json) is the authoritative identity — an empty targetMeta.Email
// means the slot has no identity at all, and that's a hard error
// unconditionally, before even looking at the slot's .claude.json: installing
// an empty identity would manufacture the exact precondition F1 guards
// against on the next rotation. The slot's own .claude.json is read
// defensively — it's a file some other program (ccp) owns, so a missing or
// malformed one degrades to the synthesized fallback rather than erroring,
// matching the convention for externally-owned files. Its oauthAccount is
// only trusted when its emailAddress matches targetMeta.Email
// (case-insensitively): a slot dir can be reused across accounts (see
// ensureSaveBackSlot's collision handling), and a stale .claude.json left
// over from a PREVIOUS occupant of this slot dir must never get installed
// under a DIFFERENT account's name — the synthesized identity from
// targetMeta (sourced from this slot's own account.json) is always safe.
//
// Note the agreement check above is case-folded, not exact: when the
// slot's .claude.json IS trusted, the identity actually installed into
// ~/.claude.json is that doc's richer, ccp-written fields (display name,
// tier, etc., as ccp recorded them at login time) — not targetMeta's. So
// the installed email's casing can differ from targetMeta.Email even
// though they name the same account; SwapMainTo's returned `to` value
// always reports targetMeta.Email (account.json's casing), a deliberately
// separate source of truth from whatever identity block actually gets
// installed here.
func resolveTargetOauthAccount(slotDir string, targetMeta accountMeta) (json.RawMessage, error) {
	if targetMeta.Email == "" {
		return nil, fmt.Errorf("target slot has no identity; run ccp login")
	}

	if slotDoc, err := readClaudeJSON(slotClaudeJSONPath(slotDir)); err == nil && slotDoc != nil {
		if raw, ok := slotDoc["oauthAccount"]; ok {
			var identity struct {
				EmailAddress string `json:"emailAddress"`
			}
			if json.Unmarshal(raw, &identity) == nil && identity.EmailAddress != "" &&
				strings.EqualFold(identity.EmailAddress, targetMeta.Email) {
				return raw, nil
			}
		}
	}

	synthesized, err := json.Marshal(struct {
		EmailAddress              string `json:"emailAddress"`
		DisplayName               string `json:"displayName,omitempty"`
		OrganizationRateLimitTier string `json:"organizationRateLimitTier,omitempty"`
	}{targetMeta.Email, targetMeta.DisplayName, targetMeta.Tier})
	if err != nil {
		return nil, err
	}
	return synthesized, nil
}

// prepareMainInstall resolves everything needed to write the target's
// identity into ~/.claude.json — path, existing permissions, the existing
// doc (round-tripping every key we don't touch), and the resolved
// oauthAccount block — WITHOUT writing anything. Every fallible read lives
// here so SwapMainTo can call this before its commit point: a malformed MAIN
// ~/.claude.json is a hard error (unlike a malformed SLOT .claude.json,
// which degrades — see resolveTargetOauthAccount), and it must surface here,
// before writeMainCreds, not after.
func prepareMainInstall(slotDir string, targetMeta accountMeta) (path string, data []byte, perm os.FileMode, err error) {
	path = claudeJSONPath()

	perm = 0o600
	if info, statErr := os.Stat(path); statErr == nil {
		perm = info.Mode().Perm()
	} else if !os.IsNotExist(statErr) {
		return "", nil, 0, statErr
	}

	doc, err := readClaudeJSON(path)
	if err != nil {
		// readClaudeJSON's error has no path embedded (see its doc comment)
		// — this is the one and only place the path gets named, and it's
		// named with the display string, never the expanded absolute path.
		return "", nil, 0, fmt.Errorf("parse %s: %w", claudeJSONDisplayName, err)
	}
	if doc == nil {
		doc = map[string]json.RawMessage{}
	}

	oauthAccount, err := resolveTargetOauthAccount(slotDir, targetMeta)
	if err != nil {
		return "", nil, 0, err
	}
	doc["oauthAccount"] = oauthAccount

	data, err = json.Marshal(doc)
	if err != nil {
		return "", nil, 0, err
	}
	return path, data, perm, nil
}
