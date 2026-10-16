package jobs

import (
	"os"
	"strings"
)

// operatorMaxRunes bounds the operator's name: it is spliced into every
// brief, header and notice kraken writes, so an oversized value would bloat
// each of them rather than fail anywhere visible.
const operatorMaxRunes = 64

// operatorFallback is the name used when neither KRAKEN_OPERATOR nor USER
// says who runs this machine. It reads correctly in every sentence the name
// fills ("…automated notice, not a reply from the operator").
const operatorFallback = "the operator"

// Operator is the name of the person this kraken works for — the one who
// reads the board, answers questions and submits jobs — as it appears in
// every agent brief, context header and automated notice. It is
// KRAKEN_OPERATOR (normally written into ~/.kraken/env by `make install`),
// trimmed and capped at 64 runes; failing that the login name in USER;
// failing that "the operator". It is read on every call, so a test (or a
// changed env file after a restart) takes effect without caching.
func Operator() string {
	if name := strings.TrimSpace(os.Getenv("KRAKEN_OPERATOR")); name != "" {
		if r := []rune(name); len(r) > operatorMaxRunes {
			name = string(r[:operatorMaxRunes])
		}
		return name
	}
	if user := strings.TrimSpace(os.Getenv("USER")); user != "" {
		return user
	}
	return operatorFallback
}

// operatorPossessive is Operator() + "'s", used by the context headers
// (goals, notes, mm index). The briefs in crew.go and iris.go form the same
// possessive inline from a local `op`, by the same rule: always "'s", even
// after a name ending in "s".
func operatorPossessive() string {
	return Operator() + "'s"
}
