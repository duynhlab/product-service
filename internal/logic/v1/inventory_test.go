package v1

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/duynhlab/product-service/internal/core/cache"
	"github.com/duynhlab/product-service/internal/core/domain"
)

func TestReserveStock(t *testing.T) {
	items := []domain.ReservationItem{{ProductID: "1", Quantity: 2}, {ProductID: "2", Quantity: 1}}

	t.Run("success passes reservation through to the repo", func(t *testing.T) {
		repo := &stubProductRepo{}
		svc := NewProductService(repo, nil, nil)

		if err := svc.ReserveStock(context.Background(), "order-42", items); err != nil {
			t.Fatalf("ReserveStock returned %v, want nil", err)
		}
		if repo.reservedID != "order-42" {
			t.Errorf("repo got reservationID %q, want order-42", repo.reservedID)
		}
		if len(repo.reservedItems) != 2 {
			t.Errorf("repo got %d items, want 2", len(repo.reservedItems))
		}
	})

	t.Run("insufficient stock is propagated unchanged", func(t *testing.T) {
		repo := &stubProductRepo{reserveErr: domain.ErrInsufficientStock}
		svc := NewProductService(repo, nil, nil)

		err := svc.ReserveStock(context.Background(), "order-42", items)
		if !errors.Is(err, domain.ErrInsufficientStock) {
			t.Fatalf("ReserveStock returned %v, want ErrInsufficientStock", err)
		}
	})

	t.Run("repo error is propagated", func(t *testing.T) {
		repo := &stubProductRepo{reserveErr: errors.New("db down")}
		svc := NewProductService(repo, nil, nil)

		if err := svc.ReserveStock(context.Background(), "order-42", items); err == nil {
			t.Fatal("ReserveStock returned nil, want an error")
		}
	})
}

func TestReleaseStock(t *testing.T) {
	t.Run("success passes the reservation id through", func(t *testing.T) {
		repo := &stubProductRepo{}
		svc := NewProductService(repo, nil, nil)

		if err := svc.ReleaseStock(context.Background(), "order-42"); err != nil {
			t.Fatalf("ReleaseStock returned %v, want nil", err)
		}
		if repo.releasedID != "order-42" {
			t.Errorf("repo got reservationID %q, want order-42", repo.releasedID)
		}
	})

	t.Run("repo error is propagated", func(t *testing.T) {
		repo := &stubProductRepo{releaseErr: errors.New("db down")}
		svc := NewProductService(repo, nil, nil)

		if err := svc.ReleaseStock(context.Background(), "order-42"); err == nil {
			t.Fatal("ReleaseStock returned nil, want an error")
		}
	})
}

func TestReserveStockInvalidatesCache(t *testing.T) {
	mc := newMemCacheClient()
	_ = mc.Set(context.Background(), "product:1", []byte("stale"), time.Minute)
	_ = mc.Set(context.Background(), "product:2", []byte("stale"), time.Minute)
	pc := cache.NewProductCache(mc, time.Minute, time.Minute)
	svc := NewProductService(&stubProductRepo{}, pc, nil)

	items := []domain.ReservationItem{{ProductID: "1", Quantity: 2}, {ProductID: "2", Quantity: 1}}
	if err := svc.ReserveStock(context.Background(), "order-42", items); err != nil {
		t.Fatalf("ReserveStock returned %v, want nil", err)
	}
	for _, id := range []string{"1", "2"} {
		if v, _ := mc.Get(context.Background(), "product:"+id); v != nil {
			t.Errorf("product:%s still cached after reserve, want invalidated", id)
		}
	}
}

func TestReleaseStockInvalidatesCache(t *testing.T) {
	mc := newMemCacheClient()
	_ = mc.Set(context.Background(), "product:7", []byte("stale"), time.Minute)
	pc := cache.NewProductCache(mc, time.Minute, time.Minute)
	repo := &stubProductRepo{releasedProductIDs: []string{"7"}}
	svc := NewProductService(repo, pc, nil)

	if err := svc.ReleaseStock(context.Background(), "order-42"); err != nil {
		t.Fatalf("ReleaseStock returned %v, want nil", err)
	}
	if v, _ := mc.Get(context.Background(), "product:7"); v != nil {
		t.Error("product:7 still cached after release, want invalidated")
	}
}

// A cache invalidation failure must not fail the stock operation (fail-open).
func TestReserveStockSwallowsCacheError(t *testing.T) {
	mc := newMemCacheClient()
	mc.deleteErr = errors.New("cache down")
	pc := cache.NewProductCache(mc, time.Minute, time.Minute)
	svc := NewProductService(&stubProductRepo{}, pc, nil)

	items := []domain.ReservationItem{{ProductID: "1", Quantity: 1}}
	if err := svc.ReserveStock(context.Background(), "order-42", items); err != nil {
		t.Fatalf("ReserveStock returned %v, want nil despite cache error", err)
	}
}
