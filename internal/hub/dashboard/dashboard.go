// Package dashboard serves the built Vue SPA (package web) on the admin
// listener. Files are served as built; any other path that isn't a file
// gets index.html so the SPA's router can handle it. The SPA talks only to
// /api/v1, which is mounted separately.
package dashboard

import (
	"bytes"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

// csp allows only the hub's own origin: the build has no inline scripts
// (the theme bootstrap is a file) and Vue sets styles through the CSSOM,
// which style-src doesn't restrict.
const csp = "default-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'self'; form-action 'self'"

const notBuilt = `<!doctype html><meta charset="utf-8"><title>mcp-mesh</title>
<p>The dashboard isn't built into this hub binary.</p>
<p>Build it with <code>npm --prefix web ci &amp;&amp; npm --prefix web run build</code>, then rebuild the hub.
The JSON API at <code>/api/v1</code> works without it.</p>
`

// Handler serves files from dist (the root of a Vite build).
func Handler(dist fs.FS) http.Handler {
	index, err := fs.ReadFile(dist, "index.html")
	built := err == nil
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			h.Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !built {
			h.Set("Content-Type", "text/html; charset=utf-8")
			h.Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(notBuilt))
			return
		}
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		// API paths that reach here don't exist; don't answer them with HTML.
		if name == "api" || strings.HasPrefix(name, "api/") {
			http.NotFound(w, r)
			return
		}
		// Dotfiles (dist/.gitkeep) aren't part of the app.
		if name != "" && strings.HasPrefix(path.Base(name), ".") {
			http.NotFound(w, r)
			return
		}
		if name != "" && name != "index.html" {
			if f, err := dist.Open(name); err == nil {
				st, err := f.Stat()
				f.Close()
				if err == nil && !st.IsDir() {
					if strings.HasPrefix(name, "assets/") {
						// Vite puts a content hash in every asset name.
						h.Set("Cache-Control", "public, max-age=31536000, immutable")
					} else {
						h.Set("Cache-Control", "no-cache")
					}
					http.ServeFileFS(w, r, dist, name)
					return
				}
			}
			// A missing file (a name with an extension) is a 404, not the app.
			if path.Ext(name) != "" {
				http.NotFound(w, r)
				return
			}
		}
		h.Set("Content-Type", "text/html; charset=utf-8")
		h.Set("Cache-Control", "no-cache")
		http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(index))
	})
}
