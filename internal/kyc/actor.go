package kyc

import "github.com/google/uuid"

// Role is what an actor is allowed to do; see Actors in CONTEXT.md.
type Role string

const (
	RoleComplianceOfficer Role = "compliance_officer"
	RoleSupportAgent      Role = "support_agent"
	RoleSystem            Role = "system"
)

// Actor is the authenticated caller of an operation.
type Actor struct {
	UserID uuid.UUID
	Role   Role
}

// SystemUserID is the seeded user that automated work is attributed to.
var SystemUserID = uuid.MustParse("00000000-0000-0000-0000-000000000003")
