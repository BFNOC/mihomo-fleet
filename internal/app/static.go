package app

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed web/*
var webFiles embed.FS

func serveStatic(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if name == "." || name == "" {
		name = "index.html"
	}
	fullName := "web/" + name
	if _, err := fs.Stat(webFiles, fullName); err != nil {
		fullName = "web/index.html"
	}
	http.ServeFileFS(w, r, webFiles, fullName)
}
