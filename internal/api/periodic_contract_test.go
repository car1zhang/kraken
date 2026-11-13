package api

// periodic_contract_test.go — athena-logic's BLIND tests for 50-contract.md
// (kraken-periodic-jobs), Slice 3: the /api/periodic route's happy paths and
// the wire shapes of GET /api/periodic, one webPeriodic element, and each POST
// op's 200 body, pinned with contract_test.go's assertShape/fieldSpec. Written
// from the frozen contract alone; internal/api/periodic.go and the
// working-tree register.go were never opened. Dimension: LOGIC. The 400 / 403
// / 404 / 405 / 409 answers are athena-edge's.
//
// Isolation: useTestStore (temp HOME + own store), a temp KRAKEN_REPOS_DIR
// holding a bare kraken/.git for AddPeriodic's repo check, mirror mode reset,
// and periodicRunFunc replaced in every test that posts op run.
//
// Every package-level identifier here starts with periodicContract or
// TestPeriodicContract.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"kraken/internal/jobs"
	"kraken/internal/store"
)

// Contract, Slice 3: "webPeriodic, one row on the wire. Every key is always
// present" — with the types the contract's table gives.
var periodicContractWebPeriodicSpec = map[string]fieldSpec{
	"name":       {kind: wireString, required: true},
	"prefix":     {kind: wireString, required: true},
	"at":         {kind: wireString, required: true},
	"days":       {kind: wireArray, required: true},
	"every":      {kind: wireString, required: true},
	"cadence":    {kind: wireString, required: true},
	"enabled":    {kind: wireBool, required: true},
	"builtin":    {kind: wireBool, required: true},
	"template":   {kind: wireString, required: true},
	"last_date":  {kind: wireString, required: true},
	"last_slug":  {kind: wireString, required: true},
	"last_note":  {kind: wireString, required: true},
	"last_at":    {kind: wireNumber, required: true},
	"next_due":   {kind: wireNumber, required: true},
	"created_at": {kind: wireNumber, required: true},
}

// Contract, GET /api/periodic: "200 with {"periodic": [webPeriodic, ...]}".
var periodicContractListSpec = map[string]fieldSpec{
	"periodic": {kind: wireArray, required: true},
}

// Contract, op add / pause / resume: "200 {"ok": true, "periodic": webPeriodic}".
var periodicContractRowAnswerSpec = map[string]fieldSpec{
	"ok":       {kind: wireBool, required: true},
	"periodic": {kind: wireObject, required: true},
}

// Contract, op run: "200 {"ok": true, "slug": string, "started": bool, "note": string}".
var periodicContractRunAnswerSpec = map[string]fieldSpec{
	"ok":      {kind: wireBool, required: true},
	"slug":    {kind: wireString, required: true},
	"started": {kind: wireBool, required: true},
	"note":    {kind: wireString, required: true},
}

// Contract, op remove: "200 {"ok": true}".
var periodicContractOKSpec = map[string]fieldSpec{
	"ok": {kind: wireBool, required: true},
}

const periodicContractTemplate = "---\nrepo: kraken\n---\nRun the {{slug}} pass for {{date}}.\n"

// periodicContractWebRow mirrors the contract's webPeriodic table, for
// decoding values once the shape has been pinned.
type periodicContractWebRow struct {
	Name      string   `json:"name"`
	Prefix    string   `json:"prefix"`
	At        string   `json:"at"`
	Days      []string `json:"days"`
	Cadence   string   `json:"cadence"`
	Enabled   bool     `json:"enabled"`
	Builtin   bool     `json:"builtin"`
	Template  string   `json:"template"`
	LastDate  string   `json:"last_date"`
	LastSlug  string   `json:"last_slug"`
	LastNote  string   `json:"last_note"`
	LastAt    int64    `json:"last_at"`
	NextDue   int64    `json:"next_due"`
	CreatedAt int64    `json:"created_at"`
}

func periodicContractMux(t *testing.T) (*http.ServeMux, *store.Store) {
	t.Helper()
	SetMirrorMode(false)
	t.Cleanup(func() { SetMirrorMode(false) })
	st := useTestStore(t)
	repos := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repos, "kraken", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KRAKEN_REPOS_DIR", repos)
	mux := http.NewServeMux()
	Register(mux)
	return mux, st
}

// periodicContractStubRun replaces periodicRunFunc for the whole test.
func periodicContractStubRun(t *testing.T, fn func(name string, now time.Time) (jobs.PeriodicRun, error)) {
	t.Helper()
	prev := periodicRunFunc
	periodicRunFunc = fn
	t.Cleanup(func() { periodicRunFunc = prev })
}

func periodicContractGET(t *testing.T, mux http.Handler) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/periodic", nil))
	return rec
}

// periodicContractPOST sends all six keys the contract names, every one a
// string, so the request is well-formed for every op.
func periodicContractPOST(t *testing.T, mux http.Handler, op, name, prefix, at, days, template string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]string{
		"op": op, "name": name, "prefix": prefix, "at": at, "days": days, "template": template,
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/periodic", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func periodicContractWant200(t *testing.T, label string, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: status %d body %s, want 200", label, rec.Code, rec.Body.String())
	}
}

// periodicContractList GETs the list, pins the list and element shapes, and
// returns the decoded rows.
func periodicContractList(t *testing.T, mux http.Handler) []periodicContractWebRow {
	t.Helper()
	rec := periodicContractGET(t, mux)
	periodicContractWant200(t, "GET /api/periodic", rec)
	assertShape(t, "GET /api/periodic", rec.Body.Bytes(), periodicContractListSpec)
	var raw struct {
		Periodic []json.RawMessage `json:"periodic"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	out := make([]periodicContractWebRow, 0, len(raw.Periodic))
	for i, el := range raw.Periodic {
		assertShape(t, "webPeriodic["+periodicContractItoa(i)+"]", el, periodicContractWebPeriodicSpec)
		var row periodicContractWebRow
		if err := json.Unmarshal(el, &row); err != nil {
			t.Fatalf("decode element %d: %v", i, err)
		}
		out = append(out, row)
	}
	return out
}

func periodicContractItoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}

// periodicContractRowAnswer pins an add/pause/resume answer's shape and
// returns its webPeriodic.
func periodicContractRowAnswer(t *testing.T, label string, rec *httptest.ResponseRecorder) periodicContractWebRow {
	t.Helper()
	periodicContractWant200(t, label, rec)
	assertShape(t, label, rec.Body.Bytes(), periodicContractRowAnswerSpec)
	var raw struct {
		OK       bool            `json:"ok"`
		Periodic json.RawMessage `json:"periodic"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("%s: decode: %v", label, err)
	}
	if !raw.OK {
		t.Fatalf("%s: ok = false", label)
	}
	assertShape(t, label+" periodic", raw.Periodic, periodicContractWebPeriodicSpec)
	var row periodicContractWebRow
	if err := json.Unmarshal(raw.Periodic, &row); err != nil {
		t.Fatalf("%s: decode periodic: %v", label, err)
	}
	return row
}

// Contract, GET /api/periodic: "200 with {"periodic": [webPeriodic, ...]} in
// ListPeriodic order." ListPeriodic puts the built-in dream row first: Name
// and Prefix dream, AtMinute 0, Days 0, Enabled true, Builtin true, Template
// empty. webPeriodic: at is "HH:MM", days "an empty array, never null, for
// every day", cadence is Cadence.String ("daily HH:MM" for Days 0), builtin
// "true only for the dream row".
func TestPeriodicContractGETDreamRowOnly(t *testing.T) {
	mux, _ := periodicContractMux(t)
	rows := periodicContractList(t, mux)
	if len(rows) != 1 {
		t.Fatalf("GET with no stored rows = %d rows, want 1 (the dream row)", len(rows))
	}
	d := rows[0]
	if d.Name != "dream" || d.Prefix != "dream" || d.At != "00:00" || d.Cadence != "daily 00:00" ||
		!d.Enabled || !d.Builtin || d.Template != "" {
		t.Fatalf("dream row = %+v", d)
	}
	// "never null": assertShape already refuses a null days, and a JSON []
	// decodes to a non-nil empty slice.
	if d.Days == nil || len(d.Days) != 0 {
		t.Fatalf("dream row days = %#v, want an empty array", d.Days)
	}
}

// Contract, op add: "Calls jobs.AddPeriodic. 200 {"ok": true, "periodic":
// webPeriodic}." The row's at / days / cadence come from Cadence.At,
// DayNames and String; the stored row is enabled with its Template as given
// and empty Last* fields; builtin is false for a stored row.
func TestPeriodicContractAddAnswer(t *testing.T) {
	mux, st := periodicContractMux(t)
	rec := periodicContractPOST(t, mux, "add", "Standup Notes", "standup", "9:05", "fri,mon,wed", periodicContractTemplate)
	row := periodicContractRowAnswer(t, "POST add", rec)

	want := periodicContractWebRow{
		Name: "Standup Notes", Prefix: "standup", At: "09:05",
		Days: []string{"mon", "wed", "fri"}, Cadence: "mon,wed,fri 09:05",
		Enabled: true, Builtin: false, Template: periodicContractTemplate,
	}
	got := row
	got.NextDue, got.CreatedAt = 0, 0 // both depend on the server's clock
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("add answer = %+v\nwant         %+v", got, want)
	}
	stored, err := st.GetPeriodicJob("Standup Notes")
	if err != nil {
		t.Fatalf("row not stored: %v", err)
	}
	if row.CreatedAt != stored.CreatedAt {
		t.Fatalf("created_at = %d, stored CreatedAt = %d", row.CreatedAt, stored.CreatedAt)
	}
}

// Contract, op add with days "" (every day): days is "an empty array, never
// null, for every day", and cadence is "daily HH:MM".
func TestPeriodicContractAddDailyAnswer(t *testing.T) {
	mux, _ := periodicContractMux(t)
	rec := periodicContractPOST(t, mux, "add", "nightly", "nightly", "23:30", "", periodicContractTemplate)
	row := periodicContractRowAnswer(t, "POST add daily", rec)
	if row.Cadence != "daily 23:30" || row.At != "23:30" {
		t.Fatalf("cadence/at = %q/%q, want daily 23:30 / 23:30", row.Cadence, row.At)
	}
	if row.Days == nil || len(row.Days) != 0 {
		t.Fatalf("days = %#v, want []", row.Days)
	}
}

// Contract, GET: rows "in ListPeriodic order" — dream first, then stored rows
// in creation order, each a full webPeriodic.
func TestPeriodicContractGETOrder(t *testing.T) {
	mux, _ := periodicContractMux(t)
	for _, n := range []string{"mike", "alpha", "zulu"} {
		periodicContractWant200(t, "add "+n, periodicContractPOST(t, mux, "add", n, n, "08:00", "weekdays", periodicContractTemplate))
	}
	rows := periodicContractList(t, mux)
	var names []string
	for _, r := range rows {
		names = append(names, r.Name)
	}
	if want := []string{"dream", "mike", "alpha", "zulu"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("GET order = %v, want %v", names, want)
	}
	for _, r := range rows[1:] {
		if r.Builtin {
			t.Fatalf("stored row %q builtin = true", r.Name)
		}
		if !reflect.DeepEqual(r.Days, []string{"mon", "tue", "wed", "thu", "fri"}) || r.Cadence != "mon,tue,wed,thu,fri 08:00" {
			t.Fatalf("row %q days/cadence = %v / %q", r.Name, r.Days, r.Cadence)
		}
	}
}

// Contract, op pause / op resume: "200 {"ok": true, "periodic": webPeriodic}
// for the row afterwards."
func TestPeriodicContractPauseResumeAnswers(t *testing.T) {
	mux, st := periodicContractMux(t)
	periodicContractWant200(t, "add", periodicContractPOST(t, mux, "add", "standup", "standup", "09:00", "", periodicContractTemplate))

	paused := periodicContractRowAnswer(t, "POST pause", periodicContractPOST(t, mux, "pause", "standup", "", "", "", ""))
	if paused.Name != "standup" || paused.Enabled {
		t.Fatalf("pause answer = %+v, want standup disabled", paused)
	}
	if paused.NextDue != 0 {
		t.Fatalf("paused next_due = %d, want 0 (PeriodicNextDue of a disabled row is the zero time)", paused.NextDue)
	}
	if p, err := st.GetPeriodicJob("standup"); err != nil || p.Enabled {
		t.Fatalf("stored after pause: %+v (err %v)", p, err)
	}

	resumed := periodicContractRowAnswer(t, "POST resume", periodicContractPOST(t, mux, "resume", "standup", "", "", "", ""))
	if resumed.Name != "standup" || !resumed.Enabled {
		t.Fatalf("resume answer = %+v, want standup enabled", resumed)
	}
	if p, err := st.GetPeriodicJob("standup"); err != nil || !p.Enabled {
		t.Fatalf("stored after resume: %+v (err %v)", p, err)
	}
}

// Contract, op run: "Calls periodicRunFunc. 200 {"ok": true, "slug": string,
// "started": bool, "note": string}, including when the job was created and its
// start was refused."
func TestPeriodicContractRunAnswer(t *testing.T) {
	mux, _ := periodicContractMux(t)
	periodicContractWant200(t, "add", periodicContractPOST(t, mux, "add", "standup", "standup", "09:00", "", periodicContractTemplate))

	for _, tc := range []struct {
		label string
		run   jobs.PeriodicRun
	}{
		{"started", jobs.PeriodicRun{Slug: "standup-2026-11-13-run", Started: true, Note: ""}},
		{"start refused", jobs.PeriodicRun{Slug: "standup-2026-11-13-run-2", Started: false, Note: "no qualifying launcher"}},
	} {
		var gotNames []string
		periodicContractStubRun(t, func(name string, now time.Time) (jobs.PeriodicRun, error) {
			gotNames = append(gotNames, name)
			return tc.run, nil
		})
		rec := periodicContractPOST(t, mux, "run", "standup", "", "", "", "")
		periodicContractWant200(t, "POST run "+tc.label, rec)
		assertShape(t, "POST run "+tc.label, rec.Body.Bytes(), periodicContractRunAnswerSpec)
		var got struct {
			OK      bool   `json:"ok"`
			Slug    string `json:"slug"`
			Started bool   `json:"started"`
			Note    string `json:"note"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("%s: decode: %v", tc.label, err)
		}
		if !got.OK || got.Slug != tc.run.Slug || got.Started != tc.run.Started || got.Note != tc.run.Note {
			t.Fatalf("%s: answer = %+v, want ok + %+v", tc.label, got, tc.run)
		}
		if !reflect.DeepEqual(gotNames, []string{"standup"}) {
			t.Fatalf("%s: periodicRunFunc names = %v, want [standup]", tc.label, gotNames)
		}
	}
}

// Contract, op remove: "200 {"ok": true}." The row then leaves the GET list.
func TestPeriodicContractRemoveAnswer(t *testing.T) {
	mux, st := periodicContractMux(t)
	periodicContractWant200(t, "add", periodicContractPOST(t, mux, "add", "standup", "standup", "09:00", "", periodicContractTemplate))
	periodicContractWant200(t, "add other", periodicContractPOST(t, mux, "add", "other", "other", "09:00", "", periodicContractTemplate))

	rec := periodicContractPOST(t, mux, "remove", "standup", "", "", "", "")
	periodicContractWant200(t, "POST remove", rec)
	assertShape(t, "POST remove", rec.Body.Bytes(), periodicContractOKSpec)
	var got struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || !got.OK {
		t.Fatalf("remove answer = %s (err %v), want ok true", rec.Body.String(), err)
	}
	rows, err := st.ListPeriodicJobs()
	if err != nil || len(rows) != 1 || rows[0].Name != "other" {
		t.Fatalf("stored rows after remove = %+v (err %v), want only other", rows, err)
	}
	var names []string
	for _, r := range periodicContractList(t, mux) {
		names = append(names, r.Name)
	}
	if !reflect.DeepEqual(names, []string{"dream", "other"}) {
		t.Fatalf("GET after remove = %v, want [dream other]", names)
	}
}

// Contract, webPeriodic: last_date / last_slug / last_note / last_at carry the
// stored row's Last* fields (here stamped straight through the store).
func TestPeriodicContractGETCarriesLastFiring(t *testing.T) {
	mux, st := periodicContractMux(t)
	periodicContractWant200(t, "add", periodicContractPOST(t, mux, "add", "standup", "standup", "09:00", "", periodicContractTemplate))
	p, err := st.GetPeriodicJob("standup")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.StampPeriodicFiring(p.ID, "2026-11-12", "standup-2026-11-12", "start refused", 1769000000000); err != nil {
		t.Fatal(err)
	}
	rows := periodicContractList(t, mux)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	r := rows[1]
	if r.LastDate != "2026-11-12" || r.LastSlug != "standup-2026-11-12" || r.LastNote != "start refused" || r.LastAt != 1769000000000 {
		t.Fatalf("last firing on the wire = %+v", r)
	}
	if r.CreatedAt != p.CreatedAt {
		t.Fatalf("created_at = %d, want %d", r.CreatedAt, p.CreatedAt)
	}
}

// Contract, GET /api/periodic: "Served in mirror mode too."
func TestPeriodicContractGETInMirror(t *testing.T) {
	mux, _ := periodicContractMux(t)
	periodicContractWant200(t, "add", periodicContractPOST(t, mux, "add", "standup", "standup", "09:00", "", periodicContractTemplate))
	SetMirrorMode(true)
	rows := periodicContractList(t, mux)
	if len(rows) != 2 || rows[0].Name != "dream" || rows[1].Name != "standup" {
		t.Fatalf("mirror GET rows = %+v", rows)
	}
}
