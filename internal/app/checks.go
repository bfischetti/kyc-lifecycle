package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/bfischetti/kyc-lifecycle/internal/db"
	"github.com/bfischetti/kyc-lifecycle/internal/db/dbgen"
	"github.com/bfischetti/kyc-lifecycle/internal/kyc"
	"github.com/bfischetti/kyc-lifecycle/internal/policy"
	"github.com/bfischetti/kyc-lifecycle/internal/screening"
)

// systemActor is who a System Rejection is attributed to.
var systemActor = kyc.Actor{UserID: kyc.SystemUserID, Role: kyc.RoleSystem}

// RunCheck screens a pending-review Entity with the Screening Provider and
// records the Check. A Hard Fail rejects the Entity in the same transaction
// (System Rejection). A Provider Error records nothing.
func (s *Service) RunCheck(ctx context.Context, actor kyc.Actor, entityID uuid.UUID, checkType kyc.CheckType) (kyc.Check, kyc.Entity, error) {
	if !policy.Allowed(actor.Role, policy.RunCheck) {
		return kyc.Check{}, kyc.Entity{}, ErrForbidden
	}
	if !checkType.Valid() {
		return kyc.Check{}, kyc.Entity{}, &kyc.ValidationError{Fields: []kyc.FieldError{
			{Field: "check_type", Message: "must be one of sanctions_screening, pep_screening, address_verification"},
		}}
	}

	// 1. Read the Entity; only pending-review Entities are screened.
	var ent dbgen.Entity
	err := s.store.InTx(ctx, actor, func(q *dbgen.Queries) error {
		var err error
		ent, err = q.GetEntity(ctx, entityID)
		if db.IsNoRows(err) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("get entity: %w", err)
		}
		return requirePendingReview(ent)
	})
	if err != nil {
		return kyc.Check{}, kyc.Entity{}, err
	}

	// 2. Call the provider outside any transaction: it can take seconds,
	//    and no row lock may be held meanwhile.
	pctx, cancel := context.WithTimeout(ctx, s.opts.ProviderTimeout)
	defer cancel()
	outcome, err := s.provider.Screen(pctx, screening.Subject{
		Type: kyc.EntityType(ent.Type), LegalName: ent.LegalName, Country: ent.Country,
	}, checkType)
	if err != nil {
		s.opts.Metrics.ProviderError(checkType)
		if errors.Is(err, screening.ErrUnavailable) || errors.Is(err, context.DeadlineExceeded) {
			return kyc.Check{}, kyc.Entity{}, fmt.Errorf("%w: %w", ErrProviderUnavailable, err)
		}
		return kyc.Check{}, kyc.Entity{}, fmt.Errorf("screen: %w", err)
	}

	// 3. Record the answer. The Entity may have changed while the provider
	//    was working, so re-check under lock; if it is no longer pending
	//    review, the (paid) answer is discarded and nothing is recorded.
	var chk dbgen.Check
	var before dbgen.Entity
	err = s.store.InTx(ctx, actor, func(q *dbgen.Queries) error {
		locked, err := q.GetEntityForUpdate(ctx, entityID)
		if err != nil {
			return fmt.Errorf("lock entity: %w", err)
		}
		if err := requirePendingReview(locked); err != nil {
			return err
		}
		before = locked
		chk, err = q.InsertCheck(ctx, dbgen.InsertCheckParams{
			EntityID:  entityID,
			CheckType: dbgen.CheckType(checkType),
			Provider:  outcome.Provider,
			Result:    dbgen.CheckResult(outcome.Result),
			RunBy:     actor.UserID,
		})
		if err != nil {
			return fmt.Errorf("insert check: %w", err)
		}
		if err := q.InsertCheckPayload(ctx, dbgen.InsertCheckPayloadParams{
			CheckID: chk.ID, RawResponse: outcome.RawResponse,
		}); err != nil {
			return fmt.Errorf("insert check payload: %w", err)
		}

		ent = locked
		if outcome.Result != kyc.ResultFail {
			return nil
		}
		// Hard Fail: System Rejection, which also rejects the cycle's documents.
		cycleStart, err := q.CurrentCycleStart(ctx, entityID)
		if err != nil {
			return fmt.Errorf("cycle start: %w", err)
		}
		if ent, err = applyTransition(ctx, q, systemActor, locked, kyc.EventHardFail, string(checkType)+" failed"); err != nil {
			return err
		}
		return settleCycleDocuments(ctx, q, entityID, cycleStart, kyc.ReviewRejected)
	})
	if err != nil {
		return kyc.Check{}, kyc.Entity{}, err
	}
	s.opts.Metrics.CheckRecorded(checkType, outcome.Result)
	s.recordTransition(before, ent)
	return checkFromRow(chk, outcome.RawResponse), entityFromRow(ent), nil
}

func requirePendingReview(e dbgen.Entity) error {
	if kyc.Status(e.Status) != kyc.StatusPendingReview {
		return fmt.Errorf("%w: checks run only while pending review; entity is %s", ErrConflict, e.Status)
	}
	return nil
}

func checkFromRow(r dbgen.Check, raw []byte) kyc.Check {
	return kyc.Check{
		ID: r.ID, EntityID: r.EntityID, Type: kyc.CheckType(r.CheckType), Provider: r.Provider,
		Result: kyc.CheckResult(r.Result), RawResponse: raw, RunBy: r.RunBy, CreatedAt: r.CreatedAt,
	}
}
