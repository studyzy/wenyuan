-- 0015_drop_dead_operation_error (embedded copy of migrations/0015_drop_dead_operation_error.sql)
-- Drops the operation table's superseded error column.
--
-- Migration 0001 created an error column, and migration 0005 added
-- error_message alongside it. Every read and write has bound to error_message
-- since, so the original column has held nothing but dead weight. The reference
-- carries only error_message, so removing this one narrows the gap rather than
-- widening it, and no caller can observe the change because nothing selects it.

ALTER TABLE async_operations DROP COLUMN IF EXISTS error;
