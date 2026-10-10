package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/http/gen"
)

// server implements the generated strict server interface, so every handler matches
// api/openapi.yaml at compile time (GO-52, ADR-0006). Handlers only translate between HTTP
// and use cases (GO-10).
type server struct {
	version string
}

var _ gen.StrictServerInterface = (*server)(nil)

// GetHealth reports that the API is up and which build is running.
func (s *server) GetHealth(_ context.Context, _ gen.GetHealthRequestObject) (gen.GetHealthResponseObject, error) {
	return gen.GetHealth200JSONResponse{Status: gen.Ok, Version: s.version}, nil
}

// mountAPI registers the generated routes under BasePath on r.
func mountAPI(r chi.Router, s *server) {
	strict := gen.NewStrictHandlerWithOptions(s, nil, gen.StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  badRequest,
		ResponseErrorHandlerFunc: WriteError,
	})
	gen.HandlerWithOptions(strict, gen.ChiServerOptions{
		BaseURL:          BasePath,
		BaseRouter:       r,
		ErrorHandlerFunc: badRequest,
	})
}

// badRequest answers a request the generated code could not bind (bad parameter or body).
// The problem names the parameter when known but never echoes the submitted value.
func badRequest(w http.ResponseWriter, r *http.Request, err error) {
	p := Problem{Type: ProblemValidation, Title: "The request is malformed", Status: http.StatusBadRequest}
	if name := paramName(err); name != "" {
		p.Errors = []ProblemField{{Field: name, Message: "is missing or has the wrong format"}}
	}
	LoggerFrom(r.Context()).LogAttrs(r.Context(), slog.LevelInfo, "request rejected",
		slog.Int("status", p.Status), slog.String("problem", string(p.Type)), slog.String("param", paramName(err)))
	writeProblem(w, r, p)
}

// paramName extracts the parameter name from the generated binding errors.
func paramName(err error) string {
	var (
		format   *gen.InvalidParamFormatError
		required *gen.RequiredParamError
		header   *gen.RequiredHeaderError
		unmarsh  *gen.UnmarshalingParamError
		tooMany  *gen.TooManyValuesForParamError
	)
	switch {
	case errors.As(err, &format):
		return format.ParamName
	case errors.As(err, &required):
		return required.ParamName
	case errors.As(err, &header):
		return header.ParamName
	case errors.As(err, &unmarsh):
		return unmarsh.ParamName
	case errors.As(err, &tooMany):
		return tooMany.ParamName
	default:
		return ""
	}
}
