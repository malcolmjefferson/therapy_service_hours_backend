// Command dbbootstrap is the role-bootstrap worker (in the VPC, ADR-0001 as amended 2026-10-10).
// The deploy-time provider Lambda invokes it directly with the master connection in the payload;
// it creates the app_rw and migrator roles over verify-full TLS. It never reads Secrets Manager
// and never logs the payload.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/aws/aws-lambda-go/lambda"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/db"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/dbroles"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With(slog.String("function", "dbbootstrap"))
	h := &handler{logger: logger, ensureRoles: ensureRoles}
	lambda.Start(h.handle)
}

func ensureRoles(ctx context.Context, s db.Settings) error {
	pool, err := db.Open(ctx, s, nil)
	if err != nil {
		return err //nolint:wrapcheck // *db.ConnectError already names the endpoint, never the password.
	}
	defer pool.Close()
	return dbroles.Ensure(ctx, pool) //nolint:wrapcheck // Ensure wraps its own errors.
}
