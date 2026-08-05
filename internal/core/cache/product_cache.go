// Package cache provides Product-specific cache wrapper
// Handles cache key generation and JSON serialization/deserialization
package cache

import (
	"context"
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/duynhlab/product-service/internal/core/domain"
)

// jitter returns d increased by a random 0–10% so that keys created together do
// not all expire at the same instant (avoids synchronized expiry stampedes).
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	//nolint:gosec // G404: cache TTL jitter is not security-sensitive; weak RNG is fine.
	return d + time.Duration(rand.Int64N(int64(d)/10+1))
}

// newLockToken returns a random hex token identifying a lock acquisition, so the
// holder can release only its own lock (compare-and-delete).
func newLockToken() []byte {
	b := make([]byte, 16)
	_, _ = crand.Read(b)
	dst := make([]byte, hex.EncodedLen(len(b)))
	hex.Encode(dst, b)
	return dst
}

// ProductCache wraps CacheClient with Product-specific operations
type ProductCache struct {
	client    CacheClient
	ttlList   time.Duration
	ttlDetail time.Duration
}

// NewProductCache creates a new ProductCache wrapper
func NewProductCache(client CacheClient, ttlList, ttlDetail time.Duration) *ProductCache {
	return &ProductCache{
		client:    client,
		ttlList:   ttlList,
		ttlDetail: ttlDetail,
	}
}

// generateProductKey generates cache key for a single product
func (c *ProductCache) generateProductKey(id string) string {
	return "product:" + id
}

// generateProductListKey generates cache key for product list with filters
func (c *ProductCache) generateProductListKey(filters domain.ProductFilters) string {
	// Normalize filters for consistent key generation
	category := filters.Category
	if category == "" {
		category = "all"
	}
	search := filters.Search
	if search == "" {
		search = "none"
	}
	sortBy := filters.SortBy
	if sortBy == "" {
		sortBy = "created_at"
	}
	order := filters.Order
	if order == "" {
		order = "desc"
	}
	page := filters.Page
	if page == 0 {
		page = 1
	}
	limit := filters.Limit
	if limit == 0 {
		limit = 20
	}

	// Hash the canonical filter tuple rather than concatenating raw values: a
	// free-text search containing the separator (e.g. "a:b") would otherwise
	// collide with a different filter combination and serve the wrong result set.
	// The "product:list:" prefix is preserved so InvalidateProductList's
	// "product:list:*" SCAN still matches every variant.
	raw := fmt.Sprintf("%s\x1f%s\x1f%s\x1f%s\x1f%d\x1f%d", category, search, sortBy, order, page, limit)
	sum := sha256.Sum256([]byte(raw))
	return "product:list:" + hex.EncodeToString(sum[:])
}

// GetProduct retrieves a single product from cache
// Returns nil, nil if cache miss (not an error)
func (c *ProductCache) GetProduct(ctx context.Context, id string) (*domain.Product, error) {
	key := c.generateProductKey(id)
	data, err := c.client.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	if data == nil {
		// Cache miss
		return nil, nil
	}

	var product domain.Product
	if err := json.Unmarshal(data, &product); err != nil {
		return nil, fmt.Errorf("failed to unmarshal cached product: %w", err)
	}

	return &product, nil
}

// SetProduct stores a single product in cache
func (c *ProductCache) SetProduct(ctx context.Context, id string, product *domain.Product) error {
	key := c.generateProductKey(id)
	data, err := json.Marshal(product)
	if err != nil {
		return fmt.Errorf("failed to marshal product: %w", err)
	}

	return c.client.Set(ctx, key, data, jitter(c.ttlDetail))
}

// GetProductOrSet retrieves a product from cache or fetches it using the provided function
// Implements Cache Stampede Prevention using distributed locking
func (c *ProductCache) GetProductOrSet(ctx context.Context, id string, fetchFunc func() (*domain.Product, error)) (*domain.Product, error) {
	// 1. Check cache first. A cache error is treated as a miss (fail-open): the
	//    read degrades to the DB instead of failing during a Valkey outage.
	if product, err := c.GetProduct(ctx, id); err == nil && product != nil {
		return product, nil
	}

	// 2. Cache miss (or cache unavailable) - try to acquire a lock tagged with a
	//    unique token so only this owner can release it.
	lockKey := "lock:product:" + id
	token := newLockToken()
	acquired, err := c.client.SetNX(ctx, lockKey, token, 5*time.Second) // 5s lock TTL
	if err != nil {
		// Lock store unavailable - degrade straight to the DB rather than fail.
		return fetchFunc()
	}

	if acquired {
		// 3a. Lock acquired - owner fetches and populates the cache.
		// Release via compare-and-delete so a fetch that overran the lock TTL
		// cannot delete a successor's lock.
		defer func() { _ = c.client.DeleteIfEqual(ctx, lockKey, token) }()

		product, err := fetchFunc()
		if err != nil {
			return nil, err
		}

		_ = c.SetProduct(ctx, id, product) // best-effort; return product either way
		return product, nil
	}

	// 3b. Lock held by another worker - wait briefly for it to populate the cache.
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	timeout := time.After(500 * time.Millisecond)
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timeout:
			// Timed out waiting for the holder - fall back to the DB to keep the
			// request available (best-effort; under a slow DB this can let
			// multiple waiters through — see docs/caching/caching.md).
			return fetchFunc()
		case <-ticker.C:
			if product, err := c.GetProduct(ctx, id); err == nil && product != nil {
				return product, nil
			}
		}
	}
}

// GetProductList retrieves product list from cache
// Returns nil, 0, nil if cache miss (not an error)
func (c *ProductCache) GetProductList(ctx context.Context, filters domain.ProductFilters) ([]domain.Product, int, error) {
	key := c.generateProductListKey(filters)
	data, err := c.client.Get(ctx, key)
	if err != nil {
		return nil, 0, err
	}
	if data == nil {
		// Cache miss
		return nil, 0, nil
	}

	var result struct {
		Products []domain.Product `json:"products"`
		Total    int              `json:"total"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, 0, fmt.Errorf("failed to unmarshal cached product list: %w", err)
	}

	return result.Products, result.Total, nil
}

// SetProductList stores product list in cache
func (c *ProductCache) SetProductList(ctx context.Context, filters domain.ProductFilters, products []domain.Product, total int) error {
	key := c.generateProductListKey(filters)
	result := struct {
		Products []domain.Product `json:"products"`
		Total    int              `json:"total"`
	}{
		Products: products,
		Total:    total,
	}

	data, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("failed to marshal product list: %w", err)
	}

	return c.client.Set(ctx, key, data, jitter(c.ttlList))
}

// InvalidateProductList deletes all product list cache keys
// Uses a SCAN over the "product:list:*" pattern so every cached list
// variant is invalidated, not just a hardcoded subset.
func (c *ProductCache) InvalidateProductList(ctx context.Context) error {
	return c.client.DeleteByPattern(ctx, "product:list:*")
}
