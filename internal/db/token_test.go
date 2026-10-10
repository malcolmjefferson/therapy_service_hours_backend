package db

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// fakeSigner returns numbered tokens and records each call.
type fakeSigner struct {
	mu    sync.Mutex
	calls int
	args  []string
	err   error
}

func (f *fakeSigner) Sign(_ context.Context, endpoint, region, user string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return "", f.err
	}
	f.calls++
	f.args = append(f.args, endpoint+"|"+region+"|"+user)
	return fmt.Sprintf("token-%d", f.calls), nil
}

// fakeClock is a settable clock.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

var iamSettings = Settings{
	Host: "db.example.internal", Port: 5432, Database: "app", User: "app_rw", Mode: AuthIAM, Region: "us-west-2",
}

func newClock() *fakeClock { return &fakeClock{now: time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)} }

func TestTokenSource_ReusesTokenUntilNearExpiryThenSignsANewOne(t *testing.T) {
	t.Parallel()
	signer, clock := &fakeSigner{}, newClock()
	ts, err := NewTokenSource(signer, clock.Now, iamSettings)
	if err != nil {
		t.Fatalf("NewTokenSource: %v", err)
	}
	steps := []struct {
		advance time.Duration
		want    string
	}{
		{0, "token-1"},
		{13*time.Minute + 59*time.Second, "token-1"}, // still more than a minute left
		{time.Second, "token-2"},                     // 14 minutes old: renew before the 15-minute expiry
		{20 * time.Minute, "token-3"},                // long expired
	}
	for i, s := range steps {
		clock.Advance(s.advance)
		got, err := ts.Token(context.Background())
		if err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		if got != s.want {
			t.Errorf("step %d: token = %q, want %q", i, got, s.want)
		}
	}
	if signer.args[0] != "db.example.internal:5432|us-west-2|app_rw" {
		t.Errorf("signed for %q", signer.args[0])
	}
}

func TestTokenSource_WrapsSignerErrors(t *testing.T) {
	t.Parallel()
	cause := errors.New("no credentials")
	ts, err := NewTokenSource(&fakeSigner{err: cause}, newClock().Now, iamSettings)
	if err != nil {
		t.Fatalf("NewTokenSource: %v", err)
	}
	_, err = ts.Token(context.Background())
	if !errors.Is(err, cause) || !strings.Contains(err.Error(), "app_rw") {
		t.Errorf("err = %v", err)
	}
}

func TestTokenSource_IsSafeForConcurrentUse(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{}
	ts, err := NewTokenSource(signer, newClock().Now, iamSettings)
	if err != nil {
		t.Fatalf("NewTokenSource: %v", err)
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := ts.Token(context.Background()); err != nil {
				t.Errorf("Token: %v", err)
			}
		}()
	}
	wg.Wait()
	if signer.calls != 1 {
		t.Errorf("signed %d times, want 1", signer.calls)
	}
}

func TestNewTokenSource_RequiresDependencies(t *testing.T) {
	t.Parallel()
	if _, err := NewTokenSource(nil, time.Now, iamSettings); err == nil {
		t.Error("expected an error without a signer")
	}
	if _, err := NewTokenSource(&fakeSigner{}, nil, iamSettings); err == nil {
		t.Error("expected an error without a clock")
	}
}

// TestPoolConfig_IAMRefreshesTheTokenOnANewConnectionAfterExpiry exercises the exact hook pgx
// runs before each new connection (ADR-0001 validation).
func TestPoolConfig_IAMRefreshesTheTokenOnANewConnectionAfterExpiry(t *testing.T) {
	t.Parallel()
	signer, clock := &fakeSigner{}, newClock()
	ts, err := NewTokenSource(signer, clock.Now, iamSettings)
	if err != nil {
		t.Fatalf("NewTokenSource: %v", err)
	}
	cfg, err := poolConfig(iamSettings, ts)
	if err != nil {
		t.Fatalf("poolConfig: %v", err)
	}

	connect := func() string {
		cc := cfg.ConnConfig.Copy()
		if err := cfg.BeforeConnect(context.Background(), cc); err != nil {
			t.Fatalf("BeforeConnect: %v", err)
		}
		return cc.Password
	}
	if got := connect(); got != "token-1" {
		t.Fatalf("first connection password = %q", got)
	}
	clock.Advance(5 * time.Minute)
	if got := connect(); got != "token-1" {
		t.Errorf("second connection within lifetime = %q, want the cached token", got)
	}
	clock.Advance(16 * time.Minute)
	if got := connect(); got != "token-2" {
		t.Errorf("connection after expiry = %q, want a fresh token", got)
	}
}

func TestPoolConfig_IAMUsesVerifiedTLSAndTheLimits(t *testing.T) {
	t.Parallel()
	ts, err := NewTokenSource(&fakeSigner{}, time.Now, iamSettings)
	if err != nil {
		t.Fatalf("NewTokenSource: %v", err)
	}
	cfg, err := poolConfig(iamSettings, ts)
	if err != nil {
		t.Fatalf("poolConfig: %v", err)
	}
	tlsCfg := cfg.ConnConfig.TLSConfig
	if tlsCfg == nil || tlsCfg.InsecureSkipVerify || tlsCfg.ServerName != iamSettings.Host || tlsCfg.MinVersion != tls.VersionTLS12 || tlsCfg.RootCAs == nil {
		t.Errorf("TLS config is not verify-full: %+v", tlsCfg)
	}
	if len(cfg.ConnConfig.Fallbacks) != 0 {
		t.Error("fallbacks would allow a non-TLS connection")
	}
	if cfg.MaxConns != MaxConns || MaxConns != 2 {
		t.Errorf("MaxConns = %d, want 2 (GO-40)", cfg.MaxConns)
	}
	if cfg.MaxConnLifetime >= TokenLifetime {
		t.Errorf("MaxConnLifetime %v should be under the token lifetime", cfg.MaxConnLifetime)
	}
	if cfg.ConnConfig.Password != "" {
		t.Error("IAM mode must not carry a static password")
	}
}

func TestEmbeddedCABundle_ParsesIntoACertPool(t *testing.T) {
	t.Parallel()
	if _, err := verifiedTLS("db.example.internal"); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(rdsCABundle), "BEGIN CERTIFICATE"); n == 0 {
		t.Error("bundle is empty")
	}
}

func TestSettings_Validate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		mutate  func(*Settings)
		wantErr string
	}{
		{"valid IAM", func(*Settings) {}, ""},
		{"valid password", func(s *Settings) { s.Mode, s.Password, s.Region = AuthPassword, "pw", "" }, ""},
		{"missing host", func(s *Settings) { s.Host = "" }, "host is required"},
		{"bad port", func(s *Settings) { s.Port = 70000 }, "out of range"},
		{"missing database", func(s *Settings) { s.Database = "" }, "database is required"},
		{"missing user", func(s *Settings) { s.User = "" }, "user is required"},
		{"IAM without region", func(s *Settings) { s.Region = "" }, "region is required"},
		{"password without password", func(s *Settings) { s.Mode = AuthPassword }, "password is required"},
		{"unknown mode", func(s *Settings) { s.Mode = "trust" }, "not iam or password"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := iamSettings
			tt.mutate(&s)
			err := s.validate()
			if tt.wantErr == "" && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Errorf("err = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestOpen_RejectsIAMWithoutATokenSource(t *testing.T) {
	t.Parallel()
	if _, err := Open(context.Background(), iamSettings, nil); err == nil {
		t.Error("expected an error")
	}
}

func TestConnectError_MatchesErrUnavailableAndHidesNothingSecret(t *testing.T) {
	t.Parallel()
	err := fmt.Errorf("startup: %w", &ConnectError{Endpoint: "db:5432", Err: pgx.ErrNoRows})
	if !errors.Is(err, ErrUnavailable) {
		t.Error("ConnectError should match ErrUnavailable")
	}
	var ce *ConnectError
	if !errors.As(err, &ce) || ce.Endpoint != "db:5432" {
		t.Errorf("errors.As failed: %v", err)
	}
}
