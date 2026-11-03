// Package screening is the boundary to Screening Providers: the external
// vendors that perform sanctions, PEP and address checks.
package screening

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/bfischetti/kyc-lifecycle/internal/kyc"
)

// ErrUnavailable is a Provider Error: no screening answer was obtained
// (timeout, outage). It is never a Check result and must never reject anyone.
var ErrUnavailable = errors.New("screening provider unavailable")

// Subject is what is screened.
type Subject struct {
	Type      kyc.EntityType
	LegalName string
	Country   string
}

// Outcome is a provider's answer.
type Outcome struct {
	Provider    string
	Result      kyc.CheckResult
	RawResponse json.RawMessage
}

// Provider screens a Subject. Implementations return ErrUnavailable (possibly
// wrapped) when they cannot answer, and must honour ctx cancellation.
type Provider interface {
	Screen(ctx context.Context, s Subject, check kyc.CheckType) (Outcome, error)
}
