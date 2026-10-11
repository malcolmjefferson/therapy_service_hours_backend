package apigw_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/aws/aws-lambda-go/events"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/apigw"
)

func TestHandler_ClaimsComeOnlyFromTheJWTAuthorizer(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		auth *events.APIGatewayV2HTTPRequestContextAuthorizerDescription
		want bool
	}{
		{"JWT authorizer", &events.APIGatewayV2HTTPRequestContextAuthorizerDescription{JWT: &events.APIGatewayV2HTTPRequestContextAuthorizerJWTDescription{Claims: map[string]string{"sub": "validated-sub"}}}, true},
		{"missing authorizer", nil, false},
		{"non-JWT authorizer", &events.APIGatewayV2HTTPRequestContextAuthorizerDescription{Lambda: map[string]interface{}{"sub": "untrusted-sub"}}, false},
		{"empty JWT claims", &events.APIGatewayV2HTTPRequestContextAuthorizerDescription{JWT: &events.APIGatewayV2HTTPRequestContextAuthorizerJWTDescription{}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := event(http.MethodGet, "/protected")
			e.RequestContext.Authorizer = tt.auth
			e.Headers = map[string]string{"authorization": "Bearer forged-token", "x-cognito-sub": "forged-sub"}
			h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				claims, ok := apigw.JWTClaimsFrom(r.Context())
				if ok != tt.want || (ok && claims["sub"] != "validated-sub") {
					t.Errorf("claims = %v, trusted = %v", claims, ok)
				}
				if ok {
					claims["sub"] = "modified-sub"
					if again, _ := apigw.JWTClaimsFrom(r.Context()); again["sub"] != "validated-sub" {
						t.Error("handler changed the request's identity")
					}
				}
				w.WriteHeader(http.StatusNoContent)
			})
			if _, err := apigw.Handler(h, setID)(t.Context(), e); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestHandler_DoesNotCarryIdentityIntoAnotherEvent(t *testing.T) {
	t.Parallel()
	var previous context.Context
	calls := 0
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, ok := apigw.JWTClaimsFrom(r.Context()) //nolint:contextcheck // Deliberately reuse a prior invocation context to prove identity isolation.
		if ok != (calls == 0) {
			t.Errorf("invocation %d trusted = %v", calls, ok)
		}
		previous = r.Context()
		calls++
		w.WriteHeader(http.StatusNoContent)
	})
	handle := apigw.Handler(h, setID)
	e := event(http.MethodGet, "/protected")
	e.RequestContext.Authorizer = &events.APIGatewayV2HTTPRequestContextAuthorizerDescription{
		JWT: &events.APIGatewayV2HTTPRequestContextAuthorizerJWTDescription{Claims: map[string]string{"sub": "first-sub"}},
	}
	if _, err := handle(t.Context(), e); err != nil {
		t.Fatal(err)
	}
	e.RequestContext.Authorizer = nil
	if _, err := handle(previous, e); err != nil {
		t.Fatal(err)
	}
}
