package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// ProblemType is the stable `type` URI of an error kind, so clients branch on it, not on text (ARC-15).
type ProblemType string

// Problem types returned by the API.
const (
	ProblemInternal         ProblemType = "urn:therapy-service-hours:problem:internal"
	ProblemNotFound         ProblemType = "urn:therapy-service-hours:problem:not-found"
	ProblemMethodNotAllowed ProblemType = "urn:therapy-service-hours:problem:method-not-allowed"
	ProblemBodyTooLarge     ProblemType = "urn:therapy-service-hours:problem:body-too-large"
	ProblemForbidden        ProblemType = "urn:therapy-service-hours:problem:forbidden"
	ProblemUnauthorized     ProblemType = "urn:therapy-service-hours:problem:unauthorized"
	ProblemConflict         ProblemType = "urn:therapy-service-hours:problem:conflict"
	ProblemValidation       ProblemType = "urn:therapy-service-hours:problem:validation"
	ProblemClientNameTaken  ProblemType = "urn:therapy-service-hours:problem:client-name-taken"
	ProblemClientInUse      ProblemType = "urn:therapy-service-hours:problem:client-in-use"
)

// Problem is an RFC 7807 problem details body.
type Problem struct {
	Type      ProblemType    `json:"type"`
	Title     string         `json:"title"`
	Status    int            `json:"status"`
	Detail    string         `json:"detail,omitempty"`
	RequestID string         `json:"request_id,omitempty"`
	Errors    []ProblemField `json:"errors,omitempty"`
	// Extensions are the extra members a problem type documents in the OpenAPI spec, for example
	// `existing_client`. They are written at the top level next to the standard members and can
	// never replace one of them.
	Extensions map[string]any `json:"-"`
}

// MarshalJSON writes the standard members and then the extension members (RFC 7807 section 3.2).
func (p Problem) MarshalJSON() ([]byte, error) {
	type plain Problem // No methods, so this does not recurse.
	base, err := json.Marshal(plain(p))
	if err != nil || len(p.Extensions) == 0 {
		return base, err //nolint:wrapcheck // Marshal errors are already specific.
	}
	members := map[string]json.RawMessage{}
	if err := json.Unmarshal(base, &members); err != nil {
		return nil, err //nolint:wrapcheck // Round trip of our own output.
	}
	for name, value := range p.Extensions {
		if _, reserved := members[name]; reserved {
			continue
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, err //nolint:wrapcheck // Marshal errors are already specific.
		}
		members[name] = raw
	}
	return json.Marshal(members) //nolint:wrapcheck // Marshal errors are already specific.
}

// ProblemField is one invalid input field in a validation problem.
type ProblemField struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// fallbackProblem is sent if a response cannot be encoded, which only a programming error causes.
const fallbackProblem = `{"type":"` + string(ProblemInternal) + `","title":"Internal Server Error","status":500}`

// writeProblem sends p as application/problem+json, stamped with the request ID.
func writeProblem(w http.ResponseWriter, r *http.Request, p Problem) {
	p.RequestID = RequestIDFrom(r.Context())
	writeJSON(w, r, p.Status, "application/problem+json", p)
}

// writeJSON encodes v with the given status. Caching headers come from apiHeaders.
func writeJSON(w http.ResponseWriter, r *http.Request, status int, contentType string, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		LoggerFrom(r.Context()).Error("encode response", slog.String("error", err.Error()))
		status = http.StatusInternalServerError
		contentType = "application/problem+json"
		body = []byte(fallbackProblem)
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(status)
	_, _ = w.Write(body) // If this fails the client has gone; there is nobody left to tell.
}

// apiHeaders marks every API response as uncacheable and not content-sniffable (SEC-33),
// including responses written by generated code.
func apiHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Cache-Control", "no-store")
		h.Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}
