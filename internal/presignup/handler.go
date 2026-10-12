// Package presignup translates trusted Cognito events into invitation checks and safe errors.
package presignup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambdacontext"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
)

// RequestBudget bounds all admission I/O below the Cognito trigger timeout.
const RequestBudget = 4 * time.Second

// Stable client-visible codes do not expose account existence or internal errors.
var (
	ErrDenied      = errors.New("SIGN_UP_NOT_ALLOWED")
	ErrUnavailable = errors.New("SIGN_UP_UNAVAILABLE")
)

// Checker is the admission use case consumed by the event adapter.
type Checker interface {
	Check(context.Context, string) error
}

// Handler reuses the service and warmed database connection across requests.
type Handler struct {
	checker      Checker
	logger       *slog.Logger
	devTestUsers bool
}

// New constructs a handler. devTestUsers permits only ADR-0011's three synthetic admin-created
// users with matching example.invalid emails; it never bypasses the federated Google gate.
func New(checker Checker, logger *slog.Logger, devTestUsers bool) (*Handler, error) {
	if checker == nil || logger == nil {
		return nil, fmt.Errorf("pre-sign-up: checker and logger are required")
	}
	return &Handler{checker: checker, logger: logger, devTestUsers: devTestUsers}, nil
}

// Handle returns Cognito's original event with verification flags cleared; verified Google
// email comes from the IdP mapping, never from autoVerifyEmail (which can transfer an alias).
func (h *Handler) Handle(ctx context.Context, event events.CognitoEventUserPoolsPreSignup) (events.CognitoEventUserPoolsPreSignup, error) {
	started := time.Now()
	event.Response = events.CognitoEventUserPoolsPreSignupResponse{}
	var err error
	switch {
	case h.syntheticAdminUser(event):
		// The uninvited native test user intentionally exists so S1-06/E2E can prove API denial.
	case event.TriggerSource == "PreSignUp_ExternalProvider" && googleUsername(event.UserName) && event.Request.UserAttributes["email_verified"] == "true":
		requestCtx, cancel := context.WithTimeout(ctx, RequestBudget)
		defer cancel()
		err = h.checker.Check(requestCtx, event.Request.UserAttributes["email"])
		if err == nil {
			err = requestCtx.Err()
		}
	default:
		err = domain.ErrForbidden
	}
	code, level := "allowed", slog.LevelInfo
	if err != nil {
		code, level = "SIGN_UP_UNAVAILABLE", slog.LevelError
		if errors.Is(err, domain.ErrForbidden) {
			code, level = "SIGN_UP_NOT_ALLOWED", slog.LevelWarn
		}
	}
	requestID := ""
	if lc, ok := lambdacontext.FromContext(ctx); ok {
		requestID = lc.AwsRequestID
	}
	h.logger.Log(ctx, level, "pre-sign-up decision", slog.String("code", code), slog.String("request_id", requestID), slog.Int64("duration_ms", time.Since(started).Milliseconds()))
	if err != nil {
		if errors.Is(err, domain.ErrForbidden) {
			return event, ErrDenied
		}
		return event, ErrUnavailable
	}
	return event, nil
}

// Cognito lowercases the provider prefix in case-insensitive pools. Preserve the
// full username in the event and compare only the fixed provider label.
func googleUsername(username string) bool {
	const prefix = "Google_"
	return len(username) > len(prefix) && strings.EqualFold(username[:len(prefix)], prefix)
}

func (h *Handler) syntheticAdminUser(event events.CognitoEventUserPoolsPreSignup) bool {
	if !h.devTestUsers || event.TriggerSource != "PreSignUp_AdminCreateUser" {
		return false
	}
	switch event.UserName {
	case "test-invited", "test-uninvited", "test-supervisor":
		return event.Request.UserAttributes["email"] == event.UserName+"@example.invalid"
	default:
		return false
	}
}
