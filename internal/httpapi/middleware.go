package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/bfischetti/kyc-lifecycle/internal/app"
	"github.com/bfischetti/kyc-lifecycle/internal/kyc"
	"github.com/bfischetti/kyc-lifecycle/internal/metrics"
	"github.com/bfischetti/kyc-lifecycle/internal/observability"
)

const (
	headerRequestID = "X-Request-ID"
	headerUserID    = "X-User-ID"
)

// Client-supplied request ids are echoed and logged, so restrict them to a
// safe alphabet and length rather than trusting arbitrary input.
var validRequestID = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(headerRequestID)
		if !validRequestID.MatchString(id) {
			id = uuid.NewString()
		}
		w.Header().Set(headerRequestID, id)
		ctx := observability.NewScope(r.Context(), slog.String("request_id", id))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func accessLog(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.InfoContext(r.Context(), "http request",
			slog.String("method", r.Method),
			slog.String("route", r.Pattern),
			slog.Int("status", rec.status),
			slog.Duration("duration", time.Since(start)),
		)
	})
}

// observeLatency records request latency by route pattern. It must wrap the
// mux directly (no request copy in between), since the mux sets r.Pattern
// on the request it is given.
func observeLatency(m *metrics.Metrics, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		m.ObserveHTTP(methodLabel(r.Method), routeLabel(r.Pattern), rec.status, time.Since(start))
	})
}

// routeLabel turns a mux pattern such as "GET /entities/{id}" into
// "/entities/{id}". Unmatched requests share one label, so arbitrary paths
// cannot create series.
func routeLabel(pattern string) string {
	if pattern == "" {
		return "unmatched"
	}
	if _, path, ok := strings.Cut(pattern, " "); ok {
		return path
	}
	return pattern
}

// methodLabel bounds the method label too: the server accepts any token
// as a method, so an unknown one could otherwise create series at will.
func methodLabel(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodOptions:
		return method
	}
	return "other"
}

func recoverPanics(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(v)
				}
				log.ErrorContext(r.Context(), "panic", slog.Any("panic", v))
				writeError(w, r, log, http.StatusInternalServerError, "internal", "internal error", nil)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type actorKey struct{}

// authenticate resolves X-User-ID to an Actor via the database. There is no
// real authentication in scope; this stands in for a verified identity.
func authenticate(svc *app.Service, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			userID, err := uuid.Parse(r.Header.Get(headerUserID))
			if err != nil {
				writeError(w, r, log, http.StatusUnauthorized, "unauthenticated", "missing or malformed "+headerUserID, nil)
				return
			}
			actor, err := svc.Authenticate(r.Context(), userID)
			if errors.Is(err, app.ErrUnauthenticated) {
				writeError(w, r, log, http.StatusUnauthorized, "unauthenticated", "unknown user", nil)
				return
			}
			if err != nil {
				writeInternal(w, r, log, err)
				return
			}
			observability.Annotate(r.Context(), slog.String("user_id", userID.String()))
			ctx := context.WithValue(r.Context(), actorKey{}, actor)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func actorFrom(ctx context.Context) kyc.Actor {
	a, ok := ctx.Value(actorKey{}).(kyc.Actor)
	if !ok {
		panic("httpapi: handler reached without authentication")
	}
	return a
}
