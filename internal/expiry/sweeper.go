// Package expiry turns abandoned Upload Intents into Expired Uploads.
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

// Sweeper periodically expires lapsed Upload Intents.
type Sweeper struct {
	store    *db.Store
	log      *slog.Logger
	interval time.Duration
}

func NewSweeper(store *db.Store, log *slog.Logger, interval time.Duration) *Sweeper {
	return &Sweeper{store: store, log: log, interval: interval}
}

// Sweeping is automated work, attributed to the System.
var systemActor = kyc.Actor{UserID: kyc.SystemUserID, Role: kyc.RoleSystem}

// Run sweeps every interval until ctx is cancelled.
func (s *Sweeper) Run(ctx context.Context) {
	s.log.InfoContext(ctx, "upload expiry sweeper started", slog.Duration("interval", s.interval))
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		if _, err := s.Sweep(ctx); err != nil && ctx.Err() == nil {
			s.log.ErrorContext(ctx, "upload expiry sweep", slog.String("error", err.Error()))
		}
		select {
		case <-ctx.Done():
			s.log.Info("upload expiry sweeper stopped")
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
