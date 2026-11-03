//go:build integration

package httpapi_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func withEvidenceTTL(d time.Duration) option {
	return func(c *config) { c.opts.EvidenceURLTTL = d }
}

// openContent requests a Document's contents without following the redirect.
func (e *env) openContent(t *testing.T, user, entityID, docID string) result {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
		e.srv.URL+"/entities/"+entityID+"/documents/"+docID+"/content", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-User-ID", user)
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noFollow.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return result{StatusCode: resp.StatusCode, Header: resp.Header, Raw: raw}
}

// fetch GETs a URL as a plain client would after the redirect: no user header.
func fetch(t *testing.T, url string) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, body
}

func (e *env) accessCount(t *testing.T, docID string) int {
	t.Helper()
	return e.queryInt(t, "SELECT count(*) FROM evidence_access WHERE document_id = $1", docID)
}

// completedPDF returns an Entity and a completed passport holding samplePDF('a').
func (e *env) completedPDF(t *testing.T) (entityID, docID string) {
	t.Helper()
	entityID = e.createEntity(t, "individual", "Ada Lovelace")
	body := e.uploadedDocument(t, entityID)
	return entityID, body["document"].(map[string]any)["id"].(string)
}

func TestEvidenceAccess(t *testing.T) {
	e := setup(t)

	t.Run("a compliance officer is redirected to exactly the uploaded bytes", func(t *testing.T) {
		entityID, docID := e.completedPDF(t)
		resp := e.openContent(t, complianceOfficer, entityID, docID)
		if resp.StatusCode != http.StatusFound {
			t.Fatalf("status %d, want 302", resp.StatusCode)
		}
		if cc, rp := resp.Header.Get("Cache-Control"), resp.Header.Get("Referrer-Policy"); cc != "no-store" || rp != "no-referrer" {
			t.Errorf("Cache-Control %q, Referrer-Policy %q; want no-store, no-referrer", cc, rp)
		}

		status, hdr, got := fetch(t, resp.Header.Get("Location"))
		if status != http.StatusOK || !bytes.Equal(got, samplePDF('a')) {
			t.Fatalf("download: status %d, %d bytes; want 200 and the uploaded %d bytes", status, len(got), len(samplePDF('a')))
		}
		if cd := hdr.Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment") || !strings.Contains(cd, docID+".pdf") {
			t.Errorf("Content-Disposition = %q, want an attachment named after the document id", cd)
		}
		if strings.Contains(e.logs.String(), resp.Header.Get("Location")) {
			t.Error("the download URL appears in the service's logs")
		}
	})

	t.Run("each access is recorded with the officer, document and time", func(t *testing.T) {
		entityID, docID := e.completedPDF(t)
		before := time.Now().Add(-time.Second)
		e.openContent(t, complianceOfficer, entityID, docID)
		e.openContent(t, complianceOfficer, entityID, docID)

		if n := e.accessCount(t, docID); n != 2 {
			t.Fatalf("access records = %d, want 2", n)
		}
		if n := e.queryInt(t, "SELECT count(*) FROM evidence_access WHERE document_id = $1 AND entity_id = $2 AND accessed_by = $3 AND accessed_at > $4",
			docID, entityID, complianceOfficer, before); n != 2 {
			t.Errorf("records naming the officer, entity and a current time = %d, want 2", n)
		}
	})

	t.Run("support agents and the system are refused, and nothing is recorded", func(t *testing.T) {
		entityID, docID := e.completedPDF(t)
		for _, user := range []string{supportAgent, systemUser} {
			if resp := e.openContent(t, user, entityID, docID); resp.StatusCode != http.StatusForbidden || resp.Header.Get("Location") != "" {
				t.Errorf("user %s: status %d, Location %q; want 403 and no URL", user, resp.StatusCode, resp.Header.Get("Location"))
			}
		}
		if n := e.accessCount(t, docID); n != 0 {
			t.Errorf("access records = %d, want 0", n)
		}
	})

	t.Run("an incomplete document is 409 and an unknown one is 404", func(t *testing.T) {
		entityID := e.createEntity(t, "individual", "Ada Lovelace")
		intentID, _ := e.intent(t, entityID, "passport", "application/pdf", samplePDF('a'))
		if resp := e.openContent(t, complianceOfficer, entityID, intentID); resp.StatusCode != http.StatusConflict {
			t.Errorf("upload intent: status %d, want 409", resp.StatusCode)
		}
		if n := e.accessCount(t, intentID); n != 0 {
			t.Errorf("access records for the intent = %d, want 0", n)
		}

		otherEntity, docID := e.completedPDF(t)
		for name, path := range map[string][2]string{
			"unknown document":          {entityID, "6f1c2a8e-0000-4000-8000-000000000000"},
			"another entity's document": {entityID, docID},
			"unknown entity":            {"6f1c2a8e-0000-4000-8000-000000000001", docID},
			"malformed document id":     {otherEntity, "not-a-uuid"},
		} {
			if resp := e.openContent(t, complianceOfficer, path[0], path[1]); resp.StatusCode != http.StatusNotFound {
				t.Errorf("%s: status %d, want 404", name, resp.StatusCode)
			}
		}
	})
}

func TestEvidenceURLExpires(t *testing.T) {
	e := setup(t, withEvidenceTTL(time.Second))
	entityID, docID := e.completedPDF(t)
	location := e.openContent(t, complianceOfficer, entityID, docID).Header.Get("Location")
	if status, _, _ := fetch(t, location); status != http.StatusOK {
		t.Fatalf("fresh URL: status %d, want 200", status)
	}
	time.Sleep(2500 * time.Millisecond)
	if status, _, _ := fetch(t, location); status != http.StatusForbidden {
		t.Errorf("expired URL: status %d, want 403", status)
	}
}

func TestEvidenceAccessLogBackstop(t *testing.T) {
	e := setup(t)
	entityID, docID := e.completedPDF(t)
	e.openContent(t, complianceOfficer, entityID, docID)
	const q = "SELECT count(*) FROM evidence_access"

	t.Run("only compliance can read the log at the database", func(t *testing.T) {
		if n := asRole(t, e.pg.AppURL, "compliance_officer", q); n == 0 {
			t.Fatal("log invisible to compliance; the test would prove nothing")
		}
		for _, role := range []string{"support_agent", "system", ""} {
			if n := asRole(t, e.pg.AppURL, role, q); n != 0 {
				t.Errorf("log visible to role %q: %d rows", role, n)
			}
		}
	})

	// exec runs one statement as kyc_app with the given caller identity.
	exec := func(t *testing.T, role, userID, sql string, args ...any) error {
		t.Helper()
		ctx := context.Background()
		conn, err := pgx.Connect(ctx, e.pg.AppURL)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close(ctx) }()
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, "SELECT set_config('app.user_role', $1, true), set_config('app.user_id', $2, true)", role, userID); err != nil {
			t.Fatal(err)
		}
		_, err = tx.Exec(ctx, sql, args...)
		return err
	}
	insert := "INSERT INTO evidence_access (document_id, entity_id, accessed_by) VALUES ($1, $2, $3)"

	t.Run("a support agent cannot record an access", func(t *testing.T) {
		err := exec(t, "support_agent", supportAgent, insert, docID, entityID, supportAgent)
		if err == nil || !strings.Contains(err.Error(), "row-level security") {
			t.Errorf("err = %v, want an RLS violation", err)
		}
	})

	t.Run("an officer cannot record an access in someone else's name", func(t *testing.T) {
		err := exec(t, "compliance_officer", complianceOfficer, insert, docID, entityID, supportAgent)
		if err == nil || !strings.Contains(err.Error(), "row-level security") {
			t.Errorf("err = %v, want an RLS violation", err)
		}
	})

	t.Run("the log cannot be edited or deleted by the application role", func(t *testing.T) {
		for _, sql := range []string{
			"UPDATE evidence_access SET accessed_at = now() - interval '1 year'",
			"DELETE FROM evidence_access",
		} {
			if err := exec(t, "compliance_officer", complianceOfficer, sql); err == nil || !strings.Contains(err.Error(), "permission denied") {
				t.Errorf("%s: err = %v, want permission denied", sql, err)
			}
		}
	})

	t.Run("row-level security is forced on the log", func(t *testing.T) {
		if s := e.queryString(t, "SELECT relrowsecurity::text || ',' || relforcerowsecurity::text FROM pg_class WHERE relname = 'evidence_access'"); s != "true,true" {
			t.Errorf("rls,force = %s, want true,true", s)
		}
	})
}
