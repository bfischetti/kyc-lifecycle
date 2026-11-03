package outbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bfischetti/kyc-lifecycle/internal/db"
	"github.com/bfischetti/kyc-lifecycle/internal/db/dbgen"
	"github.com/bfischetti/kyc-lifecycle/internal/kyc"
	"github.com/bfischetti/kyc-lifecycle/internal/observability"
)

// Options tunes delivery.
type Options struct {
	PollInterval   time.Duration // how often to look for due notifications
	MaxAttempts    int           // attempts before a notification is marked dead
	BaseBackoff    time.Duration // delay after the first failure; doubles each time
	MaxBackoff     time.Duration // cap on the delay
	PublishTimeout time.Duration // bound on one Publish call
}

// DefaultOptions are the production defaults.
var DefaultOptions = Options{
	PollInterval:   time.Second,
	MaxAttempts:    10,
	BaseBackoff:    time.Second,
	MaxBackoff:     5 * time.Minute,
	PublishTimeout: 10 * time.Second,
}

// Backoff is the delay before the next attempt after `attempts` failures.
func (o Options) Backoff(attempts int) time.Duration {
	d := o.BaseBackoff
	for i := 1; i < attempts && d < o.MaxBackoff; i++ {
		d *= 2
	}
	return min(d, o.MaxBackoff)
}

// Worker polls the outbox and publishes due notifications.
type Worker struct {
	store *db.Store
	pub   Publisher
	log   *slog.Logger
	opts  Options
}

func NewWorker(store *db.Store, pub Publisher, log *slog.Logger, opts Options) *Worker {
	return &Worker{store: store, pub: pub, log: log, opts: opts}
}

// The worker acts as the System, so the RLS backstop applies to it too.
var systemActor = kyc.Actor{UserID: kyc.SystemUserID, Role: kyc.RoleSystem}

// Run delivers until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) {
	w.log.InfoContext(ctx, "outbox worker started")
	t := time.NewTicker(w.opts.PollInterval)
	defer t.Stop()
	for {
		w.Drain(ctx)
		select {
		case <-ctx.Done():
			w.log.Info("outbox worker stopped")
			return
		case <-t.C:
		}
	}
}

// Drain processes due notifications one at a time until none is due.
func (w *Worker) Drain(ctx context.Context) {
	for ctx.Err() == nil {
		claimed, err := w.deliverNext(ctx)
		if err != nil {
			if ctx.Err() == nil {
				w.log.ErrorContext(ctx, "outbox delivery", slog.String("error", err.Error()))
			}
			return
		}
		if !claimed {
			return
		}
	}
}

// deliverNext claims one due notification, publishes it and records the
// outcome, all in one transaction. The row stays locked while publishing,
// so no other worker can publish it concurrently; if the process dies
// before commit, the lock is released and the row is retried later. That
// window is why delivery is at least once rather than exactly once.
func (w *Worker) deliverNext(ctx context.Context) (bool, error) {
	claimed := false
	err := w.store.InTx(ctx, systemActor, func(q *dbgen.Queries) error {
		row, err := q.ClaimNextOutbox(ctx)
		if db.IsNoRows(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("claim: %w", err)
		}
		claimed = true

		lctx := observability.NewScope(ctx,
			slog.String("event_id", row.ID.String()), slog.String("entity_id", row.EntityID.String()))
		pctx, cancel := context.WithTimeout(lctx, w.opts.PublishTimeout)
		pubErr := w.pub.Publish(pctx, Message{ID: row.ID, Type: row.EventType, Payload: row.Payload})
		cancel()

		if pubErr == nil {
			return q.MarkOutboxPublished(ctx, row.ID)
		}
		if errors.Is(pubErr, context.Canceled) && ctx.Err() != nil {
			return ctx.Err() // shutting down: roll back, retry after restart
		}

		attempts := int(row.Attempts) + 1
		dead := attempts >= w.opts.MaxAttempts
		next := time.Now().Add(w.opts.Backoff(attempts))
		if dead {
			w.log.ErrorContext(lctx, "notification dead-lettered",
				slog.Int("attempts", attempts), slog.String("error", pubErr.Error()))
		} else {
			w.log.WarnContext(lctx, "notification delivery failed; will retry",
				slog.Int("attempts", attempts), slog.Time("next_attempt_at", next), slog.String("error", pubErr.Error()))
		}
		return q.MarkOutboxFailed(ctx, dbgen.MarkOutboxFailedParams{
			ID: row.ID, LastError: pgtype.Text{String: pubErr.Error(), Valid: true},
			NextAttemptAt: next, Dead: dead,
		})
	})
	return claimed, err
}
