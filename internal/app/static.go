package app

import (
	"io/fs"
	"net/http"
)

// Assets holds the embedded static/ filesystem. It is set by the root main
// package at startup via Run(assets) before any handler reads it. The concrete
// type at runtime is embed.FS, but tests can substitute any fs.FS (e.g.
// fstest.MapFS or os.DirFS).
var Assets fs.FS

// adminStatic returns an http.Handler that serves files from the embedded
// static/ directory, rooted so /admin/ maps to static/index.html.
func adminStatic() http.Handler {
	sub, err := fs.Sub(Assets, "static")
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
	data, err := fs.ReadFile(Assets, "static/login.html")
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
	data, err := fs.ReadFile(Assets, "static/authorize.html")
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
