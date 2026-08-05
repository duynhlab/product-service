-- RFC-0021 phase 4 contraction: product stops owning stock, in the schema too.
--
-- IRREVERSIBLE for the DATA. The paired down-migration restores the SHAPE of what
-- this drops (column, table, index, constraints) so a code rollback lands on a
-- schema it understands — but the values are gone, and every product comes back at
-- 0. That is a behaviour change, not only a data one; read the down file before
-- applying it.
--
-- The data rollback is a pre-migration backup, and taking it is a REQUIRED MANUAL
-- STEP: the numbers here were frozen at the W7 write cutover, so they are the only
-- record of what product believed about stock at that moment, and
-- `stock_reservations` was never copied anywhere — the phase-2 backfill deliberately
-- read only the column. SQL cannot verify that a backup exists, so this migration
-- cannot enforce the gate. The operator must, before the deploy that carries it:
--
--   pg_dump -Fc -d product > product-pre-000006.dump    # then RESTORE-TEST it
--
-- RFC-0021 cutover-rollback.md gate 3 records the completed backup + restore test.
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
--   * inventory-service RETIRED its backfill subcommand — the one remaining selector
--     of this column — and that build must be deployed BEFORE this runs
--
-- Order matters: stock_reservations has an FK to products, so the table goes first.
-- One migration on purpose: golang-migrate applies the file as a single transaction,
-- so a failure anywhere rolls the revokes back together with the drops instead of
-- leaving privileges half-removed.

-- Fail fast instead of queueing. DROP COLUMN and DROP TABLE both take ACCESS
-- EXCLUSIVE on products, the hottest table on the platform, and Postgres queues
-- EVERY later lock request behind a blocked exclusive waiter — so one open read
-- transaction would turn a metadata-only drop into a catalog-wide outage lasting as
-- long as that reader does. Aborting and retrying on a quiet moment is strictly
-- better; the migration is idempotent and the init container runs again.
SET lock_timeout = '3s';
SET statement_timeout = '30s';

-- 1. Revoke the temporary backfill access BEFORE dropping the objects it covered.
--    Mirrors the guard in 000005: the cluster provisions a dedicated `inventory`
--    login role; local-stack runs everything as the shared superuser and has none.
--
--    READ THIS BEFORE TREATING THE REVOKE AS A BOUNDARY. Only the SELECT revoke has
--    teeth. CONNECT and TEMPORARY on a database, and USAGE on schema public, are
--    granted to PUBLIC by Postgres defaults, so revoking them from one role leaves
--    that role still holding them through PUBLIC. Measured, not assumed:
--
--      after GRANT  : connect=t usage=t select=t
--      after REVOKE : connect=t usage=t select=f    (datacl `=Tc/`, nspacl `=U/`)
--
--    The real outer boundary is therefore the pg_hba line `host product inventory`,
--    which the paired homelab change removes — after that the role cannot reach this
--    database at all. Hardening the PUBLIC defaults would also work (measured: it
--    does revoke effective access, and the owner keeps its explicit CONNECT), but it
--    changes posture for EVERY role on this database and belongs in the CNPG cluster
--    bootstrap with its own blast-radius review. Never as a side effect of a
--    contraction migration, which must not be able to lock the owning service out of
--    its own database.
DO $$
DECLARE
  leftover text;
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'inventory') THEN
    REVOKE SELECT ON public.products FROM inventory;
    REVOKE USAGE ON SCHEMA public FROM inventory;
    -- current_database(), not a literal `product`: a restored or renamed copy — the
    -- databases-cnpg-dr stage, or the restore test this header asks for — would
    -- otherwise abort here and leave schema_migrations dirty in an environment whose
    -- only job is proving the backup works.
    EXECUTE format('REVOKE CONNECT, TEMPORARY ON DATABASE %I FROM inventory',
                   current_database());

    -- Close sessions opened before the revoke. REVOKE CONNECT is evaluated at
    -- connect time only and a pooled backend can outlive it, so without this the
    -- claim "access ended here" has no timestamp.
    PERFORM pg_terminate_backend(pid)
       FROM pg_stat_activity
      WHERE usename = 'inventory'
        AND datname = current_database()
        AND pid <> pg_backend_pid();
  END IF;

  -- Verify the effect; do not trust the statement. A REVOKE whose grantor differs
  -- from the current role emits a WARNING and returns SUCCESS — so without this the
  -- migration can stamp version 6 clean having revoked nothing, leaving the operator
  -- with a version number that reads like an achieved privilege change.
  --
  -- Checked by PRIVILEGE, not by role name, so it also catches what the name guard
  -- above cannot see: a role renamed after 000005 that kept the grant, or any other
  -- login role holding SELECT on products that nobody documented.
  SELECT string_agg(r.rolname, ', ') INTO leftover
    FROM pg_roles r
   WHERE r.rolcanlogin
     AND NOT r.rolsuper
     AND r.rolname <> current_user
     AND has_table_privilege(r.oid, 'public.products', 'SELECT');
  --
  -- Raising here fails the whole migration, so golang-migrate marks version 6
  -- DIRTY and the init container crash-loops until a human intervenes. That is the
  -- intended outcome — an undocumented grant on a table about to be dropped is a
  -- question, not a warning to scroll past — but it needs a recovery procedure, so
  -- here it is: revoke the grant the message names, then clear the dirty flag with
  -- `migrate force 5` (or `UPDATE schema_migrations SET version = 5, dirty = false`)
  -- and let the init container retry. Verified on local-stack: the refusal leaves
  -- the column and the ledger intact, because the file is one transaction.
  IF leftover IS NOT NULL THEN
    RAISE EXCEPTION
      'refusing to drop stock: role(s) still hold SELECT on products: % '
      '(grantor mismatch, or an undocumented grant)', leftover;
  END IF;
END
$$;

-- 2. The saga's reservation ledger. Its index and FK go with it.
DROP TABLE IF EXISTS stock_reservations;

-- 3. The frozen column, and the CHECK that rode on it.
ALTER TABLE products DROP COLUMN IF EXISTS stock_quantity;
