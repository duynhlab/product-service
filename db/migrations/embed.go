// Package migrations embeds the SQL schema migrations applied by golang-migrate
// (via pkg/migratex) from the service's `migrate` subcommand.
package migrations

import "embed"

// FS holds the versioned migrations (NNNNNN_*.{up,down}.sql) under sql/.
//
// Only the up-migrations ever run here: migratex calls Up(). The one down-migration
// (000006, the stock-schema drop) is embedded so `migrate ... down 1` can reach it
// during a rollback, not because the service applies it.
//
//go:embed sql/*.sql
var FS embed.FS
