//go:build integration

package outbox_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bfischetti/kyc-lifecycle/internal/outbox"
	"github.com/bfischetti/kyc-lifecycle/internal/webhook"
)

const testSecret = "outbox-webhook-test-secret-0123456789"

// receiver is a Downstream Consumer that verifies every request with the
// shared secret and can be told how to answer each attempt.
type receiver struct {
	t        *testing.T
	mu       sync.Mutex
	verified []delivery
	rejected int
	answer   func(attempt int) (status int, delay time.Duration)
	attempts int
}

type delivery struct {
	id   string
	body []byte
}

func newReceiver(t *testing.T, answer func(attempt int) (int, time.Duration)) (*receiver, *httptest.Server) {
	r := &receiver{t: t, answer: answer}
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return r, srv
}

func (r *receiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	r.attempts++
	attempt := r.attempts
	r.mu.Unlock()

	status, delay := http.StatusNoContent, time.Duration(0)
	if r.answer != nil {
		status, delay = r.answer(attempt)
	}
	select {
	case <-time.After(delay):
	case <-req.Context().Done():
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if err := (webhook.Verifier{Secret: []byte(testSecret)}).Verify(req.Header, body); err != nil {
		r.rejected++
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if status >= 200 && status < 300 {
		r.verified = append(r.verified, delivery{id: req.Header.Get(webhook.HeaderID), body: body})
	}
	w.WriteHeader(status)
}

func (r *receiver) snapshot() (verified []delivery, rejected, attempts int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]delivery(nil), r.verified...), r.rejected, r.attempts
}

// runLogged runs a worker whose log is captured, so tests can inspect it.
func runLogged(t *testing.T, h *harness, pub outbox.Publisher, opts outbox.Options) *syncBuf {
	t.Helper()
	logs := &syncBuf{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		outbox.NewWorker(h.store, pub, slog.New(slog.NewJSONHandler(logs, nil)), opts).Run(ctx)
		close(done)
	}()
	t.Cleanup(func() { cancel(); <-done })
	return logs
}

type syncBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func (h *harness) published(t *testing.T, id uuid.UUID) bool {
	return h.str(t, "SELECT (published_at IS NOT NULL)::text FROM outbox WHERE id = $1", id) == "true"
}

func TestWebhookDelivery(t *testing.T) {
	h := newHarness(t)

	t.Run("a notification arrives as a POST whose signature verifies", func(t *testing.T) {
		rec, srv := newReceiver(t, nil)
		id := h.enqueue(t, 1)[0]
		logs := runLogged(t, h, outbox.WebhookPublisher{URL: srv.URL, Secret: []byte(testSecret)}, fastOptions)

		eventually(t, 5*time.Second, func() bool { return h.published(t, id) }, "published_at set")
		verified, rejected, _ := rec.snapshot()
		if len(verified) != 1 || rejected != 0 {
			t.Fatalf("verified %d, rejected %d; want 1 verified", len(verified), rejected)
		}
		if verified[0].id != id.String() {
			t.Errorf("webhook id = %s, want the notification id %s", verified[0].id, id)
		}
		var env struct {
			Type string         `json:"type"`
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(verified[0].body, &env); err != nil {
			t.Fatal(err)
		}
		if env.Type != "entity.status_changed" || env.Data["event_id"] != id.String() || env.Data["to"] != "pending_review" {
			t.Errorf("body = %s", verified[0].body)
		}
		if strings.Contains(logs.String(), testSecret) {
			t.Error("the secret appears in the worker's log")
		}
	})

	t.Run("5xx responses are retried until the receiver recovers", func(t *testing.T) {
		rec, srv := newReceiver(t, func(attempt int) (int, time.Duration) {
			if attempt <= 2 {
				return http.StatusServiceUnavailable, 0
			}
			return http.StatusOK, 0
		})
		id := h.enqueue(t, 1)[0]
		logs := runLogged(t, h, outbox.WebhookPublisher{URL: srv.URL, Secret: []byte(testSecret)}, fastOptions)

		eventually(t, 10*time.Second, func() bool { return h.published(t, id) }, "delivered after recovery")
		verified, _, attempts := rec.snapshot()
		if attempts != 3 || len(verified) != 1 {
			t.Errorf("attempts %d, verified %d; want 3 attempts and 1 delivery", attempts, len(verified))
		}
		if a := h.str(t, "SELECT attempts::text FROM outbox WHERE id = $1", id); a != "3" {
			t.Errorf("recorded attempts = %s, want 3", a)
		}
		if !strings.Contains(logs.String(), "receiver answered 503") {
			t.Error("the failed attempts were not logged with the receiver's status")
		}
		if strings.Contains(logs.String(), testSecret) {
			t.Error("the secret appears in the worker's log")
		}
	})

	t.Run("a receiver that times out is retried", func(t *testing.T) {
		opts := fastOptions
		opts.PublishTimeout = 200 * time.Millisecond
		rec, srv := newReceiver(t, func(attempt int) (int, time.Duration) {
			if attempt == 1 {
				return http.StatusOK, 2 * time.Second // far beyond the publish timeout
			}
			return http.StatusOK, 0
		})
		id := h.enqueue(t, 1)[0]
		runLogged(t, h, outbox.WebhookPublisher{URL: srv.URL, Secret: []byte(testSecret)}, opts)

		eventually(t, 10*time.Second, func() bool { return h.published(t, id) }, "delivered after a timeout")
		verified, _, attempts := rec.snapshot()
		if attempts < 2 || len(verified) != 1 {
			t.Errorf("attempts %d, verified %d; want a retry after the timeout and 1 delivery", attempts, len(verified))
		}
		if e := h.str(t, "SELECT coalesce(last_error, '') FROM outbox WHERE id = $1", id); e != "" {
			t.Errorf("last_error = %q after success, want it cleared", e)
		}
	})

	t.Run("a receiver with a different secret never accepts, and the notification is not lost", func(t *testing.T) {
		rec, srv := newReceiver(t, nil)
		opts := fastOptions
		opts.MaxAttempts = 3
		id := h.enqueue(t, 1)[0]
		runLogged(t, h, outbox.WebhookPublisher{URL: srv.URL, Secret: []byte("a-different-secret-0123456789abcdef")}, opts)

		eventually(t, 10*time.Second, func() bool {
			return h.str(t, "SELECT (dead_at IS NOT NULL)::text FROM outbox WHERE id = $1", id) == "true"
		}, "dead-lettered after max attempts")
		if verified, rejected, _ := rec.snapshot(); len(verified) != 0 || rejected != 3 {
			t.Errorf("verified %d, rejected %d; want 0 and 3", len(verified), rejected)
		}
	})
}
