package v1

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Business metrics for product, answering the on-call question that matters for
// the order saga: is the inventory reservation step succeeding, rejecting for
// lack of stock, or failing on infrastructure? → stock.reservations{result}.
//
// The instrument rides the global OTel MeterProvider that obsx.SetupObservability
// installs (RFC-0014 OTLP pipeline → collector → VictoriaMetrics). Before that
// setup the global provider is a no-op, so package-init here is safe. The name
// is OTel-style; the collector renders it as product_stock_reservations_total.
//
// Labels are bounded to enumerable domain values (RFC-0017 D-9): no ids, no
// free-form text, no quantities.
var (
	meter = otel.Meter("product-service")

	stockReservationCounter, _ = meter.Int64Counter("product.stock.reservations.total",
		metric.WithDescription("Order-saga inventory reservation attempts by outcome"))
)

// Stock reservation outcomes (bounded).
const (
	reservationReserved     = "reserved"
	reservationInsufficient = "insufficient_stock"
	reservationError        = "error"
)

// recordStockReservation counts one ReserveStock attempt with its outcome.
// Called exactly once per ReserveStock invocation: reserved on success,
// insufficient_stock on the not-enough-stock business rejection, error on any
// other (infrastructure) failure.
func recordStockReservation(ctx context.Context, result string) {
	stockReservationCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("result", result)))
}
