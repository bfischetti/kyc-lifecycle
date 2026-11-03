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
		{"entity.delete", co, false},
	}
	for _, tt := range tests {
		if got := Allowed(tt.role, tt.action); got != tt.want {
			t.Errorf("Allowed(%q, %q) = %v, want %v", tt.role, tt.action, got, tt.want)
		}
	}
}
