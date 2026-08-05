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

type stubCatalogReader struct {
	products []domain.Product
	batchErr error
}

func (s *stubCatalogReader) GetProductsByIDs(_ context.Context, _ []string) ([]domain.Product, error) {
	return s.products, s.batchErr
}

func TestBatchGetCurrentPrices_MapsToCurrentPrice(t *testing.T) {
	stub := &stubCatalogReader{products: []domain.Product{
		{ID: "1", Name: "Wireless Mouse", Price: 29.99},
		{ID: "3", Name: "USB-C Hub", Price: 39.5},
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
	_, err := NewServer(&stubCatalogReader{}).BatchGetCurrentPrices(context.Background(),
		&productv1.BatchGetCurrentPricesRequest{})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", status.Code(err))
	}
}

func TestBatchGetCurrentPrices_LogicErrorIsOpaqueInternal(t *testing.T) {
	stub := &stubCatalogReader{batchErr: errors.New("pq: relation missing")}
	_, err := NewServer(stub).BatchGetCurrentPrices(context.Background(),
		&productv1.BatchGetCurrentPricesRequest{SkuIds: []string{"1"}})
	st := status.Convert(err)
	if st.Code() != codes.Internal || st.Message() != "get current prices failed" {
		t.Errorf("got (%v, %q), want (Internal, \"get current prices failed\")", st.Code(), st.Message())
	}
}

func TestBatchGetCurrentPrices_BatchCapIsInvalidArgument(t *testing.T) {
	ids := make([]string, maxPriceBatch+1)
	for i := range ids {
		ids[i] = "1"
	}
	_, err := NewServer(&stubCatalogReader{}).BatchGetCurrentPrices(context.Background(),
		&productv1.BatchGetCurrentPricesRequest{SkuIds: ids})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument (batch cap)", status.Code(err))
	}
}
