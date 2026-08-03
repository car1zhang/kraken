package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// graphrender_contract_test.go — the CORRECTNESS pins for slice S1d of
// artifacts/50-contract.md: what `k graph search` prints once the server
// started reporting how many facts actually cleared the relevance bar.
//
// The verb is a thin client, so every case here drives it against a canned
// server body. That is the right altitude for this slice: S1c already pins
// what the SERVER computes against a real graph, and what S1d owns is the one
// decision the CLI makes on top of it — whether to lead with a warning line —
// plus the much larger promise that it changed NOTHING else.
//
// "Nothing else" is asserted by DIFFING the verb against itself: the same body
// with and without the new key must render identically, and the all-weak
// rendering must be exactly the warning line prepended to that. Comparing the
// renderer to itself rather than to a transcribed expectation means the
// assertion survives an unrelated formatting change while still catching the
// regression it exists to catch — a `strong` key that quietly reshuffles the
// output for every caller, including the old servers that never send one.

// kgcontractServeBody stands a server up that answers every request with body
// and points the CLI at it.
func kgcontractServeBody(t *testing.T, body string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("KRAKEN_SERVE_ADDR", srv.Listener.Addr().String())
}

// kgcontractRender runs one search against a canned body and returns stdout.
func kgcontractRender(t *testing.T, body, query string) string {
	t.Helper()
	kgcontractServeBody(t, body)
	got, err := runGraphSearch([]string{query})
	if err != nil {
		t.Fatalf("runGraphSearch(%q) against %s: %v", query, body, err)
	}
	return got
}

// The canonical non-empty response, in three variants that differ ONLY in the
// new key. Its rendering with no `strong` at all is pinned character for
// character by the pre-existing cmd_graph_test.go, which makes it the right
// body to diff the other two against.
const (
	kgcontractBodyNoStrong = `{"nodes":[{"id":1,"name":"kraken","type":"system","description":"the CLI"}],` +
		`"facts":[{"id":1}],"triples":"kraken -[spawns]-> zeus"}`
	kgcontractBodyStrongZero = `{"nodes":[{"id":1,"name":"kraken","type":"system","description":"the CLI"}],` +
		`"facts":[{"id":1}],"triples":"kraken -[spawns]-> zeus","strong":0}`
	kgcontractBodyStrongTwo = `{"nodes":[{"id":1,"name":"kraken","type":"system","description":"the CLI"}],` +
		`"facts":[{"id":1}],"triples":"kraken -[spawns]-> zeus","strong":2}`
	kgcontractBodyEmpty = `{"nodes":[],"facts":[],"triples":""}`
)

// TestKgcontractGraphSearchRespStrongIsAPointer pins the DECODING half of
// S1d's first row. A plain `int` would decode a missing key to 0, which is the
// one value that changes the output — every response from a server predating
// this slice would print the no-strong-match warning. The distinction is
// nil-versus-zero and it has to live in the type.
//
// Checked by json tag rather than by field name so this asserts the wire
// contract and not a spelling.
func TestKgcontractGraphSearchRespStrongIsAPointer(t *testing.T) {
	rt := reflect.TypeOf(graphSearchResp{})
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name != "strong" {
			continue
		}
		if f.Type.Kind() != reflect.Pointer || f.Type.Elem().Kind() != reflect.Int {
			t.Fatalf(`the "strong" field is %s, want *int — an absent key must stay distinguishable from 0`, f.Type)
		}
		return
	}
	t.Fatal(`graphSearchResp declares no field tagged "strong"`)
}

// TestKgcontractGraphSearchLeadsWithNothingStronglyMatched is the headline of
// Fix B: a corpus that answered with nothing but weak facts says so FIRST,
// before the facts a reader would otherwise take at face value. The eval's
// "python 3.14 migration" probe is exactly this case, so it is the query used.
//
// The expected line is Go's %q of the query, which is what makes a query
// containing a quote or a newline still print as one readable line.
func TestKgcontractGraphSearchLeadsWithNothingStronglyMatched(t *testing.T) {
	const query = "python 3.14 migration"

	got := kgcontractRender(t, kgcontractBodyStrongZero, query)
	want := fmt.Sprintf("nothing strongly matched %q", query)

	first, rest, found := strings.Cut(got, "\n")
	if !found {
		t.Fatalf("output has no second line: %q", got)
	}
	if first != want {
		t.Fatalf("first line = %q, want %q", first, want)
	}

	// "followed by the entities/facts rendering as today": the remainder is
	// byte-for-byte what the SAME body renders as without the key.
	baseline := kgcontractRender(t, kgcontractBodyNoStrong, query)
	if rest != baseline {
		t.Errorf("everything after the warning line =\n%q\nwant the unchanged rendering\n%q", rest, baseline)
	}
}

// TestKgcontractGraphSearchRendersUnchangedOtherwise covers the two states
// that must look exactly like they did before this slice: an old server that
// sends no `strong` at all, and a new one reporting that something did match.
//
// The no-strong rendering is additionally pinned to its literal text — the
// same string cmd_graph_test.go asserts — so "unchanged" is anchored to a real
// expectation and not merely to self-consistency.
func TestKgcontractGraphSearchRendersUnchangedOtherwise(t *testing.T) {
	const query = "how does a cell spawn"
	const literal = "entities:\n  kraken (system) — the CLI\n\nfacts:\nkraken -[spawns]-> zeus\n"

	older := kgcontractRender(t, kgcontractBodyNoStrong, query)
	if older != literal {
		t.Fatalf("a response with no `strong` key rendered as %q, want today's output %q", older, literal)
	}

	newer := kgcontractRender(t, kgcontractBodyStrongTwo, query)
	if newer != older {
		t.Errorf("strong=2 rendered as %q, want byte-identical output to the same body without the key (%q)", newer, older)
	}
	if strings.Contains(newer, "nothing strongly matched") {
		t.Errorf("a response reporting 2 strong facts printed the no-strong-match line: %q", newer)
	}
}

// TestKgcontractGraphSearchEmptyResultLineIsByteIdentical: a genuinely empty
// answer keeps its old sentence. The two lines say different things — "there
// is nothing here" versus "here are some facts, none of them a real match" —
// and collapsing them would lose the distinction the whole fix exists to draw.
//
// The strong:0 variant is the load-bearing case: the warning line is
// conditioned on a NON-EMPTY rendering, so an empty result carrying strong:0
// must still print `nothing matched`.
func TestKgcontractGraphSearchEmptyResultLineIsByteIdentical(t *testing.T) {
	const query = "sasquatch telemetry"
	want := fmt.Sprintf("nothing matched %q", query)

	for _, c := range []struct{ name, body string }{
		{"no strong key", kgcontractBodyEmpty},
		{"strong 0", `{"nodes":[],"facts":[],"triples":"","strong":0}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := kgcontractRender(t, c.body, query)
			if strings.TrimRight(got, "\n") != want {
				t.Errorf("output = %q, want exactly %q", got, want)
			}
			if strings.Contains(got, "nothing strongly matched") {
				t.Errorf("an empty result printed the all-weak line instead of the empty-result line: %q", got)
			}
		})
	}
}

// TestKgcontractGraphSearchPrintsTheWeaklyRelatedDividerVerbatim pins the last
// row of S1d, which is a promise about what the CLI does NOT do: the divider
// is a line inside the server's `triples` string, and the verb prints it the
// way it prints every other line there. No grouping, no indentation, no
// re-ordering — all of that already happened server-side, and a client that
// did it again would be a second place for the two to drift.
func TestKgcontractGraphSearchPrintsTheWeaklyRelatedDividerVerbatim(t *testing.T) {
	const triples = "(kraken, spawns, zeus) :: kraken spawns zeus in a cell\\n" +
		"weakly related:\\n" +
		"(zeus, rides, bicycle) :: zeus rides the bicycle downhill"
	body := `{"nodes":[{"id":1,"name":"kraken","type":"system","description":"the CLI"}],` +
		`"facts":[{"id":1},{"id":2}],"triples":"` + triples + `","strong":1}`

	got := kgcontractRender(t, body, "how does a cell spawn")

	// The server's block, decoded, must appear in the output as one contiguous
	// run of lines in the server's own order.
	block := strings.ReplaceAll(triples, "\\n", "\n")
	if !strings.Contains(got, block) {
		t.Fatalf("output does not carry the server's triples block verbatim.\ngot:\n%s\nwant it to contain:\n%s", got, block)
	}

	// Exactly one divider line, and it is that string alone on its line.
	dividers := 0
	for _, ln := range strings.Split(got, "\n") {
		if strings.Contains(ln, "weakly related") {
			if ln != "weakly related:" {
				t.Errorf("divider line = %q, want exactly %q — the CLI must not decorate it", ln, "weakly related:")
			}
			dividers++
		}
	}
	if dividers != 1 {
		t.Errorf("found %d divider lines, want 1", dividers)
	}
	// strong is 1, so no warning line: some fact did clear the bar.
	if strings.Contains(got, "nothing strongly matched") {
		t.Errorf("strong=1 printed the no-strong-match line:\n%s", got)
	}
}
