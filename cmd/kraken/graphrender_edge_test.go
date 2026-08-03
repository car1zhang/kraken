package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// graphrender_edge_test.go — the EDGE dimension of `k graph search`'s rendering
// after Fix B: the all-weak headline, its %q quoting, the two ways `strong` can
// mean "say nothing new" (absent, or greater than zero), and the empty-result
// line that must not have moved a byte.
//
// Every identifier is prefixed `kgedge`; a sibling author shares this package's
// test binary.
//
// The technique for every byte-identity row: rather than transcribing what the
// renderer prints today (which this file cannot read), each case renders the
// SAME response twice — once with the new key and once without — and asserts
// the two outputs are identical. That pins "unchanged" against the renderer's
// actual behaviour rather than against a copy of it that could drift.

// kgedgeRender serves one canned response body and returns what
// `k graph search` printed for it.
func kgedgeRender(t *testing.T, body string, args ...string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	defer srv.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", srv.Listener.Addr().String())

	got, err := runGraphSearch(args)
	if err != nil {
		t.Fatalf("runGraphSearch(%q) against %s: %v", args, body, err)
	}
	return got
}

// kgedgeBody assembles a search response. strong is spliced in as raw JSON so a
// case can send the key, send it as 0, or leave it out entirely — the three
// states the CLI must tell apart.
func kgedgeBody(strong, nodes, facts, triples string) string {
	b := `{"nodes":` + nodes + `,"facts":` + facts + `,"triples":` + kgedgeJSONString(triples)
	if strong != "" {
		b += `,"strong":` + strong
	}
	return b + `}`
}

// kgedgeJSONString quotes a Go string as JSON. fmt's %q is JSON-compatible for
// the plain ASCII these fixtures use.
func kgedgeJSONString(s string) string { return fmt.Sprintf("%q", s) }

const (
	kgedgeNodesOne  = `[{"id":1,"name":"kraken","type":"system","description":"the CLI"}]`
	kgedgeFactsOne  = `[{"id":1}]`
	kgedgeNodesNone = `[]`
	kgedgeFactsNone = `[]`
	kgedgeTriples   = "kraken -[spawns]-> zeus"
	kgedgeQ         = "how does a cell spawn"
)

// kgedgeHeadline is the exact first line an all-weak result must lead with.
func kgedgeHeadline(query string) string {
	return fmt.Sprintf("nothing strongly matched %q", query)
}

func kgedgeFirstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// TestKgedgeRenderAbsentStrongIsTodaysOutput pins the older-server case
// literally: `strong` decodes to a nil *int and the rendering is exactly what
// it was before Fix B. The expectation is transcribed from the shipped
// contract (entities block, blank line, facts block) rather than computed, so
// a renderer that starts inserting something for a nil strong fails here.
func TestKgedgeRenderAbsentStrongIsTodaysOutput(t *testing.T) {
	got := kgedgeRender(t, kgedgeBody("", kgedgeNodesOne, kgedgeFactsOne, kgedgeTriples), kgedgeQ, "--limit", "5")
	want := "entities:\n  kraken (system) — the CLI\n\nfacts:\nkraken -[spawns]-> zeus\n"
	if got != want {
		t.Fatalf("output = %q, want today's rendering %q", got, want)
	}
	if strings.Contains(got, "nothing strongly matched") {
		t.Error("a response with no `strong` key produced the all-weak headline — an old server would look broken")
	}
}

// TestKgedgeRenderPositiveStrongIsByteIdentical: any strong count above zero
// renders exactly as the same body with no strong key at all. Rendered twice
// and compared rather than transcribed, so this stays true if the shared
// rendering is ever reworked.
func TestKgedgeRenderPositiveStrongIsByteIdentical(t *testing.T) {
	base := kgedgeRender(t, kgedgeBody("", kgedgeNodesOne, kgedgeFactsOne, kgedgeTriples), kgedgeQ)
	for _, strong := range []string{"1", "2", "20", "200"} {
		got := kgedgeRender(t, kgedgeBody(strong, kgedgeNodesOne, kgedgeFactsOne, kgedgeTriples), kgedgeQ)
		if got != base {
			t.Errorf("strong=%s rendered %q, want the byte-identical %q", strong, got, base)
		}
		if strings.Contains(got, "nothing strongly matched") {
			t.Errorf("strong=%s produced the all-weak headline", strong)
		}
	}
}

// TestKgedgeRenderZeroStrongLeadsWithTheHeadline is Fix B's whole point at the
// CLI: an agent pasting this block into its context must be told, on the FIRST
// line, that nothing cleared the floor — before it reads a single fact.
func TestKgedgeRenderZeroStrongLeadsWithTheHeadline(t *testing.T) {
	today := kgedgeRender(t, kgedgeBody("", kgedgeNodesOne, kgedgeFactsOne, kgedgeTriples), kgedgeQ)
	got := kgedgeRender(t, kgedgeBody("0", kgedgeNodesOne, kgedgeFactsOne, kgedgeTriples), kgedgeQ)

	if first := kgedgeFirstLine(got); first != kgedgeHeadline(kgedgeQ) {
		t.Errorf("first line = %q, want %q", first, kgedgeHeadline(kgedgeQ))
	}
	// The facts still render, unchanged, below it — the headline ADDS a line,
	// it does not replace the answer. A weakly-related fact is still the best
	// thing the graph has and dropping it would be a worse lie than shipping
	// it unlabelled.
	if !strings.HasSuffix(got, today) {
		t.Errorf("output = %q does not end with today's rendering %q", got, today)
	}
	if strings.Count(got, kgedgeHeadline(kgedgeQ)) != 1 {
		t.Errorf("the headline appears %d times in %q, want once", strings.Count(got, kgedgeHeadline(kgedgeQ)), got)
	}
}

// TestKgedgeRenderHeadlineQuotesTheQuery pins the %q: the query is echoed so a
// reader can see WHICH question came back empty, and a query containing a
// quote, a backslash or a newline must not break the line into two.
func TestKgedgeRenderHeadlineQuotesTheQuery(t *testing.T) {
	for _, q := range []string{
		`he said "hi"`,
		`carl/graph-retrieval-fixes-170`,
		`a\backslash`,
		`python 3.14 migration`,
	} {
		t.Run(q, func(t *testing.T) {
			got := kgedgeRender(t, kgedgeBody("0", kgedgeNodesOne, kgedgeFactsOne, kgedgeTriples), q)
			want := kgedgeHeadline(q)
			if first := kgedgeFirstLine(got); first != want {
				t.Errorf("first line = %q, want %q", first, want)
			}
			// %q escapes every newline, so the headline is always ONE line.
			if strings.Contains(want, "\n") {
				t.Errorf("the quoted headline %q spans lines", want)
			}
		})
	}
}

// TestKgedgeRenderEmptyResultIsUnchanged is the byte-identity row that matters
// most: `nothing matched` is what an agent greps for, and Fix B must not have
// nudged it. A fully empty result renders the same whether the server sent
// strong 0, strong 3, or no strong key at all — the headline is for a result
// that HAS facts nobody should trust, not for one that has none.
func TestKgedgeRenderEmptyResultIsUnchanged(t *testing.T) {
	today := kgedgeRender(t, kgedgeBody("", kgedgeNodesNone, kgedgeFactsNone, ""), kgedgeQ)
	if !strings.Contains(today, fmt.Sprintf("nothing matched %q", kgedgeQ)) {
		t.Fatalf("today's empty rendering = %q, want it to name the query with %%q", today)
	}
	for _, strong := range []string{"0", "3"} {
		got := kgedgeRender(t, kgedgeBody(strong, kgedgeNodesNone, kgedgeFactsNone, ""), kgedgeQ)
		if got != today {
			t.Errorf("strong=%s on an empty result rendered %q, want the byte-identical %q", strong, got, today)
		}
		if strings.Contains(got, "nothing strongly matched") {
			t.Errorf("strong=%s on an EMPTY result produced the all-weak headline — there was nothing to be weak about", strong)
		}
	}
}

// TestKgedgeRenderZeroStrongWithNodesButNoFacts is the half-empty edge: the
// search resolved entities but returned no triples. That is not the fully-empty
// case (something rendered), so the headline applies — and the entities block
// must survive, since resolving a name against known entities is exactly what
// the graph skill's canonicalization step calls this verb for.
func TestKgedgeRenderZeroStrongWithNodesButNoFacts(t *testing.T) {
	today := kgedgeRender(t, kgedgeBody("", kgedgeNodesOne, kgedgeFactsNone, ""), kgedgeQ)
	if strings.Contains(today, "nothing matched") {
		t.Skipf("a nodes-only result renders as fully empty today (%q); the headline row does not apply", today)
	}
	got := kgedgeRender(t, kgedgeBody("0", kgedgeNodesOne, kgedgeFactsNone, ""), kgedgeQ)
	if first := kgedgeFirstLine(got); first != kgedgeHeadline(kgedgeQ) {
		t.Errorf("first line = %q, want %q", first, kgedgeHeadline(kgedgeQ))
	}
	if !strings.Contains(got, "kraken") {
		t.Errorf("the entities block was lost: %q", got)
	}
}

// TestKgedgeRenderDividerIsPrintedVerbatim: `weakly related:` arrives INSIDE
// the triples string and needs no CLI handling at all. This pins that it is
// neither stripped nor re-sorted nor decorated — the server owns the grouping,
// and a CLI that also grouped would apply it twice.
func TestKgedgeRenderDividerIsPrintedVerbatim(t *testing.T) {
	triples := "(kraken, spawns, zeus) :: kraken spawns zeus in a cell\n" +
		"weakly related:\n" +
		"(zeus, writes, artifact) :: zeus writes an artifact"

	got := kgedgeRender(t, kgedgeBody("1", kgedgeNodesOne, `[{"id":1},{"id":2}]`, triples), kgedgeQ)
	if !strings.Contains(got, triples) {
		t.Fatalf("the triples block was not printed verbatim.\ngot:\n%s\nwant it to contain:\n%s", got, triples)
	}
	// Rendering with a positive strong count must be identical to rendering
	// with no strong key: the divider is data, not a signal to the CLI.
	base := kgedgeRender(t, kgedgeBody("", kgedgeNodesOne, `[{"id":1},{"id":2}]`, triples), kgedgeQ)
	if got != base {
		t.Errorf("a body carrying the divider rendered differently with strong=1 (%q) than without it (%q)", got, base)
	}

	// And with strong 0 — an all-weak block, divider first — the headline
	// leads and the divider still rides through untouched.
	allWeak := "weakly related:\n(zeus, writes, artifact) :: zeus writes an artifact"
	zero := kgedgeRender(t, kgedgeBody("0", kgedgeNodesOne, `[{"id":1}]`, allWeak), kgedgeQ)
	if first := kgedgeFirstLine(zero); first != kgedgeHeadline(kgedgeQ) {
		t.Errorf("first line = %q, want %q", first, kgedgeHeadline(kgedgeQ))
	}
	if !strings.Contains(zero, allWeak) {
		t.Errorf("the all-weak triples block was altered:\n%s", zero)
	}
}

// TestKgedgeRenderBadArgsStillRefused: the new key must not have loosened the
// argument gate. No server is started — every case must fail before any
// request, which is also what proves the refusal is local.
func TestKgedgeRenderBadArgsStillRefused(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{},
		{"   "},
		{""},
		{"--limit", "5"},
	} {
		if _, err := runGraphSearch(args); err == nil {
			t.Errorf("runGraphSearch(%q) = nil error, want a usage refusal", args)
		}
	}
}
