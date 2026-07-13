package v1

import (
	"context"
	"errors"
	"testing"

	"github.com/duynhlab/product-service/internal/core/domain"
)

// TestGetProductsByIDs pins the checkout re-validation read (RFC-0015): a
// cache-bypassing passthrough to the repository batch read.
func TestGetProductsByIDs(t *testing.T) {
	t.Run("returns repo batch", func(t *testing.T) {
		repo := &stubProductRepo{all: []domain.Product{{ID: "1", Price: 29.99}}}
		got, err := NewProductService(repo, nil, nil).GetProductsByIDs(context.Background(), []string{"1"})
		if err != nil || len(got) != 1 || got[0].ID != "1" {
			t.Fatalf("got (%v, %v), want 1 product id=1", got, err)
		}
	})
	t.Run("propagates repo error", func(t *testing.T) {
		repo := &stubProductRepo{findAllErr: errors.New("boom")}
		if _, err := NewProductService(repo, nil, nil).GetProductsByIDs(context.Background(), []string{"1"}); err == nil {
			t.Fatal("want error, got nil")
		}
	})
}
