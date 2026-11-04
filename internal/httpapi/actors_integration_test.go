//go:build integration

package httpapi_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// uiPost sends a form or JSON body to the UI as the given person, without
// following redirects.
func (e *env) uiPost(t *testing.T, user, path, contentType, body string) result {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), "POST", e.srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", contentType)
	req.AddCookie(&http.Cookie{Name: "kyc_ui_user", Value: user})
	client := http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return result{StatusCode: resp.StatusCode, Header: resp.Header, Raw: raw}
}

func (e *env) assertActors(t *testing.T, entityID, user string) {
	t.Helper()
	if got := e.queryString(t, `SELECT created_by::text FROM entities WHERE id = $1`, entityID); got != user {
		t.Errorf("created_by = %s, want %s", got, user)
	}
	if got := e.queryString(t, `SELECT declared_by::text FROM documents WHERE entity_id = $1`, entityID); got != user {
		t.Errorf("declared_by = %s, want %s", got, user)
	}
	if got := e.queryString(t, `SELECT completed_by::text FROM documents WHERE entity_id = $1`, entityID); got != user {
		t.Errorf("completed_by = %s, want %s", got, user)
	}
}

func TestActorsAreRecorded(t *testing.T) {
	e := setup(t)

	t.Run("through the JSON API", func(t *testing.T) {
		id := e.createEntity(t, "individual", "Api Person")
		e.uploadedDocument(t, id)
		e.assertActors(t, id, supportAgent)
	})

	t.Run("through the UI", func(t *testing.T) {
		form := url.Values{"type": {"individual"}, "legal_name": {"Ui Person"}, "country": {"GB"}, "idempotency_key": {"actors-test"}}
		resp := e.uiPost(t, supportAgent, "/ui/entities", "application/x-www-form-urlencoded", form.Encode())
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("create: %d %s", resp.StatusCode, resp.Raw)
		}
		id := strings.TrimPrefix(resp.Header.Get("Location"), "/ui/entities/")

		file := samplePDF('b')
		body, err := json.Marshal(map[string]any{
			"doc_type": "passport", "content_type": "application/pdf", "size_bytes": len(file), "sha256": hexSHA(file),
		})
		if err != nil {
			t.Fatal(err)
		}
		resp = e.uiPost(t, supportAgent, "/ui/entities/"+id+"/documents", "application/json", string(body))
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("declare: %d %s", resp.StatusCode, resp.Raw)
		}
		var grant map[string]any
		if err := json.Unmarshal(resp.Raw, &grant); err != nil {
			t.Fatal(err)
		}
		if status, msg := putToVault(t, grant, file); status != http.StatusOK {
			t.Fatalf("upload: %d %s", status, msg)
		}
		resp = e.uiPost(t, supportAgent, grant["complete_url"].(string), "application/json", "")
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("complete: %d %s", resp.StatusCode, resp.Raw)
		}
		e.assertActors(t, id, supportAgent)
	})

	t.Run("each person is credited for their own step", func(t *testing.T) {
		id := e.createEntity(t, "individual", "Two People")
		file := samplePDF('c')
		docID, upload := e.intent(t, id, "passport", "application/pdf", file)
		if status, msg := putToVault(t, upload, file); status != http.StatusOK {
			t.Fatalf("upload: %d %s", status, msg)
		}
		resp, _ := e.do(t, "POST", "/entities/"+id+"/documents/"+docID+"/complete", complianceOfficer, "")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("complete: %d %s", resp.StatusCode, resp.Raw)
		}
		if got := e.queryString(t, `SELECT declared_by::text FROM documents WHERE id = $1`, docID); got != supportAgent {
			t.Errorf("declared_by = %s", got)
		}
		if got := e.queryString(t, `SELECT completed_by::text FROM documents WHERE id = $1`, docID); got != complianceOfficer {
			t.Errorf("completed_by = %s", got)
		}
	})
}

// The actor columns are for the Audit Trail: no role reads them from the Entity.
func TestRedactedViewHasNoActorFields(t *testing.T) {
	e := setup(t)
	id := e.createEntity(t, "individual", "Ada Lovelace")
	e.uploadedDocument(t, id)
	for _, user := range []string{supportAgent, complianceOfficer} {
		resp, _ := e.do(t, "GET", "/entities/"+id, user, "")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d", resp.StatusCode)
		}
		for _, field := range []string{"created_by", "declared_by", "completed_by"} {
			if strings.Contains(string(resp.Raw), field) {
				t.Errorf("as %s, the response has %s: %s", user, field, resp.Raw)
			}
		}
	}
}
