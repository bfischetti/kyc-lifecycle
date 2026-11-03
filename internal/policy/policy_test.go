package policy

import (
	"testing"

	"github.com/bfischetti/kyc-lifecycle/internal/kyc"
)

func TestAllowed(t *testing.T) {
	const (
		co = kyc.RoleComplianceOfficer
		sa = kyc.RoleSupportAgent
		sy = kyc.RoleSystem
	)
	tests := []struct {
		action Action
		role   kyc.Role
		want   bool
	}{
		{CreateEntity, co, true},
		{CreateEntity, sa, true},
		{CreateEntity, sy, false},
		{ReadEntity, co, true},
		{ReadEntity, sa, true},
		{ReadEntity, sy, true},
		{ReadEntity, "auditor", false},
		{DeclareDocument, co, true},
		{DeclareDocument, sa, true},
		{DeclareDocument, sy, false},
		{CompleteUpload, co, true},
		{CompleteUpload, sa, true},
		{CompleteUpload, sy, false},
		{ReadHistory, co, true},
		{ReadHistory, sa, false},
		{ReadHistory, sy, false},
		{RunCheck, co, true},
		{RunCheck, sa, false},
		{RunCheck, sy, true},
		{"entity.delete", co, false},
	}
	for _, tt := range tests {
		if got := Allowed(tt.role, tt.action); got != tt.want {
			t.Errorf("Allowed(%q, %q) = %v, want %v", tt.role, tt.action, got, tt.want)
		}
	}
}

func TestCanSee(t *testing.T) {
	tests := []struct {
		role  kyc.Role
		field Field
		want  bool
	}{
		{kyc.RoleComplianceOfficer, CheckRawResponse, true},
		{kyc.RoleSystem, CheckRawResponse, true},
		{kyc.RoleSupportAgent, CheckRawResponse, false},
		{"auditor", CheckRawResponse, false},
		{kyc.RoleComplianceOfficer, "document.vault_key", false},
	}
	for _, tt := range tests {
		if got := CanSee(tt.role, tt.field); got != tt.want {
			t.Errorf("CanSee(%q, %q) = %v, want %v", tt.role, tt.field, got, tt.want)
		}
	}
}
