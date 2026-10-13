package entries_test

import (
	"context"
	"io"
	"log/slog"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewJSONHandler(io.Discard, nil)) }

type actorFunc func(context.Context, map[string]string) (domain.Actor, error)

func (f actorFunc) Resolve(ctx context.Context, claims map[string]string) (domain.Actor, error) {
	return f(ctx, claims)
}
