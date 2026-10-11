// Package web holds the dashboard SPA (Vue 3 + Vite) and embeds its build.
//
// Build it before the hub: npm --prefix web ci && npm --prefix web run build.
// Without a build, dist/ holds only a placeholder and the hub answers the
// dashboard with a "not built" page (the API still works).
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Dist returns the built dashboard: index.html, assets/ and public files.
func Dist() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // "dist" is a valid literal path
	}
	return sub
}
