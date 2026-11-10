package goals_test

import (
	"context"
	"errors"
	"testing"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/goals"
)

type fakeStore struct {
	rows    []domain.Goal
	minutes domain.ProgressMinutes
	err     error
	calls   int
	set     struct {
		user  string
		goal  domain.GoalType
		hours int32
	}
}

func (f *fakeStore) Progress(context.Context, string) ([]domain.Goal, domain.ProgressMinutes, error) {
	f.calls++
	return f.rows, f.minutes, f.err
}

func (f *fakeStore) SetTarget(_ context.Context, user string, goal domain.GoalType, hours int32) (domain.Goal, error) {
	f.calls++
	f.set.user, f.set.goal, f.set.hours = user, goal, hours
	return domain.Goal{Type: goal, TargetHours: &hours}, f.err
}

var active = domain.Actor{ID: "user-1", OrganizationID: "org-1", Active: true}

func i32(v int32) *int32 { return &v }

func defaults() []domain.Goal {
	return []domain.Goal{
		{Type: domain.GoalOnSite},
		{Type: domain.GoalTotalActivity, TargetHours: i32(3000)},
		{Type: domain.GoalDirect, TargetHours: i32(1200)},
		{Type: domain.GoalSupervision, TargetHours: i32(100), LastCelebratedPercent: 25},
	}
}

func newService(t *testing.T, store *fakeStore) *goals.Service {
	t.Helper()
	svc, err := goals.New(store)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func TestNewRequiresAStore(t *testing.T) {
	t.Parallel()
	if _, err := goals.New(nil); err == nil {
		t.Fatal("New(nil) succeeded")
	}
}

func TestEveryOperationRequiresAnActiveActor(t *testing.T) {
	t.Parallel()
	for name, actor := range map[string]domain.Actor{
		"zero": {}, "inactive": {ID: "u", OrganizationID: "o"}, "no org": {ID: "u", Active: true}, "no id": {OrganizationID: "o", Active: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store := &fakeStore{rows: defaults()}
			svc := newService(t, store)
			if _, err := svc.Progress(t.Context(), actor); !errors.Is(err, domain.ErrForbidden) {
				t.Errorf("Progress err = %v, want ErrForbidden", err)
			}
			if _, err := svc.SetTarget(t.Context(), actor, domain.GoalDirect, 10); !errors.Is(err, domain.ErrForbidden) {
				t.Errorf("SetTarget err = %v, want ErrForbidden", err)
			}
			if store.calls != 0 {
				t.Errorf("store was called %d times for a refused actor", store.calls)
			}
		})
	}
}

// wantCard checks a card's type, target, hours and percents; a nil percent means "no target".
func wantCard(t *testing.T, c goals.Card, typ domain.GoalType, target *int32, approved, pending float64, approvedPct *int32) {
	t.Helper()
	if c.Type != typ || c.Hours.Approved != approved || c.Hours.Pending != pending {
		t.Errorf("%s card = %+v, want hours %v / %v", typ, c, approved, pending)
	}
	if (c.TargetHours == nil) != (target == nil) || (target != nil && *c.TargetHours != *target) {
		t.Errorf("%s target = %v, want %v", typ, c.TargetHours, target)
	}
	if (c.ApprovedPercent == nil) != (approvedPct == nil) || (approvedPct != nil && *c.ApprovedPercent != *approvedPct) {
		t.Errorf("%s approved percent = %v, want %v", typ, c.ApprovedPercent, approvedPct)
	}
	if (c.PendingPercent == nil) != (approvedPct == nil) {
		t.Errorf("%s pending percent = %v, nil only without a target", typ, c.PendingPercent)
	}
}

func TestProgress_ReturnsTheFourGoalsInDisplayOrderWithHoursAndPercents(t *testing.T) {
	t.Parallel()
	store := &fakeStore{rows: defaults(), minutes: domain.ProgressMinutes{
		Direct:        domain.Minutes{Approved: 1500, Pending: 75}, // 25 h, 1.25 h of 1200 h
		Supervision:   domain.Minutes{Approved: 135, Pending: 75},
		TotalActivity: domain.Minutes{Approved: 1635, Pending: 150},
		OnSite:        domain.Minutes{Approved: 120, Pending: 75},
	}}
	cards, err := newService(t, store).Progress(t.Context(), active)
	if err != nil || len(cards) != 4 {
		t.Fatalf("cards = %+v, %v", cards, err)
	}
	wantCard(t, cards[0], domain.GoalDirect, i32(1200), 25, 1.25, i32(2))
	wantCard(t, cards[1], domain.GoalSupervision, i32(100), 2.25, 1.25, i32(2))
	wantCard(t, cards[2], domain.GoalTotalActivity, i32(3000), 27.25, 2.5, i32(0))
	wantCard(t, cards[3], domain.GoalOnSite, nil, 2, 1.25, nil)
	if cards[1].LastCelebratedPercent != 25 {
		t.Errorf("last celebrated = %d, want 25", cards[1].LastCelebratedPercent)
	}
	if store.calls != 1 {
		t.Errorf("store calls = %d, want 1 (no N+1)", store.calls)
	}
}

func TestProgress_OnlyTheSupervisionCardCarriesTheGroupIndividualSplit(t *testing.T) {
	t.Parallel()
	store := &fakeStore{rows: defaults(), minutes: domain.ProgressMinutes{
		SupervisionGroup:      domain.Minutes{Approved: 75, Pending: 30},
		SupervisionIndividual: domain.Minutes{Approved: 60, Pending: 45},
	}}
	cards, err := newService(t, store).Progress(t.Context(), active)
	if err != nil {
		t.Fatal(err)
	}
	sup := cards[1]
	if sup.SupervisionGroup == nil || sup.SupervisionIndividual == nil {
		t.Fatal("supervision card has no split")
	}
	if *sup.SupervisionGroup != (goals.HourTotals{Approved: 1.25, Pending: 0.5}) || *sup.SupervisionIndividual != (goals.HourTotals{Approved: 1, Pending: 0.75}) {
		t.Errorf("split = %+v / %+v", sup.SupervisionGroup, sup.SupervisionIndividual)
	}
	for _, i := range []int{0, 2, 3} {
		if cards[i].SupervisionGroup != nil || cards[i].SupervisionIndividual != nil {
			t.Errorf("%s carries a split", cards[i].Type)
		}
	}
}

func TestProgress_ANewUserHasZeroProgressAndNoNilTargets(t *testing.T) {
	t.Parallel()
	cards, err := newService(t, &fakeStore{rows: defaults()}).Progress(t.Context(), active)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cards[:3] {
		if c.Hours.Approved != 0 || c.Hours.Pending != 0 || c.ApprovedPercent == nil || *c.ApprovedPercent != 0 {
			t.Errorf("%s = %+v", c.Type, c)
		}
	}
}

func TestProgress_OverTargetIsNotCapped(t *testing.T) {
	t.Parallel()
	store := &fakeStore{rows: defaults(), minutes: domain.ProgressMinutes{Supervision: domain.Minutes{Approved: 6500}}}
	cards, err := newService(t, store).Progress(t.Context(), active)
	if err != nil {
		t.Fatal(err)
	}
	if got := *cards[1].ApprovedPercent; got != 108 {
		t.Errorf("approved percent = %d, want 108", got)
	}
}

func TestProgress_StoredGoalsMustBeComplete(t *testing.T) {
	t.Parallel()
	store := &fakeStore{rows: defaults()[:3]}
	if _, err := newService(t, store).Progress(t.Context(), active); err == nil {
		t.Fatal("incomplete goals did not fail")
	}
}

func TestProgress_StoreErrorsPass(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	if _, err := newService(t, &fakeStore{err: boom}).Progress(t.Context(), active); !errors.Is(err, boom) {
		t.Errorf("err = %v", err)
	}
}

func TestSetTarget_StoresAValidTargetForTheActorOnly(t *testing.T) {
	t.Parallel()
	for _, hours := range []int32{1, 1500, goals.MaxTargetHours} {
		store := &fakeStore{}
		got, err := newService(t, store).SetTarget(t.Context(), active, domain.GoalSupervision, hours)
		if err != nil || got.TargetHours == nil || *got.TargetHours != hours {
			t.Fatalf("hours %d: %+v, %v", hours, got, err)
		}
		if store.set.user != active.ID || store.set.goal != domain.GoalSupervision || store.set.hours != hours {
			t.Errorf("store got %+v", store.set)
		}
	}
}

func TestSetTarget_RefusesBadInputBeforeTouchingStorage(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		goal  domain.GoalType
		hours int32
		field []string
	}{
		{"zero", domain.GoalDirect, 0, []string{"target_hours"}},
		{"negative", domain.GoalDirect, -5, []string{"target_hours"}},
		{"too large", domain.GoalDirect, goals.MaxTargetHours + 1, []string{"target_hours"}},
		{"on-site", domain.GoalOnSite, 100, []string{"goal_type"}},
		{"on-site and bad hours", domain.GoalOnSite, 0, []string{"goal_type", "target_hours"}},
		{"unknown goal", domain.GoalType("lifetime"), 10, []string{"goal_type"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := &fakeStore{}
			_, err := newService(t, store).SetTarget(t.Context(), active, tc.goal, tc.hours)
			var verr *domain.ValidationError
			if !errors.As(err, &verr) {
				t.Fatalf("err = %v, want ValidationError", err)
			}
			if len(verr.Fields) != len(tc.field) {
				t.Fatalf("fields = %+v, want %v", verr.Fields, tc.field)
			}
			for i, f := range tc.field {
				if verr.Fields[i].Field != f {
					t.Errorf("field %d = %q, want %q", i, verr.Fields[i].Field, f)
				}
			}
			if store.calls != 0 {
				t.Error("store was called")
			}
		})
	}
}
