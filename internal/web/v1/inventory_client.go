package v1

import (
	"context"
	"fmt"
	"time"

	"github.com/duynhlab/pkg/obsx"
	inventoryv1 "github.com/duynhlab/pkg/proto/inventory/v1"
	logicv1 "github.com/duynhlab/product-service/internal/logic/v1"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

// InventoryClient fetches availability from inventory-service over gRPC — the
// GetProductDetails enrichment (RFC-0021 P2-6). Mirrors ReviewClient: transport
// only, soft-fail handled by the logic layer.
type InventoryClient struct {
	client inventoryv1.InventoryServiceClient
}

// NewInventoryClient wraps a gRPC connection (typically from grpcx.Dial).
func NewInventoryClient(conn *grpc.ClientConn) *InventoryClient {
	return &InventoryClient{client: inventoryv1.NewInventoryServiceClient(conn)}
}

// GetAvailability returns inventory's availability for one SKU. A SKU inventory
// doesn't track (absent from the response) reports unknown rather than erroring
// — that is a data state, not a transport failure.
func (c *InventoryClient) GetAvailability(ctx context.Context, skuID string, logger *zap.Logger) (logicv1.Availability, error) {
	ctx, span := obsx.StartSpan(ctx, tracerScope, "inventory_client.get_availability", trace.WithAttributes(
		attribute.String("layer", "web"),
		attribute.String("product.id", skuID),
		attribute.String("downstream.service", "inventory"),
	))
	defer span.End()

	// grpcx provides a default RPC deadline; bound it explicitly too, matching
	// the 3s budget used for the review enrichment.
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	resp, err := c.client.BatchGetAvailability(ctx, &inventoryv1.BatchGetAvailabilityRequest{SkuIds: []string{skuID}})
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.Bool("inventory_service.available", false))
		logger.Error("Failed to call inventory service", zap.Error(err), zap.String("product_id", skuID))
		return logicv1.Availability{}, fmt.Errorf("call inventory service: %w", err)
	}
	span.SetAttributes(attribute.Bool("inventory_service.available", true))

	for _, a := range resp.GetAvailabilities() {
		if a.GetSkuId() == skuID {
			return logicv1.Availability{
				Status:             availabilityStatusString(a.GetStatus()),
				AvailableToPromise: a.GetAvailableToPromise(),
			}, nil
		}
	}
	// SKU not tracked by inventory (e.g. backfill incomplete): unknown, not error.
	return logicv1.Availability{Status: logicv1.AvailabilityUnknown}, nil
}

// availabilityStatusString maps the inventory.v1 enum to the detail-page string.
// Anything without a definite in/low/out-of-stock verdict reads as unknown.
func availabilityStatusString(s inventoryv1.AvailabilityStatus) string {
	switch s {
	case inventoryv1.AvailabilityStatus_AVAILABILITY_STATUS_IN_STOCK:
		return "in_stock"
	case inventoryv1.AvailabilityStatus_AVAILABILITY_STATUS_LOW_STOCK:
		return "low_stock"
	case inventoryv1.AvailabilityStatus_AVAILABILITY_STATUS_OUT_OF_STOCK:
		return "out_of_stock"
	case inventoryv1.AvailabilityStatus_AVAILABILITY_STATUS_UNKNOWN,
		inventoryv1.AvailabilityStatus_AVAILABILITY_STATUS_UNSPECIFIED:
		return logicv1.AvailabilityUnknown
	default:
		return logicv1.AvailabilityUnknown
	}
}
