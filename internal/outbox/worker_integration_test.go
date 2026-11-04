//go:build integration

package outbox_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/bfischetti/kyc-lifecycle/internal/db"
	"github.com/bfischetti/kyc-lifecycle/internal/outbox"
	"github.com/bfischetti/kyc-lifecycle/internal/testenv"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// fastOptions keep tests quick while preserving the retry shape.
var fastOptions = outbox.Options{
	PollInterval:   10 * time.Millisecond,
	MaxAttempts:    10,
	BaseBackoff:    50 * time.Millisecond,
	MaxBackoff:     time.Second,
	PublishTimeout: time.Second,
}

type harness struct {
	pg    testenv.Postgres
	store *db.Store
	admin *pgx.Conn
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	pg := testenv.StartPostgres(t)
	store, err := db.Open(context.Background(), pg.AppURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	admin, err := pgx.Connect(context.Background(), pg.AdminURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	return &harness{pg: pg, store: store, admin: admin}
}

// enqueue writes n notifications for a fresh entity, as a status change would.
func (h *harness) enqueue(t *testing.T, n int) []uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var entityID uuid.UUID
	if err := h.admin.QueryRow(ctx,
		`INSERT INTO entities (type, legal_name, country) VALUES ('business', 'Acme', 'GB') RETURNING id`).Scan(&entityID); err != nil {
		t.Fatal(err)
	}
	ids := make([]uuid.UUID, n)
	for i := range ids {
		ids[i] = uuid.New()
		payload, _ := json.Marshal(map[string]any{
			"event_id": ids[i], "entity_id": entityID, "entity_version": i + 1,
			"from": "unverified", "to": "pending_review",
			"changed_by": "00000000-0000-0000-0000-000000000002", "occurred_at": time.Now(),
		})
		if _, err := h.admin.Exec(ctx,
			`INSERT INTO outbox (id, entity_id, event_type, payload) VALUES ($1, $2, 'entity.status_changed', $3)`,
			ids[i], entityID, payload); err != nil {
			t.Fatal(err)
		}
	}
	return ids
}

func (h *harness) str(t *testing.T, sql string, args ...any) string {
	t.Helper()
	var s string
	if err := h.admin.QueryRow(context.Background(), sql, args...).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

// recorder is a Publisher that records calls and can be told to fail.
type recorder struct {
	mu       sync.Mutex
	calls    []call
	inFlight map[uuid.UUID]int
	overlaps int
	failN    map[uuid.UUID]int // fail the first N attempts per id; -1 = always
	slow     time.Duration
}

type call struct {
	msg outbox.Message
	at  time.Time
}

func newRecorder() *recorder {
	return &recorder{inFlight: map[uuid.UUID]int{}, failN: map[uuid.UUID]int{}}
}

func (r *recorder) Publish(ctx context.Context, m outbox.Message) error {
	r.mu.Lock()
	r.calls = append(r.calls, call{m, time.Now()})
	r.inFlight[m.ID]++
	if r.inFlight[m.ID] > 1 {
		r.overlaps++
	}
	attempt := 0
	for _, c := range r.calls {
		if c.msg.ID == m.ID {
			attempt++
		}
	}
	fail := r.failN[m.ID] == -1 || attempt <= r.failN[m.ID]
	slow := r.slow
	r.mu.Unlock()

	time.Sleep(slow)

	r.mu.Lock()
	r.inFlight[m.ID]--
	r.mu.Unlock()
	if fail {
		return errors.New("consumer unavailable")
	}
	return nil
}

func (r *recorder) callsFor(id uuid.UUID) []call {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []call
	for _, c := range r.calls {
		if c.msg.ID == id {
			out = append(out, c)
		}
	}
	return out
}

func eventually(t *testing.T, within time.Duration, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func run(t *testing.T, h *harness, pub outbox.Publisher, opts outbox.Options) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { outbox.NewWorker(h.store, pub, quiet, opts).Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return cancel
}

func TestDelivery(t *testing.T) {
	h := newHarness(t)

	t.Run("a pending notification is published and marked delivered", func(t *testing.T) {
		id := h.enqueue(t, 1)[0]
		rec := newRecorder()
		cancel := run(t, h, rec, fastOptions)
		defer cancel()

		eventually(t, 3*time.Second, func() bool {
			return h.str(t, "SELECT (published_at IS NOT NULL)::text FROM outbox WHERE id = $1", id) == "true"
		}, "published_at set")

		calls := rec.callsFor(id)
		if len(calls) != 1 {
			t.Fatalf("published %d times, want 1", len(calls))
		}
		var ev map[string]any
		if err := json.Unmarshal(calls[0].msg.Payload, &ev); err != nil {
			t.Fatal(err)
		}
		if calls[0].msg.Type != "entity.status_changed" || ev["event_id"] != id.String() {
			t.Errorf("message = %s %v", calls[0].msg.Type, ev)
		}
		allowed := map[string]bool{"event_id": true, "entity_id": true, "entity_version": true, "from": true,
			"to": true, "changed_by": true, "occurred_at": true}
		for k := range ev {
			if !allowed[k] {
				t.Errorf("published notification has unexpected field %q", k)
			}
		}
	})

	t.Run("failures are retried with growing delays, then delivered once", func(t *testing.T) {
		id := h.enqueue(t, 1)[0]
		rec := newRecorder()
		rec.failN[id] = 2
		cancel := run(t, h, rec, fastOptions)
		defer cancel()

		eventually(t, 5*time.Second, func() bool {
			return h.str(t, "SELECT (published_at IS NOT NULL)::text FROM outbox WHERE id = $1", id) == "true"
		}, "delivered after retries")

		calls := rec.callsFor(id)
		if len(calls) != 3 {
			t.Fatalf("attempts = %d, want 3", len(calls))
		}
		first, second := calls[1].at.Sub(calls[0].at), calls[2].at.Sub(calls[1].at)
		if first < fastOptions.BaseBackoff || second <= first {
			t.Errorf("delays %v then %v; want ≥ %v and increasing", first, second, fastOptions.BaseBackoff)
		}
		if s := h.str(t, "SELECT attempts::text || ':' || coalesce(last_error, '') FROM outbox WHERE id = $1", id); s != "3:" {
			t.Errorf("attempts:last_error = %q, want \"3:\"", s)
		}
	})

	t.Run("a notification that always fails is dead-lettered and not retried again", func(t *testing.T) {
		id := h.enqueue(t, 1)[0]
		rec := newRecorder()
		rec.failN[id] = -1
		opts := fastOptions
		opts.MaxAttempts = 3
		opts.BaseBackoff = 10 * time.Millisecond
		cancel := run(t, h, rec, opts)
		defer cancel()

		eventually(t, 5*time.Second, func() bool {
			return h.str(t, "SELECT (dead_at IS NOT NULL)::text FROM outbox WHERE id = $1", id) == "true"
		}, "dead_at set")
		time.Sleep(300 * time.Millisecond) // give it the chance to misbehave

		if n := len(rec.callsFor(id)); n != 3 {
			t.Errorf("attempts = %d, want exactly 3", n)
		}
		if s := h.str(t, "SELECT (published_at IS NULL)::text || ':' || attempts || ':' || last_error FROM outbox WHERE id = $1", id); s != "true:3:consumer unavailable" {
			t.Errorf("row = %q", s)
		}
	})
}

func TestTwoWorkersNeverPublishTheSameNotificationAtOnce(t *testing.T) {
	h := newHarness(t)
	ids := h.enqueue(t, 100)

	rec := newRecorder()
	rec.slow = 2 * time.Millisecond // widen the window for overlaps
	run(t, h, rec, fastOptions)
	run(t, h, rec, fastOptions)

	eventually(t, 20*time.Second, func() bool {
		return h.str(t, "SELECT count(*)::text FROM outbox WHERE published_at IS NULL") == "0"
	}, "all delivered")

	rec.mu.Lock()
	overlaps := rec.overlaps
	rec.mu.Unlock()
	if overlaps != 0 {
		t.Errorf("%d concurrent publishes of the same notification", overlaps)
	}
	for _, id := range ids {
		if n := len(rec.callsFor(id)); n != 1 {
			t.Errorf("notification %s published %d times, want 1", id, n)
		}
	}
}

func TestWorkerActsAsSystem(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	// Record the caller role seen by the database whenever an outbox row is updated.
	if _, err := h.admin.Exec(ctx, `
		CREATE TABLE seen_roles (role text);
		CREATE FUNCTION record_role() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER AS
			$$ BEGIN INSERT INTO seen_roles VALUES (current_setting('app.user_role', true)); RETURN NEW; END $$;
		CREATE TRIGGER record_role BEFORE UPDATE ON outbox FOR EACH ROW EXECUTE FUNCTION record_role();`); err != nil {
		t.Fatal(err)
	}
	id := h.enqueue(t, 1)[0]
	run(t, h, newRecorder(), fastOptions)

	eventually(t, 3*time.Second, func() bool {
		return h.str(t, "SELECT (published_at IS NOT NULL)::text FROM outbox WHERE id = $1", id) == "true"
	}, "delivered")
	if s := h.str(t, "SELECT string_agg(DISTINCT role, ',') FROM seen_roles"); s != "system" {
		t.Errorf("worker database role = %q, want system", s)
	}
}
