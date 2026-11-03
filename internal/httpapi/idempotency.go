package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"

	"github.com/bfischetti/kyc-lifecycle/internal/app"
)

const (
	headerIdempotencyKey = "Idempotency-Key"
	// headerReplayed marks a response served from a stored result.
	headerReplayed = "Idempotent-Replayed"
)

// Printable ASCII, so the key is safe to store and compare; a UUID fits.
var validIdempotencyKey = regexp.MustCompile(`^[\x21-\x7E]{1,255}$`)

// idempotent makes a route safe to retry. A request carrying an
// Idempotency-Key runs at most once per (user, key); retries get the
// stored response. Requests without the header pass straight through.
//
// The key is bound to the request it was first used with (method, path and
// exact body), so reusing it for anything else is refused with 422.
func idempotent(svc *app.Service, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := r.Header.Get(headerIdempotencyKey)
			if key == "" {
				next.ServeHTTP(w, r)
				return
			}
			if !validIdempotencyKey.MatchString(key) {
				writeError(w, r, log, http.StatusBadRequest, "invalid_idempotency_key",
					headerIdempotencyKey+" must be 1-255 printable ASCII characters", nil)
				return
			}
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
			if err != nil {
				writeError(w, r, log, http.StatusBadRequest, "invalid_json", "request body too large or unreadable", nil)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))

			actor := actorFrom(r.Context())
			stored, err := svc.BeginIdempotent(r.Context(), actor, key, fingerprint(r, body))
			switch {
			case errors.Is(err, app.ErrIdempotencyKeyReused):
				writeError(w, r, log, http.StatusUnprocessableEntity, "idempotency_key_reused",
					"this "+headerIdempotencyKey+" was used with a different request", nil)
				return
			case errors.Is(err, app.ErrIdempotencyInProgress):
				writeJSON(w, http.StatusConflict, errorBody{errorDetail{
					Code: "idempotency_key_in_progress", Message: err.Error(), Retryable: true,
				}})
				return
			case err != nil:
				writeInternal(w, r, log, err)
				return
			case stored != nil:
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set(headerReplayed, "true")
				w.WriteHeader(stored.Status)
				_, _ = w.Write(stored.Body)
				return
			}

			// We hold the claim. Capture the response so it can be stored
			// before the client sees it.
			rec := &capture{header: w.Header(), status: http.StatusOK}
			finished := false
			defer func() {
				if !finished { // the handler panicked: free the key for a retry
					_ = svc.FinishIdempotent(context.WithoutCancel(r.Context()), actor, key, http.StatusInternalServerError, nil)
				}
			}()
			next.ServeHTTP(rec, r)
			finished = true

			// Store even if the client has gone: the work is done, and its
			// retry must get this result rather than redo it.
			if err := svc.FinishIdempotent(context.WithoutCancel(r.Context()), actor, key, rec.status, rec.body.Bytes()); err != nil {
				log.ErrorContext(r.Context(), "store idempotent response", slog.String("error", err.Error()))
			}
			w.WriteHeader(rec.status)
			_, _ = w.Write(rec.body.Bytes())
		})
	}
}

// fingerprint identifies the request a key was first used with.
func fingerprint(r *http.Request, body []byte) []byte {
	h := sha256.New()
	h.Write([]byte(r.Method + " " + r.URL.Path + "\n"))
	h.Write(body)
	return h.Sum(nil)
}

// capture buffers a response. Headers go straight to the real writer's map.
type capture struct {
	header http.Header
	status int
	body   bytes.Buffer
	wrote  bool
}

func (c *capture) Header() http.Header { return c.header }

func (c *capture) WriteHeader(status int) {
	if !c.wrote {
		c.status, c.wrote = status, true
	}
}

func (c *capture) Write(b []byte) (int, error) {
	c.wrote = true
	return c.body.Write(b)
}
