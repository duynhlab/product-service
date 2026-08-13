# product-service

The product catalog, and the platform's price authority: what a SKU costs at
checkout time.

## Responsibilities

- **Owns:** products, categories, and current prices.
- **Does not own:** stock and availability (`inventory-service`) or reviews
  (`review-service`). Both appear on the product detail page as read-only
  enrichments this service fetches and can live without. Product's own stock
  surface was not deprecated — it was removed, schema included.

## Tech

| Area | Technology |
|------|------------|
| Runtime | Go 1.26 |
| Transports | HTTP (public catalog, one internal create) · gRPC server (prices) · gRPC client (reviews, availability) |
| Data | PostgreSQL · Valkey for cache-aside reads |
| Platform libraries | `dbx`, `grpcx`, `httpx`, `logger/zapx`, `migratex`, `obsx`, `proto` |

## API

- **Canonical contract:** [`homelab/docs/api/product.md`](https://github.com/duynhlab/homelab/blob/main/docs/api/product.md)
- **Shared conventions:** [`homelab/docs/api/api.md`](https://github.com/duynhlab/homelab/blob/main/docs/api/api.md)
- **Surfaces:** public HTTP for browsing and a detail page, one cluster-only
  internal create, and `product.v1.ProductService` east-west — checkout asks it
  for current prices. It is also a gRPC **client**: the detail page fetches
  reviews and availability from their owners. HTTP `:8080` also carries
  `/health` and `/ready`.

Routes, payloads, RPC semantics and error codes live in the contract, so there
is one place to change when they change.

## Run locally

Prefer the homelab **local-stack** — the detail page reaches two other services,
and the cache is only interesting with real traffic.

Standalone you need PostgreSQL through the `DB_*` variables; Valkey is optional
and the service degrades to database reads without it:

```bash
go run cmd/main.go migrate   # apply schema migrations
go run cmd/main.go seed      # demo catalog — development only, refuses production
go run cmd/main.go           # serve HTTP :8080 + gRPC :9090
```

## Verify

The commands CI runs, so a green local run means a green pipeline:

```bash
go build ./...
go test -race ./...
go test -tags=integration ./internal/core/repository/...   # needs Docker (testcontainers)
golangci-lint run
```

## Docs

- [Canonical contract](https://github.com/duynhlab/homelab/blob/main/docs/api/product.md)
- [local-stack guide](https://github.com/duynhlab/homelab/blob/main/local-stack/README.md)

## License

MIT
