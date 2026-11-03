// Package app holds the use cases. Each one checks policy, runs inside a
// single actor-scoped transaction, and translates storage results into
// domain types. HTTP handlers stay thin and call only this package.
package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/bfischetti/kyc-lifecycle/internal/db"
	"github.com/bfischetti/kyc-lifecycle/internal/db/dbgen"
	"github.com/bfischetti/kyc-lifecycle/internal/kyc"
	"github.com/bfischetti/kyc-lifecycle/internal/policy"
	"github.com/bfischetti/kyc-lifecycle/internal/screening"
	"github.com/bfischetti/kyc-lifecycle/internal/vault"
)

var (
	ErrUnauthenticated = errors.New("unauthenticated")
	ErrForbidden       = errors.New("forbidden")
	ErrNotFound        = errors.New("not found")
	// ErrConflict: the request is valid but not allowed in the current state.
	ErrConflict = errors.New("conflict")
	// ErrGone: the resource existed but can no longer be used.
	ErrGone = errors.New("gone")
	// ErrUploadMismatch: the vault does not hold the declared bytes.
	ErrUploadMismatch = errors.New("upload does not match declaration")
	// ErrProviderUnavailable: a Provider Error; safe to retry.
	ErrProviderUnavailable = errors.New("screening provider unavailable")
)

// Vault is the part of the Evidence Vault the use cases need.
type Vault interface {
	PresignUpload(ctx context.Context, key string, d vault.Declared, ttl time.Duration) (vault.UploadPermission, error)
	Stat(ctx context.Context, key string) (vault.ObjectInfo, error)
	ReadPrefix(ctx context.Context, key string, n int) ([]byte, error)
}

// Options tunes use-case behaviour.
type Options struct {
	// UploadURLTTL is how long an Upload Intent's permission stays valid.
	UploadURLTTL time.Duration
	// ProviderTimeout bounds each Screening Provider call.
	ProviderTimeout time.Duration
	// Metrics receives operational signals; nil records nothing.
	Metrics Recorder
}

// Recorder receives operational signals. Each is reported only once the
// change it describes has been committed.
type Recorder interface {
	CheckRecorded(kyc.CheckType, kyc.CheckResult)
	ProviderError(kyc.CheckType)
	StatusChanged(from, to kyc.Status)
}

type noRecorder struct{}

func (noRecorder) CheckRecorded(kyc.CheckType, kyc.CheckResult) {}
func (noRecorder) ProviderError(kyc.CheckType)                  {}
func (noRecorder) StatusChanged(kyc.Status, kyc.Status)         {}

// DefaultOptions are the production defaults.
var DefaultOptions = Options{UploadURLTTL: 15 * time.Minute, ProviderTimeout: 5 * time.Second}

// Service exposes the use cases.
type Service struct {
	store    *db.Store
	vault    Vault
	provider screening.Provider
	opts     Options
}

func New(store *db.Store, v Vault, p screening.Provider, opts Options) *Service {
	if opts.Metrics == nil {
		opts.Metrics = noRecorder{}
	}
	return &Service{store: store, vault: v, provider: p, opts: opts}
}

// recordTransition reports a committed status change, if there was one.
func (s *Service) recordTransition(before, after dbgen.Entity) {
	if after.Status != before.Status {
		s.opts.Metrics.StatusChanged(kyc.Status(before.Status), kyc.Status(after.Status))
	}
}

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

// EntityView is an Entity with its Documents and Checks, shaped for the
// caller's role (a Support Agent gets the Redacted View).
type EntityView struct {
	Entity    kyc.Entity
	Documents []kyc.Document
	Checks    []kyc.Check
}

// GetEntity returns an Entity with its Documents and Checks. Raw provider
// payloads are withheld from roles that may not see them, twice over: the
// policy module strips them here, and row-level security already hides the
// payload rows from such roles in the database (ADR-0001).
func (s *Service) GetEntity(ctx context.Context, actor kyc.Actor, id uuid.UUID) (EntityView, error) {
	if !policy.Allowed(actor.Role, policy.ReadEntity) {
		return EntityView{}, ErrForbidden
	}
	var out EntityView
	err := s.store.InTx(ctx, actor, func(q *dbgen.Queries) error {
		row, err := q.GetEntity(ctx, id)
		if db.IsNoRows(err) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("get entity: %w", err)
		}
		out.Entity = entityFromRow(row)

		docs, err := q.ListDocumentsForEntity(ctx, id)
		if err != nil {
			return fmt.Errorf("list documents: %w", err)
		}
		out.Documents = make([]kyc.Document, len(docs))
		for i, d := range docs {
			out.Documents[i] = documentFromRow(d)
		}

		checks, err := q.ListChecksWithPayloads(ctx, id)
		if err != nil {
			return fmt.Errorf("list checks: %w", err)
		}
		showRaw := policy.CanSee(actor.Role, policy.CheckRawResponse)
		out.Checks = make([]kyc.Check, len(checks))
		for i, c := range checks {
			var raw []byte
			if showRaw {
				raw = c.RawResponse
			}
			out.Checks[i] = kyc.Check{
				ID: c.ID, EntityID: c.EntityID, Type: kyc.CheckType(c.CheckType), Provider: c.Provider,
				Result: kyc.CheckResult(c.Result), RawResponse: raw, RunBy: c.RunBy, CreatedAt: c.CreatedAt,
			}
		}
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
