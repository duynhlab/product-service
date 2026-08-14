package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/duynhlab/product-service/internal/core/domain"
)

// The privileged catalog writers (RFC-0023 slice B / ADR-047).
//
// Every command here runs inside ONE transaction that carries both the change and
// its audit row, using pgx's BeginFunc: it rolls back on any returned error and
// commits otherwise, so "the write succeeded but the audit did not" is not a state
// this code can reach.
// Source: https://github.com/jackc/pgx/blob/master/_autodocs/pgx-tx-batch.md
//
// These live apart from PostgresProductRepository on purpose. That repository is the
// public catalog's read path — hot, cached, status-filtered — and mixing operator
// writes into it would put an audit-shaped dependency on every read.

// adminProductColumns is the operator projection: the public columns plus the two
// the lifecycle added. Category is resolved by join like the public reads do.
const adminProductColumns = `p.id, p.name, p.price, COALESCE(p.description, ''),
	COALESCE(c.name, ''), p.status, p.version`

// CatalogAdminRepository writes products and categories on behalf of an operator.
type CatalogAdminRepository struct {
	pool *pgxpool.Pool
}

// NewCatalogAdminRepository wraps a pool.
func NewCatalogAdminRepository(pool *pgxpool.Pool) *CatalogAdminRepository {
	return &CatalogAdminRepository{pool: pool}
}

// AdminProduct is one row of the operator's catalog list: the product plus the two
// fields the public payload hides.
type AdminProduct struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Price       float64 `json:"price"`
	Description string  `json:"description"`
	Category    string  `json:"category"`
	Status      string  `json:"status"`
	Version     int64   `json:"version"`
}

func scanAdminProduct(row pgx.Row) (*AdminProduct, error) {
	var p AdminProduct
	if err := row.Scan(&p.ID, &p.Name, &p.Price, &p.Description, &p.Category, &p.Status, &p.Version); err != nil {
		return nil, err
	}
	return &p, nil
}

// ListProducts returns one page of the catalog in EVERY state, newest first, plus
// the unpaged total. `status` narrows when set — the operator's DRAFT queue and
// ARCHIVED history are exactly the rows the public reads cannot show.
func (r *CatalogAdminRepository) ListProducts(ctx context.Context, status string, limit, offset int) ([]AdminProduct, int, error) {
	where := ""
	args := []any{}
	if status != "" {
		args = append(args, status)
		where = ` WHERE p.status = $1`
	}

	var total int
	if err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM products p`+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count products: %w", err)
	}

	q := fmt.Sprintf(`SELECT `+adminProductColumns+`
		  FROM products p LEFT JOIN categories c ON c.id = p.category_id%s
		 ORDER BY p.created_at DESC, p.id DESC
		 LIMIT $%d OFFSET $%d`, where, len(args)+1, len(args)+2)
	rows, err := r.pool.Query(ctx, q, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("list products: %w", err)
	}
	defer rows.Close()

	items := make([]AdminProduct, 0)
	for rows.Next() {
		p, err := scanAdminProduct(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("scan product: %w", err)
		}
		items = append(items, *p)
	}
	return items, total, rows.Err()
}

// GetProduct reads one product in any state — the operator's detail view.
func (r *CatalogAdminRepository) GetProduct(ctx context.Context, id string) (*AdminProduct, error) {
	p, err := scanAdminProduct(r.pool.QueryRow(ctx,
		`SELECT `+adminProductColumns+`
		   FROM products p LEFT JOIN categories c ON c.id = p.category_id
		  WHERE p.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get product %s: %w", id, err)
	}
	return p, nil
}

// CreateProductInput is a new catalog row. It lands in DRAFT: an operator publishes
// deliberately, so a half-filled product is never briefly public.
type CreateProductInput struct {
	Name        string
	Price       float64
	Description string
	Category    string
	ActorSub    string
	RequestID   string
}

// CreateProduct inserts a DRAFT product and its CREATE audit row in one
// transaction.
func (r *CatalogAdminRepository) CreateProduct(ctx context.Context, in CreateProductInput) (*AdminProduct, error) {
	var out *AdminProduct
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		var id int
		if err := tx.QueryRow(ctx, `
			INSERT INTO products (name, description, price, category_id, status, version)
			VALUES ($1, $2, $3, (SELECT id FROM categories WHERE name = $4), 'DRAFT', 1)
			RETURNING id`,
			in.Name, in.Description, in.Price, in.Category).Scan(&id); err != nil {
			// UNIQUE(products.name) is the create path's safety net: a retried
			// create reads as a conflict the operator can act on instead of a
			// second row (and instead of an opaque 500).
			if isUniqueViolation(err) {
				return fmt.Errorf("a product named %q exists: %w", in.Name, domain.ErrConflict)
			}
			return fmt.Errorf("insert product: %w", err)
		}

		after := int64(1)
		if err := insertAudit(ctx, tx, domain.AuditEntry{
			TargetType: "product", TargetID: id, Action: "CREATE",
			ActorSub: in.ActorSub, RequestID: in.RequestID,
			ChangedFields: map[string]any{
				"name": in.Name, "price": in.Price, "category": in.Category,
			},
			VersionAfter: &after,
		}); err != nil {
			return err
		}

		p, err := scanAdminProduct(tx.QueryRow(ctx,
			`SELECT `+adminProductColumns+`
			   FROM products p LEFT JOIN categories c ON c.id = p.category_id
			  WHERE p.id = $1`, strconv.Itoa(id)))
		if err != nil {
			return fmt.Errorf("read back product: %w", err)
		}
		out = p
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// UpdateProductInput edits a product under optimistic concurrency: ExpectedVersion
// is the version the operator read.
type UpdateProductInput struct {
	ID              string
	Name            string
	Price           float64
	Description     string
	Category        string
	ExpectedVersion int64
	ActorSub        string
	Reason          string
	RequestID       string
}

// UpdateProduct applies an edit if and only if the row still carries the expected
// version, bumping it and writing the before/after audit in the same transaction.
//
// The version match is part of the UPDATE's WHERE clause rather than a read-then-write
// check: a separate SELECT would leave a window where another operator commits
// between the two statements, which is the exact race the token exists to close.
func (r *CatalogAdminRepository) UpdateProduct(ctx context.Context, in UpdateProductInput) (*AdminProduct, error) {
	var out *AdminProduct
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		before, err := scanAdminProduct(tx.QueryRow(ctx,
			`SELECT `+adminProductColumns+`
			   FROM products p LEFT JOIN categories c ON c.id = p.category_id
			  WHERE p.id = $1 FOR UPDATE OF p`, in.ID))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("read product for update: %w", err)
		}
		if before.Version != in.ExpectedVersion {
			return fmt.Errorf("have version %d, sent %d: %w",
				before.Version, in.ExpectedVersion, domain.ErrVersionConflict)
		}

		tag, err := tx.Exec(ctx, `
			UPDATE products
			   SET name = $1, description = $2, price = $3,
			       category_id = (SELECT id FROM categories WHERE name = $4),
			       version = version + 1, updated_at = now()
			 WHERE id = $5 AND version = $6`,
			in.Name, in.Description, in.Price, in.Category, in.ID, in.ExpectedVersion)
		if err != nil {
			// Renaming onto an existing name hits the same unique index.
			if isUniqueViolation(err) {
				return fmt.Errorf("a product named %q exists: %w", in.Name, domain.ErrConflict)
			}
			return fmt.Errorf("update product: %w", err)
		}
		if tag.RowsAffected() == 0 {
			// The row was locked above, so this can only mean the version moved —
			// keep the same answer the pre-check gives rather than a bare 500.
			return domain.ErrVersionConflict
		}

		changed := map[string]any{}
		if before.Name != in.Name {
			changed["name"] = map[string]any{"before": before.Name, "after": in.Name}
		}
		if before.Price != in.Price {
			changed["price"] = map[string]any{"before": before.Price, "after": in.Price}
		}
		if before.Description != in.Description {
			changed["description"] = map[string]any{"before": before.Description, "after": in.Description}
		}
		if before.Category != in.Category {
			changed["category"] = map[string]any{"before": before.Category, "after": in.Category}
		}

		id, _ := strconv.Atoi(in.ID)
		vAfter := before.Version + 1
		if err := insertAudit(ctx, tx, domain.AuditEntry{
			TargetType: "product", TargetID: id, Action: "UPDATE",
			ActorSub: in.ActorSub, Reason: in.Reason, RequestID: in.RequestID,
			ChangedFields: changed,
			VersionBefore: &before.Version, VersionAfter: &vAfter,
		}); err != nil {
			return err
		}

		p, err := scanAdminProduct(tx.QueryRow(ctx,
			`SELECT `+adminProductColumns+`
			   FROM products p LEFT JOIN categories c ON c.id = p.category_id
			  WHERE p.id = $1`, in.ID))
		if err != nil {
			return fmt.Errorf("read back product: %w", err)
		}
		out = p
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// TransitionInput moves a product along the lifecycle.
type TransitionInput struct {
	ID        string
	Action    domain.LifecycleAction
	ActorSub  string
	Reason    string
	RequestID string
}

// Transition applies one lifecycle command. The legal-edge decision lives in the
// domain (domain.NextStatus); this function's job is to apply it atomically against
// the state the row actually has, which is why the row is locked first.
func (r *CatalogAdminRepository) Transition(ctx context.Context, in TransitionInput) (*AdminProduct, error) {
	var out *AdminProduct
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		var current domain.ProductStatus
		var version int64
		err := tx.QueryRow(ctx,
			`SELECT status, version FROM products WHERE id = $1 FOR UPDATE`, in.ID).
			Scan(&current, &version)
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("read product for transition: %w", err)
		}

		next, err := domain.NextStatus(current, in.Action)
		if err != nil {
			return err
		}

		if _, err := tx.Exec(ctx, `
			UPDATE products SET status = $1, version = version + 1, updated_at = now()
			 WHERE id = $2`, string(next), in.ID); err != nil {
			return fmt.Errorf("apply transition: %w", err)
		}

		id, _ := strconv.Atoi(in.ID)
		vAfter := version + 1
		if err := insertAudit(ctx, tx, domain.AuditEntry{
			TargetType: "product", TargetID: id, Action: string(in.Action),
			ActorSub: in.ActorSub, Reason: in.Reason, RequestID: in.RequestID,
			ChangedFields: map[string]any{
				"status": map[string]any{"before": string(current), "after": string(next)},
			},
			VersionBefore: &version, VersionAfter: &vAfter,
		}); err != nil {
			return err
		}

		p, err := scanAdminProduct(tx.QueryRow(ctx,
			`SELECT `+adminProductColumns+`
			   FROM products p LEFT JOIN categories c ON c.id = p.category_id
			  WHERE p.id = $1`, in.ID))
		if err != nil {
			return fmt.Errorf("read back product: %w", err)
		}
		out = p
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ListCategories returns one page of the flat taxonomy plus the unpaged total.
func (r *CatalogAdminRepository) ListCategories(ctx context.Context, limit, offset int) ([]domain.Category, int, error) {
	var total int
	if err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM categories`).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count categories: %w", err)
	}

	rows, err := r.pool.Query(ctx, `
		SELECT id, name, COALESCE(description, ''), created_at, updated_at
		  FROM categories ORDER BY name
		 LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list categories: %w", err)
	}
	defer rows.Close()

	out := make([]domain.Category, 0)
	for rows.Next() {
		var c domain.Category
		if err := rows.Scan(&c.ID, &c.Name, &c.Description, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, 0, fmt.Errorf("scan category: %w", err)
		}
		out = append(out, c)
	}
	return out, total, rows.Err()
}

// CreateCategory inserts a category with its audit row. Names are unique in the
// schema, so a duplicate surfaces as ErrConflict rather than a driver error.
func (r *CatalogAdminRepository) CreateCategory(ctx context.Context, name, description, actorSub, requestID string) (*domain.Category, error) {
	var out *domain.Category
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		var c domain.Category
		err := tx.QueryRow(ctx, `
			INSERT INTO categories (name, description) VALUES ($1, NULLIF($2,''))
			RETURNING id, name, COALESCE(description,''), created_at, updated_at`,
			name, description).
			Scan(&c.ID, &c.Name, &c.Description, &c.CreatedAt, &c.UpdatedAt)
		if isUniqueViolation(err) {
			return fmt.Errorf("category %q exists: %w", name, domain.ErrConflict)
		}
		if err != nil {
			return fmt.Errorf("insert category: %w", err)
		}

		if err := insertAudit(ctx, tx, domain.AuditEntry{
			TargetType: "category", TargetID: c.ID, Action: "CREATE",
			ActorSub: actorSub, RequestID: requestID,
			ChangedFields: map[string]any{"name": name, "description": description},
		}); err != nil {
			return err
		}
		out = &c
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// UpdateCategory renames or re-describes a category, with its audit row.
//
// No delete exists, deliberately: products.category_id is ON DELETE SET NULL, so a
// delete would silently uncategorize products. That is a decision with its own
// review (RFC-0023 keeps it out of the MVP).
func (r *CatalogAdminRepository) UpdateCategory(ctx context.Context, id int, name, description, actorSub, requestID string) (*domain.Category, error) {
	var out *domain.Category
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		var before domain.Category
		err := tx.QueryRow(ctx, `
			SELECT id, name, COALESCE(description,'') FROM categories WHERE id = $1 FOR UPDATE`, id).
			Scan(&before.ID, &before.Name, &before.Description)
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("read category for update: %w", err)
		}

		var c domain.Category
		err = tx.QueryRow(ctx, `
			UPDATE categories SET name = $1, description = NULLIF($2,''), updated_at = now()
			 WHERE id = $3
			RETURNING id, name, COALESCE(description,''), created_at, updated_at`,
			name, description, id).
			Scan(&c.ID, &c.Name, &c.Description, &c.CreatedAt, &c.UpdatedAt)
		if isUniqueViolation(err) {
			return fmt.Errorf("category %q exists: %w", name, domain.ErrConflict)
		}
		if err != nil {
			return fmt.Errorf("update category: %w", err)
		}

		changed := map[string]any{}
		if before.Name != name {
			changed["name"] = map[string]any{"before": before.Name, "after": name}
		}
		if before.Description != description {
			changed["description"] = map[string]any{"before": before.Description, "after": description}
		}
		if err := insertAudit(ctx, tx, domain.AuditEntry{
			TargetType: "category", TargetID: id, Action: "UPDATE",
			ActorSub: actorSub, RequestID: requestID, ChangedFields: changed,
		}); err != nil {
			return err
		}
		out = &c
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// AuditRow is one entry of a target's change history.
type AuditRow struct {
	ID            int64          `json:"id"`
	Action        string         `json:"action"`
	ActorSub      string         `json:"actor_sub"`
	Reason        string         `json:"reason"`
	ChangedFields map[string]any `json:"changed_fields"`
	VersionBefore *int64         `json:"version_before"`
	VersionAfter  *int64         `json:"version_after"`
	CreatedAt     time.Time      `json:"created_at"`
}

// ListAudit returns one target's history, newest first — the read that makes the
// audit trail useful to a human instead of only to a subpoena.
func (r *CatalogAdminRepository) ListAudit(ctx context.Context, targetType string, targetID, limit int) ([]AuditRow, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, action, actor_sub, COALESCE(reason,''), changed_fields,
		       version_before, version_after, created_at
		  FROM admin_action_audit
		 WHERE target_type = $1 AND target_id = $2
		 ORDER BY created_at DESC, id DESC
		 LIMIT $3`, targetType, targetID, limit)
	if err != nil {
		return nil, fmt.Errorf("list audit for %s %d: %w", targetType, targetID, err)
	}
	defer rows.Close()

	out := make([]AuditRow, 0)
	for rows.Next() {
		var a AuditRow
		var raw []byte
		if err := rows.Scan(&a.ID, &a.Action, &a.ActorSub, &a.Reason, &raw,
			&a.VersionBefore, &a.VersionAfter, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan audit row: %w", err)
		}
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &a.ChangedFields)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// pgUniqueViolation is Postgres' SQLSTATE for a unique constraint violation. The
// categories table has UNIQUE(name), so a duplicate must read as a conflict the
// operator can act on, not as an opaque driver error.
const pgUniqueViolation = "23505"

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation
}

// insertAudit writes one audit row on the caller's transaction. Taking pgx.Tx
// rather than the pool is the enforcement of "same transaction as the write": there
// is no way to call this outside one.
func insertAudit(ctx context.Context, tx pgx.Tx, e domain.AuditEntry) error {
	// A *string, NOT []byte. pgx encodes []byte as bytea, and the service's pool
	// runs the simple protocol, so the JSONB column received a hex bytea literal
	// and Postgres answered 22P02 invalid input syntax for type json. A plain
	// Postgres pool in a test uses the extended protocol and inferred the type,
	// which is exactly why this only failed against the real service. nil stays
	// NULL for a command with no field diff.
	var changed *string
	if len(e.ChangedFields) > 0 {
		b, err := json.Marshal(e.ChangedFields)
		if err != nil {
			return fmt.Errorf("marshal changed_fields: %w", err)
		}
		s := string(b)
		changed = &s
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO admin_action_audit
			(target_type, target_id, action, actor_sub, reason, changed_fields,
			 version_before, version_after, request_id)
		VALUES ($1, $2, $3, $4, NULLIF($5,''), $6, $7, $8, NULLIF($9,''))`,
		e.TargetType, e.TargetID, e.Action, e.ActorSub, e.Reason, changed,
		e.VersionBefore, e.VersionAfter, e.RequestID); err != nil {
		return fmt.Errorf("insert audit row: %w", err)
	}
	return nil
}
