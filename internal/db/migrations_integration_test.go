//go:build integration

package db_test

import (
	"context"
	"database/sql"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/bfischetti/kyc-lifecycle/internal/db"
	"github.com/bfischetti/kyc-lifecycle/internal/testenv"
	"github.com/bfischetti/kyc-lifecycle/migrations"
)

// Every migration must roll back cleanly and apply again, so a bad
// deploy can be reverted one step at a time.
func TestMigrationsRollBackAndReapply(t *testing.T) {
	pg := testenv.StartPostgres(t) // already migrated to the latest version
	ctx := context.Background()

	sqlDB, err := sql.Open("pgx", pg.OwnerURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sqlDB.Close() }()
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	latest, err := provider.GetDBVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sources := provider.ListSources()
	if want := sources[len(sources)-1].Version; latest != want {
		t.Fatalf("database at version %d, want the latest migration %d", latest, want)
	}

	if _, err := provider.DownTo(ctx, 0); err != nil {
		t.Fatalf("roll back every migration: %v", err)
	}
	var tables int
	if err := sqlDB.QueryRowContext(ctx,
		"SELECT count(*) FROM pg_tables WHERE schemaname = 'public' AND tablename <> 'goose_db_version'").Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 0 {
		t.Errorf("%d tables left after rolling everything back, want 0", tables)
	}

	if err := db.Migrate(ctx, pg.OwnerURL); err != nil {
		t.Fatalf("re-apply: %v", err)
	}
	if v, err := provider.GetDBVersion(ctx); err != nil || v != latest {
		t.Errorf("after re-applying: version %d (%v), want %d", v, err, latest)
	}
}
