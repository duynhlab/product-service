package v1

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/duynhlab/product-service/internal/core/domain"
	"github.com/duynhlab/product-service/internal/core/repository"
	"github.com/duynhlab/pkg/authmw"
	"github.com/duynhlab/pkg/httpx"
)

// The protected catalog surface (RFC-0023 slice B, ADR-047/050). Product's first
// authenticated routes of any kind: the public catalog is anonymous and the
// seed-only internal create was fenced by NetworkPolicy alone.
//
// Every route requires a STAFF-realm token (verified here authoritatively, not
// trusted from the edge) plus the backoffice_admin role. The actor recorded in the
// audit trail is the verified `sub`, never a request field.
//
// COMMAND SAFETY WITHOUT AN IDEMPOTENCY STORE. Each command is already guarded by a
// conflict the schema or the FSM enforces, so a retried request cannot duplicate an
// effect: create collides with UNIQUE(products.name) → 409; an edit carries the
// version it read → 409 on a stale one; a lifecycle command is refused when the edge
// does not exist → 409. Adding a `pkg/idempotency` store would give the same answers
// through a second mechanism and a new table.

// backofficeRole is the staff-realm role every protected route requires.
const backofficeRole = "backoffice_admin"

const msgInternal = "An internal error occurred"

// catalogAdmin is the slice of the repository layer these handlers use, kept as an
// interface so the handlers are testable without a database.
type catalogAdmin interface {
	ListProducts(ctx context.Context, status string, limit, offset int) ([]repository.AdminProduct, int, error)
	GetProduct(ctx context.Context, id string) (*repository.AdminProduct, error)
	CreateProduct(ctx context.Context, in repository.CreateProductInput) (*repository.AdminProduct, error)
	UpdateProduct(ctx context.Context, in repository.UpdateProductInput) (*repository.AdminProduct, error)
	Transition(ctx context.Context, in repository.TransitionInput) (*repository.AdminProduct, error)
	ListCategories(ctx context.Context, limit, offset int) ([]domain.Category, int, error)
	CreateCategory(ctx context.Context, name, description, actorSub, requestID string) (*domain.Category, error)
	UpdateCategory(ctx context.Context, id int, name, description, actorSub, requestID string) (*domain.Category, error)
	ListAudit(ctx context.Context, targetType string, targetID, limit int) ([]repository.AuditRow, error)
}

// catalogCache is the invalidation surface a write needs. Optional: a stack without
// a cache passes nil and the writes simply skip it.
type catalogCache interface {
	InvalidateProduct(ctx context.Context, id string) error
	InvalidateProductList(ctx context.Context) error
}

// ProtectedHandler serves the operator's catalog screens.
type ProtectedHandler struct {
	repo  catalogAdmin
	cache catalogCache
}

// NewProtectedHandler wires the protected catalog handler. cache may be nil.
func NewProtectedHandler(repo catalogAdmin, cache catalogCache) *ProtectedHandler {
	return &ProtectedHandler{repo: repo, cache: cache}
}

// RegisterProtectedRoutes mounts the protected group with the real guard chain.
// Split from mount so tests can inject fakes for the middleware.
func RegisterProtectedRoutes(r *gin.Engine, h *ProtectedHandler, staffVerifier *authmw.Verifier) {
	h.mount(r, authmw.MiddlewareJWT(staffVerifier), authmw.MiddlewareRequireRole(backofficeRole))
}

func (h *ProtectedHandler) mount(r *gin.Engine, authMW ...gin.HandlerFunc) {
	g := r.Group("/product/v1/protected")
	g.Use(authMW...)
	{
		g.GET("/products", h.ListProducts)
		g.GET("/products/:id", h.GetProduct)
		g.POST("/products", h.CreateProduct)
		g.PUT("/products/:id", h.UpdateProduct)
		// One route per transition, not a status setter: the URL says what the
		// operator meant, which is also what the audit records.
		g.POST("/products/:id/publish", h.transition(domain.ActionPublish))
		g.POST("/products/:id/archive", h.transition(domain.ActionArchive))
		g.POST("/products/:id/restore", h.transition(domain.ActionRestore))
		g.GET("/products/:id/audit", h.ProductAudit)

		g.GET("/categories", h.ListCategories)
		g.POST("/categories", h.CreateCategory)
		g.PUT("/categories/:id", h.UpdateCategory)
	}
}

// respondDomainError maps the domain's error vocabulary onto the shared envelope.
// Kept in one place so every command answers a conflict the same way.
func respondDomainError(c *gin.Context, err error) {
	// Record the real error on the context: the client gets the opaque envelope,
	// but a 500 with nothing in the log is undiagnosable in production (the
	// access log picks these up — middleware/logging.go).
	_ = c.Error(err)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		httpx.RespondError(c, http.StatusNotFound, httpx.CodeNotFound, "Product not found")
	case errors.Is(err, domain.ErrInvalidTransition):
		httpx.RespondError(c, http.StatusConflict, "INVALID_TRANSITION", err.Error())
	case errors.Is(err, domain.ErrVersionConflict):
		httpx.RespondError(c, http.StatusConflict, "VERSION_CONFLICT", err.Error())
	case errors.Is(err, domain.ErrConflict):
		httpx.RespondError(c, http.StatusConflict, httpx.CodeConflict, err.Error())
	case errors.Is(err, domain.ErrInvalidInput):
		httpx.RespondError(c, http.StatusBadRequest, httpx.CodeValidation, err.Error())
	default:
		httpx.RespondError(c, http.StatusInternalServerError, httpx.CodeInternal, msgInternal)
	}
}

// actor returns the verified subject and the request id for the audit trail. The
// subject comes from the context the JWT middleware populated — never from the
// request body (ADR-047).
func actor(c *gin.Context) (sub, requestID string) {
	if v, ok := c.Get(authmw.CtxUserID); ok {
		sub, _ = v.(string)
	}
	return sub, c.GetHeader("X-Request-Id")
}

// ListProducts serves GET /products?status=&page=&page_size= — the operator's view,
// which unlike the public catalog contains DRAFT and ARCHIVED rows.
func (h *ProtectedHandler) ListProducts(c *gin.Context) {
	page, pageSize := httpx.ParsePage(c)

	status := c.Query("status")
	if status != "" && !domain.ProductStatus(status).Valid() {
		httpx.RespondError(c, http.StatusBadRequest, httpx.CodeValidation, "unknown status filter")
		return
	}

	items, total, err := h.repo.ListProducts(c.Request.Context(), status, pageSize, httpx.Offset(page, pageSize))
	if err != nil {
		_ = c.Error(err)
		httpx.RespondError(c, http.StatusInternalServerError, httpx.CodeInternal, msgInternal)
		return
	}
	c.JSON(http.StatusOK, httpx.NewPaginated(items, page, pageSize, total))
}

// GetProduct serves GET /products/:id in any lifecycle state.
func (h *ProtectedHandler) GetProduct(c *gin.Context) {
	p, err := h.repo.GetProduct(c.Request.Context(), c.Param("id"))
	if err != nil {
		respondDomainError(c, err)
		return
	}
	c.JSON(http.StatusOK, p)
}

// createProductBody is the create payload. No status field: a new product is always
// DRAFT, and publishing is its own command.
type createProductBody struct {
	Name        string  `json:"name" binding:"required,max=255"`
	Price       float64 `json:"price" binding:"required,min=0"`
	Description string  `json:"description" binding:"max=4000"`
	Category    string  `json:"category" binding:"max=100"`
}

// CreateProduct serves POST /products — creates a DRAFT product.
func (h *ProtectedHandler) CreateProduct(c *gin.Context) {
	var body createProductBody
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.RespondError(c, http.StatusBadRequest, httpx.CodeValidation, err.Error())
		return
	}
	sub, reqID := actor(c)

	p, err := h.repo.CreateProduct(c.Request.Context(), repository.CreateProductInput{
		Name: body.Name, Price: body.Price, Description: body.Description,
		Category: body.Category, ActorSub: sub, RequestID: reqID,
	})
	if err != nil {
		respondDomainError(c, err)
		return
	}
	// A DRAFT product is invisible to the public reads, so only the lists could
	// hold a stale count; the detail key cannot exist yet.
	h.invalidateList(c)
	c.JSON(http.StatusCreated, p)
}

// updateProductBody carries the version the operator read — the concurrency token.
type updateProductBody struct {
	Name        string  `json:"name" binding:"required,max=255"`
	Price       float64 `json:"price" binding:"required,min=0"`
	Description string  `json:"description" binding:"max=4000"`
	Category    string  `json:"category" binding:"max=100"`
	Version     int64   `json:"version" binding:"required,min=1"`
	Reason      string  `json:"reason" binding:"max=64"`
}

// UpdateProduct serves PUT /products/:id under optimistic concurrency.
func (h *ProtectedHandler) UpdateProduct(c *gin.Context) {
	var body updateProductBody
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.RespondError(c, http.StatusBadRequest, httpx.CodeValidation, err.Error())
		return
	}
	id := c.Param("id")
	sub, reqID := actor(c)

	p, err := h.repo.UpdateProduct(c.Request.Context(), repository.UpdateProductInput{
		ID: id, Name: body.Name, Price: body.Price, Description: body.Description,
		Category: body.Category, ExpectedVersion: body.Version,
		ActorSub: sub, Reason: body.Reason, RequestID: reqID,
	})
	if err != nil {
		respondDomainError(c, err)
		return
	}
	h.invalidateProduct(c, id)
	c.JSON(http.StatusOK, p)
}

// transitionBody is the optional reason an operator can attach to a lifecycle move.
type transitionBody struct {
	Reason string `json:"reason" binding:"max=64"`
}

// transition builds the handler for one lifecycle command.
func (h *ProtectedHandler) transition(action domain.LifecycleAction) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body transitionBody
		// The body is optional — an empty POST is a valid command.
		if c.Request.ContentLength > 0 {
			if err := c.ShouldBindJSON(&body); err != nil {
				httpx.RespondError(c, http.StatusBadRequest, httpx.CodeValidation, err.Error())
				return
			}
		}
		id := c.Param("id")
		sub, reqID := actor(c)

		p, err := h.repo.Transition(c.Request.Context(), repository.TransitionInput{
			ID: id, Action: action, ActorSub: sub, Reason: body.Reason, RequestID: reqID,
		})
		if err != nil {
			respondDomainError(c, err)
			return
		}
		h.invalidateProduct(c, id)
		c.JSON(http.StatusOK, p)
	}
}

// ProductAudit serves GET /products/:id/audit — who changed this product, when.
func (h *ProtectedHandler) ProductAudit(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		httpx.RespondError(c, http.StatusBadRequest, httpx.CodeValidation, "id must be a positive integer")
		return
	}
	rows, err := h.repo.ListAudit(c.Request.Context(), "product", id, 50)
	if err != nil {
		_ = c.Error(err)
		httpx.RespondError(c, http.StatusInternalServerError, httpx.CodeInternal, msgInternal)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": rows})
}

// ListCategories serves GET /categories?page=&page_size=.
func (h *ProtectedHandler) ListCategories(c *gin.Context) {
	page, pageSize := httpx.ParsePage(c)
	items, total, err := h.repo.ListCategories(c.Request.Context(), pageSize, httpx.Offset(page, pageSize))
	if err != nil {
		_ = c.Error(err)
		httpx.RespondError(c, http.StatusInternalServerError, httpx.CodeInternal, msgInternal)
		return
	}
	c.JSON(http.StatusOK, httpx.NewPaginated(items, page, pageSize, total))
}

type categoryBody struct {
	Name        string `json:"name" binding:"required,max=100"`
	Description string `json:"description" binding:"max=1000"`
}

// CreateCategory serves POST /categories. A duplicate name is a 409, which is also
// what makes a retried create safe.
func (h *ProtectedHandler) CreateCategory(c *gin.Context) {
	var body categoryBody
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.RespondError(c, http.StatusBadRequest, httpx.CodeValidation, err.Error())
		return
	}
	sub, reqID := actor(c)

	cat, err := h.repo.CreateCategory(c.Request.Context(), body.Name, body.Description, sub, reqID)
	if err != nil {
		respondDomainError(c, err)
		return
	}
	c.JSON(http.StatusCreated, cat)
}

// UpdateCategory serves PUT /categories/:id. There is no delete: products
// reference categories ON DELETE SET NULL, so deleting one would silently
// uncategorize products — a decision RFC-0023 keeps out of the MVP.
func (h *ProtectedHandler) UpdateCategory(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		httpx.RespondError(c, http.StatusBadRequest, httpx.CodeValidation, "id must be a positive integer")
		return
	}
	var body categoryBody
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.RespondError(c, http.StatusBadRequest, httpx.CodeValidation, err.Error())
		return
	}
	sub, reqID := actor(c)

	cat, err := h.repo.UpdateCategory(c.Request.Context(), id, body.Name, body.Description, sub, reqID)
	if err != nil {
		respondDomainError(c, err)
		return
	}
	// A rename changes the category name the public product payload carries.
	h.invalidateAll(c)
	c.JSON(http.StatusOK, cat)
}

// Cache invalidation is best-effort and never fails a committed command: the write
// is already durable, and a stale cache entry expires on its own TTL. Failing the
// response here would tell the operator the change did not happen when it did.
func (h *ProtectedHandler) invalidateProduct(c *gin.Context, id string) {
	if h.cache == nil {
		return
	}
	ctx := c.Request.Context()
	_ = h.cache.InvalidateProduct(ctx, id)
	_ = h.cache.InvalidateProductList(ctx)
}

func (h *ProtectedHandler) invalidateList(c *gin.Context) {
	if h.cache == nil {
		return
	}
	_ = h.cache.InvalidateProductList(c.Request.Context())
}

func (h *ProtectedHandler) invalidateAll(c *gin.Context) {
	if h.cache == nil {
		return
	}
	ctx := c.Request.Context()
	_ = h.cache.InvalidateProductList(ctx)
}
