package web

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// bundle_test.go — the seam between the vite build and the Go binary.
//
// web/_ui/dist is GENERATED (by the Makefile's web-assets target) and
// gitignored rather than committed, and go:embed reads it from disk at
// compile time with no dependency tracking of its own. That combination has
// one silent failure mode worth a test: a build that succeeds while shipping
// a bundle whose index.html points at asset files that are not actually in
// the binary — a half-written or half-cleaned dist. The page then loads,
// renders nothing, and 404s in the console.
//
// These tests can only run at all when the embed resolved, so they are not
// guarding the empty-dist case (that one fails at compile time, loudly, which
// is the intended behaviour). They guard the PARTIAL case.

// assetRefRe pulls src="/assets/…" and href="/assets/…" out of an entry page.
var assetRefRe = regexp.MustCompile(`(?:src|href)="(/assets/[^"]+)"`)

func TestEmbeddedBundleIsComplete(t *testing.T) {
	// Both vite entries: the board SPA, and the isoscene viewer page the iOS
	// app loads for .html diagram cards. iso.html earns its place here the
	// same way index.html did — nothing else gates it: dropping the `iso`
	// entry from vite.config.ts would leave every other Go test green while
	// every diagram card silently lost its 3D view — on the phone the app's
	// own 404 fallback (ArtifactWebView.fallbackURL) would quietly serve the
	// flat card instead, so nothing would even look broken.
	for _, page := range []string{"index.html", "iso.html"} {
		idx, err := fs.ReadFile(webAssets, "_ui/dist/"+page)
		if err != nil {
			t.Fatalf("read embedded _ui/dist/%s: %v "+
				"(did `make build` run? dist/ is generated, not committed)", page, err)
		}

		refs := assetRefRe.FindAllStringSubmatch(string(idx), -1)
		if len(refs) == 0 {
			t.Fatalf("%s references no /assets/* files — vite emitted an "+
				"unprocessed page, which means the bundle was never built into it. "+
				"This exact failure shipped once during the port: an unclosed <script> tag "+
				"in the head swallowed the module script, so vite treated the page as a "+
				"static file, copied it through, and emitted no JS at all — a green build "+
				"that serves a blank page.", page)
		}

		for _, m := range refs {
			ref := strings.TrimPrefix(m[1], "/")
			if _, err := fs.Stat(webAssets, "_ui/dist/"+ref); err != nil {
				t.Errorf("%s references %s but it is not in the embed: %v", page, m[1], err)
			}
		}
	}

	idx, err := fs.ReadFile(webAssets, "_ui/dist/index.html")
	if err != nil {
		t.Fatalf("re-read embedded _ui/dist/index.html: %v", err)
	}

	// The pre-paint theme stamp is the FOUC guard: it has to be inline in the
	// head and run before the stylesheet is applied, which no React code can
	// do. Losing it in a future index.html edit would be invisible in every
	// other test.
	if !strings.Contains(string(idx), "window.krakenTheme") {
		t.Error("index.html lost the inline pre-paint theme stamp (window.krakenTheme) — " +
			"it must stay in the head, before the stylesheet, or the board flashes the " +
			"wrong theme on every load")
	}
	iTheme := strings.Index(string(idx), "window.krakenTheme")
	iCSS := strings.Index(string(idx), `rel="stylesheet"`)
	if iTheme >= 0 && iCSS >= 0 && iTheme > iCSS {
		t.Error("the stylesheet link precedes the theme stamp in index.html — " +
			"that is the FOUC the inline script exists to prevent")
	}
}

// TestStaticCacheHeaders pins the two-policy split. Hashed assets are
// immutable by construction, but index.html must NOT be: embed.FS files carry
// a zero mod time, so http.FileServer sends no Last-Modified or ETag
// validators at all, and without forced revalidation a browser's heuristic
// caching keeps serving a stale index.html across binary upgrades — which,
// since index.html is the file naming the hashed bundle, would pin the whole
// SPA to the old build.
func TestStaticCacheHeaders(t *testing.T) {
	sub, err := fs.Sub(webAssets, "_ui/dist")
	if err != nil {
		t.Fatalf("fs.Sub: %v", err)
	}
	files := http.FileServer(http.FS(sub))
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})

	// index.html — must revalidate
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("index.html Cache-Control = %q, want no-cache", got)
	}

	// a real hashed asset — must be immutable
	idx, err := fs.ReadFile(webAssets, "_ui/dist/index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	m := assetRefRe.FindStringSubmatch(string(idx))
	if m == nil {
		t.Fatal("no /assets/* reference to probe")
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, m[1], nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", m[1], rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Errorf("%s Cache-Control = %q, want the immutable policy", m[1], got)
	}
}
