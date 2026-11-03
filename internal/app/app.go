// Package app holds the use cases. Each one checks policy, runs inside a
// single actor-scoped transaction, and translates storage results into
// domain types. HTTP handlers stay thin and call only this package.
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
)

var (
	ErrUnauthenticated = errors.New("unauthenticated")
	ErrForbidden       = errors.New("forbidden")
	ErrNotFound        = errors.New("not found")
)

// Service exposes the use cases.
type Service struct {
	store *db.Store
}

func New(store *db.Store) *Service { return &Service{store: store} }

// Authenticate resolves a user id to an Actor. The role always comes from the
// database, never from the caller.
func (s *Service) Authenticate(ctx context.Context, userID uuid.UUID) (kyc.Actor, error) {
	role, err := s.store.Queries().GetUserRole(ctx, userID)
	if db.IsNoRows(err) {
		return kyc.Actor{}, ErrUnauthenticated
	}
	if err != nil {
		return kyc.Actor{}, fmt.Errorf("look up user: %w", err)
	}
	return kyc.Actor{UserID: userID, Role: kyc.Role(role)}, nil
}

// CreateEntity registers a new, unverified Entity.
func (s *Service) CreateEntity(ctx context.Context, actor kyc.Actor, in kyc.NewEntity) (kyc.Entity, error) {
	if !policy.Allowed(actor.Role, policy.CreateEntity) {
		return kyc.Entity{}, ErrForbidden
	}
	if err := in.Validate(); err != nil {
		return kyc.Entity{}, err
	}
	var out kyc.Entity
	err := s.store.InTx(ctx, actor, func(q *dbgen.Queries) error {
		row, err := q.CreateEntity(ctx, dbgen.CreateEntityParams{
			Type:      dbgen.EntityType(in.Type),
			LegalName: in.LegalName,
			Country:   in.Country,
		})
		if err != nil {
			return fmt.Errorf("insert entity: %w", err)
		}
		out = entityFromRow(row)
		return nil
	})
	return out, err
}

// GetEntity returns one Entity.
func (s *Service) GetEntity(ctx context.Context, actor kyc.Actor, id uuid.UUID) (kyc.Entity, error) {
	if !policy.Allowed(actor.Role, policy.ReadEntity) {
		return kyc.Entity{}, ErrForbidden
	}
	var out kyc.Entity
	err := s.store.InTx(ctx, actor, func(q *dbgen.Queries) error {
		row, err := q.GetEntity(ctx, id)
		if db.IsNoRows(err) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("get entity: %w", err)
		}
		out = entityFromRow(row)
		return nil
	})
	return out, err
}

func entityFromRow(r dbgen.Entity) kyc.Entity {
	return kyc.Entity{
		ID:        r.ID,
		Type:      kyc.EntityType(r.Type),
		LegalName: r.LegalName,
		Country:   r.Country,
		Status:    kyc.Status(r.Status),
		Version:   int(r.Version),
		CreatedAt: r.CreatedAt,
		UpdatedAt: r.UpdatedAt,
	}
}
