package cache

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Cache metrics (RFC-0017 W2). The hit/miss/error split answers "is the cache
// earning its keep?" — a signal redisotel's command instrumentation does not
// give (it sees GETs, not their semantic hit/miss). Instruments ride the global
// OTel MeterProvider obsx installs; the collector renders the counter as
// product_cache_gets_total{result}. Bounded enum label (RFC-0017 D-9).
var (
	cacheMeter = otel.Meter("product-service")

	cacheGets, _ = cacheMeter.Int64Counter("product.cache.gets",
		metric.WithDescription("Product cache Get outcomes (hit/miss/error)"))
)

// Cache Get outcomes (bounded).
const (
	cacheHit   = "hit"
	cacheMiss  = "miss"
	cacheError = "error"
)

func recordCacheGet(ctx context.Context, result string) {
	cacheGets.Add(ctx, 1, metric.WithAttributes(attribute.String("result", result)))
}
