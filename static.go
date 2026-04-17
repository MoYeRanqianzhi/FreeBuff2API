package main

import (
	"embed"
	"io/fs"
	"net/http"
)

// adminAssets embeds the admin UI. When static/ is missing the build still
// compiles and adminStatic serves a one-line stub page.
//
//go:embed all:static
var adminAssets embed.FS

// adminStatic returns an http.Handler that serves files from the embedded
// static/ directory, rooted so /admin/ maps to static/index.html.
func adminStatic() http.Handler {
	sub, err := fs.Sub(adminAssets, "static")
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "admin UI not built into this binary", http.StatusNotFound)
		})
	}
	return http.FileServer(http.FS(sub))
}
