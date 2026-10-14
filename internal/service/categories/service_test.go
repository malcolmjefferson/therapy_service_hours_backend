package categories_test

import (
	"context"
	"errors"
	"testing"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/categories"
)

type fakeStore struct {
	calls int
	rows  []domain.Category
	err   error
}

func (f *fakeStore) List(context.Context) ([]domain.Category, error) {
	f.calls++
	return f.rows, f.err
}

var active = domain.Actor{ID: "user-1", OrganizationID: "org-1", Active: true}

func cat(id, name string, group domain.CategoryGroup) domain.Category {
	return domain.Category{ID: id, Name: name, Group: group}
}

func newService(t *testing.T, store *fakeStore) *categories.Service {
	t.Helper()
	svc, err := categories.New(store)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func TestNewRequiresAStore(t *testing.T) {
	t.Parallel()
	if _, err := categories.New(nil); err == nil {
		t.Fatal("New(nil) succeeded")
	}
}

func TestList_RequiresAnActiveActor(t *testing.T) {
	t.Parallel()
	denied := map[string]domain.Actor{
		"zero actor":     {},
		"inactive actor": {ID: "user-1", OrganizationID: "org-1"},
		"no org":         {ID: "user-1", Active: true},
		"no id":          {OrganizationID: "org-1", Active: true},
	}
	for name, actor := range denied {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store := &fakeStore{rows: []domain.Category{cat("1", "Individual Counseling", domain.GroupDirect)}}
			if _, err := newService(t, store).List(t.Context(), actor); !errors.Is(err, domain.ErrForbidden) {
				t.Errorf("err = %v, want ErrForbidden", err)
			}
			if store.calls != 0 {
				t.Error("the store was reached")
			}
		})
	}
}

func TestList_AnyActiveRoleMayRead(t *testing.T) {
	t.Parallel()
	for name, actor := range map[string]domain.Actor{
		"user":       active,
		"supervisor": {ID: "u2", OrganizationID: "org-1", Active: true, Supervisor: true},
		"admin":      {ID: "u3", OrganizationID: "org-1", Active: true, Admin: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store := &fakeStore{rows: []domain.Category{cat("1", "Individual Counseling", domain.GroupDirect)}}
			if _, err := newService(t, store).List(t.Context(), actor); err != nil {
				t.Errorf("err = %v", err)
			}
		})
	}
}

func TestList_GroupsInDisplayOrderKeepingTheStoreOrderWithinAGroup(t *testing.T) {
	t.Parallel()
	// The store returns display order; the interleaving here proves grouping does not depend on it.
	store := &fakeStore{rows: []domain.Category{
		cat("s1", "Group Supervision", domain.GroupSupervision),
		cat("d1", "Individual Counseling", domain.GroupDirect),
		cat("i1", "Staff Meetings", domain.GroupIndirect),
		cat("d2", "Group Facilitation", domain.GroupDirect),
	}}
	got, err := newService(t, store).List(t.Context(), active)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		group domain.CategoryGroup
		label string
		ids   []string
	}{
		{domain.GroupDirect, "Direct Service", []string{"d1", "d2"}},
		{domain.GroupIndirect, "Indirect Service", []string{"i1"}},
		{domain.GroupSupervision, "Supervision", []string{"s1"}},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d sections, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].Group != w.group || got[i].Label != w.label || len(got[i].Categories) != len(w.ids) {
			t.Fatalf("section %d = %+v, want %+v", i, got[i], w)
		}
		for j, id := range w.ids {
			if got[i].Categories[j].ID != id {
				t.Errorf("section %d category %d = %q, want %q", i, j, got[i].Categories[j].ID, id)
			}
		}
	}
}

func TestList_OmitsEmptyGroupsAndReturnsNoSectionsForNoCategories(t *testing.T) {
	t.Parallel()
	only := &fakeStore{rows: []domain.Category{cat("s1", "Group Supervision", domain.GroupSupervision)}}
	got, err := newService(t, only).List(t.Context(), active)
	if err != nil || len(got) != 1 || got[0].Group != domain.GroupSupervision {
		t.Errorf("got %+v, err %v", got, err)
	}
	none, err := newService(t, &fakeStore{}).List(t.Context(), active)
	if err != nil || none == nil || len(none) != 0 {
		t.Errorf("empty store: got %#v, err %v; want an empty, non-nil slice", none, err)
	}
}

func TestList_AnUnknownStoredGroupIsAnErrorNotADroppedCategory(t *testing.T) {
	t.Parallel()
	store := &fakeStore{rows: []domain.Category{cat("x1", "Mystery", domain.CategoryGroup("other"))}}
	if _, err := newService(t, store).List(t.Context(), active); err == nil {
		t.Fatal("an unknown group was accepted")
	}
}

func TestList_StoreErrorsPassThrough(t *testing.T) {
	t.Parallel()
	boom := errors.New("list categories: connection refused")
	_, err := newService(t, &fakeStore{err: boom}).List(t.Context(), active)
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want the store error", err)
	}
}
