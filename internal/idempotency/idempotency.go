// Package idempotency defines the Idempotency-Key store contract (S2-09, GO-57, ARC-17). The HTTP
// middleware depends on Store; PostgreSQL implements it in internal/db.
package idempotency

import (
	"context"
	"errors"
	"time"
)

// KeyTTL is how long a key is remembered. A retry inside this window replays the original response;
// after it the key is free again and the scheduled cleanup may delete the record.
const KeyTTL = 24 * time.Hour

// ClaimLease is how long an in-progress claim blocks other requests with the same key. It is longer
// than the API's own deadline, so a live request keeps its claim, and short enough that a request
// that died (Lambda timeout) does not block its key until KeyTTL.
const ClaimLease = 60 * time.Second

// ErrClaimLost means the claim was no longer held when the request tried to complete it, for
// example because its lease ran out and another request took the key over.
var ErrClaimLost = errors.New("idempotency claim is no longer held")

// Outcome says what a request that presents a key must do.
type Outcome int

// Outcomes of Store.Claim.
const (
	// Acquired: the key is new (or expired); run the request, then Complete or Release the claim.
	Acquired Outcome = iota + 1
	// Replay: the same request already succeeded; answer with Claim.Response.
	Replay
	// Mismatch: the key was used with a different request; answer 409.
	Mismatch
	// InProgress: the same request is still running; answer 409 and ask the client to retry.
	InProgress
)

// Response is the stored result of a successful request, replayed verbatim.
type Response struct {
	Status      int
	ContentType string
	Body        []byte
}

// Claim is the result of Store.Claim. Response is set only for Replay.
type Claim struct {
	Outcome  Outcome
	Response Response
}

// Store keeps idempotency records per user. requestHash is the SHA-256 of the request.
type Store interface {
	// Claim takes the key for this request, or reports why it cannot.
	Claim(ctx context.Context, userID, key string, requestHash []byte) (Claim, error)
	// Complete stores the successful response under a held claim. It returns ErrClaimLost if the
	// claim is gone.
	Complete(ctx context.Context, userID, key string, requestHash []byte, response Response) error
	// Release frees a held claim whose request did not succeed, so the client can retry the key.
	Release(ctx context.Context, userID, key string, requestHash []byte) error
}
