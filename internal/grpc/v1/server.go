// Package v1 implements the gRPC transport for product, version 1. It is a thin
// adapter over the logic layer (mirroring internal/web/v1) so the gRPC and HTTP
// paths share the same business logic. It serves the inventory operations the
// order-fulfillment saga calls: ReserveStock and ReleaseStock.
package v1

import (
	"context"
	"errors"

	productv1 "github.com/duynhlab/pkg/proto/product/v1"
	"github.com/duynhlab/product-service/internal/core/domain"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// StockManager is the logic-layer dependency the gRPC server needs.
// *logicv1.ProductService satisfies it.
type StockManager interface {
	ReserveStock(ctx context.Context, reservationID string, items []domain.ReservationItem) error
	ReleaseStock(ctx context.Context, reservationID string) error
}

// Server implements productv1.ProductServiceServer.
type Server struct {
	productv1.UnimplementedProductServiceServer

	svc StockManager
}

// NewServer creates a gRPC ProductService server backed by the logic service.
func NewServer(svc StockManager) *Server {
	return &Server{svc: svc}
}

// ReserveStock reserves inventory for an order (saga step 1). Insufficient stock
// maps to FAILED_PRECONDITION so the saga can distinguish a business rejection
// (don't retry forever) from an infrastructure error.
func (s *Server) ReserveStock(
	ctx context.Context,
	req *productv1.ReserveStockRequest,
) (*productv1.ReserveStockResponse, error) {
	if req.GetReservationId() == "" {
		return nil, status.Error(codes.InvalidArgument, "reservation_id is required")
	}
	items := make([]domain.ReservationItem, 0, len(req.GetItems()))
	for _, it := range req.GetItems() {
		if it.GetProductId() == "" || it.GetQuantity() <= 0 {
			return nil, status.Error(codes.InvalidArgument, "each item needs a product_id and quantity > 0")
		}
		items = append(items, domain.ReservationItem{
			ProductID: it.GetProductId(),
			Quantity:  int(it.GetQuantity()),
		})
	}

	if err := s.svc.ReserveStock(ctx, req.GetReservationId(), items); err != nil {
		if errors.Is(err, domain.ErrInsufficientStock) {
			return nil, status.Error(codes.FailedPrecondition, "insufficient stock")
		}
		return nil, status.Error(codes.Internal, "failed to reserve stock")
	}
	return &productv1.ReserveStockResponse{}, nil
}

// ReleaseStock returns reserved inventory (saga compensation). The request items
// are ignored — the reservation ledger is authoritative for what to restore.
func (s *Server) ReleaseStock(
	ctx context.Context,
	req *productv1.ReleaseStockRequest,
) (*productv1.ReleaseStockResponse, error) {
	if req.GetReservationId() == "" {
		return nil, status.Error(codes.InvalidArgument, "reservation_id is required")
	}
	if err := s.svc.ReleaseStock(ctx, req.GetReservationId()); err != nil {
		return nil, status.Error(codes.Internal, "failed to release stock")
	}
	return &productv1.ReleaseStockResponse{}, nil
}
