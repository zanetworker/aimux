package remoteapi

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed webapp
var webappFiles embed.FS

// webAppCSP lets the page load only its own script and style and talk only
// to its own origin; agent output it renders can never pull anything in.
const webAppCSP = "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; " +
	"img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// webApp serves the browser client under /app/.
func webApp() http.Handler {
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
		files.ServeHTTP(w, r)
	})
}
