//go:build integration

package httpapi_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bfischetti/kyc-lifecycle/internal/db"
	"github.com/bfischetti/kyc-lifecycle/internal/db/dbgen"
	"github.com/bfischetti/kyc-lifecycle/internal/kyc"
)

func TestRedactedView(t *testing.T) {
	e := setup(t)
	id := e.pendingEntity(t, "individual", "Minister PEP")
	e.runCheck(t, complianceOfficer, id, "pep_screening") // manual_review with a hit in the payload

	_, full := e.do(t, "GET", "/entities/"+id, complianceOfficer, "")
	resp, redacted := e.do(t, "GET", "/entities/"+id, supportAgent, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("support agent: status %d", resp.StatusCode)
	}

	t.Run("compliance officers see documents and checks with raw payloads", func(t *testing.T) {
		docs, checks := full["documents"].([]any), full["checks"].([]any)
		if len(docs) != 1 || len(checks) != 1 {
			t.Fatalf("documents %d, checks %d; want 1 each", len(docs), len(checks))
		}
		d := docs[0].(map[string]any)
		for _, k := range []string{"doc_type", "expiry_date", "upload_status", "review_status", "size_bytes", "content_type", "sha256"} {
			if _, ok := d[k]; !ok {
				t.Errorf("document lacks %q: %v", k, d)
			}
		}
		c := checks[0].(map[string]any)
		raw, ok := c["raw_response"].(map[string]any)
		if !ok || raw["match_status"] != "potential_match" {
			t.Errorf("check raw_response = %v", c["raw_response"])
		}
	})

	t.Run("support agents get the same shape without payloads or vault keys", func(t *testing.T) {
		docs, checks := redacted["documents"].([]any), redacted["checks"].([]any)
		if len(docs) != 1 || len(checks) != 1 {
			t.Fatalf("documents %d, checks %d; want 1 each", len(docs), len(checks))
		}
		if _, ok := checks[0].(map[string]any)["raw_response"]; ok {
			t.Errorf("support agent sees raw_response: %v", checks[0])
		}
		if checks[0].(map[string]any)["result"] != "manual_review" {
			t.Errorf("support agent should still see the result: %v", checks[0])
		}
		key := e.queryString(t, "SELECT vault_key FROM documents WHERE entity_id = $1", id)
		if strings.Contains(string(resp.Raw), strings.TrimPrefix(key, "documents/")) {
			t.Error("redacted view contains the vault key")
		}
		if strings.Contains(string(resp.Raw), "potential_match") || strings.Contains(string(resp.Raw), "PEP Class") {
			t.Errorf("payload content leaked into the redacted view: %s", resp.Raw)
		}
	})
}

// asRole runs a raw query as the runtime login (kyc_app) with the given
// caller role set for the transaction, bypassing the application entirely.
func asRole(t *testing.T, appURL, role, query string, args ...any) int {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if role != "" {
		if _, err := tx.Exec(ctx, "SELECT set_config('app.user_role', $1, true)", role); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := tx.QueryRow(ctx, query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRLSBackstop(t *testing.T) {
	e := setup(t)
	id := e.pendingEntity(t, "individual", "Ada Lovelace")
	e.runCheck(t, complianceOfficer, id, "sanctions_screening")

	protected := map[string]string{
		"raw provider payloads": "SELECT count(*) FROM check_payloads",
		"audit trail":           "SELECT count(*) FROM status_history",
	}

	t.Run("support agents' database sessions cannot read protected rows", func(t *testing.T) {
		for name, q := range protected {
			if n := asRole(t, e.pg.AppURL, "support_agent", q); n != 0 {
				t.Errorf("%s visible to support_agent: %d rows", name, n)
			}
			for _, role := range []string{"compliance_officer", "system"} {
				if n := asRole(t, e.pg.AppURL, role, q); n == 0 {
					t.Errorf("%s invisible to %s; the test would prove nothing", name, role)
				}
			}
		}
	})

	t.Run("with no caller role set, nothing is visible", func(t *testing.T) {
		for name, q := range protected {
			if n := asRole(t, e.pg.AppURL, "", q); n != 0 {
				t.Errorf("%s visible with no role: %d rows", name, n)
			}
		}
	})

	t.Run("a support agent cannot write raw payloads either", func(t *testing.T) {
		ctx := context.Background()
		conn, err := pgx.Connect(ctx, e.pg.AppURL)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close(ctx) }()
		checkID := e.queryString(t, "SELECT id::text FROM checks WHERE entity_id = $1", id)
		e.adminExec(t, "DELETE FROM check_payloads WHERE check_id = '"+checkID+"'")
		tx, _ := conn.Begin(ctx)
		defer func() { _ = tx.Rollback(ctx) }()
		_, _ = tx.Exec(ctx, "SELECT set_config('app.user_role', 'support_agent', true)")
		_, err = tx.Exec(ctx, "INSERT INTO check_payloads (check_id, raw_response) VALUES ($1, '{}')", checkID)
		if err == nil || !strings.Contains(err.Error(), "row-level security") {
			t.Errorf("insert as support_agent: err = %v, want an RLS violation", err)
		}
	})

	t.Run("row-level security is forced, so it binds the table owner too", func(t *testing.T) {
		for _, table := range []string{"check_payloads", "status_history"} {
			s := e.queryString(t, "SELECT relrowsecurity::text || ',' || relforcerowsecurity::text FROM pg_class WHERE relname = $1", table)
			if s != "true,true" {
				t.Errorf("%s: rls,force = %s, want true,true", table, s)
			}
		}
		// And it really does bind the owner: kyc_owner with no role sees nothing.
		if n := asRole(t, e.pg.OwnerURL, "", "SELECT count(*) FROM status_history"); n != 0 {
			t.Errorf("owner bypassed RLS: %d rows", n)
		}
	})
}

func TestCallerRoleDoesNotLeakAcrossPooledConnections(t *testing.T) {
	e := setup(t)
	entityID := uuid.MustParse(e.pendingEntity(t, "individual", "Ada Lovelace")) // creates an audit record

	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(e.pg.AppURL)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1 // both units of work must share the one connection
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := db.NewStore(pool)

	// First unit of work: a compliance officer's transaction, which can see history.
	officer := kyc.Actor{UserID: uuid.MustParse(complianceOfficer), Role: kyc.RoleComplianceOfficer}
	var seen int
	if err := store.InTx(ctx, officer, func(q *dbgen.Queries) error {
		rows, err := q.ListStatusHistory(ctx, entityID)
		seen = len(rows)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if seen == 0 {
		t.Fatal("officer saw no history; the test would prove nothing")
	}

	// Next unit of work on the same connection, with no actor set.
	var role string
	if err := pool.QueryRow(ctx, "SELECT coalesce(current_setting('app.user_role', true), '')").Scan(&role); err != nil {
		t.Fatal(err)
	}
	if role != "" {
		t.Errorf("app.user_role leaked to the next unit of work: %q", role)
	}
	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM status_history").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("status_history visible after the officer's transaction ended: %d rows", n)
	}
}
