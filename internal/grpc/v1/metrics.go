package v1

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Deprecation telemetry for the stock surface RFC-0021 phase 4 removes.
//
// The contract-removal gate is "two weeks of zero on this counter" (see
// homelab docs/proposals/rfc/RFC-0021/cutover-rollback.md § Contract removal),
// so it must count EVERY touch of the doomed RPCs — successful, rejected by
// business rules, or malformed. A caller sending garbage is still a caller
// that breaks when the RPC is dropped, which is precisely what the gate exists
// to know about. Recorded at the top of each handler, before validation, for
// that reason.
//
// Deliberately separate from product.stock.reservations.total (the business
// outcome metric in logic/v1): that one answers "how are reservations going",
// this one answers "is anyone still here". They retire together in phase 4.
var (
	grpcMeter = otel.Meter("product-service")

	stockSurfaceCounter, _ = grpcMeter.Int64Counter("product.stock.surface.calls.total",
		metric.WithDescription("Calls to the deprecated product stock surface (RFC-0021 phase-4 removal gate; must read zero for two weeks)"))
)

// Deprecated stock-surface RPCs (bounded label set).
const (
	rpcReserveStock = "reserve_stock"
	rpcReleaseStock = "release_stock"
)

// recordStockSurfaceCall counts one touch of the deprecated surface.
func recordStockSurfaceCall(ctx context.Context, rpc string) {
	stockSurfaceCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("rpc", rpc)))
}
