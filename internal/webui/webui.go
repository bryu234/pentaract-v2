package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed dist/*
var content embed.FS

func Handler() http.Handler {
	dist, _ := fs.Sub(content, "dist")
	files := http.FileServer(http.FS(dist))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/health/") {
			http.NotFound(w, r)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path != "" {
			if file, err := dist.Open(path); err == nil {
				file.Close()
				files.ServeHTTP(w, r)
				return
			}
		}
		r.URL.Path = "/"
		files.ServeHTTP(w, r)
	})
}
