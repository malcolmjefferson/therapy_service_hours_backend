// Package migrations embeds the goose SQL migrations into the migrate Lambda binary (ADR-0003).
// Files are forward-only. A destructive statement (DROP, ALTER ... DROP) is allowed only in a file
// tagged with a `-- +contract` line; scripts/check-migrations.sh enforces that in CI.
package migrations

import "embed"

// FS holds every *.sql migration in this directory.
//
//go:embed *.sql
var FS embed.FS
