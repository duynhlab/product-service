package domain

import "context"

// ProductRepository defines the interface for product data access
type ProductRepository interface {
	// Basic CRUD operations
	FindByID(ctx context.Context, id string) (*Product, error)
	FindAll(ctx context.Context, filters ProductFilters) ([]Product, error)
	Create(ctx context.Context, product *Product) error
	Update(ctx context.Context, product *Product) error
	Delete(ctx context.Context, id string) error

	// Aggregation support for BFF endpoints
	FindRelatedProducts(ctx context.Context, productID string, limit int) ([]Product, error)

	// Count returns the total number of products matching the filters
	Count(ctx context.Context, filters ProductFilters) (int, error)

	// Inventory operations for the order-fulfillment saga (Temporal).

	// ReserveStock atomically decrements stock for every item and records the
	// reservation in one transaction. It is all-or-nothing: if any item lacks
	// stock, nothing is reserved and ErrInsufficientStock is returned. Idempotent
	// by reservationID — a repeat call reserves at most once.
	ReserveStock(ctx context.Context, reservationID string, items []ReservationItem) error

	// ReleaseStock restores stock for an active reservation and marks it released
	// (the compensation for ReserveStock). Idempotent: a no-op when the
	// reservation is unknown or already released. The recorded reservation is the
	// source of truth for what to restore. Returns the ids of the products whose
	// stock was restored, so callers can invalidate their caches (empty on a no-op).
	ReleaseStock(ctx context.Context, reservationID string) ([]string, error)
}

// ReservationItem is a product/quantity pair within a stock reservation.
type ReservationItem struct {
	ProductID string
	Quantity  int
}

// ProductFilters defines filtering options for product queries
type ProductFilters struct {
	Category string
	Search   string
	SortBy   string // e.g., "price", "created_at", "name"
	Order    string // "asc" or "desc"
	Page     int
	Limit    int
}
