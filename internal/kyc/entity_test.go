package kyc

import (
	"slices"
	"testing"
)

func TestNewEntityValidate(t *testing.T) {
	tests := []struct {
		name       string
		in         NewEntity
		wantFields []string // invalid fields, in order; nil = valid
		wantName   string   // normalised legal name when valid
	}{
		{"valid individual", NewEntity{EntityTypeIndividual, "Ada Lovelace", "GB"}, nil, "Ada Lovelace"},
		{"valid business", NewEntity{EntityTypeBusiness, "Acme Ltd", "DE"}, nil, "Acme Ltd"},
		{"trims legal name", NewEntity{EntityTypeIndividual, "  Ada  ", "GB"}, nil, "Ada"},
		{"unknown type", NewEntity{"trust", "Acme", "GB"}, []string{"type"}, ""},
		{"empty type", NewEntity{"", "Acme", "GB"}, []string{"type"}, ""},
		{"blank name", NewEntity{EntityTypeBusiness, "   ", "GB"}, []string{"legal_name"}, ""},
		{"lower-case country", NewEntity{EntityTypeBusiness, "Acme", "gb"}, []string{"country"}, ""},
		{"alias country", NewEntity{EntityTypeBusiness, "Acme", "UK"}, []string{"country"}, ""},
		{"numeric region", NewEntity{EntityTypeBusiness, "Acme", "419"}, []string{"country"}, ""},
		{"alpha-3", NewEntity{EntityTypeBusiness, "Acme", "GBR"}, []string{"country"}, ""},
		{"unassigned code", NewEntity{EntityTypeBusiness, "Acme", "ZZ"}, []string{"country"}, ""},
		{"empty country", NewEntity{EntityTypeBusiness, "Acme", ""}, []string{"country"}, ""},
		{"everything wrong", NewEntity{"x", "", "xx"}, []string{"type", "legal_name", "country"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := tt.in
			err := in.Validate()
			if tt.wantFields == nil {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				if in.LegalName != tt.wantName {
					t.Errorf("LegalName = %q, want %q", in.LegalName, tt.wantName)
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

func TestCountryCodeList(t *testing.T) {
	if got := len(countryCodes); got != 249 {
		t.Errorf("assigned ISO-3166-1 alpha-2 codes = %d, want 249", got)
	}
}
