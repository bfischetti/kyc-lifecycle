package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bfischetti/kyc-lifecycle/internal/db/dbgen"
	"github.com/bfischetti/kyc-lifecycle/internal/kyc"
	"github.com/bfischetti/kyc-lifecycle/internal/policy"
)

// EntityPageSize is how many Entities one page of a list holds.
const EntityPageSize = 50

// EntityFilter narrows a list of Entities. An empty field doesn't filter.
type EntityFilter struct {
	Status kyc.Status
	Type   kyc.EntityType
	// After continues from an earlier page; nil starts at the first.
	After *EntityCursor
}

// EntityCursor marks the last Entity of a page. Continuing from a position,
// rather than skipping a count, means that an Entity leaving the list while
// someone pages through it can't push another one past them unseen.
type EntityCursor struct {
	UpdatedAt time.Time
	ID        uuid.UUID
}

// String is the cursor's form for a link. Microseconds are Postgres's own
// precision, so it round-trips exactly.
func (c EntityCursor) String() string {
	return strconv.FormatInt(c.UpdatedAt.UnixMicro(), 10) + "_" + c.ID.String()
}

// ParseEntityCursor reads the form String produces.
func ParseEntityCursor(s string) (EntityCursor, error) {
	micros, id, ok := strings.Cut(s, "_")
	if !ok {
		return EntityCursor{}, fmt.Errorf("entity cursor %q: missing separator", s)
	}
	n, err := strconv.ParseInt(micros, 10, 64)
	if err != nil {
		return EntityCursor{}, fmt.Errorf("entity cursor %q: %w", s, err)
	}
	parsed, err := uuid.Parse(id)
	if err != nil {
		return EntityCursor{}, fmt.Errorf("entity cursor %q: %w", s, err)
	}
	return EntityCursor{UpdatedAt: time.UnixMicro(n), ID: parsed}, nil
}

// EntityPage is one page of a list. Next is nil on the last page.
type EntityPage struct {
	Entities []kyc.Entity
	Next     *EntityCursor
}

// ListEntities returns one page of the Entities matching the filter, ordered
// by when their Verification Status last changed. Pending review reads oldest
// first because it is the officer's queue. Every other list reads newest first,
// where someone looking for an Entity they just worked on will find it.
func (s *Service) ListEntities(ctx context.Context, actor kyc.Actor, f EntityFilter) (EntityPage, error) {
	if !policy.Allowed(actor.Role, policy.ListEntities) {
		return EntityPage{}, ErrForbidden
	}
	var invalid []kyc.FieldError
	if f.Status != "" && !f.Status.Valid() {
		invalid = append(invalid, kyc.FieldError{Field: "status", Message: "is not a Verification Status"})
	}
	if f.Type != "" && !f.Type.Valid() {
		invalid = append(invalid, kyc.FieldError{Field: "type", Message: "must be individual or business"})
	}
	if invalid != nil {
		return EntityPage{}, &kyc.ValidationError{Fields: invalid}
	}

	// One extra row says whether another page follows.
	params := dbgen.ListEntitiesOldestFirstParams{
		Limit:  EntityPageSize + 1,
		Status: dbgen.NullEntityStatus{EntityStatus: dbgen.EntityStatus(f.Status), Valid: f.Status != ""},
		Type:   dbgen.NullEntityType{EntityType: dbgen.EntityType(f.Type), Valid: f.Type != ""},
	}
	if f.After != nil {
		params.AfterUpdatedAt = &f.After.UpdatedAt
		params.AfterID = pgtype.UUID{Bytes: f.After.ID, Valid: true}
	}

	var rows []dbgen.Entity
	err := s.store.InTx(ctx, actor, func(q *dbgen.Queries) error {
		var err error
		if f.Status == kyc.StatusPendingReview {
			rows, err = q.ListEntitiesOldestFirst(ctx, params)
		} else {
			rows, err = q.ListEntitiesNewestFirst(ctx, dbgen.ListEntitiesNewestFirstParams(params))
		}
		if err != nil {
			return fmt.Errorf("list entities: %w", err)
		}
		return nil
	})
	if err != nil {
		return EntityPage{}, err
	}

	var page EntityPage
	if len(rows) > EntityPageSize {
		rows = rows[:EntityPageSize]
		last := rows[len(rows)-1]
		page.Next = &EntityCursor{UpdatedAt: last.UpdatedAt, ID: last.ID}
	}
	page.Entities = make([]kyc.Entity, len(rows))
	for i, r := range rows {
		page.Entities[i] = entityFromRow(r)
	}
	return page, nil
}
