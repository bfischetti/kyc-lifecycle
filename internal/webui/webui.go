// Package webui is the server-rendered back-office UI (ADR-0003). Its handlers
// call app.Service directly, so policy and row-level security apply exactly as
// they do for the JSON API. It contains no business rules.
package webui

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/bfischetti/kyc-lifecycle/internal/app"
	"github.com/bfischetti/kyc-lifecycle/internal/kyc"
	"github.com/bfischetti/kyc-lifecycle/internal/observability"
	"github.com/bfischetti/kyc-lifecycle/internal/policy"
)

const (
	cookieName = "kyc_ui_user"
	maxForm    = 1 << 16
)

// The people a browser may sign in as, by their seeded user ids. The System
// user is deliberately absent: it isn't a person.
var signInUsers = map[kyc.Role]uuid.UUID{
	kyc.RoleSupportAgent:      uuid.MustParse("00000000-0000-0000-0000-000000000002"),
	kyc.RoleComplianceOfficer: uuid.MustParse("00000000-0000-0000-0000-000000000001"),
}

// Printable ASCII, matching what the JSON API accepts as an Idempotency-Key.
var validIdempotencyKey = regexp.MustCompile(`^[\x21-\x7E]{1,255}$`)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

var funcs = template.FuncMap{
	"ago":        func(t time.Time) string { return ago(time.Since(t)) },
	"roleName":   roleName,
	"statusName": statusName,
	"typeName":   typeName,
	"docName":    docTypeName,
	"checkName":  checkTypeName,
	"resultName": resultName,
	"docsName":   documentsName,
	"problem":    problemName,
	"newKey":     uuid.NewString,
	"pretty":     prettyJSON,
	"uploadName": uploadStatusName,
	"reviewName": reviewStatusName,
	"date":       func(t time.Time) string { return t.Format(kyc.DateLayout) },
	"timestamp":  func(t time.Time) string { return t.UTC().Format("2 Jan 2006, 15:04 UTC") },
}

// Parsing here means a broken template stops the service at startup rather
// than on the first visit to that page.
var pages = func() map[string]*template.Template {
	out := map[string]*template.Template{}
	for _, name := range []string{"sign_in", "entities", "new_entity", "entity", "message"} {
		out[name] = template.Must(template.New("layout").Funcs(funcs).ParseFS(templateFS, "templates/layout.html", "templates/"+name+".html"))
	}
	return out
}()

// Handler serves the UI under /ui/.
func Handler(svc *app.Service, log *slog.Logger) http.Handler {
	u := &ui{svc: svc, log: log}
	static, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err) // the embedded directory is fixed at build time
	}

	mux := http.NewServeMux()
	mux.Handle("GET /ui/static/", http.StripPrefix("/ui/static/", http.FileServerFS(static)))
	mux.HandleFunc("GET /ui/sign-in", u.signInPage)
	mux.HandleFunc("POST /ui/sign-in", u.signIn)
	mux.Handle("GET /ui/{$}", u.authed(func(w http.ResponseWriter, r *http.Request, _ kyc.Actor) {
		http.Redirect(w, r, "/ui/entities", http.StatusSeeOther)
	}))
	mux.Handle("GET /ui/entities", u.authed(u.entities))
	mux.Handle("GET /ui/entities/new", u.authed(u.newEntity))
	mux.Handle("POST /ui/entities", u.authed(u.createEntity))
	mux.Handle("GET /ui/entities/{id}", u.authed(u.entity))
	mux.Handle("POST /ui/entities/{id}/checks", u.authed(u.runCheck))
	mux.Handle("POST /ui/entities/{id}/documents", u.authed(u.declareDocument))
	mux.Handle("POST /ui/entities/{id}/documents/{docID}/complete", u.authed(u.completeUpload))
	mux.Handle("/ui/", u.authed(func(w http.ResponseWriter, r *http.Request, a kyc.Actor) {
		u.notFound(w, r, &a)
	}))

	// SameSite=Strict doesn't cover signing in, which needs no cookie, so
	// cross-origin form posts are refused here too.
	return http.NewCrossOriginProtection().Handler(mux)
}

type ui struct {
	svc *app.Service
	log *slog.Logger
}

// view is what every page's layout receives.
type view struct {
	Title string
	Actor *kyc.Actor
	Data  any
}

func (u *ui) render(w http.ResponseWriter, r *http.Request, status int, page string, v view) {
	// Rendered to a buffer first, so a template failure can't send half a page.
	var buf bytes.Buffer
	if err := pages[page].ExecuteTemplate(&buf, "layout", v); err != nil {
		u.log.ErrorContext(r.Context(), "render page", slog.String("page", page), slog.String("error", err.Error()))
		http.Error(w, "Something went wrong.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

func (u *ui) message(w http.ResponseWriter, r *http.Request, status int, a *kyc.Actor, title, text string) {
	u.render(w, r, status, "message", view{Title: title, Actor: a, Data: text})
}

func (u *ui) notFound(w http.ResponseWriter, r *http.Request, a *kyc.Actor) {
	u.message(w, r, http.StatusNotFound, a, "Not found", "There is nothing at this address. Check the link and try again.")
}

func (u *ui) internal(w http.ResponseWriter, r *http.Request, a *kyc.Actor, err error) {
	u.log.ErrorContext(r.Context(), "internal error", slog.String("error", err.Error()))
	u.message(w, r, http.StatusInternalServerError, a, "Something went wrong", "The request couldn't be completed. Try again.")
}

// actorFromCookie resolves the identity cookie to a person. The cookie is no
// stronger than the API's X-User-ID header (ADR-0003), and the role always
// comes from the database.
func (u *ui) actorFromCookie(r *http.Request) (kyc.Actor, bool, error) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return kyc.Actor{}, false, nil
	}
	id, err := uuid.Parse(c.Value)
	if err != nil {
		return kyc.Actor{}, false, nil
	}
	actor, err := u.svc.Authenticate(r.Context(), id)
	if errors.Is(err, app.ErrUnauthenticated) {
		return kyc.Actor{}, false, nil
	}
	if err != nil {
		return kyc.Actor{}, false, err
	}
	if signInUsers[actor.Role] != actor.UserID {
		return kyc.Actor{}, false, nil
	}
	return actor, true, nil
}

// authed sends anyone without a valid identity to sign-in.
func (u *ui) authed(next func(http.ResponseWriter, *http.Request, kyc.Actor)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor, ok, err := u.actorFromCookie(r)
		if err != nil {
			u.internal(w, r, nil, err)
			return
		}
		if !ok {
			setIdentity(w, "", -1)
			http.Redirect(w, r, "/ui/sign-in", http.StatusSeeOther)
			return
		}
		observability.Annotate(r.Context(), slog.String("user_id", actor.UserID.String()))
		next(w, r, actor)
	})
}

func setIdentity(w http.ResponseWriter, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: value, Path: "/ui/", MaxAge: maxAge,
		HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
}

func (u *ui) signInPage(w http.ResponseWriter, r *http.Request) {
	var current *kyc.Actor
	actor, ok, err := u.actorFromCookie(r)
	if err != nil {
		u.internal(w, r, nil, err)
		return
	}
	if ok {
		current = &actor
	}
	u.render(w, r, http.StatusOK, "sign_in", view{Title: "Sign in", Actor: current})
}

func (u *ui) signIn(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxForm)
	id, ok := signInUsers[kyc.Role(r.PostFormValue("role"))]
	if !ok {
		http.Redirect(w, r, "/ui/sign-in", http.StatusSeeOther)
		return
	}
	setIdentity(w, id.String(), 0)
	http.Redirect(w, r, "/ui/", http.StatusSeeOther)
}

// The entity types a list can be filtered by.
var entityTypes = []kyc.EntityType{kyc.EntityTypeIndividual, kyc.EntityTypeBusiness}

type entitiesPage struct {
	Filter   app.EntityFilter
	Statuses []kyc.Status
	Types    []kyc.EntityType
	Entities []kyc.Entity
	Queue    bool   // pending review, which reads oldest first
	Paged    bool   // past the first page
	FirstURL string // this filter's first page
	HereURL  string // this page, to refresh it
	NextURL  string
}

func (u *ui) entities(w http.ResponseWriter, r *http.Request, a kyc.Actor) {
	q := r.URL.Query()
	filter := app.EntityFilter{Status: kyc.Status(q.Get("status")), Type: kyc.EntityType(q.Get("type"))}
	// No status parameter is a first visit, which starts from the role's usual
	// view. An empty one is a person choosing "All".
	if !q.Has("status") && a.Role == kyc.RoleComplianceOfficer {
		filter.Status = kyc.StatusPendingReview
	}
	if after := q.Get("after"); after != "" {
		cursor, err := app.ParseEntityCursor(after)
		if err != nil {
			u.message(w, r, http.StatusBadRequest, &a, "Couldn't open that page", "The link to this page of the list isn't valid. Go back to Entities and try again.")
			return
		}
		filter.After = &cursor
	}

	page, err := u.svc.ListEntities(r.Context(), a, filter)
	if _, invalid := kyc.AsValidationError(err); invalid {
		u.message(w, r, http.StatusBadRequest, &a, "Unknown filter", "That Verification Status or entity type doesn't exist. Go back to Entities and choose from the filters.")
		return
	}
	if err != nil {
		u.internal(w, r, &a, err)
		return
	}

	data := entitiesPage{
		Filter: filter, Statuses: kyc.Statuses, Types: entityTypes, Entities: page.Entities,
		Queue: filter.Status == kyc.StatusPendingReview, Paged: filter.After != nil,
		FirstURL: entitiesURL(filter, nil), HereURL: r.URL.RequestURI(),
	}
	if page.Next != nil {
		data.NextURL = entitiesURL(filter, page.Next)
	}
	u.render(w, r, http.StatusOK, "entities", view{Title: "Entities", Actor: &a, Data: data})
}

// entitiesURL always carries status, even empty, so "All" outlives paging.
func entitiesURL(f app.EntityFilter, after *app.EntityCursor) string {
	q := url.Values{"status": {string(f.Status)}}
	if f.Type != "" {
		q.Set("type", string(f.Type))
	}
	if after != nil {
		q.Set("after", after.String())
	}
	return "/ui/entities?" + q.Encode()
}

// ago is a duration as a person would say it.
func ago(d time.Duration) string {
	plural := func(n int, unit string) string {
		if n == 1 {
			return "1 " + unit + " ago"
		}
		return strconv.Itoa(n) + " " + unit + "s ago"
	}
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return plural(int(d.Minutes()), "minute")
	case d < 24*time.Hour:
		return plural(int(d.Hours()), "hour")
	}
	return plural(int(d.Hours())/24, "day")
}

type entityForm struct {
	IdempotencyKey string
	Type           string
	LegalName      string
	Country        string
	Errors         map[string]string
}

func (u *ui) newEntity(w http.ResponseWriter, r *http.Request, a kyc.Actor) {
	u.render(w, r, http.StatusOK, "new_entity", view{Title: "New Entity", Actor: &a, Data: entityForm{IdempotencyKey: uuid.NewString()}})
}

func (u *ui) createEntity(w http.ResponseWriter, r *http.Request, a kyc.Actor) {
	r.Body = http.MaxBytesReader(w, r.Body, maxForm)
	if err := r.ParseForm(); err != nil {
		u.message(w, r, http.StatusBadRequest, &a, "Couldn't read the form", "Go back and try again.")
		return
	}
	form := entityForm{
		IdempotencyKey: uuid.NewString(),
		Type:           r.PostFormValue("type"),
		LegalName:      r.PostFormValue("legal_name"),
		Country:        strings.ToUpper(strings.TrimSpace(r.PostFormValue("country"))),
	}
	in := kyc.NewEntity{Type: kyc.EntityType(form.Type), LegalName: form.LegalName, Country: form.Country}

	// Invalid input never claims the key, so correcting the form can't
	// collide with the attempt that was refused.
	if err := in.Validate(); err != nil {
		ve, ok := kyc.AsValidationError(err)
		if !ok {
			u.internal(w, r, &a, err)
			return
		}
		form.Errors = make(map[string]string, len(ve.Fields))
		for _, f := range ve.Fields {
			form.Errors[f.Field] = f.Message
		}
		u.render(w, r, http.StatusUnprocessableEntity, "new_entity", view{Title: "New Entity", Actor: &a, Data: form})
		return
	}

	fingerprint := []byte("ui create entity\n" + string(in.Type) + "\n" + in.LegalName + "\n" + in.Country)
	id, err := u.once(r.Context(), a, r.PostFormValue("idempotency_key"), fingerprint, func() (string, error) {
		e, err := u.svc.CreateEntity(r.Context(), a, in)
		if err != nil {
			return "", err
		}
		return e.ID.String(), nil
	})
	if err != nil {
		u.internal(w, r, &a, err)
		return
	}
	observability.Annotate(r.Context(), slog.String("entity_id", id))
	http.Redirect(w, r, "/ui/entities/"+id, http.StatusSeeOther)
}

// once makes a double-submitted form do its work a single time: every
// submission with the same key gets the first one's result.
func (u *ui) once(ctx context.Context, a kyc.Actor, key string, fingerprint []byte, work func() (string, error)) (string, error) {
	if !validIdempotencyKey.MatchString(key) {
		key = uuid.NewString()
	}
	stored, err := u.svc.BeginIdempotent(ctx, a, key, fingerprint)
	if errors.Is(err, app.ErrIdempotencyKeyReused) {
		// Back, edit, submit again: the same form key, but a different request.
		key = uuid.NewString()
		stored, err = u.svc.BeginIdempotent(ctx, a, key, fingerprint)
	}
	if err != nil {
		return "", err
	}
	if stored != nil {
		return string(stored.Body), nil
	}
	result, err := work()
	if err != nil {
		// A 5xx frees the key for a retry.
		_ = u.svc.FinishIdempotent(context.WithoutCancel(ctx), a, key, http.StatusInternalServerError, nil)
		return "", err
	}
	// The work is done, so this request still succeeds if storing fails.
	if err := u.svc.FinishIdempotent(context.WithoutCancel(ctx), a, key, http.StatusSeeOther, []byte(result)); err != nil {
		u.log.ErrorContext(ctx, "store idempotent response", slog.String("error", err.Error()))
	}
	return result, nil
}

type entityPage struct {
	Entity     kyc.Entity
	NextTitle  string
	NextDetail string
	Documents  []kyc.Document
	Checks     []kyc.Check
	// Checklist is nil for anyone who may not read it, and for an Entity with
	// no Review Cycle to show.
	Checklist *app.Checklist
	// ShowRaw is whether this role sees Screening Providers' raw responses.
	ShowRaw bool
	// CanRun is whether the checklist offers running Checks.
	CanRun bool
	// Notice reports why the last action didn't happen.
	Notice string
	// CanUpload is whether this page offers uploading. The form is the Support
	// Agent's: officers review what agents attach.
	CanUpload bool
	// Resubmitted says an earlier Review Cycle's Documents are on this page.
	Resubmitted bool
	DocTypes    []kyc.DocumentType
	MaxBytes    int64
}

func (u *ui) entity(w http.ResponseWriter, r *http.Request, a kyc.Actor) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		u.notFound(w, r, &a)
		return
	}
	observability.Annotate(r.Context(), slog.String("entity_id", id.String()))
	u.renderEntity(w, r, a, id, http.StatusOK, "")
}

func (u *ui) renderEntity(w http.ResponseWriter, r *http.Request, a kyc.Actor, id uuid.UUID, status int, notice string) {
	v, err := u.svc.GetEntity(r.Context(), a, id)
	switch {
	case errors.Is(err, app.ErrNotFound):
		u.notFound(w, r, &a)
		return
	case err != nil:
		u.internal(w, r, &a, err)
		return
	}
	title, detail := nextStep(a.Role, v.Entity)
	var checklist *app.Checklist
	if policy.Allowed(a.Role, policy.ReadChecklist) {
		cl, err := u.svc.ReviewChecklist(r.Context(), a, id)
		if err != nil {
			u.internal(w, r, &a, err)
			return
		}
		if len(cl.Items) > 0 {
			checklist = &cl
		}
		if cl.SystemRejection {
			title, detail = "System Rejection", "A Hard Fail rejected this Entity automatically. A new Completed Upload would start a Resubmission and a new Review Cycle."
		}
	}
	u.render(w, r, status, "entity", view{
		Title: v.Entity.LegalName, Actor: &a,
		Data: entityPage{
			Entity: v.Entity, NextTitle: title, NextDetail: detail, Documents: v.Documents, Checks: v.Checks,
			Checklist: checklist, Notice: notice,
			ShowRaw:     policy.CanSee(a.Role, policy.CheckRawResponse),
			CanRun:      policy.Allowed(a.Role, policy.RunCheck) && v.Entity.Status == kyc.StatusPendingReview,
			CanUpload:   a.Role == kyc.RoleSupportAgent && v.Entity.Status != kyc.StatusVerified,
			Resubmitted: v.Entity.Status == kyc.StatusPendingReview && hasRejectedDocument(v.Documents),
			DocTypes:    documentTypes,
			MaxBytes:    kyc.MaxDocumentBytes,
		},
	})
}

// An open Review Cycle alongside a rejected Document can only be a Resubmission:
// Documents are rejected when a cycle ends.
func hasRejectedDocument(docs []kyc.Document) bool {
	for _, d := range docs {
		if d.ReviewStatus == kyc.ReviewRejected {
			return true
		}
	}
	return false
}

func (u *ui) runCheck(w http.ResponseWriter, r *http.Request, a kyc.Actor) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		u.notFound(w, r, &a)
		return
	}
	observability.Annotate(r.Context(), slog.String("entity_id", id.String()))
	r.Body = http.MaxBytesReader(w, r.Body, maxForm)
	if err := r.ParseForm(); err != nil {
		u.message(w, r, http.StatusBadRequest, &a, "Couldn't read the form", "Go back and try again.")
		return
	}
	checkType := kyc.CheckType(r.PostFormValue("check_type"))
	fingerprint := []byte("ui run check\n" + id.String() + "\n" + string(checkType))
	_, err = u.once(r.Context(), a, r.PostFormValue("idempotency_key"), fingerprint, func() (string, error) {
		_, _, err := u.svc.RunCheck(r.Context(), a, id, checkType)
		return "", err
	})
	_, invalid := kyc.AsValidationError(err)
	switch {
	case err == nil:
		http.Redirect(w, r, "/ui/entities/"+id.String(), http.StatusSeeOther)
	case errors.Is(err, app.ErrNotFound):
		u.notFound(w, r, &a)
	case errors.Is(err, app.ErrForbidden):
		u.message(w, r, http.StatusForbidden, &a, "Not allowed", "Only a Compliance Officer can run Checks.")
	case errors.Is(err, app.ErrProviderUnavailable):
		u.renderEntity(w, r, a, id, http.StatusBadGateway, checkTypeName(checkType)+": the Screening Provider didn't answer. Nothing was recorded, so this Check still stands as it did. Try again.")
	case errors.Is(err, app.ErrConflict):
		u.renderEntity(w, r, a, id, http.StatusConflict, "Checks run only while an Entity is pending review, and this one no longer is. Nothing was recorded.")
	case invalid:
		u.renderEntity(w, r, a, id, http.StatusBadRequest, "That Check Type doesn't exist. Nothing was recorded.")
	default:
		u.internal(w, r, &a, err)
	}
}

// The upload script's two calls answer in JSON, because it has to stay on the
// page to report a failure and offer a retry. Every failure carries a message
// written for the person reading it.

type uploadFailure struct {
	Message string `json:"message"`
	// Retry says the same completion may still work. Otherwise the agent
	// starts a new upload.
	Retry bool `json:"retry,omitempty"`
}

func (u *ui) writeJSON(w http.ResponseWriter, r *http.Request, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		u.log.ErrorContext(r.Context(), "write response", slog.String("error", err.Error()))
	}
}

func (u *ui) uploadError(w http.ResponseWriter, r *http.Request, err error) {
	if ve, ok := kyc.AsValidationError(err); ok {
		msgs := make([]string, len(ve.Fields))
		for i, f := range ve.Fields {
			msgs[i] = uploadFieldMessage(f)
		}
		u.writeJSON(w, r, http.StatusUnprocessableEntity, uploadFailure{Message: strings.Join(msgs, " ")})
		return
	}
	switch {
	case errors.Is(err, app.ErrNotFound):
		u.writeJSON(w, r, http.StatusNotFound, uploadFailure{Message: "This Entity or Document no longer exists."})
	case errors.Is(err, app.ErrForbidden):
		u.writeJSON(w, r, http.StatusForbidden, uploadFailure{Message: "Your role can't upload Documents."})
	case errors.Is(err, app.ErrConflict):
		u.writeJSON(w, r, http.StatusConflict, uploadFailure{Message: "This Entity no longer accepts Documents. Refresh the page to see where it stands."})
	case errors.Is(err, app.ErrGone):
		u.writeJSON(w, r, http.StatusGone, uploadFailure{Message: "This upload has expired. Start a new upload."})
	case errors.Is(err, app.ErrUploadMismatch):
		u.writeJSON(w, r, http.StatusUnprocessableEntity, uploadFailure{Message: "The Evidence Vault doesn't hold a valid copy of this file, so it wasn't accepted. Check the file and start a new upload."})
	default:
		u.log.ErrorContext(r.Context(), "upload step failed", slog.String("error", err.Error()))
		u.writeJSON(w, r, http.StatusInternalServerError, uploadFailure{Message: "The service couldn't finish this step.", Retry: true})
	}
}

func uploadFieldMessage(f kyc.FieldError) string {
	switch f.Field {
	case "doc_type":
		return "Choose a Document type."
	case "expiry_date":
		return "Enter a real expiry date, or leave it empty."
	case "content_type":
		return "The file must be a PDF, JPEG or PNG."
	case "size_bytes":
		return "The file must be no larger than 10 MiB, and not empty."
	case "sha256":
		return "The file couldn't be fingerprinted. Try again."
	}
	return f.Message
}

// declaration is what the upload script sends once it has fingerprinted a file.
type declaration struct {
	DocType     string `json:"doc_type"`
	ExpiryDate  string `json:"expiry_date"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
	SHA256      string `json:"sha256"`
}

type uploadGrant struct {
	CompleteURL string            `json:"complete_url"`
	Method      string            `json:"method"`
	URL         string            `json:"url"`
	Headers     map[string]string `json:"headers"`
}

func (u *ui) declareDocument(w http.ResponseWriter, r *http.Request, a kyc.Actor) {
	entityID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		u.uploadError(w, r, app.ErrNotFound)
		return
	}
	observability.Annotate(r.Context(), slog.String("entity_id", entityID.String()))
	var in declaration
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxForm)).Decode(&in); err != nil {
		u.writeJSON(w, r, http.StatusBadRequest, uploadFailure{Message: "The upload request couldn't be read. Refresh the page and try again."})
		return
	}
	doc, perm, err := u.svc.DeclareDocument(r.Context(), a, entityID, kyc.NewDocument{
		Type: kyc.DocumentType(in.DocType), ExpiryDate: in.ExpiryDate,
		ContentType: in.ContentType, SizeBytes: in.SizeBytes, SHA256: in.SHA256,
	})
	if err != nil {
		u.uploadError(w, r, err)
		return
	}
	u.writeJSON(w, r, http.StatusCreated, uploadGrant{
		CompleteURL: "/ui/entities/" + entityID.String() + "/documents/" + doc.ID.String() + "/complete",
		Method:      perm.Method, URL: perm.URL, Headers: perm.Headers,
	})
}

func (u *ui) completeUpload(w http.ResponseWriter, r *http.Request, a kyc.Actor) {
	entityID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		u.uploadError(w, r, app.ErrNotFound)
		return
	}
	docID, err := uuid.Parse(r.PathValue("docID"))
	if err != nil {
		u.uploadError(w, r, app.ErrNotFound)
		return
	}
	observability.Annotate(r.Context(), slog.String("entity_id", entityID.String()))
	if _, _, err := u.svc.CompleteUpload(r.Context(), a, entityID, docID); err != nil {
		u.uploadError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// nextStep says, in the glossary's words, what happens next for this role.
func nextStep(role kyc.Role, e kyc.Entity) (title, detail string) {
	required := requiredDocuments(e.Type)
	if role == kyc.RoleComplianceOfficer {
		switch e.Status {
		case kyc.StatusUnverified:
			return "Waiting for a Document", "This Entity has nothing to review until a Support Agent attaches " + required + "."
		case kyc.StatusPendingReview:
			return "Ready for review", "A Review Cycle is open: run the Required Checks, then decide."
		case kyc.StatusRejected:
			return "Rejected", "A new Completed Upload would start a Resubmission and a new Review Cycle."
		case kyc.StatusVerified:
			return "Verified", "This Entity is approved. Nothing more can change."
		}
	}
	switch e.Status {
	case kyc.StatusUnverified:
		return "Upload a required Document", "This Entity needs " + required + "."
	case kyc.StatusPendingReview:
		return "Waiting for Compliance", "Nothing is needed from you right now."
	case kyc.StatusRejected:
		return "Rejected: contact Compliance for details", "To resubmit, upload a new Document once you know what to fix."
	case kyc.StatusVerified:
		return "Verified", "This Entity is approved. Nothing more is needed."
	}
	return string(e.Status), ""
}

func requiredDocuments(t kyc.EntityType) string {
	types := kyc.RequiredDocuments(t)
	names := make([]string, len(types))
	for i, d := range types {
		names[i] = documentName(d)
	}
	return strings.Join(names, " or ")
}

func documentName(d kyc.DocumentType) string {
	switch d {
	case kyc.DocPassport:
		return "a passport"
	case kyc.DocNationalID:
		return "a national ID"
	case kyc.DocBusinessRegistration:
		return "a business registration"
	case kyc.DocProofOfAddress:
		return "a proof of address"
	}
	return string(d)
}

// Every Document type an agent can attach, in the order a form lists them.
var documentTypes = []kyc.DocumentType{kyc.DocPassport, kyc.DocNationalID, kyc.DocBusinessRegistration, kyc.DocProofOfAddress}

func checkTypeName(c kyc.CheckType) string {
	switch c {
	case kyc.CheckSanctions:
		return "Sanctions screening"
	case kyc.CheckPEP:
		return "PEP screening"
	case kyc.CheckAddress:
		return "Address verification"
	}
	return string(c)
}

func resultName(r kyc.CheckResult) string {
	switch r {
	case kyc.ResultPass:
		return "Pass"
	case kyc.ResultFail:
		return "Hard Fail"
	case kyc.ResultManualReview:
		return "Manual review"
	}
	return string(r)
}

// documentsName lists the Required Documents as alternatives.
func documentsName(types []kyc.DocumentType) string {
	names := make([]string, len(types))
	for i, d := range types {
		names[i] = docTypeName(d)
	}
	return strings.Join(names, " or ")
}

// problemName says what is wrong with a checklist item.
func problemName(item kyc.ChecklistItem) string {
	switch item.Problem {
	case "":
		if item.Check != "" {
			return "Pass"
		}
		return "Present"
	case "missing":
		if item.Check != "" {
			return "Not run in this Review Cycle"
		}
		return "Missing from this Review Cycle"
	case "expired":
		return "Expired"
	case string(kyc.ResultFail):
		return "Hard Fail"
	case string(kyc.ResultManualReview):
		return "Manual review: blocks approval until a re-run passes"
	}
	return item.Problem
}

// prettyJSON indents a provider payload for reading. It's shown as it arrived
// if it can't be parsed.
func prettyJSON(raw json.RawMessage) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return string(raw)
	}
	return buf.String()
}

func docTypeName(d kyc.DocumentType) string {
	switch d {
	case kyc.DocPassport:
		return "Passport"
	case kyc.DocNationalID:
		return "National ID"
	case kyc.DocBusinessRegistration:
		return "Business registration"
	case kyc.DocProofOfAddress:
		return "Proof of address"
	}
	return string(d)
}

func uploadStatusName(s kyc.UploadStatus) string {
	switch s {
	case kyc.UploadAwaiting:
		return "Awaiting upload"
	case kyc.UploadComplete:
		return "Completed"
	case kyc.UploadExpired:
		return "Expired"
	}
	return string(s)
}

func reviewStatusName(s kyc.DocumentReviewStatus) string {
	switch s {
	case kyc.ReviewPending:
		return "Pending"
	case kyc.ReviewAccepted:
		return "Accepted"
	case kyc.ReviewRejected:
		return "Rejected"
	}
	return string(s)
}

func roleName(r kyc.Role) string {
	switch r {
	case kyc.RoleComplianceOfficer:
		return "Compliance Officer"
	case kyc.RoleSupportAgent:
		return "Support Agent"
	}
	return string(r)
}

func statusName(s kyc.Status) string {
	switch s {
	case kyc.StatusUnverified:
		return "Unverified"
	case kyc.StatusPendingReview:
		return "Pending review"
	case kyc.StatusVerified:
		return "Verified"
	case kyc.StatusRejected:
		return "Rejected"
	}
	return string(s)
}

func typeName(t kyc.EntityType) string {
	switch t {
	case kyc.EntityTypeIndividual:
		return "Individual"
	case kyc.EntityTypeBusiness:
		return "Business"
	}
	return string(t)
}
