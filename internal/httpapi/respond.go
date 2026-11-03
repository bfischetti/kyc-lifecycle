package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/bfischetti/kyc-lifecycle/internal/app"
	"github.com/bfischetti/kyc-lifecycle/internal/kyc"
)

const maxBodyBytes = 1 << 20

type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string           `json:"code"`
	Message string           `json:"message"`
	Fields  []kyc.FieldError `json:"fields,omitempty"`
	// Retryable tells the client the same request may succeed later.
	Retryable bool `json:"retryable,omitempty"`
	// Unmet lists approval requirements not yet satisfied.
	Unmet []kyc.Unmet `json:"unmet,omitempty"`
	// Current state, returned with a stale decision so the client can refresh.
	CurrentStatus  string `json:"current_status,omitempty"`
	CurrentVersion *int   `json:"current_version,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, r *http.Request, log *slog.Logger, status int, code, msg string, fields []kyc.FieldError) {
	writeJSON(w, status, errorBody{errorDetail{Code: code, Message: msg, Fields: fields}})
}

func writeInternal(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	log.ErrorContext(r.Context(), "internal error", slog.String("error", err.Error()))
	writeError(w, r, log, http.StatusInternalServerError, "internal", "internal error", nil)
}

// writeAppError maps use-case errors to HTTP responses.
func writeAppError(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	if ve, ok := kyc.AsValidationError(err); ok {
		writeError(w, r, log, http.StatusBadRequest, "invalid_input", "request has invalid fields", ve.Fields)
		return
	}
	var stale *app.StaleDecisionError
	var unmet *app.UnmetRequirementsError
	switch {
	case errors.As(err, &stale):
		writeJSON(w, http.StatusConflict, errorBody{errorDetail{
			Code: "stale_decision", Message: stale.Error(),
			CurrentStatus: string(stale.CurrentStatus), CurrentVersion: &stale.CurrentVersion,
		}})
	case errors.As(err, &unmet):
		writeJSON(w, http.StatusConflict, errorBody{errorDetail{
			Code: "approval_requirements_unmet", Message: unmet.Error(), Unmet: unmet.Unmet,
		}})
	case errors.Is(err, app.ErrForbidden):
		writeError(w, r, log, http.StatusForbidden, "forbidden", "your role may not perform this action", nil)
	case errors.Is(err, app.ErrNotFound):
		writeError(w, r, log, http.StatusNotFound, "not_found", "resource not found", nil)
	// The messages below are composed by the app package for the caller.
	case errors.Is(err, app.ErrConflict):
		writeError(w, r, log, http.StatusConflict, "conflict", err.Error(), nil)
	case errors.Is(err, app.ErrGone):
		writeError(w, r, log, http.StatusGone, "gone", err.Error(), nil)
	case errors.Is(err, app.ErrProviderUnavailable):
		// A Provider Error is an infrastructure problem, not a screening
		// answer: nothing was recorded and the client should retry.
		log.WarnContext(r.Context(), "screening provider unavailable", slog.String("error", err.Error()))
		writeJSON(w, http.StatusBadGateway, errorBody{errorDetail{
			Code: "provider_unavailable", Message: "screening provider unavailable; nothing was recorded", Retryable: true,
		}})
	case errors.Is(err, app.ErrUploadMismatch):
		writeError(w, r, log, http.StatusUnprocessableEntity, "upload_mismatch", err.Error(), nil)
	default:
		writeInternal(w, r, log, err)
	}
}

// decodeJSON reads a size-limited JSON body, rejecting unknown fields.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("body must contain a single JSON object")
	}
	return nil
}
