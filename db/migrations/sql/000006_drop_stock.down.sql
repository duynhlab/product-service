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
-- So this is for rolling CODE back onto a schema it understands — a build older than
-- product 1.8.0 SELECTs stock_quantity and fails without the column. It is NOT a
-- data rollback. For that, restore the pre-migration backup; if the values matter,
-- restore first and do not run this at all.
--
-- Apply it as EXACTLY ONE STEP (`migrate ... down 1`). This is the only
-- down-migration in the service, so a full `down` walks into 000005 and stops with a
-- missing-file error; the service's own `migrate` subcommand only ever calls Up() and
-- will never touch this file.
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
