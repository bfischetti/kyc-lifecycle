package kyc

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// CheckType is the kind of screening.
type CheckType string

const (
	CheckSanctions CheckType = "sanctions_screening"
	CheckPEP       CheckType = "pep_screening"
	CheckAddress   CheckType = "address_verification"
)

func (t CheckType) Valid() bool {
	return t == CheckSanctions || t == CheckPEP || t == CheckAddress
}

// CheckResult is a screening answer. A fail is a Hard Fail; manual_review
// is a Manual Review Result.
type CheckResult string

const (
	ResultPass         CheckResult = "pass"
	ResultFail         CheckResult = "fail"
	ResultManualReview CheckResult = "manual_review"
)

// Check is one screening answer from a Screening Provider. Immutable.
type Check struct {
	ID          uuid.UUID
	EntityID    uuid.UUID
	Type        CheckType
	Provider    string
	Result      CheckResult
	RawResponse json.RawMessage // provider payload; sensitive
	RunBy       uuid.UUID
	CreatedAt   time.Time
}
