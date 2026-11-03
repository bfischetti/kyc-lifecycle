package outbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/bfischetti/kyc-lifecycle/internal/webhook"
)

// WebhookPublisher delivers each notification as a signed HTTP POST.
//
// Only a 2xx response counts as delivered. Anything else (another status,
// a timeout, a refused connection) is returned as an error, so the worker
// retries with backoff and eventually dead-letters.
type WebhookPublisher struct {
	URL    string
	Secret []byte
	// Client sends the requests; nil means a default client. The worker's
	// per-attempt timeout bounds each request through its context.
	Client *http.Client
}

// envelope is the request body. The webhook id header carries Message.ID,
// which equals the payload's event_id.
type envelope struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

func (p WebhookPublisher) Publish(ctx context.Context, m Message) error {
	body, err := json.Marshal(envelope{Type: m.Type, Data: m.Payload})
	if err != nil {
		return fmt.Errorf("webhook: encode: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("webhook: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "kyc-lifecycle-webhooks/1")
	// Signed at send time, not enqueue time: each retry gets a fresh
	// timestamp, so a delivery retried for hours still passes the
	// receiver's replay window.
	webhook.SetHeaders(req.Header, p.Secret, m.ID.String(), time.Now(), body)

	client := p.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("webhook: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	// Drain a little so the connection can be reused; never log the body.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("webhook: receiver answered %d", resp.StatusCode)
	}
	return nil
}
