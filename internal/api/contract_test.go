package api

// contract_test.go transcribes ios/API.md into executable wire-shape
// assertions: whenever a JSON shape served to or accepted from the iOS
// app/web UI changes, update this file, ios/API.md, and
// ios/Kraken/Models.swift together (see the "API contract" rule in
// CLAUDE.md).

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"kraken/internal/core"
	"kraken/internal/jobs"
	"kraken/internal/store"
	"kraken/internal/util"
)

// Endpoints deliberately NOT shape-tested here, and why:
//   - /api/board's HTTP handler itself (webBoard's marshaled shape is
//     covered directly; wiring it through discovery/daemon state would drag
//     in tmux/process-table dependencies this file avoids).
//   - webAction-wrapped endpoints (/api/queue — the submit, /api/done,
//     /api/retry, /api/kill, /api/archive, /api/unarchive, /api/delete,
//     /api/question, /api/steer, /api/reactivate, /api/stop; /api/dequeue and
//     /api/start were removed in v6 and are unregistered):
//     all share the trivial {"ok": true} success shape or {"error": string}
//     failure shape, already covered by the generic error-response
//     convention documented in ios/API.md.
//   - /api/reorder's success/failure shapes are the same {"ok":true} /
//     {"error": string} convention; only its distinct request shape
//     (reorderReq) gets its own spec below.
//   - POST /api/threads' success body: {"ok": true} for four of the five ops
//     (the shared convention above), plus a `tid` string on a `branch` — the
//     one wire-visible key here with no assertion, because reaching branch's
//     success path needs a real cell, a forkable zeus, and a real tmux
//     window. Its refusal shapes ARE covered (threads_test.go).
//   - /api/peers/* action endpoints (spawn, send, kill, capture): need real
//     tmux panes/sessions to reach their success path. /api/peers/open is the
//     exception — its openPeerFunc test seam makes the success path
//     reachable with no tmux, so it gets its own spec below.
//   - /api/job/create and /api/job/save: exercised for behavior elsewhere
//     (web_test.go) but not given their own assertShape spec here since they
//     mirror /api/job/raw's shape exactly (see ios/API.md).
//   - webJob.artifacts' ORDER. ios/API.md documents the array as sorted by
//     name, and both clients page through it with next/prev rather than
//     re-sorting, so it is a real wire guarantee — but ordering is not a
//     wire SHAPE, and fieldSpec/assertShape have no way to express it. It is
//     pinned instead by TestJobArtifactsStoreModeUnionIsGloballySorted and
//     TestJobArtifactsLiveCellIsSorted (artifacts_storemode_test.go), which
//     cover the union and live-cell producers respectively. Named here so an
//     audit of API.md against this file doesn't read a documented guarantee
//     as an untested one.
//
// wireKind is the JSON scalar/container kind a wire field is expected to
// decode as, checked against a map[string]any built with
// json.Decoder.UseNumber() so numbers are distinguishable from strings.
type wireKind int

const (
	wireNumber wireKind = iota
	wireString
	wireBool
	wireArray
	wireObject
)

func (k wireKind) String() string {
	switch k {
	case wireNumber:
		return "number"
	case wireString:
		return "string"
	case wireBool:
		return "bool"
	case wireArray:
		return "array"
	case wireObject:
		return "object"
	default:
		return "unknown"
	}
}

func matchesKind(v any, k wireKind) bool {
	switch k {
	case wireNumber:
		_, ok := v.(json.Number)
		return ok
	case wireString:
		_, ok := v.(string)
		return ok
	case wireBool:
		_, ok := v.(bool)
		return ok
	case wireArray:
		_, ok := v.([]any)
		return ok
	case wireObject:
		_, ok := v.(map[string]any)
		return ok
	default:
		return false
	}
}

// fieldSpec is one expected key in a wire-shape spec: required distinguishes
// an always-present key from an omitempty one (may be entirely absent);
// nullable additionally allows an explicit JSON null even when the key is
// present (Go nil slices/pointers marshal that way despite omitempty not
// applying, e.g. a nil []string field with no omitempty tag).
type fieldSpec struct {
	kind     wireKind
	required bool
	nullable bool
}

// assertShape decodes data as a JSON object (via UseNumber so mtime-style
// fields read as json.Number rather than float64) and checks it against
// spec: every required key must be present and correctly typed, every
// present optional key must be correctly typed (or null if nullable), and
// no undocumented extra key may appear — the last check is what makes this
// fail the moment a served shape drifts from ios/API.md, not just when a
// key it already knows about goes missing.
func assertShape(t *testing.T, label string, data []byte, spec map[string]fieldSpec) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		t.Fatalf("%s: not a JSON object: %v (body: %s)", label, err, data)
	}
	for key, fs := range spec {
		v, present := m[key]
		if !present {
			if fs.required {
				t.Errorf("%s: missing required key %q", label, key)
			}
			continue
		}
		if v == nil {
			if !fs.nullable {
				t.Errorf("%s: key %q is null, want %s", label, key, fs.kind)
			}
			continue
		}
		if !matchesKind(v, fs.kind) {
			t.Errorf("%s: key %q = %#v, want %s", label, key, v, fs.kind)
		}
	}
	for key := range m {
		if _, ok := spec[key]; !ok {
			t.Errorf("%s: undocumented extra key %q (update ios/API.md + this spec)", label, key)
		}
	}
}

// --- board: webBoard / webJob / jobs.DaemonStatus (web.go, daemon.go) ---

var daemonStatusSpec = map[string]fieldSpec{
	"pid":        {kind: wireNumber, required: true},
	"started_at": {kind: wireNumber, required: true},
	"last_tick":  {kind: wireNumber, required: true},
	"last_error": {kind: wireString, required: false},
	"cells":      {kind: wireNumber, required: true},
	"stale":      {kind: wireBool, required: false},
}

var webBoardSpec = map[string]fieldSpec{
	"daemon":     {kind: wireObject, required: true},
	"daemon_ok":  {kind: wireBool, required: true},
	"cell_cap":   {kind: wireNumber, required: true},
	"live_cells": {kind: wireNumber, required: true},
	"jobs":       {kind: wireArray, required: true},
	"contract":   {kind: wireNumber, required: true},
	"stale":      {kind: wireBool, required: true},
	"update":     {kind: wireObject, required: true},
	"ship_ios":   {kind: wireObject, required: true},
	// The current local week's and today's goals (goals.go). Required rather
	// than omitempty: webBoard.Goals is a struct value, so the key always
	// rides, and a board with no goals written yet answers with two empty
	// arrays rather than by going silent. Additive — a key ADDED to an
	// existing shape, which old clients ignore — so it never bumps
	// apiContract.
	"goals": {kind: wireObject, required: true},
	// The board's optional background image (background.go): the updated_at
	// stamp alone, pointer + omitempty, so ABSENT is the wire's only spelling
	// of "no background set". Additive — never bumps apiContract.
	"background": {kind: wireObject, required: false},
}

// webBackgroundSpec covers background.go's webBackground — webBoard's
// "background" key when a background is set. One field, no omitempty: once
// the key rides, the SPA reads updated_at unconditionally (it is the image
// URL's cache-buster).
var webBackgroundSpec = map[string]fieldSpec{
	"updated_at": {kind: wireNumber, required: true},
}

// webUpdateSpec covers update.go's webUpdate — served both as webBoard's
// "update" key and as the whole body of GET /api/update/status, from one
// assembly function (currentUpdate) so the two shapes can't drift. No
// omitempty anywhere: the UI reads every field unconditionally, and an
// absent key would be indistinguishable from a zeroed one.
var webUpdateSpec = map[string]fieldSpec{
	"phase":      {kind: wireString, required: true},
	"from_sha":   {kind: wireString, required: true},
	"to_sha":     {kind: wireString, required: true},
	"behind":     {kind: wireNumber, required: true},
	"error":      {kind: wireString, required: true},
	"started_at": {kind: wireNumber, required: true},
	"updated_at": {kind: wireNumber, required: true},
	"build_sha":  {kind: wireString, required: true},
	"checked_at": {kind: wireNumber, required: true},
	"restarted":  {kind: wireBool, required: true},
}

// updateReqSpec / updateStartRespSpec cover POST /api/update's request body
// and its 202 response (update.go's updateReq / updateResp). The response
// needs its own spec even though update_test.go already round-trips the 202:
// that round-trip unmarshals into `updateResp` itself, so renaming a json tag
// renames both sides at once and stays green here while the phone — whose
// UpdateStartResponse (Models.swift) decodes all three keys as
// non-optionals — throws a decode error on every successful start. Pinning
// the wire names in a spec is the only thing that can catch that.
var updateReqSpec = map[string]fieldSpec{
	"force": {kind: wireBool, required: true},
	"test":  {kind: wireBool, required: true},
}

var updateStartRespSpec = map[string]fieldSpec{
	"ok":       {kind: wireBool, required: true},
	"phase":    {kind: wireString, required: true},
	"from_sha": {kind: wireString, required: true},
}

// webShipIOSSpec covers shipios.go's webShipIOS — served both as webBoard's
// "ship_ios" key and as the whole body of GET /api/ship-ios/status, from one
// assembly function (currentShipIOS) so the two shapes can't drift. No
// omitempty anywhere, for the same reason webUpdate has none: the clients
// read every field unconditionally, and an absent key would be
// indistinguishable from a zeroed one.
var webShipIOSSpec = map[string]fieldSpec{
	"phase":      {kind: wireString, required: true},
	"build":      {kind: wireNumber, required: true},
	"error":      {kind: wireString, required: true},
	"started_at": {kind: wireNumber, required: true},
	"updated_at": {kind: wireNumber, required: true},
}

// shipIOSStartRespSpec covers POST /api/ship-ios' 202 body (shipios.go's
// shipIOSResp). It gets its own spec for the lesson the update button
// taught: shipios_test.go round-trips the 202 by unmarshaling into
// `shipIOSResp` itself, so renaming a json tag renames both sides at once
// and stays green in Go while the phone — whose ShipIOSStartResponse
// (Models.swift) decodes both keys as non-optionals — throws on every
// successful start. Pinning the wire names here is the only thing that
// catches that. There is no request spec: shipIOSReq is deliberately empty
// (see its comment).
var shipIOSStartRespSpec = map[string]fieldSpec{
	"ok":    {kind: wireBool, required: true},
	"phase": {kind: wireString, required: true},
}

var webJobSpec = map[string]fieldSpec{
	"slug":    {kind: wireString, required: true},
	"repo":    {kind: wireString, required: true},
	"state":   {kind: wireString, required: true},
	"err":     {kind: wireString, required: false},
	"outcome": {kind: wireString, required: false},
	"color":   {kind: wireString, required: false},
	"cell":    {kind: wireString, required: false},
	"live":    {kind: wireBool, required: true},
	// no "prereqs" and no "start_error": both left this shape in v6, and this
	// map is what the shape suite enforces — assertShape rejects a key the
	// map does not name, so their ABSENCE here is the pin. A documented-but-
	// optional entry for a key nothing emits would let either one return
	// silently.
	"artifacts":   {kind: wireArray, required: false},
	"detail":      {kind: wireString, required: false},
	"question":    {kind: wireObject, required: false},
	"zeus_status": {kind: wireString, required: false},
	// the pipeline skill zeus most recently used (info/aim/do/pr/dream/
	// graph), read off zeus' transcript; started non-done jobs with a
	// resolvable transcript only, and omitted until a pipeline skill has
	// actually been used. Additive, no contract bump.
	"stage":         {kind: wireString, required: false},
	"wakeup":        {kind: wireObject, required: false},
	"roster":        {kind: wireArray, required: false},
	"health":        {kind: wireString, required: false},
	"mtime":         {kind: wireNumber, required: true},
	"archived":      {kind: wireBool, required: false},
	"display_slug":  {kind: wireString, required: false},
	"cell_gone":     {kind: wireBool, required: false},
	"reactivatable": {kind: wireBool, required: false},
	"last_sent":     {kind: wireString, required: false},
	"prs":           {kind: wireArray, required: false},
	"doc_questions": {kind: wireArray, required: false},
	"feedback":      {kind: wireArray, required: false},
	"threads":       {kind: wireArray, required: false},
	"tokens":        {kind: wireObject, required: false},
	// zeus' unblock/scope triage estimate, omitempty. Absent covers THREE
	// states the wire deliberately does not separate — never estimated;
	// CLEARED (a clear zeroes load_at, and webJobLoadFrom serves no object for
	// a zero at); and ARCHIVED (archivedWebJob withholds the key even when the
	// row holds a real estimate, pinned by TestArchivedWebJobWithholdsLoad).
	// The one thing absent never means is "estimated, and the numbers were
	// zero". That pair is not refused — it IS the clear, accepted with a 200
	// and written to the row as four zeroes (TestJobLoadPostSetsAndClears) —
	// it is simply never recorded AS an estimate: the clear branch zeroes
	// load_at along with the numbers, so webJobLoadFrom serves no object for
	// it. What the API does refuse with a 400 is a HALF pair, exactly one of
	// the two zero. Additive: a key ADDED to an existing
	// shape, which old clients ignore, so it never bumps apiContract. Listed
	// here because assertShape rejects any key the spec doesn't document — an
	// addition that skips this map fails the suite rather than sliding
	// through undocumented, which is the whole point of transcribing
	// ios/API.md.
	"load": {kind: wireObject, required: false},
	// Every goal this job serves (goals.go), in link order, omitempty — absent
	// means the job links none, which is every job until carl labels one and
	// every job whose goals were deleted (schema v27's ON DELETE CASCADE takes
	// the link rows with them rather than dangling them). Present exactly when
	// at least one link resolves, whatever period a goal belongs to and whether
	// or not it is done: the link is carl's statement about what the job is FOR,
	// so it must keep rendering after the week rolls over. Additive, so it never
	// bumps apiContract; listed here because assertShape rejects any key the
	// spec doesn't document.
	"goals": {kind: wireArray, required: false},
	// The job's PRIMARY goal — `goals[0]`, always, built from the same slice —
	// retained because every shipped iOS build reads it. Present under exactly
	// the same condition as "goals": the two appear and vanish together.
	"goal": {kind: wireObject, required: false},
}

// webTokensSpec covers board.go's webTokens, webJob's "tokens" object — one
// `job_tokens` row (store.JobTokens). Every key is required: the type carries
// no omitempty anywhere, because a measured zero is a real answer and must
// still ride the wire. Absence is expressed by the "tokens" key itself being
// gone (webJobSpec above marks it optional), never by a missing counter.
// The four zeus keys are the LEAD agent's share of the four totals above them
// — a subset of each, never a sibling — and they are required for the same
// reason the totals are: no omitempty anywhere on the type, so a zero must
// still ride. Subagent spend is `in - zeus_in` and so on and has no key here
// on purpose; a spec entry for one would be the first sign somebody had added
// a second stored half that could disagree with the total.
var webTokensSpec = map[string]fieldSpec{
	"in":               {kind: wireNumber, required: true},
	"out":              {kind: wireNumber, required: true},
	"cache_write":      {kind: wireNumber, required: true},
	"cache_read":       {kind: wireNumber, required: true},
	"sessions":         {kind: wireNumber, required: true},
	"updated_at":       {kind: wireNumber, required: true},
	"zeus_in":          {kind: wireNumber, required: true},
	"zeus_out":         {kind: wireNumber, required: true},
	"zeus_cache_write": {kind: wireNumber, required: true},
	"zeus_cache_read":  {kind: wireNumber, required: true},
}

func TestWebTokensWireShape(t *testing.T) {
	// a genuinely-zero row: every key must still be present, which is the
	// difference between "measured, and it was zero" and no tokens key at all
	data, err := json.Marshal(webTokens{})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "webTokens (zero row)", data, webTokensSpec)

	// The zeus counters are each strictly BELOW the total they shadow, which
	// is what a subset looks like on the wire — a fixture with one of them
	// above its total would document the opposite of the contract.
	data, err = json.Marshal(webTokens{
		In: 149754, Out: 15756009, CacheWrite: 87776983, CacheRead: 2555432608,
		Sessions: 154, UpdatedAt: 1785124027359,
		ZeusIn: 41203, ZeusOut: 4877411, ZeusCacheWrite: 30119448, ZeusCacheRead: 901883714,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "webTokens (populated)", data, webTokensSpec)
}

// webJobLoadSpec covers jobload.go's webJobLoad — webJob's "load" object and
// the body of GET /api/job/load. Every key is required: the type carries no
// omitempty anywhere, because a zero note is a real answer and must still
// ride the wire. Absence is expressed by the "load" key itself being gone
// (webJobSpec marks it optional) or by an explicit null on the GET, never by
// a missing field. There is no "roi" key on purpose — the ratio is derived
// from unblock and scope wherever it is rendered, and a spec entry for it
// would be the first sign somebody had stored a quotient that can disagree
// with the two numbers it came from.
var webJobLoadSpec = map[string]fieldSpec{
	"unblock": {kind: wireNumber, required: true},
	"scope":   {kind: wireNumber, required: true},
	"note":    {kind: wireString, required: true},
	"at":      {kind: wireNumber, required: true},
}

// jobLoadGetSpec is GET /api/job/load's envelope. "load" is nullable and
// required: the key is always present, carrying null for a job with no
// estimate — a caller asking about one named job gets an answer either way.
var jobLoadGetSpec = map[string]fieldSpec{
	"load": {kind: wireObject, required: true, nullable: true},
}

// jobLoadPostReqSpec / jobLoadPostRespSpec cover POST /api/job/load's request
// and its ack. The request carries no "at": the write time is stamped
// server-side, and the handler REFUSES a body carrying one rather than
// ignoring it (DisallowUnknownFields), so a spec entry would document a field
// that is a 400. Every other key is required, which here means literally
// required rather than merely non-omitempty: unblock and scope are pointers
// on the Go side precisely so an absent one is a refusal instead of a silent
// clear, and note carries no omitempty so an empty rationale still rides.
// The response is the shared bare ack — the estimate itself rides back on the
// next /api/board poll, already the board's single source of truth for job
// state, exactly as /api/job/color's does.
var jobLoadPostReqSpec = map[string]fieldSpec{
	"slug":    {kind: wireString, required: true},
	"unblock": {kind: wireNumber, required: true},
	"scope":   {kind: wireNumber, required: true},
	"note":    {kind: wireString, required: true},
}

var jobLoadPostRespSpec = map[string]fieldSpec{
	"ok": {kind: wireBool, required: true},
}

func TestWebJobLoadWireShape(t *testing.T) {
	// a cleared-note estimate: every key must still be present, which is the
	// difference between "estimated, with nothing to add" and no load key
	data, err := json.Marshal(webJobLoad{Unblock: 5, Scope: 5, At: 1769040000000})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "webJobLoad (no note)", data, webJobLoadSpec)

	data, err = json.Marshal(webJobLoad{
		Unblock: 2, Scope: 8, Note: "one decision: which store keys the row", At: 1769040000000,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "webJobLoad (populated)", data, webJobLoadSpec)

	unblock, scope := 2, 8
	reqData, err := json.Marshal(webJobLoadReq{Slug: "demo", Unblock: &unblock, Scope: &scope, Note: "why"})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "webJobLoadReq", reqData, jobLoadPostReqSpec)
}

// TestJobLoadEndpointWireShape drives the real handler for both verbs, so the
// envelopes above are pinned as SERVED rather than as marshaled structs.
func TestJobLoadEndpointWireShape(t *testing.T) {
	st := useTestStore(t)
	if _, err := st.CreateJob(&store.Job{Slug: "contract-load"}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	// never estimated: the key is present and null
	req := httptest.NewRequest(http.MethodGet, "/api/job/load?slug=contract-load", nil)
	rec := httptest.NewRecorder()
	jobLoadHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET (unestimated): status %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "GET /api/job/load (unestimated)", rec.Body.Bytes(), jobLoadGetSpec)

	body := mustJSON(t, map[string]any{"slug": "contract-load", "unblock": 2, "scope": 8, "note": "why"})
	req = httptest.NewRequest(http.MethodPost, "/api/job/load", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	jobLoadHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST: status %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "POST /api/job/load", rec.Body.Bytes(), jobLoadPostRespSpec)

	req = httptest.NewRequest(http.MethodGet, "/api/job/load?slug=contract-load", nil)
	rec = httptest.NewRecorder()
	jobLoadHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET (estimated): status %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "GET /api/job/load (estimated)", rec.Body.Bytes(), jobLoadGetSpec)

	var envelope struct {
		Load json.RawMessage `json:"load"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	assertShape(t, "GET /api/job/load .load", envelope.Load, webJobLoadSpec)
}

// --- goals: /api/goals, /api/job/goal, and the two additive board keys
// (goals.go). Two new PATHS plus two ADDED keys on existing shapes, so
// nothing here bumps apiContract — these specs exist because assertShape
// rejects any key ios/API.md hasn't documented, which is what makes an
// addition that skips this file fail the suite rather than slide through
// undocumented. ---

// webGoalSpec covers one goals row on the wire. parent_id is the ONLY
// optional key: 0 means "no parent" rather than "goal zero", so it is the one
// field whose zero value carries no information. Every other key is required
// even at its zero value — a client reads `done` unconditionally, and an
// absent false would be indistinguishable from a goal whose done-ness the
// server declined to state.
var webGoalSpec = map[string]fieldSpec{
	"id":        {kind: wireNumber, required: true},
	"scope":     {kind: wireString, required: true},
	"period":    {kind: wireString, required: true},
	"text":      {kind: wireString, required: true},
	"done":      {kind: wireBool, required: true},
	"position":  {kind: wireNumber, required: true},
	"parent_id": {kind: wireNumber, required: false},
}

// webGoalsSpec covers the two current-period lists — /api/goals' "goals"
// value and /api/board's top-level "goals" key, one assembly function
// (currentGoals) serving both. Both arrays are required and NOT nullable:
// newWebGoals is what guarantees that, and this spec is what would catch a
// later assembly path that skipped it and served null.
var webGoalsSpec = map[string]fieldSpec{
	"week": {kind: wireArray, required: true},
	"day":  {kind: wireArray, required: true},
}

// webJobGoalSpec covers the goal a job links, as it rides a job card and
// /api/goals?slug=. Three keys, all required: the card renders text and
// strikes it through on done, and the id is what a click addresses. Period
// and position are deliberately absent — the chip does not render them, and
// a key no client reads is a key that drifts unnoticed.
var webJobGoalSpec = map[string]fieldSpec{
	"id":   {kind: wireNumber, required: true},
	"text": {kind: wireString, required: true},
	"done": {kind: wireBool, required: true},
}

// goalsGetSpec is GET /api/goals' envelope. "goals" always rides; "job_goals"
// and its head "job_goal" appear only when ?slug= named a job that links at
// least one — omitted rather than null, unlike GET /api/job/load's explicit
// null, because this payload's subject is the goals and the slug is a rider on
// it: the keys' presence IS the answer to "does this job serve a goal", with no
// third state. The two are always present together and "job_goal" is always
// "job_goals"[0].
var goalsGetSpec = map[string]fieldSpec{
	"goals":     {kind: wireObject, required: true},
	"job_goals": {kind: wireArray, required: false},
	"job_goal":  {kind: wireObject, required: false},
}

// goalsPostRespSpec / jobGoalPostReqSpec / jobGoalPostRespSpec cover the two
// write routes. Both responses are the shared bare ack — the written state
// rides back on the next /api/board poll, already the board's single source
// of truth, exactly as /api/job/color's and /api/job/load's do.
var goalsPostRespSpec = map[string]fieldSpec{
	"ok": {kind: wireBool, required: true},
}

// op is OPTIONAL because goal_id 0 — the clear-all, and the whole body a
// pre-multi-goal client sends — carries none: 0 names no goal, so there is
// nothing for an op to disambiguate. slug and goal_id stay required.
var jobGoalPostReqSpec = map[string]fieldSpec{
	"slug":    {kind: wireString, required: true},
	"goal_id": {kind: wireNumber, required: true},
	"op":      {kind: wireString, required: false},
}

var jobGoalPostRespSpec = map[string]fieldSpec{
	"ok": {kind: wireBool, required: true},
}

func TestWebGoalWireShape(t *testing.T) {
	// an unnested week goal: parent_id must be ABSENT, not 0 — that omission
	// is the whole difference between "unassigned" and "child of goal 0"
	data, err := json.Marshal(webGoal{
		ID: 1, Scope: "week", Period: "2026-08-10", Text: "ship goal tracking", Position: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "webGoal (week, unnested)", data, webGoalSpec)
	if bytes.Contains(data, []byte(`"parent_id"`)) {
		t.Errorf("webGoal with ParentID 0 emitted a parent_id key: %s", data)
	}

	// a done day goal nested under it: every key present, parent_id included
	data, err = json.Marshal(webGoal{
		ID: 2, Scope: "day", Period: "2026-08-13", Text: "land the store slice",
		Done: true, Position: 3, ParentID: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "webGoal (day, nested, done)", data, webGoalSpec)

	data, err = json.Marshal(newWebGoals())
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "webGoals (empty)", data, webGoalsSpec)
	// the "never null" half of the contract, which assertShape cannot state:
	// an empty pair must marshal as two empty ARRAYS, not two nulls
	if got := string(data); got != `{"week":[],"day":[]}` {
		t.Errorf("newWebGoals() marshaled as %s, want {\"week\":[],\"day\":[]}", got)
	}

	data, err = json.Marshal(webJobGoal{ID: 1, Text: "ship goal tracking", Done: true})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "webJobGoal", data, webJobGoalSpec)

	// the clear: no op, and none emitted — this is the body a client predating
	// multi-goal jobs sends, and it must stay a legal request shape
	reqData, err := json.Marshal(jobGoalReq{Slug: "demo", GoalID: 0})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "jobGoalReq (clear)", reqData, jobGoalPostReqSpec)
	if bytes.Contains(reqData, []byte(`"op"`)) {
		t.Errorf("jobGoalReq with no Op emitted an op key: %s", reqData)
	}

	reqData, err = json.Marshal(jobGoalReq{Slug: "demo", GoalID: 7, Op: "add"})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "jobGoalReq (add)", reqData, jobGoalPostReqSpec)
}

// TestGoalsEndpointWireShape drives the real handlers for both verbs, so the
// envelopes above are pinned as SERVED rather than as marshaled structs — the
// same reason TestJobLoadEndpointWireShape exists beside TestWebJobLoadWireShape.
func TestGoalsEndpointWireShape(t *testing.T) {
	st := useTestStore(t)
	if _, err := st.CreateJob(&store.Job{Slug: "contract-goals"}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	week, day := store.GoalPeriods(time.Now())

	// no goals yet: both arrays present and empty, no job_goal key
	req := httptest.NewRequest(http.MethodGet, "/api/goals", nil)
	rec := httptest.NewRecorder()
	goalsHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET (no goals): status %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "GET /api/goals (no goals)", rec.Body.Bytes(), goalsGetSpec)
	if got := rec.Body.String(); !strings.Contains(got, `"week":[]`) || !strings.Contains(got, `"day":[]`) {
		t.Errorf("GET /api/goals with no goals served %s, want empty arrays for both scopes", got)
	}

	body := mustJSON(t, map[string]any{
		"op":   "add",
		"goal": map[string]any{"scope": "week", "text": "ship goal tracking"},
	})
	req = httptest.NewRequest(http.MethodPost, "/api/goals", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	goalsHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST add: status %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "POST /api/goals (add)", rec.Body.Bytes(), goalsPostRespSpec)

	weekGoals, err := st.ListGoals("week", week)
	if err != nil || len(weekGoals) != 1 {
		t.Fatalf("ListGoals(week, %s) = %v, %v; want exactly one row", week, weekGoals, err)
	}
	parentID := weekGoals[0].ID

	body = mustJSON(t, map[string]any{
		"op":   "add",
		"goal": map[string]any{"scope": "day", "text": "land the store slice", "parent_id": parentID},
	})
	req = httptest.NewRequest(http.MethodPost, "/api/goals", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	goalsHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST add (day): status %d, body %s", rec.Code, rec.Body.String())
	}
	dayGoals, err := st.ListGoals("day", day)
	if err != nil || len(dayGoals) != 1 {
		t.Fatalf("ListGoals(day, %s) = %v, %v; want exactly one row", day, dayGoals, err)
	}

	// populated: the envelope's arrays now hold real items, each of which must
	// itself match webGoalSpec — the envelope spec only says "array"
	req = httptest.NewRequest(http.MethodGet, "/api/goals", nil)
	rec = httptest.NewRecorder()
	goalsHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET (populated): status %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "GET /api/goals (populated)", rec.Body.Bytes(), goalsGetSpec)
	var envelope struct {
		Goals struct {
			Week []json.RawMessage `json:"week"`
			Day  []json.RawMessage `json:"day"`
		} `json:"goals"`
		JobGoal json.RawMessage `json:"job_goal"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Goals.Week) != 1 || len(envelope.Goals.Day) != 1 {
		t.Fatalf("GET /api/goals served %d week / %d day goals, want 1 each", len(envelope.Goals.Week), len(envelope.Goals.Day))
	}
	assertShape(t, "GET /api/goals .goals.week[0]", envelope.Goals.Week[0], webGoalSpec)
	assertShape(t, "GET /api/goals .goals.day[0]", envelope.Goals.Day[0], webGoalSpec)
	if envelope.JobGoal != nil {
		t.Errorf("GET /api/goals without ?slug= served a job_goal key: %s", envelope.JobGoal)
	}

	// ?slug= on an unlinked job: still no job_goal key
	req = httptest.NewRequest(http.MethodGet, "/api/goals?slug=contract-goals", nil)
	rec = httptest.NewRecorder()
	goalsHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET ?slug= (unlinked): status %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "GET /api/goals?slug= (unlinked)", rec.Body.Bytes(), goalsGetSpec)

	body = mustJSON(t, map[string]any{"slug": "contract-goals", "goal_id": dayGoals[0].ID, "op": "add"})
	req = httptest.NewRequest(http.MethodPost, "/api/job/goal", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	jobGoalHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/job/goal: status %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "POST /api/job/goal", rec.Body.Bytes(), jobGoalPostRespSpec)

	req = httptest.NewRequest(http.MethodGet, "/api/goals?slug=contract-goals", nil)
	rec = httptest.NewRecorder()
	goalsHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET ?slug= (linked): status %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "GET /api/goals?slug= (linked)", rec.Body.Bytes(), goalsGetSpec)
	envelope.JobGoal = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.JobGoal == nil {
		t.Fatalf("GET /api/goals?slug= on a linked job served no job_goal key: %s", rec.Body.String())
	}
	assertShape(t, "GET /api/goals .job_goal", envelope.JobGoal, webJobGoalSpec)
	// the array rides beside it, its elements the same shape, its head the
	// object above — the promise the two keys make about each other
	var linkedList struct {
		JobGoals []json.RawMessage `json:"job_goals"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &linkedList); err != nil {
		t.Fatal(err)
	}
	if len(linkedList.JobGoals) != 1 {
		t.Fatalf("GET /api/goals?slug= served %d job_goals, want 1: %s", len(linkedList.JobGoals), rec.Body.String())
	}
	assertShape(t, "GET /api/goals .job_goals[0]", linkedList.JobGoals[0], webJobGoalSpec)
	if !bytes.Equal(linkedList.JobGoals[0], envelope.JobGoal) {
		t.Errorf("job_goal = %s but job_goals[0] = %s; the primary IS the array's head", envelope.JobGoal, linkedList.JobGoals[0])
	}

	// A goal from a PAST period, linked and done: still named. This is the
	// case the whole any-period rule exists for — a job keeps serving the goal
	// carl picked for it after the week rolls over — and it is the one a
	// current-periods-only resolution answers wrongly rather than not at all,
	// which is why it is pinned here rather than left to the two lists above.
	oldWeek, _ := store.GoalPeriods(time.Now().AddDate(0, 0, -14))
	stale, err := st.AddGoal(store.GoalAdd{Scope: "week", Period: oldWeek, Text: "last fortnight's goal"}, 1000)
	if err != nil {
		t.Fatalf("AddGoal (past week): %v", err)
	}
	done := true
	if err := st.UpdateGoal(stale.ID, store.GoalUpdate{Done: &done}, 1001); err != nil {
		t.Fatalf("UpdateGoal (done): %v", err)
	}
	// cleared first, so the past-period goal is the job's ONLY link and
	// therefore its primary — the key under test below
	body = mustJSON(t, map[string]any{"slug": "contract-goals", "goal_id": 0})
	req = httptest.NewRequest(http.MethodPost, "/api/job/goal", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	jobGoalHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/job/goal (clear): status %d, body %s", rec.Code, rec.Body.String())
	}
	body = mustJSON(t, map[string]any{"slug": "contract-goals", "goal_id": stale.ID, "op": "add"})
	req = httptest.NewRequest(http.MethodPost, "/api/job/goal", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	jobGoalHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/job/goal (past-period goal): status %d, body %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/goals?slug=contract-goals", nil)
	rec = httptest.NewRecorder()
	goalsHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET ?slug= (past-period link): status %d, body %s", rec.Code, rec.Body.String())
	}
	var linked struct {
		JobGoal *webJobGoal `json:"job_goal"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &linked); err != nil {
		t.Fatal(err)
	}
	if linked.JobGoal == nil {
		t.Fatalf("a job linked to a past-period goal served no job_goal key: %s", rec.Body.String())
	}
	if linked.JobGoal.ID != stale.ID || linked.JobGoal.Text != "last fortnight's goal" || !linked.JobGoal.Done {
		t.Errorf("job_goal = %+v, want the done past-period goal %d", *linked.JobGoal, stale.ID)
	}
}

// --- Slack intake bot: /api/slack/inbox, /api/slack/ack, /api/slack/status
// (slack.go) and /api/job/list, /api/job/draft (jobdraft.go). Five new PATHS,
// no existing shape touched, so none of them bumps apiContract — these specs are here
// because assertShape rejects any key ios/API.md hasn't documented, which is
// what makes an addition that skips this file fail the suite rather than
// slide through undocumented. ---

// webSlackMsgSpec covers slack.go's webSlackMsg, the elements of
// /api/slack/inbox's "messages" array. Every key is required: the type
// carries no omitempty anywhere, because an empty channel_name (a DM) and an
// empty thread_ts (a top-level message) are real answers iris must be able
// to read unconditionally. delivered_at, acked_at and disposition have no
// entry on purpose — they are kraken's delivery bookkeeping, and a spec entry
// for one would be the first sign the route had started serving its own
// records back to the agent as if they were the message.
var webSlackMsgSpec = map[string]fieldSpec{
	"id":           {kind: wireNumber, required: true},
	"channel_id":   {kind: wireString, required: true},
	"channel_name": {kind: wireString, required: true},
	"ts":           {kind: wireString, required: true},
	"thread_ts":    {kind: wireString, required: true},
	"author_id":    {kind: wireString, required: true},
	"author_name":  {kind: wireString, required: true},
	"source":       {kind: wireString, required: true},
	"text":         {kind: wireString, required: true},
	"seen_at":      {kind: wireNumber, required: true},
}

// slackInboxSpec is GET /api/slack/inbox's envelope. "messages" is required
// and is always an ARRAY, never null — an empty inbox is `[]`, so a client
// iterates it without a nil check.
var slackInboxSpec = map[string]fieldSpec{
	"messages": {kind: wireArray, required: true},
}

// slackAckReqSpec / slackAckRespSpec cover POST /api/slack/ack's request and
// its ack. Both request keys are literally required rather than merely
// non-omitempty: an empty/absent `ids` is a 400 and so is an empty
// `disposition`, because "acked, reason unknown" is indistinguishable from a
// bug that acks everything. The body is decoded with unknown fields
// rejected, so there is deliberately no third key to document.
var slackAckReqSpec = map[string]fieldSpec{
	"ids":         {kind: wireArray, required: true},
	"disposition": {kind: wireString, required: true},
}

var slackAckRespSpec = map[string]fieldSpec{
	"ok": {kind: wireBool, required: true},
}

// webSlackStatusSpec covers slack.go's webSlackStatus, the whole body of GET
// /api/slack/status. Every key is required for the reason webUpdate's are: a
// zero is a real answer (an empty inbox, a poll that has never run) and an
// absent key would be indistinguishable from it. `configured` false with
// three zeroes is exactly what an unconfigured machine reports — the route
// answers rather than erroring, which is what makes it usable for telling
// "nothing arrived" apart from "the feature is off".
var webSlackStatusSpec = map[string]fieldSpec{
	"configured":   {kind: wireBool, required: true},
	"last_poll_at": {kind: wireNumber, required: true},
	"total":        {kind: wireNumber, required: true},
	"undelivered":  {kind: wireNumber, required: true},
	"unacked":      {kind: wireNumber, required: true},
}

// webJobListItemSpec covers jobdraft.go's webJobListItem, the elements of
// GET /api/job/list's "jobs" array — the three fields iris needs to decide
// "is there already a job about this, and may I edit it?". Every key is
// required: an empty summary (a job with an empty body) is a real answer.
var webJobListItemSpec = map[string]fieldSpec{
	"slug":    {kind: wireString, required: true},
	"state":   {kind: wireString, required: true},
	"summary": {kind: wireString, required: true},
}

var jobListSpec = map[string]fieldSpec{
	"jobs": {kind: wireArray, required: true},
}

// jobDraftReqSpec / jobDraftRespSpec cover POST /api/job/draft. `mtime` is
// the only optional key — absent or 0 skips the optimistic-lock check, the
// same contract the raw save's mtime has — while slug and content are
// required, an empty either way being a 400. The response echoes the slug and
// the NEW lock token because iris makes a follow-up edit on the same job
// within one turn often enough that another /api/job/raw round trip just to
// re-read the rev would be pure latency; that is the one place this route
// differs from /api/job/load's bare ack.
var jobDraftReqSpec = map[string]fieldSpec{
	"slug":    {kind: wireString, required: true},
	"content": {kind: wireString, required: true},
	"mtime":   {kind: wireNumber, required: false},
}

var jobDraftRespSpec = map[string]fieldSpec{
	"ok":    {kind: wireBool, required: true},
	"slug":  {kind: wireString, required: true},
	"mtime": {kind: wireNumber, required: true},
}

func TestWebSlackMsgWireShape(t *testing.T) {
	// a DM: no channel_name, no thread_ts — every key must still be present,
	// which is the difference between "there is no thread" and a key the
	// client has to guess about
	data, err := json.Marshal(webSlackMsg{
		ID: 1, ChannelID: "D0123", TS: "1769040000.000100",
		AuthorID: "U0456", AuthorName: "dana", Source: "dm",
		Text: "csv export times out for big orgs", SeenAt: 1769040001000,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "webSlackMsg (dm)", data, webSlackMsgSpec)

	data, err = json.Marshal(webSlackMsg{
		ID: 2, ChannelID: "C0789", ChannelName: "eng", TS: "1769040100.000200",
		ThreadTS: "1769040000.000100", AuthorID: "U0456", AuthorName: "dana",
		Source: "mention", Text: "<@U0001> can you look at this?", SeenAt: 1769040101000,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "webSlackMsg (threaded mention)", data, webSlackMsgSpec)

	reqData, err := json.Marshal(slackAckReq{IDs: []int64{42, 43}, Disposition: "drafted:csv-export-timeout"})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "slackAckReq", reqData, slackAckReqSpec)
}

// TestSlackEndpointWireShapes drives the two Slack reads through their real
// handlers, so the envelopes above are pinned as SERVED rather than as
// marshaled structs. useTestStore sandboxes HOME, so LoadSlackConfig reads a
// throwaway dir and `configured` is whatever an unconfigured machine reports
// — the SHAPE is the subject here either way.
func TestSlackEndpointWireShapes(t *testing.T) {
	useTestStore(t)

	rec := httptest.NewRecorder()
	slackInboxHandler(rec, httptest.NewRequest(http.MethodGet, "/api/slack/inbox", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/slack/inbox: status %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "GET /api/slack/inbox (empty)", rec.Body.Bytes(), slackInboxSpec)

	rec = httptest.NewRecorder()
	slackStatusHandler(rec, httptest.NewRequest(http.MethodGet, "/api/slack/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/slack/status: status %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "GET /api/slack/status", rec.Body.Bytes(), webSlackStatusSpec)
}

// TestJobDraftEndpointWireShapes drives the create path and the list read,
// which together cover every wire shape jobdraft.go serves on success. The
// refusal matrix itself is behavior rather than shape and is covered by the
// package's own draft tests; what is pinned here is that a successful draft
// answers with all three documented keys, since iris reuses the returned
// mtime as the lock token for its next edit.
func TestJobDraftEndpointWireShapes(t *testing.T) {
	useTestStore(t)

	// no ready:/prereqs: — the route refuses both outright now, so a fixture
	// carrying either would 400 before any shape existed to assert on.
	content := "---\njob: contract-draft\nrepo: kraken\nsolo: false\n---\n\ncsv export times out for big orgs\n"
	body := mustJSON(t, map[string]any{"slug": "contract-draft", "content": content})
	rec := httptest.NewRecorder()
	jobDraftHandler(rec, httptest.NewRequest(http.MethodPost, "/api/job/draft", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/job/draft (create): status %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "POST /api/job/draft (create)", rec.Body.Bytes(), jobDraftRespSpec)

	reqData, err := json.Marshal(jobDraftReq{Slug: "contract-draft", Content: content, MTime: 1})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "jobDraftReq", reqData, jobDraftReqSpec)

	rec = httptest.NewRecorder()
	jobListHandler(rec, httptest.NewRequest(http.MethodGet, "/api/job/list", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/job/list: status %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "GET /api/job/list", rec.Body.Bytes(), jobListSpec)

	var envelope struct {
		Jobs []json.RawMessage `json:"jobs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Jobs) != 1 {
		t.Fatalf("jobs = %d entries, want 1", len(envelope.Jobs))
	}
	assertShape(t, "GET /api/job/list .jobs[0]", envelope.Jobs[0], webJobListItemSpec)

	ackData, err := json.Marshal(map[string]any{"ok": true})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "POST /api/slack/ack", ackData, slackAckRespSpec)
}

func TestDaemonStatusWireShape(t *testing.T) {
	data, err := json.Marshal(jobs.DaemonStatus{PID: 1, StartedAt: 2, LastTick: 3, Cells: 4})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "jobs.DaemonStatus (empty optionals)", data, daemonStatusSpec)

	data, err = json.Marshal(jobs.DaemonStatus{PID: 1, StartedAt: 2, LastTick: 3, Cells: 4, LastError: "boom", Stale: true})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "jobs.DaemonStatus (populated)", data, daemonStatusSpec)
}

func TestWebBoardWireShape(t *testing.T) {
	board := webBoard{
		Daemon:   jobs.DaemonStatus{PID: 1, StartedAt: 2, LastTick: 3, Cells: 4},
		Jobs:     []webJob{},
		Contract: apiContract,
		// newWebGoals rather than the zero value: buildWebBoard always assembles
		// through currentGoals, so an empty board's goals are two empty ARRAYS,
		// and a fixture built from the zero value would document two nulls.
		Goals: newWebGoals(),
	}
	data, err := json.Marshal(board)
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "webBoard", data, webBoardSpec)

	// assertShape only checks that "contract" is a number, so a caller that
	// forgets to stamp Contract: apiContract would still pass — pin the
	// actual value too, mirroring TestHealthWireShape's equivalent check.
	var resp map[string]any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&resp); err != nil {
		t.Fatal(err)
	}
	n, ok := resp["contract"].(json.Number)
	if !ok {
		t.Fatalf("contract not a number: %#v", resp["contract"])
	}
	if got, _ := n.Int64(); got != int64(apiContract) {
		t.Errorf("contract = %d, want %d", got, apiContract)
	}
}

func TestWebBackgroundWireShape(t *testing.T) {
	data, err := json.Marshal(webBackground{UpdatedAt: 1_700_000_000_000})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "webBackground", data, webBackgroundSpec)

	// A board WITH a background carries the key as an object; the bare-board
	// fixture in TestWebBoardWireShape covers the absent side (required:
	// false there is the assertion that absence is legal).
	board, err := json.Marshal(webBoard{Goals: newWebGoals(), Jobs: []webJob{}, Background: &webBackground{UpdatedAt: 1}})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(board, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["background"].(map[string]any); !ok {
		t.Errorf("board with a background set: background key = %#v, want an object", m["background"])
	}

	// And the omitempty half: a nil pointer must DROP the key, never send
	// null or a zeroed object — absence is the wire's only "no background".
	bare, err := json.Marshal(webBoard{Goals: newWebGoals(), Jobs: []webJob{}})
	if err != nil {
		t.Fatal(err)
	}
	var m2 map[string]any
	if err := json.Unmarshal(bare, &m2); err != nil {
		t.Fatal(err)
	}
	if _, present := m2["background"]; present {
		t.Errorf("board without a background still carries the key: %#v", m2["background"])
	}
}

func TestWebJobWireShapeEmpty(t *testing.T) {
	data, err := json.Marshal(webJob{Slug: "x", Repo: "monolith", State: "draft"})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "webJob (empty optionals)", data, webJobSpec)
}

func TestWebJobWireShapePopulated(t *testing.T) {
	wj := webJob{
		Slug: "x", Repo: "monolith", State: "blocked", Live: true,
		Err: "e", Outcome: "o", Color: "purple", Cell: "cell-x",
		Artifacts: []webArtifact{{Name: "a", URL: "/api/artifact/x/cell/a"}},
		Detail:    "zeus said something",
		Question: &webQuestion{
			ToolUseID: "tu_1",
			Questions: []webQuestionItem{{
				Question: "which approach?", Header: "approach", MultiSelect: false,
				Options: []webQuestionOption{{Label: "A", Description: "do A"}},
			}},
		},
		ZeusStatus:    "running",
		Stage:         "do",
		Wakeup:        &webWakeup{Reason: "poll PR checks", FiresAt: 1234567890},
		Roster:        []webAgent{{Name: "hephaestus-x", Model: "sonnet", Role: "hephaestus", Status: "running", Working: true}},
		Health:        "unhealthy",
		PRs:           []jobs.JobPR{{URL: "https://github.com/car1zhang/kraken/pull/7", State: "merged"}},
		ModTime:       123,
		Archived:      true,
		DisplaySlug:   "x-orig",
		CellGone:      true,
		Reactivatable: true,
		LastSent:      "pivot to plan B",
		DocQuestions:  []webDocQuestion{{ID: "Q1", Text: "which approach?", Importance: "P1", Answer: "A"}},
		Feedback:      []webFeedback{{ID: 1, Artifact: "00-overview.md", Note: "tighten the intro", At: 1234567890}},
		Threads:       []webThread{{TID: "t1", Title: "why the retry loop", State: "live", Pane: "%42", CreatedAt: 1234567890}},
		Tokens: &webTokens{
			In: 149754, Out: 15756009, CacheWrite: 87776983, CacheRead: 2555432608,
			Sessions: 154, UpdatedAt: 1785124027359,
			ZeusIn: 41203, ZeusOut: 4877411, ZeusCacheWrite: 30119448, ZeusCacheRead: 901883714,
		},
		Load: &webJobLoad{Unblock: 2, Scope: 8, Note: "one decision: which store keys the row", At: 1769040000000},
		Goal: &webJobGoal{ID: 3, Text: "ship goal tracking", Done: false},
	}
	data, err := json.Marshal(wj)
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "webJob (populated)", data, webJobSpec)
}

// --- webJob's array-element types, marshaled and checked directly: webJob's
// own spec above only asserts "artifacts"/"roster" are arrays, not
// what their elements look like (web.go, job.go) ---

var webArtifactSpec = map[string]fieldSpec{
	"name": {kind: wireString, required: true},
	"url":  {kind: wireString, required: true},
}

var webAgentSpec = map[string]fieldSpec{
	"name":    {kind: wireString, required: true},
	"model":   {kind: wireString, required: true},
	"role":    {kind: wireString, required: true},
	"status":  {kind: wireString, required: true},
	"working": {kind: wireBool, required: true},
}

// webLibraryItemSpec covers library.go's webLibraryItem, the elements of GET
// /api/library's "items" array. All five keys are required — none carries
// omitempty, and each is a real answer at its zero value — and the spec's
// no-extra-keys check is what keeps the snapshot BYTES from ever appearing
// beside them: the bytes are what "url" is for.
//
// It rides here with webArtifactSpec rather than in a file of its own for the
// reason that spec does: this is a small array-element shape whose parent
// payload asserts only that it holds an array.
var webLibraryItemSpec = map[string]fieldSpec{
	"id":       {kind: wireNumber, required: true},
	"slug":     {kind: wireString, required: true},
	"name":     {kind: wireString, required: true},
	"saved_at": {kind: wireNumber, required: true},
	"url":      {kind: wireString, required: true},
}

// jobPRSpec covers job.go's jobs.JobPR, the elements of webJob's "prs" array —
// the daemon-tracked record of every PR shipped from the cell branch.
var jobPRSpec = map[string]fieldSpec{
	"url":   {kind: wireString, required: true},
	"state": {kind: wireString, required: true},
}

func TestWebArtifactWireShape(t *testing.T) {
	data, err := json.Marshal(webArtifact{Name: "a", URL: "/api/artifact/x/cell/a"})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "webArtifact", data, webArtifactSpec)
}

// TestWebLibraryItemWireShape pins one library entry's shape, marshaled from
// the same constructor the route uses — so the served "url" is asserted to be
// the one libraryItemURL builds rather than a literal restated here, which is
// the only version of that assertion a wrong route registration could fail.
func TestWebLibraryItemWireShape(t *testing.T) {
	item := webLibraryItemFrom(store.SavedArtifactMeta{
		ID: 7, JobID: 3, Slug: "kraken-save-artifacts", Name: "50-contract.md",
		SavedAt: 1769040000000, Size: 4096,
	})
	if want := "/api/library/artifact/7/50-contract.md"; item.URL != want {
		t.Fatalf("url = %q, want %q", item.URL, want)
	}
	data, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "webLibraryItem", data, webLibraryItemSpec)
}

func TestWebAgentWireShape(t *testing.T) {
	data, err := json.Marshal(webAgent{Name: "hephaestus-x", Model: "sonnet", Role: "hephaestus", Status: "running", Working: true})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "webAgent", data, webAgentSpec)
}

// webDocQuestionSpec covers board.go's webDocQuestion, the elements of
// webJob's "doc_questions" array — mirrors jobs.DocQuestion's own field set
// verbatim, same json tags, so id/text/importance are always-present and
// context/recommendation/answer/answered_at are omitempty.
var webDocQuestionSpec = map[string]fieldSpec{
	"id":             {kind: wireString, required: true},
	"text":           {kind: wireString, required: true},
	"context":        {kind: wireString, required: false},
	"importance":     {kind: wireString, required: true},
	"recommendation": {kind: wireString, required: false},
	"answer":         {kind: wireString, required: false},
	"answered_at":    {kind: wireString, required: false},
}

func TestWebDocQuestionWireShape(t *testing.T) {
	data, err := json.Marshal(webDocQuestion{ID: "Q1", Text: "which approach?", Importance: "P1"})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "webDocQuestion (empty optionals)", data, webDocQuestionSpec)

	data, err = json.Marshal(webDocQuestion{
		ID: "Q1", Text: "which approach?", Context: "some background",
		Importance: "P0", Recommendation: "go with A", Answer: "A", AnsweredAt: "2026-07-23T00:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "webDocQuestion (populated)", data, webDocQuestionSpec)
}

// webFeedbackSpec covers board.go's webFeedback, the elements of webJob's
// "feedback" array — mirrors store.Feedback's field set (minus the snapshot
// blob, which ListFeedbackMeta never surfaces on this path).
var webFeedbackSpec = map[string]fieldSpec{
	"id":           {kind: wireNumber, required: true},
	"artifact":     {kind: wireString, required: true},
	"note":         {kind: wireString, required: true},
	"at":           {kind: wireNumber, required: true},
	"addressed":    {kind: wireString, required: false},
	"addressed_at": {kind: wireString, required: false},
}

func TestWebFeedbackWireShape(t *testing.T) {
	data, err := json.Marshal(webFeedback{ID: 1, Artifact: "00-overview.md", Note: "tighten the intro", At: 1234567890})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "webFeedback (empty optionals)", data, webFeedbackSpec)

	data, err = json.Marshal(webFeedback{
		ID: 1, Artifact: "00-overview.md", Note: "tighten the intro", At: 1234567890,
		Addressed: "reworded the intro", AddressedAt: "2026-07-24T00:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "webFeedback (populated)", data, webFeedbackSpec)
}

// webThreadSpec covers board.go's webThread, the elements of webJob's
// "threads" array — the board-facing subset of a `threads` row
// (store.Thread): identity, state, the live pane, and the two timestamps.
// Provenance (session_id/parent_session/fork_uuid/seed/cwd) and the window
// target deliberately never ride the board poll; fork_uuid rides
// /api/thread/read instead.
var webThreadSpec = map[string]fieldSpec{
	"tid":        {kind: wireString, required: true},
	"title":      {kind: wireString, required: false},
	"state":      {kind: wireString, required: true},
	"pane":       {kind: wireString, required: false},
	"model":      {kind: wireString, required: false},
	"created_at": {kind: wireNumber, required: true},
	"closed_at":  {kind: wireNumber, required: false},
}

func TestWebThreadWireShape(t *testing.T) {
	// a closed thread: no title, no pane (CloseThread clears it), and no
	// model — the shape a thread branched before the picker existed keeps
	// forever, which is why `model` is omitempty rather than always present
	data, err := json.Marshal(webThread{TID: "t1", State: "closed", CreatedAt: 1234567890})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "webThread (empty optionals)", data, webThreadSpec)

	data, err = json.Marshal(webThread{
		TID: "t2", Title: "why the retry loop", State: "live", Pane: "%42",
		Model: "haiku", CreatedAt: 1234567890, ClosedAt: 1234567899,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "webThread (populated)", data, webThreadSpec)
}

func TestJobPRWireShape(t *testing.T) {
	data, err := json.Marshal(jobs.JobPR{URL: "https://github.com/car1zhang/kraken/pull/7", State: "merged"})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "jobs.JobPR", data, jobPRSpec)
}

// webPrereq and jobs.Prereq had wire-shape tests here. Both types are gone
// with the feature (Q1), and so are the shapes: the job form no longer
// carries a prereqs array in either direction, which is the v6 break.

// --- webQuestion / webQuestionItem / webQuestionOption (web.go) ---

var webQuestionSpec = map[string]fieldSpec{
	"tool_use_id": {kind: wireString, required: true},
	"questions":   {kind: wireArray, required: true},
}

var webQuestionItemSpec = map[string]fieldSpec{
	"question":     {kind: wireString, required: true},
	"header":       {kind: wireString, required: true},
	"multi_select": {kind: wireBool, required: true},
	"options":      {kind: wireArray, required: true},
}

var webQuestionOptionSpec = map[string]fieldSpec{
	"label":       {kind: wireString, required: true},
	"description": {kind: wireString, required: true},
}

func TestWebQuestionWireShape(t *testing.T) {
	wq := webQuestion{
		ToolUseID: "tu_1",
		Questions: []webQuestionItem{{
			Question: "q", Header: "h", MultiSelect: false,
			Options: []webQuestionOption{{Label: "A", Description: "do A"}},
		}},
	}
	data, err := json.Marshal(wq)
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "webQuestion", data, webQuestionSpec)

	itemData, err := json.Marshal(wq.Questions[0])
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "webQuestionItem", itemData, webQuestionItemSpec)

	optData, err := json.Marshal(wq.Questions[0].Options[0])
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "webQuestionOption", optData, webQuestionOptionSpec)
}

// --- webWakeup (board.go) ---

var webWakeupSpec = map[string]fieldSpec{
	"reason":   {kind: wireString, required: true},
	"fires_at": {kind: wireNumber, required: true},
}

func TestWebWakeupWireShape(t *testing.T) {
	data, err := json.Marshal(webWakeup{Reason: "poll PR checks", FiresAt: 1234567890})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "webWakeup", data, webWakeupSpec)
}

// --- Peer wire shape (webpeers.go/peer.go) ---

var peerSpec = map[string]fieldSpec{
	"index":              {kind: wireNumber, required: true},
	"pane":               {kind: wireString, required: false},
	"tmux_session":       {kind: wireString, required: false},
	"window":             {kind: wireNumber, required: true},
	"pid":                {kind: wireNumber, required: false},
	"session_id":         {kind: wireString, required: false},
	"name":               {kind: wireString, required: false},
	"cwd":                {kind: wireString, required: false},
	"status":             {kind: wireString, required: true},
	"status_at":          {kind: wireNumber, required: false},
	"wakeup":             {kind: wireObject, required: false},
	"session_id_guessed": {kind: wireBool, required: false},
	"branch":             {kind: wireString, required: false},
	"model":              {kind: wireString, required: false},
	"kind":               {kind: wireString, required: false},
	"agent":              {kind: wireString, required: false},
	"role":               {kind: wireString, required: true},
	"cmd":                {kind: wireString, required: false},
	"cmd_seconds":        {kind: wireNumber, required: false},
	"self":               {kind: wireBool, required: false},
	"team":               {kind: wireString, required: false},
	"agent_name":         {kind: wireString, required: false},
	"parent_session_id":  {kind: wireString, required: false},
	"messageable":        {kind: wireBool, required: true},
}

func TestPeerWireShapeEmpty(t *testing.T) {
	data, err := json.Marshal(core.Peer{Status: "running", Role: "shell"})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "Peer (empty optionals)", data, peerSpec)
}

func TestPeerWireShapePopulated(t *testing.T) {
	p := core.Peer{
		Index: 1, Pane: "%3", TmuxSession: "cell-x", WindowIdx: 0,
		PID: 123, SessionID: "sid", Name: "zeus", CWD: "/x",
		Status: "running", StatusAt: 456, SessionIDGuessed: true,
		Branch: "carl/x", Model: "sonnet", Kind: "cli", Agent: "claude",
		Role: "agent", Cmd: "vim", CmdSeconds: 7, Self: true,
		Team: "crew", AgentName: "zeus", ParentSID: "psid", Messageable: true,
	}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "Peer (populated)", data, peerSpec)
}

// core.Wakeup mirrors status "polling" over the wire on Peer.Wakeup — its
// own dedicated spec since peerSpec only asserts the field is an object.
var wakeupSpec = map[string]fieldSpec{
	"reason":        {kind: wireString, required: true},
	"delay_seconds": {kind: wireNumber, required: true},
	"fires_at":      {kind: wireNumber, required: true},
}

func TestPeerWireShapePolling(t *testing.T) {
	p := core.Peer{
		Index: 1, Pane: "%3", TmuxSession: "cell-x", WindowIdx: 0,
		Status: "polling", Role: "agent", Agent: "claude", Messageable: true,
		Wakeup: &core.Wakeup{Reason: "poll PR checks", DelaySeconds: 300, FiresAt: 1234567890},
	}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "Peer (polling)", data, peerSpec)

	wakeupData, err := json.Marshal(p.Wakeup)
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "core.Wakeup", wakeupData, wakeupSpec)
}

// --- /api/peers/read block element (webpeers.go peersReadHandler) ---
//
// peersReadHandler needs a live tmux pane to reach its success path (see the
// "not shape-tested" note above), so this checks the element shape it
// constructs directly — map[string]string{"kind": ..., "text": ...} — rather
// than driving it through the handler.

var peersReadBlockSpec = map[string]fieldSpec{
	"kind": {kind: wireString, required: true},
	"text": {kind: wireString, required: true},
	// present only on kind "teammate": the sending teammate's id
	"from": {kind: wireString, required: false},
}

func TestPeersReadBlockWireShape(t *testing.T) {
	data, err := json.Marshal(map[string]string{"kind": "text", "text": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "/api/peers/read block", data, peersReadBlockSpec)

	data, err = json.Marshal(map[string]string{"kind": "teammate", "text": "idle (available)", "from": "hermes-x"})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "/api/peers/read block (teammate)", data, peersReadBlockSpec)
}

// --- /api/peers/send key-send response (webpeers.go peersSendHandler) ---
//
// The message-send success shapes ({"ok":true,"queued":bool} /
// {"ok":true,"queued":false,"shell":true}) still need a live pane to reach
// (see the "not shape-tested" note above); the key-send response
// ({"ok":true,"key":true}) is a static literal, so it's checked directly the
// same way TestPeersReadBlockWireShape checks peersReadHandler's block shape.

var peersSendKeySpec = map[string]fieldSpec{
	"ok":  {kind: wireBool, required: true},
	"key": {kind: wireBool, required: true},
}

func TestPeersSendKeyWireShape(t *testing.T) {
	data, err := json.Marshal(map[string]any{"ok": true, "key": true})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "/api/peers/send (key)", data, peersSendKeySpec)
}

// --- /api/peers/open (webpeers.go peersOpenHandler) ---
//
// openPeerFunc is swapped out the same way peersKillHandler's paneSessionFunc
// is elsewhere in this package, so both of the handler's success shapes
// (clean focus, and a raised session whose window failed to focus) are
// reachable without a real tmux pane/session.

var peersOpenSpec = map[string]fieldSpec{
	"ok":          {kind: wireBool, required: true},
	"note":        {kind: wireString, required: true},
	"focus_error": {kind: wireString, required: false},
}

func TestPeersOpenWireShape(t *testing.T) {
	source := func() []*core.Peer {
		return []*core.Peer{{Pane: "%2", Name: "zeus", TmuxSession: "mobile"}}
	}
	old := openPeerFunc
	t.Cleanup(func() { openPeerFunc = old })
	h := peersOpenHandler(source)

	openPeerFunc = func(p *core.Peer) (string, core.RaiseOutcome, error) {
		return "focused zeus (mobile, pane %2)", core.RaiseOutcome{}, nil
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/peers/open", bytes.NewReader(mustJSON(t, map[string]any{"pane": "%2"})))
	req.Header.Set("Content-Type", "application/json")
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "/api/peers/open (clean success)", rec.Body.Bytes(), peersOpenSpec)

	openPeerFunc = func(p *core.Peer) (string, core.RaiseOutcome, error) {
		return "selected zeus (%2) — focus its window manually: boom",
			core.RaiseOutcome{FocusErr: errors.New("boom")}, nil
	}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/peers/open", bytes.NewReader(mustJSON(t, map[string]any{"pane": "%2"})))
	req.Header.Set("Content-Type", "application/json")
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "/api/peers/open (focus_error)", rec.Body.Bytes(), peersOpenSpec)
}

// --- /api/editor/open (editor.go editorOpenHandler) ---
//
// Reachable here for the same reason /api/peers/open is: the launch itself is
// behind the openInEditorFunc seam, so both success shapes are exercised with
// a temp HOME, a throwaway checkout, and no exec — nothing in this package
// ever runs Zed. The refusal shapes are the shared {"error": string}
// convention and are covered behaviourally in editoropen_*_test.go.

var editorOpenSpec = map[string]fieldSpec{
	"ok": {kind: wireBool, required: true},
	// additive, and present ONLY on the fallback open: the cell's worktree is
	// gone, so the file came from the primary checkout instead. Its
	// optionality is the wire guarantee that matters — the web client toasts
	// whatever `note` holds, so a clean open must not carry one, exactly as
	// /api/peers/open's focus_error must be absent on a clean focus.
	"note": {kind: wireString, required: false},
}

// editorOpenRepo makes a checkout under ~/Developer that jobs.ResolveRepo can
// find (a dir with a .git entry), and returns its path.
func editorOpenRepo(t *testing.T) string {
	t.Helper()
	repo := filepath.Join(util.Home(), "Developer", "demo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return repo
}

// editorOpenFile writes a file at root/rel, creating its parents — the handler
// stats every path carrying a "/" before it hands one to the seam.
func editorOpenFile(t *testing.T, root, rel string) string {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("package demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func editorOpenPost(t *testing.T, slug, path string, line int) *httptest.ResponseRecorder {
	t.Helper()
	body := mustJSON(t, map[string]any{"slug": slug, "path": path, "line": line})
	req := httptest.NewRequest(http.MethodPost, "/api/editor/open", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:52341" // the local-origin gate; httptest's default is 192.0.2.1
	rec := httptest.NewRecorder()
	editorOpenHandler(rec, req)
	return rec
}

func TestEditorOpenWireShape(t *testing.T) {
	st := useTestStore(t)
	old := openInEditorFunc
	t.Cleanup(func() { openInEditorFunc = old })
	var opened string
	openInEditorFunc = func(root, abs string, line, col int) error {
		opened = abs
		return nil
	}
	repo := editorOpenRepo(t)
	const rel = "internal/api/board.go"

	// clean success: the cell's worktree is still on disk, so the file opens
	// from there and the body is a bare {ok:true}
	live := mustCreateStoreJob(t, st, store.Job{Slug: "editor-shape-live", Repo: "demo", State: "working"})
	wt := jobs.CellWorktree(live, repo)
	want := editorOpenFile(t, wt, rel)
	rec := editorOpenPost(t, live.Slug, rel, 37)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if opened != want {
		t.Errorf("opened %q, want %q", opened, want)
	}
	assertShape(t, "/api/editor/open (clean success)", rec.Body.Bytes(), editorOpenSpec)

	// worktree gone: same shape plus the additive note, opened out of the
	// primary checkout
	gone := mustCreateStoreJob(t, st, store.Job{Slug: "editor-shape-gone", Repo: "demo", State: "done"})
	wantFallback := editorOpenFile(t, repo, rel)
	rec = editorOpenPost(t, gone.Slug, rel, 37)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if opened != wantFallback {
		t.Errorf("opened %q, want %q", opened, wantFallback)
	}
	assertShape(t, "/api/editor/open (worktree gone)", rec.Body.Bytes(), editorOpenSpec)
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["note"]; !ok {
		t.Errorf("fallback open carries no note: %s", rec.Body.String())
	}
}

// --- /api/health (webpeers.go) ---

var healthSpec = map[string]fieldSpec{
	"ok":       {kind: wireBool, required: true},
	"host":     {kind: wireString, required: true},
	"contract": {kind: wireNumber, required: true},
	"stale":    {kind: wireBool, required: true},
	// additive: the running binary's ldflags-stamped short sha, "dev" unstamped
	"build": {kind: wireString, required: true},
}

func TestHealthWireShape(t *testing.T) {
	rec := httptest.NewRecorder()
	healthHandler(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "/api/health", rec.Body.Bytes(), healthSpec)

	var resp map[string]any
	dec := json.NewDecoder(bytes.NewReader(rec.Body.Bytes()))
	dec.UseNumber()
	if err := dec.Decode(&resp); err != nil {
		t.Fatal(err)
	}
	n, ok := resp["contract"].(json.Number)
	if !ok {
		t.Fatalf("contract not a number: %#v", resp["contract"])
	}
	if got, _ := n.Int64(); got != int64(apiContract) {
		t.Errorf("contract = %d, want %d", got, apiContract)
	}
}

// --- usage: GET /api/usage (usage.go) ---

var webUsageSpec = map[string]fieldSpec{
	"available":       {kind: wireBool, required: true},
	"fetched_at":      {kind: wireNumber, required: true},
	"age_seconds":     {kind: wireNumber, required: true},
	"daemon_ok":       {kind: wireBool, required: true},
	"accounts":        {kind: wireArray, required: true},
	"rotation_mode":   {kind: wireString, required: true},
	"rotation_set_by": {kind: wireString, required: false},
	"rotation_set_at": {kind: wireNumber, required: false},
}

// accountUsageSpec covers core.AccountUsage, the elements of webUsage's
// "accounts" array — webUsage's own spec above only asserts it's an array.
var accountUsageSpec = map[string]fieldSpec{
	"slug":           {kind: wireString, required: true},
	"email":          {kind: wireString, required: false},
	"display_name":   {kind: wireString, required: false},
	"tier":           {kind: wireString, required: false},
	"current":        {kind: wireBool, required: false},
	"session":        {kind: wireObject, required: false},
	"weekly":         {kind: wireObject, required: false},
	"models":         {kind: wireArray, required: false},
	"fetched_at":     {kind: wireNumber, required: false},
	"stale":          {kind: wireBool, required: false},
	"error":          {kind: wireString, required: false},
	"reauth":         {kind: wireBool, required: false},
	"no_creds":       {kind: wireBool, required: false},
	"cooldown_until": {kind: wireNumber, required: false},
}

var usageWindowSpec = map[string]fieldSpec{
	"percent":   {kind: wireNumber, required: true},
	"resets_at": {kind: wireNumber, required: false},
}

var modelWindowSpec = map[string]fieldSpec{
	"name":      {kind: wireString, required: true},
	"percent":   {kind: wireNumber, required: true},
	"resets_at": {kind: wireNumber, required: false},
}

func TestWebUsageWireShapeEmpty(t *testing.T) {
	data, err := json.Marshal(webUsage{Accounts: []core.AccountUsage{}, RotationMode: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "webUsage (unavailable)", data, webUsageSpec)
}

func TestWebUsageWireShapePopulated(t *testing.T) {
	wu := webUsage{
		Available:     true,
		FetchedAt:     1234567890,
		AgeSeconds:    42,
		DaemonOK:      true,
		RotationMode:  "manual",
		RotationSetBy: "cli",
		RotationSetAt: 1234567890,
		Accounts: []core.AccountUsage{{
			Slug: "main", Email: "carl@example.com", DisplayName: "Carl",
			Tier: "pro", Current: true,
			Session: &core.UsageWindow{Percent: 12.5, ResetsAt: 1234567890},
			Weekly:  &core.UsageWindow{Percent: 34.5, ResetsAt: 1234567890},
			Models: []core.ModelWindow{
				{Name: "Fable", Percent: 5.5, ResetsAt: 1234567890},
			},
			FetchedAt: 1234567890, Stale: true, Error: "e",
			Reauth: true, NoCreds: true, CooldownUntil: 1234567890,
		}},
	}
	data, err := json.Marshal(wu)
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "webUsage (populated)", data, webUsageSpec)

	var resp map[string]any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&resp); err != nil {
		t.Fatal(err)
	}
	accounts, ok := resp["accounts"].([]any)
	if !ok || len(accounts) != 1 {
		t.Fatalf("accounts = %#v, want a 1-element array", resp["accounts"])
	}
	rowData, err := json.Marshal(wu.Accounts[0])
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "core.AccountUsage", rowData, accountUsageSpec)

	windowData, err := json.Marshal(wu.Accounts[0].Session)
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "core.UsageWindow", windowData, usageWindowSpec)

	modelData, err := json.Marshal(wu.Accounts[0].Models[0])
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "core.ModelWindow", modelData, modelWindowSpec)
}

// --- account rotation: POST /api/account/switch, POST /api/account/mode
// (account.go) — custom request/response shapes, so each gets its own spec.
// Handler behavior (method guards, validation, 409/500 branches) is covered
// by account_test.go; this only pins the wire shapes. ---

var accountSwitchReqSpec = map[string]fieldSpec{
	"slug": {kind: wireString, required: true},
}

var accountSwitchRespSpec = map[string]fieldSpec{
	"from": {kind: wireString, required: true},
	"to":   {kind: wireString, required: true},
	"mode": {kind: wireString, required: true},
}

func TestAccountSwitchWireShape(t *testing.T) {
	reqData, err := json.Marshal(accountSwitchReq{Slug: "carl-work"})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "accountSwitchReq", reqData, accountSwitchReqSpec)

	respData, err := json.Marshal(accountSwitchResp{From: "old@example.com", To: "new@example.com", Mode: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "accountSwitchResp", respData, accountSwitchRespSpec)
}

var accountModeReqSpec = map[string]fieldSpec{
	"mode": {kind: wireString, required: true},
}

var accountModeRespSpec = map[string]fieldSpec{
	"mode": {kind: wireString, required: true},
}

func TestAccountModeWireShape(t *testing.T) {
	reqData, err := json.Marshal(accountModeReq{Mode: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "accountModeReq", reqData, accountModeReqSpec)

	respData, err := json.Marshal(accountModeResp{Mode: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "accountModeResp", respData, accountModeRespSpec)
}

// --- job editor: /api/job/raw, /api/job/form, /api/job/saveform ---
//
// writeTestJob and mustJSON are shared fixtures from web_test.go.

var jobRawSpec = map[string]fieldSpec{
	"slug":     {kind: wireString, required: true},
	"content":  {kind: wireString, required: true},
	"mtime":    {kind: wireNumber, required: true},
	"editable": {kind: wireBool, required: true},
}

var jobFormSpec = map[string]fieldSpec{
	"slug": {kind: wireString, required: true},
	"repo": {kind: wireString, required: true},
	"base": {kind: wireString, required: true},
	// no "ready", no "prereqs": both left this shape in apiContract v6, and
	// assertShape refuses an UNEXPECTED key as well as a missing one, so
	// their absence from this map is what pins them off the wire.
	"solo":     {kind: wireBool, required: true},
	"model":    {kind: wireString, required: true},
	"color":    {kind: wireString, required: true},
	"spec":     {kind: wireString, required: true},
	"mtime":    {kind: wireNumber, required: true},
	"editable": {kind: wireBool, required: true},
	"err":      {kind: wireString, required: true},
	"slugs":    {kind: wireArray, required: true, nullable: true},
	"repos":    {kind: wireArray, required: true, nullable: true},
}

// jobColorSpec is POST /api/job/color's response: a bare ack. The colour
// itself rides back to clients on the next /api/board poll, which is
// already the board's single source of truth for job state.
var jobColorSpec = map[string]fieldSpec{
	"ok": {kind: wireBool, required: true},
}

var jobSaveFormSpec = map[string]fieldSpec{
	"ok":    {kind: wireBool, required: true},
	"mtime": {kind: wireNumber, required: true},
	"warn":  {kind: wireString, required: true},
}

func TestJobRawWireShape(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	writeTestJob(t, "contract-raw", "")

	req := httptest.NewRequest(http.MethodGet, "/api/job/raw?slug=contract-raw", nil)
	rec := httptest.NewRecorder()
	webJobRaw(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("webJobRaw: status %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "/api/job/raw", rec.Body.Bytes(), jobRawSpec)
}

func TestJobFormWireShape(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	writeTestJob(t, "contract-form", "")

	req := httptest.NewRequest(http.MethodGet, "/api/job/form?slug=contract-form", nil)
	rec := httptest.NewRecorder()
	webJobForm(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("webJobForm: status %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "/api/job/form", rec.Body.Bytes(), jobFormSpec)
}

func TestJobColorWireShape(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	writeTestJob(t, "contract-color", "")

	body := mustJSON(t, map[string]any{"slug": "contract-color", "color": "purple"})
	req := httptest.NewRequest(http.MethodPost, "/api/job/color", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	webJobColor(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("webJobColor: status %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "/api/job/color", rec.Body.Bytes(), jobColorSpec)

	// clearing is a legitimate value on the same shape
	body = mustJSON(t, map[string]any{"slug": "contract-color", "color": ""})
	req = httptest.NewRequest(http.MethodPost, "/api/job/color", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	webJobColor(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("webJobColor (clear): status %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "/api/job/color (clear)", rec.Body.Bytes(), jobColorSpec)
}

func TestJobSaveFormWireShape(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	writeTestJob(t, "contract-saveform", "")

	body := mustJSON(t, map[string]any{"slug": "contract-saveform", "repo": "monolith"})
	req := httptest.NewRequest(http.MethodPost, "/api/job/saveform", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	webJobSaveForm(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("webJobSaveForm: status %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "/api/job/saveform", rec.Body.Bytes(), jobSaveFormSpec)

	// the original regression class this file guards against: mtime silently
	// changing from a JSON number to a string would pass a naive "key
	// present" check but must fail assertShape's kind check.
	var resp map[string]any
	dec := json.NewDecoder(bytes.NewReader(rec.Body.Bytes()))
	dec.UseNumber()
	if err := dec.Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if _, ok := resp["mtime"].(json.Number); !ok {
		t.Errorf("mtime = %#v, want json.Number", resp["mtime"])
	}
}

// jobRenameReqSpec / jobRenameRespSpec cover POST /api/job/rename
// (jobrename.go). Both request keys are literally required rather than merely
// non-omitempty: an empty `slug` or `to` is a 400, and the body is decoded
// with unknown fields rejected, so there is deliberately no third key to
// document.
//
// The response is NOT the bare ack its one-field siblings serve. `slug` rides
// back because the stored slug is `util.Slugify(to)` rather than the string
// the caller sent, and both clients re-point their open editor at it
// synchronously instead of reloading — an ack alone would leave them pointing
// at a slug that no longer resolves. `skipped` and `notes` are both omitempty
// and both report what a SUCCESSFUL rename could not finish, split by what the
// reader has to do about it: `skipped` names preserved artifact files still
// sitting under the old prefix because a file already occupied the new name
// (source paths, the files to move by hand), while `notes` carries best-effort
// steps that FAILED with their cause — an artifact rename that errored outright
// rather than colliding, a tmux convergence call, the zeus notice on a live
// cell. The empty case is the normal one for each, which is why the presence of
// either is worth reading. Both are additive: apiContract stays 5.
var jobRenameReqSpec = map[string]fieldSpec{
	"slug": {kind: wireString, required: true},
	"to":   {kind: wireString, required: true},
}

var jobRenameRespSpec = map[string]fieldSpec{
	"ok":      {kind: wireBool, required: true},
	"slug":    {kind: wireString, required: true},
	"skipped": {kind: wireArray, required: false},
	"notes":   {kind: wireArray, required: false},
}

// jobRenameErrSpec is the refusal envelope — the standard {"error": string}
// every refusal on this surface serves, pinned here because a rename refusal
// is the one a client RENDERS verbatim (the gates say things like "reserved
// prefix" and "unarchive it first" that only jobs.RenameJob can phrase).
var jobRenameErrSpec = map[string]fieldSpec{
	"error": {kind: wireString, required: true},
}

// TestJobRenameWireShape drives the real handler, so the shapes above are
// pinned as SERVED rather than as marshaled structs. Store mode: a rename is a
// store transaction, and useTestStore sandboxes HOME so the pluto artifact
// sweep and the cell lookups all land in a throwaway dir with no tmux session
// anywhere — the tmux convergence branch is skipped by construction.
func TestJobRenameWireShape(t *testing.T) {
	st := useTestStore(t)
	if _, err := st.CreateJob(&store.Job{Slug: "contract-rename"}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	reqData, err := json.Marshal(webJobRenameReq{Slug: "contract-rename", To: "contract-renamed"})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "webJobRenameReq", reqData, jobRenameReqSpec)

	body := mustJSON(t, map[string]any{"slug": "contract-rename", "to": "contract-renamed"})
	rec := httptest.NewRecorder()
	webJobRename(rec, httptest.NewRequest(http.MethodPost, "/api/job/rename", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/job/rename: status %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "POST /api/job/rename", rec.Body.Bytes(), jobRenameRespSpec)

	// the served slug is the STORED one, which is what the clients re-point to
	var resp struct {
		Slug string `json:"slug"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Slug != "contract-renamed" {
		t.Errorf("slug = %q, want %q", resp.Slug, "contract-renamed")
	}

	// a refusal: the reserved-prefix gate, which no client may work around.
	// 409 with the refusal text verbatim — the shape a client renders inline.
	body = mustJSON(t, map[string]any{"slug": "contract-renamed", "to": "dream-2026-08-12"})
	rec = httptest.NewRecorder()
	webJobRename(rec, httptest.NewRequest(http.MethodPost, "/api/job/rename", bytes.NewReader(body)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("POST /api/job/rename (reserved prefix): status %d, want 409, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "POST /api/job/rename (refusal)", rec.Body.Bytes(), jobRenameErrSpec)
}

// --- GET /api/feedback and GET /api/questions (feedback.go/questions.go) —
// custom envelopes, not the standard {"ok"}/{"error"} shape, so each gets
// its own spec exercised through the real handler. POST forms all resolve
// to the standard {"ok":true}/{"error":string} shapes already covered by
// the webAction-wrapped-endpoint convention noted above; their op dispatch
// and validation are covered by feedback_test.go/questions_test.go instead.

var feedbackGetSpec = map[string]fieldSpec{
	"feedback": {kind: wireArray, required: true},
}

func TestFeedbackGetWireShape(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := mustCreateStoreJob(t, st, store.Job{Slug: "contract-feedback", Repo: "monolith"})
	if _, err := st.AddFeedback(store.Feedback{JobID: j.ID, JobSlug: "contract-feedback", Artifact: "00-overview.md", Note: "n"}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/feedback?slug=contract-feedback", nil)
	rec := httptest.NewRecorder()
	feedbackHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "/api/feedback (GET)", rec.Body.Bytes(), feedbackGetSpec)
}

var questionsGetSpec = map[string]fieldSpec{
	"questions": {kind: wireArray, required: true},
}

func TestQuestionsGetWireShape(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := mustCreateStoreJob(t, st, store.Job{Slug: "contract-questions", Repo: "monolith"})
	if err := st.UpsertQuestion(j.ID, store.DocQuestion{QID: "Q1", Text: "which approach?", Importance: "P1"}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/questions?slug=contract-questions", nil)
	rec := httptest.NewRecorder()
	questionsHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "/api/questions (GET)", rec.Body.Bytes(), questionsGetSpec)
}

// --- GET /api/threads and GET /api/thread/read (threads.go) — two more
// custom envelopes, each exercised through its real handler. POST
// /api/threads resolves to {"ok":true} (plus "tid" on a branch) /
// {"error":string}; its op dispatch and per-op required fields are covered
// by threads_test.go instead.

var threadsGetSpec = map[string]fieldSpec{
	"threads": {kind: wireArray, required: true},
}

func TestThreadsGetWireShape(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := mustCreateStoreJob(t, st, store.Job{Slug: "contract-threads", Repo: "monolith"})
	if err := st.AddThread(store.Thread{JobID: j.ID, TID: "t1", Title: "why the retry loop", SessionID: "s1", Pane: "%42"}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/threads?slug=contract-threads", nil)
	rec := httptest.NewRecorder()
	threadsHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "/api/threads (GET)", rec.Body.Bytes(), threadsGetSpec)

	// the array's elements are webThreads — same spec the board's own
	// "threads" array carries, checked here through the live handler
	var resp struct {
		Threads []json.RawMessage `json:"threads"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Threads) != 1 {
		t.Fatalf("threads = %v, want 1 element", resp.Threads)
	}
	assertShape(t, "/api/threads (GET) element", resp.Threads[0], webThreadSpec)
}

// threadReadSpec: every key is always present — a source-"none" read (no
// transcript yet) still carries an empty blocks array, counted 0, and the
// row's own live/pane/fork_uuid, so a client never has to branch on absence.
var threadReadSpec = map[string]fieldSpec{
	"blocks":    {kind: wireArray, required: true},
	"counted":   {kind: wireNumber, required: true},
	"live":      {kind: wireBool, required: true},
	"pane":      {kind: wireString, required: true},
	"fork_uuid": {kind: wireString, required: true},
	"source":    {kind: wireString, required: true},
}

// threadReadBlockSpec is /api/thread/read's block element: /api/peers/read's
// block shape (peersReadBlockSpec) plus the side-thread-only "inherited"
// flag, present only on the pre-fork half.
var threadReadBlockSpec = map[string]fieldSpec{
	"kind":      {kind: wireString, required: true},
	"text":      {kind: wireString, required: true},
	"from":      {kind: wireString, required: false},
	"inherited": {kind: wireBool, required: false},
}

func TestThreadReadWireShape(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := mustCreateStoreJob(t, st, store.Job{Slug: "contract-thread-read", Repo: "monolith"})
	sess := "abcdabcd-1111-2222-3333-444444444444"
	if err := st.AddThread(store.Thread{
		JobID: j.ID, TID: "t1", SessionID: sess, ForkUUID: "a1", Pane: "%42", State: "live",
	}); err != nil {
		t.Fatal(err)
	}
	seedTranscript(t, sess,
		`{"type":"user","uuid":"u1","message":{"content":"zeus, do the thing"}}`,
		`{"type":"assistant","uuid":"a1","message":{"content":[{"type":"text","text":"doing it"}]}}`,
		`{"type":"user","uuid":"u2","message":{"content":"why the retry?"}}`,
	)

	req := httptest.NewRequest(http.MethodGet, "/api/thread/read?slug=contract-thread-read&tid=t1&n=20", nil)
	rec := httptest.NewRecorder()
	threadReadHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "/api/thread/read", rec.Body.Bytes(), threadReadSpec)

	var resp struct {
		Blocks []json.RawMessage `json:"blocks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Blocks) != 3 {
		t.Fatalf("blocks = %v, want 3", resp.Blocks)
	}
	assertShape(t, "/api/thread/read block (inherited)", resp.Blocks[0], threadReadBlockSpec)
	assertShape(t, "/api/thread/read block (own)", resp.Blocks[2], threadReadBlockSpec)
}

// --- board reorder: POST /api/reorder (actions.go) — a custom request shape
// unlike webReq, so it gets its own wire-shape spec. Success/failure
// responses are the standard {"ok":true} / {"error":string} shapes already
// covered by the webAction-wrapped-endpoint convention noted above; the
// happy-path/unknown-column/departed-slug behavior is covered by
// actions_test.go's TestWebReorder* handler tests.

var reorderReqSpec = map[string]fieldSpec{
	"column": {kind: wireString, required: true},
	"slugs":  {kind: wireArray, required: true, nullable: true},
}

func TestReorderReqWireShape(t *testing.T) {
	data, err := json.Marshal(reorderReq{Column: "queued", Slugs: []string{"a", "b"}})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "reorderReq", data, reorderReqSpec)
}

// --- draft: /api/draft GET/POST (draft.go) ---

var webDraftSpec = map[string]fieldSpec{
	"slug":       {kind: wireString, required: true},
	"text":       {kind: wireString, required: true},
	"updated_at": {kind: wireNumber, required: true},
}

func TestDraftGetWireShape(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	writeTestJob(t, "contract-draft", "")

	req := httptest.NewRequest(http.MethodGet, "/api/draft?slug=contract-draft", nil)
	rec := httptest.NewRecorder()
	draftHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("draftHandler (GET): status %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "/api/draft (GET)", rec.Body.Bytes(), webDraftSpec)
}

var draftPostOKSpec = map[string]fieldSpec{
	"ok": {kind: wireBool, required: true},
}

func TestDraftPostWireShape(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	writeTestJob(t, "contract-draft-post", "")

	body := mustJSON(t, webReq{Slug: "contract-draft-post", Text: "hello"})
	req := httptest.NewRequest(http.MethodPost, "/api/draft", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	draftHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("draftHandler (POST): status %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "/api/draft (POST)", rec.Body.Bytes(), draftPostOKSpec)
}

// jobSaveFormConflictSpec is the 409 shape webJobSaveForm returns for a
// stale-mtime conflict specifically — distinct from the generic {"error":
// string} error shape (see ios/API.md) by the extra "conflict": true key,
// which the editor uses to offer a force-save affordance.
var jobSaveFormConflictSpec = map[string]fieldSpec{
	"error":    {kind: wireString, required: true},
	"conflict": {kind: wireBool, required: true},
}

// --- uploads: POST /api/upload, resolved for reading via api.UploadPath
// (upload.go); the GET route itself lives in web/serve.go, outside this
// package — see UploadPath's own round-trip below for the read side. ---

var uploadRespSpec = map[string]fieldSpec{
	"name": {kind: wireString, required: true},
	"path": {kind: wireString, required: true},
	"url":  {kind: wireString, required: true},
}

func TestUploadRoundTrip(t *testing.T) {
	// jobs.SaveUpload is store-only now (H1: no nil-store fallback left in
	// saveUploadToStore) — the job itself must be a store row too, since
	// wiring a store switches jobs.LoadJobs/JobBySlug onto it exclusively.
	useTestStore(t)
	t.Setenv("KRAKEN_UPLOADS_DIR", t.TempDir())
	mustCreateStoreJob(t, jobs.ActiveStore(), store.Job{Slug: "contract-upload", Repo: "monolith"})

	png := smallPNG(t)
	req := multipartUploadRequest(t, "contract-upload", "photo.png", png)
	rec := httptest.NewRecorder()
	uploadHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("uploadHandler: status %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "POST /api/upload", rec.Body.Bytes(), uploadRespSpec)

	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["name"] != "img-001.png" {
		t.Errorf("name = %q, want img-001.png", resp["name"])
	}
	if _, err := os.Stat(resp["path"]); err != nil {
		t.Errorf("uploaded file not on disk: %v", err)
	}
	if want := "/api/upload/contract-upload/img-001.png"; resp["url"] != want {
		t.Errorf("url = %q, want %q", resp["url"], want)
	}

	// read side: web/serve.go's GET route resolves through this exact call
	p, err := UploadPath("contract-upload", "img-001.png")
	if err != nil {
		t.Fatalf("UploadPath: %v", err)
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, png) {
		t.Errorf("round-tripped bytes mismatch")
	}
}

func TestUploadUnknownSlug404(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	t.Setenv("KRAKEN_UPLOADS_DIR", t.TempDir())
	req := multipartUploadRequest(t, "no-such-job", "a.png", smallPNG(t))
	rec := httptest.NewRecorder()
	uploadHandler(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body %s", rec.Code, rec.Body.String())
	}
}

func TestUploadOversize413(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	t.Setenv("KRAKEN_UPLOADS_DIR", t.TempDir())
	writeTestJob(t, "contract-upload-big", "")
	big := bytes.Repeat([]byte{0}, 10<<20+1024) // just over the 10MB file cap, still under the 11MB body ceiling
	req := multipartUploadRequest(t, "contract-upload-big", "big.png", big)
	rec := httptest.NewRecorder()
	uploadHandler(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413, body %s", rec.Code, rec.Body.String())
	}
}

func TestUploadSniffOverridesFilename(t *testing.T) {
	// this path reaches jobs.SaveUpload (store-only now), unlike the
	// oversize/unsupported-type cases above which 413/415 before ever
	// calling it — so this one needs a store-backed job.
	useTestStore(t)
	t.Setenv("KRAKEN_UPLOADS_DIR", t.TempDir())
	mustCreateStoreJob(t, jobs.ActiveStore(), store.Job{Slug: "contract-upload-sniff", Repo: "monolith"})
	// a real PNG's bytes, dressed up with a lying .jpg filename
	req := multipartUploadRequest(t, "contract-upload-sniff", "photo.jpg", smallPNG(t))
	rec := httptest.NewRecorder()
	uploadHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["name"] != "img-001.png" {
		t.Errorf("name = %q, want img-001.png (sniff must win over the .jpg filename)", resp["name"])
	}
}

func TestUploadUnsupportedType415(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	t.Setenv("KRAKEN_UPLOADS_DIR", t.TempDir())
	writeTestJob(t, "contract-upload-text", "")
	req := multipartUploadRequest(t, "contract-upload-text", "notes.txt", []byte("just plain text, not an image"))
	rec := httptest.NewRecorder()
	uploadHandler(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415, body %s", rec.Code, rec.Body.String())
	}
}

func TestUploadPathTraversal404(t *testing.T) {
	t.Setenv("KRAKEN_UPLOADS_DIR", t.TempDir())
	if _, err := UploadPath("contract-upload", "../../x"); err == nil {
		t.Fatal("expected a traversal-shaped name to be rejected")
	}
}

func TestUploadBadMethod405(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/upload", nil)
	rec := httptest.NewRecorder()
	uploadHandler(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestJobSaveFormConflictWireShape(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	writeTestJob(t, "contract-saveform-conflict", "")

	// a deliberately wrong, non-zero mtime (real mtimes are unix millis,
	// nowhere near 1) reliably mismatches the file's actual mtime without
	// needing to fetch it first.
	body := mustJSON(t, map[string]any{
		"slug": "contract-saveform-conflict", "repo": "monolith", "mtime": 1,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/job/saveform", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	webJobSaveForm(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("webJobSaveForm: status %d, body %s, want 409", rec.Code, rec.Body.String())
	}
	assertShape(t, "/api/job/saveform (409 conflict)", rec.Body.Bytes(), jobSaveFormConflictSpec)
}

// --- unified event log: /api/logs/files, /api/logs ---

var logFilesSpec = map[string]fieldSpec{
	"files": {kind: wireArray, required: true},
}

var logReadSpec = map[string]fieldSpec{
	"file":    {kind: wireString, required: true},
	"prev":    {kind: wireString, required: true},
	"entries": {kind: wireArray, required: true},
}

// logEntrySpec covers util.LogEntry, the elements of /api/logs "entries".
var logEntrySpec = map[string]fieldSpec{
	"ts":    {kind: wireNumber, required: true},
	"src":   {kind: wireString, required: true},
	"level": {kind: wireString, required: true},
	"event": {kind: wireString, required: true},
	"msg":   {kind: wireString, required: true},
}

// logFileInfoSpec covers util.LogFileInfo, the elements of /api/logs/files
// "files".
var logFileInfoSpec = map[string]fieldSpec{
	"name":  {kind: wireString, required: true},
	"size":  {kind: wireNumber, required: true},
	"mtime": {kind: wireNumber, required: true},
}

func TestLogsWireShape(t *testing.T) {
	// webLogFiles/webLogRead are store-only now (the single virtual "events"
	// file — see logs.go's doc comment); util.LogEvent's sink additively
	// lands the same call in both the JSONL file (unused by these handlers
	// once a store is wired) and the events table once jobs.UseStore has run.
	useTestStore(t)
	t.Setenv("KRAKEN_LOGS_DIR", t.TempDir())
	util.LogEvent("web", "info", "queue", "queue contract-test")

	rec := httptest.NewRecorder()
	webLogFiles(rec, httptest.NewRequest(http.MethodGet, "/api/logs/files", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("webLogFiles: status %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "/api/logs/files", rec.Body.Bytes(), logFilesSpec)

	// util.LogFileInfo's own wire shape, pinned directly (store mode never
	// calls util.ListLogFiles itself — webLogFiles builds the single virtual
	// "events" entry inline instead).
	data, err := json.Marshal(util.LogFileInfo{Name: "events", Size: 1, MTime: 2})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "util.LogFileInfo", data, logFileInfoSpec)

	// no ?file= → newest file
	rec = httptest.NewRecorder()
	webLogRead(rec, httptest.NewRequest(http.MethodGet, "/api/logs", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("webLogRead: status %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "/api/logs", rec.Body.Bytes(), logReadSpec)

	entry, err := json.Marshal(util.LogEntry{TS: 1, Src: "web", Level: "info", Event: "queue", Msg: "m"})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "util.LogEntry", entry, logEntrySpec)

	// a name outside the listing must 404, never read a path
	rec = httptest.NewRecorder()
	webLogRead(rec, httptest.NewRequest(http.MethodGet, "/api/logs?file=../../etc/passwd", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("traversal name: status %d, want 404", rec.Code)
	}
}

// --- the dream loop's routes: GET /api/feedback/sweep, GET /api/watchlist +
// its two POSTs, GET /api/dream (feedback.go, watchlist.go, dream.go).
//
// All four are ADDITIVE — new endpoints, no key retyped/renamed/removed
// anywhere — so apiContract stays where it was; TestAPIContractUnchanged
// below pins that explicitly rather than leaving it to be noticed.
//
// GET /api/feedback/snapshot has no spec here on purpose: it serves raw
// bytes, not JSON (its headers and 400/404 shapes are covered in
// feedback_test.go), the same reason /api/artifact has none.

var feedbackSweepSpec = map[string]fieldSpec{
	"feedback": {kind: wireArray, required: true},
}

// webSweepFeedbackSpec covers the elements of that array — feedbackSweepSpec
// itself only asserts it IS an array.
var webSweepFeedbackSpec = map[string]fieldSpec{
	"id":           {kind: wireNumber, required: true},
	"job":          {kind: wireString, required: true},
	"artifact":     {kind: wireString, required: true},
	"note":         {kind: wireString, required: true},
	"at":           {kind: wireNumber, required: true},
	"addressed":    {kind: wireString, required: false},
	"addressed_at": {kind: wireString, required: false},
	"snapshot_url": {kind: wireString, required: true},
}

func TestFeedbackSweepWireShape(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := mustCreateStoreJob(t, st, store.Job{Slug: "contract-sweep", Repo: "monolith"})
	if _, err := st.AddFeedback(store.Feedback{JobID: j.ID, JobSlug: "contract-sweep", Artifact: "00-overview.md", Note: "n", TS: 5}); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	feedbackSweepHandler(rec, httptest.NewRequest(http.MethodGet, "/api/feedback/sweep?since=0", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "/api/feedback/sweep", rec.Body.Bytes(), feedbackSweepSpec)

	var resp struct {
		Feedback []json.RawMessage `json:"feedback"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Feedback) != 1 {
		t.Fatalf("want one swept row, got %d", len(resp.Feedback))
	}
	assertShape(t, "/api/feedback/sweep element (empty optionals)", resp.Feedback[0], webSweepFeedbackSpec)

	populated, err := json.Marshal(webSweepFeedback{
		ID: 1, Job: "j", Artifact: "a.md", Note: "n", At: 5,
		Addressed: "done", AddressedAt: "2026-07-26T00:00:00Z", SnapshotURL: "/api/feedback/snapshot?id=1",
	})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "webSweepFeedback (populated)", populated, webSweepFeedbackSpec)
}

var watchlistGetSpec = map[string]fieldSpec{
	"watchlist": {kind: wireArray, required: true},
}

var webWatchItemSpec = map[string]fieldSpec{
	"id":            {kind: wireNumber, required: true},
	"observation":   {kind: wireString, required: true},
	"class":         {kind: wireString, required: true},
	"state":         {kind: wireString, required: true},
	"sightings":     {kind: wireNumber, required: true},
	"first_seen":    {kind: wireNumber, required: true},
	"last_seen":     {kind: wireNumber, required: true},
	"evidence":      {kind: wireString, required: false},
	"suggested_fix": {kind: wireString, required: false},
	"pr_url":        {kind: wireString, required: false},
	"updated_at":    {kind: wireNumber, required: true},
	"verdict":       {kind: wireString, required: false},
	"comment":       {kind: wireString, required: false},
	"commented_at":  {kind: wireString, required: false},
}

func TestWatchlistGetWireShape(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	// A watch item is an edge plus an overlay now; the WIRE shape below is
	// unchanged, which is exactly what this test exists to pin.
	seedWatchItem(t, st, "o", store.WatchItem{Class: "waste"})

	rec := httptest.NewRecorder()
	watchlistHandler(rec, httptest.NewRequest(http.MethodGet, "/api/watchlist", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "/api/watchlist (GET)", rec.Body.Bytes(), watchlistGetSpec)

	var resp struct {
		Watchlist []json.RawMessage `json:"watchlist"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Watchlist) != 1 {
		t.Fatalf("want one row, got %d", len(resp.Watchlist))
	}
	assertShape(t, "webWatchItem (empty optionals)", resp.Watchlist[0], webWatchItemSpec)

	populated, err := json.Marshal(webWatchItem{
		ID: 1, Observation: "o", Class: "waste", State: "watching", Sightings: 3,
		FirstSeen: 1, LastSeen: 2, Evidence: `{"n":1}`, SuggestedFix: "fix",
		PRURL: "https://example/pr/1", UpdatedAt: 2, Verdict: "fix", Comment: "c",
		CommentedAt: "2026-07-26T00:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "webWatchItem (populated)", populated, webWatchItemSpec)
}

// watchVerdictReqSpec / watchItemReqSpec / watchItemWireSpec pin the REQUEST
// shapes of the two writes. Both bodies get their own spec for the reason
// updateReqSpec does: a round-trip through the handler's own struct renames
// both sides at once when a json tag changes, so only a transcribed spec can
// catch the drift.
var watchVerdictReqSpec = map[string]fieldSpec{
	"id":      {kind: wireNumber, required: true},
	"verdict": {kind: wireString, required: true},
	"comment": {kind: wireString, required: false},
}

var watchItemReqSpec = map[string]fieldSpec{
	"op":   {kind: wireString, required: true},
	"item": {kind: wireObject, required: false},
	"id":   {kind: wireNumber, required: false},
}

// watchItemWireSpec carries src/dst/predicate/strength alongside the overlay
// fields: a watch item is a graph edge, so an `add` that mints a new one has
// to name its endpoints and state the extractor's confidence. This is a
// REQUEST shape on the agent's CLI-facing route — never decoded by the iOS app
// or the web UI — so the added keys are not an apiContract bump.
var watchItemWireSpec = map[string]fieldSpec{
	"id":            {kind: wireNumber, required: false},
	"observation":   {kind: wireString, required: false},
	"class":         {kind: wireString, required: false},
	"state":         {kind: wireString, required: false},
	"sightings":     {kind: wireNumber, required: false},
	"first_seen":    {kind: wireNumber, required: false},
	"last_seen":     {kind: wireNumber, required: false},
	"evidence":      {kind: wireString, required: false},
	"suggested_fix": {kind: wireString, required: false},
	"pr_url":        {kind: wireString, required: false},
	"src":           {kind: wireString, required: false},
	"dst":           {kind: wireString, required: false},
	"predicate":     {kind: wireString, required: false},
	"strength":      {kind: wireNumber, required: false},
}

// watchItemRespSpec is the agent write's 200: {"ok":true,"id":N} plus
// `folded` on an add. The id is the reason this isn't the standard
// {"ok":true} — a caller that just watched something has no other way to
// learn its edge id — and `folded` is the reason it isn't only the id: an add
// that landed on an EXISTING observation is a repeat sighting, and the caller
// is told rather than left to infer it from an id it didn't recognise. Both
// are CLI-facing only, so neither is an apiContract bump.
var watchItemRespSpec = map[string]fieldSpec{
	"ok":     {kind: wireBool, required: true},
	"id":     {kind: wireNumber, required: true},
	"folded": {kind: wireBool, required: false},
}

// watchRemoveRespSpec is op `remove`'s own 200, and its own spec because it
// carries NO id: the caller named the item, so echoing it back says nothing,
// whereas `unwatched` says the thing that is easy to get wrong — the overlay
// is gone and the FACT survives in the graph. Unlearning a fact is
// /api/graph/add's invalidate list, a different and much louder act.
var watchRemoveRespSpec = map[string]fieldSpec{
	"ok":        {kind: wireBool, required: true},
	"unwatched": {kind: wireBool, required: true},
}

func TestWatchlistWriteWireShapes(t *testing.T) {
	verdictReq, err := json.Marshal(watchVerdictReq{ID: 1, Verdict: "fix", Comment: "c"})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "watchVerdictReq", verdictReq, watchVerdictReqSpec)

	itemReq, err := json.Marshal(watchItemReq{Op: "update", Item: &watchItemWire{ID: 1, Class: "waste"}, ID: 0})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "watchItemReq", itemReq, watchItemReqSpec)

	itemWire, err := json.Marshal(watchItemWire{
		ID: 1, Observation: "o", Class: "waste", State: "watching", Sightings: 2,
		FirstSeen: 1, LastSeen: 2, Evidence: `{"n":1}`, SuggestedFix: "fix", PRURL: "u",
		Src: "dream pass", Dst: "kraken", Predicate: "concerns", Strength: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "watchItemWire", itemWire, watchItemWireSpec)

	useTestStore(t)
	rec := httptest.NewRecorder()
	body := mustJSON(t, map[string]any{"op": "add", "item": map[string]any{
		"observation": "o", "class": "waste", "src": "dream pass", "dst": "kraken", "strength": 5,
	}})
	watchItemHandler(rec, httptest.NewRequest(http.MethodPost, "/api/watchlist/item", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "POST /api/watchlist/item (add)", rec.Body.Bytes(), watchItemRespSpec)

	// remove's 200 is a different shape from add's and gets its own assertion:
	// it drops `id` and carries `unwatched`, and an unpinned response body is
	// exactly how a key goes missing without a test noticing.
	var added struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &added); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	body = mustJSON(t, map[string]any{"op": "remove", "id": added.ID})
	watchItemHandler(rec, httptest.NewRequest(http.MethodPost, "/api/watchlist/item", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("remove: status %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "POST /api/watchlist/item (remove)", rec.Body.Bytes(), watchRemoveRespSpec)
}

var dreamGetSpec = map[string]fieldSpec{
	"reports": {kind: wireArray, required: true},
}

var webDreamReportSpec = map[string]fieldSpec{
	"date": {kind: wireString, required: true},
	"slug": {kind: wireString, required: true},
	"body": {kind: wireString, required: true},
}

func TestDreamWireShape(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	seedDreamReport(t, st, "dream-2026-07-26", "# report")

	rec := httptest.NewRecorder()
	dreamHandler(rec, httptest.NewRequest(http.MethodGet, "/api/dream", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "/api/dream", rec.Body.Bytes(), dreamGetSpec)

	var resp struct {
		Reports []json.RawMessage `json:"reports"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Reports) != 1 {
		t.Fatalf("want one report, got %d", len(resp.Reports))
	}
	assertShape(t, "/api/dream element", resp.Reports[0], webDreamReportSpec)
}

// --- the knowledge graph's ten routes (graph.go, recall.go) ---
//
// All additive: each is a NEW PATH, never a key added to, retyped in, or
// removed from an existing shape, so none of them ever bumps apiContract.
//
// None of these are modeled in Models.swift or APIClient.swift on purpose —
// the iOS graph screen is a WKWebView onto the web board's own #/graph route
// (GraphView.swift), so the app decodes none of these shapes natively. They
// get specs here anyway, because ios/API.md documents them and this file's
// job is to make that document executable: the web UI and the `k graph` cell
// verb are real clients, and assertShape's undocumented-extra-key check is
// what keeps a served shape from drifting away from the doc.

var graphGetSpec = map[string]fieldSpec{
	"nodes": {kind: wireArray, required: true},
	"edges": {kind: wireArray, required: true},
	"stats": {kind: wireObject, required: true},
}

// webGraphNodeSpec: "aliases" is the only omitempty key — both GET /api/graph
// (one bulk join; the nightly ingest's canonicalization inventory needs them)
// and GET /api/graph/node populate it, and a node with no aliases omits the
// key rather than sending an empty array. Every other key is required,
// including x and y: a node at the origin is a node whose layout has not been
// computed yet, which is a real answer, and an absent key would be
// indistinguishable from it.
var webGraphNodeSpec = map[string]fieldSpec{
	"id":          {kind: wireNumber, required: true},
	"name":        {kind: wireString, required: true},
	"type":        {kind: wireString, required: true},
	"description": {kind: wireString, required: true},
	"x":           {kind: wireNumber, required: true},
	"y":           {kind: wireNumber, required: true},
	"aliases":     {kind: wireArray, required: false},
}

// webGraphEdgeSpec: no omitempty anywhere. Both time axes ride unconditionally
// — valid_until is world time and expired_at is system time, 0 is meaningful
// on both ("still true" / "live"), and the viewer reads them to decide whether
// to strike a fact through.
var webGraphEdgeSpec = map[string]fieldSpec{
	"id":          {kind: wireNumber, required: true},
	"src":         {kind: wireNumber, required: true},
	"dst":         {kind: wireNumber, required: true},
	"predicate":   {kind: wireString, required: true},
	"fact":        {kind: wireString, required: true},
	"strength":    {kind: wireNumber, required: true},
	"occurrences": {kind: wireNumber, required: true},
	"valid_until": {kind: wireNumber, required: true},
	"expired_at":  {kind: wireNumber, required: true},
	"episode":     {kind: wireNumber, required: true},
}

// webGraphStatsSpec. The camelCase on two keys is deliberate and documented
// in ios/API.md: it is the spelling the graph viewer was built against, and
// renaming it here would break that client rather than tidy this file.
var webGraphStatsSpec = map[string]fieldSpec{
	"nodes":              {kind: wireNumber, required: true},
	"edges":              {kind: wireNumber, required: true},
	"live_edges":         {kind: wireNumber, required: true},
	"episodes":           {kind: wireNumber, required: true},
	"pending":            {kind: wireNumber, required: true},
	"missing_embeddings": {kind: wireNumber, required: true},
}

var graphStatsGetSpec = map[string]fieldSpec{
	"stats": {kind: wireObject, required: true},
}

var graphNodeGetSpec = map[string]fieldSpec{
	"node":  {kind: wireObject, required: true},
	"edges": {kind: wireArray, required: true},
}

var graphPendingGetSpec = map[string]fieldSpec{
	"episodes": {kind: wireArray, required: true},
}

// webGraphEpisodeSpec. The row's `hash` and `state` are deliberately absent
// from the wire — hash is the store's internal dedupe key and every row this
// route can serve is 'pending' — so a spec entry for either would be the
// first sign one had leaked out.
var webGraphEpisodeSpec = map[string]fieldSpec{
	"id":      {kind: wireNumber, required: true},
	"kind":    {kind: wireString, required: true},
	"ref":     {kind: wireString, required: true},
	"content": {kind: wireString, required: true},
	"ts":      {kind: wireNumber, required: true},
}

// graphAddReqSpec / graphAddRespSpec cover POST /api/graph/add. The request
// carries no per-item ids: endpoints are named by NAME, which is what lets an
// extraction agent state a fact without a resolve round trip first.
var graphAddReqSpec = map[string]fieldSpec{
	"episode_id": {kind: wireNumber, required: true},
	"nodes":      {kind: wireArray, required: true, nullable: true},
	"edges":      {kind: wireArray, required: true, nullable: true},
	"invalidate": {kind: wireArray, required: true, nullable: true},
}

var graphAddRespSpec = map[string]fieldSpec{
	"nodes":       {kind: wireArray, required: true},
	"edges":       {kind: wireArray, required: true},
	"invalidated": {kind: wireArray, required: true},
}

var graphAddNodeResultSpec = map[string]fieldSpec{
	"name":  {kind: wireString, required: true},
	"id":    {kind: wireNumber, required: true},
	"added": {kind: wireBool, required: true},
}

var graphAddEdgeResultSpec = map[string]fieldSpec{
	"src":   {kind: wireString, required: true},
	"dst":   {kind: wireString, required: true},
	"id":    {kind: wireNumber, required: true},
	"added": {kind: wireBool, required: true},
}

var graphEpisodeDoneRespSpec = map[string]fieldSpec{
	"ok": {kind: wireBool, required: true},
}

// graphSearchReqSpec / graphSearchRespSpec cover POST /api/graph/search.
// There is no "score" key in the fact spec on purpose: array ORDER carries
// the ranking, and a spec entry for a score would be the first sign somebody
// had put a fusion-internal number on the wire for clients to threshold on.
var graphSearchReqSpec = map[string]fieldSpec{
	"query": {kind: wireString, required: true},
	"limit": {kind: wireNumber, required: true},
}

// graphSearchRespSpec. "nodes" carries the entities the matched facts touch,
// which is what .claude/skills/graph's canonicalization step resolves an
// incoming name against — facts alone answer that only indirectly. Its
// elements are webGraphNode, the same type GET /api/graph serves, so they
// reuse webGraphNodeSpec; x/y ride as 0 because a search result is a list to
// read rather than a canvas to draw.
var graphSearchRespSpec = map[string]fieldSpec{
	"nodes":   {kind: wireArray, required: true},
	"facts":   {kind: wireArray, required: true},
	"triples": {kind: wireString, required: true},
	"strong":  {kind: wireNumber, required: true},
}

// graphRecallReqSpec / graphRecallRespSpec cover POST /api/graph/recall
// (recall.go). Every request key is required rather than omitempty:
// jobs.RecallRequest carries no omitempty tags, and the shape's only caller is
// `kraken recall-hook`, which fills all five from one hook payload plus its own
// argv — a key that vanished when the hook had no prompt would make "no prompt"
// and "field removed from the payload schema" the same wire fact.
//
// "slug" is the cell's job slug, and it is required-but-often-empty for exactly
// that reason: a worktree whose settings.json predates the argument sends `""`,
// which recall reads as "fall back to the session match" — a distinction that
// only survives if the key is always on the wire.
var graphRecallReqSpec = map[string]fieldSpec{
	"session_id":      {kind: wireString, required: true},
	"event":           {kind: wireString, required: true},
	"prompt":          {kind: wireString, required: true},
	"transcript_path": {kind: wireString, required: true},
	"slug":            {kind: wireString, required: true},
}

// graphRecallRespSpec. One key, always present, and `""` is its most common
// value — the graph having nothing to add is the normal answer, not an error,
// so there is no "error"/"ok" key to spec here on the success path. An
// omitempty on "context" would erase the distinction between an empty block
// and an older server that does not serve this route at all.
var graphRecallRespSpec = map[string]fieldSpec{
	"context": {kind: wireString, required: true},
}

// TestGraphRecallWireShape asserts both recall specs against the real types —
// the request as `kraken recall-hook` marshals it (jobs.RecallRequest), the
// response as the live handler serves it — so the two specs above are
// enforced, not merely transcribed. The empty-context answer is the shape's
// common case (see graphRecallRespSpec's comment), so that is the one driven.
func TestGraphRecallWireShape(t *testing.T) {
	useTestStore(t)
	reqData, err := json.Marshal(jobs.RecallRequest{SessionID: "sess", Event: "UserPromptSubmit", Prompt: "p", TranscriptPath: ""})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "graphRecallReq", reqData, graphRecallReqSpec)

	rec := httptest.NewRecorder()
	graphRecallHandler(rec, httptest.NewRequest(http.MethodPost, "/api/graph/recall",
		bytes.NewReader([]byte(`{"session_id":"sess","event":"UserPromptSubmit","prompt":"p","transcript_path":""}`))))
	if rec.Code != http.StatusOK {
		t.Fatalf("recall = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	assertShape(t, "graphRecallResp", rec.Body.Bytes(), graphRecallRespSpec)
}

var webGraphFactSpec = map[string]fieldSpec{
	"id":          {kind: wireNumber, required: true},
	"src":         {kind: wireString, required: true},
	"dst":         {kind: wireString, required: true},
	"predicate":   {kind: wireString, required: true},
	"fact":        {kind: wireString, required: true},
	"strength":    {kind: wireNumber, required: true},
	"occurrences": {kind: wireNumber, required: true},
	"valid_until": {kind: wireNumber, required: true},
	"weak":        {kind: wireBool, required: true},
}

// --- the dereference pair: GET /api/graph/fact, GET /api/graph/episode ---
//
// The two reads `k graph get` follows a `[F# E#]` handle with. Additive paths
// like every route above, so apiContract still does not move; not modeled in
// Models.swift or APIClient.swift either, since the iOS graph screen consumes
// only /api/graph and /api/graph/node.

// graphFactGetSpec. "episode" is required-but-nullable rather than omitempty
// on purpose: an edge whose episode row is gone serves an explicit null, and
// that is a different claim from a key that vanished — "kraken cannot say
// where this came from" versus "this server does not report provenance".
var graphFactGetSpec = map[string]fieldSpec{
	"fact":     {kind: wireObject, required: true},
	"src":      {kind: wireObject, required: true},
	"dst":      {kind: wireObject, required: true},
	"episode":  {kind: wireObject, required: true, nullable: true},
	"siblings": {kind: wireObject, required: true},
}

var graphFactSiblingsSpec = map[string]fieldSpec{
	"src": {kind: wireArray, required: true},
	"dst": {kind: wireArray, required: true},
}

var graphEpisodeGetSpec = map[string]fieldSpec{
	"episode": {kind: wireObject, required: true},
	"edges":   {kind: wireArray, required: true},
}

// webGraphFactFullSpec: no omitempty anywhere, and both time axes ride in
// FULL — valid_from beside valid_until (world time), created_at beside
// expired_at (system time). That is what separates this shape from
// webGraphFact, which a ranked search result trims because a list is scanned
// rather than studied. A spec entry appearing for "weak" here would be the
// first sign a relevance verdict had leaked onto a route with no query to
// judge against.
var webGraphFactFullSpec = map[string]fieldSpec{
	"id":          {kind: wireNumber, required: true},
	"src":         {kind: wireString, required: true},
	"dst":         {kind: wireString, required: true},
	"predicate":   {kind: wireString, required: true},
	"fact":        {kind: wireString, required: true},
	"strength":    {kind: wireNumber, required: true},
	"occurrences": {kind: wireNumber, required: true},
	"valid_from":  {kind: wireNumber, required: true},
	"valid_until": {kind: wireNumber, required: true},
	"created_at":  {kind: wireNumber, required: true},
	"expired_at":  {kind: wireNumber, required: true},
	"episode":     {kind: wireNumber, required: true},
}

// webGraphSiblingFactSpec: webGraphFact minus "weak" (no query, so no verdict)
// — and, like webGraphFact, without the system-time stamps, since siblings come
// from NeighborEdges and are live-only.
var webGraphSiblingFactSpec = map[string]fieldSpec{
	"id":          {kind: wireNumber, required: true},
	"src":         {kind: wireString, required: true},
	"dst":         {kind: wireString, required: true},
	"predicate":   {kind: wireString, required: true},
	"fact":        {kind: wireString, required: true},
	"strength":    {kind: wireNumber, required: true},
	"occurrences": {kind: wireNumber, required: true},
	"valid_until": {kind: wireNumber, required: true},
}

// webGraphEpisodeRefSpec. "content" is deliberately absent — the fact bundle
// carries a provenance POINTER, and a spec entry for content would be the
// first sign kilobytes of reduced transcript had started riding along with
// every dereferenced sentence.
var webGraphEpisodeRefSpec = map[string]fieldSpec{
	"id":   {kind: wireNumber, required: true},
	"kind": {kind: wireString, required: true},
	"ref":  {kind: wireString, required: true},
}

// TestGraphDereferenceWireShapes drives both dereference handlers against a
// store seeded through the documented add path, so every shape below is one
// the handlers themselves produced rather than a struct marshaled in a test.
func TestGraphDereferenceWireShapes(t *testing.T) {
	st := useTestStore(t)
	epID, added, err := st.AddEpisode(store.Episode{
		Kind: "transcript", Ref: "/x/y.jsonl#0-400", Content: "some reduced text",
		Hash: "deref-h1", TS: 1769040000000,
	})
	if err != nil || !added {
		t.Fatalf("AddEpisode: id=%d added=%v err=%v", epID, added, err)
	}

	// Two edges sharing the src endpoint, so the dereferenced fact has a real
	// sibling to render rather than only an empty list.
	addBody := mustJSON(t, map[string]any{
		"episode_id": epID,
		"nodes": []any{
			map[string]any{"name": "kraken", "type": "system", "description": "the CLI", "aliases": []string{"k"}},
		},
		"edges": []any{
			map[string]any{"src": "kraken", "dst": "zeus", "predicate": "spawns",
				"fact": "kraken spawns zeus in a cell", "strength": 8},
			map[string]any{"src": "kraken", "dst": "carl", "predicate": "serves",
				"fact": "kraken serves carl the board", "strength": 7},
		},
	})
	rec := httptest.NewRecorder()
	graphAddHandler(rec, httptest.NewRequest(http.MethodPost, "/api/graph/add", bytes.NewReader(addBody)))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/graph/add: status %d, body %s", rec.Code, rec.Body.String())
	}
	var addResp struct {
		Edges []struct {
			ID int64 `json:"id"`
		} `json:"edges"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &addResp); err != nil {
		t.Fatal(err)
	}
	if len(addResp.Edges) != 2 {
		t.Fatalf("add results: got %d edges, want 2", len(addResp.Edges))
	}
	edgeID := addResp.Edges[0].ID

	rec = httptest.NewRecorder()
	graphFactHandler(rec, httptest.NewRequest(http.MethodGet,
		"/api/graph/fact?id="+strconv.FormatInt(edgeID, 10), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/graph/fact: status %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "GET /api/graph/fact", rec.Body.Bytes(), graphFactGetSpec)
	var factResp struct {
		Fact     json.RawMessage `json:"fact"`
		Src      json.RawMessage `json:"src"`
		Dst      json.RawMessage `json:"dst"`
		Episode  json.RawMessage `json:"episode"`
		Siblings json.RawMessage `json:"siblings"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &factResp); err != nil {
		t.Fatal(err)
	}
	assertShape(t, "GET /api/graph/fact .fact", factResp.Fact, webGraphFactFullSpec)
	// Both endpoints are webGraphNode — the same type the viewer and the
	// inspector serve — so a rename on either side fails here too.
	assertShape(t, "GET /api/graph/fact .src", factResp.Src, webGraphNodeSpec)
	assertShape(t, "GET /api/graph/fact .dst", factResp.Dst, webGraphNodeSpec)
	assertShape(t, "GET /api/graph/fact .episode", factResp.Episode, webGraphEpisodeRefSpec)
	assertShape(t, "GET /api/graph/fact .siblings", factResp.Siblings, graphFactSiblingsSpec)
	var siblings struct {
		Src []json.RawMessage `json:"src"`
		Dst []json.RawMessage `json:"dst"`
	}
	if err := json.Unmarshal(factResp.Siblings, &siblings); err != nil {
		t.Fatal(err)
	}
	if len(siblings.Src) != 1 {
		t.Fatalf("siblings.src: got %d, want 1 (the other edge on the shared src, minus the fact itself)", len(siblings.Src))
	}
	assertShape(t, "GET /api/graph/fact .siblings.src[0]", siblings.Src[0], webGraphSiblingFactSpec)

	rec = httptest.NewRecorder()
	graphEpisodeHandler(rec, httptest.NewRequest(http.MethodGet,
		"/api/graph/episode?id="+strconv.FormatInt(epID, 10), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/graph/episode: status %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "GET /api/graph/episode", rec.Body.Bytes(), graphEpisodeGetSpec)
	var epResp struct {
		Episode json.RawMessage   `json:"episode"`
		Edges   []json.RawMessage `json:"edges"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &epResp); err != nil {
		t.Fatal(err)
	}
	// The episode route DOES inline content — pulling the provenance text is
	// its purpose — so it serves the full webGraphEpisode, not the pointer.
	assertShape(t, "GET /api/graph/episode .episode", epResp.Episode, webGraphEpisodeSpec)
	if len(epResp.Edges) != 2 {
		t.Fatalf("episode edges: got %d, want 2", len(epResp.Edges))
	}
	assertShape(t, "GET /api/graph/episode .edges[0]", epResp.Edges[0], webGraphFactFullSpec)
}

// TestGraphWireShapesMarshaled pins the element types directly. The envelope
// specs above only assert that "nodes"/"edges"/"facts"/"episodes" ARE arrays;
// these check what an element of each looks like, the same split
// TestWebPrereqWireShape and friends use for webJob's arrays.
func TestGraphWireShapesMarshaled(t *testing.T) {
	// A node with no cached layout position and no aliases — the state every
	// node is in before the layout pass has run, so it must serialize with x
	// and y present and zero rather than absent.
	data, err := json.Marshal(webGraphNode{ID: 1, Name: "kraken", Type: "system", Description: "the CLI"})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "webGraphNode (unpositioned)", data, webGraphNodeSpec)

	data, err = json.Marshal(webGraphNode{
		ID: 1, Name: "kraken", Type: "system", Description: "the CLI",
		X: -12.5, Y: 88.25, Aliases: []string{"k"},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "webGraphNode (positioned, aliased)", data, webGraphNodeSpec)

	// A LIVE edge: valid_until and expired_at both zero, and both still
	// present — the difference between "still true" and "no answer".
	data, err = json.Marshal(webGraphEdge{
		ID: 7, Src: 1, Dst: 2, Predicate: "spawns", Fact: "kraken spawns zeus in a cell",
		Strength: 8, Occurrences: 3, Episode: 12,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "webGraphEdge (live)", data, webGraphEdgeSpec)

	data, err = json.Marshal(webGraphEdge{
		ID: 7, Src: 1, Dst: 2, Predicate: "spawns", Fact: "kraken spawns zeus in a cell",
		Strength: 8, Occurrences: 3, ValidUntil: 1769040000000, ExpiredAt: 1769050000000, Episode: 12,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "webGraphEdge (invalidated)", data, webGraphEdgeSpec)

	// A genuinely-empty graph: every counter must still be present, which is
	// what distinguishes "measured, and it was zero" from "no stats".
	data, err = json.Marshal(webGraphStats{})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "webGraphStats (empty graph)", data, webGraphStatsSpec)

	data, err = json.Marshal(webGraphEpisode{
		ID: 3, Kind: "transcript", Ref: "/x/y.jsonl#0-400", Content: "…", TS: 1769040000000,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "webGraphEpisode", data, webGraphEpisodeSpec)

	data, err = json.Marshal(webGraphFact{
		ID: 7, Src: "kraken", Dst: "zeus", Predicate: "spawns",
		Fact: "kraken spawns zeus in a cell", Strength: 8, Occurrences: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "webGraphFact", data, webGraphFactSpec)

	strength := int64(8)
	reqData, err := json.Marshal(graphAddReq{
		EpisodeID: 12,
		Nodes:     []graphAddNode{{Name: "kraken", Type: "system", Description: "the CLI", Aliases: []string{"k"}}},
		Edges: []graphAddEdge{{
			Src: "kraken", Dst: "zeus", Predicate: "spawns",
			Fact: "kraken spawns zeus in a cell", Strength: strength,
		}},
		Invalidate: []graphInvalidateEntry{{EdgeID: 4, ValidUntil: 1769040000000}},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "graphAddReq", reqData, graphAddReqSpec)

	reqData, err = json.Marshal(graphSearchReq{Query: "how does a cell spawn", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "graphSearchReq", reqData, graphSearchReqSpec)
}

// TestGraphEndpointWireShapes drives the real handlers, so the envelopes are
// pinned as SERVED rather than as marshaled structs — the lesson the update
// button taught (a round-trip through the handler's own Go type renames both
// sides at once and stays green while a real client breaks).
func TestGraphEndpointWireShapes(t *testing.T) {
	useTestStore(t)

	// An empty graph first: every envelope key must be present and every
	// array non-null before a single row exists, since that is the state a
	// fresh install serves and the clients render it unconditionally.
	rec := httptest.NewRecorder()
	graphHandler(rec, httptest.NewRequest(http.MethodGet, "/api/graph", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/graph (empty): status %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "GET /api/graph (empty)", rec.Body.Bytes(), graphGetSpec)

	rec = httptest.NewRecorder()
	graphStatsHandler(rec, httptest.NewRequest(http.MethodGet, "/api/graph/stats", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/graph/stats: status %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "GET /api/graph/stats", rec.Body.Bytes(), graphStatsGetSpec)
	var statsEnvelope struct {
		Stats json.RawMessage `json:"stats"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &statsEnvelope); err != nil {
		t.Fatal(err)
	}
	assertShape(t, "GET /api/graph/stats .stats", statsEnvelope.Stats, webGraphStatsSpec)

	rec = httptest.NewRecorder()
	graphPendingHandler(rec, httptest.NewRequest(http.MethodGet, "/api/graph/pending", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/graph/pending: status %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "GET /api/graph/pending (empty)", rec.Body.Bytes(), graphPendingGetSpec)

	// Now write a real episode's worth of graph through the documented add
	// path, so every populated shape below is one the handler itself produced.
	addBody := mustJSON(t, map[string]any{
		"episode_id": 0,
		"nodes": []any{
			map[string]any{"name": "kraken", "type": "system", "description": "the CLI", "aliases": []string{"k"}},
		},
		"edges": []any{
			map[string]any{"src": "kraken", "dst": "zeus", "predicate": "spawns",
				"fact": "kraken spawns zeus in a cell", "strength": 8},
		},
	})
	rec = httptest.NewRecorder()
	graphAddHandler(rec, httptest.NewRequest(http.MethodPost, "/api/graph/add", bytes.NewReader(addBody)))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/graph/add: status %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "POST /api/graph/add", rec.Body.Bytes(), graphAddRespSpec)

	var addResp struct {
		Nodes []json.RawMessage `json:"nodes"`
		Edges []json.RawMessage `json:"edges"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &addResp); err != nil {
		t.Fatal(err)
	}
	if len(addResp.Nodes) != 1 || len(addResp.Edges) != 1 {
		t.Fatalf("add results: got %d nodes / %d edges, want 1 / 1", len(addResp.Nodes), len(addResp.Edges))
	}
	assertShape(t, "POST /api/graph/add .nodes[0]", addResp.Nodes[0], graphAddNodeResultSpec)
	assertShape(t, "POST /api/graph/add .edges[0]", addResp.Edges[0], graphAddEdgeResultSpec)

	// The viewer payload, now populated: element shapes as SERVED.
	rec = httptest.NewRecorder()
	graphHandler(rec, httptest.NewRequest(http.MethodGet, "/api/graph", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/graph (populated): status %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "GET /api/graph (populated)", rec.Body.Bytes(), graphGetSpec)
	var graphResp struct {
		Nodes []json.RawMessage `json:"nodes"`
		Edges []json.RawMessage `json:"edges"`
		Stats json.RawMessage   `json:"stats"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &graphResp); err != nil {
		t.Fatal(err)
	}
	// The add named two endpoints but declared one node; the unnamed endpoint
	// becomes a bare node, which is the documented behavior — so two.
	if len(graphResp.Nodes) != 2 || len(graphResp.Edges) != 1 {
		t.Fatalf("graph: got %d nodes / %d edges, want 2 / 1", len(graphResp.Nodes), len(graphResp.Edges))
	}
	assertShape(t, "GET /api/graph .nodes[0]", graphResp.Nodes[0], webGraphNodeSpec)
	assertShape(t, "GET /api/graph .edges[0]", graphResp.Edges[0], webGraphEdgeSpec)
	assertShape(t, "GET /api/graph .stats", graphResp.Stats, webGraphStatsSpec)

	// The bulk alias join: the declared node carries its alias on THIS route
	// (the nightly ingest reads /api/graph as its one canonicalization
	// inventory, so a silent revert here re-blinds it), while the bare
	// endpoint node omits the key rather than sending an empty array.
	// Deleting the join in graphHandler must fail here, not only in prose.
	seenAliased := false
	for i, raw := range graphResp.Nodes {
		var n struct {
			Name    string   `json:"name"`
			Aliases []string `json:"aliases"`
		}
		if err := json.Unmarshal(raw, &n); err != nil {
			t.Fatal(err)
		}
		var keys map[string]json.RawMessage
		if err := json.Unmarshal(raw, &keys); err != nil {
			t.Fatal(err)
		}
		_, hasKey := keys["aliases"]
		switch n.Name {
		case "kraken":
			if len(n.Aliases) != 1 || n.Aliases[0] != "k" {
				t.Fatalf("nodes[%d] %q: aliases %v, want [k]", i, n.Name, n.Aliases)
			}
			seenAliased = true
		default:
			if hasKey {
				t.Fatalf("nodes[%d] %q: aliases key present on an alias-less node", i, n.Name)
			}
		}
	}
	if !seenAliased {
		t.Fatal("GET /api/graph: no node carried the registered alias")
	}

	// The inspector read of the node that DOES carry an alias, so the
	// "aliases" key is exercised as present rather than only as absent.
	var firstNode struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(graphResp.Nodes[0], &firstNode); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	graphNodeHandler(rec, httptest.NewRequest(http.MethodGet,
		"/api/graph/node?id="+strconv.FormatInt(firstNode.ID, 10), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/graph/node: status %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "GET /api/graph/node", rec.Body.Bytes(), graphNodeGetSpec)
	var nodeResp struct {
		Node  json.RawMessage   `json:"node"`
		Edges []json.RawMessage `json:"edges"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &nodeResp); err != nil {
		t.Fatal(err)
	}
	assertShape(t, "GET /api/graph/node .node", nodeResp.Node, webGraphNodeSpec)
	if len(nodeResp.Edges) != 1 {
		t.Fatalf("node edges: got %d, want 1", len(nodeResp.Edges))
	}
	assertShape(t, "GET /api/graph/node .edges[0]", nodeResp.Edges[0], webGraphEdgeSpec)
}

// TestGraphEpisodeDoneWireShape covers the queue's two halves as served: a
// pending episode's element shape, and the ack that retires it.
func TestGraphEpisodeDoneWireShape(t *testing.T) {
	st := useTestStore(t)
	id, added, err := st.AddEpisode(store.Episode{
		Kind: "transcript", Ref: "/x/y.jsonl#0-400", Content: "some reduced text",
		Hash: "h1", TS: 1769040000000,
	})
	if err != nil || !added {
		t.Fatalf("AddEpisode: id=%d added=%v err=%v", id, added, err)
	}

	rec := httptest.NewRecorder()
	graphPendingHandler(rec, httptest.NewRequest(http.MethodGet, "/api/graph/pending?limit=5", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/graph/pending: status %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "GET /api/graph/pending", rec.Body.Bytes(), graphPendingGetSpec)
	var pending struct {
		Episodes []json.RawMessage `json:"episodes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &pending); err != nil {
		t.Fatal(err)
	}
	if len(pending.Episodes) != 1 {
		t.Fatalf("pending: got %d episodes, want 1", len(pending.Episodes))
	}
	assertShape(t, "GET /api/graph/pending .episodes[0]", pending.Episodes[0], webGraphEpisodeSpec)

	body := mustJSON(t, map[string]any{"id": id})
	rec = httptest.NewRecorder()
	graphEpisodeDoneHandler(rec, httptest.NewRequest(http.MethodPost, "/api/graph/episode-done", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/graph/episode-done: status %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "POST /api/graph/episode-done", rec.Body.Bytes(), graphEpisodeDoneRespSpec)
}

// TestGraphSearchWireShape pins POST /api/graph/search's envelope on an EMPTY
// graph, which is the one case reachable with no embedder configured and no
// network: useTestStore points HOME at a temp dir, so core.NewEmbedder finds
// no ~/.kraken/openai.env and the handler takes its documented BM25-only
// path. `facts` must still be an array (never null) and `triples` a string.
func TestGraphSearchWireShape(t *testing.T) {
	useTestStore(t)
	t.Setenv("OPENAI_API_KEY", "") // process env wins over the file; make the absence explicit

	body := mustJSON(t, map[string]any{"query": "how does a cell spawn"})
	rec := httptest.NewRecorder()
	graphSearchHandler(rec, httptest.NewRequest(http.MethodPost, "/api/graph/search", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/graph/search: status %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "POST /api/graph/search", rec.Body.Bytes(), graphSearchRespSpec)
}

// --- kronos, the orchestrator agent: /api/orch/inbox, /api/orch/ack,
// /api/orch/context, /api/graph/recall-rearm and /api/kronos (orch.go). New
// PATHS, no existing shape touched, so none of them bumps apiContract — these
// specs are here because assertShape rejects any key ios/API.md hasn't
// documented, which is what makes an addition that skips this file fail the
// suite rather than slide through undocumented. ---

// webOrchItemSpec covers orch.go's webOrchItem, the elements of
// /api/orch/inbox's "items" array. Every key is required: the type carries no
// omitempty anywhere, because an empty slug (a row that belongs to no job) is
// a real answer kronos must be able to read unconditionally.
// delivered_at, acked_at and disposition have no entry on purpose — they are
// kraken's delivery bookkeeping, and a spec entry for one would be the first
// sign the route had started serving its own records back to the agent as if
// they were the item, the same thing webSlackMsgSpec's absences guard.
var webOrchItemSpec = map[string]fieldSpec{
	"id":      {kind: wireNumber, required: true},
	"kind":    {kind: wireString, required: true},
	"slug":    {kind: wireString, required: true},
	"summary": {kind: wireString, required: true},
	"seen_at": {kind: wireNumber, required: true},
}

// orchInboxSpec is GET /api/orch/inbox's envelope. "items" is required and is
// always an ARRAY, never null — an empty inbox is `[]`, so a client iterates
// it without a nil check.
//
// "total" is required too, and it is not len(items): the route serves at most
// the oldest orchInboxMax rows (a backlog otherwise arrives whole in kronos'
// context as one tool result), so total is the FULL unacked count and is what
// makes that truncation visible rather than silent. Additive — a new key on a
// new path, so apiContract stays 6.
var orchInboxSpec = map[string]fieldSpec{
	"items": {kind: wireArray, required: true},
	"total": {kind: wireNumber, required: true},
}

// orchAckReqSpec / orchAckRespSpec cover POST /api/orch/ack. Both request
// keys are literally required rather than merely non-omitempty: an
// empty/absent `ids` is a 400 and so is an empty `disposition`, because
// "acked, reason unknown" is indistinguishable from a bug that acks
// everything. The body is decoded with unknown fields rejected, so there is
// deliberately no third key to document.
var orchAckReqSpec = map[string]fieldSpec{
	"ids":         {kind: wireArray, required: true},
	"disposition": {kind: wireString, required: true},
}

var orchAckRespSpec = map[string]fieldSpec{
	"ok": {kind: wireBool, required: true},
}

// orchContextReqSpec / orchContextRespSpec cover POST /api/orch/context —
// kronos' statusline hook reporting how full its own context window is. All
// three request keys are required, and `used_percentage` literally so: it is
// a POINTER on the Go side precisely so an absent one is a refusal rather
// than a silent 0, which would record a full window as an empty one at
// exactly the moment the report went missing (jobload.go's rule, read one
// level up). The response is the shared bare ack.
var orchContextReqSpec = map[string]fieldSpec{
	"session_id":          {kind: wireString, required: true},
	"used_percentage":     {kind: wireNumber, required: true},
	"context_window_size": {kind: wireNumber, required: true},
}

var orchContextRespSpec = map[string]fieldSpec{
	"ok": {kind: wireBool, required: true},
}

// recallRearmReqSpec / recallRearmRespSpec cover POST
// /api/graph/recall-rearm. The response is a COUNT rather than the shared
// bare ack because the caller is a hook with no other way to learn whether
// the compaction it fired on actually re-armed anything, and 0 is a real
// answer (a session that had been told nothing yet).
var recallRearmReqSpec = map[string]fieldSpec{
	"session_id": {kind: wireString, required: true},
}

var recallRearmRespSpec = map[string]fieldSpec{
	"cleared": {kind: wireNumber, required: true},
}

// webKronosContextSpec covers orch.go's webKronosContext, /api/kronos'
// "context" object. Every key is required: the type carries no omitempty,
// because a genuine 0 pct is a real report (a freshly compacted window) and
// must still ride. Absence is expressed by the "context" key itself being
// gone — never by a zeroed object, which would claim a report OF zero.
var webKronosContextSpec = map[string]fieldSpec{
	"pct":    {kind: wireNumber, required: true},
	"window": {kind: wireNumber, required: true},
	"at":     {kind: wireNumber, required: true},
}

// webKronosSpec covers GET /api/kronos' whole body. Every key but `context`
// is required for the reason webSlackStatus' are: a zero is a real answer
// (`live` false with an empty pane is exactly what a machine with the feature
// off reports) and an absent key would be indistinguishable from it. There is
// no session_id inside `context` on purpose — the top level already names the
// session, and two ids in one payload invite a client to render a mismatch
// this route has no opinion about.
var webKronosSpec = map[string]fieldSpec{
	"enabled":    {kind: wireBool, required: true},
	"live":       {kind: wireBool, required: true},
	"session_id": {kind: wireString, required: true},
	"pane":       {kind: wireString, required: true},
	"spawned_at": {kind: wireNumber, required: true},
	"context":    {kind: wireObject, required: false},
}

func TestWebOrchItemWireShape(t *testing.T) {
	// a slugless row: it belongs to no job — every key must still be present,
	// which is the difference between "this item is not about a job" and a key
	// the client has to guess about
	data, err := json.Marshal(webOrchItem{
		ID: 1, Kind: "note", Summary: "what's blocking the csv job?", SeenAt: 1769040001000,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "webOrchItem (slugless)", data, webOrchItemSpec)

	data, err = json.Marshal(webOrchItem{
		ID: 2, Kind: "job-blocked", Slug: "csv-export-timeout",
		Summary: "zeus is waiting on a manual test", SeenAt: 1769040101000,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "webOrchItem (job-blocked)", data, webOrchItemSpec)

	reqData, err := json.Marshal(orchAckReq{IDs: []int64{42, 43}, Disposition: "steered:csv-export-timeout"})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "orchAckReq", reqData, orchAckReqSpec)

	pct := 62.5
	reqData, err = json.Marshal(orchContextReq{SessionID: "s-1", UsedPercentage: &pct, ContextWindow: 200000})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "orchContextReq", reqData, orchContextReqSpec)

	reqData, err = json.Marshal(recallRearmReq{SessionID: "s-1"})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "recallRearmReq", reqData, recallRearmReqSpec)
}

// TestOrchEndpointWireShapes drives the inbox read and the ack through their
// real handlers, so the envelopes above are pinned as SERVED rather than as
// marshaled structs — the same reason TestSlackEndpointWireShapes exists
// beside TestWebSlackMsgWireShape.
func TestOrchEndpointWireShapes(t *testing.T) {
	st := useTestStore(t)

	// empty inbox: the key is present and the array is [], never null
	rec := httptest.NewRecorder()
	orchInboxHandler(rec, httptest.NewRequest(http.MethodGet, "/api/orch/inbox", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/orch/inbox (empty): status %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "GET /api/orch/inbox (empty)", rec.Body.Bytes(), orchInboxSpec)
	if got := rec.Body.String(); !strings.Contains(got, `"items":[]`) {
		t.Errorf("GET /api/orch/inbox with no items served %s, want an empty array", got)
	}

	// a job row and a slugless row: both must serve every documented key
	if _, err := st.AddOrchItem("job-blocked", "csv-export-timeout", "zeus is waiting on a manual test", 1769040000000); err != nil {
		t.Fatalf("AddOrchItem (job-blocked): %v", err)
	}
	id, err := st.AddOrchItem("note", "", "what's blocking the csv job?", 1769040001000)
	if err != nil {
		t.Fatalf("AddOrchItem (slugless): %v", err)
	}

	rec = httptest.NewRecorder()
	orchInboxHandler(rec, httptest.NewRequest(http.MethodGet, "/api/orch/inbox", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/orch/inbox (populated): status %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "GET /api/orch/inbox (populated)", rec.Body.Bytes(), orchInboxSpec)
	var envelope struct {
		Items []json.RawMessage `json:"items"`
		Total int               `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Items) != 2 {
		t.Fatalf("items = %d entries, want 2", len(envelope.Items))
	}
	// total is the FULL unacked count, which below the cap equals the page —
	// asserted here so the key is pinned as SERVED rather than merely
	// documented; the cap itself is exercised by the edge suite.
	if envelope.Total != 2 {
		t.Errorf("total = %d, want 2 (the full unacked count)", envelope.Total)
	}
	assertShape(t, "GET /api/orch/inbox .items[0]", envelope.Items[0], webOrchItemSpec)
	assertShape(t, "GET /api/orch/inbox .items[1]", envelope.Items[1], webOrchItemSpec)

	body := mustJSON(t, map[string]any{"ids": []int64{id}, "disposition": "ignored: merge chatter"})
	rec = httptest.NewRecorder()
	orchAckHandler(rec, httptest.NewRequest(http.MethodPost, "/api/orch/ack", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/orch/ack: status %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "POST /api/orch/ack", rec.Body.Bytes(), orchAckRespSpec)
}

// TestKronosEndpointWireShape drives the context report and the status read
// end to end: the POST is the only writer of the kv row the GET serves, so
// running them against each other is what pins the "context" object as
// SERVED rather than as a struct this file marshaled itself.
//
// The peer source is a stub returning nothing — the seam peersHandler uses
// for the same reason — so the handler never shells out to tmux or ps, and
// `live` is deterministically false.
func TestKronosEndpointWireShape(t *testing.T) {
	useTestStore(t)
	noPeers := func() []*core.Peer { return nil }

	// never reported: every documented key rides, and "context" is ABSENT
	// rather than null or zeroed — the wire's only spelling of "no report"
	rec := httptest.NewRecorder()
	kronosHandler(noPeers)(rec, httptest.NewRequest(http.MethodGet, "/api/kronos", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/kronos (no report): status %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "GET /api/kronos (no report)", rec.Body.Bytes(), webKronosSpec)
	if bytes.Contains(rec.Body.Bytes(), []byte(`"context"`)) {
		t.Errorf("GET /api/kronos with no report emitted a context key: %s", rec.Body.String())
	}

	body := mustJSON(t, map[string]any{
		"session_id": "s-kronos-1", "used_percentage": 62.5, "context_window_size": 200000,
	})
	rec = httptest.NewRecorder()
	orchContextHandler(rec, httptest.NewRequest(http.MethodPost, "/api/orch/context", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/orch/context: status %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "POST /api/orch/context", rec.Body.Bytes(), orchContextRespSpec)

	rec = httptest.NewRecorder()
	kronosHandler(noPeers)(rec, httptest.NewRequest(http.MethodGet, "/api/kronos", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/kronos (reported): status %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "GET /api/kronos (reported)", rec.Body.Bytes(), webKronosSpec)
	var reported struct {
		Context json.RawMessage `json:"context"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &reported); err != nil {
		t.Fatal(err)
	}
	if reported.Context == nil {
		t.Fatalf("GET /api/kronos after a report served no context key: %s", rec.Body.String())
	}
	assertShape(t, "GET /api/kronos .context", reported.Context, webKronosContextSpec)
}

// TestRearmWireShape pins the route whose SUCCESS body needs something this
// suite cannot reach — a recorded kronos session — by driving its refusals
// through the real handler and asserting the success shape as a marshaled
// body.
func TestRearmWireShape(t *testing.T) {
	useTestStore(t)

	// blank session is a 400 (a malformed request); a session that is not the
	// recorded kronos — including the case where none is recorded at all, as
	// here — is a 403, which is the whole safety property of the route:
	// kg_recall is per-session state for every conversation in the binary.
	rec := httptest.NewRecorder()
	body := mustJSON(t, map[string]any{"session_id": "   "})
	graphRecallRearmHandler(rec, httptest.NewRequest(http.MethodPost, "/api/graph/recall-rearm", bytes.NewReader(body)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("POST /api/graph/recall-rearm (blank session): status %d, want 400 — body %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	body = mustJSON(t, map[string]any{"session_id": "some-other-session"})
	graphRecallRearmHandler(rec, httptest.NewRequest(http.MethodPost, "/api/graph/recall-rearm", bytes.NewReader(body)))
	if rec.Code != http.StatusForbidden {
		t.Errorf("POST /api/graph/recall-rearm (no kronos recorded): status %d, want 403 — body %s", rec.Code, rec.Body.String())
	}

	assertShape(t, "POST /api/graph/recall-rearm (cleared)", mustJSON(t, map[string]any{"cleared": 3}), recallRearmRespSpec)
}

// TestAPIContractUnchanged pins apiContract at the value only a BREAKING
// change may move. Additive endpoints — a whole new path, never a key
// retyped, renamed or removed on an existing one — never bump it, since old
// clients ignore what they don't know. v6 is a real break and is the reason
// the number moved: `ready` and `prereqs` left the job form's GET and POST
// shapes, and `start_error` and `prereqs` left /api/board's job objects.
// A literal here rather than a comparison against itself: a future change
// that genuinely IS breaking has to come here and change this number
// deliberately, in the same commit as expectedContract in APIClient.swift.
func TestAPIContractUnchanged(t *testing.T) {
	if apiContract != 6 {
		t.Fatalf("apiContract = %d, want 6 — bump this ONLY alongside ios/API.md and APIClient.swift's expectedContract, and only for a breaking change", apiContract)
	}
}
