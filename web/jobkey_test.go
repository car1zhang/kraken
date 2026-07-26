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

// TestArchivedCardUsesJobKey covers the two per-card keys an archived card
// shares with an active one when the slug was reused: the artifact chips'
// expand/collapse state (expandedArtifacts) and the card's data-slug, which
// snapshotDocInputs/restoreDocInputs use as a per-card scroll key. Neither is
// a server key — unarchive/delete carry the bare archive name on their own
// buttons, which is what the server actually resolves.
func TestArchivedCardUsesJobKey(t *testing.T) {
	src := readAppJS(t)
	body := funcBody(t, src, "archivedCardHTML")
	if !strings.Contains(body, "chipsHTML(j.artifacts, jobKey(j)") {
		t.Error("archivedCardHTML no longer passes jobKey(j) to chipsHTML — with a bare slug an archived " +
			"card and an active card sharing that slug toggle each other's '+N more' artifact chips")
	}
	if !strings.Contains(body, `data-slug="${esc(jobKey(j))}"`) {
		t.Error("archivedCardHTML's card data-slug is not jobKey(j) — snapshotDocInputs/restoreDocInputs " +
			"key per-card panel scroll on it, so a bare slug makes the two cards restore each other's " +
			"feedback-panel scroll position")
	}
}
