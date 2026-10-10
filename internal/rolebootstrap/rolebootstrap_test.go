package rolebootstrap_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/rolebootstrap"
)

// awkwardPassword contains every character that breaks a DSN or URL if it is ever interpolated.
const awkwardPassword = `p@ss:w/rd?#% x'"\;&=+`

type fakeSecrets struct {
	value string
	err   error
	arn   string
}

func (f *fakeSecrets) SecretString(_ context.Context, arn string) (string, error) {
	f.arn = arn
	return f.value, f.err
}

type fakeWorker struct {
	response []byte
	err      error
	function string
	payload  []byte
}

func (f *fakeWorker) Invoke(_ context.Context, fn string, payload []byte) ([]byte, error) {
	f.function, f.payload = fn, payload
	return f.response, f.err
}

func props() rolebootstrap.Properties {
	return rolebootstrap.Properties{
		MasterSecretArn: "arn:aws:secretsmanager:us-west-2:123456789012:secret:rds!db-fixture",
		WorkerFunction:  "therapy-service-hours-dev-db-bootstrap",
		Host:            "db.example.internal", Port: "5432", Database: "therapy_service_hours",
	}
}

func secretJSON(user, pw string) string {
	b, _ := json.Marshal(map[string]string{"username": user, "password": pw})
	return string(b)
}

func newProvider(t *testing.T, s *fakeSecrets, w *fakeWorker) *rolebootstrap.Provider {
	t.Helper()
	p, err := rolebootstrap.NewProvider(s, w)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	return p
}

func TestEnsure_PassesTheExactPasswordToTheWorker(t *testing.T) {
	t.Parallel()
	secrets := &fakeSecrets{value: secretJSON("dbadmin", awkwardPassword)}
	worker := &fakeWorker{response: []byte(`{"status":"ok","roles":["app_rw","migrator"]}`)}
	roles, err := newProvider(t, secrets, worker).Ensure(t.Context(), props())
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if strings.Join(roles, ",") != "app_rw,migrator" {
		t.Errorf("roles = %v", roles)
	}
	if secrets.arn != props().MasterSecretArn || worker.function != props().WorkerFunction {
		t.Errorf("read %q, invoked %q", secrets.arn, worker.function)
	}
	var sent rolebootstrap.Request
	if err := json.Unmarshal(worker.payload, &sent); err != nil {
		t.Fatalf("payload: %v", err)
	}
	want := rolebootstrap.Request{Host: "db.example.internal", Port: 5432, Database: "therapy_service_hours", Username: "dbadmin", Password: awkwardPassword}
	if sent != want {
		t.Errorf("worker got %+v (password intact: %v)", sent, sent.Password == awkwardPassword)
	}
}

func TestEnsure_FailuresNeverContainThePassword(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		secrets *fakeSecrets
		worker  *fakeWorker
		props   rolebootstrap.Properties
		wantErr string
	}{
		{"secret read fails", &fakeSecrets{err: errors.New("AccessDenied")}, &fakeWorker{}, props(), "read master secret"},
		{"secret is not JSON", &fakeSecrets{value: awkwardPassword}, &fakeWorker{}, props(), "not the expected JSON"},
		{"secret lacks a password", &fakeSecrets{value: secretJSON("dbadmin", "")}, &fakeWorker{}, props(), "no username or password"},
		{"worker fails", &fakeSecrets{value: secretJSON("dbadmin", awkwardPassword)}, &fakeWorker{err: errors.New("worker failed: SASL auth")}, props(), "invoke worker"},
		{"worker reports an error status", &fakeSecrets{value: secretJSON("dbadmin", awkwardPassword)}, &fakeWorker{response: []byte(`{"status":"error"}`)}, props(), `status "error"`},
		{"bad port", &fakeSecrets{value: secretJSON("dbadmin", awkwardPassword)}, &fakeWorker{}, func() rolebootstrap.Properties { p := props(); p.Port = "x"; return p }(), "Port"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := newProvider(t, tt.secrets, tt.worker).Ensure(t.Context(), tt.props)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want %q", err, tt.wantErr)
			}
			if strings.Contains(err.Error(), awkwardPassword) {
				t.Errorf("the password leaked into the error: %v", err)
			}
		})
	}
}

func TestRequest_NeverPrintsThePassword(t *testing.T) {
	t.Parallel()
	req := rolebootstrap.Request{Host: "h", Port: 5432, Database: "d", Username: "u", Password: awkwardPassword}
	var logs bytes.Buffer
	slog.New(slog.NewJSONHandler(&logs, nil)).Info("x", slog.Any("request", req))
	for name, out := range map[string]string{
		"%v": fmt.Sprintf("%v", req), "%+v": fmt.Sprintf("%+v", req), "%#v": fmt.Sprintf("%#v", req),
		"String": req.String(), "slog": logs.String(),
	} {
		if strings.Contains(out, awkwardPassword) || strings.Contains(out, "p@ss") {
			t.Errorf("%s printed the password: %s", name, out)
		}
	}
	if !strings.Contains(logs.String(), `"host":"h"`) {
		t.Errorf("slog lost the useful fields: %s", logs.String())
	}
}

func TestRequest_Validate(t *testing.T) {
	t.Parallel()
	if err := (rolebootstrap.Request{Host: "h", Port: 5432, Database: "d", Username: "u", Password: "p"}).Validate(); err != nil {
		t.Errorf("valid request: %v", err)
	}
	err := (rolebootstrap.Request{Port: 0}).Validate()
	if err == nil || err.Error() != "bootstrap request is missing or has invalid: [database host password port username]" {
		t.Errorf("err = %v", err)
	}
}

func TestPropertiesFrom_ListsEveryMissingProperty(t *testing.T) {
	t.Parallel()
	_, err := rolebootstrap.PropertiesFrom(map[string]any{"Host": "h"})
	if err == nil || err.Error() != "custom resource properties missing: [Database MasterSecretArn Port WorkerFunction]" {
		t.Errorf("err = %v", err)
	}
	p, err := rolebootstrap.PropertiesFrom(map[string]any{
		"ServiceToken": "x", "MasterSecretArn": "a", "WorkerFunction": "w", "Host": "h", "Port": "5432", "Database": "d",
	})
	if err != nil || p.WorkerFunction != "w" {
		t.Errorf("got %+v, %v", p, err)
	}
}

func TestNewProvider_RequiresDependencies(t *testing.T) {
	t.Parallel()
	if _, err := rolebootstrap.NewProvider(nil, &fakeWorker{}); err == nil {
		t.Error("expected an error")
	}
}
