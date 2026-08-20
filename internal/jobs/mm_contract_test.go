package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"kraken/internal/store"
)

// mm_contract_test.go — athena-logic's BLIND contract tests for
// artifacts/50-contract.md, slice S2's `internal/jobs` rows: `MintMMEpisode`,
// `MMEpisodeContent`'s FROZEN GOLDEN FORMAT, `CloseMMItemEdges`,
// `MMVerifyPass` (both halves, driven through the `mmRunGit` seam so no test
// shells out), `MarkMMRepoDirty`, and the Q11 recall retirement.
//
// Written from the frozen contract alone. internal/jobs/mm.go, graph.go,
// recall.go, cell.go, crew.go and daemon.go were never read by any route — no
// Read, no grep, no git show, no build output quoting them. Every assertion
// transcribes a clause of a contract ROW.
//
// Dimension: LOGIC — the stated invariants and the happy paths. Refusals and
// hostile inputs are athena-edge's ground; the "is all success" clauses ARE
// here, because "error-tolerant" is a stated behavior rather than an error path.
//
// Every package-level identifier declared here is prefixed `contractMM`.

// ---------------------------------------------------------------------------
// harness
// ---------------------------------------------------------------------------

// contractMMPassClock hands out a `now` for MMVerifyPass that is always LATER
// than every one it has handed out before, by more than mmVerifyInterval. The
// pass' throttle stamp is process-global and nothing in the contract names a
// reset handle for it, so a fixed timestamp per test would let the first test
// to run throttle every later one.
var contractMMPassClock = time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)

var contractMMPassClockMu sync.Mutex

func contractMMTick() time.Time {
	contractMMPassClockMu.Lock()
	defer contractMMPassClockMu.Unlock()
	contractMMPassClock = contractMMPassClock.Add(24 * time.Hour)
	return contractMMPassClock
}

// contractMMResetThrottle clears MMVerifyPass' process-global throttle stamp
// (`mmLastPass`, frozen by the contract amendment of this round) so one suite
// cannot throttle another through it. Called at the START of every test here
// that drives the pass; the throttle test deliberately does NOT reset BETWEEN
// its own calls, which is the whole thing it measures.
func contractMMResetThrottle(t *testing.T) {
	t.Helper()
	mmLastPass.Store(0)
	t.Cleanup(func() { mmLastPass.Store(0) })
}

// contractMMGitCall is one recorded invocation of the mmRunGit seam.
type contractMMGitCall struct {
	Dir  string
	Args []string
}

// contractMMGit is a scripted stand-in for git: a router keyed on the
// subcommand, recording every call. Swapped in over the package's `mmRunGit`
// var, which is the whole point of that var existing.
type contractMMGit struct {
	mu    sync.Mutex
	calls []contractMMGitCall
	route func(args []string) (string, error)
}

func contractMMSwapGit(t *testing.T, route func(args []string) (string, error)) *contractMMGit {
	t.Helper()
	g := &contractMMGit{route: route}
	prev := mmRunGit
	mmRunGit = func(ctx context.Context, dir string, args ...string) (string, error) {
		g.mu.Lock()
		g.calls = append(g.calls, contractMMGitCall{Dir: dir, Args: append([]string(nil), args...)})
		g.mu.Unlock()
		return g.route(args)
	}
	// The code half runs in a goroutine the contract names no handle to join,
	// and useTestStore's own cleanup CLOSES the store — closing a *sql.DB under
	// a live goroutine is a panic no test can recover. So the seam waits for its
	// own quiescence before it is restored: registered after useTestStore, this
	// cleanup runs BEFORE the close (cleanups are LIFO).
	t.Cleanup(func() {
		g.settle()
		mmRunGit = prev
	})
	return g
}

// settle blocks until the seam has gone quiet — no new call for a settling
// window — plus a short grace for the row writes that follow the last git call.
// Bounded, and never fatal: a test that has already made its assertions must not
// fail in cleanup.
func (g *contractMMGit) settle() {
	deadline := time.Now().Add(10 * time.Second)
	last := len(g.snapshot())
	quietSince := time.Now()
	for time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		if n := len(g.snapshot()); n != last {
			last = n
			quietSince = time.Now()
			continue
		}
		if time.Since(quietSince) > 300*time.Millisecond {
			break
		}
	}
	time.Sleep(200 * time.Millisecond)
}

func (g *contractMMGit) snapshot() []contractMMGitCall {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]contractMMGitCall(nil), g.calls...)
}

// sawSubcommand reports whether any recorded call ran `git <name> …`.
func (g *contractMMGit) sawSubcommand(name string) bool {
	for _, c := range g.snapshot() {
		if len(c.Args) > 0 && c.Args[0] == name {
			return true
		}
	}
	return false
}

func (g *contractMMGit) rendered() string {
	var b strings.Builder
	for _, c := range g.snapshot() {
		fmt.Fprintf(&b, "\n  git -C %s %s", c.Dir, strings.Join(c.Args, " "))
	}
	if b.Len() == 0 {
		return "\n  (no git calls at all)"
	}
	return b.String()
}

// contractMMWaitFor polls cond until it holds or the deadline passes. The code
// half of MMVerifyPass runs in a single-flight goroutine and the contract names
// no handle to join it, so every code-half assertion waits on the OBSERVABLE
// effect it is about.
func contractMMWaitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// contractMMWaitQuiet waits until the seam has taken at least one call and then
// stopped taking them for a settling window — the only way to assert that a
// call was NOT made by a goroutine nothing can join.
func contractMMWaitQuiet(t *testing.T, g *contractMMGit) {
	t.Helper()
	contractMMWaitFor(t, "the code half to issue its first git call", func() bool {
		return len(g.snapshot()) > 0
	})
	last := len(g.snapshot())
	quietSince := time.Now()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		if n := len(g.snapshot()); n != last {
			last = n
			quietSince = time.Now()
			continue
		}
		if time.Since(quietSince) > 400*time.Millisecond {
			return
		}
	}
	t.Fatal("the code half never stopped issuing git calls")
}

// contractMMRepo makes `<HOME>/Developer/<name>` look like a git checkout so
// jobs.ResolveRepo finds it. The seam above means nothing ever runs git in it.
func contractMMRepo(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(os.Getenv("HOME"), "Developer", name)
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatalf("make fixture repo %s: %v", name, err)
	}
	return dir
}

func contractMMSHA(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func contractMMAddItem(t *testing.T, st *store.Store, it store.MMItem) int64 {
	t.Helper()
	id, err := st.AddMMItem(it)
	if err != nil {
		t.Fatalf("AddMMItem(%+v): %v", it, err)
	}
	return id
}

func contractMMGetItem(t *testing.T, st *store.Store, id int64) store.MMItem {
	t.Helper()
	it, err := st.GetMMItem(id)
	if err != nil {
		t.Fatalf("GetMMItem(%d): %v", id, err)
	}
	return it
}

func contractMMGetAnchor(t *testing.T, st *store.Store, itemID int64) store.MMCodeAnchor {
	t.Helper()
	a, ok, err := st.GetMMCodeAnchor(itemID)
	if err != nil {
		t.Fatalf("GetMMCodeAnchor(%d): %v", itemID, err)
	}
	if !ok {
		t.Fatalf("item %d has no code anchor", itemID)
	}
	return a
}

// contractMMSeedAnchoredItem creates one live source=code item with its 1:1
// anchor and returns the item id.
func contractMMSeedAnchoredItem(t *testing.T, st *store.Store, repo, path, text string, start, end int, anchorSHA string, createdAt int64) int64 {
	t.Helper()
	ref := fmt.Sprintf("%s/%s:%d-%d", repo, path, start, end)
	id := contractMMAddItem(t, st, store.MMItem{
		Kind: "reading", Source: "code", SourceRef: ref, Content: text, CreatedAt: createdAt,
	})
	kind := "origin-main"
	if anchorSHA == "" {
		kind = "dirty"
	}
	if err := st.AddMMCodeAnchor(store.MMCodeAnchor{
		ItemID: id, Repo: repo, Path: path, StartLine: start, EndLine: end,
		AnchorSHA: anchorSHA, AnchorKind: kind, Text: text, TextSHA: contractMMSHA(text),
		Lang: "go",
	}); err != nil {
		t.Fatalf("AddMMCodeAnchor: %v", err)
	}
	return id
}

// ---------------------------------------------------------------------------
// jobs.MMEpisodeContent — the FROZEN GOLDEN FORMAT
// ---------------------------------------------------------------------------

// TestContractMMEpisodeContentGoldenStrings is the whole row, spelled out as
// exact strings rather than as substring probes: the format is the graph's ONLY
// view of an item, and the watchEpisodeContent lesson is that a second
// plausible spelling of the same fact is a second entity in the graph.
func TestContractMMEpisodeContentGoldenStrings(t *testing.T) {
	cases := []struct {
		name   string
		item   store.MMItem
		anchor *store.MMCodeAnchor
		want   string
	}{
		{
			// decision -> `decided: <content>`
			name: "a decision",
			item: store.MMItem{
				ID: 7, Kind: "decision", Source: "question", Slug: "mental-model", SourceRef: "Q3",
				Content: "Does an edit anywhere in a marked artifact invalidate the mark?\n→ yes, any byte change",
			},
			want: "mental-model decision mm:7\n" +
				"decided: Does an edit anywhere in a marked artifact invalidate the mark?\n" +
				"→ yes, any byte change",
		},
		{
			// reading WITH an anchor -> `read: <repo>/<path>:<start>-<end>`
			// then the ANCHOR's text (not the item's content).
			name: "a code reading with its anchor",
			item: store.MMItem{
				ID: 9, Kind: "reading", Source: "code", Slug: "mental-model",
				SourceRef: "kraken/internal/jobs/mm.go:10-12", Content: "ignored when an anchor is present",
			},
			anchor: &store.MMCodeAnchor{
				ItemID: 9, Repo: "kraken", Path: "internal/jobs/mm.go", StartLine: 10, EndLine: 12,
				Text: "func f() {\n\treturn nil\n}",
			},
			want: "mental-model reading mm:9\n" +
				"read: kraken/internal/jobs/mm.go:10-12\n" +
				"func f() {\n\treturn nil\n}",
		},
		{
			// reading WITHOUT an anchor -> `read: <slug>/<source_ref>` then the
			// item's content.
			name: "an artifact reading with no anchor",
			item: store.MMItem{
				ID: 11, Kind: "reading", Source: "artifact", Slug: "mental-model",
				SourceRef: "00-overview.md", Content: "00-overview.md",
			},
			want: "mental-model reading mm:11\n" +
				"read: mental-model/00-overview.md\n" +
				"00-overview.md",
		},
		{
			name: "an artifact-element reading with no anchor",
			item: store.MMItem{
				ID: 12, Kind: "reading", Source: "artifact-element", Slug: "mental-model",
				SourceRef: "50-components.md", Content: "| Kind | sqlite table |",
			},
			want: "mental-model reading mm:12\n" +
				"read: mental-model/50-components.md\n" +
				"| Kind | sqlite table |",
		},
		{
			// note -> the content alone, under the header line.
			name: "a note",
			item: store.MMItem{ID: 3, Kind: "note", Source: "manual", Content: "the graph is downstream of the mental model"},
			want: "mental-model note mm:3\n" +
				"the graph is downstream of the mental model",
		},
		{
			// "Final optional line `context: <context>` when Context non-empty"
			name: "a note with context",
			item: store.MMItem{
				ID: 4, Kind: "note", Source: "manual",
				Content: "the graph is downstream of the mental model", Context: "Q9's answer",
			},
			want: "mental-model note mm:4\n" +
				"the graph is downstream of the mental model\n" +
				"context: Q9's answer",
		},
		{
			name: "a decision with context",
			item: store.MMItem{
				ID: 5, Kind: "decision", Source: "question", SourceRef: "Q1",
				Content: "Is the steer-modal detail markable?\n→ yes", Context: "carl marked it markable",
			},
			want: "mental-model decision mm:5\n" +
				"decided: Is the steer-modal detail markable?\n→ yes\n" +
				"context: carl marked it markable",
		},
		{
			name: "a code reading with context",
			item: store.MMItem{
				ID: 6, Kind: "reading", Source: "code", SourceRef: "kraken/a.go:1-1",
				Content: "x", Context: "why this line matters",
			},
			anchor: &store.MMCodeAnchor{
				ItemID: 6, Repo: "kraken", Path: "a.go", StartLine: 1, EndLine: 1, Text: "package p",
			},
			want: "mental-model reading mm:6\n" +
				"read: kraken/a.go:1-1\n" +
				"package p\n" +
				"context: why this line matters",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := MMEpisodeContent(tc.item, tc.anchor)
			if got != tc.want {
				t.Fatalf("MMEpisodeContent =\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}

// TestContractMMEpisodeContentAlwaysOpensWithTheHandleLine restates line 1 as
// a property over every shape: it is what `k graph get` dereferences and what
// makes a compiled fact traceable back to the item carl marked.
func TestContractMMEpisodeContentAlwaysOpensWithTheHandleLine(t *testing.T) {
	for _, kind := range []string{"decision", "reading", "note"} {
		item := store.MMItem{ID: 42, Kind: kind, Source: "manual", Slug: "j", SourceRef: "r", Content: "c"}
		got := MMEpisodeContent(item, nil)
		want := "mental-model " + kind + " mm:42"
		first, _, _ := strings.Cut(got, "\n")
		if first != want {
			t.Errorf("line 1 for kind %q = %q, want %q", kind, first, want)
		}
	}
}

// TestContractMMEpisodeContentCapsAtFourThousandRunes pins the cap and the
// rune boundary: a cut mid-rune would put an invalid UTF-8 sequence into the
// episode content, which is then hashed, embedded and shown to an extractor.
func TestContractMMEpisodeContentCapsAtFourThousandRunes(t *testing.T) {
	long := strings.Repeat("é", 5_000) // 2 bytes per rune: a byte cut lands mid-rune
	got := MMEpisodeContent(store.MMItem{ID: 1, Kind: "note", Source: "manual", Content: long}, nil)
	if n := utf8.RuneCountInString(got); n != 4_000 {
		t.Fatalf("MMEpisodeContent produced %d runes, want the 4000-rune cap", n)
	}
	if !utf8.ValidString(got) {
		t.Fatal("MMEpisodeContent cut mid-rune — the cap must land on a rune boundary")
	}
	// Under the cap nothing is trimmed.
	short := strings.Repeat("é", 100)
	got = MMEpisodeContent(store.MMItem{ID: 1, Kind: "note", Source: "manual", Content: short}, nil)
	if !strings.HasSuffix(got, short) {
		t.Fatal("MMEpisodeContent trimmed a body that was well under the cap")
	}
}

// ---------------------------------------------------------------------------
// jobs.MintMMEpisode
// ---------------------------------------------------------------------------

// TestContractMMMintWritesTheEpisodeAndStampsItBack walks the mint's whole
// happy path: kind, ref, state, ts and hash all as frozen, and the item's
// episode_id stamped.
func TestContractMMMintWritesTheEpisodeAndStampsItBack(t *testing.T) {
	st := useTestStore(t)
	id := contractMMAddItem(t, st, store.MMItem{
		Kind: "note", Source: "manual", Content: "carl knows the cutover wipes the graph",
		Context: "v29", CreatedAt: 1_700_000_000_000,
	})
	it := contractMMGetItem(t, st, id)

	epID, err := MintMMEpisode(st, it, nil)
	if err != nil {
		t.Fatalf("MintMMEpisode: %v", err)
	}
	if epID <= 0 {
		t.Fatalf("MintMMEpisode returned episode id %d", epID)
	}

	ep, err := st.GetEpisode(epID)
	if err != nil {
		t.Fatalf("GetEpisode(%d): %v", epID, err)
	}
	if ep.Kind != "mental-model" {
		t.Errorf("episode kind = %q, want mental-model", ep.Kind)
	}
	if ep.Ref != fmt.Sprintf("mm:%d", id) {
		t.Errorf("episode ref = %q, want mm:%d", ep.Ref, id)
	}
	if ep.State != "pending" {
		t.Errorf("episode state = %q, want pending — the nightly /graph job drains it", ep.State)
	}
	if ep.TS != it.CreatedAt {
		t.Errorf("episode ts = %d, want the item's CreatedAt %d", ep.TS, it.CreatedAt)
	}
	wantContent := MMEpisodeContent(it, nil)
	if ep.Content != wantContent {
		t.Errorf("episode content =\n%q\nwant the frozen render\n%q", ep.Content, wantContent)
	}
	// "hash = sha256(kind + \x00 + ref + \x00 + content) hex" — the
	// jobs.addEpisode convention, restated here rather than read off the
	// implementation.
	wantHash := contractMMSHA("mental-model" + "\x00" + ep.Ref + "\x00" + wantContent)
	if ep.Hash != wantHash {
		t.Errorf("episode hash = %q, want %q", ep.Hash, wantHash)
	}

	if got := contractMMGetItem(t, st, id); got.EpisodeID != epID {
		t.Errorf("item episode_id = %d, want the minted %d", got.EpisodeID, epID)
	}
}

// TestContractMMMintIsIdempotentOnAnIdenticalRemint pins the stated dedupe:
// an identical re-mint returns the EXISTING episode id (AddEpisode's hash
// dedupe) and STILL stamps SetMMItemEpisode — the second half being what makes
// a retry after a crashed route safe.
func TestContractMMMintIsIdempotentOnAnIdenticalRemint(t *testing.T) {
	st := useTestStore(t)
	id := contractMMAddItem(t, st, store.MMItem{
		Kind: "note", Source: "manual", Content: "one fact", CreatedAt: 1_700_000_000_000,
	})
	it := contractMMGetItem(t, st, id)

	first, err := MintMMEpisode(st, it, nil)
	if err != nil {
		t.Fatalf("MintMMEpisode: %v", err)
	}
	// Deliberately re-minted from the ORIGINAL item value (episode_id still 0),
	// which is what a retried route call would carry.
	second, err := MintMMEpisode(st, it, nil)
	if err != nil {
		t.Fatalf("second MintMMEpisode: %v", err)
	}
	if second != first {
		t.Fatalf("re-mint returned episode %d, want the existing %d", second, first)
	}
	if got := contractMMGetItem(t, st, id); got.EpisodeID != first {
		t.Errorf("item episode_id = %d after the re-mint, want %d — the stamp is written either way",
			got.EpisodeID, first)
	}
	// One episode, not two.
	pending, err := st.PendingEpisodes(100)
	if err != nil {
		t.Fatalf("PendingEpisodes: %v", err)
	}
	n := 0
	for _, ep := range pending {
		if ep.Ref == fmt.Sprintf("mm:%d", id) {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d pending episodes for mm:%d, want exactly 1", n, id)
	}
}

// TestContractMMMintRendersTheAnchorWhenGivenOne: the anchor argument is what
// puts the CODE text into the graph rather than the item's stored content, so
// the mint must pass it through to the renderer.
func TestContractMMMintRendersTheAnchorWhenGivenOne(t *testing.T) {
	st := useTestStore(t)
	id := contractMMSeedAnchoredItem(t, st, "kraken", "internal/jobs/mm.go",
		"func f() {\n\treturn nil\n}", 10, 12, "aaaa", 1_700_000_000_000)
	it := contractMMGetItem(t, st, id)
	anchor := contractMMGetAnchor(t, st, id)

	epID, err := MintMMEpisode(st, it, &anchor)
	if err != nil {
		t.Fatalf("MintMMEpisode: %v", err)
	}
	ep, err := st.GetEpisode(epID)
	if err != nil {
		t.Fatalf("GetEpisode: %v", err)
	}
	want := MMEpisodeContent(it, &anchor)
	if ep.Content != want {
		t.Fatalf("episode content =\n%q\nwant\n%q", ep.Content, want)
	}
	if !strings.Contains(ep.Content, "read: kraken/internal/jobs/mm.go:10-12") {
		t.Fatalf("the minted content does not carry the anchor's coordinates:\n%s", ep.Content)
	}
}

// ---------------------------------------------------------------------------
// jobs.CloseMMItemEdges
// ---------------------------------------------------------------------------

func contractMMSeedEdge(t *testing.T, st *store.Store, episodeID int64, fact string) int64 {
	t.Helper()
	src, err := st.UpsertNode(store.GraphNode{Name: "src-" + fact, Type: "concept"})
	if err != nil {
		t.Fatalf("UpsertNode: %v", err)
	}
	dst, err := st.UpsertNode(store.GraphNode{Name: "dst-" + fact, Type: "concept"})
	if err != nil {
		t.Fatalf("UpsertNode: %v", err)
	}
	id, _, err := st.InsertEdge(store.GraphEdge{
		SrcID: src, DstID: dst, Predicate: "states", Fact: fact,
		EpisodeID: episodeID, Strength: 5, Occurrences: 1, CreatedAt: 1,
	})
	if err != nil {
		t.Fatalf("InsertEdge: %v", err)
	}
	return id
}

// TestContractMMCloseItemEdgesClosesEveryLiveEdgeOnce is the core-behaviour
// half of the row: a dead item's facts are exactly the facts that must close,
// and the verb reports how many it closed.
func TestContractMMCloseItemEdgesClosesEveryLiveEdgeOnce(t *testing.T) {
	st := useTestStore(t)
	epID, _, err := st.AddEpisode(store.Episode{
		Kind: "mental-model", Ref: "mm:1", Content: "c", Hash: "h1", TS: 1,
	})
	if err != nil {
		t.Fatalf("AddEpisode: %v", err)
	}
	a := contractMMSeedEdge(t, st, epID, "fact a")
	b := contractMMSeedEdge(t, st, epID, "fact b")
	// "already-expired edges are ... success": one of the three is closed
	// before the walk, and it must neither error nor be counted twice.
	c := contractMMSeedEdge(t, st, epID, "fact c")
	if err := st.InvalidateEdge(c, 500); err != nil {
		t.Fatalf("InvalidateEdge: %v", err)
	}

	n, err := CloseMMItemEdges(st, epID, 1_000)
	if err != nil {
		t.Fatalf("CloseMMItemEdges: %v", err)
	}
	if n != 2 {
		t.Fatalf("CloseMMItemEdges closed %d edges, want 2 (the two still live)", n)
	}

	edges, err := st.EdgesForEpisode(epID)
	if err != nil {
		t.Fatalf("EdgesForEpisode: %v", err)
	}
	byID := map[int64]store.GraphEdge{}
	for _, e := range edges {
		byID[e.ID] = e
	}
	// The row carries TWO clocks (store.GraphEdge): `at` is what
	// `InvalidateEdge(id, at)` feeds to valid_until — WORLD time, when the fact
	// stopped being true — while expired_at is the store's own system clock and
	// is what "live" is read off. So the passed value is asserted on ValidUntil
	// and liveness on ExpiredAt.
	for _, id := range []int64{a, b} {
		if byID[id].ValidUntil != 1_000 {
			t.Errorf("edge %d ValidUntil = %d, want the passed 1000", id, byID[id].ValidUntil)
		}
		if byID[id].ExpiredAt == 0 {
			t.Errorf("edge %d still reads live after the close walk", id)
		}
	}
	if byID[c].ValidUntil != 500 {
		t.Errorf("the already-expired edge's valid_until moved to %d — the first close stands", byID[c].ValidUntil)
	}
	if byID[c].ExpiredAt == 0 {
		t.Errorf("the already-expired edge reads live (ExpiredAt 0) after the walk")
	}

	// A second walk finds nothing live left: closing is idempotent, which is
	// what makes it safe on a verify pass that re-reads a dead item.
	n, err = CloseMMItemEdges(st, epID, 2_000)
	if err != nil {
		t.Fatalf("second CloseMMItemEdges: %v", err)
	}
	if n != 0 {
		t.Fatalf("second CloseMMItemEdges closed %d edges, want 0", n)
	}
}

// TestContractMMCloseItemEdgesToleratesEveryEmptyShape pins the three stated
// no-ops: episodeID 0 (an item minted before the episode landed, or a mint that
// failed), a missing episode, and an episode with zero edges (the normal state
// until the nightly /graph job has run). All three are SUCCESS — invalidation
// must never fail because the graph half has not caught up.
func TestContractMMCloseItemEdgesToleratesEveryEmptyShape(t *testing.T) {
	st := useTestStore(t)

	if n, err := CloseMMItemEdges(st, 0, 1_000); err != nil || n != 0 {
		t.Errorf("CloseMMItemEdges(0) = (%d, %v), want (0, nil)", n, err)
	}
	if n, err := CloseMMItemEdges(st, 9_999_999, 1_000); err != nil || n != 0 {
		t.Errorf("CloseMMItemEdges(missing episode) = (%d, %v), want (0, nil)", n, err)
	}

	epID, _, err := st.AddEpisode(store.Episode{
		Kind: "mental-model", Ref: "mm:2", Content: "c", Hash: "h2", TS: 1,
	})
	if err != nil {
		t.Fatalf("AddEpisode: %v", err)
	}
	if n, err := CloseMMItemEdges(st, epID, 1_000); err != nil || n != 0 {
		t.Errorf("CloseMMItemEdges(episode with no edges) = (%d, %v), want (0, nil)", n, err)
	}
}

// ---------------------------------------------------------------------------
// jobs.MMVerifyPass — the ARTIFACT half (no git)
// ---------------------------------------------------------------------------

// TestContractMMVerifyArtifactHalf walks every stated arm of the no-git half in
// ONE pass, which is also how it runs in production: an unchanged artifact
// survives, a changed one dies `content-changed`, a deleted one dies
// `artifact-deleted`, and the artifact-ELEMENT arm uses substring presence
// rather than whole-file equality.
func TestContractMMVerifyArtifactHalf(t *testing.T) {
	st := useTestStore(t)
	contractMMResetThrottle(t)
	job := mustCreateJob(t, st, store.Job{Slug: "marked", Repo: "kraken"})
	gone := mustCreateJob(t, st, store.Job{Slug: "vanished", Repo: "kraken"})

	const overview = "# overview\n\nthe mental model is carl's.\n"
	const components = "# components\n\n| table | mm_items |\n\nprose after the table.\n"
	if err := st.SyncArtifacts(job.ID, []store.ArtifactFile{
		{Path: "00-overview.md", Bytes: []byte(overview), Mtime: 1},
		{Path: "50-components.md", Bytes: []byte(components), Mtime: 1},
		{Path: "51-code-map.md", Bytes: []byte("# code map\n"), Mtime: 1},
	}, nil); err != nil {
		t.Fatalf("SyncArtifacts: %v", err)
	}
	if err := st.SyncArtifacts(gone.ID, []store.ArtifactFile{
		{Path: "00-overview.md", Bytes: []byte("# doomed\n"), Mtime: 1},
	}, nil); err != nil {
		t.Fatalf("SyncArtifacts: %v", err)
	}

	mkArtifact := func(jobID int64, slug, name, snapshot string, createdAt int64) int64 {
		id := contractMMAddItem(t, st, store.MMItem{
			Kind: "reading", Source: "artifact", JobID: jobID, Slug: slug,
			SourceRef: name, Content: name, CreatedAt: createdAt,
		})
		if err := st.SetMMSnapshot(id, []byte(snapshot), ".md"); err != nil {
			t.Fatalf("SetMMSnapshot: %v", err)
		}
		return id
	}
	mkElement := func(jobID int64, slug, name, text string, createdAt int64) int64 {
		return contractMMAddItem(t, st, store.MMItem{
			Kind: "reading", Source: "artifact-element", JobID: jobID, Slug: slug,
			SourceRef: name, Content: text, CreatedAt: createdAt,
		})
	}

	// whole-artifact marks
	unchanged := mkArtifact(job.ID, "marked", "51-code-map.md", "# code map\n", 10)
	changed := mkArtifact(job.ID, "marked", "00-overview.md", overview, 20)
	deleted := mkArtifact(gone.ID, "vanished", "00-overview.md", "# doomed\n", 30)
	// element marks
	elemLives := mkElement(job.ID, "marked", "50-components.md", "| table | mm_items |", 40)
	elemDies := mkElement(job.ID, "marked", "50-components.md", "prose after the table.", 50)
	elemDeleted := mkElement(gone.ID, "vanished", "00-overview.md", "# doomed", 60)

	// Now move the world under them: one byte changes in 00-overview.md (Q3 —
	// ANY byte change invalidates a whole-artifact mark), one element's text is
	// rewritten out of 50-components.md, and the `vanished` job's artifacts row
	// is dropped entirely.
	if err := st.SyncArtifacts(job.ID, []store.ArtifactFile{
		{Path: "00-overview.md", Bytes: []byte(overview + "\n"), Mtime: 2},
		{Path: "50-components.md", Bytes: []byte("# components\n\n| table | mm_items |\n\ndifferent prose.\n"), Mtime: 2},
	}, map[string]bool{"51-code-map.md": true}); err != nil {
		t.Fatalf("SyncArtifacts (the rewrite): %v", err)
	}
	if err := st.SyncArtifacts(gone.ID, nil, nil); err != nil {
		t.Fatalf("SyncArtifacts (the delete): %v", err)
	}

	MMVerifyPass(st, contractMMTick())

	if got := contractMMGetItem(t, st, unchanged); got.InvalidatedAt != 0 {
		t.Errorf("the UNCHANGED artifact mark was invalidated (%q) — its bytes are byte-identical", got.InvalidReason)
	}
	if got := contractMMGetItem(t, st, changed); got.InvalidatedAt == 0 {
		t.Error("the changed artifact mark survived — Q3: ANY byte change invalidates a whole-artifact mark")
	} else if got.InvalidReason != "content-changed" {
		t.Errorf("changed artifact reason = %q, want content-changed", got.InvalidReason)
	}
	if got := contractMMGetItem(t, st, deleted); got.InvalidatedAt == 0 {
		t.Error("the deleted artifact's mark survived")
	} else if got.InvalidReason != "artifact-deleted" {
		t.Errorf("deleted artifact reason = %q, want artifact-deleted", got.InvalidReason)
	}
	if got := contractMMGetItem(t, st, elemLives); got.InvalidatedAt != 0 {
		t.Errorf("an element mark whose text is STILL a substring was invalidated (%q) — the whole point "+
			"of the element arm is that the rest of the file may move", got.InvalidReason)
	}
	if got := contractMMGetItem(t, st, elemDies); got.InvalidatedAt == 0 {
		t.Error("an element mark whose text is no longer a substring survived")
	} else if got.InvalidReason != "content-changed" {
		t.Errorf("element reason = %q, want content-changed", got.InvalidReason)
	}
	if got := contractMMGetItem(t, st, elemDeleted); got.InvalidatedAt == 0 {
		t.Error("an element mark whose artifact row is gone survived")
	} else if got.InvalidReason != "artifact-deleted" {
		t.Errorf("deleted element reason = %q, want artifact-deleted", got.InvalidReason)
	}
}

// TestContractMMVerifyArtifactHalfNeverResurrects: invalidation is terminal, so
// putting the original bytes back and running the pass again leaves the item
// exactly as dead. This is the invariant the whole feature rests on — a mark is
// a record of what carl READ, not a live query.
func TestContractMMVerifyArtifactHalfNeverResurrects(t *testing.T) {
	st := useTestStore(t)
	contractMMResetThrottle(t)
	job := mustCreateJob(t, st, store.Job{Slug: "flapping", Repo: "kraken"})
	const original = "# v1\n"
	if err := st.SyncArtifacts(job.ID, []store.ArtifactFile{
		{Path: "00-overview.md", Bytes: []byte(original), Mtime: 1},
	}, nil); err != nil {
		t.Fatalf("SyncArtifacts: %v", err)
	}
	id := contractMMAddItem(t, st, store.MMItem{
		Kind: "reading", Source: "artifact", JobID: job.ID, Slug: "flapping",
		SourceRef: "00-overview.md", Content: "00-overview.md", CreatedAt: 10,
	})
	if err := st.SetMMSnapshot(id, []byte(original), ".md"); err != nil {
		t.Fatalf("SetMMSnapshot: %v", err)
	}

	if err := st.SyncArtifacts(job.ID, []store.ArtifactFile{
		{Path: "00-overview.md", Bytes: []byte("# v2\n"), Mtime: 2},
	}, nil); err != nil {
		t.Fatalf("SyncArtifacts: %v", err)
	}
	MMVerifyPass(st, contractMMTick())
	died := contractMMGetItem(t, st, id)
	if died.InvalidatedAt == 0 {
		t.Fatal("the changed artifact's mark survived the first pass")
	}

	// Put it back, byte for byte.
	if err := st.SyncArtifacts(job.ID, []store.ArtifactFile{
		{Path: "00-overview.md", Bytes: []byte(original), Mtime: 3},
	}, nil); err != nil {
		t.Fatalf("SyncArtifacts (restore): %v", err)
	}
	MMVerifyPass(st, contractMMTick())
	after := contractMMGetItem(t, st, id)
	if after.InvalidatedAt != died.InvalidatedAt || after.InvalidReason != died.InvalidReason {
		t.Fatalf("the restored bytes changed the dead item's verdict: (%d, %q) -> (%d, %q) — "+
			"content reappearing NEVER resurrects an item",
			died.InvalidatedAt, died.InvalidReason, after.InvalidatedAt, after.InvalidReason)
	}
}

// ---------------------------------------------------------------------------
// jobs.MMVerifyPass — the throttle
// ---------------------------------------------------------------------------

// TestContractMMVerifyPassThrottlesAtItsInterval pins the pacing rule the
// tokensPass starvation lesson named: the stamp goes down BEFORE any work can
// bail, so a pass that fails on its first read has still had its turn rather
// than retrying every tick.
func TestContractMMVerifyPassThrottlesAtItsInterval(t *testing.T) {
	st := useTestStore(t)
	contractMMResetThrottle(t)
	job := mustCreateJob(t, st, store.Job{Slug: "throttled", Repo: "kraken"})
	if err := st.SyncArtifacts(job.ID, []store.ArtifactFile{
		{Path: "a.md", Bytes: []byte("v1"), Mtime: 1},
	}, nil); err != nil {
		t.Fatalf("SyncArtifacts: %v", err)
	}
	mk := func(createdAt int64) int64 {
		id := contractMMAddItem(t, st, store.MMItem{
			Kind: "reading", Source: "artifact", JobID: job.ID, Slug: "throttled",
			SourceRef: "a.md", Content: "a.md", CreatedAt: createdAt,
		})
		if err := st.SetMMSnapshot(id, []byte("v1"), ".md"); err != nil {
			t.Fatalf("SetMMSnapshot: %v", err)
		}
		return id
	}
	first := mk(10)

	base := contractMMTick()
	// The world is already wrong when the first pass runs, so a pass that RAN
	// is observable as an invalidation.
	if err := st.SyncArtifacts(job.ID, []store.ArtifactFile{
		{Path: "a.md", Bytes: []byte("v2"), Mtime: 2},
	}, nil); err != nil {
		t.Fatalf("SyncArtifacts: %v", err)
	}
	MMVerifyPass(st, base)
	if contractMMGetItem(t, st, first).InvalidatedAt == 0 {
		t.Fatal("the first pass did no work at all")
	}

	// A tick a second later must be a no-op: a fresh item whose artifact is
	// already wrong stays live.
	second := mk(20)
	MMVerifyPass(st, base.Add(time.Second))
	if got := contractMMGetItem(t, st, second); got.InvalidatedAt != 0 {
		t.Errorf("the pass ran again inside its own %s interval (item %d died: %q)",
			mmVerifyInterval, second, got.InvalidReason)
	}

	// Past the interval it runs again.
	MMVerifyPass(st, base.Add(mmVerifyInterval+time.Second))
	if contractMMGetItem(t, st, second).InvalidatedAt == 0 {
		t.Errorf("the pass did not run after %s elapsed", mmVerifyInterval)
	}
}

// ---------------------------------------------------------------------------
// jobs.MMVerifyPass — the CODE half, through the mmRunGit seam
// ---------------------------------------------------------------------------

// contractMMGitRouter builds a scripted git out of a per-subcommand table, with
// every unlisted subcommand answering empty-and-successful — an unknown probe
// must never be the thing that decides a fixture.
func contractMMGitRouter(head, diff, blob, nameStatus string, fetchErr error) func([]string) (string, error) {
	return func(args []string) (string, error) {
		if len(args) == 0 {
			return "", nil
		}
		switch args[0] {
		case "fetch":
			if fetchErr != nil {
				return "", fetchErr
			}
			return "", nil
		case "rev-parse":
			return head + "\n", nil
		case "diff":
			for _, a := range args {
				if a == "--name-status" {
					return nameStatus, nil
				}
			}
			return diff, nil
		case "show":
			return blob, nil
		default:
			return "", nil
		}
	}
}

// TestContractMMVerifyCodeHalfSurvivesAShiftAndReanchors is the code half's
// headline happy path and the reason ParseHunks/MapRange exist: a commit that
// only ADDS lines above the anchor leaves it provably unchanged, so the pass
// re-anchors it to head coordinates without ever looking at the text.
func TestContractMMVerifyCodeHalfSurvivesAShiftAndReanchors(t *testing.T) {
	st := useTestStore(t)
	contractMMResetThrottle(t)
	contractMMRepo(t, "kraken")
	const anchorSHA = "1111111111111111111111111111111111111111"
	const head = "2222222222222222222222222222222222222222"
	id := contractMMSeedAnchoredItem(t, st, "kraken", "internal/jobs/mm.go",
		"func f() {\n\treturn nil\n}", 10, 12, anchorSHA, 10)

	// A pure insertion of five lines after old line 2: strictly above the
	// range, so MapRange shifts by NewCount and reports untouched.
	contractMMSwapGit(t, contractMMGitRouter(head, "@@ -2,0 +3,5 @@\n+a\n+b\n+c\n+d\n+e\n", "", "", nil))

	MMVerifyPass(st, contractMMTick())
	contractMMWaitFor(t, "the anchor to be re-anchored at head", func() bool {
		a, ok, err := st.GetMMCodeAnchor(id)
		return err == nil && ok && a.CheckedSHA == head
	})

	a := contractMMGetAnchor(t, st, id)
	if a.StartLine != 15 || a.EndLine != 17 {
		t.Errorf("range = %d-%d, want 15-17 (the +5 insertion above it)", a.StartLine, a.EndLine)
	}
	if a.AnchorSHA != head || a.AnchorKind != "origin-main" {
		t.Errorf("anchor_sha/kind = %q/%q, want %q/origin-main", a.AnchorSHA, a.AnchorKind, head)
	}
	if a.CheckError != "" {
		t.Errorf("check_error = %q on a survived verify", a.CheckError)
	}
	if got := contractMMGetItem(t, st, id); got.InvalidatedAt != 0 {
		t.Errorf("a SURVIVED anchor's item was invalidated (%q) — hunk overlap never decides changed, "+
			"and this hunk did not even overlap", got.InvalidReason)
	}
	// "After a completed repo pass: kv `mm_head:<repo>` := head"
	if got, err := st.GetKV("mm_head:kraken"); err != nil || got != head {
		t.Errorf("kv mm_head:kraken = %q, %v; want %q — the watermark is what lets the next pass skip", got, err, head)
	}
}

// TestContractMMVerifyCodeHalfSkipsARepoWhoseHeadHasNotMoved pins the
// watermark's purpose: a settled repo costs one fetch and one rev-parse, and
// nothing else. Without it every pass diffs every anchor forever.
func TestContractMMVerifyCodeHalfSkipsARepoWhoseHeadHasNotMoved(t *testing.T) {
	st := useTestStore(t)
	contractMMResetThrottle(t)
	contractMMRepo(t, "kraken")
	const head = "3333333333333333333333333333333333333333"
	contractMMSeedAnchoredItem(t, st, "kraken", "a.go", "package p", 1, 1, head, 10)
	if err := st.SetKV("mm_head:kraken", head); err != nil {
		t.Fatalf("SetKV: %v", err)
	}

	g := contractMMSwapGit(t, contractMMGitRouter(head, "@@ -1,1 +1,1 @@\n", "", "", nil))
	MMVerifyPass(st, contractMMTick())
	contractMMWaitQuiet(t, g)

	if !g.sawSubcommand("fetch") {
		t.Errorf("the pass never fetched — the watermark is checked against a FRESH origin/main, not a stale one:%s",
			g.rendered())
	}
	if g.sawSubcommand("diff") {
		t.Errorf("the pass diffed an anchor in a repo whose head had not moved:%s", g.rendered())
	}
	if g.sawSubcommand("show") {
		t.Errorf("the pass read a blob in a repo whose head had not moved:%s", g.rendered())
	}
}

// TestContractMMVerifyCodeHalfMarkRepoDirtyDefeatsTheWatermark pins
// MarkMMRepoDirty's whole job: the PR tracker's `newlyMerged` block calls it,
// and the next pass must then verify that repo even though the head watermark
// still matches whatever it last recorded.
func TestContractMMVerifyCodeHalfMarkRepoDirtyDefeatsTheWatermark(t *testing.T) {
	st := useTestStore(t)
	contractMMResetThrottle(t)
	contractMMRepo(t, "kraken")
	const head = "4444444444444444444444444444444444444444"
	contractMMSeedAnchoredItem(t, st, "kraken", "a.go", "package p", 1, 1, head, 10)
	if err := st.SetKV("mm_head:kraken", head); err != nil {
		t.Fatalf("SetKV: %v", err)
	}

	MarkMMRepoDirty("kraken")
	g := contractMMSwapGit(t, contractMMGitRouter(head, "@@ -1,0 +2,1 @@\n+x\n", "", "", nil))
	MMVerifyPass(st, contractMMTick())
	contractMMWaitQuiet(t, g)

	if !g.sawSubcommand("diff") {
		t.Errorf("a repo marked dirty was still skipped on the head watermark:%s", g.rendered())
	}
}

// TestContractMMVerifyCodeHalfFetchFailureIsCheckErrorNeverInvalidation is the
// cross-slice invariant at the layer that most wants to break it: a failed READ
// says "I could not tell", and the mark stays live.
func TestContractMMVerifyCodeHalfFetchFailureIsCheckErrorNeverInvalidation(t *testing.T) {
	st := useTestStore(t)
	contractMMResetThrottle(t)
	contractMMRepo(t, "kraken")
	id := contractMMSeedAnchoredItem(t, st, "kraken", "a.go", "package p", 1, 1,
		"5555555555555555555555555555555555555555", 10)

	contractMMSwapGit(t, contractMMGitRouter("", "", "", "",
		fmt.Errorf("fatal: unable to access origin: Could not resolve host: github.com")))
	MMVerifyPass(st, contractMMTick())
	contractMMWaitFor(t, "the fetch failure to be recorded as check_error", func() bool {
		a, ok, err := st.GetMMCodeAnchor(id)
		return err == nil && ok && a.CheckError != ""
	})

	if got := contractMMGetItem(t, st, id); got.InvalidatedAt != 0 {
		t.Fatalf("an unreachable origin invalidated the mark (%q) — a failed read is NEVER an invalidation",
			got.InvalidReason)
	}
	live, err := st.ListLiveMMCodeAnchors()
	if err != nil {
		t.Fatalf("ListLiveMMCodeAnchors: %v", err)
	}
	if len(live) != 1 || live[0].ItemID != id {
		t.Fatalf("live anchors = %+v, want the one that failed to verify still on the worklist", live)
	}
}

// TestContractMMVerifyCodeHalfUnresolvableRepoIsCheckErrorNeverInvalidation is
// the same invariant one step earlier in the chain: `ResolveRepo` failing (a
// repo renamed or removed from ~/Developer) must not read as "the code changed".
func TestContractMMVerifyCodeHalfUnresolvableRepoIsCheckErrorNeverInvalidation(t *testing.T) {
	st := useTestStore(t)
	contractMMResetThrottle(t)
	// deliberately NO fixture checkout for this repo name
	id := contractMMSeedAnchoredItem(t, st, "not-a-repo-on-this-machine", "a.go", "package p", 1, 1,
		"6666666666666666666666666666666666666666", 10)

	g := contractMMSwapGit(t, contractMMGitRouter("", "", "", "", nil))
	MMVerifyPass(st, contractMMTick())
	contractMMWaitFor(t, "the unresolvable repo to be recorded as check_error", func() bool {
		a, ok, err := st.GetMMCodeAnchor(id)
		return err == nil && ok && a.CheckError != ""
	})

	if got := contractMMGetItem(t, st, id); got.InvalidatedAt != 0 {
		t.Fatalf("an unresolvable repo invalidated the mark (%q)", got.InvalidReason)
	}
	if g.sawSubcommand("fetch") {
		t.Errorf("the pass fetched for a repo it could not resolve:%s", g.rendered())
	}
}

// TestContractMMVerifyCodeHalfInvalidatesWhenTheTextIsGone is the death happy
// path: the diff touches the anchor, the ladder finds the text nowhere, and the
// pass invalidates the ITEM, records WHERE it died on the anchor, and closes the
// facts the item had compiled into.
func TestContractMMVerifyCodeHalfInvalidatesWhenTheTextIsGone(t *testing.T) {
	st := useTestStore(t)
	contractMMResetThrottle(t)
	contractMMRepo(t, "kraken")
	const anchorSHA = "7777777777777777777777777777777777777777"
	const head = "8888888888888888888888888888888888888888"
	const text = "\tif err != nil {\n\t\treturn err\n\t}"
	id := contractMMSeedAnchoredItem(t, st, "kraken", "internal/jobs/mm.go", text, 10, 12, anchorSHA, 10)

	// Compile it into the graph, so the close-walk has something to close.
	it := contractMMGetItem(t, st, id)
	anchor := contractMMGetAnchor(t, st, id)
	epID, err := MintMMEpisode(st, it, &anchor)
	if err != nil {
		t.Fatalf("MintMMEpisode: %v", err)
	}
	edge := contractMMSeedEdge(t, st, epID, "the error path returns early")

	// A replacement right on top of the anchor, and a head blob that no longer
	// carries the text in any form.
	contractMMSwapGit(t, contractMMGitRouter(head,
		"@@ -10,3 +10,2 @@\n-\tif err != nil {\n-\t\treturn err\n-\t}\n+\tif err != nil { return fmt.Errorf(\"x: %w\", err) }\n",
		"package p\n\nfunc f() error {\n\treturn nil\n}\n", "", nil))

	MMVerifyPass(st, contractMMTick())
	contractMMWaitFor(t, "the item to be invalidated", func() bool {
		got, err := st.GetMMItem(id)
		return err == nil && got.InvalidatedAt != 0
	})

	got := contractMMGetItem(t, st, id)
	// The contract offers the implementer two spellings for the
	// ladder-exhausted case; both are the contract, so both are accepted.
	if got.InvalidReason != "content-changed" && got.InvalidReason != "content-not-found" {
		t.Errorf("invalid_reason = %q, want content-changed (or the permitted content-not-found)", got.InvalidReason)
	}
	contractMMWaitFor(t, "the death SHA to be recorded on the anchor", func() bool {
		a, ok, err := st.GetMMCodeAnchor(id)
		return err == nil && ok && a.InvalidSHA != ""
	})
	if a := contractMMGetAnchor(t, st, id); a.InvalidSHA != head {
		t.Errorf("invalid_sha = %q, want the head it died at %q", a.InvalidSHA, head)
	}

	contractMMWaitFor(t, "the derived edge to be closed", func() bool {
		edges, err := st.EdgesForEpisode(epID)
		if err != nil {
			return false
		}
		for _, e := range edges {
			if e.ID == edge {
				return e.ExpiredAt != 0
			}
		}
		return false
	})

	// ... and the dead anchor drops off the verify worklist, so nothing pays to
	// re-check it forever.
	live, err := st.ListLiveMMCodeAnchors()
	if err != nil {
		t.Fatalf("ListLiveMMCodeAnchors: %v", err)
	}
	for _, a := range live {
		if a.ItemID == id {
			t.Fatal("the invalidated item's anchor is still on the live worklist")
		}
	}
}

// TestContractMMVerifyCodeHalfCircuitBreaker pins the stated evidential rule: a
// repo where EVERY live anchor would die in one pass is a repo-level problem
// (the wrong checkout, a force-push, a botched fetch), not N independent content
// changes — so all of them record check_error and NONE is invalidated.
func TestContractMMVerifyCodeHalfCircuitBreaker(t *testing.T) {
	st := useTestStore(t)
	contractMMResetThrottle(t)
	contractMMRepo(t, "kraken")
	const anchorSHA = "9999999999999999999999999999999999999999"
	const head = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	ids := []int64{
		contractMMSeedAnchoredItem(t, st, "kraken", "a.go", "the first marked block", 10, 10, anchorSHA, 10),
		contractMMSeedAnchoredItem(t, st, "kraken", "b.go", "the second marked block", 10, 10, anchorSHA, 20),
		contractMMSeedAnchoredItem(t, st, "kraken", "c.go", "the third marked block", 10, 10, anchorSHA, 30),
	}

	// Every anchor is touched, and no blob anywhere carries its text.
	contractMMSwapGit(t, contractMMGitRouter(head,
		"@@ -10,1 +10,1 @@\n-old\n+new\n", "nothing recognisable at all\n", "", nil))

	MMVerifyPass(st, contractMMTick())
	contractMMWaitFor(t, "every anchor in the repo to record a check_error", func() bool {
		for _, id := range ids {
			a, ok, err := st.GetMMCodeAnchor(id)
			if err != nil || !ok || a.CheckError == "" {
				return false
			}
		}
		return true
	})

	for _, id := range ids {
		if got := contractMMGetItem(t, st, id); got.InvalidatedAt != 0 {
			t.Errorf("item %d was invalidated (%q) despite EVERY anchor in the repo dying at once — "+
				"that is evidence of a repo-level problem, not of %d content changes",
				id, got.InvalidReason, len(ids))
		}
	}
}

// ---------------------------------------------------------------------------
// the graph cutover
// ---------------------------------------------------------------------------

// TestContractMMGraphPassMintsNoRetiredEpisodeKinds is the retirement's
// behavioural half: with the five sweeps deleted, a pass over a store that is
// full of exactly the material they used to ingest (job specs, artifacts,
// feedback) produces NO episode of any retired kind. The absence is what makes
// "the graph reflects carl's mental model" true by construction rather than by
// filtering.
func TestContractMMGraphPassMintsNoRetiredEpisodeKinds(t *testing.T) {
	st := useTestStore(t)
	job := mustCreateJob(t, st, store.Job{
		Slug: "sweepable", Repo: "kraken", State: "done",
		Body: "a job spec full of distinctive vocabulary the old sweep would have ingested",
	})
	if err := st.SyncArtifacts(job.ID, []store.ArtifactFile{
		{Path: "00-overview.md", Bytes: []byte("# overview\n\nlots of prose.\n"), Mtime: 1},
		{Path: "50-components.md", Bytes: []byte("# components\n\nmore prose.\n"), Mtime: 1},
	}, nil); err != nil {
		t.Fatalf("SyncArtifacts: %v", err)
	}

	graphLastPass.Store(0)
	t.Cleanup(func() { graphLastPass.Store(0) })
	t.Cleanup(waitGraphSlow)
	graphPass(st, time.UnixMilli(1_700_000_000_000))
	waitGraphSlow()

	pending, err := st.PendingEpisodes(1_000)
	if err != nil {
		t.Fatalf("PendingEpisodes: %v", err)
	}
	retired := map[string]bool{
		"transcript": true, "feedback": true, "artifact": true, "job": true, "watchlist": true,
	}
	for _, ep := range pending {
		if retired[ep.Kind] {
			t.Errorf("graphPass produced an episode of the RETIRED kind %q (ref %q) — the five sweeps are gone "+
				"and only mental-model episodes may enter the graph now", ep.Kind, ep.Ref)
		}
	}
}

// TestContractMMGraphPassStillDrainsMentalModelEpisodes is the other side: the
// retirement must not take the pass' surviving job with it. A pending
// mental-model episode is still there for the nightly /graph job to drain.
func TestContractMMGraphPassStillDrainsMentalModelEpisodes(t *testing.T) {
	st := useTestStore(t)
	id := contractMMAddItem(t, st, store.MMItem{
		Kind: "note", Source: "manual", Content: "a fact carl wrote down", CreatedAt: 1_700_000_000_000,
	})
	it := contractMMGetItem(t, st, id)
	if _, err := MintMMEpisode(st, it, nil); err != nil {
		t.Fatalf("MintMMEpisode: %v", err)
	}

	graphLastPass.Store(0)
	t.Cleanup(func() { graphLastPass.Store(0) })
	t.Cleanup(waitGraphSlow)
	graphPass(st, time.UnixMilli(1_700_000_100_000))
	waitGraphSlow()

	pending, err := st.PendingEpisodes(1_000)
	if err != nil {
		t.Fatalf("PendingEpisodes: %v", err)
	}
	found := false
	for _, ep := range pending {
		if ep.Ref == fmt.Sprintf("mm:%d", id) {
			found = true
			if ep.Kind != "mental-model" {
				t.Errorf("the minted episode's kind = %q, want mental-model", ep.Kind)
			}
		}
	}
	if !found {
		t.Fatalf("the mental-model episode for mm:%d is not pending after graphPass — the /graph job "+
			"has nothing to compile", id)
	}
}

// ---------------------------------------------------------------------------
// the Q11 recall retirement
// ---------------------------------------------------------------------------

// TestContractMMRecallHookEventsIsUserPromptSubmitOnly pins Q11's whole
// observable surface at this layer: query-on-own-output is the echo machine
// regardless of corpus, so the PostToolUse arm retires and the prompt arm — the
// one whose query is carl's own text — stays.
func TestContractMMRecallHookEventsIsUserPromptSubmitOnly(t *testing.T) {
	if len(recallHookEvents) != 1 || recallHookEvents[0] != "UserPromptSubmit" {
		t.Fatalf("recallHookEvents = %v, want exactly [UserPromptSubmit] — Q11 retires the PostToolUse arm",
			recallHookEvents)
	}
}
