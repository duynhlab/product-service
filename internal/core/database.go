package database

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/duynhlab/pkg/dbx"
	"github.com/duynhlab/product-service/config"
)

// Connect builds the service's Postgres pool via the shared dbx helper. dbx
// wires otelpgx query tracing (bounded span names, no bind-parameter or
// connection PII) and pgxpool.* pool-stat metrics, and applies the
// transaction-mode-pooler-safe settings (simple protocol, statement/description
// caches off) required by the PgDog/PgBouncer pooler.
//
// The DSN is cfg.BuildDSN() — the single source shared with the `migrate`
// subcommand, so the app and migrations connect identically. Max connections is
// applied via dbx.WithMaxConns rather than the DSN string, keeping the DSN a
// single source of truth.
func Connect(ctx context.Context, cfg config.DatabaseConfig) (*pgxpool.Pool, error) {
	return dbx.NewPool(ctx, cfg.BuildDSN(), dbx.WithMaxConns(cfg.MaxConnections))
}
