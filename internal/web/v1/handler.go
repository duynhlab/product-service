package v1

import (
	"errors"
	"log/slog"
	"net/http"

	"strconv"

	"github.com/duynhlab/pkg/httpx"
	"github.com/duynhlab/pkg/logger/slogx"
	"github.com/duynhlab/product-service/internal/core/domain"
	logicv1 "github.com/duynhlab/product-service/internal/logic/v1"
	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// tracerScope is the OpenTelemetry instrumentation scope for this package's
// spans: it names the CODE that creates them, which is why it is a package path
// and not the service name. Deployment identity travels separately as
// service.name on the Resource.
const tracerScope = "github.com/duynhlab/product-service/internal/web/v1"

// ProductHandler handles HTTP requests for products
type ProductHandler struct {
	productService *logicv1.ProductService
}

// NewProductHandler creates a new ProductHandler
func NewProductHandler(service *logicv1.ProductService) *ProductHandler {
	return &ProductHandler{
		productService: service,
	}
}

func (h *ProductHandler) ListProducts(c *gin.Context) {
	ctx := c.Request.Context()
	span := trace.SpanFromContext(ctx)
	// Get query parameters for filtering
	filters := domain.ProductFilters{
		Category: c.Query("category"),
		Search:   c.Query("search"),
		SortBy:   c.Query("sort"),
		Order:    c.Query("order"),
	}

	if limitStr := c.Query("limit"); limitStr != "" {
		if limit, err := strconv.Atoi(limitStr); err == nil {
			filters.Limit = limit
		}
	}

	if pageStr := c.Query("page"); pageStr != "" {
		if page, err := strconv.Atoi(pageStr); err == nil {
			filters.Page = page
		}
	}

	products, total, err := h.productService.ListProducts(ctx, filters)
	if err != nil {
		span.RecordError(err)
		slogx.FromContext(ctx).Error(ctx, "Failed to list products", slogx.Err(err))
		httpx.RespondError(c, http.StatusInternalServerError, httpx.CodeInternal, "Internal server error")
		return
	}

	// Normalize page/pageSize to the effective values the repository applied
	// (limit defaults to 20, page is clamped to >= 1) for the response envelope.
	page := filters.Page
	if page < 1 {
		page = 1
	}
	pageSize := filters.Limit
	if pageSize < 1 {
		pageSize = 20
	}

	slogx.FromContext(ctx).Info(ctx, "Products listed", slog.Int("count", len(products)), slog.Int("total", total))
	c.JSON(http.StatusOK, httpx.NewPaginated(products, page, pageSize, total))
}

func (h *ProductHandler) GetProduct(c *gin.Context) {
	ctx := c.Request.Context()
	span := trace.SpanFromContext(ctx)
	id := c.Param("id")
	span.SetAttributes(attribute.String("product.id", id))

	product, err := h.productService.GetProduct(ctx, id)
	if err != nil {
		span.RecordError(err)
		slogx.FromContext(ctx).Error(ctx, "Failed to get product", slogx.Err(err))

		switch {
		case errors.Is(err, logicv1.ErrProductNotFound):
			httpx.RespondError(c, http.StatusNotFound, httpx.CodeNotFound, "Product not found")
		default:
			httpx.RespondError(c, http.StatusInternalServerError, httpx.CodeInternal, "Internal server error")
		}
		return
	}

	slogx.FromContext(ctx).Info(ctx, "Product retrieved", slog.String("product.id", id))
	c.JSON(http.StatusOK, product)
}

func (h *ProductHandler) CreateProduct(c *gin.Context) {
	ctx := c.Request.Context()
	span := trace.SpanFromContext(ctx)
	var req domain.CreateProductRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		span.SetAttributes(attribute.Bool("request.valid", false))
		span.RecordError(err)
		slogx.FromContext(ctx).Warn(ctx, "Invalid request", slogx.Err(err))
		httpx.RespondError(c, http.StatusBadRequest, httpx.CodeValidation, err.Error())
		return
	}

	span.SetAttributes(attribute.Bool("request.valid", true))
	product, err := h.productService.CreateProduct(ctx, req)
	if err != nil {
		span.RecordError(err)
		slogx.FromContext(ctx).Error(ctx, "Failed to create product", slogx.Err(err))

		switch {
		case errors.Is(err, logicv1.ErrInvalidPrice):
			httpx.RespondError(c, http.StatusBadRequest, httpx.CodeValidation, "Invalid price")
		case errors.Is(err, logicv1.ErrInsufficientStock):
			httpx.RespondError(c, http.StatusBadRequest, httpx.CodeValidation, "Insufficient stock")
		default:
			httpx.RespondError(c, http.StatusInternalServerError, httpx.CodeInternal, "Internal server error")
		}
		return
	}

	slogx.FromContext(ctx).Info(ctx, "Product created", slog.String("product.id", product.ID))
	c.JSON(http.StatusCreated, product)
}

// GetProductDetails retrieves aggregated product details (product + reviews + stock + related)
func (h *ProductHandler) GetProductDetails(c *gin.Context) {
	ctx := c.Request.Context()
	span := trace.SpanFromContext(ctx)
	id := c.Param("id")
	span.SetAttributes(attribute.String("product.id", id))

	details, err := h.productService.GetProductDetails(ctx, id)
	if err != nil {
		span.RecordError(err)
		slogx.FromContext(ctx).Error(ctx, "Failed to get product details", slogx.Err(err))

		switch {
		case errors.Is(err, logicv1.ErrProductNotFound):
			httpx.RespondError(c, http.StatusNotFound, httpx.CodeNotFound, "Product not found")
		default:
			httpx.RespondError(c, http.StatusInternalServerError, httpx.CodeInternal, "Internal server error")
		}
		return
	}

	// Aggregate response.
	//
	// No `stock` block. It reported products.stock_quantity, frozen at the RFC-0021
	// W7 write cutover — a number that could not change. The `availability` block
	// below answers from inventory-service and is deliberately the ONLY stock answer
	// here: two answers with one of them stale is how a caller trusts the wrong one.
	response := gin.H{
		"product": details.Product,
		"reviews": details.Reviews,
		"reviews_summary": gin.H{
			"total":          details.ReviewsTotal,
			"average_rating": details.ReviewsAverage,
		},
		"related_products": details.RelatedProducts,
	}
	// RFC-0021 P2-6: inventory-sourced availability, present only when the
	// enrichment is enabled (soft-fails to {status: unknown}).
	if details.Availability != nil {
		response["availability"] = details.Availability
	}

	slogx.FromContext(ctx).Info(ctx, "Product details retrieved", slog.String("product.id", id))
	c.JSON(http.StatusOK, response)
}
