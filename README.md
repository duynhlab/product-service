# product-service

Product catalog microservice with search, filtering, and Valkey caching.

## Features

- Product listings with filtering
- Full-text search
- Aggregated product details (with reviews)
- **Valkey caching** with stampede prevention

## API Endpoints

> **Browser callers** hit `https://gateway.duynhne.me/product/v1/public/products/…`; Kong rewrites to the cluster paths below. `POST /api/v1/products` stays internal (admin/seed only — not on the gateway). See [homelab naming convention](https://github.com/duynhlab/homelab/blob/main/docs/api/api-naming-convention.md).

| Method | Cluster path | Edge path (via gateway) |
|--------|--------------|-------------------------|
| `GET` | `/api/v1/products` | `/product/v1/public/products` |
| `GET` | `/api/v1/products/:id` | `/product/v1/public/products/:id` |
| `GET` | `/api/v1/products/:id/details` | `/product/v1/public/products/:id/details` |
| `POST` | `/api/v1/products` | *(internal — not on gateway)* |

## Tech Stack

- Go + Gin framework
- PostgreSQL 18 (product-db cluster, HA)
- PgDog connection pooling
- Valkey (Redis-compatible) caching
- OpenTelemetry tracing

## Development

### Prerequisites

- Go 1.25+
- [golangci-lint](https://golangci-lint.run/welcome/install/) v2+

### Local Development

```bash
# Install dependencies
go mod tidy
go mod download

# Build
go build ./...

# Test
go test ./...

# Lint (must pass before PR merge)
golangci-lint run --timeout=10m

# Run locally (requires .env or env vars)
go run cmd/main.go
```

### Pre-push Checklist

```bash
go build ./... && go test ./... && golangci-lint run --timeout=10m
```

## License

MIT
