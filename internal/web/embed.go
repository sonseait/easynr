package web

import (
	"embed"
	"net/http"
)

//go:embed static/*
var assets embed.FS

func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			w.WriteHeader(405)
			return
		}
		file := map[string]string{"/": "index.html", "/-/ui/app.js": "app.js", "/-/ui/style.css": "style.css"}[r.URL.Path]
		if file == "" {
			http.NotFound(w, r)
			return
		}
		data, err := assets.ReadFile("static/" + file)
		if err != nil {
			http.Error(w, "asset unavailable", 500)
			return
		}
		mt := map[string]string{"index.html": "text/html; charset=utf-8", "app.js": "text/javascript; charset=utf-8", "style.css": "text/css; charset=utf-8"}[file]
		w.Header().Set("Content-Type", mt)
		if r.Method != "HEAD" {
			w.Write(data)
		}
	})
}
