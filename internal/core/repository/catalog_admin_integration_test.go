//go:build integration

package repository

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/duynhlab/product-service/internal/core/domain"
)

// TestCatalogAdmin_Integration proves the privileged catalog writers against the
// real schema (RFC-0023 slice B): the DRAFT default, optimistic concurrency, the
// lifecycle edges, category uniqueness, and — the claim ADR-047 actually makes —
// that the audit row and the write land together or not at all.
func TestCatalogAdmin_Integration(t *testing.T) {
	pool := newTestDB(t)
	repo := NewCatalogAdminRepository(pool)
	ctx := context.Background()

	const actor = "d0e00000-0000-4000-8000-000000000001" // duyne's staff subject

	// A category to hang products on. The migration seeds reference categories,
	// so create a distinctly named one instead of assuming which exist.
	cat, err := repo.CreateCategory(ctx, "Slice B Fixtures", "integration", actor, "req-cat-1")
	if err != nil {
		t.Fatalf("CreateCategory: %v", err)
	}

	t.Run("create lands in DRAFT with its audit row", func(t *testing.T) {
		p, err := repo.CreateProduct(ctx, CreateProductInput{
			Name: "Lifecycle Widget", Price: 12.5, Description: "d",
			Category: cat.Name, ActorSub: actor, RequestID: "req-1",
		})
		if err != nil {
			t.Fatalf("CreateProduct: %v", err)
		}
		if p.Status != string(domain.StatusDraft) || p.Version != 1 {
			t.Fatalf("new product = (%s, v%d), want (DRAFT, v1)", p.Status, p.Version)
		}
		if p.Category != cat.Name {
			t.Fatalf("category not resolved: %+v", p)
		}

		id := atoiT(t, p.ID)
		audit, err := repo.ListAudit(ctx, "product", id, 10)
		if err != nil || len(audit) != 1 {
			t.Fatalf("audit after create = (%d rows, %v), want 1", len(audit), err)
		}
		if audit[0].Action != "CREATE" || audit[0].ActorSub != actor {
			t.Fatalf("audit row = %+v, want CREATE by the actor", audit[0])
		}
		if audit[0].ChangedFields["name"] != "Lifecycle Widget" {
			t.Fatalf("changed_fields did not survive the JSONB round-trip: %+v", audit[0].ChangedFields)
		}
	})

	t.Run("draft products stay out of the public read path", func(t *testing.T) {
		// The public repository is the one the catalog serves from; a DRAFT row
		// must never appear there even though it exists.
		public := NewPostgresProductRepository(pool)
		items, err := public.FindAll(ctx, domain.ProductFilters{Page: 1, Limit: 200})
		if err != nil {
			t.Fatalf("public FindAll: %v", err)
		}
		for _, p := range items {
			if p.Name == "Lifecycle Widget" {
				t.Fatal("a DRAFT product reached the public catalog list")
			}
		}
	})

	t.Run("optimistic concurrency: stale version loses", func(t *testing.T) {
		p, err := repo.CreateProduct(ctx, CreateProductInput{
			Name: "Concurrency Widget", Price: 20, Category: cat.Name,
			ActorSub: actor, RequestID: "req-2",
		})
		if err != nil {
			t.Fatalf("CreateProduct: %v", err)
		}

		updated, err := repo.UpdateProduct(ctx, UpdateProductInput{
			ID: p.ID, Name: "Concurrency Widget v2", Price: 25, Category: cat.Name,
			ExpectedVersion: p.Version, ActorSub: actor, Reason: "price fix", RequestID: "req-3",
		})
		if err != nil {
			t.Fatalf("first update: %v", err)
		}
		if updated.Version != p.Version+1 {
			t.Fatalf("version = %d, want %d", updated.Version, p.Version+1)
		}

		// The same call again carries the now-stale version.
		if _, err := repo.UpdateProduct(ctx, UpdateProductInput{
			ID: p.ID, Name: "Overwrite", Price: 99, Category: cat.Name,
			ExpectedVersion: p.Version, ActorSub: actor, RequestID: "req-4",
		}); !errors.Is(err, domain.ErrVersionConflict) {
			t.Fatalf("stale update = %v, want ErrVersionConflict", err)
		}

		// And the losing edit changed nothing.
		after, err := repo.GetProduct(ctx, p.ID)
		if err != nil || after.Name != "Concurrency Widget v2" || after.Price != 25 {
			t.Fatalf("row after refused update = %+v (%v)", after, err)
		}

		id := atoiT(t, p.ID)
		audit, err := repo.ListAudit(ctx, "product", id, 10)
		if err != nil {
			t.Fatalf("ListAudit: %v", err)
		}
		// CREATE + one accepted UPDATE — the refused one must not be recorded.
		if len(audit) != 2 {
			t.Fatalf("audit = %d rows, want 2 (create + accepted update)", len(audit))
		}
		if audit[0].Action != "UPDATE" || audit[0].Reason != "price fix" {
			t.Fatalf("newest audit row = %+v", audit[0])
		}
		if audit[0].VersionBefore == nil || *audit[0].VersionBefore != p.Version ||
			audit[0].VersionAfter == nil || *audit[0].VersionAfter != p.Version+1 {
			t.Fatalf("audit version pair wrong: %+v", audit[0])
		}
		price, ok := audit[0].ChangedFields["price"].(map[string]any)
		if !ok || price["before"] != 20.0 || price["after"] != 25.0 {
			t.Fatalf("before/after not captured: %+v", audit[0].ChangedFields)
		}
	})

	t.Run("lifecycle edges apply and refuse", func(t *testing.T) {
		p, err := repo.CreateProduct(ctx, CreateProductInput{
			Name: "Transition Widget", Price: 5, Category: cat.Name,
			ActorSub: actor, RequestID: "req-5",
		})
		if err != nil {
			t.Fatalf("CreateProduct: %v", err)
		}

		// DRAFT -> ACTIVE -> ARCHIVED -> ACTIVE, each bumping the version.
		for _, step := range []struct {
			action domain.LifecycleAction
			want   domain.ProductStatus
		}{
			{domain.ActionPublish, domain.StatusActive},
			{domain.ActionArchive, domain.StatusArchived},
			{domain.ActionRestore, domain.StatusActive},
		} {
			got, err := repo.Transition(ctx, TransitionInput{
				ID: p.ID, Action: step.action, ActorSub: actor, RequestID: "req-t",
			})
			if err != nil || got.Status != string(step.want) {
				t.Fatalf("%s → (%v, %v), want %s", step.action, got, err, step.want)
			}
		}

		// Publishing an ACTIVE product is an illegal edge, and the refusal must
		// leave both the row and the audit untouched.
		before, _ := repo.GetProduct(ctx, p.ID)
		if _, err := repo.Transition(ctx, TransitionInput{
			ID: p.ID, Action: domain.ActionPublish, ActorSub: actor,
		}); !errors.Is(err, domain.ErrInvalidTransition) {
			t.Fatalf("publish on ACTIVE = %v, want ErrInvalidTransition", err)
		}
		after, _ := repo.GetProduct(ctx, p.ID)
		if after.Version != before.Version {
			t.Fatalf("a refused transition bumped the version: %d → %d", before.Version, after.Version)
		}

		id := atoiT(t, p.ID)
		audit, err := repo.ListAudit(ctx, "product", id, 10)
		if err != nil || len(audit) != 4 {
			t.Fatalf("audit = (%d rows, %v), want 4 (create + 3 transitions)", len(audit), err)
		}
		if audit[0].Action != "RESTORE" {
			t.Fatalf("newest audit action = %s, want RESTORE", audit[0].Action)
		}
		st, ok := audit[0].ChangedFields["status"].(map[string]any)
		if !ok || st["before"] != "ARCHIVED" || st["after"] != "ACTIVE" {
			t.Fatalf("transition audit lost the edge: %+v", audit[0].ChangedFields)
		}
	})

	t.Run("archived products vanish from public reads but keep pricing", func(t *testing.T) {
		p, err := repo.CreateProduct(ctx, CreateProductInput{
			Name: "Archived Widget", Price: 33.5, Category: cat.Name,
			ActorSub: actor, RequestID: "req-6",
		})
		if err != nil {
			t.Fatalf("CreateProduct: %v", err)
		}
		if _, err := repo.Transition(ctx, TransitionInput{ID: p.ID, Action: domain.ActionPublish, ActorSub: actor}); err != nil {
			t.Fatalf("publish: %v", err)
		}
		public := NewPostgresProductRepository(pool)
		if _, err := public.FindByID(ctx, p.ID); err != nil {
			t.Fatalf("published product must be publicly readable: %v", err)
		}
		if _, err := repo.Transition(ctx, TransitionInput{ID: p.ID, Action: domain.ActionArchive, ActorSub: actor}); err != nil {
			t.Fatalf("archive: %v", err)
		}

		// The product page 404s...
		if _, err := public.FindByID(ctx, p.ID); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("archived FindByID = %v, want ErrNotFound", err)
		}
		// ...while the batch price read still resolves it, so a cart holding this
		// product prices correctly (the deliberate asymmetry in docs/api/product.md).
		batch, err := public.FindByIDs(ctx, []string{p.ID})
		if err != nil || len(batch) != 1 || batch[0].Price != 33.5 {
			t.Fatalf("archived price read = (%v, %v), want the product at 33.5", batch, err)
		}
	})

	t.Run("categories: list, rename, and the unique-name conflict", func(t *testing.T) {
		items, total, err := repo.ListCategories(ctx, 100, 0)
		if err != nil || total < 1 {
			t.Fatalf("ListCategories = (total %d, %v)", total, err)
		}
		found := false
		for _, c := range items {
			if c.ID == cat.ID {
				found = true
			}
		}
		if !found {
			t.Fatalf("the created category is missing from the list: %+v", items)
		}

		renamed, err := repo.UpdateCategory(ctx, cat.ID, "Slice B Fixtures Renamed", "still integration", actor, "req-7")
		if err != nil || renamed.Name != "Slice B Fixtures Renamed" {
			t.Fatalf("UpdateCategory = (%+v, %v)", renamed, err)
		}
		audit, err := repo.ListAudit(ctx, "category", cat.ID, 10)
		if err != nil || len(audit) != 2 {
			t.Fatalf("category audit = (%d, %v), want 2", len(audit), err)
		}

		// A second category cannot take a name that exists.
		if _, err := repo.CreateCategory(ctx, "Slice B Fixtures Renamed", "", actor, "req-8"); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("duplicate category = %v, want ErrConflict", err)
		}
	})

	t.Run("missing targets answer ErrNotFound", func(t *testing.T) {
		if _, err := repo.GetProduct(ctx, "999999"); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("GetProduct(missing) = %v", err)
		}
		if _, err := repo.Transition(ctx, TransitionInput{ID: "999999", Action: domain.ActionPublish, ActorSub: actor}); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("Transition(missing) = %v", err)
		}
		if _, err := repo.UpdateProduct(ctx, UpdateProductInput{ID: "999999", ExpectedVersion: 1, ActorSub: actor}); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("UpdateProduct(missing) = %v", err)
		}
		if _, err := repo.UpdateCategory(ctx, 999999, "x", "", actor, ""); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("UpdateCategory(missing) = %v", err)
		}
	})
}

func atoiT(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatalf("product id %q is not numeric: %v", s, err)
	}
	return n
}
