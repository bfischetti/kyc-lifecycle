package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/bfischetti/kyc-lifecycle/internal/kyc"
	"github.com/bfischetti/kyc-lifecycle/internal/observability"
)

type runCheckRequest struct {
	CheckType string `json:"check_type"`
}

type checkResponse struct {
	ID          uuid.UUID       `json:"id"`
	CheckType   string          `json:"check_type"`
	Provider    string          `json:"provider"`
	Result      string          `json:"result"`
	RunBy       uuid.UUID       `json:"run_by"`
	CreatedAt   time.Time       `json:"created_at"`
	RawResponse json.RawMessage `json:"raw_response,omitempty"`
}

type runCheckResponse struct {
	Check  checkResponse  `json:"check"`
	Entity entityResponse `json:"entity"`
}

func toCheckResponse(c kyc.Check) checkResponse {
	return checkResponse{
		ID: c.ID, CheckType: string(c.Type), Provider: c.Provider, Result: string(c.Result),
		RunBy: c.RunBy, CreatedAt: c.CreatedAt, RawResponse: c.RawResponse,
	}
}

func (h *handlers) runCheck(w http.ResponseWriter, r *http.Request) {
	entityID, ok := pathUUID(w, r, h, "id")
	if !ok {
		return
	}
	observability.Annotate(r.Context(), slog.String("entity_id", entityID.String()))

	var req runCheckRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, h.log, http.StatusBadRequest, "invalid_json", err.Error(), nil)
		return
	}
	// Only compliance officers and the system may run checks, and both may
	// see the raw provider payload, so it is returned here as-is.
	chk, ent, err := h.svc.RunCheck(r.Context(), actorFrom(r.Context()), entityID, kyc.CheckType(req.CheckType))
	if err != nil {
		writeAppError(w, r, h.log, err)
		return
	}
	observability.Annotate(r.Context(), slog.String("check_id", chk.ID.String()))
	h.log.InfoContext(r.Context(), "check recorded",
		slog.String("check_type", string(chk.Type)), slog.String("result", string(chk.Result)),
		slog.String("entity_status", string(ent.Status)))
	writeJSON(w, http.StatusCreated, runCheckResponse{Check: toCheckResponse(chk), Entity: toEntityResponse(ent)})
}
