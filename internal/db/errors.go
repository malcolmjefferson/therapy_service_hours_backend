package db

import (
	"errors"
	"fmt"
)

// ErrUnavailable means the database could not be reached or refused the connection.
// Callers match it with errors.Is; the HTTP layer will map it to 503 (GO-58).
var ErrUnavailable = errors.New("database unavailable")

// ConnectError is returned when the pool cannot open a working connection.
type ConnectError struct {
	// Endpoint is host:port. It never includes credentials.
	Endpoint string
	Err      error
}

// Error describes the failure without the password or token.
func (e *ConnectError) Error() string {
	return fmt.Sprintf("connect to database at %s: %v", e.Endpoint, e.Err)
}

// Unwrap returns the underlying driver error.
func (e *ConnectError) Unwrap() error { return e.Err }

// Is makes every ConnectError match ErrUnavailable.
func (e *ConnectError) Is(target error) bool { return target == ErrUnavailable }
