// Command webhook-sink is a stand-in Downstream Consumer. It receives Status
// Change Notifications, verifies their signatures and logs what arrived, so
// a local run shows delivery end to end.
//
// Configuration (environment):
//
//	WEBHOOK_SECRET     shared signing secret (required)
//	SINK_ADDR          listen address (default :8090)
//	WEBHOOK_TOLERANCE  accepted clock difference for timestamps (default 5m)
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/bfischetti/kyc-lifecycle/internal/webhook"
)

const maxBody = 1 << 20

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("webhook-sink", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	secret := os.Getenv("WEBHOOK_SECRET")
	if secret == "" {
		return errors.New("WEBHOOK_SECRET is required")
	}
	addr := envOr("SINK_ADDR", ":8090")
	tolerance := webhook.DefaultTolerance
	if v := os.Getenv("WEBHOOK_TOLERANCE"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return fmt.Errorf("WEBHOOK_TOLERANCE: want a positive duration, got %q", v)
		}
		tolerance = d
	}

	mux := http.NewServeMux()
	mux.Handle("POST /webhooks", newSink(webhook.Verifier{Secret: []byte(secret), Tolerance: tolerance}, log))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 1)
	go func() {
		log.Info("webhook-sink listening", slog.String("addr", addr), slog.Duration("tolerance", tolerance))
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
		close(errc)
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shutdown)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// sink verifies and logs deliveries. Deliveries are at least once, so it
// remembers ids it has accepted and reports redeliveries as duplicates,
// as a real consumer would before acting on one.
type sink struct {
	verifier webhook.Verifier
	log      *slog.Logger
	mu       sync.Mutex
	seen     map[string]bool
}

func newSink(v webhook.Verifier, log *slog.Logger) *sink {
	return &sink{verifier: v, log: log, seen: map[string]bool{}}
}

type notification struct {
	Type string `json:"type"`
	Data struct {
		EventID       string `json:"event_id"`
		EntityID      string `json:"entity_id"`
		EntityVersion int    `json:"entity_version"`
		From          string `json:"from"`
		To            string `json:"to"`
		Reason        string `json:"reason"`
		ChangedBy     string `json:"changed_by"`
		OccurredAt    string `json:"occurred_at"`
	} `json:"data"`
}

func (s *sink) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		http.Error(w, "body too large or unreadable", http.StatusRequestEntityTooLarge)
		return
	}
	// Verify before parsing: an unauthenticated body is not looked at.
	if err := s.verifier.Verify(r.Header, body); err != nil {
		s.log.Warn("delivery rejected", slog.String("reason", err.Error()),
			slog.String("webhook_id", r.Header.Get(webhook.HeaderID)))
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	var n notification
	if err := json.Unmarshal(body, &n); err != nil {
		http.Error(w, "malformed notification", http.StatusBadRequest)
		return
	}

	id := r.Header.Get(webhook.HeaderID)
	s.mu.Lock()
	duplicate := s.seen[id]
	s.seen[id] = true
	s.mu.Unlock()

	s.log.Info("notification received",
		slog.String("webhook_id", id),
		slog.Bool("duplicate", duplicate),
		slog.Bool("signature_verified", true),
		slog.String("type", n.Type),
		slog.String("entity_id", n.Data.EntityID),
		slog.Int("entity_version", n.Data.EntityVersion),
		slog.String("from", n.Data.From),
		slog.String("to", n.Data.To),
		slog.String("reason", n.Data.Reason),
	)
	w.WriteHeader(http.StatusNoContent)
}
