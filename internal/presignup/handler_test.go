package presignup_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambdacontext"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/presignup"
)

type checker struct {
	err       error
	calls     int
	remaining time.Duration
}

func (c *checker) Check(ctx context.Context, _ string) error {
	c.calls++
	if deadline, ok := ctx.Deadline(); ok {
		c.remaining = time.Until(deadline)
	}
	return c.err
}

func googleEvent() events.CognitoEventUserPoolsPreSignup {
	return events.CognitoEventUserPoolsPreSignup{
		CognitoEventUserPoolsHeader: events.CognitoEventUserPoolsHeader{TriggerSource: "PreSignUp_ExternalProvider", UserName: "Google_synthetic", UserPoolID: "test-pool"},
		Request:                     events.CognitoEventUserPoolsPreSignupRequest{UserAttributes: map[string]string{"email": "pseudonym@example.test", "email_verified": "true"}},
	}
}

func TestGoogleDecisionAndPrivacy(t *testing.T) {
	for _, tc := range []struct {
		name          string
		failure, want error
	}{
		{"allowed", nil, nil}, {"denied", domain.ErrForbidden, presignup.ErrDenied},
		{"unavailable", errors.New("sensitive SQL pseudonym@example.test TOKEN"), presignup.ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			check := &checker{err: tc.failure}
			handler, err := presignup.New(check, slog.New(slog.NewJSONHandler(&output, nil)), false)
			if err != nil {
				t.Fatal(err)
			}
			ctx := lambdacontext.NewContext(t.Context(), &lambdacontext.LambdaContext{AwsRequestID: "synthetic-request"})
			event := googleEvent()
			event.Response.AutoVerifyEmail = true
			result, err := handler.Handle(ctx, event)
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if result.UserPoolID != event.UserPoolID || result.Response.AutoVerifyEmail || result.Response.AutoVerifyPhone || result.Response.AutoConfirmUser {
				t.Fatal("event or verification flags are unsafe")
			}
			if check.remaining <= 0 || check.remaining > presignup.RequestBudget {
				t.Fatal("query not bounded by trigger budget")
			}
			assertSafeTelemetry(t, output.String(), err)
		})
	}
}

func assertSafeTelemetry(t *testing.T, output string, err error) {
	t.Helper()
	for _, private := range []string{"pseudonym@example.test", "SQL", "TOKEN", "Google_synthetic"} {
		if strings.Contains(output, private) || (err != nil && strings.Contains(err.Error(), private)) {
			t.Fatal("sensitive value leaked")
		}
	}
	if !strings.Contains(output, "synthetic-request") || !strings.Contains(output, "duration_ms") {
		t.Fatal("missing decision telemetry")
	}
}

func TestUnknownOrUnverifiedEventsNeverReachDatabase(t *testing.T) {
	for _, tc := range []struct{ source, username, verified string }{
		{"PreSignUp_SignUp", "Google_synthetic", "true"},
		{"PreSignUp_AdminCreateUser", "real-user", "true"},
		{"PreSignUp_ExternalProvider", "Other_synthetic", "true"},
		{"PreSignUp_ExternalProvider", "Google_synthetic", "false"},
		{"PreSignUp_ExternalProvider", "Google_synthetic", ""},
	} {
		event := googleEvent()
		event.TriggerSource, event.UserName = tc.source, tc.username
		event.Request.UserAttributes["email_verified"] = tc.verified
		check := &checker{}
		handler, _ := presignup.New(check, slog.Default(), true)
		if _, err := handler.Handle(t.Context(), event); !errors.Is(err, presignup.ErrDenied) || check.calls != 0 {
			t.Fatalf("unsupported event admitted: %+v", tc)
		}
	}
}

func TestSyntheticAdminUsersAreDevOnlyAndCannotBypassGoogle(t *testing.T) {
	for _, dev := range []bool{false, true} {
		for _, username := range []string{"test-invited", "test-uninvited", "test-supervisor"} {
			event := googleEvent()
			event.TriggerSource, event.UserName = "PreSignUp_AdminCreateUser", username
			event.Request.UserAttributes["email"] = username + "@example.invalid"
			check := &checker{err: domain.ErrForbidden}
			handler, _ := presignup.New(check, slog.Default(), dev)
			_, err := handler.Handle(t.Context(), event)
			if (err == nil) != dev || check.calls != 0 {
				t.Fatal("dev admin exception incorrectly scoped")
			}
			event.TriggerSource = "PreSignUp_ExternalProvider"
			event.UserName = "Google_" + username
			if _, err := handler.Handle(t.Context(), event); !errors.Is(err, presignup.ErrDenied) || check.calls != 1 {
				t.Fatal("synthetic email bypassed federated allowlist")
			}
			event.TriggerSource, event.UserName = "PreSignUp_AdminCreateUser", username
			event.Request.UserAttributes["email"] = "real-user@example.test"
			if _, err := handler.Handle(t.Context(), event); !errors.Is(err, presignup.ErrDenied) {
				t.Fatal("real email used dev exception")
			}
		}
	}
}

func TestCanceledRequestFailsClosed(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	handler, _ := presignup.New(&checker{}, slog.Default(), false)
	if _, err := handler.Handle(ctx, googleEvent()); !errors.Is(err, presignup.ErrUnavailable) {
		t.Fatal("canceled request admitted")
	}
}
