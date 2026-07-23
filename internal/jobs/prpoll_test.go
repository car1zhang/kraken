package jobs

import (
	"reflect"
	"testing"
)

// prpoll_test.go — needsPoll's decision of which recorded PR URLs deserve an
// individual gh pr view, table-tested independent of gh.

func TestNeedsPoll(t *testing.T) {
	pr := func(n, state string) JobPR { return JobPR{URL: "https://x/pull/" + n, State: state} }
	cases := []struct {
		name     string
		recorded []JobPR
		fetched  []JobPR
		want     []string
	}{
		{
			name:     "open and not in fetched needs a poll",
			recorded: []JobPR{pr("1", "open")},
			fetched:  nil,
			want:     []string{"https://x/pull/1"},
		},
		{
			name:     "merged is never polled, even absent from fetched",
			recorded: []JobPR{pr("1", "merged")},
			fetched:  nil,
			want:     nil,
		},
		{
			name:     "open already present in fetched needs no poll",
			recorded: []JobPR{pr("1", "open")},
			fetched:  []JobPR{pr("1", "open")},
			want:     nil,
		},
		{
			name:     "closed and absent from fetched still needs a poll",
			recorded: []JobPR{pr("1", "closed")},
			fetched:  nil,
			want:     []string{"https://x/pull/1"},
		},
		{
			name:     "mixed set only polls what's missing and non-terminal",
			recorded: []JobPR{pr("1", "open"), pr("2", "merged"), pr("3", "open")},
			fetched:  []JobPR{pr("1", "open")},
			want:     []string{"https://x/pull/3"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := needsPoll(c.recorded, c.fetched)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}
