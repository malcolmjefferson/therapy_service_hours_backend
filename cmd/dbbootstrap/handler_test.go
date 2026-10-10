package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/aws/aws-lambda-go/cfn"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/db"
)

const secretPassword = "fixture-master-password-do-not-log"

func props() map[string]any {
	return map[string]any{
		"ServiceToken": "arn:aws:lambda:us-west-2:123456789012:function:fixture",
		"Host":         "db.example.internal", "Port": "5432", "Database": "therapy_service_hours",
		"MasterUsername": "dbadmin", "MasterPassword": secretPassword,
	}
}

func newHandler(logs *bytes.Buffer, fn rolesFunc) *handler {
	return &handler{logger: slog.New(slog.NewJSONHandler(logs, nil)), ensureRoles: fn}
}

func TestHandle_CreateAndUpdateEnsureRolesOverVerifiedTLS(t *testing.T) {
	t.Parallel()
	for _, rt := range []cfn.RequestType{cfn.RequestCreate, cfn.RequestUpdate} {
		var got db.Settings
		var logs bytes.Buffer
		h := newHandler(&logs, func(_ context.Context, s db.Settings) error { got = s; return nil })

		id, data, err := h.handle(t.Context(), cfn.Event{RequestType: rt, ResourceProperties: props()})
		if err != nil || id != physicalID || data["Roles"] != "app_rw,migrator" {
			t.Fatalf("%s: id=%q data=%v err=%v", rt, id, data, err)
		}
		want := db.Settings{Host: "db.example.internal", Port: 5432, Database: "therapy_service_hours", User: "dbadmin",
			Mode: db.AuthPassword, Password: secretPassword, VerifyTLS: true}
		if got != want {
			t.Errorf("%s: settings = %+v", rt, got)
		}
		if strings.Contains(logs.String(), secretPassword) {
			t.Errorf("%s: the master password reached the log", rt)
		}
	}
}

func TestHandle_DeleteKeepsTheRolesAndDoesNotConnect(t *testing.T) {
	t.Parallel()
	called := false
	h := newHandler(&bytes.Buffer{}, func(context.Context, db.Settings) error { called = true; return nil })
	id, _, err := h.handle(t.Context(), cfn.Event{RequestType: cfn.RequestDelete, ResourceProperties: map[string]any{}})
	if err != nil || id != physicalID || called {
		t.Errorf("delete: id=%q err=%v connected=%v", id, err, called)
	}
}

func TestHandle_FailuresAreReportedWithoutThePassword(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	h := newHandler(&logs, func(context.Context, db.Settings) error { return errors.New("connection refused") })
	_, _, err := h.handle(t.Context(), cfn.Event{RequestType: cfn.RequestCreate, ResourceProperties: props()})
	if err == nil || strings.Contains(err.Error(), secretPassword) || strings.Contains(logs.String(), secretPassword) {
		t.Errorf("err=%v logs=%s", err, logs.String())
	}
}

func TestSettingsFrom_RequiresEveryProperty(t *testing.T) {
	t.Parallel()
	_, err := settingsFrom(map[string]any{"Port": "x"})
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, key := range []string{"Host", "Database", "MasterUsername", "MasterPassword"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("error %q does not mention %s", err, key)
		}
	}
	p := props()
	p["Port"] = "not-a-number"
	if _, err := settingsFrom(p); err == nil || !strings.Contains(err.Error(), "Port") {
		t.Errorf("bad port: %v", err)
	}
}
