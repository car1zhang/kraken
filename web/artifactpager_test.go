package web

import (
	"io/fs"
	"strings"
	"testing"
)

// readIndexHTML returns the embedded index.html — the embedded copy, for the
// same reason readAppJS reads the embedded app.js: that is what a built
// binary actually serves.
func readIndexHTML(t *testing.T) string {
	t.Helper()
	data, err := fs.ReadFile(webAssets, "static/index.html")
	if err != nil {
		t.Fatalf("read embedded static/index.html: %v", err)
	}
	return string(data)
}

// artifactpager_test.go — the artifact viewer's next/prev pager rests on two
// things that are invisible from Go and untestable from JS (there is no JS
// test harness in this repo, same as jobkey_test.go / boardsurface_test.go):
//
//  1. every artifact chip carries data-key. A chip knows its own url and name
//     and nothing else, so the click handler cannot learn the ordered list it
//     sits in from the chip alone — data-key is what gets it to jobsBySlug.
//     Drop it and the pager silently degrades to a singleton: every artifact
//     opens with the pager hidden, which looks like the feature was never
//     built. It is also what the 3s poll re-derives the sequence from, so
//     without it a live cell's new artifacts stay unreachable until reopen.
//
//  2. stepArtifact exists and is the single navigation command. Both input
//     surfaces (the ‹ › buttons and the ArrowLeft/ArrowRight handler) call it,
//     and it re-opens through openArtifact precisely so showRaw, the cached
//     rawText and the feedback form reset per artifact.
//
// These pin WHICH mechanism is used, not how any of it is written.

func TestArtifactChipsCarryJobKey(t *testing.T) {
	src := readAppJS(t)
	body := funcBody(t, src, "artifactListHTML")
	// scoped to the artifact chip's own template line rather than the whole
	// body, so a data-key appearing on some future sibling element can't
	// satisfy this on the real chip's behalf
	i := strings.Index(body, `<button class="chip" `)
	if i < 0 {
		t.Fatal("artifactListHTML no longer emits `<button class=\"chip\" ` — renamed? update this guard rather " +
			"than deleting it. The .chip class is the artifact-overlay click target (see the click " +
			"handler's .chip branch), and the pager reads data-key off that same element")
	}
	line := body[i:]
	if end := strings.Index(line, "\n"); end >= 0 {
		line = line[:end]
	}
	if !strings.Contains(line, "data-key=") {
		t.Errorf("the artifact chip no longer emits data-key — without it the click handler can't reach "+
			"jobsBySlug, so every artifact opens as a singleton sequence and the pager never appears. "+
			"Got: %s", line)
	}
}

func TestStepArtifactIsTheNavigationCommand(t *testing.T) {
	src := readAppJS(t)
	body := funcBody(t, src, "stepArtifact")
	if !strings.Contains(body, "openArtifact(") {
		t.Error("stepArtifact no longer re-opens through openArtifact — patching the panel body in place " +
			"leaves showRaw, the cached rawText and the open feedback form from the PREVIOUS artifact " +
			"attached to the new one")
	}
	if !strings.Contains(body, "artifactSeq.length") {
		t.Error("stepArtifact no longer bounds the step against artifactSeq.length — the step clamps at " +
			"both ends and must never wrap")
	}
	// both input surfaces route through it; a listener that computed the next
	// index itself would drift from the clamp above
	if !strings.Contains(src, "stepArtifact(-1)") || !strings.Contains(src, "stepArtifact(1)") {
		t.Error("the pager buttons no longer call stepArtifact(-1)/stepArtifact(1) — it is the single " +
			"navigation command, shared with the ArrowLeft/ArrowRight handler")
	}
}

// TestArtifactAsyncTailsAreGenerationGuarded pins the fix for the race the
// pager introduced. Before next/prev, openArtifact could only run with the
// overlay CLOSED, so its markdown fetch and the raw toggle's fetch both
// resolved into the world that started them. A step is synchronous and can
// now happen mid-flight, and an unguarded resolution repaints the body with
// the PREVIOUS artifact's text under the current one's title — sticky until
// you navigate again, and for the raw toggle it also desyncs showRaw from
// what's on screen.
//
// The guard is a generation counter rather than a url comparison, and that
// distinction is the part worth pinning: paging away and BACK lands on the
// same url with the body already repainted and showRaw/rawText already reset,
// so a url check passes and the stale write still lands.
func TestArtifactAsyncTailsAreGenerationGuarded(t *testing.T) {
	src := readAppJS(t)
	if !strings.Contains(src, "let artifactGen = 0;") {
		t.Fatal("static/app.js has no artifactGen — renamed? update this guard rather than deleting it. " +
			"Something must let an async tail in the artifact viewer tell whether the overlay has moved " +
			"on since it started")
	}
	open := funcBody(t, src, "openArtifact")
	if !strings.Contains(open, "++artifactGen") {
		t.Error("openArtifact no longer bumps artifactGen — nothing then invalidates the fetches still " +
			"in flight from the artifact it just replaced")
	}
	// two tails inside openArtifact (.then and .catch) and one in the
	// #panel-raw handler outside it
	if n := strings.Count(open, "gen !== artifactGen"); n < 2 {
		t.Errorf("openArtifact's markdown fetch drops fewer than 2 stale writes (found %d) — the .then "+
			"AND the .catch both repaint panelBody, and 'failed to load: …' wipes the artifact you "+
			"navigated to just as thoroughly as a successful render does", n)
	}
	if strings.Count(src, "gen !== artifactGen") < 3 {
		t.Error("the #panel-raw handler no longer drops its stale write — its await window is the widest " +
			"in the viewer (an html/svg card reaches raw only through that fetch), and resuming late " +
			"paints the old source with showRaw already false")
	}
}

// TestArtifactCloseBanksTheFeedbackDraft pins that EVERY way of leaving an
// artifact banks a half-typed feedback note, not just the pager. openArtifact
// clears the composer on every open, so any close path that skips the stash
// destroys the note outright — and "leaving banks it, except by Escape" is
// exactly the arbitrary split this routing exists to prevent. Both close
// gestures go through closeArtifact for that reason; an inlined
// `overlay.hidden = true` in either one is the regression.
func TestArtifactCloseBanksTheFeedbackDraft(t *testing.T) {
	src := readAppJS(t)
	body := funcBody(t, src, "closeArtifact")
	if !strings.Contains(body, "stashFeedbackDraft()") {
		t.Error("closeArtifact no longer banks the feedback draft — Escape is one keystroke from the " +
			"composer and destroys a half-typed note as thoroughly as › would")
	}
	// the Escape cascade and the backdrop click, the viewer's only two close
	// gestures — both must route through it rather than hiding the overlay
	// themselves
	if n := strings.Count(src, "closeArtifact()"); n < 2 {
		t.Errorf("fewer than 2 callers of closeArtifact() (found %d) — the Escape cascade and the "+
			"backdrop click are both supposed to route through it", n)
	}
	if strings.Contains(src, "if (ev.target === overlay) overlay.hidden = true") {
		t.Error("the backdrop click hides the overlay directly again, skipping the draft stash")
	}
	if strings.Contains(src, "if (!overlay.hidden) { overlay.hidden = true; return; }") {
		t.Error("the Escape cascade hides the artifact overlay directly again, skipping the draft stash")
	}
}

// TestPagerReadoutDoesNotSpamItsLiveRegion pins the compare-before-write on
// #panel-pos. renderPager runs on every 3s board poll (resyncArtifactSeq),
// and #panel-pos is aria-live: assigning textContent replaces the text node,
// which is an announceable mutation even when the string is identical. An
// unconditional write therefore reads the position aloud every three seconds
// for as long as the artifact is open — inverting the point of the attribute,
// which is to announce NAVIGATION, and reliably getting the region muted.
func TestPagerReadoutDoesNotSpamItsLiveRegion(t *testing.T) {
	src := readAppJS(t)
	body := funcBody(t, src, "renderPager")
	if !strings.Contains(body, "textContent !== pos") {
		t.Error("renderPager writes #panel-pos unconditionally — it is called from resyncArtifactSeq on " +
			"every 3s poll and the element is aria-live, so each write is another announcement of " +
			"nothing having changed. Compare first, as the poll's other small-text writes do")
	}
	if !strings.Contains(readIndexHTML(t), `id="panel-pos" aria-live`) {
		t.Error(`#panel-pos lost its aria-live — an arrow-key navigation then announces nothing at all, ` +
			`which is what the guard above exists to keep meaningful`)
	}
}
