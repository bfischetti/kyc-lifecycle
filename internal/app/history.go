package app

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/bfischetti/kyc-lifecycle/internal/db"
	"github.com/bfischetti/kyc-lifecycle/internal/db/dbgen"
	"github.com/bfischetti/kyc-lifecycle/internal/kyc"
	"github.com/bfischetti/kyc-lifecycle/internal/policy"
)

// History returns an Entity's audit trail, oldest first.
func (s *Service) History(ctx context.Context, actor kyc.Actor, entityID uuid.UUID) ([]kyc.StatusChange, error) {
	if !policy.Allowed(actor.Role, policy.ReadHistory) {
		return nil, ErrForbidden
	}
	var out []kyc.StatusChange
	err := s.store.InTx(ctx, actor, func(q *dbgen.Queries) error {
		if _, err := q.GetEntity(ctx, entityID); db.IsNoRows(err) {
			return ErrNotFound
		} else if err != nil {
			return fmt.Errorf("get entity: %w", err)
		}
		rows, err := q.ListStatusHistory(ctx, entityID)
		if err != nil {
			return fmt.Errorf("list history: %w", err)
		}
		out = make([]kyc.StatusChange, len(rows))
		for i, r := range rows {
			out[i] = kyc.StatusChange{
				ID: r.ID, EntityID: r.EntityID,
				From: kyc.Status(r.FromStatus), To: kyc.Status(r.ToStatus),
				EntityVersion: int(r.EntityVersion), Reason: r.Reason,
				ChangedBy: r.ChangedBy, ChangedAt: r.ChangedAt,
			}
		}
		return nil
	})
	return out, err
}
