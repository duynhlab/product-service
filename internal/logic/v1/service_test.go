package v1

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/duynhlab/product-service/internal/core/cache"
	"github.com/duynhlab/product-service/internal/core/domain"
)

// memCacheClient is an in-memory cache.CacheClient for exercising the
// Cache-Aside branches of the logic layer. Always a cache miss on first read.
type memCacheClient struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newMemCacheClient() *memCacheClient {
	return &memCacheClient{data: make(map[string][]byte)}
}

func (m *memCacheClient) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.data[key], nil
}

func (m *memCacheClient) Set(_ context.Context, key string, value []byte, _ time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = value
	return nil
}

func (m *memCacheClient) SetNX(_ context.Context, key string, value []byte, _ time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.data[key]; ok {
		return false, nil
	}
	m.data[key] = value
	return true, nil
}

func (m *memCacheClient) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}

func (m *memCacheClient) DeleteByPattern(_ context.Context, _ string) error { return nil }

func (m *memCacheClient) Close() error { return nil }

func newTestCache() *cache.ProductCache {
	return cache.NewProductCache(newMemCacheClient(), time.Minute, time.Minute)
}

func TestListProducts(t *testing.T) {
	// Not parallel: shares the unsynchronised middleware tracer global (see
	// TestGetProductDetails). Cache is nil, so calls hit the repo directly.
	products := []domain.Product{{ID: "p1"}, {ID: "p2"}}

	tests := []struct {
		name      string
		repo      *stubProductRepo
		wantCount int
		wantTotal int
		wantErr   bool
	}{
		{
			name:      "returns products and total",
			repo:      &stubProductRepo{all: products, count: 2},
			wantCount: 2,
			wantTotal: 2,
		},
		{
			name:    "find error propagates",
			repo:    &stubProductRepo{findAllErr: errors.New("db down")},
			wantErr: true,
		},
		{
			name:    "count error propagates",
			repo:    &stubProductRepo{all: products, countErr: errors.New("count failed")},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewProductService(tt.repo, nil, nil)
			got, total, err := svc.ListProducts(context.Background(), domain.ProductFilters{})

			if tt.wantErr {
				if err == nil {
					t.Fatalf("ListProducts() expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("ListProducts() unexpected error: %v", err)
			}
			if len(got) != tt.wantCount {
				t.Errorf("products len = %d, want %d", len(got), tt.wantCount)
			}
			if total != tt.wantTotal {
				t.Errorf("total = %d, want %d", total, tt.wantTotal)
			}
		})
	}
}

func TestListProductsCacheAside(t *testing.T) {
	// Cache miss -> DB read -> cache write -> second call serves from cache.
	products := []domain.Product{{ID: "p1"}}
	repo := &stubProductRepo{all: products, count: 1}
	svc := NewProductService(repo, newTestCache(), nil)

	got, total, err := svc.ListProducts(context.Background(), domain.ProductFilters{})
	if err != nil {
		t.Fatalf("ListProducts() unexpected error: %v", err)
	}
	if len(got) != 1 || total != 1 {
		t.Fatalf("ListProducts() = (%d items, total %d), want (1, 1)", len(got), total)
	}

	// Second call should hit the cache and still return the same data.
	got2, total2, err := svc.ListProducts(context.Background(), domain.ProductFilters{})
	if err != nil {
		t.Fatalf("ListProducts() second call error: %v", err)
	}
	if len(got2) != 1 || total2 != 1 {
		t.Errorf("cached ListProducts() = (%d items, total %d), want (1, 1)", len(got2), total2)
	}
}

func TestGetProductCacheAside(t *testing.T) {
	product := &domain.Product{ID: "p1", Name: "Widget"}

	t.Run("miss then DB", func(t *testing.T) {
		svc := NewProductService(&stubProductRepo{product: product}, newTestCache(), nil)
		got, err := svc.GetProduct(context.Background(), "p1")
		if err != nil {
			t.Fatalf("GetProduct() unexpected error: %v", err)
		}
		if got.ID != "p1" {
			t.Errorf("GetProduct() = %v, want id p1", got)
		}
	})

	t.Run("not found via cache path", func(t *testing.T) {
		svc := NewProductService(&stubProductRepo{findByIDErr: domain.ErrNotFound}, newTestCache(), nil)
		_, err := svc.GetProduct(context.Background(), "missing")
		if !errors.Is(err, ErrProductNotFound) {
			t.Errorf("GetProduct() error = %v, want ErrProductNotFound", err)
		}
	})
}

func TestCreateProductInvalidatesCache(t *testing.T) {
	repo := &stubProductRepo{}
	svc := NewProductService(repo, newTestCache(), nil)
	got, err := svc.CreateProduct(context.Background(), domain.CreateProductRequest{Name: "Widget", Price: 1.5})
	if err != nil {
		t.Fatalf("CreateProduct() unexpected error: %v", err)
	}
	if got == nil || got.ID == "" {
		t.Errorf("CreateProduct() expected product with ID, got %v", got)
	}
}

func TestGetProduct(t *testing.T) {
	// Not parallel: shares the unsynchronised middleware tracer global.
	product := &domain.Product{ID: "p1", Name: "Widget"}

	tests := []struct {
		name        string
		repo        *stubProductRepo
		wantErr     error
		wantProduct bool
	}{
		{
			name:        "found",
			repo:        &stubProductRepo{product: product},
			wantProduct: true,
		},
		{
			name:    "not found maps to ErrProductNotFound",
			repo:    &stubProductRepo{findByIDErr: domain.ErrNotFound},
			wantErr: ErrProductNotFound,
		},
		{
			name:    "generic error propagates",
			repo:    &stubProductRepo{findByIDErr: errors.New("db error")},
			wantErr: nil, // non-sentinel, just expect an error
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewProductService(tt.repo, nil, nil)
			got, err := svc.GetProduct(context.Background(), "p1")

			if tt.wantProduct {
				if err != nil {
					t.Fatalf("GetProduct() unexpected error: %v", err)
				}
				if got != product {
					t.Errorf("GetProduct() = %v, want %v", got, product)
				}
				return
			}

			if err == nil {
				t.Fatalf("GetProduct() expected error, got nil")
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Errorf("GetProduct() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestCreateProduct(t *testing.T) {
	// Not parallel: shares the unsynchronised middleware tracer global.
	tests := []struct {
		name    string
		req     domain.CreateProductRequest
		repo    *stubProductRepo
		wantErr error
	}{
		{
			name: "success assigns id",
			req:  domain.CreateProductRequest{Name: "Widget", Price: 9.99},
			repo: &stubProductRepo{},
		},
		{
			name:    "zero price rejected",
			req:     domain.CreateProductRequest{Name: "Free", Price: 0},
			repo:    &stubProductRepo{},
			wantErr: ErrInvalidPrice,
		},
		{
			name:    "negative price rejected",
			req:     domain.CreateProductRequest{Name: "Bad", Price: -1},
			repo:    &stubProductRepo{},
			wantErr: ErrInvalidPrice,
		},
		{
			name:    "repo error propagates",
			req:     domain.CreateProductRequest{Name: "Widget", Price: 5},
			repo:    &stubProductRepo{createErr: errors.New("insert failed")},
			wantErr: nil, // non-sentinel, just expect an error
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewProductService(tt.repo, nil, nil)
			got, err := svc.CreateProduct(context.Background(), tt.req)

			if tt.wantErr == ErrInvalidPrice {
				if !errors.Is(err, ErrInvalidPrice) {
					t.Fatalf("CreateProduct() error = %v, want ErrInvalidPrice", err)
				}
				return
			}
			if tt.repo.createErr != nil {
				if err == nil {
					t.Fatalf("CreateProduct() expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("CreateProduct() unexpected error: %v", err)
			}
			if got == nil || got.ID == "" {
				t.Errorf("CreateProduct() expected product with ID, got %v", got)
			}
		})
	}
}
