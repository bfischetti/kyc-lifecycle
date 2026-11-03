package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
)

func TestScopeAttrsAppearOnEveryLine(t *testing.T) {
	var buf bytes.Buffer
	log := NewLogger(&buf, "info")

	ctx := NewScope(context.Background(), slog.String("request_id", "req-1"))
	inner, cancel := context.WithCancel(ctx) // derived contexts share the scope
	defer cancel()
	Annotate(inner, slog.String("entity_id", "ent-1"))

	log.InfoContext(inner, "inner line")
	log.With("k", "v").InfoContext(ctx, "outer line, logged after the annotation")

	lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	for _, line := range lines {
		var rec map[string]any
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("line is not JSON: %s", line)
		}
		if rec["request_id"] != "req-1" || rec["entity_id"] != "ent-1" {
			t.Errorf("missing scope attrs: %s", line)
		}
	}
}

func TestAnnotateWithoutScopeIsNoop(t *testing.T) {
	Annotate(context.Background(), slog.String("k", "v")) // must not panic
}
