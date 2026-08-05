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

// TestPostgresProductRepository_Queries_Integration exercises FindRelatedProducts
// and the filtered FindAll/Count against real SQL.
//
// It was TestPostgresProductRepository_Saga_Integration and also covered
// ReserveStock/ReleaseStock; RFC-0021 phase 4 removed those methods, so their
// subtests went with them rather than being kept green against nothing.
func TestPostgresProductRepository_Queries_Integration(t *testing.T) {
	pool := newTestDB(t)
	repo := NewPostgresProductRepository(pool)
	ctx := context.Background()

	var category string
	if err := pool.QueryRow(ctx, "SELECT name FROM categories LIMIT 1").Scan(&category); err != nil {
		t.Fatalf("expected at least one seeded category: %v", err)
	}
	newProduct := func(name string) *domain.Product {
		p := &domain.Product{Name: name, Price: 10, Category: category}
		if err := repo.Create(ctx, p); err != nil {
			t.Fatalf("Create %s: %v", name, err)
		}
		return p
	}

	t.Run("FindRelatedProducts returns same-category products excluding self", func(t *testing.T) {
		a := newProduct("RelatedA")
		b := newProduct("RelatedB")
		c := newProduct("RelatedC")
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
		newProduct("FilterMe Gadget")
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
	// Create does not persist stock, and since RFC-0021 phase 4 NOTHING in this
	// service writes stock_quantity — the column is frozen pending its own removal; set it
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

// The catalog LIST reported every product as out of stock: FindAll's SELECT
// omitted stock_quantity while FindByID includes it, so the same product showed
// its real stock on the detail page and 0 in every list. Nothing downstream can
// repair a field that was never read — checkout's availability fallback and the
// SPA both consume the list.
func TestPostgresProductRepository_FindAllCarriesStock_Integration(t *testing.T) {
	pool := newTestDB(t)
	repo := NewPostgresProductRepository(pool)
	ctx := context.Background()

	var category string
	if err := pool.QueryRow(ctx, "SELECT name FROM categories LIMIT 1").Scan(&category); err != nil {
		t.Fatalf("expected at least one seeded category: %v", err)
	}

	p := &domain.Product{Name: "Stocked Widget", Price: 9.99, Description: "d", Category: category}
	if err := repo.Create(ctx, p); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Stock is set directly rather than through Create: Create's INSERT omits the
	// column (a separate, pre-existing gap — stock is managed by the saga's
	// reserve/release SQL, not by product authoring), and going through it here
	// would let this test pass on 0 == 0 without reading anything.
	if _, err := pool.Exec(ctx, "UPDATE products SET stock_quantity = 42 WHERE id = $1::int", p.ID); err != nil {
		t.Fatalf("seed stock: %v", err)
	}

	// The detail read is the reference: whatever it reports, the list must agree.
	detail, err := repo.FindByID(ctx, p.ID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if detail.StockQuantity != 42 {
		t.Fatalf("detail stock = %d, want 42 — the reference read is broken, this test cannot judge the list", detail.StockQuantity)
	}

	list, err := repo.FindAll(ctx, domain.ProductFilters{Search: "Stocked Widget"})
	if err != nil {
		t.Fatalf("FindAll: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("FindAll returned %d products, want 1", len(list))
	}
	if list[0].StockQuantity != detail.StockQuantity {
		t.Errorf("list stock = %d, detail stock = %d — the same product must not be in stock on one page and sold out on another",
			list[0].StockQuantity, detail.StockQuantity)
	}
}
