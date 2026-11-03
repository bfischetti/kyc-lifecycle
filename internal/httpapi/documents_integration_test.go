//go:build integration

package httpapi_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// samplePDF is a small file that starts with the PDF magic bytes.
func samplePDF(fill byte) []byte {
	return append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte{fill}, 2048)...)
}

func hexSHA(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func declareBody(docType, contentType string, file []byte) string {
	return fmt.Sprintf(`{"doc_type":%q,"content_type":%q,"size_bytes":%d,"sha256":%q}`,
		docType, contentType, len(file), hexSHA(file))
}

// putToVault performs the upload exactly as a client following the API
// response would: the given method and URL, plus every returned header.
func putToVault(t *testing.T, upload map[string]any, body []byte) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), upload["method"].(string), upload["url"].(string), bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range upload["headers"].(map[string]any) {
		if strings.EqualFold(k, "Content-Length") {
			continue // set by net/http from the body
		}
		req.Header.Set(k, v.(string))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func (e *env) objectExists(t *testing.T, key string) bool {
	t.Helper()
	_, err := e.vault.Client().HeadObject(t.Context(), &s3.HeadObjectInput{
		Bucket: aws.String(e.vault.Bucket), Key: aws.String(key),
	})
	var nf *types.NotFound
	if errors.As(err, &nf) {
		return false
	}
	if err != nil {
		t.Fatalf("head object: %v", err)
	}
	return true
}

func TestUploadIntent(t *testing.T) {
	e := setup(t)
	file := samplePDF('a')

	t.Run("declaring records an intent and leaves the Entity unchanged", func(t *testing.T) {
		for _, user := range []string{complianceOfficer, supportAgent} {
			entityID := e.createEntity(t, "individual", "Ada Lovelace")
			resp, body := e.do(t, "POST", "/entities/"+entityID+"/documents", user,
				`{"doc_type":"passport","expiry_date":"2031-05-01","content_type":"application/pdf","size_bytes":`+
					fmt.Sprint(len(file))+`,"sha256":"`+hexSHA(file)+`"}`)
			if resp.StatusCode != http.StatusCreated {
				t.Fatalf("declare as %s: status %d, body %s", user, resp.StatusCode, resp.Raw)
			}
			doc := body["document"].(map[string]any)
			if doc["upload_status"] != "awaiting_upload" || doc["review_status"] != "pending" ||
				doc["entity_id"] != entityID || doc["expiry_date"] != "2031-05-01" || doc["id"] == "" {
				t.Errorf("document = %v", doc)
			}
			upload := body["upload"].(map[string]any)
			if upload["method"] != "PUT" || upload["url"] == "" {
				t.Errorf("upload = %v", upload)
			}

			_, ent := e.do(t, "GET", "/entities/"+entityID, user, "")
			if ent["status"] != "unverified" || ent["version"] != float64(0) {
				t.Errorf("entity changed: %v", ent)
			}
			if n := e.queryInt(t, "SELECT count(*) FROM documents WHERE entity_id = $1 AND upload_status = 'awaiting_upload'", entityID); n != 1 {
				t.Errorf("awaiting documents = %d, want 1", n)
			}
		}
	})

	t.Run("the declared bytes can be uploaded", func(t *testing.T) {
		entityID := e.createEntity(t, "individual", "Ada Lovelace")
		_, body := e.do(t, "POST", "/entities/"+entityID+"/documents", supportAgent, declareBody("passport", "application/pdf", file))
		docID := body["document"].(map[string]any)["id"].(string)

		status, msg := putToVault(t, body["upload"].(map[string]any), file)
		if status != http.StatusOK {
			t.Fatalf("upload: status %d: %s", status, msg)
		}
		key := e.queryString(t, "SELECT vault_key FROM documents WHERE id = $1", docID)
		if !e.objectExists(t, key) {
			t.Error("uploaded object not in vault")
		}
	})

	t.Run("bytes that differ from the declaration are not stored", func(t *testing.T) {
		entityID := e.createEntity(t, "individual", "Ada Lovelace")
		_, body := e.do(t, "POST", "/entities/"+entityID+"/documents", supportAgent, declareBody("passport", "application/pdf", file))
		docID := body["document"].(map[string]any)["id"].(string)

		status, _ := putToVault(t, body["upload"].(map[string]any), samplePDF('b'))
		if status < 400 || status >= 500 {
			t.Errorf("tampered upload: status %d, want 4xx", status)
		}
		key := e.queryString(t, "SELECT vault_key FROM documents WHERE id = $1", docID)
		if e.objectExists(t, key) {
			t.Error("tampered bytes were stored")
		}
	})

	t.Run("invalid declarations are refused and nothing is recorded", func(t *testing.T) {
		entityID := e.createEntity(t, "individual", "Ada Lovelace")
		sha := hexSHA(file)
		for name, body := range map[string]string{
			"content type": `{"doc_type":"passport","content_type":"application/zip","size_bytes":10,"sha256":"` + sha + `"}`,
			"too large":    `{"doc_type":"passport","content_type":"application/pdf","size_bytes":10485761,"sha256":"` + sha + `"}`,
			"zero size":    `{"doc_type":"passport","content_type":"application/pdf","size_bytes":0,"sha256":"` + sha + `"}`,
			"bad sha":      `{"doc_type":"passport","content_type":"application/pdf","size_bytes":10,"sha256":"abc"}`,
			"doc type":     `{"doc_type":"selfie","content_type":"application/pdf","size_bytes":10,"sha256":"` + sha + `"}`,
			"bad expiry":   `{"doc_type":"passport","expiry_date":"soon","content_type":"application/pdf","size_bytes":10,"sha256":"` + sha + `"}`,
		} {
			resp, _ := e.do(t, "POST", "/entities/"+entityID+"/documents", supportAgent, body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("%s: status %d, want 400 (%s)", name, resp.StatusCode, resp.Raw)
			}
		}
		if n := e.queryInt(t, "SELECT count(*) FROM documents WHERE entity_id = $1", entityID); n != 0 {
			t.Errorf("documents recorded = %d, want 0", n)
		}
	})

	t.Run("unknown entity is 404 and the system user is refused", func(t *testing.T) {
		resp, _ := e.do(t, "POST", "/entities/11111111-1111-1111-1111-111111111111/documents", supportAgent,
			declareBody("passport", "application/pdf", file))
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("unknown entity: status %d, want 404", resp.StatusCode)
		}
		entityID := e.createEntity(t, "individual", "Ada Lovelace")
		resp, _ = e.do(t, "POST", "/entities/"+entityID+"/documents", systemUser, declareBody("passport", "application/pdf", file))
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("system user: status %d, want 403", resp.StatusCode)
		}
	})

	t.Run("the vault key is never exposed outside the upload URL", func(t *testing.T) {
		entityID := e.createEntity(t, "individual", "Ada Lovelace")
		resp, body := e.do(t, "POST", "/entities/"+entityID+"/documents", supportAgent, declareBody("passport", "application/pdf", file))
		docID := body["document"].(map[string]any)["id"].(string)
		key := e.queryString(t, "SELECT vault_key FROM documents WHERE id = $1", docID)
		objectID := strings.TrimPrefix(key, "documents/")

		if !strings.Contains(string(resp.Raw), objectID) {
			t.Fatal("test is not meaningful: key not even in the upload URL")
		}
		delete(body["upload"].(map[string]any), "url")
		rest, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(rest), objectID) {
			t.Errorf("vault key appears in a response field other than the upload URL: %s", rest)
		}
		if strings.Contains(e.logs.String(), objectID) {
			t.Error("vault key appears in logs")
		}
	})
}

func TestUploadPermissionExpires(t *testing.T) {
	e := setup(t, withUploadTTL(2*time.Second))
	file := samplePDF('a')
	entityID := e.createEntity(t, "individual", "Ada Lovelace")
	_, body := e.do(t, "POST", "/entities/"+entityID+"/documents", supportAgent, declareBody("passport", "application/pdf", file))
	upload := body["upload"].(map[string]any)

	expiresAt, err := time.Parse(time.RFC3339Nano, upload["expires_at"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Until(expiresAt); d <= 0 || d > 3*time.Second {
		t.Errorf("expires_at is %v away, want about 2s", d)
	}

	time.Sleep(3 * time.Second)
	if status, _ := putToVault(t, upload, file); status != http.StatusForbidden {
		t.Errorf("upload after expiry: status %d, want 403", status)
	}
}
