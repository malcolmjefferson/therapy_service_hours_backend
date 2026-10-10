package httpapi

import "net/http"

// HealthResponse is the body of GET /api/v1/healthz.
type HealthResponse struct {
	Status  string `json:"status"`
	Version string `json:"version"`
}

// healthz reports that the function is up and which build is running.
// It touches no user data and no database (GO-67); the database check arrives with S0-09.
func healthz(version string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, r, http.StatusOK, "application/json", HealthResponse{Status: "ok", Version: version})
	}
}
