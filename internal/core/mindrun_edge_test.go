package core

import (
	"reflect"
	"sort"
	"testing"
	"time"
)

// mindrun_edge_test.go — the EDGE dimension of 50-contract.md slice B
// (`OperatorOptions.Sessions` / `.ExcludeSessions`). Written blind from the
// frozen contract: operator.go's diff was never read. Fixtures reuse only
// mine_test.go's committed helpers (mineHome, mineWrite, mineJSON, mineISO).
//
// Every package-level identifier is prefixed `edgeMR`.

const (
	edgeMRProject = "-Users-carl-Developer-kraken"
	edgeMRLogin   = "carl@probook.ai"
)

// edgeMRTyped is one typed operator prompt (origin human, promptSource typed).
func edgeMRTyped(t *testing.T, ts int64, uuid, stem, text string) string {
	t.Helper()
	return mineJSON(t, map[string]any{
		"type":         "user",
		"timestamp":    mineISO(ts),
		"uuid":         uuid,
		"sessionId":    stem,
		"message":      map[string]any{"role": "user", "content": text},
		"origin":       map[string]any{"kind": "human"},
		"promptSource": "typed",
	})
}

func edgeMRProse(t *testing.T, opts OperatorOptions) *OperatorResult {
	t.Helper()
	res, err := OperatorProse(opts)
	if err != nil {
		t.Fatalf("OperatorProse(%+v): %v", opts, err)
	}
	if res == nil {
		t.Fatalf("OperatorProse(%+v) = nil result, nil error", opts)
	}
	return res
}

// edgeMRWorld writes three root transcripts, one typed prompt each, whose
// session ids are their file stems.
func edgeMRWorld(t *testing.T) (since int64) {
	t.Helper()
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	since = now - int64(time.Hour/time.Millisecond)
	for i, sess := range []string{"sess-a", "sess-b", "sess-c"} {
		mineWrite(t, projects, edgeMRProject, sess+".jsonl",
			edgeMRTyped(t, now-int64(5000-i*1000), "u-"+sess, sess, "PROMPT IN "+sess),
		)
	}
	return since
}

func edgeMRSessions(res *OperatorResult) []string {
	out := []string{}
	for _, r := range res.Records {
		out = append(out, r.Session)
	}
	sort.Strings(out)
	return out
}

// "both empty means today's behaviour byte for byte": nil filters and empty
// (non-nil) filters return the same result as each other, every session
// present.
func TestEdgeMROperatorEmptyFiltersChangeNothing(t *testing.T) {
	since := edgeMRWorld(t)
	base := edgeMRProse(t, OperatorOptions{Since: since, Login: edgeMRLogin})
	empty := edgeMRProse(t, OperatorOptions{Since: since, Login: edgeMRLogin, Sessions: []string{}, ExcludeSessions: []string{}})
	if !reflect.DeepEqual(base, empty) {
		t.Fatalf("empty filters changed the result:\nnil   %+v\nempty %+v", base, empty)
	}
	if got := edgeMRSessions(base); !reflect.DeepEqual(got, []string{"sess-a", "sess-b", "sess-c"}) {
		t.Fatalf("unfiltered sessions = %v, want all three", got)
	}
}

// "applied after `Sessions`": a session in both sets is dropped; an exclude
// naming a session outside `Sessions` changes nothing; an include naming no
// session on disk is an empty result, not "no filter".
func TestEdgeMROperatorExcludeAppliesAfterSessions(t *testing.T) {
	since := edgeMRWorld(t)
	cases := []struct {
		name string
		inc  []string
		exc  []string
		want []string
	}{
		{"exclude one of the included", []string{"sess-a", "sess-b"}, []string{"sess-b"}, []string{"sess-a"}},
		{"exclude outside the included", []string{"sess-a", "sess-b"}, []string{"sess-c"}, []string{"sess-a", "sess-b"}},
		{"exclude every included", []string{"sess-a"}, []string{"sess-a"}, []string{}},
		{"exclude only", nil, []string{"sess-c"}, []string{"sess-a", "sess-b"}},
		{"include unknown", []string{"sess-zzz"}, nil, []string{}},
		{"include is exact, not prefix", []string{"sess"}, nil, []string{}},
		{"exclude is exact, not prefix", nil, []string{"sess"}, []string{"sess-a", "sess-b", "sess-c"}},
	}
	for _, c := range cases {
		res := edgeMRProse(t, OperatorOptions{Since: since, Login: edgeMRLogin, Sessions: c.inc, ExcludeSessions: c.exc})
		if got := edgeMRSessions(res); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: sessions = %v, want %v", c.name, got, c.want)
		}
		if res.Records == nil {
			t.Errorf("%s: Records is nil, want a non-nil slice", c.name)
		}
	}
}

// "`Scanned`/`Skipped` still count every transcript walked" — filters that
// drop every record leave both counters equal to the unfiltered run's.
func TestEdgeMROperatorFiltersDoNotMoveCounters(t *testing.T) {
	since := edgeMRWorld(t)
	base := edgeMRProse(t, OperatorOptions{Since: since, Login: edgeMRLogin})
	if base.Scanned < 3 {
		t.Fatalf("premise: unfiltered Scanned = %d, want at least the three transcripts", base.Scanned)
	}
	for _, opts := range []OperatorOptions{
		{Since: since, Login: edgeMRLogin, Sessions: []string{"sess-a"}},
		{Since: since, Login: edgeMRLogin, Sessions: []string{"nobody"}},
		{Since: since, Login: edgeMRLogin, ExcludeSessions: []string{"sess-a", "sess-b", "sess-c"}},
	} {
		res := edgeMRProse(t, opts)
		if res.Scanned != base.Scanned || res.Skipped != base.Skipped {
			t.Errorf("filters %v / %v: scanned %d skipped %d, want %d/%d", opts.Sessions, opts.ExcludeSessions, res.Scanned, res.Skipped, base.Scanned, base.Skipped)
		}
	}
}
