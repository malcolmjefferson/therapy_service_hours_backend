// Package db owns the PostgreSQL connection: a small pgx pool that authenticates with short-lived
// RDS IAM tokens over verified TLS in AWS (ADR-0001), or with a password against a local container.
// sqlc-generated queries and store adapters are added here by later tickets (CLAUDE.md §3.1).
package db

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	_ "embed"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MaxConns is the pool size per Lambda instance. Raising it needs a check of the db.t4g.micro
// connection budget against the API Lambda's reserved concurrency (GO-40, ADR-0001).
const MaxConns = 2

// Connect timeouts per new connection. RDS checks an IAM token through its auth proxy, which took
// more than 5 s on the first connection to a fresh dev instance (2026-10-11): the token was
// accepted, but after the client had given up. Password connections answer at once.
const (
	PasswordConnectTimeout = 5 * time.Second
	IAMConnectTimeout      = 12 * time.Second
)

//go:embed certs/us-west-2-bundle.pem
var rdsCABundle []byte

// AuthMode selects how the pool authenticates.
type AuthMode string

// Supported authentication modes.
const (
	// AuthIAM signs an RDS IAM token per new connection and requires verified TLS. Used in AWS.
	AuthIAM AuthMode = "iam"
	// AuthPassword uses a static password. Without VerifyTLS it is only for local containers and
	// tests; the role bootstrap uses it with VerifyTLS for the one-time master-user connection.
	AuthPassword AuthMode = "password"
)

// Settings describe one database connection.
type Settings struct {
	Host     string
	Port     int
	Database string
	User     string
	Mode     AuthMode
	// Password is used only in AuthPassword mode.
	Password string
	// Region is the AWS region the IAM token is signed for. Used only in AuthIAM mode.
	Region string
	// VerifyTLS requires verify-full TLS against the RDS CA bundle in AuthPassword mode.
	// AuthIAM always verifies.
	VerifyTLS bool
}

// Endpoint returns host:port, the form RDS IAM tokens are signed for.
func (s Settings) Endpoint() string {
	return net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
}

func (s Settings) validate() error {
	var errs []error
	if s.Host == "" {
		errs = append(errs, errors.New("host is required"))
	}
	if s.Port <= 0 || s.Port > 65535 {
		errs = append(errs, fmt.Errorf("port %d is out of range", s.Port))
	}
	if s.Database == "" {
		errs = append(errs, errors.New("database is required"))
	}
	if s.User == "" {
		errs = append(errs, errors.New("user is required"))
	}
	switch s.Mode {
	case AuthIAM:
		if s.Region == "" {
			errs = append(errs, errors.New("region is required for IAM auth"))
		}
	case AuthPassword:
		if s.Password == "" {
			errs = append(errs, errors.New("password is required for password auth"))
		}
	default:
		errs = append(errs, fmt.Errorf("auth mode %q is not iam or password", s.Mode))
	}
	return errors.Join(errs...)
}

// Open creates the pool and checks that a connection works.
// In AuthIAM mode, tokens supplies the password for each new connection.
// Every connection failure is returned as a *ConnectError, which matches ErrUnavailable.
func Open(ctx context.Context, s Settings, tokens *TokenSource) (*pgxpool.Pool, error) {
	return open(ctx, s, tokens, poolConfig)
}

// PreSignUpConnectTimeout leaves room for the query and response inside Cognito's five seconds.
const PreSignUpConnectTimeout = 3 * time.Second

// OpenPreSignUp warms one IAM-authenticated connection during provisioned-concurrency init.
// It never changes the API's connection timeout or connection budget (ADR-0007).
func OpenPreSignUp(ctx context.Context, s Settings, tokens *TokenSource) (*pgxpool.Pool, error) {
	return open(ctx, s, tokens, preSignUpPoolConfig)
}

func preSignUpPoolConfig(s Settings, tokens *TokenSource) (*pgxpool.Config, error) {
	cfg, err := poolConfig(s, tokens)
	if err != nil {
		return nil, err
	}
	cfg.ConnConfig.ConnectTimeout = PreSignUpConnectTimeout
	cfg.MaxConns, cfg.MinConns = MaxConns, 1
	return cfg, nil
}

func open(ctx context.Context, s Settings, tokens *TokenSource, configure func(Settings, *TokenSource) (*pgxpool.Config, error)) (*pgxpool.Pool, error) {
	if err := s.validate(); err != nil {
		return nil, fmt.Errorf("database settings: %w", err)
	}
	if s.Mode == AuthIAM && tokens == nil {
		return nil, errors.New("database settings: IAM auth needs a token source")
	}
	cfg, err := configure(s, tokens)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, &ConnectError{Endpoint: s.Endpoint(), Err: err}
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, &ConnectError{Endpoint: s.Endpoint(), Err: err}
	}
	return pool, nil
}

func poolConfig(s Settings, tokens *TokenSource) (*pgxpool.Config, error) {
	// Built from a minimal DSN so no value is ever interpolated into a connection string.
	cfg, err := pgxpool.ParseConfig("")
	if err != nil {
		return nil, fmt.Errorf("parse pool config: %w", err)
	}
	cc := cfg.ConnConfig
	cc.Host = s.Host
	cc.Port = uint16(s.Port) //nolint:gosec // G115: validate() bounds the port to 1..65535.
	cc.Database = s.Database
	cc.User = s.User
	cc.ConnectTimeout = PasswordConnectTimeout
	cc.Fallbacks = nil
	cc.RuntimeParams = map[string]string{"application_name": "therapy-service-hours"}

	cfg.MaxConns = MaxConns
	cfg.MinConns = 0
	cfg.MaxConnIdleTime = 5 * time.Minute
	// Under the 15-minute IAM token lifetime, so a connection never outlives its credential by long.
	cfg.MaxConnLifetime = 14 * time.Minute

	switch s.Mode {
	case AuthIAM:
		tlsCfg, err := verifiedTLS(s.Host)
		if err != nil {
			return nil, err
		}
		cc.TLSConfig = tlsCfg
		cc.ConnectTimeout = IAMConnectTimeout
		cfg.BeforeConnect = func(ctx context.Context, c *pgx.ConnConfig) error {
			token, err := tokens.Token(ctx)
			if err != nil {
				return err
			}
			c.Password = token
			return nil
		}
	case AuthPassword:
		cc.TLSConfig = nil
		if s.VerifyTLS {
			tlsCfg, err := verifiedTLS(s.Host)
			if err != nil {
				return nil, err
			}
			cc.TLSConfig = tlsCfg
		}
		cc.Password = s.Password
	}
	return cfg, nil
}

// verifiedTLS is the equivalent of sslmode=verify-full against the embedded RDS CA bundle (SEC-43).
func verifiedTLS(host string) (*tls.Config, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(rdsCABundle) {
		return nil, errors.New("embedded RDS CA bundle has no certificates")
	}
	return &tls.Config{
		RootCAs:    pool,
		ServerName: host,
		MinVersion: tls.VersionTLS12,
	}, nil
}
