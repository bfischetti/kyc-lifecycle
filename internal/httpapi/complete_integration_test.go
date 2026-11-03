//go:build integration

package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/jackc/pgx/v5"
)

// intent declares a Document and returns its id and the upload permission.
func (e *env) intent(t *testing.T, entityID, docType, contentType string, file []byte) (string, map[string]any) {
	t.Helper()
	resp, body := e.do(t, "POST", "/entities/"+entityID+"/documents", supportAgent, declareBody(docType, contentType, file))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("declare: status %d: %s", resp.StatusCode, resp.Raw)
	}
	return body["document"].(map[string]any)["id"].(string), body["upload"].(map[string]any)
}

func (e *env) complete(t *testing.T, entityID, docID string) (result, map[string]any) {
	t.Helper()
	return e.do(t, "POST", "/entities/"+entityID+"/documents/"+docID+"/complete", supportAgent, "")
}

// uploadedDocument declares, uploads and completes a passport.
func (e *env) uploadedDocument(t *testing.T, entityID string) map[string]any {
	t.Helper()
	file := samplePDF('a')
	docID, upload := e.intent(t, entityID, "passport", "application/pdf", file)
	if status, msg := putToVault(t, upload, file); status != http.StatusOK {
		t.Fatalf("upload: %d %s", status, msg)
	}
	resp, body := e.complete(t, entityID, docID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("complete: status %d: %s", resp.StatusCode, resp.Raw)
	}
	return body
}

func (e *env) adminExec(t *testing.T, sql string) {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), e.pg.AdminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	if _, err := conn.Exec(context.Background(), sql); err != nil {
		t.Fatal(err)
	}
}

// putDirect writes an object straight to the vault, bypassing the presigned
// URL, to simulate a store that did not enforce the declaration.
func (e *env) putDirect(t *testing.T, key string, body []byte, contentType string) {
	t.Helper()
	if _, err := e.vault.Client().PutObject(t.Context(), &s3.PutObjectInput{
		Bucket: aws.String(e.vault.Bucket), Key: aws.String(key), Body: bytes.NewReader(body),
		ContentType: aws.String(contentType), ChecksumAlgorithm: types.ChecksumAlgorithmSha256,
	}); err != nil {
		t.Fatalf("direct put: %v", err)
	}
}

func TestCompletedUpload(t *testing.T) {
	e := setup(t)

	t.Run("first completed upload opens a review cycle with one audit record and one notification", func(t *testing.T) {
		entityID := e.createEntity(t, "individual", "Ada Lovelace")
		body := e.uploadedDocument(t, entityID)

		doc, ent := body["document"].(map[string]any), body["entity"].(map[string]any)
		if doc["upload_status"] != "uploaded" || doc["uploaded_at"] == nil {
			t.Errorf("document = %v", doc)
		}
		if ent["status"] != "pending_review" || ent["version"] != float64(1) {
			t.Errorf("entity = %v", ent)
		}

		if n := e.queryInt(t, `SELECT count(*) FROM status_history WHERE entity_id = $1
			AND from_status = 'unverified' AND to_status = 'pending_review' AND entity_version = 1
			AND changed_by = $2 AND reason <> ''`, entityID, supportAgent); n != 1 {
			t.Errorf("audit records = %d, want 1", n)
		}
		if n := e.queryInt(t, "SELECT count(*) FROM status_history WHERE entity_id = $1", entityID); n != 1 {
			t.Errorf("total audit records = %d, want 1", n)
		}

		payload := e.queryString(t, "SELECT payload::text FROM outbox WHERE entity_id = $1 AND published_at IS NULL", entityID)
		var ev map[string]any
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			t.Fatal(err)
		}
		if ev["entity_id"] != entityID || ev["entity_version"] != float64(1) || ev["from"] != "unverified" ||
			ev["to"] != "pending_review" || ev["changed_by"] != supportAgent || ev["event_id"] == nil {
			t.Errorf("notification = %v", ev)
		}
		eventID := e.queryString(t, "SELECT id::text FROM outbox WHERE entity_id = $1", entityID)
		if ev["event_id"] != eventID {
			t.Errorf("event_id %v != outbox id %s", ev["event_id"], eventID)
		}
		for _, k := range []string{"sha256", "vault_key", "content_type", "size_bytes", "legal_name"} {
			if strings.Contains(payload, k) {
				t.Errorf("notification contains %q: %s", k, payload)
			}
		}
	})

	t.Run("if the audit record or notification cannot be written, nothing changes", func(t *testing.T) {
		entityID := e.createEntity(t, "individual", "Ada Lovelace")
		file := samplePDF('a')
		docID, upload := e.intent(t, entityID, "passport", "application/pdf", file)
		if status, _ := putToVault(t, upload, file); status != http.StatusOK {
			t.Fatal("upload failed")
		}

		e.adminExec(t, `CREATE FUNCTION fail_outbox() RETURNS trigger LANGUAGE plpgsql AS
			$$ BEGIN RAISE EXCEPTION 'simulated outbox failure'; END $$;
			CREATE TRIGGER fail_outbox BEFORE INSERT ON outbox FOR EACH ROW EXECUTE FUNCTION fail_outbox();`)
		resp, _ := e.complete(t, entityID, docID)
		e.adminExec(t, `DROP TRIGGER fail_outbox ON outbox; DROP FUNCTION fail_outbox();`)

		if resp.StatusCode != http.StatusInternalServerError {
			t.Errorf("status %d, want 500", resp.StatusCode)
		}
		if s := e.queryString(t, "SELECT status::text || ':' || version FROM entities WHERE id = $1", entityID); s != "unverified:0" {
			t.Errorf("entity = %s, want unverified:0", s)
		}
		if s := e.queryString(t, "SELECT upload_status::text FROM documents WHERE id = $1", docID); s != "awaiting_upload" {
			t.Errorf("document = %s, want awaiting_upload", s)
		}
		if n := e.queryInt(t, "SELECT count(*) FROM status_history WHERE entity_id = $1", entityID); n != 0 {
			t.Errorf("audit records = %d, want 0", n)
		}

		// Once the fault is gone, the same request succeeds.
		if resp, _ := e.complete(t, entityID, docID); resp.StatusCode != http.StatusOK {
			t.Errorf("retry: status %d", resp.StatusCode)
		}
	})

	t.Run("an upload that does not match the declaration is refused and stays awaiting", func(t *testing.T) {
		file := samplePDF('a')
		cases := map[string]func(t *testing.T, key string, upload map[string]any){
			"nothing uploaded": func(*testing.T, string, map[string]any) {},
			"wrong checksum": func(t *testing.T, key string, _ map[string]any) {
				e.putDirect(t, key, samplePDF('b'), "application/pdf") // same size, other bytes
			},
			"wrong size": func(t *testing.T, key string, _ map[string]any) {
				e.putDirect(t, key, append(samplePDF('a'), 'x'), "application/pdf")
			},
		}
		for name, arrange := range cases {
			t.Run(name, func(t *testing.T) {
				entityID := e.createEntity(t, "individual", "Ada Lovelace")
				docID, upload := e.intent(t, entityID, "passport", "application/pdf", file)
				key := e.queryString(t, "SELECT vault_key FROM documents WHERE id = $1", docID)
				arrange(t, key, upload)

				resp, body := e.complete(t, entityID, docID)
				if resp.StatusCode != http.StatusUnprocessableEntity {
					t.Errorf("status %d, want 422 (%s)", resp.StatusCode, resp.Raw)
				}
				if body["error"] == nil {
					t.Error("no error body")
				}
				if s := e.queryString(t, "SELECT upload_status::text FROM documents WHERE id = $1", docID); s != "awaiting_upload" {
					t.Errorf("document = %s, want awaiting_upload", s)
				}
			})
		}

		t.Run("renamed file that is not really a PDF", func(t *testing.T) {
			// The bytes match the declared sha256 and size, so the store
			// accepts them; only the content check catches it.
			exe := append([]byte("MZ\x90\x00"), bytes.Repeat([]byte{0}, 600)...)
			entityID := e.createEntity(t, "individual", "Ada Lovelace")
			docID, upload := e.intent(t, entityID, "passport", "application/pdf", exe)
			if status, msg := putToVault(t, upload, exe); status != http.StatusOK {
				t.Fatalf("upload: %d %s", status, msg)
			}
			resp, _ := e.complete(t, entityID, docID)
			if resp.StatusCode != http.StatusUnprocessableEntity {
				t.Errorf("status %d, want 422 (%s)", resp.StatusCode, resp.Raw)
			}
			if s := e.queryString(t, "SELECT status::text FROM entities WHERE id = $1", entityID); s != "unverified" {
				t.Errorf("entity = %s, want unverified", s)
			}
		})
	})

	t.Run("completing twice is idempotent", func(t *testing.T) {
		entityID := e.createEntity(t, "individual", "Ada Lovelace")
		file := samplePDF('a')
		docID, upload := e.intent(t, entityID, "passport", "application/pdf", file)
		putToVault(t, upload, file)

		first, b1 := e.complete(t, entityID, docID)
		second, b2 := e.complete(t, entityID, docID)
		if first.StatusCode != http.StatusOK || second.StatusCode != http.StatusOK {
			t.Fatalf("statuses %d, %d", first.StatusCode, second.StatusCode)
		}
		if b1["entity"].(map[string]any)["version"] != b2["entity"].(map[string]any)["version"] {
			t.Errorf("version changed between calls: %v vs %v", b1["entity"], b2["entity"])
		}
		for table, want := range map[string]int{"status_history": 1, "outbox": 1} {
			if n := e.queryInt(t, "SELECT count(*) FROM "+table+" WHERE entity_id = $1", entityID); n != want {
				t.Errorf("%s rows = %d, want %d", table, n, want)
			}
		}
	})

	t.Run("another completed upload while pending review records the document only", func(t *testing.T) {
		entityID := e.createEntity(t, "individual", "Ada Lovelace")
		e.uploadedDocument(t, entityID)
		body := e.uploadedDocument(t, entityID)
		if ent := body["entity"].(map[string]any); ent["status"] != "pending_review" || ent["version"] != float64(1) {
			t.Errorf("entity = %v, want pending_review v1", ent)
		}
		if n := e.queryInt(t, "SELECT count(*) FROM status_history WHERE entity_id = $1", entityID); n != 1 {
			t.Errorf("audit records = %d, want 1", n)
		}
		if n := e.queryInt(t, "SELECT count(*) FROM documents WHERE entity_id = $1 AND upload_status = 'uploaded'", entityID); n != 2 {
			t.Errorf("uploaded documents = %d, want 2", n)
		}
	})

	t.Run("unknown document is 404 and the system user is refused", func(t *testing.T) {
		entityID := e.createEntity(t, "individual", "Ada Lovelace")
		resp, _ := e.complete(t, entityID, "11111111-1111-1111-1111-111111111111")
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("unknown document: %d, want 404", resp.StatusCode)
		}
		docID, _ := e.intent(t, entityID, "passport", "application/pdf", samplePDF('a'))
		otherEntity := e.createEntity(t, "individual", "Grace Hopper")
		if resp, _ := e.complete(t, otherEntity, docID); resp.StatusCode != http.StatusNotFound {
			t.Errorf("document of another entity: %d, want 404", resp.StatusCode)
		}
		resp, _ = e.do(t, "POST", "/entities/"+entityID+"/documents/"+docID+"/complete", systemUser, "")
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("system user: %d, want 403", resp.StatusCode)
		}
	})
}

func TestHistory(t *testing.T) {
	e := setup(t)
	entityID := e.createEntity(t, "individual", "Ada Lovelace")
	e.uploadedDocument(t, entityID)

	resp, body := e.do(t, "GET", "/entities/"+entityID+"/history", complianceOfficer, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, resp.Raw)
	}
	changes := body["changes"].([]any)
	if len(changes) != 1 {
		t.Fatalf("changes = %v, want 1", changes)
	}
	c := changes[0].(map[string]any)
	if c["from_status"] != "unverified" || c["to_status"] != "pending_review" || c["entity_version"] != float64(1) ||
		c["changed_by"] != supportAgent || c["reason"] == "" || c["changed_at"] == nil {
		t.Errorf("change = %v", c)
	}

	for _, user := range []string{supportAgent, systemUser} {
		if resp, _ := e.do(t, "GET", "/entities/"+entityID+"/history", user, ""); resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403", user, resp.StatusCode)
		}
	}
	if resp, _ := e.do(t, "GET", "/entities/11111111-1111-1111-1111-111111111111/history", complianceOfficer, ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown entity: status %d, want 404", resp.StatusCode)
	}
}
