package config_test

import (
	"strings"
	"testing"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/config"
)

func TestLoadDatabase_IAMDefaults(t *testing.T) {
	t.Parallel()
	cfg, err := config.LoadDatabase(lookupFrom(map[string]string{
		"DB_HOST": "db.internal", "DB_NAME": "therapy_service_hours", "DB_USER": "migrator", "AWS_REGION": "us-west-2",
	}))
	if err != nil {
		t.Fatalf("LoadDatabase: %v", err)
	}
	want := config.Database{Host: "db.internal", Port: 5432, Name: "therapy_service_hours", User: "migrator", Region: "us-west-2", AuthMode: "iam"}
	if cfg != want {
		t.Errorf("got %+v, want %+v", cfg, want)
	}
}

func TestLoadDatabase_PasswordModeForLocalUse(t *testing.T) {
	t.Parallel()
	cfg, err := config.LoadDatabase(lookupFrom(map[string]string{
		"DB_HOST": "localhost", "DB_PORT": "15432", "DB_NAME": "app", "DB_USER": "app_test", "DB_AUTH": "password", "DB_PASSWORD": "local",
	}))
	if err != nil || cfg.Port != 15432 || cfg.Password != "local" || cfg.Region != "" {
		t.Errorf("got %+v, %v", cfg, err)
	}
}

func TestLoadDatabase_ReportsEveryProblem(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		env  map[string]string
		want []string
	}{
		{"nothing set", map[string]string{}, []string{"DB_HOST", "DB_NAME", "DB_USER", "AWS_REGION"}},
		{"bad port and auth", map[string]string{"DB_HOST": "h", "DB_NAME": "n", "DB_USER": "u", "DB_PORT": "99999", "DB_AUTH": "trust"}, []string{"DB_PORT", "DB_AUTH"}},
		{"password mode without password", map[string]string{"DB_HOST": "h", "DB_NAME": "n", "DB_USER": "u", "DB_AUTH": "password"}, []string{"DB_PASSWORD"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := config.LoadDatabase(lookupFrom(tt.env))
			if err == nil {
				t.Fatal("expected an error")
			}
			for _, w := range tt.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not mention %s", err, w)
				}
			}
		})
	}
}
