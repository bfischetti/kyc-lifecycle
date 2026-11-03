package outbox

import (
	"testing"
	"time"
)

func TestBackoff(t *testing.T) {
	o := Options{BaseBackoff: time.Second, MaxBackoff: 5 * time.Minute}
	tests := []struct {
		attempts int
		want     time.Duration
	}{
		{1, 1 * time.Second},
		{2, 2 * time.Second},
		{3, 4 * time.Second},
		{8, 128 * time.Second},
		{9, 256 * time.Second},
		{10, 5 * time.Minute}, // capped
		{50, 5 * time.Minute},
	}
	for _, tt := range tests {
		if got := o.Backoff(tt.attempts); got != tt.want {
			t.Errorf("Backoff(%d) = %v, want %v", tt.attempts, got, tt.want)
		}
	}
}
