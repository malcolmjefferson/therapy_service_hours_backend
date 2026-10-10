package httpapi

import (
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
)

// recoverPanics turns a panic in a handler into a 500 problem response and logs the stack (GO-35).
// The stack goes to the log only, never to the client (SEC-34). http.ErrAbortHandler is re-raised
// because it is the standard way to abort a response on purpose.
func recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			if rec == http.ErrAbortHandler { //nolint:errorlint // recover() returns the exact sentinel value.
				panic(rec)
			}
			LoggerFrom(r.Context()).LogAttrs(r.Context(), slog.LevelError, "panic recovered",
				slog.String("panic", fmt.Sprint(rec)),
				slog.String("stack", string(debug.Stack())),
			)
			writeProblem(w, r, Problem{Type: ProblemInternal, Title: "Internal Server Error", Status: http.StatusInternalServerError})
		}()
		next.ServeHTTP(w, r)
	})
}
