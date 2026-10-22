package core

import (
	"os"
	"strings"
	"testing"
)

// operator_seed_test.go pins the text rung against the board's two ask-thread
// seeds. The seed builders live in web/_ui/src/components/overlays/askThread.ts
// (askThreadSeed, questionThreadSeed), which Go cannot import. The first test
// reads that file and requires every string the reader keys on to still be in
// it verbatim, so a seed edit fails here; the hand-copied builders below are
// fixtures for the unwrap cases.

// askThreadTS is the seed source, relative to this package's directory.
const askThreadTS = "../../web/_ui/src/components/overlays/askThread.ts"

func TestOperatorSeedStringsMatchWebSource(t *testing.T) {
	b, err := os.ReadFile(askThreadTS)
	if err != nil {
		t.Fatalf("cannot read the board's seed source %s: %v", askThreadTS, err)
	}
	src := string(b)
	// The TS source spells newlines as `\n` escapes, so each key is
	// compared without its leading or trailing newlines.
	for _, key := range []string{
		operatorArtifactSeedOpen,
		operatorArtifactSeedMark,
		operatorQuestionSeedOpen,
		operatorQuestionSeedDefault,
		"Answer here in the thread —",
	} {
		if !strings.Contains(src, strings.Trim(key, "\n")) {
			t.Errorf("%s no longer contains %q verbatim — the operator reader keys on it (internal/core/operator.go)", askThreadTS, key)
		}
	}
}

// webAskThreadSeed mirrors askThread.ts' askThreadSeed.
func webAskThreadSeed(artifact, question string) string {
	return "I'm reading the artifact `" +
		artifact +
		"` from this job's artifact set. A question about it:\n\n" +
		strings.TrimSpace(question) +
		"\n\nAnswer here in the thread — this is a question about the work, " +
		"not a request to change it. Don't edit the artifact."
}

// webQuestionThreadSeed mirrors askThread.ts' questionThreadSeed.
func webQuestionThreadSeed(id, importance, text, context, recommendation, ask string) string {
	if importance == "" {
		importance = "P2"
	}
	row := id + " (" + importance + "): " + strings.TrimSpace(text)
	if strings.TrimSpace(context) != "" {
		row += "\ncontext: " + strings.TrimSpace(context)
	}
	if strings.TrimSpace(recommendation) != "" {
		row += "\nrecommendation: " + strings.TrimSpace(recommendation)
	}
	want := strings.TrimSpace(ask)
	if want == "" {
		want = "Help me understand this question: what is it really asking, what context do I need to answer it, and what hinges on the answer?"
	}
	return "I'm looking at an open question this job raised on the board. The row as filed:\n\n" +
		row +
		"\n\n" +
		want +
		"\n\nAnswer here in the thread — this is a question about the question, " +
		"not an answer to it. Don't write into the questions record or change " +
		"any files; I'll answer on the board myself once it's clear."
}

func seedTyped() operatorLine {
	var rec operatorLine
	rec.Type, rec.PromptSource, rec.Origin.Kind = "user", "typed", "human"
	return rec
}

func TestOperatorTextUnwrapsBoardSeeds(t *testing.T) {
	cases := []struct {
		name, raw, want string
		drop            bool
	}{
		{name: "artifact seed", raw: webAskThreadSeed("50-contract.md", "why is the cap 20?"), want: "why is the cap 20?"},
		{name: "artifact seed, two paragraphs", raw: webAskThreadSeed("00-overview.md", "first thing.\n\nsecond thing."), want: "first thing.\n\nsecond thing."},
		{name: "question seed, full row", raw: webQuestionThreadSeed("Q3", "P1", "Should do's step 2 use the block protocol?", "AskUserQuestion was used 0 times.", "Yes, same protocol: stop.", "what exactly is pre-build p0 resolution?"), want: "what exactly is pre-build p0 resolution?"},
		{name: "question seed, bare row", raw: webQuestionThreadSeed("Q1", "", "Cap at 20?", "", "", "why 20 and not 10?"), want: "why 20 and not 10?"},
		{name: "question seed, context only", raw: webQuestionThreadSeed("Q2", "P0", "Which key?", "the id or the slug", "", "the id, obviously"), want: "the id, obviously"},
		{name: "question seed, default ask", raw: webQuestionThreadSeed("Q3", "P1", "Should it block?", "ctx", "Yes.", ""), drop: true},
	}
	for _, c := range cases {
		got, _, ok := operatorText(c.raw, seedTyped(), "Carl Zhang")
		if c.drop {
			if ok {
				t.Errorf("%s: kept %q, want the record dropped", c.name, got)
			}
			continue
		}
		if !ok || got != c.want {
			t.Errorf("%s: got %q (ok=%v), want %q", c.name, got, ok, c.want)
		}
	}
}

// The other r3 text-rung shapes, each reproduced on a real transcript.
func TestOperatorTextReviewShapes(t *testing.T) {
	pasted := func(s string) string {
		return "\n\n<pasted_content id=\"117e\">\n" + s + "\n</pasted_content id=\"117e\">"
	}
	cases := []struct {
		name, raw, want string
		drop            bool
	}{
		{name: "pasted pr-watch", raw: pasted("[kraken pr-watch — automated notice, not a reply from Carl Zhang] https://x/pull/1 has MERGED"), drop: true},
		{name: "pasted operator words kept whole", raw: pasted("> quoted\n\nthese are genuine."), want: "<pasted_content id=\"117e\">\n> quoted\n\nthese are genuine.\n</pasted_content id=\"117e\">"},
		{name: "empty paste", raw: pasted("  "), drop: true},
		{name: "mid-sentence slash command", raw: "just /handoff to a new cell", want: "just /handoff to a new cell"},
		{name: "slash command on its own line", raw: "ok\n/compact keep it short", want: "ok"},
		{name: "glued bump still cut", raw: "apply it there too.[kraken pr-watch — automated notice] merged", want: "apply it there too."},
		{name: "bracket without a stamp", raw: "[from memory] the gate is per-cell", want: "[from memory] the gate is per-cell"},
		{name: "capture seed", raw: "Carl Zhang highlighted this passage and sent it to you to become an item in their mental model:\n\n<<<passage\nagent prose\npassage>>>", drop: true},
		{name: "pasted capture follow-up", raw: pasted("Carl Zhang highlighted another passage for their mental model:\n\n<<<passage\nx\npassage>>>"), drop: true},
		{name: "rebase note alone", raw: "your branch kraken/x-1 is behind origin/main. first run: git fetch origin && git rebase origin/main — then continue.", drop: true},
		{name: "rebase note with a steer", raw: "your branch kraken/x-1 is behind origin/main. first run: …\nreorient text\nsteering from Carl Zhang: ship it", want: "ship it"},
	}
	for _, c := range cases {
		got, _, ok := operatorText(c.raw, seedTyped(), "Carl Zhang")
		if c.drop {
			if ok {
				t.Errorf("%s: kept %q, want the record dropped", c.name, got)
			}
			continue
		}
		if !ok || got != c.want {
			t.Errorf("%s: got %q (ok=%v), want %q", c.name, got, ok, c.want)
		}
	}
}
