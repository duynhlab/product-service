# product-service

> AI Agent context for understanding this repository

## 📋 Overview

Product catalog microservice. Manages product listings, search, and aggregated product details with caching.

## 🏗️ Architecture Guidelines

### 3-Layer Architecture

| Layer | Location | Responsibility |
|-------|----------|----------------|
| **Web** | `internal/web/v1/handler.go` | HTTP, validation, **aggregation APIs** |
| **Logic** | `internal/logic/v1/service.go` | Business rules + cache-aside (❌ NO SQL) |
| **Core** | `internal/core/` | Domain, repositories, **cache/** |

### 3-Layer Coding Rules

**CRITICAL**: Strict layer boundaries. Violations will be rejected in code review.

#### Layer Boundaries

| Layer | Location | ALLOWED | FORBIDDEN |
|-------|----------|---------|-----------|
| **Web** | `internal/web/v1/` | HTTP handling, JSON binding, DTO mapping, call Logic, aggregation | SQL queries, direct DB access, business rules |
| **Logic** | `internal/logic/v1/` | Business rules, call repository interfaces, domain errors | SQL queries, `database.GetPool()`, HTTP handling, `*gin.Context` |
| **Core** | `internal/core/` | Domain models, repository implementations, SQL queries, DB connection | HTTP handling, business orchestration |

#### Dependency Direction

```
Web -> Logic -> Core (one-way only, never reverse)
```

- Web imports Logic and Core/domain
- Logic imports Core/domain and Core/repository interfaces
- Core imports nothing from Web or Logic

#### DO

- Put HTTP handlers, request validation, error-to-status mapping in `web/`
- Put business rules, orchestration, transaction logic in `logic/`
- Put SQL queries in `core/repository/` implementations
- Use repository interfaces (defined in `core/domain/`) for data access in Logic layer
- Use dependency injection (constructor parameters) for all service dependencies

#### DO NOT

- Write SQL or call `database.GetPool()` in Logic layer
- Import `gin` or handle HTTP in Logic layer
- Put business rules in Web layer (Web only translates and delegates)
- Call Logic functions directly from another service (use HTTP aggregation in Web layer)
- Skip the Logic layer (Web must not call Core/repository directly)

### Directory Structure

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

## 🛠️ Development Workflow

### Code Quality

**MANDATORY**: All code changes MUST pass lint before committing.

- Linter: `golangci-lint` v2+ with `.golangci.yml` config (60+ linters enabled)
- Zero tolerance: PRs with lint errors will NOT be merged
- CI enforces: `go-check` job runs lint on every PR

#### Commands (run in order)

```bash
go mod tidy              # Clean dependencies
go build ./...           # Verify compilation
go test ./...            # Run tests
golangci-lint run --timeout=10m  # Lint (MUST pass)
```

#### Pre-commit One-liner

```bash
go build ./... && go test ./... && golangci-lint run --timeout=10m
```

### Common Lint Fixes

- `perfsprint`: Use `errors.New()` instead of `fmt.Errorf()` when no format verbs
- `nosprintfhostport`: Use `net.JoinHostPort()` instead of `fmt.Sprintf("%s:%s", host, port)`
- `errcheck`: Always check error returns (or explicitly `_ = fn()`)
- `goconst`: Extract repeated string literals to constants
- `gocognit`: Extract helper functions to reduce complexity
- `noctx`: Use `http.NewRequestWithContext()` instead of `http.NewRequest()`

## 🔧 Tech Stack

| Component | Technology |
|-----------|------------|
| Framework | Gin |
| Database | PostgreSQL 18 via pgx/v5 |
| Caching | Valkey (go-redis/v9) |
| Tracing | OpenTelemetry |

## 🏗️ Infrastructure Details

### Database

| Component | Value |
|-----------|-------|
| **Cluster** | product-db (CloudNativePG) |
| **PostgreSQL** | 18 |
| **HA** | 3 instances (1 primary + 2 replicas) |
| **Pooler** | PgDog Standalone |
| **Endpoint** | `pgdog-product.product.svc.cluster.local:6432` |

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

### Graceful Shutdown

**VictoriaMetrics Pattern:**
1. `/ready` → 503 when shutting down
2. Drain delay (5s)
3. Sequential: HTTP → Database → Tracer

## 🔌 API Reference

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/products` | List products (cached) |
| `GET` | `/api/v1/products/:id` | Get product (cached) |
| `GET` | `/api/v1/products/:id/details` | **Aggregated** product + reviews |
| `POST` | `/api/v1/products` | Create product (invalidates cache) |
