// Package webui serves the embedded read-only dashboard under /ui with a strict, fixed security
// policy (PRD FR-WD-9). A binary built without the frontend serves an explanatory page instead
// (FR-WD-10), so `go install` and `go build` never depend on Node.
package webui

import (
	"bytes"
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

// dist holds the Vite build. Only its .gitkeep is committed; `make web` fills it.
//
//go:embed all:dist
var dist embed.FS

// Prefix is where the dashboard is mounted.
const Prefix = "/ui"

// contentSecurityPolicy is FR-WD-9 verbatim. It is never loosened: no inline script or style, no
// third-party origins, no framing.
const contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

// placeholder is shown when the binary was built without the dashboard. It has no scripts or styles.
const placeholder = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Arcana dashboard not included</title><meta name="viewport" content="width=device-width, initial-scale=1"></head>
<body><h1>The dashboard is not included in this build</h1>
<p>Release binaries of Arcana include the web dashboard. This binary was built without it, for example with <code>go install</code>.</p>
<p>To build it yourself, run <code>make build-all</code> (it needs Node and pnpm). The API at <code>/v1</code> is unaffected.</p>
</body></html>
`

// Handler serves the dashboard embedded in the binary.
func Handler() http.Handler {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		// The embed directive guarantees the directory; fall back to the placeholder regardless.
		return newHandler(nil)
	}
	return newHandler(sub)
}

type handler struct {
	files fs.FS // nil when the build has no dashboard
}

func newHandler(files fs.FS) http.Handler {
	if files != nil {
		if info, err := fs.Stat(files, "index.html"); err != nil || info.IsDir() {
			files = nil
		}
	}
	return &handler{files: files}
}

func setSecurityHeaders(h http.Header) {
	h.Set("Content-Security-Policy", contentSecurityPolicy)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w.Header())
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path == Prefix {
		http.Redirect(w, r, Prefix+"/", http.StatusPermanentRedirect)
		return
	}
	if !strings.HasPrefix(r.URL.Path, Prefix+"/") {
		http.NotFound(w, r)
		return
	}
	if h.files == nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(placeholder))
		}
		return
	}
	name := path.Clean(strings.TrimPrefix(r.URL.Path, Prefix+"/"))
	if name == "." || name == "" {
		name = "index.html"
	}
	// Reject anything that is not a clean relative path inside the embedded tree.
	if !fs.ValidPath(name) || strings.Contains(r.URL.Path, "..") {
		http.NotFound(w, r)
		return
	}
	data, err := fs.ReadFile(h.files, name)
	if err != nil {
		if path.Ext(name) != "" || strings.HasPrefix(name, "assets/") {
			// A missing asset must be a 404, never index.html served as script or style.
			http.NotFound(w, r)
			return
		}
		// A client-side route: the SPA decides what to render.
		name = "index.html"
		if data, err = fs.ReadFile(h.files, name); err != nil {
			http.NotFound(w, r)
			return
		}
	}
	if name == "index.html" {
		w.Header().Set("Cache-Control", "no-cache")
	} else if strings.HasPrefix(name, "assets/") {
		// Vite names these by content hash, so they never change.
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
}
