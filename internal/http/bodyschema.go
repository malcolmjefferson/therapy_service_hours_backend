package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/go-chi/chi/v5"
)

// validateBodies checks every JSON request body against the request schema in api/openapi.yaml
// before the generated handler binds it. The generated decoder ignores unknown fields, but the
// contract forbids them (`additionalProperties: false`), so this is what makes a client-sent
// `hours` or `user_id` a 400 rather than a silent no-op (SEC-26, FR-LOG-03). It also enforces
// types, lengths and required members. The problem names the invalid fields but never echoes
// their values.
func validateBodies(spec *openapi3.T) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			schema := requestBodySchema(spec, r)
			if schema == nil {
				next.ServeHTTP(w, r)
				return
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				WriteError(w, r, err) // The body limit surfaces here as 413.
				return
			}
			var decoded any
			if err := json.Unmarshal(body, &decoded); err != nil {
				badRequest(w, r, err)
				return
			}
			if err := schema.VisitJSON(decoded, openapi3.MultiErrors()); err != nil {
				fields := schemaFields(err)
				LoggerFrom(r.Context()).LogAttrs(r.Context(), slog.LevelInfo, "request rejected",
					slog.Int("status", http.StatusBadRequest), slog.String("problem", string(ProblemValidation)), slog.Int("invalid_fields", len(fields)))
				writeProblem(w, r, Problem{Type: ProblemValidation, Title: "Some fields are invalid", Status: http.StatusBadRequest, Errors: fields})
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			next.ServeHTTP(w, r)
		})
	}
}

// requestBodySchema returns the JSON request schema of the operation chi matched, or nil when the
// operation takes no body.
func requestBodySchema(spec *openapi3.T, r *http.Request) *openapi3.Schema {
	rc := chi.RouteContext(r.Context())
	if rc == nil {
		return nil
	}
	item := spec.Paths.Find(strings.TrimPrefix(rc.RoutePattern(), BasePath))
	if item == nil {
		return nil
	}
	op := item.GetOperation(r.Method)
	if op == nil || op.RequestBody == nil || op.RequestBody.Value == nil {
		return nil
	}
	media := op.RequestBody.Value.Content.Get("application/json")
	if media == nil || media.Schema == nil {
		return nil
	}
	return media.Schema.Value
}

// schemaFields turns schema violations into problem fields without quoting the submitted values.
func schemaFields(err error) []ProblemField {
	var fields []ProblemField
	var multi openapi3.MultiError
	if errors.As(err, &multi) {
		for _, e := range multi {
			fields = append(fields, schemaFields(e)...)
		}
		return fields
	}
	var se *openapi3.SchemaError
	if errors.As(err, &se) {
		return []ProblemField{{Field: fieldName(se), Message: fieldMessage(se)}}
	}
	return []ProblemField{{Field: "body", Message: "is invalid"}}
}

// unsupportedProperty matches kin-openapi's reason for a member the schema does not declare. Only
// the member's name is taken from it, never a value, and only short names are reported.
var unsupportedProperty = regexp.MustCompile(`^property "([^"\\]{1,64})" is unsupported$`)

func fieldName(se *openapi3.SchemaError) string {
	path := se.JSONPointer()
	if m := unsupportedProperty.FindStringSubmatch(se.Reason); m != nil {
		path = append(path[:len(path):len(path)], m[1])
	}
	if len(path) > 0 {
		return strings.Join(path, ".")
	}
	return "body"
}

func fieldMessage(se *openapi3.SchemaError) string {
	switch se.SchemaField {
	case "required":
		return "is required"
	case "properties", "additionalProperties":
		return "is not allowed"
	case "type":
		return "has the wrong type"
	case "minLength":
		return "is too short"
	case "maxLength":
		return "is too long"
	default:
		return "is invalid"
	}
}
