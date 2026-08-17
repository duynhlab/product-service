package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/duynhlab/pkg/authmw"
	"github.com/duynhlab/product-service/internal/core/domain"
	"github.com/duynhlab/product-service/internal/core/repository"
)

// fakeCatalog scripts the repository layer and records what the handlers passed
// down — the actor especially, since ADR-047 requires it to come from the token.
type fakeCatalog struct {
	products []repository.AdminProduct
	total    int
	product  *repository.AdminProduct
	audit    []repository.AuditRow
	err      error

	got struct {
		status        string
		limit, offset int
		actor         string
		requestID     string
		reason        string
		version       int64
		action        domain.LifecycleAction
	}
}

func (f *fakeCatalog) ListProducts(_ context.Context, status string, limit, offset int) ([]repository.AdminProduct, int, error) {
	f.got.status, f.got.limit, f.got.offset = status, limit, offset
	return f.products, f.total, f.err
}

func (f *fakeCatalog) GetProduct(_ context.Context, _ string) (*repository.AdminProduct, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.product, nil
}

func (f *fakeCatalog) CreateProduct(_ context.Context, in repository.CreateProductInput) (*repository.AdminProduct, error) {
	f.got.actor, f.got.requestID = in.ActorSub, in.RequestID
	if f.err != nil {
		return nil, f.err
	}
	return f.product, nil
}

func (f *fakeCatalog) UpdateProduct(_ context.Context, in repository.UpdateProductInput) (*repository.AdminProduct, error) {
	f.got.actor, f.got.version, f.got.reason = in.ActorSub, in.ExpectedVersion, in.Reason
	if f.err != nil {
		return nil, f.err
	}
	return f.product, nil
}

func (f *fakeCatalog) Transition(_ context.Context, in repository.TransitionInput) (*repository.AdminProduct, error) {
	f.got.actor, f.got.action, f.got.reason = in.ActorSub, in.Action, in.Reason
	if f.err != nil {
		return nil, f.err
	}
	return f.product, nil
}

func (f *fakeCatalog) ListCategories(_ context.Context, limit, offset int) ([]domain.Category, int, error) {
	f.got.limit, f.got.offset = limit, offset
	if f.err != nil {
		return nil, 0, f.err
	}
	return []domain.Category{{ID: 1, Name: "Electronics"}}, 1, nil
}

func (f *fakeCatalog) CreateCategory(_ context.Context, name, _, actorSub, _ string) (*domain.Category, error) {
	f.got.actor = actorSub
	if f.err != nil {
		return nil, f.err
	}
	return &domain.Category{ID: 9, Name: name}, nil
}

func (f *fakeCatalog) UpdateCategory(_ context.Context, id int, name, _, actorSub, _ string) (*domain.Category, error) {
	f.got.actor = actorSub
	if f.err != nil {
		return nil, f.err
	}
	return &domain.Category{ID: id, Name: name}, nil
}

func (f *fakeCatalog) ListAudit(_ context.Context, _ string, _, _ int) ([]repository.AuditRow, error) {
	return f.audit, f.err
}

// countingCache proves a committed write invalidated what it had to.
type countingCache struct{ product, list int }

func (c *countingCache) InvalidateProduct(_ context.Context, _ string) error { c.product++; return nil }
func (c *countingCache) InvalidateProductList(_ context.Context) error       { c.list++; return nil }

const staffSub = "d0e00000-0000-4000-8000-000000000001"

func engine(t *testing.T, f *fakeCatalog, cache catalogCache, roles ...string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	NewProtectedHandler(f, cache).mount(r,
		func(c *gin.Context) {
			c.Set(authmw.CtxUserID, staffSub)
			c.Set(authmw.CtxRoles, roles)
			c.Next()
		},
		authmw.MiddlewareRequireRole(backofficeRole))
	return r
}

func do(r *gin.Engine, method, path string, body any) *httptest.ResponseRecorder {
	var buf *bytes.Buffer
	if body != nil {
		b, _ := json.Marshal(body)
		buf = bytes.NewBuffer(b)
	} else {
		buf = bytes.NewBuffer(nil)
	}
	req := httptest.NewRequest(method, path, buf)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-Id", "req-test-1")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestProtectedCatalogRoleGate(t *testing.T) {
	r := engine(t, &fakeCatalog{}, nil, "customer")
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/product/v1/protected/products"},
		{http.MethodGet, "/product/v1/protected/products/1"},
		{http.MethodPost, "/product/v1/protected/products"},
		{http.MethodPut, "/product/v1/protected/products/1"},
		{http.MethodPost, "/product/v1/protected/products/1/publish"},
		{http.MethodPost, "/product/v1/protected/products/1/archive"},
		{http.MethodPost, "/product/v1/protected/products/1/restore"},
		{http.MethodGet, "/product/v1/protected/products/1/audit"},
		{http.MethodGet, "/product/v1/protected/categories"},
		{http.MethodPost, "/product/v1/protected/categories"},
		{http.MethodPut, "/product/v1/protected/categories/1"},
	} {
		if w := do(r, tc.method, tc.path, nil); w.Code != http.StatusForbidden {
			t.Errorf("%s %s: want 403 for a customer role, got %d", tc.method, tc.path, w.Code)
		}
	}
}

func TestProtectedListProducts(t *testing.T) {
	f := &fakeCatalog{
		products: []repository.AdminProduct{{ID: "7", Name: "Widget", Status: "DRAFT", Version: 1}},
		total:    31,
	}
	r := engine(t, f, nil, backofficeRole)

	w := do(r, http.MethodGet, "/product/v1/protected/products?status=DRAFT&page=2&page_size=10", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if f.got.status != "DRAFT" || f.got.limit != 10 || f.got.offset != 10 {
		t.Fatalf("paging/filter not forwarded: %+v", f.got)
	}
	var resp struct {
		TotalItems int                       `json:"total_items"`
		Items      []repository.AdminProduct `json:"items"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.TotalItems != 31 || len(resp.Items) != 1 || resp.Items[0].Status != "DRAFT" {
		t.Fatalf("operator list must expose status: %s", w.Body.String())
	}

	// An unknown status is a validation error, not a silent empty page.
	if w := do(r, http.MethodGet, "/product/v1/protected/products?status=PUBLISHED", nil); w.Code != http.StatusBadRequest {
		t.Fatalf("bogus status: want 400, got %d", w.Code)
	}
}

func TestProtectedGetProduct(t *testing.T) {
	f := &fakeCatalog{product: &repository.AdminProduct{ID: "7", Name: "Widget", Status: "ARCHIVED", Version: 4}}
	r := engine(t, f, nil, backofficeRole)

	// The operator can read a product the public catalog no longer serves — that
	// is the whole point of the protected detail route.
	w := do(r, http.MethodGet, "/product/v1/protected/products/7", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	var got repository.AdminProduct
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Status != "ARCHIVED" || got.Version != 4 {
		t.Fatalf("detail must carry status and version: %s", w.Body.String())
	}

	f.err = domain.ErrNotFound
	if w := do(r, http.MethodGet, "/product/v1/protected/products/999", nil); w.Code != http.StatusNotFound {
		t.Fatalf("missing product: want 404, got %d", w.Code)
	}

	// A driver error must stay opaque and answer 500, not 404.
	f.err = context.DeadlineExceeded
	if w := do(r, http.MethodGet, "/product/v1/protected/products/7", nil); w.Code != http.StatusInternalServerError {
		t.Fatalf("driver error: want 500, got %d", w.Code)
	}
}

func TestRespondDomainErrorCoversTheVocabulary(t *testing.T) {
	// Each domain error maps to exactly one status/code pair; the table is the
	// contract the portal's error handling is written against.
	cases := []struct {
		err  error
		code int
	}{
		{domain.ErrNotFound, http.StatusNotFound},
		{domain.ErrInvalidTransition, http.StatusConflict},
		{domain.ErrVersionConflict, http.StatusConflict},
		{domain.ErrConflict, http.StatusConflict},
		{domain.ErrInvalidInput, http.StatusBadRequest},
		{context.DeadlineExceeded, http.StatusInternalServerError},
	}
	for _, c := range cases {
		f := &fakeCatalog{err: c.err}
		r := engine(t, f, nil, backofficeRole)
		if w := do(r, http.MethodGet, "/product/v1/protected/products/1", nil); w.Code != c.code {
			t.Errorf("%v → %d, want %d", c.err, w.Code, c.code)
		}
	}
}

func TestProtectedCreateProductUsesTokenActor(t *testing.T) {
	f := &fakeCatalog{product: &repository.AdminProduct{ID: "8", Name: "New", Status: "DRAFT", Version: 1}}
	cache := &countingCache{}
	r := engine(t, f, cache, backofficeRole)

	// The body carries an actor_sub the handler must ignore (ADR-047).
	w := do(r, http.MethodPost, "/product/v1/protected/products", map[string]any{
		"name": "New", "price": 9.99, "category": "Electronics",
		"actor_sub": "attacker-supplied",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", w.Code, w.Body.String())
	}
	if f.got.actor != staffSub {
		t.Fatalf("actor = %q, must be the verified token subject", f.got.actor)
	}
	if f.got.requestID != "req-test-1" {
		t.Fatalf("request id not carried into the audit: %q", f.got.requestID)
	}
	// A DRAFT product cannot be in the detail cache, but list counts can be stale.
	if cache.list != 1 || cache.product != 0 {
		t.Fatalf("cache invalidation after create = (list %d, product %d), want (1, 0)", cache.list, cache.product)
	}

	// Validation: no name, negative price.
	if w := do(r, http.MethodPost, "/product/v1/protected/products", map[string]any{"price": 1}); w.Code != http.StatusBadRequest {
		t.Errorf("missing name: want 400, got %d", w.Code)
	}
	if w := do(r, http.MethodPost, "/product/v1/protected/products", map[string]any{"name": "x", "price": -5}); w.Code != http.StatusBadRequest {
		t.Errorf("negative price: want 400, got %d", w.Code)
	}
}

func TestProtectedUpdateProductConcurrency(t *testing.T) {
	f := &fakeCatalog{product: &repository.AdminProduct{ID: "8", Name: "Edited", Version: 3}}
	cache := &countingCache{}
	r := engine(t, f, cache, backofficeRole)

	w := do(r, http.MethodPut, "/product/v1/protected/products/8", map[string]any{
		"name": "Edited", "price": 12, "version": 2, "reason": "typo",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if f.got.version != 2 || f.got.reason != "typo" {
		t.Fatalf("version/reason not forwarded: %+v", f.got)
	}
	if cache.product != 1 || cache.list != 1 {
		t.Fatalf("an edit must invalidate detail and lists: %+v", cache)
	}

	// A missing version is a validation error: the token is how the write is safe.
	if w := do(r, http.MethodPut, "/product/v1/protected/products/8", map[string]any{
		"name": "Edited", "price": 12,
	}); w.Code != http.StatusBadRequest {
		t.Fatalf("missing version: want 400, got %d", w.Code)
	}

	// A stale version reaches the repository and comes back as VERSION_CONFLICT.
	f.err = domain.ErrVersionConflict
	w = do(r, http.MethodPut, "/product/v1/protected/products/8", map[string]any{
		"name": "Edited", "price": 12, "version": 1,
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("stale version: want 409, got %d", w.Code)
	}
	var envelope struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &envelope)
	if envelope.Code != "VERSION_CONFLICT" {
		t.Fatalf("conflict code = %q, want VERSION_CONFLICT: %s", envelope.Code, w.Body.String())
	}
}

func TestProtectedTransitions(t *testing.T) {
	f := &fakeCatalog{product: &repository.AdminProduct{ID: "8", Status: "ACTIVE", Version: 2}}
	cache := &countingCache{}
	r := engine(t, f, cache, backofficeRole)

	for path, want := range map[string]domain.LifecycleAction{
		"publish": domain.ActionPublish,
		"archive": domain.ActionArchive,
		"restore": domain.ActionRestore,
	} {
		// An empty POST is a valid command — the reason body is optional.
		if w := do(r, http.MethodPost, "/product/v1/protected/products/8/"+path, nil); w.Code != http.StatusOK {
			t.Fatalf("%s: want 200, got %d: %s", path, w.Code, w.Body.String())
		}
		if f.got.action != want {
			t.Fatalf("%s dispatched %s", path, f.got.action)
		}
		if f.got.actor != staffSub {
			t.Fatalf("%s lost the token actor", path)
		}
	}
	if cache.product != 3 {
		t.Fatalf("every transition must invalidate the detail: %+v", cache)
	}

	// An illegal edge is a 409 naming the transition, not a 500.
	f.err = domain.ErrInvalidTransition
	w := do(r, http.MethodPost, "/product/v1/protected/products/8/publish", map[string]any{"reason": "again"})
	if w.Code != http.StatusConflict {
		t.Fatalf("illegal edge: want 409, got %d", w.Code)
	}
	var envelope struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &envelope)
	if envelope.Code != "INVALID_TRANSITION" {
		t.Fatalf("code = %q, want INVALID_TRANSITION", envelope.Code)
	}

	f.err = domain.ErrNotFound
	if w := do(r, http.MethodPost, "/product/v1/protected/products/999/archive", nil); w.Code != http.StatusNotFound {
		t.Fatalf("missing product: want 404, got %d", w.Code)
	}
}

func TestProtectedCategories(t *testing.T) {
	f := &fakeCatalog{}
	r := engine(t, f, nil, backofficeRole)

	if w := do(r, http.MethodGet, "/product/v1/protected/categories?page=1&page_size=50", nil); w.Code != http.StatusOK {
		t.Fatalf("list: want 200, got %d", w.Code)
	}
	if f.got.limit != 50 {
		t.Fatalf("page size not forwarded: %+v", f.got)
	}

	w := do(r, http.MethodPost, "/product/v1/protected/categories", map[string]any{"name": "Audio"})
	if w.Code != http.StatusCreated || f.got.actor != staffSub {
		t.Fatalf("create = %d (actor %q)", w.Code, f.got.actor)
	}

	if w := do(r, http.MethodPut, "/product/v1/protected/categories/3", map[string]any{"name": "Audio Gear"}); w.Code != http.StatusOK {
		t.Fatalf("update: want 200, got %d", w.Code)
	}
	if w := do(r, http.MethodPut, "/product/v1/protected/categories/abc", map[string]any{"name": "x"}); w.Code != http.StatusBadRequest {
		t.Fatalf("bad id: want 400, got %d", w.Code)
	}

	// A duplicate name is the conflict that also makes a retried create safe.
	f.err = domain.ErrConflict
	if w := do(r, http.MethodPost, "/product/v1/protected/categories", map[string]any{"name": "Audio"}); w.Code != http.StatusConflict {
		t.Fatalf("duplicate: want 409, got %d", w.Code)
	}
}

func TestProtectedAuditRead(t *testing.T) {
	f := &fakeCatalog{audit: []repository.AuditRow{{ID: 1, Action: "CREATE", ActorSub: staffSub}}}
	r := engine(t, f, nil, backofficeRole)

	w := do(r, http.MethodGet, "/product/v1/protected/products/8/audit", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	var resp struct {
		Items []repository.AuditRow `json:"items"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Items) != 1 || resp.Items[0].ActorSub != staffSub {
		t.Fatalf("audit body = %s", w.Body.String())
	}

	if w := do(r, http.MethodGet, "/product/v1/protected/products/abc/audit", nil); w.Code != http.StatusBadRequest {
		t.Fatalf("bad id: want 400, got %d", w.Code)
	}
}

func TestProtectedInternalErrorsAreOpaque(t *testing.T) {
	f := &fakeCatalog{err: context.DeadlineExceeded}
	r := engine(t, f, nil, backofficeRole)

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/product/v1/protected/products"},
		{http.MethodGet, "/product/v1/protected/products/1/audit"},
		{http.MethodGet, "/product/v1/protected/categories"},
	} {
		w := do(r, tc.method, tc.path, nil)
		if w.Code != http.StatusInternalServerError {
			t.Errorf("%s: want 500, got %d", tc.path, w.Code)
		}
		if bytes.Contains(w.Body.Bytes(), []byte("deadline")) {
			t.Errorf("%s leaked the driver error: %s", tc.path, w.Body.String())
		}
	}
}

func TestRegisterProtectedRoutesRealChain(t *testing.T) {
	verifier, err := authmw.NewVerifier(authmw.Config{
		Issuer:   "http://localhost:8081/realms/duynhlab-staff",
		Audience: "duynhlab-platform",
	})
	if err != nil {
		t.Fatalf("verifier: %v", err)
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	RegisterProtectedRoutes(r, NewProtectedHandler(&fakeCatalog{}, nil), verifier)

	if w := do(r, http.MethodGet, "/product/v1/protected/products", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("tokenless request through the real chain: want 401, got %d", w.Code)
	}
}
