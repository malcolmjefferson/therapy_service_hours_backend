package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/apigw"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/authn"
)

type identityStoreFunc func(context.Context, domain.Identity, time.Time) (domain.Actor, error)

func (f identityStoreFunc) Resolve(ctx context.Context, i domain.Identity, at time.Time) (domain.Actor, error) {
	return f(ctx, i, at)
}

func meEvent(claims map[string]string) events.APIGatewayV2HTTPRequest {
	e := events.APIGatewayV2HTTPRequest{RawPath: BasePath + "/me", Headers: map[string]string{"authorization": "Bearer secret-token-material"}}
	e.RequestContext.HTTP.Method = http.MethodGet
	if claims != nil {
		e.RequestContext.Authorizer = &events.APIGatewayV2HTTPRequestContextAuthorizerDescription{JWT: &events.APIGatewayV2HTTPRequestContextAuthorizerJWTDescription{Claims: claims}}
	}
	return e
}

func TestMeReturnsOnlyCurrentDatabaseProfile(t *testing.T) {
	sink := &logSink{}
	deps := testDeps(sink)
	actor := domain.Actor{ID: "00000000-0000-4000-8000-000000000002", OrganizationID: "00000000-0000-4000-8000-000000000001", OrganizationName: "Default Organization", Email: "pseudonym@example.test", FirstName: "Pseudonym", LastName: "A", Active: true}
	deps.ActorResolver, _ = authn.New(identityStoreFunc(func(_ context.Context, i domain.Identity, _ time.Time) (domain.Actor, error) {
		if i.Subject != "trusted-sub" {
			t.Fatal("wrong identity")
		}
		return actor, nil
	}), time.Now)
	h, err := NewRouter(deps)
	if err != nil {
		t.Fatal(err)
	}
	claims := map[string]string{"sub": "trusted-sub", "email": actor.Email, "email_verified": "true", "token_use": "id", "cognito:groups": "[admin,supervisor]"}
	response, err := apigw.Handler(h, ContextWithRequestID)(t.Context(), meEvent(claims))
	if err != nil || response.StatusCode != 200 {
		t.Fatalf("response=%+v error=%v", response, err)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(response.Body), &body); err != nil {
		t.Fatal(err)
	}
	if len(body) != 5 || body["name"] != "Pseudonym A" || body["id"] != actor.ID || len(body["roles"].([]any)) != 0 {
		t.Fatalf("wrong DTO: %+v", body)
	}
	org := body["organization"].(map[string]any)
	if len(org) != 2 || org["id"] != actor.OrganizationID {
		t.Fatal("wrong organization DTO")
	}
	if response.Headers["Cache-Control"] != "no-store" {
		t.Fatal("profile is cacheable")
	}
	schema := loadSpec(t).Components.Schemas["MeResponse"].Value
	if err := schema.VisitJSON(body); err != nil {
		t.Fatal(err)
	}
	assertNoIdentityLeak(t, sink.buf.String(), actor.Email, "trusted-sub", "secret-token-material")
}

func assertNoIdentityLeak(t *testing.T, output string, values ...string) {
	t.Helper()
	for _, value := range values {
		if strings.Contains(output, value) {
			t.Fatal("identity material leaked into logs")
		}
	}
}

func TestMeFailsClosedAndRedactsUpstreamErrors(t *testing.T) {
	for _, tt := range []struct {
		name    string
		claims  map[string]string
		failure error
		status  int
		called  bool
	}{
		{"forged bearer only", nil, nil, 401, false},
		{"access token", map[string]string{"sub": "subject", "token_use": "access", "email_verified": "true", "email": "private@example.test"}, nil, 401, false},
		{"uninvited or deactivated", map[string]string{"sub": "subject", "token_use": "id", "email_verified": "true", "email": "private@example.test"}, domain.ErrForbidden, 403, true},
		{"database unavailable", map[string]string{"sub": "subject", "token_use": "id", "email_verified": "true", "email": "private@example.test"}, errors.New("SQL private@example.test secret-password secret-token-material"), 500, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sink := &logSink{}
			deps := testDeps(sink)
			called := false
			deps.ActorResolver, _ = authn.New(identityStoreFunc(func(context.Context, domain.Identity, time.Time) (domain.Actor, error) {
				called = true
				return domain.Actor{}, tt.failure
			}), time.Now)
			h, _ := NewRouter(deps)
			response, err := apigw.Handler(h, ContextWithRequestID)(t.Context(), meEvent(tt.claims))
			if err != nil || response.StatusCode != tt.status || called != tt.called {
				t.Fatalf("response=%+v called=%v err=%v", response, called, err)
			}
			problem := decodeProblem(t, []byte(response.Body))
			if problem.RequestID != "generated-id" || response.Headers["Content-Type"] != "application/problem+json" {
				t.Fatal("missing safe problem response")
			}
			for _, value := range []string{"private@example.test", "secret-password", "secret-token-material", "SQL"} {
				if strings.Contains(sink.buf.String()+response.Body, value) {
					t.Fatal("sensitive data leaked")
				}
			}
		})
	}
}

func TestHealthNeverResolvesIdentity(t *testing.T) {
	deps := testDeps(&logSink{})
	deps.ActorResolver = resolverFunc(func(context.Context, map[string]string) (domain.Actor, error) {
		t.Fatal("health accessed identity store")
		return domain.Actor{}, nil
	})
	h, _ := NewRouter(deps)
	event := meEvent(nil)
	event.RawPath = BasePath + "/healthz"
	response, err := apigw.Handler(h, ContextWithRequestID)(t.Context(), event)
	if err != nil || response.StatusCode != 200 {
		t.Fatalf("health = %+v %v", response, err)
	}
}
