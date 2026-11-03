//go:build integration

package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/bfischetti/kyc-lifecycle/internal/app"
	"github.com/bfischetti/kyc-lifecycle/internal/db"
	"github.com/bfischetti/kyc-lifecycle/internal/httpapi"
	"github.com/bfischetti/kyc-lifecycle/internal/metrics"
	"github.com/bfischetti/kyc-lifecycle/internal/observability"
	"github.com/bfischetti/kyc-lifecycle/internal/screening"
	"github.com/bfischetti/kyc-lifecycle/internal/testenv"
	"github.com/bfischetti/kyc-lifecycle/internal/vault"
)

const (
	complianceOfficer = "00000000-0000-0000-0000-000000000001"
	supportAgent      = "00000000-0000-0000-0000-000000000002"
	systemUser        = "00000000-0000-0000-0000-000000000003"
)

// syncBuffer is a concurrency-safe log sink.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *syncBuffer) Lines() []string {
	return strings.Split(strings.TrimSpace(b.String()), "\n")
}

// env is a running service backed by real Postgres and a real vault.
type env struct {
	srv   *httptest.Server
	logs  *syncBuffer
	pg    testenv.Postgres
	vault testenv.Vault
}

type config struct {
	opts     app.Options
	provider screening.Provider
}

type option func(*config)

func withUploadTTL(d time.Duration) option {
	return func(c *config) { c.opts.UploadURLTTL = d }
}

func withProviderTimeout(d time.Duration) option {
	return func(c *config) { c.opts.ProviderTimeout = d }
}

func withProvider(p screening.Provider) option {
	return func(c *config) { c.provider = p }
}

func setup(t *testing.T, opts ...option) *env {
	t.Helper()
	pg := testenv.StartPostgres(t)
	vlt := testenv.StartVault(t)

	store, err := db.Open(context.Background(), pg.AppURL)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(store.Close)

	cfg := config{opts: app.DefaultOptions, provider: screening.Mock{}}
	for _, opt := range opts {
		opt(&cfg)
	}
	evidence := vault.New(vault.Config{
		Endpoint: vlt.Endpoint, Region: vlt.Region,
		AccessKey: vlt.AccessKey, SecretKey: vlt.SecretKey, Bucket: vlt.Bucket,
	})

	logs := &syncBuffer{}
	log := observability.NewLogger(logs, "debug")
	m := metrics.New(store.OutboxBacklog, log)
	cfg.opts.Metrics = m
	srv := httptest.NewServer(httpapi.Handler(app.New(store, evidence, cfg.provider, cfg.opts), log, func(*http.Request) error { return nil }, m))
	t.Cleanup(srv.Close)
	return &env{srv: srv, logs: logs, pg: pg, vault: vlt}
}

// result is a fully read response, so callers never hold an open body.
type result struct {
	StatusCode int
	Header     http.Header
	Raw        []byte
}

func (e *env) do(t *testing.T, method, path, user, body string, headers ...string) (result, map[string]any) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, e.srv.URL+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if user != "" {
		req.Header.Set("X-User-ID", user)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return result{StatusCode: resp.StatusCode, Header: resp.Header, Raw: raw}, out
}

// createEntity creates an Entity and returns its id.
func (e *env) createEntity(t *testing.T, typ, legalName string) string {
	t.Helper()
	resp, body := e.do(t, "POST", "/entities", supportAgent,
		`{"type":"`+typ+`","legal_name":"`+legalName+`","country":"GB"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create entity: status %d, body %v", resp.StatusCode, body)
	}
	return body["id"].(string)
}

// queryInt runs a single-value integer query as the superuser.
func (e *env) queryInt(t *testing.T, sql string, args ...any) int {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), e.pg.AdminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	var n int
	if err := conn.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// queryString runs a single-value text query as the superuser.
func (e *env) queryString(t *testing.T, sql string, args ...any) string {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), e.pg.AdminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	var s string
	if err := conn.QueryRow(context.Background(), sql, args...).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}
