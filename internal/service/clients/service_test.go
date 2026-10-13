package clients_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/clients"
)

// Fictional pseudonyms only (CLAUDE.md §0.7).

type call struct {
	op            string
	user, id, nm  string
	limit, offset int32
	detach        bool
}

type fakeStore struct {
	calls []call
	err   error
}

func (f *fakeStore) List(_ context.Context, user string, limit, offset int32) ([]domain.Client, int64, error) {
	f.calls = append(f.calls, call{op: "list", user: user, limit: limit, offset: offset})
	return []domain.Client{{ID: "1", Name: "A"}}, 7, f.err
}

func (f *fakeStore) Create(_ context.Context, user, name string) (domain.Client, error) {
	f.calls = append(f.calls, call{op: "create", user: user, nm: name})
	return domain.Client{ID: "1", Name: name}, f.err
}

func (f *fakeStore) Rename(_ context.Context, user, id, name string) (domain.Client, error) {
	f.calls = append(f.calls, call{op: "rename", user: user, id: id, nm: name})
	return domain.Client{ID: id, Name: name}, f.err
}

func (f *fakeStore) Delete(_ context.Context, user, id string, detach bool) error {
	f.calls = append(f.calls, call{op: "delete", user: user, id: id, detach: detach})
	return f.err
}

var active = domain.Actor{ID: "user-1", OrganizationID: "org-1", Active: true}

func newService(t *testing.T) (*clients.Service, *fakeStore) {
	t.Helper()
	store := &fakeStore{}
	svc, err := clients.New(store)
	if err != nil {
		t.Fatal(err)
	}
	return svc, store
}

func TestNewRequiresAStore(t *testing.T) {
	t.Parallel()
	if _, err := clients.New(nil); err == nil {
		t.Fatal("New(nil) succeeded")
	}
}

func TestEveryOperationRequiresAnActiveActor(t *testing.T) {
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
			svc, store := newService(t)
			ctx := t.Context()
			_, errList := svc.List(ctx, actor, 1, 50)
			_, errCreate := svc.Create(ctx, actor, "A.B.")
			_, errRename := svc.Rename(ctx, actor, "c1", "A.B.")
			errDelete := svc.Delete(ctx, actor, "c1", true)
			for op, err := range map[string]error{"list": errList, "create": errCreate, "rename": errRename, "delete": errDelete} {
				if !errors.Is(err, domain.ErrForbidden) {
					t.Errorf("%s: err = %v, want ErrForbidden", op, err)
				}
			}
			if len(store.calls) != 0 {
				t.Errorf("the store was reached: %+v", store.calls)
			}
		})
	}
}

func TestAdminAndSupervisorRolesGrantNoAccessToOthersClients(t *testing.T) {
	t.Parallel()
	svc, store := newService(t)
	actor := domain.Actor{ID: "admin-1", OrganizationID: "org-1", Active: true, Admin: true, Supervisor: true}
	if err := svc.Delete(t.Context(), actor, "someone-elses", true); err != nil {
		t.Fatal(err)
	}
	// Every call is scoped to the actor's own id, so a role cannot widen it.
	if got := store.calls[0]; got.user != "admin-1" {
		t.Errorf("store call scoped to %q, want the actor's own id", got.user)
	}
}

func TestListPaging(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                string
		page, size          int32
		wantPage, wantSize  int32
		wantLimit, wantSkip int32
	}{
		{"defaults", 0, 0, 1, 50, 50, 0},
		{"negative values use defaults", -3, -1, 1, 50, 50, 0},
		{"second page", 2, 10, 2, 10, 10, 10},
		{"page size is capped", 1, 5000, 1, 200, 200, 0},
		{"the largest page the spec allows", 100000, 200, 100000, 200, 200, 19999800},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			svc, store := newService(t)
			got, err := svc.List(t.Context(), active, tt.page, tt.size)
			if err != nil {
				t.Fatal(err)
			}
			if got.Page != tt.wantPage || got.PageSize != tt.wantSize || got.Total != 7 || len(got.Items) != 1 {
				t.Errorf("result = %+v", got)
			}
			if c := store.calls[0]; c.limit != tt.wantLimit || c.offset != tt.wantSkip || c.user != "user-1" {
				t.Errorf("store call = %+v, want limit %d offset %d", c, tt.wantLimit, tt.wantSkip)
			}
		})
	}
}

func TestNameValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		input   string
		want    string // stored name when valid
		wantMsg string // validation message when invalid
	}{
		{name: "initials", input: "A.B.", want: "A.B."},
		{name: "surrounding space is trimmed", input: "  Group C \t", want: "Group C"},
		{name: "inner space is kept", input: "Group  C", want: "Group  C"},
		{name: "exactly 64 characters", input: strings.Repeat("x", 64), want: strings.Repeat("x", 64)},
		{name: "64 multi-byte characters", input: strings.Repeat("名", 64), want: strings.Repeat("名", 64)},
		{name: "sql-looking text is just text", input: `'; DROP TABLE clients; --`, want: `'; DROP TABLE clients; --`},
		{name: "empty", input: "", wantMsg: "is required"},
		{name: "only spaces", input: "   \t ", wantMsg: "is required"},
		{name: "65 characters", input: strings.Repeat("x", 65), wantMsg: "must be at most 64 characters"},
		{name: "65 multi-byte characters", input: strings.Repeat("名", 65), wantMsg: "must be at most 64 characters"},
		{name: "control character", input: "A\x00B", wantMsg: "must not contain control characters"},
		{name: "newline inside", input: "A\nB", wantMsg: "must not contain control characters"},
		{name: "invalid UTF-8", input: "A\xffB", wantMsg: "must be valid text"},
	}
	for _, tt := range tests {
		for _, op := range []string{"create", "rename"} {
			t.Run(op+" "+tt.name, func(t *testing.T) {
				t.Parallel()
				svc, store := newService(t)
				var got domain.Client
				var err error
				if op == "create" {
					got, err = svc.Create(t.Context(), active, tt.input)
				} else {
					got, err = svc.Rename(t.Context(), active, "c1", tt.input)
				}
				if tt.wantMsg == "" {
					if err != nil || got.Name != tt.want || store.calls[0].nm != tt.want {
						t.Fatalf("got %+v, %v; store call %+v", got, err, store.calls)
					}
					return
				}
				var verr *domain.ValidationError
				if !errors.As(err, &verr) || len(verr.Fields) != 1 || verr.Fields[0].Field != "name" || verr.Fields[0].Message != tt.wantMsg {
					t.Fatalf("err = %v, want a name validation error %q", err, tt.wantMsg)
				}
				if len(store.calls) != 0 {
					t.Errorf("the store was reached with an invalid name: %+v", store.calls)
				}
				if strings.Contains(err.Error(), tt.input) && tt.input != "" {
					t.Errorf("the error echoes the submitted name: %v", err)
				}
			})
		}
	}
}

func TestRenameAndDeletePassTheIDsAndFlagsThrough(t *testing.T) {
	t.Parallel()
	svc, store := newService(t)
	if _, err := svc.Rename(t.Context(), active, "client-9", "New"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(t.Context(), active, "client-9", false); err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(t.Context(), active, "client-9", true); err != nil {
		t.Fatal(err)
	}
	want := []call{
		{op: "rename", user: "user-1", id: "client-9", nm: "New"},
		{op: "delete", user: "user-1", id: "client-9"},
		{op: "delete", user: "user-1", id: "client-9", detach: true},
	}
	for i, w := range want {
		if store.calls[i] != w {
			t.Errorf("call %d = %+v, want %+v", i, store.calls[i], w)
		}
	}
}

func TestStoreErrorsPassThroughUnchanged(t *testing.T) {
	t.Parallel()
	taken := &domain.ClientNameTakenError{Existing: domain.Client{ID: "1", Name: "A"}}
	inUse := &domain.ClientInUseError{EntryCount: 2}
	for _, want := range []error{domain.ErrNotFound, taken, inUse} {
		svc, store := newService(t)
		store.err = want
		if _, err := svc.Create(t.Context(), active, "A"); !errors.Is(err, want) {
			t.Errorf("create: err = %v, want %v", err, want)
		}
		if _, err := svc.Rename(t.Context(), active, "1", "A"); !errors.Is(err, want) {
			t.Errorf("rename: err = %v, want %v", err, want)
		}
		if err := svc.Delete(t.Context(), active, "1", false); !errors.Is(err, want) {
			t.Errorf("delete: err = %v, want %v", err, want)
		}
		if _, err := svc.List(t.Context(), active, 1, 1); !errors.Is(err, want) {
			t.Errorf("list: err = %v, want %v", err, want)
		}
	}
	if !errors.Is(taken, domain.ErrConflict) || !errors.Is(inUse, domain.ErrConflict) {
		t.Error("client conflicts must match domain.ErrConflict")
	}
}
