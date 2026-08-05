// Package migrations embeds the SQL schema migrations applied by golang-migrate
// (via pkg/migratex) from the service's `migrate` subcommand.
package migrations

import "embed"

// FS holds the versioned migrations (NNNNNN_*.{up,down}.sql) under sql/.
//
// Only the up-migrations ever run: migratex calls Up() and nothing here calls Down().
// The one down-migration (000006, the stock-schema drop) is committed for review and
// for a HAND-APPLIED rollback — it is NOT reachable through this embed at runtime,
// because the golang-migrate CLI cannot read an embed.FS compiled into another
// binary. The file documents the real procedure; do not infer from its presence that
// a `down` command exists.
//
//go:embed sql/*.sql
var FS embed.FS
