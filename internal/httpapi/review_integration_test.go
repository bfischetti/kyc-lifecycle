//go:build integration

package httpapi_test

import (
	"fmt"
	"net/http"
	"sync"
	"testing"
)

func (e *env) review(t *testing.T, user, entityID, decision, reason string, expectedVersion int) (result, map[string]any) {
	t.Helper()
	return e.do(t, "POST", "/entities/"+entityID+"/review", user,
		fmt.Sprintf(`{"decision":%q,"reason":%q,"expected_version":%d}`, decision, reason, expectedVersion))
}

func (e *env) mustPass(t *testing.T, entityID string, checkTypes ...string) {
	t.Helper()
	for _, ct := range checkTypes {
		resp, body := e.runCheck(t, complianceOfficer, entityID, ct)
		if resp.StatusCode != http.StatusCreated || body["check"].(map[string]any)["result"] != "pass" {
			t.Fatalf("%s: status %d: %s", ct, resp.StatusCode, resp.Raw)
		}
	}
}

func (e *env) version(t *testing.T, entityID string) int {
	t.Helper()
	return e.queryInt(t, "SELECT version FROM entities WHERE id = $1", entityID)
}

// declareAndUpload declares a document with optional expiry, uploads and completes it.
func (e *env) declareAndUpload(t *testing.T, entityID, docType, expiry string) {
	t.Helper()
	file := samplePDF('a')
	body := fmt.Sprintf(`{"doc_type":%q,"content_type":"application/pdf","size_bytes":%d,"sha256":%q`, docType, len(file), hexSHA(file))
	if expiry != "" {
		body += fmt.Sprintf(`,"expiry_date":%q`, expiry)
	}
	resp, out := e.do(t, "POST", "/entities/"+entityID+"/documents", supportAgent, body+"}")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("declare: %d %s", resp.StatusCode, resp.Raw)
	}
	docID := out["document"].(map[string]any)["id"].(string)
	if status, msg := putToVault(t, out["upload"].(map[string]any), file); status != http.StatusOK {
		t.Fatalf("upload: %d %s", status, msg)
	}
	if resp, _ := e.complete(t, entityID, docID); resp.StatusCode != http.StatusOK {
		t.Fatalf("complete: %d %s", resp.StatusCode, resp.Raw)
	}
}

// The spec's headline scenario, end to end against real Postgres and the vault.
func TestHappyPathCreateUploadCheckApprove(t *testing.T) {
	e := setup(t)
	id := e.createEntity(t, "individual", "Ada Lovelace")
	e.declareAndUpload(t, id, "passport", "2031-01-01")
	e.mustPass(t, id, "sanctions_screening", "pep_screening")

	resp, ent := e.review(t, complianceOfficer, id, "approve", "all screening clear, passport valid", 1)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("approve: %d %s", resp.StatusCode, resp.Raw)
	}
	if ent["status"] != "verified" || ent["version"] != float64(2) {
		t.Errorf("entity = %v, want verified v2", ent)
	}

	_, hist := e.do(t, "GET", "/entities/"+id+"/history", complianceOfficer, "")
	changes := hist["changes"].([]any)
	if len(changes) != 2 {
		t.Fatalf("history = %v", changes)
	}
	last := changes[1].(map[string]any)
	if last["from_status"] != "pending_review" || last["to_status"] != "verified" ||
		last["changed_by"] != complianceOfficer || last["reason"] != "all screening clear, passport valid" {
		t.Errorf("approval record = %v", last)
	}
	if s := e.queryString(t, "SELECT review_status::text FROM documents WHERE entity_id = $1", id); s != "accepted" {
		t.Errorf("passport review status = %s, want accepted", s)
	}
	if n := e.queryInt(t, "SELECT count(*) FROM outbox WHERE entity_id = $1", id); n != 2 {
		t.Errorf("notifications = %d, want one per transition (2)", n)
	}
}

func TestReview(t *testing.T) {
	e := setup(t)

	t.Run("approval is refused with every unmet requirement listed", func(t *testing.T) {
		id := e.pendingEntity(t, "individual", "Ada Lovelace") // passport, no expiry
		e.mustPass(t, id, "sanctions_screening")

		resp, body := e.review(t, complianceOfficer, id, "approve", "looks fine to me", e.version(t, id))
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("status %d, want 409: %s", resp.StatusCode, resp.Raw)
		}
		errBody := body["error"].(map[string]any)
		unmet := errBody["unmet"].([]any)
		if errBody["code"] != "approval_requirements_unmet" || len(unmet) != 1 ||
			unmet[0].(map[string]any)["requirement"] != "check:pep_screening" || unmet[0].(map[string]any)["problem"] != "missing" {
			t.Errorf("error = %v", errBody)
		}
		if s := e.queryString(t, "SELECT status::text FROM entities WHERE id = $1", id); s != "pending_review" {
			t.Errorf("entity = %s, want pending_review", s)
		}
	})

	t.Run("manual review blocks approval; an expired document blocks approval", func(t *testing.T) {
		pep := e.pendingEntity(t, "individual", "Minister PEP")
		e.mustPass(t, pep, "sanctions_screening")
		e.runCheck(t, complianceOfficer, pep, "pep_screening") // manual_review
		if resp, body := e.review(t, complianceOfficer, pep, "approve", "approving regardless", e.version(t, pep)); resp.StatusCode != http.StatusConflict {
			t.Errorf("manual review: status %d, want 409 (%v)", resp.StatusCode, body)
		}

		expired := e.createEntity(t, "individual", "Ada Lovelace")
		e.declareAndUpload(t, expired, "passport", "2020-01-01")
		e.mustPass(t, expired, "sanctions_screening", "pep_screening")
		resp, body := e.review(t, complianceOfficer, expired, "approve", "all screening clear", e.version(t, expired))
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("expired: status %d, want 409", resp.StatusCode)
		}
		if p := body["error"].(map[string]any)["unmet"].([]any)[0].(map[string]any)["problem"]; p != "expired" {
			t.Errorf("problem = %v, want expired", p)
		}
	})

	t.Run("a business is approved with sanctions, address and a business registration", func(t *testing.T) {
		id := e.createEntity(t, "business", "Acme Ltd")
		e.declareAndUpload(t, id, "business_registration", "")
		e.mustPass(t, id, "sanctions_screening", "address_verification")
		if resp, _ := e.review(t, complianceOfficer, id, "approve", "registry and screening clear", e.version(t, id)); resp.StatusCode != http.StatusOK {
			t.Errorf("status %d, want 200 (%s)", resp.StatusCode, resp.Raw)
		}
	})

	t.Run("rejection rejects the entity and the cycle's documents", func(t *testing.T) {
		id := e.pendingEntity(t, "individual", "Ada Lovelace")
		resp, ent := e.review(t, complianceOfficer, id, "reject", "document appears altered", e.version(t, id))
		if resp.StatusCode != http.StatusOK || ent["status"] != "rejected" {
			t.Fatalf("status %d: %s", resp.StatusCode, resp.Raw)
		}
		if s := e.queryString(t, "SELECT string_agg(DISTINCT review_status::text, ',') FROM documents WHERE entity_id = $1", id); s != "rejected" {
			t.Errorf("document review statuses = %s, want rejected", s)
		}
	})

	t.Run("a system rejection also rejects the cycle's documents", func(t *testing.T) {
		id := e.pendingEntity(t, "individual", "Ivan SANCTIONED")
		e.runCheck(t, complianceOfficer, id, "sanctions_screening")
		if s := e.queryString(t, "SELECT string_agg(DISTINCT review_status::text, ',') FROM documents WHERE entity_id = $1", id); s != "rejected" {
			t.Errorf("document review statuses = %s, want rejected", s)
		}
	})

	t.Run("bad input is refused", func(t *testing.T) {
		id := e.pendingEntity(t, "individual", "Ada Lovelace")
		v := e.version(t, id)
		for name, body := range map[string]string{
			"short reason":     fmt.Sprintf(`{"decision":"reject","reason":"no","expected_version":%d}`, v),
			"unknown decision": fmt.Sprintf(`{"decision":"escalate","reason":"needs a second look","expected_version":%d}`, v),
			"no version":       `{"decision":"reject","reason":"needs a second look"}`,
		} {
			if resp, _ := e.do(t, "POST", "/entities/"+id+"/review", complianceOfficer, body); resp.StatusCode != http.StatusBadRequest {
				t.Errorf("%s: status %d, want 400", name, resp.StatusCode)
			}
		}
	})

	t.Run("a stale decision is refused with the current state", func(t *testing.T) {
		id := e.pendingEntity(t, "individual", "Ada Lovelace")
		e.mustPass(t, id, "sanctions_screening", "pep_screening")
		resp, body := e.review(t, complianceOfficer, id, "approve", "all screening clear", 0) // looked at v0
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("status %d, want 409", resp.StatusCode)
		}
		errBody := body["error"].(map[string]any)
		if errBody["code"] != "stale_decision" || errBody["current_status"] != "pending_review" || errBody["current_version"] != float64(1) {
			t.Errorf("error = %v", errBody)
		}
		if s := e.queryString(t, "SELECT status::text FROM entities WHERE id = $1", id); s != "pending_review" {
			t.Errorf("entity changed to %s", s)
		}
	})

	t.Run("two simultaneous approvals: one wins, one is told it is stale", func(t *testing.T) {
		id := e.pendingEntity(t, "individual", "Ada Lovelace")
		e.mustPass(t, id, "sanctions_screening", "pep_screening")
		v := e.version(t, id)

		var wg sync.WaitGroup
		statuses := make([]int, 2)
		for i := range statuses {
			wg.Add(1)
			go func() {
				defer wg.Done()
				resp, _ := e.review(t, complianceOfficer, id, "approve", "all screening clear", v)
				statuses[i] = resp.StatusCode
			}()
		}
		wg.Wait()

		ok, conflict := 0, 0
		for _, s := range statuses {
			switch s {
			case http.StatusOK:
				ok++
			case http.StatusConflict:
				conflict++
			}
		}
		if ok != 1 || conflict != 1 {
			t.Errorf("statuses = %v, want one 200 and one 409", statuses)
		}
		if n := e.queryInt(t, "SELECT count(*) FROM status_history WHERE entity_id = $1 AND to_status = 'verified'", id); n != 1 {
			t.Errorf("approval records = %d, want 1", n)
		}
	})

	t.Run("only compliance officers review, and only pending entities", func(t *testing.T) {
		id := e.pendingEntity(t, "individual", "Ada Lovelace")
		v := e.version(t, id)
		for _, user := range []string{supportAgent, systemUser} {
			if resp, _ := e.review(t, user, id, "reject", "not a good fit for us", v); resp.StatusCode != http.StatusForbidden {
				t.Errorf("%s: status %d, want 403", user, resp.StatusCode)
			}
		}
		unverified := e.createEntity(t, "individual", "Ada Lovelace")
		if resp, _ := e.review(t, complianceOfficer, unverified, "reject", "not a good fit for us", 0); resp.StatusCode != http.StatusConflict {
			t.Errorf("unverified: status %d, want 409", resp.StatusCode)
		}
		if resp, _ := e.review(t, complianceOfficer, "11111111-1111-1111-1111-111111111111", "reject", "not a good fit", 0); resp.StatusCode != http.StatusNotFound {
			t.Errorf("unknown: status %d, want 404", resp.StatusCode)
		}
	})
}
