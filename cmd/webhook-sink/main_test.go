package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bfischetti/kyc-lifecycle/internal/webhook"
)

var testSecret = []byte("sink-test-secret-0123456789abcdef")

const body = `{"type":"entity.status_changed","data":{"event_id":"e1","entity_id":"ent1","entity_version":2,"from":"pending_review","to":"verified"}}`

type logged struct {
	bytes.Buffer
}

func (l *logged) lines(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(l.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line is not JSON: %s", line)
		}
		out = append(out, m)
	}
	return out
}

func newTestSink(now time.Time) (*sink, *logged) {
	logs := &logged{}
	return newSink(webhook.Verifier{Secret: testSecret, Now: func() time.Time { return now }},
		slog.New(slog.NewJSONHandler(logs, nil))), logs
}

func deliver(s http.Handler, id string, ts time.Time, signedBody, sentBody string, secret []byte) int {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/webhooks", strings.NewReader(sentBody))
	webhook.SetHeaders(req.Header, secret, id, ts, []byte(signedBody))
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec.Code
}

func TestSink(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	tampered := strings.Replace(body, `"to":"verified"`, `"to":"rejected"`, 1)

	tests := []struct {
		name       string
		ts         time.Time
		signedBody string
		sentBody   string
		secret     []byte
		want       int
	}{
		{"valid delivery is accepted", now, body, body, testSecret, http.StatusNoContent},
		{"wrong signature (other secret)", now, body, body, []byte("not-the-shared-secret-0123456789ab"), http.StatusUnauthorized},
		{"tampered body", now, body, tampered, testSecret, http.StatusUnauthorized},
		{"timestamp too old (replay)", now.Add(-webhook.DefaultTolerance - time.Second), body, body, testSecret, http.StatusUnauthorized},
		{"timestamp too far ahead", now.Add(webhook.DefaultTolerance + time.Second), body, body, testSecret, http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, logs := newTestSink(now)
			if got := deliver(s, "msg_1", tt.ts, tt.signedBody, tt.sentBody, tt.secret); got != tt.want {
				t.Fatalf("status = %d, want %d", got, tt.want)
			}
			lines := logs.lines(t)
			if len(lines) != 1 {
				t.Fatalf("log lines = %v, want exactly one", lines)
			}
			wantMsg := "notification received"
			if tt.want != http.StatusNoContent {
				wantMsg = "delivery rejected"
			}
			if lines[0]["msg"] != wantMsg {
				t.Errorf("logged %q, want %q", lines[0]["msg"], wantMsg)
			}
			if strings.Contains(logs.String(), string(testSecret)) {
				t.Error("the secret appears in the sink's log")
			}
		})
	}

	t.Run("missing headers are rejected", func(t *testing.T) {
		s, _ := newTestSink(now)
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/webhooks", strings.NewReader(body)))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", rec.Code)
		}
	})

	t.Run("a redelivery is accepted and reported as a duplicate", func(t *testing.T) {
		s, logs := newTestSink(now)
		deliver(s, "msg_1", now, body, body, testSecret)
		if got := deliver(s, "msg_1", now.Add(time.Second), body, body, testSecret); got != http.StatusNoContent {
			t.Fatalf("redelivery status = %d, want 204", got)
		}
		lines := logs.lines(t)
		if lines[0]["duplicate"] != false || lines[1]["duplicate"] != true {
			t.Errorf("duplicate flags = %v, %v; want false, true", lines[0]["duplicate"], lines[1]["duplicate"])
		}
		if lines[1]["entity_version"] != float64(2) || lines[1]["to"] != "verified" {
			t.Errorf("logged notification = %v", lines[1])
		}
	})

	t.Run("a notification queued before reason was removed is accepted and the reason isn't logged", func(t *testing.T) {
		const secretReason = "possible link to a sanctioned party"
		queued := strings.Replace(body, `"to":"verified"`, `"to":"rejected","reason":"`+secretReason+`"`, 1)
		s, logs := newTestSink(now)
		if got := deliver(s, "msg_1", now, queued, queued, testSecret); got != http.StatusNoContent {
			t.Fatalf("status = %d, want 204", got)
		}
		if strings.Contains(logs.String(), secretReason) {
			t.Errorf("the reason appears in the sink's log: %s", logs.String())
		}
		if lines := logs.lines(t); lines[0]["to"] != "rejected" {
			t.Errorf("logged notification = %v", lines[0])
		}
	})

	t.Run("an oversized body is refused", func(t *testing.T) {
		s, _ := newTestSink(now)
		big := strings.Repeat("x", maxBody+1)
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/webhooks", strings.NewReader(big))
		req.Header.Set(webhook.HeaderID, "msg_big")
		req.Header.Set(webhook.HeaderTimestamp, strconv.FormatInt(now.Unix(), 10))
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("status = %d, want 413", rec.Code)
		}
	})
}
