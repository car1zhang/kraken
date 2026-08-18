package jobs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kraken/internal/util"
)

// rotation_test.go — the mode store (ReadRotationState/WriteRotationState/
// SetRotationMode), the notify-dedup helper and the flock-based rotation
// lock, all exercised via the package-level swappable seams that survive
// (notifyFn, runUsagePass) so nothing here touches the network, keychain, or
// osascript.
//
// RotationGate's own decision matrix used to live below and went with it
// (Q2): no spawn path swaps the machine's login any more, and what replaced
// it — the per-cell binder — is binding.go's. What survives here is what
// survived the retirement: the mode a manual pin is stored in, the
// notification dedup both the binder and the limit rescue use, and the lock
// the two remaining manual swap callers take.

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

	origNotify, origPass := notifyFn, runUsagePass
	t.Cleanup(func() { notifyFn, runUsagePass = origNotify, origPass })

	var calls []notifyCall
	notifyFn = func(title, msg string) { calls = append(calls, notifyCall{title, msg}) }
	// Default to a no-op: nothing in this package kicks a usage pass any
	// more, but a test that reaches KickUsagePass directly would otherwise
	// fire a real background network fetch (and race the usagePassRunning
	// flag against whichever test runs next).
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
// comparison would silently normalize it to "auto" and let kraken resume
// taking account decisions despite the file saying manual.

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
