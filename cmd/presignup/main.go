// Command presignup is the isolated-VPC Cognito invitation gate (ADR-0007).
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/config"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/db"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/presignup"
	service "github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/presignup"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With(slog.String("function", "presignup"))
	if err := run(logger); err != nil {
		// Startup configuration/connection failures must never print credentials or database details.
		logger.Error("pre-sign-up initialization failed", slog.String("code", "SIGN_UP_UNAVAILABLE"))
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.LoadDatabase(os.LookupEnv)
	if err != nil {
		return err
	}
	if cfg.AuthMode != "iam" || cfg.User != "presignup_ro" {
		return fmt.Errorf("pre-sign-up requires presignup_ro IAM auth")
	}
	env := os.Getenv("APP_ENV")
	if env != "dev" && env != "prod" {
		return fmt.Errorf("APP_ENV must be dev or prod")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.Region))
	if err != nil {
		return fmt.Errorf("load AWS configuration: %w", err)
	}
	settings := db.Settings{Host: cfg.Host, Port: cfg.Port, Database: cfg.Name, User: cfg.User, Region: cfg.Region, Mode: db.AuthIAM}
	tokens, err := db.NewTokenSource(db.AWSSigner{Credentials: awsCfg.Credentials}, time.Now, settings)
	if err != nil {
		return err
	}
	pool, err := db.OpenPreSignUp(ctx, settings, tokens)
	if err != nil {
		return err
	}
	defer pool.Close()
	svc, err := service.New(db.NewPreSignUpReader(pool), time.Now)
	if err != nil {
		return err
	}
	handler, err := presignup.New(svc, logger, env == "dev")
	if err != nil {
		return err
	}
	lambda.Start(handler.Handle)
	return nil
}
