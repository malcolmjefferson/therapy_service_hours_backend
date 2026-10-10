// Package config parses and validates the environment configuration once at startup (GO-68).
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
)

// DefaultMaxBodyBytes caps request bodies when MAX_BODY_BYTES is unset (SEC-29).
const DefaultMaxBodyBytes int64 = 1 << 20

// Config is the typed configuration shared by every Lambda entry point.
type Config struct {
	// Version identifies the deployed build, for example the backend commit SHA. Reported by /healthz.
	Version string
	// Function is the Lambda function name, added to every log line (GO-63).
	Function string
	// LogLevel is the minimum level written to the log.
	LogLevel slog.Level
	// MaxBodyBytes is the largest request body the API accepts.
	MaxBodyBytes int64
	// LocalHTTPAddr, when set, serves plain HTTP on this address instead of the Lambda runtime.
	// It exists for local development only; CDK never sets it.
	LocalHTTPAddr string
}

// LookupFunc reads an environment variable, like os.LookupEnv.
type LookupFunc func(key string) (string, bool)

// Load builds a Config from environment variables and reports every invalid or missing value at once.
func Load(lookup LookupFunc) (Config, error) {
	var errs []error
	cfg := Config{
		Function:      valueOr(lookup, "AWS_LAMBDA_FUNCTION_NAME", "api"),
		LocalHTTPAddr: valueOr(lookup, "LOCAL_HTTP_ADDR", ""),
		MaxBodyBytes:  DefaultMaxBodyBytes,
	}

	version, ok := lookup("APP_VERSION")
	version = strings.TrimSpace(version)
	if !ok || version == "" {
		errs = append(errs, errors.New("APP_VERSION is required"))
	}
	cfg.Version = version

	level, err := parseLevel(valueOr(lookup, "LOG_LEVEL", "info"))
	if err != nil {
		errs = append(errs, err)
	}
	cfg.LogLevel = level

	if raw, ok := lookup("MAX_BODY_BYTES"); ok && raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n <= 0 {
			errs = append(errs, fmt.Errorf("MAX_BODY_BYTES must be a positive integer, got %q", raw))
		} else {
			cfg.MaxBodyBytes = n
		}
	}

	if len(errs) > 0 {
		return Config{}, fmt.Errorf("invalid configuration: %w", errors.Join(errs...))
	}
	return cfg, nil
}

func valueOr(lookup LookupFunc, key, fallback string) string {
	if v, ok := lookup(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return fallback
}

func parseLevel(raw string) (slog.Level, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(raw)); err != nil {
		return slog.LevelInfo, fmt.Errorf("LOG_LEVEL must be debug, info, warn or error, got %q", raw)
	}
	return level, nil
}
