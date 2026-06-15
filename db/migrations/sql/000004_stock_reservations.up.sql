-- Stock reservations ledger for the order-fulfillment saga (Temporal).
--
-- Makes ProductService.ReserveStock/ReleaseStock idempotent across activity
-- retries: the reservation is recorded in the SAME transaction as the stock
-- decrement, keyed by (reservation_id, product_id). A retry of ReserveStock with
-- the same reservation_id is a no-op; ReleaseStock restores stock only for an
-- 'reserved' reservation and flips it to 'released' (so a retried compensation
-- can't double-restore).
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
