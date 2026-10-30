package main

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed web
var webFS embed.FS

// pageFS serves the two HTML pages (web/index.html, web/r.html).
func pageFS() fs.FS {
	f, err := fs.Sub(webFS, "web")
	if err != nil {
		panic(err)
	}
	return f
}

// assetsFS serves web/static/* under /static/*.
func assetsFS() fs.FS {
	f, err := fs.Sub(webFS, "web/static")
	if err != nil {
		panic(err)
	}
	return f
}

func (s *server) handleIndexPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFileFS(w, r, pageFS(), "index.html")
}

func (s *server) handleReceivePage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFileFS(w, r, pageFS(), "r.html")
}
