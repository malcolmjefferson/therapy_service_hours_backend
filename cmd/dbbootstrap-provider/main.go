// Command dbbootstrap-provider is the deploy-time custom resource handler for the database roles
// (outside the VPC, ADR-0001 as amended 2026-10-10). It reads only the RDS-managed master secret
// and invokes the dbbootstrap worker in the VPC. It runs only during CloudFormation deploys.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/aws/aws-lambda-go/cfn"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	lambdasvc "github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/rolebootstrap"
)

// physicalID is stable, so updates never look like a replacement.
const physicalID = "therapy-service-hours-db-roles"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With(slog.String("function", "dbbootstrap-provider"))
	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background())
	if err != nil {
		logger.Error("load AWS config", slog.String("error", err.Error()))
		os.Exit(1)
	}
	provider, err := rolebootstrap.NewProvider(
		secretReader{client: secretsmanager.NewFromConfig(awsCfg)},
		workerInvoker{client: lambdasvc.NewFromConfig(awsCfg)},
	)
	if err != nil {
		logger.Error("build provider", slog.String("error", err.Error()))
		os.Exit(1)
	}
	h := &handler{logger: logger, provider: provider}
	lambda.Start(cfn.LambdaWrap(h.handle))
}

type handler struct {
	logger   *slog.Logger
	provider *rolebootstrap.Provider
}

func (h *handler) handle(ctx context.Context, event cfn.Event) (string, map[string]any, error) {
	h.logger.Info("db role bootstrap provider", slog.String("request_type", string(event.RequestType)), slog.String("logical_id", event.LogicalResourceID))
	// Roles are never dropped: deleting the stack must not remove access to retained data.
	if event.RequestType == cfn.RequestDelete {
		return physicalID, nil, nil
	}
	props, err := rolebootstrap.PropertiesFrom(event.ResourceProperties)
	if err != nil {
		return physicalID, nil, err //nolint:wrapcheck // The message lists the missing properties.
	}
	// Stop waiting for the worker before this Lambda times out, so CloudFormation always gets a response.
	ensureCtx, cancel := rolebootstrap.WithResponseMargin(ctx, rolebootstrap.ResponseMargin)
	defer cancel()
	roles, err := h.provider.Ensure(ensureCtx, props)
	if err != nil {
		h.logger.Error("db role bootstrap provider failed", slog.String("error", err.Error()))
		return physicalID, nil, err //nolint:wrapcheck // Ensure wraps with context.
	}
	return physicalID, map[string]any{"Roles": strings.Join(roles, ",")}, nil
}

type secretReader struct{ client *secretsmanager.Client }

func (r secretReader) SecretString(ctx context.Context, arn string) (string, error) {
	out, err := r.client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: aws.String(arn)})
	if err != nil {
		return "", fmt.Errorf("get secret value: %w", err)
	}
	if out.SecretString == nil {
		return "", errors.New("secret has no string value")
	}
	return *out.SecretString, nil
}

type workerInvoker struct{ client *lambdasvc.Client }

func (w workerInvoker) Invoke(ctx context.Context, functionName string, payload []byte) ([]byte, error) {
	out, err := w.client.Invoke(ctx, &lambdasvc.InvokeInput{FunctionName: aws.String(functionName), Payload: payload})
	if err != nil {
		return nil, fmt.Errorf("lambda invoke: %w", err)
	}
	if out.FunctionError != nil {
		// The worker's error payload holds its error message only (never the request).
		return nil, fmt.Errorf("worker failed (%s): %s", *out.FunctionError, string(out.Payload))
	}
	return out.Payload, nil
}
