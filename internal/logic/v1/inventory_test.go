package v1

import (
	"context"
	"errors"
	"testing"

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
