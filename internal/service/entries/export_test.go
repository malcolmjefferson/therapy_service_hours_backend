package entries_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/entries"
)

func TestExport_DefaultsToTheFirstPageOfFifty(t *testing.T) {
	t.Parallel()
	store := &fakeStore{items: []domain.Entry{{ID: "e1"}}, total: 1}
	page, err := newService(t, store).Export(t.Context(), owner(), entries.ExportRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if q := store.exportQuery; q.Page != 1 || q.PageSize != entries.DefaultPageSize || q.Offset() != 0 {
		t.Errorf("defaults = %+v", q)
	}
	if store.exportUserID != owner().ID {
		t.Errorf("store asked for user %q, want the actor %q", store.exportUserID, owner().ID)
	}
	if page.Page != 1 || page.PageSize != 50 || page.TotalItems != 1 || len(page.Items) != 1 {
		t.Errorf("page = %+v", page)
	}
}

func TestExport_PassesTheWindowToTheStore(t *testing.T) {
	t.Parallel()
	store := &fakeStore{}
	if _, err := newService(t, store).Export(t.Context(), owner(), entries.ExportRequest{Page: ptr(3), PageSize: ptr(20)}); err != nil {
		t.Fatal(err)
	}
	if q := store.exportQuery; q.Page != 3 || q.PageSize != 20 || q.Offset() != 40 {
		t.Errorf("query = %+v offset %d", q, q.Offset())
	}
}

func TestExport_PageSizeIsCappedAt200(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name    string
		size    int
		allowed bool
	}{
		{"largest allowed", 200, true},
		{"smallest allowed", 1, true},
		{"one over the cap", 201, false},
		{"far over the cap", 1_000_000, false},
		{"zero", 0, false},
		{"negative", -5, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := &fakeStore{}
			_, err := newService(t, store).Export(t.Context(), owner(), entries.ExportRequest{PageSize: ptr(tt.size)})
			if tt.allowed {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var verr *domain.ValidationError
			if !errors.As(err, &verr) || len(verr.Fields) != 1 || verr.Fields[0].Field != "page_size" {
				t.Fatalf("err = %v, want a validation error on page_size", err)
			}
			if store.calls != 0 {
				t.Error("the store ran for an invalid page size")
			}
		})
	}
}

func TestExport_ReportsAllInvalidFieldsTogetherWithoutEchoingValues(t *testing.T) {
	t.Parallel()
	_, err := newService(t, &fakeStore{}).Export(t.Context(), owner(), entries.ExportRequest{Page: ptr(0), PageSize: ptr(98765)})
	var verr *domain.ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("err = %v", err)
	}
	var got []string
	for _, f := range verr.Fields {
		got = append(got, f.Field)
	}
	if want := []string{"page", "page_size"}; !slices.Equal(got, want) {
		t.Errorf("fields = %v, want %v", got, want)
	}
	if strings.Contains(err.Error(), "98765") {
		t.Errorf("a submitted value was echoed: %v", err)
	}
}

func TestExport_PageBeyondTheCapIsRejected(t *testing.T) {
	t.Parallel()
	if _, err := newService(t, &fakeStore{}).Export(t.Context(), owner(), entries.ExportRequest{Page: ptr(entries.MaxPage + 1)}); err == nil {
		t.Fatal("a page past MaxPage was accepted")
	}
}

func TestExport_DeniesAnActorThatIsNotActive(t *testing.T) {
	t.Parallel()
	for name, actor := range map[string]domain.Actor{
		"zero actor":  {},
		"inactive":    {ID: "u", OrganizationID: "o"},
		"no org":      {ID: "u", Active: true},
		"no identity": {OrganizationID: "o", Active: true},
		"admin only":  {Admin: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store := &fakeStore{}
			_, err := newService(t, store).Export(t.Context(), actor, entries.ExportRequest{})
			if !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("err = %v, want ErrForbidden", err)
			}
			if store.calls != 0 {
				t.Error("the store ran for a denied actor")
			}
		})
	}
}

func TestExport_AdminAndSupervisorRolesDoNotWidenToOtherUsers(t *testing.T) {
	t.Parallel()
	actor := owner()
	actor.Admin, actor.Supervisor = true, true
	store := &fakeStore{}
	if _, err := newService(t, store).Export(t.Context(), actor, entries.ExportRequest{}); err != nil {
		t.Fatal(err)
	}
	if store.exportUserID != actor.ID {
		t.Errorf("store asked for user %q, want only the actor", store.exportUserID)
	}
}

func TestExport_WrapsStoreErrorsAndKeepsThemMatchable(t *testing.T) {
	t.Parallel()
	boom := errors.New("connection reset")
	_, err := newService(t, &fakeStore{err: boom}).Export(t.Context(), owner(), entries.ExportRequest{})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it to wrap the store error", err)
	}
	if err.Error() == boom.Error() {
		t.Error("the error was not given context")
	}
}
