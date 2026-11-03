//go:build integration

package httpapi_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bfischetti/kyc-lifecycle/internal/kyc"
	"github.com/bfischetti/kyc-lifecycle/internal/screening"
)

// pendingEntity creates an Entity and completes one upload, so it is pending review.
func (e *env) pendingEntity(t *testing.T, typ, legalName string) string {
	t.Helper()
	id := e.createEntity(t, typ, legalName)
	e.uploadedDocument(t, id)
	return id
}

func (e *env) runCheck(t *testing.T, user, entityID, checkType string) (result, map[string]any) {
	t.Helper()
	return e.do(t, "POST", "/entities/"+entityID+"/checks", user, `{"check_type":"`+checkType+`"}`)
}

func TestScreening(t *testing.T) {
	e := setup(t, withProviderTimeout(300*time.Millisecond))

	t.Run("an ordinary entity passes every check type", func(t *testing.T) {
		id := e.pendingEntity(t, "individual", "Ada Lovelace")
		for _, ct := range []string{"sanctions_screening", "pep_screening", "address_verification"} {
			resp, body := e.runCheck(t, complianceOfficer, id, ct)
			if resp.StatusCode != http.StatusCreated {
				t.Fatalf("%s: status %d: %s", ct, resp.StatusCode, resp.Raw)
			}
			chk := body["check"].(map[string]any)
			if chk["result"] != "pass" || chk["check_type"] != ct || chk["provider"] != "mock-provider" || chk["raw_response"] == nil {
				t.Errorf("check = %v", chk)
			}
		}
		if n := e.queryInt(t, "SELECT count(*) FROM checks c JOIN check_payloads p ON p.check_id = c.id WHERE c.entity_id = $1 AND c.result = 'pass'", id); n != 3 {
			t.Errorf("recorded passing checks with payloads = %d, want 3", n)
		}
	})

	t.Run("a hard fail rejects the entity on behalf of the system", func(t *testing.T) {
		id := e.pendingEntity(t, "individual", "Ivan SANCTIONED")
		resp, body := e.runCheck(t, complianceOfficer, id, "sanctions_screening")
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("status %d: %s", resp.StatusCode, resp.Raw)
		}
		if r := body["check"].(map[string]any)["result"]; r != "fail" {
			t.Errorf("result = %v, want fail", r)
		}
		if ent := body["entity"].(map[string]any); ent["status"] != "rejected" || ent["version"] != float64(2) {
			t.Errorf("entity = %v, want rejected v2", ent)
		}
		if n := e.queryInt(t, `SELECT count(*) FROM status_history WHERE entity_id = $1
			AND from_status = 'pending_review' AND to_status = 'rejected' AND entity_version = 2
			AND changed_by = $2 AND reason LIKE '%sanctions_screening%'`, id, systemUser); n != 1 {
			t.Errorf("system rejection audit records = %d, want 1", n)
		}
		if n := e.queryInt(t, `SELECT count(*) FROM outbox WHERE entity_id = $1 AND payload->>'to' = 'rejected'
			AND payload->>'changed_by' = $2`, id, systemUser); n != 1 {
			t.Errorf("rejection notifications = %d, want 1", n)
		}

		// The audit trail, oldest first, now shows both changes.
		_, hist := e.do(t, "GET", "/entities/"+id+"/history", complianceOfficer, "")
		changes := hist["changes"].([]any)
		if len(changes) != 2 || changes[0].(map[string]any)["to_status"] != "pending_review" ||
			changes[1].(map[string]any)["to_status"] != "rejected" {
			t.Errorf("history = %v", changes)
		}
	})

	t.Run("a PEP hit needs manual review and leaves the entity pending", func(t *testing.T) {
		id := e.pendingEntity(t, "individual", "Minister PEP")
		resp, body := e.runCheck(t, complianceOfficer, id, "pep_screening")
		if resp.StatusCode != http.StatusCreated || body["check"].(map[string]any)["result"] != "manual_review" {
			t.Fatalf("status %d: %s", resp.StatusCode, resp.Raw)
		}
		if s := body["entity"].(map[string]any)["status"]; s != "pending_review" {
			t.Errorf("entity status = %v, want pending_review", s)
		}
	})

	t.Run("a provider error records nothing and is retryable", func(t *testing.T) {
		for _, name := range []string{"PROVIDERDOWN Ltd", "TIMEOUT Ltd"} {
			id := e.pendingEntity(t, "business", name)
			start := time.Now()
			resp, body := e.runCheck(t, complianceOfficer, id, "sanctions_screening")
			if resp.StatusCode != http.StatusBadGateway {
				t.Errorf("%s: status %d, want 502 (%s)", name, resp.StatusCode, resp.Raw)
				continue
			}
			if errBody := body["error"].(map[string]any); errBody["retryable"] != true {
				t.Errorf("%s: error = %v, want retryable", name, errBody)
			}
			if d := time.Since(start); d > 3*time.Second {
				t.Errorf("%s: took %v; the provider deadline did not bound the request", name, d)
			}
			if n := e.queryInt(t, "SELECT count(*) FROM checks WHERE entity_id = $1", id); n != 0 {
				t.Errorf("%s: checks recorded = %d, want 0", name, n)
			}
			if s := e.queryString(t, "SELECT status::text || ':' || version FROM entities WHERE id = $1", id); s != "pending_review:1" {
				t.Errorf("%s: entity = %s, want pending_review:1", name, s)
			}
		}
	})

	t.Run("checks run only while pending review", func(t *testing.T) {
		unverified := e.createEntity(t, "individual", "Ada Lovelace")
		rejected := e.pendingEntity(t, "individual", "Ivan SANCTIONED")
		e.runCheck(t, complianceOfficer, rejected, "sanctions_screening")
		verified := e.pendingEntity(t, "individual", "Ada Lovelace")
		e.adminExec(t, "UPDATE entities SET status = 'verified' WHERE id = '"+verified+"'")

		for name, id := range map[string]string{"unverified": unverified, "rejected": rejected, "verified": verified} {
			if resp, _ := e.runCheck(t, complianceOfficer, id, "pep_screening"); resp.StatusCode != http.StatusConflict {
				t.Errorf("%s: status %d, want 409", name, resp.StatusCode)
			}
		}
	})

	t.Run("roles: support agents are refused, the system may screen", func(t *testing.T) {
		id := e.pendingEntity(t, "individual", "Ada Lovelace")
		if resp, _ := e.runCheck(t, supportAgent, id, "sanctions_screening"); resp.StatusCode != http.StatusForbidden {
			t.Errorf("support agent: status %d, want 403", resp.StatusCode)
		}
		if resp, _ := e.runCheck(t, systemUser, id, "sanctions_screening"); resp.StatusCode != http.StatusCreated {
			t.Errorf("system: status %d, want 201", resp.StatusCode)
		}
	})

	t.Run("re-running adds a new check and keeps the earlier one", func(t *testing.T) {
		id := e.pendingEntity(t, "individual", "Ada Lovelace")
		_, first := e.runCheck(t, complianceOfficer, id, "pep_screening")
		_, second := e.runCheck(t, complianceOfficer, id, "pep_screening")
		if first["check"].(map[string]any)["id"] == second["check"].(map[string]any)["id"] {
			t.Error("re-run returned the same check")
		}
		if n := e.queryInt(t, "SELECT count(*) FROM checks WHERE entity_id = $1 AND check_type = 'pep_screening'", id); n != 2 {
			t.Errorf("pep checks = %d, want 2", n)
		}
	})

	t.Run("bad input", func(t *testing.T) {
		id := e.pendingEntity(t, "individual", "Ada Lovelace")
		if resp, _ := e.runCheck(t, complianceOfficer, id, "credit_check"); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("unknown check type: status %d, want 400", resp.StatusCode)
		}
		if resp, _ := e.runCheck(t, complianceOfficer, "11111111-1111-1111-1111-111111111111", "pep_screening"); resp.StatusCode != http.StatusNotFound {
			t.Errorf("unknown entity: status %d, want 404", resp.StatusCode)
		}
	})
}

// gatedProvider pauses each Screen call until released, so a test can change
// the Entity while the provider is "working".
type gatedProvider struct {
	entered chan struct{}
	release chan struct{}
}

func (g *gatedProvider) Screen(ctx context.Context, s screening.Subject, c kyc.CheckType) (screening.Outcome, error) {
	g.entered <- struct{}{}
	select {
	case <-g.release:
	case <-ctx.Done():
		return screening.Outcome{}, ctx.Err()
	}
	return screening.Mock{}.Screen(ctx, s, c)
}

func TestScreeningDiscardsAnswerIfEntityChangedMeanwhile(t *testing.T) {
	gate := &gatedProvider{entered: make(chan struct{}), release: make(chan struct{})}
	e := setup(t, withProvider(gate))

	// Uploading does not call the provider, so no gating is needed for setup.
	id := e.pendingEntity(t, "individual", "Ada Lovelace")

	type reply struct {
		res  result
		body map[string]any
	}
	done := make(chan reply, 1)
	go func() {
		res, body := e.runCheck(t, complianceOfficer, id, "sanctions_screening")
		done <- reply{res, body}
	}()

	<-gate.entered
	// A compliance rejection lands while the provider is still working.
	e.adminExec(t, "UPDATE entities SET status = 'rejected', version = version + 1 WHERE id = '"+id+"'")
	close(gate.release)

	r := <-done
	if r.res.StatusCode != http.StatusConflict {
		t.Errorf("status %d, want 409 (%s)", r.res.StatusCode, r.res.Raw)
	}
	if !strings.Contains(string(r.res.Raw), "rejected") {
		t.Errorf("error should name the current status: %s", r.res.Raw)
	}
	if n := e.queryInt(t, "SELECT count(*) FROM checks WHERE entity_id = $1", id); n != 0 {
		t.Errorf("checks recorded = %d, want 0", n)
	}
}
