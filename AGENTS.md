# product-service

> AI Agent context for understanding this repository

## 📋 Overview

Product catalog microservice. Manages product listings, search, and aggregated product details with caching.

## 🏗️ Architecture

```
product-service/
├── cmd/main.go
├── config/config.go
├── db/migrations/sql/
├── internal/
│   ├── core/
│   │   ├── cache/           # Valkey caching layer
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
| `GET` | `/api/v1/products` | List products (cached) |
| `GET` | `/api/v1/products/:id` | Get product (cached) |
| `GET` | `/api/v1/products/:id/details` | **Aggregated** product + reviews |
| `POST` | `/api/v1/products` | Create product (invalidates cache) |

## 📐 3-Layer Architecture

| Layer | Location | Responsibility |
|-------|----------|----------------|
| **Web** | `internal/web/v1/handler.go` | HTTP, validation, **aggregation APIs** |
| **Logic** | `internal/logic/v1/service.go` | Business rules + cache-aside (❌ NO SQL) |
| **Core** | `internal/core/` | Domain, repositories, **cache/** |

## 🗄️ Database

| Component | Value |
|-----------|-------|
| **Cluster** | product-db (CloudNativePG) |
| **PostgreSQL** | 18 |
| **HA** | 3 instances (1 primary + 2 replicas) |
| **Pooler** | PgDog Standalone |
| **Endpoint** | `pgdog-product.product.svc.cluster.local:6432` |

## 🚀 Production Patterns

### Graceful Shutdown
VictoriaMetrics pattern: `/ready` → 503 → drain delay → sequential cleanup

### Caching

| Component | Value |
|-----------|-------|
| **Cache** | Valkey (Redis-compatible) |
| **Pattern** | Cache-Aside + Stampede Prevention |
| **Endpoint** | `valkey.cache-system.svc.cluster.local:6379` |
| **TTL List** | 5m |
| **TTL Detail** | 10m |

**Cache Keys:**
- `product:{id}` - single product
- `product:list:{category}:{search}:{sort}:{order}:{page}:{limit}` - product list

**Stampede Prevention:** Distributed locking (SETNX) ensures only 1 request hits DB on cache miss.

## 🔧 Tech Stack

| Component | Technology |
|-----------|------------|
| Framework | Gin |
| Database | PostgreSQL 18 via pgx/v5 |
| Caching | Valkey (go-redis/v9) |
| Tracing | OpenTelemetry |
