// Command seed is the IAM-invoked VPC identity bootstrap (S1-09).
package main

import (
	"context"
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
	transport "github.com/malcolmjefferson/therapy_service_hours_backend/internal/seed"
	service "github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/seed"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With(slog.String("function", "seed"))
	if err := run(logger); err != nil {
		logger.Error("identity seed initialization failed", slog.String("code", "SEED_UNAVAILABLE"))
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.LoadDatabase(os.LookupEnv)
	if err != nil {
		return err
	}
	if cfg.AuthMode != "iam" || cfg.User != "app_rw" {
		return fmt.Errorf("seed requires app_rw IAM auth")
	}
	store := &lazyStore{cfg: cfg}
	svc, err := service.New(store, os.Getenv("APP_ENV"), time.Now)
	if err != nil {
		return err
	}
	handler, err := transport.New(svc, logger)
	if err != nil {
		return err
	}
	lambda.Start(handler.Handle)
	return nil
}

// Open the pool only after validation, then reuse it; a failed connection may be retried.
type lazyStore struct {
	cfg  config.Database
	mu   sync.Mutex
	pool *pgxpool.Pool
}

func (s *lazyStore) Apply(ctx context.Context, plan service.Plan) (service.Result, error) {
	pool, err := s.connection(ctx)
	if err != nil {
		return service.Result{}, err
	}
	return db.NewSeedStore(pool).Apply(ctx, plan)
}

func (s *lazyStore) connection(ctx context.Context) (*pgxpool.Pool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pool != nil {
		return s.pool, nil
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(s.cfg.Region))
	if err != nil {
		return nil, fmt.Errorf("load seed AWS config: %w", err)
	}
	settings := db.Settings{Host: s.cfg.Host, Port: s.cfg.Port, Database: s.cfg.Name, User: s.cfg.User, Region: s.cfg.Region, Mode: db.AuthIAM}
	tokens, err := db.NewTokenSource(db.AWSSigner{Credentials: awsCfg.Credentials}, time.Now, settings)
	if err != nil {
		return nil, err
	}
	pool, err := db.Open(ctx, settings, tokens)
	if err != nil {
		return nil, err
	}
	s.pool = pool
	return pool, nil
}
