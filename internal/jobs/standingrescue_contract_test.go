package jobs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"kraken/internal/core"
	"kraken/internal/store"
	"kraken/internal/util"
)

// standingrescue_contract_test.go — athena-logic's BLIND contract tests for
// the standing-agent usage-limit rescue (50-contract.md, slices P and S).
// Written from the contract text alone: none of internal/jobs/limithook.go,
// limitrescue.go, the orchestrator's file, rotation.go, daemon.go, standingrescue.go,
// internal/api/limithit.go or cmd/kraken/*.go was read in its working-tree
// state while this was open. Existing helper SHAPES were learned from
// `git show HEAD:` (the pre-change versions) and from the package's own
// _test.go files, both of which are explicitly permitted.
//
// Dimension: LOGIC — the stated invariants, the happy paths, the core
// behavioural rules. The registry's contents, the key shapes, the swap
// watermark's adoption conjunction, the classification of a standing stall,
// the identity rule the kill guard applies, and the two passes' main arms.
// Boundaries, error paths and refusals belong to the sibling author
// (athena-edge); where a refusal is asserted here it is because the contract
// states it as the DEFINITION of a happy path (the "iff ALL of" conjunction,
// the "only when" identity rule) rather than as an error case.
//
// Every package-level identifier declared here is prefixed `contractStanding`
// — the jobs package shares one declaration space across every _test.go file
// in it, and `contractLimit`, `contractResumeLogic` and `edge…` are taken.
//
// Environment: every store-backed test calls contractStandingRigNew, which
// isolates HOME (via useTestStore), wires a throwaway store, empties PATH so
// nothing on these paths can shell out, and swaps every effectful package
// seam — selectTarget, swapMain, mainIdentityEmail, runUsagePass, notifyFn,
// probeOnline, respawnPeerPane and the new standingRespawn — for recorders.
// Nothing here reads or writes the real machine's pool, keychain, kraken.db,
// tmux server or notification centre.

// ---------------------------------------------------------------------------
// harness
// ---------------------------------------------------------------------------

// contractStandingRespawnCall records one invocation of the standingRespawn
// seam. The seam is the whole observable surface of the resume arm: its
// production value kills a tmux session, so no test may ever reach it.
type contractStandingRespawnCall struct {
	agentID string
	session string
	live    map[string]bool
}

// contractStandingRig owns the swapped seams and the record of what the code
// under test asked them to do. Fields the test wants to steer (Target,
// SwapTo, SwapErr, RespawnErr) are set on the rig AFTER construction; the
// closures read them live.
type contractStandingRig struct {
	st *store.Store

	// steering
	Target     string // what selectTarget answers; "" means no eligible target
	SwapTo     string // the email swapMain reports having swapped TO
	SwapErr    error
	RespawnErr error

	// recording
	Swaps        []string
	Respawns     []contractStandingRespawnCall
	PaneRespawns []string
}

// contractStandingRigNew builds the isolated world every store-backed case
// below runs in.
func contractStandingRigNew(t *testing.T) *contractStandingRig {
	t.Helper()
	rig := &contractStandingRig{SwapTo: "pool-b@example.com"}
	// useTestStore sandboxes HOME into a temp dir and wires ActiveStore,
	// unwiring both on cleanup.
	rig.st = useTestStore(t)

	// notify() shells out to osascript, and a test that fires a real desktop
	// notification has a side effect on the machine running it. Emptying PATH
	// makes every exec.Command lookup fail harmlessly — notify logs and
	// returns — whichever spelling (notify or the notifyFn seam) the pass
	// reaches for.
	t.Setenv("PATH", t.TempDir())

	// Process-global atomic shared with the daemon's own usage poll; force a
	// known state so a lingering goroutine can't make a CompareAndSwap here
	// spuriously fail (rotation_test.go's stubRotation does the same).
	usagePassRunning.Store(false)

	origSelect, origSwap, origNotify, origPass := selectTarget, swapMain, notifyFn, runUsagePass
	origMain, origProbe, origPane := mainIdentityEmail, probeOnline, respawnPeerPane
	origStanding := standingRespawn
	t.Cleanup(func() {
		selectTarget, swapMain, notifyFn, runUsagePass = origSelect, origSwap, origNotify, origPass
		mainIdentityEmail, probeOnline, respawnPeerPane = origMain, origProbe, origPane
		standingRespawn = origStanding
	})

	notifyFn = func(string, string) {}
	runUsagePass = func() error { return nil }
	mainIdentityEmail = func() string { return "main@example.com" }
	// The retryable arm runs through d.gateRetry, which probes connectivity.
	// A test must never dial the network; answering "offline" also keeps the
	// poke itself (a real tmux send) from ever being attempted.
	probeOnline = func() bool { return false }
	selectTarget = func([]core.AccountUsage, string, int64) (string, string) {
		if rig.Target == "" {
			return "", "no eligible target in this fixture"
		}
		return rig.Target, ""
	}
	swapMain = func(slug string) (string, string, error) {
		rig.Swaps = append(rig.Swaps, slug)
		if rig.SwapErr != nil {
			return "", "", rig.SwapErr
		}
		return "main@example.com", rig.SwapTo, nil
	}
	respawnPeerPane = func(p *core.Peer, _ string) error {
		if p != nil {
			rig.PaneRespawns = append(rig.PaneRespawns, p.Pane)
		}
		return nil
	}
	standingRespawn = func(a standingAgent, _ *store.Store, live map[string]bool) error {
		rig.Respawns = append(rig.Respawns, contractStandingRespawnCall{
			agentID: a.ID, session: a.Session, live: live,
		})
		return rig.RespawnErr
	}
	return rig
}

// contractStandingSnapshot writes a usage snapshot with a main row and one
// pool row, which is what selectRescueTarget needs before it will consult the
// selector at all.
func contractStandingSnapshot(t *testing.T) {
	t.Helper()
	fresh := time.Now().UnixMilli()
	snap := core.UsageSnapshot{
		FetchedAt: fresh,
		Accounts: []core.AccountUsage{
			{Slug: "main", Email: "main@example.com", FetchedAt: fresh, Session: &core.UsageWindow{Percent: 99}},
			{Slug: "pool-b", Email: "pool-b@example.com", FetchedAt: fresh, Session: &core.UsageWindow{Percent: 4}},
		},
	}
	if err := writeUsageSnapshot(snap, UsageSnapshotPath()); err != nil {
		t.Fatal(err)
	}
}

// contractStandingEnableIris writes the credential file iris' enabled-gate
// reads. A bare xoxp- token needs nothing beside it (an xoxc- one would also
// need the cookie), so this is the minimal "configured" state.
func contractStandingEnableIris(t *testing.T) {
	t.Helper()
	// Process env wins over the file, so any inherited value has to be
	// neutralised before the file can decide the answer.
	t.Setenv("SLACK_USER_TOKEN", "")
	t.Setenv("SLACK_D_COOKIE", "")
	path := core.SlackEnvPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("SLACK_USER_TOKEN=xoxp-fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// contractStandingRow returns the production descriptor for one agent id.
// Tests use the REAL row wherever the row's own wiring is not the thing under
// test, so a pass exercised here is the pass production runs.
func contractStandingRow(t *testing.T, id string) standingAgent {
	t.Helper()
	for _, a := range standingAgents() {
		if a.ID == id {
			return a
		}
	}
	t.Fatalf("standingAgents() carries no row for %q", id)
	return standingAgent{}
}

// contractStandingPutHit writes a LimitHit straight into its kv row. The
// passes read the record back through their own reader, so seeding it this
// way exercises the read path rather than bypassing it.
func contractStandingPutHit(t *testing.T, st *store.Store, key string, hit LimitHit) {
	t.Helper()
	data, err := json.Marshal(hit)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetKV(key, string(data)); err != nil {
		t.Fatalf("seed %s: %v", key, err)
	}
}

// contractStandingGetHit reads one back. A missing or unreadable row is the
// zero value, which is exactly what "no hit recorded" means everywhere else.
func contractStandingGetHit(t *testing.T, st *store.Store, key string) LimitHit {
	t.Helper()
	raw, err := st.GetKV(key)
	if err != nil || raw == "" {
		return LimitHit{}
	}
	var h LimitHit
	if err := json.Unmarshal([]byte(raw), &h); err != nil {
		t.Fatalf("row %s is not a LimitHit: %v (%q)", key, err, raw)
	}
	return h
}

// contractStandingTranscript fabricates a transcript for sessionID where
// core.TranscriptPathForSession looks — ~/.claude/projects/<any>/<id>.jsonl —
// with one synthetic assistant record carrying text. That record shape is
// what core.LastStall classifies: a `<synthetic>` model is the gate, and the
// text is what the marker lists are matched against.
func contractStandingTranscript(t *testing.T, sessionID, text string) {
	t.Helper()
	dir := filepath.Join(util.Home(), ".claude", "projects", "-tmp-kraken-standing-fixture")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	rec := map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"model": "<synthetic>",
			"content": []any{
				map[string]any{"type": "text", "text": text},
			},
		},
	}
	line, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, sessionID+".jsonl")
	if err := os.WriteFile(path, append(line, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := core.TranscriptPathForSession(sessionID); got != path {
		t.Fatalf("fixture transcript for %q resolves to %q, want %q", sessionID, got, path)
	}
}

// contractStandingUsageLimitText is a real session-limit banner: it carries a
// usage-limit marker AND a parseable reset clause, so one fixture serves both
// the classification tests and the reset re-arm.
const contractStandingUsageLimitText = "You've hit your session limit · resets 4:50am (America/New_York)"

// contractStandingPeer builds one depth-0 claude in a tmux session.
func contractStandingPeer(session, pane, name, sessionID string) *core.Peer {
	return &core.Peer{
		TmuxSession: session, Agent: "claude", Role: "main", Depth: 0,
		Pane: pane, Name: name, SessionID: sessionID, PID: 4242,
		CWD: "/tmp/kraken-standing-fixture",
	}
}

// ---------------------------------------------------------------------------
// slice P — the registry's ids
// ---------------------------------------------------------------------------

// TestContractStandingAgentIDs: "Returns exactly ["iris"], stable order." The order is the contract, not an accident — standingAgents() is
// pinned against this list, so a reorder here reorders the registry.
func TestContractStandingAgentIDs(t *testing.T) {
	got := standingAgentIDs()
	want := []string{"iris"}
	if len(got) != len(want) {
		t.Fatalf("standingAgentIDs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("standingAgentIDs() = %v, want %v", got, want)
		}
	}
	// Stable: two calls answer identically. A slice rebuilt per call is
	// fine; a slice whose order depends on map iteration is not.
	second := standingAgentIDs()
	for i := range got {
		if second[i] != got[i] {
			t.Fatalf("two calls disagree: %v then %v", got, second)
		}
	}
}

// TestContractStandingIsStandingAgentID: "True for exactly the ids above,
// case-sensitive; false for "", a job-looking numeric string, and anything
// else." internal/api routes on this, so a false negative refuses a standing
// agent's own hook and a false positive opens a kv row under an id nothing owns.
func TestContractStandingIsStandingAgentID(t *testing.T) {
	for _, id := range standingAgentIDs() {
		if !IsStandingAgentID(id) {
			t.Errorf("IsStandingAgentID(%q) = false, want true — the registry's own id is refused", id)
		}
	}
	for _, id := range []string{"", "42", "0", "Iris", "IRIS", "iris ", "zeus", "iris,iris"} {
		if IsStandingAgentID(id) {
			t.Errorf("IsStandingAgentID(%q) = true, want false", id)
		}
	}
}

// TestContractStandingHitKeyShape: `"limit_hit:" + agentID`. This string IS
// the storage format — nothing ever deletes the row, so a changed shape
// orphans every record written before the change.
func TestContractStandingHitKeyShape(t *testing.T) {
	for _, id := range standingAgentIDs() {
		if got, want := standingHitKey(id), "limit_hit:"+id; got != want {
			t.Errorf("standingHitKey(%q) = %q, want %q", id, got, want)
		}
	}
}

// TestContractStandingHitKeyNeverCollidesWithAJobKey is the collision fixture
// the contract's Notes mandate: "one uses a job whose numeric id renders to a
// string equal in LENGTH to a standing agent's key to pin non-collision by
// shape, not luck". A collision here would let a standing agent's hit and a
// job's hit read and write one row — and the rescue swaps the machine's login
// off that row.
func TestContractStandingHitKeyNeverCollidesWithAJobKey(t *testing.T) {
	// 1234 renders to four digits, exactly len("iris"). Equal length is what
	// makes this a shape test rather than a coincidence of digits being short.
	if got := len(strconv.FormatInt(1234, 10)); got != len("iris") {
		t.Fatalf("fixture is wrong: 1234 renders to %d chars, want %d", got, len("iris"))
	}
	jobIDs := []int64{0, 1, 42, 1234, 123456, 999999, 9_007_199_254_740_993}
	for _, id := range standingAgentIDs() {
		agentKey := standingHitKey(id)
		for _, jid := range jobIDs {
			if jobKey := limitHitKey(jid); jobKey == agentKey {
				t.Fatalf("job %d and standing agent %q share the key %q", jid, id, agentKey)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// slice P — readLimitHitByKey / recordLimitHitByKey
// ---------------------------------------------------------------------------

// TestContractStandingReadLimitHitByKeyIsErrorless: "no store, unset key, or
// unparseable row all read as the zero LimitHit". A read that could fail
// would have to be handled at every call site of a pass that runs every tick.
func TestContractStandingReadLimitHitByKeyIsErrorless(t *testing.T) {
	// no store wired at all
	UseStore(nil)
	if got := readLimitHitByKey("limit_hit:iris"); got != (LimitHit{}) {
		t.Errorf("no store: got %+v, want the zero LimitHit", got)
	}

	rig := contractStandingRigNew(t)
	if got := readLimitHitByKey("limit_hit:iris"); got != (LimitHit{}) {
		t.Errorf("unset key: got %+v, want the zero LimitHit", got)
	}
	if err := rig.st.SetKV("limit_hit:iris", "{not json"); err != nil {
		t.Fatal(err)
	}
	if got := readLimitHitByKey("limit_hit:iris"); got != (LimitHit{}) {
		t.Errorf("unparseable row: got %+v, want the zero LimitHit", got)
	}
}

// TestContractStandingReadLimitHitDelegatesToTheKeyedPath: "ReadLimitHit(j)
// delegates to it via limitHitKey(j.ID) with behavior byte-identical to
// today." One reader, so a fix to one is a fix to both.
func TestContractStandingReadLimitHitDelegatesToTheKeyedPath(t *testing.T) {
	rig := contractStandingRigNew(t)
	row := mustCreateJob(t, rig.st, store.Job{Slug: "standing-delegate", Repo: "kraken", State: "working"})
	j := &Job{ID: row.ID, Slug: row.Slug}

	now := time.Unix(1_700_000_000, 0)
	if _, err := RecordLimitHit(j, "rate_limit", "the banner", "sess-1", "agent-1", now); err != nil {
		t.Fatal(err)
	}
	viaJob := ReadLimitHit(j)
	viaKey := readLimitHitByKey(limitHitKey(j.ID))
	if viaJob != viaKey {
		t.Fatalf("ReadLimitHit(j) = %+v but readLimitHitByKey(limitHitKey(j.ID)) = %+v — they are not one reader", viaJob, viaKey)
	}
	if viaJob.At != now.Unix() || viaJob.Error != "rate_limit" {
		t.Fatalf("recorded hit reads back as %+v", viaJob)
	}
}

// TestContractStandingRecordLimitHitByKeyRecordsThenFolds is the fold rule
// seen through the keyed writer: a first delivery opens the record, a
// same-error delivery onto an UNRESOLVED hit folds — Deliveries increments
// and At keeps meaning "when this limit first hit".
func TestContractStandingRecordLimitHitByKeyRecordsThenFolds(t *testing.T) {
	rig := contractStandingRigNew(t)
	key := standingHitKey("iris")
	first := time.Unix(1_700_000_000, 0)

	hit, err := recordLimitHitByKey(key, "iris", "", "rate_limit", "the banner", "iris-sess", "", first)
	if err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	if hit.At != first.Unix() || hit.Deliveries != 1 || hit.HandledAt != 0 {
		t.Fatalf("first delivery recorded %+v", hit)
	}
	if hit.Error != "rate_limit" || hit.Details != "the banner" || hit.SessionID != "iris-sess" || hit.AgentID != "" {
		t.Fatalf("first delivery lost a field: %+v", hit)
	}

	second, err := recordLimitHitByKey(key, "iris", "", "rate_limit", "the banner", "iris-sess", "", first.Add(30*time.Second))
	if err != nil {
		t.Fatalf("second delivery: %v", err)
	}
	if second.Deliveries != 2 {
		t.Errorf("Deliveries = %d after a folded delivery, want 2", second.Deliveries)
	}
	if second.At != first.Unix() {
		t.Errorf("At = %d after the fold, want the first hit's %d", second.At, first.Unix())
	}
	if stored := contractStandingGetHit(t, rig.st, key); stored != second {
		t.Errorf("stored row %+v does not match the returned record %+v", stored, second)
	}
}

// TestContractStandingRecordLimitHitByKeyResolvedNeverFolds: "a resolved hit
// never folds". Resolution, not time, is the event boundary — a late banner
// re-read must open a new event rather than resurrect a finished one.
func TestContractStandingRecordLimitHitByKeyResolvedNeverFolds(t *testing.T) {
	rig := contractStandingRigNew(t)
	key := standingHitKey("iris")
	at := time.Unix(1_700_000_000, 0)
	contractStandingPutHit(t, rig.st, key, LimitHit{
		Error: "rate_limit", Details: "old banner", At: at.Unix(), Deliveries: 3,
		HandledAt: at.Add(time.Minute).Unix(), Outcome: "rotated",
		SwappedAt: at.Add(30 * time.Second).Unix(), SwappedTo: "pool-b@example.com",
	})

	later := at.Add(2 * time.Hour)
	got, err := recordLimitHitByKey(key, "iris", "", "rate_limit", "new banner", "iris-sess", "", later)
	if err != nil {
		t.Fatal(err)
	}
	if got.At != later.Unix() {
		t.Errorf("At = %d, want a fresh %d — a resolved hit folded", got.At, later.Unix())
	}
	if got.Deliveries != 1 {
		t.Errorf("Deliveries = %d, want 1 on a fresh record", got.Deliveries)
	}
	if got.HandledAt != 0 || got.Outcome != "" {
		t.Errorf("fresh record carries the resolved hit's resolution: %+v", got)
	}
	// A RESOLVED hit's swap watermark is finished business; only an
	// unresolved one survives supersession (asserted separately below).
	if got.SwappedAt != 0 || got.SwappedTo != "" {
		t.Errorf("fresh record inherited a resolved hit's swap stamp: %+v", got)
	}
}

// TestContractStandingRecordLimitHitByKeyAdoptsAResetClause: "a folded
// delivery whose details carry a parseable reset clause replaces clause-less
// stored details". Every hook-arm details string measured carries no clause
// while the transcript arm's carries the banner verbatim, so keeping the
// first arrival would suppress the reset re-arm for every hook-first hit.
func TestContractStandingRecordLimitHitByKeyAdoptsAResetClause(t *testing.T) {
	rig := contractStandingRigNew(t)
	key := standingHitKey("iris")
	at := time.Unix(1_700_000_000, 0)
	contractStandingPutHit(t, rig.st, key, LimitHit{
		Error: "rate_limit", Details: "429", At: at.Unix(), Deliveries: 1,
	})
	if parseLimitReset("429", at) != 0 {
		t.Fatal("fixture is wrong: the stored details already carry a parseable clause")
	}
	if parseLimitReset(contractStandingUsageLimitText, at) == 0 {
		t.Fatal("fixture is wrong: the incoming details carry no parseable clause")
	}

	got, err := recordLimitHitByKey(key, "iris", "", "rate_limit", contractStandingUsageLimitText, "iris-sess", "", at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if got.Details != contractStandingUsageLimitText {
		t.Errorf("Details = %q, want the clause-carrying banner", got.Details)
	}
	if got.Deliveries != 2 {
		t.Errorf("Deliveries = %d, want 2 — this was a fold, not a fresh record", got.Deliveries)
	}
	if got.At != at.Unix() {
		t.Errorf("At moved to %d — this was a fold", got.At)
	}
}

// TestContractStandingRecordLimitHitByKeySurvivesTheSwapWatermark: "an
// unresolved swap watermark survives supersession". A standing agent whose
// rescue has swapped but not yet respawned is still emitting failures; a
// delivery that zeroed SwappedAt would clear the gate saying "already
// swapped, go and resume" and re-arm the rotate path with Attempts back at
// zero — uncapped machine-wide credential swapping.
func TestContractStandingRecordLimitHitByKeySurvivesTheSwapWatermark(t *testing.T) {
	rig := contractStandingRigNew(t)
	key := standingHitKey("iris")
	at := time.Unix(1_700_000_000, 0)
	contractStandingPutHit(t, rig.st, key, LimitHit{
		Error: "rate_limit", Details: "the banner", At: at.Unix(), Deliveries: 1,
		SwappedAt: at.Add(20 * time.Second).Unix(), SwappedTo: "pool-b@example.com",
	})

	// A DIFFERENT error type supersedes rather than folds.
	got, err := recordLimitHitByKey(key, "iris", "", "overloaded", "something else", "iris-sess", "", at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if got.At == at.Unix() {
		t.Fatal("a different error type folded — it must open a fresh record")
	}
	if got.SwappedAt != at.Add(20*time.Second).Unix() || got.SwappedTo != "pool-b@example.com" {
		t.Errorf("the unresolved swap watermark did not survive supersession: %+v", got)
	}
}

// ---------------------------------------------------------------------------
// slice P — the machine-wide swap watermark
// ---------------------------------------------------------------------------

// TestContractStandingMainSwapRoundTrip pins the row's shape as well as its
// round trip: kv `main_swap` = {"at": <unix seconds>, "to": <email>}. The row
// is read by every rescue pass on the machine, so the two key names and the
// SECONDS unit (not millis — every other time on the hit record is seconds
// and the adoption compares against hit.At) are the contract.
func TestContractStandingMainSwapRoundTrip(t *testing.T) {
	rig := contractStandingRigNew(t)
	now := time.Unix(1_700_000_123, 0)
	if err := recordMainSwap("pool-b@example.com", now); err != nil {
		t.Fatalf("recordMainSwap: %v", err)
	}

	at, to := readMainSwap()
	if at != now.Unix() || to != "pool-b@example.com" {
		t.Fatalf("readMainSwap() = (%d, %q), want (%d, %q)", at, to, now.Unix(), "pool-b@example.com")
	}

	raw, err := rig.st.GetKV("main_swap")
	if err != nil || raw == "" {
		t.Fatalf("kv main_swap is not set: %v (%q)", err, raw)
	}
	var shape map[string]any
	if err := json.Unmarshal([]byte(raw), &shape); err != nil {
		t.Fatalf("main_swap is not JSON: %v (%q)", err, raw)
	}
	gotAt, ok := shape["at"].(float64)
	if !ok {
		t.Fatalf(`main_swap has no numeric "at": %q`, raw)
	}
	if int64(gotAt) != now.Unix() {
		t.Errorf(`main_swap "at" = %d, want unix SECONDS %d`, int64(gotAt), now.Unix())
	}
	if gotTo, _ := shape["to"].(string); gotTo != "pool-b@example.com" {
		t.Errorf(`main_swap "to" = %v, want the swapped-to email`, shape["to"])
	}
	if len(shape) != 2 {
		t.Errorf("main_swap carries %d keys %v, want exactly at and to", len(shape), raw)
	}
}

// TestContractStandingReadMainSwapIsErrorless: "missing/garbage row reads as
// (0, "")". A watermark that could error would be a watermark every pass has
// to branch on before it can decide anything.
func TestContractStandingReadMainSwapIsErrorless(t *testing.T) {
	rig := contractStandingRigNew(t)
	if at, to := readMainSwap(); at != 0 || to != "" {
		t.Errorf("missing row: readMainSwap() = (%d, %q), want (0, \"\")", at, to)
	}
	if err := rig.st.SetKV("main_swap", "{not json"); err != nil {
		t.Fatal(err)
	}
	if at, to := readMainSwap(); at != 0 || to != "" {
		t.Errorf("garbage row: readMainSwap() = (%d, %q), want (0, \"\")", at, to)
	}
}

// contractStandingAdoptable is the hit shape every one of adoptLandedSwap's
// six conditions holds for. Each case below flips exactly one of them.
func contractStandingAdoptable() LimitHit {
	return LimitHit{
		Error:      "rate_limit",
		Details:    "the banner",
		At:         1_700_000_000,
		Deliveries: 2,
		Attempts:   1,
		SessionID:  "iris-sess",
	}
}

// TestContractStandingAdoptLandedSwapAdoptsWhenEveryConditionHolds is the
// conjunction itself: "Adopts iff ALL of: hit.At != 0, hit.HandledAt == 0,
// hit.Error == "rate_limit", hit.SwappedAt == 0, swapAt >= hit.At,
// swapTo != ""." The negatives are the definition of the "iff", not error
// paths — a single missing condition is a second machine-wide swap.
func TestContractStandingAdoptLandedSwapAdoptsWhenEveryConditionHolds(t *testing.T) {
	base := contractStandingAdoptable()
	const to = "pool-b@example.com"
	swapAt := base.At + 5

	got, adopted := adoptLandedSwap(base, swapAt, to)
	if !adopted {
		t.Fatal("adoptLandedSwap declined a hit every condition holds for")
	}
	if got.SwappedAt != swapAt {
		t.Errorf("SwappedAt = %d, want the watermark's %d", got.SwappedAt, swapAt)
	}
	if got.SwappedTo != to {
		t.Errorf("SwappedTo = %q, want %q", got.SwappedTo, to)
	}

	cases := []struct {
		name   string
		hit    LimitHit
		swapAt int64
		to     string
	}{
		{"hit.At == 0", func() LimitHit { h := base; h.At = 0; return h }(), swapAt, to},
		{"already resolved", func() LimitHit { h := base; h.HandledAt = h.At + 1; return h }(), swapAt, to},
		{"not a rate limit", func() LimitHit { h := base; h.Error = "overloaded"; return h }(), swapAt, to},
		{"already swapped", func() LimitHit { h := base; h.SwappedAt = h.At + 1; h.SwappedTo = "x@y"; return h }(), swapAt, to},
		{"watermark predates the hit", base, base.At - 1, to},
		{"no watermark at all", base, 0, ""},
		{"watermark names no account", base, swapAt, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, adopted := adoptLandedSwap(c.hit, c.swapAt, c.to)
			if adopted {
				t.Fatalf("adopted: %+v", got)
			}
			if got != c.hit {
				t.Fatalf("returned %+v, want the input unchanged %+v", got, c.hit)
			}
		})
	}
}

// TestContractStandingAdoptLandedSwapAtEqualIsAdopted: the comparison is
// `swapAt >= hit.At`, so a swap recorded in the same second as the hit counts
// as landing after it. Both stamps are unix SECONDS, so same-second is the
// ordinary case for a herd, not a corner one.
func TestContractStandingAdoptLandedSwapAtEqualIsAdopted(t *testing.T) {
	base := contractStandingAdoptable()
	got, adopted := adoptLandedSwap(base, base.At, "pool-b@example.com")
	if !adopted {
		t.Fatal("swapAt == hit.At was not adopted — the comparison must be >=")
	}
	if got.SwappedAt != base.At {
		t.Errorf("SwappedAt = %d, want %d", got.SwappedAt, base.At)
	}
}

// TestContractStandingAdoptLandedSwapTouchesOnlyTheSwapStamp: "It never
// touches Attempts, HandledAt, or Outcome (the decide chain's landed-swap
// gate ... is what acts on the stamp)." An adoption that also bumped Attempts
// would spend a rotate attempt on a swap this hit never made.
func TestContractStandingAdoptLandedSwapTouchesOnlyTheSwapStamp(t *testing.T) {
	base := contractStandingAdoptable()
	base.Attempts = 2
	base.LastAttempt = base.At + 3
	got, adopted := adoptLandedSwap(base, base.At+10, "pool-b@example.com")
	if !adopted {
		t.Fatal("not adopted")
	}
	want := base
	want.SwappedAt = base.At + 10
	want.SwappedTo = "pool-b@example.com"
	if got != want {
		t.Fatalf("adoption changed more than the swap stamp:\n got %+v\nwant %+v", got, want)
	}
}

// ---------------------------------------------------------------------------
// slice S — the descriptor registry
// ---------------------------------------------------------------------------

// TestContractStandingAgentsMatchStandingAgentIDs is the pin the contract
// asks for by name: "Exactly two rows, ids matching standingAgentIDs() in the
// same order (a test pins the two lists against each other)". The api route
// validates against one list and the daemon iterates the other; a drift means
// a hook whose POST is accepted and whose agent no pass ever visits.
func TestContractStandingAgentsMatchStandingAgentIDs(t *testing.T) {
	rows := standingAgents()
	ids := standingAgentIDs()
	if len(rows) != len(ids) {
		t.Fatalf("standingAgents() has %d rows, standingAgentIDs() has %d", len(rows), len(ids))
	}
	for i := range ids {
		if rows[i].ID != ids[i] {
			t.Fatalf("row %d has id %q, standingAgentIDs()[%d] = %q", i, rows[i].ID, i, ids[i])
		}
	}
}

// TestContractStandingAgentsFieldValues pins the two rows' fixed fields
// against the constants the rest of the package already uses: iris'
// Session/AgentName are "iris". Session is what the pass
// reads liveness off and what the resume kills; AgentName is half the kill
// guard's identity test. A row naming a session nothing runs in is a pass
// that silently never fires.
func TestContractStandingAgentsFieldValues(t *testing.T) {
	rows := standingAgents()
	want := map[string]struct{ session, agentName string }{
		"iris": {IrisSessionName, IrisAgentName},
	}
	for _, a := range rows {
		w, ok := want[a.ID]
		if !ok {
			t.Fatalf("unexpected row id %q", a.ID)
		}
		if a.Session != w.session {
			t.Errorf("%s: Session = %q, want %q", a.ID, a.Session, w.session)
		}
		if a.AgentName != w.agentName {
			t.Errorf("%s: AgentName = %q, want %q", a.ID, a.AgentName, w.agentName)
		}
		if a.Enabled == nil {
			t.Errorf("%s: Enabled is nil — every pass gates on it", a.ID)
		}
		if a.RecordedSession == nil {
			t.Errorf("%s: RecordedSession is nil — classification runs only on the recorded id", a.ID)
		}
		if a.Find == nil {
			t.Errorf("%s: Find is nil", a.ID)
		}
		if a.Respawn == nil {
			t.Errorf("%s: Respawn is nil — the resume arm has nothing to call", a.ID)
		}
	}
	// The constants themselves must equal the ids, or the registry's id and
	// the tmux session it acts on are two different strings.
	if IrisSessionName != "iris" {
		t.Fatalf("session constant drifted: iris=%q", IrisSessionName)
	}
}

// TestContractStandingAgentsEnabledGates: iris' gate is
// "core.LoadSlackConfig() returns nil error". It defaults OFF on a machine
// that has configured nothing, which is what keeps the whole feature inert
// until carl opts in.
func TestContractStandingAgentsEnabledGates(t *testing.T) {
	contractStandingRigNew(t)
	// Neutralise any inherited slack credentials so the file is what decides.
	t.Setenv("SLACK_USER_TOKEN", "")
	t.Setenv("SLACK_D_COOKIE", "")

	iris := contractStandingRow(t, "iris")
	if iris.Enabled() {
		t.Error("iris reads as enabled with no slack credential")
	}

	contractStandingEnableIris(t)
	if !iris.Enabled() {
		t.Error("iris reads as disabled with a configured xoxp- token")
	}
	if _, err := core.LoadSlackConfig(); err != nil {
		t.Errorf("fixture is wrong: LoadSlackConfig still errors: %v", err)
	}
}

// TestContractStandingAgentsRecordedSessionWrapsTheKVReaders: iris'
// RecordedSession wraps readIrisAgent. The recorded id is the ONLY id
// classification and the kill guard may use, so a row reading the wrong kv
// key classifies nothing forever, silently.
func TestContractStandingAgentsRecordedSessionWrapsTheKVReaders(t *testing.T) {
	rig := contractStandingRigNew(t)
	iris := contractStandingRow(t, "iris")

	if sid, ok := iris.RecordedSession(rig.st); ok || sid != "" {
		t.Errorf("iris with no kv row: (%q, %v), want (\"\", false)", sid, ok)
	}

	if err := writeIrisAgent(rig.st, irisAgent{SessionID: "iris-sess-1", SpawnedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if sid, ok := iris.RecordedSession(rig.st); !ok || sid != "iris-sess-1" {
		t.Errorf("iris: (%q, %v), want (iris-sess-1, true)", sid, ok)
	}
}

// TestContractStandingAgentsFindDelegatesToTheIdentityLookups: iris' Find is
// findIris, which already encodes the nameless-startup fallback and the
// session/depth scoping; a row doing its own lookup would answer differently.
func TestContractStandingAgentsFindDelegatesToTheIdentityLookups(t *testing.T) {
	irisPeer := contractStandingPeer("iris", "%2", IrisAgentName, "iris-sess-1")
	other := contractStandingPeer("cell-something-9", "%3", "zeus-something", "zeus-sess")
	all := []*core.Peer{other, irisPeer}

	iris := contractStandingRow(t, "iris")
	if got := iris.Find(all); got != findIris(all) {
		t.Errorf("iris.Find answered %v, findIris answered %v", got, findIris(all))
	}
	if got := iris.Find(all); got != irisPeer {
		t.Errorf("iris.Find found %v, want the peer in the iris session", got)
	}
	if got := iris.Find(nil); got != nil {
		t.Errorf("iris.Find on an empty snapshot = %v, want nil", got)
	}
}

// ---------------------------------------------------------------------------
// slice S — standingStallReason
// ---------------------------------------------------------------------------

// TestContractStandingStallReasonUsageLimit: `Kind "usage-limit" → non-empty
// reason embedding st.Text, isLimit true`, and "Every non-empty reason names
// agentID and NEVER contains the word "zeus"." The zeus rule is not cosmetic:
// the reason is what the recorded hit's Details carries and what the
// notification says, so a copied cell string tells carl a job is stalled when
// it is a standing agent.
func TestContractStandingStallReasonUsageLimit(t *testing.T) {
	st := core.Stall{Kind: "usage-limit", Text: contractStandingUsageLimitText}
	for _, id := range standingAgentIDs() {
		reason, isLimit, retryable := standingStallReason(id, st)
		if reason == "" {
			t.Fatalf("%s: reason is empty for a usage-limit stall", id)
		}
		if !strings.Contains(reason, st.Text) {
			t.Errorf("%s: reason %q does not embed the banner %q", id, reason, st.Text)
		}
		if !strings.Contains(reason, id) {
			t.Errorf("%s: reason %q does not name the agent", id, reason)
		}
		if strings.Contains(strings.ToLower(reason), "zeus") {
			t.Errorf("%s: reason %q says zeus — this is a standing agent, not a cell lead", id, reason)
		}
		if !isLimit {
			t.Errorf("%s: isLimit = false for Kind usage-limit", id)
		}
		if retryable {
			t.Errorf("%s: retryable = true for a usage limit — a nudge cannot fix an exhausted window", id)
		}
	}
}

// TestContractStandingStallReasonAPIError: `Kind "api-error" → non-empty
// reason, isLimit false, retryable == st.Transient`. retryable riding on
// Transient is what separates a 529 wave (nudge it) from a credit or auth
// failure (carl).
func TestContractStandingStallReasonAPIError(t *testing.T) {
	cases := []struct {
		name      string
		stall     core.Stall
		retryable bool
	}{
		{"transient overload", core.Stall{Kind: "api-error", Text: "API Error: 529 overloaded", Transient: true}, true},
		{"non-transient credit", core.Stall{Kind: "api-error", Text: "API Error: credit balance is too low", Transient: false}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reason, isLimit, retryable := standingStallReason("iris", c.stall)
			if reason == "" {
				t.Fatal("reason is empty for an api-error stall")
			}
			if !strings.Contains(reason, c.stall.Text) {
				t.Errorf("reason %q does not embed %q", reason, c.stall.Text)
			}
			if !strings.Contains(reason, "iris") {
				t.Errorf("reason %q does not name the agent", reason)
			}
			if strings.Contains(strings.ToLower(reason), "zeus") {
				t.Errorf("reason %q says zeus", reason)
			}
			if isLimit {
				t.Error("isLimit = true for an api-error stall — the rescue would swap the machine's login over a 529")
			}
			if retryable != c.retryable {
				t.Errorf("retryable = %v, want st.Transient (%v)", retryable, c.retryable)
			}
		})
	}
}

// TestContractStandingStallReasonQuietKinds: `Kind none/interrupted →
// ("", false, false)`. An interrupted turn is carl's deliberate esc, and a
// healthy transcript is the overwhelmingly common tick.
func TestContractStandingStallReasonQuietKinds(t *testing.T) {
	for _, st := range []core.Stall{{}, {Kind: "interrupted"}, {Kind: "interrupted", Text: "[Request interrupted by user"}} {
		reason, isLimit, retryable := standingStallReason("iris", st)
		if reason != "" || isLimit || retryable {
			t.Errorf("Kind %q: got (%q, %v, %v), want (\"\", false, false)", st.Kind, reason, isLimit, retryable)
		}
	}
}

// ---------------------------------------------------------------------------
// slice S — standingHitTriggerKey and its re-arming dedup
// ---------------------------------------------------------------------------

// TestContractStandingHitTriggerKeyShape: `"limithit:" + agentID + "|" +
// reason`. This is also the contract's MANDATED token-collision fixture —
// the reason string here contains the literal agent id, which is exactly what
// a naive concatenation gets wrong.
func TestContractStandingHitTriggerKeyShape(t *testing.T) {
	// A reason that repeats the id, the way a real one does ("iris hit a
	// usage limit: …"), and one that also carries the separator.
	reasons := []string{
		"iris hit a usage limit: You've hit your session limit",
		"iris|iris",
		"iris",
	}
	for _, id := range standingAgentIDs() {
		for _, reason := range reasons {
			if got, want := standingHitTriggerKey(id, reason), "limithit:"+id+"|"+reason; got != want {
				t.Errorf("standingHitTriggerKey(%q, %q) = %q, want %q", id, reason, got, want)
			}
		}
	}
	// A genuinely different banner must be able to record while an earlier
	// one is still suppressed.
	if standingHitTriggerKey("iris", "weekly") == standingHitTriggerKey("iris", "session") {
		t.Fatal("two reasons share a key")
	}
}

// TestContractStandingHitTriggerKeyPairsWithLimitHitTriggerDue: the contract
// says the key is "paired with limitHitTriggerDue unchanged for the re-arming
// dedup". The re-arm is the load-bearing half — a key that only ever gets SET
// reads as one hit per distinct banner for the daemon's whole lifetime, and a
// weekly banner is byte-identical across two exhaustions days apart.
func TestContractStandingHitTriggerKeyPairsWithLimitHitTriggerDue(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	stamps := map[string]int64{}
	key := standingHitTriggerKey("iris", "iris hit a usage limit: weekly")

	if !limitHitTriggerDue(stamps[key], now) {
		t.Fatal("never recorded: not due, want due")
	}
	stamps[key] = now.Unix()
	if limitHitTriggerDue(stamps[key], now) {
		t.Fatal("just recorded: due, want suppressed")
	}
	if !limitHitTriggerDue(stamps[key], now.Add(limitRescueRetryInterval+time.Second)) {
		t.Fatal("past the backoff: not due — the suppression never re-arms")
	}
}

// ---------------------------------------------------------------------------
// slice S — standingHealth
// ---------------------------------------------------------------------------

// TestContractStandingHealthClassifiesTheRecordedSession is the happy path:
// a found, named peer plus a recorded session id whose transcript ends in a
// usage-limit banner classifies as a limit.
func TestContractStandingHealthClassifiesTheRecordedSession(t *testing.T) {
	contractStandingRigNew(t)
	const sid = "iris-sess-health"
	contractStandingTranscript(t, sid, contractStandingUsageLimitText)
	a := contractStandingRow(t, "iris")
	all := []*core.Peer{contractStandingPeer("iris", "%1", IrisAgentName, sid)}

	reason, isLimit, retryable := standingHealth(a, all, sid)
	wantReason, wantLimit, wantRetry := standingStallReason(a.ID, core.LastStall(core.TranscriptPathForSession(sid)))
	if reason != wantReason || isLimit != wantLimit || retryable != wantRetry {
		t.Fatalf("standingHealth = (%q, %v, %v), want standingStallReason's (%q, %v, %v)",
			reason, isLimit, retryable, wantReason, wantLimit, wantRetry)
	}
	if reason == "" || !isLimit {
		t.Fatalf("a usage-limit transcript classified as (%q, %v) — the rescue would never fire", reason, isLimit)
	}
}

// TestContractStandingHealthClassifiesATransientAPIError: the same path with
// the other kind. Kept beside the limit case because the two route to
// completely different arms of the health pass — record a hit, or nudge.
func TestContractStandingHealthClassifiesATransientAPIError(t *testing.T) {
	contractStandingRigNew(t)
	const sid = "iris-sess-529"
	contractStandingTranscript(t, sid, "API Error: 529 overloaded_error")
	a := contractStandingRow(t, "iris")
	all := []*core.Peer{contractStandingPeer("iris", "%1", IrisAgentName, sid)}

	reason, isLimit, retryable := standingHealth(a, all, sid)
	if reason == "" {
		t.Fatal("a synthetic API error classified as healthy")
	}
	if isLimit {
		t.Error("a 529 classified as a usage limit — the rescue would swap the machine's login over an overload")
	}
	if !retryable {
		t.Error("a transient API error classified as not retryable — nothing would ever nudge it")
	}
}

// TestContractStandingHealthIsEmptyWithoutAPeer: "a nil peer ... yields
// ("", false, false)". An agent that is not running has no stall to classify,
// and reading a transcript for one would classify its LAST life forever.
func TestContractStandingHealthIsEmptyWithoutAPeer(t *testing.T) {
	contractStandingRigNew(t)
	const sid = "iris-sess-nopeer"
	contractStandingTranscript(t, sid, contractStandingUsageLimitText)
	a := contractStandingRow(t, "iris")

	// nothing in the snapshot at all, and a peer in someone else's session
	for _, all := range [][]*core.Peer{
		nil,
		{contractStandingPeer("cell-other-9", "%1", "zeus-other", sid)},
	} {
		reason, isLimit, retryable := standingHealth(a, all, sid)
		if reason != "" || isLimit || retryable {
			t.Errorf("no peer in the agent's session: got (%q, %v, %v), want the empty result", reason, isLimit, retryable)
		}
	}
}

// TestContractStandingHealthIsEmptyForANamelessPeer: "a nameless peer (still
// starting) ... yields ("", false, false)". Peer.Name only appears once
// claude writes its session file, so a nameless peer is an agent inside its
// own startup window — the transcript it would be classified against is the
// PREVIOUS life's.
func TestContractStandingHealthIsEmptyForANamelessPeer(t *testing.T) {
	contractStandingRigNew(t)
	const sid = "iris-sess-nameless"
	contractStandingTranscript(t, sid, contractStandingUsageLimitText)
	a := contractStandingRow(t, "iris")
	all := []*core.Peer{contractStandingPeer("iris", "%1", "", sid)}

	reason, isLimit, retryable := standingHealth(a, all, sid)
	if reason != "" || isLimit || retryable {
		t.Fatalf("nameless peer: got (%q, %v, %v), want the empty result", reason, isLimit, retryable)
	}
}

// TestContractStandingHealthIsEmptyWithoutARecordedSession: `recorded == ""`
// yields the empty result — "classification runs ONLY on the recorded session
// id, never a discovered or guessed one". The peer here carries a perfectly
// good SessionID with a limit banner behind it, and it must still classify
// nothing: a guessed id can belong to another claude entirely.
func TestContractStandingHealthIsEmptyWithoutARecordedSession(t *testing.T) {
	contractStandingRigNew(t)
	const discovered = "iris-sess-discovered"
	contractStandingTranscript(t, discovered, contractStandingUsageLimitText)
	a := contractStandingRow(t, "iris")
	all := []*core.Peer{contractStandingPeer("iris", "%1", IrisAgentName, discovered)}

	reason, isLimit, retryable := standingHealth(a, all, "")
	if reason != "" || isLimit || retryable {
		t.Fatalf("no recorded session: got (%q, %v, %v), want the empty result", reason, isLimit, retryable)
	}
}

// TestContractStandingHealthUsesTheRecordedIDNotThePeersOwn is the same rule
// stated positively, and it is the one a wrong implementation passes every
// other test with: both ids resolve to real transcripts, and they classify
// DIFFERENTLY, so the answer says which one was read.
func TestContractStandingHealthUsesTheRecordedIDNotThePeersOwn(t *testing.T) {
	contractStandingRigNew(t)
	const recorded = "iris-sess-recorded"
	const discovered = "iris-sess-other"
	contractStandingTranscript(t, recorded, "API Error: 529 overloaded_error")
	contractStandingTranscript(t, discovered, contractStandingUsageLimitText)
	a := contractStandingRow(t, "iris")
	all := []*core.Peer{contractStandingPeer("iris", "%1", IrisAgentName, discovered)}

	reason, isLimit, retryable := standingHealth(a, all, recorded)
	if isLimit {
		t.Fatalf("classified the DISCOVERED session's usage limit (%q) — only the recorded id may be read", reason)
	}
	if !retryable || reason == "" {
		t.Fatalf("got (%q, %v, %v), want the recorded session's transient api-error", reason, isLimit, retryable)
	}
}

// TestContractStandingHealthIsEmptyWhenTheTranscriptCannotBeResolved: "An
// unresolvable transcript path yields the empty result." A recorded id whose
// file has been removed is an agent kraken cannot see into, not an agent in
// trouble.
func TestContractStandingHealthIsEmptyWhenTheTranscriptCannotBeResolved(t *testing.T) {
	contractStandingRigNew(t)
	a := contractStandingRow(t, "iris")
	all := []*core.Peer{contractStandingPeer("iris", "%1", IrisAgentName, "iris-sess-gone")}
	if got := core.TranscriptPathForSession("iris-sess-gone"); got != "" {
		t.Fatalf("fixture is wrong: a transcript exists at %q", got)
	}

	reason, isLimit, retryable := standingHealth(a, all, "iris-sess-gone")
	if reason != "" || isLimit || retryable {
		t.Fatalf("unresolvable transcript: got (%q, %v, %v), want the empty result", reason, isLimit, retryable)
	}
}

// ---------------------------------------------------------------------------
// slice S — standingKillTarget
// ---------------------------------------------------------------------------

// TestContractStandingKillTargetMatchesByName is the first identity disjunct:
// "a depth-0 claude in a.Session matches BY IDENTITY: Name == a.AgentName".
// The success reason is empty — the string is the REFUSAL's explanation, and
// a resolution has nothing to explain.
func TestContractStandingKillTargetMatchesByName(t *testing.T) {
	a := contractStandingRow(t, "iris")
	peer := contractStandingPeer("iris", "%1", IrisAgentName, "iris-sess-1")
	all := []*core.Peer{
		contractStandingPeer("cell-other-9", "%9", "zeus-other", "zeus-sess"),
		peer,
	}

	got, reason := standingKillTarget(a, all, "iris-sess-1")
	if got != peer {
		t.Fatalf("standingKillTarget = %v, want the named iris peer (reason %q)", got, reason)
	}
	if reason != "" {
		t.Errorf("reason = %q on a resolution, want empty", reason)
	}
}

// TestContractStandingKillTargetMatchesByRecordedSessionID is the second
// disjunct: "(recorded != "" and SessionID == recorded)". The fixture is a
// peer whose NAME does not match, which is the only shape that exercises the
// disjunct without also being the "nameless peer" the contract names as a
// refusal — see the report for that ambiguity.
func TestContractStandingKillTargetMatchesByRecordedSessionID(t *testing.T) {
	a := contractStandingRow(t, "iris")
	peer := contractStandingPeer("iris", "%1", "some-other-name", "iris-sess-1")
	all := []*core.Peer{peer}

	got, reason := standingKillTarget(a, all, "iris-sess-1")
	if got != peer {
		t.Fatalf("standingKillTarget = %v, want the peer whose SessionID is the recorded one (reason %q)", got, reason)
	}
	if reason != "" {
		t.Errorf("reason = %q on a resolution, want empty", reason)
	}
}

// TestContractStandingKillTargetRefusesWhenNeitherDisjunctHolds: "A nameless
// peer, a foreign name, or no peer returns (nil, <non-empty reason>) — a
// refusal is a deferral, never a resolution." The blast radius here is a
// SIGKILL: a wrong answer destroys whatever process is actually in that pane.
func TestContractStandingKillTargetRefusesWhenNeitherDisjunctHolds(t *testing.T) {
	a := contractStandingRow(t, "iris")
	const recorded = "iris-sess-1"
	cases := []struct {
		name string
		all  []*core.Peer
	}{
		{"no peer at all", nil},
		{"nothing in the agent's session", []*core.Peer{contractStandingPeer("cell-other-9", "%9", IrisAgentName, recorded)}},
		{"nameless with no session id", []*core.Peer{contractStandingPeer("iris", "%1", "", "")}},
		{"foreign name and a different session id", []*core.Peer{contractStandingPeer("iris", "%1", "someone-else", "someone-else-sess")}},
		{"a nested subagent, not the agent itself", []*core.Peer{func() *core.Peer {
			p := contractStandingPeer("iris", "%1", IrisAgentName, recorded)
			p.Depth = 1
			return p
		}()}},
		{"a codex pane, not a claude", []*core.Peer{func() *core.Peer {
			p := contractStandingPeer("iris", "%1", IrisAgentName, recorded)
			p.Agent = "codex"
			return p
		}()}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, reason := standingKillTarget(a, c.all, recorded)
			if got != nil {
				t.Fatalf("standingKillTarget returned %+v — it would be SIGKILLed", got)
			}
			if reason == "" {
				t.Fatal("a refusal carries no reason — nothing anywhere would say why the rescue deferred")
			}
		})
	}
}

// TestContractStandingKillTargetIgnoresTheRecordedIDWhenItIsEmpty: the second
// disjunct is guarded on `recorded != ""`, so an agent with no recorded
// session must not match a peer whose SessionID is also empty. Without the
// guard, "" == "" makes every nameless claude in that session a kill target.
func TestContractStandingKillTargetIgnoresTheRecordedIDWhenItIsEmpty(t *testing.T) {
	a := contractStandingRow(t, "iris")
	all := []*core.Peer{contractStandingPeer("iris", "%1", "someone-else", "")}
	got, reason := standingKillTarget(a, all, "")
	if got != nil {
		t.Fatalf("an empty recorded id matched an id-less peer %+v — every nameless claude in the session becomes a target", got)
	}
	if reason == "" {
		t.Fatal("a refusal carries no reason")
	}
}

// ---------------------------------------------------------------------------
// slice S — the standingRespawn seam
// ---------------------------------------------------------------------------

// TestContractStandingRespawnSeamHasAProductionValue. The seam is declared so
// tests can observe the resume arm without a tmux server; a nil one is a
// production path that panics the first time a standing agent hits a limit,
// inside the daemon tick.
func TestContractStandingRespawnSeamHasAProductionValue(t *testing.T) {
	if standingRespawn == nil {
		t.Fatal("standingRespawn is nil — the seam has no production value")
	}
}

// ---------------------------------------------------------------------------
// slice P — the cell pass adopts the watermark
// ---------------------------------------------------------------------------

// contractStandingCell builds a job with a live cell: the store row, the
// staged cell dir the spawn machinery writes into, zeus' peer, and the live
// map the pass reads liveness off.
func contractStandingCell(t *testing.T, rig *contractStandingRig, slug string) (*Job, []*core.Peer, map[string]bool) {
	t.Helper()
	row := mustCreateJob(t, rig.st, store.Job{Slug: slug, Repo: "kraken", State: "working"})
	j := &Job{ID: row.ID, Slug: row.Slug, Repo: row.Repo, Model: "claude-fable-5"}
	if err := os.MkdirAll(cellDir(j), 0o755); err != nil {
		t.Fatal(err)
	}
	sess := CellSession(j)
	zeus := &core.Peer{
		TmuxSession: sess, Agent: "claude", Role: "main", Depth: 0,
		Pane: "%1", Name: ZeusName(j), SessionID: "zeus-sess-" + slug,
		PID: 4242, CWD: "/tmp/kraken-cell-worktree",
	}
	return j, []*core.Peer{zeus}, map[string]bool{sess: true}
}

// TestContractStandingCellPassAdoptsALandedSwap: "After the ResetAt
// derivation and before limitRescueDecide: read the watermark, run
// adoptLandedSwap, and when it changed the record persist it ... before
// deciding, same tick." The observable consequence is that the pass performs
// NO swap of its own — the whole point of the watermark is that a herd of
// hits recorded around one exhausted window rotates once, not N times.
func TestContractStandingCellPassAdoptsALandedSwap(t *testing.T) {
	rig := contractStandingRigNew(t)
	contractStandingSnapshot(t)
	rig.Target = "pool-b" // a target IS available: the pass must still not use it
	j, all, live := contractStandingCell(t, rig, "adopt-cell")

	now := time.Now()
	hitAt := now.Add(-2 * time.Minute)
	contractStandingPutHit(t, rig.st, limitHitKey(j.ID), LimitHit{
		Error: "rate_limit", Details: "429", At: hitAt.Unix(), Deliveries: 1,
	})
	// A swap that landed AFTER the hit, aged well past the settle window.
	if err := recordMainSwap("pool-b@example.com", now.Add(-90*time.Second)); err != nil {
		t.Fatal(err)
	}

	newDaemon().limitRescue(j, all, live)

	if len(rig.Swaps) != 0 {
		t.Fatalf("the pass swapped %v despite an adoptable watermark — that is a second machine-wide credential change", rig.Swaps)
	}
	stored := contractStandingGetHit(t, rig.st, limitHitKey(j.ID))
	if stored.SwappedAt != now.Add(-90*time.Second).Unix() || stored.SwappedTo != "pool-b@example.com" {
		t.Fatalf("the watermark was not adopted onto the record: %+v", stored)
	}
	if len(rig.PaneRespawns) != 1 {
		t.Fatalf("zeus' pane was respawned %d times, want once — an adopted swap still resumes", len(rig.PaneRespawns))
	}
	if stored.HandledAt == 0 || stored.Outcome != "rotated" {
		t.Fatalf("the resume did not resolve the hit: %+v", stored)
	}
}

// TestContractStandingCellPassPersistsAdoptionBeforeDeciding is the "same
// tick" half stated on its own. The watermark here is YOUNGER than the settle
// window, so the decide chain answers skip and the pass does nothing else —
// and the record must ALREADY carry the swap stamp. Without the persist, the
// next tick reads a hit that was never swapped and rotates.
func TestContractStandingCellPassPersistsAdoptionBeforeDeciding(t *testing.T) {
	rig := contractStandingRigNew(t)
	contractStandingSnapshot(t)
	rig.Target = "pool-b"
	j, all, live := contractStandingCell(t, rig, "adopt-persist")

	now := time.Now()
	contractStandingPutHit(t, rig.st, limitHitKey(j.ID), LimitHit{
		Error: "rate_limit", Details: "429", At: now.Add(-2 * time.Minute).Unix(), Deliveries: 1,
	})
	swapAt := now.Add(-1 * time.Second) // inside limitRescueSettle
	if err := recordMainSwap("pool-b@example.com", swapAt); err != nil {
		t.Fatal(err)
	}

	newDaemon().limitRescue(j, all, live)

	if len(rig.Swaps) != 0 {
		t.Fatalf("the pass swapped %v", rig.Swaps)
	}
	if len(rig.PaneRespawns) != 0 {
		t.Fatalf("the pass resumed inside the settle window: %v", rig.PaneRespawns)
	}
	stored := contractStandingGetHit(t, rig.st, limitHitKey(j.ID))
	if stored.SwappedAt != swapAt.Unix() || stored.SwappedTo != "pool-b@example.com" {
		t.Fatalf("the adoption was not persisted before the skip: %+v", stored)
	}
}

// Retired with Q2 (job 335): TestContractStandingCellPassIsUnchangedWithNoWatermark
// pinned the CELL pass's no-watermark rotate as a machine-wide swap through
// swapMain. The cell rotate is a per-job REBIND now and never reaches that
// seam; its no-watermark behavior (one attempt, stamp from the rotate itself,
// HandledAt zero until the settle-window resume) is pinned by the limitrescue
// contract suite's rotate/two-phase tests instead. The adoption tests around
// this one keep their subject: adoption itself is unchanged for the owners
// that still adopt (standing agents, unbound cells).

// ---------------------------------------------------------------------------
// slice P — rescueSwap
// ---------------------------------------------------------------------------

// TestContractStandingRescueSwapSwapsAndStampsTheWatermark: "same outcomes
// ... plus the recordMainSwap stamp on the swapped path". The stamp is what
// every OTHER unresolved hit on the machine reads to avoid rotating again, so
// a swap that does not stamp is a swap the herd cannot see.
func TestContractStandingRescueSwapSwapsAndStampsTheWatermark(t *testing.T) {
	rig := contractStandingRigNew(t)
	contractStandingSnapshot(t)
	rig.Target = "pool-b"
	now := time.Unix(1_700_000_000, 0)

	to, outcome, err := rescueSwap("iris", now)
	if err != nil {
		t.Fatalf("rescueSwap: %v", err)
	}
	if outcome != "swapped" {
		t.Fatalf("outcome = %q, want swapped", outcome)
	}
	if to != rig.SwapTo {
		t.Errorf("toEmail = %q, want the swap's own answer %q", to, rig.SwapTo)
	}
	if len(rig.Swaps) != 1 || rig.Swaps[0] != "pool-b" {
		t.Errorf("swapMain calls = %v, want exactly [pool-b]", rig.Swaps)
	}
	at, stampedTo := readMainSwap()
	if at == 0 || stampedTo != rig.SwapTo {
		t.Fatalf("readMainSwap() = (%d, %q) after a landed swap, want the stamp", at, stampedTo)
	}
}

// TestContractStandingRescueSwapNoTargetOutcome: the outcome vocabulary is
// shared with the cell path verbatim, and "no-target" is the one the park
// arm counts. Nothing is swapped and nothing is stamped.
func TestContractStandingRescueSwapNoTargetOutcome(t *testing.T) {
	rig := contractStandingRigNew(t)
	contractStandingSnapshot(t)
	rig.Target = "" // the selector finds nobody eligible

	_, outcome, err := rescueSwap("iris", time.Unix(1_700_000_000, 0))
	if err != nil {
		t.Fatalf("rescueSwap: %v", err)
	}
	if outcome != "no-target" {
		t.Fatalf("outcome = %q, want no-target", outcome)
	}
	if len(rig.Swaps) != 0 {
		t.Errorf("swapMain was called with no target available: %v", rig.Swaps)
	}
	if at, _ := readMainSwap(); at != 0 {
		t.Error("a swap that never happened stamped the watermark")
	}
}

// Retired with Q2 (job 335): TestContractStandingRescueSwapDelegatesForACell
// pinned rescueLimitedCell as a delegate of rescueSwap — a cell's rescue
// performing the same machine-wide swap as a standing agent's. Cells REBIND
// their own job row now (rescueLimitedCell in limitrescue.go, pinned by the
// account blind suites); rescueSwap's one caller is the standing pass, and
// TestRetirementNoAutomatedMainSwap holds the boundary.

// TestContractStandingRescueSwapTakesTheLockBeforeSelecting: "same lock order
// (AcquireRotationLock BEFORE selectRescueTarget)". Observable as contention:
// with the cross-process lock already held, the selector must never be
// consulted and the outcome is lock-busy, which the caller counts as nothing
// at all.
func TestContractStandingRescueSwapTakesTheLockBeforeSelecting(t *testing.T) {
	rig := contractStandingRigNew(t)
	contractStandingSnapshot(t)
	rig.Target = "pool-b"

	selects := 0
	selectTarget = func([]core.AccountUsage, string, int64) (string, string) {
		selects++
		return "pool-b", ""
	}
	release, ok, err := AcquireRotationLock()
	if err != nil || !ok {
		t.Fatalf("could not take the rotation lock in the fixture: ok=%v err=%v", ok, err)
	}
	defer release()

	to, outcome, err := rescueSwap("iris", time.Unix(1_700_000_000, 0))
	if err != nil {
		t.Fatalf("a contended lock is not an error: %v", err)
	}
	if outcome != "lock-busy" {
		t.Fatalf("outcome = %q, want lock-busy", outcome)
	}
	if to != "" {
		t.Errorf("toEmail = %q on a contended lock, want empty", to)
	}
	if selects != 0 {
		t.Errorf("the selector ran %d times while the lock was held — selection must happen UNDER the lock", selects)
	}
	if len(rig.Swaps) != 0 {
		t.Errorf("swapMain ran while the lock was held: %v", rig.Swaps)
	}
}

// ---------------------------------------------------------------------------
// slice P — saveLimitRescueStateByKey
// ---------------------------------------------------------------------------

// TestContractStandingSaveLimitRescueStateByKeyMergesUnderAFreshRead: the
// keyed writer is "the merge-under-fresh-read write (mergeLimitRescueState
// unchanged) keyed generically". The rescue owns six fields and a delivery
// owns the rest, so a write must carry the delivery half forward from the
// row as it stands NOW.
func TestContractStandingSaveLimitRescueStateByKeyMergesUnderAFreshRead(t *testing.T) {
	rig := contractStandingRigNew(t)
	key := standingHitKey("iris")
	at := time.Unix(1_700_000_000, 0)

	// what the pass read a moment ago
	read := LimitHit{Error: "rate_limit", Details: "429", At: at.Unix(), Deliveries: 1, SessionID: "iris-sess"}
	// what a delivery landed in the meantime
	contractStandingPutHit(t, rig.st, key, LimitHit{
		Error: "rate_limit", Details: contractStandingUsageLimitText, At: at.Unix(),
		Deliveries: 4, SessionID: "iris-sess",
	})

	// the pass's own half, computed off its stale read
	mine := read
	mine.Attempts = 1
	mine.LastAttempt = at.Add(time.Minute).Unix()
	mine.SwappedAt = at.Add(time.Minute).Unix()
	mine.SwappedTo = "pool-b@example.com"
	if err := saveLimitRescueStateByKey(key, "iris", mine); err != nil {
		t.Fatalf("saveLimitRescueStateByKey: %v", err)
	}

	stored := contractStandingGetHit(t, rig.st, key)
	if stored.Deliveries != 4 {
		t.Errorf("Deliveries = %d, want the stored row's 4 — the delivery half was clobbered", stored.Deliveries)
	}
	if stored.Details != contractStandingUsageLimitText {
		t.Errorf("Details = %q, want the stored row's clause-carrying banner", stored.Details)
	}
	if stored.SwappedAt != mine.SwappedAt || stored.SwappedTo != mine.SwappedTo {
		t.Errorf("the rescue's own swap stamp did not land: %+v", stored)
	}
	if stored.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1", stored.Attempts)
	}
}

// TestContractStandingSaveLimitRescueStateDelegates: "saveLimitRescueState(j,
// hit) delegates via limitHitKey(j.ID)". One writer, so the cell arm's
// merge-under-fresh-read rule is literally the standing arm's.
func TestContractStandingSaveLimitRescueStateDelegates(t *testing.T) {
	rig := contractStandingRigNew(t)
	row := mustCreateJob(t, rig.st, store.Job{Slug: "save-delegate", Repo: "kraken", State: "working"})
	j := &Job{ID: row.ID, Slug: row.Slug}
	at := time.Unix(1_700_000_000, 0)
	contractStandingPutHit(t, rig.st, limitHitKey(j.ID), LimitHit{
		Error: "rate_limit", At: at.Unix(), Deliveries: 1,
	})

	hit := ReadLimitHit(j)
	hit.Attempts = 2
	hit.LastAttempt = at.Add(time.Minute).Unix()
	if err := saveLimitRescueState(j, hit); err != nil {
		t.Fatal(err)
	}
	if got := contractStandingGetHit(t, rig.st, limitHitKey(j.ID)); got.Attempts != 2 {
		t.Fatalf("saveLimitRescueState wrote to the wrong key or dropped the counters: %+v", got)
	}
}

// ---------------------------------------------------------------------------
// slice S — standingHealthPass
// ---------------------------------------------------------------------------

// contractStandingLiveIris wires the world a standing health/rescue pass
// needs for iris: the credential file, the recorded session id, a transcript
// for it, a named peer in the iris session, and the live map.
func contractStandingLiveIris(t *testing.T, rig *contractStandingRig, sid, text string) (standingAgent, []*core.Peer, map[string]bool) {
	t.Helper()
	contractStandingEnableIris(t)
	if err := writeIrisAgent(rig.st, irisAgent{SessionID: sid, SpawnedAt: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if text != "" {
		contractStandingTranscript(t, sid, text)
	}
	all := []*core.Peer{contractStandingPeer("iris", "%1", IrisAgentName, sid)}
	return contractStandingRow(t, "iris"), all, map[string]bool{"iris": true}
}

// TestContractStandingHealthPassRecordsAUsageLimitHit is the transcript arm's
// happy path: "isLimit → record via recordLimitHitByKey(standingHitKey(a.ID),
// a.ID, "rate_limit", reason, recorded, "", now)". Recorded as "rate_limit"
// so the rescue's own error gate accepts it — the hook arm and this one must
// describe one event in one vocabulary or their deliveries cannot fold.
func TestContractStandingHealthPassRecordsAUsageLimitHit(t *testing.T) {
	rig := contractStandingRigNew(t)
	const sid = "iris-sess-limit"
	a, all, live := contractStandingLiveIris(t, rig, sid, contractStandingUsageLimitText)
	now := time.Unix(1_700_000_000, 0)
	wantReason, isLimit, _ := standingStallReason(a.ID, core.LastStall(core.TranscriptPathForSession(sid)))
	if !isLimit {
		t.Fatal("fixture is wrong: the transcript does not classify as a usage limit")
	}

	d := newDaemon()
	d.standingHealthPass(a, rig.st, all, live, now)

	stored := contractStandingGetHit(t, rig.st, standingHitKey("iris"))
	if stored.At != now.Unix() {
		t.Fatalf("nothing recorded under %s: %+v", standingHitKey("iris"), stored)
	}
	if stored.Error != "rate_limit" {
		t.Errorf("Error = %q, want rate_limit", stored.Error)
	}
	if stored.Details != wantReason {
		t.Errorf("Details = %q, want the classified reason %q", stored.Details, wantReason)
	}
	if stored.SessionID != sid {
		t.Errorf("SessionID = %q, want the RECORDED id %q", stored.SessionID, sid)
	}
	if stored.AgentID != "" {
		t.Errorf("AgentID = %q, want empty — the transcript arm has no hook payload", stored.AgentID)
	}
	if stored.HandledAt != 0 {
		t.Errorf("the record is already resolved: %+v", stored)
	}
	if stored.Deliveries != 1 {
		t.Errorf("Deliveries = %d, want 1", stored.Deliveries)
	}
	// It must land under the AGENT's key, never a job's.
	if got := contractStandingGetHit(t, rig.st, limitHitKey(0)); got.At != 0 {
		t.Errorf("a standing hit landed on a job key too: %+v", got)
	}
}

// TestContractStandingHealthPassNotifiesOncePerStreak: the dedup key is
// `"standing-health:" + a.ID + "|" + <reason prefix before " idle ">`. A
// standing stall keeps the same reason at the tail of the transcript
// indefinitely, so this fires on the STREAK rather than on the tick.
func TestContractStandingHealthPassNotifiesOncePerStreak(t *testing.T) {
	rig := contractStandingRigNew(t)
	const sid = "iris-sess-streak"
	a, all, live := contractStandingLiveIris(t, rig, sid, contractStandingUsageLimitText)
	now := time.Unix(1_700_000_000, 0)
	reason, _, _ := standingStallReason(a.ID, core.LastStall(core.TranscriptPathForSession(sid)))
	key := "standing-health:" + a.ID + "|" + strings.SplitN(reason, " idle ", 2)[0]

	d := newDaemon()
	d.standingHealthPass(a, rig.st, all, live, now)
	if d.Notified[key] == 0 {
		t.Fatalf("no notification recorded under %q; keys were %v", key, contractStandingKeys(d))
	}
	stamp := d.Notified[key]

	d.standingHealthPass(a, rig.st, all, live, now.Add(time.Minute))
	if d.Notified[key] != stamp {
		t.Errorf("the streak re-notified: stamp moved %d → %d", stamp, d.Notified[key])
	}
}

// contractStandingKeys lists a daemon's dedup keys, for failure messages.
func contractStandingKeys(d *daemonState) []string {
	out := make([]string, 0, len(d.Notified))
	for k := range d.Notified {
		out = append(out, k)
	}
	return out
}

// TestContractStandingHealthPassRecordsNothingForATransientAPIError: only
// isLimit records. A 529 wave is fixed by a nudge; recording it as a
// rate_limit would send the rescue to swap the machine's login over an
// overload that was about to clear itself.
func TestContractStandingHealthPassRecordsNothingForATransientAPIError(t *testing.T) {
	rig := contractStandingRigNew(t)
	const sid = "iris-sess-529pass"
	a, all, live := contractStandingLiveIris(t, rig, sid, "API Error: 529 overloaded_error")

	newDaemon().standingHealthPass(a, rig.st, all, live, time.Unix(1_700_000_000, 0))

	if got := contractStandingGetHit(t, rig.st, standingHitKey("iris")); got.At != 0 {
		t.Fatalf("a transient API error recorded a limit hit: %+v", got)
	}
	if len(rig.Swaps) != 0 {
		t.Fatalf("the health pass swapped the machine's login: %v", rig.Swaps)
	}
}

// TestContractStandingHealthPassDedupRearms: the record is gated by
// standingHitTriggerKey + limitHitTriggerDue. Inside the backoff a repeat
// records nothing; past it, it records again — because a weekly banner is
// byte-identical across two exhaustions days apart, and a key that only ever
// SETS would decline the second one forever, silently.
func TestContractStandingHealthPassDedupRearms(t *testing.T) {
	rig := contractStandingRigNew(t)
	const sid = "iris-sess-dedup"
	a, all, live := contractStandingLiveIris(t, rig, sid, contractStandingUsageLimitText)
	now := time.Unix(1_700_000_000, 0)
	reason, _, _ := standingStallReason(a.ID, core.LastStall(core.TranscriptPathForSession(sid)))
	tkey := standingHitTriggerKey(a.ID, reason)

	d := newDaemon()
	d.standingHealthPass(a, rig.st, all, live, now)
	if d.Notified[tkey] == 0 {
		t.Fatalf("no trigger stamp under %q; keys were %v", tkey, contractStandingKeys(d))
	}
	if got := contractStandingGetHit(t, rig.st, standingHitKey("iris")); got.Deliveries != 1 {
		t.Fatalf("first pass recorded %+v", got)
	}

	// inside the backoff: suppressed
	d.standingHealthPass(a, rig.st, all, live, now.Add(time.Minute))
	if got := contractStandingGetHit(t, rig.st, standingHitKey("iris")); got.Deliveries != 1 {
		t.Fatalf("a repeat inside the backoff recorded again: %+v", got)
	}

	// past the backoff: re-armed, and the delivery FOLDS onto the still
	// unresolved hit rather than opening a second one
	later := now.Add(limitRescueRetryInterval + time.Minute)
	d.standingHealthPass(a, rig.st, all, live, later)
	got := contractStandingGetHit(t, rig.st, standingHitKey("iris"))
	if got.Deliveries != 2 {
		t.Fatalf("past the backoff the trigger did not re-arm: %+v", got)
	}
	if got.At != now.Unix() {
		t.Errorf("At moved to %d — the re-delivery must fold onto the first hit, not restart it", got.At)
	}
}

// TestContractStandingHealthPassSkipsWhenDisabled: "Skips unless a.Enabled()
// and live[a.Session]." A machine that has never opted in must pay nothing
// and record nothing.
func TestContractStandingHealthPassSkipsWhenDisabled(t *testing.T) {
	rig := contractStandingRigNew(t)
	const sid = "iris-sess-disabled"
	a, all, live := contractStandingLiveIris(t, rig, sid, contractStandingUsageLimitText)
	// turn the opt-in back off
	if err := os.Remove(core.SlackEnvPath()); err != nil {
		t.Fatal(err)
	}
	if a.Enabled() {
		t.Fatal("fixture is wrong: iris is still enabled")
	}

	newDaemon().standingHealthPass(a, rig.st, all, live, time.Unix(1_700_000_000, 0))

	if got := contractStandingGetHit(t, rig.st, standingHitKey("iris")); got.At != 0 {
		t.Fatalf("a disabled agent recorded a hit: %+v", got)
	}
}

// TestContractStandingHealthPassSkipsWhenTheSessionIsNotLive: the second half
// of the same gate. A dead session has no stall to nudge and nothing to
// rescue; classifying its last transcript would record its previous life's
// banner forever.
func TestContractStandingHealthPassSkipsWhenTheSessionIsNotLive(t *testing.T) {
	rig := contractStandingRigNew(t)
	const sid = "iris-sess-dead"
	a, all, _ := contractStandingLiveIris(t, rig, sid, contractStandingUsageLimitText)

	newDaemon().standingHealthPass(a, rig.st, all, map[string]bool{}, time.Unix(1_700_000_000, 0))

	if got := contractStandingGetHit(t, rig.st, standingHitKey("iris")); got.At != 0 {
		t.Fatalf("a dead session recorded a hit: %+v", got)
	}
}

// TestContractStandingHealthPassRecordsNothingWithoutARecordedSession: the
// pass classifies through standingHealth, which refuses a missing recorded
// id. Here the peer is live and named and its own SessionID has a limit
// banner behind it — and no kv row exists, so nothing may be recorded.
func TestContractStandingHealthPassRecordsNothingWithoutARecordedSession(t *testing.T) {
	rig := contractStandingRigNew(t)
	contractStandingEnableIris(t)
	const discovered = "iris-sess-guessed"
	contractStandingTranscript(t, discovered, contractStandingUsageLimitText)
	a := contractStandingRow(t, "iris")
	all := []*core.Peer{contractStandingPeer("iris", "%1", IrisAgentName, discovered)}

	newDaemon().standingHealthPass(a, rig.st, all, map[string]bool{"iris": true}, time.Unix(1_700_000_000, 0))

	if got := contractStandingGetHit(t, rig.st, standingHitKey("iris")); got.At != 0 {
		t.Fatalf("classified a discovered session id and recorded %+v", got)
	}
}

// ---------------------------------------------------------------------------
// slice S — standingLimitRescue
// ---------------------------------------------------------------------------

// contractStandingRescueWorld wires iris up for the rescue pass: enabled,
// with a recorded session, a named live peer, and a usage snapshot the
// selector can be consulted against.
func contractStandingRescueWorld(t *testing.T, rig *contractStandingRig, sid string) (standingAgent, []*core.Peer, map[string]bool) {
	t.Helper()
	contractStandingEnableIris(t)
	contractStandingSnapshot(t)
	if err := writeIrisAgent(rig.st, irisAgent{SessionID: sid, SpawnedAt: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	all := []*core.Peer{contractStandingPeer("iris", "%1", IrisAgentName, sid)}
	return contractStandingRow(t, "iris"), all, map[string]bool{"iris": true}
}

// TestContractStandingLimitRescueSkipsAnEmptyOrResolvedHit: "short-circuit on
// empty/resolved hit". Nothing ever deletes this row, so without the resolved
// short-circuit every agent that has EVER hit a limit would pay a usage
// snapshot read and a full rotation ranking every tick for the daemon's life.
func TestContractStandingLimitRescueSkipsAnEmptyOrResolvedHit(t *testing.T) {
	rig := contractStandingRigNew(t)
	rig.Target = "pool-b"
	a, all, live := contractStandingRescueWorld(t, rig, "iris-sess-skip")
	now := time.Unix(1_700_000_000, 0)
	key := standingHitKey("iris")

	// empty
	newDaemon().standingLimitRescue(a, rig.st, all, live, now)
	if len(rig.Swaps) != 0 || len(rig.Respawns) != 0 {
		t.Fatalf("an empty hit acted: swaps=%v respawns=%v", rig.Swaps, rig.Respawns)
	}
	if got := contractStandingGetHit(t, rig.st, key); got.At != 0 {
		t.Fatalf("an empty hit wrote a record: %+v", got)
	}

	// resolved
	resolved := LimitHit{
		Error: "rate_limit", At: now.Add(-time.Hour).Unix(), Deliveries: 1,
		HandledAt: now.Add(-30 * time.Minute).Unix(), Outcome: "rotated",
		SwappedAt: now.Add(-50 * time.Minute).Unix(), SwappedTo: "pool-b@example.com",
	}
	contractStandingPutHit(t, rig.st, key, resolved)
	newDaemon().standingLimitRescue(a, rig.st, all, live, now)
	if len(rig.Swaps) != 0 || len(rig.Respawns) != 0 {
		t.Fatalf("a resolved hit acted: swaps=%v respawns=%v", rig.Swaps, rig.Respawns)
	}
	if got := contractStandingGetHit(t, rig.st, key); got != resolved {
		t.Fatalf("a resolved hit was rewritten:\n got %+v\nwant %+v", got, resolved)
	}
}

// TestContractStandingLimitRescueNonRateLimitIsRecordedButNeverActedOn: gate 3
// of the reused chain. An auth failure or an overload is not fixed by a
// different account, so it is recorded (that record is how a change to Claude
// Code's error enum gets noticed) and nothing else.
func TestContractStandingLimitRescueNonRateLimitIsRecordedButNeverActedOn(t *testing.T) {
	rig := contractStandingRigNew(t)
	rig.Target = "pool-b"
	a, all, live := contractStandingRescueWorld(t, rig, "iris-sess-auth")
	now := time.Unix(1_700_000_000, 0)
	key := standingHitKey("iris")
	seeded := LimitHit{Error: "authentication_failed", Details: "Invalid API key", At: now.Add(-time.Minute).Unix(), Deliveries: 1}
	contractStandingPutHit(t, rig.st, key, seeded)

	newDaemon().standingLimitRescue(a, rig.st, all, live, now)

	if len(rig.Swaps) != 0 || len(rig.Respawns) != 0 {
		t.Fatalf("a non-rate_limit hit acted: swaps=%v respawns=%v", rig.Swaps, rig.Respawns)
	}
	if got := contractStandingGetHit(t, rig.st, key); got != seeded {
		t.Fatalf("a non-rate_limit hit was rewritten:\n got %+v\nwant %+v", got, seeded)
	}
}

// TestContractStandingLimitRescueSkipsWhenTheAgentSessionIsNotLive: the
// decide chain is handed `live[a.Session]` where the cell pass hands cellLive,
// and !live is an unconditional skip — the resume respawns a live session's
// agent, and a dead one has none to respawn.
func TestContractStandingLimitRescueSkipsWhenTheAgentSessionIsNotLive(t *testing.T) {
	rig := contractStandingRigNew(t)
	rig.Target = "pool-b"
	a, all, _ := contractStandingRescueWorld(t, rig, "iris-sess-notlive")
	now := time.Unix(1_700_000_000, 0)
	key := standingHitKey("iris")
	seeded := LimitHit{Error: "rate_limit", Details: "429", At: now.Add(-time.Minute).Unix(), Deliveries: 1}
	contractStandingPutHit(t, rig.st, key, seeded)

	newDaemon().standingLimitRescue(a, rig.st, all, map[string]bool{}, now)

	if len(rig.Swaps) != 0 || len(rig.Respawns) != 0 {
		t.Fatalf("a dead agent's hit acted: swaps=%v respawns=%v", rig.Swaps, rig.Respawns)
	}
	if got := contractStandingGetHit(t, rig.st, key); got != seeded {
		t.Fatalf("a dead agent's hit was rewritten: %+v", got)
	}
}

// TestContractStandingLimitRescueRotatesThenResumes is the two-tick shape the
// whole feature is: tick one swaps and records the stamp WITHOUT resolving;
// tick two, a settle window later, kills and respawns and only then resolves.
// The gap is a debounce, not propagation — a fresh process reads the keychain
// at startup — so the swap must be recorded and visibly complete before
// anything SIGKILLs a pane.
func TestContractStandingLimitRescueRotatesThenResumes(t *testing.T) {
	rig := contractStandingRigNew(t)
	rig.Target = "pool-b"
	a, all, live := contractStandingRescueWorld(t, rig, "iris-sess-rotate")
	key := standingHitKey("iris")
	t0 := time.Unix(1_700_000_000, 0)
	contractStandingPutHit(t, rig.st, key, LimitHit{
		Error: "rate_limit", Details: "429", At: t0.Add(-time.Minute).Unix(), Deliveries: 1,
	})

	d := newDaemon()
	d.standingLimitRescue(a, rig.st, all, live, t0)

	if len(rig.Swaps) != 1 || rig.Swaps[0] != "pool-b" {
		t.Fatalf("tick 1 swaps = %v, want exactly [pool-b]", rig.Swaps)
	}
	if len(rig.Respawns) != 0 {
		t.Fatalf("tick 1 also respawned: %+v", rig.Respawns)
	}
	after := contractStandingGetHit(t, rig.st, key)
	if after.SwappedAt != t0.Unix() || after.SwappedTo != rig.SwapTo {
		t.Fatalf("tick 1 did not stamp the swap: %+v", after)
	}
	if after.Attempts != 1 {
		t.Errorf("Attempts = %d after one rotate, want 1", after.Attempts)
	}
	if after.HandledAt != 0 {
		t.Fatalf("tick 1 resolved the hit (%+v) — the resume is what resolves it", after)
	}
	// the machine-wide watermark is stamped too, so every OTHER unresolved
	// hit adopts this swap rather than making a second one. The stamp's clock
	// is a FRESH post-swap time.Now() rather than the pass' injected `now`
	// (contract amendment r5: a pre-swap stamp made hits recorded during the
	// swap itself read as newer than the repair), so this pins the stamp's
	// presence and target plus the clock's direction — at or after the pass'
	// own decision time — never its exact value.
	if at, to := readMainSwap(); at < t0.Unix() || to != rig.SwapTo {
		t.Errorf("readMainSwap() = (%d, %q) after the rotate, want at >= %d and %q", at, to, t0.Unix(), rig.SwapTo)
	}

	// tick two, past the settle window
	t1 := t0.Add(limitRescueSettle + time.Second)
	d.standingLimitRescue(a, rig.st, all, live, t1)

	if len(rig.Swaps) != 1 {
		t.Fatalf("tick 2 swapped again: %v", rig.Swaps)
	}
	if len(rig.Respawns) != 1 {
		t.Fatalf("tick 2 respawns = %+v, want exactly one", rig.Respawns)
	}
	if got := rig.Respawns[0].agentID; got != "iris" {
		t.Errorf("respawned agent %q, want iris", got)
	}
	done := contractStandingGetHit(t, rig.st, key)
	if done.HandledAt != t1.Unix() {
		t.Errorf("HandledAt = %d, want %d", done.HandledAt, t1.Unix())
	}
	if done.Outcome != "rotated" {
		t.Errorf("Outcome = %q, want rotated", done.Outcome)
	}
}

// TestContractStandingLimitRescueManualPinParksWithoutRotating: gate 6 sits
// above every rotate path, so an agent on a machine carl has pinned to manual
// is never rotated whatever else is true — and the park is deliberately not
// counted as an attempt, since there is nothing here to retry.
func TestContractStandingLimitRescueManualPinParksWithoutRotating(t *testing.T) {
	rig := contractStandingRigNew(t)
	rig.Target = "pool-b"
	a, all, live := contractStandingRescueWorld(t, rig, "iris-sess-manual")
	if err := SetRotationMode("manual", "test"); err != nil {
		t.Fatal(err)
	}
	if RotationMode() != "manual" {
		t.Fatal("fixture is wrong: rotation mode is not manual")
	}
	now := time.Unix(1_700_000_000, 0)
	key := standingHitKey("iris")
	hitAt := now.Add(-time.Minute)
	contractStandingPutHit(t, rig.st, key, LimitHit{
		Error: "rate_limit", Details: "429", At: hitAt.Unix(), Deliveries: 1,
	})

	d := newDaemon()
	before := len(d.Notified)
	d.standingLimitRescue(a, rig.st, all, live, now)

	if len(rig.Swaps) != 0 {
		t.Fatalf("a manual pin was rotated: %v", rig.Swaps)
	}
	if len(rig.Respawns) != 0 {
		t.Fatalf("a manual pin was killed and respawned: %+v", rig.Respawns)
	}
	got := contractStandingGetHit(t, rig.st, key)
	if got.Outcome != "parked-manual" || got.HandledAt != now.Unix() {
		t.Fatalf("record = %+v, want parked-manual resolved at %d", got, now.Unix())
	}
	if got.Attempts != 0 {
		t.Errorf("Attempts = %d — a manual park counts nothing, there is nothing to retry", got.Attempts)
	}
	// "notifications deduped per hit through d.Notified with keys carrying
	// hit.At": exactly one key is added, and it names the agent and the hit.
	if len(d.Notified) != before+1 {
		t.Fatalf("the park added %d dedup keys, want exactly 1: %v", len(d.Notified)-before, contractStandingKeys(d))
	}
	var key0 string
	for k := range d.Notified {
		key0 = k
	}
	if !strings.Contains(key0, "iris") {
		t.Errorf("dedup key %q does not name the agent — two agents would share one notification", key0)
	}
	if !strings.Contains(key0, strconv.FormatInt(hitAt.Unix(), 10)) {
		t.Errorf("dedup key %q does not carry hit.At — the next limit on this agent would be silent", key0)
	}
}

// TestContractStandingLimitRescueLandedSwapOutranksTheManualPin: gate 5 sits
// ABOVE gate 6. Once the account has actually moved, the resume must complete
// even if the mode flipped in between — a swapped agent left un-resumed is
// worse than either outcome on its own.
func TestContractStandingLimitRescueLandedSwapOutranksTheManualPin(t *testing.T) {
	rig := contractStandingRigNew(t)
	rig.Target = "pool-b"
	a, all, live := contractStandingRescueWorld(t, rig, "iris-sess-flip")
	if err := SetRotationMode("manual", "test"); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	key := standingHitKey("iris")
	contractStandingPutHit(t, rig.st, key, LimitHit{
		Error: "rate_limit", Details: "429", At: now.Add(-5 * time.Minute).Unix(), Deliveries: 1,
		Attempts: 1, LastAttempt: now.Add(-time.Minute).Unix(),
		SwappedAt: now.Add(-time.Minute).Unix(), SwappedTo: "pool-b@example.com",
	})

	newDaemon().standingLimitRescue(a, rig.st, all, live, now)

	if len(rig.Swaps) != 0 {
		t.Fatalf("a landed swap rotated again: %v", rig.Swaps)
	}
	if len(rig.Respawns) != 1 {
		t.Fatalf("respawns = %+v, want exactly one — a landed swap resumes past a mode flip", rig.Respawns)
	}
	got := contractStandingGetHit(t, rig.st, key)
	if got.Outcome != "rotated" || got.HandledAt != now.Unix() {
		t.Fatalf("record = %+v, want rotated and resolved", got)
	}
}

// TestContractStandingLimitRescueAdoptsTheMachineWideWatermark: "adopt the
// watermark via readMainSwap + adoptLandedSwap ... before deciding, same
// tick". A target IS available here and the mode IS auto, so the only thing
// stopping a second machine-wide swap is the adoption.
func TestContractStandingLimitRescueAdoptsTheMachineWideWatermark(t *testing.T) {
	rig := contractStandingRigNew(t)
	rig.Target = "pool-b"
	a, all, live := contractStandingRescueWorld(t, rig, "iris-sess-adopt")
	now := time.Unix(1_700_000_000, 0)
	key := standingHitKey("iris")
	hitAt := now.Add(-5 * time.Minute)
	contractStandingPutHit(t, rig.st, key, LimitHit{
		Error: "rate_limit", Details: "429", At: hitAt.Unix(), Deliveries: 1,
	})
	swapAt := now.Add(-2 * time.Minute)
	if err := recordMainSwap("pool-c@example.com", swapAt); err != nil {
		t.Fatal(err)
	}

	newDaemon().standingLimitRescue(a, rig.st, all, live, now)

	if len(rig.Swaps) != 0 {
		t.Fatalf("the standing pass swapped despite an adoptable watermark: %v", rig.Swaps)
	}
	got := contractStandingGetHit(t, rig.st, key)
	if got.SwappedAt != swapAt.Unix() || got.SwappedTo != "pool-c@example.com" {
		t.Fatalf("the watermark was not adopted: %+v", got)
	}
	if len(rig.Respawns) != 1 {
		t.Fatalf("respawns = %+v, want one — adoption still resumes", rig.Respawns)
	}
	if got.HandledAt != now.Unix() || got.Outcome != "rotated" {
		t.Fatalf("the resume did not resolve the adopted hit: %+v", got)
	}
}

// TestContractStandingLimitRescueIgnoresAWatermarkPredatingTheHit: "a
// watermark predating the hit changes nothing". The swap happened BEFORE this
// limit was observed, so it is not the swap that fixes it — this hit still
// needs a rotate of its own.
func TestContractStandingLimitRescueIgnoresAWatermarkPredatingTheHit(t *testing.T) {
	rig := contractStandingRigNew(t)
	rig.Target = "pool-b"
	a, all, live := contractStandingRescueWorld(t, rig, "iris-sess-oldmark")
	now := time.Unix(1_700_000_000, 0)
	key := standingHitKey("iris")
	hitAt := now.Add(-time.Minute)
	contractStandingPutHit(t, rig.st, key, LimitHit{
		Error: "rate_limit", Details: "429", At: hitAt.Unix(), Deliveries: 1,
	})
	if err := recordMainSwap("pool-c@example.com", hitAt.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}

	newDaemon().standingLimitRescue(a, rig.st, all, live, now)

	if len(rig.Swaps) != 1 {
		t.Fatalf("swaps = %v, want exactly one — a stale watermark must not be adopted", rig.Swaps)
	}
	got := contractStandingGetHit(t, rig.st, key)
	if got.SwappedTo != rig.SwapTo {
		t.Errorf("SwappedTo = %q, want this rotate's own answer %q", got.SwappedTo, rig.SwapTo)
	}
	if got.SwappedAt != now.Unix() {
		t.Errorf("SwappedAt = %d, want this tick's %d", got.SwappedAt, now.Unix())
	}
}

// TestContractStandingLimitRescueLockBusyRecordsNothing: "Lock-busy rotate ...
// record NOTHING (retry next tick)." The rotation lock is contended by every
// cell spawn on the machine, so counting a lost race as an attempt would park
// a hit that never once reached the pool.
func TestContractStandingLimitRescueLockBusyRecordsNothing(t *testing.T) {
	rig := contractStandingRigNew(t)
	rig.Target = "pool-b"
	a, all, live := contractStandingRescueWorld(t, rig, "iris-sess-lock")
	now := time.Unix(1_700_000_000, 0)
	key := standingHitKey("iris")
	seeded := LimitHit{Error: "rate_limit", Details: "429", At: now.Add(-time.Minute).Unix(), Deliveries: 1}
	contractStandingPutHit(t, rig.st, key, seeded)

	release, ok, err := AcquireRotationLock()
	if err != nil || !ok {
		t.Fatalf("could not take the rotation lock: ok=%v err=%v", ok, err)
	}
	defer release()

	newDaemon().standingLimitRescue(a, rig.st, all, live, now)

	if len(rig.Swaps) != 0 {
		t.Fatalf("swapped while the lock was held: %v", rig.Swaps)
	}
	if got := contractStandingGetHit(t, rig.st, key); got != seeded {
		t.Fatalf("a lock-busy round recorded something:\n got %+v\nwant %+v", got, seeded)
	}
}

// TestContractStandingLimitRescueRefusedResumeNeverResolves: the kill guard's
// refusal is "a deferral, never a resolution". A hit resolved with the agent
// NOT resumed is a permanently stranded standing agent: the credential moved,
// its process is still latched onto the old one, and a stalled agent makes no
// further API calls, so it can never produce another hit to be rescued by.
func TestContractStandingLimitRescueRefusedResumeNeverResolves(t *testing.T) {
	rig := contractStandingRigNew(t)
	rig.Target = "pool-b"
	contractStandingEnableIris(t)
	contractStandingSnapshot(t)
	if err := writeIrisAgent(rig.st, irisAgent{SessionID: "iris-sess-refuse", SpawnedAt: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	a := contractStandingRow(t, "iris")
	// a claude in iris' session that is NOT iris by either disjunct
	all := []*core.Peer{contractStandingPeer("iris", "%1", "someone-else", "someone-else-sess")}
	live := map[string]bool{"iris": true}

	now := time.Unix(1_700_000_000, 0)
	key := standingHitKey("iris")
	contractStandingPutHit(t, rig.st, key, LimitHit{
		Error: "rate_limit", Details: "429", At: now.Add(-5 * time.Minute).Unix(), Deliveries: 1,
		Attempts: 1, LastAttempt: now.Add(-2 * time.Minute).Unix(),
		SwappedAt: now.Add(-2 * time.Minute).Unix(), SwappedTo: "pool-b@example.com",
	})

	newDaemon().standingLimitRescue(a, rig.st, all, live, now)

	if len(rig.Respawns) != 0 {
		t.Fatalf("a foreign claude was handed to the respawn seam: %+v", rig.Respawns)
	}
	got := contractStandingGetHit(t, rig.st, key)
	if got.HandledAt != 0 {
		t.Fatalf("a refused resume resolved the hit: %+v", got)
	}
	if got.Outcome != "" {
		t.Errorf("Outcome = %q on a refused resume, want empty", got.Outcome)
	}
}

// TestContractStandingLimitRescueRefusedResumesEventuallyPark: "a refusal or
// respawn failure is the "no-agents" outcome ... attempt caps and the
// path-relative failCap included". An open swapped hit that can never be
// resumed must not re-decide forever: increment-then-test means the round
// that reaches the cap is the one that parks.
//
// See the report — the contract also says a refused resume records NOTHING,
// which cannot both be true and leave the cap reachable. This asserts the
// reading that keeps the cap alive.
func TestContractStandingLimitRescueRefusedResumesEventuallyPark(t *testing.T) {
	rig := contractStandingRigNew(t)
	rig.Target = "pool-b"
	contractStandingEnableIris(t)
	contractStandingSnapshot(t)
	if err := writeIrisAgent(rig.st, irisAgent{SessionID: "iris-sess-park", SpawnedAt: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	a := contractStandingRow(t, "iris")
	all := []*core.Peer{contractStandingPeer("iris", "%1", "someone-else", "someone-else-sess")}
	live := map[string]bool{"iris": true}

	now := time.Unix(1_700_000_000, 0)
	key := standingHitKey("iris")
	contractStandingPutHit(t, rig.st, key, LimitHit{
		Error: "rate_limit", Details: "429", At: now.Add(-time.Hour).Unix(), Deliveries: 1,
		SwappedAt: now.Add(-30 * time.Minute).Unix(), SwappedTo: "pool-b@example.com",
	})

	d := newDaemon()
	for i := 0; i < limitRescueMaxAttempts; i++ {
		d.standingLimitRescue(a, rig.st, all, live, now.Add(time.Duration(i)*time.Minute))
	}

	got := contractStandingGetHit(t, rig.st, key)
	if got.HandledAt == 0 {
		t.Fatalf("after %d refused resume rounds the hit is still open: %+v", limitRescueMaxAttempts, got)
	}
	if got.Outcome != "parked-no-agents" {
		t.Errorf("Outcome = %q, want parked-no-agents", got.Outcome)
	}
	if len(rig.Swaps) != 0 {
		t.Errorf("a resume-arm park rotated: %v", rig.Swaps)
	}
}

// TestContractStandingLimitRescueRespawnFailureIsTheNoAgentsOutcome: a seam
// that returns an error is the same "reached nobody" as a refusal. The
// contract makes the seam's error contract explicit for one reason —
// EnsureIris returns nil on its silent spawn-backoff arm, and a
// nil read as success would resolve a hit whose agent never came back.
func TestContractStandingLimitRescueRespawnFailureIsTheNoAgentsOutcome(t *testing.T) {
	rig := contractStandingRigNew(t)
	rig.Target = "pool-b"
	rig.RespawnErr = fmt.Errorf("tmux refused the respawn")
	a, all, live := contractStandingRescueWorld(t, rig, "iris-sess-respawnfail")
	now := time.Unix(1_700_000_000, 0)
	key := standingHitKey("iris")
	contractStandingPutHit(t, rig.st, key, LimitHit{
		Error: "rate_limit", Details: "429", At: now.Add(-5 * time.Minute).Unix(), Deliveries: 1,
		SwappedAt: now.Add(-2 * time.Minute).Unix(), SwappedTo: "pool-b@example.com",
	})

	newDaemon().standingLimitRescue(a, rig.st, all, live, now)

	if len(rig.Respawns) != 1 {
		t.Fatalf("respawns = %+v, want exactly one attempt", rig.Respawns)
	}
	got := contractStandingGetHit(t, rig.st, key)
	if got.HandledAt != 0 {
		t.Fatalf("a failed respawn resolved the hit: %+v", got)
	}
	if got.Outcome == "rotated" {
		t.Fatalf("a failed respawn recorded the success outcome: %+v", got)
	}
}

// TestContractStandingLimitRescueResumesAfterTheWindowResets: "exhausted
// attempts with a parseable reset hold unresolved then release the resume
// after reset + settle", and the resume that follows a reset — where the
// account never moved — resolves as `resumed-after-reset` rather than
// `rotated`. Four cells measured 2026-08-06 sat 42m to 10h37m dead on the far
// side of a reset their own banner named, because the park was terminal.
func TestContractStandingLimitRescueResumesAfterTheWindowResets(t *testing.T) {
	rig := contractStandingRigNew(t)
	rig.Target = "" // no rotation target: the reset is the only way back
	a, all, live := contractStandingRescueWorld(t, rig, "iris-sess-reset")

	now := time.Unix(1_700_000_000, 0)
	anchor := now.Add(-8 * time.Hour)
	// A banner naming a reset one hour after the hit — long past by `now`,
	// and well inside limitResetMaxWait of the anchor so the staleness cap
	// does not rewrite it.
	details := "You've hit your session limit · resets " + strings.ToLower(now.Add(-7*time.Hour).Format("3:04pm"))
	resetAt := parseLimitReset(details, anchor)
	if resetAt == 0 {
		t.Fatalf("fixture is wrong: %q carries no parseable clause", details)
	}
	if now.Sub(time.Unix(resetAt, 0)) < limitResetSettle {
		t.Fatalf("fixture is wrong: the reset at %v is not yet past %v + settle", time.Unix(resetAt, 0), now)
	}

	key := standingHitKey("iris")
	contractStandingPutHit(t, rig.st, key, LimitHit{
		Error: "rate_limit", Details: details, At: anchor.Unix(), Deliveries: 2,
		Attempts: limitRescueMaxAttempts, LastAttempt: anchor.Add(30 * time.Minute).Unix(),
		Outcome: "parked-until-reset",
	})

	newDaemon().standingLimitRescue(a, rig.st, all, live, now)

	if len(rig.Swaps) != 0 {
		t.Fatalf("the reset arm swapped: %v", rig.Swaps)
	}
	if len(rig.Respawns) != 1 {
		t.Fatalf("respawns = %+v, want exactly one", rig.Respawns)
	}
	got := contractStandingGetHit(t, rig.st, key)
	if got.HandledAt != now.Unix() {
		t.Fatalf("the reset resume did not resolve the hit: %+v", got)
	}
	if got.Outcome != "resumed-after-reset" {
		t.Errorf("Outcome = %q, want resumed-after-reset — no credential ever moved on this path", got.Outcome)
	}
	if got.SwappedAt != 0 {
		t.Errorf("SwappedAt = %d on the reset path, want 0", got.SwappedAt)
	}
}

// TestContractStandingLimitRescueHoldsUnresolvedUntilTheResetPasses is the
// first half of that rule stated on its own: with the attempts spent and the
// banner's reset still in the future, the pass does nothing and writes
// nothing. Resolving here would close the one record that can wake the agent.
func TestContractStandingLimitRescueHoldsUnresolvedUntilTheResetPasses(t *testing.T) {
	rig := contractStandingRigNew(t)
	rig.Target = ""
	a, all, live := contractStandingRescueWorld(t, rig, "iris-sess-hold")

	now := time.Unix(1_700_000_000, 0)
	anchor := now.Add(-30 * time.Minute)
	details := "You've hit your session limit · resets " + strings.ToLower(now.Add(time.Hour).Format("3:04pm"))
	resetAt := parseLimitReset(details, anchor)
	if resetAt == 0 || time.Unix(resetAt, 0).Before(now) {
		t.Fatalf("fixture is wrong: %q parsed to %v, want a moment still ahead of %v", details, time.Unix(resetAt, 0), now)
	}

	key := standingHitKey("iris")
	seeded := LimitHit{
		Error: "rate_limit", Details: details, At: anchor.Unix(), Deliveries: 2,
		Attempts: limitRescueMaxAttempts, LastAttempt: anchor.Add(10 * time.Minute).Unix(),
		Outcome: "parked-until-reset",
	}
	contractStandingPutHit(t, rig.st, key, seeded)

	newDaemon().standingLimitRescue(a, rig.st, all, live, now)

	if len(rig.Swaps) != 0 || len(rig.Respawns) != 0 {
		t.Fatalf("the wait acted: swaps=%v respawns=%+v", rig.Swaps, rig.Respawns)
	}
	got := contractStandingGetHit(t, rig.st, key)
	if got.HandledAt != 0 {
		t.Fatalf("the wait resolved the hit: %+v", got)
	}
	if got.Attempts != seeded.Attempts {
		t.Errorf("Attempts = %d during the wait, want the seeded %d", got.Attempts, seeded.Attempts)
	}
}
