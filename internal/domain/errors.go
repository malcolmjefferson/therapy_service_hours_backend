// Package domain holds entities, value objects and domain errors. It imports nothing internal (GO-01).
package domain

import (
	"errors"
	"fmt"
	"strings"
)

// Domain errors returned by services and stores. Check them with errors.Is (GO-32).
var (
	// ErrNotFound means the resource does not exist, or the actor may not know it exists (GO-36).
	ErrNotFound = errors.New("not found")
	// ErrForbidden means the actor is known but not allowed to perform the action.
	ErrForbidden = errors.New("forbidden")
	// ErrConflict means the resource is not in the state the action requires, for example already approved.
	ErrConflict = errors.New("conflict")
)

// FieldError describes one invalid input field.
type FieldError struct {
	// Field is the JSON name of the input, for example "end_time".
	Field string
	// Message says what is wrong, in plain language.
	Message string
}

// ValidationError reports input that breaks a business rule. Match it with errors.As.
type ValidationError struct {
	Fields []FieldError
}

// NewValidationError returns a ValidationError for the given fields.
func NewValidationError(fields ...FieldError) *ValidationError {
	return &ValidationError{Fields: fields}
}

// Error lists the invalid fields.
func (e *ValidationError) Error() string {
	parts := make([]string, 0, len(e.Fields))
	for _, f := range e.Fields {
		parts = append(parts, fmt.Sprintf("%s: %s", f.Field, f.Message))
	}
	return "validation failed: " + strings.Join(parts, "; ")
}
