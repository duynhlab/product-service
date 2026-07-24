-- RFC-0021 P2-3: grant the inventory-service role read-only access to product
-- stock so the phase-2 backfill can copy products.stock_quantity into
-- inventory_balances. The schema owner (this migration runs as the product
-- role) grants the consumer.
--
-- Cluster-only by construction: the cluster provisions a dedicated `inventory`
-- login role (CNPG DatabaseRole, homelab), whereas local-stack runs every
-- service on the shared `postgres` superuser and has no such role. The grant is
-- therefore guarded on the role existing — a no-op in local-stack (where the
-- superuser already reads everything), an actual grant in the cluster.
--
-- Temporary: revoked at Phase 7 contraction via a follow-up migration once the
-- backfill/read cutover is complete and Product no longer owns stock.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'inventory') THEN
    GRANT CONNECT ON DATABASE product TO inventory;
    GRANT USAGE ON SCHEMA public TO inventory;
    GRANT SELECT ON public.products TO inventory;
  END IF;
END
$$;
