// Package web serves the embedded settings web UI.
package web

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static
var static embed.FS

// Handler serves the UI files; "/" serves index.html.
func Handler() http.Handler {
	sub, err := fs.Sub(static, "static")
	if err != nil {
		panic(err)
	}
	fsrv := http.FileServerFS(sub)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; connect-src 'self' https://geocoding-api.open-meteo.com; img-src 'self' data:; style-src 'self'; script-src 'self'")
		fsrv.ServeHTTP(w, r)
	})
}
