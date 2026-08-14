-- RFC-0023 slice B: the catalog gets a lifecycle, and privileged writes get an audit.
--
-- Two shapes arrive together because they are one decision: an operator may now
-- create and retire catalog rows through a protected API, so the platform needs a
-- state to retire INTO and a record of who moved it.
--
-- BACKFILL-FREE BY CONSTRUCTION. `status` lands with DEFAULT 'ACTIVE', so every row
-- that exists today — seeds included — stays exactly as publicly visible as it was
-- the moment before this ran. Only rows created through the new protected route
-- start at DRAFT. There is no data migration to review and no window where the
-- catalog reads empty.
--
-- Postgres 11+ stores a non-volatile column default in the catalog instead of
-- rewriting the table, so both ADD COLUMNs are metadata-only regardless of how many
-- products exist. The lock guard below is still here for the same reason it is in
-- 000006: ADD COLUMN takes ACCESS EXCLUSIVE on products, and Postgres queues every
-- later lock request behind a blocked exclusive waiter, so one open read transaction
-- would turn a metadata change into a catalog-wide stall. Failing fast and letting
-- the init container retry is strictly better.
SET lock_timeout = '3s';
SET statement_timeout = '30s';

-- 1. Lifecycle state. Three states, not a boolean: "not published yet" and "no
--    longer sold" are different answers to an operator and to the public reads.
ALTER TABLE products
  ADD COLUMN IF NOT EXISTS status VARCHAR(16) NOT NULL DEFAULT 'ACTIVE';

-- Named so a violation says which rule it broke. Added separately from the column
-- so a re-run cannot fail on a duplicate constraint name.
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'products_status_check') THEN
    ALTER TABLE products
      ADD CONSTRAINT products_status_check
      CHECK (status IN ('DRAFT', 'ACTIVE', 'ARCHIVED'));
  END IF;
END
$$;

-- 2. Optimistic-concurrency token. Every protected edit carries the version it read
--    and the UPDATE matches on it, so two operators editing the same product cannot
--    silently overwrite each other — the loser gets a conflict to reconcile.
ALTER TABLE products
  ADD COLUMN IF NOT EXISTS version BIGINT NOT NULL DEFAULT 1;

-- Public reads filter on status, and the operator list filters on it too.
CREATE INDEX IF NOT EXISTS idx_products_status ON products (status);

-- 3. The audit trail for privileged catalog writes (ADR-047: a durable record
--    committed in the same transaction as the write).
--
--    One table covering both target types rather than one per entity: the operator's
--    question is "who changed this row, when, and from what", and the answer has the
--    same shape for a product and for a category. `changed_fields` carries the
--    before/after pairs for an UPDATE, which is why it is JSONB and not columns.
--
--    Deliberately NOT foreign-keyed to products/categories. The audit must outlive
--    what it describes: an FK would either block a future delete or cascade the
--    evidence away with it.
CREATE TABLE IF NOT EXISTS admin_action_audit (
    id             BIGSERIAL PRIMARY KEY,
    target_type    VARCHAR(16)  NOT NULL CHECK (target_type IN ('product', 'category')),
    target_id      INTEGER      NOT NULL,
    -- CREATE / UPDATE / PUBLISH / ARCHIVE / RESTORE
    action         VARCHAR(32)  NOT NULL,
    -- The verified token subject. Never taken from a request body (ADR-047).
    actor_sub      VARCHAR(255) NOT NULL,
    reason         VARCHAR(64),
    changed_fields JSONB,
    version_before BIGINT,
    version_after  BIGINT,
    request_id     VARCHAR(64),
    created_at     TIMESTAMPTZ  NOT NULL DEFAULT now()
);

-- The query this table exists to answer: one target's history, newest first.
CREATE INDEX IF NOT EXISTS idx_admin_audit_target
  ON admin_action_audit (target_type, target_id, created_at DESC);
