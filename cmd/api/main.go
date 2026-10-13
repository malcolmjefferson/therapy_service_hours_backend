// Command api is the main API Lambda (in the VPC). It is the composition root: it reads configuration,
// builds dependencies and starts the handler (GO-03). Setting LOCAL_HTTP_ADDR serves plain HTTP
// for local development instead of the Lambda runtime.
package main

import (
	"errors"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/aws/aws-lambda-go/lambda"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/apigw"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/config"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/db"
	httpapi "github.com/malcolmjefferson/therapy_service_hours_backend/internal/http"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/authn"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/clients"
)

func main() {
	if err := run(); err != nil {
		slog.New(slog.NewJSONHandler(os.Stderr, nil)).Error("api failed to start", slog.String("code", "API_INITIALIZATION_FAILED"))
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.LookupEnv)
	if err != nil {
		return err //nolint:wrapcheck // config.Load already says what is wrong.
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel})).
		With(slog.String("function", cfg.Function))
	database, err := config.LoadDatabase(os.LookupEnv)
	if err != nil {
		return err
	}
	if cfg.LocalHTTPAddr == "" && (database.AuthMode != "iam" || database.User != "app_rw") {
		return errors.New("API requires app_rw IAM authentication")
	}
	store := &lazyDatabase{cfg: database}
	defer store.close()
	identity, err := authn.New(db.NewIdentityStore(store), time.Now)
	if err != nil {
		return err
	}

	clientService, err := clients.New(db.NewClientStore(store))
	if err != nil {
		return err //nolint:wrapcheck // Startup misconfiguration; the message is already specific.
	}

	router, err := httpapi.NewRouter(httpapi.RouterDeps{
		Logger:        logger,
		Version:       cfg.Version,
		MaxBodyBytes:  cfg.MaxBodyBytes,
		NewRequestID:  httpapi.RandomID,
		Now:           time.Now,
		ActorResolver: identity,
		Clients:       clientService,
	})
	if err != nil {
		return err //nolint:wrapcheck // Startup misconfiguration; the message is already specific.
	}

	if cfg.LocalHTTPAddr != "" {
		logger.Info("serving local HTTP", slog.String("addr", cfg.LocalHTTPAddr), slog.String("version", cfg.Version))
		srv := &http.Server{
			Addr:              cfg.LocalHTTPAddr,
			Handler:           router,
			ReadHeaderTimeout: 5 * time.Second,
		}
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err //nolint:wrapcheck // Local development only.
		}
		return nil
	}

	logger.Info("starting Lambda handler", slog.String("version", cfg.Version))
	lambda.Start(apigw.Handler(router, httpapi.ContextWithRequestID))
	return nil
}
