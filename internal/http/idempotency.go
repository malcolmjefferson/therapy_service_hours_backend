package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/go-chi/chi/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/idempotency"
)

const (
	idempotencyHeader = "Idempotency-Key"
	// replayHeader marks a response that was stored by an earlier request with the same key.
	replayHeader = "Idempotency-Replayed"
	// settleTimeout bounds the bookkeeping after the handler finished, which must still run when the
	// client has gone away.
	settleTimeout = 5 * time.Second
)

// idempotentOperations lists the operations whose contract declares an Idempotency-Key header
// parameter, as "METHOD route". The spec is the source of truth, so a new create endpoint gets the
// behavior by declaring the parameter (GO-57, ARC-17).
func idempotentOperations(spec *openapi3.T) map[string]bool {
	ops := map[string]bool{}
	for path, item := range spec.Paths.Map() {
		for method, op := range item.Operations() {
			if method != http.MethodPost {
				continue
			}
			for _, p := range append(append(openapi3.Parameters{}, item.Parameters...), op.Parameters...) {
				if p.Value != nil && p.Value.In == openapi3.ParameterInHeader && strings.EqualFold(p.Value.Name, idempotencyHeader) {
					ops[method+" "+BasePath+path] = true
				}
			}
		}
	}
	return ops
}

// idempotent makes the create operations safe to retry. A request that presents a key claims it
// before the handler runs. The same key with the same request replays the stored response; the same
// key with a different request is 409. Only successful responses are stored: an error changed
// nothing, so a retry of it runs again. A missing or malformed key passes through to the generated
// binder, which answers 400.
func idempotent(spec *openapi3.T, store idempotency.Store) func(http.Handler) http.Handler {
	operations := idempotentOperations(spec)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rc := chi.RouteContext(r.Context())
			if rc == nil || !operations[r.Method+" "+rc.RoutePattern()] {
				next.ServeHTTP(w, r)
				return
			}
			var key openapi_types.UUID
			if err := key.UnmarshalText([]byte(r.Header.Get(idempotencyHeader))); err != nil {
				next.ServeHTTP(w, r)
				return
			}
			actor := ActorFrom(r.Context())
			if actor.ID == "" {
				WriteError(w, r, domain.ErrForbidden)
				return
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				WriteError(w, r, err) // The body limit surfaces here as 413.
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			run := idempotentRun{store: store, userID: actor.ID, key: key.String(), hash: requestHash(r, body)}
			run.serve(w, r, next)
		})
	}
}

// idempotentRun is one request's pass through the idempotency store.
type idempotentRun struct {
	store  idempotency.Store
	userID string
	key    string
	hash   []byte
}

func (u idempotentRun) serve(w http.ResponseWriter, r *http.Request, next http.Handler) {
	claim, err := u.store.Claim(r.Context(), u.userID, u.key, u.hash)
	if err != nil {
		WriteError(w, r, err) // Fail closed: without the claim the create must not run.
		return
	}
	switch claim.Outcome {
	case idempotency.Replay:
		LoggerFrom(r.Context()).LogAttrs(r.Context(), slog.LevelInfo, "idempotent replay", slog.Int("status", claim.Response.Status), slog.String("actor_id", u.userID))
		if claim.Response.ContentType != "" {
			w.Header().Set("Content-Type", claim.Response.ContentType)
		}
		w.Header().Set(replayHeader, "true")
		w.WriteHeader(claim.Response.Status)
		_, _ = w.Write(claim.Response.Body) // If this fails the client has gone; there is nobody left to tell.
	case idempotency.Mismatch:
		writeProblem(w, r, Problem{
			Type: ProblemIdempotencyKeyReused, Title: "Idempotency-Key reused", Status: http.StatusConflict,
			Detail: "This Idempotency-Key was already used with a different request. Send a new key for a new action.",
		})
	case idempotency.InProgress:
		w.Header().Set("Retry-After", "1")
		writeProblem(w, r, Problem{
			Type: ProblemIdempotencyInProgress, Title: "Request still in progress", Status: http.StatusConflict,
			Detail: "A request with this Idempotency-Key is still running. Retry the same request shortly.",
		})
	case idempotency.Acquired:
		u.run(w, r, next)
	default:
		WriteError(w, r, errors.New("unknown idempotency outcome"))
	}
}

// run executes the handler against a buffer, settles the claim, then sends the buffered response.
func (u idempotentRun) run(w http.ResponseWriter, r *http.Request, next http.Handler) {
	settled := false
	defer func() {
		if !settled { // The handler panicked: free the key before recoverPanics answers 500.
			u.release(r)
		}
	}()
	rec := &responseRecorder{header: w.Header(), status: http.StatusOK}
	next.ServeHTTP(rec, r)
	settled = true
	if rec.status >= http.StatusOK && rec.status < http.StatusMultipleChoices {
		u.complete(r, rec)
	} else {
		u.release(r)
	}
	rec.flushTo(w)
}

func (u idempotentRun) complete(r *http.Request, rec *responseRecorder) {
	ctx, cancel := settleContext(r.Context())
	defer cancel()
	err := u.store.Complete(ctx, u.userID, u.key, u.hash, idempotency.Response{Status: rec.status, ContentType: rec.contentType, Body: rec.body.Bytes()})
	if err != nil {
		// The create already happened and the client gets its result. A retry will not replay it
		// (the entry's own overlap rule still stops a duplicate), so this is worth an alert.
		LoggerFrom(r.Context()).LogAttrs(r.Context(), slog.LevelError, "idempotency response not stored", slog.String("actor_id", u.userID), slog.Bool("claim_lost", errors.Is(err, idempotency.ErrClaimLost)))
	}
}

func (u idempotentRun) release(r *http.Request) {
	ctx, cancel := settleContext(r.Context())
	defer cancel()
	if err := u.store.Release(ctx, u.userID, u.key, u.hash); err != nil {
		// The claim expires by itself after idempotency.ClaimLease, so a retry is only delayed.
		LoggerFrom(r.Context()).LogAttrs(r.Context(), slog.LevelWarn, "idempotency claim not released", slog.String("actor_id", u.userID))
	}
}

func settleContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), settleTimeout)
}

// requestHash identifies a request: method, path and the body with its members in canonical order,
// so a retry that re-serializes the same JSON still matches. Keys and other headers are not part of it.
func requestHash(r *http.Request, body []byte) []byte {
	h := sha256.New()
	h.Write([]byte(r.Method))
	h.Write([]byte{0})
	h.Write([]byte(r.URL.Path))
	h.Write([]byte{0})
	h.Write(canonicalJSON(body))
	return h.Sum(nil)
}

// canonicalJSON re-encodes body with sorted members. The body was validated as JSON upstream; if it
// somehow is not, the raw bytes are hashed instead.
func canonicalJSON(body []byte) []byte {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber() // Keep numbers exactly as sent.
	var v any
	if err := dec.Decode(&v); err != nil {
		return body
	}
	out, err := json.Marshal(v)
	if err != nil {
		return body
	}
	return out
}

// responseRecorder holds the handler's response until the claim is settled. It shares the real
// header map, so headers set by earlier middleware and by the handler reach the client unchanged.
type responseRecorder struct {
	header      http.Header
	status      int
	wroteHeader bool
	contentType string
	body        bytes.Buffer
}

func (rec *responseRecorder) Header() http.Header { return rec.header }

func (rec *responseRecorder) WriteHeader(status int) {
	if rec.wroteHeader {
		return
	}
	rec.wroteHeader, rec.status = true, status
	rec.contentType = rec.header.Get("Content-Type")
}

func (rec *responseRecorder) Write(p []byte) (int, error) {
	rec.WriteHeader(http.StatusOK)
	return rec.body.Write(p) //nolint:wrapcheck // bytes.Buffer.Write never fails.
}

func (rec *responseRecorder) flushTo(w http.ResponseWriter) {
	w.WriteHeader(rec.status)
	_, _ = w.Write(rec.body.Bytes()) // If this fails the client has gone; there is nobody left to tell.
}
