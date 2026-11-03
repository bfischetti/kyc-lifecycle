package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/bfischetti/kyc-lifecycle/internal/observability"
)

// documentContent is the only route that leads to a Document's bytes. It
// records the Evidence Access, then redirects to a short-lived download
// from the Evidence Vault, so the bytes never pass through this service.
func (h *handlers) documentContent(w http.ResponseWriter, r *http.Request) {
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

	perm, err := h.svc.AccessEvidence(r.Context(), actorFrom(r.Context()), entityID, docID)
	if err != nil {
		writeAppError(w, r, h.log, err)
		return
	}
	// Never log the URL: it embeds the vault key and a live signature.
	h.log.InfoContext(r.Context(), "evidence accessed", slog.Time("url_expires_at", perm.ExpiresAt))

	// The redirect carries a bearer URL: keep it out of caches and out of
	// the Referer header of whatever the browser loads next.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, perm.URL, http.StatusFound)
}
