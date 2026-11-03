package kyc

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

// DocumentType is the kind of proof a Document is.
type DocumentType string

const (
	DocPassport             DocumentType = "passport"
	DocNationalID           DocumentType = "national_id"
	DocBusinessRegistration DocumentType = "business_registration"
	DocProofOfAddress       DocumentType = "proof_of_address"
)

func (t DocumentType) Valid() bool {
	switch t {
	case DocPassport, DocNationalID, DocBusinessRegistration, DocProofOfAddress:
		return true
	}
	return false
}

// UploadStatus says whether a Document's bytes are in the Evidence Vault:
// an Upload Intent is awaiting upload, a Completed Upload is uploaded, and an
// Expired Upload is expired.
type UploadStatus string

const (
	UploadAwaiting UploadStatus = "awaiting_upload"
	UploadComplete UploadStatus = "uploaded"
	UploadExpired  UploadStatus = "expired"
)

// DocumentReviewStatus is decided by the outcome of the Review Cycle.
type DocumentReviewStatus string

const (
	ReviewPending  DocumentReviewStatus = "pending"
	ReviewAccepted DocumentReviewStatus = "accepted"
	ReviewRejected DocumentReviewStatus = "rejected"
)

// MaxDocumentBytes is the largest Document accepted.
const MaxDocumentBytes = 10 << 20 // 10 MiB

// AllowedContentTypes are the file types a Document may be.
var AllowedContentTypes = []string{"application/pdf", "image/jpeg", "image/png"}

// DateLayout is the wire format for calendar dates such as expiry dates.
const DateLayout = "2006-01-02"

// Document is a piece of proof an Entity submitted. Its bytes live in the
// Evidence Vault; the vault key is deliberately not part of the domain type.
type Document struct {
	ID           uuid.UUID
	EntityID     uuid.UUID
	Type         DocumentType
	ExpiryDate   *time.Time // calendar date, UTC midnight
	ContentType  string
	SizeBytes    int64
	SHA256       string // lowercase hex
	UploadStatus UploadStatus
	ReviewStatus DocumentReviewStatus
	CreatedAt    time.Time
	UploadedAt   *time.Time
}

// NewDocument is what a caller declares when creating an Upload Intent.
type NewDocument struct {
	Type        DocumentType
	ExpiryDate  string // optional, YYYY-MM-DD
	ContentType string
	SizeBytes   int64
	SHA256      string // hex

	expiry *time.Time
}

// Expiry returns the parsed expiry date after a successful Validate.
func (n *NewDocument) Expiry() *time.Time { return n.expiry }

var hexSHA256 = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Validate normalises n in place and returns a *ValidationError listing every
// problem, or nil.
func (n *NewDocument) Validate() error {
	n.SHA256 = strings.ToLower(strings.TrimSpace(n.SHA256))
	n.ContentType = strings.ToLower(strings.TrimSpace(n.ContentType))

	var fields []FieldError
	if !n.Type.Valid() {
		fields = append(fields, FieldError{"doc_type", "must be one of passport, national_id, business_registration, proof_of_address"})
	}
	if n.ExpiryDate != "" {
		d, err := time.Parse(DateLayout, n.ExpiryDate)
		if err != nil {
			fields = append(fields, FieldError{"expiry_date", "must be a date in YYYY-MM-DD format"})
		} else {
			n.expiry = &d
		}
	}
	if !contentTypeAllowed(n.ContentType) {
		fields = append(fields, FieldError{"content_type", "must be one of " + strings.Join(AllowedContentTypes, ", ")})
	}
	if n.SizeBytes <= 0 || n.SizeBytes > MaxDocumentBytes {
		fields = append(fields, FieldError{"size_bytes", fmt.Sprintf("must be between 1 and %d", MaxDocumentBytes)})
	}
	if !hexSHA256.MatchString(n.SHA256) {
		fields = append(fields, FieldError{"sha256", "must be the file's SHA-256 as 64 hex characters"})
	}
	if len(fields) > 0 {
		return &ValidationError{Fields: fields}
	}
	return nil
}

func contentTypeAllowed(ct string) bool {
	for _, a := range AllowedContentTypes {
		if ct == a {
			return true
		}
	}
	return false
}
