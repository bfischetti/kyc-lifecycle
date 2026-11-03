//go:build integration

package expiry_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/bfischetti/kyc-lifecycle/internal/testenv"
)

// queryNamed returns the SQL of the sqlc query with the given name, read
// from the source the generated code is built from, so the test plans
// exactly what the sweeper runs.
func queryNamed(t *testing.T, file, name string) string {
	t.Helper()
	_, here, _, _ := runtime.Caller(0)
	src, err := os.ReadFile(filepath.Join(filepath.Dir(here), "..", "db", "queries", file))
	if err != nil {
		t.Fatal(err)
	}
	_, rest, ok := strings.Cut(string(src), "-- name: "+name+" ")
	if !ok {
		t.Fatalf("query %s not found in %s", name, file)
	}
	rest, _, _ = strings.Cut(rest, "-- name: ")
	var lines []string
	for _, l := range strings.Split(rest, "\n")[1:] { // drop the rest of the name line
		if !strings.HasPrefix(strings.TrimSpace(l), "--") {
			lines = append(lines, l)
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func plan(t *testing.T, tx pgx.Tx, sql string) string {
	t.Helper()
	rows, err := tx.Query(context.Background(), "EXPLAIN "+sql)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func TestSweepUsesTheAwaitingUploadIndex(t *testing.T) {
	pg := testenv.StartPostgres(t)
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, pg.AdminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close(ctx) }()

	// Many completed documents, a few open intents: the shape of a real
	// table, where open intents are a tiny fraction.
	if _, err := admin.Exec(ctx, `
		WITH e AS (INSERT INTO entities (type, legal_name, country) VALUES ('individual', 'Ada Lovelace', 'GB') RETURNING id)
		INSERT INTO documents (entity_id, doc_type, content_type, size_bytes, sha256, vault_key,
		                       upload_status, upload_expires_at, uploaded_at)
		SELECT e.id, 'passport', 'application/pdf', 1024, repeat('a', 64), 'documents/' || gen_random_uuid(),
		       CASE WHEN g <= 10 THEN 'awaiting_upload'::upload_status ELSE 'uploaded' END,
		       now() - interval '1 hour',
		       CASE WHEN g <= 10 THEN NULL ELSE now() - interval '2 hours' END
		FROM e, generate_series(1, 50010) AS g;
		ANALYZE documents;`); err != nil {
		t.Fatal(err)
	}

	sweep := queryNamed(t, "documents.sql", "ExpireLapsedUploads")
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	withIndex := plan(t, tx, sweep)
	if !strings.Contains(withIndex, "documents_awaiting_upload_idx") || strings.Contains(withIndex, "Seq Scan") {
		t.Errorf("sweep does not use the index:\n%s", withIndex)
	}

	// Without the index the same query scans the table, so the assertion
	// above is really about the index and not about the planner's mood.
	if _, err := tx.Exec(ctx, "DROP INDEX documents_awaiting_upload_idx"); err != nil {
		t.Fatal(err)
	}
	if without := plan(t, tx, sweep); !strings.Contains(without, "Seq Scan") {
		t.Errorf("without the index the plan was expected to be a sequential scan:\n%s", without)
	}
}
