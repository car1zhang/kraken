package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kraken/internal/jobs"
	"kraken/internal/store"
)

// periodic_edge_test.go — athena-edge's blind suite for slice 3 of the
// periodic-jobs contract (50-contract.md, "Slice 3: the route"): every 400,
// 403, 404, 405 and 409 the route promises, the `{"error": …}` shape of each
// JSON refusal, the mirror gate writing nothing, never-null `days`, and the
// run op's 200 on a refused start. Written from the contract only.
//
// The run op goes through periodicRunFunc, replaced here by a stub that
// answers the way the contract says RunPeriodicNow answers (ErrPeriodicBuiltin
// for dream, store.ErrNotFound for a missing row), so no test starts a cell.

type periodicEdgeRunStub struct {
	calls []string
	run   jobs.PeriodicRun
}

// periodicEdgeMux wires a scratch store and repos dir, mirror off, and the
// run seam stubbed.
func periodicEdgeMux(t *testing.T) (*http.ServeMux, *store.Store, *periodicEdgeRunStub) {
	t.Helper()
	SetMirrorMode(false)
	t.Cleanup(func() { SetMirrorMode(false) })
	st := useTestStore(t)
	repos := filepath.Join(t.TempDir(), "Developer")
	if err := os.MkdirAll(filepath.Join(repos, "kraken", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KRAKEN_REPOS_DIR", repos)

	stub := &periodicEdgeRunStub{run: jobs.PeriodicRun{Slug: "stub-run", Started: true}}
	prev := periodicRunFunc
	periodicRunFunc = func(name string, now time.Time) (jobs.PeriodicRun, error) {
		stub.calls = append(stub.calls, name)
		if name == jobs.PeriodicBuiltinDream {
			return jobs.PeriodicRun{}, jobs.ErrPeriodicBuiltin
		}
		if _, err := st.GetPeriodicJob(name); err != nil {
			return jobs.PeriodicRun{}, err
		}
		return stub.run, nil
	}
	t.Cleanup(func() { periodicRunFunc = prev })

	mux := http.NewServeMux()
	Register(mux)
	return mux, st, stub
}

func periodicEdgeDo(t *testing.T, mux http.Handler, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, "/api/periodic", nil)
	} else {
		req = httptest.NewRequest(method, "/api/periodic", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func periodicEdgePost(t *testing.T, mux http.Handler, v map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return periodicEdgeDo(t, mux, http.MethodPost, string(b))
}

// periodicEdgeWantError asserts a status and, per the contract's "Every
// non-200 JSON answer is `{\"error\": string}` with a non-empty message", the
// body's shape.
func periodicEdgeWantError(t *testing.T, what string, rec *httptest.ResponseRecorder, status int) {
	t.Helper()
	if rec.Code != status {
		t.Errorf("%s: status %d (%s), want %d", what, rec.Code, rec.Body.String(), status)
		return
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Errorf("%s: body %q is not JSON: %v", what, rec.Body.String(), err)
		return
	}
	msg, ok := body["error"].(string)
	if !ok || strings.TrimSpace(msg) == "" {
		t.Errorf("%s: body %s, want {\"error\": non-empty string}", what, rec.Body.String())
	}
}

func periodicEdgeSeed(t *testing.T, st *store.Store, name, prefix string, enabled bool, days int) {
	t.Helper()
	p := &store.PeriodicJob{
		Name: name, Prefix: prefix, Template: "---\nrepo: kraken\n---\nbody\n",
		AtMinute: 540, Days: days, Enabled: enabled, ArmedAt: 1, CreatedAt: 1,
	}
	if _, err := st.CreatePeriodicJob(p); err != nil {
		t.Fatalf("seed %q: %v", name, err)
	}
}

func periodicEdgeRows(t *testing.T, st *store.Store) []store.PeriodicJob {
	t.Helper()
	rows, err := st.ListPeriodicJobs()
	if err != nil {
		t.Fatalf("ListPeriodicJobs: %v", err)
	}
	return rows
}

// Contract: "other methods | 405."
func TestPeriodicEdgeOtherMethods(t *testing.T) {
	mux, _, _ := periodicEdgeMux(t)
	for _, m := range []string{http.MethodPut, http.MethodDelete, http.MethodPatch} {
		if rec := periodicEdgeDo(t, mux, m, ""); rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: %d %s, want 405", m, rec.Code, rec.Body.String())
		}
	}
}

// Contract: "An unknown key, or malformed JSON, is 400."
func TestPeriodicEdgeBadBodies(t *testing.T) {
	mux, st, stub := periodicEdgeMux(t)
	for _, body := range []string{
		`{`,
		`not json`,
		`{"op":"pause","name":"x",}`,
		`{"op":"add","name":"n","prefix":"p","at":"09:00","days":"","template":"t","extra":"1"}`,
		`{"op":"run","name":"x","slug":"y"}`,
		`{"op":"pause","name":"x","Enabled":"true"}`,
	} {
		periodicEdgeWantError(t, "body "+body, periodicEdgeDo(t, mux, http.MethodPost, body), http.StatusBadRequest)
	}
	if rows := periodicEdgeRows(t, st); len(rows) != 0 || len(stub.calls) != 0 {
		t.Fatalf("bad bodies wrote %d rows / %d runs", len(rows), len(stub.calls))
	}
}

// Contract: "A missing or blank `name` is 400" for any op.
func TestPeriodicEdgeMissingName(t *testing.T) {
	mux, st, stub := periodicEdgeMux(t)
	for _, op := range []string{"add", "pause", "resume", "run", "remove"} {
		periodicEdgeWantError(t, op+" without name", periodicEdgePost(t, mux, map[string]string{"op": op}), http.StatusBadRequest)
		periodicEdgeWantError(t, op+" with empty name", periodicEdgePost(t, mux, map[string]string{"op": op, "name": ""}), http.StatusBadRequest)
		periodicEdgeWantError(t, op+" with blank name", periodicEdgePost(t, mux, map[string]string{
			"op": op, "name": "  \t ", "prefix": "nightly", "at": "09:00", "days": "", "template": "---\nrepo: kraken\n---\nbody\n",
		}), http.StatusBadRequest)
	}
	if rows := periodicEdgeRows(t, st); len(rows) != 0 || len(stub.calls) != 0 {
		t.Fatalf("nameless requests wrote %d rows / %d runs", len(rows), len(stub.calls))
	}
}

// Contract: "An unknown `op` is 400." The CLI verb `rm`, a case variant and an
// absent op are not ops of the route.
func TestPeriodicEdgeUnknownOp(t *testing.T) {
	mux, st, stub := periodicEdgeMux(t)
	periodicEdgeSeed(t, st, "nightly", "nightly", true, 0)
	for _, op := range []string{"", "bogus", "rm", "delete", "PAUSE", "Run", "list"} {
		periodicEdgeWantError(t, "op "+op, periodicEdgePost(t, mux, map[string]string{"op": op, "name": "nightly"}), http.StatusBadRequest)
	}
	periodicEdgeWantError(t, "no op key", periodicEdgePost(t, mux, map[string]string{"name": "nightly"}), http.StatusBadRequest)
	rows := periodicEdgeRows(t, st)
	if len(rows) != 1 || !rows[0].Enabled || len(stub.calls) != 0 {
		t.Fatalf("unknown ops changed state: rows %+v, runs %d", rows, len(stub.calls))
	}
}

// Contract: "A name no row holds is 404 (for `pause`, `resume`, `run`,
// `remove`)." Names are matched exactly, so a case variant is no row.
func TestPeriodicEdgeUnknownName404(t *testing.T) {
	mux, st, _ := periodicEdgeMux(t)
	periodicEdgeSeed(t, st, "nightly", "nightly", true, 0)
	for _, op := range []string{"pause", "resume", "run", "remove"} {
		for _, name := range []string{"ghost", "Nightly"} {
			periodicEdgeWantError(t, op+" "+name, periodicEdgePost(t, mux, map[string]string{"op": op, "name": name}), http.StatusNotFound)
		}
	}
	if rows := periodicEdgeRows(t, st); len(rows) != 1 || !rows[0].Enabled {
		t.Fatalf("404 requests changed the real row: %+v", rows)
	}
}

// Contract (amended in revision 2): "The name `dream` on `run` or `remove` is
// 409." Neither creates the dream row.
func TestPeriodicEdgeDream409(t *testing.T) {
	mux, st, _ := periodicEdgeMux(t)
	for _, op := range []string{"run", "remove"} {
		periodicEdgeWantError(t, op+" dream", periodicEdgePost(t, mux, map[string]string{"op": op, "name": "dream"}), http.StatusConflict)
	}
	if rows := periodicEdgeRows(t, st); len(rows) != 0 {
		t.Fatalf("dream requests stored rows: %+v", rows)
	}
}

// Contract (revision 2): pause and resume on `dream` answer 200 with the
// dream row, `builtin` true, and the row is stored with the state asked for.
func TestPeriodicEdgeDreamPauseResume(t *testing.T) {
	mux, st, _ := periodicEdgeMux(t)
	for _, tc := range []struct {
		op      string
		enabled bool
	}{{"pause", false}, {"resume", true}} {
		rec := periodicEdgePost(t, mux, map[string]string{"op": tc.op, "name": "dream"})
		if rec.Code != http.StatusOK {
			t.Fatalf("%s dream: status %d (%s)", tc.op, rec.Code, rec.Body.String())
		}
		var got struct {
			Periodic struct {
				Name    string `json:"name"`
				Builtin bool   `json:"builtin"`
				Enabled bool   `json:"enabled"`
			} `json:"periodic"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("%s dream: decode: %v", tc.op, err)
		}
		if got.Periodic.Name != "dream" || !got.Periodic.Builtin || got.Periodic.Enabled != tc.enabled {
			t.Errorf("%s dream answered %+v, want dream, builtin, enabled %v", tc.op, got.Periodic, tc.enabled)
		}
		rows := periodicEdgeRows(t, st)
		if len(rows) != 1 || rows[0].Name != "dream" || rows[0].Enabled != tc.enabled {
			t.Errorf("%s dream stored %+v, want one dream row with enabled %v", tc.op, rows, tc.enabled)
		}
	}
}

// Contract: op add — "An `ErrPeriodicInvalid` error is 400;
// `store.ErrPeriodicTaken` is 409." The name `dream` on add is an AddPeriodic
// refusal ("the name `dream` is refused", wrapping ErrPeriodicInvalid), so 400.
func TestPeriodicEdgeAddRefusals(t *testing.T) {
	mux, st, _ := periodicEdgeMux(t)
	tmpl := "---\nrepo: kraken\n---\nbody\n"
	add := func(name, prefix, at, days, template string) *httptest.ResponseRecorder {
		return periodicEdgePost(t, mux, map[string]string{"op": "add", "name": name, "prefix": prefix, "at": at, "days": days, "template": template})
	}
	periodicEdgeWantError(t, "bad at", add("nightly", "nightly", "24:00", "", tmpl), http.StatusBadRequest)
	periodicEdgeWantError(t, "bad days", add("nightly", "nightly", "09:00", "mon,", tmpl), http.StatusBadRequest)
	periodicEdgeWantError(t, "bad prefix", add("nightly", "Night Ly", "09:00", "", tmpl), http.StatusBadRequest)
	periodicEdgeWantError(t, "reserved prefix", add("nightly", "dream", "09:00", "", tmpl), http.StatusBadRequest)
	periodicEdgeWantError(t, "name dream", add("dream", "nightly", "09:00", "", tmpl), http.StatusBadRequest)
	periodicEdgeWantError(t, "template with state", add("nightly", "nightly", "09:00", "", "---\nrepo: kraken\nstate: done\n---\nbody\n"), http.StatusBadRequest)
	periodicEdgeWantError(t, "template with no repo", add("nightly", "nightly", "09:00", "", "---\n---\nbody\n"), http.StatusBadRequest)
	periodicEdgeWantError(t, "missing template", add("nightly", "nightly", "09:00", "", ""), http.StatusBadRequest)
	if rows := periodicEdgeRows(t, st); len(rows) != 0 {
		t.Fatalf("refused adds stored rows: %+v", rows)
	}

	if rec := add("nightly", "nightly", "09:00", "", tmpl); rec.Code != http.StatusOK {
		t.Fatalf("valid add: %d %s", rec.Code, rec.Body.String())
	}
	periodicEdgeWantError(t, "name taken", add("nightly", "other", "09:00", "", tmpl), http.StatusConflict)
	periodicEdgeWantError(t, "prefix taken", add("another", "nightly", "09:00", "", tmpl), http.StatusConflict)
	if rows := periodicEdgeRows(t, st); len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
}

// Contract: "In mirror mode every POST is 403 with the plain-text body
// `disabled in mirror mode`, and nothing is written." — valid and invalid
// requests alike. "GET /api/periodic ... Served in mirror mode too."
func TestPeriodicEdgeMirrorGate(t *testing.T) {
	mux, st, stub := periodicEdgeMux(t)
	periodicEdgeSeed(t, st, "nightly", "nightly", true, 0)
	SetMirrorMode(true)
	for _, body := range []map[string]string{
		{"op": "add", "name": "weekly", "prefix": "weekly", "at": "09:00", "days": "mon", "template": "---\nrepo: kraken\n---\nbody\n"},
		{"op": "pause", "name": "nightly"},
		{"op": "resume", "name": "nightly"},
		{"op": "run", "name": "nightly"},
		{"op": "remove", "name": "nightly"},
		{"op": "bogus", "name": "nightly"},
	} {
		rec := periodicEdgePost(t, mux, body)
		if rec.Code != http.StatusForbidden {
			t.Errorf("mirror %v: %d %s, want 403", body, rec.Code, rec.Body.String())
			continue
		}
		if got := strings.TrimSpace(rec.Body.String()); got != "disabled in mirror mode" {
			t.Errorf("mirror %v: body %q, want %q", body, got, "disabled in mirror mode")
		}
	}
	if rec := periodicEdgeDo(t, mux, http.MethodGet, ""); rec.Code != http.StatusOK {
		t.Errorf("mirror GET: %d %s, want 200", rec.Code, rec.Body.String())
	}
	SetMirrorMode(false)
	rows := periodicEdgeRows(t, st)
	if len(rows) != 1 || rows[0].Name != "nightly" || !rows[0].Enabled || rows[0].ArmedAt != 1 {
		t.Fatalf("mirror POSTs wrote: %+v", rows)
	}
	if len(stub.calls) != 0 {
		t.Fatalf("mirror run reached periodicRunFunc %d times", len(stub.calls))
	}
}

// Contract: op run — "200 `{\"ok\": true, \"slug\": string, \"started\": bool,
// \"note\": string}`, including when the job was created and its start was
// refused."
func TestPeriodicEdgeRunRefusedStartIs200(t *testing.T) {
	mux, st, stub := periodicEdgeMux(t)
	periodicEdgeSeed(t, st, "nightly", "nightly", false, 0)
	stub.run = jobs.PeriodicRun{Slug: "nightly-2026-11-13-run", Started: false, Note: "submit refused: no repo"}
	rec := periodicEdgePost(t, mux, map[string]string{"op": "run", "name": "nightly"})
	if rec.Code != http.StatusOK {
		t.Fatalf("run: %d %s, want 200", rec.Code, rec.Body.String())
	}
	var body struct {
		OK      *bool   `json:"ok"`
		Slug    *string `json:"slug"`
		Started *bool   `json:"started"`
		Note    *string `json:"note"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
	if body.OK == nil || !*body.OK || body.Slug == nil || *body.Slug != "nightly-2026-11-13-run" ||
		body.Started == nil || *body.Started || body.Note == nil || *body.Note != "submit refused: no repo" {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if len(stub.calls) != 1 || stub.calls[0] != "nightly" {
		t.Fatalf("run calls = %v, want exactly [nightly]", stub.calls)
	}
}

// Contract: "an op ignores the keys it does not need" — a pause/resume/remove
// carrying every add key still succeeds.
func TestPeriodicEdgeOpsIgnoreUnneededKeys(t *testing.T) {
	mux, st, _ := periodicEdgeMux(t)
	periodicEdgeSeed(t, st, "nightly", "nightly", true, 0)
	extra := func(op string) map[string]string {
		return map[string]string{"op": op, "name": "nightly", "prefix": "Not A Slug", "at": "99:99", "days": "nope,", "template": "garbage"}
	}
	if rec := periodicEdgePost(t, mux, extra("pause")); rec.Code != http.StatusOK {
		t.Fatalf("pause with extras: %d %s", rec.Code, rec.Body.String())
	}
	if rows := periodicEdgeRows(t, st); rows[0].Enabled || rows[0].Prefix != "nightly" || rows[0].AtMinute != 540 {
		t.Fatalf("pause row = %+v, want disabled and otherwise unchanged", rows[0])
	}
	if rec := periodicEdgePost(t, mux, extra("resume")); rec.Code != http.StatusOK {
		t.Fatalf("resume with extras: %d %s", rec.Code, rec.Body.String())
	}
	if rec := periodicEdgePost(t, mux, extra("remove")); rec.Code != http.StatusOK {
		t.Fatalf("remove with extras: %d %s", rec.Code, rec.Body.String())
	}
	if rows := periodicEdgeRows(t, st); len(rows) != 0 {
		t.Fatalf("rows after remove = %+v", rows)
	}
}

// Contract: webPeriodic's `days` is "`Cadence.DayNames`; an empty array, never
// null, for every day"; `next_due` is "0 when none" (a paused row has none,
// `PeriodicNextDue` being the zero time when `!p.Enabled`); "Every key is
// always present", and the dream row comes first with `builtin` true.
func TestPeriodicEdgeListWireEdges(t *testing.T) {
	mux, st, _ := periodicEdgeMux(t)
	periodicEdgeSeed(t, st, "paused-daily", "paused-daily", false, 0)
	periodicEdgeSeed(t, st, "fri-mon", "fri-mon", true, 1<<1|1<<5)
	rec := periodicEdgeDo(t, mux, http.MethodGet, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET: %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Periodic []map[string]json.RawMessage `json:"periodic"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
	if len(body.Periodic) != 3 {
		t.Fatalf("rows = %d, want dream + 2", len(body.Periodic))
	}
	keys := []string{"name", "prefix", "at", "days", "cadence", "enabled", "builtin", "template",
		"last_date", "last_slug", "last_note", "last_at", "next_due", "created_at"}
	for i, row := range body.Periodic {
		for _, k := range keys {
			if _, ok := row[k]; !ok {
				t.Errorf("row %d missing key %q", i, k)
			}
		}
	}
	raw := func(i int, k string) string { return strings.TrimSpace(string(body.Periodic[i][k])) }
	if raw(0, "name") != `"dream"` || raw(0, "builtin") != "true" || raw(0, "days") != "[]" {
		t.Errorf("dream row: name %s builtin %s days %s", raw(0, "name"), raw(0, "builtin"), raw(0, "days"))
	}
	if raw(1, "days") != "[]" || raw(1, "next_due") != "0" || raw(1, "enabled") != "false" || raw(1, "builtin") != "false" {
		t.Errorf("paused daily row: days %s next_due %s enabled %s builtin %s", raw(1, "days"), raw(1, "next_due"), raw(1, "enabled"), raw(1, "builtin"))
	}
	if raw(2, "days") != `["mon","fri"]` || raw(2, "cadence") != `"mon,fri 09:00"` || raw(2, "at") != `"09:00"` {
		t.Errorf("fri-mon row: days %s cadence %s at %s", raw(2, "days"), raw(2, "cadence"), raw(2, "at"))
	}
	if raw(2, "next_due") == "0" {
		t.Error("enabled row next_due = 0, want a time")
	}
}
