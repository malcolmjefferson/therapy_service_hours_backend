// Command migrate is the migration Lambda (in the VPC, ADR-0003). The pipeline invokes it
// synchronously before each app deploy; it connects as the migrator role with an IAM token and
// applies the embedded migrations under an advisory lock, then returns a JSON Result.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/malcolmjefferson/therapy_service_hours_backend/db/migrations"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/config"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/db"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/migrate"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With(slog.String("function", functionName()))
	dbCfg, err := config.LoadDatabase(os.LookupEnv)
	if err != nil {
		logger.Error("migrate failed to start", slog.String("error", err.Error()))
		os.Exit(1)
	}
	h := &handler{cfg: dbCfg, logger: logger}
	lambda.Start(h.handle)
}

func functionName() string {
	if v := os.Getenv("AWS_LAMBDA_FUNCTION_NAME"); v != "" {
		return v
	}
	return "migrate"
}

// handler opens the connection on the first invocation and reuses it while the instance is warm
// (GO-39). A connection failure is reported in the payload, not as a cold-start crash, so the
// pipeline shows why the release stopped.
type handler struct {
	cfg    config.Database
	logger *slog.Logger

	mu   sync.Mutex
	conn *sql.DB
}

func (h *handler) handle(ctx context.Context) (migrate.Result, error) {
	start := time.Now()
	conn, err := h.connection(ctx)
	if err != nil {
		h.logger.Error("migrate: database unavailable", slog.String("error", err.Error()))
		return migrate.Result{Status: migrate.StatusError, Applied: []int64{}, Error: err.Error()}, nil
	}
	res := migrate.Run(ctx, conn, migrations.FS, migrate.Options{})
	h.logger.Info("migrate finished",
		slog.String("status", res.Status),
		slog.Any("applied", res.Applied),
		slog.Int64("version", res.Version),
		slog.String("error", res.Error),
		slog.Int64("duration_ms", time.Since(start).Milliseconds()),
	)
	return res, nil
}

func (h *handler) connection(ctx context.Context) (*sql.DB, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.conn != nil {
		return h.conn, nil
	}
	settings := db.Settings{
		Host: h.cfg.Host, Port: h.cfg.Port, Database: h.cfg.Name, User: h.cfg.User,
		Mode: db.AuthMode(h.cfg.AuthMode), Password: h.cfg.Password, Region: h.cfg.Region,
	}
	var tokens *db.TokenSource
	if settings.Mode == db.AuthIAM {
		awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(settings.Region))
		if err != nil {
			return nil, fmt.Errorf("load AWS config: %w", err)
		}
		tokens, err = db.NewTokenSource(db.AWSSigner{Credentials: awsCfg.Credentials}, time.Now, settings)
		if err != nil {
			return nil, fmt.Errorf("token source: %w", err)
		}
	}
	pool, err := openWithRetry(ctx, h.logger, connectAttempts, connectBackoff, func(ctx context.Context) (*pgxpool.Pool, error) {
		return db.Open(ctx, settings, tokens)
	})
	if err != nil {
		return nil, err
	}
	h.conn = stdlib.OpenDBFromPool(pool)
	return h.conn, nil
}

// The first IAM-authenticated connection to a fresh instance can outlast the connect timeout
// while RDS's auth proxy warms up; a retry then succeeds quickly. The Lambda runs for up to 15
// minutes, so a few attempts cost little and spare a failed release.
const (
	connectAttempts = 3
	connectBackoff  = 2 * time.Second
)

// openWithRetry calls open up to attempts times, waiting backoff, then twice that, between
// failures. It returns the last error, and stops early when ctx ends.
func openWithRetry(ctx context.Context, logger *slog.Logger, attempts int, backoff time.Duration, open func(context.Context) (*pgxpool.Pool, error)) (*pgxpool.Pool, error) {
	var err error
	for i := 1; i <= attempts; i++ {
		var pool *pgxpool.Pool
		if pool, err = open(ctx); err == nil {
			return pool, nil
		}
		if i == attempts {
			break
		}
		logger.Warn("migrate: connect failed, retrying", slog.Int("attempt", i), slog.String("error", err.Error()))
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("connect: %w (gave up: %w)", err, ctx.Err())
		case <-time.After(backoff):
		}
		backoff *= 2
	}
	return nil, err
}
