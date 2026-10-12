package httpapi

import (
	"net/http"
	"strings"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/apigw"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/authn"
)

// RequireAuthorizerClaims refuses protected requests without validated Gateway identity.
// Mount it on protected routes before actor resolution (S1-06); /healthz stays public.
// This guard establishes provenance only. Actor resolution must still validate token
// type and identity attributes and re-check the user's status and roles in the database.
func RequireAuthorizerClaims(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, ok := apigw.JWTClaimsFrom(r.Context())
		if !ok || strings.TrimSpace(claims["sub"]) == "" {
			WriteError(w, r, authn.ErrUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
