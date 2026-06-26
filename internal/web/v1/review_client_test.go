package v1

import (
	"context"
	"errors"
	"testing"

	reviewv1 "github.com/duynhlab/pkg/proto/review/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

// stubReviewSvcClient is a fake reviewv1.ReviewServiceClient returning canned
// data so the gRPC mapping can be tested without a server.
type stubReviewSvcClient struct {
	resp         *reviewv1.GetProductReviewsResponse
	err          error
	gotProductID string
}

func (s *stubReviewSvcClient) GetProductReviews(_ context.Context, in *reviewv1.GetProductReviewsRequest, _ ...grpc.CallOption) (*reviewv1.GetProductReviewsResponse, error) {
	s.gotProductID = in.GetProductId()
	return s.resp, s.err
}

func TestNewReviewClient(t *testing.T) {
	if NewReviewClient(nil) == nil {
		t.Fatal("NewReviewClient returned nil")
	}
}

func TestReviewClient_GetProductReviews(t *testing.T) {
	t.Run("maps proto reviews and forwards the product id", func(t *testing.T) {
		stub := &stubReviewSvcClient{resp: &reviewv1.GetProductReviewsResponse{Reviews: []*reviewv1.Review{
			{Id: "r1", ProductId: "15", UserId: "u1", Rating: 5, Title: "Great", Comment: "nice", CreatedAt: "2026-01-01"},
			{Id: "r2", ProductId: "15", UserId: "u2", Rating: 3}, // empty CreatedAt -> nil pointer
		}}}
		c := &ReviewClient{client: stub}

		got, err := c.GetProductReviews(context.Background(), "15", zap.NewNop())
		if err != nil {
			t.Fatalf("GetProductReviews err = %v", err)
		}
		if stub.gotProductID != "15" {
			t.Errorf("forwarded product id = %q, want 15", stub.gotProductID)
		}
		if len(got) != 2 {
			t.Fatalf("got %d reviews, want 2", len(got))
		}
		if got[0].ID != "r1" || got[0].UserID != "u1" || got[0].Rating != 5 || got[0].Title != "Great" || got[0].Comment != "nice" {
			t.Errorf("review[0] = %+v", got[0])
		}
		if got[0].CreatedAt == nil || *got[0].CreatedAt != "2026-01-01" {
			t.Errorf("review[0].CreatedAt = %v, want 2026-01-01", got[0].CreatedAt)
		}
		if got[1].CreatedAt != nil {
			t.Errorf("review[1].CreatedAt = %q, want nil", *got[1].CreatedAt)
		}
	})

	t.Run("gRPC error is wrapped", func(t *testing.T) {
		c := &ReviewClient{client: &stubReviewSvcClient{err: errors.New("boom")}}
		if _, err := c.GetProductReviews(context.Background(), "15", zap.NewNop()); err == nil {
			t.Fatal("want an error")
		}
	})
}
