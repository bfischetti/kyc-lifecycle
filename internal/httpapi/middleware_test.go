package httpapi

import "testing"

// The latency histogram's labels must come from small fixed sets, whatever
// the client sends.
func TestMetricLabelsAreBounded(t *testing.T) {
	routes := []struct{ pattern, want string }{
		{"GET /entities/{id}", "/entities/{id}"},
		{"POST /entities/{id}/documents/{docID}/complete", "/entities/{id}/documents/{docID}/complete"},
		{"/healthz", "/healthz"},
		{"", "unmatched"},
	}
	for _, tt := range routes {
		if got := routeLabel(tt.pattern); got != tt.want {
			t.Errorf("routeLabel(%q) = %q, want %q", tt.pattern, got, tt.want)
		}
	}

	methods := []struct{ method, want string }{
		{"GET", "GET"},
		{"POST", "POST"},
		{"DELETE", "DELETE"},
		{"BREW", "other"},
		{"get", "other"},
	}
	for _, tt := range methods {
		if got := methodLabel(tt.method); got != tt.want {
			t.Errorf("methodLabel(%q) = %q, want %q", tt.method, got, tt.want)
		}
	}
}
