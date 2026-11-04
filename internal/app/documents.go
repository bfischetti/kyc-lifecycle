package app

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bfischetti/kyc-lifecycle/internal/db"
	"github.com/bfischetti/kyc-lifecycle/internal/db/dbgen"
	"github.com/bfischetti/kyc-lifecycle/internal/kyc"
	"github.com/bfischetti/kyc-lifecycle/internal/policy"
	"github.com/bfischetti/kyc-lifecycle/internal/vault"
)

// DeclareDocument records an Upload Intent and returns a permission to
// deliver exactly the declared bytes to the Evidence Vault (ADR-0002). The
// Entity is unchanged: only a Completed Upload affects it.
func (s *Service) DeclareDocument(ctx context.Context, actor kyc.Actor, entityID uuid.UUID, in kyc.NewDocument) (kyc.Document, vault.UploadPermission, error) {
	if !policy.Allowed(actor.Role, policy.DeclareDocument) {
		return kyc.Document{}, vault.UploadPermission{}, ErrForbidden
	}
	if err := in.Validate(); err != nil {
		return kyc.Document{}, vault.UploadPermission{}, err
	}

	// Presigning is a local signature computation, so do it before writing
	// anything: a failure leaves no row behind.
	key := vault.NewKey()
	perm, err := s.vault.PresignUpload(ctx, key, vault.Declared{
		ContentType: in.ContentType, SizeBytes: in.SizeBytes, SHA256Hex: in.SHA256,
	}, s.opts.UploadURLTTL)
	if err != nil {
		return kyc.Document{}, vault.UploadPermission{}, err
	}

	var out kyc.Document
	err = s.store.InTx(ctx, actor, func(q *dbgen.Queries) error {
		ent, err := q.GetEntity(ctx, entityID)
		if db.IsNoRows(err) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("get entity: %w", err)
		}
		// Verified is terminal: no upload could ever complete, so refuse the
		// intent up front rather than issue a permission that leads nowhere.
		if _, err := kyc.Transition(kyc.Status(ent.Status), kyc.EventCompletedUpload); err != nil {
			return fmt.Errorf("%w: entity is %s; no further documents are accepted", ErrConflict, ent.Status)
		}
		row, err := q.CreateDocument(ctx, dbgen.CreateDocumentParams{
			EntityID:        entityID,
			DocType:         dbgen.DocumentType(in.Type),
			ExpiryDate:      in.Expiry(),
			ContentType:     in.ContentType,
			SizeBytes:       in.SizeBytes,
			Sha256:          in.SHA256,
			VaultKey:        key,
			UploadExpiresAt: perm.ExpiresAt,
			DeclaredBy:      pgtype.UUID{Bytes: actor.UserID, Valid: true},
		})
		if err != nil {
			return fmt.Errorf("insert document: %w", err)
		}
		out = documentFromRow(row)
		return nil
	})
	if err != nil {
		return kyc.Document{}, vault.UploadPermission{}, err
	}
	return out, perm, nil
}

func documentFromRow(r dbgen.Document) kyc.Document {
	return kyc.Document{
		ID:           r.ID,
		EntityID:     r.EntityID,
		Type:         kyc.DocumentType(r.DocType),
		ExpiryDate:   r.ExpiryDate,
		ContentType:  r.ContentType,
		SizeBytes:    r.SizeBytes,
		SHA256:       r.Sha256,
		UploadStatus: kyc.UploadStatus(r.UploadStatus),
		ReviewStatus: kyc.DocumentReviewStatus(r.ReviewStatus),
		CreatedAt:    r.CreatedAt,
		UploadedAt:   r.UploadedAt,
	}
}
