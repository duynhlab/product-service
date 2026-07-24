package v1

import (
	"context"
	"errors"
	"testing"

	productv1 "github.com/duynhlab/pkg/proto/product/v1"
	"github.com/duynhlab/product-service/internal/core/domain"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type stubStockManager struct {
	reserveErr error
	releaseErr error
	gotResID   string
	gotItems   []domain.ReservationItem
	products   []domain.Product
	batchErr   error
}

func (s *stubStockManager) GetProductsByIDs(_ context.Context, _ []string) ([]domain.Product, error) {
	return s.products, s.batchErr
}

func (s *stubStockManager) ReserveStock(_ context.Context, id string, items []domain.ReservationItem) error {
	s.gotResID = id
	s.gotItems = items
	return s.reserveErr
}

func (s *stubStockManager) ReleaseStock(_ context.Context, id string) error {
	s.gotResID = id
	return s.releaseErr
}

func reserveReq() *productv1.ReserveStockRequest {
	return &productv1.ReserveStockRequest{
		ReservationId: "order-7",
		Items:         []*productv1.StockItem{{ProductId: "1", Quantity: 3}},
	}
}

func TestServer_ReserveStock(t *testing.T) {
	tests := []struct {
		name     string
		req      *productv1.ReserveStockRequest
		svcErr   error
		wantCode codes.Code // OK means no error
	}{
		{"success", reserveReq(), nil, codes.OK},
		{"insufficient stock -> FailedPrecondition", reserveReq(), domain.ErrInsufficientStock, codes.FailedPrecondition},
		{"repo error -> Internal", reserveReq(), errors.New("db down"), codes.Internal},
		{"missing reservation_id -> InvalidArgument", &productv1.ReserveStockRequest{Items: reserveReq().Items}, nil, codes.InvalidArgument},
		{"zero quantity -> InvalidArgument", &productv1.ReserveStockRequest{ReservationId: "order-7", Items: []*productv1.StockItem{{ProductId: "1", Quantity: 0}}}, nil, codes.InvalidArgument},
		{"empty product_id -> InvalidArgument", &productv1.ReserveStockRequest{ReservationId: "order-7", Items: []*productv1.StockItem{{ProductId: "", Quantity: 1}}}, nil, codes.InvalidArgument},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := NewServer(&stubStockManager{reserveErr: tc.svcErr})
			_, err := srv.ReserveStock(context.Background(), tc.req)

			if tc.wantCode == codes.OK {
				if err != nil {
					t.Fatalf("got error %v, want nil", err)
				}
				return
			}
			if status.Code(err) != tc.wantCode {
				t.Fatalf("got code %v (err %v), want %v", status.Code(err), err, tc.wantCode)
			}
		})
	}
}

func TestServer_ReleaseStock(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		stub := &stubStockManager{}
		srv := NewServer(stub)
		if _, err := srv.ReleaseStock(context.Background(), &productv1.ReleaseStockRequest{ReservationId: "order-7"}); err != nil {
			t.Fatalf("got error %v, want nil", err)
		}
		if stub.gotResID != "order-7" {
			t.Errorf("svc got reservationID %q, want order-7", stub.gotResID)
		}
	})

	t.Run("missing reservation_id -> InvalidArgument", func(t *testing.T) {
		srv := NewServer(&stubStockManager{})
		_, err := srv.ReleaseStock(context.Background(), &productv1.ReleaseStockRequest{})
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("got code %v, want InvalidArgument", status.Code(err))
		}
	})

	t.Run("repo error -> Internal", func(t *testing.T) {
		srv := NewServer(&stubStockManager{releaseErr: errors.New("db down")})
		_, err := srv.ReleaseStock(context.Background(), &productv1.ReleaseStockRequest{ReservationId: "order-7"})
		if status.Code(err) != codes.Internal {
			t.Fatalf("got code %v, want Internal", status.Code(err))
		}
	})
}

func TestGetProducts_MapsToMinorUnits(t *testing.T) {
	stub := &stubStockManager{products: []domain.Product{
		{ID: "1", Name: "Wireless Mouse", Price: 29.99, StockQuantity: 42},
		{ID: "3", Name: "USB-C Hub", Price: 39.99, StockQuantity: 0},
	}}
	resp, err := NewServer(stub).GetProducts(context.Background(),
		&productv1.GetProductsRequest{ProductIds: []string{"1", "3", "999"}})
	if err != nil {
		t.Fatalf("GetProducts() error = %v", err)
	}
	if len(resp.Products) != 2 {
		t.Fatalf("products = %d, want 2 (unknown ids omitted)", len(resp.Products))
	}
	p := resp.Products[0]
	if p.ProductId != "1" || p.Name != "Wireless Mouse" || p.PriceMinor != 2999 || p.AvailableQty != 42 {
		t.Errorf("product[0] = %+v, want id=1 price_minor=2999 qty=42", p)
	}
	if resp.Products[1].AvailableQty != 0 {
		t.Errorf("out-of-stock qty = %d, want 0", resp.Products[1].AvailableQty)
	}
}

func TestGetProducts_EmptyIDsIsInvalidArgument(t *testing.T) {
	_, err := NewServer(&stubStockManager{}).GetProducts(context.Background(), &productv1.GetProductsRequest{})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", status.Code(err))
	}
}

func TestGetProducts_LogicErrorIsOpaqueInternal(t *testing.T) {
	stub := &stubStockManager{batchErr: errors.New("pq: relation missing")}
	_, err := NewServer(stub).GetProducts(context.Background(),
		&productv1.GetProductsRequest{ProductIds: []string{"1"}})
	st := status.Convert(err)
	if st.Code() != codes.Internal || st.Message() != "get products failed" {
		t.Errorf("got (%v, %q), want (Internal, \"get products failed\")", st.Code(), st.Message())
	}
}

func TestGetProducts_BatchCapIsInvalidArgument(t *testing.T) {
	ids := make([]string, maxGetProductsBatch+1)
	for i := range ids {
		ids[i] = "1"
	}
	_, err := NewServer(&stubStockManager{}).GetProducts(context.Background(),
		&productv1.GetProductsRequest{ProductIds: ids})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument (batch cap)", status.Code(err))
	}
}

// TestBatchGetCurrentPrices_MapsToCurrentPrice pins the RFC-0021 price-only
// read: the DB row (surfaced through the same cache-bypassing GetProductsByIDs
// as GetProducts) maps straight to CurrentPrice with no stock fields. Unknown
// SKUs are omitted, mirroring GetProducts. Every existing row is sellable —
// the catalog has no lifecycle column yet (see server.go for the gap note).
func TestBatchGetCurrentPrices_MapsToCurrentPrice(t *testing.T) {
	stub := &stubStockManager{products: []domain.Product{
		{ID: "1", Name: "Wireless Mouse", Price: 29.99, StockQuantity: 42},
		{ID: "3", Name: "USB-C Hub", Price: 39.5, StockQuantity: 0},
	}}
	resp, err := NewServer(stub).BatchGetCurrentPrices(context.Background(),
		&productv1.BatchGetCurrentPricesRequest{SkuIds: []string{"1", "3", "999"}})
	if err != nil {
		t.Fatalf("BatchGetCurrentPrices() error = %v", err)
	}
	if len(resp.Prices) != 2 {
		t.Fatalf("prices = %d, want 2 (unknown SKUs omitted)", len(resp.Prices))
	}
	p := resp.Prices[0]
	if p.SkuId != "1" || p.Name != "Wireless Mouse" || p.PriceMinor != 2999 ||
		p.Currency != "USD" || !p.Sellable {
		t.Errorf("price[0] = %+v, want sku=1 name=\"Wireless Mouse\" price_minor=2999 currency=USD sellable=true", p)
	}
	// Float dollars round to int64 minor units, same conversion as GetProducts.
	if resp.Prices[1].PriceMinor != 3950 {
		t.Errorf("price[1].price_minor = %d, want 3950", resp.Prices[1].PriceMinor)
	}
	// Every existing row is sellable — no lifecycle column in the catalog yet.
	if !resp.Prices[1].Sellable {
		t.Errorf("price[1].sellable = false, want true (every existing row sellable)")
	}
}

func TestBatchGetCurrentPrices_EmptyIDsIsInvalidArgument(t *testing.T) {
	_, err := NewServer(&stubStockManager{}).BatchGetCurrentPrices(context.Background(),
		&productv1.BatchGetCurrentPricesRequest{})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", status.Code(err))
	}
}

func TestBatchGetCurrentPrices_LogicErrorIsOpaqueInternal(t *testing.T) {
	stub := &stubStockManager{batchErr: errors.New("pq: relation missing")}
	_, err := NewServer(stub).BatchGetCurrentPrices(context.Background(),
		&productv1.BatchGetCurrentPricesRequest{SkuIds: []string{"1"}})
	st := status.Convert(err)
	if st.Code() != codes.Internal || st.Message() != "get current prices failed" {
		t.Errorf("got (%v, %q), want (Internal, \"get current prices failed\")", st.Code(), st.Message())
	}
}

func TestBatchGetCurrentPrices_BatchCapIsInvalidArgument(t *testing.T) {
	ids := make([]string, maxGetProductsBatch+1)
	for i := range ids {
		ids[i] = "1"
	}
	_, err := NewServer(&stubStockManager{}).BatchGetCurrentPrices(context.Background(),
		&productv1.BatchGetCurrentPricesRequest{SkuIds: ids})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument (batch cap)", status.Code(err))
	}
}
