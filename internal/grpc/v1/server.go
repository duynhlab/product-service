// Package v1 implements the gRPC transport for product, version 1. It is a thin
// adapter over the logic layer (mirroring internal/web/v1) so the gRPC and HTTP
// paths share the same business logic. It serves the inventory operations the
// order-fulfillment saga calls: ReserveStock and ReleaseStock.
package v1

import (
	"context"
	"errors"
	"math"

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
	GetProductsByIDs(ctx context.Context, ids []string) ([]domain.Product, error)
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
	// Before validation: the phase-4 gate counts touches, not successes.
	recordStockSurfaceCall(ctx, rpcReserveStock)
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
	recordStockSurfaceCall(ctx, rpcReleaseStock)
	if req.GetReservationId() == "" {
		return nil, status.Error(codes.InvalidArgument, "reservation_id is required")
	}
	if err := s.svc.ReleaseStock(ctx, req.GetReservationId()); err != nil {
		return nil, status.Error(codes.Internal, "failed to release stock")
	}
	return &productv1.ReleaseStockResponse{}, nil
}

// maxGetProductsBatch caps a single GetProducts call. The checkout path is
// naturally bounded by cart size, but the method is callable by any
// in-network workload (no Kong in front) — the cap stops an oversized
// ANY() scan from being a DoS amplifier (security-review finding).
const maxGetProductsBatch = 200

// GetProducts is the batch price/availability read used by checkout
// re-validation (RFC-0015). The response contains only the requested ids
// that exist; prices convert to int64 minor units once, at this boundary.
func (s *Server) GetProducts(
	ctx context.Context,
	req *productv1.GetProductsRequest,
) (*productv1.GetProductsResponse, error) {
	if len(req.GetProductIds()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "product_ids is required")
	}
	if len(req.GetProductIds()) > maxGetProductsBatch {
		return nil, status.Error(codes.InvalidArgument, "too many product_ids in one call")
	}

	products, err := s.svc.GetProductsByIDs(ctx, req.GetProductIds())
	if err != nil {
		return nil, status.Error(codes.Internal, "get products failed")
	}

	out := make([]*productv1.ProductInfo, 0, len(products))
	for _, p := range products {
		qty := p.StockQuantity
		if qty > math.MaxInt32 {
			qty = math.MaxInt32
		}
		if qty < 0 {
			qty = 0
		}
		out = append(out, &productv1.ProductInfo{
			ProductId: p.ID,
			Name:      p.Name,
			// The catalog stores float dollars; the wire contract is int64
			// minor units. Round once, at this boundary.
			PriceMinor:   int64(math.Round(p.Price * 100)),
			AvailableQty: int32(qty), //nolint:gosec // clamped above
		})
	}
	return &productv1.GetProductsResponse{Products: out}, nil
}

// defaultCurrency is the platform money currency (RFC-0010: prices are USD
// minor units). The catalog has no per-product currency column — this is a
// single-currency platform — so every CurrentPrice reports USD.
const defaultCurrency = "USD"

// BatchGetCurrentPrices is the price-only successor to GetProducts (RFC-0021:
// availability moves to inventory.v1). It reuses the same cache-bypassing,
// DB-truth batch read as GetProducts — product is the price authority at
// checkout — and returns only prices, no stock fields. Unknown SKUs are
// omitted, matching GetProducts; sku_id == product id in this phase.
func (s *Server) BatchGetCurrentPrices(
	ctx context.Context,
	req *productv1.BatchGetCurrentPricesRequest,
) (*productv1.BatchGetCurrentPricesResponse, error) {
	if len(req.GetSkuIds()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "sku_ids is required")
	}
	// Same cap as GetProducts: the method is callable by any in-network
	// workload, so bound the ANY() scan to stop an oversized DoS amplifier.
	if len(req.GetSkuIds()) > maxGetProductsBatch {
		return nil, status.Error(codes.InvalidArgument, "too many sku_ids in one call")
	}

	products, err := s.svc.GetProductsByIDs(ctx, req.GetSkuIds())
	if err != nil {
		return nil, status.Error(codes.Internal, "get current prices failed")
	}

	out := make([]*productv1.CurrentPrice, 0, len(products))
	for _, p := range products {
		out = append(out, &productv1.CurrentPrice{
			SkuId: p.ID,
			Name:  p.Name,
			// Float dollars -> int64 minor units, rounded once at this boundary
			// (same conversion as GetProducts).
			PriceMinor: int64(math.Round(p.Price * 100)),
			Currency:   defaultCurrency,
			// GAP: the catalog has no lifecycle/publish column, so any row the
			// batch read returns is by definition still sold. Every existing
			// SKU is sellable=true until an explicit product lifecycle exists.
			Sellable: true,
		})
	}
	return &productv1.BatchGetCurrentPricesResponse{Prices: out}, nil
}
