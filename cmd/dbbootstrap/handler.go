package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/db"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/dbroles"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/rolebootstrap"
)

// rolesFunc creates the roles on the database described by s. Injected for tests.
type rolesFunc func(ctx context.Context, s db.Settings) error

// handler serves direct invocations from the provider. It logs only the endpoint and outcome.
type handler struct {
	logger      *slog.Logger
	ensureRoles rolesFunc
}

func (h *handler) handle(ctx context.Context, req rolebootstrap.Request) (rolebootstrap.Response, error) {
	h.logger.Info("db role bootstrap", slog.Any("request", req)) // LogValue omits the password.
	if err := req.Validate(); err != nil {
		// An empty request usually means CloudFormation invoked the worker directly: the custom
		// resource must point at dbbootstrap-provider instead.
		return rolebootstrap.Response{}, fmt.Errorf("%w (the worker must be invoked by dbbootstrap-provider, not by CloudFormation)", err)
	}
	settings := db.Settings{
		Host: req.Host, Port: req.Port, Database: req.Database, User: req.Username,
		Mode: db.AuthPassword, Password: req.Password, VerifyTLS: true,
	}
	if err := h.ensureRoles(ctx, settings); err != nil {
		h.logger.Error("db role bootstrap failed", slog.String("error", err.Error()))
		return rolebootstrap.Response{}, err
	}
	h.logger.Info("db role bootstrap finished")
	return rolebootstrap.Response{Status: rolebootstrap.StatusOK, Roles: []string{dbroles.AppRole, dbroles.MigratorRole}}, nil
}
