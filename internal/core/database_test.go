package database

import (
	"context"
	"testing"
	"time"

	"github.com/duynhlab/product-service/config"
)

// TestConnect_ParseError verifies Connect returns an error when the DSN is
// invalid. An unknown sslmode value makes pgxpool.ParseConfig reject the DSN,
// so Connect fails before ever opening a socket.
func TestConnect_ParseError(t *testing.T) {
	cfg := config.DatabaseConfig{
		Host:    "127.0.0.1",
		Port:    "5432",
		Name:    "product",
		User:    "product",
		SSLMode: "bogus",
	}

	pool, err := Connect(context.Background(), cfg)
	if err == nil {
		if pool != nil {
			pool.Close()
		}
		t.Fatal("Connect() error = nil, want non-nil for invalid sslmode")
	}
	if pool != nil {
		t.Fatalf("Connect() pool = %v, want nil on error", pool)
	}
}

// TestConnect_PingError verifies Connect returns an error when the database is
// unreachable. Pointing at a closed port makes pool.Ping fail (connection
// refused), which Connect surfaces after closing the pool.
func TestConnect_PingError(t *testing.T) {
	cfg := config.DatabaseConfig{
		Host:           "127.0.0.1",
		Port:           "1",
		Name:           "product",
		User:           "product",
		SSLMode:        "disable",
		MaxConnections: 25,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	pool, err := Connect(ctx, cfg)
	if err == nil {
		if pool != nil {
			pool.Close()
		}
		t.Fatal("Connect() error = nil, want non-nil for unreachable host")
	}
	if pool != nil {
		t.Fatalf("Connect() pool = %v, want nil on error", pool)
	}
}
