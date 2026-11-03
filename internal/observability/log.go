// Package observability configures structured logging (and, later, metrics).
package observability

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
)

type scopeKey struct{}

// scope is a mutable, per-request set of log attributes. It is mutable so
// that attributes learned deep inside a handler (e.g. entity_id) also appear
// on lines logged by outer middleware afterwards (e.g. the access log).
type scope struct {
	mu    sync.Mutex
	attrs []slog.Attr
}

// NewScope returns a context carrying a fresh attribute scope, seeded with
// attrs. Call it once per unit of work (an HTTP request, a worker job).
func NewScope(ctx context.Context, attrs ...slog.Attr) context.Context {
	return context.WithValue(ctx, scopeKey{}, &scope{attrs: attrs})
}

// Annotate adds attrs to the scope in ctx, so every subsequent line logged
// with any context derived from the same scope carries them. Without a
// scope it is a no-op.
func Annotate(ctx context.Context, attrs ...slog.Attr) {
	if s, ok := ctx.Value(scopeKey{}).(*scope); ok {
		s.mu.Lock()
		s.attrs = append(s.attrs, attrs...)
		s.mu.Unlock()
	}
}

func scopeAttrs(ctx context.Context) []slog.Attr {
	s, ok := ctx.Value(scopeKey{}).(*scope)
	if !ok {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]slog.Attr(nil), s.attrs...)
}

// contextHandler adds the scope's attributes to every record.
type contextHandler struct{ slog.Handler }

func (h contextHandler) Handle(ctx context.Context, r slog.Record) error {
	r.AddAttrs(scopeAttrs(ctx)...)
	return h.Handler.Handle(ctx, r)
}

func (h contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return contextHandler{h.Handler.WithAttrs(attrs)}
}

func (h contextHandler) WithGroup(name string) slog.Handler {
	return contextHandler{h.Handler.WithGroup(name)}
}

// NewLogger returns a JSON logger at the given level ("debug", "info",
// "warn", "error"; default info) that adds scope attributes to every line.
func NewLogger(w io.Writer, level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(contextHandler{slog.NewJSONHandler(w, &slog.HandlerOptions{Level: lvl})})
}
