package kyc

import (
	"sort"
	"strings"
	"time"
)

// RequiredChecks are the Check Types an Entity of type t must pass before
// approval. A legal entity is not itself a politically exposed person, so a
// business needs address verification instead of PEP screening.
func RequiredChecks(t EntityType) []CheckType {
	switch t {
	case EntityTypeIndividual:
		return []CheckType{CheckSanctions, CheckPEP}
	case EntityTypeBusiness:
		return []CheckType{CheckSanctions, CheckAddress}
	}
	return nil
}

// RequiredDocuments are the alternatives, any one of which an Entity of
// type t must have as an unexpired Completed Upload before approval. Proof
// of address is never required.
func RequiredDocuments(t EntityType) []DocumentType {
	switch t {
	case EntityTypeIndividual:
		return []DocumentType{DocPassport, DocNationalID}
	case EntityTypeBusiness:
		return []DocumentType{DocBusinessRegistration}
	}
	return nil
}

// EffectiveResults returns, per Check Type, the result of the most recent
// Check in checks. Callers pass only the current Review Cycle's Checks.
func EffectiveResults(checks []Check) map[CheckType]CheckResult {
	latest := map[CheckType]Check{}
	for _, c := range checks {
		if cur, ok := latest[c.Type]; !ok || !c.CreatedAt.Before(cur.CreatedAt) {
			latest[c.Type] = c
		}
	}
	out := make(map[CheckType]CheckResult, len(latest))
	for t, c := range latest {
		out[t] = c.Result
	}
	return out
}

// Unmet is one approval requirement that is not satisfied.
type Unmet struct {
	Requirement string `json:"requirement"` // e.g. "check:pep_screening", "document:passport|national_id"
	Problem     string `json:"problem"`     // e.g. "missing", "fail", "manual_review", "expired"
}

// ApprovalInput is everything the approval decision depends on, restricted
// to the current Review Cycle.
type ApprovalInput struct {
	EntityType EntityType
	Checks     []Check    // this cycle's Checks
	Documents  []Document // this cycle's Completed Uploads
	Today      time.Time  // the decision date
}

// UnmetForApproval lists every reason a Compliance Approval is not allowed
// yet; empty means it may proceed. There is no override.
func UnmetForApproval(in ApprovalInput) []Unmet {
	var unmet []Unmet

	effective := EffectiveResults(in.Checks)
	for _, ct := range RequiredChecks(in.EntityType) {
		switch r, ok := effective[ct]; {
		case !ok:
			unmet = append(unmet, Unmet{"check:" + string(ct), "missing"})
		case r != ResultPass:
			unmet = append(unmet, Unmet{"check:" + string(ct), string(r)})
		}
	}

	required := RequiredDocuments(in.EntityType)
	requirement := documentRequirement(required)

	today := truncateToDate(in.Today)
	found, expired := false, false
	for _, d := range in.Documents {
		if !contains(required, d.Type) || d.UploadStatus != UploadComplete {
			continue
		}
		if d.ExpiryDate != nil && d.ExpiryDate.Before(today) {
			expired = true
			continue
		}
		found = true
	}
	switch {
	case found:
	case expired:
		unmet = append(unmet, Unmet{requirement, "expired"})
	default:
		unmet = append(unmet, Unmet{requirement, "missing"})
	}

	sort.SliceStable(unmet, func(i, j int) bool { return unmet[i].Requirement < unmet[j].Requirement })
	return unmet
}

func documentRequirement(required []DocumentType) string {
	names := make([]string, len(required))
	for i, d := range required {
		names[i] = string(d)
	}
	return "document:" + strings.Join(names, "|")
}

// ChecklistItem is one approval requirement and where it stands. Exactly one of
// Check and Documents is set.
type ChecklistItem struct {
	Check     CheckType      // a Required Check
	Documents []DocumentType // the Required Documents, any one of which will do
	Problem   string         // as in Unmet; empty means the requirement is met
}

// Checklist lays out every approval requirement for an Entity of type t. What
// counts as met comes only from unmet, the list UnmetForApproval produced, so
// the checklist can't disagree with the approval decision.
func Checklist(t EntityType, unmet []Unmet) []ChecklistItem {
	problems := make(map[string]string, len(unmet))
	for _, u := range unmet {
		problems[u.Requirement] = u.Problem
	}
	var items []ChecklistItem
	for _, ct := range RequiredChecks(t) {
		items = append(items, ChecklistItem{Check: ct, Problem: problems["check:"+string(ct)]})
	}
	required := RequiredDocuments(t)
	return append(items, ChecklistItem{Documents: required, Problem: problems[documentRequirement(required)]})
}

func truncateToDate(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func contains(list []DocumentType, t DocumentType) bool {
	for _, x := range list {
		if x == t {
			return true
		}
	}
	return false
}
