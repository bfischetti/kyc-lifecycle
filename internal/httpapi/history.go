package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/bfischetti/kyc-lifecycle/internal/observability"
)

type statusChangeResponse struct {
	FromStatus    string    `json:"from_status"`
	ToStatus      string    `json:"to_status"`
	EntityVersion int       `json:"entity_version"`
	Reason        string    `json:"reason"`
	ChangedBy     uuid.UUID `json:"changed_by"`
	ChangedAt     time.Time `json:"changed_at"`
}

type historyResponse struct {
	Changes []statusChangeResponse `json:"changes"`
}

func (h *handlers) history(w http.ResponseWriter, r *http.Request) {
	entityID, ok := pathUUID(w, r, h, "id")
	if !ok {
		return
	}
	observability.Annotate(r.Context(), slog.String("entity_id", entityID.String()))

	changes, err := h.svc.History(r.Context(), actorFrom(r.Context()), entityID)
	if err != nil {
		writeAppError(w, r, h.log, err)
		return
	}
	out := historyResponse{Changes: make([]statusChangeResponse, len(changes))}
	for i, c := range changes {
		out.Changes[i] = statusChangeResponse{
			FromStatus: string(c.From), ToStatus: string(c.To), EntityVersion: c.EntityVersion,
			Reason: c.Reason, ChangedBy: c.ChangedBy, ChangedAt: c.ChangedAt,
		}
	}
	writeJSON(w, http.StatusOK, out)
}
