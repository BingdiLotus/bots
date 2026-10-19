package httpapi

import "net/http"

func New() *http.ServeMux {
	mux := http.NewServeMux()
	// This is process liveness only; it does not assert any business dependency is ready.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte("{\"status\":\"alive\"}\n"))
	})
	return mux
}
