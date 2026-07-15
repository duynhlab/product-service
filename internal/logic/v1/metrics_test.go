package v1

import (
	"context"
	"errors"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/duynhlab/product-service/internal/core/domain"
)

// collectCounter reads name into an attribute→value map keyed by one label.
func collectCounter(t *testing.T, reader sdkmetric.Reader, name, label string) map[string]int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	out := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("%s is %T, want Sum[int64]", name, m.Data)
			}
			for _, dp := range sum.DataPoints {
				v, _ := dp.Attributes.Value(attribute.Key(label))
				out[v.AsString()] = dp.Value
			}
		}
	}
	return out
}

// TestStockReservationMetric drives the three ReserveStock outcomes and asserts
// each carries the right bounded label, then re-drives success to prove the
// counter is cumulative and each call increments exactly once. All on one
// MeterProvider — the OTel global delegate is first-wins, so a single provider
// install per test binary is required (see the package-init instrument).
func TestStockReservationMetric(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))

	ctx := context.Background()
	items := []domain.ReservationItem{{ProductID: "1", Quantity: 1}}
	const metricName = "product.stock.reservations.total"

	// Success → reserved, exactly once.
	if err := NewProductService(&stubProductRepo{}, nil, nil).
		ReserveStock(ctx, "order-1", items); err != nil {
		t.Fatalf("reserve success: %v", err)
	}
	got := collectCounter(t, reader, metricName, "result")
	assertCount(t, got, "reserved", 1)
	assertCount(t, got, "insufficient_stock", 0)
	assertCount(t, got, "error", 0)

	// Not-enough-stock business rejection → insufficient_stock.
	if err := NewProductService(&stubProductRepo{reserveErr: domain.ErrInsufficientStock}, nil, nil).
		ReserveStock(ctx, "order-2", items); !errors.Is(err, domain.ErrInsufficientStock) {
		t.Fatalf("reserve insufficient: got %v", err)
	}
	got = collectCounter(t, reader, metricName, "result")
	assertCount(t, got, "insufficient_stock", 1)
	assertCount(t, got, "reserved", 1) // unchanged

	// Infrastructure failure → error.
	if err := NewProductService(&stubProductRepo{reserveErr: errors.New("db down")}, nil, nil).
		ReserveStock(ctx, "order-3", items); err == nil {
		t.Fatal("reserve error: got nil, want an error")
	}
	got = collectCounter(t, reader, metricName, "result")
	assertCount(t, got, "error", 1)

	// Cumulative + exactly-once: a second success adds exactly one to reserved.
	if err := NewProductService(&stubProductRepo{}, nil, nil).
		ReserveStock(ctx, "order-4", items); err != nil {
		t.Fatalf("reserve success 2: %v", err)
	}
	got = collectCounter(t, reader, metricName, "result")
	assertCount(t, got, "reserved", 2)
	assertCount(t, got, "insufficient_stock", 1)
	assertCount(t, got, "error", 1)
}

func assertCount(t *testing.T, got map[string]int64, label string, want int64) {
	t.Helper()
	if got[label] != want {
		t.Errorf("stock.reservations{result=%s} = %d, want %d", label, got[label], want)
	}
}
