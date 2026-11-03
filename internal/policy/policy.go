// Package policy is the single authority on what each Role may do (ADR-0001).
// Handlers and use cases ask it; they never compare roles themselves.
// Postgres row-level security is an independent backstop, not a substitute.
package policy

import "github.com/bfischetti/kyc-lifecycle/internal/kyc"

// Action is an operation a caller can attempt.
type Action string

const (
	CreateEntity    Action = "entity.create"
	ReadEntity      Action = "entity.read"
	DeclareDocument Action = "document.declare"
)

var grants = map[Action]map[kyc.Role]bool{
	CreateEntity: {kyc.RoleComplianceOfficer: true, kyc.RoleSupportAgent: true},
	ReadEntity:   {kyc.RoleComplianceOfficer: true, kyc.RoleSupportAgent: true, kyc.RoleSystem: true},
	// Support agents onboard Entities, so they attach Documents too.
	DeclareDocument: {kyc.RoleComplianceOfficer: true, kyc.RoleSupportAgent: true},
}

// Allowed reports whether role may perform action. Unknown actions and roles
// are denied.
func Allowed(role kyc.Role, action Action) bool {
	return grants[action][role]
}
