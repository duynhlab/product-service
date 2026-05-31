package v1

import (
	"context"
	"fmt"
	"time"

	reviewv1 "github.com/duynhlab/pkg/proto/review/v1"
	"github.com/duynhlab/product-service/middleware"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

// Review represents a review from the review service
type Review struct {
	ID        string  `json:"id"`
	ProductID string  `json:"product_id"`
	UserID    string  `json:"user_id"`
	Rating    int     `json:"rating"`
	Title     string  `json:"title"`
	Comment   string  `json:"comment"`
	CreatedAt *string `json:"created_at,omitempty"`
}

// ReviewClient fetches reviews from the review service over gRPC.
type ReviewClient struct {
	client reviewv1.ReviewServiceClient
}

// NewReviewClient wraps a gRPC connection (typically from grpcx.Dial).
func NewReviewClient(conn *grpc.ClientConn) *ReviewClient {
	return &ReviewClient{client: reviewv1.NewReviewServiceClient(conn)}
}

// GetProductReviews fetches reviews for a product from the review service.
func (c *ReviewClient) GetProductReviews(ctx context.Context, productID string, logger *zap.Logger) ([]Review, error) {
	ctx, span := middleware.StartSpan(ctx, "review_client.get_product_reviews", trace.WithAttributes(
		attribute.String("layer", "web"),
		attribute.String("product.id", productID),
		attribute.String("downstream.service", "review"),
	))
	defer span.End()

	// grpcx provides a default RPC deadline; bound it explicitly here too,
	// matching the 3s budget the REST client used for inter-service calls.
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	resp, err := c.client.GetProductReviews(ctx, &reviewv1.GetProductReviewsRequest{ProductId: productID})
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.Bool("review_service.available", false))
		logger.Error("Failed to call review service", zap.Error(err), zap.String("product_id", productID))
		return nil, fmt.Errorf("call review service: %w", err)
	}

	span.SetAttributes(attribute.Bool("review_service.available", true))

	protoReviews := resp.GetReviews()
	reviews := make([]Review, 0, len(protoReviews))
	for _, r := range protoReviews {
		reviews = append(reviews, reviewFromProto(r))
	}

	span.SetAttributes(attribute.Int("reviews.count", len(reviews)))
	logger.Debug("Fetched reviews from review service",
		zap.String("product_id", productID),
		zap.Int("count", len(reviews)),
	)

	return reviews, nil
}

// reviewFromProto maps a protobuf review to the local Review, identically to how
// the REST client decoded the JSON review.
func reviewFromProto(r *reviewv1.Review) Review {
	var createdAt *string
	if v := r.GetCreatedAt(); v != "" {
		createdAt = &v
	}

	return Review{
		ID:        r.GetId(),
		ProductID: r.GetProductId(),
		UserID:    r.GetUserId(),
		Rating:    int(r.GetRating()),
		Title:     r.GetTitle(),
		Comment:   r.GetComment(),
		CreatedAt: createdAt,
	}
}

// ComputeReviewsSummary computes total and average rating from reviews
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
