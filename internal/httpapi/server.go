// Package httpapi is the HTTP transport: routing, middleware, and mapping
// between JSON and the app package. It contains no business rules.
package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/bfischetti/kyc-lifecycle/internal/app"
	"github.com/bfischetti/kyc-lifecycle/internal/metrics"
)

// Handler builds the service's HTTP handler. With m nil there is no
// /metrics endpoint and no latency recording.
func Handler(svc *app.Service, log *slog.Logger, ready func(*http.Request) error, m *metrics.Metrics) http.Handler {
	h := &handlers{svc: svc, log: log}
	auth := authenticate(svc, log)
	// Only where a duplicate is harmful: a second Entity, a second paid
	// provider call. Reviews are already safe: a retry is a Stale Decision.
	idem := idempotent(svc, log)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := ready(r); err != nil {
			writeError(w, r, log, http.StatusServiceUnavailable, "unavailable", "dependency not ready", nil)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.Handle("POST /entities", auth(idem(http.HandlerFunc(h.createEntity))))
	mux.Handle("GET /entities/{id}", auth(http.HandlerFunc(h.getEntity)))
	mux.Handle("POST /entities/{id}/documents", auth(http.HandlerFunc(h.declareDocument)))
	mux.Handle("POST /entities/{id}/documents/{docID}/complete", auth(http.HandlerFunc(h.completeUpload)))
	mux.Handle("GET /entities/{id}/documents/{docID}/content", auth(http.HandlerFunc(h.documentContent)))
	mux.Handle("GET /entities/{id}/history", auth(http.HandlerFunc(h.history)))
	mux.Handle("POST /entities/{id}/checks", auth(idem(http.HandlerFunc(h.runCheck))))
	mux.Handle("POST /entities/{id}/review", auth(http.HandlerFunc(h.review)))
	if m == nil {
		return requestID(accessLog(log, recoverPanics(log, mux)))
	}
	// Unauthenticated, like /healthz: scrapers carry no user identity, and
	// nothing here identifies an Entity or a person.
	mux.Handle("GET /metrics", m.Handler())
	return requestID(accessLog(log, observeLatency(m, recoverPanics(log, mux))))
}
