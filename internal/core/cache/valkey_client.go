// Package cache provides Valkey (Redis-compatible) cache client implementation
package cache

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/extra/redisotel/v9"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
)

// ValkeyCacheClient implements CacheClient interface using Valkey/Redis
type ValkeyCacheClient struct {
	client *redis.Client
}

// NewValkeyCacheClient creates a new Valkey cache client
func NewValkeyCacheClient(addr string, password string, db int) (*ValkeyCacheClient, error) {
	client := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       db,
	})

	// Test connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		return nil, err
	}

	instrumentClient(client)

	return &ValkeyCacheClient{
		client: client,
	}, nil
}

// instrumentClient adds redisotel tracing + metrics so every cache command emits
// a child span (joining the caller's trace) plus go-redis client metrics.
// Best-effort: a telemetry-instrumentation failure is reported via the OTel
// error handler and never disables the cache.
func instrumentClient(rdb redis.UniversalClient) {
	if err := redisotel.InstrumentTracing(rdb); err != nil {
		otel.Handle(err)
	}
	if err := redisotel.InstrumentMetrics(rdb); err != nil {
		otel.Handle(err)
	}
}

// Get retrieves a value from cache by key
func (c *ValkeyCacheClient) Get(ctx context.Context, key string) ([]byte, error) {
	val, err := c.client.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		// Key doesn't exist - cache miss (not an error)
		recordCacheGet(ctx, cacheMiss)
		return nil, nil
	}
	if err != nil {
		recordCacheGet(ctx, cacheError)
		return nil, err
	}
	recordCacheGet(ctx, cacheHit)
	return val, nil
}

// Set stores a value in cache with TTL
func (c *ValkeyCacheClient) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	return c.client.Set(ctx, key, value, ttl).Err()
}

// SetNX stores a value in cache only if the key does not exist
func (c *ValkeyCacheClient) SetNX(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	result, err := c.client.SetArgs(ctx, key, value, redis.SetArgs{
		Mode: "NX",
		TTL:  ttl,
	}).Result()
	if errors.Is(err, redis.Nil) {
		// Key already exists - NX condition not met
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return result == "OK", nil
}

// Delete removes a key from cache
func (c *ValkeyCacheClient) Delete(ctx context.Context, key string) error {
	return c.client.Del(ctx, key).Err()
}

// deleteIfEqualScript deletes key only if its value matches ARGV[1], atomically.
// This is the standard safe-unlock pattern: a worker releases only the lock it
// still owns, so a fetch that overran the lock TTL cannot delete a successor's lock.
var deleteIfEqualScript = redis.NewScript(`
if redis.call("get", KEYS[1]) == ARGV[1] then
	return redis.call("del", KEYS[1])
end
return 0
`)

// DeleteIfEqual removes key only if its current value equals value (compare-and-delete).
func (c *ValkeyCacheClient) DeleteIfEqual(ctx context.Context, key string, value []byte) error {
	return deleteIfEqualScript.Run(ctx, c.client, []string{key}, value).Err()
}

// DeleteByPattern removes all keys matching pattern using a SCAN iterator.
// Matched keys are removed with UNLINK (non-blocking) in batches.
func (c *ValkeyCacheClient) DeleteByPattern(ctx context.Context, pattern string) error {
	const scanCount = 100

	iter := c.client.Scan(ctx, 0, pattern, scanCount).Iterator()
	batch := make([]string, 0, scanCount)
	for iter.Next(ctx) {
		batch = append(batch, iter.Val())
		if len(batch) >= scanCount {
			if err := c.client.Unlink(ctx, batch...).Err(); err != nil {
				return err
			}
			batch = batch[:0]
		}
	}
	if err := iter.Err(); err != nil {
		return err
	}

	if len(batch) > 0 {
		if err := c.client.Unlink(ctx, batch...).Err(); err != nil {
			return err
		}
	}

	return nil
}

// Close closes the cache connection
func (c *ValkeyCacheClient) Close() error {
	return c.client.Close()
}
