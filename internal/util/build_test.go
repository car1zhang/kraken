package util

import "testing"

func TestBuild(t *testing.T) {
	// The ldflag can't be set from a test, so drive both branches by
	// assigning the var directly (restored after).
	orig := BuildSHA
	defer func() { BuildSHA = orig }()

	BuildSHA = ""
	if got := Build(); got != "dev" {
		t.Errorf("unstamped: got %q, want %q", got, "dev")
	}

	BuildSHA = "abc1234"
	if got := Build(); got != "abc1234" {
		t.Errorf("stamped: got %q, want %q", got, "abc1234")
	}
}
