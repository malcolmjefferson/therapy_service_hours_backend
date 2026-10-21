package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

type fakePurger struct {
	deleted int64
	err     error
	batch   int32
}

func (f *fakePurger) PurgeExpired(_ context.Context, batch int32) (int64, error) {
	f.batch = batch
	return f.deleted, f.err
}

func TestPurge_ReportsTheCountAndLogsNothingElse(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	store := &fakePurger{deleted: 7}
	res, err := purge(t.Context(), slog.New(slog.NewJSONHandler(&logs, nil)), store)
	if err != nil || res.Deleted != 7 {
		t.Fatalf("purge = %+v, %v", res, err)
	}
	if store.batch != batchSize {
		t.Errorf("batch size = %d, want %d", store.batch, batchSize)
	}
	if !strings.Contains(logs.String(), `"deleted":7`) {
		t.Errorf("log = %s", logs.String())
	}
}

func TestPurge_AFailureIsReturnedWithoutTheDriverText(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	store := &fakePurger{deleted: 3, err: errors.New("connection reset by peer at 10.0.0.1")}
	res, err := purge(t.Context(), slog.New(slog.NewJSONHandler(&logs, nil)), store)
	if err == nil || res.Deleted != 3 {
		t.Fatalf("purge = %+v, %v; want the failure and the partial count", res, err)
	}
	if strings.Contains(logs.String(), "10.0.0.1") || !strings.Contains(logs.String(), "IDEMPOTENCY_PURGE_FAILED") {
		t.Errorf("log = %s", logs.String())
	}
}
