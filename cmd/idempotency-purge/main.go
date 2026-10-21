// Command idempotency-purge is the scheduled cleanup of expired Idempotency-Key records (S2-09).
// A schedule invokes it; the event is ignored. It connects as app_rw over IAM, deletes expired rows
// in small batches and logs only the count.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/config"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/db"
)

// batchSize keeps each DELETE short; the run repeats until a batch comes back short.
const batchSize = 500

// runTimeout leaves room inside the Lambda's own timeout for the connection and the final log line.
const runTimeout = 50 * time.Second

// Result is what one run reports.
type Result struct {
	Deleted int64 `json:"deleted"`
}

// purger deletes expired records. *db.IdempotencyStore satisfies it.
type purger interface {
	PurgeExpired(ctx context.Context, batchSize int32) (int64, error)
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With(slog.String("function", "idempotency-purge"))
	if err := run(logger); err != nil {
		logger.Error("idempotency purge failed to start", slog.String("code", "IDEMPOTENCY_PURGE_INITIALIZATION_FAILED"))
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.LoadDatabase(os.LookupEnv)
	if err != nil {
		return err //nolint:wrapcheck // config.LoadDatabase already says what is wrong.
	}
	if cfg.AuthMode != "iam" || cfg.User != "app_rw" {
		return errors.New("idempotency purge requires app_rw IAM authentication")
	}
	database := &lazyPool{cfg: cfg}
	defer database.close()
	lambda.Start(func(ctx context.Context) (Result, error) {
		pool, err := database.connection(ctx)
		if err != nil {
			logger.ErrorContext(ctx, "idempotency purge could not connect", slog.String("code", "IDEMPOTENCY_PURGE_DB_UNAVAILABLE"))
			return Result{}, err
		}
		return purge(ctx, logger, db.NewIdempotencyStore(pool))
	})
	return nil
}

// purge deletes expired records and logs how many it removed. A failure is returned so the
// scheduler records it and the next run retries.
func purge(ctx context.Context, logger *slog.Logger, store purger) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()
	deleted, err := store.PurgeExpired(ctx, batchSize)
	if err != nil {
		logger.ErrorContext(ctx, "idempotency purge failed", slog.String("code", "IDEMPOTENCY_PURGE_FAILED"), slog.Int64("deleted_before_failure", deleted))
		return Result{Deleted: deleted}, fmt.Errorf("purge expired idempotency keys: %w", err)
	}
	logger.InfoContext(ctx, "idempotency keys purged", slog.Int64("deleted", deleted))
	return Result{Deleted: deleted}, nil
}

// lazyPool opens the pool on the first invocation and reuses it while the container is warm.
type lazyPool struct {
	cfg  config.Database
	mu   sync.Mutex
	pool *pgxpool.Pool
}

// pgxpool.Pool satisfies db.Beginner, so the store takes the pool directly.
func (p *lazyPool) connection(ctx context.Context) (*pgxpool.Pool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pool != nil {
		return p.pool, nil
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(p.cfg.Region))
	if err != nil {
		return nil, fmt.Errorf("load purge AWS config: %w", err)
	}
	settings := db.Settings{Host: p.cfg.Host, Port: p.cfg.Port, Database: p.cfg.Name, User: p.cfg.User, Region: p.cfg.Region, Mode: db.AuthIAM}
	tokens, err := db.NewTokenSource(db.AWSSigner{Credentials: awsCfg.Credentials}, time.Now, settings)
	if err != nil {
		return nil, err //nolint:wrapcheck // NewTokenSource errors are already specific.
	}
	pool, err := db.Open(ctx, settings, tokens)
	if err != nil {
		return nil, err //nolint:wrapcheck // db.Open errors are already specific and carry no credentials.
	}
	p.pool = pool
	return pool, nil
}

func (p *lazyPool) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pool != nil {
		p.pool.Close()
	}
}
