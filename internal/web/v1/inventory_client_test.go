package v1

import (
	"context"
	"errors"
	"testing"

	inventoryv1 "github.com/duynhlab/pkg/proto/inventory/v1"
	logicv1 "github.com/duynhlab/product-service/internal/logic/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

// stubInvSvcClient embeds the generated client so only BatchGetAvailability
// needs a body; the other RPCs are unimplemented (never called here).
type stubInvSvcClient struct {
	inventoryv1.InventoryServiceClient
	resp    *inventoryv1.BatchGetAvailabilityResponse
	err     error
	gotSkus []string
}

func (s *stubInvSvcClient) BatchGetAvailability(_ context.Context, in *inventoryv1.BatchGetAvailabilityRequest, _ ...grpc.CallOption) (*inventoryv1.BatchGetAvailabilityResponse, error) {
	s.gotSkus = in.GetSkuIds()
	return s.resp, s.err
}

func TestNewInventoryClient(t *testing.T) {
	if NewInventoryClient(nil) == nil {
		t.Fatal("NewInventoryClient returned nil")
	}
}

func TestInventoryClient_GetAvailability(t *testing.T) {
	t.Run("maps status + ATP and forwards the sku", func(t *testing.T) {
		stub := &stubInvSvcClient{resp: &inventoryv1.BatchGetAvailabilityResponse{
			Availabilities: []*inventoryv1.SkuAvailability{
				{SkuId: "15", AvailableToPromise: 42, Status: inventoryv1.AvailabilityStatus_AVAILABILITY_STATUS_LOW_STOCK},
			},
		}}
		c := &InventoryClient{client: stub}

		got, err := c.GetAvailability(context.Background(), "15", zap.NewNop())
		if err != nil {
			t.Fatalf("GetAvailability err = %v", err)
		}
		if len(stub.gotSkus) != 1 || stub.gotSkus[0] != "15" {
			t.Errorf("forwarded skus = %v, want [15]", stub.gotSkus)
		}
		if got.Status != "low_stock" || got.AvailableToPromise != 42 {
			t.Errorf("got %+v, want low_stock/42", got)
		}
	})

	t.Run("untracked sku reports unknown, not error", func(t *testing.T) {
		stub := &stubInvSvcClient{resp: &inventoryv1.BatchGetAvailabilityResponse{}} // empty
		c := &InventoryClient{client: stub}

		got, err := c.GetAvailability(context.Background(), "nope", zap.NewNop())
		if err != nil {
			t.Fatalf("untracked sku must not error: %v", err)
		}
		if got.Status != logicv1.AvailabilityUnknown {
			t.Errorf("got %+v, want unknown", got)
		}
	})

	t.Run("transport error propagates", func(t *testing.T) {
		c := &InventoryClient{client: &stubInvSvcClient{err: errors.New("timeout")}}
		if _, err := c.GetAvailability(context.Background(), "15", zap.NewNop()); err == nil {
			t.Fatal("transport error must propagate")
		}
	})
}

func TestAvailabilityStatusString(t *testing.T) {
	cases := map[inventoryv1.AvailabilityStatus]string{
		inventoryv1.AvailabilityStatus_AVAILABILITY_STATUS_IN_STOCK:     "in_stock",
		inventoryv1.AvailabilityStatus_AVAILABILITY_STATUS_LOW_STOCK:    "low_stock",
		inventoryv1.AvailabilityStatus_AVAILABILITY_STATUS_OUT_OF_STOCK: "out_of_stock",
		inventoryv1.AvailabilityStatus_AVAILABILITY_STATUS_UNKNOWN:      logicv1.AvailabilityUnknown,
		inventoryv1.AvailabilityStatus_AVAILABILITY_STATUS_UNSPECIFIED:  logicv1.AvailabilityUnknown,
	}
	for in, want := range cases {
		if got := availabilityStatusString(in); got != want {
			t.Errorf("availabilityStatusString(%v) = %q, want %q", in, got, want)
		}
	}
}
