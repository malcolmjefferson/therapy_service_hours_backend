// Package rolebootstrap creates the database roles at deploy time (ADR-0001, amended 2026-10-10).
//
// CloudFormation does not resolve Secrets Manager dynamic references in custom-resource
// properties, and VPC Lambdas cannot reach Secrets Manager (no NAT, no endpoints). So the work is
// split in two:
//
//   - the provider (custom resource handler, outside the VPC) reads only the RDS-managed master
//     secret and invokes the worker directly;
//   - the worker (in the VPC) receives the connection in that one invocation payload and runs the
//     idempotent role SQL.
//
// The password never appears in a template, an environment variable or a log line.
package rolebootstrap

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
)

// Request is the worker's invocation payload.
type Request struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Database string `json:"database"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// String hides the password from fmt and %v.
func (r Request) String() string {
	return fmt.Sprintf("{host:%s port:%d database:%s username:%s password:[REDACTED]}", r.Host, r.Port, r.Database, r.Username)
}

// GoString hides the password from %#v.
func (r Request) GoString() string { return r.String() }

// LogValue hides the password from slog (SEC-50).
func (r Request) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("host", r.Host), slog.Int("port", r.Port),
		slog.String("database", r.Database), slog.String("username", r.Username),
	)
}

// Validate reports missing fields without echoing any value.
func (r Request) Validate() error {
	var missing []string
	for name, empty := range map[string]bool{
		"host": r.Host == "", "database": r.Database == "", "username": r.Username == "", "password": r.Password == "",
	} {
		if empty {
			missing = append(missing, name)
		}
	}
	if r.Port < 1 || r.Port > 65535 {
		missing = append(missing, "port")
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		return fmt.Errorf("bootstrap request is missing or has invalid: %v", missing)
	}
	return nil
}

// Response is the worker's result.
type Response struct {
	Status string   `json:"status"`
	Roles  []string `json:"roles"`
}

// StatusOK marks a successful Response.
const StatusOK = "ok"

// masterSecret is the JSON RDS stores in its managed secret.
type masterSecret struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// parseMasterSecret reads the RDS-managed secret string. Errors never include the secret.
func parseMasterSecret(secretString string) (masterSecret, error) {
	var s masterSecret
	if err := json.Unmarshal([]byte(secretString), &s); err != nil {
		return masterSecret{}, fmt.Errorf("master secret is not the expected JSON (username, password)")
	}
	if s.Username == "" || s.Password == "" {
		return masterSecret{}, fmt.Errorf("master secret has no username or password")
	}
	return s, nil
}
