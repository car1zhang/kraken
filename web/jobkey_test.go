package web

import (
	"strings"
	"testing"
)

// jobkey_test.go — the board payload's `slug` is unique only WITHIN a
// namespace. An archived job rides the same jobs array as the active ones
// (api/board.go) under its numbered archive name, and that name is numbered
// only against the archive's own contents — so archiving `foo` and then
// creating a new active `foo` puts TWO entries with slug "foo" on the board,
// archived last.
//
// Keying any client-side map by bare slug therefore hands the archived entry
// a last-writer-wins victory over the live job, and the symptom is silent and
// nasty: jobsBySlug["foo"] returned the archived entry (live:false), so
// liveDisplayColumn read null and a click on the LIVE blocked card fell
// through to the read-only editor instead of the unblock modal — the job
// became unsteerable from the web UI for as long as both existed.
//
// These are string tests over app.js for the same reason boardsurface_test.go
// is: there is no JS test harness in this repo, and this failure mode passes
// every Go test while being invisible until you happen to reuse a slug. They
// pin WHICH key each site uses, not how any of it is written.

func TestJobKeyNamespacesArchivedEntries(t *testing.T) {
	src := readAppJS(t)
	const decl = "const jobKey ="
	i := strings.Index(src, decl)
	if i < 0 {
		t.Fatal("static/app.js has no jobKey — renamed? update this guard rather than deleting it. " +
			"Something must namespace archived board entries away from active ones, or a reused slug " +
			"makes the live job unsteerable")
	}
	line := src[i:]
	if end := strings.Index(line, "\n"); end >= 0 {
		line = line[:end]
	}
	if !strings.Contains(line, "j.archived") {
		t.Errorf("jobKey no longer branches on j.archived — an archived entry must not share a key "+
			"with an active job that reused its freed slug. Got: %s", line)
	}
}

func TestRenderKeysJobsByJobKeyNotSlug(t *testing.T) {
	src := readAppJS(t)
	body := funcBody(t, src, "render")
	if !strings.Contains(body, "jobsBySlug[jobKey(j)]") {
		t.Error("render() no longer keys jobsBySlug by jobKey(j) — with a bare slug the archived entry " +
			"(appended last by buildWebBoard) overwrites the active job's, and every jobsBySlug reader " +
			"(liveDisplayColumn, openSteer, loadForm) then sees an entry with live:false")
	}
	if strings.Contains(body, "jobsBySlug[j.slug]") {
		t.Error("render() keys jobsBySlug by bare j.slug again — that is the duplicate-slug regression: " +
			"an archived job and an active one that reused its freed slug collide on that key")
	}
}

func TestPredictReorderedJobsIgnoresArchived(t *testing.T) {
	src := readAppJS(t)
	body := funcBody(t, src, "predictReorderedJobs")
	if !strings.Contains(body, "!j.archived") {
		t.Error("predictReorderedJobs no longer filters archived entries out of its slug map — its input " +
			"`slugs` is a column's DOM order (active jobs only), so an archived entry sharing a reused " +
			"slug would win the map and get spliced into the column in the live job's place")
	}
}

// TestArchivedCardUsesJobKey covers the per-card key an archived card shares
// with an active one when the slug was reused: the card's data-slug, which
// snapshotDocInputs/restoreDocInputs use as a per-card scroll key. It is not a
// server key — unarchive/delete carry the bare archive name on their own
// buttons, which is what the server actually resolves.
//
// This used to guard a second key as well: the artifact chips' expand/collapse
// state, which chipsHTML kept per job in localStorage and which two cards
// sharing a reused slug would have toggled for each other. That assertion is
// gone because the STATE is gone — the card no longer lists artifacts at all,
// just a count (artifactChipHTML), so there is no "+N more" toggle left to
// collide on.
//
// It was doing a second, unnamed job though, and that job moved to
// TestArchivedCardIsClickable below rather than disappearing with it — see the
// note there. Deleting it outright, instead of asking why it had gone red, is
// what would have let that regression through.
func TestArchivedCardUsesJobKey(t *testing.T) {
	src := readAppJS(t)
	body := funcBody(t, src, "archivedCardHTML")
	if !strings.Contains(body, `data-slug="${esc(jobKey(j))}"`) {
		t.Error("archivedCardHTML's card data-slug is not jobKey(j) — snapshotDocInputs/restoreDocInputs " +
			"key per-card panel scroll on it, so a bare slug makes the two cards restore each other's " +
			"feedback-panel scroll position")
	}
}

// TestArchivedCardIsClickable pins the archived card's ROUTE to its artifacts,
// which is the job the deleted chipsHTML assertion above was silently also
// doing. The card shows a bare ▤ count (artifactChipHTML) whose entire stated
// affordance is "click the card", and the only handler that opens anything
// from a card matches `.card[data-editable]` — a PRESENCE selector, so the
// attribute must exist even though its value is "false" (which only tells the
// editor to render read-only).
//
// Without it the click lands nowhere: .ct-chip is an inert span, the .chip
// branch does not match it, and the archive's artifacts have no path in the UI
// at all. That is what the card would have done for every archived job holding
// artifacts — 69 of them at the time — when the chip list became a count. It
// was caught in review before it was ever committed, which is the only reason
// this guard is a precaution rather than a post-mortem.
// Both ends of the coupling are asserted, and the card end is scoped to the
// emitted ROOT TAG rather than to the function body. That is not fussiness:
// funcBody slices raw source, comments included, and the comment above the
// attribute necessarily quotes it — so a body-wide Contains stays green after
// the attribute itself is deleted, which is the exact edit this guards against
// and the more likely one (a careless removal takes the one-line attribute,
// not the paragraph explaining it).
func TestArchivedCardIsClickable(t *testing.T) {
	src := readAppJS(t)
	body := funcBody(t, src, "archivedCardHTML")

	// the root tag only: from `<div class="card archived"` to the `>` that
	// closes it. No interpolation in between contains a `>`.
	const rootOpen = `<div class="card archived"`
	i := strings.Index(body, rootOpen)
	if i < 0 {
		t.Fatal(`archivedCardHTML no longer emits a <div class="card archived"> root — renamed? update ` +
			"this guard rather than deleting it: something must make the archived card clickable, or its " +
			"artifacts have no path in the UI")
	}
	end := strings.Index(body[i:], ">")
	if end < 0 {
		t.Fatal("archivedCardHTML's root tag is unterminated in source — cannot scope this assertion")
	}
	rootTag := body[i : i+end]
	if !strings.Contains(rootTag, "data-editable=") {
		t.Errorf("archivedCardHTML's card ROOT TAG has no data-editable attribute — the open-card handler "+
			"matches `.card[data-editable]` on presence, so without it a click on an archived card does "+
			"nothing and its artifacts (a bare count chip, not .chip buttons) become unreachable. Got: %s",
			rootTag)
	}
	if !strings.Contains(body, "artifactChipHTML(j)") {
		t.Error("archivedCardHTML no longer renders artifactChipHTML — if it lists artifacts directly " +
			"again, the count-plus-click-through contract this test guards no longer applies and this " +
			"guard should be rewritten rather than deleted")
	}

	// the handler end. The archived card's attribute value is "false", so the
	// route survives only while the selector tests PRESENCE — tightening it to
	// [data-editable="true"] reads like a correctness fix and silently drops
	// every archived card back out of the click path.
	if !strings.Contains(src, `closest(".card[data-editable]")`) {
		t.Error(`the open-card handler no longer matches closest(".card[data-editable]") — the archived ` +
			"card carries data-editable=\"false\", so anything narrower than a presence test stops " +
			"opening it and strands the archive's artifacts")
	}
	if strings.Contains(src, `.card[data-editable="`) {
		t.Error(`a .card[data-editable="…"] value selector appeared — the archived card's value is ` +
			`"false" and it must still match, so this selector must test presence only`)
	}
}
