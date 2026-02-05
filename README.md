# product-service

Product catalog microservice with search, filtering, and Valkey caching.

## Features

- Product listings with filtering
- Full-text search
- Aggregated product details (with reviews)
- **Valkey caching** with stampede prevention

## API Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/products` | List products (cached) |
| `GET` | `/api/v1/products/:id` | Get product (cached) |
| `GET` | `/api/v1/products/:id/details` | Aggregated details |
| `POST` | `/api/v1/products` | Create product |

## Tech Stack

- Go + Gin framework
- PostgreSQL 18 (product-db cluster, HA)
- PgDog connection pooling
- Valkey (Redis-compatible) caching
- OpenTelemetry tracing

## Development

```bash
go mod download
go test ./...
go run cmd/main.go
```

## License

MIT
