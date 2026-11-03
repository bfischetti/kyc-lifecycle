package kyc

import (
	"slices"
	"strings"
	"testing"
)

func TestNewDocumentValidate(t *testing.T) {
	sha := strings.Repeat("ab", 32)
	valid := func() NewDocument {
		return NewDocument{Type: DocPassport, ContentType: "application/pdf", SizeBytes: 1024, SHA256: sha}
	}
	tests := []struct {
		name       string
		mutate     func(*NewDocument)
		wantFields []string
	}{
		{"valid", func(*NewDocument) {}, nil},
		{"valid with expiry", func(d *NewDocument) { d.ExpiryDate = "2030-01-31" }, nil},
		{"upper-case sha is normalised", func(d *NewDocument) { d.SHA256 = strings.ToUpper(sha) }, nil},
		{"jpeg", func(d *NewDocument) { d.ContentType = "image/jpeg" }, nil},
		{"png", func(d *NewDocument) { d.ContentType = "image/png" }, nil},
		{"exactly max size", func(d *NewDocument) { d.SizeBytes = MaxDocumentBytes }, nil},
		{"unknown doc type", func(d *NewDocument) { d.Type = "selfie" }, []string{"doc_type"}},
		{"bad expiry", func(d *NewDocument) { d.ExpiryDate = "31/01/2030" }, []string{"expiry_date"}},
		{"disallowed content type", func(d *NewDocument) { d.ContentType = "application/zip" }, []string{"content_type"}},
		{"zero size", func(d *NewDocument) { d.SizeBytes = 0 }, []string{"size_bytes"}},
		{"negative size", func(d *NewDocument) { d.SizeBytes = -1 }, []string{"size_bytes"}},
		{"over max size", func(d *NewDocument) { d.SizeBytes = MaxDocumentBytes + 1 }, []string{"size_bytes"}},
		{"short sha", func(d *NewDocument) { d.SHA256 = "abc" }, []string{"sha256"}},
		{"non-hex sha", func(d *NewDocument) { d.SHA256 = strings.Repeat("zz", 32) }, []string{"sha256"}},
		{"everything wrong", func(d *NewDocument) { *d = NewDocument{ExpiryDate: "x"} }, []string{"doc_type", "expiry_date", "content_type", "size_bytes", "sha256"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := valid()
			tt.mutate(&d)
			err := d.Validate()
			if tt.wantFields == nil {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				if d.SHA256 != sha {
					t.Errorf("SHA256 = %q, want normalised %q", d.SHA256, sha)
				}
				if d.ExpiryDate != "" && d.Expiry() == nil {
					t.Error("expiry not parsed")
				}
				return
			}
			ve, ok := AsValidationError(err)
			if !ok {
				t.Fatalf("Validate() = %v, want *ValidationError", err)
			}
			var got []string
			for _, f := range ve.Fields {
				got = append(got, f.Field)
			}
			if !slices.Equal(got, tt.wantFields) {
				t.Errorf("invalid fields = %v, want %v", got, tt.wantFields)
			}
		})
	}
}
