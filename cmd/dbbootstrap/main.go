// Command dbbootstrap is the CloudFormation custom resource that creates the app_rw and migrator
// roles (ADR-0001). It runs in the VPC as the RDS master user, once per deploy of the data stack.
// No runtime Lambda reads the master secret: CloudFormation resolves it into this resource's
// properties at deploy time.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/aws/aws-lambda-go/cfn"
	"github.com/aws/aws-lambda-go/lambda"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/db"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/dbroles"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With(slog.String("function", "dbbootstrap"))
	h := &handler{logger: logger, ensureRoles: ensureRoles}
	lambda.Start(cfn.LambdaWrap(h.handle))
}

func ensureRoles(ctx context.Context, s db.Settings) error {
	pool, err := db.Open(ctx, s, nil)
	if err != nil {
		return err //nolint:wrapcheck // *db.ConnectError already names the endpoint, never the password.
	}
	defer pool.Close()
	return dbroles.Ensure(ctx, pool) //nolint:wrapcheck // Ensure wraps its own errors.
}
