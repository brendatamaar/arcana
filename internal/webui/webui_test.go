package webui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

const wantCSP = "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

func built() http.Handler {
	return newHandler(fstest.MapFS{
		"index.html":            {Data: []byte(`<!doctype html><script type="module" src="/ui/assets/app-abc123.js"></script>`)},
		"assets/app-abc123.js":  {Data: []byte("console.log('app')")},
		"assets/app-abc123.css": {Data: []byte("body{}")},
		"favicon.svg":           {Data: []byte("<svg xmlns='http://www.w3.org/2000/svg'/>")},
	})
}

func get(h http.Handler, method, target string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(method, target, nil))
	return w
}

func TestHandler_EveryResponseCarriesTheFixedSecurityHeaders(t *testing.T) {
	for _, h := range []http.Handler{built(), newHandler(nil)} {
		for _, target := range []string{"/ui/", "/ui/sandboxes/sbx_a", "/ui/assets/app-abc123.js", "/ui/assets/missing.js", "/ui", "/other"} {
			for _, method := range []string{"GET", "HEAD", "POST", "DELETE"} {
				w := get(h, method, target)
				if got := w.Header().Get("Content-Security-Policy"); got != wantCSP {
					t.Errorf("%s %s: CSP %q", method, target, got)
				}
				if w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Referrer-Policy") != "no-referrer" {
					t.Errorf("%s %s: missing nosniff/referrer policy", method, target)
				}
			}
		}
	}
	if strings.Contains(wantCSP, "unsafe") || strings.Contains(contentSecurityPolicy, "unsafe") {
		t.Fatal("the policy must never allow unsafe sources")
	}
	if contentSecurityPolicy != wantCSP {
		t.Fatal("policy drifted from PRD FR-WD-9")
	}
}

func TestHandler_ServesIndexAssetsAndSPARoutesWithCorrectCaching(t *testing.T) {
	h := built()
	index := get(h, "GET", "/ui/")
	if index.Code != 200 || !strings.Contains(index.Body.String(), "app-abc123.js") || index.Header().Get("Cache-Control") != "no-cache" || !strings.HasPrefix(index.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("index: %d %v", index.Code, index.Header())
	}
	for _, route := range []string{"/ui/sandboxes", "/ui/sandboxes/sbx_a", "/ui/login", "/ui/deep/client/route"} {
		w := get(h, "GET", route)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "app-abc123.js") || w.Header().Get("Cache-Control") != "no-cache" {
			t.Errorf("%s: %d %v", route, w.Code, w.Header())
		}
	}
	js := get(h, "GET", "/ui/assets/app-abc123.js")
	if js.Code != 200 || js.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" || !strings.Contains(js.Header().Get("Content-Type"), "javascript") || js.Body.String() != "console.log('app')" {
		t.Fatalf("asset: %d %v", js.Code, js.Header())
	}
	if css := get(h, "GET", "/ui/assets/app-abc123.css"); !strings.HasPrefix(css.Header().Get("Content-Type"), "text/css") {
		t.Fatal(css.Header())
	}
	if icon := get(h, "GET", "/ui/favicon.svg"); icon.Code != 200 || icon.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("non-hashed file must not be immutable: %v", icon.Header())
	}
	head := get(h, "HEAD", "/ui/assets/app-abc123.js")
	if head.Code != 200 || head.Body.Len() != 0 {
		t.Fatalf("HEAD: %d %d", head.Code, head.Body.Len())
	}
	// An asset that does not exist is a 404, never index.html served as script.
	for _, missing := range []string{"/ui/assets/missing.js", "/ui/assets/missing", "/ui/other.js", "/ui/x.css"} {
		if w := get(h, "GET", missing); w.Code != 404 || strings.Contains(w.Body.String(), "app-abc123") {
			t.Errorf("%s: %d", missing, w.Code)
		}
	}
}

func TestHandler_RedirectsMethodsAndPathTraversal(t *testing.T) {
	h := built()
	if w := get(h, "GET", "/ui"); w.Code != http.StatusPermanentRedirect || w.Header().Get("Location") != "/ui/" {
		t.Fatalf("%d %v", w.Code, w.Header())
	}
	for _, method := range []string{"POST", "PUT", "DELETE", "PATCH"} {
		if w := get(h, method, "/ui/"); w.Code != 405 || w.Header().Get("Allow") != "GET, HEAD" {
			t.Errorf("%s: %d", method, w.Code)
		}
	}
	for _, target := range []string{"/ui/../index.html", "/ui/%2e%2e/etc/passwd", "/ui/assets/../../secret", "/ui/..%2f..%2fetc/passwd", "/ui//etc/passwd", "/uix/", "/"} {
		r := httptest.NewRequest("GET", "http://example.test"+target, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		body, _ := io.ReadAll(w.Body)
		if w.Code == 200 && !strings.Contains(string(body), "app-abc123") {
			t.Errorf("%s served unexpected content: %d %q", target, w.Code, body)
		}
		if strings.Contains(string(body), "passwd") || strings.Contains(string(body), "secret") {
			t.Errorf("%s leaked: %q", target, body)
		}
	}
}

// The embedded tree holds only dist/.gitkeep in a fresh checkout and the real build after `make web`.
func TestEmbeddedHandler_ServesWhicheverBuildIsPresent(t *testing.T) {
	w := get(Handler(), "GET", "/ui/")
	built := false
	if _, err := dist.Open("dist/index.html"); err == nil {
		built = true
	}
	body := w.Body.String()
	switch {
	case w.Code != 200:
		t.Fatalf("%d", w.Code)
	case built && strings.Contains(body, "not included in this build"):
		t.Fatal("a dashboard build is embedded but the placeholder was served")
	case !built && !strings.Contains(body, "not included in this build"):
		t.Fatalf("no dashboard build, expected the placeholder: %q", body)
	}
	if w.Header().Get("Content-Security-Policy") != wantCSP {
		t.Fatal("embedded handler lost the security headers")
	}
}

func TestHandler_WithoutADashboardBuildShowsAnExplanationNotAnError(t *testing.T) {
	for name, h := range map[string]http.Handler{
		"nil tree":        newHandler(nil),
		"empty tree":      newHandler(fstest.MapFS{}),
		"only a .gitkeep": newHandler(fstest.MapFS{".gitkeep": {}}),
	} {
		for _, target := range []string{"/ui/", "/ui/sandboxes/sbx_a"} {
			w := get(h, "GET", target)
			body := w.Body.String()
			if w.Code != 200 || !strings.Contains(body, "not included in this build") || !strings.Contains(body, "make build-all") {
				t.Errorf("%s %s: %d %q", name, target, w.Code, body)
			}
			if strings.Contains(body, "<script") || strings.Contains(body, "<style") || strings.Contains(body, "style=") {
				t.Errorf("%s: the placeholder must carry no script or style: %s", name, body)
			}
			if w.Header().Get("Cache-Control") != "no-cache" {
				t.Errorf("%s: placeholder cached", name)
			}
		}
	}
}
