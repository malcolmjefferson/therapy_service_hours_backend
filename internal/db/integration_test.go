package db

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// One Postgres container serves every integration test in this package (GO-75).
// When Docker is not available the integration tests skip; CI always has Docker (GO-72).
var container struct {
	settings Settings
	skip     string
}

func TestMain(m *testing.M) {
	os.Exit(runWithPostgres(m))
}

func runWithPostgres(m *testing.M) int {
	ctx := context.Background()
	if os.Getenv("CI") == "" && !dockerAvailable() {
		container.skip = "Docker is not available; integration tests run in CI"
		return m.Run()
	}
	pg, err := postgres.Run(ctx, "postgres:17-alpine",
		postgres.WithDatabase("app"),
		postgres.WithUsername("app_test"),
		postgres.WithPassword("local-test-password"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "start postgres container: %v\n", err)
		return 1
	}
	defer func() { _ = testcontainers.TerminateContainer(pg) }()

	host, err := pg.Host(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "container host: %v\n", err)
		return 1
	}
	port, err := pg.MappedPort(ctx, "5432/tcp")
	if err != nil {
		fmt.Fprintf(os.Stderr, "container port: %v\n", err)
		return 1
	}
	container.settings = Settings{
		Host: host, Port: int(port.Num()), Database: "app", User: "app_test",
		Mode: AuthPassword, Password: "local-test-password",
	}
	return m.Run()
}

func dockerAvailable() (ok bool) {
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
	return provider.Health(context.Background()) == nil
}

func openTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if container.skip != "" {
		t.Skip(container.skip)
	}
	pool, err := Open(context.Background(), container.settings, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestIntegration_PasswordModeConnectsWithTheConfiguredLimits(t *testing.T) {
	pool := openTestPool(t)
	var one int
	if err := pool.QueryRow(context.Background(), "SELECT 1").Scan(&one); err != nil || one != 1 {
		t.Fatalf("SELECT 1 = %d, %v", one, err)
	}
	if got := pool.Config().MaxConns; got != MaxConns {
		t.Errorf("MaxConns = %d, want %d", got, MaxConns)
	}
}

func TestIntegration_ConnectionFailureIsATypedError(t *testing.T) {
	if container.skip != "" {
		t.Skip(container.skip)
	}
	// A port with nothing listening: reserve one, then close it.
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := int(l.Addr().(*net.TCPAddr).AddrPort().Port())
	_ = l.Close()

	s := container.settings
	s.Host, s.Port = "127.0.0.1", port
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = Open(ctx, s, nil)

	var ce *ConnectError
	if !errors.As(err, &ce) || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want a *ConnectError matching ErrUnavailable", err)
	}
	if ce.Endpoint != s.Endpoint() {
		t.Errorf("endpoint = %q", ce.Endpoint)
	}
}

func TestIntegration_WrongPasswordIsATypedErrorWithoutThePassword(t *testing.T) {
	if container.skip != "" {
		t.Skip(container.skip)
	}
	s := container.settings
	s.Password = "definitely-wrong-password"
	_, err := Open(context.Background(), s, nil)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if strings.Contains(err.Error(), "definitely-wrong-password") {
		t.Error("the password leaked into the error text")
	}
}

func TestIntegration_WithTx(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, "CREATE TABLE IF NOT EXISTS tx_probe (label text PRIMARY KEY)"); err != nil {
		t.Fatalf("create table: %v", err)
	}
	runner, err := NewTxRunner(pool)
	if err != nil {
		t.Fatalf("NewTxRunner: %v", err)
	}
	insert := func(label string) func(pgx.Tx) error {
		return func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, "INSERT INTO tx_probe (label) VALUES ($1)", label)
			return err
		}
	}
	exists := func(label string) bool {
		var n int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM tx_probe WHERE label = $1", label).Scan(&n); err != nil {
			t.Fatalf("count: %v", err)
		}
		return n == 1
	}

	t.Run("commits on success", func(t *testing.T) {
		if err := runner.WithTx(ctx, insert("committed")); err != nil {
			t.Fatalf("WithTx: %v", err)
		}
		if !exists("committed") {
			t.Error("row was not committed")
		}
	})

	t.Run("rolls back on error", func(t *testing.T) {
		boom := errors.New("business rule failed")
		err := runner.WithTx(ctx, func(tx pgx.Tx) error {
			if err := insert("rolled-back")(tx); err != nil {
				return err
			}
			return boom
		})
		if !errors.Is(err, boom) {
			t.Errorf("err = %v, want the callback's error", err)
		}
		if exists("rolled-back") {
			t.Error("row survived a rollback")
		}
	})

	t.Run("rolls back and re-panics on panic", func(t *testing.T) {
		func() {
			defer func() {
				if recover() == nil {
					t.Error("panic was swallowed")
				}
			}()
			_ = runner.WithTx(ctx, func(tx pgx.Tx) error {
				if err := insert("panicked")(tx); err != nil {
					return err
				}
				panic("unexpected")
			})
		}()
		if exists("panicked") {
			t.Error("row survived a panic")
		}
	})
}

// TestIntegration_PasswordWithSpecialCharactersConnects guards against building connection strings:
// the master password RDS generates may contain any of these characters (S0-08 regression).
func TestIntegration_PasswordWithSpecialCharactersConnects(t *testing.T) {
	pool := openTestPool(t)
	ctx := t.Context()
	const role = "special_pw_probe"
	const password = `p@ss:w/rd?#% x'"\;&=+`

	// Role DDL cannot take bound parameters; format() with %I/%L quotes them inside the server.
	var ddl string
	if err := pool.QueryRow(ctx, "SELECT format('CREATE ROLE %I LOGIN PASSWORD %L', $1::text, $2::text)", role, password).Scan(&ddl); err != nil {
		t.Fatalf("format DDL: %v", err)
	}
	if _, err := pool.Exec(ctx, ddl); err != nil {
		t.Fatalf("create role: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.WithoutCancel(ctx), "DROP ROLE IF EXISTS special_pw_probe") })

	s := container.settings
	s.User, s.Password = role, password
	special, err := Open(ctx, s, nil)
	if err != nil {
		t.Fatalf("connect with a special-character password: %v", err)
	}
	defer special.Close()
	var who string
	if err := special.QueryRow(ctx, "SELECT current_user").Scan(&who); err != nil || who != role {
		t.Errorf("current_user = %q, %v", who, err)
	}
}
