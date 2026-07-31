package v1

import (
	"context"
	"os"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	productv1 "github.com/duynhlab/pkg/proto/product/v1"
)

var testReader *sdkmetric.ManualReader

func TestMain(m *testing.M) {
	testReader = sdkmetric.NewManualReader()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(testReader)))
	os.Exit(m.Run())
}

// surfaceCalls reads product_stock_surface_calls_total for one rpc label.
// Counters accumulate across the test binary, so assertions are DELTAS.
func surfaceCalls(t *testing.T, rpc string) int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := testReader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	var total int64
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "product.stock.surface.calls.total" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				continue
			}
			for _, dp := range sum.DataPoints {
				if v, found := dp.Attributes.Value(attribute.Key("rpc")); found && v.AsString() == rpc {
					total += dp.Value
				}
			}
		}
	}
	return total
}

// The phase-4 removal gate is "two weeks of zero on this counter", so the
// counter must see EVERY touch of the surface being removed — successful,
// rejected, or malformed. A caller sending garbage is still a caller that will
// break when the RPC is dropped, which is exactly what the gate exists to know.
func TestStockSurfaceCounter_CountsEveryTouch(t *testing.T) {
	srv := NewServer(&stubStockManager{})

	before := surfaceCalls(t, "reserve_stock")
	// Malformed (no reservation_id) — must still count.
	_, err := srv.ReserveStock(context.Background(), &productv1.ReserveStockRequest{})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", status.Code(err))
	}
	// Well-formed.
	_, _ = srv.ReserveStock(context.Background(), &productv1.ReserveStockRequest{
		ReservationId: "42",
		Items:         []*productv1.StockItem{{ProductId: "1", Quantity: 1}},
	})
	if delta := surfaceCalls(t, "reserve_stock") - before; delta != 2 {
		t.Errorf("reserve_stock delta = %d, want 2 (malformed touches count too)", delta)
	}

	before = surfaceCalls(t, "release_stock")
	_, _ = srv.ReleaseStock(context.Background(), &productv1.ReleaseStockRequest{ReservationId: "42"})
	if delta := surfaceCalls(t, "release_stock") - before; delta != 1 {
		t.Errorf("release_stock delta = %d, want 1", delta)
	}
}

// The read RPCs are NOT part of the deprecated surface (GetProducts stays as
// checkout's subject-less fallback) — counting them would keep the gate red
// forever for traffic that is not going away.
func TestStockSurfaceCounter_IgnoresReads(t *testing.T) {
	srv := NewServer(&stubStockManager{})
	before := surfaceCalls(t, "reserve_stock") + surfaceCalls(t, "release_stock")
	_, _ = srv.GetProducts(context.Background(), &productv1.GetProductsRequest{ProductIds: []string{"1"}})
	if delta := (surfaceCalls(t, "reserve_stock") + surfaceCalls(t, "release_stock")) - before; delta != 0 {
		t.Errorf("read RPC incremented the surface counter by %d", delta)
	}
}
