package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/apigw"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/goals"
)

// S3-03 handler tests: the real router and generated strict server run against a fake goals
// service, and each response is validated against api/openapi.yaml.

type fakeGoals struct {
	calls    int
	actor    domain.Actor
	cards    []goals.Card
	goal     domain.GoalType
	hours    int32
	err      error
	progress int
}

func (f *fakeGoals) Progress(_ context.Context, a domain.Actor) ([]goals.Card, error) {
	f.calls, f.progress, f.actor = f.calls+1, f.progress+1, a
	return f.cards, f.err
}

func (f *fakeGoals) SetTarget(_ context.Context, a domain.Actor, g domain.GoalType, h int32) (domain.Goal, error) {
	f.calls, f.actor, f.goal, f.hours = f.calls+1, a, g, h
	if f.err != nil {
		return domain.Goal{}, f.err
	}
	return domain.Goal{Type: g, TargetHours: &h}, nil
}

func newGoalsHarness(t *testing.T) (*clientsHarness, *fakeGoals) {
	t.Helper()
	svc := &fakeGoals{}
	deps := testDeps(&logSink{})
	deps.Goals = svc
	deps.MaxBodyBytes = 4096
	deps.ActorResolver = resolverFunc(func(context.Context, map[string]string) (domain.Actor, error) {
		return domain.Actor{ID: testUserID, OrganizationID: testOrgID, Email: "pseudonym@example.test", Active: true}, nil
	})
	h, err := NewRouter(deps)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	return &clientsHarness{t: t, svc: &fakeClients{}, handler: apigw.Handler(h, ContextWithRequestID), spec: loadSpec(t)}, svc
}

func ptr[T any](v T) *T { return &v }

func testCards() []goals.Card {
	return []goals.Card{
		{Type: domain.GoalDirect, TargetHours: ptr(int32(1200)), Hours: goals.HourTotals{Approved: 25, Pending: 1.25}, ApprovedPercent: ptr(int32(2)), PendingPercent: ptr(int32(0))},
		{Type: domain.GoalSupervision, TargetHours: ptr(int32(100)), Hours: goals.HourTotals{Approved: 2.25, Pending: 1.25}, ApprovedPercent: ptr(int32(2)), PendingPercent: ptr(int32(1)), LastCelebratedPercent: 25,
			SupervisionGroup: &goals.HourTotals{Approved: 1.25, Pending: 0.5}, SupervisionIndividual: &goals.HourTotals{Approved: 1, Pending: 0.75}},
		{Type: domain.GoalTotalActivity, TargetHours: ptr(int32(3000)), Hours: goals.HourTotals{Approved: 27.25, Pending: 2.5}, ApprovedPercent: ptr(int32(0)), PendingPercent: ptr(int32(0))},
		{Type: domain.GoalOnSite, Hours: goals.HourTotals{Approved: 2, Pending: 1.25}},
	}
}

func TestGoalsProgress_ReturnsTheFourCardsWithNullsForOnSiteAndTheSupervisionSplit(t *testing.T) {
	t.Parallel()
	c, svc := newGoalsHarness(t)
	svc.cards = testCards()
	res := c.send(http.MethodGet, "/api/v1/goals/progress", "", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %s", res.StatusCode, res.Body)
	}
	var got struct {
		Goals []map[string]any `json:"goals"`
	}
	if err := json.Unmarshal([]byte(res.Body), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Goals) != 4 {
		t.Fatalf("goals = %+v", got.Goals)
	}
	sup, onSite := got.Goals[1], got.Goals[3]
	split, _ := sup["supervision_split"].(map[string]any)
	if sup["goal_type"] != "supervision" || split == nil || split["group"].(map[string]any)["approved_hours"] != 1.25 || split["individual"].(map[string]any)["pending_hours"] != 0.75 {
		t.Errorf("supervision = %+v", sup)
	}
	if _, has := got.Goals[0]["supervision_split"]; has {
		t.Error("only supervision carries the split")
	}
	for _, k := range []string{"target_hours", "approved_percent", "pending_percent"} {
		if v, ok := onSite[k]; !ok || v != nil {
			t.Errorf("on-site %s = %v (present %v), want an explicit null", k, v, ok)
		}
	}
	if onSite["approved_hours"] != 2.0 || svc.actor.ID != testUserID {
		t.Errorf("on-site = %+v, actor %+v", onSite, svc.actor)
	}
	c.checkSpec(http.MethodGet, "/api/v1/goals/progress", "", res)
}

func TestGoalsProgress_WithoutClaimsIs401AndForbiddenIs403(t *testing.T) {
	t.Parallel()
	c, svc := newGoalsHarness(t)
	if res := c.sendAs(http.MethodGet, "/api/v1/goals/progress", "", nil, nil); res.StatusCode != http.StatusUnauthorized || svc.calls != 0 {
		t.Errorf("status = %d, calls = %d", res.StatusCode, svc.calls)
	}
	svc.err = domain.ErrForbidden
	res := c.send(http.MethodGet, "/api/v1/goals/progress", "", nil)
	if res.StatusCode != http.StatusForbidden || res.Headers["Cache-Control"] != "no-store" {
		t.Errorf("status = %d, cache %q", res.StatusCode, res.Headers["Cache-Control"])
	}
}

func TestUpdateGoalTarget_SetsTheTargetForTheNamedGoal(t *testing.T) {
	t.Parallel()
	c, svc := newGoalsHarness(t)
	body := `{"target_hours": 1500}`
	res := c.send(http.MethodPut, "/api/v1/goals/direct", body, nil)
	if res.StatusCode != http.StatusOK || res.Body != "{\"goal_type\":\"direct\",\"target_hours\":1500}\n" {
		t.Fatalf("status = %d, body %q", res.StatusCode, res.Body)
	}
	if svc.goal != domain.GoalDirect || svc.hours != 1500 || svc.actor.ID != testUserID {
		t.Errorf("service got %v %d %+v", svc.goal, svc.hours, svc.actor)
	}
	c.checkSpec(http.MethodPut, "/api/v1/goals/direct", body, res)
}

func TestUpdateGoalTarget_RefusesBadRequestsBeforeTheService(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct{ path, body string }{
		"zero":            {"/api/v1/goals/direct", `{"target_hours": 0}`},
		"negative":        {"/api/v1/goals/direct", `{"target_hours": -3}`},
		"fraction":        {"/api/v1/goals/direct", `{"target_hours": 10.5}`},
		"string":          {"/api/v1/goals/direct", `{"target_hours": "10"}`},
		"null":            {"/api/v1/goals/direct", `{"target_hours": null}`},
		"too large":       {"/api/v1/goals/direct", `{"target_hours": 100001}`},
		"missing":         {"/api/v1/goals/direct", `{}`},
		"unknown field":   {"/api/v1/goals/direct", `{"target_hours": 10, "last_celebrated_percent": 100}`},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c, svc := newGoalsHarness(t)
			res := c.send(http.MethodPut, tc.path, tc.body, nil)
			if res.StatusCode != http.StatusBadRequest || svc.calls != 0 {
				t.Errorf("status = %d, calls = %d, body %s", res.StatusCode, svc.calls, res.Body)
			}
			if problemOf(t, res)["status"] != float64(400) {
				t.Errorf("problem = %s", res.Body)
			}
		})
	}
}

func TestUpdateGoalTarget_OnSiteIs400WithTheGoalTypeField(t *testing.T) {
	t.Parallel()
	c, svc := newGoalsHarness(t)
	svc.err = domain.NewValidationError(domain.FieldError{Field: "goal_type", Message: "Total Time On-site is tracked only and has no target"})
	body := `{"target_hours": 100}`
	res := c.send(http.MethodPut, "/api/v1/goals/on_site", body, nil)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, body %s", res.StatusCode, res.Body)
	}
	errs, _ := problemOf(t, res)["errors"].([]any)
	if len(errs) != 1 || errs[0].(map[string]any)["field"] != "goal_type" {
		t.Errorf("errors = %v", errs)
	}
	c.checkSpec(http.MethodPut, "/api/v1/goals/on_site", body, res)
}

func TestUpdateGoalTarget_WithoutClaimsIs401(t *testing.T) {
	t.Parallel()
	c, svc := newGoalsHarness(t)
	res := c.sendAs(http.MethodPut, "/api/v1/goals/direct", `{"target_hours": 10}`, nil, nil)
	if res.StatusCode != http.StatusUnauthorized || svc.calls != 0 {
		t.Errorf("status = %d, calls = %d", res.StatusCode, svc.calls)
	}
}

// memGoalStore is a store that must never be reached for the requests below.
type memGoalStore struct{ calls int }

func (m *memGoalStore) Progress(context.Context, string) ([]domain.Goal, domain.ProgressMinutes, error) {
	m.calls++
	return nil, domain.ProgressMinutes{}, nil
}

func (m *memGoalStore) SetTarget(context.Context, string, domain.GoalType, int32) (domain.Goal, error) {
	m.calls++
	return domain.Goal{}, nil
}

// The path names a goal type, never a user or a row, so another user's goals cannot be addressed.
// An unknown name (including a user id) is refused by the real service before any storage call.
func TestUpdateGoalTarget_AnUnknownGoalNameIs400AndReachesNoStorage(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"lifetime", "00000000-0000-4000-8000-000000000009", "DIRECT"} {
		store := &memGoalStore{}
		svc, err := goals.New(store)
		if err != nil {
			t.Fatal(err)
		}
		deps := testDeps(&logSink{})
		deps.Goals = svc
		deps.MaxBodyBytes = 4096
		deps.ActorResolver = resolverFunc(func(context.Context, map[string]string) (domain.Actor, error) {
			return domain.Actor{ID: testUserID, OrganizationID: testOrgID, Active: true}, nil
		})
		h, err := NewRouter(deps)
		if err != nil {
			t.Fatal(err)
		}
		c := &clientsHarness{t: t, svc: &fakeClients{}, handler: apigw.Handler(h, ContextWithRequestID), spec: loadSpec(t)}
		res := c.send(http.MethodPut, "/api/v1/goals/"+name, `{"target_hours": 10}`, nil)
		if res.StatusCode != http.StatusBadRequest || store.calls != 0 {
			t.Errorf("%s: status = %d, store calls = %d, body %s", name, res.StatusCode, store.calls, res.Body)
		}
	}
}
