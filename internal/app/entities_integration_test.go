//go:build integration

package app_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/bfischetti/kyc-lifecycle/internal/app"
	"github.com/bfischetti/kyc-lifecycle/internal/db"
	"github.com/bfischetti/kyc-lifecycle/internal/kyc"
	"github.com/bfischetti/kyc-lifecycle/internal/screening"
	"github.com/bfischetti/kyc-lifecycle/internal/testenv"
)

var (
	officer = kyc.Actor{UserID: uuid.MustParse("00000000-0000-0000-0000-000000000001"), Role: kyc.RoleComplianceOfficer}
	agent   = kyc.Actor{UserID: uuid.MustParse("00000000-0000-0000-0000-000000000002"), Role: kyc.RoleSupportAgent}
	base    = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
)

type listEnv struct {
	svc   *app.Service
	store *db.Store
	admin *pgx.Conn
}

func setupList(t *testing.T) listEnv {
	t.Helper()
	pg := testenv.StartPostgres(t)
	store, err := db.Open(t.Context(), pg.AppURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	admin, err := pgx.Connect(t.Context(), pg.AdminURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	// Listing and screening don't touch the vault.
	return listEnv{svc: app.New(store, nil, screening.Mock{}, app.DefaultOptions), store: store, admin: admin}
}

// seed inserts an Entity directly, to control its status and position.
func (e listEnv) seed(t *testing.T, name string, typ kyc.EntityType, status kyc.Status, updated time.Time) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := e.admin.QueryRow(t.Context(),
		`INSERT INTO entities (type, legal_name, country, status, updated_at) VALUES ($1, $2, 'GB', $3, $4) RETURNING id`,
		string(typ), name, string(status), updated).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func names(es []kyc.Entity) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.LegalName
	}
	return out
}

func equal(a, b []string) bool {
	return fmt.Sprint(a) == fmt.Sprint(b)
}

func TestListEntitiesFilters(t *testing.T) {
	e := setupList(t)
	e.seed(t, "pending person", kyc.EntityTypeIndividual, kyc.StatusPendingReview, base)
	e.seed(t, "pending business", kyc.EntityTypeBusiness, kyc.StatusPendingReview, base.Add(time.Minute))
	e.seed(t, "verified person", kyc.EntityTypeIndividual, kyc.StatusVerified, base.Add(2*time.Minute))
	e.seed(t, "rejected business", kyc.EntityTypeBusiness, kyc.StatusRejected, base.Add(3*time.Minute))

	tests := []struct {
		name   string
		filter app.EntityFilter
		want   []string
	}{
		{"all, newest first", app.EntityFilter{}, []string{"rejected business", "verified person", "pending business", "pending person"}},
		{"pending review, oldest first", app.EntityFilter{Status: kyc.StatusPendingReview}, []string{"pending person", "pending business"}},
		{"another status", app.EntityFilter{Status: kyc.StatusVerified}, []string{"verified person"}},
		{"type", app.EntityFilter{Type: kyc.EntityTypeBusiness}, []string{"rejected business", "pending business"}},
		{"both", app.EntityFilter{Status: kyc.StatusPendingReview, Type: kyc.EntityTypeIndividual}, []string{"pending person"}},
		{"nothing matches", app.EntityFilter{Status: kyc.StatusUnverified}, []string{}},
	}
	for _, actor := range []kyc.Actor{officer, agent} {
		for _, tt := range tests {
			t.Run(string(actor.Role)+"/"+tt.name, func(t *testing.T) {
				page, err := e.svc.ListEntities(t.Context(), actor, tt.filter)
				if err != nil {
					t.Fatal(err)
				}
				if got := names(page.Entities); !equal(got, tt.want) {
					t.Errorf("got %v, want %v", got, tt.want)
				}
				if page.Next != nil {
					t.Errorf("Next = %v on the only page", page.Next)
				}
			})
		}
	}
}

func TestListEntitiesRefusals(t *testing.T) {
	e := setupList(t)
	system := kyc.Actor{UserID: kyc.SystemUserID, Role: kyc.RoleSystem}
	if _, err := e.svc.ListEntities(t.Context(), system, app.EntityFilter{}); !errors.Is(err, app.ErrForbidden) {
		t.Errorf("system: err = %v, want forbidden", err)
	}
	for _, f := range []app.EntityFilter{{Status: "approved"}, {Type: "trust"}} {
		_, err := e.svc.ListEntities(t.Context(), officer, f)
		if _, ok := kyc.AsValidationError(err); !ok {
			t.Errorf("filter %+v: err = %v, want a validation error", f, err)
		}
	}
}

// Identical timestamps are the hard case: the position has to separate them.
func TestListEntitiesPagesWithoutDuplicatesOrGaps(t *testing.T) {
	e := setupList(t)
	const total = 2*app.EntityPageSize + 1
	seeded := map[string]bool{}
	for i := range total {
		name := fmt.Sprintf("entity %03d", i)
		seeded[name] = true
		// Groups of ten share an updated_at.
		e.seed(t, name, kyc.EntityTypeBusiness, kyc.StatusPendingReview, base.Add(time.Duration(i/10)*time.Second))
	}

	for _, tt := range []struct {
		name   string
		status kyc.Status
	}{{"oldest first", kyc.StatusPendingReview}, {"newest first", ""}} {
		t.Run(tt.name, func(t *testing.T) {
			seen := map[string]bool{}
			pages := []int{}
			filter := app.EntityFilter{Status: tt.status}
			for {
				page, err := e.svc.ListEntities(t.Context(), officer, filter)
				if err != nil {
					t.Fatal(err)
				}
				pages = append(pages, len(page.Entities))
				for _, name := range names(page.Entities) {
					if seen[name] {
						t.Errorf("%s appeared twice", name)
					}
					seen[name] = true
				}
				if page.Next == nil {
					break
				}
				// Through its link form, as the UI uses it.
				next, err := app.ParseEntityCursor(page.Next.String())
				if err != nil {
					t.Fatal(err)
				}
				filter.After = &next
			}
			if fmt.Sprint(pages) != "[50 50 1]" {
				t.Errorf("page sizes = %v, want [50 50 1]", pages)
			}
			if len(seen) != len(seeded) {
				t.Errorf("saw %d Entities, want %d", len(seen), len(seeded))
			}
		})
	}
}

// Exactly one full page must not offer an empty next one.
func TestListEntitiesFullLastPageHasNoNext(t *testing.T) {
	e := setupList(t)
	for i := range app.EntityPageSize {
		e.seed(t, fmt.Sprintf("entity %03d", i), kyc.EntityTypeBusiness, kyc.StatusVerified, base)
	}
	page, err := e.svc.ListEntities(t.Context(), agent, app.EntityFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entities) != app.EntityPageSize || page.Next != nil {
		t.Errorf("got %d Entities, Next = %v", len(page.Entities), page.Next)
	}
}

// An officer working through the queue mustn't send an Entity to the back of
// it by screening it.
func TestListEntitiesRunningACheckKeepsQueuePosition(t *testing.T) {
	e := setupList(t)
	first := e.seed(t, "first in line", kyc.EntityTypeIndividual, kyc.StatusPendingReview, base)
	e.seed(t, "second in line", kyc.EntityTypeIndividual, kyc.StatusPendingReview, base.Add(time.Minute))
	queue := app.EntityFilter{Status: kyc.StatusPendingReview}

	if _, _, err := e.svc.RunCheck(t.Context(), officer, first, kyc.CheckSanctions); err != nil {
		t.Fatal(err)
	}

	page, err := e.svc.ListEntities(t.Context(), officer, queue)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := names(page.Entities), []string{"first in line", "second in line"}; !equal(got, want) {
		t.Errorf("queue = %v, want %v", got, want)
	}
}

func TestEntityCursorRoundTrip(t *testing.T) {
	c := app.EntityCursor{UpdatedAt: base.Add(123456 * time.Microsecond), ID: uuid.New()}
	got, err := app.ParseEntityCursor(c.String())
	if err != nil {
		t.Fatal(err)
	}
	if !got.UpdatedAt.Equal(c.UpdatedAt) || got.ID != c.ID {
		t.Errorf("got %+v, want %+v", got, c)
	}
	for _, bad := range []string{"", "abc", "12_notauuid", "x_" + c.ID.String()} {
		if _, err := app.ParseEntityCursor(bad); err == nil {
			t.Errorf("ParseEntityCursor(%q) succeeded", bad)
		}
	}
}
