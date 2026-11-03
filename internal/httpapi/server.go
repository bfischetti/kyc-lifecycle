// Package httpapi is the HTTP transport: routing, middleware, and mapping
// between JSON and the app package. It contains no business rules.
package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/bfischetti/kyc-lifecycle/internal/app"
)

// Handler builds the service's HTTP handler.
func Handler(svc *app.Service, log *slog.Logger, ready func(*http.Request) error) http.Handler {
	h := &handlers{svc: svc, log: log}
	auth := authenticate(svc, log)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := ready(r); err != nil {
			writeError(w, r, log, http.StatusServiceUnavailable, "unavailable", "dependency not ready", nil)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.Handle("POST /entities", auth(http.HandlerFunc(h.createEntity)))
	mux.Handle("GET /entities/{id}", auth(http.HandlerFunc(h.getEntity)))
	mux.Handle("POST /entities/{id}/documents", auth(http.HandlerFunc(h.declareDocument)))

	return requestID(accessLog(log, recoverPanics(log, mux)))
}
