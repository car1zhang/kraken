package main

import (
	"strings"
	"testing"
)

// fleet_edge_test.go — the EDGE dimension of 50-contract.md slice G5 for
// package main: splitNodeSelector's refusals (no `/`, an unknown or empty
// left half, a too-short id prefix, an empty registry), its split on the
// FIRST `/` only, and renderNodesTable's empty registry. Written blind from
// the contract: cmd_nodes.go, cmd_actions.go and cmd_read.go were not read.
// Happy paths are athena-logic's.
//
// Every package-level identifier is prefixed `edgeFleet`.

func edgeFleetRegistry() []nodeRowWire {
	return []nodeRowWire{
		{NodeID: "nea6fba5q821CNTRL", Label: "Studio-Mini", DNSName: "studio-mini.tail072ad8.ts.net"},
		{NodeID: "nzz91kk2CNTRL", Label: "Carl's Macbook", DNSName: "carls-macbook-bro.tail072ad8.ts.net"},
	}
}

// "A selector without `/` … returns `ok == false`".
func TestEdgeFleetSplitSelectorWithoutSlash(t *testing.T) {
	for _, sel := range []string{"studio-mini", "tech-comms-rollout", "3", ".", "%3", "nea6"} {
		if n, slug, ok := splitNodeSelector(sel, edgeFleetRegistry()); ok {
			t.Errorf("splitNodeSelector(%q) = (%+v, %q, true), want ok=false", sel, n, slug)
		}
	}
}

// "or whose left half matches no node, returns `ok == false`" — including an
// empty left half and an empty registry.
func TestEdgeFleetSplitSelectorUnknownLeftHalf(t *testing.T) {
	for _, sel := range []string{"unknown-node/slug", "/slug", "studio/slug", "tail072ad8/slug", "studio-mini.tail072ad8.ts.net/slug"} {
		if n, slug, ok := splitNodeSelector(sel, edgeFleetRegistry()); ok {
			t.Errorf("splitNodeSelector(%q) = (%+v, %q, true), want ok=false", sel, n, slug)
		}
	}
	if _, _, ok := splitNodeSelector("studio-mini/slug", nil); ok {
		t.Errorf("splitNodeSelector with an empty registry said ok")
	}
}

// "a prefix (≥4 chars) of its `NodeID`" — three characters is not enough.
func TestEdgeFleetSplitSelectorIDPrefixMinimum(t *testing.T) {
	if _, _, ok := splitNodeSelector("nea/slug", edgeFleetRegistry()); ok {
		t.Errorf("a 3-char id prefix matched")
	}
	n, slug, ok := splitNodeSelector("nea6/slug", edgeFleetRegistry())
	if !ok || n == nil || n.NodeID != "nea6fba5q821CNTRL" || slug != "slug" {
		t.Errorf("4-char id prefix: (%+v, %q, %v), want the studio node and slug", n, slug, ok)
	}
}

// "the text before the FIRST `/` … `slug` is everything after that `/`" —
// with the Label matched case-insensitively.
func TestEdgeFleetSplitSelectorFirstSlashOnly(t *testing.T) {
	n, slug, ok := splitNodeSelector("STUDIO-MINI/a/b/c", edgeFleetRegistry())
	if !ok || n == nil {
		t.Fatalf("splitNodeSelector(STUDIO-MINI/a/b/c) ok=false")
	}
	if n.NodeID != "nea6fba5q821CNTRL" {
		t.Errorf("node = %+v, want the studio node", n)
	}
	if slug != "a/b/c" {
		t.Errorf("slug = %q, want a/b/c", slug)
	}
}

// "the first label of its `DNSName`" — the DNS label, not the Label field,
// reaches the second node.
func TestEdgeFleetSplitSelectorDNSFirstLabel(t *testing.T) {
	n, slug, ok := splitNodeSelector("carls-macbook-bro/fix-it", edgeFleetRegistry())
	if !ok || n == nil || n.NodeID != "nzz91kk2CNTRL" || slug != "fix-it" {
		t.Errorf("DNS first label: (%+v, %q, %v), want the macbook node", n, slug, ok)
	}
}

// `renderNodesTable`: "`no nodes on the tailnet` when empty" — and an empty
// registry prints no row data.
func TestEdgeFleetRenderNodesTableEmpty(t *testing.T) {
	for _, resp := range []nodesResponse{{}, {Nodes: []nodeRowWire{}}} {
		out := renderNodesTable(resp)
		if !strings.Contains(out, "no nodes on the tailnet") {
			t.Errorf("empty registry rendered %q, want the empty-registry line", out)
		}
	}
}
