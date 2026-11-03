package app

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/bfischetti/kyc-lifecycle/internal/db"
	"github.com/bfischetti/kyc-lifecycle/internal/db/dbgen"
	"github.com/bfischetti/kyc-lifecycle/internal/kyc"
	"github.com/bfischetti/kyc-lifecycle/internal/policy"
	"github.com/bfischetti/kyc-lifecycle/internal/vault"
)

// AccessEvidence is an Evidence Access: it records that actor opened the
// Document's contents, then returns a short-lived URL for the bytes.
//
// The record commits before the URL is returned, so every URL handed out
// is accounted for. A refused request (wrong role, unknown or incomplete
// document) records nothing.
func (s *Service) AccessEvidence(ctx context.Context, actor kyc.Actor, entityID, docID uuid.UUID) (vault.DownloadPermission, error) {
	if !policy.Allowed(actor.Role, policy.AccessEvidence) {
		return vault.DownloadPermission{}, ErrForbidden
	}

	var perm vault.DownloadPermission
	err := s.store.InTx(ctx, actor, func(q *dbgen.Queries) error {
		doc, err := q.GetDocument(ctx, dbgen.GetDocumentParams{ID: docID, EntityID: entityID})
		if db.IsNoRows(err) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("get document: %w", err)
		}
		if kyc.UploadStatus(doc.UploadStatus) != kyc.UploadComplete {
			return fmt.Errorf("%w: document is %s; only a completed upload has contents to open", ErrConflict, doc.UploadStatus)
		}
		if err := q.RecordEvidenceAccess(ctx, dbgen.RecordEvidenceAccessParams{
			DocumentID: doc.ID, EntityID: doc.EntityID, AccessedBy: actor.UserID,
		}); err != nil {
			return fmt.Errorf("record evidence access: %w", err)
		}
		// Presigning is local, so it can sit inside the transaction: if it
		// fails, the access record rolls back with it.
		perm, err = s.vault.PresignDownload(ctx, doc.VaultKey, downloadName(doc), s.opts.EvidenceURLTTL)
		return err
	})
	if err != nil {
		return vault.DownloadPermission{}, err
	}
	return perm, nil
}

// downloadName names the file after the Document id, never anything that
// identifies the Entity.
func downloadName(d dbgen.Document) string {
	ext := map[string]string{"application/pdf": ".pdf", "image/jpeg": ".jpg", "image/png": ".png"}[d.ContentType]
	return d.ID.String() + ext
}
