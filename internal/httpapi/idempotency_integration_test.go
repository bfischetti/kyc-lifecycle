//go:build integration

package httpapi_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/bfischetti/kyc-lifecycle/internal/kyc"
	"github.com/bfischetti/kyc-lifecycle/internal/screening"
)

// countingProvider counts calls to the mock, optionally failing the first
// ones with a Provider Error or delaying each answer.
type countingProvider struct {
	calls     atomic.Int64
	failUntil atomic.Int64 // calls numbered up to this fail
	delay     atomic.Int64 // nanoseconds
}

func (p *countingProvider) Screen(ctx context.Context, s screening.Subject, c kyc.CheckType) (screening.Outcome, error) {
	n := p.calls.Add(1)
	if n <= p.failUntil.Load() {
		return screening.Outcome{}, fmt.Errorf("%w: upstream 503", screening.ErrUnavailable)
	}
	select {
	case <-time.After(time.Duration(p.delay.Load())):
	case <-ctx.Done():
		return screening.Outcome{}, ctx.Err()
	}
	return screening.Mock{}.Screen(ctx, s, c)
}

const entityBody = `{"type":"individual","legal_name":"Ada Lovelace","country":"GB"}`

func (e *env) post(t *testing.T, path, user, key, body string) result {
	t.Helper()
	var headers []string
	if key != "" {
		headers = []string{"Idempotency-Key", key}
	}
	resp, _ := e.do(t, "POST", path, user, body, headers...)
	return resp
}

func (e *env) countEntities(t *testing.T, legalName string) int {
	t.Helper()
	return e.queryInt(t, "SELECT count(*) FROM entities WHERE legal_name = $1", legalName)
}

func TestIdempotentEntityCreation(t *testing.T) {
	e := setup(t)

	t.Run("a retry with the same key and body returns the original response", func(t *testing.T) {
		key, name := uuid.NewString(), "Idem "+uuid.NewString()[:8]
		body := strings.Replace(entityBody, "Ada Lovelace", name, 1)
		first := e.post(t, "/entities", supportAgent, key, body)
		second := e.post(t, "/entities", supportAgent, key, body)

		if first.StatusCode != http.StatusCreated || second.StatusCode != first.StatusCode || !bytes.Equal(first.Raw, second.Raw) {
			t.Fatalf("first %d %s; second %d %s; want identical 201s", first.StatusCode, first.Raw, second.StatusCode, second.Raw)
		}
		if first.Header.Get("Idempotent-Replayed") != "" || second.Header.Get("Idempotent-Replayed") != "true" {
			t.Errorf("Idempotent-Replayed: first %q, second %q; want only the retry marked",
				first.Header.Get("Idempotent-Replayed"), second.Header.Get("Idempotent-Replayed"))
		}
		if n := e.countEntities(t, name); n != 1 {
			t.Errorf("entities created = %d, want 1", n)
		}
	})

	t.Run("reusing a key with a different body is refused and does nothing", func(t *testing.T) {
		key, name := uuid.NewString(), "Idem "+uuid.NewString()[:8]
		e.post(t, "/entities", supportAgent, key, strings.Replace(entityBody, "Ada Lovelace", name, 1))
		other := "Other " + uuid.NewString()[:8]
		resp := e.post(t, "/entities", supportAgent, key, strings.Replace(entityBody, "Ada Lovelace", other, 1))
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(resp.Raw), "idempotency_key_reused") {
			t.Fatalf("status %d %s, want 422 idempotency_key_reused", resp.StatusCode, resp.Raw)
		}
		if n := e.countEntities(t, other); n != 0 {
			t.Errorf("entities created by the refused request = %d, want 0", n)
		}
	})

	t.Run("the same key from a different user is unrelated", func(t *testing.T) {
		key, name := uuid.NewString(), "Idem "+uuid.NewString()[:8]
		body := strings.Replace(entityBody, "Ada Lovelace", name, 1)
		a := e.post(t, "/entities", supportAgent, key, body)
		b := e.post(t, "/entities", complianceOfficer, key, body)
		if a.StatusCode != http.StatusCreated || b.StatusCode != http.StatusCreated || b.Header.Get("Idempotent-Replayed") != "" {
			t.Fatalf("statuses %d, %d (replayed %q); want two fresh 201s", a.StatusCode, b.StatusCode, b.Header.Get("Idempotent-Replayed"))
		}
		if n := e.countEntities(t, name); n != 2 {
			t.Errorf("entities = %d, want 2", n)
		}
	})

	t.Run("without the header nothing changes", func(t *testing.T) {
		name := "Idem " + uuid.NewString()[:8]
		body := strings.Replace(entityBody, "Ada Lovelace", name, 1)
		e.post(t, "/entities", supportAgent, "", body)
		e.post(t, "/entities", supportAgent, "", body)
		if n := e.countEntities(t, name); n != 2 {
			t.Errorf("entities = %d, want 2 (no key, no de-duplication)", n)
		}
	})

	t.Run("a validation error is replayed too", func(t *testing.T) {
		key := uuid.NewString()
		first := e.post(t, "/entities", supportAgent, key, `{"type":"individual","legal_name":"","country":"GB"}`)
		second := e.post(t, "/entities", supportAgent, key, `{"type":"individual","legal_name":"","country":"GB"}`)
		if first.StatusCode != http.StatusBadRequest || second.StatusCode != http.StatusBadRequest || second.Header.Get("Idempotent-Replayed") != "true" {
			t.Errorf("statuses %d, %d (replayed %q); want 400 then a replayed 400", first.StatusCode, second.StatusCode, second.Header.Get("Idempotent-Replayed"))
		}
	})

	t.Run("a malformed key is refused", func(t *testing.T) {
		for _, key := range []string{strings.Repeat("k", 256), "has space"} {
			if resp := e.post(t, "/entities", supportAgent, key, entityBody); resp.StatusCode != http.StatusBadRequest {
				t.Errorf("key %.20q…: status %d, want 400", key, resp.StatusCode)
			}
		}
	})
}

func TestIdempotentChecks(t *testing.T) {
	provider := &countingProvider{}
	e := setup(t, withProvider(provider))
	checkBody := `{"check_type":"sanctions_screening"}`
	checks := func(entityID string) int {
		return e.queryInt(t, "SELECT count(*) FROM checks WHERE entity_id = $1", entityID)
	}

	t.Run("a retried check calls the provider once and records one check", func(t *testing.T) {
		id := e.pendingEntity(t, "individual", "Ada Lovelace")
		before := provider.calls.Load()
		key := uuid.NewString()
		first := e.post(t, "/entities/"+id+"/checks", complianceOfficer, key, checkBody)
		second := e.post(t, "/entities/"+id+"/checks", complianceOfficer, key, checkBody)

		if first.StatusCode != http.StatusCreated || !bytes.Equal(first.Raw, second.Raw) {
			t.Fatalf("first %d, second %d; want identical 201s", first.StatusCode, second.StatusCode)
		}
		if calls := provider.calls.Load() - before; calls != 1 {
			t.Errorf("provider calls = %d, want 1", calls)
		}
		if n := checks(id); n != 1 {
			t.Errorf("checks = %d, want 1", n)
		}
	})

	t.Run("after a provider error the same key reaches the provider again", func(t *testing.T) {
		id := e.pendingEntity(t, "individual", "Ada Lovelace")
		provider.failUntil.Store(provider.calls.Load() + 1) // the next call fails
		before := provider.calls.Load()
		key := uuid.NewString()

		if resp := e.post(t, "/entities/"+id+"/checks", complianceOfficer, key, checkBody); resp.StatusCode != http.StatusBadGateway {
			t.Fatalf("first attempt: status %d, want 502", resp.StatusCode)
		}
		resp := e.post(t, "/entities/"+id+"/checks", complianceOfficer, key, checkBody)
		if resp.StatusCode != http.StatusCreated || resp.Header.Get("Idempotent-Replayed") != "" {
			t.Fatalf("retry: status %d (replayed %q), want a fresh 201", resp.StatusCode, resp.Header.Get("Idempotent-Replayed"))
		}
		if calls := provider.calls.Load() - before; calls != 2 {
			t.Errorf("provider calls = %d, want 2 (the failure, then the retry)", calls)
		}
		if n := checks(id); n != 1 {
			t.Errorf("checks = %d, want 1", n)
		}
	})

	t.Run("simultaneous requests with the same key do the work once", func(t *testing.T) {
		id := e.pendingEntity(t, "individual", "Ada Lovelace")
		provider.delay.Store(int64(300 * time.Millisecond))
		defer provider.delay.Store(0)
		before := provider.calls.Load()
		key := uuid.NewString()

		const n = 5
		results := make([]result, n)
		var wg sync.WaitGroup
		for i := range results {
			wg.Add(1)
			go func() {
				defer wg.Done()
				results[i] = e.post(t, "/entities/"+id+"/checks", complianceOfficer, key, checkBody)
			}()
		}
		wg.Wait()

		if calls := provider.calls.Load() - before; calls != 1 {
			t.Errorf("provider calls = %d, want 1", calls)
		}
		if c := checks(id); c != 1 {
			t.Errorf("checks = %d, want 1", c)
		}
		for i, r := range results {
			if r.StatusCode != http.StatusCreated || !bytes.Equal(r.Raw, results[0].Raw) {
				t.Errorf("request %d: status %d, body differs %v; want the same 201 for all", i, r.StatusCode, !bytes.Equal(r.Raw, results[0].Raw))
			}
		}
	})
}

func TestIdempotencyKeysBackstop(t *testing.T) {
	e := setup(t)
	e.post(t, "/entities", supportAgent, uuid.NewString(), entityBody)

	ownKeys := func(userID string) int {
		t.Helper()
		ctx := context.Background()
		conn, err := pgx.Connect(ctx, e.pg.AppURL)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close(ctx) }()
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, "SELECT set_config('app.user_id', $1, true)", userID); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM idempotency_keys").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := ownKeys(supportAgent); n != 1 {
		t.Fatalf("own keys = %d, want 1; the test would prove nothing", n)
	}
	if n := ownKeys(complianceOfficer); n != 0 {
		t.Errorf("another user's keys visible: %d", n)
	}
	if n := ownKeys(""); n != 0 {
		t.Errorf("keys visible with no user set: %d", n)
	}
}

func withIdempotencyWait(d time.Duration) option {
	return func(c *config) { c.opts.Idempotency.Wait = d }
}

// plantClaim inserts an unfinished claim for (user, key) on POST /entities
// with body, started `age` ago, as a crashed or slow request would leave it.
func (e *env) plantClaim(t *testing.T, user, key, body string, age time.Duration) {
	t.Helper()
	sum := sha256.Sum256([]byte("POST /entities\n" + body))
	e.adminExec(t, fmt.Sprintf(
		`INSERT INTO idempotency_keys (user_id, key, fingerprint, started_at)
		 VALUES ('%s', '%s', '\x%s', now() - interval '%d seconds')`,
		user, key, hex.EncodeToString(sum[:]), int(age.Seconds())))
}

func TestIdempotencyClaims(t *testing.T) {
	e := setup(t, withIdempotencyWait(300*time.Millisecond))

	t.Run("a claim abandoned past its lease is taken over and the work done", func(t *testing.T) {
		key, name := uuid.NewString(), "Idem "+uuid.NewString()[:8]
		body := strings.Replace(entityBody, "Ada Lovelace", name, 1)
		e.plantClaim(t, supportAgent, key, body, 2*time.Minute) // the default lease is 1m

		resp := e.post(t, "/entities", supportAgent, key, body)
		if resp.StatusCode != http.StatusCreated || resp.Header.Get("Idempotent-Replayed") != "" {
			t.Fatalf("status %d (replayed %q), want a fresh 201", resp.StatusCode, resp.Header.Get("Idempotent-Replayed"))
		}
		if n := e.countEntities(t, name); n != 1 {
			t.Errorf("entities = %d, want 1", n)
		}
		if again := e.post(t, "/entities", supportAgent, key, body); again.Header.Get("Idempotent-Replayed") != "true" {
			t.Error("the taken-over key was not completed: its retry was not replayed")
		}
	})

	t.Run("a live claim that outlasts the wait gives a retryable 409", func(t *testing.T) {
		key, name := uuid.NewString(), "Idem "+uuid.NewString()[:8]
		body := strings.Replace(entityBody, "Ada Lovelace", name, 1)
		e.plantClaim(t, supportAgent, key, body, 0)

		resp := e.post(t, "/entities", supportAgent, key, body)
		if resp.StatusCode != http.StatusConflict || !strings.Contains(string(resp.Raw), `"retryable":true`) ||
			!strings.Contains(string(resp.Raw), "idempotency_key_in_progress") {
			t.Fatalf("status %d %s, want a retryable 409 idempotency_key_in_progress", resp.StatusCode, resp.Raw)
		}
		if n := e.countEntities(t, name); n != 0 {
			t.Errorf("entities = %d, want 0 while another request holds the key", n)
		}
	})
}
