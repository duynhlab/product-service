package database

import (
	"context"
	"net/url"

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
//
// When DB_PASSWORD_FILE is set (ADR-025 pattern A), WithPasswordFile makes
// dbx read the password from that mounted file on every new connection, so a
// rotated password is used without a pod restart. The DSN password is then
// ignored: a missing or empty file fails the connection instead of falling
// back. dbx also caps connection lifetime at 30m (±3m) so the pool moves onto
// a rotated password. An empty path is a no-op (DB_PASSWORD as before).
func Connect(ctx context.Context, cfg config.DatabaseConfig) (*pgxpool.Pool, error) {
	return dbx.NewPool(ctx, cfg.BuildDSN(),
		dbx.WithMaxConns(cfg.MaxConnections),
		dbx.WithPasswordFile(cfg.PasswordFile),
		dbx.WithMaxConnLifetime(cfg.MaxConnLifetime),
		dbx.WithHealthCheckPeriod(cfg.HealthCheckPeriod),
	)
}

// replicaPoolMaxConns sizes the replica pool: it serves only related-product
// reads, so it stays far below the primary pool.
const replicaPoolMaxConns = 5

// ConnectReplica opens the opt-in stale-read pool (ADR-089, #1304). It dials
// the same PgDog database as Connect, with the startup option
// pgdog.role=replica, so PgDog routes every statement on these connections to
// a product-db replica and falls back to the primary when all replicas lag
// past its ban threshold. Only reads that tolerate that lag and are not cached
// may use it: today FindRelatedProducts.
func ConnectReplica(ctx context.Context, cfg config.DatabaseConfig) (*pgxpool.Pool, error) {
	return dbx.NewPool(ctx, replicaDSN(cfg),
		dbx.WithMaxConns(replicaPoolMaxConns),
		dbx.WithPoolName("product-replica"),
		dbx.WithPasswordFile(cfg.PasswordFile),
		dbx.WithMaxConnLifetime(cfg.MaxConnLifetime),
		dbx.WithHealthCheckPeriod(cfg.HealthCheckPeriod),
	)
}

// replicaDSN is the primary DSN plus options=-c pgdog.role=replica. PostgreSQL
// accepts the dotted name as a custom setting, so the same DSN also works
// without PgDog (local-stack), where it simply reads the primary.
func replicaDSN(cfg config.DatabaseConfig) string {
	return cfg.BuildDSN() + "&options=" + url.QueryEscape("-c pgdog.role=replica")
}
