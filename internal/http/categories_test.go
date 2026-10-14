package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/apigw"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
)

// S2-08 handler tests: the real router and generated strict server run against a fake categories
// service, and each response is validated against api/openapi.yaml.

const (
	testCategory1 = "00000000-0000-4000-8000-0000000000c1"
	testCategory2 = "00000000-0000-4000-8000-0000000000c2"
	testCategory3 = "00000000-0000-4000-8000-0000000000c3"
)

// fakeCategories records its calls and answers with the configured result.
type fakeCategories struct {
	calls    int
	actor    domain.Actor
	sections []domain.CategorySection
	err      error
}

func (f *fakeCategories) List(_ context.Context, a domain.Actor) ([]domain.CategorySection, error) {
	f.calls, f.actor = f.calls+1, a
	return f.sections, f.err
}

func newCategoriesHarness(t *testing.T) (*clientsHarness, *fakeCategories) {
	t.Helper()
	svc := &fakeCategories{}
	deps := testDeps(&logSink{})
	deps.Categories = svc
	deps.ActorResolver = resolverFunc(func(context.Context, map[string]string) (domain.Actor, error) {
		return domain.Actor{ID: testUserID, OrganizationID: testOrgID, Email: "pseudonym@example.test", Active: true}, nil
	})
	h, err := NewRouter(deps)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	return &clientsHarness{t: t, svc: &fakeClients{}, handler: apigw.Handler(h, ContextWithRequestID), spec: loadSpec(t)}, svc
}

var testSections = []domain.CategorySection{
	{Group: domain.GroupDirect, Label: "Direct Service", Categories: []domain.Category{
		{ID: testCategory1, Name: "Individual Counseling", Group: domain.GroupDirect},
		{ID: testCategory2, Name: "Group Facilitation", Group: domain.GroupDirect},
	}},
	{Group: domain.GroupSupervision, Label: "Supervision", Categories: []domain.Category{
		{ID: testCategory3, Name: "Group Supervision", Group: domain.GroupSupervision},
	}},
}

// returns the groups in order with only id and name per category
func TestCategories_ReturnsTheGroupsInOrderWithOnlyIDAndName(t *testing.T) {
	t.Parallel()
	c, svc := newCategoriesHarness(t)
	svc.sections = testSections
	res := c.send(http.MethodGet, "/api/v1/categories", "", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %s", res.StatusCode, res.Body)
	}
	var got struct {
		Groups []struct {
			Group      string           `json:"group"`
			Label      string           `json:"label"`
			Categories []map[string]any `json:"categories"`
		} `json:"groups"`
	}
	if err := json.Unmarshal([]byte(res.Body), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Groups) != 2 || got.Groups[0].Group != "direct" || got.Groups[0].Label != "Direct Service" || got.Groups[1].Group != "supervision" {
		t.Fatalf("groups = %+v", got.Groups)
	}
	first := got.Groups[0].Categories
	if len(first) != 2 || first[0]["name"] != "Individual Counseling" || first[0]["id"] != testCategory1 || first[1]["name"] != "Group Facilitation" || len(first[0]) != 2 {
		t.Errorf("direct categories = %+v", first)
	}
	if svc.actor.ID != testUserID {
		t.Errorf("service did not receive the resolved actor: %+v", svc.actor)
	}
	c.checkSpec(http.MethodGet, "/api/v1/categories", "", res)
}

// is cacheable by the browser only, unlike every other response
func TestCategories_IsCacheableByTheBrowserOnly(t *testing.T) {
	t.Parallel()
	c, svc := newCategoriesHarness(t)
	svc.sections = testSections
	res := c.send(http.MethodGet, "/api/v1/categories", "", nil)
	if got := res.Headers["Cache-Control"]; got != "private, max-age=3600" {
		t.Errorf("Cache-Control = %q, want private, max-age=3600", got)
	}
	if res.Headers["X-Content-Type-Options"] != "nosniff" || res.Headers["Content-Type"] != "application/json" {
		t.Errorf("security headers = %v", res.Headers)
	}
	// The cache exception is not inherited by anything else (SEC-33, ADR-0012).
	other := c.send(http.MethodGet, "/api/v1/clients", "", nil)
	if other.Headers["Cache-Control"] != "no-store" {
		t.Errorf("clients Cache-Control = %q, want no-store", other.Headers["Cache-Control"])
	}
}

// an empty list is an empty array, not null
func TestCategories_EmptyListIsAnEmptyArray(t *testing.T) {
	t.Parallel()
	c, _ := newCategoriesHarness(t)
	res := c.send(http.MethodGet, "/api/v1/categories", "", nil)
	if res.StatusCode != http.StatusOK || res.Body != "{\"groups\":[]}\n" {
		t.Errorf("status = %d, body %q", res.StatusCode, res.Body)
	}
}

// a request without trusted claims is 401 and never reaches the service
func TestCategories_WithoutTrustedClaimsIs401(t *testing.T) {
	t.Parallel()
	c, svc := newCategoriesHarness(t)
	res := c.sendAs(http.MethodGet, "/api/v1/categories", "", nil, nil)
	if res.StatusCode != http.StatusUnauthorized || svc.calls != 0 {
		t.Errorf("status = %d, service calls = %d", res.StatusCode, svc.calls)
	}
	if res.Headers["Cache-Control"] != "no-store" {
		t.Errorf("401 Cache-Control = %q, want no-store", res.Headers["Cache-Control"])
	}
}

// a forbidden actor gets the forbidden problem, not cached
func TestCategories_ForbiddenActorGetsTheForbiddenProblem(t *testing.T) {
	t.Parallel()
	c, svc := newCategoriesHarness(t)
	svc.err = domain.ErrForbidden
	res := c.send(http.MethodGet, "/api/v1/categories", "", nil)
	if res.StatusCode != http.StatusForbidden || problemOf(t, res)["status"] != float64(http.StatusForbidden) {
		t.Errorf("status = %d, body %s", res.StatusCode, res.Body)
	}
	if res.Headers["Cache-Control"] != "no-store" {
		t.Errorf("403 Cache-Control = %q, want no-store", res.Headers["Cache-Control"])
	}
	c.checkSpec(http.MethodGet, "/api/v1/categories", "", res)
}

// a storage failure is a generic 500 that leaks nothing
func TestCategories_StorageFailureIsAGeneric500(t *testing.T) {
	t.Parallel()
	c, svc := newCategoriesHarness(t)
	svc.err = errors.New("list categories: password authentication failed for user app_rw")
	res := c.send(http.MethodGet, "/api/v1/categories", "", nil)
	if res.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d", res.StatusCode)
	}
	if got := res.Body; got == "" || strings.Contains(got, "password") || strings.Contains(got, "app_rw") {
		t.Errorf("500 body leaks internals: %s", got)
	}
	if res.Headers["Cache-Control"] != "no-store" {
		t.Errorf("500 Cache-Control = %q, want no-store", res.Headers["Cache-Control"])
	}
}

// rejects a write method
func TestCategories_RejectsAWriteMethod(t *testing.T) {
	t.Parallel()
	c, svc := newCategoriesHarness(t)
	res := c.send(http.MethodPost, "/api/v1/categories", "{}", nil)
	if res.StatusCode != http.StatusMethodNotAllowed || svc.calls != 0 {
		t.Errorf("status = %d, calls = %d", res.StatusCode, svc.calls)
	}
}
