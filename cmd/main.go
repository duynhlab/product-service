package main

import (
	"context"
	"fmt"
	"io/fs"
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
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"google.golang.org/grpc"

	"github.com/duynhlab/pkg/grpcx"
	"github.com/duynhlab/pkg/logger/zapx"
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
	"github.com/duynhlab/product-service/middleware"
)

// startGRPC starts the internal gRPC server on cfg.GRPC.Port, serving
// ProductService (checkout's price reads; the order saga's stock steps were
// removed in RFC-0021 phase 4) alongside the HTTP listener. It uses the shared grpcx bootstrap (OpenTelemetry,
// health, reflection) and returns nil only if the listener can't bind.
func startGRPC(cfg *config.Config, logger *zap.Logger, svc *logicv1.ProductService) *grpc.Server {
	lc := net.ListenConfig{}
	lis, err := lc.Listen(context.Background(), "tcp", ":"+cfg.GRPC.Port)
	if err != nil {
		logger.Error("Failed to listen for gRPC", zap.String("port", cfg.GRPC.Port), zap.Error(err))
		return nil
	}

	grpcSrv, _ := grpcx.NewServer(logger)
	productv1.RegisterProductServiceServer(grpcSrv, grpcv1.NewServer(svc))

	go func() {
		logger.Info("Starting gRPC server", zap.String("port", cfg.GRPC.Port))
		if err := grpcSrv.Serve(lis); err != nil {
			logger.Error("gRPC server error", zap.Error(err))
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
func runSubcommand(cmd string, cfg *config.Config, logger *zap.Logger) bool {
	switch cmd {
	case "migrate":
		if err := migratex.Run(migrations.FS, "sql", cfg.Database.BuildDSN()); err != nil {
			logger.Fatal("Schema migration failed", zap.Error(err))
		}
		logger.Info("Schema migrations applied")
		return true
	case "seed":
		// Demo data is DEV-ONLY; refuse to seed a production database.
		if cfg.IsProduction() {
			logger.Fatal("seed refused in production — demo data is dev-only")
		}
		if err := applySeed(context.Background(), cfg); err != nil {
			logger.Fatal("Demo seed failed", zap.Error(err))
		}
		logger.Info("Demo seed data applied")
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
	// Load configuration from environment variables (with .env file support for local dev)
	cfg := config.Load()

	// Initialize structured logger
	logger, err := zapx.New(os.Getenv("LOG_LEVEL"))
	if err != nil {
		panic("Failed to initialize logger: " + err.Error())
	}
	defer func() { _ = logger.Sync() }()

	// Subcommands (`migrate`, `seed`) run an embedded SQL set (init container,
	// against the direct DB host) and exit; no args serves the app.
	if len(os.Args) > 1 && runSubcommand(os.Args[1], cfg, logger) {
		return
	}

	// Validate runs after the migrate check so migrations need only DB config.
	if err := cfg.Validate(); err != nil {
		panic("Configuration validation failed: " + err.Error())
	}

	logger.Info("Service starting",
		zap.String("service", cfg.Service.Name),
		zap.String("version", cfg.Service.Version),
		zap.String("env", cfg.Service.Env),
		zap.String("port", cfg.Service.Port),
	)

	// RFC-0014: single OTel wiring point — traces per TRACING_ENABLED, OTLP
	// metrics (the only pipeline since the P3 cutover; OTEL_METRICS_ENABLED
	// defaults on, =false is a kill switch), logs behind OTEL_LOGS_ENABLED.
	// The config is built once so the tracer scope name and the startup log
	// reflect the values obsx actually uses.
	otelCfg := obsx.ConfigFromEnv()
	middleware.SetServiceName(otelCfg.ServiceName)
	var tp interface{ Shutdown(context.Context) error }
	obs, err := obsx.SetupObservability(context.Background(), otelCfg)
	if err != nil {
		logger.Warn("Failed to initialize OpenTelemetry", zap.Error(err))
	} else {
		tp = obs
		// RFC-0014 P4: tee application logs into the OTLP pipeline. ZapCore
		// returns a NopCore when OTEL_LOGS_ENABLED is off, so the tee is
		// unconditional; the min level mirrors the stdout core so debug
		// lines never leave the pod on an info-level service.
		minLevel, err := zapcore.ParseLevel(os.Getenv("LOG_LEVEL"))
		if err != nil {
			minLevel = zapcore.InfoLevel
		}
		logger = logger.WithOptions(zap.WrapCore(func(c zapcore.Core) zapcore.Core {
			return zapcore.NewTee(c, obs.ZapCore(otelCfg.ServiceName, minLevel))
		}))
		logger.Info("OpenTelemetry initialized",
			zap.Bool("traces", obs.TracerProvider != nil),
			zap.Bool("otlp_metrics", obs.MeterProvider != nil),
			zap.Bool("otlp_logs", obs.LoggerProvider != nil),
			zap.String("endpoint", otelCfg.Endpoint),
			zap.Float64("sample_rate", otelCfg.SampleRate),
		)
	}

	// Initialize Pyroscope profiling
	if cfg.Profiling.Enabled {
		stopProfiling, err := obsx.SetupProfiling()
		if err != nil {
			logger.Warn("Failed to initialize profiling", zap.Error(err))
		} else {
			logger.Info("Profiling initialized",
				zap.String("endpoint", cfg.Profiling.Endpoint),
			)
			defer func() { _ = stopProfiling(context.Background()) }()
		}
	} else {
		logger.Info("Profiling disabled (PROFILING_ENABLED=false)")
	}

	// Initialize database connection pool (pgx)
	pool, err := database.Connect(context.Background(), cfg.Database)
	if err != nil {
		logger.Fatal("Failed to connect to database", zap.Error(err))
	}
	defer pool.Close()
	logger.Info("Database connection pool established")

	// Initialize repositories (Core layer)
	productRepo := repository.NewPostgresProductRepository(pool)
	logger.Info("Product repository initialized")

	// Initialize cache client (Core layer) - optional, can be nil if disabled
	var productCache *cache.ProductCache
	if cfg.Cache.Enabled {
		cacheAddr := cfg.Cache.Host + ":" + cfg.Cache.Port
		cacheClient, err := cache.NewValkeyCacheClient(cacheAddr, cfg.Cache.Password, cfg.Cache.DB)
		if err != nil {
			logger.Warn("Failed to initialize cache client, continuing without cache",
				zap.Error(err),
				zap.String("cache_addr", cacheAddr),
			)
		} else {
			productCache = cache.NewProductCache(cacheClient, cfg.Cache.TTLProductList, cfg.Cache.TTLProductDetail)
			logger.Info("Cache client initialized",
				zap.String("cache_addr", cacheAddr),
				zap.Duration("ttl_list", cfg.Cache.TTLProductList),
				zap.Duration("ttl_detail", cfg.Cache.TTLProductDetail),
			)
			defer func() {
				if err := cacheClient.Close(); err != nil {
					logger.Error("Failed to close cache client", zap.Error(err))
				} else {
					logger.Info("Cache client closed")
				}
			}()
		}
	} else {
		logger.Info("Cache disabled (CACHE_ENABLED=false)")
	}

	// Initialize review service gRPC client for aggregation in product details endpoint
	reviewConn, err := grpcx.Dial(cfg.ReviewGRPCAddr)
	if err != nil {
		logger.Error("Failed to dial review gRPC", zap.String("addr", cfg.ReviewGRPCAddr), zap.Error(err))
		return
	}
	defer func() { _ = reviewConn.Close() }()
	reviewClient := v1.NewReviewClient(reviewConn)
	logger.Info("Review gRPC client initialized", zap.String("review_grpc_addr", cfg.ReviewGRPCAddr))

	// Initialize services (Logic layer) with dependency injection
	productService := logicv1.NewProductService(productRepo, productCache, reviewClient)

	// RFC-0021 P2-6: inventory availability enrichment for GetProductDetails,
	// only when explicitly selected. Soft-fail like the review enrichment — a
	// dial failure disables it and logs, never blocks startup (the detail page
	// keeps showing Product's own stock).
	if cfg.AvailabilitySource == "inventory" {
		invConn, ierr := grpcx.Dial(cfg.InventoryGRPCAddr)
		if ierr != nil {
			logger.Error("inventory availability enrichment disabled: dial failed",
				zap.String("addr", cfg.InventoryGRPCAddr), zap.Error(ierr))
		} else {
			defer func() { _ = invConn.Close() }()
			productService = productService.WithAvailability(v1.NewInventoryClient(invConn))
			logger.Info("Inventory availability enrichment enabled",
				zap.String("inventory_grpc_addr", cfg.InventoryGRPCAddr))
		}
	}
	logger.Info("Product service initialized")

	// Start the internal gRPC server (east-west: order-fulfillment saga).
	grpcSrv := startGRPC(cfg, logger, productService)

	// Initialize Web handler with dependency injection
	productHandler := v1.NewProductHandler(productService)
	logger.Info("Web handlers configured")

	r := gin.Default()

	var isShuttingDown atomic.Bool

	// CORS is handled at the Kong edge (global cors plugin in both stacks),
	// same as the other services — no service-level CORS middleware.

	// Tracing middleware (must be first for context propagation)
	r.Use(middleware.TracingMiddleware())

	// Logging middleware
	r.Use(middleware.LoggingMiddleware(logger))

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
	// Internal: admin/seed only. Not routed through Kong.
	r.POST("/product/v1/internal/products", productHandler.CreateProduct)

	// Create HTTP server (ReadHeaderTimeout mitigates Slowloris)
	srv := &http.Server{
		Addr:              ":" + cfg.Service.Port,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Start server in a goroutine
	go func() {
		logger.Info("Starting product service", zap.String("port", cfg.Service.Port))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatal("Failed to start server", zap.Error(err))
		}
	}()

	// Graceful shutdown - modern signal handling with context
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	// Wait for shutdown signal
	<-ctx.Done()
	logger.Info("Shutdown signal received")

	// Fail readiness first and wait for propagation (best practice for K8s rollout).
	isShuttingDown.Store(true)
	drainDelay := cfg.GetReadinessDrainDelayDuration()
	if drainDelay > 0 {
		logger.Info("Readiness drain delay started", zap.Duration("delay", drainDelay))
		time.Sleep(drainDelay)
		logger.Info("Readiness drain delay completed", zap.Duration("delay", drainDelay))
	}

	// Shutdown context with configurable timeout
	shutdownTimeout := cfg.GetShutdownTimeoutDuration()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	logger.Info("Shutting down server...", zap.Duration("timeout", shutdownTimeout))

	// Explicit cleanup sequence: HTTP Server → Cache → Database → OTel SDK
	// This ensures predictable shutdown order and easier debugging

	// 1. Shutdown HTTP server (stop accepting new connections, wait for in-flight requests)
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("HTTP server shutdown error", zap.Error(err))
	} else {
		logger.Info("HTTP server shutdown complete")
	}

	// 1b. Stop the gRPC server (drains in-flight RPCs).
	if grpcSrv != nil {
		grpcSrv.GracefulStop()
		logger.Info("gRPC server shutdown complete")
	}

	// 2. Close cache connection (if enabled)
	// Note: Cache client cleanup is handled by defer in initialization section above

	// 3. Close database connections (explicit cleanup + defer for safety)
	pool.Close()
	logger.Info("Database pool closed")

	// 4. Shutdown the OTel SDK — flushes pending spans plus any OTLP
	// metrics/logs providers built behind the RFC-0014 flags.
	if tp != nil {
		if err := tp.Shutdown(shutdownCtx); err != nil {
			logger.Error("OpenTelemetry shutdown error", zap.Error(err))
		} else {
			logger.Info("OpenTelemetry shutdown complete")
		}
	}

	logger.Info("Graceful shutdown complete")
}
