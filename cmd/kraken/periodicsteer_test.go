package main

import "testing"

// periodicsteer_test.go — `k periodic add --every`, added at the manual-test
// gate. Written by the lead with the implementation in view.

func TestPeriodicSteerParseAddEvery(t *testing.T) {
	for _, args := range [][]string{
		{"pulse", "--prefix", "pulse", "--every", "2h"},
		{"pulse", "--every=2h", "--prefix=pulse", "--file", "t.md"},
	} {
		a, err := parsePeriodicAdd(args)
		if err != nil || a.name != "pulse" || a.prefix != "pulse" || a.every != "2h" || a.at != "" || a.days != "" {
			t.Errorf("parsePeriodicAdd(%v) = %+v, %v; want every 2h and no at or days", args, a, err)
		}
	}
	for _, args := range [][]string{
		{"pulse", "--prefix", "pulse", "--every", "2h", "--at", "09:00"},
		{"pulse", "--prefix", "pulse", "--every", "2h", "--days", "mon"},
		{"pulse", "--prefix", "pulse"},
		{"pulse", "--prefix", "pulse", "--every"},
		{"pulse", "--prefix", "pulse", "--every="},
	} {
		if a, err := parsePeriodicAdd(args); err == nil {
			t.Errorf("parsePeriodicAdd(%v) = %+v, want an error", args, a)
		}
	}
}
