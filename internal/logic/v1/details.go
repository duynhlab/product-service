package v1

import (
	"context"

	"github.com/duynhlab/product-service/internal/core/domain"
	"github.com/duynhlab/product-service/middleware"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

// Review represents a review fetched from the review service.
type Review struct {
	ID        string  `json:"id"`
	ProductID string  `json:"product_id"`
	UserID    string  `json:"user_id"`
	Rating    int     `json:"rating"`
	Title     string  `json:"title"`
	Comment   string  `json:"comment"`
	CreatedAt *string `json:"created_at,omitempty"`
}

// ReviewFetcher fetches reviews for a product from the review service.
// Implemented by the web-layer ReviewClient (gRPC transport stays in web).
type ReviewFetcher interface {
	GetProductReviews(ctx context.Context, productID string, logger *zap.Logger) ([]Review, error)
}

// ProductDetails is the aggregated view returned by GetProductDetails.
type ProductDetails struct {
	Product         *domain.Product
	RelatedProducts []domain.Product
	Reviews         []Review
	ReviewsTotal    int
	ReviewsAverage  float64
}

// GetProductDetails aggregates a product with its related products and review
// summary. Reviews are soft-fail: on fetch error (or no review client wired)
// it returns an empty list and a zero summary.
func (s *ProductService) GetProductDetails(ctx context.Context, id string, logger *zap.Logger) (*ProductDetails, error) {
	ctx, span := middleware.StartSpan(ctx, "product.details", trace.WithAttributes(
		attribute.String("layer", "logic"),
		attribute.String("product.id", id),
	))
	defer span.End()

	product, err := s.GetProduct(ctx, id)
	if err != nil {
		return nil, err
	}

	// Related products aggregation (soft-fail, mirrors prior handler behavior).
	relatedProducts, _ := s.GetRelatedProducts(ctx, id, DefaultRelatedProductsLimit)

	details := &ProductDetails{
		Product:         product,
		RelatedProducts: relatedProducts,
		Reviews:         []Review{},
	}

	if s.reviewFetcher == nil {
		logger.Warn("Review client not configured, returning empty reviews")
		return details, nil
	}

	reviews, err := s.reviewFetcher.GetProductReviews(ctx, id, logger)
	if err != nil {
		// Soft-fail: log and continue with empty reviews.
		span.SetAttributes(attribute.Bool("reviews.fetch_failed", true))
		logger.Warn("Failed to fetch reviews, continuing with empty list",
			zap.Error(err),
			zap.String("product_id", id),
		)
		return details, nil
	}

	details.Reviews = reviews
	details.ReviewsTotal, details.ReviewsAverage = ComputeReviewsSummary(reviews)
	span.SetAttributes(
		attribute.Bool("reviews.fetch_failed", false),
		attribute.Int("reviews.total", details.ReviewsTotal),
		attribute.Float64("reviews.average_rating", details.ReviewsAverage),
	)

	return details, nil
}

// ComputeReviewsSummary computes total and average rating from reviews.
func ComputeReviewsSummary(reviews []Review) (total int, averageRating float64) {
	total = len(reviews)
	if total == 0 {
		return 0, 0.0
	}

	sum := 0
	for _, r := range reviews {
		sum += r.Rating
	}
	averageRating = float64(sum) / float64(total)
	return total, averageRating
}
