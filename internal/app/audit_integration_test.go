//go:build integration

package app_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bfischetti/kyc-lifecycle/internal/app"
	"github.com/bfischetti/kyc-lifecycle/internal/db/dbgen"
	"github.com/bfischetti/kyc-lifecycle/internal/kyc"
)

func describe(trail []app.AuditEvent) string {
	lines := make([]string, len(trail))
	for i, e := range trail {
		who := "not recorded"
		if e.Actor != nil {
			who = string(e.Actor.Role)
		}
		detail := ""
		switch e.Kind {
		case app.AuditStatusChanged:
			detail = fmt.Sprintf(" %s>%s v%d cycle%d %q", e.From, e.To, e.Version, e.CycleOpened, e.Reason)
		case app.AuditCheckRecorded:
			detail = fmt.Sprintf(" %s=%s", e.Check, e.Result)
		case app.AuditEntityCreated:
		case app.AuditDocumentDeclared:
			detail = " declared " + string(e.Document)
		case app.AuditDocumentCompleted:
			detail = " completed " + string(e.Document)
		case app.AuditDocumentExpired:
			detail = " expired " + string(e.Document)
		case app.AuditEvidenceAccessed:
			detail = " " + string(e.Document)
		}
		lines[i] = e.KindName() + detail + " by " + who
	}
	return strings.Join(lines, "\n")
}

// The events of two Review Cycles, inserted out of order, some at the same
// instant as the change they caused.
func TestAuditTrailMergesEverythingInOrder(t *testing.T) {
	e := setupList(t)
	at := func(minutes int) time.Time { return base.Add(time.Duration(minutes) * time.Minute) }

	var id uuid.UUID
	if err := e.admin.QueryRow(t.Context(),
		`INSERT INTO entities (type, legal_name, country, status, version, created_at, created_by)
		 VALUES ('individual', 'Ada', 'GB', 'pending_review', 3, $1, $2) RETURNING id`, at(0), agent.UserID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	history := func(from, to kyc.Status, version int, reason string, by uuid.UUID, when time.Time) {
		e.exec(t, `INSERT INTO status_history (entity_id, from_status, to_status, entity_version, reason, changed_by, changed_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`, id, string(from), string(to), version, reason, by, when)
	}
	history(kyc.StatusRejected, kyc.StatusPendingReview, 3, "resubmission: document uploaded", agent.UserID, at(50))
	history(kyc.StatusPendingReview, kyc.StatusRejected, 2, "sanctions_screening failed", kyc.SystemUserID, at(30))
	history(kyc.StatusUnverified, kyc.StatusPendingReview, 1, "document uploaded", agent.UserID, at(10))

	document := func(declared, completed time.Time, status string) uuid.UUID {
		var doc uuid.UUID
		if err := e.admin.QueryRow(t.Context(),
			`INSERT INTO documents (entity_id, doc_type, content_type, size_bytes, sha256, vault_key, upload_status, upload_expires_at, uploaded_at, created_at, declared_by, completed_by)
			 VALUES ($1, 'passport', 'application/pdf', 1, repeat('a', 64), $2, $3::upload_status, $5, CASE WHEN $3::text = 'uploaded' THEN $5::timestamptz END, $4, $6, CASE WHEN $3::text = 'uploaded' THEN $6::uuid END) RETURNING id`,
			id, uuid.NewString(), status, declared, completed, agent.UserID).Scan(&doc); err != nil {
			t.Fatal(err)
		}
		return doc
	}
	first := document(at(5), at(10), "uploaded")
	document(at(40), at(45), "expired")
	document(at(48), at(50), "uploaded")

	e.seedCheck(t, id, kyc.CheckSanctions, kyc.ResultFail, at(30))
	e.exec(t, `INSERT INTO evidence_access (document_id, entity_id, accessed_by, accessed_at) VALUES ($1, $2, $3, $4)`, first, id, officer.UserID, at(20))

	trail, err := e.svc.AuditTrail(t.Context(), officer, id)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		`Entity by support_agent`,
		`Document declared passport by support_agent`,
		`Document completed passport by support_agent`,
		`Status change unverified>pending_review v1 cycle1 "document uploaded" by support_agent`,
		`Evidence Access passport by compliance_officer`,
		`Check sanctions_screening=fail by compliance_officer`,
		`Status change pending_review>rejected v2 cycle0 "sanctions_screening failed" by system`,
		`Document declared passport by support_agent`,
		`Document expired passport by system`,
		`Document declared passport by support_agent`,
		`Document completed passport by support_agent`,
		`Status change rejected>pending_review v3 cycle2 "resubmission: document uploaded" by support_agent`,
	}, "\n")
	if got := describe(trail); got != want {
		t.Errorf("trail:\n%s\nwant:\n%s", got, want)
	}
}

// Rows from before actors were recorded must not be attributed to anyone.
func TestAuditTrailOlderRowsAreNotRecorded(t *testing.T) {
	e := setupList(t)
	id := e.seedInReview(t, "older", kyc.EntityTypeIndividual)
	e.exec(t, `UPDATE entities SET created_at = now() - interval '2 hours' WHERE id = $1`, id)
	e.seedDocument(t, id, kyc.DocPassport, nil, time.Now())

	trail, err := e.svc.AuditTrail(t.Context(), officer, id)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		`Entity by not recorded`,
		`Status change unverified>pending_review v1 cycle1 "document uploaded" by support_agent`,
		`Document declared passport by not recorded`,
		`Document completed passport by not recorded`,
	}, "\n")
	if got := describe(trail); got != want {
		t.Errorf("trail:\n%s\nwant:\n%s", got, want)
	}
}

func TestAuditTrailLeavesOutWhatDidNotHappen(t *testing.T) {
	e := setupList(t)
	id := e.seedInReview(t, "PROVIDERDOWN Ltd", kyc.EntityTypeIndividual)
	before, err := e.svc.AuditTrail(t.Context(), officer, id)
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := e.svc.RunCheck(t.Context(), officer, id, kyc.CheckSanctions); !errors.Is(err, app.ErrProviderUnavailable) {
		t.Fatalf("Provider Error: err = %v", err)
	}
	version := 1
	if _, err := e.svc.Review(t.Context(), officer, id, app.ReviewRequest{
		Decision: app.DecisionApprove, Reason: "should be refused", ExpectedVersion: &version,
	}); err == nil {
		t.Fatal("approval went through")
	}
	stale := 9
	if _, err := e.svc.Review(t.Context(), officer, id, app.ReviewRequest{
		Decision: app.DecisionReject, Reason: "a stale decision", ExpectedVersion: &stale,
	}); err == nil {
		t.Fatal("stale decision went through")
	}

	after, err := e.svc.AuditTrail(t.Context(), officer, id)
	if err != nil {
		t.Fatal(err)
	}
	if describe(after) != describe(before) {
		t.Errorf("trail changed:\n%s\nwas:\n%s", describe(after), describe(before))
	}
}

func TestAuditTrailIsForOfficersOnly(t *testing.T) {
	e := setupList(t)
	id := e.seedInReview(t, "someone", kyc.EntityTypeIndividual)
	e.seedDocument(t, id, kyc.DocPassport, nil, time.Now())
	var doc uuid.UUID
	if err := e.admin.QueryRow(t.Context(), `SELECT id FROM documents WHERE entity_id = $1`, id).Scan(&doc); err != nil {
		t.Fatal(err)
	}
	e.exec(t, `INSERT INTO evidence_access (document_id, entity_id, accessed_by) VALUES ($1, $2, $3)`, doc, id, officer.UserID)

	system := kyc.Actor{UserID: kyc.SystemUserID, Role: kyc.RoleSystem}
	for _, actor := range []kyc.Actor{agent, system} {
		if _, err := e.svc.AuditTrail(t.Context(), actor, id); !errors.Is(err, app.ErrForbidden) {
			t.Errorf("%s: err = %v, want forbidden", actor.Role, err)
		}
	}
	if _, err := e.svc.AuditTrail(t.Context(), officer, uuid.New()); !errors.Is(err, app.ErrNotFound) {
		t.Errorf("unknown Entity: err = %v, want not found", err)
	}

	// Past the policy module, row-level security still hides the log.
	rows := func(actor kyc.Actor) int {
		var n int
		err := e.store.InTx(t.Context(), actor, func(q *dbgen.Queries) error {
			got, err := q.ListEvidenceAccessForEntity(t.Context(), id)
			n = len(got)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if got := rows(agent); got != 0 {
		t.Errorf("an agent read %d access rows through RLS", got)
	}
	if got := rows(officer); got != 1 {
		t.Errorf("an officer read %d access rows, want 1", got)
	}
}
