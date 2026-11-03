package screening

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/bfischetti/kyc-lifecycle/internal/kyc"
)

func TestMockOutcomes(t *testing.T) {
	tests := []struct {
		name  string
		check kyc.CheckType
		want  kyc.CheckResult
	}{
		{"Ada Lovelace", kyc.CheckSanctions, kyc.ResultPass},
		{"Ada Lovelace", kyc.CheckPEP, kyc.ResultPass},
		{"Ada Lovelace", kyc.CheckAddress, kyc.ResultPass},
		{"Ivan SANCTIONED", kyc.CheckSanctions, kyc.ResultFail},
		{"Ivan SANCTIONED", kyc.CheckPEP, kyc.ResultPass}, // magic value is check-specific
		{"Minister PEP", kyc.CheckPEP, kyc.ResultManualReview},
		{"Minister PEP", kyc.CheckSanctions, kyc.ResultPass},
		{"Acme BADADDRESS Ltd", kyc.CheckAddress, kyc.ResultFail},
		{"acme badaddress ltd", kyc.CheckAddress, kyc.ResultFail}, // case-insensitive
	}
	for _, tt := range tests {
		out, err := Mock{}.Screen(context.Background(), Subject{Type: kyc.EntityTypeIndividual, LegalName: tt.name, Country: "GB"}, tt.check)
		if err != nil {
			t.Fatalf("%s/%s: %v", tt.name, tt.check, err)
		}
		if out.Result != tt.want || out.Provider != MockName {
			t.Errorf("%s/%s = %s from %s, want %s", tt.name, tt.check, out.Result, out.Provider, tt.want)
		}
		var raw map[string]any
		if err := json.Unmarshal(out.RawResponse, &raw); err != nil || raw["search_id"] == "" {
			t.Errorf("%s/%s: raw response not a vendor-like payload: %s", tt.name, tt.check, out.RawResponse)
		}
	}
}

func TestMockProviderErrors(t *testing.T) {
	_, err := Mock{}.Screen(context.Background(), Subject{LegalName: "PROVIDERDOWN Ltd"}, kyc.CheckSanctions)
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("PROVIDERDOWN: err = %v, want ErrUnavailable", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = Mock{}.Screen(ctx, Subject{LegalName: "TIMEOUT Ltd"}, kyc.CheckSanctions)
	if !errors.Is(err, ErrUnavailable) || time.Since(start) > time.Second {
		t.Errorf("TIMEOUT: err = %v after %v, want ErrUnavailable at the deadline", err, time.Since(start))
	}
}
