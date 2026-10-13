package seed_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	transport "github.com/malcolmjefferson/therapy_service_hours_backend/internal/seed"
	service "github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/seed"
)

type bindingRunner struct{ reads, writes int }

func (r *bindingRunner) Run(context.Context, service.Request) (service.Result, error) {
	r.writes++
	return service.Result{AccountsCreated: 1}, nil
}

func (r *bindingRunner) VerifyBinding(context.Context, service.BindingRequest) (service.BindingResult, error) {
	r.reads++
	return service.BindingResult{Users: 1}, nil
}

func TestBindingOperationIsSeparateFromLegacySeed(t *testing.T) {
	r := &bindingRunner{}
	h, _ := transport.New(r, slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
	var legacy transport.Request
	if err := json.Unmarshal([]byte(`{"admin_email":"admin@example.test","include_dev_users":true,"allow_prod":false}`), &legacy); err != nil {
		t.Fatal(err)
	}
	response, _ := h.Handle(t.Context(), legacy)
	if response.Status != "ok" || response.AccountsCreated != 1 || response.Binding != nil || r.writes != 1 || r.reads != 0 || !legacy.IncludeDevUsers {
		t.Fatal("legacy seed event changed behavior")
	}
}

func TestBindingDispatchRefusesMixedOrUnknownOperations(t *testing.T) {
	r := &bindingRunner{}
	h, _ := transport.New(r, slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
	response, _ := h.Handle(t.Context(), transport.Request{VerificationEmail: "admin@example.test", Operation: "verify-binding", ExpectedSubject: "expected-subject"})
	if response.Status != "ok" || response.Binding == nil || response.Binding.Users != 1 || response.AccountsCreated != 0 || r.writes != 0 || r.reads != 1 {
		t.Fatal("verification dispatched to seed mutation")
	}
	for _, request := range []transport.Request{
		{Operation: "unsupported"},
		{ExpectedSubject: "misplaced-subject"},
		{VerificationEmail: "misplaced@example.test"},
		{Operation: "verify-binding", Request: service.Request{AdminEmail: "admin@example.test"}},
		{Operation: "verify-binding", Request: service.Request{AllowProd: true}},
		{Operation: "verify-binding", Request: service.Request{IncludeDevUsers: true}},
	} {
		response, _ = h.Handle(t.Context(), request)
		if response.Code != "INVALID_SEED_REQUEST" || response.Binding != nil || r.writes != 0 || r.reads != 1 {
			t.Fatal("invalid operation reached storage")
		}
	}
}

func TestBindingTransportNeverExposesInputsOrRawErrors(t *testing.T) {
	for _, storeError := range []error{nil, errors.New("private-subject admin@example.test database.internal")} {
		var logs bytes.Buffer
		h, _ := transport.New(runner{err: storeError}, slog.New(slog.NewJSONHandler(&logs, nil)))
		response, _ := h.Handle(t.Context(), transport.Request{VerificationEmail: "admin@example.test", Operation: "verify-binding", ExpectedSubject: "private-subject"})
		encoded, _ := json.Marshal(response)
		for _, secret := range []string{"private-subject", "admin@example.test", "database.internal"} {
			if strings.Contains(logs.String()+string(encoded), secret) {
				t.Fatal("verification leaked an identity attribute")
			}
		}
		if storeError != nil && (response.Status != "error" || response.Code != "SEED_UNAVAILABLE" || response.Binding != nil) {
			t.Fatal("failed verification exposed partial data")
		}
	}
}
