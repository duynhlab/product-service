package domain

import "context"

// ProductRepository defines the interface for product data access.
//
// It has no stock anything: RFC-0021 phase 4 removed the operations, and then the
// reads. inventory-service is the authority, and no query here names
// products.stock_quantity any more — which is what makes dropping that column
// possible.
type ProductRepository interface {
	// Basic CRUD operations
	FindByID(ctx context.Context, id string) (*Product, error)
	// FindByIDs is the batch read for checkout re-validation (RFC-0015).
	// Unknown or non-numeric ids are omitted from the result, not errors.
	FindByIDs(ctx context.Context, ids []string) ([]Product, error)
	FindAll(ctx context.Context, filters ProductFilters) ([]Product, error)
	Create(ctx context.Context, product *Product) error
	Update(ctx context.Context, product *Product) error
	Delete(ctx context.Context, id string) error

	// Aggregation support for BFF endpoints
	FindRelatedProducts(ctx context.Context, productID string, limit int) ([]Product, error)

	// Count returns the total number of products matching the filters
	Count(ctx context.Context, filters ProductFilters) (int, error)
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
