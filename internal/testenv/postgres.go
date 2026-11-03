//go:build integration

// Package testenv starts real infrastructure for integration tests.
package testenv

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/bfischetti/kyc-lifecycle/internal/db"
)

const (
	ownerPassword = "owner-test"
	appPassword   = "app-test"
	dbName        = "kyc"
)

// Postgres holds connection strings for a migrated, throwaway database.
type Postgres struct {
	OwnerURL string // kyc_owner: owns the tables; used for migrations
	AppURL   string // kyc_app: the runtime role; what the server uses
	AdminURL string // superuser; for inspecting catalog state in tests
}

func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// StartPostgres starts Postgres provisioned by the same role script as
// docker compose, applies migrations as the owner, and terminates the
// container when the test ends.
func StartPostgres(t testing.TB) Postgres {
	t.Helper()
	ctx := context.Background()

	ctr, err := postgres.Run(ctx, "postgres:17-alpine",
		postgres.WithUsername("postgres"),
		postgres.WithPassword("postgres"),
		postgres.WithDatabase("postgres"),
		postgres.WithInitScripts(filepath.Join(repoRoot(), "deploy", "postgres", "init-roles.sh")),
		testcontainers.WithEnv(map[string]string{
			"KYC_OWNER_PASSWORD": ownerPassword,
			"KYC_APP_PASSWORD":   appPassword,
			"KYC_DB_NAME":        dbName,
		}),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(60*time.Second),
		),
	)
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}

	host, err := ctr.Host(ctx)
	if err != nil {
		t.Fatalf("postgres host: %v", err)
	}
	port, err := ctr.MappedPort(ctx, "5432/tcp")
	if err != nil {
		t.Fatalf("postgres port: %v", err)
	}
	url := func(user, pw, database string) string {
		return fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable", user, pw, host, port.Port(), database)
	}
	pg := Postgres{
		OwnerURL: url("kyc_owner", ownerPassword, dbName),
		AppURL:   url("kyc_app", appPassword, dbName),
		AdminURL: url("postgres", "postgres", dbName),
	}
	if err := db.Migrate(ctx, pg.OwnerURL); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return pg
}
