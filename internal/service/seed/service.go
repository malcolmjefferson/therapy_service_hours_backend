// Package seed plans the operator-only identity bootstrap (S1-09).
package seed

import (
	"context"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
)

// Request is accepted only through IAM-authorized Lambda invocation, never an HTTP route.
type Request struct {
	AdminEmail      string `json:"admin_email"`
	IncludeDevUsers bool   `json:"include_dev_users"`
	AllowProd       bool   `json:"allow_prod"`
}

// Account describes the expected identity; the store must not overwrite conflicting roles.
type Account struct {
	Email, FirstName, LastName string
	Admin, Supervisor          bool
}

// Plan is validated against the Lambda's environment before any database work.
type Plan struct {
	Accounts []Account
	At       time.Time
}

// Result contains counts only, so no identity attributes are returned or logged.
type Result struct {
	AccountsCreated    int64 `json:"accounts_created"`
	InvitationsCreated int64 `json:"invitations_created"`
	InvitationsRenewed int64 `json:"invitations_renewed"`
}

// Store applies a complete seed plan or reads binding counts without mutation.
type Store interface {
	Apply(context.Context, Plan) (Result, error)
	VerifyBinding(context.Context, BindingRequest) (BindingResult, error)
}

// Service validates operator input and enforces the dev/prod boundary.
type Service struct {
	store Store
	env   string
	now   func() time.Time
}

// New requires a trusted deployment environment and injected dependencies.
func New(store Store, env string, now func() time.Time) (*Service, error) {
	if store == nil || now == nil || (env != "dev" && env != "prod") {
		return nil, fmt.Errorf("seed requires a store, clock and dev/prod environment")
	}
	return &Service{store: store, env: env, now: now}, nil
}

// Run creates the admin row/invitation and optionally the two invited synthetic dev accounts.
// test-uninvited intentionally has neither a users row nor an invitation (ADR-0011).
func (s *Service) Run(ctx context.Context, request Request) (Result, error) {
	if (s.env == "prod" && !request.AllowProd) || (s.env == "prod" && request.IncludeDevUsers) {
		return Result{}, domain.ErrForbidden
	}
	email := strings.ToLower(strings.TrimSpace(request.AdminEmail))
	address, err := mail.ParseAddress(email)
	if err != nil || address.Address != email || len(email) > 254 || strings.HasSuffix(email, "@example.invalid") {
		return Result{}, domain.NewValidationError(domain.FieldError{Field: "admin_email", Message: "a valid non-test email is required"})
	}
	accounts := []Account{{Email: email, Admin: true}}
	if request.IncludeDevUsers {
		accounts = append(accounts,
			Account{Email: "test-invited@example.invalid", FirstName: "Test", LastName: "Invited"},
			Account{Email: "test-supervisor@example.invalid", FirstName: "Test", LastName: "Supervisor", Supervisor: true},
		)
	}
	result, err := s.store.Apply(ctx, Plan{Accounts: accounts, At: s.now().UTC()})
	if err != nil {
		return Result{}, fmt.Errorf("apply identity seed: %w", err)
	}
	return result, nil
}
