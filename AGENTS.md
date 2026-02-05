# product-service

> AI Agent context for understanding this repository

## 📋 Overview

Product catalog microservice for the monitoring platform.

## 🏗️ Architecture

```
product-service/
├── cmd/
│   └── main.go              # Entry point, graceful shutdown
├── config/
│   └── config.go            # Environment-based configuration
├── db/migrations/
│   └── sql/                  # Flyway SQL migrations
├── internal/
│   ├── core/
│   │   ├── database.go      # PostgreSQL connection pool (pgx)
│   │   └── domain/          # Domain models
│   ├── logic/v1/
│   │   ├── service.go       # Business logic layer
│   │   └── errors.go        # Domain errors
│   └── web/v1/
│       └── handler.go       # HTTP handlers (Gin)
├── middleware/
│   ├── logging.go           # Request logging
│   ├── prometheus.go        # Metrics
│   └── tracing.go           # OpenTelemetry
└── Dockerfile
```

## 🔌 API Endpoints

GET /api/v1/products, GET /api/v1/products/:id, POST /api/v1/products

## 🔧 Tech Stack

| Component | Technology |
|-----------|------------|
| **Framework** | Gin v1.11 |
| **Database** | PostgreSQL via pgx/v5 |
| **Logging** | Zerolog (from `github.com/duynhne/pkg`) |
| **Tracing** | OpenTelemetry with OTLP exporter |
| **Metrics** | Prometheus client |

## 🛠️ Development

```bash
go mod download
go test -v ./...
go build -o product-service ./cmd/main.go
```

## 🚀 CI/CD

Uses reusable GitHub Actions from [shared-workflows](https://github.com/duyhenryer/shared-workflows)
