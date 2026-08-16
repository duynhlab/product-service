package v1

import (
	"context"

	"github.com/duynhlab/pkg/obsx"
	"github.com/duynhlab/product-service/internal/core/domain"
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

// AvailabilityUnknown is the soft-fail status when inventory can't be reached or
// doesn't track a SKU (RFC-0021 P2-6): the detail page still renders, just
// without a definite availability.
const AvailabilityUnknown = "unknown"

// Availability is inventory-service's view of a SKU (from
// inventory.v1/BatchGetAvailability), surfaced on the detail page as an
// enrichment. Status ∈ in_stock|low_stock|out_of_stock|unknown.
type Availability struct {
	Status string `json:"status"`
	// omitempty so an unknown/soft-failed result emits just {"status":"unknown"}
	// rather than an ambiguous available_to_promise:0 (vs a genuine 0 stock).
	AvailableToPromise int64 `json:"available_to_promise,omitempty"`
}

// AvailabilityFetcher fetches inventory availability for a SKU (RFC-0021).
// Implemented by the web-layer InventoryClient (gRPC transport stays in web).
//
// Always wired in the serving path since the flag that gated it was removed — its
// `product` position stopped meaning anything once product's own stock left the
// response. It stays an interface (and nil-tolerant below) because the logic layer
// must not know about gRPC, and unit tests construct the service without it.
type AvailabilityFetcher interface {
	GetAvailability(ctx context.Context, skuID string, logger *zap.Logger) (Availability, error)
}

// ProductDetails is the aggregated view returned by GetProductDetails.
type ProductDetails struct {
	Product         *domain.Product
	RelatedProducts []domain.Product
	Reviews         []Review
	ReviewsTotal    int
	ReviewsAverage  float64
	// Availability is nil unless inventory enrichment is enabled (P2-6); on a
	// fetch failure it is set to {Status: unknown} (soft-fail, never blocks).
	Availability *Availability
}

// GetProductDetails aggregates a product with its related products and review
// summary. Reviews are soft-fail: on fetch error (or no review client wired)
// it returns an empty list and a zero summary.
func (s *ProductService) GetProductDetails(ctx context.Context, id string, logger *zap.Logger) (*ProductDetails, error) {
	ctx, span := obsx.StartSpan(ctx, tracerScope, "product.details", trace.WithAttributes(
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

	// Availability enrichment is independent of reviews (RFC-0021 P2-6): run it
	// here so the review soft-fail early-returns below still carry it.
	s.enrichAvailability(ctx, id, details, logger)

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

// enrichAvailability adds inventory-sourced availability to the detail view. The
// product id IS the inventory sku_id (RFC-0021 key decision: sku_id = product_id
// initially), so the same id keys both.
//
// Soft-fail, and this is the whole availability story on this page now that
// product's own stock has left the response: a fetch error reports
// {Status: unknown} rather than failing the page, and the SPA treats unknown as
// PURCHASABLE — adding to a cart is not a reservation, and refusing the action
// because a read degraded turns a lost read into a lost sale. Checkout is where
// availability is enforced, and it fails closed there.
//
// A nil fetcher leaves the block off entirely. That is a test seam, not a runtime
// mode: the serving path always wires one.
func (s *ProductService) enrichAvailability(ctx context.Context, id string, details *ProductDetails, logger *zap.Logger) {
	if s.availabilityFetcher == nil {
		return
	}
	span := trace.SpanFromContext(ctx)
	avail, err := s.availabilityFetcher.GetAvailability(ctx, id, logger)
	if err != nil {
		span.SetAttributes(attribute.Bool("availability.fetch_failed", true))
		logger.Warn("Failed to fetch inventory availability, reporting unknown",
			zap.Error(err), zap.String("product_id", id))
		details.Availability = &Availability{Status: AvailabilityUnknown}
		return
	}
	details.Availability = &avail
	span.SetAttributes(
		attribute.Bool("availability.fetch_failed", false),
		attribute.String("availability.status", avail.Status),
	)
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
