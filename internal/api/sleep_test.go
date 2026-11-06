package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// sleep_test.go — /api/sleep (sleep.go). Both core calls are behind seams,
// so nothing here runs pmset, sudo or caffeinate.

// sleepSwap replaces both seams. The fake machine starts in `disabled`; a
// set that returns no error moves it, and every value handed to the setter
// is recorded.
type sleepSwap struct {
	disabled bool
	sets     []bool
	setErr   error
	readErr  error
}

func newSleepSwap(t *testing.T, disabled bool) *sleepSwap {
	t.Helper()
	s := &sleepSwap{disabled: disabled}
	oldGet, oldSet := sleepDisabledFunc, setSleepDisabledFunc
	t.Cleanup(func() { sleepDisabledFunc, setSleepDisabledFunc = oldGet, oldSet })
	sleepDisabledFunc = func() (bool, error) { return s.disabled, s.readErr }
	setSleepDisabledFunc = func(v bool) error {
		s.sets = append(s.sets, v)
		if s.setErr != nil {
			return s.setErr
		}
		s.disabled = v
		return nil
	}
	return s
}

func sleepDo(t *testing.T, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "/api/sleep", strings.NewReader(body))
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	req.RemoteAddr = "127.0.0.1:5000"
	rec := httptest.NewRecorder()
	sleepHandler(rec, req)
	return rec
}

// sleepBody decodes a response body as a JSON object.
func sleepBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("body %q is not a JSON object: %v", rec.Body.String(), err)
	}
	return m
}

func TestSleepGetReportsTheState(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		newSleepSwap(t, disabled)
		rec := sleepDo(t, http.MethodGet, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("disabled=%v: status = %d, body %s", disabled, rec.Code, rec.Body.String())
		}
		assertShape(t, "/api/sleep GET", rec.Body.Bytes(), sleepSpec)
		if got := sleepBody(t, rec)["disabled"]; got != disabled {
			t.Errorf("disabled = %v, want %v", got, disabled)
		}
	}
}

func TestSleepGetFailureIsAnError(t *testing.T) {
	s := newSleepSwap(t, false)
	s.readErr = errors.New("pmset -g exited 1")
	rec := sleepDo(t, http.MethodGet, "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if got, _ := sleepBody(t, rec)["error"].(string); !strings.Contains(got, "pmset") {
		t.Errorf("error = %q, want the read's own reason", got)
	}
}

func TestSleepPostSetsTheNamedState(t *testing.T) {
	s := newSleepSwap(t, false)
	rec := sleepDo(t, http.MethodPost, `{"disabled": true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "/api/sleep POST", rec.Body.Bytes(), sleepSpec)
	if got := sleepBody(t, rec)["disabled"]; got != true {
		t.Errorf("disabled = %v, want true", got)
	}
	// and back; the body names a state, so repeating it is not a flip
	sleepDo(t, http.MethodPost, `{"disabled": false}`)
	sleepDo(t, http.MethodPost, `{"disabled": false}`)
	if want := []bool{true, false, false}; len(s.sets) != 3 || s.sets[0] != want[0] || s.sets[1] != want[1] || s.sets[2] != want[2] {
		t.Errorf("setter saw %v, want %v", s.sets, want)
	}
	if s.disabled {
		t.Error("the machine is still disabled after two requests to enable sleep")
	}
}

// The answer is what the machine reports after the write, not the request.
func TestSleepPostAnswersWithTheReadBack(t *testing.T) {
	s := newSleepSwap(t, false)
	setSleepDisabledFunc = func(v bool) error {
		s.sets = append(s.sets, v)
		return nil // reports success and leaves the state where it was
	}
	rec := sleepDo(t, http.MethodPost, `{"disabled": true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if got := sleepBody(t, rec)["disabled"]; got != false {
		t.Errorf("disabled = %v, want the read-back false", got)
	}
}

func TestSleepPostRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       int
	}{
		{"no key", `{}`, http.StatusBadRequest},
		{"null", `{"disabled": null}`, http.StatusBadRequest},
		{"unknown key", `{"disabled": true, "toggle": true}`, http.StatusBadRequest},
		{"misspelled key only", `{"disable": true}`, http.StatusBadRequest},
		{"not a bool", `{"disabled": "yes"}`, http.StatusBadRequest},
		{"malformed", `{"disabled":`, http.StatusBadRequest},
		{"empty body", ``, http.StatusBadRequest},
	} {
		s := newSleepSwap(t, true)
		rec := sleepDo(t, http.MethodPost, tc.body)
		if rec.Code != tc.want {
			t.Errorf("%s: status = %d, want %d (body %s)", tc.name, rec.Code, tc.want, rec.Body.String())
		}
		if len(s.sets) != 0 || !s.disabled {
			t.Errorf("%s: a refused body reached the setter (%v) or changed the state", tc.name, s.sets)
		}
	}
}

func TestSleepPostSetFailureIsA409WithTheReason(t *testing.T) {
	s := newSleepSwap(t, false)
	s.setErr = errors.New("pmset needs passwordless sudo")
	rec := sleepDo(t, http.MethodPost, `{"disabled": true}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	if got, _ := sleepBody(t, rec)["error"].(string); got != "pmset needs passwordless sudo" {
		t.Errorf("error = %q, want the setter's own reason", got)
	}
}

func TestSleepPostReadBackFailureIsAnError(t *testing.T) {
	s := newSleepSwap(t, false)
	s.readErr = errors.New("pmset -g exited 1")
	if rec := sleepDo(t, http.MethodPost, `{"disabled": true}`); rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestSleepOtherMethodsRefused(t *testing.T) {
	s := newSleepSwap(t, false)
	for _, m := range []string{http.MethodPut, http.MethodDelete, http.MethodPatch} {
		if rec := sleepDo(t, m, `{"disabled": true}`); rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s = %d, want 405", m, rec.Code)
		}
	}
	if len(s.sets) != 0 {
		t.Errorf("a refused method reached the setter: %v", s.sets)
	}
}

// A mirror may show the state and must not change the machine's.
func TestSleepMirrorReadsButNeverWrites(t *testing.T) {
	s := newSleepSwap(t, true)
	SetMirrorMode(true)
	t.Cleanup(func() { SetMirrorMode(false) })
	if rec := sleepDo(t, http.MethodGet, ""); rec.Code != http.StatusOK {
		t.Errorf("mirror GET = %d, want 200", rec.Code)
	}
	if rec := sleepDo(t, http.MethodPost, `{"disabled": false}`); rec.Code != http.StatusForbidden {
		t.Errorf("mirror POST = %d, want 403", rec.Code)
	}
	if len(s.sets) != 0 || !s.disabled {
		t.Errorf("a mirror POST reached the setter: %v", s.sets)
	}
}

// Registered, and at the tier every other board control sits at: a paired
// device may use it, a bare tailnet node may not.
func TestSleepRouteIsRegisteredAtTheBoardsTier(t *testing.T) {
	found := false
	for _, p := range RoutePatterns() {
		if p == "/api/sleep" {
			found = true
		}
	}
	if !found {
		t.Fatal("/api/sleep is not registered")
	}
	if got := policyFor("/api/sleep"); got != TierPaired {
		t.Errorf("/api/sleep tier = %v, want paired (%v)", got, TierPaired)
	}
}
