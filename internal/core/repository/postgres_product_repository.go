package repository

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/duynhlab/product-service/internal/core/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresProductRepository implements ProductRepository using PostgreSQL with pgx
type PostgresProductRepository struct {
	pool *pgxpool.Pool
}

// Ensure interface compliance
var _ domain.ProductRepository = (*PostgresProductRepository)(nil)

// NewPostgresProductRepository creates a new PostgreSQL product repository
func NewPostgresProductRepository(pool *pgxpool.Pool) *PostgresProductRepository {
	return &PostgresProductRepository{pool: pool}
}

// FindByID retrieves a product by ID
func (r *PostgresProductRepository) FindByID(ctx context.Context, id string) (*domain.Product, error) {
	query := `
		SELECT p.id, p.name, p.description, p.price, COALESCE(c.name, 'Uncategorized') as category, p.stock_quantity
		FROM products p
		LEFT JOIN categories c ON p.category_id = c.id
		WHERE p.id = $1
	`

	var product domain.Product
	var idInt int
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&idInt, &product.Name, &product.Description, &product.Price, &product.Category, &product.StockQuantity,
	)

	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	product.ID = strconv.Itoa(idInt)
	return &product, nil
}


// FindByIDs retrieves a batch of products by id (checkout re-validation,
// RFC-0015). Non-numeric and unknown ids are omitted from the result — the
// caller treats a missing id as "product gone". Reads hit the DB directly
// (never the cache): this is the price/stock authority at checkout time.
func (r *PostgresProductRepository) FindByIDs(ctx context.Context, ids []string) ([]domain.Product, error) {
	intIDs := make([]int, 0, len(ids))
	for _, id := range ids {
		n, err := strconv.Atoi(id)
		if err != nil {
			continue // non-numeric id cannot exist in the catalog
		}
		intIDs = append(intIDs, n)
	}
	if len(intIDs) == 0 {
		return nil, nil
	}

	query := `
		SELECT p.id, p.name, p.description, p.price, COALESCE(c.name, 'Uncategorized') as category, p.stock_quantity
		FROM products p
		LEFT JOIN categories c ON p.category_id = c.id
		WHERE p.id = ANY($1)
	`
	rows, err := r.pool.Query(ctx, query, intIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	products := make([]domain.Product, 0, len(intIDs))
	for rows.Next() {
		var p domain.Product
		var idInt int
		if err := rows.Scan(&idInt, &p.Name, &p.Description, &p.Price, &p.Category, &p.StockQuantity); err != nil {
			return nil, err
		}
		p.ID = strconv.Itoa(idInt)
		products = append(products, p)
	}
	return products, rows.Err()
}

// FindAll retrieves all products with optional filtering
func (r *PostgresProductRepository) FindAll(ctx context.Context, filters domain.ProductFilters) ([]domain.Product, error) {
	query := `
		SELECT p.id, p.name, p.description, p.price, COALESCE(c.name, 'Uncategorized') as category
		FROM products p
		LEFT JOIN categories c ON p.category_id = c.id
		WHERE 1=1
	`

	args := []interface{}{}
	argPos := 1

	if filters.Category != "" {
		query += fmt.Sprintf(" AND c.name = $%d", argPos)
		args = append(args, filters.Category)
		argPos++
	}

	if filters.Search != "" {
		query += fmt.Sprintf(" AND p.name ILIKE $%d", argPos)
		args = append(args, "%"+filters.Search+"%")
		argPos++
	}

	sortBy := filters.SortBy
	allowedSortFields := map[string]string{
		"id": "p.id", "name": "p.name", "price": "p.price", "created_at": "p.created_at",
	}

	sortColumn := allowedSortFields["created_at"]
	if sortBy != "" {
		if col, ok := allowedSortFields[sortBy]; ok {
			sortColumn = col
		}
	}

	order := strings.ToUpper(strings.TrimSpace(filters.Order))
	if order != "ASC" && order != "DESC" {
		order = "DESC"
	}
	query += fmt.Sprintf(" ORDER BY %s %s", sortColumn, order)

	limit := filters.Limit
	if limit == 0 {
		limit = 20
	}
	offset := (filters.Page - 1) * limit
	if offset < 0 {
		offset = 0
	}
	query += fmt.Sprintf(" LIMIT $%d OFFSET $%d", argPos, argPos+1)
	args = append(args, limit, offset)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	products := []domain.Product{}
	for rows.Next() {
		var product domain.Product
		var idInt int
		err := rows.Scan(&idInt, &product.Name, &product.Description, &product.Price, &product.Category)
		if err != nil {
			return nil, err
		}
		product.ID = strconv.Itoa(idInt)
		products = append(products, product)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return products, nil
}

// Count returns the total number of products matching the filters
func (r *PostgresProductRepository) Count(ctx context.Context, filters domain.ProductFilters) (int, error) {
	query := `
		SELECT COUNT(*)
		FROM products p
		LEFT JOIN categories c ON p.category_id = c.id
		WHERE 1=1
	`

	args := []interface{}{}
	argPos := 1

	if filters.Category != "" {
		query += fmt.Sprintf(" AND c.name = $%d", argPos)
		args = append(args, filters.Category)
		argPos++
	}

	if filters.Search != "" {
		query += fmt.Sprintf(" AND p.name ILIKE $%d", argPos)
		args = append(args, "%"+filters.Search+"%")
	}

	var count int
	err := r.pool.QueryRow(ctx, query, args...).Scan(&count)
	if err != nil {
		return 0, err
	}

	return count, nil
}

// FindRelatedProducts finds products in the same category
func (r *PostgresProductRepository) FindRelatedProducts(ctx context.Context, productID string, limit int) ([]domain.Product, error) {
	query := `
		SELECT p2.id, p2.name, p2.price
		FROM products p1
		JOIN products p2 ON p1.category_id = p2.category_id
		WHERE p1.id = $1 AND p2.id != $1
		LIMIT $2
	`

	rows, err := r.pool.Query(ctx, query, productID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	products := []domain.Product{}
	for rows.Next() {
		var product domain.Product
		var idInt int
		err := rows.Scan(&idInt, &product.Name, &product.Price)
		if err != nil {
			return nil, err
		}
		product.ID = strconv.Itoa(idInt)
		products = append(products, product)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return products, nil
}

// Create creates a new product
func (r *PostgresProductRepository) Create(ctx context.Context, product *domain.Product) error {
	query := `
		INSERT INTO products (name, description, price, category_id)
		VALUES ($1, $2, $3, (SELECT id FROM categories WHERE name = $4))
		RETURNING id
	`

	var id int
	err := r.pool.QueryRow(ctx, query, product.Name, product.Description, product.Price, product.Category).Scan(&id)
	if err != nil {
		return err
	}

	product.ID = strconv.Itoa(id)
	return nil
}

// Update updates an existing product
func (r *PostgresProductRepository) Update(ctx context.Context, product *domain.Product) error {
	query := `
		UPDATE products
		SET name = $1, description = $2, price = $3,
		    category_id = (SELECT id FROM categories WHERE name = $4)
		WHERE id = $5
	`

	result, err := r.pool.Exec(ctx, query, product.Name, product.Description, product.Price, product.Category, product.ID)
	if err != nil {
		return err
	}

	if result.RowsAffected() == 0 {
		return domain.ErrNotFound
	}

	return nil
}

// Delete deletes a product
func (r *PostgresProductRepository) Delete(ctx context.Context, id string) error {
	query := `DELETE FROM products WHERE id = $1`

	result, err := r.pool.Exec(ctx, query, id)
	if err != nil {
		return err
	}

	if result.RowsAffected() == 0 {
		return domain.ErrNotFound
	}

	return nil
}

// ReserveStock atomically decrements stock for every item and records the
// reservation in one transaction (order-fulfillment saga, step 1). It is
// all-or-nothing: a single insufficient item rolls back the whole tx and returns
// domain.ErrInsufficientStock. Idempotent by reservationID — if the reservation
// already exists (a retried call) it is a no-op.
func (r *PostgresProductRepository) ReserveStock(ctx context.Context, reservationID string, items []domain.ReservationItem) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after Commit

	// Idempotency fast-path: a prior (possibly retried) call already reserved.
	var existing int
	if err := tx.QueryRow(ctx,
		`SELECT COUNT(*) FROM stock_reservations WHERE reservation_id = $1`,
		reservationID).Scan(&existing); err != nil {
		return err
	}
	if existing > 0 {
		return tx.Commit(ctx)
	}

	for _, item := range items {
		// Guarded decrement: succeeds only if enough stock remains. RowsAffected
		// is 0 when the product is missing or understocked.
		ct, err := tx.Exec(ctx,
			`UPDATE products SET stock_quantity = stock_quantity - $1, updated_at = CURRENT_TIMESTAMP
			 WHERE id = $2 AND stock_quantity >= $1`,
			item.Quantity, item.ProductID)
		if err != nil {
			return err
		}
		if ct.RowsAffected() == 0 {
			return domain.ErrInsufficientStock
		}
		// (reservation_id, product_id) PK also guards against a racing duplicate.
		if _, err := tx.Exec(ctx,
			`INSERT INTO stock_reservations (reservation_id, product_id, quantity, status)
			 VALUES ($1, $2, $3, 'reserved')`,
			reservationID, item.ProductID, item.Quantity); err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

// ReleaseStock restores stock for an active reservation and marks it released
// (saga compensation for ReserveStock). The ledger is the source of truth, so it
// is idempotent: a no-op when the reservation is unknown or already released.
func (r *PostgresProductRepository) ReleaseStock(ctx context.Context, reservationID string) ([]string, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx,
		`SELECT product_id, quantity FROM stock_reservations
		 WHERE reservation_id = $1 AND status = 'reserved' FOR UPDATE`,
		reservationID)
	if err != nil {
		return nil, err
	}
	type reserved struct {
		productID int
		quantity  int
	}
	var active []reserved
	for rows.Next() {
		var rr reserved
		if err := rows.Scan(&rr.productID, &rr.quantity); err != nil {
			rows.Close()
			return nil, err
		}
		active = append(active, rr)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(active) == 0 {
		return nil, tx.Commit(ctx) // nothing to release — idempotent no-op
	}

	released := make([]string, 0, len(active))
	for _, a := range active {
		if _, err := tx.Exec(ctx,
			`UPDATE products SET stock_quantity = stock_quantity + $1, updated_at = CURRENT_TIMESTAMP
			 WHERE id = $2`,
			a.quantity, a.productID); err != nil {
			return nil, err
		}
		released = append(released, strconv.Itoa(a.productID))
	}
	if _, err := tx.Exec(ctx,
		`UPDATE stock_reservations SET status = 'released', updated_at = CURRENT_TIMESTAMP
		 WHERE reservation_id = $1 AND status = 'reserved'`,
		reservationID); err != nil {
		return nil, err
	}

	return released, tx.Commit(ctx)
}
