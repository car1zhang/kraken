package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"kraken/internal/core"
	"kraken/internal/jobs"
)

// stubAccountSwap isolates HOME to a fresh temp dir (so SetRotationMode/
// ReadRotationState never touch the real ~/.kraken/rotation.json) and swaps
// swapMainTo + kickUsagePass for recording/no-op stubs, restoring both on
// cleanup — mirrors rotation_test.go's stubRotation in package jobs.
func stubAccountSwap(t *testing.T, fn func(slug string) (from, to string, err error)) *int {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	origSwap, origKick := swapMainTo, kickUsagePass
	t.Cleanup(func() { swapMainTo, kickUsagePass = origSwap, origKick })
	swapMainTo = fn
	kicks := 0
	kickUsagePass = func() { kicks++ }
	return &kicks
}

func TestAccountSwitchMethodNotAllowed(t *testing.T) {
	w := httptest.NewRecorder()
	accountSwitchHandler(w, httptest.NewRequest(http.MethodGet, "/api/account/switch", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405, body = %s", w.Code, w.Body.String())
	}
}

func TestAccountSwitchEmptySlug400(t *testing.T) {
	stubAccountSwap(t, func(string) (string, string, error) {
		t.Fatal("swapMainTo should not be called for an empty slug")
		return "", "", nil
	})
	body := mustJSON(t, accountSwitchReq{Slug: ""})
	w := httptest.NewRecorder()
	accountSwitchHandler(w, httptest.NewRequest(http.MethodPost, "/api/account/switch", bytes.NewReader(body)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", w.Code, w.Body.String())
	}
}

func TestAccountSwitchBadJSON400(t *testing.T) {
	stubAccountSwap(t, func(string) (string, string, error) {
		t.Fatal("swapMainTo should not be called for bad json")
		return "", "", nil
	})
	w := httptest.NewRecorder()
	accountSwitchHandler(w, httptest.NewRequest(http.MethodPost, "/api/account/switch", bytes.NewReader([]byte("not json"))))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", w.Code, w.Body.String())
	}
}

// TestAccountSwitchOversizeBody400 covers P6: a body over the 1MB
// MaxBytesReader cap must be distinguished from ordinary malformed JSON —
// "request body too large", not the generic "bad json" — via
// errors.As(*http.MaxBytesError).
func TestAccountSwitchOversizeBody400(t *testing.T) {
	stubAccountSwap(t, func(string) (string, string, error) {
		t.Fatal("swapMainTo should not be called for an oversize body")
		return "", "", nil
	})
	oversize := append([]byte(`{"slug":"`), bytes.Repeat([]byte("a"), 1<<20)...)
	oversize = append(oversize, []byte(`"}`)...)
	w := httptest.NewRecorder()
	accountSwitchHandler(w, httptest.NewRequest(http.MethodPost, "/api/account/switch", bytes.NewReader(oversize)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["error"] != "request body too large" {
		t.Errorf(`error = %q, want "request body too large"`, resp["error"])
	}
}

func TestAccountSwitchHappyPathSetsManualMode(t *testing.T) {
	kicks := stubAccountSwap(t, func(slug string) (string, string, error) {
		if slug != "carl-work" {
			t.Errorf("swapMainTo slug = %q, want carl-work", slug)
		}
		return "old@example.com", "new@example.com", nil
	})

	body := mustJSON(t, accountSwitchReq{Slug: "carl-work"})
	w := httptest.NewRecorder()
	accountSwitchHandler(w, httptest.NewRequest(http.MethodPost, "/api/account/switch", bytes.NewReader(body)))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", w.Code, w.Body.String())
	}

	var resp accountSwitchResp
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.From != "old@example.com" || resp.To != "new@example.com" || resp.Mode != "manual" {
		t.Errorf("resp = %+v, want from=old@example.com to=new@example.com mode=manual", resp)
	}

	if got := jobs.RotationMode(); got != "manual" {
		t.Errorf("jobs.RotationMode() = %q, want manual", got)
	}
	st := jobs.ReadRotationState()
	if st.SetBy != "web" {
		t.Errorf("SetBy = %q, want web", st.SetBy)
	}
	if *kicks != 1 {
		t.Errorf("kickUsagePass calls = %d, want 1", *kicks)
	}
}

func TestAccountSwitchInFlight409(t *testing.T) {
	stubAccountSwap(t, func(string) (string, string, error) {
		return "", "", core.ErrRotationInFlight
	})
	body := mustJSON(t, accountSwitchReq{Slug: "carl-work"})
	w := httptest.NewRecorder()
	accountSwitchHandler(w, httptest.NewRequest(http.MethodPost, "/api/account/switch", bytes.NewReader(body)))
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body = %s", w.Code, w.Body.String())
	}
	if got := jobs.RotationMode(); got != "auto" {
		t.Errorf("jobs.RotationMode() = %q, want auto (untouched)", got)
	}
}

func TestAccountSwitchOtherError500LeavesModeUntouched(t *testing.T) {
	stubAccountSwap(t, func(string) (string, string, error) {
		return "", "", errBoom
	})
	body := mustJSON(t, accountSwitchReq{Slug: "carl-work"})
	w := httptest.NewRecorder()
	accountSwitchHandler(w, httptest.NewRequest(http.MethodPost, "/api/account/switch", bytes.NewReader(body)))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body = %s", w.Code, w.Body.String())
	}
	if got := jobs.RotationMode(); got != "auto" {
		t.Errorf("jobs.RotationMode() = %q, want auto (untouched)", got)
	}
}

// errBoom is a plain sentinel for the swap-failure tests above — distinct
// from core.ErrRotationInFlight so the 409-vs-500 branch is exercised on
// both sides.
var errBoom = &accountTestErr{"boom"}

type accountTestErr struct{ msg string }

func (e *accountTestErr) Error() string { return e.msg }

// TestAccountSwitchSaveBackIsTarget409 covers K3(c): core.ErrSaveBackIsTarget
// (the main-mirror slot refusing to also be the rotation target) is a
// conflict, not a bare server error — same 409 treatment as
// ErrRotationInFlight.
func TestAccountSwitchSaveBackIsTarget409(t *testing.T) {
	stubAccountSwap(t, func(string) (string, string, error) {
		return "", "", core.ErrSaveBackIsTarget
	})
	body := mustJSON(t, accountSwitchReq{Slug: "carl-work"})
	w := httptest.NewRecorder()
	accountSwitchHandler(w, httptest.NewRequest(http.MethodPost, "/api/account/switch", bytes.NewReader(body)))
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body = %s", w.Code, w.Body.String())
	}
}

// TestAccountSwitchInvalidSlug400 covers K4: a bad/unsafe slug is a client
// error, not a bare server error.
func TestAccountSwitchInvalidSlug400(t *testing.T) {
	stubAccountSwap(t, func(string) (string, string, error) {
		return "", "", core.ErrInvalidSlug
	})
	body := mustJSON(t, accountSwitchReq{Slug: "carl-work"})
	w := httptest.NewRecorder()
	accountSwitchHandler(w, httptest.NewRequest(http.MethodPost, "/api/account/switch", bytes.NewReader(body)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", w.Code, w.Body.String())
	}
}

// TestAccountSwitchLockHeldConflict409 covers K2: a manual web switch must
// take the same cross-process rotation.lock RotationGate itself takes,
// refusing (409) rather than racing a swap already in flight elsewhere. The
// lock is held directly via the real jobs.AcquireRotationLock (the same
// seam the handler itself calls, deliberately not stubbed — see
// acquireRotationLock's doc comment above) to simulate cross-process
// contention.
func TestAccountSwitchLockHeldConflict409(t *testing.T) {
	stubAccountSwap(t, func(string) (string, string, error) {
		t.Fatal("swapMainTo should not be called while the rotation lock is held elsewhere")
		return "", "", nil
	})
	release, ok, err := jobs.AcquireRotationLock()
	if !ok || err != nil {
		t.Fatalf("acquire rotation lock: ok=%v err=%v", ok, err)
	}
	defer release()

	body := mustJSON(t, accountSwitchReq{Slug: "carl-work"})
	w := httptest.NewRecorder()
	accountSwitchHandler(w, httptest.NewRequest(http.MethodPost, "/api/account/switch", bytes.NewReader(body)))
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body = %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["error"] != "rotation in flight" {
		t.Errorf(`error = %q, want "rotation in flight"`, resp["error"])
	}
}

// TestAccountSwitchModeWriteFailureReportsTruthfulMode covers K5: when the
// swap succeeds but the manual-mode pin fails to persist, the response must
// report the actually-in-effect mode (read back from disk), not a hardcoded
// "manual" the write never actually achieved.
func TestAccountSwitchModeWriteFailureReportsTruthfulMode(t *testing.T) {
	stubAccountSwap(t, func(string) (string, string, error) {
		return "old@example.com", "new@example.com", nil
	})
	// Seed rotation.json as a DIRECTORY so SetRotationMode's AtomicWrite
	// rename-over-path fails — the swap itself still committed.
	path := filepath.Join(os.Getenv("HOME"), ".kraken", "rotation.json")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}

	body := mustJSON(t, accountSwitchReq{Slug: "carl-work"})
	w := httptest.NewRecorder()
	accountSwitchHandler(w, httptest.NewRequest(http.MethodPost, "/api/account/switch", bytes.NewReader(body)))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (the swap itself succeeded), body = %s", w.Code, w.Body.String())
	}
	var resp accountSwitchResp
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Mode != "auto" {
		t.Errorf("resp.Mode = %q, want auto (truthful read-back after a failed mode write, not a hardcoded manual)", resp.Mode)
	}
}

func TestAccountModeMethodNotAllowed(t *testing.T) {
	w := httptest.NewRecorder()
	accountModeHandler(w, httptest.NewRequest(http.MethodGet, "/api/account/mode", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405, body = %s", w.Code, w.Body.String())
	}
}

func TestAccountModeInvalidValue400(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	body := mustJSON(t, accountModeReq{Mode: "sometimes"})
	w := httptest.NewRecorder()
	accountModeHandler(w, httptest.NewRequest(http.MethodPost, "/api/account/mode", bytes.NewReader(body)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", w.Code, w.Body.String())
	}
}

// TestAccountModeOversizeBody400 mirrors TestAccountSwitchOversizeBody400
// for the sibling handler, which decodes through the same MaxBytesReader +
// errors.As(*http.MaxBytesError) distinction.
func TestAccountModeOversizeBody400(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	oversize := append([]byte(`{"mode":"`), bytes.Repeat([]byte("a"), 1<<20)...)
	oversize = append(oversize, []byte(`"}`)...)
	w := httptest.NewRecorder()
	accountModeHandler(w, httptest.NewRequest(http.MethodPost, "/api/account/mode", bytes.NewReader(oversize)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["error"] != "request body too large" {
		t.Errorf(`error = %q, want "request body too large"`, resp["error"])
	}
}

func TestAccountModeRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	body := mustJSON(t, accountModeReq{Mode: "manual"})
	w := httptest.NewRecorder()
	accountModeHandler(w, httptest.NewRequest(http.MethodPost, "/api/account/mode", bytes.NewReader(body)))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", w.Code, w.Body.String())
	}
	var resp accountModeResp
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Mode != "manual" {
		t.Errorf("mode = %q, want manual", resp.Mode)
	}
	if got := jobs.RotationMode(); got != "manual" {
		t.Errorf("jobs.RotationMode() = %q, want manual", got)
	}

	// manual here pins WITHOUT swapping main — no SwapMainTo call should be
	// necessary to reach this state.
	body = mustJSON(t, accountModeReq{Mode: "auto"})
	w = httptest.NewRecorder()
	accountModeHandler(w, httptest.NewRequest(http.MethodPost, "/api/account/mode", bytes.NewReader(body)))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Mode != "auto" {
		t.Errorf("mode = %q, want auto", resp.Mode)
	}
	if got := jobs.RotationMode(); got != "auto" {
		t.Errorf("jobs.RotationMode() = %q, want auto", got)
	}
}
