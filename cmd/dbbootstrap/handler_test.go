package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/db"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/rolebootstrap"
)

const awkwardPassword = `p@ss:w/rd?#% x'"\;&=+`

func request() rolebootstrap.Request {
	return rolebootstrap.Request{Host: "db.example.internal", Port: 5432, Database: "therapy_service_hours", Username: "dbadmin", Password: awkwardPassword}
}

func newHandler(logs *bytes.Buffer, fn rolesFunc) *handler {
	return &handler{logger: slog.New(slog.NewJSONHandler(logs, nil)), ensureRoles: fn}
}

func TestHandle_ConnectsOverVerifiedTLSWithTheExactPassword(t *testing.T) {
	t.Parallel()
	var got db.Settings
	var logs bytes.Buffer
	h := newHandler(&logs, func(_ context.Context, s db.Settings) error { got = s; return nil })

	resp, err := h.handle(t.Context(), request())
	if err != nil || resp.Status != rolebootstrap.StatusOK || strings.Join(resp.Roles, ",") != "app_rw,migrator" {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
	want := db.Settings{Host: "db.example.internal", Port: 5432, Database: "therapy_service_hours", User: "dbadmin",
		Mode: db.AuthPassword, Password: awkwardPassword, VerifyTLS: true}
	if got != want {
		t.Errorf("settings differ (password intact: %v)", got.Password == awkwardPassword)
	}
	if strings.Contains(logs.String(), "p@ss") {
		t.Errorf("the password reached the log: %s", logs.String())
	}
}

func TestHandle_FailuresAreReturnedWithoutThePassword(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	h := newHandler(&logs, func(context.Context, db.Settings) error { return errors.New("failed SASL auth") })
	_, err := h.handle(t.Context(), request())
	if err == nil || strings.Contains(err.Error(), "p@ss") || strings.Contains(logs.String(), "p@ss") {
		t.Errorf("err=%v logs=%s", err, logs.String())
	}
}

func TestHandle_RejectsAnIncompleteRequestWithoutConnecting(t *testing.T) {
	t.Parallel()
	called := false
	h := newHandler(&bytes.Buffer{}, func(context.Context, db.Settings) error { called = true; return nil })
	if _, err := h.handle(t.Context(), rolebootstrap.Request{Host: "h"}); err == nil || called {
		t.Errorf("err=%v connected=%v", err, called)
	}
}
