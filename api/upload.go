package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"kraken/jobs"
	"kraken/util"
)

// upload.go — POST /api/upload lets the web board and iOS app attach an
// image to a job (the composer references it in message text as
// "[image: <path>]"); jobs.SaveUpload (jobs/uploads.go) does the actual
// write. UploadPath is the read-side counterpart the web layer's GET route
// (web/serve.go) resolves a served file through, kept here so the saved-name
// format (img-NNN.<ext>) has one definition shared by both directions.

const (
	// maxUploadBytes is the enforced per-file cap.
	maxUploadBytes = 10 << 20
	// maxUploadBody bounds the whole request body a client may send — 1MB of
	// slack over maxUploadBytes for multipart framing and the "slug" field,
	// mirroring the http.MaxBytesReader pattern used elsewhere (peers.go).
	maxUploadBody = 11 << 20
)

// uploadHandler handles POST /api/upload: multipart/form-data with "slug"
// and "file" fields. The saved extension is always the sniffed type, never
// the client-supplied filename — a PNG renamed to .jpg still saves as a
// .png, and anything that doesn't sniff as a supported image type is
// refused outright.
func uploadHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
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
	slug := r.FormValue("slug")
	j := jobs.JobBySlug(jobs.LoadJobs(), slug)
	if j == nil {
		webJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("no job %q", slug)})
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
	ext, ok := sniffImageExt(data)
	if !ok {
		webJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "unsupported file type — must be png, jpeg, gif, or webp"})
		return
	}
	name, path, err := jobs.SaveUpload(j.Slug, ext, data)
	if err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	webJSON(w, http.StatusOK, map[string]string{
		"name": name,
		"path": path,
		"url":  "/api/upload/" + url.PathEscape(j.Slug) + "/" + name,
	})
}

// sniffImageExt maps http.DetectContentType's sniffed type (from the first
// 512 bytes) to the extension jobs.SaveUpload should use. Sniffing rather
// than trusting the client filename means a mislabeled or malicious
// extension can never smuggle a different file type onto disk.
func sniffImageExt(data []byte) (string, bool) {
	switch http.DetectContentType(data) {
	case "image/png":
		return "png", true
	case "image/jpeg":
		return "jpg", true
	case "image/gif":
		return "gif", true
	case "image/webp":
		return "webp", true
	default:
		return "", false
	}
}

// uploadNamePattern matches exactly the filenames jobs.SaveUpload can
// produce (img-NNN.<ext>, sequence starting at 001 and unbounded — %03d
// grows past three digits, ext one of the four sniffed types) — strict
// enough that no traversal or arbitrary filename can resolve through
// UploadPath.
var uploadNamePattern = regexp.MustCompile(`^img-\d{3,}\.(png|jpg|gif|webp)$`)

// UploadPath resolves a slug + upload filename to its absolute path,
// refusing anything that doesn't match the saved-name format or escapes the
// slug's uploads dir — mirrors ArtifactPath's rigor (board.go), including
// the belt-and-suspenders filepath.Rel containment check. Callers (the web
// layer's GET route) 404 on any error.
func UploadPath(slug, name string) (string, error) {
	if slug == "" || strings.Contains(slug, "/") || util.Slugify(slug) == "" {
		return "", fmt.Errorf("bad slug")
	}
	if !uploadNamePattern.MatchString(name) {
		return "", fmt.Errorf("bad upload name")
	}
	root := jobs.UploadsDir(slug)
	p := filepath.Join(root, name)
	if rel, err := filepath.Rel(root, p); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes uploads dir")
	}
	fi, err := os.Stat(p)
	if err != nil || fi.IsDir() {
		return "", fmt.Errorf("not found")
	}
	return p, nil
}

// UploadBytes is UploadPath's store-backed read-side twin: resolves
// slug+name straight from the uploads table instead of a file on disk.
// jobs.SaveUpload always writes both a DB row and a materialized file (its
// path-returning contract, kept even store-backed — see jobs/store.go's
// saveUploadToStore), so ok=false here — no store wired, or a lookup miss —
// is always safe for the caller to treat as "fall through to UploadPath".
func UploadBytes(slug, name string) (data []byte, mimeType string, ok bool) {
	st := jobs.ActiveStore()
	if st == nil {
		return nil, "", false
	}
	if slug == "" || strings.Contains(slug, "/") || util.Slugify(slug) == "" {
		return nil, "", false
	}
	if !uploadNamePattern.MatchString(name) {
		return nil, "", false
	}
	sr, err := st.GetJobBySlug(slug)
	if err != nil {
		return nil, "", false
	}
	u, err := st.GetUpload(sr.ID, name)
	if err != nil {
		return nil, "", false
	}
	return u.Bytes, u.Mime, true
}
