package api

// contract_test.go transcribes ios/API.md into executable wire-shape
// assertions: whenever a JSON shape served to or accepted from the iOS
// app/web UI changes, update this file, ios/API.md, and
// ios/Kraken/Models.swift together (see the "API contract" rule in
// CLAUDE.md).

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"kraken/core"
	"kraken/jobs"
	"kraken/util"
)

// Endpoints deliberately NOT shape-tested here, and why:
//   - /api/board's HTTP handler itself (webBoard's marshaled shape is
//     covered directly; wiring it through discovery/daemon state would drag
//     in tmux/process-table dependencies this file avoids).
//   - webAction-wrapped endpoints (/api/queue, /api/dequeue, /api/done,
//     /api/retry, /api/kill, /api/archive, /api/unarchive, /api/delete,
//     /api/question, /api/steer, /api/reactivate): all share the trivial
//     {"ok": true} success shape or {"error": string} failure shape, already
//     covered by the generic error-response convention documented in
//     ios/API.md.
//   - /api/peers/* action endpoints (spawn, send, kill, open, capture): need
//     real tmux panes/sessions to reach their success path.
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
}

var webJobSpec = map[string]fieldSpec{
	"slug":          {kind: wireString, required: true},
	"type":          {kind: wireString, required: true},
	"repo":          {kind: wireString, required: true},
	"state":         {kind: wireString, required: true},
	"err":           {kind: wireString, required: false},
	"outcome":       {kind: wireString, required: false},
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
	data, err := json.Marshal(webJob{Slug: "x", Type: "build", Repo: "monolith", State: "draft"})
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "webJob (empty optionals)", data, webJobSpec)
}

func TestWebJobWireShapePopulated(t *testing.T) {
	wj := webJob{
		Slug: "x", Type: "build", Repo: "monolith", State: "blocked", Live: true,
		Err: "e", Outcome: "o", Cell: "cell-x",
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

// --- /api/health (webpeers.go) ---

var healthSpec = map[string]fieldSpec{
	"ok":       {kind: wireBool, required: true},
	"host":     {kind: wireString, required: true},
	"contract": {kind: wireNumber, required: true},
	"stale":    {kind: wireBool, required: true},
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
	"type":     {kind: wireString, required: true},
	"repo":     {kind: wireString, required: true},
	"base":     {kind: wireString, required: true},
	"ready":    {kind: wireBool, required: true},
	"solo":     {kind: wireBool, required: true},
	"model":    {kind: wireString, required: true},
	"skip_doc": {kind: wireBool, required: true},
	"prereqs":  {kind: wireArray, required: true},
	"spec":     {kind: wireString, required: true},
	"mtime":    {kind: wireNumber, required: true},
	"editable": {kind: wireBool, required: true},
	"err":      {kind: wireString, required: true},
	"slugs":    {kind: wireArray, required: true, nullable: true},
	"repos":    {kind: wireArray, required: true, nullable: true},
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

func TestJobSaveFormWireShape(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	writeTestJob(t, "contract-saveform", "")

	body := mustJSON(t, map[string]any{"slug": "contract-saveform", "type": "build", "repo": "monolith"})
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
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	t.Setenv("KRAKEN_UPLOADS_DIR", t.TempDir())
	writeTestJob(t, "contract-upload", "")

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
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	t.Setenv("KRAKEN_UPLOADS_DIR", t.TempDir())
	writeTestJob(t, "contract-upload-sniff", "")
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
		"slug": "contract-saveform-conflict", "type": "build", "repo": "monolith", "mtime": 1,
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
	t.Setenv("KRAKEN_LOGS_DIR", t.TempDir())
	util.LogEvent("web", "info", "queue", "queue contract-test")

	rec := httptest.NewRecorder()
	webLogFiles(rec, httptest.NewRequest(http.MethodGet, "/api/logs/files", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("webLogFiles: status %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "/api/logs/files", rec.Body.Bytes(), logFilesSpec)

	files := util.ListLogFiles()
	if len(files) != 1 {
		t.Fatalf("want 1 log file, got %+v", files)
	}
	data, err := json.Marshal(files[0])
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
