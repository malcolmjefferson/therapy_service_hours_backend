package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/config"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/db"
)

// Health and unauthenticated requests never open the database. Warm requests reuse
// one pool with db.MaxConns=2; a failed first connection can be retried later.
type lazyDatabase struct {
	cfg  config.Database
	mu   sync.Mutex
	pool *pgxpool.Pool
}

func (d *lazyDatabase) Begin(ctx context.Context) (pgx.Tx, error) {
	pool, err := d.connection(ctx)
	if err != nil {
		return nil, err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin API transaction: %w", err)
	}
	return tx, nil
}

func (d *lazyDatabase) connection(ctx context.Context) (*pgxpool.Pool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.pool != nil {
		return d.pool, nil
	}
	s := db.Settings{Host: d.cfg.Host, Port: d.cfg.Port, Database: d.cfg.Name, User: d.cfg.User, Region: d.cfg.Region, Mode: db.AuthMode(d.cfg.AuthMode), Password: d.cfg.Password}
	var tokens *db.TokenSource
	if s.Mode == db.AuthIAM {
		cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(s.Region))
		if err != nil {
			return nil, fmt.Errorf("load API AWS config: %w", err)
		}
		tokens, err = db.NewTokenSource(db.AWSSigner{Credentials: cfg.Credentials}, time.Now, s)
		if err != nil {
			return nil, err
		}
	}
	pool, err := db.Open(ctx, s, tokens)
	if err != nil {
		return nil, err
	}
	d.pool = pool
	return pool, nil
}

func (d *lazyDatabase) close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.pool != nil {
		d.pool.Close()
	}
}
