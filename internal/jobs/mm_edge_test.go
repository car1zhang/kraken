package jobs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"kraken/internal/store"
)

// mm_edge_test.go — the EDGE dimension of 50-contract.md's slice S2 daemon half:
// MMVerifyPass' FAILURE paths, driven entirely through the `mmRunGit` seam so no
// test here shells out, plus the three refusals-to-act the contract states as
// invariants (a failed read is never an invalidation, a repo whose head has not
// moved is skipped whole, a repo where EVERYTHING would die trips the breaker).
// Written blind from the contract text alone — internal/jobs/mm.go, graph.go,
// recall.go, cell.go, crew.go and daemon.go were never read.
//
// The sibling blind author (athena-logic) owns the mint, the render format and
// the survived-verify happy path. What lives here is the other half.
//
// Every package-level identifier is prefixed `edgeMM`: this package's test
// namespace is shared with the sibling blind author and with a large existing
// suite that already owns shorter `edge…` names.

// ---------------------------------------------------------------------------
// clock
// ---------------------------------------------------------------------------

// MMVerifyPass is self-throttled on process-global state, and the contract now
// freezes that stamp as `mmLastPass` (atomic.Int64, the graphLastPass shape) —
// so every rig below resets it to 0 at its test's start and no suite can
// throttle another. The strictly increasing far-future clock is kept beside it:
// the reset answers cross-suite interference, while a clock that never repeats
// keeps the ≥10m gate open between two passes inside one test.
var (
	edgeMMClockMu sync.Mutex
	edgeMMClockAt = time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
)

func edgeMMTick() time.Time {
	edgeMMClockMu.Lock()
	defer edgeMMClockMu.Unlock()
	edgeMMClockAt = edgeMMClockAt.Add(time.Hour)
	return edgeMMClockAt
}

// ---------------------------------------------------------------------------
// rig
// ---------------------------------------------------------------------------

type edgeMMCall struct {
	Dir  string
	Args []string
}

// edgeMMRig is one isolated world: a wired store, a sandboxed HOME carrying a
// fake ~/Developer checkout, and a scripted `mmRunGit` recording every call.
type edgeMMRig struct {
	t  *testing.T
	st *store.Store

	mu        sync.Mutex
	calls     []edgeMMCall
	head      string
	failFetch bool
	diffOut   string            // stage (a): the --unified=0 diff
	blobs     map[string]string // stage (b): what `git show <head>:<path>` finds
}

func edgeMMNewRig(t *testing.T) *edgeMMRig {
	t.Helper()
	// The throttle stamp is process-global, so a sibling suite's pass would
	// otherwise park it ahead of this test and make MMVerifyPass a silent
	// no-op here. Reset at the start of every test that drives the pass — the
	// contract's own seam.
	mmLastPass.Store(0)
	t.Cleanup(func() { mmLastPass.Store(0) })
	// useTestStore sandboxes HOME into a temp dir and wires ActiveStore,
	// unwiring both on cleanup.
	st := useTestStore(t)
	rig := &edgeMMRig{
		t:     t,
		st:    st,
		head:  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		blobs: map[string]string{},
	}

	// notify() shells out to osascript, and a test that fires a real desktop
	// notification has a side effect on the machine running it. Empty PATH makes
	// every exec lookup fail harmlessly whichever spelling the pass reaches for,
	// and the notifyFn seam covers the other one.
	t.Setenv("PATH", t.TempDir())
	origNotify := notifyFn
	origGit := mmRunGit
	t.Cleanup(func() { notifyFn, mmRunGit = origNotify, origGit })
	notifyFn = func(string, string) {}
	mmRunGit = rig.run

	return rig
}

// run is the seam. It answers the four call shapes the contract's code-half
// algorithm names and returns ("", nil) — "nothing there, and nothing broke" —
// for everything else, so an unscripted probe reads as a MISS rather than as an
// error the pass would (correctly) turn into a check_error.
func (r *edgeMMRig) run(ctx context.Context, dir string, args ...string) (string, error) {
	r.mu.Lock()
	r.calls = append(r.calls, edgeMMCall{Dir: dir, Args: append([]string(nil), args...)})
	failFetch, head, diffOut := r.failFetch, r.head, r.diffOut
	blobs := make(map[string]string, len(r.blobs))
	for k, v := range r.blobs {
		blobs[k] = v
	}
	r.mu.Unlock()

	if len(args) == 0 {
		return "", nil
	}
	joined := strings.Join(args, " ")
	switch {
	case args[0] == "fetch":
		if failFetch {
			return "", context.DeadlineExceeded
		}
		return "", nil
	case args[0] == "rev-parse":
		return head + "\n", nil
	case args[0] == "show":
		// `show <sha>:<path>`
		for _, a := range args[1:] {
			if i := strings.Index(a, ":"); i >= 0 {
				if b, ok := blobs[a[i+1:]]; ok {
					return b, nil
				}
			}
		}
		return "", nil
	case args[0] == "diff" && strings.Contains(joined, "--name-status"):
		return "", nil
	case args[0] == "diff":
		return diffOut, nil
	}
	return "", nil
}

func (r *edgeMMRig) snapshot() []edgeMMCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]edgeMMCall(nil), r.calls...)
}

// sawVerb reports whether any recorded call's first arg is verb and whose whole
// argv contains every fragment.
func (r *edgeMMRig) sawVerb(verb string, fragments ...string) bool {
	for _, c := range r.snapshot() {
		if len(c.Args) == 0 || c.Args[0] != verb {
			continue
		}
		joined := strings.Join(c.Args, " ")
		ok := true
		for _, f := range fragments {
			if !strings.Contains(joined, f) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// repo creates ~/Developer/<name> as a git checkout so jobs.ResolveRepo finds
// it (isGitRepo is one Stat of .git), and returns the name.
func (r *edgeMMRig) repo(name string) string {
	r.t.Helper()
	dir := filepath.Join(os.Getenv("HOME"), "Developer", name)
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		r.t.Fatalf("make fake repo %s: %v", name, err)
	}
	return name
}

// item + anchor: the smallest live code reading.
func (r *edgeMMRig) anchor(repo, path string, start, end int, text string) int64 {
	r.t.Helper()
	id, err := r.st.AddMMItem(store.MMItem{
		Kind:      "reading",
		Source:    "code",
		SourceRef: repo + "/" + path,
		Content:   text,
		CreatedAt: 1_770_000_000_000,
	})
	if err != nil {
		r.t.Fatalf("AddMMItem: %v", err)
	}
	if err := r.st.AddMMCodeAnchor(store.MMCodeAnchor{
		ItemID:     id,
		Repo:       repo,
		Path:       path,
		StartLine:  start,
		EndLine:    end,
		AnchorSHA:  "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		AnchorKind: "origin-main",
		Text:       text,
		TextSHA:    "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		Lang:       "go",
	}); err != nil {
		r.t.Fatalf("AddMMCodeAnchor: %v", err)
	}
	return id
}

func (r *edgeMMRig) readAnchor(id int64) store.MMCodeAnchor {
	r.t.Helper()
	a, ok, err := r.st.GetMMCodeAnchor(id)
	if err != nil || !ok {
		r.t.Fatalf("GetMMCodeAnchor(%d) = (ok %v, err %v)", id, ok, err)
	}
	return a
}

func (r *edgeMMRig) readItem(id int64) store.MMItem {
	r.t.Helper()
	it, err := r.st.GetMMItem(id)
	if err != nil {
		r.t.Fatalf("GetMMItem(%d): %v", id, err)
	}
	return it
}

// settle waits for the code half to go quiet. The contract runs it in a
// single-flight goroutine with no completion seam, so quiescence of the git-call
// log is the only observable end — and waiting for it is required rather than
// merely tidy: the CAS is process-global, so a goroutine still running when this
// test returns would make the NEXT test's code half a silent no-op.
func (r *edgeMMRig) settle() {
	r.t.Helper()
	last, stable := -1, 0
	for i := 0; i < 400; i++ {
		n := len(r.snapshot())
		if n == last {
			stable++
			if stable >= 20 {
				return
			}
		} else {
			last, stable = n, 0
		}
		time.Sleep(10 * time.Millisecond)
	}
	r.t.Fatalf("the code half never went quiet: %d git calls and still moving", len(r.snapshot()))
}

// ---------------------------------------------------------------------------
// a read that failed is never an invalidation
// ---------------------------------------------------------------------------

// TestEdgeMMVerifyUnresolvableRepoIsCheckErrorNeverInvalidation is the first arm
// of the cross-slice invariant. A repo that no longer sits under ~/Developer is
// a machine that has changed, not a fact that has: kraken cannot see the code, so
// it may not claim the code is gone. This is also the state of every anchor
// marked from a repo carl has since moved or renamed.
func TestEdgeMMVerifyUnresolvableRepoIsCheckErrorNeverInvalidation(t *testing.T) {
	rig := edgeMMNewRig(t)
	// deliberately NO rig.repo(): ~/Developer does not even exist
	id := rig.anchor("not-a-repo-on-this-machine", "internal/x.go", 10, 12, "if err != nil {")

	MMVerifyPass(rig.st, edgeMMTick())
	rig.settle()

	a := rig.readAnchor(id)
	if a.CheckError == "" {
		t.Error("an unresolvable repo left check_error empty — a repo kraken cannot find must be diagnosable from `k mm get`")
	}
	if it := rig.readItem(id); it.InvalidatedAt != 0 {
		t.Errorf("an unresolvable repo INVALIDATED the item (reason %q) — a failed read is never an invalidation", it.InvalidReason)
	}
	if rig.sawVerb("fetch") {
		t.Error("the pass fetched in a repo it could not resolve")
	}
}

// TestEdgeMMVerifyFetchFailureIsCheckErrorAndAnchorsStayLive is the second arm,
// and the one the daemon meets in real life: no network, a VPN, an ssh agent
// that has forgotten its key. Every anchor in that repo gets check_error, none
// of them dies, and the pass moves ON to the next repo rather than aborting —
// asserted here by a second repo whose anchor is verified normally in the same
// pass.
func TestEdgeMMVerifyFetchFailureIsCheckErrorAndAnchorsStayLive(t *testing.T) {
	rig := edgeMMNewRig(t)
	broken := rig.repo("aaa-broken")
	healthy := rig.repo("zzz-healthy")
	rig.failFetch = true

	a1 := rig.anchor(broken, "internal/a.go", 10, 12, "if err != nil {")
	a2 := rig.anchor(broken, "internal/b.go", 20, 22, "return nil")
	// the healthy repo's fetch fails too (the seam is scripted per-process, not
	// per-repo), so what this second repo proves is the "next repo" clause: its
	// anchors are reached at all.
	a3 := rig.anchor(healthy, "internal/c.go", 30, 32, "func main() {")

	MMVerifyPass(rig.st, edgeMMTick())
	rig.settle()

	for _, id := range []int64{a1, a2, a3} {
		if got := rig.readAnchor(id); got.CheckError == "" {
			t.Errorf("anchor %d: check_error is empty after a failed fetch", id)
		}
		if it := rig.readItem(id); it.InvalidatedAt != 0 {
			t.Errorf("anchor %d: the item was invalidated by a failed FETCH (reason %q)", id, it.InvalidReason)
		}
	}
	if !rig.sawVerb("fetch") {
		t.Fatal("the pass never fetched")
	}
	if rig.sawVerb("diff") || rig.sawVerb("show") {
		t.Error("the pass kept working in a repo whose fetch failed — a failed fetch means the repo's answer is unknown, so nothing after it can be trusted")
	}
}

// TestEdgeMMVerifyFetchFailureLeavesTheHeadWatermarkAlone is the failure's
// second, quieter consequence. The watermark is what makes the NEXT pass skip a
// quiet repo, so stamping it after a fetch that failed would mean the repo is
// skipped from then on and its anchors are never verified again — the exact
// silent-forever shape the tokensPass starvation lesson is about.
func TestEdgeMMVerifyFetchFailureLeavesTheHeadWatermarkAlone(t *testing.T) {
	rig := edgeMMNewRig(t)
	repo := rig.repo("watermark-repo")
	rig.failFetch = true
	rig.anchor(repo, "internal/a.go", 10, 12, "if err != nil {")

	MMVerifyPass(rig.st, edgeMMTick())
	rig.settle()

	if got, _ := rig.st.GetKV("mm_head:" + repo); got != "" {
		t.Errorf("mm_head:%s = %q after a FAILED fetch, want unset — a repo pass that did not complete may not claim it did", repo, got)
	}
}

// ---------------------------------------------------------------------------
// head unmoved
// ---------------------------------------------------------------------------

// TestEdgeMMVerifyHeadUnmovedSkipsTheRepoEntirely is the pass' whole cost
// control: nothing has landed on origin/main since the last look, so no anchor
// in that repo can have changed and the per-anchor diffs are pure waste. "Skip
// entirely" is asserted as the absence of every later git verb AND as the anchor
// rows being untouched — a skip that still stamped checked_at would silently
// report every anchor as freshly verified against a commit nobody read.
func TestEdgeMMVerifyHeadUnmovedSkipsTheRepoEntirely(t *testing.T) {
	rig := edgeMMNewRig(t)
	repo := rig.repo("quiet-repo")
	id := rig.anchor(repo, "internal/a.go", 10, 12, "if err != nil {")
	if err := rig.st.SetKV("mm_head:"+repo, rig.head); err != nil {
		t.Fatalf("seed the head watermark: %v", err)
	}

	MMVerifyPass(rig.st, edgeMMTick())
	rig.settle()

	if !rig.sawVerb("rev-parse") {
		t.Fatal("the pass never resolved head — it cannot know the repo is quiet without asking")
	}
	if rig.sawVerb("diff") || rig.sawVerb("show") {
		t.Error("the pass diffed anchors in a repo whose head had not moved")
	}
	a := rig.readAnchor(id)
	if a.CheckedAt != 0 || a.CheckedSHA != "" {
		t.Errorf("a skipped repo stamped its anchor: checked_at=%d checked_sha=%q, want both untouched", a.CheckedAt, a.CheckedSHA)
	}
	if a.CheckError != "" {
		t.Errorf("a skipped repo recorded check_error %q — a skip is not a failure", a.CheckError)
	}
	if it := rig.readItem(id); it.InvalidatedAt != 0 {
		t.Error("a skipped repo invalidated its item")
	}
}

// TestEdgeMMVerifyDirtyRepoIsCheckedDespiteTheWatermark is the accelerant's
// whole job. MarkMMRepoDirty is called when kraken merges one of its own cell
// branches, and the merge lands on origin/main between the last pass and this
// one — the watermark is stale in exactly the moment the anchors are most likely
// to have moved.
func TestEdgeMMVerifyDirtyRepoIsCheckedDespiteTheWatermark(t *testing.T) {
	rig := edgeMMNewRig(t)
	repo := rig.repo("dirty-repo")
	id := rig.anchor(repo, "internal/a.go", 10, 12, "if err != nil {")
	if err := rig.st.SetKV("mm_head:"+repo, rig.head); err != nil {
		t.Fatalf("seed the head watermark: %v", err)
	}
	rig.diffOut = "" // no hunks: the anchor is provably unchanged
	MarkMMRepoDirty(repo)

	MMVerifyPass(rig.st, edgeMMTick())
	rig.settle()

	if !rig.sawVerb("diff") {
		t.Fatal("a repo marked dirty was still skipped on the head watermark")
	}
	a := rig.readAnchor(id)
	if a.CheckedSHA != rig.head {
		t.Errorf("checked_sha = %q after a survived verify, want the head %q", a.CheckedSHA, rig.head)
	}
	if a.CheckError != "" {
		t.Errorf("check_error = %q after a clean pass", a.CheckError)
	}
	if it := rig.readItem(id); it.InvalidatedAt != 0 {
		t.Errorf("an anchor with no intersecting hunk was invalidated (reason %q) — that is the provably-unchanged case", it.InvalidReason)
	}
}

// ---------------------------------------------------------------------------
// the circuit breaker, at exactly three
// ---------------------------------------------------------------------------

// edgeMMDoomedRig scripts the world where every anchor's text is gone: stage (a)
// reports a hunk over the range, and every rung of the text ladder comes back
// empty. That is the ONLY shape that may invalidate, which is what makes it the
// right shape to test the breaker with.
func edgeMMDoomedRig(t *testing.T, repoName string, n int) (*edgeMMRig, string, []int64) {
	t.Helper()
	rig := edgeMMNewRig(t)
	repo := rig.repo(repoName)
	rig.diffOut = "@@ -10,3 +10,4 @@ func x() {\n-old\n+new\n"
	var ids []int64
	for i := 0; i < n; i++ {
		p := filepath.Join("internal", string(rune('a'+i))+".go")
		ids = append(ids, rig.anchor(repo, p, 10, 12, "the text that is no longer anywhere "+string(rune('a'+i))))
		rig.blobs[p] = "a file whose contents no longer contain the anchored lines\n"
	}
	return rig, repo, ids
}

// TestEdgeMMVerifyTwoDoomedAnchorsBothDie is the breaker's LOWER side: two is
// below the threshold, so two genuine content changes are two invalidations.
// Without this case a breaker that tripped at 2 — or at 1 — would look exactly
// like a correct one.
func TestEdgeMMVerifyTwoDoomedAnchorsBothDie(t *testing.T) {
	rig, _, ids := edgeMMDoomedRig(t, "two-doomed", 2)

	MMVerifyPass(rig.st, edgeMMTick())
	rig.settle()

	for _, id := range ids {
		it := rig.readItem(id)
		if it.InvalidatedAt == 0 {
			t.Errorf("item %d survived although its text is nowhere in the repo", id)
			continue
		}
		if it.InvalidReason == "" {
			t.Errorf("item %d was invalidated with an empty reason", id)
		}
		if a := rig.readAnchor(id); a.InvalidSHA != rig.head {
			t.Errorf("item %d: invalid_sha = %q, want the head it died at (%q)", id, a.InvalidSHA, rig.head)
		}
	}
}

// TestEdgeMMVerifyThreeDoomedAnchorsTripTheBreaker is the threshold itself. A
// repo where EVERY live anchor would die in one pass is overwhelmingly a
// repo-level problem — a truncated fetch, a rewritten history, a wrong default
// branch — and not N independent content changes; so the pass records
// check_error on each instead, which is recoverable, rather than N terminal
// invalidations, which are not.
func TestEdgeMMVerifyThreeDoomedAnchorsTripTheBreaker(t *testing.T) {
	rig, _, ids := edgeMMDoomedRig(t, "three-doomed", 3)

	MMVerifyPass(rig.st, edgeMMTick())
	rig.settle()

	for _, id := range ids {
		if it := rig.readItem(id); it.InvalidatedAt != 0 {
			t.Errorf("item %d was invalidated (reason %q) although ALL of its repo's anchors would have died in one pass", id, it.InvalidReason)
		}
		if a := rig.readAnchor(id); a.CheckError == "" {
			t.Errorf("anchor %d: check_error is empty after a breaker trip — the trip must be diagnosable", id)
		}
	}
}

// TestEdgeMMVerifyBreakerNeedsALLOfThemToDie is the breaker's other edge: three
// live anchors of which only two would die is ordinary news, not evidence of a
// broken repo, so the two die and the survivor re-anchors. The breaker keys on
// "ALL", and an implementation keying on a COUNT alone would swallow every real
// invalidation in any repo carrying three or more marks — which is every repo
// carl uses.
func TestEdgeMMVerifyBreakerNeedsALLOfThemToDie(t *testing.T) {
	rig, _, ids := edgeMMDoomedRig(t, "mixed-repo", 3)
	// give the third anchor's text a home in its file: it survives via the
	// text ladder, so not ALL of them would die.
	survivor := ids[2]
	a := rig.readAnchor(survivor)
	rig.blobs[a.Path] = "line one\nline two\n" + a.Text + "\ntail\n"

	MMVerifyPass(rig.st, edgeMMTick())
	rig.settle()

	if it := rig.readItem(survivor); it.InvalidatedAt != 0 {
		t.Errorf("the anchor whose text is still in the file was invalidated (reason %q)", it.InvalidReason)
	}
	for _, id := range ids[:2] {
		if it := rig.readItem(id); it.InvalidatedAt == 0 {
			t.Errorf("item %d survived: with one anchor alive the breaker must not trip", id)
		}
	}
}

// ---------------------------------------------------------------------------
// ambiguity never invalidates
// ---------------------------------------------------------------------------

// TestEdgeMMVerifyAmbiguousTextSurvivesWithTheFlag is the uncertainty rule in
// its sharpest form: the anchored text now appears TWICE in the file, so the
// pass cannot know which one carl read. It relocates to the occurrence nearest
// the expected position, sets `ambiguous`, and does NOT invalidate — the
// contract's "never invalidate on ambiguity", which matters because the
// alternative (killing what it cannot disambiguate) would delete carl's memory
// of a function every time it was copied.
func TestEdgeMMVerifyAmbiguousTextSurvivesWithTheFlag(t *testing.T) {
	rig := edgeMMNewRig(t)
	repo := rig.repo("ambiguous-repo")
	const text = "if err != nil {\n\treturn err\n}"
	id := rig.anchor(repo, "internal/a.go", 10, 12, text)
	rig.diffOut = "@@ -10,3 +10,3 @@ func x() {\n-old\n+new\n"
	// two occurrences: one starting at line 5, one starting at line 100. The
	// expected position is 10 (the intersecting hunk contributes no shift), so
	// the nearest is the one at line 5.
	var b strings.Builder
	for i := 1; i < 5; i++ {
		b.WriteString("filler\n")
	}
	b.WriteString(text + "\n")
	for i := 8; i < 100; i++ {
		b.WriteString("filler\n")
	}
	b.WriteString(text + "\n")
	rig.blobs["internal/a.go"] = b.String()

	MMVerifyPass(rig.st, edgeMMTick())
	rig.settle()

	if it := rig.readItem(id); it.InvalidatedAt != 0 {
		t.Fatalf("an AMBIGUOUS match invalidated the item (reason %q) — uncertainty is never a death", it.InvalidReason)
	}
	a := rig.readAnchor(id)
	if !a.Ambiguous {
		t.Error("ambiguous is false after a multi-occurrence relocation")
	}
	if a.CheckError != "" {
		t.Errorf("check_error = %q — ambiguity is a survival, not a failure", a.CheckError)
	}
	if a.StartLine != 5 {
		t.Errorf("relocated to line %d, want 5 — the occurrence NEAREST the expected position (10), not the first or last", a.StartLine)
	}
}

// ---------------------------------------------------------------------------
// the artifact half
// ---------------------------------------------------------------------------

func edgeMMSeedArtifact(t *testing.T, st *store.Store, slug, name, body string) int64 {
	t.Helper()
	j := mustCreateJob(t, st, store.Job{Slug: slug, Repo: "kraken", State: "done"})
	if err := st.SyncArtifacts(j.ID, []store.ArtifactFile{{Path: name, Bytes: []byte(body), Mtime: 1}}, nil); err != nil {
		t.Fatalf("SyncArtifacts: %v", err)
	}
	return j.ID
}

func edgeMMElementItem(t *testing.T, st *store.Store, jobID int64, name, content string) int64 {
	t.Helper()
	id, err := st.AddMMItem(store.MMItem{
		Kind:      "reading",
		Source:    "artifact-element",
		JobID:     jobID,
		Slug:      "an-artifact-job",
		SourceRef: name,
		Content:   content,
		CreatedAt: 1_770_000_000_000,
	})
	if err != nil {
		t.Fatalf("AddMMItem: %v", err)
	}
	return id
}

// TestEdgeMMVerifyArtifactElementLineEndingsAreNotAChange is the artifact half's
// whitespace boundary. The contract normalizes BOTH sides' \r\n before the
// substring test, so a doc rewritten with CRLF endings — one `git config` away
// on any machine — must not read as "carl's element is gone". This is the same
// policy the code half's two stages share, and the same failure if it is missed:
// a mass invalidation caused by nothing anyone wrote.
func TestEdgeMMVerifyArtifactElementLineEndingsAreNotAChange(t *testing.T) {
	rig := edgeMMNewRig(t)
	jobID := edgeMMSeedArtifact(t, rig.st, "crlf-job", "50-contract.md",
		"intro\r\n\r\n| a | b |\r\n| - | - |\r\n| 1 | 2 |\r\n\r\ntail\r\n")
	id := edgeMMElementItem(t, rig.st, jobID, "50-contract.md", "| a | b |\n| - | - |\n| 1 | 2 |")

	MMVerifyPass(rig.st, edgeMMTick())
	rig.settle()

	if it := rig.readItem(id); it.InvalidatedAt != 0 {
		t.Errorf("a CRLF-only difference invalidated the element (reason %q) — both sides are \\r\\n-normalized before the substring test", it.InvalidReason)
	}
}

// TestEdgeMMVerifyArtifactRowAbsentIsArtifactDeleted pins the boundary BETWEEN
// the artifact half's two reasons. A row that is gone and a row whose bytes moved
// are different events with different repairs — the first means the doc was
// deleted or the job was, the second means it was rewritten — and collapsing
// them onto one reason loses the only distinction the pane has to show.
func TestEdgeMMVerifyArtifactRowAbsentIsArtifactDeleted(t *testing.T) {
	rig := edgeMMNewRig(t)
	jobID := edgeMMSeedArtifact(t, rig.st, "deleted-job", "00-overview.md", "the overview")
	id := edgeMMElementItem(t, rig.st, jobID, "10-gone.md", "a paragraph from a doc that is not there")

	MMVerifyPass(rig.st, edgeMMTick())
	rig.settle()

	it := rig.readItem(id)
	if it.InvalidatedAt == 0 {
		t.Fatal("an element whose artifact row does not exist survived")
	}
	if it.InvalidReason != "artifact-deleted" {
		t.Errorf("reason = %q, want artifact-deleted", it.InvalidReason)
	}
}

// TestEdgeMMVerifyLeavesAnAlreadyInvalidatedItemAlone is the one-way door seen
// from the pass' side. A dead item is out of the live set, so the pass must not
// re-examine it — and if it does, the record of WHY it died must not be
// rewritten by whatever the pass would have concluded today. Here the artifact
// is long gone, so a pass that walked dead items would relabel a row carl
// removed by hand as `artifact-deleted`.
func TestEdgeMMVerifyLeavesAnAlreadyInvalidatedItemAlone(t *testing.T) {
	rig := edgeMMNewRig(t)
	jobID := edgeMMSeedArtifact(t, rig.st, "dead-item-job", "00-overview.md", "the overview")
	id := edgeMMElementItem(t, rig.st, jobID, "10-gone.md", "a paragraph nobody can find")
	const at = int64(1_700_000_000_000)
	if err := rig.st.InvalidateMMItem(id, at, "removed", "by hand"); err != nil {
		t.Fatalf("seed the invalidation: %v", err)
	}

	MMVerifyPass(rig.st, edgeMMTick())
	rig.settle()

	it := rig.readItem(id)
	if it.InvalidatedAt != at || it.InvalidReason != "removed" || it.InvalidDetail != "by hand" {
		t.Errorf("the pass rewrote a dead item's record: at=%d reason=%q detail=%q, want %d/removed/by hand",
			it.InvalidatedAt, it.InvalidReason, it.InvalidDetail, at)
	}
}

// TestEdgeMMVerifyIgnoresAnchorsOfDeadItems is the same rule on the code side,
// and it is what keeps a death from being announced twice: an anchor row is
// never deleted, so a pass that verified anchors rather than LIVE anchors would
// re-invalidate — and re-notify about — every mark that has ever died, on every
// tick, forever.
func TestEdgeMMVerifyIgnoresAnchorsOfDeadItems(t *testing.T) {
	rig := edgeMMNewRig(t)
	repo := rig.repo("dead-anchor-repo")
	id := rig.anchor(repo, "internal/a.go", 10, 12, "text that is nowhere")
	rig.diffOut = "@@ -10,3 +10,4 @@\n-old\n+new\n"
	if err := rig.st.InvalidateMMItem(id, 1_700_000_000_000, "content-changed", "an earlier pass"); err != nil {
		t.Fatalf("seed the invalidation: %v", err)
	}

	MMVerifyPass(rig.st, edgeMMTick())
	rig.settle()

	if rig.sawVerb("fetch") || rig.sawVerb("diff") {
		t.Error("the pass did git work for a repo whose only anchor belongs to a DEAD item")
	}
	a := rig.readAnchor(id)
	if a.CheckedAt != 0 || a.CheckError != "" {
		t.Errorf("a dead item's anchor was touched: checked_at=%d check_error=%q", a.CheckedAt, a.CheckError)
	}
}

// TestEdgeMMVerifyWithNoLiveAnchorsDoesNoGitWorkAtAll is the empty state — the
// one every fresh install and every mirror-adjacent test process is in. Fetching
// into ~/Developer costs network and disk, so a pass with nothing to verify must
// cost neither.
func TestEdgeMMVerifyWithNoLiveAnchorsDoesNoGitWorkAtAll(t *testing.T) {
	rig := edgeMMNewRig(t)
	rig.repo("unused-repo")

	MMVerifyPass(rig.st, edgeMMTick())
	rig.settle()

	if calls := rig.snapshot(); len(calls) != 0 {
		t.Errorf("a pass with no live anchors made %d git calls: %+v", len(calls), calls)
	}
}

// TestEdgeMMVerifyWithNoStoreIsANoOp covers the nil the daemon can hand it
// during startup and teardown (ActiveStore() is nil before UseStore and after
// UseStore(nil)). A pass that dereferenced it would panic inside the daemon
// goroutine and take the whole tick loop down.
func TestEdgeMMVerifyWithNoStoreIsANoOp(t *testing.T) {
	rig := edgeMMNewRig(t)
	rig.repo("unused-repo")
	MMVerifyPass(nil, edgeMMTick())
	rig.settle()
	if calls := rig.snapshot(); len(calls) != 0 {
		t.Errorf("MMVerifyPass(nil) made %d git calls", len(calls))
	}
}
