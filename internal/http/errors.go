package httpapi

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
)

// problemFor maps an error to its problem response. It is the only place domain errors become
// HTTP statuses (GO-33). Unknown errors become a generic 500 that reveals nothing internal.
func problemFor(err error) Problem {
	var verr *domain.ValidationError
	var maxErr *http.MaxBytesError
	switch {
	case errors.As(err, &maxErr):
		return bodyTooLarge(maxErr.Limit)
	case errors.As(err, &verr):
		fields := make([]ProblemField, 0, len(verr.Fields))
		for _, f := range verr.Fields {
			fields = append(fields, ProblemField{Field: f.Field, Message: f.Message})
		}
		return Problem{Type: ProblemValidation, Title: "Some fields are invalid", Status: http.StatusBadRequest, Errors: fields}
	case errors.Is(err, domain.ErrNotFound):
		return Problem{Type: ProblemNotFound, Title: "Not Found", Status: http.StatusNotFound}
	case errors.Is(err, domain.ErrForbidden):
		return Problem{Type: ProblemForbidden, Title: "Forbidden", Status: http.StatusForbidden}
	case errors.Is(err, domain.ErrConflict):
		return Problem{Type: ProblemConflict, Title: "Conflict", Status: http.StatusConflict}
	default:
		return Problem{Type: ProblemInternal, Title: "Internal Server Error", Status: http.StatusInternalServerError}
	}
}

// WriteError logs err once and writes the matching problem response (GO-65).
// Client errors log at info and server errors at error. The error text is never sent to the client.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	p := problemFor(err)
	level := slog.LevelInfo
	if p.Status >= http.StatusInternalServerError {
		level = slog.LevelError
	}
	LoggerFrom(r.Context()).LogAttrs(r.Context(), level, "request failed",
		slog.Int("status", p.Status), slog.String("problem", string(p.Type)), slog.String("error", err.Error()))
	writeProblem(w, r, p)
}
