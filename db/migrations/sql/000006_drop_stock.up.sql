-- RFC-0021 phase 4 contraction: product stops owning stock, in the schema too.
--
-- IRREVERSIBLE for the DATA. The paired down-migration restores the SHAPE of what
-- this drops (column, table, index, constraints) so a code rollback lands on a
-- schema it understands — but the values are gone. The rollback for the data is the
-- backup taken before this runs, and that is not a formality: the numbers here were
-- frozen at the W7 write cutover, so they are the only record of what product
-- believed about stock at that moment.
--
-- Safe to run because nothing reads either object any more, verified in code rather
-- than assumed:
--   * the saga's product stock branch was deleted in order 1.13.0 (Current)
--   * ReserveStock/ReleaseStock left the contract in pkg v0.33.0 / product 1.7.0 —
--     they were the only writers of stock_reservations, so it has had no writer
--     since the cutover and no reader since 1.7.0
--   * GetProducts.available_qty was the last reader of stock_quantity and left in
--     pkg v0.34.0 / product 1.8.0+
--   * checkout reads availability from inventory.v1 only (checkout 0.5.0)
--   * inventory-service is the authority and seeds/owns its own balances
--
-- Order matters: stock_reservations has an FK to products, so the table goes first.

-- 1. Revoke the temporary backfill access BEFORE dropping the objects it covered.
--    Mirrors the guard in 000005: the cluster provisions a dedicated `inventory`
--    login role; local-stack runs everything as the shared superuser and has none.
--
--    Least privilege, not tidiness: the phase-2 backfill is finished, so a standing
--    cross-service SELECT on another service's table is exactly the grant that gets
--    forgotten and then relied on. Revoking CONNECT is the outer boundary; the
--    homelab pg_hba entry `host product inventory` is revoked in the same wave, so
--    the role cannot even reach the database.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'inventory') THEN
    REVOKE SELECT ON public.products FROM inventory;
    REVOKE USAGE ON SCHEMA public FROM inventory;
    REVOKE CONNECT ON DATABASE product FROM inventory;
  END IF;
END
$$;

-- 2. The saga's reservation ledger. Its index and FK go with it.
DROP TABLE IF EXISTS stock_reservations;

-- 3. The frozen column, and the CHECK that rode on it.
ALTER TABLE products DROP COLUMN IF EXISTS stock_quantity;
