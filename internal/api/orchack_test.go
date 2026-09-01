package api

import (
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"
)

// orchack_test.go — POST /api/orch/ack's response reports the disposition
// AS STORED, and whether the 200-rune cap cut it. The cap itself is
// unchanged; what changed is that an overrun is no longer silent.

func TestOrchAckReportsTheStoredDisposition(t *testing.T) {
	st := useTestStore(t)
	contractKronosEnable(t)
	mux := contractKronosMux(t)

	// Inside the cap: stored verbatim (one-lined), not truncated.
	one := contractKronosSeedRow(t, st, "job-blocked", "csv-export", "blocked", 1_755_000_000_001)
	rec := contractKronosPost(t, mux, "/api/orch/ack", map[string]any{
		"ids": []int64{one}, "disposition": "steered:csv-export  --  two\nlines",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	var resp orchAckResp
	contractKronosDecode(t, rec, &resp)
	if !resp.OK || resp.Truncated || resp.Disposition != "steered:csv-export -- two lines" {
		t.Fatalf("short disposition: %+v", resp)
	}

	// Over the cap: the response carries exactly what the row holds, and
	// says it was cut.
	two := contractKronosSeedRow(t, st, "note", "", "carl asked something", 1_755_000_000_002)
	long := "answered:" + strings.Repeat("x", 400)
	rec = contractKronosPost(t, mux, "/api/orch/ack", map[string]any{
		"ids": []int64{two}, "disposition": long,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	contractKronosDecode(t, rec, &resp)
	if !resp.OK || !resp.Truncated {
		t.Fatalf("long disposition not reported truncated: %+v", resp)
	}
	if n := utf8.RuneCountInString(resp.Disposition); n != orchDispositionMax {
		t.Fatalf("stored disposition is %d runes, want %d", n, orchDispositionMax)
	}
	if !strings.HasPrefix(resp.Disposition, "answered:xxx") || !strings.HasSuffix(resp.Disposition, "…") {
		t.Fatalf("stored disposition is not the capped head: %q", resp.Disposition)
	}

	// Exactly at the cap after one-lining: not truncated — whitespace that
	// one-lining collapses is not a cut.
	three := contractKronosSeedRow(t, st, "pr-merged", "csv-export", "merged", 1_755_000_000_003)
	exact := strings.Repeat("y", orchDispositionMax) + "\n\n   \n"
	rec = contractKronosPost(t, mux, "/api/orch/ack", map[string]any{
		"ids": []int64{three}, "disposition": exact,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	contractKronosDecode(t, rec, &resp)
	if resp.Truncated || utf8.RuneCountInString(resp.Disposition) != orchDispositionMax {
		t.Fatalf("at-cap disposition misreported: truncated=%v len=%d", resp.Truncated, utf8.RuneCountInString(resp.Disposition))
	}
}
