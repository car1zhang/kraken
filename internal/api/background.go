package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"kraken/internal/jobs"
	"kraken/internal/store"
)

// background.go — the board's one optional background image, a singleton
// blob in kraken.db (store/background.go, schema v26). One route, three
// methods: GET serves the image bytes, POST uploads/replaces it, DELETE
// removes it (falling back to the plain board). The board payload carries
// only the updated_at stamp (webBackground, board.go) — the SPA derives the
// image URL from it, with the stamp doubling as the cache-buster, so a
// replaced image repaints exactly when the stamp moves.
//
// Ungated on purpose, /api/upload's posture on a strictly smaller
// footprint: the write lands in whichever DB is wired (a mirror's sandbox
// included) and drives no tmux pane, no process, and no file outside
// kraken.db — nothing here is the live deployment's to protect. (register.go
// draws the contrast: /api/upload also materializes a real file on disk.)

// webBackground rides webBoard as the additive "background" key: present
// when a background is set (updated_at = the upload time, unix millis, and
// the SPA's cache-buster), absent otherwise. A pointer with omitempty for
// webJob.load's reason — a zeroed object would claim a background that
// isn't there.
type webBackground struct {
	UpdatedAt int64 `json:"updated_at"`
}

// currentBackground assembles webBoard's "background" key: nil — key absent
// — when no background is set, no store is wired, or the read fails (a sick
// store must degrade to the plain board, never to a broken payload).
func currentBackground(st *store.Store) *webBackground {
	if st == nil {
		return nil
	}
	at, err := st.BoardBackgroundUpdatedAt()
	// A zero stamp is treated as unset too, so the wire has ONE spelling of
	// "a background is set": a present key with a nonzero stamp. The store
	// never writes a zero (SetBoardBackground stamps a monotonic UnixMilli),
	// so this only guards a hand-written row — without it, the SPA's two
	// truth tests (Chrome's key-presence, stampBackground's stamp-truthiness)
	// would disagree on exactly that row.
	if err != nil || at == 0 {
		return nil
	}
	return &webBackground{UpdatedAt: at}
}

// backgroundHandler handles /api/background. POST takes multipart/form-data
// with a single "file" field, sniffs the type exactly as uploadHandler does
// (the client filename is never trusted), and enforces the same 10MB cap.
func backgroundHandler(w http.ResponseWriter, r *http.Request) {
	st := jobs.ActiveStore()
	if st == nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": "no store"})
		return
	}
	switch r.Method {
	// GET only, no HEAD: the two byte-serving siblings that carry an
	// explicit method gate — /api/upload/<slug>/<name> (web/serve.go) and
	// /api/library/artifact/ — both 405 anything but GET, and this route
	// stays in step rather than being the one exception nothing asks for.
	// (/api/artifact/ is the odd one out: no gate at all, so http.ServeFile
	// answers HEAD itself — an accident of the file-serving path rather
	// than a posture to copy.)
	case http.MethodGet:
		bg, err := st.GetBoardBackground()
		if errors.Is(err, store.ErrNotFound) {
			webJSON(w, http.StatusNotFound, map[string]string{"error": "no background set"})
			return
		}
		if err != nil {
			webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		w.Header().Set("Content-Type", bg.Mime)
		w.Header().Set("Content-Length", strconv.Itoa(len(bg.Bytes)))
		// The SPA's URL carries ?v=<updated_at>, so a long-lived cache is
		// safe: a replaced image is a NEW URL, and the stale one is never
		// re-requested.
		w.Header().Set("Cache-Control", "private, max-age=31536000")
		_, _ = w.Write(bg.Bytes)
	case http.MethodPost:
		r.Body = http.MaxBytesReader(w, r.Body, maxUploadBody)
		if err := r.ParseMultipartForm(2 << 20); err != nil {
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				webJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "file exceeds 10MB limit"})
				return
			}
			webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad multipart form"})
			return
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			webJSON(w, http.StatusBadRequest, map[string]string{"error": "missing file"})
			return
		}
		defer file.Close()
		data, err := io.ReadAll(file)
		if err != nil {
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				webJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "file exceeds 10MB limit"})
				return
			}
			webJSON(w, http.StatusBadRequest, map[string]string{"error": "read failed"})
			return
		}
		if len(data) > maxUploadBytes {
			webJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "file exceeds 10MB limit"})
			return
		}
		// Sniffed MIME, never the client filename — uploadHandler's rule. The
		// allow-list is sniffImageExt's four types; the mime string is what
		// GET serves back as Content-Type.
		mime := http.DetectContentType(data)
		if _, ok := sniffImageExt(data); !ok {
			webJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "unsupported file type — must be png, jpeg, gif, or webp"})
			return
		}
		at, err := st.SetBoardBackground(mime, data)
		if err != nil {
			webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		// The board payload carries the background stamp, so this write moves
		// it — invalidate the 2s board cache or the SPA's post-upload refetch
		// (useBoard.mutate's refetch-after-write) reads a pre-upload snapshot
		// and the click appears to do nothing for a couple of seconds.
		webBoardCache.invalidate()
		webJSON(w, http.StatusOK, map[string]any{"ok": true, "updated_at": at})
	case http.MethodDelete:
		if err := st.DeleteBoardBackground(); err != nil {
			webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		// Same invalidate as the POST arm: a stale cached payload here keeps
		// the image up — and the ▨ BG button's ✓ mark and the menu's live
		// CLEAR row with it — after a 200.
		webBoardCache.invalidate()
		webJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": fmt.Sprintf("method %s not allowed", r.Method)})
	}
}
