package kyc

import "testing"

func TestMatchesMagic(t *testing.T) {
	pdf := []byte("%PDF-1.7\n...")
	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0}
	jpg := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0}
	exe := []byte("MZ\x90\x00")
	tests := []struct {
		ct   string
		head []byte
		want bool
	}{
		{"application/pdf", pdf, true},
		{"image/png", png, true},
		{"image/jpeg", jpg, true},
		{"application/pdf", exe, false},
		{"application/pdf", png, false},
		{"image/png", pdf, false},
		{"image/jpeg", []byte{0xFF, 0xD8}, false},
		{"application/pdf", nil, false},
		{"text/plain", []byte("hello"), false},
	}
	for _, tt := range tests {
		if got := MatchesMagic(tt.ct, tt.head); got != tt.want {
			t.Errorf("MatchesMagic(%q, %q) = %v, want %v", tt.ct, tt.head, got, tt.want)
		}
	}
}
