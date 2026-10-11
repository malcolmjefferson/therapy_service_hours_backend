package db

import (
	"testing"
	"time"
)

func TestPreSignUpPoolHasIndependentTimeoutAndWarmConnection(t *testing.T) {
	settings := Settings{Host: "localhost", Port: 5432, Database: "test", User: "presignup_ro", Mode: AuthPassword, Password: "local-test-password"}
	trigger, err := preSignUpPoolConfig(settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	if trigger.MaxConns != MaxConns || trigger.MinConns != 1 || trigger.ConnConfig.ConnectTimeout != 3*time.Second {
		t.Fatal("trigger connection is not warmed and bounded")
	}
	api, err := poolConfig(settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	if api.MaxConns != MaxConns || api.MinConns != 0 || api.ConnConfig.ConnectTimeout != PasswordConnectTimeout {
		t.Fatal("API pool defaults changed")
	}
	settings.Mode, settings.Region = AuthIAM, "us-west-2"
	trigger, err = preSignUpPoolConfig(settings, nil)
	if err != nil || trigger.ConnConfig.TLSConfig == nil || trigger.ConnConfig.ConnectTimeout >= 5*time.Second {
		t.Fatal("IAM trigger lost TLS or timeout bound")
	}
	api, err = poolConfig(settings, nil)
	if err != nil || api.ConnConfig.ConnectTimeout != IAMConnectTimeout {
		t.Fatal("API IAM timeout changed")
	}
}
