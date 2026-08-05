package main

import (
	"os"
	"strings"
	"testing"
)

// TestMainDispatchesEveryHookVerb pins main.go's dispatch cases for the three
// hook verbs a cell's .claude/settings.json installs (jobs/cell.go's
// prHookCommand, limitHookCommand and recallHookCommand). The pin exists
// because this regression actually shipped: a refactor of the hook-verb
// region dropped `case "recall-hook"` while the build stayed green — nothing
// else references a dispatch case — leaving every installed recall-hook
// command dying on an unknown argument, once per prompt and per tool call,
// with no test anywhere noticing. A dropped case here is a feature silently
// dead in every cell, so the check is deliberately on the dispatch text
// itself rather than on the cmd functions existing (the functions survived
// that incident; the case did not).
func TestMainDispatchesEveryHookVerb(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, verb := range []string{"pr-hook", "limit-hook", "recall-hook"} {
		if !strings.Contains(string(src), `case "`+verb+`":`) {
			t.Errorf("main.go has no dispatch case for %q — every cell's settings.json installs a hook command that calls it", verb)
		}
	}
}
