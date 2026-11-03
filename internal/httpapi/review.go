package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/bfischetti/kyc-lifecycle/internal/app"
	"github.com/bfischetti/kyc-lifecycle/internal/observability"
)

type reviewRequest struct {
	Decision        string `json:"decision"`
	Reason          string `json:"reason"`
	ExpectedVersion *int   `json:"expected_version"`
}

func (h *handlers) review(w http.ResponseWriter, r *http.Request) {
	entityID, ok := pathUUID(w, r, h, "id")
	if !ok {
		return
	}
	observability.Annotate(r.Context(), slog.String("entity_id", entityID.String()))

	var req reviewRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, h.log, http.StatusBadRequest, "invalid_json", err.Error(), nil)
		return
	}
	ent, err := h.svc.Review(r.Context(), actorFrom(r.Context()), entityID, app.ReviewRequest{
		Decision: app.Decision(req.Decision), Reason: req.Reason, ExpectedVersion: req.ExpectedVersion,
	})
	if err != nil {
		writeAppError(w, r, h.log, err)
		return
	}
	h.log.InfoContext(r.Context(), "compliance decision recorded",
		slog.String("decision", req.Decision), slog.String("entity_status", string(ent.Status)),
		slog.Int("entity_version", ent.Version))
	writeJSON(w, http.StatusOK, toEntityResponse(ent))
}
