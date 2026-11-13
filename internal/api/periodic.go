package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"kraken/internal/jobs"
	"kraken/internal/store"
	"kraken/internal/util"
)

// periodic.go — GET/POST /api/periodic: the periodic jobs the daemon fires
// on a cadence (internal/jobs/periodic.go), listed with the built-in dream
// pass first. Read by the board's periodic view and proxied by
// `kraken periodic`. The POST is per-op ({op, name, ...}) like
// /api/job/stack's, so each write names the one row it changes.

// webPeriodic is one row on the wire. Every key is always present; `days` is
// an empty array, never null, for a row that fires every day.
type webPeriodic struct {
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

// webPeriodicReq is the POST body. Every key is a string; an op ignores the
// ones it does not need.
type webPeriodicReq struct {
	Op       string `json:"op"`
	Name     string `json:"name"`
	Prefix   string `json:"prefix"`
	At       string `json:"at"`
	Days     string `json:"days"`
	Template string `json:"template"`
}

// periodicRunFunc is the run op's seam: no test in this package starts a cell.
var periodicRunFunc = jobs.RunPeriodicNow

// periodicNow is the clock every op reads.
var periodicNow = time.Now

func webPeriodicFrom(p store.PeriodicJob, builtin bool, nextDue int64) webPeriodic {
	c := jobs.Cadence{AtMinute: p.AtMinute, Days: p.Days}
	return webPeriodic{
		Name: p.Name, Prefix: p.Prefix,
		At: c.At(), Days: c.DayNames(), Cadence: c.String(),
		Enabled: p.Enabled, Builtin: builtin, Template: p.Template,
		LastDate: p.LastDate, LastSlug: p.LastSlug, LastNote: p.LastNote, LastAt: p.LastAt,
		NextDue: nextDue, CreatedAt: p.CreatedAt,
	}
}

// webPeriodicStored shapes a row just written, with its next due time
// computed against the same clock the write used.
func webPeriodicStored(p *store.PeriodicJob, now time.Time) webPeriodic {
	var next int64
	if t := jobs.PeriodicNextDue(*p, now); !t.IsZero() {
		next = t.UnixMilli()
	}
	return webPeriodicFrom(*p, false, next)
}

// periodicHandler serves the list (GET, open under --mirror) and the writes
// (POST, gated inline so the GET stays reachable).
func periodicHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		rows, err := jobs.ListPeriodic(periodicNow())
		if err != nil {
			webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		out := make([]webPeriodic, 0, len(rows))
		for _, row := range rows {
			out = append(out, webPeriodicFrom(row.PeriodicJob, row.Builtin, row.NextDue))
		}
		webJSON(w, http.StatusOK, map[string]any{"periodic": out})
	case http.MethodPost:
		if mirrorModeOn() {
			http.Error(w, "disabled in mirror mode", http.StatusForbidden)
			return
		}
		periodicPost(w, r)
	default:
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET or POST only"})
	}
}

func periodicPost(w http.ResponseWriter, r *http.Request) {
	var req webPeriodicReq
	if !decodeStrictBody(w, r, &req, "this route takes op, name, prefix, at, days and template") {
		return
	}
	// Trimmed once for every op: add stores the trimmed name, so the other
	// ops must look it up the same way whatever padding the caller sent.
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "missing name"})
		return
	}
	now := periodicNow()
	switch req.Op {
	case "add":
		p, err := jobs.AddPeriodic(req.Name, req.Prefix, req.At, req.Days, req.Template, now)
		if err != nil {
			periodicError(w, "add", req.Name, err)
			return
		}
		util.LogEvent("web", "info", "periodic", "added "+p.Name)
		webJSON(w, http.StatusOK, map[string]any{"ok": true, "periodic": webPeriodicStored(p, now)})
	case "pause", "resume":
		p, err := jobs.SetPeriodicPaused(req.Name, req.Op == "pause", now)
		if err != nil {
			periodicError(w, req.Op, req.Name, err)
			return
		}
		util.LogEvent("web", "info", "periodic", req.Op+" "+p.Name)
		webJSON(w, http.StatusOK, map[string]any{"ok": true, "periodic": webPeriodicStored(p, now)})
	case "run":
		run, err := periodicRunFunc(req.Name, now)
		if err != nil {
			periodicError(w, "run", req.Name, err)
			return
		}
		webJSON(w, http.StatusOK, map[string]any{"ok": true, "slug": run.Slug, "started": run.Started, "note": run.Note})
	case "remove":
		if err := jobs.RemovePeriodic(req.Name); err != nil {
			periodicError(w, "remove", req.Name, err)
			return
		}
		util.LogEvent("web", "info", "periodic", "removed "+req.Name)
		webJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown op " + quoteOp(req.Op) + "; use add, pause, resume, run or remove"})
	}
}

func quoteOp(op string) string {
	if op == "" {
		return "(none)"
	}
	return `"` + op + `"`
}

// periodicError maps the jobs layer's refusals onto status codes. Anything
// it does not recognise is a 500, logged, since the remaining errors are a
// store that failed rather than a request that was wrong.
func periodicError(w http.ResponseWriter, op, name string, err error) {
	status := http.StatusInternalServerError
	msg := err.Error()
	switch {
	case errors.Is(err, jobs.ErrPeriodicInvalid):
		status = http.StatusBadRequest
	case errors.Is(err, store.ErrPeriodicTaken):
		status = http.StatusConflict
		msg = "a periodic job already uses that name or prefix"
	case errors.Is(err, jobs.ErrPeriodicBuiltin):
		status = http.StatusConflict
	case errors.Is(err, store.ErrNotFound):
		status = http.StatusNotFound
		msg = "no periodic job named " + name
	default:
		util.LogEvent("web", "error", "periodic", op+" "+name+": "+msg)
	}
	webJSON(w, status, map[string]string{"error": msg})
}
