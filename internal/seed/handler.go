// Package seed is the safe event transport for IAM-authorized identity seeding.
package seed

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/aws/aws-lambda-go/lambdacontext"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
	service "github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/seed"
)

// Runner is the environment-validated identity bootstrap.
type Runner interface {
	Run(context.Context, service.Request) (service.Result, error)
	VerifyBinding(context.Context, service.BindingRequest) (service.BindingResult, error)
}

// Request preserves the existing seed event; verification is a separate operation.
type Request struct {
	service.Request
	Operation         string `json:"operation,omitempty"`
	VerificationEmail string `json:"verification_email,omitempty"`
	ExpectedSubject   string `json:"expected_subject,omitempty"`
}

// Response is safe to show in deployment logs and contains no identity or database attributes.
type Response struct {
	Status string `json:"status"`
	Code   string `json:"code,omitempty"`
	service.Result
	Binding *service.BindingResult `json:"binding,omitempty"`
}

// Handler adapts the trusted operator request to stable, non-sensitive results.
type Handler struct {
	runner Runner
	logger *slog.Logger
}

// New requires the seed runner and a structured logger.
func New(runner Runner, logger *slog.Logger) (*Handler, error) {
	if runner == nil || logger == nil {
		return nil, fmt.Errorf("seed handler requires runner and logger")
	}
	return &Handler{runner: runner, logger: logger}, nil
}

// Handle returns only counts and stable codes, even when the store returns a raw database error.
func (h *Handler) Handle(ctx context.Context, request Request) (Response, error) {
	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	response, err := h.dispatch(ctx, request)
	if err != nil {
		response = Response{Status: "error", Code: "SEED_UNAVAILABLE"}
		var invalid *domain.ValidationError
		switch {
		case errors.Is(err, domain.ErrForbidden):
			response.Code = "SEED_NOT_ALLOWED"
		case errors.Is(err, domain.ErrConflict):
			response.Code = "SEED_CONFLICT"
		case errors.As(err, &invalid):
			response.Code = "INVALID_SEED_REQUEST"
		}
	}
	requestID := ""
	if invocation, ok := lambdacontext.FromContext(ctx); ok {
		requestID = invocation.AwsRequestID
	}
	level := slog.LevelInfo
	if response.Status != "ok" {
		level = slog.LevelError
	}
	h.logger.Log(ctx, level, "identity seed finished", slog.String("status", response.Status), slog.String("code", response.Code), slog.String("request_id", requestID), slog.Int64("duration_ms", time.Since(started).Milliseconds()), slog.Int64("accounts_created", response.AccountsCreated), slog.Int64("invitations_created", response.InvitationsCreated), slog.Int64("invitations_renewed", response.InvitationsRenewed))
	return response, nil
}

func (h *Handler) dispatch(ctx context.Context, request Request) (Response, error) {
	invalid := domain.NewValidationError(domain.FieldError{Field: "operation", Message: "invalid operator request"})
	switch request.Operation {
	case "":
		if request.ExpectedSubject != "" || request.VerificationEmail != "" {
			return Response{}, invalid
		}
		result, err := h.runner.Run(ctx, request.Request)
		return Response{Status: "ok", Result: result}, err
	case "verify-binding":
		if request.AllowProd || request.IncludeDevUsers || request.AdminEmail != "" {
			return Response{}, invalid
		}
		binding, err := h.runner.VerifyBinding(ctx, service.BindingRequest{Email: request.VerificationEmail, Subject: request.ExpectedSubject})
		return Response{Status: "ok", Binding: &binding}, err
	default:
		return Response{}, invalid
	}
}
