package core

import "testing"

// kronos_contract_test.go — what survives of athena-logic's blind contract
// suite for the kronos build's core slice. The slice was the iMessage
// bridge's pure half plus KronosModel; the bridge was removed whole at the
// manual gate (carl's call — the board panel is the channel for now), and its
// tests were deliberately obsoleted with it. The model pin is the one
// obligation that outlived the bridge.

// The contract freezes the exact id, never an alias — an alias silently
// follows whatever claude ships next, which is the drift the model-policy
// section exists to prevent. (Whether it EQUALS IrisModel/ZeusModel today is
// deliberately not asserted in either direction; see branch.go's rationale.)
func TestContractKronosModelIsTheExactID(t *testing.T) {
	if KronosModel != "claude-fable-5" {
		t.Fatalf("KronosModel = %q, want %q", KronosModel, "claude-fable-5")
	}
}
