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

// loginHandler serves the public crowdfunding login page at /login.html.
// Only this single path is served — the rest of static/ stays behind /admin/.
// We deliberately do NOT expose a FileServer here because static/index.html
// belongs to the admin surface and its existence should not be discoverable
// without the admin token.
func loginHandler() http.Handler {
	data, err := adminAssets.ReadFile("static/login.html")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path != "/login.html" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(data)
	})
}

// authorizeHandler serves the standalone OAuth authorize wrapper page.
// This page can be opened in any browser independently for cross-browser login.
func authorizeHandler() http.Handler {
	data, err := adminAssets.ReadFile("static/authorize.html")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path != "/authorize.html" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(data)
	})
}
