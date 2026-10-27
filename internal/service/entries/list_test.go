package entries_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/entries"
)

// fakeStore records the call and returns canned rows. It never touches a database (GO-17: the
// PostgreSQL adapter has its own contract tests in store_test.go).
type fakeStore struct {
	entries.Writer // The write paths have their own tests; calling one here is a bug in the test.
	calls          int
	userID         string
	query          entries.ListQuery
	exportUserID   string
	exportQuery    entries.ExportQuery
	items          []domain.Entry
	total          int64
	err            error
}

func (f *fakeStore) ListEntries(_ context.Context, userID string, q entries.ListQuery) ([]domain.Entry, int64, error) {
	f.calls++
	f.userID, f.query = userID, q
	return f.items, f.total, f.err
}

func (f *fakeStore) ExportEntries(_ context.Context, userID string, q entries.ExportQuery) ([]domain.Entry, int64, error) {
	f.calls++
	f.exportUserID, f.exportQuery = userID, q
	return f.items, f.total, f.err
}

func owner() domain.Actor {
	return domain.Actor{ID: "00000000-0000-4000-8000-000000000002", OrganizationID: "00000000-0000-4000-8000-000000000001", Active: true}
}

func newService(t *testing.T, store entries.Store) *entries.Service {
	t.Helper()
	s, err := entries.New(store)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func ptr[T any](v T) *T { return &v }

func TestNew_RequiresStore(t *testing.T) {
	t.Parallel()
	if _, err := entries.New(nil); err == nil {
		t.Fatal("expected an error for a nil store")
	}
}

func TestList_DefaultsToNewestFirstFirstPageOfFifty(t *testing.T) {
	t.Parallel()
	store := &fakeStore{items: []domain.Entry{{ID: "e1"}}, total: 1}
	page, err := newService(t, store).List(t.Context(), owner(), entries.ListRequest{})
	if err != nil {
		t.Fatal(err)
	}
	q := store.query
	if q.Sort != domain.SortSessionDate || q.Dir != domain.SortDesc || q.Page != 1 || q.PageSize != 50 {
		t.Errorf("defaults = %+v", q)
	}
	if q.Group != nil || q.Client != nil || q.From != nil || q.To != nil {
		t.Errorf("filters set by default: %+v", q)
	}
	if store.userID != owner().ID {
		t.Errorf("store asked for user %q, want the actor %q", store.userID, owner().ID)
	}
	if page.Page != 1 || page.PageSize != 50 || page.TotalItems != 1 || len(page.Items) != 1 {
		t.Errorf("page = %+v", page)
	}
}

func TestList_PassesValidatedInputToTheStore(t *testing.T) {
	t.Parallel()
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)
	store := &fakeStore{}
	_, err := newService(t, store).List(t.Context(), owner(), entries.ListRequest{
		Group: ptr("supervision"), Client: ptr("  a_b%  "), From: &from, To: &to,
		Sort: ptr("category"), Dir: ptr("asc"), Page: ptr(3), PageSize: ptr(20),
	})
	if err != nil {
		t.Fatal(err)
	}
	q := store.query
	if q.Group == nil || *q.Group != domain.GroupSupervision || q.Client == nil || *q.Client != "a_b%" {
		t.Errorf("filters = %+v", q)
	}
	if q.From == nil || !q.From.Equal(from) || q.To == nil || !q.To.Equal(to) {
		t.Errorf("dates = %v %v", q.From, q.To)
	}
	if q.Sort != domain.SortCategory || q.Dir != domain.SortAsc || q.Page != 3 || q.PageSize != 20 || q.Offset() != 40 {
		t.Errorf("order and window = %+v offset %d", q, q.Offset())
	}
}

func TestList_RejectsInvalidInputNamingEveryField(t *testing.T) {
	t.Parallel()
	from := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	before := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		req   entries.ListRequest
		field string
	}{
		{"unknown group", entries.ListRequest{Group: ptr("Direct; DROP TABLE entries")}, "group"},
		{"unknown sort", entries.ListRequest{Sort: ptr("notes; DROP TABLE entries")}, "sort"},
		{"sort by an internal column", entries.ListRequest{Sort: ptr("user_id")}, "sort"},
		{"unknown direction", entries.ListRequest{Dir: ptr("sideways")}, "dir"},
		{"upper-case direction", entries.ListRequest{Dir: ptr("DESC")}, "dir"},
		{"empty client", entries.ListRequest{Client: ptr("")}, "client"},
		{"blank client", entries.ListRequest{Client: ptr("   ")}, "client"},
		{"client too long", entries.ListRequest{Client: ptr(strings.Repeat("a", 65))}, "client"},
		{"client with NUL", entries.ListRequest{Client: ptr("a\x00b")}, "client"},
		{"client with newline", entries.ListRequest{Client: ptr("a\nb")}, "client"},
		{"client not UTF-8", entries.ListRequest{Client: ptr("a\xffb")}, "client"},
		{"to before from", entries.ListRequest{From: &from, To: &before}, "to"},
		{"page zero", entries.ListRequest{Page: ptr(0)}, "page"},
		{"negative page", entries.ListRequest{Page: ptr(-1)}, "page"},
		{"page beyond the cap", entries.ListRequest{Page: ptr(100001)}, "page"},
		{"page size zero", entries.ListRequest{PageSize: ptr(0)}, "page_size"},
		{"page size over 200", entries.ListRequest{PageSize: ptr(201)}, "page_size"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := &fakeStore{}
			_, err := newService(t, store).List(t.Context(), owner(), tt.req)
			var verr *domain.ValidationError
			if !errors.As(err, &verr) {
				t.Fatalf("err = %v, want a validation error", err)
			}
			if len(verr.Fields) != 1 || verr.Fields[0].Field != tt.field {
				t.Errorf("fields = %+v, want only %q", verr.Fields, tt.field)
			}
			if store.calls != 0 {
				t.Error("the store ran for invalid input")
			}
		})
	}
}

func TestList_ReportsAllInvalidFieldsTogetherWithoutEchoingValues(t *testing.T) {
	t.Parallel()
	_, err := newService(t, &fakeStore{}).List(t.Context(), owner(), entries.ListRequest{
		Group: ptr("secret-group"), Sort: ptr("secret-sort"), Dir: ptr("secret-dir"), Page: ptr(0), PageSize: ptr(999),
	})
	var verr *domain.ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("err = %v", err)
	}
	var got []string
	for _, f := range verr.Fields {
		got = append(got, f.Field)
	}
	if want := []string{"group", "sort", "dir", "page", "page_size"}; !slices.Equal(got, want) {
		t.Errorf("fields = %v, want %v", got, want)
	}
	if strings.Contains(err.Error(), "secret-") {
		t.Errorf("a submitted value was echoed: %v", err)
	}
}

func TestList_AcceptsBoundaryValues(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		req  entries.ListRequest
	}{
		{"64 characters", entries.ListRequest{Client: ptr(strings.Repeat("a", 64))}},
		{"64 multi-byte characters", entries.ListRequest{Client: ptr(strings.Repeat("é", 64))}},
		{"wildcards and a backslash", entries.ListRequest{Client: ptr(`%_\`)}},
		{"first and last page", entries.ListRequest{Page: ptr(100000), PageSize: ptr(200)}},
		{"smallest page size", entries.ListRequest{PageSize: ptr(1)}},
		{"same day range", entries.ListRequest{From: ptr(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)), To: ptr(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := newService(t, &fakeStore{}).List(t.Context(), owner(), tt.req); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestList_EverySortAndDirectionOnTheAllowlistIsAccepted(t *testing.T) {
	t.Parallel()
	for _, sort := range []string{"session_date", "category", "client", "hours", "on_site"} {
		for _, dir := range []string{"asc", "desc"} {
			store := &fakeStore{}
			if _, err := newService(t, store).List(t.Context(), owner(), entries.ListRequest{Sort: ptr(sort), Dir: ptr(dir)}); err != nil {
				t.Errorf("%s %s: %v", sort, dir, err)
			}
			if string(store.query.Sort) != sort || string(store.query.Dir) != dir {
				t.Errorf("%s %s: store got %s %s", sort, dir, store.query.Sort, store.query.Dir)
			}
		}
	}
}

func TestList_DeniesAnActorThatIsNotActive(t *testing.T) {
	t.Parallel()
	for name, actor := range map[string]domain.Actor{
		"zero actor":   {},
		"inactive":     {ID: "u", OrganizationID: "o"},
		"no org":       {ID: "u", Active: true},
		"no identity":  {OrganizationID: "o", Active: true},
		"admin only":   {Admin: true},
		"supervisor":   {Supervisor: true},
		"inactive adm": {ID: "u", OrganizationID: "o", Admin: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store := &fakeStore{}
			_, err := newService(t, store).List(t.Context(), actor, entries.ListRequest{})
			if !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("err = %v, want ErrForbidden", err)
			}
			if store.calls != 0 {
				t.Error("the store ran for a denied actor")
			}
		})
	}
}

func TestList_AdminRoleDoesNotWidenToOtherUsersEntries(t *testing.T) {
	t.Parallel()
	actor := owner()
	actor.Admin, actor.Supervisor = true, true
	store := &fakeStore{}
	if _, err := newService(t, store).List(t.Context(), actor, entries.ListRequest{}); err != nil {
		t.Fatal(err)
	}
	if store.userID != actor.ID {
		t.Errorf("store asked for user %q, want only the actor", store.userID)
	}
}

func TestList_WrapsStoreErrorsAndKeepsThemMatchable(t *testing.T) {
	t.Parallel()
	boom := errors.New("connection reset")
	_, err := newService(t, &fakeStore{err: boom}).List(t.Context(), owner(), entries.ListRequest{})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it to wrap the store error", err)
	}
	if err.Error() == boom.Error() {
		t.Error("the error was not given context")
	}
}
