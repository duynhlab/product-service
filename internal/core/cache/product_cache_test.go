package cache

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/duynhlab/product-service/internal/core/domain"
)

// MockCacheClient for testing
type MockCacheClient struct {
	data      map[string][]byte
	locks     map[string][]byte
	mu        sync.Mutex
	setNXCall int32
}

func NewMockCacheClient() *MockCacheClient {
	return &MockCacheClient{
		data:  make(map[string][]byte),
		locks: make(map[string][]byte),
	}
}

func (m *MockCacheClient) Get(ctx context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if val, ok := m.data[key]; ok {
		return val, nil
	}
	return nil, nil
}

func (m *MockCacheClient) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = value
	return nil
}

func (m *MockCacheClient) SetNX(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	atomic.AddInt32(&m.setNXCall, 1)
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, held := m.locks[key]; held {
		return false, nil
	}
	m.locks[key] = value
	return true, nil
}

func (m *MockCacheClient) Delete(ctx context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	delete(m.locks, key) // Also release lock if deleting key (simplified)
	return nil
}

func (m *MockCacheClient) DeleteIfEqual(ctx context.Context, key string, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if v, ok := m.locks[key]; ok && bytes.Equal(v, value) {
		delete(m.locks, key)
	}
	return nil
}

func (m *MockCacheClient) DeleteByPattern(ctx context.Context, pattern string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	prefix := strings.TrimSuffix(pattern, "*")
	for key := range m.data {
		if strings.HasPrefix(key, prefix) {
			delete(m.data, key)
		}
	}
	return nil
}

func (m *MockCacheClient) Close() error {
	return nil
}

// TestGetProductOrSet_StampedePrevention verifies that multiple concurrent calls
// only trigger a single DB fetch
func TestGetProductOrSet_StampedePrevention(t *testing.T) {
	mockClient := NewMockCacheClient()
	productCache := NewProductCache(mockClient, 5*time.Minute, 10*time.Minute)
	ctx := context.Background()
	productID := "123"

	// Counter for DB fetch calls
	var dbFetchCalls int32

	// Simulated DB fetch function
	fetchFunc := func() (*domain.Product, error) {
		atomic.AddInt32(&dbFetchCalls, 1)
		time.Sleep(100 * time.Millisecond) // Simulate DB latency
		return &domain.Product{
			ID:   productID,
			Name: "Test Product",
		}, nil
	}

	// Concurrent requests
	concurrency := 10
	var wg sync.WaitGroup
	wg.Add(concurrency)

	start := time.Now()
	for i := 0; i < concurrency; i++ {
		go func() {
			defer wg.Done()
			product, err := productCache.GetProductOrSet(ctx, productID, fetchFunc)
			if err != nil {
				t.Errorf("GetProductOrSet failed: %v", err)
			}
			if product == nil || product.ID != productID {
				t.Errorf("Expected product ID %s, got %v", productID, product)
			}
		}()
	}

	wg.Wait()
	duration := time.Since(start)

	// Verification
	calls := atomic.LoadInt32(&dbFetchCalls)
	if calls != 1 {
		t.Errorf("Stampede Prevention FAILED: Expected 1 DB fetch, got %d", calls)
	} else {
		t.Logf("Stampede Prevention PASSED: %d concurrent requests -> %d DB fetch", concurrency, calls)
	}

	t.Logf("Total time: %v", duration)
}

// TestGetProductOrSet_CacheHit verifies cache hit behavior
func TestGetProductOrSet_CacheHit(t *testing.T) {
	mockClient := NewMockCacheClient()
	productCache := NewProductCache(mockClient, 5*time.Minute, 10*time.Minute)
	ctx := context.Background()
	productID := "123"

	// Pre-populate cache
	product := &domain.Product{ID: productID, Name: "Cached Product"}
	data, _ := json.Marshal(product)
	mockClient.Set(ctx, "product:"+productID, data, 0)

	// Fetch
	dbFetchCalls := 0
	fetchFunc := func() (*domain.Product, error) {
		dbFetchCalls++
		return nil, errors.New("should not be called")
	}

	p, err := productCache.GetProductOrSet(ctx, productID, fetchFunc)
	if err != nil {
		t.Fatalf("Failed to get product: %v", err)
	}

	if p.Name != "Cached Product" {
		t.Errorf("Expected 'Cached Product', got '%s'", p.Name)
	}
	if dbFetchCalls != 0 {
		t.Errorf("Expected 0 DB fetches, got %d", dbFetchCalls)
	}
}

// failingCacheClient simulates a Valkey outage: every operation errors.
type failingCacheClient struct{}

func (failingCacheClient) Get(context.Context, string) ([]byte, error) {
	return nil, errors.New("cache down")
}
func (failingCacheClient) Set(context.Context, string, []byte, time.Duration) error {
	return errors.New("cache down")
}
func (failingCacheClient) SetNX(context.Context, string, []byte, time.Duration) (bool, error) {
	return false, errors.New("cache down")
}
func (failingCacheClient) Delete(context.Context, string) error { return errors.New("cache down") }
func (failingCacheClient) DeleteIfEqual(context.Context, string, []byte) error {
	return errors.New("cache down")
}
func (failingCacheClient) DeleteByPattern(context.Context, string) error {
	return errors.New("cache down")
}
func (failingCacheClient) Close() error { return nil }

// TestGetProductOrSet_FailOpenOnCacheError verifies that a cache outage degrades
// to the DB (fetchFunc) instead of failing the read.
func TestGetProductOrSet_FailOpenOnCacheError(t *testing.T) {
	pc := NewProductCache(failingCacheClient{}, time.Minute, time.Minute)
	calls := 0
	got, err := pc.GetProductOrSet(context.Background(), "1", func() (*domain.Product, error) {
		calls++
		return &domain.Product{ID: "1", Name: "FromDB"}, nil
	})
	if err != nil {
		t.Fatalf("expected fail-open (nil error) on cache outage, got %v", err)
	}
	if got == nil || got.Name != "FromDB" {
		t.Fatalf("expected DB product, got %v", got)
	}
	if calls != 1 {
		t.Fatalf("expected exactly 1 DB fetch, got %d", calls)
	}
}

// TestDeleteIfEqual_OnlyOwnLock verifies compare-and-delete semantics: a wrong
// token must not release another owner's lock.
func TestDeleteIfEqual_OnlyOwnLock(t *testing.T) {
	m := NewMockCacheClient()
	ctx := context.Background()
	if ok, _ := m.SetNX(ctx, "lock:x", []byte("tokenA"), time.Minute); !ok {
		t.Fatal("expected lock acquired")
	}
	// Wrong token must NOT release the lock.
	_ = m.DeleteIfEqual(ctx, "lock:x", []byte("tokenB"))
	if ok, _ := m.SetNX(ctx, "lock:x", []byte("tokenC"), time.Minute); ok {
		t.Fatal("lock should still be held after a wrong-token release")
	}
	// Correct token releases it.
	_ = m.DeleteIfEqual(ctx, "lock:x", []byte("tokenA"))
	if ok, _ := m.SetNX(ctx, "lock:x", []byte("tokenC"), time.Minute); !ok {
		t.Fatal("lock should be re-acquirable after the owner releases it")
	}
}

// TestGenerateProductListKey_NoCollision verifies that filter values containing
// the old ':' separator no longer collide onto one cache key.
func TestGenerateProductListKey_NoCollision(t *testing.T) {
	pc := NewProductCache(NewMockCacheClient(), time.Minute, time.Minute)
	k1 := pc.generateProductListKey(domain.ProductFilters{Search: "a:b"})
	k2 := pc.generateProductListKey(domain.ProductFilters{Category: "a", Search: "b"})
	if k1 == k2 {
		t.Fatalf("distinct filter sets must not share a key: %q == %q", k1, k2)
	}
	for _, k := range []string{k1, k2} {
		if !strings.HasPrefix(k, "product:list:") {
			t.Fatalf("key %q missing product:list: prefix", k)
		}
	}
	if pc.generateProductListKey(domain.ProductFilters{Search: "a:b"}) != k1 {
		t.Fatal("key generation must be stable for the same filters")
	}
}

// TestJitter_WithinBounds verifies jitter stays in [d, d + 10%].
func TestJitter_WithinBounds(t *testing.T) {
	d := 100 * time.Second
	upper := d + d/10 + 1
	for i := 0; i < 1000; i++ {
		j := jitter(d)
		if j < d || j > upper {
			t.Fatalf("jitter %v out of bounds [%v, %v]", j, d, upper)
		}
	}
	if jitter(0) != 0 {
		t.Fatal("jitter(0) must be 0")
	}
}
