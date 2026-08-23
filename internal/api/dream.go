package api

import (
	"net/http"
	"sort"
	"strings"

	"kraken/internal/jobs"
)

// dream.go — GET /api/dream: the board's DREAM view, which is nothing but
// the night reports in reverse chronological order.
//
// There is no new persistence path here and deliberately so. A night report
// is `00-dream-report.md`, an ordinary artifact of an ordinary job — the
// dream-<YYYY-MM-DD> job that wrote it — so this handler is a filter over
// the artifact plumbing board.go already owns: the same cell ∪ store ∪ pluto
// union jobArtifacts resolves, which is what makes a report survive its
// cell's teardown without anything having to copy it anywhere. Adding a
// `dream_reports` table would have meant a second copy of bytes that already
// exist, kept in sync by hand.

// dreamSlugPrefix identifies a dream job. jobs.CreateDreamJob mints exactly
// one per local calendar day as `dream-<YYYY-MM-DD>`, so the prefix is both
// the filter and (with it stripped) the report's date.
const dreamSlugPrefix = "dream-"

// webDreamReport is one night's report on the wire. Additive endpoint — no
// apiContract bump. body is the raw markdown, served inline rather than as a
// URL because a report is one short page and the DREAM view renders every
// visible one: a URL per report would turn one poll into N round trips for
// no benefit.
type webDreamReport struct {
	Date string `json:"date"` // YYYY-MM-DD, from the job's own slug
	Slug string `json:"slug"`
	Body string `json:"body"`
}

// dreamJobHome is the artifact home a non-live dream job's report resolves
// under — mirrors currentArtifactHome's own archived/active split.
func dreamJobHome(j *jobs.Job) string {
	if j.ArchivedName != "" {
		return "archive"
	}
	return "pluto"
}

// dreamReportBody reads j's night report, trying the homes in jobArtifacts'
// own precedence: a live cell's files first (Q12 — a live cell's artifacts
// are never DB-backed), then the store row / preserved flat copy for a job
// whose cell is gone. ok is false when the job has no report yet, which is
// the normal state of a dream job that is still running.
func dreamReportBody(j *jobs.Job) (string, bool) {
	for _, home := range []string{"cell", dreamJobHome(j)} {
		if data, _, ok := resolveArtifact(j, home, jobs.DreamReportName); ok && len(data) > 0 {
			return string(data), true
		}
	}
	return "", false
}

// dreamReports collects every dream job's report, newest first. Both
// namespaces are swept: an archived dream job's report is still a night that
// happened, and the archive is where old ones end up.
func dreamReports() []webDreamReport {
	var out []webDreamReport
	seen := map[string]bool{}
	add := func(js []*jobs.Job) {
		for _, j := range js {
			if !strings.HasPrefix(j.Slug, dreamSlugPrefix) || seen[j.Slug] {
				continue
			}
			body, ok := dreamReportBody(j)
			if !ok {
				continue // a night still in progress has nothing to show yet
			}
			seen[j.Slug] = true
			out = append(out, webDreamReport{
				Date: strings.TrimPrefix(j.Slug, dreamSlugPrefix),
				Slug: j.Slug, Body: body,
			})
		}
	}
	add(jobs.LoadJobs())
	add(jobs.LoadArchivedJobs())
	// Newest first. The date is a zero-padded YYYY-MM-DD, so a plain reverse
	// string sort IS chronological; slug breaks ties (a re-run day that got a
	// suffix) deterministically.
	sort.Slice(out, func(a, b int) bool {
		if out[a].Date != out[b].Date {
			return out[a].Date > out[b].Date
		}
		return out[a].Slug > out[b].Slug
	})
	return out
}

// dreamHandler serves GET /api/dream. A read, so it stays reachable under
// --mirror. Always a JSON array, never null — a machine that has never
// dreamt serves [].
func dreamHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
		return
	}
	reports := dreamReports()
	if reports == nil {
		reports = []webDreamReport{}
	}
	webJSON(w, http.StatusOK, map[string]any{"reports": reports})
}
