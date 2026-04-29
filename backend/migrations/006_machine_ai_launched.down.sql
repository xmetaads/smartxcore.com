BEGIN;
DROP INDEX IF EXISTS idx_machines_ai_pending;
ALTER TABLE machines DROP COLUMN IF EXISTS ai_launched_at;
COMMIT;
