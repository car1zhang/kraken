package jobs

// Edge tests for contract slice 1, "merge bump": the two pure functions
// mergeBumpMessage and mergeBumpWanted. trackPRs itself shells out to tmux
// and is deliberately not called here.

import (
	"strings"
	"testing"
)

// edgeMergeBumpLiterals are the fixed phrases every notice must carry,
// whatever url it names.
var edgeMergeBumpLiterals = []string{"MERGED", "do not push to it", "origin/main", "awaiting checks"}

// TestEdgeMergeBumpMessageBoundaryURLs: the fixed obligations hold for an
// empty url and for urls carrying query strings, fragments and percent
// escapes — the url is embedded verbatim, never normalised.
func TestEdgeMergeBumpMessageBoundaryURLs(t *testing.T) {
	cases := []struct {
		name string
		url  string
	}{
		{"empty", ""},
		{"plain", "https://github.com/car1zhang/kraken/pull/1"},
		{"query and fragment", "https://github.com/o/r/pull/7?w=1#discussion_r42"},
		{"percent escapes", "https://github.com/o/r/pull/7%20x"},
		{"unicode owner", "https://github.com/ö/r/pull/9"},
		{"very long", "https://github.com/o/r/pull/" + strings.Repeat("9", 500)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := mergeBumpMessage(c.url)
			if !strings.HasPrefix(got, prWatchPrefix()+" ") {
				t.Errorf("mergeBumpMessage(%q) does not begin with prWatchPrefix+\" \": %q", c.url, got)
			}
			if !strings.Contains(got, c.url) {
				t.Errorf("mergeBumpMessage(%q) does not contain the url verbatim: %q", c.url, got)
			}
			for _, want := range edgeMergeBumpLiterals {
				if !strings.Contains(got, want) {
					t.Errorf("mergeBumpMessage(%q) missing literal %q: %q", c.url, want, got)
				}
			}
			if strings.Contains(got, "\n") {
				t.Errorf("mergeBumpMessage(%q) contains a newline: %q", c.url, got)
			}
			if again := mergeBumpMessage(c.url); again != got {
				t.Errorf("mergeBumpMessage(%q) not deterministic:\n%q\n%q", c.url, got, again)
			}
		})
	}
}

// TestEdgeMergeBumpMessageMergedIsUpperCase: the contract names the literal
// word MERGED in upper case; a lower-case "merged" alone does not satisfy it.
func TestEdgeMergeBumpMessageMergedIsUpperCase(t *testing.T) {
	got := mergeBumpMessage("https://github.com/o/r/pull/3")
	if !strings.Contains(got, "MERGED") {
		t.Fatalf("mergeBumpMessage lacks upper-case MERGED: %q", got)
	}
}

// TestEdgeMergeBumpMessageDistinctURLs: near-identical urls — a prefix of
// another, differing in one digit, differing only in the repo — must still
// yield distinct notices, since the url is the only thing that tells zeus
// WHICH of its PRs merged.
func TestEdgeMergeBumpMessageDistinctURLs(t *testing.T) {
	pairs := [][2]string{
		{"https://github.com/o/r/pull/1", "https://github.com/o/r/pull/10"},
		{"https://github.com/o/r/pull/12", "https://github.com/o/r/pull/13"},
		{"https://github.com/o/a/pull/5", "https://github.com/o/b/pull/5"},
		{"", "https://github.com/o/r/pull/1"},
	}
	for _, p := range pairs {
		a, b := mergeBumpMessage(p[0]), mergeBumpMessage(p[1])
		if a == b {
			t.Errorf("mergeBumpMessage(%q) == mergeBumpMessage(%q) = %q; want distinct", p[0], p[1], a)
		}
	}
}

// TestEdgeMergeBumpMessageNamesThePrefixOnceAtStart is a sanity check that the
// prefix is not doubled: the notice starts with the prefix exactly once.
func TestEdgeMergeBumpMessageNamesThePrefixOnceAtStart(t *testing.T) {
	got := mergeBumpMessage("https://github.com/o/r/pull/4")
	rest := strings.TrimPrefix(got, prWatchPrefix()+" ")
	if strings.HasPrefix(rest, prWatchPrefix()) {
		t.Errorf("mergeBumpMessage doubles the prefix: %q", got)
	}
}

func edgeMergeBumpJob(state string) *Job {
	return &Job{Slug: "edge-merge-job", ID: 4242, State: state}
}

// TestEdgeMergeBumpWantedTable: true exactly for working/blocked with the
// job's own session live; every other state, map shape and key is false.
func TestEdgeMergeBumpWantedTable(t *testing.T) {
	liveFor := func(j *Job) map[string]bool { return map[string]bool{CellSession(j): true} }
	cases := []struct {
		name  string
		state string
		live  func(j *Job) map[string]bool
		want  bool
	}{
		{"working live", "working", liveFor, true},
		{"blocked live", "blocked", liveFor, true},
		{"draft live", "draft", liveFor, false},
		{"done live", "done", liveFor, false},
		{"empty state live", "", liveFor, false},
		{"unknown state live", "archived", liveFor, false},
		{"upper-case Working live", "Working", liveFor, false},
		{"padded working live", " working", liveFor, false},
		{"working nil map", "working", func(*Job) map[string]bool { return nil }, false},
		{"blocked nil map", "blocked", func(*Job) map[string]bool { return nil }, false},
		{"working empty map", "working", func(*Job) map[string]bool { return map[string]bool{} }, false},
		{"working key false", "working", func(j *Job) map[string]bool { return map[string]bool{CellSession(j): false} }, false},
		{"blocked key false", "blocked", func(j *Job) map[string]bool { return map[string]bool{CellSession(j): false} }, false},
		{"working only other sessions live", "working", func(*Job) map[string]bool {
			return map[string]bool{"cell-other-1": true, "iris": true}
		}, false},
		{"working bare-slug session live", "working", func(j *Job) map[string]bool {
			// The pre-cutover bare-slug name is NOT this job's session.
			return map[string]bool{"cell-" + j.Slug: true}
		}, false},
		{"working same slug other id live", "working", func(j *Job) map[string]bool {
			other := &Job{Slug: j.Slug, ID: j.ID + 1, State: j.State}
			return map[string]bool{CellSession(other): true}
		}, false},
		{"draft nil map", "draft", func(*Job) map[string]bool { return nil }, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			j := edgeMergeBumpJob(c.state)
			if got := mergeBumpWanted(j, c.live(j)); got != c.want {
				t.Errorf("mergeBumpWanted(state=%q) = %v, want %v", c.state, got, c.want)
			}
		})
	}
}

// TestEdgeMergeBumpWantedIsPure: the function must not write into the map it
// is handed (the tick's live map is shared with watchPRs) nor change the job.
func TestEdgeMergeBumpWantedIsPure(t *testing.T) {
	j := edgeMergeBumpJob("working")
	live := map[string]bool{CellSession(j): true, "cell-x-1": false}
	for i := 0; i < 3; i++ {
		if !mergeBumpWanted(j, live) {
			t.Fatalf("call %d: mergeBumpWanted = false, want true", i)
		}
	}
	if len(live) != 2 || !live[CellSession(j)] || live["cell-x-1"] {
		t.Errorf("mergeBumpWanted mutated the live map: %v", live)
	}
	if j.State != "working" || j.Slug != "edge-merge-job" || j.ID != 4242 {
		t.Errorf("mergeBumpWanted mutated the job: %+v", j)
	}

	// A lookup on a missing key must not insert it.
	missing := map[string]bool{}
	mergeBumpWanted(j, missing)
	if len(missing) != 0 {
		t.Errorf("mergeBumpWanted inserted into an empty map: %v", missing)
	}
}
