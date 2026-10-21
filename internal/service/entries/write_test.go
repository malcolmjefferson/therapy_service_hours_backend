package entries_test

import (
	"context"
	"errors"
	"testing"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/entries"
)

// fakeWriter records the call and returns canned results. It never touches a database (GO-17: the
// PostgreSQL adapter is covered end to end in write_store_test.go).
type fakeWriter struct {
	entries.Store
	calls  []string
	userID string
	id     string
	got    entries.Validated
	entry  domain.Entry
	err    error
}

func (f *fakeWriter) GetEntry(_ context.Context, userID, id string) (domain.Entry, error) {
	f.calls, f.userID, f.id = append(f.calls, "get"), userID, id
	return f.entry, f.err
}

func (f *fakeWriter) CreateEntry(_ context.Context, userID string, v entries.Validated) (domain.Entry, error) {
	f.calls, f.userID, f.got = append(f.calls, "create"), userID, v
	return f.entry, f.err
}

func (f *fakeWriter) UpdateEntry(_ context.Context, userID, id string, v entries.Validated) (domain.Entry, error) {
	f.calls, f.userID, f.id, f.got = append(f.calls, "update"), userID, id, v
	return f.entry, f.err
}

func (f *fakeWriter) DeleteEntry(_ context.Context, userID, id string) error {
	f.calls, f.userID, f.id = append(f.calls, "delete"), userID, id
	return f.err
}

const entryID = "00000000-0000-4000-8000-0000000000e1"

func validInput() entries.Input {
	return entries.Input{
		SessionDate: "2026-10-05", StartTime: "09:00", EndTime: "10:15",
		CategoryID: "00000000-0000-4000-8000-0000000000c1",
	}
}

// writes runs one call per write path with the same actor and input.
var writes = map[string]func(*entries.Service, domain.Actor, entries.Input) error{
	"get": func(s *entries.Service, a domain.Actor, _ entries.Input) error {
		_, err := s.Get(context.Background(), a, entryID)
		return err
	},
	"create": func(s *entries.Service, a domain.Actor, in entries.Input) error {
		_, err := s.Create(context.Background(), a, in)
		return err
	},
	"update": func(s *entries.Service, a domain.Actor, in entries.Input) error {
		_, err := s.Update(context.Background(), a, entryID, in)
		return err
	},
	"delete": func(s *entries.Service, a domain.Actor, _ entries.Input) error {
		return s.Delete(context.Background(), a, entryID)
	},
}

func TestWrites_ActOnTheActorsOwnEntriesOnly(t *testing.T) {
	t.Parallel()
	for name, call := range writes {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := &fakeWriter{}
			if err := call(newService(t, f), owner(), validInput()); err != nil {
				t.Fatal(err)
			}
			if f.userID != owner().ID {
				t.Errorf("store was asked for user %q, want the actor %q", f.userID, owner().ID)
			}
		})
	}
}

func TestWrites_UnusableActorsAreForbiddenBeforeTheStore(t *testing.T) {
	t.Parallel()
	inactive := owner()
	inactive.Active = false
	noOrg := owner()
	noOrg.OrganizationID = ""
	for name, call := range writes {
		for who, actor := range map[string]domain.Actor{"deactivated": inactive, "no organization": noOrg, "zero": {}} {
			t.Run(name+" "+who, func(t *testing.T) {
				t.Parallel()
				f := &fakeWriter{}
				if err := call(newService(t, f), actor, validInput()); !errors.Is(err, domain.ErrForbidden) {
					t.Errorf("err = %v, want ErrForbidden", err)
				}
				if len(f.calls) != 0 {
					t.Errorf("store was called: %v", f.calls)
				}
			})
		}
	}
}

func TestAdminAndSupervisorGetNoExtraReachOverEntries(t *testing.T) {
	t.Parallel()
	boss := owner()
	boss.Admin, boss.Supervisor = true, true
	f := &fakeWriter{}
	s := newService(t, f)
	for name, call := range writes {
		if err := call(s, boss, validInput()); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if f.userID != boss.ID {
			t.Errorf("%s: store was asked for user %q, want only the actor's own %q", name, f.userID, boss.ID)
		}
	}
}

func TestCreateAndUpdate_ValidateBeforeTheStore(t *testing.T) {
	t.Parallel()
	bad := validInput()
	bad.EndTime = "09:00"
	for _, name := range []string{"create", "update"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := &fakeWriter{}
			err := writes[name](newService(t, f), owner(), bad)
			var verr *domain.ValidationError
			if !errors.As(err, &verr) {
				t.Fatalf("err = %v, want a ValidationError", err)
			}
			if len(f.calls) != 0 {
				t.Errorf("store was called: %v", f.calls)
			}
		})
	}
}

func TestCreateAndUpdate_PassTheCalculatedValuesToTheStore(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"create", "update"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := &fakeWriter{}
			if err := writes[name](newService(t, f), owner(), validInput()); err != nil {
				t.Fatal(err)
			}
			if f.got.StartMinute != 540 || f.got.EndMinute != 615 || f.got.DurationMinutes != 75 {
				t.Errorf("store got %+v, want 09:00 to 10:15 as 75 minutes", f.got)
			}
		})
	}
}

func TestWrites_StoreErrorsPassThroughForTheHandlerToMap(t *testing.T) {
	t.Parallel()
	overlap := &domain.EntryOverlapError{Conflicting: domain.ConflictingEntry{ID: entryID}}
	for _, want := range []error{domain.ErrNotFound, &domain.EntryLockedError{}, overlap} {
		for name, call := range writes {
			t.Run(name+" "+want.Error(), func(t *testing.T) {
				t.Parallel()
				err := call(newService(t, &fakeWriter{err: want}), owner(), validInput())
				if !errors.Is(err, want) {
					t.Errorf("err = %v, want it to wrap %v", err, want)
				}
			})
		}
	}
	var got *domain.EntryOverlapError
	err := writes["create"](newService(t, &fakeWriter{err: overlap}), owner(), validInput())
	if !errors.As(err, &got) || got.Conflicting.ID != entryID {
		t.Errorf("the overlap lost its detail: %v", err)
	}
}
