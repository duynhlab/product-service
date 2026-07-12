package v1

import (
	"context"
	"errors"
	"testing"

	"github.com/duynhlab/product-service/internal/core/domain"
	"go.uber.org/zap"
)

func TestComputeReviewsSummary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		reviews     []Review
		wantTotal   int
		wantAverage float64
	}{
		{
			name:        "empty",
			reviews:     nil,
			wantTotal:   0,
			wantAverage: 0.0,
		},
		{
			name:        "empty slice",
			reviews:     []Review{},
			wantTotal:   0,
			wantAverage: 0.0,
		},
		{
			name:        "single review",
			reviews:     []Review{{Rating: 4}},
			wantTotal:   1,
			wantAverage: 4.0,
		},
		{
			name:        "many reviews exact average",
			reviews:     []Review{{Rating: 2}, {Rating: 4}, {Rating: 6}},
			wantTotal:   3,
			wantAverage: 4.0,
		},
		{
			name:        "average requires rounding",
			reviews:     []Review{{Rating: 5}, {Rating: 4}, {Rating: 4}},
			wantTotal:   3,
			wantAverage: 13.0 / 3.0,
		},
		{
			name:        "all zero ratings",
			reviews:     []Review{{Rating: 0}, {Rating: 0}},
			wantTotal:   2,
			wantAverage: 0.0,
		},
		{
			name:        "max ratings",
			reviews:     []Review{{Rating: 5}, {Rating: 5}, {Rating: 5}, {Rating: 5}},
			wantTotal:   4,
			wantAverage: 5.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gotTotal, gotAverage := ComputeReviewsSummary(tt.reviews)
			if gotTotal != tt.wantTotal {
				t.Errorf("ComputeReviewsSummary() total = %d, want %d", gotTotal, tt.wantTotal)
			}
			if gotAverage != tt.wantAverage {
				t.Errorf("ComputeReviewsSummary() average = %v, want %v", gotAverage, tt.wantAverage)
			}
		})
	}
}

// stubReviewFetcher is a test double for the ReviewFetcher interface.
type stubReviewFetcher struct {
	reviews []Review
	err     error
}

func (s *stubReviewFetcher) GetProductReviews(_ context.Context, _ string, _ *zap.Logger) ([]Review, error) {
	return s.reviews, s.err
}

// stubProductRepo is a test double for domain.ProductRepository covering the
// methods exercised by the logic layer.
type stubProductRepo struct {
	product     *domain.Product
	findByIDErr error
	related     []domain.Product
	relatedErr  error
	all         []domain.Product
	findAllErr  error
	count       int
	countErr    error
	createErr   error

	// inventory
	reserveErr         error
	releaseErr         error
	reservedID         string
	reservedItems      []domain.ReservationItem
	releasedID         string
	releasedProductIDs []string
}

func (s *stubProductRepo) FindByID(_ context.Context, _ string) (*domain.Product, error) {
	return s.product, s.findByIDErr
}

func (s *stubProductRepo) FindByIDs(_ context.Context, _ []string) ([]domain.Product, error) {
	return s.all, s.findAllErr
}

func (s *stubProductRepo) FindAll(_ context.Context, _ domain.ProductFilters) ([]domain.Product, error) {
	return s.all, s.findAllErr
}

func (s *stubProductRepo) Create(_ context.Context, p *domain.Product) error {
	if s.createErr != nil {
		return s.createErr
	}
	p.ID = "generated-id"
	return nil
}

func (s *stubProductRepo) Update(_ context.Context, _ *domain.Product) error { return nil }

func (s *stubProductRepo) Delete(_ context.Context, _ string) error { return nil }

func (s *stubProductRepo) FindRelatedProducts(_ context.Context, _ string, _ int) ([]domain.Product, error) {
	return s.related, s.relatedErr
}

func (s *stubProductRepo) Count(_ context.Context, _ domain.ProductFilters) (int, error) {
	return s.count, s.countErr
}

func (s *stubProductRepo) ReserveStock(_ context.Context, reservationID string, items []domain.ReservationItem) error {
	s.reservedID = reservationID
	s.reservedItems = items
	return s.reserveErr
}

func (s *stubProductRepo) ReleaseStock(_ context.Context, reservationID string) ([]string, error) {
	s.releasedID = reservationID
	return s.releasedProductIDs, s.releaseErr
}

func TestGetProductDetails(t *testing.T) {
	// Not parallel: GetProductDetails calls middleware.StartSpan, whose
	// GetTracer lazily initialises an unsynchronised package-level tracer.
	// Concurrent first-calls race on that global, so these cases run serially.

	product := &domain.Product{ID: "p1", Name: "Widget"}
	related := []domain.Product{{ID: "p2"}, {ID: "p3"}}
	reviews := []Review{{ID: "r1", Rating: 4}, {ID: "r2", Rating: 2}}

	tests := []struct {
		name         string
		repo         *stubProductRepo
		fetcher      ReviewFetcher
		wantErr      bool
		wantReviews  int
		wantTotal    int
		wantAverage  float64
		wantRelatedN int
	}{
		{
			name:         "happy path aggregates reviews and related",
			repo:         &stubProductRepo{product: product, related: related},
			fetcher:      &stubReviewFetcher{reviews: reviews},
			wantReviews:  2,
			wantTotal:    2,
			wantAverage:  3.0,
			wantRelatedN: 2,
		},
		{
			name:         "review fetch error soft-fails to empty",
			repo:         &stubProductRepo{product: product, related: related},
			fetcher:      &stubReviewFetcher{err: errors.New("review service down")},
			wantReviews:  0,
			wantTotal:    0,
			wantAverage:  0.0,
			wantRelatedN: 2,
		},
		{
			name:         "nil review fetcher returns empty reviews",
			repo:         &stubProductRepo{product: product, related: related},
			fetcher:      nil,
			wantReviews:  0,
			wantTotal:    0,
			wantAverage:  0.0,
			wantRelatedN: 2,
		},
		{
			name:         "related products error soft-fails",
			repo:         &stubProductRepo{product: product, relatedErr: errors.New("db error")},
			fetcher:      &stubReviewFetcher{reviews: reviews},
			wantReviews:  2,
			wantTotal:    2,
			wantAverage:  3.0,
			wantRelatedN: 0,
		},
		{
			name:    "product not found propagates error",
			repo:    &stubProductRepo{findByIDErr: domain.ErrNotFound},
			fetcher: &stubReviewFetcher{reviews: reviews},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Cache disabled (nil) so GetProduct/GetRelatedProducts hit the repo directly.
			svc := NewProductService(tt.repo, nil, tt.fetcher)
			details, err := svc.GetProductDetails(context.Background(), "p1", zap.NewNop())

			if tt.wantErr {
				if err == nil {
					t.Fatalf("GetProductDetails() expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("GetProductDetails() unexpected error: %v", err)
			}

			if got := len(details.Reviews); got != tt.wantReviews {
				t.Errorf("Reviews len = %d, want %d", got, tt.wantReviews)
			}
			if details.ReviewsTotal != tt.wantTotal {
				t.Errorf("ReviewsTotal = %d, want %d", details.ReviewsTotal, tt.wantTotal)
			}
			if details.ReviewsAverage != tt.wantAverage {
				t.Errorf("ReviewsAverage = %v, want %v", details.ReviewsAverage, tt.wantAverage)
			}
			if got := len(details.RelatedProducts); got != tt.wantRelatedN {
				t.Errorf("RelatedProducts len = %d, want %d", got, tt.wantRelatedN)
			}
			if details.Product != product {
				t.Errorf("Product = %v, want %v", details.Product, product)
			}
		})
	}
}
