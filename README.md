# product-service

Product catalog microservice with search, filtering, Valkey caching, and gRPC review aggregation.

## Features

- Product listings with filtering, sorting, and pagination
- Aggregated product details (product + stock + related products + reviews)
- **Valkey caching** (Cache-Aside) with stampede prevention for product reads
- **gRPC review aggregation**: fetches reviews from `review-service` over gRPC, soft-failing to an empty list when review is unavailable

## API Endpoints

All routes follow Variant A naming — single path for browser and in-cluster callers. See [homelab naming convention](https://github.com/duynhlab/homelab/blob/main/docs/api/api-naming-convention.md).

| Method | Path | Audience |
|--------|------|----------|
| `GET` | `/product/v1/public/products` | public (cached) |
| `GET` | `/product/v1/public/products/:id` | public (cached) |
| `GET` | `/product/v1/public/products/:id/details` | public (aggregates reviews via gRPC) |
| `POST` | `/product/v1/internal/products` | internal (admin/seed; in-cluster only) |

Operational endpoints: `GET /health`, `GET /ready` (503 while draining), `GET /metrics`.

## East-West gRPC (review aggregation)

`product-service` is a gRPC **client**. On `GET /product/v1/public/products/:id/details` it
calls `review.v1.ReviewService/GetProductReviews` on `review-service` over gRPC (the official
east-west transport, replacing the earlier HTTP call) and computes a rating summary
(total + average). If review-service is unavailable the call **soft-fails** and the response
returns an empty `reviews` list with a zeroed summary.

- Target: `REVIEW_GRPC_ADDR` (default `dns:///review.review.svc.cluster.local:9090`)
- Per-call deadline: 3s
- Dialed via `github.com/duynhlab/pkg/grpcx` (`grpcx.Dial`), which installs the otelgrpc client stats handler

## Observability

Built on `github.com/duynhlab/pkg/obsx`:

- **Metrics**: `obsx.SetupMetrics()` bridges OpenTelemetry metrics from the otelgrpc client
  stats handler into the default Prometheus registry, so gRPC **client** RED metrics
  (`rpc_client_*`) for the review calls surface on the **existing** `/metrics` endpoint — no
  separate metrics port. The platform `ServiceMonitor` scrapes the same endpoint that serves
  HTTP RED metrics (`request_duration_seconds`, `requests_in_flight`, request/response size).
- **Logging**: structured Zap logs; the logging middleware uses `obsx.TraceIDFromContext` so
  every log line carries the active span's trace ID for log↔trace correlation (falling back to
  header-derived/generated IDs only when no span is present).
- **Tracing**: OpenTelemetry traces exported to the OTel Collector (Tempo).
- **Profiling**: Pyroscope continuous profiling.

Middleware chain (order matters): **CORS → tracing → logging → metrics**.

## Tech Stack

- Go + Gin framework
- PostgreSQL via pgx/v5 (product-db cluster, HA)
- PgDog connection pooling
- Valkey (Redis-compatible) caching via go-redis/v9
- gRPC client (`pkg/grpcx`) for review aggregation
- OpenTelemetry tracing, OTel→Prometheus metrics, Pyroscope profiling (`pkg/obsx`)

## Configuration

Config is loaded from environment variables (with `.env` support for local dev) via `config/config.go`.

| Variable | Default | Purpose |
|----------|---------|---------|
| `SERVICE_NAME` | _(required)_ | Service name (e.g. `product`) |
| `PORT` | `8080` | HTTP listen port |
| `ENV` | `development` | `development`/`staging`/`production` |
| `REVIEW_GRPC_ADDR` | `dns:///review.review.svc.cluster.local:9090` | review-service gRPC target |
| `DB_HOST` / `DB_PORT` / `DB_NAME` / `DB_USER` / `DB_PASSWORD` | — / `5432` / — / — / — | PostgreSQL connection |
| `DB_SSLMODE` | `disable` | PostgreSQL SSL mode |
| `DB_POOL_MAX_CONNECTIONS` | `25` | pgx pool max connections |
| `CACHE_ENABLED` | `true` | Toggle Valkey cache |
| `CACHE_HOST` / `CACHE_PORT` | `valkey.cache-system.svc.cluster.local` / `6379` | Valkey endpoint |
| `CACHE_TTL_PRODUCT_LIST` | `5m` | Product-list cache TTL |
| `CACHE_TTL_PRODUCT_DETAIL` | `10m` | Single-product cache TTL |
| `TRACING_ENABLED` | `true` | Toggle OTel tracing |
| `OTEL_COLLECTOR_ENDPOINT` | `otel-collector-opentelemetry-collector.monitoring.svc.cluster.local:4318` | OTLP endpoint |
| `OTEL_SAMPLE_RATE` | `0.1` | Trace sample rate (0.0–1.0) |
| `PROFILING_ENABLED` | `true` | Toggle Pyroscope profiling |
| `PYROSCOPE_ENDPOINT` | `http://pyroscope.monitoring.svc.cluster.local:4040` | Pyroscope endpoint |
| `METRICS_ENABLED` | `true` | Toggle metrics setup |
| `LOG_LEVEL` / `LOG_FORMAT` | `info` / `json` | Logging |
| `SHUTDOWN_TIMEOUT` | `10` (s) | Graceful shutdown timeout |
| `READINESS_DRAIN_DELAY` | `5` (s, max 30) | Delay after failing readiness before shutdown |

## Development

### Prerequisites

- Go 1.26+
- [golangci-lint](https://golangci-lint.run/welcome/install/) v2+
- Docker (only for the integration tests — see [Testing](#testing))

### Local Development

```bash
# Install dependencies
go mod tidy
go mod download

# Build
go build ./...

# Unit tests (no Docker needed)
go test ./...

# Lint (must pass before PR merge)
golangci-lint run --timeout=10m

# Run locally (requires .env or env vars)
go run cmd/main.go
```

### Testing

Unit tests use the stdlib `testing` package with hand-written mocks and table-driven
subtests (no testify/gomock). The **repository layer** is covered by **integration tests**
against a real PostgreSQL via [testcontainers](https://golang.testcontainers.org/).

```bash
# Unit tests (no Docker)
go test ./...

# With coverage (as CI runs it)
go test -race -coverprofile=coverage.out ./...

# Integration tests — repository layer, real Postgres (needs a running Docker daemon)
go test -tags=integration ./internal/core/repository/...
```

Integration tests are build-tagged `//go:build integration`, so the default `go test ./...`
skips them and the service binary never links testcontainers. CI runs both jobs and merges
their coverage into SonarCloud (gate: ≥ 80% on new code).

### Pre-push Checklist

```bash
go build ./... && \
  go test ./... && \
  go test -tags=integration ./internal/core/repository/... && \
  golangci-lint run --timeout=10m
```

## License

MIT
