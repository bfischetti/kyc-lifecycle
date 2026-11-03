// Package expiry turns abandoned Upload Intents into Expired Uploads and
// deletes idempotency keys past their retention window.
//
// The sweep is housekeeping, not enforcement: completing an intent after its
// upload permission lapsed is refused whether or not the sweep has run yet.
// Sweeping makes the state visible (an Expired Upload in the Entity's
// documents) instead of leaving intents waiting forever.
package expiry

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/bfischetti/kyc-lifecycle/internal/db"
	"github.com/bfischetti/kyc-lifecycle/internal/db/dbgen"
	"github.com/bfischetti/kyc-lifecycle/internal/kyc"
)

// DefaultInterval is how often the sweeper runs unless configured.
const DefaultInterval = time.Minute

// DefaultKeyRetention is how long idempotency keys are kept unless configured.
const DefaultKeyRetention = 24 * time.Hour

// Sweeper periodically expires lapsed Upload Intents and deletes
// idempotency keys older than their retention window.
type Sweeper struct {
	store        *db.Store
	log          *slog.Logger
	interval     time.Duration
	keyRetention time.Duration
}

// Option configures a Sweeper.
type Option func(*Sweeper)

// WithKeyRetention sets how long idempotency keys are kept.
func WithKeyRetention(d time.Duration) Option {
	return func(s *Sweeper) { s.keyRetention = d }
}

func NewSweeper(store *db.Store, log *slog.Logger, interval time.Duration, opts ...Option) *Sweeper {
	s := &Sweeper{store: store, log: log, interval: interval, keyRetention: DefaultKeyRetention}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Sweeping is automated work, attributed to the System.
var systemActor = kyc.Actor{UserID: kyc.SystemUserID, Role: kyc.RoleSystem}

// Run sweeps every interval until ctx is cancelled.
func (s *Sweeper) Run(ctx context.Context) {
	s.log.InfoContext(ctx, "expiry sweeper started",
		slog.Duration("interval", s.interval), slog.Duration("idempotency_key_retention", s.keyRetention))
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		if _, err := s.Sweep(ctx); err != nil && ctx.Err() == nil {
			s.log.ErrorContext(ctx, "upload expiry sweep", slog.String("error", err.Error()))
		}
		if _, err := s.SweepIdempotencyKeys(ctx); err != nil && ctx.Err() == nil {
			s.log.ErrorContext(ctx, "idempotency key sweep", slog.String("error", err.Error()))
		}
		select {
		case <-ctx.Done():
			s.log.Info("expiry sweeper stopped")
			return
		case <-t.C:
		}
	}
}

// Sweep expires every lapsed Upload Intent and returns how many it expired.
// The Entity is unaffected: an Upload Intent never changed it.
func (s *Sweeper) Sweep(ctx context.Context) (int64, error) {
	var n int64
	err := s.store.InTx(ctx, systemActor, func(q *dbgen.Queries) error {
		var err error
		n, err = q.ExpireLapsedUploads(ctx)
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("expire lapsed uploads: %w", err)
	}
	if n > 0 {
		s.log.InfoContext(ctx, "upload intents expired", slog.Int64("count", n))
	}
	return n, nil
}

// SweepIdempotencyKeys deletes idempotency keys first used longer ago than
// the retention window, whoever they belong to, and returns how many. A
// request reusing a deleted key is treated as new.
func (s *Sweeper) SweepIdempotencyKeys(ctx context.Context) (int64, error) {
	var n int64
	err := s.store.InTx(ctx, systemActor, func(q *dbgen.Queries) error {
		var err error
		n, err = q.DeleteIdempotencyKeysOlderThan(ctx, s.keyRetention.Seconds())
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("delete old idempotency keys: %w", err)
	}
	if n > 0 {
		s.log.InfoContext(ctx, "idempotency keys deleted", slog.Int64("count", n))
	}
	return n, nil
}
