package v1

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/duynhlab/product-service/internal/core/domain"
	logicv1 "github.com/duynhlab/product-service/internal/logic/v1"
	"github.com/gin-gonic/gin"
)

func init() { gin.SetMode(gin.TestMode) }

// mockProductRepo is a configurable domain.ProductRepository double for web tests.
type mockProductRepo struct {
	all        []domain.Product
	total      int
	findAllErr error
	countErr   error
	product    *domain.Product
	findErr    error
	related    []domain.Product
	relatedErr error
	createErr  error
}

func (m *mockProductRepo) FindByID(_ context.Context, _ string) (*domain.Product, error) {
	return m.product, m.findErr
}
func (m *mockProductRepo) FindByIDs(_ context.Context, _ []string) ([]domain.Product, error) {
	return nil, nil
}

func (m *mockProductRepo) FindAll(_ context.Context, _ domain.ProductFilters) ([]domain.Product, error) {
	return m.all, m.findAllErr
}
func (m *mockProductRepo) Create(_ context.Context, p *domain.Product) error {
	if m.createErr == nil {
		p.ID = "new-id"
	}
	return m.createErr
}
func (m *mockProductRepo) Update(_ context.Context, _ *domain.Product) error { return nil }
func (m *mockProductRepo) Delete(_ context.Context, _ string) error          { return nil }
func (m *mockProductRepo) FindRelatedProducts(_ context.Context, _ string, _ int) ([]domain.Product, error) {
	return m.related, m.relatedErr
}
func (m *mockProductRepo) Count(_ context.Context, _ domain.ProductFilters) (int, error) {
	return m.total, m.countErr
}
func newHandler(repo domain.ProductRepository) *ProductHandler {
	return NewProductHandler(logicv1.NewProductService(repo, nil, nil))
}

func newCtx(method, target string, params gin.Params) (*gin.Context, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(method, target, nil)
	c.Params = params
	return c, rec
}

func ctxWithBody(method, target, body string) (*gin.Context, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(method, target, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	return c, rec
}

// decode returns the parsed JSON body.
func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON body %q: %v", rec.Body.String(), err)
	}
	return body
}

func TestListProducts_Success(t *testing.T) {
	repo := &mockProductRepo{
		all:   []domain.Product{{ID: "1"}, {ID: "2"}},
		total: 2,
	}
	c, rec := newCtx(http.MethodGet, "/product/v1/public/products?page=1&limit=5", nil)

	newHandler(repo).ListProducts(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := decode(t, rec)
	if body["total_items"].(float64) != 2 {
		t.Errorf("total_items = %v, want 2", body["total_items"])
	}
	if items, ok := body["items"].([]any); !ok || len(items) != 2 {
		t.Errorf("items = %v, want length 2", body["items"])
	}
}

func TestListProducts_DefaultPaging(t *testing.T) {
	// No page/limit query params → envelope uses normalized defaults (page 1, size 20).
	repo := &mockProductRepo{all: []domain.Product{}, total: 0}
	c, rec := newCtx(http.MethodGet, "/product/v1/public/products", nil)

	newHandler(repo).ListProducts(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := decode(t, rec)
	if body["page"].(float64) != 1 {
		t.Errorf("page = %v, want 1", body["page"])
	}
	if body["page_size"].(float64) != 20 {
		t.Errorf("page_size = %v, want 20", body["page_size"])
	}
}

func TestListProducts_ServiceError(t *testing.T) {
	repo := &mockProductRepo{findAllErr: context.DeadlineExceeded}
	c, rec := newCtx(http.MethodGet, "/product/v1/public/products", nil)

	newHandler(repo).ListProducts(c)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if code := decode(t, rec)["code"]; code != "INTERNAL_ERROR" {
		t.Errorf("code = %v, want INTERNAL_ERROR", code)
	}
}

func TestGetProduct_Success(t *testing.T) {
	repo := &mockProductRepo{product: &domain.Product{ID: "1", Name: "Widget"}}
	c, rec := newCtx(http.MethodGet, "/product/v1/public/products/1", gin.Params{{Key: "id", Value: "1"}})

	newHandler(repo).GetProduct(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if id := decode(t, rec)["id"]; id != "1" {
		t.Errorf("id = %v, want 1", id)
	}
}

func TestGetProduct_NotFound(t *testing.T) {
	repo := &mockProductRepo{findErr: domain.ErrNotFound}
	c, rec := newCtx(http.MethodGet, "/product/v1/public/products/9", gin.Params{{Key: "id", Value: "9"}})

	newHandler(repo).GetProduct(c)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if code := decode(t, rec)["code"]; code != "NOT_FOUND" {
		t.Errorf("code = %v, want NOT_FOUND", code)
	}
}

func TestGetProduct_ServiceError(t *testing.T) {
	repo := &mockProductRepo{findErr: context.DeadlineExceeded}
	c, rec := newCtx(http.MethodGet, "/product/v1/public/products/1", gin.Params{{Key: "id", Value: "1"}})

	newHandler(repo).GetProduct(c)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if code := decode(t, rec)["code"]; code != "INTERNAL_ERROR" {
		t.Errorf("code = %v, want INTERNAL_ERROR", code)
	}
}

func TestCreateProduct_Success(t *testing.T) {
	repo := &mockProductRepo{}
	c, rec := ctxWithBody(http.MethodPost, "/product/v1/private/products",
		`{"name":"Widget","price":9.99}`)

	newHandler(repo).CreateProduct(c)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rec.Code)
	}
}

func TestCreateProduct_BadJSON(t *testing.T) {
	c, rec := ctxWithBody(http.MethodPost, "/product/v1/private/products", "{")

	newHandler(&mockProductRepo{}).CreateProduct(c)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if code := decode(t, rec)["code"]; code != "VALIDATION_ERROR" {
		t.Errorf("code = %v, want VALIDATION_ERROR", code)
	}
}

func TestCreateProduct_ServiceError(t *testing.T) {
	repo := &mockProductRepo{createErr: context.DeadlineExceeded}
	c, rec := ctxWithBody(http.MethodPost, "/product/v1/private/products",
		`{"name":"Widget","price":9.99}`)

	newHandler(repo).CreateProduct(c)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if code := decode(t, rec)["code"]; code != "INTERNAL_ERROR" {
		t.Errorf("code = %v, want INTERNAL_ERROR", code)
	}
}

func TestGetProductDetails_NoReviews(t *testing.T) {
	// nil reviewFetcher → soft-fail no-reviews path; product + related still aggregate.
	repo := &mockProductRepo{
		product: &domain.Product{ID: "1", Name: "Widget", StockQuantity: 3},
		related: []domain.Product{{ID: "2"}},
	}
	c, rec := newCtx(http.MethodGet, "/product/v1/public/products/1/details", gin.Params{{Key: "id", Value: "1"}})

	newHandler(repo).GetProductDetails(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := decode(t, rec)
	if _, ok := body["product"]; !ok {
		t.Errorf("response missing product field: %s", rec.Body.String())
	}
	if _, ok := body["reviews_summary"]; !ok {
		t.Errorf("response missing reviews_summary field: %s", rec.Body.String())
	}
	// No `stock` block, and no stock_quantity inside `product`: both reported a
	// column frozen at the RFC-0021 W7 write cutover. Availability comes from
	// inventory-service now, and a stale second answer beside it is how a caller
	// ends up trusting the wrong one.
	if _, ok := body["stock"]; ok {
		t.Errorf("response still carries the frozen stock block: %s", rec.Body.String())
	}
	product, ok := body["product"].(map[string]any)
	if !ok {
		t.Fatalf("product is %T, want an object: %s", body["product"], rec.Body.String())
	}
	if _, ok := product["stock_quantity"]; ok {
		t.Errorf("product still publishes stock_quantity: %s", rec.Body.String())
	}
}

func TestGetProductDetails_NotFound(t *testing.T) {
	repo := &mockProductRepo{findErr: domain.ErrNotFound}
	c, rec := newCtx(http.MethodGet, "/product/v1/public/products/9/details", gin.Params{{Key: "id", Value: "9"}})

	newHandler(repo).GetProductDetails(c)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if code := decode(t, rec)["code"]; code != "NOT_FOUND" {
		t.Errorf("code = %v, want NOT_FOUND", code)
	}
}

func TestGetProductDetails_ServiceError(t *testing.T) {
	repo := &mockProductRepo{findErr: context.DeadlineExceeded}
	c, rec := newCtx(http.MethodGet, "/product/v1/public/products/1/details", gin.Params{{Key: "id", Value: "1"}})

	newHandler(repo).GetProductDetails(c)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}
