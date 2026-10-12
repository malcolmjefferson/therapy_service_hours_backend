package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/apigw"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
)

// ActorResolver resolves only claims received from the Gateway adapter.
type ActorResolver interface {
	Resolve(context.Context, map[string]string) (domain.Actor, error)
}

type actorKey struct{}

// ActorFrom returns the current database-backed actor or the deny-by-default zero actor.
func ActorFrom(ctx context.Context) domain.Actor {
	actor, _ := ctx.Value(actorKey{}).(domain.Actor)
	return actor
}

func resolveActor(resolver ActorResolver) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
			defer cancel()
			claims, _ := apigw.JWTClaimsFrom(ctx)
			actor, err := resolver.Resolve(ctx, claims)
			if err != nil {
				WriteError(w, r, err)
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, actorKey{}, actor)))
		})
	}
}
