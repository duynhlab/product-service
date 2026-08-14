-- Reverses 000007. Restores the pre-lifecycle SHAPE; the audit ROWS are gone.
--
-- Read that twice before applying it: this drop deletes the record of who changed
-- the catalog and when, which is the one thing an audit trail exists to keep. If the
-- rollback is a code rollback rather than a decision to abandon the feature,
-- `pg_dump -Fc -t admin_action_audit` first — the table is small and the dump is the
-- only copy.
--
-- Dropping `status` is also a behaviour change, not only a shape change: any product
-- an operator had left in DRAFT becomes publicly visible again, because the public
-- reads lose the filter that hid it.
SET lock_timeout = '3s';
SET statement_timeout = '30s';

DROP TABLE IF EXISTS admin_action_audit;

DROP INDEX IF EXISTS idx_products_status;

ALTER TABLE products DROP CONSTRAINT IF EXISTS products_status_check;
ALTER TABLE products DROP COLUMN IF EXISTS version;
ALTER TABLE products DROP COLUMN IF EXISTS status;
