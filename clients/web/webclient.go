// Package webclient is a browser client for aimux's remote API: a phone
// web app served at /app/ by `aimux remote serve`. It talks to the server
// only through the documented /v1 contract (api/remote-v1.openapi.yaml, served at /v1/openapi.yaml),
// like any other client would.
package webclient

import (
	"embed"
	"io/fs"
	"mime"
	"net/http"
	"strings"
)

//go:embed webapp
var webappFiles embed.FS

// webAppCSP lets the page load only its own script and style and talk only
// to its own origin; agent output it renders can never pull anything in.
const webAppCSP = "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; " +
	"img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// Go's mime table may not know .mjs; browsers refuse ES modules without a JavaScript type.
func init() {
	_ = mime.AddExtensionType(".mjs", "text/javascript; charset=utf-8")
}

// Handler serves the web app under /app/, with a Content-Security-Policy
// that keeps it to its own files and its own origin.
func Handler() http.Handler {
	sub, err := fs.Sub(webappFiles, "webapp")
	if err != nil {
		panic(err) // the directory is embedded at build time
	}
	files := http.StripPrefix("/app/", http.FileServerFS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", webAppCSP)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-cache")
		if strings.HasSuffix(r.URL.Path, ".test.mjs") { // tests live beside the code but are not part of the app
			http.NotFound(w, r)
			return
		}
		files.ServeHTTP(w, r)
	})
}
