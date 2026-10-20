package httpapi

import (
	"bytes"
	"embed"
	"html/template"
	"net/http"
)

//go:embed console/index.html console/app.js console/style.css
var consoleFiles embed.FS

var consoleTemplate = template.Must(template.ParseFS(consoleFiles, "console/index.html"))

// registerConsole serves only a public shell and fixed assets. The shell
// receives no business records or credentials; existing APIs retain their
// administrator authentication and tenant authorization boundaries.
func (a *App) registerConsole(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin", func(w http.ResponseWriter, r *http.Request) {
		consoleHeaders(w)
		if a.TenantID == "" {
			http.Error(w, "尚未配置组织，管理页面暂不可用。", http.StatusServiceUnavailable)
			return
		}
		var body bytes.Buffer
		if err := consoleTemplate.Execute(&body, struct{ TenantID string }{a.TenantID}); err != nil {
			http.Error(w, "管理页面暂不可用。", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.Method != http.MethodHead {
			_, _ = w.Write(body.Bytes())
		}
	})
	for _, asset := range []struct{ path, file, contentType string }{
		{"/admin/app.js", "console/app.js", "text/javascript; charset=utf-8"},
		{"/admin/style.css", "console/style.css", "text/css; charset=utf-8"},
	} {
		content, err := consoleFiles.ReadFile(asset.file)
		if err != nil {
			panic(err) // Fixed embedded assets are a build-time invariant.
		}
		mux.HandleFunc("GET "+asset.path, func(w http.ResponseWriter, r *http.Request) {
			consoleHeaders(w)
			w.Header().Set("Content-Type", asset.contentType)
			if r.Method != http.MethodHead {
				_, _ = w.Write(content)
			}
		})
	}
}

func consoleHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'none'; font-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'; object-src 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
}
