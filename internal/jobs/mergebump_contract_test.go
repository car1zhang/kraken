package jobs

// Contract slice 1 (merge bump): pins the two pure functions behind the
// daemon's per-merge notice to a live cell's zeus — mergeBumpMessage's
// wording obligations and mergeBumpWanted's state × liveness gate.
// trackPRs itself shells out to tmux and is reviewed, not tested.

import (
	"strings"
	"testing"
)

const contractMergeURL = "https://github.com/car1zhang/kraken/pull/311"

func TestContractMergeBumpMessageWording(t *testing.T) {
	msg := mergeBumpMessage(contractMergeURL)
	if !strings.HasPrefix(msg, prWatchPrefix+" ") {
		t.Errorf("message does not begin with prWatchPrefix + space: %q", msg)
	}
	for _, want := range []string{
		contractMergeURL,
		"MERGED",
		"do not push to it",
		"origin/main",
		"awaiting checks",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q: %q", want, msg)
		}
	}
	if strings.Contains(msg, "\n") {
		t.Errorf("message contains a newline: %q", msg)
	}
}

func TestContractMergeBumpMessageDeterministic(t *testing.T) {
	a := mergeBumpMessage(contractMergeURL)
	b := mergeBumpMessage(contractMergeURL)
	if a != b {
		t.Errorf("repeated calls differ:\n%q\n%q", a, b)
	}
}

func TestContractMergeBumpMessageDistinctPerURL(t *testing.T) {
	other := "https://github.com/car1zhang/kraken/pull/312"
	a := mergeBumpMessage(contractMergeURL)
	b := mergeBumpMessage(other)
	if a == b {
		t.Errorf("two different urls yield the same message: %q", a)
	}
	if !strings.Contains(b, other) {
		t.Errorf("message for %q does not carry it verbatim: %q", other, b)
	}
}

func TestContractMergeBumpWanted(t *testing.T) {
	mk := func(state string) *Job {
		return &Job{Slug: "merge-bump-demo", ID: 42, State: state}
	}
	liveFor := func(j *Job, v bool) map[string]bool {
		return map[string]bool{CellSession(j): v}
	}
	cases := []struct {
		name  string
		state string
		live  func(j *Job) map[string]bool
		want  bool
	}{
		{"working+live", "working", func(j *Job) map[string]bool { return liveFor(j, true) }, true},
		{"blocked+live", "blocked", func(j *Job) map[string]bool { return liveFor(j, true) }, true},
		{"draft+live", "draft", func(j *Job) map[string]bool { return liveFor(j, true) }, false},
		{"done+live", "done", func(j *Job) map[string]bool { return liveFor(j, true) }, false},
		{"empty-state+live", "", func(j *Job) map[string]bool { return liveFor(j, true) }, false},
		{"working+nil-map", "working", func(*Job) map[string]bool { return nil }, false},
		{"blocked+nil-map", "blocked", func(*Job) map[string]bool { return nil }, false},
		{"working+key-missing", "working", func(*Job) map[string]bool {
			return map[string]bool{"cell-some-other-job-7": true}
		}, false},
		{"working+key-false", "working", func(j *Job) map[string]bool { return liveFor(j, false) }, false},
		{"blocked+key-false", "blocked", func(j *Job) map[string]bool { return liveFor(j, false) }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			j := mk(tc.state)
			if got := mergeBumpWanted(j, tc.live(j)); got != tc.want {
				t.Errorf("mergeBumpWanted(state=%q) = %v, want %v", tc.state, got, tc.want)
			}
		})
	}
}

// The liveness key is the job's OWN cell session: a live session belonging to
// a different generation of the same slug (different id) must not count.
func TestContractMergeBumpWantedKeysOnOwnCellSession(t *testing.T) {
	j := &Job{Slug: "merge-bump-demo", ID: 42, State: "working"}
	sibling := &Job{Slug: "merge-bump-demo", ID: 43, State: "working"}
	if CellSession(j) == CellSession(sibling) {
		t.Skip("CellSession does not distinguish ids; premise of this test does not hold")
	}
	live := map[string]bool{CellSession(sibling): true}
	if mergeBumpWanted(j, live) {
		t.Errorf("mergeBumpWanted true on a sibling generation's live session %q", CellSession(sibling))
	}
}
