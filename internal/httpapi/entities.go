package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/bfischetti/kyc-lifecycle/internal/app"
	"github.com/bfischetti/kyc-lifecycle/internal/kyc"
	"github.com/bfischetti/kyc-lifecycle/internal/observability"
)

type handlers struct {
	svc *app.Service
	log *slog.Logger
}

type createEntityRequest struct {
	Type      string `json:"type"`
	LegalName string `json:"legal_name"`
	Country   string `json:"country"`
}

type entityResponse struct {
	ID        uuid.UUID `json:"id"`
	Type      string    `json:"type"`
	LegalName string    `json:"legal_name"`
	Country   string    `json:"country"`
	Status    string    `json:"status"`
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func toEntityResponse(e kyc.Entity) entityResponse {
	return entityResponse{
		ID: e.ID, Type: string(e.Type), LegalName: e.LegalName, Country: e.Country,
		Status: string(e.Status), Version: e.Version, CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt,
	}
}

func (h *handlers) createEntity(w http.ResponseWriter, r *http.Request) {
	var req createEntityRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, h.log, http.StatusBadRequest, "invalid_json", err.Error(), nil)
		return
	}
	e, err := h.svc.CreateEntity(r.Context(), actorFrom(r.Context()), kyc.NewEntity{
		Type: kyc.EntityType(req.Type), LegalName: req.LegalName, Country: req.Country,
	})
	if err != nil {
		writeAppError(w, r, h.log, err)
		return
	}
	observability.Annotate(r.Context(), slog.String("entity_id", e.ID.String()))
	h.log.InfoContext(r.Context(), "entity created", slog.String("type", string(e.Type)))
	w.Header().Set("Location", "/entities/"+e.ID.String())
	writeJSON(w, http.StatusCreated, toEntityResponse(e))
}

func (h *handlers) getEntity(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, r, h.log, http.StatusNotFound, "not_found", "resource not found", nil)
		return
	}
	observability.Annotate(r.Context(), slog.String("entity_id", id.String()))
	e, err := h.svc.GetEntity(r.Context(), actorFrom(r.Context()), id)
	if err != nil {
		writeAppError(w, r, h.log, err)
		return
	}
	writeJSON(w, http.StatusOK, toEntityResponse(e))
}
