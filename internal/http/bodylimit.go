package httpapi

import (
	"fmt"
	"net/http"
)

// limitBody rejects bodies larger than maxBytes before any handler parses them (SEC-29).
// A declared Content-Length over the limit is refused at once. Otherwise the body is wrapped in
// http.MaxBytesReader; a handler that hits the limit passes the read error to WriteError, which answers 413.
func limitBody(maxBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > maxBytes {
				writeProblem(w, r, bodyTooLarge(maxBytes))
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
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
