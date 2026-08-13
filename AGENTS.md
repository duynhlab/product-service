# AGENTS.md

Agent-focused guide for `product-service`. Keep changes minimal, verified
against the code, and consistent with existing patterns.

## Authority and scope

This repository implements the service. It does **not** define the contract.

- **Canonical contract:** [`homelab/docs/api/product.md`](https://github.com/duynhlab/homelab/blob/main/docs/api/product.md)
- **Shared API rules:** [`homelab/docs/api/api.md`](https://github.com/duynhlab/homelab/blob/main/docs/api/api.md)

Implement against those files. When this repository and the contract disagree,
**stop and classify the mismatch** using
[Resolving a mismatch](https://github.com/duynhlab/homelab/blob/main/docs/api/README.md#resolving-a-mismatch)
before changing either side. One class — an implementation that violates the
intended contract — **blocks the release tag**.

No route, RPC, payload or error inventory belongs in this file. Manifests,
gateway routing, NetworkPolicy, database topology and platform observability
belong to [duynhlab/homelab](https://github.com/duynhlab/homelab).

## Contribution workflow

- Never commit or push to `main`. Branch first, then open a PR.
- Branch names use conventional prefixes: `feat/`, `fix/`, `docs/`, `chore/`,
  `refactor/`, `test/`.
- Commit subjects: imperative mood, capitalised, ≤ 50 characters, no trailing
  period. Add a body wrapped at 72 characters when the change is non-trivial.
- Do not add attribution trailers (`Signed-off-by`, `Co-authored-by`,
  `Generated-by`, etc.), GitHub issue references, or `@`-mentions in commit
  messages. Put issue links in the PR description.
- One logical change per PR. PRs are squash-merged and CI must be green.

## Build, test, lint

These are the commands CI runs, so a green local run means a green pipeline.

```bash
go build ./...
go vet ./...
go test -race ./...
go test -tags=integration ./internal/core/repository/...   # needs Docker (testcontainers)
golangci-lint run
```

Sonar new-code coverage must be ≥80%; `**/cmd/**`, `**/db/migrations/**` and
`**/core/repository/**` are excluded, everything else counts.

Local development against an unreleased `pkg`: `pkg` is one module per package,
so its root has no `go.mod` and a single `replace github.com/duynhlab/pkg` can no
longer resolve. Use one commented `replace` line per module — the trailer in
`go.mod` shows the shape, and
[`docs/api/pkg.md`](https://github.com/duynhlab/homelab/blob/main/docs/api/pkg.md)
explains why.

## Architecture boundaries

**3-layer, dependencies flow one way only: transport → logic → core.**

This service is **both** a gRPC server and a gRPC client, and confusing the two
has already caused a wrong claim in this file's history:

- **Server** — `internal/grpc/v1/` exposes the price RPC that checkout depends on.
- **Clients** — `internal/web/v1/` holds the review and inventory clients. gRPC
  transport stays in the web layer; logic sees only narrow interfaces, so the
  detail aggregation can be tested without a network.
- **Logic** — `internal/logic/v1/` holds the rules and the detail assembly.
- **Core** — `internal/core/` owns the domain model, the repository, and the
  cache.

Observability is wired once through `github.com/duynhlab/pkg/obsx`; the pool comes
from `github.com/duynhlab/pkg/dbx`; the gRPC server is built by
`github.com/duynhlab/pkg/grpcx`; responses use the shared
`github.com/duynhlab/pkg/httpx` envelope.

## Invariants

Rules an implementer can violate at the keyboard.

- **The price RPC deliberately bypasses the cache.** Product is the price
  authority at checkout time, so the answer must be the current row, not a
  possibly-stale copy. Adding it to the cache-aside path would be the single most
  expensive "optimisation" available here.
- **Money converts from float dollars to integer minor units exactly once, at the
  gRPC boundary.** The catalog has no per-product currency column — this is a
  single-currency platform, and the constant says so.
- **The price batch is capped.** The RPC is callable by any in-network workload
  with no gateway in front, so the cap is what stops an oversized query from
  being a denial-of-service amplifier.
- **The cache fails open.** A cache error is treated as a miss and the read
  degrades to the database, so a Valkey outage slows the catalog rather than
  breaking it.
- **The stampede lock is released by compare-and-delete**, so a fetch that
  overran its lock TTL cannot delete a successor's lock. A plain delete
  reintroduces exactly the stampede the lock prevents.
- **List cache keys are hashed over the canonical filter tuple, and the prefix is
  preserved.** Hashing stops a free-text search containing the separator from
  colliding with a different filter set and serving the wrong results; keeping the
  prefix is what lets invalidation still match every variant.
- **Invalidation covers the list cache only.** Single-product entries have no
  invalidation hook and are stale until their TTL. That is a known, documented
  boundary — do not quietly widen it without updating the contract.
- **Availability is inventory's answer, always wired, and soft-fails.** The flag
  that used to gate it was deliberately deleted: a flag whose off position
  silently removes information from a customer-facing page is worse than no flag,
  because "turned off" and "inventory is down" become the same code path.
- **Unknown availability is omitted, never zeroed.** A zero would be
  indistinguishable from genuinely no stock. The storefront treats unknown as
  purchasable on purpose — checkout is where availability is enforced, and it
  fails closed there.
- **There is exactly one availability answer on the detail page.** Never re-add a
  stock block alongside it: two answers with one of them stale is how a caller
  ends up trusting the wrong one.
- **Reviews soft-fail to an empty list and a zero summary** — a review outage must
  not take down a product page.
- **Sort input never reaches `ORDER BY` raw** — it goes through an allowlist with
  a safe fallback.
- **Pooler-safe database settings live in `pkg/dbx`.** One DSN serves the app and
  migrations so both connect identically.
- **`seed` is development-only** and refuses production, and seeds live outside
  the migration chain on purpose — `migrate` runs everywhere, including
  production, and must never insert demo products.
- **The one down migration is hand-applied only.** It is committed for the record
  but unreachable at runtime, because the migrate CLI cannot read an `embed.FS`
  compiled into another binary. It restores shape, not data.
- **Graceful-shutdown ordering is load-bearing:** fail readiness → drain delay →
  HTTP → gRPC `GracefulStop` → cache → pool → OTel last.
- **Metric labels are a bounded enum.** The cache hit/miss counter exists because
  the Redis client's own instrumentation sees GETs, not their semantic outcome.
- **Probe suppression is one contract across spans, RED metrics and logs**, driven
  by the same skip list; a **failing** probe is still recorded. 4xx logs at warn,
  5xx at error.
- **No service-level CORS.** A hardcoded localhost allowlist once turned every
  browser call into a 403 on the cluster. The edge owns CORS.

## Repository map

- `cmd/main.go` — wiring, subcommand dispatch, HTTP + gRPC bootstrap, graceful shutdown
- `config/config.go` — env config and validation
- `internal/web/v1/` — HTTP handlers plus the review and inventory gRPC clients
- `internal/grpc/v1/` — the `ProductService` server
- `internal/logic/v1/` — business rules and the detail-page assembly
- `internal/core/domain/` — models, repository interface, sentinel errors
- `internal/core/repository/` — Postgres implementation
- `internal/core/cache/` — Valkey client, cache-aside with stampede lock, cache metrics
- `db/migrations/` — golang-migrate SQL, embedded
- `db/seed/` — development-only demo seed, embedded separately from migrations
- `middleware/` — tracing and logging only

## Gotchas

- Kyverno admission rejects a workload image tagged `:latest` or unpinned. The
  published image is `ghcr.io/duynhlab/product-service/product-service:<tag>` —
  the repository path repeats, and the tag carries no `v` prefix. There is no
  separate migration image; the init container reuses the app image with
  `args: ["migrate"]`.
- Metrics leave over OTLP. There is no `/metrics` endpoint and nothing scrapes
  this service.
- A dead `insufficient stock` sentinel error is still mapped in the HTTP handler
  and can no longer be returned. It is harmless, but do not treat it as evidence
  that this service still knows about stock.

## API change synchronization

An API change is not done when the code compiles.

- The contract in homelab and this repository move **together** — same change,
  and either the same PR pair or an immediate follow-up.
- Behaviour that is designed but not deployed is marked **`Planned`** in the
  contract; it is never described as current.
- A material mismatch between the contract and this implementation **blocks the
  release tag** until it is reconciled or explicitly accepted.
