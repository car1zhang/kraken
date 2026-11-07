package jobs

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"kraken/internal/store"
)

// mindrun_itemrefuse_test.go — an operation whose ITEM breaks its own rules
// is refused alone and the rest of the answer lands, while a defect the rest
// of the answer may depend on still refuses the whole answer. Written by the
// lead alongside the change (no blind author); identifiers are prefixed
// `itemRefuse`.

// itemRefuseLong is a title exactly one rune over the cap.
var itemRefuseLong = strings.Repeat("a", mmTitleMaxRunes+1)

// itemRefuseAdd is an add whose item carries the given title and a quote of
// its own.
func itemRefuseAdd(title, quote string) MindOp {
	it := opQuoteItem(title)
	it.Quote = quote
	return MindOp{Op: "add", Item: it}
}

// itemRefuseSupersede is a supersede of target whose item carries the given
// title and a quote of its own.
func itemRefuseSupersede(target int64, title, quote string) MindOp {
	it := opQuoteItem(title)
	it.Quote = quote
	return MindOp{Op: "supersede", Target: target, Item: it}
}

const itemRefuseReason = "title must be 1-120 runes, got 121"

func TestItemRefuseOverlongTitleIsRefusedAlone(t *testing.T) {
	st := contractMREnv(t)
	run := edgeMRDaily(t, st)
	before := len(edgeMRItems(t, st))
	ans := MindAnswer{Ops: []MindOp{
		edgeMRAdd("the first claim lands"),
		itemRefuseAdd(itemRefuseLong, "the claim behind the long title"),
		edgeMRAdd("the second claim lands too"),
	}}
	res, err := ApplyMindOps(st, run, ans, nil, time.Now().UnixMilli())
	if err != nil {
		t.Fatalf("ApplyMindOps = %v, want the over-long op refused alone", err)
	}
	if len(res.Results) != 3 {
		t.Fatalf("results = %+v, want three, one per op", res.Results)
	}
	if r := res.Results[1]; r.Outcome != "refused" || r.Index != 1 || r.Op != "add" || !strings.Contains(r.Reason, itemRefuseReason) {
		t.Errorf("op 1 = %+v, want refused with the title rule as its reason", r)
	}
	if res.Results[0].Outcome != "added" || res.Results[2].Outcome != "added" {
		t.Errorf("ops 0 and 2 = %q, %q, want both added", res.Results[0].Outcome, res.Results[2].Outcome)
	}
	if res.Counts.Added != 2 || res.Counts.Refused != 1 {
		t.Errorf("counts = %+v, want 2 added, 1 refused", res.Counts)
	}
	if after := len(edgeMRItems(t, st)); after != before+2 {
		t.Errorf("%d rows written, want 2", after-before)
	}
	report := MindRunReport(run, ans, res, MindManifest{})
	if !strings.Contains(report, "refused ("+itemRefuseReason+")") {
		t.Errorf("report does not name the refused op with its reason:\n%s", report)
	}
}

// A bad item never hides a bad target: the shape and target checks run
// first, and a supersede of a dead, missing, zero or code-reading target
// refuses the whole answer however bad its item is.
func TestItemRefuseBadItemWithBadTargetRefusesTheAnswer(t *testing.T) {
	st := contractMREnv(t)
	run := edgeMRDaily(t, st)
	dead := edgeMRAddItem(t, st, store.MMItem{Quote: "a claim that was dropped"})
	if err := st.InvalidateMMItem(dead, time.Now().UnixMilli(), "removed", ""); err != nil {
		t.Fatal(err)
	}
	code := edgeMRAddItem(t, st, store.MMItem{Kind: "reading", Source: "code", Quote: "func helper() {}"})
	targets := map[string]int64{"dead": dead, "code reading": code, "missing": 1 << 40, "zero": 0}
	for name, target := range targets {
		before := len(edgeMRItems(t, st))
		ans := MindAnswer{Ops: []MindOp{
			edgeMRAdd("a valid add ahead of the " + name + " target"),
			itemRefuseSupersede(target, itemRefuseLong, "replaces the "+name+" target"),
		}}
		_, err := ApplyMindOps(st, run, ans, nil, time.Now().UnixMilli())
		if err == nil || strings.Contains(err.Error(), "title must be") {
			t.Errorf("%s: ApplyMindOps = %v, want the whole answer refused on the target, not the title", name, err)
		}
		if after := len(edgeMRItems(t, st)); after != before {
			t.Errorf("%s: a refused answer wrote %d rows", name, after-before)
		}
	}
}

// A refused supersede claims nothing: a later retire of the same target is
// not a second claim and lands.
func TestItemRefuseRefusedSupersedeClaimsNothing(t *testing.T) {
	st := contractMREnv(t)
	run := edgeMRDaily(t, st)
	target := edgeMRAddItem(t, st, store.MMItem{Quote: "the plan as first stated"})
	ans := MindAnswer{Ops: []MindOp{
		itemRefuseSupersede(target, itemRefuseLong, "the plan restated at length"),
		{Op: "retire", Target: target, Quote: "the plan is dropped"},
	}}
	res, err := ApplyMindOps(st, run, ans, nil, time.Now().UnixMilli())
	if err != nil {
		t.Fatalf("ApplyMindOps = %v, want the refused supersede to claim nothing", err)
	}
	if len(res.Results) != 2 || res.Results[0].Outcome != "refused" || res.Results[1].Outcome != "retired" {
		t.Errorf("results = %+v, want refused then retired", res.Results)
	}
	if it := contractMRGetItem(t, st, target); it.InvalidReason != "outdated" {
		t.Errorf("target = %q, want retired as outdated by op 1", it.InvalidReason)
	}
}

// A refused supersede alone leaves its target live, inserts nothing, and the
// report names the target the reader would otherwise have to guess.
func TestItemRefuseRefusedSupersedeLeavesTheTargetLive(t *testing.T) {
	st := contractMREnv(t)
	run := edgeMRDaily(t, st)
	target := edgeMRAddItem(t, st, store.MMItem{Quote: "indent with tabs"})
	before := len(edgeMRItems(t, st))
	ans := MindAnswer{Ops: []MindOp{itemRefuseSupersede(target, itemRefuseLong, "indent with spaces after all")}}
	res, err := ApplyMindOps(st, run, ans, nil, time.Now().UnixMilli())
	if err != nil {
		t.Fatalf("ApplyMindOps = %v", err)
	}
	if res.Counts.Refused != 1 || res.Counts.Superseded != 0 {
		t.Errorf("counts = %+v, want 1 refused, 0 superseded", res.Counts)
	}
	if it := contractMRGetItem(t, st, target); it.InvalidatedAt != 0 {
		t.Errorf("target was invalidated (%q) by a refused supersede", it.InvalidReason)
	}
	if after := len(edgeMRItems(t, st)); after != before {
		t.Errorf("a refused supersede wrote %d rows", after-before)
	}
	report := MindRunReport(run, ans, res, MindManifest{})
	if want := fmt.Sprintf("refused (%s): #%d ", itemRefuseReason, target); !strings.Contains(report, want) {
		t.Errorf("report lacks %q:\n%s", want, report)
	}
}

// A refused op is not an insert: the cap counts rows written, so refusals
// ahead of the valid adds leave the whole cap for them.
func TestItemRefuseDoesNotCountTowardTheCap(t *testing.T) {
	st := contractMREnv(t)
	run := edgeMRDaily(t, st)
	var ops []MindOp
	for i := 0; i < 5; i++ {
		ops = append(ops, itemRefuseAdd(itemRefuseLong, fmt.Sprintf("over-long claim %d", i)))
	}
	for i := 0; i < mmProposeCap; i++ {
		ops = append(ops, edgeMRAdd(fmt.Sprintf("valid claim number %d", i)))
	}
	res, err := ApplyMindOps(st, run, MindAnswer{Ops: ops}, nil, time.Now().UnixMilli())
	if err != nil {
		t.Fatalf("ApplyMindOps = %v", err)
	}
	if res.Counts.Added != mmProposeCap || res.Counts.Refused != 5 {
		t.Errorf("counts = %+v, want %d added and 5 refused", res.Counts, mmProposeCap)
	}
	for _, r := range res.Results {
		if r.Reason == "cap" {
			t.Errorf("op %d refused for the cap: the refused items were charged against it", r.Index)
		}
	}
}

func TestItemRefuseStructuralDefectStillRefusesTheAnswer(t *testing.T) {
	st := contractMREnv(t)
	run := edgeMRDaily(t, st)
	before := len(edgeMRItems(t, st))
	ans := MindAnswer{Ops: []MindOp{
		edgeMRAdd("a valid add ahead of a missing target"),
		{Op: "retire", Target: 1 << 40, Quote: "retire something that is not there"},
	}}
	_, err := ApplyMindOps(st, run, ans, nil, time.Now().UnixMilli())
	if err == nil || !strings.Contains(err.Error(), "no such item") {
		t.Fatalf("ApplyMindOps = %v, want the whole answer refused on the missing target", err)
	}
	if after := len(edgeMRItems(t, st)); after != before {
		t.Errorf("a refused answer wrote %d rows", after-before)
	}
}
