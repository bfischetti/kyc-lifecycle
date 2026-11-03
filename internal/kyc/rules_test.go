package kyc

import (
	"slices"
	"testing"
	"time"
)

var today = time.Date(2026, 11, 3, 15, 0, 0, 0, time.UTC)

func chk(t CheckType, r CheckResult, minutesAgo int) Check {
	return Check{Type: t, Result: r, CreatedAt: today.Add(-time.Duration(minutesAgo) * time.Minute)}
}

func doc(t DocumentType, expiry string) Document {
	d := Document{Type: t, UploadStatus: UploadComplete}
	if expiry != "" {
		e, _ := time.Parse(DateLayout, expiry)
		d.ExpiryDate = &e
	}
	return d
}

func TestUnmetForApproval(t *testing.T) {
	passAll := func(types ...CheckType) []Check {
		var cs []Check
		for _, ct := range types {
			cs = append(cs, chk(ct, ResultPass, 5))
		}
		return cs
	}
	tests := []struct {
		name string
		in   ApprovalInput
		want []Unmet
	}{
		{
			name: "individual with everything passes",
			in:   ApprovalInput{EntityTypeIndividual, passAll(CheckSanctions, CheckPEP), []Document{doc(DocPassport, "")}, today},
		},
		{
			name: "national ID satisfies an individual",
			in:   ApprovalInput{EntityTypeIndividual, passAll(CheckSanctions, CheckPEP), []Document{doc(DocNationalID, "2030-01-01")}, today},
		},
		{
			name: "business with everything passes",
			in:   ApprovalInput{EntityTypeBusiness, passAll(CheckSanctions, CheckAddress), []Document{doc(DocBusinessRegistration, "")}, today},
		},
		{
			name: "nothing at all lists every requirement",
			in:   ApprovalInput{EntityTypeIndividual, nil, nil, today},
			want: []Unmet{{"check:pep_screening", "missing"}, {"check:sanctions_screening", "missing"}, {"document:passport|national_id", "missing"}},
		},
		{
			name: "failed and manual-review checks block approval",
			in: ApprovalInput{EntityTypeIndividual,
				[]Check{chk(CheckSanctions, ResultFail, 5), chk(CheckPEP, ResultManualReview, 5)},
				[]Document{doc(DocPassport, "")}, today},
			want: []Unmet{{"check:pep_screening", "manual_review"}, {"check:sanctions_screening", "fail"}},
		},
		{
			name: "latest result counts: fail then pass approves",
			in: ApprovalInput{EntityTypeIndividual,
				[]Check{chk(CheckSanctions, ResultFail, 10), chk(CheckSanctions, ResultPass, 1), chk(CheckPEP, ResultPass, 5)},
				[]Document{doc(DocPassport, "")}, today},
		},
		{
			name: "latest result counts: pass then manual review blocks",
			in: ApprovalInput{EntityTypeIndividual,
				[]Check{chk(CheckSanctions, ResultPass, 5), chk(CheckPEP, ResultPass, 10), chk(CheckPEP, ResultManualReview, 1)},
				[]Document{doc(DocPassport, "")}, today},
			want: []Unmet{{"check:pep_screening", "manual_review"}},
		},
		{
			name: "a business needs address verification, not PEP",
			in:   ApprovalInput{EntityTypeBusiness, passAll(CheckSanctions, CheckPEP), []Document{doc(DocBusinessRegistration, "")}, today},
			want: []Unmet{{"check:address_verification", "missing"}},
		},
		{
			name: "a passport does not satisfy a business",
			in:   ApprovalInput{EntityTypeBusiness, passAll(CheckSanctions, CheckAddress), []Document{doc(DocPassport, "")}, today},
			want: []Unmet{{"document:business_registration", "missing"}},
		},
		{
			name: "proof of address alone is not enough",
			in:   ApprovalInput{EntityTypeIndividual, passAll(CheckSanctions, CheckPEP), []Document{doc(DocProofOfAddress, "")}, today},
			want: []Unmet{{"document:passport|national_id", "missing"}},
		},
		{
			name: "expired required document",
			in:   ApprovalInput{EntityTypeIndividual, passAll(CheckSanctions, CheckPEP), []Document{doc(DocPassport, "2026-11-02")}, today},
			want: []Unmet{{"document:passport|national_id", "expired"}},
		},
		{
			name: "a document expiring today is still valid",
			in:   ApprovalInput{EntityTypeIndividual, passAll(CheckSanctions, CheckPEP), []Document{doc(DocPassport, "2026-11-03")}, today},
		},
		{
			name: "one valid document is enough even if another expired",
			in: ApprovalInput{EntityTypeIndividual, passAll(CheckSanctions, CheckPEP),
				[]Document{doc(DocPassport, "2020-01-01"), doc(DocNationalID, "")}, today},
		},
		{
			name: "an upload that is not completed does not count",
			in: ApprovalInput{EntityTypeIndividual, passAll(CheckSanctions, CheckPEP),
				[]Document{{Type: DocPassport, UploadStatus: UploadAwaiting}}, today},
			want: []Unmet{{"document:passport|national_id", "missing"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := UnmetForApproval(tt.in)
			if !slices.Equal(got, tt.want) {
				t.Errorf("UnmetForApproval() = %v, want %v", got, tt.want)
			}
		})
	}
}
