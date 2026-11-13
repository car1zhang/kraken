package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

// periodicsteer_test.go — the route's half of interval rows, added at the
// manual-test gate. Written by the lead with the implementation in view; it
// borrows the edge suite's mux and post helpers.

const periodicSteerTemplate = "---\nrepo: kraken\n---\nsteer {{slug}}\n"

func TestPeriodicSteerAddEvery(t *testing.T) {
	mux, st, _ := periodicEdgeMux(t)
	rec := periodicEdgePost(t, mux, map[string]string{
		"op": "add", "name": "pulse", "prefix": "pulse", "every": "2h", "template": periodicSteerTemplate,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("add every: status %d (%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		Periodic struct {
			At      string   `json:"at"`
			Days    []string `json:"days"`
			Every   string   `json:"every"`
			Cadence string   `json:"cadence"`
			NextDue int64    `json:"next_due"`
		} `json:"periodic"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	p := got.Periodic
	if p.At != "" || p.Days == nil || len(p.Days) != 0 || p.Every != "2h" || p.Cadence != "every 2h" || p.NextDue == 0 {
		t.Errorf("add every answered %+v; want at \"\", days [], every 2h, cadence every 2h, a next_due", p)
	}
	rows := periodicEdgeRows(t, st)
	if len(rows) != 1 || rows[0].EveryMinutes != 120 {
		t.Fatalf("stored rows = %+v, want one with EveryMinutes 120", rows)
	}
}

func TestPeriodicSteerAddEveryRefusals(t *testing.T) {
	mux, st, _ := periodicEdgeMux(t)
	for name, body := range map[string]map[string]string{
		"every with at":   {"op": "add", "name": "pulse", "prefix": "pulse", "every": "2h", "at": "09:00", "template": periodicSteerTemplate},
		"every with days": {"op": "add", "name": "pulse", "prefix": "pulse", "every": "2h", "days": "mon", "template": periodicSteerTemplate},
		"bad interval":    {"op": "add", "name": "pulse", "prefix": "pulse", "every": "1m", "template": periodicSteerTemplate},
		"neither":         {"op": "add", "name": "pulse", "prefix": "pulse", "template": periodicSteerTemplate},
	} {
		periodicEdgeWantError(t, name, periodicEdgePost(t, mux, body), http.StatusBadRequest)
	}
	if rows := periodicEdgeRows(t, st); len(rows) != 0 {
		t.Fatalf("refused adds stored rows: %+v", rows)
	}
}
