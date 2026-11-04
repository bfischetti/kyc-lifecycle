package app

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/bfischetti/kyc-lifecycle/internal/db"
	"github.com/bfischetti/kyc-lifecycle/internal/db/dbgen"
	"github.com/bfischetti/kyc-lifecycle/internal/kyc"
	"github.com/bfischetti/kyc-lifecycle/internal/policy"
)

// Checklist is where an Entity's most recent Review Cycle stands against the
// approval requirements. An Entity with nothing under review has no Items:
// an unverified one has no cycle yet, and a verified one's is settled.
type Checklist struct {
	Items      []kyc.ChecklistItem
	Unmet      []kyc.Unmet
	Ready      bool // the cycle is open and approval would be allowed
	CycleStart time.Time
	// SystemRejection says a Hard Fail, not a person, rejected the Entity.
	SystemRejection bool
}

// ReviewChecklist reads the checklist from the same logic that decides approval
// (see Review), so the two can't disagree. A rejected Entity gets the checklist
// of the cycle that ended, so the page can show what failed.
func (s *Service) ReviewChecklist(ctx context.Context, actor kyc.Actor, entityID uuid.UUID) (Checklist, error) {
	if !policy.Allowed(actor.Role, policy.ReadChecklist) {
		return Checklist{}, ErrForbidden
	}
	var out Checklist
	err := s.store.InTx(ctx, actor, func(q *dbgen.Queries) error {
		ent, err := q.GetEntity(ctx, entityID)
		if db.IsNoRows(err) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("get entity: %w", err)
		}
		status := kyc.Status(ent.Status)
		if status != kyc.StatusPendingReview && status != kyc.StatusRejected {
			return nil
		}

		out.CycleStart, err = q.CurrentCycleStart(ctx, entityID)
		if err != nil {
			return fmt.Errorf("cycle start: %w", err)
		}
		out.Unmet, err = s.unmetForApproval(ctx, q, ent, out.CycleStart)
		if err != nil {
			return err
		}
		out.Items = kyc.Checklist(kyc.EntityType(ent.Type), out.Unmet)
		out.Ready = status == kyc.StatusPendingReview && len(out.Unmet) == 0

		if status == kyc.StatusRejected {
			history, err := q.ListStatusHistory(ctx, entityID)
			if err != nil {
				return fmt.Errorf("list history: %w", err)
			}
			if n := len(history); n > 0 {
				out.SystemRejection = history[n-1].ChangedBy == kyc.SystemUserID
			}
		}
		return nil
	})
	if err != nil {
		return Checklist{}, err
	}
	return out, nil
}
