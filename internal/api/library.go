package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"kraken/internal/jobs"
	"kraken/internal/store"
	"kraken/internal/util"
)

// library.go — the artifact LIBRARY: GET/POST /api/library, POST
// /api/library/remove, and GET /api/library/artifact/<id>/<name>.
//
// A library entry is one artifact carl asked to KEEP, frozen as the bytes it
// held when he asked (store/saved.go). That freeze is the whole feature and
// it is why the fourth route exists: an artifact is rewritten in place by
// the agent that owns it and disappears with the cell dir at teardown, so a
// library that served the live file would be a bookmark whose target keeps
// changing and eventually 404s. Saving takes a copy; the copy is what this
// serves, forever, addressed by its own row id.
//
// The route posture is /api/job/load's, wholesale (jobload.go): GET and POST
// on one path with a 405 JSON body for anything else, and the mirror gate
// INLINE on the POST arm rather than a mirrorGated wrapper at registration —
// a wrapper would take the GET down with it, and a mirror board must still be
// able to READ the library it cannot write. Both POST bodies are decoded with
// DisallowUnknownFields under a 1 MB cap, so a request that means something
// else is refused rather than silently reinterpreted, and an oversize one is
// a 413 distinct from a malformed one's 400.
//
// The board payload is deliberately untouched by all of this: no key on
// webJob, no webBoardCache.invalidate() on any write here. The library is its
// own view with its own poll, and putting it on the board's hot payload would
// make every card rebuild carry a list nothing on a card renders.

// webLibraryItem is one library entry on the wire. No omitempty anywhere: a
// client reads all five keys unconditionally, and every one of them is a real
// answer even at its zero value. The snapshot BYTES are deliberately not here
// — they are what `url` is for.
type webLibraryItem struct {
	ID   int64  `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
	// SavedAt is unix millis, the same clock every other `at` on this surface
	// is stamped from.
	SavedAt int64 `json:"saved_at"`
	// URL serves the FROZEN copy, never the live artifact.
	URL string `json:"url"`
}

// libraryReq is POST /api/library's body: which job, in which namespace,
// which artifact. Exactly the triple POST /api/feedback's create form takes,
// and for the same reason — home is what decides the namespace the slug
// resolves in, so an archived "foo-2" and an active job slugged "foo-2" name
// different files.
type libraryReq struct {
	Slug string `json:"slug"`
	Home string `json:"home"`
	Name string `json:"name"`
}

// libraryRemoveReq is POST /api/library/remove's body: the row id, which is
// the only thing that identifies an entry. Not (slug, name) — a saved
// artifact outlives its job, so a removal keyed on the job would stop
// working exactly when the library is most useful.
type libraryRemoveReq struct {
	ID int64 `json:"id"`
}

// libraryItemURL is the route a saved entry's frozen bytes are served from.
// Built here rather than by each caller so the served URL and the route
// registration can never drift — feedbackSnapshotURL's reasoning.
//
// The name rides along after the id purely so a browser download, a phone's
// share sheet and the board's own viewer all see a filename with the right
// extension. Lookup is by id alone; the segment is display.
//
// Escaping is done here, PER SEGMENT: a name is a path with slashes intact,
// so escaping it whole would turn its separators into %2F and give the url a
// single opaque segment, while escaping nothing would let a '#' truncate the
// url at the fragment, a '?' start a query, a space break it outright and a
// stray '%' make it undecodable. Splitting on "/" and PathEscape-ing each
// piece keeps the path SHAPE and makes every piece legal. Clients therefore
// use this string as given and never escape it again — and since lookup is
// by id, even a name this round-trips imperfectly still resolves.
func libraryItemURL(id int64, name string) string {
	segs := strings.Split(name, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return "/api/library/artifact/" + strconv.FormatInt(id, 10) + "/" + strings.Join(segs, "/")
}

func webLibraryItemFrom(m store.SavedArtifactMeta) webLibraryItem {
	return webLibraryItem{
		ID: m.ID, Slug: m.Slug, Name: m.Name, SavedAt: m.SavedAt,
		URL: libraryItemURL(m.ID, m.Name),
	}
}

// libraryHandler serves GET (every client's read of the library) and POST
// (the save). POST is mirror-gated inline with the same plain-text body every
// gated route uses (mirror_test.go asserts the exact string across all of
// them); the GET stays reachable, because a read is a read.
func libraryHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		libraryGet(w, r)
	case http.MethodPost:
		if mirrorModeOn() {
			http.Error(w, "disabled in mirror mode", http.StatusForbidden)
			return
		}
		librarySave(w, r)
	default:
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET or POST only"})
	}
}

// libraryGet serves {"items":[…]} newest first, in the order the store
// returns them (saved_at DESC, id DESC). The array is ALWAYS present and
// never null: a client renders "nothing saved yet" from an empty array, and
// a null would make every consumer write a second nil check for a state that
// is not different.
func libraryGet(w http.ResponseWriter, _ *http.Request) {
	st := jobs.ActiveStore()
	if st == nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": "no store wired"})
		return
	}
	rows, err := st.ListSavedArtifacts()
	if err != nil {
		util.LogEvent("web", "error", "library", "library list: "+err.Error())
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	items := make([]webLibraryItem, 0, len(rows))
	for _, m := range rows {
		items = append(items, webLibraryItemFrom(m))
	}
	webJSON(w, http.StatusOK, map[string]any{"items": items})
}

// librarySave freezes the artifact's CURRENT bytes into the library and
// answers with the row id, so a client that has just saved can address the
// entry without waiting for its next poll.
//
// The bytes come from resolveArtifact — the package's one artifact-resolution
// helper, shared with feedback's create/edit — so a saved copy and a feedback
// snapshot of the same artifact taken at the same moment are byte-identical,
// and both match what the artifact route would have served (B6).
//
// A re-save of an artifact already in the library REPLACES it under the same
// id (store.SaveArtifact's upsert): saving twice is one entry whose bytes
// move forward, not two entries the toggle would have to be in two states
// about.
func librarySave(w http.ResponseWriter, r *http.Request) {
	// Body cap like every other POST in this package (peers.go, feedback.go,
	// questions.go, jobcolor.go, jobload.go): the whole legitimate request is
	// three short strings, so an oversize one is refused with 413 rather than
	// read into memory, and that stays distinct from a decode error's 400.
	var req libraryReq
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	// DisallowUnknownFields for jobload's reason one step weaker: nothing here
	// decodes to a destructive default, but this route takes exactly three
	// keys and a body carrying a fourth is a client saying something this
	// server does not implement — answering 200 to it would report a save of
	// whatever the three recognized keys happened to name. The offending key
	// is NOT echoed: it comes from the body, so quoting it would let a caller
	// choose the size of the response.
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			webJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "request body too large"})
			return
		}
		// encoding/json reports an unknown field only as prose, with no typed
		// error to match on, so this substring test is the only way to tell that
		// case from ordinary malformed JSON (jobload.go measured the same
		// thing). A reworded stdlib message degrades this to the generic 400
		// below — a worse sentence, never a different outcome.
		if strings.Contains(err.Error(), "unknown field") {
			webJSON(w, http.StatusBadRequest, map[string]string{
				"error": "unrecognized field in request body — this route takes only slug, home and name",
			})
			return
		}
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	// Named one at a time rather than as "slug, home and name are required":
	// all three come from a client assembling a URL it already has, so the one
	// it dropped is the whole content of the refusal.
	for _, f := range []struct {
		name  string
		value string
	}{{"slug", req.Slug}, {"home", req.Home}, {"name", req.Name}} {
		if strings.TrimSpace(f.value) == "" {
			webJSON(w, http.StatusBadRequest, map[string]string{"error": "missing " + f.name})
			return
		}
	}
	st := jobs.ActiveStore()
	if st == nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": "no store wired"})
		return
	}
	j := resolveJobForHome(req.Slug, req.Home)
	if j == nil {
		webJSON(w, http.StatusNotFound, map[string]string{"error": "unknown job"})
		return
	}
	data, _, ok := resolveArtifact(j, req.Home, req.Name)
	if !ok {
		webJSON(w, http.StatusNotFound, map[string]string{"error": "unknown artifact"})
		return
	}
	// The stored name is leading-slash-trimmed, the same trim the resolver
	// this artifact was just read through applies on every home — ArtifactPath
	// for a live cell, ArtifactBytes for the store-backed ones (board.go:721
	// and :803): "/00-overview.md" and "00-overview.md" are one file to
	// every reader, so without it they would be two rows under the UNIQUE
	// (job_id, name) — one artifact the save toggle could be in two states
	// about at once, which is the exact thing the upsert exists to prevent.
	name := strings.TrimPrefix(req.Name, "/")
	// feedbackJobKey, not req.Slug: the RESOLVED job's own display identity
	// (an archived job's numbered archive name, an active job's slug), which
	// is the string every client already addresses it by — so a saved row
	// renders under the same name the card it came from does, and a request
	// slug that only resolved through a lookup's fuzz cannot write a display
	// name nothing matches.
	id, err := st.SaveArtifact(j.ID, feedbackJobKey(j), name, data, time.Now().UnixMilli())
	if err != nil {
		util.LogEvent("web", "error", "library-save", "library-save "+req.Slug+"/"+req.Name+": "+err.Error())
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	util.LogEvent("web", "info", "library-save", "library-save "+req.Slug+"/"+req.Name)
	webJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id})
}

// libraryRemoveHandler drops one entry. POST-only, so unlike libraryHandler
// there is no GET to keep reachable — but the mirror gate is still INLINE
// rather than a mirrorGated wrapper at registration, so a caller reaching the
// handler directly still hits the refusal (jobGoalHandler's posture).
//
// An id naming no row is a 404 rather than a silent 200: the remove
// affordance sits on a row the caller is looking at, so "already gone" means
// the view is stale and is worth saying.
func libraryRemoveHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	if mirrorModeOn() {
		http.Error(w, "disabled in mirror mode", http.StatusForbidden)
		return
	}
	var req libraryRemoveReq
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			webJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "request body too large"})
			return
		}
		if strings.Contains(err.Error(), "unknown field") {
			webJSON(w, http.StatusBadRequest, map[string]string{
				"error": "unrecognized field in request body — this route takes only id",
			})
			return
		}
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	// id 0 is what an absent key decodes to and no row ever has, so the two
	// share one refusal; a negative id is the same mistake wearing a sign.
	if req.ID <= 0 {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "missing id"})
		return
	}
	st := jobs.ActiveStore()
	if st == nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": "no store wired"})
		return
	}
	if err := st.DeleteSavedArtifact(req.ID); err != nil {
		util.LogEvent("web", "error", "library-remove", "library-remove "+strconv.FormatInt(req.ID, 10)+": "+err.Error())
		if errors.Is(err, store.ErrNotFound) {
			webJSON(w, http.StatusNotFound, map[string]string{"error": "unknown library item"})
			return
		}
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	util.LogEvent("web", "info", "library-remove", "library-remove "+strconv.FormatInt(req.ID, 10))
	webJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// libraryArtifactHandler serves GET /api/library/artifact/<id>/<name>: the
// frozen bytes of one saved entry, and never the live artifact — the live one
// may have been rewritten a dozen times since, or deleted with its cell.
//
// The <name> segment is display and extension only. Lookup is by ID, and the
// content type comes from the STORED name rather than the requested one, so a
// mistyped or stale trailing segment cannot change what a browser does with
// the response.
//
// A GET is a read, so it stays reachable under --mirror.
func libraryArtifactHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
		return
	}
	// Agents rewrite artifacts in place and a re-save re-freezes this row
	// under the same id, so a cached response could outlive the bytes it
	// carries — the same reasoning web/serve.go's artifactHandler uses.
	w.Header().Set("Cache-Control", "no-store")
	rest := strings.TrimPrefix(r.URL.Path, "/api/library/artifact/")
	idPart, _, _ := strings.Cut(rest, "/")
	id, err := strconv.ParseInt(idPart, 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	st := jobs.ActiveStore()
	if st == nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": "no store wired"})
		return
	}
	a, err := st.GetSavedArtifact(id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// artifactMimeType, the same extension → type mapping the live artifact
	// route serves under (board.go): a library entry is the same artifact
	// carl was already looking at, so it must render the same way. Set before
	// ServeContent, which only sniffs when Content-Type is absent.
	w.Header().Set("Content-Type", artifactMimeType(a.Name))
	http.ServeContent(w, r, a.Name, time.Time{}, bytes.NewReader(a.Snapshot))
}
