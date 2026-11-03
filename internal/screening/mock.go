package screening

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/bfischetti/kyc-lifecycle/internal/kyc"
)

// MockName is the provider name recorded on Checks from Mock.
const MockName = "mock-provider"

// Mock is a deterministic stand-in for a real vendor, driven by magic values
// in the legal name, like vendor sandboxes are:
//
//	SANCTIONED    sanctions screening fails
//	PEP           PEP screening needs manual review
//	BADADDRESS    address verification fails
//	TIMEOUT       the provider hangs until the caller gives up
//	PROVIDERDOWN  the provider is unavailable
//
// Anything else passes.
type Mock struct{}

func (Mock) Screen(ctx context.Context, s Subject, check kyc.CheckType) (Outcome, error) {
	name := strings.ToUpper(s.LegalName)
	switch {
	case strings.Contains(name, "PROVIDERDOWN"):
		return Outcome{}, fmt.Errorf("%w: 503 from upstream", ErrUnavailable)
	case strings.Contains(name, "TIMEOUT"):
		<-ctx.Done()
		return Outcome{}, fmt.Errorf("%w: %w", ErrUnavailable, ctx.Err())
	}

	result := kyc.ResultPass
	var hits []hit
	switch {
	case check == kyc.CheckSanctions && strings.Contains(name, "SANCTIONED"):
		result = kyc.ResultFail
		hits = []hit{{Name: s.LegalName, Score: 0.97, Lists: []string{"OFAC SDN", "UN Consolidated"}, MatchType: "name_exact"}}
	case check == kyc.CheckPEP && strings.Contains(name, "PEP"):
		result = kyc.ResultManualReview
		hits = []hit{{Name: s.LegalName, Score: 0.81, Lists: []string{"PEP Class 1"}, MatchType: "name_fuzzy"}}
	case check == kyc.CheckAddress && strings.Contains(name, "BADADDRESS"):
		result = kyc.ResultFail
	}

	raw, err := json.Marshal(response{
		SearchID:    uuid.NewString(),
		CheckType:   string(check),
		MatchStatus: matchStatus(result),
		Subject:     subject{Name: s.LegalName, Country: s.Country, EntityType: string(s.Type)},
		Hits:        hits,
		TotalHits:   len(hits),
	})
	if err != nil {
		return Outcome{}, err
	}
	return Outcome{Provider: MockName, Result: result, RawResponse: raw}, nil
}

func matchStatus(r kyc.CheckResult) string {
	switch r {
	case kyc.ResultFail:
		return "true_positive"
	case kyc.ResultManualReview:
		return "potential_match"
	default:
		return "no_match"
	}
}

// response resembles a real screening vendor's payload.
type response struct {
	SearchID    string  `json:"search_id"`
	CheckType   string  `json:"check_type"`
	MatchStatus string  `json:"match_status"`
	Subject     subject `json:"subject"`
	TotalHits   int     `json:"total_hits"`
	Hits        []hit   `json:"hits"`
}

type subject struct {
	Name       string `json:"name"`
	Country    string `json:"country"`
	EntityType string `json:"entity_type"`
}

type hit struct {
	Name      string   `json:"name"`
	Score     float64  `json:"score"`
	Lists     []string `json:"lists"`
	MatchType string   `json:"match_type"`
}
