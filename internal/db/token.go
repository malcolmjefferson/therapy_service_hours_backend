package db

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// TokenLifetime is how long RDS accepts an IAM auth token.
const TokenLifetime = 15 * time.Minute

// tokenRefreshMargin renews a token this long before it expires, so a connection attempt that
// starts just before expiry does not present a token that has lapsed by the time RDS checks it.
const tokenRefreshMargin = time.Minute

// Signer creates an RDS IAM auth token for a database user. Signing is local; it makes no
// network call (ADR-0001). Declared here, where it is consumed (GO-20).
type Signer interface {
	// Sign returns a token for user at endpoint (host:port) in region, or an error if no
	// credentials are available.
	Sign(ctx context.Context, endpoint, region, user string) (string, error)
}

// Clock returns the current time. Injected so token expiry is testable (GO-24).
type Clock func() time.Time

// TokenSource hands out IAM tokens, signing a new one only when the cached token is near expiry.
// It is safe for concurrent use by the pool.
type TokenSource struct {
	signer   Signer
	now      Clock
	endpoint string
	region   string
	user     string

	mu       sync.Mutex
	token    string
	signedAt time.Time
}

// NewTokenSource returns a TokenSource for the database described by s.
func NewTokenSource(signer Signer, now Clock, s Settings) (*TokenSource, error) {
	if signer == nil || now == nil {
		return nil, fmt.Errorf("token source: signer and clock are required")
	}
	return &TokenSource{signer: signer, now: now, endpoint: s.Endpoint(), region: s.Region, user: s.User}, nil
}

// Token returns a token valid for at least tokenRefreshMargin more.
func (t *TokenSource) Token(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	if t.token != "" && now.Before(t.signedAt.Add(TokenLifetime-tokenRefreshMargin)) {
		return t.token, nil
	}
	token, err := t.signer.Sign(ctx, t.endpoint, t.region, t.user)
	if err != nil {
		// The error never includes the token; signing failed before one existed.
		return "", fmt.Errorf("sign RDS IAM token for %s: %w", t.user, err)
	}
	t.token, t.signedAt = token, now
	return token, nil
}
