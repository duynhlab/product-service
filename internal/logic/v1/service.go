package v1

import (
	"context"
	"errors"

	"github.com/duynhlab/pkg/obsx"
	"github.com/duynhlab/product-service/internal/core/cache"
	"github.com/duynhlab/product-service/internal/core/domain"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// tracerScope is the OpenTelemetry instrumentation scope for this package's
// spans: it names the CODE that creates them, which is why it is a package path
// and not the service name. Deployment identity travels separately as
// service.name on the Resource.
const tracerScope = "github.com/duynhlab/product-service/internal/logic/v1"

// DefaultRelatedProductsLimit is the default number of related products to return.
const DefaultRelatedProductsLimit = 4

// ProductService handles product business logic
type ProductService struct {
	productRepo         domain.ProductRepository
	productCache        *cache.ProductCache // Optional - nil if caching disabled
	reviewFetcher       ReviewFetcher       // Optional - nil if review client not configured
	availabilityFetcher AvailabilityFetcher // Optional - nil unless inventory enrichment enabled (P2-6)
}

// NewProductService creates a new ProductService with repository injection
// productCache can be nil if caching is disabled
// reviewFetcher can be nil if the review client is not configured
func NewProductService(repo domain.ProductRepository, productCache *cache.ProductCache, reviewFetcher ReviewFetcher) *ProductService {
	return &ProductService{
		productRepo:   repo,
		productCache:  productCache,
		reviewFetcher: reviewFetcher,
	}
}

// WithAvailability wires the inventory availability enrichment for
// GetProductDetails (RFC-0021 P2-6). A nil fetcher (the default) keeps the
// detail page on Product's own stock — the enrichment is soft-fail and additive.
func (s *ProductService) WithAvailability(f AvailabilityFetcher) *ProductService {
	s.availabilityFetcher = f
	return s
}

// ListProducts retrieves all products with optional filtering
// Implements Cache-Aside pattern: check cache first, fallback to repository
func (s *ProductService) ListProducts(ctx context.Context, filters domain.ProductFilters) ([]domain.Product, int, error) {
	ctx, span := obsx.StartSpan(ctx, tracerScope, "product.list", trace.WithAttributes(
		attribute.String("layer", "logic"),
	))
	defer span.End()

	// Cache-Aside pattern: Check cache first
	if s.productCache != nil {
		cachedProducts, cachedTotal, err := s.productCache.GetProductList(ctx, filters)
		if err != nil {
			// Cache error - log but continue to database
			span.RecordError(err)
			span.SetAttributes(attribute.Bool("cache.error", true))
		} else if cachedProducts != nil {
			// Cache hit
			span.SetAttributes(
				attribute.Bool("cache.hit", true),
				attribute.Int("products.count", len(cachedProducts)),
				attribute.Int("products.total", cachedTotal),
			)
			return cachedProducts, cachedTotal, nil
		}
		// Cache miss - continue to database
		span.SetAttributes(attribute.Bool("cache.hit", false))
	}

	// Cache miss or cache disabled - query repository
	products, err := s.productRepo.FindAll(ctx, filters)
	if err != nil {
		span.RecordError(err)
		return nil, 0, err
	}

	// Get total count
	total, err := s.productRepo.Count(ctx, filters)
	if err != nil {
		span.RecordError(err)
		return nil, 0, err
	}

	// Write to cache (async - don't block on cache write errors)
	if s.productCache != nil {
		if err := s.productCache.SetProductList(ctx, filters, products, total); err != nil {
			// Log cache write error but don't fail the request
			span.RecordError(err)
			span.SetAttributes(attribute.Bool("cache.write_error", true))
		}
	}

	span.SetAttributes(
		attribute.Int("products.count", len(products)),
		attribute.Int("products.total", total),
	)
	return products, total, nil
}


// GetProductsByIDs is the batch price/availability read for checkout
// re-validation (RFC-0015). It deliberately bypasses the cache: product is
// the price authority at checkout time, so the answer must be the DB's
// current row, not a possibly-stale cached copy. Unknown ids are omitted.
func (s *ProductService) GetProductsByIDs(ctx context.Context, ids []string) ([]domain.Product, error) {
	ctx, span := obsx.StartSpan(ctx, tracerScope, "product.get_batch", trace.WithAttributes(
		attribute.String("layer", "logic"),
		attribute.Int("products.requested", len(ids)),
	))
	defer span.End()

	products, err := s.productRepo.FindByIDs(ctx, ids)
	if err != nil {
		span.RecordError(err)
		return nil, err
	}
	span.SetAttributes(attribute.Int("products.found", len(products)))
	return products, nil
}

// GetProduct retrieves a single product by ID
// Implements Cache-Aside pattern: check cache first, fallback to repository
func (s *ProductService) GetProduct(ctx context.Context, id string) (*domain.Product, error) {
	ctx, span := obsx.StartSpan(ctx, tracerScope, "product.get", trace.WithAttributes(
		attribute.String("layer", "logic"),
		attribute.String("product.id", id),
	))
	defer span.End()

	// Check cache with Stampede Prevention (Locking)
	if s.productCache != nil {
		product, err := s.productCache.GetProductOrSet(ctx, id, func() (*domain.Product, error) {
			// This closure is only called if cache miss AND lock acquired
			p, err := s.productRepo.FindByID(ctx, id)
			if err != nil {
				// If not found, return domain error which will be propagated
				return nil, err
			}
			return p, nil
		})

		if err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				span.SetAttributes(attribute.Bool("product.found", false))
				return nil, ErrProductNotFound
			}
			// Log other errors
			span.RecordError(err)
			// Decide whether to fail or return error. Since GetProductOrSet handles fallback logic internally
			// for timeouts, if we get here it's likely a DB error or a persistent cache/lock error.
			return nil, err
		}

		span.SetAttributes(attribute.Bool("product.found", true))
		return product, nil
	}

	// Cache disabled - direct DB call
	product, err := s.productRepo.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			span.SetAttributes(attribute.Bool("product.found", false))
			return nil, ErrProductNotFound
		}
		span.RecordError(err)
		return nil, err
	}

	span.SetAttributes(attribute.Bool("product.found", true))
	return product, nil
}

// GetRelatedProducts retrieves related products for a given product
func (s *ProductService) GetRelatedProducts(ctx context.Context, productID string, limit int) ([]domain.Product, error) {
	ctx, span := obsx.StartSpan(ctx, tracerScope, "product.related", trace.WithAttributes(
		attribute.String("layer", "logic"),
		attribute.String("product.id", productID),
	))
	defer span.End()

	// Call repository
	products, err := s.productRepo.FindRelatedProducts(ctx, productID, limit)
	if err != nil {
		span.RecordError(err)
		return nil, err
	}

	return products, nil
}

// CreateProduct creates a new product
func (s *ProductService) CreateProduct(ctx context.Context, req domain.CreateProductRequest) (*domain.Product, error) {
	ctx, span := obsx.StartSpan(ctx, tracerScope, "product.create", trace.WithAttributes(
		attribute.String("layer", "logic"),
		attribute.String("product.name", req.Name),
	))
	defer span.End()

	// Business validation
	if req.Price <= 0 {
		span.SetAttributes(attribute.Bool("product.created", false))
		return nil, ErrInvalidPrice
	}

	// Create product domain model
	product := &domain.Product{
		Name:        req.Name,
		Description: req.Description,
		Price:       req.Price,
		Category:    req.Category,
	}

	// Call repository
	err := s.productRepo.Create(ctx, product)
	if err != nil {
		span.RecordError(err)
		return nil, err
	}

	// Invalidate cache after successful creation
	// This ensures new products appear in list queries
	if s.productCache != nil {
		// Invalidate list caches (new product should appear in lists)
		if err := s.productCache.InvalidateProductList(ctx); err != nil {
			// Log cache invalidation error but don't fail the request
			span.RecordError(err)
			span.SetAttributes(attribute.Bool("cache.invalidation_error", true))
		}
	}

	span.SetAttributes(
		attribute.String("product.id", product.ID),
		attribute.Bool("product.created", true),
	)
	span.AddEvent("product.created")

	return product, nil
}
