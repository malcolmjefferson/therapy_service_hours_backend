package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/aws/aws-lambda-go/cfn"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/db"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/dbroles"
)

// physicalID is stable, so updates never make CloudFormation think the resource was replaced.
const physicalID = "therapy-service-hours-db-roles"

// rolesFunc creates the roles on the database described by s. Injected for tests.
type rolesFunc func(ctx context.Context, s db.Settings) error

// handler serves the custom resource events. It never logs ResourceProperties, which carry the
// master password resolved by CloudFormation (SEC-50).
type handler struct {
	logger      *slog.Logger
	ensureRoles rolesFunc
}

func (h *handler) handle(ctx context.Context, event cfn.Event) (string, map[string]any, error) {
	h.logger.Info("db role bootstrap", slog.String("request_type", string(event.RequestType)), slog.String("logical_id", event.LogicalResourceID))

	// Roles are never dropped: deleting the stack must not remove access to retained data.
	if event.RequestType == cfn.RequestDelete {
		return physicalID, nil, nil
	}
	settings, err := settingsFrom(event.ResourceProperties)
	if err != nil {
		return physicalID, nil, err
	}
	if err := h.ensureRoles(ctx, settings); err != nil {
		h.logger.Error("db role bootstrap failed", slog.String("error", err.Error()))
		return physicalID, nil, err
	}
	return physicalID, map[string]any{"Roles": dbroles.AppRole + "," + dbroles.MigratorRole}, nil
}

// settingsFrom reads the connection from the resource properties. The master password arrives
// through a CloudFormation dynamic reference (ADR-0001); TLS is always verified.
func settingsFrom(props map[string]any) (db.Settings, error) {
	str := func(key string) (string, error) {
		v, ok := props[key].(string)
		if !ok || v == "" {
			return "", fmt.Errorf("resource property %s is required", key)
		}
		return v, nil
	}
	var errs []error
	host, err := str("Host")
	errs = append(errs, err)
	portStr, err := str("Port")
	errs = append(errs, err)
	database, err := str("Database")
	errs = append(errs, err)
	user, err := str("MasterUsername")
	errs = append(errs, err)
	password, err := str("MasterPassword")
	errs = append(errs, err)
	if err := errors.Join(errs...); err != nil {
		return db.Settings{}, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return db.Settings{}, fmt.Errorf("resource property Port must be a number: %w", err)
	}
	return db.Settings{
		Host: host, Port: port, Database: database, User: user,
		Mode: db.AuthPassword, Password: password, VerifyTLS: true,
	}, nil
}
