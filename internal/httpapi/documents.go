package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/bfischetti/kyc-lifecycle/internal/kyc"
	"github.com/bfischetti/kyc-lifecycle/internal/observability"
	"github.com/bfischetti/kyc-lifecycle/internal/vault"
)

type declareDocumentRequest struct {
	DocType     string `json:"doc_type"`
	ExpiryDate  string `json:"expiry_date"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
	SHA256      string `json:"sha256"`
}

type documentResponse struct {
	ID           uuid.UUID  `json:"id"`
	EntityID     uuid.UUID  `json:"entity_id"`
	DocType      string     `json:"doc_type"`
	ExpiryDate   *string    `json:"expiry_date"`
	ContentType  string     `json:"content_type"`
	SizeBytes    int64      `json:"size_bytes"`
	SHA256       string     `json:"sha256"`
	UploadStatus string     `json:"upload_status"`
	ReviewStatus string     `json:"review_status"`
	CreatedAt    time.Time  `json:"created_at"`
	UploadedAt   *time.Time `json:"uploaded_at"`
}

type uploadResponse struct {
	Method    string            `json:"method"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers"`
	ExpiresAt time.Time         `json:"expires_at"`
}

type declareDocumentResponse struct {
	Document documentResponse `json:"document"`
	Upload   uploadResponse   `json:"upload"`
}

func toDocumentResponse(d kyc.Document) documentResponse {
	var expiry *string
	if d.ExpiryDate != nil {
		s := d.ExpiryDate.Format(kyc.DateLayout)
		expiry = &s
	}
	return documentResponse{
		ID: d.ID, EntityID: d.EntityID, DocType: string(d.Type), ExpiryDate: expiry,
		ContentType: d.ContentType, SizeBytes: d.SizeBytes, SHA256: d.SHA256,
		UploadStatus: string(d.UploadStatus), ReviewStatus: string(d.ReviewStatus),
		CreatedAt: d.CreatedAt, UploadedAt: d.UploadedAt,
	}
}

func toUploadResponse(p vault.UploadPermission) uploadResponse {
	return uploadResponse{Method: p.Method, URL: p.URL, Headers: p.Headers, ExpiresAt: p.ExpiresAt}
}

func (h *handlers) declareDocument(w http.ResponseWriter, r *http.Request) {
	entityID, ok := pathUUID(w, r, h, "id")
	if !ok {
		return
	}
	observability.Annotate(r.Context(), slog.String("entity_id", entityID.String()))

	var req declareDocumentRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, h.log, http.StatusBadRequest, "invalid_json", err.Error(), nil)
		return
	}
	doc, perm, err := h.svc.DeclareDocument(r.Context(), actorFrom(r.Context()), entityID, kyc.NewDocument{
		Type: kyc.DocumentType(req.DocType), ExpiryDate: req.ExpiryDate,
		ContentType: req.ContentType, SizeBytes: req.SizeBytes, SHA256: req.SHA256,
	})
	if err != nil {
		writeAppError(w, r, h.log, err)
		return
	}
	// Never log the upload URL: it embeds the vault key and a live signature.
	observability.Annotate(r.Context(), slog.String("document_id", doc.ID.String()))
	h.log.InfoContext(r.Context(), "upload intent created",
		slog.String("doc_type", string(doc.Type)), slog.Time("upload_expires_at", perm.ExpiresAt))
	writeJSON(w, http.StatusCreated, declareDocumentResponse{Document: toDocumentResponse(doc), Upload: toUploadResponse(perm)})
}

// pathUUID parses a UUID path value; anything else is treated as not found.
func pathUUID(w http.ResponseWriter, r *http.Request, h *handlers, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue(name))
	if err != nil {
		writeError(w, r, h.log, http.StatusNotFound, "not_found", "resource not found", nil)
		return uuid.Nil, false
	}
	return id, true
}

type completeUploadResponse struct {
	Document documentResponse `json:"document"`
	Entity   entityResponse   `json:"entity"`
}

func (h *handlers) completeUpload(w http.ResponseWriter, r *http.Request) {
	entityID, ok := pathUUID(w, r, h, "id")
	if !ok {
		return
	}
	docID, ok := pathUUID(w, r, h, "docID")
	if !ok {
		return
	}
	observability.Annotate(r.Context(),
		slog.String("entity_id", entityID.String()), slog.String("document_id", docID.String()))

	doc, ent, err := h.svc.CompleteUpload(r.Context(), actorFrom(r.Context()), entityID, docID)
	if err != nil {
		writeAppError(w, r, h.log, err)
		return
	}
	h.log.InfoContext(r.Context(), "upload completed",
		slog.String("entity_status", string(ent.Status)), slog.Int("entity_version", ent.Version))
	writeJSON(w, http.StatusOK, completeUploadResponse{Document: toDocumentResponse(doc), Entity: toEntityResponse(ent)})
}
