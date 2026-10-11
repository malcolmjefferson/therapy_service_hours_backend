package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestOpenWithRetry_SucceedsAfterTransientFailures(t *testing.T) {
	t.Parallel()
	calls := 0
	want := &pgxpool.Pool{}
	got, err := openWithRetry(t.Context(), quiet, 3, time.Millisecond, func(context.Context) (*pgxpool.Pool, error) {
		calls++
		if calls < 3 {
			return nil, errors.New("timeout: context deadline exceeded")
		}
		return want, nil
	})
	if err != nil || got != want || calls != 3 {
		t.Fatalf("got (%v, %v) after %d calls; want the pool on the third call", got, err, calls)
	}
}

func TestOpenWithRetry_ReturnsTheLastErrorAfterAllAttempts(t *testing.T) {
	t.Parallel()
	calls := 0
	_, err := openWithRetry(t.Context(), quiet, 3, time.Millisecond, func(context.Context) (*pgxpool.Pool, error) {
		calls++
		return nil, errors.New("refused")
	})
	if err == nil || err.Error() != "refused" || calls != 3 {
		t.Fatalf("err = %v after %d calls; want the last error after 3", err, calls)
	}
}

func TestOpenWithRetry_StopsWhenTheContextEnds(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	calls := 0
	_, err := openWithRetry(ctx, quiet, 5, time.Hour, func(context.Context) (*pgxpool.Pool, error) {
		calls++
		cancel()
		return nil, errors.New("timeout")
	})
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("err = %v after %d calls; want context.Canceled after 1", err, calls)
	}
}
