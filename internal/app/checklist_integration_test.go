//go:build integration

package app_test

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bfischetti/kyc-lifecycle/internal/app"
	"github.com/bfischetti/kyc-lifecycle/internal/kyc"
)

// seedInReview opens a Review Cycle that began an hour ago at Entity Version 1.
func (e listEnv) seedInReview(t *testing.T, name string, typ kyc.EntityType) uuid.UUID {
	t.Helper()
	id := e.seed(t, name, typ, kyc.StatusPendingReview, base)
	e.exec(t, `UPDATE entities SET version = 1 WHERE id = $1`, id)
	e.exec(t, `INSERT INTO status_history (entity_id, from_status, to_status, entity_version, reason, changed_by, changed_at)
		VALUES ($1, 'unverified', 'pending_review', 1, 'document uploaded', $2, now() - interval '1 hour')`, id, agent.UserID)
	return id
}

func (e listEnv) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := e.admin.Exec(t.Context(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

// seedDocument adds a Completed Upload, uploaded at the given time.
func (e listEnv) seedDocument(t *testing.T, id uuid.UUID, doc kyc.DocumentType, expiry *time.Time, uploaded time.Time) {
	t.Helper()
	e.exec(t, `INSERT INTO documents (entity_id, doc_type, expiry_date, content_type, size_bytes, sha256, vault_key, upload_status, upload_expires_at, uploaded_at)
		VALUES ($1, $2, $3, 'application/pdf', 1, repeat('a', 64), $4, 'uploaded', now(), $5)`,
		id, string(doc), expiry, uuid.NewString(), uploaded)
}

func (e listEnv) seedCheck(t *testing.T, id uuid.UUID, check kyc.CheckType, result kyc.CheckResult, at time.Time) {
	t.Helper()
	e.exec(t, `INSERT INTO checks (entity_id, check_type, provider, result, run_by, created_at) VALUES ($1, $2, 'test', $3, $4, $5)`,
		id, string(check), string(result), officer.UserID, at)
}

func TestChecklistAgreesWithApproval(t *testing.T) {
	e := setupList(t)
	now := time.Now()
	inCycle := now.Add(-time.Minute)
	earlier := now.Add(-2 * time.Hour)
	past := now.AddDate(0, 0, -2)
	type requirement = kyc.Unmet

	const (
		sanctions = "check:sanctions_screening"
		pep       = "check:pep_screening"
		address   = "check:address_verification"
		personDoc = "document:passport|national_id"
		bizDoc    = "document:business_registration"
	)

	tests := []struct {
		name    string
		typ     kyc.EntityType
		prepare func(t *testing.T, id uuid.UUID)
		want    []requirement
	}{
		{
			name:    "individual with nothing",
			typ:     kyc.EntityTypeIndividual,
			prepare: func(*testing.T, uuid.UUID) {},
			want:    []requirement{{Requirement: pep, Problem: "missing"}, {Requirement: sanctions, Problem: "missing"}, {Requirement: personDoc, Problem: "missing"}},
		},
		{
			name:    "business with nothing",
			typ:     kyc.EntityTypeBusiness,
			prepare: func(*testing.T, uuid.UUID) {},
			want:    []requirement{{Requirement: address, Problem: "missing"}, {Requirement: sanctions, Problem: "missing"}, {Requirement: bizDoc, Problem: "missing"}},
		},
		{
			name: "manual review blocks",
			typ:  kyc.EntityTypeIndividual,
			prepare: func(t *testing.T, id uuid.UUID) {
				e.seedDocument(t, id, kyc.DocPassport, nil, inCycle)
				e.seedCheck(t, id, kyc.CheckSanctions, kyc.ResultPass, inCycle)
				e.seedCheck(t, id, kyc.CheckPEP, kyc.ResultManualReview, inCycle)
			},
			want: []requirement{{Requirement: pep, Problem: "manual_review"}},
		},
		{
			name: "a later pass replaces a manual review",
			typ:  kyc.EntityTypeIndividual,
			prepare: func(t *testing.T, id uuid.UUID) {
				e.seedDocument(t, id, kyc.DocNationalID, nil, inCycle)
				e.seedCheck(t, id, kyc.CheckSanctions, kyc.ResultPass, inCycle)
				e.seedCheck(t, id, kyc.CheckPEP, kyc.ResultManualReview, inCycle)
				e.seedCheck(t, id, kyc.CheckPEP, kyc.ResultPass, now)
			},
		},
		{
			name: "an expired document blocks",
			typ:  kyc.EntityTypeBusiness,
			prepare: func(t *testing.T, id uuid.UUID) {
				e.seedDocument(t, id, kyc.DocBusinessRegistration, &past, inCycle)
				e.seedCheck(t, id, kyc.CheckSanctions, kyc.ResultPass, inCycle)
				e.seedCheck(t, id, kyc.CheckAddress, kyc.ResultPass, inCycle)
			},
			want: []requirement{{Requirement: bizDoc, Problem: "expired"}},
		},
		{
			name: "results from an earlier cycle don't count",
			typ:  kyc.EntityTypeBusiness,
			prepare: func(t *testing.T, id uuid.UUID) {
				e.seedDocument(t, id, kyc.DocBusinessRegistration, nil, earlier)
				e.seedCheck(t, id, kyc.CheckSanctions, kyc.ResultPass, earlier)
				e.seedCheck(t, id, kyc.CheckAddress, kyc.ResultPass, inCycle)
			},
			want: []requirement{{Requirement: sanctions, Problem: "missing"}, {Requirement: bizDoc, Problem: "missing"}},
		},
		{
			name: "everything met",
			typ:  kyc.EntityTypeBusiness,
			prepare: func(t *testing.T, id uuid.UUID) {
				e.seedDocument(t, id, kyc.DocBusinessRegistration, nil, inCycle)
				e.seedCheck(t, id, kyc.CheckSanctions, kyc.ResultPass, inCycle)
				e.seedCheck(t, id, kyc.CheckAddress, kyc.ResultPass, inCycle)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := e.seedInReview(t, tt.name, tt.typ)
			tt.prepare(t, id)

			cl, err := e.svc.ReviewChecklist(t.Context(), officer, id)
			if err != nil {
				t.Fatal(err)
			}
			if len(cl.Unmet) != len(tt.want) || (len(tt.want) > 0 && !reflect.DeepEqual(cl.Unmet, tt.want)) {
				t.Errorf("unmet = %v, want %v", cl.Unmet, tt.want)
			}
			if got, want := len(cl.Items), len(kyc.RequiredChecks(tt.typ))+1; got != want {
				t.Errorf("%d items, want %d", got, want)
			}
			open := 0
			for _, item := range cl.Items {
				if item.Problem != "" {
					open++
				}
			}
			if open != len(tt.want) {
				t.Errorf("%d items show a problem, want %d: %+v", open, len(tt.want), cl.Items)
			}
			if cl.Ready != (len(tt.want) == 0) {
				t.Errorf("Ready = %v with unmet %v", cl.Ready, tt.want)
			}

			version := 1
			_, err = e.svc.Review(t.Context(), officer, id, app.ReviewRequest{
				Decision: app.DecisionApprove, Reason: "reviewed in full", ExpectedVersion: &version,
			})
			if cl.Ready {
				if err != nil {
					t.Fatalf("checklist said ready, approval said: %v", err)
				}
				return
			}
			var refused *app.UnmetRequirementsError
			if !errors.As(err, &refused) {
				t.Fatalf("approval err = %v, want unmet requirements", err)
			}
			if !reflect.DeepEqual(refused.Unmet, cl.Unmet) {
				t.Errorf("approval said %v, checklist said %v", refused.Unmet, cl.Unmet)
			}
		})
	}
}

func TestChecklistRefusesEveryoneButOfficers(t *testing.T) {
	e := setupList(t)
	id := e.seedInReview(t, "someone", kyc.EntityTypeIndividual)
	system := kyc.Actor{UserID: kyc.SystemUserID, Role: kyc.RoleSystem}
	for _, actor := range []kyc.Actor{agent, system} {
		if _, err := e.svc.ReviewChecklist(t.Context(), actor, id); !errors.Is(err, app.ErrForbidden) {
			t.Errorf("%s: err = %v, want forbidden", actor.Role, err)
		}
	}
	if _, err := e.svc.ReviewChecklist(t.Context(), officer, uuid.New()); !errors.Is(err, app.ErrNotFound) {
		t.Errorf("unknown Entity: err = %v, want not found", err)
	}
}

func TestChecklistOutsideAnOpenCycle(t *testing.T) {
	e := setupList(t)
	for _, status := range []kyc.Status{kyc.StatusUnverified, kyc.StatusVerified} {
		id := e.seed(t, string(status), kyc.EntityTypeIndividual, status, base)
		cl, err := e.svc.ReviewChecklist(t.Context(), officer, id)
		if err != nil {
			t.Fatalf("%s: %v", status, err)
		}
		if len(cl.Items) != 0 || cl.Ready {
			t.Errorf("%s: %+v, want an empty checklist", status, cl)
		}
	}
}

func TestChecklistShowsWhoRejected(t *testing.T) {
	e := setupList(t)

	hard := e.seedInReview(t, "SANCTIONED person", kyc.EntityTypeIndividual)
	if _, _, err := e.svc.RunCheck(t.Context(), officer, hard, kyc.CheckSanctions); err != nil {
		t.Fatal(err)
	}
	cl, err := e.svc.ReviewChecklist(t.Context(), officer, hard)
	if err != nil {
		t.Fatal(err)
	}
	if !cl.SystemRejection || cl.Ready {
		t.Errorf("Hard Fail: SystemRejection = %v, Ready = %v", cl.SystemRejection, cl.Ready)
	}
	failed := false
	for _, item := range cl.Items {
		failed = failed || (item.Check == kyc.CheckSanctions && item.Problem == string(kyc.ResultFail))
	}
	if !failed {
		t.Errorf("no item shows the Hard Fail: %+v", cl.Items)
	}

	person := e.seedInReview(t, "ordinary person", kyc.EntityTypeIndividual)
	version := 1
	if _, err := e.svc.Review(t.Context(), officer, person, app.ReviewRequest{
		Decision: app.DecisionReject, Reason: "the photo is unreadable", ExpectedVersion: &version,
	}); err != nil {
		t.Fatal(err)
	}
	cl, err = e.svc.ReviewChecklist(t.Context(), officer, person)
	if err != nil {
		t.Fatal(err)
	}
	if cl.SystemRejection {
		t.Error("a Compliance Rejection was reported as a System Rejection")
	}
}
