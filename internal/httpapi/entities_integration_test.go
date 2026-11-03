//go:build integration

package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/bfischetti/kyc-lifecycle/internal/testenv"
)

func TestWalkingSkeleton(t *testing.T) {
	e := setup(t)

	t.Run("create and read", func(t *testing.T) {
		for _, user := range []string{complianceOfficer, supportAgent} {
			for _, typ := range []string{"individual", "business"} {
				resp, body := e.do(t, "POST", "/entities", user,
					`{"type":"`+typ+`","legal_name":"Acme","country":"GB"}`)
				if resp.StatusCode != http.StatusCreated {
					t.Fatalf("create %s as %s: status %d, body %v", typ, user, resp.StatusCode, body)
				}
				if body["status"] != "unverified" || body["version"] != float64(0) || body["type"] != typ {
					t.Errorf("created entity = %v, want unverified v0 %s", body, typ)
				}
				id := body["id"].(string)
				if loc := resp.Header.Get("Location"); loc != "/entities/"+id {
					t.Errorf("Location = %q", loc)
				}

				resp, got := e.do(t, "GET", "/entities/"+id, user, "")
				if resp.StatusCode != http.StatusOK || got["id"] != id || got["legal_name"] != "Acme" {
					t.Errorf("get: status %d, body %v", resp.StatusCode, got)
				}
			}
		}
	})

	t.Run("invalid input is refused and nothing stored", func(t *testing.T) {
		before := e.queryInt(t, "SELECT count(*) FROM entities")
		for _, body := range []string{
			`{"type":"trust","legal_name":"Acme","country":"GB"}`,
			`{"type":"business","legal_name":"   ","country":"GB"}`,
			`{"type":"business","legal_name":"Acme","country":"UK"}`,
			`{"type":"business","legal_name":"Acme","country":"gb"}`,
			`{"type":"business","legal_name":"Acme","country":"GB","extra":1}`,
			`not json`,
		} {
			resp, out := e.do(t, "POST", "/entities", complianceOfficer, body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("POST %s: status %d, want 400 (%v)", body, resp.StatusCode, out)
			}
		}
		if after := e.queryInt(t, "SELECT count(*) FROM entities"); after != before {
			t.Errorf("entity count changed from %d to %d", before, after)
		}
	})

	t.Run("system user cannot create", func(t *testing.T) {
		resp, _ := e.do(t, "POST", "/entities", systemUser, `{"type":"business","legal_name":"Acme","country":"GB"}`)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("status %d, want 403", resp.StatusCode)
		}
	})

	t.Run("caller identity is required", func(t *testing.T) {
		for name, user := range map[string]string{
			"missing":   "",
			"malformed": "not-a-uuid",
			"unknown":   "11111111-1111-1111-1111-111111111111",
		} {
			resp, _ := e.do(t, "POST", "/entities", user, `{"type":"business","legal_name":"Acme","country":"GB"}`)
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("%s user: status %d, want 401", name, resp.StatusCode)
			}
		}
	})

	t.Run("unknown entity is 404", func(t *testing.T) {
		for _, id := range []string{"11111111-1111-1111-1111-111111111111", "nope"} {
			resp, _ := e.do(t, "GET", "/entities/"+id, complianceOfficer, "")
			if resp.StatusCode != http.StatusNotFound {
				t.Errorf("GET %s: status %d, want 404", id, resp.StatusCode)
			}
		}
	})

	t.Run("request ids are echoed or generated and on every log line", func(t *testing.T) {
		resp, _ := e.do(t, "GET", "/entities/11111111-1111-1111-1111-111111111111", complianceOfficer, "", "X-Request-ID", "client-req-42")
		if got := resp.Header.Get("X-Request-ID"); got != "client-req-42" {
			t.Errorf("echoed request id = %q", got)
		}
		resp, _ = e.do(t, "GET", "/entities/11111111-1111-1111-1111-111111111111", complianceOfficer, "", "X-Request-ID", "<bad id with spaces>")
		if got := resp.Header.Get("X-Request-ID"); got == "" || strings.ContainsAny(got, " <>") {
			t.Errorf("unsafe request id not replaced: %q", got)
		}
		resp, _ = e.do(t, "GET", "/entities/11111111-1111-1111-1111-111111111111", complianceOfficer, "")
		if resp.Header.Get("X-Request-ID") == "" {
			t.Error("no request id generated")
		}

		var sawEntityOnAccessLog bool
		for _, line := range e.logs.Lines() {
			var rec map[string]any
			if err := json.Unmarshal([]byte(line), &rec); err != nil {
				t.Fatalf("log line is not JSON: %q", line)
			}
			if rec["request_id"] == nil || rec["request_id"] == "" {
				t.Errorf("log line without request_id: %s", line)
			}
			if rec["msg"] == "http request" && rec["request_id"] == "client-req-42" && rec["entity_id"] != nil {
				sawEntityOnAccessLog = true
			}
		}
		if !sawEntityOnAccessLog {
			t.Error("access log line for an entity request lacks entity_id")
		}
	})
}

func TestRuntimeRoleCannotBypassRLS(t *testing.T) {
	pg := testenv.StartPostgres(t)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, pg.AdminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()

	var super, bypass bool
	if err := conn.QueryRow(ctx,
		`SELECT rolsuper, rolbypassrls FROM pg_roles WHERE rolname = 'kyc_app'`).Scan(&super, &bypass); err != nil {
		t.Fatal(err)
	}
	if super || bypass {
		t.Errorf("kyc_app superuser=%v bypassrls=%v, want both false", super, bypass)
	}

	rows, err := conn.Query(ctx,
		`SELECT tablename, tableowner FROM pg_tables WHERE schemaname = 'public'`)
	if err != nil {
		t.Fatal(err)
	}
	tables, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) ([2]string, error) {
		var v [2]string
		return v, r.Scan(&v[0], &v[1])
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(tables) == 0 {
		t.Fatal("no tables found")
	}
	for _, tb := range tables {
		if tb[1] != "kyc_owner" {
			t.Errorf("table %s owned by %s, want kyc_owner", tb[0], tb[1])
		}
	}

	seeded := map[string]string{}
	rows, err = conn.Query(ctx, `SELECT id::text, role::text FROM users`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, role string
		if err := rows.Scan(&id, &role); err != nil {
			t.Fatal(err)
		}
		seeded[id] = role
	}
	want := map[string]string{
		complianceOfficer: "compliance_officer",
		supportAgent:      "support_agent",
		systemUser:        "system",
	}
	for id, role := range want {
		if seeded[id] != role {
			t.Errorf("seeded user %s role = %q, want %q", id, seeded[id], role)
		}
	}
}
