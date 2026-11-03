package migrate_test

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5/stdlib"

	"github.com/malcolmjefferson/therapy_service_hours_backend/db/migrations"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/db"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/migrate"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/testpg"
)

var server *testpg.Server

func TestMain(m *testing.M) { os.Exit(testpg.Main(m, &server)) }

// open returns a database/sql handle on a fresh database, through the production pool code.
func open(t *testing.T, database string) *sql.DB {
	t.Helper()
	pool, err := db.Open(t.Context(), db.Settings{
		Host: server.Host, Port: server.Port, Database: database, User: server.User,
		Mode: db.AuthPassword, Password: server.Password,
	}, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	conn := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = conn.Close(); pool.Close() })
	return conn
}

func TestRun_AppliesTheMigrationsThenIsANoOp(t *testing.T) {
	database := server.NewDatabase(t)
	conn := open(t, database)

	first := migrate.Run(t.Context(), conn, migrations.FS, migrate.Options{})
	if first.Status != migrate.StatusOK || len(first.Applied) != 5 || first.Applied[0] != 1 || first.Applied[1] != 2 || first.Applied[2] != 3 || first.Applied[3] != 4 || first.Applied[4] != 5 || first.Version != 5 {
		t.Fatalf("first run = %+v", first)
	}
	second := migrate.Run(t.Context(), conn, migrations.FS, migrate.Options{})
	if second.Status != migrate.StatusOK || len(second.Applied) != 0 || second.Version != 5 {
		t.Fatalf("second run should be a no-op, got %+v", second)
	}

	check := server.Connect(t, database)
	for _, ext := range []string{"citext", "btree_gist"} {
		var n int
		if err := check.QueryRow(t.Context(), "SELECT count(*) FROM pg_extension WHERE extname = $1", ext).Scan(&n); err != nil || n != 1 {
			t.Errorf("extension %s installed = %d, %v", ext, n, err)
		}
	}
}

func TestRun_WaitsForTheAdvisoryLock(t *testing.T) {
	const lockID = 917_001
	database := server.NewDatabase(t)
	conn := open(t, database)

	holder := server.Connect(t, database)
	if _, err := holder.Exec(t.Context(), "SELECT pg_advisory_lock($1)", int64(lockID)); err != nil {
		t.Fatalf("take lock: %v", err)
	}

	done := make(chan migrate.Result, 1)
	go func() {
		done <- migrate.Run(context.WithoutCancel(t.Context()), conn, migrations.FS, migrate.Options{LockID: lockID, LockRetrySeconds: 1})
	}()

	select {
	case res := <-done:
		t.Fatalf("run finished while another session held the lock: %+v", res)
	case <-time.After(2 * time.Second):
	}
	if _, err := holder.Exec(t.Context(), "SELECT pg_advisory_unlock($1)", int64(lockID)); err != nil {
		t.Fatalf("release lock: %v", err)
	}
	select {
	case res := <-done:
		if res.Status != migrate.StatusOK || len(res.Applied) != 5 {
			t.Errorf("run after the lock was released = %+v", res)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("run did not finish after the lock was released")
	}
}

func TestRun_ConcurrentRunsApplyEachMigrationOnce(t *testing.T) {
	const lockID = 917_002
	database := server.NewDatabase(t)
	results := make([]migrate.Result, 2)
	var wg sync.WaitGroup
	for i := range results {
		conn := open(t, database)
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = migrate.Run(t.Context(), conn, migrations.FS, migrate.Options{LockID: lockID, LockRetrySeconds: 1})
		}()
	}
	wg.Wait()

	applied := 0
	for _, r := range results {
		if r.Status != migrate.StatusOK || r.Version != 5 {
			t.Errorf("run = %+v", r)
		}
		applied += len(r.Applied)
	}
	if applied != 5 {
		t.Errorf("migrations applied %d times across both runs, want exactly 5", applied)
	}
}

func TestRun_FailureReturnsAnErrorPayload(t *testing.T) {
	database := server.NewDatabase(t)
	conn := open(t, database)
	broken := fstest.MapFS{
		"00001_ok.sql":     {Data: []byte("-- +goose Up\nCREATE TABLE probe (id int);\n")},
		"00002_broken.sql": {Data: []byte("-- +goose Up\nCREATE TABLE probe (id int);\n")},
	}
	res := migrate.Run(t.Context(), conn, broken, migrate.Options{LockID: 917_003})
	if res.Status != migrate.StatusError {
		t.Fatalf("status = %q, want error", res.Status)
	}
	if !strings.Contains(res.Error, "00002_broken.sql") && !strings.Contains(res.Error, "already exists") {
		t.Errorf("error does not explain the failure: %q", res.Error)
	}
	if len(res.Applied) != 1 || res.Version != 1 {
		t.Errorf("the good migration should stay applied: %+v", res)
	}
}
