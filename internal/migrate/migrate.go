// Package migrate applies the embedded goose migrations under a PostgreSQL advisory lock (ADR-0003).
package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

// Status values in a Result.
const (
	StatusOK    = "ok"
	StatusError = "error"
)

// lockTimeoutPeriodSeconds and lockTimeoutAttempts bound the wait for another run's lock: 10 s x 80 =
// about 13 minutes, inside the 15-minute Lambda timeout.
const (
	lockTimeoutPeriodSeconds = 10
	lockTimeoutAttempts      = 80
)

// Result is the Lambda's JSON payload. The pipeline's CodeBuild step fails the release unless
// Status is "ok" (ADR-0003).
type Result struct {
	Status string `json:"status"`
	// Applied lists the versions applied by this run; empty when there was nothing to do.
	Applied []int64 `json:"applied"`
	// Version is the database's schema version after the run.
	Version int64 `json:"version"`
	// Error describes the failure. Migration SQL is static, so it carries no user data.
	Error string `json:"error,omitempty"`
}

// Options tune a run. The zero value is the production setting.
type Options struct {
	// LockID overrides the advisory lock key. Tests use it to isolate runs.
	LockID int64
	// LockRetrySeconds overrides how often a waiting run retries the lock. Tests shorten it.
	LockRetrySeconds uint64
}

// Run applies every pending migration in fsys. Concurrent runs serialize on a PostgreSQL advisory
// lock, so a second release waits and then finds nothing to do. Run never panics on a migration
// failure; it reports it in the Result.
func Run(ctx context.Context, db *sql.DB, fsys fs.FS, opts Options) Result {
	provider, err := newProvider(db, fsys, opts)
	if err != nil {
		return failure(fmt.Errorf("prepare migrations: %w", err))
	}
	results, upErr := provider.Up(ctx)
	var partial *goose.PartialError
	if errors.As(upErr, &partial) {
		// Migrations before the failing one stay applied; report them.
		results = partial.Applied
	}
	applied := make([]int64, 0, len(results))
	for _, r := range results {
		if r.Error == nil {
			applied = append(applied, r.Source.Version)
		}
	}
	version, verErr := provider.GetDBVersion(ctx)
	if upErr != nil {
		res := failure(fmt.Errorf("apply migrations: %w", upErr))
		res.Applied, res.Version = applied, version
		return res
	}
	if verErr != nil {
		return failure(fmt.Errorf("read schema version: %w", verErr))
	}
	return Result{Status: StatusOK, Applied: applied, Version: version}
}

func newProvider(db *sql.DB, fsys fs.FS, opts Options) (*goose.Provider, error) {
	period, attempts := uint64(lockTimeoutPeriodSeconds), uint64(lockTimeoutAttempts)
	if opts.LockRetrySeconds > 0 {
		period, attempts = opts.LockRetrySeconds, lockTimeoutPeriodSeconds*lockTimeoutAttempts/opts.LockRetrySeconds
	}
	lockOpts := []lock.SessionLockerOption{lock.WithLockTimeout(period, attempts)}
	if opts.LockID != 0 {
		lockOpts = append(lockOpts, lock.WithLockID(opts.LockID))
	}
	locker, err := lock.NewPostgresSessionLocker(lockOpts...)
	if err != nil {
		return nil, fmt.Errorf("session locker: %w", err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, fsys, goose.WithSessionLocker(locker))
	if err != nil {
		return nil, fmt.Errorf("goose provider: %w", err)
	}
	return provider, nil
}

func failure(err error) Result {
	return Result{Status: StatusError, Applied: []int64{}, Error: err.Error()}
}
