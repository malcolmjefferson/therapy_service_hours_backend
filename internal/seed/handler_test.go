package seed_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
	transport "github.com/malcolmjefferson/therapy_service_hours_backend/internal/seed"
	service "github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/seed"
)

type runner struct{ err error }

func (r runner) Run(context.Context, service.Request) (service.Result, error) {
	return service.Result{AccountsCreated: 1}, r.err
}

func TestSeedTransportNeverExposesIdentityOrRawErrors(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code string
	}{
		{domain.ErrForbidden, "SEED_NOT_ALLOWED"}, {domain.ErrConflict, "SEED_CONFLICT"},
		{domain.NewValidationError(domain.FieldError{Field: "admin_email", Message: "invalid"}), "INVALID_SEED_REQUEST"},
		{errors.New("password=private-token admin@example.test database.internal"), "SEED_UNAVAILABLE"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			var logs bytes.Buffer
			h, _ := transport.New(runner{err: tc.err}, slog.New(slog.NewJSONHandler(&logs, nil)))
			response, err := h.Handle(t.Context(), service.Request{AdminEmail: "admin@example.test"})
			if err != nil || response.Status != "error" || response.Code != tc.code || response.AccountsCreated != 0 {
				t.Fatalf("unexpected response: %+v", response)
			}
			encoded, _ := json.Marshal(response)
			for _, secret := range []string{"admin@example.test", "private-token", "database.internal"} {
				if strings.Contains(logs.String()+string(encoded), secret) {
					t.Fatal("sensitive data leaked")
				}
			}
		})
	}
}
