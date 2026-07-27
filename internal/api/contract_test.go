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
	"testing"

	"kraken/internal/core"
	"kraken/internal/jobs"
	"kraken/internal/store"
	"kraken/internal/util"
)

// Endpoints deliberately NOT shape-tested here, and why:
//   - /api/board's HTTP handler itself (webBoard's marshaled shape is
//     covered directly; wiring it through discovery/daemon state would drag
//     in tmux/process-table dependencies this file avoids).
//   - webAction-wrapped endpoints (/api/queue, /api/dequeue, /api/done,
//     /api/retry, /api/kill, /api/archive, /api/unarchive, /api/delete,
//     /api/question, /api/steer, /api/reactivate, /api/start, /api/stop):
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
	"slug":          {kind: wireString, required: true},
	"repo":          {kind: wireString, required: true},
	"state":         {kind: wireString, required: true},
	"err":           {kind: wireString, required: false},
	"outcome":       {kind: wireString, required: false},
	"color":         {kind: wireString, required: false},
	"cell":          {kind: wireString, required: false},
	"live":          {kind: wireBool, required: true},
	"prereqs":       {kind: wireArray, required: false},
	"artifacts":     {kind: wireArray, required: false},
	"detail":        {kind: wireString, required: false},
	"question":      {kind: wireObject, required: false},
	"zeus_status":   {kind: wireString, required: false},
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
		Prereqs:   []webPrereq{{Job: "y", Gated: true, Done: false}},
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
	}
	data, err := json.Marshal(wj)
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "webJob (populated)", data, webJobSpec)
}

// --- webJob's array-element types, marshaled and checked directly: webJob's
// own spec above only asserts "prereqs"/"artifacts"/"roster" are arrays, not
// what their elements look like (web.go, job.go) ---

var webPrereqSpec = map[string]fieldSpec{
	"job":   {kind: wireString, required: true},
	"gated": {kind: wireBool, required: true},
	"done":  {kind: wireBool, required: true},
}

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

// jobPRSpec covers job.go's jobs.JobPR, the elements of webJob's "prs" array —
// the daemon-tracked record of every PR shipped from the cell branch.
var jobPRSpec = map[string]fieldSpec{
	"url":   {kind: wireString, required: true},
	"state": {kind: wireString, required: true},
}

// prereqSpec covers job.go's jobs.Prereq, the form-editor's element type (distinct
// from webPrereq above: no computed "done", and "gated" is omitempty).
var prereqSpec = map[string]fieldSpec{
	"job":   {kind: wireString, required: true},
	"gated": {kind: wireBool, required: false},
}

func TestWebPrereqWireShape(t *testing.T) {
	data, err := json.Marshal(webPrereq{Job: "y", Gated: true, Done: false})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "webPrereq", data, webPrereqSpec)
}

func TestWebArtifactWireShape(t *testing.T) {
	data, err := json.Marshal(webArtifact{Name: "a", URL: "/api/artifact/x/cell/a"})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "webArtifact", data, webArtifactSpec)
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
	"created_at": {kind: wireNumber, required: true},
	"closed_at":  {kind: wireNumber, required: false},
}

func TestWebThreadWireShape(t *testing.T) {
	// a closed thread: no title, no pane (CloseThread clears it)
	data, err := json.Marshal(webThread{TID: "t1", State: "closed", CreatedAt: 1234567890})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "webThread (empty optionals)", data, webThreadSpec)

	data, err = json.Marshal(webThread{
		TID: "t2", Title: "why the retry loop", State: "live", Pane: "%42",
		CreatedAt: 1234567890, ClosedAt: 1234567899,
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

func TestPrereqWireShape(t *testing.T) {
	data, err := json.Marshal(jobs.Prereq{Job: "y", Gated: true})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "jobs.Prereq (gated)", data, prereqSpec)

	data, err = json.Marshal(jobs.Prereq{Job: "y"})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "jobs.Prereq (empty optionals)", data, prereqSpec)
}

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
	"slug":     {kind: wireString, required: true},
	"repo":     {kind: wireString, required: true},
	"base":     {kind: wireString, required: true},
	"ready":    {kind: wireBool, required: true},
	"solo":     {kind: wireBool, required: true},
	"model":    {kind: wireString, required: true},
	"color":    {kind: wireString, required: true},
	"prereqs":  {kind: wireArray, required: true},
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
	if _, err := st.AddWatchItem(store.WatchItem{Observation: "o", Class: "waste"}); err != nil {
		t.Fatal(err)
	}

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
}

// watchItemRespSpec is the agent write's 200: {"ok":true,"id":N}. The id is
// the only reason this isn't the standard {"ok":true} — a caller that just
// created a row has no other way to learn its id.
var watchItemRespSpec = map[string]fieldSpec{
	"ok": {kind: wireBool, required: true},
	"id": {kind: wireNumber, required: true},
}

func TestWatchlistWriteWireShapes(t *testing.T) {
	verdictReq, err := json.Marshal(watchVerdictReq{ID: 1, Verdict: "fix", Comment: "c"})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "watchVerdictReq", verdictReq, watchVerdictReqSpec)

	itemReq, err := json.Marshal(watchItemReq{Op: "update", Item: &watchItemWire{ID: 1, Observation: "o", Class: "waste"}, ID: 0})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "watchItemReq", itemReq, watchItemReqSpec)

	itemWire, err := json.Marshal(watchItemWire{
		ID: 1, Observation: "o", Class: "waste", State: "watching", Sightings: 2,
		FirstSeen: 1, LastSeen: 2, Evidence: `{"n":1}`, SuggestedFix: "fix", PRURL: "u",
	})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "watchItemWire", itemWire, watchItemWireSpec)

	useTestStore(t)
	rec := httptest.NewRecorder()
	body := mustJSON(t, map[string]any{"op": "add", "item": map[string]any{"observation": "o", "class": "waste"}})
	watchItemHandler(rec, httptest.NewRequest(http.MethodPost, "/api/watchlist/item", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "POST /api/watchlist/item", rec.Body.Bytes(), watchItemRespSpec)
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

// TestAPIContractUnchanged pins apiContract at the value the dream loop's
// routes did NOT move it to. Every endpoint that shipped with them is
// additive — a whole new path, never a key retyped, renamed or removed on an
// existing one — and additive shapes never bump the contract (old clients
// ignore what they don't know). A literal here rather than a comparison
// against itself: a future change that genuinely IS breaking has to come here
// and change this number deliberately, in the same commit as
// expectedContract in APIClient.swift.
func TestAPIContractUnchanged(t *testing.T) {
	if apiContract != 5 {
		t.Fatalf("apiContract = %d, want 5 — bump this ONLY alongside ios/API.md and APIClient.swift's expectedContract, and only for a breaking change", apiContract)
	}
}
