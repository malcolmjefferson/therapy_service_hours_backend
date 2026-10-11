package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-lambda-go/events"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/apigw"
)

func TestRequireAuthorizerClaims_FailsClosedWithoutLoggingTokenMaterial(t *testing.T) {
	t.Parallel()
	for _, sub := range []string{"", " ", "validated-sub"} {
		t.Run("subject="+sub, func(t *testing.T) {
			t.Parallel()
			sink := &logSink{}
			r := newMux(testDeps(sink))
			called := false
			r.With(RequireAuthorizerClaims).Get("/protected", func(w http.ResponseWriter, _ *http.Request) {
				called = true
				w.WriteHeader(http.StatusNoContent)
			})
			e := events.APIGatewayV2HTTPRequest{RawPath: "/protected", Headers: map[string]string{"authorization": "Bearer secret-token-material"}}
			e.RequestContext.HTTP.Method = http.MethodGet
			if sub != "" {
				e.RequestContext.Authorizer = &events.APIGatewayV2HTTPRequestContextAuthorizerDescription{
					JWT: &events.APIGatewayV2HTTPRequestContextAuthorizerJWTDescription{Claims: map[string]string{"sub": sub, "email": "pseudonym@example.test"}},
				}
			}
			res, err := apigw.Handler(r, ContextWithRequestID)(t.Context(), e)
			if err != nil {
				t.Fatal(err)
			}
			if sub == "validated-sub" {
				if res.StatusCode != http.StatusNoContent || !called {
					t.Fatalf("validated subject denied: %+v", res)
				}
			} else {
				if res.StatusCode != http.StatusUnauthorized || called {
					t.Fatalf("untrusted request reached handler: %+v", res)
				}
				p := decodeProblem(t, []byte(res.Body))
				if p.Type != ProblemUnauthorized || p.RequestID != "generated-id" || res.Headers["Www-Authenticate"] != "Bearer" {
					t.Errorf("unauthorized response = %+v", res)
				}
			}
			for _, secret := range []string{"secret-token-material", "pseudonym@example.test", "validated-sub"} {
				if strings.Contains(sink.buf.String(), secret) {
					t.Errorf("sensitive identity material appeared in logs")
				}
			}
		})
	}
}
