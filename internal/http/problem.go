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
	ProblemConflict         ProblemType = "urn:therapy-service-hours:problem:conflict"
	ProblemValidation       ProblemType = "urn:therapy-service-hours:problem:validation"
)

// Problem is an RFC 7807 problem details body.
type Problem struct {
	Type      ProblemType    `json:"type"`
	Title     string         `json:"title"`
	Status    int            `json:"status"`
	Detail    string         `json:"detail,omitempty"`
	RequestID string         `json:"request_id,omitempty"`
	Errors    []ProblemField `json:"errors,omitempty"`
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

// writeJSON encodes v with the given status. API responses are never cached (SEC-33).
func writeJSON(w http.ResponseWriter, r *http.Request, status int, contentType string, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		LoggerFrom(r.Context()).Error("encode response", slog.String("error", err.Error()))
		status = http.StatusInternalServerError
		contentType = "application/problem+json"
		body = []byte(fallbackProblem)
	}
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write(body) // If this fails the client has gone; there is nobody left to tell.
}
