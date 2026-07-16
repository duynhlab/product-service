package cache

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// fakeUniversal is an unsupported redis client type: redisotel's Instrument*
// funcs return an error for it, which drives instrumentClient's best-effort
// otel.Handle branches.
type fakeUniversal struct{ redis.UniversalClient }

// TestInstrumentClient_BestEffortOnError proves instrumentation failure is
// swallowed via otel.Handle (never panics, never returned) — the cache stays
// usable even if telemetry wiring fails.
func TestInstrumentClient_BestEffortOnError(t *testing.T) {
	instrumentClient(fakeUniversal{}) // must not panic
}

// TestValkeyCacheClient_HitMissMetric proves product.cache.gets records the
// bounded hit/miss outcome. Runs against miniredis; the client is fully
// instrumented (redisotel tracing + metrics) so this also exercises that the
// instrumentation hooks don't break normal Get/Set.
func TestValkeyCacheClient_HitMissMetric(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))

	mr := miniredis.RunT(t)
	c, err := NewValkeyCacheClient(mr.Addr(), "", 0)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer func() { _ = c.Close() }()

	ctx := context.Background()
	if err := c.Set(ctx, "k", []byte("v"), time.Minute); err != nil {
		t.Fatalf("set: %v", err)
	}
	if _, err := c.Get(ctx, "k"); err != nil { // hit
		t.Fatalf("get hit: %v", err)
	}
	if v, err := c.Get(ctx, "missing"); err != nil || v != nil { // miss
		t.Fatalf("get miss: v=%v err=%v", v, err)
	}
	mr.Close() // server gone → next Get is a connection error, not redis.Nil
	if _, err := c.Get(ctx, "k"); err == nil {
		t.Fatal("expected error Get after server close")
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	got := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "product.cache.gets" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("%s is %T, want Sum[int64]", m.Name, m.Data)
			}
			for _, dp := range sum.DataPoints {
				v, _ := dp.Attributes.Value(attribute.Key("result"))
				got[v.AsString()] = dp.Value
			}
		}
	}
	if got["hit"] != 1 || got["miss"] != 1 || got["error"] != 1 {
		t.Errorf("cache gets = %v, want hit=1 miss=1 error=1", got)
	}
}
