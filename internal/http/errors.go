package httpapi

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/authn"
)

// problemFor maps an error to its problem response. It is the only place domain errors become
// HTTP statuses (GO-33). Unknown errors become a generic 500 that reveals nothing internal.
func problemFor(err error) Problem {
	var verr *domain.ValidationError
	var nameTaken *domain.ClientNameTakenError
	var inUse *domain.ClientInUseError
	var maxErr *http.MaxBytesError
	switch {
	case errors.Is(err, authn.ErrUnauthorized):
		return Problem{Type: ProblemUnauthorized, Title: "Unauthorized", Status: http.StatusUnauthorized}
	case errors.As(err, &maxErr):
		return bodyTooLarge(maxErr.Limit)
	case errors.As(err, &verr):
		fields := make([]ProblemField, 0, len(verr.Fields))
		for _, f := range verr.Fields {
			fields = append(fields, ProblemField{Field: f.Field, Message: f.Message})
		}
		return Problem{Type: ProblemValidation, Title: "Some fields are invalid", Status: http.StatusBadRequest, Errors: fields}
	case errors.As(err, &nameTaken):
		// The existing client is the caller's own, so naming it leaks nothing.
		return Problem{Type: ProblemClientNameTaken, Title: "Client name already saved", Status: http.StatusConflict,
			Extensions: map[string]any{"existing_client": clientDTO(nameTaken.Existing)}}
	case errors.As(err, &inUse):
		return Problem{Type: ProblemClientInUse, Title: "Client is used by entries", Status: http.StatusConflict,
			Extensions: map[string]any{"entry_count": inUse.EntryCount}}
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
	} else if p.Status == http.StatusForbidden || p.Status == http.StatusUnauthorized {
		level = slog.LevelWarn
	}
	if p.Status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", "Bearer")
	}
	LoggerFrom(r.Context()).LogAttrs(r.Context(), level, "request failed",
		slog.Int("status", p.Status), slog.String("problem", string(p.Type)), slog.String("action", "api_request"), slog.String("resource_type", "user"), slog.String("actor_id", ActorFrom(r.Context()).ID))
	writeProblem(w, r, p)
}
