package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
)

type requestIDKey struct{}

// RequestIDHeader is the response header that carries the request ID back to the caller.
const RequestIDHeader = "X-Request-Id"

// IDGenerator returns a new unique request ID. It is injected so tests are deterministic (GO-24).
type IDGenerator func() string

// RandomID returns 16 random bytes as hex. It is the production IDGenerator.
func RandomID() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never returns an error since Go 1.24.
	return hex.EncodeToString(b[:])
}

// ContextWithRequestID stores an upstream request ID, such as API Gateway's, for the request ID middleware.
func ContextWithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestIDFrom returns the request ID stored in ctx, or "" if there is none.
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// requestID ensures every request has an ID: the upstream one when present, otherwise a new one.
// Client-supplied X-Request-Id headers are ignored, so callers cannot forge log correlation.
func requestID(newID IDGenerator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			id := RequestIDFrom(ctx)
			if id == "" {
				id = newID()
				ctx = ContextWithRequestID(ctx, id)
			}
			w.Header().Set(RequestIDHeader, id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
