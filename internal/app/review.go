package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/bfischetti/kyc-lifecycle/internal/db"
	"github.com/bfischetti/kyc-lifecycle/internal/db/dbgen"
	"github.com/bfischetti/kyc-lifecycle/internal/kyc"
	"github.com/bfischetti/kyc-lifecycle/internal/policy"
)

// Decision is a compliance officer's verdict.
type Decision string

const (
	DecisionApprove Decision = "approve"
	DecisionReject  Decision = "reject"
)

// MinReasonLength is the shortest written reason accepted for a decision.
const MinReasonLength = 10

// ReviewRequest is a Compliance Approval or Compliance Rejection.
type ReviewRequest struct {
	Decision        Decision
	Reason          string
	ExpectedVersion *int // the Entity Version the officer looked at
}

// StaleDecisionError: the officer decided on an Entity Version that is no
// longer current, so they must look again.
type StaleDecisionError struct {
	CurrentStatus  kyc.Status
	CurrentVersion int
}

func (e *StaleDecisionError) Error() string {
	return fmt.Sprintf("stale decision: entity is now %s at version %d; review it again", e.CurrentStatus, e.CurrentVersion)
}

func (e *StaleDecisionError) Unwrap() error { return ErrConflict }

// UnmetRequirementsError: approval is not allowed yet.
type UnmetRequirementsError struct {
	Unmet []kyc.Unmet
}

func (e *UnmetRequirementsError) Error() string {
	parts := make([]string, len(e.Unmet))
	for i, u := range e.Unmet {
		parts[i] = u.Requirement + " (" + u.Problem + ")"
	}
	return "approval requirements not met: " + strings.Join(parts, ", ")
}

func (e *UnmetRequirementsError) Unwrap() error { return ErrConflict }

func (r *ReviewRequest) validate() error {
	r.Reason = strings.TrimSpace(r.Reason)
	var fields []kyc.FieldError
	if r.Decision != DecisionApprove && r.Decision != DecisionReject {
		fields = append(fields, kyc.FieldError{Field: "decision", Message: `must be "approve" or "reject"`})
	}
	if len([]rune(r.Reason)) < MinReasonLength {
		fields = append(fields, kyc.FieldError{Field: "reason", Message: fmt.Sprintf("must be at least %d characters", MinReasonLength)})
	}
	if r.ExpectedVersion == nil {
		fields = append(fields, kyc.FieldError{Field: "expected_version", Message: "is required: the entity version you reviewed"})
	}
	if len(fields) > 0 {
		return &kyc.ValidationError{Fields: fields}
	}
	return nil
}

// Review applies a Compliance Approval or Compliance Rejection.
func (s *Service) Review(ctx context.Context, actor kyc.Actor, entityID uuid.UUID, req ReviewRequest) (kyc.Entity, error) {
	if !policy.Allowed(actor.Role, policy.Review) {
		return kyc.Entity{}, ErrForbidden
	}
	if err := req.validate(); err != nil {
		return kyc.Entity{}, err
	}

	var out dbgen.Entity
	err := s.store.InTx(ctx, actor, func(q *dbgen.Queries) error {
		locked, err := q.GetEntityForUpdate(ctx, entityID)
		if db.IsNoRows(err) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock entity: %w", err)
		}
		if int(locked.Version) != *req.ExpectedVersion {
			return &StaleDecisionError{CurrentStatus: kyc.Status(locked.Status), CurrentVersion: int(locked.Version)}
		}
		if kyc.Status(locked.Status) != kyc.StatusPendingReview {
			return fmt.Errorf("%w: only entities pending review can be reviewed; entity is %s", ErrConflict, locked.Status)
		}

		cycleStart, err := q.CurrentCycleStart(ctx, entityID)
		if err != nil {
			return fmt.Errorf("cycle start: %w", err)
		}

		event, settle := kyc.EventReject, kyc.ReviewRejected
		if req.Decision == DecisionApprove {
			event, settle = kyc.EventApprove, kyc.ReviewAccepted
			unmet, err := s.unmetForApproval(ctx, q, locked, cycleStart)
			if err != nil {
				return err
			}
			if len(unmet) > 0 {
				return &UnmetRequirementsError{Unmet: unmet}
			}
		}

		if out, err = applyTransition(ctx, q, actor, locked, event, req.Reason); err != nil {
			return err
		}
		return settleCycleDocuments(ctx, q, entityID, cycleStart, settle)
	})
	return entityFromRow(out), err
}

func (s *Service) unmetForApproval(ctx context.Context, q *dbgen.Queries, e dbgen.Entity, cycleStart time.Time) ([]kyc.Unmet, error) {
	checkRows, err := q.ListChecksSince(ctx, dbgen.ListChecksSinceParams{EntityID: e.ID, Since: cycleStart})
	if err != nil {
		return nil, fmt.Errorf("list checks: %w", err)
	}
	docRows, err := q.ListCompletedDocumentsSince(ctx, dbgen.ListCompletedDocumentsSinceParams{EntityID: e.ID, Since: &cycleStart})
	if err != nil {
		return nil, fmt.Errorf("list documents: %w", err)
	}
	checks := make([]kyc.Check, len(checkRows))
	for i, r := range checkRows {
		checks[i] = checkFromRow(r, nil)
	}
	docs := make([]kyc.Document, len(docRows))
	for i, r := range docRows {
		docs[i] = documentFromRow(r)
	}
	return kyc.UnmetForApproval(kyc.ApprovalInput{
		EntityType: kyc.EntityType(e.Type), Checks: checks, Documents: docs, Today: time.Now(),
	}), nil
}

// settleCycleDocuments applies the Review Cycle's outcome to its Completed
// Uploads still pending review (Document Review Status).
func settleCycleDocuments(ctx context.Context, q *dbgen.Queries, entityID uuid.UUID, cycleStart time.Time, status kyc.DocumentReviewStatus) error {
	if err := q.SettleCycleDocuments(ctx, dbgen.SettleCycleDocumentsParams{
		EntityID: entityID, Since: &cycleStart, ReviewStatus: dbgen.DocumentReviewStatus(status),
	}); err != nil {
		return fmt.Errorf("settle documents: %w", err)
	}
	return nil
}
