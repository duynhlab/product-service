# product-service

> AI Agent context for understanding this repository

## 📋 Overview

Product catalog microservice. Manages product listings, search, and aggregated product details.

## 🏗️ Architecture

```
product-service/
├── cmd/main.go
├── config/config.go
├── db/migrations/sql/
├── internal/
│   ├── core/
│   │   ├── database.go
│   │   └── domain/
│   ├── logic/v1/service.go
│   └── web/v1/handler.go
├── middleware/
└── Dockerfile
```

## 🔌 API Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/products` | List products with filtering |
| `GET` | `/api/v1/products/:id` | Get product by ID |
| `GET` | `/api/v1/products/:id/details` | **Aggregated** product + reviews + related |
| `POST` | `/api/v1/products` | Create product (internal) |

## 📐 3-Layer Architecture

| Layer | Location | Responsibility |
|-------|----------|----------------|
| **Web** | `internal/web/v1/handler.go` | HTTP, validation, **aggregation APIs** |
| **Logic** | `internal/logic/v1/service.go` | Business rules (❌ NO SQL) |
| **Core** | `internal/core/` | Domain models, repositories |

**Aggregation:** `/products/:id/details` combines product + reviews (HTTP call to review-service) + related products.

## 🗄️ Database

| Component | Value |
|-----------|-------|
| **Cluster** | product-db (CloudNativePG) |
| **PostgreSQL** | 18 |
| **HA** | 3 instances (1 primary + 2 replicas) |
| **Pooler** | PgDog Standalone |
| **Endpoint** | `pgdog-product.product.svc.cluster.local:6432` |
| **Pool Mode** | Transaction |
| **Replication** | Asynchronous |

**CloudNativePG Services:**
- `product-db-rw` → Primary (writes)
- `product-db-r` → Replicas (reads, load balanced)

## 🚀 Graceful Shutdown

**VictoriaMetrics Pattern:**
1. `/ready` → 503 when shutting down
2. Drain delay (5s)
3. Sequential: HTTP → Database → Tracer

## 🔧 Tech Stack

| Component | Technology |
|-----------|------------|
| **Framework** | Gin |
| **Database** | PostgreSQL 18 via pgx/v5 |
| **Caching** | Redis (go-redis/v9) |
| **Tracing** | OpenTelemetry |

## 🛠️ Development

```bash
go mod download && go test ./... && go build ./cmd/main.go
```
