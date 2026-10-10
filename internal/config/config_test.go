package config_test

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/config"
)

func lookupFrom(env map[string]string) config.LookupFunc {
	return func(key string) (string, bool) {
		v, ok := env[key]
		return v, ok
	}
}

func TestLoad_AppliesDefaults(t *testing.T) {
	t.Parallel()
	cfg, err := config.Load(lookupFrom(map[string]string{"APP_VERSION": "abc123"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := config.Config{
		Version:      "abc123",
		Function:     "api",
		LogLevel:     slog.LevelInfo,
		MaxBodyBytes: config.DefaultMaxBodyBytes,
	}
	if cfg != want {
		t.Fatalf("got %+v, want %+v", cfg, want)
	}
}

func TestLoad_ReadsEveryVariable(t *testing.T) {
	t.Parallel()
	cfg, err := config.Load(lookupFrom(map[string]string{
		"APP_VERSION":              " v1.2.3 ",
		"AWS_LAMBDA_FUNCTION_NAME": "therapy-service-hours-dev-api",
		"LOG_LEVEL":                "debug",
		"MAX_BODY_BYTES":           "2048",
		"LOCAL_HTTP_ADDR":          "127.0.0.1:8080",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := config.Config{
		Version:       "v1.2.3",
		Function:      "therapy-service-hours-dev-api",
		LogLevel:      slog.LevelDebug,
		MaxBodyBytes:  2048,
		LocalHTTPAddr: "127.0.0.1:8080",
	}
	if cfg != want {
		t.Fatalf("got %+v, want %+v", cfg, want)
	}
}

func TestLoad_RejectsInvalidValues(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		env     map[string]string
		wantErr []string
	}{
		{name: "missing version", env: map[string]string{}, wantErr: []string{"APP_VERSION is required"}},
		{name: "blank version", env: map[string]string{"APP_VERSION": "  "}, wantErr: []string{"APP_VERSION is required"}},
		{name: "bad log level", env: map[string]string{"APP_VERSION": "x", "LOG_LEVEL": "loud"}, wantErr: []string{"LOG_LEVEL"}},
		{name: "non-numeric body limit", env: map[string]string{"APP_VERSION": "x", "MAX_BODY_BYTES": "1MB"}, wantErr: []string{"MAX_BODY_BYTES"}},
		{name: "zero body limit", env: map[string]string{"APP_VERSION": "x", "MAX_BODY_BYTES": "0"}, wantErr: []string{"MAX_BODY_BYTES"}},
		{
			name:    "reports every problem at once",
			env:     map[string]string{"LOG_LEVEL": "loud", "MAX_BODY_BYTES": "-1"},
			wantErr: []string{"APP_VERSION", "LOG_LEVEL", "MAX_BODY_BYTES"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := config.Load(lookupFrom(tt.env))
			if err == nil {
				t.Fatal("expected an error")
			}
			for _, want := range tt.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err, want)
				}
			}
		})
	}
}
