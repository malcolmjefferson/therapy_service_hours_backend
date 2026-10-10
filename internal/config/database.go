package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Database is the connection configuration of a VPC Lambda that talks to RDS (ADR-0001).
type Database struct {
	Host     string
	Port     int
	Name     string
	User     string
	Region   string
	AuthMode string
	// Password is only for AuthMode "password" (local containers). CDK never sets it.
	Password string
}

// LoadDatabase reads DB_HOST, DB_PORT, DB_NAME, DB_USER, DB_AUTH (iam or password; default iam),
// AWS_REGION and, for password auth only, DB_PASSWORD. It reports every problem at once.
func LoadDatabase(lookup LookupFunc) (Database, error) {
	var errs []error
	required := func(key string) string {
		v, ok := lookup(key)
		v = strings.TrimSpace(v)
		if !ok || v == "" {
			errs = append(errs, fmt.Errorf("%s is required", key))
		}
		return v
	}
	cfg := Database{
		Host:     required("DB_HOST"),
		Name:     required("DB_NAME"),
		User:     required("DB_USER"),
		AuthMode: valueOr(lookup, "DB_AUTH", "iam"),
		Port:     5432,
	}
	if raw := valueOr(lookup, "DB_PORT", ""); raw != "" {
		port, err := strconv.Atoi(raw)
		if err != nil || port < 1 || port > 65535 {
			errs = append(errs, fmt.Errorf("DB_PORT must be 1-65535, got %q", raw))
		}
		cfg.Port = port
	}
	switch cfg.AuthMode {
	case "iam":
		cfg.Region = required("AWS_REGION")
	case "password":
		cfg.Password = required("DB_PASSWORD")
	default:
		errs = append(errs, fmt.Errorf("DB_AUTH must be iam or password, got %q", cfg.AuthMode))
	}
	if len(errs) > 0 {
		return Database{}, fmt.Errorf("invalid database configuration: %w", errors.Join(errs...))
	}
	return cfg, nil
}
