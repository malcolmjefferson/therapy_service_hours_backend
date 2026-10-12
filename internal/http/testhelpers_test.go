package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
)

// logSink collects JSON log lines written during a test.
type logSink struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *logSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

// lines parses every log line, failing the test if any line is not a JSON object.
func (s *logSink) lines(t *testing.T) []map[string]any {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(s.buf.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line is not JSON: %q (%v)", line, err)
		}
		out = append(out, m)
	}
	return out
}

// testDeps returns router dependencies with a fixed clock and request ID, logging into sink.
func testDeps(sink *logSink) RouterDeps {
	start := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)
	return RouterDeps{
		Logger:       slog.New(slog.NewJSONHandler(sink, &slog.HandlerOptions{Level: slog.LevelDebug})).With(slog.String("function", "api")),
		Version:      "test-version",
		MaxBodyBytes: 16,
		NewRequestID: func() string { return "generated-id" },
		Now:          func() time.Time { return start },
		ActorResolver: resolverFunc(func(context.Context, map[string]string) (domain.Actor, error) {
			return domain.Actor{}, domain.ErrForbidden
		}),
	}
}

type resolverFunc func(context.Context, map[string]string) (domain.Actor, error)

func (f resolverFunc) Resolve(ctx context.Context, claims map[string]string) (domain.Actor, error) {
	return f(ctx, claims)
}

// decodeProblem parses a problem+json body.
func decodeProblem(t *testing.T, body []byte) Problem {
	t.Helper()
	var p Problem
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("body is not a problem: %q (%v)", body, err)
	}
	return p
}
