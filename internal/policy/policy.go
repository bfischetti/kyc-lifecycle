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
	CompleteUpload  Action = "document.complete"
	ReadHistory     Action = "entity.history"
	RunCheck        Action = "check.run"
	Review          Action = "entity.review"
	AccessEvidence  Action = "document.access"
)

var grants = map[Action]map[kyc.Role]bool{
	CreateEntity: {kyc.RoleComplianceOfficer: true, kyc.RoleSupportAgent: true},
	ReadEntity:   {kyc.RoleComplianceOfficer: true, kyc.RoleSupportAgent: true, kyc.RoleSystem: true},
	// Support agents onboard Entities, so they attach Documents too.
	DeclareDocument: {kyc.RoleComplianceOfficer: true, kyc.RoleSupportAgent: true},
	CompleteUpload:  {kyc.RoleComplianceOfficer: true, kyc.RoleSupportAgent: true},
	// The audit trail is for compliance only.
	ReadHistory: {kyc.RoleComplianceOfficer: true},
	// Screening is for compliance and automation; support agents only onboard.
	RunCheck: {kyc.RoleComplianceOfficer: true, kyc.RoleSystem: true},
	// Decisions are made by people with compliance authority, never by automation.
	Review: {kyc.RoleComplianceOfficer: true},
	// Opening a Document's contents is an Evidence Access: always a person,
	// always compliance. Automation never needs the bytes.
	AccessEvidence: {kyc.RoleComplianceOfficer: true},
}

// Allowed reports whether role may perform action. Unknown actions and roles
// are denied.
func Allowed(role kyc.Role, action Action) bool {
	return grants[action][role]
}

// Field is a piece of data whose visibility depends on the caller's role.
type Field string

const (
	// CheckRawResponse is a Screening Provider's raw payload.
	CheckRawResponse Field = "check.raw_response"
)

// Evidence Vault keys are not listed: no role ever sees them through the API.
var visibility = map[Field]map[kyc.Role]bool{
	CheckRawResponse: {kyc.RoleComplianceOfficer: true, kyc.RoleSystem: true},
}

// CanSee reports whether role may see field. A support agent gets the
// Redacted View. Unknown fields and roles are hidden.
func CanSee(role kyc.Role, field Field) bool {
	return visibility[field][role]
}
