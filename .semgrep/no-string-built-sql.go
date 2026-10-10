//go:build semgrep_fixture

// Fixture for .semgrep/no-string-built-sql.yaml. Each `ruleid` line must be flagged and each `ok`
// line must not. The build tag keeps it out of every real build.
package fixture

import (
	"context"
	"fmt"
	"strings"
)

type pool interface {
	Exec(ctx context.Context, sql string, args ...any) (any, error)
	Query(ctx context.Context, sql string, args ...any) (any, error)
	QueryRow(ctx context.Context, sql string, args ...any) any
}

func unsafeQueries(ctx context.Context, db pool, name, sortCol string) {
	// ruleid: no-string-built-sql
	db.Exec(ctx, fmt.Sprintf("DELETE FROM clients WHERE name = '%s'", name))

	// ruleid: no-string-built-sql
	db.Query(ctx, "SELECT * FROM entries ORDER BY "+sortCol)

	q := fmt.Sprintf("SELECT * FROM users WHERE email = '%s'", name)
	// ruleid: no-string-built-sql
	db.QueryRow(ctx, q)

	where := "SELECT id FROM clients WHERE name = '" + name + "'"
	// ruleid: no-string-built-sql
	db.Query(ctx, where)

	var b strings.Builder
	b.WriteString("SELECT * FROM entries WHERE notes LIKE '%")
	b.WriteString(name)
	// ruleid: no-string-built-sql
	db.Query(ctx, b.String())

	sql := "SELECT * FROM entries"
	sql += " ORDER BY " + sortCol
	// ruleid: no-string-built-sql
	db.Query(ctx, sql)
}

func safeQueries(ctx context.Context, db pool, name string) {
	// ok: no-string-built-sql
	db.Exec(ctx, "DELETE FROM clients WHERE name = $1", name)

	// ok: no-string-built-sql
	db.Query(ctx, "SELECT id, name "+"FROM clients WHERE user_id = $1", name)

	const listClients = "SELECT id, name FROM clients WHERE user_id = $1"
	// ok: no-string-built-sql
	db.Query(ctx, listClients, name)
}
