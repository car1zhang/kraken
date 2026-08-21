package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"kraken/internal/core"
	"kraken/internal/jobs"
	"kraken/internal/util"
)

// mmlsp.go — `kraken mm lsp`: a stdio language server whose entire job is to
// show carl, inside Zed, which lines of the file he is looking at he has
// already marked as read — and to let him drop one from there.
//
// WHY A LANGUAGE SERVER AT ALL. A Zed extension cannot draw a decoration, read
// a selection, or register a command; only a language server can put anything
// on a line. Marks render as LSP DOCUMENT COLORS — one whole-line color per
// covered line, which Zed's `lsp_document_colors: "background"` setting
// paints as a tinted bar (three rounds of carl's steering landed here:
// block-underline diagnostics were unreadable, indent-only rails invisible,
// first-word rails still underlines — "keep the bars, get rid of the
// underlines"). So kraken becomes the language server. The extension in
// zed/kraken-mm is a WASM shim that does nothing but say "run `kraken mm lsp`".
//
// WHAT IT IS NOT. It is not a language server in any other sense: it does no
// parsing, offers no completion, and publishes no diagnostics at all — a
// mark is a fact about carl, never a problem with the code. And it never
// invalidates anything. Text that has left the buffer simply stops being
// drawn; the daemon's MMVerifyPass owns the question of whether a mark is
// still true, against origin/main rather than against an unsaved buffer, and
// two writers for one verdict is exactly how a mark would die because carl
// was mid-edit.
//
// THE CLI FAMILY RULE HOLDS. Like every other cell-side verb this process
// never opens kraken.db: it reads `GET /api/mm?live=1` and writes
// `POST /api/mm/remove` through qfGet/qfPost, over ServeBaseURL. A dead
// kraken serve degrades to zero diagnostics and one log line — never a crash,
// because the crash is what Zed shows the user, and "my editor threw an error"
// is a far worse failure than "the marks are not showing right now".
//
// The buffer is the truth while a document is open. Every locate runs against
// what didOpen/didChange handed over, never against the file on disk: carl's
// unsaved edits are precisely when he wants to see whether he is about to
// touch something he marked.

// mmLSPItemTTL bounds how stale the mark list may be on the PULL paths
// (documentColor, codeAction). It is the fallback bound only: the poll
// ticker stamps a fresh list every mmLSPPollInterval, so in practice a pull
// answers from a list at most one tick old. The TTL exists because a pull
// can arrive with the ticker idle (no open docs a moment ago), and one HTTP
// round trip per keystroke-driven pull would make kraken serve the editor's
// typing latency.
//
// A DROP force-refreshes rather than waiting this out: the one moment the
// cached list is provably wrong is immediately after this process changed it.
const mmLSPItemTTL = 15 * time.Second

// mmLSPGitTimeout bounds the single git call this server ever makes — origin's
// url, once per worktree root. It is per-call and short because it runs
// inside a didOpen the editor is waiting on.
const mmLSPGitTimeout = 5 * time.Second

// errMMLSPUncleanExit is `exit` arriving without a preceding `shutdown`. The
// protocol asks for a non-zero process exit in exactly that case, and it is a
// silent one — the client is already gone, so a message on stderr has no
// reader and would only land in Zed's log as a false alarm.
var errMMLSPUncleanExit = errors.New("lsp: exit without shutdown")

// --- JSON-RPC wire ---

type lspMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type lspResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *lspError       `json:"error,omitempty"`
}

type lspNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

type lspError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// The four JSON-RPC / LSP error codes this server can produce. Spelled out
// rather than inlined because a wrong code reads to a client as a different
// KIND of failure — MethodNotFound is how a client learns a capability is
// absent, and answering InternalError instead makes an unimplemented method
// look like a broken one.
const (
	lspParseError           = -32700
	lspInvalidRequest       = -32600
	lspInvalidParams        = -32602
	lspMethodNotFound       = -32601
	lspInternalError        = -32603
	lspServerNotInitialized = -32002
)

// mmLSPMethodLabel bounds a method name before it is quoted back in an error.
// The name is client-supplied and unvalidated, so echoing it whole puts
// whatever the client sent — a 10 KB string, in the shape this guards against
// — into an error message and a log. Truncation is at a RUNE boundary: a
// message cut mid-rune is invalid UTF-8 in a JSON string.
func mmLSPMethodLabel(method string) string {
	const max = 64
	r := []rune(method)
	if len(r) <= max {
		return method
	}
	return string(r[:max]) + "…"
}

type lspPosition struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type lspRange struct {
	Start lspPosition `json:"start"`
	End   lspPosition `json:"end"`
}

type lspCommand struct {
	Title     string `json:"title"`
	Command   string `json:"command"`
	Arguments []any  `json:"arguments,omitempty"`
}

type lspCodeAction struct {
	Title   string      `json:"title"`
	Kind    string      `json:"kind"`
	Command *lspCommand `json:"command,omitempty"`
}

// mmLSPDropCommand is the one command this server registers. Namespaced so it
// cannot collide with another server's in a workspace running several.
const mmLSPDropCommand = "kraken.mm.drop"

// --- the mental model, as this server sees it ---

// mmLSPAnchor and mmLSPItem are the SUBSET of webMMItem (internal/api/mm.go)
// this server reads. A subset rather than the whole struct because the wire is
// additive by contract: decoding only what is used means a key added tomorrow
// cannot break the editor, and the fields here are exactly the ones a
// diagnostic is built out of.
type mmLSPAnchor struct {
	Repo       string `json:"repo"`
	Path       string `json:"path"`
	AnchorKind string `json:"anchor_kind"`
	AnchorSHA  string `json:"anchor_sha"`

	// StartLine is decoded for ONE purpose: the tiebreak when the marked text
	// occurs more than once in the buffer. It is where the mark was when it
	// was made, so it is the only signal that separates two byte-identical
	// occurrences — see locateMark.
	StartLine int `json:"start_line"`
	EndLine   int `json:"end_line"`
}

type mmLSPItem struct {
	ID        int64        `json:"id"`
	Kind      string       `json:"kind"`
	Source    string       `json:"source"`
	Content   string       `json:"content"`
	CreatedAt int64        `json:"created_at"`
	Anchor    *mmLSPAnchor `json:"anchor"`
}

// mmLSPDoc is one open buffer plus the identity resolved for it once, at
// didOpen. Resolving the repo per KEYSTROKE would put two git calls inside
// every didChange; resolving it once per document is why this struct exists
// at all.
type mmLSPDoc struct {
	text string
	repo string
	rel  string
	ok   bool // false: not in a git worktree, or an unparseable uri
}

type mmLSPServer struct {
	out io.Writer

	// wmu guards the WRITER alone: one frame's bytes reach out contiguously.
	// It is ordered strictly under mu — a frame is only ever written while mu
	// is held (or from the single-threaded pre-loop), and wmu never acquires
	// anything — so the two cannot deadlock.
	wmu sync.Mutex

	// mu guards every field below it. The server ran on ONE goroutine until
	// the poll ticker (mmLSPPoll) arrived — the read loop reads, dispatches
	// and writes, and the ticker is the second goroutine the old wmu comment
	// warned would need real locking: the read loop takes mu around each
	// message's dispatch, the ticker takes it around applying a fetch, and
	// the HTTP fetch itself happens OUTSIDE mu on the ticker (a dead serve
	// must stall the tick, never the protocol loop).
	mu sync.Mutex

	docs  map[string]*mmLSPDoc
	roots map[string]string // worktree root -> repo identity

	items   []mmLSPItem
	fetched time.Time
	warned  bool

	initialized bool
	gotShutdown bool
}

func newMMLSPServer(out io.Writer) *mmLSPServer {
	return &mmLSPServer{
		out:   out,
		docs:  map[string]*mmLSPDoc{},
		roots: map[string]string{},
	}
}

// runMMLSP is the read loop: one frame in, zero or more frames out, until the
// client sends `exit` or closes the stream.
//
// A closed stream is a CLEAN end, not an error. Editors terminate a language
// server by closing its stdin as often as they do it politely, and a server
// that reported that as a failure would fill Zed's log with false alarms on
// every window close.
func runMMLSP(in io.Reader, out io.Writer) error {
	s := newMMLSPServer(out)
	done := make(chan struct{})
	defer close(done)
	go s.poll(done)
	r := bufio.NewReader(in)
	for {
		payload, err := core.ReadLSPFrame(r)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		var msg lspMessage
		if err := json.Unmarshal(payload, &msg); err != nil {
			// There is no id to answer to, so JSON-RPC's null-id ParseError is
			// the only correct reply. Continuing rather than returning is
			// deliberate: the framing is still in sync (ReadLSPFrame consumed
			// exactly Content-Length bytes), so one unreadable message costs
			// one message.
			s.respondError(json.RawMessage("null"), lspParseError, "malformed JSON-RPC message")
			continue
		}
		s.mu.Lock()
		stop, err := s.handle(msg)
		s.mu.Unlock()
		if stop {
			return err
		}
	}
}

// mmLSPPollInterval is how often the poll ticker refetches the mark list. A
// mark carl makes on the board or with the capture keybind appears in an
// UNTOUCHED buffer within this long — before the ticker existed it appeared
// on the next edit, save or reopen, which read as "adding marks does
// nothing" whenever carl was only looking. 2s on carl's ask ("can we make it
// poll faster?"): a tick is one local GET answered from sqlite, cheap enough
// that mark-to-visible latency wins.
const mmLSPPollInterval = 2 * time.Second

// poll is the background refresh: every tick, refetch the mark list so the
// client's NEXT PULL answers from a model at most one tick old. The marks
// render as document colors, and colors are pulled by Zed — on open, edit,
// scroll (new lines shown) — with no server-initiated refresh defined for
// them (unlike inlay hints, code lenses and semantic tokens, which each have
// a workspace/*/refresh request; measured against Zed's lsp_store, which
// registers exactly those), so there is nothing here to PUSH: the tick's
// whole job is that whatever pulls next — documentColor after a scroll, a
// codeAction on cmd-. — sees marks made seconds ago, not mmLSPItemTTL ago.
// The fetch runs outside mu — a dead serve stalls the tick, never the
// protocol loop. A FAILED fetch clears the list, exactly as the event-driven
// path does — served state is the state — and logs once.
func (s *mmLSPServer) poll(done <-chan struct{}) {
	t := time.NewTicker(mmLSPPollInterval)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
		}
		s.mu.Lock()
		idle := len(s.docs) == 0 || !s.initialized || s.gotShutdown
		s.mu.Unlock()
		if idle {
			continue
		}
		_, items, err := mmLSPFetch()
		s.mu.Lock()
		s.fetched = time.Now()
		if err != nil {
			s.items = nil
			s.logOnce("kraken mm: " + jobs.ServeBaseURL() + " unreachable, read marks are not being shown (" + err.Error() + ")")
		} else {
			s.items = items
			s.warned = false
		}
		s.mu.Unlock()
	}
}

// isRequest distinguishes a request (must be answered) from a notification
// (must NOT be answered — an id-less reply is a protocol violation that some
// clients treat as fatal). An explicit JSON null id is a notification here for
// the same practical reason.
func (m lspMessage) isRequest() bool {
	return len(m.ID) > 0 && string(m.ID) != "null"
}

func (s *mmLSPServer) handle(msg lspMessage) (stop bool, err error) {
	// An id with NO method is a RESPONSE, not a request. JSON-RPC forbids
	// answering one, and this server sends the client no requests, so a
	// conforming client sends it no responses — but keying "is this a
	// request?" on the id alone made a stray response come back as
	// `unsupported method ""`, which is both a protocol violation and the
	// shape that breaks the day anything server→client is added.
	if msg.Method == "" {
		return false, nil
	}
	// Lifecycle ordering. Both arms answer only REQUESTS: a notification
	// arriving out of order is dropped, per the spec, since there is nobody
	// waiting on it and a reply to one is malformed. `exit` is exempt from
	// both, being the one message that is always in order.
	if !s.initialized && msg.Method != "initialize" && msg.Method != "exit" {
		if msg.isRequest() {
			s.respondError(msg.ID, lspServerNotInitialized, "server not initialized")
		}
		return false, nil
	}
	if s.gotShutdown && msg.Method != "exit" {
		if msg.isRequest() {
			s.respondError(msg.ID, lspInvalidRequest, "server is shutting down")
		}
		return false, nil
	}
	switch msg.Method {
	case "initialize":
		s.initialized = true
		s.respond(msg.ID, s.initializeResult())
	case "initialized", "$/setTrace", "$/cancelRequest":
		// Notifications with nothing to do. Named rather than left to the
		// default arm so the default can stay loud about genuinely unknown
		// requests.
	case "shutdown":
		s.gotShutdown = true
		s.respond(msg.ID, nil)
	case "exit":
		if s.gotShutdown {
			return true, nil
		}
		return true, errMMLSPUncleanExit
	case "textDocument/didOpen":
		s.didOpen(msg.Params)
	case "textDocument/didChange":
		s.didChange(msg.Params)
	case "textDocument/didSave":
		// Nothing to do: the buffer is already current (full sync on every
		// change), and marks render as client-pulled document colors, so
		// there is nothing to push on save either.
	case "textDocument/didClose":
		s.didClose(msg.Params)
	case "textDocument/codeAction":
		s.codeAction(msg.ID, msg.Params)
	case "textDocument/documentColor":
		s.documentColor(msg.ID, msg.Params)
	case "textDocument/colorPresentation":
		// No presentations to offer: the "colors" are read-mark highlights,
		// not values a user edits. An empty list is the protocol's way to
		// say so; not answering would hang the client's request.
		s.respond(msg.ID, []any{})
	case "workspace/executeCommand":
		s.executeCommand(msg.ID, msg.Params)
	default:
		// A request for something unimplemented is answered with
		// MethodNotFound; an unimplemented NOTIFICATION is dropped, because
		// there is nobody waiting and a reply to one is malformed.
		if msg.isRequest() {
			s.respondError(msg.ID, lspMethodNotFound, "unsupported method "+mmLSPMethodLabel(msg.Method))
		}
	}
	return false, nil
}

func (s *mmLSPServer) initializeResult() any {
	return map[string]any{
		"capabilities": map[string]any{
			// Full sync (change kind 1). Incremental sync would mean
			// reimplementing a rope against a protocol this server has no
			// other reason to model; a mark list is refetched every 15s
			// anyway, so the saving would buy nothing.
			"textDocumentSync": map[string]any{
				"openClose": true,
				"change":    1,
				"save":      map[string]any{"includeText": false},
			},
			"codeActionProvider":     true,
			"colorProvider":          true,
			"executeCommandProvider": map[string]any{"commands": []string{mmLSPDropCommand}},
		},
		"serverInfo": map[string]any{"name": "kraken-mm", "version": util.Build()},
	}
}

// --- transport ---

func (s *mmLSPServer) send(v any) {
	buf, err := json.Marshal(v)
	if err != nil {
		return
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_ = core.WriteLSPFrame(s.out, buf)
}

// respond answers a request. A nil result is marshalled as JSON null rather
// than omitted: the protocol requires a `result` key on every success, and
// `shutdown`'s result is exactly null.
func (s *mmLSPServer) respond(id json.RawMessage, result any) {
	raw, err := json.Marshal(result)
	if err != nil {
		s.respondError(id, lspInternalError, err.Error())
		return
	}
	s.send(lspResponse{JSONRPC: "2.0", ID: mmLSPID(id), Result: raw})
}

func (s *mmLSPServer) respondError(id json.RawMessage, code int, message string) {
	s.send(lspResponse{JSONRPC: "2.0", ID: mmLSPID(id), Error: &lspError{Code: code, Message: message}})
}

// mmLSPID normalizes an absent id to JSON null so the response always carries
// the required `id` key — an empty RawMessage would marshal as invalid JSON.
func mmLSPID(id json.RawMessage) json.RawMessage {
	if len(id) == 0 {
		return json.RawMessage("null")
	}
	return id
}

func (s *mmLSPServer) notify(method string, params any) {
	s.send(lspNotification{JSONRPC: "2.0", Method: method, Params: params})
}

// logOnce puts one line in the client's log per outage, not one per keystroke.
// The flag clears on the next successful fetch, so a serve that goes down
// again says so again — a single lifetime-wide "already warned" would make the
// second outage of a long editing session silent.
func (s *mmLSPServer) logOnce(msg string) {
	if s.warned {
		return
	}
	s.warned = true
	s.notify("window/logMessage", map[string]any{"type": 3, "message": msg})
}

// --- document lifecycle ---

type lspTextDocumentItem struct {
	URI  string `json:"uri"`
	Text string `json:"text"`
}

type lspDidOpenParams struct {
	TextDocument lspTextDocumentItem `json:"textDocument"`
}

type lspDidChangeParams struct {
	TextDocument struct {
		URI string `json:"uri"`
	} `json:"textDocument"`
	ContentChanges []struct {
		// Range is decoded ONLY to detect its presence. A change carrying one
		// is an INCREMENTAL delta whose `text` is the replacement for that
		// span, not the document — and the two shapes are indistinguishable
		// without this field, so an unguarded read makes a two-character edit
		// become the whole buffer and every mark in the file vanish, with
		// nothing logged anywhere.
		Range *json.RawMessage `json:"range"`
		Text  string           `json:"text"`
	} `json:"contentChanges"`
}

type lspDocIDParams struct {
	TextDocument struct {
		URI string `json:"uri"`
	} `json:"textDocument"`
}

func (s *mmLSPServer) didOpen(raw json.RawMessage) {
	var p lspDidOpenParams
	if json.Unmarshal(raw, &p) != nil || p.TextDocument.URI == "" {
		return
	}
	repo, rel, ok := s.resolveDoc(p.TextDocument.URI)
	s.docs[p.TextDocument.URI] = &mmLSPDoc{text: p.TextDocument.Text, repo: repo, rel: rel, ok: ok}
}

func (s *mmLSPServer) didChange(raw json.RawMessage) {
	var p lspDidChangeParams
	if json.Unmarshal(raw, &p) != nil || p.TextDocument.URI == "" {
		return
	}
	doc := s.docs[p.TextDocument.URI]
	if doc == nil {
		// A change for a document never opened. Resolving identity here rather
		// than dropping it keeps the server working with a client whose
		// didOpen this process missed (a restart mid-session), and the full
		// sync means the change carries the whole buffer anyway.
		repo, rel, ok := s.resolveDoc(p.TextDocument.URI)
		doc = &mmLSPDoc{repo: repo, rel: rel, ok: ok}
		s.docs[p.TextDocument.URI] = doc
	}
	// Full sync: the last change with no `range` carries the whole document.
	// A ranged change is SKIPPED rather than applied — this server advertises
	// `change: 1`, so it has no incremental machinery to apply one with, and
	// the failure of pretending otherwise is silent and total. Skipping leaves
	// the previous buffer standing, which is stale but honest; the next full
	// change or a save corrects it.
	for i := len(p.ContentChanges) - 1; i >= 0; i-- {
		if p.ContentChanges[i].Range == nil {
			doc.text = p.ContentChanges[i].Text
			break
		}
	}
}

func (s *mmLSPServer) didClose(raw json.RawMessage) {
	var p lspDocIDParams
	if json.Unmarshal(raw, &p) != nil || p.TextDocument.URI == "" {
		return
	}
	delete(s.docs, p.TextDocument.URI)
}

// --- identity ---

// resolveDoc answers "which repo and which repo-relative path is this uri".
//
// THE REPO HALF is the SAME rule the capture route uses (internal/api/mm.go's
// mmRepoName): the basename of origin's url with `.git` stripped, falling back
// to the worktree directory's own basename. It has to be the same rule or a
// mark made from `~/Developer/kraken` would not draw in a cell's `wt-kraken`
// checkout, which is most of what makes the mark survive the cell that
// produced it. It is reimplemented rather than shared because that one is
// unexported in package api and this package sits above it — two spellings of
// one rule, which is why this comment names the file the other one lives in.
//
// THE PATH HALF is relative to the GIT ROOT, found by walking up for a `.git`
// entry, and that is a computation the capture side does not do for itself:
// it is handed `$ZED_WORKTREE_ROOT`, Zed's PROJECT folder, which is the git
// root only when carl opened the repo root. So `k mm mark` re-roots through
// the same walk before posting (cmd_mm.go's mmGitRoot) — the two halves then
// agree by construction rather than by coincidence. Without that, a mark made
// with `src/` open as the project stored `path: "main.go"` while this asked
// for `src/main.go`: invisible in the file it names, and drawable on a
// different file of the same name at the root.
//
// A git WORKTREE has a `.git` FILE rather than a directory, so the walk tests
// existence, not directory-ness.
func (s *mmLSPServer) resolveDoc(uri string) (repo, rel string, ok bool) {
	p, ok := mmLSPURIPath(uri)
	if !ok {
		return "", "", false
	}
	root, ok := mmGitRoot(filepath.Dir(p))
	if !ok {
		return "", "", false
	}
	repo, cached := s.roots[root]
	if !cached {
		repo = mmLSPRepoName(root)
		s.roots[root] = repo
	}
	rel, err := filepath.Rel(root, p)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return "", "", false
	}
	return repo, filepath.ToSlash(rel), true
}

// mmLSPURIPath turns a `file://` uri into a local path, percent-decoding it.
// Any other scheme — an untitled buffer, a remote document — is not a file
// this server can locate a mark in, and says so rather than guessing.
func mmLSPURIPath(uri string) (string, bool) {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" || u.Path == "" {
		return "", false
	}
	return filepath.FromSlash(u.Path), true
}

// mmLSPGit is the swappable seam every git call on this surface goes through,
// so no cmd/kraken test has to shell out — the discipline jobs' `mmRunGit` and
// api's `mmAnchorGit` already apply to the other two copies of this rule. It
// is a THIRD var rather than a reuse of either, for api's own stated reason:
// both of those are unexported in packages this one sits above, so it is three
// packages, three vars, one behavior frozen by the contract.
var mmLSPGit = func(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return string(out), err
}

func mmLSPRepoName(root string) string {
	ctx, cancel := context.WithTimeout(context.Background(), mmLSPGitTimeout)
	defer cancel()
	out, err := mmLSPGit(ctx, root, "remote", "get-url", "origin")
	if remote := strings.TrimSpace(out); err == nil && remote != "" {
		return strings.TrimSuffix(filepath.Base(strings.TrimSuffix(remote, "/")), ".git")
	}
	// Never empty: root came back from a `.git` walk, so its basename is a
	// real directory name. The caller therefore has no empty-repo case to
	// guard, which is why it does not.
	return filepath.Base(root)
}

// --- the mark list ---

// mmItems returns the live mental model, refetched at most every
// mmLSPItemTTL. force is what a drop uses. Called with mu held, like every
// state reader here; the poll ticker's per-tick fetch stamps `fetched` too,
// so in practice this TTL path almost never dials — the ticker keeps the
// list fresher than the TTL requires and this remains as the fallback.
//
// EVERY failure returns an empty list and stamps the clock. Empty means the
// caller answers with zero marks, which is the honest rendering of "kraken
// serve cannot tell me what is marked" — the alternative, keeping the last
// good list, would serve marks that may have been dropped from the board
// minutes ago. Stamping the clock on failure matters as much: without it a
// dead serve would be re-dialled on every pull.
func (s *mmLSPServer) mmItems(force bool) []mmLSPItem {
	if !force && !s.fetched.IsZero() && time.Since(s.fetched) < mmLSPItemTTL {
		return s.items
	}
	s.fetched = time.Now()
	_, items, err := mmLSPFetch()
	if err != nil {
		s.items = nil
		s.logOnce("kraken mm: " + jobs.ServeBaseURL() + " unreachable, read marks are not being shown (" + err.Error() + ")")
		return nil
	}
	s.items = items
	s.warned = false
	return s.items
}

// mmLSPFetch is one GET + decode of the live model, touching no server
// state — the shared half of mmItems and the poll ticker, which need the
// same read but hold the lock at different times around it.
func mmLSPFetch() ([]byte, []mmLSPItem, error) {
	raw, err := qfGet("/api/mm?live=1")
	if err != nil {
		return nil, nil, err
	}
	var page struct {
		Items []mmLSPItem `json:"items"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		return nil, nil, err
	}
	return raw, page.Items, nil
}

// --- rendering: document colors ---

// mmLSPBarColor is the highlight painted behind every marked line: the
// server reports each covered line as an LSP document color, and Zed's
// `lsp_document_colors: "background"` setting (set in carl's settings.json)
// paints that range's background — the bar rendering. It is the ONE channel
// that can paint a background: diagnostics only ever draw as wavy underlines,
// and the two prior rounds' underline shapes (whole block, then first word)
// were both rejected — "get rid of the underlines". A soft blue at low
// alpha, so it reads as a wash over either theme rather than a block of
// paint.
var mmLSPBarColor = map[string]any{"red": 0.33, "green": 0.60, "blue": 0.90, "alpha": 0.16}

// documentColor serves textDocument/documentColor: one ColorInformation per
// covered line of every mark located in the buffer, ranging over the WHOLE
// line, colored mmLSPBarColor.
//
// Colors are PULLED by Zed — on open, on edit, and on scroll (new lines
// shown), with no server-initiated refresh defined for them — so a brand-new
// mark's bar appears on the next of those, and the poll ticker's job is that
// the answer given then is at most one tick old. This is the accepted trade
// of the bars rendering; the poll.go comment carries the measurement.
func (s *mmLSPServer) documentColor(id json.RawMessage, raw json.RawMessage) {
	var p lspDocIDParams
	if err := json.Unmarshal(raw, &p); err != nil {
		s.respondError(id, lspInvalidParams, "bad documentColor params")
		return
	}
	out := []map[string]any{}
	if doc := s.docs[p.TextDocument.URI]; doc != nil && doc.ok {
		lines := strings.Split(strings.ReplaceAll(doc.text, "\r\n", "\n"), "\n")
		for _, m := range s.locateDocMarks(doc) {
			for l := m.start; l <= m.end && l-1 < len(lines); l++ {
				if l < 1 {
					continue
				}
				out = append(out, map[string]any{
					"range": lspRange{
						Start: lspPosition{Line: l - 1, Character: 0},
						End:   lspPosition{Line: l - 1, Character: len(utf16.Encode([]rune(lines[l-1])))},
					},
					"color": mmLSPBarColor,
				})
			}
		}
	}
	s.respond(id, out)
}

// locatedMark is one live code mark found in one open buffer: the item and
// the 1-based inclusive line span it occupies there.
type locatedMark struct {
	it         mmLSPItem
	start, end int
}

// locateDocMarks finds every live code mark in one open document — the shared
// half of the diagnostics build and the documentColor build, so the two
// layers can never disagree about which lines are marked. A mark not located
// in the buffer contributes nothing: a local edit hides the mark rather than
// killing it — MMVerifyPass owns invalidation, and it asks the question
// against origin/main.
func (s *mmLSPServer) locateDocMarks(doc *mmLSPDoc) []locatedMark {
	var out []locatedMark
	for _, it := range s.mmItems(false) {
		if it.Source != "code" || it.Anchor == nil {
			continue
		}
		if it.Anchor.Repo != doc.repo || it.Anchor.Path != doc.rel {
			continue
		}
		start, end, ok := locateMark(doc.text, it)
		if !ok {
			continue
		}
		out = append(out, locatedMark{it: it, start: start, end: end})
	}
	return out
}

// locateMark finds one mark's lines in a buffer, resolving the AMBIGUOUS case
// before `core.LocateBlock` can decide it the wrong way.
//
// A helper called twice in one file is an ordinary thing to mark, and the two
// occurrences are byte-identical — so the only thing separating them is where
// the mark WAS when it was made, which the anchor carries as `start_line`.
// LocateBlock's exact arm takes the first hit, because its signature has no
// hint to be near; here there is one, so the pick is `core.NearestLine`, the
// same tiebreak the verify pass applies to the same question. Two halves of
// one feature disagreeing about which occurrence a mark names is how carl gets
// a hint on a line he never read and a code action that drops the OTHER mark.
//
// The span comes from the mark's own line count (EndRowFromSelection), which
// is exactly what LocateBlock's exact arm would have derived — only the START
// is being chosen differently.
//
// A single hit and the whitespace arm are LocateBlock's, untouched: with one
// occurrence there is nothing to tie-break, and the ws arm's window is already
// variable-length, so "nearest" has no meaning to impose on it.
func locateMark(buffer string, it mmLSPItem) (startLine, endLine int, ok bool) {
	if it.Anchor != nil && it.Anchor.StartLine > 0 {
		if hits := core.LocateText(buffer, it.Content); len(hits) > 1 {
			start := core.NearestLine(hits, it.Anchor.StartLine)
			return start, core.EndRowFromSelection(start, it.Content), true
		}
	}
	return core.LocateBlock(buffer, it.Content)
}

// --- code action + command ---

type lspCodeActionParams struct {
	TextDocument struct {
		URI string `json:"uri"`
	} `json:"textDocument"`
	Range lspRange `json:"range"`
}

func (s *mmLSPServer) codeAction(id json.RawMessage, raw json.RawMessage) {
	var p lspCodeActionParams
	if err := json.Unmarshal(raw, &p); err != nil {
		s.respondError(id, lspInvalidParams, "bad codeAction params")
		return
	}
	actions := make([]lspCodeAction, 0, 1)
	if doc := s.docs[p.TextDocument.URI]; doc != nil && doc.ok {
		for _, m := range s.locateDocMarks(doc) {
			// Line-granular intersection against the mark's located span —
			// there are no published diagnostics to key on any more, so the
			// action is derived from the same locate the color build uses,
			// which is what keeps "which mark is under the cursor" one
			// answer. A cursor anywhere on a covered line is on the mark;
			// columns are deliberately not consulted.
			if m.start-1 > p.Range.End.Line || p.Range.Start.Line > m.end-1 {
				continue
			}
			title := fmt.Sprintf("drop read mark (mm %d)", m.it.ID)
			actions = append(actions, lspCodeAction{
				Title:   title,
				Kind:    "quickfix",
				Command: &lspCommand{Title: title, Command: mmLSPDropCommand, Arguments: []any{m.it.ID}},
			})
		}
	}
	s.respond(id, actions)
}

type lspExecuteCommandParams struct {
	Command   string            `json:"command"`
	Arguments []json.RawMessage `json:"arguments"`
}

func (s *mmLSPServer) executeCommand(id json.RawMessage, raw json.RawMessage) {
	var p lspExecuteCommandParams
	if err := json.Unmarshal(raw, &p); err != nil {
		s.respondError(id, lspInvalidParams, "bad executeCommand params")
		return
	}
	if p.Command != mmLSPDropCommand {
		s.respondError(id, lspInvalidParams, "unknown command "+mmLSPMethodLabel(p.Command))
		return
	}
	if len(p.Arguments) == 0 {
		s.respondError(id, lspInvalidParams, mmLSPDropCommand+" needs a mental-model item id")
		return
	}
	var mmID int64
	if err := json.Unmarshal(p.Arguments[0], &mmID); err != nil || mmID <= 0 {
		s.respondError(id, lspInvalidParams, mmLSPDropCommand+" needs a positive mental-model item id")
		return
	}
	if _, err := qfPost("/api/mm/remove", map[string]any{"id": mmID}); err != nil {
		// The client asked for a write and did not get one; saying so is the
		// whole point, since a silent failure leaves the mark drawn and carl
		// believing he dropped it. The notification is what Zed actually shows
		// — an error response alone lands only in the log.
		s.notify("window/showMessage", map[string]any{"type": 1, "message": fmt.Sprintf("kraken mm: could not drop mm %d — %v", mmID, err)})
		s.respondError(id, lspInternalError, err.Error())
		return
	}
	// The one moment the cache is provably wrong is right now, so it is
	// force-refetched: the dropped mark's bar disappears on the client's next
	// color pull (edit, scroll, reopen — there is no push channel for
	// colors), and that pull must not be answered from a list still carrying
	// the mark carl just watched himself drop.
	s.mmItems(true)
	s.respond(id, nil)
}
