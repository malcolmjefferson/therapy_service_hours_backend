// Package testpg starts one PostgreSQL container per test package and hands each test its own
// database, so integration tests are isolated (GO-72, GO-75). Tests skip when Docker is not
// available locally; CI always has Docker.
package testpg

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// Image is the PostgreSQL version the tests run against; it matches the RDS major version.
const Image = "postgres:17-alpine"

// Server is a running container.
type Server struct {
	Host, User, Password string
	Port                 int
	skip                 string
	terminate            func()
}

// Main runs the package's tests with a container, for use from TestMain:
//
//	func TestMain(m *testing.M) { os.Exit(testpg.Main(m, &server)) }
func Main(m *testing.M, out **Server) int {
	srv, err := start(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "start postgres container: %v\n", err)
		return 1
	}
	*out = srv
	defer srv.terminate()
	return m.Run()
}

func start(ctx context.Context) (*Server, error) {
	if os.Getenv("CI") == "" && !dockerAvailable(ctx) {
		return &Server{skip: "Docker is not available; integration tests run in CI", terminate: func() {}}, nil
	}
	pg, err := postgres.Run(ctx, Image,
		postgres.WithDatabase("postgres_test"),
		postgres.WithUsername("app_test"),
		postgres.WithPassword("local-test-password"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		return nil, fmt.Errorf("run container: %w", err)
	}
	host, err := pg.Host(ctx)
	if err != nil {
		return nil, fmt.Errorf("container host: %w", err)
	}
	port, err := pg.MappedPort(ctx, "5432/tcp")
	if err != nil {
		return nil, fmt.Errorf("container port: %w", err)
	}
	return &Server{
		Host: host, Port: int(port.Num()), User: "app_test", Password: "local-test-password",
		terminate: func() { _ = testcontainers.TerminateContainer(pg) },
	}, nil
}

func dockerAvailable(ctx context.Context) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	provider, err := testcontainers.NewDockerProvider()
	if err != nil {
		return false
	}
	defer func() { _ = provider.Close() }()
	return provider.Health(ctx) == nil
}

// NewDatabase creates an empty database for one test and drops it afterwards. It skips the test
// when Docker is unavailable.
func (s *Server) NewDatabase(t *testing.T) string {
	t.Helper()
	if s.skip != "" {
		t.Skip(s.skip)
	}
	var b [6]byte
	_, _ = rand.Read(b[:])
	name := "t_" + hex.EncodeToString(b[:])

	admin := s.connect(t, "postgres_test")
	// Identifiers cannot be bound parameters; name is generated above from hex only.
	if _, err := admin.Exec(t.Context(), "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.WithoutCancel(t.Context())
		_, _ = admin.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		_ = admin.Close(ctx)
	})
	return name
}

// Connect opens a single connection to database for test assertions.
func (s *Server) Connect(t *testing.T, database string) *pgx.Conn {
	t.Helper()
	conn := s.connect(t, database)
	t.Cleanup(func() { _ = conn.Close(context.WithoutCancel(t.Context())) })
	return conn
}

func (s *Server) connect(t *testing.T, database string) *pgx.Conn {
	t.Helper()
	cfg, err := pgx.ParseConfig("")
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	cfg.Host, cfg.Port, cfg.Database, cfg.User, cfg.Password = s.Host, uint16(s.Port), database, s.User, s.Password //nolint:gosec // G115: port from Docker.
	cfg.TLSConfig, cfg.Fallbacks = nil, nil
	conn, err := pgx.ConnectConfig(t.Context(), cfg)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	return conn
}
