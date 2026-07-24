package jobs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kraken/internal/core"
	"kraken/internal/util"
)

// rotation_test.go — the mode store (ReadRotationState/WriteRotationState/
// SetRotationMode), the notify-dedup helper, the flock-based rotation lock,
// and RotationGate's decision matrix, all exercised via the package-level
// swappable seams (fetchMainUsage, selectTarget, swapMain, notifyFn,
// runUsagePass) so nothing here touches the network, keychain, or osascript.

type notifyCall struct{ title, msg string }

// stubRotation isolates HOME to a fresh temp dir (so rotation.json/
// rotation.lock/usage.json never touch the real ones) and swaps notifyFn for
// a recording stub, restoring every swapped var on test cleanup.
func stubRotation(t *testing.T) *[]notifyCall {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	// usagePassRunning is a process-global atomic shared with the daemon's
	// own periodic poll (see daemon.go); some daemon tests exercise tick()'s
	// first-tick-eager path directly against the real UsagePass and its
	// background goroutine can still be in flight when this test starts.
	// Force a known state so that lingering goroutine (harmless — it only
	// flips the flag back on completion) can't make CompareAndSwap below
	// spuriously fail.
	usagePassRunning.Store(false)

	origFetch, origSelect, origSwap, origNotify, origPass :=
		fetchMainUsage, selectTarget, swapMain, notifyFn, runUsagePass
	t.Cleanup(func() {
		fetchMainUsage, selectTarget, swapMain, notifyFn, runUsagePass =
			origFetch, origSelect, origSwap, origNotify, origPass
	})

	var calls []notifyCall
	notifyFn = func(title, msg string) { calls = append(calls, notifyCall{title, msg}) }
	// Default to a no-op: most tests below don't care about the post-swap
	// usage pass, and letting it default to the real UsagePass would fire a
	// real background network fetch (and race the usagePassRunning flag
	// against whichever test runs next). Tests that DO care (the swap-
	// success test) override this again after calling stubRotation.
	runUsagePass = func() error { return nil }
	return &calls
}

// --- RotationState store ---

func TestReadRotationStateDefaultsOnMissingOrCorrupt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if st := ReadRotationState(); st.Mode != "auto" || st.SetBy != "" || st.SetAt != 0 || st.LastNotice != (NoticeState{}) {
		t.Fatalf("missing file: got %+v, want zero-value auto", st)
	}

	path := rotationStatePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if st := ReadRotationState(); st.Mode != "auto" {
		t.Fatalf("corrupt file: mode = %q, want auto", st.Mode)
	}

	if err := os.WriteFile(path, []byte(`{"mode":"bogus"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if st := ReadRotationState(); st.Mode != "auto" {
		t.Fatalf("unknown mode value: got %q, want normalized to auto", st.Mode)
	}
}

func TestSetRotationModeRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if err := SetRotationMode("manual", "cli"); err != nil {
		t.Fatal(err)
	}
	st := ReadRotationState()
	if st.Mode != "manual" || st.SetBy != "cli" || st.SetAt == 0 {
		t.Fatalf("got %+v, want manual/cli/nonzero", st)
	}
	if got := RotationMode(); got != "manual" {
		t.Fatalf("RotationMode() = %q, want manual", got)
	}

	if err := SetRotationMode("bogus", "web"); err != nil {
		t.Fatal(err)
	}
	if got := RotationMode(); got != "auto" {
		t.Fatalf("RotationMode() after invalid mode = %q, want normalized to auto", got)
	}
}

// TestSetRotationModeNormalizesManualVariants covers H4: SetRotationMode's
// write side must normalize exactly like ReadRotationState's read side
// (strings.EqualFold on the trimmed value), storing the canonical lowercase
// "manual" regardless of the caller's casing/whitespace — Slice C feeds this
// straight from user input (CLI flag, web UI), so the two sides disagreeing
// would silently un-pin a manual mode set with, say, "Manual".
func TestSetRotationModeNormalizesManualVariants(t *testing.T) {
	for _, raw := range []string{"manual", "MANUAL", "Manual", " manual\n", "\tmanual "} {
		t.Run(fmt.Sprintf("%q", raw), func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			if err := SetRotationMode(raw, "cli"); err != nil {
				t.Fatal(err)
			}
			st := ReadRotationState()
			if st.Mode != "manual" {
				t.Errorf("stored mode = %q for input %q, want canonical manual", st.Mode, raw)
			}
			// The persisted bytes themselves must carry the canonical form,
			// not just what ReadRotationState's own normalization papers
			// over on the way back out.
			data, err := os.ReadFile(rotationStatePath())
			if err != nil {
				t.Fatal(err)
			}
			var onDisk struct {
				Mode string `json:"mode"`
			}
			if err := json.Unmarshal(data, &onDisk); err != nil {
				t.Fatal(err)
			}
			if onDisk.Mode != "manual" {
				t.Errorf("on-disk mode = %q for input %q, want canonical manual stored", onDisk.Mode, raw)
			}
		})
	}
}

// TestSetRotationModeNormalizesNonManualVariantsToAuto is TestSetRotationMode
// -NormalizesManualVariants' mirror: anything that ISN'T recognizably
// "manual" must be stored as the canonical "auto", not round-tripped as-is.
func TestSetRotationModeNormalizesNonManualVariantsToAuto(t *testing.T) {
	for _, raw := range []string{"auto", "AUTO", "", "bogus", "manually"} {
		t.Run(fmt.Sprintf("%q", raw), func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			if err := SetRotationMode(raw, "cli"); err != nil {
				t.Fatal(err)
			}
			st := ReadRotationState()
			if st.Mode != "auto" {
				t.Errorf("stored mode = %q for input %q, want canonical auto", st.Mode, raw)
			}
		})
	}
}

// --- ReadRotationState / RotationMode: fail-closed manual parsing (G6) ---
//
// A hand-edited mode value that's recognizably "manual" under any
// case/whitespace variation must still keep the pin — an exact-match
// comparison would silently normalize it to "auto" and let the gate resume
// auto-rotating despite the file saying manual.

func TestReadRotationStateManualModeParsingFailsClosed(t *testing.T) {
	cases := []string{"manual", "MANUAL", "Manual", " manual\n", "\tmanual "}
	for _, raw := range cases {
		// %q (not the raw string) as the subtest name: two of the whitespace
		// variants above (" manual\n" and "\tmanual ") both sanitize to the
		// same underscore-collapsed name under a bare t.Run(raw, ...), which
		// silently merges them into indistinguishable "#01"-suffixed
		// subtests in test output.
		t.Run(fmt.Sprintf("%q", raw), func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			path := rotationStatePath()
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			data := fmt.Sprintf(`{"mode":%q}`, raw)
			if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			if st := ReadRotationState(); st.Mode != "manual" {
				t.Errorf("ReadRotationState().Mode = %q for raw %q, want manual (fail closed)", st.Mode, raw)
			}
			if got := RotationMode(); got != "manual" {
				t.Errorf("RotationMode() = %q for raw %q, want manual (fail closed)", got, raw)
			}
		})
	}
}

func TestReadRotationStateNonManualStringsNormalizeToAuto(t *testing.T) {
	for _, raw := range []string{"auto", "AUTO", "", "bogus", "manually"} {
		t.Run(raw, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			path := rotationStatePath()
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(fmt.Sprintf(`{"mode":%q}`, raw)), 0o600); err != nil {
				t.Fatal(err)
			}
			if st := ReadRotationState(); st.Mode != "auto" {
				t.Errorf("ReadRotationState().Mode = %q for raw %q, want auto", st.Mode, raw)
			}
		})
	}
}

// --- notifyDeduped ---

func TestNotifyDedupedSuppressesSameKeyWithinWindow(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	orig := notifyFn
	t.Cleanup(func() { notifyFn = orig })
	var calls []notifyCall
	notifyFn = func(title, msg string) { calls = append(calls, notifyCall{title, msg}) }

	notifyDeduped("k1", "t1", "m1")
	notifyDeduped("k1", "t1", "m1-again")
	if len(calls) != 1 {
		t.Fatalf("same key within the window should suppress the repeat: got %d calls: %+v", len(calls), calls)
	}

	notifyDeduped("k2", "t2", "m2")
	if len(calls) != 2 {
		t.Fatalf("a different key should still notify: got %d calls: %+v", len(calls), calls)
	}
}

// TestNotifyDedupedPreservesConcurrentModeFlip covers G5: notifyFn is the
// (slow, real-life osascript) call — simulate a concurrent SetRotationMode
// (from `k account` or the web UI, neither guarded by rotation.lock) landing
// WHILE notifyFn is running, and confirm notifyDeduped's post-notify write
// doesn't clobber it back to whatever mode was in effect pre-notify.
func TestNotifyDedupedPreservesConcurrentModeFlip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	orig := notifyFn
	t.Cleanup(func() { notifyFn = orig })
	notifyFn = func(title, msg string) {
		if err := SetRotationMode("manual", "concurrent"); err != nil {
			t.Fatal(err)
		}
	}

	notifyDeduped("k1", "t1", "m1")

	st := ReadRotationState()
	if st.Mode != "manual" {
		t.Fatalf("mode = %q, want manual — notifyDeduped must not clobber a concurrent mode flip that landed mid-notify", st.Mode)
	}
	if st.LastNotice.Key != "k1" {
		t.Fatalf("last_notice.key = %q, want k1 — the notify's own effect must still land", st.LastNotice.Key)
	}
}

// TestNotifyDedupedFutureAtNotifiesAnyway covers G5's clock-rollback guard:
// a LastNotice.At in the future (a prior notify recorded before a clock
// rollback) must not suppress every future notify with the same key
// forever.
func TestNotifyDedupedFutureAtNotifiesAnyway(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	orig := notifyFn
	t.Cleanup(func() { notifyFn = orig })
	var calls []notifyCall
	notifyFn = func(title, msg string) { calls = append(calls, notifyCall{title, msg}) }

	future := time.Now().Add(time.Hour).UnixMilli()
	if err := WriteRotationState(RotationState{Mode: "auto", LastNotice: NoticeState{Key: "k1", At: future}}); err != nil {
		t.Fatal(err)
	}

	notifyDeduped("k1", "t1", "m1")

	if len(calls) != 1 {
		t.Fatalf("a future last_notice.at must not suppress forever: got %d calls", len(calls))
	}
}

// TestNotifyDedupedUnwritableStateLogsFailure covers G5: a WriteRotationState
// failure must be logged (dlogEvent), not silently discarded.
func TestNotifyDedupedUnwritableStateLogsFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	orig := notifyFn
	t.Cleanup(func() { notifyFn = orig })
	notifyFn = func(string, string) {}

	// Seed rotation.json as a DIRECTORY so AtomicWrite's rename-over-path
	// fails — the read side (ReadRotationState) still defensively degrades,
	// but the write can't succeed.
	path := rotationStatePath()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}

	before := countMatchingLogEntries(t, "persist last_notice failed")
	notifyDeduped("k1", "t1", "m1")
	after := countMatchingLogEntries(t, "persist last_notice failed")

	if after <= before {
		t.Fatal("expected a logged error event when WriteRotationState fails inside notifyDeduped")
	}
}

// countMatchingLogEntries scans every KRAKEN_LOGS_DIR log file (set by
// TestMain) for error-level entries whose message contains substr.
func countMatchingLogEntries(t *testing.T, substr string) int {
	t.Helper()
	n := 0
	for _, f := range util.ListLogFiles() {
		entries, err := util.ReadLogFile(f.Name)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.Level == "error" && strings.Contains(e.Msg, substr) {
				n++
			}
		}
	}
	return n
}

// --- acquireRotationLock: kernel flock semantics (G3) ---

func TestAcquireRotationLockSecondAcquireFailsWhileHeld(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	release, ok, err := acquireRotationLock()
	if !ok || err != nil {
		t.Fatalf("first acquire: ok=%v err=%v, want ok=true err=nil", ok, err)
	}
	defer release()

	_, ok2, err2 := acquireRotationLock()
	if ok2 || err2 != nil {
		t.Fatalf("second in-process acquire while held: ok=%v err=%v, want ok=false err=nil (held, not an error)", ok2, err2)
	}
}

func TestAcquireRotationLockSucceedsAfterRelease(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	release, ok, err := acquireRotationLock()
	if !ok || err != nil {
		t.Fatalf("first acquire: ok=%v err=%v", ok, err)
	}
	release()

	_, ok2, err2 := acquireRotationLock()
	if !ok2 || err2 != nil {
		t.Fatalf("acquire after release: ok=%v err=%v, want ok=true err=nil", ok2, err2)
	}
}

func TestAcquireRotationLockLeftoverFileFromDeadProcessDoesNotBlock(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := rotationLockPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	// A lock FILE with content but never actually flocked — as if a
	// process crashed (or the whole machine rebooted) leaving the file
	// behind without the kernel-held advisory lock that would come with a
	// live holder. Must not block a fresh acquire.
	if err := os.WriteFile(path, []byte("pid=99999 at=1"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, ok, err := acquireRotationLock()
	if !ok || err != nil {
		t.Fatalf("acquire over a leftover unflocked lock file: ok=%v err=%v, want ok=true err=nil", ok, err)
	}
}

func TestAcquireRotationLockCreateFailureReturnsError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// ~/.kraken as a plain FILE makes MkdirAll(dir) fail — the "couldn't
	// even create it" path, distinct from "held".
	if err := os.WriteFile(filepath.Join(home, ".kraken"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, ok, err := acquireRotationLock()
	if ok || err == nil {
		t.Fatalf("acquire with ~/.kraken as a plain file: ok=%v err=%v, want ok=false err!=nil", ok, err)
	}
}

// --- RotationGate ---

func TestRotationGateManualModeSkips(t *testing.T) {
	calls := stubRotation(t)
	if err := SetRotationMode("manual", "cli"); err != nil {
		t.Fatal(err)
	}
	fetchMainUsage = func() core.AccountUsage {
		t.Fatal("manual mode must skip before ever fetching usage")
		return core.AccountUsage{}
	}

	RotationGate("start x")

	if len(*calls) != 0 {
		t.Fatalf("manual mode should never notify: %+v", *calls)
	}
}

func TestRotationGateLockHeldSkips(t *testing.T) {
	stubRotation(t)
	// Actually hold the flock (not just drop a file at the path) — the gate
	// no longer treats a bare file as "held".
	release, ok, err := acquireRotationLock()
	if !ok || err != nil {
		t.Fatalf("acquireRotationLock: ok=%v err=%v", ok, err)
	}
	defer release()

	fetchCalled := false
	fetchMainUsage = func() core.AccountUsage { fetchCalled = true; return core.AccountUsage{} }

	RotationGate("start x")

	if fetchCalled {
		t.Fatal("gate proceeded despite the lock being held")
	}
}

func TestRotationGateLeftoverLockFileFromDeadProcessDoesNotBlock(t *testing.T) {
	calls := stubRotation(t)
	path := rotationLockPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("pid=99999 at=1"), 0o600); err != nil {
		t.Fatal(err)
	}

	fetchCalled := false
	fetchMainUsage = func() core.AccountUsage {
		fetchCalled = true
		return core.AccountUsage{Email: "main@x.com", Session: &core.UsageWindow{Percent: 10}}
	}

	RotationGate("start x")

	if !fetchCalled {
		t.Fatal("a leftover lock FILE from a dead process (never flocked) must not block the gate")
	}
	if len(*calls) != 0 {
		t.Fatalf("healthy main after acquiring the lock should not notify: %+v", *calls)
	}
}

func TestRotationGateFreshFetchFailedUsesFreshSnapshotFallback(t *testing.T) {
	calls := stubRotation(t)
	fetchMainUsage = func() core.AccountUsage { return core.AccountUsage{} } // no windows: "failed"

	snap := core.UsageSnapshot{
		FetchedAt: time.Now().UnixMilli(),
		Accounts: []core.AccountUsage{
			{Slug: "main", Email: "main@x.com", Session: &core.UsageWindow{Percent: 95}, FetchedAt: time.Now().UnixMilli()},
			{Slug: "slot-a", Email: "a@x.com", FetchedAt: time.Now().UnixMilli()},
		},
	}
	if err := writeUsageSnapshot(snap, UsageSnapshotPath()); err != nil {
		t.Fatal(err)
	}

	var gotEmail string
	selectTarget = func(accts []core.AccountUsage, mainEmail string, now int64) (string, string) {
		gotEmail = mainEmail
		return "slot-a", ""
	}
	swapMain = func(slug string) (string, string, error) { return "main@x.com", "a@x.com", nil }

	RotationGate("start x")

	if gotEmail != "main@x.com" {
		t.Fatalf("selectTarget mainEmail = %q, want main@x.com from the snapshot fallback row", gotEmail)
	}
	if len(*calls) != 1 || (*calls)[0].title != "kraken ⇄ rotated main" {
		t.Fatalf("expected a rotated-main notify, got %+v", *calls)
	}
}

// TestRotationGateFreshFetchFailedStaleMainRowSkips covers G2: freshness is
// keyed off the MAIN ROW's own FetchedAt, not the snapshot's — a row can
// carry forward days-old windows (mergeUsage's Stale-preserving merge)
// under a snapshot whose own FetchedAt looks perfectly fresh (mergeUsage
// re-stamps it on every pass regardless).
func TestRotationGateFreshFetchFailedStaleMainRowSkips(t *testing.T) {
	calls := stubRotation(t)
	fetchMainUsage = func() core.AccountUsage { return core.AccountUsage{} } // "failed"

	snap := core.UsageSnapshot{
		FetchedAt: time.Now().UnixMilli(), // snapshot-level stamp looks fresh...
		Accounts: []core.AccountUsage{
			// ...but this ROW's own FetchedAt is 48h old — carried forward by
			// mergeUsage across many passes of a failing fetch.
			{Slug: "main", Email: "main@x.com", Session: &core.UsageWindow{Percent: 95}, Stale: true,
				FetchedAt: time.Now().Add(-48 * time.Hour).UnixMilli()},
		},
	}
	if err := writeUsageSnapshot(snap, UsageSnapshotPath()); err != nil {
		t.Fatal(err)
	}

	selectCalled := false
	selectTarget = func([]core.AccountUsage, string, int64) (string, string) {
		selectCalled = true
		return "", ""
	}

	RotationGate("start x")

	if selectCalled {
		t.Fatal("a stale (by its own FetchedAt) carried-forward row must never stand in for a failed fresh fetch")
	}
	if len(*calls) != 0 {
		t.Fatalf("stale-data skip should not notify: %+v", *calls)
	}
}

func TestRotationGateFreshFetchFailedStaleSnapshotSkips(t *testing.T) {
	calls := stubRotation(t)
	fetchMainUsage = func() core.AccountUsage { return core.AccountUsage{} } // "failed"

	snap := core.UsageSnapshot{
		FetchedAt: time.Now().Add(-20 * time.Minute).UnixMilli(),
		Accounts: []core.AccountUsage{
			{Slug: "main", Email: "main@x.com", Session: &core.UsageWindow{Percent: 95},
				FetchedAt: time.Now().Add(-20 * time.Minute).UnixMilli()}, // stale (>=15min)
		},
	}
	if err := writeUsageSnapshot(snap, UsageSnapshotPath()); err != nil {
		t.Fatal(err)
	}

	selectCalled := false
	selectTarget = func([]core.AccountUsage, string, int64) (string, string) {
		selectCalled = true
		return "", ""
	}

	RotationGate("start x")

	if selectCalled {
		t.Fatal("a stale snapshot must never stand in for a failed fresh fetch")
	}
	if len(*calls) != 0 {
		t.Fatalf("stale-data skip should not notify: %+v", *calls)
	}
}

// TestRotationGateFreshFetchFailedZeroFetchedAtSkips covers G2's boundary:
// a main row with FetchedAt == 0 (never actually stamped) must be treated
// as absent, not as "unix epoch, therefore very stale but still a number".
func TestRotationGateFreshFetchFailedZeroFetchedAtSkips(t *testing.T) {
	calls := stubRotation(t)
	fetchMainUsage = func() core.AccountUsage { return core.AccountUsage{} }

	snap := core.UsageSnapshot{
		FetchedAt: time.Now().UnixMilli(),
		Accounts: []core.AccountUsage{
			{Slug: "main", Email: "main@x.com", Session: &core.UsageWindow{Percent: 95}, FetchedAt: 0},
		},
	}
	if err := writeUsageSnapshot(snap, UsageSnapshotPath()); err != nil {
		t.Fatal(err)
	}

	RotationGate("start x")

	if len(*calls) != 0 {
		t.Fatalf("a zero FetchedAt row must not be usable as a fallback: %+v", *calls)
	}
}

// TestRotationGateCandidatePoolFiltersStaleRows covers G2's second half: a
// pool row's own FetchedAt must gate whether SelectRotationTarget even sees
// it, so days-old slot data can never be picked (or wrongly excluded) as a
// rotation target.
func TestRotationGateCandidatePoolFiltersStaleRows(t *testing.T) {
	stubRotation(t)
	fetchMainUsage = func() core.AccountUsage {
		return core.AccountUsage{Email: "main@x.com", Session: &core.UsageWindow{Percent: 95}}
	}
	now := time.Now().UnixMilli()
	snap := core.UsageSnapshot{
		FetchedAt: now,
		Accounts: []core.AccountUsage{
			{Slug: "stale-slot", Email: "stale@x.com", Session: &core.UsageWindow{Percent: 5},
				FetchedAt: time.Now().Add(-48 * time.Hour).UnixMilli()},
			{Slug: "zero-fetched-slot", Email: "zero@x.com", Session: &core.UsageWindow{Percent: 5}, FetchedAt: 0},
			{Slug: "fresh-slot", Email: "fresh@x.com", Session: &core.UsageWindow{Percent: 5}, FetchedAt: now},
		},
	}
	if err := writeUsageSnapshot(snap, UsageSnapshotPath()); err != nil {
		t.Fatal(err)
	}

	var gotPool []core.AccountUsage
	selectTarget = func(accts []core.AccountUsage, mainEmail string, n int64) (string, string) {
		gotPool = accts
		return "", "stub"
	}

	RotationGate("start x")

	if len(gotPool) != 1 || gotPool[0].Slug != "fresh-slot" {
		t.Fatalf("selectTarget pool = %+v, want only fresh-slot (stale/zero-FetchedAt rows filtered)", gotPool)
	}
}

// TestRotationGateAllPoolRowsStaleReportsStaleNotNoPool covers H3: when
// EVERY non-main row got filtered out by the freshness bound, the pool
// handed to SelectRotationTarget would be empty either way SelectRotationTarget
// can't tell that apart from "no pool accounts" — RotationGate must say the
// real story (stale rows exist, just untrusted) directly, and must never
// call selectTarget with an empty slice for this reason (that would burn a
// call and produce a misleading "no pool accounts" exclusion reason).
func TestRotationGateAllPoolRowsStaleReportsStaleNotNoPool(t *testing.T) {
	calls := stubRotation(t)
	fetchMainUsage = func() core.AccountUsage {
		return core.AccountUsage{Email: "main@x.com", Session: &core.UsageWindow{Percent: 95}}
	}
	snap := core.UsageSnapshot{
		FetchedAt: time.Now().UnixMilli(),
		Accounts: []core.AccountUsage{
			{Slug: "stale-a", Email: "a@x.com", Session: &core.UsageWindow{Percent: 5},
				FetchedAt: time.Now().Add(-48 * time.Hour).UnixMilli()},
			{Slug: "stale-b", Email: "b@x.com", Session: &core.UsageWindow{Percent: 5}, FetchedAt: 0},
		},
	}
	if err := writeUsageSnapshot(snap, UsageSnapshotPath()); err != nil {
		t.Fatal(err)
	}
	selectCalled := false
	selectTarget = func([]core.AccountUsage, string, int64) (string, string) {
		selectCalled = true
		return "", "no pool accounts"
	}

	RotationGate("start x")

	if selectCalled {
		t.Fatal("selectTarget must not be called when every pool row was filtered as stale")
	}
	if len(*calls) != 1 {
		t.Fatalf("expected exactly one no-target notify, got %d: %+v", len(*calls), *calls)
	}
	if msg := (*calls)[0].msg; !strings.Contains(msg, "2 pool row(s) skipped as stale") ||
		!strings.Contains(msg, "no fresh candidate data") {
		t.Errorf("notify msg = %q, want it to name the 2 stale-skipped rows, not a bare no-pool-accounts reason", msg)
	}
}

// TestRotationGateSomeStaleRowsAppendedToNoCandidateReason covers H3's other
// half: when SOME rows survive the freshness filter but selectTarget still
// finds no eligible candidate among them, the stale-skipped count is
// appended to whatever reason selectTarget gave, rather than silently
// dropped.
func TestRotationGateSomeStaleRowsAppendedToNoCandidateReason(t *testing.T) {
	calls := stubRotation(t)
	fetchMainUsage = func() core.AccountUsage {
		return core.AccountUsage{Email: "main@x.com", Session: &core.UsageWindow{Percent: 95}}
	}
	now := time.Now().UnixMilli()
	snap := core.UsageSnapshot{
		FetchedAt: now,
		Accounts: []core.AccountUsage{
			{Slug: "stale-a", Email: "a@x.com", Session: &core.UsageWindow{Percent: 5},
				FetchedAt: time.Now().Add(-48 * time.Hour).UnixMilli()},
			{Slug: "fresh-a", Email: "c@x.com", Session: &core.UsageWindow{Percent: 5}, FetchedAt: now},
		},
	}
	if err := writeUsageSnapshot(snap, UsageSnapshotPath()); err != nil {
		t.Fatal(err)
	}
	selectTarget = func([]core.AccountUsage, string, int64) (string, string) {
		return "", "nobody eligible"
	}

	RotationGate("start x")

	if len(*calls) != 1 {
		t.Fatalf("expected exactly one no-target notify, got %d: %+v", len(*calls), *calls)
	}
	if msg := (*calls)[0].msg; !strings.Contains(msg, "nobody eligible") || !strings.Contains(msg, "1 skipped as stale") {
		t.Errorf("notify msg = %q, want both selectTarget's reason and the stale-skipped count", msg)
	}
}

func TestRotationGateHealthyMainNoOp(t *testing.T) {
	calls := stubRotation(t)
	fetchMainUsage = func() core.AccountUsage {
		return core.AccountUsage{Email: "main@x.com", Session: &core.UsageWindow{Percent: 10}}
	}
	selectCalled := false
	selectTarget = func([]core.AccountUsage, string, int64) (string, string) {
		selectCalled = true
		return "", ""
	}

	RotationGate("start x")

	if selectCalled {
		t.Fatal("a healthy main should never reach candidate selection")
	}
	if len(*calls) != 0 {
		t.Fatalf("a healthy main should not notify: %+v", *calls)
	}
	// The flock lock file persists on disk after release (release only
	// unlocks + closes the fd — it never removes the file, unlike the old
	// O_EXCL model), so assert the lock is actually free by successfully
	// re-acquiring it rather than checking the file is gone.
	release, ok, err := acquireRotationLock()
	if !ok || err != nil {
		t.Fatalf("lock should be released after a no-op gate run: ok=%v err=%v", ok, err)
	}
	release()
}

func TestRotationGateNoTargetDedupedNotify(t *testing.T) {
	calls := stubRotation(t)
	selectCalls := 0
	fetchMainUsage = func() core.AccountUsage {
		return core.AccountUsage{Email: "main@x.com", Session: &core.UsageWindow{Percent: 95}}
	}
	selectTarget = func([]core.AccountUsage, string, int64) (string, string) {
		selectCalls++
		return "", "nobody eligible"
	}

	RotationGate("start x")

	// Age LastGateAt out of the cooldown window (the pattern
	// TestRotationGateCooldownExpiredAllowsNextCall uses) so the second call
	// genuinely reaches the dedup logic below instead of exiting early at the
	// gate-level cooldown check — a naive "call it twice" here would pass for
	// the wrong reason, since the cooldown alone would already make the
	// second call a no-op without ever re-reaching notifyDeduped.
	st := ReadRotationState()
	st.LastGateAt = time.Now().Add(-2 * rotationGateCooldown).UnixMilli()
	if err := WriteRotationState(st); err != nil {
		t.Fatal(err)
	}

	RotationGate("start x") // second real gate run: same no-target key, must dedup the notify

	if selectCalls != 2 {
		t.Fatalf("selectTarget called %d times, want 2 — both calls must have reached the dedup logic", selectCalls)
	}
	if len(*calls) != 1 {
		t.Fatalf("expected exactly one deduped no-target notify, got %d: %+v", len(*calls), *calls)
	}
}

func TestRotationGateSwapSuccess(t *testing.T) {
	calls := stubRotation(t)
	fetchMainUsage = func() core.AccountUsage {
		return core.AccountUsage{Email: "main@x.com", Session: &core.UsageWindow{Percent: 95}}
	}
	selectTarget = func([]core.AccountUsage, string, int64) (string, string) { return "slot-a", "" }
	swapMain = func(string) (string, string, error) { return "main@x.com", "a@x.com", nil }
	passKicked := make(chan struct{})
	runUsagePass = func() error { close(passKicked); return nil }

	RotationGate("start x")

	select {
	case <-passKicked:
	case <-time.After(2 * time.Second):
		t.Fatal("expected a successful rotation to kick off a usage pass")
	}

	if len(*calls) != 1 || (*calls)[0].title != "kraken ⇄ rotated main" {
		t.Fatalf("notify calls = %+v, want one rotated-main notify", *calls)
	}
	st := ReadRotationState()
	if st.Mode != "auto" {
		t.Fatalf("mode = %q, want still auto after a successful rotation", st.Mode)
	}
	if st.LastNotice != (NoticeState{}) {
		t.Fatalf("last_notice = %+v, want untouched — a success notify isn't deduped", st.LastNotice)
	}
}

func TestRotationGateSwapFailureDedupedNotifyGateReturnsNormally(t *testing.T) {
	calls := stubRotation(t)
	selectCalls := 0
	fetchMainUsage = func() core.AccountUsage {
		return core.AccountUsage{Email: "main@x.com", Session: &core.UsageWindow{Percent: 95}}
	}
	selectTarget = func([]core.AccountUsage, string, int64) (string, string) {
		selectCalls++
		return "slot-a", ""
	}
	swapMain = func(string) (string, string, error) { return "", "", fmt.Errorf("boom") }

	// Neither call should block or panic — the gate degrades to a notify and
	// returns, letting the spawn it guards proceed either way.
	RotationGate("start x")

	// Age LastGateAt out of the cooldown window so the second call actually
	// re-runs the gate's real check (and so reaches notifyDeduped) instead of
	// exiting early at the cooldown short-circuit — see the identical note in
	// TestRotationGateNoTargetDedupedNotify.
	st := ReadRotationState()
	st.LastGateAt = time.Now().Add(-2 * rotationGateCooldown).UnixMilli()
	if err := WriteRotationState(st); err != nil {
		t.Fatal(err)
	}

	RotationGate("start x") // same swap-failure key: must dedup the notify

	if selectCalls != 2 {
		t.Fatalf("selectTarget called %d times, want 2 — both calls must have reached the swap/dedup logic", selectCalls)
	}
	if len(*calls) != 1 {
		t.Fatalf("expected exactly one deduped swap-failure notify, got %d: %+v", len(*calls), *calls)
	}
}

// --- RotationGate: swap-failure dedup key cause classification (G7) ---

func TestRotationGateSwapFailureDedupKeyClassifiesJournal(t *testing.T) {
	stubRotation(t)
	fetchMainUsage = func() core.AccountUsage {
		return core.AccountUsage{Email: "main@x.com", Session: &core.UsageWindow{Percent: 95}}
	}
	selectTarget = func([]core.AccountUsage, string, int64) (string, string) { return "slot-a", "" }
	swapMain = func(string) (string, string, error) {
		// H6: classification is via errors.Is(swapErr, core.ErrRotationInFlight),
		// not a "did not complete" substring match — the stub must actually
		// wrap the sentinel for the dedup key to classify as journal.
		return "", "", fmt.Errorf("previous rotation (old -> mid) did not complete; main login MAY be diverged: %w", core.ErrRotationInFlight)
	}

	RotationGate("start x")

	st := ReadRotationState()
	if st.LastNotice.Key != "rotation:swap-failed:journal" {
		t.Fatalf("last_notice.key = %q, want rotation:swap-failed:journal", st.LastNotice.Key)
	}
}

func TestRotationGateSwapFailureDedupKeyDefaultsToTarget(t *testing.T) {
	stubRotation(t)
	fetchMainUsage = func() core.AccountUsage {
		return core.AccountUsage{Email: "main@x.com", Session: &core.UsageWindow{Percent: 95}}
	}
	selectTarget = func([]core.AccountUsage, string, int64) (string, string) { return "slot-a", "" }
	swapMain = func(string) (string, string, error) { return "", "", fmt.Errorf("keychain locked") }

	RotationGate("start x")

	st := ReadRotationState()
	if st.LastNotice.Key != "rotation:swap-failed:target" {
		t.Fatalf("last_notice.key = %q, want rotation:swap-failed:target", st.LastNotice.Key)
	}
}

// --- RotationGate: gate-level cooldown (G4) ---

func TestRotationGateCooldownSkipsSecondCallWithinWindow(t *testing.T) {
	calls := stubRotation(t)
	fetchCount := 0
	fetchMainUsage = func() core.AccountUsage {
		fetchCount++
		return core.AccountUsage{Email: "main@x.com", Session: &core.UsageWindow{Percent: 10}}
	}

	RotationGate("start a")
	RotationGate("start b") // simulates a second spawn in the same daemon tick

	if fetchCount != 1 {
		t.Fatalf("fetchMainUsage called %d times, want exactly 1 — the second call should be cooldown-skipped", fetchCount)
	}
	if len(*calls) != 0 {
		t.Fatalf("cooldown skip should not notify: %+v", *calls)
	}
}

func TestRotationGateCooldownExpiredAllowsNextCall(t *testing.T) {
	stubRotation(t)
	fetchCount := 0
	fetchMainUsage = func() core.AccountUsage {
		fetchCount++
		return core.AccountUsage{Email: "main@x.com", Session: &core.UsageWindow{Percent: 10}}
	}

	RotationGate("start a")

	st := ReadRotationState()
	st.LastGateAt = time.Now().Add(-2 * rotationGateCooldown).UnixMilli()
	if err := WriteRotationState(st); err != nil {
		t.Fatal(err)
	}

	RotationGate("start b")

	if fetchCount != 2 {
		t.Fatalf("fetchMainUsage called %d times, want 2 once the cooldown window has passed", fetchCount)
	}
}

// TestRotationGateCooldownFutureLastGateAtDoesNotSuppressForever mirrors the
// same clock-rollback guard as notifyDeduped's future-At test, but for the
// gate cooldown.
func TestRotationGateCooldownFutureLastGateAtDoesNotSuppressForever(t *testing.T) {
	stubRotation(t)
	fetchCount := 0
	fetchMainUsage = func() core.AccountUsage {
		fetchCount++
		return core.AccountUsage{Email: "main@x.com", Session: &core.UsageWindow{Percent: 10}}
	}
	if err := WriteRotationState(RotationState{Mode: "auto", LastGateAt: time.Now().Add(time.Hour).UnixMilli()}); err != nil {
		t.Fatal(err)
	}

	RotationGate("start x")

	if fetchCount != 1 {
		t.Fatalf("a future last_gate_at (clock rollback) must not suppress the gate forever: fetchCount=%d", fetchCount)
	}
}

func TestRotationGateManualModeDoesNotConsumeCooldown(t *testing.T) {
	stubRotation(t)
	if err := SetRotationMode("manual", "cli"); err != nil {
		t.Fatal(err)
	}
	fetchCalled := false
	fetchMainUsage = func() core.AccountUsage { fetchCalled = true; return core.AccountUsage{} }

	RotationGate("start x")

	if fetchCalled {
		t.Fatal("manual mode must never fetch")
	}
	st := ReadRotationState()
	if st.LastGateAt != 0 {
		t.Fatalf("last_gate_at = %d, want untouched (0) — a manual-mode skip is not a completed gate run", st.LastGateAt)
	}
}

// --- RotationGate: corrupt rotation.json must not silently drop a pin (H5) ---

// TestRotationGateCorruptStateLogsButStillRuns covers H5: a present-but-
// unparseable rotation.json must not block the gate (fail-open stays — a
// corrupt file must never fail a spawn), but recordGateRun is about to
// rewrite over it with the auto default regardless of what was actually
// there (possibly a hand-pinned manual), so the gate must log loudly about
// the corruption rather than silently absorb it.
func TestRotationGateCorruptStateLogsButStillRuns(t *testing.T) {
	calls := stubRotation(t)
	fetchCalled := false
	fetchMainUsage = func() core.AccountUsage {
		fetchCalled = true
		return core.AccountUsage{Email: "main@x.com", Session: &core.UsageWindow{Percent: 10}}
	}

	path := rotationStatePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	before := countMatchingLogEntries(t, "rotation.json unreadable")
	RotationGate("start x")
	after := countMatchingLogEntries(t, "rotation.json unreadable")

	if !fetchCalled {
		t.Fatal("a corrupt rotation.json must fail OPEN — the gate should still run its real check, not skip it")
	}
	if after <= before {
		t.Fatal("expected a logged error event for the unreadable rotation.json")
	}
	if len(*calls) != 0 {
		t.Fatalf("a healthy main after recovering from a corrupt state should not notify: %+v", *calls)
	}
}

// --- RotationGate: panic recovery (G9) ---

func TestRotationGatePanicRecovered(t *testing.T) {
	stubRotation(t)
	fetchMainUsage = func() core.AccountUsage {
		panic("simulated panic mid-fetch")
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("RotationGate must recover its own panics, got: %v", r)
		}
	}()
	RotationGate("start x") // must not panic out of this test
}
