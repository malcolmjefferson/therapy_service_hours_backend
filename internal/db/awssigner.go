package db

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/rds/auth"
)

// AWSSigner signs RDS IAM tokens with the Lambda role's credentials.
type AWSSigner struct {
	Credentials aws.CredentialsProvider
}

// Sign builds a presigned RDS auth token locally (no network call).
func (s AWSSigner) Sign(ctx context.Context, endpoint, region, user string) (string, error) {
	token, err := auth.BuildAuthToken(ctx, endpoint, region, user, s.Credentials)
	if err != nil {
		return "", fmt.Errorf("build auth token: %w", err)
	}
	return token, nil
}
