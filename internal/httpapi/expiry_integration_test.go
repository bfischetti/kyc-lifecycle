//go:build integration

package httpapi_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bfischetti/kyc-lifecycle/internal/db"
	"github.com/bfischetti/kyc-lifecycle/internal/db/dbgen"
	"github.com/bfischetti/kyc-lifecycle/internal/expiry"
	"github.com/bfischetti/kyc-lifecycle/internal/kyc"
)

const uploadTTL = 2 * time.Second

func (e *env) store(t *testing.T) *db.Store {
	t.Helper()
	store, err := db.Open(context.Background(), e.pg.AppURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	return store
}

func (e *env) uploadStatus(t *testing.T, docID string) string {
	t.Helper()
	return e.queryString(t, "SELECT upload_status::text FROM documents WHERE id = $1", docID)
}

// untouched asserts the Entity is still unverified with no audit records or
// notifications: an Upload Intent, expired or not, never changes it.
func (e *env) untouched(t *testing.T, entityID string) {
	t.Helper()
	if s, v := e.queryString(t, "SELECT status::text FROM entities WHERE id = $1", entityID), e.version(t, entityID); s != "unverified" || v != 0 {
		t.Errorf("entity is %s v%d, want unverified v0", s, v)
	}
	if n := e.queryInt(t, "SELECT count(*) FROM status_history WHERE entity_id = $1", entityID); n != 0 {
		t.Errorf("audit records = %d, want 0", n)
	}
	if n := e.queryInt(t, "SELECT count(*) FROM outbox WHERE entity_id = $1", entityID); n != 0 {
		t.Errorf("notifications = %d, want 0", n)
	}
}

// uploadedThenLapsed declares a passport, delivers the bytes within the
// window, and waits for the window to lapse without completing.
func (e *env) uploadedThenLapsed(t *testing.T, entityID string) string {
	t.Helper()
	file := samplePDF('a')
	docID, upload := e.intent(t, entityID, "passport", "application/pdf", file)
	if status, msg := putToVault(t, upload, file); status != http.StatusOK {
		t.Fatalf("upload: %d %s", status, msg)
	}
	time.Sleep(uploadTTL + 500*time.Millisecond)
	return docID
}

func TestUploadIntentExpiry(t *testing.T) {
	e := setup(t, withUploadTTL(uploadTTL))
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))

	t.Run("the sweeper expires lapsed intents shortly after they lapse", func(t *testing.T) {
		id := e.createEntity(t, "individual", "Ada Lovelace")
		lapsed, _ := e.intent(t, id, "passport", "application/pdf", samplePDF('a'))
		time.Sleep(uploadTTL + 500*time.Millisecond)
		live, _ := e.intent(t, id, "national_id", "application/pdf", samplePDF('b'))

		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() { expiry.NewSweeper(e.store(t), quiet, 100*time.Millisecond).Run(ctx); close(done) }()
		defer func() { cancel(); <-done }()

		deadline := time.Now().Add(5 * time.Second)
		for e.uploadStatus(t, lapsed) != "expired" {
			if time.Now().After(deadline) {
				t.Fatal("lapsed intent was not expired by the sweeper")
			}
			time.Sleep(50 * time.Millisecond)
		}
		if s := e.uploadStatus(t, live); s != "awaiting_upload" {
			t.Errorf("intent still within its window is %s, want awaiting_upload", s)
		}
		e.untouched(t, id)
	})

	t.Run("an expired intent cannot be completed, even with the bytes delivered in time", func(t *testing.T) {
		id := e.createEntity(t, "individual", "Ada Lovelace")
		docID := e.uploadedThenLapsed(t, id)
		if n, err := expiry.NewSweeper(e.store(t), quiet, time.Minute).Sweep(t.Context()); err != nil || n < 1 {
			t.Fatalf("Sweep() = %d, %v; want at least 1", n, err)
		}

		resp, _ := e.complete(t, id, docID)
		if resp.StatusCode != http.StatusGone {
			t.Fatalf("complete: status %d, want 410: %s", resp.StatusCode, resp.Raw)
		}
		if s := e.uploadStatus(t, docID); s != "expired" {
			t.Errorf("document is %s, want expired", s)
		}
		e.untouched(t, id)
	})

	t.Run("an intent past its window is refused before the sweep runs", func(t *testing.T) {
		id := e.createEntity(t, "individual", "Ada Lovelace")
		docID := e.uploadedThenLapsed(t, id) // no sweeper in this subtest

		resp, _ := e.complete(t, id, docID)
		if resp.StatusCode != http.StatusGone {
			t.Fatalf("complete: status %d, want 410: %s", resp.StatusCode, resp.Raw)
		}
		if s := e.uploadStatus(t, docID); s != "awaiting_upload" {
			t.Errorf("document is %s, want awaiting_upload (refusal writes nothing)", s)
		}
		e.untouched(t, id)
	})

	t.Run("the database refuses to complete a lapsed intent on its own", func(t *testing.T) {
		// The service checks the window before calling the vault, which hides
		// this guard. It is what decides when the window lapses mid-request.
		id := e.createEntity(t, "individual", "Ada Lovelace")
		docID := e.uploadedThenLapsed(t, id)

		agent := kyc.Actor{UserID: uuid.MustParse(supportAgent), Role: kyc.RoleSupportAgent}
		err := e.store(t).InTx(t.Context(), agent, func(q *dbgen.Queries) error {
			_, err := q.MarkDocumentUploaded(t.Context(), uuid.MustParse(docID))
			return err
		})
		if !db.IsNoRows(err) {
			t.Errorf("MarkDocumentUploaded on a lapsed intent: err = %v, want no rows", err)
		}
	})

	t.Run("expired intents never count toward required documents", func(t *testing.T) {
		id := e.createEntity(t, "individual", "Ada Lovelace")
		e.uploadedThenLapsed(t, id) // the passport, never completed
		if _, err := expiry.NewSweeper(e.store(t), quiet, time.Minute).Sweep(t.Context()); err != nil {
			t.Fatal(err)
		}
		e.untouched(t, id)

		// Open a cycle with a document that is never required, then screen.
		e.declareAndUpload(t, id, "proof_of_address", "")
		e.mustPass(t, id, "sanctions_screening", "pep_screening")
		resp, body := e.review(t, complianceOfficer, id, "approve", "screening clear for this applicant", e.version(t, id))
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("approve: status %d, want 409: %s", resp.StatusCode, resp.Raw)
		}
		unmet := body["error"].(map[string]any)["unmet"].([]any)
		if len(unmet) != 1 || unmet[0].(map[string]any)["requirement"] != "document:passport|national_id" ||
			unmet[0].(map[string]any)["problem"] != "missing" {
			t.Errorf("unmet = %v, want the identity document missing", unmet)
		}
	})
}
