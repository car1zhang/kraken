package jobs

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"kraken/internal/core"
	"kraken/internal/store"
)

// limitrescue_contract_test.go — blind contract tests for the jobs-side half
// of the mid-run usage-limit rescue (50-contract.md, slices A and B). Written
// from the contract text alone; no implementation file was read.
//
// Dimension: LOGIC — the stated invariants and the happy paths. The ten-gate
// decision chain IN ITS STATED ORDER, the dedupe/fold rule, the parse
// precedence, the round-trip of the stored record, the pane enumeration's
// inclusion rules. Boundaries, error paths and refusals belong to a sibling
// author.
//
// Every package-level identifier here is prefixed `contractLimit` — the jobs
// package shares one declaration space across every _test.go file in it.

// --- jobs.LimitHit: the stored record's shape and round-trip ---
//
// Contract row `jobs.LimitHit`: the twelve fields, their json tags, and the
// obligation that slice A round-trips SwappedAt/SwappedTo (slice B's alone to
// write) through marshal/unmarshal unchanged.

// contractLimitFullHit is a LimitHit with every field set to a distinct,
// recognisable value — nothing zero, so a dropped field cannot pass by
// coincidence.
func contractLimitFullHit() LimitHit {
	return LimitHit{
		Error:       "rate_limit",
		Details:     "You've hit your session limit",
		At:          1_700_000_001,
		SessionID:   "sess-abc",
		AgentID:     "agent-xyz",
		Deliveries:  4,
		HandledAt:   1_700_000_002,
		Outcome:     "rotated",
		Attempts:    2,
		LastAttempt: 1_700_000_003,
		SwappedAt:   1_700_000_004,
		SwappedTo:   "carl@example.com",
		ResetAt:     1_700_000_005,
	}
}

// TestContractLimitHitJSONTags pins the twelve wire names the contract row
// spells out. The record is persisted as one kv row, so these names ARE the
// storage format: renaming one silently orphans every record written before
// the rename.
func TestContractLimitHitJSONTags(t *testing.T) {
	raw, err := json.Marshal(contractLimitFullHit())
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"error", "details", "at", "session_id", "agent_id", "deliveries",
		"handled_at", "outcome", "attempts", "last_attempt_at",
		"swapped_at", "swapped_to", "reset_at",
	}
	for _, k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("marshalled record has no %q key: %s", k, raw)
		}
	}
	if len(got) != len(want) {
		keys := make([]string, 0, len(got))
		for k := range got {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		t.Errorf("marshalled %d keys %v, want exactly %d %v", len(got), keys, len(want), want)
	}
}

// TestContractLimitHitRoundTrip is the round-trip obligation itself, and its
// point is the last two fields: slice A declares SwappedAt/SwappedTo without
// ever writing them, so the only thing that can keep slice B's swap watermark
// alive across a store write is slice A's marshal carrying them unchanged. A
// dropped SwappedAt reads as "no swap has landed", which sends the rescue
// round the rotate gate a second time on an account it already swapped.
func TestContractLimitHitRoundTrip(t *testing.T) {
	want := contractLimitFullHit()
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got LimitHit
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
}

// TestContractLimitHitZeroValueMeansNoHit pins the sentinel the whole feature
// reads on: "A zero-value LimitHit (At == 0) means no hit is recorded", and
// "HandledAt == 0 means unresolved".
func TestContractLimitHitZeroValueMeansNoHit(t *testing.T) {
	var zero LimitHit
	if zero.At != 0 {
		t.Errorf("zero LimitHit.At = %d, want 0", zero.At)
	}
	if zero.HandledAt != 0 {
		t.Errorf("zero LimitHit.HandledAt = %d, want 0 (unresolved)", zero.HandledAt)
	}
}

// --- jobs.limitHitKey ---

// TestContractLimitHitKey: Amendment 5 rekeyed the row on the job's IMMUTABLE
// id — "limit_hit:" + strconv.FormatInt(id, 10) — rather than its slug. A slug
// is unique only among ACTIVE jobs and archiving frees it, nothing ever
// deletes this row, and gate 4 leaves a hit on a torn-down cell unresolved
// forever; keyed on the slug, a new job reusing an archived generation's name
// would read the dead generation's record and perform a real machine-wide
// credential swap on evidence from a job that no longer exists.
//
// The two-generation case is the assertion that matters: same slug, different
// ids, and the keys must not collide.
func TestContractLimitHitKey(t *testing.T) {
	for _, id := range []int64{0, 1, 42, 9_007_199_254_740_993} {
		if got, want := limitHitKey(id), "limit_hit:"+strconv.FormatInt(id, 10); got != want {
			t.Errorf("limitHitKey(%d) = %q, want %q", id, got, want)
		}
	}
	if limitHitKey(7) == limitHitKey(8) {
		t.Fatal("two job ids share one key — an archived generation's record would be read by its successor")
	}
}

// contractLimitJob creates a real store job and returns the *Job the record
// API now takes (Amendment 5). Every store-backed case below needs one,
// because the kv key is derived from ID and a hand-built Job{Slug: …} would
// key every fixture onto "limit_hit:0".
func contractLimitJob(t *testing.T, st *store.Store, slug string) *Job {
	t.Helper()
	row := mustCreateJob(t, st, store.Job{Slug: slug, Repo: "kraken", State: "working"})
	return &Job{ID: row.ID, Slug: row.Slug}
}

// --- jobs.parseLimitPayload: the field precedence ---
//
// Contract row `jobs.parseLimitPayload`: reads `error` first and falls back to
// `error_type` when `error` is absent OR empty; reads `error_details` first
// and falls back to `error_message`; unknown extra fields never cause a
// failure; an `api_error` field, present or not, is ignored.

func TestContractLimitParsePayloadPrecedence(t *testing.T) {
	cases := []struct {
		name        string
		raw         string
		wantError   string
		wantDetails string
		wantSession string
		wantAgent   string
	}{
		{
			// the payload actually captured from Claude Code 2.1.222, quoted
			// in the contract's ground-truth section: `error`, no
			// `error_type`, no `api_error`, no `agent_id`, no `error_details`
			name:        "the captured shipping payload",
			raw:         `{"session_id":"s1","transcript_path":"/tmp/t.jsonl","cwd":"/tmp","prompt_id":"p1","effort":{"level":"high"},"hook_event_name":"StopFailure","error":"authentication_failed","last_assistant_message":"Invalid API key"}`,
			wantError:   "authentication_failed",
			wantSession: "s1",
		},
		{
			name:        "rate_limit with details and an agent id",
			raw:         `{"hook_event_name":"StopFailure","error":"rate_limit","error_details":"You've hit your session limit","session_id":"s2","agent_id":"a2"}`,
			wantError:   "rate_limit",
			wantDetails: "You've hit your session limit",
			wantSession: "s2",
			wantAgent:   "a2",
		},
		{
			// `error` wins outright when both spellings are present — the
			// binary's own field is the truth, the documented one is only a
			// fallback for a schema Claude Code may change
			name:      "error wins over error_type when both are present",
			raw:       `{"error":"rate_limit","error_type":"overloaded","session_id":"s3"}`,
			wantError: "rate_limit",
		},
		{
			name:      "error_type used when error is absent",
			raw:       `{"error_type":"rate_limit","session_id":"s4"}`,
			wantError: "rate_limit",
		},
		{
			// "absent OR EMPTY" — an empty `error` must not shadow a
			// populated `error_type`
			name:      "error_type used when error is present but empty",
			raw:       `{"error":"","error_type":"rate_limit"}`,
			wantError: "rate_limit",
		},
		{
			name:        "error_details wins over error_message",
			raw:         `{"error":"rate_limit","error_details":"binary field","error_message":"documented field"}`,
			wantError:   "rate_limit",
			wantDetails: "binary field",
		},
		{
			name:        "error_message used when error_details is absent",
			raw:         `{"error":"rate_limit","error_message":"documented field"}`,
			wantError:   "rate_limit",
			wantDetails: "documented field",
		},
		{
			name:      "unknown extra fields are ignored, not fatal",
			raw:       `{"error":"rate_limit","permission_mode":"acceptEdits","agent_type":"zeus","some_future_key":{"nested":[1,2,3]}}`,
			wantError: "rate_limit",
		},
		{
			// api_error is ignored whatever shape it takes — the contract
			// says the shipping binary sends none at all, and the parser must
			// not start depending on one if a later build adds it back
			name:      "api_error as an object is ignored",
			raw:       `{"error":"rate_limit","api_error":{"type":"rate_limit_error","message":"nope"}}`,
			wantError: "rate_limit",
		},
		{
			name:      "api_error as a string is ignored",
			raw:       `{"error":"rate_limit","api_error":"rate_limit_error"}`,
			wantError: "rate_limit",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			errType, details, sessionID, agentID, ok := parseLimitPayload([]byte(c.raw))
			if !ok {
				t.Fatalf("ok = false, want true for %s", c.raw)
			}
			if errType != c.wantError {
				t.Errorf("errorType = %q, want %q", errType, c.wantError)
			}
			if details != c.wantDetails {
				t.Errorf("details = %q, want %q", details, c.wantDetails)
			}
			if c.wantSession != "" && sessionID != c.wantSession {
				t.Errorf("sessionID = %q, want %q", sessionID, c.wantSession)
			}
			if agentID != c.wantAgent {
				t.Errorf("agentID = %q, want %q", agentID, c.wantAgent)
			}
		})
	}
}

// TestContractLimitParsePayloadAbsentOptionalsAreNormal restates the ground
// truth the contract draws a conclusion from: on the main thread the shipping
// binary omits `agent_id` and `error_details` entirely, so their absence is
// the NORMAL case and must parse, not the error case.
func TestContractLimitParsePayloadAbsentOptionalsAreNormal(t *testing.T) {
	_, details, _, agentID, ok := parseLimitPayload([]byte(`{"hook_event_name":"StopFailure","error":"rate_limit","session_id":"s"}`))
	if !ok {
		t.Fatal("ok = false for a payload missing only its optional fields")
	}
	if details != "" || agentID != "" {
		t.Errorf("details=%q agentID=%q, want both empty", details, agentID)
	}
}

// --- jobs.limitHookCommand / limitHookEntry ---
//
// Contract row `jobs.limitHookCommand`: byte-for-byte the same flag rule
// prHookCommand applies. These four tests mirror the four prHookCommand tests
// in cell_test.go one for one, because "the same rule" is the obligation.

func TestContractLimitHookCommandDefaults(t *testing.T) {
	SetHookServeAddr("")
	t.Cleanup(func() { SetHookServeAddr("") })
	t.Setenv("KRAKEN_DB_PATH", "")

	if got, want := limitHookCommand("foo", "kraken"), "kraken limit-hook foo"; got != want {
		t.Fatalf("limitHookCommand = %q, want %q", got, want)
	}
}

func TestContractLimitHookCommandCarriesNonDefaultServeAddr(t *testing.T) {
	SetHookServeAddr("127.0.0.1:9999")
	t.Cleanup(func() { SetHookServeAddr("") })
	t.Setenv("KRAKEN_DB_PATH", "")

	if got, want := limitHookCommand("foo", "kraken"), "kraken limit-hook --serve-addr 127.0.0.1:9999 foo"; got != want {
		t.Fatalf("limitHookCommand = %q, want %q", got, want)
	}
}

func TestContractLimitHookCommandOmitsDefaultServeAddr(t *testing.T) {
	SetHookServeAddr(defaultServeAddr)
	t.Cleanup(func() { SetHookServeAddr("") })
	t.Setenv("KRAKEN_DB_PATH", "")

	if got, want := limitHookCommand("foo", "kraken"), "kraken limit-hook foo"; got != want {
		t.Fatalf("limitHookCommand = %q, want %q", got, want)
	}
}

// Both overrides at once pins the ORDER the contract spells out:
// `<krakenBin> limit-hook [--serve-addr <addr>] [--db-path <p>] <slug>`.
func TestContractLimitHookCommandCarriesBothOverrides(t *testing.T) {
	SetHookServeAddr("127.0.0.1:9999")
	t.Cleanup(func() { SetHookServeAddr("") })
	t.Setenv("KRAKEN_DB_PATH", "/custom/kraken.db")

	got := limitHookCommand("foo", "kraken")
	want := "kraken limit-hook --serve-addr 127.0.0.1:9999 --db-path /custom/kraken.db foo"
	if got != want {
		t.Fatalf("limitHookCommand = %q, want %q", got, want)
	}
}

// TestContractLimitHookEntry pins the settings-file shape: matcher
// "rate_limit" (the value the StopFailure matcher matches against — an
// `error` enum member, not a tool name), one command hook, and the command
// string built by the SHARED builder rather than a second copy.
func TestContractLimitHookEntry(t *testing.T) {
	SetHookServeAddr("")
	t.Cleanup(func() { SetHookServeAddr("") })
	t.Setenv("KRAKEN_DB_PATH", "")

	// asserted through the entry's MARSHALLED form: the contract freezes the
	// JSON the settings file must carry, not whether the inner list is built
	// as []any or []map[string]any, and a Go type assertion would be
	// asserting the latter.
	raw, err := json.Marshal(limitHookEntry("foo", "kraken"))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Matcher string `json:"matcher"`
		Hooks   []struct {
			Type    string `json:"type"`
			Command string `json:"command"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("entry does not marshal to the frozen shape: %v\n%s", err, raw)
	}
	if got.Matcher != "rate_limit" {
		t.Errorf("matcher = %q, want %q", got.Matcher, "rate_limit")
	}
	if len(got.Hooks) != 1 {
		t.Fatalf("hooks = %s, want exactly one command hook", raw)
	}
	if got.Hooks[0].Type != "command" {
		t.Errorf("hooks[0].type = %q, want %q", got.Hooks[0].Type, "command")
	}
	if want := limitHookCommand("foo", "kraken"); got.Hooks[0].Command != want {
		t.Errorf("hooks[0].command = %q, want %q — the entry must use the shared builder", got.Hooks[0].Command, want)
	}
}

// --- jobs.mergeHookSettings: the StopFailure install ---
//
// Contract row `jobs.mergeHookSettings`: the StopFailure entry lands under
// hooks.StopFailure with the same three properties the PostToolUse entry
// already has — merged never clobbered, idempotent by exact command-string
// match, and (a sibling's case) invalid JSON errors out.

// contractLimitStopFailureGroups pulls hooks.StopFailure out of a merged
// settings blob, failing the test if the path is missing or mis-shaped.
func contractLimitStopFailureGroups(t *testing.T, merged []byte) []any {
	t.Helper()
	var parsed map[string]any
	if err := json.Unmarshal(merged, &parsed); err != nil {
		t.Fatalf("merged settings are not valid JSON: %v\n%s", err, merged)
	}
	hooks, ok := parsed["hooks"].(map[string]any)
	if !ok {
		t.Fatalf("merged settings carry no hooks object: %s", merged)
	}
	groups, ok := hooks["StopFailure"].([]any)
	if !ok {
		t.Fatalf("merged settings carry no hooks.StopFailure array: %s", merged)
	}
	return groups
}

// contractLimitGroupCommands lists the command strings inside one hook group.
func contractLimitGroupCommands(t *testing.T, group any) []string {
	t.Helper()
	g, ok := group.(map[string]any)
	if !ok {
		t.Fatalf("hook group = %#v, want map[string]any", group)
	}
	inner, ok := g["hooks"].([]any)
	if !ok {
		t.Fatalf("hook group has no hooks array: %#v", g)
	}
	var out []string
	for _, h := range inner {
		hm, ok := h.(map[string]any)
		if !ok {
			continue
		}
		if cmd, ok := hm["command"].(string); ok {
			out = append(out, cmd)
		}
	}
	return out
}

func TestContractLimitMergeInstallsStopFailureEntry(t *testing.T) {
	SetHookServeAddr("")
	t.Cleanup(func() { SetHookServeAddr("") })
	t.Setenv("KRAKEN_DB_PATH", "")

	merged, err := mergeHookSettings(nil, "foo", "kraken")
	if err != nil {
		t.Fatal(err)
	}
	groups := contractLimitStopFailureGroups(t, merged)
	if len(groups) != 1 {
		t.Fatalf("want exactly 1 StopFailure group on a fresh render, got %d: %v", len(groups), groups)
	}
	g, ok := groups[0].(map[string]any)
	if !ok {
		t.Fatalf("group = %#v", groups[0])
	}
	if g["matcher"] != "rate_limit" {
		t.Errorf("matcher = %v, want rate_limit", g["matcher"])
	}
	if cmds := contractLimitGroupCommands(t, groups[0]); len(cmds) != 1 || cmds[0] != limitHookCommand("foo", "kraken") {
		t.Errorf("commands = %v, want [%q]", cmds, limitHookCommand("foo", "kraken"))
	}
}

// TestContractLimitMergeLeavesPrHookEntryUntouched: the cell's PostToolUse
// pr-hook entry is what attaches PRs to the job in real time. Installing the
// new StopFailure entry beside it must not disturb it — same file, one merge.
func TestContractLimitMergeLeavesPrHookEntryUntouched(t *testing.T) {
	SetHookServeAddr("")
	t.Cleanup(func() { SetHookServeAddr("") })
	t.Setenv("KRAKEN_DB_PATH", "")

	merged, err := mergeHookSettings(nil, "foo", "kraken")
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(merged, &parsed); err != nil {
		t.Fatal(err)
	}
	hooks, ok := parsed["hooks"].(map[string]any)
	if !ok {
		t.Fatalf("no hooks object: %s", merged)
	}
	post, ok := hooks["PostToolUse"].([]any)
	if !ok || len(post) == 0 {
		t.Fatalf("PostToolUse = %#v, want at least the pr-hook group", hooks["PostToolUse"])
	}
	// The obligation is that the pr-hook group survives intact, NOT that it is
	// alone under this key: the recall hook installs a matcher-less group under
	// PostToolUse too, so asserting cardinality here would fail on a change
	// that has nothing to do with the StopFailure entry.
	var found bool
	for _, g := range post {
		cmds := contractLimitGroupCommands(t, g)
		if len(cmds) == 1 && cmds[0] == prHookCommand("foo", "kraken") {
			found = true
		}
	}
	if !found {
		t.Errorf("PostToolUse = %#v, want it to still carry the pr-hook group %q", post, prHookCommand("foo", "kraken"))
	}
}

// TestContractLimitMergeStopFailureIdempotent: a reinstall (reactivation
// recreates the cell's settings) must add nothing. This is the same bug E7
// had to guard for pr-hook — the builder is shared with the dedup check
// precisely so a non-default addr can't make the two disagree, so the
// idempotency is asserted WITH the overrides set.
func TestContractLimitMergeStopFailureIdempotent(t *testing.T) {
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
	var firstParsed, secondParsed map[string]any
	if err := json.Unmarshal(first, &firstParsed); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(second, &secondParsed); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(firstParsed, secondParsed) {
		t.Fatalf("second merge changed the settings:\nfirst  = %s\nsecond = %s", first, second)
	}
	if groups := contractLimitStopFailureGroups(t, second); len(groups) != 1 {
		t.Fatalf("want 1 StopFailure group after re-merge, got %d: %v", len(groups), groups)
	}
}

// TestContractLimitMergeKeepsForeignStopFailureGroup: merged never clobbered.
// A user's own StopFailure hook — a different matcher, or the same one with a
// different command — survives, with kraken's appended beside it.
func TestContractLimitMergeKeepsForeignStopFailureGroup(t *testing.T) {
	SetHookServeAddr("")
	t.Cleanup(func() { SetHookServeAddr("") })
	t.Setenv("KRAKEN_DB_PATH", "")

	existing := `{"hooks":{"StopFailure":[{"matcher":"overloaded","hooks":[{"type":"command","command":"echo mine"}]}]}}`
	merged, err := mergeHookSettings([]byte(existing), "foo", "kraken")
	if err != nil {
		t.Fatal(err)
	}
	groups := contractLimitStopFailureGroups(t, merged)
	if len(groups) != 2 {
		t.Fatalf("want 2 StopFailure groups (theirs + kraken's), got %d: %v", len(groups), groups)
	}
	var all []string
	for _, g := range groups {
		all = append(all, contractLimitGroupCommands(t, g)...)
	}
	sort.Strings(all)
	want := []string{"echo mine", limitHookCommand("foo", "kraken")}
	sort.Strings(want)
	if !reflect.DeepEqual(all, want) {
		t.Errorf("commands = %v, want %v", all, want)
	}
}

// TestContractLimitMergePreservesUnrelatedTopLevelKeys: the settings file is a
// repo's own checked-in config; kraken adds to it and never rewrites it.
func TestContractLimitMergePreservesUnrelatedTopLevelKeys(t *testing.T) {
	SetHookServeAddr("")
	t.Cleanup(func() { SetHookServeAddr("") })
	t.Setenv("KRAKEN_DB_PATH", "")

	merged, err := mergeHookSettings([]byte(`{"env":{"FOO":"bar"},"permissions":{"allow":["Bash"]}}`), "foo", "kraken")
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(merged, &parsed); err != nil {
		t.Fatal(err)
	}
	env, ok := parsed["env"].(map[string]any)
	if !ok || env["FOO"] != "bar" {
		t.Errorf("env = %#v, want FOO preserved", parsed["env"])
	}
	if _, ok := parsed["permissions"]; !ok {
		t.Errorf("unrelated top-level key dropped: %s", merged)
	}
	if groups := contractLimitStopFailureGroups(t, merged); len(groups) != 1 {
		t.Errorf("want the StopFailure entry installed alongside, got %v", groups)
	}
}

// --- jobs.RecordLimitHit / jobs.ReadLimitHit: the fold rule ---
//
// Contract row `jobs.RecordLimitHit`: when the stored record is unresolved
// (HandledAt == 0) and carries the SAME Error, the record is kept with
// At/SessionID/AgentID unchanged and Deliveries incremented by one — with NO
// time bound, since a time-bounded fold let the transcript trigger's
// re-deliveries reset the attempt counter forever. Otherwise (resolved, or a
// different error) a fresh record is written.

// TestContractLimitRecordFreshRecord: a missing key is not an error, it is the
// fresh-record case — and every counter starts at its documented value.
func TestContractLimitRecordFreshRecord(t *testing.T) {
	j := contractLimitJob(t, useTestStore(t), "fresh")
	now := time.Unix(1_700_000_000, 0)

	got, err := RecordLimitHit(j, "rate_limit", "hit your session limit", "s1", "a1", now)
	if err != nil {
		t.Fatalf("RecordLimitHit on a missing key: %v", err)
	}
	want := LimitHit{
		Error:          "rate_limit",
		Details:        "hit your session limit",
		At:             now.Unix(),
		SessionID:      "s1",
		AgentID:        "a1",
		Deliveries:     1,
		LastDeliveryAt: now.Unix(),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fresh record = %+v, want %+v", got, want)
	}
	if read := ReadLimitHit(j); !reflect.DeepEqual(read, want) {
		t.Errorf("ReadLimitHit = %+v, want the record just written %+v", read, want)
	}
}

// TestContractLimitRecordFoldsTheTeammateHerd is the row's own worked example:
// every claude in the cell fires its own hook for ONE limit, seconds apart.
// The record must stay one hit — At, SessionID and AgentID pinned to the FIRST
// delivery, Deliveries counting the rest.
func TestContractLimitRecordFoldsTheTeammateHerd(t *testing.T) {
	j := contractLimitJob(t, useTestStore(t), "herd")
	first := time.Unix(1_700_000_000, 0)

	if _, err := RecordLimitHit(j, "rate_limit", "session limit", "zeus-sess", "", first); err != nil {
		t.Fatal(err)
	}
	// three teammates fire over the next 40 seconds, each with its own
	// session and agent id
	for i, d := range []time.Duration{5 * time.Second, 20 * time.Second, 40 * time.Second} {
		got, err := RecordLimitHit(j, "rate_limit", "session limit", "teammate-sess", "agent-h", first.Add(d))
		if err != nil {
			t.Fatalf("delivery %d: %v", i+2, err)
		}
		if got.Deliveries != i+2 {
			t.Errorf("delivery %d: Deliveries = %d, want %d", i+2, got.Deliveries, i+2)
		}
		if got.At != first.Unix() {
			t.Errorf("delivery %d: At = %d, want the first delivery's %d", i+2, got.At, first.Unix())
		}
		if got.SessionID != "zeus-sess" || got.AgentID != "" {
			t.Errorf("delivery %d: session/agent = %q/%q, want the first delivery's zeus-sess/\"\"", i+2, got.SessionID, got.AgentID)
		}
		if got.HandledAt != 0 {
			t.Errorf("delivery %d: HandledAt = %d, want 0 — a fold must not resolve the hit", i+2, got.HandledAt)
		}
	}
	if read := ReadLimitHit(j); read.Deliveries != 4 {
		t.Errorf("stored Deliveries = %d, want 4 — the fold did not persist", read.Deliveries)
	}
}

// TestContractLimitRecordFoldsLateSameErrorDeliveries: the fold is bounded by
// RESOLUTION, not time. A same-error delivery hours later — the transcript
// trigger re-reading a standing stall — is the same ongoing event, and folds
// onto the unresolved hit with its identity and counters intact; a fresh
// record here is what reset the attempt counter forever and kept the reset
// re-arm unreachable.
func TestContractLimitRecordFoldsLateSameErrorDeliveries(t *testing.T) {
	j := contractLimitJob(t, useTestStore(t), "window")
	first := time.Unix(1_700_000_000, 0)

	if _, err := RecordLimitHit(j, "rate_limit", "old", "s-old", "a-old", first); err != nil {
		t.Fatal(err)
	}
	later := first.Add(3 * time.Hour)
	got, err := RecordLimitHit(j, "rate_limit", "new", "s-new", "a-new", later)
	if err != nil {
		t.Fatal(err)
	}
	want := LimitHit{
		Error:          "rate_limit",
		Details:        "old",
		At:             first.Unix(),
		SessionID:      "s-old",
		AgentID:        "a-old",
		Deliveries:     2,
		LastDeliveryAt: later.Unix(),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("late same-error delivery = %+v, want a fold onto the unresolved hit %+v", got, want)
	}
}

// TestContractLimitRecordStartsFreshOnADifferentError: the fold is keyed on
// the error type too — a billing_error arriving inside a rate_limit's window
// is a different fact, and folding it would let the rescue act on a hit whose
// stored Error no longer describes what happened.
func TestContractLimitRecordStartsFreshOnADifferentError(t *testing.T) {
	j := contractLimitJob(t, useTestStore(t), "differr")
	first := time.Unix(1_700_000_000, 0)

	if _, err := RecordLimitHit(j, "rate_limit", "d1", "s1", "", first); err != nil {
		t.Fatal(err)
	}
	got, err := RecordLimitHit(j, "billing_error", "d2", "s2", "", first.Add(10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if got.Error != "billing_error" || got.Deliveries != 1 || got.At != first.Add(10*time.Second).Unix() {
		t.Fatalf("record = %+v, want a fresh billing_error record", got)
	}
}

// TestContractLimitRecordStartsFreshOnAResolvedRecord: the third leg of the
// fold condition. A record the rescue already resolved (HandledAt != 0) must
// not absorb the next limit — the cell would then sit on a handled hit forever
// while the account is exhausted again. The stored record is planted through
// the kv row directly, since the contract froze no writer for HandledAt in
// slice A.
func TestContractLimitRecordStartsFreshOnAResolvedRecord(t *testing.T) {
	st := useTestStore(t)
	j := contractLimitJob(t, st, "resolved")
	first := time.Unix(1_700_000_000, 0)

	resolved := LimitHit{
		Error:       "rate_limit",
		Details:     "old",
		At:          first.Unix(),
		SessionID:   "s-old",
		Deliveries:  3,
		HandledAt:   first.Add(30 * time.Second).Unix(),
		Outcome:     "rotated",
		Attempts:    1,
		LastAttempt: first.Unix(),
		SwappedAt:   first.Unix(),
		SwappedTo:   "someone@example.com",
	}
	raw, err := json.Marshal(resolved)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetKV(limitHitKey(j.ID), string(raw)); err != nil {
		t.Fatal(err)
	}
	// the planted record must read back intact first — this is also the
	// store-level round trip of every field, SwappedAt/SwappedTo included
	if read := ReadLimitHit(j); !reflect.DeepEqual(read, resolved) {
		t.Fatalf("ReadLimitHit = %+v, want the planted %+v", read, resolved)
	}

	// ...and a delivery well INSIDE the dedupe window still starts fresh,
	// because the stored record is resolved
	next := first.Add(20 * time.Second)
	got, err := RecordLimitHit(j, "rate_limit", "new", "s-new", "", next)
	if err != nil {
		t.Fatal(err)
	}
	want := LimitHit{
		Error:          "rate_limit",
		Details:        "new",
		At:             next.Unix(),
		SessionID:      "s-new",
		Deliveries:     1,
		LastDeliveryAt: next.Unix(),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("record after a resolved one = %+v, want %+v (every counter reset)", got, want)
	}
}

// TestContractLimitReadMissingKeyIsZero: "returns the stored record, or the
// zero value when the key is missing" — and never an error, since an
// unreadable record must mean "no hit", the direction in which the rescue does
// nothing.
func TestContractLimitReadMissingKeyIsZero(t *testing.T) {
	j := contractLimitJob(t, useTestStore(t), "never-hit")
	if got := ReadLimitHit(j); got != (LimitHit{}) {
		t.Errorf("ReadLimitHit on a missing key = %+v, want the zero value", got)
	}
}

// --- jobs.stallReason / RetryableHealth / IsUsageLimitReason ---

// TestContractLimitStallReasonUsageLimit pins the exact wording the whole
// routing chain is coupled by: stallReason produces it, IsUsageLimitReason
// recognises it, RetryableHealth refuses it.
func TestContractLimitStallReasonUsageLimit(t *testing.T) {
	st := core.Stall{Kind: "usage-limit", Text: "API Error: you've hit your session limit"}
	got := stallReason(st)
	want := "zeus hit a usage limit: " + st.Text
	if got != want {
		t.Fatalf("stallReason = %q, want %q", got, want)
	}
	if !IsUsageLimitReason(got) {
		t.Errorf("IsUsageLimitReason(%q) = false, want true — the daemon's health pass routes on this", got)
	}
	if RetryableHealth(got) {
		t.Errorf("RetryableHealth(%q) = true, want false — a nudge cannot fix an exhausted account", got)
	}
}

// TestContractLimitStallReasonOtherArmsUnchanged: "the api-error and default
// arms are unchanged", and RetryableHealth "must not be made true by the
// existing 'zeus hit an API error' prefix test" — which is only meaningful if
// that prefix test still works for the reason it exists.
func TestContractLimitStallReasonOtherArmsUnchanged(t *testing.T) {
	cases := []struct {
		name          string
		st            core.Stall
		wantEmpty     bool
		wantRetryable bool
		wantUsage     bool
	}{
		{"transient api error still retryable", core.Stall{Kind: "api-error", Text: "overloaded_error", Transient: true}, false, true, false},
		{"non-transient api error still not retryable", core.Stall{Kind: "api-error", Text: "credit balance is too low"}, false, false, false},
		{"interrupted still yields no reason", core.Stall{Kind: "interrupted"}, true, false, false},
		{"no stall still yields no reason", core.Stall{}, true, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reason := stallReason(c.st)
			if empty := reason == ""; empty != c.wantEmpty {
				t.Fatalf("stallReason(%+v) = %q, want empty=%v", c.st, reason, c.wantEmpty)
			}
			if got := RetryableHealth(reason); got != c.wantRetryable {
				t.Errorf("RetryableHealth(%q) = %v, want %v", reason, got, c.wantRetryable)
			}
			if got := IsUsageLimitReason(reason); got != c.wantUsage {
				t.Errorf("IsUsageLimitReason(%q) = %v, want %v", reason, got, c.wantUsage)
			}
		})
	}
}

// TestContractLimitIsUsageLimitReasonIsExact: "true EXACTLY for reasons
// stallReason produces from a usage-limit stall". A neighbouring reason that
// merely mentions a limit must not route into the rescue.
func TestContractLimitIsUsageLimitReasonIsExact(t *testing.T) {
	for _, reason := range []string{
		"",
		"zeus hit an API error: rate limit exceeded",
		"zeus needs attention: credit balance is too low — fix and retry by hand",
		"zeus process is gone — restart the cell",
	} {
		if IsUsageLimitReason(reason) {
			t.Errorf("IsUsageLimitReason(%q) = true, want false", reason)
		}
	}
	// ...and every usage-limit stall stallReason can produce is recognised,
	// including one whose own text happens to be empty
	for _, text := range []string{"", "usage limit", "You've hit your weekly limit · resets Monday"} {
		reason := stallReason(core.Stall{Kind: "usage-limit", Text: text})
		if !IsUsageLimitReason(reason) {
			t.Errorf("IsUsageLimitReason(%q) = false, want true", reason)
		}
	}
}

// --- jobs.limitRescueDecide: the ten-gate chain ---
//
// Contract row `jobs.limitRescueDecide`: the gate chain in its stated order,
// "and the order is itself an obligation because a later gate must never
// pre-empt an earlier one". Every case below either walks the chain to one
// outcome or pins one adjacency; the two orderings the contract calls out by
// name (manual never rotates; gate 5 sits ABOVE gate 6) get cases of their own.

func TestContractLimitRescueConstants(t *testing.T) {
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

func TestContractLimitRescueActionValues(t *testing.T) {
	cases := []struct {
		got  limitRescueAction
		want string
	}{
		{rescueSkip, "skip"},
		{rescueRotate, "rotate"},
		{rescueResume, "resume"},
		{rescueParkManual, "park-manual"},
		{rescueParkNoTarget, "park-no-target"},
	}
	for _, c := range cases {
		if string(c.got) != c.want {
			t.Errorf("action = %q, want %q", string(c.got), c.want)
		}
	}
}

// contractLimitNow is the fixed clock every decision case reasons against.
var contractLimitNow = time.Unix(1_700_000_000, 0)

// contractLimitRotatable is a hit that walks the whole chain to `rotate`: a
// fresh, unresolved rate_limit on a live cell in auto mode with a target
// available and no attempt yet made. Every case below is this value with the
// one field under test changed, so a case's outcome is attributable to that
// field alone.
func contractLimitRotatable() LimitHit {
	return LimitHit{
		Error:      "rate_limit",
		Details:    "you've hit your session limit",
		At:         contractLimitNow.Add(-30 * time.Second).Unix(),
		SessionID:  "zeus-sess",
		Deliveries: 3,
	}
}

func TestContractLimitRescueDecideChain(t *testing.T) {
	// helpers that mutate the baseline for one case
	with := func(f func(*LimitHit)) LimitHit {
		h := contractLimitRotatable()
		f(&h)
		return h
	}

	cases := []struct {
		name   string
		hit    LimitHit
		mode   string
		live   bool
		target bool
		want   limitRescueAction
	}{
		{
			// gate 10: the whole chain falls through to the rescue
			name: "clean rate_limit on a live auto cell rotates",
			hit:  contractLimitRotatable(), mode: "auto", live: true, target: true,
			want: rescueRotate,
		},
		{
			// gate 1 — and it must win even though everything else is set up
			// to rotate, since At == 0 IS "no hit recorded"
			name: "gate 1: no hit recorded",
			hit:  with(func(h *LimitHit) { h.At = 0 }), mode: "auto", live: true, target: true,
			want: rescueSkip,
		},
		{
			// gate 2: one limit produces one rescue, forever
			name: "gate 2: an already-handled hit never re-fires",
			hit:  with(func(h *LimitHit) { h.HandledAt = contractLimitNow.Add(-time.Minute).Unix() }),
			mode: "auto", live: true, target: true,
			want: rescueSkip,
		},
		{
			// gate 3: the route records EVERY delivery so a mapping change is
			// noticed, but only rate_limit is acted on
			name: "gate 3: a non-rate_limit error is recorded but not acted on",
			hit:  with(func(h *LimitHit) { h.Error = "authentication_failed" }),
			mode: "auto", live: true, target: true,
			want: rescueSkip,
		},
		{
			name: "gate 3: billing_error is not acted on either",
			hit:  with(func(h *LimitHit) { h.Error = "billing_error" }),
			mode: "auto", live: true, target: true,
			want: rescueSkip,
		},
		{
			// gate 4: nothing to rescue — the credential re-read only helps a
			// process that is still running
			name: "gate 4: a dead cell is not rescued",
			hit:  contractLimitRotatable(), mode: "auto", live: false, target: true,
			want: rescueSkip,
		},
		{
			// gate 5, settled: the swap landed and was recorded a settle
			// window ago, so this tick performs the resume
			name: "gate 5: a settled swap resumes",
			hit: with(func(h *LimitHit) {
				h.SwappedAt = contractLimitNow.Add(-limitRescueSettle).Unix()
				h.SwappedTo = "other@example.com"
				h.Attempts = 1
				h.LastAttempt = contractLimitNow.Add(-limitRescueSettle).Unix()
			}),
			mode: "auto", live: true, target: true,
			want: rescueResume,
		},
		{
			// gate 5, unsettled: the swap is too fresh, so the resume waits —
			// enforced across ticks, never by sleeping inside one
			name: "gate 5: an unsettled swap waits",
			hit: with(func(h *LimitHit) {
				h.SwappedAt = contractLimitNow.Add(-limitRescueSettle + time.Second).Unix()
				h.SwappedTo = "other@example.com"
				h.Attempts = 1
				h.LastAttempt = contractLimitNow.Unix()
			}),
			mode: "auto", live: true, target: true,
			want: rescueSkip,
		},
		{
			// gate 6: carl pinned the account by hand; the rescue says so and
			// stops rather than overriding him
			name: "gate 6: manual mode parks",
			hit:  contractLimitRotatable(), mode: "manual", live: true, target: true,
			want: rescueParkManual,
		},
		{
			// gate 7: three tries and the pool never produced a working
			// target — stop rather than retry forever
			name: "gate 7: attempts exhausted parks",
			hit: with(func(h *LimitHit) {
				h.Attempts = limitRescueMaxAttempts
				h.LastAttempt = contractLimitNow.Add(-time.Hour).Unix()
			}),
			mode: "auto", live: true, target: true,
			want: rescueParkNoTarget,
		},
		{
			// gate 8: backoff between rotate attempts
			name: "gate 8: inside the retry interval, wait",
			hit: with(func(h *LimitHit) {
				h.Attempts = 1
				h.LastAttempt = contractLimitNow.Add(-limitRescueRetryInterval + time.Minute).Unix()
			}),
			mode: "auto", live: true, target: true,
			want: rescueSkip,
		},
		{
			name: "gate 8: past the retry interval, rotate again",
			hit: with(func(h *LimitHit) {
				h.Attempts = 1
				h.LastAttempt = contractLimitNow.Add(-limitRescueRetryInterval).Unix()
			}),
			mode: "auto", live: true, target: true,
			want: rescueRotate,
		},
		{
			// gate 8's `Attempts > 0` conjunct: a first attempt is never
			// delayed, whatever a leftover LastAttempt says
			name: "gate 8: a zero attempt count is never backed off",
			hit: with(func(h *LimitHit) {
				h.Attempts = 0
				h.LastAttempt = contractLimitNow.Unix()
			}),
			mode: "auto", live: true, target: true,
			want: rescueRotate,
		},
		{
			// gate 9: the pool has nothing healthy to swap to
			name: "gate 9: no target parks",
			hit:  contractLimitRotatable(), mode: "auto", live: true, target: false,
			want: rescueParkNoTarget,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, reason := limitRescueDecide(c.hit, c.mode, c.live, c.target, contractLimitNow)
			if got != c.want {
				t.Fatalf("limitRescueDecide = %q (reason %q), want %q", got, reason, c.want)
			}
			// "the second return is a short human reason, non-empty for every
			// outcome except skip"
			if got != rescueSkip && strings.TrimSpace(reason) == "" {
				t.Errorf("outcome %q carries no reason — the board's blocked detail would render nothing", got)
			}
		})
	}
}

// TestContractLimitRescueDecideManualNeverRotates is the first of the two
// orderings the contract names as obligations: "a manual-mode job is never
// rotated whatever else is true (gate 6 sitting above every rotate path)". So
// no combination of the gates BELOW 6 may produce a rotate in manual mode.
func TestContractLimitRescueDecideManualNeverRotates(t *testing.T) {
	base := contractLimitRotatable()
	variants := map[string]LimitHit{
		"no attempts yet":     base,
		"attempts exhausted":  func() LimitHit { h := base; h.Attempts = limitRescueMaxAttempts; return h }(),
		"inside the backoff":  func() LimitHit { h := base; h.Attempts = 1; h.LastAttempt = contractLimitNow.Unix(); return h }(),
		"past the backoff":    func() LimitHit { h := base; h.Attempts = 1; h.LastAttempt = 0; return h }(),
		"many deliveries":     func() LimitHit { h := base; h.Deliveries = 40; return h }(),
		"an ancient open hit": func() LimitHit { h := base; h.At = contractLimitNow.Add(-72 * time.Hour).Unix(); return h }(),
	}
	for name, hit := range variants {
		for _, target := range []bool{true, false} {
			got, _ := limitRescueDecide(hit, "manual", true, target, contractLimitNow)
			if got == rescueRotate {
				t.Errorf("%s (target=%v): decided rotate in manual mode — carl pinned the account", name, target)
			}
			if got != rescueParkManual {
				t.Errorf("%s (target=%v): decided %q, want park-manual", name, target, got)
			}
		}
	}
}

// TestContractLimitRescueDecideSwapOutranksManual is the second named
// ordering: "gate 5 sits ABOVE gate 6 because once a swap has landed the resume
// must complete even if the mode changed in between — a swapped cell left
// un-resumed is worse than either outcome on its own". It also has to outrank
// the two gates below 6, so an exhausted attempt count or an active backoff
// cannot swallow the resume either.
func TestContractLimitRescueDecideSwapOutranksManual(t *testing.T) {
	settled := contractLimitRotatable()
	settled.SwappedAt = contractLimitNow.Add(-limitRescueSettle).Unix()
	settled.SwappedTo = "other@example.com"

	t.Run("manual mode set after the swap still resumes", func(t *testing.T) {
		got, _ := limitRescueDecide(settled, "manual", true, true, contractLimitNow)
		if got != rescueResume {
			t.Fatalf("decided %q, want resume — a swapped cell must not be left un-resumed", got)
		}
	})
	t.Run("exhausted attempts still resume", func(t *testing.T) {
		h := settled
		h.Attempts = limitRescueMaxAttempts
		got, _ := limitRescueDecide(h, "auto", true, true, contractLimitNow)
		if got != rescueResume {
			t.Fatalf("decided %q, want resume", got)
		}
	})
	t.Run("an active backoff still resumes", func(t *testing.T) {
		h := settled
		h.Attempts = 1
		h.LastAttempt = contractLimitNow.Unix()
		got, _ := limitRescueDecide(h, "auto", true, true, contractLimitNow)
		if got != rescueResume {
			t.Fatalf("decided %q, want resume", got)
		}
	})
	t.Run("no target left in the pool still resumes", func(t *testing.T) {
		got, _ := limitRescueDecide(settled, "auto", true, false, contractLimitNow)
		if got != rescueResume {
			t.Fatalf("decided %q, want resume — the swap already happened, the pool no longer matters", got)
		}
	})
}

// TestContractLimitRescueDecideEarlyGatesOutrankLaterOnes walks the remaining
// adjacencies the chain claims, each stated as "the earlier gate's outcome
// wins even though a later gate would fire too".
func TestContractLimitRescueDecideEarlyGatesOutrankLaterOnes(t *testing.T) {
	swapped := func(h LimitHit) LimitHit {
		h.SwappedAt = contractLimitNow.Add(-limitRescueSettle).Unix()
		h.SwappedTo = "other@example.com"
		return h
	}
	cases := []struct {
		name   string
		hit    LimitHit
		mode   string
		live   bool
		target bool
		want   limitRescueAction
	}{
		{
			// gate 1 above gate 5: a zero record has no swap to complete
			name: "no hit outranks a settled swap",
			hit:  swapped(LimitHit{Error: "rate_limit"}), mode: "auto", live: true, target: true,
			want: rescueSkip,
		},
		{
			// gate 2 above gate 5: the resume already ran
			name: "handled outranks a settled swap",
			hit: func() LimitHit {
				h := swapped(contractLimitRotatable())
				h.HandledAt = contractLimitNow.Unix()
				return h
			}(), mode: "auto", live: true, target: true,
			want: rescueSkip,
		},
		{
			// gate 4 above gate 5: resuming a dead cell reaches nothing
			name: "a dead cell outranks a settled swap",
			hit:  swapped(contractLimitRotatable()), mode: "auto", live: false, target: true,
			want: rescueSkip,
		},
		{
			// gate 6 above gate 7: manual parks as MANUAL, and the reason
			// carl reads must name the pin rather than the pool
			name: "manual outranks exhausted attempts",
			hit: func() LimitHit {
				h := contractLimitRotatable()
				h.Attempts = limitRescueMaxAttempts
				return h
			}(), mode: "manual", live: true, target: false,
			want: rescueParkManual,
		},
		{
			// gate 7 above gate 8: a hit that has run out of attempts parks
			// rather than sitting in a backoff that will never end
			name: "exhausted attempts outrank the backoff",
			hit: func() LimitHit {
				h := contractLimitRotatable()
				h.Attempts = limitRescueMaxAttempts
				h.LastAttempt = contractLimitNow.Unix()
				return h
			}(), mode: "auto", live: true, target: true,
			want: rescueParkNoTarget,
		},
		{
			// gate 8 above gate 9: inside the backoff nothing is decided at
			// all, so an empty pool does not consume an attempt
			name: "the backoff outranks a missing target",
			hit: func() LimitHit {
				h := contractLimitRotatable()
				h.Attempts = 1
				h.LastAttempt = contractLimitNow.Unix()
				return h
			}(), mode: "auto", live: true, target: false,
			want: rescueSkip,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _ := limitRescueDecide(c.hit, c.mode, c.live, c.target, contractLimitNow)
			if got != c.want {
				t.Fatalf("limitRescueDecide = %q, want %q", got, c.want)
			}
		})
	}
}

// TestContractLimitRescueDecideIsPure: "no store, no tmux, no clock of its
// own". The clock half is the observable one — the same inputs at two
// different `now` values on either side of the settle boundary must differ,
// and the same inputs at the SAME now must be identical however many times it
// is called.
func TestContractLimitRescueDecideIsPure(t *testing.T) {
	hit := contractLimitRotatable()
	first, r1 := limitRescueDecide(hit, "auto", true, true, contractLimitNow)
	for i := 0; i < 5; i++ {
		got, r := limitRescueDecide(hit, "auto", true, true, contractLimitNow)
		if got != first || r != r1 {
			t.Fatalf("call %d = (%q, %q), want the stable (%q, %q)", i, got, r, first, r1)
		}
	}
	// and the input hit is not mutated by the decision
	if !reflect.DeepEqual(hit, contractLimitRotatable()) {
		t.Errorf("hit mutated to %+v — the decision takes a value, not a handle", hit)
	}
}

// --- jobs.cellAgentPanes ---
//
// There is no `jobs.cellAgentPanes` row in the contract — v1's row said only
// that `nudgeCellAgents` "targets exactly cellAgentPanes(j, all)", and the
// enumeration itself is interrupt.go's own documented definition:
// same-session depth-0 claude/codex panes, plus zeus' pane and its tmux
// teammates, minus live side-thread panes. These cases pin THAT, which is
// where the definition actually lives. The map's KEY is frozen by nothing,
// so every assertion here reads the VALUES' panes.
//
// v2 note: the rescue no longer reads this set at all — it resumes zeus
// alone, since a teammate has no session id to resume — so what remains
// below covers InterruptCell's Stop targets, its only non-test caller.

// contractLimitPaneSet lists the panes of every peer in a cellAgentPanes
// result, sorted — key-agnostic by design.
func contractLimitPaneSet(m map[string]*core.Peer) []string {
	out := make([]string, 0, len(m))
	for _, p := range m {
		if p == nil {
			continue
		}
		out = append(out, p.Pane)
	}
	sort.Strings(out)
	return out
}

func TestContractLimitCellAgentPanesEnumeration(t *testing.T) {
	j := &Job{Slug: "panes"}
	sess := CellSession(j)

	zeus := &core.Peer{TmuxSession: sess, Agent: "claude", Depth: 0, Pane: "%1", Name: ZeusName(j), SessionID: "zeus-sess"}
	// a tmux teammate hangs off zeus rather than appearing in `all`
	zeus.Teammates = []*core.Peer{
		{Pane: "%5", Agent: "claude", Name: "hephaestus-slice-a"},
		{Pane: "", Agent: "claude", Name: "in-process-teammate"}, // no pane: nothing to address
	}
	all := []*core.Peer{
		zeus,
		{TmuxSession: sess, Agent: "claude", Depth: 0, Pane: "%2"},          // another cell claude
		{TmuxSession: sess, Agent: "codex", Depth: 0, Pane: "%3"},           // codex counts too
		{TmuxSession: sess, Agent: "claude", Depth: 1, Pane: "%2"},          // nested subagent: same pane
		{TmuxSession: "other", Agent: "claude", Depth: 0, Pane: "%4"},       // another cell entirely
		{TmuxSession: sess, Agent: "", Role: "shell", Depth: 0, Pane: "%6"}, // a shell pane
		{TmuxSession: sess, Agent: "claude", Depth: 0, Pane: ""},            // no pane to reach
	}

	got := contractLimitPaneSet(cellAgentPanes(j, all))
	want := []string{"%1", "%2", "%3", "%5"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("panes = %v, want %v", got, want)
	}
}

// TestContractLimitCellAgentPanesDedupesByPane: a nested subagent shares its
// lead's pane, so one pane must appear exactly once — Stop's Escape is sent
// per pane, and sending it twice interrupts that pane twice.
func TestContractLimitCellAgentPanesDedupesByPane(t *testing.T) {
	j := &Job{Slug: "dedupe"}
	sess := CellSession(j)
	zeus := &core.Peer{TmuxSession: sess, Agent: "claude", Depth: 0, Pane: "%1", Name: ZeusName(j)}
	zeus.Teammates = []*core.Peer{{Pane: "%1", Agent: "claude", Name: "nested"}}
	all := []*core.Peer{
		zeus,
		{TmuxSession: sess, Agent: "claude", Depth: 0, Pane: "%1"},
		{TmuxSession: sess, Agent: "claude", Depth: 2, Pane: "%1"},
	}
	m := cellAgentPanes(j, all)
	if len(m) != 1 {
		t.Fatalf("got %d entries for one pane: %v", len(m), contractLimitPaneSet(m))
	}
}

// TestContractLimitCellAgentPanesExcludesLiveThreads: "a side thread is still
// never included, since a thread must never be disturbed by the job it hangs
// off". A thread's window lives in the cell's own tmux session, so only the
// recorded thread row can exclude it.
func TestContractLimitCellAgentPanesExcludesLiveThreads(t *testing.T) {
	st := useTestStore(t)
	row := mustCreateJob(t, st, store.Job{Slug: "threaded", Repo: "kraken", State: "working"})
	j := &Job{ID: row.ID, Slug: row.Slug}
	if err := st.AddThread(store.Thread{JobID: j.ID, TID: "t1", SessionID: "thread-sess", Pane: "%9", State: "live"}); err != nil {
		t.Fatal(err)
	}
	sess := CellSession(j)
	all := []*core.Peer{
		{TmuxSession: sess, Agent: "claude", Depth: 0, Pane: "%1", Name: ZeusName(j), SessionID: "zeus-sess"},
		{TmuxSession: sess, Agent: "claude", Depth: 0, Pane: "%9", Name: ThreadAgentName(j, "t1"), SessionID: "thread-sess"},
	}
	got := contractLimitPaneSet(cellAgentPanes(j, all))
	want := []string{"%1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("panes = %v, want %v — the live side thread was swept up", got, want)
	}
}

// --- jobs.rescueLimitedCell: what an empty pool answers ---
//
// The lock-precedes-selection ordering test lived here and retired with Q2:
// rescueLimitedCell takes no cross-process rotation flock any more, because
// it changes no machine-wide credential — it serializes on the binder's own
// in-process mutex against the other select-then-stamp, a spawn-time bind.
// A blocking mutex has no "busy" outcome to assert an ordering against.
//
// What remains is the second step's own outcome, and it is safe to run for
// the same reason it always was: HOME is sandboxed, so the pool is empty and
// there is nothing to bind to.

// TestContractLimitRescueNoTargetOutcome: an empty pool yields "no-target",
// no error, no rebind.
func TestContractLimitRescueNoTargetOutcome(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	j := &Job{Slug: "no-target"}
	to, outcome, err := rescueLimitedCell(j, contractLimitNow)
	if err != nil {
		t.Fatalf("err = %v, want nil — an empty pool is a decision, not a failure", err)
	}
	if outcome != "no-target" {
		t.Fatalf("outcome = %q, want %q", outcome, "no-target")
	}
	if to != "" {
		t.Errorf("toEmail = %q, want empty", to)
	}
}

// --- jobs.LimitHook: the transport ---
//
// Contract row `jobs.LimitHook`: read one JSON payload from stdin, POST
// {slug, error, details, session_id, agent_id} to
// ServeBaseURL()+"/api/limit-hit". Never calls os.Exit and never opens
// kraken.db. The failure outcomes (unparseable payload, POST failure,
// non-200, empty error type) are a sibling author's; this is the delivery
// itself.

// TestContractLimitHookPostsTheDelivery drives the verb the way Claude Code
// does — a payload on stdin — against an httptest stand-in for kraken serve,
// and asserts the five keys land on the wire under their frozen names. The
// hook is the ONLY thing that carries a limit out of the cell, so a body whose
// keys the route cannot read is a feature that never fires.
func TestContractLimitHookPostsTheDelivery(t *testing.T) {
	var gotPath, gotMethod string
	var gotBody []byte
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		gotPath, gotMethod = r.URL.Path, r.Method
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", strings.TrimPrefix(srv.URL, "http://"))

	payload := `{"hook_event_name":"StopFailure","error":"rate_limit","error_details":"You've hit your session limit","session_id":"zeus-sess","agent_id":"agent-3","cwd":"/tmp"}`
	withStdin(t, payload, func() { LimitHook("herd-slug") })

	if hits != 1 {
		t.Fatalf("want exactly 1 POST, got %d", hits)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/limit-hit" {
		t.Fatalf("%s %s, want POST /api/limit-hit", gotMethod, gotPath)
	}
	var req map[string]string
	if err := json.Unmarshal(gotBody, &req); err != nil {
		t.Fatalf("posted body is not a flat JSON object: %v\n%s", err, gotBody)
	}
	want := map[string]string{
		"slug":       "herd-slug",
		"error":      "rate_limit",
		"details":    "You've hit your session limit",
		"session_id": "zeus-sess",
		"agent_id":   "agent-3",
	}
	for k, v := range want {
		if req[k] != v {
			t.Errorf("posted %q = %q, want %q (full body %s)", k, req[k], v, gotBody)
		}
	}
}

// TestContractLimitHookPostsTheDocumentedFieldNames is the drift-tolerance
// half seen from the transport: a payload spelled the way the published docs
// describe it — `error_type`/`error_message`, plus an `api_error` object that
// must be ignored — still reaches the route with the same five keys.
func TestContractLimitHookPostsTheDocumentedFieldNames(t *testing.T) {
	var gotBody []byte
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", strings.TrimPrefix(srv.URL, "http://"))

	payload := `{"hook_event_name":"StopFailure","error_type":"rate_limit","error_message":"usage limit reached","api_error":{"type":"rate_limit_error"},"session_id":"s9"}`
	withStdin(t, payload, func() { LimitHook("docs-slug") })

	if hits != 1 {
		t.Fatalf("want exactly 1 POST, got %d", hits)
	}
	var req map[string]string
	if err := json.Unmarshal(gotBody, &req); err != nil {
		t.Fatal(err)
	}
	if req["error"] != "rate_limit" || req["details"] != "usage limit reached" || req["session_id"] != "s9" {
		t.Errorf("posted body = %s, want the documented spellings mapped onto error/details/session_id", gotBody)
	}
}

// --- Amendment 4: jobs.limitRescueApply, the write-back's reader ---
//
// Contract, Amendment 4 row `jobs.limitRescueApply`:
//   func(hit LimitHit, action limitRescueAction, outcome, toEmail string, now time.Time) LimitHit
// Pure — takes the record and what just happened, returns the record to
// store. This is the symbol both test authors asked for in round 1, so the
// arms below are the ones that previously had no assertion anywhere.
//
// Dimension split for this batch: the NORMAL arms are mine (skip,
// rotate/"rebound", resume, park-manual). The rotate-with-a-failing-outcome
// arm and park-no-target's increment-then-test threshold are boundary/refusal
// work and belong to the sibling author.

// contractLimitApplyBase is a hit mid-flight: unresolved, one rotate attempt
// already spent, no swap landed yet. Every arm below starts from this so a
// field that changes when it should not is visible.
func contractLimitApplyBase() LimitHit {
	return LimitHit{
		Error:       "rate_limit",
		Details:     "you've hit your session limit",
		At:          contractLimitNow.Add(-5 * time.Minute).Unix(),
		SessionID:   "zeus-sess",
		AgentID:     "agent-1",
		Deliveries:  6,
		Attempts:    1,
		LastAttempt: contractLimitNow.Add(-11 * time.Minute).Unix(),
	}
}

// contractLimitApplyIdentityHeld fails the test if any of the five fields
// that describe WHICH limit this is were disturbed. No arm of the write-back
// may touch them: they are the hit's identity, written once by
// RecordLimitHit.
func contractLimitApplyIdentityHeld(t *testing.T, got, want LimitHit) {
	t.Helper()
	if got.Error != want.Error || got.Details != want.Details || got.At != want.At ||
		got.SessionID != want.SessionID || got.AgentID != want.AgentID || got.Deliveries != want.Deliveries {
		t.Errorf("the hit's identity changed:\n got  %+v\n want %+v", got, want)
	}
}

// TestContractLimitApplySkipReturnsHitUnchanged: "rescueSkip returns hit
// unchanged". A skip is the decision that nothing happened, so a write-back
// that altered anything would advance a counter for a tick in which no rotate
// was attempted — inventing an attempt out of a quiet tick.
func TestContractLimitApplySkipReturnsHitUnchanged(t *testing.T) {
	shapes := map[string]LimitHit{
		"mid-flight":  contractLimitApplyBase(),
		"zero record": {},
		"already swapped": func() LimitHit {
			h := contractLimitApplyBase()
			h.SwappedAt = contractLimitNow.Add(-time.Minute).Unix()
			h.SwappedTo = "other@example.com"
			return h
		}(),
		"already resolved": func() LimitHit {
			h := contractLimitApplyBase()
			h.HandledAt = contractLimitNow.Add(-time.Minute).Unix()
			h.Outcome = "rotated"
			return h
		}(),
	}
	for name, hit := range shapes {
		t.Run(name, func(t *testing.T) {
			// the outcome/toEmail arguments carry leftovers from a call that
			// never happened; a skip must ignore both rather than record them
			got := limitRescueApply(hit, rescueSkip, "rebound", "somebody@example.com", contractLimitNow)
			if !reflect.DeepEqual(got, hit) {
				t.Fatalf("skip changed the record:\n got  %+v\n want %+v", got, hit)
			}
		})
	}
}

// TestContractLimitApplyRotateSwapped is the first half of the two-phase
// design: "sets SwappedAt = now, SwappedTo = toEmail, increments Attempts,
// sets LastAttempt = now, and leaves HandledAt zero — the resume on a later
// tick is what resolves the hit". The HandledAt-stays-zero clause is the
// load-bearing one: resolve here and the resume never runs, leaving a cell
// rebound onto a fresh account and still sitting on a dead turn.
//
// The outcome WORD is "rebound" since Q2 — the rescue rebinds one job row
// rather than swapping the machine's login — but the two stamped fields
// still carry the old names, because they mean what they always meant: when
// the cell's account moved, and to whom.
func TestContractLimitApplyRotateSwapped(t *testing.T) {
	base := contractLimitApplyBase()
	got := limitRescueApply(base, rescueRotate, "rebound", "rescued@example.com", contractLimitNow)

	if got.SwappedAt != contractLimitNow.Unix() {
		t.Errorf("SwappedAt = %d, want %d", got.SwappedAt, contractLimitNow.Unix())
	}
	if got.SwappedTo != "rescued@example.com" {
		t.Errorf("SwappedTo = %q, want the target's email", got.SwappedTo)
	}
	if got.Attempts != base.Attempts+1 {
		t.Errorf("Attempts = %d, want %d (incremented)", got.Attempts, base.Attempts+1)
	}
	if got.LastAttempt != contractLimitNow.Unix() {
		t.Errorf("LastAttempt = %d, want %d", got.LastAttempt, contractLimitNow.Unix())
	}
	if got.HandledAt != 0 {
		t.Errorf("HandledAt = %d, want 0 — the swap does not resolve the hit, the resume does", got.HandledAt)
	}
	contractLimitApplyIdentityHeld(t, got, base)
}

// TestContractLimitApplyResumeResolves is the second half: "rescueResume sets
// HandledAt = now and Outcome = 'rotated' on any outcome that is not the
// 'no-agents' literal". Asserted across three plausible resume outcomes
// precisely because the row says the resolution does not depend on them —
// re-running the swap would be wrong either way.
func TestContractLimitApplyResumeResolves(t *testing.T) {
	base := contractLimitApplyBase()
	base.SwappedAt = contractLimitNow.Add(-limitRescueSettle).Unix()
	base.SwappedTo = "rescued@example.com"
	base.Attempts = 1
	base.LastAttempt = base.SwappedAt

	for _, outcome := range []string{"resumed", "partial", ""} {
		t.Run("outcome="+outcome, func(t *testing.T) {
			got := limitRescueApply(base, rescueResume, outcome, "", contractLimitNow)
			if got.HandledAt != contractLimitNow.Unix() {
				t.Errorf("HandledAt = %d, want %d — the hit must be resolved", got.HandledAt, contractLimitNow.Unix())
			}
			if got.Outcome != "rotated" {
				t.Errorf("Outcome = %q, want %q", got.Outcome, "rotated")
			}
			// the swap watermark is history now, not state to clear, and the
			// attempt counters describe rotates rather than resumes
			if got.SwappedAt != base.SwappedAt || got.SwappedTo != base.SwappedTo {
				t.Errorf("resume disturbed the swap watermark: %d/%q, want %d/%q", got.SwappedAt, got.SwappedTo, base.SwappedAt, base.SwappedTo)
			}
			if got.Attempts != base.Attempts || got.LastAttempt != base.LastAttempt {
				t.Errorf("resume moved a rotate counter: Attempts %d LastAttempt %d, want %d / %d", got.Attempts, got.LastAttempt, base.Attempts, base.LastAttempt)
			}
			contractLimitApplyIdentityHeld(t, got, base)
		})
	}
}

// TestContractLimitApplyParkManualTouchesNoCounter: "sets HandledAt = now and
// Outcome = 'parked-manual' and touches NO counter — carl pinned the account
// deliberately and there is nothing to retry". Burning an attempt here would
// silently spend the budget a later auto-mode rescue depends on.
func TestContractLimitApplyParkManualTouchesNoCounter(t *testing.T) {
	base := contractLimitApplyBase()
	got := limitRescueApply(base, rescueParkManual, "", "", contractLimitNow)

	if got.HandledAt != contractLimitNow.Unix() {
		t.Errorf("HandledAt = %d, want %d", got.HandledAt, contractLimitNow.Unix())
	}
	if got.Outcome != "parked-manual" {
		t.Errorf("Outcome = %q, want %q", got.Outcome, "parked-manual")
	}
	if got.Attempts != base.Attempts {
		t.Errorf("Attempts = %d, want %d — a manual park is not an attempt", got.Attempts, base.Attempts)
	}
	if got.LastAttempt != base.LastAttempt {
		t.Errorf("LastAttempt = %d, want %d unchanged", got.LastAttempt, base.LastAttempt)
	}
	if got.SwappedAt != 0 || got.SwappedTo != "" {
		t.Errorf("park-manual invented a swap watermark: %d/%q", got.SwappedAt, got.SwappedTo)
	}
	contractLimitApplyIdentityHeld(t, got, base)
}

// TestContractLimitApplyIsPure: "Pure — no store, no tmux, no clock". The
// observable halves are that the argument is not mutated (it is passed by
// value, so an implementation reaching through a pointer field would show
// here) and that repeated calls at one `now` are identical.
func TestContractLimitApplyIsPure(t *testing.T) {
	for _, action := range []limitRescueAction{rescueSkip, rescueRotate, rescueResume, rescueParkManual} {
		t.Run(string(action), func(t *testing.T) {
			hit := contractLimitApplyBase()
			first := limitRescueApply(hit, action, "rebound", "x@example.com", contractLimitNow)
			if !reflect.DeepEqual(hit, contractLimitApplyBase()) {
				t.Fatalf("the argument was mutated to %+v", hit)
			}
			for i := 0; i < 3; i++ {
				again := limitRescueApply(hit, action, "rebound", "x@example.com", contractLimitNow)
				if !reflect.DeepEqual(again, first) {
					t.Fatalf("call %d = %+v, want the stable %+v", i, again, first)
				}
			}
		})
	}
}

// TestContractLimitApplyTwoPhaseSequence walks the whole rescue through the
// two pure functions, which is the only place the design's central claim is
// visible end to end: a swap on one tick, a resume on a LATER one, and the hit
// resolved exactly once. The middle step is the real assertion — immediately
// after the swap the decision must still be `skip`, because the credential has
// not propagated yet.
func TestContractLimitApplyTwoPhaseSequence(t *testing.T) {
	tick1 := contractLimitNow
	hit := contractLimitApplyBase()
	hit.Attempts, hit.LastAttempt = 0, 0

	// tick 1: the decision rotates, the rebind lands, the record remembers it
	action, _ := limitRescueDecide(hit, "auto", true, true, tick1)
	if action != rescueRotate {
		t.Fatalf("tick 1 decided %q, want rotate", action)
	}
	hit = limitRescueApply(hit, action, "rebound", "rescued@example.com", tick1)
	if hit.HandledAt != 0 {
		t.Fatalf("the hit resolved on the rebind tick: %+v", hit)
	}

	// tick 2, inside the settle window: nothing may happen yet
	tick2 := tick1.Add(limitRescueSettle - time.Second)
	action, _ = limitRescueDecide(hit, "auto", true, true, tick2)
	if action != rescueSkip {
		t.Fatalf("tick 2 (inside the settle window) decided %q, want skip", action)
	}
	if got := limitRescueApply(hit, action, "", "", tick2); !reflect.DeepEqual(got, hit) {
		t.Fatalf("the skip tick changed the record: %+v", got)
	}

	// tick 3, past it: the resume runs and resolves the hit
	tick3 := tick1.Add(limitRescueSettle)
	action, _ = limitRescueDecide(hit, "auto", true, true, tick3)
	if action != rescueResume {
		t.Fatalf("tick 3 decided %q, want resume", action)
	}
	hit = limitRescueApply(hit, action, "resumed", "", tick3)
	if hit.HandledAt != tick3.Unix() || hit.Outcome != "rotated" {
		t.Fatalf("after the resume: %+v, want HandledAt=%d Outcome=rotated", hit, tick3.Unix())
	}
	if hit.SwappedTo != "rescued@example.com" {
		t.Errorf("SwappedTo = %q, want the swap remembered across the two ticks", hit.SwappedTo)
	}

	// tick 4: a resolved hit is inert forever after
	action, _ = limitRescueDecide(hit, "auto", true, true, tick3.Add(time.Hour))
	if action != rescueSkip {
		t.Fatalf("tick 4 decided %q on a resolved hit, want skip", action)
	}
}

// --- Amendment 1: the timestamps are unix SECONDS ---
//
// Contract, Amendment 1: "Every int64 timestamp on LimitHit — At, HandledAt,
// LastAttempt, SwappedAt — is unix SECONDS (now.Unix(), read back with
// time.Unix(v, 0))." In round 1 this was unstated and I guessed seconds; the
// guess happened to match, so nothing I wrote would have caught a move to
// millis. These two tests exist so something would.

// contractLimitSecondsCeiling is a magnitude a unix SECONDS timestamp cannot
// reach until the year 5138, and that a unix MILLIS timestamp passed years
// ago: 1e11. It is what makes the assertion below fail LOUDLY on a units
// change rather than merely comparing a value against itself.
const contractLimitSecondsCeiling int64 = 100_000_000_000

func TestContractLimitTimestampsAreUnixSeconds(t *testing.T) {
	j := contractLimitJob(t, useTestStore(t), "units")
	now := contractLimitNow

	// At, via the record writer
	rec, err := RecordLimitHit(j, "rate_limit", "d", "s", "", now)
	if err != nil {
		t.Fatal(err)
	}
	// SwappedAt + LastAttempt, via the rotate arm
	rec = limitRescueApply(rec, rescueRotate, "rebound", "x@example.com", now)
	// HandledAt, via the resume arm
	rec = limitRescueApply(rec, rescueResume, "resumed", "", now)

	for _, f := range []struct {
		name string
		got  int64
	}{
		{"At", rec.At},
		{"LastAttempt", rec.LastAttempt},
		{"SwappedAt", rec.SwappedAt},
		{"HandledAt", rec.HandledAt},
	} {
		if f.got != now.Unix() {
			t.Errorf("%s = %d, want now.Unix() = %d", f.name, f.got, now.Unix())
		}
		// the units guard: a millis stamp is ~1.7e12 and trips this
		if f.got >= contractLimitSecondsCeiling {
			t.Errorf("%s = %d is too large to be unix seconds — the record moved to millis", f.name, f.got)
		}
		// and it must read back through time.Unix(v, 0), the accessor the
		// gate chain itself uses
		if !time.Unix(f.got, 0).Equal(now) {
			t.Errorf("time.Unix(%s, 0) = %v, want %v", f.name, time.Unix(f.got, 0), now)
		}
	}
}

// TestContractLimitSettleWindowSurvivesTheUnitRoundTrip is Amendment 1's own
// stated consequence, asserted between the WRITER and the READER rather than
// against a literal: a SwappedAt stamped by limitRescueApply at t must leave
// gate 5 unsatisfied at t + settle - 1s and satisfied at t + settle. Units
// that disagree between the two halves collapse the settle window — which is
// the entire reason the rescue is two-phase — while every other test stays
// green.
func TestContractLimitSettleWindowSurvivesTheUnitRoundTrip(t *testing.T) {
	swapped := limitRescueApply(contractLimitApplyBase(), rescueRotate, "rebound", "x@example.com", contractLimitNow)

	if got, _ := limitRescueDecide(swapped, "auto", true, true, contractLimitNow); got != rescueSkip {
		t.Errorf("at the rebind instant the decision is %q, want skip — the settle window is not being observed", got)
	}
	if got, _ := limitRescueDecide(swapped, "auto", true, true, contractLimitNow.Add(limitRescueSettle-time.Second)); got != rescueSkip {
		t.Errorf("one second short of the settle window the decision is %q, want skip", got)
	}
	if got, _ := limitRescueDecide(swapped, "auto", true, true, contractLimitNow.Add(limitRescueSettle)); got != rescueResume {
		t.Errorf("at the settle boundary the decision is %q, want resume", got)
	}
}

// --- Amendment 2: what a fold preserves ---

// TestContractLimitRecordFoldPreservesTheSwapWatermark: "RecordLimitHit's fold
// branch changes Deliveries and NOTHING else — explicitly including SwappedAt
// and SwappedTo". The consequence the amendment names is the one asserted
// here: a late teammate delivery landing inside the dedupe window AFTER a swap
// must not zero the watermark, or the next tick sends an already-swapped cell
// around for a second swap instead of the resume it was owed.
func TestContractLimitRecordFoldPreservesTheSwapWatermark(t *testing.T) {
	st := useTestStore(t)
	j := contractLimitJob(t, st, "late")
	first := contractLimitNow

	swapped := LimitHit{
		Error:       "rate_limit",
		Details:     "you've hit your session limit",
		At:          first.Unix(),
		SessionID:   "zeus-sess",
		AgentID:     "",
		Deliveries:  3,
		Attempts:    1,
		LastAttempt: first.Unix(),
		SwappedAt:   first.Unix(),
		SwappedTo:   "rescued@example.com",
	}
	raw, err := json.Marshal(swapped)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetKV(limitHitKey(j.ID), string(raw)); err != nil {
		t.Fatal(err)
	}

	// a straggler teammate fires 30s later — inside the dedupe window, and
	// after the swap has already landed
	got, err := RecordLimitHit(j, "rate_limit", "different text", "teammate-sess", "agent-9", first.Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	want := swapped
	want.Deliveries = swapped.Deliveries + 1
	want.LastDeliveryAt = first.Add(30 * time.Second).Unix()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("the fold changed more than Deliveries and the delivery stamp:\n got  %+v\n want %+v", got, want)
	}

	// ...and the decision that follows is still the resume it was owed, not a
	// second rotate
	if action, _ := limitRescueDecide(got, "auto", true, true, first.Add(time.Minute)); action != rescueResume {
		t.Fatalf("after the fold the decision is %q, want resume — the swap watermark was lost", action)
	}
}

// --- Amendment 10: mergeLimitRescueState, the three arms ---
//
// Contract, Amendment 10 row `jobs.mergeLimitRescueState`:
//   func(cur, hit LimitHit) (LimitHit, bool)
// Pure. Re-reads under the write and copies only the SIX rescue-owned fields
// onto the stored record, so a delivery that landed while the pass was
// working is neither clobbered nor allowed to clobber. This replaced the
// "save before resuming" reorder, which Amendment 10 shows is mutually
// exclusive with Amendment 6's guard — the resume count is not knowable until
// the sends return, so the write has to stay after them and the race is
// closed here instead.

// contractLimitMergeStored is the record as the STORE holds it: the rescue's
// own fields as they were a swap ago, plus a Deliveries increment and fresh
// identity text that landed from the API goroutine in between.
func contractLimitMergeStored() LimitHit {
	return LimitHit{
		Error:      "rate_limit",
		Details:    "a later delivery's message",
		At:         contractLimitNow.Add(-time.Minute).Unix(),
		SessionID:  "zeus-sess",
		AgentID:    "agent-1",
		Deliveries: 9, // the herd kept arriving while the pass was swapping
	}
}

// contractLimitMergeOurs is the copy the pass has been working on: the same
// hit (same At), read before those deliveries landed, now carrying the swap
// it just performed.
func contractLimitMergeOurs() LimitHit {
	h := contractLimitMergeStored()
	h.Details = "the message this pass read"
	h.Deliveries = 3
	h.Attempts = 1
	h.LastAttempt = contractLimitNow.Unix()
	h.SwappedAt = contractLimitNow.Unix()
	h.SwappedTo = "rescued@example.com"
	return h
}

// TestContractLimitMergeSameHitTakesOnlyTheRescueFields is the middle arm and
// the one the amendment is really about: the two sides own disjoint halves of
// one row. The rescue owns Attempts, LastAttempt, SwappedAt, SwappedTo,
// HandledAt and Outcome; the delivery side owns Deliveries and the identity
// text. A merge that took the whole of either side loses one of them — and
// losing SwappedAt is the expensive direction, since gate 5 then never fires
// and the cell is swapped a second time.
func TestContractLimitMergeSameHitTakesOnlyTheRescueFields(t *testing.T) {
	cur, ours := contractLimitMergeStored(), contractLimitMergeOurs()
	got, ok := mergeLimitRescueState(cur, ours)
	if !ok {
		t.Fatal("ok = false for two views of the SAME hit — the write would be refused for nothing")
	}

	// the six rescue-owned fields come from our copy
	if got.Attempts != ours.Attempts || got.LastAttempt != ours.LastAttempt {
		t.Errorf("attempt counters = %d/%d, want this pass' %d/%d", got.Attempts, got.LastAttempt, ours.Attempts, ours.LastAttempt)
	}
	if got.SwappedAt != ours.SwappedAt || got.SwappedTo != ours.SwappedTo {
		t.Errorf("swap watermark = %d/%q, want %d/%q — a lost SwappedAt is a second credential swap", got.SwappedAt, got.SwappedTo, ours.SwappedAt, ours.SwappedTo)
	}
	if got.HandledAt != ours.HandledAt || got.Outcome != ours.Outcome {
		t.Errorf("resolution = %d/%q, want %d/%q", got.HandledAt, got.Outcome, ours.HandledAt, ours.Outcome)
	}
	// ...and everything else stays as the STORE has it
	if got.Deliveries != cur.Deliveries {
		t.Errorf("Deliveries = %d, want the stored %d — the increment that landed mid-rescue was erased", got.Deliveries, cur.Deliveries)
	}
	if got.Details != cur.Details || got.SessionID != cur.SessionID || got.AgentID != cur.AgentID || got.Error != cur.Error || got.At != cur.At {
		t.Errorf("identity fields came from this pass' stale copy: %+v, want the stored %+v", got, cur)
	}
}

// TestContractLimitMergeResolutionSurvives is the same arm exercised on the
// resume write specifically, because that is the write Amendment 10 could not
// move earlier: the respawn has to return before the outcome is known, so a
// delivery has a real window to land first. The resolution must still reach
// the store, or the hit stays open and the next tick SIGKILLs a zeus that
// was already resumed seconds ago.
func TestContractLimitMergeResolutionSurvives(t *testing.T) {
	cur := contractLimitMergeStored()
	cur.Deliveries = 12
	ours := contractLimitMergeOurs()
	ours.HandledAt = contractLimitNow.Unix()
	ours.Outcome = "rotated"

	got, ok := mergeLimitRescueState(cur, ours)
	if !ok {
		t.Fatal("ok = false")
	}
	if got.HandledAt != ours.HandledAt || got.Outcome != "rotated" {
		t.Fatalf("resolution lost: %+v", got)
	}
	if got.Deliveries != 12 {
		t.Errorf("Deliveries = %d, want 12", got.Deliveries)
	}
}

// TestContractLimitMergeMissingRowWritesOursWholesale is the first arm: "a
// stored record that reads as zero (row missing, or JSON that no longer
// parses) is the one case where our copy is written wholesale — it is the
// only readable version of the record left". Refusing here would throw away
// a swap that really happened.
func TestContractLimitMergeMissingRowWritesOursWholesale(t *testing.T) {
	ours := contractLimitMergeOurs()
	got, ok := mergeLimitRescueState(LimitHit{}, ours)
	if !ok {
		t.Fatal("ok = false for a missing row — the only surviving copy of the record would be dropped")
	}
	if !reflect.DeepEqual(got, ours) {
		t.Fatalf("merge = %+v, want our copy verbatim %+v", got, ours)
	}
}

// TestContractLimitMergeDifferentHitRefuses is the third arm, and it is the
// one that closes the race: "ok is false when the stored record is a DIFFERENT
// hit (a fresh At) ... our counters describe an event the store has already
// moved past, and stamping them onto the new one would tell a brand-new limit
// it had already been swapped". That is precisely the sequence the resume
// opens — the crew resumes, the new account is also exhausted, a hook starts a
// fresh hit — and a merge here would mark that fresh hit as already rescued.
func TestContractLimitMergeDifferentHitRefuses(t *testing.T) {
	ours := contractLimitMergeOurs()
	fresh := LimitHit{
		Error:      "rate_limit",
		Details:    "the NEW account is exhausted too",
		At:         contractLimitNow.Add(time.Second).Unix(), // a different hit
		SessionID:  "teammate-sess",
		Deliveries: 1,
	}

	got, ok := mergeLimitRescueState(fresh, ours)
	if ok {
		t.Fatal("ok = true across two different hits — this pass' counters would be stamped onto a brand-new limit")
	}
	if !reflect.DeepEqual(got, fresh) {
		t.Fatalf("merge = %+v, want the stored record untouched %+v", got, fresh)
	}
	// the fresh hit must still look unrescued to the decision, or it is
	// silently skipped forever
	if action, _ := limitRescueDecide(got, "auto", true, true, contractLimitNow.Add(time.Minute)); action != rescueRotate {
		t.Fatalf("the fresh hit decides %q, want rotate — the refused merge leaked into it", action)
	}
}

// TestContractLimitMergeIsPure: neither argument is mutated, and the answer is
// stable. mergeLimitRescueState takes both records by value and runs under the
// write, so an implementation that reached through and edited its input would
// corrupt the caller's own copy mid-save.
func TestContractLimitMergeIsPure(t *testing.T) {
	cur, ours := contractLimitMergeStored(), contractLimitMergeOurs()
	first, firstOK := mergeLimitRescueState(cur, ours)
	if !reflect.DeepEqual(cur, contractLimitMergeStored()) || !reflect.DeepEqual(ours, contractLimitMergeOurs()) {
		t.Fatalf("an argument was mutated: cur=%+v ours=%+v", cur, ours)
	}
	for i := 0; i < 3; i++ {
		again, againOK := mergeLimitRescueState(cur, ours)
		if !reflect.DeepEqual(again, first) || againOK != firstOK {
			t.Fatalf("call %d = (%+v, %v), want the stable (%+v, %v)", i, again, againOK, first, firstOK)
		}
	}
}

// --- Amendment 9: the parser degrades per FIELD, not per payload ---

// TestContractLimitParsePayloadDegradesPerField is the positive half of
// Amendment 9, and it is the one that keeps the feature switched on: a
// payload carrying a perfectly readable `rate_limit` must still parse when
// some OTHER field has drifted to a non-string shape. Decoded into a struct
// of strings, encoding/json fails the whole payload on any one drifted
// field, which turns the entire rescue off silently the day Claude Code gives
// `error_details` a structure.
func TestContractLimitParsePayloadDegradesPerField(t *testing.T) {
	cases := []struct {
		name        string
		raw         string
		wantDetails string
		wantSession string
	}{
		{
			name: "error_details drifts to an object",
			raw:  `{"error":"rate_limit","error_details":{"code":429,"text":"session limit"},"session_id":"s1"}`,
			// the drifted field alone degrades to "" — the error type and
			// every other readable field survive
			wantSession: "s1",
		},
		{
			name:        "session_id drifts to a number",
			raw:         `{"error":"rate_limit","error_details":"session limit","session_id":12345}`,
			wantDetails: "session limit",
		},
		{
			name:        "agent_id drifts to an array and effort is an object",
			raw:         `{"error":"rate_limit","agent_id":["a","b"],"effort":{"level":"high"},"session_id":"s2"}`,
			wantSession: "s2",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			errType, details, sessionID, _, ok := parseLimitPayload([]byte(c.raw))
			if !ok {
				t.Fatalf("ok = false — one drifted field failed the whole payload: %s", c.raw)
			}
			if errType != "rate_limit" {
				t.Errorf("errorType = %q, want rate_limit", errType)
			}
			if details != c.wantDetails {
				t.Errorf("details = %q, want %q", details, c.wantDetails)
			}
			if sessionID != c.wantSession {
				t.Errorf("sessionID = %q, want %q", sessionID, c.wantSession)
			}
		})
	}
}

// TestContractLimitHookTruncatesDetailsBeforePosting is Amendment 9's second
// half: LimitHook truncates `details` through core.TruncateOneLine at 200
// runes BEFORE posting, because the route caps the request body at 64 KiB and
// a large payload would otherwise come back a 400 — losing the limit entirely
// rather than losing the message.
// contractLimitBigStdin is withStdin's large-payload sibling. The shared
// helper writes the whole payload into the pipe BEFORE fn runs, which
// deadlocks the moment the payload exceeds the OS pipe buffer (64 KiB here) —
// and a payload that big is exactly what this obligation is about. Writing
// from a goroutine lets the reader drain as the writer fills.
func contractLimitBigStdin(t *testing.T, data string, fn func()) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.WriteString(w, data)
		w.Close()
	}()
	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old; r.Close(); <-done }()
	fn()
}

func TestContractLimitHookTruncatesDetailsBeforePosting(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", strings.TrimPrefix(srv.URL, "http://"))

	long := "You've hit your session limit\n" + strings.Repeat("padding text ", 8000) // ~96 KiB
	payload, err := json.Marshal(map[string]string{"error": "rate_limit", "error_details": long, "session_id": "s"})
	if err != nil {
		t.Fatal(err)
	}
	contractLimitBigStdin(t, string(payload), func() { LimitHook("big") })

	var req map[string]string
	if err := json.Unmarshal(gotBody, &req); err != nil {
		t.Fatalf("posted body did not decode: %v", err)
	}
	if want := core.TruncateOneLine(long, 200); req["details"] != want {
		t.Errorf("posted details = %q, want %q", req["details"], want)
	}
	if len(gotBody) >= 1<<16 {
		t.Errorf("posted body is %d bytes — the route's 64 KiB cap would 400 it and the limit would be lost", len(gotBody))
	}
	if req["error"] != "rate_limit" {
		t.Errorf("posted error = %q, want rate_limit", req["error"])
	}
}

// --- Retired with Q2: the "swapped" outcome, through rotation.go's seams ---
//
// Four tests here drove rescueLimitedCell all the way through the rotation
// selector and a real machine-wide swap: the success path and its returned
// email, the "must not consult NeedsRotation" sentinel, the lock-busy
// no-seam-touched ordering, and the swap-failure propagation. None of them
// describes the rescue any more — it rebinds ONE job row through
// selectRescueBind → decideBind → core.SelectBindTarget, calls
// core.SwapMainTo nowhere, and takes no flock. The outcome set they pinned
// ("swapped", "lock-busy", "swap-failed") is now "rebound", "no-target",
// "bind-failed", and the surviving no-target case is asserted above.

// TestContractLimitApplyResumeNormalPathSurvivesAmendment11 re-states, against
// the amended two-path arm, what this file already asserted about the
// resolving path: any outcome that is not the failure sentinel resolves as
// "rotated" and touches no counter. Amendment 11 added a second path keyed on
// the outcome argument, so the assertion is now also that the normal path is
// the DEFAULT — a future third sentinel must not silently capture "".
func TestContractLimitApplyResumeNormalPathSurvivesAmendment11(t *testing.T) {
	base := contractLimitApplyBase()
	base.SwappedAt = contractLimitNow.Add(-limitRescueSettle).Unix()
	base.SwappedTo = "rescued@example.com"

	for _, outcome := range []string{"", "resumed", "partial", "3 of 4"} {
		t.Run("outcome="+outcome, func(t *testing.T) {
			got := limitRescueApply(base, rescueResume, outcome, "", contractLimitNow)
			if got.HandledAt != contractLimitNow.Unix() || got.Outcome != "rotated" {
				t.Fatalf("got HandledAt=%d Outcome=%q, want %d/rotated", got.HandledAt, got.Outcome, contractLimitNow.Unix())
			}
			if got.Attempts != base.Attempts || got.LastAttempt != base.LastAttempt {
				t.Errorf("counters moved: %d/%d, want %d/%d — a reached resume is not an attempt", got.Attempts, got.LastAttempt, base.Attempts, base.LastAttempt)
			}
		})
	}
}

// --- Amendment 11: a supersession keeps the swap watermark ---

// TestContractLimitRecordCarriesTheSwapForwardOnSupersession covers the half
// of Amendment 11 nothing else does. A delivery arriving PAST the dedupe
// window on a hit that is swapped-but-unresolved writes a fresh record, and a
// fresh record that zeroed SwappedAt would re-arm gate 10 with Attempts back
// at zero — so the attempt cap is bypassed rather than exceeded and the pool
// drains one account per cycle for as long as the tmux session outlives its
// agents. The fresh branch therefore carries SwappedAt/SwappedTo forward, and
// gate 5 keeps ownership across the supersession.
//
// This is the fold rule's third case rather than a boundary: the record is
// replaced, and the question is which fields survive the replacement.
func TestContractLimitRecordCarriesTheSwapForwardOnSupersession(t *testing.T) {
	st := useTestStore(t)
	j := contractLimitJob(t, st, "supersede")
	first := contractLimitNow

	swappedOpen := LimitHit{
		Error:       "rate_limit",
		Details:     "the original limit",
		At:          first.Unix(),
		SessionID:   "zeus-sess",
		Deliveries:  4,
		Attempts:    1,
		LastAttempt: first.Unix(),
		SwappedAt:   first.Unix(),
		SwappedTo:   "rescued@example.com",
		// HandledAt stays zero: swapped, not yet resumed
	}
	raw, err := json.Marshal(swappedOpen)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetKV(limitHitKey(j.ID), string(raw)); err != nil {
		t.Fatal(err)
	}

	// a same-error delivery now folds however late it lands, so the fresh
	// branch is reached through a DIFFERENT error type superseding the
	// unresolved swapped hit, then a rate_limit superseding that
	later := first.Add(3 * time.Minute)
	if _, err := RecordLimitHit(j, "billing_error", "a different failure", "mid-sess", "", later); err != nil {
		t.Fatal(err)
	}
	got, err := RecordLimitHit(j, "rate_limit", "a later limit", "other-sess", "", later.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if got.At != later.Add(time.Minute).Unix() || got.Deliveries != 1 || got.Attempts != 0 {
		t.Fatalf("record = %+v, want a fresh record with the attempt counters reset", got)
	}
	if got.SwappedAt != swappedOpen.SwappedAt || got.SwappedTo != swappedOpen.SwappedTo {
		t.Fatalf("swap watermark = %d/%q, want the superseded record's %d/%q carried forward",
			got.SwappedAt, got.SwappedTo, swappedOpen.SwappedAt, swappedOpen.SwappedTo)
	}

	// ...and the consequence: the decision is still the resume that swap is
	// owed, not a second rotate. Without the carry-forward this reads
	// `rotate` with Attempts at zero, which is the uncapped-swapping cycle.
	if action, _ := limitRescueDecide(got, "auto", true, true, later.Add(limitRescueSettle)); action != rescueResume {
		t.Fatalf("after the supersession the decision is %q, want resume — the swap watermark was lost", action)
	}
}

// TestContractLimitRecordDoesNotCarryAResolvedSwapForward is the other side of
// the same condition (`cur.SwappedAt != 0 && cur.HandledAt == 0`): once a hit
// has been resolved, its swap is history and must NOT be inherited. Inherited,
// the new hit would come up already looking swapped and would go straight to a
// resume without ever rotating — a fresh limit answered by a restart onto the
// same exhausted account instead of by a new one.
func TestContractLimitRecordDoesNotCarryAResolvedSwapForward(t *testing.T) {
	st := useTestStore(t)
	j := contractLimitJob(t, st, "supersede-resolved")
	first := contractLimitNow

	resolved := LimitHit{
		Error:      "rate_limit",
		At:         first.Unix(),
		Deliveries: 4,
		Attempts:   1,
		HandledAt:  first.Add(time.Minute).Unix(),
		Outcome:    "rotated",
		SwappedAt:  first.Unix(),
		SwappedTo:  "rescued@example.com",
	}
	raw, err := json.Marshal(resolved)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetKV(limitHitKey(j.ID), string(raw)); err != nil {
		t.Fatal(err)
	}

	got, err := RecordLimitHit(j, "rate_limit", "a new limit", "s", "", first.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if got.SwappedAt != 0 || got.SwappedTo != "" {
		t.Fatalf("swap watermark = %d/%q, want zero — a resolved hit's swap is history", got.SwappedAt, got.SwappedTo)
	}
	if action, _ := limitRescueDecide(got, "auto", true, true, first.Add(time.Hour)); action != rescueRotate {
		t.Fatalf("the new hit decides %q, want rotate — it inherited a swap it never had", action)
	}
}

// --- saveLimitRescueState: the merge reaches the store ---

// TestContractLimitSaveMergesUnderAFreshRead is the integration of the two
// halves this file tests separately: the pass' write goes through
// mergeLimitRescueState against whatever is stored NOW, not against the copy
// it read a swap ago. Nothing else exercises saveLimitRescueState at all.
//
// The sequence is the real one: the pass reads the hit, spends real time in
// SwapMainTo, and a straggler delivery lands in between.
func TestContractLimitSaveMergesUnderAFreshRead(t *testing.T) {
	st := useTestStore(t)
	j := contractLimitJob(t, st, "save-merge")
	now := contractLimitNow

	// the pass reads the hit...
	opened, err := RecordLimitHit(j, "rate_limit", "session limit", "zeus-sess", "", now)
	if err != nil {
		t.Fatal(err)
	}
	// ...a straggler teammate's hook lands while the swap is in flight...
	if _, err := RecordLimitHit(j, "rate_limit", "session limit", "teammate-sess", "a2", now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	// ...and only then does the pass write back what it computed from its
	// own, now-stale, copy
	swapped := limitRescueApply(opened, rescueRotate, "rebound", "rescued@example.com", now.Add(3*time.Second))
	if err := saveLimitRescueState(j, swapped); err != nil {
		t.Fatalf("saveLimitRescueState: %v", err)
	}

	stored := ReadLimitHit(j)
	if stored.Deliveries != 2 {
		t.Errorf("Deliveries = %d, want 2 — the straggler's increment was clobbered by the stale copy", stored.Deliveries)
	}
	if stored.SwappedAt != now.Add(3*time.Second).Unix() || stored.SwappedTo != "rescued@example.com" {
		t.Errorf("swap watermark = %d/%q, want the rescue's own", stored.SwappedAt, stored.SwappedTo)
	}
	if stored.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1", stored.Attempts)
	}
	if stored.HandledAt != 0 {
		t.Errorf("HandledAt = %d, want 0 — a swap does not resolve", stored.HandledAt)
	}
}

// TestContractLimitSaveRefusesADifferentHit: when the stored record is a
// DIFFERENT hit, the write is abandoned and reported rather than stamped. The
// store must be left exactly as the newer hit wrote it, or this rescue's
// counters would tell a brand-new limit it had already been swapped.
func TestContractLimitSaveRefusesADifferentHit(t *testing.T) {
	st := useTestStore(t)
	j := contractLimitJob(t, st, "save-refuse")
	now := contractLimitNow

	opened, err := RecordLimitHit(j, "rate_limit", "the first limit", "zeus-sess", "", now)
	if err != nil {
		t.Fatal(err)
	}
	// a different-error delivery replaces the record with a new hit (a
	// same-error one would fold onto the unresolved record)
	fresh, err := RecordLimitHit(j, "billing_error", "a different failure", "other-sess", "", now.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}

	stale := limitRescueApply(opened, rescueResume, "", "", now.Add(time.Second))
	if err := saveLimitRescueState(j, stale); err == nil {
		t.Fatal("saveLimitRescueState returned nil — a write against a superseded hit must be reported, not silently applied")
	}
	stored := ReadLimitHit(j)
	if stored.At != fresh.At || stored.HandledAt != 0 || stored.Outcome != "" {
		t.Fatalf("stored = %+v, want the newer hit untouched (At=%d, unresolved)", stored, fresh.At)
	}
}
