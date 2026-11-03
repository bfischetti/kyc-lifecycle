//go:build integration

package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/bfischetti/kyc-lifecycle/internal/kyc"
	"github.com/bfischetti/kyc-lifecycle/internal/screening"
)

// scriptedProvider returns queued results per Check Type, then passes. The
// mock is keyed on the legal name, which a Resubmission does not change, so
// a script is the only way to get "PEP fails, then PEP passes".
type scriptedProvider struct {
	mu     sync.Mutex
	script map[kyc.CheckType][]kyc.CheckResult
}

func (p *scriptedProvider) Screen(_ context.Context, _ screening.Subject, c kyc.CheckType) (screening.Outcome, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := kyc.ResultPass
	if q := p.script[c]; len(q) > 0 {
		result, p.script[c] = q[0], q[1:]
	}
	raw, _ := json.Marshal(map[string]string{"scripted": string(result)})
	return screening.Outcome{Provider: "scripted", Result: result, RawResponse: raw}, nil
}

func (e *env) status(t *testing.T, entityID string) string {
	t.Helper()
	return e.queryString(t, "SELECT status::text FROM entities WHERE id = $1", entityID)
}

// rejected returns an Entity rejected by a Compliance Rejection, with a
// passport completed in that first cycle.
func (e *env) rejected(t *testing.T, legalName string) string {
	t.Helper()
	id := e.createEntity(t, "individual", legalName)
	e.declareAndUpload(t, id, "passport", "")
	if resp, _ := e.review(t, complianceOfficer, id, "reject", "photo does not match the applicant", e.version(t, id)); resp.StatusCode != http.StatusOK {
		t.Fatalf("reject: %d %s", resp.StatusCode, resp.Raw)
	}
	return id
}

func TestResubmission(t *testing.T) {
	provider := &scriptedProvider{script: map[kyc.CheckType][]kyc.CheckResult{}}
	e := setup(t, withProvider(provider))

	t.Run("screening from the previous cycle does not count", func(t *testing.T) {
		provider.mu.Lock()
		provider.script[kyc.CheckPEP] = []kyc.CheckResult{kyc.ResultFail}
		provider.mu.Unlock()

		id := e.createEntity(t, "individual", "Ada Lovelace")
		e.declareAndUpload(t, id, "passport", "")
		e.mustPass(t, id, "sanctions_screening")
		if _, body := e.runCheck(t, complianceOfficer, id, "pep_screening"); body["entity"].(map[string]any)["status"] != "rejected" {
			t.Fatalf("PEP fail should cause a System Rejection: %v", body)
		}

		// Resubmission: a Completed Upload on the rejected Entity.
		e.declareAndUpload(t, id, "passport", "")
		if s, v := e.status(t, id), e.version(t, id); s != "pending_review" || v != 3 {
			t.Fatalf("after resubmission: %s v%d, want pending_review v3", s, v)
		}
		_, hist := e.do(t, "GET", "/entities/"+id+"/history", complianceOfficer, "")
		changes := hist["changes"].([]any)
		last := changes[len(changes)-1].(map[string]any)
		if len(changes) != 3 || last["from_status"] != "rejected" || last["to_status"] != "pending_review" ||
			last["entity_version"] != float64(3) || last["changed_by"] != supportAgent {
			t.Errorf("history = %v", changes)
		}
		if n := e.queryInt(t, "SELECT count(*) FROM outbox WHERE entity_id = $1", id); n != 3 {
			t.Errorf("notifications = %d, want 3 (one per transition)", n)
		}

		// PEP now passes, but the sanctions pass belongs to the closed cycle.
		e.mustPass(t, id, "pep_screening")
		resp, body := e.review(t, complianceOfficer, id, "approve", "PEP hit cleared on resubmission", e.version(t, id))
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("approve: status %d, want 409: %s", resp.StatusCode, resp.Raw)
		}
		unmet := body["error"].(map[string]any)["unmet"].([]any)
		if len(unmet) != 1 || unmet[0].(map[string]any)["requirement"] != "check:sanctions_screening" ||
			unmet[0].(map[string]any)["problem"] != "missing" {
			t.Errorf("unmet = %v, want only sanctions missing", unmet)
		}

		e.mustPass(t, id, "sanctions_screening")
		if resp, _ := e.review(t, complianceOfficer, id, "approve", "PEP hit cleared on resubmission", e.version(t, id)); resp.StatusCode != http.StatusOK {
			t.Fatalf("approve after re-screening: %d %s", resp.StatusCode, resp.Raw)
		}
	})

	t.Run("a document from the previous cycle does not count", func(t *testing.T) {
		id := e.rejected(t, "Grace Hopper")
		// Resubmit with a document that is never a Required Document.
		e.declareAndUpload(t, id, "proof_of_address", "")
		e.mustPass(t, id, "sanctions_screening", "pep_screening")

		resp, body := e.review(t, complianceOfficer, id, "approve", "screening clear in new cycle", e.version(t, id))
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("approve: status %d, want 409: %s", resp.StatusCode, resp.Raw)
		}
		unmet := body["error"].(map[string]any)["unmet"].([]any)
		if len(unmet) != 1 || unmet[0].(map[string]any)["requirement"] != "document:passport|national_id" {
			t.Errorf("unmet = %v, want only the identity document", unmet)
		}
	})

	t.Run("earlier documents and checks keep their original state", func(t *testing.T) {
		provider.mu.Lock()
		provider.script[kyc.CheckSanctions] = []kyc.CheckResult{kyc.ResultFail}
		provider.mu.Unlock()

		id := e.createEntity(t, "individual", "Alan Turing")
		e.declareAndUpload(t, id, "passport", "")
		e.runCheck(t, complianceOfficer, id, "sanctions_screening") // System Rejection
		e.declareAndUpload(t, id, "national_id", "")

		_, view := e.do(t, "GET", "/entities/"+id, complianceOfficer, "")
		review := map[string]string{}
		for _, d := range view["documents"].([]any) {
			doc := d.(map[string]any)
			review[doc["doc_type"].(string)] = doc["review_status"].(string)
		}
		if review["passport"] != "rejected" || review["national_id"] != "pending" {
			t.Errorf("document review statuses = %v, want passport rejected, national_id pending", review)
		}
		checks := view["checks"].([]any)
		if len(checks) != 1 || checks[0].(map[string]any)["result"] != "fail" {
			t.Errorf("checks = %v, want the earlier sanctions fail", checks)
		}
	})

	t.Run("a verified entity accepts no further documents", func(t *testing.T) {
		id := e.createEntity(t, "individual", "Katherine Johnson")
		e.declareAndUpload(t, id, "passport", "")
		e.mustPass(t, id, "sanctions_screening", "pep_screening")
		if resp, _ := e.review(t, complianceOfficer, id, "approve", "all screening clear", e.version(t, id)); resp.StatusCode != http.StatusOK {
			t.Fatalf("approve: %d %s", resp.StatusCode, resp.Raw)
		}
		before := e.queryInt(t, "SELECT count(*) FROM documents WHERE entity_id = $1", id)

		file := samplePDF('b')
		resp, _ := e.do(t, "POST", "/entities/"+id+"/documents", supportAgent, declareBody("national_id", "application/pdf", file))
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("declare: status %d, want 409: %s", resp.StatusCode, resp.Raw)
		}
		if after := e.queryInt(t, "SELECT count(*) FROM documents WHERE entity_id = $1", id); after != before {
			t.Errorf("documents = %d, want %d (nothing recorded)", after, before)
		}
		if s := e.status(t, id); s != "verified" {
			t.Errorf("status = %s, want verified", s)
		}
	})
}
