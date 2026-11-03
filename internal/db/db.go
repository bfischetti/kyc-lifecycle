// Package db owns Postgres access: the connection pool, migrations, and the
// transaction helper that scopes the caller's identity to each transaction.
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver for goose
	"github.com/pressly/goose/v3"

	"github.com/bfischetti/kyc-lifecycle/internal/db/dbgen"
	"github.com/bfischetti/kyc-lifecycle/internal/kyc"
	"github.com/bfischetti/kyc-lifecycle/migrations"
)

// Store is the runtime database handle. It must be opened with the
// non-owner application role so row-level security applies (ADR-0001).
type Store struct {
	pool *pgxpool.Pool
}

// Open connects to Postgres and verifies the connection.
func Open(ctx context.Context, url string) (*Store, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return &Store{pool: pool}, nil
}

// NewStore wraps an existing pool.
func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func (s *Store) Close() { s.pool.Close() }

// Ping checks the database is reachable.
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// Queries runs outside any actor-scoped transaction. Use only for lookups
// that happen before the caller is known (authentication).
func (s *Store) Queries() *dbgen.Queries { return dbgen.New(s.pool) }

// InTx runs fn in a transaction with the actor's identity set as
// transaction-local settings (app.user_id, app.user_role). Being
// transaction-local, they are discarded at commit or rollback and cannot leak
// to the next user of a pooled connection.
func (s *Store) InTx(ctx context.Context, actor kyc.Actor, fn func(q *dbgen.Queries) error) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`SELECT set_config('app.user_id', $1, true), set_config('app.user_role', $2, true)`,
			actor.UserID.String(), string(actor.Role),
		); err != nil {
			return fmt.Errorf("set actor: %w", err)
		}
		return fn(dbgen.New(tx))
	})
}

// OutboxBacklog counts undelivered notifications: pending and dead. The
// outbox has no row-level security, so no actor is needed.
func (s *Store) OutboxBacklog(ctx context.Context) (pending, dead int64, err error) {
	b, err := dbgen.New(s.pool).OutboxBacklog(ctx)
	return b.Pending, b.Dead, err
}

// IsNoRows reports whether err means a query matched no rows.
func IsNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

// Migrate applies all pending migrations. It must be called with the owner
// role's connection string, never the application role's.
func Migrate(ctx context.Context, url string) error {
	sqlDB, err := sql.Open("pgx", url)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer func() { _ = sqlDB.Close() }()

	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.FS)
	if err != nil {
		return fmt.Errorf("goose: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("migrate up: %w", err)
	}
	return nil
}
