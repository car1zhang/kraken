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
// a selection, or register a command; the phase-1 research found exactly one
// supported channel for painting something onto a line, which is diagnostics
// published by a language server (the zed-bookmark extension is the
// precedent). So kraken becomes the language server. The extension in
// zed/kraken-mm is a WASM shim that does nothing but say "run `kraken mm lsp`".
//
// WHAT IT IS NOT. It is not a language server in any other sense: it does no
// parsing, offers no completion, and its diagnostics are severity Hint — a
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

// mmLSPItemTTL bounds how stale the mark list may be. didChange fires on
// every keystroke, and one HTTP round trip per keystroke would make kraken
// serve the editor's typing latency. Fifteen seconds is short enough that a
// mark made in the board shows up in the editor while carl is still thinking
// about it, and long enough that a burst of typing costs one fetch.
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

type lspDiagnostic struct {
	Range    lspRange `json:"range"`
	Severity int      `json:"severity"`
	Source   string   `json:"source"`
	Message  string   `json:"message"`
	Data     any      `json:"data,omitempty"`
}

// lspSeverityHint is the LSP DiagnosticSeverity for Hint. A read mark is
// information about carl, not a defect, and Zed renders Hint without the
// error/warning furniture that would make a marked line look broken.
const lspSeverityHint = 4

type lspCommand struct {
	Title     string `json:"title"`
	Command   string `json:"command"`
	Arguments []any  `json:"arguments,omitempty"`
}

type lspCodeAction struct {
	Title       string          `json:"title"`
	Kind        string          `json:"kind"`
	Diagnostics []lspDiagnostic `json:"diagnostics,omitempty"`
	Command     *lspCommand     `json:"command,omitempty"`
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

// mmLSPDiag is a published diagnostic remembered alongside the mark it came
// from, so codeAction can answer "which mark is under the cursor" without
// re-deriving it. The client's own `context.diagnostics` carries the same
// ranges, but trusting it would mean trusting a client to hand back the id it
// was given — this server keeps its own copy of what it published.
type mmLSPDiag struct {
	id   int64
	diag lspDiagnostic
}

type mmLSPServer struct {
	out io.Writer

	// wmu is belt and braces. This server runs on ONE goroutine — the read
	// loop reads, dispatches and writes — so nothing here is contended, and
	// none of the state below is guarded either. Anyone adding a background
	// refresh (a poll ticker is the obvious one) must guard `docs`, `diags`,
	// `roots`, `items`, `fetched` and `warned` too; this mutex is not a
	// promise that concurrent access is already handled.
	wmu sync.Mutex

	docs  map[string]*mmLSPDoc
	diags map[string][]mmLSPDiag
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
		diags: map[string][]mmLSPDiag{},
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
		if stop, err := s.handle(msg); stop {
			return err
		}
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
		s.didSave(msg.Params)
	case "textDocument/didClose":
		s.didClose(msg.Params)
	case "textDocument/codeAction":
		s.codeAction(msg.ID, msg.Params)
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
	s.publish(p.TextDocument.URI)
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
	s.publish(p.TextDocument.URI)
}

// didSave republishes from the BUFFER, not from disk. The two agree at this
// instant, and reading the file would introduce the one code path that can
// disagree with what carl is looking at.
func (s *mmLSPServer) didSave(raw json.RawMessage) {
	var p lspDocIDParams
	if json.Unmarshal(raw, &p) != nil || p.TextDocument.URI == "" {
		return
	}
	s.publish(p.TextDocument.URI)
}

func (s *mmLSPServer) didClose(raw json.RawMessage) {
	var p lspDocIDParams
	if json.Unmarshal(raw, &p) != nil || p.TextDocument.URI == "" {
		return
	}
	delete(s.docs, p.TextDocument.URI)
	delete(s.diags, p.TextDocument.URI)
	// Diagnostics are owned by the server until it says otherwise, so a closed
	// document is cleared explicitly — otherwise Zed keeps rendering the last
	// set it was sent.
	s.sendDiags(p.TextDocument.URI, nil)
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
// mmLSPItemTTL. force is what a drop uses.
//
// EVERY failure returns an empty list and stamps the clock. Empty means the
// caller publishes zero diagnostics, which is the honest rendering of "kraken
// serve cannot tell me what is marked" — the alternative, keeping the last
// good list, would draw marks that may have been dropped from the board
// minutes ago. Stamping the clock on failure matters as much: without it a
// dead serve would be re-dialled on every keystroke.
func (s *mmLSPServer) mmItems(force bool) []mmLSPItem {
	if !force && !s.fetched.IsZero() && time.Since(s.fetched) < mmLSPItemTTL {
		return s.items
	}
	s.fetched = time.Now()
	raw, err := qfGet("/api/mm?live=1")
	if err != nil {
		s.items = nil
		s.logOnce("kraken mm: " + jobs.ServeBaseURL() + " unreachable, read marks are not being shown (" + err.Error() + ")")
		return nil
	}
	var page struct {
		Items []mmLSPItem `json:"items"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		s.items = nil
		s.logOnce("kraken mm: could not read the mental model from " + jobs.ServeBaseURL() + " (" + err.Error() + ")")
		return nil
	}
	s.items = page.Items
	s.warned = false
	return s.items
}

// --- diagnostics ---

// publish rebuilds and publishes this document's diagnostics as a FULL
// replacement. Every path through here ends in exactly one
// publishDiagnostics, empty array included: LSP has no "remove one
// diagnostic" message, so the published set IS the state, and a path that
// returned early without publishing would leave a stale mark drawn on screen
// forever.
func (s *mmLSPServer) publish(uri string) {
	var found []mmLSPDiag
	if doc := s.docs[uri]; doc != nil && doc.ok {
		lines := strings.Split(strings.ReplaceAll(doc.text, "\r\n", "\n"), "\n")
		for _, it := range s.mmItems(false) {
			if it.Source != "code" || it.Anchor == nil {
				continue
			}
			if it.Anchor.Repo != doc.repo || it.Anchor.Path != doc.rel {
				continue
			}
			start, end, ok := locateMark(doc.text, it)
			if !ok {
				// Not in the buffer: no diagnostic. A local edit hides the
				// mark rather than killing it — MMVerifyPass owns invalidation,
				// and it asks the question against origin/main.
				continue
			}
			found = append(found, mmLSPDiag{id: it.ID, diag: lspDiagnostic{
				Range:    mmLSPRange(lines, start, end),
				Severity: lspSeverityHint,
				Source:   "kraken-mm",
				Message:  mmLSPMessage(it),
				Data:     map[string]any{"mm": it.ID},
			}})
		}
	}
	s.diags[uri] = found
	s.sendDiags(uri, found)
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

func (s *mmLSPServer) sendDiags(uri string, found []mmLSPDiag) {
	// make, never nil: `"diagnostics": null` is what a nil slice marshals to,
	// and a client is entitled to treat that as "no update" rather than as
	// "clear everything".
	out := make([]lspDiagnostic, 0, len(found))
	for _, d := range found {
		out = append(out, d.diag)
	}
	s.notify("textDocument/publishDiagnostics", map[string]any{"uri": uri, "diagnostics": out})
}

// mmLSPRange turns a 1-based inclusive line range into the 0-based LSP range
// covering those whole lines. The end character is the last line's length in
// UTF-16 code units, which is what LSP's default position encoding counts —
// on a line of ASCII it is the byte count, and on a line with an emoji in it
// the two differ by enough to underline the wrong span.
func mmLSPRange(lines []string, start, end int) lspRange {
	endChar := 0
	if end >= 1 && end <= len(lines) {
		endChar = len(utf16.Encode([]rune(lines[end-1])))
	}
	return lspRange{
		Start: lspPosition{Line: start - 1, Character: 0},
		End:   lspPosition{Line: end - 1, Character: endChar},
	}
}

// mmLSPMessage is the line Zed shows on hover. It carries the item id (so the
// code action's title and the `k mm drop <id>` verb name the same thing), the
// commit the mark is anchored to, and when it was made — the three facts that
// answer "what is this and can I trust it" without a round trip to the board.
func mmLSPMessage(it mmLSPItem) string {
	where := "dirty"
	if it.Anchor != nil && it.Anchor.AnchorKind != "" && it.Anchor.AnchorKind != "dirty" {
		where = it.Anchor.AnchorKind
		if sha := it.Anchor.AnchorSHA; sha != "" {
			if len(sha) > 7 {
				sha = sha[:7]
			}
			where += " " + sha
		}
	}
	msg := fmt.Sprintf("read mark mm %d · %s", it.ID, where)
	if it.CreatedAt > 0 {
		msg += " · marked " + time.UnixMilli(it.CreatedAt).Local().Format("2006-01-02")
	}
	return msg
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
	for _, d := range s.diags[p.TextDocument.URI] {
		// Line-granular intersection: a mark's diagnostic covers whole lines,
		// so a cursor anywhere on one of them is on the mark.
		if d.diag.Range.Start.Line > p.Range.End.Line || p.Range.Start.Line > d.diag.Range.End.Line {
			continue
		}
		title := fmt.Sprintf("drop read mark (mm %d)", d.id)
		actions = append(actions, lspCodeAction{
			Title:       title,
			Kind:        "quickfix",
			Diagnostics: []lspDiagnostic{d.diag},
			Command:     &lspCommand{Title: title, Command: mmLSPDropCommand, Arguments: []any{d.id}},
		})
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
	// The one moment the cache is provably wrong is right now, and EVERY open
	// document is republished rather than just this one: the same mark can be
	// visible in more than one buffer (the primary checkout and a cell
	// worktree of the same repo), and republishing only the requesting
	// document would leave the other still drawing a mark that no longer
	// exists.
	s.mmItems(true)
	for uri := range s.docs {
		s.publish(uri)
	}
	s.respond(id, nil)
}
