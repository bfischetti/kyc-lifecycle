package app

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bfischetti/kyc-lifecycle/internal/db"
	"github.com/bfischetti/kyc-lifecycle/internal/db/dbgen"
	"github.com/bfischetti/kyc-lifecycle/internal/kyc"
	"github.com/bfischetti/kyc-lifecycle/internal/policy"
)

// AuditKind is what kind of thing an Audit Trail entry records. The order of
// the constants is the order things happen in within one transaction, which
// gives entries with the same timestamp their place: a Completed Upload comes
// before the status change it causes, as a Hard Fail's Check does.
type AuditKind int

const (
	AuditEntityCreated AuditKind = iota
	AuditDocumentDeclared
	AuditDocumentCompleted
	AuditDocumentExpired
	AuditEvidenceAccessed
	AuditCheckRecorded
	AuditStatusChanged
)

// AuditEvent is one entry of an Entity's Audit Trail. Which detail fields are
// set depends on Kind.
type AuditEvent struct {
	Kind  AuditKind
	At    time.Time
	Actor *kyc.Actor // nil on rows from before this actor was recorded

	From, To    kyc.Status
	Version     int
	Reason      string
	CycleOpened int // set when this status change opened a Review Cycle, from 1

	Check    kyc.CheckType
	Result   kyc.CheckResult
	Document kyc.DocumentType
}

// KindName is the entry's kind in the glossary's words.
func (e AuditEvent) KindName() string {
	switch e.Kind {
	case AuditEntityCreated:
		return "Entity"
	case AuditStatusChanged:
		return "Status change"
	case AuditCheckRecorded:
		return "Check"
	case AuditEvidenceAccessed:
		return "Evidence Access"
	}
	return "Document"
}

// AuditTrail returns one Entity's Audit Trail, oldest first. It reads only what
// was recorded, so refused attempts and Provider Errors never appear.
func (s *Service) AuditTrail(ctx context.Context, actor kyc.Actor, entityID uuid.UUID) ([]AuditEvent, error) {
	if !policy.Allowed(actor.Role, policy.ReadAuditTrail) {
		return nil, ErrForbidden
	}
	var out []AuditEvent
	err := s.store.InTx(ctx, actor, func(q *dbgen.Queries) error {
		ent, err := q.GetEntity(ctx, entityID)
		if db.IsNoRows(err) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("get entity: %w", err)
		}
		actors := actorLookup{q: q, known: map[uuid.UUID]kyc.Role{}}

		created, err := actors.optional(ctx, ent.CreatedBy)
		if err != nil {
			return err
		}
		out = append(out, AuditEvent{Kind: AuditEntityCreated, At: ent.CreatedAt, Actor: created})

		if out, err = appendDocumentEvents(ctx, q, &actors, entityID, out); err != nil {
			return err
		}
		if out, err = appendStatusEvents(ctx, q, &actors, entityID, out); err != nil {
			return err
		}

		checks, err := q.ListChecksForEntity(ctx, entityID)
		if err != nil {
			return fmt.Errorf("list checks: %w", err)
		}
		for _, c := range checks {
			by, err := actors.get(ctx, c.RunBy)
			if err != nil {
				return err
			}
			out = append(out, AuditEvent{
				Kind: AuditCheckRecorded, At: c.CreatedAt, Actor: by,
				Check: kyc.CheckType(c.CheckType), Result: kyc.CheckResult(c.Result),
			})
		}

		accesses, err := q.ListEvidenceAccessForEntity(ctx, entityID)
		if err != nil {
			return fmt.Errorf("list evidence access: %w", err)
		}
		for _, a := range accesses {
			by, err := actors.get(ctx, a.AccessedBy)
			if err != nil {
				return err
			}
			out = append(out, AuditEvent{
				Kind: AuditEvidenceAccessed, At: a.AccessedAt, Actor: by, Document: kyc.DocumentType(a.DocType),
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.SortStableFunc(out, func(a, b AuditEvent) int {
		return cmp.Or(a.At.Compare(b.At), cmp.Compare(a.Kind, b.Kind))
	})
	return out, nil
}

func appendDocumentEvents(ctx context.Context, q *dbgen.Queries, actors *actorLookup, entityID uuid.UUID, out []AuditEvent) ([]AuditEvent, error) {
	docs, err := q.ListDocumentsForEntity(ctx, entityID)
	if err != nil {
		return nil, fmt.Errorf("list documents: %w", err)
	}
	for _, d := range docs {
		docType := kyc.DocumentType(d.DocType)
		declared, err := actors.optional(ctx, d.DeclaredBy)
		if err != nil {
			return nil, err
		}
		out = append(out, AuditEvent{Kind: AuditDocumentDeclared, At: d.CreatedAt, Actor: declared, Document: docType})

		switch kyc.UploadStatus(d.UploadStatus) {
		case kyc.UploadComplete:
			completed, err := actors.optional(ctx, d.CompletedBy)
			if err != nil {
				return nil, err
			}
			at := d.CreatedAt
			if d.UploadedAt != nil {
				at = *d.UploadedAt
			}
			out = append(out, AuditEvent{Kind: AuditDocumentCompleted, At: at, Actor: completed, Document: docType})
		case kyc.UploadExpired:
			// No person lets an Upload Intent lapse. Its permission ended at
			// this time; the sweeper only records that afterwards.
			system, err := actors.get(ctx, kyc.SystemUserID)
			if err != nil {
				return nil, err
			}
			out = append(out, AuditEvent{Kind: AuditDocumentExpired, At: d.UploadExpiresAt, Actor: system, Document: docType})
		}
	}
	return out, nil
}

func appendStatusEvents(ctx context.Context, q *dbgen.Queries, actors *actorLookup, entityID uuid.UUID, out []AuditEvent) ([]AuditEvent, error) {
	history, err := q.ListStatusHistory(ctx, entityID)
	if err != nil {
		return nil, fmt.Errorf("list history: %w", err)
	}
	cycle := 0
	for _, h := range history {
		by, err := actors.get(ctx, h.ChangedBy)
		if err != nil {
			return nil, err
		}
		e := AuditEvent{
			Kind: AuditStatusChanged, At: h.ChangedAt, Actor: by,
			From: kyc.Status(h.FromStatus), To: kyc.Status(h.ToStatus),
			Version: int(h.EntityVersion), Reason: h.Reason,
		}
		if e.To == kyc.StatusPendingReview {
			cycle++
			e.CycleOpened = cycle
		}
		out = append(out, e)
	}
	return out, nil
}

// actorLookup resolves user ids to roles, asking the database once for each.
type actorLookup struct {
	q     *dbgen.Queries
	known map[uuid.UUID]kyc.Role
}

func (l *actorLookup) get(ctx context.Context, id uuid.UUID) (*kyc.Actor, error) {
	role, ok := l.known[id]
	if !ok {
		r, err := l.q.GetUserRole(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("look up user: %w", err)
		}
		role = kyc.Role(r)
		l.known[id] = role
	}
	return &kyc.Actor{UserID: id, Role: role}, nil
}

func (l *actorLookup) optional(ctx context.Context, id pgtype.UUID) (*kyc.Actor, error) {
	if !id.Valid {
		return nil, nil
	}
	return l.get(ctx, id.Bytes)
}
