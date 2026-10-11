package dashboard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestHandler(t *testing.T) {
	dist := fstest.MapFS{
		"index.html":           {Data: []byte("<!doctype html><title>app</title>")},
		"favicon.svg":          {Data: []byte("<svg/>")},
		"assets/index-abc.js":  {Data: []byte("console.log(1)")},
		"assets/index-abc.css": {Data: []byte("body{}")},
		".gitkeep":             {Data: nil},
	}
	h := Handler(dist)

	tests := []struct {
		method, path string
		status       int
		body         string // substring
		cache        string
		ctype        string // prefix
	}{
		{"GET", "/", 200, "<title>app</title>", "no-cache", "text/html"},
		{"GET", "/index.html", 200, "<title>app</title>", "no-cache", "text/html"},
		{"GET", "/nodes", 200, "<title>app</title>", "no-cache", "text/html"},
		{"GET", "/tools/deep/link", 200, "<title>app</title>", "no-cache", "text/html"},
		{"GET", "/assets/index-abc.js", 200, "console.log", "public, max-age=31536000, immutable", "text/javascript"},
		{"GET", "/assets/index-abc.css", 200, "body{}", "public, max-age=31536000, immutable", "text/css"},
		{"GET", "/favicon.svg", 200, "<svg/>", "no-cache", "image/svg+xml"},
		{"HEAD", "/", 200, "", "no-cache", "text/html"},
		{"GET", "/assets/missing.js", 404, "", "", ""},
		{"GET", "/robots.txt", 404, "", "", ""},
		{"GET", "/.gitkeep", 404, "", "", ""},
		{"GET", "/api/v2/nodes", 404, "", "", ""},
		{"GET", "/assets", 200, "<title>app</title>", "no-cache", "text/html"},
		// Cleaned to /etc/passwd, which isn't in dist: the app, never a file outside it.
		{"GET", "/../../etc/passwd", 200, "<title>app</title>", "no-cache", "text/html"},
		{"POST", "/", 405, "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			r := httptest.NewRequest(tt.method, "http://hub"+tt.path, nil)
			r.URL.Path = tt.path // keep dot segments the client might send
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tt.status {
				t.Fatalf("status = %d, want %d (body %q)", w.Code, tt.status, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), tt.body) {
				t.Errorf("body = %q, want it to contain %q", w.Body.String(), tt.body)
			}
			if tt.cache != "" && w.Header().Get("Cache-Control") != tt.cache {
				t.Errorf("Cache-Control = %q, want %q", w.Header().Get("Cache-Control"), tt.cache)
			}
			if tt.ctype != "" && !strings.HasPrefix(w.Header().Get("Content-Type"), tt.ctype) {
				t.Errorf("Content-Type = %q, want %q…", w.Header().Get("Content-Type"), tt.ctype)
			}
			if w.Header().Get("Content-Security-Policy") == "" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Error("security headers missing")
			}
		})
	}
}

func TestHandlerNotBuilt(t *testing.T) {
	h := Handler(fstest.MapFS{".gitkeep": {}})
	for _, p := range []string{"/", "/nodes"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", p, nil))
		if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "npm --prefix web") {
			t.Errorf("%s: status %d body %q", p, w.Code, w.Body.String())
		}
	}
}
