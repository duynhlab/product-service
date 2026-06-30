//go:build integration

// Integration tests for the PostgreSQL ProductRepository. They run a real
// Postgres via testcontainers-go and apply the service's migrations, so they
// exercise the actual SQL (not a mock). Run with:
//
//	go test -tags=integration ./internal/core/repository/...
//
// Requires a reachable Docker daemon. Excluded from the default `go test ./...`
// unit run by the `integration` build tag.
package repository

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/duynhlab/product-service/internal/core/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// newTestDB starts a throwaway Postgres, applies the migrations, and returns a
// pool for the repository under test. Everything is torn down via t.Cleanup.
func newTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	container, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("product"),
		postgres.WithUsername("product"),
		postgres.WithPassword("secret"),
		testcontainers.WithWaitStrategy(
			wait.ForListeningPort("5432/tcp").WithStartupTimeout(90*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}

	applyMigrations(t, ctx, dsn)

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// applyMigrations runs every db/migrations/sql/*.up.sql in lexical order using a
// simple-protocol connection (so multi-statement files execute in one round).
func applyMigrations(t *testing.T, ctx context.Context, dsn string) {
	t.Helper()
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect for migrations: %v", err)
	}
	defer conn.Close(ctx)

	dir := filepath.Join("..", "..", "..", "db", "migrations", "sql")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read migrations dir: %v", err)
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".sql" &&
			len(e.Name()) > 7 && e.Name()[len(e.Name())-7:] == ".up.sql" {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	for _, f := range files {
		sqlBytes, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if _, err := conn.Exec(ctx, string(sqlBytes)); err != nil {
			t.Fatalf("apply migration %s: %v", f, err)
		}
	}
}

func TestPostgresProductRepository_Integration(t *testing.T) {
	pool := newTestDB(t)
	repo := NewPostgresProductRepository(pool)
	ctx := context.Background()

	// Create maps a Category NAME to category_id via subquery, so use a category
	// the migrations actually seeded.
	var category string
	if err := pool.QueryRow(ctx, "SELECT name FROM categories LIMIT 1").Scan(&category); err != nil {
		t.Fatalf("expected at least one seeded category: %v", err)
	}

	t.Run("Create then FindByID", func(t *testing.T) {
		p := &domain.Product{Name: "Integ Widget", Price: 12.5, Description: "d", Category: category}
		if err := repo.Create(ctx, p); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if p.ID == "" {
			t.Fatal("Create did not set ID")
		}
		got, err := repo.FindByID(ctx, p.ID)
		if err != nil {
			t.Fatalf("FindByID: %v", err)
		}
		if got.Name != "Integ Widget" || got.Price != 12.5 || got.Category != category {
			t.Errorf("FindByID = %+v", got)
		}
	})

	t.Run("Count and FindAll see data", func(t *testing.T) {
		n, err := repo.Count(ctx, domain.ProductFilters{})
		if err != nil {
			t.Fatalf("Count: %v", err)
		}
		if n < 1 {
			t.Errorf("Count = %d, want >= 1", n)
		}
		all, err := repo.FindAll(ctx, domain.ProductFilters{})
		if err != nil {
			t.Fatalf("FindAll: %v", err)
		}
		if len(all) == 0 {
			t.Error("FindAll returned no products")
		}
	})

	t.Run("Update is reflected by FindByID", func(t *testing.T) {
		p := &domain.Product{Name: "ToUpdate", Price: 1, Category: category}
		if err := repo.Create(ctx, p); err != nil {
			t.Fatalf("Create: %v", err)
		}
		p.Name = "Updated"
		p.Price = 99
		if err := repo.Update(ctx, p); err != nil {
			t.Fatalf("Update: %v", err)
		}
		got, err := repo.FindByID(ctx, p.ID)
		if err != nil {
			t.Fatalf("FindByID: %v", err)
		}
		if got.Name != "Updated" || got.Price != 99 {
			t.Errorf("after Update = %+v", got)
		}
	})

	t.Run("Delete then FindByID returns ErrNotFound", func(t *testing.T) {
		p := &domain.Product{Name: "ToDelete", Price: 1, Category: category}
		if err := repo.Create(ctx, p); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if err := repo.Delete(ctx, p.ID); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, err := repo.FindByID(ctx, p.ID); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("FindByID after delete err = %v, want ErrNotFound", err)
		}
	})

	t.Run("FindByID missing returns ErrNotFound", func(t *testing.T) {
		if _, err := repo.FindByID(ctx, "987654321"); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("FindByID(missing) err = %v, want ErrNotFound", err)
		}
	})
}
