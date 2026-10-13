package httpapi

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// BasePath is the prefix of every API route (CLAUDE.md §10).
const BasePath = "/api/v1"

// RouterDeps are the dependencies of the API router. All are required.
type RouterDeps struct {
	// Logger is the base logger; it should already carry the function name.
	Logger *slog.Logger
	// Version is reported by the health check.
	Version string
	// MaxBodyBytes caps request bodies.
	MaxBodyBytes int64
	// NewRequestID generates IDs for requests that arrive without one.
	NewRequestID IDGenerator
	// Now is the clock used for request durations.
	Now Clock
	// ActorResolver authenticates protected routes; health never calls it.
	ActorResolver ActorResolver
	// Entries serves the log-hours endpoints.
	Entries EntryLister
}

func (d RouterDeps) validate() error {
	var errs []error
	if d.Logger == nil {
		errs = append(errs, errors.New("logger is required"))
	}
	if d.Version == "" {
		errs = append(errs, errors.New("version is required"))
	}
	if d.MaxBodyBytes <= 0 {
		errs = append(errs, errors.New("max body bytes must be positive"))
	}
	if d.NewRequestID == nil {
		errs = append(errs, errors.New("request ID generator is required"))
	}
	if d.Now == nil {
		errs = append(errs, errors.New("clock is required"))
	}
	if d.ActorResolver == nil {
		errs = append(errs, errors.New("actor resolver is required"))
	}
	if d.Entries == nil {
		errs = append(errs, errors.New("entries service is required"))
	}
	return errors.Join(errs...)
}

// NewRouter builds the API handler.
func NewRouter(deps RouterDeps) (http.Handler, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	r := newMux(deps)
	mountAPI(r, &server{version: deps.Version, entries: deps.Entries}, deps.ActorResolver)
	return r, nil
}

// newMux returns a router with the middleware stack and problem+json fallbacks, but no routes.
// Tests use it to mount probe handlers behind the same middleware as production.
//
// Middleware order matters: the request ID comes first so every later log line carries it,
// and panic recovery sits inside logging so the access log records the 500.
func newMux(deps RouterDeps) *chi.Mux {
	r := chi.NewRouter()
	r.Use(requestID(deps.NewRequestID))
	r.Use(accessLog(deps.Logger, deps.Now))
	r.Use(recoverPanics)
	r.Use(limitBody(deps.MaxBodyBytes))
	r.Use(apiHeaders)

	r.NotFound(func(w http.ResponseWriter, req *http.Request) {
		writeProblem(w, req, Problem{Type: ProblemNotFound, Title: "Not Found", Status: http.StatusNotFound})
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, req *http.Request) {
		writeProblem(w, req, Problem{Type: ProblemMethodNotAllowed, Title: "Method Not Allowed", Status: http.StatusMethodNotAllowed})
	})
	return r
}
