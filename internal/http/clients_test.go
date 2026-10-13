package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/aws/aws-lambda-go/events"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers/gorillamux"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/apigw"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
)

// S2-07 handler tests: the real router, middleware and generated strict server run against a fake
// clients service, and each response is validated against api/openapi.yaml. Names are fictional
// pseudonyms (CLAUDE.md §0.7).

const (
	testUserID  = "00000000-0000-4000-8000-000000000002"
	testOrgID   = "00000000-0000-4000-8000-000000000001"
	testClient1 = "00000000-0000-4000-8000-0000000000a1"
	testClient2 = "00000000-0000-4000-8000-0000000000a2"
	testKey     = "00000000-0000-4000-8000-0000000000f1"
)

// fakeClients records its calls and answers with the configured results.
type fakeClients struct {
	calls []string
	actor domain.Actor

	page      domain.ClientPage
	client    domain.Client
	err       error
	gotPage   [2]int32
	gotName   string
	gotID     string
	gotDetach bool
}

func (f *fakeClients) List(_ context.Context, a domain.Actor, page, size int32) (domain.ClientPage, error) {
	f.calls, f.actor, f.gotPage = append(f.calls, "list"), a, [2]int32{page, size}
	return f.page, f.err
}

func (f *fakeClients) Create(_ context.Context, a domain.Actor, name string) (domain.Client, error) {
	f.calls, f.actor, f.gotName = append(f.calls, "create"), a, name
	return f.client, f.err
}

func (f *fakeClients) Rename(_ context.Context, a domain.Actor, id, name string) (domain.Client, error) {
	f.calls, f.actor, f.gotID, f.gotName = append(f.calls, "rename"), a, id, name
	return f.client, f.err
}

func (f *fakeClients) Delete(_ context.Context, a domain.Actor, id string, detach bool) error {
	f.calls, f.actor, f.gotID, f.gotDetach = append(f.calls, "delete"), a, id, detach
	return f.err
}

// clientsHarness sends API Gateway events through the real router and checks responses against the spec.
type clientsHarness struct {
	t       *testing.T
	svc     *fakeClients
	handler func(context.Context, events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error)
	spec    *openapi3.T
}

func newClientsHarness(t *testing.T) *clientsHarness {
	t.Helper()
	svc := &fakeClients{}
	deps := testDeps(&logSink{})
	deps.MaxBodyBytes = 4096
	deps.Clients = svc
	deps.ActorResolver = resolverFunc(func(context.Context, map[string]string) (domain.Actor, error) {
		return domain.Actor{ID: testUserID, OrganizationID: testOrgID, Email: "pseudonym@example.test", Active: true}, nil
	})
	h, err := NewRouter(deps)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	return &clientsHarness{t: t, svc: svc, handler: apigw.Handler(h, ContextWithRequestID), spec: loadSpec(t)}
}

// send sends one request as an authenticated caller. headers may add or override request headers.
func (c *clientsHarness) send(method, target, body string, headers map[string]string) events.APIGatewayV2HTTPResponse {
	c.t.Helper()
	return c.sendAs(method, target, body, headers, map[string]string{"sub": "trusted-sub"})
}

func (c *clientsHarness) sendAs(method, target, body string, headers, claims map[string]string) events.APIGatewayV2HTTPResponse {
	c.t.Helper()
	u, err := url.Parse(target)
	if err != nil {
		c.t.Fatal(err)
	}
	e := events.APIGatewayV2HTTPRequest{RawPath: u.Path, RawQueryString: u.RawQuery, Body: body, Headers: map[string]string{"content-type": "application/json"}}
	for k, v := range headers {
		e.Headers[k] = v
	}
	e.RequestContext.HTTP.Method = method
	if claims != nil {
		e.RequestContext.Authorizer = &events.APIGatewayV2HTTPRequestContextAuthorizerDescription{JWT: &events.APIGatewayV2HTTPRequestContextAuthorizerJWTDescription{Claims: claims}}
	}
	res, err := c.handler(c.t.Context(), e)
	if err != nil {
		c.t.Fatalf("handler: %v", err)
	}
	return res
}

// checkSpec validates the response against the operation's documented responses.
func (c *clientsHarness) checkSpec(method, target string, body string, res events.APIGatewayV2HTTPResponse) {
	c.t.Helper()
	spec := c.spec
	router, err := gorillamux.NewRouter(spec)
	if err != nil {
		c.t.Fatalf("spec router: %v", err)
	}
	req := httptest.NewRequestWithContext(c.t.Context(), method, "http://example.test"+target, strings.NewReader(body))
	route, params, err := router.FindRoute(req)
	if err != nil {
		c.t.Fatalf("route not in spec: %v", err)
	}
	header := http.Header{}
	for k, v := range res.Headers {
		header.Set(k, v)
	}
	input := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: &openapi3filter.RequestValidationInput{Request: req, PathParams: params, Route: route},
		Status:                 res.StatusCode,
		Header:                 header,
		Body:                   io.NopCloser(bytes.NewReader([]byte(res.Body))),
		Options:                &openapi3filter.Options{IncludeResponseStatus: true},
	}
	if err := openapi3filter.ValidateResponse(context.Background(), input); err != nil {
		c.t.Errorf("response %d does not match the spec: %v\nbody: %s", res.StatusCode, err, res.Body)
	}
}

func problemOf(t *testing.T, res events.APIGatewayV2HTTPResponse) map[string]any {
	t.Helper()
	if ct := res.Headers["Content-Type"]; ct != "application/problem+json" {
		t.Fatalf("content type = %q, want application/problem+json (body %s)", ct, res.Body)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(res.Body), &m); err != nil {
		t.Fatalf("problem body: %v", err)
	}
	return m
}

func keyed() map[string]string { return map[string]string{"Idempotency-Key": testKey} }

func TestClients_List(t *testing.T) {
	t.Parallel()
	t.Run("returns the page and total", func(t *testing.T) {
		t.Parallel()
		c := newClientsHarness(t)
		c.svc.page = domain.ClientPage{Items: []domain.Client{{ID: testClient1, Name: "A.B."}, {ID: testClient2, Name: "Group C"}}, Page: 2, PageSize: 2, Total: 5}
		res := c.send(http.MethodGet, "/api/v1/clients?page=2&page_size=2", "", nil)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, body %s", res.StatusCode, res.Body)
		}
		var got struct {
			Items []map[string]any `json:"items"`
			Page  map[string]any   `json:"page"`
		}
		if err := json.Unmarshal([]byte(res.Body), &got); err != nil {
			t.Fatal(err)
		}
		if len(got.Items) != 2 || got.Items[0]["name"] != "A.B." || got.Items[0]["id"] != testClient1 || len(got.Items[0]) != 2 {
			t.Errorf("items = %+v", got.Items)
		}
		if got.Page["page"] != float64(2) || got.Page["page_size"] != float64(2) || got.Page["total_items"] != float64(5) {
			t.Errorf("page = %+v", got.Page)
		}
		if c.svc.gotPage != [2]int32{2, 2} {
			t.Errorf("service got page/size %v, want [2 2]", c.svc.gotPage)
		}
		if c.svc.actor.ID != testUserID {
			t.Errorf("service did not receive the resolved actor: %+v", c.svc.actor)
		}
		if res.Headers["Cache-Control"] != "no-store" {
			t.Error("client list is cacheable")
		}
		c.checkSpec(http.MethodGet, "/api/v1/clients?page=2&page_size=2", "", res)
	})
}

func TestClients_ListEdgeCases(t *testing.T) {
	t.Parallel()
	t.Run("an empty list is an empty array", func(t *testing.T) {
		t.Parallel()
		c := newClientsHarness(t)
		c.svc.page = domain.ClientPage{Items: nil, Page: 1, PageSize: 50}
		res := c.send(http.MethodGet, "/api/v1/clients", "", nil)
		if !strings.Contains(res.Body, `"items":[]`) {
			t.Errorf("body = %s, want an empty items array", res.Body)
		}
		if c.svc.gotPage != [2]int32{0, 0} {
			t.Errorf("without parameters the service should choose defaults, got %v", c.svc.gotPage)
		}
		c.checkSpec(http.MethodGet, "/api/v1/clients", "", res)
	})
	t.Run("a malformed page is a 400", func(t *testing.T) {
		t.Parallel()
		c := newClientsHarness(t)
		res := c.send(http.MethodGet, "/api/v1/clients?page=abc", "", nil)
		if res.StatusCode != http.StatusBadRequest || len(c.svc.calls) != 0 {
			t.Fatalf("status = %d, calls = %v", res.StatusCode, c.svc.calls)
		}
		c.checkSpec(http.MethodGet, "/api/v1/clients?page=abc", "", res)
	})
}

func TestClients_Create(t *testing.T) {
	t.Parallel()
	t.Run("creates and returns 201", func(t *testing.T) {
		t.Parallel()
		c := newClientsHarness(t)
		c.svc.client = domain.Client{ID: testClient1, Name: "A.B."}
		body := `{"name":"A.B."}`
		res := c.send(http.MethodPost, "/api/v1/clients", body, keyed())
		if res.StatusCode != http.StatusCreated {
			t.Fatalf("status = %d, body %s", res.StatusCode, res.Body)
		}
		if c.svc.gotName != "A.B." {
			t.Errorf("service got name %q", c.svc.gotName)
		}
		c.checkSpec(http.MethodPost, "/api/v1/clients", body, res)
	})
	t.Run("a name that is taken is a 409 naming the existing client", func(t *testing.T) {
		t.Parallel()
		c := newClientsHarness(t)
		c.svc.err = &domain.ClientNameTakenError{Existing: domain.Client{ID: testClient1, Name: "A.B."}}
		body := `{"name":"a.b."}`
		res := c.send(http.MethodPost, "/api/v1/clients", body, keyed())
		if res.StatusCode != http.StatusConflict {
			t.Fatalf("status = %d, body %s", res.StatusCode, res.Body)
		}
		p := problemOf(t, res)
		existing, _ := p["existing_client"].(map[string]any)
		if p["type"] != string(ProblemClientNameTaken) || existing["id"] != testClient1 || existing["name"] != "A.B." {
			t.Errorf("problem = %+v", p)
		}
		if _, leaked := p["entry_count"]; leaked {
			t.Error("name-taken problem carries entry_count")
		}
		c.checkSpec(http.MethodPost, "/api/v1/clients", body, res)
	})
	t.Run("a validation error is a 400 naming the field", func(t *testing.T) {
		t.Parallel()
		c := newClientsHarness(t)
		c.svc.err = domain.NewValidationError(domain.FieldError{Field: "name", Message: "is required"})
		body := `{"name":"   "}`
		res := c.send(http.MethodPost, "/api/v1/clients", body, keyed())
		p := problemOf(t, res)
		if res.StatusCode != http.StatusBadRequest || p["type"] != string(ProblemValidation) {
			t.Fatalf("status = %d, problem = %+v", res.StatusCode, p)
		}
		c.checkSpec(http.MethodPost, "/api/v1/clients", body, res)
	})
}

func TestClients_CreateRejectsBadRequests(t *testing.T) {
	t.Parallel()
	t.Run("the Idempotency-Key header is required", func(t *testing.T) {
		t.Parallel()
		c := newClientsHarness(t)
		res := c.send(http.MethodPost, "/api/v1/clients", `{"name":"A.B."}`, nil)
		if res.StatusCode != http.StatusBadRequest || len(c.svc.calls) != 0 {
			t.Fatalf("status = %d, calls = %v", res.StatusCode, c.svc.calls)
		}
	})
	bad := []struct{ name, body string }{
		{"unknown field", `{"name":"A.B.","user_id":"` + testUserID + `"}`},
		{"hours-style extra field", `{"name":"A.B.","hours":1.25}`},
		{"missing name", `{}`},
		{"empty name", `{"name":""}`},
		{"name too long", `{"name":"` + strings.Repeat("x", 65) + `"}`},
		{"name is not a string", `{"name":5}`},
		{"body is an array", `[]`},
		{"body is not JSON", `{"name":`},
		{"empty body", ``},
	}
	for _, tt := range bad {
		t.Run("rejects "+tt.name, func(t *testing.T) {
			t.Parallel()
			c := newClientsHarness(t)
			res := c.send(http.MethodPost, "/api/v1/clients", tt.body, keyed())
			if res.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, body %s", res.StatusCode, res.Body)
			}
			if len(c.svc.calls) != 0 {
				t.Errorf("service was called for an invalid body: %v", c.svc.calls)
			}
			if p := problemOf(t, res); p["type"] != string(ProblemValidation) && p["type"] != "urn:therapy-service-hours:problem:validation" {
				t.Errorf("problem type = %v", p["type"])
			}
			c.checkSpec(http.MethodPost, "/api/v1/clients", tt.body, res)
		})
	}
}

func TestClients_CreateProblemDetails(t *testing.T) {
	t.Parallel()
	t.Run("the rejection names the field but never echoes the value", func(t *testing.T) {
		t.Parallel()
		c := newClientsHarness(t)
		res := c.send(http.MethodPost, "/api/v1/clients", `{"name":"A.B.","secret_value_xyz":"hunter2"}`, keyed())
		if res.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d", res.StatusCode)
		}
		if strings.Contains(res.Body, "hunter2") {
			t.Errorf("problem echoes the submitted value: %s", res.Body)
		}
	})
	t.Run("an unknown field is named in the problem", func(t *testing.T) {
		t.Parallel()
		c := newClientsHarness(t)
		res := c.send(http.MethodPost, "/api/v1/clients", `{"name":"A.B.","user_id":"`+testUserID+`"}`, keyed())
		if !strings.Contains(res.Body, `"field":"user_id","message":"is not allowed"`) {
			t.Errorf("body = %s", res.Body)
		}
	})
	t.Run("an oversized body is a 413", func(t *testing.T) {
		t.Parallel()
		c := newClientsHarness(t)
		res := c.send(http.MethodPost, "/api/v1/clients", `{"name":"`+strings.Repeat("x", 5000)+`"}`, keyed())
		if res.StatusCode != http.StatusRequestEntityTooLarge || len(c.svc.calls) != 0 {
			t.Fatalf("status = %d, calls = %v", res.StatusCode, c.svc.calls)
		}
	})
	t.Run("injection payloads reach the service as plain text", func(t *testing.T) {
		t.Parallel()
		for _, payload := range []string{`' OR 1=1 --`, `'; DROP TABLE clients; --`, `A\B`, `100%_`, `"quoted"`} {
			c := newClientsHarness(t)
			c.svc.client = domain.Client{ID: testClient1, Name: payload}
			raw, _ := json.Marshal(map[string]string{"name": payload})
			res := c.send(http.MethodPost, "/api/v1/clients", string(raw), keyed())
			if res.StatusCode != http.StatusCreated || c.svc.gotName != payload {
				t.Errorf("payload %q: status = %d, service got %q", payload, res.StatusCode, c.svc.gotName)
			}
		}
	})
}

func TestClients_Rename(t *testing.T) {
	t.Parallel()
	path := "/api/v1/clients/" + testClient1
	t.Run("renames and returns the client", func(t *testing.T) {
		t.Parallel()
		c := newClientsHarness(t)
		c.svc.client = domain.Client{ID: testClient1, Name: "New Name"}
		body := `{"name":"New Name"}`
		res := c.send(http.MethodPatch, path, body, nil)
		if res.StatusCode != http.StatusOK || c.svc.gotID != testClient1 || c.svc.gotName != "New Name" {
			t.Fatalf("status = %d, id = %q, name = %q, body %s", res.StatusCode, c.svc.gotID, c.svc.gotName, res.Body)
		}
		c.checkSpec(http.MethodPatch, path, body, res)
	})
	t.Run("another user's or a missing client is a 404", func(t *testing.T) {
		t.Parallel()
		c := newClientsHarness(t)
		c.svc.err = domain.ErrNotFound
		body := `{"name":"New Name"}`
		res := c.send(http.MethodPatch, path, body, nil)
		if res.StatusCode != http.StatusNotFound {
			t.Fatalf("status = %d", res.StatusCode)
		}
		c.checkSpec(http.MethodPatch, path, body, res)
	})
	t.Run("a name that is taken is a 409", func(t *testing.T) {
		t.Parallel()
		c := newClientsHarness(t)
		c.svc.err = &domain.ClientNameTakenError{Existing: domain.Client{ID: testClient2, Name: "Group C"}}
		body := `{"name":"group c"}`
		res := c.send(http.MethodPatch, path, body, nil)
		if res.StatusCode != http.StatusConflict || problemOf(t, res)["type"] != string(ProblemClientNameTaken) {
			t.Fatalf("status = %d, body %s", res.StatusCode, res.Body)
		}
		c.checkSpec(http.MethodPatch, path, body, res)
	})
	t.Run("a client id that is not a UUID is a 400", func(t *testing.T) {
		t.Parallel()
		c := newClientsHarness(t)
		res := c.send(http.MethodPatch, "/api/v1/clients/not-a-uuid", `{"name":"X"}`, nil)
		if res.StatusCode != http.StatusBadRequest || len(c.svc.calls) != 0 {
			t.Fatalf("status = %d, calls = %v", res.StatusCode, c.svc.calls)
		}
	})
	t.Run("unknown fields are rejected", func(t *testing.T) {
		t.Parallel()
		c := newClientsHarness(t)
		res := c.send(http.MethodPatch, path, `{"name":"X","user_id":"`+testUserID+`"}`, nil)
		if res.StatusCode != http.StatusBadRequest || len(c.svc.calls) != 0 {
			t.Fatalf("status = %d, calls = %v", res.StatusCode, c.svc.calls)
		}
	})
}

func TestClients_Delete(t *testing.T) {
	t.Parallel()
	path := "/api/v1/clients/" + testClient1
	t.Run("deletes with 204 and no body", func(t *testing.T) {
		t.Parallel()
		c := newClientsHarness(t)
		res := c.send(http.MethodDelete, path, "", nil)
		if res.StatusCode != http.StatusNoContent || res.Body != "" || c.svc.gotDetach {
			t.Fatalf("status = %d, body %q, detach = %v", res.StatusCode, res.Body, c.svc.gotDetach)
		}
		c.checkSpec(http.MethodDelete, path, "", res)
	})
	t.Run("confirm_detach=true is passed on", func(t *testing.T) {
		t.Parallel()
		c := newClientsHarness(t)
		res := c.send(http.MethodDelete, path+"?confirm_detach=true", "", nil)
		if res.StatusCode != http.StatusNoContent || !c.svc.gotDetach || c.svc.gotID != testClient1 {
			t.Fatalf("status = %d, detach = %v, id = %q", res.StatusCode, c.svc.gotDetach, c.svc.gotID)
		}
	})
	t.Run("a client that entries use is a 409 with the count", func(t *testing.T) {
		t.Parallel()
		c := newClientsHarness(t)
		c.svc.err = &domain.ClientInUseError{EntryCount: 3}
		res := c.send(http.MethodDelete, path, "", nil)
		p := problemOf(t, res)
		if res.StatusCode != http.StatusConflict || p["type"] != string(ProblemClientInUse) || p["entry_count"] != float64(3) {
			t.Fatalf("status = %d, problem = %+v", res.StatusCode, p)
		}
		if _, leaked := p["existing_client"]; leaked {
			t.Error("client-in-use problem carries existing_client")
		}
		c.checkSpec(http.MethodDelete, path, "", res)
	})
	t.Run("another user's or a missing client is a 404", func(t *testing.T) {
		t.Parallel()
		c := newClientsHarness(t)
		c.svc.err = domain.ErrNotFound
		res := c.send(http.MethodDelete, path, "", nil)
		if res.StatusCode != http.StatusNotFound {
			t.Fatalf("status = %d", res.StatusCode)
		}
		c.checkSpec(http.MethodDelete, path, "", res)
	})
	t.Run("confirm_detach must be a boolean", func(t *testing.T) {
		t.Parallel()
		c := newClientsHarness(t)
		res := c.send(http.MethodDelete, path+"?confirm_detach=maybe", "", nil)
		if res.StatusCode != http.StatusBadRequest || len(c.svc.calls) != 0 {
			t.Fatalf("status = %d, calls = %v", res.StatusCode, c.svc.calls)
		}
	})
}

func TestClients_AccessControl(t *testing.T) {
	t.Parallel()
	requests := []struct{ name, method, path, body string }{
		{"list", http.MethodGet, "/api/v1/clients", ""},
		{"create", http.MethodPost, "/api/v1/clients", `{"name":"A.B."}`},
		{"rename", http.MethodPatch, "/api/v1/clients/" + testClient1, `{"name":"A.B."}`},
		{"delete", http.MethodDelete, "/api/v1/clients/" + testClient1, ""},
	}
	for _, rq := range requests {
		t.Run(rq.name+" without gateway claims is a 401", func(t *testing.T) {
			t.Parallel()
			c := newClientsHarness(t)
			res := c.sendAs(rq.method, rq.path, rq.body, keyed(), nil)
			if res.StatusCode != http.StatusUnauthorized || len(c.svc.calls) != 0 {
				t.Fatalf("status = %d, calls = %v", res.StatusCode, c.svc.calls)
			}
		})
		t.Run(rq.name+" by a forbidden actor is a 403", func(t *testing.T) {
			t.Parallel()
			c := newClientsHarness(t)
			c.svc.err = domain.ErrForbidden
			res := c.send(rq.method, rq.path, rq.body, keyed())
			if res.StatusCode != http.StatusForbidden {
				t.Fatalf("status = %d, body %s", res.StatusCode, res.Body)
			}
		})
		t.Run(rq.name+" hides an unexpected failure", func(t *testing.T) {
			t.Parallel()
			c := newClientsHarness(t)
			c.svc.err = errors.New("pq: relation clients is broken at 10.0.0.5")
			res := c.send(rq.method, rq.path, rq.body, keyed())
			if res.StatusCode != http.StatusInternalServerError || strings.Contains(res.Body, "10.0.0.5") {
				t.Fatalf("status = %d, body %s", res.StatusCode, res.Body)
			}
		})
	}
}

func TestProblem_ExtensionsNeverReplaceStandardMembers(t *testing.T) {
	t.Parallel()
	raw, err := json.Marshal(Problem{Type: ProblemClientInUse, Title: "t", Status: 409, Extensions: map[string]any{"status": 200, "entry_count": 2}})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["status"] != float64(409) || got["entry_count"] != float64(2) {
		t.Errorf("problem = %s", raw)
	}
}
