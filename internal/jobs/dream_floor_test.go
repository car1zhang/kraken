package jobs

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"kraken/internal/store"
)

// dream_floor_test.go — the reportless-predecessor fallback: SweptTo
// advances when the night's job is CREATED, so a dream job that died before
// writing 00-dream-report.md left its whole window swept zero times with
// the floor already past it. DreamRun.Floor records what that job was told
// to sweep, and the next pass re-sweeps from there exactly when the
// predecessor's job row exists and demonstrably holds no report artifact.

// dreamFloorSeed stages the recurring fixture: a predecessor dream job
// created against floor, a watermark advanced to sweptTo, and one feedback
// row inside the dead window (after floor, before sweptTo) — evidence only
// a re-sweep can reach.
func dreamFloorSeed(t *testing.T, st *store.Store, now time.Time) (floor, sweptTo int64, prev *store.Job) {
	t.Helper()
	floor = now.Add(-36 * time.Hour).UnixMilli()
	sweptTo = now.Add(-12 * time.Hour).UnixMilli()
	prevSlug := "dream-" + today(now.AddDate(0, 0, -1))
	prev = mustCreateJob(t, st, store.Job{Slug: prevSlug, Repo: "kraken", Body: "x"})
	if _, err := st.AddFeedback(store.Feedback{
		JobSlug: "some-job", Artifact: "00-overview.md",
		Note: "left inside the dead window", TS: now.Add(-24 * time.Hour).UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}
	seedDreamRun(t, DreamRun{Date: today(now.AddDate(0, 0, -1)), SweptTo: sweptTo, Job: prevSlug, JobID: prev.ID, Floor: floor})
	return floor, sweptTo, prev
}

// A predecessor with no report re-opens its window: tonight's job sweeps
// from the predecessor's floor, and the new watermark records that floor as
// its own Floor.
func TestDreamPassReSweepsAReportlessPredecessor(t *testing.T) {
	st := useTestStore(t)
	resetDreamGuards(t)
	now := dreamNoon()
	floor, _, _ := dreamFloorSeed(t, st, now)

	created := kickDream(now)

	if created == nil {
		t.Fatal("no job created — the dead window's feedback was reachable only through the predecessor's floor")
	}
	if want := "--since " + fmt.Sprint(floor); !strings.Contains(created.Body, want) {
		t.Fatalf("job body does not carry %q — the sweep would start at the advanced floor and miss the dead window", want)
	}
	got := ReadDreamRun()
	if got.Floor != floor {
		t.Fatalf("watermark Floor = %d, want the re-swept floor %d", got.Floor, floor)
	}
	if got.Job != created.Slug {
		t.Fatalf("watermark Job = %q, want %q", got.Job, created.Slug)
	}
}

// A predecessor whose report synced keeps today's floor: the only evidence
// sits before SweptTo, so the night is quiet and no job is created.
func TestDreamPassTrustsAReportedPredecessor(t *testing.T) {
	st := useTestStore(t)
	resetDreamGuards(t)
	now := dreamNoon()
	_, _, prev := dreamFloorSeed(t, st, now)
	if err := st.SyncArtifacts(prev.ID, []store.ArtifactFile{
		{Path: "00-dream-report.md", Bytes: []byte("swept"), Mtime: now.UnixMilli()},
	}, nil); err != nil {
		t.Fatal(err)
	}

	if created := kickDream(now); created != nil {
		t.Fatalf("created %s, want none — the predecessor ran, its window is settled", created.Slug)
	}
}

// A watermark written before Floor existed carries zero, and zero means the
// fallback cannot know where the predecessor started — so it must not fire,
// however reportless the predecessor looks.
func TestDreamPassLegacyWatermarkWithoutFloorKeepsToday(t *testing.T) {
	st := useTestStore(t)
	resetDreamGuards(t)
	now := dreamNoon()
	_, sweptTo, _ := dreamFloorSeed(t, st, now)
	seedDreamRun(t, DreamRun{Date: today(now.AddDate(0, 0, -1)), SweptTo: sweptTo, Job: "dream-" + today(now.AddDate(0, 0, -1))})

	if created := kickDream(now); created != nil {
		t.Fatalf("created %s, want none — a legacy watermark has no floor to fall back to", created.Slug)
	}
}

// A slug collision skips the night but must not launder the predecessor's
// claim: Job and Floor ride forward so the reportless window is still
// re-sweepable tomorrow.
func TestDreamPassCollisionCarriesThePredecessorClaim(t *testing.T) {
	st := useTestStore(t)
	resetDreamGuards(t)
	now := dreamNoon()
	floor, sweptTo, prev := dreamFloorSeed(t, st, now)
	mustCreateJob(t, st, store.Job{Slug: "dream-" + today(now), Repo: "kraken", Body: "hand-made"})

	if created := kickDream(now); created != nil {
		t.Fatalf("created %s over a taken slug, want none", created.Slug)
	}
	got := ReadDreamRun()
	if got.SweptTo != sweptTo {
		t.Fatalf("swept_to = %d, want the untouched %d", got.SweptTo, sweptTo)
	}
	if got.Job != prev.Slug || got.JobID != prev.ID || got.Floor != floor {
		t.Fatalf("watermark = %+v, want the predecessor's Job %q, JobID %d and Floor %d carried through the skip", got, prev.Slug, prev.ID, floor)
	}
}

// A zero-byte report row is NOT a report: the store mirror has no size
// filter, so a placeholder or a write killed mid-run syncs as a real row,
// and the DREAM view's own reader counts only len > 0. The fallback must
// agree with it, or a night can be lost with a row asserting it ran.
func TestDreamPassTreatsAZeroByteReportAsReportless(t *testing.T) {
	st := useTestStore(t)
	resetDreamGuards(t)
	now := dreamNoon()
	floor, _, prev := dreamFloorSeed(t, st, now)
	if err := st.SyncArtifacts(prev.ID, []store.ArtifactFile{
		{Path: DreamReportName, Bytes: nil, Mtime: now.UnixMilli()},
	}, nil); err != nil {
		t.Fatal(err)
	}

	created := kickDream(now)

	if created == nil {
		t.Fatal("no job created — a zero-byte report row was taken as proof the pass ran")
	}
	if want := "--since " + fmt.Sprint(floor); !strings.Contains(created.Body, want) {
		t.Fatalf("job body does not carry %q — the dead window was not re-opened", want)
	}
}

// An ARCHIVED reportless predecessor is left alone: archiving is carl's
// explicit act, and the fallback must not re-litigate a night he closed.
// Resolution goes by JobID, which — unlike GetJobBySlug — still finds an
// archived row, so the archived check is what carries this, not a lookup
// miss.
func TestDreamPassArchivedPredecessorReadsAsRan(t *testing.T) {
	st := useTestStore(t)
	resetDreamGuards(t)
	now := dreamNoon()
	_, _, prev := dreamFloorSeed(t, st, now)
	if _, err := st.ArchiveJob(prev.ID); err != nil {
		t.Fatal(err)
	}

	if created := kickDream(now); created != nil {
		t.Fatalf("created %s, want none — the archived predecessor's window is carl's closed night", created.Slug)
	}
}

// A quiet night must not launder an UNCHECKED claim: when the widened
// window is measured empty the claim is settled, but when dreamReportExists
// could not answer the claim was never checked at all — so the quiet-night
// watermark carries Job/JobID/Floor forward exactly as the collision path
// does, and re-checking a settled claim later costs one empty overlap.
func TestDreamPassQuietNightCarriesTheClaim(t *testing.T) {
	st := useTestStore(t)
	resetDreamGuards(t)
	now := dreamNoon()
	floor := now.Add(-36 * time.Hour).UnixMilli()
	sweptTo := now.Add(-12 * time.Hour).UnixMilli()
	prevSlug := "dream-" + today(now.AddDate(0, 0, -1))
	prev := mustCreateJob(t, st, store.Job{Slug: prevSlug, Repo: "kraken", Body: "x"})
	// no feedback anywhere: the re-opened window is genuinely empty
	seedDreamRun(t, DreamRun{Date: today(now.AddDate(0, 0, -1)), SweptTo: sweptTo, Job: prevSlug, JobID: prev.ID, Floor: floor})

	if created := kickDream(now); created != nil {
		t.Fatalf("created %s on an empty night, want none", created.Slug)
	}
	got := ReadDreamRun()
	if got.Job != prevSlug || got.JobID != prev.ID || got.Floor != floor {
		t.Fatalf("watermark = %+v, want the claim (%q, %d, %d) carried through the quiet night", got, prevSlug, prev.ID, floor)
	}
	if got.SweptTo <= sweptTo {
		t.Fatalf("swept_to = %d, want advanced past %d — the empty window itself is settled", got.SweptTo, sweptTo)
	}
}
