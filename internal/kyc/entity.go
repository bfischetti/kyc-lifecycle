// Package kyc is the domain model of the KYC/KYB lifecycle: Entities, their
// Verification Status, and the rules that govern them. It performs no I/O.
//
// Vocabulary follows CONTEXT.md at the repository root.
package kyc

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// EntityType distinguishes a person from a legal entity.
type EntityType string

const (
	EntityTypeIndividual EntityType = "individual"
	EntityTypeBusiness   EntityType = "business"
)

func (t EntityType) Valid() bool {
	return t == EntityTypeIndividual || t == EntityTypeBusiness
}

// Status is an Entity's Verification Status.
type Status string

const (
	StatusUnverified    Status = "unverified"
	StatusPendingReview Status = "pending_review"
	StatusVerified      Status = "verified"
	StatusRejected      Status = "rejected"
)

// Entity is the merchant or person being verified.
type Entity struct {
	ID        uuid.UUID
	Type      EntityType
	LegalName string
	Country   string // ISO-3166 alpha-2
	Status    Status
	Version   int // Entity Version: incremented on every status change
	CreatedAt time.Time
	UpdatedAt time.Time
}

// NewEntity is the caller-supplied data needed to create an Entity.
type NewEntity struct {
	Type      EntityType
	LegalName string
	Country   string
}

// FieldError describes one invalid input field.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// ValidationError collects every invalid field so callers can fix them in one go.
type ValidationError struct {
	Fields []FieldError
}

func (e *ValidationError) Error() string {
	parts := make([]string, len(e.Fields))
	for i, f := range e.Fields {
		parts[i] = f.Field + ": " + f.Message
	}
	return "invalid input: " + strings.Join(parts, "; ")
}

// AsValidationError reports whether err is a *ValidationError.
func AsValidationError(err error) (*ValidationError, bool) {
	var ve *ValidationError
	ok := errors.As(err, &ve)
	return ve, ok
}

// Validate normalises n in place and returns a *ValidationError listing every
// problem, or nil.
func (n *NewEntity) Validate() error {
	n.LegalName = strings.TrimSpace(n.LegalName)

	var fields []FieldError
	if !n.Type.Valid() {
		fields = append(fields, FieldError{"type", fmt.Sprintf("must be %q or %q", EntityTypeIndividual, EntityTypeBusiness)})
	}
	if n.LegalName == "" {
		fields = append(fields, FieldError{"legal_name", "must not be blank"})
	}
	if !IsCountryCode(n.Country) {
		fields = append(fields, FieldError{"country", "must be an ISO-3166 alpha-2 country code, e.g. \"GB\""})
	}
	if len(fields) > 0 {
		return &ValidationError{Fields: fields}
	}
	return nil
}

// StatusChange is one audit record: a single change of Verification Status.
type StatusChange struct {
	ID            uuid.UUID
	EntityID      uuid.UUID
	From          Status
	To            Status
	EntityVersion int // the version this change produced
	Reason        string
	ChangedBy     uuid.UUID
	ChangedAt     time.Time
}
