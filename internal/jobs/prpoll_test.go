package jobs

import (
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

// prpoll_test.go — needsPoll's decision of which recorded PR URLs deserve an
// individual gh pr view, table-tested independent of gh, plus execErrDetail's
// failure-log decoration.

// TestExecErrDetail covers the failure-log decoration: first stderr line for
// a command that ran and exited non-zero, the exec error itself otherwise,
// empty on nil, and always bounded to one clean line.
func TestExecErrDetail(t *testing.T) {
	if got := execErrDetail(nil); got != "" {
		t.Fatalf("nil error: got %q, want empty", got)
	}
	ee := &exec.ExitError{Stderr: []byte("GraphQL: API rate limit exceeded for user\nsecond line\n")}
	if got := execErrDetail(ee); got != "GraphQL: API rate limit exceeded for user" {
		t.Fatalf("exit error with stderr: got %q", got)
	}
	plain := errors.New("exec: \"gh\": executable file not found in $PATH")
	if got := execErrDetail(plain); got != plain.Error() {
		t.Fatalf("plain error: got %q, want %q", got, plain.Error())
	}
	long := &exec.ExitError{Stderr: []byte(strings.Repeat("x", 5000) + "\x1b[31m")}
	if got := execErrDetail(long); len(got) > 210 || strings.ContainsRune(got, '\x1b') {
		t.Fatalf("unbounded or uncleaned detail: len=%d", len(got))
	}
}

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
