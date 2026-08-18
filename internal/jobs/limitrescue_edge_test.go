package jobs

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"kraken/internal/core"
	"kraken/internal/store"
)

// limitrescue_edge_test.go — the EDGE dimension of the mid-run usage-limit
// rescue (artifacts/50-contract.md, slices A and B): boundaries, refusals,
// degradation, and the two gate orderings the contract calls obligations
// rather than incidental. Written blind against the contract text, before
// the implementation existed; the happy paths and stated invariants belong
// to the sibling author's *_contract_test.go in this same package, so every
// package-level identifier here carries the `edge` prefix (one declaration
// space per package — an unprefixed helper would take the whole test binary
// down).
//
// Two boundaries are deliberately NOT crossed here, per the contract's own
// note: rescueLimitedCell performs a real machine-wide credential swap and
// resumeCellAgents SIGKILLs the real process in zeus' pane, so both are
// exercised through their refusal paths ONLY (and, for the resume, the
// respawnPeerPane seam) — never by performing a swap or a respawn.

// edgeNow is a fixed, whole-second instant. Whole seconds matter: the record
// stores unix SECONDS (At/SwappedAt/LastAttempt), so a now carrying
// nanoseconds would put every window boundary a sub-second off and make the
// "exactly at the boundary" cases test something other than the boundary.
var edgeNow = time.Unix(1_800_000_000, 0)

// edgeJob builds the *Job the record's readers and writers key off. Only ID
// and Slug are read — the id for the kv key (Amendment 5), the slug for the
// error text — so nothing here needs a worktree or a live cell.
func edgeJob(slug string, id int64) *Job { return &Job{Slug: slug, ID: id} }

// --- limitHitKey ---

// Amendment 5: the key is the job's IMMUTABLE ID, never its slug. A slug is
// unique only among ACTIVE jobs and archiving frees it, nothing ever deletes
// this row, and a hit on a torn-down cell is never resolved — so a
// slug-keyed row let a NEW job reusing an archived generation's name read
// the dead generation's record and swap the machine's login on evidence from
// a job that no longer exists. Two generations of one name must therefore
// produce two different keys, which is the assertion that matters most here.
func TestEdgeLimitHitKeyIsKeyedOnTheImmutableID(t *testing.T) {
	cases := []struct {
		id   int64
		want string
	}{
		{0, "limit_hit:0"},
		{1, "limit_hit:1"},
		{40, "limit_hit:40"},
		{9007199254740993, "limit_hit:9007199254740993"},
	}
	for _, c := range cases {
		if got := limitHitKey(c.id); got != c.want {
			t.Errorf("limitHitKey(%d) = %q, want %q", c.id, got, c.want)
		}
	}

	// the finding in one line: same slug, different generations, different rows
	old, fresh := edgeJob("fix-thing", 12), edgeJob("fix-thing", 40)
	if limitHitKey(old.ID) == limitHitKey(fresh.ID) {
		t.Fatalf("two generations of slug %q share the key %q — an archived generation's hit would be acted on by the new job", old.Slug, limitHitKey(old.ID))
	}
}

// --- LimitHit JSON shape ---

// Slice A declares SwappedAt/SwappedTo but never writes them; slice B is
// their only writer. The obligation slice A carries is that its marshal /
// unmarshal round-trips them UNCHANGED — a record written by the rescue
// pass and then folded by a later delivery must not silently lose the swap
// watermark, which is the one thing standing between "resume once" and "swap
// again".
func TestEdgeLimitHitRoundTripsEveryField(t *testing.T) {
	want := LimitHit{
		Error: "rate_limit", Details: "you've hit your session limit",
		At: edgeNow.Unix(), SessionID: "sess-1", AgentID: "agent-9",
		Deliveries: 4, HandledAt: edgeNow.Unix() + 60, Outcome: "rotated",
		Attempts: 2, LastAttempt: edgeNow.Unix() + 10,
		SwappedAt: edgeNow.Unix() + 20, SwappedTo: "someone@example.com",
	}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got LimitHit
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}

	// the wire names are contract-fixed (a stored record outlives the code
	// that wrote it, so a renamed tag reads every past record as zero)
	var keys map[string]any
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{
		"error", "details", "at", "session_id", "agent_id", "deliveries",
		"handled_at", "outcome", "attempts", "last_attempt_at",
		"swapped_at", "swapped_to",
	} {
		if _, ok := keys[k]; !ok {
			t.Errorf("marshalled record has no %q key: %s", k, raw)
		}
	}
}

// A zero-value LimitHit (At == 0) means NO hit is recorded — the shape
// ReadLimitHit degrades to and the shape gate 1 of the decision skips on.
func TestEdgeZeroLimitHitMeansNoHit(t *testing.T) {
	var zero LimitHit
	if zero.At != 0 {
		t.Fatalf("zero LimitHit.At = %d, want 0", zero.At)
	}
	if act, _ := limitRescueDecide(zero, "auto", true, true, edgeNow); act != rescueSkip {
		t.Fatalf("decide(zero hit) = %q, want %q", act, rescueSkip)
	}
}

// --- parseLimitPayload ---

// The parser's `ok` is false ONLY for invalid JSON or an empty parsed error
// type. Everything else — absent optionals, unknown extras, an api_error of
// any shape — parses. Absent `agent_id` and `error_details` are the NORMAL
// case on the main thread (contract's captured real payload), not an error
// case.
func TestEdgeParseLimitPayloadRefusalsAndTolerance(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		wantOK    bool
		wantErr   string
		wantDet   string
		wantSess  string
		wantAgent string
	}{
		{name: "not JSON at all", raw: `{not json`, wantOK: false},
		{name: "empty input", raw: ``, wantOK: false},
		{name: "whitespace only", raw: "   \n\t", wantOK: false},
		{name: "truncated mid-object", raw: `{"error":"rate_li`, wantOK: false},
		{name: "JSON null", raw: `null`, wantOK: false},
		{name: "empty object has no error type", raw: `{}`, wantOK: false},
		{
			name:   "error present but empty, no fallback available",
			raw:    `{"error":"","session_id":"s"}`,
			wantOK: false,
		},
		{
			name:    "error present but empty falls back to error_type",
			raw:     `{"error":"","error_type":"rate_limit"}`,
			wantOK:  true,
			wantErr: "rate_limit",
		},
		{
			name:    "error absent falls back to error_type",
			raw:     `{"error_type":"rate_limit","error_message":"legacy shape"}`,
			wantOK:  true,
			wantErr: "rate_limit",
			wantDet: "legacy shape",
		},
		{
			name:    "error wins over error_type when both are non-empty",
			raw:     `{"error":"rate_limit","error_type":"overloaded"}`,
			wantOK:  true,
			wantErr: "rate_limit",
		},
		{
			name:    "error_details wins over error_message when both present",
			raw:     `{"error":"rate_limit","error_details":"binary shape","error_message":"doc shape"}`,
			wantOK:  true,
			wantErr: "rate_limit",
			wantDet: "binary shape",
		},
		{
			// the real captured payload from the contract's ground-truth
			// section: no error_details, no agent_id, no api_error
			name: "real main-thread payload with optionals absent",
			raw: `{"session_id":"abc-123","transcript_path":"/x.jsonl","cwd":"/w",` +
				`"prompt_id":"p1","effort":{"level":"high"},"hook_event_name":"StopFailure",` +
				`"error":"authentication_failed","last_assistant_message":"Invalid API key"}`,
			wantOK:   true,
			wantErr:  "authentication_failed",
			wantSess: "abc-123",
		},
		{
			name:      "subagent payload carries agent_id",
			raw:       `{"error":"rate_limit","session_id":"s2","agent_id":"agent-7"}`,
			wantOK:    true,
			wantErr:   "rate_limit",
			wantSess:  "s2",
			wantAgent: "agent-7",
		},
		{
			name:    "api_error as an object is ignored, never fatal",
			raw:     `{"error":"rate_limit","api_error":{"type":"rate_limit_error","message":"nope"}}`,
			wantOK:  true,
			wantErr: "rate_limit",
		},
		{
			name:    "api_error as a bare string is ignored, never fatal",
			raw:     `{"error":"rate_limit","api_error":"rate_limit_error"}`,
			wantOK:  true,
			wantErr: "rate_limit",
		},
		{
			name:    "api_error as null is ignored, never fatal",
			raw:     `{"error":"rate_limit","api_error":null}`,
			wantOK:  true,
			wantErr: "rate_limit",
		},
		{
			name: "unknown extra fields of every shape never fail the parse",
			raw: `{"error":"rate_limit","brand_new_field":{"nested":[1,2,3]},` +
				`"another":true,"third":null,"fourth":12.5}`,
			wantOK:  true,
			wantErr: "rate_limit",
		},
		{
			name:    "an error type the rescue never acts on still parses",
			raw:     `{"error":"max_output_tokens"}`,
			wantOK:  true,
			wantErr: "max_output_tokens",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			errType, details, sess, agent, ok := parseLimitPayload([]byte(c.raw))
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v (errType=%q)", ok, c.wantOK, errType)
			}
			if !ok {
				return
			}
			if errType != c.wantErr {
				t.Errorf("errorType = %q, want %q", errType, c.wantErr)
			}
			if details != c.wantDet {
				t.Errorf("details = %q, want %q", details, c.wantDet)
			}
			if sess != c.wantSess {
				t.Errorf("sessionID = %q, want %q", sess, c.wantSess)
			}
			if agent != c.wantAgent {
				t.Errorf("agentID = %q, want %q", agent, c.wantAgent)
			}
		})
	}
}

// --- limitHookCommand / limitHookEntry ---

// The flag rule is "byte-for-byte the same rule prHookCommand applies"
// (contract). These are the three boundary cases that rule has: nothing
// set, an addr EQUAL to the default (must not be spelled out), and an addr
// that differs. Getting the equal-to-default case wrong is what makes the
// entry builder and the idempotency check disagree and re-append a hook
// entry on every reinstall.
func TestEdgeLimitHookCommandFlagBoundaries(t *testing.T) {
	cases := []struct {
		name   string
		addr   string
		dbPath string
		want   string
	}{
		{"neither set: bare command", "", "", "kraken limit-hook foo"},
		{"addr equal to the default is omitted", defaultServeAddr, "", "kraken limit-hook foo"},
		{"addr differing from the default is spelled out", "127.0.0.1:9999", "", "kraken limit-hook --serve-addr 127.0.0.1:9999 foo"},
		{"db path override rides along", "", "/custom/kraken.db", "kraken limit-hook --db-path /custom/kraken.db foo"},
		{"both flags together", "127.0.0.1:9999", "/custom/kraken.db", "kraken limit-hook --serve-addr 127.0.0.1:9999 --db-path /custom/kraken.db foo"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			SetHookServeAddr(c.addr)
			t.Cleanup(func() { SetHookServeAddr("") })
			t.Setenv("KRAKEN_DB_PATH", c.dbPath)

			if got := limitHookCommand("foo", "kraken"); got != c.want {
				t.Fatalf("limitHookCommand = %q, want %q", got, c.want)
			}
		})
	}
}

// The entry's matcher is the error-type enum value "rate_limit" (Claude
// Code matches StopFailure hooks against `error`), and its single hook must
// be exactly what limitHookCommand builds — the shared-builder property
// that keeps the entry and the dedup check from drifting.
func TestEdgeLimitHookEntryShape(t *testing.T) {
	SetHookServeAddr("")
	t.Cleanup(func() { SetHookServeAddr("") })
	t.Setenv("KRAKEN_DB_PATH", "")

	entry := limitHookEntry("foo", "kraken")
	if got := entry["matcher"]; got != "rate_limit" {
		t.Fatalf("matcher = %v, want %q", got, "rate_limit")
	}
	hooks, ok := entry["hooks"].([]any)
	if !ok || len(hooks) != 1 {
		t.Fatalf("hooks = %#v, want exactly one entry", entry["hooks"])
	}
	h, ok := hooks[0].(map[string]any)
	if !ok {
		t.Fatalf("hook entry = %#v, want a map", hooks[0])
	}
	if h["type"] != "command" {
		t.Errorf("hook type = %v, want %q", h["type"], "command")
	}
	if h["command"] != limitHookCommand("foo", "kraken") {
		t.Errorf("hook command = %v, want %q — the builder must be shared", h["command"], limitHookCommand("foo", "kraken"))
	}
}

// --- mergeHookSettings: the StopFailure install's edges ---

// edgeHookCommands pulls every command string out of one hook event group
// list, tolerating any shape it does not recognise (this is a merged file
// that a human also edits).
func edgeHookCommands(t *testing.T, settings []byte, event string) []string {
	t.Helper()
	var parsed map[string]any
	if err := json.Unmarshal(settings, &parsed); err != nil {
		t.Fatalf("merged settings are not valid JSON: %v\n%s", err, settings)
	}
	hooks, _ := parsed["hooks"].(map[string]any)
	groups, _ := hooks[event].([]any)
	var out []string
	for _, g := range groups {
		gm, _ := g.(map[string]any)
		inner, _ := gm["hooks"].([]any)
		for _, h := range inner {
			hm, _ := h.(map[string]any)
			if s, ok := hm["command"].(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

func edgeCountString(hay []string, needle string) int {
	n := 0
	for _, s := range hay {
		if s == needle {
			n++
		}
	}
	return n
}

// Invalid existing JSON returns an ERROR so the caller leaves the file
// alone — the one refusal in the merge path, and the reason a repo's own
// hand-edited settings can never be clobbered by a cell spawn.
func TestEdgeMergeHookSettingsInvalidJSONStillRefuses(t *testing.T) {
	for _, existing := range []string{"{not json", `{"hooks":`, `[]not-json`} {
		if _, err := mergeHookSettings([]byte(existing), "foo", "kraken"); err == nil {
			t.Errorf("mergeHookSettings(%q) err = nil, want an error so the caller skips the install", existing)
		}
	}
}

// The three merge properties, exercised on the StopFailure entry: unrelated
// top-level keys survive, an unrelated StopFailure group of the user's own
// survives beside kraken's, and a second install adds nothing.
func TestEdgeMergeHookSettingsStopFailureMergesNeverClobbers(t *testing.T) {
	SetHookServeAddr("")
	t.Cleanup(func() { SetHookServeAddr("") })
	t.Setenv("KRAKEN_DB_PATH", "")
	want := limitHookCommand("foo", "kraken")

	t.Run("unrelated top-level keys survive", func(t *testing.T) {
		got, err := mergeHookSettings([]byte(`{"env":{"FOO":"bar"},"permissions":{"allow":["Read"]}}`), "foo", "kraken")
		if err != nil {
			t.Fatal(err)
		}
		var parsed map[string]any
		if err := json.Unmarshal(got, &parsed); err != nil {
			t.Fatal(err)
		}
		if _, ok := parsed["permissions"]; !ok {
			t.Errorf("unrelated top-level key was dropped: %s", got)
		}
		if n := edgeCountString(edgeHookCommands(t, got, "StopFailure"), want); n != 1 {
			t.Errorf("StopFailure carries kraken's command %d times, want 1: %s", n, got)
		}
	})

	t.Run("a user's own StopFailure group survives beside kraken's", func(t *testing.T) {
		existing := `{"hooks":{"StopFailure":[{"matcher":"overloaded","hooks":[{"type":"command","command":"echo mine"}]}]}}`
		got, err := mergeHookSettings([]byte(existing), "foo", "kraken")
		if err != nil {
			t.Fatal(err)
		}
		cmds := edgeHookCommands(t, got, "StopFailure")
		if edgeCountString(cmds, "echo mine") != 1 {
			t.Errorf("the user's own StopFailure hook was clobbered: %v", cmds)
		}
		if edgeCountString(cmds, want) != 1 {
			t.Errorf("kraken's StopFailure hook is not installed exactly once: %v", cmds)
		}
	})

	t.Run("installing twice adds nothing the second time", func(t *testing.T) {
		first, err := mergeHookSettings(nil, "foo", "kraken")
		if err != nil {
			t.Fatal(err)
		}
		second, err := mergeHookSettings(first, "foo", "kraken")
		if err != nil {
			t.Fatal(err)
		}
		if n := edgeCountString(edgeHookCommands(t, second, "StopFailure"), want); n != 1 {
			t.Fatalf("after a second install the StopFailure command appears %d times, want 1: %s", n, second)
		}
	})

	t.Run("idempotent with a non-default addr and db path too", func(t *testing.T) {
		SetHookServeAddr("127.0.0.1:9999")
		t.Cleanup(func() { SetHookServeAddr("") })
		t.Setenv("KRAKEN_DB_PATH", "/custom/kraken.db")

		first, err := mergeHookSettings(nil, "foo", "kraken")
		if err != nil {
			t.Fatal(err)
		}
		second, err := mergeHookSettings(first, "foo", "kraken")
		if err != nil {
			t.Fatal(err)
		}
		cmd := limitHookCommand("foo", "kraken")
		if n := edgeCountString(edgeHookCommands(t, second, "StopFailure"), cmd); n != 1 {
			t.Fatalf("non-default flags broke idempotency: command appears %d times, want 1: %s", n, second)
		}
	})
}

// The StopFailure install must leave the PostToolUse pr-hook entry it lands
// beside completely untouched — the two entries share one file, and the
// pr-hook channel is how every PR this cell opens ever gets recorded.
func TestEdgeMergeHookSettingsLeavesPrHookEntryAlone(t *testing.T) {
	SetHookServeAddr("")
	t.Cleanup(func() { SetHookServeAddr("") })
	t.Setenv("KRAKEN_DB_PATH", "")

	prCmd := prHookCommand("foo", "kraken")
	existing := `{"hooks":{"PostToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"` + prCmd + `"}]}]}}`
	got, err := mergeHookSettings([]byte(existing), "foo", "kraken")
	if err != nil {
		t.Fatal(err)
	}
	post := edgeHookCommands(t, got, "PostToolUse")
	if edgeCountString(post, prCmd) != 1 {
		t.Fatalf("PostToolUse pr-hook entry = %v, want exactly one %q", post, prCmd)
	}
	// Deliberately NOT asserting len(post) == 1: the recall hook installs its
	// own matcher-less group under PostToolUse, so this key legitimately holds
	// more than the pr-hook group. What must hold is that installing the
	// StopFailure entry neither duplicates the pr-hook command nor drops it,
	// which the exactly-one count above already pins.
	if n := edgeCountString(edgeHookCommands(t, got, "StopFailure"), limitHookCommand("foo", "kraken")); n != 1 {
		t.Fatalf("StopFailure entry not installed exactly once alongside: %s", got)
	}
}

// --- stallReason / RetryableHealth / IsUsageLimitReason ---

// A usage-limit stall is not nudgeable: RetryableHealth must be false for
// it, and must not become true by accident through the existing "zeus hit
// an API error" prefix test. IsUsageLimitReason is the routing symbol, so
// its FALSE cases matter as much as its true one — a reason it wrongly
// claims sends the health pass down the rescue path for an ordinary stall.
func TestEdgeUsageLimitReasonRoutingEdges(t *testing.T) {
	usage := stallReason(core.Stall{Kind: "usage-limit", Text: "you've hit your session limit", Transient: false})
	if usage != "zeus hit a usage limit: you've hit your session limit" {
		t.Fatalf("stallReason(usage-limit) = %q", usage)
	}
	if RetryableHealth(usage) {
		t.Error("RetryableHealth(usage-limit reason) = true — a nudge cannot fix an exhausted account")
	}
	if !IsUsageLimitReason(usage) {
		t.Error("IsUsageLimitReason(usage-limit reason) = false")
	}

	// the degenerate text: a stall classified usage-limit whose Text came
	// back empty still produces a usage-limit REASON, so the routing symbol
	// must still recognise it — otherwise an empty transcript tail silently
	// routes the cell to the nudge path instead of the rescue.
	empty := stallReason(core.Stall{Kind: "usage-limit"})
	if !IsUsageLimitReason(empty) {
		t.Errorf("IsUsageLimitReason(%q) = false — an empty-text usage-limit stall is still a usage limit", empty)
	}
	if RetryableHealth(empty) {
		t.Errorf("RetryableHealth(%q) = true", empty)
	}

	notUsage := []string{
		"",
		stallReason(core.Stall{Kind: "api-error", Text: "overloaded_error", Transient: true}),
		stallReason(core.Stall{Kind: "api-error", Text: "credit balance is too low", Transient: false}),
		"zeus process is gone — restart the cell (kill the session, then start it again from the board after cleanup)",
		"cell agent is stuck on a startup prompt — k o cell-foo to answer it",
		"a message that merely mentions a usage limit somewhere in the middle",
	}
	for _, r := range notUsage {
		if IsUsageLimitReason(r) {
			t.Errorf("IsUsageLimitReason(%q) = true, want false", r)
		}
	}
}

// --- limitRescueDecide: the ten-gate chain ---

// edgeRotatable is the hit shape that reaches gate 10 (rotate) with mode
// "auto", a live cell and an available target: unresolved, rate_limit, no
// swap yet, no attempts. Every case below is this minus exactly the field
// the gate under test cares about, so a wrong answer names its gate.
func edgeRotatable() LimitHit {
	return LimitHit{Error: "rate_limit", At: edgeNow.Add(-time.Minute).Unix()}
}

func TestEdgeLimitRescueDecideGateChain(t *testing.T) {
	settled := edgeNow.Add(-limitRescueSettle).Unix()                 // exactly at the settle boundary
	unsettled := edgeNow.Add(-limitRescueSettle + time.Second).Unix() // one second short

	cases := []struct {
		name            string
		mutate          func(*LimitHit)
		mode            string
		cellLive        bool
		targetAvailable bool
		want            limitRescueAction
	}{
		// gate 1 — no hit at all
		{
			name:     "gate 1: At==0 skips even with everything else primed",
			mutate:   func(h *LimitHit) { h.At = 0 },
			mode:     "auto",
			cellLive: true, targetAvailable: true, want: rescueSkip,
		},
		// gate 2 — already resolved, above the resume gate
		{
			name:     "gate 2: a handled hit never re-fires, even with a settled swap",
			mutate:   func(h *LimitHit) { h.HandledAt = edgeNow.Unix() - 1; h.SwappedAt = settled },
			mode:     "auto",
			cellLive: true, targetAvailable: true, want: rescueSkip,
		},
		// gate 3 — wrong error type, above the resume gate
		{
			name:     "gate 3: a non-rate_limit hit is skipped",
			mutate:   func(h *LimitHit) { h.Error = "overloaded" },
			mode:     "auto",
			cellLive: true, targetAvailable: true, want: rescueSkip,
		},
		{
			name:     "gate 3: an empty error type is skipped",
			mutate:   func(h *LimitHit) { h.Error = "" },
			mode:     "auto",
			cellLive: true, targetAvailable: true, want: rescueSkip,
		},
		{
			name:     "gate 3 pre-empts gate 5: wrong error type is skipped even after a settled swap",
			mutate:   func(h *LimitHit) { h.Error = "authentication_failed"; h.SwappedAt = settled },
			mode:     "auto",
			cellLive: true, targetAvailable: true, want: rescueSkip,
		},
		// gate 4 — dead cell, above the resume gate
		{
			name:     "gate 4: a dead cell is skipped",
			mutate:   func(h *LimitHit) {},
			mode:     "auto",
			cellLive: false, targetAvailable: true, want: rescueSkip,
		},
		{
			name:     "gate 4 pre-empts gate 5: nothing to resume in a dead cell",
			mutate:   func(h *LimitHit) { h.SwappedAt = settled },
			mode:     "auto",
			cellLive: false, targetAvailable: true, want: rescueSkip,
		},
		// gate 5 — the settle window, both sides of the boundary
		{
			name:     "gate 5 boundary: exactly limitRescueSettle after the swap resumes",
			mutate:   func(h *LimitHit) { h.SwappedAt = settled },
			mode:     "auto",
			cellLive: true, targetAvailable: true, want: rescueResume,
		},
		{
			name:     "gate 5 boundary: one second short of the settle window skips",
			mutate:   func(h *LimitHit) { h.SwappedAt = unsettled },
			mode:     "auto",
			cellLive: true, targetAvailable: true, want: rescueSkip,
		},
		{
			name:     "gate 5: a swap far in the past still resumes",
			mutate:   func(h *LimitHit) { h.SwappedAt = edgeNow.Add(-time.Hour).Unix() },
			mode:     "auto",
			cellLive: true, targetAvailable: true, want: rescueResume,
		},
		// gate 5 ABOVE gate 6 — the stated ordering obligation
		{
			name:     "gate 5 above gate 6: a settled swap resumes even in manual mode",
			mutate:   func(h *LimitHit) { h.SwappedAt = settled },
			mode:     "manual",
			cellLive: true, targetAvailable: true, want: rescueResume,
		},
		{
			name:     "gate 5 above gate 7: a settled swap resumes even at the attempt cap",
			mutate:   func(h *LimitHit) { h.SwappedAt = settled; h.Attempts = limitRescueMaxAttempts },
			mode:     "auto",
			cellLive: true, targetAvailable: true, want: rescueResume,
		},
		{
			name:     "gate 5 above gate 9: a settled swap resumes even with no target left",
			mutate:   func(h *LimitHit) { h.SwappedAt = settled },
			mode:     "auto",
			cellLive: true, targetAvailable: false, want: rescueResume,
		},
		{
			// gate 5's else-branch is SKIP, so an unsettled swap never
			// reaches park-manual: the cell is mid-rescue, not parked
			name:     "gate 5 else-branch pre-empts gate 6: unsettled swap in manual mode skips",
			mutate:   func(h *LimitHit) { h.SwappedAt = unsettled },
			mode:     "manual",
			cellLive: true, targetAvailable: true, want: rescueSkip,
		},
		// gate 6 — manual mode never rotates, whatever else is true
		{
			name:     "gate 6: manual mode parks rather than rotating",
			mutate:   func(h *LimitHit) {},
			mode:     "manual",
			cellLive: true, targetAvailable: true, want: rescueParkManual,
		},
		{
			name:     "gate 6 above gate 7: manual mode parks manual, not no-target, at the cap",
			mutate:   func(h *LimitHit) { h.Attempts = limitRescueMaxAttempts },
			mode:     "manual",
			cellLive: true, targetAvailable: true, want: rescueParkManual,
		},
		{
			name:     "gate 6 above gate 8: manual mode parks manual even inside the backoff",
			mutate:   func(h *LimitHit) { h.Attempts = 1; h.LastAttempt = edgeNow.Unix() },
			mode:     "manual",
			cellLive: true, targetAvailable: true, want: rescueParkManual,
		},
		{
			name:     "gate 6 above gate 9: manual mode parks manual, not no-target",
			mutate:   func(h *LimitHit) {},
			mode:     "manual",
			cellLive: true, targetAvailable: false, want: rescueParkManual,
		},
		// gate 7 — the attempt cap, both sides
		{
			name: "gate 7 boundary: attempts one short of the cap still rotates",
			mutate: func(h *LimitHit) {
				h.Attempts = limitRescueMaxAttempts - 1
				h.LastAttempt = edgeNow.Add(-limitRescueRetryInterval).Unix()
			},
			mode:     "auto",
			cellLive: true, targetAvailable: true, want: rescueRotate,
		},
		{
			name: "gate 7 boundary: attempts exactly at the cap parks",
			mutate: func(h *LimitHit) {
				h.Attempts = limitRescueMaxAttempts
				h.LastAttempt = edgeNow.Add(-limitRescueRetryInterval).Unix()
			},
			mode:     "auto",
			cellLive: true, targetAvailable: true, want: rescueParkNoTarget,
		},
		{
			name:     "gate 7: attempts past the cap parks",
			mutate:   func(h *LimitHit) { h.Attempts = limitRescueMaxAttempts + 5 },
			mode:     "auto",
			cellLive: true, targetAvailable: true, want: rescueParkNoTarget,
		},
		{
			// the cap is terminal, so it must not be masked by the backoff
			// skip below it — a capped hit parks now rather than waiting
			// ten more minutes to park
			name:     "gate 7 above gate 8: a capped hit parks even inside the backoff",
			mutate:   func(h *LimitHit) { h.Attempts = limitRescueMaxAttempts; h.LastAttempt = edgeNow.Unix() },
			mode:     "auto",
			cellLive: true, targetAvailable: true, want: rescueParkNoTarget,
		},
		// gate 8 — the retry backoff, both sides
		{
			name:     "gate 8 boundary: exactly limitRescueRetryInterval since the last attempt rotates",
			mutate:   func(h *LimitHit) { h.Attempts = 1; h.LastAttempt = edgeNow.Add(-limitRescueRetryInterval).Unix() },
			mode:     "auto",
			cellLive: true, targetAvailable: true, want: rescueRotate,
		},
		{
			name: "gate 8 boundary: one second short of the backoff skips",
			mutate: func(h *LimitHit) {
				h.Attempts = 1
				h.LastAttempt = edgeNow.Add(-limitRescueRetryInterval + time.Second).Unix()
			},
			mode:     "auto",
			cellLive: true, targetAvailable: true, want: rescueSkip,
		},
		{
			// the backoff is keyed on Attempts > 0: a first-ever rescue must
			// not be held back by a LastAttempt that no attempt ever wrote
			name:     "gate 8: attempts==0 ignores LastAttempt entirely",
			mutate:   func(h *LimitHit) { h.Attempts = 0; h.LastAttempt = edgeNow.Unix() },
			mode:     "auto",
			cellLive: true, targetAvailable: true, want: rescueRotate,
		},
		{
			// inside the backoff the answer is SKIP (try again later), not
			// park — parking here would burn the hit on a pool that may
			// well have recovered by the next window
			name:     "gate 8 above gate 9: inside the backoff with no target, skip not park",
			mutate:   func(h *LimitHit) { h.Attempts = 1; h.LastAttempt = edgeNow.Unix() },
			mode:     "auto",
			cellLive: true, targetAvailable: false, want: rescueSkip,
		},
		// gate 9 / gate 10
		{
			name:     "gate 9: no target parks",
			mutate:   func(h *LimitHit) {},
			mode:     "auto",
			cellLive: true, targetAvailable: false, want: rescueParkNoTarget,
		},
		{
			name:     "gate 10: everything clear rotates",
			mutate:   func(h *LimitHit) {},
			mode:     "auto",
			cellLive: true, targetAvailable: true, want: rescueRotate,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hit := edgeRotatable()
			c.mutate(&hit)
			got, reason := limitRescueDecide(hit, c.mode, c.cellLive, c.targetAvailable, edgeNow)
			if got != c.want {
				t.Fatalf("decide(%+v, mode=%q, live=%v, target=%v) = %q (%q), want %q",
					hit, c.mode, c.cellLive, c.targetAvailable, got, reason, c.want)
			}
			// every outcome but skip carries a short human reason — it is
			// what the board's blocked detail and the notification render,
			// and a blank one is a park nobody can explain
			if got != rescueSkip && strings.TrimSpace(reason) == "" {
				t.Errorf("outcome %q returned an empty reason", got)
			}
		})
	}
}

// The decision is PURE: no store, no tmux, no clock of its own. Calling it
// twice with the same arguments must give the same answer, and it must not
// mutate the hit it is handed (the pass writes the record back itself, and
// a decision that also wrote would double-count attempts).
func TestEdgeLimitRescueDecideIsPureAndNonMutating(t *testing.T) {
	hit := edgeRotatable()
	before := hit
	a1, r1 := limitRescueDecide(hit, "auto", true, true, edgeNow)
	a2, r2 := limitRescueDecide(hit, "auto", true, true, edgeNow)
	if a1 != a2 || r1 != r2 {
		t.Fatalf("decide is not deterministic: (%q,%q) then (%q,%q)", a1, r1, a2, r2)
	}
	if hit != before {
		t.Fatalf("decide mutated its argument: %+v, was %+v", hit, before)
	}
}

// The five action constants must be distinct strings — they are the keys
// the pass dedupes notifications on ("limit:"+slug+"|"+action), so two
// actions sharing a value would silently suppress one another's
// notification.
func TestEdgeLimitRescueActionsAreDistinct(t *testing.T) {
	seen := map[limitRescueAction]bool{}
	for _, a := range []limitRescueAction{rescueSkip, rescueRotate, rescueResume, rescueParkManual, rescueParkNoTarget} {
		if a == "" {
			t.Errorf("an action constant is the empty string")
		}
		if seen[a] {
			t.Errorf("duplicate action value %q", a)
		}
		seen[a] = true
	}
}

// --- ReadLimitHit / RecordLimitHit: degradation and the dedupe boundary ---

// ReadLimitHit never returns an error: every failure degrades to the zero
// record, which the decision reads as "no hit" and skips on. That is the
// conservative direction — an unreadable record must make the rescue do
// NOTHING, never guess.
func TestEdgeReadLimitHitDegradesToZero(t *testing.T) {
	t.Run("no store wired", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		if got := ReadLimitHit(edgeJob("no-store-here", 1)); got != (LimitHit{}) {
			t.Fatalf("ReadLimitHit with no store = %+v, want the zero record", got)
		}
	})

	t.Run("missing key", func(t *testing.T) {
		useTestStore(t)
		if got := ReadLimitHit(edgeJob("never-recorded", 2)); got != (LimitHit{}) {
			t.Fatalf("ReadLimitHit(missing) = %+v, want the zero record", got)
		}
	})

	t.Run("unparseable stored JSON", func(t *testing.T) {
		st := useTestStore(t)
		j := edgeJob("corrupt", 3)
		for _, bad := range []string{"{not json", "", "[1,2,3]", `{"at":"not-a-number"}`} {
			if err := st.SetKV(limitHitKey(j.ID), bad); err != nil {
				t.Fatal(err)
			}
			if got := ReadLimitHit(j); got != (LimitHit{}) {
				t.Fatalf("ReadLimitHit over stored %q = %+v, want the zero record", bad, got)
			}
		}
	})

	// Amendment 5 moved the key onto *Job, which adds one degradation the
	// slug signature could not have: a nil job. It is the same conservative
	// direction as the rest — no job means no hit, never a guess.
	t.Run("nil job", func(t *testing.T) {
		useTestStore(t)
		if got := ReadLimitHit(nil); got != (LimitHit{}) {
			t.Fatalf("ReadLimitHit(nil) = %+v, want the zero record", got)
		}
	})
}

// With no active store the record has nowhere to go: RecordLimitHit returns
// an error and writes NOTHING. The route is what turns that into a status;
// silently succeeding would tell the hook its limit was recorded when the
// rescue will never see it.
func TestEdgeRecordLimitHitWithoutStoreErrs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	j := edgeJob("no-store", 4)
	got, err := RecordLimitHit(j, "rate_limit", "details", "s", "a", edgeNow)
	if err == nil {
		t.Fatalf("RecordLimitHit with no store = (%+v, nil), want an error", got)
	}
	if ReadLimitHit(j) != (LimitHit{}) {
		t.Fatal("RecordLimitHit wrote a record despite having no store")
	}

	// and the other half of Amendment 5's new signature: no job is the same
	// error, for the same reason — there is nowhere to put the record, and
	// reporting success would tell the hook a limit was recorded that nothing
	// will ever act on
	useTestStore(t)
	if got, err := RecordLimitHit(nil, "rate_limit", "", "", "", edgeNow); err == nil {
		t.Fatalf("RecordLimitHit(nil job) = (%+v, nil), want an error", got)
	}
}

// The fold's boundary is RESOLUTION, not time. The fold is what makes the
// teammate herd — every claude in the cell firing its own hook for one limit
// — count as ONE hit, and it also absorbs the transcript trigger's
// re-deliveries of a standing stall, which arrive every ten minutes for as
// long as the cell is down: a time-bounded fold reset the attempt counter on
// each of those, so the exhausted-attempts state was unreachable on exactly
// the long-dead cells the reset re-arm exists for. Only resolving the hit
// closes it to further folds.
func TestEdgeRecordLimitHitFoldIgnoresElapsedTime(t *testing.T) {
	st := useTestStore(t)
	j := edgeStoreJob(t, st, "edge-fold-time")

	first, err := RecordLimitHit(j, "rate_limit", "one", "sess-a", "", edgeNow)
	if err != nil {
		t.Fatal(err)
	}
	if first.At != edgeNow.Unix() || first.Deliveries != 1 || first.HandledAt != 0 {
		t.Fatalf("first delivery = %+v, want At=now Deliveries=1 HandledAt=0", first)
	}

	// seconds later, from a DIFFERENT teammate: folds, and the fold keeps
	// the original At/SessionID/AgentID — the hit's identity is the FIRST
	// delivery, not the loudest one
	folded, err := RecordLimitHit(j, "rate_limit", "two", "sess-b", "agent-2", edgeNow.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if folded.Deliveries != 2 {
		t.Errorf("folded Deliveries = %d, want 2", folded.Deliveries)
	}
	if folded.At != first.At {
		t.Errorf("folded At = %d, want the first delivery's %d", folded.At, first.At)
	}
	if folded.SessionID != "sess-a" || folded.AgentID != "" {
		t.Errorf("folded identity = (%q,%q), want the first delivery's (%q,%q)", folded.SessionID, folded.AgentID, "sess-a", "")
	}

	// ten minutes on (the transcript trigger's re-delivery cadence) and then
	// nineteen hours on: still the same unresolved hit, still folds
	for i, late := range []time.Time{edgeNow.Add(10*time.Minute + time.Second), edgeNow.Add(19 * time.Hour)} {
		got, err := RecordLimitHit(j, "rate_limit", "again", "sess-late", "", late)
		if err != nil {
			t.Fatal(err)
		}
		if got.At != first.At || got.Deliveries != 3+i {
			t.Fatalf("late delivery %d = %+v, want a fold onto the original hit (At=%d, Deliveries=%d)", i, got, first.At, 3+i)
		}
	}

	// resolution closes the hit: the next delivery is a fresh record
	resolved := ReadLimitHit(j)
	resolved.HandledAt = edgeNow.Add(20 * time.Hour).Unix()
	resolved.Outcome = "parked-no-target"
	raw, err := json.Marshal(resolved)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetKV(limitHitKey(j.ID), string(raw)); err != nil {
		t.Fatal(err)
	}
	fresh, err := RecordLimitHit(j, "rate_limit", "three", "sess-c", "", edgeNow.Add(21*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if fresh.At != edgeNow.Add(21*time.Hour).Unix() || fresh.Deliveries != 1 {
		t.Fatalf("delivery after resolution = %+v, want a fresh record (At=now, Deliveries=1)", fresh)
	}
	if fresh.SessionID != "sess-c" {
		t.Errorf("fresh SessionID = %q, want the new delivery's", fresh.SessionID)
	}
	if fresh.HandledAt != 0 || fresh.Outcome != "" || fresh.Attempts != 0 || fresh.LastAttempt != 0 {
		t.Errorf("fresh record kept stale rescue state: %+v", fresh)
	}
}

// The fold has three preconditions, and each one failing on its own must
// produce a FRESH record rather than a fold: a different error type, an
// already-handled record, and (covered above) a delivery past the window.
func TestEdgeRecordLimitHitFoldPreconditions(t *testing.T) {
	t.Run("a different error type never folds", func(t *testing.T) {
		st := useTestStore(t)
		j := edgeStoreJob(t, st, "edge-fold-err")
		if _, err := RecordLimitHit(j, "rate_limit", "", "s1", "", edgeNow); err != nil {
			t.Fatal(err)
		}
		got, err := RecordLimitHit(j, "overloaded", "", "s2", "", edgeNow.Add(time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if got.Deliveries != 1 || got.Error != "overloaded" || got.At != edgeNow.Add(time.Second).Unix() {
			t.Fatalf("a different error type folded into the old hit: %+v", got)
		}
	})

	t.Run("an already-handled record never folds", func(t *testing.T) {
		st := useTestStore(t)
		j := edgeStoreJob(t, st, "edge-fold-handled")
		// plant a resolved record directly: only the rescue pass writes
		// HandledAt, and this test is about what RecordLimitHit does when
		// it finds one
		handled := LimitHit{
			Error: "rate_limit", At: edgeNow.Unix(), Deliveries: 3,
			HandledAt: edgeNow.Unix(), Outcome: "rotated", Attempts: 1,
			SwappedAt: edgeNow.Unix(), SwappedTo: "old@example.com",
		}
		raw, err := json.Marshal(handled)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.SetKV(limitHitKey(j.ID), string(raw)); err != nil {
			t.Fatal(err)
		}

		got, err := RecordLimitHit(j, "rate_limit", "", "s2", "", edgeNow.Add(time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if got.Deliveries != 1 || got.HandledAt != 0 || got.Outcome != "" || got.Attempts != 0 {
			t.Fatalf("a resolved hit was folded into instead of replaced: %+v", got)
		}
		if got.At != edgeNow.Add(time.Second).Unix() {
			t.Errorf("fresh At = %d, want the new delivery's %d", got.At, edgeNow.Add(time.Second).Unix())
		}
	})

	t.Run("a missing key is the fresh-record case, not an error", func(t *testing.T) {
		st := useTestStore(t)
		j := edgeStoreJob(t, st, "edge-fold-missing")
		got, err := RecordLimitHit(j, "rate_limit", "", "s", "", edgeNow)
		if err != nil {
			t.Fatalf("a first-ever delivery errored: %v", err)
		}
		if got.Deliveries != 1 || got.At != edgeNow.Unix() {
			t.Fatalf("first-ever delivery = %+v", got)
		}
	})
}

// A record written by RecordLimitHit must come back out of ReadLimitHit
// unchanged, INCLUDING slice B's two swap fields when a fold runs over a
// record that already carries them — the round trip is the only thing
// keeping the swap watermark alive across a herd of late deliveries.
func TestEdgeRecordLimitHitPersistsThroughRead(t *testing.T) {
	st := useTestStore(t)
	j := edgeStoreJob(t, st, "edge-persist")

	got, err := RecordLimitHit(j, "rate_limit", "some details", "sess", "agent", edgeNow)
	if err != nil {
		t.Fatal(err)
	}
	if read := ReadLimitHit(j); read != got {
		t.Fatalf("ReadLimitHit = %+v, want the record RecordLimitHit returned %+v", read, got)
	}
}

// --- rescueLimitedCell: refusal paths only ---

// The lock-held refusal was tested here and retired with Q2: a rebind writes
// one job row and displaces no machine-wide credential, so it takes the
// binder's in-process mutex rather than the cross-process rotation flock, and
// a mutex blocks instead of answering "busy". Nothing races `k account
// <slug>` any more because nothing on this path touches what that command
// changes.

// With no usage snapshot at all (a scratch HOME, no store), the pool has no
// rows, so the binder has nothing to pick: the outcome is "no-target" and
// NOTHING is rebound. This is also the only way to exercise
// rescueLimitedCell's target-selection step without a real pool on disk.
func TestEdgeRescueLimitedCellNoTargetNeverSwaps(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	j := &Job{Slug: "edge-no-target", ID: 2, Repo: "monolith", State: "working"}
	email, outcome, err := rescueLimitedCell(j, edgeNow)
	if err != nil {
		t.Fatalf("an empty pool returned an error %v — an empty pool is an outcome, not a failure", err)
	}
	if outcome != "no-target" {
		t.Fatalf("outcome = %q, want %q", outcome, "no-target")
	}
	if email != "" {
		t.Errorf("toEmail = %q, want empty — no target means no swap", email)
	}
}

// --- cellAgentPanes / resumeCellAgents: the empty and excluded sets ---

// edgePeer builds the minimum peer shape the enumeration reads: a
// foreground agent pane, the shape DiscoverAll produces for a pane's own
// claude (Role "main", the value discover.go stamps).
func edgeAgentPeer(session, pane, agent string, depth int) *core.Peer {
	return &core.Peer{TmuxSession: session, Pane: pane, Agent: agent, Depth: depth, Role: "main"}
}

func edgePanesOf(m map[string]*core.Peer) map[string]bool {
	out := map[string]bool{}
	for _, p := range m {
		if p != nil {
			out[p.Pane] = true
		}
	}
	return out
}

// cellAgentPanes is Stop's enumeration, and its exclusions are what keep
// Stop — and anything else that acts on "everything working on this job" —
// inside the cell it was aimed at. Each exclusion has its own stake, and
// they are not the same size: another job's SESSION means acting on a pane
// kraken was never asked about, which under a kill-based verb is another
// job's process destroyed; a NESTED agent (depth > 0) has no pane of its
// own, so anything addressed to it lands on the lead sharing that pane,
// send-keys and respawn alike; and a shell pane holds no agent at all.
//
// (The limit rescue no longer reads this set — it resumes zeus alone, since
// a teammate has no session id to resume — but the enumeration and its
// exclusions still belong to InterruptCell, so they are still pinned here.)
func TestEdgeCellAgentPanesExclusions(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	j := &Job{Slug: "edge-panes", ID: 7, Repo: "monolith"}
	sess := CellSession(j)

	mine := edgeAgentPeer(sess, "%1", "claude", 0)
	nested := edgeAgentPeer(sess, "%2", "claude", 1)
	otherCell := edgeAgentPeer("cell-someone-else-3", "%3", "claude", 0)
	loose := edgeAgentPeer("carl-scratch", "%4", "claude", 0)
	shell := &core.Peer{TmuxSession: sess, Pane: "%5", Role: "shell"}

	got := edgePanesOf(cellAgentPanes(j, []*core.Peer{mine, nested, otherCell, loose, shell}))

	if !got["%1"] {
		t.Error("the cell's own depth-0 claude pane was excluded")
	}
	for pane, why := range map[string]string{
		"%2": "a nested (depth > 0) agent — send-keys cannot reach it",
		"%3": "another cell's session",
		"%4": "an unrelated tmux session",
		"%5": "a shell pane with no agent in it",
	} {
		if got[pane] {
			t.Errorf("pane %s was included: %s", pane, why)
		}
	}
}

func TestEdgeCellAgentPanesEmptyInputs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	j := &Job{Slug: "edge-panes-empty", ID: 8, Repo: "monolith"}

	if got := cellAgentPanes(j, nil); len(got) != 0 {
		t.Fatalf("cellAgentPanes(nil peers) = %v, want empty", got)
	}
	if got := cellAgentPanes(j, []*core.Peer{}); len(got) != 0 {
		t.Fatalf("cellAgentPanes(no peers) = %v, want empty", got)
	}
}

// No zeus pane is the ONE error resumeCellAgents returns: there is nobody
// to resume, and reporting that as success would let the pass mark the hit
// handled with the whole cell still sitting on a dead account. (A respawn
// that FAILS is the opposite case — counted out, never fatal — see
// TestEdgeResumeCellAgentsRefusedRespawnCountsOutToZero.)
func TestEdgeResumeCellAgentsNoZeusErrs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	j := &Job{Slug: "edge-resume-empty", ID: 9, Repo: "monolith"}

	t.Run("no peers at all", func(t *testing.T) {
		n, err := resumeCellAgents(j, nil, limitRescueResumePrompt)
		if err == nil {
			t.Fatalf("resumeCellAgents(nil) = (%d, nil), want an error when there is no zeus", n)
		}
		if n != 0 {
			t.Errorf("resumed = %d, want 0", n)
		}
	})

	t.Run("peers that all belong to other sessions", func(t *testing.T) {
		peers := []*core.Peer{
			edgeAgentPeer("cell-other-1", "%1", "claude", 0),
			edgeAgentPeer("carl-scratch", "%2", "claude", 0),
		}
		n, err := resumeCellAgents(j, peers, limitRescueResumePrompt)
		if err == nil {
			t.Fatalf("resumeCellAgents(other sessions only) = (%d, nil), want an error", n)
		}
		if n != 0 {
			t.Errorf("resumed = %d, want 0", n)
		}
	})
}

// --- the constants themselves ---

// The windows are load-bearing numbers rather than taste: the settle is
// enforced ACROSS daemon ticks (so it must be larger than nothing and
// small enough that one tick's cadence can carry it), the retry interval
// matches the health pass's repoke cadence, and the reset pair bounds the
// re-arm in limitrescue_reset_test.go's cases. Pinning them here is what
// makes those boundary cases mean what their names say.
func TestEdgeLimitConstants(t *testing.T) {
	if limitResetSettle != 2*time.Minute {
		t.Errorf("limitResetSettle = %v, want 2m", limitResetSettle)
	}
	if limitResetMaxWait != 6*time.Hour {
		t.Errorf("limitResetMaxWait = %v, want 6h", limitResetMaxWait)
	}
	if limitRescueSettle != 15*time.Second {
		t.Errorf("limitRescueSettle = %v, want 15s", limitRescueSettle)
	}
	if limitRescueMaxAttempts != 3 {
		t.Errorf("limitRescueMaxAttempts = %d, want 3", limitRescueMaxAttempts)
	}
	if limitRescueRetryInterval != 10*time.Minute {
		t.Errorf("limitRescueRetryInterval = %v, want 10m", limitRescueRetryInterval)
	}
}

// --- LimitHook: the fail-open paths ---

// The hook runs as a child of the cell's claude, inside the very turn that
// just failed. Every outcome — an unparseable payload, an unreachable
// serve, a non-200 — must be ONE stderr line and a normal return: a hook
// that exits non-zero fails the turn it fires on, which would turn a
// recoverable usage limit into a broken cell.
func TestEdgeLimitHookNeverFailsTheTurn(t *testing.T) {
	cases := []struct {
		name      string
		payload   string
		status    int
		reachable bool
		wantPosts int
	}{
		{
			name:      "control: a well-formed payload is posted exactly once",
			payload:   `{"hook_event_name":"StopFailure","error":"rate_limit","session_id":"s","agent_id":"a","error_details":"resets 4pm"}`,
			status:    http.StatusOK,
			reachable: true,
			wantPosts: 1,
		},
		{
			// nothing to report: a payload the parser refuses must not
			// become a POST claiming an empty error type
			name:      "an unparseable payload is never posted",
			payload:   `{not json`,
			status:    http.StatusOK,
			reachable: true,
			wantPosts: 0,
		},
		{
			name:      "a payload with an empty error type is never posted",
			payload:   `{"hook_event_name":"StopFailure","error":"","session_id":"s"}`,
			status:    http.StatusOK,
			reachable: true,
			wantPosts: 0,
		},
		{
			name:      "empty stdin is never posted",
			payload:   ``,
			status:    http.StatusOK,
			reachable: true,
			wantPosts: 0,
		},
		{
			name:      "a non-200 answer is survived",
			payload:   `{"hook_event_name":"StopFailure","error":"rate_limit"}`,
			status:    http.StatusInternalServerError,
			reachable: true,
			wantPosts: 1,
		},
		{
			name:      "a 400 answer is survived",
			payload:   `{"hook_event_name":"StopFailure","error":"rate_limit"}`,
			status:    http.StatusBadRequest,
			reachable: true,
			wantPosts: 1,
		},
		{
			name:      "an unreachable serve is survived",
			payload:   `{"hook_event_name":"StopFailure","error":"rate_limit"}`,
			reachable: false,
			wantPosts: 0,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var posts int
			var gotBody []byte
			if c.reachable {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/api/limit-hit" || r.Method != http.MethodPost {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					posts++
					gotBody, _ = io.ReadAll(r.Body)
					w.WriteHeader(c.status)
				}))
				defer srv.Close()
				t.Setenv("KRAKEN_SERVE_ADDR", strings.TrimPrefix(srv.URL, "http://"))
			} else {
				ln, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				addr := ln.Addr().String()
				ln.Close() // nothing listens here: connections refuse at once
				t.Setenv("KRAKEN_SERVE_ADDR", addr)
			}

			// a normal return is the assertion: an os.Exit anywhere in here
			// takes the whole test binary down with it
			stderr := withStderrCapture(t, func() {
				withStdin(t, c.payload, func() { LimitHook("edge-hook-job") })
			})

			if posts != c.wantPosts {
				t.Fatalf("POSTs = %d, want %d", posts, c.wantPosts)
			}
			// a FAILED outcome must say so, on exactly one line — the
			// contract's "single stderr line" is what makes a lost limit
			// visible in the pane without failing the turn. A clean success
			// is left unconstrained: the contract does not say whether it
			// reports one.
			failed := !c.reachable || c.status != http.StatusOK || c.wantPosts == 0
			if failed {
				if strings.TrimSpace(stderr) == "" {
					t.Errorf("a failed delivery reported nothing on stderr")
				}
				if lines := strings.Split(strings.TrimSpace(stderr), "\n"); len(lines) != 1 {
					t.Errorf("stderr carries %d lines, want exactly 1:\n%s", len(lines), stderr)
				}
			}
			if c.wantPosts == 1 && c.status == http.StatusOK {
				var req map[string]string
				if err := json.Unmarshal(gotBody, &req); err != nil {
					t.Fatalf("posted body is not a flat JSON object: %v (%s)", err, gotBody)
				}
				if req["slug"] != "edge-hook-job" || req["error"] != "rate_limit" {
					t.Fatalf("posted body = %+v", req)
				}
				for _, k := range []string{"slug", "error", "details", "session_id", "agent_id"} {
					if _, ok := req[k]; !ok {
						t.Errorf("posted body has no %q key: %s", k, gotBody)
					}
				}
			}
		})
	}
}

// --- limitRescueApply (Amendment 4): the write-back's boundaries ---
//
// Amendment 4 exists because the write-back table had no reader and so no
// test anywhere. What follows is the EDGE half of it: the attempt-cap
// boundary, the arms that must NOT touch a field, and the units. The normal
// arms are the sibling's.

// edgeApplyHit is a mid-rescue record: unresolved, rate_limit, already
// delivered several times, with no swap yet — so each arm below has
// something to advance or something to leave alone.
func edgeApplyHit() LimitHit {
	return LimitHit{
		Error: "rate_limit", Details: "you've hit your session limit",
		At: edgeNow.Add(-time.Minute).Unix(), SessionID: "sess", Deliveries: 3,
	}
}

// The cap boundary, both sides, and the arithmetic that decides which side a
// record lands on. `rescueParkNoTarget` increments Attempts and then tests
// the INCREMENTED value, so the THIRD failed park is the one that gives up:
// a park below the cap must leave HandledAt at zero, because that is what
// lets a pool account recovering later still rescue the cell. Testing before
// the increment would record a fourth attempt that never happened.
func TestEdgeLimitRescueApplyParkNoTargetCapBoundary(t *testing.T) {
	cases := []struct {
		name         string
		startAttempt int
		wantAttempts int
		wantResolved bool
	}{
		{"first park stays open", 0, 1, false},
		{"second park stays open", 1, 2, false},
		{"third park is the one that gives up", 2, limitRescueMaxAttempts, true},
		{"a record already at the cap resolves too", limitRescueMaxAttempts, limitRescueMaxAttempts + 1, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hit := edgeApplyHit()
			hit.Attempts = c.startAttempt
			hit.LastAttempt = edgeNow.Add(-time.Hour).Unix()

			got := limitRescueApply(hit, rescueParkNoTarget, "no-target", "", edgeNow)

			if got.Attempts != c.wantAttempts {
				t.Fatalf("Attempts = %d, want %d (started at %d)", got.Attempts, c.wantAttempts, c.startAttempt)
			}
			if got.LastAttempt != edgeNow.Unix() {
				t.Errorf("LastAttempt = %d, want %d — every park stamps its attempt", got.LastAttempt, edgeNow.Unix())
			}
			if c.wantResolved {
				if got.HandledAt != edgeNow.Unix() {
					t.Errorf("HandledAt = %d, want %d — the cap is reached, the hit gives up", got.HandledAt, edgeNow.Unix())
				}
				if got.Outcome != "parked-no-target" {
					t.Errorf("Outcome = %q, want %q", got.Outcome, "parked-no-target")
				}
			} else {
				if got.HandledAt != 0 {
					t.Errorf("HandledAt = %d, want 0 — a park below the cap must stay open so a pool account recovering later still rescues the cell", got.HandledAt)
				}
				if got.Outcome != "" {
					t.Errorf("Outcome = %q, want empty — the hit is not resolved yet", got.Outcome)
				}
			}
			// a park never swaps, so the watermark stays untouched
			if got.SwappedAt != 0 || got.SwappedTo != "" {
				t.Errorf("park wrote a swap watermark: SwappedAt=%d SwappedTo=%q", got.SwappedAt, got.SwappedTo)
			}
		})
	}
}

// The increment-then-test arithmetic, stated as the number it produces: the
// record that gives up reads exactly limitRescueMaxAttempts, never one more.
// A test-before-increment implementation resolves one park LATE and stores a
// fourth attempt that was never made — which is the same green suite, one
// wasted rescue window, and a counter that lies.
func TestEdgeLimitRescueApplyParkNoTargetResolvesAtExactlyTheCap(t *testing.T) {
	hit := edgeApplyHit()
	hit.Attempts = limitRescueMaxAttempts - 1

	got := limitRescueApply(hit, rescueParkNoTarget, "no-target", "", edgeNow)

	if got.HandledAt == 0 {
		t.Fatalf("a park from Attempts=%d left the hit open — it must give up on the %dth", limitRescueMaxAttempts-1, limitRescueMaxAttempts)
	}
	if got.Attempts != limitRescueMaxAttempts {
		t.Fatalf("the resolving park recorded Attempts=%d, want exactly %d — a higher number is an attempt that never happened", got.Attempts, limitRescueMaxAttempts)
	}

	// and one park earlier must NOT have resolved, or the cap moved down
	earlier := edgeApplyHit()
	earlier.Attempts = limitRescueMaxAttempts - 2
	if got := limitRescueApply(earlier, rescueParkNoTarget, "no-target", "", edgeNow); got.HandledAt != 0 {
		t.Fatalf("a park from Attempts=%d resolved: HandledAt=%d — the cap fires early", limitRescueMaxAttempts-2, got.HandledAt)
	}
}

// A rotate that did NOT swap advances the counters and writes no watermark.
// The refusal outcomes all share one arm, and the sharp edge in it is
// `toEmail`: a rescueLimitedCell that returns an email alongside a failure
// must not have that email recorded as a swap that happened.
func TestEdgeLimitRescueApplyRotateWithoutASwap(t *testing.T) {
	for _, outcome := range []string{"no-target", "bind-failed", ""} {
		t.Run("outcome "+outcome, func(t *testing.T) {
			hit := edgeApplyHit()
			hit.Attempts = 1
			hit.LastAttempt = edgeNow.Add(-limitRescueRetryInterval).Unix()

			// a non-empty email alongside a non-"rebound" outcome must be ignored
			got := limitRescueApply(hit, rescueRotate, outcome, "someone@example.com", edgeNow)

			if got.Attempts != 2 {
				t.Errorf("Attempts = %d, want 2", got.Attempts)
			}
			if got.LastAttempt != edgeNow.Unix() {
				t.Errorf("LastAttempt = %d, want %d", got.LastAttempt, edgeNow.Unix())
			}
			if got.SwappedAt != 0 {
				t.Errorf("SwappedAt = %d, want 0 — no swap happened", got.SwappedAt)
			}
			if got.SwappedTo != "" {
				t.Errorf("SwappedTo = %q, want empty — the email of a swap that failed is not a swap", got.SwappedTo)
			}
			if got.HandledAt != 0 || got.Outcome != "" {
				t.Errorf("a failed rotate resolved the hit: HandledAt=%d Outcome=%q — the next tick must retry", got.HandledAt, got.Outcome)
			}
		})
	}
}

// A rotate that failed must not clobber a watermark that is already there.
// The gate chain makes this unreachable in normal flow (gate 5 would resume
// first), which is exactly why it is worth pinning on the pure function: a
// clobber here would send an already-swapped cell around for a second swap.
func TestEdgeLimitRescueApplyFailedRotateKeepsAnExistingWatermark(t *testing.T) {
	hit := edgeApplyHit()
	hit.SwappedAt = edgeNow.Add(-time.Minute).Unix()
	hit.SwappedTo = "already@example.com"

	got := limitRescueApply(hit, rescueRotate, "swap-failed", "", edgeNow)

	if got.SwappedAt != hit.SwappedAt || got.SwappedTo != hit.SwappedTo {
		t.Fatalf("watermark = (%d, %q), want the original (%d, %q)", got.SwappedAt, got.SwappedTo, hit.SwappedAt, hit.SwappedTo)
	}
}

// park-manual "touches no counter" — carl pinned the account deliberately,
// so there is nothing to retry and nothing to count. The negative is the
// whole obligation: an incremented Attempts here would burn a rescue budget
// against a decision that was never a retry.
func TestEdgeLimitRescueApplyParkManualTouchesNoCounter(t *testing.T) {
	hit := edgeApplyHit()
	hit.Attempts = 2
	hit.LastAttempt = edgeNow.Add(-time.Hour).Unix()
	hit.SwappedAt = edgeNow.Add(-time.Hour).Unix()
	hit.SwappedTo = "earlier@example.com"

	got := limitRescueApply(hit, rescueParkManual, "manual", "", edgeNow)

	if got.HandledAt != edgeNow.Unix() || got.Outcome != "parked-manual" {
		t.Fatalf("= (HandledAt %d, Outcome %q), want (%d, %q)", got.HandledAt, got.Outcome, edgeNow.Unix(), "parked-manual")
	}
	if got.Attempts != hit.Attempts {
		t.Errorf("Attempts = %d, want %d unchanged", got.Attempts, hit.Attempts)
	}
	if got.LastAttempt != hit.LastAttempt {
		t.Errorf("LastAttempt = %d, want %d unchanged", got.LastAttempt, hit.LastAttempt)
	}
	if got.SwappedAt != hit.SwappedAt || got.SwappedTo != hit.SwappedTo {
		t.Errorf("watermark = (%d, %q), want it unchanged", got.SwappedAt, got.SwappedTo)
	}
}

// The resume arm resolves the hit unconditionally — no counter of its own is
// consulted, so a record sitting at the attempt cap still resolves rather
// than falling through to a park. The account is already swapped; re-running
// the swap would be wrong.
func TestEdgeLimitRescueApplyResumeResolvesRegardlessOfCounters(t *testing.T) {
	hit := edgeApplyHit()
	hit.Attempts = limitRescueMaxAttempts
	hit.LastAttempt = edgeNow.Add(-time.Second).Unix()
	hit.SwappedAt = edgeNow.Add(-limitRescueSettle).Unix()
	hit.SwappedTo = "new@example.com"

	got := limitRescueApply(hit, rescueResume, "", "", edgeNow)

	if got.HandledAt != edgeNow.Unix() {
		t.Fatalf("HandledAt = %d, want %d — a resume resolves the hit", got.HandledAt, edgeNow.Unix())
	}
	if got.Outcome != "rotated" {
		t.Fatalf("Outcome = %q, want %q", got.Outcome, "rotated")
	}
	// the watermark is the record of what actually happened to this cell
	if got.SwappedAt != hit.SwappedAt || got.SwappedTo != hit.SwappedTo {
		t.Errorf("the resume overwrote the swap watermark: (%d, %q), want (%d, %q)", got.SwappedAt, got.SwappedTo, hit.SwappedAt, hit.SwappedTo)
	}
}

// Amendment 1, on the write side: every timestamp limitRescueApply stamps is
// unix SECONDS. Millis would satisfy limitRescueSettle on every tick with the
// build still green, silently deleting the settle window the whole two-phase
// design exists for.
func TestEdgeLimitRescueApplyStampsUnixSeconds(t *testing.T) {
	cases := []struct {
		action  limitRescueAction
		outcome string
		read    func(LimitHit) []int64
	}{
		{rescueRotate, "rebound", func(h LimitHit) []int64 { return []int64{h.SwappedAt, h.LastAttempt} }},
		{rescueRotate, "no-target", func(h LimitHit) []int64 { return []int64{h.LastAttempt} }},
		{rescueResume, "", func(h LimitHit) []int64 { return []int64{h.HandledAt} }},
		{rescueParkManual, "manual", func(h LimitHit) []int64 { return []int64{h.HandledAt} }},
		{rescueParkNoTarget, "no-target", func(h LimitHit) []int64 { return []int64{h.LastAttempt} }},
	}
	for _, c := range cases {
		t.Run(string(c.action)+"/"+c.outcome, func(t *testing.T) {
			got := limitRescueApply(edgeApplyHit(), c.action, c.outcome, "to@example.com", edgeNow)
			for i, v := range c.read(got) {
				switch {
				case v == edgeNow.Unix():
					// the obligation
				case v == edgeNow.UnixMilli():
					// the specific way it goes wrong, named so the failure
					// reads as a unit error rather than an arithmetic one:
					// millis satisfy limitRescueSettle on every tick, which
					// deletes the settle window with the build still green
					t.Errorf("stamp %d = %d — that is now.UnixMilli(); every timestamp on this record is unix SECONDS", i, v)
				default:
					t.Errorf("stamp %d = %d, want %d (now.Unix())", i, v, edgeNow.Unix())
				}
			}
		})
	}
}

// Pure means pure: same inputs, same output, and the record it was handed is
// not modified in place. `limitRescue` owns the store write, so an apply that
// also mutated would double-count on any retry of that write.
func TestEdgeLimitRescueApplyIsPureAndNonMutating(t *testing.T) {
	for _, action := range []limitRescueAction{rescueSkip, rescueRotate, rescueResume, rescueParkManual, rescueParkNoTarget} {
		t.Run(string(action), func(t *testing.T) {
			hit := edgeApplyHit()
			before := hit
			first := limitRescueApply(hit, action, "rebound", "to@example.com", edgeNow)
			second := limitRescueApply(hit, action, "rebound", "to@example.com", edgeNow)
			if first != second {
				t.Fatalf("not deterministic: %+v then %+v", first, second)
			}
			if hit != before {
				t.Fatalf("mutated its argument: %+v, was %+v", hit, before)
			}
		})
	}
}

// --- Amendment 2: what a fold preserves ---

// A fold changes Deliveries and NOTHING else, explicitly including the swap
// watermark. The consequence is the whole reason the amendment exists: a
// late teammate delivery landing inside the dedupe window after a swap must
// not send an already-swapped cell around for a SECOND swap instead of the
// resume it was owed.
func TestEdgeRecordLimitHitFoldPreservesTheSwapWatermark(t *testing.T) {
	st := useTestStore(t)
	j := edgeStoreJob(t, st, "edge-fold-watermark")

	// a mid-rescue record: swapped, unresolved, waiting for its resume
	stored := LimitHit{
		Error: "rate_limit", Details: "the first delivery's message",
		At: edgeNow.Unix(), SessionID: "sess-a", AgentID: "agent-a", Deliveries: 2,
		Attempts: 1, LastAttempt: edgeNow.Unix(),
		SwappedAt: edgeNow.Unix(), SwappedTo: "rescued@example.com",
	}
	raw, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetKV(limitHitKey(j.ID), string(raw)); err != nil {
		t.Fatal(err)
	}

	// a late teammate hook fires a minute on, carrying different
	// details, a different session and a different agent
	late := edgeNow.Add(time.Minute)
	got, err := RecordLimitHit(j, "rate_limit", "a later teammate's message", "sess-b", "agent-b", late)
	if err != nil {
		t.Fatal(err)
	}

	want := stored
	want.Deliveries = stored.Deliveries + 1
	if got != want {
		t.Fatalf("fold changed more than Deliveries:\n got  %+v\n want %+v", got, want)
	}
	if got.SwappedAt != stored.SwappedAt || got.SwappedTo != stored.SwappedTo {
		t.Fatalf("the fold cleared the swap watermark: (%d, %q)", got.SwappedAt, got.SwappedTo)
	}

	// and the consequence, end to end: the folded record still decides to
	// RESUME once the settle window has passed — not to swap a second time
	when := time.Unix(got.SwappedAt, 0).Add(limitRescueSettle)
	if act, reason := limitRescueDecide(got, "auto", true, true, when); act != rescueResume {
		t.Fatalf("after the fold the decision is %q (%q), want %q — a second swap is exactly what Amendment 2 forbids", act, reason, rescueResume)
	}
}

// edgeStoreJob creates a job row and returns the *Job the record's readers
// key off. It has to be a real row rather than a hand-made struct: Amendment
// 5 keys the record on the job's IMMUTABLE id, and only the store mints one.
func edgeStoreJob(t *testing.T, st *store.Store, slug string) *Job {
	t.Helper()
	row := mustCreateJob(t, st, store.Job{Slug: slug, Repo: "monolith"})
	return &Job{Slug: row.Slug, ID: row.ID}
}

// --- Amendment 5: the record cannot be reached across a slug's generations ---

// The finding this amendment came from, as a test: two jobs sharing a slug
// (the archived generation and the new one that took its freed name) must
// not see each other's record. The dangerous shape is the one that was
// measured — an UNRESOLVED hit, which gate 4 leaves open forever once its
// cell is torn down — because the new cell's first tick would read it and
// swap the machine's login on evidence from a job that no longer exists.
func TestEdgeLimitHitIsInvisibleAcrossSlugGenerations(t *testing.T) {
	st := useTestStore(t)
	old := edgeStoreJob(t, st, "recycled-slug")

	if _, err := RecordLimitHit(old, "rate_limit", "the dead generation's limit", "sess-old", "", edgeNow); err != nil {
		t.Fatal(err)
	}
	if ReadLimitHit(old).At == 0 {
		t.Fatal("the first generation's record did not land")
	}

	// the new generation: same slug, a fresh id — which is all archiving and
	// re-creating a job amounts to as far as this record is concerned
	fresh := &Job{Slug: old.Slug, ID: old.ID + 1}
	if got := ReadLimitHit(fresh); got != (LimitHit{}) {
		t.Fatalf("a new job reusing slug %q reads the archived generation's hit: %+v", old.Slug, got)
	}
	// and the decision it would have driven, spelled out: an unresolved
	// rate_limit hit on a live cell with a target available rotates
	if act, _ := limitRescueDecide(ReadLimitHit(old), "auto", true, true, edgeNow.Add(time.Hour)); act != rescueRotate {
		t.Fatalf("sanity: the stale record decides %q, not %q — this test no longer demonstrates the hazard", act, rescueRotate)
	}
	if act, _ := limitRescueDecide(ReadLimitHit(fresh), "auto", true, true, edgeNow.Add(time.Hour)); act != rescueSkip {
		t.Fatalf("the new generation decides %q — it is acting on the dead generation's hit", act)
	}
}

// --- Amendment 10: mergeLimitRescueState ---

// The write-back re-reads under the write and REFUSES outright when the
// stored At differs. That refusal is the whole point: a delivery that landed
// while the pass was swapping starts a NEW hit, and stamping this rescue's
// counters onto it would tell a brand-new limit it had already been swapped
// — the same second-swap failure Amendment 2 closed, arriving by the
// read-modify-write route instead of by the fold.
func TestEdgeMergeLimitRescueStateRefusesAcrossHits(t *testing.T) {
	mine := LimitHit{
		Error: "rate_limit", At: edgeNow.Unix(), Deliveries: 1,
		Attempts: 1, LastAttempt: edgeNow.Unix(),
		SwappedAt: edgeNow.Unix(), SwappedTo: "rescued@example.com",
	}

	t.Run("a stored record from a different hit is refused", func(t *testing.T) {
		newer := LimitHit{Error: "rate_limit", At: edgeNow.Add(5 * time.Minute).Unix(), Deliveries: 1}
		got, ok := mergeLimitRescueState(newer, mine)
		if ok {
			t.Fatalf("merge accepted a write across hits: %+v", got)
		}
		if got != newer {
			t.Fatalf("a refused merge returned %+v, want the stored record %+v untouched", got, newer)
		}
	})

	t.Run("an OLDER stored At is refused just as hard", func(t *testing.T) {
		// direction-free: the rule is "not the same hit", not "not newer"
		older := LimitHit{Error: "rate_limit", At: edgeNow.Add(-5 * time.Minute).Unix(), Deliveries: 4}
		if got, ok := mergeLimitRescueState(older, mine); ok {
			t.Fatalf("merge accepted a write onto an older hit: %+v", got)
		}
	})

	t.Run("a zero stored record is written wholesale", func(t *testing.T) {
		// the row is missing or no longer parses, so our copy is the only
		// readable version left — refusing here would discard a real swap
		got, ok := mergeLimitRescueState(LimitHit{}, mine)
		if !ok || got != mine {
			t.Fatalf("= (%+v, %v), want (%+v, true)", got, ok, mine)
		}
	})

	t.Run("same hit: only the six rescue-owned fields cross", func(t *testing.T) {
		// the delivery half of the record (Deliveries, and the identity the
		// first delivery set) belongs to the hook and must survive a rescue
		// write that raced it
		stored := LimitHit{
			Error: "rate_limit", Details: "the stored message", At: edgeNow.Unix(),
			SessionID: "sess-stored", AgentID: "agent-stored", Deliveries: 7,
		}
		got, ok := mergeLimitRescueState(stored, mine)
		if !ok {
			t.Fatal("merge refused a write to the same hit")
		}
		want := stored
		want.Attempts, want.LastAttempt = mine.Attempts, mine.LastAttempt
		want.SwappedAt, want.SwappedTo = mine.SwappedAt, mine.SwappedTo
		want.HandledAt, want.Outcome = mine.HandledAt, mine.Outcome
		if got != want {
			t.Fatalf("merged = %+v, want %+v — only the six rescue-owned fields may cross", got, want)
		}
		if got.Deliveries != 7 || got.Details != "the stored message" || got.SessionID != "sess-stored" {
			t.Errorf("the rescue clobbered the hook's half of the record: %+v", got)
		}
	})
}

// --- Amendment 6: the resume guard's second half ---

// The caller's guard is `err == nil && resumed > 0`, and the missing-zeus
// error is not the only way a resume reaches nobody: a cell that HAS a zeus
// whose respawn fails returns (0, nil), and a caller checking only the error
// would resolve the hit with the whole cell still sitting on a dead account.
// This pins the pair the guard has to read, through the respawnPeerPane seam
// so no tmux server is involved.
func TestEdgeResumeCellAgentsRefusedRespawnCountsOutToZero(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	j := &Job{Slug: "edge-resume-refused", ID: 11, Repo: "monolith"}
	sess := CellSession(j)
	// the resume stages zeus' brief into the cell dir before it respawns
	// anything, so the dir has to exist or the command never gets built
	if err := os.MkdirAll(cellDir(j), 0o755); err != nil {
		t.Fatalf("fixture: %v", err)
	}

	origRespawn := respawnPeerPane
	t.Cleanup(func() { respawnPeerPane = origRespawn })
	tried := 0
	respawnPeerPane = func(p *core.Peer, cmd string) error {
		tried++
		return fmt.Errorf("pane %s is gone", p.Pane)
	}

	// zeus by IDENTITY (its --name) with a real cwd: the resume refuses a
	// merely-positional election and refuses an empty cwd, so a bare
	// edgeAgentPeer would never reach the seam at all
	zeus := edgeAgentPeer(sess, "%999991", "claude", 0)
	zeus.Name = ZeusName(j)
	zeus.CWD = home
	peers := []*core.Peer{zeus}
	if CellZeus(j, peers) == nil {
		t.Fatal("fixture: want a zeus in the cell session, got none")
	}

	resumed, err := resumeCellAgents(j, peers, limitRescueResumePrompt)
	if err != nil {
		t.Fatalf("resumeCellAgents = (%d, %v), want a nil error — a failed respawn is counted out, never fatal", resumed, err)
	}
	if resumed != 0 {
		t.Fatalf("resumed = %d, want 0 — the respawn failed", resumed)
	}
	if tried != 1 {
		t.Fatalf("the seam was called %d time(s), want exactly 1 — zeus and nobody else", tried)
	}
	// the pairing the guard depends on: err==nil does NOT imply zeus came
	// back, so `resumed > 0` has to be checked in its own right
	if empty, emptyErr := resumeCellAgents(j, nil, limitRescueResumePrompt); emptyErr == nil || empty != 0 {
		t.Fatalf("no zeus = (%d, %v), want (0, error)", empty, emptyErr)
	}
}

// --- Amendment 9: the parser degrades per FIELD ---

// A field that drifts to an object, array or number costs that one field and
// nothing more. The struct-of-strings decode this replaced failed the WHOLE
// payload on the first type mismatch, so the day Claude Code gives
// error_details a structure every payload — including ones carrying a
// perfectly readable rate_limit — would have parsed as garbage and the
// rescue would have switched off with nothing logging a reason.
func TestEdgeParseLimitPayloadDegradesPerField(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		wantOK    bool
		wantErr   string
		wantDet   string
		wantSess  string
		wantAgent string
	}{
		{
			name:    "error_details drifted to an object costs only error_details",
			raw:     `{"error":"rate_limit","error_details":{"resets_at":"4pm"},"session_id":"s"}`,
			wantOK:  true,
			wantErr: "rate_limit", wantSess: "s",
		},
		{
			name:    "error_details drifted to an array costs only error_details",
			raw:     `{"error":"rate_limit","error_details":["a","b"]}`,
			wantOK:  true,
			wantErr: "rate_limit",
		},
		{
			name:    "error_details drifted to a number costs only error_details",
			raw:     `{"error":"rate_limit","error_details":429}`,
			wantOK:  true,
			wantErr: "rate_limit",
		},
		{
			// the fallback still has to work while its sibling is drifted
			name:    "a drifted error_details falls through to error_message",
			raw:     `{"error":"rate_limit","error_details":{"x":1},"error_message":"resets 4pm"}`,
			wantOK:  true,
			wantErr: "rate_limit", wantDet: "resets 4pm",
		},
		{
			name:      "session_id drifted to a number costs only session_id",
			raw:       `{"error":"rate_limit","session_id":12345,"agent_id":"agent-7"}`,
			wantOK:    true,
			wantErr:   "rate_limit",
			wantAgent: "agent-7",
		},
		{
			name:    "agent_id drifted to an object costs only agent_id",
			raw:     `{"error":"rate_limit","session_id":"s","agent_id":{"id":"a"}}`,
			wantOK:  true,
			wantErr: "rate_limit", wantSess: "s",
		},
		{
			// the documented fallback survives its shipping sibling drifting,
			// which the struct decode specifically could not do
			name:    "error drifted to an object still falls back to error_type",
			raw:     `{"error":{"type":"rate_limit"},"error_type":"rate_limit"}`,
			wantOK:  true,
			wantErr: "rate_limit",
		},
		{
			name:   "error drifted to a number with no fallback is the empty-type refusal",
			raw:    `{"error":429}`,
			wantOK: false,
		},
		{
			name:    "every field drifted at once still yields the error type",
			raw:     `{"error":"rate_limit","error_details":[],"error_message":{},"session_id":1,"agent_id":true}`,
			wantOK:  true,
			wantErr: "rate_limit",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			errType, details, sess, agent, ok := parseLimitPayload([]byte(c.raw))
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v (errType=%q)", ok, c.wantOK, errType)
			}
			if !ok {
				return
			}
			if errType != c.wantErr || details != c.wantDet || sess != c.wantSess || agent != c.wantAgent {
				t.Errorf("= (%q, %q, %q, %q), want (%q, %q, %q, %q)",
					errType, details, sess, agent, c.wantErr, c.wantDet, c.wantSess, c.wantAgent)
			}
		})
	}
}

// The hook truncates details BEFORE posting: the route caps the request body
// at 64 KiB, so an over-long message would otherwise come back 400 and lose
// the limit entirely — a refusal over free text kraken did not author.
func TestEdgeLimitHookTruncatesDetailsBeforePosting(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", strings.TrimPrefix(srv.URL, "http://"))

	// ~8 KB of details: far past the 200-rune cap, and deliberately well
	// UNDER the ~64 KiB pipe buffer withStdin writes into before anything
	// reads — a payload larger than that buffer deadlocks the test rather
	// than the code.
	long := "You've hit your session limit.\n" + strings.Repeat("wait or upgrade ", 500)
	payload, err := json.Marshal(map[string]string{"error": "rate_limit", "error_details": long})
	if err != nil {
		t.Fatal(err)
	}
	withStderrCapture(t, func() {
		withStdin(t, string(payload), func() { LimitHook("edge-hook-trunc") })
	})

	if len(gotBody) == 0 {
		t.Fatal("nothing was posted")
	}
	// the posted body is bounded by the truncation rather than by the
	// payload: that is what keeps a large StopFailure message from meeting
	// the route's 64 KiB cap and coming back 400 with the limit lost
	if len(gotBody) >= len(payload) {
		t.Fatalf("posted body is %d bytes for a %d-byte payload — details were forwarded untruncated", len(gotBody), len(payload))
	}
	var req map[string]string
	if err := json.Unmarshal(gotBody, &req); err != nil {
		t.Fatal(err)
	}
	if want := core.TruncateOneLine(long, 200); req["details"] != want {
		t.Fatalf("posted details = %q, want core.TruncateOneLine(details, 200)", req["details"])
	}
	if req["error"] != "rate_limit" {
		t.Errorf("posted error = %q, want rate_limit", req["error"])
	}
}

// --- Amendment 11: the resume is bounded ---

// The bound this arm exists for is the one the re-review found: while a hit
// sits swapped-but-open, a delivery past the dedupe window supersedes it and
// re-arms the rotate gate with Attempts back at zero, so the attempt cap is
// BYPASSED rather than exceeded and the pool drains an account per cycle.
// The resume arm therefore counts its failed rounds and parks, increment-then-
// test exactly as rescueParkNoTarget does — so both sides of the cap want
// pinning: below it the hit must stay OPEN so a later tick retries, at it the
// hit must park.
func TestEdgeLimitRescueApplyResumeNoAgentsCapBoundary(t *testing.T) {
	cases := []struct {
		name         string
		startAttempt int
		wantAttempts int
		wantParked   bool
	}{
		{"first failed round stays open", 0, 1, false},
		{"second failed round stays open", 1, 2, false},
		{"third failed round parks", 2, limitRescueMaxAttempts, true},
		{"a record already at the cap parks too", limitRescueMaxAttempts, limitRescueMaxAttempts + 1, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hit := edgeApplyHit()
			hit.Attempts = c.startAttempt
			hit.LastAttempt = edgeNow.Add(-time.Hour).Unix()
			// the account really did move before the resume was attempted:
			// a park must still record WHAT it moved to
			hit.SwappedAt = edgeNow.Add(-limitRescueSettle).Unix()
			hit.SwappedTo = "rescued@example.com"

			got := limitRescueApply(hit, rescueResume, "no-agents", "", edgeNow)

			if got.Attempts != c.wantAttempts {
				t.Fatalf("Attempts = %d, want %d (started at %d)", got.Attempts, c.wantAttempts, c.startAttempt)
			}
			if got.LastAttempt != edgeNow.Unix() {
				t.Errorf("LastAttempt = %d, want %d — every round stamps its attempt", got.LastAttempt, edgeNow.Unix())
			}
			if c.wantParked {
				if got.HandledAt != edgeNow.Unix() {
					t.Errorf("HandledAt = %d, want %d — the rounds are spent, the hit parks", got.HandledAt, edgeNow.Unix())
				}
				if got.Outcome != "parked-no-agents" {
					t.Errorf("Outcome = %q, want %q", got.Outcome, "parked-no-agents")
				}
			} else {
				if got.HandledAt != 0 {
					t.Errorf("HandledAt = %d, want 0 — a round below the cap must leave the hit open so a later tick retries", got.HandledAt)
				}
				if got.Outcome != "" {
					t.Errorf("Outcome = %q, want empty — the hit is not resolved yet", got.Outcome)
				}
				if got.Outcome == "rotated" {
					t.Error("a round that reached nobody resolved as rotated — that is the stranded-cell bug this arm exists to prevent")
				}
			}
			// either way the watermark is untouched: it is the record of a
			// swap that really happened, and a park that forgot it would let
			// a superseding delivery re-arm the rotate gate
			if got.SwappedAt != hit.SwappedAt || got.SwappedTo != hit.SwappedTo {
				t.Errorf("watermark = (%d, %q), want it unchanged", got.SwappedAt, got.SwappedTo)
			}
		})
	}
}

// The same arithmetic stated as the number it produces, the pair
// TestEdgeLimitRescueApplyParkNoTargetResolvesAtExactlyTheCap already pins
// for the other capped arm: the round that gives up reads exactly
// limitRescueMaxAttempts, and the round before it must not have parked.
func TestEdgeLimitRescueApplyResumeParksAtExactlyTheCap(t *testing.T) {
	at := edgeApplyHit()
	at.Attempts = limitRescueMaxAttempts - 1
	got := limitRescueApply(at, rescueResume, "no-agents", "", edgeNow)
	if got.HandledAt == 0 {
		t.Fatalf("a round from Attempts=%d left the hit open — it must park on the %dth", limitRescueMaxAttempts-1, limitRescueMaxAttempts)
	}
	if got.Attempts != limitRescueMaxAttempts {
		t.Fatalf("the parking round recorded Attempts=%d, want exactly %d — a higher number is a round that never happened", got.Attempts, limitRescueMaxAttempts)
	}

	before := edgeApplyHit()
	before.Attempts = limitRescueMaxAttempts - 2
	if got := limitRescueApply(before, rescueResume, "no-agents", "", edgeNow); got.HandledAt != 0 {
		t.Fatalf("a round from Attempts=%d parked: HandledAt=%d — the cap fires early", limitRescueMaxAttempts-2, got.HandledAt)
	}
}

// `"no-agents"` is a string literal doing load-bearing work: it is the ONLY
// value that sends the resume arm down the counting path, and everything else
// — including the `""` the resolving call site passes — resolves the hit.
// An overloaded literal with no test is how a later refactor renames one
// side and silently turns every failed round into a resolution, which is the
// stranded-cell bug wearing the fix's clothes.
func TestEdgeLimitRescueApplyResumeOutcomeLiteralIsExact(t *testing.T) {
	// every one of these must take the RESOLVE path
	for _, outcome := range []string{
		"",              // what the reached-at-least-one-pane call site passes
		"rotated",       // the Outcome it writes, fed back by mistake
		"rebound",       // the rotate arm's literal, in the wrong arm
		"no-agent",      // one character off
		"No-Agents",     // case variant
		" no-agents",    // whitespace
		"no-agents-yet", // prefix of a longer word
		"partial",
	} {
		t.Run("outcome "+outcome, func(t *testing.T) {
			hit := edgeApplyHit()
			hit.Attempts = 1
			hit.SwappedAt = edgeNow.Unix() - 60 // a landed swap: this resume is the swap's completion
			got := limitRescueApply(hit, rescueResume, outcome, "", edgeNow)
			if got.Outcome != "rotated" || got.HandledAt != edgeNow.Unix() {
				t.Fatalf("= (HandledAt %d, Outcome %q), want (%d, %q) — only the exact literal counts a round", got.HandledAt, got.Outcome, edgeNow.Unix(), "rotated")
			}
			if got.Attempts != hit.Attempts {
				t.Errorf("Attempts = %d, want %d unchanged — the resolving path counts nothing", got.Attempts, hit.Attempts)
			}
		})
		t.Run("outcome "+outcome+" reset re-arm", func(t *testing.T) {
			// The same resolve path with NO landed swap can only be the
			// reset re-arm's resume, and the outcome word must say so — a
			// "rotated" here would claim a credential change that never
			// happened.
			hit := edgeApplyHit()
			hit.Attempts = limitRescueMaxAttempts
			hit.ResetAt = edgeNow.Unix() - 300
			got := limitRescueApply(hit, rescueResume, outcome, "", edgeNow)
			if got.Outcome != "resumed-after-reset" || got.HandledAt != edgeNow.Unix() {
				t.Fatalf("= (HandledAt %d, Outcome %q), want (%d, %q)", got.HandledAt, got.Outcome, edgeNow.Unix(), "resumed-after-reset")
			}
		})
	}

	// ... and the literal must not leak into the other arms, where the same
	// argument carries a completely different vocabulary
	t.Run("park-no-target ignores it", func(t *testing.T) {
		hit := edgeApplyHit()
		withLiteral := limitRescueApply(hit, rescueParkNoTarget, "no-agents", "", edgeNow)
		withEmpty := limitRescueApply(hit, rescueParkNoTarget, "", "", edgeNow)
		if withLiteral != withEmpty {
			t.Fatalf("park-no-target read the resume arm's literal: %+v vs %+v", withLiteral, withEmpty)
		}
	})
	t.Run("rotate treats it as any other non-swap outcome", func(t *testing.T) {
		hit := edgeApplyHit()
		got := limitRescueApply(hit, rescueRotate, "no-agents", "someone@example.com", edgeNow)
		if got.SwappedAt != 0 || got.SwappedTo != "" {
			t.Fatalf("rotate wrote a watermark for a non-swap outcome: (%d, %q)", got.SwappedAt, got.SwappedTo)
		}
	})
}

// --- Amendment 11: the supersession carry-forward ---

// The other half of the same fix, and the half that closes the loop: a fresh
// record must inherit SwappedAt/SwappedTo from the record it supersedes when
// that record was swapped and still open, so gate 5 keeps ownership and the
// rotate gate cannot re-arm with Attempts back at zero. Both directions
// matter — carrying from a RESOLVED record would resurrect a finished swap.
func TestEdgeMergeLimitHitDeliveryCarriesTheSwapWatermark(t *testing.T) {
	past := edgeNow.Add(-time.Hour)
	swapped := LimitHit{
		Error: "rate_limit", Details: "the first delivery", At: past.Unix(),
		SessionID: "sess-old", AgentID: "agent-old", Deliveries: 3,
		Attempts: 1, LastAttempt: past.Unix(),
		SwappedAt: past.Unix() + 15, SwappedTo: "rescued@example.com",
	}

	t.Run("carried when the superseded record was swapped and open", func(t *testing.T) {
		// A same-error delivery now FOLDS onto an unresolved hit however
		// late it lands, so the fresh branch is reached through a DIFFERENT
		// error type. The carry must survive that hop and the hop back:
		// unresolved-swapped → billing_error supersedes (carries) →
		// rate_limit supersedes again (carries) → gate 5 resumes rather than
		// swapping a second time.
		hop := mergeLimitHitDelivery(swapped, "billing_error", "a different failure", "sess-mid", "", edgeNow.Add(-time.Minute))
		if hop.Error != "billing_error" || hop.At != edgeNow.Add(-time.Minute).Unix() || hop.Deliveries != 1 {
			t.Fatalf("first supersession = %+v, want a fresh billing_error record", hop)
		}
		if hop.SwappedAt != swapped.SwappedAt || hop.SwappedTo != swapped.SwappedTo {
			t.Fatalf("first supersession dropped the swap watermark: (%d, %q)", hop.SwappedAt, hop.SwappedTo)
		}

		got := mergeLimitHitDelivery(hop, "rate_limit", "a later message", "sess-new", "agent-new", edgeNow)
		want := LimitHit{
			Error: "rate_limit", Details: "a later message", At: edgeNow.Unix(),
			SessionID: "sess-new", AgentID: "agent-new", Deliveries: 1,
			SwappedAt: swapped.SwappedAt, SwappedTo: swapped.SwappedTo,
		}
		if got != want {
			t.Fatalf("superseded record = %+v, want %+v — only the swap watermark crosses", got, want)
		}
		// the consequence, spelled out: the superseding record still belongs
		// to gate 5, so it resumes rather than swapping a second time
		when := time.Unix(got.SwappedAt, 0).Add(limitRescueSettle)
		if act, reason := limitRescueDecide(got, "auto", true, true, when); act != rescueResume {
			t.Fatalf("after supersession the decision is %q (%q), want %q — the rotate gate re-armed", act, reason, rescueResume)
		}
	})

	t.Run("not carried when the superseded record was already resolved", func(t *testing.T) {
		resolved := swapped
		resolved.HandledAt = past.Unix() + 30
		resolved.Outcome = "rotated"

		got := mergeLimitHitDelivery(resolved, "rate_limit", "a new limit entirely", "sess-new", "", edgeNow)
		if got.SwappedAt != 0 || got.SwappedTo != "" {
			t.Fatalf("a finished swap was carried onto a new hit: (%d, %q) — the new limit would report itself already swapped and skip its own rescue", got.SwappedAt, got.SwappedTo)
		}
		if act, _ := limitRescueDecide(got, "auto", true, true, edgeNow); act != rescueRotate {
			t.Fatalf("the new hit decides %q, want %q — a genuinely new limit must get its own swap", act, rescueRotate)
		}
	})

	t.Run("not carried when the superseded record never swapped", func(t *testing.T) {
		unswapped := swapped
		unswapped.SwappedAt, unswapped.SwappedTo = 0, ""
		got := mergeLimitHitDelivery(unswapped, "rate_limit", "", "", "", edgeNow)
		if got.SwappedAt != 0 || got.SwappedTo != "" {
			t.Fatalf("a watermark appeared from nowhere: (%d, %q)", got.SwappedAt, got.SwappedTo)
		}
	})

	t.Run("the carry condition reads the superseded record only, not the error type", func(t *testing.T) {
		// a different error type still takes the fresh branch, and the
		// condition the contract states carries no error-type qualifier —
		// pinned literally so that adding one is a visible change
		got := mergeLimitHitDelivery(swapped, "overloaded", "", "", "", edgeNow)
		if got.Error != "overloaded" || got.Deliveries != 1 {
			t.Fatalf("a different error type did not take the fresh branch: %+v", got)
		}
		if got.SwappedAt != swapped.SwappedAt {
			t.Errorf("SwappedAt = %d, want %d carried", got.SwappedAt, swapped.SwappedAt)
		}
	})

	t.Run("a fold is unaffected by the carry", func(t *testing.T) {
		// inside the window the fold branch runs and changes only Deliveries,
		// so the watermark survives by that route rather than this one
		inWindow := swapped
		inWindow.At = edgeNow.Add(-time.Second).Unix()
		got := mergeLimitHitDelivery(inWindow, "rate_limit", "other", "other", "other", edgeNow)
		want := inWindow
		want.Deliveries++
		if got != want {
			t.Fatalf("fold = %+v, want %+v", got, want)
		}
	})
}

// --- the rescue's refusal paths ---

// edgeSeedUsageSnapshot writes a snapshot with one main row and one pool row
// into the wired store, which is what selectRescueBind reads before it ever
// reaches the selector — without it the rescue exits at "no usage snapshot"
// and the selection step is never entered at all.
func edgeSeedUsageSnapshot(t *testing.T, st *store.Store) {
	t.Helper()
	snap := core.UsageSnapshot{Accounts: []core.AccountUsage{
		{Slug: "main", Email: "main@example.com"},
		{Slug: "pool-a", Email: "a@example.com"},
	}}
	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetKV(usageSnapshotKVKey, string(data)); err != nil {
		t.Fatal(err)
	}
}

// The step-ordering proof this held — lock, then select, then swap, each
// refusal meaning the later steps were never reached — retired with Q2 along
// with two of its three steps: rescueLimitedCell takes no flock (a rebind
// displaces nothing machine-wide, so the binder's in-process mutex is the
// whole guard) and never swaps the machine's login. "Never swaps" is pinned
// statically now by TestRetirementNoAutomatedMainSwap, which is stronger
// than a stub nobody calls — it fails on the mere REFERENCE. What survives
// here is the middle step.
func TestEdgeRescueLimitedCellRefusalOrderingThroughTheSeams(t *testing.T) {
	j := &Job{Slug: "edge-seam", ID: 21, Repo: "monolith"}

	t.Run("no target: nothing is bound", func(t *testing.T) {
		st := useTestStore(t)
		stubRotation(t)
		edgeSeedUsageSnapshot(t, st)
		// no keychain items exist under this scratch HOME, so every seeded
		// slot is excluded by the item-exists guard
		slotItemExists = func(string) bool { return false }
		t.Cleanup(func() { slotItemExists = core.SlotItemExists })

		to, outcome, err := rescueLimitedCell(j, edgeNow)
		if outcome != "no-target" || err != nil || to != "" {
			t.Fatalf("= (%q, %q, %v), want (\"\", \"no-target\", nil)", to, outcome, err)
		}
	})
}
