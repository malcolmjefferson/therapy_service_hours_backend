package httpapi

import (
	"fmt"
	"net/http"
)

// limitBody rejects bodies larger than maxBytes before any handler parses them (SEC-29). The import
// upload is the one route with its own, larger limit: its body is a multipart wrapper around a file
// of up to 1 MiB, which the handler then checks exactly.
// A declared Content-Length over the limit is refused at once. Otherwise the body is wrapped in
// http.MaxBytesReader; a handler that hits the limit passes the read error to WriteError, which answers 413.
func limitBody(maxBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			limit := maxBytes
			if r.Method == http.MethodPost && r.URL.Path == importPath {
				limit = maxImportBodyBytes
			}
			if r.ContentLength > limit {
				writeProblem(w, r, bodyTooLarge(limit))
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, limit)
			next.ServeHTTP(w, r)
		})
	}
}

func bodyTooLarge(maxBytes int64) Problem {
	return Problem{
		Type:   ProblemBodyTooLarge,
		Title:  "Request body too large",
		Status: http.StatusRequestEntityTooLarge,
		Detail: fmt.Sprintf("The request body must be at most %d bytes.", maxBytes),
	}
}
