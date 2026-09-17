package jobs

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"kraken/internal/core"
	"kraken/internal/store"
)

// graph.go — the knowledge graph's daemon-side halves: graphPass (the
// throttled embed/layout sweep) and CreateGraphJob (the programmatic creator
// for the two graph job shapes).
//
// graphPass does NOT extract. Extraction costs a model and happens inside a
// `/graph` cell; everything here is mechanical — fill in vectors, refresh the
// cached layout. That split is the whole reason the pass can run on a
// 15-second daemon tick at all.
//
// WHAT USED TO BE HERE, and why it is gone. This file once carried five ingest
// sweeps — feedback rows, job specs, artifacts, and a byte-by-byte drain of
// every claude transcript on the machine — that between them WERE the graph's
// corpus. The sibling memory-usefulness job measured what that corpus was
// worth: of 2,556 hand-classified fact deliveries across 15 days, FOUR were
// ever used (0.16%), because a graph swept from what agents wrote echoes the
// work the agent just finished (68-79% by a rare-token test), restates skills
// at lower fidelity, and leaks across roles and repos — while its 2,326-episode
// backlog meant recent work was not recallable at all. The corpus was wrong,
// not the plumbing. So the graph is now compiled from the MENTAL MODEL alone
// (mm.go): one pending `mental-model` episode per thing carl marks as read,
// decided, or noted, at carl's authoring pace, which is what kills the backlog
// problem outright. The sweeps, their cursors and their whole watermark
// discipline are deleted rather than disabled — a sweep behind a flag is a
// sweep somebody turns back on.
//
// It is still shaped after tokensPass: same self-pacing, same "stamp the
// throttle before any early bail". The one structural difference is that
// graphPass is a FREE FUNCTION, not a daemonState method, so its throttle lives
// in the package-level atomics below rather than in daemonState. That keeps the
// daemon.go edit to a single line — the pass owns its own pacing, which is also
// what lets a test drive it without building a daemon.

const (
	// graphPassInterval paces the whole pass. The daemon ticks every 15s
	// (DaemonInterval); a minute is slow enough that the pass is not a
	// per-tick cost and fast enough that a freshly compiled fact gets its
	// vector while the work it describes is still recent.
	graphPassInterval = time.Minute

	// graphEmbedBatch is how many missing vectors one embedding sweep asks
	// for. core.Embedder batches internally at 128; 64 keeps one pass's API
	// spend and failure blast radius small.
	graphEmbedBatch = 64

	// graphJobVecBatch caps how many job spec vectors one pass refreshes.
	// Each is one embedding call's worth of text, and the cache only has to
	// be warm by the time a job STARTS, not the moment it is written.
	graphJobVecBatch = 8

	// graphSpecEmbedCap truncates a job spec before embedding it. The
	// embedding model's own input limit is far larger; this bounds what a
	// pathological spec costs, and the head of a spec is what states its
	// subject.
	graphSpecEmbedCap = 8000

	// graphLayoutIterationsCold / graphLayoutIterationsWarm are the force
	// simulation's budgets. A layout seeded from existing positions only has
	// to settle the nodes that moved, which is what makes a refresh cheap
	// enough to run on a pass at all.
	graphLayoutIterationsCold = 300
	graphLayoutIterationsWarm = 60

	// graphLayoutInterval floors how often the layout recomputes. The rev
	// check alone re-ran the whole simulation every minute during any active
	// ingest (MaxEdgeID moves on every edge), which at the spec's projected
	// 50k nodes is ~4s of CPU per minute forever — and it holds
	// graphSlowRunning, starving the embedding sweep behind it (review
	// finding). Ten minutes keeps the picture fresh against a graph that
	// changes nightly.
	graphLayoutInterval = 10 * time.Minute
)

// graphLastLayout is graphLayoutInterval's watermark, in-memory like
// graphLastPass and for the same reason: losing it on restart costs one
// extra layout, not correctness.
var graphLastLayout atomic.Int64

// graphJobVecKey / graphJobVecHashKey name the two kv rows that make the
// SPAWN path offline: the cached embedding of a job's spec, and the hash of
// the text that vector embedded. graphContext reads the first and never
// calls a network; this pass writes both. Two rows rather than one JSON
// object because the vector is the hot read and the hash is only ever
// compared by the writer.
func graphJobVecKey(jobID int64) string     { return fmt.Sprintf("kg.jobvec.%d", jobID) }
func graphJobVecHashKey(jobID int64) string { return fmt.Sprintf("kg.jobvechash.%d", jobID) }

// graphLastPass is the pass throttle (unix millis of the last run). In-memory
// like d.TokenScan and lastDreamDate: losing it on a restart costs one extra
// sweep, which is one directory walk and no writes when nothing moved.
var graphLastPass atomic.Int64

// graphSlowRunning single-flights the background half of the pass — the same
// shape usagePassRunning and dreamRunning use, and for the sharper version of
// their reason: the embedding sweep makes a NETWORK call (up to a 60s timeout
// in core.Embedder) and the layout refresh is a force simulation over the
// whole graph. Neither may sit inside tick(), which drives every cell's state
// derivation; and two of them overlapping would double-embed the same rows.
var graphSlowRunning atomic.Bool

// graphPass is the daemon's per-tick knowledge-graph pass. Skipped under
// --mirror by its caller, on the same rule as every other pass in the tick:
// mirror runs state derivation and the block-event artifact sync and nothing
// else — and this one both writes the database and spends money.
//
// Everything it does now is in the single-flighted goroutine (embeddings,
// layout — see graphSlowRunning): the foreground half was the ingest sweeps,
// and episodes arrive from mm.go's mint at the moment carl marks something
// rather than from anything on a timer. What is left is the THROTTLE, which
// still earns its place — it is what keeps the slow half from being kicked
// four times a minute.
func graphPass(st *store.Store, now time.Time) {
	if st == nil {
		return // file-mode entrypoints have no graph to sweep
	}
	if last := graphLastPass.Load(); last != 0 && now.UnixMilli()-last < graphPassInterval.Milliseconds() {
		return
	}
	// Stamped HERE, before any of the work below can bail, for tokensPass'
	// reason: a pass that fails on its first query has still had its turn,
	// and a throttle stamped only on success turns a persistent failure into
	// a per-tick retry that logs the same line four times a minute.
	graphLastPass.Store(now.UnixMilli())

	kickGraphSlow(st)
}

// addEpisode hashes and enqueues one episode, returning its id.
//
// The hash covers kind, ref AND content, which is what makes re-minting an
// unchanged item free: AddEpisode dedupes on it and returns the existing row.
// It is the convention every episode writer in this package shares, and after
// the sweep retirement it has exactly one caller — mm.go's MintMMEpisode — so
// the convention now lives where the only writer can see it.
func addEpisode(st *store.Store, kind, ref, content string, ts int64) (int64, error) {
	sum := sha256.Sum256([]byte(kind + "\x00" + ref + "\x00" + content))
	id, _, err := st.AddEpisode(store.Episode{
		Kind:    kind,
		Ref:     ref,
		Content: content,
		Hash:    hex.EncodeToString(sum[:]),
		TS:      ts,
	})
	return id, err
}

// capString truncates to at most n bytes on a rune boundary, marking the cut.
// Silent truncation is what this codebase keeps paying for; the marker is
// what tells the extraction agent to go back to the ref.
func capString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	// Back up to a rune start so the cut never leaves half a multi-byte
	// character: the content column is TEXT, and a torn rune is a corrupt row
	// rather than a shorter one.
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "\n…[truncated]"
}

// ---------------------------------------------------------------------------
// The slow half: embeddings, the job-spec vector cache, and layout
// ---------------------------------------------------------------------------

// kickGraphSlow runs the parts of the pass that must not sit inside the
// daemon tick — a network round trip and a force simulation — in one
// single-flighted goroutine, the same shape KickUsagePass uses.
func kickGraphSlow(st *store.Store) {
	if !graphSlowRunning.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer graphSlowRunning.Store(false)
		graphSlowPass(st)
	}()
}

// graphSlowPass is kickGraphSlow's body, factored out so a test can drive it
// synchronously.
func graphSlowPass(st *store.Store) {
	emb, err := core.NewEmbedder()
	switch {
	case errors.Is(err, core.ErrNoEmbedder):
		// The normal state of a machine with no key configured. Retrieval
		// degrades to BM25-only and nothing here is a failure, so this is
		// SILENT — a line per minute saying a feature is not configured is
		// noise that trains the reader to ignore the log.
		emb = nil
	case err != nil:
		dlogErr("graph pass: embedder: %v", err)
		emb = nil
	}
	if emb != nil {
		if err := embedMissing(st, emb); err != nil {
			dlogErr("graph pass: embed: %v", err)
		}
		if err := refreshJobVectors(st, emb); err != nil {
			dlogErr("graph pass: job vectors: %v", err)
		}
	}
	if err := refreshLayout(st); err != nil {
		dlogErr("graph pass: layout: %v", err)
	}
}

// embedMissing fills in one batch of absent node/edge vectors. Rows are
// written without vectors and filled in here, so a down API or a missing key
// never blocks a write — it only leaves rows in this queue.
func embedMissing(st *store.Store, emb core.Embedder) error {
	miss, err := st.MissingEmbeddings(graphEmbedBatch)
	if err != nil || len(miss) == 0 {
		return err
	}
	texts := make([]string, 0, len(miss))
	for _, m := range miss {
		texts = append(texts, m.Text)
	}
	vecs, err := emb.Embed(texts)
	if err != nil {
		return err
	}
	if len(vecs) != len(miss) {
		// core.Embedder documents input order and count; a mismatch would
		// pair a vector with the wrong row, which is silent and permanent.
		return fmt.Errorf("embed returned %d vectors for %d texts", len(vecs), len(miss))
	}
	for i, m := range miss {
		if err := st.SetEmbedding(m.Kind, m.ID, core.EncodeVector(vecs[i])); err != nil {
			return err
		}
	}
	return nil
}

// refreshJobVectors keeps the job-spec vector cache current. This is what
// makes graphContext offline: the spawn path reads a stored vector rather
// than embedding the spec at spawn time, so a cell start never waits on (or
// fails because of) a network call.
//
// The hash is of the exact text embedded, so a spec edited back to a previous
// wording re-uses the cached vector rather than paying for it twice.
func refreshJobVectors(st *store.Store, emb core.Embedder) error {
	jobs, err := st.ListJobs()
	if err != nil {
		return err
	}
	n := 0
	for _, j := range jobs {
		if n >= graphJobVecBatch {
			break
		}
		text := capString(strings.TrimSpace(j.Body), graphSpecEmbedCap)
		if text == "" {
			continue
		}
		sum := sha256.Sum256([]byte(text))
		want := hex.EncodeToString(sum[:])
		if have, err := st.GetKV(graphJobVecHashKey(j.ID)); err == nil && have == want {
			continue
		} else if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		vecs, err := emb.Embed([]string{text})
		if err != nil {
			return err
		}
		if len(vecs) != 1 {
			return fmt.Errorf("embed returned %d vectors for 1 text", len(vecs))
		}
		n++
		// Vector first, hash second. A crash between the two writes leaves
		// the vector uncached-looking and it is simply recomputed; the other
		// order would record "cached" against a vector that was never stored,
		// and nothing would ever repair it.
		if err := st.SetKV(graphJobVecKey(j.ID), base64.StdEncoding.EncodeToString(core.EncodeVector(vecs[0]))); err != nil {
			return err
		}
		if err := st.SetKV(graphJobVecHashKey(j.ID), want); err != nil {
			return err
		}
	}
	return nil
}

// refreshLayout recomputes the cached node positions when the graph's
// revision has moved. The revision marker is the max edge id (GraphStats), and
// it is stored on every layout row — so "the layout is current" is a single
// integer comparison rather than a diff of the topology.
//
// Seeded from the existing positions, which is what keeps the picture stable:
// a new node joins the map where its neighbours already are instead of
// reshuffling everything a viewer had learned to read.
func refreshLayout(st *store.Store) error {
	// Time floor first, before any store read: see graphLayoutInterval.
	nowMS := time.Now().UnixMilli()
	if last := graphLastLayout.Load(); last != 0 && nowMS-last < graphLayoutInterval.Milliseconds() {
		return nil
	}
	stats, err := st.GetGraphStats()
	if err != nil {
		return err
	}
	if stats.Nodes == 0 {
		return nil
	}
	prev, err := st.Layout()
	if err != nil {
		return err
	}
	if len(prev) > 0 {
		current := true
		for _, p := range prev {
			if p.Rev != stats.MaxEdgeID {
				current = false
				break
			}
		}
		// A node added with no edge does not move MaxEdgeID, so the row count
		// is checked too — otherwise an isolated node would never be placed.
		if current && int64(len(prev)) == stats.Nodes {
			return nil
		}
	}
	nodes, err := st.AllNodes()
	if err != nil {
		return err
	}
	edges, err := st.AllEdges()
	if err != nil {
		return err
	}
	iterations := graphLayoutIterationsCold
	if len(prev) > 0 {
		iterations = graphLayoutIterationsWarm
	}
	pos := core.ComputeLayout(nodes, edges, prev, iterations)
	for i := range pos {
		pos[i].Rev = stats.MaxEdgeID
	}
	if err := st.SetLayout(pos); err != nil {
		return err
	}
	// Stamped on SUCCESS, unlike the pass throttle: a failed layout write
	// should retry at the rev check's own pace, not sit out the full floor.
	graphLastLayout.Store(time.Now().UnixMilli())
	return nil
}

// ---------------------------------------------------------------------------
// Graph jobs
// ---------------------------------------------------------------------------

// The two graph job shapes (Q3). Both send the cell into the same skill; the
// spec text is what selects the mode, because a job's `type` was removed from
// this codebase deliberately (Q14) and re-introducing one for this feature
// would be the only one left.
const (
	// GraphModeIngest drains the pending episode queue into nodes and edges.
	// Mechanical, opens no PR.
	GraphModeIngest = "ingest"
	// GraphModeRevision carries carl's instruction to correct the graph:
	// merge, split, re-describe, or invalidate.
	GraphModeRevision = "revision"
)

// CreateGraphJob writes a graph job's row and returns it loaded, for the
// caller to submit — the sibling of CreateDreamJob, and built the same way
// for the same reason: render the job's TEXT, ParseJob it, and create the row
// from the parsed fields, so the row can never disagree with what the same
// text would have produced had a human pasted it into the board's editor.
//
// instruction is carl's revision brief and is required in revision mode
// (a revision job with nothing to revise would spawn a cell to do nothing)
// and ignored in ingest mode.
//
// ingestCap is how many episodes the run may drain, written into the ingest
// spec as the `Per-run episode cap:` line the skill reads. It is a PARAMETER
// rather than a constant because the only caller that can size it — kickGraph,
// via graphIngestCap — is the only one holding the queue depth; a hand-made
// job that passes 0 gets the skill's own default of 25. Revision mode ignores
// it entirely: a revision run drains nothing.
func CreateGraphJob(st *store.Store, slug, mode, instruction string, ingestCap int) (*Job, error) {
	if st == nil {
		return nil, fmt.Errorf("graph job %s: no store", slug)
	}
	switch mode {
	case GraphModeIngest:
	case GraphModeRevision:
		if strings.TrimSpace(instruction) == "" {
			return nil, fmt.Errorf("graph job %s: revision mode needs an instruction", slug)
		}
	default:
		return nil, fmt.Errorf("graph job %s: unknown mode %q", slug, mode)
	}
	content := graphJobContent(slug, mode, instruction, ingestCap)
	parsed := ParseJob(slug+JobSuffix, []byte(content))
	if parsed.Err != "" {
		return nil, fmt.Errorf("graph job %s does not parse: %s", slug, parsed.Err)
	}
	if strings.TrimSpace(parsed.Body) == "" {
		// SubmitJob refuses an empty spec too, so this is belt and braces on
		// top of the template below, exactly as CreateDreamJob carries — but
		// failing the CREATE leaves no job at all rather than an unstartable
		// draft.
		return nil, fmt.Errorf("graph job %s has an empty body", slug)
	}
	if _, err := st.CreateJob(&store.Job{
		Slug: slug, Repo: parsed.Repo, Base: parsed.Base,
		Solo: parsed.Solo, Model: parsed.Model,
		Body: parsed.Body,
	}); err != nil {
		return nil, err
	}
	sr, err := st.GetJobBySlug(slug)
	if err != nil {
		return nil, fmt.Errorf("graph job %s created but could not be reloaded: %w", slug, err)
	}
	return loadJobFull(st, sr)
}

// graphJobContent renders a graph job's file text. solo: false, so the cell
// may fan extraction out across episodes. Nothing in the frontmatter asks for
// a start: the tick that created this job submits it itself on the same pass
// (daemon.go's startCreatedJob).
//
// The body says as little as possible: the procedure lives in the skill, and
// the only things the skill cannot know are which mode this job is and what
// carl asked for. It is read BY PATH because a personal ~/.claude/skills/graph
// would otherwise shadow the repo-owned copy materialized into the cell's
// worktree — the same hazard dreamJobContent spells out.
//
// The one thing the body does carry beyond the mode is the ingest cap, and it
// is carried as SPEC TEXT rather than as a flag the skill would have to be
// taught to read from somewhere else: the spec is the only channel between the
// daemon that knows the queue depth and the cell that drains it.
func graphJobContent(slug, mode, instruction string, ingestCap int) string {
	head := "---\n" +
		"job: " + slug + "\n" +
		"repo: kraken\n" +
		"solo: false\n" +
		"---\n" +
		"\n" +
		"Open `.claude/skills/graph/SKILL.md` from this worktree **with the Read tool** and\n" +
		"follow it end to end. Read it by path rather than invoking `/graph`: a personal skill\n" +
		"of the same name shadows the repo-owned copy, and the repo-owned copy is the one that\n" +
		"knows about cells. Never `cat` it — a Bash `cat` of a skill this size lands in a\n" +
		"persisted file and leaves you a 2KB preview.\n" +
		"\n"
	if mode == GraphModeRevision {
		return head +
			"## Mode: revision\n" +
			"\n" +
			"Follow the skill's **Revision mode** section. Carl's instruction, verbatim:\n" +
			"\n" +
			instruction + "\n" +
			"\n" +
			"Change only what the instruction reaches. A revision job opens no PR — the\n" +
			"graph lives in `kraken.db`, not in git.\n"
	}
	// 0 means "no caller sized this" (a hand-made job, or a caller written
	// before the parameter existed), and the floor — which is also the value
	// the skill defaults to when the spec carries no cap line at all — is the
	// honest answer to that, not a cap of zero, which would spawn a cell to
	// drain nothing.
	perRun := ingestCap
	if perRun <= 0 {
		perRun = graphIngestCapMin
	}
	return head +
		"## Mode: ingest\n" +
		"\n" +
		fmt.Sprintf("Per-run episode cap: %d\n", perRun) +
		"\n" +
		"Every pending episode is now one thing CARL marked into his mental model — a decision\n" +
		"he made, a passage or code range he read, a note he left. Compile them: drain\n" +
		"`k graph pending`, extract the entities and facts each one states, resolve against\n" +
		"what already exists, and write with `k graph add`. Follow the skill's **Ingest mode**\n" +
		"section. Stop when the queue is empty or the cap above is reached, whichever comes\n" +
		"first.\n" +
		"\n" +
		"What you are compiling is what carl KNOWS, not what agents observed — the transcript,\n" +
		"feedback, artifact and job-spec sweeps are retired, so nothing else feeds this queue.\n" +
		"Extract accordingly: a fact worth writing is one that would change how an agent\n" +
		"explains something to carl.\n" +
		"\n" +
		"An ingest job opens no PR — nothing it produces belongs in git.\n"
}

// ---------------------------------------------------------------------------
// kickGraph — the nightly ingest trigger
// ---------------------------------------------------------------------------

// graphIngestKVKey is the kv watermark holding the LOCAL DATE (dream's
// dreamDateLayout) of the last nightly ingest job created. A date rather than
// an elapsed duration for kickDream's stated reason: a Mac asleep at midnight
// fires on the first tick after it wakes instead of skipping the night.
const graphIngestKVKey = "graph_ingest_last"

// The nightly batch's shape. A cap that scales with the BACKLOG rather than a
// constant, because a constant is what the queue outran back when the sweeps
// filled it: measured on this machine 2026-08-03, 1,997 episodes sat pending
// against the transcript sweep's 2,000-episode high-water mark, with 86 and 85
// new episodes arriving on each of the two days before — and on 2026-08-01 not
// one of them was a transcript, since the sweep was already backpressured off.
// A fixed 25 a night against ~85 a day is a drain that loses ground every
// night, not a slow one.
//
// The sweeps are gone and carl's own marking pace is the arrival rate now, so
// the scaling should rarely leave the floor. It is KEPT rather than collapsed
// back to a constant because the shape costs nothing and the failure it
// prevents — a backlog the nightly drain never catches — is one this queue has
// actually had, and a burst of marking after a week away reproduces it.
//
// The floor keeps a quiet week cheap (an empty-ish queue still gets exactly one
// batch, and 25 is what the skill has always defaulted to), and the divisor was
// picked so the retired high-water mark itself asked for 100 — comfortably past
// that measured arrival rate, so the queue moved backwards instead of forwards.
//
// graphIngestCapMin has a THIRD site that must agree with it and that Go cannot
// see: the skill's "when the spec carries no such line, use 25" fallback in
// .claude/skills/graph/SKILL.md. Raising this floor silently changes the
// hand-made-job default (graphJobContent's <= 0 branch) while leaving that
// fallback behind, so a cell reading a capless spec would drain a different
// number than the same cell reading a spec the daemon wrote. Move both.
//
// The ceiling exists because nothing bounds the pending queue at all — the
// backpressure that once gated the transcript sweep went with the sweep, and
// mm mints are unthrottled by design (carl marking a long document's tables
// one by one is a burst, and refusing to record one because a queue is deep
// would lose the thing he actually did). Without the ceiling one night's cell
// could be handed a batch it cannot finish in one run, which costs the spend
// and produces a partially-drained queue either way.
const (
	graphIngestCapDivisor = 20
	graphIngestCapMin     = 25
	graphIngestCapMax     = 150
)

// graphIngestCap sizes one nightly run's batch from the pending queue's depth:
// clamp(pending/20, 25, 150). Integer division, so the cap only steps up once
// the backlog has actually grown by a whole batch-twentieth.
func graphIngestCap(pending int64) int {
	n := pending / graphIngestCapDivisor
	if n < graphIngestCapMin {
		return graphIngestCapMin
	}
	if n > graphIngestCapMax {
		return graphIngestCapMax
	}
	return int(n)
}

// kickGraph creates at most one nightly ingest job per local calendar day,
// and only when there is actually something to drain. It is what makes the
// whole extraction side reachable: without a caller, CreateGraphJob was
// exported dead code, graphPass enqueued episodes forever, and nothing ever
// turned one into a fact (review finding — the feature shipped inert).
//
// Deliberately smaller than kickDream: no swept-window bookkeeping (the
// pending queue IS the work list, owned by the store), no in-memory date
// guard (one kv read per tick is nothing), and the cell-storm guard is
// structural — one date row, plus a skip while any active graph job exists,
// so a night whose job is still running does not stack a second one.
func kickGraph(st *store.Store, now time.Time) *Job {
	if st == nil {
		return nil
	}
	today := now.Format(dreamDateLayout)
	if last, err := st.GetKV(graphIngestKVKey); err == nil && last >= today {
		return nil // >= for kickDream's backwards-clock reason
	}
	stats, err := st.GetGraphStats()
	if err != nil || stats.PendingEpisodes == 0 {
		return nil // nothing to drain: no job, and the date stays unstamped
	}
	jobs, err := st.ListJobs()
	if err != nil {
		return nil
	}
	slug := "graph-" + today
	for _, j := range jobs {
		if j.Slug == slug {
			// Already created today (the kv write below failed, or a hand-made
			// job took the name): stamp and stand down rather than colliding.
			_ = st.SetKV(graphIngestKVKey, today)
			return nil
		}
		// Any RUNNING graph job — last night's still draining — means no new
		// one: two cells writing k graph add concurrently is safe for the
		// store but doubles the extraction spend on the same queue.
		//
		// working/blocked, spelled out rather than "not done": those two are
		// exactly the states in which a cell exists. An older graph job left
		// as a DRAFT is one whose start FAILED, and it will never drain
		// anything on its own — declining tonight's kick for it would stop the
		// ingest queue forever with nothing on screen saying why.
		if strings.HasPrefix(j.Slug, "graph-") && (j.State == "working" || j.State == "blocked") {
			return nil
		}
	}
	// The stats read above is the only place the queue depth is in hand, so it
	// is where the batch gets sized — see graphIngestCap for why a constant is
	// not enough.
	created, err := CreateGraphJob(st, slug, GraphModeIngest, "", graphIngestCap(stats.PendingEpisodes))
	if err != nil {
		dlogErr("kick graph: %v", err)
		return nil
	}
	if err := st.SetKV(graphIngestKVKey, today); err != nil {
		// The slug check above makes a re-fire on the next tick a no-op, so a
		// failed stamp costs one extra ListJobs, not a second job.
		dlogErr("kick graph: stamp date: %v", err)
	}
	return created
}
