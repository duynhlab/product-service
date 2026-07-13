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

// setStock sets a product's stock_quantity directly (Create doesn't take stock).
func setStock(t *testing.T, pool *pgxpool.Pool, productID string, qty int) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`UPDATE products SET stock_quantity = $1 WHERE id = $2`, qty, productID); err != nil {
		t.Fatalf("set stock: %v", err)
	}
}

// stockOf reads back a product's stock_quantity.
func stockOf(t *testing.T, pool *pgxpool.Pool, productID string) int {
	t.Helper()
	var q int
	if err := pool.QueryRow(context.Background(),
		`SELECT stock_quantity FROM products WHERE id = $1`, productID).Scan(&q); err != nil {
		t.Fatalf("read stock: %v", err)
	}
	return q
}

// TestPostgresProductRepository_Saga_Integration exercises the
// order-fulfillment saga methods (ReserveStock / ReleaseStock) and
// FindRelatedProducts against real SQL — the new-code paths of RFC-0001.
func TestPostgresProductRepository_Saga_Integration(t *testing.T) {
	pool := newTestDB(t)
	repo := NewPostgresProductRepository(pool)
	ctx := context.Background()

	var category string
	if err := pool.QueryRow(ctx, "SELECT name FROM categories LIMIT 1").Scan(&category); err != nil {
		t.Fatalf("expected at least one seeded category: %v", err)
	}
	newProduct := func(name string, stock int) *domain.Product {
		p := &domain.Product{Name: name, Price: 10, Category: category}
		if err := repo.Create(ctx, p); err != nil {
			t.Fatalf("Create %s: %v", name, err)
		}
		setStock(t, pool, p.ID, stock)
		return p
	}

	t.Run("ReserveStock decrements and records the reservation", func(t *testing.T) {
		p := newProduct("SagaHappy", 10)
		items := []domain.ReservationItem{{ProductID: p.ID, Quantity: 3}}
		if err := repo.ReserveStock(ctx, "res-happy", items); err != nil {
			t.Fatalf("ReserveStock: %v", err)
		}
		if got := stockOf(t, pool, p.ID); got != 7 {
			t.Errorf("stock after reserve = %d, want 7", got)
		}
	})

	t.Run("ReserveStock is idempotent by reservationID", func(t *testing.T) {
		p := newProduct("SagaIdem", 5)
		items := []domain.ReservationItem{{ProductID: p.ID, Quantity: 2}}
		if err := repo.ReserveStock(ctx, "res-idem", items); err != nil {
			t.Fatalf("first ReserveStock: %v", err)
		}
		if err := repo.ReserveStock(ctx, "res-idem", items); err != nil {
			t.Fatalf("retried ReserveStock: %v", err)
		}
		if got := stockOf(t, pool, p.ID); got != 3 {
			t.Errorf("stock after retried reserve = %d, want 3 (single decrement)", got)
		}
	})

	t.Run("ReserveStock rolls back everything on insufficient stock", func(t *testing.T) {
		ok := newProduct("SagaOK", 10)
		low := newProduct("SagaLow", 1)
		items := []domain.ReservationItem{
			{ProductID: ok.ID, Quantity: 2},
			{ProductID: low.ID, Quantity: 5}, // understocked -> whole tx must roll back
		}
		err := repo.ReserveStock(ctx, "res-fail", items)
		if !errors.Is(err, domain.ErrInsufficientStock) {
			t.Fatalf("err = %v, want ErrInsufficientStock", err)
		}
		if got := stockOf(t, pool, ok.ID); got != 10 {
			t.Errorf("first item's stock = %d, want 10 (rolled back)", got)
		}
		if got := stockOf(t, pool, low.ID); got != 1 {
			t.Errorf("understocked item's stock = %d, want 1 (untouched)", got)
		}
	})

	t.Run("ReleaseStock restores stock and reports released product IDs", func(t *testing.T) {
		p := newProduct("SagaRelease", 8)
		items := []domain.ReservationItem{{ProductID: p.ID, Quantity: 6}}
		if err := repo.ReserveStock(ctx, "res-release", items); err != nil {
			t.Fatalf("ReserveStock: %v", err)
		}
		released, err := repo.ReleaseStock(ctx, "res-release")
		if err != nil {
			t.Fatalf("ReleaseStock: %v", err)
		}
		if len(released) != 1 || released[0] != p.ID {
			t.Errorf("released = %v, want [%s]", released, p.ID)
		}
		if got := stockOf(t, pool, p.ID); got != 8 {
			t.Errorf("stock after release = %d, want 8", got)
		}
	})

	t.Run("ReleaseStock is an idempotent no-op when already released or unknown", func(t *testing.T) {
		p := newProduct("SagaReleaseTwice", 4)
		items := []domain.ReservationItem{{ProductID: p.ID, Quantity: 2}}
		if err := repo.ReserveStock(ctx, "res-twice", items); err != nil {
			t.Fatalf("ReserveStock: %v", err)
		}
		if _, err := repo.ReleaseStock(ctx, "res-twice"); err != nil {
			t.Fatalf("first ReleaseStock: %v", err)
		}
		released, err := repo.ReleaseStock(ctx, "res-twice")
		if err != nil {
			t.Fatalf("second ReleaseStock: %v", err)
		}
		if len(released) != 0 {
			t.Errorf("second release = %v, want empty (no-op)", released)
		}
		if got := stockOf(t, pool, p.ID); got != 4 {
			t.Errorf("stock after double release = %d, want 4 (restored once)", got)
		}
		if rel2, err := repo.ReleaseStock(ctx, "res-never-existed"); err != nil || len(rel2) != 0 {
			t.Errorf("unknown reservation release = (%v, %v), want empty no-op", rel2, err)
		}
	})

	t.Run("FindRelatedProducts returns same-category products excluding self", func(t *testing.T) {
		a := newProduct("RelatedA", 1)
		b := newProduct("RelatedB", 1)
		c := newProduct("RelatedC", 1)
		got, err := repo.FindRelatedProducts(ctx, a.ID, 10)
		if err != nil {
			t.Fatalf("FindRelatedProducts: %v", err)
		}
		ids := map[string]bool{}
		for _, p := range got {
			ids[p.ID] = true
		}
		if ids[a.ID] {
			t.Error("related products must exclude the product itself")
		}
		if !ids[b.ID] || !ids[c.ID] {
			t.Errorf("related = %v, want to include %s and %s", got, b.ID, c.ID)
		}
		limited, err := repo.FindRelatedProducts(ctx, a.ID, 1)
		if err != nil {
			t.Fatalf("FindRelatedProducts(limit=1): %v", err)
		}
		if len(limited) != 1 {
			t.Errorf("limit=1 returned %d products", len(limited))
		}
	})

	t.Run("FindAll and Count honour filters and sorting", func(t *testing.T) {
		newProduct("FilterMe Gadget", 1)
		f := domain.ProductFilters{Category: category, Search: "FilterMe", SortBy: "price", Order: "desc", Page: 1, Limit: 5}
		got, err := repo.FindAll(ctx, f)
		if err != nil {
			t.Fatalf("FindAll(filters): %v", err)
		}
		if len(got) != 1 || got[0].Name != "FilterMe Gadget" {
			t.Errorf("filtered FindAll = %+v, want the single FilterMe product", got)
		}
		n, err := repo.Count(ctx, f)
		if err != nil {
			t.Fatalf("Count(filters): %v", err)
		}
		if n != 1 {
			t.Errorf("filtered Count = %d, want 1", n)
		}
	})
}

// TestPostgresProductRepository_FindByIDs_Integration covers the checkout
// batch read (RFC-0015): known ids round-trip, unknown and non-numeric ids
// are omitted, and an all-invalid batch returns empty without touching SQL.
func TestPostgresProductRepository_FindByIDs_Integration(t *testing.T) {
	pool := newTestDB(t)
	repo := NewPostgresProductRepository(pool)
	ctx := context.Background()

	var category string
	if err := pool.QueryRow(ctx, "SELECT name FROM categories LIMIT 1").Scan(&category); err != nil {
		t.Fatalf("expected at least one seeded category: %v", err)
	}
	a := &domain.Product{Name: "Batch A", Price: 12.5, Description: "d", Category: category, StockQuantity: 3}
	b := &domain.Product{Name: "Batch B", Price: 20, Description: "d", Category: category, StockQuantity: 0}
	for _, prod := range []*domain.Product{a, b} {
		if err := repo.Create(ctx, prod); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}
	// Create does not persist stock (inventory arrives via stock ops); set it
	// directly so the availability field in the batch read is exercised.
	if _, err := pool.Exec(ctx, "UPDATE products SET stock_quantity = 3 WHERE id = $1", a.ID); err != nil {
		t.Fatalf("set stock: %v", err)
	}

	t.Run("known ids round-trip, unknown and garbage omitted", func(t *testing.T) {
		got, err := repo.FindByIDs(ctx, []string{a.ID, b.ID, "999999", "not-a-number"})
		if err != nil {
			t.Fatalf("FindByIDs: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("len = %d, want 2 (unknown + garbage omitted): %+v", len(got), got)
		}
		byID := map[string]domain.Product{}
		for _, p := range got {
			byID[p.ID] = p
		}
		if byID[a.ID].Name != "Batch A" || byID[a.ID].StockQuantity != 3 {
			t.Errorf("a = %+v, want Batch A qty 3", byID[a.ID])
		}
		if byID[b.ID].Price != 20 || byID[b.ID].StockQuantity != 0 {
			t.Errorf("b = %+v, want price 20 qty 0", byID[b.ID])
		}
	})

	t.Run("all-invalid batch is empty, no error", func(t *testing.T) {
		got, err := repo.FindByIDs(ctx, []string{"abc", "-", ""})
		if err != nil || len(got) != 0 {
			t.Errorf("FindByIDs(all invalid) = (%v, %v), want empty, nil", got, err)
		}
	})
}
