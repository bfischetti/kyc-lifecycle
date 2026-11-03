//go:build integration

package httpapi_test

import (
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bfischetti/kyc-lifecycle/internal/expiry"
)

// age moves a key's first use into the past, as if it were sent then.
func (e *env) age(t *testing.T, key string, by time.Duration) {
	t.Helper()
	e.adminExec(t, "UPDATE idempotency_keys SET started_at = now() - interval '"+
		by.String()+"' WHERE key = '"+key+"'")
}

func TestIdempotencyKeyRetention(t *testing.T) {
	e := setup(t)
	sweeper := expiry.NewSweeper(e.store(t), slog.New(slog.NewTextHandler(io.Discard, nil)), time.Minute,
		expiry.WithKeyRetention(time.Hour))
	keyCount := func() int { return e.queryInt(t, "SELECT count(*) FROM idempotency_keys") }

	t.Run("keys past the window are deleted, keys inside it are kept", func(t *testing.T) {
		old, fresh := uuid.NewString(), uuid.NewString()
		e.post(t, "/entities", supportAgent, old, entityBody)
		e.post(t, "/entities", supportAgent, fresh, entityBody)
		e.age(t, old, 2*time.Hour)
		e.age(t, fresh, 30*time.Minute)
		before := keyCount()

		n, err := sweeper.SweepIdempotencyKeys(t.Context())
		if err != nil || n != 1 {
			t.Fatalf("SweepIdempotencyKeys() = %d, %v; want 1 deleted", n, err)
		}
		if after := keyCount(); after != before-1 {
			t.Errorf("keys %d → %d, want one fewer", before, after)
		}
		if e.queryInt(t, "SELECT count(*) FROM idempotency_keys WHERE key = $1", fresh) != 1 {
			t.Error("a key inside the window was deleted")
		}
	})

	t.Run("a swept key is treated as new", func(t *testing.T) {
		key, name := uuid.NewString(), "Retained "+uuid.NewString()[:8]
		body := strings.Replace(entityBody, "Ada Lovelace", name, 1)
		if first := e.post(t, "/entities", supportAgent, key, body); first.StatusCode != http.StatusCreated {
			t.Fatalf("first: %d", first.StatusCode)
		}
		e.age(t, key, 2*time.Hour)
		if _, err := sweeper.SweepIdempotencyKeys(t.Context()); err != nil {
			t.Fatal(err)
		}

		again := e.post(t, "/entities", supportAgent, key, body)
		if again.StatusCode != http.StatusCreated || again.Header.Get("Idempotent-Replayed") != "" {
			t.Fatalf("after the sweep: status %d (replayed %q), want a fresh 201", again.StatusCode, again.Header.Get("Idempotent-Replayed"))
		}
		if n := e.countEntities(t, name); n != 2 {
			t.Errorf("entities = %d, want 2 (the key no longer de-duplicates)", n)
		}
	})

	t.Run("the sweep removes every user's old keys", func(t *testing.T) {
		agentKey, officerKey := uuid.NewString(), uuid.NewString()
		e.post(t, "/entities", supportAgent, agentKey, entityBody)
		e.post(t, "/entities", complianceOfficer, officerKey, entityBody)
		e.age(t, agentKey, 2*time.Hour)
		e.age(t, officerKey, 2*time.Hour)

		n, err := sweeper.SweepIdempotencyKeys(t.Context())
		if err != nil || n != 2 {
			t.Fatalf("SweepIdempotencyKeys() = %d, %v; want both users' keys deleted", n, err)
		}
	})

	t.Run("only the system may delete other users' keys", func(t *testing.T) {
		key := uuid.NewString()
		e.post(t, "/entities", supportAgent, key, entityBody)
		e.age(t, key, 2*time.Hour)
		// A compliance officer's session (not the key's owner) deletes nothing.
		if n := asRole(t, e.pg.AppURL, "compliance_officer",
			"WITH d AS (DELETE FROM idempotency_keys WHERE key = $1 RETURNING 1) SELECT count(*) FROM d", key); n != 0 {
			t.Errorf("a compliance officer deleted %d of another user's keys", n)
		}
		if e.queryInt(t, "SELECT count(*) FROM idempotency_keys WHERE key = $1", key) != 1 {
			t.Error("the key is gone")
		}
	})
}
