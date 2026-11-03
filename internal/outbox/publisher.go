// Package outbox delivers Status Change Notifications written to the
// transactional outbox to Downstream Consumers.
//
// Delivery is at least once: consumers de-duplicate on the notification id
// and order on the Entity Version it carries.
package outbox

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/google/uuid"
)

// Message is one notification as handed to a Publisher.
type Message struct {
	ID      uuid.UUID // stable across retries; consumers de-duplicate on it
	Type    string
	Payload json.RawMessage
}

// Publisher hands a Message to Downstream Consumers. A nil error means it
// was accepted; any error schedules a retry.
type Publisher interface {
	Publish(ctx context.Context, m Message) error
}

// LogPublisher "delivers" by logging, standing in for a real consumer.
type LogPublisher struct {
	Log *slog.Logger
}

// The worker's log scope already carries event_id and entity_id.
func (p LogPublisher) Publish(ctx context.Context, m Message) error {
	p.Log.InfoContext(ctx, "notification published",
		slog.String("event_type", m.Type),
		slog.Any("payload", m.Payload))
	return nil
}
