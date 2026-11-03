package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/bfischetti/kyc-lifecycle/internal/db/dbgen"
	"github.com/bfischetti/kyc-lifecycle/internal/kyc"
)

// EventTypeStatusChanged is the type of every Status Change Notification.
const EventTypeStatusChanged = "entity.status_changed"

// StatusChangedEvent is the Status Change Notification payload: identifiers
// and the transition only, never Document contents or provider payloads.
// Consumers de-duplicate on EventID and order on EntityVersion.
type StatusChangedEvent struct {
	EventID       uuid.UUID  `json:"event_id"`
	EntityID      uuid.UUID  `json:"entity_id"`
	EntityVersion int        `json:"entity_version"`
	From          kyc.Status `json:"from"`
	To            kyc.Status `json:"to"`
	Reason        string     `json:"reason"`
	ChangedBy     uuid.UUID  `json:"changed_by"`
	OccurredAt    time.Time  `json:"occurred_at"`
}

// applyTransition applies event to an Entity that the caller has already
// locked (GetEntityForUpdate) in the current transaction. If the status
// changes, it increments the Entity Version and writes exactly one audit
// record and one outbox row, all in that same transaction, so either all of
// it happens or none of it does. An allowed event that leaves the status
// unchanged writes nothing.
//
// Lock order everywhere: the entity row first, then any document rows.
func applyTransition(ctx context.Context, q *dbgen.Queries, actor kyc.Actor, locked dbgen.Entity, event kyc.Event, reason string) (dbgen.Entity, error) {
	from := kyc.Status(locked.Status)
	to, err := kyc.Transition(from, event)
	if errors.Is(err, kyc.ErrInvalidTransition) {
		return locked, fmt.Errorf("%w: entity is %s", ErrConflict, from)
	}
	if err != nil {
		return locked, err
	}
	if to == from {
		return locked, nil
	}

	updated, err := q.UpdateEntityStatus(ctx, dbgen.UpdateEntityStatusParams{
		ID: locked.ID, Version: locked.Version, Status: dbgen.EntityStatus(to),
	})
	if err != nil {
		return locked, fmt.Errorf("update entity status: %w", err)
	}
	change, err := q.InsertStatusHistory(ctx, dbgen.InsertStatusHistoryParams{
		EntityID:      locked.ID,
		FromStatus:    dbgen.EntityStatus(from),
		ToStatus:      dbgen.EntityStatus(to),
		EntityVersion: updated.Version,
		Reason:        reason,
		ChangedBy:     actor.UserID,
	})
	if err != nil {
		return locked, fmt.Errorf("insert status history: %w", err)
	}

	ev := StatusChangedEvent{
		EventID:       uuid.New(),
		EntityID:      locked.ID,
		EntityVersion: int(updated.Version),
		From:          from,
		To:            to,
		Reason:        reason,
		ChangedBy:     actor.UserID,
		OccurredAt:    change.ChangedAt,
	}
	payload, err := json.Marshal(ev)
	if err != nil {
		return locked, fmt.Errorf("marshal notification: %w", err)
	}
	if err := q.InsertOutbox(ctx, dbgen.InsertOutboxParams{
		ID: ev.EventID, EntityID: locked.ID, EventType: EventTypeStatusChanged, Payload: payload,
	}); err != nil {
		return locked, fmt.Errorf("insert outbox: %w", err)
	}
	return updated, nil
}
