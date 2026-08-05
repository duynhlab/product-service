-- Reverse of 000006 — SHAPE ONLY.
--
-- This is the first down-migration in this service, and it exists because 000006 is
-- the first one that cannot simply be re-run. Read what it does and does not do:
--
--   RESTORES: products.stock_quantity (with its CHECK), the stock_reservations
--             table, its index, its FK.
--   DOES NOT RESTORE: a single value. Every product comes back at the column
--             default of 0 and the ledger comes back empty.
--
-- WHAT THAT MEANS IN PRODUCTION, stated plainly because "shape only" undersells it:
-- the code this exists for is pre-1.8.0, which reserves stock with a guarded
-- decrement under CHECK (stock_quantity >= 0). With every row at 0, every reservation
-- fails and checkout goes to 100% failure. That is the INTENDED fail-closed outcome,
-- not a bug — but it also means applying this alone creates two authorities that
-- disagree completely, because inventory_balances still holds the live numbers.
-- If the values matter: restore the pre-migration backup and do NOT run this at all.
--
-- HOW TO ACTUALLY APPLY IT. `migrate ... down 1` is NOT available:
--   * pkg/migratex exposes only Run(), which calls Up() — the service binary has no
--     down path at all;
--   * the standalone golang-migrate CLI reads a filesystem or URL and cannot read an
--     embed.FS compiled into another binary;
--   * a pre-1.8.0 image does not contain this file in the first place.
-- So the real procedure is manual and deliberately so — check the file out at the tag
-- that introduced it and apply it by hand, reviewing each statement:
--
--   git show <tag>:db/migrations/sql/000006_drop_stock.down.sql | psql -d product
--   psql -d product -c "UPDATE schema_migrations SET version = 5, dirty = false"
--
-- The schema_migrations line is not optional: without it golang-migrate still
-- believes 000006 is applied and the next Up() is a no-op onto a rolled-back schema.
-- Recorded in RFC-0021 cutover-rollback.md as well, so it is not only in this comment.
--
-- The IF NOT EXISTS guards below make re-application safe, but they also SILENTLY
-- ACCEPT a pre-existing object of a different shape — e.g. a stock_reservations
-- hand-made during an earlier improvised recovery. Check the shape matches before
-- trusting a clean exit.
--
-- The revoked backfill grant is deliberately NOT re-granted here. Re-granting
-- cross-service SELECT is a privilege decision, not a schema one, and it should
-- require someone to ask for it — re-run 000005 explicitly if the backfill has to
-- happen again.

ALTER TABLE products ADD COLUMN IF NOT EXISTS stock_quantity INTEGER DEFAULT 0 CHECK (stock_quantity >= 0);

CREATE TABLE IF NOT EXISTS stock_reservations (
    reservation_id VARCHAR(255) NOT NULL,
    product_id     INTEGER NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    quantity       INTEGER NOT NULL CHECK (quantity > 0),
    status         VARCHAR(20) NOT NULL DEFAULT 'reserved' CHECK (status IN ('reserved', 'released')),
    created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (reservation_id, product_id)
);

CREATE INDEX IF NOT EXISTS idx_stock_reservations_lookup
    ON stock_reservations (reservation_id, status);
