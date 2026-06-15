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
