package cache

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

// newTestClient spins up an in-process miniredis and returns a connected
// ValkeyCacheClient. miniredis speaks enough RESP for every operation the
// client uses (GET/SET/SET NX/DEL/UNLINK/SCAN/EVAL), so the concrete client is
// unit-testable without Docker.
func newTestClient(t *testing.T) (*ValkeyCacheClient, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	c, err := NewValkeyCacheClient(mr.Addr(), "", 0)
	if err != nil {
		t.Fatalf("NewValkeyCacheClient: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, mr
}

func TestNewValkeyCacheClient_Unreachable(t *testing.T) {
	if _, err := NewValkeyCacheClient("127.0.0.1:1", "", 0); err == nil {
		t.Fatal("expected connection error for an unreachable address")
	}
}

func TestValkeyCacheClient_GetSet(t *testing.T) {
	c, _ := newTestClient(t)
	ctx := context.Background()

	// Miss is (nil, nil) — not an error.
	got, err := c.Get(ctx, "absent")
	if err != nil || got != nil {
		t.Fatalf("Get(absent) = (%v, %v), want (nil, nil)", got, err)
	}

	if err := c.Set(ctx, "k", []byte("v"), time.Minute); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err = c.Get(ctx, "k")
	if err != nil || string(got) != "v" {
		t.Fatalf("Get(k) = (%q, %v), want (v, nil)", got, err)
	}
}

func TestValkeyCacheClient_SetNX(t *testing.T) {
	c, _ := newTestClient(t)
	ctx := context.Background()

	ok, err := c.SetNX(ctx, "lock", []byte("owner-1"), time.Minute)
	if err != nil || !ok {
		t.Fatalf("first SetNX = (%v, %v), want (true, nil)", ok, err)
	}
	// Second NX on an existing key must report false without error.
	ok, err = c.SetNX(ctx, "lock", []byte("owner-2"), time.Minute)
	if err != nil || ok {
		t.Fatalf("second SetNX = (%v, %v), want (false, nil)", ok, err)
	}
	got, _ := c.Get(ctx, "lock")
	if string(got) != "owner-1" {
		t.Errorf("lock value = %q, want owner-1 (unchanged)", got)
	}
}

func TestValkeyCacheClient_Delete(t *testing.T) {
	c, _ := newTestClient(t)
	ctx := context.Background()

	if err := c.Set(ctx, "k", []byte("v"), time.Minute); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := c.Delete(ctx, "k"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got, _ := c.Get(ctx, "k"); got != nil {
		t.Errorf("Get after delete = %q, want nil", got)
	}
	// Deleting an absent key is a no-op.
	if err := c.Delete(ctx, "absent"); err != nil {
		t.Errorf("Delete(absent): %v", err)
	}
}

func TestValkeyCacheClient_DeleteIfEqual(t *testing.T) {
	c, _ := newTestClient(t)
	ctx := context.Background()

	if err := c.Set(ctx, "lock", []byte("mine"), time.Minute); err != nil {
		t.Fatalf("Set: %v", err)
	}
	// Wrong value: key must survive (safe-unlock semantics).
	if err := c.DeleteIfEqual(ctx, "lock", []byte("theirs")); err != nil {
		t.Fatalf("DeleteIfEqual(mismatch): %v", err)
	}
	if got, _ := c.Get(ctx, "lock"); string(got) != "mine" {
		t.Errorf("lock after mismatched delete = %q, want mine", got)
	}
	// Matching value: key is removed.
	if err := c.DeleteIfEqual(ctx, "lock", []byte("mine")); err != nil {
		t.Fatalf("DeleteIfEqual(match): %v", err)
	}
	if got, _ := c.Get(ctx, "lock"); got != nil {
		t.Errorf("lock after matched delete = %q, want nil", got)
	}
}

func TestValkeyCacheClient_DeleteByPattern(t *testing.T) {
	c, _ := newTestClient(t)
	ctx := context.Background()

	for _, k := range []string{"product:1", "product:2", "product:3", "category:1"} {
		if err := c.Set(ctx, k, []byte("v"), time.Minute); err != nil {
			t.Fatalf("Set %s: %v", k, err)
		}
	}
	if err := c.DeleteByPattern(ctx, "product:*"); err != nil {
		t.Fatalf("DeleteByPattern: %v", err)
	}
	for _, k := range []string{"product:1", "product:2", "product:3"} {
		if got, _ := c.Get(ctx, k); got != nil {
			t.Errorf("%s survived DeleteByPattern", k)
		}
	}
	if got, _ := c.Get(ctx, "category:1"); string(got) != "v" {
		t.Errorf("category:1 = %q, want untouched", got)
	}
	// No matches is a no-op.
	if err := c.DeleteByPattern(ctx, "nothing:*"); err != nil {
		t.Errorf("DeleteByPattern(no matches): %v", err)
	}
}
