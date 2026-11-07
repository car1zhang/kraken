package jobs

import (
	"strings"
	"testing"
	"time"

	"kraken/internal/store"
)

// mindrun_opquote_test.go — an add or a supersede whose quote was written on
// the operation instead of inside its item. Written by the lead alongside the
// change (no blind author); identifiers are prefixed `opQuote`.

// opQuoteItem is a valid proposal with NO quote of its own.
func opQuoteItem(title string) *MMProposal {
	return &MMProposal{
		Title: title, Scope: "universal", Kind: "note",
		Body: "Owner stated it plainly.", Rung: "5",
		Source: CaptureSource{Kind: "transcript", Session: "sess-opquote", UUID: "uuid-opquote"},
	}
}

func opQuoteLive(t *testing.T, st *store.Store, id int64) store.MMItem {
	t.Helper()
	it, err := st.GetMMItem(id)
	if err != nil {
		t.Fatalf("GetMMItem(%d): %v", id, err)
	}
	return it
}

func TestOpQuoteAddTakesTheOperationQuote(t *testing.T) {
	st := contractMREnv(t)
	run := edgeMRDaily(t, st)
	ans := MindAnswer{Ops: []MindOp{
		{Op: "add", Item: opQuoteItem("tabs are four wide"), Quote: "  keep tabs four wide\r\n"},
		{Op: "add", Item: opQuoteItem("lines stay under a hundred"), Quote: "lines stay under a hundred"},
	}}
	res, err := ApplyMindOps(st, run, ans, nil, time.Now().UnixMilli())
	if err != nil {
		t.Fatalf("ApplyMindOps = %v, want both adds applied", err)
	}
	if res.Counts.Added != 2 || len(res.Results) != 2 {
		t.Fatalf("results = %+v, want two added", res)
	}
	// the item carries the operation's quote in its normalized form, which
	// is the form a later run's fold lookup matches on
	if got := opQuoteLive(t, st, res.Results[0].ID).Quote; got != "keep tabs four wide" {
		t.Errorf("first item quote = %q, want the operation's quote, normalized", got)
	}
	if got := opQuoteLive(t, st, res.Results[1].ID).Quote; got != "lines stay under a hundred" {
		t.Errorf("second item quote = %q", got)
	}
}

func TestOpQuoteItemQuoteWinsWhenBothAreGiven(t *testing.T) {
	st := contractMREnv(t)
	run := edgeMRDaily(t, st)
	item := opQuoteItem("imports are grouped")
	item.Quote = "group the imports"
	res, err := ApplyMindOps(st, run, MindAnswer{Ops: []MindOp{{Op: "add", Item: item, Quote: "a different sentence"}}}, nil, time.Now().UnixMilli())
	if err != nil {
		t.Fatalf("ApplyMindOps = %v", err)
	}
	if got := opQuoteLive(t, st, res.Results[0].ID).Quote; got != "group the imports" {
		t.Errorf("item quote = %q, want the item's own", got)
	}
}

func TestOpQuoteAddWithNoQuoteAnywhereIsStillRefused(t *testing.T) {
	st := contractMREnv(t)
	run := edgeMRDaily(t, st)
	for name, quote := range map[string]string{"absent": "", "blank": " \r\n "} {
		before := len(edgeMRItems(t, st))
		ans := MindAnswer{Ops: []MindOp{
			edgeMRAdd("a valid add ahead of the " + name + " one"),
			{Op: "add", Item: opQuoteItem("no quote at all, " + name), Quote: quote},
		}}
		res, err := ApplyMindOps(st, run, ans, nil, time.Now().UnixMilli())
		if err != nil {
			t.Fatalf("%s: ApplyMindOps = %v, want the quote-less op refused alone", name, err)
		}
		if len(res.Results) != 2 || res.Results[1].Outcome != "refused" || !strings.Contains(res.Results[1].Reason, "quote must be") {
			t.Errorf("%s: results = %+v, want op 1 refused for its empty quote", name, res.Results)
		}
		if res.Counts.Added != 1 || res.Counts.Refused != 1 {
			t.Errorf("%s: counts = %+v, want 1 added, 1 refused", name, res.Counts)
		}
		if after := len(edgeMRItems(t, st)); after != before+1 {
			t.Errorf("%s: %d rows written, want 1 (the valid add ahead of the refused one)", name, after-before)
		}
	}
}

func TestOpQuoteSupersedeTakesTheOperationQuote(t *testing.T) {
	st := contractMREnv(t)
	run := edgeMRDaily(t, st)
	old := edgeMRAddItem(t, st, store.MMItem{Quote: "indent with two spaces"})
	ans := MindAnswer{Ops: []MindOp{{Op: "supersede", Target: old, Item: opQuoteItem("indent with four spaces"), Quote: "make it four spaces now"}}}
	res, err := ApplyMindOps(st, run, ans, nil, time.Now().UnixMilli())
	if err != nil {
		t.Fatalf("ApplyMindOps = %v, want the supersede applied", err)
	}
	if res.Counts.Superseded != 1 {
		t.Fatalf("counts = %+v, want one superseded", res.Counts)
	}
	if got := opQuoteLive(t, st, res.Results[0].ID).Quote; got != "make it four spaces now" {
		t.Errorf("replacement quote = %q, want the operation's", got)
	}
	if dead := opQuoteLive(t, st, old); dead.InvalidatedAt == 0 {
		t.Errorf("superseded target %d is still live", old)
	}
}
