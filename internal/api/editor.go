package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"kraken/internal/core"
	"kraken/internal/jobs"
	"kraken/internal/util"
)

// editor.go — POST /api/editor/open, the server half of a clickable code ref
// in an artifact: the browser hands over {slug, path, line, col} and this
// resolves it against that job's checkout and opens Zed on it.
//
// Two things make this endpoint unlike its neighbours. It takes a PATH from
// the client, so it carries ArtifactPath's traversal guard (board.go) even
// though the value is supposed to come from a diagram we generated — any
// local process can hit this port. And it is the one route that refuses a
// request from the phone: everything else here is more useful the further
// away the caller is, while an editor launch is only ever meaningful on the
// machine the editor is running on (Q4).

// editorOpenReq is the request body. archived selects the archive namespace
// for slug resolution rather than being a hint — an archived job and a live
// one can share a slug, and the two have different worktrees. Its producers
// are both web overlays: ArtifactOverlay sends `parts.home === "archive"`, so
// a ref clicked in an artifact served out of the archive home resolves in the
// namespace that artifact came from, and SteerOverlay sends `job?.archived`
// for a steer opened on an archived job.
type editorOpenReq struct {
	Slug     string `json:"slug"`
	Archived bool   `json:"archived"`
	Path     string `json:"path"`
	Line     int    `json:"line"`
	Col      int    `json:"col"`
}

// interfaceAddrs lists this host's own addresses; a seam so a test can decide
// what "this Mac" means without depending on the machine it runs on.
var interfaceAddrs = net.InterfaceAddrs

// lsTreeFiles lists the tracked files under root whose basename is base. A
// seam for the same reason openInEditorFunc is one: the test suite never
// shells out.
var lsTreeFiles = gitLsFilesByBase

// openInEditorFunc performs the actual launch. Swapped in tests — nothing in
// the suite ever execs, and nothing in it ever touches /Applications.
var openInEditorFunc = core.OpenInZed

// isLocalOrigin reports whether remoteAddr belongs to the machine serving the
// request: loopback, or one of the host's own interface addresses (which is
// what lets the Mac's browser reach the board over its tailnet name and still
// count as local). Everything else — a phone, another Mac on the tailnet — is
// refused, because a tap over there would open a window over here.
//
// A remoteAddr that doesn't parse is NOT local: this gate fails closed, since
// the thing it protects is a GUI launch on carl's own desktop.
func isLocalOrigin(remoteAddr string) bool {
	ip := requestIP(remoteAddr)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	addrs, err := interfaceAddrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		var self net.IP
		switch v := a.(type) {
		case *net.IPNet:
			self = v.IP
		case *net.IPAddr:
			self = v.IP
		}
		if self != nil && self.Equal(ip) {
			return true
		}
	}
	return false
}

// requestIP extracts the IP from an http.Request's RemoteAddr. Real requests
// carry host:port ("127.0.0.1:52341", "[::1]:52341"); a bare address is
// accepted too, since that is the shape a synthesized request has. An IPv6
// zone ("fe80::1%en0") is trimmed — net.ParseIP rejects it, and the zone is
// not part of the identity being compared.
func requestIP(remoteAddr string) net.IP {
	host := strings.TrimSpace(remoteAddr)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if i := strings.Index(host, "%"); i >= 0 {
		host = host[:i]
	}
	return net.ParseIP(host)
}

// validEditorRel applies ArtifactPath's guard (board.go) to a client-supplied
// repo-relative path: no absolute path, no empty/./.. segment, no dot-prefixed
// segment. Dotfiles are refused rather than merely traversal, exactly as the
// artifact reader refuses them — the value comes off a rendered page, and the
// set of things worth opening from one contains no .env.
func validEditorRel(rel string) error {
	if rel == "" {
		return errors.New("path is required")
	}
	if strings.HasPrefix(rel, "/") {
		return fmt.Errorf("path must be repo-relative: %s", rel)
	}
	for _, seg := range strings.Split(rel, "/") {
		if seg == "" || seg == "." || seg == ".." || strings.HasPrefix(seg, ".") {
			return fmt.Errorf("path must be repo-relative: %s", rel)
		}
	}
	return nil
}

// gitLsFilesByBase is lsTreeFiles' real implementation: every tracked file
// under root whose basename matches. The filter is done here rather than with
// a pathspec so base is never handed to git as a pattern: nothing earlier on
// the server rejects glob characters — validEditorRel refuses only empty,
// absolute and ./../dotted segments — so a base of "*.go" arrives here intact.
// The exact `==` below is what refuses it: it matches no real basename, the
// request 409s with a zero-match message, and git is never asked to expand
// anything. (The browser's parser and the SVG linter do drop glob characters,
// but neither of them is the only thing that can reach this port.)
func gitLsFilesByBase(root, base string) ([]string, error) {
	out, err := exec.Command("git", "-C", root, "ls-files", "-z").Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			if msg := strings.TrimSpace(string(ee.Stderr)); msg != "" {
				return nil, fmt.Errorf("git ls-files in %s: %v: %s", root, err, msg)
			}
		}
		return nil, fmt.Errorf("git ls-files in %s: %v", root, err)
	}
	var hits []string
	for _, f := range strings.Split(string(out), "\x00") {
		if f != "" && filepath.Base(f) == base {
			hits = append(hits, f)
		}
	}
	return hits, nil
}

// editorJobFor resolves the request's slug in the namespace it asked for.
func editorJobFor(req editorOpenReq) *jobs.Job {
	if req.Archived {
		return jobs.ArchivedJobByName(jobs.LoadArchivedJobs(), req.Slug)
	}
	return jobs.JobBySlug(jobs.LoadJobs(), req.Slug)
}

// editorOpenHandler resolves a code ref to a file and opens it in Zed.
//
// Everything that goes wrong once resolution has started answers 409 with a
// message, the shape respondOpened (peers.go) established for this family: the
// click came from a page that cannot know whether a cell still has a worktree
// or whether a basename is unique, so those refusals have to be readable in a
// toast. The gates ahead of resolution keep their own codes — 405/415 from
// requireJSONPost, 403 from the origin gate, 400 for a body that isn't JSON.
func editorOpenHandler(w http.ResponseWriter, r *http.Request) {
	if !requireJSONPost(w, r) {
		return
	}
	// before any resolution work: a request from elsewhere never gets to
	// learn what does or doesn't exist on this machine, either
	if !isLocalOrigin(r.RemoteAddr) {
		// 403 rather than the 409 every refusal routed through editorFail
		// carries: this one is about WHO asked, not about what they asked
		// for, and no retry with a different body changes it
		msg := fmt.Sprintf("editor opens are restricted to this Mac — %s is not a local address", r.RemoteAddr)
		util.LogEvent("web", "error", "editor", "open: "+msg)
		webJSON(w, http.StatusForbidden, map[string]string{"error": msg})
		return
	}
	var req editorOpenReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	if err := validEditorRel(req.Path); err != nil {
		editorFail(w, err.Error())
		return
	}
	if req.Line < 0 || req.Col < 0 {
		editorFail(w, fmt.Sprintf("line/col must not be negative (got %d:%d)", req.Line, req.Col))
		return
	}
	j := editorJobFor(req)
	if j == nil {
		editorFail(w, fmt.Sprintf("no job %q", req.Slug))
		return
	}
	repo, err := jobs.ResolveRepo(j.Repo)
	if err != nil {
		editorFail(w, err.Error())
		return
	}
	// the cell's worktree is the right root while it exists — the ref was
	// written against the branch, not against main. Once the cell is torn
	// down the primary checkout is the only copy left, so open from there
	// and say so: same file, possibly a different revision (Q1).
	root, note := jobs.ExistingWorktree(j, repo), ""
	if root == "" {
		root = repo
		note = fmt.Sprintf("worktree gone — opened from the primary checkout %s", repo)
	}
	rel := req.Path
	if !strings.Contains(rel, "/") {
		// a basename-only ref — the `cell.go:553` shape a diagram uses when it
		// names a file without its directory — is resolvable only if the repo
		// has exactly one file by that name; anything else would be a guess
		matches, err := lsTreeFiles(root, rel)
		if err != nil {
			editorFail(w, err.Error())
			return
		}
		switch len(matches) {
		case 0:
			editorFail(w, fmt.Sprintf("no tracked file named %s under %s", rel, root))
			return
		case 1:
			rel = matches[0]
		default:
			editorFail(w, fmt.Sprintf("%d tracked files are named %s — cite the full repo-relative path", len(matches), rel))
			return
		}
	} else if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
		editorFail(w, fmt.Sprintf("%s does not exist under %s", rel, root))
		return
	}
	abs := filepath.Join(root, rel)
	// belt-and-braces, the way ArtifactPath ends: validEditorRel already
	// makes an escape unreachable, and this is what keeps that true if it
	// ever stops being
	if rp, err := filepath.Rel(root, abs); err != nil || rp == ".." || strings.HasPrefix(rp, ".."+string(filepath.Separator)) {
		editorFail(w, fmt.Sprintf("path escapes %s: %s", root, req.Path))
		return
	}
	// root rides along so the editor opens the file inside a window rooted at
	// the resolved checkout — reusing that window when one exists (core/zed.go)
	if err := openInEditorFunc(root, abs, req.Line, req.Col); err != nil {
		editorFail(w, err.Error())
		return
	}
	resp := map[string]any{"ok": true}
	if note != "" {
		resp["note"] = note
	}
	util.LogEvent("web", "info", "editor", fmt.Sprintf("open %s: %s", req.Slug, abs))
	webJSON(w, http.StatusOK, resp)
}

// editorFail is respondOpened's error altitude, alone: every refusal routed
// through it is a 409 carrying a sentence for the toast, and every one of them
// is worth a line in the event log — a click that silently did nothing is the
// failure mode this endpoint has to make impossible to miss. The three
// refusals that do NOT come through here are the ones that never reach the
// resolution work: requireJSONPost's 405/415, the origin gate's 403, and a
// 400 for a body that isn't JSON.
func editorFail(w http.ResponseWriter, msg string) {
	util.LogEvent("web", "error", "editor", "open: "+msg)
	webJSON(w, http.StatusConflict, map[string]string{"error": msg})
}
