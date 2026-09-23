// Package web serves the dashboard built from ../../../frontend.
// `npm run build` in frontend/ writes into dist/, which is embedded here, so
// the release binary carries the UI with it.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var dist embed.FS

const notBuilt = `<!doctype html><meta charset="utf-8"><title>RLCD Gateway</title>
<body style="font:15px system-ui;padding:40px;max-width:640px">
<h1>RLCD Gateway is running</h1>
<p>The proxy works, but the dashboard was not built into this binary.</p>
<pre>cd frontend && npm install && npm run build</pre>
<p>Then rebuild the gateway, or run <code>npm run dev</code> for the dev server.</p>`

// Handler serves the SPA at /ui/, falling back to index.html for client routes.
func Handler() http.Handler {
	sub, _ := fs.Sub(dist, "dist")
	files := http.FileServer(http.FS(sub))
	return http.StripPrefix("/ui", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := fs.Stat(sub, "index.html"); err != nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(notBuilt))
			return
		}
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		if _, err := fs.Stat(sub, p); err != nil {
			r.URL.Path = "/"
		}
		files.ServeHTTP(w, r)
	}))
}
