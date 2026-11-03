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
	"github.com/bfischetti/kyc-lifecycle/internal/vault"
)

// CompleteUpload turns an Upload Intent into a Completed Upload once the
// Evidence Vault holds exactly the declared bytes, and applies the resulting
// status change (e.g. unverified -> pending_review, opening a Review Cycle).
//
// It is idempotent: completing an already-completed Document returns the
// same result and changes nothing.
func (s *Service) CompleteUpload(ctx context.Context, actor kyc.Actor, entityID, docID uuid.UUID) (kyc.Document, kyc.Entity, error) {
	if !policy.Allowed(actor.Role, policy.CompleteUpload) {
		return kyc.Document{}, kyc.Entity{}, ErrForbidden
	}

	// 1. Read the intent. Done in its own short transaction because step 2
	//    makes network calls, which must not run while holding row locks.
	var doc dbgen.Document
	var ent dbgen.Entity
	err := s.store.InTx(ctx, actor, func(q *dbgen.Queries) error {
		var err error
		doc, err = q.GetDocument(ctx, dbgen.GetDocumentParams{ID: docID, EntityID: entityID})
		if db.IsNoRows(err) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("get document: %w", err)
		}
		ent, err = q.GetEntity(ctx, entityID)
		return err
	})
	if err != nil {
		return kyc.Document{}, kyc.Entity{}, err
	}
	switch kyc.UploadStatus(doc.UploadStatus) {
	case kyc.UploadComplete:
		return documentFromRow(doc), entityFromRow(ent), nil
	case kyc.UploadExpired:
		return kyc.Document{}, kyc.Entity{}, fmt.Errorf("%w: upload permission expired; declare the document again", ErrGone)
	}

	// 2. Verify the bytes really are in the vault and match the declaration.
	if err := s.verifyUpload(ctx, doc); err != nil {
		return kyc.Document{}, kyc.Entity{}, err
	}

	// 3. Record the Completed Upload and apply the transition atomically.
	var before dbgen.Entity
	err = s.store.InTx(ctx, actor, func(q *dbgen.Queries) error {
		locked, err := q.GetEntityForUpdate(ctx, entityID)
		if err != nil {
			return fmt.Errorf("lock entity: %w", err)
		}
		before = locked
		cur, err := q.GetDocumentForUpdate(ctx, dbgen.GetDocumentForUpdateParams{ID: docID, EntityID: entityID})
		if err != nil {
			return fmt.Errorf("lock document: %w", err)
		}
		switch kyc.UploadStatus(cur.UploadStatus) {
		case kyc.UploadComplete: // a concurrent /complete won; same result
			doc, ent = cur, locked
			return nil
		case kyc.UploadExpired:
			return fmt.Errorf("%w: upload permission expired; declare the document again", ErrGone)
		}

		// Check the transition is allowed before recording anything.
		from := kyc.Status(locked.Status)
		if _, err := kyc.Transition(from, kyc.EventCompletedUpload); err != nil {
			return fmt.Errorf("%w: entity is %s", ErrConflict, from)
		}
		if doc, err = q.MarkDocumentUploaded(ctx, docID); err != nil {
			return fmt.Errorf("mark uploaded: %w", err)
		}
		ent, err = applyTransition(ctx, q, actor, locked, kyc.EventCompletedUpload, completionReason(from))
		return err
	})
	if err != nil {
		return kyc.Document{}, kyc.Entity{}, err
	}
	s.recordTransition(before, ent)
	return documentFromRow(doc), entityFromRow(ent), nil
}

func completionReason(from kyc.Status) string {
	if from == kyc.StatusRejected {
		return "resubmission: document uploaded"
	}
	return "document uploaded"
}

// verifyUpload checks existence, size, checksum and content type via the
// store's metadata, then the file's leading bytes. The store already rejects
// uploads that differ from the signed declaration (ADR-0002); this is the
// second, independent layer.
func (s *Service) verifyUpload(ctx context.Context, doc dbgen.Document) error {
	info, err := s.vault.Stat(ctx, doc.VaultKey)
	if errors.Is(err, vault.ErrObjectNotFound) {
		return fmt.Errorf("%w: nothing has been uploaded yet", ErrUploadMismatch)
	}
	if err != nil {
		return err
	}
	switch {
	case info.SizeBytes != doc.SizeBytes:
		return fmt.Errorf("%w: size is %d bytes, declared %d", ErrUploadMismatch, info.SizeBytes, doc.SizeBytes)
	case info.SHA256Hex != doc.Sha256:
		return fmt.Errorf("%w: checksum differs from the declared sha256", ErrUploadMismatch)
	case info.ContentType != doc.ContentType:
		return fmt.Errorf("%w: content type is %q, declared %q", ErrUploadMismatch, info.ContentType, doc.ContentType)
	}
	head, err := s.vault.ReadPrefix(ctx, doc.VaultKey, kyc.MagicBytesLen)
	if err != nil {
		return err
	}
	if !kyc.MatchesMagic(doc.ContentType, head) {
		return fmt.Errorf("%w: file contents are not %s", ErrUploadMismatch, doc.ContentType)
	}
	return nil
}
