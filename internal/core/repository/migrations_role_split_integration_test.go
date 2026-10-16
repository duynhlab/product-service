//go:build integration

// The migrations must apply on the platform's owner / migrator / runtime split
// (homelab RFC-0029, ADR-088): the migrator logs in, becomes product_owner at
// login, and runs every file. The setup reproduces the catalog the platform
// builds: authz_admin creates the three roles (so it is a member of the owner,
// with INHERIT), the db_authz login inherits authz_admin, and the runtime's
// default privileges exist BEFORE the first migration, the worst case of the
// race between pgroles and the migrate init container.
package repository

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

const roleSplitSetup = `
CREATE ROLE authz_admin NOLOGIN CREATEROLE INHERIT;
CREATE ROLE db_authz LOGIN PASSWORD 'db_authz' IN ROLE authz_admin;
SET ROLE authz_admin;
SET createrole_self_grant = 'set, inherit';
CREATE ROLE product_owner NOLOGIN;
CREATE ROLE product_migrator LOGIN NOINHERIT PASSWORD 'migrator';
CREATE ROLE product_runtime LOGIN PASSWORD 'runtime';
GRANT product_owner TO product_migrator WITH INHERIT FALSE, SET TRUE, ADMIN FALSE;
ALTER ROLE product_migrator SET role = 'product_owner';
RESET ROLE;
ALTER DATABASE product OWNER TO product_owner;
SET ROLE authz_admin;
GRANT CONNECT ON DATABASE product TO product_runtime, product_migrator;
ALTER DEFAULT PRIVILEGES FOR ROLE product_owner IN SCHEMA public
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO product_runtime;
ALTER DEFAULT PRIVILEGES FOR ROLE product_owner IN SCHEMA public
  GRANT USAGE, SELECT ON SEQUENCES TO product_runtime;
RESET ROLE;
`

// newRoleSplitDB starts Postgres with the role split in place and returns the
// superuser DSN and the migrator DSN.
func newRoleSplitDB(t *testing.T) (admin, migrator string) {
	t.Helper()
	ctx := context.Background()

	container, err := postgres.Run(ctx, "postgres:18-alpine",
		postgres.WithDatabase("product"),
		postgres.WithUsername("postgres"),
		postgres.WithPassword("secret"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	admin, err = container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	execAs(t, admin, roleSplitSetup)

	u, err := url.Parse(admin)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	u.User = url.UserPassword("product_migrator", "migrator")
	return admin, u.String()
}

func execAs(t *testing.T, dsn, sql string) {
	t.Helper()
	ctx := context.Background()
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, sql); err != nil {
		t.Fatalf("exec: %v", err)
	}
}

func queryBool(t *testing.T, dsn, sql string) bool {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)
	var v bool
	if err := conn.QueryRow(ctx, sql).Scan(&v); err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	return v
}

// The full chain applies as the migrator, although db_authz (through
// authz_admin -> product_owner) and product_runtime (through the default
// privileges) both hold SELECT on products when 000006 checks for leftovers.
func TestMigrations_ApplyOnRoleSplit(t *testing.T) {
	admin, migrator := newRoleSplitDB(t)
	if err := applyMigrationFiles(context.Background(), migrator, migrationFiles(t)); err != nil {
		t.Fatalf("migrations as product_migrator: %v", err)
	}

	for name, sql := range map[string]string{
		"products owned by product_owner": `SELECT tableowner = 'product_owner' FROM pg_tables WHERE tablename = 'products'`,
		"runtime reads products":          `SELECT has_table_privilege('product_runtime', 'public.products', 'SELECT')`,
		// What made the old has_table_privilege() check refuse forever.
		"db_authz reaches products through the owner": `SELECT has_table_privilege('db_authz', 'public.products', 'SELECT')`,
		"stock column dropped": `SELECT NOT EXISTS (SELECT 1 FROM information_schema.columns
		                          WHERE table_name = 'products' AND column_name = 'stock_quantity')`,
	} {
		if !queryBool(t, admin, sql) {
			t.Errorf("%s: got false", name)
		}
	}
}

// 000006 still refuses an undocumented direct grant on products.
func TestMigrations_DropStockRefusesDirectGrant(t *testing.T) {
	admin, migrator := newRoleSplitDB(t)
	files := migrationFiles(t)

	var before, rest []string
	for _, f := range files {
		if strings.Contains(f, "000006_") || len(rest) > 0 {
			rest = append(rest, f)
			continue
		}
		before = append(before, f)
	}
	if len(rest) == 0 {
		t.Fatal("000006 migration not found")
	}
	if err := applyMigrationFiles(context.Background(), migrator, before); err != nil {
		t.Fatalf("migrations before 000006: %v", err)
	}
	execAs(t, admin, `CREATE ROLE intruder LOGIN; GRANT SELECT ON public.products TO intruder;`)

	err := applyMigrationFiles(context.Background(), migrator, rest[:1])
	if err == nil || !strings.Contains(err.Error(), "intruder") {
		t.Fatalf("000006 with an undocumented grant: want a refusal naming intruder, got %v", err)
	}
}
