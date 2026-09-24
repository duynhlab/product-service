package main

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"

	"github.com/duynhlab/pkg/authmw"
	"github.com/duynhlab/pkg/grpcx"
	"github.com/duynhlab/pkg/httpmw"
	"github.com/duynhlab/pkg/logger/slogx"
	"github.com/duynhlab/pkg/migratex"
	"github.com/duynhlab/pkg/obsx"
	productv1 "github.com/duynhlab/pkg/proto/product/v1"
	"github.com/duynhlab/product-service/config"
	migrations "github.com/duynhlab/product-service/db/migrations"
	seed "github.com/duynhlab/product-service/db/seed"
	database "github.com/duynhlab/product-service/internal/core"
	"github.com/duynhlab/product-service/internal/core/cache"
	"github.com/duynhlab/product-service/internal/core/repository"
	grpcv1 "github.com/duynhlab/product-service/internal/grpc/v1"
	logicv1 "github.com/duynhlab/product-service/internal/logic/v1"
	v1 "github.com/duynhlab/product-service/internal/web/v1"
)

// startGRPC starts the internal gRPC server on cfg.GRPC.Port, serving
// ProductService (checkout's price reads; the order saga's stock steps were
// removed in RFC-0021 phase 4) alongside the HTTP listener. It uses the shared grpcx bootstrap (OpenTelemetry,
// health, reflection) and returns nil only if the listener can't bind.
func startGRPC(cfg *config.Config, logger *slogx.Logger, svc *logicv1.ProductService) *grpc.Server {
	ctx := context.Background()
	lc := net.ListenConfig{}
	lis, err := lc.Listen(context.Background(), "tcp", ":"+cfg.GRPC.Port)
	if err != nil {
		logger.Error(ctx, "Failed to listen for gRPC", slog.String("port", cfg.GRPC.Port), slogx.Err(err))
		return nil
	}

	grpcSrv, _ := grpcx.NewServer(logger.Slog())
	productv1.RegisterProductServiceServer(grpcSrv, grpcv1.NewServer(svc))

	go func() {
		logger.Info(ctx, "Starting gRPC server", slog.String("port", cfg.GRPC.Port))
		if err := grpcSrv.Serve(lis); err != nil {
			logger.Error(ctx, "gRPC server error", slogx.Err(err))
		}
	}()

	return grpcSrv
}

// runSubcommand handles the `migrate` and `seed` subcommands. It returns true
// when a subcommand was recognised and executed (the caller then exits), or
// false to fall through to serving the app.
//
// `migrate` applies the versioned schema migrations and runs in every
// environment (init container, direct DB host). `seed` applies DEV-ONLY demo
// data and is invoked explicitly — never by `migrate` or the serve path — so
// production databases are never seeded.
func runSubcommand(cmd string, cfg *config.Config, logger *slogx.Logger) bool {
	ctx := context.Background()
	switch cmd {
	case "migrate":
		if err := migratex.Run(migrations.FS, "sql", cfg.Database.BuildDSN()); err != nil {
			logger.Fatal(ctx, "Schema migration failed", slogx.Err(err))
		}
		logger.Info(ctx, "Schema migrations applied")
		return true
	case "seed":
		// Demo data is DEV-ONLY; refuse to seed a production database.
		if cfg.IsProduction() {
			logger.Fatal(ctx, "seed refused in production — demo data is dev-only")
		}
		if err := applySeed(context.Background(), cfg); err != nil {
			logger.Fatal(ctx, "Demo seed failed", slogx.Err(err))
		}
		logger.Info(ctx, "Demo seed data applied")
		return true
	default:
		return false
	}
}

// applySeed executes the embedded dev-only seed SQL directly against the database.
// It does NOT use golang-migrate: seeds are idempotent (ON CONFLICT) and must not
// share the schema_migrations version table with the schema migrations. Simple
// query protocol lets each multi-statement seed file run in one Exec.
func applySeed(ctx context.Context, cfg *config.Config) error {
	poolCfg, err := pgxpool.ParseConfig(cfg.Database.BuildDSN())
	if err != nil {
		return fmt.Errorf("parse seed DSN: %w", err)
	}
	poolCfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return fmt.Errorf("connect for seed: %w", err)
	}
	defer pool.Close()

	entries, err := fs.ReadDir(seed.FS, "sql")
	if err != nil {
		return fmt.Errorf("read seed dir: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".up.sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		b, readErr := fs.ReadFile(seed.FS, "sql/"+name)
		if readErr != nil {
			return fmt.Errorf("read seed %s: %w", name, readErr)
		}
		if _, execErr := pool.Exec(ctx, string(b)); execErr != nil {
			return fmt.Errorf("apply seed %s: %w", name, execErr)
		}
	}
	return nil
}

//nolint:gocognit,funlen // main orchestrates startup/shutdown; single func is intentional
func main() {
	ctx := context.Background()
	// Load configuration from environment variables (with .env file support for local dev)
	cfg := config.Load()

	// Initialize structured logger
	logger := slogx.New(slogx.Config{Level: os.Getenv("LOG_LEVEL")})
	slogx.SetDefault(logger)

	// Subcommands (`migrate`, `seed`) run an embedded SQL set (init container,
	// against the direct DB host) and exit; no args serves the app.
	if len(os.Args) > 1 && runSubcommand(os.Args[1], cfg, logger) {
		return
	}

	// Validate runs after the migrate check so migrations need only DB config.
	if err := cfg.Validate(); err != nil {
		panic("Configuration validation failed: " + err.Error())
	}

	logger.Info(ctx, "Service starting",
		slog.String("service.version", cfg.Service.Version),
		slog.String("deployment.environment.name", cfg.Service.Env),
		slog.String("port", cfg.Service.Port),
	)

	// RFC-0014: single OTel wiring point — traces per TRACING_ENABLED, OTLP
	// metrics (the only pipeline since the P3 cutover; OTEL_METRICS_ENABLED
	// defaults on, =false is a kill switch), logs behind OTEL_LOGS_ENABLED.
	// The config is built once so the tracer scope name and the startup log
	// reflect the values obsx actually uses.
	otelCfg := obsx.ConfigFromEnv()
	var tp interface{ Shutdown(context.Context) error }
	obs, err := obsx.SetupObservability(context.Background(), otelCfg)
	if err != nil {
		logger.Warn(ctx, "Failed to initialize OpenTelemetry", slogx.Err(err))
	} else {
		tp = obs
		// The facade reaches OTLP through the global logger provider obsx
		// installed; rebuilding it only wires Flush, so a Fatal record is
		// exported before the process exits.
		logger = slogx.New(slogx.Config{Level: os.Getenv("LOG_LEVEL"), Flush: obs.ForceFlush})
		slogx.SetDefault(logger)
		logger.Info(ctx, "OpenTelemetry initialized",
			slog.Bool("traces", obs.Enabled().Traces),
			slog.Bool("otlp_metrics", obs.Enabled().Metrics),
			slog.Bool("otlp_logs", obs.Enabled().Logs),
			slog.String("endpoint", otelCfg.Endpoint),
			slog.Float64("sample_rate", otelCfg.SampleRate),
		)
	}

	// Initialize Pyroscope profiling
	if cfg.Profiling.Enabled {
		stopProfiling, err := obsx.SetupProfiling()
		if err != nil {
			logger.Warn(ctx, "Failed to initialize profiling", slogx.Err(err))
		} else {
			logger.Info(ctx, "Profiling initialized",
				slog.String("endpoint", cfg.Profiling.Endpoint),
			)
			defer func() { _ = stopProfiling(context.Background()) }()
		}
	} else {
		logger.Info(ctx, "Profiling disabled (PROFILING_ENABLED=false)")
	}

	// Initialize database connection pool (pgx)
	pool, err := database.Connect(context.Background(), cfg.Database)
	if err != nil {
		logger.Fatal(ctx, "Failed to connect to database", slogx.Err(err))
	}
	defer pool.Close()
	logger.Info(ctx, "Database connection pool established")

	// Initialize repositories (Core layer)
	productRepo := repository.NewPostgresProductRepository(pool)
	logger.Info(ctx, "Product repository initialized")

	// Initialize cache client (Core layer) - optional, can be nil if disabled
	var productCache *cache.ProductCache
	if cfg.Cache.Enabled {
		cacheAddr := cfg.Cache.Host + ":" + cfg.Cache.Port
		cacheClient, err := cache.NewValkeyCacheClient(cacheAddr, cfg.Cache.Password, cfg.Cache.DB)
		if err != nil {
			logger.Warn(ctx, "Failed to initialize cache client, continuing without cache",
				slogx.Err(err),
				slog.String("cache_addr", cacheAddr),
			)
		} else {
			productCache = cache.NewProductCache(cacheClient, cfg.Cache.TTLProductList, cfg.Cache.TTLProductDetail)
			logger.Info(ctx, "Cache client initialized",
				slog.String("cache_addr", cacheAddr),
				slog.Duration("ttl_list", cfg.Cache.TTLProductList),
				slog.Duration("ttl_detail", cfg.Cache.TTLProductDetail),
			)
			defer func() {
				if err := cacheClient.Close(); err != nil {
					logger.Error(ctx, "Failed to close cache client", slogx.Err(err))
				} else {
					logger.Info(ctx, "Cache client closed")
				}
			}()
		}
	} else {
		logger.Info(ctx, "Cache disabled (CACHE_ENABLED=false)")
	}

	// Initialize review service gRPC client for aggregation in product details endpoint
	reviewConn, err := grpcx.Dial(cfg.ReviewGRPCAddr)
	if err != nil {
		logger.Error(ctx, "Failed to dial review gRPC", slog.String("addr", cfg.ReviewGRPCAddr), slogx.Err(err))
		return
	}
	defer func() { _ = reviewConn.Close() }()
	reviewClient := v1.NewReviewClient(reviewConn)
	logger.Info(ctx, "Review gRPC client initialized", slog.String("review_grpc_addr", cfg.ReviewGRPCAddr))

	// Initialize services (Logic layer) with dependency injection
	productService := logicv1.NewProductService(productRepo, productCache, reviewClient)

	// Inventory availability for GetProductDetails. NOT optional any more.
	//
	// This used to be gated behind PRODUCT_AVAILABILITY_SOURCE=product|inventory,
	// defaulting to `product`. That default is now a trap: since 1.8.0 the `product`
	// position means the detail page carries NO availability at all — the frozen
	// stock block it used to fall back to is gone, and 1.10.0 dropped the column
	// underneath it. A flag whose off position silently removes information from a
	// customer-facing page is worse than no flag, and it is the third time in this
	// migration that a default pointed at an authority that had already been removed.
	//
	// The dial still soft-fails: an unreachable inventory resolves each request to
	// {"status":"unknown"}, which the SPA renders as unknown and still allows an
	// add-to-cart, because checkout is where availability is enforced (fail-closed,
	// retryable 503). So there is nothing left for a flag to buy — "turn it off" and
	// "inventory is down" are the same code path, and the second one is already
	// handled.
	invConn, ierr := grpcx.Dial(cfg.InventoryGRPCAddr)
	if ierr != nil {
		logger.Error(ctx, "inventory availability unavailable: dial failed — /details will report status=unknown",
			slog.String("addr", cfg.InventoryGRPCAddr), slogx.Err(ierr))
	} else {
		defer func() { _ = invConn.Close() }()
		productService = productService.WithAvailability(v1.NewInventoryClient(invConn))
		logger.Info(ctx, "Inventory availability enabled",
			slog.String("inventory_grpc_addr", cfg.InventoryGRPCAddr))
	}
	logger.Info(ctx, "Product service initialized")

	// Start the internal gRPC server (east-west: order-fulfillment saga).
	grpcSrv := startGRPC(cfg, logger, productService)

	// Initialize Web handler with dependency injection
	productHandler := v1.NewProductHandler(productService)
	logger.Info(ctx, "Web handlers configured")

	// gin.New, not gin.Default: Default installs gin's own logger and
	// recovery, which print the raw path and client address past the facade.
	r := gin.New()

	var isShuttingDown atomic.Bool

	// CORS is handled at the Kong edge (global cors plugin in both stacks),
	// same as the other services — no service-level CORS middleware.

	// Tracing middleware (must be first for context propagation)
	r.Use(httpmw.Tracing(otelCfg.ServiceName))

	// Logging middleware
	r.Use(httpmw.Logging(logger.Slog()))
	r.Use(httpmw.Recovery(logger.Slog()))

	// Health check
	r.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	// Readiness check
	// Returns 503 once shutdown has started, to drain traffic before HTTP shutdown.
	r.GET("/ready", func(c *gin.Context) {
		if isShuttingDown.Load() {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "shutting_down"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// Product v1 routes — Variant A edge naming (see api-naming-convention.md)
	r.GET("/product/v1/public/products", productHandler.ListProducts)
	r.GET("/product/v1/public/products/:id", productHandler.GetProduct)
	r.GET("/product/v1/public/products/:id/details", productHandler.GetProductDetails) // Aggregation endpoint
	// RETIRED (RFC-0023 slice B / ADR-047): POST /product/v1/internal/products is
	// gone. It was the platform's last unauthenticated write path — no token, no
	// role, no actor, no audit, fenced by NetworkPolicy alone — and ADR-047's
	// boundary rule says such routes are REPLACED, not promoted, once a governed
	// equivalent exists. The protected create below is that equivalent, and it is
	// strictly better: staff-realm token, backoffice_admin, the actor from the
	// token, an audit row in the same transaction, and DRAFT instead of straight
	// into the public catalog.
	//
	// Verified callerless before deleting: db/seed loads SQL
	// (db/seed/sql/000001_demo_products.up.sql), nothing in the fleet or in
	// local-stack posts to it, and audit row A8 asserts the edge has no route for
	// it — that assertion still passes, because "no route at the edge" is now true
	// of a handler that does not exist either.

	// Protected catalog surface (RFC-0023 slice B, ADR-047/050) — product's first
	// authenticated routes. The verifier trusts the STAFF realm; the edge does the
	// same check coarsely and this one is authoritative.
	staffVerifier, err := authmw.NewVerifier(authmw.Config{
		Issuer:   cfg.OIDCStaffIssuer,
		Audience: cfg.OIDCAudience,
		JWKSURL:  cfg.OIDCStaffJWKSURL,
	})
	if err != nil {
		logger.Fatal(ctx, "staff JWKS verifier init failed", slogx.Err(err))
	}
	v1.RegisterProtectedRoutes(r, v1.NewProtectedHandler(
		repository.NewCatalogAdminRepository(pool), productCache), staffVerifier)
	logger.Info(ctx, "Protected catalog routes registered",
		slog.String("issuer", cfg.OIDCStaffIssuer))

	// Create HTTP server (ReadHeaderTimeout mitigates Slowloris)
	srv := &http.Server{
		Addr:              ":" + cfg.Service.Port,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Start server in a goroutine
	go func() {
		logger.Info(ctx, "Starting product service", slog.String("port", cfg.Service.Port))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatal(ctx, "Failed to start server", slogx.Err(err))
		}
	}()

	logger.ProcessStarted(ctx, slogx.ComponentAPI)

	// Graceful shutdown - modern signal handling with context. The signal
	// context is cancelled on shutdown, so records keep using ctx.
	sigCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	// Wait for shutdown signal
	<-sigCtx.Done()
	logger.Info(ctx, "Shutdown signal received")

	// Fail readiness first and wait for propagation (best practice for K8s rollout).
	isShuttingDown.Store(true)
	drainDelay := cfg.GetReadinessDrainDelayDuration()
	if drainDelay > 0 {
		logger.Info(ctx, "Readiness drain delay started", slog.Duration("delay", drainDelay))
		time.Sleep(drainDelay)
		logger.Info(ctx, "Readiness drain delay completed", slog.Duration("delay", drainDelay))
	}

	// Shutdown context with configurable timeout
	shutdownTimeout := cfg.GetShutdownTimeoutDuration()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	logger.Info(ctx, "Shutting down server...", slog.Duration("timeout", shutdownTimeout))

	// Explicit cleanup sequence: HTTP Server → Cache → Database → OTel SDK
	// This ensures predictable shutdown order and easier debugging

	// 1. Shutdown HTTP server (stop accepting new connections, wait for in-flight requests)
	outcome := slogx.OutcomeGraceful
	if err := srv.Shutdown(shutdownCtx); err != nil {
		outcome = slogx.OutcomeError
		logger.Error(ctx, "HTTP server shutdown error", slogx.Err(err))
	} else {
		logger.Info(ctx, "HTTP server shutdown complete")
	}

	// 1b. Stop the gRPC server (drains in-flight RPCs).
	if grpcSrv != nil {
		grpcSrv.GracefulStop()
		logger.Info(ctx, "gRPC server shutdown complete")
	}

	// 2. Close cache connection (if enabled)
	// Note: Cache client cleanup is handled by defer in initialization section above

	// 3. Close database connections (explicit cleanup + defer for safety)
	pool.Close()
	logger.Info(ctx, "Database pool closed")

	// process.stopped goes out BEFORE the OTel SDK shuts down: a record
	// emitted after it is dropped rather than exported.
	logger.ProcessStopped(ctx, slogx.ComponentAPI, outcome)

	// 4. Shutdown the OTel SDK — flushes pending spans plus any OTLP
	// metrics/logs providers built behind the RFC-0014 flags.
	if tp != nil {
		if err := tp.Shutdown(shutdownCtx); err != nil {
			logger.Error(ctx, "OpenTelemetry shutdown error", slogx.Err(err))
		} else {
			logger.Info(ctx, "OpenTelemetry shutdown complete")
		}
	}

	logger.Info(ctx, "Graceful shutdown complete")
}
